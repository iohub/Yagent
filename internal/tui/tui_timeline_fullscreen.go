package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// renderTimelineFullscreenView 全屏时间线主渲染函数。
// 分屏布局：左侧 ~35% 为 timeline 条目列表（带光标选择），
// 右侧 ~65% 为选中条目的详细内容。
func renderTimelineFullscreenView(m *model) tea.View {
	if m.termWidth <= 0 || m.termHeight <= 0 {
		return tea.View{AltScreen: true}
	}

	titleHeight := 1
	statusHeight := 1
	borderPad := 2
	contentHeight := m.termHeight - titleHeight - statusHeight - borderPad
	if contentHeight < 3 {
		contentHeight = 3
	}

	leftWidth := int(float64(m.termWidth-3) * 0.35)
	if leftWidth < 25 {
		leftWidth = 25
	}
	rightWidth := m.termWidth - 3 - leftWidth
	if rightWidth < 30 {
		rightWidth = 30
	}

	// 渲染各区域
	titleBar := renderTimelineFullscreenTitleBar(m, m.termWidth)
	leftList := renderTimelineFullscreenList(m, leftWidth, contentHeight)
	var rightDetail string
	if m.timelineFullscreenCursor >= 0 && m.timelineFullscreenCursor < len(m.timelineEntries) {
		rightDetail = renderTimelineFullscreenDetail(m, m.timelineEntries[m.timelineFullscreenCursor], rightWidth)
	} else {
		rightDetail = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("  (select an entry)")
	}
	statusBar := renderTimelineFullscreenStatusBar(m, m.termWidth)

	// 组装：左侧 + 分隔线 + 右侧
	leftBlock := lipgloss.NewStyle().
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(lipgloss.Color("236")).
		Render(leftList)

	rightBlock := lipgloss.NewStyle().
		BorderStyle(lipgloss.ThickBorder()).
		BorderForeground(lipgloss.Color("236")).
		Render(rightDetail)

	contentArea := lipgloss.JoinHorizontal(lipgloss.Top, leftBlock, " ", rightBlock)

	// 完整布局：标题栏 + 内容区 + 状态栏
	fullContent := strings.Join([]string{
		titleBar,
		contentArea,
		statusBar,
	}, "\n")

	// 上下边框
	topBorder := lipgloss.NewStyle().Foreground(lipgloss.Color("236")).Render(strings.Repeat("─", m.termWidth))
	bottomBorder := lipgloss.NewStyle().Foreground(lipgloss.Color("236")).Render(strings.Repeat("─", m.termWidth))
	fullContent = topBorder + "\n" + fullContent + "\n" + bottomBorder

	return tea.View{AltScreen: true, Content: fullContent}
}

// renderTimelineFullscreenTitleBar 渲染标题栏。
// 深色背景 + 白色加粗字体。
func renderTimelineFullscreenTitleBar(m *model, width int) string {
	titleStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("62")).
		Foreground(lipgloss.Color("255")).
		Bold(true).
		Width(width - 2)

	entryCount := len(m.timelineEntries)
	text := fmt.Sprintf(" Timeline │ %d entries │ j/k Navigate · esc/q Exit ", entryCount)
	return titleStyle.Render(text)
}

// renderTimelineFullscreenList 渲染左侧 timeline 条目列表。
// 采用窗口渲染（非 viewport，因为条目单行）。
func renderTimelineFullscreenList(m *model, width, height int) string {
	if len(m.timelineEntries) == 0 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("  (no entries)")
	}

	entries := m.timelineEntries
	n := len(entries)

	// 计算可见窗口：保持光标居中
	half := height / 2
	start := m.timelineFullscreenCursor - half
	if start < 0 {
		start = 0
	}
	end := start + height
	if end > n {
		end = n
	}
	if start >= n {
		start = n - 1
		end = n
	}
	if end <= 0 {
		end = 1
	}

	var lines []string

	// 顶部省略指示
	if start > 0 {
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("  ▲ ..."))
	}

	// 遍历可见条目
	for i := start; i < end; i++ {
		isCursor := i == m.timelineFullscreenCursor
		line := renderTimelineFullscreenListItem(entries[i], width, isCursor)
		lines = append(lines, line)
	}

	// 底部省略指示
	if end < n {
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("  ▼ ..."))
	}

	return strings.Join(lines, "\n")
}

// renderTimelineFullscreenListItem 渲染单个 timeline 条目行。
// 使用与非全屏一致的 dot+name+duration 样式（更好看），
// 并添加光标指示器▸和选中高亮。
func renderTimelineFullscreenListItem(entry *TimelineEntry, width int, isCursor bool) string {
	// 宽度预留 -2 用于边框内边距
	contentWidth := width - 2
	if contentWidth < 20 {
		contentWidth = 20
	}
	return renderTimelineRowWithCursor(entry, contentWidth, nil, isCursor)
}

// renderTimelineFullscreenStatusBar 渲染底部状态栏。
// 深色背景。
func renderTimelineFullscreenStatusBar(m *model, width int) string {
	statusStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("235")).
		Foreground(lipgloss.Color("250")).
		Width(width - 2)

	if len(m.timelineEntries) == 0 {
		text := "  (no entries) "
		return statusStyle.Render(text)
	}

	cursor := m.timelineFullscreenCursor
	if cursor >= len(m.timelineEntries) {
		cursor = len(m.timelineEntries) - 1
	}
	if cursor < 0 {
		cursor = 0
	}
	entry := m.timelineEntries[cursor]

	posText := fmt.Sprintf(" %d/%d ", cursor+1, len(m.timelineEntries))
	kindLabel := timelineKindLabel(entry.Kind)
	statusText := statusTextFor(entry)

	// 截断名称
	nameStr := entry.Name
	maxNameWidth := width - lipgloss.Width(posText) - lipgloss.Width(kindLabel) - lipgloss.Width(statusText) - 12
	if maxNameWidth < 5 {
		maxNameWidth = 5
	}
	if len(nameStr) > maxNameWidth {
		nameStr = nameStr[:maxNameWidth-1] + "…"
	}

	text := fmt.Sprintf("%s│ %s │ %s │ %s │", posText, kindLabel, nameStr, statusText)
	return statusStyle.Render(text)
}

// renderTimelineFullscreenDetail 渲染右侧详情 viewport 的内容。
// 复用现有的 RenderToolLine 渲染工具调用行。
func renderTimelineFullscreenDetail(m *model, entry *TimelineEntry, width int) string {
	if entry == nil {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("  (select an entry)")
	}

	var sb strings.Builder

	// 头部：名称
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("213")) // 粉色
	kindLabel := timelineKindLabel(entry.Kind)
	sb.WriteString(headerStyle.Render(fmt.Sprintf("%s %s", kindLabel, entry.Name)))
	sb.WriteString("\n")

	// 元数据行
	metaStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	metaParts := []string{
		fmt.Sprintf("  Time: %s", entry.Timestamp.Format("15:04:05.000")),
	}
	if entry.Duration > 0 {
		metaParts = append(metaParts, fmt.Sprintf("Duration: %s", formatTimelineDuration(entry.Duration)))
	}
	statusText := statusTextFor(entry)
	metaParts = append(metaParts, fmt.Sprintf("Status: %s", statusText))
	sb.WriteString(metaStyle.Render(strings.Join(metaParts, "  │  ")))
	sb.WriteString("\n")

	// 分隔线
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(strings.Repeat("─", width)))
	sb.WriteString("\n\n")

	// 主体内容（按类型分发）
	switch entry.Kind {
	case TimelineKindTool:
		// 查找关联的 toolEntry
		toolEntry := findToolEntryByCallID(m, entry.ID)
		if toolEntry != nil {
			// 复用 RenderToolLine 渲染工具调用行（header + body）
			rendered := RenderToolLine(toolEntry, m.anim, width)
			sb.WriteString(rendered)
		} else {
			sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("  (tool data not available)"))
		}
	case TimelineKindLLMCall:
		if entry.Detail != "" {
			sb.WriteString("  ")
			sb.WriteString(entry.Detail)
		} else {
			sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("  (no details available)"))
		}
	case TimelineKindContextEvent:
		if entry.Detail != "" {
			sb.WriteString("  ")
			sb.WriteString(entry.Detail)
		} else {
			sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("  (no details available)"))
		}
	default:
		sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("  (unknown entry type)"))
	}

	return sb.String()
}

// findToolEntryByCallID 通过 callID 在 logEntries 中查找关联的 ToolEntry。
// 从后往前搜索，找到 toolCallID == callID && toolEntry != nil 的条目，返回其 toolEntry。
func findToolEntryByCallID(m *model, callID string) *ToolEntry {
	for i := len(m.logEntries) - 1; i >= 0; i-- {
		entry := &m.logEntries[i]
		if entry.toolCallID == callID && entry.toolEntry != nil {
			return entry.toolEntry
		}
	}
	return nil
}

// timelineKindLabel 返回类型标签。
func timelineKindLabel(kind TimelineKind) string {
	switch kind {
	case TimelineKindTool:
		return "[T]"
	case TimelineKindLLMCall:
		return "[L]"
	case TimelineKindContextEvent:
		return "[C]"
	default:
		return "[?]"
	}
}

// statusTextFor 返回状态文本。
func statusTextFor(entry *TimelineEntry) string {
	switch entry.Status {
	case ToolStatusError:
		return "ERROR"
	case ToolStatusRunning:
		return "Running"
	case ToolStatusSuccess:
		return "Success"
	case ToolStatusPending:
		return "Pending"
	case ToolStatusCanceled:
		return "Canceled"
	default:
		return "Unknown"
	}
}

// moveTimelineCursor 移动并更新详情 viewport。
func moveTimelineCursor(m *model, delta int) {
	n := len(m.timelineEntries)
	if n == 0 {
		return
	}
	m.timelineFullscreenCursor += delta
	if m.timelineFullscreenCursor < 0 {
		m.timelineFullscreenCursor = 0
	}
	if m.timelineFullscreenCursor >= n {
		m.timelineFullscreenCursor = n - 1
	}
	refreshTimelineDetail(m)
}

// refreshTimelineDetail 重新生成右侧详情 viewport 的内容，并重置到顶部。
func refreshTimelineDetail(m *model) {
	if m.timelineDetailVP == nil {
		return
	}
	if len(m.timelineEntries) == 0 {
		m.timelineDetailVP.SetContent("")
		return
	}

	cursor := m.timelineFullscreenCursor
	if cursor < 0 || cursor >= len(m.timelineEntries) {
		m.timelineDetailVP.SetContent("")
		return
	}

	entry := m.timelineEntries[cursor]

	// 计算右侧宽度（减去边框和 padding）
	leftWidth := int(float64(m.termWidth-3) * 0.35)
	if leftWidth < 25 {
		leftWidth = 25
	}
	rightWidth := m.termWidth - 3 - leftWidth - 4 // -4 for padding+border
	if rightWidth < 30 {
		rightWidth = 30
	}

	content := renderTimelineFullscreenDetail(m, entry, rightWidth)
	m.timelineDetailVP.SetContent(content)
	m.timelineDetailVP.GotoTop()
}

// initTimelineDetailViewport 初始化/重置详情 viewport。
func initTimelineDetailViewport(m *model) {
	leftWidth := int(float64(m.termWidth-3) * 0.35)
	if leftWidth < 25 {
		leftWidth = 25
	}
	rightWidth := m.termWidth - 3 - leftWidth - 4

	contentHeight := m.termHeight - 4 // title + status + 2 borders
	if contentHeight < 3 {
		contentHeight = 3
	}

	vp := viewport.New()
	vp.SetWidth(rightWidth)
	vp.SetHeight(contentHeight)
	m.timelineDetailVP = &vp

	refreshTimelineDetail(m)
}

// ExitTimelineFullscreen 退出全屏时间线模式，恢复到正常视图。
func (m *model) ExitTimelineFullscreen() {
	m.timelineFullscreenMode = false
	m.timelineFullscreenCursor = 0
	// 重置 viewport 以便重新渲染正常界面
	m.timelineDetailVP = nil
}
