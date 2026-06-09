package tui

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codeactor/internal/app"
	"codeactor/internal/config"
	"codeactor/internal/datamanager"
	"codeactor/internal/dict"
	"codeactor/internal/http"
	"codeactor/internal/messaging"
	"codeactor/internal/tui/anim"
	"codeactor/internal/tui/common"
	"codeactor/internal/tui/components"
	"codeactor/internal/tui/layout"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
)

// Global Language Manager
var langManager *LanguageManager

// keywordCompletionConfig 关键词补全配置
type keywordCompletionConfig struct {
	enabled bool // 是否启用关键词补全
}

// Global styles — Claude Code-like minimalist aesthetic.
// Deprecated: prefer m.com.Styles for new code. These globals are kept for
// backward compatibility with the old View/Update rendering path.
var (
	bannerPadStyle = lipgloss.NewStyle().Padding(0, 1)

	// Deprecated: use m.com.Styles.PromptFocused / PromptBlurred instead.
	// promptFocusedStyle / promptBlurredStyle removed (unused).

	welcomePanelStyle = lipgloss.NewStyle().Padding(1, 2)
	welcomeLeftStyle  = lipgloss.NewStyle().Width(38)
	// Deprecated: use m.com.Styles.WelcomeTitle instead.
	// welcomeTitleStyle removed (unused).
	welcomeSubStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	// Deprecated: use m.com.Styles.WelcomeDim instead.
	// welcomeRightTitle removed (unused).
	welcomeTipStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	welcomeDimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)

	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("167")).Bold(true)
	// Deprecated: use m.com.Styles.InfoMsg / Footer instead.
	// infoMsgStyle and footerStyle removed (unused).

	// Message log styles
	logTimeStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Faint(true)
	logAIResStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	// User message styles - warm gold accents to visually distinguish from AI messages
	userPrefixStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true) // warm gold for "You" prefix
	logUserMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("222")).       // warm cream/beige for content
			BorderLeft(true).                         // thin left border accent
			BorderForeground(lipgloss.Color("214")).  // border in gold
			PaddingLeft(1)                            // space after border
	// User message textbox styles — simple read-only textbox with "You" label
	userMsgBoxBorderStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")) // subtle grey border to match separator style
	userMsgBoxTextStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("222")) // warm cream, same as logUserMsgStyle foreground
	logToolStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("228"))
	logResultStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	logStatusStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("36"))
	logErrorLogStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("167"))
	logSeparatorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	// Collapse/expand hint styles for long messages
	collapseHintLineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	collapseHintTextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	// Input panel styles — visually separate the input area from the message body
	inputPanelStyle = lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("39")).  // blue accent, matches prompt ❯ color
		BorderBackground(lipgloss.Color("236")). // blends with textarea background
		Padding(0, 1).
		MarginTop(1)

	// Deprecated: use m.com.Styles.InputPanelBlurred instead.
	// inputPanelBlurredStyle removed (unused).

	// Separator between message body and input panel — slightly brighter for clarity
	inputSeparatorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	diffHunkStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	diffAddStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	diffDelStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("167"))
	diffCtxStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	// Deprecated: use m.com.Styles.DiffNoNewline instead.
	// diffNoNewlineStyle removed (unused).

	// Tool status styles (running → finished transition)
	toolRunningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("228")) // gold — running
	toolDoneStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("114")) // green — success
	toolErrorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("167")) // red — error

	// LLM call styles
	llmCallStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("141")) // purple — LLM call start
	llmCallEndStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("111")) // blue — LLM call end

	// Mode-specific styles (vim-like edit / command modes) — harmonized with TUI 256-color palette
	commandPrefixStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true) // orange ":"
	commandModeBarStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("214")).
				Foreground(lipgloss.Color("0")).
				Bold(true)
)

// =============================================================================
// Airline-style status bar — color palette.
// Deprecated: prefer m.com.Styles for new code. Globals kept for old View path.
// =============================================================================

var (
	// Mode segment colors
	airlineColorNormalBg = compat.AdaptiveColor{Light: lipgloss.Color("24"), Dark: lipgloss.Color("24")}   // Blue
	airlineColorNormalFg = compat.AdaptiveColor{Light: lipgloss.Color("15"), Dark: lipgloss.Color("15")}   // White
	airlineColorRunBg    = compat.AdaptiveColor{Light: lipgloss.Color("70"), Dark: lipgloss.Color("76")}   // Green
	airlineColorRunFg    = compat.AdaptiveColor{Light: lipgloss.Color("15"), Dark: lipgloss.Color("15")}   // White
	airlineColorCmdBg    = compat.AdaptiveColor{Light: lipgloss.Color("202"), Dark: lipgloss.Color("214")} // Orange
	airlineColorCmdFg    = compat.AdaptiveColor{Light: lipgloss.Color("0"), Dark: lipgloss.Color("0")}     // Black

	// Info/secondary segment colors
	airlineColorInfoBg    = compat.AdaptiveColor{Light: lipgloss.Color("235"), Dark: lipgloss.Color("236")} // Dark gray
	airlineColorInfoFg    = compat.AdaptiveColor{Light: lipgloss.Color("252"), Dark: lipgloss.Color("250")} // Light gray
	airlineColorInfoAltBg = compat.AdaptiveColor{Light: lipgloss.Color("237"), Dark: lipgloss.Color("238")} // Slightly lighter dark gray
	airlineColorInfoAltFg = compat.AdaptiveColor{Light: lipgloss.Color("252"), Dark: lipgloss.Color("250")} // Light gray

	// Accent/highlight segment colors
	airlineColorAccentBg = compat.AdaptiveColor{Light: lipgloss.Color("166"), Dark: lipgloss.Color("166")} // Orange accent
	airlineColorAccentFg = compat.AdaptiveColor{Light: lipgloss.Color("15"), Dark: lipgloss.Color("15")}   // White
)

// Pre-defined segment styles
var (
	airlineNormalModeStyle = lipgloss.NewStyle().
				Background(airlineColorNormalBg).
				Foreground(airlineColorNormalFg).
				Bold(true).
				Padding(0, 1)

	airlineRunModeStyle = lipgloss.NewStyle().
				Background(airlineColorRunBg).
				Foreground(airlineColorRunFg).
				Bold(true).
				Padding(0, 1)

	airlineCommandModeStyle = lipgloss.NewStyle().
				Background(airlineColorCmdBg).
				Foreground(airlineColorCmdFg).
				Bold(true).
				Padding(0, 1)

	airlineInfoStyle = lipgloss.NewStyle().
				Background(airlineColorInfoBg).
				Foreground(airlineColorInfoFg).
				Padding(0, 1)

	airlineInfoAltStyle = lipgloss.NewStyle().
				Background(airlineColorInfoAltBg).
				Foreground(airlineColorInfoAltFg).
				Padding(0, 1)

	airlineAccentStyle = lipgloss.NewStyle().
				Background(airlineColorAccentBg).
				Foreground(airlineColorAccentFg).
				Padding(0, 1)

	airlineFillerStyle = lipgloss.NewStyle().
				Background(airlineColorInfoBg).
				Foreground(airlineColorInfoFg)
)

// CompactData carries context compression statistics.
type CompactData struct {
	OriginalTokens   int
	CompressedTokens int
	Ratio            float64 // 0-100 percentage
	Stats            string  // compression stats description
}

// logEntry represents a single message in the TUI log area.
type logEntry struct {
	timestamp        time.Time
	eventType        string
	from             string
	content          string
	prefix           string // indentation prefix for sub-agent messages (e.g., "  │ ")
	toolName         string
	toolCallID       string // tool_call_id for matching start/result events
	isToolRunning    bool   // true when awaiting result
	executionSummary string // short summary extracted from arguments (file path, command, etc.)
	resultBrief      string // brief result description (e.g., "120 lines", "modified")
	diffText         string // unified diff content for file edit results
	renderedCache    map[int]string // width-keyed cache: key=width, value=rendered content
	collapsed        bool           // true if content is currently folded (>15 lines)

	compactData *CompactData

	// Tool entry for new-style rendering (non-nil for tool events)
	toolEntry *ToolEntry
}

// getCachedRender returns the cached render for the given width.
func (e *logEntry) getCachedRender(width int) (string, bool) {
	if e.renderedCache == nil {
		return "", false
	}
	cached, ok := e.renderedCache[width]
	return cached, ok
}

// setCachedRender stores the rendered content for the given width.
func (e *logEntry) setCachedRender(content string, width int) {
	if e.renderedCache == nil {
		e.renderedCache = make(map[int]string)
	}
	e.renderedCache[width] = content
}

// clearRenderCache clears all cached renders.
func (e *logEntry) clearRenderCache() {
	e.renderedCache = nil
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

// All dialogs are now managed via the DialogStack (m.dialogStack).
// See components/ for dialog implementations:
//   - ConfirmDialog      → authorization confirmation
//   - QuitConfirmDialog   → quit / cancel task confirmation
//   - TaskCompleteDialog  → task completion notification
//   - HelpDialog          → command mode help
//   - HistoryDialog       → history browsing (placeholder)

// tuiEventConsumer routes MessageEvents to a Go channel consumed by the tea program.
type tuiEventConsumer struct {
	ch chan *messaging.MessageEvent
}

func (c *tuiEventConsumer) Consume(event *messaging.MessageEvent) error {
	select {
	case c.ch <- event:
	default:
		// Drop event if channel is full to avoid blocking the task.
		// Log a warning so the user / developer knows events were lost.
		slog.Warn("TUI 事件通道已满，丢弃事件", "component", "tui-model", "event_type", event.Type, "event_from", event.From)
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

// visibleEntryIndices 返回当前视口中可见的logEntry索引范围 [start, end]。
// 通过遍历contentParts累加行数来确定可见范围。
// 如果contentParts和logEntries长度不一致，使用较短的长度。
// 返回的索引是logEntries中的索引，与contentParts一一对应。
func (m *model) visibleEntryIndices() (start, end int) {
	if len(m.logEntries) == 0 || m.termHeight <= 0 {
		return 0, 0
	}

	// 计算viewport高度
	footerHeight := m.computeFooterHeight()
	vpHeight := m.termHeight - footerHeight
	if vpHeight < 3 {
		vpHeight = 3
	}

	// 获取viewport当前的YOffset（滚动位置）
	yOffset := m.viewport.YOffset()

	// 使用contentParts的行数信息来确定可见范围
	// 注意：contentParts和logEntries可能长度不一致（增量追加过程中），使用较短的长度
	partCount := len(m.contentParts)
	if len(m.logEntries) < partCount {
		partCount = len(m.logEntries)
	}

	if partCount == 0 {
		return 0, 0
	}

	currentLine := 0
	start = -1
	end = -1

	for i := 0; i < partCount; i++ {
		lineCount := strings.Count(m.contentParts[i], "\n") + 1
		partStart := currentLine
		partEnd := currentLine + lineCount

		// 检查这个部分是否与可见区域 [yOffset, yOffset+vpHeight) 有重叠
		if partEnd > yOffset && partStart < yOffset+vpHeight {
			if start == -1 {
				start = i
			}
			end = i
		}

		currentLine += lineCount
	}

	if start == -1 {
		return 0, 0
	}
	return start, end
}

// TUI Model
type model struct {
	// ── 新架构：共享上下文 ──
	com *common.Common // 共享样式、配置、助手引用

	// External dependencies
	assistant   *app.CodeActor
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
	taskRunning   bool
	taskCancelled bool // 标记任务是否由用户主动取消
	currentTask   *http.Task
	eventCh       chan *messaging.MessageEvent

	// Standard state
	termWidth   int
	termHeight  int
	quitting    bool
	errMsg      string
	infoMsg     string
	currentLang Language
	projectDir  string

	publisher   *messaging.MessagePublisher
	publisherCh chan *messaging.MessagePublisher

	// Command mode (vim-like): hidden input, ":" prefix, different bg.
	// Toggled with Esc (edit→cmd) and i (cmd→edit). Auto-enabled on task submit.
	commandMode   bool
	commandBuffer string // hidden command input buffer in command mode
	lastKey       string // tracks previous key for multi-key sequences (gg)

	// Skill autocomplete in edit mode (inline, not popup)
	skillAutoComplete  bool     // whether autocomplete suggestions are shown
	skillSuggestions   []string // matching skill names based on current query
	skillSuggestionIdx int      // currently selected suggestion index

	// Keyword autocomplete in edit mode (triggered by Tab key)
	keywordAutoComplete  bool                 // whether keyword autocomplete suggestions are shown
	keywordSuggestions   []string             // matching keyword suggestions based on current word at cursor
	keywordSuggestionIdx int                  // currently selected suggestion index
	keywordDict          *dict.CompletionDict // keyword dictionary for autocomplete
	keywordCompletionCfg keywordCompletionConfig // 关键词补全配置

	// Tool call state tracking: tool_call_id → ToolEntry
	toolCallEntries map[string]*ToolEntry

	// Active LLM calls: agent_name → log entry index for matching start/end
	llmCallActiveEntries map[string]int

	// Current LLM model being used (extracted from model_info events)
	currentModel string

	// Current agent name (set when task is running, cleared when finished)
	currentAgent string

	// Current provider name for status bar display
	currentProvider string
	// pendingModelTarget 记录 :model 命令当前正在配置的目标 agent（空=全局默认）
	pendingModelTarget string

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
	animFrame   int           // frame counter for throttled viewport rebuilds

	// History mode
	historyMode     bool
	historyItems    []datamanager.TaskHistoryItem
	historyCursor   int
	historyPage     int // 当前页码，0-indexed
	historyPageSize int // 每页条数，固定20
	historyLoading  bool

	// pendingDeleteTaskID tracks the task to delete when the delete confirmation
	// dialog (a QuitConfirmDialog) is on the DialogStack.
	pendingDeleteTaskID string

	// ── 新组件（TUI 改进） ──
	dialogStack  *components.DialogStack   // 栈式弹窗管理器
	animManager  *anim.Manager             // 可见性感知动画管理器
	layoutEngine *layout.LayoutEngine      // 动态布局引擎
	mouseHandler *components.ClickDetector // 鼠标事件处理器

	// ── 预创建的渲染样式（避免循环内重复创建） ──
	skillSuggestionStyle   lipgloss.Style // 普通技能建议样式
	skillHighlightStyle    lipgloss.Style // 高亮技能建议样式
	skillHintStyle         lipgloss.Style // 技能建议提示行样式
	keywordSuggestionStyle lipgloss.Style // 普通关键词建议样式
	keywordHighlightStyle  lipgloss.Style // 高亮关键词建议样式

	// ── 补全防抖相关 ──
	pendingAutocomplete bool        // 是否有待处理的补全请求
	debounceTimer       *time.Timer // 防抖定时器
	snapshotText        string      // 补全计算时的输入文本快照
	snapshotCursor      int         // 补全计算时的光标位置快照

	// ── 补全结果缓存 - key: (光标前的单词, 是否有/) value: 补全结果 ──
	// 使用细粒度缓存键，在快速输入时缓存命中率更高
	autocompleteCache map[autocompleteCacheKey]*AutocompleteResult

	// ── Footer 渲染缓存（在 Update() 中维护，View() 直接使用）──
	cachedTokenDashboard string // 缓存的 token 面板渲染结果
	tokenDashboardValid  bool   // tokenDashboard 缓存是否有效
	cachedStatusBar      string // 缓存的状态栏渲染结果
	statusBarValid       bool   // statusBar 缓存是否有效

	// ── 性能优化标志 ──
	tickStarted   bool // tick 循环是否已启动
	viewportDirty bool // 标记 viewport 内容是否需要重建

	// ── Glamour renderer 缓存 (key=width) ──
	glamourRenderers map[int]*glamour.TermRenderer

	// ── Footer 缓存 ──
	cachedFooterHeight int
	cachedSeparator    string
	footerHeightValid  bool

	// ── Viewport 渲染缓存 ──
	// viewport.View() 内部对每帧做 strings.Split 裁剪大段内容，
	// 在快速输入时这是卡顿的主要原因。缓存避免重复的 split/join。
	cachedViewportView  string
	viewportViewValid   bool
	prevViewportYOffset int
	prevViewportHeight  int

	// ── 增量内容构建相关 ──
	contentParts      []string // 每个logEntry的已渲染内容，与logEntries一一对应
	contentPartsDirty bool     // 是否需要完全重建contentParts
	prevViewportWidth int      // 上次渲染时的viewport宽度，用于检测resize
}

// autocompleteCacheKey is a fine-grained cache key for autocomplete results.
// Using (word, hasSlash) instead of full text provides better hit rates
// during fast typing since the word changes less frequently than the full text.
type autocompleteCacheKey struct {
	word     string // 光标前的单词
	hasSlash bool   // 是否有 / 字符
}

// AutocompleteResult holds the cached result of autocomplete computation.
type AutocompleteResult struct {
	skillSuggestions     []string
	skillSuggestionIdx   int
	keywordSuggestions   []string
	keywordSuggestionIdx int
}

func initialModel(preloadedTaskContent string, ca *app.CodeActor, tm *http.TaskManager, dm *datamanager.DataManager, useDarkStyle bool, cfg *config.Config, termWidth, termHeight int) model {
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
	ti.SetVirtualCursor(true) // 启用虚拟光标，显示编辑位置

	// Text style (lipgloss v2)
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))

	// Edit base style (lipgloss v2)
	editBaseStyle := lipgloss.NewStyle().Background(lipgloss.Color("235"))

	// Focused state styles
	focusedStyle := textarea.StyleState{
		Base:        editBaseStyle,
		Text:        textStyle,
		Prompt:      lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true).Background(lipgloss.Color("235")),
		CursorLine:  lipgloss.NewStyle().Background(lipgloss.Color("237")),
		Placeholder: lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(lipgloss.Color("235")),
	}

	// Blurred state styles
	blurredStyle := textarea.StyleState{
		Base:        editBaseStyle,
		Text:        textStyle,
		Prompt:      lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Background(lipgloss.Color("235")),
		CursorLine:  lipgloss.NewStyle().Background(lipgloss.Color("237")),
		Placeholder: lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Background(lipgloss.Color("235")),
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

	// Create viewport with real terminal dimensions to eliminate startup flash
	vp := viewport.New(viewport.WithWidth(termWidth), viewport.WithHeight(termHeight))
	vp.Style = lipgloss.NewStyle().Padding(0, 1)

	// Create glamour markdown renderer with explicit style to avoid
	// terminal background-color queries leaking into input.
	glamourStyle := "dark"
	if !useDarkStyle {
		glamourStyle = "light"
	}
	glamourRenderer, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(glamourStyle),
		glamour.WithWordWrap(termWidth-10), // 使用真实宽度
	)
	if err != nil {
		// Fallback: glamourRenderer will be nil, and we'll use plain text
		glamourRenderer = nil
	}

	// 初始化新组件
	ds := components.NewDialogStack()
	am := anim.NewManager()
	le := layout.NewLayoutEngine()
	md := components.NewClickDetector()

	// 注册默认的 tool_call 动画
	am.Register("tool_call_anim", 10) // 10 FPS

	// Initialize keyword completion dictionary (conditionally based on config)
	var keywordDict *dict.CompletionDict
	var completionEnabled bool

	// 检查配置：如果 keywords.disable_completion = true，则禁用补全（向后兼容）
	// 默认行为：启用补全
	if cfg != nil && cfg.Keywords.DisableCompletion {
		completionEnabled = false
	} else {
		// 默认启用补全，创建词典
		completionEnabled = true
		homeDir, _ := os.UserHomeDir()
		userDictPath := filepath.Join(homeDir, ".codeactor", "keywords.txt")
		projectDictPath := filepath.Join(projectDir, ".codeactor", "keywords.txt")

		// Create dict with sources (will auto-load existing files)
		keywordDict = dict.NewCompletionDict("autocomplete", []string{userDictPath, projectDictPath})

		// Add builtin default keywords
		keywordDict.AddWords(dict.DefaultKeywords())
	}

	// Get current provider info for status bar display
	initProvider := ""
	initModel := ""
	if ca != nil && ca.GetClient() != nil {
		prov, model := ca.GetClient().GetCurrentProviderInfo()
		initProvider = prov
		initModel = model
	}

	// —— 创建共享组件 ——
	styles := common.NewStyles()
	com := common.NewCommon(styles, cfg, ca, projectDir, useDarkStyle)

	return model{
		com: com,

		assistant:          ca,
		taskManager:        tm,
		dataManager:        dm,
		input:              ti,
		projectDir:         projectDir,
		infoMsg:            langManager.GetText("InfoMessage"),
		currentLang:        langManager.currentLang,
		eventCh:            make(chan *messaging.MessageEvent, 1000),
		logEntries:         make([]logEntry, 0),
			llmCallActiveEntries: make(map[string]int),
			viewport:           vp,
		contentCache:       &strings.Builder{},
		glamourRenderer:    glamourRenderer,
		useDarkStyle:       useDarkStyle,
		toolCallEntries:    make(map[string]*ToolEntry),
		anim: NewAnim(10),
		tokenUsagePerAgent: make(map[string]*AgentTokenUsage),

		// 新组件
		dialogStack:  ds,
		animManager:  am,
		layoutEngine: le,
		mouseHandler: md,
		keywordDict:  keywordDict,
		keywordCompletionCfg: keywordCompletionConfig{enabled: completionEnabled},

		// will be populated on first task via model_info event
		currentProvider:    initProvider,
		currentModel:       initModel,

		// 预创建的渲染样式（避免循环内重复创建）
		skillSuggestionStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("245")).
			PaddingLeft(4),
		skillHighlightStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("39")).
			Bold(true).
			PaddingLeft(4),
		skillHintStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")).
			PaddingLeft(4),
		keywordSuggestionStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("243")).
			PaddingLeft(1),
		keywordHighlightStyle: lipgloss.NewStyle().
			Background(lipgloss.Color("57")).
			Foreground(lipgloss.Color("15")).
			PaddingLeft(1),

		// 补全结果缓存 - 使用细粒度缓存键
		autocompleteCache: make(map[autocompleteCacheKey]*AutocompleteResult),

		// 性能优化标志
		tickStarted:   false,
		viewportDirty: false,

		// ── 增量内容构建相关 (Step 2) ──
		contentParts:      make([]string, 0),
		contentPartsDirty: false,
		prevViewportWidth: 0,

		// 使用传入的终端尺寸初始化
		termWidth:   termWidth,
		termHeight:  termHeight,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(
		listenForEvents(m.eventCh),
		// 延迟启动 tick 循环：初始时不启动 tick，
		// 在首次收到 WindowSizeMsg 后才真正启动 tickCmd()
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
