use std::sync::Arc;
use std::time::{SystemTime, UNIX_EPOCH};

use lancedb::{connect, Connection};
use lancedb::query::{QueryBase, ExecutableQuery};
use arrow::array::{
    FixedSizeListBuilder, Float32Builder, RecordBatch, StringBuilder, TimestampMillisecondBuilder, AsArray,
};
use arrow::datatypes::{DataType, Field, Schema};
use arrow::record_batch::RecordBatchIterator;
use async_trait::async_trait;
use futures::TryStreamExt;
use tracing::{info, error};
use uuid::Uuid;

use super::embedding_service::EmbeddingProvider;

/// Repo Knowledge 匹配结果
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct RepoKnowledgeMatch {
    pub id: String,
    pub task: String,
    pub result: String,
    pub score: f32,
}

/// Repo Knowledge 嵌入提供者 trait
#[async_trait]
pub trait RepoKnowledgeEmbeddingProvider: Send + Sync {
    /// 获取文本的嵌入向量
    async fn get_embedding(&self, text: &str) -> Result<Vec<f32>, Box<dyn std::error::Error>>;

    /// 获取使用的模型名称
    fn model(&self) -> String;
}

/// 适配器：将 EmbeddingProvider 适配到 RepoKnowledgeEmbeddingProvider
pub(crate) struct EmbeddingProviderAdapter {
    inner: Box<dyn EmbeddingProvider + Send + Sync>,
}

impl EmbeddingProviderAdapter {
    pub(crate) fn from_embedding_provider(provider: Box<dyn EmbeddingProvider + Send + Sync>) -> Self {
        Self { inner: provider }
    }
}

#[async_trait]
impl RepoKnowledgeEmbeddingProvider for EmbeddingProviderAdapter {
    fn model(&self) -> String {
        self.inner.model()
    }

    async fn get_embedding(&self, text: &str) -> Result<Vec<f32>, Box<dyn std::error::Error>> {
        self.inner.get_embedding(text).await
    }
}

/// Repo Knowledge 向量嵌入服务
///
/// 使用 LanceDB 存储仓库分析知识的向量嵌入，支持相似性搜索。
/// 表名: `repo_knowledge`
/// Schema: {id, task, result, embedding, created_at}
pub struct RepoKnowledgeService {
    connection: Connection,
    embedding_provider: Box<dyn RepoKnowledgeEmbeddingProvider + Send + Sync>,
    dimensions: i32,
    table_name: String,
}

type ServiceResult<T> = Result<T, Box<dyn std::error::Error>>;

impl RepoKnowledgeService {
    /// 创建一个新的 RepoKnowledgeService
    ///
    /// # Arguments
    /// * `connection` - LanceDB 连接
    /// * `embedding_provider` - 嵌入提供者
    /// * `dimensions` - 向量维度
    /// * `project_id` - 项目 ID，用于表名隔离
    pub async fn new(
        connection: Connection,
        embedding_provider: Box<dyn RepoKnowledgeEmbeddingProvider + Send + Sync>,
        dimensions: i32,
        project_id: &str,
    ) -> ServiceResult<Self> {
        let table_name = format!("repo_knowledge_{}", project_id);

        info!("Creating RepoKnowledgeService for project: {}, dimensions: {}", project_id, dimensions);

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

    /// 从配置创建 RepoKnowledgeService（便捷方法）
    ///
    /// # Arguments
    /// * `db_path` - LanceDB 数据库路径
    /// * `project_id` - 项目 ID，用于表名隔离
    /// * `config` - 可选的配置引用
    pub async fn from_config(
        db_path: &str,
        project_id: &str,
        config: Option<&super::super::config::Config>,
        embedding_provider: Box<dyn EmbeddingProvider + Send + Sync>,
    ) -> ServiceResult<Self> {
        let connection = connect(db_path).execute().await?;

        // 获取 embedding 配置
        let mut dimensions = 2560i32;

        if let Some(conf) = config {
            if let Some(dim) = conf.codebase.embedding.dimensions {
                dimensions = dim as i32;
            }
        }

        let adapter = EmbeddingProviderAdapter::from_embedding_provider(embedding_provider);

        Self::new(connection, Box::new(adapter), dimensions, project_id).await
    }

    /// 初始化 repo knowledge 表
    ///
    /// 创建 LanceDB 表，Schema 包含：
    /// - id (string): 唯一标识符（主键）
    /// - task (string): 任务描述
    /// - result (string): 分析结果
    /// - embedding (vector): 嵌入向量
    /// - created_at (timestamp): 创建时间
    pub async fn init_table(&self) -> ServiceResult<()> {
        let table_names = self.connection.table_names().execute().await?;

        if !table_names.contains(&self.table_name) {
            info!("Creating repo knowledge table: {}", self.table_name);

            let vector_size = self.dimensions;

            let schema = Arc::new(Schema::new(vec![
                Field::new("id", DataType::Utf8, false),
                Field::new("task", DataType::Utf8, false),
                Field::new("result", DataType::Utf8, false),
                Field::new("embedding", DataType::FixedSizeList(
                    Arc::new(Field::new("item", DataType::Float32, true)),
                    vector_size,
                ), false),
                Field::new("created_at", DataType::Timestamp(
                    arrow::datatypes::TimeUnit::Millisecond,
                    None,
                ), false),
            ]));

            self.connection
                .create_empty_table(&self.table_name, schema)
                .execute()
                .await?;

            info!("Repo knowledge table created successfully");
        } else {
            info!("Repo knowledge table already exists: {}", self.table_name);
        }

        Ok(())
    }

    /// 添加知识条目到向量数据库
    ///
    /// # Arguments
    /// * `task` - 任务描述（用于生成 embedding）
    /// * `result` - 分析结果
    ///
    /// # Returns
    /// 返回生成的知识条目 ID
    pub async fn add_knowledge(&self, task: &str, result: &str) -> ServiceResult<String> {
        if task.is_empty() || result.is_empty() {
            return Err("Task and result cannot be empty".into());
        }

        // 获取嵌入向量（基于 task 字段）
        let embedding = match self.embedding_provider.get_embedding(task).await {
            Ok(vec) => vec,
            Err(e) => {
                error!("Failed to get embedding for task: {}", e);
                return Err(e.into());
            }
        };

        // 验证向量维度
        if embedding.len() != self.dimensions as usize {
            return Err(format!(
                "Vector dimension mismatch: expected {}, got {}",
                self.dimensions,
                embedding.len()
            )
            .into());
        }

        // 生成 UUID 作为 id
        let id = Uuid::new_v4().to_string();

        // 获取当前时间戳
        let created_at = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("Time went backwards")
            .as_millis() as i64;

        // 插入数据
        self.insert_knowledge(&id, task, result, &embedding, created_at).await?;

        info!("Added knowledge to vector store: {}", id);
        Ok(id)
    }

    /// 搜索相似的历史分析
    ///
    /// # Arguments
    /// * `task` - 查询任务描述
    /// * `top_k` - 返回结果数量
    pub async fn search_similar(&self, task: &str, top_k: usize) -> ServiceResult<Vec<RepoKnowledgeMatch>> {
        if task.is_empty() {
            return Err("Query task cannot be empty".into());
        }

        if top_k == 0 {
            return Err("top_k must be greater than 0".into());
        }

        // 获取查询向量
        let query_vector = match self.embedding_provider.get_embedding(task).await {
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
            )
            .into());
        }

        // 打开表
        let table = self.connection.open_table(&self.table_name).execute().await?;

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
            let id_col = batch
                .column_by_name("id")
                .ok_or("Missing id column")?
                .as_string::<i32>();

            let task_col = batch
                .column_by_name("task")
                .ok_or("Missing task column")?
                .as_string::<i32>();

            let result_col = batch
                .column_by_name("result")
                .ok_or("Missing result column")?
                .as_string::<i32>();

            let dist_col = batch.column_by_name("_distance");
            let dist_vals = if let Some(d) = dist_col {
                d.as_any()
                    .downcast_ref::<arrow::array::Float32Array>()
            } else {
                None
            };

            for i in 0..batch.num_rows() {
                let id = id_col.value(i).to_string();
                let task = task_col.value(i).to_string();
                let result = result_col.value(i).to_string();
                let distance = if let Some(d) = dist_vals {
                    d.value(i)
                } else {
                    0.0
                };

                // LanceDB 的 _distance 是距离，转换为相似度（1 - distance）
                let score: f32 = (1.0_f32 - distance).max(0.0).min(1.0);

                matches.push(RepoKnowledgeMatch {
                    id,
                    task,
                    result,
                    score,
                });
            }
        }

        // 按 score 降序排序
        matches.sort_by(|a, b| b.score.partial_cmp(&a.score).unwrap_or(std::cmp::Ordering::Equal));

        info!("Found {} similar knowledge entries for task", matches.len());
        Ok(matches)
    }

   /// 清空所有知识数据
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

        info!("Cleared all knowledge from vector store (table dropped and recreated)");
        Ok(())
    }

    /// 内部方法：插入单条知识记录到表中
    async fn insert_knowledge(
        &self,
        id: &str,
        task: &str,
        result: &str,
        embedding: &[f32],
        created_at: i64,
    ) -> ServiceResult<()> {
        let vector_size = self.dimensions;

        let schema = Arc::new(Schema::new(vec![
            Field::new("id", DataType::Utf8, false),
            Field::new("task", DataType::Utf8, false),
            Field::new("result", DataType::Utf8, false),
            Field::new("embedding", DataType::FixedSizeList(
                Arc::new(Field::new("item", DataType::Float32, true)),
                vector_size,
            ), false),
            Field::new("created_at", DataType::Timestamp(
                arrow::datatypes::TimeUnit::Millisecond,
                None,
            ), false),
        ]));

        // 构建数组
        let mut id_builder = StringBuilder::new();
        let mut task_builder = StringBuilder::new();
        let mut result_builder = StringBuilder::new();
        let mut embedding_builder = FixedSizeListBuilder::new(Float32Builder::new(), vector_size);
        let mut created_at_builder = TimestampMillisecondBuilder::new();

        id_builder.append_value(id);
        task_builder.append_value(task);
        result_builder.append_value(result);

        embedding_builder.values().append_slice(embedding);
        embedding_builder.append(true);

        created_at_builder.append_value(created_at);

        let batch = RecordBatch::try_new(schema.clone(), vec![
            Arc::new(id_builder.finish()),
            Arc::new(task_builder.finish()),
            Arc::new(result_builder.finish()),
            Arc::new(embedding_builder.finish()),
            Arc::new(created_at_builder.finish()),
        ])?;

        let table = self.connection.open_table(&self.table_name).execute().await?;

        let batches = vec![Ok(batch)];
        let batch_iter = RecordBatchIterator::new(batches, schema);
        table.add(batch_iter).execute().await?;

        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    struct MockRepoKnowledgeEmbeddingProvider {
        call_count: Arc<std::sync::atomic::AtomicUsize>,
    }

    impl MockRepoKnowledgeEmbeddingProvider {
        fn new() -> Self {
            Self {
                call_count: Arc::new(std::sync::atomic::AtomicUsize::new(0)),
            }
        }
    }

    #[async_trait]
    impl RepoKnowledgeEmbeddingProvider for MockRepoKnowledgeEmbeddingProvider {
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

        let provider = Box::new(MockRepoKnowledgeEmbeddingProvider::new());
        let service = RepoKnowledgeService::new(connection, provider, 256, "test").await?;

        // new 方法已经自动调用了 init_table，验证表已创建
        let table_names = service.connection.table_names().execute().await?;
        assert!(table_names.contains(&"repo_knowledge_test".to_string()));

        Ok(())
    }

    #[tokio::test]
    async fn test_add_knowledge() -> ServiceResult<()> {
        let dir = tempfile::tempdir()?;
        let db_path = dir.path().join("test.lancedb");
        let connection = lancedb::connect(db_path.to_str().unwrap()).execute().await?;

        let provider = Box::new(MockRepoKnowledgeEmbeddingProvider::new());
        let service = RepoKnowledgeService::new(connection, provider, 256, "test").await?;

        // 添加知识
        service
            .add_knowledge("Analyze auth module", "The auth module handles...")
            .await?;

        // 验证知识已添加（通过搜索验证）
        let results = service.search_similar("Analyze auth module", 5).await?;
        assert_eq!(results.len(), 1);

        Ok(())
    }

    #[tokio::test]
    async fn test_search_similar() -> ServiceResult<()> {
        let dir = tempfile::tempdir()?;
        let db_path = dir.path().join("test.lancedb");
        let connection = lancedb::connect(db_path.to_str().unwrap()).execute().await?;

        let provider = Box::new(MockRepoKnowledgeEmbeddingProvider::new());
        let service = RepoKnowledgeService::new(connection, provider, 256, "test").await?;

        // 添加多条知识
        service
            .add_knowledge("Analyze auth module", "Authentication implementation...")
            .await?;
        service
            .add_knowledge("Analyze database layer", "Database connection pooling...")
            .await?;
        service
            .add_knowledge("Analyze API endpoints", "REST API routing configuration...")
            .await?;

        // 搜索
        let results = service.search_similar("auth", 5).await?;

        // 验证结果
        assert!(results.len() <= 3); // 最多 3 个
        assert!(results.len() > 0); // 至少 1 个

        // 验证排序（按 score 降序）
        for i in 0..results.len() - 1 {
            assert!(results[i].score >= results[i + 1].score);
        }

        Ok(())
    }

    #[tokio::test]
    async fn test_clear_all() -> ServiceResult<()> {
        let dir = tempfile::tempdir()?;
        let db_path = dir.path().join("test.lancedb");
        let connection = lancedb::connect(db_path.to_str().unwrap()).execute().await?;

        let provider = Box::new(MockRepoKnowledgeEmbeddingProvider::new());
        let service = RepoKnowledgeService::new(connection, provider, 256, "test").await?;

        // 添加知识
        service
            .add_knowledge("Task 1", "Result 1")
            .await?;
        service
            .add_knowledge("Task 2", "Result 2")
            .await?;

        // 验证已添加
        let results = service.search_similar("Task", 10).await?;
        assert_eq!(results.len(), 2);

        // 清空
        service.clear_all().await?;

        // 验证已清空
        let results = service.search_similar("Task", 10).await?;
        assert_eq!(results.len(), 0);

        Ok(())
    }

    #[tokio::test]
    async fn test_empty_inputs() -> ServiceResult<()> {
        let dir = tempfile::tempdir()?;
        let db_path = dir.path().join("test.lancedb");
        let connection = lancedb::connect(db_path.to_str().unwrap()).execute().await?;

        let provider = Box::new(MockRepoKnowledgeEmbeddingProvider::new());
        let service = RepoKnowledgeService::new(connection, provider, 256, "test").await?;

        // 测试空 task
        let result = service.add_knowledge("", "Result").await;
        assert!(result.is_err());

        // 测试空 result
        let result = service.add_knowledge("Task", "").await;
        assert!(result.is_err());

        // 测试空查询
        let result = service.search_similar("", 5).await;
        assert!(result.is_err());

        // 测试 top_k = 0
        service.add_knowledge("Task", "Result").await?;
        let result = service.search_similar("Task", 0).await;
        assert!(result.is_err());

        Ok(())
    }
}
