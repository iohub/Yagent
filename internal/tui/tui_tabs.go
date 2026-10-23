package tui

import (
	"fmt"
	"strconv"
	"strings"

	"yagent/internal/http"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/google/uuid"
)

const tabBarHeight = 1

type sessionTab struct {
	id    int
	title string

	logEntries        []logEntry
	contentParts      []string
	dirtyEntryIndices map[int]struct{}
	needFullRebuild   bool
	// 正文虚拟化状态与 viewport 必须同版本一起存，否则恢复后 bodySync 以为窗口没变而跳过 SetContent，屏幕停在另一个 tab 的内容上。
	body     bodyIndex
	viewport viewport.Model

	inputValue string

	currentTask *http.Task
	// sessionID 决定这块会话用哪块便签板：同一 tab 内多次任务共享结论，换 tab 或清空会话才换新 id（见 model.sessionID）。
	sessionID     string
	taskRunning   bool
	taskStarted   bool
	taskCancelled bool

	timelineEntries  []*TimelineEntry
	timelineExpanded bool
	timelineCache    string
	timelineCacheKey string

	toolCallEntries          map[string]*ToolEntry
	llmCallActiveEntries     map[string]int
	aiStreamActiveEntries    map[string]int
	aiStreamCompletedEntries map[string]int
	aiChunkBuffers           map[string]*aiChunkBuffer

	currentModel    string
	currentAgent    string
	currentProvider string

	inputTokens              int64
	outputTokens             int64
	cacheCreationInputTokens int64
	cacheReadInputTokens     int64
	totalInputTokens         int64
	tokenUsagePerAgent       map[string]*AgentTokenUsage
	runTokensPerAgent        map[string]*AgentRunTokens
	runAgent                 string
	compress                 compressStats
}

func (m *model) initSessionTabs() {
	m.sessionTabs = make([]*sessionTab, 0, m.maxTabs)
	m.activeSessionIdx = 0
	m.nextTabID = 1

	// 创建第一个默认 tab（快照初始为空，首次 save 时填充）
	defaultTab := m.newSessionTab(m.nextTabID, m.generateTabTitle(m.nextTabID))
	m.sessionTabs = append(m.sessionTabs, defaultTab)
	m.nextTabID++
}

func (m *model) newSessionTab(id int, title string) *sessionTab {
	vp := viewport.New(viewport.WithWidth(0), viewport.WithHeight(0))
	return &sessionTab{
		id:                id,
		title:             title,
		viewport:          vp,
		logEntries:        make([]logEntry, 0),
		contentParts:      make([]string, 0),
		dirtyEntryIndices: make(map[int]struct{}),
		needFullRebuild:   true,

		inputValue:               "",
		sessionID:                uuid.New().String(),
		taskRunning:              false,
		taskStarted:              false,
		taskCancelled:            false,
		timelineEntries:          make([]*TimelineEntry, 0),
		timelineExpanded:         false,
		toolCallEntries:          make(map[string]*ToolEntry),
		llmCallActiveEntries:     make(map[string]int),
		aiStreamActiveEntries:    make(map[string]int),
		aiStreamCompletedEntries: make(map[string]int),
		aiChunkBuffers:           make(map[string]*aiChunkBuffer),
		tokenUsagePerAgent:       make(map[string]*AgentTokenUsage),
		runTokensPerAgent:        make(map[string]*AgentRunTokens),
	}
}

func (m *model) saveActiveSession() {
	if m.activeSessionIdx < 0 || m.activeSessionIdx >= len(m.sessionTabs) {
		return
	}
	tab := m.sessionTabs[m.activeSessionIdx]

	tab.logEntries = make([]logEntry, len(m.logEntries))
	copy(tab.logEntries, m.logEntries)
	tab.contentParts = make([]string, len(m.contentParts))
	copy(tab.contentParts, m.contentParts)
	tab.dirtyEntryIndices = make(map[int]struct{}, len(m.dirtyEntryIndices))
	for k, v := range m.dirtyEntryIndices {
		tab.dirtyEntryIndices[k] = v
	}
	tab.needFullRebuild = m.needFullRebuild
	tab.body = m.body.clone()

	// 保存 viewport（值复制保留内部内容）
	tab.viewport = m.viewport

	tab.inputValue = m.input.Value()

	tab.currentTask = m.currentTask
	tab.sessionID = m.sessionID
	tab.taskRunning = m.taskRunning
	tab.taskStarted = m.taskStarted
	tab.taskCancelled = m.taskCancelled

	tab.timelineEntries = make([]*TimelineEntry, len(m.timelineEntries))
	copy(tab.timelineEntries, m.timelineEntries)
	tab.timelineExpanded = m.timelineExpanded
	tab.timelineCache = m.timelineCache
	tab.timelineCacheKey = m.timelineCacheKey

	tab.toolCallEntries = make(map[string]*ToolEntry, len(m.toolCallEntries))
	for k, v := range m.toolCallEntries {
		tab.toolCallEntries[k] = v
	}
	tab.llmCallActiveEntries = make(map[string]int, len(m.llmCallActiveEntries))
	for k, v := range m.llmCallActiveEntries {
		tab.llmCallActiveEntries[k] = v
	}
	tab.aiStreamActiveEntries = make(map[string]int, len(m.aiStreamActiveEntries))
	for k, v := range m.aiStreamActiveEntries {
		tab.aiStreamActiveEntries[k] = v
	}
	tab.aiStreamCompletedEntries = make(map[string]int, len(m.aiStreamCompletedEntries))
	for k, v := range m.aiStreamCompletedEntries {
		tab.aiStreamCompletedEntries[k] = v
	}
	tab.aiChunkBuffers = make(map[string]*aiChunkBuffer, len(m.aiChunkBuffers))
	for k, v := range m.aiChunkBuffers {
		tab.aiChunkBuffers[k] = v
	}

	tab.currentModel = m.currentModel
	tab.currentAgent = m.currentAgent
	tab.currentProvider = m.currentProvider

	tab.inputTokens = m.inputTokens
	tab.outputTokens = m.outputTokens
	tab.cacheCreationInputTokens = m.cacheCreationInputTokens
	tab.cacheReadInputTokens = m.cacheReadInputTokens
	tab.totalInputTokens = m.totalInputTokens
	tab.tokenUsagePerAgent = make(map[string]*AgentTokenUsage, len(m.tokenUsagePerAgent))
	for k, v := range m.tokenUsagePerAgent {
		tab.tokenUsagePerAgent[k] = v
	}
	// RUN 表存的是会被事件原地改写的指针，切 tab 前必须深拷贝，否则两个 tab 会共用同一个 agent 的计数器。
	tab.runTokensPerAgent = make(map[string]*AgentRunTokens, len(m.runTokensPerAgent))
	for k, v := range m.runTokensPerAgent {
		copied := *v
		tab.runTokensPerAgent[k] = &copied
	}
	tab.runAgent = m.runAgent
	// 压缩统计是会话累计（与 Σ 同生命周期），cut 表和采样数组同样被事件原地改写，所以同 RUN 表必须深拷贝。
	tab.compress = m.compress.clone()

	if m.currentTask != nil && !m.taskRunning {
		m.saveAndFlushTaskMemory()
	}
}

func (m *model) restoreSessionTab(idx int) {
	if idx < 0 || idx >= len(m.sessionTabs) {
		return
	}
	tab := m.sessionTabs[idx]

	m.logEntries = tab.logEntries
	m.contentParts = tab.contentParts
	m.dirtyEntryIndices = tab.dirtyEntryIndices
	m.needFullRebuild = tab.needFullRebuild
	m.body = tab.body.clone()

	m.viewport = tab.viewport
	// viewport 被整体替换后，逐行回调（搜索高亮）必须重挂；命中行号是相对上一个会话的内容算的，切标签页后一律作废。
	m.bindViewportHooks()
	m.clearSearch()

	m.input.SetValue(tab.inputValue)
	m.input.CursorEnd()

	m.currentTask = tab.currentTask
	m.sessionID = tab.sessionID
	m.taskRunning = tab.taskRunning
	m.taskStarted = tab.taskStarted
	m.taskCancelled = tab.taskCancelled

	m.timelineEntries = tab.timelineEntries
	m.timelineExpanded = tab.timelineExpanded
	m.timelineCache = tab.timelineCache
	m.timelineCacheKey = tab.timelineCacheKey

	m.toolCallEntries = tab.toolCallEntries
	m.llmCallActiveEntries = tab.llmCallActiveEntries
	m.aiStreamActiveEntries = tab.aiStreamActiveEntries
	m.aiStreamCompletedEntries = tab.aiStreamCompletedEntries
	m.aiChunkBuffers = tab.aiChunkBuffers

	m.currentModel = tab.currentModel
	m.currentAgent = tab.currentAgent
	m.currentProvider = tab.currentProvider
	// provider 变化 → 刷新上下文窗口上限（末尾统一 invalidateFooterCache）
	m.refreshContextWindow()

	m.inputTokens = tab.inputTokens
	m.outputTokens = tab.outputTokens
	m.cacheCreationInputTokens = tab.cacheCreationInputTokens
	m.cacheReadInputTokens = tab.cacheReadInputTokens
	m.totalInputTokens = tab.totalInputTokens
	m.tokenUsagePerAgent = tab.tokenUsagePerAgent
	m.runTokensPerAgent = make(map[string]*AgentRunTokens, len(tab.runTokensPerAgent))
	for k, v := range tab.runTokensPerAgent {
		copied := *v
		m.runTokensPerAgent[k] = &copied
	}
	m.runAgent = tab.runAgent
	m.compress = tab.compress.clone()

	// viewport 尺寸必须在任务状态恢复之后才算：dashboard 占不占那一列取决于 taskRunning 与 token 用量，早算用的是上一个标签页的状态。
	l := m.computeBodyLayout(m.bodyTotalLines())
	m.viewport.SetWidth(l.contentWidth)
	m.viewport.SetHeight(l.height)
	if tab.viewport.Width() != 0 && tab.viewport.Width() != l.contentWidth {
		m.needFullRebuild = true
	}

	m.rebuildContentCache()
	m.bodyScrollTo(m.body.top)

	m.invalidateFooterCache()
	m.viewportViewValid = false
	m.cachedStatusBar = m.renderAirlineStatusBar()
}

func (m *model) switchSessionTab(idx int) {
	if idx < 0 || idx >= len(m.sessionTabs) {
		return
	}
	m.saveActiveSession()
	m.activeSessionIdx = idx
	m.restoreSessionTab(idx)
}

func (m *model) newSessionTabAction() tea.Cmd {
	m.saveActiveSession()
	newTab := m.newSessionTab(m.nextTabID, m.generateTabTitle(m.nextTabID))
	m.sessionTabs = append(m.sessionTabs, newTab)
	m.activeSessionIdx = len(m.sessionTabs) - 1
	m.nextTabID++
	m.restoreSessionTab(m.activeSessionIdx)
	m.setInfoMsg(langManager.GetText("TabNewCreated"))
	return nil
}

func (m *model) clearCurrentSession() {
	if m.currentTask != nil && !m.taskRunning {
		m.saveAndFlushTaskMemory()
	}

	m.logEntries = nil
	m.contentParts = nil
	m.dirtyEntryIndices = make(map[int]struct{})
	m.needFullRebuild = true
	m.body = bodyIndex{}
	m.body.forget()
	m.viewport.SetContent("")
	m.currentTask = nil
	// 清空会话 = 换一块便签板：旧会话的结论不该继续出现在新话题的索引里。
	m.sessionID = uuid.New().String()
	m.taskRunning = false
	m.taskStarted = false
	m.taskCancelled = false
	m.timelineEntries = nil
	m.toolCallEntries = make(map[string]*ToolEntry)
	m.llmCallActiveEntries = make(map[string]int)
	m.aiStreamActiveEntries = make(map[string]int)
	m.aiStreamCompletedEntries = make(map[string]int)
	m.aiChunkBuffers = make(map[string]*aiChunkBuffer)
	m.currentModel = ""
	m.currentAgent = ""
	m.currentProvider = ""
	// provider 清空 → 上下文窗口上限同步置为未知
	m.contextWindow = 0
	m.inputTokens = 0
	m.outputTokens = 0
	m.cacheCreationInputTokens = 0
	m.cacheReadInputTokens = 0
	m.totalInputTokens = 0
	m.tokenUsagePerAgent = make(map[string]*AgentTokenUsage)
	m.runTokensPerAgent = make(map[string]*AgentRunTokens)
	m.runAgent = ""
	m.compress = compressStats{}
	m.input.SetValue("")

	m.sessionTabs[m.activeSessionIdx].title = m.generateTabTitle(m.sessionTabs[m.activeSessionIdx].id)

	m.saveActiveSession()
	m.setInfoMsg(langManager.GetText("TabCleared"))
}

func (m *model) closeCurrentSessionTab() {
	if m.currentTask != nil && !m.taskRunning {
		m.saveAndFlushTaskMemory()
	}

	if len(m.sessionTabs) <= 1 {
		m.setInfoMsg(langManager.GetText("TabCloseLastBlocked"))
		return
	}

	m.saveActiveSession()

	m.sessionTabs = append(m.sessionTabs[:m.activeSessionIdx], m.sessionTabs[m.activeSessionIdx+1:]...)

	if m.activeSessionIdx >= len(m.sessionTabs) {
		m.activeSessionIdx = len(m.sessionTabs) - 1
	}

	m.restoreSessionTab(m.activeSessionIdx)
	m.setInfoMsg(langManager.GetText("TabClosed"))
}

func (m *model) updateActiveTabTitle(desc string) {
	if m.activeSessionIdx < 0 || m.activeSessionIdx >= len(m.sessionTabs) {
		return
	}
	tab := m.sessionTabs[m.activeSessionIdx]
	prefix := langManager.GetText("SessionTitlePrefix")
	defaultTitle := prefix + " " + strconv.Itoa(tab.id)
	if tab.title == defaultTitle {
		runes := []rune(desc)
		if len(runes) > 12 {
			tab.title = string(runes[:12]) + "…"
		} else {
			tab.title = string(runes)
		}
	}
}

func (m *model) generateTabTitle(id int) string {
	return langManager.GetText("SessionTitlePrefix") + " " + strconv.Itoa(id)
}

var (
	tabChipActiveBg    = lipgloss.Color("141")
	tabChipActiveFg    = lipgloss.Color("16")
	tabChipIdleBg      = lipgloss.Color("236")
	tabChipIdleNumFg   = lipgloss.Color("241")
	tabChipIdleTitleFg = lipgloss.Color("246")
	tabNewBtnBg        = lipgloss.Color("235")
	tabNewBtnFg        = lipgloss.Color("243")
	tabDotRunActiveFg  = lipgloss.Color("22")
	tabDotRunIdleFg    = lipgloss.Color("114")
)

func renderSessionChip(num int, tab *sessionTab, active bool) string {
	bg := tabChipIdleBg
	numFg, titleFg := tabChipIdleNumFg, tabChipIdleTitleFg
	if active {
		bg = tabChipActiveBg
		numFg, titleFg = tabChipActiveFg, tabChipActiveFg
	}
	numStyle := lipgloss.NewStyle().Background(bg).Foreground(numFg).Bold(true)
	titleStyle := lipgloss.NewStyle().Background(bg).Foreground(titleFg).Bold(active)

	chip := numStyle.Render(fmt.Sprintf(" %d ", num)) + titleStyle.Render(tab.title+" ")
	if tab.taskRunning {
		dotFg := tabDotRunIdleFg
		if active {
			dotFg = tabDotRunActiveFg
		}
		chip += lipgloss.NewStyle().Background(bg).Foreground(dotFg).Bold(true).Render("● ")
	}
	return chip
}

// renderTabBar 渲染恰好 1 行 tab 栏（tabBarHeight 假设的就是这个）。
func (m *model) renderTabBar() string {
	if len(m.sessionTabs) == 0 {
		return ""
	}

	availableWidth := m.termWidth - 1

	type tabItem struct {
		label  string
		styled string
		width  int
	}
	var items []tabItem

	for i, tab := range m.sessionTabs {
		chip := renderSessionChip(i+1, tab, i == m.activeSessionIdx)
		items = append(items, tabItem{label: tab.title, styled: chip, width: lipgloss.Width(chip)})
	}

	if len(m.sessionTabs) < m.maxTabs {
		newBtn := lipgloss.NewStyle().
			Background(tabNewBtnBg).
			Foreground(tabNewBtnFg).
			Bold(true).
			Render(" + ")
		items = append(items, tabItem{label: "+", styled: newBtn, width: lipgloss.Width(newBtn)})
	}

	// 贪心追加 tab（chip 间以单空格分隔，计入 1 列分隔宽），保证活动 tab 始终可见
	var resultItems []tabItem
	totalWidth := 0
	activeIdx := m.activeSessionIdx

	for _, item := range items {
		cost := item.width
		if len(resultItems) > 0 {
			cost++
		}
		if totalWidth+cost <= availableWidth {
			resultItems = append(resultItems, item)
			totalWidth += cost
		} else {
			foundActive := false
			for j := range resultItems {
				if j == activeIdx {
					foundActive = true
					break
				}
			}
			if !foundActive {
				resultItems = nil
				totalWidth = 0
				for k, item := range items {
					if k < activeIdx {
						continue
					}
					cost := item.width
					if len(resultItems) > 0 {
						cost++
					}
					if totalWidth+cost <= availableWidth {
						resultItems = append(resultItems, item)
						totalWidth += cost
					}
				}
			}
			break
		}
	}

	var tabStrings []string
	for _, item := range resultItems {
		tabStrings = append(tabStrings, item.styled)
	}
	result := strings.Join(tabStrings, " ")

	// 右侧键帽提示：空间富余（chips 与提示之间至少留 2 列）才显示，且按优先级从尾部逐项丢弃而不是整组消失。
	hint := fitKeycaps(m.tabKeycaps(), availableWidth-totalWidth-2)
	if hintWidth := lipgloss.Width(hint); hintWidth > 0 {
		result += strings.Repeat(" ", availableWidth-totalWidth-hintWidth) + hint
	} else if totalWidth < availableWidth {
		result += strings.Repeat(" ", availableWidth-totalWidth)
	}

	return result
}

// tabKeycaps 是 tab 行右端的全局键帽组，按"越靠前越重要"排序（fitKeycaps 从尾部丢弃）。
func (m *model) tabKeycaps() []string {
	caps := []string{
		dashKeycap("ctrl+t", langManager.GetText("TabNewHint")),
		dashKeycap("alt+w", langManager.GetText("TabCloseHint")),
		dashKeycap("alt+m", langManager.GetText("TabModelHint")),
		dashKeycap("alt+r", langManager.GetText("TabRouteHint")),
	}
	// dashboard 展开时它自己的标题栏已经写了 alt+d，这里不重复报；收起（或没有用量）时才在 tab 行补上入口提示。
	if !m.dashboardVisible() {
		caps = append(caps, dashKeycap("alt+d", langManager.GetText("DashboardExpandHint")))
	}
	return caps
}

func fitKeycaps(caps []string, budget int) string {
	var b strings.Builder
	used := 0
	for _, c := range caps {
		w := lipgloss.Width(c)
		if used+w > budget {
			break
		}
		b.WriteString(c)
		used += w
	}
	return b.String()
}
