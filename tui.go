package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"codeactor/internal/app"
	"codeactor/internal/datamanager"
	"codeactor/internal/http"
	"codeactor/internal/memory"
	"codeactor/pkg/messaging"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"
)

// Global Language Manager
var langManager *LanguageManager

// Global styles — Claude Code-like minimalist aesthetic
var (
	bannerPadStyle = lipgloss.NewStyle().Padding(0, 1)

	promptFocusedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	promptBlurredStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	welcomePanelStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("39")).Padding(1, 2)
	welcomeLeftStyle  = lipgloss.NewStyle().Width(38)
	welcomeTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
	welcomeSubStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	welcomeRightTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
	welcomeTipStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	welcomeDimStyle   = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("242"))

	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("167")).Bold(true)
	infoMsgStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	footerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	// Message log styles
	logTimeStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Faint(true)
	logAIResStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	logToolStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("228"))
	logResultStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	logStatusStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("36"))
	logErrorLogStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("167"))
	logSeparatorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("237"))
)

// logEntry represents a single message in the TUI log area.
type logEntry struct {
	timestamp time.Time
	eventType string
	from      string
	content   string
	toolName  string
	rendered  string // cached rendered output (glamour or plain), cleared on resize
}

// taskEventMsg carries a MessageEvent from the task execution goroutine to the tea program.
type taskEventMsg struct {
	event *messaging.MessageEvent
}

// taskCompleteMsg signals that a task has finished (or failed).
type taskCompleteMsg struct {
	taskID string
	result string
	err    error
}

// tuiEventConsumer routes MessageEvents to a Go channel consumed by the tea program.
type tuiEventConsumer struct {
	ch chan *messaging.MessageEvent
}

func (c *tuiEventConsumer) Consume(event *messaging.MessageEvent) error {
	select {
	case c.ch <- event:
	default:
		// Drop event if channel is full to avoid blocking the task
	}
	return nil
}

// TUI Model
type model struct {
	// External dependencies
	assistant   *app.CodingAssistant
	taskManager *http.TaskManager
	dataManager *datamanager.DataManager

	// Input
	input textarea.Model

	// Message log
	logEntries      []logEntry
	viewport        viewport.Model
	contentCache    *strings.Builder // incremental viewport content cache (pointer avoids copy panic)
	glamourRenderer *glamour.TermRenderer
	useDarkStyle    bool

	// Task execution state
	taskRunning bool
	currentTask *http.Task
	eventCh     chan *messaging.MessageEvent

	// Standard state
	termWidth   int
	termHeight  int
	quitting    bool
	errMsg      string
	infoMsg     string
	currentLang Language
	projectDir  string

	// History panel state
	showHistoryPanel     bool
	historyItems         []datamanager.TaskHistoryItem
	filteredItems        []datamanager.TaskHistoryItem
	historyIndex         int
	historyScrollStart   int // first visible item index (for stable scroll)
	historyFilter        string
	historyConfirmDelete bool
}

func initialModel(preloadedTaskContent string, ca *app.CodingAssistant, tm *http.TaskManager, dm *datamanager.DataManager, useDarkStyle bool) model {
	ti := textarea.New()
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	ti.Placeholder = langManager.GetText("TaskDescPlaceholder")
	ti.Focus()
	ti.CharLimit = 0
	ti.SetWidth(60)
	ti.SetHeight(2)
	ti.ShowLineNumbers = false

	// Text style for both focused and blurred states
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	ti.FocusedStyle.Text = textStyle
	ti.BlurredStyle.Text = textStyle

	// Dynamic prompt: "❯ " on first line, "  " on continuation lines
	ti.SetPromptFunc(2, func(line int) string {
		if line == 0 {
			return "❯ "
		}
		return "  "
	})

	if preloadedTaskContent != "" {
		ti.SetValue(preloadedTaskContent)
	}

	projectDir, _ := os.Getwd()

	// Create viewport for scrollable message area
	vp := viewport.New(80, 10)
	vp.Style = lipgloss.NewStyle().Padding(0, 1)

	// Create glamour markdown renderer with explicit style to avoid
	// terminal background-color queries leaking into input.
	glamourStyle := "dark"
	if !useDarkStyle {
		glamourStyle = "light"
	}
	glamourRenderer, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(glamourStyle),
		glamour.WithWordWrap(60),
	)
	if err != nil {
		// Fallback: glamourRenderer will be nil, and we'll use plain text
		glamourRenderer = nil
	}

	return model{
		assistant:       ca,
		taskManager:     tm,
		dataManager:     dm,
		input:           ti,
		projectDir:      projectDir,
		infoMsg:         langManager.GetText("InfoMessage"),
		currentLang:     langManager.currentLang,
		eventCh:         make(chan *messaging.MessageEvent, 1000),
		logEntries:      make([]logEntry, 0),
		viewport:        vp,
		contentCache:    &strings.Builder{},
		glamourRenderer: glamourRenderer,
		useDarkStyle:    useDarkStyle,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		listenForEvents(m.eventCh),
	)
}

func (m *model) toggleLanguage() {
	if m.currentLang == LangEnglish {
		langManager.SetLanguage(LangChinese)
		m.currentLang = LangChinese
	} else {
		langManager.SetLanguage(LangEnglish)
		m.currentLang = LangEnglish
	}
	m.input.Placeholder = langManager.GetText("TaskDescPlaceholder")
	m.infoMsg = langManager.GetText("InfoMessage")
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
	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		m.input.SetWidth(m.computeFieldWidth())
		m.resizeViewport()
		m.invalidateRenderedCache()
		m.buildViewportContent()
		return m, nil

	case tea.KeyMsg:
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

		switch msg.String() {
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit

		case "esc":
			// Cancel the currently running task
			if m.taskRunning && m.currentTask != nil && m.currentTask.CancelFunc != nil {
				m.currentTask.CancelFunc()
				m.logEntries = append(m.logEntries, logEntry{
					timestamp: time.Now(),
					eventType: "status",
					content:   "Task cancelled by user",
				})
				m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
			}
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

		default:
			// Pass to viewport for scrolling (up/down/pgup/pgdown)
			var vpCmd tea.Cmd
			m.viewport, vpCmd = m.viewport.Update(msg)
			// Also pass to input for text editing
			var inputCmd tea.Cmd
			m.input, inputCmd = m.input.Update(msg)
			return m, tea.Batch(vpCmd, inputCmd)
		}

	case taskEventMsg:
		entry := formatEventAsEntry(msg.event)
		m.logEntries = append(m.logEntries, entry)
		m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
		return m, listenForEvents(m.eventCh)

	case taskCompleteMsg:
		m.taskRunning = false
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			m.currentTask = nil
			m.logEntries = append(m.logEntries, logEntry{
				timestamp: time.Now(),
				eventType: "error",
				content:   msg.err.Error(),
			})
			m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
		}
		return m, nil
	}

	// Handle text input
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m model) View() string {
	if m.quitting {
		return ""
	}

	var b strings.Builder

	// Main content area: history panel or scrollable viewport
	if m.showHistoryPanel {
		b.WriteString(m.renderHistoryPanel())
	} else {
		b.WriteString(m.viewport.View())
	}

	// Separator
	sepWidth := m.termWidth
	if sepWidth < 40 {
		sepWidth = 40
	}
	b.WriteString(logSeparatorStyle.Render(strings.Repeat("─", sepWidth)))
	b.WriteString("\n")

	// Input line (textarea handles its own prompt via PromptFunc)
	m.input.SetWidth(m.computeFieldWidth())
	inputLine := m.input.View()

	// Build footer area
	var footer strings.Builder
	footer.WriteString(lipgloss.NewStyle().MarginLeft(2).Render(inputLine))
	footer.WriteString("\n")

	// Error message
	if m.errMsg != "" {
		footer.WriteString(lipgloss.NewStyle().MarginLeft(2).Render(errorStyle.Render("✖ " + m.errMsg)))
		footer.WriteString("\n")
	}

	// Status line: shortcuts + task indicator
	taskIndicator := ""
	if m.taskRunning {
		taskIndicator = logStatusStyle.Render(" ◷ Running...")
	}
	footer.WriteString("\n")
	enterLabel := "ctrl+s submit"
	if m.currentTask != nil && !m.taskRunning {
		enterLabel = "ctrl+s send"
	}
	statusLine := footerStyle.Render(enterLabel+" │ ctrl+l lang │ ctrl+h history │ esc cancel │ ctrl+c quit") + taskIndicator
	footer.WriteString(lipgloss.NewStyle().MarginLeft(2).Render(statusLine))

	b.WriteString(footer.String())

	return b.String()
}

func (m model) renderWelcomePanel() string {
	// Build left panel: logo + cwd
	var left strings.Builder
	left.WriteString(renderBanner())
	left.WriteString("\n\n")
	cwd := m.projectDir
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(cwd, home) {
		cwd = "~" + strings.TrimPrefix(cwd, home)
	}
	left.WriteString(welcomeSubStyle.Render(cwd))

	leftContent := welcomeLeftStyle.Render(left.String())

	// Build right panel: recent activity
	var right strings.Builder
	right.WriteString(welcomeDimStyle.Render("─── Recent activity"))
	right.WriteString("\n")
	right.WriteString(welcomeDimStyle.Render("  Use Ctrl+H to browse history"))

	// Compute responsive widths
	panelWidth := m.computeFieldWidth() + 4
	innerWidth := panelWidth - 4 // 2 border + 2 padding
	leftWidth := 36
	if innerWidth < 65 {
		// Narrow terminal: stack vertically
		boxInner := leftContent + "\n\n" + welcomeDimStyle.Render(strings.Repeat("─", leftWidth)) + "\n\n" + right.String()
		return welcomePanelStyle.Width(panelWidth).Render(boxInner)
	}
	rightWidth := innerWidth - leftWidth - 3 // 3 for " │ "
	if rightWidth < 20 {
		rightWidth = 20
	}

	separator := welcomeDimStyle.Render(" │ ")

	leftStyled := lipgloss.NewStyle().Width(leftWidth).Render(leftContent)
	rightStyled := lipgloss.NewStyle().Width(rightWidth).Render(right.String())

	inner := lipgloss.JoinHorizontal(lipgloss.Top, leftStyled, separator, rightStyled)
	return welcomePanelStyle.Width(panelWidth).Render(inner)
}

// resizeViewport recalculates viewport dimensions and recreates the glamour renderer.
func (m *model) resizeViewport() {
	footerHeight := 5
	if m.errMsg != "" {
		footerHeight++
	}
	vpHeight := m.termHeight - footerHeight
	if vpHeight < 3 {
		vpHeight = 3
	}
	m.viewport.Width = m.termWidth
	m.viewport.Height = vpHeight

	// Recreate glamour renderer with updated width
	if m.viewport.Width > 0 {
		frameSize := m.viewport.Style.GetHorizontalFrameSize()
		const glamourGutter = 4
		glamourWidth := m.viewport.Width - frameSize - glamourGutter
		if glamourWidth < 40 {
			glamourWidth = 40
		}
		glamourStyle := "dark"
		if !m.useDarkStyle {
			glamourStyle = "light"
		}
		renderer, err := glamour.NewTermRenderer(
			glamour.WithStandardStyle(glamourStyle),
			glamour.WithWordWrap(glamourWidth),
		)
		if err == nil {
			m.glamourRenderer = renderer
		}
	}
}


// invalidateRenderedCache clears cached rendered output on all log entries.
// Called on terminal resize since glamour rendering depends on viewport width.
func (m *model) invalidateRenderedCache() {
	for i := range m.logEntries {
		m.logEntries[i].rendered = ""
	}
}

// buildViewportContent rebuilds the full viewport content from scratch.
// Used for initial load, terminal resize, or conversation switch.
func (m *model) buildViewportContent() {
	m.contentCache.Reset()

	// Welcome panel as scrollable content — scrolls together with messages
	m.contentCache.WriteString(m.renderWelcomePanel())
	m.contentCache.WriteString("\n")

	for i := range m.logEntries {
		entry := &m.logEntries[i]
		m.renderEntryTo(entry, m.contentCache)
		m.contentCache.WriteString("\n")
	}

	m.viewport.SetContent(m.contentCache.String())
	m.viewport.GotoBottom()
}

// renderEntryTo renders a single log entry into the builder, caching the result
// in the entry for reuse. Uses glamour for ai_response, plain formatting otherwise.
func (m *model) renderEntryTo(entry *logEntry, b *strings.Builder) {
	// Use cached rendered content if available
	if entry.rendered != "" {
		b.WriteString(entry.rendered)
		return
	}

	// Capture the start position to cache the output
	start := b.Len()

	if entry.eventType == "ai_response" && m.glamourRenderer != nil {
		rendered, err := m.glamourRenderer.Render(entry.content)
		if err == nil {
			b.WriteString(rendered)
			entry.rendered = b.String()[start:]
			return
		}
	}
	// Fallback to simple text rendering
	formatted := formatLogEntry(*entry, m.viewport.Width)
	b.WriteString(formatted)
	entry.rendered = b.String()[start:]
}

// appendLogEntry renders a single new entry and appends it incrementally to the viewport.
// Uses scroll lock: only auto-scrolls to bottom if the user was already at the bottom.
func (m *model) appendLogEntry(entry *logEntry) {
	wasAtBottom := m.viewport.AtBottom()

	if m.contentCache.Len() > 0 {
		m.contentCache.WriteString("\n")
	}
	m.renderEntryTo(entry, m.contentCache)

	m.viewport.SetContent(m.contentCache.String())
	if wasAtBottom {
		m.viewport.GotoBottom()
	}
}

// formatEventAsEntry converts a MessageEvent to a logEntry.
func formatEventAsEntry(event *messaging.MessageEvent) logEntry {
	entry := logEntry{
		timestamp: event.Timestamp,
		eventType: event.Type,
		from:      event.From,
	}

	switch event.Type {
	case "ai_response":
		if s, ok := event.Content.(string); ok {
			entry.content = s
		} else {
			entry.content = fmt.Sprintf("%v", event.Content)
		}
	case "tool_call_start":
		if m, ok := event.Content.(map[string]interface{}); ok {
			if name, ok := m["tool_name"].(string); ok {
				entry.toolName = name
			}
			if args, ok := m["arguments"].(string); ok {
				entry.content = args
			}
		}
		if entry.content == "" {
			entry.content = fmt.Sprintf("%v", event.Content)
		}
	case "tool_call_result":
		if m, ok := event.Content.(map[string]interface{}); ok {
			if name, ok := m["tool_name"].(string); ok {
				entry.toolName = name
			}
			if result, ok := m["result"].(string); ok {
				entry.content = result
			}
		}
		if entry.content == "" {
			entry.content = fmt.Sprintf("%v", event.Content)
		}
	case "user_help_needed":
		if s, ok := event.Content.(string); ok {
			entry.content = "HELP: " + s
		} else {
			entry.content = fmt.Sprintf("HELP: %v", event.Content)
		}
	default:
		if s, ok := event.Content.(string); ok {
			entry.content = s
		} else {
			entry.content = fmt.Sprintf("%v", event.Content)
		}
	}

	return entry
}

// formatLogEntry renders a single log entry as a styled line.
func formatLogEntry(entry logEntry, maxWidth int) string {
	timeStr := logTimeStyle.Render(entry.timestamp.Format("15:04:05"))

	var prefix string
	var contentStyle lipgloss.Style

	switch entry.eventType {
	case "ai_response":
		prefix = "AI  "
		contentStyle = logAIResStyle
	case "tool_call_start":
		if entry.toolName != "" {
			prefix = fmt.Sprintf("▶ %s", entry.toolName)
		} else {
			prefix = "▶ TOOL"
		}
		contentStyle = logToolStyle
	case "tool_call_result":
		if entry.toolName != "" {
			prefix = fmt.Sprintf("✔ %s", entry.toolName)
		} else {
			prefix = "✔ RESULT"
		}
		contentStyle = logResultStyle
	case "error":
		prefix = "✖ ERROR"
		contentStyle = logErrorLogStyle
	case "user_help_needed":
		prefix = "? HELP"
		contentStyle = logToolStyle
	default:
		prefix = "● " + entry.eventType
		contentStyle = logStatusStyle
	}

	// Ensure prefix is fixed width for alignment
	prefixStr := lipgloss.NewStyle().Width(24).Render(prefix)

	// Content: truncate long lines
	content := strings.ReplaceAll(entry.content, "\n", " ")
	contentWidth := maxWidth - 36
	if contentWidth < 20 {
		contentWidth = 20
	}
	if lipgloss.Width(content) > contentWidth {
		content = content[:contentWidth-3] + "..."
	}

	return timeStr + " " + prefixStr + " " + contentStyle.Render(content)
}

// submitTask creates a new task and starts execution in the background.
func (m *model) submitTask() tea.Cmd {
	taskDesc := strings.TrimSpace(m.input.Value())
	m.input.SetValue("")
	m.taskRunning = true
	m.errMsg = ""

	ctx, cancel := context.WithCancel(context.Background())
	task := &http.Task{
		ID:         uuid.New().String(),
		Status:     http.TaskStatusRunning,
		ProjectDir: m.projectDir,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
		Memory:     memory.NewConversationMemory(300),
		Context:    ctx,
		CancelFunc: cancel,
	}
	m.taskManager.AddTask(task)
	m.currentTask = task

	// Add submission entry
	m.logEntries = append(m.logEntries, logEntry{
		timestamp: time.Now(),
		eventType: "status",
		content:   "Task submitted: " + taskDesc,
	})
	m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])

	return tea.Batch(
		executeTaskCmd(taskDesc, task, m.assistant, m.taskManager, m.dataManager, m.eventCh),
		listenForEvents(m.eventCh),
	)
}

// submitFollowUp sends a follow-up message to an existing task.
func (m *model) submitFollowUp(message string) tea.Cmd {
	m.input.SetValue("")
	m.taskRunning = true
	m.errMsg = ""

	m.logEntries = append(m.logEntries, logEntry{
		timestamp: time.Now(),
		eventType: "status",
		content:   message,
	})
	m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])

	return tea.Batch(
		executeFollowUpCmd(message, m.currentTask, m.assistant, m.dataManager, m.eventCh),
		listenForEvents(m.eventCh),
	)
}

// executeTaskCmd runs a coding task in a background goroutine.
func executeTaskCmd(
	taskDesc string,
	task *http.Task,
	ca *app.CodingAssistant,
	tm *http.TaskManager,
	dm *datamanager.DataManager,
	eventCh chan *messaging.MessageEvent,
) tea.Cmd {
	return func() tea.Msg {
		dispatcher := messaging.NewMessageDispatcher(100)
		defer dispatcher.Shutdown()

		consumer := &tuiEventConsumer{ch: eventCh}
		dispatcher.RegisterConsumer(consumer)

		ca.IntegrateMessaging(dispatcher)

		request := app.NewTaskRequest(task.Context, task.ID).
			WithProjectDir(task.ProjectDir).
			WithTaskDesc(taskDesc).
			WithMemory(task.Memory).
			WithMessagePublisher(messaging.NewMessagePublisher(dispatcher))

		result, err := ca.ProcessCodingTaskWithCallback(request)

		if dm != nil {
			if saveErr := dm.SaveTaskMemory(task.ID, task.Memory); saveErr != nil {
				// non-fatal
			}
		}

		if err != nil {
			tm.SetTaskError(task.ID, err.Error())
		} else {
			tm.SetTaskResult(task.ID, result)
		}

		return taskCompleteMsg{taskID: task.ID, result: result, err: err}
	}
}

// executeFollowUpCmd runs a follow-up message on an existing task.
func executeFollowUpCmd(
	message string,
	task *http.Task,
	ca *app.CodingAssistant,
	dm *datamanager.DataManager,
	eventCh chan *messaging.MessageEvent,
) tea.Cmd {
	return func() tea.Msg {
		dispatcher := messaging.NewMessageDispatcher(100)
		defer dispatcher.Shutdown()

		consumer := &tuiEventConsumer{ch: eventCh}
		dispatcher.RegisterConsumer(consumer)

		ca.IntegrateMessaging(dispatcher)

		request := app.NewTaskRequest(task.Context, task.ID).
			WithProjectDir(task.ProjectDir).
			WithUserMessage(message).
			WithMemory(task.Memory).
			WithMessagePublisher(messaging.NewMessagePublisher(dispatcher))

		result, err := ca.ProcessConversation(request)

		if dm != nil {
			if saveErr := dm.SaveTaskMemory(task.ID, task.Memory); saveErr != nil {
				// non-fatal
			}
		}

		return taskCompleteMsg{taskID: task.ID, result: result, err: err}
	}
}

// listenForEvents returns a command that waits for the next MessageEvent on the channel.
func listenForEvents(ch chan *messaging.MessageEvent) tea.Cmd {
	return func() tea.Msg {
		event, ok := <-ch
		if !ok {
			return nil
		}
		return taskEventMsg{event: event}
	}
}

func validateInputs(projectDir, taskDesc string) (bool, string) {
	if strings.TrimSpace(taskDesc) == "" {
		return false, langManager.GetText("ValidationErrorEmptyTaskDesc")
	}
	if len([]rune(taskDesc)) < 4 {
		return false, langManager.GetText("ValidationErrorShortTaskDesc")
	}
	return true, ""
}

// startTUI starts the Bubble Tea TUI with the given dependencies.
func startTUI(taskFilePath string, ca *app.CodingAssistant, tm *http.TaskManager, dm *datamanager.DataManager) {
	langManager = NewLanguageManager()

	taskContent := ""
	if taskFilePath != "" {
		if data, err := os.ReadFile(taskFilePath); err == nil {
			taskContent = string(data)
		} else {
			fmt.Printf("无法读取任务文件: %v\n", err)
		}
	}

	// Detect terminal background before entering raw mode to avoid
	// escape-sequence leakage into the input field.
	useDarkStyle := lipgloss.HasDarkBackground()

	p := tea.NewProgram(initialModel(taskContent, ca, tm, dm, useDarkStyle))
	if _, err := p.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
	}
}

// renderBanner draws a rainbow ASCII logo with per-character coloring.
func renderBanner() string {
	asciiLogo := []string{

		"╔═╗┌─┐┌┬┐┌─┐  ╔═╗┌─┐┌┬┐┌─┐┬─┐  ╔═╗╦",
		"║  │ │ ││├┤   ╠═╣│   │ │ │├┬┘  ╠═╣║",
		"╚═╝└─┘─┴┘└─┘  ╩ ╩└─┘ ┴ └─┘┴└─  ╩ ╩╩",
	}

	rainbowColors := []string{
		"167", "180", "221", "114", "75", "98", "176",
	}

	var rendered []string
	for _, line := range asciiLogo {
		var chars []string
		for i, r := range line {
			color := rainbowColors[i%len(rainbowColors)]
			style := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true)
			chars = append(chars, style.Render(string(r)))
		}
		rendered = append(rendered, lipgloss.JoinHorizontal(lipgloss.Top, chars...))
	}
	return bannerPadStyle.Render(lipgloss.JoinVertical(lipgloss.Left, rendered...))
}

// computeFieldWidth returns a responsive width for input fields.
func (m model) computeFieldWidth() int {
	const minField = 38
	const maxField = 90
	if m.termWidth <= 0 {
		return 60
	}
	avail := m.termWidth - 8
	if avail < minField {
		return minField
	}
	if avail > maxField {
		return maxField
	}
	return avail
}

// renderHistoryPanel renders the history panel with single-line items and stable scrolling.
func (m model) renderHistoryPanel() string {
	panelWidth := m.termWidth - 4
	if panelWidth < 40 {
		panelWidth = 40
	}

	var b strings.Builder

	// ── Header: title + filter input ──
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39")).Render("◆ " + langManager.GetText("HistoryTitle"))

	var filterDisplay string
	if m.historyFilter != "" {
		cursor := lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Render("▌")
		filterDisplay = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(m.historyFilter) + cursor
	} else {
		filterDisplay = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("244")).Render(langManager.GetText("HistoryFilterPlaceholder"))
	}
	filterPart := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("│ ") + filterDisplay

	counter := lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("244")).Render(
		fmt.Sprintf("%d/%d", m.historyIndex+1, len(m.filteredItems)),
	)

	headerText := title + "  " + filterPart + "  " + counter

	headerStyle := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(lipgloss.Color("237")).
		Width(panelWidth).
		Padding(0, 1)

	b.WriteString(headerStyle.Render(headerText))
	b.WriteString("\n")

	// ── Body: single-line items ──
	bodyHeight := m.termHeight - 8 // header(~2) + footer(~6)
	if bodyHeight < 4 {
		bodyHeight = 4
	}

	if len(m.filteredItems) == 0 {
		empty := lipgloss.NewStyle().
			Foreground(lipgloss.Color("244")).
			Width(panelWidth).
			Padding(1, 2).
			Render(langManager.GetText("HistoryEmpty"))
		b.WriteString(empty)
	} else {
		// Edge-triggered scroll: update scrollStart only when selection leaves visible area
		topMargin := 2
		btmMargin := 2
		if bodyHeight < topMargin+btmMargin+1 {
			topMargin = 1
			btmMargin = 1
		}
		scrollStart := m.historyScrollStart
		if m.historyIndex < scrollStart+topMargin {
			scrollStart = m.historyIndex - topMargin
		} else if m.historyIndex >= scrollStart+bodyHeight-btmMargin {
			scrollStart = m.historyIndex - bodyHeight + btmMargin + 1
		}
		if scrollStart < 0 {
			scrollStart = 0
		}
		maxStart := len(m.filteredItems) - bodyHeight
		if maxStart < 0 {
			maxStart = 0
		}
		if scrollStart > maxStart {
			scrollStart = maxStart
		}
		m.historyScrollStart = scrollStart

		end := scrollStart + bodyHeight
		if end > len(m.filteredItems) {
			end = len(m.filteredItems)
		}

		// "more above" indicator
		if scrollStart > 0 {
			indicator := lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("244")).
				Width(panelWidth).Padding(0, 2).
				Render(fmt.Sprintf("▲ %s", fmt.Sprintf(langManager.GetText("HistoryMoreAbove"), scrollStart)))
			b.WriteString(indicator)
			b.WriteString("\n")
		}

		// Compute title area width: date(11) + indent(2) + spacer(2) + cursor(2 for selected)
		const dateWidth = 11
		const indentWidth = 2
		const spacerWidth = 2
		titleMaxWidth := panelWidth - dateWidth - indentWidth - spacerWidth - 2
		if titleMaxWidth < 20 {
			titleMaxWidth = 20
		}

		dateStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
		titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
		selStyle := lipgloss.NewStyle().
			Background(lipgloss.Color("39")).
			Foreground(lipgloss.Color("0")).
			Width(panelWidth - 2).
			Padding(0, 1)

		normalStyle := lipgloss.NewStyle().
			Width(panelWidth - 2).
			Padding(0, 1)

		for i := scrollStart; i < end; i++ {
			item := m.filteredItems[i]
			selected := i == m.historyIndex

			// Truncate title to fit
			title := item.Title
			titleRunes := []rune(title)
			if len(titleRunes) > titleMaxWidth {
				title = string(titleRunes[:titleMaxWidth-1]) + "…"
			}

			dateStr := item.CreatedAt.Format("01-02 15:04")

			if selected {
				cursor := "▐ "
				line := selStyle.Render(cursor + dateStyle.Foreground(lipgloss.Color("0")).Render(dateStr) + "  " + title)
				b.WriteString(line)
			} else {
				line := normalStyle.Render("  " + dateStyle.Render(dateStr) + "  " + titleStyle.Render(title))
				b.WriteString(line)
			}
			b.WriteString("\n")
		}

		// "more below" indicator
		if end < len(m.filteredItems) {
			remaining := len(m.filteredItems) - end
			indicator := lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("244")).
				Width(panelWidth).Padding(0, 2).
				Render(fmt.Sprintf("▼ %s", fmt.Sprintf(langManager.GetText("HistoryMoreBelow"), remaining)))
			b.WriteString(indicator)
			b.WriteString("\n")
		}
	}

	// ── Footer: key hints ──
	var hintText string
	if m.historyConfirmDelete {
		hintText = lipgloss.NewStyle().Foreground(lipgloss.Color("167")).Bold(true).Render(langManager.GetText("HistoryConfirmDelete"))
	} else {
		hints := []string{
			lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true).Render(langManager.GetText("HistoryKeyContinue")),
			lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("245")).Render(langManager.GetText("HistoryKeyDelete")),
			lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("245")).Render(langManager.GetText("HistoryKeyBack")),
			lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("245")).Render(langManager.GetText("HistoryKeyClearFilter")),
		}
		hintText = strings.Join(hints, "  ")
	}

	footerStyle := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(lipgloss.Color("237")).
		Width(panelWidth).
		Padding(0, 1)

	b.WriteString(footerStyle.Render(hintText))

	return b.String()
}

