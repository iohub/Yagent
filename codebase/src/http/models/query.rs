use serde::{Deserialize, Serialize};

#[derive(Debug, Deserialize)]
pub struct QueryCallGraphRequest {
    pub filepath: String,
    pub function_name: Option<String>,
    pub max_depth: Option<usize>,
}

#[derive(Debug, Serialize, Clone)]
pub struct FunctionInfo {
    pub id: String,
    pub name: String,
    pub line_start: usize,
    pub line_end: usize,
    pub callers: Vec<CallRelation>,
    pub callees: Vec<CallRelation>,
}

#[derive(Debug, Serialize, Clone)]
pub struct CallRelation {
    pub function_name: String,
    pub file_path: String,
}

#[derive(Debug, Serialize)]
pub struct QueryCallGraphResponse {
    pub filepath: String,
    pub functions: Vec<FunctionInfo>,
}

// New models for hierarchical tree structure output
#[derive(Debug, Deserialize)]
pub struct QueryHierarchicalGraphRequest {
    pub project_id: Option<String>,
    pub root_function: Option<String>,
    pub max_depth: Option<usize>,
    pub include_file_info: Option<bool>,
}

#[derive(Debug, Serialize)]
pub struct HierarchicalNode {
    pub name: String,
    pub function_id: Option<String>,
    pub file_path: Option<String>,
    pub line_start: Option<usize>,
    pub line_end: Option<usize>,
    pub children: Vec<HierarchicalNode>,
    pub call_type: Option<String>, // "direct", "indirect", etc.
}

#[derive(Debug, Serialize)]
pub struct QueryHierarchicalGraphResponse {
    pub project_id: String,
    pub root_function: Option<String>,
    pub max_depth: usize,
    pub tree_structure: HierarchicalNode,
    pub total_functions: usize,
    pub total_relations: usize,
} 

#[derive(Debug, Deserialize)]
pub struct DrawCallGraphRequest {
    pub filepath: String,
    pub function_name: Option<String>,
    pub max_depth: Option<usize>,
}

// 用于 GET 请求的查询参数结构
#[derive(Debug, Deserialize)]
pub struct DrawCallGraphQuery {
    #[serde(default)]
    pub filepath: String,
    pub function_name: Option<String>,
    #[serde(default = "default_max_depth")]
    pub max_depth: Option<usize>,
}

fn default_max_depth() -> Option<usize> {
    Some(3)
}

 