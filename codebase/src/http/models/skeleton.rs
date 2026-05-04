use serde::{Deserialize, Serialize};

#[derive(Debug, Deserialize)]
pub struct QueryCodeSkeletonRequest {
    pub filepaths: Vec<String>,
}

#[derive(Debug, Serialize)]
pub struct CodeSkeletonResponse {
    pub filepath: String,
    pub language: String,
    pub skeleton_text: String,
}

#[derive(Debug, Serialize)]
pub struct CodeSkeletonBatchResponse {
    pub skeletons: Vec<CodeSkeletonResponse>,
} 