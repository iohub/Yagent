pub mod analyzer;
pub mod snippet_service;
pub mod embedding_service;
pub mod commit_embedding_service;

pub use analyzer::CodeAnalyzer;
pub use snippet_service::SnippetService;
pub use embedding_service::EmbeddingService;
pub use commit_embedding_service::{
    CommitEmbeddingService,
    CommitEmbeddingProvider,
    CommitMatch,
};
