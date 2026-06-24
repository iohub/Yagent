# 分布式认知架构重构方案

> 基于对 codeactor-agent 现有代码（`internal/agents/conductor.go` 1518行、`internal/agents/conductor/` 子目录、`internal/agents/executor.go` 523行、`internal/messaging/bus/bus.go` 254行、`internal/messaging/peer/peer.go` 330行、`internal/memory/layered.go` 394行、`internal/memory/shared.go` 441行、`internal/agents/mesh.go` 88行、`internal/agents/p2p_integration.go` 230行、`internal/agents/router.go` 584行 等）的系统性审查编写。

---

## 目录

- [1. 现状分析](#1-现状分析)
- [2. 根本问题：中央智能体瓶颈](#2-根本问题中央智能体瓶颈)
- [3. 目标架构：混合促进者模型](#3-目标架构混合促进者模型)
- [4. 核心组件设计](#4-核心组件设计)
  - [4.1 CapabilityRegistry — 能力注册中心](#41-capabilityregistry--能力注册中心)
  - [4.2 Blackboard — 结构化黑板](#42-blackboard--结构化黑板)
  - [4.3 DelegationContext — 委派上下文与环检测](#43-delegationcontext--委派上下文与环检测)
  - [4.4 P2P 协作工具集](#44-p2p-协作工具集)
  - [4.5 Sub-Agent Prompt 增强](#45-sub-agent-prompt-增强)
  - [4.6 Facilitator Conductor](#46-facilitator-conductor)
- [5. 修改文件清单](#5-修改文件清单)
- [6. 实施步骤](#6-实施步骤)
- [7. 安全机制与降级策略](#7-安全机制与降级策略)
- [8. 验证策略](#8-验证策略)
- [9. 回滚计划](#9-回滚计划)
- [10. 长期演进路径](#10-长期演进路径)

---

## 1. 现状分析

### 1.1 现有架构概览

当前 CodeActor Agent 采用 **Hub-and-Spoke（中枢-辐条）** 架构，核心是一个 **ConductorAgent**（约 1518 行）协调多个专用子 Agent。

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

#### 重构中的子模块

`internal/agents/conductor/` 目录下已有重构版本，将 ConductorAgent 拆分为：

```
conductor/
├── conductor.go        # 顶层协调器，委托子组件
├── conductor.prompt.md # 系统提示词
├── memory_manager.go   # 记忆管理
├── planner.go          # 任务规划
├── router.go           # 子组件路由
├── recovery.go         # 错误恢复
├── metrics.go          # 指标收集
└── types.go            # 类型定义
```

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

---

## 2. 根本问题：中央智能体瓶颈

### 2.1 因果链

```
Capability Registry 缺失
    └─► agent 无法发现同伴能力
        └─► 所有跨 agent 协作必须经 Conductor
            └─► Conductor prompt 承载全部路由智能
                └─► 单次 LLM 调用 token 预算封顶
                    └─► 复杂任务分解质量下降
                        └─► sub-agent 只收到"窄指令"
                            └─► sub-agent 无需也无能力协作
                                └─► P2P 工具 + SharedMemory 成为死代码
```

### 2.2 证据

1. **ConductorAgent.Run() 方法 1518 行** — 同时承担 task intake、context gathering、planning、delegation、result integration、context compression、circuit breaker、error recovery。单一方法承担了 8 个职责。

2. **delegate_* 工具是唯一的 Agent 间通信通道** — 所有子 Agent 的输出必须流经 Conductor 的 LLM 才能到达另一个子 Agent。

3. **P2P 工具已注入但 prompt 未描述** — `internal/agents/executor.go` 的 `buildP2PTeamAdapters()` 函数创建了 `p2p_query` 和 `p2p_notify` 工具，但没有任何子 Agent 的 system prompt 描述这些工具的存在。LLM 不知道这些工具可用。

4. **SharedMemory 未被主动使用** — `LayeredMemory` 实现了 Local + Shared 合并，但 `AutoPromote` 策略仅在 `AddMessageWithPromote` 中触发，而子 Agent 执行循环（`RunAgentLoop`）调用的是 `AddMessage` 而非 `AddMessageWithPromote`。

### 2.3 问题总结

| 问题 | 严重程度 | 影响范围 |
|------|----------|----------|
| 中央智能体瓶颈 | 🔴 致命 | 系统整体智能被 Conductor prompt 上限封顶 |
| P2P 基础设施未激活 | 🟡 严重 | 已投入的开发资源浪费，子 Agent 无自主协作能力 |
| SharedMemory 协作语义缺失 | 🟡 严重 | 跨 Agent 信息共享需要显式编码而非自然涌现 |
| Conductor 职责过载 | 🟠 中等 | 单一文件 1518 行，难以测试和维护 |

---

## 3. 目标架构：混合促进者模型

### 3.1 核心转变

| 维度 | 旧模式 (Commander) | 新模式 (Facilitator) |
|------|--------------------|----------------------|
| Conductor 角色 | 指挥官：分解→指派→检查 | 促进者：播种→锚定→整合 |
| 协作方式 | 所有通信经 Conductor 中转 | Agent 间 P2P 直连 |
| 信息共享 | 通过 Conductor 的 memory 隐式传递 | 通过 Blackboard 显式发布/订阅 |
| 能力发现 | Conductor 硬编码路由 | CapabilityRegistry 动态搜索 |
| 智能分布 | 集中在 Conductor prompt | 分布在每个 Agent 的 LocalMemory + 工具集 |
| 系统瓶颈 | Conductor prompt token 上限 | 理论上 N × Agent prompt（N=Agent 数量） |

### 3.2 目标架构图

```
                              ┌─────────────────────┐
                              │       User          │
                              └──────────┬──────────┘
                                         │
                              ┌──────────▼──────────┐
                              │    Conductor         │
                              │    (Facilitator)     │
                              │  ┌───────────────┐   │
                              │  │ 1. Task Intake │   │  ← LLM call #1
                              │  │ 2. Seed Board  │   │
                              │  │ 3. Anchor Sel  │   │  ← LLM call #2
                              │  │ 4. Monitor     │   │  ← Event-driven (非 LLM)
                              │  │ 5. Integrate   │   │  ← LLM call #3
                              │  └───────────────┘   │
                              └──┬───────────────┬───┘
                                 │               │
                    ┌────────────▼──┐         ┌──▼────────────┐
                    │   Blackboard  │◄────────┤  Capability    │
                    │  (SharedMem)  │  查询   │  Registry      │
                    │               │         │               │
                    │ tasks/        │         │ agent_id→caps │
                    │ findings/     │         │ tag→agents    │
                    │ decisions/    │         │ text→agents   │
                    │ artifacts     │         │               │
                    └──────┬────────┘         └──┬────────────┘
                           │                     │ search
            ┌──────────────┼─────────────────────┼──────────────┐
            │              │                     │              │
       ┌────▼───┐    ┌────▼───┐           ┌────▼───┐    ┌────▼───┐
       │Agent A │◄──►│Agent B │◄─────────►│Agent C │◄──►│Agent D │
       │(anchor)│ P2P│(peer)  │    P2P    │(peer)  │ P2P│(peer)  │
       │  del   │    │  del   │   delegate│  del   │    │  del   │
       └────┬───┘    └────┬───┘           └────┬───┘    └────┬───┘
            │              │                    │              │
            └──────────────┴────────┬───────────┴──────────────┘
                                     │
                              ┌──────▼──────┐
                              │  AgentMesh  │
                              │  (P2P grid) │
                              │             │
                              │ Conductor = │
                              │   Observer  │
                              └─────────────┘
```

### 3.3 设计原则

1. **分布智能而非集中路由** — 每个子 Agent 拥有搜索能力、委派同伴、读写黑板的能力
2. **最小化 Conductor 干预** — Conductor 仅负责任务播种、锚点选择、最终整合，微观决策下放
3. **安全网先行** — DelegationContext 提供环检测 + 深度控制 + 超时控制，防止失控
4. **渐进迁移** — Commander 模式作为默认值保留，Facilitator 模式通过 feature flag 启用
5. **可观测性** — 所有 P2P 交互、黑板变更、委派事件都通过 EventBus Observer 通知 Conductor

---

## 4. 核心组件设计

### 4.1 CapabilityRegistry — 能力注册中心

**位置**: `internal/registry/capability_registry.go`（新增文件）

**定位**: 让子 Agent 能发现同伴的能力，而不是依赖 Conductor 的"全局路由表"。

#### 数据结构

```go
package registry

import (
    "sort"
    "strings"
    "sync"
    "time"
)

// AgentCapability 描述一个 agent 的能力
type AgentCapability struct {
    AgentID     string                 `json:"agent_id"`      // 唯一标识（如 "coding-agent"）
    Name        string                 `json:"name"`           // 人类可读名称（如 "Code Engineer"）
    Description string                 `json:"description"`   // 自然语言描述
    Tags        []string               `json:"tags"`          // 能力标签（如 ["go", "python", "testing"]）
    InputSchema map[string]interface{} `json:"input_schema,omitempty"`
    OutputSchema map[string]interface{} `json:"output_schema,omitempty"`
    Version     string                 `json:"version"`
    RegisteredAt time.Time             `json:"registered_at"`
}

// CapabilityQuery 查询条件
type CapabilityQuery struct {
    Text  string   // 自然语言描述（关键词匹配，可升级为向量搜索）
    Tags  []string // 标签过滤
    Name  string   // 精确名称匹配
    Limit int      // 默认 10
}

// ChangeHandler 能力变更回调
type ChangeHandler func(CapabilityEvent)

type CapabilityEvent struct {
    Type       string          // "registered" | "unregistered" | "updated"
    Capability AgentCapability
    Timestamp  time.Time
}
```

#### 核心接口

```go
type CapabilityRegistry interface {
    // Register 注册/更新一个 agent 的能力
    Register(cap AgentCapability) error
    // Unregister 注销一个 agent 的所有能力
    Unregister(agentID string) error
    // Get 获取指定 agent 的能力
    Get(agentID string) (AgentCapability, bool)
    // Search 搜索能力 — 按标签匹配数排序 + 文本关键词评分
    Search(query CapabilityQuery) ([]AgentCapability, error)
    // List 列出所有已注册能力
    List() []AgentCapability
    // SubscribeChanges 订阅能力变更
    SubscribeChanges(handler ChangeHandler) (unsubscribe func())
}
```

#### 标签索引实现

```go
type capabilityRegistry struct {
    mu        sync.RWMutex
    caps      map[string]AgentCapability          // agentID → capability
    tagIndex  map[string]map[string]bool          // tag → set of agentIDs
    handlers  []ChangeHandler
    handlersMu sync.RWMutex
}

func (r *capabilityRegistry) Search(query CapabilityQuery) ([]AgentCapability, error) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    // 1. Name 精确匹配（最高优先级）
    // 2. Tags 过滤 + 按匹配数排序
    // 3. Text 关键词评分（name=10分, description=5分, tag=3分）
    // 4. 无条件返回全部（截断到 Limit）
}
```

#### 与现有 AgentMesh 的集成

```go
// internal/mesh/agent_mesh.go — 修改 RegisterAgent
func (m *AgentMesh) RegisterAgent(peer *AgentPeer, caps AgentCapability) error {
    // 1. 注册 P2P 通道（已有逻辑）
    // 2. 自动注册能力到 CapabilityRegistry
    if err := m.registry.Register(caps); err != nil {
        return err
    }
    // 3. 广播能力注册事件（Conductor 作为 Observer 会收到）
    m.eventBus.Publish("mesh.agent.registered", caps)
    return nil
}
```

---

### 4.2 Blackboard — 结构化黑板

**位置**: `internal/memory/blackboard.go`（新增文件）

**定位**: 在 SharedMemory 的 MVCC 基础上构建带语义区域的结构化协作空间。

#### 设计动机

现有的 `SharedMemory` 是一个扁平的 KV 存储——Agent 不知道该写什么、读什么，因为没有语义结构。Blackboard 引入"区域"概念，让 Agent 的协作信息有结构化的流转通道。

#### 数据结构

```go
package memory

// BlackboardRegion 黑板区域
type BlackboardRegion string

const (
    RegionTasks     BlackboardRegion = "tasks"      // 任务定义与子任务
    RegionFindings  BlackboardRegion = "findings"   // 中间发现/分析结果
    RegionDecisions BlackboardRegion = "decisions"  // 设计决策与取舍
    RegionQuestions BlackboardRegion = "questions"  // 待回答问题
    RegionArtifacts BlackboardRegion = "artifacts"  // 最终产物
)

// BlackboardEntry 黑板条目
type BlackboardEntry struct {
    ID         string                 `json:"id"`          // 唯一标识（"bb-{seq}"）
    Region     BlackboardRegion       `json:"region"`      // 所属区域
    Author     string                 `json:"author"`      // 发布者 agent ID
    Content    map[string]interface{} `json:"content"`     // 结构化内容
    Tags       []string               `json:"tags"`        // 可发现性标签
    References []string               `json:"references"`  // 引用的其他 entry ID
    Status     string                 `json:"status"`      // draft|committed|superseded|closed
    Version    int                    `json:"version"`     // MVCC 版本号
    CreatedAt  time.Time              `json:"created_at"`
    UpdatedAt  time.Time              `json:"updated_at"`
}

// BlackboardFilter 读取过滤器
type BlackboardFilter struct {
    Tags    []string
    Author  string
    Status  string
    Since   time.Time
    Limit   int       // 默认 50
}

// BlackboardEvent 黑板事件（用于订阅通知）
type BlackboardEvent struct {
    Type   string          // "posted" | "updated" | "status_changed"
    Entry  BlackboardEntry
}
```

#### 核心接口

```go
type Blackboard interface {
    // Post 发布条目到指定区域
    Post(region BlackboardRegion, author string, content map[string]interface{},
         tags []string, references []string) (string, error)
    // Read 从指定区域读取条目（按时间倒序）
    Read(region BlackboardRegion, filter BlackboardFilter) ([]BlackboardEntry, error)
    // Get 按 ID 获取条目
    Get(entryID string) (BlackboardEntry, bool)
    // Update 更新条目内容（MVCC 乐观锁：版本递增）
    Update(entryID string, author string, content map[string]interface{}) error
    // SetStatus 更新条目状态
    SetStatus(entryID string, author string, status string) error
    // Subscribe 订阅区域变更（用于 Conductor 监控和 Agent 间通知）
    Subscribe(region BlackboardRegion, handler func(BlackboardEvent)) func()
    // Snapshot 导出全量快照（用于调试和可观测性）
    Snapshot() map[BlackboardRegion][]BlackboardEntry
}
```

#### 内部实现要点

```go
type blackboard struct {
    mu          sync.RWMutex
    entries     map[string]BlackboardEntry               // entryID → entry
    regionIndex map[BlackboardRegion][]string            // region → entryIDs (时间序)
    tagIndex    map[string]map[string]bool               // tag → entryIDs
    subscribers map[BlackboardRegion][]func(BlackboardEvent)
    idCounter   int64
}
```

**MVCC 乐观更新**: 每次 `Update` 递增 `Version`，调用者需在 Read 后比对版本。

**订阅通知**: 当新条目发布时，异步通知该区域的所有订阅者。Conductor 可订阅 `RegionArtifacts` 感知任务完成。

---

### 4.3 DelegationContext — 委派上下文与环检测

**位置**: `internal/mesh/delegation.go`（新增文件）

**定位**: 随 P2P 消息传播的安全上下文，提供环检测、深度控制、超时控制。

#### 数据结构

```go
package mesh

type DelegationContext struct {
    TaskID       string         `json:"task_id"`        // 关联的黑板任务 entry ID
    InitiatorID  string         `json:"initiator_id"`   // 任务发起者（通常是 anchor agent）
    Chain        []string       `json:"chain"`          // 委派链 [anchor, B, C, ...]
    Depth        int            `json:"depth"`          // 当前深度
    MaxDepth     int            `json:"max_depth"`      // 最大深度（默认 4）
    Visited      map[string]int `json:"visited"`        // agentID → 被访问次数
    Deadline     time.Time      `json:"deadline"`       // 截止时间（默认 120s）
    BlackboardID string         `json:"blackboard_id"`  // 黑板关联条目
}
```

#### 核心逻辑

```go
func NewDelegationContext(initiatorID, taskID, blackboardID string) *DelegationContext {
    return &DelegationContext{
        TaskID:       taskID,
        InitiatorID:  initiatorID,
        Chain:        []string{initiatorID},
        Depth:        0,
        MaxDepth:     4,
        Visited:      map[string]int{initiatorID: 1},
        Deadline:     time.Now().Add(120 * time.Second),
        BlackboardID: blackboardID,
    }
}

func (dc *DelegationContext) CanDelegateTo(targetID, fromID string) error {
    if targetID == fromID {
        return ErrSelfDelegation          // 不能委派给自己
    }
    if dc.Depth >= dc.MaxDepth {
        return ErrMaxDepthExceeded        // 委派链过长
    }
    if dc.Visited[targetID] >= 2 {
        return ErrCycleDetected           // 形成循环
    }
    if time.Now().After(dc.Deadline) {
        return ErrDeadlineExceeded        // 超时
    }
    return nil
}

// Fork 为目标 agent 创建子上下文（Depth+1, Chain追加, Visited递增）
func (dc *DelegationContext) Fork(targetID string) *DelegationContext {
    // 深拷贝 Visited + 追加 Chain
    return &DelegationContext{...}
}
```

#### 规则矩阵

| 场景 | 结果 | 说明 |
|------|------|------|
| A → B (A≠B, depth=0) | ✅ 允许 | 首次委派 |
| A → B → C (depth=1→2) | ✅ 允许 | 链式委派 |
| A → B → A (visited[A]=1) | ✅ 允许 | 合法：B 反馈给 A |
| A → B → A → B (visited[A]=2) | ❌ ErrCycleDetected | 循环检测触发 |
| depth=4 时试图 depth=5 | ❌ ErrMaxDepthExceeded | 深度限制 |
| 超时后委派 | ❌ ErrDeadlineExceeded | 时间预算耗尽 |
| A → A | ❌ ErrSelfDelegation | 不能委派给自己 |

---

### 4.4 P2P 协作工具集

#### 4.4.1 capability_search 工具

**位置**: `internal/tools/capability_search.go`（新增）

**功能**: 让子 Agent 搜索同伴的能力。

```go
// LLM 看到的工具描述
const CapabilitySearchDesc = `Search for other agents that can help with a specific task.
Returns a list of agents with their IDs, names, descriptions, and capability tags.
ALWAYS call this before attempting a task that might be outside your core expertise.`

// 使用示例（LLM 视角）
// capability_search(query="I need help analyzing python test coverage")
// → 返回: Found 2 agent(s):
//   1. Code Analyst (id: repo-agent) — Can analyze code structure...
//   2. DevOps Engineer (id: devops-agent) — Can run shell commands...
//   Use p2p_delegate with the agent_id to request help.
```

#### 4.4.2 p2p_delegate 工具

**位置**: `internal/tools/p2p_delegate.go`（新增）

**功能**: 让子 Agent 直接委派子任务给同伴，绕开 Conductor。

```go
// LLM 看到的工具描述
const P2PDelegateDesc = `Delegate a subtask to another agent via P2P communication.
The target agent will receive your request and return a response.
Use capability_search first to find the right agent.
IMPORTANT:
- Always search for capabilities first to find the best agent
- Provide clear, self-contained task descriptions
- Include relevant context (file paths, previous findings, etc.)`

// 执行流程
func (t *P2PDelegateTool) Execute(ctx, params) (ToolResult, error) {
    // 1. 获取当前 DelegationContext（从 ctx 中提取，由 Executor 注入）
    // 2. CanDelegateTo 检查（环检测 + 深度 + 超时）
    // 3. 在 Blackboard 记录委派事件（decisions 区域）
    // 4. 通过 AgentMesh 发送 P2P 请求并等待响应
    // 5. 返回目标 Agent 的结果
}
```

#### 4.4.3 blackboard_read / blackboard_post 工具

**位置**: `internal/tools/blackboard_tools.go`（新增）

**功能**: 让子 Agent 读写黑板，实现异步协作。

```go
// LLM 看到的 blackboard_read 描述
const BlackboardReadDesc = `Read entries from the shared blackboard.
Regions:
- "tasks": task definitions and subtasks
- "findings": intermediate results and discoveries
- "decisions": decisions made by agents
- "questions": open questions waiting for answers
- "artifacts": final deliverables
ALWAYS check the blackboard before starting work.`

// LLM 看到的 blackboard_post 描述
const BlackboardPostDesc = `Post a finding, decision, or question to the shared blackboard.
Other agents will be able to read this and build on your work.
Be concise but complete — include enough context for another agent to understand.`
```

#### 4.4.4 工具注入机制

**修改 `internal/agents/executor.go`** 的 `RunAgentLoop`：

```go
// SubAgentToolBundle 协作工具集
type SubAgentToolBundle struct {
    CapabilitySearch  Tool
    P2PDelegate       Tool
    P2PQuery          Tool   // 已有
    P2PNotify         Tool   // 已有
    BlackboardRead    Tool
    BlackboardPost    Tool
}

func (b *SubAgentToolBundle) InjectIntoExecutor(cfg *ExecutorConfig) {
    // 1. 注入所有 P2P 协作工具到 cfg.Adapters
    // 2. 追加 CollaborationPromptFragment 到 system prompt
}
```

---

### 4.5 Sub-Agent Prompt 增强

**位置**: `internal/prompts/subagent_collaboration.go`（新增）

**说明**: 在每个子 Agent 的 system prompt 末尾追加协作能力描述，让 LLM 知道新工具的存在。

```markdown
## Collaboration Capabilities

You are part of a multi-agent team. You have direct P2P communication with other agents
and access to a shared blackboard.

### Available Collaboration Tools

1. **capability_search**: Find other agents with specific skills.
2. **p2p_delegate**: Ask another agent to handle a subtask.
3. **blackboard_read**: Read what other agents have discovered.
4. **blackboard_post**: Share your findings with other agents.
5. **p2p_query**: Ask a quick question to another agent.
6. **p2p_notify**: Broadcast a notification to all agents.

### Collaboration Protocol

1. **Before starting**: Read "tasks" and "findings" regions of the blackboard.
2. **During work**: Post important findings to "findings" region.
3. **When stuck**: Search for capabilities, then delegate or query.
4. **When done**: Post your output to "artifacts" region.

### Delegation Safety

- Max delegation depth: 4 levels
- Cycle detection is active — circular delegations will be rejected
- Always try to handle tasks yourself first if they're within your expertise
```

**Token 预算**: 全文约 800 tokens，在现有 sub-agent prompt（约 2000-3000 tokens）的预算内。

---

### 4.6 Facilitator Conductor

**位置**: `internal/prompts/conductor_facilitator.go`（新增）+ `internal/agents/conductor.go`（修改）

#### 新 Conductor Prompt

```markdown
You are the **Conductor** — a facilitator and coordinator for a team of AI agents.

## Your Role

You are NOT a commander who micro-manages. You are a **facilitator** who:
1. Understands the user's goal
2. Seeds the blackboard with initial task context
3. Selects an "anchor agent" best suited to start the work
4. Monitors progress via the blackboard
5. Integrates final results
```

#### 新编排循环

```go
func (c *ConductorAgent) RunFacilitatorCycle(ctx context.Context, userInput string) (string, error) {
    // ── Phase 1: Task Intake (LLM call #1) ──
    // 理解用户目标、约束、成功标准
    // 输出: taskAnalysis (Goal, SuccessCriteria, Constraints, Context)
    
    // ── Phase 2: Blackboard Seeding (无 LLM) ──
    // 在黑板 "tasks" 区域播种任务定义
    taskEntryID, _ := c.board.Post(RegionTasks, "conductor", taskAnalysis, 
        []string{"root-task"}, nil)
    
    // ── Phase 3: Anchor Selection (LLM call #2) ──
    // 搜索 CapabilityRegistry，选择最适合的锚点 Agent
    caps := c.registry.Search(CapabilityQuery{Text: taskAnalysis.Goal})
    anchorID := c.selectAnchor(ctx, taskAnalysis, caps)
    
    // ── Phase 4: Delegate to Anchor (无 LLM) ──
    // 创建 DelegationContext，委派给锚点 Agent
    delCtx := NewDelegationContext(anchorID, taskEntryID, taskEntryID)
    c.delegateToAnchor(ctx, anchorID, taskAnalysis, delCtx, taskEntryID)
    
    // ── Phase 5: Progress Monitoring (事件驱动, 非 LLM) ──
    // 订阅黑板变更，等待 artifact 出现
    artifacts := c.waitForArtifacts(ctx, taskEntryID, 180*time.Second)
    
    // ── Phase 6: Final Integration (LLM call #3) ──
    // 读取黑板上的所有发现和产物，整合为最终响应
    return c.integrate(ctx, userInput, artifacts)
}
```

#### ConductorMode 切换

```go
type ConductorMode string

const (
    ModeCommander   ConductorMode = "commander"    // 旧模式（默认，向后兼容）
    ModeFacilitator ConductorMode = "facilitator"  // 新模式
)

// 通过环境变量控制
func GetConductorMode() ConductorMode {
    if os.Getenv("CONDUCTOR_MODE") == "facilitator" {
        return ModeFacilitator
    }
    return ModeCommander
}
```

---

## 5. 修改文件清单

### 5.1 新增文件（11 个）

| # | 路径 | 说明 | 依赖 |
|---|------|------|------|
| 1 | `internal/registry/capability_registry.go` | 能力注册中心（含标签索引、文本搜索、变更订阅） | 无 |
| 2 | `internal/registry/capability_registry_test.go` | 并发安全测试、标签索引测试、搜索排序测试 | 1 |
| 3 | `internal/memory/blackboard.go` | 黑板数据结构（5 区域、MVCC、订阅通知） | 无 |
| 4 | `internal/memory/blackboard_test.go` | MVCC 版本测试、区域过滤测试、并发测试 | 3 |
| 5 | `internal/mesh/delegation.go` | 委派上下文（环检测、深度控制、超时） | 无 |
| 6 | `internal/mesh/delegation_test.go` | 4 种场景的环检测测试、深度测试 | 5 |
| 7 | `internal/tools/capability_search.go` | capability_search 工具实现 | 1 |
| 8 | `internal/tools/p2p_delegate.go` | p2p_delegate 工具实现（含 DelegationContext 集成） | 1,3,5 |
| 9 | `internal/tools/blackboard_tools.go` | blackboard_read + blackboard_post 工具实现 | 3 |
| 10 | `internal/prompts/subagent_collaboration.go` | 子 Agent 协作 prompt 片段（~800 tokens） | 无 |
| 11 | `internal/prompts/conductor_facilitator.go` | Conductor Facilitator 模式 prompt | 无 |

### 5.2 修改文件（7 个）

| # | 路径 | 修改内容 | 风险 |
|---|------|----------|------|
| 12 | `internal/agents/executor.go` | 注入 SubAgentToolBundle + CollaborationPromptFragment | Medium |
| 13 | `internal/agents/conductor.go` | 增加 Facilitator 模式 + RunFacilitatorCycle | High |
| 14 | `internal/mesh/agent_mesh.go` | RegisterAgent 自动注册能力到 CapabilityRegistry | Low |
| 15 | `internal/messaging/peer/peer.go` | 携带 AgentCapability 信息（可选） | Low |
| 16 | `internal/agents/meta.go` | 新 Agent 自动注册能力 | Low |
| 17 | `internal/agents/p2p_integration.go` | 扩展默认能力注册（Repos/Coding） | Low |
| 18 | `internal/agents/conductor.prompt.md` | 切换为 Facilitator prompt（通过 ConductorMode 控制） | High |

### 5.3 无需修改的文件

| 路径 | 原因 |
|------|------|
| `internal/messaging/bus/bus.go` | EventBus 功能完备，直接复用 |
| `internal/memory/local.go` | LocalMemory 功能完备 |
| `internal/memory/shared.go` | SharedMemory MVCC 功能完备 |
| `internal/memory/layered.go` | LayeredMemory 的 AutoPromote 将在后续阶段启用 |
| `internal/memory/memory.go` | ConversationMemory 不变 |
| `internal/messaging/peer/peer.go` | AgentPeer 的 Request/Response 功能完备 |

---

## 6. 实施步骤

### 步骤 1: CapabilityRegistry（半天）

```
实现 → 单元测试 → 集成到 AgentMesh
验收标准: 并发读写无 race，标签索引搜索正确
```

### 步骤 2: Blackboard（半天）

```
实现 → 单元测试 → 验证 MVCC 版本递增
验收标准: 5 区域隔离，订阅通知正常，Snapshot 可导出
```

### 步骤 3: DelegationContext（半天）

```
实现 → 单元测试 → 验证 4 种拦截场景
验收标准: 环检测 100% 拦截，深度限制精确，超时触发
```

### 步骤 4: P2P 协作工具（1 天）

```
实现 capability_search → blackboard_read/post → p2p_delegate
验收标准: 工具返回值格式正确，能与 Blackboard/Registry 正确交互
```

### 步骤 5: Executor 注入（半天）

```
实现 SubAgentToolBundle → 修改 RunAgentLoop → 编写 CollaborationPromptFragment
验收标准: sub-agent system prompt 包含协作工具描述，工具可用
```

### 步骤 6: Facilitator Conductor（1 天）

```
编写新 prompt → 实现 RunFacilitatorCycle → 添加 ConductorMode 开关
验收标准: 新旧模式可切换，Facilitator 模式完成端到端流程
```

### 步骤 7: 端到端测试（半天）

```
编写集成测试场景 → 验证 3-agent 协作 → 验证回滚路径
验收标准: 全部测试场景通过
```

**总计**: 约 4 天开发时间

---

## 7. 安全机制与降级策略

### 7.1 安全矩阵

| 风险 | 防护机制 | 检测时机 | 响应行为 |
|------|----------|----------|----------|
| 循环委派 | DelegationContext.Visited ≥ 2 | p2p_delegate 调用时 | 返回错误信息，发起者自己处理或找不同 Agent |
| 委派深度爆炸 | DelegationContext.MaxDepth=4 | p2p_delegate 调用时 | 返回 ErrMaxDepthExceeded |
| P2P 超时 | 默认 120s timeout | AgentPeer.Request 等待时 | 返回超时错误，黑板记录 "failed" |
| Agent 崩溃 | 目标无响应 | timeout 触发 | 发起者收到错误，黑板记录 "agent_crash" |
| Conductor 单点故障 | ConductorMode=commander | 启动时检测 | 回退到旧模式 |
| 黑板内存溢出 | 无（需后续增加 TTL） | — | Phase 2 增加自动清理 |

### 7.2 降级阶梯

```
层级 0: Facilitator 模式（全功能）
  └─► 层级 1: p2p_delegate 失败 → 提示发起者"自己处理或问 Conductor"
      └─► 层级 2: Blackboard 不可用 → 降级为纯 P2P（跳过黑板记录）
          └─► 层级 3: CapabilityRegistry 不可用 → 降级为硬编码能力列表
              └─► 层级 4: ConductorMode=commander → 完全回退到旧模式
```

---

## 8. 验证策略

### 8.1 单元测试

| 组件 | 测试场景 | 预期 |
|------|----------|------|
| CapabilityRegistry | 100 goroutines 并发 Register | 无 data race，数据一致 |
| CapabilityRegistry | Search by tags 多标签匹配 | 按匹配数降序排序 |
| CapabilityRegistry | Unregister 后 Search | 不返回已注销 agent |
| Blackboard | Post → Read → Update → Status | 版本递增，状态变更 |
| Blackboard | Subscribe → Post → 收到事件 | 事件类型正确 |
| Blackboard | 并发 Update 同一 entry | MVCC 无冲突 |
| DelegationContext | A→B→A（2nd visit） | ✅ 允许（visited=2） |
| DelegationContext | A→B→A→B（3rd visit） | ❌ ErrCycleDetected |
| DelegationContext | depth=5 时委派 | ❌ ErrMaxDepthExceeded |
| DelegationContext | 超时后续操作 | ❌ ErrDeadlineExceeded |

### 8.2 集成测试场景

```go
// 场景 1: 线性委派 (A→B→C)
// 验证: 3 层委派成功，黑板上有 3 条 delegation 记录

// 场景 2: 循环委派拦截 (A→B→C→A)
// 验证: 第 4 步被环检测拦截，A 收到错误并自行处理

// 场景 3: 黑板协作
// A posts finding → B reads it → B posts decision → C reads → C posts artifact
// 验证: artifact 包含 A 的 finding 和 B 的 decision

// 场景 4: Facilitator 端到端
// User → Conductor seeds → selects anchor A → A delegates to B → B posts artifact
// → Conductor integrates
// 验证: Conductor 只做 3 次 LLM call (intake, select, integrate)

// 场景 5: 超时降级
// A delegates to B, B hangs → A receives timeout → A handles task itself

// 场景 6: 向后兼容
// Conductor mode=commander → 使用旧 delegate_* 工具 → 行为一致
```

---

## 9. 回滚计划

### 9.1 Feature Flag

```go
// 通过环境变量控制所有新模式特性
const (
    EnvConductorMode       = "CONDUCTOR_MODE"          // "facilitator" | "commander"
    EnvEnableP2PDelegate   = "ENABLE_P2P_DELEGATE"     // "true" | "false"
    EnvEnableBlackboard    = "ENABLE_BLACKBOARD"        // "true" | "false"
    EnvEnableCapRegistry   = "ENABLE_CAPABILITY_REGISTRY" // "true" | "false"
)

// 默认全部关闭，确保零影响
func isEnabled(key string) bool {
    return os.Getenv(key) == "true"
}
```

### 9.2 回滚步骤

1. **设置 `CONDUCTOR_MODE=commander`** → Conductor 回到旧流程
2. **设置 `ENABLE_P2P_DELEGATE=false`** → sub-agent 不注入 p2p_delegate
3. **设置 `ENABLE_BLACKBOARD=false`** → Blackboard 不可用（惰性组件，无副作用）
4. **设置 `ENABLE_CAPABILITY_REGISTRY=false`** → CapabilityRegistry 不可用
5. 若完全回滚：**删除或注释新增文件的引用**
6. **验证旧工作流**：运行存量测试套件

### 9.3 向前兼容承诺

- `delegate_repo`、`delegate_coding`、`delegate_chat` 等旧工具保持可用
- `Commander` 模式下 Conductor 行为 **完全不变**
- 新增组件（CapabilityRegistry、Blackboard）是**惰性初始化**的——不被调用就不产生副作用
- `SubAgentToolBundle` 只在 `ENABLE_P2P_DELEGATE=true` 时注入

---

## 10. 长期演进路径

| 阶段 | 目标 | 关键改动 | 时间线 |
|------|------|----------|--------|
| **Phase 1** 🎯 | 打破瓶颈，启用 P2P + Blackboard | 本方案全部内容（步骤 1-7） | 当前 |
| **Phase 2** | 语义能力搜索 + 黑板 TTL | CapabilityRegistry 接入 embedding 向量搜索；Blackboard 添加 TTL 自动清理 | Phase 1 + 1周 |
| **Phase 3** | 动态 Agent 组队 | Conductor 根据任务自动创建临时 agent team | Phase 2 + 2周 |
| **Phase 4** | 跨进程协作 | EventBus 升级为支持 Redis/NATS 后端 | Phase 3 + 4周 |
| **Phase 5** | 自适应协作策略 | Agent 根据历史协作效果调整委派策略（RL 驱动） | Phase 4 + 8周 |

---

## 附录 A：关键假设

1. **所有 agent 运行在同一进程内**（EventBus 是进程内的）。如果需要跨进程，需要引入 Redis/NATS 作为 EventBus 后端。
2. **agent 数量 ≤ 50**。如果超过，CapabilityRegistry 的文本搜索需要升级为向量搜索。
3. **LLM 足够聪明能够正确使用 p2p_delegate**（不过度委派）。如果观察到"委派成瘾"现象，需要在 prompt 中加强"先自己尝试"的约束。
4. **Blackboard 的内存增长可控**。如果任务量大，Phase 2 需要添加 TTL 自动清理机制。
5. **sub-agent 的 system prompt 有足够 token 预算容纳协作工具描述**（约 800 tokens）。如果超限，需要采用"按需注入"策略。

## 附录 B：术语表

| 术语 | 定义 |
|------|------|
| Conductor | 编排引擎，Facilitator 模式下负责任务播种、锚点选择、最终整合 |
| Sub-Agent | 专用 Agent（Repo/Coding/Chat/DevOps/Browser），执行具体任务 |
| Anchor Agent | 被 Conductor 选中的"锚点"Agent，负责起始任务并可通过 P2P 委派同伴 |
| CapabilityRegistry | 能力注册中心，Agent 注册/发现彼此的能力 |
| Blackboard | 结构化共享工作空间，5 区域（tasks/findings/decisions/questions/artifacts） |
| DelegationContext | 委派安全上下文，含环检测、深度控制、超时控制 |
| P2P Delegate | 子 Agent 间直接的任务委派，绕过 Conductor |
| Commander Mode | 旧模式，Conductor 承担全部路由智能 |
| Facilitator Mode | 新模式，Conductor 仅做高层协调 |

---

> 本文档对应 codeactor-agent commit: 当前 HEAD
> 最后更新: 2025 年
