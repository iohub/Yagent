use std::path::Path;
use std::fs;
use std::sync::{Arc, Mutex};
use std::env;
use lancedb::{connect, Connection};
use lancedb::query::{QueryBase, ExecutableQuery};
use arrow::array::{
    FixedSizeListBuilder, Float32Builder, Int64Builder, RecordBatch, StringBuilder, AsArray
};
use arrow::datatypes::{DataType, Field, Schema};
use arrow::record_batch::RecordBatchIterator;
use uuid::Uuid;
use tracing::{info, error, debug};
use reqwest::Client;
use serde::Deserialize;
use async_trait::async_trait;
use futures::TryStreamExt;
use rusqlite::{params, Connection as SqliteConnection};

use crate::codegraph::treesitter::TreeSitterParser;
use crate::codegraph::parser::CodeParser;
use crate::config::Config;

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

#[derive(Debug, Clone, serde::Serialize)]
pub struct SearchResult {
    pub file_path: String,
    pub symbol_name: String,
    pub code_block: String,
    pub semantic_distance: f32,
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
    connection: Connection, 
    pub table_name: String,
    embedding_provider: Box<dyn EmbeddingProvider + Send + Sync>,
    pub dimensions: i32,
    cache: Arc<EmbeddingCache>,
}

impl EmbeddingService {
    pub async fn new(db_path: &str, table_name: String, config: Option<&Config>) -> Result<Self, Box<dyn std::error::Error>> {
        // LanceDB connection (embedded)
        let connection = connect(db_path).execute().await?;
        
        // Initialize Cache
        let cache_path = Path::new(db_path).parent()
            .unwrap_or_else(|| Path::new("."))
            .join("embedding_cache.sqlite");
        let cache = Arc::new(EmbeddingCache::new(cache_path.to_str().unwrap())?);

        // Initialize HTTP client and get API token
        let mut api_token = env::var("SILICONFLOW_API_KEY").ok();
        let mut base_url = None;
        let mut model = "Qwen/Qwen3-Embedding-4B".to_string(); // Default fallback
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
        
        Ok(Self {
            connection,
            table_name,
            embedding_provider: Box::new(provider),
            dimensions,
            cache,
        })
    }
    
    /// Create a new VectorizeService with a custom embedding provider
    pub async fn new_with_provider(
        db_path: &str, 
        table_name: String, 
        provider: Box<dyn EmbeddingProvider>
    ) -> Result<Self, Box<dyn std::error::Error>> {
        let connection = connect(db_path).execute().await?;
        
        // Initialize Cache
        let cache_path = Path::new(db_path).parent()
            .unwrap_or_else(|| Path::new("."))
            .join("embedding_cache.sqlite");
        let cache = Arc::new(EmbeddingCache::new(cache_path.to_str().unwrap())?);

        Ok(Self {
            connection,
            table_name,
            embedding_provider: provider,
            dimensions: 2560,
            cache,
        })
    }

    /// Create or get the collection (table)
    pub async fn ensure_collection(&self) -> Result<(), Box<dyn std::error::Error>> {
        let table_names = self.connection.table_names().execute().await?;
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
            
            self.connection.create_empty_table(&self.table_name, schema).execute().await?;
            info!("Table {} created successfully", self.table_name);
        } else {
            info!("Table {} already exists", self.table_name);
        }

        Ok(())
    }

    /// Vectorize directory
    pub async fn vectorize_directory(&self, dir_path: &str, existing_hashes: Option<&std::collections::HashMap<String, String>>) -> Result<std::collections::HashMap<String, String>, Box<dyn std::error::Error>> {
        info!("Starting vectorization of directory: {}", dir_path);
        
        let mut parser = CodeParser::new();
        let mut ts_parser = TreeSitterParser::new();
        
        let path = Path::new(dir_path);
        let files = parser.scan_directory(path);
        
        info!("Found {} files to vectorize", files.len());
        let mut total_vectors = 0;
        let mut new_hashes = std::collections::HashMap::new();
        
        for file_path in files {
            info!("Processing file: {}", file_path.display());
            
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
        Ok(new_hashes)
    }

    /// Delete existing embeddings for a file
    async fn delete_file_embeddings(&self, file_path: &str) -> Result<(), Box<dyn std::error::Error>> {
        let table = self.connection.open_table(&self.table_name).execute().await?;
        // Delete rows where file_path matches
        let predicate = format!("file_path = '{}'", file_path.replace("'", "''"));
        table.delete(&predicate).await?;
        Ok(())
    }

    /// Process single file content
    async fn process_file_content(&self, file_path: &Path, _content: &str, ts_parser: &mut TreeSitterParser) -> Result<usize, Box<dyn std::error::Error>> {
        // Delete existing embeddings for this file to prevent duplicates
        self.delete_file_embeddings(&file_path.to_string_lossy()).await?;

        // Parse with TreeSitter
        let symbols = ts_parser.parse_file(&file_path.to_path_buf())?;
        
        let mut vectors_created = 0;
        let mut points = Vec::new();
        
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
                // Check cache first
                let model = self.embedding_provider.model();
                let hash_input = format!("{}{}", model, code_block);
                let hash = format!("{:x}", md5::compute(hash_input));
                
                let embedding = if let Some(cached) = self.cache.get(&hash) {
                    info!("Cache hit for symbol: {}", name);
                    cached
                } else {
                    info!("Cache miss for symbol: {}", name);
                    // Generate embedding (now safe to await)
                    let vec = match self.embedding_provider.get_embedding(&code_block).await {
                        Ok(vec) => vec,
                        Err(e) => {
                            error!("Failed to get embedding for symbol {}: {}", name, e);
                            continue;
                        }
                    };
                    
                    // Cache the result
                    if let Err(e) = self.cache.insert(&hash, &vec) {
                        error!("Failed to cache embedding for symbol {}: {}", name, e);
                    }
                    vec
                };

                // Create point
                let point = CodePoint {
                    id: Uuid::new_v4().to_string(),
                    vector: embedding,
                    file_path: file_path.to_string_lossy().to_string(),
                    symbol_name: name,
                    symbol_type: symbol_type_str,
                    language: language_str,
                    line_start: (start_row + 1) as i64,
                    line_end: (end_row + 1) as i64,
                    code_block,
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
        
        let table = self.connection.open_table(&self.table_name).execute().await?;
        
        let batches = vec![Ok(batch)];
        let batch_iter = RecordBatchIterator::new(batches, schema.clone());
        table.add(batch_iter).execute().await?;

        Ok(())
    }

    /// Search for code blocks using semantic search
    pub async fn search(&self, query: &str, limit: usize) -> Result<Vec<SearchResult>, Box<dyn std::error::Error>> {
        // 1. Generate embedding for the query
        let query_vector = self.embedding_provider.get_embedding(query).await?;

        // 2. Search in LanceDB
        let table = self.connection.open_table(&self.table_name).execute().await?;
        
        let mut results_stream = table.query()
            .nearest_to(query_vector)?
            .limit(limit)
            .execute()
            .await?;
            
        // 3. Parse results
        let mut search_results = Vec::new();
        
        while let Some(batch) = results_stream.try_next().await? {
            let file_path_col = batch.column_by_name("file_path").ok_or("Missing file_path column")?.as_string::<i32>();
            let symbol_name_col = batch.column_by_name("symbol_name").ok_or("Missing symbol_name column")?.as_string::<i32>();
            let code_block_col = batch.column_by_name("code_block").ok_or("Missing code_block column")?.as_string::<i32>();
            
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
                let semantic_distance = if let Some(d) = dist_vals { d.value(i) } else { 0.0 };
                
                search_results.push(SearchResult {
                    file_path,
                    symbol_name: symbol_name_col.value(i).to_string(),
                    code_block: code_block_col.value(i).to_string(),
                    semantic_distance,
                });
            }
        }
        
        Ok(search_results)
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

        let provider = Box::new(MockEmbeddingProvider::new());
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
        let provider = Box::new(mock_provider);
        
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

        let provider = Box::new(MockEmbeddingProvider::new());
        let service = EmbeddingService::new_with_provider(db_path_str, table_name.clone(), provider).await?;
        service.ensure_collection().await?;

        // Create a dummy file
        let file_path = dir.path().join("test_dedup.rs");
        fs::write(&file_path, "fn test_fn() {}")?;

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
