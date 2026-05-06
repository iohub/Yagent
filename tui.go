package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"codeactor/internal/app"
	"codeactor/internal/datamanager"
	"codeactor/internal/http"
	"codeactor/internal/memory"
	"codeactor/internal/tui"
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

	// Diff rendering styles
	diffHunkStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	diffAddStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	diffDelStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("167"))
	diffCtxStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	diffNoNewlineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	// Tool status styles (running → finished transition)
	toolRunningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("228")) // gold — running
	toolDoneStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("114")) // green — success
	toolErrorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("167")) // red — error

	// Mode-specific styles (vim-like edit / command modes)
	commandPrefixStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)       // orange ":"
	commandLabelStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)       // "COMMAND"
	commandHintStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))                  // tips text
)

// logEntry represents a single message in the TUI log area.
type logEntry struct {
	timestamp        time.Time
	eventType        string
	from             string
	content          string
	toolName         string
	toolCallID       string // tool_call_id for matching start/result events
	isToolRunning    bool   // true when awaiting result
	executionSummary string // short summary extracted from arguments (file path, command, etc.)
	resultBrief      string // brief result description (e.g., "120 lines", "modified")
	diffText         string // unified diff content for file edit results
	rendered         string // cached rendered output (glamour or plain), cleared on resize

	// Tool entry for new-style rendering (non-nil for tool events)
	toolEntry *tui.ToolEntry
}

// tickMsg is sent by the animation ticker to advance animations.
type tickMsg struct{}

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

// publisherReadyMsg signals that the MessagePublisher is ready for dialog responses.
type publisherReadyMsg struct {
	publisher *messaging.MessagePublisher
}

// confirmDialog holds the state of the authorization confirmation dialog.
type confirmDialog struct {
	open           bool
	question       string
	requestID      string
	selectedOption int // 0=Allow, 1=Allow All, 2=Deny
}

// taskCompleteDialog holds the state of the task completion overlay dialog.
type taskCompleteDialog struct {
	open    bool
	message string
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

	// Authorization confirmation dialog
	confirmDialog     confirmDialog
	taskCompleteDialog taskCompleteDialog
	publisher         *messaging.MessagePublisher
	publisherCh   chan *messaging.MessagePublisher

	// Command mode (vim-like): hidden input, ":" prefix, different bg.
	// Toggled with Esc (edit→cmd) and i (cmd→edit). Auto-enabled on task submit.
	commandMode   bool
	commandBuffer string // hidden command input buffer in command mode
	lastKey       string // tracks previous key for multi-key sequences (gg, ZZ)
	showHelpDialog bool  // "?" help overlay in command mode

	// Tool call state tracking: tool_call_id → ToolEntry
	toolCallEntries map[string]*tui.ToolEntry

	// Current LLM model being used (extracted from model_info events)
	currentModel string

	// Animation state for running tools
	anim        *tui.Anim
	activeAnim  bool // true when there are running tool entries
	animFrame   int  // frame counter for throttled viewport rebuilds
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

	// Edit mode base style: dark background shadow
	editBaseStyle := lipgloss.NewStyle().Background(lipgloss.Color("235"))
	ti.FocusedStyle.Base = editBaseStyle
	ti.BlurredStyle.Base = editBaseStyle
	ti.FocusedStyle.Prompt = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true).Background(lipgloss.Color("235"))
	ti.BlurredStyle.Prompt = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Background(lipgloss.Color("235"))
	ti.FocusedStyle.CursorLine = lipgloss.NewStyle().Background(lipgloss.Color("235"))
	ti.BlurredStyle.CursorLine = lipgloss.NewStyle().Background(lipgloss.Color("235"))
	ti.FocusedStyle.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(lipgloss.Color("235"))
	ti.BlurredStyle.Placeholder = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(lipgloss.Color("235"))

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
		viewport:         vp,
		contentCache:     &strings.Builder{},
		glamourRenderer:  glamourRenderer,
		useDarkStyle:     useDarkStyle,
		toolCallEntries:  make(map[string]*tui.ToolEntry),
			anim:             tui.NewAnim(10),
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		textarea.Blink,
		listenForEvents(m.eventCh),
		tickCmd(),
	)
}

// tickCmd returns a command that fires a tickMsg every 100ms for animation.
func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg{}
	})
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

// processCommand handles vim-like command input (hidden buffer) in command mode.
func (m *model) processCommand(cmd string) {
	cmd = strings.TrimSpace(cmd)
	switch {
	case cmd == ":q" || cmd == ":quit" || cmd == ":q!":
		m.quitting = true
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
					if te.Status == tui.ToolStatusRunning {
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
					m.quitting = true
					return m, tea.Quit
				default:
					// Invalid combo: discard lastKey and fall through to process key normally
				}
			}

			// Help dialog is open: let action keys (i/esc/enter/?/ctrl+c) pass through,
			// dismiss on any other key without processing it.
			if m.showHelpDialog && key != "i" && key != "esc" && key != "enter" && key != "?" && key != "ctrl+c" {
				m.showHelpDialog = false
				return m, nil
			}

			switch key {
			case "ctrl+c":
				m.quitting = true
				return m, tea.Quit

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
					// Cancel the running task
					m.currentTask.CancelFunc()
					m.logEntries = append(m.logEntries, logEntry{
						timestamp: time.Now(),
						eventType: "status",
						content:   "Task cancelled by user",
					})
					m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
				} else {
					// Idle: switch to edit mode
					m.commandMode = false
					m.commandBuffer = ""
					m.lastKey = ""
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
			m.quitting = true
			return m, tea.Quit

		case "esc":
			// Enter command mode (cancel task if running)
			if m.taskRunning && m.currentTask != nil && m.currentTask.CancelFunc != nil {
				m.currentTask.CancelFunc()
				m.logEntries = append(m.logEntries, logEntry{
					timestamp: time.Now(),
					eventType: "status",
					content:   "Task cancelled by user",
				})
				m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
			}
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
					toolEntry.SetResult(tui.ToolResultInfo{
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
					matchedEntry.SetResult(tui.ToolResultInfo{
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

// openConfirmDialog parses a user_help_needed event and opens the confirmation dialog.
func (m *model) openConfirmDialog(event *messaging.MessageEvent) {
	content, ok := event.Content.(map[string]interface{})
	if !ok {
		return
	}
	question, _ := content["question"].(string)
	if question == "" {
		return
	}
	requestID, _ := content["request_id"].(string)

	m.confirmDialog = confirmDialog{
		open:           true,
		question:       question,
		requestID:      requestID,
		selectedOption: 0, // default: Allow
	}
}

// respondToAuth publishes the user response and closes the dialog.
func (m *model) respondToAuth(response string) {
	if m.publisher != nil {
		m.publisher.Publish("user_help_response", map[string]interface{}{
			"response":   response,
			"request_id": m.confirmDialog.requestID,
		}, "User")
	}
	m.confirmDialog.open = false

	// Log the response
	m.logEntries = append(m.logEntries, logEntry{
		timestamp: time.Now(),
		eventType: "status",
		content:   fmt.Sprintf("Auth response: %s", response),
	})
	m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
}

// listenForPublisher waits for the publisher to become available via the channel.
func listenForPublisher(ch chan *messaging.MessagePublisher) tea.Cmd {
	return func() tea.Msg {
		publisher, ok := <-ch
		if !ok {
			return nil
		}
		return publisherReadyMsg{publisher: publisher}
	}
}

func (m model) View() string {
	if m.quitting {
		return ""
	}

	// When confirmation dialog is open, render it as an overlay on top of the normal view
	if m.confirmDialog.open {
		return m.renderConfirmDialog()
	}

	// When help dialog is open in command mode, render it as an overlay
	if m.showHelpDialog {
		return m.renderHelpDialog()
	}

	// When task complete dialog is open, render it as an overlay
	if m.taskCompleteDialog.open {
		return m.renderTaskCompleteDialog()
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

		// ── Input area: edit mode vs command mode ──
		var footer strings.Builder

		if m.commandMode {
			// ── Command mode (vim-like): hidden input, ":" prefix, colored bar ──
			modeBar := lipgloss.NewStyle().
				Background(lipgloss.Color("214")).
				Foreground(lipgloss.Color("214")).
				Render("▊")

			var cmdLine string
			cmdPrefix := commandPrefixStyle.Render(" :")
			cmdLabel := commandLabelStyle.Render(" " + langManager.GetText("CommandModePrompt") + " ")
			if m.commandBuffer != "" {
				cmdBufDisplay := lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(m.commandBuffer)
				cmdCursor := lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Blink(true).Render("▊")
				cmdLine = modeBar + cmdPrefix + cmdLabel + " " + cmdBufDisplay + cmdCursor
			} else {
				cmdCursor := lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Blink(true).Render("▊")
				cmdLine = modeBar + cmdPrefix + cmdLabel + " " + cmdCursor
			}

			var cmdTips string
			if m.taskRunning {
				cmdTips = langManager.GetText("CommandModeTips")
			} else {
				cmdTips = langManager.GetText("CommandModeIdleTips")
			}
			cmdTipsLine := commandHintStyle.Render("  " + cmdTips)

			footer.WriteString(cmdLine + cmdTipsLine)
			footer.WriteString("\n")
		} else {
			// ── Edit mode: textarea with dark background (via Base style), no bar ──
			m.input.SetWidth(m.computeFieldWidth())
			inputLine := m.input.View()
			footer.WriteString(lipgloss.NewStyle().Render(inputLine))
			footer.WriteString("\n")
		}

		// Error message
		if m.errMsg != "" {
			footer.WriteString(lipgloss.NewStyle().MarginLeft(2).Render(errorStyle.Render("✖ " + m.errMsg)))
			footer.WriteString("\n")
		}

		// Status line: mode indicator + task indicator + model name
		taskIndicator := ""
		if m.taskRunning {
			if m.currentModel != "" {
				taskIndicator = logStatusStyle.Render(fmt.Sprintf(" ◷ Running [%s]...", m.currentModel))
			} else {
				taskIndicator = logStatusStyle.Render(" ◷ Running...")
			}
		}
		footer.WriteString("\n")
		var statusLine string
		if m.commandMode {
			statusLine = footerStyle.Render("COMMAND MODE")
		} else {
			statusLine = footerStyle.Render(langManager.GetText("EditModeTips"))
		}
		footer.WriteString(lipgloss.NewStyle().MarginLeft(2).Render(statusLine + taskIndicator))

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
	footerHeight := 6
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
// Called on terminal resize since rendering depends on viewport width.
func (m *model) invalidateRenderedCache() {
	for i := range m.logEntries {
		m.logEntries[i].rendered = ""
		if m.logEntries[i].toolEntry != nil {
			m.logEntries[i].toolEntry.InvalidateCache()
		}
	}
}

// buildViewportContent rebuilds the full viewport content from scratch.
// Used for initial load, terminal resize, or conversation switch.
func (m *model) buildViewportContent() {
	m.rebuildContentCache()
	m.viewport.SetContent(m.contentCache.String())
	m.viewport.GotoBottom()
}

// rebuildViewportPreservingScroll rebuilds viewport content but preserves
// the current scroll position. Used for animation tick updates so that
// scrolling up to read history isn't interrupted by SetContent+GotoBottom.
func (m *model) rebuildViewportPreservingScroll() {
	yOffset := m.viewport.YOffset
	m.rebuildContentCache()
	m.viewport.SetContent(m.contentCache.String())
	// Restore Y offset, clamped to avoid overscroll
	totalLines := m.viewport.TotalLineCount()
	visibleLines := m.viewport.Height
	maxOffset := totalLines - visibleLines
	if maxOffset < 0 {
		maxOffset = 0
	}
	if yOffset > maxOffset {
		yOffset = maxOffset
	}
	m.viewport.YOffset = yOffset
}

// rebuildContentCache rebuilds m.contentCache with the current welcome panel
// and all log entries. Callers must then call viewport.SetContent().
func (m *model) rebuildContentCache() {
	m.contentCache.Reset()
	// Estimate capacity: ~200 bytes per entry to reduce reallocations
	estCap := (len(m.logEntries) + 2) * 200
	if estCap > m.contentCache.Cap() {
		m.contentCache.Grow(estCap)
	}

	m.contentCache.WriteString(m.renderWelcomePanel())
	m.contentCache.WriteString("\n")

	for i := range m.logEntries {
		entry := &m.logEntries[i]
		m.renderEntryTo(entry, m.contentCache)
		m.contentCache.WriteString("\n")
	}
}

// renderEntryTo renders a single log entry into the builder, caching the result
// in the entry for reuse. Uses glamour for ai_response, diff styling for diffs,
// plain formatting otherwise.
func (m *model) renderEntryTo(entry *logEntry, b *strings.Builder) {
	// For running tool entries, never cache (animation changes each frame)
	if entry.toolEntry != nil && entry.toolEntry.Status == tui.ToolStatusRunning {
		toolLine := renderToolEntryWithAnim(*entry, m.viewport.Width, m.anim)
		b.WriteString(toolLine)
		return
	}

	// Use cached rendered content if available
	if entry.rendered != "" {
		b.WriteString(entry.rendered)
		return
	}

	// Capture the start position to cache the output
	start := b.Len()

	// Tool entry rendering (non-running) — use new renderer
	if entry.toolEntry != nil {
		rendered := renderToolEntry(*entry, m.viewport.Width)
		b.WriteString(rendered)
		entry.rendered = b.String()[start:]
		return
	}

	// Diff rendering takes priority
	if entry.diffText != "" {
		rendered := renderDiff(entry)
		b.WriteString(rendered)
		entry.rendered = b.String()[start:]
		return
	}

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

// extractToolSummary extracts a concise execution summary from tool arguments.
// Used to display what a tool is doing (file path, command, pattern, etc.).
func extractToolSummary(toolName string, argsJSON string) string {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}
	switch toolName {
	case "read_file", "edit_file", "search_replace_in_file", "create_file", "delete_file":
		if fp, ok := args["file_path"].(string); ok && fp != "" {
			return fp
		}
		if fp, ok := args["path"].(string); ok && fp != "" {
			return fp
		}
	case "run_bash":
		if cmd, ok := args["command"].(string); ok && cmd != "" {
			if len(cmd) > 80 {
				return cmd[:77] + "..."
			}
			return cmd
		}
	case "search_by_regex", "grep_search":
		if pattern, ok := args["pattern"].(string); ok && pattern != "" {
			if len(pattern) > 60 {
				return pattern[:57] + "..."
			}
			return pattern
		}
	case "list_dir", "print_dir_tree":
		if path, ok := args["path"].(string); ok && path != "" {
			return path
		}
		if dir, ok := args["directory"].(string); ok && dir != "" {
			return dir
		}
	case "rename_file":
		from, _ := args["from"].(string)
		to, _ := args["to"].(string)
		if from != "" && to != "" {
			return from + " → " + to
		}
	case "thinking", "agent_exit":
		if reason, ok := args["reason"].(string); ok && reason != "" {
			return reason
		}
	}
	// For delegate tools, show task summary
	if strings.HasPrefix(toolName, "delegate_") {
		if task, ok := args["task"].(string); ok && task != "" {
			if len(task) > 60 {
				return task[:57] + "..."
			}
			return task
		}
	}
	return ""
}

// extractResultBrief generates a short result summary for the finished line.
func extractResultBrief(toolName string, result string) string {
	if strings.HasPrefix(result, "Error:") {
		// Short error message
		errMsg := strings.TrimPrefix(result, "Error: ")
		if len(errMsg) > 50 {
			return errMsg[:47] + "..."
		}
		return errMsg
	}
	switch toolName {
	case "read_file":
		lines := strings.Count(result, "\n")
		if lines > 0 {
			return fmt.Sprintf("%d lines", lines)
		}
		return fmt.Sprintf("%d bytes", len(result))
	case "list_dir", "print_dir_tree":
		return ""
	case "run_bash":
		// Show first non-empty line of output
		trimmed := strings.TrimSpace(result)
		if len(trimmed) > 60 {
			return trimmed[:57] + "..."
		}
		return trimmed
	case "search_by_regex", "grep_search":
		lines := strings.Count(result, "\n")
		return fmt.Sprintf("%d matches", lines)
	default:
		if strings.HasPrefix(toolName, "delegate_") {
			return ""
		}
		return ""
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
			if id, ok := m["tool_call_id"].(string); ok {
				entry.toolCallID = id
			}
			if args, ok := m["arguments"].(string); ok {
				entry.content = args
				entry.executionSummary = extractToolSummary(entry.toolName, args)
				// Create ToolEntry for new-style rendering
				entry.toolEntry = tui.NewToolEntry(tui.ToolCallInfo{
					ID:        entry.toolCallID,
					Name:      entry.toolName,
					Arguments: args,
					Summary:   entry.executionSummary,
				})
			}
		}
		entry.isToolRunning = true
		if entry.content == "" {
			entry.content = fmt.Sprintf("%v", event.Content)
		}
		if entry.toolEntry == nil {
			// Fallback: create ToolEntry even without parsed args
			entry.toolEntry = tui.NewToolEntry(tui.ToolCallInfo{
				ID:   entry.toolCallID,
				Name: entry.toolName,
			})
		}
	case "tool_call_result":
		if m, ok := event.Content.(map[string]interface{}); ok {
			if name, ok := m["tool_name"].(string); ok {
				entry.toolName = name
			}
			if id, ok := m["tool_call_id"].(string); ok {
				entry.toolCallID = id
			}
			if result, ok := m["result"].(string); ok {
				entry.content = result
				entry.resultBrief = extractResultBrief(entry.toolName, result)
				// Try to extract diff from JSON result
				entry.diffText = extractDiffFromResult(result)
			}
		}
		entry.isToolRunning = false
		if entry.content == "" {
			entry.content = fmt.Sprintf("%v", event.Content)
		}
		// Create a ToolEntry for standalone results so they use new-style
		// rendering (✓ read_file · /path/to/file) instead of the legacy
		// emoji path (✅ read_file).
		isErr := strings.HasPrefix(entry.content, "Error:")
		entry.toolEntry = tui.NewToolEntry(tui.ToolCallInfo{
			ID:      entry.toolCallID,
			Name:    entry.toolName,
			Summary: extractToolSummary(entry.toolName, ""),
		})
		if isErr {
			entry.toolEntry.SetResult(tui.ToolResultInfo{
				ToolCallID: entry.toolCallID,
				Name:       entry.toolName,
				Content:    entry.content,
				IsError:    true,
			})
		} else {
			entry.toolEntry.SetResult(tui.ToolResultInfo{
				ToolCallID: entry.toolCallID,
				Name:       entry.toolName,
				Content:    entry.content,
				IsError:    false,
			})
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
	var prefix string
	var contentStyle lipgloss.Style

	switch entry.eventType {
	case "ai_response":
		prefix = "AI  "
		contentStyle = logAIResStyle
	case "tool_call_start":
		// Use new-style rendering if ToolEntry is available
		if entry.toolEntry != nil {
			return renderToolEntry(entry, maxWidth)
		}
		// Fallback: legacy rendering
		if entry.toolName != "" {
			prefix = "🔘 " + tui.RenderToolName(entry.toolName)
		} else {
			prefix = "🔘 TOOL"
		}
		contentStyle = toolRunningStyle
	case "tool_call_result":
		// Use new-style rendering if ToolEntry is available (via parent start entry)
		// standalone entries use legacy rendering
		if entry.toolEntry != nil {
			return renderToolEntry(entry, maxWidth)
		}
		if strings.HasPrefix(entry.content, "Error:") {
			if entry.toolName != "" {
				prefix = "❌ " + tui.RenderToolName(entry.toolName)
			} else {
				prefix = "❌ RESULT"
			}
			contentStyle = toolErrorStyle
		} else {
			if entry.toolName != "" {
				prefix = "✅ " + tui.RenderToolName(entry.toolName)
			} else {
				prefix = "✅ RESULT"
			}
			contentStyle = toolDoneStyle
		}
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

	// Build display content: prefer execution summary + result brief
	var displayContent string
	if entry.executionSummary != "" {
		displayContent = entry.executionSummary
		if entry.resultBrief != "" {
			displayContent += " · " + entry.resultBrief
		}
	} else {
		displayContent = strings.ReplaceAll(entry.content, "\n", " ")
	}

	// Truncate long content
	contentWidth := maxWidth - 10
	if contentWidth < 20 {
		contentWidth = 20
	}
	if lipgloss.Width(displayContent) > contentWidth {
		runes := []rune(displayContent)
		if len(runes) > contentWidth-3 {
			displayContent = string(runes[:contentWidth-3]) + "..."
		}
	}

	return prefix + " " + contentStyle.Render(displayContent)
}

// submitTask creates a new task and starts execution in the background.
func (m *model) submitTask() tea.Cmd {
	taskDesc := strings.TrimSpace(m.input.Value())
	m.input.SetValue("")
	m.taskRunning = true
	m.commandMode = true
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

	m.publisherCh = make(chan *messaging.MessagePublisher, 1)
	return tea.Batch(
		executeTaskCmd(taskDesc, task, m.assistant, m.taskManager, m.dataManager, m.eventCh, m.publisherCh),
		listenForEvents(m.eventCh),
		listenForPublisher(m.publisherCh),
		tickCmd(),
	)
}

// submitFollowUp sends a follow-up message to an existing task.
func (m *model) submitFollowUp(message string) tea.Cmd {
	m.input.SetValue("")
	m.taskRunning = true
	m.commandMode = true
	m.errMsg = ""

	m.logEntries = append(m.logEntries, logEntry{
		timestamp: time.Now(),
		eventType: "status",
		content:   message,
	})
	m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])

	m.publisherCh = make(chan *messaging.MessagePublisher, 1)
	return tea.Batch(
		executeFollowUpCmd(message, m.currentTask, m.assistant, m.dataManager, m.eventCh, m.publisherCh),
		listenForEvents(m.eventCh),
		listenForPublisher(m.publisherCh),
		tickCmd(),
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
	publisherCh chan *messaging.MessagePublisher,
) tea.Cmd {
	return func() tea.Msg {
		dispatcher := messaging.NewMessageDispatcher(100)
		defer dispatcher.Shutdown()

		consumer := &tuiEventConsumer{ch: eventCh}
		dispatcher.RegisterConsumer(consumer)

		ca.IntegrateMessaging(dispatcher)

		// Send publisher to TUI so it can respond to authorization dialogs
		publisher := messaging.NewMessagePublisher(dispatcher)
		select {
		case publisherCh <- publisher:
		default:
		}

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
	publisherCh chan *messaging.MessagePublisher,
) tea.Cmd {
	return func() tea.Msg {
		dispatcher := messaging.NewMessageDispatcher(100)
		defer dispatcher.Shutdown()

		consumer := &tuiEventConsumer{ch: eventCh}
		dispatcher.RegisterConsumer(consumer)

		ca.IntegrateMessaging(dispatcher)

		// Send publisher to TUI so it can respond to authorization dialogs
		publisher := messaging.NewMessagePublisher(dispatcher)
		select {
		case publisherCh <- publisher:
		default:
		}

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

// confirmDialog styles
var (
	confirmBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("240")).
				Padding(0, 2)

	confirmToolStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("214")).
				Bold(true)

	confirmDetailStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("252"))

	confirmButtonFocused = lipgloss.NewStyle().
				Foreground(lipgloss.Color("0")).
				Background(lipgloss.Color("214")).
				Bold(true).
				Padding(0, 2)

	confirmButtonBlurred = lipgloss.NewStyle().
				Foreground(lipgloss.Color("244")).
				Padding(0, 2)

	confirmHelpStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("240"))
)

// taskCompleteDialog styles
var (
	taskCompleteBorderStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("36")). // 青绿色边框，表示成功
		Padding(0, 2)

	taskCompleteTitleStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("36")). // 青绿色
		Bold(true)

	taskCompleteMessageStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")). // 浅灰色详情文字
		MaxWidth(50)

	taskCompleteButtonFocused = lipgloss.NewStyle().
		Foreground(lipgloss.Color("0")).  // 黑字
		Background(lipgloss.Color("36")). // 青绿色底
		Bold(true).
		Padding(0, 4)

	taskCompleteButtonBlurred = lipgloss.NewStyle().
		Foreground(lipgloss.Color("244")). // 灰色未选中
		Padding(0, 4)
)

// parseConfirmQuestion extracts toolName and detail body from the question string.
func parseConfirmQuestion(question string) (toolName, body string) {
	q := strings.TrimSpace(question)
	// Remove markdown bold
	q = strings.ReplaceAll(q, "**", "")

	// Extract tool name from pattern: 工具 `name` or tool `name`
	toolName = "?"
	if idx := strings.Index(q, "工具 `"); idx >= 0 {
		start := idx + len("工具 `")
		if end := strings.Index(q[start:], "`"); end >= 0 {
			toolName = q[start : start+end]
		}
	} else if idx := strings.Index(q, "tool `"); idx >= 0 {
		start := idx + len("tool `")
		if end := strings.Index(q[start:], "`"); end >= 0 {
			toolName = q[start : start+end]
		}
	}

	// Extract body: after first blank line, before boilerplate explanatory text
	// Split by double newline to separate header / body / footer
	parts := strings.SplitN(q, "\n\n", 3)
	if len(parts) >= 2 {
		// parts[0] = header line, parts[1..] = body (may include boilerplate)
		body = strings.Join(parts[1:], "\n\n")
	} else if len(parts) == 1 {
		body = parts[0]
	}

	// Strip boilerplate suffixes
	boilerplates := []string{
		"此操作可能影响工作空间外的文件或系统环境。是否允许执行？",
		"是否允许执行？",
		"This operation may affect files or the system environment outside the workspace. Allow?",
	}
	for _, bp := range boilerplates {
		body = strings.ReplaceAll(body, "\n\n"+bp, "")
		body = strings.ReplaceAll(body, bp, "")
	}
	body = strings.TrimSpace(body)

	if body == "" {
		body = q
	}
	return toolName, body
}

// renderConfirmDialog renders the authorization confirmation overlay dialog.
func (m model) renderConfirmDialog() string {
	const maxDialogWidth = 64
	dialogWidth := maxDialogWidth
	if m.termWidth-4 < dialogWidth {
		dialogWidth = m.termWidth - 4
	}
	innerWidth := dialogWidth - 4

	toolName, body := parseConfirmQuestion(m.confirmDialog.question)

	// ── Tool name badge ──
	toolLine := confirmToolStyle.Render("⚡ " + toolName)

	// ── Command / detail ──
	detailWidth := innerWidth
	if detailWidth < 20 {
		detailWidth = 20
	}
	detail := wrapText(body, detailWidth)
	detail = confirmDetailStyle.Render(detail)

	// ── Buttons (3 options) ──
	renderBtn := func(label string, idx int) string {
		if m.confirmDialog.selectedOption == idx {
			return confirmButtonFocused.Render(label)
		}
		return confirmButtonBlurred.Render(label)
	}
	buttons := lipgloss.JoinHorizontal(lipgloss.Center,
		renderBtn("Allow", 0),
		"  ",
		renderBtn("Allow All", 1),
		"  ",
		renderBtn("Deny", 2),
	)

	// ── Help ──
	help := confirmHelpStyle.Render(langManager.GetText("ConfirmDialogHelp"))

	// ── Assemble with a horizontal separator between detail and buttons ──
	sep := lipgloss.NewStyle().
		Foreground(lipgloss.Color("237")).
		Width(innerWidth).
		Render(strings.Repeat("─", innerWidth))

	content := lipgloss.JoinVertical(lipgloss.Left,
		toolLine,
		"",
		detail,
		"",
		sep,
		lipgloss.NewStyle().Width(innerWidth).Align(lipgloss.Center).Render(buttons),
		help,
	)

	dialog := confirmBorderStyle.Width(dialogWidth).Render(content)

	return lipgloss.Place(m.termWidth, m.termHeight,
		lipgloss.Center, lipgloss.Center,
		dialog,
	)
}

// renderTaskCompleteDialog renders the task completion overlay dialog.
func (m model) renderTaskCompleteDialog() string {
	const maxDialogWidth = 40
	dialogWidth := maxDialogWidth
	if m.termWidth-4 < dialogWidth {
		dialogWidth = m.termWidth - 4
	}
	innerWidth := dialogWidth - 4

	// ── Title ──
	titleLine := taskCompleteTitleStyle.Render("Task Completed")

	// ── OK Button ──
	okBtn := taskCompleteButtonFocused.Render("OK")

	// ── Help text ──
	help := confirmHelpStyle.Render("Press ENTER or SPACE to close")

	// ── Separator ──
	sep := lipgloss.NewStyle().
		Foreground(lipgloss.Color("237")).
		Width(innerWidth).
		Render(strings.Repeat("─", innerWidth))

	// ── Assemble ──
	content := lipgloss.JoinVertical(lipgloss.Left,
		titleLine,
		"",
		sep,
		"",
		lipgloss.NewStyle().Width(innerWidth).Align(lipgloss.Center).Render(okBtn),
		"",
		help,
	)

	dialog := taskCompleteBorderStyle.Width(dialogWidth).Render(content)

	return lipgloss.Place(m.termWidth, m.termHeight,
		lipgloss.Center, lipgloss.Center,
		dialog,
	)
}

// renderHelpDialog renders the vim-like help overlay showing all command mode shortcuts.
func (m model) renderHelpDialog() string {
	const maxDialogWidth = 50
	dialogWidth := maxDialogWidth
	if m.termWidth-4 < dialogWidth {
		dialogWidth = m.termWidth - 4
	}
	innerWidth := dialogWidth - 4

	// ── Title ──
	titleStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("39")).Bold(true)
	titleLine := titleStyle.Render("?  " + langManager.GetText("HelpDialogTitle"))

	// ── Content ──
	contentStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252"))
	content := contentStyle.Render(langManager.GetText("HelpDialogContent"))

	// ── Separator ──
	sepStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("237")).
		Width(innerWidth)
	sep := sepStyle.Render(strings.Repeat("─", innerWidth))

	// ── Dismiss hint ──
	hintStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240"))
	hint := hintStyle.Render("Press any key to dismiss")

	// ── Assemble ──
	dialogContent := lipgloss.JoinVertical(lipgloss.Left,
		titleLine,
		"",
		content,
		"",
		sep,
		"",
		hint,
	)

	dialogStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("39")).
		Padding(1, 2)

	dialog := dialogStyle.Width(dialogWidth).Render(dialogContent)

	return lipgloss.Place(m.termWidth, m.termHeight,
		lipgloss.Center, lipgloss.Center,
		dialog,
	)
}

// wrapText wraps text to a maximum width, preserving line breaks.
func wrapText(text string, maxWidth int) string {
	if maxWidth <= 0 {
		return text
	}
	lines := strings.Split(text, "\n")
	var wrapped []string
	for _, line := range lines {
		if line == "" {
			wrapped = append(wrapped, "")
			continue
		}
		for len(line) > maxWidth {
			wrapped = append(wrapped, line[:maxWidth])
			line = line[maxWidth:]
		}
		if len(line) > 0 {
			wrapped = append(wrapped, line)
		}
	}
	return strings.Join(wrapped, "\n")
}

// renderToolEntry renders a logEntry using the new tool rendering pipeline.
func renderToolEntry(entry logEntry, maxWidth int) string {
	if entry.toolEntry == nil {
		return formatLogEntry(entry, maxWidth)
	}

	contentWidth := maxWidth - 4
	if contentWidth < 30 {
		contentWidth = 30
	}
	return tui.RenderToolLine(entry.toolEntry, nil, contentWidth)
}

// getToolCallIDFromEventContent extracts tool_call_id from event content.
func getToolCallIDFromEventContent(content interface{}) string {
	if m, ok := content.(map[string]interface{}); ok {
		if id, ok := m["tool_call_id"]; ok {
			if idStr, ok := id.(string); ok {
				return idStr
			}
		}
	}
	return ""
}

// getToolNameFromEventContent extracts tool_name from event content.
func getToolNameFromEventContent(content interface{}) string {
	if m, ok := content.(map[string]interface{}); ok {
		if name, ok := m["tool_name"]; ok {
			if nameStr, ok := name.(string); ok {
				return nameStr
			}
		}
	}
	return ""
}

// findRunningEntryByName finds the most recently-added running entry with the
// given tool name in the toolCallEntries map. Returns the call ID and the entry.
func findRunningEntryByName(entries map[string]*tui.ToolEntry, toolName string) (string, *tui.ToolEntry) {
	for id, entry := range entries {
		if entry.Call.Name == toolName && entry.Status == tui.ToolStatusRunning {
			return id, entry
		}
	}
	return "", nil
}

// getResultFromEventContent extracts the result string from event content.
func getResultFromEventContent(content interface{}) string {
	if m, ok := content.(map[string]interface{}); ok {
		if result, ok := m["result"]; ok {
			if resultStr, ok := result.(string); ok {
				return resultStr
			}
		}
	}
	return fmt.Sprintf("%v", content)
}

// findLogEntryByToolCallID finds the index of a log entry with the given tool_call_id.
func findLogEntryByToolCallID(entries []logEntry, callID string) int {
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].toolCallID == callID {
			return i
		}
	}
	return -1
}

// updateActiveAnim checks if there are any running tool entries and updates the flag.
func (m *model) updateActiveAnim() {
	for _, te := range m.toolCallEntries {
		if te.Status == tui.ToolStatusRunning {
			m.activeAnim = true
			return
		}
	}
	m.activeAnim = false
}

// renderToolEntryWithAnim renders a running tool entry using the provided animation.
func renderToolEntryWithAnim(entry logEntry, maxWidth int, anim *tui.Anim) string {
	if entry.toolEntry == nil {
		return formatLogEntry(entry, maxWidth)
	}
	params := entry.toolEntry.Call.Summary
	if params == "" {
		params = entry.executionSummary
	}
	return tui.RenderPending(entry.toolEntry.Call.Name, params, anim)
}

// extractDiffFromResult attempts to parse a JSON result string and extract the "diff" field.
func extractDiffFromResult(result string) string {
	// Quick check: does it look like JSON with a "diff" field?
	if !strings.Contains(result, `"diff"`) {
		return ""
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		return ""
	}
	if diff, ok := parsed["diff"].(string); ok && diff != "" {
		return diff
	}
	return ""
}

// renderDiff renders a unified diff string with ANSI color styling.
func renderDiff(entry *logEntry) string {
	var prefix string
	icon := tui.IconSuccess
	if entry.isToolRunning {
		icon = tui.IconPending
	}
	prefix = icon + " " + tui.RenderToolName(entry.toolName)
	if entry.executionSummary != "" {
		prefix += " " + tui.ParamMain.Render("· "+entry.executionSummary)
	}

	diffContent := tui.RenderDiffContent(entry.diffText, 100)

	return prefix + "\n" + diffContent
}

// renderHistoryPanel renders the history panel with single-line items and stable scrolling.
func (m model) renderHistoryPanel() string {
	panelWidth := m.termWidth - 4
	if panelWidth < 40 {
		panelWidth = 40
	}

	var b strings.Builder

	// ── Header: ◆ title │ filter │ counter ──
	{
		htStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
		hdStyle := lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("244"))

		var parts []string
		parts = append(parts, htStyle.Render("◆ "+langManager.GetText("HistoryTitle")))

		if m.historyFilter != "" {
			cur := lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Render("▌")
			parts = append(parts, hdStyle.Render("│")+" "+lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(m.historyFilter)+cur)
		} else {
			parts = append(parts, hdStyle.Render("│ "+langManager.GetText("HistoryFilterPlaceholder")))
		}
		parts = append(parts, hdStyle.Render(fmt.Sprintf("%d/%d", m.historyIndex+1, len(m.filteredItems))))

		hbStyle := lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, true, false).
			BorderForeground(lipgloss.Color("237")).
			Width(panelWidth).
			Padding(0, 1)

		b.WriteString(hbStyle.Render(strings.Join(parts, "  ")))
		b.WriteString("\n")
	}

	// ── Body: single-line items ──
	bodyHeight := m.termHeight - 8 // header(~2) + footer(~6)
	if bodyHeight < 4 {
		bodyHeight = 4
	}

	if len(m.filteredItems) == 0 {
		empty := lipgloss.NewStyle().
			Foreground(lipgloss.Color("244")).
			Width(panelWidth).
			Padding(2, 2).
			Render("  " + langManager.GetText("HistoryEmpty"))
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

		// Column layout: date(11) + title + count(Nm)
		const dateWidth = 11
		const countArea = 6
		const selMarker = 2
		const spacing = 2
		titleMaxWidth := panelWidth - dateWidth - countArea - selMarker - spacing - 2
		if titleMaxWidth < 15 {
			titleMaxWidth = 15
		}

		dateStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Faint(true)
		titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
		countStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Faint(true)
		selStyle := lipgloss.NewStyle().
			Background(lipgloss.Color("39")).
			Foreground(lipgloss.Color("15")).
			Width(panelWidth).
			Padding(0, 1)

		normalStyle := lipgloss.NewStyle().
			Width(panelWidth).
			Padding(0, 1)

		for i := scrollStart; i < end; i++ {
			item := m.filteredItems[i]
			selected := i == m.historyIndex

			// Title is pre-truncated to 30 chars; further truncate for narrow terminals
			displayTitle := item.Title
			titleRunes := []rune(displayTitle)
			if len(titleRunes) > titleMaxWidth {
				displayTitle = string(titleRunes[:titleMaxWidth-1]) + "…"
			}
			titlePadded := lipgloss.NewStyle().Width(titleMaxWidth).Render(displayTitle)

			dateStr := item.CreatedAt.Format("01-02 15:04")
			countStr := fmt.Sprintf("%dm", item.MessageCount)

			if selected {
				line := fmt.Sprintf("▐ %s  %s  %s", dateStr, titlePadded, countStr)
				b.WriteString(selStyle.Render(line))
			} else {
				line := fmt.Sprintf("  %s  %s  %s",
					dateStyle.Render(dateStr),
					titleStyle.Render(titlePadded),
					countStyle.Render(countStr))
				b.WriteString(normalStyle.Render(line))
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

