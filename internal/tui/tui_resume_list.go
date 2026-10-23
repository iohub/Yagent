package tui

// tui_resume_list.go — /resume 会话选择器的几何层（docs/resume-tui-redesign.md §2、R3），四条硬规则各对应一个历史故障：
// 列宽按显示宽度算、截断只走 common.Ellipsize，否则中文任务名把一行顶到 135 格后软换行多出一行（F1）。
// 可见区间只有一个来源（resumeWindow → common.WindowRange），按键与渲染读同一个 rows；旧实现两套游标，短终端静默吞行（F5）。
// 详情区在列表之外且行数恒定，游标移动不改变列表几何（F4）；每一行出框前过 fitLines、面板最后过高度闸门——lipgloss 的 Width() 会把超宽的行再折一次（AGENTS §11 坑 30）。
// 导轨与键帽只调 renderPanelTop/renderPanelBottom/editorBodyStyle（tui_chrome.go），与输入面板、引导向导逐字节同源；在这里再抄一套是禁止的——漂走的那一份永远不会报错。

import (
	"fmt"
	"image/color"
	"path/filepath"
	"strings"
	"time"

	"yagent/internal/memory"
	"yagent/internal/tui/common"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	// resumeMaxPanelWidth 是面板上限：再宽正文就飘在两侧，读起来像两张表。
	resumeMaxPanelWidth = 104
	// resumeMinInner 是"标题列 + 一个可读预览"的下限。
	resumeMinInner = 30
	// resumeDetailRows 恒定不随选中项与终端变——这正是列表不跳的原因。
	resumeDetailRows = 4
	// resumeBodyChrome = 列表后空行 1 + 发丝线 1 + 详情 resumeDetailRows。
	resumeBodyChrome = 2 + resumeDetailRows
	// resumeMaxListRows 只是护栏；真实可见行数由终端高度决定。
	resumeMaxListRows = 1000
)

// 列的成本模型只有一个：每列 = 1 格前置分隔 + 自身宽度，只有最左的树形列不要分隔；
// 分配器、渲染器与 resumeUsedCells 都按这个模型算——三处各算一遍就是三份事实。
const (
	resumeTreeCol  = 3
	resumeStateCol = 1
	resumeDateCol  = 12 // 01-02 15:04
	resumeBadgeCol = 5
	resumeBaseCol  = resumeTreeCol + 1 + resumeStateCol + 1 + resumeDateCol
)

// resumeCols 是一次渲染的列宽事实。preview 是唯一吸收余量的列，因此每行总宽恒等于 inner。
type resumeCols struct {
	inner   int // 正文宽（= panelW - 4，与 editorBodyStyle 的口径一致）
	proj    int // 0 = 这一档放不下，整列丢掉
	agent   int
	model   int
	size    int
	preview int
	badge   int // 0 = 丢掉成员计数列
}

// 列的保留优先级（高→低）：日期·状态符·Agent·成员计数·大小·模型·projectID；成员计数排在大小之前——"+3" 是下一步动作的信息，"3.3 MB" 不是。
// 阈值不是写死的数字，而是 resumeColumnSpecs 那段累加算术的结果——写死阈值与逐列累加是两份事实，早晚会漂走。
var resumeColumnSpecs = []struct {
	field string
	width int
}{
	{"agent", 13},
	{"badge", resumeBadgeCol},
	{"size", 8},
	{"model", 13},
	{"proj", 9},
}

// resumePreviewFloor 是预览列的最低可读写度：再窄就不算"标题"了。
const resumePreviewFloor = 8

// resumeColumnsFor 逐列累加，两条约束：前缀单调（某一列付不起之后不许再插后面的列）、preview 拿走余量（行宽是算出来的，不靠最后 padOrClip 剪一刀）。
func resumeColumnsFor(inner int) resumeCols {
	c := resumeCols{inner: inner}
	used := resumeBaseCol

	for _, spec := range resumeColumnSpecs {
		if used+1+spec.width+1+resumePreviewFloor > inner {
			break
		}
		used += 1 + spec.width
		switch spec.field {
		case "agent":
			c.agent = spec.width
		case "badge":
			c.badge = spec.width
		case "size":
			c.size = spec.width
		case "model":
			c.model = spec.width
		case "proj":
			c.proj = spec.width
		}
	}

	// preview 自己也要付 1 格分隔。
	c.preview = inner - used - 1
	if c.preview < resumePreviewFloor {
		// 连 8 格预览都放不下就退到"日期 + 预览"——半截到看不清的列不是在区分东西，是在制造噪音。
		c.agent, c.badge, c.size, c.model, c.proj = 0, 0, 0, 0, 0
		c.preview = max(inner-resumeBaseCol-1, resumePreviewFloor)
	}
	return c
}

// resumeUsedCells 不参与渲染，存在的意义是把成本模型变成可断言的东西——行宽不等于 inner 就意味着有人在靠 padOrClip 兜底。
func resumeUsedCells(c resumeCols) int {
	cells := resumeBaseCol
	for _, w := range []int{c.agent, c.badge, c.size, c.model, c.proj} {
		if w > 0 {
			cells += 1 + w
		}
	}
	return cells + 1 + c.preview
}

func (m *model) resumeLayout() (panelW, inner int, cols resumeCols) {
	panelW, _ = common.PanelWidths(resumeMaxPanelWidth, resumeMinInner, m.termWidth)
	inner = max(panelW-4, resumeMinInner)
	return panelW, inner, resumeColumnsFor(inner)
}

// resumeWindow 是按键与渲染共用的可见区间；两处各算一套是 F5 的成因（旧实现按键用 pageSize、渲染用夹过的高度）。
func (m *model) resumeWindow() (rows, start, end int) {
	bodyRows := max(m.termHeight-2, 3)
	rows, _ = common.ListBudget(resumeBodyChrome, resumeMaxListRows, bodyRows, len(m.resumeRows))
	start, end = common.WindowRange(len(m.resumeRows), m.resumeCursor, rows)
	return rows, start, end
}

func (m *model) resumeClampCursor() {
	if m.resumeCursor < 0 {
		m.resumeCursor = 0
	}
	if n := len(m.resumeRows); m.resumeCursor >= n {
		m.resumeCursor = n - 1
	}
	if m.resumeCursor < 0 {
		m.resumeCursor = 0
	}
}

func (m *model) resumeBorderColor(c common.ColorTokens) color.Color {
	// 颜色是停点判据的第二重指示（setup_render.go 的 borderFor 是先例）：可原样重放=紫，只能交接=橙，真失败=红，没有停点=灰。
	switch m.resumeBestState() {
	case memory.ResumeReplayable:
		return lipgloss.Color("141")
	case memory.ResumeHandoff:
		return c.Accent
	case memory.ResumeFailed:
		return c.Danger
	default:
		return c.Border
	}
}

// resumeBestState 是整份清单里"最好的那一档"（用于边框色），取组级判据 groupRank——一个可重放的子文件会把它那一组抬到最前。
func (m *model) resumeBestState() memory.ResumeState {
	best := memory.ResumeFinished
	for _, g := range m.resumeGroups {
		switch groupRank(m.resumeStops, g.head, g.members) {
		case 0:
			return memory.ResumeReplayable
		case 1:
			best = memory.ResumeHandoff
		case 2:
			if best == memory.ResumeFinished {
				best = memory.ResumeUnknown
			}
		}
	}
	return best
}

func renderResumeView(m *model) tea.View {
	c := common.DarkModeColors()
	panelW, inner, cols := m.resumeLayout()
	bodyRows := max(m.termHeight-2, 3)
	listRows, showMore := common.ListBudget(resumeBodyChrome, resumeMaxListRows, bodyRows, len(m.resumeRows))
	start, end := common.WindowRange(len(m.resumeRows), m.resumeCursor, listRows)

	lines := []string{""}
	if len(m.resumeRows) == 0 {
		lines = append(lines, resumeEmptyLines(m, inner, c)...)
	} else {
		for i := start; i < end; i++ {
			lines = append(lines, renderResumeRow(m, m.resumeRows[i], i == m.resumeCursor, cols, c))
		}
		lines = append(lines, resumeMoreLines(start, len(m.resumeRows)-end, showMore, inner, c)...)
		lines = append(lines, moreRule(inner, c))
		lines = append(lines, renderResumeDetail(m, inner, c)...)
	}
	for len(lines) < bodyRows {
		lines = append(lines, blankLine(inner))
	}
	// 两道闸门都在计数之后：任何一行越过 inner 都会被 editorBodyStyle 的 Width() 再折一次，而行数预算是按折行之前算的。
	lines = fitLines(lines, inner)
	if len(lines) > bodyRows {
		lines = lines[:bodyRows]
	}

	rail := m.resumeBorderColor(c)
	frame := strings.Join([]string{
		renderPanelTop(m.resumeLabel(inner), m.resumeMeta(inner), panelW, rail),
		editorBodyStyle(rail, panelW).Render(strings.Join(lines, "\n")),
		renderPanelBottom(m.resumeHints(), panelW, rail),
	}, "\n")

	// AltScreen 必须为 true：主视图开着备用屏，而渲染器看到这个字段翻转就会退出备用屏（cursed_renderer.go:314）——旧实现进 /resume 就把用户甩回主屏缓冲区。
	return tea.View{AltScreen: true, Content: lipgloss.Place(
		m.termWidth, m.termHeight, lipgloss.Center, lipgloss.Top, frame)}
}

func (m *model) resumeLabel(inner int) string {
	n := len(m.resumeGroups)
	if inner < 46 {
		return fmt.Sprintf("RESUME %d", n)
	}
	return fmt.Sprintf("RESUME · %d sessions", n)
}

// resumeMeta 的账目里可续条数最该先看到：不写它，用户就得逐条 Enter 试才知道哪条其实早就跑完了。
func (m *model) resumeMeta(inner int) string {
	resumable := resumableGroups(m.resumeStops, m.resumeGroups)
	if inner < 52 {
		return fmt.Sprintf("%d/%d resumable", resumable, len(m.resumeGroups))
	}
	root := shrinkPath(m.resumeRoot, max(inner/4, 10))
	if root == "" {
		return fmt.Sprintf("%d/%d resumable", resumable, len(m.resumeGroups))
	}
	return fmt.Sprintf("%d/%d resumable · %s", resumable, len(m.resumeGroups), root)
}

func (m *model) resumeHints() string {
	if len(m.resumeRows) == 0 {
		return "r rescan · q quit"
	}
	return "j/k move · ⏎ open · q quit · space expand · pgup/pgdn page · g/G first/last · r rescan"
}

func renderResumeRow(m *model, row resumeRow, selected bool, cols resumeCols, c common.ColorTokens) string {
	item := row.item
	g := m.resumeGroups[row.groupIdx]

	state := memory.ResumeUnknown
	if f, ok := m.resumeStops[item.Path]; ok {
		state, _ = f.State()
	}

	date := item.StartedAt.Format("01-02 15:04")
	if item.StartedAt.IsZero() {
		date = strings.Repeat("-", resumeDateCol)
	}
	proj := ""
	if item.ProjectID != "" {
		proj = "[" + item.ProjectID + "]"
	}
	agent := item.AgentName
	if isMainRolloutFile(item) {
		agent = "main"
	} else {
		agent = "sub:" + agent
	}
	badge := ""
	if cols.badge > 0 && row.isHead && len(g.members) > 0 {
		badge = fmt.Sprintf("+%d", len(g.members))
	}

	// 每一段都是 "分隔 + 定宽单元格"，顺序与 cols 的字段一一对应。
	seg := resumeTreeCell(row, g, m.resumeExpanded[g.key], selected, c) +
		" " + styled(resumeStateGlyph(state), resumeStateColor(state), c) +
		" " + resumePlain(common.Ellipsize(date, resumeDateCol), selected, c)
	if cols.proj > 0 {
		seg += " " + resumeDim(common.Ellipsize(proj, cols.proj), selected, c)
	}
	if cols.agent > 0 {
		seg += " " + styled(common.Ellipsize(agent, cols.agent),
			resumeAgentColor(state, selected, c), c)
	}
	if cols.model > 0 {
		seg += " " + resumeDim(common.Ellipsize(orDash(item.Model), cols.model), selected, c)
	}
	if cols.size > 0 {
		seg += " " + resumeDim(common.PadLeft(formatResumeBytes(item.SizeBytes), cols.size), selected, c)
	}
	seg += " " + common.Ellipsize(orNoPreview(item.Preview), cols.preview)
	if cols.badge > 0 {
		seg += " " + common.PadLeft(badge, cols.badge)
	}

	// 行宽应当已经等于 inner；这里的 clip/pad 只是闸门，不是对齐手段。
	return padOrClip(seg, cols.inner, selected, c)
}

// resumeTreeCell 三格宽：选中轨道与树形符同栏共存——旧实现用 "▸ " 前缀把有子文件的行整体右推 2 格（F2）。
func resumeTreeCell(row resumeRow, g resumeGroup, expanded, selected bool, c common.ColorTokens) string {
	mark := " "
	switch {
	case !row.isHead:
		mark = "└"
	case len(g.members) == 0:
		mark = " "
	case expanded:
		mark = "▾"
	default:
		mark = "▸"
	}
	if selected {
		return lipgloss.NewStyle().Foreground(c.Accent).Render("▌") +
			lipgloss.NewStyle().Foreground(c.TextSecondary).Render(mark+" ")
	}
	return lipgloss.NewStyle().Foreground(c.Border).Render(" " + mark + " ")
}

func resumeAgentColor(state memory.ResumeState, selected bool, c common.ColorTokens) color.Color {
	switch {
	case selected:
		return c.TextPrimary
	case state == memory.ResumeReplayable:
		return c.Primary
	default:
		return c.TextSecondary
	}
}

func resumePlain(s string, selected bool, c common.ColorTokens) string {
	if selected {
		return lipgloss.NewStyle().Foreground(c.TextPrimary).Render(s)
	}
	return lipgloss.NewStyle().Foreground(c.TextSecondary).Render(s)
}

func resumeDim(s string, selected bool, c common.ColorTokens) string {
	if selected {
		return lipgloss.NewStyle().Foreground(c.TextSecondary).Render(s)
	}
	return lipgloss.NewStyle().Foreground(c.TextMuted).Render(s)
}

func styled(s string, fg color.Color, c common.ColorTokens) string {
	return lipgloss.NewStyle().Foreground(fg).Render(s)
}

// padOrClip 只收累计误差；越过 inner 必须剪掉而不是留着——lipgloss 的 Width() 会把超宽的行再折一次，行数预算当场失效。
func padOrClip(s string, inner int, selected bool, c common.ColorTokens) string {
	if w := lipgloss.Width(s); w < inner {
		s += strings.Repeat(" ", inner-w)
	} else if w > inner {
		s = clipVisible(s, inner)
	}
	if selected {
		return lipgloss.NewStyle().Background(c.Surface).Render(s)
	}
	return s
}

// renderResumeDetail 是详情抽屉的恒定 4 行：内容随选中项变，**行数不变**。
func renderResumeDetail(m *model, inner int, c common.ColorTokens) []string {
	row := m.resumeRowAtCursor()
	if row == nil {
		return resumeEmptyLines(m, inner, c)
	}
	item := row.item
	facts, hasFacts := m.resumeStops[item.Path]
	state, why := memory.ResumeUnknown, ""
	if hasFacts {
		state, why = facts.State()
	}

	dim := lipgloss.NewStyle().Foreground(c.TextMuted)
	head := lipgloss.NewStyle().Foreground(c.TextPrimary).Bold(true)

	sid := shortID(item.SessionID, item.Path)
	meta := strings.Join(nonEmptyCells(
		"cwd "+shrinkPath(item.CWD, 24),
		prefixed("git ", item.GitBranch),
		prefixed("board ", item.BoardID),
	), " · ")

	// 四行分工：停在哪 → 为什么不能（或能）这样续 → 有没有坐标 → 账目；原因独占一行才不会被截断——挤在状态行尾巴上的截断原因等于没有原因。
	lines := []string{
		head.Render("▌STOP") + " " + dim.Render(clipCells(sid+" "+meta, inner-7)),
		" " + styled(resumeStateGlyph(state), resumeStateColor(state), c) + " " +
			clipCells(stopHeadline(state, facts), inner-4),
		"  " + styled(reasonFor(why, state), resumeStateColor(state), c) + "",
		"  " + dim.Render(clipCells(strings.Join(nonEmptyCells(
			checkpointLine(facts, hasFacts),
			fmt.Sprintf("%d msgs(head)", item.HeadMessages),
			formatResumeBytes(item.SizeBytes),
			resumeRelTime(item.ModTime),
		), " · "), inner-2)),
	}
	for len(lines) < resumeDetailRows {
		lines = append(lines, blankLine(inner))
	}
	return lines[:resumeDetailRows]
}

// checkpointLine 只陈述"有没有重放坐标"，不下结论——结论归状态行，两处都写会出现 "● replayable" 与 "只能走交接" 互相打脸。
func checkpointLine(f memory.StopFacts, hasFacts bool) string {
	if hasFacts && f.Checkpoint != nil {
		cp := f.Checkpoint
		step := fmt.Sprintf("step %d/%d", cp.Step, cp.MaxSteps)
		nested := ""
		if n := len(cp.Nested); n > 0 {
			nested = fmt.Sprintf(" · %d nested", n)
		}
		return "checkpoint " + orDash(cp.AgentName) + " " + step +
			" · digest " + shortDigest(cp.Digest) + nested
	}
	if hasFacts && f.Found {
		return "no checkpoint coordinate: replay start unknown, continue from the Director instead"
	}
	return "no checkpoint record (legacy file, or the run never reached a stop path)"
}

// stopHeadline 是详情第二行的主干：状态 + 停在第几步 + 停止原因码；刻意不带 why——why 自己占一行才有不被截断的机会。
func stopHeadline(state memory.ResumeState, f memory.StopFacts) string {
	out := state.String()
	if f.Step > 0 {
		if f.MaxSteps > 0 {
			out += fmt.Sprintf(" · step %d/%d", f.Step, f.MaxSteps)
		} else {
			out += fmt.Sprintf(" · step %d", f.Step)
		}
	}
	return out + " · " + orNoStopReason(f.StopReason)
}

// reasonFor 可原样重放时给一句肯定话而不是留空行——空行会让人以为渲染坏了。
func reasonFor(why string, state memory.ResumeState) string {
	if why != "" {
		return why
	}
	if state == memory.ResumeReplayable {
		return "stopped at a step boundary; safe to replay as-is"
	}
	return ""
}

// describeStop 保留给测试与非 TUI 消费者：单行版（状态 · 步数 · 原因 · 说明）。
func describeStop(state memory.ResumeState, why string, f memory.StopFacts) string {
	out := stopHeadline(state, f)
	if why != "" {
		out += " · " + why
	}
	return out
}

// resumeEmptyLines 的错误原文必须看得见落点：只显示 "Error" 而不说是哪个目录，等于把排查工作推给用户。
func resumeEmptyLines(m *model, inner int, c common.ColorTokens) []string {
	dim := lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true)
	text := "Loading rollout sessions…"
	if m.resumeErr != "" {
		text = m.resumeErr
	} else if !m.resumeLoading {
		text = "No rollout sessions yet."
	}
	out := wrapPlain(text, inner)
	if !m.resumeLoading && m.resumeRoot != "" {
		out = append(out, dim.Render(clipCells("dir "+m.resumeRoot, inner)))
	}
	for len(out) < resumeDetailRows+1 {
		out = append(out, blankLine(inner))
	}
	return out[:max(resumeDetailRows+1, 1)]
}

func resumeMoreLines(up, down int, showMore bool, inner int, c common.ColorTokens) []string {
	if !showMore {
		return nil
	}
	var out []string
	if up > 0 {
		out = append(out, common.MoreLine(up, true, inner, c))
	}
	if down > 0 {
		out = append(out, common.MoreLine(down, false, inner, c))
	}
	return out
}

func moreRule(inner int, c common.ColorTokens) string {
	return lipgloss.NewStyle().Foreground(c.Border).Render(strings.Repeat("─", inner))
}

func blankLine(inner int) string { return strings.Repeat(" ", inner) }

func shortID(sessionID, path string) string {
	s := sessionID
	if s == "" {
		s = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	if r := []rune(s); len(r) > 8 {
		return string(r[:8])
	}
	return s
}

func shortDigest(d string) string {
	if d == "" {
		return "-"
	}
	if r := []rune(d); len(r) > 8 {
		return string(r[:8])
	}
	return d
}

func resumeRelTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%.1fh ago", d.Hours())
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func nonEmptyCells(in ...string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		if strings.TrimSpace(s) != "" && !strings.HasSuffix(s, " ") {
			out = append(out, s)
		}
	}
	return out
}

func prefixed(label, val string) string {
	if val == "" {
		return ""
	}
	return label + val
}

// firstColumnPad 放不下就整个丢掉，而不是让这一行越过 inner（越过就会被 editorBodyStyle 再折一次，行数预算当场失效）。
func firstColumnPad(extra string, inner int) string {
	if inner <= 0 || lipgloss.Width(extra) > inner {
		return ""
	}
	return extra
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func orNoPreview(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(no preview)"
	}
	return s
}

func orNoStopReason(s string) string {
	if s == "" {
		return "no stop_reason recorded"
	}
	return s
}

// clipCells 按显示宽度截断（不补空格），给"只能占这么宽"的自由文本行用。
func clipCells(s string, w int) string {
	if w <= 0 || lipgloss.Width(s) <= w {
		return s
	}
	return clipVisible(s, max(w-1, 0)) + "…"
}
