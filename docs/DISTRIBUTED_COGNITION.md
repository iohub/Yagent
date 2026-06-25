# 分布式认知架构重构方案（v2 — 增强型 Commander 模式）

> 基于对 codeactor-agent 现有代码的系统性审查编写。
> 
> **版本说明**：v2 版本从「Facilitator 混合促进者模型」调整为「增强型 Commander 模式」。
> 保留 Commander 模式的核心优势（Conductor 主导分解→委派→验证），
> 在 delegation 层增加 P2P 旁路通道解决上下文超限问题。

---

## 目录

- [1. 现状分析](#1-现状分析)
- [2. 根本问题：通信路径瓶颈](#2-根本问题通信路径瓶颈)
- [3. 目标架构：增强型 Commander](#3-目标架构增强型-commander)
- [4. 核心组件设计](#4-核心组件设计)
  - [4.1 P2P Supplement — 子 Agent 协作 Prompt 注入](#41-p2p-supplement--子-agent-协作-prompt-注入)
  - [4.2 AgentMesh 增强 — 自动注册/注销 + Capability 字段](#42-agentmesh-增强--自动注册注销--capability-字段)
  - [4.3 Result Compressor — 结果压缩器](#43-result-compressor--结果压缩器)
  - [4.4 P2P Delegate — 子 Agent 委派工具](#44-p2p-delegate--子-agent-委派工具)
  - [4.5 Observer Filter — Conductor 事件过滤器](#45-observer-filter--conductor-事件过滤器)
  - [4.6 Feature Flag 系统 — 渐进式部署开关](#46-feature-flag-系统--渐进式部署开关)
- [5. 修改文件清单](#5-修改文件清单)
- [6. 实施步骤](#6-实施步骤)
- [7. 安全机制与降级策略](#7-安全机制与降级策略)
- [8. 验证策略](#8-验证策略)
- [9. 回滚计划](#9-回滚计划)
- [10. 长期演进路径](#10-长期演进路径)

---

## 1. 现状分析

### 1.1 现有架构概览

当前 CodeActor Agent 采用 **Hub-and-Spoke（中枢-辐条）** 架构，核心是一个 **ConductorAgent** 协调多个专用子 Agent。

#### 通信层

| 组件 | 位置 | 说明 |
|------|------|------|
| `EventBus` | `internal/messaging/bus/bus.go` | 进程内 topic-based pub/sub，支持 Publish/Subscribe/Request/Response/Observer 模式 |
| `AgentPeer` | `internal/messaging/peer/peer.go` | 每个 Agent 拥有 P2P 身份（`_request.{id}` / `_response.{id}` 通道），支持同步 Request/Response |
| `AgentMesh` | `internal/agents/mesh.go` | 管理 Agent P2P 通信网格，Conductor 注册为全局 Observer |
| `AgentRouter` | `internal/agents/router.go` | 路由规则引擎（已实现但未被实际用于运行时路由） |

**P2P Topic 定义**（`internal/messaging/peer/topics.go`）：

```go
// 同域 P2P Topic（直连，Conductor 仅观察）
const (
    TopicSymbolsUpdated    = "symbols.updated"     // Repo → Coding (pub/sub)
    TopicSymbolsRequest    = "symbols.request"     // Coding → Repo (req/resp)
    TopicSymbolsInvalidate = "symbols.invalidate"  // Repo → Coding (pub/sub)
    TopicImpactAnalysis    = "analysis.impact"     // Coding → Repo (req/resp)
    TopicFileChanged       = "files.changed"       // Coding → Repo (pub/sub)
)

// 路由策略
const (
    RoutingP2P        // 纯 P2P 直连
    RoutingHybrid     // P2P 直连 + Conductor 观察
    RoutingConductor  // Conductor 仲裁
)
```

#### 内存层

| 组件 | 位置 | 说明 |
|------|------|------|
| `LocalMemory` | `internal/memory/local.go` | Agent 私有对话历史（maxSize 控制 FIFO 裁剪） |
| `SharedMemory` | `internal/memory/shared.go` | 全局可见共享上下文（MVCC 版本控制，topic-based pub/sub） |
| `LayeredMemory` | `internal/memory/layered.go` | 合并 Local+Shared，支持 Auto-Promote 策略 |
| `ConversationMemory` | `internal/memory/memory.go` | Conductor 使用的对话记忆 |

#### 编排层

| 组件 | 位置 | 说明 |
|------|------|------|
| `ConductorAgent` | `internal/agents/conductor.go` | 中心化 LLM-driven 编排，通过 delegate_* 工具委派子任务 |
| `Executor` | `internal/agents/executor.go` | 通用 LLM-tool 循环（RunAgentLoop），用于执行子 Agent |
| `MetaAgent` | `internal/agents/meta.go` | 动态设计新 Agent 并注册为永久 delegate 工具 |
| `p2p_integration.go` | `internal/agents/p2p_integration.go` | P2P 工具函数 + 符号表缓存 + Repo/Coding P2P 初始化 |

### 1.2 基础设施存量分析

```
┌──────────────────────────────────────────────────────────────┐
│                    基础设施存量地图                            │
├─────────────┬──────────────────┬────────────────────────────┤
│   层级      │   已实现但未启用   │    可直接复用              │
├─────────────┼──────────────────┼────────────────────────────┤
│   通信层    │ p2p_query 工具    │ EventBus                  │
│             │ p2p_notify 工具   │ AgentPeer (Request/Resp)  │
│             │ AgentRouter      │ AgentMesh + Observer       │
├─────────────┼──────────────────┼────────────────────────────┤
│   内存层    │ LayeredMemory    │ SharedMemory (MVCC)       │
│             │ AutoPromote      │ LocalMemory               │
│             │ SharedMemory     │ ConversationMemory        │
│             │ 的 topic 订阅    │                           │
├─────────────┼──────────────────┼────────────────────────────┤
│   认知层    │ sub-agent prompt │ Executor (RunAgentLoop)    │
│             │ 中无 P2P 描述    │ Meta-Agent 动态注册       │
│             │                 │ SubAgentMessage inject    │
└─────────────┴──────────────────┴────────────────────────────┘
```

### 1.3 Commander 模式运行现状评估

| 维度 | 评估 | 结论 |
|------|------|------|
| **决策路径**（分解→委派→验证→整合） | ✅ 运行良好 | **保留不动** |
| **Conductor Prompt** 设计 | ✅ 团队熟悉、有效 | **不大改** |
| **主线任务管理** | ✅ Conductor 全局掌控 | **保留** |
| **通信路径**（Conductor 充当通信中继） | ❌ 上下文超限的根源 | **需要旁路** |

---

## 2. 根本问题：通信路径瓶颈

### 2.1 核心洞察：问题不在决策路径，在通信路径

```
错误归因（v1 Facilitator 方案）：
  "Conductor 的决策能力被 token 上限封顶 → 需要角色转换"

正确归因（v2 增强型 Commander）：
  "Conductor 的决策能力没问题，但通信路径导致 context 膨胀"
```

### 2.2 因果链

```
所有跨 Agent 通信必须经 Conductor 中转
    └─► Conductor LLM context 承载中转数据
        └─► Repo-Agent 的结果 → Conductor context
            └─► Conductor 将结果写入 Coding-Agent 的 delegation prompt
                └─► Coding-Agent 的结果 → Conductor context
                    └─► 跨 Agent 依赖越多，context 线性/超线性增长
                        └─► 复杂任务上下文超限
```

**本质**：Conductor 的 context 中充斥着大量「**中转数据**」而非「**决策数据**」。

### 2.3 证据

1. **delegate_* 工具是唯一的 Agent 间通信通道** — 所有子 Agent 的输出必须流经 Conductor 的 LLM 才能到达另一个子 Agent。如果 Coding-Agent 需要 Repo-Agent 的信息，流程是：
   ```
   Conductor: delegate_repo → 拿到完整结果(5K) → 放入自己的 context
            → delegate_coding prompt 中包含 Repo 结果(5K) → 等待结果
            → Coding 结果(8K) → 放入自己的 context
   Conductor context += 5K(Repo) + 8K(Coding) = 13K ← 中转数据
   ```

2. **P2P 工具已注入但 prompt 未描述** — `internal/agents/executor.go` 的 `buildP2PTeamAdapters()` 函数创建了 `p2p_query` 和 `p2p_notify` 工具，但没有任何子 Agent 的 system prompt 描述这些工具的存在。LLM 不知道这些工具可用。

3. **SharedMemory 未被主动使用** — `LayeredMemory` 实现了 Local + Shared 合并，但 `AutoPromote` 策略仅在 `AddMessageWithPromote` 中触发，而子 Agent 执行循环（`RunAgentLoop`）调用的是 `AddMessage` 而非 `AddMessageWithPromote`。

4. **AgentMesh 的 P2P 能力闲置** — `AgentMesh` 已实现 Agent 发现和 P2P 通信网格，但 delegation 流程中从未将 Agent 注册到 Mesh，导致 P2P 通信无目标。

### 2.4 问题总结

| 问题 | 严重程度 | 影响范围 | 解决方案 |
|------|----------|----------|----------|
| Conductor 充当通信中继 | 🔴 致命 | 上下文超限的根本原因 | P2P 旁路通道 |
| P2P 基础设施未激活 | 🟡 严重 | 已投入的开发资源浪费 | 激活 AgentMesh + p2p_query/notify |
| SharedMemory 协作语义缺失 | 🟡 严重 | 跨 Agent 信息共享需要显式编码 | 作为完整结果存储 |
| 结果传递无压缩 | 🟠 中等 | 大结果直接进入 Conductor context | Result Compressor |

---

## 3. 目标架构：增强型 Commander

### 3.1 核心设计思路

**一句话总结**：在 Commander 架构的 delegation 层增加一条「P2P 旁路通道」，子 Agent 间的跨依赖通信走旁路直接解决，Conductor 只接收压缩后的结果摘要。

**设计原则**：

1. **Conductor 决策路径不变** — 保留 Commander 的分解→委派→验证→整合流程，prompt 不做结构性改动
2. **子 Agent 通信路径旁路** — P2P + SharedMemory，不经过 Conductor context
3. **结果上行压缩** — 完整结果存 SharedMemory，摘要返回 Conductor
4. **基础设施自动驱动** — P2P 能力在 delegation 时由 Delegation Layer 自动注入，不依赖 Conductor prompt
5. **增量修改** — 最小化代码改动，优先复用现有设施（AgentMesh, AgentPeer, SharedMemory, p2p_query/notify）
6. **Feature Flag 控制** — 所有新行为通过环境变量控制，默认关闭，零影响

### 3.2 核心转变

| 维度 | 当前模式 (Commander) | 增强模式 (Enhanced Commander) | 改动方式 |
|------|---------------------|-------------------------------|----------|
| Conductor Prompt | 完整编排 prompt | **完全不改** | 🔵 零改动 |
| Conductor 角色 | 分解→指派→检查→整合 | **保留不变**，全局管理主线 | 🔵 零改动 |
| 跨 Agent 通信 | 全部经 Conductor 中转 | P2P 旁路（Conductor 只收摘要） | 🟡 Delegation 层增强 |
| 子 Agent Prompt | 无 P2P 描述 | 动态注入 P2P Supplement | 🟢 追加模板 |
| AgentMesh | 已实现未使用 | 自动注册/注销 Agent | 🟢 激活使用 |
| p2p_query/notify | 已注入未描述 | 子 Agent 知道可用 | 🟢 prompt 描述 |
| SharedMemory | 未被主动使用 | 完整结果 + P2P 日志存储 | 🟢 增加写入 |
| 结果传递 | 完整结果进 Conductor | 摘要进 Conductor，完整存 SharedMemory | 🟡 增加压缩器 |

### 3.3 目标架构图

```
┌──────────────────────────────────────────────────────────────────────┐
│                     Enhanced Commander Architecture                   │
├──────────────────────────────────────────────────────────────────────┤
│                                                                      │
│  ┌────────────────────────────────────────────────────────────────┐  │
│  │                    Conductor (Main Thread)                     │  │
│  │                                                                │  │
│  │  Prompt: 原样保留                                              │  │
│  │  Role: Decompose → Delegate → Verify → Integrate              │  │
│  │  Context: Task + Delegate Prompts + Compressed Results        │  │
│  │           (不包含 P2P 中间通信)                                │  │
│  │                                                                │  │
│  │  ┌─────────────────────────────────────────────────────────┐   │  │
│  │  │ Observer Filter: P2P 事件 → SharedMemory 日志           │   │  │
│  │  │                (NOT injected to LLM context)            │   │  │
│  │  └─────────────────────────────────────────────────────────┘   │  │
│  └────────────────────────────────────────────────────────────────┘  │
│                              │                                       │
│                    delegate_repo() / delegate_coding() / ...         │
│                              │                                       │
│  ┌───────────────────────────▼────────────────────────────────────┐  │
│  │              Enhanced Delegation Layer                          │  │
│  │                                                                 │  │
│  │  ┌──────────────────┐  ┌────────────────┐  ┌───────────────┐  │  │
│  │  │ Mesh Register    │  │ P2P Supplement │  │ Result        │  │  │
│  │  │ (on delegate)    │  │ (inject to     │  │ Compressor    │  │  │
│  │  │ + TTL evict      │  │  sub-agent,    │  │ (summary→Cond)│  │  │
│  │  │                  │  │  role-based)   │  │               │  │  │
│  │  └──────────────────┘  └────────────────┘  └───────────────┘  │  │
│  └───────────────────────────┬────────────────────────────────────┘  │
│                              │                                       │
│         ┌────────────────────┼────────────────────┐                 │
│         ▼                    ▼                    ▼                  │
│  ┌────────────┐     ┌────────────┐      ┌────────────┐             │
│  │ Repo-Agent │     │Coding-Agent│      │DevOps-Agent│             │
│  │ (explorer) │◄────│ (executor) │◄────►│ (executor) │             │
│  │ Read-Only  │ p2p │            │ p2p  │            │             │
│  │ Code Search│query│ +P2P aware │deleg.│ +P2P aware │             │
│  │            │     │ prompt     │      │ prompt     │             │
│  └────────────┘     └─────┬──────┘      └─────┬──────┘             │
│                           │                   │                     │
│                    p2p_delegate│        p2p_delegate│               │
│                           ▼                   ▼                     │
│                    ┌───────────────────────────────────┐            │
│                    │        AgentMesh                  │            │
│                    │  ┌──────────────────────────┐     │            │
│                    │  │  AgentEntry              │     │            │
│                    │  │  - agent_id              │     │            │
│                    │  │  - role (explorer|exec) │     │            │
│                    │  │  - capabilities          │     │            │
│                    │  │  - endpoint              │     │            │
│                    │  └──────────────────────────┘     │            │
│                    │  ┌──────────────────────────┐     │            │
│                    │  │  SharedMemory (MVCC)     │     │            │
│                    │  │  Full Results / P2P Logs │     │            │
│                    │  └──────────────────────────┘     │            │
│                    └───────────────────────────────────┘            │
│                                                                      │
│ 图例:                                                                 │
│   ◄──── p2p_query : 只读查询（Repo-Agent 仅接受入站查询）            │
│   ────► p2p_delegate : 任务委派（仅 executor→executor）              │
│   ◄────► p2p 双向 : executor 之间可相互委派                          │
│   (explorer) : 只读探索角色，不接受/不发起 p2p_delegate              │
│   (executor) : 执行角色，可发起/接受 p2p_delegate                    │
└──────────────────────────────────────────────────────────────────────┘
```

### 3.4 Conductor 流程变化对比

#### 当前流程（不变的部分）

```
1. [不变] 接收用户请求
2. [不变] 分析任务，分解为子任务
3. [不变] 决定委派策略（哪个 Agent 做什么，顺序如何）
4. [不变] 调用 delegate_repo(task_1) → 等待结果
5. [不变] 调用 delegate_coding(task_2) → 等待结果
6. [不变] 调用 delegate_devops(task_3) → 等待结果
7. [不变] 验证结果，整合输出
```

#### 变化的部分（Delegation Layer 基础设施层面，Conductor 无感知）

```
Step 4: delegate_repo(task_1)
  [新增] Delegation Layer 自动:
    a. 创建 Repo-Agent 实例
    b. 在 AgentMesh 中注册（含能力声明）
    c. 注入 P2P Supplement 到 Repo-Agent 的 system prompt
    d. Repo-Agent 执行任务
    e. 完整结果存入 SharedMemory: /tasks/{id}/agents/repo-agent/result
    f. 压缩摘要返回给 Conductor  ← Conductor 只看到摘要
    g. Repo-Agent 在 Mesh 中标记为 "completed" (保留 TTL 供 P2P 查询)

   ⚠️ Repo-Agent 角色约束:
      - Repo-Agent 是 **只读探索者（explorer）**，不改变代码环境
      - 它 **不接受** p2p_delegate（其他 Agent 不能委派任务给它）
      - 它的 prompt 中 **不注入** p2p_delegate 工具（它不能委派其他 Agent）
      - 它仅响应 p2p_query 提供代码信息，是纯粹的能力提供者
      - 当 Coding-Agent 需要 Repo-Agent 的信息时，只能用 p2p_query
      - AgentMesh 的 Role 守卫会拒绝任何指向 explorer 的 p2p_delegate

Step 5: delegate_coding(task_2)
  [新增] Delegation Layer 自动:
    a. 创建 Coding-Agent 实例
    b. 在 AgentMesh 中注册
    c. 注入 P2P Supplement（包含可用 Agent 列表）
    d. Coding-Agent 执行任务:
       - 发现需要 Repo-Agent 的分析结果
       - [P2P] p2p_query("repo-agent", "module Y structure?")
         → Repo-Agent 从 SharedMemory 返回缓存结果
       - [P2P] p2p_delegate("devops-agent", "run lint on my changes")
         → DevOps-Agent 执行 lint，返回结果
       - 完成编码
    e. 完整结果存入 SharedMemory
    f. 压缩摘要返回给 Conductor ← 包含 "collaborated with repo-agent, devops-agent"
    g. P2P 通信日志已写入 SharedMemory，未进入 Conductor context
```

### 3.5 Context 对比（核心收益）

```
【当前模式】Conductor Context 内容:
  System Prompt
  + User Task
  + delegate_repo prompt
  + Repo Full Result (可能很长)              ← 中转数据
  + delegate_coding prompt + Repo Result     ← 中转数据重复
  + Coding Full Result (可能很长)            ← 中转数据
  + delegate_devops prompt + Coding Result   ← 中转数据重复
  + DevOps Full Result (可能很长)            ← 中转数据
  + Integration prompt + All Results         ← 中转数据再次重复
  = ~50K chars（以3个子任务为例）            ← O(n²) 增长

【增强模式】Conductor Context 内容:
  System Prompt
  + User Task
  + delegate_repo prompt
  + Repo Summary (200 chars)                 ← 决策数据
  + delegate_coding prompt (不含 Repo Result)← P2P 旁路
  + Coding Summary (250 chars)               ← 决策数据
  + delegate_devops prompt (不含 Coding Result) ← P2P 旁路
  + DevOps Summary (200 chars)               ← 决策数据
  + Integration prompt + All Summaries       ← 决策数据
  = ~5K chars                                ← O(n) 增长，压缩比 ~10x 🎯
```

---

## 4. 核心组件设计

### 4.1 P2P Supplement — 子 Agent 协作 Prompt 注入

**位置**: `internal/agents/prompts/p2p_supplement.go`（新增）

**定位**: 在子 Agent 的 system prompt 末尾动态追加 P2P 协作能力描述，让 LLM 知道可用工具。

#### 角色化模板（Role-Based Templates）

根据 Agent 的 `role`（explorer / executor），注入不同的 P2P 工具集。

##### Executor 模板（适用于 Coding-Agent / DevOps-Agent 等执行型 Agent）

```go
package prompts

type P2PAgentInfo struct {
    Name         string // "repo-agent" | "coding-agent" | ...
    Role         string // "explorer" | "executor"   ← 新增角色字段
    Status       string // "running" | "completed"
    Capabilities string // 人类可读的能力描述
}

type P2PSupplementData struct {
    AvailableAgents []P2PAgentInfo  // 当前可用的 Agent 列表
    TaskID          string          // 当前任务 ID
    MaxDepth        int             // P2P 委派最大深度（默认 2）
}

const ExecutorSupplementTemplate = `
## Collaboration Capabilities

You are part of a multi-agent system. You can communicate directly with other agents.

### Available Tools

1. **p2p_query(agent_name, query)** — Query another agent for information (read-only).
   Use when: You need context from another agent's domain expertise.
   Example: p2p_query("repo-agent", "What is the structure of internal/handlers/?")
   NOTE: You can query ANY agent, including read-only explorers like Repo-Agent.

2. **p2p_notify(agent_name, message)** — Notify another agent asynchronously.

3. **p2p_delegate(agent_name, subtask)** — Delegate a sub-task to another EXECUTOR agent.
   Use when: A sub-task is better handled by another agent's expertise.
   IMPORTANT: You CANNOT delegate to explorer agents (e.g., Repo-Agent).
   They are read-only and do not accept task delegation.
   Max delegation depth: {{.MaxDepth}}.

### Available Peers
{{range .AvailableAgents}}
- **{{.Name}}** (role: {{.Role}}, status: {{.Status}}): {{.Capabilities}}
{{end}}

### Role-Based Rules
- **explorer** agents (like Repo-Agent): Query-only. Use p2p_query, NOT p2p_delegate.
- **executor** agents (like Coding/DevOps): Can delegate and be delegated to.
- If you need code analysis, p2p_query the repo-agent.
- If you need a task executed, p2p_delegate an executor agent.
`
```

##### Explorer 模板（适用于 Repo-Agent 等只读探索型 Agent）

```go
const ExplorerSupplementTemplate = `
## Collaboration Capabilities

You are a **read-only code explorer**. Other agents may query you for code context.

### Available Tools

1. **p2p_query** — You will be queried by other agents for code information.
   Respond with code structure, symbol locations, dependency information, etc.
   NOTE: You can only RESPOND to queries. You cannot initiate queries to other agents.

### Constraints
- You do NOT have the p2p_delegate tool — you cannot delegate tasks to anyone.
- You do NOT accept p2p_delegate — if another agent tries, the runtime rejects it.
- Your responses are informational only. You do NOT modify code or the environment.
- Your access to the codebase is **read-only** at all times.
`
```

**Token 预算**：Executor 模板约 650 tokens，Explorer 模板约 300 tokens，均在现有子 Agent prompt 预算内。

---

### 4.2 AgentMesh 增强 — 自动注册/注销 + Capability 字段

**位置**: `internal/agents/mesh.go`（修改）

**定位**: 让 AgentMesh 在 delegation 流程中自动注册/注销 Agent，使 P2P 通信有目标可寻。

#### 增强的数据结构

```go
package mesh

// AgentCapability 描述 Agent 的能力和状态
type AgentCapability struct {
    Name         string       // "repo-agent"
    Role         string       // "explorer" | "executor"   ← 新增
    TaskID       string       // 关联任务 ID
    Status       string       // "running" | "completed" | "evicted"
    Capabilities string       // 能力描述（给 LLM 看）
    Peer         *AgentPeer   // P2P 通信端点
    RegisteredAt time.Time
    CompletedAt  time.Time
}

// 新增方法
func (m *AgentMesh) Register(ctx context.Context, cap AgentCapability) error {
    // 1. 注册到 Mesh（已有逻辑）
    // 2. 广播注册事件（Conductor Observer 可收到）
    // 3. 保存在活跃列表中
}

func (m *AgentMesh) SetStatus(agentName string, status string) error {
    // 更新 Agent 状态
}

func (m *AgentMesh) Find(agentName string) (*AgentCapability, error) {
    // 查找 Agent，优先返回 running 状态的
}

func (m *AgentMesh) ListActiveAgents(taskID string) []P2PAgentInfo {
    // 返回指定任务中所有活跃 Agent 的摘要信息（用于 P2P Supplement 渲染）
}

func (m *AgentMesh) QueryAgent(agentName string, query string) (string, error) {
    // 向指定 Agent 发送 P2P 查询
    // 1. 先查找 running 状态的 Agent → 直接 P2P
    // 2. 降级：从 SharedMemory 读取缓存结果
    // 3. 都不可用 → 返回错误
}
```

#### Role 字段语义

| Role | p2p_query 入站 | p2p_delegate 入站 | p2p_delegate 出站 | 代码写权限 |
|------|---------------|-------------------|-------------------|-----------|
| `explorer` | ✅ 接受查询 | ❌ 运行时拒绝 | ❌ 工具未注入 | ❌ 只读 |
| `executor` | ✅ 接受查询 | ✅ 接受委派 | ✅ 可委派其他 executor | ✅ 按职责 |

#### P2P 查询降级逻辑（更新版）

当 Coding-Agent 通过 p2p_query 查询 Repo-Agent 时：

```
Agent A p2p_query("repo-agent", "..."):

  1. AgentMesh.Find("repo-agent")
     ├── role == "explorer" → 允许查询 ✅
     ├── Status = "running" → AgentPeer.Request(query) → 实时响应 ✅
     ├── Status = "completed" → SharedMemory.Read("...")
     │   └── 返回 [Cached] 标注的缓存结果 ✅
     └── Status = "evicted" 或未找到 → 返回 "agent unavailable" ❌

  2. 查询不受 role 限制，explorer 和 executor 都可被查询

#### Repo-Agent 查询最佳实践

当其他 Agent 需要代码上下文时，应优先使用 p2p_query 而非等待 Conductor 中转：

```
Coding-Agent 内部:
  1. p2p_query("repo-agent", "Find all route definitions")
     → Repo-Agent 返回路由位置和结构
  2. p2p_query("repo-agent", "What middleware is currently used?")
     → Repo-Agent 返回中间件列表
  3. 基于这些信息，Coding-Agent 自行决策并编码
```

**优势**：
- 避免 Conductor context 中充斥探索中间结果
- Repo-Agent 的只读保证让 Coding-Agent 可以安全地反复查询
- Repo-Agent 完成后（completed 状态），查询自动降级为 SharedMemory 缓存

#### TTL 管理

```go
// Agent 完成后的缓存 TTL
const defaultAgentCacheTTL = 300 * time.Second

func (m *AgentMesh) OnAgentCompleted(agentName string) {
    m.SetStatus(agentName, "completed")

    // TTL 后自动 evict
    time.AfterFunc(config.AgentCacheTTL(), func() {
        m.SetStatus(agentName, "evicted")
        m.Unregister(agentName)
    })
}
```

---

### 4.3 Result Compressor — 结果压缩器

**位置**: `internal/agents/result_compressor.go`（新增）

**定位**: 将子 Agent 的完整结果压缩为摘要返回给 Conductor，完整结果存入 SharedMemory。

#### 数据结构

```go
package agents

type CompressedResult struct {
    Summary        string                 // 压缩摘要（返回给 Conductor）
    FullResult     *AgentResult           // 完整结果（存入 SharedMemory）
    StorageKey     string                 // SharedMemory 中的存储路径
    Metadata       map[string]interface{} // 压缩元数据
}

type ResultCompressor struct {
    threshold  int            // 压缩阈值（字符数，默认 2000）
    sharedMem  *SharedMemory
}

func NewResultCompressor(threshold int, sharedMem *SharedMemory) *ResultCompressor {
    return &ResultCompressor{
        threshold:  threshold,
        sharedMem: sharedMem,
    }
}
```

#### 压缩逻辑

```go
func (c *ResultCompressor) Compress(result *AgentResult, taskID string, agentName string) (*CompressedResult, error) {
    // 小结果不压缩
    if len(result.Content) <= c.threshold {
        return &CompressedResult{
            Summary:    result.Content,
            FullResult: result,
            StorageKey: "",
            Metadata:   map[string]interface{}{"compressed": false},
        }, nil
    }

    // 存储完整结果到 SharedMemory
    storageKey := fmt.Sprintf("/tasks/%s/agents/%s/result", taskID, agentName)
    c.sharedMem.Store(storageKey, result)

    // 生成摘要
    summary := c.generateSummary(result)

    return &CompressedResult{
        Summary:    summary,
        FullResult: result,
        StorageKey: storageKey,
        Metadata: map[string]interface{}{
            "compressed":      true,
            "original_length": len(result.Content),
            "storage_key":     storageKey,
        },
    }, nil
}

func (c *ResultCompressor) generateSummary(result *AgentResult) string {
    // 策略 1: 如果结果中有显式的 Summary 字段，直接使用
    if result.Summary != "" {
        return result.Summary
    }

    // 策略 2: 结构化提取（前 N 行关键信息）
    // 从结果中提取: 改动文件列表、关键决策、协作记录
    return extractStructuredSummary(result)
}
```

#### 摘要格式例

```
┌──────────────────────────────────────────────┐
│ [Agent: coding-agent]                        │
│ Task: 实现用户认证功能                        │
│ Status: COMPLETED                            │
│                                              │
│ Key Results:                                 │
│ - Created internal/handlers/auth.go          │
│ - Created internal/middleware/jwt.go         │
│ - 3 files modified, 1 file created          │
│                                              │
│ Collaborations:                              │
│ - Queried repo-agent for module structure    │
│ - Delegated test execution to devops-agent   │
│                                              │
│ Full details: /tasks/abc/agents/coding/      │
│   result (SharedMemory)                      │
└──────────────────────────────────────────────┘
```

#### Conductor Expand 机制

```go
// Conductor 可以按需获取完整结果（若有需要）
// 方式 1: 从 SharedMemory 直接读取
//   read_shared_memory("/tasks/abc/agents/coding-agent/result")

// 方式 2: 通过 Delegation Layer 的辅助方法（可选）
//   expand_result(storage_key) → 返回完整结果
```

---

### 4.4 P2P Delegate — 子 Agent 委派工具

**位置**: `internal/agents/p2p_delegate.go`（新增）

**定位**: 让子 Agent 可以直接委派子任务给同伴 Agent，绕开 Conductor。

#### 工具定义

```go
package agents

type P2PDelegateTool struct {
    mesh     *AgentMesh
    maxDepth int
}

// LLM 看到的工具描述
const P2PDelegateDesc = `Delegate a subtask to another agent via P2P communication.
The target agent receives your request and returns a response directly.
Use when: A task is better handled by another agent's expertise.
Example: p2p_delegate("devops-agent", "Run tests for the auth module")
Note: Max delegation depth is limited. Use p2p_query first for quick questions.`

func (t *P2PDelegateTool) Execute(ctx context.Context, params map[string]interface{}) (string, error) {
    targetAgent := params["agent"].(string)
    subtask := params["subtask"].(string)

    // 1. 深度检查
    currentDepth := getP2PDepth(ctx)
    if currentDepth >= t.maxDepth {
        return "", fmt.Errorf("max P2P delegation depth (%d) exceeded", t.maxDepth)
    }

    // 2. 查找目标 Agent
    cap, err := t.mesh.Find(targetAgent)
    if err != nil {
        return "", fmt.Errorf("agent %s not available: %w", targetAgent, err)
    }

    // 2.5 Role 守卫: 不能委派给 explorer 角色
    if cap.Role == "explorer" {
        return "", fmt.Errorf(
            "cannot delegate to %s (role=explorer): it is a read-only agent. "+
                "Use p2p_query instead to obtain information from it.",
            targetAgent)
    }

    // 3. 循环检测
    if detectCycle(ctx, targetAgent) {
        return "", fmt.Errorf("cycle detected: %s already in delegation chain", targetAgent)
    }

    // 4. 记录 P2P 事件日志
    logP2PEvent(ctx, P2PEvent{
        Type:   "delegate",
        From:   getAgentName(ctx),
        To:     targetAgent,
        Task:   subtask,
        Depth:  currentDepth + 1,
    })

    // 5. 同步委派
    ctx = withP2PDepth(ctx, currentDepth+1)
    response, err := cap.Peer.Request(ctx, subtask)
    if err != nil {
        return "", fmt.Errorf("P2P delegate to %s failed: %w", targetAgent, err)
    }

    return response, nil
}
```

#### 深度控制与循环检测

```go
// 深度通过 context 传递
type p2pDepthKey struct{}

func getP2PDepth(ctx context.Context) int {
    if depth, ok := ctx.Value(p2pDepthKey{}).(int); ok {
        return depth
    }
    return 0
}

func withP2PDepth(ctx context.Context, depth int) context.Context {
    return context.WithValue(ctx, p2pDepthKey{}, depth)
}

// 循环检测通过 delegation chain 实现
type delegationChainKey struct{}

func detectCycle(ctx context.Context, targetAgent string) bool {
    chain, ok := ctx.Value(delegationChainKey{}).([]string)
    if !ok {
        return false
    }
    for _, agent := range chain {
        if agent == targetAgent {
            return true
        }
    }
    return false
}
```

```
规则矩阵:

| 场景 | 结果 | 说明 |
|------|------|------|
| A → B (A≠B, depth=0) | ✅ 允许 | 首次委派 |
| A → B → C (depth=1→2) | ✅ 允许 | 链式委派（深度 2） |
| A → B → A (chain中无重复) | ❌ 循环检测拒绝 | A 已在委派链中 |
| A → B → C → D (depth≥3) | ❌ 深度限制拒绝 | 默认 MaxDepth=2 |
| A → A | ❌ 禁止 | 不能委派给自己 |
| A(executor) → B(explorer) (p2p_delegate) | ❌ Role 守卫拒绝 | explorer 不接受委派，请用 p2p_query |
```

---

### 4.5 Observer Filter — Conductor 事件过滤器

**位置**: `internal/agents/observer_filter.go`（新增）

**定位**: 确保 P2P 通信事件不注入 Conductor 的 LLM context，仅记录到 SharedMemory。

#### 设计

```go
package agents

type ObserverLevel string

const (
    ObserverOff     ObserverLevel = "off"      // 不处理 P2P 事件
    ObserverLog     ObserverLevel = "log"      // 仅记录到 SharedMemory（默认）
    ObserverInject  ObserverLevel = "inject"   // 记录 + 注入摘要到 context（不推荐）
)

type P2PEvent struct {
    Type      string    // "query" | "notify" | "delegate"
    From      string    // 发起者
    To        string    // 目标
    Task      string    // 任务描述摘要
    Depth     int       // 委派深度
    Timestamp time.Time
    Status    string    // "success" | "failure" | "timeout"
}

type ObserverFilter struct {
    level     ObserverLevel
    sharedMem *SharedMemory
}

func NewObserverFilter(level ObserverLevel, sharedMem *SharedMemory) *ObserverFilter {
    return &ObserverFilter{
        level:     level,
        sharedMem: sharedMem,
    }
}

func (f *ObserverFilter) OnP2PEvent(event P2PEvent) {
    if f.level == ObserverOff {
        return
    }

    // 总是记录到 SharedMemory
    logPath := fmt.Sprintf("/tasks/%s/p2p_logs/%d_%s_to_%s",
        event.Timestamp.UnixNano(), event.From, event.To)
    f.sharedMem.Store(logPath, event)

    // 根据级别决定是否注入 Conductor context
    switch f.level {
    case ObserverInject:
        // 记录 + 注入压缩摘要到 Conductor context
        // （默认不启用，避免增加 context 负担）
    case ObserverLog:
        // 仅记录，不注入
        // Conductor 可通过 expand_result 查询
    }
}
```

#### Conductor 中的集成

```go
// 在 Conductor 的 AgentMesh Observer 回调中:
func (c *Conductor) onMeshEvent(event MeshEvent) {
    switch event.Type {
    case "p2p_communication":
        c.observerFilter.OnP2PEvent(event.P2PEvent)
        // !! 关键: 不调用 c.context.Add(...)
        // 除非 ObserverLevel = "inject"
    case "agent_registered":
        // 正常的 Agent 注册事件，可以被 Conductor 感知
        c.context.Add(fmt.Sprintf("Agent %s started: %s", event.AgentName, event.Capabilities))
    case "agent_completed":
        // Agent 完成事件（与压缩后的结果摘要一起）
        c.context.Add(fmt.Sprintf("Agent %s completed", event.AgentName))
    }
}
```

**关键区分**：

| 事件类型 | 是否注入 Conductor context | 理由 |
|----------|---------------------------|------|
| `agent_registered` | ✅ 是（摘要） | 让 Conductor 知道有哪些 Agent 在工作 |
| `agent_completed` | ✅ 是（摘要） | 让 Conductor 感知进度 |
| `p2p_communication` | ❌ 否（默认） | 中间通信细节会污染 context |
| `artifact_produced` | ✅ 是（摘要） | 最终产物需要 Conductor 整合 |

---

### 4.6 Feature Flag 系统 — 渐进式部署开关

**位置**: `internal/config/config.go`（修改）

**定位**: 所有新行为通过环境变量控制，默认关闭确保零影响。

#### 环境变量定义

```bash
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
# Enhanced Commander Mode Feature Flags
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

# 总开关: P2P 协作模式（sub-agent 间直连通信）
# off = 当前 Commander 行为（默认）
# on  = 激活 P2P 旁路通道
CODEACTOR_P2P_COLLABORATION=off

# 结果压缩: 完整结果存 SharedMemory，摘要返回 Conductor
# off      = 返回完整结果（默认）
# summary  = 返回摘要，完整结果存 SharedMemory
CODEACTOR_RESULT_COMPRESSION=off

# P2P 委派深度限制
CODEACTOR_P2P_MAX_DEPTH=2

# Conductor Observer 级别
# off    = 不处理 P2P 事件
# log    = 仅记录到 SharedMemory（默认，不注入 context）
CODEACTOR_OBSERVER_LEVEL=off

# 已完成 Agent 的 P2P 缓存 TTL（秒）
CODEACTOR_AGENT_CACHE_TTL=300

# 结果压缩阈值（字符数，超过则压缩）
CODEACTOR_COMPRESSION_THRESHOLD=2000
```

#### Go Config 集成

```go
package config

type EnhancedCommanderConfig struct {
    P2PCollaboration    bool          // 总开关
    ResultCompression   bool          // 结果压缩
    P2PMaxDepth         int           // P2P 委派深度
    ObserverLevel       string        // Observer 级别
    AgentCacheTTL       time.Duration // Agent 缓存 TTL
    CompressionThreshold int          // 压缩阈值
}

func DefaultEnhancedCommanderConfig() EnhancedCommanderConfig {
    return EnhancedCommanderConfig{
        P2PCollaboration:    false,                     // 默认关闭
        ResultCompression:   false,                     // 默认关闭
        P2PMaxDepth:         2,                         // 最大 2 层
        ObserverLevel:       "off",                     // 默认不处理
        AgentCacheTTL:       300 * time.Second,         // 5 分钟
        CompressionThreshold: 2000,                     // 2000 字符
    }
}

func LoadEnhancedCommanderConfig() EnhancedCommanderConfig {
    cfg := DefaultEnhancedCommanderConfig()
    cfg.P2PCollaboration = os.Getenv("CODEACTOR_P2P_COLLABORATION") == "on"
    cfg.ResultCompression = os.Getenv("CODEACTOR_RESULT_COMPRESSION") == "summary"
    // ... 其他配置
    return cfg
}
```

---

## 5. 修改文件清单

### 5.1 新增文件（4 个）

| # | 路径 | 说明 | 工作量 |
|---|------|------|--------|
| 1 | `internal/agents/prompts/p2p_supplement.go` | **两套** P2P 工具描述模板（executor / explorer），按 role 动态注入 | Low |
| 2 | `internal/agents/result_compressor.go` | 结果压缩 + SharedMemory 存储 | Medium |
| 3 | `internal/agents/p2p_delegate.go` | p2p_delegate 工具实现（深度控制+循环检测） | Medium |
| 4 | `internal/agents/observer_filter.go` | P2P 事件过滤器，防止注入 Conductor context | Low |

### 5.2 修改文件（6 个）

| # | 路径 | 修改内容 | 风险 |
|---|------|----------|------|
| 5 | Delegation 处理逻辑（`internal/agents/` 相关文件） | 插入：Mesh 注册 → P2P Supplement 注入 → 结果压缩 | 🟡 Medium |
| 6 | `internal/agents/mesh.go` | 增加 AgentCapability 字段、TTL 管理、ListActiveAgents、QueryAgent | 🟢 Low |
| 7 | 子 Agent prompt 文件 | 在 prompt 构建时引用 P2P Supplement 模板 | 🟢 Low |
| 8 | `internal/config/config.go` | 增加 EnhancedCommanderConfig + Feature Flag | 🟢 Low |
| 9 | `internal/agents/conductor.go` | Observer 回调中过滤 P2P 事件（不注入 context） | 🟡 Low-Med |
| 10 | `internal/agents/executor.go` | 注册 p2p_delegate 工具到 Executor 的工具集 | 🟢 Low |

### 5.3 无需修改的文件

| 路径 | 原因 |
|------|------|
| Conductor 主 prompt | 用户硬约束，零改动 |
| `internal/messaging/bus/bus.go` | EventBus 功能完备，直接复用 |
| `internal/memory/local.go` | LocalMemory 功能完备 |
| `internal/memory/shared.go` | SharedMemory MVCC 功能完备，仅增加写入调用 |
| `internal/memory/layered.go` | LayeredMemory 功能完备 |
| `internal/memory/memory.go` | ConversationMemory 不变 |
| `internal/messaging/peer/peer.go` | AgentPeer 的 Request/Response 功能完备 |
| `internal/agents/meta.go` | Meta-Agent 逻辑不变 |
| `internal/agents/p2p_integration.go` | P2P 工具已注入，仅需 prompt 描述 |

---

## 6. 实施步骤

### 实施路线图总览

```
Phase 1: 激活 Mesh 注册     Phase 2: P2P Supplement    Phase 3: 结果压缩
   (半天, Low Risk)            (半天, Low Risk)           (半天, Med Risk)
       │                            │                        │
       └────────────────────────────┼────────────────────────┘
                                    ▼
                            Phase 4: P2P Delegate
                             (半天, Med Risk)
                                    │
                                    ▼
                            Phase 5: Observer Filter
                             (半天, Low Risk)
                                    │
                                    ▼
                            Phase 6: Feature Flag + 测试
                             (半天, Low Risk)
```

### 步骤 1: 激活 AgentMesh 注册/注销（半天）

```
目标: 让 AgentMesh 在 delegation 流程中自动注册/注销 Agent

修改:
  - mesh.go: 增加 AgentCapability 结构体、TTL 管理、Find/ListActiveAgents 方法
  - Delegation 逻辑: 创建 Agent 时注册到 Mesh，完成时标记 + TTL 后注销

验收标准:
  - delegate_repo 执行后，AgentMesh 中有 repo-agent 记录
  - repo-agent 完成后状态变为 "completed"
  - TTL 过期后自动 evict
  - mesh.Find("repo-agent") 能正确返回
```

### 步骤 2: 子 Agent P2P Supplement 注入（半天）

```
目标: 让子 Agent 的 LLM 知道 P2P 工具可用

修改:
  - 创建 internal/agents/prompts/p2p_supplement.go
  - 在 Delegation 逻辑中: 构建 system prompt 时动态注入 P2P Supplement
  - 从 AgentMesh 获取可用 Agent 列表，渲染到模板中

验收标准:
  - 开启 P2P_COLLABORATION 后，子 Agent prompt 包含 P2P 工具描述
  - 可用 Agent 列表动态正确（已完成 Agent 显示为 "completed"）
  - 关闭 Flag 后，prompt 不受影响
  - Executor 类型 Agent 获得 p2p_query + p2p_delegate 工具
  - Explorer 类型 Agent（Repo-Agent）**仅获得 p2p_query**，不含 p2p_delegate
  - 其他 Agent 尝试 p2p_delegate 到 Repo-Agent 时被 Role 守卫拒绝
```

### 步骤 3: 结果压缩（半天）

```
目标: Conductor 只接收压缩摘要，完整结果存 SharedMemory

修改:
  - 创建 internal/agents/result_compressor.go
  - 在 Delegation 返回路径上集成 ResultCompressor
  - 在 P2P Supplement 中引导 Agent 返回结构化结果

验收标准:
  - 小结果（< 2000 字符）不压缩，直接返回
  - 大结果压缩为摘要，完整结果存 SharedMemory
  - Conductor 可通过 SharedMemory 路径获取完整结果
```

### 步骤 4: P2P Delegate 工具（半天）

```
目标: 子 Agent 可以委派子任务给其他 Agent

修改:
  - 创建 internal/agents/p2p_delegate.go
  - 注册 p2p_delegate 到 executor 的工具集中
  - 实现深度控制（context 传递）+ 循环检测

验收标准:
  - Coding-Agent 可 p2p_delegate 给 DevOps-Agent
  - 深度超过配置值时拒绝
  - 循环委派被检测并拒绝
  - p2p_query（已有工具）和使用 p2p_delegate 场景区分清晰
```

### 步骤 5: Observer 事件过滤（半天）

```
目标: 确保 P2P 通信事件不污染 Conductor context

修改:
  - 创建 internal/agents/observer_filter.go
  - 修改 Conductor 的事件回调，过滤 p2p_communication 事件
  - P2P 事件记录到 SharedMemory

验收标准:
  - P2P 通信发生时，Conductor context 不含 P2P 通信内容
  - SharedMemory 中有完整的 P2P 日志
  - Agent 注册/完成等非 P2P 事件仍然进入 Conductor context
```

### 步骤 6: Feature Flag 集成 + 测试（半天）

```
目标: 所有新行为可通过环境变量控制，默认行为不变

修改:
  - config.go: 增加 EnhancedCommanderConfig
  - 所有新增逻辑入口检查 Feature Flag
  - 编写单元测试 + 集成测试

验收标准:
  - 所有 Flag = OFF: 行为与当前完全一致
  - 逐个开启 Flag: 对应行为生效
  - 测试覆盖: 注册/注销、P2P 查询、委派深度、循环检测、压缩
```

**总计**: 约 3 天开发时间（相比 Facilitator 方案的 4 天减少 25%）

---

## 7. 安全机制与降级策略

### 7.1 安全矩阵

| 风险 | 防护机制 | 检测时机 | 响应行为 |
|------|----------|----------|----------|
| P2P 委派死循环 | delegation chain 检测 | p2p_delegate 调用时 | 返回循环错误，发起者自行处理 |
| 委派深度爆炸 | Context 深度计数 > MaxDepth | p2p_delegate 调用时 | 返回深度超限错误 |
| P2P 超时 | AgentPeer.Request 30s 超时 | 等待响应时 | 返回超时错误，日志记录 "timeout" |
| Agent 不可用 | AgentMesh 查不到目标 | p2p_query/delegate 时 | 降级：从 SharedMemory 读缓存 / 返回错误 |
| Conductor context 污染 | Observer Filter | P2P 事件发生时 | 默认不注入，仅记录到 SharedMemory |
| 大结果爆炸 | Result Compressor | 返回路径 | 大结果压缩为摘要，完整结果存 SharedMemory |
| 并发冲突 | SharedMemory MVCC | 写入时 | 版本号递增，无冲突写入 |

### 7.2 降级阶梯

```
层级 0: 全功能增强模式
  └─► 层级 1: p2p_delegate 失败 → 提示"自己处理或找 Conductor"
      └─► 层级 2: AgentMesh 不可用 → 降级为直通（不注册/不 P2P）
          └─► 层级 3: SharedMemory 不可用 → 降级为不压缩（完整结果返回）
              └─► 层级 4: 关闭所有 Flag → 完全回退到当前 Commander 模式
```

### 7.3 与服务降级的集成

```go
// P2P Delegate 失败时的处理建议（在工具返回值中体现）
func (t *P2PDelegateTool) Execute(ctx, params) (string, error) {
    // ...
    if err != nil {
        return fmt.Sprintf(
            `P2P delegate failed: %v
Suggestions:
1. Handle the subtask yourself if it's within your expertise
2. Ask the Conductor for assistance via your result summary
3. Check SharedMemory for cached results from the target agent`, err), nil
    }
    // ...
}
```

---

## 8. 验证策略

### 8.1 单元测试

| 组件 | 测试场景 | 预期 |
|------|----------|------|
| AgentMesh Register | Delegate 时自动注册 | Mesh 中有 Agent 记录 |
| AgentMesh TTL | 完成后 TTL 自动 evict | 超时后不可查 |
| AgentMesh Find | 查找 running/completed/evicted | 状态判断正确 |
| AgentMesh QueryAgent | P2P 直连 + SharedMemory 降级 | 降级路径返回 [Cached] 标注 |
| ResultCompressor | 大结果(>2000)压缩 | 摘要 < 500 字符，SharedMemory 有完整结果 |
| ResultCompressor | 小结果(<2000)不压缩 | 直接返回，无 SharedMemory 存储 |
| P2PDelegateTool | depth=0→1→2 正常 | 链式委派成功 |
| P2PDelegateTool | depth=3 (max=2) 拒绝 | 返回深度超限 |
| P2PDelegateTool | A→B→A 循环检测 | 返回循环错误 |
| ObserverFilter | P2P 事件 level=log | 不入 context，SharedMemory 有记录 |
| ObserverFilter | Agent 注册事件 | 正常进入 context |
| Feature Flag OFF | 所有 Flag 关闭 | 行为与当前完全一致 |

### 8.2 集成测试场景

```go
// 场景 1: 基本 P2P 查询
// delegate_repo → delegate_coding (coding p2p_query repo)
// 验证: Conductor context 不含 P2P 通信内容
// 验证: Coding-Agent 正确获取 Repo 信息
// 验证: SharedMemory 有完整 P2P 日志

// 场景 2: P2P 委派
// delegate_coding (coding p2p_delegate devops for tests)
// 验证: 委派深度正确追踪
// 验证: DevOps-Agent 正确执行并返回
// 验证: Conductor 只看到 Coding-Agent 最终摘要

// 场景 3: 缓存降级
// delegate_repo (完成, TTL 内) → delegate_coding (p2p_query repo)
// 验证: p2p_query 从 SharedMemory 返回缓存结果 (标注 [Cached])

// 场景 4: Agent 不可用
// delegate_repo (完成, TTL 过期) → delegate_coding (p2p_query repo)
// 验证: 返回 "agent unavailable"
// 验证: Coding-Agent 能优雅降级（不崩溃）

// 场景 5: 结果压缩
// 生成大结果 (>2000 chars) 的 delegation
// 验证: Conductor 收到摘要
// 验证: SharedMemory 有完整结果

// 场景 6: Feature Flag 回退
// 所有 Flag = OFF
// 验证: 行为与当前 Commander 模式完全一致

// 场景 7: 循环检测
// Agent A p2p_delegate B, B p2p_delegate A
// 验证: 第二次委派被拒绝

// 场景 8: 深度限制
// A → B → C → D (MAX_DEPTH=2)
// 验证: C → D 被拒绝

// 场景 9: Repo-Agent Role 约束 — 拒绝委派
// Coder p2p_delegate 到 Repo-Agent
// 验证: Role 守卫拒绝，返回 "cannot delegate to explorer"
// 验证: 错误信息引导使用 p2p_query

// 场景 10: Repo-Agent Role 约束 — 查询正常
// Coder p2p_query 到 Repo-Agent
// 验证: 查询正常执行，Repo-Agent 返回代码信息
// 验证: Repo-Agent 不改变代码环境

// 场景 11: Repo-Agent 无 delegate 工具
// 检查 Repo-Agent 的 system prompt
// 验证: prompt 中不包含 p2p_delegate 工具描述
// 验证: prompt 中包含 "read-only" 角色声明

// 场景 12: Delegation 候选列表过滤
// 获取 delegation candidates 列表
// 验证: 不包含 role=explorer 的 Agent
```

### 8.3 Context 压缩效果验证

```go
// 性能测试场景: 复杂任务
// 用户请求: "分析项目架构，添加日志中间件，编写测试，部署验证"
// 子任务: 4 个 (repo + coding + devops + test)
// 跨 Agent 依赖: 每个子任务依赖前一个的结果

// 预期结果:
// 当前模式: Conductor context ≈ 50K chars
// 增强模式: Conductor context ≈ 5K chars
// 压缩比: ~10x
```

---

## 9. 回滚计划

### 9.1 Feature Flag 回滚

```bash
# 即时回滚：关闭所有增强功能，立即恢复原始行为
export CODEACTOR_P2P_COLLABORATION=off
export CODEACTOR_RESULT_COMPRESSION=off
export CODEACTOR_OBSERVER_LEVEL=off

# 重启应用后，行为与当前完全一致
```

### 9.2 代码回滚

| 回滚方式 | 操作 | 复杂度 |
|----------|------|--------|
| Feature Flag OFF | 设置环境变量即可 | 🔵 秒级回滚 |
| Git revert commit | 回退新增/修改的文件 | 🟢 分钟级 |
| 删除新增文件 | 4 个新增文件可安全删除 | 🟢 安全删除 |

### 9.3 向前兼容承诺

- `delegate_repo`、`delegate_coding`、`delegate_chat` 等旧工具**保持可用且行为不变**
- 所有 Flags 默认 **OFF**，增强模式为 opt-in
- 新增组件是**惰性初始化**的——不被调用就不产生副作用
- 修改文件在 Flag=OFF 时走原有代码路径，**零行为差异**

---

## 10. 长期演进路径

| 阶段 | 目标 | 关键改动 | 时间线 |
|------|------|----------|--------|
| **Phase 1** 🎯 | 打破通信瓶颈，激活 P2P + 结果压缩 | 本方案全部内容（步骤 1-6） | 当前 |
| **Phase 2** | 智能协作策略 | 子 Agent 根据任务特征自动决定是否 P2P（而非总是问 Conductor） | Phase 1 + 1周 |
| **Phase 3** | 动态 Agent 组队 | Conductor 根据任务自动创建临时 agent team（利用 Meta-Agent） | Phase 2 + 2周 |
| **Phase 4** | 跨进程协作 | EventBus 升级为支持 Redis/NATS 后端 | Phase 3 + 4周 |
| **Phase 5** | 自适应委派优化 | 根据历史协作效果调整委派策略（Metrics 驱动） | Phase 4 + 8周 |

---

## 附录 A：设计决策记录

### ADR-1: 为什么选择增强型 Commander 而非 Facilitator？

| 维度 | Facilitator 方案 | 增强型 Commander ✅ |
|------|-----------------|-------------------|
| Conductor Prompt | 需要大幅修改 | **零改动** |
| Conductor 角色 | 退居二线（促进者） | **保留指挥官（全局管理）** |
| 新组件 | CapabilityRegistry + Blackboard + DelegationContext | **复用现有设施** |
| 工作量 | 4 天 | **3 天** |
| 风险 | 高（新模式） | **低（增量改进）** |
| 用户反馈匹配度 | ❌ 与需求冲突 | **✅ 完全匹配** |

### ADR-2: 为什么使用 SharedMemory 而非 Blackboard？

| 维度 | Blackboard（新组件） | SharedMemory（现有设施） ✅ |
|------|-------------------|--------------------------|
| 存储能力 | KV + 区域 + 订阅 | KV + MVCC + pub/sub |
| 开发成本 | 新文件 + 测试 | **零成本（已实现）** |
| 功能匹配度 | 过度设计（5区域+状态机） | **刚好够用（路径存储）** |
| 团队熟悉度 | 新概念 | **已存在** |

### ADR-3: 为什么不需要独立的 CapabilityRegistry？

AgentMesh 已管理 Agent 注册，只需增加 `capabilities` 字段即可。不需要独立组件。子 Agent 通过 P2P Supplement 被动获得可用 Agent 列表，而非主动搜索——这降低了 LLM 的认知负担（不需要学一个新工具）。

---

## 附录 B：关键假设

1. **所有 agent 运行在同一进程内**（EventBus 是进程内的）。如果需要跨进程，需要引入 Redis/NATS 作为 EventBus 后端。
2. **LLM 足够聪明能够正确使用 P2P 工具**（不过度委派也不不使用）。如果观察到"委派成瘾"现象，需要在 P2P Supplement 中加强"先自己尝试"的约束。
3. **SharedMemory 的内存增长可控**。P2P 日志和完整结果会在 TTL 后自动清理。
4. **sub-agent 的 system prompt 有足够 token 预算容纳 P2P Supplement**（约 600 tokens）。如果超限，需要采用"按需注入"策略。
5. **P2P 通信失败不会导致任务失败**。所有 P2P 工具调用都有降级路径，失败时子 Agent 可以自行处理或通过摘要告知 Conductor。

## 附录 C：术语表

| 术语 | 定义 |
|------|------|
| Conductor | 编排引擎，Commander 模式下负责任务分解→委派→验证→整合 |
| Sub-Agent | 专用 Agent（Repo/Coding/Chat/DevOps/Browser），执行具体任务 |
| P2P Sideband | P2P 旁路通道，子 Agent 间直连通信的通路 |
| P2P Supplement | 注入到子 Agent system prompt 末尾的协作能力描述模板 |
| Enhanced Commander | 保留 Commander 决策路径，增加 P2P 旁路通信通道的增强模式 |
| Commander Mode | 当前模式，Conductor 承担全部路由智能（默认，向后兼容） |
| Facilitator Mode | 被否决的方案（v1），Conductor 从指挥官变为促进者 |
| Delegation Layer | Conductor 和子 Agent 之间的基础设施层，自动处理注册/注入/压缩 |
| Result Compressor | 结果压缩器，将大结果压缩为摘要返回给 Conductor |
| Observer Filter | 事件过滤器，防止 P2P 通信事件污染 Conductor context |
| Explorer Agent / 探索型 Agent | `role=explorer` 的 Agent。仅响应 p2p_query，提供只读代码探索能力。不接受/不发起 p2p_delegate。代表：Repo-Agent。 |
| Executor Agent / 执行型 Agent | `role=executor` 的 Agent。可接受 p2p_delegate 承担子任务，也可向其他 executor 发起委派。代表：Coding-Agent, DevOps-Agent。 |
| Role Guard / 角色守卫 | p2p_delegate 入口处的前置校验，根据目标 Agent 的 role 字段拒绝委派给 explorer。 |
| Role-Based Injection / 角色化注入 | 根据 Agent 的 role 选择性注入 P2P 工具。Explorer 不注入 p2p_delegate。 |

---

> 本文档对应 codeactor-agent commit: 当前 HEAD
> 最后更新: 2025 年
> 版本: v2.0 — 从 Facilitator 模型调整为增强型 Commander 模式
