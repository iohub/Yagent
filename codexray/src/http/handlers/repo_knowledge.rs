use axum::{
    extract::State,
    Json,
    http::StatusCode as AxumStatusCode,
};
use crate::http::models::{
    ApiResponse, RepoKnowledgeEmbedRequest, RepoKnowledgeSearchRequest, RepoKnowledgeSearchResponse,
    EmbedResponse,
};
use crate::http::server::AppState;
use tracing::{info, error};

/// 添加知识条目
///
/// 接收 task 和 result，生成 task 的向量嵌入并存储到 LanceDB
pub async fn repo_knowledge_embed(
    State(storage): State<AppState>,
    Json(request): Json<RepoKnowledgeEmbedRequest>,
) -> Result<Json<ApiResponse<EmbedResponse>>, AxumStatusCode> {
    // 获取已初始化的 repo knowledge service
    let service = match storage.storage.get_repo_knowledge_service() {
        Ok(s) => s,
        Err(e) => {
            error!("Repo knowledge service not available: {}", e);
            return Err(AxumStatusCode::INTERNAL_SERVER_ERROR);
        }
    };

    // 添加知识并获取生成的 ID
    let id = match service.add_knowledge(&request.task, &request.result).await {
        Ok(id) => id,
        Err(e) => {
            error!("Failed to add repo knowledge: {}", e);
            return Err(AxumStatusCode::INTERNAL_SERVER_ERROR);
        }
    };

    info!("Added repo knowledge for task: {}", &request.task);

    Ok(Json(ApiResponse {
        success: true,
        data: EmbedResponse { id },
    }))
}

/// 知识相似性搜索
///
/// 使用任务描述搜索相似的历史分析结果
pub async fn repo_knowledge_search(
    State(storage): State<AppState>,
    Json(request): Json<RepoKnowledgeSearchRequest>,
) -> Result<Json<ApiResponse<RepoKnowledgeSearchResponse>>, AxumStatusCode> {
    // 获取已初始化的 repo knowledge service
    let service = match storage.storage.get_repo_knowledge_service() {
        Ok(s) => s,
        Err(e) => {
            error!("Repo knowledge service not available: {}", e);
            return Err(AxumStatusCode::INTERNAL_SERVER_ERROR);
        }
    };

    // 执行搜索
    let top_k = request.top_k;

    let matches = match service.search_similar(&request.task, top_k).await {
        Ok(results) => results,
        Err(e) => {
            error!("Repo knowledge search failed: {}", e);
            return Err(AxumStatusCode::INTERNAL_SERVER_ERROR);
        }
    };

    Ok(Json(ApiResponse {
        success: true,
        data: RepoKnowledgeSearchResponse {
            matches,
        },
    }))
}
