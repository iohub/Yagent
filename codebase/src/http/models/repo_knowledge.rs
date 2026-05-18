use serde::{Deserialize, Serialize};

/// 知识向量化请求
#[derive(Debug, Deserialize)]
pub struct RepoKnowledgeEmbedRequest {
    pub task: String,
    pub result: String,
}

/// 向量化响应
#[derive(Debug, Serialize, Deserialize)]
pub struct EmbedResponse {
    pub id: String,
}

/// 知识搜索请求
#[derive(Debug, Deserialize)]
pub struct RepoKnowledgeSearchRequest {
    pub task: String,
    #[serde(default = "default_top_k")]
    pub top_k: usize,
}

fn default_top_k() -> usize {
    5
}

/// 知识搜索响应
#[derive(Debug, Serialize, Deserialize)]
pub struct RepoKnowledgeSearchResponse {
    pub matches: Vec<crate::services::RepoKnowledgeMatch>,
}

/// 清空所有知识请求
#[derive(Debug, Deserialize)]
pub struct ClearRepoKnowledgeRequest {}
