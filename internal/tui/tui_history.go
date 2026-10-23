package tui

import (
	"fmt"
	"strings"

	"yagent/internal/datamanager"
	"yagent/internal/memory"
	"yagent/internal/tui/components"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// historyStyles 预计算的样式集合，避免每帧重建
type historyStyles struct {
	badgeBgColor   string         // 选中行背景色 (lipgloss.Color string)
	indicatorStyle lipgloss.Style // 选中指示器 "● "
	dateStyle      lipgloss.Style // 日期样式
	titleStyle     lipgloss.Style // 标题样式
	spacerStyle    lipgloss.Style // 空白填充
	normalStyle    lipgloss.Style // 普通状态
}

func (m *model) initHistoryStyles() *historyStyles {
	bgColor := lipgloss.Color("39") // 蓝色
	fgColor := lipgloss.Color("0")  // 黑色
	dimFg := lipgloss.Color("245")
	normalFg := lipgloss.Color("252")

	return &historyStyles{
		badgeBgColor: "39",
		indicatorStyle: lipgloss.NewStyle().
			Background(bgColor).
			Foreground(fgColor).
			Bold(true),
		dateStyle: lipgloss.NewStyle().
			Background(bgColor).
			Foreground(fgColor).
			Bold(true),
		titleStyle: lipgloss.NewStyle().
			Background(bgColor).
			Foreground(fgColor).
			Bold(true),
		spacerStyle: lipgloss.NewStyle().
			Foreground(dimFg),
		normalStyle: lipgloss.NewStyle().
			Foreground(normalFg),
	}
}

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

const (
	defaultPageSize = 20 // 每页固定20条
)

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
			m.setInfoMsg("No conversation history found")
		}
		return m, nil

	case memoryLoadedMsg:
		m.historyLoading = false
		restoreSession(m, msg.memory, msg.taskID)
		exitHistoryMode(m)
		return m, nil

	case historyErrMsg:
		m.setInfoMsg(fmt.Sprintf("History error: %v", msg.err))
		m.historyLoading = false
		return m, nil

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		return m, nil

	case taskEventMsg:
		// 浏览历史时任务仍在跑：事件必须照常消费并续链，否则退出后主界面停在旧内容上（listenForEvents 没有重放队列）。
		_, cmd := m.handleTaskEventMsg(msg)
		return m, cmd

	case taskCompleteMsg:
		_, cmd := m.handleTaskCompleteMsg(msg)
		return m, cmd

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

	if k == "esc" || k == "ctrl+c" {
		exitHistoryMode(m)
		return m, nil
	}

	if m.historyLoading {
		return m, nil
	}

	switch k {
	case "j", "down":
		_, endIdx := m.visibleRange()
		visibleCount := endIdx - (m.historyPage * m.historyPageSize)
		if visibleCount > 0 && m.historyCursor < visibleCount-1 {
			m.historyCursor++
		}
		return m, nil

	case "k", "up":
		if m.historyCursor > 0 {
			m.historyCursor--
		}
		return m, nil

	case "n", "pagedown", "ctrl+f":
		totalPages := m.totalNumPages()
		if m.historyPage+1 < totalPages {
			m.historyPage++
			m.historyCursor = 0
		}
		return m, nil

	case "p", "pageup", "ctrl+b":
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
		absIdx := m.absCursor()
		if absIdx < 0 || absIdx >= len(m.historyItems) {
			return m, nil
		}
		item := m.historyItems[absIdx]
		m.historyLoading = true
		return m, loadMemoryCmd(m, item.TaskID)

	case "d", "delete", "ctrl+d":
		if len(m.historyItems) == 0 || m.historyLoading {
			return m, nil
		}
		absIdx := m.absCursor()
		if absIdx < 0 || absIdx >= len(m.historyItems) {
			return m, nil
		}
		item := m.historyItems[absIdx]
		detail := item.Title
		if len(detail) > 30 {
			detail = detail[:30] + "..."
		}
		d := components.NewQuitConfirmDialogForDelete(components.Language(m.currentLang), detail)
		d.SetBounds(m.termWidth, m.termHeight)
		m.dialogStack.Push(d)
		m.pendingDeleteTaskID = item.TaskID
		return m, nil
	}

	return m, nil
}

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
		if mem == nil || mem.Size() == 0 {
			return historyErrMsg{err: fmt.Errorf("no messages found in task %s", taskID)}
		}
		return memoryLoadedMsg{taskID: taskID, memory: mem}
	}
}

// memory→TUI 的落点在 applyRestoredMemory（tui_resume.go，与 :resume rollout 路径共用）。
func restoreSession(m *model, mem *memory.ConversationMemory, taskID string) {
	// Guard against double-load (e.g., rapid double-press on history item)
	if m.historyLoading {
		return
	}
	m.historyLoading = true
	defer func() { m.historyLoading = false }()

	if mem == nil || mem.Size() == 0 {
		m.setInfoMsg("No messages in this session")
		return
	}

	m.setInfoMsg(applyRestoredMemory(m, mem, taskID, m.projectDir, "Loaded"))
	// history 加载只有 taskID 可用（tasks/*.jsonl 不记 board_id）：改动前的旧会话（board 就是 taskID）能续上便签，改动后的新会话只能靠 /resume（rollout 里写了 board_id）。
	m.adoptRestoredBoard("", taskID)
}

// enterHistoryMode enters history browsing mode, loading the list asynchronously.
func enterHistoryMode(m *model) tea.Cmd {
	m.historyMode = true
	m.historyItems = nil
	m.historyCursor = 0
	m.historyPage = 0
	m.historyPageSize = defaultPageSize
	m.historyLoading = true
	m.lastKey = ""

	m.historyStyles = m.initHistoryStyles()

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

	// 顶边框(1) + 标题栏(1) + 内容区 + 状态栏(1) + 底边框(1) = height，故内容区 = height - 4
	contentHeight := height - 4
	if contentHeight < 1 {
		contentHeight = 1
	}

	effectivePageSize := m.historyPageSize
	if effectivePageSize > contentHeight {
		effectivePageSize = contentHeight
	}

	var b strings.Builder

	topLeft := "┌" + strings.Repeat("─", width-2) + "┐"
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(topLeft))
	b.WriteString("\n")

	titleBar := renderHistoryTitleBar(m, width)
	b.WriteString(titleBar)
	b.WriteString("\n")

	contentArea := renderHistoryContent(m, width, effectivePageSize)
	b.WriteString(contentArea)

	statusBar := renderHistoryStatusBar(m, width)
	b.WriteString(statusBar)
	b.WriteString("\n")

	bottomLeft := "└" + strings.Repeat("─", width-2) + "┘"
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(bottomLeft))

	// AltScreen 必须显式为 true：渲染器看到它从 true 翻转就会退出备用屏（cursed_renderer.go:314），进 history 会把用户甩回主屏缓冲区，多出来的行滚进真实终端历史。
	return tea.View{AltScreen: true, Content: b.String()}
}

// renderHistoryTitleBar renders the top title bar of the history view.
func renderHistoryTitleBar(m *model, width int) string {
	totalPages := m.totalNumPages()
	pageNum := m.historyPage + 1 // 1-based for display
	titleText := fmt.Sprintf(" History          Page %d/%d ", pageNum, totalPages)
	rightText := "esc: back  enter: load  d: delete"

	contentWidth := width - 2 // account for border
	leftWidth := len(titleText)
	rightWidth := len(rightText)
	paddingNeeded := contentWidth - leftWidth - rightWidth
	if paddingNeeded < 1 {
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

// renderHistoryContent renders exactly `height` lines: visible items plus empty fill lines.
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
		centerText := "No history yet. Start a conversation with alt+s."
		padding := strings.Repeat(" ", (width-len(centerText))/2)
		lines := make([]string, height)
		lines[0] = bgStyle.Render(padding + centerText)
		for i := 1; i < height; i++ {
			lines[i] = bgStyle.Render("")
		}
		return strings.Join(lines, "\n")
	}

	startIdx, endIdx := m.visibleRange()
	visibleItems := m.historyItems[startIdx:endIdx]

	m.clampCursor()

	lines := make([]string, height)
	for i := 0; i < height; i++ {
		if i < len(visibleItems) {
			isSelected := (i == m.historyCursor)
			lines[i] = renderHistoryItem(m, visibleItems[i], isSelected, width)
		} else {
			lines[i] = bgStyle.Render("")
		}
	}

	return strings.Join(lines, "\n")
}

// renderHistoryItem 渲染单个历史条目（使用预计算样式，零分配模式）
func renderHistoryItem(m *model, item datamanager.TaskHistoryItem, selected bool, width int) string {
	dateStr := item.CreatedAt.Format("01-02 15:04")

	maxTitleWidth := width - 14
	if maxTitleWidth < 10 {
		maxTitleWidth = 10
	}

	title := cleanTitle(item.Title, maxTitleWidth)

	if selected {
		return renderSelectedItem(m, dateStr, title, width)
	}
	return renderNormalItem(m, dateStr, title, width)
}

// 一次遍历完成换行替换与截断，避免多次 ReplaceAll。
func cleanTitle(raw string, maxWidth int) string {
	if raw == "" {
		return ""
	}

	var b strings.Builder
	b.Grow(len(raw))

	runeCount := 0
	for _, r := range raw {
		if r == '\r' || r == '\n' {
			b.WriteByte(' ')
			continue
		}
		if runeCount >= maxWidth {
			continue
		}
		b.WriteRune(r)
		runeCount++
	}

	result := b.String()
	if runeCount < len([]rune(raw)) {
		result += "…"
	}
	return result
}

func renderSelectedItem(m *model, dateStr, title string, width int) string {
	s := m.historyStyles
	if s == nil {
		s = m.initHistoryStyles()
	}

	indicator := s.indicatorStyle.Render("● ")
	datePart := s.dateStyle.Render(dateStr)
	titlePart := s.titleStyle.Render(title)

	left := indicator + datePart + " " + titlePart

	displayWidth := lipgloss.Width(left)
	if displayWidth < width {
		left += strings.Repeat(" ", width-displayWidth)
	}

	return lipgloss.NewStyle().
		Background(lipgloss.Color(s.badgeBgColor)).
		Foreground(lipgloss.Color("0")).
		Bold(true).
		Width(width).
		Render(left)
}

func renderNormalItem(m *model, dateStr, title string, width int) string {
	s := m.historyStyles
	if s == nil {
		s = m.initHistoryStyles()
	}

	indicator := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Render("  ")
	datePart := s.normalStyle.Render(dateStr)
	titlePart := s.normalStyle.Render(title)

	datePart = lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Render(dateStr)
	titlePart = lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")).
		Render(title)

	left := indicator + datePart + " " + titlePart

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

		commandHint = "n:next  p:prev  j/k:select  d:delete"
	}

	separator := "── "
	suffix := " ──"
	contentWidth := width - len(separator) - len(suffix)

	var line string
	if commandHint != "" {
		// 状态在左、命令提示在右
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

// confirmDeleteHistoryEntryByID deletes a history entry by taskID; called once the DialogStack confirmation comes back.
func confirmDeleteHistoryEntryByID(m *model, taskID string) tea.Cmd {
	if taskID == "" || m.dataManager == nil {
		m.setInfoMsg("Cannot delete: data manager not available")
		return nil
	}

	err := m.dataManager.DeleteTaskMemory(taskID)
	if err != nil {
		m.setInfoMsg(langManager.GetText("DeleteFailed") + err.Error())
		return nil
	}

	absIdx := -1
	for i, item := range m.historyItems {
		if item.TaskID == taskID {
			absIdx = i
			break
		}
	}

	if absIdx >= 0 {
		m.historyItems = append(m.historyItems[:absIdx], m.historyItems[absIdx+1:]...)
	}

	if len(m.historyItems) == 0 {
		m.historyCursor = 0
		m.historyPage = 0
	} else {
		startIdx, _ := m.visibleRange()
		absCursor := startIdx + m.historyCursor
		if absCursor >= len(m.historyItems) {
			m.historyCursor--
			if m.historyCursor < 0 {
				m.historyCursor = 0
			}
		}
	}
	m.clampCursor()

	m.setInfoMsg(langManager.GetText("DeleteSuccess"))
	return nil
}
