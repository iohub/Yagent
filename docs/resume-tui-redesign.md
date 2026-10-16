# Resume 界面重设计（主从双栏 + 停点事实进列表）

> 状态：待评审 · 日期 2026-10-16
> 决策（用户已拍板）：**布局 B 主从双栏** / **数据 D1 读尾部检查点** /
> **样式 S1 同族导轨** / **L2 模态确认框取消，内容并进详情面**。
> 本文是实施规格，不是提案；四种布局的取舍见 §10（已否决的备选留档）。

---

## 0. 一句话

`/resume` 现在是仓库里唯一还在用直角框 + 实心青色反白条的列表面，而且实测
**在任何终端宽度下都会吐出一行比终端更宽的内容**。这次改动做三件事：
把几何搬到 `common` 那份内核上（修 5 个布局病）、把"哪条真的能续"这个事实
喂进列表（修可用性问题）、把两层表面收成一层（消灭选中后再弹一次模态框）。

---

## 1. 现状与缺陷（全部实测，非推断）

两层：

| 层 | 位置 | 触发 |
|----|------|------|
| **L1 全屏会话选择器** | `internal/tui/tui_resume.go:1047-1385` | `/resume` `/rs`（`tui_update_commands.go:96`） |
| **L2 模态确认框** | `internal/tui/components/resume_dialog.go` | 选中后 `FinishState()==FinishInterrupted` 才弹（`tui_resume.go:463-470`） |

实测渲染（`renderResumeView` 真实输出，110 列）：

```
┌──────────────────────────────────────────────────────────┐
 Resume Session     Page 1/1          esc: back  enter: resume  r: refresh
● 10-17 03:16  [7f3a2b] main glm-4.6  browser agent 的 CDP 端口冲突排查
    sess-T3d · /Users/ydl/iohub/yagent · git:main · 14.6 KB
  10-16 23:46  [7f3a2b] main gpt-4.1  为什么压缩阈值取小之后 TUI 面板百分比不对
▸   10-16 22:16  [7f3a2b] main qwen3-coder  重构 TUI 的 resume 界面，…  +2
── Page 1/1 · 1-3 of 3 ──
└──────────────────────────────────────────────────────────┘
```

### F1 几何是坏的（不是丑）

一个临时闸门跑 `renderResumeView`（跑完已删）：**每个宽度下都至少有一行超出终端宽度**。
根因是两处：

- `tui_resume.go:1236` `fixed := 2 + len(dateStr) + 2 + len(proj) + len(agent) + 1 + len(modelStr) + 2`
  —— **字节**长度；
- `cleanTitle`（`tui_history.go:561-579`）按 **rune** 截断，1 个中文 = 2 列。

| 终端宽 | 预览 ASCII | 预览中文（本项目常态） |
|--------|-----------|----------------------|
| 60  | 超 1 列 → 总行数 21 > 20 | 超 4 列 |
| 76  | 超 1 列 → 25 > 24 | 超 12 列 |
| 100 | 超 1 列 → 31 > 30 | 超 **35** 列 |

超出 1 列的那一行固定是展开后的成员行（`  └ ` 前缀那条）。后果不是"难看一点"，
而是终端软换行 → 整屏多顶一行 → **AltScreen 下底框被推出可视区，且滚不回去**
（AGENTS §11 坑 30 同族）。

### F2 列不对齐，无法纵向扫读
预览起始列随 model 名长度漂移；`▸ ` 前缀把有子文件的行整体右推 2 列。

### F3 选中即失去层次
`indicatorStyle`/`dateStyle`/`titleStyle` **三个样式的前景/背景完全相同**
（`tui_resume.go:38-51`：都是 36 底 / 0 前景 / Bold）→ 选中行内部零层次。

### F4 游标一动，列表几何就变
详情行只在选中那条**后面**插一行（`tui_resume.go:1163-1165`），
于是移动光标时它下面的所有行上下跳一格。

### F5 分页两套数，静默吞行
窗口用 `m.resumePageSize`（20，`tui_resume.go:300-323`），正文用
`effectivePageSize`（夹到 contentHeight，`tui_resume.go:1062-1068`），
短终端下靠 `if len(lines) >= height { break }` 截断，**没有 `↓ N more`**。

### F6 没有侧边框 + i18n 断层
只画顶/底两条横线，内容行宽 = 终端宽 ⇒ 是"上下各一根线"的表而不是面板；
标题/状态栏文案硬编码英文，而 L2 是双语。

### F7 列表无法回答"哪条值得续"（唯一影响可用性的缺陷）
`memory.RolloutSessionInfo`（`rollout_sessions.go:37-64`）只有
`Model/CWD/GitBranch/Preview/SizeBytes/StartedAt` —— **没有 StopReason**。
而 `agents.ScanResumeCandidates`（`internal/agents/resume.go:189`）早已返回
`{StopKind, Step, MaxSteps, Resumable, Unresumeable, BoardID}`，TUI 一次都没用。
用户只能挨个 Enter 试，才发现"这条其实是跑完的"。

---

## 2. 目标形状

**一屏三层**：同族导轨（顶/底）+ 左列表（34 列）+ 右详情卡。L2 取消后，
"续跑哪一档"变成详情卡里的一个**内联选择块**，`⏎` 直接落定。

### 2.1 顶栏

`renderPanelTop(label, meta, panelW, border)` —— 与向导、输入面板逐字节同源。

- label：`RESUME · <N> sessions`
- meta：`<可续> resumable · <不可续> blocked`（判据来自 §3，不是"有几个文件"）
- 边框色随"这一屏有没有可续的停点"：有 = `141`（Accent，等待决策），
  无 = `240`（Border，只是在看历史）。沿用 `setup_render.go:114-127` 的
  "颜色是第二重状态指示"先例。

### 2.2 左列表：行语法

```
[树 1][状态 1] [时间 16] [分支 14] [模型 12] [大小 8]  预览…
```

列宽由 `common.AllocateColumns` 分配、截断一律 `common.Ellipsize`
（**显示宽度**，不是 rune 数）—— 这是对 F1/F2 的结构性修复。

- **状态列恒定存在**（无子文件时也是 1 列空格 + 1 列状态符）→ 修 F2。
  词表：`●` 可原样重放 / `‖` 已取消可试 / `✗` 需交接（不可重放，附原因）/
  `✓` 正常收尾 / `!` 真失败（llm_error·panic·熔断）/ `·` 未知（存量 rollout）。
  判据是 `Replayable`+`WhyNot`，不是猜。
  选字硬约束：**禁 Emoji_Presentation 或 EA=W 的符号**（`⏸` U+23F8、`⚡` U+26A1
  在多数终端占两列，会把固定列整行顶歪）。本表全部为 N/A/Na，与仓库既有的
  `● ▸ ▾ ✓ ×` 同一档。
- 选中态：**行底 236 + 左侧 1 列 `▌` 用 141**，行内前景分层
  （时间 244 / 分支 80 / 模型 248 / 预览 252）→ 修 F3，不再整行糊成一块。
- 树形保留 `▸/▾`，但箭头**与状态符分列**，展开不再移动正文列。

### 2.3 右详情卡

一张 `cardStyle`（圆角 + `SurfaceRaised`）卡，五个发丝分节（`sectionRule`）：

1. **停点**：`停在 Director step 12/100 · idle_timeout` + 一句可续性判据
   （"检查点指纹相符 · 工作区未移动" 或 `Unresumeable` 原文）。
2. **账目**：model / branch / cwd / session / msgs / tool calls / size / thinklink 块数。
3. **委派树**：`✓ delegate_repo`、`● delegate_coding step 37/150`、`○ 未派发` —— 
   来自 `ScanDelegationStops`（`tui_resume.go:542` 已在用）。
4. **未完成计划**：thinklink T&P 的 `[ ]`/`[>]` 行（`unfinishedPlanItems`，现成）。
5. **风险**：悬空工具调用 + 一句"重放会重做这些调用，副作用需核实"
   （`DanglingToolCalls`，现成）。

底部**内联选择块**取代 L2 的三个按钮，每项 = `标签 + 一句后果`：

```
▸ 从 coding step 37 续跑   重放子 Agent 账目，不重做已完成的委派
  从 Director 续跑         带已有结论重新规划，可能重做一次委派
  仅浏览                   不动任务；按 e 导出这份停点报告
```

`tab`/`←→` 换档、`⏎` 确认、`esc` 退出 —— **键盘语义与 L2 完全一致**，
所以 `ResumeChoice` 三档枚举和 `buildResumeContinuationPrompt` 都能原样复用。

### 2.4 高度不变量（对 F4/F5 的闸门）

- 详情卡**固定 = 左列表可见窗口高**，行预算在渲染前算好，内容不足补空行、
  超出走 `common.MoreLine`；
- 游标移动**只改右卡内容，不改左列表几何**（这是 B 相对 A 的核心收益）；
- 整块过 `fitLines`，总高过 `trimToHeight` 同族闸门；
- 分页窗口和正文用**同一个** pageSize（消掉 F5 的两套数）。

### 2.5 底栏

`renderPanelBottom(hints, panelW, border)`，键帽用 `common.KeyHintStyle`（235 底/250 前景）。
`hints()` 按焦点侧（列表 / 选择块）给**一组**，不在两处重复。全部经 i18n 表
→ 修 F6。

### 2.6 退化

`inner < 76` 时收成单栏：详情卡降到列表下方、固定 6 行预算；
`inner < 52` 时只留列表 + 顶栏 meta 摘要。两种形态共用同一套行渲染函数。

---

## 3. D1：把停点事实喂进列表

`ScanResumeCandidates` 今天对每个文件跑 `ReadRolloutMemory`（**全量读**），
直接搬到清单上 = O(总字节)。清单要的是常数级信息，所以新增一条**尾读**：

```go
// internal/memory/rollout_stop.go（新）
type StopFacts struct {
    StopReason string   // 词表在 rollout_types.go
    Step       int
    MaxSteps   int
    Checkpoint *AgentCheckpoint // 有则非 nil：重放坐标
    Aborted    bool
}
func ReadRolloutStopFacts(path string) (StopFacts, error)
```

- 从文件尾反向取最后 64 KB 的完整行（不解析半行），扫到第一条
  `agent_checkpoint` + 终止 `event_msg` 即返回；
- 尾读失败（文件 <64 KB / 无终止记录）回落 `StopFacts{}` + `slog.Debug` ——
  **不能**因为读不到就滤掉这条会话（`ScanResumeCandidates` 的注释已经把这条
  写成不变量：静默过滤是 §13 禁止的事）；
- 分类复用 `agents/resume.go` 里那张 `switch kind`（默认不可重放），
  把它从 `ScanResumeCandidates` 抽成 `classifyCandidate(StopFacts) ResumeCandidate`
  供两条路径共用 —— 一张表两份实现必然漂走（AGENTS §11 坑 18）。

TUI 侧新增薄封装（`internal/tui → internal/app → internal/agents` 已有先例
`app.ResumableDelegation`，TUI 不许 import `internal/agents`）：

```go
// internal/app/app.go
type RolloutStop struct { AgentName, StopKind string; Step, MaxSteps int
                          Resumable bool; WhyNot string }
func (ca *Yagent) RolloutStops(dir string) map[string]RolloutStop // key = rollout path
```

`loadResumeListCmd` 拿清单后**并发**补停点，合成一个 msg 回投。并发用
`sync.WaitGroup` + 容量 8 的 channel 信号量即可 ——
`golang.org/x/sync` 在 go.mod 里是 **indirect**（`go.mod:86`），为这点便利把它
提成直接依赖不值当（会多一条 require 与一次 tidy 差异）。
尾读**不做**"读不到就滤掉"（上面那条不变量）。**不建索引文件**：那是会漂移的
第二份事实（§10 D2）。

排序改为：`Resumable` 优先 → 其后 `⚡ 需交接` → 最后 `✓/!`，同组内仍按 mtime 新→旧。
"全部 5 条"这类计数进顶栏 meta。

---

## 4. L2 的去留

`components/resume_dialog.go` 删除；`ResumeChoice` /
`buildResumeContinuationPrompt` / `pushResumeDialog` 的**语义**搬进 TUI 侧新的
`resumeDecision` 结构。`restoreFromRollout` 里那条
`if state == FinishInterrupted { pushResumeDialog(...) }` 改成
"进入 resume 选择器时直接把详情卡渲染出来"，不再二次弹窗。

被取消的旧路径不能静默消失：`SetAgentOption` 那条"只有主会话 rollout 才谈得上
停在哪个委派"的判据（`tui_resume.go:540-547`）原样保留在选择块里 ——
子 Agent 文件没有第三档。

---

## 5. 测试闸门

新增 `internal/tui/tui_resume_render_test.go`，形状照 `TestWizardRenderWidthMatrix`：

1. **宽度矩阵**：W ∈ {52,60,76,92,120,240} × H ∈ {12,18,24,40} ×
   焦点侧 ∈ {列表, 选择块} × 折叠 ∈ {收, 展}，断言
   **每行 `lipgloss.Width ≤ W`** 且**总行数 ≤ H**。
   夹具必须**同时**含纯 ASCII 预览与中文预览（F1 的两条触发路径），
   且必须至少有一条会话带子 Agent 成员（`  └ ` 那一行是历史越界点）。
2. **夹具前置条件**：断言"这份夹具确实超出了预算"，否则又是一个
   永远不会失败的测试（AGENTS §8.3）。
3. **游标不动几何**：游标在第 1/2/3 行之间移动，左列表行数必须逐字节相同（钉 F4）。
4. **停点分类**：`classifyCandidate` 对每个 `StopKind` 都有一个能响的用例
   （坑 15：新增校验必须同时写"它必须报"的夹具）。
5. **尾读**：`go test ./internal/memory -run RolloutStop` 钉
   "尾部有 checkpoint + 终止事件 → 读得到"、"只有终止事件"、"什么都没有"
   三条；文件用 `t.TempDir()`，**不碰 `~/.yagent`**（坑 23：internal/memory 不隔离 $HOME）。
6. **选择块键帽**：反向钉住底栏写的每个单键都真能改状态
   （`TestWizardHintsAreNotDecoration` 同族）。
7. 现有 `tui_resume_restore_test.go` / `resume_dialog*_test.go` 随 L2 删除而迁移，
   不允许留 `t.Skip` 空壳（`scripts/check_test_hygiene.sh`）。

---

## 6. 实施顺序

| 步 | 内容 | 风险 |
|----|------|------|
| R1 | `memory.ReadRolloutStopFacts` + `classifyCandidate` 抽取 + 单测 | 低（纯新增，旧路径不动） |
| R2 | `app.RolloutStops` 薄封装 + `loadResumeListCmd` 并发补停点 + 排序/计数 | 低 |
| R3 | 单栏几何修好（`AllocateColumns`/`Ellipsize`/恒定列/单一 pageSize/`fitLines`）——**先把 F1~F6 修死**，此时还没有右栏 | 中：宽度矩阵会立刻红，正好当回归网 |
| R4 | 双栏壳 + 详情卡 + 高度不变量 + `<76` 退化 | 中 |
| R5 | 内联选择块 + 删 `resume_dialog.go` + 迁移测试 + i18n 补齐 | 高：动了确认路径 |
| R6 | AGENTS.md §6.7/§6.8 增补（双层→单层、停点判据、几何闸门） | 低 |

R3 单独可交付：即使 R4/R5 被砍，几何修复也已经把用户实际遇到的坏行为解决了。

---

## 7. 明确不做

- 不做搜索/筛选轨、不引入 `tab` 换栏的三焦点模型（那是 §10 的 D）；
- 不建 rollout 索引文件（§10 的 D2）；
- 不动 `/resume` 的会话级/委派级恢复语义与 B/C 层重放判据；
- 不在 TUI 里做"删除会话"这类新写操作。

---

## 8. 目标形状示意（W=100）

```
╭─ ▌RESUME · 5 sessions ──────────────────── 2 resumable · 1 blocked ─╮
│                                                                    │
│ ╭ TASKS ──────────────╮ ╭─▌SESSION 10-16 22:16 · T1 ──── 42m ago ─╮│
│ │ ▸   10-17 03:16  glm │ │  停在 Director step 12/100 · idle_time… ││
│ │ ▾ ● 10-16 22:16  qwe │ │  可原样重放：检查点指纹相符 · 工作区未动 ││
│ │     重构 resum…    +2│ │ ── 账目 ──────────────────────────────── ││
│ │   ‖ 10-15 19:02  gpt │ │  model qwen3-coder   branch main        ││
│ │   ✓ 10-14 11:40  dee │ │  cwd ~/iohub/yagent  sess 4f9c2a1b      ││
│ │                      │ │  128 msgs · 41 calls · 3.3 MB           ││
│ │                      │ ├─ ▌停点（委派树）────────────────────────┤│
│ │                      │ │  ✓ delegate_repo   梳理边界    1.1 KB   ││
│ │                      │ │  ● delegate_coding step 37/150 可重放   ││
│ ├─ ▌未完成计划 ────────┤ │  ○ next            跑宽度矩阵   未开始  ││
│ │ [>] 重写列宽分配     │ ├─ ▌风险 ─────────────────────────────────┤│
│ │ [ ] 详情常驻         │ │  悬空 search_replace_in_file · run_bash ││
│ │ [ ] 宽度矩阵         │ │  → 重放这 2 次调用，副作用需核实        ││
│ ╰──────────────────────╯ │ ▸ 从 coding 37 续跑  重放，不重做委派   ││
│                          │   从 Director 续跑  重规划，可能重做    ││
│                          │   仅浏览            e 导出停点报告      ││
│                          ╰─────────────────────────────────────────╯│
╰─ j/k 移动 · tab 选项 · ⏎ 确认 · e 导出 · q 退出 ──────────────────╯
```

（示意按显示宽度对齐，实现以 §2 的列定义与 §5 的矩阵闸门为准。）

---

## 9. 验收

1. `bash scripts/check_all.sh` 全绿。**注意 `check_file_sizes.sh` 只守
   `internal/agents/director/`**（`TARGET_DIR` 写死），`internal/tui` 没有任何行数守卫 ——
   所以这次拆分靠自觉：`tui_resume.go` 已经 1409 行，新渲染层必须另起文件
   （`tui_resume_list.go` / `tui_resume_detail.go` / `tui_resume_stop.go`），
   否则它会长成下一个没人敢碰的巨物，而没有任何测试会拦；
2. 宽度矩阵在 {52…240} 全部零越界、零超行；
3. `go test ./internal/memory ./internal/agents -count=1` 绿；
4. 真机：`./yagent` → `/resume`，在有子 Agent 的长会话上移动游标，
   左列表**一行都不跳**；不可重放的会话能看到原因而不是点了没反应；
5. 无 TTY 环境下明说测不到（本仓约定）。

---

## 10. 已否决的备选（留档，免得下次重走）

| 方案 | 描述 | 否决理由 |
|------|------|---------|
| **A 单栏 + 就地抽屉** | 一栏，详情用缩进抽屉跟在选中行后 | 抽屉要么重现 F4（几何随游标变），要么固定高度吃掉列表 8 行；窄终端友好是它唯一的优势，最后只在 `<76` 退化形态里保留它 |
| **C 列表 + 决策抽屉** | 一栏 + 底部常驻 DECIDE 卡，消灭模态框 | 与 B 同样能消灭 L2，但放不下整份停点账目（§2.3 的第 2、3 节）。B 已含 C 的全部收益 |
| **D 三栏工作台** | SCOPE 筛选轨 + 列表 + PREVIEW | 实测需 ≥105 列；且引入第二套焦点环（`tab` 换栏），与 timeline 全屏的 `h/l` 模型并存 = 长期两套导航语法 |
| **D2 rollout 索引文件** | 写终止事件时同步更新索引，清单零扫描 | 制造会漂移的第二份事实，与 §6.4"派生态必须每次重算"的不变量冲突 |
| **S2 卡片化** | 每个会话一张圆角卡 | 每条 3 行高，一屏只剩 6 条，长清单要重做滚动 |
| **D0 只用现有字段** | 只换皮，判据仍在 Enter 之后 | F7 原样保留 —— 列表依然无法回答"哪条值得续"，这次重做的收益砍掉一半 |
