package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"yagent/internal/tui/components"

	tea "charm.land/bubbletea/v2"
)

func isModelTargetTool(target string) bool {
	return target == "deepthinking" || target == "design"
}

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

func (m *model) handleDialogStackKey(msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	top := m.dialogStack.Top()
	if top == nil {
		return m, nil, false
	}

	key := msg.String()

	if key == "ctrl+c" {
		m.saveAndFlushTaskMemory()
		m.quitting = true
		return m, tea.Quit, true
	}

	switch d := top.(type) {
	case *components.ConfirmDialog:
		updated, cmd := d.Update(msg)
		_ = updated

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
		updated, cmd := d.Update(msg)
		_ = updated

		if d.IsClosed() {
			m.dialogStack.Pop()
			m.respondToUserHelp(d.Result())
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
				if d.ID() == "delete_history_confirm" {
					taskID := m.pendingDeleteTaskID
					m.pendingDeleteTaskID = ""
					m.dialogStack.Pop()
					return m, confirmDeleteHistoryEntryByID(m, taskID), true
				}
				if m.currentTask != nil && m.currentTask.CancelFunc != nil {
					m.taskCancelled = true
					m.taskRunning = false
					m.currentAgent = ""
					m.currentTask.CancelFunc()
					if m.assistant != nil {
						m.assistant.CloseIdleConnections()
					}
					m.logEntries = append(m.logEntries, logEntry{
						timestamp: time.Now(),
						eventType: "status",
						content:   "Task cancelled by user",
					})
					m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
					m.invalidateFooterCache()
					m.cachedStatusBar = m.renderAirlineStatusBar()
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

	case *components.ResumeDialog:
		_, cmd := d.Update(msg)
		if cmd != nil {
			return m, cmd, true
		}

		switch key {
		case "enter", "y", "Y":
			confirmed := d.GetConfirmed()
			m.dialogStack.Pop()
			if confirmed && m.currentTask != nil && !m.taskRunning {
				return m, m.submitFollowUp(buildResumeContinuationPrompt(d)), true
			}
			return m, nil, true
		case "esc", "n", "N":
			d.SetConfirmed(false)
			m.dialogStack.Pop()
			return m, nil, true
		}
		return m, nil, true

	case *components.AgentSelectDialog:
		_, cmd := d.Update(msg)
		if cmd != nil {
			return m, cmd, true
		}
		if d.Selected != "" {
			target := d.Selected
			// 设置项：LLM 调用超时。弹出目标选择器后打开超时对话框，而非 provider 选择器。
			if target == timeoutTarget {
				m.dialogStack.Pop()
				return m, m.showTimeoutDialog(), true
			}
			if target == "global" {
				m.pendingModelTarget = ""
			} else {
				m.pendingModelTarget = target
			}
			m.dialogStack.Pop()
			var currentProv string
			switch {
			case target == "global":
				currentProv = m.currentProvider
			case isModelTargetTool(target):
				currentProv, _ = m.assistant.GetToolProviderInfo(target)
			default:
				currentProv, _ = m.assistant.GetAgentProvider(target)
			}
			clientCfg := m.assistant.GetClient().Config
			providers := clientCfg.GetProviderNames()
			options := make([]components.ModelOption, 0, len(providers))
			for _, p := range providers {
				opt := components.ModelOption{Provider: p, Context: contextWindowFromCfg(clientCfg, p)}
				if provCfg, err := clientCfg.GetProvider(p); err == nil {
					opt.Model = provCfg.Model
				}
				options = append(options, opt)
			}
			providerDialog := components.NewModelSelectDialog(options, currentProv)
			providerDialog.SetBounds(m.termWidth, m.termHeight)
			m.dialogStack.Push(providerDialog)
			return m, nil, true
		}
		switch key {
		case "enter", " ", "esc", "q", "Q":
			d.Selected = ""
			m.dialogStack.Pop()
			return m, nil, true
		}
		return m, nil, true

	case *components.ModelSelectDialog:
		_, cmd := d.Update(msg)
		if cmd != nil {
			return m, cmd, true
		}
		if d.Selected != "" {
			persist := d.Persist
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
			target := m.pendingModelTarget
			m.pendingModelTarget = ""
			switch {
			case isModelTargetTool(target):
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
					m.refreshContextWindow()
					m.invalidateFooterCache()
					m.logEntries = append(m.logEntries, logEntry{
						timestamp: time.Now(),
						eventType: "status",
						content:   fmt.Sprintf("Switched global provider to: %s (model: %s)", d.Selected, modelName),
					})
					m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
				}
			}
			if persist {
				kind, name := "global", ""
				switch {
				case isModelTargetTool(target):
					kind, name = "tool", target
				case target != "":
					kind, name = "agent", target
				}
				if err := m.assistant.GetClient().PersistProviderChoice(kind, name, d.Selected); err != nil {
					m.logEntries = append(m.logEntries, logEntry{
						timestamp: time.Now(),
						eventType: "error",
						content:   fmt.Sprintf("Failed to save provider to config file: %v", err),
					})
					m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
				} else {
					m.logEntries = append(m.logEntries, logEntry{
						timestamp: time.Now(),
						eventType: "status",
						content:   fmt.Sprintf("Saved provider '%s' to config file", d.Selected),
					})
					m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
				}
			}
			m.dialogStack.Pop()
			return m, nil, true
		}
		switch key {
		case "enter", " ", "esc", "q", "Q":
			d.Selected = ""
			m.dialogStack.Pop()
			return m, nil, true
		}
		return m, nil, true

	case *components.RoutingDialog:
		_, cmd := d.Update(msg)
		if cmd != nil {
			return m, cmd, true
		}
		if d.Selected != "" {
			mode := d.Selected
			persist := d.Persist
			logStatus := func(kind, content string) {
				m.logEntries = append(m.logEntries, logEntry{timestamp: time.Now(), eventType: kind, content: content})
				m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
			}
			if err := m.assistant.SetRoutingMode(mode); err != nil {
				logStatus("error", fmt.Sprintf("Failed to switch routing mode: %v", err))
			} else {
				snap := m.assistant.GetRouteSnapshot()
				m.autoRouting = snap.Enabled
				line := "Routing mode set to " + strings.ToUpper(mode)
				switch {
				case snap.Enabled:
					// Tiers 索引：0=fast, 1=balanced, 2=high
					line += fmt.Sprintf(" — high: %s (%.1f) · balanced: %s (%.1f) · fast: %s (%.1f)",
						snap.Tiers[2].Model, snap.Tiers[2].Score,
						snap.Tiers[1].Model, snap.Tiers[1].Score,
						snap.Tiers[0].Model, snap.Tiers[0].Score)
				case snap.Reason != "":
					line += " — auto unavailable: " + snap.Reason
				}
				logStatus("status", line)
				if persist {
					if err := m.assistant.PersistRoutingMode(mode); err != nil {
						logStatus("error", fmt.Sprintf("Failed to save routing mode to config: %v", err))
					} else {
						logStatus("status", fmt.Sprintf("Saved routing mode '%s' to config file", mode))
					}
				}
				m.invalidateFooterCache()
			}
			m.dialogStack.Pop()
			return m, nil, true
		}
		switch key {
		case "esc", "q", "Q":
			d.Selected = ""
			m.dialogStack.Pop()
			return m, nil, true
		}
		return m, nil, true

	case *components.LLMTimeoutDialog:
		_, cmd := d.Update(msg)
		if cmd != nil {
			return m, cmd, true
		}
		// 非空 Selected = 用户确认了预设：应用（Space 才持久化）后关闭。
		if d.Selected != "" {
			dur, err := time.ParseDuration(d.Selected)
			if err == nil {
				m.applyLLMTimeout(dur, d.Persist)
			}
			m.dialogStack.Pop()
			return m, nil, true
		}
		switch key {
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

func (m *model) handleTabShortcut(key string) (bool, tea.Cmd) {
	switch key {
	case "ctrl+t":
		if m.taskRunning {
			m.infoMsg = langManager.GetText("TabCannotWhileRunning")
			return true, nil
		}
		if len(m.sessionTabs) >= m.maxTabs {
			m.infoMsg = langManager.GetText("TabMaxReached")
			return true, nil
		}
		return true, m.newSessionTabAction()
	case "alt+[":
		if m.taskRunning {
			m.infoMsg = langManager.GetText("TabCannotWhileRunning")
			return true, nil
		}
		if m.activeSessionIdx > 0 {
			m.switchSessionTab(m.activeSessionIdx - 1)
		}
		return true, nil
	case "alt+]":
		if m.taskRunning {
			m.infoMsg = langManager.GetText("TabCannotWhileRunning")
			return true, nil
		}
		if m.activeSessionIdx < len(m.sessionTabs)-1 {
			m.switchSessionTab(m.activeSessionIdx + 1)
		}
		return true, nil
	case "alt+c":
		if m.taskRunning {
			m.infoMsg = langManager.GetText("TabCannotWhileRunning")
			return true, nil
		}
		m.clearCurrentSession()
		return true, nil
	case "alt+w":
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

func (m *model) handleCommandModeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
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
		}
	}

	helpOpen := m.dialogStack != nil && func() bool {
		top := m.dialogStack.Top()
		return top != nil && top.ID() == "help_dialog"
	}()
	if helpOpen && key != "i" && key != "ctrl+e" && key != "enter" && key != "?" && key != "ctrl+c" {
		m.dialogStack.CloseDialog("help_dialog")
		return m, nil
	}

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
		if m.taskRunning {
			m.infoMsg = "Task is running, cannot switch to edit mode"
			return m, nil
		}
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
		if m.commandBuffer != "" {
			cmd := m.processCommand(m.commandBuffer)
			m.commandBuffer = ""
			if cmd != nil {
				return m, cmd
			}
		} else if !m.taskRunning {
			m.commandMode = false
		}
		return m, nil

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
		m.viewport.GotoBottom()
		m.updateViewportCache()
		return m, nil

	case "g":
		if m.commandBuffer == "" {
			m.lastKey = key
		} else {
			m.commandBuffer += key
		}
		return m, nil

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

	case "backspace":
		if len(m.commandBuffer) > 0 {
			m.commandBuffer = m.commandBuffer[:len(m.commandBuffer)-1]
		}
		return m, nil

	case "alt+t":
		m.tokenDashboardCollapsed = !m.tokenDashboardCollapsed
		m.cachedTokenDashboard = m.renderTokenDashboard()
		m.tokenDashboardValid = true
		m.invalidateFooterCache()
		return m, nil

	case "alt+d":
		m.toggleDashboard()
		return m, nil

	case "alt+m":
		return m, m.showModelSelectionDialog()

	case "alt+r":
		return m, m.showRoutingDialog()

	case "ctrl+l":
		if !m.timelineFullscreenMode {
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
			m.ExitTimelineFullscreen()
			m.timelineExpanded = false
			m.timelineCacheKey = ""
			m.invalidateFooterCache()
		}
		return m, nil

	case "ctrl+g":
		if !m.thinklinkMode {
			return m, enterThinklinkMode(m)
		}
		exitThinklinkMode(m)
		return m, nil

	default:
		if len(msg.Key().Text) > 0 {
			m.commandBuffer += msg.Key().Text
			return m, nil
		}
		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		return m, vpCmd
	}
}

func (m *model) handleEditModeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	editKeyStr := msg.String()
	if m.editKeyMap != nil {
		if mapped, ok := m.editKeyMap[editKeyStr]; ok {
			editKeyStr = mapped
		}
	}
	if handled, cmd := m.handleTabShortcut(editKeyStr); handled {
		return m, cmd
	}
	switch editKeyStr {
	case "ctrl+c":
		d := components.NewQuitConfirmDialogForQuit(components.Language(m.currentLang))
		d.SetBounds(m.termWidth, m.termHeight)
		m.dialogStack.Push(d)
		return m, nil

	case "esc":
		if m.keywordAutoComplete {
			m.keywordAutoComplete = false
			m.keywordSuggestions = nil
			m.keywordSuggestionIdx = 0
			return m, nil
		}
		if m.skillAutoComplete {
			m.skillAutoComplete = false
			m.skillSuggestions = nil
			m.skillSuggestionIdx = 0
			return m, nil
		}
		if m.taskRunning && m.currentTask != nil && m.currentTask.CancelFunc != nil {
			d := components.NewQuitConfirmDialogForCancel(components.Language(m.currentLang))
			d.SetBounds(m.termWidth, m.termHeight)
			m.dialogStack.Push(d)
		}
		return m, nil

	case "ctrl+e":
		m.commandMode = true
		m.commandBuffer = ""
		m.invalidateFooterCache()
		return m, nil

	case "ctrl+h":
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
				return m, m.submitFollowUp(taskDesc)
			}
			return m, nil
		}
		return m, m.submitTask()

	case "alt+m":
		if m.taskRunning {
			return m, nil
		}
		return m, m.showModelSelectionDialog()

	case "alt+r":
		if m.taskRunning {
			return m, nil
		}
		return m, m.showRoutingDialog()

	case "alt+d":
		m.toggleDashboard()
		return m, nil

	case "ctrl+l":
		if !m.timelineFullscreenMode {
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
		if m.keywordAutoComplete && len(m.keywordSuggestions) > 0 {
			m.keywordSuggestionIdx = (m.keywordSuggestionIdx + 1) % len(m.keywordSuggestions)
			return m, nil
		}
		if m.skillAutoComplete && len(m.skillSuggestions) > 0 {
			m.skillSuggestionIdx = (m.skillSuggestionIdx + 1) % len(m.skillSuggestions)
			return m, nil
		}
		var inputCmd tea.Cmd
		m.input, inputCmd = m.input.Update(msg)
		return m, inputCmd

	case "enter":
		if m.keywordAutoComplete && len(m.keywordSuggestions) > 0 && m.keywordSuggestionIdx >= 0 && m.keywordSuggestionIdx < len(m.keywordSuggestions) {
			keyword := m.keywordSuggestions[m.keywordSuggestionIdx]
			content := m.input.Value()

			cursorLine := m.input.Line()
			cursorCol := m.input.Column()

			contentRunes := []rune(content)
			byteOffset := 0
			if cursorLine > 0 {
				lines := splitLogicalLines(contentRunes, cursorLine-1)
				for _, line := range lines {
					byteOffset += len(line) + 1
				}
			}
			if cursorCol <= len(contentRunes)-byteOffset {
				byteOffset += cursorCol
			}

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

			var newContent []rune
			newContent = append(newContent, contentRunes[:wordStart]...)
			newContent = append(newContent, []rune(keyword)...)
			newContent = append(newContent, contentRunes[byteOffset:]...)

			m.input.SetValue(string(newContent))
			m.invalidateFooterCache()

			m.keywordAutoComplete = false
			m.keywordSuggestions = nil
			m.keywordSuggestionIdx = 0
			return m, nil
		}
		if m.skillAutoComplete && len(m.skillSuggestions) > 0 && m.skillSuggestionIdx >= 0 && m.skillSuggestionIdx < len(m.skillSuggestions) {
			skillName := m.skillSuggestions[m.skillSuggestionIdx]
			if skillName == "history" {
				m.skillAutoComplete = false
				m.skillSuggestions = nil
				m.skillSuggestionIdx = 0
				if m.taskRunning {
					m.infoMsg = "Cannot browse history while a task is running"
					return m, nil
				}
				m.input.SetValue("")
				m.invalidateFooterCache()
				return m, enterHistoryMode(m)
			}
			if skillName == "resume" || skillName == "rs" {
				m.skillAutoComplete = false
				m.skillSuggestions = nil
				m.skillSuggestionIdx = 0
				if m.taskRunning {
					m.infoMsg = "Cannot resume sessions while a task is running"
					return m, nil
				}
				m.input.SetValue("")
				m.invalidateFooterCache()
				return m, enterResumeMode(m)
			}
			if skill, ok := m.assistant.SkillRegistry.Get(skillName); ok {
				userContext := strings.TrimSpace(m.input.Value())
				combinedContent := skill.Content + "\n\n---\n用户上下文: " + userContext
				m.skillAutoComplete = false
				m.skillSuggestions = nil
				m.skillSuggestionIdx = 0
				m.infoMsg = fmt.Sprintf("正在执行 skill: %s", skill.Name)
				return m, m.submitTaskWithContent(combinedContent)
			}
		}
		var inputCmd tea.Cmd
		m.input, inputCmd = m.input.Update(msg)
		m.invalidateFooterCache()
		return m, tea.Batch(inputCmd, m.scheduleAutocomplete())

	case "/":
		var inputCmd tea.Cmd
		m.input, inputCmd = m.input.Update(msg)
		return m, tea.Batch(inputCmd, m.scheduleAutocomplete())

	case "@":
		var inputCmd tea.Cmd
		m.input, inputCmd = m.input.Update(msg)
		return m, tea.Batch(
			inputCmd,
			runFzfCmd(m.projectDir),
		)

	case "up", "down":
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		m.invalidateFooterCache()
		return m, cmd

	default:
		var inputCmd tea.Cmd
		m.input, inputCmd = m.input.Update(msg)
		m.invalidateFooterCache()
		return m, tea.Batch(inputCmd, m.scheduleAutocomplete())
	}
}
