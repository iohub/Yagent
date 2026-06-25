use serde::{Deserialize, Serialize};

use super::{CallRelation, CodeSkeletonResponse};

/// Maximum number of core (high-centrality) functions to return.
pub const MAX_CORE_FUNCTIONS: usize = 10;
/// Maximum number of caller edges to return per core function.
pub const MAX_CALLERS_PER_FUNC: usize = 20;
/// Maximum number of callee edges to return per core function.
pub const MAX_CALLEES_PER_FUNC: usize = 20;
/// Maximum number of file skeletons to return.
pub const MAX_FILE_SKELETONS: usize = 10;

#[derive(Debug, Deserialize)]
pub struct InvestigateRepoRequest {}

#[derive(Debug, Serialize, Deserialize)]
pub struct InvestigateFunctionInfo {
    pub name: String,
    pub file_path: String,
    pub out_degree: usize,
    pub callers: Vec<CallRelation>,
    pub callees: Vec<CallRelation>,
    pub callers_truncated: bool,
    pub callees_truncated: bool,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct InvestigateRepoResponse {
    pub project_id: String,
    pub total_functions: usize,
    pub core_functions: Vec<InvestigateFunctionInfo>,
    pub file_skeletons: Vec<CodeSkeletonResponse>,
} 