package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"sync"

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

// ── Glamour 缓存辅助函数 ──

// glamourCacheKey 从内容生成缓存 key
func glamourCacheKey(content string) string {
	if len(content) == 0 {
		return ""
	}
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:8]) // 16 字符，冲突概率极低
}

// getGlamourCached 获取缓存的渲染结果
func (m *model) getGlamourCached(content string) (string, bool) {
	key := glamourCacheKey(content)
	m.glamourCacheMu.Lock()
	defer m.glamourCacheMu.Unlock()
	if r, ok := m.glamourCache[key]; ok {
		return r, true
	}
	return "", false
}

// putGlamourCached 缓存渲染结果（LRU 淘汰）
func (m *model) putGlamourCached(content, rendered string) {
	key := glamourCacheKey(content)
	if key == "" || rendered == "" {
		return
	}
	m.glamourCacheMu.Lock()
	defer m.glamourCacheMu.Unlock()

	// 已存在则更新
	if _, ok := m.glamourCache[key]; ok {
		m.glamourCache[key] = rendered
		return
	}

	// 插入新条目
	m.glamourCache[key] = rendered
	m.glamourLRU = append(m.glamourLRU, key)

	// LRU 淘汰
	for len(m.glamourLRU) > m.glamourCacheCap {
		oldest := m.glamourLRU[0]
		m.glamourLRU = m.glamourLRU[1:]
		delete(m.glamourCache, oldest)
	}
}

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
	welcomeDimStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("62")).Bold(true)

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
		BorderForeground(lipgloss.Color("62")).  // soft indigo accent, consistent design system color
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

	compactData *CompactData

	// Tool entry for new-style rendering (non-nil for tool events)
	toolEntry *ToolEntry

	// isVerbose marks entries that contain operational details
	// (tool calls, LLM calls, internal operations).
	// These are always hidden from the main view and displayed
	// in the tool timeline panel instead.
	isVerbose bool
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

// AgentRunTokens 追踪当前 agent 本次运行的 token 消耗（非历史累计）
type AgentRunTokens struct {
	AgentName                string
	InputTokens              int64
	OutputTokens             int64
	CacheReadInputTokens     int64
	CacheCreationInputTokens int64
}

// visibleEntryIndices 返回当前视口中可见的logEntry索引范围 [start, end]。
// 使用前缀和数组 + 二分查找，O(log n) 定位可见范围。
// 返回的索引是logEntries中的索引，与contentParts一一对应。
func (m *model) visibleEntryIndices() (start, end int) {
	prefix := m.contentPartLinePrefix
	n := len(prefix) - 1 // number of parts
	if n <= 0 || m.termHeight <= 0 {
		return 0, -1
	}

	// 计算viewport高度
	footerHeight := m.computeFooterHeight()
	vpHeight := m.termHeight - footerHeight
	if vpHeight < 3 {
		vpHeight = 3
	}

	yOffset := m.viewport.YOffset()
	viewEnd := yOffset + vpHeight

	// 二分查找：找到第一个 partStart < viewEnd 的 part（即 end）
	// prefix[i] 是 part i 的起始行号，part i 占据 [prefix[i], prefix[i+1])
	// part i 与 [yOffset, viewEnd) 有重叠 ⟺ prefix[i+1] > yOffset && prefix[i] < viewEnd

	// 找 start：最小的 i 使得 prefix[i+1] > yOffset
	// 即 prefix[i+1] > yOffset，等价于在 prefix[1:] 中找第一个 > yOffset 的位置
	start = sort.Search(n, func(i int) bool {
		return prefix[i+1] > yOffset
	})

	// 找 end：最大的 i 使得 prefix[i] < viewEnd
	// 即在 prefix[0:n] 中找第一个 >= viewEnd 的位置，然后 -1
	endIdx := sort.Search(n, func(i int) bool {
		return prefix[i] >= viewEnd
	})
	end = endIdx - 1

	// 边界检查
	if start > end || start >= n {
		return 0, -1
	}
	if end >= n {
		end = n - 1
	}

	return start, end
}

// rebuildLinePrefix 完全重建前缀和数组。
// 在 contentParts 整体重建后调用。
func (m *model) rebuildLinePrefix() {
	n := len(m.contentParts)
	if n == 0 {
		m.contentPartLinePrefix = nil
		return
	}
	prefix := make([]int, n+1)
	prefix[0] = 0
	for i, part := range m.contentParts {
		prefix[i+1] = prefix[i] + strings.Count(part, "\n") + 1
	}
	m.contentPartLinePrefix = prefix
}

// appendLinePrefix 追加新增 part 的前缀和。
// oldLen 是追加前的 contentParts 长度。
func (m *model) appendLinePrefix(oldLen int) {
	newLen := len(m.contentParts)
	if newLen == 0 {
		m.contentPartLinePrefix = nil
		return
	}
	// 确保 prefix 长度足够
	prefix := m.contentPartLinePrefix
	if len(prefix) < oldLen+1 {
		// 前缀和不存在或过短，完全重建
		m.rebuildLinePrefix()
		return
	}
	// 扩展到 newLen+1
	if cap(prefix) >= newLen+1 {
		prefix = prefix[:newLen+1]
	} else {
		newPrefix := make([]int, newLen+1)
		copy(newPrefix, prefix)
		prefix = newPrefix
	}
	base := prefix[oldLen]
	for i := oldLen; i < newLen; i++ {
		prefix[i+1] = base + strings.Count(m.contentParts[i], "\n") + 1
		base = prefix[i+1]
	}
	m.contentPartLinePrefix = prefix
}

// updateLinePrefixEntry 更新单个 part 的前缀和。
// 当 part i 的行数可能变化时调用（如动画帧更新）。
// 如果行数不变则前缀和不变，否则从 i 开始重新计算后续值。
func (m *model) updateLinePrefixEntry(i int) {
	prefix := m.contentPartLinePrefix
	n := len(m.contentParts)
	if i < 0 || i >= n || len(prefix) < n+1 {
		m.rebuildLinePrefix()
		return
	}
	newLines := strings.Count(m.contentParts[i], "\n") + 1
	oldLines := prefix[i+1] - prefix[i]
	if newLines == oldLines {
		return // 行数不变，前缀和无需更新
	}
	// 差值传播到后续所有元素
	delta := newLines - oldLines
	for j := i + 1; j <= n; j++ {
		prefix[j] += delta
	}
}

// setEntryContent 安全更新 contentParts[i] 并自动维护前缀和。
func (m *model) setEntryContent(i int, content string) {
	if i < 0 || i >= len(m.contentParts) {
		return
	}
	m.contentParts[i] = content
	m.updateLinePrefixEntry(i)
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
	taskStarted   bool   // 标记用户是否已经提交过任务（用于控制输入面板边框高亮）
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

	// Timeline entries for the tool timeline panel (replaces verbose mode)
	timelineEntries  []*TimelineEntry // 有序的时间线条目
	timelineExpanded bool             // 是否展开显示全部历史
	timelineCache    string           // 缓存的渲染结果
	timelineCacheKey string           // 缓存键 (len + expanded + width)

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

	// Token dashboard collapse/expand control
	tokenDashboardCollapsed bool // 默认 true（折叠），按 alt+t 切换

	// Current agent run-specific token tracking (reset on agent switch)
	currentAgentRunTokens AgentRunTokens

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
	historyStyles   *historyStyles // 预计算的历史列表样式

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

	// ── Glamour 渲染缓存（LRU） ──
	glamourCache    map[string]string // key=content短哈希(16), value=渲染结果
	glamourLRU      []string          // LRU 顺序列表
	glamourCacheMu  sync.Mutex        // 因可能在 batch goroutine 中访问
	glamourCacheCap int               // 缓存容量，初始化时设为 32

	// ── Viewport 渲染缓存 ──
	// viewport.View() 内部对每帧做 strings.Split 裁剪大段内容，
	// 在快速输入时这是卡顿的主要原因。缓存避免重复的 split/join。
	cachedViewportView  string
	viewportViewValid   bool
	prevViewportYOffset int
	prevViewportHeight  int

	// ── 增量内容构建相关 ──
	contentParts      []string            // 每个logEntry的已渲染内容，与logEntries一一对应
	dirtyEntryIndices map[int]struct{}    // 需要重新渲染的条目索引（细粒度脏标记）
	needFullRebuild   bool                // 需要完全重建（resize、对话重置）
	prevViewportWidth int                 // 上次渲染时的viewport宽度，用于检测resize

	// ── 前缀和数组（用于 visibleEntryIndices 二分查找）──
	// contentPartLinePrefix[i] = 第 i 个 part 的起始行号（从 0 开始）。
	// 长度 = len(contentParts)+1，最后一个元素是总行数。
	// 由 rebuildLinePrefix / appendLinePrefix 维护。
	contentPartLinePrefix []int

	// ── Timeline 全屏模式状态 ──
	timelineFullscreenMode   bool              // 是否处于全屏时间线模式
	timelineFullscreenCursor int               // 全屏模式下当前选中的条目索引
	timelineDetailVP         *viewport.Model   // 全屏模式下右侧详情 viewport
	timelineFullscreenFocus  string            // 全屏模式焦点: "list" 或 "detail" (默认 "list")
	timelineDetailOffsets    []int             // 每个条目在拼接详情中的行偏移量

	// ── 可配置快捷键映射表 ──
	// editKeyMap 将用户配置的编辑模式快捷键映射为内部标准键名
	// key: 用户配置的按键, value: 内部标准键名
	editKeyMap map[string]string

	// cmdKeyMap 将用户配置的命令模式快捷键映射为内部标准键名
	// key: 用户配置的按键, value: 内部标准键名
	cmdKeyMap map[string]string
}

// autocompleteCacheKey is a fine-grained cache key for autocomplete results.
// Using (word, hasSlash) instead of full text provides better hit rates
// during fast typing since the word changes less frequently than the full text.
// Note: hasSlash now has word-boundary semantics — it indicates whether the
// last '/' is at a word boundary (start of text or preceded by whitespace).
type autocompleteCacheKey struct {
	word     string // 光标前的单词
	hasSlash bool   // 最后一个 / 是否在单词边界上（文本开头或前面是空白）
}

// AutocompleteResult holds the cached result of autocomplete computation.
type AutocompleteResult struct {
	skillSuggestions     []string
	skillSuggestionIdx   int
	keywordSuggestions   []string
	keywordSuggestionIdx int
}

// markEntryDirty 标记指定索引的条目需要重新渲染。
func (m *model) markEntryDirty(idx int) {
	if m.dirtyEntryIndices == nil {
		m.dirtyEntryIndices = make(map[int]struct{})
	}
	m.dirtyEntryIndices[idx] = struct{}{}
}

// markAllEntriesDirty 强制完全重建所有条目（如 resize、对话重置）。
func (m *model) markAllEntriesDirty() {
	m.needFullRebuild = true
	// 清除细粒度脏标记，因为全量重建会覆盖一切
	for k := range m.dirtyEntryIndices {
		delete(m.dirtyEntryIndices, k)
	}
}

// hasDirtyEntries 返回是否有待处理的脏标记。
func (m *model) hasDirtyEntries() bool {
	return m.needFullRebuild || len(m.dirtyEntryIndices) > 0
}

func initialModel(preloadedTaskContent string, ca *app.CodeActor, tm *http.TaskManager, dm *datamanager.DataManager, useDarkStyle bool, cfg *config.Config, termWidth, termHeight int) *model {
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

	// 创建快捷键映射表：将用户配置的按键映射为内部标准键名
	editKeyMap := make(map[string]string)
	cmdKeyMap := make(map[string]string)
	if cfg != nil && cfg.TUI.Keybindings.Edit.SubmitTask != "" {
		editKeyMap[cfg.TUI.Keybindings.Edit.SubmitTask] = "alt+s"
	}
	if cfg != nil && cfg.TUI.Keybindings.Edit.CommandMode != "" {
		editKeyMap[cfg.TUI.Keybindings.Edit.CommandMode] = "ctrl+e"
	}
	if cfg != nil && cfg.TUI.Keybindings.Edit.ToggleHelp != "" {
		editKeyMap[cfg.TUI.Keybindings.Edit.ToggleHelp] = "ctrl+h"
	}
	if cfg != nil && cfg.TUI.Keybindings.Edit.ToggleTimeline != "" {
		editKeyMap[cfg.TUI.Keybindings.Edit.ToggleTimeline] = "ctrl+l"
	}
	if cfg != nil && cfg.TUI.Keybindings.Edit.PageDown != "" {
		editKeyMap[cfg.TUI.Keybindings.Edit.PageDown] = "ctrl+f"
	}
	if cfg != nil && cfg.TUI.Keybindings.Edit.PageUp != "" {
		editKeyMap[cfg.TUI.Keybindings.Edit.PageUp] = "ctrl+b"
	}
	if cfg != nil && cfg.TUI.Keybindings.Edit.Quit != "" {
		editKeyMap[cfg.TUI.Keybindings.Edit.Quit] = "ctrl+c"
	}
	if cfg != nil && cfg.TUI.Keybindings.Edit.SwitchModel != "" {
		editKeyMap[cfg.TUI.Keybindings.Edit.SwitchModel] = "alt+m"
	}

	if cfg != nil && cfg.TUI.Keybindings.Command.ScrollDown != "" {
		cmdKeyMap[cfg.TUI.Keybindings.Command.ScrollDown] = "j"
	}
	if cfg != nil && cfg.TUI.Keybindings.Command.ScrollUp != "" {
		cmdKeyMap[cfg.TUI.Keybindings.Command.ScrollUp] = "k"
	}
	if cfg != nil && cfg.TUI.Keybindings.Command.PageDown != "" {
		cmdKeyMap[cfg.TUI.Keybindings.Command.PageDown] = "f"
	}
	if cfg != nil && cfg.TUI.Keybindings.Command.PageUp != "" {
		cmdKeyMap[cfg.TUI.Keybindings.Command.PageUp] = "b"
	}
	if cfg != nil && cfg.TUI.Keybindings.Command.EditMode != "" {
		cmdKeyMap[cfg.TUI.Keybindings.Command.EditMode] = "i"
	}
	if cfg != nil && cfg.TUI.Keybindings.Command.CmdToggleHelp != "" {
		cmdKeyMap[cfg.TUI.Keybindings.Command.CmdToggleHelp] = "?"
	}
	if cfg != nil && cfg.TUI.Keybindings.Command.ToggleTokenPanel != "" {
		cmdKeyMap[cfg.TUI.Keybindings.Command.ToggleTokenPanel] = "alt+t"
	}
	if cfg != nil && cfg.TUI.Keybindings.Command.SwitchModel != "" {
		cmdKeyMap[cfg.TUI.Keybindings.Command.SwitchModel] = "alt+m"
	}
	if cfg != nil && cfg.TUI.Keybindings.Command.Quit != "" {
		cmdKeyMap[cfg.TUI.Keybindings.Command.Quit] = "ctrl+c"
	}

return &model{
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
		tokenDashboardCollapsed: true, // 默认折叠

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

		// Performance optimization flags
		tickStarted:   false,
		viewportDirty: false,

		// Timeline initialization
		timelineEntries:  make([]*TimelineEntry, 0),
		timelineExpanded: false,
		timelineFullscreenFocus: "list",
		timelineDetailOffsets:   []int{},

		// ── Glamour 渲染缓存（LRU） ──
		glamourCache:    make(map[string]string),
		glamourLRU:      make([]string, 0, 32),
		glamourCacheCap: 32,

		// ── 增量内容构建相关 (Step 2) ──
		contentParts:        make([]string, 0),
		dirtyEntryIndices:   make(map[int]struct{}),
		needFullRebuild:     true, // 首次构建需要完全重建
		prevViewportWidth:   0,

		// 使用传入的终端尺寸初始化
		termWidth:   termWidth,
		termHeight:  termHeight,

		// ── 快捷键映射表 ──
		editKeyMap: editKeyMap,
		cmdKeyMap:  cmdKeyMap,
	}
}

func (m *model) Init() tea.Cmd {
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
