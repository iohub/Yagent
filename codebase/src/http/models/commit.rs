use serde::{Deserialize, Serialize};

/// 单个 commit 向量化请求
#[derive(Debug, Deserialize)]
pub struct CommitEmbedRequest {
    pub commit_hash: String,
    pub summary_text: String,
}

/// 批量 commit 向量化请求
#[derive(Debug, Deserialize)]
pub struct BatchCommitEmbedRequest {
    pub commits: Vec<CommitEmbedRequest>,
}

/// Commit 相似性搜索请求
#[derive(Debug, Deserialize)]
pub struct CommitSearchRequest {
    pub query: String,
    pub top_k: Option<usize>,
    pub threshold: Option<f32>,
}

/// Commit 相似性搜索响应
#[derive(Debug, Serialize, Deserialize)]
pub struct CommitSearchResponse {
    pub matches: Vec<CommitMatch>,
}

/// Commit 匹配结果
#[derive(Debug, Serialize, Deserialize, Clone)]
pub struct CommitMatch {
    pub commit_hash: String,
    pub summary_text: String,
    pub similarity: f32,
}

/// 清空所有 commit 向量化数据请求
#[derive(Debug, Deserialize)]
pub struct ClearCommitsRequest {}
