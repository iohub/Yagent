package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"codeactor/internal/tui/components"

	tea "charm.land/bubbletea/v2"
)

// isModelTargetTool reports whether the given model-switch target refers to a
// per-tool LLM override (rather than an agent or the global default).
// Currently only the deepthinking tool supports runtime provider switching;
// add more tools here as they gain support.
func isModelTargetTool(target string) bool {
	return target == "deepthinking"
}

// ─────────────────────────────────────────────────────────────────────────────
// handleKeyMsg — 分发入口
// ─────────────────────────────────────────────────────────────────────────────

func (m *model) handleKeyMsg(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.dialogStack != nil && m.dialogStack.Len() > 0 {
		if model, cmd, handled := m.handleDialogStackKey(msg); handled {
			return model, cmd
		}
	}
	if m.commandMode {
		return m.handleCommandModeKey(msg)
	}
	return m.handleEditModeKey(msg)
}

// ─────────────────────────────────────────────────────────────────────────────
// handleDialogStackKey — 原 DialogStack 键处理 if 块
// ─────────────────────────────────────────────────────────────────────────────

func (m *model) handleDialogStackKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	top := m.dialogStack.Top()
	if top == nil {
		return m, nil, false
	}

	key := msg.String()

	// Ctrl+C: 强制退出
	if key == "ctrl+c" {
		m.saveAndFlushTaskMemory()
		m.quitting = true
		return m, tea.Quit, true
	}

	// 根据弹窗类型处理确认操作
	switch d := top.(type) {
	case *components.ConfirmDialog:
		// Let the dialog handle navigation keys via its Update
		updated, cmd := d.Update(msg)
		_ = updated // dialog state mutated in-place via pointer

		if key == "enter" {
			action := d.GetResponseAction()
			m.respondToAuth(action)
			return m, nil, true
		}
		if cmd != nil {
			return m, cmd, true
		}
		return m, nil, true

	case *components.UserHelpDialog:
		// Let the dialog handle the key internally via its Update
		updated, cmd := d.Update(msg)
		_ = updated // dialog state mutated in-place via pointer

		// Check if dialog is closed (user submitted or cancelled)
		if d.IsClosed() {
			m.dialogStack.Pop()
			m.respondToUserHelp(d.Result())
			// Re-establish event chain to receive agent's subsequent events
			if m.taskRunning {
				return m, listenForEvents(m.eventCh), true
			}
			return m, nil, true
		}

		if cmd != nil {
			return m, cmd, true
		}
		return m, nil, true

	case *components.QuitConfirmDialog:
		// Let the dialog handle keys internally to update Confirmed/SelectedIndex
		_, cmd := d.Update(msg)
		if cmd != nil {
			return m, cmd, true
		}

		switch key {
		case "enter", "y", "Y":
			if d.GetConfirmed() {
				if d.ID() == "quit_confirm" || d.ID() == "quit_confirm_dialog" {
					m.saveAndFlushTaskMemory()
					m.quitting = true
					m.cleanupDebounceTimer()
					return m, tea.Quit, true
				}
				// Delete history confirmation
				if d.ID() == "delete_history_confirm" {
					taskID := m.pendingDeleteTaskID
					m.pendingDeleteTaskID = ""
					m.dialogStack.Pop()
					return m, confirmDeleteHistoryEntryByID(m, taskID), true
				}
				// Cancel confirmation
				if m.currentTask != nil && m.currentTask.CancelFunc != nil {
					m.taskCancelled = true
					m.taskRunning = false // 立即更新 UI 状态，不等 taskCompleteMsg
					m.currentAgent = ""   // 清除当前 agent 显示
					m.currentTask.CancelFunc()
					// 主动清理 LLM HTTP 连接池，加速 context 取消传播
					if m.assistant != nil {
						m.assistant.CloseIdleConnections()
					}
					m.logEntries = append(m.logEntries, logEntry{
						timestamp: time.Now(),
						eventType: "status",
						content:   "Task cancelled by user",
					})
					m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
					m.invalidateFooterCache()                      // 立即刷新状态栏
					m.cachedStatusBar = m.renderAirlineStatusBar() // 重新渲染状态栏
				}
			}
			m.dialogStack.Pop()
			return m, nil, true
		case "esc", "n", "N":
			d.SetConfirmed(false)
			d.SelectedIndex = 0
			m.dialogStack.Pop()
			return m, nil, true
		}
		return m, nil, true

	case *components.AgentSelectDialog:
		// Let the dialog handle keys internally for cursor movement
		_, cmd := d.Update(msg)
		if cmd != nil {
			return m, cmd, true
		}
		switch key {
		case "enter", " ":
			if d.Selected != "" {
				target := d.Selected
				// Set pendingModelTarget: "global" → "" (empty means global), agent name → agent
				if target == "global" {
					m.pendingModelTarget = ""
				} else {
					m.pendingModelTarget = target
				}
				// Pop the agent select dialog
				m.dialogStack.Pop()
				// Build provider list and push ModelSelectDialog
				providers := m.assistant.GetClient().Config.GetProviderNames()
				providerDescs := make(map[string]string)
				// Get current provider for the selected target
				var currentProv string
				switch {
				case target == "global":
					currentProv = m.currentProvider
				case isModelTargetTool(target):
					currentProv, _ = m.assistant.GetToolProviderInfo(target)
				default:
					currentProv, _ = m.assistant.GetAgentProvider(target)
				}
				for _, p := range providers {
					if provCfg, err := m.assistant.GetClient().Config.GetProvider(p); err == nil {
						providerDescs[p] = components.FormatProviderDesc(p, provCfg.Model)
					} else {
						providerDescs[p] = p
					}
				}
				providerDialog := components.NewModelSelectDialog(m.com.Styles, providers, providerDescs, currentProv)
				providerDialog.SetBounds(m.termWidth, m.termHeight)
				m.dialogStack.Push(providerDialog)
			} else {
				m.dialogStack.Pop()
			}
			return m, nil, true
		case "esc", "q", "Q":
			d.Selected = ""
			m.dialogStack.Pop()
			return m, nil, true
		}
		return m, nil, true

	case *components.ModelSelectDialog:
		// Let the dialog handle keys internally for cursor movement
		_, cmd := d.Update(msg)
		if cmd != nil {
			return m, cmd, true
		}
		switch key {
		case "enter", " ":
			if d.Selected != "" {
				providers := m.assistant.GetClient().Config.GetProviderNames()
				found := false
				for _, p := range providers {
					if p == d.Selected {
						found = true
						break
					}
				}
				if !found {
					m.logEntries = append(m.logEntries, logEntry{
						timestamp: time.Now(),
						eventType: "status",
						content:   fmt.Sprintf("Unknown provider: %s", d.Selected),
					})
					m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
					m.dialogStack.Pop()
					return m, nil, true
				}
				// 根据 pendingModelTarget 分流: tool 覆盖 / agent 覆盖 / 全局
				target := m.pendingModelTarget
				m.pendingModelTarget = ""
				switch {
				case isModelTargetTool(target):
					// 为指定 tool 设置 provider
					if err := m.assistant.SetToolProvider(target, d.Selected); err != nil {
						m.logEntries = append(m.logEntries, logEntry{
							timestamp: time.Now(),
							eventType: "error",
							content:   fmt.Sprintf("Failed to set tool provider: %v", err),
						})
						m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
					} else {
						_, modelName := m.assistant.GetToolProviderInfo(target)
						m.logEntries = append(m.logEntries, logEntry{
							timestamp: time.Now(),
							eventType: "status",
							content:   fmt.Sprintf("Set tool '%s' provider to: %s (model: %s)", target, d.Selected, modelName),
						})
						m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
					}
				case target != "":
					// 为指定 agent 设置 provider
					if err := m.assistant.SetAgentProvider(target, d.Selected); err != nil {
						m.logEntries = append(m.logEntries, logEntry{
							timestamp: time.Now(),
							eventType: "error",
							content:   fmt.Sprintf("Failed to set agent provider: %v", err),
						})
						m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
					} else {
						_, modelName := m.assistant.GetAgentProvider(target)
						m.logEntries = append(m.logEntries, logEntry{
							timestamp: time.Now(),
							eventType: "status",
							content:   fmt.Sprintf("Set agent '%s' provider to: %s (model: %s)", target, d.Selected, modelName),
						})
						m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
					}
				default:
					// 全局设置
					if err := m.assistant.SwitchProvider(d.Selected); err != nil {
						m.logEntries = append(m.logEntries, logEntry{
							timestamp: time.Now(),
							eventType: "error",
							content:   fmt.Sprintf("Failed to switch provider: %v", err),
						})
						m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
					} else {
						_, modelName := m.assistant.GetClient().GetCurrentProviderInfo()
						m.currentProvider = d.Selected
						m.currentModel = modelName
						m.logEntries = append(m.logEntries, logEntry{
							timestamp: time.Now(),
							eventType: "status",
							content:   fmt.Sprintf("Switched global provider to: %s (model: %s)", d.Selected, modelName),
						})
						m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
					}
				}
			}
			m.dialogStack.Pop()
			return m, nil, true
		case "esc", "q", "Q":
			d.Selected = ""
			m.dialogStack.Pop()
			return m, nil, true
		}
		return m, nil, true

	case *components.HelpDialog:
		switch key {
		case "esc", "i", "I", "ctrl+h":
			m.dialogStack.Pop()
			return m, nil, true
		}
		return m, nil, true

	case *components.TaskCompleteDialog:
		switch key {
		case "enter", " ", "esc":
			m.dialogStack.Pop()
			return m, nil, true
		}
		return m, nil, true
	}

	return m, nil, false
}

// ─────────────────────────────────────────────────────────────────────────────
// handleTabShortcut — 处理会话 tab 快捷键（编辑/命令模式共用）
// ─────────────────────────────────────────────────────────────────────────────

func (m *model) handleTabShortcut(key string) (bool, tea.Cmd) {
	switch key {
	case "ctrl+t": // 新建会话 tab
		if m.taskRunning {
			m.infoMsg = langManager.GetText("TabCannotWhileRunning")
			return true, nil
		}
		if len(m.sessionTabs) >= m.maxTabs {
			m.infoMsg = langManager.GetText("TabMaxReached")
			return true, nil
		}
		return true, m.newSessionTabAction()
	case "alt+[": // 上一个 tab
		if m.taskRunning {
			m.infoMsg = langManager.GetText("TabCannotWhileRunning")
			return true, nil
		}
		if m.activeSessionIdx > 0 {
			m.switchSessionTab(m.activeSessionIdx - 1)
		}
		return true, nil
	case "alt+]": // 下一个 tab
		if m.taskRunning {
			m.infoMsg = langManager.GetText("TabCannotWhileRunning")
			return true, nil
		}
		if m.activeSessionIdx < len(m.sessionTabs)-1 {
			m.switchSessionTab(m.activeSessionIdx + 1)
		}
		return true, nil
	case "alt+c": // 清空当前会话
		if m.taskRunning {
			m.infoMsg = langManager.GetText("TabCannotWhileRunning")
			return true, nil
		}
		m.clearCurrentSession()
		return true, nil
	case "alt+w": // 关闭当前 tab
		if m.taskRunning {
			m.infoMsg = langManager.GetText("TabCannotWhileRunning")
			return true, nil
		}
		if len(m.sessionTabs) <= 1 {
			m.infoMsg = langManager.GetText("TabCloseLastBlocked")
			return true, nil
		}
		m.closeCurrentSessionTab()
		return true, nil
	}
	// alt+1..9 直达 tab
	if strings.HasPrefix(key, "alt+") {
		numStr := strings.TrimPrefix(key, "alt+")
		if n, err := strconv.Atoi(numStr); err == nil && n >= 1 && n <= 9 {
			if m.taskRunning {
				m.infoMsg = langManager.GetText("TabCannotWhileRunning")
			} else {
				idx := n - 1
				if idx < len(m.sessionTabs) {
					m.switchSessionTab(idx)
				}
			}
			return true, nil
		}
	}
	return false, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// handleCommandModeKey — 原 command mode if 块
// ─────────────────────────────────────────────────────────────────────────────

func (m *model) handleCommandModeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Resolve multi-key sequences: check if lastKey + current key forms a valid combo
	key := msg.String()
	// ── 可配置快捷键重映射 ──
	if m.cmdKeyMap != nil {
		if mapped, ok := m.cmdKeyMap[key]; ok {
			key = mapped
		}
	}
	if m.lastKey != "" {
		combo := m.lastKey + key
		m.lastKey = ""
		switch combo {
		case "gg":
			m.viewport.GotoTop()
			m.updateViewportCache()
			return m, nil
		default:
			// Invalid combo: discard lastKey and fall through to process key normally
		}
	}

	// Help dialog is open: let action keys (i/esc/enter/?/ctrl+c) pass through,
	// dismiss on any other key without processing it.
	helpOpen := m.dialogStack != nil && func() bool {
		top := m.dialogStack.Top()
		return top != nil && top.ID() == "help_dialog"
	}()
	if helpOpen && key != "i" && key != "ctrl+e" && key != "enter" && key != "?" && key != "ctrl+c" {
		m.dialogStack.CloseDialog("help_dialog")
		return m, nil
	}

	// 会话 tab 快捷键（命令模式也支持）
	if handled, cmd := m.handleTabShortcut(key); handled {
		return m, cmd
	}

	switch key {
	case "ctrl+c":
		d := components.NewQuitConfirmDialogForQuit(components.Language(m.currentLang))
		d.SetBounds(m.termWidth, m.termHeight)
		m.dialogStack.Push(d)
		return m, nil

	case "esc":
		if m.dialogStack != nil {
			top := m.dialogStack.Top()
			if top != nil && top.ID() == "help_dialog" {
				m.dialogStack.CloseDialog("help_dialog")
				return m, nil
			}
		}
		if m.commandBuffer != "" {
			// Clear command buffer, stay in command mode
			m.commandBuffer = ""
			return m, nil
		}
		if m.taskRunning && m.currentTask != nil && m.currentTask.CancelFunc != nil {
			d := components.NewQuitConfirmDialogForCancel(components.Language(m.currentLang))
			d.SetBounds(m.termWidth, m.termHeight)
			m.dialogStack.Push(d)
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
		m.invalidateFooterCache()
		return m, nil

	case "enter":
		if m.dialogStack != nil {
			top := m.dialogStack.Top()
			if top != nil && top.ID() == "help_dialog" {
				m.dialogStack.CloseDialog("help_dialog")
				return m, nil
			}
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
		m.updateViewportCache()
		return m, nil

	case "b":
		m.viewport.PageUp()
		m.updateViewportCache()
		return m, nil

	case "j", "down":
		m.viewport.ScrollDown(1)
		m.updateViewportCache()
		return m, nil

	case "k", "up":
		m.viewport.ScrollUp(1)
		m.updateViewportCache()
		return m, nil

	case "G":
		// Vim: Shift+G → go to bottom
		m.viewport.GotoBottom()
		m.updateViewportCache()
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
			top := m.dialogStack.Top()
			if top != nil && top.ID() == "help_dialog" {
				m.dialogStack.CloseDialog("help_dialog")
			} else {
				d := components.NewHelpDialog(components.Language(m.currentLang))
				if m.com != nil && m.com.Config != nil {
					overrides := buildHelpKeyOverrides(&m.com.Config.TUI.Keybindings)
					if len(overrides) > 0 {
						d.SetAltKeybindings(overrides)
					}
				}
				d.SetBounds(m.termWidth, m.termHeight)
				m.dialogStack.Push(d)
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

	// ── Token dashboard collapse toggle ──
	case "alt+t":
		m.tokenDashboardCollapsed = !m.tokenDashboardCollapsed
		m.cachedTokenDashboard = m.renderTokenDashboard()
		m.tokenDashboardValid = true
		m.invalidateFooterCache()
		return m, nil

	// ── Dashboard collapse toggle ──
	case "alt+d":
		m.toggleDashboard()
		return m, nil

	// ── Model selection ──
	case "alt+m":
		return m, m.showModelSelectionDialog()

	// ── Misc ──
	case "ctrl+l":
		// 切换全屏时间线模式
		if !m.timelineFullscreenMode {
			// 进入全屏模式
			m.timelineFullscreenMode = true
			m.timelineExpanded = false
			m.timelineFullscreenFocus = "list"
			if len(m.timelineEntries) > 0 {
				m.timelineFullscreenCursor = len(m.timelineEntries) - 1
			} else {
				m.timelineFullscreenCursor = 0
			}
			initTimelineDetailViewport(m)
			m.timelineCacheKey = ""
			m.invalidateFooterCache()
		} else {
			// 退出全屏模式
			m.ExitTimelineFullscreen()
			m.timelineExpanded = false
			m.timelineCacheKey = ""
			m.invalidateFooterCache()
		}
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

// ─────────────────────────────────────────────────────────────────────────────
// handleEditModeKey — 原 edit mode 部分
// ─────────────────────────────────────────────────────────────────────────────

func (m *model) handleEditModeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// ── 可配置快捷键重映射 ──
	editKeyStr := msg.String()
	if m.editKeyMap != nil {
		if mapped, ok := m.editKeyMap[editKeyStr]; ok {
			editKeyStr = mapped
		}
	}
	// 会话 tab 快捷键（与命令模式共用）
	if handled, cmd := m.handleTabShortcut(editKeyStr); handled {
		return m, cmd
	}
	// ── 编辑模式功能键处理 ──
	switch editKeyStr {
	case "ctrl+c":
		d := components.NewQuitConfirmDialogForQuit(components.Language(m.currentLang))
		d.SetBounds(m.termWidth, m.termHeight)
		m.dialogStack.Push(d)
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
			d := components.NewQuitConfirmDialogForCancel(components.Language(m.currentLang))
			d.SetBounds(m.termWidth, m.termHeight)
			m.dialogStack.Push(d)
		}
		return m, nil

	case "ctrl+e":
		// Enter command mode
		m.commandMode = true
		m.commandBuffer = ""
		m.invalidateFooterCache()
		return m, nil

	case "ctrl+h":
		// Toggle HelpDialog
		if m.dialogStack != nil {
			top := m.dialogStack.Top()
			if top != nil && top.ID() == "help_dialog" {
				m.dialogStack.Pop()
				return m, nil
			}
		}
		d := components.NewHelpDialog(components.Language(m.currentLang))
		if m.com != nil && m.com.Config != nil {
			overrides := buildHelpKeyOverrides(&m.com.Config.TUI.Keybindings)
			if len(overrides) > 0 {
				d.SetAltKeybindings(overrides)
			}
		}
		d.SetBounds(m.termWidth, m.termHeight)
		if m.dialogStack == nil {
			m.dialogStack = components.NewDialogStack()
		}
		m.dialogStack.Push(d)
		return m, nil

	case "alt+s":
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

	case "alt+m":
		if m.taskRunning {
			return m, nil
		}
		return m, m.showModelSelectionDialog()

	case "alt+d":
		m.toggleDashboard()
		return m, nil

	case "ctrl+l":
		// 切换全屏时间线模式
		if !m.timelineFullscreenMode {
			// 进入全屏模式
			m.timelineFullscreenMode = true
			m.timelineExpanded = false
			m.timelineFullscreenFocus = "list"
			if len(m.timelineEntries) > 0 {
				m.timelineFullscreenCursor = len(m.timelineEntries) - 1
			} else {
				m.timelineFullscreenCursor = 0
			}
			initTimelineDetailViewport(m)
			m.timelineCacheKey = ""
			m.invalidateFooterCache()
		} else {
			// 退出全屏模式
			m.ExitTimelineFullscreen()
			m.timelineExpanded = false
			m.timelineCacheKey = ""
			m.invalidateFooterCache()
		}
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
			m.invalidateFooterCache()

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
				m.invalidateFooterCache()
				return m, enterHistoryMode(m)
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
		m.invalidateFooterCache()
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
		m.invalidateFooterCache()
		return m, cmd

	default:
		// Only update input — viewport scrolling keys (ctrl+f, ctrl+b)
		// are handled in dedicated case branches above.
		var inputCmd tea.Cmd
		m.input, inputCmd = m.input.Update(msg)
		m.invalidateFooterCache()
		// 启动或重置补全防抖
		return m, tea.Batch(inputCmd, m.scheduleAutocomplete())
	}
}
