# CodeActor-Agent 知识管理系统优化分析报告

> **文档信息**

| 属性 | 值 |
|------|------|
| **版本** | v1.0 |
| **日期** | 2025 |
| **分析对象** | 系统 Agent 知识整理逻辑（RepoMemoryStore / ConsolidationWorker / KnowledgeInjector / consolidate_knowledge / prune_history） |
| **分析方法** | Repo-Agent 代码实证分析 + 深度机制分析（deepthinking） |
| **相关文档** | [knowledge-management-design.md](./knowledge-management-design.md)、[ARCHITECTURE.md](./ARCHITECTURE.md) |

---

## 目录

- [1. 分析概述](#1-分析概述)
- [2. 现状实现分析](#2-现状实现分析)
- [3. 问题诊断与根因分析](#3-问题诊断与根因分析)
- [4. 系统级影响评估](#4-系统级影响评估)
- [5. 约束分析](#5-约束分析)
- [6. 优化方案设计](#6-优化方案设计)
- [7. 分阶段实施路线图](#7-分阶段实施路线图)
- [8. 验证策略](#8-验证策略)
- [9. 优化效果总结矩阵](#9-优化效果总结矩阵)
- [10. 附录：关键代码位置索引](#10-附录关键代码位置索引)

---

## 1. 分析概述

### 1.1 分析目标

对 CodeActor-Agent 系统中"Agent 知识整理"的完整逻辑进行实证分析，识别现有实现的缺陷与深层机制问题，并从**机制/架构层面**（而非仅修 bug）提出可落地的优化方案，提升知识整理的效果：知识质量、去重效率、检索注入质量、闭环有效性、成本与延迟。

### 1.2 分析结论摘要

- **已落地**：consolidate_knowledge / prune_history 工具、ConsolidationWorker 异步 LLM 整理、知识提取 → 知识库写入闭环、KnowledgeInjector 注入。
- **致命缺陷**：SharedMemory 无磁盘持久化（重启全丢）、Compact() 空实现、去重阈值硬编码、token 估算中文偏差 4 倍、无 TTL。
- **核心建议**：采用 **Solution B（机制增强方案）**——修复全部 bug 并重构知识整理核心机制（持久化基座、质量门禁、多信号去重、单次 LLM 整理、增量 prune、MMR 智能注入、反馈闭环、生命周期管理、可观测性），预计 6-8 周分 4 个阶段落地。

---

## 2. 现状实现分析

### 2.1 核心数据结构

#### 2.1.1 知识库条目（`mcp/knowledge.go:1-30`）

```go
type KnowledgeRecord struct {
    ID           string   `json:"id"`
    Type         string   `json:"type"`          // repo_retrieval | coding_modification
    Title        string   `json:"title"`
    Content      string   `json:"content"`
    Tags         []string `json:"tags"`
    RelatedFiles []string `json:"related_files"`
    SourceAgent  string   `json:"source_agent"`
    TaskID       string   `json:"task_id"`
    Confidence   float64  `json:"confidence"`
    CreatedAt    string   `json:"created_at"`
    UpdatedAt    string   `json:"updated_at"`
    AccessCount  int      `json:"access_count"`
    LastAccessed *string  `json:"last_accessed"`
    ParentIDs    []string `json:"parent_ids"`
}
```

**存储位置**：外部 Rust 进程 `codeseek`，通过 MCP 协议访问（`MCPClient.KnowledgeAdd/Delete/Search/List`）。Go 侧不持有持久化副本，每次读写均走 MCP IPC。

#### 2.1.2 仓库记忆（`agents/repo_memory.go`）

```go
type RepoMemoryStore struct {
    repoID string
    shared *memory.SharedMemory
    mu     sync.RWMutex
    cache  string   // 6 分区 Markdown，MaxMemoryTokens=1500 字符预算
    loaded bool
}

// 6 个固定分区
const (
    SectionArchitecture MemorySection = "Architecture"
    SectionPatterns     MemorySection = "Patterns"
    SectionConventions  MemorySection = "Conventions"
    SectionDependencies MemorySection = "Dependencies"
    SectionGotchas      MemorySection = "Gotchas"
    SectionKeyFiles     MemorySection = "Key Files"
)
```

**持久化方式**：`SharedMemory.SetKey("repo_memory:" + repoID, content)` → 内存 KV，进程重启丢失。**没有磁盘持久化**。

#### 2.1.3 SharedMemory（`memory/shared.go`）

```go
type SharedMemory struct {
    messages    []ChatMessage
    maxSize     int              // default: 500
    subscribers []func(ChatMessage)
    kv          map[string]string
    kvMu        sync.RWMutex
    // persistPath/persistTick/persistDone 字段存在但从未被初始化（死代码）
    persistPath string
    dirty       bool
}
```

### 2.2 知识整理完整流程

**触发点 1：RepoAgent 任务结束后自动触发**（`app.go:329-344`）

```go
repoMemStore := agents.NewRepoMemoryStore(repoID, ca.sharedMemory)
consolidationWorker := agents.NewConsolidationWorker(repoMemStore, repoEngine, ca.globalCtx.CodeSeekMCP, knowledgeCfg)
consolidationWorker.Start()
repoAgent.SetMemory(repoMemStore, consolidationWorker)
```

**触发点 2：手动工具调用**（`coding.go:533-588`）— Agent 可直接调用 `prune_history(action="merge/list/delete")` 和 `consolidate_knowledge` 工具。

**触发点 3：子任务自动沉淀**（`knowledge_hook.go:1-60`）— `autoConsolidateSubtask()` 在子 Agent 完成时异步（60s 超时）调用 `ConsolidateKnowledgeTool`。

**ConsolidationWorker 主流程**（`consolidation_worker.go:100-170`）：

```
process(task):
  1. currentMem := store.Get()
  2. obs := TruncateObservations(task.NewObservations)  // 10000 char cap
  3. consolidated := callConsolidationLLM(currentMem, obs)  // 带重试，30s timeout, max 2 retries
  4. consolidated := EnforceTokenBudget(consolidated)  // 1500 char token budget
  5. ValidateMemoryFormat(consolidated)  // 检查 6 个分区标题
  6. store.Save(ctx, consolidated)  // 写入 SharedMemory KV
  7. writeConsolidationFile(consolidated)  // 写入 ~/.codeactor/logs/memory-consolidated-YYYY-MM-DD.log
  8. extractKnowledge(consolidated)  // LLM 提取知识条目 → MCP KnowledgeAdd
  9. if consolidationCount % 10 == 0: triggerPruneMerge()
```

### 2.3 相似度/去重计算方式

- **consolidate_knowledge 去重**（`tools/knowledge.go:120-145`）：以 title 为 query 检索 top-5，使用 rerank_score > 0.85 判定重复。**阈值硬编码在 `tools/knowledge.go:135`**。
- **prune_history merge 去重**（`tools/knowledge.go:370-400`）：默认 threshold=0.80，可通过 `similarity_threshold` 参数覆盖。
- **计算方式**：Go 侧不计算相似度，完全依赖 codeseek MCP 返回的 `RerankScore`（底层：tantivy BM25 + LanceDB 向量 + Cross-Encoder reranker）。

### 2.4 Merge 与 Delete 具体逻辑

**Merge（触发路径 A：consolidate_knowledge 自动合并）**：

```
检测到重复 (rerank_score > 0.85):
  → mergeKnowledgeLLM(newTitle, newContent, newTags, oldRecord)
  → 调用 LLM 生成合并后的 title/content/tags
  → KnowledgeAdd(newRecord)
  → KnowledgeDelete(dupResult.ID)
  → 返回 {status: "merged", id: newID, parent_ids: [dupID]}
```

**Merge（触发路径 B：prune_history merge）**：

```
遍历所有候选条目（limit=200）:
  对每个条目 A:
    KnowledgeSearch(A.Content, limit=5, rerank=true)
    找到 B 且 rerank_score > threshold:
      → mergeKnowledgeLLM(A, B)
      → KnowledgeAdd(merged)
      → KnowledgeDelete(A.ID)
      → KnowledgeDelete(B.ID)
```

**Delete**（`tools/knowledge.go:320-350`）：按 ID 列表逐个调用 `KnowledgeDelete`，全部失败才报错。**无自动过期/TTL 机制，删除完全由 LLM 或手动调用决定。**

### 2.5 三组件关系与数据流

```
┌─────────────────────────────────────────────────────────────┐
│  RepoAgent.Run()                                            │
│   1. repoAgent 执行任务 → 产出 observation text              │
│   2. ConsolidationWorker.Submit(observation)  ← 异步提交    │
└─────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────┐
│  ConsolidationWorker.process()                              │
│   RepoMemoryStore.Get() ──→ SharedMemory.GetKey("repo_mem:..")
│   callConsolidationLLM() ──→ LLM 合并记忆                   │
│       EnforceTokenBudget() ──→ ≤1500 字符                   │
│       ValidateMemoryFormat() ──→ 检查 6 个分区               │
│   RepoMemoryStore.Save() ──→ SharedMemory.SetKey(...)       │
│       writeConsolidationFile() ──→ ~/.codeactor/logs/*.log  │
│   extractKnowledge() ──→ LLM 提取 JSON 条目                 │
│       → ConsolidateKnowledgeTool.Execute()                  │
│           → MCP KnowledgeAdd（写入 codeseek 知识库）         │
│   if count%10==0: triggerPruneMerge()                       │
│       → PruneHistoryTool.Execute(action="merge")            │
└─────────────────────────────────────────────────────────────┘
```

**关系说明**：
- `RepoMemoryStore`：仓库记忆缓存层，读来自 SharedMemory KV，写来自 ConsolidationWorker。
- `ConsolidationWorker`：记忆整理引擎，是唯一写入者，通过 LLM 合并 + 提取知识。
- `ResultCompressor`：与知识整理无关，仅用于压缩子 Agent 大结果并存储到 SharedMemory KV，供 Director 检索。

### 2.6 反馈闭环状态

| 环节 | LLM 调用 | 输入 | 输出 | 回写 |
|------|---------|------|------|------|
| 记忆合并 | `callConsolidationLLM()` | 旧记忆 + 新观察 | 合并后 Markdown | `RepoMemoryStore.Save()` → SharedMemory |
| 知识提取 | `extractKnowledge()` | 合并后记忆文本 | JSON 知识条目数组 | `ConsolidateKnowledgeTool.Execute()` → MCP KnowledgeAdd |
| 知识蒸馏 | `distillContent()` | title + content (>1500字) | 压缩后内容 | 直接替换 content 字段 |
| 知识合并 | `mergeKnowledgeLLM()` | 新旧条目 | 合并后 JSON | `MCP.KnowledgeAdd()` + `MCP.KnowledgeDelete()` |
| prune merge | 同上 | 每对重复条目 | 合并后 JSON | `MCP.KnowledgeAdd()` + `MCP.KnowledgeDelete()` |

- ✅ **RepoMemory（进程内记忆）**：LLM 合并 → 回写 SharedMemory → 下次 Run 读取 → 形成闭环
- ✅ **知识库条目（codeseek）**：LLM 提取 → MCP KnowledgeAdd → 注入时检索 → 形成闭环
- ❌ **SharedMemory 消息历史**：`Compact()` 为空实现，消息仅靠 `maxSize=500` 截断，无 LLM 蒸馏

### 2.7 工具注册与调用链

**注册**（`coding.go:533-588`）：

```go
// 每个 RepoAgent 实例注册
pruneTool := tools.NewPruneHistoryTool(globalCtx.CodeSeekMCP, llm)
tools.NewAdapter("prune_history", ..., pruneTool.Execute)

if sourceAgent != "" && knowledgeType != "" {
    consolidateTool := tools.NewConsolidateKnowledgeTool(globalCtx.CodeSeekMCP, llm, sourceAgent, knowledgeType)
    tools.NewAdapter("consolidate_knowledge", ..., consolidateTool.Execute)
}
// Director 不注册 consolidate_knowledge（sourceAgent=""）
```

**调用链**：

```
LLM 调用 prune_history(action="merge")
  → PruneHistoryTool.Execute()
  → executeMerge()
  → MCP.KnowledgeList(limit=200)
  → 循环: MCP.KnowledgeSearch(A.Content, limit=5, rerank=true)
  → LLM mergeKnowledgeLLM(A, B)
  → MCP.KnowledgeAdd(merged)
  → MCP.KnowledgeDelete(A.ID)
  → MCP.KnowledgeDelete(B.ID)

LLM 调用 consolidate_knowledge(title, content, tags)
  → ConsolidateKnowledgeTool.Execute()
  → distillContent() (if len>1500)
  → MCP.KnowledgeSearch(title, limit=5, rerank=true) → 去重检测 (threshold=0.85)
  → mergeKnowledgeLLM() (if dup found) → Add + Delete
  → MCP.KnowledgeAdd() (normal add)
```

---

## 3. 问题诊断与根因分析

### 3.1 已识别的 13 项缺陷清单

#### 3.1.1 严重缺陷

| # | 位置 | 问题 | 影响 |
|---|------|------|------|
| 1 | `memory/shared.go:163` | `Compact()` **空实现**，仅返回 `nil`，注释写着 "Future: use LLM to summarize" | 共享消息历史无法压缩，只能靠 `maxSize=500` 硬截断，丢失早期上下文 |
| 2 | `memory/shared.go:48-52` | `persistPath`/`persistTick`/`persistDone` 字段存在但**从未初始化/使用** | SharedMemory 无磁盘持久化，进程重启后所有 KV（包括 repo memory）丢失 |
| 3 | `agents/repo_memory.go` | `RepoMemoryStore` 无持久化机制，`Save()` 仅写入内存 KV | 仓库记忆重启即丢失，与设计文档"跨会话知识积累"矛盾 |

#### 3.1.2 逻辑缺陷

| # | 位置 | 问题 |
|---|------|------|
| 4 | `tools/knowledge.go:135` | 去重阈值 `0.85` 硬编码，无配置项；prune merge 默认 `0.80` 可通过参数覆盖，二者不一致 |
| 5 | `agents/consolidation_worker.go:152-158` | `ValidateMemoryFormat()` 只检查 6 个分区标题是否存在，**不验证内容质量**；LLM 返回空内容时仅 Warn 不重试 |
| 6 | `agents/consolidation_worker.go:339-344` | `callConsolidationLLM` 中 `defer cancel()` 在循环内，**每次迭代都 defer 了 cancel**，ctx 在循环外也声明了，可能导致 ctx 提前取消或泄漏 |
| 7 | `agents/repo_memory.go:192-210` | `EnforceTokenBudget` 用 `len(content)/4` 估算 token，中文环境下严重不准（中文 1 字符≈1 token），实际预算可能超 4 倍 |
| 8 | `tools/knowledge.go:129-133` | `distillContent` 仅在 `len(content) > 1500` 时调用 LLM，否则硬截断；但硬截断后仍追加 `"..."`，实际输出 1503 字符，违反 1500 限制 |
| 9 | `agents/consolidation_worker.go:431` | `pruneTriggerInterval = 10`，每 10 次 consolidation 触发一次全量 merge，**无上限保护**，条目越多 merge 越慢 |

#### 3.1.3 设计缺陷

| # | 位置 | 问题 |
|---|------|------|
| 10 | `memory/shared.go:Publish` | 溢出截断是**头部截断**（`messages[overflow:]`），丢弃最旧的消息；但 `GetContext()` 按顺序拼接，截断后上下文断裂 |
| 11 | `agents/result_compressor.go` | `SharedMemoryStore.Store()` 用 `Publish()` 存消息，但 `Retrieve()` 线性遍历所有消息找 `kv_key` metadata；消息被截断后可能找不到（**数据丢失风险**） |
| 12 | `knowledge/injector.go` | `InjectionMinScore` 默认 0.3，但 `consolidate_knowledge` 去重用 0.85，两个阈值语义不同但无文档说明 |
| 13 | `agents/knowledge_hook.go` | `autoConsolidateSubtask` 使用固定 tags `[sourceAgent, "auto", "子任务总结"]`，无去重，同类型子任务会重复写入知识库 |

### 3.2 超越 13 项的深层机制问题

| 编号 | 深层问题 | 影响 |
|------|----------|------|
| **D1** | 去重以 title 为 query 检索，但知识条目的 title 可能与内容语义不同步 | 同一问题不同标题的知识无法去重，去重召回率低 |
| **D2** | merge 是破坏性的（Add new + Delete old），无回滚机制 | LLM 合并失败/质量差时原始知识永久丢失（parent_ids 记录了但无恢复路径） |
| **D3** | ConsolidationWorker 的 LLM merge 和 extractKnowledge 是两次独立调用 | 成本翻倍；两次调用间上下文割裂，extract 看不到 merge 的推理过程 |
| **D4** | 注入只按 rerank_score 排序，无多样性保证 | 可能注入 5 条关于同一函数的知识，浪费 token 预算 |
| **D5** | AccessCount/LastAccessed 字段存在但未反馈到注入排序或质量评估 | 无使用反馈闭环，热门知识和死知识同等对待 |
| **D6** | Confidence 字段来源不明（LLM 自评？默认值？），无校准机制 | 质量评分不可靠 |
| **D7** | TruncateObservations(10000 char) 硬截断，不分优先级 | 最近的、最相关的观察可能被截掉 |
| **D8** | 无知识类型分类（架构决策 vs 代码片段 vs bug 模式），所有知识统一处理 | 不同类型应有不同 TTL、去重阈值、注入优先级 |

### 3.3 问题分层

| 层次 | 性质 | 涉及缺陷 | 根因 |
|------|------|----------|------|
| **L0 致命层** | 数据完整性/持久性机制缺失 | #1, #2, #3, #6, #10 | SharedMemory 作为知识系统的存储基座，既无磁盘持久化也无正确的内存管理；上下文取消 bug 可静默中断整理流程。整个知识系统建在沙上 |
| **L1 质量层** | 知识准入无有效门禁 | #5, #7, #8, #12 | 验证只查格式不查内容；token 估算中文场景偏差 4 倍；截断违反硬约束；自动沉淀无去重。导致知识库会被低质量/重复内容污染 |
| **L2 效率层** | 规模化退化 | #4, #9, #11, #13 | 阈值硬编码不可配置；prune 是 O(n²) 全量比较；无 TTL 导致无限增长；语义文档缺失。系统越用越慢、越用越脏 |

### 3.4 因果链分析

```
L0: 无持久化 (#2,#3)
  └→ 重启丢失全部 repo memory + KV
      └→ ConsolidationWorker 每次从零开始 → LLM 成本浪费
          └→ 知识积累断裂，注入质量持续低位

L0: Compact() 空实现 + 头部截断 (#1,#10)
  └→ 上下文断裂 → LLM 整理输入不完整
      └→ 合并质量下降 → 提取的知识条目质量差
      └→ ResultCompressor Retrieve 找不到 kv_key → 数据丢失

L1: ValidateMemoryFormat 只查标题 (#5)
  └→ LLM 返回空内容/garbage 仅 Warn → 脏数据入库
      └→ 注入阶段把脏数据注入 prompt → 污染下游 Agent 决策
          └→ 任务执行偏差 → 反馈缺失 (#13 无 TTL) → 脏数据永驻

L1: token 估算偏差 4x (#7) + 截断越界 (#8)
  └→ EnforceTokenBudget 形同虚设
      └→ 超长 memory 入 SharedMemory KV → 加剧截断问题
      └→ 超长 knowledge 入 LanceDB → 注入时 1000 token 预算浪费在单条

L2: prune O(n²) (#9) + 无 TTL (#13)
  └→ 知识库增长 → prune 耗时指数上升
      └→ ConsolidationWorker 每 10 次触发 → 阻塞主流程
      └→ 知识库只增不减 → 注入噪声比上升 → 检索精度下降
```

---

## 4. 系统级影响评估

### 4.1 受影响组件映射

```
┌─────────────────────────────────────────────────────────────────┐
│                    知识子系统依赖图                               │
│                                                                  │
│  ┌──────────────┐    ┌───────────────┐    ┌──────────────┐     │
│  │ ConsolWorker │───→│ SharedMemory  │───→│ (无磁盘)     │     │
│  │  .process()  │    │  KV (内存)    │    │  重启即丢失   │     │
│  └──────┬───────┘    └───────┬───────┘    └──────────────┘     │
│         │                    │                                   │
│         │            ┌───────▼───────┐                          │
│         │            │RepoMemoryStore│                          │
│         │            │  .Save()      │ ← 仅写内存 KV             │
│         │            └───────────────┘                          │
│         │                                                       │
│  ┌──────▼───────┐    ┌───────────────┐    ┌──────────────┐     │
│  │EnforceToken  │    │ValidateMemory │    │extractKnow   │     │
│  │Budget(/4)    │    │Format(标题)   │    │ledge(LLM)    │     │
│  │ ⚠ 中文4x偏差 │    │ ⚠ 无内容校验  │    │ ⚠ 独立调用    │     │
│  └──────────────┘    └───────────────┘    └──────┬───────┘     │
│                                                  │              │
│  ▼  MCP JSON-RPC                                                │
│ ┌────────────────────────────────────────────────────────────┐  │
│ │ codeseek Rust: LanceDB + BM25 + Cross-Encoder Reranker    │  │
│ │ Qwen3-Embedding-4B (2560-dim)                              │  │
│ └────────────────────────┬───────────────────────────────────┘  │
│                          │                                      │
│  ┌───────────────────────────────────────────────────────────┐  │
│  │  consolidate_knowledge        prune_history              │   │
│  │  (title query, 0.85 阈值)     (O(n²), 0.80 阈值)        │   │
│  │  ⚠ 破坏性合并无回滚           ⚠ 无上限保护              │   │
│  └───────────────────────────────────────────────────────────┘  │
│                                                                  │
│  ┌──────────────────────────────────────────────────────────┐   │
│  │  KnowledgeInjector (score≥0.3, token≤1000, 无多样性)    │   │
│  │  KnowledgeSearch → filter → format → inject              │   │
│  │  ⚠ 无反馈闭环  ⚠ 无动态预算  ⚠ 无 AccessCount 反馈     │   │
│  └──────────────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────────────┘
```

### 4.2 爆炸半径评估

| 组件 | 受影响路径 | 爆炸半径 | 严重度 |
|------|-----------|----------|--------|
| SharedMemory 无持久化 | RepoMemoryStore → 所有 Agent 的记忆 → 注入内容 | **全系统** | 🔴 致命 |
| Compact() 空实现 | ConsolWorker 输入 → LLM 合并质量 → 知识提取质量 | **知识链路** | 🔴 致命 |
| ResultCompressor 截断丢数据 | 大结果存储/检索 → CodingAgent 上下文 | **执行链路** | 🟡 高 |
| token 估算偏差 | memory 预算 → SharedMemory 溢出 → 截断 | **存储链路** | 🟡 高 |
| prune O(n²) | ConsolWorker 每 10 次阻塞 → 主流程延迟 | **性能链路** | 🟠 中 |
| 无 TTL | 知识库无限增长 → 注入噪声比 ↑ → 检索精度 ↓ | **质量链路** | 🟠 中渐进 |
| 去重以 title 查询 | 重复知识积累 → 注入浪费 → Agent 困惑 | **质量链路** | 🟡 高 |

### 4.3 级联效应与隐藏风险

1. **持久化缺失 × 整理成本**：重启后 repo memory 丢失 → ConsolWorker 从空记忆重建 → 每次重建消耗 LLM 调用 → 成本叠加。若一个仓库日均整理 5 次，每次 3 次 LLM 调用，重启一次浪费 15 次调用。

2. **截断 × 数据丢失**：SharedMemory 溢出头部截断 → ResultCompressor 存的大结果 kv_key 丢失 → CodingAgent 找不到之前的结果 → 重复执行相同子任务 → 成本翻倍 + 用户体验下降。

3. **无质量门禁 × 无 TTL**：脏知识入库后无过期机制 → 永驻知识库 → 注入时混入 prompt → Agent 基于错误信息决策 → 任务失败 → 无反馈降权 → 恶性循环。

4. **prune O(n²) × 无 TTL**：知识条目持续增长 → prune 耗时从秒级到分钟级 → ConsolWorker 阻塞 → 整理延迟 → 知识时效性下降。

---

## 5. 约束分析

### 5.1 技术约束

| 约束 | 详情 | 影响 |
|------|------|------|
| Go 编排层 | 需修改 SharedMemory、ConsolidationWorker、工具实现 | 所有 Go 层改动需保证编译兼容 |
| codeseek Rust 引擎 | LanceDB knowledge 表 schema 已定（KnowledgeRecord 字段） | 新增字段需改 Rust 端 schema + Go 端映射 |
| MCP JSON-RPC 协议 | Go ↔ Rust 通信走 JSON-RPC | 新增接口需定义 MCP method |
| Embedding 模型 | Qwen3-Embedding-4B 2560 维 | 本地推理，延迟 ~50-200ms/query；无法轻易更换 |
| Cross-Encoder reranker | 已集成 | 可复用为去重相似度计算 |
| 知识格式 | 6 分区 Markdown + KnowledgeRecord 结构化 | 需保持向后兼容 |

### 5.2 资源约束

| 约束 | 详情 |
|------|------|
| LLM 调用成本 | 每次整理 cycle 当前 2-3 次调用（merge + extract + 可能的 prune merge），需降低 |
| LLM 延迟 | 30s 超时，2 次重试 → 最坏 90s 阻塞 |
| 内存 | SharedMemory maxSize=500 条消息，LanceDB 在 Rust 进程内 |
| Embedding 计算 | 每条知识需 2560 维向量，批量插入时是瓶颈 |

### 5.3 组织约束

| 约束 | 详情 |
|------|------|
| 跨语言协作 | Go ↔ Rust，需两侧同步修改 |
| 向后兼容 | 已有知识库数据不能丢，schema 变更需 migration |
| 团队能力 | 需要理解 LLM prompt 工程 + 向量检索 + Go 并发 |

### 5.4 风险约束

| 风险 | 详情 |
|------|------|
| 数据完整性 | merge 是破坏性的，需保证原子性 |
| 知识丢失 | 持久化/迁移过程中不能丢数据 |
| 一致性 | SharedMemory KV 与 LanceDB 之间的双写一致性 |

### 5.5 显式假设

1. codeseek Rust 引擎的 `KnowledgeSearch` 已支持以任意文本为 query（不限于 title）。
2. `KnowledgeRecord` 的 `ParentIDs` 字段已持久化到 LanceDB，可用于软删除追溯。
3. SharedMemory 的 `persistPath` 等字段已有定义但未初始化，说明**设计意图是支持持久化的**，只是未实现。
4. MCP 协议可扩展新增 method（如 `KnowledgeBatchAdd`、`KnowledgeUpdate`）。
5. Go 层可引入新依赖（如 tokenizer 库）。

---

## 6. 优化方案设计

### 6.1 方案对比

#### Solution A: 最小修复方案（仅修 bug）

**概述**：逐一修复 13 项缺陷，不改变架构。

| 修复项 | 做法 | 涉及模块 |
|--------|------|----------|
| 持久化 | 初始化 persistPath，实现定期 JSON dump | SharedMemory |
| Compact | 实现摘要式压缩而非截断 | SharedMemory |
| token 估算 | 引入 tiktoken Go 库 | EnforceTokenBudget |
| 截断越界 | 先截断到 1497 再追加 "..." | distillContent |
| 阈值配置 | 提取为常量/配置项 | consolidate_knowledge, prune_history |
| ctx 取消 | 移 defer cancel() 到循环外 | callConsolidationLLM |
| TTL | 添加定期清理 goroutine | 新增 lifecycle manager |

**优点**：改动范围小，风险低，快速见效
**缺点**：不解决深层机制问题（去重策略、质量闭环、注入多样性），技术债累积
**工作量**：Low（1-2 周）｜**风险**：Low

#### Solution B: 机制增强方案（推荐）

**概述**：修复全部 bug + 重构知识整理的核心机制（去重/质量/注入/生命周期/反馈闭环），但不改变整体架构（仍是 Go 编排 + Rust 引擎）。

**优点**：系统性解决根因，长期可维护，知识质量显著提升
**缺点**：改动面大，需分阶段实施，需跨 Go/Rust 协调
**工作量**：High（6-8 周）｜**风险**：Medium

#### Solution C: 架构重设计

**概述**：将知识子系统独立为微服务，引入消息队列（异步整理）、独立质量评估服务、知识版本管理。

**优点**：彻底解耦，可独立扩展
**缺点**：过度工程，当前规模不需要，团队成本极高
**工作量**：Very High（3+ 月）｜**风险**：High

#### 方案对比表

| 维度 | Solution A | Solution B | Solution C |
|------|-----------|-----------|-----------|
| 修 bug | ✅ 全部 | ✅ 全部 | ✅ 全部 |
| 机制改进 | ❌ | ✅ 深度 | ✅ 彻底 |
| 成本 | Low | Medium | Very High |
| 风险 | Low | Medium | High |
| 长期价值 | 有限 | 高 | 高但过度 |
| 推荐度 | ⭐⭐ | ⭐⭐⭐⭐⭐ | ⭐⭐ |

**决策**：选择 **Solution B（机制增强方案）**。理由：Solution A 不解决 D1-D8 深层问题，技术债持续累积；Solution C 对当前规模过度，引入不必要的分布式复杂度；Solution B 在现有架构上做机制升级，投入产出比最优。

### 6.2 优化 O1: SharedMemory 磁盘持久化与安全压缩

- **解决缺陷**：#1, #2, #3, #10
- **涉及模块**：`SharedMemory`、`RepoMemoryStore`、`ResultCompressor`
- **优先级**：P0

#### (1) 持久化初始化

```go
// SharedMemory 初始化时
func NewSharedMemory(maxSize int, persistPath string) *SharedMemory {
    sm := &SharedMemory{
        maxSize:     maxSize,
        persistPath: persistPath,
        persistTick: 30 * time.Second,  // 每 30s 落盘
        persistDone: make(chan struct{}),
        kv:          make(map[string]string),
        // ...
    }
    sm.loadFromDisk()  // 启动时恢复
    go sm.persistLoop()
    return sm
}

func (sm *SharedMemory) loadFromDisk() error {
    data, err := os.ReadFile(sm.persistPath)
    if err != nil { return err }
    var snapshot struct {
        KV       map[string]string `json:"kv"`
        Messages []Message         `json:"messages"`
    }
    if err := json.Unmarshal(data, &snapshot); err != nil { return err }
    sm.kv = snapshot.KV
    sm.messages = snapshot.Messages
    return nil
}

func (sm *SharedMemory) persistLoop() {
    ticker := time.NewTicker(sm.persistTick)
    defer ticker.Stop()
    for {
        select {
        case <-ticker.C:
            sm.persistToDisk()
        case <-sm.persistDone:
            sm.persistToDisk()  // 退出前最终落盘
            return
        }
    }
}

func (sm *SharedMemory) persistToDisk() error {
    sm.mu.RLock()
    defer sm.mu.RUnlock()
    snapshot := struct {
        KV       map[string]string `json:"kv"`
        Messages []Message         `json:"messages"`
    }{KV: sm.kv, Messages: sm.messages}
    data, _ := json.Marshal(snapshot)
    // 原子写入：先写临时文件再 rename
    tmpPath := sm.persistPath + ".tmp"
    os.WriteFile(tmpPath, data, 0644)
    return os.Rename(tmpPath, sm.persistPath)
}
```

#### (2) Compact() 实现摘要式压缩

```go
func (sm *SharedMemory) Compact() {
    sm.mu.Lock()
    defer sm.mu.Unlock()

    if len(sm.messages) <= sm.maxSize {
        return
    }

    // 保留最近 maxSize/2 条消息完整
    keepCount := sm.maxSize / 2
    oldMessages := sm.messages[:len(sm.messages)-keepCount]
    recentMessages := sm.messages[len(sm.messages)-keepCount:]

    // 对旧消息按角色分组摘要（不调用 LLM，用规则压缩）
    summary := sm.summarizeOldMessages(oldMessages)

    // 构建压缩后的消息列表：[摘要消息] + [最近消息]
    sm.messages = append([]Message{summary}, recentMessages...)
}

func (sm *SharedMemory) summarizeOldMessages(msgs []Message) Message {
    // 规则式压缩：保留 user 消息要点 + assistant 消息结论
    var sb strings.Builder
    sb.WriteString("[历史上下文摘要]\n")
    for _, m := range msgs {
        if m.Role == "user" {
            // 保留 user 消息前 200 字符
            content := m.Content
            if len(content) > 200 { content = content[:200] + "..." }
            sb.WriteString(fmt.Sprintf("用户: %s\n", content))
        } else if m.Role == "assistant" {
            // 保留 assistant 消息最后 200 字符（通常含结论）
            content := m.Content
            if len(content) > 200 { content = content[len(content)-200:] }
            sb.WriteString(fmt.Sprintf("助手: %s\n", content))
        }
    }
    return Message{Role: "system", Content: sb.String()}
}
```

#### (3) ResultCompressor 存储隔离

```go
// ResultCompressor 不再走 Publish/Subscribe 消息流
// 改为直接使用独立的 KV namespace
type ResultCompressor struct {
    storage *SharedMemory  // 使用 "result:" 前缀的 KV namespace
}

func (rc *ResultCompressor) Store(key string, content string) error {
    // 存到 KV 而非消息流，避免被 Compact 截断
    return rc.storage.SetKV("result:"+key, content)
}

func (rc *ResultCompressor) Retrieve(key string) (string, error) {
    return rc.storage.GetKV("result:" + key)
}
```

**预期收益**：重启零数据丢失；上下文压缩不再断裂；ResultCompressor 数据可靠
**实现成本**：Medium（~3 天）
**验证**：重启后检查 KV 完整性；发送 >500 条消息后检查上下文连贯性

### 6.3 优化 O2: Token 估算修正与预算前置

- **解决缺陷**：#7, #8
- **涉及模块**：`EnforceTokenBudget`、`distillContent`、ConsolidationWorker
- **优先级**：P0

#### (1) 语言感知的 token 估算

```go
// 替代 len(content)/4
func estimateTokens(content string) int {
    runeCount := utf8.RuneCountInString(content)
    // 统计 CJK 字符比例
    cjkCount := 0
    for _, r := range content {
        if unicode.Is(unicode.Han, r) ||
           unicode.Is(unicode.Hiragana, r) ||
           unicode.Is(unicode.Katakana, r) {
            cjkCount++
        }
    }
    cjkRatio := float64(cjkCount) / float64(runeCount)

    if cjkRatio > 0.3 {
        // 中文为主：~1.2 token/字符（含标点、代码混合）
        return int(float64(runeCount) * 1.2)
    }
    // 英文为主：~0.25 token/字符（~4 字符/token）
    return int(float64(runeCount) / 4.0)
}
```

#### (2) 预算前置到 LLM prompt

```go
// 不再事后截断，而是在 prompt 中告知 LLM 预算约束
func buildConsolidationPrompt(oldMemory string, observations []string) string {
    return fmt.Sprintf(`你是一个知识整理专家。请合并以下记忆。

**硬性约束**：
- 总输出不超过 %d tokens（约 %d 个中文字符）
- 每个分区内容必须完整、连贯，不得在句中截断
- 如内容超出预算，优先保留：最近/最重要的信息，丢弃过时信息

**现有记忆**：
%s

**新观察**：
%s

请输出 6 分区的 Markdown 格式记忆。`,
        TOKEN_BUDGET, TOKEN_BUDGET/1.2,
        oldMemory,
        strings.Join(observations, "\n---\n"))
}

// 事后仍做安全网检查，但使用正确的估算
func EnforceTokenBudget(content string, maxTokens int) string {
    if estimateTokens(content) <= maxTokens {
        return content
    }
    // 按分区粒度截断，非字符级截断
    return truncateBySection(content, maxTokens)
}

func truncateBySection(content string, maxTokens int) string {
    sections := splitByMarkdownHeader(content)
    budget := maxTokens
    var result []string
    for _, sec := range sections {
        secTokens := estimateTokens(sec)
        if secTokens <= budget {
            result = append(result, sec)
            budget -= secTokens
        } else {
            // 在分区内容级别截断，保证标题完整
            truncated := truncateRuneAware(sec, budget)
            result = append(result, truncated)
            break
        }
    }
    return strings.Join(result, "\n\n")
}
```

#### (3) distillContent 截断修正

```go
func distillContent(content string, maxChars int) string {
    const ellipsis = "..."
    if len([]rune(content)) <= maxChars {
        return content
    }
    // 预留 ellipsis 空间
    limit := maxChars - len([]rune(ellipsis))
    runes := []rune(content)
    // 按句号/换行截断，保证语义完整
    cutAt := findLastSentenceBoundary(runes, limit)
    return string(runes[:cutAt]) + ellipsis
}
```

**预期收益**：token 预算准确；不再超限；LLM 输出更完整
**实现成本**：Low（~1 天）

### 6.4 优化 O3: Context 取消修复

- **解决缺陷**：#6
- **涉及模块**：`callConsolidationLLM`
- **优先级**：P0

```go
func callConsolidationLLM(ctx context.Context, prompt string) (string, error) {
    // 创建独立 context，不依赖循环 context
    callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
    defer cancel()  // defer 紧跟创建，确保每次调用都清理

    var lastErr error
    for attempt := 0; attempt < 2; attempt++ {
        result, err := llmClient.Call(callCtx, prompt)
        if err == nil {
            return result, nil
        }
        lastErr = err
        if callCtx.Err() != nil {
            break  // context 已取消/超时，不再重试
        }
        time.Sleep(time.Duration(attempt+1) * time.Second)
    }
    return "", fmt.Errorf("consolidation LLM failed after retries: %w", lastErr)
}
```

**预期收益**：消除 ctx 提前取消/泄漏
**实现成本**：Low（~0.5 天）

### 6.5 优化 O4: 多信号去重与分级合并策略

- **解决缺陷**：#4, D1, D2
- **涉及模块**：`consolidate_knowledge`、`prune_history`、新增 `DedupEngine`
- **优先级**：P1

#### 当前问题

```
当前: title as query → top5 → rerank_score > 0.85 → LLM merge → Add+Delete
问题: 1) title 可能与内容语义不同步
      2) 单一 rerank_score 无法区分"同主题不同细节"和"真正重复"
      3) 破坏性合并无回滚
```

#### (1) 多信号相似度评分

```go
type DedupScore struct {
    ContentSim    float64  // 内容 embedding 余弦相似度
    TitleSim      float64  // 标题 embedding 相似度
    TagJaccard    float64  // tags Jaccard 相似度
    FileOverlap   float64  // RelatedFiles 重叠率
    CombinedScore float64  // 加权综合
}

func computeDedupScore(a, b *KnowledgeRecord) DedupScore {
    contentSim := cosineSim(a.Embedding, b.Embedding)  // 复用 LanceDB 已有 embedding
    titleSim := textSimilarity(a.Title, b.Title)        // BM25 或编辑距离
    tagJac := jaccard(a.Tags, b.Tags)
    fileOverlap := overlapRatio(a.RelatedFiles, b.RelatedFiles)

    combined := 0.45*contentSim + 0.20*titleSim +
                0.15*tagJac + 0.20*fileOverlap

    return DedupScore{contentSim, titleSim, tagJac, fileOverlap, combined}
}
```

#### (2) 三级处理策略

```go
const (
    ThresholdAutoMerge = 0.90  // 高置信度，自动合并（无 LLM）
    ThresholdLLMMerge  = 0.75  // 中置信度，LLM 辅助决策
    ThresholdNoMerge   = 0.60  // 低置信度，标记关联但不合并
)

func (e *DedupEngine) Process(newEntry *KnowledgeRecord, candidates []*KnowledgeRecord) (*MergeDecision, error) {
    var bestCandidate *KnowledgeRecord
    var bestScore DedupScore

    for _, c := range candidates {
        score := computeDedupScore(newEntry, c)
        if score.CombinedScore > bestScore.CombinedScore {
            bestScore = score
            bestCandidate = c
        }
    }

    switch {
    case bestScore.CombinedScore >= ThresholdAutoMerge:
        // 自动合并：内容拼接去重（无 LLM 调用）
        merged := autoMergeKnowledge(newEntry, bestCandidate)
        return &MergeDecision{
            Action: ActionAutoMerge,
            Target: bestCandidate,
            Merged: merged,
            Score:  bestScore,
        }, nil

    case bestScore.CombinedScore >= ThresholdLLMMerge:
        // LLM 辅助：让 LLM 判断是否真重复 + 合并
        merged, isDup, err := e.llmAssistedMerge(newEntry, bestCandidate)
        if err != nil {
            return &MergeDecision{Action: ActionAddDirect}, nil  // 失败则直接添加
        }
        if !isDup {
            return &MergeDecision{Action: ActionAddDirect}, nil  // LLM 判定非重复
        }
        return &MergeDecision{
            Action: ActionLLMMerge,
            Target: bestCandidate,
            Merged: merged,
            Score:  bestScore,
        }, nil

    default:
        // 不合并，但记录关联（RelatedFiles 或 ParentIDs）
        return &MergeDecision{
            Action: ActionAddDirect,
            RelatedTo: bestCandidate,  // 可选：记录关联
        }, nil
    }
}
```

#### (3) 原子性事务与软删除

```go
func (e *DedupEngine) ExecuteDecision(decision *MergeDecision) error {
    // 使用两阶段提交模式
    txID := generateTxID()

    // Phase 1: 准备
    // 1. 写入新条目（merged 或 new entry）
    if err := e.mcpClient.KnowledgeAdd(decision.Merged); err != nil {
        return err
    }
    newID := decision.Merged.ID

    // 2. 如果是合并，软删除旧条目（标记 deleted=true，保留 parent_ids 链）
    if decision.Target != nil {
        if err := e.mcpClient.KnowledgeSoftDelete(decision.Target.ID, txID); err != nil {
            // 回滚：删除新条目
            e.mcpClient.KnowledgeDelete(newID)
            return err
        }
    }

    // Phase 2: 提交（清理软删除标记或延迟清理）
    e.mcpClient.KnowledgeCommit(txID)

    // 记录操作日志用于回滚
    e.logOperation(txID, "merge", decision)

    return nil
}
```

#### (4) 阈值配置化

```go
type DedupConfig struct {
    ThresholdAutoMerge  float64 `yaml:"threshold_auto_merge" default:"0.90"`
    ThresholdLLMMerge   float64 `yaml:"threshold_llm_merge" default:"0.75"`
    SearchTopK          int     `yaml:"search_topk" default:"10"`
    QueryField          string  `yaml:"query_field" default:"content"`  // content 而非 title
    EnableFileOverlap   bool    `yaml:"enable_file_overlap" default:"true"`
}

// 两种工具共享配置
var globalDedupConfig = loadDedupConfig()

// consolidate_knowledge 和 prune_history 均使用 globalDedupConfig
```

**预期收益**：去重召回率提升（content-based 检索 + 多信号）；LLM 调用减少（高置信度自动合并不调 LLM）；数据安全（软删除 + 回滚）；阈值统一且可配置
**实现成本**：Medium-High（~5 天）
**涉及 Rust 侧**：新增 `KnowledgeSoftDelete`、`KnowledgeCommit` MCP method

### 6.6 优化 O5: Consolidation + Extract 合并为单次 LLM 调用

- **解决缺陷**：D3, D7
- **涉及模块**：`ConsolidationWorker.process()`
- **优先级**：P1

#### 当前流程 vs 优化流程

```
当前（2 次 LLM 调用）:
  读旧记忆 → TruncateObservations(10000硬截断) → LLM merge → EnforceTokenBudget
  → ValidateMemoryFormat → Save → LLM extract → KnowledgeAdd

优化（1 次 LLM 调用）:
  读旧记忆 → 智能截断(保留近期+摘要旧) → LLM merge+extract(单次结构化输出)
  → 质量门禁 → 原子保存 → 增量去重 → KnowledgeAdd
```

#### 具体实现

```go
func (w *ConsolidationWorker) process(ctx context.Context) error {
    // 1. 读取旧记忆（结构化，非纯文本）
    oldMemory := w.repoMemoryStore.Load()

    // 2. 智能截断：保留最近 N 条 + 对更早的做规则摘要
    observations := w.sharedMemory.GetRecentObservations(50)
    olderObs := w.sharedMemory.GetOlderObservations(50, 200)
    olderSummary := summarizeObservations(olderObs)  // 规则式，非 LLM

    // 3. 单次 LLM 调用：合并 + 提取知识（结构化 JSON 输出）
    prompt := w.buildUnifiedPrompt(oldMemory, observations, olderSummary)
    result, err := w.callConsolidationLLM(ctx, prompt)
    if err != nil {
        return err
    }

    // 4. 解析结构化输出
    var unified struct {
        Memory    string             `json:"memory"`      // 6 分区 Markdown
        Knowledge []KnowledgeRecord  `json:"knowledge"`   // 提取的知识条目
    }
    if err := json.Unmarshal(result, &unified); err != nil {
        // 降级：尝试分别解析
        return w.fallbackSeparateParse(result)
    }

    // 5. 质量门禁（见 O6）
    validatedMemory, rejectedMemory := w.validateMemory(unified.Memory)
    if validatedMemory == "" {
        return fmt.Errorf("memory validation failed, rejected: %s", rejectedMemory)
    }

    validKnowledge := w.filterValidKnowledge(unified.Knowledge)

    // 6. 原子保存
    w.repoMemoryStore.Save(validatedMemory)

    // 7. 增量去重 + 入库
    for _, k := range validKnowledge {
        decision := w.dedupEngine.Process(&k, w.searchCandidates(k, globalDedupConfig.SearchTopK))
        w.dedupEngine.ExecuteDecision(decision)
    }

    // 8. 增量 prune（见 O7）
    if w.consolidationCount % w.pruneInterval == 0 {
        w.incrementalPrune()
    }

    return nil
}

func (w *ConsolidationWorker) buildUnifiedPrompt(old, recent, older string) string {
    return fmt.Sprintf(`你是知识整理专家。请完成两个任务：

## 任务 1: 合并仓库记忆
将现有记忆与新观察合并为 6 分区 Markdown。
**预算**: 总输出 ≤ %d tokens。超出时优先保留最新/最重要信息。
**6 分区**: 架构概览、关键模块、数据流、依赖关系、已知问题、历史决策

## 任务 2: 提取知识条目
从新观察中提取可复用的知识条目。每条需：
- title: ≤200 字符，一句话概括
- content: ≤1500 字符，只记核心
- tags: ≥1 个
- related_files: 必含文件路径或函数名
- type: architecture|code_pattern|bug_pattern|api_usage|config|decision

## 现有记忆
%s

## 新观察
%s

## 更早观察摘要
%s

## 输出格式（严格 JSON）
{
  "memory": "6 分区 Markdown 文本",
  "knowledge": [
    {"title":"...","content":"...","tags":["..."],"related_files":["..."],"type":"..."}
  ]
}`, TOKEN_BUDGET, old, recent, older)
}
```

**预期收益**：LLM 调用减半（2→1）；extract 能看到 merge 的完整上下文，知识提取质量更高；智能截断避免丢失近期重要观察
**实现成本**：Medium（~3 天）

### 6.7 优化 O6: 知识质量门禁与评分系统

- **解决缺陷**：#5, #12, D6
- **涉及模块**：新增 `QualityGate`、`ConsolidationWorker`、`autoConsolidateSubtask`
- **优先级**：P1

#### 质量门禁设计

```go
type QualityValidator struct{}

type ValidationResult struct {
    Passed   bool
    Score    float64  // 0.0 - 1.0
    Reasons  []string
}

func (v *QualityValidator) ValidateKnowledge(k *KnowledgeRecord) ValidationResult {
    var reasons []string
    score := 0.0

    // 硬性规则（任一不过则拒绝）
    if len(k.Title) == 0 || len(k.Title) > 200 {
        return ValidationResult{Passed: false, Reasons: append(reasons, "title 无效")}
    }
    if len(k.Content) == 0 || len(k.Content) > 1500 {
        return ValidationResult{Passed: false, Reasons: append(reasons, "content 无效或超长")}
    }
    if len(k.Tags) < 1 {
        return ValidationResult{Passed: false, Reasons: append(reasons, "tags 为空")}
    }
    if !hasFileOrFunctionRef(k.Content, k.RelatedFiles) {
        return ValidationResult{Passed: false, Reasons: append(reasons, "缺少文件路径/函数名坐标")}
    }

    // 软性规则（影响分数但不拒绝）
    score += 0.2  // 基础分（通过了硬性规则）

    if len(k.Title) <= 100 { score += 0.1; }  // 标题简洁
    if len(k.Content) >= 100 && len(k.Content) <= 800 { score += 0.15; }  // 内容适中
    if len(k.Tags) >= 2 { score += 0.1; }  // 多标签
    if k.Type != "" { score += 0.1; }  // 有类型
    if len(k.RelatedFiles) >= 1 { score += 0.15; }  // 有文件坐标
    if !isVagueContent(k.Content) { score += 0.1; }  // 非空泛内容
    if k.Confidence > 0.5 { score += 0.1; }  // LLM 自信度

    return ValidationResult{Passed: true, Score: score, Reasons: reasons}
}

func isVagueContent(content string) bool {
    vaguePatterns := []string{"可能", "大概", "似乎", "不确定", "TODO", "FIXME"}
    lowerContent := strings.ToLower(content)
    for _, p := range vaguePatterns {
        if strings.Contains(lowerContent, strings.ToLower(p)) {
            count := strings.Count(lowerContent, strings.ToLower(p))
            if count > 2 { return true }
        }
    }
    // 内容过短也判定为空泛
    if utf8.RuneCountInString(content) < 50 { return true }
    return false
}

func hasFileOrFunctionRef(content string, files []string) bool {
    if len(files) > 0 { return true }
    // 检查内容中是否包含文件路径模式（如 src/xxx.go）或函数名模式
    filePattern := regexp.MustCompile(`[\w/]+\.\w+:\d+|func\s+\w+|def\s+\w+`)
    return filePattern.MatchString(content)
}
```

#### 质量评分（动态）

```go
// 知识的质量评分 = 静态质量 + 动态使用反馈
func (q *QualityScorer) ComputeScore(k *KnowledgeRecord) float64 {
    staticScore := k.QualityScore  // 入库时的 ValidationResult.Score

    // 动态因子
    accessFactor := math.Log(float64(k.AccessCount)+1) / 10.0  // 访问频次
    if accessFactor > 0.2 { accessFactor = 0.2 }

    recencyDays := time.Since(k.LastAccessed).Hours() / 24
    recencyFactor := math.Exp(-recencyDays / 30.0) * 0.1  // 30 天衰减

    // 综合
    return staticScore + accessFactor + recencyFactor
}
```

#### autoConsolidateSubtask 去重

```go
func (w *ConsolidationWorker) autoConsolidateSubtask(subtask *Subtask) error {
    // 提取知识
    knowledge := w.extractFromSubtask(subtask)

    for _, k := range knowledge {
        k.Tags = append(k.Tags, w.sourceAgent, "auto", "子任务总结")

        // 质量门禁
        result := w.qualityValidator.ValidateKnowledge(&k)
        if !result.Passed {
            w.logger.Warn("subtask knowledge rejected", "reasons", result.Reasons)
            continue
        }
        k.QualityScore = result.Score

        // 去重（复用 DedupEngine）
        candidates := w.searchCandidates(k, globalDedupConfig.SearchTopK)
        decision := w.dedupEngine.Process(&k, candidates)

        if decision.Action == ActionAddDirect {
            // 对 auto 总结降低 Confidence
            k.Confidence = min(k.Confidence, 0.6)
        }

        w.dedupEngine.ExecuteDecision(decision)
    }
    return nil
}
```

**预期收益**：脏知识拦截率 >80%；自动沉淀不再重复写入；质量评分可反馈到注入
**实现成本**：Medium（~3 天）

### 6.8 优化 O7: 增量 Prune 与批量处理

- **解决缺陷**：#9, #13
- **涉及模块**：`prune_history`、`ConsolidationWorker`
- **优先级**：P1

#### 当前问题

```
当前 prune: KnowledgeList(limit=200) → 每条 search top5 → LLM merge
时间复杂度: O(n × 5 × LLM_call) → n=200 时最多 1000 次 LLM 调用
```

#### (1) 增量 prune（只处理新增条目）

```go
func (w *ConsolidationWorker) incrementalPrune() error {
    // 只获取上次 prune 后新增的条目
    newEntries := w.mcpClient.KnowledgeList(KnowledgeListRequest{
        Since: w.lastPruneTime,
        Limit: 50,  // 单次上限
    })

    if len(newEntries) == 0 { return nil }

    for _, entry := range newEntries {
        // 用 content 检索（非 title）
        candidates := w.mcpClient.KnowledgeSearch(KnowledgeSearchRequest{
            Query:  entry.Content,
            TopK:   globalDedupConfig.SearchTopK,
        })

        // 过滤掉自己
        candidates = filterSelf(candidates, entry.ID)

        if len(candidates) == 0 { continue }

        decision := w.dedupEngine.Process(&entry, candidates)
        if decision.Action != ActionAddDirect {
            w.dedupEngine.ExecuteDecision(decision)
        }
    }

    w.lastPruneTime = time.Now()
    return nil
}
```

#### (2) 全量 prune 降级为低频维护

```go
// 全量 prune 改为定时任务（如每天一次），且使用聚类降低比较量
func (w *ConsolidationWorker) fullPrune() error {
    allEntries := w.mcpClient.KnowledgeList(KnowledgeListRequest{Limit: 1000})

    // 按 tag 聚类，只比较同 tag 内的条目
    clusters := clusterByTag(allEntries)

    for _, cluster := range clusters {
        if len(cluster) < 2 { continue }

        // 簇内两两比较（簇大小通常 << n）
        for i := 0; i < len(cluster); i++ {
            for j := i+1; j < len(cluster); j++ {
                score := computeDedupScore(cluster[i], cluster[j])
                if score.CombinedScore >= ThresholdAutoMerge {
                    // 自动合并
                    merged := autoMergeKnowledge(cluster[i], cluster[j])
                    w.dedupEngine.ExecuteDecision(&MergeDecision{
                        Action: ActionAutoMerge,
                        Target: cluster[j],
                        Merged: merged,
                    })
                    cluster = removeFromCluster(cluster, j)
                    j--
                }
                // 中等相似度的不在全量 prune 中调 LLM（成本太高）
                // 留给增量 prune 处理
            }
        }
    }
    return nil
}
```

#### (3) prune 频率自适应

```go
func (w *ConsolidationWorker) shouldPrune() bool {
    count := w.mcpClient.KnowledgeCount()

    // 增量 prune: 每次整理后检查
    if w.consolidationCount % 5 == 0 {  // 从 10 改为 5
        return true
    }

    // 全量 prune: 当条目数超过阈值或距上次 >24h
    if count > 500 && time.Since(w.lastFullPrune) > 24*time.Hour {
        return true
    }

    return false
}
```

**预期收益**：prune 从 O(n²) 降为 O(n×k)（增量）或 O(Σ|cluster_i|²)（全量聚类）；LLM 调用大幅减少
**实现成本**：Medium（~3 天）

### 6.9 优化 O8: 智能注入（MMR + 动态预算 + 反馈）

- **解决缺陷**：D4, D5, #11
- **涉及模块**：`KnowledgeInjector`
- **优先级**：P2

#### 当前问题

```
当前: KnowledgeSearch → score≥0.3 过滤 → 按 score 排序 → 截断到 1000 token → 注入
问题: 1) 无多样性：可能注入 5 条关于同一函数的知识
      2) 1000 token 固定预算：简单任务浪费，复杂任务不够
      3) AccessCount/LastAccessed 不影响注入排序
      4) 0.3 阈值无文档说明
```

#### (1) MMR 多样性选择

```go
func (inj *KnowledgeInjector) SelectKnowledge(
    query string,
    candidates []*KnowledgeRecord,
    tokenBudget int,
) []*KnowledgeRecord {
    const lambda = 0.7  // relevance vs diversity 权重

    // 预计算每条候选的 relevance
    relevanceMap := make(map[string]float64)
    for _, c := range candidates {
        // relevance = rerank_score × (1 + access_boost) × (1 + recency_boost)
        rel := c.RerankScore
        rel *= (1.0 + math.Log(float64(c.AccessCount)+1)*0.1)  // 访问频次加权
        rel *= (1.0 + math.Exp(-time.Since(c.LastAccessed).Hours()/168.0)*0.1)  // 7天衰减
        rel *= c.QualityScore  // 质量加权
        relevanceMap[c.ID] = rel
    }

    selected := []*KnowledgeRecord{}
    remaining := make([]*KnowledgeRecord, len(candidates))
    copy(remaining, candidates)

    remainingBudget := tokenBudget

    for len(remaining) > 0 && remainingBudget > 0 {
        bestIdx := -1
        bestScore := -1.0

        for i, c := range remaining {
            rel := relevanceMap[c.ID]

            // 计算与已选条目的最大相似度（多样性惩罚）
            maxSim := 0.0
            for _, s := range selected {
                sim := computeDedupScore(c, s).CombinedScore
                if sim > maxSim { maxSim = sim }
            }

            mmrScore := lambda*rel - (1-lambda)*maxSim
            if mmrScore > bestScore {
                bestScore = mmrScore
                bestIdx = i
            }
        }

        if bestIdx == -1 { break }

        chosen := remaining[bestIdx]
        chosenTokens := estimateTokens(chosen.Content)

        if chosenTokens <= remainingBudget {
            selected = append(selected, chosen)
            remainingBudget -= chosenTokens
        }
        // 无论是否选中，都从 remaining 移除（预算不够就跳过）
        remaining = append(remaining[:bestIdx], remaining[bestIdx+1:]...)
    }

    return selected
}
```

#### (2) 动态 token 预算

```go
func (inj *KnowledgeInjector) computeBudget(taskContext *TaskContext) int {
    baseBudget := 800

    // 根据任务复杂度调整
    switch taskContext.Complexity {
    case "simple":
        baseBudget = 500  // 简单任务少注入
    case "moderate":
        baseBudget = 1000
    case "complex":
        baseBudget = 2000  // 复杂任务多注入
    }

    // 根据知识库大小调整（库小则全注入更多）
    knowledgeCount := inj.mcpClient.KnowledgeCount()
    if knowledgeCount < 20 {
        baseBudget = min(baseBudget+500, knowledgeCount*100)
    }

    // 留出空间给系统 prompt 和对话
    return min(baseBudget, MAX_INJECTION_BUDGET)  // MAX = 3000
}
```

#### (3) 注入格式优化

```go
func (inj *KnowledgeInjector) Format(selected []*KnowledgeRecord) string {
    if len(selected) == 0 { return "" }

    var sb strings.Builder
    sb.WriteString("<knowledge_context>\n")
    sb.WriteString(fmt.Sprintf("<!-- 共 %d 条相关知识，按相关性×多样性选择 -->\n\n", len(selected)))

    for i, k := range selected {
        sb.WriteString(fmt.Sprintf("### [%d] %s\n", i+1, k.Title))
        sb.WriteString(fmt.Sprintf("类型: %s | 置信度: %.2f | 来源: %s\n", k.Type, k.Confidence, k.SourceAgent))
        if len(k.RelatedFiles) > 0 {
            sb.WriteString(fmt.Sprintf("相关文件: %s\n", strings.Join(k.RelatedFiles, ", ")))
        }
        sb.WriteString(fmt.Sprintf("\n%s\n\n", k.Content))
        if len(k.Tags) > 0 {
            sb.WriteString(fmt.Sprintf("标签: %s\n", strings.Join(k.Tags, ", ")))
        }
        sb.WriteString("---\n")
    }

    sb.WriteString("</knowledge_context>\n")
    sb.WriteString("<!-- 以上知识仅供参考，请结合实际代码验证 -->")
    return sb.String()
}
```

#### (4) 注入阈值文档化与配置

```go
type InjectionConfig struct {
    MinScore        float64 `yaml:"min_score" default:"0.3"`     // 最低相关性
    MinQuality      float64 `yaml:"min_quality" default:"0.4"`   // 最低质量分
    LambdaMMR       float64 `yaml:"lambda_mmr" default:"0.7"`    // MMR 多样性权重
    MaxBudget       int     `yaml:"max_budget" default:"3000"`   // 最大 token 预算
    AccessBoost     float64 `yaml:"access_boost" default:"0.1"`  // 访问频次加权
    RecencyHalfLife float64 `yaml:"recency_half_life" default:"168"` // 7天半衰期(小时)
}
// 语义说明：MinScore=0.3 表示"有一定相关性即可入选候选池"，
//          去重阈值 0.85 表示"高度相似才判定为重复"，两者语义不同。
```

**预期收益**：注入多样性提升；token 预算利用率提升；热门/近期知识优先注入
**实现成本**：Medium（~3 天）

### 6.10 优化 O9: 注入反馈闭环

- **解决缺陷**：D5
- **涉及模块**：`KnowledgeInjector`、`DirectorAgent`/`CodingAgent`
- **优先级**：P2

#### 闭环设计

```
注入知识(带 ID 标记) → Agent 执行任务 → 检测知识是否被引用 → 更新 AccessCount/UsefulnessScore
```

```go
// 注入时记录 ID
func (inj *KnowledgeInjector) Inject(ctx *TaskContext) string {
    selected := inj.SelectKnowledge(ctx.Query, ctx.Candidates, inj.computeBudget(ctx))

    // 记录注入的 ID 列表
    inj.injectedIDs = make([]string, len(selected))
    for i, k := range selected {
        inj.injectedIDs[i] = k.ID
    }

    return inj.Format(selected)
}

// 任务完成后回调
func (inj *KnowledgeInjector) OnTaskComplete(taskResult *TaskResult) {
    // 启发式检测：注入的知识是否在 Agent 输出中被引用
    agentOutput := taskResult.Output

    for _, id := range inj.injectedIDs {
        k, _ := inj.mcpClient.KnowledgeGet(id)

        // 检查 Agent 输出是否包含知识的关键信息
        referenced := isKnowledgeReferenced(k, agentOutput)

        // 更新 AccessCount
        inj.mcpClient.KnowledgeUpdateAccess(id, referenced)

        // 更新 UsefulnessScore
        if referenced && taskResult.Success {
            // 知识被引用且任务成功 → 正反馈
            inj.mcpClient.KnowledgeAdjustScore(id, +0.05)
        } else if !referenced {
            // 知识注入但未被引用 → 轻微负反馈
            inj.mcpClient.KnowledgeAdjustScore(id, -0.02)
        }
    }
}

func isKnowledgeReferenced(k *KnowledgeRecord, output string) bool {
    // 检查输出中是否出现知识的 RelatedFiles 或关键术语
    for _, f := range k.RelatedFiles {
        if strings.Contains(output, f) { return true }
    }
    // 检查 title 中的关键名词
    keywords := extractKeywords(k.Title)
    matchCount := 0
    for _, kw := range keywords {
        if strings.Contains(output, kw) { matchCount++ }
    }
    return matchCount >= len(keywords)/2
}
```

**预期收益**：形成"注入→使用→反馈→提升"闭环；高质量知识自动浮现，低质量知识自动衰减
**实现成本**：Medium（~3 天）
**涉及 Rust 侧**：新增 `KnowledgeUpdateAccess`、`KnowledgeAdjustScore` MCP method

### 6.11 优化 O10: 生命周期管理（TTL + 过时检测 + 归档）

- **解决缺陷**：#13
- **涉及模块**：新增 `LifecycleManager`、`ConsolidationWorker`
- **优先级**：P2

#### 设计

```go
type LifecycleConfig struct {
    TTLByType              map[KnowledgeType]time.Duration `yaml:"ttl_by_type"`
    StalenessCheckEnabled  bool `yaml:"staleness_check" default:"true"`
    ArchiveEnabled         bool `yaml:"archive_enabled" default:"true"`
    ArchiveScoreThreshold  float64 `yaml:"archive_score_threshold" default:"0.2"`
    ArchiveAgeThreshold    time.Duration `yaml:"archive_age_threshold" default:"720h"` // 30天
}

var defaultLifecycleConfig = LifecycleConfig{
    TTLByType: map[KnowledgeType]time.Duration{
        KnowledgeTypeCodePattern:    90 * 24 * time.Hour,   // 代码模式: 90天
        KnowledgeTypeArchitecture:   180 * 24 * time.Hour,  // 架构决策: 180天
        KnowledgeTypeBugPattern:     60 * 24 * time.Hour,   // Bug 模式: 60天
        KnowledgeTypeAPIUsage:       90 * 24 * time.Hour,   // API 用法: 90天
        KnowledgeTypeConfig:         30 * 24 * time.Hour,   // 配置: 30天
        KnowledgeTypeDecision:       365 * 24 * time.Hour,  // 决策: 365天
    },
}

type LifecycleManager struct {
    config    LifecycleConfig
    mcpClient MCPClient
    logger    Logger
}

func (lm *LifecycleManager) Run(ctx context.Context) {
    ticker := time.NewTicker(1 * time.Hour)  // 每小时检查一次
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            lm.sweep()
        case <-ctx.Done():
            return
        }
    }
}

func (lm *LifecycleManager) sweep() {
    entries := lm.mcpClient.KnowledgeList(KnowledgeListRequest{Limit: 1000})

    for _, entry := range entries {
        action := lm.evaluate(&entry)
        switch action {
        case ActionExpire:
            lm.mcpClient.KnowledgeSoftDelete(entry.ID, "ttl_expiry")
            lm.logger.Info("knowledge expired", "id", entry.ID, "type", entry.Type)

        case ActionMarkStale:
            lm.mcpClient.KnowledgeMarkStale(entry.ID)
            lm.logger.Info("knowledge marked stale", "id", entry.ID)

        case ActionArchive:
            lm.mcpClient.KnowledgeArchive(entry.ID)
            lm.logger.Info("knowledge archived", "id", entry.ID)

        case ActionKeep:
            // 无操作
        }
    }
}

func (lm *LifecycleManager) evaluate(k *KnowledgeRecord) LifecycleAction {
    // 1. TTL 检查
    ttl, ok := lm.config.TTLByType[k.Type]
    if !ok {
        ttl = 90 * 24 * time.Hour  // 默认 90 天
    }
    if time.Since(k.CreatedAt) > ttl {
        // 超过 TTL，但如果有访问记录则延长
        if time.Since(k.LastAccessed) < 7*24*time.Hour && k.AccessCount > 5 {
            // 最近有访问且访问频次高 → 保留但标记可能过时
            return ActionMarkStale
        }
        return ActionExpire
    }

    // 2. 过时检测：关联文件是否在知识创建后被修改
    if lm.config.StalenessCheckEnabled && len(k.RelatedFiles) > 0 {
        for _, f := range k.RelatedFiles {
            modTime, err := getFileModTime(f)
            if err == nil && modTime.After(k.CreatedAt) {
                return ActionMarkStale
            }
        }
    }

    // 3. 归档检测：低质量 + 长期未访问
    age := time.Since(k.CreatedAt)
    if age > lm.config.ArchiveAgeThreshold {
        scorer := &QualityScorer{}
        score := scorer.ComputeScore(k)
        if score < lm.config.ArchiveScoreThreshold {
            return ActionArchive
        }
    }

    return ActionKeep
}
```

**预期收益**：知识库自动精简；过时知识标记/清理；存储和检索性能长期稳定
**实现成本**：Medium（~3 天）
**涉及 Rust 侧**：新增 `KnowledgeMarkStale`、`KnowledgeArchive` MCP method；KnowledgeRecord 新增 `stale` 字段

### 6.12 优化 O11: 可观测性

- **涉及模块**：全局
- **优先级**：P2

```go
type KnowledgeMetrics struct {
    TotalCount        map[KnowledgeType]int  `json:"total_count"`
    AvgQualityScore   float64                `json:"avg_quality_score"`
    DedupHitRate      float64                `json:"dedup_hit_rate"`      // 去重命中率
    InjectionCount    int                    `json:"injection_count"`
    InjectionRefCount int                    `json:"injection_ref_count"` // 被引用次数
    InjectionRefRate  float64                `json:"injection_ref_rate"`  // 引用率
    MergeSuccessRate  float64                `json:"merge_success_rate"`
    LLMCallCount      int                    `json:"llm_call_count"`
    LLMCallLatency    time.Duration          `json:"llm_call_latency_avg"`
    PruneDuration     time.Duration          `json:"prune_duration_avg"`
    ExpiredCount      int                    `json:"expired_count"`
    ArchivedCount     int                    `json:"archived_count"`
    StaleCount        int                    `json:"stale_count"`
}

type KnowledgeObserver struct {
    metrics KnowledgeMetrics
    mu      sync.Mutex
}

// 在关键路径埋点
func (o *KnowledgeObserver) OnDedupAttempt(decision *MergeDecision) {
    o.mu.Lock()
    defer o.mu.Unlock()
    if decision.Action != ActionAddDirect {
        o.metrics.DedupHitCount++
    }
    o.metrics.DedupTotalCount++
}

func (o *KnowledgeObserver) OnInjection(selected []*KnowledgeRecord) {
    o.mu.Lock()
    defer o.mu.Unlock()
    o.metrics.InjectionCount++
    // 记录注入 ID 供后续引用追踪
}

func (o *KnowledgeObserver) Snapshot() KnowledgeMetrics {
    o.mu.Lock()
    defer o.mu.Unlock()
    // 计算派生指标
    m := o.metrics
    if m.DedupTotalCount > 0 {
        m.DedupHitRate = float64(m.DedupHitCount) / float64(m.DedupTotalCount)
    }
    if m.InjectionCount > 0 {
        m.InjectionRefRate = float64(m.InjectionRefCount) / float64(m.InjectionCount)
    }
    return m
}
```

**预期收益**：知识系统可观测；可量化优化效果；问题快速定位
**实现成本**：Low-Medium（~2 天）

---

## 7. 分阶段实施路线图

### Phase 1: 地基修复（第 1-2 周）

| 序号 | 优化项 | 涉及缺陷 | 工作量 | 验证标准 |
|------|--------|----------|--------|----------|
| 1.1 | O1: SharedMemory 持久化 + Compact | #1,#2,#3,#10 | 3d | 重启后 KV 完整；500+消息后上下文连贯 |
| 1.2 | O2: Token 估算修正 | #7,#8 | 1d | 中文内容 token 估算误差 <15% |
| 1.3 | O3: Context 取消修复 | #6 | 0.5d | 循环 5 次无 ctx 泄漏/提前取消 |
| 1.4 | O4部分: ResultCompressor 存储隔离 | #10 | 1d | 截断后 Retrieve 仍可找到数据 |
| 1.5 | 阈值配置化 | #4 | 0.5d | 去重/prune 阈值可配置 |

**Phase 1 验收**：重启零数据丢失；无 ctx 泄漏；token 预算准确。

### Phase 2: 质量门禁（第 3-4 周）

| 序号 | 优化项 | 涉及缺陷 | 工作量 | 验证标准 |
|------|--------|----------|--------|----------|
| 2.1 | O6: QualityGate 实现 | #5 | 2d | 空内容/garbage 被拦截；空泛内容低分 |
| 2.2 | O6: 质量评分系统 | D6 | 1d | 每条知识有 quality_score |
| 2.3 | O6: autoConsolidateSubtask 去重 | #12 | 1d | 同类型子任务不重复写入 |
| 2.4 | O5: Consolidation+Extract 合并 | D3,D7 | 3d | LLM 调用从 2→1；知识提取质量不降 |

**Phase 2 验收**：脏知识拦截率 >80%；整理 LLM 调用减半。

### Phase 3: 去重与整理优化（第 5-6 周）

| 序号 | 优化项 | 涉及缺陷 | 工作量 | 验证标准 |
|------|--------|----------|--------|----------|
| 3.1 | O4: 多信号 DedupEngine | #4,D1,D2 | 3d | content-based 去重召回率 >90% |
| 3.2 | O4: 软删除与回滚 | D2 | 2d | 合并失败可回滚 |
| 3.3 | O7: 增量 prune | #9 | 2d | 500条知识 prune <5s |
| 3.4 | O7: 全量 prune 聚类 | #9 | 1d | 1000条知识全量 prune <30s |

**Phase 3 验收**：去重准确率 >90%；prune 性能提升 10x+。

### Phase 4: 注入与生命周期（第 7-8 周）

| 序号 | 优化项 | 涉及缺陷 | 工作量 | 验证标准 |
|------|--------|----------|--------|----------|
| 4.1 | O8: MMR 注入 | D4,#11 | 2d | 注入多样性提升（无同主题 >2条） |
| 4.2 | O8: 动态预算 | D4 | 1d | 简单任务 ≤500 token；复杂任务 ≤2000 |
| 4.3 | O9: 注入反馈闭环 | D5 | 3d | AccessCount/UsefulnessScore 正确更新 |
| 4.4 | O10: 生命周期管理 | #13 | 3d | 过期知识自动清理；过时知识标记 |
| 4.5 | O11: 可观测性 | - | 2d | 指标 Dashboard 可用 |

**Phase 4 验收**：注入引用率 >30%（基线待测）；知识库大小稳定（不再无限增长）。

---

## 8. 验证策略

### 8.1 单元测试

| 模块 | 测试用例 |
|------|----------|
| EnforceTokenBudget | 纯中文/纯英文/中英混合/代码混合的 token 估算 |
| QualityGate | 空内容/超长内容/无文件路径/空泛内容/高质量内容 |
| DedupEngine | 同内容不同标题/同标题不同内容/高相似自动合并/中等相似LLM辅助/低相似跳过 |
| MMR注入 | 5条同主题/5条不同主题/预算不足/空候选 |
| LifecycleManager | 超TTL有访问/超TTL无访问/关联文件已修改/低质量长期未访问 |
| Compact | 500条→压缩/跨分区截断/保留摘要完整性 |

### 8.2 集成测试

```
场景 1: 持久化恢复
  1. 启动系统，写入 50 条 KV + 10 条知识
  2. 重启系统
  3. 验证 KV 完整 + 知识可检索

场景 2: 整理闭环
  1. 注入 20 条观察（含重复主题）
  2. 触发 ConsolidationWorker.process()
  3. 验证：LLM 调用 1 次、知识条目正确提取、重复条目去重
  4. 验证：repo memory 已更新且 ≤1500 token

场景 3: 注入反馈
  1. 创建 10 条知识（5条高相关、5条低相关）
  2. 执行任务，Agent 输出引用了 3 条高相关知识
  3. 验证：被引用知识 AccessCount++、UsefulnessScore 提升
  4. 验证：未引用知识 UsefulnessScore 微降

场景 4: 生命周期
  1. 创建 10 条知识（不同 type、不同 age）
  2. 修改部分关联文件
  3. 触发 LifecycleManager.sweep()
  4. 验证：过期清理/过时标记/归档正确
```

### 8.3 性能基准

| 指标 | 基线 | 目标 |
|------|------|------|
| 整理延迟（单次 process） | ~60s（2次LLM） | ~30s（1次LLM） |
| Prune 延迟（200条） | O(n²) ~120s | O(n×k) ~10s |
| 重启恢复时间 | N/A（全部丢失） | <2s |
| 注入 token 利用率 | ~60%（固定1000） | >85%（动态预算） |
| 去重召回率 | ~50%（title-based） | >90%（content+多信号） |

### 8.4 回滚计划

| 阶段 | 回滚策略 |
|------|----------|
| Phase 1 | 持久化：删除磁盘文件即可回退到内存模式；其余为代码修复，可 revert |
| Phase 2 | 质量门禁：添加 feature flag `enable_quality_gate`，关闭后走旧逻辑 |
| Phase 3 | DedupEngine：添加 feature flag `enable_multi_signal_dedup`，关闭后走 title-based |
| Phase 4 | 生命周期：添加 feature flag `enable_lifecycle_manager`，关闭后无自动清理 |

所有 Phase 均通过 feature flag 控制，可独立开关，支持灰度发布。

---

## 9. 优化效果总结矩阵

| 优化领域 | 优化项 | 解决缺陷 | 预期收益 | 成本 | 优先级 |
|----------|--------|----------|----------|------|--------|
| **数据完整性** | O1 持久化+压缩 | #1,2,3,10 | 重启零丢失 | M | P0 |
| | O2 Token修正 | #7,8 | 预算准确 | L | P0 |
| | O3 ctx修复 | #6 | 消除泄漏 | L | P0 |
| **质量门禁** | O6 质量验证 | #5,12,D6 | 脏数据拦截>80% | M | P1 |
| | O5 合并调用 | D3,D7 | LLM成本-50% | M | P1 |
| **去重策略** | O4 多信号去重 | #4,D1,D2 | 召回率>90% | MH | P1 |
| | O7 增量prune | #9,13 | 性能10x | M | P1 |
| **注入质量** | O8 MMR注入 | D4,#11 | 多样性+利用率 | M | P2 |
| | O9 反馈闭环 | D5 | 闭环自优化 | M | P2 |
| **生命周期** | O10 TTL管理 | #13 | 库自动精简 | M | P2 |
| **可观测** | O11 指标 | - | 可量化可定位 | L | P2 |

**总计**：修复 13/13 已知缺陷 + 8/8 深层机制问题；预计 6-8 周完成全部 4 个 Phase。

---

## 10. 附录：关键代码位置索引

| 组件 | 文件路径 | 关键函数/行号 |
|------|----------|---------------|
| RepoAgent.Run | `internal/agents/repo.go` | L98-141，记忆注入点 L108-111 |
| ConsolidationWorker | `internal/agents/consolidation_worker.go` | L50-100（Start/Stop），L102-150（process） |
| RepoMemoryStore | `internal/agents/repo_memory.go` | L60-100（Load/Save），L152-162（RenderMemoryForInjection） |
| SharedMemory.Compact | `internal/memory/shared.go` | L141（空实现） |
| SharedMemory 持久化字段 | `internal/memory/shared.go` | L48-52（persistPath 等死代码） |
| MCP Client | `internal/mcp/client.go` | L100-200（CallTool），L200-300（JSON-RPC 处理） |
| KnowledgeRecord | `internal/mcp/knowledge.go` | L1-30 |
| 去重阈值 0.85 | `internal/tools/knowledge.go` | L120-145（consolidate），L135（硬编码） |
| prune merge 阈值 0.80 | `internal/tools/knowledge.go` | L370-400 |
| mergeKnowledgeLLM | `internal/tools/knowledge.go` | L470-530 |
| executeDelete | `internal/tools/knowledge.go` | L320-350 |
| distillContent | `internal/tools/knowledge.go` | L129-133 |
| 工具注册 | `internal/agents/coding.go` | L533-588 |
| KnowledgeInjector | `internal/knowledge/injector.go` | InjectionMinScore 默认 0.3 |
| autoConsolidateSubtask | `internal/agents/knowledge_hook.go` | L1-60 |
| ResultCompressor | `internal/agents/result_compressor.go` | Store/Retrieve |
| 阈值估算 | `internal/agents/repo_memory.go` | L192-210（EnforceTokenBudget） |
| pruneTriggerInterval | `internal/agents/consolidation_worker.go` | L431（=10） |
| app.go 初始化 | `internal/app/app.go` | L111-169（MCP 启动），L329-344（worker 装配），L535-537（worker.Stop） |
| CodeSeekConfig | `internal/config/config.go` | L661-668 |
| EmbeddingService | `codeseek/rust-core/src/services/embedding_service.rs` | L50-100（vectorize_directory），L120-180（search） |
| HybridSearchService | `codeseek/rust-core/src/services/hybrid_search.rs` | L30-80（search） |
| RerankerService | `codeseek/rust-core/src/services/reranker_service.rs` | L40-90（rerank） |

---

（本文档由 CodeActor-Agent 系统分析生成，内容基于代码实证分析，代码示例为优化设计草案，落地时需结合实际情况调整。）