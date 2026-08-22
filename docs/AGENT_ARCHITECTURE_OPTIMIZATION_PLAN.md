---
title: "CodeActor Agent 架构分析与优化技术方案"
version: "v1.0"
date: "2026-01"
status: "评审稿"
author: "Director 编排系统"
---

# CodeActor Agent 架构分析与优化技术方案

## 执行摘要（TL;DR）

CodeActor 当前采用「**Director 编排 + RunAgentLoop 统一执行循环 + MetaAgent 动态注册**」的多 Agent 架构：Director 负责任务编排与子 Agent 委派，`executor.RunAgentLoop` 为子 Agent 提供统一的"思考—调工具—观察"循环，MetaAgent 则支持运行时按需设计并注册专属子 Agent。本次分析识别出 **3 个高优先级问题**：① **双循环重复**——Director 内联 LLM 循环与 `RunAgentLoop` 并存，上下文压缩、指数退避重试、事件发布等关键逻辑重复实现两份，行为漂移风险高；② **director.go 过大**——单文件约 1675 行，为全仓库最大文件，编排/解析/修复/注入等多重职责耦合，维护成本高；③ **工具分发 O(n)**——每次工具调用都线性遍历 Adapters 列表查找匹配项，随 Agent 数量与工具数增长性能劣化。为此提出**三阶段优化路线图**：Phase 1 聚焦低风险速赢（魔法值收敛、局部清理），Phase 2 进行结构性重构（核心是把 Director 内联循环与 `RunAgentLoop` 统一为一套循环），Phase 3 建设并行工具执行等长期能力。

## 1. 背景与目标

本文档基于对 codeactor-agent 当前 Agent 架构与执行流程的全面分析（覆盖入口层、应用装配层、编排层、执行层、LLM 层、工具层、记忆层与支撑层的源码走读），给出分阶段、可落地的优化技术方案。方案遵循以下四条目标：

1. **消除重复逻辑**——统一 Director 内联循环与 `RunAgentLoop` 两套执行循环，消除压缩、重试、事件发布的重复实现；
2. **降低维护成本**——对超大文件（`internal/agents/director.go`，约 1675 行）进行职责化拆分；
3. **提升并发能力**——将当前单线程顺序的工具执行改造为可并行的工具调度；
4. **增强可配置性**——收敛散落在代码中的魔法值（硬编码步数、次数上限等）为具名常量或配置项。

## 2. 现状架构分析

### 2.1 技术栈

| 类别 | 技术选型 | 用途说明 |
|------|----------|----------|
| 语言/运行时 | Go 1.25 | 主架构实现语言 |
| HTTP/WebSocket | gin + melody | HTTP 服务模式与 WebSocket 实时推送 |
| 终端 UI | Bubble Tea v2 | TUI 交互模式 |
| LLM 接入 | 自研 `internal/llm` | openai-go v3、anthropic 官方 SDK；`FallbackEngine` 多 provider 兜底 |
| 浏览器自动化 | go-rod | BrowserAgent 浏览器操作能力 |
| 配置管理 | BurntSushi/toml | `config/config.toml` 加载 + hot_reload 热重载 |
| Token 计数 | tiktoken-go | 上下文压缩的 token 预算估算 |
| CLI 框架 | cobra | 命令行入口与参数解析 |
| 事件协议 | protobuf（`protocol/` 目录） | agent-events schema（`protocol/agent-events.schema.json` 等） |

> 另有内嵌子模块 **codeseek**（Rust + TypeScript），作为 MCP 代码搜索后端供 `internal/mcp` 客户端调用；它属于外部工具服务，不属于本文讨论的主架构范围。

### 2.2 分层结构与关键组件

```
┌──────────────────────────── 入口层 ────────────────────────────┐
│ main.go                                                        │
│   runTUI（Bubble Tea v2）/ runHTTP（gin+melody）/ runPrompt      │
└───────────────────────────────┬────────────────────────────────┘
                                ▼
┌──────────────────────────── 应用层 ────────────────────────────┐
│ internal/app/app.go                                            │
│   Init()：initOnce（sync.Once）保证单次初始化                    │
│   组装 GlobalCtx（FileOps/SearchOps/SysOps/CodeSeekMCP）         │
│        + 6 个子 Agent + Director + ConsolidationWorker          │
└───────────────────────────────┬────────────────────────────────┘
                                ▼
┌──────────────────────────── 编排层 ────────────────────────────┐
│ internal/agents/director.go（约 1675 行，仓库最大文件）            │
│   任务编排 / 委派强制检测 / MetaAgent 注册链 / 内联 LLM 循环        │
└───────────────────────────────┬────────────────────────────────┘
                                ▼
┌──────────────────────────── 执行层 ────────────────────────────┐
│ internal/agents/                                               │
│   executor.go（RunAgentLoop 统一循环）  types.go（Agent 接口）     │
│   meta.go（MetaAgent）                                           │
│   repo.go / coding.go / chat.go / devops.go / browser_agent.go │
│   consolidation_worker.go（异步记忆整理）                          │
│   context_compressor.go / emergency_compressor.go（两层压缩）     │
│   tool_logger.go  director_adapter.go（熔断适配器）                │
└──────────┬─────────────────────┬──────────────────┬────────────┘
           ▼                     ▼                  ▼
┌─────── LLM 层 ───────┐ ┌──── 工具层 ────┐ ┌──── 记忆层 ────────┐
│ internal/llm          │ │ internal/tools │ │ internal/memory    │
│ Engine 接口           │ │ Adapter/Regisry│ │ ConversationMemory │
│ Client 按 Agent/Tool  │ │ workspace_guard│ │ SharedMemory       │
│ 路由引擎，运行时可切换   │ │ UserConfirm    │ │ RolloutWriter      │
│ FallbackEngine 兜底    │ │ Manager        │ │                    │
└───────────────────────┘ └────────────────┘ └────────────────────┘
                                ▼
┌──────────────────────────── 支撑层 ────────────────────────────┐
│ globalctx / messaging / knowledge / mcp / config / protocol    │
└────────────────────────────────────────────────────────────────┘
```

各层要点：

- **入口层**：`main.go` 提供 `runTUI` / `runHTTP` / `runPrompt` 三种运行模式，均汇聚到应用层任务处理入口。
- **应用层**：`internal/app/app.go`（约 641 行）。`Init()` 通过 `sync.Once`（`initOnce`）保证只执行一次完整初始化：创建 `GlobalCtx`（挂载 FileOps/SearchOps/SysOps 工具组与 CodeSeekMCP 客户端）、6 个子 Agent（repo/coding/chat/meta/devops/browser）、Director 以及 ConsolidationWorker。
- **编排层**：`internal/agents/director.go`（约 1675 行，**仓库最大文件**）。承担任务编排、委派检测、自定义 Agent 注册、tool_call 修复、子 Agent 记忆回流等职责，同时内联了一套与执行层重复的 LLM 循环（详见 §2.4 与问题 P1）。
- **执行层**：`internal/agents/` 下——`executor.go`（632 行，核心函数 `RunAgentLoop`）、`types.go`（`Agent` 接口与 `BaseAgent`）、`meta.go`（MetaAgent，纯设计者角色）、五个内置子 Agent（`repo.go` / `coding.go` / `chat.go` / `devops.go` / `browser_agent.go`）、`consolidation_worker.go`（异步记忆整理 Worker）、`context_compressor.go` 与 `emergency_compressor.go`（两级上下文压缩）、`tool_logger.go`、`director_adapter.go`（熔断适配器）。各 Agent 的 system prompt 以 `*.prompt.md` 文件经 `go:embed` 打进二进制。
- **LLM 层**：`internal/llm/` 定义 `Engine` 接口 `{ GenerateContent, Model, CloseIdleConnections }`；`llm.Client` 按 Agent/Tool 名称路由引擎（如 `GetAgentEngine("director")`、`GetToolEngine("micro_agent")`），支持运行时切换；`FallbackEngine` 在主引擎失败时按配置切换到 fallback provider 兜底。
- **工具层**：`internal/tools/` 的 `Adapter{name, description, schema, fn}` 通过 `ToToolDef` 转换为 OpenAI function calling schema；`Call` 流程为：JSON 反序列化 → workspace_guard 鉴权 → 执行 → 结果序列化。`Registry` 本身是一个 map。工具有两类来源：RepoAgent 一类走 `tools.json` embed 驱动的 switch 映射（read_file、search_by_regex 等）；其余 ToolFunc 直接注入（file_edit、system_operations、search_operations、delegate、micro_agent、deepthinking）。安全层由 `WorkspaceGuard` + `UserConfirmManager` 组成，YOLO/FULL-YOLO 模式下可跳过人工确认。
- **记忆层**：`internal/memory/` 包含三类组件——`ConversationMemory`（单任务对话记忆，带 `max_size` 上限）、`SharedMemory`（跨会话持久化，位于 `~/.codeactor/data/shared_memory/{projectID}/`，约 5 秒落盘一次）、`RolloutWriter`（结构化事件流，写入 task_started / task_complete / token_count 等事件）。
- **支撑层**：`internal/globalctx`（全局上下文注入）、`internal/messaging`（Publisher → Dispatcher → consumers/tui.go + websock.go + UserConfirmManager，15+ 事件类型：ai_chunk、llm_call_start/end、thinking、tool_call_start、tool_call_result、context_compressed 等）、`internal/knowledge`（KnowledgeInjector，依赖 CodeSeekMCP）、`internal/mcp`（MCP 客户端，后台 goroutine 初始化）、`internal/config`（toml 加载 + hot_reload 热重载）。

### 2.3 核心抽象与两种 Run 签名

`internal/agents/types.go`（全文仅 27 行）定义了系统最核心的两个抽象：

```go
// AgentResult 封装 sub-agent 的完整执行结果
type AgentResult struct {
    Text   string               // 最终文本输出（给上层作为 tool_result）
    Memory []memory.ChatMessage // 完整内部对话历史
}

// Agent defines the interface for all agents in the system.
type Agent interface {
    Name() string
    Run(ctx context.Context, input string) (AgentResult, error)
}

// BaseAgent holds common dependencies for agents.
type BaseAgent struct {
    LLM       llm.Engine
    Publisher *messaging.MessagePublisher
}
```

系统中实际并存**两种 Run 签名**：

| 签名 | 使用者 | 语义 |
|------|--------|------|
| `Run(ctx, input, mem)` —— 3 参数，自持 `ConversationMemory` | `DirectorAgent.Run`（director.go） | Director 自持跨步骤会话记忆，自行管理历史拼接 |
| `Run(ctx, input)` —— 2 参数，无内部记忆 | 所有子 Agent（`Agent` 接口约定） | 无状态单轮执行，历史由调用方持有 |

两种签名语义不统一：Director 需要"带记忆的长程编排"，子 Agent 是"无状态的单轮执行器"，接口层却没有体现这一差异——这是问题 **P2-8（接口语义不一致）** 的根源，也是 Phase 2 统一抽象时要重点处理的点。

### 2.4 执行循环机制（RunAgentLoop）

`internal/agents/executor.go`（632 行）中的 `RunAgentLoop(ctx, cfg)` 是子 Agent 执行的标准循环：

```
RunAgentLoop(ctx, ExecutorConfig)
        │
        ▼
┌────────────────────────────────────────────────────┐
│ 构建初始消息：SystemPrompt + UserInput               │
└────────────────────────┬───────────────────────────┘
                         ▼
     ┌───────── for i in [0, MaxSteps) ─────────────┐
     │                                              │
     │ ① 上下文压缩                                   │
     │    TruncateToolResultsToBudget                 │
     │      （按 token 预算截断 tool 结果）              │
     │    截断后仍超限 →                               │
     │    EmergencyCompressMessages                   │
     │      （LLM 紧急摘要，保留末尾 N 条）               │
     │                                              │
     │ ② 发布事件 llm_call_start / ai_stream_start     │
     │                                              │
     │ ③ LLM.GenerateContent                          │
     │    · 带 timeout 的 context 控制单次调用超时        │
     │    · 失败按指数退避重试，等待上限 30s               │
     │                                              │
     │ ④ 发布事件 ai_stream_end / thinking /           │
     │            llm_call_end / ai_response           │
     │                                              │
     │ ⑤ 无 ToolCalls ──────────► 返回 Text，循环结束    │
     │                                              │
     │ ⑥ 有 ToolCalls ──► 遍历 Adapters 线性查找        │
     │    匹配工具并顺序执行（O(n) 分发），               │
     │    结果以 Role=Tool 消息回填历史                  │
     │                                              │
     │ ⑦ OnStepEnd hook / RolloutWriter 写入           │
     │                                              │
     └──────────────────继续下一步◄───────────────────┘
```

`ExecutorConfig` 提供四个生命周期 hook：

| Hook | 触发时机 | 说明 |
|------|----------|------|
| `OnAgentStart` | 进入循环前调用一次 | 失败则直接中止启动 |
| `OnAgentExit` | 循环结束后经 defer + recover 调用一次 | 即使捕获 panic 也保证执行，用于兜底收尾 |
| `OnToolResult` | 每个工具执行完成后回调 `(toolName, result)` | 用于日志/审计 |
| `OnStepEnd` | 每步 ToolCalls 执行完毕后触发 | 仅当该步存在 ToolCalls 时调用 |

> 典型使用方是 `GitCheckpointManager`（internal/agents/git_checkpoint.go）：CodingAgent 在构建 `ExecutorConfig` 时注入 OnAgentStart/OnAgentExit/OnStepEnd 实现 git 检查点生命周期管理。

### 2.5 Director 特有能力

Director 相比普通子 Agent 多出五项特有能力，全部实现在 `internal/agents/director.go`：

#### 2.5.1 熔断器

`DirectorAdapter`（director_adapter.go）提供 `IsCircuitBreakerOpen()`：连续 LLM 调用失败达到阈值后熔断被触发，阻断后续请求直接失败返回，避免雪崩式无效重试；配套的恢复逻辑见 `internal/agents/director/recovery.go` 的 CircuitBreaker（失败计数阈值 + 冷却时间窗口，超时 30s 量级）。

#### 2.5.2 委派强制检测

Director 收尾时若模型未通过任何 `delegate_*` 工具委派子 Agent 就想直接结束，会向消息序列注入一条模拟 user 消息强制其委派；该"强制提醒"最多触发 `maxNonDelegationPrompts`（常量 = 3，director.go）次，达到上限后放行，防止死循环（循环本身还有 MaxSteps 兜底）。

#### 2.5.3 tool_call 配对修复

`validateAndRepairToolCallPairs(messages []llm.Message)`（director.go 尾部）在送入 LLM 前校验并修复消息序列中 assistant 的 tool_calls 与 Role=Tool 结果消息的配对关系——主要修复上下文截断/紧急压缩后可能出现的配对断裂，否则部分 provider 会直接拒绝请求。

#### 2.5.4 子 Agent 记忆回流

`injectSubAgentMemory(result, toolCallID, toolName)` 将子 Agent 的执行结果写入 Director 的 `currentMemory`：写入的是结果摘要消息（`result.Text`），并打上 `IsSubAgent=true` 标记与 ParentID 关联；子 Agent 的完整内部对话历史**不会**整体回灌，也不会再发送给 LLM——避免 Director 上下文快速膨胀和 Compact Engine 频繁压缩导致信息丢失。Director 后续组装历史时会过滤 `IsSubAgent=true` 的内部消息。

#### 2.5.5 项目上下文缓存

`cachedProjectContext` 字段使项目上下文文件在同一会话内只加载一次：首次加载后将结果缓存于 Director 实例，之后直接命中缓存返回，避免每个任务重复读取项目上下文带来的延迟与 token 开销。

### 2.6 MetaAgent 动态注册链（重点）

MetaAgent 是系统的"元能力"：它本身是一个纯设计者（单次 LLM 调用、无工具），根据任务动态设计一个专属子 Agent，由 Director 完成注册与执行。完整时序如下：

```mermaid
sequenceDiagram
    autonumber
    participant D as DirectorAgent
    participant M as MetaAgent
    participant P as parseMetaAgentOutput
    participant R as registerCustomAgent
    participant E as executeCustomAgent
    participant L as RunAgentLoop

    D->>M: Run(ctx, 设计任务描述)
    M->>M: 单次 LLM 调用（纯设计，无工具）
    M-->>D: JSON{thinking, agent_name, agent_design(systemPrompt), tools_used[], task_for_agent}
    D->>P: 解析 MetaAgent 原始输出
    P->>P: extractJSONObject 从 markdown 围栏容错提取 JSON
    alt 解析失败
        P-->>D: error
        D->>D: 追加 [FORMAT CORRECTION] 格式修正提示，重试至多 metaRetryCount 次
        D->>P: 再次解析
    else 解析成功
        P-->>D: systemPrompt + CustomAgent{name/design/tools_used/task}
    end
    D->>R: registerCustomAgent(ca)
    R->>R: 构造 delegate_{snake_name} 工具（闭包捕获 ca 与专属 adapters）
    R->>R: 从 Director 工具集按 ca.ToolsUsed 挑选，构建专属 adapters
    R->>R: 追加 agent_exit 工具，并用 SetGuardOnAdapters 套上 workspace_guard
    R->>R: 注册 customAgents map，并将 delegate 工具 append 到 Director.Adapters
    D->>E: executeCustomAgent(ctx, ca, adapters, task_for_agent)
    E->>E: 新建 ExecutorConfig（MaxSteps=15 硬编码，StopOnFinish=true）
    E->>L: RunAgentLoop（ctx 注入独立 RolloutWriter，defer Close）
    L-->>E: result.Text + result.History
    E-->>D: 子 Agent 文本结果（存入 pendingSubAgentMemory 待回流）
    Note over D: 此后 delegate_{name} 工具对本会话永久可用
```

关键实现细节：

1. **容错解析**：`extractJSONObject` 能从 markdown 代码围栏等包裹文本中定位最外层 JSON 对象；`parseMetaAgentOutput` 校验必需字段（缺 `agent_design` 即报错）。
2. **格式修正重试**：解析失败时，Director 会把带 `[FORMAT CORRECTION — Attempt n/N]` 的修正提示追加进消息再次请求 MetaAgent，最多重试 `metaRetryCount`（默认 3）次。
3. **专属工具集**：自定义 Agent 的 adapters 只从 Director 现有工具集中挑选（`ca.ToolsUsed` 声明），未知工具会被跳过并告警；无论声明与否都会追加 `agent_exit` 工具以便显式退出。
4. **立即执行**：注册完成后立刻用 `task_for_agent`（剥离了元设计指令的干净任务描述）驱动 `executeCustomAgent`，其中 `MaxSteps=15` 为硬编码值（魔法值，列入 Phase 1 收敛清单）。

### 2.7 端到端执行流程

从用户输入到最终输出的完整链路：

```mermaid
sequenceDiagram
    autonumber
    participant U as 用户
    participant Main as main.go（模式入口）
    participant App as app.CodeActor
    participant Dir as DirectorAgent
    participant Sub as 自定义子Agent
    participant Loop as RunAgentLoop

    U->>Main: 输入任务（runTUI / runHTTP / runPrompt）
    Main->>App: ProcessCodingTaskWithCallback(request)
    App->>App: Init()（initOnce 保证单次初始化）
    App->>Dir: Run(ctx, input, mem)
    Dir->>Dir: 组装 systemPrompt = director.prompt + 自定义Agent描述 + 项目上下文 + 知识注入
    Dir->>Dir: 追加会话历史（过滤 IsSubAgent=true 的内部消息）
    loop Director 内联 LLM 循环（三分支）
        Dir->>Dir: 压缩检查 → GenerateContent
        alt 分支一：直接文本
            Dir-->>Dir: 准备返回最终文本
        else 分支二：普通工具
            Dir->>Dir: Adapters 线性查找分发执行，Role=Tool 回填
        else 分支三：委派 delegate_*
            Dir->>Sub: executeCustomAgent(task_for_agent)
            Sub->>Loop: RunAgentLoop(maxSteps=15)
            Loop-->>Sub: result.Text
            Sub-->>Dir: 子 Agent 结果
            Dir->>Dir: injectSubAgentMemory（标记 IsSubAgent=true）
        end
    end
    Note over Dir: 未委派即想结束 → 强制提醒（上限 maxNonDelegationPrompts）
    Dir-->>U: 通过委派检测后返回最终文本
```

### 2.8 并发模型现状

当前系统的并发特征可概括为四点：

1. **工具单线程顺序执行**：无论是 `RunAgentLoop` 还是 Director 内联循环，同一轮的多个 ToolCalls 都是 for 循环逐个分发、串行执行，**没有任何并行**——这是 Phase 3「并行工具」优化的直接动因；
2. **MCP 客户端后台初始化**：`internal/mcp` 的客户端在 `Init()` 中以后台 goroutine 启动，不阻塞主流程，就绪前相关工具调用会等待/降级；
3. **流式事件逐 chunk 发布**：LLM 流式输出经 `CallOptions.StreamHandler` 回调逐 chunk 发布 `ai_chunk` 事件，经 messaging 总线推往 TUI/WebSocket 前端；
4. **ConsolidationWorker 异步整理**：记忆整理任务由独立的 goroutine + channel 异步消费，与主执行链路解耦。

---

## 3. 问题诊断（分级清单）

基于第 2 章的现状走读，本章将识别出的问题按 **P0（阻断性/高维护风险）/ P1（结构性缺陷）/ P2（一致性与体验）** 三级归类，先给出总表，再对三项 P0 问题逐一展开影响链详述。

### 3.1 问题总表

| 编号 | 问题描述 | 影响 | 级别 |
|-------|----------|------|------|
| P0-1 | **双循环重复**：`RunAgentLoop`（executor.go，632 行）与 Director.Run 内联循环（约 600 行）并存，压缩逻辑/事件发布序列/token 估算/重试退避（上限 30s）/Rollout 写入全部双份 | 任何改动需双处同步，是主要维护负担和 bug 来源 | 🔴 P0 |
| P0-2 | **director.go 过大（1675 行）**：混合了主循环/Meta 解析/Agent 注册/委派执行/压缩/熔断/memory 注入/Rollout | 违反单一职责，改动影响面不可控 | 🔴 P0 |
| P0-3 | **工具分发 O(n)**：每次分发 `for` 遍历 self.Adapters 线性查找；tools.Registry 本身是 map 但 Director 分发未用索引 | 随工具与动态注册 delegate 增多线性退化 | 🔴 P0 |
| P1-4 | **taskID 包级全局隐式传播**：经 logging.SetCurrentTaskID/GetCurrentTaskID 传播 | 全局可变状态有并发竞态风险；Director 结构体不持有 taskID，调试困难 | 🟡 P1 |
| P1-5 | **魔法值散落**：executeCustomAgent 硬编码 maxSteps=15；普通工具超时 120s、delegate 超时 10min；错误消息截断 1000 字符；压缩默认阈值 | 均未配置化，调参必须改代码重新编译 | 🟡 P1 |
| P1-6 | **配对规则三处实现**：tool_call↔tool 配对/修复规则在 executor、memory（repairToolCallPairsAfterTruncation）、Director（validateAndRepairToolCallPairs）三处分别实现 | convertToolCalls 等转换函数跨文件，易不一致 | 🟡 P1 |
| P1-7 | **引擎同步窗口**：子 Agent 的 LLM 引擎刷新依赖 refreshSubAgentEngines() 手动推式同步 | 存在 Director 与子 Agent 引擎不同步窗口 | 🟡 P1 |
| P2-8 | **Run 签名不一致**：Director.Run(ctx, input, mem) 3 参 vs 子 Agent Run(ctx, input) 2 参 | 接口不统一，阻碍进一步抽象 | 🟢 P2 |
| P2-9 | **MetaAgent 解析鲁棒性**：依赖 extractJSONObject 从 markdown 围栏容错提取 JSON，解析失败才重试 | 鲁棒性受 LLM 输出质量制约 | 🟢 P2 |
| P2-10 | **RolloutWriter 重复创建**：Director 与每个 delegate 分别创建，session/turn 元数据写入逻辑重复（SessionMetaWritten 守卫） | 写入逻辑重复，session 归属易混乱 | 🟢 P2 |

### 3.2 P0-1 详述：双循环重复

**影响链**：`RunAgentLoop`（executor.go）为子 Agent 提供统一的"思考—调工具—观察"循环，而 Director.Run 内部又维护了一条约 600 行的内联 LLM 循环用于自身编排。两条循环在以下五类关键逻辑上各有一份实现：

| 重复逻辑 | executor.go（RunAgentLoop） | director.go（Director.Run 内联循环） |
|----------|------------------------------|--------------------------------------|
| 上下文压缩 | 循环内按 token 预算检查并压缩/截断历史消息 | 内联循环独立实现压缩检查与压缩执行 |
| 事件发布 | OnAgentStart/OnToolResult/OnStepEnd 等钩子发布事件序列 | 内联循环自行发布同构事件序列，顺序与字段需人工对齐 |
| token 估算 | 基于 tiktoken 的消息 token 估算 | 内联循环独立估算，阈值判断逻辑重复 |
| 重试退避 | LLM 调用失败的指数退避重试（上限 30s） | 内联循环独立实现同样的指数退避（上限 30s） |
| Rollout 写入 | 循环过程写入 Rollout 记录 | 内联循环独立写入 Rollout，格式需人工保持一致 |

由此形成的影响链为：**修改任一关键逻辑（如压缩策略）→ 必须双处同步修改 → 漏改一处 → 子 Agent 与 Director 行为漂移 → 压缩/重试/事件表现不一致 → 难以定位的偶发 bug**。这是全系统最主要的维护负担与 bug 来源，也是执行摘要将其列为第一优先级问题的原因。

### 3.3 P0-2 详述：director.go 过大（1675 行）

`internal/agents/director.go` 是全仓库最大文件，单文件内混合了以下 8 类职责：

| 职责 | 典型内容 |
|------|----------|
| 主循环 | Director.Run 内联 LLM 循环与三分支分发 |
| Meta 解析 | MetaAgent 输出的 JSON 解析（extractJSONObject）与校验 |
| Agent 注册 | 动态注册/注销子 Agent（Adapters 维护） |
| 委派执行 | executeCustomAgent：组装任务、调用 RunAgentLoop、收集结果 |
| 压缩 | 内联循环内的上下文压缩逻辑 |
| 熔断 | 委派失败的熔断器（失败计数/冷却） |
| memory 注入 | injectSubAgentMemory 等会话记忆操作 |
| Rollout | 会话/轮次元数据与过程记录写入 |

**危害**：单一文件承载 8 类职责，任何一类改动都要在 1675 行内定位上下文；代码评审 diff 噪声大，回归测试范围不可控。这是继 P0-1 之后的第二大维护负担，且 P0-1 的修复（统一循环）必须先以本问题的拆分为前提，否则 diff 无法审查。

### 3.4 P0-3 详述：工具分发 O(n)

**现状**：每次工具分发时，Director 以 `for` 循环遍历 `self.Adapters` 逐个匹配工具名。`tools.Registry` 底层本身是 map（O(1) 查找），但 Director 的分发层没有使用任何索引，等于放弃了已有的哈希能力。

**复杂度影响**：

- 单次分发的匹配成本与「Adapters 数量 × 每 Adapter 工具数」成正比；
- 每个执行步骤中的每个 ToolCall 都触发一次线性查找，成本随步骤数线性叠加。

**动态注册场景的放大效应**：MetaAgent 支持运行时按需注册专属子 Agent，Adapters 列表随会话进行持续增长；每个 delegate 内部又持有自己的工具集，委派链路（Director → delegate → 工具）形成多层线性查找嵌套。在长会话、多 delegate 场景下，分发耗时从常数级退化为可感知的线性开销，而该开销完全可以通过索引化消除。

---

## 4. 重构方案详述

本章针对第 3 章诊断的问题逐项给出重构方案；每个方案按「目标形态 / 具体设计 / 迁移步骤 / 风险与回滚 / 工作量级」统一结构展开。方案之间存在明确依赖：方案 B 必须先行于方案 A，其余方案彼此独立，可按标注的优先级与工作量级排期实施。

### 4.1 方案A【最高优先级】：统一双循环（解决 P0-1）[L]

**目标形态**：Director 不再内联循环，收敛为「组装 prompt → 注入特有能力到 ExecutorConfig → 复用 RunAgentLoop」，消除约 600 行重复逻辑。Director 与普通子 Agent 共享同一条执行主干，Director 特有能力全部通过 hook 注入点表达，而非复制一份循环实现。

**具体设计**：关键洞察是现有 ExecutorConfig hooks（OnAgentStart/OnAgentExit/OnToolResult/OnStepEnd）已具雏形，扩展 3 个注入点即可承载 Director 特有能力：

- `BeforeLLMCall(messages []Message) ([]Message, error)`——每次 LLM 调用前回调；熔断器检查在此实现（返回 error 即中断循环）；委派强制检测所需的消息注入也在此修改 messages 后返回；
- `ShouldReturn(response *Response, messages []Message) (bool, []Message)`——循环结束决策点；Director 在「无 ToolCalls 想结束」时返回 false 并附加强制委派提示消息，循环据此继续执行下一轮；普通子 Agent 使用默认实现（恒返回 true），行为不变；
- `MessageSanitizer(messages []Message) []Message`——每步消息净化钩子；tool_call 配对修复统一挂载于此。

ExecutorConfig 扩展字段示意（含三个新 hook 字段及默认 no-op 实现说明）：

```go
// executor.go — ExecutorConfig 扩展示意
type ExecutorConfig struct {
	// ……既有字段保持不变……

	// ---- 既有 hooks ----
	OnAgentStart func(ctx context.Context, messages []Message) error
	OnAgentExit  func(ctx context.Context, messages []Message)
	OnToolResult func(ctx context.Context, call ToolCall, result string) error
	OnStepEnd    func(ctx context.Context, step int, messages []Message)

	// ---- 新增注入点（三者均提供默认 no-op 实现）----

	// BeforeLLMCall 在每次 LLM 调用前回调；返回 error 即中断循环。
	// 熔断器检查在此实现；委派强制检测的消息注入也在此修改 messages。
	// 默认实现：原样返回入参，error 为 nil。
	BeforeLLMCall func(messages []Message) ([]Message, error)

	// ShouldReturn 是循环结束决策点：返回 true 则按原逻辑结束本轮循环，
	// 返回 false 则以第二个返回值替换 messages 继续下一轮。
	// 默认实现：恒返回 (true, nil)——对现有子 Agent 等价于无此钩子。
	ShouldReturn func(response *Response, messages []Message) (bool, []Message)

	// MessageSanitizer 每步消息净化钩子，返回净化后的消息序列。
	// 默认实现：原样返回入参。
	MessageSanitizer func(messages []Message) []Message
}
```

Director 特有能力 → hook 注入点映射表：

| Director 特有能力 | hook 注入点 | 说明 |
| --- | --- | --- |
| 熔断器检查 | BeforeLLMCall | 达到阈值时返回 error 中断循环 |
| 委派强制检测 | ShouldReturn | 「无 ToolCalls 想结束」时返回 false 并附加强制委派提示消息 |
| tool_call 配对修复 | MessageSanitizer | 每步统一执行配对校验与修复 |
| Sub-Agent memory 注入 | 既有 OnToolResult 扩展承载 | 复用既有 hook 语义，仅扩展实现内容 |
| Rollout 写入 | 既有 OnStepEnd 扩展承载 | 同上 |

必须强调：三个新 hook 均提供默认空操作(no-op)，未显式注入实现的调用方行为完全不变，因此对现有子 Agent 零行为变化；Director 仅在自己组装 ExecutorConfig 时注入上述实现。

**迁移步骤**（五步，每步独立提交）：

1. 先做纯拆分（方案 B），使 director.go 具备可审查的最小 diff 粒度；
2. executor.go 增加 3 个 hook 字段（默认 no-op），现有调用方零改动、编译通过；
3. Director 内联循环逐段搬运为 hook 实现（熔断器→BeforeLLMCall、委派强制检测→ShouldReturn、tool_call 配对修复→MessageSanitizer、Sub-Agent memory 注入与 Rollout 写入→既有 OnToolResult/OnStepEnd 扩展承载）；
4. 删除内联循环，Director.Run 改为「组装 prompt + 注入 hooks + 调 RunAgentLoop」的三段式结构；
5. 行为等价性验证：以事件流录制(Rollout)对照新旧输出，确认语义一致后合入。

**风险与回滚**：主要风险在于事件发布顺序与压缩时机可能存在细微差异，进而改变可观测行为；对策是全程使用 Git Checkpoint 保护每一步状态，并利用事件流录制(Rollout)做新旧 diff 对照；由于迁移五步各自独立提交，任一步出问题均可单独 revert，不影响已完成步骤。

### 4.2 方案B：director.go 纯拆分（解决 P0-2）[M]

**目标形态**：director.go（1675 行）按职责拆分为四个文件，单一职责、边界清晰：

- director_run.go——主流程（Run 入口与编排逻辑）；
- director_delegate.go——委派执行（delegate 创建、分发与生命周期管理）；
- director_meta.go——Meta 解析与注册（MetaAgent 动态子 Agent 注册链路）；
- director_memory.go——记忆注入（injectSubAgentMemory 等会话记忆操作）。

**具体设计**：原则为「先拆不改」——仅移动代码不改任何逻辑，不调整函数签名、不重命名符号、不动包级状态；拆分本身不追求性能或结构优化，其核心价值在于为方案 A 提供可审查的前提：后续「内联循环搬为 hook」的大改动可以按文件聚焦评审，diff 噪声可控。

**迁移步骤**：纯机械移动代码至四个目标文件 → 补齐各文件 import → 编译通过 → 行为等价验证（Rollout 输出无 diff）。

**风险与回滚**：风险极低（不引入任何行为变化）；以单个提交完成整体拆分，若需回退可直接 revert 该提交，无需局部挑选。

**与方案 A 的依赖关系**：B 必须先行于 A——只有先把 1675 行拆散到职责明确的文件中，方案 A 对主流程与委派执行的改造才能获得最小、可审查的 diff 粒度；顺序颠倒会使两个高风险变更混在同一份巨型文件的 diff 里，无法有效评审。
