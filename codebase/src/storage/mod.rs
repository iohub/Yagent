pub mod persistence;
pub mod incremental;
pub mod petgraph_storage;
pub mod traits;
pub mod traits_bm25;
pub mod tantivy_index;
pub mod prelude;

pub use persistence::PersistenceManager;
pub use incremental::IncrementalManager;
pub use petgraph_storage::{PetGraphStorage, PetGraphStorageManager};
pub use traits::{GraphPersistence, IncrementalUpdater, GraphSerializer};
pub use tantivy_index::TantivyBm25Index;

use std::sync::Arc;
use std::sync::Mutex;
use std::collections::{HashMap, HashSet};
use notify::RecommendedWatcher;
use parking_lot::RwLock;
use anyhow::{Result, anyhow};
use tracing::info;
use crate::codegraph::types::PetCodeGraph;
use crate::cli::args::StorageMode;
use crate::config::Config;
use crate::services::commit_embedding_service::{CommitEmbeddingService, CommitEmbeddingProvider};
use crate::services::repo_knowledge_service::{RepoKnowledgeService, RepoKnowledgeEmbeddingProvider};
use crate::storage::traits_bm25::TextSearchProvider;

pub struct StorageManager {
    persistence: Arc<PersistenceManager>,
    incremental: Arc<IncrementalManager>,
    graph: Arc<RwLock<Option<PetCodeGraph>>>,
    storage_mode: StorageMode,
    watchers: Arc<Mutex<HashMap<String, RecommendedWatcher>>>,
    pub vector_tasks: Arc<Mutex<HashSet<String>>>,
    pub config: Arc<RwLock<Option<Config>>>,
    /// 当前进程绑定的仓库路径，一个进程只支持索引一个仓库
    current_repo: Arc<RwLock<Option<String>>>,
    /// Commit 向量嵌入服务（使用 Mutex 支持内部可变性）
    commit_embedding_service: parking_lot::Mutex<Option<Arc<CommitEmbeddingService>>>,
    /// Repo Knowledge 向量嵌入服务（使用 Mutex 支持内部可变性）
    repo_knowledge_service: parking_lot::Mutex<Option<Arc<RepoKnowledgeService>>>,
    /// 共享的 BM25 全文搜索索引（Tantivy）
    /// 用于在 server 启动、background embedding task 和文件 watcher 之间共享同一实例
    pub bm25_index: Arc<parking_lot::RwLock<Option<Arc<dyn TextSearchProvider>>>>,
}

impl StorageManager {
    pub fn new() -> Self {
        Self::with_storage_mode(StorageMode::default())
    }

    pub fn with_storage_mode(storage_mode: StorageMode) -> Self {
        let base_dir = std::env::current_dir()
            .unwrap_or_else(|_| std::path::PathBuf::from("."))
            .join(".codegraph_db");

        Self {
            persistence: Arc::new(PersistenceManager::with_storage_mode(storage_mode.clone(), base_dir)),
            incremental: Arc::new(IncrementalManager::new()),
            graph: Arc::new(RwLock::new(None)),
            storage_mode,
            watchers: Arc::new(Mutex::new(HashMap::new())),
            vector_tasks: Arc::new(Mutex::new(HashSet::new())),
            config: Arc::new(RwLock::new(None)),
            current_repo: Arc::new(RwLock::new(None)),
            commit_embedding_service: parking_lot::Mutex::new(None),
            repo_knowledge_service: parking_lot::Mutex::new(None),
            bm25_index: Arc::new(parking_lot::RwLock::new(None)),
        }
    }

    pub fn with_config(storage_mode: StorageMode, config: Config) -> Self {
        let base_dir = std::path::PathBuf::from(&config.codebase.graph_db_uri);

        Self {
            persistence: Arc::new(PersistenceManager::with_storage_mode(storage_mode.clone(), base_dir)),
            incremental: Arc::new(IncrementalManager::new()),
            graph: Arc::new(RwLock::new(None)),
            storage_mode,
            watchers: Arc::new(Mutex::new(HashMap::new())),
            vector_tasks: Arc::new(Mutex::new(HashSet::new())),
            config: Arc::new(RwLock::new(Some(config))),
            current_repo: Arc::new(RwLock::new(None)),
            commit_embedding_service: parking_lot::Mutex::new(None),
            repo_knowledge_service: parking_lot::Mutex::new(None),
            bm25_index: Arc::new(parking_lot::RwLock::new(None)),
        }
    }

    pub fn set_config(&self, config: Config) {
        *self.config.write() = Some(config);
    }

    pub fn get_config(&self) -> Option<Config> {
        self.config.read().clone()
    }

    pub fn add_watcher(&self, project_id: String, watcher: RecommendedWatcher) {
        self.watchers.lock().unwrap().insert(project_id, watcher);
    }

    pub fn has_watcher(&self, project_id: &str) -> bool {
        self.watchers.lock().unwrap().contains_key(project_id)
    }

    pub fn set_storage_mode(&mut self, storage_mode: StorageMode) {
        self.storage_mode = storage_mode.clone();
        // Update persistence manager's storage mode
        Arc::get_mut(&mut self.persistence)
            .unwrap()
            .set_storage_mode(storage_mode);
    }

    pub fn get_storage_mode(&self) -> &StorageMode {
        &self.storage_mode
    }

    pub fn get_persistence(&self) -> Arc<PersistenceManager> {
        self.persistence.clone()
    }

    pub fn get_incremental(&self) -> Arc<IncrementalManager> {
        self.incremental.clone()
    }

    pub fn set_bm25_index(&self, index: Arc<dyn TextSearchProvider>) {
        *self.bm25_index.write() = Some(index);
    }

    pub fn get_bm25_index(&self) -> Option<Arc<dyn TextSearchProvider>> {
        self.bm25_index.read().clone()
    }

    pub fn get_graph(&self) -> Arc<RwLock<Option<PetCodeGraph>>> {
        self.graph.clone()
    }

    pub fn set_graph(&self, graph: PetCodeGraph) {
        *self.graph.write() = Some(graph);
    }

    pub fn get_graph_clone(&self) -> Option<PetCodeGraph> {
        self.graph.read().clone()
    }

    /// 尝试绑定当前进程到指定仓库。如果尚未绑定则绑定并返回 Ok(())，
    /// 如果已绑定到同一仓库则返回 Ok(())，
    /// 如果已绑定到不同仓库则返回 Err(已绑定的仓库路径)。
    pub fn try_bind_repo(&self, repo_path: &str) -> Result<(), String> {
        let mut current = self.current_repo.write();
        match current.as_ref() {
            None => {
                *current = Some(repo_path.to_string());
                Ok(())
            }
            Some(existing) if existing == repo_path => Ok(()),
            Some(existing) => Err(existing.clone()),
        }
    }

    /// 获取当前进程绑定的仓库路径
    pub fn get_current_repo(&self) -> Option<String> {
        self.current_repo.read().clone()
    }

   /// 初始化 Commit Embedding Service
    ///
    /// # Arguments
    /// * `provider` - Commit 嵌入提供者，用于生成 commit summary 的向量嵌入
    /// * `project_id` - 项目 ID，用于表名隔离
    ///
    /// # Errors
    /// 当配置不存在或初始化失败时返回错误
    pub async fn init_commit_embedding_service(
        &self,
        provider: Box<dyn CommitEmbeddingProvider + Send + Sync>,
        project_id: &str,
    ) -> Result<()> {
        use lancedb::connect;

        // 从配置中获取数据库路径
        let config = self.config.read();
        let config = config.as_ref().ok_or_else(|| {
            anyhow!("Config not set. Please call set_config() before initializing commit embedding service")
        })?;

        let graph_db_uri = &config.codebase.graph_db_uri;
        let dimensions = config.codebase.embedding.dimensions.unwrap_or(2560) as i32;

        // 构建 LanceDB 连接路径
        let db_path = format!("{}/commit_embeddings.lance", graph_db_uri.trim_end_matches('/'));

        // 创建 LanceDB 连接
        let connection = connect(&db_path).execute().await?;

        info!(
            "Initializing CommitEmbeddingService for project: {}, dimensions: {}, db_path: {}",
            project_id, dimensions, db_path
        );

        // 创建服务（new 方法会自动初始化表）
        let service = CommitEmbeddingService::new(connection, provider, dimensions, project_id)
            .await
            .map_err(|e| anyhow!("Failed to create commit embedding service: {}", e))?;

        *self.commit_embedding_service.lock() = Some(Arc::new(service));
        Ok(())
    }

    /// 获取 Commit Embedding Service 实例
    ///
    /// # Errors
    /// 当服务未初始化时返回错误
    pub fn get_commit_embedding_service(&self) -> Result<Arc<CommitEmbeddingService>> {
        self.commit_embedding_service
            .lock()
            .clone()
            .ok_or_else(|| anyhow!("Commit embedding service not initialized. Call init_commit_embedding_service() first"))
    }

    /// 初始化 Repo Knowledge Service
    ///
    /// # Arguments
    /// * `embedding_provider` - 嵌入提供者，用于生成 task 的向量嵌入
    /// * `project_id` - 项目 ID，用于表名隔离
    ///
    /// # Errors
    /// 当配置不存在或初始化失败时返回错误
    pub async fn init_repo_knowledge_service(
        &self,
        embedding_provider: Box<dyn RepoKnowledgeEmbeddingProvider + Send + Sync>,
        project_id: &str,
    ) -> Result<()> {
        use lancedb::connect;

        // 从配置中获取数据库路径
        let config = self.config.read();
        let config = config.as_ref().ok_or_else(|| {
            anyhow!("Config not set. Please call set_config() before initializing repo knowledge service")
        })?;

        let graph_db_uri = &config.codebase.graph_db_uri;
        let dimensions = config.codebase.embedding.dimensions.unwrap_or(2560) as i32;

        // 构建 LanceDB 连接路径
        let db_path = format!("{}/repo_knowledge.lance", graph_db_uri.trim_end_matches('/'));

        // 创建 LanceDB 连接
        let connection = connect(&db_path).execute().await?;

        info!(
            "Initializing RepoKnowledgeService for project: {}, dimensions: {}, db_path: {}",
            project_id, dimensions, db_path
        );

        // 创建服务（new 方法会自动初始化表）
        let service = RepoKnowledgeService::new(connection, embedding_provider, dimensions, project_id)
            .await
            .map_err(|e| anyhow!("Failed to create repo knowledge service: {}", e))?;

        *self.repo_knowledge_service.lock() = Some(Arc::new(service));
        Ok(())
    }

    /// 获取 Repo Knowledge Service 实例
    ///
    /// # Errors
    /// 当服务未初始化时返回错误
    pub fn get_repo_knowledge_service(&self) -> Result<Arc<RepoKnowledgeService>> {
        self.repo_knowledge_service
            .lock()
            .clone()
            .ok_or_else(|| anyhow!("Repo knowledge service not initialized. Call init_repo_knowledge_service() first"))
    }
} 