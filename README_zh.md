# CodeActor Agent

基于 Go 语言开发的 **Hub-and-Spoke（中枢-辐条）多智能体架构** AI 自主编程助手，后端由 Rust 代码分析引擎支撑。

CodeActor Agent 协调多个专用智能体——指挥家（Conductor）、仓库分析员（Repo-Analyst）、编码工程师（Coding-Engineer）、对话助手（Chat-Assistant）、运维操作员（DevOps-Operator）和元代理（Meta-Agent）——自主完成复杂的软件工程任务，具备自我纠错能力。

## 特性

### 多智能体系统
- **中枢-辐条架构** — 中央 Conductor 将任务委派给专用子智能体（仓库分析、代码编辑、通用对话、运维操作）
- **元代理（Meta-Agent）** — 自主设计代理，在运行时为超出内置 Agent 能力的任务动态创建自定义子智能体
- **自我修正** — `thinking` 工具使 Agent 能够在出错时分析原因并恢复，避免盲目重试
- **Agent 禁用** — 通过 `--disable-agents=repo,coding,chat,meta,devops` 在启动时有条件地排除子智能体
- **ImplPlan 工具** — 状态化实现计划文档，用于复杂多步骤编码任务的分步规划

### 丰富工具系统（17 个工具）
- **文件操作** — 读取、创建、删除、重命名、列出目录、打印目录树
- **代码编辑** — `search_replace_in_file` 精准替换，返回 unified diff，带 10MB 大小保护
- **代码搜索** — ripgrep 正则搜索、基于向量嵌入的语义搜索、代码骨架/片段查询
- **Shell 执行** — `run_bash` 支持前台/后台运行，含危险检测和工作空间边界检查
- **认知工具** — `thinking` 错误分析反思、`micro_agent` 子 LLM 推理调用
- **流程控制** — `finish` 任务完成通知、用户帮助请求
- **仓库分析** — 调用图查询、层级调用树、目录树、函数级代码骨架

### 双交互模式
- **TUI 模式** — 基于 Bubble Tea 的全功能终端界面，支持消息日志、Agent 流式输出和交互式授权
- **HTTP + WebSocket 服务** — REST API 和实时 WebSocket 流式推送，用于 IDE/Web 集成

### LLM 基础设施
- **官方 OpenAI Go SDK** — 用 `openai-go/v3` 替换 langchaingo，实现直接 API 控制
- **DeepSeek 推理支持** — 完整 `reasoning_content` 往返（流式 + 非流式），通过 `SetExtraFields` 注入
- **自定义 Engine 抽象层** — 轻量级 `Engine` 接口，Message/ToolDef/ToolCall 类型与 SDK 解耦
- **13 个 LLM 提供商** — 小米 MiMo、阿里 Qwen、DeepSeek、硅基流动、Moonshot、Mistral、智谱 GLM、OpenRouter、StreamLake、AWS Bedrock 及任意 OpenAI 兼容端点

### 安全机制
- **WorkspaceGuard** — 验证文件操作不超出项目工作空间，拦截危险 shell 命令
- **纵深防御** — 同时检查 LLM 标记的 `is_dangerous` 和命令中的绝对路径分析
- **用户确认管道** — 基于 Pub-Sub 的确认流程，同时适用于 TUI 和 WebSocket 消费者

### Codebase 分析引擎（Rust）
- **Tree-sitter 多语言解析** — AST 级解析支持 Rust、Python、JavaScript、TypeScript、Java、C++、Go
- **调用图分析** — 函数级调用图，含调用者/被调者关系、环路检测、复杂度评分
- **语义代码搜索** — 通过 LanceDB + SQLite 缓存的向量嵌入（OpenAI `text-embedding-3-small`）
- **代码骨架/片段** — 批量文件骨架提取和按函数的代码片段检索

## 效果截图

<p align="center">
  <img src="docs/sceenshot-1.png" alt="CodeActor TUI 截图 1" width="49%">
  <img src="docs/sceenshot-2.png" alt="CodeActor TUI 截图 2" width="49%">
</p>

## 快速开始

### 环境要求

- Go 1.24+
- `ripgrep` (`rg`) — 全文正则搜索
- `codeactor-codebase` 服务（Go 二进制自动启动，也可手动设置）

### 安装

```bash
git clone https://github.com/your-org/codeactor-agent.git
cd codeactor-agent
go build -o codeactor .
```

### 配置

创建 `$HOME/.codeactor/config/config.toml`：

```toml
[llm]
use_provider = "siliconflow"

[llm.providers.siliconflow]
model = "deepseek-ai/DeepSeek-V3.2"
temperature = 0.0
max_tokens = 23000
api_base_url = "https://api.siliconflow.cn/v1"
api_key = "your-api-key-here"

[app]
enable_streaming = true

[agent]
conductor_max_steps = 30
coding_max_steps = 50
repo_max_steps = 30
devops_max_steps = 15
meta_max_steps = 30
meta_retry_count = 5
lang = "Chinese"
```

### 运行

**TUI 模式**（终端界面）：
```bash
./codeactor tui
# 或携带任务文件：
./codeactor tui --taskfile TASK.md
# 禁用特定 Agent：
./codeactor tui --disable-agents=meta
```

**HTTP 服务模式**（API + WebSocket）：
```bash
./codeactor http
# 服务启动在 http://localhost:9800

# 自定义端口：
./codeactor http --port 9090
```

### Node.js CLI 客户端

```bash
cd clients/nodejs-cli && npm install
node index.js run <project-dir> "任务描述"             # 创建并流式输出任务
node index.js chat <task-id> <project-dir>             # 继续对话
node index.js status <task-id>                         # 查询状态
node index.js memory <task-id>                         # 查看对话历史
node index.js history                                  # 列出最近任务
```

服务默认连接 `localhost:9080`。可通过 `--host`/`--port` 或环境变量 `CODECACTOR_HOST=host:port` 覆盖。

## 架构

<p align="center">
  <img src="docs/architecture.svg" alt="CodeActor Agent 架构图" width="900">
</p>

### 技术栈

| 层级 | 技术 |
|-------|-----------|
| 语言 | Go 1.24+, Rust（codebase 引擎） |
| LLM SDK | `github.com/openai/openai-go/v3` |
| HTTP/WS | Gin + Melody |
| TUI | Bubble Tea + Lipgloss + Glamour |
| 代码分析 | Tree-sitter, Petgraph, LanceDB, Axum |
| Diff | `github.com/aymanbagabas/go-udiff` |

### 各 Agent 工具分配

| Agent | 工具 | 数量 |
|-------|-------|-------|
| Conductor | `delegate_repo`、`delegate_coding`、`delegate_chat`、`delegate_devops`、`delegate_meta`、`finish`、`read_file`、`search_by_regex`、`list_dir`、`print_dir_tree` | 10 |
| CodingAgent | 全部 17 个工具（文件、搜索、Shell、thinking、impl_plan、micro_agent） | 17 |
| RepoAgent | `read_file`、`search_by_regex`、`list_dir`、`print_dir_tree`、`semantic_search`、`query_code_skeleton`、`query_code_snippet` | 7 |
| ChatAgent | `micro_agent`、`thinking`、`finish` | 3 |
| DevOpsAgent | `run_bash`、`read_file`、`list_dir`、`print_dir_tree`、`search_by_regex`、`thinking`、`micro_agent`、`finish` | 8 |

[完整架构文档 →](docs/ARCHITECTURE.md)

## Meta-Agent（元代理）

**Meta-Agent** 是一个自主代理设计器——它在运行时创建专用子智能体，按需扩展系统能力。当 Conductor 遇到超出内置 Agent（Repo/Coding/Chat）专业范围的任务时，它会委派给 Meta-Agent，后者将：

1. **设计**自定义 Agent 的系统提示词、工具选择和结果结构
2. **执行**任务，使用设计好的 Agent 配置
3. **注册**新 Agent 为永久委托工具，供会话后续使用

### 示例用例

- `delegate_security_auditor` — 全代码库安全漏洞审计
- `delegate_performance_profiler` — 性能瓶颈分析
- `delegate_db_migration_planner` — 数据库迁移规划与验证

### 配置

```toml
[agent]
meta_max_steps = 30    # Meta-Agent 最大 LLM 步数（默认 30）
meta_retry_count = 5   # JSON 解析失败重试次数（默认 5）
```

通过启动参数禁用 Meta-Agent：

```bash
./codeactor tui --disable-agents=meta
```

## DevOps-Agent（运维代理）

**DevOps-Agent** 是运维和基础设施专家——通过执行 Shell 命令、检查文件系统和分析命令输出来处理所有非编码的运维任务。当 Conductor 遇到系统管理、日志检查、进程管理或 ad-hoc shell 命令类任务时，会通过 `delegate_devops` 委派给 DevOps-Agent。

### 核心能力

- **Shell 命令执行** (`run_bash`) — 运行任意 bash 命令，支持前台/后台运行，含危险检测和工作空间边界检查
- **文件系统检查** — `read_file`、`list_dir`、`print_dir_tree`、`search_by_regex` 用于浏览日志、配置和目录
- **自我修正** — 使用 `thinking` 工具分析命令失败原因，调整策略后重试
- **独立分析** — 使用 `micro_agent` 对命令输出进行深度推理或生成结构化报告

### 示例用例

- 检查磁盘使用率、内存和系统资源
- 查找最近 24 小时内修改的所有日志文件
- 重启服务或检查进程状态
- 检查配置文件
- 运行系统诊断并生成报告
- 执行 ad-hoc shell 管道进行数据处理

### 配置

```toml
[agent]
devops_max_steps = 15    # DevOps-Agent 最大 LLM 步数（默认 15）
```

通过启动参数禁用 DevOps-Agent：

```bash
./codeactor tui --disable-agents=devops
```

## Codebase 分析引擎

`codeactor-codebase` 是一个独立的 **Rust** 服务，提供深度代码分析能力。它作为后台 HTTP 服务器运行，由 Go 二进制自动管理。

### 核心能力

- **AST 级解析** — 基于 Tree-sitter 语法解析，支持 Rust、Python、JavaScript/TypeScript、Java、C++、Go
- **调用图** — 函数级 `CallGraph`，按出度排名，支持调用者/被调者遍历、环路检测和复杂度报告
- **语义搜索** — 向量嵌入（OpenAI `text-embedding-3-small`，1536 维），存储于 LanceDB，带 SQLite 元数据缓存
- **代码骨架/片段** — 批量提取函数/类签名或完整实现（按文件路径+函数名）
- **文件监听** — 基于 `notify` 的文件系统监听器，20s 防抖，自动重索引
- **层级调用树** — 带深度限制的调用树遍历，帮助理解代码流

### HTTP API

| 方法 | 路径 | 说明 |
|--------|------|------|
| `GET` | `/health` | 健康检查 |
| `GET` | `/status` | 仓库状态（函数数、文件数、嵌入状态） |
| `POST` | `/investigate_repo` | 返回出度 Top-15 函数、目录树、文件骨架 |
| `POST` | `/semantic_search` | 基于向量的语义代码搜索 |
| `POST` | `/query_code_skeleton` | 批量从文件路径提取骨架 |
| `POST` | `/query_code_snippet` | 按 `filepath` + `function_name` 提取代码片段 |
| `POST` | `/query_call_graph` | 按文件/函数名查询调用图 |
| `POST` | `/query_hierarchical_graph` | 带深度限制的层级调用树 |
| `POST` | `/query_indexing_status` | 嵌入索引状态 |
| `GET` | `/draw_call_graph` | ECharts 调用图可视化 |

### 生命周期管理

Go 二进制负责完整生命周期：
1. **动态端口分配** — 从 12800 向上扫描，寻找可用端口
2. **二进制提取** — 将内嵌的 `codeactor-codebase` 提取到 `~/.codeactor/bin/`
3. **自动启动** — 以子进程方式启动 Rust 服务器，传入 `--repo-path` 和 `--address`
4. **健康轮询** — 最多等待 30s，直至 `/health` 返回 200
5. **HTTP 重试** — 所有 codebase API 调用最多重试 3 次，带退避
6. **退出清理** — Go 进程退出时 `defer` 杀死子进程

### 配置

```toml
[http]
codebase_port = 12800

[codebase]
enable_embedding = true
embedding_db_uri = "~/.codeactor/data/lancedb"
graph_db_uri = "~/.codeactor/data/graph"

[codebase.embedding]
model = "text-embedding-3-small"
api_token = "sk-..."
api_base_url = "https://api.openai.com/v1"
dimensions = 1536
```

## API 概览

### REST 接口

| 方法 | 路径 | 说明 |
|--------|------|------|
| `POST` | `/api/start_task` | 启动或恢复编码任务 |
| `GET` | `/api/task_status?task_id=` | 查询任务状态和记忆 |
| `POST` | `/api/cancel_task` | 取消运行中的任务 |
| `GET` | `/api/history` | 历史任务列表 |
| `POST` | `/api/load_task` | 从持久化恢复任务 |
| `GET` | `/api/memory?task_id=` | 获取对话记忆 |
| `DELETE` | `/api/memory?task_id=` | 清空对话记忆 |

### WebSocket

连接到 `ws://localhost:9800/ws`

| 客户端事件 | 说明 |
|-------------|------|
| `start_task` | 创建并启动新编码任务 |
| `chat_message` | 发送后续对话消息 |
| `get_memory` | 获取对话记忆 |
| `clear_memory` | 清空对话记忆 |

详细 API 文档见 [docs/Agent_Reference.md](docs/Agent_Reference.md)。

## 支持的 LLM 提供商

| 提供商 | 配置键 | 模型示例 |
|----------|-----------|------|
| 小米 MiMo | `xiaomi` | `mimo-v2-flash` |
| 阿里云百炼 | `aliyun` | `qwen3-coder-plus` |
| 硅基流动 | `siliconflow` | `deepseek-ai/DeepSeek-V3.2` |
| DeepSeek | `deepseek` | `deepseek-ai/DeepSeek-V3` |
| Moonshot | `moonshot` | `moonshotai/Kimi-K2-Instruct` |
| Mistral | `mistral` | `mistralai/devstral-small` |
| 智谱 Z.ai | `zai` | `zai-org/GLM-4.5-Air` |
| OpenRouter | `openrouter` | `qwen3-coder-plus` |
| StreamLake | `streamlake` | 自定义端点 |
| AWS Bedrock | `bedrock` | `us.anthropic.claude-3-7-sonnet-*` |
| 本地 | `local` | 任意 OpenAI 兼容服务 |

## 文档

- [ARCHITECTURE.md](docs/ARCHITECTURE.md) — 系统架构、模块、数据流、协议
- [Agent_Reference.md](docs/Agent_Reference.md) — API 参考和配置指南
- [Agent_Design.md](docs/Agent_Design.md) — 多智能体设计理念

## 许可证

[Apache License 2.0](LICENSE)
