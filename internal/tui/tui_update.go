package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"codeactor/internal/datamanager"
	"codeactor/internal/http"

	"github.com/google/uuid"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *model) processCommand(cmd string) {
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

func (m *model) openHistoryPanel() {
	dm, err := datamanager.NewDataManager()
	if err == nil {
		items, err2 := dm.ListTaskHistory(50)
		if err2 == nil {
			m.historyItems = items
			m.filteredItems = items
		}
	}
	m.historyIndex = 0
	m.historyScrollStart = 0
	m.historyFilter = ""
	m.historyConfirmDelete = false
	m.showHistoryPanel = true
}

func (m *model) closeHistoryPanel() {
	m.showHistoryPanel = false
	m.historyFilter = ""
	m.historyConfirmDelete = false
}

func (m *model) applyHistoryFilter() {
	query := strings.TrimSpace(m.historyFilter)
	if query == "" {
		m.filteredItems = m.historyItems
		m.historyIndex = 0
		m.historyScrollStart = 0
		return
	}
	qLower := strings.ToLower(query)
	filtered := make([]datamanager.TaskHistoryItem, 0, len(m.historyItems))
	for _, it := range m.historyItems {
		txt := strings.ToLower(it.Title + " " + it.TaskID)
		if strings.Contains(txt, qLower) {
			filtered = append(filtered, it)
		}
	}
	m.filteredItems = filtered
	if m.historyIndex >= len(m.filteredItems) {
		m.historyIndex = 0
	}
	m.historyScrollStart = 0
}

func (m *model) continueConversation() tea.Cmd {
	if len(m.filteredItems) == 0 {
		return nil
	}
	if m.historyIndex < 0 {
		m.historyIndex = 0
	}
	if m.historyIndex >= len(m.filteredItems) {
		m.historyIndex = len(m.filteredItems) - 1
	}
	selected := m.filteredItems[m.historyIndex]

	mem, err := m.dataManager.LoadTaskMemory(selected.TaskID)
	if err != nil {
		m.errMsg = fmt.Sprintf("Failed to load conversation: %v", err)
		return nil
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

	m.showHistoryPanel = false
	m.historyFilter = ""
	m.historyConfirmDelete = false

	m.logEntries = append(m.logEntries, logEntry{
		timestamp: time.Now(),
		eventType: "status",
		content:   fmt.Sprintf("Loaded conversation: %s (%d messages)", selected.Title, selected.MessageCount),
	})
	m.buildViewportContent()

	return nil
}

func (m *model) deleteHistoryItem() {
	if len(m.filteredItems) == 0 {
		return
	}
	selected := m.filteredItems[m.historyIndex]

	if err := m.dataManager.DeleteTaskMemory(selected.TaskID); err != nil {
		m.errMsg = fmt.Sprintf("Failed to delete: %v", err)
		return
	}

	// Remove from historyItems
	for i, it := range m.historyItems {
		if it.TaskID == selected.TaskID {
			m.historyItems = append(m.historyItems[:i], m.historyItems[i+1:]...)
			break
		}
	}
	// Remove from filteredItems
	for i, it := range m.filteredItems {
		if it.TaskID == selected.TaskID {
			m.filteredItems = append(m.filteredItems[:i], m.filteredItems[i+1:]...)
			break
		}
	}

	if m.historyIndex >= len(m.filteredItems) {
		m.historyIndex = len(m.filteredItems) - 1
	}
	if m.historyIndex < 0 {
		m.historyIndex = 0
	}

	m.historyConfirmDelete = false
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		// Always continue ticking so that the animation resumes immediately
		// when activeAnim becomes true — never let the tick die.
		return m, tickCmd()

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		m.input.SetWidth(m.computeFieldWidth())
		m.resizeViewport()
		m.invalidateRenderedCache()
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
			case "right", "tab":
				m.confirmDialog.selectedOption = (m.confirmDialog.selectedOption + 1) % 3
				return m, nil
			case "left":
				m.confirmDialog.selectedOption = (m.confirmDialog.selectedOption + 2) % 3
				return m, nil
			case "enter":
				switch m.confirmDialog.selectedOption {
				case 0:
					m.respondToAuth("allow")
				case 1:
					m.respondToAuth("allow_session")
				case 2:
					m.respondToAuth("deny")
				}
				return m, nil
			case "a", "A":
				m.respondToAuth("allow")
				return m, nil
			case "s", "S":
				m.respondToAuth("allow_session")
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
		if m.showHistoryPanel {
			// Delete confirmation mode
			if m.historyConfirmDelete {
				switch msg.String() {
				case "y", "Y":
					m.deleteHistoryItem()
					return m, nil
				default:
					m.historyConfirmDelete = false
					return m, nil
				}
			}

			switch msg.String() {
			case "esc", "ctrl+h":
				m.closeHistoryPanel()
				return m, nil

			case "enter":
				return m, m.continueConversation()

			case "up", "ctrl+k":
				if m.historyIndex > 0 {
					m.historyIndex--
				}
				return m, nil

			case "down", "ctrl+j":
				if m.historyIndex < len(m.filteredItems)-1 {
					m.historyIndex++
				}
				return m, nil

			case "ctrl+f":
				pageSize := m.termHeight - 8
				if pageSize < 1 {
					pageSize = 1
				}
				m.historyIndex += pageSize
				if m.historyIndex >= len(m.filteredItems) {
					m.historyIndex = len(m.filteredItems) - 1
				}
				return m, nil

			case "ctrl+b":
				pageSize := m.termHeight - 8
				if pageSize < 1 {
					pageSize = 1
				}
				m.historyIndex -= pageSize
				if m.historyIndex < 0 {
					m.historyIndex = 0
				}
				return m, nil

			case "ctrl+d":
				if len(m.filteredItems) > 0 {
					m.historyConfirmDelete = true
				}
				return m, nil

			case "backspace":
				if len(m.historyFilter) > 0 {
					m.historyFilter = m.historyFilter[:len(m.historyFilter)-1]
					m.applyHistoryFilter()
				}
				return m, nil

			case "ctrl+u":
				m.historyFilter = ""
				m.applyHistoryFilter()
				return m, nil

			default:
				// Printable characters → filter
				if len(msg.Runes) > 0 {
					m.historyFilter += string(msg.Runes)
					m.applyHistoryFilter()
				}
				return m, nil
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
				case "ZZ":
					m.confirmQuitDialog.open = true
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
					m.processCommand(m.commandBuffer)
					m.commandBuffer = ""
				} else {
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
				m.viewport.LineDown(1)
				return m, nil

			case "k", "up":
				m.viewport.LineUp(1)
				return m, nil

			case "ctrl+d":
				m.viewport.HalfPageDown()
				return m, nil

			case "ctrl+u":
				m.viewport.HalfPageUp()
				return m, nil

			case "G":
				// Vim: Shift+G → go to bottom
				m.viewport.GotoBottom()
				return m, nil

			// ── Multi-key prefix: g (for gg), Z (for ZZ) ──
			case "g", "Z":
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

			case "ctrl+h":
				m.openHistoryPanel()
				return m, nil

			default:
				// Append printable characters to command buffer (hidden input)
				if len(msg.Runes) > 0 {
					m.commandBuffer += string(msg.Runes)
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

		case "ctrl+h":
			m.openHistoryPanel()
			return m, nil

		case "ctrl+f":
			m.viewport.PageDown()
			return m, nil

		case "ctrl+b":
			m.viewport.PageUp()
			return m, nil

		case "up", "down":
			// Cycle through task history when input is empty
			if strings.TrimSpace(m.input.Value()) == "" {
				m.handleTaskHistoryCycle(msg.String())
				return m, nil
			}
			// Non-empty: pass to viewport for scrolling
			var vpCmd tea.Cmd
			m.viewport, vpCmd = m.viewport.Update(msg)
			return m, vpCmd

		default:
			// Reset history cursor when user starts typing
			if len(msg.Runes) > 0 {
				m.taskHistoryIdx = -1
			}
			// Pass to viewport for scrolling (up/down/pgup/pgdown)
			var vpCmd tea.Cmd
			m.viewport, vpCmd = m.viewport.Update(msg)
			// Also pass to input for text editing
			var inputCmd tea.Cmd
			m.input, inputCmd = m.input.Update(msg)
			return m, tea.Batch(vpCmd, inputCmd)
		}

	case taskEventMsg:
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
					m.buildViewportContent()
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
					m.buildViewportContent()
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

	// Handle text input
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}
