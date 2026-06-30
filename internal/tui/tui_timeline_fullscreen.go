package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

var (
	focusedBorderStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.ThickBorder()).
				BorderForeground(lipgloss.Color("42")) // bright green

	unfocusedBorderStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.ThickBorder()).
				BorderForeground(lipgloss.Color("236")) // dim gray
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
	if m.timelineDetailVP != nil {
		// 使用 viewport 的 View() 输出作为右侧详情内容
		// viewport 内容由 buildAllTimelineDetails 设置（全量拼接）
		rightDetail = m.timelineDetailVP.View()
	} else {
		rightDetail = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("  (select an entry)")
	}
	statusBar := renderTimelineFullscreenStatusBar(m, m.termWidth)

	// 组装：左侧 + 分隔线 + 右侧
	var leftStyle, rightStyle lipgloss.Style
	if m.timelineFullscreenFocus == "detail" {
		leftStyle = unfocusedBorderStyle
		rightStyle = focusedBorderStyle
	} else {
		leftStyle = focusedBorderStyle
		rightStyle = unfocusedBorderStyle
	}
	leftBlock := leftStyle.Render(leftList)
	rightBlock := rightStyle.Render(rightDetail)

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
	focusHint := "[h]◀List"
	if m.timelineFullscreenFocus == "detail" {
		focusHint = "Detail▶[l]"
	}
	text := fmt.Sprintf(" Timeline │ %d entries │ %s │ j/k Navigate · h/l Switch · esc/q Exit ", entryCount, focusHint)
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

	// 头部：名称（合并条目显示计数）
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("213")) // 粉色
	kindLabel := timelineKindLabel(entry.Kind)
	nameDisplay := entry.Name
	if entry.MergedCount() > 1 {
		nameDisplay = fmt.Sprintf("%s ×%d", nameDisplay, entry.MergedCount())
	}
	sb.WriteString(headerStyle.Render(fmt.Sprintf("%s %s", kindLabel, nameDisplay)))
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
		if len(entry.SubEntries) > 0 {
			// 合并条目：渲染所有子条目（父条目 + SubEntries）
			allEntries := []*TimelineEntry{entry}
			allEntries = append(allEntries, entry.SubEntries...)

			for idx, sub := range allEntries {
				// 子条目编号行
				numStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("248"))
				detailStr := sub.Detail
				if detailStr == "" {
					detailStr = sub.Name
				}
				sb.WriteString(numStyle.Render(fmt.Sprintf("  #%d  %s", idx+1, detailStr)))
				sb.WriteString("\n")

				// 查找关联的 toolEntry 并渲染
				toolEntry := findToolEntryByCallID(m, sub.ID)
				if toolEntry != nil {
					rendered := RenderToolLine(toolEntry, m.anim, width-4)
					if rendered != "" {
						lines := strings.Split(rendered, "\n")
						for _, line := range lines {
							sb.WriteString("  ")
							sb.WriteString(line)
							sb.WriteString("\n")
						}
					}

					// 详情页展示：为 semantic_search 补充完整结果内容
					if toolEntry.Result != nil && toolEntry.Result.Content != "" &&
						toolEntry.Call.Name == "semantic_search" {
						bodyContent := RenderResultBody(toolEntry.Call.Name, toolEntry.Result.Content, width-8)
						if bodyContent != "" {
							bodyLines := strings.Split(bodyContent, "\n")
							for _, line := range bodyLines {
								sb.WriteString("    ")
								sb.WriteString(line)
								sb.WriteString("\n")
							}
						}
					}
					// 没有 toolEntry 时显示简要状态
					statusIcon := "●"
					if sub.Status == ToolStatusRunning {
						statusIcon = "○"
					}
					statusStr := statusTextFor(sub)
					sb.WriteString(fmt.Sprintf("    %s  %s\n", statusIcon, statusStr))
				}

				// 子条目间添加空行分隔
				if idx < len(allEntries)-1 {
					sb.WriteString("\n")
				}
			}
		} else {
			// 单条目：保持原有逻辑
			toolEntry := findToolEntryByCallID(m, entry.ID)
			if toolEntry != nil {
				// 复用 RenderToolLine 渲染工具调用行（header + body）
				rendered := RenderToolLine(toolEntry, m.anim, width)
				sb.WriteString(rendered)

				// 详情页展示：为 semantic_search 补充完整结果内容
				if toolEntry.Result != nil && toolEntry.Result.Content != "" &&
					toolEntry.Call.Name == "semantic_search" {
					bodyContent := RenderResultBody(toolEntry.Call.Name, toolEntry.Result.Content, width-4)
					if bodyContent != "" {
						sb.WriteString("\n")
						sb.WriteString(bodyContent)
					}
				}
			} else {
				sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("  (tool data not available)"))
			}
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
	syncDetailToCursor(m)
}

// refreshTimelineDetail 重建所有条目详情内容，并滚动到当前光标位置
func refreshTimelineDetail(m *model) {
	buildAllTimelineDetails(m)
	syncDetailToCursor(m)
}

// initTimelineDetailViewport 初始化/重置详情 viewport。
func initTimelineDetailViewport(m *model) {
	leftWidth := int(float64(m.termWidth-3) * 0.35)
	if leftWidth < 25 {
		leftWidth = 25
	}
	rightWidth := m.termWidth - 3 - leftWidth - 4 // viewport content width (inside border)
	if rightWidth < 30 {
		rightWidth = 30
	}

	// 内容高度 = termHeight - title(1) - status(1) - 上下大边框(2)
	// viewport 高度需要减去 lipgloss border 的上下各 1 行
	contentHeight := m.termHeight - 4 - 2 // -2 for lipgloss border (top+bottom)
	if contentHeight < 3 {
		contentHeight = 3
	}

	vp := viewport.New()
	vp.SetWidth(rightWidth)
	vp.SetHeight(contentHeight)
	m.timelineDetailVP = &vp

	buildAllTimelineDetails(m)
	syncDetailToCursor(m)
}

// ExitTimelineFullscreen 退出全屏时间线模式，恢复到正常视图。
func (m *model) ExitTimelineFullscreen() {
	m.timelineFullscreenMode = false
	m.timelineFullscreenCursor = 0
	m.timelineFullscreenFocus = "list"
	m.timelineDetailOffsets = nil
	m.timelineDetailVP = nil
}

// calcRightPaneWidth 计算右侧详情面板的 viewport 内容宽度（减去 lipgloss ThickBorder 左右各 2 字符）
func calcRightPaneWidth(m *model) int {
	leftWidth := int(float64(m.termWidth-3) * 0.35)
	if leftWidth < 25 {
		leftWidth = 25
	}
	rightWidth := m.termWidth - 3 - leftWidth - 4 // -4 for border (2 each side)
	if rightWidth < 30 {
		rightWidth = 30
	}
	return rightWidth
}

// buildAllTimelineDetails 将所有 timeline 条目的详情拼接成一个连续页面，
// 计算每个条目的行偏移量，并设置到 viewport 中。不改变滚动位置。
func buildAllTimelineDetails(m *model) {
	entries := m.timelineEntries
	rightWidth := calcRightPaneWidth(m)
	
	var sb strings.Builder
	offsets := make([]int, len(entries))
	currentLine := 0
	
	// 分隔线样式
	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Faint(true)

	for i, entry := range entries {
		offsets[i] = currentLine

		// 在条目之间添加分隔线（第一个条目不加）
		if i > 0 {
			sep := sepStyle.Render(strings.Repeat("─", rightWidth)) + "\n\n"
			sb.WriteString(sep)
			currentLine += strings.Count(sep, "\n")
		}

		// 渲染单个条目的详情（带条目编号锚点）
		detail := renderTimelineFullscreenDetailWithAnchor(m, entry, rightWidth, i)
		sb.WriteString(detail)
		currentLine += strings.Count(detail, "\n")
	}

	m.timelineDetailOffsets = offsets
	
	if m.timelineDetailVP != nil {
		m.timelineDetailVP.SetContent(sb.String())
	}
}

// renderTimelineFullscreenDetailWithAnchor 渲染带锚点编号的单个条目详情
func renderTimelineFullscreenDetailWithAnchor(m *model, entry *TimelineEntry, width int, index int) string {
	if entry == nil {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("  (select an entry)")
	}

	var sb strings.Builder

	// 锚点行（显示条目编号，方便定位）
	anchorStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Faint(true)
	sb.WriteString(anchorStyle.Render(fmt.Sprintf("── Entry #%d ──", index+1)))
	sb.WriteString("\n")

	// 头部行：名称 + 元数据（Time/Duration/Status 在同一行，右对齐）
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("213"))
	metaStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	kindLabel := timelineKindLabel(entry.Kind)
	nameDisplay := entry.Name
	if entry.MergedCount() > 1 {
		nameDisplay = fmt.Sprintf("%s ×%d", nameDisplay, entry.MergedCount())
	}
	// 左侧：类型标签 + 名称
	leftPart := headerStyle.Render(fmt.Sprintf(" %s %s", kindLabel, nameDisplay))
	
	// 右侧：时间 + 耗时 + 状态
	var metaParts []string
	metaParts = append(metaParts, entry.Timestamp.Format("15:04:05.000"))
	if entry.Duration > 0 {
		metaParts = append(metaParts, formatTimelineDuration(entry.Duration))
	}
	statusText := statusTextFor(entry)
	metaParts = append(metaParts, statusText)
	rightPart := metaStyle.Render(strings.Join(metaParts, " · "))
	
	leftWidth := lipgloss.Width(leftPart)
	rightWidth := lipgloss.Width(rightPart)
	padding := width - leftWidth - rightWidth - 2
	if padding < 1 {
		padding = 1
	}
	sb.WriteString(leftPart)
	sb.WriteString(strings.Repeat(" ", padding))
	sb.WriteString(rightPart)
	sb.WriteString("\n")

	// 分隔线
	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(strings.Repeat("─", width)))
	sb.WriteString("\n\n")

	// 主体内容 — 复用原有的 renderTimelineFullscreenDetail 逻辑
	sb.WriteString(renderTimelineDetailBody(m, entry, width))

	return sb.String()
}

// renderTimelineDetailBody 渲染条目详情的主体内容（不含头部元数据）
func renderTimelineDetailBody(m *model, entry *TimelineEntry, width int) string {
	var sb strings.Builder
	
	switch entry.Kind {
	case TimelineKindTool:
		if len(entry.SubEntries) > 0 {
			allEntries := []*TimelineEntry{entry}
			allEntries = append(allEntries, entry.SubEntries...)

			for idx, sub := range allEntries {
				numStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("248"))
				detailStr := sub.Detail
				if detailStr == "" {
					detailStr = sub.Name
				}
				sb.WriteString(numStyle.Render(fmt.Sprintf("  #%d  %s", idx+1, detailStr)))
				sb.WriteString("\n")

				toolEntry := findToolEntryByCallID(m, sub.ID)
				if toolEntry != nil {
					rendered := RenderToolLine(toolEntry, m.anim, width-4)
					if rendered != "" {
						lines := strings.Split(rendered, "\n")
						for _, line := range lines {
							sb.WriteString("  ")
							sb.WriteString(line)
							sb.WriteString("\n")
						}
					}

					if toolEntry.Result != nil && toolEntry.Result.Content != "" &&
						toolEntry.Call.Name == "semantic_search" {
						bodyContent := RenderResultBody(toolEntry.Call.Name, toolEntry.Result.Content, width-8)
						if bodyContent != "" {
							bodyLines := strings.Split(bodyContent, "\n")
							for _, line := range bodyLines {
								sb.WriteString("    ")
								sb.WriteString(line)
								sb.WriteString("\n")
							}
						}
					}
				} else {
					statusIcon := "●"
					if sub.Status == ToolStatusRunning {
						statusIcon = "○"
					}
					statusStr := statusTextFor(sub)
					sb.WriteString(fmt.Sprintf("    %s  %s\n", statusIcon, statusStr))
				}

				if idx < len(allEntries)-1 {
					sb.WriteString("\n")
				}
			}
		} else {
			toolEntry := findToolEntryByCallID(m, entry.ID)
			if toolEntry != nil {
				rendered := RenderToolLine(toolEntry, m.anim, width)
				sb.WriteString(rendered)

				if toolEntry.Result != nil && toolEntry.Result.Content != "" &&
					toolEntry.Call.Name == "semantic_search" {
					bodyContent := RenderResultBody(toolEntry.Call.Name, toolEntry.Result.Content, width-4)
					if bodyContent != "" {
						sb.WriteString("\n")
						sb.WriteString(bodyContent)
					}
				}
			} else {
				sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("  (tool data not available)"))
			}
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

// syncDetailToCursor 将详情 viewport 滚动到当前光标条目对应的位置
func syncDetailToCursor(m *model) {
	if m.timelineDetailVP == nil {
		return
	}
	if len(m.timelineDetailOffsets) == 0 {
		return
	}
	cursor := m.timelineFullscreenCursor
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(m.timelineDetailOffsets) {
		cursor = len(m.timelineDetailOffsets) - 1
	}
	targetOffset := m.timelineDetailOffsets[cursor]
	m.timelineDetailVP.SetYOffset(targetOffset)
}
