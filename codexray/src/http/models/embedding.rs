use serde::{Deserialize, Serialize};
use crate::services::embedding_service::SearchResult;

#[derive(Debug, Deserialize, Serialize)]
pub struct SemanticSearchRequest {
    pub text: String,
    pub limit: Option<usize>,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct SemanticSearchResponse {
    pub results: Vec<SearchResult>,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct QueryIndexingStatusResponse {
    pub status: String, // "indexing", "completed", "not_found", "failed"
    pub message: Option<String>,
}

use std::collections::HashMap;

#[derive(Serialize, Deserialize, Debug, Clone)]
pub struct ProjectInfo {
    pub repo_path: String,
    pub collection_name: String,
    pub status: String,
    pub last_updated: u64,
    pub file_hashes: HashMap<String, String>,
}
