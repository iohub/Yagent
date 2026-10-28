package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"yagent/internal/messaging"
	"yagent/internal/thinklink"
	"yagent/internal/toolspec"

	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
)

// computeFooterHeight 返回 viewport 以下所有区块的总行数。
// 必须与 View() 中 footer 的实际拼接逐行对应，否则 viewport 高度算错会导致整屏抖动。
func (m *model) computeFooterHeight() int {
	if m.footerHeightValid {
		return m.cachedFooterHeight
	}

	height := 0

	// 时间线浮层紧贴视口最后一行之下：View() 里那个 "\n" 只是终止视口的最后一行，
	// 并不产生空行，所以这里只算面板自身的高度。
	if !m.dashboardVisible() && (m.taskRunning || len(m.timelineEntries) > 0) {
		if tl := m.renderTimelinePanel(m.termWidth); tl != "" {
			height += lipgloss.Height(tl)
		}
	}

	// 日志与输入面板之间的空行
	height += 1
	height += m.editorBlockHeight()

	if m.keywordAutoComplete && len(m.keywordSuggestions) > 0 {
		height++
	}
	if m.errMsg != "" {
		height++
	}
	if m.infoMsg != "" {
		height++
	}
	if dash := m.tokenDashboardBlock(); dash != "" {
		height += lipgloss.Height(dash)
	}

	// 面板与状态栏之间的空行 + 状态栏本身
	height += 2

	m.cachedFooterHeight = height
	m.footerHeightValid = true
	return height
}

// editorBlockHeight 是输入面板区块的行数（不含前后空行）：
// 上下两条手绘边框 + 编辑模式的 textarea / 命令行展开时的正文行。
func (m *model) editorBlockHeight() int {
	if m.commandMode {
		return m.cmdlineHeight()
	}
	h := 2 + m.computeInputHeight()
	if m.skillAutoComplete && len(m.skillSuggestions) > 0 {
		// 组头（按实际出现的组数算，只有一组时是 1 行）+ 每条建议一行 + 末尾一行操作提示。
		h += m.skillSuggestionHeaderCount() + len(m.skillSuggestions) + 1
	}
	return h
}

// tokenDashboardBlock 返回状态栏上方的 token 面板；dashboard 可见或无用量时为空。
// 前置一个空行：它和输入面板是上下两个圆角框，直接贴在一起会显得挤压。
// 缓存分支也要走同一条加空隙的路径，否则命中缓存与否会改变 footer 行数导致抖动。
func (m *model) tokenDashboardBlock() string {
	if m.dashboardVisible() {
		return ""
	}
	if !m.tokenDashboardValid {
		m.cachedTokenDashboard = m.renderFooterTokenPanel()
		m.tokenDashboardValid = true
	}
	if m.cachedTokenDashboard == "" {
		return ""
	}
	return "\n" + m.cachedTokenDashboard
}

func (m *model) updateViewportCache() {
	m.syncSearchMatches()
	m.cachedViewportView = m.viewport.View()
	m.viewportViewValid = true
}

func (m *model) invalidateFooterCache() {
	m.footerHeightValid = false
	m.cachedStatusBar = ""
	m.statusBarValid = false
	m.cachedTokenDashboard = ""
	m.tokenDashboardValid = false
	m.dashboardCache = ""
	m.dashboardCacheKey = ""
}

func (m *model) getGlamourRenderer(width int) *glamour.TermRenderer {
	if width <= 0 {
		width = 80
	}
	if m.glamourRenderers == nil {
		m.glamourRenderers = make(map[int]*glamour.TermRenderer)
	}
	if r, ok := m.glamourRenderers[width]; ok {
		return r
	}
	glamourStyle := "dark"
	if !m.useDarkStyle {
		glamourStyle = "light"
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(glamourStyle),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		r, _ = glamour.NewTermRenderer(
			glamour.WithStandardStyle(glamourStyle),
			glamour.WithWordWrap(80),
		)
	}
	m.glamourRenderers[width] = r
	return r
}

// resizeViewport 把 viewport 尺寸对齐到 computeBodyLayout。这里曾经是另一套公式
// （不扣滚动条列、也不扣 tab 栏），View 每一轮都要纠正它并重排整段正文。
func (m *model) resizeViewport() {
	l := m.computeBodyLayout(m.bodyTotalLines())
	m.viewport.SetWidth(l.contentWidth)
	m.viewport.SetHeight(l.height)

	if m.termWidth > 0 && l.contentWidth > 0 {
		frameSize := m.viewport.Style.GetHorizontalFrameSize()
		const glamourGutter = 4
		glamourWidth := l.contentWidth - frameSize - glamourGutter
		if glamourWidth < 40 {
			glamourWidth = 40
		}
		m.glamourRenderer = m.getGlamourRenderer(glamourWidth)
	}
	m.updateViewportCache()
}

func (m *model) invalidateRenderedCache() {
	for i := range m.logEntries {
		m.logEntries[i].clearRenderCache()
	}
	m.glamourRenderers = nil
	m.markAllEntriesDirty()
}

func (m *model) buildViewportContent() {
	if m.termWidth <= 0 || m.termHeight <= 0 {
		return
	}
	m.rebuildContentCache()
	m.bodyGotoBottom()
	m.updateViewportCache()
}

// rebuildViewportScrollLock 重排正文；原本贴底就继续贴底（新内容把尾巴推出视野时，
// 最后一条消息会被留在屏幕外）。
func (m *model) rebuildViewportScrollLock() {
	if m.termWidth <= 0 || m.termHeight <= 0 {
		return
	}
	wasAtBottom := m.bodyAtBottom()
	m.rebuildContentCache()
	if wasAtBottom {
		m.bodyGotoBottom()
	} else {
		m.bodySync()
	}
	m.updateViewportCache()
}

// streamRepaintInterval 是流式追加能合并多久再重排一次正文。它必须小于 tick 周期
// （tickCmd 的 100ms），否则"下一拍 tick"永远赶不上窗口到点，尾巴要迟一拍才出现。
const streamRepaintInterval = 80 * time.Millisecond

// streamDirtyHeld 报告当前的脏是不是"只有正在流式的条目被追加了文本"、且合并窗口还没到点。
// 只有那种脏可以推迟：新条目、工具结果、流式定稿、尺寸变化都必须立刻落地。
func (m *model) streamDirtyHeld() bool {
	if time.Since(m.lastViewportRepaint) >= streamRepaintInterval {
		return false
	}
	if m.needFullRebuild || len(m.dirtyEntryIndices) == 0 {
		return false
	}
	for idx := range m.dirtyEntryIndices {
		if idx < 0 || idx >= len(m.logEntries) {
			return false
		}
		le := &m.logEntries[idx]
		if le.eventType != "ai_stream" || !le.streaming {
			return false
		}
	}
	return true
}

// rebuildContentCache 只重排"变过的条目"的正文文本，然后把行索引与窗口落地。
// 它不再拼整段 transcript：拼接与 viewport.SetContent 都是 O(全部历史)，
// 而虚拟化要省掉的正是这个（见 tui_body.go）。
func (m *model) rebuildContentCache() {
	if m.termWidth <= 0 || m.termHeight <= 0 {
		return
	}

	currentWidth := m.viewport.Width()
	widthChanged := currentWidth != m.prevViewportWidth

	// firstChanged 是"从哪一条起需要重新排版"，正文索引按它做增量重建。
	firstChanged := len(m.contentParts)
	renderAll := func() {
		m.contentParts = make([]string, len(m.logEntries))
		for i := range m.logEntries {
			entry := &m.logEntries[i]
			m.contentParts[i] = m.renderSingleEntry(entry, currentWidth)
		}
		firstChanged = 0
	}

	if widthChanged {
		for i := range m.logEntries {
			m.logEntries[i].clearRenderCache()
		}
		renderAll()
		m.prevViewportWidth = currentWidth
		m.needFullRebuild = false
		for k := range m.dirtyEntryIndices {
			delete(m.dirtyEntryIndices, k)
		}
		m.bodyRepaint(0)
		return
	}

	contentChanged := false

	// 两条硬约束，分支顺序不许调换：
	// ① 只能整段重排的两种情况：显式要求全量重建，或者 contentParts 比条目还长 ——
	//    多出来的那截是**另一批条目**的残留（从历史里恢复了一个更短的会话就是这个形状），
	//    逐条补尾部永远修不掉它，留着它下一次排版就会按虚高的条目数去索引 logEntries。
	// ② 增量路径里**先对齐长度、再处理脏下标**（下面那两个 if 的先后）。脏下标写的是
	//    contentParts 这份派生缓存，缓存长度必须先行可信：先跑脏循环就是拿 idx 裸索引一个
	//    可能还没覆盖到它的切片（新条目一边被 markEntryDirty、一边正文还没 append，正是
	//    同一拍）。代价只是"追加与置脏落在同一拍"的那条新条目重复渲染一次；两切片等长的
	//    流式热路径仍然只走脏循环，每消息的增量成本一点没变。
	//    守卫：TestRebuildContentCache_AppendedDirtyEntryAlignsFirst。
	if m.needFullRebuild || len(m.contentParts) > len(m.logEntries) {
		renderAll()
		m.needFullRebuild = false
		for k := range m.dirtyEntryIndices {
			delete(m.dirtyEntryIndices, k)
		}
		contentChanged = true
	} else {
		if len(m.contentParts) < len(m.logEntries) {
			oldLen := len(m.contentParts)
			newParts := make([]string, len(m.logEntries))
			copy(newParts, m.contentParts)
			for i := oldLen; i < len(m.logEntries); i++ {
				entry := &m.logEntries[i]
				newParts[i] = m.renderSingleEntry(entry, currentWidth)
			}
			m.contentParts = newParts
			firstChanged = min(firstChanged, oldLen)
			contentChanged = true
		}

		if len(m.dirtyEntryIndices) > 0 {
			for idx := range m.dirtyEntryIndices {
				if idx < 0 || idx >= len(m.logEntries) {
					// 下标指向一条已经不存在的条目（另一批条目留下的脏）。它永远渲染不出来，
					// 留着会让 hasDirtyEntries() 永远为真、tick 永远不肯停 —— 只能丢。
					delete(m.dirtyEntryIndices, idx)
					continue
				}
				if idx >= len(m.contentParts) {
					// 上面的长度对齐之后不该到这里；真到了就把这个脏下标**留给下一拍**。
					// 在这里 delete 掉等于留下一段永远不会重排的陈旧正文。
					continue
				}
				entry := &m.logEntries[idx]
				entry.clearRenderCache()
				m.contentParts[idx] = m.renderSingleEntry(entry, currentWidth)
				firstChanged = min(firstChanged, idx)
				contentChanged = true
				delete(m.dirtyEntryIndices, idx)
			}
		}
	}

	if contentChanged {
		m.bodyRepaint(firstChanged)
	}
}

// 正文虚拟化之后，"条目 → 文本"由 tui_body.go 的排版块负责，这里只留渲染单条目的入口。

func (m *model) renderSingleEntry(entry *logEntry, width int) string {
	if entry.toolEntry != nil && entry.toolEntry.Status == ToolStatusRunning {
		return renderToolEntryWithAnim(*entry, width, m.anim)
	}

	if entry.eventType == "llm_call_start" && entry.isToolRunning {
		return renderLLMCallWithAnim(*entry, width, m.anim)
	}

	if entry.eventType == "ai_stream" {
		if !entry.streaming {
			// 定稿的流式条目和普通条目一样进渲染缓存：它的内容不会再变，
			// 每次整段重排都重拆重画纯属白做。
			if cached, ok := entry.getCachedRender(width); ok {
				return cached
			}
			rendered := renderStreamEntry(entry, width)
			entry.setCachedRender(rendered, width)
			return rendered
		}
		return renderStreamEntry(entry, width)
	}

	if cached, ok := entry.getCachedRender(width); ok {
		return cached
	}

	if entry.toolEntry != nil {
		if (entry.toolEntry.Call.Name == "deepthinking" || entry.toolEntry.Call.Name == "design") &&
			entry.toolEntry.Status == ToolStatusSuccess &&
			entry.toolEntry.Result != nil &&
			entry.toolEntry.Result.Content != "" &&
			m.glamourRenderer != nil {
			if cached, ok := m.getGlamourCached(entry.toolEntry.Result.Content); ok {
				entry.setCachedRender(cached, width)
				return cached
			}
			rendered := m.renderDeepThinkingEntry(entry)
			m.putGlamourCached(entry.toolEntry.Result.Content, rendered)
			entry.setCachedRender(rendered, width)
			return rendered
		}
		rendered := renderToolEntry(*entry, width)
		entry.setCachedRender(rendered, width)
		return rendered
	}

	if entry.diffText != "" {
		rendered := renderDiff(entry)
		entry.setCachedRender(rendered, width)
		return rendered
	}

	if entry.eventType == "ai_response" {
		if rendered, ok := m.renderAssistantContent(entry.content, width); ok {
			entry.setCachedRender(rendered, width)
			return rendered
		}
	}

	if entry.eventType == "user_message" {
		rendered := renderUserMessageBox(entry.content, width)
		entry.setCachedRender(rendered, width)
		return rendered
	}

	if entry.eventType == "thinking" {
		rendered := formatLogEntry(*entry, width)
		entry.setCachedRender(rendered, width)
		return rendered
	}

	formatted := formatLogEntry(*entry, width)
	entry.setCachedRender(formatted, width)
	return formatted
}

// thinklinkPanelGutter 主页面 thought 面板相对 viewport.Width() 让出的列数：
// 视口左右 padding(2) + 2 列留白，与用户消息框同宽。
const thinklinkPanelGutter = 4

// renderStreamEntry 渲染流式条目：已闭合的 <thought> 块提前渲染成面板（与 markdown
// 只在 ai_response 定稿时才套样式的既有节奏一致），未闭合的尾巴保持原文。
// 面板按块 memo —— 块数量随步数增长，每次刷新整段重画就是二次的开销；
// 输出与"一次性渲染全部块"逐字相同（TestTUI_StreamPanelsMatchOneShot）。
func renderStreamEntry(entry *logEntry, width int) string {
	content := entry.streamContent
	if entry.streaming && content != "" {
		content += "▌"
	}
	blocks, rest := thinklink.SplitThoughtBlocks(content)
	if len(blocks) == 0 {
		return content
	}

	panelW := width - thinklinkPanelGutter
	if entry.streamPanelW != panelW || !sameThoughtBlockPrefix(entry.streamPanelSrc, blocks) {
		// 宽度变了整段重画；块序列被改写（内容被替换而不是追加）同理。
		entry.streamPanels, entry.streamPanelSrc, entry.streamPanelW = nil, nil, panelW
	}
	for _, block := range blocks[len(entry.streamPanels):] {
		entry.streamPanels = append(entry.streamPanels, renderThinklinkPanel(block, panelW))
		entry.streamPanelSrc = append(entry.streamPanelSrc, block)
	}
	return joinThinklinkPanels(strings.Join(entry.streamPanels, "\n"), rest)
}

// sameThoughtBlockPrefix 校验已画的面板对应的那批块还是同一批（流式正文只追加，
// 正常情况下只有末尾会多出来）。
func sameThoughtBlockPrefix(done, blocks []string) bool {
	if len(done) > len(blocks) {
		return false
	}
	for i, block := range done {
		if blocks[i] != block {
			return false
		}
	}
	return true
}

// renderAssistantContent 渲染 assistant 正文：结构化 thought 块渲染成与 thinklink 全屏
// 详情一致的面板，排在正文之前（"先思考后回答"的阅读顺序），其余正文走 markdown。
// 没有 thought 块时与旧实现逐字一致；markdown 渲染失败且没有面板可依附时返回
// ok=false，由调用方退回单行兜底渲染。
func (m *model) renderAssistantContent(content string, width int) (string, bool) {
	blocks, body := thinklink.SplitThoughtBlocks(content)
	hasBlocks := len(blocks) > 0
	if !hasBlocks {
		body = content
	}

	rendered, ok := m.renderMarkdownBody(body)
	if !ok && !hasBlocks {
		return "", false
	}
	if !hasBlocks {
		return rendered, true
	}
	panels := renderThinklinkPanels(blocks, width-thinklinkPanelGutter)
	return joinThinklinkPanels(panels, rendered), true
}

// renderMarkdownBody 渲染去掉 thought 块之后的正文。glamour 不可用或渲染失败时
// 返回原文与 ok=false —— 有 thought 面板时正文宁可原样显示，也不丢整条消息。
func (m *model) renderMarkdownBody(body string) (string, bool) {
	if body == "" {
		return "", true
	}
	if m.glamourRenderer == nil {
		return body, false
	}
	if cached, ok := m.getGlamourCached(body); ok {
		return cached, true
	}
	rendered, err := m.glamourRenderer.Render(body)
	if err != nil {
		return body, false
	}
	m.putGlamourCached(body, rendered)
	return rendered, true
}

// renderThinklinkPanels 把 thought 块逐个渲染成圆角面板（多个块纵向堆叠）。
func renderThinklinkPanels(blocks []string, panelW int) string {
	panels := make([]string, 0, len(blocks))
	for _, block := range blocks {
		panels = append(panels, renderThinklinkPanel(block, panelW))
	}
	return strings.Join(panels, "\n")
}

// renderThinklinkPanel 是单个已闭合块的面板 —— 面板之间互不依赖，所以流式条目可以
// 只画新增的那一个（见 renderStreamEntry）。
func renderThinklinkPanel(block string, panelW int) string {
	innerW := panelW - 4 // 边框(2) + 内边距(2)
	if innerW < 10 {
		innerW, panelW = 10, 14
	}
	lines := thinklinkDetailLines(ThinklinkEntry{Kind: "thought_plan", Content: block}, innerW)
	rows := []string{thinklinkPanelTop("THOUGHT", "", panelW)}
	rows = append(rows, thinklinkPanelRows(lines, innerW)...)
	rows = append(rows, thinklinkPanelBottom(panelW))
	return strings.Join(rows, "\n")
}

// joinThinklinkPanels 把面板与正文纵向拼接，去掉正文尾部多余的空行。
func joinThinklinkPanels(panels, body string) string {
	body = strings.TrimRight(body, "\n")
	switch {
	case panels == "":
		return body
	case body == "":
		return panels
	default:
		return panels + "\n" + body
	}
}

func (m *model) renderDeepThinkingEntry(entry *logEntry) string {
	maxWidth := m.viewport.Width()

	toolEntry := entry.toolEntry
	params := toolEntry.Call.Summary
	if params == "" {
		params = formatToolParams(toolEntry.Call.Name, toolEntry.Call.Arguments)
	}

	header := RenderHeader(toolEntry.Status, toolEntry.Call.Name, params, "")

	mdContent := decodeIfJSONString(toolEntry.Result.Content)
	renderedBody, err := m.getGlamourRenderer(maxWidth - 4).Render(mdContent)
	if err != nil {
		return renderToolEntry(*entry, maxWidth)
	}

	return addToolCallBorders(header, maxWidth) + "\n" + renderedBody
}

// appendLogEntry 追一条已定稿的日志：渲染它的正文，然后把行索引从这一条起补上并落地窗口。
func (m *model) appendLogEntry(entry *logEntry) {
	m.viewportDirty = true
	oldLen := len(m.contentParts)
	m.contentParts = append(m.contentParts, m.renderSingleEntry(entry, m.viewport.Width()))
	m.bodyRepaint(oldLen)
}

func extractToolSummary(toolName string, argsJSON string) string {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}
	switch toolName {
	case "read_file", "edit_file", "search_replace_in_file", "create_file", "delete_file":
		if fp, ok := args["target_file"].(string); ok && fp != "" {
			return fp
		}
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
	// 收尾工具名走 toolspec 的单一来源（其余分支是纯展示用的名字，注册表里没有对应常量）。
	case "thinking", toolspec.ExitToolName:
		if reason, ok := args["reason"].(string); ok && reason != "" {
			return reason
		}
	case "find_function_callee", "find_function_caller":
		if fn, ok := args["function_name"].(string); ok && fn != "" {
			return fn
		}
	}
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

func extractResultBrief(toolName string, result string) string {
	if strings.HasPrefix(result, "Error:") {
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
		var r struct {
			Output string `json:"output"`
		}
		if err := json.Unmarshal([]byte(result), &r); err == nil {
			if r.Output == "" {
				return ""
			}
			trimmed := strings.TrimSpace(r.Output)
			if len(trimmed) > 60 {
				return trimmed[:57] + "..."
			}
			return trimmed
		}
		trimmed := strings.TrimSpace(result)
		if len(trimmed) > 60 {
			return trimmed[:57] + "..."
		}
		return trimmed
	case "search_by_regex", "grep_search":
		lines := strings.Count(result, "\n")
		return fmt.Sprintf("%d matches", lines)
	case "find_function_callee":
		var r struct {
			Callees []interface{} `json:"callees"`
		}
		if err := json.Unmarshal([]byte(result), &r); err == nil {
			return fmt.Sprintf("%d callees", len(r.Callees))
		}
		return ""
	case "find_function_caller":
		var r struct {
			Callers []interface{} `json:"callers"`
		}
		if err := json.Unmarshal([]byte(result), &r); err == nil {
			return fmt.Sprintf("%d callers", len(r.Callers))
		}
		return ""
	default:
		if strings.HasPrefix(toolName, "delegate_") {
			return ""
		}
		return ""
	}
}

func isVerboseEventType(eventType string) bool {
	switch eventType {
	case "tool_call_start", "tool_call_result",
		"llm_call_start", "llm_call_end",
		"commit_context_loaded", "context_compressed", "context_ultimate_compressed",
		"model_info", "thinking":
		return true
	}
	return false
}

func isEditFileTool(toolName string) bool {
	return toolName == "search_replace_in_file"
}

func formatEventAsEntry(event *messaging.MessageEvent) logEntry {
	entry := logEntry{
		timestamp: event.Timestamp,
		eventType: string(event.Type),
		from:      event.From,
	}

	switch event.Type {
	case "ai_stream_start", "ai_chunk", "ai_stream_end":
		return entry
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
				entry.toolEntry = NewToolEntry(ToolCallInfo{
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
			entry.toolEntry = NewToolEntry(ToolCallInfo{
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
				entry.diffText = extractDiffFromResult(result)
			}
		}
		entry.isToolRunning = false
		if entry.content == "" {
			entry.content = fmt.Sprintf("%v", event.Content)
		}
		isErr := strings.HasPrefix(entry.content, "Error:")
		entry.toolEntry = NewToolEntry(ToolCallInfo{
			ID:      entry.toolCallID,
			Name:    entry.toolName,
			Summary: extractToolSummary(entry.toolName, ""),
		})
		if isErr {
			entry.toolEntry.SetResult(ToolResultInfo{
				ToolCallID: entry.toolCallID,
				Name:       entry.toolName,
				Content:    entry.content,
				IsError:    true,
			})
		} else {
			entry.toolEntry.SetResult(ToolResultInfo{
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
	case "commit_context_loaded":
		if m, ok := event.Content.(map[string]interface{}); ok {
			if summaries, ok := m["summaries"].([]interface{}); ok && len(summaries) > 0 {
				var parts []string
				maxShow := len(summaries)
				if maxShow > 3 {
					maxShow = 3
				}
				for i := 0; i < maxShow; i++ {
					sum, ok := summaries[i].(map[string]interface{})
					if !ok {
						continue
					}
					hash := ""
					if h, ok := sum["hash"].(string); ok {
						hash = h[:8]
					}
					req := ""
					if r, ok := sum["requirement"].(string); ok {
						req = r
					}
					if hash != "" && req != "" {
						parts = append(parts, fmt.Sprintf("[%s] %s", hash, req))
					} else if hash != "" {
						parts = append(parts, fmt.Sprintf("[%s]", hash))
					}
				}
				if len(parts) > 0 {
					entry.content = fmt.Sprintf("📦 Commit Knowledge (%d total): %s", len(summaries), strings.Join(parts, " | "))
				} else {
					entry.content = fmt.Sprintf("📦 Loaded %d relevant commit(s)", len(summaries))
				}
			} else {
				if count, ok := m["count"].(float64); ok {
					entry.content = fmt.Sprintf("📦 Loaded %d relevant commit(s)", int(count))
				}
			}
		}
		if entry.content == "" {
			entry.content = fmt.Sprintf("%v", event.Content)
		}
	case "context_compressed", "context_ultimate_compressed":
		if m, ok := event.Content.(map[string]interface{}); ok {
			origTokens := 0
			if v, ok := m["original_tokens"].(int); ok {
				origTokens = v
			} else if v, ok := m["original_tokens"].(float64); ok {
				origTokens = int(v)
			}
			compTokens := 0
			if v, ok := m["compressed_tokens"].(int); ok {
				compTokens = v
			} else if v, ok := m["compressed_tokens"].(float64); ok {
				compTokens = int(v)
			}
			savedPercent := 0.0
			if v, ok := m["saved_percent"].(float64); ok {
				savedPercent = v
			}
			if event.Type == "context_ultimate_compressed" {
				totalPlans := 0
				if v, ok := m["total_plans"].(int); ok {
					totalPlans = v
				} else if v, ok := m["total_plans"].(float64); ok {
					totalPlans = int(v)
				}
				keptPlans := 0
				if v, ok := m["kept_plans"].(int); ok {
					keptPlans = v
				} else if v, ok := m["kept_plans"].(float64); ok {
					keptPlans = int(v)
				}
				userInputs := 0
				if v, ok := m["user_inputs"].(int); ok {
					userInputs = v
				} else if v, ok := m["user_inputs"].(float64); ok {
					userInputs = int(v)
				}
				truncated, _ := m["truncated"].(bool)
				entry.content = fmt.Sprintf("⚡ Ultimate compression: %d → %d tokens, kept %d/%d plans, user_inputs=%d, truncated=%v",
					origTokens, compTokens, keptPlans, totalPlans, userInputs, truncated)
			} else {
				entry.content = fmt.Sprintf("🧠 Context compressed: %d → %d tokens (%.1f%%)", origTokens, compTokens, savedPercent)
			}
		}
		if entry.content == "" {
			entry.content = fmt.Sprintf("%v", event.Content)
		}
	case "llm_call_start":
		if m, ok := event.Content.(map[string]interface{}); ok {
			modelName, _ := m["model"].(string)
			entry.content = fmt.Sprintf("[%s]", modelName)
		} else {
			entry.content = fmt.Sprintf("%v", event.Content)
		}
	case "llm_call_end":
		if durationRaw, ok := event.Metadata["duration_seconds"]; ok {
			var duration float64
			switch v := durationRaw.(type) {
			case float64:
				duration = v
			case int:
				duration = float64(v)
			}

			modelName, _ := event.Metadata["model"].(string)
			if modelName == "" {
				if m, ok := event.Content.(map[string]interface{}); ok {
					modelName, _ = m["model"].(string)
				}
			}

			hasError := false
			if errStr, ok := event.Metadata["error"]; ok && errStr != "" {
				hasError = true
			}

			if hasError {
				entry.content = fmt.Sprintf("✗ [%s] · %.2fs", modelName, duration)
			} else {
				entry.content = fmt.Sprintf("✓ [%s] · %.2fs", modelName, duration)
			}
		} else {
			entry.content = "✓ LLM call completed"
		}
	default:
		if s, ok := event.Content.(string); ok {
			entry.content = s
		} else {
			entry.content = fmt.Sprintf("%v", event.Content)
		}
	}

	entry.isVerbose = isVerboseEventType(string(event.Type))

	if entry.isVerbose && entry.eventType == "tool_call_start" && entry.toolEntry != nil {
		if isEditFileTool(entry.toolEntry.Call.Name) {
			entry.isVerbose = false
		}
	}

	return entry
}

func formatLogEntry(entry logEntry, maxWidth int) string {
	var prefix string
	var contentStyle lipgloss.Style

	switch entry.eventType {
	case "sub_agent_header":
		separator := strings.Repeat("─", maxWidth-2)
		return logSeparatorStyle.Render(" " + separator + " ")

	case "ai_response":
		if entry.from != "" {
			prefix = RenderAgentTag(entry.from) + " "
		} else {
			prefix = "AI  "
		}
		contentStyle = logAIResStyle
	case "user_message":
		return renderUserMessageBox(entry.content, maxWidth)
	case "tool_call_start":
		if entry.toolEntry != nil {
			return renderToolEntry(entry, maxWidth)
		}
		if entry.toolName != "" {
			prefix = "🔘 " + RenderToolName(DisplayToolName(entry.toolName))
		} else {
			prefix = "🔘 TOOL"
		}
		contentStyle = toolRunningStyle
	case "tool_call_result":
		if entry.toolEntry != nil {
			return renderToolEntry(entry, maxWidth)
		}
		if strings.HasPrefix(entry.content, "Error:") {
			if entry.toolName != "" {
				prefix = "❌ " + RenderToolName(DisplayToolName(entry.toolName))
			} else {
				prefix = "❌ RESULT"
			}
			contentStyle = toolErrorStyle
		} else {
			if entry.toolName != "" {
				prefix = "✅ " + RenderToolName(DisplayToolName(entry.toolName))
			} else {
				prefix = "✅ RESULT"
			}
			contentStyle = toolDoneStyle
		}
	case "llm_call_start":
		prefix = ""
		contentStyle = llmCallStyle
	case "llm_call_end":
		prefix = ""
		contentStyle = llmCallEndStyle
	case "thinking":
		prefix = thinkIconStyle.String()
		contentStyle = thinkTextStyle
		contentWidth := maxWidth - 8
		if contentWidth < 20 {
			contentWidth = 20
		}
		runes := []rune(entry.content)
		if len(runes) > contentWidth {
			entry.content = string(runes[:contentWidth-3]) + "..."
		}
		entry.content = strings.ReplaceAll(entry.content, "\n", " ")
	case "error":
		prefix = "✖ ERROR"
		contentStyle = logErrorLogStyle
	case "user_help_needed":
		prefix = "? HELP"
		contentStyle = logToolStyle
	case "context_compressed":
		prefix = ""
		contentStyle = logStatusStyle
	default:
		prefix = "● " + entry.eventType
		contentStyle = logStatusStyle
	}

	var displayContent string
	if entry.executionSummary != "" {
		displayContent = entry.executionSummary
		if entry.resultBrief != "" {
			displayContent += " · " + entry.resultBrief
		}
	} else {
		displayContent = strings.ReplaceAll(entry.content, "\n", " ")
	}

	if entry.prefix != "" {
		displayContent = entry.prefix + displayContent
	}

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

func renderThinkingContinuation(entry logEntry, maxWidth int) string {
	content := strings.ReplaceAll(entry.content, "\n", " ")
	contentWidth := maxWidth - 10
	if contentWidth < 20 {
		contentWidth = 20
	}
	if lipgloss.Width(content) > contentWidth {
		runes := []rune(content)
		if len(runes) > contentWidth-3 {
			content = string(runes[:contentWidth-3]) + "..."
		}
	}
	indent := "      │ "
	return indent + thinkTextStyle.Render(content)
}

func renderUserMessageBox(content string, maxWidth int) string {
	boxWidth := maxWidth - 4
	if boxWidth < 20 {
		wrapped := wrapText(content, boxWidth)
		wrappedLines := strings.Split(wrapped, "\n")
		var lines []string
		lines = append(lines, userPrefixStyle.Render("You:")+" "+wrappedLines[0])
		for i := 1; i < len(wrappedLines); i++ {
			lines = append(lines, "     "+wrappedLines[i])
		}
		return strings.Join(lines, "\n")
	}

	innerWidth := boxWidth - 2
	if innerWidth < 4 {
		innerWidth = 4
	}
	textWidth := innerWidth - 2

	var msgLines []string
	for _, para := range strings.Split(content, "\n") {
		if para == "" {
			msgLines = append(msgLines, "")
		} else {
			wrapped := wrapText(para, textWidth)
			msgLines = append(msgLines, strings.Split(wrapped, "\n")...)
		}
	}
	if len(msgLines) == 0 {
		msgLines = []string{""}
	}

	label := " You "
	labelWidth := lipgloss.Width(label)
	dashCount := boxWidth - labelWidth - 3
	if dashCount < 0 {
		dashCount = 0
	}
	topLine := userMsgBoxBorderStyle.Render("┌─") +
		userPrefixStyle.Render(label) +
		userMsgBoxBorderStyle.Render(strings.Repeat("─", dashCount)+"┐")

	var interior []string
	for _, line := range msgLines {
		lineWidth := lipgloss.Width(line)
		padding := textWidth - lineWidth
		if padding < 0 {
			padding = 0
		}
		interiorLine := userMsgBoxBorderStyle.Render("│") +
			userMsgBoxTextStyle.Render(" "+line+strings.Repeat(" ", padding)+" ") +
			userMsgBoxBorderStyle.Render("│")
		interior = append(interior, interiorLine)
	}

	bottomLine := userMsgBoxBorderStyle.Render("└" + strings.Repeat("─", boxWidth-2) + "┘")

	return topLine + "\n" + strings.Join(interior, "\n") + "\n" + bottomLine
}

func wrapText(text string, maxWidth int) string {
	if maxWidth <= 0 {
		return text
	}
	lines := strings.Split(text, "\n")
	var result []string
	for _, line := range lines {
		if line == "" {
			result = append(result, "")
			continue
		}
		words := strings.Fields(line)
		if len(words) == 0 {
			result = append(result, "")
			continue
		}
		var current string
		var currentWidth int
		for _, word := range words {
			w := lipgloss.Width(word)
			if current == "" {
				current = word
				currentWidth = w
			} else if currentWidth+1+w <= maxWidth {
				current += " " + word
				currentWidth += 1 + w
			} else {
				result = append(result, current)
				current = word
				currentWidth = w
			}
		}
		if current != "" {
			result = append(result, current)
		}
	}
	return strings.Join(result, "\n")
}

func renderToolEntry(entry logEntry, maxWidth int) string {
	if entry.toolEntry == nil {
		return formatLogEntry(entry, maxWidth)
	}

	contentWidth := maxWidth - 4
	if contentWidth < 30 {
		contentWidth = 30
	}
	full := RenderToolLine(entry.toolEntry, nil, contentWidth)

	header, body, hasBody := splitToolRendered(full)
	if !hasBody {
		return full
	}
	return header + "\n" + body
}

func splitToolRendered(full string) (header, body string, hasBody bool) {
	lines := strings.SplitN(full, "\n", 4)
	if len(lines) <= 3 {
		return full, "", false
	}
	header = strings.Join(lines[:3], "\n")
	body = lines[3]
	return header, body, true
}
func renderToolEntryWithAnim(entry logEntry, maxWidth int, anim *Anim) string {
	if entry.toolEntry == nil {
		return formatLogEntry(entry, maxWidth)
	}
	params := entry.toolEntry.Call.Summary
	if params == "" {
		params = entry.executionSummary
	}
	return RenderPending(entry.toolEntry.Call.Name, params, anim)
}

func extractDiffFromResult(result string) string {
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

func renderDiff(entry *logEntry) string {
	var prefix string
	icon := IconSuccess
	if entry.isToolRunning {
		icon = IconPending
	}
	prefix = icon + " " + RenderToolName(DisplayToolName(entry.toolName))
	if entry.executionSummary != "" {
		prefix += " " + ParamMain.Render("· "+entry.executionSummary)
	}

	diffContent := RenderDiffContent(entry.diffText, 100)
	return prefix + "\n" + diffContent
}

func decodeIfJSONString(s string) string {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) >= 2 && trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"' {
		var decoded string
		if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
			return decoded
		}
	}
	return s
}

func renderLLMCallWithAnim(entry logEntry, maxWidth int, anim *Anim) string {
	icon := IconPending
	content := strings.ReplaceAll(entry.content, "\n", " ")
	contentWidth := maxWidth - 20
	if contentWidth < 20 {
		contentWidth = 20
	}
	if lipgloss.Width(content) > contentWidth {
		runes := []rune(content)
		if len(runes) > contentWidth-3 {
			content = string(runes[:contentWidth-3]) + "..."
		}
	}
	return icon + " " + llmCallStyle.Render(content) + " " + anim.Render()
}
