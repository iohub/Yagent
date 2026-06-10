use axum::{
    extract::State,
    Json,
    http::StatusCode as AxumStatusCode,
};
use crate::http::models::{
    ApiResponse, CommitEmbedRequest, CommitSearchRequest, CommitSearchResponse,
    CommitMatch, ClearCommitsRequest,
};
use crate::http::server::AppState;
use tracing::{info, error};

/// Commit 向量化处理
///
/// 接收 commit hash 和 summary text，生成向量嵌入并存储到 LanceDB
pub async fn commit_embed(
    State(storage): State<AppState>,
    Json(request): Json<CommitEmbedRequest>,
) -> Result<Json<ApiResponse<()>>, AxumStatusCode> {
    // 获取已初始化的 commit embedding service
    let service = match storage.storage.get_commit_embedding_service() {
        Ok(s) => s,
        Err(e) => {
            error!("Commit embedding service not available: {}", e);
            return Err(AxumStatusCode::INTERNAL_SERVER_ERROR);
        }
    };

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
    State(storage): State<AppState>,
    Json(request): Json<CommitSearchRequest>,
) -> Result<Json<ApiResponse<CommitSearchResponse>>, AxumStatusCode> {
    // 获取已初始化的 commit embedding service
    let service = match storage.storage.get_commit_embedding_service() {
        Ok(s) => s,
        Err(e) => {
            error!("Commit embedding service not available: {}", e);
            return Err(AxumStatusCode::INTERNAL_SERVER_ERROR);
        }
    };

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
    State(storage): State<AppState>,
    Json(_request): Json<ClearCommitsRequest>,
) -> Result<Json<ApiResponse<()>>, AxumStatusCode> {
    // 获取已初始化的 commit embedding service
    let service = match storage.storage.get_commit_embedding_service() {
        Ok(s) => s,
        Err(e) => {
            error!("Commit embedding service not available: {}", e);
            return Err(AxumStatusCode::INTERNAL_SERVER_ERROR);
        }
    };

    // 清空数据
    if let Err(e) = service.clear_all().await {
        error!("Failed to clear commit embeddings: {}", e);
        return Err(AxumStatusCode::INTERNAL_SERVER_ERROR);
    }

    let repo_path = storage.storage.get_current_repo().unwrap_or_default();
    info!("Cleared all commit embeddings for repo: {}", repo_path);

    Ok(Json(ApiResponse {
        success: true,
        data: (),
    }))
}
