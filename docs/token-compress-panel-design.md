# Token Dashboard 增量：⧉ COMPRESS 区块

> **本文是给 Coding-Agent 的实施规格书，不是讨论稿。** 所有列宽、字符串、file:line 均已实测核对（核对时间 2026-10-14，基线 commit `43bd883`）。
> 每一行 golden 都用与生产一致的宽度口径（`lipgloss.Width`：CJK=2、East-Asian-Width `A`/`N` 字形=1）逐行断言过，可直接当 golden 用。
>
> **本文只描述增量。** S1 统一渲染器（`buildTokenPanel` / `tokenSnapshot`）已经落地，规格见 `docs/token-dashboard-design.md`；本次不改它、不重排它、不碰它的 golden。
> 相关既有约定见 `AGENTS.md` §6.8（TUI）、§6.5（三档压缩）、§11 坑 6/7（事件无重放、footer 高度自洽）。

---

## 0. TL;DR

**做什么**：把三档上下文压缩的**实际效果**显示出来。压缩事件带着 `saved_tokens` / `truncated_tools` 到达 TUI，但现在只在 `tui_update_tasks.go:681-689` 取了 `compressed_tokens` 覆写 ⛁ 行，**省了多少、截了哪些工具、到过第几档全部丢弃**。

视觉上是在侧栏 dashboard 的 **Activity 段之下**追加一个区块：

```
│ ────────────────────────────────────────────── │
│ ⧉ COMPRESS −1.42m (38%)                     T2 │
│   T1 rule         −180.2k  ×2 ▰▰▱▱▱▱▱▱▱▱▱▱▱▱▱▱ │
│   T2 emergency     −1.24m  ×1 ▰▰▰▰▰▰▰▰▰▰▰▰▰▰▱▱ │
│   T3 ultimate           0  ×0 ▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱ │
│   ⛁ ctx ▁▂▄▇█▃▃▄               128.4k / 200.0k │
│   cut: read_file×7 search_by_regex×2           │
```

**动哪些文件**：

| 文件 | 动作 |
|------|------|
| `internal/tui/compress_panel.go` | **新建**：`compressSnapshot` + `buildCompressBlock` + 摘要行 |
| `internal/tui/token_panel.go` | 新增 `setContextTokens` 收敛两处 ctx 写入；`buildTokenPanel` **不改** |
| `internal/tui/tui_dashboard.go` | Activity 段之后追加 compress 段（`:173` 之后、`:175` 的宽度收敛之前）；新增 `compressReserveRows`；cache key 加压缩签名 |
| `internal/tui/tui_update_tasks.go` | `:681-689` 共享分支内累计压缩统计；`:181` 改调 `setContextTokens` |
| `internal/tui/token_panel.go:276` | `updateAgentContextTokens` 内部改走 `setContextTokens` |
| `internal/tui/tui_render.go` / `token_panel.go:699` | 底部面板追加 1 行摘要（折叠态不加） |
| `internal/tui/tui_model.go` / `tui_tabs.go` / `tui_resume.go` | 新状态字段、tab 存取、reset 挂点、resume 重建 |
| `internal/memory/rollout_types.go` / `rollout_writer.go` / `rollout_reader.go` | `CompactedPayload` 加 `kind`/`saved_tokens`，reader 暴露压缩记录（§8） |
| `internal/agents/executor.go` / `director/planner_run.go` | 只在 4 个 `WriteCompacted` 调用点补两个字段，**不改事件 payload** |

**不能动**：`buildTokenPanel` 的输出与 golden、`RenderTimeline` 的语义、事件 payload 的键名、`protocol/`、状态栏、i18n 既有键。

**怎么验证**：`go build -o yagent .` + `go vet ./...` + `go test ./internal/tui/... -count=1`；涉及 `internal/memory` 与 `internal/agents` 的步骤再跑 `go test ./internal/memory/... ./internal/agents/... -count=1 -timeout 300s`。**本仓库没有 CI 闸门**，全靠本地。

---

## 1. 目标与非目标

### 1.1 目标

1. **压缩到底做了什么** —— 三档各自触发几次、各省了多少，一眼可读。
2. **危险程度** —— 徽标显示本任务到过第几档（T1/T2/T3）。
3. **归因** —— 哪些工具的结果被截断最多（`cut:` 榜，按次数降序）。
4. **趋势** —— ⛁ 上下文占用的最近 8 个采样点（sparkline），压缩前后的" sawtooth" 形状是唯一的可视化手段。
5. **零口径分叉** —— 与 S1 同构：数字只在 `compressSnapshot()` 里算一次，三个表面（侧栏 / 底部 / 底部折叠）从同一 snapshot 排版。
6. **/resume 后不为空** —— 从 rollout 的 `compacted` 记录重建（§8）。

### 1.2 非目标（明确不做）

- **不做 per-agent 归因**。事件带 `From`，但本块是任务级视图；S1 的 agent 表已经有"各 agent"视角。留到后续。
- **不做 tokens/s**：`duration_seconds` 在 `llm_call_end`（`executor.go:581-590`），与压缩事件不同源。
- **不改压缩策略、不改事件 payload 键名、不改 `protocol/agent-events.yaml`**。三档的阈值口径仍由 `internal/compression/threshold.go:18-26` 决定，本块只展示结果。
- **不显示 `⌐ kept_plans` / `summarized_by_llm` 等结构细节**。它们进 Detail（`ctrl+l` 全屏）足够，面板只放"多少、值不值"。
- **不新增 i18n 键**。区块内所有标签（`COMPRESS`/`rule`/`emergency`/`ultimate`/`cut`/`ctx`）与既有 `AGENT`/`RUN`/`TOTAL`/`Activity` 一样保持英文常量（见 `docs/token-dashboard-design.md` §11 末尾的处理方式）。

---

## 2. 视觉规范

### 2.1 侧栏位置与完整效果（面板 50 / inner 46）

区块在 **Activity 段之下**，是面板的最后一段（边框收敛逻辑 `tui_dashboard.go:175-213` 原样复用）：

```
│ ◆ Tokens                           alt+d 收缩  │  ← buildTokenPanel（S1，不动）
│ ────────────────────────────────────────────── │
│ Σ 2.41m                   in 2.35m · out 58.2k │
│ ⊕ ▰▰▰▰▰▰▰▱▱▱ 71%           hit 1.67m · new 800 │
│ ⛁ ▰▰▰▰▰▰▱▱▱▱ 64%               128.4k / 200.0k │
│ ────────────────────────────────────────────── │
│   AGENT         RUN ▸   TOTAL          ⊕ CACHE │
│ ● director    12.4k ▸   1.82m             ⊕74% │
│ ────────────────────────────────────────────── │  ← Activity 段（renderDashboard 现有逻辑）
│ ⚙ Activity                                     │
│ ● read_file                                    │
│  ctrl+l 详情                                   │
│ ────────────────────────────────────────────── │  ← ⧉ COMPRESS 段（本次新增）
│ ⧉ COMPRESS −1.42m (38%)                     T2 │
│   T1 rule         −180.2k  ×2 ▰▰▱▱▱▱▱▱▱▱▱▱▱▱▱▱ │
│   T2 emergency     −1.24m  ×1 ▰▰▰▰▰▰▰▰▰▰▰▰▰▰▱▱ │
│   T3 ultimate           0  ×0 ▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱ │
│   ⛁ ctx ▁▂▄▇█▃▃▄               128.4k / 200.0k │
│   cut: read_file×7 search_by_regex×2           │
```

### 2.2 golden 字符串（已用 `lipgloss.Width` 逐行实测，全部 46 列）

```go
// 分隔线：tokenSep(inner) 复用，不新造
strings.Repeat("─", 46)                                            // 46
padRight("⧉ COMPRESS −1.42m (38%)", 44) + "T2"                       // 46  标题行（左段 24，右对齐徽标 2）
"  " + padRight("T1 rule", 13) + " " + padLeft("−180.2k", 9) +
     " " + padLeft("×2", 3) + " " + fillBar(0.13, 16)              // 46  档位行
"  " + padRight("T2 emergency", 13) + " " + padLeft("−1.24m", 9) +
     " " + padLeft("×1", 3) + " " + fillBar(0.87, 16)              // 46
"  " + padRight("T3 ultimate", 13) + " " + padLeft("0", 9) +
     " " + padLeft("×0", 3) + " " + fillBar(0.0, 16)               // 46
padRight("  ⛁ ctx ▁▂▄▇█▃▃▄", 31) + "128.4k / 200.0k"               // 46  采样行
"  cut: read_file×7 search_by_regex×2"                            // 36（不拉伸）
```

**列网格（档位行，inner=46 时）**：

> **落地修正 1（golden 字面量差 1 列）**：上面那组字符串里标题行是 45 列、三条档位行是 44 列，与本节"全部 46 列"的承诺以及下面的列网格表自相矛盾。实现按**列网格表**为准（`padRight(left, inner-badge)`、档位行左侧固定 30 列 + 条形吃余量），落地后的实测值就是每行 46（`cut:` 行 36）—— 逐行断言见 `TestCompressPanel_Golden`。

> **落地修正 2（`cut:` 的计数粒度）**：§3.2 说"同一工具一次事件里只算 1 次"，其前提（"一个事件的多条 truncated_tools 条目本身就是多次截断的汇总"）与代码不符 —— `compression/truncate_results.go` 是在**逐条截断的循环内** `append`，每条被截断的 tool 消息一项，同一工具一次事件可以出现多次；而本节自己的 golden（T1 `×2` 却 `read_file×7`）也只有按条目计数才成立。实现按**每条截断计 1 次**、`omitted` 逐条累加。

> **落地修正 3（类型形状）**：`truncated_tools` 到 TUI 手里的具体类型是 `[]map[string]interface{}`（两个发布方都是 `make([]map[string]interface{}, …)`，而消息总线在 TUI 这条路上**不做 JSON 往返**），不是 `[]interface{}`。只断言后者会让 `cut:` 榜与 ctrl+l 的截断明细在真机上永远为空 —— 已由 `compressionToolEntries` 同时吃两种形状，并用 `TestCompressPanel_RealPublisherShapeEndToEnd` 按发布方的构造方式钉住。

> **落地修正 4（`pct` 封顶）**：§3.3 的公式没有封顶，但压缩是**每个 step 独立触发**的（`executor.go` 的压缩检查在 `for i := 0; i < MaxSteps` 循环内），多轮任务的 `Σsaved` 会合法地超过任何单次 `original_tokens`（5 次 ×900/1000 → 450%）。面板回答的是"现在离溢出还有多远"，所以**显示层封顶 100%**，`Σsaved` 的绝对值仍按公式不封顶。§3.3 的反例（60% 而不是 33%）不受影响，`TestCompressPanel_PercentUsesPeakOriginal` 照旧钉住。

| 段 | 宽 | 对齐 | 内容 |
|----|----|------|------|
| indent | 2 | | 固定两空格，表示"从属于标题行" |
| label | 13 | 左 | `T1 rule` / `T2 emergency` / `T3 ultimate` |
| gap | 1 | | |
| saved | 9 | **右** | `savedStr(x)`：`saved>0` 时 `"−" + formatToken(saved)`，`saved==0` 时裸 `0`（见 §3.2）。负号用 U+2212 `−`（实测 1 列），不用 ASCII `-` |
| gap | 1 | | |
| count | 3 | **右** | `×N`（`×` U+00D7，A 类 1 列） |
| gap | 1 | | |
| bar | `inner − 30` | 左 | 该档 saved 占 saved 总量的比例 |

`inner != 46` 时：**indent/label/gap/saved/gap/count/gap 固定 30 列，bar 吃掉余量**；`inner < 38`（bar 不足 8 格）时**去 bar**，档位行收成 `  T1 rule  −180.2k ×2`。

### 2.3 标题行语义

```
⧉ COMPRESS <−总节省> (<pct>)   <右对齐：到过第几档>
```

- `pct` = `总节省 / 单次最大 original`，见 §3.3 —— **不是** `Σsaved / Σoriginal`。
- 徽标 `T1`/`T2`/`T3` = 本任务**到达过的最高档**（不是"最后一次触发的档"）。它回答"这个任务有多危险"，一旦到过 T3 就保持 T3。
- 色：`⧉` 与徽标 = `141`（`dashAccent`）；`−1.42m` = `114`（`dashOutFg`，节省是好事）；`(38%)` 与 `COMPRESS` = `244`（`dashLabelFg`）。
- 档位行的 saved 一律 `244`，**只有触发过的档位行**用 `250` bold 的 label；未触发档位（`×0`）整行 `240` 暗色、bar 全 `▱`。
- `T2 emergency` 行的 label 色 = `214`，`T3 ultimate` = `203`（沿用 `contextBarColor` 已有的两档告警色，不引新色号）。

### 2.4 采样行（`⛁ ctx`）

- 8 个采样点，字形 `▁▂▃▄▅▆▇█`（已实测均 1 列）。归一化到**采样窗口内**的 min/max，不是除以 `contextWindow` —— 窗口内自归一才能看出形状；绝对值由右段的 `128.4k / 200.0k` 给出。
- 右段与 token 段的 ⛁ 计量条**同一条口径**（`snap.ctxTokens` / `snap.ctxWindow`），`ctxWindow<=0` 时只写 `128.4k`。
- 采样点 < 2 时**整行不渲染**（单点画不出趋势）。
- 色：sparkline 用 `contextBarColor(pct)` 的同一渐变（`79`/`214`/`203`）。

### 2.5 截断榜（`cut:`）

- 最多 2 项，按**次数**降序，同次数按 omitted tokens 降序，再同按名字；溢出补 ` ┈ +N`（`tokenMoreStyle`，`243` italic）。
- `cut: ` 标签 `244`，工具名 `250`，`×N` `244`。
- 只列 `truncated_tools[]` 里出现过的工具。注意事实：**`deepthinking` / `design` / `load_tool` 是 `TruncProtected`，永不进榜**（`internal/toolspec/effect.go:150-155`、`internal/compression/truncate_results.go:40`、`internal/tools/specs_cog.go:56-75`）。测试里不要拿它们当被截断的例子。
- `inner < 30` 时整行不渲染。

### 2.6 三个表面的形态

| 表面 | 形态 | 行数 |
|------|------|------|
| 侧栏 dashboard | §2.1 全块（按 §4.3 高度阶梯收缩）；没压缩过时退到"分隔线 + 标题 `⧉ COMPRESS 0 (0%) T0`" | 2（空闲）/ 3–7 |
| 底部面板 | **1 行摘要**，接在 ⛁ 计量条之后：`  ⧉ −1.42m (38%) T2  ·  cut read_file×7`（空闲态 `  ⧉ 0 (0%) T0`） | +1 |
| 底部折叠（`alt+t`） | **不显示** | 0 |

底部只给 1 行的理由：footer 高度是 `computeFooterHeight` 与 `View()` 逐行自洽的（AGENTS.md §11 坑 7），多一个段就多一处错配风险；而底部面板出现的条件是 `termWidth < 120`，本来就窄。

`cut` 段在无截断记录时省略整行（不是省略 `· cut` 之后的部分）。

---

## 3. 数字口径

### 3.1 事件 → 统计的唯一映射表

| 档 | 事件 | 发布点 | 读取键 |
|----|------|--------|--------|
| T1 rule | `context_compressed` | `executor.go:432-439`、`director/planner_run.go:247` | `original_tokens`、`compressed_tokens`、`saved_tokens`、`truncated_count`、`truncated_tools[]{tool_name, omitted_tokens}` |
| T2 emergency | `context_emergency_compressed` | `executor.go:467-475`、`planner_run.go:272` | `original_tokens`、`compressed_tokens`、`saved_tokens`（**无 `saved_percent`**） |
| T3 ultimate | `context_ultimate_compressed` | `executor.go:502-510`、`planner_run.go:300` | 同上 + `total_plans`/`kept_plans`（本块不展示） |

写入时机：**每收到一个事件就 `count++`**（一次压缩 = 一行统计，不做去重）。三档的 `saved` 取事件自己的 `saved_tokens`，不要自己算 `original-compressed`（T1 的 `saved_tokens` 只算被截断的工具结果之和，与 `original-compressed` 在边界上不严格相等；以上游为准）。

### 3.2 聚合定义

| 显示 | 公式 |
|------|------|
| `−<总节省>` | `"−" + formatToken(Σ tiers[i].saved)`；**`saved==0` 时不写负号**（golden 里 T3 是 `0` 而不是 `−0`） |
| 档位行 bar 比例 | `tiers[i].saved / Σ saved`（同档多次合并成一条） |
| `⛁ ctx` 右段 | `snap.ctxTokens` / `snap.ctxWindow`（与 S1 同一份 snapshot 口径） |
| `cut:` 计数 | `Σ` 每个事件里 `truncated_tools[]` 的**条目出现次数**；同一工具一次事件里只算 1 次（一个事件的多条 truncated_tools 条目本身就是多次截断的汇总，见 `truncate_results.go` 的 stats 组装） |

### 3.3 `pct` 的定义（**这条必须照抄，不要"顺手修正"**）

三档是**串行嵌套**的：T2 的 `original_tokens` 是 T1 截断**之后**的规模，T3 的 original 又是 T2 之后的。所以

```
Σ original  = 1000 + 800 + 600   ← 重复计算同一批 token，分母虚高
```

定义：**`pct = Σ saved / max(original_tokens)`**，分母是单次事件里最大的 `original_tokens` ≈ 压缩前的上下文峰值。

> 反例（测试会钉住）：T1 `1000→800`（saved 200）、T2 `800→400`（saved 400）。
> 正确 `pct = 600/1000 = 60%`；错误 `Σsaved/Σoriginal = 600/1800 = 33%`。

### 3.4 与其他口径的关系

- `⛁` 的 ctx 语义（"最近一次调用的输入规模，**覆写不累加**"）由 S1 定义，闸门是 `TestContextTokens_OverwriteNotAccumulate`（`tui_token_test.go`）。本次**只加一个采样点**，不改语义。
- `saved_tokens` 与 Σ 行没有任何重叠：**不要**把节省量从 `totalInputTokens` 里减掉，也别显示"净值"。压缩不减少真实付费量，只减少后续调用的输入规模。
- 估算路径（provider 无 usage 时 `executor.go:618-627` 的 `estimated:true`）与本块无关：压缩统计来自 `internal/tokenutil` 的真实计数，不受 provider 影响。

---

## 4. 渲染架构

### 4.1 API（新建 `internal/tui/compress_panel.go`）

```go
// tierStat 是单档压缩的累计效果。
type tierStat struct {
    count    int
    saved    int64
    original int64 // 该档历次事件里最大的 original_tokens（pct 分母的候选）
}

// compressSnapshot 是压缩块的唯一输入；数字在这里算完，排版层不碰 model。
type compressSnapshot struct {
    tiers    [3]tierLog // idx 0=T1,1=T2,2=T3；tierLog{count, saved, barRatio}
    maxTier  int        // 到过第几档（0=从没压缩）→ 徽标 T1/T2/T3
    saved    int64      // Σ saved
    pct      float64    // saved / maxOriginal
    samples  []int      // 最近 ≤8 个 ctx 采样点（已归一化为 0..7 的档位）
    ctxNow   int64
    ctxWin   int
    cuts     []cutStat // 已排序、已截断到 2 项
    cutMore  int
}

type tierLog struct{ count int; saved int64; ratio float64 }
type cutStat struct{ tool string; count int; omitted int64 }

func (m *model) compressSnapshot() compressSnapshot
func (s compressSnapshot) any() bool                 // 无压缩且无采样 → 整块不渲染
func (s compressSnapshot) signature() string         // 进侧栏 cache key
func buildCompressBlock(s compressSnapshot, spec compressPanelSpec) []string
func compressSummaryRow(s compressSnapshot) string   // 底部面板那 1 行

type compressPanelSpec struct {
    innerWidth int
    maxRows    int // 0 = 不限高
}
```

**不要**把压缩排版塞进 `buildTokenPanel`：侧栏要求它出现在 **Activity 之后**，而 `buildTokenPanel` 的输出必须在 Activity 之前 —— 一个函数无法同时满足两个位置约束。两者共享 `tokenSep`/`fillBar`/`padLeft`/`padRight`/`truncateCells`/`formatToken` 这些**已存在**的原语，这是本次唯一的"统一"手段。

### 4.2 位置与行预算（`tui_dashboard.go`）

三段抢同一个 `height`，优先级 **token > Activity > COMPRESS**（用户明确要求压缩块在 Activity 之下）：

```go
const timelineReserveRows = 4  // 已存在，:82，不改
const compressReserveRows = 2  // 新增：分隔线 + 摘要行（阶梯最低可见形态）

// ① token 段预算（现在是 height - timelineReserveRows，下限 3）
maxTokenRows := height
if len(m.timelineEntries) > 0 {
    maxTokenRows = height - timelineReserveRows
}
if m.compress.any() {          // 只有真有压缩时才占用预算
    maxTokenRows -= compressReserveRows
}
if maxTokenRows < 3 { maxTokenRows = 3 }

// ② Activity 段：tlAvailable := height - len(lines) - compressBudget(maxRows)
// ③ COMPRESS 段：remaining := height - len(lines)，buildCompressBlock 自己收缩
```

`compressBudget(maxRows)` 是个纯函数，返回"在给定剩余高度里压缩块实际会占几行"，供 ② 用。**不要**让 ② 直接减常量：Activity 少留一行就会把 `⚙ Activity` 标题挤掉（这正是 S1 把 `timelineReserveRows` 从 1 提到 4 的原因，见 `tui_dashboard.go:80-82` 的注释）。

**空闲态常驻**（2026-10-14 落地时与用户确认的改动，覆盖本节原来的"零成本"）：没触发过压缩时区块不消失，而是**直接从 `compressTierSummary` 起步** —— 只占分隔线 + 标题两行，徽标 `T0`、数字 `0 (0%)`。理由是"这段存在但还没数据"必须可见，否则功能在不在只能靠真触发一次压缩来确认。因此 `maxTokenRows` 的 `-compressReserveRows` 是**无条件**的；只有面板高度实在放不下 2 行时才整段让位（宁可不画，也不留孤零零一行标题）。测试：`TestCompressPanel_IdleStateIsTwoRows`、`TestDashboard_IdleBlockCostsExactlyTwoRows`。

**行预算按内容高度分配**：`lipgloss.Style.Height(h)` 是**含上下边框**的总高（实测 `Height(5)` 装 3 行内容 → 输出 5 行），所以三段抢的是 `contentRows = height - 2`，不是 `height`。按 `height` 分配会多要 2 行，被末尾的"行数收敛 + 补边框"逻辑悄悄截掉 —— 表现是最后一段莫名少 2 行（原来是 Activity 的尾部，现在是 ⧉ 段）。扫描结果：`height<=7` 时只放得下 token 段，`8–11` 放下 Activity 但 ⧉ 让位，`>=12` 起 ⧉ 从 2 行的空闲/最低形态逐级长满。

### 4.3 高度阶梯（与 S1 同构：从富到简逐档试，每档只砍一整行）

```go
const (
    compressTierFull       = 0 // 分隔+标题+T1+T2+T3+采样+榜 = 7 行
    compressTierNoIdle     = 1 // 砍掉"高于已触发档"的空档行
    compressTierNoSample   = 2 // 砍 ⛁ 采样行（与 token 段的 ⛁ 计量条重复度最高，先砍）
    compressTierNoCut      = 3 // 砍 cut: 榜
    compressTierNoSep      = 4 // 砍分隔线
    compressTierSummary    = 5 // 只留 分隔线 + 标题行 = 2 行
    compressTierLast       = compressTierSummary
)
```

- `compressTierNoIdle` 之后典型行数：到过 T2 ⇒ 6；到过 T1 ⇒ 5。
- **硬规则**（仿 `tokenAgentBlock`）：装不下"标题行 + 至少一行内容"时整块让位（返回 nil），绝不画"只有分隔线和标题"的空壳。
- 永不被砍：标题行（`⧉ COMPRESS …`）。徽标 `T<n>` 与 `pct` 在同一行内，不单独裁剪。

### 4.4 宽度阶梯（底部面板，inner = `termWidth-6`）

| inner | 摘要行 |
|-------|--------|
| ≥ 40 | `  ⧉ −1.42m (38%) T2  ·  cut read_file×7` |
| 30–39 | `  ⧉ −1.42m (38%) T2` |
| < 30 | `  ⧉ −1.42m T2`（去 pct） |
| 无压缩 | 空字符串（不加行） |

侧栏 inner 恒为 46（`dashboardPanelWidth = 50`），不存在侧栏宽度降级。

---

## 5. 状态模型

### 5.1 新状态（`internal/tui/tui_model.go`，紧邻 `runTokensPerAgent` `:466`）

```go
// compress 是本任务历次上下文压缩的累计效果，dashboard 的 ⧉ 段依赖它。
compress compressStats
```

```go
// internal/tui/compress_panel.go
type compressStats struct {
    tiers       [3]tierStat
    cutByTool   map[string]*cutStat // 懒初始化
    ctxSamples  []int64             // 追加序，保留最近 8 个（渲染时归一化）
    peakCtx     int64               // 采样窗口外的历史峰值，供 pct 分母回落
    maxOriginal int64               // 历次事件中最大的 original_tokens
}
```

生命周期与 `totalInputTokens`（Σ）**一致**，是"会话累计"，不是"本轮"：tab 切换要存取、`/resume` 要重建、新建 tab 才清零。**与 RUN 无关**，`resetRunTokens`（`token_panel.go:268`）不得清它。

### 5.2 读写点（逐处）

| 位置 | 改动 |
|------|------|
| `tui_update_tasks.go:681-689`（三档共享分支，**当前不 return**，安全） | 在覆写 ctx 之后调 `m.recordCompression(msg.event.Type, contentMap)`；`original_tokens`/`saved_tokens`/`truncated_*` 全在这里读 |
| `tui_update_tasks.go:181`（`ai_response` 写 `rt.ContextTokens`） | 改调 `m.setContextTokens(agentName, totalInputVal)` |
| `token_panel.go:276` `updateAgentContextTokens` | 内部改走 `m.setContextTokens`，签名不变（调用者 `:686` 不动） |
| `tui_model.go:~860`（`make(...)` 初始化区） | `compress.cutByTool = make(map[string]*cutStat)` 走懒初始化即可，别在这里塞 |
| `tui_tabs.go` —— `sessionTab` 结构体（`:20`，token 字段在 `:65-71`）、save（`:191-205`）、restore（`:284-293`）、reset（`:362-368`）**四处**同步搬 `compress`。`cutByTool` 是 map，save/restore 必须逐 key 复制值（照抄 `:202-205` / `:290-293` 处理 `runTokensPerAgent` 的写法），**不要**让两个 tab 共享同一张 map |
| `tui_resume.go:936-947` `resetTokenState` | 清零 `m.compress = compressStats{}` |
| `tui_resume.go:949-985` `applyRolloutRunState` | 用 §8 的压缩记录重建 tiers/maxOriginal/徽标（`cutByTool` 与 `ctxSamples` 无法恢复，见 §8.4） |

**采样收敛**：`setContextTokens(agent, v)` 是 ctx 的**唯一**写入者（两处调用点），它做三件事 —— 覆写 `rt.ContextTokens`、`if len(samples)==0 || samples[last]!=v { append }`（连续同值不记录，否则一个平台期会吃光 8 格窗口）、`len(samples)>8` 时丢最旧。**不要**在 `tokenSnapshot()`（读路径）里采样：它在每次渲染都被调用。

### 5.3 `recordCompression` 的实现约束

- 事件类型 → 档位下标用一张**局部 map 或 switch**（`context_compressed`→0、`context_emergency_compressed`→1、`context_ultimate_compressed`→2）。三档事件名在 `executor.go` 与 `planner_run.go` 各写了两遍字面量、且**不在** `protocol/agent-events.yaml` SSOT 里 —— 这是既有的不一致，**本次不动 SSOT**，但在 `compress_panel.go` 顶部注释里写明"这三串是 TUI 侧唯一的字面量副本，新增档位要同步改 `recordCompression` 与 `tierLabel`"。
- 数值解析一律用现成的 `parseEventInt`（`tui_update_tasks.go:22`），它已覆盖 `int/int64/float64/json.Number`。**不要**自己再写类型 switch。
- `truncated_tools` 的运行时类型是 `[]interface{}`，元素是 `map[string]interface{}`（TUI 现有解析在 `tui_update_tasks.go:704-716`，照抄结构）。
- 每次写入后 `m.invalidateFooterCache()`（`tui_render.go:96-104` 已会清 `dashboardCache`/`dashboardCacheKey`/`cachedTokenDashboard`，不要另开一条失效路径）。

---

## 6. 缓存与失效

侧栏 cache key 现在（`tui_dashboard.go:123-125`）是：

```go
key := fmt.Sprintf("%d|%s|%d|%d|%s|ctx:%d/%d",
    len(m.timelineEntries), m.timelineLastID(), width, height,
    snap.signature(), snap.ctxTokens, snap.ctxWindow)
```

**必须**追加压缩签名，否则压缩块永远命中旧缓存、事件到了也不更新（这类"编译通过但静默不刷新"是本模块最容易犯的错）：

```go
key += "|cmp:" + cs.signature()
```

`cs.signature()` = 三档各自的 `count:saved` + `maxTier` + `maxOriginal` + cut 榜的 `tool:count` 串 + 采样点拼接。顺序稳定（档位固定、榜已排序）。

`hasRunning` 跳过缓存以支持动画的行为（`tui_dashboard.go:91-98`、`:127`）保留；底部面板继续走 `tokenDashboardBlock` 的懒渲染（`tui_render.go:74-86`）。

**失效点清单**（改状态必须调 `invalidateFooterCache()`）：`recordCompression`、`setContextTokens`（采样变了 ⇒ 图形变了）、`applyRolloutRunState`、tab restore。

---

## 7. footer 高度不变量

- `computeFooterHeight`（`tui_render.go:17-55`）与 `View()` 的 block 拼接（`tui_view.go:208-245`）逐行一一对应；token 段在两处的位置是 `tui_render.go:45` ↔ `tui_view.go:229`，都经 `tokenDashboardBlock()` → `renderFooterTokenPanel()`（`token_panel.go:699-723`）。
- 摘要行**由 `renderFooterTokenPanel` 产出**（在 `buildTokenPanel` 的 rows 之后 append 一行，再统一上边框），因此 `lipgloss.Height` 自动跟着变，`computeFooterHeight` 不需要加新分支 —— 这是选择"只给 1 行"的第二个理由。
- **不要**给摘要行前置空行或独立边框：它属于同一个面板。
- 折叠态（`spec.collapsed == true`）不 append。
- 因此 `TestView_ExactHeight`（`tui_chrome_test.go:274`）与 `TestView_ExactHeightWithPanels`（`:537`）的期望行数要 +1（有压缩数据的 fixture 才加；无压缩 fixture 不变）。

事件侧（AGENTS.md §11 坑 6）：`handleTaskEventMsg` 是 if-chain（`tui_update_tasks.go:92`），**每个 return 都要 re-arm** `listenForEvents`。本次在共享分支加写入、不新增 return 路径，风险为零；但若为了让 `ctrl+l` 详情看到压缩明细而新增叠加态，必须显式放行消息类型。

---

## 8. /resume：从 rollout 重建

现状（实测）：`memory.CompactedPayload` 只有 `Summary` / `TokensBefore` / `TokensAfter`（`internal/memory/rollout_types.go:329-333`）；写入点 4 处（`executor.go:448`、`:485`、`:523`，`director/planner_run.go:176`）；读取端 `rollout_reader.go:355-362` → `handleCompacted`（`:555`）**只在 `opts.Compaction == CompactionSummaryTail` 时消费，且只产出 messages**，历史压缩记录不对外暴露。⇒ 恢复后区块必然为空。

### 8.1 rollout 格式增量（向后兼容）

```go
// internal/memory/rollout_types.go
type CompactedPayload struct {
    Summary      string `json:"summary,omitempty"`
    TokensBefore int    `json:"tokens_before,omitempty"`
    TokensAfter  int    `json:"tokens_after,omitempty"`
    Kind         string `json:"kind,omitempty"`         // "rule" | "emergency" | "ultimate"
    SavedTokens  int    `json:"saved_tokens,omitempty"` // 与事件的 saved_tokens 同源
}
```

4 个写入点各补两个字段（`Kind` 与该处档位一一对应，`SavedTokens` 用 `compStats.SavedTokens` / `emergencyStats.SavedTokens` / `ultStats.SavedTokens`）。**不改** `Summary` 文案（历史恢复与人工排查都依赖它）。

### 8.2 reader 暴露压缩记录

```go
type CompactionRecord struct {
    Kind          string
    Saved         int64
    TokensBefore  int64
    TokensAfter   int64
}
// RolloutInfo.Compressions []CompactionRecord —— 与 Compaction 选项无关，始终收集
```

`handleCompacted` 现在在 `Compaction != CompactionSummaryTail` 时**提前 return**（`rollout_reader.go:556-558`）：收集 `CompactionRecord` 的逻辑必须放在那个 return **之前**，否则 fixed/rebuild 模式恢复的任务又拿不到统计 —— 这正是本条要修的 bug 的翻版。

### 8.3 老 rollout 的回落推导

缺 `Kind` 时从 `Summary` 前缀判定（`"tool-result truncation"`→rule、`"emergency compression"`→emergency、含 `"ultimate"`→ultimate）；缺 `SavedTokens` 时用 `TokensBefore - TokensAfter`（对 T1 会偏大 —— 可接受，注释写明）。推导不出来就整条跳过，**不要**猜成 rule。

### 8.4 恢复不了的部分（明确承认）

- `truncated_tools[]` 没落盘 ⇒ `cut:` 榜在 /resume 后为空（区块继续显示，只是少一行）。
- ctx 采样序列没落盘 ⇒ 采样行不渲染（`⛁` 计量条仍然由 `RolloutTokenTotals.LastContextTokens` 正确显示，`tui_resume.go:969`）。
- **不要**为了这两条把 `truncated_tools` 塞进 rollout —— 那是把面板需求写进持久化格式，收益/成本比不合理。若将来要做，另开设计稿。

---

## 9. 实施步骤（有序，每步都能独立 build + test）

1. **口径层**：新建 `compress_panel.go`，只写数据结构 + `recordCompression` + `setContextTokens` + `compressSnapshot()`/`signature()`/`any()`。不碰渲染。跑 `go build` + `go test ./internal/tui -run Compress`（此时只有 §10 的口径测试）。
2. **接入事件**：改 `tui_update_tasks.go:681-689` 与 `:181`、`token_panel.go:276`；补 tab 四处存取与 `resetTokenState`。
3. **渲染层**：`buildCompressBlock`（列网格 + 高度阶梯 + 宽度阶梯）+ `compressSummaryRow`，接进 `tui_dashboard.go`（Activity 之后）与 `renderFooterTokenPanel`。跑 `tui_dashboard_border_test.go` + `tui_chrome_test.go`。
4. **缓存**：cache key 加 `cmp:` 签名，补 `TestDashboardCacheKey_IncludesCompressStats`。
5. **/resume**：§8.1→8.3，跑 `go test ./internal/memory/... -count=1` 与 `tui_resume_restore_test.go`。
6. **测试补齐 + 文档**：§10 全部用例；`AGENTS.md` §6.8 加一句"⧉ 压缩段见 `docs/token-compress-panel-design.md`"（一行，别重写那一节）。
7. **人工核对**：真终端跑 `YAGENT_COMPRESS_THRESHOLD=2000 ./yagent`，一次"读个大文件"的任务就能连出 T1/T2/T3（这个环境变量是落地时新加的调试覆盖：`compression.ResolveThreshold` 是**模型目录优先**，目录收录的模型动辄 1M 窗口 ⇒ 阈值 750k，原来设想的"把 `context_compression_threshold` 调小"在目录命中的模型上完全不起作用）。在 120 / 100 / 80 / 60 列宽下各看一眼；`alt+d`、`alt+t`、`ctrl+l` 各按一次；再 `/resume` 一次这个任务，确认 `⧉` 段的徽标与节省量重建出来（`cut:` 与采样行按 §8.4 应该为空）。**这是唯一能确认渐变与告警色对不对的手段。**

每步之后：

```bash
go build -o yagent . && go vet ./... && go test ./internal/tui/... -count=1
```

> **gofmt**：`internal/tui` 有存量对齐漂移（AGENTS.md §9.1），改到被 `gofmt -l` 点名的文件先 `gofmt -d` 分清"我写的"和"历史遗留"，**不要全量重排**。新建的两个文件必须干净。

---

## 10. 测试计划

### 10.1 新增 `compress_panel_test.go`

1. `TestCompressPanel_Golden` —— §2.2 的 7 行逐行 `==`（`stripANSI` 后），并断言每行 `lipgloss.Width == 46`；`cut:` 行断言 `== 36`（不拉伸）。
2. `TestCompressPanel_HeightLadder` —— `maxRows ∈ {0,2,3,4,5,6,7}`：`len(rows) <= maxRows`；标题行恒在；`maxRows==2` 时只剩 分隔+标题；`maxRows==1` 时**整块为空**（禁止只有分隔线）；到过 T1 时 `NoIdle` 档省 2 行。
3. `TestCompressPanel_PercentUsesPeakOriginal` —— 喂 T1 `1000→800`、T2 `800→400`：`pct == 60`，且断言字符串里**不出现** `33%`（钉 §3.3）。
4. `TestCompressPanel_EmergencyHasNoSavedPercent` —— 只喂 emergency 事件（无 `saved_percent` 键）也能得到正确 saved 与 pct。
5. `TestCompressPanel_CutRankingAndOverflow` —— 4 种工具、次数与 omitted tokens 交叉：取前 2、顺序正确、`┈ +2` 出现。
6. `TestCompressPanel_SamplesDedupAndCap` —— 连续 5 个相同 ctx ⇒ 采样点 1 个；超过 8 个 ⇒ 保留最近 8 个且丢最旧。
7. `TestCompressPanel_IdleStateIsTwoRows` —— 没有压缩事件（只有 ctx 采样）时：`any()==false`，区块**不消失**而是恰好 2 行（分隔线 + `⧉ COMPRESS 0 (0%) T0`，每行 46 列），没有档位/采样/榜行；`maxRows==1` 时整块为空（不留孤零零的标题）；底部摘要行给 `  ⧉ 0 (0%) T0`。侧栏配套见 `TestDashboard_IdleBlockCostsExactlyTwoRows`。
8. `TestCompressPanel_FeedsFromAllThreeEvents` —— 三个 handler 分别喂一次：三档各 `count==1`，`maxTier==3`。
9. `TestCompressPanel_RestoredFromRollout` —— §8.3 的老格式记录（无 `kind`）由 Summary 前缀正确归到 emergency；`cut:` 榜为空。

### 10.2 既有测试

| 测试 | 动作 |
|------|------|
| `TestDashboardCacheKey_IncludesContextTokens`（`tui_token_test.go`） | **保留**，再加 `TestDashboardCacheKey_IncludesCompressStats`：喂一个压缩事件，断言第二次 `renderDashboard` 的产物不同 |
| `TestTokenPanel_GoldenS1` / `_HeightLadder` / `_WidthLadder` / `_FooterAndSidebarAgree` | 不改；若失败说明 token 段被侵入（本任务的红线） |
| `TestDashboard_ActivityNeverStarvedByCompress` | **新增**：`height ∈ {7..24}` 下，只要有 timeline 条目，`⚙ Activity` 标题行必须在输出里（钉 §4.2 的预算分配） |
| `TestView_ExactHeight` / `TestView_ExactHeightWithPanels`（`tui_chrome_test.go:274 / :537`） | 期望行数 +1（含压缩数据的 fixture） |
| `TestDashboardBorder_*`（`tui_dashboard_border_test.go:21/:102/:159/:214`） | fixture 补压缩事件后仍要过"每行恰好 width、`╭╮/╰╯` 完整" |
| `tui_resume_restore_test.go` | 新增 /resume 后 `⧉` 徽标与 saved 正确的断言（§8） |
| `TestContextTokens_*`、`TestTokenCounting_*` | 语义不变，必须保持绿；`setContextTokens` 收敛后 `TestContextTokens_OverwriteNotAccumulate` 仍应原样通过 |

### 10.3 矩阵与 fixture（沿用既有约定）

- 宽度矩阵 `{30,40,60,80,100,120,160,200,240}`；终端尺寸矩阵 `{24,20}…{200,60}`。
- fixture：`newTestModel()`（`tui_last_message_markdown_test.go:17`）、`feedEvent`（`:59`）、`buildEvent`（`:65`）、`chromeTestModel(t,w,h)`（`tui_chrome_test.go:22`）；压缩事件用 `buildEvent("context_compressed", map[string]interface{}{...}, "director")`，**别自造第 4 份 ANSI stripping**。
- 断言**精确单元格宽度**，除 golden 逐行比对外不用子串匹配。

---

## 11. 验收标准

- [ ] 侧栏 inner=46 时，压缩块逐行等于 §2.2 golden；每行 `lipgloss.Width == 46`（`cut:` 行 36）。**档位行以 §2.2 的列网格表为准**（左侧固定 30 列 + 条形吃余量），原文那三条字面量各少 1 列，见 §2.2 落地修正 1。
- [ ] 块出现在 `⚙ Activity` 段**之后**、面板底边框之前（`TestDashboardCompressBlock_BelowActivity` 用行索引断言）。
- [ ] `pct` 用 `Σsaved / max(original_tokens)` **并封顶 100%**，嵌套 original 的双算与多轮任务的 >100% 都被测试钉死（`TestCompressPanel_PercentUsesPeakOriginal` / `TestCompressPanel_PercentCapsAtHundred`）。
- [ ] 三档各自的 次数 / 节省 / bar 比例正确；`NoIdle` 档砍掉未触发的档位行；徽标显示到过过的**最高**档；`T0` = 还没压缩过。
- [ ] `cut:` 榜按**每条被截断的 tool 消息**计 1 次（§2.2 落地修正 2），且 `truncated_tools` 的两种具体形状（`[]map[string]interface{}` 与 `[]interface{}`）都要能解析（`TestCompressPanel_RealPublisherShapeEndToEnd`）；不含 `TruncProtected` 工具（`deepthinking`/`design`/`load_tool`，上游永不送进来）。
- [ ] 无压缩事件时：区块退化为**常驻的 2 行空闲形态**（`⧉ COMPRESS 0 (0%) T0`），不画档位/采样/榜行、不留孤零零的标题；底部面板给 1 行 `  ⧉ 0 (0%) T0`；折叠态不给行。
- [ ] 三段行预算按 `contentRows = height - 2` 分配（`lipgloss.Height` 含边框），面板在 `height ∈ [8..24]` 下 `⚙ Activity` 标题恒在、没有任何一行是被末尾截断丢掉的。
- [ ] 底部面板只多 1 行摘要；折叠态（`alt+t`）不加行；`View()` 行数仍等于终端高度。
- [ ] `/resume` 后（含老 rollout）三档统计与徽标重建；`cut:`/采样行为空但不报错、不留空壳行。
- [ ] 压缩事件到达后面板**确实刷新**（cache key 含 `cmp:` 签名的两个测试通过）。
- [ ] `go vet ./...` 干净；`go test ./internal/tui/... ./internal/memory/... -count=1` 全绿；`internal/agents` 只跑了改动相关的 `-run`；新增文件 `gofmt -l` 为零。
- [ ] 未新增 i18n 键；未改 `protocol/agent-events.yaml`；未改事件 payload 键名；未改 `buildTokenPanel` 输出。
- [ ] 调试覆盖 `YAGENT_COMPRESS_THRESHOLD`：非法值必须原样回落（绝不能把阈值变成 0 让每步都压缩），且不缓存取值（`internal/compression/threshold_test.go` 三条用例）。

---

## 附录 A：宽度自检口径

本环境无法跑真终端，所有布局断言必须在 `go test` 里完成。手工核对视觉稿时的等价口径：

```python
# CJK(W/F)=2，其余（含 EAW 的 A 与 N 类）=1 —— 与 go-runewidth 默认一致
import unicodedata as u
width = lambda s: sum(2 if u.east_asian_width(c) in ("W","F") else 1 for c in s)
```

Go 侧一律 `lipgloss.Width`（本仓库唯一真值）。本文用到的字形已逐个实测：

| 字形 | 码点 | `lipgloss.Width` | 结论 |
|------|------|------------------|------|
| `⧉` | U+29C9 | 1 | 安全（N 类） |
| `▁▂▃▄▅▆▇█` | U+2581–2588 | 1 | 安全（N 类），sparkline 用 |
| `▰▱ ─ × − ⌐` | U+25B0/25B1/2500/00D7/2212/2310 | 1 | 安全（A 类，与全仓边框同级） |
| `⚡` | U+26A1 | **2** | **禁用**（lipgloss 与终端都给 2） |
| `☰` | U+2630 | **2** | **禁用**（本仓库唯一的实测分歧点：python 按 EAW 算 1，lipgloss 算 2 —— 以 lipgloss 为准） |
| `⏱` | U+23F1 | 1 | **禁用**：`Emoji_Presentation=Yes`，多数终端画 2 列，与 lipgloss 不一致 —— 这类"度量说 1、终端说 2"的字形最危险 |
| `▶` | U+25B6 | 1 | 不用：S1 已选定 `▸`（U+25B8，N 类）作"当前"记号，同一含义只用一个字形 |
| `🗜` U+1F5DC / 任何 emoji | — | — | **禁用** |

`⧉`（两个重叠方框）与既有 `◆ ● ⊕ ⛁ ▸ ⚙ ┊ ┈` 同族；**不用** `🗜`（emoji，宽度不可控）。
