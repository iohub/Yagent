use serde::{Deserialize, Serialize};

#[derive(Debug, Deserialize)]
pub struct QueryCodeSnippetRequest {
    pub filepath: String,
    pub function_name: Option<String>,
    pub include_context: Option<bool>,
    pub context_lines: Option<usize>,
}

#[derive(Debug, Serialize)]
pub struct CodeSnippetResponse {
    pub filepath: String,
    pub function_name: Option<String>,
    pub code_snippet: String,
    pub line_start: usize,
    pub line_end: usize,
    pub language: String,
} 