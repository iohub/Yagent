# 引导向导美化：阶段轨 + 渐进表单 + diff 摘要

> 范围：`internal/tui/setup*.go` 的**呈现与按键层**。状态机（`setupWizard` 的 op 队列）、
> `internal/config` 的写回内核、探测的请求形状**一律不动** —— 这样"纯界面改动"是可以被证明的：
> `go test ./internal/config/...` 的结果必须逐字节不变。
>
> 决策已拍板（2026-10-16）：**A2 左侧阶段轨 / B2 三段渐进表单 + 目录预填 / C2 spinner + 人话 diff 摘要**。
> 界面文案继续用英文（与既有 10 个对话框一致，见 §8）。

---

## 0. 决策记录

| # | 决策点 | 选定 | 放弃项与代价 |
|---|--------|------|--------------|
| DA | 骨架 | **A2 左侧阶段轨 + 右侧内容**，`inner < 60` 折叠为居中单栏 | 放弃 A1（只换令牌，进度仍是会撒谎的 `1/7`）与 A3（贴底 stepper，视觉分量最弱）。代价：多一套列宽数学，窄终端必须有降级分支 |
| DB | 进度语义 | 轨道的"已完成"**由账本推导，不由步骤游标推导** | 放弃"走到第几屏就算第几段"。探测失败退回表单时，游标会倒退而事实没倒退 —— 用游标渲染就是在撒谎 |
| DC | 表单 | **B2 三段渐进**：必填 3 行常驻，可选 6 行 `o` 折叠；`p` 端点预设；model 命中目录自动填 `context_window`；`f2` 合并 key 来源 | 放弃 B1（9 行全展开）与 B3（一屏一问，3~9 次回车且要重写 `setupStep`）。代价：`enter = 存草稿` 的落点随折叠区变化，必须写死规则（§3.3） |
| DD | 探测 | **自实现 `tickCmd` spinner**，不接 `internal/tui/anim` | `anim.Manager` 全仓零引用（`grep -rn "tui/anim"` 空），把正在交付的界面接到没有测试的包上是新风险。代价：帧计数逻辑多 20 行 |
| DE | 摘要 | **C2 人话 diff**，`op.note` 降为次级 dim 行**保留不删** | 因为 `applyOps` 的失败信息就是 `op.note` 原文（AGENTS §11 坑 28），删掉它 = 写失败时用户不知道是哪条没落盘 |
| DF | 共享层 | 把 `components` 的四个私有列表原语**上提到 `internal/tui/common`**，对话框与向导共用同一份 | 放弃"在向导里再写一份 `scrollWindow`/`moreLine`/`allocateColumns`/`dialogWidths`"。同形两份实现正是 AGENTS §11 坑 18 点名的病。兜底：现有 4 个渲染测试（§6.3） |
| DG | 范围外 | 不做"写后立即生效"（原方案 §6.2 承诺的 `SwitchProvider`/`SetLLMTimeout`/`SetRoutingMode` 复用） | `setup_embed.go:79` 那句"restart yagent"**继续留着并如实说明**。它仍然是未兑现的承诺，另开一次改动 |

---

## 1. 现状事实（逐条已核实）

`docs/config-trim-onboarding-design.md` §6.3 第 4 条要求"排版只用现成令牌：`common.DialogBorderStyle`、
`renderPanelTop/renderPanelBottom`、`KeyHintStyle` 键帽"，第 2 条要求 textarea "样式沿用 `newTaskInput`"。
实现两条都没做。

| # | 事实 | 位置 | 后果 |
|---|------|------|------|
| 1 | 自造 `setupTop/setupBottom`，边框整行硬编码 `141` | `setup_render.go:410-436` | 与 10 个对话框不同族；`renderPanelTop` 的 `▌` 渐变标签、`meta` 前的 `─` 填充全都没用上 |
| 2 | `hints()` 返回裸串 `"enter start · q quit"`，从不渲染键帽 | `:376-394` | 全 TUI 唯一没有键帽的表面；`setupPalette.hint` 定义了却从未使用 |
| 3 | `setupPalette` 的 `border`/`title`/`hint` 三个字段是死代码 | `:110-137` | `setupTop` 另外 `NewStyle()`，令牌与消费者分家 |
| 4 | 正文 `JoinVertical(Left)`，面板宽 `min(width-2, 96)` | `:53` | 200 列终端下 96 宽面板死贴左边缘；对话框一律 `lipgloss.Place(...,Center,Center,...)` |
| 5 | `w.height` 赋值后从未被读 | `setup.go:116`、`setup_render.go:512` | 无行数预算、无滚动窗口。24 行终端 + 摘要屏 8 条 op + 长错误 = 溢出到 AltScreen 之外，用户看不到也滚不回去 |
| 6 | 游标是 `c.cursor.Render("> ")` | `:204-207`、`:328`、`:352` | 家族用 `common.CursorIndicatorStyle` + `"▶ "` + 焦点行整行加粗（`model_select_dialog.go:256-266`） |
| 7 | 徽章是文本 `" [default\|queued]"` | `:209-218` | `common.BadgeStyle` 是填充块；扫视时认不出哪行是默认档 |
| 8 | 表单 textarea 用 bubbles 默认样式 | `setup_keys.go:47-53` | 主输入框（`tui_model.go:648-679`：base bg 235 / 焦点 prompt 39 / 光标 39 bar / placeholder italic 243）比配置表单好看 |
| 9 | 表单每行**两个游标** | `setup_render.go:283-284` + `tui_model.go:681-686` | 向导自己画 `> `，而 `SetPromptFunc(2, …)` 让 textarea 自己印 `❯ `，两者叠加 |
| 10 | 探测屏是静态字 `"contacting the endpoint..."` | `:306` | 30s 预算里界面完全不动，看起来像卡死 |
| 11 | 摘要屏逐条打 `op.note` 原文 | `:369-371` | 最后一屏全是 `write [global.llm.providers.mimo]`，没有变化前后，只有键路径 |
| 12 | 首启屏每项两行、值槽固定 22 列、标签固定 14 列 | `:350-357` | `llm timeout` 被 `padTo(label,14)` 挤在 14 列里，说明文字比值还长；5 行吃 10 行正文 |
| 13 | `providerList` 在 `inner < 64` 时**整列丢掉 KEY** | `:188`、`:223-225` | 恰好在空间最紧的时候丢掉"这个 provider 到底有没有密钥"这条首启最关键的信息 |
| 14 | §6.2 承诺但没实现：`数字键快捷选`、`Enter 设为默认` | `docs/config-trim-onboarding-design.md:245` | `enter` 现在是"编辑"。本方案定：**enter 保持编辑**，新增 `s` = 设为默认（见 §5），文档随本次更正 |
| 15 | `TestWizardRenderWidthMatrix` 只测宽度不测高度 | `setup_test.go:327-354` | 事实 5 的溢出从来没被抓到过 |

---

## 2. 轴 A：骨架

### 2.1 布局

```
╭─ ▌YAGENT SETUP ── PROVIDERS ──────────────────────────────── 2 of 4 ─╮
│                                                                    │
│   ● PROVIDER ✓        ┌────────────────────────────────────────┐   │
│   ○ DEFAULT           │   PROVIDER  MODEL         BASE      KEY│   │
│   ○ CHOICES           │   ▶▒ mimo   ▒ qwen3-max   ▒ api.x  ▒ $MIMO_KEY
│   ○ REVIEW            │     aliyun   qwen3-coder  dashscope  your-key…
│                       └────────────────────────────────────────┘   │
│                                                                    │
│  ✗ model is required                                               │
│  ⓘ nothing is written until enter on REVIEW                        │
╰─ a add · e edit · s default · t test · o options · c continue ─────╯
```

- **整屏 `lipgloss.Place(w, h, Center, Center, panel)`**，面板宽 `min(w-2, 104)`。
  `Place` 之后每行宽度恰好 = `w`，宽度矩阵测试天然通过（但仍保留逐行断言，见 §6）。
- 外框用 `common.DialogBorderStyle(c)` + 顶栏 `renderPanelTop(label, meta, width, borderColor)`、
  底栏 `renderPanelBottom(hints, width, borderColor)` —— 这三个都在 `package tui`，向导**直接调用**，
  不需要重写。删掉 `setupTop`/`setupBottom`/`setupMinWidth` 之外自造的那套。
- **边框色随屏义变化**（家族做法，`tui_chrome.go:26-33` 的先例）：
  浏览类屏 `Primary(39)`、表单屏 `62`、探测进行中 `76`、探测失败/写失败 `Danger(196)`、摘要 `Accent(214)`。
- 键帽底栏要求 hints 串是 `"key desc · key desc"` 格式（`renderHintBar` 按 `" · "` 切、
  `renderHintItem` 取首词做键帽）。现有 `hints()` 已经是这个形状，**换成现成函数几乎免费**。
  宽度不够时 `renderHintBar` 从尾部整段丢弃，不再需要向导自己的截断分支。

### 2.2 阶段轨（DB）

7 个 `setupStep` 映射到 4 段，映射是**纯函数**，不新增状态字段：

| 段 | 覆盖的 step | 完成判据（从账本推导） |
|----|-------------|------------------------|
| `PROVIDER` | `stepProviders` / `stepProviderForm` / `stepProbe` | `len(w.effectiveProviders()) > 0` |
| `DEFAULT` | `stepDefault` | 存在 `kind=="default"` 的 op，**或** `snap.useProvider` 命中现有 provider 且没有 provider 被排队删除 |
| `CHOICES` | `stepFirstRun` | 存在 `kind=="firstrun"` 的 op |
| `REVIEW` | `stepSummary` | `w.saved` |

符号：`●` 当前段（`Primary`）、`✓` 已完成（`Success`）、`○` 未开始（`TextMuted`）。
`stepWelcome` 不占段，轨道整条不显示，改为居中说明块。

为什么判据不能是游标：`providers → form → probe → providers → default` 这条真实路径里游标来回跳，
"3/7" 会既倒退又不反映"其实已经配完了"。DB 这条是本次唯一改变**信息正确性**（而非观感）的决定。

### 2.3 高度预算与折叠（修事实 5）

- `railW = 20`，`gutter = 2`；`inner < 60` 时轨道**整条折叠**，退回 A1 形态：标题行渲染
  `PROVIDERS · 2 of 4`，正文全宽。折叠判据只看 `inner`，不看 `width`，避免 96 面板 + 20 轨道在 100 列终端上溢出。
- `bodyBudget = h - 顶栏 - 底栏 - extras(错误/提示行数) - 2`。正文超出就走滚动窗口
  （上提后的 `scrollWindow` + `moreLine`），并提供 `j/k` 滚动、`g/G` 跳首尾 —— 只在 `summary` 与 `providers`
  两个可能长的屏生效，其余屏条目本就 ≤ 9。
- `h < 18` 时给一行"请把终端拉高到至少 18 行（现在 %d）"，与既有 `setupMinWidth` 的处理对称
  （事实：现在只有宽度分支，高度分支根本不存在）。

---

## 3. 轴 B：表单

### 3.1 三段

```
╭─ ▌YAGENT SETUP ── NEW PROVIDER ─────────────── 3 required · 6 optional ─╮
│                                                                        │
│  ❯ name          gateway                                               │
│  ❯ api_base_url  https://gateway.example/v1        p endpoint presets  │
│  ❯ api_key       sk-•••••••••••••••••••           f2 use $ENV instead  │
│  ❯ model         qwen3-coder-plus                                       │
│    catalog ✓ context 1,000,000  →  context_window prefilled            │
│  ────────────────────────────────────────────────────────────────────  │
│    ▸ 4 optional fields  (ctrl+o opens them, tab reaches them)           │
│                                                                        │
│  ⓘ leave a field empty to omit it — no zero is ever written            │
╰─ ⇥ next · enter next (last = save) · ctrl+s save draft · esc back ─────╯
```

- **必填 4 行**：`name` / `api_base_url` / 密钥（`api_key` ⇄ `api_key_env` 同一行）/ `model`。
  前三条判据来自 `queueProviderOp`（名字合法、`model` 非空、密钥两种至少一种）。
  ⚠️ 落地时发现 `api_base_url` **不在旧校验里**：`config.WriteProvider` 只要求"URL 与密钥不全空"
  （`config/setup.go:59`），而 `validate()` 要求每个 provider 都有 URL（`config.go:592`）——
  于是"填了 key 忘了 URL"能一路放行到落盘，下一启动直接加载失败，正是这个向导存在的理由。
  本轮把它补进校验，并且必填分组与校验集合由 `TestWizardRequiredGroupMatchesValidator` 双向钉住：
  清空任一格必须被表单拦下，四格全填必须能被 `config.WriteProvider` 接受。
- **可选 6 行**：`api_format` / `reasoning_effort` / `max_tokens` / `context_window`（+ 已有的
  `placeholder` 语义不变）。温度继续不进表单（`setup_keys.go:16-18` 的理由成立：裸 `float64`
  分不清"0.0"与"没写"，而显式 0 会顶成贪心解码）。
- 折叠区收起时**不参与 `tab` 循环**，`fieldIdx` 也不可能落在隐藏行上（隐藏行走 `Blur()`），
  否则会出现"焦点在一个看不见的输入框里"。
- ⚠️ 落地时改的主意：**模态键必须带修饰符**。方案原本用裸 `o` / 裸 `p`，实测后果是填
  `https://…` 时 `ps` 被吃掉、还莫名打开了预设列表 —— 表单每一格都是活的 textarea，
  裸字母必须先送进它。最终形状：`ctrl+o` / `ctrl+p` / `f2`（与主 TUI 用 `alt+s` 提交同理）。
  这条也写进了 AGENTS.md §11 坑 31。

### 3.2 预填的两条来源（都不新增配置面）

- **目录 → `context_window`**：`models.ContextWindow(model)` 命中且该行为空（或仍是上一次自动填的值，
  用 `ctxAutoFilled bool` 记）时写入。手输过就绝不覆盖。`catalogHint` 从一行扩成实况行：
  命中 → `catalog ✓ context 1,000,000`；未命中 → `catalog ✗ not in the embedded catalog: the compression
  threshold falls back to 120000`（现有措辞照搬，它已经说清了后果）。
- **端点预设 → `api_base_url` + `api_format` + 建议 `model`**：`p` 打开内嵌预设列表
  （DashScope/Bailian、local vLLM、Anthropic native、DeepSeek、SiliconFlow、Moonshot、Z.ai、OpenRouter、Mistral —
  与 `default_config.toml:12-16` 已列的厂商一致，Anthropic 走 `api_format = "anthropic"`）。
  预设**只填空字段**，绝不覆盖已有值；选中后焦点停在 `api_key`。
  数据放新文件 `internal/tui/setup_presets.go`，一份 `[]endpointPreset{Name, BaseURL, APIFormat, Model}`。
  注释必须写明：**URL 会过期，探测屏是唯一事实** —— 预设是省事，不是权威。
  `internal/models` 不提供 `base_url`（目录只有 `limit.context` 与 benchmark），所以这份表只能是手写的，
  不要把它伪装成从目录推导。

### 3.3 `enter` 的落点（DC 的代价，写死）

- 必填区：`enter` = 下一行，停在 `model` 上按 `enter` = 存草稿（`queueProviderOp`）。
- 展开可选区后：`enter` = 下一行，停在**最后一行**按 `enter` = 存草稿。
- `ctrl+s` 在两个区都随时可存草稿，语义不变。
- `o` 只切换可见性，**不移动焦点、不改草稿**。
- 规则的唯一来源是 `w.lastVisibleField()`，两个分支都读它 —— 不在 `handleFormKey` 里再抄一次
  `len(w.inputs)-1`（现状 `setup_keys.go:253` 正是硬编码这个）。

### 3.4 key 来源合并成一行（`f2`）

- 一行两态：`api_key`（遮罩态）⇄ `api_key_env`（`$VAR` 态）。切换时把当前输入搬到目标字段、
  清空另一个，于是"两个字段都有值、后者胜"这条只能靠 hint 说明的隐式规则消失
  （现状 `setup.go:318-329` + `setup_render.go:258-259`）。
- 遮罩策略：**焦点行明文、非焦点行遮罩**（`sk-…` + 末 2 位，与列表屏 `maskKey` 同形）。
  全程遮罩会让用户看不见自己打错的 key，全程明文则把密钥摊在 100 列终端里 —— 只有焦点态能两者兼顾。
- `api_key_env` 态下变量未设时保留现有的**不拦截 + 提示**行为（`note: $X is not set in this shell`），
  因为它对"写了 .env 再启动"的人是合法的；探测屏会诚实失败并点名该变量。

### 3.5 textarea 令牌（修事实 8、9）

按 §6.3 第 2 条改用 `newTaskInput` 的同一套：base bg `235`、焦点 prompt `39` 加粗、
`❯ ` 前缀（`SetPromptFunc(2, …)`）、placeholder italic `243`、光标 `39` bar + 400ms blink、
`SetVirtualCursor(true)`（已有）。向导行的自有 `> ` 指针**删掉** —— `❯` 已经承担了"这格在聚焦"，
再加一个就是两个游标。焦点行的标签同时升到 `TextPrimary` + 加粗，非焦点行 `TextMuted`。

---

## 4. 轴 C：探测与摘要

### 4.1 探测屏

```
  ⠹ contacting api.gateway.example  ·  model qwen3-coder-plus
      2.4s elapsed · 30s budget · max_tokens 16, no tools
    waiting for a real round-trip — nothing here is simulated
```
成功后：
```
  ✓ answered in 812ms
    reply: ok
```
失败后（原文逐字，另加一行归类）：
```
  ✗ HTTP 401: invalid api key …（provider 原话，折行进面板）
    → 401 usually means the key, not the URL. Re-check api_key with f2/$ENV.
```

- spinner 用 `tea.Tick(120*time.Millisecond, …)` + 帧表 `⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏`（DD）。
  `probeResultMsg` 已经把 `probing` 置回 false，所以 stop condition 现成。
- 目标主机从 `api_base_url` 解析后显示，**scheme+host 不显示 path 与 query**，
  避免把带 token 的 URL 摊在屏幕上。
- 归类行只是**追加**，provider 原文一字不改（§6.2 明确要求"不做美化"）；
  词表限 401/403 → key、404 → base_url、timeout/`context deadline` → `[llm] timeout` 与网络，
  认不出就不给归类行（宁缺毋滥的猜测比不猜更糟）。

### 4.2 摘要屏（DE）

```
╭─ ▌YAGENT SETUP ── REVIEW ──── 4 changes · nothing written yet ─╮
│  ~/.yagent/config/config.toml                                 │
│                                                             │
│  PROVIDERS                                                  │
│  + gateway        qwen3-coder-plus · api.gateway.example    │
│      write [global.llm.providers.gateway]                   │
│  − aliyun         (was: qwen3-coder-plus)                    │
│      delete [global.llm.providers.aliyun]                    │
│  DEFAULT                                                    │
│  → use_provider   local → gateway                            │
│      use_provider = "gateway"                               │
│  CHOICES                                                    │
│  ~ language       Chinese → English                          │
│  ~ autonomy       ask for approval → yolo (-y)               │
│                                                             │
│  ⚠ $GATEWAY_KEY is not set in this shell                     │
│  ⓘ restart yagent after writing — the running engine is unchanged
╰─ enter write · esc back · q quit without writing ───────────╯
```

- 行格式：`+ 新增` / `− 删除` / `~ 改值（old → new）` / `→ 默认档`，
  前缀色 = `Success` / `Danger` / `Accent` / `Primary`。
- 每个 `op` 的**次级 dim 行继续渲染 `op.note` 原文**：`applyOps` 失败时报的就是这条 note，
  人话行是给用户看的，note 是给用户和日志对账用的，两者不可互相替代。
- 分组头（PROVIDERS / DEFAULT / CHOICES）用 `common.SectionHeaderStyle`。
- 空态保持现状那句 `no changes queued — enter leaves the file untouched`：它是承诺不是装饰。
- `2 of 4 written` 这类部分失败事实必须显示（现状 `applyOps` 的错只报哪条失败，
  不报成功了几条，而"前面几条已经落盘"是 §11 坑 28 的核心）。
- `ⓘ restart yagent…` 那句照 DG 保留，并从 `setup_embed.go` 的信息行改成摘要屏上的一条常驻说明。

### 4.3 首启屏（修事实 12）

```
│  ▸ language      ‹ Chinese ›        what the model answers in
│    autonomy       ask for approval  auto-approve dangerous ops   ⚠ full yolo removes every prompt
│    routing        auto              pick tier per task difficulty
│    browser        enabled           needs browser-control CLI + Chrome
│    llm timeout    10m               per-call ceiling
```
- 标签列宽按内容取（不再 `padTo(label,14)` 硬截），值列用 `‹ ›` 明示"这是可切换的枚举不是文本"，
  说明列与值列同屏（`inner < 72` 时说明整列下沉成第二行，与表单的 `showHints` 同一降级阈值风格）。
- 被改过的行加 `Accent` 前缀 `~`，与摘要屏同一套记号；`full yolo` 那档把 ⚠ 说明染成 `Danger`
  —— 这一档会让 `WorkspaceGuard` 之外的一切确认消失，值得在唯一一次决定它的屏幕上显形。

### 4.4 列表屏（修事实 6、7、13）

- `▶ ` 游标 + 焦点行 `Bold` + `common.BadgeStyle` 徽章：`default`（`Success`）、
  `queued`（`Accent`）、`no key`（`Danger`）。
- KEY 列**不再整列消失**：`inner < 64` 时收成 3 列图标（`✓`/`⚠`/`✗`），
  因为"有没有密钥"正是首启要看的那件事，空间紧就砍 base_url，不砍事实。

---

## 5. 键位变化表

| 屏 | 现有 | 新增 | 变更 |
|----|------|------|------|
| providers | `a e d x t c` `j k ↑ ↓` `enter` | `s` 设为默认、`1-9` 快捷选中、`g/G` | `enter` 保持"编辑"（原方案 §6.2 那句 `Enter 设为默认` 按本文更正，避免"同名键两屏不同义"） |
| form | `tab ⇤tab enter ctrl+s esc` | `ctrl+o` 折叠可选区、`f2` 切 key 来源、`ctrl+p` 预设 | 无删除 |
| probe | `p enter any` | `esc` 显式列进底栏 | 无（`default` 分支已回列表） |
| default | `j k enter c` | `1-9` | 无 |
| first-run | `j k h l space enter c` | `g/G`（将来条目变长时备用，本次不加） | 无 |
| summary | `enter esc q` | `j k g/G`（滚动窗口打开时） | 无 |

**新键必须进 `hints()`，且必须由新测试钉住双向一致**（§6 的 `TestWizardHintsMatchHandledKeys`）：
把裸串升级成键帽之后，"键帽上写着、按下没反应"就成了新的失效模式，而这正是键帽比裸串更容易被相信的代价。

---

## 6. 测试与闸门

### 6.1 必须重写的既有测试（改了交互形状就得改，且要确认它们真的能红）

| 测试 | 为什么红 |
|------|----------|
| `TestWizardHappyPathWritesUsableConfig` | tab 次数从 8 变 3（可选区折叠），随后走预设/`o` 展开路径 |
| `TestWizardFormFieldFiltersNonDigits` | `max_tokens` 进了可选区，用例需先 `o` 展开才能拿到数字字段 |
| `TestWizardMasksKeySources` | key 来源合并成一行，断言改成"`f2` 切换 + 非焦点遮罩 + 焦点明文" |
| `TestWizardRefusesToDeleteReferencedProvider`、`TestWizardBlocksRename` | 断言的是状态与 `errMsg`，预期不变；若变则是真回归 |

### 6.2 新增契约测试（每条都必须能因为对应的实现漂移而失败）

1. `TestWizardRailCompletionComesFromLedger` —— 停在 `stepProbe` 时，轨道 `PROVIDER` 段已显示 `✓`；
   退回 `stepProviders` 不改判据。钉 DB：游标倒退而事实没倒退。
2. `TestWizardRequiredGroupMatchesValidator` —— 必填组的字段集合必须与 `config.WriteProvider` 的
   校验要求逐一对应；给一份"必填全填但校验失败"的草稿必须让测试红。
3. `TestWizardHiddenFieldsNeverHoldFocus` —— 折叠状态下 `fieldIdx` 不可能指向隐藏行，
   且 `tab` 循环跳过它们。
4. `TestWizardPresetsOnlyFillEmptyFields` —— 先手输 `api_base_url`，再应用预设，值必须不变。
5. `TestWizardCatalogPrefillDoesNotOverwriteTypedValue` —— 同上，针对 `context_window`。
6. `TestWizardEnterSavesAtEachGroupEnd` —— 必填末行 `enter` 与展开态末行 `enter` 都落草稿。
7. `TestWizardHintsMatchHandledKeys` —— 遍历 `hints()` 的键帽首词，断言该屏 `handleKey` 有对应分支；
   反向也查（新加的分支若没进 hints 就报）。
8. `TestWizardSummaryLinesCarryTheirNote` —— 每条 op 的人话行旁必须有那条 `op.note` 子串，
   保证写失败时可定位。
9. `TestWizardHeightMatrixNeverOverflows` —— `{18,20,24,30,40}` × 7 屏 × 8 条排队 op，
   断言 `View()` 行数 ≤ 终端行数且长错误原文要么折行要么被滚动窗口接管。
   **补的是事实 5 的空缺：现在只测宽度。**
10. `TestProbeSpinnerTicksWhileProbing` —— `probing == true` 时 `Init/Update` 必须产出 tick cmd，
    收到 `probeResultMsg` 后不再产出（防 spinner 空转）。

### 6.3 共享层上提（DF）的安全网

`components` 的 `dialogWidths` / `moreLine` / `allocateColumns`（`dialog_list.go:31/77/89`）与
`scrollWindow`（`model_select_dialog.go:298`）上提为 `common` 导出函数后，必须跑并全绿：
`go test ./internal/tui/... ./internal/tui/components/...`，其中
`confirm_dialog_render_test.go`、`user_help_dialog_render_test.go`、`model_select_dialog_test.go`、
`agent_select_dialog_test.go`、`routing_dialog_test.go`、`timeout_dialog_test.go` 是行为不变的证据
（它们断言的是**精确单元格宽度**，签名搬动骗不过去）。
`setup_render.go` 里向导自己那份 `clampInt/truncate/padTo` 若与 `common` 或 `components.ellipsize` 重形，
**一并换掉**，不留第二份。

### 6.4 闸门与验证局限

- `bash scripts/check_all.sh`（build + vet + 五把守卫 + 全量单测）；
  `TestWizard*` 涉及并发（探测 tick）时另跑一次 `RACE=true`。
- 纯 UI 改动的**否证测试**：`go test ./internal/config/... ./internal/llm/...` 结果必须与改动前逐条一致，
  且不新增任何配置键。
- 界面文案不进 `i18n.go`（10 个对话框同样没进，`modelDialogHint` 就是硬编码英文）—— 一致性优先，
  向导接 i18n 是独立一件事。
- **验证局限**：本机无 TTY，我只能靠渲染字符串的宽度/高度断言与尺寸矩阵证明形状，
  不能声称亲眼看过真实终端里的效果。渐变（`ApplyGrad`）、`❯` 光标闪烁、spinner 观感这三类需要你在真终端里过一眼。

---

## 7. 实施顺序（每步结束都要 `go build ./... && go vet ./... && go test ./...` 干净）

| 步 | 内容 | 该步的独立价值 |
|----|------|----------------|
| S1 | DF：四个原语上提 `common`，对话框改调用，跑 §6.3 的测试 | 零行为变化，先让共享层就位 |
| S2 | A2 骨架：rail + Place 居中 + `renderPanelTop/Bottom` + 删 `setupTop/setupBottom` 与三个死调色板字段 + 高度预算与滚动 | 单这一步就已经"同族" |
| S3 | 键帽底栏 + `TestWizardHintsMatchHandledKeys` | 把"提示"变成可信的 |
| S4 | B2 表单：三段 + `o` 折叠 + `p` 预设 + 目录预填 + `f2` key 来源 + `newTaskInput` 令牌 + 删双游标 | 首次填写从 9 格降到 3 格 |
| S5 | C2 探测：spinner + 目标/耗时/预算 + 归类行 | 30s 里界面不再是死的 |
| S6 | C2 摘要：diff 视图 + 分组 + note 次级行 + 部分失败计数；首启屏与列表屏的徽章/列宽修正 | 最后一屏说人话 |
| S7 | 文档同步：`docs/config-trim-onboarding-design.md` §6.2/§6.3 逐条标注"已兑现 / 仍未兑现（DG）"；AGENTS.md §3 模块树里 `setup*.go` 那两行加上 `setup_presets.go`，并把 §6.8 的一句话写清"向导与对话框同族（阶段轨 + 键帽 + ▶ 游标 + 徽章）" | 免得下一轮又把"用现成令牌"当成新主意 |

---

## 8. 明确不做

- **不做写后立即生效**（DG）：`setup_embed.go` 继续提示重启；原方案 §6.2 那条承诺仍然未兑现，
  由 S7 的文档标注如实记下，而不是在本次改动里悄悄扩大范围。
- 不接 `internal/tui/anim`（DD），不做全量键编辑器（原 D3），不把向导塞进 `DialogStack`（原 D7）。
- 不动 `internal/config`（写回内核、`NeedsSetup` 判据、`api_key_env` 展开一律原样）。
- 不新增配置键、不改 `default_config.toml` 模板、不动 provider schema。
- 不改 7 屏的状态机拓扑（不新增/合并 `setupStep`），只改渲染与按键。
- 界面文案不接 i18n，与既有对话框一致。

---

## 9. 落地时的更正（方案写错或被现实推翻的地方）

| # | 方案的说法 | 落地后的事实 |
|---|-----------|-------------|
| 1 | 状态条按 `inner` 折行就够了 | **不够**。`lipgloss.Style.Width()` 会把超宽的行再折一次：前缀 `× ` 让每行宽 2 列 → 渲染行数比计数多 → 面板超出终端高度。三道闸门都在计数之前做：`fitLines` 先剪宽、`wrapPlain` 硬切超长单词（provider 原文里全是 URL/base64）、`capStatus` 给状态条自己设预算（否则 `正文 = 高-2-状态行` 算出负数并夹到最小值，总高仍超）。§2.3 的公式因此改成 `rows = max(h-2-extras, 1)`，配合 `extras ≤ h-5` 的预算保证不变量成立 |
| 2 | 卡片里列宽 = `cw/3 + cw/3 + 余下` | **列宽要正好加到容器内宽**，且分隔符数量按真正拼出的列数算（`providerRow` 是 6 列 5 个空格，方案那份算法少算 2 个 → 整行溢出 1~2 列 → 在卡片里再折一行）。徽章还要算上 `BadgeStyle` 的 `Padding(0,1)`：`fix key` 是 9 列，预留 8 列就会被 `Width(8)` 折成两行 |
| 3 | 模态键用裸 `o` / 裸 `p` | 见 §3.1 的警告：**必须带修饰符**。裸字母要先送进 textarea |
| 4 | 必填集合 = `queueProviderOp` 现有的三条校验 | **少一格**：`api_base_url` 没人查，而 `validate()` 要求它。见 §3.1 的 ⚠️ |
| 5 | 摘要用 `+ − ~ →` 四种记号 | 补了第五种 `=`（不变）：`routing`/`browser` 没被改过时，"→ auto → auto"是在谎报一次改写。另外 `lang` 选"不注入"时 `WriteFirstRun` 根本不写那一行（`config/setup.go:282`），必须显示 `stays "Chinese"` 而不是假装归零 |
| 6 | 错误原文由 `View` 统一渲染 + 探测屏也渲染 | 同一段 provider 原文会**显示两遍**（状态条一次、探测屏一次）。现在 `probeErr` 只归探测屏，状态条跳过它；归类行也改成折行而不是截断（"re-check this row with…" 这种半句话没法照着做） |
| 7 | §6.2 测试清单第 9 条只写"高度矩阵" | 落地时把它并进了 `TestWizardRenderWidthMatrix`：宽度 {40..240} × 高度 {18,24,40} × 七屏 × 表单的折叠/预设子模式，断言**行数 ≤ 终端高度**且**每行宽度 ≤ 终端宽度**。`setupMinHeight` 从方案暗示的 14 提到 18（8 格全展开的表单在 14 行里放不下） |
| 8 | 顶栏标签固定 "YAGENT SETUP · <屏名> · n of 4" | 窄面板时标签自己就比边框宽，`renderPanelTop` 只能把横线夹到 1 列、整行仍越界（表现是顶栏被剪成 `╭─ …`）。改成三级退让：`YAGENT SETUP · X · n of 4` → `SETUP · X` → `X` |

| 9 | 向导的 `done` 就是退出 | **`done ≠ quit`**：bubbletea 只认 `tea.Quit`，旧实现置完 `done` 就返回 nil，于是独立跑 `yagent setup` 时 q / ctrl+c / 写盘成功后都停在最后一屏（用户报障）。修法是给向导一个 `standalone` 标记，`Update` 单点收口：`done && standalone → tea.Quit`；嵌入式宿主**必须不**翻（那会把整个 yagent 退掉）。`TestRunSetupProgramActuallyExits` 用 `tea.WithInput(strings.NewReader("q"))` 真跑一次程序断言 Run 返回 |
| 10 | 欢迎屏只认 enter | 也认 `a`（直达表单）并把它写进底栏键帽：第二次运行 setup 的人十有八九是来加第二个 provider，只认 enter 会让人以为界面没反应——这正是"看不到新建 provider 的交互"的来源 |

**变异验证**：`stageDone` 退回游标、预设覆盖手输值、目录回填的手输守卫失效 —— 三个变异都被对应测试抓到（红）。
其余项靠断言与矩阵测试，未逐个做变异。
第 9 条例外，而且是双向验的：把 `done && standalone → tea.Quit` 那三行拆掉后，
`TestRunSetupProgramActuallyExits` 的两条子用例都按 5s 超时红掉，报的就是用户原话
（"按 q 之后程序没退出"）。
