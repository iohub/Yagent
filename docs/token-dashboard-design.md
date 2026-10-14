# Token Dashboard 重构设计

> **本文档是给 Coding-Agent 的实施规格书，不是讨论稿。** 所有列宽、字符串、file:line 均已实测核对（核对时间 2026-10-14，基线 commit `0854f56`）。
> 视觉稿的每一行都用与生产一致的宽度口径（`lipgloss.Width`：CJK=2、East-Asian-Width 为 `A`/`N` 的字形=1）逐行断言过，可直接当 golden 用。
>
> 相关既有约定见 `AGENTS.md` §6.8（TUI）、§11 坑 6/7（事件重放、footer 高度自洽）。

---

## 0. TL;DR

**做什么**：把 token 消耗展示从"三份分叉实现"收敛为**一个渲染器 + 一份口径**，视觉升级为已批准的 S1 布局（Σ 总账 → ⊕/⛁ 双计量条 → AGENT 表 `RUN ▸ TOTAL ⊕`）。

**动哪些文件**（全部在 `internal/tui/`）：

| 文件 | 动作 |
|------|------|
| `token_panel.go` | **新建**：口径计算 + 统一渲染器 |
| `tui_dashboard.go` | 重写 token 段（`:118-296`），保留 Activity 段（`:298-338`）与边框收敛逻辑（`:340-385`） |
| `tui_view.go` | **删除** `renderTokenDashboard`(`:658-757`)、`renderCollapsedTokenDashboard`(`:759-807`)、`formatCacheInfo`(`:643-656`)；`formatToken`(`:632`) 保留并移入 `token_panel.go` |
| `tui_context_line.go` | **已删除**：⛁ 行收进 `token_panel.go` 的 `meterLine`/`ctxMeterParts`，与 ⊕ 行共用同一个计量条原语 |
| `tui_model.go` | 状态字段增删（`:442-463`） |
| `tui_update_tasks.go` | `ai_response` 累计逻辑（`:108-239`）改为写新的 per-agent run 表 |
| `tui_render.go` | `tokenDashboardBlock`(`:74-86`) 改调新渲染器；`computeFooterHeight`(`:45`) 不变但行数会变 |
| `tui_update_keys.go` | 修 `alt+t`(`:609-614`) 的 render-then-invalidate 顺序 bug |
| `tui_tabs.go` / `tui_resume.go` / `tui_tasks.go` | 新状态的存取与重置挂点 |
| `tui_token_test.go` / `tui_dashboard_border_test.go` / `tui_chrome_test.go` | 更新 + 新增（见 §10） |

**不能动**：Activity/timeline 段的渲染与语义、`protocol/`、`internal/llm/`、`internal/agents/` 的事件发布、状态栏（`renderAirlineStatusBar`）、i18n 既有键。

**怎么验证**：`go vet ./...` + `go test ./internal/tui/... -count=1`；渲染类改动必须过宽度/尺寸矩阵（§10.3）。**本仓库没有 CI 闸门**，全靠本地跑。

---

## 1. 目标与非目标

### 1.1 目标

1. **一眼看出总消耗** —— Σ 行是面板最亮的元素，且 Σ 口径在三个表面完全一致。
2. **各 agent 累计消耗** —— `TOTAL` 列。
3. **各 agent 当前消耗** —— `RUN` 列（本次提交以来的累计，见 §5.3）。
4. **cache 命中率** —— ⊕ 计量条（全局）+ 每 agent 的 `⊕NN%`。
5. **消灭口径分叉** —— 现在底部面板与侧栏对同一个"In"给出不同数字（§8 缺陷 1）。
6. **不增加 footer 高度** —— 底部表面从 `4 + agent数` 行降到 `2 + ⌈agent数/每行容量⌉` 行。

### 1.2 非目标（明确不做）

- 不做成本/单价换算（仓库里没有任何价格表，`dist/data/models.json` 只有 `limit.context` 与 `benchmarks`）。
- 不做按模型/按路由档位（fast/balanced/high）的分栏：`ai_response` 的 usage payload **不带 model/tier**（`internal/agents/executor.go:638-645`），`model_routed` 事件甚至没进 `protocol/agent-events.yaml`。要做必须先改 SSOT 并重新生成，属于另一个任务。
- 不做 tokens/s：`duration_seconds` 只在 `llm_call_end`（`executor.go:580-590`）里，与 usage 不在同一事件。
- 不改 rollout 持久化格式（`internal/memory/rollout_writer.go:280-313`）。
- 不做跨会话/历史统计。

---

## 2. 视觉规范

### 2.1 目标效果（侧栏 dashboard，面板 50 列 / 内容 46 列）

```
╭────────────────────────────────────────────────╮
│ ◆ Tokens                           alt+d 收缩  │
│ ────────────────────────────────────────────── │
│ Σ 2.41m                   in 2.35m · out 58.2k │
│ ⊕ ▰▰▰▰▰▰▰▱▱▱ 71%           hit 1.67m · new 800 │
│ ⛁ ▰▰▰▰▰▰▱▱▱▱ 64%               128.4k / 200.0k │
│ ────────────────────────────────────────────── │
│   AGENT         RUN ▸   TOTAL          ⊕ CACHE │
│ ● director    12.4k ▸   1.82m             ⊕74% │
│ ● coding       8.2k ▸  384.2k             ⊕58% │
│ ● repo         3.1k ▸   96.4k             ⊕66% │
│ ● browser      1.2k ▸   42.8k             ⊕31% │
│ ────────────────────────────────────────────── │
│ ⚙ Activity                                     │
│ ● read_file                                    │
│ ┊                                              │
│ ● search_by_regex                              │
│  ctrl+l 详情                                   │
╰────────────────────────────────────────────────╯
```

**行序与语义**（token 段共 `7 + agent行数 [+1]` 行）：

| # | 行 | 语义 | 缺数据时 |
|---|----|------|----------|
| 1 | `◆ Tokens` + 右对齐键帽 | 标题 + `alt+d` 折叠提示 | 恒在 |
| 2 | `─`×inner | 分隔 | 恒在 |
| 3 | `Σ <total>` + 右对齐 `in <i> · out <o>` | **会话总账**（英雄行） | Σ=0 → 整个 token 段不渲染（沿用 `tui_view.go:663-666` 的既有行为） |
| 4 | `⊕ <bar> <pct>` + 右对齐 `hit <r> · new <w>` | **缓存命中率计量条** | `cacheRead+cacheNew == 0` → 整行跳过 |
| 5 | `⛁ <bar> <pct>` + 右对齐 `<ctx> / <window>` | **当前活跃 agent 的上下文占用** | `ctxTokens<=0` → 跳过；`window<=0` → 右段只渲染 `<ctx>`（无 `/ xxx`），bar 用 0 比例 |
| 6 | `─`×inner | 分隔 | 无 agent 行时与第 7 行一起跳过 |
| 7 | 列头 `AGENT / RUN ▸ TOTAL / ⊕ CACHE` | 列语义 | 高度不足时**第一个**被砍 |
| 8.. | agent 行 | 每 agent 的 RUN/TOTAL/⊕ | 按 TOTAL 降序，超预算截断 |
| 末 | `┈ +N more` | 截断提示 | 无截断则不出现 |

**计量条统一语法**（⊕ 与 ⛁ 必须完全同构，这是本设计的"质感"来源）：

```
<glyph> <bar 10格> <pct 整数>   …右对齐明细…
└────── 固定 16 列 ──────┘
```

- `pct` 一律 `%.0f%%`（**注意**：现有 `renderContextLine` 用 `%.1f%%` → `64.2%`，新规范改成 `64%`，见 §8 缺陷 10）。
- bar 填充数 = `int(ratio*cells + 0.5)`，clamp 到 `[0, cells]`；`ratio` 封顶 1.0。
- 左段恒为 16 列，右段右对齐到 inner —— 两条计量条的 bar 起点与 pct 位置严格对齐。

### 2.2 列网格（inner = 46 时）

```
 2   9    1   7   3   7   1   16        = 46
├dot┤name├─┤RUN├▸─┤TOT├─┤⊕CACHE┤
```

| 段 | 宽 | 对齐 | 内容 |
|----|----|------|------|
| dot | 2 | 左 | `●` + 空格，色 = `AgentColor(name)` |
| name | 9 | 左 | `truncateRunes(name, 9)`，bold，色 = `AgentColor(name)` |
| gap | 1 | | |
| RUN | 7 | **右** | `formatToken(runIn+runOut)` |
| arrow | 3 | **居中** | `▸`（读作"本轮 → 累计"） |
| TOTAL | 7 | **右** | `formatToken(totalIn+totalOut)` |
| gap | 1 | | |
| ⊕ CACHE | 16 | **右** | `⊕NN%` |

列头行用同一网格：dot 段留空、`AGENT` 左对齐在 name 段、`RUN`/`TOTAL` 右对齐、`▸` 居中、`⊕ CACHE` 右对齐。

**inner ≠ 46 时**：`dot/gap/RUN/arrow/TOTAL/gap` 固定（合计 21），`name` 固定 9，**余量全部给 ⊕ CACHE 段**（`cacheCol = inner - 30`），右对齐不变。

### 2.3 golden 字符串（实现已逐行核对，均为 46 列）

来自 `TestTokenPanel_GoldenS1`，样例数据：`totalInput=2.35m / output=58.2k / cacheRead=1.67m / cacheNew=800 / ctx=128.4k, window=200k`，四个 agent 按 TOTAL 降序。

```go
"◆ Tokens                           alt+d 收缩 "   // 标题行（fill = 46-8-12 = 26）
"──────────────────────────────────────────────"   // 分隔
"Σ 2.41m                   in 2.35m · out 58.2k"   // Σ 行
"⊕ ▰▰▰▰▰▰▰▱▱▱ 71%           hit 1.67m · new 800"   // 缓存计量条
"⛁ ▰▰▰▰▰▰▱▱▱▱ 64%               128.4k / 200.0k"   // 上下文计量条
"──────────────────────────────────────────────"   // 分隔
"  AGENT         RUN ▸   TOTAL          ⊕ CACHE"   // 列头
"● director    12.4k ▸   1.82m             ⊕74%"   // agent 行
"● coding       8.2k ▸  384.2k             ⊕58%"
"● repo         3.1k ▸   96.4k             ⊕66%"
"● browser      1.2k ▸   42.8k             ⊕31%"
```

> **`formatToken` 的 m 档改成 2 位小数**（`%.2fm`，k 档仍 1 位）：`Σ` 与 `in` 常常只差几个百分点，1 位小数会把两者印成同一个数（`2.4m` / `2.4m`），英雄行就失去了意义。视觉稿里的 `2.41m / 2.35m / 1.82m` 依赖这一点。
> 改这个函数是安全的：`internal/tui` 里只有 token 面板用它（`internal/tui/components` 另有自己的格式化，模型选择框的 `1.0m` 不受影响）。

> **键帽宽度随语言变化**：`dashKeycap("alt+d", "收缩")` = 12 列，`dashKeycap("alt+d", "collapse")` = 16 列。fill 必须用 `inner - lipgloss.Width(title) - lipgloss.Width(keycap)` 现算，**禁止硬编码 26**。

### 2.4 字形与宽度假设

| 字形 | 码点 | EAW 类 | 结论 |
|------|------|--------|------|
| `▸ ⛁ ▰ ▱ ⚙ ┊` | 25B8 / 26C1 / 25B0 / 25B1 / 2699 | **N** | 恒 1 列，安全 |
| `Σ ◆ ● ⊕ ─ │ ╭ ╮ ╰ ╯ ┈` | 03A3 / 25C6 / 25CF / 2295 / 2500… | **A**（Ambiguous） | go-runewidth 默认按 1 列 |

整个 TUI 的边框（`─ │ ╭ ╮`）本身就是 A 类，也就是说"ambiguous = 1"是这个仓库**已经承担**的前提，`Σ`/`◆`/`●`/`⊕` 不引入新风险。规则：**新增字形优先选 N 类**；`▸` 之所以选 U+25B8 而不是 `▶`(U+25B6，A 类)，就是这个原因。若将来要支持 CJK-locale 终端，那是全仓库级别的改造，不在本次范围。

测试断言一律用 `lipgloss.Width`，**不要**给 runewidth 开 `EastAsianWidth` 选项。

### 2.5 颜色令牌（全部复用现有，禁止引入新色号）

| 元素 | 色 | 现有常量 |
|------|----|----|
| 面板边框 | `62` | `dashBorderFg` (`tui_dashboard.go:20`) |
| `◆`、⊕ bar、`⊕NN%` | `141` | `dashAccent` (`:21`) |
| `Tokens` 标题、`Σ` 数值、`TOTAL` 列值 | `250` bold | `dashTitleFg` (`:22`) |
| `RUN` 列值、`in` 值 | `111` | `dashInFg` (`:24`) |
| `out` 值 | `114` | `dashOutFg` (`:25`) |
| 所有标签（`in`/`out`/`hit`/`new`/`AGENT`/`RUN`/`TOTAL`/`⊕ CACHE`） | `244` | `dashLabelFg` (`:23`) |
| ⛁ bar + pct 渐变 | `79` / `214` / `203`（<60% / 60–85% / >85%） | `tui_context_line.go:50-56` |
| agent 圆点与名字 | `AgentColor(name)` | `styles.go:210` |
| 分隔线 | `238` | `dashSepFg` (`:26`) |
| 键帽 | `247` bold + `241` italic | `dashKeycap` (`:31-35`) |
| `┈ +N more` | `243` italic | `tui_dashboard.go:291-294` |

**视觉层级**：Σ 最亮（250 bold）→ 每 agent 的 TOTAL 次之（250 bold）→ RUN 用 111 作参照。"当前"有色但不抢总账。

---

## 3. 数字口径（唯一真值表）

上游事实：`llm.TokenUsage`（`internal/llm/engine.go:61-71`）有 6 个 int64 字段。**两条 provider 路径对 `PromptTokens` 的语义相反**：

| | `PromptTokens` | `TotalInputTokens` |
|---|---|---|
| OpenAI（`engine_openai.go:222-259`、`:386-413`） | **含** cached | `= PromptTokens`（`:231`） |
| Anthropic（`engine_anthropic.go:464-476`、`:645-657`） | **不含** cache | `= input + cacheRead + cacheCreate`（`:474`） |

⇒ **任何"输入总量"都必须用 `TotalInputTokens`**，这是本次重构最重要的一条口径规则。

| 显示 | 公式 | 说明 |
|------|------|------|
| `Σ` | `totalIn + totalOut` | `totalIn` 见下；**不要**再用 `inputTokens + outputTokens`（Anthropic 路径会漏掉全部缓存） |
| `totalIn` | `m.totalInputTokens`；为 0 时回落 `inputTokens + cacheRead + cacheNew` | 回落分支保留（老 rollout / 估算路径没有 `total_input_tokens`），沿用 `tui_dashboard.go:201-204` |
| `totalOut` | `m.outputTokens` | |
| `in` | `totalIn` | 与 Σ 同源，标签含义在两条 provider 路径下一致 |
| `out` | `totalOut` | |
| `⊕ pct`（全局） | `cacheRead / totalIn * 100`，`%.0f` | 分母必须是 `totalIn`（commit `6a35cbc` 的修法），`totalIn<=0` → 整条计量条不渲染 |
| `hit` | `formatToken(cacheRead)` | |
| `new` | `formatToken(cacheNew)` | `cacheCreationInputTokens` |
| `RUN`（每 agent） | `runTotalIn + runOut`（该 agent 本轮） | 见 §5.3 的重置语义 |
| `TOTAL`（每 agent） | `au.TotalInputTokens + au.OutputTokens`，`TotalInputTokens==0` 时回落三项求和 | 沿用 `tui_dashboard.go:273-276` |
| `⊕NN%`（每 agent） | `au.CacheReadInputTokens / auTotalIn * 100` | 分母同上 |
| `⛁ pct` | `ctxTokens / contextWindow * 100`，封顶 100 | |
| `ctxTokens` | **当前活跃 agent** 最近一次调用的归一化输入（覆写，不累加） | `tui_update_tasks.go:88` 的既有语义，必须保留；闸门 `TestContextTokens_OverwriteNotAccumulate` |
| `contextWindow` | `contextWindowFromCfg`（`tui_model.go:597-609`）：**provider `context_window` 优先**，未设置才查模型目录 | ⚠️ 与压缩阈值口径相反（`internal/compression/threshold.go:18-26` 是**目录优先**）。这是 AGENTS.md §11 坑 3 记录的既有分裂，**本次不修**，但渲染时 `window<=0` 必须降级为无分母形态 |

**估算路径**：provider 不返回 usage 时，`executor.go:618-627` 用 `EstimateTokens` 兜底并打 `estimated: true`（`:651`）。TUI 目前**不读这个标记**。本次仍不做特殊标记（避免多一个视觉状态），但要求：估算分支的 usage 里**没有** cache 字段也没有 `total_input_tokens`，因此 `totalIn` 会走回落分支、⊕ 计量条自然不渲染 —— 这是可接受的降级，不要为此加特例代码。

**Anthropic 零 usage 陷阱**（不在本次修复范围，但实现时会撞到）：`engine_anthropic.go:697-699` 在无 usage 时返回**非 nil 的零值** `&TokenUsage{}`，而估算兜底的判据是 `resp.Usage == nil`，于是该路径会静默发布全 0。TUI 侧的表现是"Σ 不增长"，**不要**在 TUI 里加补偿逻辑。

---

## 4. 渲染架构：一个渲染器，三个表面

### 4.1 表面矩阵

| 表面 | 何时出现 | 外框宽 | inner | 高度 |
|------|----------|--------|-------|------|
| **侧栏 dashboard** | `dashboardVisible()`：`!dashboardCollapsed && termWidth>=120 && termWidth-50>=40`（`tui_dashboard.go:61-84`） | 50（`dashboardPanelWidth`） | **46** | = viewport 高，与 Activity 段共享 |
| **底部面板** | `!dashboardVisible()` 且有用量（`tui_render.go:74-86`） | `termWidth-2` | `termWidth-6`（≤ 113） | 内容行数 |
| **底部折叠** | 底部面板 + `tokenDashboardCollapsed`（`alt+t`） | `termWidth-2` | `termWidth-6` | 1 行（+ 键帽内联） |

`dashboardPanelWidth` 恒为 50 ⇒ **侧栏 inner 恒为 46** ⇒ S1 是像素级确定的，不存在"侧栏也要降级"的情况。降级/升档只发生在底部表面。

### 4.2 渲染器 API（新建 `internal/tui/token_panel.go`）

```go
// tokenSnapshot 是渲染器的唯一输入：所有口径计算都在构造它时完成，
// 渲染层只做排版，不再碰 model 上的任何计数器。
type tokenSnapshot struct {
    totalIn, totalOut   int64
    cacheRead, cacheNew int64
    ctxTokens           int64
    ctxWindow           int
    agents              []agentTokens // 已按 TOTAL 降序（同值按名字），已过滤零用量
    active              agentTokens   // 活跃 agent 的那一行，供折叠形态取用
}

type agentTokens struct {
    name              string
    runIn, runOut     int64 // 本轮
    totalIn, totalOut int64 // 会话累计
    cacheRead, cacheNew int64
}

type tokenPanelSpec struct {
    innerWidth int
    maxRows    int    // 0 = 不限高
    keycap     string // dashKeycap(...) 的结果；"" = 不渲染。标题恒为 "◆ Tokens"，不是可配项
    collapsed  bool   // 单行摘要形态
}

func (m *model) tokenSnapshot() tokenSnapshot
func buildTokenPanel(snap tokenSnapshot, spec tokenPanelSpec) []string // 返回不含边框的行
func (m *model) renderFooterTokenPanel() string                        // 底部表面：行 + 圆角边框
func (s tokenSnapshot) signature() string                              // 侧栏缓存键的数字串

func formatToken(n int64) string            // 从 tui_view.go:632 迁入
func normalizedInput(total, prompt, cacheRead, cacheNew int64) int64
func cacheHitRatio(cacheRead, cacheNew, totalIn int64) (float64, bool)
func meterLine(glyph string, ratio float64, detail string, inner, cells int, c color.Color) string   // 整行（明细右对齐）
func meterInline(glyph string, ratio float64, detail string, cells int, c color.Color) string        // 不拉伸，供折叠行
```

**组合方式**：
- `renderDashboard`（侧栏）= `buildTokenPanel(...)` 的行 + 分隔 + Activity 段的行 → 一起过边框收敛（`tui_dashboard.go:340-385` 的逻辑**原样保留**）。
- `tokenDashboardBlock`（底部）= `buildTokenPanel(...)` 的行 → 边框。
- Activity 段**完全不进**渲染器，`RenderTimeline` 调用（`tui_dashboard.go:311`）保持原样。

### 4.3 宽度阶梯（底部表面）

`inner` 决定细节档位；**≥46 时与 S1 同构**（侧栏恒为 46）。两个门槛彼此独立，不要耦合：

| inner | 计量条 bar 格数 | 列头 | agent 排布 | Σ 行右段 |
|-------|----------------|------|-----------|---------|
| ≥ 90 | 20 | 有 | 横条，每行 `inner/48` 段（≥98 才塞得下 2 段） | `in 2.35m · out 58.2k` |
| 60–89 | 16 | 有 | 横条（实际每行 1 段） | 同上 |
| 46–59 | 10 | 有 | 横条（每行 1 段，缓存列固定 16） | 同上 |
| 40–45 | 10 | **无** | 一行一个，缓存列 = `inner - 30` | 同上 |
| < 40 | **无 bar**（`⊕ 71%`） | 无 | 一行一个，纯文本 `truncateCells` 到 inner | `in 2.35m·out 58.2k`（去 ` · ` 空格） |

**实现偏差记录**（相对本稿上一版）：

1. **横条档门槛从 60 降到 46**（`tokenFlowFrom`）。原门槛下 60–97 列区间每行仍只塞得下一个 46 列段，等于凭空多一档却没有收益；而 `inner=46` 时"段宽 46 + 缓存列 16"与单列档的 golden **完全一致**，所以合并成一档更简单。
2. **取消"每 agent 缓存长形态"**（`⊕74% hit 1.2m · new 0.4k`）。单列档最大 inner 是 45，长形态要求缓存列 ≥26 —— 那条分支永远走不到，是死代码；缓存明细由 `⊕` 那条整行计量条统一给出，agent 行只留 `⊕NN%`。
3. 因此 `tokenLongCacheFrom` 常量已删除；bar 格数改用 `tokenMeterCellsMidFrom=60` / `tokenMeterCellsWideFrom=90` 两个显式门槛。

窄档（inner 34）实测样例：

```
╭────────────────────────────────────╮
│ ◆ Tokens                    alt+d  │
│ Σ 2.41m     in 2.35m·out 58.2k     │
│ ⊕ 71%                              │
│ ⛁ 128.4k · 64%                     │
│ ────────────────────────────────── │
│ ● director 12.4k▸1.82m ⊕74%        │
└────────────────────────────────────╯
```

### 4.4 高度阶梯（侧栏，与 Activity 抢行）

预算入口在 `renderDashboard`：给 Activity 段留 `timelineReserveRows = 4` 行（分隔线 + 段标题 + 1 条事件 + 快捷键提示），下限 3 行。

> 上一版只留 1 行，实测会把整条时间线挤出面板 —— 改成 4 行是让两个段都能活着的最低成本。

`buildTokenPanel` 用"从富到简的档位循环"实现：渲染一档，行数超预算就升到下一档。**每档只砍一整行**，因为行内裁剪（去 `in/out` 明细、去 bar）根本不省行 —— 那些是**宽度**阶梯的事，不要混进高度阶梯。

| 档位 | 相对上一档少掉 | 4 个 agent 时的行数 |
|------|---------------|--------------------|
| `tokenTierFull` | — | `7 + 4 = 11` |
| `tokenTierNoHeader` | 列头 | 10 |
| `tokenTierNoSep` | agent 段前的分隔线 | 9 |
| `tokenTierCacheInline` | `⊕` 独立行（命中率内联进 Σ 行尾） | 8 |
| `tokenTierAgentsSummary` | agent 行 → 一行 `┈ +N agents · ⊕NN%` | 6 |
| `tokenTierNoSep1` | 标题后的分隔线 ⇒ 极限 **3 行**：标题 / Σ / ⛁ | 3 |

另有一条与档位无关的硬规则，在 `tokenAgentBlock` 里：**先算预算再产出列头与分隔线**。装不下"至少一行 agent 内容"时整段让位，绝不画出"有列头、下面空着"的残缺表格；截断时保留 TOTAL 降序的前 N 个，并补 `┈ +N more`。

**永不被砍**：标题行、Σ 行、⛁ 行。

实测（`TestTokenPanel_HeightLadder`，4 个 agent）：`maxRows ≥ 9` 才有列头；`6–8` 行只剩 `┈ +4 agents` 摘要；`≤ 5` 行连摘要都放不下，agent 段整段消失。

### 4.5 折叠形态（`alt+t`，底部表面）

```
[director] ▸12.4k   ·   Σ 2.41m ⊕71%   ·   ⛁ ▰▰▰▰▰▰▱▱▱▱ 128.4k          alt+t 展开 
```

- 三段：活跃 agent 的本轮量 → 全局 Σ + 命中率 → ⛁ 上下文。
- ⛁ 段用 `meterInline`（**不拉伸**）：`meterLine` 会把明细右对齐到 inner、恒返回整行宽度，直接拼进摘要会把整行撑爆后被 `truncateCells` 截断 —— 只留当前值、不带 `/ 窗口上限`。
- 键帽沿用 `dashKeycap`（与 `alt+d`、`ctrl+l` 同一族），不再用 `(alt+t 展开)` 这种括号写法。
- `alt+t` 只影响底部表面（侧栏由 `alt+d` 整体收起）。**侧栏的 cache key 里不要再放 `tokenDashboardCollapsed`**（见 §8 缺陷 5）。

---

## 5. 状态模型改动

### 5.1 新增：每 agent 的本轮用量

现状：`currentAgentRunTokens AgentRunTokens`（`tui_model.go:463`）是**单值**，`updateAgentContextTokens`（`tui_update_tasks.go:79-90`）在 agent 切换时整体重置 ⇒ **"当前各 agent 消耗"目前无法渲染**，这是本次必须新增的状态。

```go
// tui_model.go —— 替换 currentAgentRunTokens (:463)
runTokensPerAgent map[string]*AgentRunTokens // 本轮（自最近一次提交）每 agent 的用量
runAgent          string                     // 活跃 run 是哪一个 agent
```

`AgentRunTokens`（`tui_model.go:246-255`）已经带 `ContextTokens`，直接复用。

**不能借用 `m.currentAgent` 当活跃指针**（本稿上一版的错误）：`m.currentAgent` 只在 `taskRunning` 时才被 `ai_response` 赋值（`tui_update_tasks.go:197-201`），而 `ContextTokens` 必须在任务未运行时也能更新（历史恢复、单轮问答都走这条路）。所以新增 `runAgent`，由 `runTokens()/setRun()/updateAgentContextTokens()` 与事件里的 `From` 同步维护；测试用 `m.currentRun()` 取活跃项，`m.setRun(...)` 构造状态。

⛁ 行取 `m.runTokensPerAgent[m.runAgent].ContextTokens`（缺失则 0 → 整行跳过）。

### 5.2 需要改的读写点（逐处）

| 位置 | 改动 |
|------|------|
| `tui_model.go:463` | 字段替换；`:856` 附近（`tokenUsagePerAgent` 的 `make` 处）补新 map 初始化 |
| `tui_update_tasks.go:79-90` `updateAgentContextTokens` | 改为写 `runTokensPerAgent[agent].ContextTokens`，**不再重置整个结构** |
| `tui_update_tasks.go:178-195` | RUN 累计写入 `runTokensPerAgent[agentNameForRun]`（不存在则建） |
| `tui_update_tasks.go:716-724` 压缩事件 | 覆写对应 agent 的 `ContextTokens`（agent 名取 `event.From`，为空则用 `m.currentAgent`） |
| `tui_tabs.go:71 / :198 / :281 / :354` | tab 结构体字段、save、restore、reset 四处同步 |
| `tui_resume.go:936-946` `resetTokenState` | 清 `runTokensPerAgent` |
| `tui_resume.go:948-984` `applyRolloutRunState` | **RUN 不恢复**（本轮语义 ⇒ 从 0 开始）；只恢复 Σ 与 `ContextTokens`。注意 `:956` 现有 `totalInputTokens = Input + CachedInput` 丢了 `cache_creation`，见 §8 缺陷 6 |
| `tui_view.go:752 / :760 / :802`、`tui_dashboard.go:225` | 随旧渲染器一起删除/改调 snapshot |

### 5.3 RUN 的重置语义（**需要人确认的唯一一处**）

本设计取 **"本轮"**：在 `submitTaskWithContent`（`tui_tasks.go:57`，`:66-75` 那段）与 `submitFollowUp`（`tui_tasks.go:113`，`:119-128` 那段）**两处**都调用 `resetRunTokens(m)`，只清 RUN、不动 Σ 与 per-agent TOTAL。

理由：一个任务可能包含多轮追问，若 RUN 跨轮累加，它会一路涨到与 TOTAL 无差别，"当前"就失去意义；累计视角已经由 Σ / TOTAL 承担。

> 若要改成"每任务"语义：把 `submitFollowUp` 里的那一次 `resetRunTokens` 删掉即可，其余代码不变。**Coding-Agent 不要自行改这个决定。**

`resetRunTokens` 必须调 `m.invalidateFooterCache()`（RUN 变了，两个表面的缓存都失效）。

---

## 6. 缓存与失效

现有三套缓存：`dashboardCache`/`dashboardCacheKey`（侧栏，`tui_model.go:512-513`）、`cachedTokenDashboard`/`tokenDashboardValid`（底部，`:507-508`）、`cachedFooterHeight`/`footerHeightValid`（`:520-521`）。三者都由 `invalidateFooterCache()`（`tui_render.go:96-104`）一把清空。

**保留这套结构**，但修两处：

1. **侧栏 cache key 的构造要跟着新口径改**。现有 key（`tui_dashboard.go:155-165`）已包含 `tokenSig`（每 agent 四个计数）与尾部追加的 `ctx:`。新增 RUN 后，`tokenSig` **必须**把 `runIn/runOut` 也拼进去，否则本轮数字变化不会触发重渲染。`:162-165` 那段注释解释的"压缩事件不改任何累计量、只改 ContextTokens，所以必须单独进 key"这条不变量继续成立，别删注释。
2. **`alt+t` 的顺序 bug**（`tui_update_keys.go:609-614`）：现在是"先 render 再 `invalidateFooterCache()`"，而后者会把 `cachedTokenDashboard` 清空、`tokenDashboardValid` 置 false ⇒ 那次 render 是纯粹的无效功。改成**只 toggle + invalidate**，渲染交给 `tokenDashboardBlock` 的懒计算分支（`tui_render.go:78-81`）。同类写法在 `tui_tasks.go:74-75`、`:127-128` 也是先 render 后不 invalidate（那两处反而**缺** invalidate，导致 tab 切换后可能命中陈旧缓存）——统一成"改状态 → invalidate → 懒渲染"。

`hasRunning` 时跳过侧栏缓存以支持动画（`tui_dashboard.go:124-130`）的行为保留。

---

## 7. footer 高度不变量（最容易踩的坑）

`computeFooterHeight`（`tui_render.go:17-55`）必须与 `View()` 的 block 拼接（`tui_view.go:208-245`）**逐行一一对应**，差一个空行整屏抖动（AGENTS.md §11 坑 7）。

- token 段在两处的位置：`tui_render.go:45` ↔ `tui_view.go:230`，都经 `tokenDashboardBlock()`。
- `tokenDashboardBlock` 前置一个 `"\n"`（`tui_render.go:85`）——**缓存命中与未命中必须走同一条加空隙的路径**，否则命中与否会改变 footer 行数。现有注释已写明，保留。
- 本次改动会让底部面板行数变化（`4+N` → `2+⌈N/容量⌉`），因此 `TestView_ExactHeight`（`tui_chrome_test.go:274`）与 `TestView_ExactHeightWithPanels`（`:537`，含 `token-dashboard-collapsed` / `-expanded` / `side-dashboard` 三变体）**必须重跑并更新期望值**。

事件侧不变量（AGENTS.md §11 坑 6）：`listenForEvents`（`tui_helpers.go:21-29`）是一次性 cmd，`handleTaskEventMsg`（`tui_update_tasks.go:92`，是 if-chain 不是 switch）的**每个 return 都要 re-arm**；弹窗守卫白名单在 `tui_update.go:42-69`。本次不新增消息类型，但如果新增了叠加态，必须显式放行。

---

## 8. 顺带修复清单（每条都是实测确认的缺陷）

| # | 缺陷 | 位置 | 修复后行为 |
|---|------|------|-----------|
| 1 | 同一个"In"在两个表面口径不同：底部用原始 `inputTokens`，侧栏用归一化 `totalInputTokens` | `tui_view.go:681` vs `tui_dashboard.go:201-205` | 统一走 snapshot 的 `totalIn` |
| 2 | `Σ = inputTokens + outputTokens` 在 Anthropic 路径**漏掉全部缓存**（`input_tokens` 不含 cache） | `tui_view.go:663` | `Σ = totalIn + totalOut` |
| 3 | agent 行同样分叉：底部用 `au.InputTokens`，侧栏用 `auTotalInput` | `tui_view.go:730` vs `tui_dashboard.go:273-277` | 统一 |
| 4 | agent 名按**字节**截断（`agentLabel[:9]`），中文名会被切碎且列宽算错 | `tui_view.go:725-726` | 统一走 `truncateRunes`（`tui_dashboard.go:49`） |
| 5 | `alt+t` 在侧栏是 no-op：`tokenDashboardCollapsed` 只出现在 cache key 里，布局从不读它 | `tui_dashboard.go:157` | 从侧栏 cache key 移除；折叠只作用于底部表面 |
| 6 | `/resume` 后 `cache_creation` 永久丢失（`totalInputTokens = Input + CachedInput`，且 `resetTokenState` 把 `cacheCreationInputTokens` 清零后无人恢复） | `tui_resume.go:940 / :956` | rollout 里没有这个量 ⇒ **无法完全恢复**；至少让 `⊕` 分母与 `hit` 自洽（都用 `CachedInput`），并在 `new` 缺数据时不渲染 `new` 段 |
| 7 | 底部面板分隔线硬编码 48 列，而真实内容宽是 `termWidth-6` | `tui_view.go:721` | 一律 `strings.Repeat("─", innerWidth)` |
| 8 | `alt+t` 与 tab 切换处的 render/invalidate 顺序错误（render 结果立刻被丢弃，或缺 invalidate 命中陈旧缓存） | `tui_update_keys.go:609-614`、`tui_tasks.go:74-75 / :127-128` | 统一"改状态 → invalidate → 懒渲染" |
| 9 | TUI 自己的 `len(content)/4` 兜底分支不可达（三个发布方总带 `usage` 键） | `tui_update_tasks.go:219-233`（发布方：`executor.go:638/647`、`planner_run.go:459/467`、`meta.go:151/158`） | **删掉**该分支；`usage` 缺失时按"无数据"处理（不累加、不渲染） |
| 10 | pct 精度不一致：ctx 用 `%.1f%%`，缓存用 `%.0f%%` | `tui_context_line.go:74` vs `tui_dashboard.go:100` | 统一 `%.0f%%` |

---

## 9. 实施步骤（有序，每步都能独立 build + test）

1. **口径层**：新建 `token_panel.go`，实现 `formatToken`（从 `tui_view.go:632` 迁入）、`meterRow`、`tokenSnapshot`/`agentTokens`、`cacheStats`（返回 `{rate, hit, new}`）。此步不改任何渲染，`go build` + `go vet` 过即可。
2. **状态层**：加 `runTokensPerAgent`，改 §5.2 表里的所有读写点，加 `resetRunTokens` 并挂到两个提交入口。此步结束时旧渲染器仍在，但 `currentAgentRunTokens` 已删除 ⇒ 旧渲染器改为从新 map 取值（临时适配即可，下一步会删）。
3. **渲染层**：实现 `buildTokenPanel`（宽度阶梯 + 高度阶梯 + 折叠形态），先把**侧栏**接上去（`renderDashboard` 的 token 段替换为 `buildTokenPanel`，Activity 段与边框逻辑不动）。跑 `tui_dashboard_border_test.go`。
4. **底部表面**：`tokenDashboardBlock` 改调 `buildTokenPanel`；**删除** `renderTokenDashboard`、`renderCollapsedTokenDashboard`、`formatCacheInfo`。跑 `tui_chrome_test.go`（含高度矩阵）。
5. **清理**：修 §8 的 5/7/8/9/10；`renderContextLine` 收敛为 `meterRow` 的一层薄封装（或直接删，让 ⛁ 行走 `meterRow`）。
6. **测试补齐**：§10.2 的新增用例 + golden。
7. **人工核对**：真终端里跑 `./yagent`，在 120/100/80/60/40 列宽下各看一眼三个表面；`alt+d`/`alt+t`/`ctrl+l` 各按一次。

每步之后都要跑：

```bash
go build -o yagent .                                  # dist/bin 已有产物时可用
go vet ./...
go test ./internal/tui/... -count=1                   # 秒级
```

> **gofmt 注意**：`internal/tui` 有存量对齐漂移（AGENTS.md §9.1）。改到被 `gofmt -l` 报出的文件时先 `gofmt -d` 区分"我写的"和"历史遗留"，**不要全量重排**。新建的 `token_panel.go` 必须干净。

---

## 10. 测试计划

### 10.1 必须更新的既有测试

| 测试 | 位置 | 为什么要改 |
|------|------|-----------|
| `TestRenderContextLine_BarStyle` / `_Degraded` / `_Empty` | `tui_token_test.go:834 / :848 / :865` | ⛁ 行形态变了（bar 左置、pct 整数、明细右对齐） |
| `TestFormatCacheInfo` | `tui_token_test.go:287` | `formatCacheInfo` 被删 |
| `TestFormatCacheShort` | `tui_token_test.go:321` | `formatCacheShort` 被 `cacheStats` + 长/短形态取代 |
| `TestCollapsedDashboard_*` | `tui_token_test.go:709 / :719` | 折叠形态重设计 |
| `TestTokenDashboard_IndependentContextLine` | `tui_token_test.go:737` | 调 `renderTokenDashboard`（已删） |
| `TestDashboardCacheKey_IncludesContextTokens` | `tui_token_test.go:767` | key 里多了 RUN 分量、少了 `tokenDashboardCollapsed` |
| `TestDashboardCtx_DoesNotExceedPanelWidth` | `tui_token_test.go:798` | 保留并加强为全表面 |
| `TestView_ExactHeight` / `WithPanels` | `tui_chrome_test.go:274 / :537` | 底部面板行数变化 |
| `TestDashboardBorder_*`（4 个） | `tui_dashboard_border_test.go:21 / :102 / :159 / :214` | fixture 里的 `inputTokens/outputTokens` 需补 `totalInputTokens` 与新 map |
| `tui_resume_restore_test.go:351-387` | | `:387` 断言 `renderTokenDashboard()` 含 `592.7k` ⇒ 改调新渲染器 |

**必须保持绿且不改语义**的：`TestTokenCounting_OnlyAiResponseCounts`(`:16`)、`_AiResponseAccumulates`(`:49`)、`_CurrentAgentReset`(`:140`)、`TestContextTokens_*`(`:570-707`)、`TestTotalInputTokens_*`(`:358/:411/:444`)、`TestCacheHitRate_OpenAIPath`(`:494`)、`TestContextWindowFromCfg_CatalogFallback`(`:876`)。（原列于此的 `_EmptyContentNotOverwriteStream`(`:207`) 已于 2026-10-14 单测整理中删除：它只重复 `:49` 已钉的计数器，"空块不覆盖流式内容"这条真正的契约由 `tui_last_message_markdown_test.go` 的 `TestAIResponse_ShortStreamFlushedOnEnd` 钉住。）其中 `TestTokenCounting_CurrentAgentReset` 与 `TestContextTokens_AgentSwitchResetsRun` 的**期望值会变**（RUN 不再在 agent 切换时清零，而是各 agent 独立保留），改之前先读懂它们钉的是哪条语义，并在 PR 描述里写明。

### 10.2 新增测试（已落地）

`token_panel_test.go`：

1. `TestTokenPanel_GoldenS1` —— §2.3 的 11 行逐行 `==`（`stripANSI` 后），并断言每行 `lipgloss.Width == 46`。
2. `TestTokenPanel_WidthLadder` —— inner ∈ `{30,34,40,46,52,60,90,114}`：无行超界；首行恒为标题；列头仅 `inner>=46`；agent 排布行数由 `agentFlowPerRow` 推导。
3. `TestTokenPanel_HeightLadder` —— maxRows ∈ `{3..12}`：`len(rows) <= maxRows`；标题/Σ/⛁ 恒在；**列头出现时下面必须有 agent 行**（禁止空表头）；截断提示最多 1 条。
4. `TestTokenPanel_SigmaUsesNormalizedInput` —— Anthropic 形态（`input=100, cacheRead=900, totalInput=1000, output=50`）下 Σ 必须是 `1.1k`，且不得出现 `Σ 150`（钉 §8 缺陷 2）。
5. `TestTokenPanel_PerAgentRunIndependent` —— 两个 agent 交替 `ai_response`，RUN 各自累计互不清零，`runAgent` 跟到最后一条事件。
6. `TestTokenPanel_RunResetOnSubmit` —— `resetRunTokens` 后 RUN 与 `ctxTokens` 归零，Σ 与每 agent TOTAL 不变。
7. `TestTokenPanel_FooterAndSidebarAgree` —— 同一 model 在侧栏（inner 46）与底部（inner 94）上 `Σ / in·out / 命中率 / 各 TOTAL` 逐片段相同。这是"消灭口径分叉"这条目标的直接证据。
8. `TestTokenPanel_FooterFitsTermWidth` —— `{60,80,100,119} × {展开,折叠}`：无行超终端宽；`tokenDashboardBlock` 行数与缓存面板高度自洽；`View()` 行数恰等于终端高度（合并了原计划的 `TestFooterHeight_MatchesView_TokenPanel`）。
9. `TestTokenPanel_AiResponseFeedsSnapshot` —— 端到端：OpenAI 形态（prompt 含 cached）命中率 30% 不被算成 20%；缺 `total_input_tokens` 时回落三项相加。

`tui_token_test.go`（替换被删除的旧渲染器测试）：

- `TestCacheHitRatio`、`TestTokenCacheMeterRow`（替 `TestFormatCacheInfo` / `TestFormatCacheShort`）
- `TestTokenCtxMeterRow_BarStyle` / `_NoWindow` / `_Empty`（替 `TestRenderContextLine_*`）
- `TestTokenPanel_CollapsedEmptyWhenNoUsage` / `TestTokenPanel_CollapsedOneLine`（替 `TestCollapsedDashboard_*`）
- `TestTokenPanel_CtxLineAppearsAndDisappears`（替 `TestTokenDashboard_IndependentContextLine`）
- `TestDashboardCacheKey_IncludesContextTokens`（保留，改文案断言）+ **新增** `TestDashboardRunTokensInvalidateCache`（RUN 必须进缓存键，否则新列永远不会刷新）
- `TestTokenCounting_NoUsageMetadataIgnored`（替 `..._NoUsageMetadataFallback`，钉住 §8 缺陷 9 的反向行为）

> 踩过的两个坑，写在这是为了避免下次重演：
> ① `runTokensPerAgent` 的 `TotalInputTokens` 优先于 `InputTokens`，所以只改 `InputTokens` 的测试断言不会动 —— 要动 `TotalInputTokens`。
> ② `meterLine` 恒返回整行宽度（明细右对齐到 inner），因此**不能**直接拼进折叠行，必须用 `meterInline`。

### 10.3 矩阵与 fixture（沿用既有约定，AGENTS.md §6.8）

- 宽度矩阵 `{30,40,60,80,100,120,160,200,240}`（`tui_chrome_test.go:47`、`:676`）。
- 终端尺寸矩阵 `{24,20}…{200,60}`（`tui_chrome_test.go:275-278`）与 `{100,34},{120,40},{160,50}`（`:575`）。
- fixture：`newTestModel()`（`tui_last_message_markdown_test.go:17`）、`feedEvent`(`:59`)、`buildEvent`(`:65`)、`chromeTestModel(t,w,h)`（`tui_chrome_test.go:22`）。
- 断言**精确单元格宽度**，不用子串匹配（除 golden 逐行比对）。
- ANSI stripping 已有三份重复实现（`tui_dashboard.go:108-114`、`tui_dashboard_border_test.go:12-16`、`tui_chrome_test.go:14-18`）；新测试复用 `tui_dashboard_border_test.go` 的 `stripANSI`，**不要**再加第四份。

---

## 11. 验收标准

- [ ] 侧栏在 inner=46 时逐行等于 §2.3 的 golden（含分隔线与列头）。
- [ ] Σ、`in`、`TOTAL` 三处的输入量全部来自 `TotalInputTokens` 口径；Anthropic 形态数据下 Σ 不再漏缓存。
- [ ] 每个 agent 一行，同时可见 RUN（本轮）与 TOTAL（会话累计）与 `⊕NN%`。
- [ ] ⊕ 与 ⛁ 两条计量条左段严格 16 列对齐，bar 起点同列。
- [ ] 三个表面（侧栏/底部/折叠）由**同一个** `buildTokenPanel` 产出；`renderTokenDashboard`、`renderCollapsedTokenDashboard`、`formatCacheInfo` 已从仓库消失（`grep` 为零命中）。
- [ ] 任意 `termWidth ∈ {30..240}` × `termHeight ∈ {20..60}` 下：无一行超出面板宽、footer 高度与 `View()` 自洽、无空行抖动。
- [ ] `alt+d` 收起/展开侧栏、`alt+t` 折叠底部面板、`ctrl+l` 进 timeline 全屏，三者都正常 re-arm 事件监听。
- [ ] `go vet ./...` 干净；`go test ./internal/tui/... -count=1` 全绿；`gofmt -l` 对新增文件为零。
- [ ] 新标签（若有）改满 i18n 四处：struct 字段（`i18n.go:13`）、zh（`:171-174`）、en（`:259-262`）、`GetText` case（`:411-418`）。**建议列头 `AGENT/RUN/TOTAL/⊕ CACHE` 不进翻译表**，与既有 `Total`/`Activity` 的英文常量处理保持一致。

---

## 附录 A：宽度自检（无需启动 TUI）

本仓库无法在此环境跑真终端，因此所有布局断言都必须能在 `go test` 里完成。手工核对视觉稿时用的等价口径：

```python
# CJK(W/F)=2，其余（含 East-Asian-Width 的 A 与 N 类）=1 —— 与 go-runewidth 默认一致
import unicodedata as u
width = lambda s: sum(2 if u.east_asian_width(c) in ("W","F") else 1 for c in s)
```

Go 侧一律用 `lipgloss.Width`（本仓库唯一的宽度真值，无 `runewidth` 直接调用点）。
