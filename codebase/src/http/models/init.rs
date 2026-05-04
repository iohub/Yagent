use serde::{Deserialize, Serialize};

#[derive(Debug, Deserialize)]
pub struct InitRequest {
    pub project_dir: String,
}

#[derive(Debug, Serialize)]
pub struct InitResponse {
    pub project_id: String,
    pub loaded_from_cache: bool,
    pub total_functions: usize,
    pub total_files: usize,
} 