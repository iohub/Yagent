use axum::{
    extract::State,
    Json,
    http::StatusCode as AxumStatusCode,
};
use std::sync::Arc;
use crate::storage::StorageManager;
use crate::services::commit_embedding_service::CommitEmbeddingService;
use crate::http::models::{
    ApiResponse, CommitEmbedRequest, CommitSearchRequest, CommitSearchResponse,
    CommitMatch, ClearCommitsRequest,
};
use md5;
use tracing::{info, error};

/// 为当前仓库生成 commit embeddings 表的集合名称
fn get_commit_collection_name(repo_path: &str) -> String {
    let path = std::path::Path::new(repo_path);
    let last_dir = path.file_name()
        .and_then(|n| n.to_str())
        .unwrap_or("unknown");
    let hash = md5::compute(repo_path);
    format!("{}_{:x}_commits", last_dir, hash)
}

/// Commit 向量化处理
///
/// 接收 commit hash 和 summary text，生成向量嵌入并存储到 LanceDB
pub async fn commit_embed(
    State(storage): State<Arc<StorageManager>>,
    Json(request): Json<CommitEmbedRequest>,
) -> Result<Json<ApiResponse<()>>, AxumStatusCode> {
    // 获取配置
    let config = storage.get_config().ok_or(AxumStatusCode::INTERNAL_SERVER_ERROR)?;
    
    // 检查 embedding 是否启用
    if !config.codebase.enable_embedding {
        return Err(AxumStatusCode::BAD_REQUEST);
    }

    let db_path = config.codebase.embedding_db_uri.clone();
    
    // 获取当前绑定的仓库路径
    let repo_path = storage.get_current_repo().ok_or(AxumStatusCode::BAD_REQUEST)?;
    let collection_name = get_commit_collection_name(&repo_path);

    // 创建或获取 commit embedding service
    let service = match CommitEmbeddingService::from_config(&db_path, collection_name, Some(&config)).await {
        Ok(s) => s,
        Err(e) => {
            error!("Failed to create commit embedding service: {}", e);
            return Err(AxumStatusCode::INTERNAL_SERVER_ERROR);
        }
    };

    // 确保表存在
    if let Err(e) = service.init_table().await {
        error!("Failed to ensure commit embeddings table: {}", e);
        return Err(AxumStatusCode::INTERNAL_SERVER_ERROR);
    }

    // 添加 commit
    if let Err(e) = service.add_commit(&request.commit_hash, &request.summary_text).await {
        error!("Failed to add commit embedding: {}", e);
        return Err(AxumStatusCode::INTERNAL_SERVER_ERROR);
    }

    info!("Added commit embedding: {} ({})", &request.commit_hash, &request.summary_text);

    Ok(Json(ApiResponse {
        success: true,
        data: (),
    }))
}

/// Commit 相似性搜索
///
/// 使用查询文本搜索相似的 commit
pub async fn commit_search(
    State(storage): State<Arc<StorageManager>>,
    Json(request): Json<CommitSearchRequest>,
) -> Result<Json<ApiResponse<CommitSearchResponse>>, AxumStatusCode> {
    // 获取配置
    let config = storage.get_config().ok_or(AxumStatusCode::INTERNAL_SERVER_ERROR)?;
    
    // 检查 embedding 是否启用
    if !config.codebase.enable_embedding {
        return Err(AxumStatusCode::BAD_REQUEST);
    }

    let db_path = config.codebase.embedding_db_uri.clone();
    
    // 获取当前绑定的仓库路径
    let repo_path = storage.get_current_repo().ok_or(AxumStatusCode::BAD_REQUEST)?;
    let collection_name = get_commit_collection_name(&repo_path);

    // 创建 commit embedding service
    let service = match CommitEmbeddingService::from_config(&db_path, collection_name, Some(&config)).await {
        Ok(s) => s,
        Err(e) => {
            error!("Failed to create commit embedding service: {}", e);
            return Err(AxumStatusCode::INTERNAL_SERVER_ERROR);
        }
    };

    // 确保表存在
    if let Err(e) = service.init_table().await {
        error!("Failed to ensure commit embeddings table: {}", e);
        return Err(AxumStatusCode::INTERNAL_SERVER_ERROR);
    }

    // 执行搜索
    let top_k = request.top_k.unwrap_or(10);
    
    let matches = match service.search_similar(&request.query, top_k).await {
        Ok(results) => results,
        Err(e) => {
            error!("Commit search failed: {}", e);
            return Err(AxumStatusCode::INTERNAL_SERVER_ERROR);
        }
    };

    // 转换为公开响应格式
    let matches_response: Vec<CommitMatch> = matches.into_iter().map(|m| CommitMatch {
        commit_hash: m.commit_hash,
        summary_text: m.summary_text,
        similarity: m.similarity,
    }).collect();

    Ok(Json(ApiResponse {
        success: true,
        data: CommitSearchResponse {
            matches: matches_response,
        },
    }))
}

/// 清空所有 commit 向量数据
///
/// 删除 LanceDB 中存储的所有 commit 嵌入
pub async fn commit_clear(
    State(storage): State<Arc<StorageManager>>,
    Json(_request): Json<ClearCommitsRequest>,
) -> Result<Json<ApiResponse<()>>, AxumStatusCode> {
    // 获取配置
    let config = storage.get_config().ok_or(AxumStatusCode::INTERNAL_SERVER_ERROR)?;
    
    // 检查 embedding 是否启用
    if !config.codebase.enable_embedding {
        return Err(AxumStatusCode::BAD_REQUEST);
    }

    let db_path = config.codebase.embedding_db_uri.clone();
    
    // 获取当前绑定的仓库路径
    let repo_path = storage.get_current_repo().ok_or(AxumStatusCode::BAD_REQUEST)?;
    let collection_name = get_commit_collection_name(&repo_path);

    // 创建 commit embedding service
    let service = match CommitEmbeddingService::from_config(&db_path, collection_name, Some(&config)).await {
        Ok(s) => s,
        Err(e) => {
            error!("Failed to create commit embedding service: {}", e);
            return Err(AxumStatusCode::INTERNAL_SERVER_ERROR);
        }
    };

    // 清空数据
    if let Err(e) = service.clear_all().await {
        error!("Failed to clear commit embeddings: {}", e);
        return Err(AxumStatusCode::INTERNAL_SERVER_ERROR);
    }

    info!("Cleared all commit embeddings for repo: {}", repo_path);

    Ok(Json(ApiResponse {
        success: true,
        data: (),
    }))
}
