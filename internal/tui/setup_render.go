// 引导向导的外壳（docs/setup-wizard-ui-design.md §2）。
//
// 视觉与其它 TUI 表面同族，而且是同一次实现、不是第二次：
//   - 上下导轨直接调 renderPanelTop / renderPanelBottom / editorBodyStyle（tui_chrome.go），
//     所以 `▌` 渐变标签、meta 前的横线填充、键帽底栏与主输入面板逐字节同源；
//   - 尺寸推导、滚动窗口、列宽分配、截断走 common 那份内核
//     （与 components 里那 10 个对话框共用，见 common/list_metrics.go）。
//
// 在这个文件里"再抄一个更好看的版本"是禁止的：漂走的那一份永远不会报错。
// 各屏正文在 setup_screens.go，按键在 setup_keys.go。
package tui

import (
	"fmt"
	"image/color"
	"os"
	"strings"

	"yagent/internal/config"
	"yagent/internal/tui/common"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	// setupMinWidth below this we print a one-liner instead of a broken panel
	// (导轨 + 内边距之后至少要剩几列正文)。
	setupMinWidth = 40
	// setupMinHeight 是"上下导轨 2 行 + 完整必填区 + 状态条 2 行 + 余量"。
	// 只守宽度的后果：长错误 + 满队列的摘要屏溢出到 AltScreen 之外，看不见也滚不回去。
	setupMinHeight = 18
	// setupMaxPanelWidth 是面板上限：再宽正文就飘在两侧，读起来像两张表。
	setupMaxPanelWidth = 104
	// setupRailWidth 是阶段轨那一列；setupRailMinInner 是"还容得下轨道 + 可读正文"的最小内宽。
	setupRailWidth    = 18
	setupRailMinInner = 54
	// setupMinInner 是正文列的下限：再窄就容不下标签列加一个 URL 的前缀。
	setupMinInner = 30
	// bodyChrome 是 editorBodyStyle 左右各吃掉的 1 列边框 + 1 列内边距。
	bodyChrome = 4
)

// setupColors 是向导的全部语义色。刻意只是 common.ColorTokens 的薄封装 ——
// 这里再定义一套色号就是"第二套配色"，而配色只集中在 common/styles.go（AGENTS §6.8）。
type setupColors struct{ common.ColorTokens }

func setupPaletteColors() setupColors {
	return setupColors{common.DarkModeColors()}
}

// setupLayout 是一次渲染的尺寸事实。所有屏幕都从这里取宽度，不再各自 w.width-2。
type setupLayout struct {
	panelW   int // 含左右边框
	inner    int // 面板正文宽（= panelW - bodyChrome）
	railW    int // 0 = 轨道折叠
	contentW int // 正文列宽（轨道折叠时 = inner）
	rows     int // 正文可用行数（不含上下导轨与状态条）
}

func (w *setupWizard) layout(extras int) setupLayout {
	panelW, _ := common.PanelWidths(setupMaxPanelWidth, setupMinInner, w.width)
	inner := max(panelW-bodyChrome, setupMinInner)
	L := setupLayout{panelW: panelW, inner: inner, contentW: inner}
	if inner >= setupRailMinInner {
		L.railW = setupRailWidth
		L.contentW = inner - setupRailWidth - 1
	}
	// 不变量：1(顶栏) + rows + extras + 1(底栏) ≤ height。
	// capStatus 保证 extras ≤ height-5，所以这里永远 ≥ 3 —— 不需要往负数方向夹。
	L.rows = max(w.height-2-extras, 1)
	return L
}

func (w *setupWizard) View() tea.View {
	if w.width < setupMinWidth || w.height < setupMinHeight {
		return tea.View{AltScreen: true, Content: fmt.Sprintf(
			"yagent setup: needs at least %d×%d columns×rows (now %d×%d).",
			setupMinWidth, setupMinHeight, w.width, w.height)}
	}
	c := setupPaletteColors()
	// 两道夹紧都在数行数之前：lipgloss 的 Width() 会把超宽的行**再折一次**，
	// 于是"我数过是 13 行"和屏上真正的 16 行不是一个数 —— 高度不变量就是这么破的。
	status := fitLines(w.capStatus(fitLines(w.statusLines(c), w.statusInner()), c), w.statusInner())
	L := w.layout(len(status))

	body := fitLines(w.body(c, L), L.contentW)
	var joined string
	if L.railW > 0 {
		joined = lipgloss.JoinHorizontal(lipgloss.Top, w.rail(c, L),
			lipgloss.JoinVertical(lipgloss.Left, body...))
	} else {
		joined = lipgloss.JoinVertical(lipgloss.Left, body...)
	}
	// 高度闸门在"拼好之后"而不是正文上：导轨那一列自己也有 6~7 行，
	// 只截正文的话，18 行终端 + 满屏错误原文仍会把面板顶出 AltScreen。
	content := strings.Join(w.trimToHeight(strings.Split(joined, "\n"), c, L), "\n")

	rail := w.borderFor(c)
	frame := []string{renderPanelTop(w.panelLabel(c, L), w.panelMeta(), L.panelW, rail)}
	frame = append(frame, editorBodyStyle(rail, L.panelW).Render(content))
	if strip := lipgloss.JoinVertical(lipgloss.Left, status...); strip != "" {
		frame = append(frame, editorBodyStyle(rail, L.panelW).Render(strip))
	}
	frame = append(frame, renderPanelBottom(w.hints(), L.panelW, rail))

	packed := lipgloss.JoinVertical(lipgloss.Left, fitLines(frame, L.panelW)...)
	return tea.View{AltScreen: true, Content: lipgloss.Place(
		w.width, w.height, lipgloss.Center, lipgloss.Center, packed)}
}

// borderFor 让边框色随屏义变化（tui_chrome.go:26-33 用颜色指示状态是先例）：
// 只读屏蓝、编辑屏紫、探测中绿、出错红、写盘前橙。
func (w *setupWizard) borderFor(c setupColors) color.Color {
	switch {
	case w.errMsg != "" || w.probeErr != "":
		return c.Danger
	case w.probing:
		return c.Success
	case w.step == stepProviderForm:
		return lipgloss.Color("62")
	case w.step == stepSummary:
		return c.Accent
	default:
		return c.BorderActive
	}
}

// panelLabel 是顶栏标题。措辞按面板宽度三级退让：标签自己比面板还宽时
// renderPanelTop 只能把横线夹到 1 列，整行仍越界，表现是顶栏被剪成 "╭─ …"。
func (w *setupWizard) panelLabel(c setupColors, L setupLayout) string {
	name := w.step.title()
	switch {
	case L.panelW >= 46:
		if L.railW > 0 {
			return "YAGENT SETUP · " + name
		}
		// 轨道折叠时把"第几段"并进标题，否则进度就只剩右上角那个数字。
		return fmt.Sprintf("YAGENT SETUP · %s · %d of %d", name, w.stage()+1, setupStageCount)
	case L.panelW >= 32:
		return "SETUP · " + name
	default:
		return name
	}
}

// panelMeta 是顶栏右侧的账目：这一屏之外还有几条没落盘。
func (w *setupWizard) panelMeta() string {
	if len(w.ops) == 0 {
		return "nothing written"
	}
	return fmt.Sprintf("%d change%s queued", len(w.ops), plural(len(w.ops)))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// statusLines 是面板底部的状态条：错误原文逐字（§6.2 要求"不做美化"），
// 成功提示走 Success，进入理由走 Muted。没有内容就不占行。
//
// 折行预算必须扣掉前缀本身：按 inner 折好再加 "× " 会让每行宽 2 列，
// 而 editorBodyStyle 的 Width() 会把超出的部分再折一次。
func (w *setupWizard) statusLines(c setupColors) []string {
	inner := w.statusInner()
	add := func(prefix, text string, style lipgloss.Style) []string {
		if text == "" {
			return nil
		}
		budget := max(inner-lipgloss.Width(prefix), 8)
		var out []string
		for _, line := range wrapPlain(text, budget) {
			out = append(out, prefix+style.Render(line))
		}
		return out
	}

	var out []string
	out = append(out, add("× ", w.errMsg, lipgloss.NewStyle().Foreground(c.Danger))...)
	// probeErr 在探测屏由那一屏自己排版（含归类行）；再进状态条就是把同一段原文显示两遍。
	if w.step != stepProbe {
		out = append(out, add("× ", w.probeErr, lipgloss.NewStyle().Foreground(c.Danger))...)
	}
	out = append(out, add("✓ ", w.note, lipgloss.NewStyle().Foreground(c.Success))...)
	if w.detail != "" && w.step == stepWelcome {
		out = append(out, add("! why setup: ", w.detail,
			lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true))...)
	}
	return out
}

// capStatus 给状态条设预算：40×18 的终端 + 一段长 provider 错误能写出 15 行原文，
// 那时"正文 = 高-2-状态行"会算出负数并夹到最小值，总高反而超出终端（面板被顶出屏幕）。
// 预算 = 高 - 上下导轨 2 行 - 正文至少 3 行；被藏起来的行数如实标出来 ——
// 静默截断错误原文正是这套向导明令禁止的事。
func (w *setupWizard) capStatus(status []string, c setupColors) []string {
	budget := w.height - 2 - 3
	if budget < 2 || len(status) <= budget {
		return status
	}
	hidden := len(status) - (budget - 1)
	out := append([]string{}, status[:budget-1]...)
	// 措辞必须短到一行装得下：这一行是"藏了几行"的事实，不是第二次说明。
	marker := fmt.Sprintf("… %d more (terminal too short)", hidden)
	return append(out, lipgloss.NewStyle().Foreground(c.DangerDim).Italic(true).Render(marker))
}

func (w *setupWizard) statusInner() int {
	panelW, _ := common.PanelWidths(setupMaxPanelWidth, setupMinInner, w.width)
	return max(panelW-bodyChrome, setupMinInner)
}

// ── 阶段轨（§2.2）──

const setupStageCount = 4

// stage 把 7 屏映射到 4 段。它是纯函数，不新增"游标"状态字段。
func (w *setupWizard) stage() int {
	switch w.step {
	case stepWelcome, stepProviders, stepProviderForm, stepProbe:
		return 0
	case stepDefault:
		return 1
	case stepFirstRun:
		return 2
	case stepSummary:
		return 3
	}
	return 0
}

// stageDone 的判据来自账本（op 队列 + 文件现状），不来自游标：
// providers→form→probe→providers→default 这条真实路径里游标来回跳，
// 而"其实已经配完了"这个事实没有倒退（决策 DB）。用游标渲染就是在撒谎。
func (w *setupWizard) stageDone(stage int) bool {
	switch stage {
	case 0:
		return len(w.effectiveProviders()) > 0
	case 1:
		def := w.currentDefault()
		if def == "" {
			return false
		}
		for _, r := range w.effectiveProviders() {
			if r.draft.Name == def {
				return true
			}
		}
		return false
	case 2:
		for _, op := range w.ops {
			if op.kind == "firstrun" {
				return true
			}
		}
		return false
	case 3:
		return w.saved
	}
	return false
}

// currentDefault 是"当前视图"里的默认档：文件现状 + 已排队的变更。
func (w *setupWizard) currentDefault() string {
	def := w.snap.useProvider
	for _, op := range w.ops {
		if op.kind == "default" {
			def = op.name
		}
	}
	return def
}

func (w *setupWizard) rail(c setupColors, L setupLayout) string {
	names := []string{"PROVIDER", "DEFAULT", "CHOICES", "REVIEW"}
	cur := w.stage()
	var out []string
	for i, name := range names {
		node, style := "○", lipgloss.NewStyle().Foreground(c.TextMuted)
		switch {
		case i == cur:
			node, style = "●", lipgloss.NewStyle().Foreground(c.Primary).Bold(true)
		case w.stageDone(i):
			node, style = "✓", lipgloss.NewStyle().Foreground(c.Success)
		}
		out = append(out, node+" "+style.Render(common.Ellipsize(name, L.railW-2)))
		if i == cur {
			// 只有当前段带实况摘要：四行都带就成了一张贴满小抄的纸。
			if detail := w.stageDetail(i, c, L.railW-4); detail != "" {
				out = append(out, "  "+detail)
			}
		}
		if i < len(names)-1 {
			out = append(out, lipgloss.NewStyle().Foreground(c.Border).Render("│"))
		}
	}
	return lipgloss.NewStyle().Width(L.railW).PaddingRight(1).Render(strings.Join(out, "\n"))
}

func (w *setupWizard) stageDetail(stage int, c setupColors, width int) string {
	muted := lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true)
	width = max(width, 8)
	switch stage {
	case 0:
		rows := w.effectiveProviders()
		if len(rows) == 0 {
			return muted.Render("none yet")
		}
		for _, r := range rows {
			if _, pending := w.pendingFor(r.draft.Name); pending {
				return muted.Render("draft " + truncate(r.draft.Name, width-6))
			}
		}
		return muted.Render(fmt.Sprintf("%d provider%s", len(rows), plural(len(rows))))
	case 1:
		if def := w.currentDefault(); def != "" {
			return muted.Render(truncate(def, width))
		}
		return muted.Render("not set")
	case 2:
		for _, op := range w.ops {
			if op.kind == "firstrun" {
				return muted.Render("answered")
			}
		}
		return muted.Render("5 questions")
	case 3:
		if w.saved {
			return muted.Render("written")
		}
		if n := len(w.ops); n > 0 {
			return muted.Render(fmt.Sprintf("%d to write", n))
		}
		return muted.Render("nothing queued")
	}
	return ""
}

// ── 排版内核 ──

// fitLines 把越过宽度的行剪断（而不是让它们被再折一次）。用 lipgloss.Width
// （而非字节数/可见字符数）是因为样式串里的 ANSI 序列不占列。
func fitLines(lines []string, width int) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if lipgloss.Width(l) > width {
			l = clipVisible(l, max(width-1, 0)) + "…"
		}
		out = append(out, l)
	}
	return out
}

// trimToHeight 是最后一道高度闸门。各屏按 L.rows 自己截列表，但总行数还受折行、
// 焦点说明行、阶段轨这些运行时因素影响；一旦超出就把面板顶到 AltScreen 之外
// （看不见、也滚不回去）。截断必须留一条 "↓ N more"：静默少几行是 bug，静默看不见才是事故。
func (w *setupWizard) trimToHeight(lines []string, c setupColors, L setupLayout) []string {
	if len(lines) <= L.rows {
		return lines
	}
	keep := max(L.rows-1, 1)
	out := append([]string{}, lines[:keep]...)
	return append(out, common.MoreLine(len(lines)-keep, false, L.contentW, c.ColorTokens))
}

// clipVisible 按显示宽度截断，尽量不切断 ANSI 序列：先数可见列，样式串原样带过。
func clipVisible(s string, width int) string {
	if width <= 0 {
		return ""
	}
	var b strings.Builder
	seen := 0
	inEsc := false
	for _, r := range s {
		if inEsc {
			b.WriteRune(r)
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		if r == '\x1b' {
			b.WriteRune(r)
			inEsc = true
			continue
		}
		if seen+lipgloss.Width(string(r)) > width {
			break
		}
		b.WriteRune(r)
		seen += lipgloss.Width(string(r))
	}
	return b.String()
}

// wrapPlain 按列宽折行（只折空白，不做样式），用于把 provider 的错误原文放进面板。
//
// 超过一整行的"单词"必须硬切：provider 的原文里全是 URL、base64 密钥与 JSON 片段，
// 只按空白折行会让一行渲染成四行 —— 而行数预算是按渲染前的行数算的，
// 表现是面板被顶到 AltScreen 之外。
func wrapPlain(s string, width int) []string {
	if s == "" {
		return nil
	}
	if width <= 0 {
		return []string{s}
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		line := ""
		for _, word := range strings.Fields(para) {
			for lipgloss.Width(word) > width {
				if line != "" {
					out = append(out, line)
					line = ""
				}
				var head string
				head, word = cutWidth(word, width)
				out = append(out, head)
			}
			if lipgloss.Width(line)+lipgloss.Width(word)+1 > width && line != "" {
				out = append(out, line)
				line = word
				continue
			}
			if line == "" {
				line = word
			} else {
				line += " " + word
			}
		}
		out = append(out, line)
	}
	return out
}

// cutWidth 在显示宽度 w 处切开一个字符串，返回 (头, 尾)。宽字符按显示列计数，
// 所以一个中文不会被切成半列。
func cutWidth(s string, w int) (head, rest string) {
	used := 0
	for i, r := range s {
		rw := lipgloss.Width(string(r))
		if rw == 0 {
			continue
		}
		if used+rw > w {
			return s[:i], s[i:]
		}
		used += rw
	}
	return s, ""
}

// listWindow 把 (条目数, 游标, 正文预算) 变成可见区间与上下还有多少条。
// 内核在 common.ListBudget / common.WindowRange —— 与对话框同一份，滚动语义不会两边漂。
func listWindow(n, focus, rows int) (start, end, moreUp, moreDown int) {
	visible, _ := common.ListBudget(0, max(rows, 1), max(rows, 1), n)
	start, end = common.WindowRange(n, focus, visible)
	return start, end, start, n - end
}

// ── 卡片与小件 ──

// truncate 按 rune 数截断并补省略号。只给"宁可少看几个字也不能撑破面板"的装饰文本用；
// 需要精确列宽的地方一律用 common.Ellipsize（它会补齐到指定宽度，表格才对得齐）。
func truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// padTo 右侧补空格到显示宽度 n（已超宽则原样返回）。
func padTo(s string, n int) string {
	if lipgloss.Width(s) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-lipgloss.Width(s))
}

// cardStyle 是一块 SurfaceRaised 底的圆角区：列表、预设、摘要都用它。
// 表单刻意不用 —— 每个字段自带 235 底的凹槽，再套一层就成三层嵌套。
func cardStyle(c setupColors, width int) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(c.Border).
		Background(c.SurfaceRaised).
		BorderBackground(c.SurfaceRaised).
		Padding(0, 1).
		Width(width)
}

// cardInner 是卡片内的正文宽：圆角边框 2 + Padding(0,1) 的 2。
func cardInner(width int) int { return max(width-4, 8) }

// sectionRule 是一条发丝分隔线（而不是空行）：分节要看得见，但不能再加一层边框。
func sectionRule(c setupColors, width int, label string) string {
	if label == "" {
		return lipgloss.NewStyle().Foreground(c.Border).Render(strings.Repeat("─", width))
	}
	label = " " + label + " "
	dashes := max(width-lipgloss.Width(label), 0)
	left := dashes / 2
	return lipgloss.NewStyle().Foreground(c.Border).Render(strings.Repeat("─", left)) +
		lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true).Render(label) +
		lipgloss.NewStyle().Foreground(c.Border).Render(strings.Repeat("─", dashes-left))
}

// keycapHint 用主输入面板那套键帽渲染一个按键（正文里引用某个键时用；
// 底栏的整排提示由 renderPanelBottom 负责）。
func keycapHint(c setupColors, key string) string {
	return common.KeyHintStyle(c.ColorTokens).Render(key)
}

// RunSetup 独立跑一次引导（坏配置启动、`yagent setup` 用）。
// 返回 saved：调用方据此决定要不要重载配置继续正常启动。
func RunSetup(path string, reason config.SetupReason, detail string) (saved bool, err error) {
	w := newSetupWizard(path, reason, detail)
	w.standalone = true
	termWidth, termHeight := 80, 24 // fallback
	if ws, sizeErr := getWindowSize(int(os.Stdout.Fd())); sizeErr == nil {
		termWidth, termHeight = int(ws.Width), int(ws.Height)
	}
	w.width, w.height = termWidth, termHeight

	p := tea.NewProgram(w)
	final, runErr := p.Run()
	if runErr != nil {
		return false, runErr
	}
	if done, ok := final.(*setupWizard); ok {
		return done.Saved(), nil
	}
	return false, nil
}
