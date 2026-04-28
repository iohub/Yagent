package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"codeactor/internal/assistant"
	"codeactor/internal/http"
	"codeactor/internal/memory"
	"codeactor/pkg/messaging"

	"github.com/charmbracelet/bubbles/textinput"
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

	focusedInputStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("39")).Padding(0, 1)
	blurredInputStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("237")).Padding(0, 1)

	welcomePanelStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("39")).Padding(1, 2)
	welcomeLeftStyle  = lipgloss.NewStyle().Width(38)
	welcomeTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
	welcomeSubStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	welcomeRightTitle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
	welcomeTipStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	welcomeDimStyle   = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("242"))

	buttonFocusedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	buttonBlurredStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("167")).Bold(true)
	infoMsgStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	footerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	backdropStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("237"))
	modalBoxStyle   = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("39")).Padding(1, 2)
	modalTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	itemStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	itemDimStyle    = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("244"))
	itemSelStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("39"))

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
	assistant   *assistant.CodingAssistant
	taskManager *http.TaskManager
	dataManager *assistant.DataManager

	// Input
	input textinput.Model

	// Message log
	logEntries      []logEntry
	viewport        viewport.Model
	glamourRenderer *glamour.TermRenderer
	useDarkStyle    bool

	// Task execution state
	taskRunning bool
	eventCh     chan *messaging.MessageEvent

	// Standard state
	termWidth   int
	termHeight  int
	quitting    bool
	errMsg      string
	infoMsg     string
	currentLang Language
	projectDir  string

	// History modal state
	showHistoryModal bool
	historyItems     []assistant.TaskHistoryItem
	filteredItems    []assistant.TaskHistoryItem
	historyIndex     int
	historySearch    textinput.Model
}

func initialModel(preloadedTaskContent string, ca *assistant.CodingAssistant, tm *http.TaskManager, dm *assistant.DataManager, useDarkStyle bool) model {
	ti := textinput.New()
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	ti.Placeholder = langManager.GetText("TaskDescPlaceholder")
	ti.Focus()
	ti.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	ti.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	ti.CharLimit = 256
	ti.Width = 60
	if preloadedTaskContent != "" {
		ti.SetValue(preloadedTaskContent)
	}

	hSearch := textinput.New()
	hSearch.Placeholder = langManager.GetText("HistorySearchHint")
	hSearch.CharLimit = 256
	hSearch.Width = 60
	hSearch.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))

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
		historySearch:   hSearch,
		viewport:        vp,
		glamourRenderer: glamourRenderer,
		useDarkStyle:    useDarkStyle,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		textinput.Blink,
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
	m.historySearch.Placeholder = langManager.GetText("HistorySearchHint")
}

func (m *model) openHistoryModal() {
	dm, err := assistant.NewDataManager()
	if err == nil {
		items, err2 := dm.ListTaskHistory(50)
		if err2 == nil {
			m.historyItems = items
			m.filteredItems = items
		}
	}
	m.historyIndex = 0
	m.showHistoryModal = true
	m.historySearch.SetValue("")
	m.historySearch.Focus()
}

func (m *model) closeHistoryModal() {
	m.showHistoryModal = false
	m.historySearch.Blur()
}

func (m *model) applyHistorySelection() {
	if len(m.filteredItems) == 0 {
		return
	}
	if m.historyIndex < 0 {
		m.historyIndex = 0
	}
	if m.historyIndex >= len(m.filteredItems) {
		m.historyIndex = len(m.filteredItems) - 1
	}
	selected := m.filteredItems[m.historyIndex]
	m.input.SetValue(selected.Title)
	m.closeHistoryModal()
}

func (m *model) filterHistoryList() {
	query := strings.TrimSpace(m.historySearch.Value())
	if query == "" {
		m.filteredItems = m.historyItems
		m.historyIndex = 0
		return
	}
	qLower := strings.ToLower(query)
	filtered := make([]assistant.TaskHistoryItem, 0, len(m.historyItems))
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
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		m.input.Width = m.computeFieldWidth()
		m.historySearch.Width = m.computeFieldWidth()
		m.resizeViewport()
		m.rebuildViewportContent()
		return m, nil

	case tea.KeyMsg:
		// History modal key handling
		if m.showHistoryModal {
			switch msg.String() {
			case "esc", "ctrl+c":
				m.closeHistoryModal()
				return m, nil
			case "enter":
				m.applyHistorySelection()
				return m, nil
			case "up":
				if m.historyIndex > 0 {
					m.historyIndex--
				}
				return m, nil
			case "down":
				if m.historyIndex < len(m.filteredItems)-1 {
					m.historyIndex++
				}
				return m, nil
			default:
				var cmd tea.Cmd
				m.historySearch, cmd = m.historySearch.Update(msg)
				m.filterHistoryList()
				return m, cmd
			}
		}

		switch msg.String() {
		case "ctrl+c", "esc":
			m.quitting = true
			return m, tea.Quit

		case "enter":
			if m.taskRunning {
				m.errMsg = langManager.GetText("ValidationErrorEmptyTaskDesc")
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
			return m, m.submitTask()

		case "ctrl+l":
			m.toggleLanguage()
			return m, nil

		case "ctrl+h":
			m.openHistoryModal()
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
		m.rebuildViewportContent()
		return m, listenForEvents(m.eventCh)

	case taskCompleteMsg:
		m.taskRunning = false
		if msg.err != nil {
			m.errMsg = msg.err.Error()
			m.logEntries = append(m.logEntries, logEntry{
				timestamp: time.Now(),
				eventType: "error",
				content:   msg.err.Error(),
			})
		}
		m.rebuildViewportContent()
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

	// Welcome panel with logo
	b.WriteString(m.renderWelcomePanel())
	b.WriteString("\n")

	// Scrollable message area with viewport
	b.WriteString(m.viewport.View())

	// Separator
	sepWidth := m.termWidth
	if sepWidth < 40 {
		sepWidth = 40
	}
	b.WriteString(logSeparatorStyle.Render(strings.Repeat("─", sepWidth)))
	b.WriteString("\n")

	// Input line: ❯ [input]
	promptChar := "❯ "
	var promptStyled string
	if m.input.Focused() {
		promptStyled = promptFocusedStyle.Render(promptChar)
	} else {
		promptStyled = promptBlurredStyle.Render(promptChar)
	}
	m.input.Width = m.computeFieldWidth()
	inputLine := promptStyled + m.input.View()

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
	statusLine := footerStyle.Render("enter submit │ ctrl+l lang │ ctrl+h history │ esc quit") + taskIndicator
	footer.WriteString(lipgloss.NewStyle().MarginLeft(2).Render(statusLine))

	b.WriteString(footer.String())

	// History modal overlay
	if m.showHistoryModal {
		b.WriteString("\n")
		b.WriteString(m.renderHistoryModal())
	}

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
	welcomeHeight := lipgloss.Height(m.renderWelcomePanel())
	footerHeight := 5
	if m.errMsg != "" {
		footerHeight++
	}
	vpHeight := m.termHeight - welcomeHeight - footerHeight
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

// rebuildViewportContent rebuilds the full viewport content from logEntries,
// using glamour for ai_response entries and plain formatting for others.
func (m *model) rebuildViewportContent() {
	var b strings.Builder

	for _, entry := range m.logEntries {
		if entry.eventType == "ai_response" && m.glamourRenderer != nil {
			rendered, err := m.glamourRenderer.Render(entry.content)
			if err == nil {
				b.WriteString(rendered)
				b.WriteString("\n")
				continue
			}
		}
		// Fallback to simple text rendering
		b.WriteString(formatLogEntry(entry, m.viewport.Width))
		b.WriteString("\n")
	}

	m.viewport.SetContent(b.String())
	m.viewport.GotoBottom()
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

	// Add submission entry
	m.logEntries = append(m.logEntries, logEntry{
		timestamp: time.Now(),
		eventType: "status",
		content:   "Task submitted: " + taskDesc,
	})
	m.rebuildViewportContent()

	return tea.Batch(
		executeTaskCmd(taskDesc, m.projectDir, m.assistant, m.taskManager, m.dataManager, m.eventCh),
		listenForEvents(m.eventCh),
	)
}

// executeTaskCmd runs a coding task in a background goroutine.
func executeTaskCmd(
	taskDesc string,
	projectDir string,
	ca *assistant.CodingAssistant,
	tm *http.TaskManager,
	dm *assistant.DataManager,
	eventCh chan *messaging.MessageEvent,
) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithCancel(context.Background())

		task := &http.Task{
			ID:         uuid.New().String(),
			Status:     http.TaskStatusRunning,
			ProjectDir: projectDir,
			CreatedAt:  time.Now(),
			UpdatedAt:  time.Now(),
			Memory:     memory.NewConversationMemory(300),
			Context:    ctx,
			CancelFunc: cancel,
		}
		tm.AddTask(task)

		dispatcher := messaging.NewMessageDispatcher(100)
		defer dispatcher.Shutdown()

		consumer := &tuiEventConsumer{ch: eventCh}
		dispatcher.RegisterConsumer(consumer)

		ca.IntegrateMessaging(dispatcher)

		request := assistant.NewTaskRequest(ctx, task.ID).
			WithProjectDir(projectDir).
			WithTaskDesc(taskDesc).
			WithMemory(task.Memory).
			WithMessagePublisher(assistant.NewMessagePublisher(dispatcher))

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
func startTUI(taskFilePath string, ca *assistant.CodingAssistant, tm *http.TaskManager, dm *assistant.DataManager) {
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
		"╔╦╗╦  ╦  ╔═╗┌─┐┌┬┐┌─┐",
		" ║║║  ║  ║  │ │ ││├┤ ",
		"═╩╝╩═╝╩  ╚═╝└─┘─┴┘└─┘",
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

// renderHistoryModal renders the history selection popup.
func (m model) renderHistoryModal() string {
	var out strings.Builder

	out.WriteString(backdropStyle.Render(strings.Repeat("─", max(40, m.computeFieldWidth()+8))))
	out.WriteString("\n")

	title := modalTitleStyle.Render("◇ " + langManager.GetText("HistoryTitle"))
	searchWidth := m.computeFieldWidth()
	m.historySearch.Width = searchWidth
	searchLine := focusedInputStyle.Render(m.historySearch.View())

	boxInner := title + "\n" + itemDimStyle.Render(langManager.GetText("HistorySearchHint")) + "\n\n" + searchLine + "\n"

	maxItems := 12
	if len(m.filteredItems) == 0 {
		boxInner += "\n" + itemDimStyle.Render(langManager.GetText("HistoryEmpty"))
	} else {
		end := len(m.filteredItems)
		if end > maxItems {
			start := m.historyIndex - maxItems/2
			if start < 0 {
				start = 0
			}
			end = start + maxItems
			if end > len(m.filteredItems) {
				end = len(m.filteredItems)
				start = end - maxItems
				if start < 0 {
					start = 0
				}
			}
			for i := start; i < end; i++ {
				it := m.filteredItems[i]
				line := fmt.Sprintf("%s  %s", itemDimStyle.Render(it.CreatedAt.Format("01-02 15:04")), it.Title)
				if i == m.historyIndex {
					boxInner += "\n" + itemSelStyle.Render(line)
				} else {
					boxInner += "\n" + itemStyle.Render(line)
				}
			}
		} else {
			for i, it := range m.filteredItems {
				line := fmt.Sprintf("%s  %s", itemDimStyle.Render(it.CreatedAt.Format("01-02 15:04")), it.Title)
				if i == m.historyIndex {
					boxInner += "\n" + itemSelStyle.Render(line)
				} else {
					boxInner += "\n" + itemStyle.Render(line)
				}
			}
		}
	}

	footer := lipgloss.JoinHorizontal(lipgloss.Top,
		buttonFocusedStyle.Render("● "+langManager.GetText("HistoryUseSelected")),
		"  ",
		buttonBlurredStyle.Render("○ "+langManager.GetText("HistoryClose")),
	)
	boxInner += "\n\n" + footer

	box := modalBoxStyle.Width(m.computeFieldWidth() + 8).Render(boxInner)
	leftAligned := lipgloss.NewStyle().MarginLeft(2).Render(box)
	out.WriteString(leftAligned)
	out.WriteString("\n")

	return out.String()
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
