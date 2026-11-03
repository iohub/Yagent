package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// ASK / AUTH 面板的排版与渲染（设计稿 §4）。
//
// 单一来源：面板只渲染一次、缓存成字符串，行数就数这个字符串
// （先例见 tui_render.go:28-32 的 renderTimelinePanel）。
// computeFooterHeight 加的是它、View 拼的也是它，所以"高度累加与拼接逐行对应"
// 这条契约不可能对不上 —— 不需要第二个函数去预测渲染结果。
// 骨架复用家族的 renderPanelTop / editorBodyStyle / renderPanelBottom（tui_chrome.go:100-138）。

// askBorderRows 是面板里与内容无关的固定开销：上下边框 2 + 选项区上方空行 1。
// 正文段与问题之间那行空白算在正文段里（见 rowsFor）。
const askBorderRows = 3

// askFit 是面板一帧之内的排版结果。
type askFit struct {
	question   []string // 已折行、已按上限夹取
	body       []string // HELP=上下文；AUTH=命令/路径正文
	bodyIsCode bool     // AUTH 的正文用 code 样式而不是 ℹ
	foldMarker string   // "" = 不占行；否则是"┄ N more lines · ctrl+o"
	warn       string   // "" = 不占行
	optionRows int      // 选项窗口行数
	scrollUp   int      // 窗口上方还有几项
	scrollDown int      // 窗口下方还有几项
	inner      int      // editorBodyStyle 的可用内宽
}

func (f askFit) rowsFor() int {
	n := askBorderRows + len(f.question) + f.optionRows
	if len(f.body) > 0 {
		n++ // 正文段与问题之间的一行空白
	}
	n += len(f.body)
	if f.foldMarker != "" {
		n++
	}
	if f.warn != "" {
		n++
	}
	if f.scrollUp > 0 {
		n++
	}
	if f.scrollDown > 0 {
		n++
	}
	return n
}

// askFitFor 按"最多能放几行"排出最丰形态，放不下就按设计稿 §4.2 的让位序降级：
// ① 上下文压到 1 行 → ② 上下文整段折成一行提示 → ③ 问题压到 2 行、1 行 → ④ 选项窗口收到 3。
// 键帽底边框、▲/▼ 与折叠提示行**不让位**：它们分别是"怎么出去""还有多少没看见"的唯一出口。
func (m *model) askFitFor(space int, p *pendingAsk) askFit {
	inner := m.panelWidth() - 4
	if inner < 12 {
		inner = 12
	}
	qAll := wrapKeepingSeparators(p.question, inner-2)
	var bAll []string
	isCode := p.kind == askKindAuth
	if t := p.askBodyText(); t != "" {
		bAll = wrapKeepingSeparators(t, inner-6)
	}

	f := askFit{inner: inner, bodyIsCode: isCode, warn: p.warning}
	nOpts := len(p.askOptions())

	for _, step := range [][2]int{
		{askQuestionMax, askBodyMax},
		{askQuestionMax, 1},
		{2, 0},
		{1, 0},
	} {
		f.question, _ = foldLines(qAll, max(1, step[0]))
		f.body, _ = foldLines(bAll, step[1])
		f.foldMarker = askFoldMarker(qAll, f.question, bAll, f.body)
		f.optionRows = clampRows(nOpts, askWindowMin, askWindowMax)
		f.scrollUp, f.scrollDown = askScrollFlags(nOpts, p.scroll, f.optionRows)
		if f.rowsFor() <= space {
			return f
		}
	}

	// 最瘦形态仍然超行：收选项窗口，最后才收 ▼。
	for f.optionRows > 1 && f.rowsFor() > space {
		f.optionRows--
		f.scrollUp, f.scrollDown = askScrollFlags(nOpts, p.scroll, f.optionRows)
	}
	return f
}

// askFoldMarker 是"还有多少没看见"的那一行。今天的实现砍完内容不留任何痕迹
// （设计稿 R3），这一行就是补上的那笔账。
func askFoldMarker(qAll, qShown, bAll, bShown []string) string {
	hidden := len(qAll) - len(qShown) + len(bAll) - len(bShown)
	if hidden <= 0 {
		return ""
	}
	// 短句：这行自己也要受 inner 宽约束，写长的句子在 30 列下会被再折一次。
	return fmt.Sprintf("  ┄ %d more · ctrl+o", hidden)
}

func askScrollFlags(nOpts, scroll, window int) (up, down int) {
	if window < 1 {
		window = 1
	}
	up = scroll
	if end := scroll + window; end < nOpts {
		down = nOpts - end
	}
	return up, max(0, down)
}

// askFitNow 是当前显示项的排版。clampCursor 可能挪动窗口，
// 所以 ▲/▼ 在夹完之后必须重算 —— 否则指示行会说谎（说有 3 项，实际只剩 1 项）。
func (m *model) askFitNow() askFit {
	p := m.pendingAsk()
	if p == nil {
		return askFit{}
	}
	f := m.askFitFor(m.askSpaceRows().panelRows, p)
	p.clampCursor(f.optionRows)
	f.scrollUp, f.scrollDown = askScrollFlags(len(p.askOptions()), p.scroll, f.optionRows)
	return f
}

func (m *model) askWindowRows() int {
	if m.pendingAsk() == nil {
		return askWindowMin
	}
	return m.askFitNow().optionRows
}

// askPanelHeight 就是缓存字符串的高度；0 表示没有待答求助（整块不渲染）。
// 空串必须返回 0：lipgloss.Height("") 是 1，照它加进 footer 就会比 View 多算一行
// —— 而 View 因为块是空串不拼它，整屏就少一行（footer 与拼接逐行对应那条契约）。
func (m *model) askPanelHeight() int {
	blk := m.askPanelBlock()
	if blk == "" {
		return 0
	}
	return lipgloss.Height(blk)
}

// askPanelBlock 渲染面板并缓存。缓存随 footer 一起作废（invalidateFooterCache），
// 求助状态变化的每一个入口都调它，所以不会拿到旧串。
func (m *model) askPanelBlock() string {
	if m.pendingAsk() == nil || m.termWidth <= 0 {
		return ""
	}
	// meta 里那个"已等待 Ns"是当帧时间：tick 不作废 footer 缓存（它只重画状态栏的
	// spinner），所以这里按整秒比较，既不会把秒数冻住，也不会每帧重排一次面板。
	sec := int(time.Since(m.pendingAsk().arrivedAt).Seconds())
	if m.askBlockValid && m.askBlockSec == sec {
		return m.askBlockCache
	}
	out := m.renderAskPanel()
	m.askBlockCache, m.askBlockValid, m.askBlockSec = out, true, sec
	return out
}

// 边框色是焦点态的第二重指示（第一重是 ◉/○ 与 ▸ 导轨，去色后仍可辨认）。
var (
	askColorList = lipgloss.Color("214") // 有待答求助，焦点在选项列表
	askColorAuth = lipgloss.Color("39")  // 授权待批
	askColorType = lipgloss.Color("39")  // 焦点已切到输入框：面板让位，压暗
	askColorIdle = lipgloss.Color("94")  // 授权 + 焦点不在列表：等按键，压暗
)

func askEdgeColor(p *pendingAsk) color.Color {
	if p.inputMode {
		if p.kind == askKindAuth {
			return askColorIdle
		}
		return askColorType
	}
	if p.kind == askKindAuth {
		return askColorAuth
	}
	return askColorList
}

var (
	askQuestionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true)
	askBodyStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	askCodeStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("151"))
	askFoldStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("242")).Italic(true)
	askWarnStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	askScrollStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("242"))
	askRailStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Bold(true)
	askLabelLit      = lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Bold(true)
	askLabelDim      = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	askKeyLit        = lipgloss.NewStyle().Foreground(lipgloss.Color("250")).Bold(true)
	askKeyDim        = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	askMarkStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("111")).Bold(true)
)

func (m *model) renderAskPanel() string {
	p := m.pendingAsk()
	if p == nil {
		return ""
	}
	f := m.askFitNow()
	w := m.panelWidth()
	edge := askEdgeColor(p)

	label := "ASK"
	if p.kind == askKindAuth {
		label = "AUTH"
	}
	body := func(s string) string { return editorBodyStyle(edge, w).Render(s) }

	rows := []string{renderPanelTop(label, m.askPanelMeta(p), w, edge)}
	for i, l := range f.question {
		if i == 0 {
			rows = append(rows, body(askMarkStyle.Render("◆")+" "+askQuestionStyle.Render(l)))
			continue
		}
		rows = append(rows, body("  "+askQuestionStyle.Render(l)))
	}
	if len(f.body) > 0 {
		rows = append(rows, body(""))
		for _, l := range f.body {
			if f.bodyIsCode {
				rows = append(rows, body("  "+askCodeStyle.Render(l)))
				continue
			}
			rows = append(rows, body("  "+askMarkStyle.Render("ℹ")+" "+askBodyStyle.Render(l)))
		}
	}
	if f.foldMarker != "" {
		rows = append(rows, body(askFoldStyle.Render(truncateToDisplayWidth(f.foldMarker, f.inner))))
	}
	if f.warn != "" {
		rows = append(rows, body(askWarnStyle.Render(truncateToDisplayWidth("⚠ "+f.warn, f.inner))))
	}
	rows = append(rows, body(""))

	opts := p.askOptions()
	end := min(p.scroll+f.optionRows, len(opts))
	for i := p.scroll; i < end; i++ {
		rows = append(rows, body(renderChoiceRow(i, opts[i], i == p.focusIndex, !p.inputMode, f.inner)))
	}
	if f.scrollUp > 0 {
		rows = append(rows, body(askScrollStyle.Render(truncateToDisplayWidth(
			fmt.Sprintf("  ▲ %d · ctrl+u", f.scrollUp), f.inner))))
	}
	if f.scrollDown > 0 {
		rows = append(rows, body(askScrollStyle.Render(truncateToDisplayWidth(
			fmt.Sprintf("  ▼ %d · ctrl+d", f.scrollDown), f.inner))))
	}
	rows = append(rows, renderPanelBottom(m.askHintBar(m.askStateNow()), w, edge))
	return strings.Join(rows, "\n")
}

// askPanelMeta 是顶边框右上的状态：队列位置 · 来源 Agent · 已等待秒数。
// 秒数不进缓存键也没关系：invalidateFooterCache 由 tick 触发（见 handleTickMsg），
// 但 tick 在弹窗之外本来就会重算 footer，所以这里读的是当帧时间。
func (m *model) askPanelMeta(p *pendingAsk) string {
	var parts []string
	if m.askCount() > 1 {
		parts = append(parts, fmt.Sprintf("%d/%d", m.askActiveIndex+1, m.askCount()))
	}
	if p.agent != "" {
		parts = append(parts, p.agent)
	}
	if el := int(time.Since(p.arrivedAt).Seconds()); el > 0 {
		parts = append(parts, fmt.Sprintf("%ds", el))
	}
	return strings.Join(parts, " · ")
}

// renderChoiceRow 是决策面板家族唯一的选项行渲染
// （今天 renderConfirm / renderSelect / ConfirmDialog.View 各抄了一份，见设计稿 §7.2）。
//
// 焦点态用**形状**而不是颜色表达：列表焦点时 radio 是 ◉；焦点切到输入框后 radio 全部
// 回到 ○，改用左侧 ▸ 导轨标出"enter 会选中哪一项"。剥掉 ANSI 仍然读得出来 ——
// 今天 Submit/Cancel 两态去色后逐字相同（设计稿 R4 第三条）。
func renderChoiceRow(idx int, label string, focused, listMode bool, inner int) string {
	radio, key := "○", askQuickKey(idx)
	if focused && listMode {
		radio = "◉"
	}
	mark := "  "
	if focused && !listMode {
		mark = "▸ "
	}
	// 一行只有一条算式：左侧固定 4 列（导轨 + radio + 空格），右侧有键帽时占 2 列。
	// 截断预算与补空预算必须是同一个 lead/tail —— 各写一遍的话行宽会多出几列，
	// 于是每条选项都被 editorBodyStyle 再折一次，窄终端上 12 行选项变成 24 行。
	lead := lipgloss.Width(mark) + 2
	tail := 2
	if key == "" {
		tail = 0
	}
	textBudget := max(4, inner-lead-tail)
	text := truncateToDisplayWidth(label, textBudget)
	pad := max(0, textBudget-lipgloss.Width(text))

	var b strings.Builder
	b.WriteString(askRailStyle.Render(mark))
	b.WriteString(radio + " ")
	if focused {
		b.WriteString(askLabelLit.Render(text))
	} else {
		b.WriteString(askLabelDim.Render(text))
	}
	b.WriteString(strings.Repeat(" ", pad))
	if key != "" {
		b.WriteString(" ")
		if focused {
			b.WriteString(askKeyLit.Render(key))
		} else {
			b.WriteString(askKeyDim.Render(key))
		}
	}
	return b.String()
}
