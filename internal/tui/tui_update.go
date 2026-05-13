package tui

import (
	"fmt"
	"strings"
	"time"

	"codeactor/internal/compact"
	"codeactor/internal/tui/components"

	tea "charm.land/bubbletea/v2"
)

func (m *model) processCommand(cmd string) tea.Cmd {
	cmd = strings.TrimSpace(cmd)

	switch {
	case cmd == ":q!":
		// Force quit — skip confirmation (vim convention)
		m.quitting = true
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

		if m.activeAnim {
			m.anim.Tick()
			// Throttle viewport rebuild to every 3 ticks (~300ms) to avoid
			// flooding viewport.SetContent() — the #1 cause of scroll lag.
			if m.animFrame%3 == 0 {
				for _, te := range m.toolCallEntries {
					if te.Status == ToolStatusRunning {
						te.InvalidateCache()
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
								return m, tea.Quit
							}
							// Cancel confirmation
							if m.currentTask != nil && m.currentTask.CancelFunc != nil {
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
				return m, tea.Quit
			}
			return m, nil
		}

		// Quit confirmation dialog key handling
		if m.confirmQuitDialog.open {
			switch msg.String() {
			case "ctrl+c":
				m.quitting = true
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
					return m, tea.Quit
				}
				m.confirmQuitDialog.open = false
				m.confirmQuitDialog.selectedOption = 0
				return m, nil
			case "y", "Y":
				m.quitting = true
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
			if m.currentTask != nil && m.taskRunning {
				return m, m.submitFollowUp(taskDesc)
			}
			if m.currentTask != nil && !m.taskRunning {
				m.errMsg = "Task has already finished, cannot submit follow-up"
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
			m.updateSkillAutocomplete()
			return m, inputCmd

		case "/":
			// Pass '/' to textarea normally, then check for skill autocomplete
			var inputCmd tea.Cmd
			m.input, inputCmd = m.input.Update(msg)
			m.updateSkillAutocomplete()
			return m, inputCmd

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
			m.updateSkillAutocomplete()
			return m, inputCmd
		}

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
		m.commandMode = false
		m.confirmDialog.open = false // safety: close any stale dialog
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
			m.currentTask = nil
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
func (m *model) updateSkillAutocomplete() {
	// Only active in edit mode and when not task running
	if m.commandMode || m.taskRunning {
		m.skillAutoComplete = false
		m.skillSuggestions = nil
		m.skillSuggestionIdx = 0
		return
	}

	inputValue := m.input.Value()

	// Find the last '/' in the input to extract the skill query
	lastSlash := strings.LastIndex(inputValue, "/")
	if lastSlash < 0 {
		m.skillAutoComplete = false
		m.skillSuggestions = nil
		m.skillSuggestionIdx = 0
		return
	}

	// Extract the text after the last '/' as the query
	query := inputValue[lastSlash+1:]

	// Build the list of matching skills
	var matches []string
	queryLower := strings.ToLower(query)

	// Add skills from registry (if available)
	if m.assistant.SkillRegistry != nil {
		allSkills := m.assistant.SkillRegistry.List()
		for _, name := range allSkills {
			if strings.HasPrefix(strings.ToLower(name), queryLower) {
				matches = append(matches, name)
			}
		}
	}

	// Add "history" as a built-in command (opens history dialog, not a skill)
	if strings.HasPrefix("history", queryLower) {
		matches = append([]string{"history"}, matches...)
	}

	if len(matches) > 0 {
		m.skillAutoComplete = true
		m.skillSuggestions = matches
		// Reset index if out of bounds
		if m.skillSuggestionIdx >= len(matches) {
			m.skillSuggestionIdx = 0
		}
	} else {
		m.skillAutoComplete = false
		m.skillSuggestions = nil
		m.skillSuggestionIdx = 0
	}
}

// handleMouseClick 处理鼠标点击事件
func (m *model) handleMouseClick(x, y int) {
	// 检查是否点击了弹窗中的按钮
	// 简化实现：记录点击位置，等待后续扩展
	_ = x
	_ = y
}
