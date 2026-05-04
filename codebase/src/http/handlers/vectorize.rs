use axum::{
    extract::State,
    Json,
    http::StatusCode as AxumStatusCode,
};
use std::sync::Arc;
use crate::storage::StorageManager;
use crate::services::embedding_service::EmbeddingService;
use crate::http::models::{ApiResponse, SemanticSearchRequest, SemanticSearchResponse, QueryIndexingStatusResponse, ProjectInfo};
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

            // Create service and run vectorization
            let service = EmbeddingService::new(&db_path_clone, collection.clone(), Some(&config_clone)).await
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
    State(storage): State<Arc<StorageManager>>,
) -> Result<Json<ApiResponse<QueryIndexingStatusResponse>>, AxumStatusCode> {
    // 使用当前绑定的仓库
    let repo_path = match storage.get_current_repo() {
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
        let tasks = storage.vector_tasks.lock().unwrap();
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
    let config = storage.get_config().ok_or(AxumStatusCode::INTERNAL_SERVER_ERROR)?;
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

pub async fn semantic_search(
    State(storage): State<Arc<StorageManager>>,
    Json(request): Json<SemanticSearchRequest>,
) -> Result<Json<ApiResponse<SemanticSearchResponse>>, AxumStatusCode> {
    // Get config
    let config = storage.get_config().ok_or(AxumStatusCode::INTERNAL_SERVER_ERROR)?;
    let db_path = config.codebase.embedding_db_uri.clone();

    // 使用当前绑定的仓库
    let repo_path = storage.get_current_repo().ok_or(AxumStatusCode::BAD_REQUEST)?;

    // Check index status
    let projects_path = std::path::Path::new(&db_path).join("projects.json");
    let mut is_indexed = false;

    if projects_path.exists() {
         if let Ok(content) = tokio::fs::read_to_string(&projects_path).await {
            if let Ok(projects) = serde_json::from_str::<HashMap<String, ProjectInfo>>(&content) {
                if let Some(info) = projects.get(&repo_path) {
                    if info.status == "completed" {
                        is_indexed = true;
                    }
                }
            }
         }
    }

    if !is_indexed {
        tracing::warn!("Index not ready for repo: {}", repo_path);
        return Err(AxumStatusCode::NOT_FOUND);
    }

    let path = std::path::Path::new(&repo_path);
    let last_dir = path.file_name()
        .and_then(|n| n.to_str())
        .unwrap_or("unknown");
    let hash = md5::compute(&repo_path);
    let collection = format!("{}_{:x}", last_dir, hash);

    // Create service
    let service = EmbeddingService::new(&db_path, collection, Some(&config)).await
        .map_err(|e| {
            tracing::error!("Failed to create vectorize service: {}", e);
            AxumStatusCode::INTERNAL_SERVER_ERROR
        })?;

    let limit = request.limit.unwrap_or(10);

    // Search
    let results = service.search(&request.text, limit).await
        .map_err(|e| {
            tracing::error!("Search failed: {}", e);
            AxumStatusCode::INTERNAL_SERVER_ERROR
        })?;

    Ok(Json(ApiResponse {
        success: true,
        data: SemanticSearchResponse { results }
    }))
}
