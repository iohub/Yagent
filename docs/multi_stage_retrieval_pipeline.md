# 多阶段级联检索 Pipeline：架构设计与实现路径

> **版本**: v1.0  
> **项目**: codeactor-agent / codexray (Rust 代码分析引擎)  
> **目标读者**: 核心 Rust 开发、AI Agent 架构师、检索系统工程师  
> **文档性质**: 架构设计与实现路径规格书（Architecture RFC）

---

## 1. 概述

### 1.1 项目定位

`codexray` 是 `codeactor-agent` 系统的**代码分析与检索中枢**。它为上层 Agent（Conductor、Coding-Agent、Repo-Agent）提供以下核心能力：

| 能力 | 实现模块 | 技术栈 |
|------|---------|--------|
| **代码解析** | `codegraph/parser.rs`, `codegraph/treesitter/` | TreeSitter AST 解析 |
| **代码图谱** | `codegraph/types.rs` (PetCodeGraph) | petgraph::DiGraph |
| **语义搜索** | `services/embedding_service.rs` | LanceDB + Qwen3-Embedding-4B |
| **增量更新** | `storage/incremental.rs` | MD5 文件变化检测 |
| **持久化** | `storage/persistence.rs` | JSON / Binary 序列化 |
| **HTTP API** | `http/server.rs` | axum web 框架 |

### 1.2 背景与动机

当前系统的检索链路是**单层开环架构**：

```
用户查询 → 单一检索通道 → Top-K 结果 → LLM
```

这种架构存在三个核心问题：

1. **召回精度不足**：仅依赖向量语义搜索（Dense Retrieval），缺少基于词项精确匹配的稀疏检索（Sparse Retrieval），导致函数名、类型名、API 标识符等精确检索场景表现不佳。
2. **上下文碎片化**：向量检索返回的是孤立的代码块（Chunk），缺少代码结构信息——调用关系、类层次、导入依赖等。LLM 难以从孤立片段中理解完整的代码语义。
3. **结果排序粗糙**：向量余弦相似度不等于任务相关性，缺少 Query-Document 交叉编码的精细重排阶段。

### 1.3 目标架构：多阶段级联检索 Pipeline

本文档提出一个**三阶段级联检索 Pipeline**，将检索从"单通道扁平召回"演进为"多阶段逐层精化"：

```
┌─────────────────────────────────────────────────────────────────────┐
│                   多阶段级联检索 Pipeline                             │
│                                                                     │
│  Query ──→ Stage1: Hybrid Search ──→ Stage2: Graph Expansion        │
│               (BM25 + Vector)          (PetCodeGraph BFS)           │
│                  │                          │                       │
│                  ↓                          ↓                       │
│             Stage3: Rerank ←───────── Context Packages              │
│             (Cross-Encoder)                 │                        │
│                  │                          │                       │
│                  ↓                          ↓                       │
│             最终 Prompt Context ────────→ LLM                        │
└─────────────────────────────────────────────────────────────────────┘
```

各阶段职责：

| 阶段 | 名称 | 职责 | 输入 → 输出 |
|------|------|------|------------|
| **Stage 1** | **Hybrid Search** | 高召回率双通道检索 | Query → Top-100 Candidate Snippets |
| **Stage 2** | **Graph Expansion** | 结构上下文扩展 | Seed Snippets → Context Packages |
| **Stage 3** | **Cross-Encoder Rerank** | 高精度重排 | Query + Contexts → Top-10 Ranked Contexts |

---

## 2. 现有检索架构深度分析

### 2.1 索引构建数据流

当前系统的索引构建在 `CodeXRayServer::start()` 启动时触发，完整流程如下：

```
CodeXRayServer::start()
    │
    ├── try_bind_repo() ── 绑定仓库路径
    │
    ├── 加载已有图谱或执行分析
    │   ├── load_graph(project_id) → 尝试从磁盘加载 PetCodeGraph
    │   └── 未命中 → perform_analysis()
    │       ├── CodeParser::build_petgraph_code_graph()
    │       │   [codegraph/parser.rs]
    │       │   ├── scan_directory() → 扫描所有支持的文件
    │       │   ├── parse_file() → TreeSitter AST 解析
    │       │   │   ├── _extract_function_info() ── 提取函数信息
    │       │   │   └── _extract_class_info() ── 提取类信息
    │       │   ├── 增量合并或全量添加函数节点
    │       │   ├── _analyze_petgraph_call_relations()
    │       │   │   └── _analyze_file_calls() ── 建立调用边
    │       │   └── update_stats() ── 更新统计信息
    │       ├── PetGraphStorageManager::save_to_binary()
    │       └── PersistenceManager::save_graph()
    │
    ├── trigger_embedding_build()
    │   [services/embedding_service.rs]
    │   ├── CodeParser::scan_directory() ── 扫描代码文件
    │   ├── TreeSitterParser::parse_file() ── 每文件 AST 解析
    │   ├── EmbeddingProvider::get_embedding() ── 每符号生成向量
    │   └── LanceDB upload_points() ── 每 100 条批量上传
    │
    └── setup_watcher() ── 启动文件变化监听
```

**关键源码引用**：

- **`codegraph/parser.rs`** — `CodeParser` 是索引构建的核心入口，负责 AST 解析和调用图构建
- **`services/embedding_service.rs`** — `EmbeddingService` 管理向量化流程，包括缓存（SQLite）和 LanceDB 批量写入
- **`storage/persistence.rs`** — `PersistenceManager` 负责图谱的持久化存储

### 2.2 核心数据结构

系统维护两个独立但互补的数据存储：

#### PetCodeGraph（内存图谱）

```rust
// codegraph/types.rs
pub struct PetCodeGraph {
    pub graph: DiGraph<FunctionInfo, CallRelation>,  // petgraph 有向图
    pub function_to_node: HashMap<Uuid, NodeIndex>,  // 函数ID → 节点索引
    pub node_to_function: HashMap<NodeIndex, Uuid>,  // 节点索引 → 函数ID
    pub function_names: HashMap<String, Vec<Uuid>>,  // 函数名 → ID（支持重载）
    pub file_functions: HashMap<PathBuf, Vec<Uuid>>, // 文件 → 函数ID列表
    pub stats: CodeGraphStats,                       // 统计信息
}
```

**能力**：
- `get_callers()` / `get_callees()` — 基于 `petgraph::Direction::Incoming/Outgoing` 遍历有向边
- `get_call_chain()` — 递归 DFS 遍历调用链（`max_depth` 控制深度）
- `has_cycles()` / `topological_sort()` / `strongly_connected_components()` — 图分析算法

#### LanceDB 向量索引

```
表结构：
┌─────────────┬──────────────────────────────┬────────────┐
│ 字段         │ 类型                         │ 说明        │
├─────────────┼──────────────────────────────┼────────────┤
│ id          │ Utf8 (UUID)                  │ 主键        │
│ vector      │ FixedSizeList(2560, Float32) │ 嵌入向量    │
│ file_path   │ Utf8                         │ 文件路径    │
│ symbol_name │ Utf8                         │ 符号名      │
│ symbol_type │ Utf8                         │ 类型        │
│ language    │ Utf8                         │ 语言        │
│ line_start  │ Int64                        │ 起始行      │
│ line_end    │ Int64                        │ 结束行      │
│ code_block  │ Utf8                         │ 代码内容    │
└─────────────┴──────────────────────────────┴────────────┘
```

**模型**：Qwen3-Embedding-4B（2560 维），通过 SiliconFlow API 调用。

### 2.3 查询服务数据流

系统提供两个独立的查询路径：

#### 语义搜索（Vector Search Only）

```
POST /semantic_search
    │
    http/handlers/vectorize.rs: semantic_search()
    │
    └── EmbeddingService::search(query, limit)
        │
        ├── embedding_provider.get_embedding(query)
        │   → SiliconFlow API → 2560维向量
        │
        ├── lancedb_table.query()
        │   .nearest_to(query_vector)
        │   .limit(limit)
        │
        └── 返回 Vec<SearchResult>
            { file_path, symbol_name, code_block, score }
```

#### 代码图谱查询（Graph Query Only）

```
POST /query_call_graph
    │
    http/handlers/mod.rs: query_call_graph()
    │
    ├── PetCodeGraph 中按文件/函数名查找
    ├── get_callers() / get_callees() → 单层调用关系
    ├── expand_call_chain() → 递归多层展开
    └── 返回 { filepath, functions[] }
```

### 2.4 存储抽象层现状

`storage/traits.rs` 定义了三个核心 trait：

```rust
// 图持久化
pub trait GraphPersistence {
    fn save_graph(&self, project_id: &str, graph: &PetCodeGraph) -> io::Result<()>;
    fn load_graph(&self, project_id: &str) -> io::Result<Option<PetCodeGraph>>;
    // ... 哈希管理、项目注册等
}

// 增量更新
pub trait IncrementalUpdater {
    fn compute_file_md5(&self, file_path: &Path) -> Result<String, io::Error>;
    fn needs_update(&self, file_path: &Path) -> Result<bool, io::Error>;
    fn refresh_file(&mut self, file_path: &PathBuf,
        entity_graph: &mut EntityGraph, call_graph: &mut PetCodeGraph) -> Result<(), String>;
    // ...
}

// 图序列化
pub trait GraphSerializer {
    fn save_to_binary(code_graph: &PetCodeGraph, file_path: &Path) -> Result<(), String>;
    fn load_from_binary(file_path: &Path) -> Result<PetCodeGraph, String>;
    // ... JSON, GraphML, GEXF 格式支持
}
```

### 2.5 差距诊断

| 维度 | 当前状态 | 目标状态 | 差距 |
|------|---------|---------|------|
| **检索通道** | 仅 Dense（向量） | Dense + Sparse（BM25） | 缺少全文索引和融合层 |
| **图谱扩展** | 手动查询 Caller/Callee | 自动 BFS/DFS 上下文打包 | 缺少自动化扩展服务 |
| **结果排序** | 余弦距离排序 | Cross-Encoder 重排 | 缺少 Reranker 推理引擎 |
| **执行反馈** | 无 | 无 | — |
| **Pipeline 编排** | 各 handler 独立 | 统一 Pipeline Orchestrator | 缺少编排层 |
| **增量一致** | 仅更新图谱 | 三端（图+向量+文本）一致 | 缺少多索引事务协调 |

---

## 3. 阶段一：混合检索（Hybrid Search）

### 3.1 双通道检索原理

混合检索结合两种互补的检索范式：

```
                    ┌─────────────────────┐
                    │   User Query (文本)   │
                    └──────────┬──────────┘
                               │
                ┌──────────────┼──────────────┐
                │              │              │
    ┌───────────▼─────────┐  ┌▼──────────────▼──┐
    │  Dense Channel      │  │  Sparse Channel   │
    │  (向量语义检索)      │  │  (词项精准匹配)   │
    │                     │  │                   │
    │  EmbeddingService   │  │  Tantivy BM25     │
    │  LanceDB ANN        │  │  倒排索引          │
    │  Qwen3-Embedding    │  │  CodeTokenizer     │
    │  2560维            │  │                   │
    │                     │  │                   │
    │  优势: 语义相似性    │  │  优势: 精确定位    │
    │  擅长: "解析配置"   │  │  擅长: "parse_    │
    │        "错误处理"   │  │        config_file"│
    └───────────┬─────────┘  └────────┬──────────┘
                │                     │
                └──────────┬──────────┘
                           │
                ┌──────────▼──────────┐
                │  RRF 融合策略        │
                │  (Reciprocal Rank    │
                │   Fusion)           │
                └──────────┬──────────┘
                           │
                ┌──────────▼──────────┐
                │  Top-K 融合候选      │
                └─────────────────────┘
```

#### Dense Channel（已有）

基于 `services/embedding_service.rs` 已有的 `EmbeddingService`：
- 模型：Qwen3-Embedding-4B，2560 维
- 存储：LanceDB（嵌入式向量数据库，支持 IVF-PQ 索引）
- 缓存：SQLite 嵌入缓存（`EmbeddingCache`）
- 查询：LanceDB `nearest_to()` ANN 搜索（余弦距离）

#### Sparse Channel（新增）

需要引入基于 **Tantivy**（Rust 生态中最成熟的全文检索引擎）的 BM25 实现。

### 3.2 Tantivy Schema 设计

```rust
// storage/bm25_index.rs（新增）

use tantivy::schema::*;

pub fn build_bm25_schema() -> Schema {
    let mut schema_builder = Schema::builder();
    
    // snippet_id: 关联 SnippetIndex 中的唯一标识
    schema_builder.add_text_field("snippet_id", STRING | STORED);
    
    // file_path: 用于文件级过滤
    schema_builder.add_text_field("file_path", STRING | STORED);
    
    // content: 代码正文，使用自定义 CodeTokenizer
    // CodeTokenizer 处理 snake_case / CamelCase 分词
    schema_builder.add_text_field("content", TEXT | STORED);
    
    // symbol_name: 符号名（函数名/类名），精确匹配
    schema_builder.add_text_field("symbol_name", STRING | STORED);
    
    // language: 编程语言过滤
    schema_builder.add_text_field("language", STRING | STORED);
    
    schema_builder.build()
}
```

**CodeTokenizer 设计**：需要实现 Tantivy 的 `Tokenizer` trait，支持：
- 标准空格/标点分词
- CamelCase 拆分（`parseConfigFile` → `parse` `Config` `File`）
- snake_case 拆分（`parse_config_file` → `parse` `config` `file`）
- 保留常见分隔符（`::`, `.`, `->`）作为词项边界
- 数字和特殊字符处理

### 3.3 TextSearchProvider Trait 定义

在 `storage/traits.rs` 中扩展：

```rust
/// 文本搜索提供者接口
#[async_trait]
pub trait TextSearchProvider: Send + Sync {
    /// 批量索引代码片段
    async fn index_snippets(&self, snippets: &[CodeSnippet]) -> Result<()>;
    
    /// 按路径删除索引
    async fn remove_by_path(&self, file_path: &Path) -> Result<()>;
    
    /// 搜索
    async fn search(&self, query: &str, top_k: usize) -> Result<Vec<TextSearchResult>>;
    
    /// 提交索引变更
    async fn commit(&self) -> Result<()>;
}

pub struct TextSearchResult {
    pub snippet_id: String,
    pub file_path: String,
    pub score: f32,
}
```

### 3.4 Hybrid Search 服务

```rust
// services/hybrid_search_service.rs（新增）

pub struct HybridSearchService {
    embedding: Arc<EmbeddingService>,          // Dense 通道
    bm25: Arc<dyn TextSearchProvider>,        // Sparse 通道
    fusion_alpha: f32,                        // 融合参数（RRF k 值）
}

impl HybridSearchService {
    pub async fn search(
        &self, 
        query: &str, 
        top_k: usize,        // 最终返回数
        top_n_per_channel: usize,  // 每通道召回数（建议 top_k * 2）
    ) -> Result<Vec<FusedCandidate>> {
        
        // 并发执行两个通道
        let (dense_res, sparse_res) = tokio::join!(
            self.embedding.semantic_search(query, top_n_per_channel),
            self.bm25.search(query, top_n_per_channel)
        );
        
        // Reciprocal Rank Fusion (RRF)
        // score = Σ 1/(k + rank_i)  其中 k 取 60
        let fused = reciprocal_rank_fusion(
            dense_res?, 
            sparse_res?, 
            FUSION_K_DEFAULT,  // 60
            top_k
        );
        
        Ok(fused)
    }
}

/// RRF 融合算法
fn reciprocal_rank_fusion(
    dense: Vec<SearchResult>,
    sparse: Vec<TextSearchResult>,
    k: f32,
    top_k: usize,
) -> Vec<FusedCandidate> {
    let mut score_map: HashMap<String, (f32, FusedCandidate)> = HashMap::new();
    
    // Dense channel
    for (rank, result) in dense.iter().enumerate() {
        let score = 1.0 / (k + rank as f32 + 1.0);
        score_map.entry(result.snippet_id.clone())
            .or_insert((0.0, FusedCandidate::from(result)))
            .0 += score;
    }
    
    // Sparse channel
    for (rank, result) in sparse.iter().enumerate() {
        let score = 1.0 / (k + rank as f32 + 1.0);
        score_map.entry(result.snippet_id.clone())
            .or_insert((0.0, FusedCandidate::from(result)))
            .0 += score;
    }
    
    // 按融合分值排序取 top_k
    let mut candidates: Vec<(f32, FusedCandidate)> = score_map.into_values().collect();
    candidates.sort_by(|a, b| b.0.partial_cmp(&a.0).unwrap());
    candidates.truncate(top_k);
    candidates.into_iter().map(|(_, c)| c).collect()
}
```

### 3.5 增量更新联动

修改 `storage/incremental.rs` 中的 `IncrementalManager`，使其在 `refresh_file()` 时同步维护 BM25 索引：

```
refresh_file(file_path, entity_graph, call_graph)
    │
    ├── [现有] 删除旧实体节点
    ├── [现有] 重新解析文件
    ├── [现有] 更新 PetCodeGraph
    │
    ├── [新增] bm25.remove_by_path(file_path)  ── 删除旧索引
    ├── [新增] bm25.index_snippets(new_snippets) ── 索引新片段
    ├── [新增] bm25.commit()                     ── 提交变更
    │
    └── [现有] 持久化状态
```

### 3.6 降级策略

- **Tantivy 索引未就绪**：自动降级为纯 Vector Search，记录 Warning
- **Tantivy 搜索异常**：捕获错误，降级为纯 Vector Search
- **LanceDB 不可用**：降级为纯 BM25 搜索

---

## 4. 阶段二：图扩展（Graph Expansion）

### 4.1 核心价值

将 Hybrid Search 返回的"扁平代码片段候选集"，转化为结构化的"上下文包"（Context Package）。解决 LLM 的"只见树木不见森林"问题——在代码补全/修复任务中，孤立片段缺少调用链、导入关系和类型定义等关键上下文。

### 4.2 当前能力分析

#### 已具备的能力

`PetCodeGraph`（`codegraph/types.rs`）已提供基础图操作：

| 方法 | 说明 | 复杂度 |
|------|------|--------|
| `get_callers(id)` | 获取直接调用者 | O(1) |
| `get_callees(id)` | 获取直接被调用者 | O(1) |
| `get_call_chain(id, max_depth)` | DFS 递归调用链 | O(V+E) |
| `find_functions_by_name(name)` | 按名查找 | O(1) |
| `find_functions_by_file(path)` | 按文件查找 | O(1) |

`CodeAnalyzer`（`services/analyzer.rs`）提供更高层分析：

| 方法 | 说明 |
|------|------|
| `find_callers(func_name)` | 通过函数名查找调用者 |
| `find_callees(func_name)` | 通过函数名查找被调用者 |
| `find_call_chains(func_name, max_depth)` | 查找完整调用链 |

#### 缺失的能力

1. **自动化 BFS/DFS 扩展器** — 目前 `expand_call_chain` 是 handler 中的 inline 递归函数，未封装为可复用的服务
2. **多样化的边类型** — 目前仅支持 `CallRelation`（函数调用），缺少 `Import`, `Inherit`, `Implement`, `Contain` 等语义边
3. **Token 预算控制** — 无限制的图扩展会导致上下文爆炸，需要按 Token 预算裁剪
4. **与检索结果的联动** — 无自动化机制将 `SearchResult` 映射为图中的 `NodeIndex` 并执行扩展

### 4.3 图扩展策略

```
                    Seed Candidate (来自 Hybrid Search)
                              │
               ┌──────────────┴──────────────┐
               │                              │
      ┌────────▼────────┐          ┌─────────▼────────┐
      │ 纵向扩展         │          │ 横向扩展           │
      │ (Vertical)       │          │ (Horizontal)       │
      │                  │          │                    │
      │ Caller 链        │          │ 同文件邻接函数     │
      │ Callee 链        │          │ Import 依赖        │
      │ 深度: 1-2 层     │          │ 接口/类继承        │
      │                  │          │ 泛型实例化         │
      └────────┬─────────┘          └─────────┬──────────┘
               │                              │
               └──────────────┬──────────────┘
                              │
                    ┌─────────▼─────────┐
                    │  Context Package   │
                    │   ┌─────────────┐ │
                    │   │ Seed:       │ │
                    │   │  函数 A     │ │
                    │   ├─────────────┤ │
                    │   │ Related:    │ │
                    │   │  - Caller B │ │
                    │   │  - Callee C │ │
                    │   │  - Import D │ │
                    │   │  - Type  E  │ │
                    │   └─────────────┘ │
                    │  Token Budget: 4K │
                    └──────────────────┘
```

#### 纵向扩展（控制流）

追踪函数的调用链，最多扩展 depth=2 层：

```
foo()                          # Seed
├── caller: bar()              # depth=1
│   └── caller: main()         # depth=2
└── callee: helper()           # depth=1
    └── callee: util_fn()      # depth=2
```

#### 横向扩展（数据流/依赖）

基于文件级和项目级的关系：

```
foo() [seed]
├── 同文件: foo_init(), foo_cleanup()    # 邻接函数
├── 导入: use std::collections::HashMap  # Import 边
├── 类型: impl SomeTrait for Foo         # Implement 边
└── 继承: struct Foo : BaseFoo            # Inherit 边
```

### 4.4 详细设计

#### 扩展边类型

在 `codegraph/types.rs` 中丰富关系类型：

```rust
/// 关系类型枚举
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize)]
pub enum RelationKind {
    /// 函数调用（调用者→被调用者）
    Call,
    /// 模块/文件导入（文件→导入的目标符号）
    Import,
    /// 继承关系（子类→父类）
    Inherit,
    /// 接口实现（实现类→接口）
    Implement,
    /// 包含关系（文件→符号）
    Contain,
    /// 类型引用（引用→类型定义）
    TypeReference,
    /// 泛型实例化（具体化→泛型定义）
    GenericInstantiation,
}

/// 代码关系（增强版）
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct CodeRelation {
    pub kind: RelationKind,
    pub source: Uuid,      // 源节点
    pub target: Uuid,      // 目标节点
    pub source_name: String,
    pub target_name: String,
    pub source_file: PathBuf,
    pub target_file: PathBuf,
    pub line_number: usize,
    pub is_resolved: bool,
}
```

#### GraphExpansionService

```rust
// services/graph_expansion_service.rs（新增）

pub struct ExpansionConfig {
    /// 纵向扩展最大深度
    pub max_depth: usize,           // 默认: 2
    /// 是否启用横向扩展
    pub enable_lateral: bool,       // 默认: true
    /// Token 预算上限
    pub token_budget: usize,        // 默认: 4096
    /// 扩展方向
    pub direction: ExpansionDirection,
    /// 包含的关系类型
    pub relation_types: HashSet<RelationKind>,
}

pub enum ExpansionDirection {
    /// 双向扩展（Caller + Callee）
    Bidirectional,
    /// 仅向上（Callers）
    Upward,
    /// 仅向下（Callees）
    Downward,
}

pub struct GraphExpansionService {
    graph: Arc<RwLock<PetCodeGraph>>,
    snippet_index: Arc<RwLock<SnippetIndex>>,
    stable_id_map: HashMap<String, Uuid>,  // snippet_id → 稳定 UUID
}

impl GraphExpansionService {
    /// 将 SearchResult 映射为图节点，执行扩展
    pub fn expand(
        &self, 
        seeds: &[SearchResult],
        config: &ExpansionConfig,
    ) -> Result<Vec<ContextPackage>> {
        
        let mut packages = Vec::new();
        
        for seed in seeds {
            // 1. 将 snippet_id 映射为 PetCodeGraph 中的节点 UUID
            let node_id = self.resolve_stable_id(&seed.snippet_id)?;
            
            // 2. 执行 BFS 扩展
            let expanded_nodes = self.bfs_expand(node_id, config);
            
            // 3. 按 Token 预算裁剪
            let package = self.build_context_package(
                seed, expanded_nodes, config.token_budget
            );
            
            packages.push(package);
        }
        
        Ok(packages)
    }
    
    /// BFS 遍历图，收集相关节点
    fn bfs_expand(
        &self, 
        root: Uuid, 
        config: &ExpansionConfig,
    ) -> Vec<ExpandedNode> {
        let graph = self.graph.read().unwrap();
        let mut visited = HashSet::new();
        let mut queue = VecDeque::new();
        let mut results = Vec::new();
        
        // 从 root 出发，BFS 遍历
        queue.push_back((root, 0));  // (node_id, current_depth)
        visited.insert(root);
        
        while let Some((current, depth)) = queue.pop_front() {
            if depth <= config.max_depth {
                // 纵向：追踪 Caller/Callee
                if let Some(&node_idx) = graph.function_to_node.get(&current) {
                    // 遍历入边（Callers）
                    for edge in graph.graph.edges_directed(node_idx, Direction::Incoming) {
                        let caller_id = graph.node_to_function[&edge.source()];
                        if visited.insert(caller_id) {
                            let info = &graph.graph[edge.source()];
                            results.push(ExpandedNode {
                                uuid: caller_id,
                                info: info.clone(),
                                relation: edge.weight().clone(),
                                depth: depth + 1,
                            });
                            queue.push_back((caller_id, depth + 1));
                        }
                    }
                    // 遍历出边（Callees）
                    for edge in graph.graph.edges_directed(node_idx, Direction::Outgoing) {
                        let callee_id = graph.node_to_function[&edge.target()];
                        // 类似处理
                    }
                }
            }
        }
        
        results
    }
    
    /// 构建上下文包，受 Token 预算限制
    fn build_context_package(
        &self,
        seed: &SearchResult,
        nodes: Vec<ExpandedNode>,
        token_budget: usize,
    ) -> ContextPackage {
        // 1. 按优先级排序：深度小 > 深度大，Call 关系 > 其他
        // 2. 按 token 估算裁剪，确保总 token 不超过预算
        // 3. seed 节点总是包含
        // ...
    }
}
```

#### ContextPackage 结构

```rust
/// 上下文包：一个种子节点及其扩展的上下文
pub struct ContextPackage {
    /// 种子信息
    pub seed: SearchResult,
    /// 扩展的相关节点
    pub related: Vec<ExpandedNode>,
    /// 预估 Token 数
    pub estimated_tokens: usize,
    /// 总体优先级分数（用于后续 Rerank）
    pub priority_score: f32,
}

/// 扩展节点信息
pub struct ExpandedNode {
    pub uuid: Uuid,
    pub info: FunctionInfo,
    pub relation: CallRelation,
    pub depth: usize,
}
```

### 4.5 稳定性保证

由于 `PetCodeGraph` 的 `NodeIndex` 在增量更新后可能改变（图重建），不能将内存中的 `NodeIndex` 作为跨版本的稳定标识。解决方案：

1. **使用 SnippetIndex 作为稳定标识** — `storage/incremental.rs` 中的 `SnippetIndex` 使用文件路径+行号范围作为键，是稳定的
2. **内部映射表** — `GraphExpansionService` 维护 `HashMap<SnippetIndex, Uuid>`，从稳定键映射到当前内存图中的 `Uuid`

---

## 5. 阶段三：专用 Rerank API 精排（Cross-Encoder Reranking）

> 本章基于 SiliconFlow 实际提供的 `/v1/rerank` 专用端点进行设计，取代原基于 `/chat/completions` 的 Listwise Prompt 重排方案。

### 5.1 两阶段检索架构

Hybrid Search 的 RRF 融合虽然综合了向量与 BM25 的排序信号，但仍然是"浅层融合"——没有对 Query 与每个候选文档进行深度交叉编码。专用 Rerank API 通过 Cross-Encoder 模型，对每对 (Query, Document) 进行双向注意力交互，输出精确的相关性分数。

```
Stage 1 (高召回)                            Stage 2 (高精度)
┌──────────────────┐                     ┌──────────────────────────────┐
│  Hybrid Search   │                     │  Cross-Encoder Reranker     │
│                  │  Top-K candidates   │  (SiliconFlow /v1/rerank)   │
│  ┌────────────┐  │ ──────────────────→ │                              │
│  │ Dense      │  │                     │  ┌────────────────────────┐  │
│  │ (Vector)   │  │                     │  │ BAAI/bge-reranker-v2-  │  │
│  │            │  │                     │  │ m3 (Cross-Encoder)     │  │
│  └────────────┘  │                     │  │                        │  │
│  ┌────────────┐  │                     │  │ Query + Documents      │  │
│  │ Sparse     │  │                     │  │ → relevance_score[]    │  │
│  │ (BM25)     │  │                     │  └────────────────────────┘  │
│  └────────────┘  │                     │                              │
│                  │                     │  Top-N Ranked Candidates    │
└──────────────────┘                     └──────────────────────────────┘
```

关键变化 vs. 原 LLM Chat 方案：
- **精排器类型**：从生成式 Listwise Ranking（LLM 输出 JSON 排序）改为判别式 Cross-Encoder Ranking（专用模型输出 `relevance_score`）
- **服务边界**：`RerankerService` 不再依赖 `PromptBuilder`、`ContextWindowGuard`、`JsonExtractor`
- **评分语义**：直接获取归一化后的相关性分数 `(0, 1]`，无需后处理校准

### 5.2 设计决策

#### 决策 1：使用专用 Rerank API 替代 LLM Listwise Prompting

| 维度 | 原 LLM Chat 方案 | 新 SiliconFlow Rerank API |
|------|-----------------|--------------------------|
| 端点 | `/chat/completions` | `/v1/rerank` |
| 模型 | Qwen3-8B-Instruct (~8B) | BAAI/bge-reranker-v2-m3 (~568M) |
| 输出格式 | 自由文本 JSON，需解析与修复 | 结构化 `relevance_score` 列表 |
| Token 管理 | 客户端需实现 Context Window Guard | API 层自动截断处理 |
| 典型延迟 (Top-50) | 1.5s ~ 4s | 100ms ~ 500ms |
| 成本 | LLM 生成 Token 费用高 | Reranker 判别 Token 费用低 |

**结论**：对于纯"查询-文档相关性排序"任务，专用 Cross-Encoder 在效率、成本和输出稳定性上均优于通用 LLM。LLM 的指令遵循能力在此属于过度能力（Over-capability），引入不必要的非确定性。

#### 决策 2：保留两阶段级联结构

尽管 Rerank API 延迟较低，其计算复杂度仍为 O(N·L)（Cross-Encoder 需对每对 (query, doc) 进行前向计算），无法承受全库（百万级文档）遍历。因此仍将其置于召回阶段之后，仅对 Top-K 候选集进行重排。

#### 决策 3：采用托管服务，暂不自托管

`bge-reranker-v2-m3` 虽轻量（568M），但自托管仍需 GPU 资源与模型运维。SiliconFlow 托管版本提供标准 HTTP API 与自动扩缩容，符合当前阶段"免运维、按量计费"的目标。

### 5.3 模型选型：BAAI/bge-reranker-v2-m3

| 属性 | 值 |
|------|-----|
| **架构** | Cross-Encoder（双向注意力交互） |
| **规模** | 568M 参数 |
| **上下文** | 最大 8192 tokens（query + document 总和） |
| **语言** | 多语言（含中文）相关性排序优化 |
| **输出** | 单值标量 `relevance_score`，已归一化 |

> **注意**：若输入文档长度超过 8192 tokens，模型会自动截断。建议在传入 Rerank API 前，对超长文档执行轻量级前置截断（如保留前 6000 tokens），以保留语义完整性。

### 5.4 API 接口设计

SiliconFlow Rerank API 采用标准重排序接口设计。

**请求格式**：

```json
POST https://api.siliconflow.cn/v1/rerank
Authorization: Bearer <API_KEY>
Content-Type: application/json

{
  "model": "BAAI/bge-reranker-v2-m3",
  "query": "用户原始查询",
  "documents": [
    "文档1全文...",
    "文档2全文...",
    "..."
  ],
  "return_documents": true,
  "top_n": 10
}
```

**响应格式**：

```json
{
  "id": "rerank-20240601-xyz789",
  "results": [
    {
      "index": 3,
      "document": {
        "text": "..."
      },
      "relevance_score": 0.9412
    },
    {
      "index": 0,
      "document": {
        "text": "..."
      },
      "relevance_score": 0.8235
    }
  ],
  "meta": {
    "tokens": {
      "input_tokens": 4200,
      "output_tokens": 0,
      "image_tokens": 0
    },
    "billed_units": {
      "input_tokens": 4200,
      "output_tokens": 0,
      "image_tokens": 0,
      "search_units": 1,
      "classifications": 0
    }
  }
}
```

**关键字段语义**：
- `results[i].index`：指向输入 `documents` 数组的下标，**非**重排后的顺序下标
- `results[i].relevance_score`：归一化相关性分数 (0, 1]，越高越相关
- 返回数组默认按 `relevance_score` 降序排列
- `meta.tokens.input_tokens`：计费用输入 Token 数
- `meta.tokens.output_tokens`：输出端 Token 计数。Rerank 为判别式排序任务，不对输入进行续写，该值通常为 0
- `meta.tokens.image_tokens`：图像 Token 计数。当前纯文本 Rerank 场景下通常为 0，保留以兼容未来多模态模型
- `meta.billed_units`：计费单元统计对象，用于成本核算与用量监控
  - `billed_units.input_tokens`：计费输入 Token 数
  - `billed_units.output_tokens`：计费输出 Token 数
  - `billed_units.image_tokens`：计费图像 Token 数
  - `billed_units.search_units`：搜索单元计费计数
  - `billed_units.classifications`：分类单元计费计数

### 5.5 RerankerService 设计

与原 LLM Chat 方案相比，新设计的架构大幅简化：不再需要 Prompt 构造、JSON 解析修复、Context Window Guard 等模块。

```rust
// services/reranker_service.rs

use reqwest::{Client, StatusCode};
use serde::{Deserialize, Serialize};
use std::time::Duration;
use thiserror::Error;

// ── 配置 ─────────────────────────────────────────────

#[derive(Debug, Clone)]
pub struct RerankerConfig {
    pub api_key: String,
    pub base_url: String,               // e.g., "https://api.siliconflow.cn"
    pub model: String,                  // e.g., "BAAI/bge-reranker-v2-m3"
    pub timeout: Duration,
    pub max_retries: u32,               // 仅针对网络/限流错误
}

// ── API 请求/响应绑定 ─────────────────────────────────

#[derive(Serialize)]
struct RerankRequest<'a> {
    model: &'a str,
    query: &'a str,
    documents: &'a [String],
    return_documents: bool,
    top_n: usize,
}

#[derive(Deserialize)]
struct RerankResponse {
    #[allow(dead_code)]
    id: String,
    results: Vec<RerankResult>,
    #[serde(default)]
    meta: Option<RerankMeta>,
}

#[derive(Deserialize, Clone)]
struct RerankResult {
    index: usize,
    #[serde(default)]
    document: Option<RerankDocument>,
    relevance_score: f32,
}

#[derive(Deserialize, Clone)]
struct RerankDocument {
    text: String,
}

#[derive(Deserialize)]
struct RerankMeta {
    tokens: Option<TokenUsage>,
    #[serde(default)]
    billed_units: Option<BilledUnits>,
}

#[derive(Deserialize)]
struct TokenUsage {
    input_tokens: u32,
    output_tokens: u32,
    image_tokens: u32,
}

/// 计费单元统计，用于成本核算与用量监控
#[derive(Deserialize)]
struct BilledUnits {
    input_tokens: u32,
    output_tokens: u32,
    image_tokens: u32,
    search_units: u32,
    classifications: u32,
}

// ── 领域对象 ──────────────────────────────────────────

#[derive(Debug, Clone)]
pub struct RankedDocument {
    pub original_index: usize,
    pub text: String,
    pub score: f32,                     // relevance_score
}

#[derive(Error, Debug)]
pub enum RerankError {
    #[error("网络请求失败: {0}")]
    Network(#[from] reqwest::Error),
    #[error("API 返回错误: status={0}, body={1}")]
    ApiError(StatusCode, String),
    #[error("响应解析失败: {0}")]
    ParseError(String),
    #[error("全部重试后仍失败")]
    RetryExhausted,
}

// ── RerankerService ──────────────────────────────────

pub struct RerankerService {
    client: Client,
    config: RerankerConfig,
}

impl RerankerService {
    pub fn new(config: RerankerConfig) -> Result<Self, RerankError> {
        let client = Client::builder()
            .timeout(config.timeout)
            .pool_max_idle_per_host(10)
            .build()?;
        Ok(Self { client, config })
    }

    /// 对候选文档进行重排。
    ///
    /// # 参数
    /// - `query`: 用户查询
    /// - `candidates`: 召回阶段得到的文档全文列表
    /// - `top_n`: 最终保留的 Top N 数量
    ///
    /// # 返回
    /// 按 `relevance_score` 降序排列的文档列表。
    pub async fn rerank(
        &self,
        query: &str,
        candidates: Vec<String>,
        top_n: usize,
    ) -> Result<Vec<RankedDocument>, RerankError> {
        if candidates.is_empty() {
            return Ok(vec![]);
        }

        let request_body = RerankRequest {
            model: &self.config.model,
            query,
            documents: &candidates,
            return_documents: true,
            top_n,
        };

        let response = self.execute_with_backoff(request_body).await?;

        let ranked = response
            .results
            .into_iter()
            .map(|r| {
                let text = r.document.map(|d| d.text).unwrap_or_default();
                RankedDocument {
                    original_index: r.index,
                    text,
                    score: r.relevance_score,
                }
            })
            .collect();

        Ok(ranked)
    }

    /// 带指数退避重试的 API 调用
    async fn execute_with_backoff(
        &self,
        body: RerankRequest<'_>,
    ) -> Result<RerankResponse, RerankError> {
        let url = format!("{}/v1/rerank", self.config.base_url.trim_end_matches('/'));
        let mut last_err = None;

        for attempt in 0..self.config.max_retries {
            match self
                .client
                .post(&url)
                .bearer_auth(&self.config.api_key)
                .json(&body)
                .send()
                .await
            {
                Ok(resp) => {
                    let status = resp.status();
                    if status.is_success() {
                        return resp
                            .json::<RerankResponse>()
                            .await
                            .map_err(|e| RerankError::ParseError(e.to_string()));
                    } else if status == StatusCode::TOO_MANY_REQUESTS || status.is_server_error() {
                        let body_text = resp.text().await.unwrap_or_default();
                        last_err = Some(RerankError::ApiError(status, body_text));
                        // 指数退避: 2^attempt * 100ms
                        tokio::time::sleep(Duration::from_millis(100 * (1 << attempt))).await;
                    } else {
                        // 4xx 客户端错误不重试
                        let body_text = resp.text().await.unwrap_or_default();
                        return Err(RerankError::ApiError(status, body_text));
                    }
                }
                Err(e) if e.is_timeout() || e.is_connect() => {
                    last_err = Some(RerankError::Network(e));
                    tokio::time::sleep(Duration::from_millis(100 * (1 << attempt))).await;
                }
                Err(e) => return Err(RerankError::Network(e)),
            }
        }

        Err(last_err.unwrap_or(RerankError::RetryExhausted))
    }
}
```

**架构对比（原设计 vs 新设计）**：

```
原设计 (LLM Chat)                   新设计 (专用 Rerank API)
────────────────────────────────────────────────────────────────
Query + Docs                        Query + Docs
    │                                   │
    ▼                                   ▼
┌──────────────┐                  ┌──────────────┐
│PromptBuilder │                  │  Serialize   │
│(复杂模板)     │                  │  (serde)     │
└──────────────┘                  └──────────────┘
    │                                   │
    ▼                                   ▼
┌──────────────┐                  ┌──────────────┐
│ContextWindow │                  │   HTTP POST  │
│Guard         │                  │  /v1/rerank  │
└──────────────┘                  └──────────────┘
    │                                   │
    ▼                                   ▼
┌──────────────┐                  ┌──────────────┐
│ /chat/comp.  │                  │  Parse JSON  │
│ Qwen3-8B     │                  │  (强类型 Orz) │
└──────────────┘                  └──────────────┘
    │                                   │
    ▼                                   ▼
┌──────────────┐                  ┌──────────────┐
│JsonExtractor │                  │  relevance   │
│+ Repair      │                  │  _score      │
└──────────────┘                  └──────────────┘
```

### 5.6 Pipeline 位置策略

Rerank API 在 Pipeline 中的位置有两种策略：

**策略 A：Rerank → Expand（推荐）**

```
Hybrid Search (Top-100) → Rerank API (Top-20) → Graph Expansion → LLM
```

优点：先过滤掉低质量候选，减少 Graph Expansion 的无意义 API 调用。

**策略 B：Expand → Rerank**

```
Hybrid Search (Top-50) → Graph Expansion → Rerank API (Top-10) → LLM
```

优点：Reranker 能看到扩展后的完整上下文包，相关性判断更准确。

**推荐采用策略 A**，主要原因：
- 计算效率更高（减少 Graph Expansion 的无意义调用）
- Top-20 的候选已经足够覆盖大多数场景的多样性
- 由于 Rerank API 成本远低于 LLM，可考虑将 K 值从 20-30 适度提升至 50-100

**降级策略**：

```rust
// Pipeline 降级逻辑
let final_docs = match reranker.rerank(query, candidates, top_n).await {
    Ok(ranked) => ranked,
    Err(e) => {
        tracing::warn!("Rerank 服务不可用，降级为初检索分数排序: {}", e);
        candidates.into_iter()
            .take(top_n)
            .collect()
    }
};
```

### 5.7 性能与资源考量

#### 延迟估算（SiliconFlow API，bge-reranker-v2-m3，国内节点）

| 候选集大小 | 预估延迟 (P50) | 预估延迟 (P95) |
|-----------|---------------|---------------|
| Top 20    | 50-120 ms     | 200 ms        |
| Top 50    | 100-300 ms    | 600 ms        |
| Top 100   | 200-600 ms    | 1200 ms       |

> 实际延迟取决于单条文档长度。建议对超长文档（>4000 tokens）进行前置截断，以降低 Reranker 的 pairwise 编码开销。

#### 资源对比

| 维度 | 原方案（LLM Chat API） | 新方案（专用 Rerank API） |
|------|----------------------|------------------------|
| 额外内存 | ~0MB（仅 HTTP client） | **~0MB**（仅 HTTP client） |
| 启动时间 | ~0s（无加载） | **~0s**（无加载） |
| 运维复杂度 | 低（无模型部署） | **低**（无模型部署） |
| 单次请求成本 | 高（生成 Token） | **低**（仅输入 Token） |

#### 混合评分（可选高级策略）

虽然 `relevance_score` 已足够可靠，若初检索分数（如 BM25/向量距离）质量较高，可保留实验性融合：

```rust
// 仅作为 A/B 实验开关，默认关闭
fn hybrid_score(initial: f32, rerank: f32, alpha: f32) -> f32 {
    alpha * initial + (1.0 - alpha) * rerank
}
```

默认生产配置建议 `alpha = 0.0`，即完全信任 Reranker 分数。

---

## 7. Pipeline 集成架构

### 7.1 统一 Pipeline 定义

```rust
// services/retrieval_pipeline.rs

/// 多阶段级联检索 Pipeline
pub struct MultiStagePipeline {
    /// 阶段一: 混合检索
    hybrid: Arc<HybridSearchService>,
    /// 阶段二: 图扩展（可选）
    expander: Option<Arc<GraphExpansionService>>,
    /// 阶段三: 重排器（可选）
    reranker: Option<Arc<dyn Reranker>>,
}

/// Pipeline 配置
pub struct PipelineConfig {
    // 阶段开关
    pub enable_hybrid: bool,       // 默认 true
    pub enable_graph: bool,        // 默认 false（第二阶段上线后设为 true）
    pub enable_rerank: bool,       // 默认 false（第三阶段上线后设为 true）
    
    // 各阶段参数
    pub top_k_hybrid: usize,       // Hybrid 召回数（默认 100）
    pub top_k_after_rerank: usize, // Reranker 输出数（默认 20）
    pub graph_depth: usize,        // 图扩展深度（默认 2）
    pub token_budget: usize,       // 上下文 Token 预算（默认 4096）
}

impl MultiStagePipeline {
    /// 完整的检索流程
    pub async fn retrieve(
        &self,
        query: &str,
        config: &PipelineConfig,
    ) -> Result<Vec<ContextPackage>> {
        
        // Stage 1: Hybrid Search
        let candidates = self.hybrid.search(
            query, 
            config.top_k_hybrid,
            config.top_k_hybrid * 2,  // 每通道召回数
        ).await?;
        
        if candidates.is_empty() {
            return Ok(Vec::new());
        }
        
        // Stage 3: Rerank（优先过滤低质量候选）
        let ranked = if let Some(reranker) = &self.reranker && config.enable_rerank {
            let candidate_contexts: Vec<CandidateContext> = candidates.into_iter()
                .map(|c| c.into())
                .collect();
            let reranked = reranker.rerank(query, &candidate_contexts).await?;
            reranked.into_iter()
                .take(config.top_k_after_rerank)
                .map(|r| r.candidate)
                .collect()
        } else {
            candidates
        };
        
        // Stage 2: Graph Expansion（对精排后的候选扩展）
        let expanded = if let Some(expander) = &self.expander && config.enable_graph {
            expander.expand(&ranked, &ExpansionConfig {
                max_depth: config.graph_depth,
                token_budget: config.token_budget,
                ..Default::default()
            })?
        } else {
            ranked.into_iter()
                .map(|c| ContextPackage {
                    seed: c,
                    related: Vec::new(),
                    estimated_tokens: 0,
                    priority_score: 0.0,
                })
                .collect()
        };
        
        Ok(expanded)
    }
}
```

### 7.2 完整数据流时序

```
Client                     HTTP Server                  Pipeline                    Storage
  │                            │                           │                          │
  │  POST /api/v2/retrieve     │                           │                          │
  │───────────────────────────→│                           │                          │
  │                            │                           │                          │
  │                            │  MultiStagePipeline::     │                          │
  │                            │    retrieve(query, cfg)   │                          │
  │                            │──────────────────────────→│                          │
  │                            │                           │                          │
  │                            │  ── Stage 1: Hybrid ──    │                          │
  │                            │                           │── EmbeddingService.search │
  │                            │                           │── Bm25Index.search        │
  │                            │                           │←──────── results ─────────│
  │                            │                           │                          │
  │                            │  ── Stage 3: Rerank ──    │                          │
  │                            │                           │── CrossEncoder.infer      │
  │                            │                           │←───── ranked top-20 ─────│
  │                            │                           │                          │
  │                            │  ── Stage 2: Expand ──    │                          │
  │                            │                           │── PetCodeGraph BFS        │
  │                            │                           │←── context packages ─────│
  │                            │                           │                          │
  │                            │←───── final contexts ────│                          │
  │                            │                           │                          │
  │  ←── RetrievalResponse ───│                           │                          │
  │                            │                           │                          │
```

### 7.3 HTTP API 设计

```rust
// http/handlers/retrieval.rs（新增）

/// 统一检索请求
#[derive(Deserialize)]
pub struct RetrievalRequest {
    /// 查询文本
    pub query: String,
    
    /// 任务类型（代码生成/修复/解释）
    pub task_type: Option<TaskType>,
    
    /// Pipeline 配置（可选，覆盖默认）
    pub pipeline: Option<PipelineConfigDto>,
}

/// 统一检索响应
#[derive(Serialize)]
pub struct RetrievalResponse {
    /// 上下文包列表
    pub contexts: Vec<ContextPackageDto>,
    
    /// 采用的 Pipeline 路径
    pub pipeline_path: Vec<String>,  // ["hybrid", "rerank", "graph"]
    
    /// 各阶段统计
    pub stats: RetrievalStats,
}

/// 路由注册
pub fn retrieval_routes() -> Router<Arc<StorageManager>> {
    Router::new()
        .route("/api/v2/retrieve", post(handle_retrieve))
}
```

**向后兼容性**：保留现有 V1 端点不变，新 Pipeline 仅暴露于 `/api/v2/retrieve`。

### 7.4 降级与容错策略

```
┌──────────────────────────────────────────────────────────┐
│                Pipeline 降级策略                          │
│                                                          │
│  全功能                         降级链路                   │
│  ┌────────────────┐            ┌────────────────┐        │
│  │ Hybrid+Expand  │            │ Vector Only    │        │
│  │ +Rerank        │            │ (最简回退)      │        │
│  └───────┬────────┘            └────────────────┘        │
│          │                              ▲                 │
│          │  BM25 不可用                   │ BM25 故障     │
│          ▼                              │                 │
│  ┌────────────────┐            ┌────────────────┐        │
│  │ Vector+Expand  │            │ Vector Only    │        │
│  │ +Rerank        │            │ (Hybrid 回退)  │        │
│  └───────┬────────┘            └────────────────┘        │
│          │                              ▲                 │
│          │  Reranker OOM                  │ Reranker 故障 │
│          ▼                              │                 │
│  ┌────────────────┐            ┌────────────────┐        │
│  │ Hybrid+Expand  │            │ Hybrid Only   │        │
│  │ (无重排)       │            │ (Rerank 回退)  │        │
│  └───────┬────────┘            └────────────────┘        │
│          │                              ▲                 │
│          │  Graph Expansion 超时          │ Graph 故障    │
│          ▼                              │                 │
│  ┌────────────────┐            ┌────────────────┐        │
│  │ Hybrid+Rerank  │            │ Hybrid Only    │        │
│  │ (无图扩展)     │            │ (Expand 回退)   │        │
│  └────────────────┘            └────────────────┘        │
│                                                          │
│  每条降级路径记录 warning 日志，返回 response.pipeline_path │
└──────────────────────────────────────────────────────────┘
```

### 7.5 配置管理

建议通过 `codexray/` 的 `config.toml` 管理 Pipeline 配置：

```toml
# config.toml（扩展）

[retrieval_pipeline]
# 阶段开关
enable_hybrid = true
enable_rerank = false   # 待 Reranker 模型部署后开启
enable_graph_expansion = false  # 待边类型完善后开启

# Stage 1: Hybrid Search
[retrieval_pipeline.hybrid]
bm25_index_path = ".codexray/index.bm25/"
fusion_k = 60
top_k_per_channel = 200

# Stage 3: SiliconFlow Rerank API
[retrieval_pipeline.reranker]
type = "siliconflow_rerank"

[retrieval_pipeline.reranker.siliconflow]
api_key = "${SILICONFLOW_API_KEY}"         # 环境变量注入
base_url = "https://api.siliconflow.cn/v1"
model = "BAAI/bge-reranker-v2-m3"
timeout_ms = 10000
max_retries = 3
max_candidates = 50                         # 进入精排阶段的最大候选数
top_n = 10                                  # 精排后保留的文档数
cache_ttl_secs = 7200

# 注意：以下原 LLM Chat 配置项已废弃
# - api_base                          → 替换为 base_url
# - temperature / max_tokens          → 不适用于 Rerank API
# - prompt_template                   → 不需要 Prompt
# - max_context_window                → API 自动管理

# Stage 2: Graph Expansion
[retrieval_pipeline.graph_expansion]
max_depth = 2
token_budget = 4096
enable_lateral = true
```

---

## 8. 总结与路线图

### 8.1 实现优先级

| 优先级 | 阶段 | 依赖 | 工作量 | 收益 |
|--------|------|------|--------|------|
| **P0** | **Stage 1: Hybrid Search** | 无外部依赖，Tantivy 生态成熟 | 1-2 周 | 🟢 解决最痛的精确匹配问题 |
| **P1** | **Stage 3: Rerank API 集成** | 依赖 SiliconFlow API（已有）；需实现 RerankerService + 降级逻辑 | **1 周** | 🟢 显著提升 Top-K 精度；延迟 < 500ms（Top-50） |
| **P2** | **Stage 2: Graph Expansion** | 需 Parser 完善多类型边；需 Token 预算控制 | 2-3 周 | 🟡 解决上下文碎片化 |

### 8.2 关键里程碑

```
M1 (Week 1-2): TextSearchProvider trait + Tantivy 实现
    ├── TextSearchProvider trait 定义 (traits.rs)
    ├── TantivyBm25Index 实现 (bm25_index.rs)
    ├── CodeTokenizer (camelCase/snake_case 分词)
    ├── HybridSearchService (RRF 融合)
    └── 增量更新双写 (LanceDB + Tantivy)
    ✅ 验证: BM25 通道精确匹配 Top-1; RRF 融合 NDCG 提升 > 10%

M2 (Week 3-4): Rerank API 集成（与 M1 可并行）
    ├── RerankerService 实现（reqwest 绑定 + 错误处理 + 降级逻辑）
    ├── 集成测试：验证 Hybrid → Rerank 端到端数据流
    ├── 超长文档前置截断策略
    ├── 性能压测：Top-50 / Top-100 候选集 P99 延迟
    └── Pipeline 串联 Stage1 + Stage3
    ✅ 验证: NDCG@10 提升 > 15%; P99 延迟 < 1.5s

M3 (Week 5-7): Graph Expansion
    ├── RelationKind 枚举扩展 (Call/Import/Inherit/Implement)
    ├── Parser 增强: AST 阶段提取 Import/Inherit 边
    ├── GraphExpansionService (BFS + Token 预算)
    └── Pipeline 串联 Stage1+2+3
    ✅ 验证: 种子节点扩展后，上下文包含完整调用链
```

### 8.3 风险与缓解

| 风险 | 可能性 | 影响 | 缓解措施 |
|------|--------|------|---------|
| **索引不一致** | 中 | 高 | 在 `IncrementalManager` 引入"索引组提交"概念；增加定期全量校验 |
| **Rerank API 可用性** | 低 | 低 | Pipeline 内置降级（初检索分数排序）；缓存减少重复调用；SiliconFlow SLA 99.9% |
| **JSON 解析失败** | 低 | 低 | `parse_scores` 自动补缺失 ID 为中性分（5.0）；清理 markdown 包裹后仍失败则触发 fallback |
| **Graph Expansion Token 超限** | 高 | 中 | 必须实现 `token_budget` 硬限制；优先保留 seed + 最近邻居 |
| **Tantivy 索引膨胀** | 低 | 中 | 定期合并（`IndexWriter::commit`）；支持索引目录清理 |
| **多语言执行沙箱安全** | 中 | 高 | 严格 `timeout_secs` + `memory_limit_mb`；优先使用容器隔离 |

### 8.4 Feature Flag 与回滚计划

每个阶段通过独立的 Feature Flag 控制：

```rust
// 运行时 feature 控制
pub struct PipelineFeatures {
    /// Stage 1: BM25 索引构建（会影响启动时间）
    pub enable_bm25_indexing: bool,
    /// Stage 1: Hybrid Search（查询时使用 BM25）
    pub enable_hybrid_search: bool,
    /// Stage 2: Graph Expansion
    pub enable_graph_expansion: bool,
    /// Stage 3: Cross-Encoder Reranker
    pub enable_reranker: bool,
}
```

**回滚策略**：
1. **API 版本控制**：新 Pipeline 仅暴露于 `/api/v2/retrieve`，V1 端点完全不变。故障时客户端秒级回切
2. **数据回滚**：Tantivy 索引存放在独立目录，删除即可回退，不影响 LanceDB 和 PetCodeGraph
3. **代码回滚**：每个阶段以独立 PR 提交，启用以 Feature Flag 控制，可单独 revert

---

## 附录 A：术语表

| 术语 | 英文 | 定义 |
|------|------|------|
| **稠密检索** | Dense Retrieval | 基于神经网络 Embedding 的向量相似度检索 |
| **稀疏检索** | Sparse Retrieval | 基于词项统计（如 BM25）的倒排索引检索 |
| **混合检索** | Hybrid Search | 稠密 + 稀疏双通道检索，通过 RRF 等策略融合 |
| **RRF** | Reciprocal Rank Fusion | 基于排序位置的融合算法，无需分数归一化 |
| **交叉编码器** | Cross-Encoder | 对 Query-Document 对进行联合编码的 Transformer |
| **双编码器** | Bi-Encoder | Query 和 Document 独立编码，余弦相似度比较 |
| **上下文包** | Context Package | 包含种子代码块及其扩展上下文的结构化数据 |
| **图扩展** | Graph Expansion | 在代码知识图谱上通过 BFS/DFS 收集相关节点 |
| **Token 预算** | Token Budget | 为控制 LLM 输入长度的 Token 数量上限 |

## 附录 B：源码引用索引

| 文件 | 关键类型/函数 | 文档章节 |
|------|-------------|---------|
| `codexray/src/codegraph/types.rs` | `PetCodeGraph`, `FunctionInfo`, `CallRelation`, `FileIndex`, `SnippetIndex` | §2.2, §4 |
| `codexray/src/codegraph/parser.rs` | `CodeParser::build_petgraph_code_graph()`, `_analyze_file_calls()` | §2.1 |
| `codexray/src/services/embedding_service.rs` | `EmbeddingService::search()`, `vectorize_directory()` | §2.3, §3 |
| `codexray/src/services/analyzer.rs` | `CodeAnalyzer::find_callers()`, `find_callees()`, `find_call_chains()` | §2.3, §4 |
| `codexray/src/storage/traits.rs` | `GraphPersistence`, `IncrementalUpdater`, `GraphSerializer` | §2.4 |
| `codexray/src/storage/incremental.rs` | `IncrementalManager::refresh_file()`, `needs_update()` | §2.4, §3.5 |
| `codexray/src/storage/persistence.rs` | `PersistenceManager` | §2.1 |
| `codexray/src/http/handlers/mod.rs` | `query_call_graph()`, `query_code_snippet()`, `expand_call_chain()` | §2.3 |
| `codexray/src/http/handlers/vectorize.rs` | `semantic_search()` | §2.3 |
| `codexray/src/http/models/query.rs` | `QueryCallGraphRequest/Response` | §2.3 |
| `codexray/src/http/models/embedding.rs` | `SemanticSearchRequest/Response` | §2.3 |
| `codexray/src/storage/mod.rs` | `StorageManager` | §2.1 |

---

> **文档版本历史**
> - v1.0 (2025-01): 初始版本，完成三阶段方案设计与源码分析
