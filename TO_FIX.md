# TUI 潜在问题清单

> 基于 `internal/tui/` 全部文件系统分析，共 21 个问题。

## 高严重度（功能 BUG / 数据丢失）

---

### 1. `respondToAuth` 可能弹出错误弹窗类型

- **文件**: `internal/tui/tui_dialogs.go:61-68`
- **分类**: 逻辑 BUG
- **描述**: `respondToAuth` 无条件 `dialogStack.Pop()` 然后尝试断言为 `*ConfirmDialog`。如果栈顶不是 ConfirmDialog（如遗留的 HelpDialog），requestID 丢失。
- **影响**: Task 端收到空 requestID，无法匹配待确认请求 → 永久挂起。
- **修复**: Pop 前先检查栈顶类型，或用 `CloseDialog("confirm_dialog")` 按 ID 移除。

```go
// 当前代码（有问题）
popped := m.dialogStack.Pop()
if dlg, ok := popped.(*components.ConfirmDialog); ok {
    requestID = dlg.GetRequestID()
}

// 建议: 先检查类型再 pop
top := m.dialogStack.Top()
if dlg, ok := top.(*components.ConfirmDialog); ok {
    requestID = dlg.GetRequestID()
    m.dialogStack.Pop()
}
```

---

### 2. `wrapText` 函数名实为截断函数

- **文件**: `internal/tui/tui_render.go:512-535`
- **分类**: 逻辑 BUG
- **描述**: 函数名叫 `wrapText`，但实际行为是逐字符从尾部删除直到适配宽度，不换行只截断。
- **影响**: 授权弹窗中用户看不到完整的危险命令内容。`components/confirm_dialog.go` 中有正确的 `wrapText` 实现，但此处未被使用。
- **修复**: 用 `components` 包中的正确换行实现替换，或重命名此函数为 `truncateLine`。

---

### 3. 事件静默丢弃

- **文件**: `internal/tui/tui_model.go:144-151`
- **分类**: 并发 / 数据丢失
- **描述**: `tuiEventConsumer.Consume()` 使用非阻塞 `select default` 发送到 channel。当 eventCh（buf=1000）满时，事件被静默丢弃。
- **影响**: 工具调用匹配断裂，AI 响应缺失，用户看到日志空缺无提示。
- **修复**: 
  - 增大缓冲区到 5000+
  - 或使用阻塞发送施加反压
  - 至少 log 丢弃事件到 stderr

```go
select {
case c.ch <- event:
default:
    // 至少记录丢弃
    log.Printf("TUI event dropped: type=%s", event.Type)
}
```

---

### 4. 弹窗先弹出再发消息，失败无恢复

- **文件**: `internal/tui/tui_dialogs.go:57-69`
- **分类**: 错误处理
- **描述**: `respondToAuth` 第 62 行 Pop 弹窗，第 69 行才 publish 响应。若 publisher 为 nil 或 publish 失败，弹窗已消失无法恢复。
- **影响**: Task 卡住等待确认，UI 无法重新弹窗。
- **修复**: 先检查 `m.publisher != nil`，publish 成功后再 Pop。

---

### 5. eventCh 永不关闭，goroutine 潜在泄漏

- **文件**: `internal/tui/tui_model.go:356`
- **分类**: 内存泄漏
- **描述**: `eventCh` 创建后从未 close。`listenForEvents` goroutine 阻塞读取。多次 `StartTUI` 调用时旧 channel 和 goroutine 泄漏。
- **影响**: 测试或错误恢复路径下 goroutine 累积。
- **修复**: 添加 context 取消机制或 defer close。

---

## 中严重度（逻辑 / UX 缺陷）

---

### 6. 提交后输入框内容立即被清除

- **文件**: `internal/tui/tui_tasks.go:42-43`
- **分类**: 逻辑 BUG
- **描述**: 
  ```go
  m.input.SetValue(taskDesc)   // 意图让用户看到提交内容
  m.input.SetValue("")          // 下一行立即清空
  ```
- **影响**: 用户永远看不见提交内容。
- **修复**: 删除第 42 行，或延迟清空。提交内容已通过日志记录（第 64-69 行）。

---

### 7. Footer 高度计算不准确

- **文件**: `internal/tui/tui_render.go:15-64` vs `internal/tui/tui_view.go:317-325`
- **分类**: 布局 BUG
- **描述**: `computeFooterHeight()` 未计入 command mode + running 时 `renderTokenDashboard()` 额外渲染的 "Running [model]..." 行。
- **影响**: viewport 多占 1 行，底部内容被遮挡。
- **修复**: 
  ```go
  if m.commandMode && m.taskRunning {
      height += 1
  }
  ```

---

### 8. 终端 resize 强制滚到底部

- **文件**: `internal/tui/tui_update.go:161-163`
- **分类**: UX 缺陷
- **描述**: `resizeViewport()` → `buildViewportContent()` → `viewport.GotoBottom()`。用户向上滚动看历史时 resize 窗口即被强制踢回底部。
- **影响**: 阅读历史被中断。
- **修复**: 改用 `rebuildViewportScrollLock()` 替代 `buildViewportContent()`，保留滚动位置。

---

### 9. `submitFollowUp` 不检查 task 状态

- **文件**: `internal/tui/tui_update.go:671-674`
- **分类**: 逻辑 BUG
- **描述**: 仅判断 `m.currentTask != nil` 即允许提交 follow-up，不检查 task 是否已完成/失败/取消。
- **影响**: 对已结束 task 继续对话，日志混乱。
- **修复**: 添加状态检查 `m.currentTask.Status == http.TaskStatusRunning`。

---

### 10. `showHelpDialog` 与 `dialogStack` 双源真相

- **文件**: `internal/tui/tui_view.go:37-39` / `internal/tui/tui_dialogs.go:24`
- **分类**: 状态管理
- **描述**: `m.showHelpDialog`（旧 bool 标志）和 `dialogStack` 中 HelpDialog 是两个独立的状态源，可能不一致。
- **影响**: 弹窗显示异常（空白或错误弹窗）。
- **修复**: 删除 `m.showHelpDialog`，统一用 `dialogStack` 管理。

---

### 11. fzf 结果缓冲区限制 4096 字节

- **文件**: `internal/tui/tui_fzf.go:115-123`
- **分类**: 健壮性
- **描述**: 固定 4KB 缓冲区读取 fzf 输出。极端情况数据截断。
- **影响**: 极低概率（文件路径很少超过 4KB）。
- **修复**: 使用 `io.ReadAll`。

---

## 低严重度（性能 / 代码质量）

---

### 13. 每个 tool_result 全量重建 viewport

- **文件**: `internal/tui/tui_render.go:150-166` (`rebuildViewportScrollLock`)
- **分类**: 性能
- **描述**: 遍历**所有** log 条目重新渲染。长会话数千条消息时每个工具完成都 O(n) 重建。
- **影响**: CPU 线性增长，长会话逐渐卡顿。
- **修复**: 增量渲染——只从最后修改的条目追加，而非重建全部内容。

---

### 14. 每 300ms 动画帧全量重建 viewport

- **文件**: `internal/tui/tui_render.go:143-145` (`rebuildViewportPreservingScroll`)
- **分类**: 性能
- **描述**: 动画 tick 每 3 帧（300ms）触发全量重建，与 issue 13 同样 O(n)。
- **影响**: 长会话持续 CPU 占用。
- **修复**: 仅重建动画中条目的行，或使用独立动画覆盖层。

---

### 15. resize 时大规模缓存失效

- **文件**: `internal/tui/tui_render.go:98-105`
- **分类**: 性能
- **描述**: `invalidateRenderedCache()` 在每次 `WindowSizeMsg` 时清空所有 `entry.rendered`。拖拽窗口产生密集 resize 事件，触发大量重渲染。
- **影响**: 窗口 resize 时短暂卡顿。
- **修复**: 延迟失效——使用 generation counter，仅在渲染时检查。

---

### 16. `publisherCh` 重复创建，旧 channel 泄漏

- **文件**: `internal/tui/tui_tasks.go:71,103`
- **分类**: 内存泄漏
- **描述**: 每次 submit 都 `m.publisherCh = make(chan ...)`，旧 channel 被抛弃。
- **影响**: 轻微内存泄漏。
- **修复**: 复用同一 channel 或 nil 检查。

---

### 17. `restoreSession` 无防抖保护

- **文件**: `internal/tui/tui_history.go:269`
- **分类**: 竞态
- **描述**: `logEntries = nil` 后重建，快速双击可能触发两次 restore 互相覆盖。
- **影响**: 丢失部分会话上下文。
- **修复**: 添加 `m.historyLoading` 防抖 guard。

---

### 18. 退出 history 模式时 pageSize 置零

- **文件**: `internal/tui/tui_history.go:369`
- **分类**: 潜在 panic
- **描述**: `exitHistoryMode` 设 `historyPageSize = 0`。若其他路径在非 history 模式调用分页函数则除零。
- **影响**: 低概率 panic。
- **修复**: 不重置 `historyPageSize` 或在分页函数加零值保护。

---

### 19. `left` 键使用 `+1` 而非 `-1`

- **文件**: `internal/tui/tui_update.go:364-366 (+ confirmCancelDialog)`
- **分类**: 逻辑 BUG（潜伏）
- **描述**: confirmQuitDialog 和 confirmCancelDialog 的 left 键使用 `+1 % 2` 而非 `-1 % 2`，在 2 选项下碰巧等价。
- **影响**: 当前无害，选项数 >2 时才出错。
- **修复**: 改为 `(selectedOption - 1 + N) % N`。

---

### 20. `:q!` 被当作普通 `:q` 处理

- **文件**: `internal/tui/tui_update.go:18-19`
- **分类**: UX
- **描述**: `case cmd == ":q" || cmd == ":quit" || cmd == ":q!":` 三个命令进入同一分支，都弹出确认弹窗。`!` 后缀被忽略。
- **影响**: Vim 用户期望 `:q!` 跳过确认直接退出。
- **修复**: 为 `:q!` 添加独立分支直接 `tea.Quit`。

---

### 21. `Anim.Render()` 无同步保护

- **文件**: `internal/tui/anim.go:85-111`
- **分类**: 代码质量
- **描述**: `Render()` 方法读写内部状态无锁。Bubble Tea 单 goroutine 下安全，但无文档说明。
- **影响**: 未来多 goroutine 场景可能 data race。
- **修复**: 添加文档注释说明单 goroutine 约束，或加 mutex。

---

### 22. `langManager` 全局可变状态

- **文件**: `internal/tui/tui_model.go:25`
- **分类**: 代码质量
- **描述**: `var langManager *LanguageManager` 是包级全局变量，无同步保护。
- **影响**: 无法并行测试不同语言。
- **修复**: 通过 model struct 注入，而非全局变量。

---

## 统计

| 严重度 | 数量 |
|--------|------|
| 高 | 5 |
| 中 | 6 |
| 低 | 10 |
| **总计** | **21** |

| 文件 | 问题数 |
|------|--------|
| `tui_update.go` | 7 |
| `tui_dialogs.go` | 4 |
| `tui_render.go` | 4 |
| `tui_tasks.go` | 3 |
| `tui_fzf.go` | 2 |
| `tui_history.go` | 2 |
| `tui_model.go` | 2 |
| `anim.go` | 1 |
| `render.go` | 0 |
