package tui

import (
	"context"
	"fmt"
	"strings"

	"codeactor/internal/datamanager"
	"codeactor/internal/http"
	"codeactor/internal/memory"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ── History message types ──

// historyItemsMsg is sent when async history list loading completes.
type historyItemsMsg struct {
	items []datamanager.TaskHistoryItem
}

// memoryLoadedMsg is sent when async memory loading completes.
type memoryLoadedMsg struct {
	taskID string
	memory *memory.ConversationMemory
}

// historyErrMsg is sent when history/memory loading fails.
type historyErrMsg struct {
	err error
}

// ── Constants ──

const (
	defaultPageSize = 20 // 每页固定20条
)

// ── History Update ──

// historyUpdate processes all messages in history mode.
func historyUpdate(msg tea.Msg, m *model) (*model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return historyHandleKey(msg, m)

	case historyItemsMsg:
		m.historyItems = msg.items
		m.historyPage = 0
		m.historyCursor = 0
		m.historyLoading = false
		if len(msg.items) == 0 {
			m.infoMsg = "No conversation history found"
		}
		return m, nil

	case memoryLoadedMsg:
		m.historyLoading = false
		restoreSession(m, msg.memory, msg.taskID)
		exitHistoryMode(m)
		return m, nil

	case historyErrMsg:
		m.infoMsg = fmt.Sprintf("History error: %v", msg.err)
		m.historyLoading = false
		return m, nil

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		return m, nil

	default:
		return m, nil
	}
}

// totalNumPages calculates the total number of pages for the current items.
func (m *model) totalNumPages() int {
	if len(m.historyItems) == 0 {
		return 1
	}
	pageSize := m.historyPageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	pages := len(m.historyItems) / pageSize
	if len(m.historyItems)%pageSize > 0 {
		pages++
	}
	return pages
}

// visibleRange returns the start and end indices (exclusive) of visible items on the current page.
func (m *model) visibleRange() (startIdx, endIdx int) {
	total := len(m.historyItems)
	pageSize := m.historyPageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	page := m.historyPage
	totalPages := m.totalNumPages()

	// Clamp page
	if page < 0 {
		page = 0
	}
	if page >= totalPages {
		page = totalPages - 1
		if page < 0 {
			page = 0
		}
	}
	m.historyPage = page

	startIdx = page * pageSize
	if startIdx >= total {
		startIdx = total - 1
		if startIdx < 0 {
			startIdx = 0
		}
	}
	endIdx = startIdx + pageSize
	if endIdx > total {
		endIdx = total
	}
	return
}

// absCursor returns the absolute cursor index into m.historyItems.
func (m *model) absCursor() int {
	startIdx, _ := m.visibleRange()
	return startIdx + m.historyCursor
}

// clampCursor ensures cursor is within valid range for the current page.
func (m *model) clampCursor() {
	_, endIdx := m.visibleRange()
	count := endIdx - (m.historyPage * m.historyPageSize)
	if count <= 0 {
		m.historyCursor = 0
		return
	}
	if m.historyCursor >= count {
		m.historyCursor = count - 1
	}
	if m.historyCursor < 0 {
		m.historyCursor = 0
	}
}

// historyHandleKey processes keyboard input in history mode.
func historyHandleKey(key tea.KeyMsg, m *model) (*model, tea.Cmd) {
	k := key.String()

	// Exit history mode on escape or ctrl+c
	if k == "esc" || k == "ctrl+c" {
		exitHistoryMode(m)
		return m, nil
	}

	// Skip all other keys while loading (esc already handled above)
	if m.historyLoading {
		return m, nil
	}

	// Navigation
	switch k {
	case "j", "down":
		// Cursor down within current page (no auto-page)
		_, endIdx := m.visibleRange()
		visibleCount := endIdx - (m.historyPage * m.historyPageSize)
		if visibleCount > 0 && m.historyCursor < visibleCount-1 {
			m.historyCursor++
		}
		return m, nil

	case "k", "up":
		// Cursor up within current page (no auto-page)
		if m.historyCursor > 0 {
			m.historyCursor--
		}
		return m, nil

	case "n", "pagedown", "ctrl+f":
		// Next page
		totalPages := m.totalNumPages()
		if m.historyPage+1 < totalPages {
			m.historyPage++
			m.historyCursor = 0
		}
		return m, nil

	case "p", "pageup", "ctrl+b":
		// Previous page
		if m.historyPage > 0 {
			m.historyPage--
			m.historyCursor = 0
		}
		return m, nil

	case "g":
		// Check for double-press gg (fast key detection via lastKey)
		if m.lastKey == "g" {
			m.historyPage = 0
			m.historyCursor = 0
			m.lastKey = ""
			return m, nil
		}
		m.lastKey = k
		return m, nil

	case "G":
		// Last page
		totalPages := m.totalNumPages()
		if totalPages > 0 {
			m.historyPage = totalPages - 1
			m.historyCursor = 0
		}
		return m, nil

	case "enter":
		if len(m.historyItems) == 0 || m.historyLoading {
			return m, nil
		}
		// Load memory for the selected item using absolute cursor index
		absIdx := m.absCursor()
		if absIdx < 0 || absIdx >= len(m.historyItems) {
			return m, nil
		}
		item := m.historyItems[absIdx]
		m.historyLoading = true
		return m, loadMemoryCmd(m, item.TaskID)
	}

	return m, nil
}

// ── History Commands ──

// loadHistoryCmd asynchronously loads the history list.
func loadHistoryCmd(m *model) tea.Cmd {
	return func() tea.Msg {
		if m.dataManager == nil {
			return historyItemsMsg{}
		}
		items, err := m.dataManager.ListTaskHistoryFast(100)
		if err != nil {
			return historyErrMsg{err: err}
		}
		return historyItemsMsg{items: items}
	}
}

// loadMemoryCmd asynchronously loads the conversation memory for a task.
func loadMemoryCmd(m *model, taskID string) tea.Cmd {
	return func() tea.Msg {
		if m.dataManager == nil {
			return historyErrMsg{err: fmt.Errorf("data manager is not initialized")}
		}
		mem, err := m.dataManager.LoadTaskMemory(taskID)
		if err != nil {
			return historyErrMsg{err: err}
		}
		if mem == nil || len(mem.Messages) == 0 {
			return historyErrMsg{err: fmt.Errorf("no messages found in task %s", taskID)}
		}
		return memoryLoadedMsg{taskID: taskID, memory: mem}
	}
}

// ── Session Restore ──

// restoreSession restores a conversation from loaded memory into the current TUI session.
func restoreSession(m *model, mem *memory.ConversationMemory, taskID string) {
	// Guard against double-load (e.g., rapid double-press on history item)
	if m.historyLoading {
		return
	}
	m.historyLoading = true
	defer func() { m.historyLoading = false }()

	if mem == nil || len(mem.Messages) == 0 {
		m.infoMsg = "No messages in this session"
		return
	}

	// 1. Clear existing log entries
	m.logEntries = nil

	// 2. Convert each message to a logEntry
	for _, msg := range mem.Messages {
		entry := logEntry{
			timestamp: msg.Timestamp,
		}

		switch msg.Type {
		case memory.MessageTypeSystem:
			entry.eventType = "system"
			entry.from = "System"
			entry.content = msg.Content

		case memory.MessageTypeHuman:
			entry.eventType = "user_input"
			entry.from = "You"
			entry.content = msg.Content

		case memory.MessageTypeAssistant:
			entry.eventType = "ai_response"
			entry.from = "Assistant"
			entry.content = msg.Content

		case memory.MessageTypeTool:
			entry.eventType = "tool_result"
			entry.from = "Tool"
			entry.content = msg.Content

		default:
			entry.eventType = string(msg.Type)
			entry.from = "Unknown"
			entry.content = msg.Content
		}

		m.logEntries = append(m.logEntries, entry)
	}

	// 3. Create a new http.Task with the loaded memory
	// Extract title from first human message
	title := taskID
	for _, msg := range mem.Messages {
		if msg.Type == memory.MessageTypeHuman {
			r := []rune(msg.Content)
			if len(r) > 40 {
				title = string(r[:40]) + "…"
			} else {
				title = msg.Content
			}
			break
		}
	}

	// 4. Add task to task manager
	ctx, cancel := context.WithCancel(context.Background())
	m.taskManager.AddTask(&http.Task{
		ID:         taskID,
		Status:     "finished",
		Result:     fmt.Sprintf("Session restored: %d messages", len(mem.Messages)),
		ProjectDir: m.projectDir,
		Memory:     mem,
		Context:    ctx,
		CancelFunc: cancel,
	})

	// 5. Set as current task
	if task, ok := m.taskManager.GetTask(taskID); ok {
		m.currentTask = task
	}

	// 6. Rebuild viewport content
	m.buildViewportContent()

	// 7. Set info message
	m.infoMsg = fmt.Sprintf("Loaded session: %s", title)
}

// ── History Mode Entry/Exit ──

// enterHistoryMode enters history browsing mode, loading the list asynchronously.
func enterHistoryMode(m *model) tea.Cmd {
	m.historyMode = true
	m.historyItems = nil
	m.historyCursor = 0
	m.historyPage = 0
	m.historyPageSize = defaultPageSize
	m.historyLoading = true
	m.lastKey = ""
	return loadHistoryCmd(m)
}

// exitHistoryMode exits history mode, resetting all history-related fields.
func exitHistoryMode(m *model) {
	m.historyMode = false
	m.historyItems = nil
	m.historyCursor = 0
	m.historyPage = 0
	m.historyPageSize = 0
	m.historyLoading = false
	m.lastKey = ""
}

// ── History View Rendering ──

// renderHistoryView renders the fullscreen history browsing UI.
func renderHistoryView(m *model) tea.View {
	width := m.termWidth
	height := m.termHeight
	if width < 40 {
		width = 40
	}
	if height < 8 {
		height = 8
	}

	// ── Height calculation:
	//   Top border (1) + Title bar (1) + Content area (?) + Status bar (1) + Bottom border (1) = height
	//   => contentHeight = height - 4
	contentHeight := height - 4
	if contentHeight < 1 {
		contentHeight = 1
	}

	// Actual rendered item lines = min(pageSize, contentHeight)
	effectivePageSize := m.historyPageSize
	if effectivePageSize > contentHeight {
		effectivePageSize = contentHeight
	}

	var b strings.Builder

	// ── Top border ──
	topLeft := "┌" + strings.Repeat("─", width-2) + "┐"
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(topLeft))
	b.WriteString("\n")

	// ── Title bar ──
	titleBar := renderHistoryTitleBar(m, width)
	b.WriteString(titleBar)
	b.WriteString("\n")

	// ── Content area ──
	contentArea := renderHistoryContent(m, width, effectivePageSize)
	b.WriteString(contentArea)

	// ── Status bar ──
	statusBar := renderHistoryStatusBar(m, width)
	b.WriteString(statusBar)
	b.WriteString("\n")

	// ── Bottom border ──
	bottomLeft := "└" + strings.Repeat("─", width-2) + "┘"
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(bottomLeft))

	return tea.NewView(b.String())
}

// renderHistoryTitleBar renders the top title bar of the history view.
func renderHistoryTitleBar(m *model, width int) string {
	// Determine page info for title
	totalPages := m.totalNumPages()
	pageNum := m.historyPage + 1 // 1-based for display
	titleText := fmt.Sprintf(" History          Page %d/%d ", pageNum, totalPages)
	rightText := "esc: back  enter: load"

	// Calculate available width for right text
	contentWidth := width - 2 // account for border
	leftWidth := len(titleText)
	rightWidth := len(rightText)
	paddingNeeded := contentWidth - leftWidth - rightWidth
	if paddingNeeded < 1 {
		// Truncate right text to fit
		maxRight := contentWidth - leftWidth - 1
		if maxRight > 3 {
			rightText = rightText[:maxRight] + "…"
		} else {
			rightText = "..."
		}
	} else {
		rightText = strings.Repeat(" ", paddingNeeded) + rightText
	}

	combined := titleText + rightText

	style := lipgloss.NewStyle().
		Background(lipgloss.Color("214")).
		Foreground(lipgloss.Color("0")).
		Bold(true).
		Width(width)

	return style.Render(combined)
}

// renderHistoryContent renders the page-based content area.
// It renders exactly `height` lines: visible items + empty fill lines.
func renderHistoryContent(m *model, width, height int) string {
	bgStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("234")).
		Foreground(lipgloss.Color("252")).
		Width(width)

	if len(m.historyItems) == 0 {
		if m.historyLoading {
			centerText := "Loading history…"
			padding := strings.Repeat(" ", (width-len(centerText))/2)
			lines := make([]string, height)
			lines[0] = bgStyle.Render(padding + centerText)
			for i := 1; i < height; i++ {
				lines[i] = bgStyle.Render("")
			}
			return strings.Join(lines, "\n")
		}
		// Empty state
		centerText := "No history yet. Start a conversation with ctrl+s."
		padding := strings.Repeat(" ", (width-len(centerText))/2)
		lines := make([]string, height)
		lines[0] = bgStyle.Render(padding + centerText)
		for i := 1; i < height; i++ {
			lines[i] = bgStyle.Render("")
		}
		return strings.Join(lines, "\n")
	}

	// Calculate visible range
	startIdx, endIdx := m.visibleRange()
	visibleItems := m.historyItems[startIdx:endIdx]

	// Clamp cursor for this page
	m.clampCursor()

	// Build lines
	lines := make([]string, height)
	for i := 0; i < height; i++ {
		if i < len(visibleItems) {
			// Render actual item
			isSelected := (i == m.historyCursor)
			lines[i] = renderHistoryItem(m, visibleItems[i], isSelected, width)
		} else {
			// Empty fill line
			lines[i] = bgStyle.Render("")
		}
	}

	return strings.Join(lines, "\n")
}

// renderHistoryItem renders a single history item line.
func renderHistoryItem(m *model, item datamanager.TaskHistoryItem, selected bool, width int) string {
	// Date for left alignment
	dateStr := item.CreatedAt.Format("01-02 15:04")

	// Fixed left part: indicator (2) + date (11) + space (1) = 14
	// Title takes remaining width
	maxTitleWidth := width - 14
	if maxTitleWidth < 10 {
		maxTitleWidth = 10
	}

	// Truncate title to fit (rune-safe), replace newlines with spaces
	title := item.Title
	title = strings.ReplaceAll(title, "\r\n", " ")
	title = strings.ReplaceAll(title, "\n", " ")
	title = strings.ReplaceAll(title, "\r", " ")
	if runeCount := len([]rune(title)); runeCount > maxTitleWidth {
		tr := []rune(title)
		title = string(tr[:maxTitleWidth]) + "…"
	}

	if selected {
		// Selected: blue background, black text, bold
		indicator := lipgloss.NewStyle().
			Background(lipgloss.Color("39")).
			Foreground(lipgloss.Color("0")).
			Bold(true).
			Render("● ")

		dateStyle := lipgloss.NewStyle().
			Background(lipgloss.Color("39")).
			Foreground(lipgloss.Color("0")).
			Bold(true)

		titleStyle := lipgloss.NewStyle().
			Background(lipgloss.Color("39")).
			Foreground(lipgloss.Color("0")).
			Bold(true)

		left := indicator + dateStyle.Render(dateStr) + " " + titleStyle.Render(title)

		// Pad to fill width (use lipgloss.Width for display width, not byte length)
		displayWidth := lipgloss.Width(left)
		if displayWidth < width {
			left += strings.Repeat(" ", width-displayWidth)
		}

		lineStyle := lipgloss.NewStyle().
			Background(lipgloss.Color("39")).
			Foreground(lipgloss.Color("0")).
			Bold(true).
			Width(width)

		return lineStyle.Render(left)
	}

	// Non-selected: gray text, double-space indicator
	indicator := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Render("  ")

	dateStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("245"))

	titleStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252"))

	left := indicator + dateStyle.Render(dateStr) + " " + titleStyle.Render(title)

	// Pad to fill width
	displayWidth := lipgloss.Width(left)
	if displayWidth < width {
		left += strings.Repeat(" ", width-displayWidth)
	}

	return left
}

// renderHistoryStatusBar renders the bottom pagination status bar.
func renderHistoryStatusBar(m *model, width int) string {
	var statusText string
	var commandHint string

	if len(m.historyItems) == 0 {
		if m.historyLoading {
			statusText = "Loading…"
		} else {
			statusText = "No history"
		}
		commandHint = ""
	} else {
		startIdx, endIdx := m.visibleRange()
		pageNum := m.historyPage + 1
		totalPages := m.totalNumPages()
		statusText = fmt.Sprintf("Page %d/%d · %d-%d of %d",
			pageNum, totalPages,
			startIdx+1, endIdx,
			len(m.historyItems))

		commandHint = "n:next  p:prev  j/k:select"
	}

	separator := "── "
	suffix := " ──"
	contentWidth := width - len(separator) - len(suffix)

	var line string
	if commandHint != "" {
		// "statusText ── commandHint"
		// Put status on left, commands on right
		statusWidth := len(statusText)
		cmdWidth := len(commandHint)
		if contentWidth > statusWidth+cmdWidth+4 {
			padding := contentWidth - statusWidth - cmdWidth - 4
			line = separator + statusText + strings.Repeat(" ", padding) + "  " + commandHint + suffix
		} else {
			line = separator + statusText + suffix
		}
	} else {
		line = separator + statusText + strings.Repeat(" ", contentWidth-len(statusText)) + suffix
	}

	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Background(lipgloss.Color("234")).
		Width(width)

	return style.Render(line)
}
