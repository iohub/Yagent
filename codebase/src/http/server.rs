use axum::{
    routing::{post, get},
    Router,
    response::Json,
};
use std::sync::Arc;
use std::time::Duration;
use tokio::net::TcpListener;
use tower_http::cors::CorsLayer;
use tower_http::trace::TraceLayer;
use tower_http::classify::ServerErrorsFailureClass;
use axum::extract::Request;
use axum::response::Response;
use tracing::Span;
use crate::storage::StorageManager;

use super::{
    handlers::{query_call_graph, query_code_snippet, query_code_skeleton,
         query_hierarchical_graph, draw_call_graph, draw_call_graph_home,
         investigate_repo, semantic_search, query_indexing_status,
         perform_analysis, setup_watcher, trigger_embedding_build,
         commit_embed, commit_search, commit_clear},
    models::ApiResponse,
};

pub struct CodeBaseServer {
    storage: Arc<StorageManager>,
    repo_path: String,
}

#[derive(serde::Serialize)]
struct StatusResponse {
    repo_path: String,
    project_id: String,
    total_functions: usize,
    total_files: usize,
    embedding_enabled: bool,
    indexing_status: String,
}

impl CodeBaseServer {
    pub fn new(storage: Arc<StorageManager>, repo_path: String) -> Self {
        Self { storage, repo_path }
    }

    pub async fn start(self, addr: &str) -> Result<(), Box<dyn std::error::Error>> {
        // ---- 启动时自动初始化仓库 ----
        let project_dir = std::path::Path::new(&self.repo_path);
        if !project_dir.exists() || !project_dir.is_dir() {
            return Err(format!("Repository path does not exist or is not a directory: {}", self.repo_path).into());
        }

        // 绑定当前进程到该仓库
        if let Err(existing) = self.storage.try_bind_repo(&self.repo_path) {
            return Err(format!("Process already bound to repo '{}'", existing).into());
        }

        let project_id = format!("{:x}", md5::compute(self.repo_path.as_bytes()));
        tracing::info!("Initializing repo: {} (project_id: {})", self.repo_path, project_id);

        // 加载已有图谱或执行分析
        match self.storage.get_persistence().load_graph(&project_id) {
            Ok(Some(graph)) => {
                let stats = graph.get_stats().clone();
                self.storage.set_graph(graph);
                tracing::info!("Loaded cached graph: {} functions, {} files", stats.total_functions, stats.total_files);
            }
            Ok(None) => {
                tracing::info!("No cached graph found, performing analysis...");
                match perform_analysis(
                    self.storage.clone(),
                    project_dir.to_path_buf(),
                    project_id.clone(),
                ).await {
                    Ok(resp) => {
                        tracing::info!("Analysis complete: {} functions, {} files", resp.total_functions, resp.total_files);
                    }
                    Err(e) => {
                        tracing::error!("Analysis failed: {:?}", e);
                        return Err("Failed to analyze repository".into());
                    }
                }
            }
            Err(e) => {
                tracing::error!("Failed to load graph: {}", e);
                return Err(e.into());
            }
        }

        // 触发嵌入索引构建
        if let Err(e) = trigger_embedding_build(self.storage.clone(), self.repo_path.clone()).await {
            tracing::info!("Embedding build skipped: {}", e);
        }

        // 启动文件监听
        setup_watcher(self.storage.clone(), project_dir.to_path_buf(), project_id.clone());

        // ---- 启动 HTTP 服务器 ----
        let app = self.create_router();

        let listener = TcpListener::bind(addr).await?;
        println!("CodeGraph HTTP server starting on {}, repo: {}", addr, self.repo_path);

        axum::serve(listener, app).await?;
        Ok(())
    }

    fn create_router(&self) -> Router {
        let cors = CorsLayer::permissive();
        
        // 创建 HTTP 请求日志中间件
        let request_logging = TraceLayer::new_for_http()
            .make_span_with(|request: &Request| {
                let method = request.method().to_string();
                let path = request.uri().path().to_string();
                let query = request.uri().query().unwrap_or("");
                
                tracing::info_span!(
                    "http_request",
                    method = %method,
                    path = %path,
                    query = %query,
                )
            })
            .on_request(|request: &Request, _span: &Span| {
                let method = request.method().to_string();
                let path = request.uri().path().to_string();
                let client_ip = request
                    .headers()
                    .get("x-forwarded-for")
                    .map(|v| v.to_str().unwrap_or(""))
                    .unwrap_or("");
                
                tracing::info!(
                    method = method,
                    path = path,
                    client_ip = %client_ip,
                    "Request started"
                );
            })
            .on_response(|response: &Response, latency: Duration, _span: &Span| {
                let status = response.status().as_u16();
                let latency_ms = latency.as_millis();
                
                if status >= 500 {
                    tracing::error!(
                        status = status,
                        latency_ms = latency_ms,
                        "Request completed (server error)"
                    );
                } else if status >= 400 {
                    tracing::warn!(
                        status = status,
                        latency_ms = latency_ms,
                        "Request completed (client error)"
                    );
                } else {
                    tracing::info!(
                        status = status,
                        latency_ms = latency_ms,
                        "Request completed (success)"
                    );
                }
            })
            .on_failure(|failure: ServerErrorsFailureClass, latency: Duration, _span: &Span| {
                let latency_ms = latency.as_millis();
                match &failure {
                    ServerErrorsFailureClass::Error(error) => {
                        tracing::error!(
                            error = %error,
                            latency_ms = latency_ms,
                            "Server error occurred"
                        );
                    }
                    ServerErrorsFailureClass::StatusCode(status) => {
                        tracing::error!(
                            status = %status,
                            latency_ms = latency_ms,
                            "Request failed with status code"
                        );
                    }
                }
            });
        
        Router::new()
            .route("/health", get(health_check))
            .route("/status", get(get_status))
            .route("/query_call_graph", post(query_call_graph))
            .route("/query_code_snippet", post(query_code_snippet))
            .route("/query_code_skeleton", post(query_code_skeleton))
            .route("/query_hierarchical_graph", post(query_hierarchical_graph))
            .route("/investigate_repo", post(investigate_repo))
            .route("/semantic_search", post(semantic_search))
            .route("/query_indexing_status", post(query_indexing_status))
            .route("/commit/embed", post(commit_embed))
            .route("/commit/search", post(commit_search))
            .route("/commit/clear", post(commit_clear))
            .route("/", get(draw_call_graph_home))
            .route("/draw_call_graph", get(draw_call_graph))
            // 中间件顺序：先应用日志，再应用 CORS
            .layer(request_logging)
            .layer(cors)
            .with_state(self.storage.clone())
    }
}

// Health check endpoint
async fn health_check() -> Json<ApiResponse<&'static str>> {
    Json(ApiResponse {
        success: true,
        data: "Codebase HTTP service is running",
    })
}

// Status endpoint - returns info about the currently indexed repo
async fn get_status(
    axum::extract::State(storage): axum::extract::State<Arc<StorageManager>>,
) -> Json<ApiResponse<StatusResponse>> {
    let repo_path = storage.get_current_repo().unwrap_or_default();
    let project_id = format!("{:x}", md5::compute(&repo_path));

    let (total_functions, total_files) = storage
        .get_graph_clone()
        .map(|g| {
            let stats = g.get_stats();
            (stats.total_functions, stats.total_files)
        })
        .unwrap_or((0, 0));

    let embedding_enabled = storage
        .get_config()
        .map(|c| c.codebase.enable_embedding)
        .unwrap_or(false);

    let indexing_status = {
        let tasks = storage.vector_tasks.lock().unwrap();
        if tasks.contains(&repo_path) {
            "indexing".to_string()
        } else {
            "idle".to_string()
        }
    };

    Json(ApiResponse {
        success: true,
        data: StatusResponse {
            repo_path,
            project_id,
            total_functions,
            total_files,
            embedding_enabled,
            indexing_status,
        },
    })
}
