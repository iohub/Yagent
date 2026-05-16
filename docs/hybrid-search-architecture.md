# Hybrid Search 混合检索架构设计方案

## 一、整体架构

```
┌─────────────────────────────────────────────────────────────────┐
│                      Axum HTTP Server                            │
│                                                                   │
│  POST /hybrid_search ──► HybridSearchHandler                     │
│  POST /evor_search   ──► EvoRHandler                            │
│  POST /rerank        ──► RerankHandler (独立调用)               │
│                                                                   │
├─────────────────────────────────────────────────────────────────┤
│                     Retrieval Service Layer                       │
│                                                                   │
│  ┌────────────────────┐    ┌───────────────────┐                │
│  │  SparseRetriever   │    │  DenseRetriever   │                │
│  │  (Tantivy BM25)    │    │  (LanceDB 向量)   │                │
│  └────────┬───────────┘    └────────┬──────────┘                │
│           │                         │                            │
│           └──────────┬──────────────┘                            │
│                      ▼                                            │
│  ┌──────────────────────────────────────────┐                   │
│  │       HybridFusion (RRF 融合)            │                   │
│  │  Reciprocal Rank Fusion: RRF(d)=Σ 1/(k+r)│                   │
│  └──────────────────┬───────────────────────┘                   │
│                     ▼                                            │
│  ┌──────────────────────────────────────────┐                   │
│  │     CrossEncoder Reranker (ORT)          │                   │
│  │     输入: (query, code_pairs) → Top-N    │                   │
│  └──────────────────┬───────────────────────┘                   │
│                     ▼                                            │
│  ┌──────────────────────────────────────────┐                   │
│  │     GraphExpander (Petgraph BFS)         │                   │
│  │     扩展: callees, callers, imports, impl│                   │
│  └──────────────────────────────────────────┘                   │
│                                                                   │
├─────────────────────────────────────────────────────────────────┤
│                      EvoR 闭环                                   │
│  ErrorFeatureExtractor → QueryAugmenter → HybridSearch(adjusted)│
│  最大迭代: 3次 | 会话管理: HashMap<String, EvoSession>           │
├─────────────────────────────────────────────────────────────────┤
│                     Storage Layer                                │
│  StorageManager                                                │
│  ├─ TantivyIndexManager (BM25 稀疏索引)                        │
│  ├─ LanceDBManager (向量稠密索引)                               │
│  └─ CodeGraphManager (Petgraph 代码图)                          │
└─────────────────────────────────────────────────────────────────┘
```

## 二、新增模块结构

```
codebase/src/
├── retrieval/                     # 新增：检索核心模块
│   ├── mod.rs                     # 模块入口与导出
│   ├── types.rs                   # 共享数据类型 (Request/Response)
│   ├── hybrid.rs                  # Hybrid Fusion (RRF 融合算法)
│   ├── sparse.rs                  # 稀疏检索 (Tantivy BM25)
│   ├── dense.rs                   # 稠密检索 (LanceDB 封装)
│   ├── reranker.rs                # Cross-Encoder 重排序
│   ├── expansion.rs               # 图扩展 (BFS)
│   └── evor.rs                    # EvoR 演进检索
├── codegraph/
│   ├── graph.rs                   # 修改：增加 expand_context 方法
│   └── types.rs                   # 扩展 Node 类型
├── http/
│   ├── server.rs                  # 修改：新增路由
│   ├── handlers/
│   │   ├── hybrid.rs              # 新增：HybridSearchHandler
│   │   ├── evor.rs                # 新增：EvoRHandler
│   │   └── rerank.rs              # 新增：RerankHandler
│   └── models/
│       └── hybrid.rs              # 新增：HTTP 模型
├── storage/
│   └── mod.rs                     # 修改：增加 Tantivy 索引管理
├── config.rs                      # 修改：新增检索参数
└── lib.rs                         # 修改：新增 retrieval 模块
```

## 三、核心数据结构设计

### 3.1 检索类型定义 (`retrieval/types.rs`)

```rust
use serde::{Serialize, Deserialize};
use uuid::Uuid;

// ==================== 请求/响应 ====================

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HybridSearchRequest {
    /// 搜索查询文本
    pub query: String,
    /// 返回结果数量
    #[serde(default = "default_top_k")]
    pub top_k: usize,
    /// 是否执行 Cross-Encoder 重排
    #[serde(default)]
    pub rerank: bool,
    /// 图扩展深度 (0 = 不扩展)
    #[serde(default)]
    pub expand_depth: u32,
    /// 扩展最大节点数
    #[serde(default = "default_expand_max")]
    pub expand_max_nodes: usize,
    /// 可选过滤器 (文件类型、路径等)
    pub filters: Option<SearchFilters>,
}

fn default_top_k() -> usize { 10 }
fn default_expand_max() -> usize { 50 }

#[derive(Debug, Clone, Serialize, Deserialize, Default)]
pub struct SearchFilters {
    pub file_extensions: Option<Vec<String>>,
    pub exclude_paths: Option<Vec<String>>,
    pub symbol_types: Option<Vec<SymbolType>>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum SymbolType {
    Function,
    Struct,
    Trait,
    Class,
    Method,
    Variable,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct HybridSearchResponse {
    pub results: Vec<RankedCodeNode>,
    /// EvoR 会话令牌，用于错误反馈
    pub evo_token: Option<String>,
    /// 检索统计信息
    pub stats: RetreivalStats,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RetreivalStats {
    pub sparse_top_k: usize,
    pub dense_top_k: usize,
    pub fusion_count: usize,
    pub reranked: bool,
    pub expanded: bool,
    pub latency_ms: u64,
}

// ==================== 排序节点 ====================

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RankedCodeNode {
    /// 节点唯一标识
    pub node_id: String,
    /// 文件路径
    pub file_path: String,
    /// 符号名称
    pub symbol_name: String,
    /// 符号类型
    pub symbol_type: String,
    /// 代码片段内容
    pub code_snippet: String,
    /// 综合评分 (0-1)
    pub score: f32,
    /// 评分构成详情
    pub score_breakdown: ScoreBreakdown,
    /// 位置信息
    pub position: CodePosition,
    /// 扩展上下文节点 IDs (如果执行了图扩展)
    pub context_node_ids: Vec<String>,
    /// 追溯信息
    pub trace: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ScoreBreakdown {
    /// BM25 稀疏分数 (归一化)
    pub sparse_score: f32,
    /// 向量稠密分数 (归一化)
    pub dense_score: f32,
    /// RRF 融合分数
    pub rrf_score: f32,
    /// Cross-Encoder 重排分数 (如执行)
    pub rerank_score: Option<f32>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CodePosition {
    pub line_start: i64,
    pub line_end: i64,
    pub column_start: Option<i64>,
    pub column_end: Option<i64>,
}

// ==================== EvoR 类型 ====================

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct EvoSearchRequest {
    /// 前次搜索的 evo_token
    pub evo_token: String,
    /// 编译错误诊断信息
    pub error_diagnostics: Vec<CompileErrorInfo>,
    /// 原始查询
    pub original_query: String,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CompileErrorInfo {
    pub file_path: String,
    pub line: usize,
    pub column: Option<usize>,
    pub message: String,
    /// 错误分类
    pub error_type: ErrorType,
    /// 提取的未解析符号名 (如有)
    pub unresolved_symbols: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum ErrorType {
    UnresolvedReference,
    TypeMismatch,
    MissingTrait,
    MissingMethod,
    MissingImport,
    SyntaxError,
    Other(String),
}

// ==================== 中间状态 ====================

#[derive(Debug, Clone)]
pub struct ScoredCandidate {
    pub node_id: String,
    pub sparse_score: f32,
    pub dense_score: f32,
    pub rrf_score: f32,
    pub file_path: String,
    pub symbol_name: String,
    pub code_snippet: String,
    pub line_start: i64,
    pub line_end: i64,
    pub language: String,
}

#[derive(Debug, Clone)]
pub struct EvoSession {
    pub token: String,
    pub original_query: String,
    pub attempt: u32,
    pub max_attempts: u32,
    pub history: Vec<SessionStep>,
    pub error_accumulator: ErrorAccumulator,
}

#[derive(Debug, Clone)]
pub struct SessionStep {
    pub attempt: u32,
    pub results: Vec<ScoredCandidate>,
    pub errors: Vec<CompileErrorInfo>,
}

#[derive(Debug, Clone, Default)]
pub struct ErrorAccumulator {
    pub unresolved_symbols: Vec<String>,
    pub error_messages: Vec<String>,
    pub affected_files: Vec<String>,
}
```

### 3.2 Hybrid 参数配置 (`config.rs` 扩展)

```rust
// 在 CodeBaseConfig 中新增
#[derive(Debug, Deserialize, Clone)]
pub struct RetrievalConfig {
    /// 是否启用混合检索
    #[serde(default = "default_true")]
    pub enable_hybrid: bool,
    /// RRF 融合常数 k
    #[serde(default = "default_rrf_k")]
    pub rrf_k: f32,
    /// 稀疏检索 Top-K
    #[serde(default = "default_sparse_top_k")]
    pub sparse_top_k: usize,
    /// 稠密检索 Top-K
    #[serde(default = "default_dense_top_k")]
    pub dense_top_k: usize,
    /// 融合后进入重排的候选数
    #[serde(default = "default_rrf_top_k")]
    pub rrf_top_k: usize,
    /// 重排后返回的 Top-N
    #[serde(default = "default_rerank_top_n")]
    pub rerank_top_n: usize,
    /// 是否启用图扩展
    #[serde(default)]
    pub enable_expansion: bool,
    /// 默认图扩展深度
    #[serde(default = "default_expand_depth")]
    pub expand_depth: u32,
    /// 最大扩展节点数
    #[serde(default = "default_expand_max")]
    pub expand_max_nodes: usize,
    /// Cross-Encoder 模型路径
    pub reranker_model_path: Option<String>,
    /// Cross-Encoder tokenizer 路径
    pub reranker_tokenizer_path: Option<String>,
}

fn default_true() -> bool { true }
fn default_rrf_k() -> f32 { 60.0 }
fn default_sparse_top_k() -> usize { 100 }
fn default_dense_top_k() -> usize { 100 }
fn default_rrf_top_k() -> usize { 40 }
fn default_rerank_top_n() -> usize { 10 }
fn default_expand_depth() -> u32 { 2 }
fn default_expand_max() -> usize { 50 }
```

## 四、核心算法实现

### 4.1 Hybrid Fusion - RRF 融合 (`retrieval/hybrid.rs`)

```rust
use std::collections::HashMap;
use tracing::{info, debug};
use crate::retrieval::types::{HybridSearchRequest, HybridSearchResponse, RetreivalStats, ScoredCandidate, SearchFilters};
use super::sparse::SparseRetriever;
use super::dense::DenseRetriever;
use super::reranker::CrossEncoder;
use super::expansion::GraphExpander;

pub struct HybridRetriever {
    sparse: SparseRetriever,
    dense: DenseRetriever,
    reranker: Option<CrossEncoder>,
    expander: GraphExpander,
    rrf_k: f32,
    sparse_top_k: usize,
    dense_top_k: usize,
    rrf_top_k: usize,
    rerank_top_n: usize,
    expand_depth: u32,
    expand_max_nodes: usize,
}

impl HybridRetriever {
    /// 执行完整的混合检索流水线
    pub async fn search(
        &self,
        query: &str,
        filters: Option<&SearchFilters>,
    ) -> Result<HybridSearchResponse, Box<dyn std::error::Error>> {
        let start = std::time::Instant::now();
        
        // 步骤1: 并行执行稀疏检索和稠密检索
        let (sparse_results, dense_results) = tokio::join!(
            self.sparse.search(query, self.sparse_top_k, filters),
            self.dense.search(query, self.dense_top_k, filters),
        );
        
        let sparse_results = sparse_results?;
        let dense_results = dense_results?;
        
        debug!(
            "Sparse: {} results, Dense: {} results",
            sparse_results.len(), dense_results.len()
        );
        
        // 步骤2: RRF 融合
        let fused = self.reciprocal_rank_fusion(
            &sparse_results, &dense_results,
        );
        
        // 步骤3: 交叉编码重排 (可选)
        let reranked = if let Some(reranker) = &self.reranker {
            self.rerank_candidates(reranker, query, &fused).await?
        } else {
            fused.iter().map(|c| c.clone()).collect()
        };
        
        // 步骤4: 图扩展 (可选)
        let expanded = if self.expand_depth > 0 {
            self.expand_results(&reranked).await?
        } else {
            reranked
        };
        
        let elapsed = start.elapsed().as_millis() as u64;
        
        Ok(HybridSearchResponse {
            results: expanded,
            evo_token: Some(uuid::Uuid::new_v4().to_string()),
            stats: RetreivalStats {
                sparse_top_k: self.sparse_top_k,
                dense_top_k: self.dense_top_k,
                fusion_count: fused.len(),
                reranked: self.reranker.is_some(),
                expanded: self.expand_depth > 0,
                latency_ms: elapsed,
            },
        })
    }
    
    /// 互反排名融合 (RRF)
    /// RRF(d) = Σ_{r∈R} 1 / (k + rank_r(d))
    fn reciprocal_rank_fusion(
        &self,
        sparse: &[ScoredCandidate],
        dense: &[ScoredCandidate],
    ) -> Vec<ScoredCandidate> {
        let mut rrf_scores: HashMap<String, f32> = HashMap::new();
        let mut node_scores: HashMap<String, (f32, f32)> = HashMap::new();
        
        // 计算稀疏排名贡献
        for (rank, candidate) in sparse.iter().enumerate() {
            let rrf_contribution = 1.0 / (self.rrf_k + (rank + 1) as f32);
            *rrf_scores.entry(candidate.node_id.clone()).or_insert(0.0) += rrf_contribution;
            node_scores.insert(
                candidate.node_id.clone(),
                (candidate.sparse_score, candidate.dense_score),
            );
        }
        
        // 计算稠密排名贡献
        for (rank, candidate) in dense.iter().enumerate() {
            let rrf_contribution = 1.0 / (self.rrf_k + (rank + 1) as f32);
            *rrf_scores.entry(candidate.node_id.clone()).or_insert(0.0) += rrf_contribution;
            if !node_scores.contains_key(&candidate.node_id) {
                node_scores.insert(
                    candidate.node_id.clone(),
                    (candidate.sparse_score, candidate.dense_score),
                );
            }
        }
        
        // 构建融合结果
        let mut fused: Vec<ScoredCandidate> = rrf_scores.into_iter()
            .map(|(node_id, rrf_score)| {
                let (sparse_score, dense_score) = node_scores.get(&node_id).copied().unwrap_or((0.0, 0.0));
                ScoredCandidate {
                    node_id,
                    sparse_score,
                    dense_score,
                    rrf_score,
                    file_path: String::new(), // 从原结果获取
                    symbol_name: String::new(),
                    code_snippet: String::new(),
                    line_start: 0,
                    line_end: 0,
                    language: String::new(),
                }
            })
            .collect();
        
        // 按 RRF 分数排序
        fused.sort_by(|a, b| b.rrf_score.partial_cmp(&a.rrf_score).unwrap());
        
        // 截断到目标数量
        fused.truncate(self.rrf_top_k);
        
        info!("Fused to {} candidates via RRF", fused.len());
        fused
    }
    
    /// 候选重排 (占位方法，实际由 CrossEncoder 实现)
    async fn rerank_candidates(
        &self,
        _reranker: &CrossEncoder,
        _query: &str,
        _candidates: &[ScoredCandidate],
    ) -> Result<Vec<crate::retrieval::types::RankedCodeNode>, Box<dyn std::error::Error>> {
        todo!("实现 Cross-Encoder 重排逻辑")
    }
    
    /// 图扩展 (占位方法，实际由 GraphExpander 实现)
    async fn expand_results(
        &self,
        _candidates: &[crate::retrieval::types::RankedCodeNode],
    ) -> Result<Vec<crate::retrieval::types::RankedCodeNode>, Box<dyn std::error::Error>> {
        todo!("实现图扩展逻辑")
    }
}
```

### 4.2 Cloud Reranker - 云端 Reranker API 客户端 (`retrieval/reranker.rs`)

```rust
use anyhow::{Result, anyhow};
use reqwest::Client;
use serde::{Deserialize, Serialize};
use serde_json::json;
use std::time::Duration;
use tracing::{info, warn, debug};

use crate::retrieval::types::ScoredCandidate;

// ==================== 统一接口 ====================

/// Reranker API 后端类型
pub enum RerankerProvider {
    Bge,      // BGE-Reranker (OpenAI 兼容)
    Cohere,   // Cohere Rerank API
    Jina,     // Jina Reranker
}

impl std::fmt::Display for RerankerProvider {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Bge => write!(f, "bge"),
            Self::Cohere => write!(f, "cohere"),
            Self::Jina => write!(f, "jina"),
        }
    }
}

// ==================== 配置 ====================

#[derive(Debug, Clone)]
pub struct RerankerConfig {
    /// API 提供商
    pub provider: RerankerProvider,
    /// API 基础 URL
    pub api_url: String,
    /// API 认证令牌
    pub api_token: String,
    /// 模型名称
    pub model: String,
    /// 返回 Top-N 数量
    pub top_n: usize,
    /// 请求超时 (秒)
    pub timeout_secs: u64,
    /// 最大重试次数
    pub max_retries: u32,
}

impl RerankerConfig {
    pub fn new(provider: RerankerProvider, api_url: &str, api_token: &str, model: &str) -> Self {
        Self {
            provider,
            api_url: api_url.to_string(),
            api_token: api_token.to_string(),
            model: model.to_string(),
            top_n: 10,
            timeout_secs: 5,
            max_retries: 3,
        }
    }
}

// ==================== 统一请求/响应类型 ====================

#[derive(Debug, Clone, Serialize)]
pub struct RerankRequest {
    pub query: String,
    pub documents: Vec<String>,
    pub top_n: usize,
}

#[derive(Debug, Clone)]
pub struct ScoredDocument {
    pub index: usize,
    pub relevance_score: f32,
}

// ==================== 统一错误类型 ====================

#[derive(Debug)]
pub enum RerankerError {
    /// HTTP 错误 (网络、超时)
    Http(reqwest::Error),
    /// API 返回非 2xx 状态码
    ApiError(reqwest::StatusCode, String),
    /// JSON 解析错误
    Deserialize(reqwest::Error),
    /// 空文档列表
    EmptyDocuments,
    /// 其他错误
    Other(String),
}

impl std::fmt::Display for RerankerError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            Self::Http(e) => write!(f, "HTTP error: {}", e),
            Self::ApiError(code, body) => write!(f, "API error {}: {}", code, body),
            Self::Deserialize(e) => write!(f, "Deserialize error: {}", e),
            Self::EmptyDocuments => write!(f, "Empty documents list"),
            Self::Other(msg) => write!(f, "Error: {}", msg),
        }
    }
}

impl std::error::Error for RerankerError {}

// ==================== Reranker 主结构体 ====================

pub struct CloudReranker {
    client: Client,
    config: RerankerConfig,
}

impl CloudReranker {
    /// 创建 Reranker 实例
    pub fn new(config: RerankerConfig) -> Self {
        let client = Client::builder()
            .timeout(Duration::from_secs(config.timeout_secs))
            .build()
            .expect("Failed to create HTTP client");
        
        Self { client, config }
    }
    
    /// 对候选列表进行重排序
    pub async fn rerank(
        &self,
        query: &str,
        candidates: &[ScoredCandidate],
    ) -> Result<Vec<ScoredCandidate>, RerankerError> {
        if candidates.is_empty() {
            return Ok(Vec::new());
        }
        
        // 准备文档列表 (截断每个文档到 1024 字符，控制 payload 大小)
        let documents: Vec<String> = candidates.iter()
            .map(|c| {
                let snippet = &c.code_snippet;
                if snippet.len() > 1024 {
                    snippet[..1024].to_string()
                } else {
                    snippet.clone()
                }
            })
            .collect();
        
        // 创建请求
        let request = RerankRequest {
            query: query.to_string(),
            documents,
            top_n: self.config.top_n,
        };
        
        // 执行带重试的 API 调用
        self._rerank_with_retry(&request).await
    }
    
    /// 带重试的重排序 (指数退避)
    async fn _rerank_with_retry(
        &self,
        request: &RerankRequest,
    ) -> Result<Vec<ScoredCandidate>, RerankerError> {
        let mut attempts = 0;
        let mut delay_ms = 100;
        
        loop {
            attempts += 1;
            
            match self._call_api(request).await {
                Ok(scores) => return Ok(scores),
                Err(e) => {
                    // 只有可重试错误才重试
                    if self._is_retryable_error(&e) && attempts < self.config.max_retries {
                        warn!(
                            "Rerank API call failed (attempt {}/{}): {:?}, retrying in {}ms",
                            attempts, self.config.max_retries, e, delay_ms
                        );
                        
                        tokio::time::sleep(Duration::from_millis(delay_ms)).await;
                        delay_ms *= 2; // 指数退避
                    } else {
                        return Err(e);
                    }
                }
            }
        }
    }
    
    /// 判断是否为可重试错误
    fn _is_retryable_error(&self, error: &RerankerError) -> bool {
        match error {
            RerankerError::Http(_) => true,
            RerankerError::ApiError(code, _) => matches!(code, reqwest::StatusCode::TOO_MANY_REQUESTS | reqwest::StatusCode::SERVICE_UNAVAILABLE | reqwest::StatusCode::GATEWAY_TIMEOUT),
            _ => false,
        }
    }
    
    /// 调用 Reranker API (根据提供商类型)
    async fn _call_api(&self, request: &RerankRequest) -> Result<Vec<ScoredCandidate>, RerankerError> {
        match self.config.provider {
            RerankerProvider::Bge => self._call_bge_api(request).await,
            RerankerProvider::Cohere => self._call_cohere_api(request).await,
            RerankerProvider::Jina => self._call_jina_api(request).await,
        }
    }
    
    /// BGE-Reranker API (OpenAI 兼容格式)
    /// POST {base_url}/rerank
    async fn _call_bge_api(&self, request: &RerankRequest) -> Result<Vec<ScoredCandidate>, RerankerError> {
        let body = json!({
            "model": self.config.model,
            "query": request.query,
            "documents": request.documents,
            "top_n": request.top_n,
        });
        
        let response = self.client
            .post(format!("{}/rerank", self.config.api_url))
            .header("Authorization", format!("Bearer {}", self.config.api_token))
            .header("Content-Type", "application/json")
            .json(&body)
            .send()
            .await
            .map_err(RerankerError::Http)?;
        
        if !response.status().is_success() {
            let status = response.status();
            let body = response.text().await.unwrap_or_default();
            return Err(RerankerError::ApiError(status, body));
        }
        
        let resp: BgeResponse = response.json().await
            .map_err(RerankerError::Deserialize)?;
        
        // 转换为 ScoredCandidate
        let mut results: Vec<ScoredCandidate> = resp.results.into_iter()
            .map(|r| ScoredCandidate {
                index: r.index,
                sparse_score: 0.0,
                dense_score: 0.0,
                rrf_score: r.relevance_score,
                file_path: String::new(),
                symbol_name: String::new(),
                code_snippet: String::new(),
                line_start: 0,
                line_end: 0,
                language: String::new(),
                score: r.relevance_score,
                score_breakdown: crate::retrieval::types::ScoreBreakdown {
                    sparse_score: 0.0,
                    dense_score: 0.0,
                    rrf_score: 0.0,
                    rerank_score: Some(r.relevance_score),
                },
                position: crate::retrieval::types::CodePosition {
                    line_start: 0,
                    line_end: 0,
                    column_start: None,
                    column_end: None,
                },
                context_node_ids: vec![],
                trace: format!("BGE Rerank: {:.2}", r.relevance_score),
            })
            .collect();
        
        // 按分数排序
        results.sort_by(|a, b| b.score.partial_cmp(&a.score).unwrap());
        
        Ok(results)
    }
    
    /// Cohere Rerank API
    /// POST https://api.cohere.ai/v1/rerank
    async fn _call_cohere_api(&self, request: &RerankRequest) -> Result<Vec<ScoredCandidate>, RerankerError> {
        let body = json!({
            "model": self.config.model,
            "query": request.query,
            "documents": request.documents,
            "top_n": request.top_n,
        });
        
        let response = self.client
            .post("https://api.cohere.ai/v1/rerank")
            .header("Authorization", format!("Bearer {}", self.config.api_token))
            .header("Content-Type", "application/json")
            .json(&body)
            .send()
            .await
            .map_err(RerankerError::Http)?;
        
        if !response.status().is_success() {
            let status = response.status();
            let body = response.text().await.unwrap_or_default();
            return Err(RerankerError::ApiError(status, body));
        }
        
        let resp: CohereResponse = response.json().await
            .map_err(RerankerError::Deserialize)?;
        
        let mut results: Vec<ScoredCandidate> = resp.results.into_iter()
            .map(|r| ScoredCandidate {
                index: r.index,
                sparse_score: 0.0,
                dense_score: 0.0,
                rrf_score: r.relevance_score,
                file_path: String::new(),
                symbol_name: String::new(),
                code_snippet: String::new(),
                line_start: 0,
                line_end: 0,
                language: String::new(),
                score: r.relevance_score,
                score_breakdown: crate::retrieval::types::ScoreBreakdown {
                    sparse_score: 0.0,
                    dense_score: 0.0,
                    rrf_score: 0.0,
                    rerank_score: Some(r.relevance_score),
                },
                position: crate::retrieval::types::CodePosition {
                    line_start: 0,
                    line_end: 0,
                    column_start: None,
                    column_end: None,
                },
                context_node_ids: vec![],
                trace: format!("Cohere Rerank: {:.2}", r.relevance_score),
            })
            .collect();
        
        results.sort_by(|a, b| b.score.partial_cmp(&a.score).unwrap());
        
        Ok(results)
    }
    
    /// Jina Reranker API
    /// POST https://api.jina.ai/v1/rerank
    async fn _call_jina_api(&self, request: &RerankRequest) -> Result<Vec<ScoredCandidate>, RerankerError> {
        let body = json!({
            "model": self.config.model,
            "query": request.query,
            "documents": request.documents,
            "top_k": request.top_n,
        });
        
        let response = self.client
            .post("https://api.jina.ai/v1/rerank")
            .header("Authorization", format!("Bearer {}", self.config.api_token))
            .header("Content-Type", "application/json")
            .json(&body)
            .send()
            .await
            .map_err(RerankerError::Http)?;
        
        if !response.status().is_success() {
            let status = response.status();
            let body = response.text().await.unwrap_or_default();
            return Err(RerankerError::ApiError(status, body));
        }
        
        let resp: JinaResponse = response.json().await
            .map_err(RerankerError::Deserialize)?;
        
        let mut results: Vec<ScoredCandidate> = resp.results.into_iter()
            .map(|r| ScoredCandidate {
                index: r.index,
                sparse_score: 0.0,
                dense_score: 0.0,
                rrf_score: r.relevance_score,
                file_path: String::new(),
                symbol_name: String::new(),
                code_snippet: String::new(),
                line_start: 0,
                line_end: 0,
                language: String::new(),
                score: r.relevance_score,
                score_breakdown: crate::retrieval::types::ScoreBreakdown {
                    sparse_score: 0.0,
                    dense_score: 0.0,
                    rrf_score: 0.0,
                    rerank_score: Some(r.relevance_score),
                },
                position: crate::retrieval::types::CodePosition {
                    line_start: 0,
                    line_end: 0,
                    column_start: None,
                    column_end: None,
                },
                context_node_ids: vec![],
                trace: format!("Jina Rerank: {:.2}", r.relevance_score),
            })
            .collect();
        
        results.sort_by(|a, b| b.score.partial_cmp(&a.score).unwrap());
        
        Ok(results)
    }
}

// ==================== 各 API 响应类型 ====================

#[derive(Debug, Deserialize)]
struct BgeResponse {
    results: Vec<BgeResult>,
}

#[derive(Debug, Deserialize)]
struct BgeResult {
    index: usize,
    relevance_score: f32,
}

#[derive(Debug, Deserialize)]
struct CohereResponse {
    results: Vec<CohereResult>,
}

#[derive(Debug, Deserialize)]
struct CohereResult {
    index: usize,
    relevance_score: f32,
}

#[derive(Debug, Deserialize)]
struct JinaResponse {
    results: Vec<JinaResult>,
}

#[derive(Debug, Deserialize)]
struct JinaResult {
    index: usize,
    relevance_score: f32,
}
```

### 4.3 图扩展 (`retrieval/expansion.rs`)

```rust
use std::collections::{HashMap, HashSet, VecDeque};
use tracing::debug;
use crate::codegraph::graph::CodeGraph;

pub struct GraphExpander {
    graph: std::sync::Arc<CodeGraph>,
}

/// 扩展的子图结果
pub struct ExpandedContext {
    /// 种子节点及扩展节点: (node_id, depth_from_seed)
    pub nodes: Vec<(String, u32)>,
    /// 扩展的关联节点 IDs
    pub expanded_node_ids: Vec<String>,
}

impl GraphExpander {
    pub fn new(graph: std::sync::Arc<CodeGraph>) -> Self {
        Self { graph }
    }
    
    /// 从种子节点集合开始 BFS 扩展
    /// 
    /// 扩展方向:
    /// - Outgoing: 调用者 (Callees) - 该函数调用了谁
    /// - Incoming: 被调用者 (Callers) - 谁调用了该函数
    /// - 额外: Imports、Trait Implementations
    pub fn expand(
        &self,
        seed_node_ids: &[String],
        max_depth: u32,
        max_nodes: usize,
    ) -> ExpandedContext {
        debug!("Expanding {} seed nodes, depth={}, max_nodes={}",
            seed_node_ids.len(), max_depth, max_nodes);
        
        let mut queue: VecDeque<(String, u32)> = VecDeque::new();
        let mut visited: HashSet<String> = HashSet::new();
        let mut expanded: Vec<(String, u32)> = Vec::new();
        
        // 初始化种子节点
        for id in seed_node_ids {
            if visited.insert(id.clone()) {
                queue.push_back((id.clone(), 0));
            }
        }
        
        while let Some((node_id, depth)) = queue.pop_front() {
            if depth > max_depth || expanded.len() >= max_nodes {
                break;
            }
            
            expanded.push((node_id.clone(), depth));
            
            if depth < max_depth {
                // 扩展邻居节点
                self.expand_neighbors(&node_id, depth + 1, &mut queue, &mut visited, max_nodes - expanded.len());
            }
        }
        
        let expanded_ids: Vec<String> = expanded.iter()
            .filter(|(id, _)| !seed_node_ids.contains(id))
            .map(|(id, _)| id.clone())
            .collect();
        
        debug!("Expanded to {} total nodes, {} new", expanded.len(), expanded_ids.len());
        
        ExpandedContext {
            nodes: expanded,
            expanded_node_ids: expanded_ids,
        }
    }
    
    /// 扩展单个节点的邻居
    fn expand_neighbors(
        &self,
        node_id: &str,
        next_depth: u32,
        queue: &mut VecDeque<(String, u32)>,
        visited: &mut HashSet<String>,
        remaining: usize,
    ) {
        // 查找节点在 PetGraph 中的索引
        // 注意: 这里需要根据实际的 CodeGraph 实现调整
        if let Some(_node_idx) = self.find_node_index(node_id) {
            // 1. 扩展 Outgoing 边 (该函数调用的目标)
            // 2. 扩展 Incoming 边 (调用该函数的源)
            // 3. 扩展 Imports
            self.expand_imports(node_id, next_depth, queue, visited, remaining);
            
            // 4. 扩展 Trait Implementations
            self.expand_trait_impls(node_id, next_depth, queue, visited, remaining);
        }
    }
    
    /// 在代码图中查找节点索引
    fn find_node_index(&self, _node_id: &str) -> Option<usize> {
        // TODO: 从 CodeGraph 的 HashMap 中查找节点索引
        None
    }
    
    /// 根据导入关系扩展
    fn expand_imports(&self, _node_id: &str, _depth: u32, _queue: &mut VecDeque<(String, u32)>, _visited: &mut HashSet<String>, _remaining: usize) {
        // TODO: 根据导入关系扩展
    }
    
    /// 根据 trait 实现关系扩展
    fn expand_trait_impls(&self, _node_id: &str, _depth: u32, _queue: &mut VecDeque<(String, u32)>, _visited: &mut HashSet<String>, _remaining: usize) {
        // TODO: 根据 trait 实现关系扩展
    }
}
```

### 4.4 EvoR - 演进检索 (`retrieval/evor.rs`)

```rust
use std::collections::HashMap;
use std::sync::{Arc, RwLock};
use anyhow::{Result, anyhow};
use tracing::{info, warn};
use uuid::Uuid;
use crate::retrieval::types::*;

/// EvoR 会话管理器
pub struct EvoRSessionManager {
    sessions: Arc<RwLock<HashMap<String, EvoSession>>>,
}

impl EvoRSessionManager {
    pub fn new() -> Self {
        Self {
            sessions: Arc::new(RwLock::new(HashMap::new())),
        }
    }
    
    /// 创建新会话
    pub fn create_session(&self, original_query: &str, max_attempts: u32) -> String {
        let token = Uuid::new_v4().to_string();
        let session = EvoSession {
            token: token.clone(),
            original_query: original_query.to_string(),
            attempt: 0,
            max_attempts,
            history: Vec::new(),
            error_accumulator: ErrorAccumulator::default(),
        };
        self.sessions.write().unwrap().insert(token.clone(), session);
        token
    }
    
    /// 获取会话 (带所有权转移用于更新)
    pub fn get_session(&self, token: &str) -> Option<EvoSession> {
        self.sessions.read().unwrap().get(token).cloned()
    }
    
    /// 更新会话
    pub fn update_session(&self, token: &str, mut session: EvoSession) {
        self.sessions.write().unwrap().insert(token.to_string(), session);
    }
    
    /// 删除会话
    pub fn remove_session(&self, token: &str) {
        self.sessions.write().unwrap().remove(token);
    }
}

/// EvoR 搜索处理
pub struct EvoRProcessor {
    session_manager: Arc<EvoRSessionManager>,
    hybrid_retriever: Arc<HybridRetriever>,
    default_rrf_k: f32,
}

impl EvoRProcessor {
    pub fn new(
        session_manager: Arc<EvoRSessionManager>,
        hybrid_retriever: Arc<HybridRetriever>,
        default_rrf_k: f32,
    ) -> Self {
        Self {
            session_manager,
            hybrid_retriever,
            default_rrf_k,
        }
    }
    
    /// 执行演进检索
    pub async fn search(
        &self,
        req: EvoSearchRequest,
    ) -> Result<HybridSearchResponse> {
        // 获取或创建会话
        let session = self.session_manager.get_session(&req.evo_token)
            .ok_or_else(|| anyhow!("Session not found: {}", req.evo_token))?;
        
        let attempt = session.attempt + 1;
        if attempt > session.max_attempts {
            return Err(anyhow!("Maximum retry attempts ({}) exceeded", session.max_attempts));
        }
        
        info!("EvoR attempt {}: processing {} error diagnostics", attempt, req.error_diagnostics.len());
        
        // 步骤1: 提取错误特征
        let features = self.extract_error_features(&req.error_diagnostics);
        
        // 步骤2: 增强查询
        let augmented_query = self.augment_query(
            &req.original_query,
            &features,
            attempt,
        );
        
        // 步骤3: 调整检索参数
        let adjusted_rrf_k = if attempt == 1 {
            self.default_rrf_k
        } else {
            // 后续迭代中增大 k 值，让排名更均匀，更重视精确匹配
            self.default_rrf_k + (attempt as f32 * 20.0)
        };
        
        // 步骤4: 执行混合检索
        // 注意: 需要在 HybridRetriever 中添加 search_with_params 方法
        let response = self.hybrid_retriever
            .search(&augmented_query, None)
            .await?;
        
        // 步骤5: 更新会话
        let mut updated_session = session;
        updated_session.attempt = attempt;
        updated_session.error_accumulator.merge(&features);
        self.session_manager.update_session(&req.evo_token, updated_session);
        
        info!("EvoR attempt {} complete, total results: {}", attempt, response.results.len());
        
        Ok(response)
    }
    
    /// 从编译错误中提取特征
    fn extract_error_features(&self, diagnostics: &[CompileErrorInfo]) -> ErrorAccumulator {
        let mut accumulator = ErrorAccumulator::default();
        
        for diag in diagnostics {
            // 收集未解析符号
            accumulator.unresolved_symbols.extend(diag.unresolved_symbols.clone());
            
            // 收集错误消息
            accumulator.error_messages.push(diag.message.clone());
            
            // 收集受影响文件
            accumulator.affected_files.push(diag.file_path.clone());
        }
        
        // 去重
        accumulator.unresolved_symbols.sort();
        accumulator.unresolved_symbols.dedup();
        accumulator.affected_files.sort();
        accumulator.affected_files.dedup();
        
        accumulator
    }
    
    /// 增强查询：将错误信息融入查询
    fn augment_query(
        &self,
        original_query: &str,
        features: &ErrorAccumulator,
        attempt: u32,
    ) -> String {
        let mut augmented = original_query.to_string();
        
        // 添加未解析符号的显式引用
        if !features.unresolved_symbols.is_empty() {
            let symbols_str = features.unresolved_symbols.join(" ");
            augmented.push_str(&format!(
                " [SOLVE: unresolved symbols: {}]",
                symbols_str
            ));
        }
        
        // 添加错误消息关键词
        if attempt > 1 && !features.error_messages.is_empty() {
            let keywords = self.extract_keywords(&features.error_messages);
            if !keywords.is_empty() {
                augmented.push_str(&format!(
                    " [FIX: {}]",
                    keywords.join(" ")
                ));
            }
        }
        
        augmented
    }
    
    /// 从错误消息中提取关键词
    fn extract_keywords(&self, messages: &[String]) -> Vec<String> {
        let mut keywords = Vec::new();
        for msg in messages {
            // 简单的关键词提取：按空格分割，过滤常见停用词
            for word in msg.split_whitespace() {
                let lower = word.to_lowercase();
                if !STOPWORDS.contains(&lower.as_str()) && word.len() > 3 {
                    keywords.push(lower);
                }
            }
        }
        keywords.sort();
        keywords.dedup();
        keywords
    }
}

/// 错误累加器合并
impl ErrorAccumulator {
    fn merge(&mut self, other: &ErrorAccumulator) {
        self.unresolved_symbols.extend(other.unresolved_symbols.iter().cloned());
        self.error_messages.extend(other.error_messages.iter().cloned());
        self.affected_files.extend(other.affected_files.iter().cloned());
        
        self.unresolved_symbols.sort();
        self.unresolved_symbols.dedup();
        self.error_messages.sort();
        self.error_messages.dedup();
        self.affected_files.sort();
        self.affected_files.dedup();
    }
}

const STOPWORDS: &[&str] = &[
    "the", "a", "an", "is", "are", "was", "were", "be", "been",
    "this", "that", "these", "those", "in", "on", "at", "to", "for",
    "with", "by", "from", "of", "and", "or", "not", "no", "but",
];
```

### 4.5 稀疏检索 (Tantivy BM25) 概览

```rust
// retrieval/sparse.rs

use tantivy::{
    index::{Index, IndexBuilder, OpenMode},
    schema::{Schema, TextFieldIndexing, STORED, FAST, TEXT},
    directory::MmapDirectory,
    query::TermQuery,
    reader::Searcher,
    doc,
    Term,
};
use std::sync::Arc;
use parking_lot::RwLock;

pub struct SparseRetriever {
    index: Index,
    schema: Schema,
    searcher: RwLock<Option<Searcher>>,
    writer: Arc<parking_lot::Mutex<tantivy::IndexWriter>>,
}

impl SparseRetriever {
    /// 创建或打开 Tantivy 索引
    pub fn open(index_path: &str) -> Result<Self, tantivy::TantivyError> {
        let mut schema_builder = Schema::builder();
        let node_id = schema_builder.add_text_field("node_id", TEXT | STORED);
        let file_path = schema_builder.add_text_field("file_path", TEXT | FAST);
        let code = schema_builder.add_text_field("code", TEXT | STORED);
        let symbol_name = schema_builder.add_text_field("symbol_name", TEXT | FAST);
        let language = schema_builder.add_text_field("language", TEXT | FAST);
        let schema = schema_builder.build();
        
        let directory = MmapDirectory::open(&std::path::Path::new(index_path))?;
        let index = IndexBuilder::new()
            .schema(schema.clone())
            .build()?;
        
        Ok(Self {
            index,
            schema,
            searcher: RwLock::new(None),
            writer: Arc::new(parking_lot::Mutex::new(
                index.writer(150_000_000)?
            )),
        })
    }
    
    /// 索引代码节点
    pub fn index_node(&self, node_id: &str, file_path: &str, code: &str, symbol_name: &str, language: &str) -> Result<()> {
        let mut writer = self.writer.lock();
        
        let mut document = doc!(
            node_id => node_id,
            file_path => file_path,
            code => code,
            symbol_name => symbol_name,
            language => language,
        );
        
        writer.add_document(document)?;
        writer.commit()?;
        
        Ok(())
    }
    
    /// 执行 BM25 搜索
    pub fn search(&self, query: &str, top_k: usize) -> Result<Vec<ScoredCandidate>, tantivy::TantivyError> {
        let searcher = self.searcher.read();
        let searcher = searcher.as_ref()
            .ok_or(tantivy::TantivyError::FileOpenError(
                "Searcher not ready".to_string(),
            ))?;
        
        // 使用 tantivy 的 QueryParser 执行 BM25 搜索
        // 简化版实现
        todo!("实现 BM25 搜索逻辑")
    }
    
    /// 更新搜索器引用 (在 commit 后调用)
    pub fn reload_searcher(&mut self) -> Result<()> {
        let searcher = self.index.reader()?.searcher();
        *self.searcher.write() = Some(searcher);
        Ok(())
    }
}
```

## 五、HTTP Handler 定义

### 5.1 路由注册 (`http/server.rs` 修改)

```rust
// 在 create_router 方法中新增:

fn create_router(&self) -> Router {
    Router::new()
        // 原有路由...
        .route("/health", get(handlers::health_check))
        .route("/status", get(handlers::get_status))
        .route("/semantic_search", post(handlers::vectorize::semantic_search))
        .route("/query_call_graph", post(handlers::query_call_graph))
        .route("/query_code_snippet", post(handlers::query_code_snippet))
        // ... 其他原有路由
        
        // ==================== 新增路由 ====================
        .route("/hybrid_search", post(handlers::hybrid::hybrid_search_handler))
        .route("/evor_search", post(handlers::evor::evor_search_handler))
        .route("/rerank", post(handlers::rerank::rerank_handler))
        .route("/evo_token/create", post(handlers::evor::create_evo_session_handler))
        // ================================================
        .layer(CorsLayer::permissive())
        .layer(TraceLayer::new_for_http())
}
```

### 5.2 Hybrid Search Handler (`http/handlers/hybrid.rs`)

```rust
use axum::{
    extract::State,
    http::StatusCode,
    response::Json,
    Json as JsonBody,
};
use serde_json::json;
use tracing::info;
use crate::retrieval::types::*;
use crate::storage::StorageManager;

pub async fn hybrid_search_handler(
    State(storage): State<Arc<StorageManager>>,
    JsonBody(request): JsonBody<HybridSearchRequest>,
) -> Result<Json<HybridSearchResponse>, (StatusCode, Json<serde_json::Value>)> {
    info!("Hybrid search request: query='{}'", request.query);
    
    let start = std::time::Instant::now();
    
    // 获取 HybridRetriever (需要从 StorageManager 获取)
    let retriever = storage.get_hybrid_retriever()
        .map_err(|e| {
            (
                StatusCode::SERVICE_UNAVAILABLE,
                Json(json!({ "error": format!("Retriever not initialized: {}", e) })),
            )
        })?;
    
    // 执行搜索
    let filters = request.filters.as_ref().map(|f| f);
    let response = retriever.search(&request.query, filters)
        .await
        .map_err(|e| {
            (
                StatusCode::INTERNAL_SERVER_ERROR,
                Json(json!({ "error": format!("Search failed: {}", e) })),
            )
        })?;
    
    let elapsed = start.elapsed().as_millis();
    info!(
        "Hybrid search complete: {} results in {}ms",
        response.results.len(), elapsed
    );
    
    Ok(Json(response))
}
```

### 5.3 EvoR Handler (`http/handlers/evor.rs`)

```rust
use axum::{
    extract::State,
    http::StatusCode,
    response::Json,
    Json as JsonBody,
};
use serde_json::json;
use tracing::info;
use crate::retrieval::types::*;
use crate::retrieval::evor::{EvoRProcessor, EvoRSessionManager};
use crate::storage::StorageManager;

pub async fn evor_search_handler(
    State(storage): State<Arc<StorageManager>>,
    JsonBody(request): JsonBody<EvoSearchRequest>,
) -> Result<Json<HybridSearchResponse>, (StatusCode, Json<serde_json::Value>)> {
    info!(
        "EvoR search: token={}, errors={}",
        request.evo_token, request.error_diagnostics.len()
    );
    
    let processor = storage.get_evor_processor()
        .map_err(|e| {
            (
                StatusCode::SERVICE_UNAVAILABLE,
                Json(json!({ "error": format!("EvoR processor not initialized: {}", e) })),
            )
        })?;
    
    let response = processor.search(request)
        .await
        .map_err(|e| {
            (
                StatusCode::BAD_REQUEST,
                Json(json!({ "error": e.to_string() })),
            )
        })?;
    
    Ok(Json(response))
}

pub async fn create_evo_session_handler(
    State(storage): State<Arc<StorageManager>>,
    JsonBody(query): JsonBody<serde_json::Value>,
) -> Result<Json<serde_json::Value>, (StatusCode, Json<serde_json::Value>)> {
    let original_query = query.get("query")
        .and_then(|v| v.as_str())
        .ok_or((
            StatusCode::BAD_REQUEST,
            Json(json!({ "error": "Missing 'query' field" })),
        ))?;
    
    let session_manager = storage.get_evor_session_manager()
        .map_err(|e| {
            (
                StatusCode::SERVICE_UNAVAILABLE,
                Json(json!({ "error": format!("Session manager not initialized: {}", e) })),
            )
        })?;
    
    let token = session_manager.create_session(original_query, 3);
    
    Ok(Json(json!({
        "evo_token": token,
        "max_attempts": 3,
    })))
}
```

## 六、Cargo.toml 新增依赖

```toml
[dependencies]
# 现有依赖保持不变...

# === 新增依赖 ===

# Tantivy 全文检索 (BM25)
tantivy = "0.22"

# Reranker API 客户端 (已有 reqwest，确保包含 json 特性)
reqwest = { version = "0.12", features = ["json", "rustls-tls"] }

# 序列化 (已有，确认)
serde = { version = "1.0", features = ["derive"] }
serde_json = "1.0"

# 异步 trait
async-trait = "0.1"

# 重试机制 (指数退避)
backoff = { version = "0.4", features = ["tokio"] }
```

**说明**：移除了 `ort`、`ndarray`、`tokenizers` 依赖。Reranker 改为通过 HTTP API 调用云端大模型，无需本地推理引擎。

## 七、实施计划 (分阶段)

### Phase 1: 基础设施 (1-2 周)

| 任务 | 文件 | 优先级 |
|------|------|--------|
| 新增 Cargo 依赖 (tantivy, ort, tokenizers, ndarray) | `Cargo.toml` | P0 |
| 创建 `retrieval/` 模块骨架 | `lib.rs`, `retrieval/mod.rs` | P0 |
| 定义类型系统 | `retrieval/types.rs` | P0 |
| 扩展 Config | `config.rs` | P1 |
| 实现 Tantivy 索引管理 | `retrieval/sparse.rs` | P1 |

### Phase 2: Hybrid Fusion (1 周)

| 任务 | 文件 | 优先级 |
|------|------|--------|
| 实现 DenseRetriever 封装 | `retrieval/dense.rs` | P0 |
| 实现 RRF 融合算法 | `retrieval/hybrid.rs` | P0 |
| 单元测试 RRF 逻辑 | `retrieval/hybrid_test.rs` | P1 |

### Phase 3: Cross-Encoder (1-2 周)

| 任务 | 文件 | 优先级 |
|------|------|--------|
| 准备 ONNX 模型 (CodeBERT reranker) | `models/reranker.onnx` | P0 |
| 实现 CrossEncoder 推理 | `retrieval/reranker.rs` | P0 |
| 集成到 Hybrid 流水线 | `retrieval/hybrid.rs` | P1 |

### Phase 4: Graph Expansion (1 周)

| 任务 | 文件 | 优先级 |
|------|------|--------|
| CodeGraph 增加子图方法 | `codegraph/graph.rs` | P0 |
| 实现 BFS 扩展 | `retrieval/expansion.rs` | P0 |
| 集成到 Hybrid 流水线 | `retrieval/hybrid.rs` | P1 |

### Phase 5: EvoR (1 周)

| 任务 | 文件 | 优先级 |
|------|------|--------|
| 实现会话管理 | `retrieval/evor.rs` | P0 |
| 实现错误特征提取 | `retrieval/evor.rs` | P0 |
| 实现查询增强 | `retrieval/evor.rs` | P1 |

### Phase 6: HTTP API (3-5 天)

| 任务 | 文件 | 优先级 |
|------|------|--------|
| 新增路由 | `http/server.rs` | P0 |
| 实现 Handlers | `http/handlers/hybrid.rs`, `evor.rs` | P0 |
| 集成 StorageManager | `storage/mod.rs` | P0 |

### Phase 7: 测试与优化 (1 周)

| 任务 | 文件 | 优先级 |
|------|------|--------|
| 集成测试 | `tests/hybrid_search_integration.rs` | P0 |
| 性能测试 (criterion) | `benches/` | P1 |
| 文档与配置示例 | `docs/` | P1 |

## 八、检索流水线时序图

```
Client                          HybridRetriever           Sparse      Dense      Reranker     Expander
  │                                 │                       │           │            │            │
  │ POST /hybrid_search             │                       │           │            │            │
  │ { query, top_k, rerank, ... }   │                       │           │            │            │
  │────────────────────────────────>│                       │           │            │            │
  │                                 │                       │           │            │            │
  │                                 │──并行执行─────────────>│           │            │            │
  │                                 │                       │           │            │            │
  │                                 │<──────────────────────┤           │            │            │
  │                                 │   BM25 Top-100        │           │            │            │
  │                                 │                       │───>         │            │            │
  │                                 │                       │           │            │            │
  │                                 │<──────────────────────┤───────────┤            │            │
  │                                 │   Vector Top-100      │           │            │            │
  │                                 │                       │           │            │            │
  │                                 │                       │           │            │            │
  │                                 │── RRF 融合 ────────────────────────────────────>│            │
  │                                 │                       │           │            │            │
  │                                 │<──────────────────────┘           │            │            │
  │                                 │   Fused Top-40                    │            │            │
  │                                 │                       │           │            │            │
  │                                 │── Cross-Encoder ─────────────────────────────>│            │
  │                                 │                       │           │            │            │
  │                                 │<──────────────────────────────────────────────┤            │
  │                                 │   Reranked Top-10     │           │            │            │
  │                                 │                       │           │            │            │
  │                                 │── BFS 扩展 ─────────────────────────────────────────────>│
  │                                 │                       │           │            │            │
  │                                 │<──────────────────────────────────────────────────┤            │
  │                                 │   Expanded Context    │           │            │            │
  │                                 │                       │           │            │            │
  │                                 │                       │           │            │            │
  │  HybridSearchResponse           │                       │           │            │            │
  │  { results, evo_token, stats }  │                       │           │            │            │
  │<────────────────────────────────│                       │           │            │            │
  │                                 │                       │           │            │            │
  │  (如果下游编译错误)              │                       │           │            │            │
  │                                 │                       │           │            │            │
  │ POST /evor_search               │                       │           │            │            │
  │ { evo_token, error_diag, ... }  │                       │           │            │            │
  │────────────────────────────────>│                       │           │            │            │
  │                                 │── ErrorExtractor ──────────────────────────────────────────>│
  │                                 │                       │           │            │            │
  │                                 │── QueryAugment ────────────────────────────────────────────>│
  │                                 │                       │           │            │            │
  │                                 │── 再次 HybridSearch (调整参数) ────────────────────────────>│
  │                                 │                       │           │            │            │
  │<────────────────────────────────│                       │           │            │            │
  │  (修正后的结果)                  │                       │           │            │            │
```

## 九、配置示例 (`~/.codeactor/config/config.toml`)

```toml
[codebase]
enable_embedding = true
embedding_db_uri = "~/.codeactor/data/embedding"
graph_db_uri = "~/.codeactor/data/graph"

[codebase.embedding]
model = "Qwen/Qwen3-Embedding-4B"
api_token = "your-api-token"
api_base_url = "https://api.siliconflow.cn/v1"
dimensions = 2560

# === 新增检索配置 ===
[codebase.retrieval]
enable_hybrid = true
enable_expansion = true

# RRF 融合参数
rrf_k = 60.0
sparse_top_k = 100
dense_top_k = 100
rrf_top_k = 40     # 融合后候选数
rerank_top_n = 10  # 重排后返回数

# 图扩展参数
expand_depth = 2
expand_max_nodes = 50

# Cross-Encoder 模型路径 (可选，为空则跳过重排)
# reranker_model_path = "/path/to/reranker.onnx"
# reranker_tokenizer_path = "/path/to/tokenizer.json"
```

## 十、关键设计决策总结

| 决策点 | 选择 | 理由 |
|--------|------|------|
| **融合算法** | RRF (Reciprocal Rank Fusion) | 无需调参，对两种检索尺度不敏感 |
| **BM25 引擎** | Tantivy | Rust 原生，生产级全文检索 |
| **Cross-Encoder** | 云端 Reranker API (BGE/Cohere/Jina) | 无需本地推理引擎，模型灵活可切换，无需 GPU |
| **图扩展** | Petgraph BFS | 复用现有 CodeGraph，无新增存储 |
| **EvoR 会话** | 内存 HashMap | 简单高效，生产环境可替换为 Redis |
| **默认降级** | API 不可用时跳过重排 | 保证系统可用性，支持优雅降级 |

**与旧方案对比**：

| 维度 | 旧方案 (本地 ONNX) | 新方案 (云端 API) |
|------|-------------------|-------------------|
| **内存占用** | ~500MB (模型加载) | <10MB |
| **依赖复杂度** | ort + ndarray + tokenizers | reqwest + serde |
| **模型更新** | 需重新编译/部署 | API 端更新，无需变更 |
| **硬件要求** | 需要 GPU 或高性能 CPU | 任意硬件 |
| **网络延迟** | 无 (本地推理) | 100-500ms (RTT) |
| **可用性** | 100% (本地) | 依赖 API 可用性 |
| **成本** | 免费 | API 调用可能收费 |
| **可扩展性** | 受限于本地硬件 | 云端弹性伸缩 |

---

## 方案总结

此方案完整覆盖了 **Hybrid Search**、**Graph Expansion**、**Cross-Encoder Reranking** 和 **EvoR** 四个核心能力的 Rust 实现设计，可直接作为开发蓝图使用。
