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
	// 提示行是一次性的：任何按键都会把它让位给这次操作自己的结果
	// （包括新的 setInfoMsg），不需要定时器就能保证它不会赖在底部。
	if m.infoMsg != "" {
		m.infoMsg = ""
		m.invalidateFooterCache()
	}

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
			choice := d.Choice()
			m.dialogStack.Pop()
			if !confirmed || m.currentTask == nil || m.taskRunning {
				return m, nil, true
			}
			// 选了"从 <agent> step <n> 继续"：这一轮先让被停住的那次委派接着做完，
			// 而不是把任务描述再交给 Director 重想一遍。恢复本身不调模型，
			// 所以点这个选项的代价是确定性的（§6.3）。
			// 另一档（从 Director 继续）必须把这个指路牌清掉 —— 留着它，
			// 下一次追问会以为用户还是"从子 Agent 继续"，于是又提交一遍同一份重放结果。
			if choice != components.ResumeChoiceAgent {
				m.resumedDelegationPath = ""
			}
			return m, m.submitFollowUp(buildResumeContinuationPrompt(d)), true
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
					// 取实际生效的引擎，而不是配置里的默认 provider：SwitchProvider 只换
					// Client.Engine，不动配置，所以 GetCurrentProviderInfo 会说回旧模型。
					modelName := ""
					if eng := m.assistant.GetClient().DefaultEngine(); eng != nil {
						modelName = eng.Model()
					}
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

	// 信息面板（/skills、/mcp）按接口认领，不再往上面的类型 switch 里加 case：
	// 漏一条 case 的后果不是编译错误，而是"这个面板进得去出不来"（Esc 连同滚动键
	// 一起漏给编辑模式，覆盖层却还压在屏幕上）。面板是模态只读的，所以每一把键都归它。
	if panel, ok := top.(components.InfoPanel); ok {
		if panel.HandleInfoKey(msg) {
			m.dialogStack.Pop()
		}
		return m, nil, true
	}

	return m, nil, false
}

func (m *model) handleTabShortcut(key string) (bool, tea.Cmd) {
	switch key {
	case "ctrl+t":
		if m.taskRunning {
			m.setInfoMsg(langManager.GetText("TabCannotWhileRunning"))
			return true, nil
		}
		if len(m.sessionTabs) >= m.maxTabs {
			m.setInfoMsg(langManager.GetText("TabMaxReached"))
			return true, nil
		}
		return true, m.newSessionTabAction()
	case "alt+[":
		if m.taskRunning {
			m.setInfoMsg(langManager.GetText("TabCannotWhileRunning"))
			return true, nil
		}
		if m.activeSessionIdx > 0 {
			m.switchSessionTab(m.activeSessionIdx - 1)
		}
		return true, nil
	case "alt+]":
		if m.taskRunning {
			m.setInfoMsg(langManager.GetText("TabCannotWhileRunning"))
			return true, nil
		}
		if m.activeSessionIdx < len(m.sessionTabs)-1 {
			m.switchSessionTab(m.activeSessionIdx + 1)
		}
		return true, nil
	case "alt+c":
		if m.taskRunning {
			m.setInfoMsg(langManager.GetText("TabCannotWhileRunning"))
			return true, nil
		}
		m.clearCurrentSession()
		return true, nil
	case "alt+w":
		if m.taskRunning {
			m.setInfoMsg(langManager.GetText("TabCannotWhileRunning"))
			return true, nil
		}
		if len(m.sessionTabs) <= 1 {
			m.setInfoMsg(langManager.GetText("TabCloseLastBlocked"))
			return true, nil
		}
		m.closeCurrentSessionTab()
		return true, nil
	}
	if strings.HasPrefix(key, "alt+") {
		numStr := strings.TrimPrefix(key, "alt+")
		if n, err := strconv.Atoi(numStr); err == nil && n >= 1 && n <= 9 {
			if m.taskRunning {
				m.setInfoMsg(langManager.GetText("TabCannotWhileRunning"))
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

// handleCommandModeKey 处理 NORMAL（vim 普通模式）按键，分三层依次分派：
// 命令行输入 → 全局快捷键 → 移动/跳转动作。
func (m *model) handleCommandModeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := translateKey(m.cmdKeyMap, msg.String())

	// 命令行激活时按键优先归命令行。此时 commandBuffer 一定以 ":" 或 "/" 开头
	// —— 只有这两个前缀才会开启命令行，其它可打印字符不再被悄悄吞进缓冲区
	// （旧实现里在普通模式下随便打字都会攒进 buffer）。
	if m.commandBuffer != "" {
		if model, cmd, handled := m.handleCmdlineKey(key, msg); handled {
			return model, cmd
		}
	}

	if handled, cmd := m.handleTabShortcut(key); handled {
		return m, cmd
	}
	if handled, cmd := m.handleNormalGlobalKey(key); handled {
		return m, cmd
	}

	// 数字前缀："10j" 下滚 10 行，"5G" 跳到第 5 行。
	if len(key) == 1 && key[0] >= '0' && key[0] <= '9' {
		if key[0] == '0' && m.pendingCount == "" {
			return m, nil // 前导 0 既不是计数，这里也没有水平滚动语义，直接忽略
		}
		m.pendingCount += key
		return m, nil
	}

	count := m.takeCount()

	// 双键动作
	if m.lastKey != "" {
		combo := m.lastKey + key
		m.lastKey = ""
		if combo == "gg" {
			m.viewport.GotoTop()
			m.updateViewportCache()
			m.invalidateFooterCache()
			return m, nil
		}
	}

	switch key {
	case "j", "down":
		m.viewport.ScrollDown(count)
	case "k", "up":
		m.viewport.ScrollUp(count)
	case "f", "ctrl+f":
		m.viewport.PageDown()
	case "b", "ctrl+b":
		m.viewport.PageUp()
	case "u", "ctrl+u":
		m.viewport.HalfPageUp()
	case "d", "ctrl+d":
		m.viewport.HalfPageDown()
	case "G":
		if count > 1 {
			m.viewport.SetYOffset(count - 1)
		} else {
			m.viewport.GotoBottom()
		}
	case "g":
		m.lastKey = key
		return m, nil

	case "n":
		m.searchStep(1)
		return m, nil
	case "N":
		m.searchStep(-1)
		return m, nil

	case "i", "a", "ctrl+e":
		return m, m.enterInsertMode()

	case ":", "/":
		m.commandBuffer = key
		m.resetCmdRecall()
		m.invalidateFooterCache()
		return m, nil

	case "enter":
		// 空命令行 + 回车：回编辑模式，相当于 vim 里按回车继续输入。
		return m, m.enterInsertMode()

	case "esc":
		return m, m.normalEscape()

	default:
		var vpCmd tea.Cmd
		m.viewport, vpCmd = m.viewport.Update(msg)
		m.invalidateFooterCache()
		return m, vpCmd
	}

	m.updateViewportCache()
	m.invalidateFooterCache()
	return m, nil
}

// takeCount 取出并清空数字前缀，无前缀时按 1 计。
func (m *model) takeCount() int {
	if m.pendingCount == "" {
		return 1
	}
	n, err := strconv.Atoi(m.pendingCount)
	m.pendingCount = ""
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// handleNormalGlobalKey 处理任何 NORMAL 状态下都可用的快捷键。
func (m *model) handleNormalGlobalKey(key string) (bool, tea.Cmd) {
	switch key {
	case "ctrl+c":
		d := components.NewQuitConfirmDialogForQuit(components.Language(m.currentLang))
		d.SetBounds(m.termWidth, m.termHeight)
		m.dialogStack.Push(d)
		return true, nil

	case "alt+t":
		// 只 toggle + 失效：在这里预渲染会被紧接着的 invalidate 丢掉，
		// 面板由 tokenDashboardBlock 按需重算。
		m.tokenDashboardCollapsed = !m.tokenDashboardCollapsed
		m.invalidateFooterCache()
		return true, nil

	case "alt+d":
		m.toggleDashboard()
		return true, nil

	case "alt+m":
		return true, m.showModelSelectionDialog()

	case "alt+r":
		return true, m.showRoutingDialog()

	case "ctrl+l":
		m.toggleTimelineFullscreen()
		return true, nil

	case "ctrl+g":
		if !m.thinklinkMode {
			return true, enterThinklinkMode(m)
		}
		exitThinklinkMode(m)
		return true, nil

	case "?":
		return true, m.toggleHelpDialog()
	}
	return false, nil
}

// handleCmdlineKey 处理命令行内的按键。光标固定在行尾 —— 这是刻意的最小实现：
// 命令行只承载短命令与搜索串，多行文本编辑交给编辑面板本身。
func (m *model) handleCmdlineKey(key string, msg tea.KeyMsg) (tea.Model, tea.Cmd, bool) {
	switch key {
	case "enter":
		text := m.commandBuffer
		m.commandBuffer = ""
		m.pendingCount = ""
		m.resetCmdRecall()
		m.pushCmdHistory(text)
		m.invalidateFooterCache()
		return m, m.processCommand(text), true

	case "esc":
		m.commandBuffer = ""
		m.pendingCount = ""
		m.resetCmdRecall()
		m.invalidateFooterCache()
		return m, nil, true

	case "backspace", "ctrl+h":
		// 按 rune 退格：命令里含中文时按字节删会切坏半个字符。
		r := []rune(m.commandBuffer)
		if len(r) > 1 {
			m.commandBuffer = string(r[:len(r)-1])
		} else {
			m.commandBuffer = "" // 只剩前缀时再退一次即关闭命令行
		}
		m.resetCmdRecall()
		m.invalidateFooterCache()
		return m, nil, true

	case "ctrl+u":
		m.commandBuffer = m.commandBuffer[:1] // 只留 ":" 或 "/"
		m.resetCmdRecall()
		m.invalidateFooterCache()
		return m, nil, true

	case "up", "ctrl+p":
		m.recallCmdHistory(-1)
		return m, nil, true

	case "down", "ctrl+n":
		m.recallCmdHistory(1)
		return m, nil, true
	}

	if txt := msg.Key().Text; txt != "" {
		m.commandBuffer += txt
		m.resetCmdRecall()
		m.invalidateFooterCache()
		return m, nil, true
	}
	return m, nil, false
}

func (m *model) resetCmdRecall() {
	m.cmdRecalling = false
	m.cmdRecallIdx = 0
	m.cmdDraft = ""
}

// pushCmdHistory 记录已执行的命令行，连续重复只留一条，超出上限丢弃最旧的。
func (m *model) pushCmdHistory(cmd string) {
	if cmd == "" || (len(m.cmdHistory) > 0 && m.cmdHistory[len(m.cmdHistory)-1] == cmd) {
		return
	}
	m.cmdHistory = append(m.cmdHistory, cmd)
	const cmdHistoryMax = 50
	if len(m.cmdHistory) > cmdHistoryMax {
		m.cmdHistory = m.cmdHistory[len(m.cmdHistory)-cmdHistoryMax:]
	}
}

// recallCmdHistory 在命令行历史中导航，dir=-1 往更早、+1 往更新；
// 走到最新之后恢复用户开始导航前敲的半截命令。
func (m *model) recallCmdHistory(dir int) {
	if len(m.cmdHistory) == 0 {
		return
	}
	if !m.cmdRecalling {
		m.cmdRecalling = true
		m.cmdRecallIdx = len(m.cmdHistory) // 下一格就是"最新再往前"
		m.cmdDraft = m.commandBuffer
	}
	m.cmdRecallIdx += dir

	switch {
	case m.cmdRecallIdx >= len(m.cmdHistory):
		m.cmdRecallIdx = len(m.cmdHistory)
		m.commandBuffer = m.cmdDraft
	case m.cmdRecallIdx < 0:
		m.cmdRecallIdx = 0
		m.commandBuffer = m.cmdHistory[0]
	default:
		m.commandBuffer = m.cmdHistory[m.cmdRecallIdx]
	}
	m.invalidateFooterCache()
}

// normalEscape 是 NORMAL 模式下 Esc 的分层退出：
// 先取消数字前缀，再清除搜索高亮，最后才谈任务取消确认。
func (m *model) normalEscape() tea.Cmd {
	m.lastKey = ""
	if m.pendingCount != "" {
		m.pendingCount = ""
		m.invalidateFooterCache()
		return nil
	}
	if m.searchPattern != "" {
		m.clearSearch()
		return nil
	}
	if m.taskRunning && m.currentTask != nil && m.currentTask.CancelFunc != nil {
		d := components.NewQuitConfirmDialogForCancel(components.Language(m.currentLang))
		d.SetBounds(m.termWidth, m.termHeight)
		m.dialogStack.Push(d)
	}
	return nil
}

// enterInsertMode 回到编辑（INSERT）模式。运行中也允许进入：提交本来就由
// alt+s 那侧拦截，若在此处一并禁止，Esc 进 NORMAL 后会把自己关在外面。
func (m *model) enterInsertMode() tea.Cmd {
	m.commandMode = false
	m.commandBuffer = ""
	m.lastKey = ""
	m.pendingCount = ""
	m.resetCmdRecall()
	m.invalidateFooterCache()
	return nil
}

// enterNormalMode 从编辑模式回到 NORMAL 模式（编辑模式下的 Esc）。
func (m *model) enterNormalMode() {
	m.commandMode = true
	m.commandBuffer = ""
	m.lastKey = ""
	m.pendingCount = ""
	m.resetCmdRecall()
	m.invalidateFooterCache()
}

// toggleTimelineFullscreen 打开/关闭时间线全屏视图。
func (m *model) toggleTimelineFullscreen() {
	if m.timelineFullscreenMode {
		m.ExitTimelineFullscreen()
	} else {
		m.timelineFullscreenMode = true
		m.timelineFullscreenFocus = "list"
		if len(m.timelineEntries) > 0 {
			m.timelineFullscreenCursor = len(m.timelineEntries) - 1
		} else {
			m.timelineFullscreenCursor = 0
		}
		initTimelineDetailViewport(m)
	}
	m.timelineExpanded = false
	m.timelineCacheKey = ""
	m.invalidateFooterCache()
}

// toggleHelpDialog 打开帮助对话框；已打开则关闭（"?"、":help"、ctrl+h 共用）。
func (m *model) toggleHelpDialog() tea.Cmd {
	if m.dialogStack == nil {
		m.dialogStack = components.NewDialogStack()
	}
	if top := m.dialogStack.Top(); top != nil && top.ID() == "help_dialog" {
		m.dialogStack.CloseDialog("help_dialog")
		return nil
	}
	d := components.NewHelpDialog(components.Language(m.currentLang))
	if m.com != nil && m.com.Config != nil {
		if overrides := buildHelpKeyOverrides(&m.com.Config.TUI.Keybindings); len(overrides) > 0 {
			d.SetAltKeybindings(overrides)
		}
	}
	d.SetBounds(m.termWidth, m.termHeight)
	m.dialogStack.Push(d)
	return nil
}

func (m *model) handleEditModeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	editKeyStr := translateKey(m.editKeyMap, msg.String())
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
		if m.taskRunning {
			if m.currentTask != nil && m.currentTask.CancelFunc != nil {
				d := components.NewQuitConfirmDialogForCancel(components.Language(m.currentLang))
				d.SetBounds(m.termWidth, m.termHeight)
				m.dialogStack.Push(d)
			}
			return m, nil
		}
		// 空闲时 Esc 回到 NORMAL 模式，与 vim 一致 —— 这是进入普通模式的
		// 主路径，之前只有 ctrl+e 一条路，手上是 Esc 的人永远进不去。
		m.enterNormalMode()
		return m, nil

	case "ctrl+e":
		m.enterNormalMode()
		return m, nil

	case "ctrl+h":
		return m, m.toggleHelpDialog()

	case "alt+s", "ctrl+s":
		return m, m.submitFromEditor()

	case "alt+m":
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
		m.toggleTimelineFullscreen()
		return m, nil

	case "ctrl+f":
		m.viewport.PageDown()
		m.updateViewportCache()
		m.invalidateFooterCache()
		return m, nil

	case "ctrl+b":
		m.viewport.PageUp()
		m.updateViewportCache()
		m.invalidateFooterCache()
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
		if item, ok := m.selectedCompletionItem(); ok {
			// 收尾一律先清补全状态：下面每条分支都会离开补全界面。
			m.skillAutoComplete = false
			m.skillSuggestions = nil
			m.skillSuggestionIdx = 0

			switch item.kind {
			case completionCmd:
				switch item.name {
				case "history":
					if m.taskRunning {
						m.setInfoMsg("Cannot browse history while a task is running")
						return m, nil
					}
					m.input.SetValue("")
					m.invalidateFooterCache()
					return m, enterHistoryMode(m)
				case "resume", "rs":
					if m.taskRunning {
						m.setInfoMsg("Cannot resume sessions while a task is running")
						return m, nil
					}
					m.input.SetValue("")
					m.invalidateFooterCache()
					return m, enterResumeMode(m)
				default:
					m.input.SetValue("")
					m.invalidateFooterCache()
					return m, m.processCommand(item.name)
				}
			case completionSkill:
				args := strings.TrimSpace(m.input.Value())
				m.input.SetValue("")
				m.invalidateFooterCache()
				return m, m.runSkill(item.name, args)
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
