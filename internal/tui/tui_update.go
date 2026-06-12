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

		// 添加 "history" 和 "model" 作为内置命令
		if hasPrefixIgnoreCase("history", query) {
			matches = append([]string{"history"}, matches...)
		}
		if hasPrefixIgnoreCase("model", query) {
			matches = append([]string{"model"}, matches...)
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
	// 检查配置：如果补全功能被禁用，直接返回
	if !m.keywordCompletionCfg.enabled {
		m.keywordAutoComplete = false
		m.keywordSuggestions = nil
		m.keywordSuggestionIdx = 0
		return
	}

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
		m.saveRunningTaskMemory()
		m.quitting = true
		m.cleanupDebounceTimer()
		return tea.Quit
	case cmd == ":q" || cmd == ":quit":
		d := components.NewQuitConfirmDialogForQuit(components.Language(m.currentLang))
		d.SetBounds(m.termWidth, m.termHeight)
		m.dialogStack.Push(d)
	case cmd == ":help" || cmd == ":h":
		top := m.dialogStack.Top()
		if top != nil && top.ID() == "help_dialog" {
			m.dialogStack.CloseDialog("help_dialog")
		} else {
			m.dialogStack.Push(components.NewHelpDialog(components.Language(m.currentLang)))
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
	// ═══════════════════════════════════════════════════════════════
	// :model — Switch LLM provider (interactive dialog or direct)
	// 支持设置不同 agent 的模型，不支持设置 tool 的模型
	// 语法:
	//   :model                      - 显示状态 + 交互式 agent → provider 选择
	//   :model <agent>              - 显示指定 agent 的 provider 选择
	//   :model <agent> <provider>   - 直接设置 agent 的 provider
	//   :model <provider>           - 设置全局默认 (向后兼容)
	// ═══════════════════════════════════════════════════════════════
	case cmd == ":model" || strings.HasPrefix(cmd, ":model "):
		// Block switching while a task is running
		if m.taskRunning {
			m.logEntries = append(m.logEntries, logEntry{
				timestamp: time.Now(),
				eventType: "status",
				content:   "Cannot switch provider while a task is running. Wait for the task to complete first.",
			})
			m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
			return nil
		}

		parts := strings.Fields(cmd)
		validAgents := []string{"conductor", "coding", "repo", "chat", "meta", "devops", "browser"}

		// Helper: check if string is in list
		isInList := func(s string, list []string) bool {
			for _, item := range list {
				if item == s {
					return true
				}
			}
			return false
		}

		validProviders := m.assistant.GetClient().Config.GetProviderNames()

		// ── 3 args: :model <agent> <provider> ──
		if len(parts) >= 3 {
			agentName := parts[1]
			providerName := parts[2]

			if !isInList(agentName, validAgents) {
				m.logEntries = append(m.logEntries, logEntry{
					timestamp: time.Now(),
					eventType: "status",
					content:   fmt.Sprintf("Unknown agent: %s. Valid agents: %s", agentName, strings.Join(validAgents, ", ")),
				})
				m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
				return nil
			}
			if !isInList(providerName, validProviders) {
				m.logEntries = append(m.logEntries, logEntry{
					timestamp: time.Now(),
					eventType: "status",
					content:   fmt.Sprintf("Unknown provider: %s. Available providers: %s", providerName, strings.Join(validProviders, ", ")),
				})
				m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
				return nil
			}
			if err := m.assistant.SetAgentProvider(agentName, providerName); err != nil {
				m.logEntries = append(m.logEntries, logEntry{
					timestamp: time.Now(),
					eventType: "error",
					content:   fmt.Sprintf("Failed to set agent provider: %v", err),
				})
				m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
				return nil
			}
			_, modelName := m.assistant.GetAgentProvider(agentName)
			m.logEntries = append(m.logEntries, logEntry{
				timestamp: time.Now(),
				eventType: "status",
				content:   fmt.Sprintf("Set agent '%s' provider to: %s (model: %s)", agentName, providerName, modelName),
			})
			m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
			return nil
		}

		// ── 2 args: :model <agent> or :model <provider> ──
		if len(parts) == 2 {
			arg := parts[1]

			// Check if arg is a known agent name
			if isInList(arg, validAgents) {
				// :model <agent> — show provider selection for this agent
				m.pendingModelTarget = arg
				providers := m.assistant.GetClient().Config.GetProviderNames()
				providerDescs := make(map[string]string)
				currentProv, _ := m.assistant.GetAgentProvider(arg)
				for _, p := range providers {
					if provCfg, err := m.assistant.GetClient().Config.GetProvider(p); err == nil {
						providerDescs[p] = components.FormatProviderDesc(p, provCfg.Model)
					} else {
						providerDescs[p] = p
					}
				}
				dialog := components.NewModelSelectDialog(providers, providerDescs, currentProv)
				dialog.SetBounds(m.termWidth, m.termHeight)
				m.dialogStack.Push(dialog)
				return nil
			}

			// Otherwise treat as :model <provider> (global default, backward compat)
			if !isInList(arg, validProviders) {
				m.logEntries = append(m.logEntries, logEntry{
					timestamp: time.Now(),
					eventType: "status",
					content:   fmt.Sprintf("Unknown provider: %s. Available providers: %s", arg, strings.Join(validProviders, ", ")),
				})
				m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
				return nil
			}
			if err := m.assistant.SwitchProvider(arg); err != nil {
				m.logEntries = append(m.logEntries, logEntry{
					timestamp: time.Now(),
					eventType: "error",
					content:   fmt.Sprintf("Failed to switch provider: %v", err),
				})
				m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
				return nil
			}
			_, modelName := m.assistant.GetClient().GetCurrentProviderInfo()
			m.currentProvider = arg
			m.currentModel = modelName
			m.pendingModelTarget = "" // clear any pending agent target
			m.logEntries = append(m.logEntries, logEntry{
				timestamp: time.Now(),
				eventType: "status",
				content:   fmt.Sprintf("Switched global provider to: %s (model: %s)", arg, modelName),
			})
			m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
			return nil
		}

		// ── No args: :model — show interactive target selection dialog ──
		// Build config entries for global + all agents
		entries := make([]components.ConfigEntry, 0, len(validAgents)+1)
		// Global entry
		entries = append(entries, components.ConfigEntry{
			Target:      "global",
			DisplayName: "global",
			Provider:    m.currentProvider,
			Model:       m.currentModel,
		})
		// Agent entries
		for _, agent := range validAgents {
			prov, model := m.assistant.GetAgentProvider(agent)
			entries = append(entries, components.ConfigEntry{
				Target:      agent,
				DisplayName: agent,
				Provider:    prov,
				Model:       model,
			})
		}
		dialog := components.NewAgentSelectDialog(entries)
		dialog.SetBounds(m.termWidth, m.termHeight)
		m.dialogStack.Push(dialog)
		return nil
	// ═══════════════════════════════════════════════════════════════
	// /pattern — Search in log entries (must come AFTER more specific / commands)
	// ═══════════════════════════════════════════════════════════════
	case strings.HasPrefix(cmd, "/"):
		// Search in log entries
		query := strings.TrimPrefix(cmd, "/")
		m.searchInLog(query)
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
	m.viewportDirty = true
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
	if m.dialogStack != nil && m.dialogStack.Len() > 0 {
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

	// ====== DialogStack: takes priority even over history mode ======
	if m.dialogStack != nil && m.dialogStack.Len() > 0 {
		topDialog := m.dialogStack.Top()
		if topDialog != nil {
			newComp, cmd := topDialog.Update(msg)
			if newComp != nil {
				m.dialogStack.ReplaceTop(newComp.(components.Dialog))
			}
			if cmd != nil {
				return m, cmd
			}
		}
	}

	// History mode: intercept all messages and delegate to history handler.
	// Skip when a dialog is active so dialog confirmations (e.g. delete_history_confirm)
	// can be processed by the DialogStack key handler below.
	if m.historyMode && (m.dialogStack == nil || m.dialogStack.Len() == 0) {
		return historyUpdate(msg, &m)
	}

	switch msg := msg.(type) {
	case tickMsg:
		m.animFrame++

		if m.animManager != nil {
			m.animManager.Tick(100)
		}

		// Stop tick early when idle: avoids unnecessary View() calls that cause flicker
		if !m.activeAnim && !m.taskRunning {
			m.tickStarted = false
			return m, nil
		}

		animChanged := m.anim.Tick()

		if m.activeAnim || m.taskRunning {
			// Only rebuild status bar cache when animation actually changed
			// or when taskRunning first becomes true (status bar shows RUN mode)
			if animChanged || !m.statusBarValid {
				m.cachedStatusBar = m.renderAirlineStatusBar()
				m.statusBarValid = true
			}

			// Only update viewport content if animation changed and there are
			// visible animated entries. Skip the expensive re-render + SetContent
			// when the progress bar position didn't move.
			if m.activeAnim && animChanged {
				visStart, visEnd := m.visibleEntryIndices()
				hasVisibleAnim := false

				// Invalidate cache only for visible running entries
				for i := visStart; i <= visEnd && i < len(m.logEntries); i++ {
					entry := &m.logEntries[i]
					if entry.toolEntry != nil && entry.toolEntry.Status == ToolStatusRunning {
						entry.clearRenderCache()
						entry.toolEntry.InvalidateCache()
						hasVisibleAnim = true
					} else if entry.eventType == "llm_call_start" && entry.isToolRunning {
						entry.clearRenderCache()
						hasVisibleAnim = true
					}
				}

				if hasVisibleAnim {
					// Re-render only the visible animated entries
					width := m.viewport.Width()
					for i := visStart; i <= visEnd && i < len(m.logEntries); i++ {
						entry := &m.logEntries[i]
						if (entry.toolEntry != nil && entry.toolEntry.Status == ToolStatusRunning) ||
							(entry.eventType == "llm_call_start" && entry.isToolRunning) {
							m.setEntryContent(i, m.renderSingleEntry(entry, width))
						}
					}
					m.assembleViewportContent()
					m.viewport.SetContent(m.contentCache.String())
					if m.viewport.AtBottom() {
						m.viewport.GotoBottom()
					}
				}
			}

			// Non-animation dirty rebuild
			if m.viewportDirty && !m.activeAnim {
				m.rebuildViewportPreservingScroll()
				m.viewportDirty = false
			}

		}
		return m, tickCmd()

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		m.input.SetWidth(m.computeFieldWidth())
		m.resizeViewport()
		m.invalidateRenderedCache()
		m.invalidateFooterCache()
		m.viewportDirty = true
		m.rebuildViewportScrollLock()
		// 始终启动/重启 tick 循环（空闲时 tick 会自动停止）
		if m.layoutEngine != nil {
			m.layoutEngine.Resize(msg.Width, msg.Height)
		}
		return m, tickCmd()

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
				m.updateViewportCache()
			case components.MouseScrollDown:
				// 滚动向下（viewport 增加行）
				m.viewport.ScrollDown(3)
				m.updateViewportCache()
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
					m.saveRunningTaskMemory()
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
								m.saveRunningTaskMemory()
								m.quitting = true
								m.cleanupDebounceTimer()
								return m, tea.Quit
							}
							// Delete history confirmation
							if d.ID() == "delete_history_confirm" {
								taskID := m.pendingDeleteTaskID
								m.pendingDeleteTaskID = ""
								m.dialogStack.Pop()
								return m, confirmDeleteHistoryEntryByID(&m, taskID)
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

				case *components.AgentSelectDialog:
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
							if target == "global" {
								currentProv = m.currentProvider
							} else {
								currentProv, _ = m.assistant.GetAgentProvider(target)
							}
							for _, p := range providers {
								if provCfg, err := m.assistant.GetClient().Config.GetProvider(p); err == nil {
									providerDescs[p] = components.FormatProviderDesc(p, provCfg.Model)
								} else {
									providerDescs[p] = p
								}
							}
							providerDialog := components.NewModelSelectDialog(providers, providerDescs, currentProv)
							providerDialog.SetBounds(m.termWidth, m.termHeight)
							m.dialogStack.Push(providerDialog)
						} else {
							m.dialogStack.Pop()
						}
						return m, nil
					case "esc", "q", "Q":
						d.Selected = ""
						m.dialogStack.Pop()
						return m, nil
					}
					return m, nil

				case *components.ModelSelectDialog:
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
								return m, nil
							}
							// 根据 pendingModelTarget 决定是设置 agent 还是全局
							if m.pendingModelTarget != "" {
								// 为指定 agent 设置 provider
								agentName := m.pendingModelTarget
								m.pendingModelTarget = ""
								if err := m.assistant.SetAgentProvider(agentName, d.Selected); err != nil {
									m.logEntries = append(m.logEntries, logEntry{
										timestamp: time.Now(),
										eventType: "error",
										content:   fmt.Sprintf("Failed to set agent provider: %v", err),
									})
									m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
								} else {
									_, modelName := m.assistant.GetAgentProvider(agentName)
									m.logEntries = append(m.logEntries, logEntry{
										timestamp: time.Now(),
										eventType: "status",
										content:   fmt.Sprintf("Set agent '%s' provider to: %s (model: %s)", agentName, d.Selected, modelName),
									})
									m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
								}
							} else {
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
						return m, nil
					case "esc", "q", "Q":
						d.Selected = ""
						m.dialogStack.Pop()
						return m, nil
					}
					return m, nil

				case *components.HelpDialog:
					switch key {
					case "esc", "i", "I":
						m.dialogStack.Pop()
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
						m.dialogStack.Push(components.NewHelpDialog(components.Language(m.currentLang)))
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

			// ── Collapse toggle ──
			case "ctrl+o":
				m.toggleCollapseAll()
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
				// If "model" is selected, open model selection dialog with target picker
				if skillName == "model" {
					m.skillAutoComplete = false
					m.skillSuggestions = nil
					m.skillSuggestionIdx = 0
					if m.taskRunning {
						m.infoMsg = "Cannot switch model while a task is running"
						return m, nil
					}
					// Clear the input field
					m.input.SetValue("")

					// Show interactive target selection dialog (same as :model no-args)
					validAgents := []string{"conductor", "coding", "repo", "chat", "meta", "devops", "browser"}
					entries := make([]components.ConfigEntry, 0, len(validAgents)+1)
					// Global entry
					entries = append(entries, components.ConfigEntry{
						Target:      "global",
						DisplayName: "global",
						Provider:    m.currentProvider,
						Model:       m.currentModel,
					})
					// Agent entries
					for _, agent := range validAgents {
						prov, model := m.assistant.GetAgentProvider(agent)
						entries = append(entries, components.ConfigEntry{
							Target:      agent,
							DisplayName: agent,
							Provider:    prov,
							Model:       model,
						})
					}
					dialog := components.NewAgentSelectDialog(entries)
					dialog.SetBounds(m.termWidth, m.termHeight)
					m.dialogStack.Push(dialog)
					return m, nil
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
		// 补全状态变化时刷新 footer 缓存以正确计算 viewport 高度
		m.invalidateFooterCache()


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
		if m.dialogStack != nil && m.dialogStack.Len() > 0 {
			if m.taskRunning {
				return m, listenForEvents(m.eventCh)
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
				// Token counts changed — update cached token dashboard render
				m.cachedTokenDashboard = m.renderTokenDashboard()
				m.tokenDashboardValid = true
				m.invalidateFooterCache()
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
				// Capture provider name if available
				if providerName, ok := contentMap["provider"].(string); ok && providerName != "" {
					m.currentProvider = providerName
				}
			}
			// Model info changed — update status bar cache
			m.invalidateFooterCache()
			return m, listenForEvents(m.eventCh)
		}

		// Handle llm_call_start — create a running entry with animation (single line)
		if msg.event.Type == "llm_call_start" {
			entry := formatEventAsEntry(msg.event)
			entry.isToolRunning = true

			// Generate a unique ID for this LLM call (use agent name + timestamp)
			callID := fmt.Sprintf("llm_%s_%d", msg.event.From, msg.event.Timestamp.UnixNano())
			entry.toolCallID = callID

			m.logEntries = append(m.logEntries, entry)
			m.viewportDirty = true
			m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])

			// Store the entry index for llm_call_end to update
			m.llmCallActiveEntries[msg.event.From] = len(m.logEntries) - 1
			m.activeAnim = true

			return m, listenForEvents(m.eventCh)
		}

		// Handle llm_call_end — update the matching start entry (no new entry created)
		if msg.event.Type == "llm_call_end" {
			if idx, ok := m.llmCallActiveEntries[msg.event.From]; ok && idx >= 0 && idx < len(m.logEntries) {
				delete(m.llmCallActiveEntries, msg.event.From)

				// Update the log entry with end information
				le := &m.logEntries[idx]
				le.isToolRunning = false

				// Format content like current llm_call_end with duration
				if durationRaw, ok := msg.event.Metadata["duration_seconds"]; ok {
					var duration float64
					switch v := durationRaw.(type) {
					case float64:
						duration = v
					case int:
						duration = float64(v)
					}

					modelName, _ := msg.event.Metadata["model"].(string)
					agentName, _ := msg.event.Metadata["agent"].(string)
					if modelName == "" {
						if m, ok := msg.event.Content.(map[string]interface{}); ok {
							modelName, _ = m["model"].(string)
						}
					}

					hasError := false
					if errStr, ok := msg.event.Metadata["error"]; ok && errStr != "" {
						hasError = true
					}

					if hasError {
						le.content = fmt.Sprintf("◂ %s  [%s]  ✗ %.2fs", agentName, modelName, duration)
					} else {
						le.content = fmt.Sprintf("◂ %s  [%s]  ✓ %.2fs", agentName, modelName, duration)
					}
				} else {
					le.content = "◂ LLM call completed"
				}

				le.clearRenderCache()      // invalidate cache
				m.markEntryDirty(idx)      // 细粒度：仅标记此条目脏
				m.updateActiveAnim()
				m.viewportDirty = true
				m.rebuildViewportScrollLock()
			}
			return m, listenForEvents(m.eventCh)
		}

		// Intercept user_help_needed to show interactive dialog
		if msg.event.Type == "user_help_needed" {
			m.openConfirmDialog(msg.event)
			// Still log the event so it appears in the background
			entry := formatEventAsEntry(msg.event)
			m.logEntries = append(m.logEntries, entry)
			m.viewportDirty = true
			m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
			return m, listenForEvents(m.eventCh)
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
					m.viewportDirty = true
					m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
				}
			}
			return m, listenForEvents(m.eventCh)
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
						le.clearRenderCache()      // invalidate cache
						m.markEntryDirty(idx)      // 细粒度：仅标记此条目脏
						m.viewportDirty = true
					}
					delete(m.toolCallEntries, callID)
					m.updateActiveAnim()
					m.viewportDirty = true
					m.rebuildViewportScrollLock()
					return m, listenForEvents(m.eventCh)
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
						le.clearRenderCache()
						m.markEntryDirty(idx)      // 细粒度：仅标记此条目脏
						m.viewportDirty = true
					}
					delete(m.toolCallEntries, matchedID)
					m.updateActiveAnim()
					m.viewportDirty = true
					m.rebuildViewportScrollLock()
					return m, listenForEvents(m.eventCh)
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
		m.viewportDirty = true
		m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
		return m, listenForEvents(m.eventCh)

	case taskCompleteMsg:
		m.taskRunning = false
		m.currentAgent = ""
		m.commandMode = false
		m.dialogStack.CloseDialog("confirm_dialog") // safety: close any stale dialog
		m.invalidateFooterCache()

		// 如果是用户主动取消，不显示错误弹窗或完成弹窗
		if m.taskCancelled {
			m.taskCancelled = false
			m.currentTask = nil
			return m, nil
		}
		m.taskCancelled = false

		if msg.err != nil {
			m.errMsg = msg.err.Error()
			m.logEntries = append(m.logEntries, logEntry{
				timestamp: time.Now(),
				eventType: "error",
				content:   msg.err.Error(),
			})
			m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
			// Show error dialog via DialogStack
		d := components.NewTaskCompleteDialog(false, "❌ Task Failed\n\n"+msg.err.Error(), components.Language(m.currentLang))
		d.SetBounds(m.termWidth, m.termHeight)
		m.dialogStack.Push(d)
	} else {
		// 保留 currentTask 以支持任务完成后继续对话
		// m.currentTask = nil  // 不再清除
		d := components.NewTaskCompleteDialog(true, "All tasks have been finished.", components.Language(m.currentLang))
		d.SetBounds(m.termWidth, m.termHeight)
		m.dialogStack.Push(d)
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

// saveRunningTaskMemory saves the current task's memory before quitting,
// ensuring conversation history is not lost when TUI exits mid-execution.
func (m *model) saveRunningTaskMemory() {
	if m.currentTask != nil && m.dataManager != nil && m.taskRunning {
		m.dataManager.SaveTaskMemory(m.currentTask.ID, m.currentTask.Memory)
		m.dataManager.FlushTaskMemory(m.currentTask.ID)
	}
}
