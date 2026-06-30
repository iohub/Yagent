# Repo Agent 记忆常驻改造方案

> 版本：v1.0
> 日期：2024-01
> 状态：设计草案

---

## 1. 背景与目标

### 1.1 当前问题

RepoAgent 被设计为完全无状态的——每次 `Run()` 调用都是"白手起家"：

- 每次查询都要重新搜索、重新理解相同的代码结构
- 没有跨调用的知识继承机制
- 前一次调用的发现无法被后续调用复用
- 长会话中大量重复的搜索浪费 token 和响应时间

### 1.2 改造目标

| 目标 | 优先级 | 衡量标准 |
|------|--------|----------|
| 跨调用记忆常驻 | P0 | 第二次查询能使用第一次积累的知识 |
| LLM 驱动的记忆整理 | P0 | 使用 LLM 提取关键知识，丢弃噪音 |
| 1500 tokens 硬限制 | P0 | 每次保存前检查 token 预算 |
| 异步非阻塞 | P0 | 记忆整理不阻塞主流程返回 |
| 知识迭代更新 | P1 | 新知识能纠错旧知识，版本化管理 |
| 故障透明降级 | P0 | 记忆系统任何故障不影响核心分析能力 |

---

## 2. 现有架构分析

### 2.1 RepoAgent 现状

```go
// repo.go 核心流程
func (a *RepoAgent) Run(ctx context.Context, input string) (AgentResult, error) {
    systemPrompt := repoPrompt  // 从 repo.prompt.md 加载
    // ...
    result, err := RunAgentLoop(ctx, cfg)
    return AgentResult{
        Text:   result.Text,
        Memory: ConvertLLMHistoryToMemory(result.History),  // 完整对话历史
    }, nil
}
```

**关键特征**：
- 实现 `Agent` 接口：`Name() string` + `Run(ctx, input) (AgentResult, error)`
- 内部使用 `RunAgentLoop()` 与 LLM 交互
- 返回 `AgentResult{Text, Memory}`，其中 `Memory` 包含完整的 LLM 对话历史
- **每次调用完全无状态**——没有跨调用的知识记忆

### 2.2 Director 委派机制

```go
// director.go - delegate_repo 适配器
delegateRepo := tools.NewAdapter("delegate_repo", ..., func(ctx context.Context, params) {
    result, err := repo.Run(ctx, task)
    return self.applyEnhancedCommander("repo", task, result, err)
})
```

- 结果经过 `applyEnhancedCommander()` 压缩后返回
- 结果文本存入 `GlobalCtx.RepoSummary`
- `injectSubAgentMemory()` 注入摘要到 Director 的 ConversationMemory

### 2.3 现有 Memory 系统三层设计

```
┌─────────────────────────────────────────────────────────────┐
│  ConversationMemory (memory.go)                              │
│  Director 持有，管理完整对话上下文                           │
│  支持 sub-agent 分组 (GroupID, ParentID, IsSubAgent)         │
├─────────────────────────────────────────────────────────────┤
│  SharedMemory (shared.go)                                    │
│  全局可见，MVCC 版本控制，publish/subscribe 机制              │
│  所有 Agent 可读写，适合跨 Agent 数据共享                    │
├─────────────────────────────────────────────────────────────┤
│  LocalMemory (local.go)                                      │
│  每个 Agent 私有，thread-safe                                 │
│  Run() 生命周期内有效，调用结束后丢弃                        │
└─────────────────────────────────────────────────────────────┘
```

**问题**：三层设计中，缺少"Agent 级持久化知识层"。
- `LocalMemory` — 仅单次生命周期内有效，Run() 结束即丢弃
- `SharedMemory` — 全局 KV 存储，无结构化知识提炼能力
- `ConversationMemory` — 仅 Director 持有，RepoAgent 无法访问

### 2.4 根因分析

```
Observed Symptom: 每次查询重复搜索相同代码结构
        ↓
Direct Cause: Run() 无状态，无跨调用上下文注入
        ↓
Design Gap: RepoAgent 没有跨调用的"长期记忆"机制
        ↓
Root Cause: 现有 Memory 系统缺少"Agent 级持久化知识层"
        ↓
Fundamental Issue: 系统缺少"压缩→持久→可注入"的知识沉淀管道
```

---

## 3. 方案设计

### 3.1 总体架构

```
┌─────────────────────────────────────────────────────────────┐
│                    RepoAgent (repo.go)                        │
│                                                               │
│  Run(ctx, input)                                              │
│    │                                                          │
│    ├─ 1. Load Memory ──────────────────────────────────┐     │
│    │        从 SharedMemory 读取持久化记忆               │     │
│    │        失败时降级为空记忆，不阻断主流程              │     │
│    │                                                    │     │
│    ├─ 2. Inject Memory ────────────────────────────────┤     │
│    │        将记忆渲染为 XML 标签注入 System Prompt      │     │
│    │        格式: <repository_knowledge>...</>           │     │
│    │                                                    │     │
│    ├─ 3. RunAgentLoop (增强 System Prompt) ────────────┤     │
│    │        记忆作为先验知识指导代码分析                  │     │
│    │                                                    │     │
│    ├─ 4. Return AgentResult ───────────────────────────┤     │
│    │        主流程返回，不等待 consolidation             │     │
│    │                                                    │     │
│    └─ 5. Submit Consolidation (异步) ──────────────────┤     │
│             提交整理任务到 worker channel               │     │
│             非阻塞，满时丢弃                            │     │
│                                                               │
│  ┌────────────────────────────────────────────────────────┐  │
│  │ ConsolidationWorker (后台 goroutine)                    │  │
│  │                                                        │  │
│  │  Loop: receive task from channel                        │  │
│  │    ├─ Load latest memory (version check)                │  │
│  │    ├─ Build consolidation prompt                        │  │
│  │    ├─ Call LLM for consolidation (with retry)            │  │
│  │    ├─ Validate format & token budget                    │  │
│  │    └─ Save updated memory to SharedMemory               │  │
│  └────────────────────────────────────────────────────────┘  │
│                                                               │
│  ┌────────────────────────────────────────────────────────┐  │
│  │ RepoMemoryStore (本地缓存 + SharedMemory 后端)          │  │
│  │  - Load(repoID) → RepoMemory                           │  │
│  │  - Save(repoID, memory)                                │  │
│  │  - SaveWithSnapshot(repoID, memory) [快照版本]          │  │
│  │  - Rollback(repoID, version) [回滚]                     │  │
│  └────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────┘
```

### 3.2 核心设计决策

#### 决策 1：记忆格式 — Markdown 分区

| 方案 | 描述 | 评价 |
|------|------|------|
| A: 纯文本 | 单块文本，LLM 全量合并 | 实现最简，但无结构 |
| **B: Markdown 分区** ✅ | 使用固定标题分区，LLM 输出 Markdown | **LLM 最自然的输出格式，结构适度，容错强** |
| C: JSON 条目 | 结构化 KnowledgeEntry 数组 | 解析依赖 JSON，LLM JSON 输出不稳定 |

**推荐方案 B**，理由：
- LLM 天然擅长生成 Markdown，质量和稳定性远高于 JSON
- 即使某分区格式异常，其他分区仍可用
- 分区提供知识组织框架，避免纯文本混乱
- v1 使用 Markdown → v2 可升级 JSON → v3 引入向量索引

#### 决策 2：记忆注入位置 — System Prompt 追加

| 位置 | 评价 |
|------|------|
| **System Prompt 末尾追加** ✅ | 先验知识属于系统级上下文；LLM 权重最高；不干扰对话结构 |
| User message 前置 | 与用户任务混淆，降低用户指令权重 |
| 独立 system 消息 | 增加消息数，对某些 LLM 可能不稳定 |

#### 决策 3：Consolidation 方式 — 异步串行 Worker

- 单 goroutine + channel 串行处理
- 非阻塞 `Submit()`，满时丢弃（降级优雅）
- 每次 consolidation 读取最新记忆版本，处理并发写入
- 超时控制 60s，2 次重试

---

## 4. 详细设计

### 4.1 记忆数据结构（repo_memory.go）

```go
// MemorySection 定义记忆的分区名称
type MemorySection string

const (
    SectionArchitecture MemorySection = "Architecture"
    SectionPatterns     MemorySection = "Patterns"
    SectionConventions  MemorySection = "Conventions"
    SectionDependencies MemorySection = "Dependencies"
    SectionGotchas      MemorySection = "Gotchas"
    SectionKeyFiles     MemorySection = "Key Files"
)

var MemorySections = []MemorySection{
    SectionArchitecture,
    SectionPatterns,
    SectionConventions,
    SectionDependencies,
    SectionGotchas,
    SectionKeyFiles,
}

// RepoMemory 表示一个仓库的持久化记忆
type RepoMemory struct {
    RepoID      string    `json:"repo_id"`       // 仓库唯一标识
    Version     int       `json:"version"`        // 版本号，递增
    Content     string    `json:"content"`        // Markdown 格式完整记忆内容
    TokenCount  int       `json:"token_count"`    // 估算 token 数
    UpdatedAt   time.Time `json:"updated_at"`     // 最后更新时间
    UpdateCount int       `json:"update_count"`   // 历史整理次数
}

const MaxMemoryTokens = 1500

// RenderForInjection 将记忆渲染为可注入 LLM 的 XML 标签包裹文本
func (m *RepoMemory) RenderForInjection() string {
    if m == nil || m.Content == "" {
        return ""
    }
    return fmt.Sprintf(
        `<repository_knowledge>
The following is accumulated knowledge from your previous analysis sessions on this repository.
Use this as prior context. If new findings contradict this knowledge, trust the new findings.
Do NOT repeat or reference this knowledge section explicitly in your responses.

%s
</repository_knowledge>`, m.Content)
}
```

### 4.2 RepoMemoryStore

```go
// RepoMemoryStore 管理仓库记忆的持久化
type RepoMemoryStore struct {
    mu       sync.RWMutex
    cache    map[string]*RepoMemory           // 本地缓存，减少 SharedMemory 读取
    shared   SharedMemoryInterface            // 依赖倒置，便于测试
}

// SharedMemoryInterface 抽象 SharedMemory 操作
type SharedMemoryInterface interface {
    Get(ctx context.Context, key string) (string, bool, error)
    Set(ctx context.Context, key string, value string) error
}

func memoryKey(repoID string) string {
    return fmt.Sprintf("repo_memory:%s", repoID)
}

func (s *RepoMemoryStore) Load(ctx context.Context, repoID string) (*RepoMemory, error) {
    // 1. 检查本地缓存（读锁）
    // 2. 从 SharedMemory 读取
    // 3. 反序列化后写入缓存
    // 4. 不存在则返回空记忆
}

func (s *RepoMemoryStore) Save(ctx context.Context, memory *RepoMemory) error {
    // 1. Token 预算硬检查（≤1500）
    // 2. 序列化为 JSON
    // 3. 写入 SharedMemory
    // 4. 更新本地缓存
}
```

### 4.3 ConsolidationWorker

```go
// ConsolidationTask 表示一次记忆整理任务
type ConsolidationTask struct {
    RepoID     string
    TaskDesc   string          // 本次任务描述
    Messages   []llm.Message   // 完整对话历史（深拷贝）
    OldMemory  *RepoMemory     // 当前记忆快照（用于版本比对）
}

// ConsolidationWorker 异步记忆整理工作器
type ConsolidationWorker struct {
    ch            chan ConsolidationTask
    memoryStore   *RepoMemoryStore
    llmClient     LLMClient   // 抽象 LLM 调用
    done          chan struct{}
    maxRetries    int
}

// LLMClient 抽象 LLM 调用，与现有 RunAgentLoop 解耦
type LLMClient interface {
    Call(ctx context.Context, systemPrompt string, userPrompt string) (string, error)
}

func (w *ConsolidationWorker) process(task ConsolidationTask) {
    // 1. 读取最新记忆（可能已被其他任务更新）
    currentMemory, _ := w.memoryStore.Load(ctx, task.RepoID)

    // 2. 版本检测：如果当前版本更新，用当前版本做基准
    if currentMemory.Version > task.OldMemory.Version {
        task.OldMemory = currentMemory
    }

    // 3. 构建 LLM prompt
    systemPrompt := ConsolidationSystemPrompt
    userPrompt := BuildConsolidationUserPrompt(task, currentMemory)

    // 4. 调用 LLM（带重试）
    result, err := w.llmClient.Call(ctx, systemPrompt, userPrompt)

    // 5. Token 预算检查 + 截断
    // 6. Markdown 格式验证
    // 7. 保存新记忆（版本号递增）
}
```

### 4.4 LLM 记忆整理 Prompt

#### System Prompt

```
You are a Memory Consolidation Engine for a code analysis agent. Your job is to
distill reusable knowledge from a completed analysis session and merge it with
existing accumulated knowledge.

## Rules

1. EXTRACT only confirmed, reusable knowledge
2. DISCARD all noise — failed searches, intermediate reasoning, tool call params
3. MERGE with existing knowledge; prefer newer confirmed findings on conflict
4. STAY WITHIN BUDGET — output MUST be under 1500 tokens
5. USE EXACT section headings: ## Architecture, ## Patterns, ## Conventions,
   ## Dependencies, ## Gotchas, ## Key Files
6. BE SPECIFIC: concrete facts > vague descriptions
7. NO DUPLICATION within or across sections
8. NO SPECULATION — only include confirmed knowledge

## Section Definitions

- Architecture: High-level structure, module organization, entry points, data flow
- Patterns: Repeated code patterns, error handling, logging, design patterns
- Conventions: Naming, file organization, coding style
- Dependencies: Key libraries, frameworks, external services
- Gotchas: Non-obvious behaviors, common pitfalls, workarounds
- Key Files: Important files with brief role descriptions
```

#### User Prompt 构建逻辑

```
## Current Task
{task.TaskDesc}

## Existing Knowledge (may need updating)
{currentMemory.Content}

## Conversation Summary
{压缩后的对话历史，只保留关键信息}

## Instruction
{如果是首次：提取知识；如果是更新：合并新旧知识}
```

### 4.5 RepoAgent 修改点

```go
// RepoAgent 新增字段
type RepoAgent struct {
    BaseAgent
    GlobalCtx *globalctx.GlobalCtx
    Adapters  []*tools.Adapter
    maxSteps  int

    // [NEW] 记忆系统
    memoryStore         *RepoMemoryStore
    consolidationWorker *ConsolidationWorker
    repoID              string
}

// Run() 方法修改
func (a *RepoAgent) Run(ctx context.Context, input string) (AgentResult, error) {
    // [NEW] Step 1: 加载记忆
    memory, err := a.memoryStore.Load(ctx, a.repoID)
    if err != nil {
        // 降级：记忆加载失败不阻断主流程
        memory = &RepoMemory{RepoID: a.repoID}
    }

    // [NEW] Step 2: 构建增强的 system prompt（追加记忆）
    enhancedPrompt := repoPrompt
    if injection := memory.RenderForInjection(); injection != "" {
        enhancedPrompt += "\n\n" + injection
    }

    // Step 3: 执行原有 Agent Loop（使用增强 prompt）
    cfg := DefaultExecutorConfig()
    cfg.SystemPrompt = a.GlobalCtx.FormatPrompt(enhancedPrompt)
    cfg.UserInput = input
    cfg.Adapters = a.Adapters
    cfg.LLM = a.LLM
    cfg.MaxSteps = a.maxSteps
    cfg.Publisher = a.Publisher
    cfg.AgentName = a.Name()
    cfg.SystemAsHuman = true

    result, err := RunAgentLoop(ctx, cfg)
    if err != nil {
        return AgentResult{}, err
    }

    agentResult := AgentResult{
        Text:   result.Text,
        Memory: ConvertLLMHistoryToMemory(result.History),
    }

    // [NEW] Step 4: 异步触发记忆整理
    if len(agentResult.Memory) > 0 {
        a.consolidationWorker.Submit(ConsolidationTask{
            RepoID:    a.repoID,
            TaskDesc:  input,
            Messages:  deepCopyMessages(result.History),
            OldMemory: memory,
        })
    }

    return agentResult, nil
}
```

---

## 5. 边界条件与降级策略

### 5.1 边界条件处理

| 场景 | 处理策略 |
|------|----------|
| 首次调用，无记忆 | `Load()` 返回空 `RepoMemory`，`RenderForInjection()` 返回空字符串，行为与改造前完全一致 |
| 记忆加载失败 | 不阻断主流程，降级为无记忆模式，记录 warning 日志 |
| Consolidation 队列满 | `Submit()` 返回 `false`，丢弃本次整理，不影响结果返回 |
| LLM 调用失败 | Worker 内部重试最多 2 次，全部失败则保留旧记忆 |
| Token 预算超限 | 硬截断 + 日志告警，保证 ≤1500 tokens |
| 进程崩溃 | 旧记忆已在 SharedMemory 持久化不受影响；新知识丢失，下次调用重新发现（最坏情况 = 改造前行为） |
| 并发写入 | SharedMemory MVCC + 版本号递增保证最终一致性 |
| 记忆格式非法 | `ValidateMemoryFormat()` 校验不通过则保留旧记忆 |

### 5.2 降级层次

```go
// L1: 禁用记忆注入（保留 consolidation 运行但无影响）
if !config.MemoryEnabled || memory.Content == "" {
    // 走原有流程，不注入
}

// L2: 禁用全链路
if !config.MemoryEnabled {
    // 不初始化 worker，不读写 SharedMemory
    // 完全回退到改造前行为
}

// L3: 清除记忆（运维操作）
// 删除 SharedMemory 中 repo_memory:* 前缀的所有 key
```

**关键原则**：记忆系统是"增强型"功能，任何失败都不应影响 RepoAgent 的核心分析能力。所有新代码路径都有降级分支。

---

## 6. 快照与回滚

```go
const MaxMemorySnapshots = 5

type MemorySnapshot struct {
    Version int       `json:"version"`
    Content string    `json:"content"`
    SavedAt time.Time `json:"saved_at"`
}

// SaveWithSnapshot 保存记忆时保留历史快照
func (s *RepoMemoryStore) SaveWithSnapshot(ctx context.Context, memory *RepoMemory) error {
    // 1. 将当前版本加入快照列表
    // 2. 保留最近 5 个版本
    // 3. 保存快照到 SharedMemory
    // 4. 保存新记忆
}

// Rollback 回滚到指定版本
func (s *RepoMemoryStore) Rollback(ctx context.Context, repoID string, targetVersion int) (*RepoMemory, error) {
    // 从快照中找到目标版本，覆盖当前记忆
}
```

---

## 7. 文件清单

| 文件 | 操作 | 职责 |
|------|------|------|
| `internal/agents/repo_memory.go` | 新增 | 记忆数据结构、RepoMemoryStore、token 计数、快照管理 |
| `internal/agents/consolidation_worker.go` | 新增 | 异步 consolidation 后台 worker |
| `internal/agents/prompts/memory_consolidation.go` | 新增 | System prompt + User prompt 构建器 |
| `internal/agents/repo.go` | 修改 | Run() 中注入记忆 + 触发异步 consolidation |
| `internal/agents/repo_memory_test.go` | 新增 | 记忆系统单元测试 |
| `internal/agents/consolidation_worker_test.go` | 新增 | Worker 单元测试 |

---

## 8. 配置参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `MaxMemoryTokens` | 1500 | 记忆 token 上限 |
| `ConsolidationBufferSize` | 16 | Worker channel 缓冲区大小 |
| `ConsolidationMaxRetries` | 2 | LLM 调用重试次数 |
| `ConsolidationTimeout` | 60s | 单次 consolidation 超时 |
| `MaxMemorySnapshots` | 5 | 保留的历史版本数 |
| `MemoryEnabled` | true | 记忆功能开关 |
| `TokenEstimateFactor` | 3.5 | 字符到 token 的估算系数 |

---

## 9. 测试策略

### 9.1 单元测试

| 测试 | 验证点 |
|------|--------|
| `TestMemoryRenderForInjection` | 空记忆不注入；有记忆正确包裹 XML 标签 |
| `TestTokenBudgetEnforcement` | 超长内容被截断；始终 ≤1500 tokens |
| `TestMemorySaveAndLoad` | 读写一致性；版本号正确 |
| `TestConsolidationWorkerProcess` | 完整 consolidation 流程；记忆内容正确更新 |
| `TestFormatValidation` | 有效 Markdown 通过；无效格式拒绝 |
| `TestConcurrentConsolidation` | 两次快速 consolidation 不丢失数据 |

### 9.2 集成测试场景

| 场景 | 验证点 |
|------|--------|
| 首次查询（无记忆） | 行为与改造前一致 |
| 第二次查询（有记忆） | system prompt 包含知识，LLM 利用记忆 |
| 记忆纠错 | 错误记忆被纠正，后续使用正确版本 |
| 并发查询 | 不互相干扰，consolidation 串行处理 |
| 超长会话 | 多轮 consolidation 后 ≤1500 tokens |
| LLM 失败 | 旧记忆不丢失，主流程不受影响 |
| 仓库切换 | 不同 repoID 的记忆完全隔离 |

---

## 10. 未来演化路径

```
v1 (当前方案)                    v2                              v3
─────────────────            ───────────────               ───────────────
Markdown 分区记忆    ──▶      JSON 结构化条目     ──▶      向量索引 + 语义检索
SharedMemory KV     ──▶      独立记忆服务         ──▶      分布式记忆存储
单 repo 粒度        ──▶      跨 repo 知识迁移     ──▶      组织级知识图谱
固定 1500 tokens    ──▶      动态预算分配         ──▶      自适应上下文窗口
全量 consolidation  ──▶      增量条目更新         ──▶      实时流式知识提取
```

---

## 附录：Blast Radius 分析

| 组件 | 变更类型 | 风险 | 说明 |
|------|----------|------|------|
| `repo.go` | 修改 | 中 | Run() 流程变更，需确保不破坏现有接口 |
| `repo_memory.go` | 新增 | 低 | 独立模块，不侵入现有逻辑 |
| `consolidation_worker.go` | 新增 | 低 | 异步后台任务，故障不影响主流程 |
| `prompts/memory_consolidation.go` | 新增 | 低 | 纯 prompt 定义 |
| `SharedMemory` | 使用方 | 低 | 仅新增 key，不修改 SharedMemory 本身 |
| `director.go` | 无变更 | 无 | 记忆常驻对 Director 完全透明 |
| `executor.go` | 无变更 | 无 | — |
| `LocalMemory` | 无变更 | 无 | — |
| `ConversationMemory` | 无变更 | 无 | — |
