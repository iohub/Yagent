use axum::{
    extract::State,
    Json,
    http::StatusCode,
};
use std::sync::Arc;
use crate::storage::StorageManager;
use crate::storage::TantivyBm25Index;
use crate::services::embedding_service::EmbeddingService;
use crate::storage::traits_bm25::CandidateSource;
use crate::http::models::{ApiResponse, SemanticSearchRequest, SemanticSearchResponse, QueryIndexingStatusResponse, ProjectInfo};
use crate::http::server::AppState;
use std::time::{SystemTime, UNIX_EPOCH};
use std::collections::HashMap;
use md5;

pub async fn trigger_embedding_build(
    storage: Arc<StorageManager>,
    repo_path: String,
) -> Result<(), String> {
    // Check and set lock
    {
        let mut tasks = storage.vector_tasks.lock().unwrap();
        if tasks.contains(&repo_path) {
            return Err("Task already running for this repo".to_string());
        }
        tasks.insert(repo_path.clone());
    }

    // Get config
    let config = match storage.get_config() {
        Some(c) => c,
        None => {
             let mut tasks = storage.vector_tasks.lock().unwrap();
             tasks.remove(&repo_path);
             return Err("Config not found".to_string());
        }
    };

    // Check if embedding is enabled
    if !config.codebase.enable_embedding {
        let mut tasks = storage.vector_tasks.lock().unwrap();
        tasks.remove(&repo_path);
        return Err("Embedding is not enabled".to_string());
    }

    let db_path = config.codebase.embedding_db_uri.clone();

    let storage_clone = storage.clone();
    let repo_path_clone = repo_path.clone();
    let db_path_clone = db_path.clone();
    let config_clone = config.clone();

    // Spawn background task
    tokio::spawn(async move {
        let result = async {
            // Calculate collection name: last_dir_md5(repo_path)
            let path = std::path::Path::new(&repo_path_clone);
            let last_dir = path.file_name()
                .and_then(|n| n.to_str())
                .unwrap_or("unknown");
            let hash = md5::compute(&repo_path_clone);
            let collection = format!("{}_{:x}", last_dir, hash);

            // Create Tantivy BM25 index alongside EmbeddingService
            let tantivy_dir = std::path::Path::new(&db_path_clone).join("tantivy_bm25");
            let bm25_index = match TantivyBm25Index::open_or_create(&tantivy_dir) {
                Ok(idx) => {
                    tracing::info!("Tantivy BM25 index ready at {:?}", tantivy_dir);
                    Some(Arc::new(idx) as Arc<dyn crate::storage::traits_bm25::TextSearchProvider>)
                }
                Err(e) => {
                    tracing::warn!("Failed to create Tantivy index (search will fall back to dense-only): {}", e);
                    None
                }
            };

            // Create service and run vectorization
            let service = EmbeddingService::new(&db_path_clone, collection.clone(), Some(&config_clone), bm25_index).await
                .map_err(|e| format!("Failed to create vectorize service: {}", e))?;

            // Ensure collection exists
            service.ensure_collection().await
                .map_err(|e| format!("Failed to ensure collection: {}", e))?;

            // Read existing project info to get file hashes
            let projects_path = std::path::Path::new(&db_path_clone).join("projects.json");
            let mut existing_hashes = None;

            if projects_path.exists() {
                if let Ok(content) = tokio::fs::read_to_string(&projects_path).await {
                    if let Ok(projects) = serde_json::from_str::<HashMap<String, ProjectInfo>>(&content) {
                        if let Some(info) = projects.get(&repo_path_clone) {
                            existing_hashes = Some(info.file_hashes.clone());
                        }
                    }
                }
            }

            // Vectorize directory
            let new_hashes = service.vectorize_directory(&repo_path_clone, existing_hashes.as_ref()).await
                .map_err(|e| format!("Vectorization failed: {}", e))?;

            // Update projects.json
            let info = ProjectInfo {
                repo_path: repo_path_clone.clone(),
                collection_name: collection,
                status: "completed".to_string(),
                last_updated: SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_secs(),
                file_hashes: new_hashes,
            };
            update_project_status(&db_path_clone, info).await
                .map_err(|e| format!("Failed to update project status: {}", e))?;

            Ok::<(), String>(())
        }.await;

        if let Err(e) = result {
            tracing::error!("Embedding task failed for {}: {}", repo_path_clone, e);
        }

        // Remove from tasks
        let mut tasks = storage_clone.vector_tasks.lock().unwrap();
        tasks.remove(&repo_path_clone);
    });

    Ok(())
}

async fn update_project_status(db_path: &str, info: ProjectInfo) -> Result<(), Box<dyn std::error::Error + Send + Sync>> {
    let projects_path = std::path::Path::new(db_path).join("projects.json");

    let content = if projects_path.exists() {
        tokio::fs::read_to_string(&projects_path).await?
    } else {
        "{}".to_string()
    };

    let mut projects: HashMap<String, ProjectInfo> = serde_json::from_str(&content).unwrap_or_default();

    projects.insert(info.repo_path.clone(), info);

    let new_content = serde_json::to_string_pretty(&projects)?;
    tokio::fs::write(&projects_path, new_content).await?;

    Ok(())
}

pub async fn query_indexing_status(
    State(storage): State<AppState>,
) -> Result<Json<ApiResponse<QueryIndexingStatusResponse>>, StatusCode> {
    // 使用当前绑定的仓库
    let repo_path = match storage.storage.get_current_repo() {
        Some(p) => p,
        None => {
            return Ok(Json(ApiResponse {
                success: true,
                data: QueryIndexingStatusResponse {
                    status: "not_found".to_string(),
                    message: Some("No repo bound to this process".to_string()),
                },
            }));
        }
    };

    // Check running tasks
    {
        let tasks = storage.storage.vector_tasks.lock().unwrap();
        if tasks.contains(&repo_path) {
            return Ok(Json(ApiResponse {
                success: true,
                data: QueryIndexingStatusResponse {
                    status: "indexing".to_string(),
                    message: Some("Indexing is in progress".to_string()),
                }
            }));
        }
    }

    // Check projects.json
    let config = storage.storage.get_config().ok_or(StatusCode::INTERNAL_SERVER_ERROR)?;
    let db_path = config.codebase.embedding_db_uri;
    let projects_path = std::path::Path::new(&db_path).join("projects.json");

    if projects_path.exists() {
        if let Ok(content) = tokio::fs::read_to_string(&projects_path).await {
            if let Ok(projects) = serde_json::from_str::<HashMap<String, ProjectInfo>>(&content) {
                if let Some(info) = projects.get(&repo_path) {
                     return Ok(Json(ApiResponse {
                        success: true,
                        data: QueryIndexingStatusResponse {
                            status: info.status.clone(),
                            message: Some(format!("Last updated at {}", info.last_updated)),
                        }
                    }));
                }
            }
        }
    }

    Ok(Json(ApiResponse {
        success: true,
        data: QueryIndexingStatusResponse {
            status: "not_found".to_string(),
            message: Some("Project not found in index".to_string()),
        }
    }))
}

#[axum::debug_handler]
pub async fn semantic_search(
    State(state): State<AppState>,
    Json(request): Json<SemanticSearchRequest>,
) -> Result<Json<ApiResponse<SemanticSearchResponse>>, StatusCode> {
    // 获取 hybrid service，如果不存在则返回 503
    let hybrid = state.hybrid.as_ref().ok_or(StatusCode::SERVICE_UNAVAILABLE)?;
    
    // 1. 执行混合搜索
    let limit = request.limit.unwrap_or(10);
    let fused_candidates = hybrid.search(&request.text, limit).await
        .map_err(|e| {
            tracing::error!("Hybrid search failed: {}", e);
            StatusCode::INTERNAL_SERVER_ERROR
        })?;

    // 2. 转换结果为 SemanticSearchResponse 格式（向后兼容）
    let results: Vec<crate::services::embedding_service::SearchResult> = fused_candidates
        .into_iter()
        .map(|c| crate::services::embedding_service::SearchResult {
            file_path: c.file_path,
            symbol_name: c.symbol_name,
            code_block: c.code_block,
            semantic_distance: if c.source == CandidateSource::DenseOnly {
                c.final_score as f32
            } else {
                c.final_score as f32
            },
            symbol_type: c.symbol_type,
            language: c.language,
            line_start: c.line_start,
            line_end: c.line_end,
        })
        .collect();

    // 3. 返回结果
    Ok(Json(ApiResponse {
        success: true,
        data: SemanticSearchResponse { results },
    }))
}
