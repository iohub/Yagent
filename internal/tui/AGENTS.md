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

**3. 布局高度必须自洽。** `computeFooterHeight`（`tui_render.go:15-17`）与 `View()` 的 footer
拼接逐行对应（`tui_view.go:211-213`），viewport 高 = `termHeight - footerHeight`
（`tui_render.go:139`），差一行整屏抖动。`lipgloss.Style.Height(h)` 是**含上下边框**的总高，
按 `height` 而不是 `height-2` 分配行预算会多要 2 行，被末尾的"行数收敛 + 补边框"悄悄截掉
（根 AGENTS.md §11 坑 7）。

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
输入框视觉令牌同理：`applyTextareaTokens`（`tui_model.go:654-657`）是主输入框与向导字段的唯一来源。
`dialogListChrome = 10`（`internal/tui/components/dialog_list.go:18-22`）是"除列表外占用的行数"的事实，对话框加一行必须同步改它。

**8. 对话框自己不知道屏幕多大。** 入栈方必须 `SetBounds`（`tui_dialogs.go:245-249`）——弹窗守卫会
吞掉 `WindowSizeMsg`，不设尺寸则 `View()` 因 width<40 返回空串，对话框永远不可见。
`components` 不许 import `internal/tui`（循环依赖），共享类型在本地重定义（`internal/tui/components/shared_types.go:4-7`）。

**9. 向导独占键盘。** 任务运行中或弹窗打开时 `/setup` 直接拒绝（`setup_embed.go:24-34`），
理由是第 1 条那个没有重放队列的监听器。

## 子目录

- **`common/`** — 令牌层（`styles.go` 主题/配色、`list_metrics.go` 几何原语、`grad.go` 渐变按
  grapheme 切分）。`PanelWidths` 的 inner = panel − 6；`ListBudget` 装不下指示行就**放弃指示行**
  （保证表面永远不高出终端）；`Ellipsize` 超长走**中间省略**——同前缀的模型名靠尾部区分。
  零测试文件，靠调用方的矩阵测试间接暴露。
- **`components/`** — 10 个对话框 + `DialogStack`。只渲染、只消费按键。三个选择类对话框共用一套
  按键契约，此前在三个文件各手抄一份（`internal/tui/components/select_dialog_contract_test.go:19-20`）——新选择类对话框
  必须接进 `selectProbe`。同包只准一个 ANSI 剥离辅助函数（`check_test_hygiene.sh` 硬拦）。
- **`layout/`** — **当前实际不负责布局**：`Region`/`AllRegions`/`IsCompact` 在包外零调用点，
  唯一被调的是 `Resize`（`tui_update_system.go:128-129`，构造在 `tui_model.go:720`）。
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
`TestLLMTimeoutDialog_SizeGuard` / `_WidthMatrix`（`components/timeout_dialog_test.go:146,167`）、
`TestWizardRenderWidthMatrix`（向导侧：宽度 {40..240} × 高度 × 七屏，断言行数 ≤ 终端高度）。

**测试约定**：断言**精确单元格宽度**而非子串；宽度矩阵 `{30..240}`、终端尺寸矩阵 `{20..240}`；
共享 fixture `newTestModel()` / `chromeTestModel(t,w,h)`。
