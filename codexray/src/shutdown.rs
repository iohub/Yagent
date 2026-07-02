use std::sync::atomic::{AtomicBool, AtomicUsize, Ordering};
use std::sync::Arc;
use std::time::Duration;

use tokio_util::sync::CancellationToken;

/// Central coordinator for graceful shutdown.
/// Shared across all components via Arc.
pub struct ShutdownCoordinator {
    cancel_token: CancellationToken,
    shutdown_triggered: AtomicBool,
    compaction_counter: Arc<CompactionCounter>,
}

/// Tracks in-flight compaction operations.
/// When shutdown is triggered, we wait for all active compactions to complete
/// before allowing the process to exit, ensuring LanceDB data consistency.
struct CompactionCounter {
    active_count: AtomicUsize,
}

/// RAII guard: increments count on creation, decrements on drop.
/// Hold this guard for the entire duration of a compaction operation.
pub struct CompactionGuard {
    counter: Arc<CompactionCounter>,
}

impl ShutdownCoordinator {
    pub fn new() -> Arc<Self> {
        Arc::new(Self {
            cancel_token: CancellationToken::new(),
            shutdown_triggered: AtomicBool::new(false),
            compaction_counter: Arc::new(CompactionCounter {
                active_count: AtomicUsize::new(0),
            }),
        })
    }

    /// Trigger graceful shutdown. Safe to call multiple times (idempotent).
    pub fn trigger(&self) {
        if self
            .shutdown_triggered
            .compare_exchange(false, true, Ordering::SeqCst, Ordering::SeqCst)
            .is_ok()
        {
            tracing::info!("🚦 Graceful shutdown triggered, cancelling all background tasks");
            self.cancel_token.cancel();
        }
    }

    /// Check if shutdown is in progress.
    pub fn is_shutting_down(&self) -> bool {
        self.shutdown_triggered.load(Ordering::SeqCst)
    }

    /// Get the cancellation token for background tasks.
    pub fn cancel_token(&self) -> CancellationToken {
        self.cancel_token.clone()
    }

    /// Start tracking a compaction operation. Returns a RAII guard.
    /// The guard MUST be held for the entire compaction duration.
    pub fn start_compaction(&self) -> CompactionGuard {
        self.compaction_counter
            .active_count
            .fetch_add(1, Ordering::Release);
        CompactionGuard {
            counter: self.compaction_counter.clone(),
        }
    }

    /// Wait for all in-flight compaction operations to complete.
    /// Returns true if all compactions completed within timeout, false otherwise.
    pub async fn wait_for_compaction(&self, timeout: Duration) -> bool {
        let deadline = tokio::time::Instant::now() + timeout;
        loop {
            let count = self.compaction_counter.active_count.load(Ordering::Acquire);
            if count == 0 {
                return true;
            }
            if tokio::time::Instant::now() >= deadline {
                tracing::warn!(
                    "Timed out waiting for {} compaction operation(s) to complete",
                    count
                );
                return false;
            }
            tokio::time::sleep(Duration::from_millis(100)).await;
        }
    }
}

impl Drop for CompactionGuard {
    fn drop(&mut self) {
        let prev = self.counter.active_count.fetch_sub(1, Ordering::Release);
        tracing::debug!("Compaction completed, {} remaining", prev - 1);
    }
}

/// Wait for SIGTERM or SIGINT signal.
pub async fn wait_for_shutdown_signal() {
    let ctrl_c = async {
        tokio::signal::ctrl_c()
            .await
            .expect("Failed to install Ctrl+C handler");
    };

    #[cfg(unix)]
    let terminate = async {
        let mut signal = tokio::signal::unix::signal(
            tokio::signal::unix::SignalKind::terminate(),
        )
        .expect("Failed to install SIGTERM handler");
        signal.recv().await;
    };

    #[cfg(not(unix))]
    let terminate = std::future::pending::<()>();

    tokio::select! {
        _ = ctrl_c => {},
        _ = terminate => {},
    }
}
