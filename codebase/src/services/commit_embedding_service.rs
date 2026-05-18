use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use lancedb::{connect, Connection};
use lancedb::query::{ExecutableQuery, QueryBase};
use arrow::array::{
    FixedSizeListBuilder, Float32Builder, Int64Builder, RecordBatch, StringBuilder, AsArray
};
use arrow::datatypes::{DataType, Field, Schema};
use arrow::record_batch::RecordBatchIterator;
use async_trait::async_trait;
use futures::TryStreamExt;
use tracing::{info, error};

use crate::config::Config;
use super::embedding_service::{EmbeddingProvider, OpenAICompatibleEmbeddingProvider};

/// Commit 匹配结果
#[derive(Debug, Clone)]
pub struct CommitMatch {
    pub commit_hash: String,
    pub summary_text: String,
    pub similarity: f32,
}

/// Commit 嵌入提供者 trait
#[async_trait]
pub trait CommitEmbeddingProvider: Send + Sync {
    /// 获取文本的嵌入向量
    async fn get_embedding(&self, text: &str) -> Result<Vec<f32>, Box<dyn std::error::Error>>;
    
    /// 获取使用的模型名称
    fn model(&self) -> String;
}

/// 适配器：将 EmbeddingProvider 适配到 CommitEmbeddingProvider
pub(crate) struct EmbeddingProviderAdapter {
    inner: Box<dyn EmbeddingProvider + Send + Sync>,
}

impl EmbeddingProviderAdapter {
    pub(crate) fn from_openai_provider(provider: OpenAICompatibleEmbeddingProvider) -> Self {
        Self {
            inner: Box::new(provider),
        }
    }
}

#[async_trait]
impl CommitEmbeddingProvider for EmbeddingProviderAdapter {
    fn model(&self) -> String {
        self.inner.model()
    }

    async fn get_embedding(&self, text: &str) -> Result<Vec<f32>, Box<dyn std::error::Error>> {
        self.inner.get_embedding(text).await
    }
}

/// Commit 向量嵌入服务
/// 
/// 使用 LanceDB 存储 commit 的向量嵌入，支持相似性搜索。
/// 表名: `commit_embeddings`
/// Schema: {commit_hash, summary_text, embedding, timestamp}
pub struct CommitEmbeddingService {
    connection: Connection,
    embedding_provider: Box<dyn CommitEmbeddingProvider + Send + Sync>,
    dimensions: i32,
    table_name: String,
}

type ServiceResult<T> = Result<T, Box<dyn std::error::Error>>;

impl CommitEmbeddingService {
    /// 创建一个新的 CommitEmbeddingService
    /// 
    /// # Arguments
    /// * `connection` - LanceDB 连接
    /// * `embedding_provider` - 嵌入提供者
    /// * `dimensions` - 向量维度
    /// * `project_id` - 项目 ID，用于表名隔离
    pub async fn new(
        connection: Connection,
        embedding_provider: Box<dyn CommitEmbeddingProvider + Send + Sync>,
        dimensions: i32,
        project_id: &str,
    ) -> ServiceResult<Self> {
        let table_name = format!("commit_embeddings_{}", project_id);
        
        info!("Creating CommitEmbeddingService for project: {}, dimensions: {}", project_id, dimensions);
        
        let service = Self {
            connection,
            embedding_provider,
            dimensions,
            table_name,
        };
        
        // 自动初始化表
        service.init_table().await?;
        
        Ok(service)
    }

    /// 从配置创建 CommitEmbeddingService（便捷方法）
    /// 
    /// # Arguments
    /// * `db_path` - LanceDB 数据库路径
    /// * `project_id` - 项目 ID，用于表名隔离
    /// * `config` - 可选的配置引用
    pub async fn from_config(
        db_path: &str,
        project_id: &str,
        config: Option<&Config>,
    ) -> ServiceResult<Self> {
        let connection = connect(db_path).execute().await?;
        
        // 获取 embedding 配置
        let mut api_token = std::env::var("SILICONFLOW_API_KEY").ok();
        let mut base_url = None;
        let mut model = "Qwen/Qwen3-Embedding-4B".to_string();
        let mut dimensions = 2560;

        if let Some(conf) = config {
            let embedding_config = &conf.codebase.embedding;
            if !embedding_config.api_token.is_empty() {
                api_token = Some(embedding_config.api_token.clone());
            }
            if !embedding_config.api_base_url.is_empty() {
                base_url = Some(embedding_config.api_base_url.clone());
            }
            if !embedding_config.model.is_empty() {
                model = embedding_config.model.clone();
            }
            if let Some(dim) = embedding_config.dimensions {
                dimensions = dim as i32;
            }
        }
        
        let api_token = api_token.ok_or("API Key not found in config or environment")?;
        
        let provider = OpenAICompatibleEmbeddingProvider::new(api_token, base_url, model);
        let adapter = EmbeddingProviderAdapter::from_openai_provider(provider);

        Self::new(connection, Box::new(adapter), dimensions, project_id).await
    }

    /// 初始化 commit 嵌入表
    /// 
    /// 创建 LanceDB 表，Schema 包含：
    /// - commit_hash (string): Commit 哈希
    /// - summary_text (string): Commit 摘要文本
    /// - embedding (vector): 嵌入向量
    /// - timestamp (i64): 时间戳
    pub async fn init_table(&self) -> ServiceResult<()> {
        let table_names = self.connection.table_names().execute().await?;
        
        if !table_names.contains(&self.table_name) {
            info!("Creating commit embeddings table: {}", self.table_name);
            
            let vector_size = self.dimensions;
            
            let schema = Arc::new(Schema::new(vec![
                Field::new("commit_hash", DataType::Utf8, false),
                Field::new("summary_text", DataType::Utf8, false),
                Field::new("embedding", DataType::FixedSizeList(
                    Arc::new(Field::new("item", DataType::Float32, true)),
                    vector_size
                ), false),
                Field::new("timestamp", DataType::Int64, false),
            ]));
            
            self.connection
                .create_empty_table(&self.table_name, schema)
                .execute()
                .await?;
            
            info!("Commit embeddings table created successfully");
        } else {
            info!("Commit embeddings table already exists: {}", self.table_name);
        }

        Ok(())
    }

    /// 添加 commit 到向量数据库
    /// 
    /// # Arguments
    /// * `commit_hash` - Commit 哈希值
    /// * `summary_text` - Commit 摘要文本
    pub async fn add_commit(&self, commit_hash: &str, summary_text: &str) -> ServiceResult<()> {
        if commit_hash.is_empty() || summary_text.is_empty() {
            return Err("Commit hash and summary text cannot be empty".into());
        }

        // 获取嵌入向量
        let embedding = match self.embedding_provider.get_embedding(summary_text).await {
            Ok(vec) => vec,
            Err(e) => {
                error!("Failed to get embedding for commit {}: {}", commit_hash, e);
                return Err(e.into());
            }
        };

        // 验证向量维度
        if embedding.len() != self.dimensions as usize {
            return Err(format!(
                "Vector dimension mismatch: expected {}, got {}",
                self.dimensions,
                embedding.len()
            ).into());
        }

        // 获取当前时间戳
        let timestamp = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("Time went backwards")
            .as_secs() as i64;

        // 插入 commit
        self.insert_commit(commit_hash, summary_text, &embedding, timestamp).await?;

        info!("Added commit to vector store: {}", commit_hash);
        Ok(())
    }

    /// 搜索相似的 commit
    /// 
    /// # Arguments
    /// * `query` - 查询文本
    /// * `top_k` - 返回结果数量
    pub async fn search_similar(&self, query: &str, top_k: usize) -> ServiceResult<Vec<CommitMatch>> {
        if query.is_empty() {
            return Err("Query cannot be empty".into());
        }

        if top_k == 0 {
            return Err("top_k must be greater than 0".into());
        }

        // 获取查询向量
        let query_vector = match self.embedding_provider.get_embedding(query).await {
            Ok(vec) => vec,
            Err(e) => {
                error!("Failed to get embedding for query: {}", e);
                return Err(e.into());
            }
        };

        // 验证向量维度
        if query_vector.len() != self.dimensions as usize {
            return Err(format!(
                "Query vector dimension mismatch: expected {}, got {}",
                self.dimensions,
                query_vector.len()
            ).into());
        }

        // 打开表
        let table = self
            .connection
            .open_table(&self.table_name)
            .execute()
            .await?;

        // 执行向量搜索
        let mut results_stream = table
            .query()
            .nearest_to(query_vector)?
            .limit(top_k)
            .execute()
            .await?;

        // 解析结果
        let mut matches = Vec::new();

        while let Some(batch) = results_stream.try_next().await? {
            let commit_hash_col = batch
                .column_by_name("commit_hash")
                .ok_or("Missing commit_hash column")?
                .as_string::<i32>();

            let summary_text_col = batch
                .column_by_name("summary_text")
                .ok_or("Missing summary_text column")?
                .as_string::<i32>();

            let dist_col = batch.column_by_name("_distance");
            let dist_vals = if let Some(d) = dist_col {
                d.as_any()
                    .downcast_ref::<arrow::array::Float32Array>()
            } else {
                None
            };

            for i in 0..batch.num_rows() {
                let commit_hash = commit_hash_col.value(i).to_string();
                let summary_text = summary_text_col.value(i).to_string();
                let distance = if let Some(d) = dist_vals {
                    d.value(i)
                } else {
                    0.0
                };

                // LanceDB 的 _distance 是距离，转换为相似度（1 - distance）
                let similarity: f32 = (1.0_f32 - distance).max(0.0).min(1.0);

                matches.push(CommitMatch {
                    commit_hash,
                    summary_text,
                    similarity,
                });
            }
        }

        // 按相似度降序排序
        matches.sort_by(|a, b| {
            b.similarity
                .partial_cmp(&a.similarity)
                .unwrap_or(std::cmp::Ordering::Equal)
        });

        info!("Found {} similar commits for query", matches.len());
        Ok(matches)
    }

   /// 清空所有 commit 数据
    /// 
    /// 使用 drop_table 后重建的方式，确保表结构始终可用
    pub async fn clear_all(&self) -> ServiceResult<()> {
        // 先删除表
        info!("Dropping table: {}", self.table_name);
        self.connection
            .drop_table(&self.table_name)
            .await?;
        
        // 重建空表
        info!("Recreating empty table: {}", self.table_name);
        self.init_table().await?;
        
        info!("Cleared all commits from vector store (table dropped and recreated)");
        Ok(())
    }

    /// 批量添加 commits
    pub async fn add_commits_batch(
        &self,
        commits: Vec<(&str, &str)>,
    ) -> ServiceResult<()> {
        if commits.is_empty() {
            return Ok(());
        }

        info!("Batch adding {} commits to vector store", commits.len());

        for (commit_hash, summary_text) in commits {
            self.add_commit(commit_hash, summary_text).await?;
        }

        Ok(())
    }

   /// 获取表的行数
    pub async fn count_commits(&self) -> ServiceResult<usize> {
        let table = self
            .connection
            .open_table(&self.table_name)
            .execute()
            .await?;

        // 使用空查询获取所有行
        let mut results_stream = table
            .query()
            .execute()
            .await?;

        let mut count = 0;
        while let Some(batch) = results_stream.try_next().await? {
            count += batch.num_rows();
        }

        Ok(count)
    }

    /// 内部方法：插入单个 commit 到表中
    async fn insert_commit(
        &self,
        commit_hash: &str,
        summary_text: &str,
        embedding: &[f32],
        timestamp: i64,
    ) -> ServiceResult<()> {
        let vector_size = self.dimensions;

        let schema = Arc::new(Schema::new(vec![
            Field::new("commit_hash", DataType::Utf8, false),
            Field::new("summary_text", DataType::Utf8, false),
            Field::new("embedding", DataType::FixedSizeList(
                Arc::new(Field::new("item", DataType::Float32, true)),
                vector_size
            ), false),
            Field::new("timestamp", DataType::Int64, false),
        ]));

        // 构建数组
        let mut commit_hash_builder = StringBuilder::new();
        let mut summary_text_builder = StringBuilder::new();
        let mut embedding_builder = FixedSizeListBuilder::new(Float32Builder::new(), vector_size);
        let mut timestamp_builder = Int64Builder::new();

        commit_hash_builder.append_value(commit_hash);
        summary_text_builder.append_value(summary_text);
        
        embedding_builder.values().append_slice(embedding);
        embedding_builder.append(true);
        
        timestamp_builder.append_value(timestamp);

        let batch = RecordBatch::try_new(schema.clone(), vec![
            Arc::new(commit_hash_builder.finish()),
            Arc::new(summary_text_builder.finish()),
            Arc::new(embedding_builder.finish()),
            Arc::new(timestamp_builder.finish()),
        ])?;

        let table = self
            .connection
            .open_table(&self.table_name)
            .execute()
            .await?;

        let batches = vec![Ok(batch)];
        let batch_iter = RecordBatchIterator::new(batches, schema);
        table.add(batch_iter).execute().await?;

        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    struct MockCommitEmbeddingProvider {
        call_count: Arc<std::sync::atomic::AtomicUsize>,
    }

    impl MockCommitEmbeddingProvider {
        fn new() -> Self {
            Self {
                call_count: Arc::new(std::sync::atomic::AtomicUsize::new(0)),
            }
        }
    }

    #[async_trait]
    impl CommitEmbeddingProvider for MockCommitEmbeddingProvider {
        fn model(&self) -> String {
            "mock-model".to_string()
        }

        async fn get_embedding(
            &self,
            _text: &str,
        ) -> Result<Vec<f32>, Box<dyn std::error::Error>> {
            self.call_count.fetch_add(1, std::sync::atomic::Ordering::SeqCst);
            // 返回一个 dummy 向量
            Ok(vec![0.1; 256])
        }
    }

    #[tokio::test]
    async fn test_init_table() -> ServiceResult<()> {
        let dir = tempfile::tempdir()?;
        let db_path = dir.path().join("test.lancedb");
        let connection = lancedb::connect(db_path.to_str().unwrap()).execute().await?;

        let provider = Box::new(MockCommitEmbeddingProvider::new());
        let service = CommitEmbeddingService::new(connection, provider, 256, "test").await?;

        // new 方法已经自动调用了 init_table，验证表已创建
        let table_names = service.connection.table_names().execute().await?;
        assert!(table_names.contains(&"commit_embeddings_test".to_string()));

        Ok(())
    }

    #[tokio::test]
    async fn test_add_commit() -> ServiceResult<()> {
        let dir = tempfile::tempdir()?;
        let db_path = dir.path().join("test.lancedb");
        let connection = lancedb::connect(db_path.to_str().unwrap()).execute().await?;

        let provider = Box::new(MockCommitEmbeddingProvider::new());
        let service = CommitEmbeddingService::new(connection, provider, 256, "test").await?;

        // 添加 commit
        service
            .add_commit("abc123", "Add new feature")
            .await?;

        // 验证 commit 已添加
        let count = service.count_commits().await?;
        assert_eq!(count, 1);

        Ok(())
    }

    #[tokio::test]
    async fn test_search_similar() -> ServiceResult<()> {
        let dir = tempfile::tempdir()?;
        let db_path = dir.path().join("test.lancedb");
        let connection = lancedb::connect(db_path.to_str().unwrap()).execute().await?;

        let provider = Box::new(MockCommitEmbeddingProvider::new());
        let service = CommitEmbeddingService::new(connection, provider, 256, "test").await?;

        // 添加多个 commits
        service
            .add_commit("abc123", "Add new feature")
            .await?;
        service
            .add_commit("def456", "Fix bug in authentication")
            .await?;
        service
            .add_commit("ghi789", "Update documentation")
            .await?;

        // 搜索
        let results = service.search_similar("feature", 5).await?;

        // 验证结果
        assert!(results.len() <= 3); // 最多 3 个
        assert!(results.len() > 0); // 至少 1 个

        // 验证排序（按相似度降序）
        for i in 0..results.len() - 1 {
            assert!(results[i].similarity >= results[i + 1].similarity);
        }

        Ok(())
    }

    #[tokio::test]
    async fn test_clear_all() -> ServiceResult<()> {
        let dir = tempfile::tempdir()?;
        let db_path = dir.path().join("test.lancedb");
        let connection = lancedb::connect(db_path.to_str().unwrap()).execute().await?;

        let provider = Box::new(MockCommitEmbeddingProvider::new());
        let service = CommitEmbeddingService::new(connection, provider, 256, "test").await?;

        // 添加 commits
        service
            .add_commit("abc123", "Add new feature")
            .await?;
        service
            .add_commit("def456", "Fix bug")
            .await?;

        // 验证已添加
        let count = service.count_commits().await?;
        assert_eq!(count, 2);

        // 清空
        service.clear_all().await?;

        // 验证已清空
        let count = service.count_commits().await?;
        assert_eq!(count, 0);

        Ok(())
    }

    #[tokio::test]
    async fn test_add_commits_batch() -> ServiceResult<()> {
        let dir = tempfile::tempdir()?;
        let db_path = dir.path().join("test.lancedb");
        let connection = lancedb::connect(db_path.to_str().unwrap()).execute().await?;

        let provider = Box::new(MockCommitEmbeddingProvider::new());
        let service = CommitEmbeddingService::new(connection, provider, 256, "test").await?;

        // 批量添加 commits
        let commits = vec![
            ("abc123", "Add new feature"),
            ("def456", "Fix bug"),
            ("ghi789", "Update docs"),
        ];
        service.add_commits_batch(commits).await?;

        // 验证
        let count = service.count_commits().await?;
        assert_eq!(count, 3);

        Ok(())
    }

    #[tokio::test]
    async fn test_empty_inputs() -> ServiceResult<()> {
        let dir = tempfile::tempdir()?;
        let db_path = dir.path().join("test.lancedb");
        let connection = lancedb::connect(db_path.to_str().unwrap()).execute().await?;

        let provider = Box::new(MockCommitEmbeddingProvider::new());
        let service = CommitEmbeddingService::new(connection, provider, 256, "test").await?;

        // 测试空 commit hash
        let result = service.add_commit("", "Add feature").await;
        assert!(result.is_err());

        // 测试空 summary
        let result = service.add_commit("abc123", "").await;
        assert!(result.is_err());

        // 测试空查询
        let result = service.search_similar("", 5).await;
        assert!(result.is_err());

        // 测试 top_k = 0
        service.add_commit("abc123", "Add feature").await?;
        let result = service.search_similar("feature", 0).await;
        assert!(result.is_err());

        Ok(())
    }
}
