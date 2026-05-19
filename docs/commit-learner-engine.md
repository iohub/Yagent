# Commit Learner 后台引擎 — 运行机制

> **设计理念**: Commit Learner 不是 Agent 的工具，而是像 BM25 一样的后台自动知识引擎。Agent 不需要知道它的存在，相关的 commit 知识会在需要时自动注入到 system prompt 中。

---

## 1. 架构总览

```
┌──────────────────────────────────────────────────────────────────────────┐
│                         ConductorAgent.Run()                            │
│                                                                          │
│  1. CommitManager.Initialize(ctx, repoPath)                             │
│     └─ 异步 goroutine: 根据状态决定增量/全量学习                         │
│        ├─ 有持久化状态 → 增量学习 (EnsureLatest)                         │
│        ├─ 无持久化状态 → 全量学习 (EnsureLatest)                         │
│        └─ 学习完成 → 保存状态到 .codeactor/commit_state.json             │
│                                                                          │
│  2. 构建 System Prompt                                                  │
│     ├─ GlobalCtx.FormatPrompt(conductorPrompt)  ← 基础 prompt            │
│     ├─ loadProjectContext()                      ← CODEACTOR.md 等       │
│     ├─ GetCommitContext(userInput)               ← ⭐ 每次对话自动注入   │
│     │   └─ CommitLearner.SearchSimilar(query)                           │
│     │       └─ POST /commit/search → Rust 向量数据库                    │
│     └─ Custom Agents 描述                          ← 自定义 Agent       │
│                                                                          │
│  3. RunAgentLoop(systemPrompt, userInput)        ← LLM 工具循环         │
└──────────────────────────────────────────────────────────────────────────┘
```

**关键转变**:
- Before: `learn_commits` 和 `search_similar_commits` 是 Agent 可调用的工具
- After: 学习由后台引擎自动触发，搜索在 `GetCommitContext` 中自动完成

---

## 2. 核心组件

### 2.1 CommitManager — 引擎入口

**文件**: `internal/agents/commit_manager.go`

```go
type CommitManager struct {
    learner  *CommitLearner    // 底层学习器
    once     sync.Once         // 保证只初始化一次
    err      error             // 初始化错误
    ready    atomic.Bool       // 初始学习是否完成
    stateMu  sync.RWMutex
    state    *CommitState      // 持久化状态
}
```

**职责**:
- 管理 CommitLearner 生命周期
- 控制异步初始化流程（`sync.Once` + goroutine）
- 持久化学习状态（`loadState` / `saveState`）
- 提供 `IsReady()` 检查初始学习状态

### 2.2 CommitLearner — 学习与检索引擎

**文件**: `internal/agents/commit_learner.go`

```go
type CommitLearner struct {
    config          CommitLearnConfig    // 配置
    llmEngine       llm.Engine           // 默认 LLM 引擎
    dedicatedEngine llm.Engine           // 专用 LLM 引擎（可选）
    globalCtx       *globalctx.GlobalCtx // 全局上下文
    httpClient      *http.Client
    cache           map[string]*CachedSummary  // 本地缓存
    cacheMu         sync.RWMutex
    lastHead        string              // 上次学习的 HEAD
    lastFetch       time.Time           // 上次获取时间
}
```

**职责**:
- 执行 `git log` 获取 commit 信息
- 调用 LLM 生成结构化摘要（并发，上限 3）
- 将摘要嵌入并存储到 Rust 向量数据库
- 根据用户输入搜索相似 commit

### 2.3 ConductorAgent — 知识注入点

**文件**: `internal/agents/conductor.go`

```go
// GetCommitContext — 在 Run() 中被调用，每次用户输入都会触发
func (a *ConductorAgent) GetCommitContext(ctx, userInput) string {
    // 1. 检查 commitManager 是否可用
    // 2. 调用 learner.SearchSimilar(query, topK)
    // 3. 格式化为 markdown 文本
    // 4. 发布事件到 TUI
    // 5. 返回格式化文本
}
```

---

## 3. 配置详解

### 3.1 config.toml 配置项

```toml
[commit_learner]
enabled = true                  # 是否启用
max_commits = 30                # 每次学习最多获取的 commit 数
similarity_threshold = 0.75     # 检索时相似度阈值（0.0 ~ 1.0）
top_k = 3                       # 返回的最相似结果数
trigger = "both"                # 触发方式: "on_demand" | "on_session_start" | "both"
cache_ttl = 3600                # 缓存有效期（秒）

# 专用 LLM provider（可选）
# 配置后，commit 摘要生成使用此 provider
# 不配置则使用全局默认 LLM
summarization_provider = ""

# 自定义 LLM 系统提示词（可选）
# llm_system_prompt = """
# You are a software engineering analyst. ...
# """
```

### 3.2 Trigger 模式说明

| 模式 | 行为 | 适用场景 |
|------|------|---------|
| `on_demand` | 不自动学习，仅标记 ready | 手动控制学习时机 |
| `on_session_start` | 会话开始时异步学习 | 非首次运行，有状态可增量更新 |
| `both` | 会话开始时异步学习 | 默认模式，兼顾首次和增量 |

### 3.3 持久化状态文件

**路径**: `<project_root>/.codeactor/commit_state.json`

```json
{
  "last_head_hash": "a1b2c3d4e5f6...",
  "updated_at": "2025-01-01T12:00:00Z",
  "commit_count": 30
}
```

作用：记录上次学习的 HEAD hash，下次启动时以此为基础做增量学习。

---

## 4. 初始化流程

```
ConductorAgent.Run() 被调用
    │
    ▼
CommitManager.Initialize(ctx, repoPath)
    │
    ├─ sync.Once 保证只执行一次
    │
    ├─ □ learner 为 nil?           ──► 直接返回
    │
    ├─ □ enabled = false?          ──► ready = true, 返回
    │
    ├─ □ trigger = "on_demand"?    ──► ready = true, 返回
    │
    └─ trigger = "on_session_start" / "both"
         │
         ▼
    启动 goroutine 异步执行:
         │
         ├─ 1. loadState(projectPath)
         │     └─ 读取 .codeactor/commit_state.json
         │
         ├─ □ state 不存在或 last_head_hash 为空?
         │     │
         │     ├─ Yes → 全量学习: learner.EnsureLatest()
         │     └─ No  → 增量学习: learner.EnsureLatest()
         │
         ├─ □ 增量学习失败?
         │     └─ Yes → 回退到全量学习
         │
         ├─ 2. 获取当前 HEAD hash
         │     └─ learner.getCurrentHead(repoPath)
         │
         ├─ 3. 保存状态到 .codeactor/commit_state.json
         │
         └─ 4. ready = true, 日志记录
```

**异步特性**:
- 初始化不阻塞 ConductorAgent.Run() 的正常流程
- 学习在后台 goroutine 中完成
- 学习完成前，`GetCommitContext()` 可能返回空（因为向量数据库还没数据）
- 一旦学习完成，后续对话都能命中相关的 commit 知识

---

## 5. 学习流程 (EnsureLatest)

```
CommitLearner.EnsureLatest(ctx, repoPath)
    │
    ├─ 1. getCurrentHead(repoPath)          ← git rev-parse HEAD
    │
    ├─ □ HEAD == lastHead && 缓存未过期?
    │     └─ Yes → 跳过，直接返回（缓存命中）
    │
    ├─ 2. FetchRecentCommits(ctx, repoPath, maxCommits)
    │     └─ 执行 git log --max-count=N --name-only --patch
    │     └─ 解析为 []CommitMeta
    │
    ├─ 3. SummarizeCommits(ctx, commits)
    │     └─ 并发调用 LLM（信号量限制为 3）
    │     └─ 每个 commit: LLM 生成结构化摘要 (JSON)
    │     └─ 失败时: extractSummaryWithLLM 二次提取
    │     └─ 最终降级: extractSummaryFromText 文本解析
    │
    ├─ 4. StoreEmbeddings(ctx, summaries)
    │     └─ POST /commit/embed → Rust 向量数据库
    │     └─ 更新本地缓存
    │
    └─ 5. 更新 lastHead 和 lastFetch
```

**LLM 生成的 Commit 摘要结构**:
```go
type CommitSummary struct {
    Hash           string  // commit hash
    Requirement    string  // 需求描述（该 commit 解决了什么需求）
    Files          string  // 变更文件列表
    Approach       string  // 技术方案（采用了什么思路/策略）
    Implementation string  // 实现细节（关键实现要点）
}
```

**容错机制**:
| 失败场景 | 处理方式 |
|---------|---------|
| LLM 返回非 JSON | 用 LLM 二次提取结构化信息 |
| 二次提取也失败 | 基于文本关键字正则提取 |
| 单个 commit 处理失败 | 静默跳过，不影响其他 commit |
| 存储嵌入失败 | 跳过该 commit，继续处理 |

---

## 6. 检索与注入流程

### 6.1 注入时机

每次用户输入都会触发 `GetCommitContext()` 检索相关 commit 知识并注入到 system prompt：

```
ConductorAgent.Run() 每次被调用
    │
    ├─ 构建 systemPrompt（静态部分）
    │
    ├─ □ input != "" ?
    │     └─ Yes → GetCommitContext(ctx, input)
    │         │
    │         ├─ □ commitManager.Enabled()?      ← 功能启用检查
    │         ├─ □ learner 可用?                  ← 初始化检查
    │         ├─ SearchSimilar(query, topK)       ← 向量搜索
    │         └─ □ 有结果?
    │               └─ Yes → 格式化并追加到 systemPrompt
    │                         │
    │                         ▼
    │                   ### Recent Relevant Commits
    │                   ### Commit `a1b2c3d4`:
    │                   - **Requirement**: ...
    │                   - **Files**: ...
    │                   - **Approach**: ...
    │                   - **Implementation**: ...
    │
    └─ RunAgentLoop(systemPrompt + commitContext, input)
```

### 6.2 搜索流程

```
CommitLearner.SearchSimilar(ctx, userInput, topK)
    │
    ├─ 构建搜索请求:
    │   {
    │     "query": userInput,
    │     "top_k": topK,
    │     "threshold": config.SimilarityThreshold
    │   }
    │
    ├─ POST /commit/search → Rust 后端
    │     └─ Rust: 向量相似度搜索 (cosine similarity)
    │
    ├─ 解析响应
    │   {
    │     "matches": [
    │       { "commit_hash": "...", "summary_text": "...", "similarity": 0.85 }
    │     ]
    │   }
    │
    ├─ 过滤: similarity < threshold 的结果丢弃
    │
    └─ 返回 []CommitSummary（已解析为结构化数据）
```

### 6.3 注入效果

注入到 system prompt 后的样例如下：

```
### Recent Relevant Commits

### Commit `a1b2c3d4`:
- **Requirement**: 实现用户认证功能
- **Files**: auth.go, middleware.go
- **Approach**: 基于 JWT 的无状态认证
- **Implementation**: 创建 JWT middleware，实现 token 生成和验证

### Commit `e5f6g7h8`:
- **Requirement**: 修复并发写入数据竞争问题
- **Files**: handler.go, store.go
- **Approach**: 引入 sync.RWMutex 保护共享状态
- **Implementation**: 在 Store 结构体中添加读写锁
```

LLM 在对话开始时就能"看到"相关的历史 commit 信息，无需主动搜索。

---

## 7. 完整数据流

### 7.1 首次运行（无状态）

```
首次启动 ConductorAgent
    │
    ├─ CommitManager.Initialize()
    │     └─ goroutine: 全量学习 30 个 commit
    │         ├─ git log
    │         ├─ LLM 摘要 (并发 ×3)
    │         └─ 存储到向量 DB
    │
    ├─ 用户输入: "如何修改认证逻辑？"
    │     └─ GetCommitContext("如何修改认证逻辑？")
    │           └─ 向量搜索 → 可能为空（学习还没完成）
    │
    ├─ 用户输入: "添加新的 API 端点" (第二轮)
    │     └─ GetCommitContext("添加新的 API 端点")
    │           └─ 向量搜索 → [CommitSummary...] (学习已完成 ✓)
    │           └─ systemPrompt += "### Recent Relevant Commits\n..."
    │
    └─ RunAgentLoop → LLM 可用看到相关 commit 信息
```

### 7.2 增量运行（有状态）

```
再次启动 ConductorAgent
    │
    ├─ CommitManager.Initialize()
    │     ├─ loadState() → last_head_hash = "abc123"
    │     └─ goroutine: 增量学习
    │         ├─ getCurrentHead → "xyz789"
    │         ├─ HEAD 已变化 → EnsureLatest()
    │         └─ 重新学习 → 保存新状态
    │
    └─ 正常检索流程...
```

---

## 8. 状态持久化

### 8.1 文件格式

`.codeactor/commit_state.json`:
```json
{
  "last_head_hash": "abc123def456abc123def456abc123def456abc1",
  "updated_at": "2025-06-01T10:30:00Z",
  "commit_count": 30
}
```

### 8.2 生命周期

| 场景 | 行为 |
|------|------|
| 文件不存在 | 首次运行 → 全量学习 → 创建文件 |
| HEAD 未变化 | 缓存有效期内跳过，否则重新学习 |
| HEAD 已变化 | 重新学习 → 更新文件 |
| 仓库重新 clone | 状态不匹配 → 全量学习 → 覆盖文件 |
| 文件损坏 | 警告日志 → 回退全量学习 |

---

## 9. 配置参考

### 9.1 快速启用

```toml
[commit_learner]
enabled = true
trigger = "both"
```

### 9.2 使用专用 LLM 模型

```toml
[commit_learner]
enabled = true
max_commits = 50
summarization_provider = "siliconflow"   # 指向 global.llm.providers 中的 provider
```

使用专用轻量模型处理 commit 摘要，避免影响主要对话的 token 配额。

### 9.3 仅按需触发

```toml
[commit_learner]
enabled = true
trigger = "on_demand"
```

不自动学习，适合 CI/CD 环境或手动控制学习时机的场景。

---

## 10. 与旧架构对比

| 维度 | 旧架构 (工具模式) | 新架构 (后台引擎模式) |
|------|------------------|---------------------|
| learn_commits | Agent 工具，需手动或由 LLM 触发 | 后台自动触发，无需人工干预 |
| search_similar_commits | Agent 工具，LLM 决定是否调用 | 自动在 `GetCommitContext` 中调用 |
| 注入时机 | 仅首次对话 | 每次用户输入 |
| 状态持久化 | 无 | `.codeactor/commit_state.json` |
| 增量学习 | 不支持（每次都全量） | 基于 HEAD hash 增量更新 |
| Agent 感知 | 需要知道工具存在 | 完全透明 |
| system prompt | 需描述工具 | 无需提及（干净简洁） |
| 容错 | 失败影响工具调用 | 静默降级，不影响主流程 |
