package tui

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"sync"

	"yagent/internal/app"
	"yagent/internal/config"
	"yagent/internal/datamanager"
	"yagent/internal/dict"
	"yagent/internal/http"
	"yagent/internal/messaging"
	"yagent/internal/models"
	"yagent/internal/tui/common"
	"yagent/internal/tui/components"
	"yagent/internal/tui/layout"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"
)

var langManager *LanguageManager

func glamourCacheKey(content string) string {
	if len(content) == 0 {
		return ""
	}
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:8])
}

func (m *model) getGlamourCached(content string) (string, bool) {
	key := glamourCacheKey(content)
	m.glamourCacheMu.Lock()
	defer m.glamourCacheMu.Unlock()
	if r, ok := m.glamourCache[key]; ok {
		return r, true
	}
	return "", false
}

func (m *model) putGlamourCached(content, rendered string) {
	key := glamourCacheKey(content)
	if key == "" || rendered == "" {
		return
	}
	m.glamourCacheMu.Lock()
	defer m.glamourCacheMu.Unlock()

	if _, ok := m.glamourCache[key]; ok {
		m.glamourCache[key] = rendered
		return
	}

	m.glamourCache[key] = rendered
	m.glamourLRU = append(m.glamourLRU, key)

	for len(m.glamourLRU) > m.glamourCacheCap {
		oldest := m.glamourLRU[0]
		m.glamourLRU = m.glamourLRU[1:]
		delete(m.glamourCache, oldest)
	}
}

type keywordCompletionConfig struct {
	enabled bool
}

var (
	bannerPadStyle = lipgloss.NewStyle().Padding(0, 1)

	welcomePanelStyle = lipgloss.NewStyle().Padding(1, 2)
	welcomeSubStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	welcomeTipStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	welcomeDimStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("62")).Bold(true)

	errorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("167")).Bold(true)

	logTimeStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Faint(true)
	logAIResStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	userPrefixStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	logUserMsgStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("222")).
			BorderLeft(true).
			BorderForeground(lipgloss.Color("214")).
			PaddingLeft(1)
	userMsgBoxBorderStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("240"))
	userMsgBoxTextStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("222"))
	logToolStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("228"))
	logResultStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	logStatusStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("36"))
	logErrorLogStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("167"))
	logSeparatorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	collapseHintLineStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	collapseHintTextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	diffHunkStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	diffAddStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	diffDelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("167"))
	diffCtxStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	toolRunningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("228"))
	toolDoneStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	toolErrorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("167"))

	llmCallStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("141"))
	llmCallEndStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("111"))
)

var (
	airlineColorNormalBg = compat.AdaptiveColor{Light: lipgloss.Color("24"), Dark: lipgloss.Color("24")}
	airlineColorNormalFg = compat.AdaptiveColor{Light: lipgloss.Color("15"), Dark: lipgloss.Color("15")}
	airlineColorRunBg    = compat.AdaptiveColor{Light: lipgloss.Color("70"), Dark: lipgloss.Color("76")}
	airlineColorRunFg    = compat.AdaptiveColor{Light: lipgloss.Color("15"), Dark: lipgloss.Color("15")}

	// INSERT 段与输入面板边框同色，模式切换时上下两处的视觉指示一致。
	airlineColorInsertBg = compat.AdaptiveColor{Light: lipgloss.Color("62"), Dark: lipgloss.Color("62")}
	airlineColorInsertFg = compat.AdaptiveColor{Light: lipgloss.Color("15"), Dark: lipgloss.Color("15")}

	airlineColorSearchBg = compat.AdaptiveColor{Light: lipgloss.Color("24"), Dark: lipgloss.Color("24")}
	airlineColorSearchFg = compat.AdaptiveColor{Light: lipgloss.Color("75"), Dark: lipgloss.Color("117")}

	airlineColorInfoBg    = compat.AdaptiveColor{Light: lipgloss.Color("235"), Dark: lipgloss.Color("236")}
	airlineColorInfoFg    = compat.AdaptiveColor{Light: lipgloss.Color("252"), Dark: lipgloss.Color("250")}
	airlineColorInfoAltBg = compat.AdaptiveColor{Light: lipgloss.Color("237"), Dark: lipgloss.Color("238")}
	airlineColorInfoAltFg = compat.AdaptiveColor{Light: lipgloss.Color("252"), Dark: lipgloss.Color("250")}

	airlineColorAccentBg = compat.AdaptiveColor{Light: lipgloss.Color("166"), Dark: lipgloss.Color("166")}
	airlineColorAccentFg = compat.AdaptiveColor{Light: lipgloss.Color("15"), Dark: lipgloss.Color("15")}

	airlineColorCancelBg = compat.AdaptiveColor{Light: lipgloss.Color("214"), Dark: lipgloss.Color("208")}
	airlineColorCancelFg = compat.AdaptiveColor{Light: lipgloss.Color("0"), Dark: lipgloss.Color("0")}
)

var (
	// 模式段与右段统一由 airlineSegment / chip 现场构造（配色见 tui_view.go
	// 的 airlineModeSegment），这里只保留填充区需要的样式。
	airlineFillerStyle = lipgloss.NewStyle().
		Background(airlineColorInfoBg).
		Foreground(airlineColorInfoFg)
)

type aiChunkBuffer struct {
	content   string
	count     int
	lastFlush time.Time
}

type logEntry struct {
	timestamp        time.Time
	eventType        string
	from             string
	content          string
	prefix           string
	toolName         string
	toolCallID       string
	isToolRunning    bool
	executionSummary string
	resultBrief      string
	diffText         string
	renderedCache    map[int]string

	toolEntry *ToolEntry

	isVerbose bool

	streamContent string
	streaming     bool
	finalized     bool
	agentName     string
}

func (e *logEntry) getCachedRender(width int) (string, bool) {
	if e.renderedCache == nil {
		return "", false
	}
	cached, ok := e.renderedCache[width]
	return cached, ok
}

func (e *logEntry) setCachedRender(content string, width int) {
	if e.renderedCache == nil {
		e.renderedCache = make(map[int]string)
	}
	e.renderedCache[width] = content
}

func (e *logEntry) clearRenderCache() {
	e.renderedCache = nil
}

type tickMsg struct{}

type taskEventMsg struct {
	event *messaging.MessageEvent
}

type taskCompleteMsg struct {
	taskID string
	result string
	err    error
}

type publisherReadyMsg struct {
	publisher *messaging.MessagePublisher
}

type tuiEventConsumer struct {
	ch chan *messaging.MessageEvent
}

func (c *tuiEventConsumer) Consume(event *messaging.MessageEvent) error {
	select {
	case c.ch <- event:
	default:
		slog.Warn("TUI 事件通道已满，丢弃事件", "component", "tui-model", "event_type", event.Type, "event_from", event.From)
	}
	return nil
}

type AgentTokenUsage struct {
	AgentName                string
	InputTokens              int64
	OutputTokens             int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	TotalInputTokens         int64
}

type AgentRunTokens struct {
	AgentName                string
	InputTokens              int64
	OutputTokens             int64
	CacheReadInputTokens     int64
	CacheCreationInputTokens int64
	TotalInputTokens         int64
	ContextTokens            int64
}

func (m *model) visibleEntryIndices() (start, end int) {
	prefix := m.contentPartLinePrefix
	n := len(prefix) - 1
	if n <= 0 || m.termHeight <= 0 {
		return 0, -1
	}

	footerHeight := m.computeFooterHeight()
	vpHeight := m.termHeight - footerHeight
	if vpHeight < 3 {
		vpHeight = 3
	}

	yOffset := m.viewport.YOffset()
	viewEnd := yOffset + vpHeight

	start = sort.Search(n, func(i int) bool {
		return prefix[i+1] > yOffset
	})

	endIdx := sort.Search(n, func(i int) bool {
		return prefix[i] >= viewEnd
	})
	end = endIdx - 1

	if start > end || start >= n {
		return 0, -1
	}
	if end >= n {
		end = n - 1
	}

	return start, end
}

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

func (m *model) appendLinePrefix(oldLen int) {
	newLen := len(m.contentParts)
	if newLen == 0 {
		m.contentPartLinePrefix = nil
		return
	}
	prefix := m.contentPartLinePrefix
	if len(prefix) < oldLen+1 {
		m.rebuildLinePrefix()
		return
	}
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
		return
	}
	delta := newLines - oldLines
	for j := i + 1; j <= n; j++ {
		prefix[j] += delta
	}
}

func (m *model) setEntryContent(i int, content string) {
	if i < 0 || i >= len(m.contentParts) {
		return
	}
	m.contentParts[i] = content
	m.updateLinePrefixEntry(i)
}

type model struct {
	com *common.Common

	assistant   *app.Yagent
	taskManager *http.TaskManager
	dataManager *datamanager.DataManager

	input textarea.Model

	logEntries      []logEntry
	viewport        viewport.Model
	contentCache    *strings.Builder
	glamourRenderer *glamour.TermRenderer
	useDarkStyle    bool

	taskRunning   bool
	taskStarted   bool
	taskCancelled bool
	currentTask   *http.Task
	// sessionID 决定本块会话用哪块便签板：同一 tab 内的多次任务共享便签，
	// 换 tab（restoreSessionTab）或清空会话（clearCurrentSession）才换新的。
	sessionID          string
	resumedFromRollout bool
	// resumedDelegationPath 是"从子 Agent 的那一步继续"要交给 app 的指路牌：
	// 刚恢复的那份 Director rollout。一次性，与 resumedFromRollout 同生同灭。
	resumedDelegationPath string
	eventCh               chan *messaging.MessageEvent

	termWidth   int
	termHeight  int
	quitting    bool
	errMsg      string
	infoMsg     string
	currentLang Language
	projectDir  string

	publisher   *messaging.MessagePublisher
	publisherCh chan *messaging.MessagePublisher

	commandMode   bool
	commandBuffer string
	lastKey       string

	// cmdHistory 保存已执行过的命令行（含 ":" / "/" 前缀原文）。
	// cmdRecalling 为 false 表示不在历史导航中；此时 cmdDraft 暂存开始导航前敲的半截命令。
	cmdHistory   []string
	cmdRecalling bool
	cmdRecallIdx int
	cmdDraft     string

	// NORMAL 模式数字前缀：输入 "10" 后接 j 表示下滚 10 行。
	pendingCount string

	// searchPattern 为空表示当前没有活动搜索。searchLines 保存命中行在
	// viewport 内容中的行号（升序），searchLineAt 是其反查表（行号→命中序号），
	// searchCursor 为当前命中下标。
	searchPattern   string
	searchLines     []int
	searchLineAt    map[int]int
	searchCursor    int
	searchContentFP int

	timelineEntries  []*TimelineEntry
	timelineExpanded bool
	timelineCache    string
	timelineCacheKey string

	skillAutoComplete  bool
	skillSuggestions   []string
	skillSuggestionIdx int

	keywordAutoComplete  bool
	keywordSuggestions   []string
	keywordSuggestionIdx int
	keywordDict          *dict.CompletionDict
	keywordCompletionCfg keywordCompletionConfig

	toolCallEntries map[string]*ToolEntry

	llmCallActiveEntries map[string]int

	aiStreamActiveEntries    map[string]int
	aiStreamCompletedEntries map[string]int

	aiChunkBuffers map[string]*aiChunkBuffer

	currentModel string

	currentAgent string

	currentProvider    string
	contextWindow      int
	pendingModelTarget string

	// autoRouting 反映自动路由是否生效，用于状态栏展示 AUTO 段。
	autoRouting bool

	inputTokens              int64
	outputTokens             int64
	cacheCreationInputTokens int64
	cacheReadInputTokens     int64
	totalInputTokens         int64

	tokenUsagePerAgent map[string]*AgentTokenUsage

	tokenDashboardCollapsed bool

	dashboardCollapsed bool

	// runTokensPerAgent 是"本轮（自最近一次提交）每个 agent 的用量"，dashboard 的 RUN 列依赖它；
	// runAgent 指向当前活跃 agent（⛁ 行显示它的 ContextTokens）。
	// 不能用 m.currentAgent 当指针 —— 它只在 taskRunning 时才被赋值。
	runTokensPerAgent map[string]*AgentRunTokens
	runAgent          string

	// compress 是本任务历次上下文压缩的累计效果，dashboard 的 ⧉ 段依赖它。
	// 与 Σ 同生命周期（会话累计），不受 resetRunTokens 影响。
	compress compressStats

	anim       *Anim
	activeAnim bool

	historyMode     bool
	historyItems    []datamanager.TaskHistoryItem
	historyCursor   int
	historyPage     int
	historyPageSize int
	historyLoading  bool
	historyStyles   *historyStyles

	resumeMode     bool
	resumeGroups   []resumeGroup
	resumeStops    resumeStopIndex // path → 终止事实；缺失 = 未知，不是不能续
	resumeRows     []resumeRow
	resumeExpanded map[string]bool
	resumeCursor   int
	resumeLoading  bool
	resumeErr      string
	resumeRoot     string

	// 引导向导（/setup）。setupActive 时向导独占键盘，见 setup_embed.go。
	setupActive bool
	wizard      *setupWizard

	pendingDeleteTaskID string

	dialogStack  *components.DialogStack
	layoutEngine *layout.LayoutEngine
	mouseHandler *components.ClickDetector

	skillSuggestionStyle   lipgloss.Style
	skillHighlightStyle    lipgloss.Style
	skillHintStyle         lipgloss.Style
	keywordSuggestionStyle lipgloss.Style
	keywordHighlightStyle  lipgloss.Style

	pendingAutocomplete bool
	debounceTimer       *time.Timer
	snapshotText        string
	snapshotCursor      int

	autocompleteCache map[autocompleteCacheKey]*AutocompleteResult

	cachedTokenDashboard string
	tokenDashboardValid  bool
	cachedStatusBar      string
	statusBarValid       bool

	dashboardCache    string
	dashboardCacheKey string

	tickStarted   bool
	viewportDirty bool

	glamourRenderers map[int]*glamour.TermRenderer

	cachedFooterHeight int
	footerHeightValid  bool

	glamourCache    map[string]string
	glamourLRU      []string
	glamourCacheMu  sync.Mutex
	glamourCacheCap int

	cachedViewportView  string
	viewportViewValid   bool
	prevViewportYOffset int
	prevViewportHeight  int

	contentParts      []string
	dirtyEntryIndices map[int]struct{}
	needFullRebuild   bool
	prevViewportWidth int

	contentPartLinePrefix []int

	timelineFullscreenMode   bool
	timelineFullscreenCursor int
	timelineDetailVP         *viewport.Model
	timelineFullscreenFocus  string
	timelineDetailOffsets    []int

	thinklinkMode         bool
	thinklinkEntries      []ThinklinkEntry
	thinklinkCursor       int
	thinklinkVP           *viewport.Model
	thinklinkDetailActive bool
	// thinklinkAgentFilter 为空表示不过滤；非空时只显示该 Agent 的条目（`f` 循环）。
	thinklinkAgentFilter string

	editKeyMap map[string]string

	cmdKeyMap map[string]string

	sessionTabs      []*sessionTab
	activeSessionIdx int
	nextTabID        int
	maxTabs          int
}

type autocompleteCacheKey struct {
	word     string
	hasSlash bool
}

type AutocompleteResult struct {
	skillSuggestions     []string
	skillSuggestionIdx   int
	keywordSuggestions   []string
	keywordSuggestionIdx int
}

func (m *model) markEntryDirty(idx int) {
	if m.dirtyEntryIndices == nil {
		m.dirtyEntryIndices = make(map[int]struct{})
	}
	m.dirtyEntryIndices[idx] = struct{}{}
}

func (m *model) markAllEntriesDirty() {
	m.needFullRebuild = true
	for k := range m.dirtyEntryIndices {
		delete(m.dirtyEntryIndices, k)
	}
}

func (m *model) hasDirtyEntries() bool {
	return m.needFullRebuild || len(m.dirtyEntryIndices) > 0
}

func contextWindowFromCfg(cfg *config.Config, providerName string) int {
	if cfg == nil || providerName == "" {
		return 0
	}
	pc, ok := cfg.Global.LLM.Providers[providerName]
	if !ok {
		return 0
	}
	if pc.ContextWindow > 0 {
		return pc.ContextWindow
	}
	return models.ContextWindow(pc.Model)
}

func (m *model) refreshContextWindow() {
	m.contextWindow = 0
	if m.assistant == nil {
		return
	}
	client := m.assistant.GetClient()
	if client == nil {
		return
	}
	m.contextWindow = contextWindowFromCfg(client.Config, m.currentProvider)
}

// newTaskInput 构造任务输入框（textarea），带完整的视觉状态样式。
// 独立成函数是为了让渲染测试能用与生产完全一致的输入框。
func newTaskInput(preloadedTaskContent string) textarea.Model {
	ti := textarea.New()

	ti.Placeholder = langManager.GetText("TaskDescPlaceholder")
	ti.Focus()
	ti.CharLimit = 0
	ti.SetWidth(60)
	ti.SetHeight(3)
	ti.ShowLineNumbers = false
	ti.SetVirtualCursor(true)

	applyTextareaTokens(&ti)

	if preloadedTaskContent != "" {
		ti.SetValue(preloadedTaskContent)
	}
	return ti
}

// applyTextareaTokens 是"输入框长什么样"的唯一答案：主输入框与引导向导的表单字段共用
// 同一套令牌（docs/setup-wizard-ui-design.md §3.5）。两边各写一份的后果是配置表单
// —— 用户对这个项目的第一印象 —— 比聊天框朴素。
func applyTextareaTokens(ti *textarea.Model) {
	textStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))

	// 输入区是一块比面板底色更深的"字段"，光标所在行再抬亮一档，
	// 让多行编辑时的当前行位置一眼可辨。
	editBaseStyle := lipgloss.NewStyle().Background(lipgloss.Color("235"))

	focusedStyle := textarea.StyleState{
		Base:        editBaseStyle,
		Text:        textStyle,
		Prompt:      lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true).Background(lipgloss.Color("235")),
		CursorLine:  lipgloss.NewStyle().Background(lipgloss.Color("236")),
		Placeholder: lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Background(lipgloss.Color("235")).Italic(true),
	}

	blurredStyle := textarea.StyleState{
		Base:        editBaseStyle,
		Text:        lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		Prompt:      lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Background(lipgloss.Color("235")),
		CursorLine:  lipgloss.NewStyle().Background(lipgloss.Color("235")),
		Placeholder: lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Background(lipgloss.Color("235")).Italic(true),
	}

	ti.SetStyles(textarea.Styles{
		Focused: focusedStyle,
		Blurred: blurredStyle,
		Cursor: textarea.CursorStyle{
			Color:      lipgloss.Color("39"),
			Shape:      tea.CursorBar,
			Blink:      true,
			BlinkSpeed: 400 * time.Millisecond,
		},
	})

	ti.SetPromptFunc(2, func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return "❯ "
		}
		return "  "
	})
}

func initialModel(preloadedTaskContent string, ca *app.Yagent, tm *http.TaskManager, dm *datamanager.DataManager, useDarkStyle bool, cfg *config.Config, termWidth, termHeight int) *model {
	ti := newTaskInput(preloadedTaskContent)

	projectDir, _ := os.Getwd()

	vp := viewport.New(viewport.WithWidth(termWidth), viewport.WithHeight(termHeight))
	vp.Style = lipgloss.NewStyle().Padding(0, 1)

	glamourStyle := "dark"
	if !useDarkStyle {
		glamourStyle = "light"
	}
	glamourRenderer, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(glamourStyle),
		glamour.WithWordWrap(termWidth-10),
	)
	if err != nil {
		glamourRenderer = nil
	}

	ds := components.NewDialogStack()
	le := layout.NewLayoutEngine()
	md := components.NewClickDetector()

	var keywordDict *dict.CompletionDict
	var completionEnabled bool

	if cfg != nil && cfg.Keywords.DisableCompletion {
		completionEnabled = false
	} else {
		completionEnabled = true
		homeDir, _ := os.UserHomeDir()
		userDictPath := filepath.Join(homeDir, ".yagent", "keywords.txt")
		projectDictPath := filepath.Join(projectDir, ".yagent", "keywords.txt")

		keywordDict = dict.NewCompletionDict("autocomplete", []string{userDictPath, projectDictPath})

		keywordDict.AddWords(dict.DefaultKeywords())
	}

	initProvider := ""
	initModel := ""
	initAutoRouting := false
	if ca != nil && ca.GetClient() != nil {
		prov, model := ca.GetClient().GetCurrentProviderInfo()
		initProvider = prov
		initModel = model
		initAutoRouting = ca.GetRouteSnapshot().Enabled
	}

	styles := common.NewStyles()
	com := common.NewCommon(styles, cfg, ca, projectDir, useDarkStyle)

	// 键位表：见 tui_keymap.go —— 自定义键是**替换**默认键，不是再加一个别名。
	editKeyMap, _ := buildKeyMap(editKeyBindings(cfg))
	cmdKeyMap, _ := buildKeyMap(commandKeyBindings(cfg))

	m := &model{
		com: com,

		assistant:                ca,
		taskManager:              tm,
		dataManager:              dm,
		input:                    ti,
		projectDir:               projectDir,
		currentLang:              langManager.currentLang,
		eventCh:                  make(chan *messaging.MessageEvent, 1000),
		logEntries:               make([]logEntry, 0),
		llmCallActiveEntries:     make(map[string]int),
		viewport:                 vp,
		aiStreamActiveEntries:    make(map[string]int),
		aiStreamCompletedEntries: make(map[string]int),
		aiChunkBuffers:           make(map[string]*aiChunkBuffer),
		contentCache:             &strings.Builder{},
		glamourRenderer:          glamourRenderer,
		useDarkStyle:             useDarkStyle,
		toolCallEntries:          make(map[string]*ToolEntry),
		anim:                     NewAnim(10),
		tokenUsagePerAgent:       make(map[string]*AgentTokenUsage),
		runTokensPerAgent:        make(map[string]*AgentRunTokens),
		tokenDashboardCollapsed:  false,

		dialogStack:          ds,
		layoutEngine:         le,
		mouseHandler:         md,
		keywordDict:          keywordDict,
		keywordCompletionCfg: keywordCompletionConfig{enabled: completionEnabled},

		currentProvider: initProvider,
		currentModel:    initModel,
		contextWindow:   contextWindowFromCfg(cfg, initProvider),
		autoRouting:     initAutoRouting,

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

		autocompleteCache: make(map[autocompleteCacheKey]*AutocompleteResult),

		tickStarted:   false,
		viewportDirty: false,

		timelineEntries:         make([]*TimelineEntry, 0),
		timelineExpanded:        false,
		timelineFullscreenFocus: "list",
		timelineDetailOffsets:   []int{},

		glamourCache:    make(map[string]string),
		glamourLRU:      make([]string, 0, 32),
		glamourCacheCap: 32,

		contentParts:      make([]string, 0),
		dirtyEntryIndices: make(map[int]struct{}),
		needFullRebuild:   true,
		prevViewportWidth: 0,

		termWidth:  termWidth,
		termHeight: termHeight,

		editKeyMap: editKeyMap,
		cmdKeyMap:  cmdKeyMap,

		maxTabs: 10,
	}
	m.bindViewportHooks()
	m.initSessionTabs()
	return m
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(
		listenForEvents(m.eventCh),
	)
}

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
}

func formatKeyDisplayName(key string) string {
	if key == "" {
		return ""
	}
	parts := strings.Split(key, "+")
	if len(parts) == 1 {
		return key
	}
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "+")
}

func buildHelpKeyOverrides(kb *config.KeybindingsConfig) map[string]string {
	overrides := make(map[string]string)

	if kb.Edit.CommandMode != "ctrl+e" {
		overrides["Ctrl+E"] = formatKeyDisplayName(kb.Edit.CommandMode)
	}
	if kb.Edit.ToggleHelp != "ctrl+h" {
		overrides["Ctrl+H"] = formatKeyDisplayName(kb.Edit.ToggleHelp)
	}
	if kb.Edit.ToggleTimeline != "ctrl+l" {
		overrides["Ctrl+L"] = formatKeyDisplayName(kb.Edit.ToggleTimeline)
	}
	if kb.Edit.PageDown != "ctrl+f" {
		overrides["Ctrl+F"] = formatKeyDisplayName(kb.Edit.PageDown)
	}
	if kb.Edit.PageUp != "ctrl+b" {
		overrides["Ctrl+B"] = formatKeyDisplayName(kb.Edit.PageUp)
	}
	if kb.Edit.Quit != "ctrl+c" {
		overrides["Ctrl+C"] = formatKeyDisplayName(kb.Edit.Quit)
	}
	if kb.Edit.SubmitTask != "alt+s" {
		overrides["Alt+S"] = formatKeyDisplayName(kb.Edit.SubmitTask)
	}
	if kb.Edit.SwitchModel != "alt+m" {
		overrides["Alt+M"] = formatKeyDisplayName(kb.Edit.SwitchModel)
	}
	if kb.Edit.ToggleDashboard != "alt+d" {
		overrides["Alt+D"] = formatKeyDisplayName(kb.Edit.ToggleDashboard)
	}

	if kb.Command.ScrollDown != "j" {
		overrides["j"] = formatKeyDisplayName(kb.Command.ScrollDown)
	}
	if kb.Command.ScrollUp != "k" {
		overrides["k"] = formatKeyDisplayName(kb.Command.ScrollUp)
	}
	if kb.Command.PageDown != "f" {
		overrides["f"] = formatKeyDisplayName(kb.Command.PageDown)
	}
	if kb.Command.PageUp != "b" {
		overrides["b"] = formatKeyDisplayName(kb.Command.PageUp)
	}
	if kb.Command.EditMode != "i" {
		overrides["i"] = formatKeyDisplayName(kb.Command.EditMode)
	}
	if kb.Command.CmdToggleHelp != "?" {
		overrides["?"] = formatKeyDisplayName(kb.Command.CmdToggleHelp)
	}
	if kb.Command.ToggleTokenPanel != "alt+t" {
		overrides["Alt+T"] = formatKeyDisplayName(kb.Command.ToggleTokenPanel)
	}
	if kb.Command.SwitchModel != "alt+m" {
		overrides["Alt+M"] = formatKeyDisplayName(kb.Command.SwitchModel)
	}
	if kb.Command.ToggleDashboard != "alt+d" {
		overrides["Alt+D"] = formatKeyDisplayName(kb.Command.ToggleDashboard)
	}
	if kb.Command.Quit != "ctrl+c" {
		// 仅作为回退：若 Edit.Quit 已自定义，Ctrl+C 的显示以编辑模式键名优先
		if _, ok := overrides["Ctrl+C"]; !ok {
			overrides["Ctrl+C"] = formatKeyDisplayName(kb.Command.Quit)
		}
	}

	if kb.Edit.NewTab != "ctrl+t" {
		overrides["Ctrl+T"] = formatKeyDisplayName(kb.Edit.NewTab)
	}
	if kb.Edit.PrevTab != "alt+[" {
		overrides["Alt+["] = formatKeyDisplayName(kb.Edit.PrevTab)
	}
	if kb.Edit.NextTab != "alt+]" {
		overrides["Alt+]"] = formatKeyDisplayName(kb.Edit.NextTab)
	}
	if kb.Edit.ClearSession != "alt+c" {
		overrides["Alt+C"] = formatKeyDisplayName(kb.Edit.ClearSession)
	}
	if kb.Edit.CloseTab != "alt+w" {
		overrides["Alt+W"] = formatKeyDisplayName(kb.Edit.CloseTab)
	}
	for i := 1; i <= 9; i++ {
		key := fmt.Sprintf("Alt+%d", i)
		var cfgVal string
		switch i {
		case 1:
			cfgVal = kb.Edit.Tab1
		case 2:
			cfgVal = kb.Edit.Tab2
		case 3:
			cfgVal = kb.Edit.Tab3
		case 4:
			cfgVal = kb.Edit.Tab4
		case 5:
			cfgVal = kb.Edit.Tab5
		case 6:
			cfgVal = kb.Edit.Tab6
		case 7:
			cfgVal = kb.Edit.Tab7
		case 8:
			cfgVal = kb.Edit.Tab8
		case 9:
			cfgVal = kb.Edit.Tab9
		}
		if cfgVal != fmt.Sprintf("alt+%d", i) {
			overrides[key] = formatKeyDisplayName(cfgVal)
		}
	}

	return overrides
}
