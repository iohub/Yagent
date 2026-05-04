# CodeActor Codebase

基于 Rust 构建的代码分析引擎，提供 AST 解析、函数调用图谱构建、向量嵌入索引和语义搜索功能。支持 6 种编程语言，通过 HTTP API 对外暴露所有能力。

## 核心设计：单进程单仓库

每个进程在启动时通过 `--repo-path` 绑定一个仓库，之后所有 API 操作都针对该仓库，无需在请求中重复指定路径。`StorageManager.current_repo` 管理绑定状态，`try_bind_repo()` 拒绝切换仓库。

## 功能

### 代码解析与图谱构建
- **多语言支持**：Rust、Python、JavaScript/TypeScript、Go、C/C++、Java
- **AST 解析**：基于 tree-sitter 的精确语法分析
- **调用图谱**：构建函数间调用关系的有向图（`PetCodeGraph`，基于 petgraph）
- **增量分析**：MD5 哈希检测文件变更，跳过未修改文件
- **层次化视图**：按文件/函数展开的层级调用树

### 向量嵌入与语义搜索
- **代码向量化**：对函数/类代码块生成向量嵌入，支持 OpenAI 兼容 API
- **LanceDB 存储**：嵌入式向量数据库，零外部依赖
- **SQLite 缓存**：嵌入结果缓存，避免重复 API 调用
- **增量索引**：通过文件 MD5 哈希判断是否需要重新向量化
- **语义搜索**：用自然语言描述搜索匹配的代码块

### 可视化
- **ECharts 交互式图谱**：Web 界面可视化函数调用关系，支持缩放/拖拽/高亮
- **目录树导出**：生成 ASCII 风格目录结构

### HTTP API
- RESTful 接口，Axum 框架，内置 CORS 支持
- 统一的 `ApiResponse<T>` 响应格式（`success` + `data`）
- 所有端点无状态，共享 `Arc<StorageManager>`

### 文件监听
- 基于 `notify` crate，20 秒防抖
- 源码变更后自动重解析图谱 + 重建嵌入索引

<img width="720" src="assets/demo.gif" alt="graph demo"/><br>

## 快速开始

### 前置条件

- Rust 1.70+
- 嵌入 API Token（可选，使用语义搜索时需要，如 SiliconFlow）

### 构建

```bash
cargo build --release
# 二进制文件: target/release/codeactor-codebase
```

### 启动服务

```bash
# 必须指定 --repo-path
cargo run -- server --repo-path /path/to/your/repo

# 自定义地址和端口
cargo run -- server --repo-path /path/to/your/repo --address 0.0.0.0:12800

# 启用 verbose 日志
cargo run -- server --repo-path /path/to/your/repo -v

# 选择存储模式（json / binary / both）
cargo run -- server --repo-path /path/to/your/repo --storage-mode binary
```

### 命令行向量化

```bash
cargo run -- vectorize --path /path/to/code --collection my-collection --db-uri data/lancedb
```

## HTTP API 参考

### 端点一览

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/health` | 健康检查 |
| GET | `/status` | 当前仓库状态（路径、函数数、文件数、索引状态） |
| POST | `/query_call_graph` | 查询函数调用图谱 |
| POST | `/query_code_snippet` | 提取代码片段（带行号上下文） |
| POST | `/query_code_skeleton` | 批量提取文件骨架（函数/类签名） |
| POST | `/query_hierarchical_graph` | 层级调用树（可指定根函数和深度） |
| POST | `/investigate_repo` | 仓库全景分析：Top 15 高出入度函数 + 目录树 + 文件骨架 |
| POST | `/semantic_search` | 语义搜索代码块 |
| POST | `/query_indexing_status` | 查询嵌入索引状态 |
| GET | `/` 和 `/draw_call_graph` | ECharts 调用图谱可视化页面 |

所有响应格式：

```json
{
  "success": true,
  "data": { ... }
}
```

### 示例

#### 查询调用图谱

```bash
curl -X POST http://localhost:12800/query_call_graph \
  -H "Content-Type: application/json" \
  -d '{
    "filepath": "src/main.rs",
    "function_name": "main",
    "max_depth": 3
  }'
```

#### 提取代码片段

```bash
curl -X POST http://localhost:12800/query_code_snippet \
  -H "Content-Type: application/json" \
  -d '{
    "filepath": "src/main.rs",
    "function_name": "handle_request",
    "context_lines": 5,
    "include_context": true
  }'
```

#### 批量提取文件骨架

```bash
curl -X POST http://localhost:12800/query_code_skeleton \
  -H "Content-Type: application/json" \
  -d '{
    "filepaths": ["src/main.rs", "src/lib.rs", "src/http/server.rs"]
  }'
```

#### 仓库全景分析

```bash
curl -X POST http://localhost:12800/investigate_repo \
  -H "Content-Type: application/json" \
  -d '{}'
```

返回：Top 15 出度最高的核心函数（含调用者/被调用者）、ASCII 目录树、相关文件的代码骨架。

#### 语义搜索

```bash
curl -X POST http://localhost:12800/semantic_search \
  -H "Content-Type: application/json" \
  -d '{
    "text": "handle HTTP request and return JSON response",
    "limit": 10
  }'
```

#### 层级调用树

```bash
curl -X POST http://localhost:12800/query_hierarchical_graph \
  -H "Content-Type: application/json" \
  -d '{
    "root_function": "main",
    "max_depth": 3,
    "include_file_info": true
  }'
```

#### 状态查询

```bash
curl http://localhost:12800/status
```

返回当前绑定仓库路径、函数/文件数量、嵌入索引状态。

### 可视化

浏览器访问 `http://localhost:12800/` 或 `http://localhost:12800/draw_call_graph?filepath=src/main.rs&function_name=main&max_depth=3`。

## 配置

配置文件位于 `~/.codeactor/config/config.toml`：

```toml
[http]
server_port = 12800
codebase_port = 12800

[codebase]
enable_embedding = true
embedding_db_uri = "data/lancedb"
graph_db_uri = ".codegraph_db"

[codebase.embedding]
model = "Qwen/Qwen3-Embedding-4B"
api_token = "sk-..."
api_base_url = "https://api.siliconflow.cn/v1"
dimensions = 2560
```

嵌入索引状态存储在 `{embedding_db_uri}/projects.json`，文件哈希用于增量更新。

## 向量嵌入索引

### 工作流程

1. **代码解析**：tree-sitter 提取函数和结构体声明
2. **内容提取**：获取完整代码块
3. **缓存查询**：SQLite 检查 `hash(model + code)` 是否已有缓存向量
4. **嵌入生成**：调用 OpenAI 兼容 API 生成向量
5. **LanceDB 存储**：向量 + 元数据（文件路径、符号名、语言、行号、代码块）
6. **批量处理**：每 100 条批量写入

### 向量元数据

每个向量包含：`id`, `vector`, `file_path`, `symbol_name`, `symbol_type`, `language`, `line_start`, `line_end`, `code_block`

LanceDB 表名规则：`{repo_dir_name}_{md5(repo_path)}`

## 支持的编程语言

| 语言 | 函数 | 结构体/类 | 函数调用 | 注释 |
|------|------|-----------|----------|------|
| Rust | yes | yes | yes | yes |
| Python | yes | yes | yes | yes |
| JavaScript/TypeScript | yes | yes | yes | yes |
| Go | yes | yes | yes | yes |
| C/C++ | yes | yes | yes | yes |
| Java | yes | yes | yes | yes |

## 存储

- 图谱持久化：`.codegraph_db/{project_id}/graph.json`（或 `.bin`）
- 文件哈希：`.codegraph_db/{project_id}/file_hashes.json`
- 项目注册表：`.codegraph_db/projects.json`
- 嵌入数据库：LanceDB（路径由配置指定）
- 嵌入缓存：SQLite（`embedding_cache.sqlite`，与 LanceDB 同目录）

存储模式通过 `--storage-mode` 指定：`json`、`binary`、`both`（默认 `json`）。

## 架构

```
src/
├── main.rs              # CLI 入口：server 和 vectorize 子命令
├── lib.rs               # 顶层模块导出
├── config.rs            # 配置加载 (~/.codeactor/config/config.toml)
├── cli/                 # CLI 参数解析 (clap)、runner、analyze、vectorize
├── codegraph/           # AST 解析 + 图数据结构
│   ├── graph.rs         # 平坦 CodeGraph（HashMap 结构）
│   ├── types.rs         # PetCodeGraph、EntityGraph、FileIndex、SnippetIndex
│   ├── parser.rs        # CodeParser：tree-sitter 解析 + 增量构建
│   └── treesitter/      # 多语言解析器（Rust/Python/JS/TS/Java/C++/Go）
├── services/            # 高级分析服务
│   ├── analyzer.rs      # CodeAnalyzer：调用链、循环检测、复杂度报告
│   ├── embedding_service.rs  # LanceDB 向量嵌入 + SQLite 缓存 + 语义搜索
│   └── snippet_service.rs    # 代码片段提取 + 缓存
├── storage/             # 图谱持久化和文件监听
│   ├── persistence.rs   # 图谱保存/加载（JSON + binary）+ 项目注册表
│   ├── petgraph_storage.rs  # PetCodeGraph 序列化（JSON、bincode、GraphML、GEXF）
│   ├── incremental.rs   # MD5 增量文件变更检测
│   └── mod.rs           # StorageManager：中心枢纽（图谱、监听器、任务、配置）
└── http/                # Axum HTTP 服务
    ├── server.rs        # CodeBaseServer：启动初始化 + 路由
    ├── handlers/        # 请求处理（query、search、investigate、embed）
    └── models/          # 请求/响应类型 + ApiResponse<T>
```

## 核心类型

- **`StorageManager`**：中心状态管理器 — 内存图谱、持久化、文件监听、嵌入任务、配置、仓库绑定
- **`PetCodeGraph`**：`petgraph::DiGraph<FunctionInfo, CallRelation>`，提供 `get_callers`、`get_callees`、`find_functions_by_name`、`find_functions_by_file` 等查询方法
- **`CodeAnalyzer`**：封装 `CodeParser`，执行完整目录分析，提供循环检测、复杂度分析等方法
- **`CodeBaseServer`**：Axum 服务器；`start()` 自动初始化（加载/分析图谱 + 嵌入 + 监听）后绑定端口
- **`EmbeddingService`**：LanceDB 后端，表名 `{last_dir}_{md5(repo_path)}`，批处理嵌入 + SQLite 缓存
- **`PersistenceManager`**：文件图谱持久化到 `.codegraph_db/{project_id}/`

## 开发

```bash
# 运行测试
cargo test

# 运行特定测试
cargo test test_build_graph_functionality

# 输出测试日志
cargo test -- --nocapture
```

## License

MIT
