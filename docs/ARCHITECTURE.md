# CodeActor Agent 系统架构文档

## 目录

1. [项目概览](#1-项目概览)
2. [代码文件结构与功能](#2-代码文件结构与功能)
3. [系统架构与模块](#3-系统架构与模块)
4. [消息通讯机制](#4-消息通讯机制)
5. [接口协议](#5-接口协议)
6. [数据流与工作流](#6-数据流与工作流)
7. [配置系统](#7-配置系统)
8. [外部依赖服务](#8-外部依赖服务)

---

## 1. 项目概览

**CodeActor Agent** 是一个基于 Go 语言开发的多智能体 AI 编程助手。采用 **Hub-and-Spoke（中枢-辐条）** 架构，通过一个核心的 **Conductor Agent（指挥家）** 协调多个专用子智能体完成复杂的代码分析、规划、编写、测试和自我修正任务。

### 核心依赖

| 依赖 | 用途 |
|------|------|
| `github.com/tmc/langchaingo` | LLM 抽象层，提供统一的模型调用接口 |
| `github.com/gin-gonic/gin` | HTTP 服务器框架 |
| `github.com/olahol/melody` | WebSocket 连接管理 |

### 运行模式

- **TUI 模式** (`codeactor tui`): 终端交互界面，输入任务描述后执行
- **HTTP 模式** (`codeactor http`): 启动 REST API + WebSocket 服务器，供 Web 前端或 IDE 插件集成

---

## 2. 代码文件结构与功能

```
codeactor-agent/
├── main.go                          # 程序入口，解析命令行参数，启动 TUI 或 HTTP 模式
├── tui.go                           # TUI 实现：Bubble Tea 界面模型、输入处理、渲染
├── i18n.go                          # 国际化：中/英文翻译字符串和 LanguageManager
├── go.mod / go.sum                  # Go 模块依赖管理
├── config/
│   └── config.toml                  # 应用配置：LLM 提供商、HTTP 端口、Agent 参数
├── docs/
│   ├── Agent_Design.md              # Agent 系统设计文档（构思阶段）
│   ├── Agent_Reference.md           # API 参考文档
│   └── Prompt_Best_Guides.md        # Prompt 工程最佳实践
├── internal/
│   ├── config/
│   │   └── config.go                # TOML 配置解析、ProviderConfig 结构定义、多 LLM 提供商支持
│   ├── globalctx/
│   │   └── global_context.go        # 全局上下文：项目路径、OS/Arch、语言、Prompt 格式化、工具引用
│   ├── assistant/
│   │   ├── assistant.go             # CodingAssistant 核心：初始化 Agent、任务处理入口
│   │   ├── llm.go                   # LLM 客户端：多提供商支持、流式输出、Bedrock 集成、日志记录
│   │   ├── integration.go           # 消息集成：MessagePublisher 封装
│   │   ├── data_manager.go          # 数据持久化：任务 Memory 的保存/加载/历史列表
│   │   ├── user_response.go         # 用户响应通道：等待和处理用户回复
│   │   ├── agents/                  # 智能体实现
│   │   │   ├── conductor.go         # ConductorAgent：任务调度中枢，委派子 Agent
│   │   │   ├── coding.go            # CodingAgent：代码编辑、Shell 命令执行
│   │   │   ├── repo.go              # RepoAgent：仓库分析、预调查（pre-investigate）
│   │   │   ├── chat.go              # ChatAgent：通用对话、技术解释
│   │   │   ├── types.go             # Agent 接口定义（Agent, BaseAgent）
│   │   │   ├── tools.go             # 嵌入 tools.json 工具定义
│   │   │   ├── conductor.prompt.md  # Conductor 系统提示词
│   │   │   ├── coding.prompt.md     # Coding-Agent 系统提示词
│   │   │   ├── repo.prompt.md       # Repo-Agent 系统提示词
│   │   │   ├── chat.prompt.md       # Chat-Agent 系统提示词
│   │   │   └── tools.json           # 14 个工具定义的 JSON Schema
│   │   │   └── conductor_test.go    # Conductor 消息转换测试
│   │   ├── tools/                   # 工具实现
│   │   │   ├── adapter.go           # Adapter 模式：将 ToolFunc 包装为 langchaingo Tool 接口
│   │   │   ├── cognitive.go         # ThinkingTool：错误分析和自我反思
│   │   │   ├── file_edit.go         # ReplaceBlockTool：精准代码块替换（类似 apply_patch）
│   │   │   ├── file_operations.go   # FileOperationsTool：读写删除重命名列表目录树
│   │   │   ├── flow_control.go      # FlowControlTool：finish、ask_user_for_help
│   │   │   ├── search_operations.go # SearchOperationsTool：ripgrep 全文搜索、fzf 文件搜索
│   │   │   ├── system_operations.go # SystemOperationsTool：Shell 命令执行（前台/后台）
│   │   │   ├── repo_operations.go   # RepoOperationsTool：语义搜索/代码骨架/代码片段（调用 codebase 服务）
│   │   │   ├── file_operations_test.go
│   │   │   └── system_operations_test.go
│   ├── http/
│   │   ├── server.go                # HTTP 服务器：路由注册、REST API 处理、CORS
│   │   ├── task_manager.go          # 任务生命周期管理：创建、状态更新、取消、WebSocket 推送
│   │   ├── task_executor.go         # 任务执行引擎：启动 codebase_init、消息分发、Agent 调用
│   │   ├── websocket.go             # WebSocket 事件处理：start_task、chat_message、get_memory、clear_memory
│   │   └── types.go                 # 数据结构：Task、SocketMessage、TaskUpdate、API 请求/响应
│   ├── memory/
│   │   ├── memory.go                # ConversationMemory：对话历史管理（含 ToolCall 支持）
│   │   └── memory_test.go           # Memory 单元测试
│   └── util/
│       └── error_utils.go           # 带调用栈的错误处理、ErrorWithContext
├── pkg/
│   └── messaging/                   # 消息总线系统
│       ├── message_event.go         # MessageEvent 核心数据结构
│       ├── message_dispatcher.go    # 消息分发器：队列、消费者注册、广播
│       ├── message_publisher.go     # 消息发布者封装
│       ├── message_consumer.go      # MessageConsumer 接口定义
│       └── consumers/
│           ├── tui.go               # TUI 消费者：终端美化输出、用户交互
│           └── websock.go           # WebSocket 消费者：将事件序列化为 JSON 回调
├── logs/
│   └── server.log                   # 运行时日志（HTTP 模式）
└── codeactor                        # 编译产物（二进制）
```

### 文件依赖关系图

```
main.go
  ├── tui.go ───────────────── i18n.go
  ├── internal/config/config.go
  ├── internal/assistant/
  │     ├── assistant.go ──────── globalctx/global_context.go
  │     ├── llm.go ────────────── config (ProviderConfig, Bedrock)
  │     ├── data_manager.go ──── memory (持久化)
  │     ├── integration.go ───── pkg/messaging
  │     └── agents/
  │           ├── conductor.go ── tools (Adapter), memory, globalctx
  │           ├── coding.go ───── tools (Adapter), tools.json
  │           ├── repo.go ─────── tools (codebase HTTP API), globalctx
  │           └── chat.go ─────── globalctx
  ├── internal/http/
  │     ├── server.go ─────────── gin, melody (WebSocket), assistant
  │     ├── task_manager.go ──── melody
  │     ├── task_executor.go ─── assistant, messaging, consumers
  │     ├── websocket.go ─────── melody, messaging
  │     └── types.go ─────────── memory
  └── internal/util/error_utils.go
```

---

## 3. 系统架构与模块

### 3.1 整体架构：Hub-and-Spoke 模式

```
                          ┌──────────────────────────────────────┐
                          │           用户交互层                   │
                          │  ┌──────────┐   ┌──────────────┐     │
                          │  │   TUI    │   │ HTTP/WebSocket│    │
                          │  │(bubbletea)│  │  (gin+melody) │    │
                          │  └────┬─────┘   └──────┬───────┘     │
                          └───────┼──────────────────┼───────────┘
                                  │                  │
                          ┌───────┼──────────────────┼───────────┐
                          │       ▼                  ▼            │
                          │  ┌──────────────────────────────┐    │
                          │  │     CodingAssistant          │    │
                          │  │  (任务调度 + Agent 初始化)     │    │
                          │  └─────────────┬────────────────┘    │
                          │                │                      │
                          │                ▼                      │
                          │  ┌──────────────────────────────┐    │
                          │  │     ConductorAgent            │    │
                          │  │     (中枢 / 指挥家)            │    │
                          │  │                               │    │
                          │  │  ┌─────────┐ ┌───────────┐  │    │
                          │  │  │delegate  │ │delegate   │  │    │
                          │  │  │  repo    │ │  coding   │  │    │
                          │  │  └────┬─────┘ └─────┬─────┘  │    │
                          │  └───────┼───────────────┼───────┘    │
                          │          │               │            │
                          │          ▼               ▼            │
                          │  ┌──────────┐   ┌──────────────┐     │
                          │  │RepoAgent │   │ CodingAgent  │     │
                          │  │ (只读分析) │  │ (代码编辑)    │     │  Agent 层
                          │  └──────────┘   └──────────────┘     │
                          │                                      │
                          │  ┌──────────────────────────────┐    │
                          │  │      ChatAgent (通用对话)      │    │
                          │  └──────────────────────────────┘    │
                          └──────────────────────────────────────┘
                                  │                  │
                          ┌───────┼──────────────────┼───────────┐
                          │       ▼                  ▼            │
                          │  ┌──────────────────────────────┐    │
                          │  │         工具层 (Tools)         │    │
                          │  │                               │    │
                          │  │  FileOps │ SearchOps │ SysOps │    │
                          │  │  ReplaceBlock │ Thinking │    │    │
                          │  │  RepoOps │ FlowControl        │    │  Tool 层
                          │  │       │       │       │       │    │
                          │  │   ripgrep  fzf   bash  HTTP   │    │
                          │  │                               │    │
                          │  └───────────────────────────────┘    │
                          └──────────────────────────────────────┘
                                  │
                          ┌───────┼───────────────────────────────┐
                          │       ▼                                │
                          │  ┌──────────────────────────────┐    │
                          │  │  codeactor-codebase (Rust)    │    │
                          │  │  外部代码分析服务 (127.0.0.1:12800)│   │  外部服务
                          │  │  - semantic_search             │    │
                          │  │  - investigate_repo            │    │
                          │  │  - query_code_skeleton         │    │
                          │  │  - query_code_snippet          │    │
                          │  │  - codebase_init               │    │
                          │  └──────────────────────────────┘    │
                          └──────────────────────────────────────┘
                                  │
                          ┌───────┼───────────────────────────────┐
                          │       ▼                                │
                          │  ┌──────────────────────────────┐    │
                          │  │  多种 LLM 提供商               │    │
                          │  │  小米MiMo / 阿里Qwen / 硅基流动 │    │   LLM 层
                          │  │  DeepSeek / Mistral / Bedrock  │    │
                          │  │  通过 OpenAI 兼容 API 调用      │    │
                          │  └──────────────────────────────┘    │
                          └──────────────────────────────────────┘
```

### 3.2 核心模块详解

#### 3.2.1 Agent 层 (`internal/assistant/agents/`)

**Agent 接口** (`types.go`):
```go
type Agent interface {
    Name() string
    Run(ctx context.Context, input string) (string, error)
}
```

##### ConductorAgent（指挥家）— `conductor.go`

- **角色**: 项目经理 + 技术主管，唯一与用户直接交互的 Agent
- **核心循环**: `Delegate Repo → Analyze → Plan → Delegate Coding → Review → Iterate`
- **拥有的工具**:
  - `delegate_repo`: 委派仓库分析任务给 RepoAgent
  - `delegate_coding`: 委派编码任务给 CodingAgent（自动附加上下文 `RepoSummary`）
  - `delegate_chat`: 委派对话任务给 ChatAgent
  - `finish`: 表示任务完成
  - `search_by_regex`, `read_file`, `list_dir`, `print_dir_tree`: 只读分析工具
- **关键特性**:
  - 维护 `RepoSummary`：当 RepoAgent 返回分析结果时，自动存储到 `GlobalCtx`
  - 支持 `ConversationMemory`：保存多轮对话上下文
  - 最大步数限制（默认 20 步，可配置）
  - 每步发布实时消息事件（`ai_response`、`tool_call_start`、`tool_call_result`）

##### CodingAgent（编码工程师）— `coding.go`

- **角色**: 高级开发工程师，拥有文件系统读写权限
- **拥有的工具**（14 个中的 12 个）:
  - 文件操作: `read_file`, `create_file`, `delete_file`, `rename_file`, `list_dir`, `print_dir_tree`
  - 代码编辑: `search_replace_in_file`
  - 搜索: `search_by_regex`, `semantic_search`, `query_code_skeleton`, `query_code_snippet`
  - 系统: `run_terminal_cmd`
  - 反思: `thinking`
- **工作流程**: Analyze → Explore → Plan → Implement → Verify
- **最大步数限制**（默认 30 步，可配置）
- **不保持对话记忆**（每次 Run 都是独立的新对话）

##### RepoAgent（仓库分析员）— `repo.go`

- **角色**: 资深架构师/代码审计员，**只读模式**
- **拥有的工具**（7 个）:
  - `read_file`, `search_by_regex`, `list_dir`, `print_dir_tree`
  - `semantic_search`, `query_code_skeleton`, `query_code_snippet`
- **预调查机制** (`doPreInvestigate`):
  - 在每次 Run 之前，自动调用 `POST /investigate_repo` 到 codebase 服务
  - 获取目录树、核心函数列表（含调用者/被调者依赖图）、文件骨架
  - 将调查结果注入系统提示词
- **最大步数限制**（默认 20 步，可配置）

##### ChatAgent（对话者）— `chat.go`

- **角色**: 通用 AI 助手，无法访问文件系统和修改代码
- **适用场景**: 技术解释、百科问答、生活常识、创意写作
- **无工具调用**: 直接生成回复
- **固定 temperature**: 0.7（更富创造力）

#### 3.2.2 工具层 (`internal/assistant/tools/`)

**Adapter 模式** (`adapter.go`): 将普通的 `ToolFunc` 函数包装为 `langchaingo` 的 `llms.Tool` 接口，使得 LLM 可以通过 Function Calling 调用这些工具。

```go
type ToolFunc func(ctx context.Context, params map[string]interface{}) (interface{}, error)

// Adapter 实现了 langchaingo 的 Tool 接口
type Adapter struct {
    name        string
    description string
    fn          ToolFunc
    schema      map[string]interface{}  // JSON Schema 参数定义
}
```

**工具清单**（14 个工具，定义在 `tools.json`）:

| 工具名 | 类别 | 实现文件 | 描述 |
|--------|------|---------|------|
| `read_file` | 文件 | `file_operations.go` | 读取文件（支持行范围和全文） |
| `create_file` | 文件 | `file_operations.go` | 创建新文件 |
| `delete_file` | 文件 | `file_operations.go` | 批量删除文件/目录 |
| `rename_file` | 文件 | `file_operations.go` | 重命名/移动文件 |
| `list_dir` | 文件 | `file_operations.go` | 列出目录内容（支持最大深度） |
| `print_dir_tree` | 文件 | `file_operations.go` | 打印目录树结构 |
| `search_replace_in_file` | 编辑 | `file_edit.go` | 精准代码块替换（old_string → new_string） |
| `search_by_regex` | 搜索 | `search_operations.go` | ripgrep 正则全文搜索 |
| `run_terminal_cmd` | 系统 | `system_operations.go` | 执行 Shell 命令（前台/后台） |
| `thinking` | 认知 | `cognitive.go` | 错误分析和反思思维链 |
| `semantic_search` | 仓库 | `repo_operations.go` | 语义搜索（调用 codebase 服务） |
| `query_code_skeleton` | 仓库 | `repo_operations.go` | 查询代码骨架（函数/类定义） |
| `query_code_snippet` | 仓库 | `repo_operations.go` | 查询代码片段（函数实现） |
| `finish` | 流程 | `flow_control.go` | 通知 Conductor 任务完成 |

**工具分配策略**:

| Agent | 可用工具 | 数量 |
|-------|---------|------|
| Conductor | `delegate_*` + `finish` + 4 只读工具 | 8 |
| CodingAgent | 全部 14 个工具 | 14 |
| RepoAgent | 7 只读/搜索工具 | 7 |
| ChatAgent | 无工具 | 0 |

#### 3.2.3 HTTP 服务层 (`internal/http/`)

- **框架**: Gin (HTTP) + Melody (WebSocket)
- **路由表**:

| 方法 | 路径 | 处理函数 | 说明 |
|------|------|---------|------|
| `GET` | `/ws` | melody.HandleRequest | WebSocket 连接升级 |
| `POST` | `/api/start_task` | `handleStartTask` | 启动/恢复编码任务 |
| `GET` | `/api/task_status` | `handleTaskStatus` | 查询任务状态（含 Memory） |
| `POST` | `/api/cancel_task` | `handleCancelTask` | 取消任务 |
| `GET` | `/api/history` | `handleListHistory` | 历史任务列表 |
| `POST` | `/api/load_task` | `handleLoadTask` | 从持久化恢复任务 |
| `GET` | `/api/memory` | `handleGetMemory` | 获取全部对话记忆 |
| `GET` | `/api/memory/:type` | `handleGetMemoryByType` | 按类型获取记忆 |
| `DELETE` | `/api/memory` | `handleClearMemory` | 清空对话记忆 |

- **Task 生命周期**: `running` → `finished` / `failed` / `cancelled`

#### 3.2.4 记忆管理层 (`internal/memory/`)

- **ConversationMemory**: 对话历史管理，支持最大消息数限制
- **消息类型**: `system`、`human`、`assistant`、`tool`
- **ToolCall 支持**: 完整的 `ToolCallData` 结构（ID、Type、Function）
- **溢出策略**: 保留 System 消息，移除最早的非 System 消息
- **持久化**: 通过 `DataManager` 以 JSON 格式保存到 `~/.codeactor/tasks/{taskID}.json`

#### 3.2.5 LLM 客户端层 (`internal/assistant/llm.go`)

- **多提供商支持**: 通过 OpenAI 兼容 API 统一接口
- **Bedrock 支持**: 使用 `langchaingo/llms/bedrock` 包，支持 Claude 3.7 Sonnet、Nova、Llama 3.1
- **LoggingLLM 包装器**: 记录所有 LLM 输入/输出到日志文件 (`~/.codeactor/logs/llm-{date}.log`)
- **流式处理**: 支持 `StreamDebugHandler`，实时输出流式响应

---

## 4. 消息通讯机制

### 4.1 消息总线架构

系统使用 **发布-订阅（Pub-Sub）** 模式实现各组件间的松耦合通信。

```
┌──────────────────────────────────────────────────────────────────┐
│                        MessagePublisher                          │
│  (Agents 发布消息)                                                │
│                                                                   │
│  ConductorAgent ─── Publish("ai_response", content, "Conductor")  │
│  CodingAgent ────── Publish("tool_call_start", {...}, "Coding")   │
│  CodingAgent ────── Publish("tool_call_result", {...}, "Coding")  │
└────────────────────┬──────────────────────────────────────────────┘
                     │
                     ▼
┌──────────────────────────────────────────────────────────────────┐
│                      MessageDispatcher                           │
│  (缓冲队列: 100 → 分发循环 → 每个 Consumer 独立 channel: 1000)     │
│                                                                   │
│  1. Publish(event) → queue (chan *MessageEvent, 100)             │
│  2. start() goroutine: 从 queue 读取，dispatch 到所有 consumer    │
│  3. 每个 Consumer 有独立 goroutine 消费其 channel                  │
└────────┬──────────────────────────────────┬───────────────────────┘
         │                                  │
         ▼                                  ▼
┌─────────────────────┐          ┌─────────────────────────┐
│   TUIConsumer       │          │  WebSocketConsumer       │
│   (终端美化输出)      │          │  (JSON序列化 → callback) │
│                     │          │                          │
│  - 根据 event.Type  │          │  - json.Marshal(event)   │
│    渲染不同样式      │          │  - callback(data)        │
│  - 图标、颜色、边框  │          │  - s.Write(data)         │
│  - 支持 user_help   │          │    发送到 ws 客户端       │
│    交互输入         │          │                          │
└─────────────────────┘          └─────────────────────────┘
```

### 4.2 MessageEvent 数据结构

```go
type MessageEvent struct {
    Type      string                 `json:"type"`      // 事件类型
    From      string                 `json:"from"`      // 来源（Agent 名称）
    Content   interface{}            `json:"content"`   // 事件内容
    Timestamp time.Time              `json:"timestamp"` // 时间戳
    Metadata  map[string]interface{} `json:"metadata"`  // 元数据
}
```

### 4.3 消息事件类型

| 事件类型 | 发布者 | 内容格式 | 说明 |
|---------|-------|---------|------|
| `ai_response` | 所有 Agent | `string` (AI 文本回复) | LLM 生成的文本内容 |
| `tool_call_start` | Conductor / Coding | `map[string]interface{}` | `{tool_name, arguments, tool_call_id}` |
| `tool_call_result` | Conductor / Coding | `map[string]interface{}` | `{tool_name, result, tool_call_id}` |
| `conversation_result` | 系统 | `map[string]interface{}` | 对话轮次完成 |
| `conversation_error` | 系统 | `map[string]interface{}` | 对话处理错误 |
| `user_help_needed` | Agent | `string` (帮助请求) | Agent 请求用户协助 |
| `user_help_response` | TUI | `map[string]interface{}` | 用户对帮助请求的回复 |

### 4.4 WebSocket 消息通讯

#### 连接生命周期

```
Client                              Server
  │                                    │
  │────────── ws connect ────────────>│
  │<────── {type:"connection", ───────│
  │         event:"connected"}        │
  │                                    │
  │────────── event: "start_task" ───>│
  │<────── {type:"task_created", ─────│
  │         data:{task_id:"..."}}     │
  │                                    │
  │<────── 实时流 (type:"realtime") ───│  持续推送
  │        ai_response                 │
  │        tool_call_start             │
  │        tool_call_result            │
  │        conversation_result         │
  │                                    │
  │─────── ws disconnect ───────────>│
```

#### SocketMessage 结构

```go
type SocketMessage struct {
    Type    string      `json:"type"`    // "connection" | "task_created" | "realtime" | "chat_message" | "memory" | "error"
    Event   string      `json:"event"`   // 具体事件名
    Data    interface{} `json:"data"`    // 负载数据
    From    string      `json:"from"`    // 发送者标识
    TaskID  string      `json:"task_id"` // 关联任务
    Message string      `json:"message"` // 错误信息文本
}
```

#### 客户端命令 (Client → Server)

| Event | Data 字段 | 说明 |
|-------|----------|------|
| `start_task` | `{project_dir, task_desc}` | 创建并启动新任务 |
| `chat_message` | `{task_id, message, project_dir?}` | 发送对话消息 |
| `get_memory` | `{task_id}` | 获取任务对话记忆 |
| `clear_memory` | `{task_id}` | 清空任务对话记忆 |

---

## 5. 接口协议

### 5.1 Agent 接口

```go
type Agent interface {
    Name() string                                           // 返回 Agent 名称
    Run(ctx context.Context, input string) (string, error)  // 执行任务
}
```

### 5.2 Tool 适配器接口

```go
type ToolFunc func(ctx context.Context, params map[string]interface{}) (interface{}, error)

// Adapter 实现 langchaingo tools.Tool 接口
type Adapter struct {}
func (a *Adapter) Name() string
func (a *Adapter) Description() string
func (a *Adapter) Call(ctx context.Context, input string) (string, error)  // input 为 JSON 字符串
func (a *Adapter) ToLLMSTool() llms.Tool                                    // 转换为 langchaingo Tool
func (a *Adapter) WithSchema(schema map[string]interface{}) *Adapter        // 设置 JSON Schema
```

### 5.3 MessageConsumer 接口

```go
type MessageConsumer interface {
    Consume(event *MessageEvent) error
}
```

### 5.4 LLM 调用接口

系统通过 `langchaingo` 抽象层调用 LLM：

```go
// 核心调用方法
GenerateContent(ctx context.Context, messages []llms.MessageContent, options ...llms.CallOption) (*llms.ContentResponse, error)

// 支持的消息类型
llms.ChatMessageTypeSystem   // 系统提示
llms.ChatMessageTypeHuman    // 用户消息
llms.ChatMessageTypeAI       // AI 回复（含 ToolCalls）
llms.ChatMessageTypeTool     // 工具执行结果（含 ToolCallResponse）

// 支持的调用选项
llms.WithMaxTokens(n)
llms.WithTemperature(t)
llms.WithStreamingFunc(handler)
llms.WithTools(tools)

// Function Calling
llms.FunctionCall{Name, Arguments}
llms.ToolCallResponse{ToolCallID, Name, Content}
llms.Tool{Type: "function", Function: &llms.FunctionDefinition{...}}
```

### 5.5 Codebase 服务 API（Rust 外部服务）

运行在 `http://127.0.0.1:12800`：

| 端点 | 方法 | 调用方 | 说明 |
|------|------|--------|------|
| `/codebase_init` | POST | task_executor | 初始化代码库索引 |
| `/investigate_repo` | POST | RepoAgent | 仓库预调查 → 返回目录树+核心函数+文件骨架 |
| `/semantic_search` | POST | RepoAgent/CodingAgent | 语义搜索代码 |
| `/query_code_skeleton` | POST | RepoAgent/CodingAgent | 查询文件骨架 |
| `/query_code_snippet` | POST | RepoAgent/CodingAgent | 查询函数实现片段 |

### 5.6 HTTP REST API 协议

详见 `docs/Agent_Reference.md` 第 3.1 节。

---

## 6. 数据流与工作流

### 6.1 完整任务执行流程

```
1. 用户输入 (TUI → projectDir + taskDesc)
   或 (HTTP POST /api/start_task)
   或 (WebSocket event: start_task)
        │
2. TaskManager.CreateTask() / AddTask()
   - 生成 UUID
   - 创建 ConversationMemory(300)
   - 设置状态为 "running"
        │
3. go ExecuteTask()
   ├── 后台: POST /codebase_init (初始化代码索引)
   ├── 创建 MessageDispatcher(100)
   ├── 注册 TUIConsumer (终端输出)
   ├── 注册 WebSocketConsumer (广播到 ws 客户端)
   ├── CodingAssistant.IntegrateMessaging(dispatcher)
   └── CodingAssistant.ProcessCodingTaskWithCallback(req)
        │
4. CodingAssistant.Init() → ConductorAgent.Run()
        │
5. Conductor 循环 (最多 maxSteps 步):
   ├── 构造 messages: [SystemPrompt, ...Memory.Messages]
   ├── LLM.GenerateContent(messages, WithTools(llmTools))
   ├── 发布 ai_response 事件
   ├── 如果没有 ToolCall → 返回最终结果
   ├── 如果有 ToolCall:
   │   ├── 发布 tool_call_start
   │   ├── 执行适配器: Adapter.Call()
   │   │   ├── delegate_repo → RepoAgent.Run() (含预调查)
   │   │   ├── delegate_coding → CodingAgent.Run() (含 RepoSummary)
   │   │   └── delegate_chat → ChatAgent.Run()
   │   ├── 发布 tool_call_result
   │   ├── 如果是 delegate_repo → 更新 GlobalCtx.RepoSummary
   │   ├── 如果是 finish → 返回 "Task completed successfully"
   │   └── 将 ToolCallResponse 追加到 messages
   └── 循环继续...
        │
6. 任务完成:
   ├── TaskManager.SetTaskResult() / SetTaskError()
   ├── DataManager.SaveTaskMemory() (每个对话轮次都会保存)
   ├── 广播 TaskUpdate 到 WebSocket
   └── dispatcher.Shutdown()
```

### 6.2 多轮对话流程（HTTP 模式 chat_message）

```
Client (WebSocket) → event: chat_message → Server
  │
  1. 查找 Task（或从 DataManager 恢复）
  2. Memory.AddHumanMessage(userMessage)
  3. DataManager.SaveTaskMemory() (保存用户消息)
  4. go 处理:
     ├── 创建独立的 MessageDispatcher
     ├── CodingAssistant.ProcessConversation(req)
     │   → ConductorAgent.Run() (使用完整 Memory 上下文)
     ├── 广播 ai_response（实时流和最终回复）
     └── DataManager.SaveTaskMemory() (保存 AI 回复)
```

### 6.3 RepoAgent 预调查流程

```
RepoAgent.Run(input)
  │
  1. POST /investigate_repo (project_dir)
     → PreInvestigateResponse {
         DirectoryTree,      // 目录结构
         CoreFunctions[],    // 核心函数 + 调用图
         FileSkeletons[]     // 文件骨架
       }
  │
  2. 将调查结果格式化为 Markdown 注入 systemPrompt
  3. LLM.GenerateContent（带工具）
  4. 返回结构化分析摘要
  │
Conductor 接收到摘要 → 存储到 GlobalCtx.RepoSummary
```

---

## 7. 配置系统

### 7.1 配置文件路径

1. 优先: `$HOME/.codeactor/config/config.toml`
2. 回退: `config/config.toml`（项目本地）
3. 找不到则 panic 退出

### 7.2 配置结构

```toml
[http]
server_port = 9080                     # HTTP/WebSocket 服务端口

[llm]
use_provider = "xiaomi"                # 当前使用的 LLM 提供商

[llm.providers.<name>]
model = "mimo-v2-flash"               # 模型名称
temperature = 0.3                      # 采样温度
max_tokens = 28000                     # 最大输出 Token
api_base_url = "https://..."          # API 端点
api_key = "your-key"                   # API 密钥
# Bedrock 专用字段
aws_region = "us-east-1"
aws_profile = "default"
model_provider = "anthropic"

[app]
enable_streaming = true                # 是否启用流式输出

[agent]
conductor_max_steps = 30               # Conductor 最大循环步数
coding_max_steps = 50                  # CodingAgent 最大步数
repo_max_steps = 30                    # RepoAgent 最大步数
lang = "Chinese"                       # 输出语言
```

### 7.3 支持的 LLM 提供商

| 提供商 | 配置键 | 模型示例 |
|--------|--------|---------|
| 小米 MiMo | `xiaomi` | `mimo-v2-flash` |
| 阿里云百炼 | `aliyun` | `qwen3-coder-plus` |
| 硅基流动 | `siliconflow` | `deepseek-ai/DeepSeek-V3.2` |
| StreamLake | `streamlake` | `ep-*` |
| OpenRouter | `openrouter` | `qwen3-coder-plus` |
| DeepSeek | `deepseek` | `deepseek-ai/DeepSeek-V3` |
| Moonshot | `moonshot` | `moonshotai/Kimi-K2-Instruct` |
| Mistral | `mistral` | `mistralai/devstral-small` |
| 智谱 Z.ai | `zai` | `zai-org/GLM-4.5-Air` |
| AWS Bedrock | `bedrock` | `us.anthropic.claude-3-7-sonnet-*` |
| 本地 | `local` | 任意支持 OpenAI API 的本地模型 |

---

## 8. 外部依赖服务

### 8.1 codeactor-codebase（Rust 服务）

- **路径**: `~/.codeactor/bin/codeactor-codebase`
- **端口**: `127.0.0.1:12800`
- **启动**: `main.go::startCodebaseServer()` 在程序启动时自动以子进程方式启动
- **功能**: 代码索引、语义搜索、仓库分析、代码骨架/Snippet 查询

### 8.2 系统工具依赖

| 工具 | 用途 | 使用位置 |
|------|------|---------|
| `rg` (ripgrep) | 全文正则搜索 | `SearchOperationsTool.ExecuteGrepSearch` |
| `fzf` | 模糊文件搜索 | `SearchOperationsTool.ExecuteFileSearch` |
| `bash` | Shell 命令执行 | `SystemOperationsTool.ExecuteRunTerminalCmd` |

### 8.3 数据存储

- **任务 Memory**: `~/.codeactor/tasks/{taskID}.json`
- **LLM 日志**: `~/.codeactor/logs/llm-{YYYY-MM-DD}.log`
- **服务日志**: `./logs/server.log`（HTTP 模式）
- **Codebase 日志**: `~/.codeactor/logs/codeactor-codebase/{date}.log`
