package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// timelineNowFunc 是瀑布几何里取"现在"的唯一入口：Duration<=0 且仍在运行的条目
// 需要把条尾延伸到当下。生产代码与测试都经它取时间——测试在调用渲染前后临时替换
// 它来固定时钟，几何函数内绝不直接调 time.Now（否则运行中的条目每帧变长、断言无法成立）。
var timelineNowFunc = time.Now

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

	titleBar := renderTimelineFullscreenTitleBar(m, m.termWidth)

	// 树列宽 = layout.TreeW：WaterfallW==0 时它等于 LeftW（树吃满整左列），
	// 否则 LeftW = TreeW + Gap + WaterfallW。两列必须共用同一窗口口径。
	leftList := renderTimelineFullscreenLeft(m, layout)

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

// renderTimelineFullscreenLeft 组装全屏左列：
//   - WaterfallW==0：整左列让给树（treeWidth==LeftW），行为与阶段 B2 逐字一致。
//   - WaterfallW>0：树列 + 暗色 "│" gap 列（宽恰好 layout.Gap）+ 瀑布列并排，
//     三块都走 computeTimelineWindow(m, ContentH) 的同一窗口，行数严格相等。
//     最终显示宽 <= layout.LeftW（JoinHorizontal 把各块补到自身最大行宽，
//     树 <= TreeW、gap == Gap、瀑布 == WaterfallW）。
func renderTimelineFullscreenLeft(m *model, layout timelineLayout) string {
	if layout.WaterfallW <= 0 {
		return renderTimelineFullscreenList(m, layout.TreeW, layout.ContentH)
	}

	win := computeTimelineWindow(m, layout.ContentH)
	treeBlock := renderTimelineFullscreenList(m, layout.TreeW, layout.ContentH)

	wfLines := renderTimelineWaterfall(m, layout.WaterfallW, win)
	waterfallBlock := strings.Join(wfLines, "\n")

	// gap 列：与窗口等行数，每行一个暗色 "│"，宽恰为 layout.Gap（==1）。
	gapStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("236"))
	gapLine := gapStyle.Render("│")
rowCount := win.lineCount()
	if rowCount <= 0 {
		// 无可见行（空树）：不插 gap，树列自会显示 "(no entries)"。
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

// timelineWindow 是全屏左列"当前可见窗口"的唯一口径：树列与瀑布列都从这里取
// start/end 与上/下溢出标志，保证两列行数严格一致（含 ▲/▼ 占位行）。
// 空树时 start==end==0、两个标志都 false（行数 0），由调用方各自处理占位。
type timelineWindow struct {
	start    int
	end      int
	hasAbove bool
	hasBelow bool
}

// lineCount 返回该窗口渲染出的总行数：可见条目行 + ▲/▼ 占位行。
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

// computeTimelineWindow 是窗口计算的唯一来源，从 renderTimelineFullscreenList
// 原样抽出（不改任何边界行为）：half 居中、start/end 夹到 [0,n]、
// hasAbove=start>0、hasBelow=end<n。光标是可见树行下标（m.timelineTreeRows）。
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

// 采用窗口渲染（非 viewport，因为条目单行）。窗口按"可见树行"取，光标是树行下标。
// 窗口计算抽到 computeTimelineWindow，与瀑布列共用同一口径——两列行数必须严格一致。
func renderTimelineFullscreenList(m *model, width, height int) string {
	rows := m.timelineTreeRows
	if len(rows) == 0 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("  (no entries)")
	}

	win := computeTimelineWindow(m, height)

	var lines []string

	if win.hasAbove {
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("  ▲ ..."))
	}

	for i := win.start; i < win.end; i++ {
		isCursor := i == m.timelineFullscreenCursor
		line := renderTimelineFullscreenTreeListItem(m, rows[i], width, isCursor)
		lines = append(lines, line)
	}

	if win.hasBelow {
		lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("  ▼ ..."))
	}

	return strings.Join(lines, "\n")
}

// ─────────────────────────────────────────────────────────────────────────────
// 阶段 C：瀑布图（waterfall）时间条列。
//
// 与树列共用 computeTimelineWindow 的同一窗口，逐行输出一条时间条；条的横轴位置
// 由条目 [Timestamp, end] 映射到 [0, width-1] 列。时间跨度取"当前窗口内可见行"的
// 最小起点与最大终点，因此窗口滚动时条会随之重新铺满，但同一时刻内两列行数严格一致。
// 时钟经 timelineNowFunc 取，几何函数内不直接调 time.Now。
// ─────────────────────────────────────────────────────────────────────────────

// timelineMinBarSpan 是"瞬时/零时长"条目的极小兜底跨度：保证 end>start、
// 在 span 归一时不至于 0，再配合下面的"条宽 >= 1 列"强制可见。
const timelineMinBarSpan = time.Nanosecond

// timelineEntryInterval 返回一个条目用于铺条的时间区间 [s, e]。
//   - Duration>0：e = Timestamp + Duration。
//   - Duration<=0 且运行中：e = now（经 timelineNowFunc，条随实时推进变长）。
//   - Duration<=0 其它：e = Timestamp + timelineMinBarSpan（保证 1 列可见）。
// 任何情况下都保证 e >= s + timelineMinBarSpan（时钟回拨时夹住），使几何不退化。
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

// waterfallBarColor 返回该条目时间条的前景色令牌，与树列配色协调：
// 错误优先于 Kind（红 203），否则 Tool 绿 42 / LLMCall 蓝 111 / ContextEvent 黄 179 /
// Thinking 及未知灰 245。运行中条目在颜色之外再加粗（见调用处）。
func waterfallBarColor(e *TimelineEntry) string {
	if e.IsError || e.Status == ToolStatusError {
		return "203"
	}
	switch e.Kind {
	case TimelineKindTool:
		return "42"
	case TimelineKindLLMCall:
		return "111"
	case TimelineKindContextEvent:
		return "179"
	case TimelineKindThinking:
		return "245"
	default:
		return "245"
	}
}

// renderTimelineWaterfall 产出与树列等长的行切片：win.hasAbove/hasBelow 对应
// 暗色 ▲/▼ 占位行，中间每个可见树行对应一条时间条。每行显示宽严格 == width
// （条右侧用空格补齐）。空窗口（0 行）返回 nil——JoinHorizontal 会把缺失块补成空白。
//
// 几何（按规格手算，被 TestTimelineWaterfall_Geometry 逐项核对）：
//
//	span      = maxEnd - minStart（<=0 时按 1ns 处理）
//	barStart  = int( (entryStart - minStart) / span * (width-1) )，负则 0
//	barEnd    = int( (entryEnd   - minStart) / span * (width-1) )，clamp 到 [barStart, width-1]
//	barWidth  = barEnd - barStart + 1（恒 >= 1）
func renderTimelineWaterfall(m *model, width int, win timelineWindow) []string {
	rows := m.timelineTreeRows
	total := win.lineCount()
	if total == 0 || width <= 0 {
		return nil
	}

	now := timelineNowFunc()

	// 第一遍：收集可见区间内每个条目的 [s, e]，并求窗口跨度。
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
	// 跨度归一：<=0 时按 1ns，避免除零。
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
		if ent.Status == ToolStatusRunning && !ent.IsError && ent.Status != ToolStatusError {
			st = st.Bold(true)
		}
		return st
	}
	placeholderStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	// 第二遍：逐行输出，占位行优先，与树列同序同长。
	var lines []string
	if win.hasAbove {
		lines = append(lines, padWaterfallRow(placeholderStyle.Render("▲"), width))
	}

	// visible 与 [win.start, win.end) 里"有 entryIndex 的行"一一对应；占位行不算，
	// 因此按窗口下标推进，跳过被过滤的缺失行，保持与树列逐行对齐。
	visIdx := 0
	for i := win.start; i < win.end && i < len(rows); i++ {
		r := rows[i]
		if r.entryIndex < 0 || r.entryIndex >= len(m.timelineEntries) || m.timelineEntries[r.entryIndex] == nil {
			// 缺失条目行：树列也会渲染占位，这里输出同宽空白保持对齐。
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
		barWidth := barEnd - barStart + 1 // 恒 >= 1

		line := strings.Repeat(" ", barStart) +
			barStyleFor(it.ent).Render(strings.Repeat("█", barWidth))
		lines = append(lines, padWaterfallRow(line, width))
	}

	if win.hasBelow {
		lines = append(lines, padWaterfallRow(placeholderStyle.Render("▼"), width))
	}

	return lines
}

// padWaterfallRow 把一行补空格到显示宽恰为 width（lipgloss.Width 计空格与 █ 各为 1）。
// 若已超宽（不应发生）则原样返回，交由外层宽度不变量测试暴露。
func padWaterfallRow(line string, width int) string {
	dw := lipgloss.Width(line)
	if dw >= width {
		return line
	}
	return line + strings.Repeat(" ", width-dw)
}


// renderTimelineFullscreenTreeListItem 渲染一个树行：暗色 guides 前缀 + 源条目行。
// 整行显示宽必须 <= 传入 width：用 lipgloss.Width 扣除 guides 的可视宽（不许用 len），
// 余下宽度交给 renderTimelineRowWithCursor。entryIndex 越界时退化为占位行，绝不 panic。
func renderTimelineFullscreenTreeListItem(m *model, row timelineTreeRow, width int, isCursor bool) string {
	contentWidth := width - 2
	if contentWidth < 20 {
		contentWidth = 20
	}

	if row.entryIndex < 0 || row.entryIndex >= len(m.timelineEntries) {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(row.guides + " (missing entry)")
	}
	entry := m.timelineEntries[row.entryIndex]

	guidesW := lipgloss.Width(row.guides)
	guides := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(row.guides)
	innerWidth := contentWidth - guidesW
	if innerWidth < 8 {
		innerWidth = 8
	}
	body := renderTimelineRowWithCursor(entry, innerWidth, nil, isCursor)
	return guides + body
}

func renderTimelineFullscreenStatusBar(m *model, width int) string {
	statusStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("235")).
		Foreground(lipgloss.Color("250")).
		Width(width - 2)

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

// rebuildTimelineTreeRows 重新派生全屏可见树行，并把光标夹到合法区间。
//
// 这是 buildTimelineTreeRows 唯一的生产调用点：只在"打开全屏 / resize / 全屏期间追加条目"
// 这三处触发，绝不在每帧 View() 里跑（派生是 O(n)、且会重排光标）。空树时光标置 0。
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
	// 光标是"可见树行下标"，须经 rows[cursor].entryIndex 映射回扁平条目下标，
	// 再到该条目的详情起始 offset。空树 / offsets 缺失时安全返回，绝不越界 panic。
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

// ─────────────────────────────────────────────────────────────────────────────
// 全屏 timeline 的树行派生（阶段 B1）。
//
// 这里只把扁平的 []*TimelineEntry 还原成"父子 + 缩进 + 树形引导符"的行序列，
// 不触碰渲染/光标/键位（那些留给 B2）。归组规则见函数注释，刻意复用现有
// (Agent, DelegationID) 坐标：DelegationID 指向前一个 delegate_* 工具条目的 ID，
// 于是子 Agent 的条目挂到那轮委派工具条目之下，其余条目挂根。
// ─────────────────────────────────────────────────────────────────────────────

// maxTimelineTreeDepth 是缩进的层级上限：超过后 guides 不再增加祖先段，
// 但当前层 connector（├─ / └─）照常渲染，避免极深嵌套把整行挤出台阶。
const maxTimelineTreeDepth = 8

// timelineTreeRow 是 buildTimelineTreeRows 输出的单行：指向源条目的下标、
// 缩进深度（已 clamp 到 maxTimelineTreeDepth）、树形引导前缀、以及"是否为
// 其父的最后一个子节点"标志（B2 的光标/折叠会用）。
type timelineTreeRow struct {
	entryIndex int    // 在传入 entries 切片中的原始下标（含被跳过的 Thinking 占位）
	depth      int    // 缩进深度，根为 0，上限 maxTimelineTreeDepth
	guides     string // 祖先竖线段 + 当前层 ├─/└─ connector；根为空串
	isLast     bool   // 是否为其父的最后一个子节点（根视为 true）
}

// buildTimelineTreeRows 按追加顺序把扁平条目派生成 DFS（先父后子）的树行序列。
//
// 规则：
//   - 跳过 Kind==TimelineKindThinking 的条目，它们不产行，也不影响其余行的相对顺序；
//   - 若 entry.DelegationID 非空，且在它之前存在 Kind==TimelineKindTool 且 ID==DelegationID
//     的条目，则挂为该条目的子节点；否则（含 ID 不存在、或指向非工具条目）挂根；
//   - 同一父的子节点保持追加顺序，根节点保持追加顺序；
//   - guides：祖先层为 "│  "（该祖先不是其父的最后子）或 "   "（是最后子），
//     当前层为 "├─ "（非最后子）或 "└─ "（最后子），根的前缀为空串；
//   - depth 超过 maxTimelineTreeDepth 后祖先段封顶，不再增加缩进。
func buildTimelineTreeRows(entries []*TimelineEntry) []timelineTreeRow {
	if len(entries) == 0 {
		return nil
	}

	// 候选节点：过滤 nil 与 Thinking，保留其在 entries 中的原始下标 entryIndex。
	type treeBuilderNode struct {
		entryIndex int
		children   []int // 指向 nodes 切片的下标，按追加顺序
		isLast     bool  // 是否为其父的最后一个子；根在标记阶段保持默认 false，输出时按根处理
	}
	nodes := make([]treeBuilderNode, 0, len(entries))
	entryToNode := make(map[int]int, len(entries)) // entryIndex -> node 下标
	for i, e := range entries {
		if e == nil || e.Kind == TimelineKindThinking {
			continue
		}
		entryToNode[i] = len(nodes)
		nodes = append(nodes, treeBuilderNode{entryIndex: i})
	}

	// 为每个节点找父：在 entries 中、位于其之前、Kind==Tool 且 ID==DelegationID 的条目。
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
					// 命中的父条目必非 Thinking（Kind==Tool），故一定在 entryToNode 里。
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

	// 标记每个子节点的 isLast：children 顺序即追加顺序（上面 ni 单调递增 append）。
	for ni := range nodes {
		ch := nodes[ni].children
		for ci := range ch {
			nodes[ch[ci]].isLast = ci == len(ch)-1
		}
	}

	// DFS 输出。ancestorPrefix 是已构建的祖先竖线段；depth 为当前节点层级（根=0）。
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

		// 下一层的祖先段前缀 = 当前祖先段 + 当前层延续段。
		// 当前层延续段：根不画竖线（根无后续兄弟），其余按"当前节点是否最后子"决定 "   " / "│  "。
		// depth 已达上限时冻结，不再增加缩进。
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
