# CodeActor 上下文 Compact 机制深度分析报告

> 分析日期：2025年  
> 分析范围：`internal/compact/` 全部 7 个源文件 + `internal/agents/conductor.go` + `internal/app/app.go`

---

## 一、当前 Compact 机制全景

### 1.1 整体架构

```
conductor.go (Run() 循环)
  │  每步迭代前检查 token 是否超限
  │
  └─→ compact.Engine.Compress()
        │
        ├─→ CountTokens()              ──── tiktoken 计数
        ├─→ CalculatePriorities()       ──── 优先级计算
        └─→ LLMSummarizer.Summarize()
              │
              ├─→ 分区（保留区 vs 摘要区）
              ├─→ 分段（按 SummarizationMaxInputTokens 分批次，默认 8000）
              ├─→ 并发 goroutine 调用 LLM 摘要
              └─→ 合并 → [原始System] + [摘要System] + [保留区消息]
```

### 1.2 核心文件职责

| 文件 | 行数 | 核心职责 |
|------|------|----------|
| `engine.go` | 128 | 压缩引擎主入口，协调各组件 |
| `priority.go` | 173 | 消息优先级计算（角色+区域+时间衰减） |
| `summarizer.go` | 307 | LLM 摘要核心：分区、分段、并发摘要、结果合并 |
| `tokenizer.go` | 103 | 基于 tiktoken 的全局单例 Token 计数器 |
| `compact_config.go` | 66 | 配置结构与工厂函数 |
| `compact_types.go` | 36 | 接口定义（SummarizationClient, ContextCompressor）+ 结果结构 |
| `compact_test.go` | 514 | 完整测试套件（13 个测试函数） |

### 1.3 调用链路

```
app.go (初始化)
  │  ConfigFrom() → compact.Config
  │  可选创建独立 summaryEngine
  │
  └─→ NewConductorAgent(compactCfg, summaryEngine)
        │
        └─→ Run() 循环
              │  Run() 内按需初始化 compactEngine
              │  每步迭代前：
              │    CountTokens(messages) > MaxContextTokens ?
              │      └─→ Compress(ctx, messages) → new messages
              │
              └─→ createSummaryClient()
                    └─→ SummaryAdapter{LLM, Model, Temperature, MaxTokens}
```

### 1.4 优先级算法（`priority.go`）

**权重体系**：

| 维度 | 机制 | 默认值 |
|------|------|--------|
| **角色基础分** | System=10, User=8, Assistant=4, Tool=2 | 语法角色驱动 |
| **近期加成** | 最近 N 轮消息 ×2.0 | N=3 |
| **早期保留** | 前 1/3 消息 ×1.2 | 前1/3区 |
| **中间压制** | 中间区 Tool 消息 ×0.5 | 优先压缩中间区域 |
| **时间衰减** | ×(1+0.08)^depth | depth=距末尾轮数 |
| **长度惩罚** | >5000字符 ×0.7 | 防单条过长 |

**区域分类逻辑**（伪代码）：
```go
depth := totalLen - i          // i=0是第一条，depth越大越旧
isRecent := depth <= keepRecentRounds   // 最近N轮
isEarly := i < totalLen/3              // 前1/3
isIntermediate := !isRecent && !isEarly // 中间区域——优先压缩
```

### 1.5 摘要流程（`summarizer.go`）

**分区规则**：
- **始终保留**：System 消息、User 消息、近期（最近 N 轮）消息
- **早期锚点**：前 1/3 区域中的第一条和最后一条消息
- **其余 → 摘要区**：调用 LLM 并发生成摘要

**分段策略**（`segmentMessages()`）：
- 粗略估算：每 4 字符 ≈ 1 token
- 每段不超过 `SummarizationMaxInputTokens`（默认 8000）
- 单条消息超过限制则自成一段

**并发模型**：
```go
for i, batch := range batches {
    wg.Add(1)
    go func(idx int, batchMsgs []llm.Message) {
        defer wg.Done()
        summary, err := s.client.GenerateSummary(sumCtx, batchMsgs)
        // ...收集结果
    }(i, batch)
}
wg.Wait()
```

**输出格式**：
```
[原始 System 消息]（第一条）
[CONTEXT SUMMARY]
  + 原始 Prompt 模板全文
  + ---对话摘要---
  + 摘要段 1
  + 摘要段 2
  + ...
[保留区消息]（User + 近期 + 早期锚点）
```

---

## 二、核心问题分析

### 🔴 问题 1：全量重构范式（根本性架构问题）

每次触发压缩，**重新分析和摘要整个对话历史**，而非只处理新增部分。

**代码证据**（`engine.go`）：
```go
func (e *Engine) Compress(ctx context.Context, messages []llm.Message) (*CompressResult, error) {
    // 接收全量 messages，没有 state 追踪
}
```

**引发级联问题**：

```
全量重构范式
    ├── 信息退化级联：已压缩的摘要被重新摘要 → 关键信息逐次丢失
    ├── 计算浪费级联：每次全量优先级计算 + 全量 LLM 摘要调用
    ├── Cache 破坏级联：重构消息顺序 → Prompt Cache 前缀失效 → API 成本暴涨 3-5x
    └── 状态丢失级联：不区分"已压缩"与"原始"消息 → 无法感知压缩历史
```

**后果**：长对话经过 2-3 次压缩后，早期关键约束和决策信息会逐渐退化消失。

### 🟠 问题 2：优先级基于语法角色而非语义重要性

**代码证据**（`priority.go`）：
```go
func (pc *PriorityCalculator) calculateBaseScore(msg llm.Message, ...) float64 {
    var score float64
    switch msg.Role {   // 只依赖 msg.Role
    case llm.RoleSystem:    score = pc.weights.RoleSystem    // 10.0
    case llm.RoleUser:      score = pc.weights.RoleUser      // 8.0
    case llm.RoleAssistant: score = pc.weights.RoleAssistant // 4.0
    case llm.RoleTool:      score = pc.weights.RoleTool      // 2.0
    }
}
```

**后果**：

| 场景 | 当前行为 | 应有行为 |
|------|---------|---------|
| 用户说："必须使用 MIT 协议" vs "好的谢谢" | 同等优先级（都是 RoleUser=8） | 前者应锁定保护 |
| Tool 返回了编译错误 vs 正常输出 | Tool 最低分（2.0），优先被压缩 | 错误应加权保留 |
| Assistant 给出了关键代码架构决策 | 中等分（4.0） | 决策应加权保留 |
| 中间区域的 Assistant 消息 | 时间衰减后分数很低 | 含代码片段应保护 |

### 🟡 问题 3：摘要质量相关

#### 3.1 摘要中嵌入完整 Prompt 模板

**代码证据**（`summarizer.go` 第 226-227 行）：
```go
var fullSummary strings.Builder
fullSummary.WriteString(summaryPrompt + "\n\n---对话摘要---\n\n")
```

每次摘要结果中都嵌入了完整的 `defaultSummarizationPrompt`（约 50 行、数百 token 的系统指令）。这些指令对后续对话完全无用，纯属浪费。

#### 3.2 分段估算粗糙

```go
getApproxTokens := func(content string) int {
    return len([]rune(content)) / 4  // 4字符≈1token
}
```

不同内容类型的实际 token 密度差异巨大：

| 内容类型 | 实际字符/token | 估算偏差 |
|---------|---------------|---------|
| 中文文本 | 约 1.5-2 | **-50%~-60%** |
| 代码（密集符号） | 约 3-3.5 | -12%~-25% |
| 英文自然语言 | 约 4 | 基本准确 |
| 混合（代码+中文+英文） | 约 2.5-3.5 | 偏差不可预测 |

**后果**：包含大量中文或代码的消息批次，实际 token 可能远超 8000 限制，导致 LLM 调用失败或摘要被截断。

#### 3.3 无摘要输出后处理

LLM 生成的摘要直接写入上下文，包含大量脏数据：
- Prompt 指令镜像（LLM 偶然重复指令）
- 开头废话（"Sure, here is the summary..."）
- 不稳定的格式输出

### 🟡 问题 4：性能与扩展性问题

| 问题 | 位置 | 严重度 | 影响 |
|------|------|--------|------|
| **Tokenizer 缓存非 LRU** | `tokenizer.go:L80-82` | 🟠 | 缓存满时 `make(map[string]int)` 全清，大量重新计算 |
| **硬编码模型编码** | `tokenizer.go:L68` | 🟡 | 固定用 `"gpt-4"` 编码，其他模型 tokenizer 不同 |
| **goroutine 无限制** | `summarizer.go:L120-150` | 🟡 | 所有分段同时 goroutine，API 限流风险 |
| **无重试机制** | `summarizer.go` | 🟡 | LLM 调用失败直接返回错误 |
| **compressor.go 废弃** | 仅5行注释 | 🟢 | 原规则压缩器已被移除，只剩 LLM 摘要一种策略 |
| **中文本地化缺失** | `summarizer.go` | 🟢 | 摘要 Prompt 只有英文版 |

---

## 三、改进方案设计

### 总体策略：分阶段混合方案

```
采用增量压缩为核心骨架
    + 融合语义优先级 + 结构化摘要的关键改进
    + 预留策略接口以便未来扩展
```

### Phase 1（核心架构变更）：增量压缩 + Cache-Aware 适配

**目标**：解决根本性的全量重构问题，降低 60-80% 的摘要 LLM 调用。

#### 1.1 新增 CompressionState 类型

**位置**：`compact/compact_types.go`（新增）

```go
type CompressionState struct {
    // 已压缩的消息范围 [0, LastCompressedIndex)
    LastCompressedIndex int

    // 累积摘要文本栈（每次增量压缩追加一条）
    SummaryStack []SummaryEntry

    // 已提取的约束块（用户显式约束，永不压缩）
    ConstraintsBlock string

    // 当前估算 token 总数（避免全量重算）
    EstimatedTokens int

    // 上次压缩时的消息总数，用于快速判断增量
    LastTotalMessages int
}

type SummaryEntry struct {
    Content           string    // 摘要文本
    TokenCount        int       // 摘要占用 token 数
    RangeStart        int       // 原始消息范围起点
    RangeEnd          int       // 原始消息范围终点
    Timestamp         time.Time // 压缩时间
    CompressionLevel  int       // 1=首次摘要, 2=整合摘要
}
```

#### 1.2 增量压缩逻辑

**位置**：`compact/engine.go`（修改 Compress 方法）

```go
type Engine struct {
    config       *Config
    tokenizer    Tokenizer
    priorityCalc *PriorityCalculator
    summarizer   *LLMSummarizer
    state        *CompressionState  // 新增：跨调用保持状态
    stateMutex   sync.Mutex         // 新增：并发保护
}

func (e *Engine) Compress(messages []llm.Message) ([]llm.Message, error) {
    e.stateMutex.Lock()
    defer e.stateMutex.Unlock()

    totalTokens := e.CountTokens(messages)
    if totalTokens <= e.config.MaxContextTokens {
        // 不超限，更新状态但不压缩
        e.state.EstimatedTokens = totalTokens
        e.state.LastTotalMessages = len(messages)
        return messages, nil
    }

    // 判断首次还是增量
    if e.state.LastCompressedIndex == 0 && len(e.state.SummaryStack) == 0 {
        return e.fullCompress(messages)  // 首次：全量（与旧版一致）
    }
    return e.incrementalCompress(messages) // 增量：只处理新增
}

func (e *Engine) incrementalCompress(messages []llm.Message) ([]llm.Message, error) {
    // 1. 确定需要压缩的新增量
    recentStart := len(messages) - e.config.KeepRecentRounds*2
    if recentStart < e.state.LastCompressedIndex {
        recentStart = e.state.LastCompressedIndex
    }

    // 2. 压缩范围：上次压缩点 → 近期保留区起点
    toCompress := messages[e.state.LastCompressedIndex:recentStart]
    if len(toCompress) == 0 {
        return e.consolidateSummaries(messages) // 尝试整合摘要栈
    }

    // 3. 对增量做摘要
    summary, tokenCount, err := e.summarizer.SummarizeSegment(ctx, toCompress)
    if err != nil {
        return nil, err
    }

    // 4. 追加到摘要栈
    e.state.SummaryStack = append(e.state.SummaryStack, SummaryEntry{
        Content:      summary,
        TokenCount:   tokenCount,
        RangeStart:   e.state.LastCompressedIndex,
        RangeEnd:     recentStart,
        CompressionLevel: 1,
    })
    e.state.LastCompressedIndex = recentStart

    // 5. 构建 Cache-Aware 输出
    return e.buildCacheAwareOutput(messages), nil
}
```

#### 1.3 Cache-Aware 消息布局

**设计原则**：固定前缀最大化 Prompt Cache 命中。

```
┌──────────────────────────────────────────────┐
│ [System Message]           ← 绝对稳定前缀     │
│ [Constraints Block]        ← 稳定（提取后不变） │
│ [Summary Entry 1]          ← 稳定（已生成不变） │
│ [Summary Entry 2]          ← 稳定              │
│ [Summary Entry ...]        ← 缓慢增长          │
│ [Summary Entry N]          ← 新追加            │
│ [Recent Kept Messages]     ← 变动区域          │
│ [Newest Messages]          ← 最新消息（每步变） │
└──────────────────────────────────────────────┘
```

**效果**：稳定前缀部分（数万 token）始终保持不变，Prompt Cache 持续命中，预计节省 30-50% API 成本。

#### 1.4 摘要栈整合机制

**触发条件**：摘要栈 token 总数 > `MaxContextTokens × 30%`

```
整合前：[Entry 1] + [Entry 2] + [Entry 3] + ...（Level-1）
              ↓ LLM 整合摘要
整合后：[Consolidated Entry]（Level-2）
```

**优势**：
- 摘要栈大小受到控制，不会无限增长
- 已整合的内容不再重复摘要（根治信息退化）
- 整合频率远低于每次压缩（约 4-6 次增量才整合一次）

---

### Phase 2（质量提升）：语义优先级 + 结构化摘要

**目标**：让真正重要的内容永不丢失，摘要质量可衡量、可追踪。

#### 2.1 约束提取器

**新文件**：`compact/constraint_extractor.go`

采用正则规则引擎，从用户消息中提取显式约束：

| 语言 | 模式 | 类别 |
|------|------|------|
| 英文 | `must`, `should`, `need to`, `require`, `ensure` | requirement |
| 英文 | `don't`, `never`, `avoid`, `must not` | prohibition |
| 英文 | `prefer`, `better`, `would like` | preference |
| 英文 | `decided`, `let's`, `we'll` | decision |
| 中文 | `必须`, `需要`, `确保`, `一定要`, `务必` | requirement |
| 中文 | `不要`, `禁止`, `避免`, `不能`, `绝不` | prohibition |
| 中文 | `偏好`, `倾向于`, `最好` | preference |
| 中文 | `决定`, `选择`, `我们将` | decision |

**设计要点**：
- 提取的约束进入独立的 `[Constraints Block]` 消息
- 约束块**永不参与压缩**，在每次消息构建时放在固定位置
- 提取后从原消息中移除约束语句，避免重复

#### 2.2 语义优先级增强

在保留现有角色权重的基础上叠加语义因子：

```go
type SemanticPriorityCalculator struct {
    baseCalc      *PriorityCalculator  // 保留原算法作为基础分
    constraintExt *ConstraintExtractor // 约束提取器
}

func (spc *SemanticPriorityCalculator) Calculate(messages []Message) []float64 {
    // 1. 基础优先级（角色+区域+时间衰减）
    base := spc.baseCalc.Calculate(messages)

    for i, m := range messages {
        score := base[i]

        // 约束锁定：含约束的 User 消息 → 绝对保留
        if m.Role == "user" && containsConstraint(m.Content) {
            score = math.MaxFloat64
        }

        // 错误加权：Tool 结果含 error/failed/panic → ×3.0
        if m.Role == "tool" && isErrorResult(m.Content) {
            score *= 3.0
            // 若错误已被后续消息解决 → 降权至 ×0.3
            if isResolvedInLaterMessages(m, messages, i) {
                score *= 0.3
            }
        }

        // 决策加权：Assistant 消息含明确决策 → ×2.5
        if m.Role == "assistant" && containsDecision(m.Content) {
            score *= 2.5
        }

        // 代码块保护：含代码片段 → ×1.5
        if containsCodeBlock(m.Content) {
            score *= 1.5
        }

        result[i] = score
    }
    return result
}
```

#### 2.3 结构化摘要格式

**英文模板**：
```markdown
## Project Constraints
- [用户显式约束列表]

## Key Decisions
- [重要架构/设计决策]

## Actions Taken
- [工具调用、文件操作及结果，按主题分组]

## Current State
- [项目/任务的当前状态]

## Unresolved Issues
- [错误、待解答问题、未完成任务]
```

**中文模板**：
```markdown
## 项目约束
- [用户明确提出的约束列表]

## 关键决策
- [对话中做出的重要决策]

## 已执行操作
- [工具调用、文件变更及结果]

## 当前状态
- [项目/任务的当前进度]

## 未解决问题
- [错误、开放问题、未完成任务]
```

**规则**：
1. 使用要点列表，不用段落
2. 保留精确的技术术语、文件路径、函数名、错误消息
3. 不在输出中包含指令文本
4. 直接以 `## xxx` 开始输出

#### 2.4 摘要输出清洗

```go
func (s *LLMSummarizer) cleanSummaryOutput(raw string) string {
    // 1. 移除 Prompt 镜像
    // 2. 移除开头客套话（"Sure," "Here's" "好的，"）
    // 3. 验证结构化格式，不满足时强制包装
    return cleaned
}
```

---

### Phase 3（性能优化 + 扩展预留）

**目标**：提升系统可靠性和性能，为未来扩展做好准备。

#### 3.1 LRU Tokenizer 缓存

**位置**：`compact/tokenizer.go`

使用 Go 的 `container/list` 实现真正 LRU，替代当前的全清策略：

```go
type lruCache struct {
    capacity int
    items    map[string]*list.Element
    order    *list.List
}

func (c *lruCache) Get(key string) (int, bool) {
    if elem, ok := c.items[key]; ok {
        c.order.MoveToFront(elem)  // 移动到最前（最近使用）
        return elem.Value.(*cacheEntry).value, true
    }
    return 0, false
}

func (c *lruCache) Put(key string, value int) {
    if elem, ok := c.items[key]; ok {
        c.order.MoveToFront(elem)
        return
    }
    if len(c.items) >= c.capacity {
        oldest := c.order.Back()  // 淘汰最久未使用
        c.order.Remove(oldest)
        delete(c.items, oldest.Value.(*cacheEntry).key)
    }
    // 插入到最前
}
```

#### 3.2 模型感知 Tokenizer

```go
var ModelTokenizerConfigs = map[string]TokenizerConfig{
    "gpt-4":        {Encoding: "cl100k_base", MaxTokens: 128000, Ratio: 4.0},
    "gpt-4o":       {Encoding: "o200k_base",  MaxTokens: 128000, Ratio: 4.0},
    "gpt-3.5-turbo":{Encoding: "cl100k_base", MaxTokens: 16384,  Ratio: 4.0},
    "claude-3-opus":{Encoding: "cl100k_base", MaxTokens: 200000, Ratio: 3.5},
    "claude-3-haiku":{Encoding: "cl100k_base", MaxTokens: 200000, Ratio: 3.5},
}
```

#### 3.3 精确分段估算

根据内容类型混合估算 token：

```go
func EstimateTokensByContent(text string) int {
    codeRatio := 3.5     // 代码内容约 3.5 字符/token
    chineseRatio := 1.5  // 中文约 1.5 字符/token
    englishRatio := 4.0  // 英文约 4.0 字符/token

    codeChars := countCodeCharacters(text)
    chineseChars := countChineseCharacters(text)
    otherChars := len(text) - codeChars - chineseChars

    return int(
        float64(codeChars)/codeRatio +
        float64(chineseChars)/chineseRatio +
        float64(otherChars)/englishRatio
    )
}
```

#### 3.4 Worker Pool 并发控制

```go
type SummarizerPool struct {
    workers    int           // 最大并发数
    semaphore  chan struct{} // 信号量
    maxRetries int           // 失败重试次数
    timeout    time.Duration
}

func (sp *SummarizerPool) SummarizeSegments(segments []Segment) ([]Result, error) {
    for i, seg := range segments {
        sp.semaphore <- struct{}{}  // 获取 worker slot
        go func(idx int) {
            defer func() { <-sp.semaphore }()  // 释放
            // 带重试的摘要调用
        }(i)
    }
}
```

#### 3.5 策略接口预留

```go
type CompressionStrategy interface {
    Name() string
    Compress(messages []Message, state *CompressionState) ([]Message, *CompressionState, error)
    CanHandle(messages []Message, state *CompressionState) bool
}

// 策略管道：多个策略串联执行
type StrategyPipeline struct {
    strategies []CompressionStrategy
}
```

为未来支持多种压缩策略（规则过滤、语义搜索、关键词提取等）预留扩展点。

---

## 四、改进收益预估

### 4.1 量化预期

| 改进项 | 优化效果 | 投入 | 风险 |
|--------|---------|------|------|
| **增量压缩** | 摘要 LLM 调用减少 60-80%，信息退化根治 | 🟠 中（核心变更） | 🟡 中 |
| **Cache-Aware 布局** | Prompt Cache 命中率提升，API 成本降 30-50% | 🟡 低（输出格式变更） | 🟢 低 |
| **约束提取** | 关键约束保留率从 ~60% → ~95% | 🟡 低（新组件） | 🟢 低 |
| **语义优先级** | 关键消息保留率提升 30-40% | 🟡 低（算法改进） | 🟢 低 |
| **结构化摘要** | 摘要信息密度提升 2-3 倍 | 🟡 低（Prompt 设计） | 🟢 低 |
| **LRU 缓存** | Tokenizer 性能稳定，无突降 | 🟢 很低（替换实现） | 🟢 低 |
| **模型感知** | Token 计数偏差从 20%+ → <5% | 🟢 很低（配置映射） | 🟢 低 |

### 4.2 综合收益

```
API 调用成本：     降低 30-50%（Cache 命中 + 增量摘要）
摘要 LLM 调用：    减少 60-80%（增量模式）
关键信息保留率：   从 ~60% 提升到 ~95%（约束提取 + 语义加权）
摘要信息密度：     提升 2-3 倍（结构化格式 + 后处理清洗）
Token 计数精度：   从偏差 20%+ 降到 <5%（内容感知估算）
```

---

## 五、实施建议路线图

### 快速取胜（1-2天）

| 改进项 | 改动量 | 文件 | 效果 |
|--------|--------|------|------|
| **摘要输出清洗** | ~20 行 | `summarizer.go` | 移除 Prompt 废料，节省数百 token |
| **分段估算优化** | ~30 行 | `summarizer.go` | 分段精度提升，减少截断风险 |

### 核心改善（1-2周）

| 改进项 | 改动量 | 主要文件 | 效果 |
|--------|--------|---------|------|
| **CompressionState** | ~60 行新增 | `compact_types.go` | 状态追踪基础 |
| **增量 Compress** | ~80 行修改 | `engine.go` | 摘要调用减少 60-80% |
| **Cache-Aware 输出** | ~40 行新增 | `engine.go` | Cache 命中率提升 |
| **摘要栈整合** | ~50 行新增 | `engine.go` | 控制摘要栈大小 |

### 质量提升（2-3周）

| 改进项 | 改动量 | 主要文件 | 效果 |
|--------|--------|---------|------|
| **约束提取器** | ~80 行新文件 | `constraint_extractor.go` | 关键约束 95% 保留 |
| **语义优先级** | ~60 行修改 | `priority.go` | 错误/决策加权保留 |
| **结构化摘要 Prompt** | ~40 行修改 | `summarizer.go` | 摘要质量可衡量 |

### 架构优化（持续）

| 改进项 | 改动量 | 主要文件 | 效果 |
|--------|--------|---------|------|
| **LRU 缓存** | ~50 行修改 | `tokenizer.go` | 性能稳定 |
| **模型感知 Tokenizer** | ~40 行修改 | `tokenizer.go` | 跨模型兼容 |
| **Worker Pool** | ~60 行新增 | `summarizer.go` | 并发控制+重试 |
| **Strategy 接口** | ~30 行新增 | `compact_types.go` | 扩展性预留 |

---

## 六、总结

### 当前机制的核心缺陷

CodeActor 当前的上下文 compact 机制**在"压缩"动作的实现上是正确的（使用 LLM 摘要），但在三个关键维度上存在不足**：

1. **范式层面**：全量重构而非增量演进，导致信息退化、计算浪费、Cache 失效
2. **决策层面**：优先级仅依赖语法角色而非语义重要性，关键约束和错误信息可能被压缩
3. **质量层面**：摘要含 Prompt 废料、分段估算粗糙、无输出后处理

### 三条最核心的改进建议

| 优先级 | 改进 | 一句话概括 |
|--------|------|-----------|
| 🔴 P0 | **改全量为增量** | 用 CompressionState 追踪压缩进度，每次只处理新增消息 |
| 🟠 P1 | **改语法为语义** | 引入约束提取和语义加权，让真正重要的内容永不丢失 |
| 🟡 P2 | **改无序为有序** | 用 Cache-Aware 固定前缀布局，让 Prompt Cache 持续生效 |

### 预期综合效果

这三条改进相互独立可渐进实施，但协同作用时能带来：

> **LLM 摘要调用减少 60-80% + 关键信息保留率 95%+ + API 成本降低 30-50%**

---

## 附录

### A. 关键文件路径

| 文件 | 路径 |
|------|------|
| 压缩引擎主入口 | `internal/compact/engine.go` |
| 优先级计算 | `internal/compact/priority.go` |
| LLM 摘要器 | `internal/compact/summarizer.go` |
| Token 计数器 | `internal/compact/tokenizer.go` |
| 配置结构 | `internal/compact/compact_config.go` |
| 类型和接口 | `internal/compact/compact_types.go` |
| 测试套件 | `internal/compact/compact_test.go` |
| Conductor 调用 | `internal/agents/conductor.go` |
| 初始化入口 | `internal/app/app.go` |

### B. 当前默认配置

```toml
[compact]
max_context_tokens = 198000
enable_auto_compact = true
summarization_model = "gpt-3.5-turbo"
summarization_timeout = 15
summarization_max_input_tokens = 8000
keep_recent_rounds = 3
```

### C. 相关设计文档

- `docs/Prompt_cache.md` — Prompt Cache 优化指南（已存在的设计文档）
