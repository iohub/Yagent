pub mod query;
pub mod snippet;
pub mod skeleton;
pub mod init;
pub mod investigate;
pub mod embedding;
pub mod commit;
pub mod repo_knowledge;

pub use query::*;
pub use snippet::*;
pub use skeleton::*;
pub use init::*;
pub use investigate::*;
pub use embedding::*;
pub use commit::*;
pub use repo_knowledge::*;

use serde::{Deserialize, Serialize};

#[derive(Debug, Serialize, Deserialize)]
pub struct ApiResponse<T> {
    pub success: bool,
    pub data: T,
}

#[derive(Debug, Serialize, Deserialize)]
pub struct ApiError {
    pub success: bool,
    pub error: String,
    pub code: u16,
}
