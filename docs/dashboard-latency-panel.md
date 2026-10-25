# 侧栏 dashboard 新增 ⧗ LATENCY 段 —— 近期事件耗时分布

> 状态：**已实施**（分支 `feat-dashboard-latency`，2026-10-25）。§13 记录实现相对本稿的六处偏离。
> 目标：在右侧 dashboard 上加一段，**一眼看出近期的高耗时事件**，并同时给出
> 各轮 agent 运行的耗时与本任务到当前的整体耗时。
>
> 本文所有 `file:行` 都逐条核对过。行号会漂 —— 动手前先 `grep` 那个**函数名**而不是那个行号。

---

## 0. 需求翻成五条可渲染的判据

| 用户要的话 | 屏幕上的承载物 | 为什么是它 |
|---|---|---|
| "近期" | 最近 **20 条已测得时长的事件**，行尾印 `n=20` | 分母必须可见，否则读者以为窗口小；20 这个数在 `internal/tui/render.go:843` 已经写了但从未生效（`expanded` 分支是死枝，见 §11.1），把它接成唯一的"近期"定义 |
| "耗时分布" | 一行 sparkline，一格一事件，**按时间序**，高度=本窗口内相对值，颜色=绝对档位 | 时间序保留"什么时候开始变慢"；排序后永远是一道楼梯，形状就没有信息了（§10 有一条测试专门守这个决定） |
| "高耗时事件" | 按耗时降序的前 2 个**具名叶子事件**（`▲` 行） | sparkline 只能说"有个尖峰"，"是哪个动作"必须有文字 |
| "各轮 agent 运行的耗时" | 按耗时降序的前 2 **轮**（`◉` 行），每行带该轮内事件数与轮内单次最慢值 | 委派是嵌套容器，和叶子事件不是一个量纲，混排会互相掩盖（§4） |
| "到目前整体耗时" | 带子右段 `total 3m41s` | 唯一一个每 tick 会变的数字，代价见 §6 |

两个通道分工：**高度是相对的**（本窗口 min/max 归一，直接复用 `ctxSampleLevels`，
`internal/tui/compress_panel.go:338`），**颜色是绝对的**（下表）。于是"整批都慢"表现为一条平顶 +
满行暖色，"偶发尖峰"表现为一个红格从一片灰里顶出来 —— 两者不混淆。

| 绝对档位 | 颜色（现有令牌，`internal/tui/tui_dashboard.go:24-35`） | 含义 |
|---|---|---|
| `< 1s` | `244` `dashLabelFg` | 不值得注意 |
| `1–3s` | `111` `dashInFg` | 有点分量 |
| `3–10s` | `214` | 慢（与 `contextBarColor` 的黄档同色同义，`internal/tui/token_panel.go:363-372`） |
| `> 10s` | `203` | 值得查（同红档） |

**冷静规则**：窗口 `max < 2s` 时全部走 `244`，不许出现暖色。防的是假警 —— 一次正常的高速会话里
400ms 的 `read_file` 不该被染成"慢"。`latencyBands` 阈值表 + `latencyCalmMax = 2s` 是本模块唯一的
两个魔数，必须各有一条"改值就有可观察后果"的测试（根 `AGENTS.md` §11 坑 25 / 35：
"配置项存在"≠"生效"、"默认值"必须有人真的用它）。

---

## 1. 数据来源：三条已核实的事实 + 一条硬前置

### 1.1 每轮 agent 运行本来就有时长事件对

Director 的 planner 对它调用的**每一个**工具都发 `tool_call_start` / `tool_call_result`，
带 `tool_call_id`（`internal/agents/director/planner_run.go:474-479` 与 `:513-518`）。
`delegate_*` 也在其中 —— 所以一次委派 = 一条事件对，而 TUI 已经在算它的墙钟
（`internal/tui/tui_update_tasks.go:832`）。**不需要上游新增任何事件。**

### 1.2 "这一轮内部发生了什么"只有一条路可走

子 Agent 的每一个事件都盖 `DelegationID`，值就是那次 `delegate_*` 的 `tool_call_id`
（闸门 `TestRunAgentLoopStampsDelegationOnEveryEvent`，`internal/agents/event_delegation_test.go:40`）。
轮内事件数、轮内单次最慢值都只能靠按 `DelegationID` 归组得到。

`sub_agent_activity` **帮不上忙**：它只写 rollout、不上消息总线
（`internal/agents/executor.go:917-919`，用途是读侧推 `EffectiveSteps`），TUI 侧零消费。

### 1.3 时长有两种钟，本设计不假装它们同源

- **工具与委派的时长**是 TUI 侧墙钟：`time.Since(entry.Timestamp)`
  （`internal/tui/tui_update_tasks.go:832`、`:844`、`:890`、`:902`）—— 起点的 `Timestamp` 是 dispatcher
  盖章的时刻，终点是 TUI **收到** result 的那一刻，**含事件总线排队**。
- **LLM 调用的时长**是上游给的 `duration_seconds`
  （`internal/tui/tui_update_tasks.go:478-482`）。

两种钟混在同一个分布里是现状，不是本文的选择。带子右段与 `◉` 行只用 TUI 墙钟（同源），
sparkline 混用 —— 这一点必须写进 §10 那条测试的注释，别让下一个人以为是 bug。

总线满载丢的是**普通事件**（`internal/messaging/message_dispatcher.go:179` `Dropped()`、`:196` `WarnDropped`），
丢一条 `tool_call_result` 就留下一行永远 running、时长永远不填。本模块的数据完整性依赖事件不丢，
所以 §9 的"诚实性"一条把 `Dropped()` 接了出来。

### 1.4 硬前置：`TimelineEntry` 必须先加坐标

`TimelineEntry` 九个字段里**没有 Agent、没有 DelegationID**（`internal/tui/types.go:76-86`）。
不补这两列，`◉` 行写不出"Coding 还是 Repo"，并行两个 Coding 时更是无从区分。

改动面：七处构造点各填一次（`internal/tui/tui_update_tasks.go:451`、`:593`、`:634`、`:697`、`:725`、
`:761`、`:962`），恢复侧一处（`internal/tui/tui_resume.go:835-863`）。
**顺手**把六遍手写的 `m.timelineCacheKey = ""` 收进一个 `addTimelineEntry()` 入口 ——
现状是每加一种事件都要记得手写那一行，漏了不报错，只留下"浮层停在旧内容"。

### 1.5 委派的识别一律走注册表

```go
toolspec.EffectOf(name)&toolspec.EffectDelegate != 0   // internal/toolspec/queries.go:56 + effect.go:48
```

**不许按 `delegate_` 名字前缀硬判** —— 根 `AGENTS.md` §5.1 明令"不再往任何名字表加条目"，
`internal/toolspec/AGENTS.md` 硬约束 8 是那条纪律的正面表述。

---

## 2. T0 完整形态（7 行）

面板宽 50（`internal/tui/tui_dashboard.go:11`），内宽 `width-4 = 46`（`:156`）。
下面每行 = 46 格，两侧 `│` 只是列边界标记，真实渲染是圆角边框 + 各 1 格内边距。

```
──────────────────────────────────────────────   [46]
⧗ LATENCY                        total 3.7m ⊘2   [46]
  last 7  █▆▆▁▂▁▁                 n=7 p50 2.0s    [46]
  ▲ search_by_regex    8.9s ▰▰▰▰▰▰▰▰▰▰▰▰▰▱▱▱▱▱   [46]
  ▲ run_bash           2.0s ▰▰▰▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱   [46]
  ◉ Coding·ll_a  12.4s ▰▰▰▰▰▰▰▰▰▰▰▰   3c ▲8.9s   [46]
  ◉ Repo·ll_b     8.9s ▰▰▰▰▰▰▰▰▰▱▱▱  2c ▲900ms   [46]
```

**这七行是从渲染器里实拍出来的**（方括号里是 `lipgloss.Width` 的测量值，七行全等于 46），
不是手画的：夹具是 2 轮委派（`call_a` 有 3 个子事件、`call_b` 有 2 个）+ 5 条叶子 + 1 条 8.9s 的
`search_by_regex`，`total` = 221s。注意三处形状细节都符合本稿的意图：
`delegate_repo`(8.9s) 因为它的子事件在窗口里而没有出现在 `▲` 榜上（§4a）；
`last 7` 的高度是 `█▆▆▁▂▁▁` —— 时间序、不是排序（§0）；
带子右段带着 `⊘2`，因为那两轮确实在同时跑（§4c）。

上面七行的内容宽度是按显示宽度**量过**的，每行恰好 46 格（`▰/▱` 由 `round(dur/max × 格数)` 得来：
8.9/12.4 → 13 格、1.2/12.4 → 2 格）。榜首满格是 §2.2 那条"条只做同级比较"的必然结果。
本包所有稿子都按这条标准画 —— 手写对齐不算数，量过才算。

### 2.1 三种行各自的网格

照 `compressColsLeft` 的排法（`internal/tui/compress_panel.go:43-61`）：先固定左段，余量全给条，
右段用 `padRight` 对齐到内宽（`internal/tui/token_panel.go:378-387` 的 `meterLine` 是同一条路）。

| 行 | 网格 | 合计 |
|---|---|---|
| sparkline | `2 │ "last 20 " 8 │ 20 cell │ 2 │ 右段右对齐到 46` | 46 |
| `▲` 事件行 | `2 │ "▲ " 2 │ name 16 │ 1 │ dur 6 │ 1 │ bar 18` | 46 |
| `◉` 轮次行 | `2 │ "◉ " 2 │ run 11 │ 1 │ dur 6 │ 1 │ bar 12 │ 1 │ tail 10` | 46 |

- `name` 一律走 `truncateCells`（`internal/tui/token_panel.go:110-125`）—— 它按**显示宽度**截，
  按字节或按 rune 都会把中文算错（本包已有三处字节截断的既有缺陷，见 §11.3，不要新增第四处）。
- `dur` 一律走 `formatTimelineDuration`（`internal/tui/render.go:826-840`），**不要另写格式化**。
  注意它 `d == 0` 返回空串，空闲态的 `total —` 要显式给字面量，别依赖那个空串。
- `tail` = `17c ▲8.9s`：该轮内事件数 + 该轮单次最慢值。
- `run` 名 = 目标 Agent + `DelegationID` 尾 4 位（`Coding·7f3c`）。并行两个 Coding 时靠后缀分得开。

### 2.2 条长只做同级比较

`▲`/`◉` 行的 `▰▱` 条 = 该行时长 / 窗口最大值，所以榜首永远是满格。这是刻意的：
绝对值一律看左边的数字，条只回答"和最慢的比差多少"。
`▰▱` 是 `fillBar` 的既有字符（`internal/tui/token_panel.go:348-360`），sparkline 的 `▁–█` 是
`ctxSpark`（`internal/tui/compress_panel.go:80`）—— 不引入新字符。

---

## 3. 带子、字形与配色：沿用既有视觉家族

段名走 `dashSectionRow`（`internal/tui/tui_dashboard.go:54-68`）—— 现有三段类别行
（`◆ Tokens` / `⚙ Activity` / `⧉ COMPRESS`）共用的唯一排版：一条铺满内宽的深色横带，
glyph 走 lavender `141`、名称近白粗体 `250`、右段右对齐。

- 带子底色 `#262656` 是边框色 62 的**四成亮度同相色**，不是新添的一种颜色
  （`internal/tui/tui_dashboard.go:13-18`）。不要改它，也不要给本段另立底色。
- **背景必须逐段涂**：lipgloss 在每个内层样式末尾发 `\x1b[m`，会把外层背景一并关掉，
  "整行涂一次"的带子会在第一个彩色字之后断掉（`internal/tui/tui_dashboard.go:38-41` 的 `dashBand`）。
  本段只有带子那一行走 `dashBand`；sparkline 与两条榜都在带外，直接用带外色号。
- `TestCategoryRows_BandSpansEveryCell` 现在只认三行（`internal/tui/tui_dashboard_section_test.go:134`），
  新段的类别行必须纳进去，否则"漏一格底色"没人抓。
- `TestCategoryRows_OnBandTextIsReadable`（同文件 `:163-184`）是**反向**断言 `241/244` 在这条底色上
  不到 4.5:1 的。含义：带外可以用 `244`（本段正是如此），**任何想上带子的新配色都必须换成 `246`**。
- 段名 `LATENCY` 沿用硬编码英文 —— 三个现有段名都不是 i18n 字段：`tokenTitleRow` 直接把字面量
  `"Tokens"` 交给 `dashSectionRow`（`internal/tui/token_panel.go:402-404`），走 i18n 的只有键帽文案，
  它在调用侧填进来（`internal/tui/tui_dashboard.go:183` 的 `langManager.GetText(...)`）。
  本段**不带键帽**（钻取走既有的 `ctrl+l`，见 §8.3）。
- **字形宽度必须是断言**：`⧗ ▲ ◉ ⊘` 四把都要 `lipgloss.Width(...) == 1`。`⧗` 与已用的 `⧉`
  同码段（U+2900 区），风险低，但绝不靠肉眼。`⏱`/`💭` 那类 emoji 形态是 2 格，用了就整行差一列。

---

## 4. 嵌套耗时与并行：五个决定

这三条上文曾作为岔口提出、答复是"无偏好"，此处按推荐理由定案；要推翻就在实现前说。

**(a) `▲` 事件榜排除"子事件在窗口里可见"的那些委派。** 委派 12.4s 几乎必然压过任何叶子，
榜就永远只有委派、看不见可优化的动作；而那件事已由 `◉` 行专门承载。
判据**不是**工具名而是 `runStat.nested()`（轮内有观察到的子事件）——
恢复出来的会话只有 Director 那一层的记忆、看不到任何子事件，这时委派必须仍然上榜，
否则 sparkline 有尖峰而榜上没有名字（`TestLatencyOrphanDelegateStaysOnBoard` 钉这一半）。

**(b) sparkline 里两类都留。** 委派那 12.4s 是真实占用掉的墙钟时间，去掉它就谎报了总账。
所以 `▲` 榜与 sparkline 的样本集**刻意不同**，靠两件事交代：行尾 `n=20` 写分母；
行首形状（`▲` vs `◉`）告诉读者这两行不同源。

**(c) 并行委派下 `◉` 行时长不可相加。** `max_parallel_delegations`（默认 2，根 `AGENTS.md` §7）
>1 时两行同时在跑，各行墙钟之和会超过 `total`。46 列画不了重叠时间轴，改用一条尾注：
当且仅当窗口内检测到重叠时，带子右段变成 `total 3m41s ⊘2`，`⊘N` = 最多同时并行了 N 轮。
必须有一对能失败的夹具：串行数据里出现 `⊘` 就红（§10 第 7 条）。

**(d) 空闲态常驻 2 行。** 一次时长都没测到时走 T5 形态、带子右段 `total —` ——
与 `⧉ COMPRESS` 同一条口径："这一段存在、还没数据"必须可见，
代价核算的思路见 `TestDashboard_IdleBlockCostsExactlyTwoRows`
（`internal/tui/compress_dashboard_test.go:149`）。想省这 2 行就改成整段消失，
那是 30 秒的决定，代价是 discoverability：第一次压缩之前没人知道 `⧉` 段在不在，这个先例已经摆在那。

**(e) 空轮次不占 `◉` 行。** 既没观察到子事件、因而也不知道是哪个 Agent 的那一轮
（`agent == "" && events == 0`），它的那次时长已经以 `delegate_xxx` 的名字出现在 `▲` 榜上了 ——
再给一行纯属占预算。恢复出来的会话整批都是这种轮次，所以那条路上只有 `▲` 榜、没有 `◉` 榜，
这是 §4a 那半边判据的另一面。
（`internal/tui/latency_panel.go` 的 `latencyRunRows`；断言在 `TestLatencyOrphanDelegateStaysOnBoard` 尾部。）

---

## 5. 高度阶梯与让位序

每档只砍一整行 —— 与 `compressTier*`（`internal/tui/compress_panel.go:33-41`）和
`tokenTier*`（`internal/tui/token_panel.go:48-56`）同一套纪律。

```
T0 7   带 / 分隔 / sparkline / ▲▲ / ◉◉
T1 6   − ◉ 第 2 行
T2 5   − ▲ 第 2 行
T3 4   − sparkline          → 只剩两个具名榜首，仍然回答"哪个动作慢"
T4 3   − ◉ 第 1 行                       （分隔 + 带 + ▲1）
T5 2   − ▲ 第 1 行 → 分隔 + 带子          （total 与 ⊘N 永远写在带子上，不是空壳）
T6 0   预算 < 2 → 整段消失，绝不画半截空壳
```

让位序 = **轮次 → 事件 → 分布**。理由：具体 > 抽象。分布条最先砍是因为它没有名字；
轮次行比事件行先砍是因为"哪个动作慢"比"哪一轮慢"更可行动。

两个实现陷阱，都是既有代码踩过的：

1. `maxRows <= 0` 在渲染器里的含义是**不限高**，所以预算不足时必须跳过、不能把 0 传进去 ——
   否则本段会反过来把 Activity 挤掉（那段禁令写在 `internal/tui/tui_dashboard.go:208-210`）。
2. 空壳禁令：带子行要么带着自己的分隔线，要么后面真的跟着内容行（判定逻辑
   `internal/tui/compress_dashboard_test.go:133-142`）。T5 恰好满足前者。

---

## 6. `total` 这个活数字的代价

它必须进渲染缓存键，否则"面板不动"（键在 `internal/tui/tui_dashboard.go:191-196`）。三条判据：

1. **任务运行中本来就绕过缓存** —— `hasRunning` 为真时整块缓存不查（`:198`），而运行中几乎总有
   一条 running 的工具或 llm 条目。所以把 `total` 量化到 1s 进键，**在运行期不增加一次重排**。
2. **任务结束必须定格**。`handleTaskCompleteMsg` 已经调 `invalidateFooterCache()`
   （`internal/tui/tui_update_tasks.go:991`）；在那一刻把 `total` 冻结进 `latencyStats`。
   否则空闲态每秒白白重排一次整段面板，而且 TUI 停 tick 的条件
   （`internal/tui/tui_update_system.go:50` 的 `activeAnim || taskRunning || viewportDirty || hasDirtyEntries`）
   永远不满足 —— 静置时界面在转。
3. **暂停期间不许增长**（根 `AGENTS.md` §11 坑 17：暂停不是取消）。`Gate().Paused()` 在 TUI 读得到
   （`internal/tui/tui_view.go:481-490` 的 `⏸ PAUSED / ⏸ PAUSING` 就是它）。不必等总线事件：
   tick 是 100ms 一拍且只在 `taskRunning` 时跑，在 `handleTickMsg` 里发现这拍处于暂停就累加一个
   `pausedAccum`，显示时减掉。

显示粒度必须 = 进键的粒度（都用 1s），否则"数字在变、面板不重画"。

`dashboardVisible()`（`internal/tui/tui_dashboard.go:103-108`）**不用改**：其中的
`len(m.timelineEntries) > 0` 已经覆盖"有时长数据但 provider 不给 usage"的场景。

---

## 7. 行预算算术（能不能落地的分水岭）

footer 在空输入、侧栏可见时 = `1 空行 + 输入面板 (2+3) + 2` = **8 行**
（`computeFooterHeight` `internal/tui/tui_render.go:18` + `computeInputHeight` 的 `minHeight = 3`
`internal/tui/tui_helpers.go:85`）；视口高 = `termHeight − 8 − 1`（tab 栏）；
面板 `contentRows = 视口高 − 2`（`internal/tui/tui_dashboard.go:160`）。

| 终端行数 | contentRows | token | ⧉ COMPRESS | ⧗ LATENCY | Activity | 合计 |
|---|---|---|---|---|---|---|
| 40 | 29 | 9 | 7 | **T0 7** | 5 | 29 |
| 30 | 19 | 7 | 2 | **T5 2** | 8 | 19 |
| 24 | 13 | 5 | 2 | **T5 2** | 4 | 13 |
| 20 | 9 | 3 | 0（消失） | **T5 2** | 4 | 9 |
| 18 | 6 | 3 | 0 | **0（消失）** | 3 | 6 |

`⧗` 的 `latencyReserveRows = 2`，与 `⧉` 的 `compressReserveRows`（`internal/tui/compress_panel.go:65`）
**同层，都低于 Activity 的 `timelineReserveRows = 4`**（`internal/tui/tui_dashboard.go:138`）。

链式预留要改三处：`internal/tui/tui_dashboard.go:169-176`（token 的扣除）、
`:211-217`（compress 的余量算法，同形加一份 latency 的）、`:220-221`（Activity 拿剩下的）。

**最容易写错的地方**：`TestDashboard_ActivityNeverStarvedByCompress`
（`internal/tui/compress_dashboard_test.go:113`）扫 height 8..24 并断言 `◆` 与 `⚙` 恒在，
它 `:110-112` 的注释用算术证明"下界 8 是物理极限"：`contentRows = 6` = token 保底 3 + Activity 保底 3。
加第四个抢行方之后它仍然能绿，前提有两个，都必须写进代码：
(a) latency 与 compress 都在 `maxRows < 2` 时**一个字节都不占**；
(b) 该测试扩一条断言：`⧗` 不得挤掉 `◆` / `⚙`。
这条一旦写错，症状是**测试永远绿、矮终端天天缺段**。

---

## 8. 降级形态

### 8.1 宽度阶梯（与高度阶梯正交）

```
inner ≥ 40：全网格（bar 18 / bar 12）
inner 28–39：去 bar，dur 紧跟名字（照 compressTierCompactRow，internal/tui/compress_panel.go:517-523）
inner < 28：run 名截到 6、tail 去掉 "Nc"，sparkline cell 数 = inner − 12（下限 8，同 compressMinBar 的判法）
```

侧栏内宽恒为 46（`dashboardPanelWidth = 50` 且 `dashboardWidth()` 只在 `termWidth-50 ≥ 40` 时放行，
`internal/tui/tui_dashboard.go:117-126`），所以上面两档**只在底部面板那条路上会跑到**。

### 8.2 侧栏不可见时（`termWidth < 120` 或 `alt+d` 收起）

底部 token 面板给 1 行，形状照 `compressSummaryRow`
（`internal/tui/compress_panel.go:581-595`，接法见 `internal/tui/token_panel.go:727-734`：
同一个面板、不另起边框、不前置空行，让 `lipgloss.Height` 自己跟着长，
`computeFooterHeight` 不必新增分支）：

```
  ⧗ total 3.7m ⊘2  ·  p50 2.0s  ·  ▲search_by_regex 8.9s  ·  ◉Coding·ll_a 12.4s
```

`alt+t` 折叠态**不给**（一行塞不下四段，与 `⧉` 的处理一致）。

### 8.3 钻取：不加新键

`ctrl+l` 全屏 timeline 里加一个 `s` 键循环"时间序 ↔ 耗时降序"。`s` 在该模式的按键表里没被占用
（`internal/tui/tui_update_timeline.go:39-207` 的 switch 分支里没有 `s`）。

走这条路**必须先修两个既有缺陷**，否则新视图第一天就复现它们：

- **全屏详情在任务运行中不重建**：`timelineFullscreenUpdate` 的 `taskEventMsg` 分支
  （`internal/tui/tui_update_timeline.go:245-249`）只调 `handleTaskEventMsg`，那里不会
  `buildAllTimelineDetails`；左列表读活数据、右栏的 `timelineDetailOffsets` 是开全屏那一刻的数组，
  `syncDetailToCursor` 又把 cursor 夹到 `len(offsets)-1`（`internal/tui/tui_timeline_fullscreen.go:517-533`）。
  症状：跑到一半进全屏、跳到最新一条，右侧显示的是**开全屏之前**那条的详情。
- **右栏宽度两套公式**：`internal/tui/tui_timeline_fullscreen.go:35`、`:258`、`:292` 用 `0.2333`，
  而 `internal/tui/tui_update_timeline.go:215` 用 `0.35`。resize 之后视口宽按 0.35 设、
  内容排版宽按 0.2333 算（`buildAllTimelineDetails` 调 `calcRightPaneWidth`），
  两栏拼起来总宽超过 `termWidth`。这是 `internal/tui/AGENTS.md` 硬约束 3"只有一个口径"那一类。
  症状需要真终端确认（本仓没有 TTY 测试回路）。

反例（不要这样做）：加一把 `alt+e` 虽然空着，但要同改 `[tui.keybindings]` config 结构、
`editKeyBindings` 与 `commandKeyBindings` 两张表（`internal/tui/tui_keymap.go:71-120`）、消费点、
帮助对话框，并受"键位是替换语义、键帽写的每个单键都必须真的改状态"那条双向纪律约束
（`internal/tui/AGENTS.md` 硬约束 10 / 12）。为一视图内的一把排序键付这个代价不值。

---

## 9. 数据结构与接入清单

### 9.1 新文件 `internal/tui/latency_panel.go`

与 `⧉` 段同构（数字只在一处算、排版层不碰 model 计数器 —— `internal/tui/AGENTS.md` 硬约束 5）：

```go
type latencySample struct {
    name       string        // 显示名（已走 DisplayToolName 的那份）
    dur        time.Duration
    ts         time.Time
    delegation string        // 空 = 非委派子事件（Director 自己的工具、以及 llm_call）
    isDelegate bool          // §1.5 的判据，采集时算好存着，排版层不再查注册表
}

type latencyStats struct {
    samples []latencySample   // 环形，容量 latencyWindow = 20
    runs    map[string]*runStat  // delegationID → {name, dur, count, slowest}
    total   time.Duration     // 任务结束时定格
    paused  time.Duration     // 暂停累计
}

func (l latencyStats) clone() latencyStats   // 切片 + map 都要拷
func (m *model) latencySnapshot() latencySnapshot
func buildLatencyBlock(s latencySnapshot, spec latencyPanelSpec) []string
func latencySummaryRow(s latencySnapshot, inner int) string
```

`clone()` 不是可选项：`compressStats` 就是因为 cut 表和采样数组被事件原地改写，
切标签页会串台，才必须拷（`internal/tui/compress_panel.go:105-121`）。

### 9.2 接入点（漏一条都是静默失效，不是崩溃）

| 项 | 落点 |
|---|---|
| 采集漏斗 `recordLatency(name, kind, dur, ts, delegationID, isDelegate)` | 只在两个既有改写点各调一次：`tool_call_result`（`internal/tui/tui_update_tasks.go:828-858`）与 `llm_call_end`（`:475-493`）。先例：`recordCompression` 在三档共享分支只统计一次（`:651-662`），各档自己不再碰计数器 |
| `taskStartTS` + `markTaskStarted()` | 收编现有三处 `taskStarted = true`（`internal/tui/tui_tasks.go:69`、`:123`，`internal/tui/tui_resume.go:946`）；`clearCurrentSession` 清零（`internal/tui/tui_tabs.go:296` 那一组） |
| `pausedAccum` | `internal/tui/tui_update_system.go:43` 的 `handleTickMsg`，读 `Gate().Paused()` |
| 随标签页存/取 | `internal/tui/tui_tabs.go`：字段声明（同 `:62` 的 `compress`）、save（同 `:180`）、restore（同 `:242`）、清空（同 `:317`）**四处成组改** |
| 进渲染缓存键 | `internal/tui/tui_dashboard.go:196` 之后 `key += "\|lat:" + ls.signature()`；签名含 `total`、窗口样本、轮次表 |
| 行预算链 | 三处，见 §7 |
| 底部面板那 1 行 | `internal/tui/token_panel.go:727-734` 那段的同侧 |
| `/resume` 侧也要喂 | `internal/tui/tui_resume.go:835-863` 已有 `duration` 与 `restoredToolDuration`（`:873-879`）—— 恢复的会话必须同时建 latency 环，否则时间线有数据而本段永远空（根 `AGENTS.md` §11 坑 33 的"测试与生产不同载体"版本） |
| 诚实性（可选，动手前先确认） | `internal/tui/tui_tasks.go:204`、`:280` 已经 `defer dispatcher.WarnDropped("tui/task")`，说明 TUI 每轮自建 dispatcher。把 `Dropped()` 接进带子右段的 dim `⚠N` 需要 model 上持一个 dispatcher 句柄 —— **这一条我没核实到 model 是否已有该句柄，实施前先确认，不要临时造全局变量** |

---

## 10. 闸门清单（每条都得真的能红）

`internal/tui` **没有任何脚本守卫**（九把 `check_*.sh` 只覆盖依赖方向、裸 go、原子写、文件行数、
测试卫生），所以本段的全部不变量只由下面这些测试 + 注释钉住。写测试时守
`internal/tui/AGENTS.md` 的"测试约定"：断言**精确单元格宽度**而非子串，宽度矩阵 `{30..240}`，
共享 fixture `newTestModel()` / `chromeTestModel(t,w,h)`，语言用 `testLang(t)`
（`internal/tui/tui_dashboard_border_test.go:34`）钉英文。

| # | 测试 | 钉住哪条决定 / 变异 |
|---|---|---|
| 1 | `TestLatencyRows_NeverExceedsInner` + `TestLatencySectionRows_SumToPanelWidth` | 宽度矩阵 {20..240} × 六个档位：每行 `<= inner`，而 inner=46 时分隔线与带子**恰好** 46。漏 `padRight` 即红 |
| 2 | `TestLatencyHeat_IsChronologicalNotSorted` | 喂一组乱序时长，断言 cell 序列 == 输入顺序。**写成排序就红** —— 这条守 §0"形状有信息" |
| 3 | `TestLatencyCalmWindowHasNoHotCell` | 全窗口 `< 2s` 不许出现 214/203。把 `latencyCalmMax` 调成 0 必须红 |
| 4 | `TestLatencyBands_EachThresholdIsReachable` | 四档各造一条刚好越界的数据（根 `AGENTS.md` §11 坑 15：校验规则本身必须能被触发） |
| 5 | `TestLatencyNestedDelegateStaysOffBoard` + `TestLatencyOrphanDelegateStaysOnBoard` | 排除判据是"子事件在不在窗口里"，不是工具名（§4a）。删掉 `nested` 过滤即红；把它写成无条件排除，恢复侧那条即红 |
| 6 | `TestLatencyRunRowCarriesOwnStats` | `17c` 与 `▲8.9s` 来自按 `DelegationID` 归组；喂两个不同 delegation 的交错事件，断言两行计数互不污染 |
| 7 | `TestLatencyOverlapsStampParallelRuns` | 串行数据里出现 `⊘` 就红；并行数据里不出现也红（§4c） |
| 8 | `TestLatencyPauseDoesNotAdvanceTotal` + `TestLatencyTickAccountsForPause` | 真闸门（`gatedTask` + `parkOnGate`）：PAUSING 那 10 秒算工作、`Blocked()` 之后才算暂停；并断言结算点接在 `handleTickMsg` 里 |
| 9 | `TestLatencyTotalFreezesOnTaskComplete` | `taskCompleteMsg` 之后再喂 tick，`total` 不变（§6.2） |
| 10 | `TestLatencySection_EntersDashboardCacheKey` | 只改延迟数据、不动 token/timeline/compress，断言面板重画。手法照 `TestDashboardCacheKey_IncludesCompressStats`（`internal/tui/compress_dashboard_test.go:210`） |
| 11 | `TestLatency_NeverStarvesTokensAndActivity` | 扩写 `TestDashboard_ActivityNeverStarvedByCompress`（`internal/tui/compress_dashboard_test.go:113`）的 height 8..24 扫描，加 `⧗` 不得挤掉 `◆`/`⚙`；`contentRows` 不足时 `⧗` 与 `⧉` 必须为 0 |
| 12 | `TestLatencyIdleCostsExactlyTwoRows` | 空闲态恰好"带 + ▲ 行"或"带 + 分隔"两行，不越界、不空壳。同 `TestDashboard_IdleBlockCostsExactlyTwoRows`（`:149`）的思路 |
| 13 | `TestLatencyStats_ClonedPerTab` | 两个 tab 各喂事件，断言环与 runs map 不共享底层数组（`internal/tui/tui_tabs.go:180`/`:242` 那一组） |
| 14 | `TestApplyRestoredMemory_LatencySeededWithTimeline` | 与 `TestApplyRestoredMemory_TimelineRebuildAndMerge`（`internal/tui/tui_resume_restore_test.go:210`）**成对**：恢复后时间线有 N 条可测事件 ⇒ 窗口有 N 个样本 |
| 15 | `TestTimelineEntryCarriesDelegationCoordinates` | §1.4 那七处构造点 + 恢复侧每一处都真的填了 `Agent`/`DelegationID`。只填测试夹具不填生产即红（坑 33） |
| 16 | `TestLatencyGlyphsAreOneCell` | `⧗ ▲ ◉ ⊘` 各自 `lipgloss.Width == 1` |
| 17 | `TestCategoryRows_*` 三条扩写 | 新段的类别行纳入 `_BandSpansEveryCell`、`_ShareOneHeaderPalette`；带上文字若新增色号必须进 `_OnBandTextIsReadable` 的 4.5:1 名单 |
| 18 | `TestLatencyFooterRow_NeverExceedsInner` + `_CountedInFooterHeight` + `TestLatencyFooterWidthLadder` | 底部那 1 行不越内宽（手法照 `TestTokenPanel_FooterFitsTermWidth`，`internal/tui/token_panel_test.go:258`），且加了它 footer 必须**恰好**长 1 行；窄档只掉细节不掉 total |
| 19 | `TestLatencyP50IsMedianNotMax` | 报的分位数不许等于榜首那个数（换回 p90 即红） |
| 20 | `TestLatencyRingCapsAtWindow` / `TestLatencyIgnoresUnmeasured` / `TestLatencyEvictedRunIsDropped` | 窗口上限、没有时长的一律不进、被淘汰的委派连带作废那一轮 |

`bash scripts/check_all.sh` 一条命令跑完全部守卫 + 单测。改到并发或计时还要 `RACE=true`。

---

## 11. 既有事实与相邻缺陷（不在本次范围，但会被引用）

### 11.1 `expanded` 与 `timelineExpanded` 是死枝

`RenderTimeline` 的 `expanded=true` 分支（含 `maxTimelineEntries = 20`，
`internal/tui/render.go:842-863`）**两个生产调用点都传 false**
（`internal/tui/tui_dashboard.go:229`、`internal/tui/tui_view.go:321`）；
`m.timelineExpanded` 被写四次、全是 `= false`（`internal/tui/tui_update_keys.go:818`，
`internal/tui/tui_update_timeline.go:42/54/66`），从未被读进渲染。
本设计把"20 条窗口"接成活的定义（§0），但**不要**把 `timelineExpanded` 复活成第三种形态 ——
那个概念已经整个收敛进 `ctrl+l` 全屏。

### 11.2 timeline 条目没有总上限

`m.timelineEntries` 只有折叠视图取 3 条（`selectCollapsedEntries`，
`internal/tui/render.go:958-992`），列表本身无上限，而 `tool_call_result` 是 O(n) 倒序扫。
`latencyStats.samples` 必须有环上限（20），别学它。

### 11.3 三处按字节截断（中文会切坏半个 rune）

`internal/tui/tui_update_tasks.go:589-591`（thinking 预览 80）、
`internal/tui/render.go:939-943`（脊柱行 detail 40）、
`internal/tui/tui_timeline_fullscreen.go:181-183`（全屏状态栏名字）。
本包已有按显示宽度截的正确工具：`truncateCells`（`internal/tui/token_panel.go:110-125`）
与 `common.Ellipsize`。本模块一律用前者；**顺手修那三处不在本次范围**，
另开一条机械清理单独 commit。

### 11.4 `DisplayToolName` 有三份、已经漂了

`internal/tui/styles.go:63` 只映射 `search_replace_in_file→edit_file`；
`internal/tui/common/styles.go:49` 多一条 `llm_call→think`；第三份在
`internal/messaging/consumers/tui.go:50`（那是 `--prompt`/HTTP 的 stdout 消费者，与本模块无关）。
timeline 行调的是包内那一份（`internal/tui/render.go:905`），所以 `⧗` 段也应只认包内那一份，
**不要**去调 `common` 的那份 —— 那会让 `llm_call` 在榜上显示成 `think`、在脊柱上显示成 `llm_call`。

### 11.5 `alt+r` 被消费但没注册进按键表

`internal/tui/tui_update_keys.go:641`/`:894` 消费 `"alt+r"`，而它不在
`editKeyBindings`/`commandKeybindings`（`internal/tui/tui_keymap.go:71-120`）里，
所以这把键**换不走也重新绑定不了**。`ctrl+g`（`:648`）同样。与本模块无关，登记备查。

---

## 12. 实施顺序建议

按"先补前置、再上渲染、最后接恢复"排，每步都能单独 build + 跑 §10 里对应那几条：

```
L1  TimelineEntry 加 Agent/DelegationID（§1.4，七处 + 恢复一处）+ 闸门 15
L2  latencyStats 数据结构 + recordLatency 两个采集点 + clone + tab 四处成组 + 闸门 13
L3  snapshot / signature / buildLatencyBlock 的 T5（2 行）+ 接进行预算链 + 闸门 11/12/10
L4  sparkline + ▲ 榜 + 阈值染色 + 冷静规则 + 闸门 1/2/3/4/5/16
L5  ◉ 轮次行（runs 归组、17c、▲8.9s）+ 并行 ⊘ + 闸门 6/7
L6  taskStartTS/markTaskStarted 收编三处 + total 定格 + pausedAccum + 闸门 8/9 + §6
L7  底部面板那 1 行 + 宽度阶梯 + 闸门 18 + 尺寸矩阵
L8  /resume 喂数据 + 闸门 14（与既有那条成对）
L9  文档：internal/tui/AGENTS.md 加"硬约束 16 ⧗ 段"一节 + 根 §6.8 一句话 + 闸门名登记
```

L1 是本设计唯一的"改动面比新代码大"的一步。它的价值不止本模块：
委派归属、`llm_call_end` 关错行（`internal/tui/tui_update_tasks.go:475-476` 找"最后一条 running 的
LLMCall"时不校验 `msg.event.From`）、并行委派的合并串台，三件事都卡在同一个缺失的坐标上。

---

## 13. 实施状态与偏离（2026-10-25，分支 `feat-dashboard-latency`）

`bash scripts/check_all.sh` 全绿（编译 / vet / 十把守卫 / 全量单测）。L1–L8 与 L9 的文档部分都已落地。

**新增文件**：`internal/tui/latency_panel.go`（数据层 + 快照 + 排版 + footer 那行）、
`latency_panel_data_test.go`、`latency_panel_render_test.go`、`latency_panel_surfaces_test.go`、
`latency_total_test.go`、`tui_timeline_coords_test.go`（L1 的坐标闸门）。

### 13.1 相对本稿的六处偏离

1. **`p90` → `p50`**（§0、§2）。`n=9` 时 `ceil(0.9n)-1` 正好是最后一个元素，分位数退化成榜首那个数，
   报它等于把同一个数字说两遍。中位数在任何 `n` 上都有意义，"最慢的是谁"已经由 `▲` 榜负责。
   钉住它的是 `TestLatencyP50IsMedianNotMax`（断言 `p50 != maxDur`）。
2. **T5 的最低形态是"分隔线 + 带子"，不是"带子 + ▲ 行"**（§5）。与 `⧉` 段同一条空壳禁令：
   带子必须带着自己的分隔线或后面跟着内容行。`total` 与 `⊘N` 永远写在带子上，所以两行形态仍然有信息。
3. **`▲` 榜排除委派的判据改成了数据驱动的 `runStat.nested()`**（§4a），而不是无条件排除委派类工具。
   理由：恢复出来的会话看不到子事件，无条件排除会让"有尖峰却没有名字"。两半各有一条测试。
4. **新增 (e)：空轮次不占 `◉` 行**（§4e）。本稿没写这条，实现时是 §4a 那半边判据逼出来的。
5. **恢复的会话 `total` 保持 `—`**，不盖起点。`tui_resume.go` 里那处 `taskStarted = true` 是输入框边框
   旗标、不是一次新执行的起点，注释已就地写明不要换成 `markTaskStarted()` —— 我们没计时就别编一个数。
6. **§8.3 的钻取（`ctrl+l` 全屏里加 `s` 排序）没有做**。它的前置是那两个既有缺陷
   （全屏详情运行中不重建、右栏 `0.2333` 与 `0.35` 两套宽度系数），本次没有动 —— 那是独立的一档工作量，
   而且第一条需要先决定"运行中多久重建一次详情"。

### 13.2 L1 顺手修掉的三件事（本稿 §12 已预告）

- 合并判据现在要求**同一 Agent、同一轮委派**：并行两个 Coding 的 `read_file` 不再串成一个节点
  （`TestParallelSameToolDoesNotMergeAcrossDelegations`）。
- `llm_call_end` 改为按坐标配对，A 的收尾不再关掉 B 那一行
  （`TestLLMCallEndClosesOwnAgentRow` —— 变异回旧写法时它精确地报出"关错行"）。
- `llm_call_end` 的时长解析与记账移到 `llmCallActiveEntries` 守卫**之前**：`llm_call_start` 被总线丢掉时，
  配不上那一行，但这次调用的耗时不再凭空消失。

### 13.3 验过的变异，和没验的

按"声称闸门能失败就得真做变异"的规矩，下列五处做了原地变异 + 还原，各自精确红在该红的断言上：
`TimelineEntry` 漏填 `DelegationID`、合并判据去掉坐标、`llm_call_end` 退回"最后一条 running"、
`addTimelineEntry` 不失效浮层缓存、`dashboardCacheKey` 漏 `|lat:` 签名、`▲` 榜的 `nested` 过滤、
恢复路径不喂耗时账（七处，其中最后三条在实现过程中就是被测试逼出来的 bug）。

**没做变异的**：`latencyCalmMax` 的冷静规则、⊘N 的重叠判定、footer 行数自洽。
前者写测试时想把 `calm := s.maxDur < latencyCalmMax` 改成常量被权限层拦下，后两条只有测试断言、没有红过。
读这段的人应当把它们当作"未证伪的防线"（根 `AGENTS.md` §11 坑 43 说的就是这件事）。

**从来没有真终端跑过**：本仓没有 TTY 测试回路，所有形状断言都是 `lipgloss.Width` 上的测量。
第一次真机验收只能由你在真实终端里做 —— 尤其要看的是 40/30/24 行三档下 ⧗ 段的让位形态，
以及 `⧗ ▲ ◉ ⊘` 四把字形在你的终端字体里是否真的各占一格。
