use serde::{Deserialize, Serialize};
use crate::services::embedding_service::SearchResult;

#[derive(Deserialize)]
pub struct SemanticSearchRequest {
    pub text: String,
    pub limit: Option<usize>,
}

#[derive(Serialize)]
pub struct SemanticSearchResponse {
    pub results: Vec<SearchResult>,
}

#[derive(Serialize)]
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
