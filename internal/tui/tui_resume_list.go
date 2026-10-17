package tui

// tui_resume_list.go — /resume 会话选择器的几何层（docs/resume-tui-redesign.md §2、R3）。
//
// 四条硬规则，缺一条就复现原来那几个病：
//  1. 列宽一律按**显示宽度**算，截断只走 common.Ellipsize（它保证输出恰好 w 格）。
//     旧实现用 len() 字节算列宽、按 rune 截断预览，中文任务名能把一行顶到 135 格 ——
//     软换行之后整屏多出一行（F1）。
//  2. 可见区间只有一个来源（resumeWindow → common.WindowRange），按键与渲染读同一个
//     rows。旧实现按键用 pageSize、渲染用夹过的 effectivePageSize，短终端静默吞行（F5）。
//  3. 详情区在列表**之外**且行数恒定 ⇒ 游标移动不改变列表几何（F4）。
//  4. 每一行出框前过 fitLines，整个面板最后过高度闸门 —— lipgloss 的 Width() 会把
//     超宽的行**再折一次**，而行数预算是按折行之前算的（AGENTS §11 坑 30）。
//
// 导轨与键帽直接调 renderPanelTop / renderPanelBottom / editorBodyStyle（tui_chrome.go），
// 所以和输入面板、引导向导逐字节同源。在这个文件里再抄一套更好看的版本是禁止的：
// 漂走的那一份永远不会报错。

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
	// resumeDetailRows 是详情抽屉的**恒定**行数。它不随选中项变、也不随终端变 ——
	// 这正是列表不再跳的原因。
	resumeDetailRows = 4
	// resumeBodyChrome = 列表后空行 1 + 发丝线 1 + 详情 resumeDetailRows。
	resumeBodyChrome = 2 + resumeDetailRows
	// resumeMaxListRows 只是护栏；真实可见行数由终端高度决定。
	resumeMaxListRows = 1000
)

// 行首三列与尾列的固定宽度。树形列宽恒定，展开与否不再移动正文起点。
// 列的成本模型只有一个：**每列 = 1 格前置分隔 + 自身宽度**，
// 只有最左边的树形列不要分隔（它就是第一格）。
// 分配器、渲染器与 resumeUsedCells 都按这个模型算 —— 三处各算一遍就是三份事实。
const (
	resumeTreeCol  = 3  // ▌/▸/▾/└ + 标记 + 空格
	resumeStateCol = 1  // 停点字形
	resumeDateCol  = 12 // 01-02 15:04
	resumeBadgeCol = 5  // "+12"
	// resumeBaseCol = 树形 + (分隔+状态) + (分隔+日期)
	resumeBaseCol = resumeTreeCol + 1 + resumeStateCol + 1 + resumeDateCol
)

// resumeCols 是一次渲染的列宽事实。preview 是**唯一**吸收余量的列，
// 因此每行总宽恒等于 inner，不需要在任何别处再对齐一次。
type resumeCols struct {
	inner   int // 正文宽（= panelW - 4，与 editorBodyStyle 的口径一致）
	proj    int // 0 = 这一档放不下，整列丢掉
	agent   int
	model   int
	size    int
	preview int
	badge   int // 0 = 丢掉成员计数列
}

// 列的保留优先级（从高到低）：日期 · 状态符 · Agent · 成员计数 · 大小 · 模型 · projectID。
//
// 成员计数排在大小之前 —— "+3" 告诉你这一组还有子 Agent 文件没展开看，那是**下一步动作**
// 的信息；"3.3 MB" 不是。
//
// 阈值不是写死的数字，而是下面那段累加算术的结果（见 resumeColumnSpecs）：
// 写死阈值与逐列累加是两份事实，早晚会漂走。
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

// resumeColumnsFor 按可用宽度逐列累加。
//
// 两个约束：
//  1. **前缀单调** —— 一旦某一列付不起就停止，后面的列不许再插进来。
//     否则会出现"projectID 在位而模型被丢掉"这种说不通的组合。
//  2. **preview 拿走余量** —— 行宽是算出来的，不是靠最后 padOrClip 剪一刀。
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
		// 连 8 格预览都放不下：退到"日期 + 预览"，把让出来的格子还给预览。
		// 半截到看不清的列不是在区分东西，是在制造噪音。
		c.agent, c.badge, c.size, c.model, c.proj = 0, 0, 0, 0, 0
		c.preview = max(inner-resumeBaseCol-1, resumePreviewFloor)
	}
	return c
}

// resumeUsedCells 按同一套成本模型算出"这一行应当占多少格"。
// 它不参与渲染，存在的意义是把模型变成可断言的东西：
// 行宽不等于 inner 就意味着有人在靠 padOrClip 兜底，而那是会静默错位的地方。
func resumeUsedCells(c resumeCols) int {
	cells := resumeBaseCol
	for _, w := range []int{c.agent, c.badge, c.size, c.model, c.proj} {
		if w > 0 {
			cells += 1 + w
		}
	}
	return cells + 1 + c.preview
}

// resumeLayout 由终端尺寸推出面板宽、正文宽与列语法。
func (m *model) resumeLayout() (panelW, inner int, cols resumeCols) {
	panelW, _ = common.PanelWidths(resumeMaxPanelWidth, resumeMinInner, m.termWidth)
	inner = max(panelW-4, resumeMinInner)
	return panelW, inner, resumeColumnsFor(inner)
}

// resumeWindow 是**按键与渲染共用**的可见区间。返回的 rows 就是这一屏能放几行列表；
// 两处各算一套是 F5 的成因（旧实现按键用 pageSize、渲染用夹过的高度）。
func (m *model) resumeWindow() (rows, start, end int) {
	bodyRows := max(m.termHeight-2, 3)
	rows, _ = common.ListBudget(resumeBodyChrome, resumeMaxListRows, bodyRows, len(m.resumeRows))
	start, end = common.WindowRange(len(m.resumeRows), m.resumeCursor, rows)
	return rows, start, end
}

// resumeClampCursor 把游标夹回 [0, 行数)。
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
	// 颜色是停点判据的第二重指示（setup_render.go 的 borderFor 是先例）：
	// 有可原样重放的停点 = 等待决策的紫，只能交接 = 橙，真失败 = 红，没有停点 = 灰。
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

// resumeBestState 是整份清单里"最好的那一档"，用于边框色。
// 取组级判据（groupRank），所以一个可重放的子文件会把它那一组抬到最前。
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

// ── 渲染 ──

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
	// 两道闸门都在计数之后：任何一行越过 inner 都会被 editorBodyStyle 的 Width()
	// 再折一次，而总行数预算是按折行之前算的。
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

	// AltScreen 必须是 true：主视图开着备用屏，而 tea.NewView 的这个字段是 false，
	// 渲染器看到它翻转就会**退出备用屏**（cursed_renderer.go:314）。
	// 旧实现正是这样 —— 进 /resume 就把用户甩回主屏缓冲区。
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

// resumeMeta 是顶栏右侧的账目。可续条数是这一屏最该先看到的事实：
// 不写它，用户就得逐条 Enter 试才知道哪条其实早就跑完了。
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

// renderResumeRow 输出一行**恰好 inner 格**的列表行。
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

// resumeTreeCell 是三格宽的树形/选中列：▌ 是选中轨道，▸/▾/└ 是树形，两者同栏共存。
// 旧实现用 "▸ " 前缀把有子文件的行整体右推 2 格（F2），而选中符 ● 又占了同一栏。
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

// padOrClip 把整行收到 inner 格。Ellipsize 已经保证各列精确，
// 这里只收最后的累计误差；越过 inner 必须**剪掉**而不是留着让它再折一次。
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

	// 四行的分工：**停在哪 → 为什么不能（或能）这样续 → 有没有坐标 → 账目**。
	// 原因单独占一行：它是这一屏里唯一决定"我该不该按 Enter"的文字，
	// 挤在状态行尾巴上就会被截断，而截断的原因等于没有原因。
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

// checkpointLine 只陈述"有没有重放坐标"这一件事，不下结论。
//
// 结论归状态行。曾经这一行写的是"只能带着已有结论重新规划（走交接）"，
// 于是当 stop_reason=paused（可原样重放）而文件里恰好没有检查点时，
// 同一张卡上会出现 "● replayable" 与 "只能走交接" 两句话互相打脸。
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

// stopHeadline 是详情第二行的主干：状态 + 停在第几步 + 停止原因码。
// 刻意不带 why —— why 自己占一行，才有不被截断的机会。
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

// reasonFor 是"为什么是这个资格"。可原样重放时给一句肯定话而不是留空行 ——
// 空行会让人以为渲染坏了。
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

// resumeEmptyLines 是空 / 加载 / 出错三态。错误原文必须看得见落点：
// 只显示 "Error" 而不说是哪个目录，等于把排查工作推给用户。
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

// moreRule 是分隔列表与详情的发丝线：分节要看得见，但不能再加一层边框。
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

// firstColumnPad 在余量内放第二个字段：放不下就整个丢掉，
// 而不是让这一行越过 inner（越过就会被 editorBodyStyle 再折一次，行数预算当场失效）。
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
