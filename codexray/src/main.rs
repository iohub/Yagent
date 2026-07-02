use clap::Parser;
use codeactor_codexray::cli::{Cli, CodeXRayRunner};
use codeactor_codexray::cli::args::Commands;
use codeactor_codexray::http::CodeXRayServer;
use codeactor_codexray::storage::StorageManager;
use codeactor_codexray::config::Config;
use codeactor_codexray::shutdown::{ShutdownCoordinator, wait_for_shutdown_signal};
use std::sync::Arc;
use std::time::Duration;
use tracing::{info, warn};

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let cli = Cli::parse();

    // Initialize logging
    let filter_layer = tracing_subscriber::EnvFilter::try_from_default_env()
        .unwrap_or_else(|_| {
            if cli.verbose {
                tracing_subscriber::EnvFilter::new("debug")
            } else {
                tracing_subscriber::EnvFilter::new("info")
            }
        });

    tracing_subscriber::fmt()
        .with_env_filter(filter_layer)
        .init();

    // Create shutdown coordinator (MUST be created early for signal handler)
    let shutdown = ShutdownCoordinator::new();
    info!("Initializing graceful shutdown handler (SIGTERM/SIGINT)");

    // ── Signal Handler ──────────────────────────────────────────
    // First signal (SIGTERM/SIGINT): trigger graceful shutdown
    // Second signal: force immediate exit
    {
        let shutdown_for_signal = shutdown.clone();
        tokio::spawn(async move {
            // Wait for first signal
            wait_for_shutdown_signal().await;
            shutdown_for_signal.trigger();

            // Wait for second signal — if received, force exit
            wait_for_shutdown_signal().await;
            warn!("Second shutdown signal received, forcing immediate exit");
            std::process::exit(1);
        });
    }

    // Load configuration
    let config = match Config::load() {
        Ok(c) => Some(c),
        Err(e) => {
            warn!("Failed to load configuration: {}", e);
            None
        }
    };

    match &cli.command {
        Commands::Server { address, storage_mode, repo_path } => {
            let default_addr = format!("127.0.0.1:{}", 12700);
            let server_addr = address.as_deref().unwrap_or(&default_addr);
            info!("Starting CodeXRay HTTP server on {}, repo: {}", server_addr, repo_path);

            // Determine storage mode
            let storage_mode = storage_mode.as_ref().unwrap_or(&cli.storage_mode).clone();
            info!("Using storage mode: {:?}", storage_mode);

            let storage = if let Some(cfg) = config {
                Arc::new(StorageManager::with_config(storage_mode, cfg))
            } else {
                Arc::new(StorageManager::with_storage_mode(storage_mode))
            };

            let mut server = CodeXRayServer::new(storage, repo_path.clone(), shutdown.clone());
            server.start(server_addr).await?;

            // ── Post-Server Cleanup ─────────────────────────────────
            // At this point, axum has stopped accepting new connections
            // and all in-flight requests have been drained.
            // Now we wait for any remaining LanceDB compaction operations.
            info!("HTTP server stopped, waiting for LanceDB compaction to drain...");
            let drained = shutdown
                .wait_for_compaction(Duration::from_secs(30))
                .await;
            
            if drained {
                info!("All LanceDB compaction operations completed safely");
            } else {
                warn!(
                    "Some compaction operations did not complete within timeout. \
                     LanceDB auto-repair will attempt recovery on next startup if needed."
                );
            }

            info!("CodeXRay shutdown complete. Goodbye!");
        }
        Commands::Vectorize { .. } => {
            CodeXRayRunner::run(cli, config).await?;
        }
    }

    Ok(())
}
