use std::path::Path;
use std::fs;
use std::sync::{atomic::{AtomicBool, Ordering}, Arc, Mutex};
use std::env;
use lancedb::{connect, Connection};
use lancedb::query::{QueryBase, ExecutableQuery};
use lancedb::table::OptimizeAction;
use lance::dataset::optimize::CompactionOptions;
use arrow::array::{
    FixedSizeListBuilder, Float32Builder, Int64Builder, RecordBatch, StringBuilder, AsArray
};
use arrow::datatypes::{DataType, Field, Schema};
use arrow::record_batch::RecordBatchIterator;
use uuid::Uuid;
use tracing::{info, error, debug, warn};
use reqwest::Client;
use serde::{Deserialize, Serialize};
use async_trait::async_trait;
use futures::TryStreamExt;
use futures::future::join_all;
use tokio::sync::Semaphore;
use rusqlite::{params, Connection as SqliteConnection};
use anyhow::anyhow;

use crate::codegraph::treesitter::TreeSitterParser;
use crate::codegraph::js_detector::{JsDetector, DetectionResult};
use crate::config::JsIndexConfig;
use crate::codegraph::parser::CodeParser;
use crate::config::Config;
use crate::storage::traits_bm25::{TextSearchProvider, CodeChunk};

struct EmbeddingCache {
    conn: Mutex<SqliteConnection>,
}

impl EmbeddingCache {
    fn new(path: &str) -> Result<Self, Box<dyn std::error::Error>> {
        let conn = SqliteConnection::open(path)?;
        conn.execute(
            "CREATE TABLE IF NOT EXISTS embedding_cache (
                hash TEXT PRIMARY KEY,
                vector BLOB,
                created_at INTEGER
            )",
            [],
        )?;
        Ok(Self { conn: Mutex::new(conn) })
    }

    fn get(&self, hash: &str) -> Option<Vec<f32>> {
        let conn = self.conn.lock().unwrap();
        let mut stmt = conn.prepare("SELECT vector FROM embedding_cache WHERE hash = ?1").ok()?;
        let mut rows = stmt.query(params![hash]).ok()?;
        
        if let Some(row) = rows.next().ok()? {
            let blob: Vec<u8> = row.get(0).ok()?;
            bincode::deserialize(&blob).ok()
        } else {
            None
        }
    }

    fn insert(&self, hash: &str, vector: &[f32]) -> Result<(), Box<dyn std::error::Error>> {
        let conn = self.conn.lock().unwrap();
        let blob = bincode::serialize(vector)?;
        conn.execute(
            "INSERT OR REPLACE INTO embedding_cache (hash, vector, created_at) VALUES (?1, ?2, strftime('%s', 'now'))",
            params![hash, blob],
        )?;
        Ok(())
    }
}

#[derive(Deserialize)]
struct EmbeddingResponse {
    data: Vec<EmbeddingData>,
}

#[derive(Deserialize)]
struct EmbeddingData {
    embedding: Vec<f32>,
}

struct CodePoint {
    id: String,
    vector: Vec<f32>,
    file_path: String,
    symbol_name: String,
    symbol_type: String,
    language: String,
    line_start: i64,
    line_end: i64,
    code_block: String,
}

/// 搜索结果（统一使用 score 字段，越大越相关）
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SearchResult {
    pub file_path: String,
    pub symbol_name: String,
    pub code_block: String,
    /// 相关性分数，越大表示越相关（统一转换自所有搜索来源）
    pub score: f32,
    // Fields for hybrid search
    pub symbol_type: String,
    pub language: String,
    pub line_start: usize,
    pub line_end: usize,
}

#[async_trait]
pub trait EmbeddingProvider: Send + Sync {
    async fn get_embedding(&self, text: &str) -> Result<Vec<f32>, Box<dyn std::error::Error>>;
    fn model(&self) -> String;
}

pub struct OpenAICompatibleEmbeddingProvider {
    client: Client,
    api_token: String,
    base_url: String,
    model: String,
}

impl OpenAICompatibleEmbeddingProvider {
    pub fn new(api_token: String, base_url: Option<String>, model: String) -> Self {
        let base_url = base_url.unwrap_or_else(|| "https://api.siliconflow.cn/v1".to_string());
        // Clean up the URL if it contains backticks or extra spaces
        let base_url = base_url.replace('`', "").trim().to_string();
        Self {
            client: Client::new(),
            api_token,
            base_url,
            model,
        }
    }
}

#[async_trait]
impl EmbeddingProvider for OpenAICompatibleEmbeddingProvider {
    fn model(&self) -> String {
        self.model.clone()
    }

    async fn get_embedding(&self, code_block: &str) -> Result<Vec<f32>, Box<dyn std::error::Error>> {
        if code_block.is_empty() {
            return Err("Code block is empty".into());
        }

        // Truncate if too long (approx 32k tokens, safe limit 30k chars for now)
        // Note: 32K context window is quite large, but we should still have a safety limit
        // Assuming ~4 chars per token for English, 32k tokens is ~128k chars.
        // For mixed content, being conservative with 64k chars is safe.
        let code_block = if code_block.len() > 64000 {
            &code_block[..64000]
        } else {
            code_block
        };
        
        let url = format!("{}/embeddings", self.base_url.trim_end_matches('/'));
        let response = self.client.post(&url)
            .header("Authorization", format!("Bearer {}", self.api_token))
            .header("Content-Type", "application/json")
            .json(&serde_json::json!({
                "model": self.model, 
                "input": code_block,
                "encoding_format": "float"
            }))
            .send()
            .await?;

        if !response.status().is_success() {
            let status = response.status();
            let text = response.text().await?;
            return Err(format!("API request failed with status {}: {}", status, text).into());
        }

        let embedding_response: EmbeddingResponse = response.json().await?;
        
        if let Some(data) = embedding_response.data.into_iter().next() {
            Ok(data.embedding)
        } else {
            Err("No embedding data returned from API".into())
        }
    }
}

pub struct EmbeddingService {
    connection: tokio::sync::Mutex<Connection>,
    /// The original db_path used to create this service (for self-healing).
    db_path: String,
    pub table_name: String,
    embedding_provider: Arc<dyn EmbeddingProvider + Send + Sync>,
    pub dimensions: i32,
    cache: Arc<EmbeddingCache>,
    /// Optional BM25 text search index for sparse channel.
    /// When Some, chunks are indexed here alongside LanceDB vector indexing.
    pub bm25_index: Option<Arc<dyn TextSearchProvider>>,
    /// Minimum code block length (chars after trim) to be indexed.
    min_code_block_length: usize,
    /// Concurrency limit for embedding API calls (default: 2x CPU cores).
    concurrency: usize,
    /// Marked true when a LanceDB corruption was detected and repair was triggered.
    repaired: Arc<AtomicBool>,
    /// Shutdown coordinator reference for tracking compaction operations
    shutdown: Option<Arc<crate::shutdown::ShutdownCoordinator>>,
}

impl EmbeddingService {
    pub async fn new(
        db_path: &str, 
        table_name: String, 
        config: Option<&Config>,
        bm25_index: Option<Arc<dyn TextSearchProvider>>,
    ) -> Result<Self, Box<dyn std::error::Error>> {
        // LanceDB connection (embedded)
        let connection = connect(db_path).execute().await?;
        
        // Initialize Cache - use a subdirectory within db_path to keep cache
        // isolated per service instance
        let cache_dir = Path::new(db_path).join("_cache");
        fs::create_dir_all(&cache_dir)?;
        let cache = Arc::new(EmbeddingCache::new(cache_dir.join("embedding_cache.sqlite").to_str().unwrap())?);

        // Initialize HTTP client and get API token
        let mut api_token = env::var("SILICONFLOW_API_KEY").ok();
        let mut base_url = None;
        let mut model = "Qwen/Qwen3-Embedding-4B".to_string(); // Default fallback
        let mut dimensions = 2560;

        if let Some(conf) = config {
             let embedding_config = &conf.codexray.embedding;
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
        
        let min_code_block_length = config
            .map(|cfg| cfg.codexray.retrieval_pipeline.min_code_block_length)
            .unwrap_or(16);
        
        let cpu_count = std::thread::available_parallelism()
            .map(|n| n.get())
            .unwrap_or(4);
        let concurrency = cpu_count * 2;
        
        Ok(Self {
            connection: tokio::sync::Mutex::new(connection),
            db_path: db_path.to_string(),
            table_name,
            embedding_provider: Arc::new(provider),
            dimensions,
            cache,
            bm25_index,
            min_code_block_length,
            concurrency,
            repaired: Arc::new(AtomicBool::new(false)),
            shutdown: None,
        })
    }
    
    /// Create a new EmbeddingService with a custom embedding provider
    pub async fn new_with_provider(
        db_path: &str, 
        table_name: String, 
        provider: Arc<dyn EmbeddingProvider + Send + Sync>
    ) -> Result<Self, Box<dyn std::error::Error>> {
        let connection = connect(db_path).execute().await?;
        
        // Initialize Cache - use a subdirectory within db_path to keep cache
        // isolated per service instance and avoid conflicts between tests
        let cache_dir = Path::new(db_path).join("_cache");
        fs::create_dir_all(&cache_dir)?;
        let cache_path = cache_dir.join("embedding_cache.sqlite");
        let cache = Arc::new(EmbeddingCache::new(cache_path.to_str().unwrap())?);
        
        let cpu_count = std::thread::available_parallelism()
            .map(|n| n.get())
            .unwrap_or(4);
        let concurrency = cpu_count * 2;

        Ok(Self {
            connection: tokio::sync::Mutex::new(connection),
            db_path: db_path.to_string(),
            table_name,
            embedding_provider: provider,
            dimensions: 2560,
            cache,
            bm25_index: None,
            min_code_block_length: 16,
            concurrency,
            repaired: Arc::new(AtomicBool::new(false)),
            shutdown: None,
        })
    }

    /// Set the shutdown coordinator for compaction tracking
    pub fn set_shutdown_coordinator(&mut self, shutdown: Arc<crate::shutdown::ShutdownCoordinator>) {
        self.shutdown = Some(shutdown);
    }

    /// Create or get the collection (table)
    pub async fn ensure_collection(&self) -> Result<(), Box<dyn std::error::Error>> {
        let conn = self.connection.lock().await;
        let table_names = conn.table_names().execute().await?;
        if !table_names.contains(&self.table_name) {
            info!("Creating table: {}", self.table_name);
            
            // Qwen/Qwen3-Embedding-4B has 2560 dimensions
            let vector_size = self.dimensions; 
            
            let schema = Arc::new(Schema::new(vec![
                Field::new("id", DataType::Utf8, false),
                Field::new("vector", DataType::FixedSizeList(
                    Arc::new(Field::new("item", DataType::Float32, true)),
                    vector_size
                ), false),
                Field::new("file_path", DataType::Utf8, false),
                Field::new("symbol_name", DataType::Utf8, false),
                Field::new("symbol_type", DataType::Utf8, false),
                Field::new("language", DataType::Utf8, false),
                Field::new("line_start", DataType::Int64, false),
                Field::new("line_end", DataType::Int64, false),
                Field::new("code_block", DataType::Utf8, false),
            ]));
            
            conn.create_empty_table(&self.table_name, schema).execute().await?;
            info!("Table {} created successfully", self.table_name);
        } else {
            info!("Table {} already exists", self.table_name);
        }

        Ok(())
    }

    /// Vectorize directory
    pub async fn vectorize_directory(&self, dir_path: &str, existing_hashes: Option<&std::collections::HashMap<String, String>>) -> Result<std::collections::HashMap<String, String>, Box<dyn std::error::Error>> {
        info!("Starting vectorization of directory: {}", dir_path);
        
        let mut parser = CodeParser::new(JsIndexConfig::default());
        let mut ts_parser = TreeSitterParser::new();
        
        let path = Path::new(dir_path);
        let files = parser.scan_directory(path);
        
        info!("Found {} files to vectorize", files.len());
        let mut total_vectors = 0;
        let mut new_hashes = std::collections::HashMap::new();
        
        for file_path in files {
            // Calculate MD5
            let content = match fs::read_to_string(&file_path) {
                Ok(c) => c,
                Err(e) => {
                    error!("Failed to read file {}: {}", file_path.display(), e);
                    continue;
                }
            };
            let hash = format!("{:x}", md5::compute(&content));
            let file_key = file_path.to_string_lossy().to_string();
            
            new_hashes.insert(file_key.clone(), hash.clone());

            // ── JavaScript 混淆检测过滤 ──
            if let Some(ext) = file_path.extension().and_then(|e| e.to_str()) {
                if matches!(ext.to_lowercase().as_str(), "js" | "jsx" | "mjs" | "cjs") {
                    let detector = JsDetector::default_detector();
                    match detector.detect(&content) {
                        DetectionResult::CompiledCode | DetectionResult::Empty => {
                            info!("JS 混淆过滤器：跳过编译/混淆文件: {}", file_path.display());
                            continue;
                        }
                        DetectionResult::SourceCode => {}
                    }
                }
            }

            // Check if modified
            if let Some(hashes) = existing_hashes {
                if let Some(old_hash) = hashes.get(&file_key) {
                    if old_hash == &hash {
                        continue;
                    }
                }
            }

            match self.process_file_content(&file_path, &content, &mut ts_parser).await {
                Ok(vectors) => {
                    total_vectors += vectors;
                    info!("File {} processed successfully with {} vectors", file_path.display(), vectors);
                }
                Err(e) => {
                    error!("Failed to process file {}: {}", file_path.display(), e);
                }
            }
        }
        
        info!("Vectorization completed. Total vectors created: {}", total_vectors);

        // [新增] 清理已删除文件的孤儿 embedding
        if let Some(old_hashes) = existing_hashes {
            let orphaned: Vec<String> = old_hashes
                .keys()
                .filter(|k| !new_hashes.contains_key(*k))
                .cloned()
                .collect();
            
            if !orphaned.is_empty() {
                info!("Cleaning up {} orphaned file embeddings from deleted files", orphaned.len());
                for file_path in &orphaned {
                    // 从 LanceDB 中删除
                    if let Err(e) = self.delete_file_embeddings(file_path).await {
                        warn!("Failed to remove LanceDB embeddings for deleted file {}: {}", file_path, e);
                    }
                    // 从 BM25 索引中删除
                    if let Some(ref bm25) = self.bm25_index {
                        if let Err(e) = bm25.remove_by_path(file_path).await {
                            warn!("Failed to remove BM25 index for deleted file {}: {}", file_path, e);
                        }
                    }
                }
            }
        }

        // [Compaction] 物理清理 LanceDB 旧版本数据，回收磁盘空间
        // 使用 CompactionGuard 确保 shutdown 时等待 compaction 完成
        let _compaction_guard = self.shutdown.as_ref().map(|s| s.start_compaction());
        info!("Starting LanceDB table optimization (compact + prune) for {}", self.table_name);
        let compact_result = (|| async {
            let conn = self.connection.lock().await;
            let table = conn.open_table(&self.table_name).execute().await?;
            
            // Step 1: Compact - 合并小文件，物理移除被删除的行
            let compact_options = CompactionOptions {
                materialize_deletions: true,
                materialize_deletions_threshold: 0.0,  // Always materialize deletions
                ..Default::default()
            };
            let stats = table.optimize(OptimizeAction::Compact {
                options: compact_options,
                remap_options: None,
            }).await?;
            let c = stats.compaction.as_ref().unwrap();
            info!("LanceDB compaction completed for {}: {} fragments removed, {} fragments added, {} files added, {} files removed",
                self.table_name, c.fragments_removed, c.fragments_added, c.files_added, c.files_removed);
            
            // Step 2: Prune - 删除旧的版本快照（立即清理，包含孤立数据文件）
            let stats = table.optimize(OptimizeAction::Prune {
                older_than: chrono::Duration::seconds(0),
                delete_unverified: Some(true),  // KEY FIX: 删除孤立数据文件
            }).await?;
            let p = stats.prune.as_ref().unwrap();
            info!("LanceDB prune completed for {}: {} bytes removed, {} old versions removed",
                self.table_name, p.bytes_removed, p.old_versions);
            
            Ok::<_, Box<dyn std::error::Error>>(())
        })();
        
        if let Err(e) = compact_result.await {
            warn!("LanceDB optimization (compact+prune) failed for {}: {}. This is non-fatal, but disk usage may accumulate.", self.table_name, e);
        }
        // _compaction_guard 在此 drop，compaction 计数器减 1

        Ok(new_hashes)
    }

    /// Delete existing embeddings for a file
    async fn delete_file_embeddings(&self, file_path: &str) -> Result<(), Box<dyn std::error::Error>> {
        let conn = self.connection.lock().await;
        let table = conn.open_table(&self.table_name).execute().await?;
        // Delete rows where file_path matches
        let predicate = format!("file_path = '{}'", file_path.replace("'", "''"));
        table.delete(&predicate).await?;
        Ok(())
    }

    /// Process single file content with concurrent embedding API calls
    async fn process_file_content(&self, file_path: &Path, _content: &str, ts_parser: &mut TreeSitterParser) -> Result<usize, Box<dyn std::error::Error>> {
        // Delete existing embeddings for this file to prevent duplicates
        self.delete_file_embeddings(&file_path.to_string_lossy()).await?;

        // Parse with TreeSitter
        let symbols = ts_parser.parse_file(&file_path.to_path_buf())?;
        
        // Local struct to hold task data for each symbol
        struct EmbeddingTask {
            code_block: String,
            name: String,
            symbol_type_str: String,
            language_str: String,
            start_row: usize,
            end_row: usize,
            hash: String,
        }
        
        let mut tasks: Vec<EmbeddingTask> = Vec::new();
        let mut bm25_chunks: Vec<CodeChunk> = Vec::new();
        
        // Step 1: Collect all valid symbols and build BM25 chunks
        let model = self.embedding_provider.model();
        
        for symbol in symbols {
            // Extract data and drop guard immediately
            let extracted = {
                let symbol_guard = symbol.read();
                let symbol_ref = symbol_guard.as_ref();
                
                match symbol_ref.symbol_type() {
                    crate::codegraph::treesitter::structs::SymbolType::StructDeclaration |
                    crate::codegraph::treesitter::structs::SymbolType::FunctionDeclaration => {
                        let symbol_info = symbol_ref.symbol_info_struct();
                        let code_block = symbol_info.get_content_from_file_blocked()
                            .unwrap_or_else(|e| {
                                eprintln!("Warning: Failed to get content for {}: {}", symbol_ref.name(), e);
                                symbol_ref.name().to_string()
                            });
                        
                        Some((
                            code_block,
                            symbol_ref.name().to_string(),
                            format!("{:?}", symbol_ref.symbol_type()),
                            format!("{:?}", symbol_ref.language()),
                            symbol_ref.full_range().start_point.row,
                            symbol_ref.full_range().end_point.row,
                        ))
                    }
                    _ => None,
                }
            };

            if let Some((code_block, name, symbol_type_str, language_str, start_row, end_row)) = extracted {
                // P0: Skip short code blocks to improve retrieval quality
                // See: docs/retrieval-quality-analysis.md
                if code_block.trim().chars().count() < self.min_code_block_length {
                    debug!("Skipping short symbol '{}' ({} chars, min: {})", 
                        name, code_block.len(), self.min_code_block_length);
                    continue;
                }
                
                // Index into BM25 if available
                if self.bm25_index.is_some() {
                    let chunk = CodeChunk::new(
                        file_path.to_string_lossy().into_owned(),
                        code_block.clone(),
                        name.clone(),
                        symbol_type_str.clone(),
                        language_str.clone(),
                        start_row + 1,
                        end_row + 1,
                    );
                    bm25_chunks.push(chunk);
                }

                // Pre-compute cache hash
                let hash_input = format!("{}{}", model, code_block);
                let hash = format!("{:x}", md5::compute(&hash_input));
                
                tasks.push(EmbeddingTask {
                    code_block,
                    name,
                    symbol_type_str,
                    language_str,
                    start_row,
                    end_row,
                    hash,
                });
            }
        }

        // Step 2: Check cache for each task, identify cache misses
        let cache = self.cache.clone();
        let mut embeddings: Vec<Option<Vec<f32>>> = vec![None; tasks.len()];
        let mut miss_indices: Vec<usize> = Vec::new();

        for (i, task) in tasks.iter().enumerate() {
            if let Some(cached) = cache.get(&task.hash) {
                embeddings[i] = Some(cached);
            } else {
                miss_indices.push(i);
            }
        }

        // Step 3: Concurrent API calls for cache misses using Semaphore
        if !miss_indices.is_empty() {
            let provider = self.embedding_provider.clone();
            let cache = self.cache.clone();
            let semaphore = Arc::new(Semaphore::new(self.concurrency));
            
            let futures: Vec<_> = miss_indices.iter().map(|&idx| {
                let provider = provider.clone();
                let cache = cache.clone();
                let semaphore = semaphore.clone();
                let code_block = tasks[idx].code_block.clone();
                let hash = tasks[idx].hash.clone();
                let name = tasks[idx].name.clone();
                
                async move {
                    // Acquire semaphore permit to limit concurrency
                    let _permit = semaphore.acquire().await.unwrap_or_else(|e| {
                        panic!("Semaphore closed: {}", e);
                    });
                    
                    // Double-check cache (another concurrent request may have cached this hash)
                    if let Some(cached) = cache.get(&hash) {
                        return (idx, Some(cached));
                    }
                    
                    match provider.get_embedding(&code_block).await {
                        Ok(vec) => {
                            // Cache the result
                            if let Err(e) = cache.insert(&hash, &vec) {
                                error!("Failed to cache embedding for symbol {}: {}", name, e);
                            }
                            (idx, Some(vec))
                        }
                        Err(e) => {
                            error!("Failed to get embedding for symbol {}: {}", name, e);
                            (idx, None)
                        }
                    }
                }
            }).collect();
            
            let results = join_all(futures).await;
            for (idx, vec_opt) in results {
                embeddings[idx] = vec_opt;
            }
        }

        // Step 4: Build CodePoints in original order and batch upload
        let mut points = Vec::new();
        let mut vectors_created = 0;

        for (i, task) in tasks.iter().enumerate() {
            if let Some(embedding) = embeddings[i].take() {
                let point = CodePoint {
                    id: Uuid::new_v4().to_string(),
                    vector: embedding,
                    file_path: file_path.to_string_lossy().to_string(),
                    symbol_name: task.name.clone(),
                    symbol_type: task.symbol_type_str.clone(),
                    language: task.language_str.clone(),
                    line_start: (task.start_row + 1) as i64,
                    line_end: (task.end_row + 1) as i64,
                    code_block: task.code_block.clone(),
                };
                
                debug!("Point created for symbol: {}", point.symbol_name);
                points.push(point);
                vectors_created += 1;
                
                // Batch upload every 100 vectors
                if points.len() >= 100 {
                    self.upload_points(&points).await?;
                    points.clear();
                }
            }
        }
        
        // Upload remaining vectors
        if !points.is_empty() {
            self.upload_points(&points).await?;
        }
        
        // Step 5: Batch index chunks into BM25
        // 先删除该文件的所有旧 BM25 条目，再添加新条目（确保一致性）
        if let Some(bm25) = &self.bm25_index {
            // 先清空该文件的旧 BM25 索引（就像 LanceDB 的 delete_file_embeddings 一样）
            if let Err(e) = bm25.remove_by_path(&file_path.to_string_lossy()).await {
                tracing::warn!("Failed to remove old BM25 entries for {:?}: {}", file_path, e);
            }
            // 再添加当前符号的新条目
            if !bm25_chunks.is_empty() {
                if let Err(e) = bm25.index_chunks(bm25_chunks.clone()).await {
                    tracing::warn!("BM25 indexing failed for {:?}: {}", file_path, e);
                    // Non-fatal: continue with vector indexing
                }
            }
        }
        
        Ok(vectors_created)
    }

    /// Upload vectors to LanceDB
    async fn upload_points(&self, points: &[CodePoint]) -> Result<(), Box<dyn std::error::Error>> {
        debug!("Uploading {} vectors to LanceDB", points.len());
        
        if points.is_empty() {
            return Ok(());
        }

        let vector_size = self.dimensions;

        let schema = Arc::new(Schema::new(vec![
            Field::new("id", DataType::Utf8, false),
            Field::new("vector", DataType::FixedSizeList(
                Arc::new(Field::new("item", DataType::Float32, true)),
                vector_size
            ), false),
            Field::new("file_path", DataType::Utf8, false),
            Field::new("symbol_name", DataType::Utf8, false),
            Field::new("symbol_type", DataType::Utf8, false),
            Field::new("language", DataType::Utf8, false),
            Field::new("line_start", DataType::Int64, false),
            Field::new("line_end", DataType::Int64, false),
            Field::new("code_block", DataType::Utf8, false),
        ]));

        // Build arrays
        let mut id_builder = StringBuilder::new();
        let mut vector_builder = FixedSizeListBuilder::new(Float32Builder::new(), vector_size);
        let mut file_path_builder = StringBuilder::new();
        let mut symbol_name_builder = StringBuilder::new();
        let mut symbol_type_builder = StringBuilder::new();
        let mut language_builder = StringBuilder::new();
        let mut line_start_builder = Int64Builder::new();
        let mut line_end_builder = Int64Builder::new();
        let mut code_block_builder = StringBuilder::new();
        
        for p in points {
            id_builder.append_value(&p.id);
            
            // Ensure vector size matches
            if p.vector.len() != vector_size as usize {
                error!("Vector size mismatch: expected {}, got {}", vector_size, p.vector.len());
                continue;
            }

            vector_builder.values().append_slice(&p.vector);
            vector_builder.append(true);
            
            file_path_builder.append_value(&p.file_path);
            symbol_name_builder.append_value(&p.symbol_name);
            symbol_type_builder.append_value(&p.symbol_type);
            language_builder.append_value(&p.language);
            line_start_builder.append_value(p.line_start);
            line_end_builder.append_value(p.line_end);
            code_block_builder.append_value(&p.code_block);
        }
        
        let batch = RecordBatch::try_new(schema.clone(), vec![
            Arc::new(id_builder.finish()),
            Arc::new(vector_builder.finish()),
            Arc::new(file_path_builder.finish()),
            Arc::new(symbol_name_builder.finish()),
            Arc::new(symbol_type_builder.finish()),
            Arc::new(language_builder.finish()),
            Arc::new(line_start_builder.finish()),
            Arc::new(line_end_builder.finish()),
            Arc::new(code_block_builder.finish()),
        ])?;
        
        let conn = self.connection.lock().await;
        let table = conn.open_table(&self.table_name).execute().await?;
        
        let batches = vec![Ok(batch)];
        let batch_iter = RecordBatchIterator::new(batches, schema.clone());
        table.add(batch_iter).execute().await?;

        Ok(())
    }

    /// Search for code blocks using semantic search.
    /// Detects LanceDB corruption (missing deletion files) and signals for repair.
    pub async fn search(&self, query: &str, limit: usize) -> Result<Vec<SearchResult>, anyhow::Error> {
        // 1. Generate embedding for the query
        let query_vector = self.embedding_provider.get_embedding(query).await.map_err(|e| anyhow::anyhow!("{}", e))?;

        // 2. Search in LanceDB with corruption detection
        let result = self.do_search(&query_vector, limit).await;
        
        // Check for LanceDB corruption patterns in error message
        if let Err(ref e) = result {
            let err_msg = e.to_string();
            if Self::is_lancedb_corruption_error(&err_msg) {
                warn!(
                    "[LANCE_DB_CORRUPTION] Detected LanceDB corruption during search: {}. \
                     Triggering repair signal.",
                    err_msg
                );
                self.repaired.store(true, Ordering::SeqCst);
            }
        }
        
        result
    }

    /// Check if an error message indicates LanceDB corruption.
    fn is_lancedb_corruption_error(msg: &str) -> bool {
        let lower = msg.to_lowercase();
        lower.contains("not found")
            || lower.contains("no such file")
            || lower.contains("object at location")
            || lower.contains("file not found")
            || lower.contains(".arrow") && lower.contains("delet")
    }

    /// Internal search implementation (separated for corruption detection).
    async fn do_search(&self, query_vector: &[f32], limit: usize) -> Result<Vec<SearchResult>, anyhow::Error> {
        let conn = self.connection.lock().await;
        let table = conn.open_table(&self.table_name).execute().await?;
        
        let mut results_stream = table.query()
            .nearest_to(query_vector)?
            .limit(limit)
            .execute()
            .await?;
            
        // 3. Parse results
        let mut search_results = Vec::new();
        
        while let Some(batch) = results_stream.try_next().await? {
            let file_path_col = batch.column_by_name("file_path").ok_or(anyhow!("Missing file_path column"))?.as_string::<i32>();
            let symbol_name_col = batch.column_by_name("symbol_name").ok_or(anyhow!("Missing symbol_name column"))?.as_string::<i32>();
            let code_block_col = batch.column_by_name("code_block").ok_or(anyhow!("Missing code_block column"))?.as_string::<i32>();
            
            let dist_col = batch.column_by_name("_distance");
            let dist_vals = if let Some(d) = dist_col {
                d.as_any().downcast_ref::<arrow::array::Float32Array>()
            } else {
                None
            };

            for i in 0..batch.num_rows() {
                let file_path = file_path_col.value(i).to_string();
                if !std::path::Path::new(&file_path).exists() {
                    continue;
                }
                // L2 距离 → 相关性分数 (0,1]，越大越相关
                let distance = if let Some(d) = dist_vals { d.value(i) } else { 0.0 };
                let score = (1.0 / (1.0 + distance)) as f32;
                
                // Try to get additional fields from LanceDB batch
                let symbol_type_col = batch.column_by_name("symbol_type");
                let language_col = batch.column_by_name("language");
                let line_start_col = batch.column_by_name("line_start");
                let line_end_col = batch.column_by_name("line_end");

                let symbol_type = symbol_type_col
                    .and_then(|c| c.as_any().downcast_ref::<arrow::array::StringArray>())
                    .map(|col| col.value(i).to_string())
                    .unwrap_or_default();

                let language = language_col
                    .and_then(|c| c.as_any().downcast_ref::<arrow::array::StringArray>())
                    .map(|col| col.value(i).to_string())
                    .unwrap_or_default();

                let line_start = line_start_col
                    .and_then(|c| c.as_any().downcast_ref::<arrow::array::Int64Array>())
                    .map(|col| col.value(i) as usize)
                    .unwrap_or(0);

                let line_end = line_end_col
                    .and_then(|c| c.as_any().downcast_ref::<arrow::array::Int64Array>())
                    .map(|col| col.value(i) as usize)
                    .unwrap_or(0);

                search_results.push(SearchResult {
                    file_path,
                    symbol_name: symbol_name_col.value(i).to_string(),
                    code_block: code_block_col.value(i).to_string(),
                    score,
                    symbol_type,
                    language,
                    line_start,
                    line_end,
                });
            }
        }
        
        Ok(search_results)
    }

    // ========================= Self-Healing Methods =========================

    /// Probe LanceDB health by executing a minimal query.
    /// Returns Ok(()) if the database is healthy, Err if it's corrupted.
    pub async fn probe_health(&self) -> Result<(), String> {
        let conn = self.connection.lock().await;
        let table = conn.open_table(&self.table_name)
            .execute()
            .await
            .map_err(|e| format!("Failed to execute table query: {}", e))?;

        // Execute a minimal query (limit 0) to verify the dataset is accessible
        let query_result = table.query()
            .nearest_to(vec![0.0; self.dimensions as usize]);
        let mut results_stream = match query_result {
            Ok(q) => q
                .limit(0)
                .execute()
                .await
                .map_err(|e| format!("LanceDB query failed (corruption likely): {}", e))?,
            Err(e) => return Err(format!("LanceDB query creation failed (corruption likely): {}", e)),
        };

        // Consume at least one batch to trigger any hidden errors
        while let Some(_batch) = results_stream.try_next().await
            .map_err(|e| format!("Failed to read batch: {}", e))? {}

        info!("LanceDB health check passed");
        Ok(())
    }

    /// Self-heal a corrupted LanceDB database.
    /// 1. Backs up the corrupted directory with a timestamp
    /// 2. Reconnects to LanceDB (creates fresh connection)
    /// 3. Recreates the collection (empty table)
    pub async fn try_repair(&self) -> Result<(), String> {
        // Avoid duplicate repairs
        if self.repaired.load(Ordering::SeqCst) {
            warn!("LanceDB already marked as repaired, skipping");
            return Ok(());
        }

        let timestamp = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_secs();
        let backup_path = format!("{}.corrupted.{}", self.db_path, timestamp);

        info!(
            "Attempting LanceDB self-heal: backing up '{}' to '{}'",
            self.db_path, backup_path
        );

        // 1. Backup corrupted directory
        if std::path::Path::new(&self.db_path).exists() {
            std::fs::rename(&self.db_path, &backup_path)
                .map_err(|e| format!("Failed to backup corrupted directory to '{}': {}", backup_path, e))?;
            info!("LanceDB backed up to: {}", backup_path);
        }

        // 2. Reconnect (creates a fresh LanceDB connection)
        let new_connection = connect(&self.db_path)
            .execute()
            .await
            .map_err(|e| format!("Failed to reconnect LanceDB: {}", e))?;
        *self.connection.lock().await = new_connection;
        info!("LanceDB reconnected at: {}", self.db_path);

        // 3. Recreate the collection (empty table)
        self.ensure_collection()
            .await
            .map_err(|e| format!("Failed to recreate collection '{}': {}", self.table_name, e))?;

        // Mark the repaired flag for future repair cycles
        self.repaired.store(true, Ordering::SeqCst);
        info!("LanceDB repair completed: fresh collection '{}' created at {}", self.table_name, self.db_path);

        Ok(())
    }

    /// Reset the repaired flag to allow future repair cycles.
    /// Called after a successful rebuild (e.g., after trigger_embedding_build).
    pub fn reset_repaired_flag(&self) {
        self.repaired.store(false, Ordering::SeqCst);
    }

    /// Check if a repair has been triggered since last reset.
    pub fn is_repaired(&self) -> bool {
        self.repaired.load(Ordering::SeqCst)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tempfile::tempdir;
    use std::sync::atomic::{AtomicUsize, Ordering};

    struct MockEmbeddingProvider {
        call_count: Arc<AtomicUsize>,
    }

    impl MockEmbeddingProvider {
        fn new() -> Self {
            Self {
                call_count: Arc::new(AtomicUsize::new(0)),
            }
        }
    }

    #[async_trait]
    impl EmbeddingProvider for MockEmbeddingProvider {
        fn model(&self) -> String {
            "mock-model".to_string()
        }

        async fn get_embedding(&self, _text: &str) -> Result<Vec<f32>, Box<dyn std::error::Error>> {
            self.call_count.fetch_add(1, Ordering::SeqCst);
            // Return a dummy vector of size 2560
            Ok(vec![0.1; 2560])
        }
    }

    #[tokio::test]
    async fn test_search() -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;
        let db_path = dir.path().to_str().unwrap();
        let table_name = "test_vectors".to_string();

        let provider = Arc::new(MockEmbeddingProvider::new());
        let service = EmbeddingService::new_with_provider(db_path, table_name.clone(), provider).await?;
        
        service.ensure_collection().await?;

        // Create a dummy file that exists so it passes the existence check
        let test_file_path = dir.path().join("test.rs");
        std::fs::write(&test_file_path, "fn test_fn() {}")?;
        let test_file_path_str = test_file_path.to_str().unwrap().to_string();

        // Manually insert some data using upload_points
        let point = CodePoint {
            id: Uuid::new_v4().to_string(),
            vector: vec![0.1; 2560],
            file_path: test_file_path_str.clone(),
            symbol_name: "test_fn".to_string(),
            symbol_type: "Function".to_string(),
            language: "Rust".to_string(),
            line_start: 1,
            line_end: 10,
            code_block: "fn test_fn() {}".to_string(),
        };

        service.upload_points(&[point]).await?;

        // Search
        let results = service.search("test query", 5).await?;
        
        assert_eq!(results.len(), 1);
        assert_eq!(results[0].symbol_name, "test_fn");
        assert_eq!(results[0].file_path, test_file_path_str);

        Ok(())
    }

    #[tokio::test]
    async fn test_caching() -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;
        let db_path = dir.path().to_str().unwrap();
        let table_name = "test_caching".to_string();

        let mock_provider = MockEmbeddingProvider::new();
        let call_count = mock_provider.call_count.clone();
        let provider = Arc::new(mock_provider);
        
        let service = EmbeddingService::new_with_provider(db_path, table_name.clone(), provider).await?;
        service.ensure_collection().await?;

        let mut ts_parser = TreeSitterParser::new();
        
        // Create a dummy file
        let file_path = dir.path().join("test_cache.rs");
        fs::write(&file_path, "fn test_cache() {}")?;
        
        // First pass - should call provider
        service.process_file_content(&file_path, "fn test_cache() {}", &mut ts_parser).await?;
        assert_eq!(call_count.load(Ordering::SeqCst), 1, "First call should hit provider");

        // Second pass - should hit cache
        service.process_file_content(&file_path, "fn test_cache() {}", &mut ts_parser).await?;
        assert_eq!(call_count.load(Ordering::SeqCst), 1, "Second call should hit cache");
        
        // Modify content - should call provider
        let file_path_2 = dir.path().join("test_cache_2.rs");
        fs::write(&file_path_2, "fn test_cache_2() {}")?;
        service.process_file_content(&file_path_2, "fn test_cache_2() {}", &mut ts_parser).await?;
        assert_eq!(call_count.load(Ordering::SeqCst), 2, "Modified content should hit provider");

        Ok(())
    }

    #[tokio::test]
    async fn test_duplicate_prevention() -> Result<(), Box<dyn std::error::Error>> {
        let dir = tempdir()?;
        let db_path = dir.path().join("lancedb");
        let db_path_str = db_path.to_str().unwrap();
        let table_name = "test_vectors_dedup".to_string();

        let provider = Arc::new(MockEmbeddingProvider::new());
        let service = EmbeddingService::new_with_provider(db_path_str, table_name.clone(), provider).await?;
        service.ensure_collection().await?;

        // Create a dummy file with content long enough to pass min_code_block_length check
        let file_path = dir.path().join("test_dedup.rs");
        fs::write(&file_path, "fn test_function() { let x = 1; }")?;

        // Vectorize first time
        service.vectorize_directory(dir.path().to_str().unwrap(), None).await?;

        // Verify count
        let results = service.search("test", 100).await?;
        assert_eq!(results.len(), 1);

        // Vectorize second time
        service.vectorize_directory(dir.path().to_str().unwrap(), None).await?;

        // Verify count is still 1
        let results = service.search("test", 100).await?;
        assert_eq!(results.len(), 1);

        Ok(())
    }

}
