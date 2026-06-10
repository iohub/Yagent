# 知识索引与检索封装技术方案

> **文档版本**: v1.0  
> **创建日期**: 2025-01-15  
> **状态**: 草案  
> **适用范围**: CodeActor 项目（Go Agent + Rust Codebase 双服务架构）

---

## 目录

1. [概述与设计原则](#1-概述与设计原则)
2. [知识索引完整流程](#2-知识索引完整流程)
3. [检索能力分类与接口设计总览](#3-检索能力分类与接口设计总览)
4. [Rust 层 HTTP API 接口定义](#4-rust-层-http-api-接口定义)
5. [Go 层封装接口定义](#5-go-层封装接口定义)
6. [数据流与调用关系图](#6-数据流与调用关系图)
7. [与现有文档的关系定位](#7-与现有文档的关系定位)
8. [验证策略](#8-验证策略)
9. [附录](#9-附录)

---

## 1. 概述与设计原则

### 1.1 背景

CodeActor 项目采用 **Hub-and-Spoke（中枢-辐条）** 多智能体架构，由 Go 实现的 Agent 系统（`codeactor-agent`）与 Rust 实现的代码分析服务（`codeactor-codexray`）协同工作。知识索引与检索是整个系统的核心基础设施，为 Agent 提供代码理解、语义搜索和依赖分析能力。

### 1.2 核心组件

| 组件 | 语言 | 职责 |
|------|------|------|
| **CodeXRay Server** | Rust | 源码解析、图谱构建、向量嵌入、HTTP API 服务 |
| **RepoOperationsTool** | Go | Agent 侧封装，通过 HTTP 调用 CodeXRay 服务 |
| **StorageManager** | Rust | 图谱缓存、持久化、文件监听、嵌入任务调度 |
| **CodeGraph** | Rust | 函数调用有向图（Petgraph DiGraph） |
| **EmbeddingService** | Rust | 向量索引构建与语义检索 |

### 1.3 设计原则

#### 1.3.1 单进程单仓库

每个 CodeXRay 进程在启动时通过 `--repo-path` 参数绑定一个代码仓库，生命周期内不可切换。

**设计动机**：
- 简化 API 设计，端点无需携带仓库路径参数
- 避免并发冲突，降低实现复杂度
- 多仓库场景通过启动多个进程实例解决

```rust
// StorageManager 中的仓库绑定逻辑
current_repo: Arc<RwLock<Option<String>>>

fn try_bind_repo(&self, repo_path: &str) -> Result<(), String> {
    let mut current = self.current_repo.write().unwrap();
    if current.is_some() {
        return Err(format!("Already bound to: {}", current.unwrap()));
    }
    *current = Some(repo_path.to_string());
    Ok(())
}
```

#### 1.3.2 启动自初始化

`CodeXRayServer::start()` 在绑定端口前执行完整初始化：

```
启动流程:
  1. 验证 --repo-path 路径存在
  2. try_bind_repo() 绑定仓库
  3. 加载已缓存图谱 或 执行全量分析
  4. 触发后台嵌入索引构建
  5. 启动文件监听器 (inotify/FSEvents, 20s 防抖)
  6. 启动 HTTP 监听
```

#### 1.3.3 分层解耦

```
┌─────────────────────────────────────────────────┐
│              HTTP Handlers (Axum)                │
│  请求解析 → 参数验证 → 调用 Service → 响应封装   │
└──────────────────────┬──────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────┐
│              Services (业务逻辑)                  │
│  CodeAnalyzer / SnippetService / EmbeddingService│
└──────────────────────┬──────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────┐
│           StorageManager (状态中心)               │
│  图谱缓存 / 持久化 / 监听 / 配置 / 仓库绑定       │
└──────────────────────┬──────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────┐
│              CodeGraph (核心数据结构)             │
│  CodeParser / PetCodeGraph / TreeSitterParser    │
└─────────────────────────────────────────────────┘
```

所有组件通过 `StorageManager` 解耦，HTTP handlers、服务、解析器之间不直接依赖。

#### 1.3.4 增量更新

- **图谱增量**: 通过 MD5 文件哈希比较，仅重新解析变更文件
- **向量索引增量**: 基于 `projects.json` 中的文件哈希，仅处理变更文件
- **嵌入缓存**: SQLite 缓存 `md5(model + code_block)`，避免重复调用 API

#### 1.3.5 统一响应格式

所有 HTTP API 响应统一包装：

```json
{
  "success": true,
  "data": { ... }
}
```

失败时：

```json
{
  "success": false,
  "error": "错误描述信息"
}
```

---

## 2. 知识索引完整流程

### 2.1 端到端数据流

```
源码文件 (Source Code)
       │
       ▼
┌─────────────────────┐
│  Tree-sitter 解析    │  ← 多语言 Parser (Rust/Python/JS/TS/Go/C++/Java)
│  AST 生成            │    输出: AstSymbolInstance 列表
└─────────┬───────────┘
          │
          ▼
┌─────────────────────┐
│  符号提取            │  ← 提取 Function/Struct/Class 等声明
│  扁平化信息          │    输出: FunctionInfo (id, name, signature, range...)
└─────────┬───────────┘
          │
          ▼
┌─────────────────────┐
│  图谱构建            │  ← Petgraph DiGraph
│  调用关系分析        │    节点: FunctionInfo
│  (Call Graph)        │    边: CallRelation (caller_id, callee_id, is_resolved)
└─────────┬───────────┘
          │
     ┌────┴────┐
     ▼         ▼
┌─────────┐ ┌──────────────┐
│ 持久化   │ │ Embedding    │
│ JSON/Bin│ │ 向量索引      │
│ code    │ │ (LanceDB)    │
└─────────┘ └──────────────┘
```

### 2.2 第一阶段：源码 → AST

#### 2.2.1 语言检测

按文件扩展名映射到对应语言的 Tree-sitter Parser：

| 扩展名 | 语言 ID | Parser 模块 | Tree-sitter 库 |
|--------|---------|-------------|----------------|
| `.rs` | `rust` | `parsers/rust_parser.rs` | `tree-sitter-rust` |
| `.py` | `python` | `parsers/python_parser.rs` | `tree-sitter-python` |
| `.js` | `javascript` | `parsers/javascript_parser.rs` | `tree-sitter-javascript` |
| `.ts/.tsx` | `typescript` | `parsers/typescript_parser.rs` | `tree-sitter-typescript` |
| `.go` | `go` | `parsers/go_parser.rs` | `tree-sitter-go` |
| `.cpp/.cc/.h` | `cpp` | `parsers/cpp_parser.rs` | `tree-sitter-cpp` |
| `.java` | `java` | `parsers/java_parser.rs` | `tree-sitter-java` |

#### 2.2.2 AST 符号实例

每个 `AstSymbolInstance` 提供：

```rust
trait AstSymbolInstance {
    fn symbol_type(&self) -> SymbolType;        // FunctionDeclaration, StructDeclaration...
    fn name(&self) -> String;                    // 符号名称
    fn full_range(&self) -> Range;               // 完整代码范围 (start_line, end_line)
    fn declaration_range(&self) -> Range;        // 声明范围
    fn childs_guid(&self) -> Vec<String>;        // 子符号 ID 列表
    fn symbol_info_struct(&self) -> SymbolInfo;  // 扁平化信息结构体
    fn get_content_from_file(&self) -> String;   // 从源文件提取代码内容
}
```

### 2.3 第二阶段：AST → 图谱

#### 2.3.1 CodeGraph 核心结构

```rust
pub struct CodeGraph {
    /// 函数 ID → 函数信息
    pub functions: HashMap<Uuid, FunctionInfo>,
    
    /// 函数名 → 函数 ID 列表 (支持重载)
    pub function_names: HashMap<String, Vec<Uuid>>,
    
    /// 文件路径 → 函数 ID 列表
    pub file_functions: HashMap<PathBuf, Vec<Uuid>>,
    
    /// 调用关系列表
    pub call_relations: Vec<CallRelation>,
    
    /// 图关系 (扩展)
    pub graph_relations: Vec<GraphRelation>,
    
    /// 统计信息
    pub stats: CodeGraphStats,
}

pub struct FunctionInfo {
    pub id: Uuid,
    pub name: String,
    pub file_path: PathBuf,
    pub line_start: usize,
    pub line_end: usize,
    pub namespace: Option<String>,
    pub language: String,
    pub signature: String,
}

pub struct CallRelation {
    pub caller_id: Uuid,
    pub callee_id: Uuid,
    pub is_resolved: bool,
    pub line_start: usize,
}
```

#### 2.3.2 图谱构建算法

```rust
impl CodeParser {
    pub fn build_petgraph_code_graph(&self, dir: &Path) -> Result<CodeGraph, Error> {
        // 1. 扫描目录获取文件列表 (跳过 .git, target, node_modules 等)
        let files = self.scan_files(dir)?;
        
        // 2. 加载已有文件哈希 (增量更新)
        let existing_hashes = self.load_file_hashes(project_id)?;
        
        // 3. 对每个文件:
        //    - 计算 MD5
        //    - 与已存储哈希比较，相同则跳过
        //    - 不同则重新解析
        let mut new_functions = HashMap::new();
        for file in files {
            let current_md5 = self.calculate_md5(&file)?;
            if let Some(stored_md5) = existing_hashes.get(&file) {
                if current_md5 == *stored_md5 {
                    continue; // 跳过未变更文件
                }
            }
            let symbols = self.parse_file(&file)?;
            for symbol in symbols {
                if let FunctionInfo = symbol.extract_function_info() {
                    new_functions.insert(id, info);
                }
            }
            self.update_file_hashes(&file, current_md5);
        }
        
        // 4. 合并新解析的函数到已有图谱
        // 5. 重新分析调用关系 (跨文件解析函数调用)
        // 6. 保存更新后的哈希
        // 7. 返回 CodeGraph
    }
}
```

#### 2.3.3 图谱核心查询方法

| 方法 | 签名 | 说明 |
|------|------|------|
| `find_functions_by_name` | `&self, name: &str) -> Vec<&FunctionInfo>` | 按函数名搜索（支持重载） |
| `find_functions_by_file` | `&self, file_path: &PathBuf) -> Vec<&FunctionInfo>` | 按文件路径查找函数 |
| `get_callers` | `&self, function_id: &Uuid) -> Vec<&CallRelation>` | 获取调用该函数的所有函数 |
| `get_callees` | `&self, function_id: &Uuid) -> Vec<&CallRelation>` | 获取该函数调用的所有函数 |
| `get_call_chain` | `&self, id: &Uuid, max_depth: usize) -> Vec<CallChain>` | 递归获取调用链（BFS/DFS） |
| `has_cycles` | `&self) -> bool` | 检测循环调用 |
| `topological_sort` | `&self) -> Vec<Uuid>` | 拓扑排序 |
| `strongly_connected_components` | `&self) -> Vec<HashSet<Uuid>>` | 强连通分量（Kosaraju 算法） |

### 2.4 第三阶段：图谱 → 向量索引

#### 2.4.1 EmbeddingService 架构

```rust
pub struct EmbeddingService {
    /// 嵌入提供者（兼容 OpenAI API）
    provider: Box<dyn EmbeddingProvider>,
    
    /// LanceDB 连接
    vector_db: Connection,
    
    /// 表名: {repo_dir_name}_{md5(repo_full_path)}
    table_name: String,
    
    /// SQLite 嵌入缓存
    cache: EmbeddingCache,
    
    /// 项目注册表
    projects_json: ProjectsManager,
}
```

#### 2.4.2 嵌入提供者接口

```rust
#[async_trait]
pub trait EmbeddingProvider: Send + Sync {
    async fn get_embedding(&self, text: &str) -> Result<Vec<f32>, Box<dyn Error>>;
    fn model(&self) -> String;
}

// 实现: OpenAICompatibleEmbeddingProvider
pub struct OpenAICompatibleEmbeddingProvider {
    client: Client,
    api_base_url: String,
    api_token: String,
    model: String,
    dimensions: Option<usize>,
}
```

#### 2.4.3 嵌入缓存策略

```sql
-- SQLite embedding_cache 表结构
CREATE TABLE embedding_cache (
    hash TEXT PRIMARY KEY,           -- md5(model + code_block)
    vector BLOB,                     -- bincode 序列化的 Vec<f32>
    created_at INTEGER               -- Unix 时间戳
);
```

**缓存 Key 设计**: 包含模型名称，切换嵌入模型后不会误用旧缓存。

#### 2.4.4 向量索引构建流程

```
源码文件
    │
    ▼
Tree-sitter 解析 → 提取函数/结构体声明
    │
    ▼
对每个代码块:
    │
    ├── 1. 计算 hash = md5(model + code_block)
    │
    ├── 2. 查询 SQLite 缓存
    │       │
    │       ├── 命中 → 使用缓存向量
    │       │
    │       └── 未命中 → 继续下一步
    │
    ├── 3. 调用嵌入 API (OpenAI Compatible)
    │       │
    │       ▼
    │   POST /v1/embeddings
    │   { "model": "...", "input": "code_block" }
    │       │
    │       ▼
    │   返回 { "data": [{ "embedding": [f32; N] }] }
    │
    ├── 4. 写入 SQLite 缓存
    │
    └── 5. 批量写入 LanceDB (每 100 条)
            │
            ▼
        INSERT INTO {table_name}
        (id, vector, file_path, symbol_name, symbol_type, ...)
```

#### 2.4.5 LanceDB 表结构

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | UUID | 唯一标识 |
| `vector` | FixedSizeList<f32, D> | 嵌入向量 |
| `file_path` | String | 源文件路径 |
| `symbol_name` | String | 符号名称 |
| `symbol_type` | String | 符号类型 (Function/Struct/Class) |
| `language` | String | 编程语言 |
| `line_start` | Int64 | 起始行 |
| `line_end` | Int64 | 结束行 |
| `code_block` | String | 源代码片段 |

---

## 3. 检索能力分类与接口设计总览

### 3.1 检索能力矩阵

| 能力 | 数据源 | 检索方式 | 返回粒度 | 典型场景 |
|------|--------|----------|----------|----------|
| **调用图谱查询** | CodeGraph | 精确匹配 (函数名/文件) | 函数 + 调用关系 | "谁调用了 X？"、"X 调用了谁？" |
| **层级调用树** | CodeGraph | 精确匹配 (根函数) | 层级树 | 理解函数调用层次 |
| **代码骨架提取** | AST | 精确匹配 (文件路径) | 函数/类签名 | 快速了解文件结构 |
| **代码片段提取** | AST + 源码 | 精确匹配 (文件 + 函数) | 完整实现代码 | 需要看具体实现 |
| **仓库全景调查** | CodeGraph + AST | 自动分析 | Top15 核心函数 + 目录树 + 骨架 | 快速熟悉新仓库 |
| **语义搜索** | LanceDB 向量索引 | 向量相似度 | 相关代码块 | "找到处理用户登录的代码" |
| **索引状态查询** | projects.json | 内部状态 | 进度信息 | 等待索引完成 |

### 3.2 接口设计分层

```
┌─────────────────────────────────────────────────────────┐
│                   Go Agent 层                           │
│  ┌───────────────────────────────────────────────────┐  │
│  │  RepoOperationsTool                                │  │
│  │  - ExecuteSemanticSearch(ctx, params)             │  │
│  │  - ExecuteQueryCodeSkeleton(ctx, params)          │  │
│  │  - ExecuteQueryCodeSnippet(ctx, params)           │  │
│  └───────────────────────────────────────────────────┘  │
│                           │ HTTP POST                    │
└───────────────────────────┼─────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────┐
│                   Rust HTTP API 层                       │
│  ┌─────────────┐ ┌──────────────┐ ┌──────────────────┐  │
│  │ /semantic_  │ │/query_code_  │ │/query_code_      │  │
│  │ search      │ │skeleton     │ │snippet           │  │
│  └─────────────┘ └──────────────┘ └──────────────────┘  │
│  ┌─────────────┐ ┌──────────────┐ ┌──────────────────┐  │
│  │/query_call_ │ │/investigate_ │ │/query_indexing_  │  │
│  │graph        │ │repo          │ │status            │  │
│  └─────────────┘ └──────────────┘ └──────────────────┘  │
└───────────────────────────┼─────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────┐
│                   Rust Service 层                        │
│  ┌──────────────────┐ ┌─────────────────────────────┐   │
│  │ CodeAnalyzer     │ │ EmbeddingService            │   │
│  │ - find_callers   │ │ - semantic_search           │   │
│  │ - find_callees   │ │ - query_indexing_status     │   │
│  │ - find_call_chains│ │                             │   │
│  │ - analyze_dir    │ │ SnippetService              │   │
│  └──────────────────┘ │ - get_code_snippet          │   │
│                        │                             │   │
│                        │ Skeletonizer                │   │
│                        │ - format_skeletons          │   │
│                        └─────────────────────────────┘   │
└───────────────────────────┼─────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────┐
│                   Rust Storage 层                        │
│  ┌──────────────┐ ┌──────────────┐ ┌─────────────────┐  │
│  │ CodeGraph    │ │ LanceDB      │ │ SQLite          │  │
│  │ (内存缓存)    │ │ (向量索引)    │ │ (嵌入缓存)       │  │
│  └──────────────┘ └──────────────┘ └─────────────────┘  │
│  ┌───────────────────────────────────────────────────┐  │
│  │ StorageManager                                    │  │
│  │ - get_graph_clone()                               │  │
│  │ - get_persistence()                               │  │
│  └───────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────┘
```

---

## 4. Rust 层 HTTP API 接口定义

### 4.1 路由表

| 方法 | 路径 | Handler | 说明 | 认证 |
|------|------|---------|------|------|
| `GET` | `/health` | `health_check` | 健康检查 | 无 |
| `GET` | `/status` | `get_status` | 当前仓库状态 | 无 |
| `POST` | `/codexray_init` | `perform_analysis` | 初始化代码索引 | 无 |
| `POST` | `/query_call_graph` | `query_call_graph` | 查询函数调用图谱 | 无 |
| `POST` | `/query_hierarchical_graph` | `query_hierarchical_graph` | 查询层级调用树 | 无 |
| `POST` | `/query_code_skeleton` | `query_code_skeleton` | 批量提取代码骨架 | 无 |
| `POST` | `/query_code_snippet` | `query_code_snippet` | 提取代码片段 | 无 |
| `POST` | `/investigate_repo` | `investigate_repo` | 仓库全景调查 | 无 |
| `POST` | `/semantic_search` | `semantic_search` | 语义搜索代码块 | 无 |
| `POST` | `/query_indexing_status` | `query_indexing_status` | 查询嵌入索引进度 | 无 |
| `GET` | `/` | `draw_call_graph_home` | ECharts 可视化主页 | 无 |
| `GET` | `/draw_call_graph` | `draw_call_graph` | 带参数的可视化页面 | 无 |

### 4.2 统一响应格式

```rust
// 所有响应统一包装
#[derive(Debug, Serialize)]
pub struct ApiResponse<T: Serialize> {
    pub success: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub error: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub data: Option<T>,
}

// 成功响应示例
{
  "success": true,
  "data": { ... }
}

// 失败响应示例
{
  "success": false,
  "error": "错误描述信息"
}
```

### 4.3 端点详解

#### 4.3.1 `GET /health` — 健康检查

**请求**:
```http
GET /health
```

**响应**:
```json
{
  "success": true,
  "data": {
    "status": "healthy",
    "timestamp": 1705315200
  }
}
```

#### 4.3.2 `GET /status` — 仓库状态

**请求**:
```http
GET /status
```

**响应**:
```json
{
  "success": true,
  "data": {
    "repo_path": "/path/to/repo",
    "project_id": "abc123...",
    "total_functions": 1234,
    "total_files": 56,
    "embedding_enabled": true,
    "indexing_status": "completed"
  }
}
```

**字段说明**:

| 字段 | 类型 | 说明 |
|------|------|------|
| `repo_path` | string | 绑定的仓库路径 |
| `project_id` | string | MD5 哈希的项目 ID |
| `total_functions` | integer | 图谱中函数总数 |
| `total_files` | integer | 已解析文件数 |
| `embedding_enabled` | boolean | 是否启用嵌入索引 |
| `indexing_status` | string | 索引状态: `not_started` / `indexing` / `completed` / `failed` |

#### 4.3.3 `POST /codexray_init` — 初始化代码索引

**请求**:
```http
POST /codexray_init
Content-Type: application/json

{
  "project_dir": "/path/to/repo"
}
```

**响应**:
```json
{
  "success": true,
  "data": {
    "project_id": "abc123...",
    "total_functions": 1234,
    "total_files": 56,
    "analyzed_in_ms": 2345
  }
}
```

**说明**: 此端点在 Agent 启动任务时被 `task_executor` 调用，触发全量分析。如果图谱已存在则跳过。

#### 4.3.4 `POST /query_call_graph` — 查询函数调用图谱

**请求**:
```http
POST /query_call_graph
Content-Type: application/json

{
  "filepath": "src/main.rs",
  "function_name": "main",
  "max_depth": 3
}
```

**请求参数**:

| 参数 | 类型 | 必填 | 默认值 | 说明 |
|------|------|------|--------|------|
| `filepath` | string | 是 | - | 文件路径 |
| `function_name` | string | 否 | - | 函数名（可选，不填则返回文件中所有函数） |
| `max_depth` | integer | 否 | 1 | 递归扩展深度 |

**响应**:
```json
{
  "success": true,
  "data": {
    "filepath": "src/main.rs",
    "functions": [
      {
        "id": "550e8400-e29b-41d4-a716-446655440000",
        "name": "main",
        "line_start": 1,
        "line_end": 15,
        "callers": [
          {
            "function_name": "run_event_loop",
            "file_path": "src/server.rs"
          }
        ],
        "callees": [
          {
            "function_name": "init_config",
            "file_path": "src/config.rs"
          },
          {
            "function_name": "start_server",
            "file_path": "src/http/server.rs"
          }
        ]
      }
    ]
  }
}
```

#### 4.3.5 `POST /query_hierarchical_graph` — 查询层级调用树

**请求**:
```http
POST /query_hierarchical_graph
Content-Type: application/json

{
  "project_id": "abc123...",
  "root_function": "main",
  "max_depth": 3,
  "include_file_info": true
}
```

**请求参数**:

| 参数 | 类型 | 必填 | 默认值 | 说明 |
|------|------|------|--------|------|
| `project_id` | string | 否 | - | 项目 ID（通常可省略） |
| `root_function` | string | 是 | - | 根函数名 |
| `max_depth` | integer | 否 | 3 | 递归展开深度 |
| `include_file_info` | boolean | 否 | false | 是否包含文件信息 |

**响应**:
```json
{
  "success": true,
  "data": {
    "project_id": "abc123...",
    "root_function": "main",
    "max_depth": 3,
    "tree_structure": {
      "name": "main",
      "function_id": "550e8400-...",
      "file_path": "src/main.rs",
      "line_start": 1,
      "line_end": 15,
      "children": [
        {
          "name": "init_config",
          "function_id": "660e8400-...",
          "file_path": "src/config.rs",
          "children": [
            {
              "name": "load_config_file",
              "file_path": "src/config.rs"
            },
            {
              "name": "validate_config",
              "file_path": "src/config.rs"
            }
          ]
        },
        {
          "name": "start_server",
          "file_path": "src/http/server.rs",
          "children": [...]
        }
      ]
    },
    "total_functions": 12,
    "total_relations": 15
  }
}
```

#### 4.3.6 `POST /query_code_skeleton` — 批量提取代码骨架

**请求**:
```http
POST /query_code_skeleton
Content-Type: application/json

{
  "filepaths": [
    "src/main.rs",
    "src/lib.rs",
    "src/config.rs"
  ]
}
```

**请求参数**:

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `filepaths` | string[] | 是 | 文件路径列表 |

**响应**:
```json
{
  "success": true,
  "data": {
    "skeletons": [
      {
        "filepath": "src/main.rs",
        "language": "rust",
        "skeleton_text": "pub fn main() {\n    ...\n}\npub struct AppConfig {\n    ...\n}"
      },
      {
        "filepath": "src/lib.rs",
        "language": "rust",
        "skeleton_text": "pub fn helper() -> Result<()> {\n    ...\n}\npub struct DataProcessor {\n    ...\n}"
      }
    ]
  }
}
```

#### 4.3.7 `POST /query_code_snippet` — 提取代码片段

**请求**:
```http
POST /query_code_snippet
Content-Type: application/json

{
  "filepath": "src/main.rs",
  "function_name": "main",
  "include_context": true,
  "context_lines": 5
}
```

**请求参数**:

| 参数 | 类型 | 必填 | 默认值 | 说明 |
|------|------|------|--------|------|
| `filepath` | string | 是 | - | 文件路径 |
| `function_name` | string | 是 | - | 函数名 |
| `include_context` | boolean | 否 | false | 是否包含上下文行 |
| `context_lines` | integer | 否 | 5 | 上下文行数 |

**响应**:
```json
{
  "success": true,
  "data": {
    "filepath": "src/main.rs",
    "function_name": "main",
    "code_snippet": "pub fn main() {\n    let config = init_config();\n    start_server(config);\n}",
    "line_start": 1,
    "line_end": 15,
    "language": "rust",
    "context_before": "//! Main entry point\nuse crate::config;\nuse crate::http::server;",
    "context_after": "\n    println!(\"Server started!\");\n}"
  }
}
```

#### 4.3.8 `POST /investigate_repo` — 仓库全景调查

**请求**:
```http
POST /investigate_repo
Content-Type: application/json

{
  "project_dir": "/path/to/repo"
}
```

**请求参数**:

| 参数 | 类型 | 必填 | 说明 |
|------|------|------|------|
| `project_dir` | string | 是 | 仓库根目录路径 |

**响应**:
```json
{
  "success": true,
  "data": {
    "project_id": "abc123...",
    "total_functions": 1234,
    "core_functions": [
      {
        "name": "main",
        "file_path": "src/main.rs",
        "out_degree": 15,
        "callers": [],
        "callees": [
          { "function_name": "init_config", "file_path": "src/config.rs" },
          { "function_name": "start_server", "file_path": "src/http/server.rs" }
        ]
      },
      {
        "name": "handle_request",
        "file_path": "src/http/server.rs",
        "out_degree": 12,
        "callers": [
          { "function_name": "run_event_loop", "file_path": "src/server.rs" }
        ],
        "callees": [...]
      }
    ],
    "file_skeletons": [
      {
        "filepath": "src/main.rs",
        "language": "rust",
        "skeleton_text": "..."
      }
    ],
    "directory_tree": "codeactor-agent/\n├── src/\n│   ├── main.rs\n│   ├── lib.rs\n│   └── ...\n├── Cargo.toml\n└── ..."
  }
}
```

**返回内容说明**:

| 字段 | 说明 |
|------|------|
| `core_functions` | 按出度 (out_degree) 排序的 Top 15 核心函数，附带去重的 callers/callees |
| `file_skeletons` | 核心函数所在文件的代码骨架 |
| `directory_tree` | ASCII 风格目录树（自动忽略 `.git`、`node_modules`、`target` 等） |

**典型使用场景**: RepoAgent 预调查，在 Agent 开始工作前获取仓库全局视图。

#### 4.3.9 `POST /semantic_search` — 语义搜索

**请求**:
```http
POST /semantic_search
Content-Type: application/json

{
  "repo_path": "/path/to/repo",
  "text": "处理用户登录验证的逻辑",
  "limit": 5
}
```

**请求参数**:

| 参数 | 类型 | 必填 | 默认值 | 说明 |
|------|------|------|--------|------|
| `repo_path` | string | 是 | - | 仓库路径 |
| `text` | string | 是 | - | 搜索查询（自然语言） |
| `limit` | integer | 否 | 5 | 返回结果数量上限 |

**响应**:
```json
{
  "success": true,
  "data": {
    "results": [
      {
        "file_path": "src/auth.rs",
        "symbol_name": "verify_jwt_token",
        "symbol_type": "Function",
        "code_block": "pub fn verify_jwt_token(token: &str) -> Result<User> {\n    ...\n}",
        "score": 0.8766,
        "line_start": 45,
        "line_end": 62,
        "language": "rust"
      },
      {
        "file_path": "src/middleware/auth.rs",
        "symbol_name": "authenticate",
        "symbol_type": "Function",
        "code_block": "pub async fn authenticate(req: Request) -> Result<Response> {\n    ...\n}",
        "score": 0.7433,
        "line_start": 12,
        "line_end": 38,
        "language": "rust"
      }
    ]
  }
}
```

**响应字段说明**:

| 字段 | 类型 | 说明 |
|------|------|------|
| `file_path` | string | 源代码文件路径 |
| `symbol_name` | string | 符号名称 |
| `symbol_type` | string | 符号类型: `Function` / `Struct` / `Class` / `Method` |
| `code_block` | string | 源代码片段 |
| `score` | float | 相关性分数（越大越相关） |
| `line_start` | integer | 起始行 |
| `line_end` | integer | 结束行 |
| `language` | string | 编程语言 |

**底层实现**: 使用 LanceDB 向量数据库执行近邻搜索 (KNN)，基于 `Qwen3-Embedding-4B` 等嵌入模型。

#### 4.3.10 `POST /query_indexing_status` — 查询索引状态

**请求**:
```http
POST /query_indexing_status
Content-Type: application/json

{
  "repo_path": "/path/to/repo"
}
```

**响应**:
```json
{
  "success": true,
  "data": {
    "status": "completed",
    "message": "Indexing completed successfully. 1234 functions indexed."
  }
}
```

**状态值**:

| 状态 | 说明 |
|------|------|
| `not_found` | 未找到项目记录 |
| `not_started` | 索引尚未开始 |
| `indexing` | 索引进行中 |
| `completed` | 索引完成 |
| `failed` | 索引失败 |

---

## 5. Go 层封装接口定义

### 5.1 RepoOperationsTool 类型定义

```go
package tools

import "context"

// RepoOperationsTool 封装与 codexray Rust 服务的 HTTP 通信
type RepoOperationsTool struct {
    CodexrayURL string            // CodeXRay 服务地址，如 "http://127.0.0.1:12800"
    ProjectPath string            // 当前项目/仓库路径
}

// NewRepoOperationsTool 构造函数
func NewRepoOperationsTool(codexrayURL, projectPath string) *RepoOperationsTool {
    return &RepoOperationsTool{
        CodexrayURL: codexrayURL,
        ProjectPath: projectPath,
    }
}
```

### 5.2 核心方法

#### 5.2.1 ExecuteSemanticSearch — 语义搜索

**方法签名**:
```go
func (t *RepoOperationsTool) ExecuteSemanticSearch(ctx context.Context, params map[string]interface{}) (interface{}, error)
```

**参数**:
```go
params := map[string]interface{}{
    "query": "处理用户登录验证的逻辑",  // 搜索查询 (必填)
    "limit": 5,                          // 返回数量 (可选, 默认 5)
}
```

**返回值**:
- **成功**: `interface{}` — 解码后的 `SemanticSearchResponse` JSON
- **失败**: `nil, error` — 错误描述

**内部实现**:
```go
func (t *RepoOperationsTool) ExecuteSemanticSearch(ctx context.Context, params map[string]interface{}) (interface{}, error) {
    // 1. 参数解析
    query, ok := params["query"].(string)
    if !ok {
        return nil, fmt.Errorf("query parameter must be a string")
    }
    limit := 5
    if l, ok := params["limit"].(float64); ok {
        limit = int(l)
    }
    if limit <= 0 {
        limit = 5
    }

    // 2. 发送 HTTP 请求 (含 3 次重试)
    body, err := t.doCodexrayRequest("/semantic_search", map[string]interface{}{
        "repo_path": t.ProjectPath,
        "limit":     limit,
        "text":      query,
    })
    if err != nil {
        return nil, err
    }

    // 3. 解析响应
    var response interface{}
    if err := json.Unmarshal(body, &response); err != nil {
        return string(body), nil
    }
    return response, nil
}
```

#### 5.2.2 ExecuteQueryCodeSkeleton — 查询代码骨架

**方法签名**:
```go
func (t *RepoOperationsTool) ExecuteQueryCodeSkeleton(ctx context.Context, params map[string]interface{}) (interface{}, error)
```

**参数**:
```go
params := map[string]interface{}{
    "filepaths": []string{
        "src/main.rs",
        "src/lib.rs",
    },
}
```

**返回值**:
```go
// 成功时返回:
QueryCodeSkeletonResponse{
    Success: true,
    Data: struct{
        Skeletons []struct{
            Filepath     string `json:"filepath"`
            Language     string `json:"language"`
            SkeletonText string `json:"skeleton_text"`
        }
    }
}
```

#### 5.2.3 ExecuteQueryCodeSnippet — 查询代码片段

**方法签名**:
```go
func (t *RepoOperationsTool) ExecuteQueryCodeSnippet(ctx context.Context, params map[string]interface{}) (interface{}, error)
```

**参数**:
```go
params := map[string]interface{}{
    "filepath":      "src/main.rs",
    "function_name": "main",
}
```

**返回值**:
```go
// 成功时返回:
QueryCodeSnippetResponse{
    Success: true,
    Data: struct{
        Filepath     string `json:"filepath"`
        FunctionName string `json:"function_name"`
        CodeSnippet  string `json:"code_snippet"`
        LineStart    int    `json:"line_start"`
        LineEnd      int    `json:"line_end"`
        Language     string `json:"language"`
    }
}
```

### 5.3 底层 HTTP 请求封装

#### 5.3.1 doCodexrayRequest — 带重试的 HTTP 请求

```go
// doCodexrayRequest 发送 HTTP POST 到 codexray 服务，含重试逻辑
// 返回成功时的响应体字节
func (t *RepoOperationsTool) doCodexrayRequest(endpoint string, body interface{}) ([]byte, error) {
    bodyBytes, err := json.Marshal(body)
    if err != nil {
        return nil, fmt.Errorf("failed to marshal request: %w", err)
    }

    url := fmt.Sprintf("%s%s", t.CodexrayURL, endpoint)

    var lastErr error
    for attempt := 0; attempt < 3; attempt++ {
        if attempt > 0 {
            time.Sleep(time.Duration(attempt) * 500 * time.Millisecond) // 指数退避
        }

        req, err := http.NewRequest("POST", url, bytes.NewReader(bodyBytes))
        if err != nil {
            lastErr = fmt.Errorf("failed to create request: %w", err)
            continue
        }
        req.Header.Set("Content-Type", "application/json")

        client := &http.Client{}
        resp, err := client.Do(req)
        if err != nil {
            lastErr = fmt.Errorf("failed to send request: %w", err)
            continue
        }

        respBody, err := io.ReadAll(resp.Body)
        resp.Body.Close()
        if err != nil {
            lastErr = fmt.Errorf("failed to read response: %w", err)
            continue
        }

        if resp.StatusCode != http.StatusOK {
            lastErr = fmt.Errorf("server returned status %d: %s", resp.StatusCode, string(respBody))
            continue
        }

        return respBody, nil
    }

    return nil, fmt.Errorf("codexray request failed after 3 retries: %w", lastErr)
}
```

**重试策略**:
- 最大重试次数: 3
- 退避间隔: 500ms × 尝试次数 (500ms, 1000ms)
- 失败条件: 网络错误、非 200 状态码、响应读取失败

### 5.4 响应结构定义

```go
// QueryCodeSkeletonResponse codexray /query_code_skeleton 响应
type QueryCodeSkeletonResponse struct {
    Success bool `json:"success"`
    Data    struct {
        Skeletons []struct {
            Filepath     string `json:"filepath"`
            Language     string `json:"language"`
            SkeletonText string `json:"skeleton_text"`
        } `json:"skeletons"`
    } `json:"data"`
}

// QueryCodeSnippetResponse codexray /query_code_snippet 响应
type QueryCodeSnippetResponse struct {
    Success bool `json:"success"`
    Data    struct {
        Filepath     string `json:"filepath"`
        FunctionName string `json:"function_name"`
        CodeSnippet  string `json:"code_snippet"`
        LineStart    int    `json:"line_start"`
        LineEnd      int    `json:"line_end"`
        Language     string `json:"language"`
    } `json:"data"`
}
```

### 5.5 工具注册

```go
// tools.go 中注册 RepoOperations 工具
func registerRepoOperationsTool(agentType string, tool *RepoOperationsTool) []llms.Tool {
    tools := []llms.Tool{
        {
            Type:      "function",
            Function: &llms.FunctionDefinition{
                Name:        "semantic_search",
                Description: "Semantic search code blocks using vector embeddings",
                Parameters: map[string]interface{}{
                    "type": "object",
                    "properties": map[string]interface{}{
                        "query": map[string]interface{}{
                            "type":        "string",
                            "description": "Natural language search query",
                        },
                        "limit": map[string]interface{}{
                            "type":        "integer",
                            "description": "Maximum number of results (default: 5)",
                        },
                    },
                    "required": []string{"query"},
                },
            },
        },
        {
            Type:      "function",
            Function: &llms.FunctionDefinition{
                Name:        "query_code_skeleton",
                Description: "Extract function/class skeletons from specified files",
                Parameters: map[string]interface{}{
                    "type": "object",
                    "properties": map[string]interface{}{
                        "filepaths": map[string]interface{}{
                            "type":        "array",
                            "items":       map[string]interface{}{"type": "string"},
                            "description": "List of file paths to extract skeletons from",
                        },
                    },
                    "required": []string{"filepaths"},
                },
            },
        },
        {
            Type:      "function",
            Function: &llms.FunctionDefinition{
                Name:        "query_code_snippet",
                Description: "Extract code snippet for a specific function",
                Parameters: map[string]interface{}{
                    "type": "object",
                    "properties": map[string]interface{}{
                        "filepath": map[string]interface{}{
                            "type":        "string",
                            "description": "File path",
                        },
                        "function_name": map[string]interface{}{
                            "type":        "string",
                            "description": "Function name",
                        },
                    },
                    "required": []string{"filepath", "function_name"},
                },
            },
        },
    }

    // 通过 Adapter 包装为 langchaingo Tool 接口
    var llmTools []llms.Tool
    for _, t := range tools {
        adapter := NewAdapter(t.Function.Name, t.Function.Description, tool.executeFunction(t.Function.Name))
        llmTools = append(llmTools, adapter.ToLLMSTool())
    }
    return llmTools
}
```

### 5.6 错误处理

Go 层通过 `doCodexrayRequest` 统一处理错误：

| 错误类型 | 返回 | 示例 |
|----------|------|------|
| JSON 序列化失败 | `nil, error` | `"failed to marshal request: ..."` |
| HTTP 请求失败 | `nil, error` | `"failed to send request: ..."` |
| 非 200 状态码 | `nil, error` | `"server returned status 500: ..."` |
| 重试耗尽 | `nil, error` | `"codexray request failed after 3 retries: ..."` |
| 参数类型错误 | `nil, error` | `"query parameter must be a string"` |
| 响应 unsuccessful | `nil, error` | `"server returned unsuccessful response: ..."` |
| 服务不可用 | `nil, error` | `"codexray request failed after 3 retries: failed to send request: dial tcp 127.0.0.1:12800: connect: connection refused"` |

---

## 6. 数据流与调用关系图

### 6.1 完整任务执行数据流

```
┌─────────────────────────────────────────────────────────────────────────┐
│                          用户输入 (Task)                                 │
└──────────────────────────┬──────────────────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  HTTP Server (Go) / TUI (Go)                                            │
│  TaskManager.CreateTask()                                               │
│  - 生成 TaskID                                                          │
│  - 创建 ConversationMemory                                              │
│  - 设置状态为 "running"                                                  │
└──────────────────────────┬──────────────────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  ExecuteTask()                                                          │
│  ┌─────────────────────────────────────────────────────────────────┐    │
│  │ Step 1: POST /codexray_init (Go → Rust codexray service)        │    │
│  │   目的: 初始化代码索引                                            │    │
│  │   触发: 全量 AST 解析 + 图谱构建 + 嵌入索引构建                   │    │
│  └─────────────────────────────────────────────────────────────────┘    │
│  ┌─────────────────────────────────────────────────────────────────┐    │
│  │ Step 2: 注册 MessageDispatcher                                  │    │
│  │   - TUIConsumer (终端输出)                                       │    │
│  │   - WebSocketConsumer (广播到客户端)                             │    │
│  └─────────────────────────────────────────────────────────────────┘    │
│  ┌─────────────────────────────────────────────────────────────────┐    │
│  │ Step 3: CodeActor.ProcessCodingTaskWithCallback()               │    │
│  │   → ConductorAgent.Run()                                        │    │
│  └─────────────────────────────────────────────────────────────────┘    │
└──────────────────────────┬──────────────────────────────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────────────────┐
│  ConductorAgent 循环 (最多 maxSteps 步)                                    │
│  ┌─────────────────────────────────────────────────────────────────┐    │
│  │ 构造 messages: [SystemPrompt, ...Memory.Messages]                │    │
│  │ LLM.GenerateContent(messages, WithTools(llmTools))              │    │
│  │ 发布 ai_response 事件                                           │    │
│  └─────────────────────────────────────────────────────────────────┘    │
│  ┌─────────────────────────────────────────────────────────────────┐    │
│  │ 如果有 ToolCall:                                                  │    │
│  │   发布 tool_call_start                                           │    │
│  │   执行 Adapter.Call()                                            │    │
│  │     ├── delegate_repo → RepoAgent.Run()                         │    │
│  │     ├── delegate_coding → CodingAgent.Run()                     │    │
│  │     └── delegate_chat → ChatAgent.Run()                         │    │
│  │   发布 tool_call_result                                          │    │
│  │   将 ToolCallResponse 追加到 messages                           │    │
│  └─────────────────────────────────────────────────────────────────┘    │
└──────────────────────────┬──────────────────────────────────────────────┘
                           │
        ┌──────────────────┼──────────────────┐
        ▼                  ▼                  ▼
┌──────────────┐  ┌──────────────┐  ┌──────────────────┐
│ RepoAgent    │  │ CodingAgent  │  │ ChatAgent        │
│ (预调查 + 分析)│  │ (编码执行)    │  │ (通用对话)        │
└──────┬───────┘  └──────┬───────┘  └──────────────────┘
       │                 │
       │                 │  调用仓库检索工具:
       │                 │
       │                 ▼
       │          ┌──────────────────────┐
       │          │ RepoOperationsTool   │
       │          │ (Go 层封装)           │
       │          └──────────┬───────────┘
       │                     │ HTTP POST
       │                     ▼
       │          ┌──────────────────────┐
       │          │ CodeXRay Server      │
       │          │ (Rust, :12800)       │
       │          └──────────┬───────────┘
       │                     │
       │                     ▼
       │          ┌──────────────────────┐
       │          │ Services 层           │
       │          │ - CodeAnalyzer       │
       │          │ - SnippetService     │
       │          │ - EmbeddingService   │
       │          └──────────┬───────────┘
       │                     │
       │                     ▼
       │          ┌──────────────────────┐
       │          │ Storage 层            │
       │          │ - CodeGraph (内存)    │
       │          │ - LanceDB (向量)      │
       │          │ - SQLite (缓存)       │
       │          │ - 磁盘 (持久化)        │
       │          └──────────────────────┘
       │
       └──► Conductor 接收结果 → 更新 GlobalCtx.RepoSummary
```

### 6.2 RepoAgent 预调查流程

```
RepoAgent.Run(input)
  │
  ├── 1. doPreInvestigate()
  │     │
  │     ├── POST /investigate_repo (Go → Rust)
  │     │     Body: { "project_dir": "/path/to/repo" }
  │     │
  │     ├── 接收 PreInvestigateResponse:
  │     │     {
  │     │       "DirectoryTree": "...",
  │     │       "CoreFunctions": [...],
  │     │       "FileSkeletons": [...]
  │     │     }
  │     │
  │     └── 格式化为 Markdown 注入 systemPrompt
  │
  ├── 2. LLM.GenerateContent(systemPrompt + input, WithTools)
  │
  └── 3. 返回结构化分析摘要
        │
        ▼
ConductorAgent 接收摘要
  → 存储到 GlobalCtx.RepoSummary
  → 作为上下文传递给 CodingAgent
```

### 6.3 工具调用关系

```
┌─────────────────────────────────────────────────────────────────┐
│                         Agent 工具分配                           │
├─────────────┬───────────────────────────────────────────────────┤
│ Conductor   │ delegate_repo, delegate_coding, delegate_chat,    │
│             │ finish, read_file, search_by_regex,               │
│             │ list_dir, print_dir_tree                          │
├─────────────┼───────────────────────────────────────────────────┤
│ CodingAgent │ 全部 14 个工具 (含文件编辑、Shell 执行)              │
│             │ semantic_search, query_code_skeleton,             │
│             │ query_code_snippet, search_replace_in_file,       │
│             │ run_bash, thinking, ...                           │
├─────────────┼───────────────────────────────────────────────────┤
│ RepoAgent   │ 7 个只读/搜索工具                                   │
│             │ read_file, search_by_regex, list_dir,             │
│             │ print_dir_tree, semantic_search,                  │
│             │ query_code_skeleton, query_code_snippet            │
├─────────────┼───────────────────────────────────────────────────┤
│ ChatAgent   │ 无工具 (纯 LLM 对话)                               │
└─────────────┴───────────────────────────────────────────────────┘
```

### 6.4 文件监听与增量更新

```
┌─────────────────────────────────────────────────────────────────┐
│                    CodeXRay Server 后台任务                      │
├─────────────────────────────────────────────────────────────────┤
│                                                                  │
│  setup_watcher()                                                 │
│    │                                                             │
│    ▼                                                             │
│  notify.NewWatcher() → inotify (Linux) / FSEvents (macOS)       │
│    │                                                             │
│    ▼                                                             │
│  监听目录文件变更事件                                              │
│    │                                                             │
│    ▼                                                             │
│  20 秒防抖 (Debounce)                                            │
│    │                                                             │
│    ▼                                                             │
│  触发 perform_analysis() (spawn_blocking)                        │
│    │                                                             │
│    ├── 1. 扫描变更文件                                            │
│    ├── 2. MD5 比较, 仅重新解析变更文件                             │
│    ├── 3. 合并到 CodeGraph                                      │
│    └── 4. 触发 embedding 增量更新                                │
│         │                                                        │
│         ▼                                                        │
│     vectorize_directory(existing_hashes)                         │
│       │                                                          │
│       ├── 计算文件 MD5, 仅处理变更文件                             │
│       ├── 查询 SQLite 嵌入缓存                                    │
│       ├── 未命中 → 调用嵌入 API                                   │
│       └── 每 100 条批量写入 LanceDB                               │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
```

---

## 7. 与现有文档的关系定位

### 7.1 文档关系矩阵

| 文档 | 路径 | 本文档与之的关系 |
|------|------|-----------------|
| `codexray/docs/ARCHITECTURE.md` | `codexray/docs/ARCHITECTURE.md` | **补充细化**: 本文档将其中关于检索 API 的部分系统化，补充 Go 层封装细节 |
| `docs/ARCHITECTURE.md` | `docs/ARCHITECTURE.md` | **引用关系**: 本文档描述 Go 层 `RepoOperationsTool` 如何调用 Rust 服务，与其中第 5.5 节、第 6.3 节互补 |
| `docs/Agent_Design.md` | `docs/Agent_Design.md` | **实现落地**: 将其中"代码检索"、"code_search"设计转化为具体的接口定义 |

### 7.2 与 codexray/docs/ARCHITECTURE.md 的关系

| 章节 | 原文档 | 本文档补充 |
|------|--------|-----------|
| 路由表 | 第 4 节 HTTP API 设计 | 完整请求/响应格式、字段类型说明 |
| 检索能力 | 第 4.3 节 关键端点 | 分类矩阵、设计原则、Go 层封装 |
| AST 解析 | 第 5 节 AST 解析层 | 索引流程中的 AST→图谱转换细节 |
| 嵌入索引 | 第 6 节 嵌入索引生命周期 | 缓存策略、向量数据库表结构 |

### 7.3 与 docs/ARCHITECTURE.md 的关系

| 章节 | 原文档 | 本文档补充 |
|------|--------|-----------|
| 工具层 | 第 3.2.2 节 | 工具注册、参数定义、JSON Schema |
| Codebase API | 第 5.5 节 | 完整接口文档、错误处理 |
| 数据流 | 第 6.3 节 | 预调查流程的详细时序 |

### 7.4 文档职责划分

```
┌─────────────────────────────────────────────────────────────┐
│                    文档体系                                   │
├──────────────────┬──────────────────────────────────────────┤
│ codexray docs    │ 聚焦 Rust 服务内部实现细节                 │
│ ARCHITECTURE     │ - 模块架构                                │
│                  │ - 核心数据结构                             │
│                  │ - 持久化格式                               │
│                  │ - 配置系统                                 │
├──────────────────┼──────────────────────────────────────────┤
│ Agent docs       │ 聚焦 Go Agent 系统架构                     │
│ ARCHITECTURE     │ - Hub-and-Spoke 架构                      │
│                  │ - Agent 角色与工具分配                     │
│                  │ - 消息通讯机制                             │
│                  │ - 外部依赖服务                             │
├──────────────────┼──────────────────────────────────────────┤
│ Agent docs       │ Agent 设计构思与规划                       │
│ Agent_Design     │ - Hub-and-Spoke 模式                      │
│                  │ - 角色定义                                │
│                  │ - 工具系统设计                             │
│                  │ - 工作流设计                              │
├──────────────────┼──────────────────────────────────────────┤
│ ← 本文档 →       │ 聚焦知识索引与检索的完整接口契约             │
│ knowledge-index  │ - Rust HTTP API 接口定义                   │
│ and-retrieval    │ - Go 层封装定义                           │
│                  │ - 数据流与调用关系                         │
│                  │ - 知识索引全流程                           │
└──────────────────┴──────────────────────────────────────────┘
```

---

## 8. 验证策略

### 8.1 单元测试

#### 8.1.1 Rust 层测试

```rust
// codexray/tests/test_functional.rs 中新增

#[tokio::test]
async fn test_query_call_graph() {
    // 1. 启动测试服务器
    let storage = create_test_storage();
    let server = CodeXRayServer::new(storage, "/path/to/test/repo");
    
    // 2. 发送请求
    let resp = reqwest::Client::new()
        .post("/query_call_graph")
        .json(&serde_json::json!({
            "filepath": "src/main.rs",
            "function_name": "main"
        }))
        .send()
        .await
        .unwrap();
    
    // 3. 验证响应
    assert!(resp.status().is_success());
    let body: ApiResponse<QueryCallGraphResponse> = resp.json().await.unwrap();
    assert!(body.success);
    assert!(!body.data.unwrap().functions.is_empty());
}

#[tokio::test]
async fn test_semantic_search() {
    // 1. 确保索引已完成
    wait_for_indexing_completion().await;
    
    // 2. 发送语义搜索请求
    let resp = reqwest::Client::new()
        .post("/semantic_search")
        .json(&serde_json::json!({
            "text": "处理用户登录",
            "limit": 5
        }))
        .send()
        .await
        .unwrap();
    
    // 3. 验证返回结果
    assert!(resp.status().is_success());
    let body: ApiResponse<SemanticSearchResponse> = resp.json().await.unwrap();
    assert!(body.success);
    assert!(body.data.unwrap().results.len() <= 5);
}
```

#### 8.1.2 Go 层测试

```go
// internal/tools/repo_operations_test.go

func TestExecuteSemanticSearch(t *testing.T) {
    // 1. 启动 mock server
    mux := http.NewServeMux()
    mux.HandleFunc("/semantic_search", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusOK)
        json.NewEncoder(w).Encode(map[string]interface{}{
            "success": true,
            "data": map[string]interface{}{
                "results": []map[string]interface{}{
                    {
                        "file_path": "src/auth.rs",
                        "symbol_name": "verify_token",
                    },
                },
            },
        })
    })
    
    server := httptest.NewServer(mux)
    defer server.Close()
    
    // 2. 创建 tool
    tool := NewRepoOperationsTool(server.URL, "/path/to/repo")
    
    // 3. 执行搜索
    result, err := tool.ExecuteSemanticSearch(context.Background(), map[string]interface{}{
        "query": "验证 token",
        "limit": 5,
    })
    
    // 4. 断言
    assert.NoError(t, err)
    assert.NotNil(t, result)
}

func TestRetryLogic(t *testing.T) {
    // 测试 3 次重试逻辑
    callCount := 0
    mux := http.NewServeMux()
    mux.HandleFunc("/semantic_search", func(w http.ResponseWriter, r *http.Request) {
        callCount++
        if callCount < 3 {
            w.WriteHeader(http.StatusInternalServerError)
            return
        }
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]interface{}{
            "success": true,
            "data":    nil,
        })
    })
    
    server := httptest.NewServer(mux)
    defer server.Close()
    
    tool := NewRepoOperationsTool(server.URL, "/path/to/repo")
    _, err := tool.doCodexrayRequest("/semantic_search", nil)
    
    assert.NoError(t, err)
    assert.Equal(t, 3, callCount)
}
```

### 8.2 集成测试

#### 8.2.1 端到端测试

```bash
#!/bin/bash
# benchmark/run_knowledge_retrieval_tests.sh

set -e

echo "=== Knowledge Retrieval Integration Tests ==="

# 1. 启动 CodeXRay 服务
echo "[1/5] Starting codexray server..."
cargo run -- server --repo-path /tmp/test-repo --address 127.0.0.1:12800 &
SERVER_PID=$!
sleep 5

# 2. 等待索引完成
echo "[2/5] Waiting for indexing to complete..."
for i in {1..30}; do
    status=$(curl -s http://127.0.0.1:12800/query_indexing_status | jq -r '.data.status')
    if [ "$status" = "completed" ]; then
        echo "Indexing completed."
        break
    fi
    if [ $i -eq 30 ]; then
        echo "ERROR: Indexing timed out."
        exit 1
    fi
    sleep 2
done

# 3. 测试各端点
echo "[3/5] Testing API endpoints..."

# 3.1 Health check
curl -sf http://127.0.0.1:12800/health || { echo "Health check failed"; exit 1; }

# 3.2 Status
curl -sf http://127.0.0.1:12800/status || { echo "Status check failed"; exit 1; }

# 3.3 Query call graph
curl -sf -X POST http://127.0.0.1:12800/query_call_graph \
    -H "Content-Type: application/json" \
    -d '{"filepath": "src/main.rs", "function_name": "main"}' \
    | jq -e '.success == true' || { echo "Query call graph failed"; exit 1; }

# 3.4 Semantic search
curl -sf -X POST http://127.0.0.1:12800/semantic_search \
    -H "Content-Type: application/json" \
    -d '{"text": "处理用户登录", "limit": 5}' \
    | jq -e '.success == true and (.data.results | length) > 0' \
    || { echo "Semantic search failed"; exit 1; }

# 4. 测试 Go 层封装
echo "[4/5] Testing Go layer..."
go test -v ./internal/tools/... -run TestExecuteSemanticSearch

# 5. 清理
echo "[5/5] Cleaning up..."
kill $SERVER_PID 2>/dev/null || true
wait $SERVER_PID 2>/dev/null || true

echo "=== All tests passed ==="
```

### 8.3 性能基准

| 测试项 | 指标 | 目标值 | 说明 |
|--------|------|--------|------|
| 图谱查询 (1 层) | 响应时间 | < 10ms | 内存图谱查询 |
| 图谱查询 (5 层) | 响应时间 | < 100ms | 递归扩展 |
| 语义搜索 | 响应时间 | < 500ms | 含向量检索 |
| 代码骨架提取 (10 文件) | 响应时间 | < 200ms | AST 解析 + 格式化 |
| 代码片段提取 | 响应时间 | < 50ms | 直接从内存获取 |
| 仓库全景调查 | 响应时间 | < 2s | Top15 + 目录树 + 骨架 |
| 嵌入索引构建 (1000 函数) | 构建时间 | < 60s | 含 API 调用 |

### 8.4 边界条件测试

| 场景 | 预期行为 |
|------|---------|
| 空仓库 | 返回空结果，不报错 |
| 不存在的文件 | 返回 `success: false, error: "file not found"` |
| 不存在的函数 | 返回空 functions 列表 |
| 语义搜索无结果 | 返回空 results 列表 |
| CodeXRay 服务未启动 | Go 层重试 3 次后返回错误 |
| 超大响应 (>1MB) | 设置响应体大小限制 |
| 非法 JSON 输入 | 返回 400 Bad Request |

---

## 9. 附录

### 9.1 嵌入模型细节

#### 9.1.1 当前配置

```toml
# ~/.codeactor/config/config.toml

[codexray.embedding]
model = "Qwen/Qwen3-Embedding-4B"          # 嵌入模型
api_base_url = "https://api.siliconflow.cn/v1"  # API 端点
api_token = "sk-..."                        # API 密钥
dimensions = 2560                           # 向量维度
```

#### 9.1.2 支持的嵌入模型

| 模型 | 提供商 | 维度 | 建议场景 |
|------|--------|------|---------|
| `Qwen/Qwen3-Embedding-4B` | 硅基流动 | 2560 | 通用代码语义理解 |
| `text-embedding-3-small` | OpenAI | 1536 | 英文代码库 |
| `text-embedding-3-large` | OpenAI | 3072 | 高精度搜索 |
| `bge-m3` | BAAI | 1024 | 多语言代码库 |

#### 9.1.3 嵌入请求格式

```http
POST https://api.siliconflow.cn/v1/embeddings
Content-Type: application/json
Authorization: Bearer sk-...

{
  "model": "Qwen/Qwen3-Embedding-4B",
  "input": "pub fn verify_jwt_token(token: &str) -> Result<User> {\n    let decoded = decode::<Claims>(token, &Key::from_secret(SECRET))?\n}",
  "encoding_format": "float"
}
```

#### 9.1.4 嵌入响应格式

```json
{
  "object": "list",
  "data": [
    {
      "object": "embedding",
      "index": 0,
      "embedding": [0.0123, -0.0456, 0.0789, ...],  // 2560 维向量
      "usage": {
        "prompt_tokens": 128,
        "total_tokens": 128
      }
    }
  ],
  "usage": {
    "prompt_tokens": 128,
    "total_tokens": 128
  }
}
```

### 9.2 存储路径规范

#### 9.2.1 CodeXRay 持久化

```
.codegraph_db/
├── projects.json                    # 项目注册表
│   {
│     "projects": {
│       "{project_id}": {
│         "repo_path": "/path/to/repo",
│         "collection_name": "repo_dir_md5",
│         "status": "completed",
│         "last_updated": 1705315200,
│         "file_hashes": {
│           "src/main.rs": "abc123...",
│           "src/lib.rs": "def456..."
│         }
│       }
│     }
│   }
│
└── {project_id}/
    ├── graph.json                   # JSON 格式图谱
    ├── graph.bin                    # 二进制格式图谱 (可选)
    └── file_hashes.json             # 文件哈希映射

data/
├── lancedb/                         # LanceDB 向量数据库
│   └── {repo_dir_name}_{md5(repo_path)}.lance/
└── embedding_cache.sqlite           # SQLite 嵌入缓存
```

#### 9.2.2 LanceDB 表命名规则

```
表名 = {repo_dir_name}_{md5(repo_full_path)}

示例:
  repo_path: /home/user/my-project
  repo_dir_name: my-project
  md5: a1b2c3d4...
  表名: my-project_a1b2c3d4...
```

### 9.3 配置系统

#### 9.3.1 CodeXRay 配置

```toml
# ~/.codeactor/config/config.toml

[http]
server_port = 12800           # CodeXRay HTTP 端口

[codexray]
enable_embedding = true       # 是否启用语义搜索
# 数据目录自动生成在 $HOME/.codeactor/data/
#   embedding/ — 全局共享索引（BM25 + LanceDB 向量库）
#   graph/     — 项目隔离数据（按 project_id 分目录）
storage_mode = "both"         # JSON / Binary / Both
```

#### 9.3.2 Agent 配置

```toml
[agent]
conductor_max_steps = 30      # Conductor 最大步数
coding_max_steps = 50         # CodingAgent 最大步数
repo_max_steps = 20           # RepoAgent 最大步数
lang = "zh"                   # 输出语言
```

### 9.4 未来扩展

#### 9.4.1 短期扩展 (1-3 个月)

| 功能 | 描述 | 优先级 |
|------|------|--------|
| **缓存预热** | 启动时预加载常用函数的嵌入向量 | 高 |
| **批量语义搜索** | 支持多查询并行搜索 | 中 |
| **索引版本管理** | 支持回滚到历史索引版本 | 中 |
| **增量图谱更新优化** | 减少全量调用关系重分析 | 高 |

#### 9.4.2 中期扩展 (3-6 个月)

| 功能 | 描述 | 优先级 |
|------|------|--------|
| **混合搜索** | 向量搜索 + 关键词搜索融合 | 高 |
| **跨仓库搜索** | 多仓库联合语义搜索 | 中 |
| **代码相似度检测** | 基于向量的代码重复检测 | 中 |
| **实时索引更新** | 文件变更即时更新向量索引 | 高 |

#### 9.4.3 长期扩展 (6-12 个月)

| 功能 | 描述 | 优先级 |
|------|------|--------|
| **图神经网络** | 基于 CodeGraph 的 GNN 嵌入 | 高 |
| **类型感知搜索** | 结合类型系统的语义搜索 | 中 |
| **多模态嵌入** | 代码 + 文档联合嵌入 | 低 |
| **分布式索引** | 大规模代码库分布式向量索引 | 中 |

### 9.5 常见问题 (FAQ)

**Q1: 为什么单进程只能绑定一个仓库？**  
A: 简化 API 设计、避免并发冲突。多仓库场景通过启动多个进程实例解决，每个进程绑定不同 `--repo-path`。

**Q2: 嵌入缓存的 key 为什么包含模型名？**  
A: 不同模型的嵌入向量不兼容，切换模型后必须重新计算。包含模型名的 hash 确保缓存隔离。

**Q3: 如何切换嵌入模型？**  
A: 修改 `config.toml` 中的 `model` 字段，重启 CodeXRay 服务。旧缓存不会误用，但需要重新构建索引。

**Q4: 语义搜索的 `score` 越大越相关吗？**  
A: 是的。该值已统一转换为相关性分数（范围 0-1），值越大表示语义越相似。底层 Dense 搜索使用 `1 / (1 + L2距离)` 将距离转换为分数。

**Q5: CodeXRay 服务与 Agent 如何协同？**  
A: Agent 启动时自动启动 CodeXRay 子进程 (:12800)，通过 `RepoOperationsTool` 封装 HTTP 调用。服务生命周期由 Agent 管理。

### 9.6 术语表

| 术语 | 英文 | 说明 |
|------|------|------|
| 代码图谱 | CodeGraph | 函数调用有向图，节点为函数，边为调用关系 |
| 向量索引 | Vector Index | LanceDB 中存储的代码嵌入向量 |
| 嵌入 | Embedding | 将代码文本转换为固定维度向量的过程 |
| 语义搜索 | Semantic Search | 基于向量相似度的代码检索 |
| 骨架 | Skeleton | 函数/类签名，不包含实现细节 |
| 代码片段 | Code Snippet | 函数的完整实现代码 |
| 预调查 | Pre-investigation | RepoAgent 启动前自动获取仓库全景 |
| 防抖 | Debounce | 事件聚合机制，20 秒内合并多个文件变更 |
| 持久化 | Persistence | 将图谱保存到磁盘 (JSON/Bincode) |
| 增量更新 | Incremental Update | 仅处理变更的文件，跳过未变更文件 |

---

> **文档维护**: 本文档应随 API 变更同步更新。  
> **反馈渠道**: 提交 Issue 或 PR 到 `codeactor-agent` 仓库 `docs/` 目录。
