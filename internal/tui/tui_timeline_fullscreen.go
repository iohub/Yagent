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

// ─────────────────────────────────────────────────────────────────────────────
// 全屏 timeline 布局尺寸的唯一口径（internal/tui/AGENTS.md 硬约束 3）。
//
// 改造前这里有四套互相矛盾的公式（视图/init/calc 用 *0.2333、resize 用 *0.35，
// 内容高 termHeight-4 与 termHeight-4-2 并存），任何一处单独改动都会让另几处对不上，
// 造成 resize 时左右分栏比例跳变。现在所有调用点都从这里取尺寸，公式只算一次。
// ─────────────────────────────────────────────────────────────────────────────

const (
	// 全屏外壳固定占用的列数：左右两条发丝边框/内边距的既有口径（avail = termW - 3）。
	timelineShellChrome = 3
	// 全屏外壳固定占用的行数：标题 1 + 状态栏 1 + 上下发丝边框 2。
	timelineShellRows = 4
	// 左右分栏的 floors。窄终端下优先保右侧详情面板。
	timelineLeftFloorW  = 24
	timelineRightFloorW = 30
	// 左列内部：树形区最小可用宽，低于它则放弃瀑布列、把整左列让给树。
	timelineTreeMinW     = 22
	timelineWaterfallMin = 16
	timelineWaterfallMax = 30
	// 防负值的绝对下界。
	timelineMinAvailW = 1
	timelineMinContH  = 3
)

// timelineLayout 是全屏 timeline 一次布局的全部尺寸。render/init/calc/resize
// 四个表面都必须从这里取值，不许各自再算一遍。
type timelineLayout struct {
	TotalW     int // 终端总宽（termW，已 floor 到 >=1）
	ContentH   int // 外壳内的内容高 = termH - timelineShellRows，floor 3
	LeftW      int // 左列外框宽（含边框）
	RightW     int // 右详情外框宽（含边框）；LeftW + RightW == avail
	TreeW      int // 左列内树形区宽；WaterfallW==0 时 == LeftW
	WaterfallW int // 左列内瀑布列宽；可为 0（窄左列时整段让位）
	Gap        int // 树形区与瀑布列之间的间隔
	availWidth int // 内部字段：termW - timelineShellChrome（含 floors），用于不变量
}

// computeTimelineLayout 是全屏 timeline 尺寸的唯一来源。
//
// 不变量（被 TestComputeTimelineLayout_Matrix 全宽度扫描钉死）：
//  1. LeftW + RightW == availWidth（availWidth = max(termW - 3, 1)）
//  2. WaterfallW == 0 时 TreeW == LeftW；否则 TreeW + Gap + WaterfallW == LeftW
//  3. 所有字段非负，ContentH >= 3
//  4. availWidth 足够（>= 54）时 RightW >= 30
func computeTimelineLayout(termW, termH int) timelineLayout {
	if termW < 1 {
		termW = 1
	}
	avail := termW - timelineShellChrome
	if avail < timelineMinAvailW {
		avail = timelineMinAvailW
	}

	contentH := termH - timelineShellRows
	if contentH < timelineMinContH {
		contentH = timelineMinContH
	}

	// LeftW ≈ 56% of avail，整数守恒：RightW 由减法得出，永远 LeftW+RightW==avail。
	leftW := avail * 56 / 100
	if leftW < timelineLeftFloorW {
		leftW = timelineLeftFloorW
	}
	if leftW > avail {
		leftW = avail
	}
	rightW := avail - leftW

	// 窄终端下优先保右详情面板：仅当 avail 能同时容纳两个 floor（>= 54）时才回挤左列，
	// 否则维持减法结果（此时 RightW 可能 < 30，测试只在 avail 足够处断言 RightW>=30）。
	if rightW < timelineRightFloorW && avail >= timelineLeftFloorW+timelineRightFloorW {
		rightW = timelineRightFloorW
		leftW = avail - rightW
	}

	// 左列内部分树 + 瀑布（本阶段仅计算尺寸、不渲染瀑布）。
	gap := 1
	waterfallW := leftW / 3
	if waterfallW < timelineWaterfallMin {
		waterfallW = timelineWaterfallMin
	}
	if waterfallW > timelineWaterfallMax {
		waterfallW = timelineWaterfallMax
	}
	treeW := leftW - gap - waterfallW
	if treeW < timelineTreeMinW {
		// 树形区太窄：整段让位，瀑布列与间隔归零，TreeW==LeftW。
		gap = 0
		waterfallW = 0
		treeW = leftW
	}

	return timelineLayout{
		TotalW:     termW,
		ContentH:   contentH,
		LeftW:      leftW,
		RightW:     rightW,
		TreeW:      treeW,
		WaterfallW: waterfallW,
		Gap:        gap,
		availWidth: avail,
	}
}

func renderTimelineFullscreenView(m *model) tea.View {
	if m.termWidth <= 0 || m.termHeight <= 0 {
		return tea.View{AltScreen: true}
	}

	layout := computeTimelineLayout(m.termWidth, m.termHeight)
	contentHeight := layout.ContentH
	leftWidth := layout.LeftW

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

// Detail 的三种形态：运行中 "[gpt-4]"、完成 "✓ [gpt-4-o] · 3.42s"、错误 "✗ [gpt-4] · 1.20s"，模型名含方括号。
func parseModelFromDetail(detail string) string {
	start := strings.Index(detail, "[")
	end := strings.Index(detail, "]")
	if start >= 0 && end > start {
		return detail[start : end+1] // 返回 "[gpt-4-o]"
	}
	return ""
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

func initTimelineDetailViewport(m *model) {
	layout := computeTimelineLayout(m.termWidth, m.termHeight)
	// viewport 内容宽 = 右详情外框宽 - ThickBorder 左右各 2。
	rightWidth := layout.RightW - 4
	if rightWidth < 1 {
		rightWidth = 1
	}
	contentHeight := layout.ContentH

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

// calcRightPaneWidth 计算右侧详情面板的 viewport 内容宽度（减去 lipgloss ThickBorder 左右各 2 字符）。
// 与 initTimelineDetailViewport 共用 computeTimelineLayout，二者宽度口径必须一致。
func calcRightPaneWidth(m *model) int {
	layout := computeTimelineLayout(m.termWidth, m.termHeight)
	rightWidth := layout.RightW - 4 // -4 for border (2 each side)
	if rightWidth < 1 {
		rightWidth = 1
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
