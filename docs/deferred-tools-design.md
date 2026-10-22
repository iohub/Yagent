# 工具按需加载（Deferred Tools）机制设计

Yagent 的工具面（tool surface）采用「目录 + 按需取回」的两级曝光模型：绝大多数工具的参数声明不会每步随请求重发，而是以「名字 + 一行摘要」的形式常驻系统提示词；模型需要某个工具时，通过元工具 `load_tool` 取回它的完整 JSON Schema，从下一步起该工具才进入真正发给模型的 `tools` 数组。

本文讲解该机制的数据结构、档位模型、预算降级算法、注入与执行链路、生命周期与已知边界。

| 项 | 值 |
|---|---|
| 适用模块 | `internal/tools`（Surface）、`internal/agents`（执行循环）、`internal/toolspec`（工具规格）、`internal/config`（配置） |
| 关键类型 | `Surface`、`SurfaceOptions`、`toolspec.Frequency`、`toolspec.Spec.Brief`、`ToolState` |
| 关键配置 | `[tools.deferred] disable` / `budget_tokens`（默认 2500） |
| 证据来源 | 基于仓库当前代码实测（非推断） |

---

## 目录

1. [机制速览：一个真实调用序列](#1-机制速览一个真实调用序列)
2. [设计动机：工具声明的成本模型](#2-设计动机工具声明的成本模型)
3. [档位模型：Hot / Warm / Cold](#3-档位模型hot--warm--cold)
4. [预算降级：2500 token 的贪心砍刀](#4-预算降级2500-token-的贪心砍刀)
5. [Surface：机制的状态机](#5-surfacesurface-机制的状态机)
6. [两条可见性通道：目录 vs 完整声明](#6-两条可见性通道目录-vs-完整声明)
7. [load_tool 的完整语义](#7-load_tool-的完整语义)
8. [兜底路径：teachColdTool](#8-兜底路径teachcoldtool)
9. [时序全景](#9-时序全景)
10. [生命周期、并发与边界](#10-生命周期并发与边界)
11. [配置项](#11-配置项)
12. [一句话总结](#12-一句话总结)
13. [延伸阅读建议](#13-延伸阅读建议)

---

## 1. 机制速览：一个真实调用序列

在 Agent 的可用工具清单里，一批工具只显示「名字 + 一行摘要」——它们就是 Cold / deferred 工具。模型要使用它们时先调：

```json
load_tool({"names": ["read_note", "list_notes", "read_blueprint"]})
```

`load_tool` 的返回**不是**「加载成功」，而是这几个工具的完整 JSON Schema 全文（`name` / `description` / `parameters`）。模型拿到 schema 之后，才真正去调用 `read_note` 这类工具。

这就是整套机制的用户体验：**上下文里常驻的只有「目录」，Schema 按需付费取回。**

---

## 2. 设计动机：工具声明的成本模型

`internal/tools` 目录下有 30 个源文件，`internal/toolspec` 注册的工具规格更多。如果把所有工具的完整参数声明每步都发给模型：

1. **每轮请求都要重付一次 token**（工具声明位于 system / tools 段，不随单次对话结束而消失）；
2. 绝大多数步骤只会用到 2–3 个工具，其余声明是纯浪费；
3. 工具越多，稳定前缀越大 → 提示缓存命中率、上下文余量都被吃掉。

`internal/tools/surface.go` 顶部的实现注释把这个取舍写得很直白：deferred 目录是「稳定前缀的一部分，比每步重发的完整参数声明便宜一个数量级」（并引用 `docs/tool-registry-design.md §7.6`）。

于是设计定为：**完整声明 = 稀缺资源，目录 = 索引。**

---

## 3. 档位模型：Hot / Warm / Cold

`internal/toolspec/effect.go:104-116`：

```go
const (
    Hot  Frequency = iota // 每步全量声明，永不降级
    Warm                  // 默认全量；超预算可降级为 Cold
    Cold                  // 只出现在目录里（name + Brief），需 load_tool 取 schema
)
```

`internal/toolspec/spec.go:28-29`：**`Brief` 是 Cold 工具的强制字段**。因为 Cold 工具在模型眼里「只剩下这一行字」，没有 Brief 就等于存在一个看不见的工具。这也是为什么目录里每一行都是名词短语式摘要，而不是完整描述。

工具的档位由三层叠加决定：

| 层级 | 位置 | 作用 |
|---|---|---|
| ① 规格声明 | `Spec.Frequency` | 工具作者声明自身档位 |
| ② 画像覆盖 | `Profile.Promote` / `Profile.Demote`（`profile.resolveTier`） | 某个 Agent 强制把工具提为 Hot 或压为 Cold |
| ③ 运行时预算 | `Surface.applyBudget` | 超预算时把 **Warm** 动态降级为 Cold |

注意 ③ **只动 Warm**：Hot 是显式选择，Cold 已经在目录里，这两档都不参与降级。

---

## 4. 预算降级：2500 token 的贪心砍刀

配置定义（`internal/config/deferred_tools.go:20-27`）：

```toml
[tools.deferred]
disable = false        # true 时完全禁用 deferred，所有工具每步全量声明
budget_tokens = 2500   # 超出时 Warm 按声明体积降级为 Cold
```

```go
type DeferredToolsConfig struct {
    Disable      bool `toml:"disable"`
    BudgetTokens int  `toml:"budget_tokens"` // 默认 2500
}
```

降级算法（`internal/tools/surface.go:134-157`）：

```go
func (s *Surface) applyBudget(ordered []string) {
    if !s.opts.Deferred || s.opts.BudgetTokens <= 0 {
        return
    }
    for {
        if s.opts.BudgetTokens-s.estimatedTokens(ordered) >= 0 {
            return
        }
        candidate := s.warmestLargest(ordered)
        if candidate == "" {
            return // 预算再紧也不能动 Hot：流程控制与元工具降级会让模型失去收尾能力
        }
        s.tiers[candidate] = toolspec.Cold
    }
}
```

三个值得注意的细节：

- **`warmestLargest`：每轮砍「当前仍为 Warm 且声明体积最大」的那一个**，循环直到装得下为止 —— 典型贪心，砍掉一个最大工具的收益最高。
- **`estimatedTokens`（`internal/tools/surface.go:171-181`）用「字节数 / 4」近似 token**。注释给出的理由：这个数值每步都要计算，只为决定「谁先降级」，不值得引入 tokenizer 的 CPU 开销；其误差不影响降级顺序的正确性。对未取回 schema 的 Cold 工具只计 `len(name) + len(toolspec.Brief(name)) + 8`（目录里一行），因此降级是真心省预算，而非账面数字游戏。
- **`candidate == ""` 是保底分支**：Hot 降级会让模型失去 `agent_exit` / `load_tool` 这类收尾与元能力，所以宁可超预算也不动 Hot。此外 `Spec.Validate()` 会拦住「把流程控制类工具声明成可降级」这种写法。

降级结果**确定且可复现**：按声明体积比较，无随机性、无时间依赖。

---

## 5. Surface：机制的状态机

`internal/tools/surface.go:20-40`：

```go
type Surface struct {
    mu           sync.Mutex
    profile      toolspec.Profile
    deps         *toolspec.Deps
    opts         SurfaceOptions          // { Deferred bool; BudgetTokens int }
    adapters     map[string]*Adapter     // 名字 → 执行器 + schema + readOnly
    tiers        map[string]toolspec.Frequency
    advertised   []llm.ToolDef           // 缓存的「完整声明」列表
    catalog      string                  // 缓存的目录正文
    materialized map[string]bool         // ⭐ 已取回 schema 的 Cold 工具
    skipped      []toolspec.SkippedTool  // 依赖缺失被跳过的工具 + 原因
}
```

构建流程 `rebuild()`：

```text
toolspec.MatchAll(profile, deps)
  → 逐个 AvailableFor(deps) 判定（不可用 → 记入 skipped，不静默丢弃）
  → Build(deps) 得到执行函数 + Adapter(WithSchema / WithReadOnly)
  → tiers[name] = profile.Tier(spec)
  → SetGuardOnAdapters(...)   ← 统一挂工作区边界 guard（集中一处，避免漏挂）
  → applyBudget(ordered)
  → invalidate()              ← 清空 advertised / catalog 缓存
```

`NewSurface` 里最关键的三行（`internal/tools/surface.go:53-82`）：

```go
if opts.Deferred {
    deps.LoadTool = s.executeLoadTool // load_tool 的执行器就是 Surface 自己
    deps.DeferSchemas = true
} else {
    deps.LoadTool = nil               // 关闭时整体退回历史行为
    deps.DeferSchemas = false
}
```

**`load_tool` 不是外部注册的普通工具，而是 `Surface` 把自己挂到 `Deps` 上的自举元工具**，因此它能直接访问 `materialized` 与 `adapters`；它必须是 Hot，并且必须在 `MatchAll` 之前挂上，否则可用性判定会把它自己排除掉。

---

## 6. 两条可见性通道：目录 vs 完整声明

整套机制的关键在于：同一批工具在**两个不同的地方**曝光。

### 通道 A：系统提示词里的目录（稳定前缀）

`internal/agents/executor.go:262-269`：

```go
systemPrompt := cfg.SystemPrompt
if cfg.RepoContext != "" {
    systemPrompt += "\n\n" + cfg.RepoContext
}
// deferred 目录跟着系统提示走：它是稳定前缀的一部分，比每步重发的
// 完整参数声明便宜一个数量级（docs/tool-registry-design.md §7.6）。
if cfg.Surface != nil {
    if catalog := cfg.Surface.Catalog(); catalog != "" {
        systemPrompt += "\n\n" + catalog
    }
}
```

`Catalog()` → `catalogLines()`（`internal/tools/surface.go:236-246`）产出的原文：

```text
## Deferred tools
You currently see only their name and one-line summary. Call `load_tool` with the names you need to fetch their full parameter schema, then call them. Batch several names into one call.
- deepthinking: 常规手段失败后的多维深度分析
- print_dir_tree: Print directory tree. Auto-ignores common build artifacts...
- semantic_search: Semantic search for relevant code snippets via natural language...
```

`Catalog()` **带缓存**（字段 `s.catalog`）；任何 `materialized` 变更都会走 `invalidate()` 清空缓存，避免每步重算字符串。

### 通道 B：发给 LLM 的 tools 数组（每步重算）

```go
defsForStep := func() []llm.ToolDef { return cfg.Surface.Defs() }
resp, err := cfg.LLM.GenerateContent(llmCtx, messages, defsForStep(), opts)
```

`Defs()` 用 `hidden()` 过滤（`internal/tools/surface.go:262` 起）：

```go
func (s *Surface) hidden(name string) bool {
    if !s.opts.Deferred {
        return false // 关闭开关 → 什么都不隐藏
    }
    if name == "load_tool" {
        _, pending := s.pendingCatalog()
        return !pending // 没有待加载冷工具 → load_tool 自己也消失
    }
    return s.tiers[name] == toolspec.Cold && !s.materialized[name]
}
```

两个精妙之处：

1. **未取回 schema 的 Cold 工具在 `tools` 数组里完全不存在** —— 模型物理上无法调用它，只能走 `load_tool`。
2. **`load_tool` 自身也会消失**：所有冷工具都取回后 `pendingCatalog()` 为空，目录变为空串，`load_tool` 也从声明中移除（`TestSurface_DropsLoadToolWhenCatalogEmptied` 专测该行为）。工具面会「自我收窄」。

---

## 7. load_tool 的完整语义

Schema（`internal/toolspec/schemas.json`）：参数只有 `names` 字符串数组，**`maxItems = 8`**，对应批量加载。

执行链与行为：

```text
LLM tool_call(load_tool, {names:[...]})
  → toolbatch 分发 → deps.LoadTool = Surface.executeLoadTool
  → Surface.Materialize(names)                        (surface.go:304)
       ├ 未启用 deferred        → error "deferred schema 未启用"
       ├ len(names) == 0        → error "names 不能为空"
       ├ len(names) > 8         → names = names[:8]   （静默截断，不报错）
       └ 逐名处理：
            adapter 不存在     → unknown[]
            !hidden(name)      → notDeferred[]（已在声明里，不必重复加载）
            否则               → materialized[name] = true; invalidate(); loaded += schemaText()
```

返回文本的真实构成（`internal/tools/surface.go:330-360`）：

```text
{ 每个成功加载工具的 JSON: name / description / parameters 全文 }
Now call the tool with these parameters.
Already available (no need to load): xxx        ← notDeferred 非空时
Unknown: zzz                                    ← unknown 非空时
Deferred tools available to load: a, b, c       ← 顺带把剩余清单再喂一次
   （或 No deferred tools remain in this run.）
```

设计意图很明确：**返回 schema 全文而不是「加载成功」** —— 因为模型下一步要用它填参数；`unknown` 时回吐剩余可加载清单，让模型能自我纠正而不是卡死。

---

## 8. 兜底路径：teachColdTool

模型可能无视目录，直接调用一个冷工具（它可能在本轮历史消息里见过这个名字）。这时不报「工具不存在」把模型绕死，而是**教它一次**。

`internal/agents/executor.go:206-217`：

```go
func isColdUnloaded(cfg *ExecutorConfig, name string) bool {
    if cfg.Surface == nil {
        return false
    }
    _, state := cfg.Surface.Lookup(name)
    return state == tools.StateColdKnown
}

func teachColdTool(cfg *ExecutorConfig, name string) string {
    schema, err := cfg.Surface.Materialize([]string{name})
    if err != nil {
        return fmt.Sprintf("Error: cannot load schema for %s: %v", name, err)
    }
    return fmt.Sprintf("Error: schema for %s was not loaded. Full schema:\n%s\nRetry the call now with these parameters.", name, schema)
}
```

`Lookup` 的三态（`internal/tools/surface.go:Lookup`）：

| 状态 | 含义 | 执行循环的反应 |
|---|---|---|
| `StateReady` | 已带完整声明 | 正常执行 |
| `StateColdKnown` | 注册表里有、模型没取回 schema | **同步 `Materialize` + 回吐 schema + 要求重试** |
| `StateUnknown` | 这个 Agent 根本没有该工具 | 报未找到 |

关键点：`teachColdTool` 中的 `Materialize` 是**同步完成**的 —— 它既登记了 `materialized`，又把 schema 塞进「错误结果」。所以模型下一步重试时，该工具已进入 `Defs()` 的完整声明，调用会成功。代价是模型视角多走一步，但用户无感，也不需要额外请求。

---

## 9. 时序全景

```text
① RunAgentLoop 组装 systemPrompt += Surface.Catalog()          executor.go:262
② 每步 defsForStep() = Surface.Defs()  → 未 materialize 的 Cold 被 hidden
③ LLM 只看到：完整声明(Hot / Warm / 已加载 Cold) + 目录文本 + load_tool
④ LLM 调 load_tool{names:[...]}
⑤ executeLoadTool → Materialize：materialized[name] = true + invalidate()
⑥ 工具结果 = schema 全文 + "Now call the tool with these parameters."
⑦ 下一步 ② 重算 → 这些工具出现在完整声明中，模型正常调用
```

**「每步重算 `Defs()`」是整套机制成立的前提。** `internal/agents/executor.go` 的注释还点出它顺带修掉了一个老限制：「本回合注册的 Custom Agent 要到下一回合才可见」—— `Surface.Add()` 让 Meta 现场注册的 `delegate_<name>` 在同一回合的下一步就可见。

---

## 10. 生命周期、并发与边界

### 状态归属

`materialized` 挂在 `Surface` 实例上，`mu sync.Mutex` 保护全部公共方法（`Defs` / `Catalog` / `Lookup` / `Materialize` / `RestoreMaterialized` / `pendingCatalog`）。并发安全由 `TestSurface_ConcurrentMaterializeAndDefs`（24 个 goroutine 并行调用 Materialize / Defs / Catalog，跑 race detector）兜住。

### append-only，永不回收

`s.materialized[name] = true` 只增不减，`TestSurface_MaterializeIsAppendOnly` 明确这条。理由是**工具列表来回抖动会让模型的前缀缓存失效、行为不稳定**。重复加载是幂等的 —— 走 `!hidden(name)` 分支，只回一句 `Already available (no need to load): xxx`。

### 生命周期

- 一次 Agent 会话内有效；`NewSurface()` 新建实例时 `materialized` 重置为空 map。
- **pause / resume 不丢**：`RunAgentLoop` 收到 `ResumeState` 时执行 `cfg.Surface.RestoreMaterialized(rs.Materialized)`（`internal/agents/executor.go:284`），连同 `Messages` / `StartStep` / `ThinkLink` 一起接续。接不上的后果注释写得很重：「从第 0 步重跑会重复已经做完的副作用」。
- `Surface.Materialized()` 返回排序后的已加载名单，**供 rollout 持久化**。因此准确说法是：**不落业务数据库，但会随暂停 / rollout 快照序列化。**

### 作用域

`ExecutorConfig.Surface` 是指针：同一个 `Surface` 被复用的执行循环（含委派子循环）共享同一份 `materialized`；而 `NewSurface` 在 `internal/app/run.go`、`internal/agents/activity`、`internal/agents/director` 等多处独立调用，**不同 Agent 的工具面各自独立**。所以「是否跨代理共享」取决于是否复用同一实例，而不是硬编码的全局共享。

### 配置只有两个旋钮

`[tools.deferred] disable` 与 `budget_tokens`（配置文件 `~/.yagent/config/config.toml`）。`disable = true` 时 `Deferred=false` 且 `LoadTool=nil`，**整体退回「所有工具每步全量声明」的历史行为** —— 这是留给排障的开关，也解释了 `hidden()` 第一行 `if !s.opts.Deferred { return false }`。没有白名单 / 黑名单机制：想强制某工具常驻，就在 `Spec.Frequency` 或 `Profile.Promote` 里声明 Hot。

---

## 11. 配置项

| 配置项 | 默认值 | 说明 |
|---|---|---|
| `[tools.deferred] disable` | `false` | `true` 时**完全禁用** deferred，所有工具每步全量声明（历史行为） |
| `[tools.deferred] budget_tokens` | `2500` | 超出该预算时按声明体积把 Warm 降级为 Cold；`<= 0` 视为不启用预算降级 |

无白名单 / 黑名单：所有降级决策只基于**工具声明体积**与**档位**。Hot 工具在代码层面永不降级（`applyBudget` 中 `if s.tiers[name] != toolspec.Warm { continue }`）；`load_tool` 被硬编码注册为 `Frequency: Hot`。

---

## 12. 一句话总结

> **`Surface` 把「工具面」建模成一个按 token 预算自适应的三档目录：Hot 永远全量、Warm 超预算按体积贪心降级、Cold 只剩 `name + Brief` 出现在稳定前缀里；模型通过 `load_tool`（最多 8 个一批）把 schema 从「索引」里取回，`materialized` 只增不减地记账，`Defs()` 每步重算保证加载下一步即生效；模型若跳过 `load_tool` 直接调用冷工具，`teachColdTool` 会同步补票并把 schema 塞进错误结果让它重试。**

---

## 13. 延伸阅读建议

1. `internal/toolspec/effect.go`：`Frequency` 的 `Validate()` 约束 —— 哪些工具不允许被声明成可降级。
2. `docs/tool-registry-design.md` §7.6：`internal/tools/surface.go` 注释直接引用的设计稿，讲解稳定前缀与提示缓存的取舍。
3. `internal/tools/surface_test.go`：`TestSurface_DefersColdTools`（主路径）、`TestSurface_BudgetDemotesWarmOnly`（只降 Warm）、`TestSurface_DropsLoadToolWhenCatalogEmptied`（load_tool 自我移除）、`TestSurface_RestoreMaterialized`（resume 恢复）。
