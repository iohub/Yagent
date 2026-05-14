# CodeActor — 结构化代码智能，驱动自主 AI 编程

> **超越文本生成。** CodeActor 建立代码库的*认知模型*——调用图、语义搜索、架构分析——让智能体以极致精度导航、理解并演进你的代码。

**厌倦了只会「看」文本的 AI 编程工具？**  
传统助手把代码当作文本流处理，导致幻觉式建议、盲目编辑，以及面对「哪些模块依赖这个函数？」时的一筹莫展。CodeActor 截然不同。它的 **Repo-Agent** 通过深度结构化分析，像资深工程师一样理解你的软件——早在动手写第一行代码之前。

<p align="center">
  <img src="docs/sceenshot-1.png" alt="CodeActor TUI 截图 1" width="49%">
  <img src="docs/sceenshot-2.png" alt="CodeActor TUI 截图 2" width="49%">
</p>

## 为什么选择 CodeActor？

传统 AI 编程助手有一个根本缺陷：它们把代码视为扁平文本。这导致：

- ❌ **幻觉式 API** — 建议你代码库中根本不存在的函数
- ❌ **缺乏架构感知** — 改一行代码，悄然崩掉远处依赖它的模块
- ❌ **盲目重构** — 无法评估跨文件影响，也无法检测循环依赖
- ❌ **仅靠关键词搜索** — 变量名不同就搜不到，遗漏关键逻辑

CodeActor 从根源上解决了这个问题。由 Rust 代码智能引擎驱动，它构建代码的丰富结构模型——AST、调用图、语义嵌入——让系统中的每个 Agent 都能像资深工程师一样推理代码。

| 传统 AI 工具 | CodeActor |
|---|---|
| 扁平文本匹配 | 按代码*含义*进行语义搜索 |
| 逐文件编辑 | 通过调用图分析跨文件影响 |
| 无复杂度感知 | 环路检测与复杂度评分 |
| 基于正则搜索 | 自然语言查询「找到认证逻辑」 |
| 单智能体 | 中枢-辐条多智能体 + Meta-Agent 运行时扩展 |
| 🌐 **实时网络调研** | 无法自主上网浏览 | 内置浏览器智能体可自主导航网页，查找最新文档、API 及 Issue 讨论 |

## 特性

### 多智能体系统
- **中枢-辐条架构** — 中央 Conductor 将任务委派给专用子智能体（仓库分析、代码编辑、通用对话、运维操作、浏览器自动化）
- **元代理（Meta-Agent）** — 自主设计代理，在运行时为超出内置 Agent 能力的任务动态创建自定义子智能体
- **自我修正** — `thinking` 工具使 Agent 能够在出错时分析原因并恢复，避免盲目重试
- **Agent 禁用** — 通过 `--disable-agents=repo,coding,chat,meta,devops,browser` 在启动时有条件地排除子智能体

### 丰富工具系统（22 个工具）
- **文件操作** — 读取、创建、删除、重命名、列出目录、打印目录树
- **代码编辑** — `search_replace_in_file` 精准替换，返回 unified diff，带 10MB 大小保护
- **代码搜索** — ripgrep 正则搜索、基于向量嵌入的语义搜索、代码骨架/片段查询
- **Shell 执行** — `run_bash` 支持前台/后台运行，含危险检测和工作空间边界检查
- **认知工具** — `thinking` 错误分析反思、`micro_agent` 子 LLM 推理调用
- **流程控制** — `finish` 任务完成通知、用户帮助请求
- **浏览器自动化** — `delegate_browser` 无头 Chrome 网页调研、导航、数据提取、截图和 PDF 生成
- **仓库分析** — 调用图查询、层级调用树、目录树、函数级代码骨架

### 双交互模式
- **TUI 模式** — 基于 Bubble Tea 的全功能终端界面，支持消息日志、Agent 流式输出和交互式授权
- **HTTP + WebSocket 服务** — REST API 和实时 WebSocket 流式推送，用于 IDE/Web 集成

### 🌐 浏览器智能体：自主网页智能

> *「让你的 AI 替你上网阅读——从在线文档、社区讨论和 API 参考中实时查找答案。」*

**浏览器智能体（Browser-Agent）**让 CodeActor 成为真正的网络原生助手。它基于 [go-rod](https://github.com/go-rod/rod) 驱动无头 Chrome，能够在安全的沙箱环境中自主导航网页、与页面元素交互并提取知识。当本地文档不足时，指挥 Agent 会将网络调研任务委派给浏览器智能体，由它上网查找最新答案。

**它能做什么：**

- **🔍 自主网络调研** — 浏览文档站点、GitHub Issues、Stack Overflow 和 API 参考。在互联网上实时查找答案，无需人工复制链接。
- **🖱️ 完整页面交互** — 点击按钮、填写并提交表单、滚动页面、等待动态内容加载。
- **📄 数据提取** — 从任意网页提取文本和 HTML。捕获全页面或元素级截图，生成 PDF 文件。
- **🧠 JavaScript 执行** — 在页面上下文中运行自定义 JS（需用户明确确认），解锁需要客户端逻辑的 Web 应用。
- **🔒 安全优先** — 所有文件输出限制在工作区目录内。每个任务通过 Cookie 管理获得独立的浏览器会话。
- **📊 健康监控** — 检查网站可用性并监控内容变动，支持主动运维。

浏览器智能体由指挥 Agent 通过 `delegate_browser` 调度，无缝融入多智能体工作流。它配备专属工具集（`navigate`、`go_back`、`go_forward`、`reload`、`get_current_url`、`click`、`input`、`scroll`、`wait_element`、`wait`、`extract_text`、`extract_html`、`screenshot`、`pdf`、`execute_js`），遵循与其他 Agent 相同的 LLM-工具循环模式。

> *示例：* 开发者提问：「查找最新的 FastAPI 中间件文档并总结 CORS 配置。」浏览器智能体导航至 FastAPI 文档，定位中间件章节，提取相关文本，返回简洁摘要——全程无需开发者离开编辑器。

### 📝 Git Commit 学习

> *"让你的 AI 了解项目的最近演进——这样它就不会重复工作或与新架构冲突。"*

**Git Commit 学习**系统使 CodeActor 能够自动从项目的 git 历史记录中学习，理解最近的代码变更，并将这些知识作为用户任务的上下文。指挥家分析最近的提交，通过 LLM 生成结构化摘要，并将它们存储在向量数据库中用于语义相似度搜索。

**功能特性：**

- **📖 自动提交分析** — 获取最近 N 个提交（默认：30），解析提交信息、变更文件和 diff
- **🧠 结构化摘要** — LLM 生成简洁的摘要，涵盖：满足的需求、变更的文件、技术思路和实现细节
- **🔍 语义相似度搜索** — 提交嵌入存储在 LanceDB 中；用户查询通过含义（而非仅关键词）与提交摘要进行匹配
- **🔗 上下文注入** — 高相似度提交摘要自动注入 Agent 的上下文，使其了解项目的最近演进
- **⚡ 智能缓存** — 使用 git HEAD hash 作为缓存键；当没有新提交时跳过重复处理
- **🛠️ 手动控制** — 通过 `learn_commits` 工具触发学习，或使用 `search_similar_commits("用户认证")` 显式搜索

**配置：**

```toml
[commit_learner]
enabled = true                          # 启用/禁用
max_commits = 30                        # 学习的最近提交数量
similarity_threshold = 0.75             # 包含在上下文中的最小余弦相似度
top_k = 3                               # 注入的最相关提交数量
trigger = "both"                        # "on_demand" | "on_session_start" | "both"
cache_ttl = 3600                        # 缓存有效期（秒）
rust_service_url = "http://127.0.0.1:12800"  # Rust 代码分析服务地址
```

**工作流程：**

```
git log（获取提交）
    → LLM（生成结构化摘要）
    → 嵌入服务（转换为向量）
    → LanceDB（存储在 commit_embeddings 表）
    → 用户查询（语义搜索）
    → Top-K 匹配提交
    → 注入 Agent 上下文
```

系统与多智能体工作流无缝集成。指挥家在会话启动时自动触发学习（可配置），并在将任务委派给子智能体之前注入相关的提交上下文。


### LLM 基础设施
- **官方 OpenAI Go SDK** — 用 `openai-go/v3` 替换 langchaingo，实现直接 API 控制
- **DeepSeek 推理支持** — 完整 `reasoning_content` 往返（流式 + 非流式），通过 `SetExtraFields` 注入
- **自定义 Engine 抽象层** — 轻量级 `Engine` 接口，Message/ToolDef/ToolCall 类型与 SDK 解耦
- **13 个 LLM 提供商** — 小米 MiMo、阿里 Qwen、DeepSeek、硅基流动、Moonshot、Mistral、智谱 GLM、OpenRouter、StreamLake、AWS Bedrock 及任意 OpenAI 兼容端点

### 安全机制
- **WorkspaceGuard** — 验证文件操作不超出项目工作空间，拦截危险 shell 命令
- **纵深防御** — 同时检查 LLM 标记的 `is_dangerous` 和命令中的绝对路径分析
- **用户确认管道** — 基于 Pub-Sub 的确认流程，同时适用于 TUI 和 WebSocket 消费者

## 智能核心：Repo-Agent

CodeActor 的心脏是 **Repo-Agent**——一个由 Rust 引擎驱动的代码智能代理，集成了 Tree-sitter、LanceDB 向量嵌入和 Petgraph 调用图分析。

### 🧠 语义代码搜索
*「找到认证逻辑的实现位置，即使关键词完全不同。」*

基于 LanceDB 向量嵌入（OpenAI `text-embedding-3-small`，1536 维），语义搜索理解查询背后的*意图*。与传统正则搜索不同，它按含义匹配代码——跨越命名差异、语言风格，甚至注释中的描述。

### 🏗️ 代码骨架与片段提取
*「无需手动翻阅，瞬间看清 5000 行文件中所有公开函数。」*

批量查询返回指定文件的结构化大纲（函数、类型、导入）。需要某个函数的完整实现？通过 `文件路径` + `函数名` 一键获取。省下数小时的人工代码阅读时间。

### 🔗 调用图分析
*「这个废弃工具的调用链有多长？是否存在循环依赖？」*

函数级调用图，支持调用者/被调者遍历、环路检测和复杂度评分。在动手改代码前，先看清连锁反应。按出度排名快速定位核心模块。

### 🌲 层级调用树
*「展示请求从 Handler 到数据库的前 3 层调用流程。」*

带深度限制的调用树遍历，以合适的粒度呈现全局架构流程。特别适合新人上手、代码审查和架构文档编写。

### 🌍 多语言 AST 解析
Tree-sitter 语法支持 **Rust、Python、JavaScript、TypeScript、Java、C++、Go**——代码在语法层面被理解，而不仅仅是字节串。实现精确的函数提取、依赖分析和跨语言结构查询。

### ⚡ 自动索引与文件监听
基于 `notify` 的文件系统监听器，20 秒防抖，自动保持代码模型同步。在 IDE 中编辑文件——CodeActor 自动重新索引。

## 多智能体系统

CodeActor 采用**中枢-辐条（Hub-and-Spoke）架构**，由中央 Conductor 统筹调度，各智能体各司其职：

| Agent | 工具 | 数量 |
|-------|-------|-------|
| Conductor | `delegate_repo`、`delegate_coding`、`delegate_chat`、`delegate_devops`、`delegate_meta`、`delegate_browser`、`finish`、`read_file`、`search_by_regex`、`list_dir`、`print_dir_tree` | 12 |
| CodingAgent | 全部 16 个工具（文件、搜索、Shell、thinking、micro_agent） | 16 |
| RepoAgent | `read_file`、`search_by_regex`、`list_dir`、`print_dir_tree`、`semantic_search`、`query_code_skeleton`、`query_code_snippet` | 7 |
| ChatAgent | `micro_agent`、`thinking`、`finish` | 3 |
| DevOpsAgent | `run_bash`、`read_file`、`list_dir`、`print_dir_tree`、`search_by_regex`、`thinking`、`micro_agent`、`finish` | 8 |
| BrowserAgent | `navigate`、`go_back`、`go_forward`、`reload`、`get_current_url`、`click`、`input`、`scroll`、`wait_element`、`wait`、`extract_text`、`extract_html`、`screenshot`、`pdf`、`execute_js`、`thinking`、`micro_agent`、`finish` | 18 |

每个 Agent 配备专属工具集，确保专注高效地完成任务。Conductor 根据任务类型智能路由到最合适的 Agent。

## 架构

<p align="center">
  <img src="docs/architecture.svg" alt="CodeActor Agent 架构图" width="900">
</p>

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

## Codebase 分析引擎

`codeactor-codebase` 是一个独立的 **Rust** 服务，提供深度代码分析能力。它作为后台 HTTP 服务器运行，由 Go 二进制自动管理。

> **以上能力已在[智能核心：Repo-Agent](#智能核心repo-agent)部分预览。** 以下是实现细节。

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
[global.llm]
use_provider = "siliconflow"

[global.llm.providers.siliconflow]
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
- [Browser_Agent_Design.md](docs/Browser_Agent_Design.md) — 浏览器自动化架构与实现

## 社区与贡献

欢迎任何形式的参与——Bug 报告、功能建议、文档改进以及代码贡献。无论你是经验丰富的 Go/Rust 开发者，还是刚刚起步的新手，CodeActor 社区都有你的位置。

**参与其中：**
- 通过 [Issue](https://github.com/your-org/codeactor-agent/issues) 报告 Bug 或提出功能需求
- 通过 [Pull Request](https://github.com/your-org/codeactor-agent/pulls) 提交你的改进
- 在 [Discussions](https://github.com/your-org/codeactor-agent/discussions) 中参与交流讨论

## 许可证

[Apache License 2.0](LICENSE)
