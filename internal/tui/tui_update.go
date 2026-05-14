package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"codeactor/internal/compact"
	"codeactor/internal/tui/components"

	tea "charm.land/bubbletea/v2"
)

// autocompleteMsg is sent by the debounce timer to trigger autocomplete computation.
type autocompleteMsg struct{}

// ─────────────────────────────────────────────────────────────────────────────
// 补全防抖与缓存辅助函数
// ─────────────────────────────────────────────────────────────────────────────

// shouldAttemptCompletion 检查是否有触发补全的条件
// 优化：仅检查基本状态和光标位置，避免调用 m.input.Value()
// 实际字符检查在 scheduleAutocomplete 中进行
func (m *model) shouldAttemptCompletion() bool {
	if m.commandMode || m.taskRunning {
		return false
	}

	// 快速检查：光标在有效范围内（Column 从 0 开始，0 表示行首）
	// 如果 Column > 0 或者 Line > 0，说明光标不在文档开头
	column := m.input.Column()
	if column > 0 {
		return true
	}

	// Column == 0 时，如果 Line > 0，说明光标在后续行的行首，也是有效的
	if m.input.Line() > 0 {
		return true
	}

	// 光标在文档开头（Line=0, Column=0），不触发补全
	return false
}

// clearAutocomplete 清除补全状态
func (m *model) clearAutocomplete() {
	m.skillAutoComplete = false
	m.skillSuggestions = nil
	m.skillSuggestionIdx = 0
	m.keywordAutoComplete = false
	m.keywordSuggestions = nil
	m.keywordSuggestionIdx = 0
}

// scheduleAutocomplete 启动或重置补全防抖
// 优化：
// 1. 使用细粒度缓存键（单词+是否有/）提高命中率
// 2. 只调用一次 Value() 和 Line()/Column()
// 3. 共享 rune 切片给后续函数
// 4. 防抖时间增加到 100ms
func (m *model) scheduleAutocomplete() tea.Cmd {
	// 提前退出检查：无触发条件时直接清除补全
	if !m.shouldAttemptCompletion() {
		m.clearAutocomplete()
		return nil
	}

	// 获取当前输入快照（只调用一次）
	text := m.input.Value()
	line := m.input.Line()
	column := m.input.Column()

	// 提取光标前的单词和检查是否有 /（只转换一次 rune 切片）
	contentRunes := []rune(text)
	word := extractWordAtCursorRunes(contentRunes, column)
	hasSlash := strings.Contains(text, "/")

	cacheKey := autocompleteCacheKey{word: word, hasSlash: hasSlash}

	// 检查缓存
	if cached, ok := m.autocompleteCache[cacheKey]; ok {
		// 缓存命中，直接应用结果
		result := *cached
		m.skillSuggestions = result.skillSuggestions
		m.skillSuggestionIdx = result.skillSuggestionIdx
		m.keywordSuggestions = result.keywordSuggestions
		m.keywordSuggestionIdx = result.keywordSuggestionIdx
		return nil
	}

	// 缓存未命中，启动或重置防抖
	if m.debounceTimer != nil {
		m.debounceTimer.Stop()
		// drain timer channel to prevent leak
		select {
		case <-m.debounceTimer.C:
		default:
		}
	}

	// 保存快照
	m.snapshotText = text
	m.snapshotCursor = line*10000 + column // 编码 line 和 column
	m.pendingAutocomplete = true

	// 启动 100ms 防抖（从 50ms 增加到 100ms，减少快速输入时的计算频率）
	m.debounceTimer = time.NewTimer(100 * time.Millisecond)

	// 返回 Cmd 用于在防抖超时后发送 autocompleteMsg
	return func() tea.Msg {
		// 等待 100ms 后发送消息
		return autocompleteMsg{}
	}
}

// doAutocomplete 执行补全计算（接收已提取的文本和光标，避免重复调用 Value()）
// 优化：共享 rune 切片，避免在多个函数中重复转换
func (m *model) doAutocomplete(text string, cursor int) {
	// 只转换一次 rune 切片
	contentRunes := []rune(text)

	// 执行技能补全
	m.doSkillAutocomplete(text, contentRunes, cursor)

	// 执行关键词补全
	m.doKeywordAutocomplete(text, contentRunes, cursor)
}

// doSkillAutocomplete 执行技能补全计算
// 优化：接收 contentRunes 参数（为未来优化预留）
func (m *model) doSkillAutocomplete(text string, contentRunes []rune, cursor int) {
	// 仅在编辑模式且任务未运行时激活
	if m.commandMode || m.taskRunning {
		m.skillAutoComplete = false
		m.skillSuggestions = nil
		m.skillSuggestionIdx = 0
		return
	}

	// 查找最后一个 '/'
	lastSlash := strings.LastIndex(text, "/")
	if lastSlash < 0 {
		m.skillAutoComplete = false
		m.skillSuggestions = nil
		m.skillSuggestionIdx = 0
		return
	}

	// 提取 '/' 之后的文本作为查询
	query := text[lastSlash+1:]

	// 构建匹配的技能列表
	var matches []string
	if m.assistant.SkillRegistry != nil {
		allSkills := m.assistant.SkillRegistry.List()
		matches = make([]string, 0, len(allSkills)+1)
		for _, name := range allSkills {
			if hasPrefixIgnoreCase(name, query) {
				matches = append(matches, name)
			}
		}

		// 添加 "history" 作为内置命令
		if hasPrefixIgnoreCase("history", query) {
			matches = append([]string{"history"}, matches...)
		}
	}

	if len(matches) > 0 {
		m.skillAutoComplete = true
		m.skillSuggestions = matches
		// 重置索引如果超出范围
		if m.skillSuggestionIdx >= len(matches) {
			m.skillSuggestionIdx = 0
		}
	} else {
		m.skillAutoComplete = false
		m.skillSuggestions = nil
		m.skillSuggestionIdx = 0
	}
}

// doKeywordAutocomplete 执行关键词补全计算
// 优化：
// 1. 接收 contentRunes 参数，避免重复转换
// 2. 如果光标在第一行，避免调用 splitLogicalLines
func (m *model) doKeywordAutocomplete(text string, contentRunes []rune, cursor int) {
	// 仅在编辑模式且任务未运行时激活
	if m.commandMode || m.taskRunning {
		m.keywordAutoComplete = false
		m.keywordSuggestions = nil
		m.keywordSuggestionIdx = 0
		return
	}

	// 将编码的 cursor 解码为实际的光标位置（字节偏移量）
	// cursor = line*10000 + column
	line := cursor / 10000
	column := cursor % 10000

	// 计算光标前的字节偏移量
	byteOffset := 0
	if line > 0 {
		// 只有在多行时才调用 splitLogicalLines
		lines := splitLogicalLines(contentRunes, line)
		for _, l := range lines {
			byteOffset += len(l) + 1 // +1 for newline
		}
	}
	byteOffset += column

	// 提取光标前的单词
	word := extractWordAtCursorRunes(contentRunes, byteOffset)

	// 仅在单词非空且无特殊前缀时触发
	if word == "" || strings.HasPrefix(word, "/") {
		m.keywordAutoComplete = false
		m.keywordSuggestions = nil
		m.keywordSuggestionIdx = 0
		return
	}

	// 从关键词字典获取补全建议
	suggestions := GetKeywordCompletions(m, word, 20)

	if len(suggestions) > 0 {
		m.keywordAutoComplete = true
		m.keywordSuggestions = suggestions
		m.keywordSuggestionIdx = 0
	} else {
		m.keywordAutoComplete = false
		m.keywordSuggestions = nil
		m.keywordSuggestionIdx = 0
	}
}

// cleanupDebounceTimer 清理防抖定时器（在退出时调用）
func (m *model) cleanupDebounceTimer() {
	if m.debounceTimer != nil {
		m.debounceTimer.Stop()
		// drain timer channel to prevent leak
		select {
		case <-m.debounceTimer.C:
		default:
		}
		m.debounceTimer = nil
	}
}

func (m *model) processCommand(cmd string) tea.Cmd {
	cmd = strings.TrimSpace(cmd)

	switch {
	case cmd == ":q!":
		// Force quit — skip confirmation (vim convention)
		m.quitting = true
		m.cleanupDebounceTimer()
		return tea.Quit
	case cmd == ":q" || cmd == ":quit":
		if m.dialogStack != nil {
			d := components.NewQuitConfirmDialogForQuit(components.Language(m.currentLang))
			d.SetBounds(m.termWidth, m.termHeight)
			m.dialogStack.Push(d)
		} else {
			m.confirmQuitDialog.open = true
		}
	case strings.HasPrefix(cmd, "/"):
		// Search in log entries
		query := strings.TrimPrefix(cmd, "/")
		m.searchInLog(query)
	case cmd == ":help" || cmd == ":h":
		if m.dialogStack != nil {
			if m.showHelpDialog {
				// 关闭帮助弹窗（从栈中移除）
				m.dialogStack.CloseDialog("help_dialog")
			} else {
				// 打开帮助弹窗
				m.dialogStack.Push(components.NewHelpDialog(components.Language(m.currentLang)))
			}
			m.showHelpDialog = !m.showHelpDialog
		} else {
			m.showHelpDialog = true
		}
	case cmd == ":mode":
		mode := "COMMAND"
		if !m.commandMode {
			mode = "EDIT"
		}
		m.logEntries = append(m.logEntries, logEntry{
			timestamp: time.Now(),
			eventType: "status",
			content:   fmt.Sprintf("Current mode: %s | Task running: %v | Buffer: %q", mode, m.taskRunning, m.commandBuffer),
		})
		m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
	case cmd == ":hist" || cmd == ":history":
		if m.taskRunning {
			m.infoMsg = "Cannot browse history while a task is running"
			return nil
		}
		if !m.commandMode {
			// Switch to command mode first since history is accessed from there
			m.commandMode = true
			m.commandBuffer = ""
		}
		return enterHistoryMode(m)
	default:
		m.infoMsg = fmt.Sprintf("Unknown command: %s (type :help or ? for available commands)", cmd)
	}
	return nil
}

// searchInLog highlights entries containing the query string.
func (m *model) searchInLog(query string) {
	queryLower := strings.ToLower(query)
	found := 0
	for i := range m.logEntries {
		if strings.Contains(strings.ToLower(m.logEntries[i].content), queryLower) {
			found++
		}
	}
	m.logEntries = append(m.logEntries, logEntry{
		timestamp: time.Now(),
		eventType: "status",
		content:   fmt.Sprintf("Search '/%s': %d matches", query, found),
	})
	m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Global popup guard: when any overlay is shown, only allow KeyMsg through.
	// taskEventMsg is allowed through so the listenForEvents chain stays alive;
	// its handler drops the event when dialogs are open but reschedules the chain.
	// tickMsg is intercepted here and the tick is re-scheduled (not processed)
	// so animations keep running without touching the hidden viewport.
	// Other non-KeyMsg events (WindowSizeMsg, MouseMsg, etc.) also keep both
	// chains alive when the task is running, to prevent permanent TUI freeze.
	if m.showHelpDialog || m.confirmDialog.open ||
		m.taskCompleteDialog.open || m.confirmQuitDialog.open || m.confirmCancelDialog.open ||
		(m.dialogStack != nil && m.dialogStack.Len() > 0) {
		if _, ok := msg.(tea.KeyMsg); !ok {
			switch msg.(type) {
			case taskEventMsg:
				// Let through — the handler's defensive check keeps chains alive
			case tickMsg:
				// Keep ticking for animation, but don't touch the viewport
				if m.taskRunning {
					return m, tickCmd()
				}
				return m, nil
			default:
				// WindowSizeMsg, MouseMsg, publisherReadyMsg, etc.
				if m.taskRunning {
					return m, tea.Batch(listenForEvents(m.eventCh), tickCmd())
				}
				return m, nil
			}
		}
	}

	// History mode: intercept all messages and delegate to history handler
	if m.historyMode {
		return historyUpdate(msg, &m)
	}

	// ====== 新组件：弹窗栈优先处理 ======
	if m.dialogStack != nil && m.dialogStack.Len() > 0 {
		topDialog := m.dialogStack.Top()
		if topDialog != nil {
			newComp, cmd := topDialog.Update(msg)
			if newComp != nil {
				// 更新栈顶弹窗
				m.dialogStack.ReplaceTop(newComp.(components.Dialog))
			}
			if cmd != nil {
				return m, cmd
			}
			// 弹窗已处理消息，但仍需继续传递 KeyMsg 到后续 switch 处理（如确认退出逻辑）
			// 注意：此处不再 return m, nil，否则 KeyMsg 会被吞掉，导致 tea.Quit 无法执行
		}
	}

	switch msg := msg.(type) {
	case tickMsg:
		// Advance animation and rebuild viewport if there are running tools
		m.animFrame++

		// 通知动画管理器推进可见动画的帧
		if m.animManager != nil {
			// 使用 Tick 方法直接推进帧（100ms 间隔）
			m.animManager.Tick(100)
		}

		if m.activeAnim || m.taskRunning {
			m.anim.Tick()
			// Throttle viewport rebuild to every 3 ticks (~300ms) to avoid
			// flooding viewport.SetContent() — the #1 cause of scroll lag.
			if m.animFrame%3 == 0 {
				if m.activeAnim {
					for _, te := range m.toolCallEntries {
						if te.Status == ToolStatusRunning {
							te.InvalidateCache()
						}
					}
				}
				m.rebuildViewportPreservingScroll()
			}
		}
		// Don't schedule next tick if any popup/dialog is showing
		if m.showHelpDialog || m.confirmDialog.open ||
			m.taskCompleteDialog.open || m.confirmQuitDialog.open || m.confirmCancelDialog.open ||
			(m.dialogStack != nil && m.dialogStack.Len() > 0) {
			return m, nil
		}
		// Continue ticking so that the animation resumes immediately
		// when activeAnim becomes true — never let the tick die.
		return m, tickCmd()

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		m.input.SetWidth(m.computeFieldWidth())
		m.resizeViewport()
		m.invalidateRenderedCache()
		m.rebuildViewportScrollLock()
		// 更新布局引擎
		if m.layoutEngine != nil {
			m.layoutEngine.Resize(msg.Width, msg.Height)
		}
		return m, nil

	case publisherReadyMsg:
		m.publisher = msg.publisher
		return m, nil

	case tea.MouseMsg:
		if m.mouseHandler != nil {
			action, x, y := m.mouseHandler.Detect(msg)
			switch action {
			case components.MouseScrollUp:
				// 滚动向上（viewport 减少行）
				m.viewport.ScrollUp(3)
			case components.MouseScrollDown:
				// 滚动向下（viewport 增加行）
				m.viewport.ScrollDown(3)
			case components.MouseClick:
				// 单击：检测是否点击了弹窗按钮
				m.handleMouseClick(x, y)
			case components.MouseDoubleClick:
				// 双击：选词（暂时占位）
				_ = x
				_ = y
			case components.MouseTripleClick:
				// 三击：选行（暂时占位）
				_ = x
				_ = y
			case components.MouseDragStart:
				// 拖拽开始（暂时占位）
			case components.MouseDragMove:
				// 拖拽移动（暂时占位）
			case components.MouseDragEnd:
				// 拖拽结束（暂时占位）
			}
		}
		return m, nil

	case tea.KeyMsg:

		// ====== DialogStack 键处理：优先于所有旧弹窗 ======
		if m.dialogStack != nil && m.dialogStack.Len() > 0 {
			top := m.dialogStack.Top()
			if top != nil {
				key := msg.String()

				// Ctrl+C: 强制退出
				if key == "ctrl+c" {
					m.quitting = true
					return m, tea.Quit
				}

				// 根据弹窗类型处理确认操作
				switch d := top.(type) {
				case *components.ConfirmDialog:
					switch key {
					case "enter":
						action := d.GetResponseAction()
						m.respondToAuth(action)
						return m, nil
					}
					return m, nil

				case *components.QuitConfirmDialog:
					switch key {
					case "enter", "y", "Y":
						if d.GetConfirmed() {
							if d.ID() == "quit_confirm" || d.ID() == "quit_confirm_dialog" {
								m.quitting = true
								m.cleanupDebounceTimer()
								return m, tea.Quit
							}
							// Cancel confirmation
							if m.currentTask != nil && m.currentTask.CancelFunc != nil {
								m.taskCancelled = true
								m.currentTask.CancelFunc()
								m.logEntries = append(m.logEntries, logEntry{
									timestamp: time.Now(),
									eventType: "status",
									content:   "Task cancelled by user",
								})
								m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
							}
						}
						m.dialogStack.Pop()
						return m, nil
					case "esc", "n", "N":
						d.SetConfirmed(false)
						d.SelectedIndex = 0
						m.dialogStack.Pop()
						return m, nil
					}
					return m, nil

				case *components.HelpDialog:
					switch key {
					case "esc", "i", "I":
						m.dialogStack.Pop()
						m.showHelpDialog = false
						return m, nil
					}
					return m, nil

				case *components.TaskCompleteDialog:
					switch key {
					case "enter", " ", "esc":
						m.dialogStack.Pop()
						return m, nil
					}
					return m, nil
				}
			}
		}

		// Confirmation dialog key handling (fallback for old dialog) — takes priority over everything
		if m.confirmDialog.open {
			switch msg.String() {
			case "ctrl+c":
				m.quitting = true
				m.cleanupDebounceTimer()
				return m, tea.Quit
			case "down", "tab":
				m.confirmDialog.selectedOption = (m.confirmDialog.selectedOption + 1) % 5
				return m, nil
			case "up", "k":
				m.confirmDialog.selectedOption = (m.confirmDialog.selectedOption + 4) % 5
				return m, nil
			case "enter":
				switch m.confirmDialog.selectedOption {
				case 0:
					m.respondToAuth("allow")
				case 1:
					m.respondToAuth("allow_session")
				case 2:
					m.respondToAuth("allow_all_session")
				case 3:
					m.respondToAuth("allow_all_project")
				case 4:
					m.respondToAuth("deny")
				}
				return m, nil
			case "a", "A":
				m.respondToAuth("allow")
				return m, nil
			case "t", "T":
				m.respondToAuth("allow_session")
				return m, nil
			case "s", "S":
				m.respondToAuth("allow_all_session")
				return m, nil
			case "p", "P":
				m.respondToAuth("allow_all_project")
				return m, nil
			case "d", "D", "esc":
				m.respondToAuth("deny")
				return m, nil
			}
			return m, nil
		}

		// Task complete dialog key handling
		if m.taskCompleteDialog.open {
			switch msg.String() {
			case "enter", " ", "esc":
				m.taskCompleteDialog.open = false
				return m, nil
			case "ctrl+c":
				m.quitting = true
				m.cleanupDebounceTimer()
				return m, tea.Quit
			}
			return m, nil
		}

		// Quit confirmation dialog key handling
		if m.confirmQuitDialog.open {
			switch msg.String() {
			case "ctrl+c":
				m.quitting = true
				m.cleanupDebounceTimer()
				return m, tea.Quit
			case "right", "tab":
				m.confirmQuitDialog.selectedOption = (m.confirmQuitDialog.selectedOption + 1) % 2
				return m, nil
			case "left":
				m.confirmQuitDialog.selectedOption = (m.confirmQuitDialog.selectedOption + 1) % 2
				return m, nil
			case "enter":
				if m.confirmQuitDialog.selectedOption == 0 {
					m.quitting = true
					m.cleanupDebounceTimer()
					return m, tea.Quit
				}
				m.confirmQuitDialog.open = false
				m.confirmQuitDialog.selectedOption = 0
				return m, nil
			case "y", "Y":
				m.quitting = true
				m.cleanupDebounceTimer()
				return m, tea.Quit
			case "n", "N", "esc":
				m.confirmQuitDialog.open = false
				m.confirmQuitDialog.selectedOption = 0
				return m, nil
			}
			return m, nil
		}

		// Cancel task confirmation dialog key handling
		if m.confirmCancelDialog.open {
			switch msg.String() {
			case "ctrl+c":
				m.quitting = true
				m.cleanupDebounceTimer()
				return m, tea.Quit
			case "right", "tab":
				m.confirmCancelDialog.selectedOption = (m.confirmCancelDialog.selectedOption + 1) % 2
				return m, nil
			case "left":
				m.confirmCancelDialog.selectedOption = (m.confirmCancelDialog.selectedOption + 1) % 2
				return m, nil
			case "enter":
				if m.confirmCancelDialog.selectedOption == 0 {
					// Confirm cancel
					if m.currentTask != nil && m.currentTask.CancelFunc != nil {
						m.taskCancelled = true
						m.currentTask.CancelFunc()
						m.logEntries = append(m.logEntries, logEntry{
							timestamp: time.Now(),
							eventType: "status",
							content:   "Task cancelled by user",
						})
						m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
					}
				}
				m.confirmCancelDialog.open = false
				m.confirmCancelDialog.selectedOption = 0
				return m, nil
			case "y", "Y":
				if m.currentTask != nil && m.currentTask.CancelFunc != nil {
					m.taskCancelled = true
					m.currentTask.CancelFunc()
					m.logEntries = append(m.logEntries, logEntry{
						timestamp: time.Now(),
						eventType: "status",
						content:   "Task cancelled by user",
					})
					m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
				}
				m.confirmCancelDialog.open = false
				m.confirmCancelDialog.selectedOption = 0
				return m, nil
			case "n", "N", "esc":
				m.confirmCancelDialog.open = false
				m.confirmCancelDialog.selectedOption = 0
				return m, nil
			}
			return m, nil
		}

		// ── Command mode key handling (vim-like: hidden input, single-key commands) ──
		if m.commandMode {
			// Resolve multi-key sequences: check if lastKey + current key forms a valid combo
			key := msg.String()
			if m.lastKey != "" {
				combo := m.lastKey + key
				m.lastKey = ""
				switch combo {
				case "gg":
					m.viewport.GotoTop()
					return m, nil
				default:
					// Invalid combo: discard lastKey and fall through to process key normally
				}
			}

			// Help dialog is open: let action keys (i/esc/enter/?/ctrl+c) pass through,
			// dismiss on any other key without processing it.
			if m.showHelpDialog && key != "i" && key != "ctrl+e" && key != "enter" && key != "?" && key != "ctrl+c" {
				// 如果 dialogStack 中有 help_dialog，从栈中关闭
				if m.dialogStack != nil && m.showHelpDialog {
					m.dialogStack.CloseDialog("help_dialog")
				}
				m.showHelpDialog = false
				return m, nil
			}

			switch key {
			case "ctrl+c":
				if m.dialogStack != nil {
					d := components.NewQuitConfirmDialogForQuit(components.Language(m.currentLang))
					d.SetBounds(m.termWidth, m.termHeight)
					m.dialogStack.Push(d)
				} else {
					m.confirmQuitDialog.open = true
				}
				return m, nil

			case "esc":
				if m.showHelpDialog {
					// 如果 dialogStack 中有 help_dialog，从栈中关闭
					if m.dialogStack != nil {
						m.dialogStack.CloseDialog("help_dialog")
					}
					m.showHelpDialog = false
					return m, nil
				}
				if m.commandBuffer != "" {
					// Clear command buffer, stay in command mode
					m.commandBuffer = ""
					return m, nil
				}
				if m.taskRunning && m.currentTask != nil && m.currentTask.CancelFunc != nil {
					// Show cancel confirmation dialog
					if m.dialogStack != nil {
						d := components.NewQuitConfirmDialogForCancel(components.Language(m.currentLang))
						d.SetBounds(m.termWidth, m.termHeight)
						m.dialogStack.Push(d)
					} else {
						m.confirmCancelDialog.open = true
					}
				}
				return m, nil

			case "i":
				// Block switching to edit mode while task is running
				if m.taskRunning {
					m.infoMsg = "Task is running, cannot switch to edit mode"
					return m, nil
				}
				// Enter edit mode (vim-like: press i to insert)
				m.commandMode = false
				m.commandBuffer = ""
				m.lastKey = ""
				m.showHelpDialog = false
				return m, nil

			case "enter":
				if m.showHelpDialog {
					m.showHelpDialog = false
					return m, nil
				}
				// Process command buffer if non-empty, otherwise enter edit mode
				if m.commandBuffer != "" {
					cmd := m.processCommand(m.commandBuffer)
					m.commandBuffer = ""
					if cmd != nil {
						return m, cmd
					}
				} else if !m.taskRunning {
					// Only enter edit mode if task is not running
					m.commandMode = false
				}
				return m, nil

			// ── Scroll navigation ──
			case "f":
				m.viewport.PageDown()
				return m, nil

			case "b":
				m.viewport.PageUp()
				return m, nil

			case "j", "down":
				m.viewport.ScrollDown(1)
				return m, nil

			case "k", "up":
				m.viewport.ScrollUp(1)
				return m, nil

			case "G":
				// Vim: Shift+G → go to bottom
				m.viewport.GotoBottom()
				return m, nil

			// ── Multi-key prefix: g (for gg) ──
			case "g":
				if m.commandBuffer == "" {
					m.lastKey = key
				} else {
					m.commandBuffer += key
				}
				return m, nil

			// ── Command line prefixes ──
			case ":":
				if m.commandBuffer == "" {
					m.commandBuffer = ":"
				} else {
					m.commandBuffer += ":"
				}
				return m, nil

			case "/":
				if m.commandBuffer == "" {
					m.commandBuffer = "/"
				} else {
					m.commandBuffer += "/"
				}
				return m, nil

			// ── Help overlay ──
			case "?":
				if m.commandBuffer == "" {
					if m.dialogStack != nil {
						if m.showHelpDialog {
							// 关闭帮助弹窗（从栈中移除）
							m.dialogStack.CloseDialog("help_dialog")
						} else {
							// 打开帮助弹窗
							m.dialogStack.Push(components.NewHelpDialog(components.Language(m.currentLang)))
						}
						m.showHelpDialog = !m.showHelpDialog
					} else {
						m.showHelpDialog = !m.showHelpDialog
					}
				} else {
					m.commandBuffer += "?"
				}
				return m, nil

			// ── Command buffer editing ──
			case "backspace":
				if len(m.commandBuffer) > 0 {
					m.commandBuffer = m.commandBuffer[:len(m.commandBuffer)-1]
				}
				return m, nil

			// ── Misc ──
			case "ctrl+l":
				m.toggleLanguage()
				return m, nil

			default:
				// Append printable characters to command buffer (hidden input)
				if len(msg.Key().Text) > 0 {
					m.commandBuffer += msg.Key().Text
					return m, nil
				}
				// Pass to viewport for scrolling
				var vpCmd tea.Cmd
				m.viewport, vpCmd = m.viewport.Update(msg)
				return m, vpCmd
			}
		}

		// ── Edit mode key handling ──
		switch msg.String() {
		case "ctrl+c":
			if m.dialogStack != nil {
				d := components.NewQuitConfirmDialogForQuit(components.Language(m.currentLang))
				d.SetBounds(m.termWidth, m.termHeight)
				m.dialogStack.Push(d)
			} else {
				m.confirmQuitDialog.open = true
			}
			return m, nil

		case "esc":
			// Dismiss keyword autocomplete if active
			if m.keywordAutoComplete {
				m.keywordAutoComplete = false
				m.keywordSuggestions = nil
				m.keywordSuggestionIdx = 0
				return m, nil
			}
			// Dismiss skill autocomplete if active
			if m.skillAutoComplete {
				m.skillAutoComplete = false
				m.skillSuggestions = nil
				m.skillSuggestionIdx = 0
				return m, nil
			}
			// Show cancel confirmation dialog if task is running
			if m.taskRunning && m.currentTask != nil && m.currentTask.CancelFunc != nil {
				if m.dialogStack != nil {
					d := components.NewQuitConfirmDialogForCancel(components.Language(m.currentLang))
					d.SetBounds(m.termWidth, m.termHeight)
					m.dialogStack.Push(d)
				} else {
					m.confirmCancelDialog.open = true
				}
			}
			return m, nil

		case "ctrl+e":
			// Enter command mode
			m.commandMode = true
			m.commandBuffer = ""
			return m, nil

		case "ctrl+s":
			if m.taskRunning {
				return m, nil
			}
			taskDesc := strings.TrimSpace(m.input.Value())
			if taskDesc == "" {
				return m, nil
			}
			if ok, errStr := validateInputs(m.projectDir, taskDesc); !ok {
				m.errMsg = errStr
				return m, nil
			}
			if m.currentTask != nil {
				if !m.taskRunning {
					// 任务已完成或空闲，允许提交 follow-up
					return m, m.submitFollowUp(taskDesc)
				}
				// 任务正在运行，不处理
				return m, nil
			}
			return m, m.submitTask()

		case "ctrl+l":
			m.toggleLanguage()
			return m, nil

		case "ctrl+f":
			m.viewport.PageDown()
			return m, nil

		case "ctrl+b":
			m.viewport.PageUp()
			return m, nil

		case "tab":
			// Cycle through keyword autocomplete suggestions first
			if m.keywordAutoComplete && len(m.keywordSuggestions) > 0 {
				m.keywordSuggestionIdx = (m.keywordSuggestionIdx + 1) % len(m.keywordSuggestions)
				return m, nil
			}
			// Cycle through skill autocomplete suggestions
			if m.skillAutoComplete && len(m.skillSuggestions) > 0 {
				m.skillSuggestionIdx = (m.skillSuggestionIdx + 1) % len(m.skillSuggestions)
				return m, nil
			}
			// No autocomplete active: pass tab to textarea
			var inputCmd tea.Cmd
			m.input, inputCmd = m.input.Update(msg)
			return m, inputCmd

		case "enter":
			// If keyword autocomplete is active, insert the selected keyword at cursor
			if m.keywordAutoComplete && len(m.keywordSuggestions) > 0 && m.keywordSuggestionIdx >= 0 && m.keywordSuggestionIdx < len(m.keywordSuggestions) {
				keyword := m.keywordSuggestions[m.keywordSuggestionIdx]
				content := m.input.Value()

				// Calculate byte offset from cursor line and column
				cursorLine := m.input.Line()
				cursorCol := m.input.Column()

				// Calculate byte offset: sum of lengths of all previous lines + current column
				contentRunes := []rune(content)
				byteOffset := 0
				if cursorLine > 0 {
					// Split content into logical lines and sum their lengths (+1 for newline)
					lines := splitLogicalLines(contentRunes, cursorLine-1)
					for _, line := range lines {
						byteOffset += len(line) + 1 // +1 for newline character
					}
				}
				// Add current column (which is the character offset within the current line)
				if cursorCol <= len(contentRunes)-byteOffset {
					byteOffset += cursorCol
				}

				// Find the word boundary before cursor to replace the current word
				wordStart := byteOffset
				for wordStart > 0 {
					prevRune := contentRunes[wordStart-1]
					if (prevRune >= 'a' && prevRune <= 'z') || (prevRune >= 'A' && prevRune <= 'Z') ||
						(prevRune >= '0' && prevRune <= '9') || prevRune == '_' ||
						(prevRune >= 0x4e00 && prevRune <= 0x9fff) {
						wordStart--
					} else {
						break
					}
				}

				// Build new content: replace word at cursor with selected keyword
				var newContent []rune
				newContent = append(newContent, contentRunes[:wordStart]...)
				newContent = append(newContent, []rune(keyword)...)
				newContent = append(newContent, contentRunes[byteOffset:]...)

				m.input.SetValue(string(newContent))

				// Reset autocomplete state
				m.keywordAutoComplete = false
				m.keywordSuggestions = nil
				m.keywordSuggestionIdx = 0
				return m, nil
			}
			// If skill autocomplete is active, expand the selected skill
			if m.skillAutoComplete && len(m.skillSuggestions) > 0 && m.skillSuggestionIdx >= 0 && m.skillSuggestionIdx < len(m.skillSuggestions) {
				skillName := m.skillSuggestions[m.skillSuggestionIdx]
				// If "history" is selected, open history dialog instead of executing a skill
				if skillName == "history" {
					m.skillAutoComplete = false
					m.skillSuggestions = nil
					m.skillSuggestionIdx = 0
					if m.taskRunning {
						m.infoMsg = "Cannot browse history while a task is running"
						return m, nil
					}
					// Clear the input field
					m.input.SetValue("")
					return m, enterHistoryMode(&m)
				}
				if skill, ok := m.assistant.SkillRegistry.Get(skillName); ok {
					userContext := strings.TrimSpace(m.input.Value())
					// Combine skill content with user's input as context
					combinedContent := skill.Content + "\n\n---\n用户上下文: " + userContext
					m.skillAutoComplete = false
					m.skillSuggestions = nil
					m.skillSuggestionIdx = 0
					m.infoMsg = fmt.Sprintf("正在执行 skill: %s", skill.Name)
					return m, m.submitTaskWithContent(combinedContent)
				}
			}
			// Otherwise, insert newline into textarea
			var inputCmd tea.Cmd
			m.input, inputCmd = m.input.Update(msg)
			// 启动或重置补全防抖
			return m, tea.Batch(inputCmd, m.scheduleAutocomplete())

		case "/":
			// Pass '/' to textarea normally, then check for skill autocomplete
			var inputCmd tea.Cmd
			m.input, inputCmd = m.input.Update(msg)
			return m, tea.Batch(inputCmd, m.scheduleAutocomplete())

		case "@":
			// Trigger fzf file fuzzy finder
			// First, let @ be inserted into textarea, then start fzf
			var inputCmd tea.Cmd
			m.input, inputCmd = m.input.Update(msg)
			return m, tea.Batch(
				inputCmd,
				runFzfCmd(m.projectDir),
			)

		case "up", "down":
			// Input has content: pass to textarea for line navigation
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd

		default:
			// Only update input — viewport scrolling keys (ctrl+f, ctrl+b)
			// are handled in dedicated case branches above.
			var inputCmd tea.Cmd
			m.input, inputCmd = m.input.Update(msg)
			// 启动或重置补全防抖
			return m, tea.Batch(inputCmd, m.scheduleAutocomplete())
		}

	case autocompleteMsg:
		if !m.pendingAutocomplete {
			return m, nil
		}

		// 检查快照是否仍然有效
		if currentText := m.input.Value(); currentText != m.snapshotText {
			// 输入已变化，重新调度
			m.scheduleAutocomplete()
			return m, nil
		}

		// 执行补全计算
		m.doAutocomplete(m.snapshotText, m.snapshotCursor)

		// 存入缓存 - 使用细粒度缓存键（基于单词和是否有/）
		contentRunes := []rune(m.snapshotText)
		column := m.input.Column()
		word := extractWordAtCursorRunes(contentRunes, column)
		hasSlash := strings.Contains(m.snapshotText, "/")
		cacheKey := autocompleteCacheKey{word: word, hasSlash: hasSlash}
		m.autocompleteCache[cacheKey] = &AutocompleteResult{
			skillSuggestions:     m.skillSuggestions,
			skillSuggestionIdx:   m.skillSuggestionIdx,
			keywordSuggestions:   m.keywordSuggestions,
			keywordSuggestionIdx: m.keywordSuggestionIdx,
		}

		// 清理缓存大小（防止内存泄漏）
		if len(m.autocompleteCache) > 64 {
			m.autocompleteCache = make(map[autocompleteCacheKey]*AutocompleteResult)
		}

		m.pendingAutocomplete = false
		return m, nil

	case taskEventMsg:
		// Don't process task events while any popup/dialog is showing.
		// Keep the event chain alive so the TUI resumes after dialog dismissal.
		if m.showHelpDialog || m.confirmDialog.open ||
			m.taskCompleteDialog.open || m.confirmQuitDialog.open || m.confirmCancelDialog.open ||
			(m.dialogStack != nil && m.dialogStack.Len() > 0) {
			if m.taskRunning {
				return m, tea.Batch(listenForEvents(m.eventCh), tickCmd())
			}
			return m, nil
		}

		// Count tokens for AI response events
		// Prefer real token usage from API metadata, fallback to estimation
		if msg.event.Type == "ai_response" || msg.event.Type == "ai_stream_end" {
			if usageData, ok := msg.event.Metadata["usage"]; ok {
				if usageMap, ok := usageData.(map[string]interface{}); ok {
					var completionVal int64
					if completionTokens, ok := usageMap["completion_tokens"]; ok {
						switch v := completionTokens.(type) {
						case float64:
							completionVal = int64(v)
						case int64:
							completionVal = v
						case int:
							completionVal = int64(v)
						}
					}
					m.outputTokens += completionVal

					// Also track input tokens from API (PromptTokens)
					var promptVal int64
					if promptTokens, ok := usageMap["prompt_tokens"]; ok {
						switch v := promptTokens.(type) {
						case float64:
							promptVal = int64(v)
						case int64:
							promptVal = v
						case int:
							promptVal = int64(v)
						}
					}
					m.inputTokens += promptVal

					// Parse cache tokens
					var cacheCreationVal int64
					if cacheCreationTokens, ok := usageMap["cache_creation_input_tokens"]; ok {
						switch v := cacheCreationTokens.(type) {
						case float64:
							cacheCreationVal = int64(v)
						case int64:
							cacheCreationVal = v
						case int:
							cacheCreationVal = int64(v)
						}
					}
					m.cacheCreationInputTokens += cacheCreationVal

					var cacheReadVal int64
					if cacheReadTokens, ok := usageMap["cache_read_input_tokens"]; ok {
						switch v := cacheReadTokens.(type) {
						case float64:
							cacheReadVal = int64(v)
						case int64:
							cacheReadVal = v
						case int:
							cacheReadVal = int64(v)
						}
					}
					m.cacheReadInputTokens += cacheReadVal

					// Track current running agent
					if m.taskRunning {
						if msg.event.From != "" {
							m.currentAgent = msg.event.From
						}
					}

					// Per-agent token tracking
					agentName := msg.event.From
					if agentName == "" {
						agentName = "Unknown"
					}
					agentUsage, exists := m.tokenUsagePerAgent[agentName]
					if !exists {
						agentUsage = &AgentTokenUsage{AgentName: agentName}
						m.tokenUsagePerAgent[agentName] = agentUsage
					}
					agentUsage.InputTokens += promptVal
					agentUsage.OutputTokens += completionVal
					agentUsage.CacheCreationInputTokens += cacheCreationVal
					agentUsage.CacheReadInputTokens += cacheReadVal
				}
			} else {
				// Fallback: estimate tokens from content string
				if content, ok := msg.event.Content.(string); ok && content != "" {
					if tok := compact.GetGlobalTokenizer(); tok != nil {
						if count, err := tok.CountTokens(content); err == nil && count > 0 {
							m.outputTokens += int64(count)
						}
					}
				}
			}
		}

		// Capture model info for status bar display
		if msg.event.Type == "model_info" {
			if contentMap, ok := msg.event.Content.(map[string]interface{}); ok {
				if modelName, ok := contentMap["model"].(string); ok {
					m.currentModel = modelName
				}
				// Also capture agent name from model_info event
				if agentName, ok := contentMap["agent"].(string); ok && agentName != "" {
					m.currentAgent = agentName
				}
			}
			return m, tea.Batch(listenForEvents(m.eventCh), tickCmd())
		}

		// Intercept user_help_needed to show interactive dialog
		if msg.event.Type == "user_help_needed" {
			m.openConfirmDialog(msg.event)
			// Still log the event so it appears in the background
			entry := formatEventAsEntry(msg.event)
			m.logEntries = append(m.logEntries, entry)
			m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
			return m, tea.Batch(listenForEvents(m.eventCh), tickCmd())
		}

		// Handle commit context loaded event — log it in the TUI
		if msg.event.Type == "commit_context_loaded" {
			if contentMap, ok := msg.event.Content.(map[string]interface{}); ok {
				if count, ok := contentMap["count"].(float64); ok {
					countInt := int(count)
					entry := logEntry{
						timestamp: msg.event.Timestamp,
						eventType: "commit_context",
						from:      msg.event.From,
						content:   fmt.Sprintf("📦 Loaded %d relevant commit(s) for context", countInt),
					}
					m.logEntries = append(m.logEntries, entry)
					m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
				}
			}
			return m, tea.Batch(listenForEvents(m.eventCh), tickCmd())
		}

		// ── Tool call result: update the matching running entry ──
		if msg.event.Type == "tool_call_result" {
			callID := getToolCallIDFromEventContent(msg.event.Content)
			if callID != "" {
				if toolEntry, ok := m.toolCallEntries[callID]; ok {
					resultContent := getResultFromEventContent(msg.event.Content)
					isError := strings.HasPrefix(resultContent, "Error:")
					toolEntry.SetResult(ToolResultInfo{
						ToolCallID: callID,
						Name:       toolEntry.Call.Name,
						Content:    resultContent,
						IsError:    isError,
					})
					// Update the log entry content and diff for backward compat
					if idx := findLogEntryByToolCallID(m.logEntries, callID); idx >= 0 {
						le := &m.logEntries[idx]
						le.content = resultContent
						le.isToolRunning = false
						le.rendered = "" // invalidate cache
					}
					delete(m.toolCallEntries, callID)
					m.updateActiveAnim()
					m.rebuildViewportScrollLock()
					return m, tea.Batch(listenForEvents(m.eventCh), tickCmd())
				}
			}
			// No matching start entry by callID — try matching by tool name
			// as a fallback for the most recent running entry of the same type.
			toolName := getToolNameFromEventContent(msg.event.Content)
			if toolName != "" {
				if matchedID, matchedEntry := findRunningEntryByName(m.toolCallEntries, toolName); matchedEntry != nil {
					resultContent := getResultFromEventContent(msg.event.Content)
					isError := strings.HasPrefix(resultContent, "Error:")
					matchedEntry.SetResult(ToolResultInfo{
						ToolCallID: matchedID,
						Name:       matchedEntry.Call.Name,
						Content:    resultContent,
						IsError:    isError,
					})
					if idx := findLogEntryByToolCallID(m.logEntries, matchedID); idx >= 0 {
						le := &m.logEntries[idx]
						le.content = resultContent
						le.isToolRunning = false
						le.rendered = ""
					}
					delete(m.toolCallEntries, matchedID)
					m.updateActiveAnim()
					m.rebuildViewportScrollLock()
					return m, tea.Batch(listenForEvents(m.eventCh), tickCmd())
				}
			}
			// No matching start entry — add as standalone
		}

		entry := formatEventAsEntry(msg.event)

		// Track running tool calls for status transition
		if entry.eventType == "tool_call_start" && entry.toolCallID != "" {
			m.toolCallEntries[entry.toolCallID] = entry.toolEntry
			m.activeAnim = true
		}

		m.logEntries = append(m.logEntries, entry)
		m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
		return m, tea.Batch(listenForEvents(m.eventCh), tickCmd())

	case taskCompleteMsg:
		m.taskRunning = false
		m.currentModel = ""
		m.currentAgent = ""
		m.commandMode = false
		m.confirmDialog.open = false // safety: close any stale dialog

		// 如果是用户主动取消，不显示错误弹窗或完成弹窗
		if m.taskCancelled {
			m.taskCancelled = false
			m.currentTask = nil
			return m, nil
		}
		m.taskCancelled = false

		if msg.err != nil {
			m.errMsg = msg.err.Error()
			m.currentTask = nil
			m.logEntries = append(m.logEntries, logEntry{
				timestamp: time.Now(),
				eventType: "error",
				content:   msg.err.Error(),
			})
			m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
			// Show error dialog via DialogStack
			if m.dialogStack != nil {
				d := components.NewTaskCompleteDialog(false, "❌ Task Failed\n\n"+msg.err.Error(), components.Language(m.currentLang))
				d.SetBounds(m.termWidth, m.termHeight)
				m.dialogStack.Push(d)
			} else {
				m.taskCompleteDialog = taskCompleteDialog{
					open:    true,
					message: "❌ Task Failed\n\n" + msg.err.Error(),
				}
			}
		} else {
			// 保留 currentTask 以支持任务完成后继续对话
			// m.currentTask = nil  // 不再清除
			// Show success dialog via DialogStack
			if m.dialogStack != nil {
				d := components.NewTaskCompleteDialog(true, "All tasks have been finished.", components.Language(m.currentLang))
				d.SetBounds(m.termWidth, m.termHeight)
				m.dialogStack.Push(d)
			} else {
				m.taskCompleteDialog = taskCompleteDialog{
					open:    true,
					message: "All tasks have been finished.",
				}
			}
		}

		// 清空编辑器输入框
		m.input.SetValue("")
		return m, nil
	}

	// Handle fzf file selection
	if msg, ok := msg.(fzfFileSelectedMsg); ok {
		if msg.path != "" {
			currentVal := m.input.Value()
			// Find the last @ and insert the file path after it
			lastAt := strings.LastIndex(currentVal, "@")
			if lastAt >= 0 {
				newVal := currentVal[:lastAt+1] + msg.path + " " + currentVal[lastAt+1:]
				m.input.SetValue(newVal)
			}
		}
		return m, nil
	}

	// Handle text input
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// updateSkillAutocomplete checks the current input for skill references (/skillname)
// and updates the autocomplete suggestions accordingly.
// 已优化：委托给 doSkillAutocomplete 以减少代码重复
func (m *model) updateSkillAutocomplete() {
	text := m.input.Value()
	line := m.input.Line()
	column := m.input.Column()
	cursor := line*10000 + column
	// 提前转换 rune 切片，避免在 doSkillAutocomplete 中重复转换
	contentRunes := []rune(text)
	m.doSkillAutocomplete(text, contentRunes, cursor)
}

// updateKeywordAutocomplete checks if keyword autocomplete should be triggered
// based on current input and cursor position.
// 已优化：委托给 doKeywordAutocomplete 以减少代码重复
func (m *model) updateKeywordAutocomplete() {
	text := m.input.Value()
	line := m.input.Line()
	column := m.input.Column()
	cursor := line*10000 + column
	// 提前转换 rune 切片，避免在 doKeywordAutocomplete 中重复转换
	contentRunes := []rune(text)
	m.doKeywordAutocomplete(text, contentRunes, cursor)
}

// extractWordAtCursor extracts the word immediately before the cursor position.
// A word is defined as a sequence of alphanumeric characters, underscores,
// and common Chinese characters.
//
// Optimizations:
//   - Uses append + reverse instead of O(n²) prepend
func extractWordAtCursor(content string, cursorPos int) string {
	return extractWordAtCursorRunes([]rune(content), cursorPos)
}

// extractWordAtCursorRunes extracts the word immediately before the cursor position
// from a pre-computed runes slice. This avoids the overhead of converting the
// string to runes again when the runes are already available.
func extractWordAtCursorRunes(runes []rune, cursorPos int) string {
	if cursorPos <= 0 || cursorPos > len(runes) {
		return ""
	}

	// Extract backwards from cursor position using append (O(n) instead of O(n²))
	var word []rune
	for i := cursorPos - 1; i >= 0; i-- {
		r := runes[i]
		// Allow alphanumeric, underscore, and common Chinese characters
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || (r >= 0x4e00 && r <= 0x9fff) {
			word = append(word, r)
		} else {
			break
		}
	}

	// Reverse the word (collected backwards)
	for i, j := 0, len(word)-1; i < j; i, j = i+1, j-1 {
		word[i], word[j] = word[j], word[i]
	}

	return string(word)
}

// splitLogicalLines splits the content runes into logical lines (separated by newlines).
// It returns the first `count` logical lines from the content.
//
// Optimized for early termination: uses efficient slices.Index to skip directly to
// newline positions, and returns immediately once count lines are found.
func splitLogicalLines(content []rune, count int) [][]rune {
	if count <= 0 {
		return nil
	}

	result := make([][]rune, 0, count)
	// Use slices.Index for efficient newline scanning
	rest := content
	for len(rest) > 0 {
		// Find next newline position (fast slice search)
		idx := slices.Index(rest, '\n')
		if idx < 0 {
			// No more newlines: remaining content is the last line
			if len(rest) > 0 {
				result = append(result, rest)
			}
			break
		}
		// Extract line before newline (exclusive)
		result = append(result, rest[:idx])
		rest = rest[idx+1:] // Skip the newline character
		if len(result) >= count {
			return result
		}
	}
	return result
}

// handleMouseClick 处理鼠标点击事件
func (m *model) handleMouseClick(x, y int) {
	// 检查是否点击了弹窗中的按钮
	// 简化实现：记录点击位置，等待后续扩展
	_ = x
	_ = y
}

// hasPrefixIgnoreCase reports whether string s starts with prefix, ignoring case.
// Uses byte-level comparison for ASCII characters, avoiding the overhead of
// strings.ToLower() which creates a new string allocation on every call.
// This is safe for ASCII-only prefixes (skill names are typically ASCII).
func hasPrefixIgnoreCase(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		a := s[i]
		b := prefix[i]
		// Convert to lowercase if uppercase ASCII
		if a >= 'A' && a <= 'Z' {
			a += 32
		}
		if b >= 'A' && b <= 'Z' {
			b += 32
		}
		if a != b {
			return false
		}
	}
	return true
}
