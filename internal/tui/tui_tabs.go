package tui

import (
	"fmt"
	"strconv"
	"strings"

	"codeactor/internal/http"

	tea "charm.land/bubbletea/v2"
	"charm.land/bubbles/v2/viewport"
	"charm.land/lipgloss/v2"
)

// tabBarHeight 是 tab 栏的高度（固定 1 行）
const tabBarHeight = 1

// sessionTab 保存一个会话页面的完整状态快照
type sessionTab struct {
	id    int    // 自增 ID
	title string // 显示标题

	// 消息与渲染
	logEntries          []logEntry
	contentParts        []string
	dirtyEntryIndices   map[int]struct{}
	needFullRebuild     bool
	contentPartLinePrefix []int
	contentCache        *strings.Builder
	viewport            viewport.Model // 值复制，保留滚动位置
	viewportYOffset     int            // 保存的滚动偏移量

	// 输入
	inputValue string // textarea 文本（Value/SetValue 恢复）

	// 任务状态
	currentTask   *http.Task
	taskRunning   bool
	taskStarted   bool
	taskCancelled bool

	// 时间线
	timelineEntries  []*TimelineEntry
	timelineExpanded bool
	timelineCache    string
	timelineCacheKey string

	// 工具/AI 流式追踪
	toolCallEntries          map[string]*ToolEntry
	llmCallActiveEntries     map[string]int
	aiStreamActiveEntries    map[string]int
	aiStreamCompletedEntries map[string]int
	aiChunkBuffers           map[string]*aiChunkBuffer

	// 模型信息
	currentModel    string
	currentAgent    string
	currentProvider string

	// Token 统计
	inputTokens              int64
	outputTokens             int64
	cacheCreationInputTokens int64
	cacheReadInputTokens     int64
	totalInputTokens         int64
	tokenUsagePerAgent       map[string]*AgentTokenUsage
	currentAgentRunTokens    AgentRunTokens
}

// initSessionTabs 初始化会话 tab 系统，创建第一个默认 tab
func (m *model) initSessionTabs() {
	m.sessionTabs = make([]*sessionTab, 0, m.maxTabs)
	m.activeSessionIdx = 0
	m.nextTabID = 1

	// 创建第一个默认 tab（快照初始为空，首次 save 时填充）
	defaultTab := m.newSessionTab(m.nextTabID, m.generateTabTitle(m.nextTabID))
	m.sessionTabs = append(m.sessionTabs, defaultTab)
	m.nextTabID++
}

// newSessionTab 创建一个新的空会话 tab
func (m *model) newSessionTab(id int, title string) *sessionTab {
	// 创建默认尺寸的 viewport（会在 resize 时调整）
	vp := viewport.New(viewport.WithWidth(0), viewport.WithHeight(0))
	return &sessionTab{
		id:    id,
		title: title,
		viewport:            vp,
		viewportYOffset:     0,
		logEntries:          make([]logEntry, 0),
		contentParts:        make([]string, 0),
		dirtyEntryIndices:   make(map[int]struct{}),
		needFullRebuild:     true,
		contentPartLinePrefix: make([]int, 0),
		contentCache:        &strings.Builder{},
		inputValue:          "",
		taskRunning:         false,
		taskStarted:         false,
		taskCancelled:       false,
		timelineEntries:     make([]*TimelineEntry, 0),
		timelineExpanded:    false,
		toolCallEntries:     make(map[string]*ToolEntry),
		llmCallActiveEntries:     make(map[string]int),
		aiStreamActiveEntries:    make(map[string]int),
		aiStreamCompletedEntries: make(map[string]int),
		aiChunkBuffers:           make(map[string]*aiChunkBuffer),
		tokenUsagePerAgent:       make(map[string]*AgentTokenUsage),
	}
}

// saveActiveSession 把 model 的所有会话级字段复制进当前 sessionTab
func (m *model) saveActiveSession() {
	if m.activeSessionIdx < 0 || m.activeSessionIdx >= len(m.sessionTabs) {
		return
	}
	tab := m.sessionTabs[m.activeSessionIdx]

	// 保存 viewport 滚动位置
	tab.viewportYOffset = m.viewport.YOffset()

	// 复制消息与渲染状态
	tab.logEntries = make([]logEntry, len(m.logEntries))
	copy(tab.logEntries, m.logEntries)
	tab.contentParts = make([]string, len(m.contentParts))
	copy(tab.contentParts, m.contentParts)
	tab.dirtyEntryIndices = make(map[int]struct{}, len(m.dirtyEntryIndices))
	for k, v := range m.dirtyEntryIndices {
		tab.dirtyEntryIndices[k] = v
	}
	tab.needFullRebuild = m.needFullRebuild
	tab.contentPartLinePrefix = make([]int, len(m.contentPartLinePrefix))
	copy(tab.contentPartLinePrefix, m.contentPartLinePrefix)
	tab.contentCache = m.contentCache

	// 保存 viewport（值复制保留内部内容）
	tab.viewport = m.viewport

	// 保存输入
	tab.inputValue = m.input.Value()

	// 保存任务状态
	tab.currentTask = m.currentTask
	tab.taskRunning = m.taskRunning
	tab.taskStarted = m.taskStarted
	tab.taskCancelled = m.taskCancelled

	// 保存时间线
	tab.timelineEntries = make([]*TimelineEntry, len(m.timelineEntries))
	copy(tab.timelineEntries, m.timelineEntries)
	tab.timelineExpanded = m.timelineExpanded
	tab.timelineCache = m.timelineCache
	tab.timelineCacheKey = m.timelineCacheKey

	// 保存工具/AI 流式追踪
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

	// 保存模型信息
	tab.currentModel = m.currentModel
	tab.currentAgent = m.currentAgent
	tab.currentProvider = m.currentProvider

	// 保存 Token 统计
	tab.inputTokens = m.inputTokens
	tab.outputTokens = m.outputTokens
	tab.cacheCreationInputTokens = m.cacheCreationInputTokens
	tab.cacheReadInputTokens = m.cacheReadInputTokens
	tab.totalInputTokens = m.totalInputTokens
	tab.tokenUsagePerAgent = make(map[string]*AgentTokenUsage, len(m.tokenUsagePerAgent))
	for k, v := range m.tokenUsagePerAgent {
		tab.tokenUsagePerAgent[k] = v
	}
	tab.currentAgentRunTokens = m.currentAgentRunTokens

	// 如果当前有活跃任务，保存其 memory
	if m.currentTask != nil && !m.taskRunning {
		m.saveAndFlushTaskMemory()
	}
}

// restoreSessionTab 把 sessionTab[idx] 的字段复制回 model
func (m *model) restoreSessionTab(idx int) {
	if idx < 0 || idx >= len(m.sessionTabs) {
		return
	}
	tab := m.sessionTabs[idx]

	// 恢复消息与渲染状态
	m.logEntries = tab.logEntries
	m.contentParts = tab.contentParts
	m.dirtyEntryIndices = tab.dirtyEntryIndices
	m.needFullRebuild = tab.needFullRebuild
	m.contentPartLinePrefix = tab.contentPartLinePrefix
	m.contentCache = tab.contentCache
	if m.contentCache == nil {
		m.contentCache = &strings.Builder{}
	}

	// 恢复 viewport
	m.viewport = tab.viewport
	// 按当前终端尺寸修正 viewport
	m.viewport.SetWidth(m.computeFieldWidth())
	vpHeight := m.termHeight - m.computeFooterHeight() - tabBarHeight
	if vpHeight < 3 {
		vpHeight = 3
	}
	m.viewport.SetHeight(vpHeight)

	// 检测宽度变化需要重排
	if tab.viewport.Width() != 0 && tab.viewport.Width() != m.viewport.Width() {
		m.needFullRebuild = true
	}

	// 恢复输入
	m.input.SetValue(tab.inputValue)
	m.input.CursorEnd()

	// 恢复任务状态
	m.currentTask = tab.currentTask
	m.taskRunning = tab.taskRunning
	m.taskStarted = tab.taskStarted
	m.taskCancelled = tab.taskCancelled

	// 恢复时间线
	m.timelineEntries = tab.timelineEntries
	m.timelineExpanded = tab.timelineExpanded
	m.timelineCache = tab.timelineCache
	m.timelineCacheKey = tab.timelineCacheKey

	// 恢复工具/AI 流式追踪
	m.toolCallEntries = tab.toolCallEntries
	m.llmCallActiveEntries = tab.llmCallActiveEntries
	m.aiStreamActiveEntries = tab.aiStreamActiveEntries
	m.aiStreamCompletedEntries = tab.aiStreamCompletedEntries
	m.aiChunkBuffers = tab.aiChunkBuffers

	// 恢复模型信息
	m.currentModel = tab.currentModel
	m.currentAgent = tab.currentAgent
	m.currentProvider = tab.currentProvider

	// 恢复 Token 统计
	m.inputTokens = tab.inputTokens
	m.outputTokens = tab.outputTokens
	m.cacheCreationInputTokens = tab.cacheCreationInputTokens
	m.cacheReadInputTokens = tab.cacheReadInputTokens
	m.totalInputTokens = tab.totalInputTokens
	m.tokenUsagePerAgent = tab.tokenUsagePerAgent
	m.currentAgentRunTokens = tab.currentAgentRunTokens

	// 重建内容缓存并恢复滚动位置
	m.rebuildContentCache()
	m.viewport.SetYOffset(tab.viewportYOffset)

	// 使缓存失效
	m.invalidateFooterCache()
	m.viewportViewValid = false
	m.cachedStatusBar = m.renderAirlineStatusBar()
}

// switchSessionTab 切换到指定索引的 tab（会先保存当前 tab）
func (m *model) switchSessionTab(idx int) {
	if idx < 0 || idx >= len(m.sessionTabs) {
		return
	}
	m.saveActiveSession()
	m.activeSessionIdx = idx
	m.restoreSessionTab(idx)
}

// newSessionTabAction 创建并切换到新 tab
func (m *model) newSessionTabAction() tea.Cmd {
	m.saveActiveSession()
	newTab := m.newSessionTab(m.nextTabID, m.generateTabTitle(m.nextTabID))
	m.sessionTabs = append(m.sessionTabs, newTab)
	m.activeSessionIdx = len(m.sessionTabs) - 1
	m.nextTabID++
	m.restoreSessionTab(m.activeSessionIdx)
	m.infoMsg = langManager.GetText("TabNewCreated")
	return nil
}

// clearCurrentSession 清空当前会话的所有内容
func (m *model) clearCurrentSession() {
	// 先保存当前任务的 memory
	if m.currentTask != nil && !m.taskRunning {
		m.saveAndFlushTaskMemory()
	}

	// 重置所有会话字段
	m.logEntries = nil
	m.contentParts = nil
	m.dirtyEntryIndices = make(map[int]struct{})
	m.needFullRebuild = true
	m.contentPartLinePrefix = nil
	m.contentCache = &strings.Builder{}
	m.viewport.SetContent("")
	m.viewport.GotoTop()
	m.currentTask = nil
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
	m.inputTokens = 0
	m.outputTokens = 0
	m.cacheCreationInputTokens = 0
	m.cacheReadInputTokens = 0
	m.totalInputTokens = 0
	m.tokenUsagePerAgent = make(map[string]*AgentTokenUsage)
	m.currentAgentRunTokens = AgentRunTokens{}
	m.input.SetValue("")

	// 更新 tab 标题为默认
	m.sessionTabs[m.activeSessionIdx].title = m.generateTabTitle(m.sessionTabs[m.activeSessionIdx].id)

	// 保存清空后的状态
	m.saveActiveSession()
	m.infoMsg = langManager.GetText("TabCleared")
}

// closeCurrentSessionTab 关闭当前 tab（至少保留一个）
func (m *model) closeCurrentSessionTab() {
	// 先保存当前任务的 memory
	if m.currentTask != nil && !m.taskRunning {
		m.saveAndFlushTaskMemory()
	}

	if len(m.sessionTabs) <= 1 {
		m.infoMsg = langManager.GetText("TabCloseLastBlocked")
		return
	}

	// 保存当前状态
	m.saveActiveSession()

	// 移除当前 tab
	m.sessionTabs = append(m.sessionTabs[:m.activeSessionIdx], m.sessionTabs[m.activeSessionIdx+1:]...)

	// 修正 activeSessionIdx
	if m.activeSessionIdx >= len(m.sessionTabs) {
		m.activeSessionIdx = len(m.sessionTabs) - 1
	}

	// 恢复新活动 tab
	m.restoreSessionTab(m.activeSessionIdx)
	m.infoMsg = langManager.GetText("TabClosed")
}

// updateActiveTabTitle 更新当前 tab 的标题（基于任务描述）
func (m *model) updateActiveTabTitle(desc string) {
	if m.activeSessionIdx < 0 || m.activeSessionIdx >= len(m.sessionTabs) {
		return
	}
	tab := m.sessionTabs[m.activeSessionIdx]
	// 如果标题是默认标题，才更新
	prefix := langManager.GetText("SessionTitlePrefix")
	defaultTitle := prefix + " " + strconv.Itoa(tab.id)
	if tab.title == defaultTitle {
		// 截断到 12 个 rune
		runes := []rune(desc)
		if len(runes) > 12 {
			tab.title = string(runes[:12]) + "…"
		} else {
			tab.title = string(runes)
		}
	}
}

// generateTabTitle 生成默认 tab 标题
func (m *model) generateTabTitle(id int) string {
	return langManager.GetText("SessionTitlePrefix") + " " + strconv.Itoa(id)
}

// renderTabBar 渲染 1 行 tab 栏
func (m *model) renderTabBar() string {
	if len(m.sessionTabs) == 0 {
		return ""
	}

	// 计算可用宽度
	availableWidth := m.termWidth - 1 // 减去右侧 padding

	// 渲染每个 tab
	var tabStrings []string
	for i, tab := range m.sessionTabs {
		// 格式: "N:title"
		label := fmt.Sprintf("%d:%s", i+1, tab.title)
		if i == m.activeSessionIdx {
			// 活动 tab: 高亮背景
			style := lipgloss.NewStyle().
				Background(lipgloss.Color("39")).
				Foreground(lipgloss.Color("0")).
				Bold(true)
			tabStrings = append(tabStrings, style.Render(label))
		} else {
			// 非活动 tab: 暗色背景
			style := lipgloss.NewStyle().
				Background(lipgloss.Color("236")).
				Foreground(lipgloss.Color("245"))
			tabStrings = append(tabStrings, style.Render(label))
		}
	}

	// 添加"新建"按钮（如果未达到上限）
	if len(m.sessionTabs) < m.maxTabs {
		newBtn := lipgloss.NewStyle().
			Background(lipgloss.Color("237")).
			Foreground(lipgloss.Color("245")).
			Render(" +")
		tabStrings = append(tabStrings, newBtn)
	}

	// 拼接并截断到可用宽度
	result := strings.Join(tabStrings, "")
	if len(result) > availableWidth {
		// 简单截断：保留活动 tab 可见
		// 计算活动 tab 的位置
		var currentLen int
		for i, ts := range tabStrings {
			if i == m.activeSessionIdx {
				// 找到活动 tab 开始的位置
				start := currentLen
				// 截取活动 tab 及其后面的内容
				remaining := result[start:]
				if len(remaining) > availableWidth {
					result = remaining[:availableWidth]
				} else {
					result = remaining
				}
				break
			}
			currentLen += len(ts)
		}
		// 如果截断后不完整，补上省略号
		if strings.HasSuffix(result, "+") && len(result) < availableWidth-1 {
			result = result + strings.Repeat(" ", availableWidth-len(result))
		} else if len(result) < availableWidth {
			result = result + strings.Repeat(" ", availableWidth-len(result))
		}
	} else if len(result) < availableWidth {
		// 填充空格
		result = result + strings.Repeat(" ", availableWidth-len(result))
	}

	return result
}
