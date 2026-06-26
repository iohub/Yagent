# Agent 委派与通讯机制重新设计

> **文档版本**：v2.0
> **设计目标**：简约、易维护、不过度设计
> **核心改进**：从纯星型架构升级为 DAG 委派图架构，支持子 Agent 间委派，解决上下文爆炸
> **适用范围**：codeactor-agent 项目

---

## 一、背景与问题

### 1.1 项目概述

codeactor-agent 是一个基于 Go 的多 Agent 协作系统，核心是 **Conductor（主协调器）** 模式：Conductor 接收用户请求，通过 `delegate_*` 工具将子任务委派给专业化子 Agent（Repo/Coding/Chat/Meta/DevOps/Browser），并支持 Meta-Agent 动态设计新 Agent。

### 1.2 核心问题

#### 问题一：过度设计（~1840 行死代码）

经过对代码库的深入分析，发现当前架构存在严重的过度设计——在星型委派架构已满足需求的情况下，平行建设了一套从未真正使用的去中心化通讯基础设施：

| 问题 | 严重程度 | 详情 |
|------|---------|------|
| P2P 通讯机制完全死代码 | 🔴 严重 | `InitAgentMeshP2P` 从未被调用，符号表 P2P（120+行）零调用 |
| 黑板系统过度复杂 | 🟡 中等 | 701 行代码，含 MVCC+事件订阅，但 LLM 极少使用 |
| 分层记忆未生效 | 🟡 中等 | 3 层架构，AutoPromote 从未触发，多个方法是死代码 |
| conductor/ 未完成重构 | 🔴 严重 | planner.go/router.go/memory_manager.go 共 425 行零引用 |
| DelegationContext 无用 | 🔴 严重 | 314 行 + 380 行测试，依赖的 p2p_delegate 几乎未被使用 |

#### 问题二：纯星型架构的上下文爆炸

第一版重设计采用纯星型架构（所有委派都走 Conductor），但这会导致**上下文爆炸**：

```
场景：Coding Agent 编码时需要先探索代码库

纯星型架构（上下文爆炸）：
  Coding Agent → (退回 Conductor) → Conductor 上下文 +1
  → delegate_repo → Repo Agent 探索 → (退回 Conductor) → Conductor 上下文 +1
  → (重新委派 Coding) → Coding Agent 继续

  Conductor 上下文被污染：Repo 的探索结果、Coding 的中间状态全部堆积
  随着子 Agent 交互增多，Conductor 上下文线性膨胀
```

### 1.3 根因分析

```
问题一根因：过早抽象
  预设了去中心化多 Agent 协作场景 → 同时实现两套通讯路径
  ├─ 路径A：Conductor → delegate_* → subAgent.Run()（实际在用）
  └─ 路径B：P2P Mesh + 黑板 + DelegationContext（从未真正使用）
  → ~1840 行死代码

问题二根因：委派路径与上下文传播路径耦合
  全走 Conductor → 所有中间结果都进 Conductor 上下文 → 上下文爆炸

核心洞察：
  问题不在于"子 Agent 能否委派"，而在于"如何结构性地控制委派权限，
  使递归不可能发生，而非运行时检测"
```

---

## 二、设计目标与原则

### 2.1 设计目标

1. **简约**：用最少的机制满足需求，消除所有死代码
2. **子 Agent 间可委派**：Coding Agent 能直接委派 Repo Agent，不退回 Conductor
3. **上下文隔离**：子 Agent 委派的结果留在子 Agent 自己的上下文，不冒泡到 Conductor
4. **Repo Agent 是终结点**：不能委派其他 Agent
5. **易维护**：架构清晰，新人能在 30 分钟内理解全貌

### 2.2 设计原则

| 原则 | 含义 | 应用 |
|------|------|------|
| **宁缺毋滥** | 不确定是否需要的机制一律不加 | 删除 P2P/黑板/DelegationContext |
| **结构性防递归** | 用 DAG 结构保证无环，而非运行时检测 | 静态委派图 + 启动时验证 |
| **上下文天然隔离** | 委派结果 = tool_result，留在调用者上下文 | 不需要额外的隔离机制 |
| **统一抽象** | 内置 Agent 和 Custom Agent 用同一套机制 | 统一 Agent 接口 + Registry |
| **最小接口** | Agent 接口尽可能简单 | 3 方法接口：Name/Description/Run |
| **显式优于隐式** | 委派关系清晰可见 | 委派权限在一个 map 中定义 |

### 2.3 非目标（明确不做的事）

- ❌ 不实现 Agent 间 P2P 直接通信
- ❌ 不实现共享状态（黑板）
- ❌ 不实现运行时递归检测（用结构性 DAG 替代）
- ❌ 不重写 RunAgentLoop 执行引擎
- ❌ 不改动 tools.Adapter 工具系统基础

---

## 三、当前架构分析

### 3.1 当前架构全景

```
┌─────────────────────────────────────────────────────────────────┐
│                      ConductorAgent (1678行)                     │
│                                                                 │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │                    Adapters List                          │  │
│  │  delegate_repo | delegate_coding | delegate_chat         │  │
│  │  delegate_meta | delegate_devops | delegate_browser      │  │
│  │  delegate_<custom> ...                                    │  │
│  │  + blackboard_read/post + p2p_delegate (几乎未用)         │  │
│  └──────────────────────────┬────────────────────────────────┘  │
│  ┌──────────────────────────▼────────────────────────────────┐  │
│  │              AgentMesh (P2P 网格，未真正使用)               │  │
│  │  EventBus ←→ AgentPeer ←→ Blackboard ←→ LayeredMemory    │  │
│  └───────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────┘
         │                        │
         ▼                        ▼
  ┌──────────────┐        ┌──────────────┐
  │  子 Agent     │        │  Custom Agent │
  │  .Run(ctx)   │        │  (Meta动态创建)│
  └──────────────┘        └──────────────┘
```

### 3.2 实际在用的核心机制

| 机制 | 实现位置 | 说明 |
|------|---------|------|
| Conductor 直接委派 | conductor.go | delegate_* 工具 = tools.Adapter 包装 subAgent.Run() |
| AgentResult | types.go | {Text, Memory}，Text 作为 tool_result 返回 |
| RunAgentLoop | executor.go | 统一子 Agent 执行引擎 |
| Meta-Agent 动态注册 | conductor.go | JSON 解析 → registerCustomAgent() → 立即执行 |
| 工具系统 | tools/ | Adapter{name, description, fn, schema} + Registry |
| EventBus 发布 | messaging/ | 子 Agent 执行过程事件发布，供 TUI/WebUI 展示 |

### 3.3 死代码/过度设计清单

| 模块 | 文件 | 行数 | 问题 |
|------|------|------|------|
| P2P 符号表 | p2p_integration.go | ~120 | `InitAgentMeshP2P` 从未被调用，完全死代码 |
| 委派上下文 | mesh/delegation.go + test | ~694 | 依赖的 p2p_delegate 几乎未使用 |
| 规划器 | conductor/planner.go | ~150 | 未完成重构，零引用 |
| 路由器 | conductor/router.go | ~150 | 未完成重构，零引用 |
| 内存管理器 | conductor/memory_manager.go | ~125 | 未完成重构，零引用 |
| 黑板系统 | memory/blackboard.go | ~701 | LLM 极少使用，MVCC+事件订阅复杂度过高 |
| 分层记忆 | memory/layered.go | ~100 | AutoPromote 未生效，多层方法死代码 |
| AgentMesh | agents/mesh.go | ~100 | P2P 网格管理，未真正使用 |
| **合计** | | **~1840** | |

---

## 四、新架构设计：DAG 委派图

### 4.1 核心思想

**委派就是一个 tool call。**

子 Agent 能委派其他 Agent，是因为它的工具列表中包含 `delegate_*` 工具。委派的结果作为 `tool_result` 返回给调用者，**天然留在调用者的上下文中，不会冒泡到上层**。

上下文隔离不需要任何额外机制——这是 tool_call 语义的自然结果。

```
Conductor → delegate_coding → Coding Agent 运行
  Coding Agent → delegate_repo → Repo Agent 运行
  Repo Agent 结果 → Coding Agent 的 tool_result（留在 Coding 上下文）
Coding Agent 结果 → Conductor 的 tool_result（只有摘要）

Conductor 永远看不到 Repo Agent 的详细探索结果 ✓
```

### 4.2 架构总览

```
┌─────────────────────────────────────────────────────────────────┐
│                    DelegationGraph (DAG)                         │
│                                                                 │
│  conductor → [repo, coding, chat, meta, devops, browser]       │
│  coding    → [repo]                                             │
│  devops    → [repo]                                             │
│  browser   → [repo]                                             │
│  repo      → []  ◄── 叶子节点（终结点）                          │
│  chat      → []                                                 │
│  meta      → []                                                 │
│                                                                 │
│  ValidateDelegationGraph() 启动时验证无环                        │
└──────────────────────────┬──────────────────────────────────────┘
                           │
┌──────────────────────────▼──────────────────────────────────────┐
│                   AgentBuilder.Build()                           │
│                                                                 │
│  1. 验证委派图（DAG 检查）                                       │
│  2. 拓扑排序（叶子节点先构建）                                    │
│  3. 按序构建：repo → coding/devops/browser → conductor           │
│  4. 每个节点根据委派图注入 delegate_* 工具                       │
└──────────────────────────┬──────────────────────────────────────┘
                           │
    ┌──────────────────────┼──────────────────────┐
    │                      │                      │
    ▼                      ▼                      ▼
┌──────────────┐  ┌──────────────────┐  ┌────────────────────┐
│  Repo Agent  │  │  Coding Agent    │  │  Conductor         │
│  (叶子节点)   │  │                  │  │                    │
│              │  │  Tools:          │  │  Tools:            │
│  Tools:      │  │  - write_file    │  │  - delegate_repo   │
│  - grep      │  │  - edit_file     │  │  - delegate_coding │
│  - read_file │  │  - run_tests     │  │  - delegate_chat   │
│  - list_dir  │  │  - delegate_repo │  │  - delegate_meta   │
│              │  │    (→ Repo)      │  │  - delegate_devops │
│  无 delegate │  │                  │  │  - delegate_browser│
│  工具!       │  │  RunAgentLoop    │  │                    │
│              │  │                  │  │  RunAgentLoop      │
└──────────────┘  └──────────────────┘  └────────────────────┘
    │                      │                      │
    └──────────────────────┼──────────────────────┘
                           │
                           ▼
┌─────────────────────────────────────────────────────────────────┐
│                 RunAgentLoop (executor.go)                       │
│                   【保持不动】                                    │
│                                                                 │
│  loop:                                                          │
│    1. LLM.Call(messages) → EventBus.Publish(llm_call_start)     │
│    2. if tool_call →                                           │
│         Execute tool → EventBus.Publish(tool_call_start)        │
│         Append tool_result to messages ← 结果留在当前上下文      │
│    3. if finish → break                                         │
│    4. goto loop (until MaxSteps)                                │
│                                                                 │
│  return AgentResult{Text: lastMessage}                          │
└─────────────────────────────────────────────────────────────────┘
```

### 4.3 设计要点

#### 4.3.1 静态委派权限图（DAG）

用一个 `map[string][]string` 定义所有委派关系。Repo Agent 的值为空数组，结构性保证它是终结点。

**为什么用静态图而非运行时检测？**
- 旧架构的 DelegationContext（314行）用运行时环检测，复杂且脆弱
- 静态 DAG 在启动时一次性验证，运行时零开销
- 委派关系一目了然，不靠运行时发现

#### 4.3.2 上下文天然隔离

委派结果作为 `tool_result` 返回给调用者的 `RunAgentLoop`，自动加入调用者的消息历史。

**这不需要任何额外机制**——这是 tool_call 的标准语义：
- Conductor 调用 `delegate_coding` → Coding 的结果进入 Conductor 上下文
- Coding 调用 `delegate_repo` → Repo 的结果进入 Coding 上下文（不进 Conductor）
- Repo 的详细探索结果永远停留在 Repo 的上下文中，执行完毕后随上下文销毁

#### 4.3.3 工具分配隔离防递归

子 Agent 的工具列表由 `DelegationGraph` 决定。Repo Agent 不获得任何 `delegate_*` 工具 → 无法委派 → 天然无递归。DAG 保证无环 → 拓扑构建保证依赖顺序。

#### 4.3.4 拓扑序构建

Agent 必须按拓扑序构建（叶子节点先），因为父 Agent 的 `delegate_*` 工具需要持有子 Agent 的引用。这恰好是自然的依赖顺序。

---

## 五、核心接口设计

### 5.1 Agent 接口（最小接口，零变更）

```go
// agent/agent.go — 核心 Agent 接口

package agent

import "context"

// Agent 是所有子 Agent 的统一接口。
// 内置 Agent 和 Meta-Agent 动态创建的 Custom Agent 实现同一接口。
type Agent interface {
    // Name 返回 Agent 唯一标识，用于生成 delegate_<name> 工具名。
    Name() string

    // Description 返回 Agent 能力描述，供 LLM 决定何时委派。
    Description() string

    // Run 执行 Agent 任务，返回结果文本。
    // 结果作为 tool_result 返回给调用者，天然留在调用者上下文中。
    Run(ctx context.Context, input Input) (Output, error)
}

// Input 是委派给子 Agent 的输入。
type Input struct {
    Task    string   `json:"task"`              // 任务描述
    Context []string `json:"context,omitempty"` // 可选补充上下文
}

// Output 是子 Agent 返回给调用者的结果。
// 仅包含结果文本，作为 tool_result 返回。
type Output struct {
    Text string `json:"text"`
}
```
