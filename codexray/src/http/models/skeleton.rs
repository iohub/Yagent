use serde::{Deserialize, Serialize};

/// Maximum characters in a skeleton_text field.
/// Aligns with Go client's truncation limit (5000 chars).
pub const MAX_SKELETON_TEXT_CHARS: usize = 5000;

#[derive(Debug, Deserialize)]
pub struct QueryCodeSkeletonRequest {
    pub filepaths: Vec<String>,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct CodeSkeletonResponse {
    pub filepath: String,
    pub language: String,
    pub skeleton_text: String,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct CodeSkeletonBatchResponse {
    pub skeletons: Vec<CodeSkeletonResponse>,
} 