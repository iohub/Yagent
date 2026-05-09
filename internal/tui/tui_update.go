package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"codeactor/internal/compact"
	"codeactor/internal/datamanager"
	"codeactor/internal/http"

	"github.com/google/uuid"

	"charm.land/bubbles/v2/list"
	tea "charm.land/bubbletea/v2"
)

func (m *model) processCommand(cmd string) tea.Cmd {
	cmd = strings.TrimSpace(cmd)

	switch {
	case cmd == ":q" || cmd == ":quit" || cmd == ":q!":
		m.confirmQuitDialog.open = true
	case strings.HasPrefix(cmd, "/"):
		// Search in log entries
		query := strings.TrimPrefix(cmd, "/")
		m.searchInLog(query)
	case cmd == ":help" || cmd == ":h":
		m.showHelpDialog = true
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

// loadTaskHistoryItems loads the task history list (cached) for quick cycling
// in edit mode. Called lazily on first up/down press.
func (m *model) loadTaskHistoryItems() {
	if len(m.taskHistoryItems) > 0 {
		return // already loaded
	}
	dm, err := datamanager.NewDataManager()
	if err != nil {
		return
	}
	items, err := dm.ListTaskHistory(50)
	if err != nil {
		return
	}
	m.taskHistoryItems = items
}

// handleTaskHistoryCycle handles up/down arrow key presses in edit mode when
// the input is empty. It cycles through the task history list and loads the
// selected task description into the input field.
func (m *model) handleTaskHistoryCycle(direction string) {
	m.loadTaskHistoryItems()
	if len(m.taskHistoryItems) == 0 {
		return
	}

	n := len(m.taskHistoryItems)

	switch direction {
	case "up":
		if m.taskHistoryIdx < 0 {
			// First press: start from the newest (index 0)
			m.taskHistoryIdx = 0
		} else {
			m.taskHistoryIdx++
			if m.taskHistoryIdx >= n {
				m.taskHistoryIdx = 0 // wrap around
			}
		}
	case "down":
		if m.taskHistoryIdx < 0 {
			// First press: start from the newest (index 0)
			m.taskHistoryIdx = 0
		} else {
			m.taskHistoryIdx--
			if m.taskHistoryIdx < 0 {
				m.taskHistoryIdx = n - 1 // wrap around
			}
		}
	}

	// Load the selected task description
	if m.taskHistoryIdx >= 0 && m.taskHistoryIdx < n {
		m.input.SetValue(m.taskHistoryItems[m.taskHistoryIdx].Title)
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Global popup guard: when any overlay is shown, only allow KeyMsg through.
	// All other message types (tickMsg, taskEventMsg, WindowSizeMsg, etc.) are
	// blocked to prevent viewport scrolling behind the overlay.
	// NOTE: showHistoryPanel is handled separately below to allow historyLoadedMsg.
	if m.showHelpDialog || m.confirmDialog.open ||
		m.taskCompleteDialog.open || m.confirmQuitDialog.open || m.confirmCancelDialog.open {
		if _, ok := msg.(tea.KeyMsg); !ok {
			return m, nil
		}
	}
	// History panel: allow KeyMsg, historyLoadedMsg, historyDeletedMsg,
	// continueWithTaskMsg, and WindowSizeMsg to pass through.
	if m.historyPanel != nil && m.historyPanel.active {
		switch msg.(type) {
		case tea.KeyMsg, historyLoadedMsg, historyDeletedMsg, continueWithTaskMsg, tea.WindowSizeMsg:
			// allowed — fall through to main switch
		default:
			return m, nil
		}
	}

	switch msg := msg.(type) {
	case tickMsg:
		// Advance animation and rebuild viewport if there are running tools
		if m.activeAnim {
			m.anim.Tick()
			m.animFrame++
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
		// Don't schedule next tick if any popup/dialog is showing (except history panel)
		if m.showHelpDialog || m.confirmDialog.open ||
			m.taskCompleteDialog.open || m.confirmQuitDialog.open || m.confirmCancelDialog.open {
			return m, nil
		}
		// Continue ticking so that the animation resumes immediately
		// when activeAnim becomes true — never let the tick die.
		return m, tickCmd()

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		if m.historyPanel != nil && m.historyPanel.active {
			m.historyPanel.SetSize(msg.Width, msg.Height)
		}
		m.input.SetWidth(m.computeFieldWidth())
		m.resizeViewport()
		m.invalidateRenderedCache()
		m.buildViewportContent()
		return m, nil

	case historyLoadedMsg:
		if m.historyPanel == nil || !m.historyPanel.active {
			return m, nil
		}
		if msg.err != nil {
			m.errMsg = fmt.Sprintf("Failed to load history: %v", msg.err)
			m.historyPanel.Close()
			m.historyPanel = nil
			return m, nil
		}
		// 转换为 historyItem 列表
		items := make([]list.Item, len(msg.items))
		for i, it := range msg.items {
			items[i] = historyItem(it)
		}
		if msg.offset == 0 {
			m.historyPanel.list.SetItems(items)
		} else {
			currentItems := m.historyPanel.list.Items()
			newItems := append(currentItems, items...)
			m.historyPanel.list.SetItems(newItems)
		}
		m.historyPanel.offset = msg.offset + len(msg.items)
		m.historyPanel.hasMore = m.historyPanel.offset < msg.total
		m.historyPanel.loading = false
		return m, nil

	case historyDeletedMsg:
		if m.historyPanel == nil || !m.historyPanel.active {
			return m, nil
		}
		m.historyPanel.loading = false
		if msg.err != nil {
			m.errMsg = fmt.Sprintf("Failed to delete: %v", msg.err)
			return m, nil
		}
		// 重新加载面板数据
		m.historyPanel.offset = 0
		m.historyPanel.hasMore = true
		m.historyPanel.loading = true
		m.historyPanel.list.SetItems([]list.Item{})
		m.historyPanel.list.Select(0)
		return m, LoadHistoryCmd(m.dataManager, m.historyPanel.ctx, 0, m.historyPanel.pageSize)

	case continueWithTaskMsg:
		// 继续对话处理
		mem, err := m.dataManager.LoadTaskMemory(msg.taskID)
		if err != nil {
			m.errMsg = fmt.Sprintf("Failed to load conversation: %v", err)
			return m, nil
		}
		ctx, cancel := context.WithCancel(context.Background())
		task := &http.Task{
			ID:         uuid.New().String(),
			Status:     http.TaskStatusRunning,
			ProjectDir: m.projectDir,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			Memory:     mem,
			Context:    ctx,
			CancelFunc: cancel,
		}
		m.taskManager.AddTask(task)
		m.currentTask = task
		m.taskRunning = false
		if m.historyPanel != nil {
			m.historyPanel.Close()
			m.historyPanel = nil
		}
		m.logEntries = append(m.logEntries, logEntry{
			timestamp: time.Now(),
			eventType: "status",
			content:   fmt.Sprintf("Loaded conversation: %s", msg.taskID),
		})
		m.buildViewportContent()
		return m, nil

	case publisherReadyMsg:
		m.publisher = msg.publisher
		return m, nil

	case tea.KeyMsg:

		// Confirmation dialog key handling — takes priority over everything
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

		// History panel key handling
		if m.historyPanel != nil && m.historyPanel.active {
			if m.historyPanel.loading {
				return m, nil
			}

			switch msg.String() {
			case "esc", "q":
				m.historyPanel.Close()
				m.historyPanel = nil
				return m, nil

			case "enter":
				if selected, ok := m.historyPanel.SelectedItem(); ok {
					m.historyPanel.Close()
					m.historyPanel = nil
					return m, ContinueConversationCmd(selected.TaskID)
				}
				return m, nil

			case "d", "D":
				if selected, ok := m.historyPanel.SelectedItem(); ok {
					m.historyPanel.loading = true
					return m, DeleteHistoryCmd(m.dataManager, m.historyPanel.ctx, selected.TaskID)
				}
				return m, nil

			default:
				// 其他按键交给 list 组件处理（方向键等）
				var listCmd tea.Cmd
				m.historyPanel.list, listCmd = m.historyPanel.list.Update(msg)

				// 自动加载更多
				hp := m.historyPanel
				if !hp.loading && hp.hasMore && hp.list.Index() >= len(hp.list.Items())-3 {
					hp.loading = true
					return m, tea.Batch(listCmd, LoadHistoryCmd(m.dataManager, hp.ctx, hp.offset, hp.pageSize))
				}
				return m, listCmd
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
					return m, nil
				default:
					// Invalid combo: discard lastKey and fall through to process key normally
				}
			}

			// Help dialog is open: let action keys (i/esc/enter/?/ctrl+c) pass through,
			// dismiss on any other key without processing it.
			if m.showHelpDialog && key != "i" && key != "ctrl+e" && key != "enter" && key != "?" && key != "ctrl+c" {
				m.showHelpDialog = false
				return m, nil
			}

			switch key {
			case "ctrl+c":
				m.confirmQuitDialog.open = true
				return m, nil

			case "esc":
				if m.showHelpDialog {
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
					m.confirmCancelDialog.open = true
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
					m.showHelpDialog = !m.showHelpDialog
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
			m.confirmQuitDialog.open = true
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
				m.confirmCancelDialog.open = true
			}
			return m, nil

		case "ctrl+e":
			// Enter command mode
			m.taskHistoryIdx = -1
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
			m.taskHistoryIdx = -1
			if m.currentTask != nil {
				return m, m.submitFollowUp(taskDesc)
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
				// Built-in command: history
				if skillName == "history" {
					m.skillAutoComplete = false
					m.skillSuggestions = nil
					m.skillSuggestionIdx = 0
					m.input.Reset()
					m.historyPanel = NewHistoryPanel(m.termWidth, m.termHeight)
					return m, LoadHistoryCmd(m.dataManager, m.historyPanel.ctx, 0, m.historyPanel.pageSize)
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
			// Cycle through task history when input is empty
			if strings.TrimSpace(m.input.Value()) == "" {
				m.handleTaskHistoryCycle(msg.String())
				return m, nil
			}
			// Input has content: pass to textarea for line navigation
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(msg)
			return m, cmd

		default:
			// Reset history cursor when user starts typing
			if len(msg.Key().Text) > 0 {
				m.taskHistoryIdx = -1
			}
			// Only update input — viewport scrolling keys (ctrl+f, ctrl+b)
			// are handled in dedicated case branches above.
			var inputCmd tea.Cmd
			m.input, inputCmd = m.input.Update(msg)
			m.updateSkillAutocomplete()
			return m, inputCmd
		}

	case taskEventMsg:
		// Don't process task events while any popup/dialog is showing.
		// The global guard at the top of Update() already blocks these messages,
		// but keep this as a defensive double-check.
		if m.showHelpDialog || m.confirmDialog.open ||
			m.taskCompleteDialog.open || m.confirmQuitDialog.open || m.confirmCancelDialog.open || (m.historyPanel != nil && m.historyPanel.active) {
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
			// Show error dialog
			m.taskCompleteDialog = taskCompleteDialog{
				open:    true,
				message: "❌ Task Failed\n\n" + msg.err.Error(),
			}
		} else {
			// Show success dialog
			m.taskCompleteDialog = taskCompleteDialog{
				open:    true,
				message: "Task Completed\n\nAll tasks have been finished.",
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
				newVal := currentVal[:lastAt+1] + msg.path + currentVal[lastAt+1:]
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

	// Add built-in commands first (always available)
	builtinCommands := []string{"history"}
	for _, cmd := range builtinCommands {
		if strings.HasPrefix(strings.ToLower(cmd), queryLower) {
			matches = append(matches, cmd)
		}
	}

	// Add skills from registry (if available)
	if m.assistant.SkillRegistry != nil {
		allSkills := m.assistant.SkillRegistry.List()
		for _, name := range allSkills {
			if strings.HasPrefix(strings.ToLower(name), queryLower) {
				matches = append(matches, name)
			}
		}
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
