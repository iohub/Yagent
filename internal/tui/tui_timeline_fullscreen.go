package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

var timelineNowFunc = time.Now

var (
	focusedBorderStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.ThickBorder()).
				BorderForeground(lipgloss.Color("42"))

	unfocusedBorderStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.ThickBorder()).
				BorderForeground(lipgloss.Color("236"))
)

const (
	timelineShellChrome = 3

	timelineShellRows = 4

	timelineTreeMinW     = 16
	timelineWaterfallMin = 8
	timelineWaterfallMax = 20

	timelineMinAvailW = 1
	timelineMinContH  = 3
)

type timelineLayout struct {
	TotalW     int
	ContentH   int
	LeftW      int
	RightW     int
	TreeW      int
	WaterfallW int
	Gap        int
	availWidth int
}

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

	leftW := termW / 4
	if leftW > avail {
		leftW = avail
	}
	if leftW < 1 {
		leftW = 1
	}
	rightW := avail - leftW

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

	titleBar := renderTimelineFullscreenTitleBar(m, m.termWidth)

	leftList := renderTimelineFullscreenLeft(m, layout)

	var rightDetail string
	if m.timelineDetailVP != nil {
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

func renderTimelineFullscreenLeft(m *model, layout timelineLayout) string {
	if layout.WaterfallW <= 0 {
		return renderTimelineFullscreenList(m, layout.TreeW, layout.ContentH)
	}

	win := computeTimelineWindow(m, layout.ContentH)
	treeBlock := renderTimelineFullscreenList(m, layout.TreeW, layout.ContentH)

	wfLines := renderTimelineWaterfall(m, layout.WaterfallW, win)
	waterfallBlock := strings.Join(wfLines, "\n")

	gapStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("236"))
	gapLine := gapStyle.Render("│")
	rowCount := win.lineCount()
	if rowCount <= 0 {
		return treeBlock
	}
	var gapLines []string
	for i := 0; i < rowCount; i++ {
		gapLines = append(gapLines, gapLine)
	}
	gapBlock := strings.Join(gapLines, "\n")

	return lipgloss.JoinHorizontal(lipgloss.Top, treeBlock, gapBlock, waterfallBlock)
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

type timelineWindow struct {
	start    int
	end      int
	hasAbove bool
	hasBelow bool
}

func (w timelineWindow) lineCount() int {
	n := w.end - w.start
	if n < 0 {
		n = 0
	}
	if w.hasAbove {
		n++
	}
	if w.hasBelow {
		n++
	}
	return n
}

func computeTimelineWindow(m *model, height int) timelineWindow {
	rows := m.timelineTreeRows
	if len(rows) == 0 || height <= 0 {
		return timelineWindow{}
	}

	n := len(rows)
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

	return timelineWindow{start: start, end: end, hasAbove: start > 0, hasBelow: end < n}
}

func renderTimelineFullscreenList(m *model, width, height int) string {
	if width < 3 {
		width = 3
	}
	budget := width - 2

	rows := m.timelineTreeRows
	if len(rows) == 0 {
		return truncateAnsiToWidth(
			lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("  (no entries)"), budget)
	}

	win := computeTimelineWindow(m, height)

	var lines []string

	if win.hasAbove {
		lines = append(lines, truncateAnsiToWidth(
			lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("  ▲ ..."), budget))
	}

	for i := win.start; i < win.end; i++ {
		isCursor := i == m.timelineFullscreenCursor
		line := renderTimelineFullscreenTreeListItem(m, rows[i], width, isCursor)
		lines = append(lines, line)
	}

	if win.hasBelow {
		lines = append(lines, truncateAnsiToWidth(
			lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("  ▼ ..."), budget))
	}

	return strings.Join(lines, "\n")
}

const timelineMinBarSpan = time.Nanosecond

func timelineEntryInterval(e *TimelineEntry, now time.Time) (time.Time, time.Time) {
	s := e.Timestamp
	end := s.Add(e.Duration)
	if e.Duration <= 0 {
		if e.Status == ToolStatusRunning {
			end = now
		} else {
			end = s.Add(timelineMinBarSpan)
		}
	}
	if !end.After(s) {
		end = s.Add(timelineMinBarSpan)
	}
	return s, end
}

func waterfallBarStatus(e *TimelineEntry) ToolStatus {
	if e == nil {
		return ToolStatusPending
	}
	st := e.EffectiveStatus()
	if e.IsError || st == ToolStatusError {
		return ToolStatusError
	}
	return st
}

func waterfallBarColor(e *TimelineEntry) string {
	switch st := waterfallBarStatus(e); st {
	case ToolStatusError, ToolStatusRunning, ToolStatusCanceled, ToolStatusPending:
		return timelineStatusColor(st)
	}

	if c := timelineKindColor(e.Kind); c != "" {
		return c
	}
	return timelineStatusColor(ToolStatusSuccess)
}

func renderTimelineWaterfall(m *model, width int, win timelineWindow) []string {
	rows := m.timelineTreeRows
	total := win.lineCount()
	if total == 0 || width <= 0 {
		return nil
	}

	now := timelineNowFunc()

	type wfItem struct {
		s, e time.Time
		ent  *TimelineEntry
	}
	visible := make([]wfItem, 0, win.end-win.start)
	var minStart, maxEnd time.Time
	first := true
	for i := win.start; i < win.end && i < len(rows); i++ {
		r := rows[i]
		if r.entryIndex < 0 || r.entryIndex >= len(m.timelineEntries) {
			continue
		}
		ent := m.timelineEntries[r.entryIndex]
		if ent == nil {
			continue
		}
		s, e := timelineEntryInterval(ent, now)
		visible = append(visible, wfItem{s: s, e: e, ent: ent})
		if first {
			minStart, maxEnd = s, e
			first = false
		} else {
			if s.Before(minStart) {
				minStart = s
			}
			if e.After(maxEnd) {
				maxEnd = e
			}
		}
	}

	spanNs := float64(maxEnd.Sub(minStart))
	if spanNs <= 0 {
		spanNs = float64(time.Nanosecond)
	}
	colSpan := float64(width - 1)
	if colSpan < 0 {
		colSpan = 0
	}

	barStyleFor := func(ent *TimelineEntry) lipgloss.Style {
		st := lipgloss.NewStyle().Foreground(lipgloss.Color(waterfallBarColor(ent)))
		switch waterfallBarStatus(ent) {
		case ToolStatusError, ToolStatusRunning:
			st = st.Bold(true)
		case ToolStatusCanceled:
			st = st.Faint(true)
		}
		return st
	}
	placeholderStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	var lines []string
	if win.hasAbove {
		lines = append(lines, padWaterfallRow(placeholderStyle.Render("▲"), width))
	}

	visIdx := 0
	for i := win.start; i < win.end && i < len(rows); i++ {
		r := rows[i]
		if r.entryIndex < 0 || r.entryIndex >= len(m.timelineEntries) || m.timelineEntries[r.entryIndex] == nil {
			lines = append(lines, strings.Repeat(" ", width))
			continue
		}
		it := visible[visIdx]
		visIdx++

		barStart := int(float64(it.s.Sub(minStart)) / spanNs * colSpan)
		if barStart < 0 {
			barStart = 0
		}
		barEnd := int(float64(it.e.Sub(minStart)) / spanNs * colSpan)
		if barEnd > width-1 {
			barEnd = width - 1
		}
		if barEnd < barStart {
			barEnd = barStart
		}
		if barStart > width-1 {
			barStart = width - 1
		}
		barWidth := barEnd - barStart + 1

		line := strings.Repeat(" ", barStart) +
			barStyleFor(it.ent).Render(strings.Repeat("█", barWidth))
		lines = append(lines, padWaterfallRow(line, width))
	}

	if win.hasBelow {
		lines = append(lines, padWaterfallRow(placeholderStyle.Render("▼"), width))
	}

	return lines
}

func padWaterfallRow(line string, width int) string {
	dw := lipgloss.Width(line)
	if dw >= width {
		return line
	}
	return line + strings.Repeat(" ", width-dw)
}

func renderTimelineFullscreenTreeListItem(m *model, row timelineTreeRow, width int, isCursor bool) string {
	if width < 3 {
		width = 3
	}
	contentWidth := width - 2

	if row.entryIndex < 0 || row.entryIndex >= len(m.timelineEntries) {
		return truncateAnsiToWidth(
			lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(row.guides+" (missing entry)"),
			contentWidth)
	}
	entry := m.timelineEntries[row.entryIndex]

	guides := row.guides
	guidesW := lipgloss.Width(guides)

	if contentWidth <= guidesW {
		guides = truncateRunesToWidth(guides, contentWidth-1)
		guidesW = lipgloss.Width(guides)
	}
	renderedGuides := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(guides)

	innerWidth := contentWidth - guidesW
	if innerWidth < 1 {
		innerWidth = 1
	}
	if innerWidth > contentWidth {
		innerWidth = contentWidth
	}

	body := renderTimelineRowWithCursor(entry, innerWidth, nil, isCursor)
	return truncateAnsiToWidth(renderedGuides+body, contentWidth)
}

func truncateAnsiToWidth(line string, maxW int) string {
	if maxW < 1 {
		maxW = 1
	}
	if lipgloss.Width(line) <= maxW {
		return line
	}
	return lipgloss.NewStyle().MaxWidth(maxW).Render(line)
}

func truncateRunesToWidth(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxW {
		return s
	}
	r := []rune(s)
	if len(r) > maxW {
		r = r[:maxW]
	}
	return string(r)
}

func renderTimelineFullscreenStatusBar(m *model, width int) string {
	if width < 1 {
		width = 1
	}

	inner := width - 2
	if inner < 1 {
		inner = 1
	}
	statusStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("235")).
		Foreground(lipgloss.Color("250")).
		Width(inner).
		MaxWidth(width)

	rows := m.timelineTreeRows
	if len(rows) == 0 {
		text := "  (no entries) "
		return statusStyle.Render(text)
	}

	cursor := m.timelineFullscreenCursor
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(rows) {
		cursor = len(rows) - 1
	}
	entryIndex := rows[cursor].entryIndex
	if entryIndex < 0 || entryIndex >= len(m.timelineEntries) {
		return statusStyle.Render("  (entry unavailable) ")
	}
	entry := m.timelineEntries[entryIndex]

	posText := fmt.Sprintf(" %d/%d ", cursor+1, len(rows))
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

func parseModelFromDetail(detail string) string {
	start := strings.Index(detail, "[")
	end := strings.Index(detail, "]")
	if start >= 0 && end > start {
		return detail[start : end+1]
	}
	return ""
}

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
	n := len(m.timelineTreeRows)
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

func rebuildTimelineTreeRows(m *model) {
	m.timelineTreeRows = buildTimelineTreeRows(m.timelineEntries)
	n := len(m.timelineTreeRows)
	if n == 0 {
		m.timelineFullscreenCursor = 0
		return
	}
	if m.timelineFullscreenCursor < 0 {
		m.timelineFullscreenCursor = 0
	}
	if m.timelineFullscreenCursor >= n {
		m.timelineFullscreenCursor = n - 1
	}
}

func (m *model) ExitTimelineFullscreen() {
	m.timelineFullscreenMode = false
	m.timelineFullscreenCursor = 0
	m.timelineFullscreenFocus = "list"
	m.timelineDetailOffsets = nil
	m.timelineDetailVP = nil
	m.timelineTreeRows = nil
}

func calcRightPaneWidth(m *model) int {
	layout := computeTimelineLayout(m.termWidth, m.termHeight)
	rightWidth := layout.RightW - 4
	if rightWidth < 1 {
		rightWidth = 1
	}
	return rightWidth
}

func buildAllTimelineDetails(m *model) {
	entries := m.timelineEntries
	rightWidth := calcRightPaneWidth(m)

	var sb strings.Builder
	offsets := make([]int, len(entries))
	currentLine := 0

	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Faint(true)

	mergedThinkings := make(map[int]bool)
	mergedContent := make(map[int]string)

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

	if len(m.timelineTreeRows) == 0 {
		return
	}
	cursor := m.timelineFullscreenCursor
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(m.timelineTreeRows) {
		cursor = len(m.timelineTreeRows) - 1
	}
	entryIndex := m.timelineTreeRows[cursor].entryIndex
	if entryIndex < 0 || entryIndex >= len(m.timelineDetailOffsets) {
		return
	}
	targetOffset := m.timelineDetailOffsets[entryIndex]
	m.timelineDetailVP.SetYOffset(targetOffset)
}

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

	contentWidth := width - 6
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

const maxTimelineTreeDepth = 8

type timelineTreeRow struct {
	entryIndex int
	depth      int
	guides     string
	isLast     bool
}

func buildTimelineTreeRows(entries []*TimelineEntry) []timelineTreeRow {
	if len(entries) == 0 {
		return nil
	}

	type treeBuilderNode struct {
		entryIndex int
		children   []int
		isLast     bool
	}
	nodes := make([]treeBuilderNode, 0, len(entries))
	entryToNode := make(map[int]int, len(entries))
	for i, e := range entries {
		if e == nil || e.Kind == TimelineKindThinking {
			continue
		}
		entryToNode[i] = len(nodes)
		nodes = append(nodes, treeBuilderNode{entryIndex: i})
	}

	roots := make([]int, 0)
	for ni := range nodes {
		n := &nodes[ni]
		e := entries[n.entryIndex]

		parent := -1
		if e != nil && e.DelegationID != "" {
			for j := 0; j < n.entryIndex; j++ {
				pe := entries[j]
				if pe == nil {
					continue
				}
				if pe.Kind == TimelineKindTool && pe.ID == e.DelegationID {
					if pj, ok := entryToNode[j]; ok {
						parent = pj
					}
					break
				}
			}
		}

		if parent >= 0 {
			nodes[parent].children = append(nodes[parent].children, ni)
		} else {
			roots = append(roots, ni)
		}
	}

	for ni := range nodes {
		ch := nodes[ni].children
		for ci := range ch {
			nodes[ch[ci]].isLast = ci == len(ch)-1
		}
	}

	rows := make([]timelineTreeRow, 0, len(nodes))
	var visit func(ni int, depth int, ancestorPrefix string)
	visit = func(ni int, depth int, ancestorPrefix string) {
		n := nodes[ni]
		isRoot := len(ancestorPrefix) == 0 && depth == 0

		connector := ""
		switch {
		case isRoot:
			connector = ""
		case n.isLast:
			connector = "└─ "
		default:
			connector = "├─ "
		}

		displayDepth := depth
		if displayDepth > maxTimelineTreeDepth {
			displayDepth = maxTimelineTreeDepth
		}

		rows = append(rows, timelineTreeRow{
			entryIndex: n.entryIndex,
			depth:      displayDepth,
			guides:     ancestorPrefix + connector,
			isLast:     isRoot || n.isLast,
		})

		cont := ""
		if !isRoot {
			if n.isLast {
				cont = "   "
			} else {
				cont = "│  "
			}
		}
		nextPrefix := ancestorPrefix
		if depth < maxTimelineTreeDepth {
			nextPrefix = ancestorPrefix + cont
		}

		for _, ci := range n.children {
			visit(ci, depth+1, nextPrefix)
		}
	}

	for _, ri := range roots {
		visit(ri, 0, "")
	}

	return rows
}
