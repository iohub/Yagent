use serde::{Deserialize, Serialize};

use super::{CallRelation, CodeSkeletonResponse};

#[derive(Debug, Deserialize)]
pub struct InvestigateRepoRequest {}

#[derive(Debug, Serialize, Deserialize)]
pub struct InvestigateFunctionInfo {
    pub name: String,
    pub file_path: String,
    pub out_degree: usize,
    pub callers: Vec<CallRelation>,
    pub callees: Vec<CallRelation>,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct InvestigateRepoResponse {
    pub project_id: String,
    pub total_functions: usize,
    pub core_functions: Vec<InvestigateFunctionInfo>,
    pub file_skeletons: Vec<CodeSkeletonResponse>,
} 