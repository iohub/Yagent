package tui

import (
	"os"
	"strings"
	"time"

	"codeactor/internal/app"
	"codeactor/internal/datamanager"
	"codeactor/internal/http"
	"codeactor/internal/messaging"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
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
	welcomeDimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)

	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("167")).Bold(true)
	infoMsgStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	footerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	// Message log styles
	logTimeStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Faint(true)
	logAIResStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	logToolStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("228"))
	logResultStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	logStatusStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("36"))
	logErrorLogStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("167"))
	logSeparatorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("237"))
	diffHunkStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	diffAddStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	diffDelStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("167"))
	diffCtxStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	diffNoNewlineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	// Tool status styles (running → finished transition)
	toolRunningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("228")) // gold — running
	toolDoneStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("114")) // green — success
	toolErrorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("167")) // red — error

	// Mode-specific styles (vim-like edit / command modes) — harmonized with TUI 256-color palette
	commandPrefixStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true) // orange ":"
	commandModeBarStyle = lipgloss.NewStyle().
		Background(lipgloss.Color("214")).
		Foreground(lipgloss.Color("0")).
		Bold(true)
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
	toolEntry *ToolEntry
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
	toolName       string // 工具名，如 "run_bash"
	reason         string // 原因/命令
	requestID      string
	selectedOption int // 0=Allow, 1=Allow Tool(session), 2=Allow Session All, 3=Allow Project All, 4=Deny
}

// taskCompleteDialog holds the state of the task completion overlay dialog.
type taskCompleteDialog struct {
	open    bool
	message string
}

// confirmQuitDialog holds the state of the quit confirmation dialog.
type confirmQuitDialog struct {
	open           bool
	selectedOption int // 0=Confirm, 1=Cancel
}

// confirmCancelDialog holds the state of the cancel task confirmation dialog.
type confirmCancelDialog struct {
	open           bool
	selectedOption int // 0=Confirm, 1=Cancel
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

// AgentTokenUsage tracks token consumption for a single agent.
type AgentTokenUsage struct {
	AgentName                string
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
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

	

	// Authorization confirmation dialog
	confirmDialog      confirmDialog
	taskCompleteDialog taskCompleteDialog
	// Quit / Cancel confirmation dialogs
	confirmQuitDialog   confirmQuitDialog
	confirmCancelDialog confirmCancelDialog
	publisher           *messaging.MessagePublisher
	publisherCh         chan *messaging.MessagePublisher

	// Command mode (vim-like): hidden input, ":" prefix, different bg.
	// Toggled with Esc (edit→cmd) and i (cmd→edit). Auto-enabled on task submit.
	commandMode    bool
	commandBuffer  string // hidden command input buffer in command mode
	lastKey        string // tracks previous key for multi-key sequences (gg)
	showHelpDialog bool   // "?" help overlay in command mode

	// Skill autocomplete in edit mode (inline, not popup)
	skillAutoComplete  bool       // whether autocomplete suggestions are shown
	skillSuggestions   []string   // matching skill names based on current query
	skillSuggestionIdx int        // currently selected suggestion index

	// Tool call state tracking: tool_call_id → ToolEntry
	toolCallEntries map[string]*ToolEntry

	// Current LLM model being used (extracted from model_info events)
	currentModel string

	// Token consumption tracking
	inputTokens              int64 // accumulated input tokens
	outputTokens             int64 // accumulated output tokens
	cacheCreationInputTokens int64 // accumulated cache creation input tokens
	cacheReadInputTokens     int64 // accumulated cache read (hit) tokens

	// Per-agent token tracking
	tokenUsagePerAgent map[string]*AgentTokenUsage

	// Animation state for running tools
	anim       *Anim
	activeAnim bool // true when there are running tool entries
	animFrame  int  // frame counter for throttled viewport rebuilds

	// History mode
	historyMode     bool
	historyItems    []datamanager.TaskHistoryItem
	historyCursor   int
	historyPage     int     // 当前页码，0-indexed
	historyPageSize int     // 每页条数，固定20
	historyLoading  bool

	
}

func initialModel(preloadedTaskContent string, ca *app.CodingAssistant, tm *http.TaskManager, dm *datamanager.DataManager, useDarkStyle bool) model {
	ti := textarea.New()

	// ── Editor input styles (harmonized with TUI 256-color palette) ──
	// Accent: 39 (blue, matches NameNormal/tool names)
	// Text: 252 (light gray, matches Body/AIResStyle)
	// Muted: 245 (gray, matches ContentLine/ParamKey)
	// Subtle bg: 236 (dark gray, barely visible on dark terminals)
	// Cursor line: 237 (matches SeparatorStyle)

	ti.Placeholder = langManager.GetText("TaskDescPlaceholder")
	ti.Focus()
	ti.CharLimit = 0
	ti.SetWidth(60)
	ti.SetHeight(3)
	ti.ShowLineNumbers = false

	// Text style (lipgloss v2)
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))

	// Edit base style (lipgloss v2)
	editBaseStyle := lipgloss.NewStyle().Background(lipgloss.Color("236"))

	// Focused state styles
	focusedStyle := textarea.StyleState{
		Base:          editBaseStyle,
		Text:          textStyle,
		Prompt:        lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true).Background(lipgloss.Color("236")),
		CursorLine:    lipgloss.NewStyle().Background(lipgloss.Color("237")),
		Placeholder:   lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(lipgloss.Color("236")),
	}

	// Blurred state styles
	blurredStyle := textarea.StyleState{
		Base:          editBaseStyle,
		Text:          textStyle,
		Prompt:        lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Background(lipgloss.Color("236")),
		CursorLine:    lipgloss.NewStyle().Background(lipgloss.Color("237")),
		Placeholder:   lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(lipgloss.Color("236")),
	}

	// Cursor style
	cursorStyle := textarea.CursorStyle{
		Color: lipgloss.Color("39"),
	}

	// Apply styles to textarea
	ti.SetStyles(textarea.Styles{
		Focused: focusedStyle,
		Blurred: blurredStyle,
		Cursor:  cursorStyle,
	})

	// Dynamic prompt: "❯ " on first line, "  " on continuation lines
	ti.SetPromptFunc(2, func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return "❯ "
		}
		return "  "
	})

	if preloadedTaskContent != "" {
		ti.SetValue(preloadedTaskContent)
	}

	projectDir, _ := os.Getwd()

	// Create viewport for scrollable message area (v1 lipgloss for bubbles compatibility)
	vp := viewport.New(viewport.WithWidth(80), viewport.WithHeight(10))
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
		toolCallEntries: make(map[string]*ToolEntry),
		anim:            NewAnim(10),
		tokenUsagePerAgent: make(map[string]*AgentTokenUsage),
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		tea.Raw(textarea.Blink()),
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
