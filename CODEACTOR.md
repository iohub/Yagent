# CodeActor Agent 开发知识库

> **本文件是 Agent 理解仓库和进行开发的标准指南。** 所有 Agent 在操作此仓库前必须先阅读本文件。

---

## 目录

- [1. 项目概述](#1-项目概述)
- [2. 技术栈](#2-技术栈)
- [3. Agent 角色定义](#3-agent-角色定义)
- [4. 核心工具](#4-核心工具)
- [5. 系统架构](#5-系统架构)
- [6. 开发与构建](#6-开发与构建)
- [7. 编码规范](#7-编码规范)
- [8. Conductor 行为准则](#8-conductor-行为准则)

---

## 1. 项目概述

**CodeActor Agent** 是一个基于 Go 语言开发的多智能体 AI 编程助手系统，采用 **Hub-and-Spoke（中枢-辐条）** 架构。系统的核心是 **Conductor Agent（指挥家）**，它协调多个专用子智能体完成代码分析、规划、编写、测试和自我修正等复杂任务。

### 1.1 核心定位

| 维度 | 描述 |
|------|------|
| **系统类型** | 多智能体 AI 编程助手 |
| **架构模式** | Hub-and-Spoke（中枢-辐条） |
| **核心原则** | 职责分离、强制委派、验证优先 |
| **用户接口** | TUI (Bubble Tea) / HTTP API (WebSocket) / CLI |

### 1.2 架构图

```
                    用户交互层
                TUI / HTTP / WebSocket
                        │
              CodingAssistant (任务调度)
                        │
              ConductorAgent (中枢指挥家)
              ┌───────────┼───────────┬───────────┐
              │           │           │           │
        delegate_repo delegate_coding delegate_chat delegate_meta
              │           │           │           │
         RepoAgent   CodingAgent   ChatAgent   MetaAgent → 注册自定义Agent
```

### 1.3 设计原则

1. **单一交互点**: Conductor 是唯一与用户直接交互的 Agent
2. **职责分离**: 每个子 Agent 有明确的职责边界和权限限制
3. **强制委派**: 文件操作和代码修改必须通过专用 Agent 完成
4. **验证优先**: 所有修改必须经过编译/测试验证
5. **可追溯性**: 所有任务 Memory 持久化存储，支持恢复和审计

---

## 2. 技术栈

### 2.1 主要技术

| 类别 | 技术 | 版本 | 用途 |
|------|------|------|------|
| **主程序** | Go | 1.23+ | 核心实现语言，模块名 `codeactor` |
| **代码引擎** | Rust | 1.70+ (Tokio) | `codeactor-codebase` 服务，运行在 `127.0.0.1:12800` |
| **LLM 抽象层** | `github.com/tmc/langchaingo` | - | 支持多 LLM 提供商的统一接口 |
| **HTTP 框架** | `gin-gonic/gin` | - | REST API 服务器 |
| **WebSocket** | `olahol/melody` | - | WebSocket 连接管理 |
| **TUI** | Charmbracelet/Bubble Tea | - | 终端交互界面 |

### 2.2 外部依赖

| 依赖 | 用途 |
|------|------|
| `codeactor-codebase` (Rust) | 代码分析服务（语义搜索、骨架提取、依赖分析） |
| `ripgrep` (rg) | 全文正则搜索 |
| `fzf` | 模糊文件搜索 |

### 2.3 支持的 LLM 提供商

小米 MiMo、阿里云百炼、硅基流动、DeepSeek、Moonshot、Mistral、智谱 Z.ai、AWS Bedrock、OpenRouter 等。

---

## 3. Agent 角色定义

### 3.1 Conductor (指挥家)

**定位**: 项目经理 + 技术主管，**唯一与用户直接交互的 Agent**。

**核心职责**:

| 职责 | 说明 |
|------|------|
| 任务分析 | 解析用户需求，拆解为可执行的 TODO 列表 |
| 上下文管理 | 维护全局状态（文件修改、任务进度等） |
| 任务委派 | 根据任务类型委派给合适的子 Agent |
| 结果评审 | 验证子 Agent 的输出，决定是否接受或修正 |

**拥有工具**:

| 工具 | 类别 | 说明 |
|------|------|------|
| `delegate_repo` | 委派 | 委派仓库分析任务 |
| `delegate_coding` | 委派 | 委派编码/修改任务 |
| `delegate_chat` | 委派 | 委派通用问答 |
| `delegate_meta` | 委派 | 设计自定义 Agent |
| `delegate_devops` | 委派 | 委派运维/系统任务 |
| `read_file` | 分析 | 读取文件（只读） |
| `search_by_regex` | 分析 | ripgrep 正则搜索（只读） |
| `list_dir` | 分析 | 列出目录内容（只读） |
| `print_dir_tree` | 分析 | 打印目录树（只读） |
| `agent_exit` | 流程 | 通知任务完成 |

### 3.2 Coding (编码)

**定位**: 高级开发工程师，拥有文件系统完整读写权限。

**核心职责**:

| 职责 | 说明 |
|------|------|
| 代码编写 | 创建新文件、编写功能代码 |
| 代码修改 | 精确替换现有代码块 |
| 测试验证 | 运行编译和测试确认修改正确 |
| 错误修复 | 调试和修复编译/运行时错误 |

**拥有工具** (14 个全部):

| 工具 | 类别 | 说明 |
|------|------|------|
| `read_file` | 文件 | 读取文件（支持行范围） |
| `create_file` | 文件 | 创建新文件 |
| `delete_file` | 文件 | 批量删除文件/目录 |
| `rename_file` | 文件 | 重命名/移动文件 |
| `list_dir` | 文件 | 列出目录内容 |
| `print_dir_tree` | 文件 | 打印目录树 |
| `search_replace_in_file` | 编辑 | 精准代码块替换 |
| `search_by_regex` | 搜索 | ripgrep 全文正则搜索 |
| `semantic_search` | 仓库 | 语义搜索（调用 codebase 服务） |
| `query_code_skeleton` | 仓库 | 查询代码骨架 |
| `query_code_snippet` | 仓库 | 查询函数实现 |
| `run_bash` | 系统 | 执行 Shell 命令 |
| `thinking` | 认知 | 错误分析和反思思维链 |
| `agent_exit` | 流程 | 通知任务完成 |

### 3.3 Repo (分析)

**定位**: 资深架构师/代码审计员，**只读模式**。

**核心职责**:

| 职责 | 说明 |
|------|------|
| 代码检索 | 语义搜索和结构化代码查询 |
| 依赖分析 | 评估影响范围和依赖关系 |
| 预调查 | 每次 Run 之前自动调用 `POST /investigate_repo` 获取仓库全景 |
| 上下文提供 | 为 Coding Agent 提供必要的代码上下文 |

**拥有工具** (7 个):

| 工具 | 类别 | 说明 |
|------|------|------|
| `read_file` | 文件 | 读取文件（只读） |
| `search_by_regex` | 搜索 | ripgrep 全文正则搜索 |
| `list_dir` | 文件 | 列出目录内容 |
| `print_dir_tree` | 文件 | 打印目录树 |
| `semantic_search` | 仓库 | 语义搜索（调用 codebase 服务） |
| `query_code_skeleton` | 仓库 | 查询代码骨架 |
| `query_code_snippet` | 仓库 | 查询函数实现 |

### 3.4 Chat (对话)

**定位**: 通用 AI 助手。

**核心职责**:

| 职责 | 说明 |
|------|------|
| 技术解释 | 解释代码逻辑、架构设计 |
| 百科问答 | 通用技术知识问答 |
| 创意写作 | 文档编写、文案创作 |

**限制**: 无法访问文件系统，无工具调用权限。

### 3.5 DevOps (运维)

**定位**: 系统管理员。

**核心职责**:

| 职责 | 说明 |
|------|------|
| Shell 执行 | 运行系统命令、构建脚本 |
| 日志检查 | 查看和分析系统/应用日志 |
| 进程管理 | 检查和管理后台进程 |
| 系统诊断 | 环境检查和故障排查 |

**限制**: 只读文件检查和 Shell 执行，不能修改/创建文件。

### 3.6 Meta (设计)

**定位**: 自定义 Agent 设计器。

**核心职责**:

| 职责 | 说明 |
|------|------|
| Agent 设计 | 使用高级 Prompt 工程技术设计定制 Agent |
| 工具选择 | 选择最小必要工具集 |
| 任务执行 | 执行设计并返回结构化结果 |
| 自动注册 | 将新 Agent 注册为永久 Delegate 工具 |

**动态注册流程**:

```
Conductor 调用 delegate_meta(task)
  ↓
MetaAgent.Run(task) - LLM 生成 JSON 输出
  ↓
parseMetaAgentOutput() - 提取 JSON，验证必填字段
  ↓
registerCustomAgent() - 创建 delegate_<name> 工具
  ↓
立即执行新 Agent（最多 15 步）
  ↓
返回格式化结果，新 Agent 永久可用
```

---

## 4. 核心工具

### 4.1 工具总览

| 工具名 | 类别 | 权限 | 说明 |
|--------|------|------|------|
| `read_file` | 文件 | R | 读取文件（支持行范围） |
| `create_file` | 文件 | W | 创建新文件 |
| `delete_file` | 文件 | D | 批量删除文件/目录 |
| `rename_file` | 文件 | M | 重命名/移动文件 |
| `list_dir` | 文件 | R | 列出目录内容 |
| `print_dir_tree` | 文件 | R | 打印目录树 |
| `search_replace_in_file` | 编辑 | E | 精准代码块替换 |
| `search_by_regex` | 搜索 | R | ripgrep 全文正则搜索 |
| `semantic_search` | 仓库 | R | 语义搜索（调用 codebase 服务） |
| `query_code_skeleton` | 仓库 | R | 查询代码骨架 |
| `query_code_snippet` | 仓库 | R | 查询函数实现 |
| `run_bash` | 系统 | S | 执行 Shell 命令 |
| `thinking` | 认知 | C | 错误分析和反思思维链 |
| `agent_exit` | 流程 | P | 通知任务完成 |

**图例**: R=只读, W=写入, D=删除, M=移动/重命名, E=编辑, S=系统, C=认知, P=流程

### 4.2 文件操作工具

#### read_file

```go
// 读取文件内容，支持指定行范围
read_file(
    target_file: string,   // 文件路径（绝对或相对）
    start_line: int,       // 起始行（1-indexed）
    end_line: int,         // 结束行（1-indexed）
    should_read_entire_file: bool
)
```

#### create_file

```go
// 创建新文件
create_file(
    target_file: string,   // 文件路径
    content: string        // 文件内容
)
```

#### search_replace_in_file

```go
// 精准替换文件中的代码块
search_replace_in_file(
    file_path: string,     // 文件路径
    old_string: string,    // 原文（需包含3-5行上下文，唯一匹配）
    new_string: string     // 替换文
)
```

**约束**: `old_string` 必须唯一标识要替换的实例，需包含充分的上下文。

### 4.3 搜索工具

#### search_by_regex

```go
// ripgrep 全文正则搜索
search_by_regex(
    query: string,         // 正则表达式
    search_directory: string  // 搜索目录
)
```

#### semantic_search

```go
// 调用 codebase Rust 服务进行语义搜索
semantic_search(
    query: string,         // 自然语言或代码片段
    limit: int             // 返回结果数量上限
)
```

### 4.4 仓库查询工具

#### query_code_skeleton

```go
// 获取文件的方法/类定义（无实现）
query_code_skeleton(
    filepaths: []string    // 文件路径列表
)
```

#### query_code_snippet

```go
// 获取特定函数的实现代码
query_code_snippet(
    filepath: string,      // 文件路径
    function_name: string  // 函数名
)
```

### 4.5 系统工具

#### run_bash

```go
// 执行 Shell 命令
run_bash(
    command: string,       // 命令字符串
    is_background: bool,   // 是否后台运行
    is_dangerous: bool     // 是否危险操作
)
```

**约束**: 禁止启动长期运行的服务器进程，使用编译/测试验证代码。

#### thinking

```go
// 反思和错误分析
thinking(
    problem_description: string,  // 问题描述
    current_action: string,       // 当前操作
    observation: string           // 观察结果
)
```

### 4.6 工具适配器模式

所有工具通过 `Adapter` 模式包装为 langchaingo 的 `Tool` 接口：

```go
type ToolFunc func(ctx context.Context, params map[string]interface{}) (interface{}, error)

type Adapter struct {
    name        string
    description string
    fn          ToolFunc
    schema      map[string]interface{}
}
```

---

## 5. 系统架构

### 5.1 Hub-and-Spoke 消息流

```
                    用户交互层
                TUI / HTTP / WebSocket
                        │
              CodingAssistant (任务调度)
                        │
              ConductorAgent (中枢指挥家)
              ┌───────────┼───────────┬───────────┐
              │           │           │           │
        delegate_repo delegate_coding delegate_chat delegate_meta
              │           │           │           │
         RepoAgent   CodingAgent   ChatAgent   MetaAgent → 注册自定义Agent
```

### 5.2 Pub-Sub 消息总线

```
Agent → MessagePublisher → MessageDispatcher → TUIConsumer / WebSocketConsumer
```

**事件类型**:

| 事件 | 说明 |
|------|------|
| `ai_response` | AI 文本回复 |
| `tool_call_start` | 工具调用开始 |
| `tool_call_result` | 工具调用结果 |
| `user_help_needed` | 需要用户帮助 |
| `user_help_response` | 用户帮助回复 |

### 5.3 任务执行流程

```
1. 用户输入 (TUI 或 HTTP POST /api/start_task)
2. TaskManager.CreateTask() - 生成 UUID，创建 Memory
3. ExecuteTask() - 初始化 codebase 索引，启动消息分发
4. ConductorAgent.Run() - 进入循环
5. Conductor 循环 (最多 maxSteps 步):
   ├── 构造 messages: [SystemPrompt, ...Memory.Messages]
   ├── LLM.GenerateContent(messages, WithTools)
   ├── 发布 ai_response 事件
   ├── 如果有 ToolCall → 执行对应 Adapter
   │   ├── delegate_repo → RepoAgent.Run()（含预调查）
   │   ├── delegate_coding → CodingAgent.Run()
   │   ├── delegate_meta → MetaAgent 设计并注册新 Agent
   │   └── agent_exit → 返回 "Task completed"
   └── 将 ToolCallResponse 追加到 messages
6. 任务完成 → 保存 Memory → 广播状态 → 关闭分发器
```

### 5.4 模块结构

```
codeactor-agent/
├── main.go                    # 入口
├── internal/
│   ├── agents/                # Agent 实现
│   │   ├── conductor/         # Conductor Agent
│   │   ├── coding/            # Coding Agent
│   │   ├── repo/              # Repo Agent
│   │   ├── chat/              # Chat Agent
│   │   ├── devops/            # DevOps Agent
│   │   └── meta/              # Meta Agent
│   ├── app/                   # 应用入口
│   ├── config/                # 配置加载
│   ├── datamanager/           # 数据存储
│   ├── diff/                  # 差异计算
│   ├── embedbin/              # 嵌入二进制
│   ├── globalctx/             # 全局上下文
│   ├── http/                  # HTTP API
│   ├── llm/                   # LLM 抽象层
│   ├── memory/                # 任务记忆
│   ├── tools/                 # 工具适配器
│   ├── tui/                   # 终端界面
│   └── util/                  # 工具函数
├── pkg/messaging/             # 消息总线
├── codebase/                  # Rust 代码引擎
├── clients/                   # 客户端
│   └── nodejs-cli/            # Node.js CLI
├── config/                    # 配置文件
├── docs/                      # 文档
└── benchmark/                 # 基准测试
```

---

## 6. 开发与构建

### 6.1 构建命令

```bash
# 构建主程序
go build -o codeactor .

# 构建 Rust codebase 服务
cd codebase && cargo build --release
```

### 6.2 运行模式

```bash
# TUI 模式
./codeactor tui [--taskfile TASK.md] [--disable-agents=repo,coding,chat,meta]

# HTTP 服务器模式
./codeactor http [--disable-agents=repo,coding,chat,meta]
```

### 6.3 CLI 客户端（Node.js）

```bash
cd clients/nodejs-cli && npm install

# 创建任务
node index.js run <project-dir> "task description"

# 继续对话
node index.js chat <task-id> <project-dir>

# 查询状态
node index.js status <task-id>

# 查看记忆
node index.js memory <task-id>
```

### 6.4 测试方法

```bash
# 单元测试（mock LLM）
go test ./internal/... -v -count=1

# 特定测试
go test ./internal/agents/... -v -run TestDelegateMeta_DynamicRegistration
```

### 6.5 配置系统

**配置文件路径**: `$HOME/.codeactor/config/config.toml` → `config/config.toml`（降级）

**配置结构**:

```toml
[http]
server_port = 9080

[global.llm]
use_provider = "xiaomi"

[global.llm.providers.<name>]
model = "mimo-v2-flash"
temperature = 0.3
max_tokens = 28000
api_base_url = "https://..."
api_key = "your-key"

[agent]
conductor_max_steps = 30
coding_max_steps = 50
repo_max_steps = 30
lang = "Chinese"
```

### 6.6 数据存储

| 类型 | 路径 |
|------|------|
| 任务 Memory | `~/.codeactor/tasks/{taskID}.json` |
| LLM 日志 | `~/.codeactor/logs/llm-{date}.log` |
| Codebase 日志 | `~/.codeactor/logs/codeactor-codebase/{date}.log` |

---

## 7. 编码规范

### 7.1 导入路径

```go
import (
    "codeactor/internal/..."
    "codeactor/pkg/..."
)
```

### 7.2 Agent 接口

```go
type Agent interface {
    Name() string
    Run(ctx context.Context, input string) (string, error)
}
```

### 7.3 错误处理

- 必须清晰传递错误栈，不要吞掉错误
- 使用 `fmt.Errorf("context: %w", err)` 包装错误
- 关键操作失败时返回有意义的错误信息

### 7.4 并发规范

- 使用 goroutine 和 channel 进行并发操作
- 注意 mutex 锁的粒度，避免死锁
- 长时间运行的操作必须支持 context 取消

### 7.5 代码风格

- 遵循 Go 官方 `gofmt` 和 `golint` 规范
- 函数命名使用驼峰命名法（camelCase）
- 常量使用全大写下划线分隔（UPPER_SNAKE_CASE）
- 接口命名遵循 Go 惯例（单方法接口加 `-er` 后缀）

---

## 8. Conductor 行为准则

> **以下准则为 Conductor Agent 的强制约束，所有 Agent 在执行任务时必须遵守。**

### 8.1 核心原则

| 原则 | 说明 |
|------|------|
| **禁止幻觉** | 只依赖 Repo-Agent 提供的信息，不编造文件名或代码 |
| **编码分离** | 不直接输出代码块，必须委派给 Coding-Agent |
| **逐步执行** | 一次只委派一个子任务，验证后再继续 |
| **强制委派** | 仓库探索必须通过 `delegate_repo`，不可直接操作文件 |
| **思考先于行动** | 必须在 `Thought Process` 块中分析后再执行工具调用 |

### 8.2 行为约束

1. **禁止幻觉**
   - 只依赖 Repo-Agent 提供的信息
   - 不编造文件名、函数名或代码内容
   - 不确定时先委派 Repo-Agent 查询

2. **编码分离**
   - 不直接输出代码块供用户复制
   - 所有代码修改必须通过 `delegate_coding` 完成
   - 代码评审应关注逻辑正确性而非复制粘贴

3. **逐步执行**
   - 一次只委派一个子任务
   - 验证子 Agent 输出后再继续下一步
   - 复杂任务分解为可独立验证的小步骤

4. **禁止长进程**
   - 不启动开发服务器
   - 使用 `go build` 或测试验证代码
   - 避免长时间阻塞的操作

5. **并行执行**
   - 要求子 Agent 对只读操作使用并行工具调用
   - 独立任务可同时委派（如多个只读分析）

6. **思考先于行动**
   - 在每次工具调用前包含 `Thought Process` 块
   - 分析问题根因再选择工具
   - 记录决策理由和预期结果

### 8.3 Thought Process 模板

```
Thought Process:
1. 当前状态: [描述当前任务进度和上下文]
2. 问题分析: [分析用户请求或当前遇到的问题]
3. 决策理由: [为什么选择这个 Agent 或工具]
4. 预期结果: [期望得到什么输出]
```

---

## 附录

### A. 文件索引

| 文件/目录 | 说明 |
|-----------|------|
| `main.go` | 程序入口 |
| `internal/agents/` | 所有 Agent 实现 |
| `internal/tools/` | 工具适配器 |
| `internal/memory/` | 任务记忆管理 |
| `pkg/messaging/` | Pub-Sub 消息总线 |
| `codebase/` | Rust 代码引擎 |
| `clients/nodejs-cli/` | Node.js CLI 客户端 |
| `config/config.toml` | 示例配置文件 |
| `docs/` | 补充文档 |

### B. 快速参考

```bash
# 常用命令速查
go build -o codeactor .          # 构建
./codeactor tui                  # TUI 模式
./codeactor http                 # HTTP 模式
go test ./internal/... -v -count=1  # 测试
cd codebase && cargo build       # Rust 构建
```

---

*本文档由 CodeActor Agent 系统自动生成并维护，最后更新: 2025*
