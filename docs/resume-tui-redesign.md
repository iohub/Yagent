# Resume TUI 重设计（主从双栏 + 停点判据）

> 决策（2026-10-16 用户拍板，四轴互斥各选其一）：
> **布局 B 主从双栏** · **数据 D1 读尾部判据** · **样式 S1 同族导轨** ·
> **L2 模态确认框收进新屏**。
> 本文是实施规格。§1 的缺陷全部是实测，不是观感判断。

---

## 1. 现状与缺陷

两层：

| 层 | 位置 | 触发 |
|----|------|------|
| **L1 全屏会话选择器** | `internal/tui/tui_resume.go:1047-1385` `renderResumeView` | `/resume`、`/rs`（`tui_update_commands.go:28,96`） |
| **L2 模态确认框** | `internal/tui/components/resume_dialog.go` | 选中并**全量读出** rollout 后，`FinishState()==FinishInterrupted` 才弹（`tui_resume.go:463-470`） |

L1 的数据源是 `memory.ListRolloutSessions`（`rollout_sessions.go:73`）——**只 head-scan**。

| # | 缺陷 | 根因位置 |
|---|------|---------|
| **F1** | **任何终端宽度下都至少吐出一行超出终端宽度** ⇒ 软换行 ⇒ 整屏多顶一行 ⇒ AltScreen 下底框被推出可视区且滚不回去 | `tui_resume.go:1236` 用 `len()` 字节算列宽；`cleanTitle`（`tui_history.go:561`）按 **rune** 截断，1 个中文 = 2 列 |
| **F2** | 列不对齐，纵向无法扫读；`▸ ` 前缀把有子文件的行整体右推 2 列 | 同上 |
| **F3** | 选中即失去层次：`indicatorStyle`/`dateStyle`/`titleStyle` **三个样式前景+背景完全相同**（都是 36 底 / 0 前景 / Bold） | `tui_resume.go:38-51` |
| **F4** | 游标一动列表几何就变：详情行插在选中那条**后面** | `tui_resume.go:1163-1165` |
| **F5** | 分页两套数：窗口用 `resumePageSize`(20)、正文用夹过的 `effectivePageSize`；短终端静默吞行且无 `↓ N more` | `tui_resume.go:300-323` vs `1062-1068` |
| **F6** | 没有侧边框（只画顶/底两横线，内容行宽 = 终端宽）；文案硬编码英文，而 L2 是双语 | `tui_resume.go:1072,1090,1099,1358` |
| **F7** | **列表无法回答"哪条值得续"**：`RolloutSessionInfo`（`rollout_sessions.go:37-64`）没有 `StopReason` | 判据在文件**尾部**，列表只扫头部 |

F1 的实测数字（临时闸门跑 `renderResumeView`，跑完已删）：

| 终端宽 | ASCII 预览 | 中文预览（本项目常态） |
|---|---|---|
| 60 | 超 1 列 → 21 行 > 20 | 超 4 列 |
| 76 | 超 1 列 → 25 行 > 24 | 超 12 列 |
| 100 | 超 1 列 → 31 行 > 30 | 超 **35** 列 |

超出那行固定是展开后的 `  └ ` 成员行。

---

## 2. 目标形状（布局 B）

```
╭─ ▌RESUME · 6 sessions ────────────────────── PAUSED 1 · CANCEL 1 · DONE 3 · OTHER 1 ─╮
│                                                                                      │
│ ╭─ ▌TASKS · 6                 ─╮ ╭─ ▌SESSION 10-16 22:16 · T1 · 42m 前            ─╮ │
│ │ ▌ ● 10-16 22:16 qwen3-coder  │ │ 停在 Director step 12/100 · idle_timeout        │ │
│ │     重构 TUI 的 resume 界面  │ │ 可原样续跑：检查点指纹相符 · 工作区未移动       │ │
│ │   ○ 10-15 19:02 gpt-4.1      │ │ ── 账目 ─────────────────────────────────────── │ │
│ │     压缩阈值取小后面板百分比 │ │ model qwen3-coder      branch main              │ │
│ │   × 10-14 11:40 deepseek-v3  │ │ cwd ~/iohub/yagent     sess 4f9c2a1b            │ │
│ │   ! 10-13 09:12 glm-4.6      │ │ 128 msgs · 41 calls · 3.3 MB · thinklink 9      │ │
│ │   - 10-09 08:20 mimo-coder   │ │ ── 停点（委派树） ───────────────────────────── │ │
│ ╰──────────────────────────────╯ │ ✓ delegate_repo    梅理边界       1.1 KB        │ │
│                                  │ ● delegate_coding  step 37/150    可重放        │ │
│                                  │ ○ next             跑宽度矩阵     未开始        │ │
│                                  │ ── 风险 ─────────────────────────────────────── │ │
│                                  │ 悬空 search_replace_in_file · run_bash          │ │
│                                  │ ── 续跑哪一档 · tab · ⏎ ─────────────────────── │ │
│                                  │ ▸ 从 coding 37 续跑  重放账目不重做已完成       │ │
│                                  │   从 Director 续跑    重新规划可能重做一次      │ │
│                                  │   仅浏览              不动任务                  │ │
│                                  ╰─────────────────────────────────────────────────╯ │
╰─ j/k 列表 · tab 焦点 · ⏎ 确认 · e 导出 · ? 帮助 · q 退出─────────────────────────────╯
```

**为什么是双栏**：F1/F4/F5 同根 —— 一栏里"既要列表又要详情"，详情只能插在列表中，于是游标移动必然改变列表几何。双栏是**唯一能结构性消除 F4** 的形状：游标移动只换右栏内容，左栏行数恒定。

**为什么 L2 可以取消**：`ResumeDialog` 展示的就是「原因 / 步数 / 未完成计划 / 悬空工具 / 三档选择」，与右栏卡片**同一份内容**。合并后少一层表面，也少一次"Enter 之后才告诉你这条不能续"。

**几何不变量**（沿用向导的三道闸门，`setup_render.go:82-104`）：
1. 列宽全部来自 `common.AllocateColumns`，截断一律 `common.Ellipsize`（按**显示宽度**，不是 rune 数）；
2. 每行出框前过 `fitLines`（超宽剪断，不让 lipgloss 再折一次）；
3. 面板总高过 `trimToHeight`，藏起来的行数如实标 `↓ N more`；
4. 左列表 `visibleRows = 右栏 rows`，两边共用同一个数字（消掉 F5 的两套数）；
5. 窄终端退化：`width < 76` 时不渲染右栏，详情压成列表下方固定 4 行抽屉（布局 A 的形状，此时不会跳，因为抽屉在列表**之外**）。

**语义色**（S1，零新定义，全从 `common.DarkModeColors()` 取）：
边框 `ResumeState` 可重放=39 / 需交接=214 / 已完成=240 / 失败=196；
选中行 = `▌` gutter(141) + 行底 236 + 前景分层（时间 244、分支 80、模型 248、标题 252）；键帽 `common.KeyHintStyle`（235 底 250 前景）。
**状态列恒定存在**（无子文件时补空格），这是对 F2 的修复而不是装饰。
文案全部进 `i18n.go`（修 F6 的双语断层），且底栏键帽必须每个都真能改状态
（`TestWizardHintsAreNotDecoration` 同族反向闸门）。

** glyph 词表**：`●` 可原样重放 / `○` 已取消可试 / `✗` 需交接 / `✓` 正常收尾 / `!` 真失败 / `-` 未知（存量）。
**刻意避开 `⏸`（U+23F8，EA 歧义 + Emoji）与 `⚠`（U+26A0）**：一个歧义宽度字符就能让固定列整行歪掉。

---

## 3. D1：判据怎么进来

### 3.1 memory 层新增尾读

```go
// internal/memory/rollout_stop.go（新）
type StopFacts struct {
    Found      bool // 尾部读到过终止记录（false = 存量/被截断/半路被杀）
    Finished   bool // task_complete
    Aborted    bool // turn_aborted
    StopReason string
    AbortReason string
    Step       int
    MaxSteps   int
    Checkpoint *AgentCheckpoint // 有则非 nil：重放坐标
}

func ReadRolloutStopFacts(path string) (StopFacts, error)
```

- 从文件尾反向读**最后 32 KB**，丢掉首条可能被截断的行，逐行解码，只保留
  `event_msg`（`task_complete`/`turn_aborted`）的最后一条与 `agent_checkpoint` 的最后一条；
  终止记录由写入端保证靠近文件尾（两个循环都只在**终止路径**上写，见 §11 坑 21）。
- 解码**必须复用 `rollout_types.go` 的 `EventMsg` / `AgentCheckpoint` 结构**，
  不在 stop 侧另写一套 JSON tag —— 两套 tag 漂移的表现是"判据静默读不出来"，
  而读不出来会被 `Found=false` 当成存量会话，比报错更难查。
- 文件 ≤32 KB 就整读；读失败返回 `error`，调用方负责回落成 `Found=false` 而不是滤掉这条。

### 3.2 分类表只留一份（放在 memory）

`agents/resume.go:204-232` 已有一张 `switch kind → (Resumable, Unresumeable)`，
而 `rollout_types.go:339-345` 的词表注释明确写了：**词表只能放在 memory，
因为消费方分属三个包**。所以这张表搬到 memory，`agents` 追它：

```go
type ResumeState int
const (
    ResumeReplayable ResumeState = iota // paused / cancelled → 可原样继续
    ResumeHandoff                       // max_steps / *timeout → 只能走交接报告
    ResumeFailed                        // llm_error / circuit_breaker_open / panic
    ResumeFinished                      // agent_exit / plain_text / task_complete
    ResumeUnknown                       // 存量 rollout：无终止记录
)
func (f StopFacts) State() (ResumeState, string) // string = 人话原因
```

默认值必须是"不可原样重放"：反过来写会让将来新增的任何停止原因
自动获得重放资格，而那正是把一次失败当成可继续、进而重复执行副作用的路径
（`agents/resume.go:228-231` 的原话）。

### 3.3 TUI 侧：不需要新接缝

`internal/tui` **已经**直接 import `internal/memory`（`tui_resume.go:13`），
所以 `loadResumeListCmd` 里直接调 `ReadRolloutStopFacts` 就行。
（本文早先草案提过 `app.RolloutStops` 薄封装 —— **作废**：那是一层没有必要的转发。
需要走 `app` 的只有 `ResumableDelegation`，因为它落在 `agents.ScanDelegationStops` 上，
而那确实不能从 TUI import。）

并发：`sync.WaitGroup` + 容量 8 的 channel 信号量。
**不用 `errgroup`** —— `golang.org/x/sync` 在 `go.mod:86` 是 indirect，
为这点便利提成直接依赖不值当。

排序改为 **ResumeState 优先 → 组内 mtime 新→旧**：
Replayable → Handoff → Unknown → Failed → Finished。
计数进顶栏 meta。不可重放的**照样列出并写明原因**，静默滤掉是
`ScanResumeCandidates` 注释里已经写死的禁令。

---

## 4. 实施切分

| 步 | 交付 | 动的文件 | 验收 |
|----|------|---------|------|
| **R1** | `ReadRolloutStopFacts` + `State()` 分类表搬到 memory；`agents.ScanResumeCandidates` 改为追这张表 | 新 `memory/rollout_stop.go`；改 `memory/rollout_types.go`（词表注释）、`agents/resume.go` | 单测 + `e2e_replay`/`e2e_resume` 全绿（行为等价重构） |
| **R2** | TUI 拿到判据：列表带 `StopFacts`、按 state 排序、顶栏计数、行首状态符 | `tui_resume.go`（数据侧）、新 `tui_resume_stop.go` | 现有 resume 测试全绿 + 新判据用例 |
| R3 | 几何内核搬到 `common`（列对齐 + `Ellipsize` + `fitLines` + 单一 pageSize + `↓ N more`） | 重写 `tui_resume.go:1047-1385` | 宽度矩阵闸门（先让它**红**，再修绿） |
| R4 | 双栏 + 右栏详情卡 + `<76` 退化 | 新 `tui_resume_detail.go` | 矩阵 + 游标不动几何测试 |
| R5 | 三档决策收进新屏，删 `components/resume_dialog.go`，i18n 补齐 | 改 `tui_resume.go`/`i18n.go`，删 3 个文件 | 迁移 `resume_dialog*_test.go` 的断言 |

R1+R2 = 本文档这一次交付。R1 是纯等价重构，R2 只加数据与排序 ——
两步都不动渲染几何，所以宽度矩阵暂时还是红的，那是 R3 的活。

---

## 5. 测试闸门

新增 `internal/tui/tui_resume_render_test.go`，形状照 `TestWizardRenderWidthMatrix`：

1. **宽度矩阵**：W ∈ {52,60,76,92,120,240} × H ∈ {12,18,24,40} ×
   焦点侧 ∈ {列表, 决策档} × 折叠 ∈ {收, 展}，断言
   **每行 `lipgloss.Width ≤ W`** 且**总行数 ≤ H**。
   夹具必须**同时**含纯 ASCII 预览与中文预览（F1 的两条触发路径），
   且至少一条会话带子 Agent 成员（`  └ ` 那一行就是历史越界点）。
2. **夹具自带前置条件**：断言"这份夹具确实超出了预算"，否则又是一个永远不会失败的测试
   （AGENTS §8.3 整理前的 16 个空壳用例就是这么来的）。
3. **游标不动几何**：游标在第 1/2/3 行之间移动，左列表输出必须逐字节相同（钉 F4）。
4. **分类表每条都可触发**：`State()` 对每个 `StopKind*` 都要有一个"它必须这么判"的用例，
   含 `Found=false → ResumeUnknown`（坑 15：只测"生产注册表没有 problems"等于没测）。
5. **尾读**：`go test ./internal/memory -run RolloutStop` 钉三条 ——
   "尾部有 checkpoint + 终止事件 → 读得到"、"只有终止事件没有检查点"、
   "什么都没有 → `Found=false` 且**不报错**"。
   夹具文件写在 `t.TempDir()`，**不碰 `~/.yagent`**（坑 23：`internal/memory`
   不像 `internal/agents` 那样隔离 `$HOME`，"扫目录再计数"的用例在并发下会累加）。
6. **底栏键帽反向闸门**：写出来的每个单键都必须真能改状态。
7. R5 删 `resume_dialog.go` 时，`resume_dialog*_test.go` 的断言必须**迁移**而不是删除，
   也不许留 `t.Skip` 空壳（`scripts/check_test_hygiene.sh`）。

---

## 6. 明确不做

- 不做搜索 / 筛选轨，不引入 `tab` 换栏的三焦点模型（那是 §7 的 C）；
- 不建 rollout 索引文件（§7 的 D2）；
- 不动 `/resume` 的会话级 / 委派级恢复语义，也不动 B/C 层的八条重放降级判据 ——
  本次只把它们**提前显示**出来；
- 不在 TUI 里新增"删除会话"这类写操作。

---

## 7. 已否决的备选（留档，免得下次重走）

| 方案 | 描述 | 否决理由 |
|------|------|---------|
| **A 单栏 + 固定详情区** | 一栏列表 + 列表**之外**的固定 4 行抽屉 | 不跳（抽屉不在列表流里），窄终端友好；但一屏要分给两块，列表可见条数从 20 掉到 15。它的形状保留为 `<76` 列的退化态 |
| **C 三段工作台** | SCOPE 筛选轨 + 列表 + 委派时间线抽屉 | 实测需 ≥104 列；且引入第二套焦点环（`tab` 换栏），与 timeline 全屏已有的 `h/l` 模型并存 = 长期两套导航语法 |
| **D 只换皮** | 保留 space 就地展开与 L2 模态框，只修列对齐和配色 | F4/F7 原样保留。F4 是"看着晃眼"，F7 是"点了才知道不能续"，都不解决 |
| **D2 rollout 索引文件** | 写终止事件时同步更新索引，清单零扫描 | 制造会漂移的第二份事实，与 §6.4"派生态必须每次重算"的不变量冲突 |
| **S2 卡片化** | 每条会话一张圆角卡 | 每条 3 行高，一屏只剩 6 条，长清单要重做滚动 |
| **S3 反白收敛** | 保留整行反白，只换 62/141 + 前景分层 | 与向导那一族仍不像同一个产品；F3 修了一半 |
| **`app.RolloutStops` 薄封装** | TUI → app → memory 三层转发 | **本文草案里我自己提的，已作废**：`internal/tui` 已经直接 import `internal/memory`，那层转发没有内容 |
</content>


---

## 8. R1 / R2 落地实况（2026-10-17）

分支 `feat-resume-tui`。已交付 §4 的 R1+R2，**未动渲染几何**（F1 仍在，那是 R3）。

### 8.1 真实 API（与草案的差别已就地标注）

```go
// internal/memory/rollout_stop.go（新）
type StopFacts struct{ Found, Finished, Aborted bool
                      StopReason, AbortReason    string
                      Step, MaxSteps             int
                      Checkpoint *AgentCheckpoint }
func ReadRolloutStopFacts(path string) (StopFacts, error)   // 尾读 32 KB → 256 KB → 整文件
func StopFactsOf(info *RolloutInfo) StopFacts               // 草案里没有，见 §8.2
type ResumeState int  // Unknown / Replayable / Handoff / Failed / Finished
func (f StopFacts) State() (ResumeState, string)            // string = 人话原因，可续时为空
```

```go
// internal/tui/tui_resume_stop.go（新）
readResumeStopFacts(items) resumeStopIndex   // WaitGroup + 容量 8 信号量（不用 errgroup）
sortResumeGroupsByState(groups, index)        // SliceStable：可续最前，同档保持 mtime 新→旧
groupRank(index, head, members)               // 取一组里最好的一档（主会话跑完≠这组不值得看）
resumeIndicator(index, item)                  // 行首两格；读不到判据画 `?`，不留白
resumableGroups(index, groups)                // 顶栏计数：分子分母都是"任务"
```

行首词表（单格宽硬约束）：`●` 可原样重放 · `○` 只能交接 · `!` 真失败 · `·` 已收尾 · `?` 无判据。

### 8.2 计划外但必须做的一处：整读侧以前看不见检查点

`rolloutParser.handleEnvelope` 的 `default:` 分支把 `agent_checkpoint` 当普通条目计数，
所以**整读拿不到停点坐标**，尾读能拿到 —— 同一个文件两条路径给出不同判据，
表现是"列表说可原样重放、Enter 之后弹框说不能"。
修法不是在新文件里再写一份解析，而是给共享解析器加 `case RecordAgentCheckpoint:`
并往 `RolloutInfo` 加 `Checkpoint *AgentCheckpoint`，然后 `scanStopFacts` 退化成
"把尾窗的行喂给同一个 parser"。现在**终止字段的回填规则全站只有一份**，
由 `TestStopFactsMatchFullRead` 逐字段钉住（含只有检查点、没有终止事件的夹具）。

### 8.3 一处我自己写错的计数（已修）

顶栏最初写 `1/6 resumable`：分母取的是 `len(resumeRows)`（展开后含子 Agent 文件），
分子取的是"有几条**文件**可续"—— 分子分母不同类，用户数的"条"是任务。
改为 `resumableGroups()/len(resumeGroups)`，措辞 `N/M tasks resumable`。

顺带修掉这个计数引出的一处回归：`renderResumeTitleBar` 的旧实现在空间不够时把右侧键位
提示**剪断**（`rightText[:maxRight] + "…"`）。我加长左侧之后，52–80 列会剪出
`enter: resu…` 这种半截键帽 —— 它会教用户去按一个不存在的组合。改成整段丢弃，
并把优先级显式排成 **标题 > 计数 > 提示**（提示在底栏状态条里本来就写着完整一份）。
宽度也从 `len()` 换成 `lipgloss.Width()`，因为这一屏马上要走 i18n，
届时长度的口径错了就正好复现 F1。闸门：`TestResumeTitleBarDropsHintsInsteadOfCuttingThem`
（W ∈ {40…240} 断言渲染宽度精确相等、无 `…`/`...` 截断痕迹、"提示在则计数在"）。

### 8.4 真实成本（这台机器上 198 份 rollout 实测）

| 做法 | 耗时 |
|------|------|
| 整读全部（`ReadRolloutMemory` ×198，串行） | 314 ms |
| 尾读全部（`ReadRolloutStopFacts` ×198，串行） | 125 ms |
| 尾读全部（并发 8，即 `readResumeStopFacts`） | 37–68 ms 墙钟 |
| 分类分布 | 有终止记录 111 / 198，带检查点 5 / 198 |

这台机器的 rollout 大多不足一个尾窗，所以尾读只比整读快 2.5 倍；
差距会随单个会话体积增长而拉大（整读是 O(文件)，尾读上界是 O(32 KB)）。
37–68 ms 是一次性成本（进 `/resume` 时），换来清单开门就带判据。

### 8.5 闸门

| 用例 | 钉什么 |
|------|--------|
| `TestReadRolloutStopFacts_*` | 尾读三态 + 窗口放大（末尾一条 >32 KB 的 response_item 仍要读到终止事件）+ 缺文件报错 |
| `TestStopFactsState_NoDeclaredKindFallsThrough` | 扫 `rollout_types.go` 里声明的每个 `StopKind*`，未被分类表认领就红。**变异验证过**：塞一个 `StopKindScratchProbe` 进去立刻红；它同时抓到了 `StopKindUnaccounted` 原本走 default |
| `TestStopFactsMatchFullRead` | 尾读与整读对同一份夹具给出同一答案 |
| `TestResumeIndicatorUnknownDiffersFromBlocked` | `?` 与 `○`/`!` 不同形；行首恒 2 格 |
| `TestSortResumeGroupsByStatePutsReplayableFirstAndIsStable` | 排序档位 + 同档内不许二次洗牌 |
| `TestResumeRowKeepsGroupedByStateThenByRecency` | `groupRank` 必须看成员 —— **变异验证过**：改成只看主会话立刻红 |
| `TestResumeIndicatorKeepsRowWidthIdentical` | 加判据前后整行显示宽度不变（R2 的承诺是"不动几何"） |

`bash scripts/check_all.sh` 全绿；`go test -race -count=2 ./internal/tui ./internal/memory` 干净。

### 8.6 留给 R3 的两件

1. F1 仍在：`renderResumeItem` 的 `fixed` 用字节长度、`cleanTitle` 按 rune 截断。
2. 行首符是**该文件自己**的判据，所以"组排序按最好的一档、而组头显示 handoff"会同时出现
   （主会话 idle_timeout 而它的 coding 子文件 paused）。R4 的详情卡要把这层说清楚，
   而不是把组头符号改成"取最好的一档" —— 那会让行首不再描述这一行。

---

## 9. R3 落地实况（2026-10-17）

新文件 `internal/tui/tui_resume_list.go`（631 行）+ `tui_resume_list_test.go`（566 行闸门）；
删掉 `tui_resume.go` 里旧的 357 行渲染块，以及随之变成死代码的 `resumeStyles` 整套样式、
`resumeOffset`、`resumePageSize`（**没有留兼容壳**）。`tui_resume.go` 从 1438 行降到 970 行。

### 9.1 列的成本模型只有一份

`resumeTreeCol + 1 + resumeStateCol + 1 + resumeDateCol + 1` 是基准，
之后**每一列 = 1 格前置分隔 + 自身宽**，preview 拿余量。
分配器 `resumeColumnsFor`、渲染器 `renderResumeRow`、验证器 `resumeUsedCells` 三处读同一套算术。

丢弃优先级（写死在 `resumeColumnSpecs` 的顺序里）：
日期 · 状态符 · **Agent** · **成员计数** · 大小 · 模型 · projectID。
成员计数排在大小之前：`+3` 说的是"这一组还有子文件没展开"，是下一步动作的信息；`3.3 MB` 不是。
阈值不再是常量，而是那段算术的结果（41 / 47 / 56 / 70 / 80），
`TestResumeColumnTiers` 每一档都给"刚到"和"差 1 格"两行。

### 9.2 三条新闸门都做过变异验证

| 闸门 | 变异 | 结果 |
|------|------|------|
| `TestResumeRowNeedsNoLastClip` | 把 preview 的截断从 `common.Ellipsize`（显示宽度）换回旧的 `cleanTitle`（rune 数） | **红**：行尾成员徽标被 `padOrClip` 剪掉 |
| `TestResumeDetailNeverContradictsItself` | 把检查点行的措辞换回"只能带着已有结论（走交接）" | **红**：与状态行 `● replayable` 打脸 |
| `TestResumeHintsKeepTheEssentialKeys` | 把 `⏎ open · q quit` 挪回提示串尾部 | **红**：W=46 的底栏看不出怎么离开这一屏 |

第一条值得单独说：**宽度矩阵本身抓不到 F1**。`padOrClip` 会把越界的部分剪掉，
所以"每行 ≤ 终端宽"在截断口径错掉时依然绿 —— 溢出没有消失，只是换了个伤口
（被剪的是行尾徽标）。所以矩阵之外必须再钉"行宽本来就是算出来的、不需要剪"。

### 9.3 顺手发现并修掉的 F8：叠加层把备用屏关了

`tea.NewView(s)` 的 `AltScreen` 字段是 **false**，而 bubbletea 的渲染器在这个字段翻转时
执行 `enableAltScreen(s, false, …)`（`cursed_renderer.go:314/322`）—— 也就是**退出备用屏**。
旧的 `renderResumeView` 与 `renderHistoryView` 都返回 `tea.NewView`，而主视图返回
`AltScreen: true`。表现不是"样式不对"，是进 `/resume` 就把整个界面掉回主屏缓冲区，
此时任何一行越界都会真的滚进用户的终端历史。两处都改成显式 `tea.View{AltScreen: true}`，
并由 `TestOverlayViewsKeepAltScreen` 钉住（history 全屏自己有一个 `contentHeight=0` 时写
`lines[0]` 的 panic，与本条无关，已在测试注释里点名，没扩大本轮范围）。

### 9.4 语言口径

R3 新增的文案一律**英文**（`▌STOP` / `replayable · step 37/150 · paused` / 键帽行）。
理由：这个 TUI 默认英文、`i18n.go` 的 zh/en 切换是 R5 的活；
在 R3 里硬编码中文等于给默认语言发一份没人翻译的界面。R5 要把它们整批接进表。

### 9.5 还没做的

- **R4 双栏**：现在详情是列表下方的固定 4 行抽屉（单栏形态），
  宽度矩阵里 `< 76` 的退化路径已经按这个形状钉住了；右栏卡片是 R4 的活。
- **R5**：删 `components/resume_dialog.go`、把 `ResumeChoice` 三档搬进详情区、i18n 补齐。
- F7 的判据已经在列表里（R2），但 `ScanResumeCandidates` 那条**整读**路径没有换成尾读
  （它还要 BoardID 等字段），本轮只把分类表合一，没动它的 I/O 形状。
