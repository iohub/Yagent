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

	leftWidth := int(float64(m.termWidth-3) * 0.2333)
	if leftWidth < 25 {
		leftWidth = 25
	}
	rightWidth := m.termWidth - 3 - leftWidth
	if rightWidth < 30 {
		rightWidth = 30
	}

	titleBar := renderTimelineFullscreenTitleBar(m, m.termWidth)
	leftList := renderTimelineFullscreenList(m, leftWidth, contentHeight)
	var rightDetail string
	if m.timelineDetailVP != nil {
		// 详情内容由 buildAllTimelineDetails 全量拼进 viewport，这里只取它的 View()
		rightDetail = m.timelineDetailVP.View()
	} else {
		rightDetail = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("  (select an entry)")
	}
	statusBar := renderTimelineFullscreenStatusBar(m, m.termWidth)

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

	fullContent := strings.Join([]string{
		titleBar,
		contentArea,
		statusBar,
	}, "\n")

	topBorder := lipgloss.NewStyle().Foreground(lipgloss.Color("236")).Render(strings.Repeat("─", m.termWidth))
	bottomBorder := lipgloss.NewStyle().Foreground(lipgloss.Color("236")).Render(strings.Repeat("─", m.termWidth))
	fullContent = topBorder + "\n" + fullContent + "\n" + bottomBorder

	return tea.View{AltScreen: true, Content: fullContent}
}

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

// 采用窗口渲染（非 viewport，因为条目单行）。
func renderTimelineFullscreenList(m *model, width, height int) string {
	if len(m.timelineEntries) == 0 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("  (no entries)")
	}

	entries := m.timelineEntries
	n := len(entries)

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

	if start > 0 {
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("  ▲ ..."))
	}

	for i := start; i < end; i++ {
		isCursor := i == m.timelineFullscreenCursor
		line := renderTimelineFullscreenListItem(entries[i], width, isCursor)
		lines = append(lines, line)
	}

	if end < n {
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("  ▼ ..."))
	}

	return strings.Join(lines, "\n")
}

// 行样式与非全屏一致（dot+name+duration），另加光标指示与选中高亮。
func renderTimelineFullscreenListItem(entry *TimelineEntry, width int, isCursor bool) string {
	// 宽度预留 -2 用于边框内边距
	contentWidth := width - 2
	if contentWidth < 20 {
		contentWidth = 20
	}
	return renderTimelineRowWithCursor(entry, contentWidth, nil, isCursor)
}

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

func renderTimelineFullscreenDetail(m *model, entry *TimelineEntry, width int) string {
	if entry == nil {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("  (select an entry)")
	}

	var sb strings.Builder

	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("213")) // 粉色
	modelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))             // 灰色
	durationStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))          // 灰色

	nameDisplay := entry.Name
	if entry.MergedCount() > 1 {
		nameDisplay = fmt.Sprintf("%s ×%d", nameDisplay, entry.MergedCount())
	}
	if entry.Kind == TimelineKindLLMCall {
		sb.WriteString(headerStyle.Render(fmt.Sprintf("%s %s", timelineKindLabel(entry.Kind), nameDisplay)))
	} else {
		sb.WriteString(headerStyle.Render(nameDisplay))
	}

	if entry.Kind == TimelineKindLLMCall {
		if entry.Detail != "" {
			if modelPart := parseModelFromDetail(entry.Detail); modelPart != "" {
				sb.WriteString("  ")
				sb.WriteString(modelStyle.Render(modelPart))
			}
		}
		if icon, color := statusIconFor(entry); icon != "" {
			iconStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true)
			sb.WriteString("  ")
			sb.WriteString(iconStyle.Render(icon))
		}
		if entry.Duration > 0 {
			sb.WriteString("  ")
			sb.WriteString(durationStyle.Render(formatTimelineDuration(entry.Duration)))
		}
	}

	sb.WriteString("\n")

	metaStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	metaParts := []string{
		fmt.Sprintf("  Time: %s", entry.Timestamp.Format("15:04:05.000")),
	}
	// Duration在LLMCall的头部行已展示，避免重复
	if entry.Duration > 0 && entry.Kind != TimelineKindLLMCall {
		metaParts = append(metaParts, fmt.Sprintf("Duration: %s", formatTimelineDuration(entry.Duration)))
	}
	statusText := statusTextFor(entry)
	metaParts = append(metaParts, fmt.Sprintf("Status: %s", statusText))
	sb.WriteString(metaStyle.Render(strings.Join(metaParts, "  │  ")))
	sb.WriteString("\n")

	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(strings.Repeat("─", width)))
	sb.WriteString("\n\n")

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
		// 模型名称、状态图标、耗时已在头部行展示，此处不再重复
	case TimelineKindThinking:
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

// Detail 的三种形态：运行中 "[gpt-4]"、完成 "✓ [gpt-4-o] · 3.42s"、错误 "✗ [gpt-4] · 1.20s"，模型名含方括号。
func parseModelFromDetail(detail string) string {
	start := strings.Index(detail, "[")
	end := strings.Index(detail, "]")
	if start >= 0 && end > start {
		return detail[start : end+1] // 返回 "[gpt-4-o]"
	}
	return ""
}

func statusIconFor(entry *TimelineEntry) (icon string, colorStr string) {
	switch entry.Status {
	case ToolStatusSuccess:
		return "✓", "82" // 绿色
	case ToolStatusError:
		return "✗", "196" // 红色
	case ToolStatusRunning:
		return "⟳", "220" // 黄色
	default:
		return "", "245" // 无图标
	}
}

// 倒序扫 logEntries：同一 callID 取最近的一条。
func findToolEntryByCallID(m *model, callID string) *ToolEntry {
	for i := len(m.logEntries) - 1; i >= 0; i-- {
		entry := &m.logEntries[i]
		if entry.toolCallID == callID && entry.toolEntry != nil {
			return entry.toolEntry
		}
	}
	return nil
}

func timelineKindLabel(kind TimelineKind) string {
	switch kind {
	case TimelineKindTool:
		return "[T]"
	case TimelineKindLLMCall:
		return "[L]"
	case TimelineKindThinking:
		return "[Tn]"
	case TimelineKindContextEvent:
		return "[C]"
	default:
		return "[?]"
	}
}

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

func refreshTimelineDetail(m *model) {
	buildAllTimelineDetails(m)
	syncDetailToCursor(m)
}

func initTimelineDetailViewport(m *model) {
	leftWidth := int(float64(m.termWidth-3) * 0.2333)
	if leftWidth < 25 {
		leftWidth = 25
	}
	rightWidth := m.termWidth - 3 - leftWidth - 4 // viewport content width (inside border)
	if rightWidth < 30 {
		rightWidth = 30
	}

	// -4 = title(1) + status(1) + 上下大边框(2)
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

func (m *model) ExitTimelineFullscreen() {
	m.timelineFullscreenMode = false
	m.timelineFullscreenCursor = 0
	m.timelineFullscreenFocus = "list"
	m.timelineDetailOffsets = nil
	m.timelineDetailVP = nil
}

// calcRightPaneWidth 计算右侧详情面板的 viewport 内容宽度（减去 lipgloss ThickBorder 左右各 2 字符）
func calcRightPaneWidth(m *model) int {
	leftWidth := int(float64(m.termWidth-3) * 0.2333)
	if leftWidth < 25 {
		leftWidth = 25
	}
	rightWidth := m.termWidth - 3 - leftWidth - 4 // -4 for border (2 each side)
	if rightWidth < 30 {
		rightWidth = 30
	}
	return rightWidth
}

// LLM Call 条目与紧随其后的 Thinking 条目合并展示为一个整体。
func buildAllTimelineDetails(m *model) {
	entries := m.timelineEntries
	rightWidth := calcRightPaneWidth(m)

	var sb strings.Builder
	offsets := make([]int, len(entries))
	currentLine := 0

	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Faint(true)

	mergedThinkings := make(map[int]bool) // thinking index → merged
	mergedContent := make(map[int]string) // llm_call index → thinking content

	for i, entry := range entries {
		if i > 0 && entry.Kind == TimelineKindThinking && entries[i-1].Kind == TimelineKindLLMCall {
			mergedThinkings[i] = true
			content := findFullThinkingContent(m, entry)
			if content != "" {
				mergedContent[i-1] = content
			}
		}
	}

	for i, entry := range entries {
		offsets[i] = currentLine

		if mergedThinkings[i] {
			continue
		}

		// 条目之间的分隔线（跳过被合并的 thinking 前后的分隔）
		if i > 0 {
			isPrevMerged := mergedThinkings[i-1]
			if !isPrevMerged {
				sep := sepStyle.Render(strings.Repeat("─", rightWidth)) + "\n\n"
				sb.WriteString(sep)
				currentLine += strings.Count(sep, "\n")
			}
		}

		detail := renderTimelineFullscreenDetailWithAnchor(m, entry, rightWidth, i)
		sb.WriteString(detail)
		currentLine += strings.Count(detail, "\n")

		if content, ok := mergedContent[i]; ok {
			inlineContent := renderThinkingInline(content, rightWidth)
			sb.WriteString(inlineContent)
			currentLine += strings.Count(inlineContent, "\n")
		}
	}

	m.timelineDetailOffsets = offsets

	if m.timelineDetailVP != nil {
		m.timelineDetailVP.SetContent(sb.String())
	}
}

func renderTimelineFullscreenDetailWithAnchor(m *model, entry *TimelineEntry, width int, index int) string {
	if entry == nil {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("  (select an entry)")
	}

	var sb strings.Builder

	// 头部行：名称 + 元数据（Time/Duration/Status 在同一行，右对齐）
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("213"))
	metaStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	nameDisplay := entry.Name
	if entry.MergedCount() > 1 {
		nameDisplay = fmt.Sprintf("%s ×%d", nameDisplay, entry.MergedCount())
	}
	var leftBase string
	if entry.Kind == TimelineKindLLMCall {
		leftBase = fmt.Sprintf(" %s %s", timelineKindLabel(entry.Kind), nameDisplay)
	} else {
		leftBase = fmt.Sprintf(" %s", nameDisplay)
	}

	if entry.Kind == TimelineKindLLMCall {
		if entry.Detail != "" {
			if modelPart := parseModelFromDetail(entry.Detail); modelPart != "" {
				leftBase = leftBase + " " + modelPart
			}
		}
	}

	leftPart := headerStyle.Render(leftBase)

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

	sb.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(strings.Repeat("─", width)))
	sb.WriteString("\n\n")

	sb.WriteString(renderTimelineDetailBody(m, entry, width))

	return sb.String()
}

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
		// 模型名称、状态图标、耗时已在头部行展示，此处不再重复
	case TimelineKindThinking:
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

// 通过匹配 eventType="thinking" 和时间戳精确关联 logEntries。
func findFullThinkingContent(m *model, thinkingEntry *TimelineEntry) string {
	ts := thinkingEntry.Timestamp
	for i := len(m.logEntries) - 1; i >= 0; i-- {
		le := &m.logEntries[i]
		if le.eventType == "thinking" && le.timestamp.Equal(ts) {
			return le.content
		}
	}
	return thinkingEntry.Detail
}

func renderThinkingInline(thinkingContent string, width int) string {
	var sb strings.Builder

	contentWidth := width - 6 // 缩进空间
	if contentWidth < 30 {
		contentWidth = 30
	}

	lines := strings.Split(thinkingContent, "\n")

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			sb.WriteString("\n")
			continue
		}

		displayLine := trimmed
		runes := []rune(displayLine)
		if len(runes) > contentWidth {
			displayLine = string(runes[:contentWidth-3]) + "..."
		}

		if i == 0 {
			sb.WriteString("  ")
			sb.WriteString(thinkIconStyle.String())
			sb.WriteString(thinkTextStyle.Render(displayLine))
			sb.WriteString("\n")
		} else {
			padding := 4
			if strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t") {
				padding = 4
			}
			sb.WriteString(strings.Repeat(" ", padding))
			sb.WriteString(thinkTextStyle.Render(displayLine))
			sb.WriteString("\n")
		}
	}

	return sb.String()
}
