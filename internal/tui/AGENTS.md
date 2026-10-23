# internal/tui — Bubble Tea v2 界面

> 范围：本目录内不可派生的约束。全局见仓库根 `AGENTS.md` §6.8。
> **这里没有任何脚本守卫**（九把 `check_*.sh` 只覆盖依赖方向、裸 go、原子写、文件行数、测试卫生），
> 所以 TUI 的全部不变量只由代码注释 + 渲染/尺寸矩阵测试钉住。

## 边界

唯一面向用户的 model/Update/View。只消费事件总线并按其渲染：**不生产事件、不做压缩判定、
不写配置生效逻辑**（写回内核在 `internal/config/persist.go`）。不 import `internal/agents`
（落点由 `app` 侧计算）。

## 硬约束

**1. `listenForEvents` 是一次性 cmd，没有重放队列。** 只有"事件被消费"才补回监听
（`tui_update.go:9-17` 的 `keepListening`），给按键或 resize 补会让监听 goroutine 按按键次数堆积。
任何新增叠加模式/守卫都必须消费 `taskEventMsg` 并重新武装监听器，否则界面永久断流
（根 AGENTS.md §11 坑 6）。

**2. 弹窗守卫是白名单，`taskCompleteMsg` 必须透传。** `tui_update.go:46-56`：弹窗打开时只放行
少数消息类型。`taskCompleteMsg` 是 `taskRunning` **唯一的清零点**，吞掉的表现是界面永久卡在
"运行中"且最终答案不落地；`tea.PasteMsg` 不是 KeyMsg，也要转发给 textarea。

**3. 布局尺寸只有一个口径。** `computeFooterHeight`（`tui_render.go:16-18`）与 `View()` 的 footer
拼接逐行对应（`tui_view.go:212-214`）；视口行高与正文四列宽度也只算一次 —— `computeBodyLayout`
（`tui_view.go:40`）= 高 `termHeight - footer - tabBarHeight`、宽 `termWidth - 滚动条列 - dashboard 列`，
resize（`tui_render.go:137`）、切标签页（`tui_tabs.go:283`）、后台标签页尺寸（`tui_update_system.go:135`）
全部走它。差一行整屏抖动；差一列（旧 resize 用 `termWidth - dashboardWidth` 且没扣 tab 栏）的代价是
View 每轮纠正宽度 + 整段正文白排一遍。`View()` 因此**只拼接**正文列（`joinColumns`，`tui_view.go:239`）：
viewport 自己已把每一行补齐到列宽（`TestViewportViewPadsEveryLine` 钉住这条第三方前提），再套一次
`Width().Render()` 就是把同一件事做第二遍（40 行 × 109 列实测 243µs → 5.5µs），而且满宽的行会被
再折一次、行数翻倍。`lipgloss.Style.Height(h)` 是**含上下边框**的总高，
按 `height` 而不是 `height-2` 分配行预算会多要 2 行，被末尾的"行数收敛 + 补边框"悄悄截掉
（根 AGENTS.md §11 坑 7）。

**3.1 流式重排按时间合并，不按 chunk。** bubbletea **每条消息都调 `View()`**（`tea.go` 的 Update 后
直接 render），而一次 flush 就标一次脏 —— 不合并的话"重排次数 = chunk 数 / 5"，实测 4000 chunk
触发 800 次重排（每消息 1.4ms@300 条、2.7ms@1200 条）。所以合并必须发生在 `View()` 里，
发生在 chunk 处理里没用。规则：只有"没有结构性变化 **且** 脏条目全部是正在流式的 `ai_stream`"才允许推迟到
`streamRepaintInterval`（80ms，必须 < tick 的 100ms，否则尾巴要迟一拍）—— 判据在 `streamDirtyHeld`
（`tui_render.go:207`），别退化成布尔开关：定稿（`streaming=false`）、`ai_response`、新条目、尺寸变化
都必须立刻落地。被推迟时 `viewportDirty` 保持为真，且 tick 的 idle 早退条件里必须带
`hasDirtyEntries()`（`tui_update_system.go:50`），否则没人再来触发。配套的两处 memo：流式条目的
已闭合 thought 面板（`renderStreamEntry`，`tui_render.go:391`：宽度或块序列变了才重画，定稿后走
普通条目的 `renderedCache`）、transcript 顶部固定头部（`renderWelcomePanel`，`tui_view.go:559`，
key 只含宽度/行高/cwd；**头部分支不许算 footer 行高**，那条路会走 token 面板与 langManager）。

**3.2 正文是虚拟化的：viewport 里只有可见区间。** 虚拟化之后一次重排的成本是
O(变过的条目 + 窗口)，不再是 O(历史)（实测 4000 chunk：viewport 内容 924KB → 41KB，
每消息 548µs → 385µs，且 300 条与 1200 条历史已经分不出差别）。滚动游标是绝对行号
`body.top`，`bodyIndex`（`tui_body.go:43`）是唯一行索引：条目 → 排版块（agent 分隔行 + 正文，
含 thinking 续行）→ 行数前缀和。三条不可违反：
① **行数统计与取词同源** —— `bodyBlockFor`（`:158`）既算 `lines` 也给文本，`bodyText`（`:295`）
必须等于全文的连续切片；旧的 `contentPartLinePrefix` 按条目原始行数累加（不认跳过项、分隔行、
续行），圈动画范围够用、拿来切片整屏错位，已删。
② **`scrolledUp` 是反着写的** —— 跟随尾部必须是零值（新建 model、清空会话、恢复 tab 都从零值起步），
写反的症状是"流式输出停在开头"。
③ **`growBodySlice`（`:149`）必须保留内容** —— 换数组时 `make` 会把已排好的块清零，而 `validFrom`
不知道数组被换过。
滚动一律走 `bodyScrollTo/bodyScrollBy/bodyGoto*/bodyPageBy`，**不许**再直接对正文 viewport 调
`Scroll*/Page*/SetYOffset/GotoBottom`，`viewport.Update` 也不接正文按键（滚轮在 `handleMouseMsg`）：
两处都能滚就错位。滚动条、ruler、搜索读绝对索引 —— `searchLineAt` 存绝对行，
而 `searchLineStyle`（`tui_search.go:158`）收到的是**窗口内**行号，必须加回 `body.top`。
`bodyIndex` 整体随标签页保存/恢复（`tab.body = m.body.clone()`），且必须与 `tab.viewport` 同版本，
否则恢复后 `bodySync` 以为窗口没变而跳过 SetContent，屏幕停在另一个 tab 的内容上。

**4. 流式槽的键 ≠ 显示名。** `streamSlotKey` 返回 `agent#delegationID`（`tui_update_tasks.go:1025-1030`），
它只用于分槽；`le.from` / `m.currentAgent` / token 归集**必须继续用裸 Agent 名**——混用的症状是
标签变成 `Coding#call_3`。坐标为空时退回裸名，所以 Director 与存量 rollout 行为逐字不变。

**5. 口径只算一次。** token 面板三个表面共用 `buildTokenPanel`，所有口径只在 `tokenSnapshot()` 里算；
压缩区块同理在 `compressSnapshot()`。**不要在任何表面另取计数器直接格式化**（根 AGENTS.md §6.8）。
三个压缩事件名在本包只有一份字面量副本，加档位要同改三张表（`compress_panel.go:20-22`）。

**6. 斜杠命令必须显式列进 `/` 分支**（`tui_update_commands.go:33`），否则 `/pause` 是在搜字符串
"pause"，界面上什么都没发生。**暂停态绝不能走 `handleTaskCompleteMsg` 的换 ctx 分支**
（`tui_update_commands.go:199-201`）——那会丢掉闸门所在的那棵 ctx。

**7. 列表/表单的共用原语只有一份。** `common/list_metrics.go:1-6` 就是"两份同形实现"的修法：
`components` 只留别名（`components/dialog_list.go:28-31`），引导向导共用同一份。**不许在任一边再抄一份**。
输入框视觉令牌同理：`applyTextareaTokens`（`tui_model.go:573-576`）是主输入框与向导字段的唯一来源。
`dialogListChrome = 10`（`internal/tui/components/dialog_list.go:18-22`）是"除列表外占用的行数"的事实，对话框加一行必须同步改它。

**8. 对话框自己不知道屏幕多大。** 入栈方必须 `SetBounds`（`tui_dialogs.go:245-249`）——弹窗守卫会
吞掉 `WindowSizeMsg`，不设尺寸则 `View()` 因 width<40 返回空串，对话框永远不可见。
`components` 不许 import `internal/tui`（循环依赖），共享类型在本地重定义（`internal/tui/components/shared_types.go:4-7`）。

**8.1 信息类对话框（`/skills`、`/mcp`）与列表对话框共用同一套几何。**
`components/info_dialogs.go` 的 `infoDialogChrome`（9）是除列表外的固定行数：边框 2 + 内边距 2 +
标题 1 + 分隔线 1 + 表头 1 + 提示前空行 1 + 提示 1。每多/少一行非列表内容都必须同步改它，
列表预算走 `common.ListBudget`。它们不可选择，所以**不进** `selectProbe`；但必须透传
`taskCompleteMsg`（硬约束 2）。

**8.2 斜杠补全带类型。** `skillSuggestions` 是 `[]completionItem{name,kind,desc}`，不是 `[]string`；
内置命令与技能撞名时**内置命令胜**且只 warn 一次（`tui_completion.go`）。手打 `/name args` 直接执行技能
（首个 token 精确命中才算），否则回落搜索 —— 改这条要保留 `TestTUI_SlashUnknownFallsBackToSearch`。

**8.3 新对话框进按键分发要么加 `case`，要么实现 `components.InfoPanel`。**
`handleDialogStackKey`（`tui_update_keys.go:51`）是**类型 switch**，末尾 `return m, nil, false`（`:435`）
就意味着这把键归编辑模式。而 `tui_update.go:86` 明确**不**把 KeyMsg 转发给 `dialog.Update`，
所以漏加的那个对话框：Esc 关不掉（面板还压在屏幕上，输入框却顺手被切到 NORMAL）、滚动键全部失效，
**并且没有任何编译错误或日志**——根 AGENTS.md §11 坑 33 的 TUI 版本（测试与生产不走同一个载体）。
信息类面板因此按接口认领（`components/info_dialogs.go:16`，model 侧 `:428`）：
只读面板实现 `HandleInfoKey` 就自动拿到 Esc/滚动，不必再改类型 switch。
闸门：`TestInfoDialogsCloseOnEsc`（`slash_skill_test.go:188`，删掉接口那条 case 即红）、
`TestInfoDialog_ScrollsToEnd`（`info_dialogs_test.go:78`，按的是 `HandleInfoKey` 而不是 `Update`）。

**9. 向导独占键盘。** 任务运行中或弹窗打开时 `/setup` 直接拒绝（`setup_embed.go:24-34`），
理由是第 1 条那个没有重放队列的监听器。

## 子目录

- **`common/`** — 令牌层（`styles.go` 主题/配色、`list_metrics.go` 几何原语、`grad.go` 渐变按
  grapheme 切分）。`PanelWidths` 的 inner = panel − 6；`ListBudget` 装不下指示行就**放弃指示行**
  （保证表面永远不高出终端）；`Ellipsize` 超长走**中间省略**——同前缀的模型名靠尾部区分。
  零测试文件，靠调用方的矩阵测试间接暴露。
- **`components/`** — 对话框 + `DialogStack`。只渲染、只消费按键。三个选择类对话框共用一套
  按键契约，此前在三个文件各手抄一份（`internal/tui/components/select_dialog_contract_test.go:19-20`）——新选择类对话框
  必须接进 `selectProbe`。同包只准一个 ANSI 剥离辅助函数（`check_test_hygiene.sh` 硬拦）。
- **`layout/`** — **当前实际不负责布局**：`Region`/`AllRegions`/`IsCompact` 在包外零调用点，
  唯一被调的是 `Resize`（`tui_update_system.go:130-132`，构造在 `tui_model.go:639`）。
  改 `computeRegions` 不会改变界面；真实高度契约在 `tui_render.go`。compact 断点 120
  （`layout/engine.go:40-42`）与 dashboard 可见断点 `termWidth >= 120`（`tui_dashboard.go:51`）
  是同值两份实现。零测试，且因无消费者而永远不会红——要接进真实渲染就先补宽度矩阵，或先删掉它。
- **`anim/`** — **零生产消费者**（全仓无 `yagent/internal/tui/anim` import），零测试文件。
  别把新界面接到这个包：`setup.go:465` 给的理由就是"零引用 + 无测试 = 新风险"。
  注意根目录的 `anim.go` 是另一套时间驱动动画，`tui/anim_test.go` 钉的是根目录那个。

## 入口

`tui_model.go`（状态 + 渲染缓存 + 输入框令牌）、`tui_update.go`（消息路由与守卫）、
`tui_view.go` + `tui_render.go`（高度契约）、`tui_chrome.go`（手绘面板骨架）。

## 闸门

`TestView_ExactHeight`（`tui_chrome_test.go:267`）与 `TestView_ExactHeightWithPanels`（`:530`）、
`TestLiveRender_ListenerStaysArmedInOverlayModes`（`tui_livestream_test.go:97`，四个覆盖层）、
`TestTaskCompleteMsgNotSwallowedBehindDialog`（`tui_model_switch_test.go:155`）、
`TestUpdate_PasteMsgRoutedToTopDialog`（`tui_paste_test.go:17`）、
`TestParallelStreamsSameAgentDoNotMerge`（`tui_parallel_stream_test.go:26`，让 `streamSlotKey`
忽略 `DelegationID` 即红）、`TestTokenPanel_FooterFitsTermWidth`（`token_panel_test.go:257`）、
`TestBodyWidthIsOneSource`（`tui_view_layout_test.go:34`，把 resize/切标签页的宽度公式改回旧写法即红）、
`TestViewportViewPadsEveryLine`（同文件 `:100`，钉住"viewport 自己补齐每行"这条被 `joinColumns` 依赖的前提）、
`TestTUI_StreamRepaintIsCoalesced` / `_NonStreamDirtIsNeverHeld` / `_TickStaysArmedWhileRepaintHeld` /
`_StreamPanelsMatchOneShot`（增量渲染必须与一次性逐字相同）/ `_WelcomeHeaderSkipsFooterMath`
（`tui_stream_repaint_test.go`：把 `streamDirtyHeld` 改成恒 false、或把面板 memo 关掉，立刻红）、
`TestBody_WindowIsAPlainSliceOfTheTranscript`（`tui_body_test.go:36`，任意高度任意游标下窗口必须等于
全文切片）/ `TestBody_ViewportHoldsOnlyTheWindow`（`:60`，改回 `SetContent(全文)` 即红）/
`TestBody_AppendRebuildsOnlyTheTail`（`:91`，扩容用 `make` 换数组即红）/ `TestBody_FollowTail`
（`:117`，`scrolledUp` 写反即红）/ `TestBody_TabSwitchKeepsCursorAndWindow`（`:208`）、
`TestLLMTimeoutDialog_SizeGuard` / `_WidthMatrix`（`components/timeout_dialog_test.go:146,167`）、
`TestWizardRenderWidthMatrix`（向导侧：宽度 {40..240} × 高度 × 七屏，断言行数 ≤ 终端高度）。

**测试约定**：断言**精确单元格宽度**而非子串；宽度矩阵 `{30..240}`、终端尺寸矩阵 `{20..240}`；
共享 fixture `newTestModel()` / `chromeTestModel(t,w,h)`。
