package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"yagent/internal/thinklink"
)

// thinklink 全屏视图调色板 —— 与 dashboard 的设计语言保持一致：
// 薰衣草强调色 + 靛蓝圆角边框 + dim 标签 / 亮色数值分层。
var (
	tlAccent    = lipgloss.Color("141") // ◆ 图标、选中行导轨、字段标签
	tlBorderFg  = lipgloss.Color("62")  // 详情面板圆角边框
	tlTitleFg   = lipgloss.Color("250") // 标题近白
	tlHairFg    = lipgloss.Color("238") // 发丝分隔线
	tlDimFg     = lipgloss.Color("241") // 次要信息
	tlRailFg    = lipgloss.Color("236") // 未选中行导轨
	tlUserChip  = lipgloss.Color("75")  // USER chip 底色（soft blue）
	tlPlanChip  = lipgloss.Color("214") // PLAN chip 底色（warm gold）
	tlChipFg    = lipgloss.Color("16")  // chip 前景（近黑）
	tlPreviewFg = lipgloss.Color("246") // 未选中行预览
	tlSelPrevFg = lipgloss.Color("252") // 选中行预览

	// 结构化 thought 详情的语义配色
	tlGoalFg    = lipgloss.Color("252") // GOAL 值：亮白，最突出
	tlFieldFg   = lipgloss.Color("246") // STEP/REASON 值
	tlDoneFg    = lipgloss.Color("114") // 已完成条目 ✓（柔和绿）
	tlCurrentFg = lipgloss.Color("141") // 进行中条目 ▸（薰衣草强调）
	tlPendingFg = lipgloss.Color("238") // 待处理条目 ○（暗）
	tlDoneText  = lipgloss.Color("240") // 已完成条目文字（弱化）
)

var (
	thinklinkTitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(tlTitleFg)
	thinklinkDimStyle    = lipgloss.NewStyle().Foreground(tlDimFg)
	thinklinkHairStyle   = lipgloss.NewStyle().Foreground(tlHairFg)
	thinklinkBorderStyle = lipgloss.NewStyle().Foreground(tlBorderFg)

	// 结构化 thought 详情样式
	tlFieldLabelStyle = lipgloss.NewStyle().Bold(true).Foreground(tlAccent)
	tlGoalValueStyle  = lipgloss.NewStyle().Foreground(tlGoalFg)
	tlFieldValueStyle = lipgloss.NewStyle().Foreground(tlFieldFg)
	tlDoneGlyphStyle  = lipgloss.NewStyle().Bold(true).Foreground(tlDoneFg)
	tlCurGlyphStyle   = lipgloss.NewStyle().Bold(true).Foreground(tlCurrentFg)
	tlPendGlyphStyle  = lipgloss.NewStyle().Foreground(tlPendingFg)
	tlDoneTextStyle   = lipgloss.NewStyle().Foreground(tlDoneText)
	tlCurTextStyle    = lipgloss.NewStyle().Bold(true).Foreground(tlSelPrevFg)
	tlPendTextStyle   = lipgloss.NewStyle().Foreground(tlPreviewFg)
)

// thinklinkLabelGutter 结构化详情中字段标签的对齐槽宽（"REASON" 6 + 2 空格）。
const thinklinkLabelGutter = 8

// thinklinkChip 渲染类型徽标（USER / PLAN），彩底深字 chip。
func thinklinkChip(kind string, selected bool) string {
	label, bg := "PLAN", tlPlanChip
	if kind == "user_input" {
		label, bg = "USER", tlUserChip
	}
	style := lipgloss.NewStyle().
		Background(bg).
		Foreground(tlChipFg).
		Bold(true)
	if selected {
		style = style.Underline(true)
	}
	return style.Render(label)
}

// enterThinklinkMode 进入 thinklink 全屏模式：查看用户输入与 Thought & Plan 块。
func enterThinklinkMode(m *model) tea.Cmd {
	m.thinklinkMode = true
	if n := len(thinklinkVisibleEntries(m)); n > 0 {
		m.thinklinkCursor = n - 1
	} else {
		m.thinklinkCursor = 0
	}
	m.thinklinkDetailActive = false
	initThinklinkViewport(m)
	syncThinklinkDetail(m)
	m.invalidateFooterCache()
	return nil
}

// exitThinklinkMode 退出 thinklink 全屏模式并复位全部相关状态。
func exitThinklinkMode(m *model) {
	m.thinklinkMode = false
	m.thinklinkCursor = 0
	m.thinklinkVP = nil
	m.thinklinkDetailActive = false
	m.thinklinkAgentFilter = ""
	m.invalidateFooterCache()
}

// thinklinkAgentOptions 返回条目里出现过的 Agent 名（按首次出现顺序，跳过空名）。
// 空名来自 /resume 恢复的旧 rollout——它们只在"全部"视图里出现。
func thinklinkAgentOptions(entries []ThinklinkEntry) []string {
	var out []string
	for _, e := range entries {
		if e.Agent == "" {
			continue
		}
		seen := false
		for _, a := range out {
			if a == e.Agent {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, e.Agent)
		}
	}
	return out
}

// thinklinkVisibleEntries 当前过滤条件下的条目；cursor 与列表都以此为准。
func thinklinkVisibleEntries(m *model) []ThinklinkEntry {
	if m.thinklinkAgentFilter == "" {
		return m.thinklinkEntries
	}
	out := make([]ThinklinkEntry, 0, len(m.thinklinkEntries))
	for _, e := range m.thinklinkEntries {
		if e.Agent == m.thinklinkAgentFilter {
			out = append(out, e)
		}
	}
	return out
}

// cycleThinklinkAgentFilter 在 All → 各 Agent 之间循环，并把 cursor 落到新列表的最后一条。
func cycleThinklinkAgentFilter(m *model) {
	opts := thinklinkAgentOptions(m.thinklinkEntries)
	if len(opts) == 0 {
		return
	}
	if m.thinklinkAgentFilter == "" {
		m.thinklinkAgentFilter = opts[0]
	} else {
		next := ""
		for i, a := range opts {
			if a == m.thinklinkAgentFilter && i+1 < len(opts) {
				next = opts[i+1]
				break
			}
		}
		m.thinklinkAgentFilter = next // 走到最后一个 Agent 后回到 All
	}
	m.thinklinkDetailActive = false
	initThinklinkViewport(m)
	m.thinklinkCursor = len(thinklinkVisibleEntries(m)) - 1
	syncThinklinkDetail(m)
	m.invalidateFooterCache()
}

// thinklinkDetailHeight 计算详情区高度（约 40% 终端高度，夹在 [3, termHeight-6]）。
func thinklinkDetailHeight(m *model) int {
	h := m.termHeight * 2 / 5
	if h < 3 {
		h = 3
	}
	if maxH := m.termHeight - 6; h > maxH {
		h = maxH
	}
	if h < 1 {
		h = 1
	}
	return h
}

// thinklinkDetailWidth 详情面板内容宽度 = 终端宽 − 边框(2) − 内边距(2)。
func thinklinkDetailWidth(m *model) int {
	w := m.termWidth - 4
	if w < 10 {
		w = 10
	}
	return w
}

// initThinklinkViewport 初始化/重置详情 viewport 尺寸（适配带边框详情面板）。
func initThinklinkViewport(m *model) {
	vp := viewport.New()
	vp.SetWidth(thinklinkDetailWidth(m))
	vp.SetHeight(thinklinkDetailHeight(m))
	m.thinklinkVP = &vp
}

// syncThinklinkDetail 将 cursor 所指条目的结构化渲染结果写入详情 viewport 并回到顶部。
func syncThinklinkDetail(m *model) {
	clampThinklinkCursor(m)
	if m.thinklinkVP == nil {
		return
	}
	visible := thinklinkVisibleEntries(m)
	entry := ThinklinkEntry{}
	if m.thinklinkCursor >= 0 && m.thinklinkCursor < len(visible) {
		entry = visible[m.thinklinkCursor]
	}
	content := thinklinkDetailContent(entry, thinklinkDetailWidth(m))
	m.thinklinkVP.SetContent(content)
	m.thinklinkVP.GotoTop()
}

// clampThinklinkCursor 保证 cursor 落在可见条目列表的合法范围内。
func clampThinklinkCursor(m *model) {
	n := len(thinklinkVisibleEntries(m))
	if m.thinklinkCursor >= n {
		m.thinklinkCursor = n - 1
	}
	if m.thinklinkCursor < 0 {
		m.thinklinkCursor = 0
	}
}

// truncateThinklinkRunes 按 rune 截断字符串，超出部分以 "…" 结尾。
func truncateThinklinkRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	if max == 1 {
		return string(runes[:1])
	}
	return string(runes[:max-1]) + "…"
}

// firstThinklinkLine 返回内容中第一个非空行（去空白与 markdown 前缀），用于列表行预览。
// 跳过 "Thought & Plan" 标题行（每个 plan 块都以它开头，预览价值为零），
// 全部行都是标题时退回标题本身。
func firstThinklinkLine(s string) string {
	fallback := ""
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		clean := strings.TrimLeft(trimmed, "#>*- \t")
		if strings.EqualFold(clean, "Thought & Plan") {
			if fallback == "" {
				fallback = clean
			}
			continue
		}
		return clean
	}
	if fallback != "" {
		return fallback
	}
	return "(empty)"
}

// wrapWords 按词将纯文本折行到 width 列（超长单词硬切）；返回不含样式的行切片。
// 空串返回单元素空切片，保证调用方至少有一行可渲染。
func wrapWords(s string, width int) []string {
	if width < 4 {
		width = 4
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		cur := ""
		for _, w := range words {
			for len([]rune(w)) > width { // 超长单词硬切
				if cur != "" {
					out = append(out, cur)
					cur = ""
				}
				r := []rune(w)
				out = append(out, string(r[:width]))
				w = string(r[width:])
			}
			switch {
			case cur == "":
				cur = w
			case len([]rune(cur))+1+len([]rune(w)) <= width:
				cur += " " + w
			default:
				out = append(out, cur)
				cur = w
			}
		}
		if cur != "" {
			out = append(out, cur)
		}
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// thinklinkDetailContent 返回条目在详情面板中的完整结构化渲染文本（含样式，按 \n 连接）。
// 供滚动模式的 viewport 使用。
func thinklinkDetailContent(entry ThinklinkEntry, innerW int) string {
	return strings.Join(thinklinkDetailLines(entry, innerW), "\n")
}

// thinklinkDetailLines 将条目渲染为已样式、每行显示宽度 ≤ innerW 的行切片。
// 用户输入与非结构化（旧 markdown）内容走原文词折行；结构化 thought 块走字段化渲染。
func thinklinkDetailLines(entry ThinklinkEntry, innerW int) []string {
	if innerW < 10 {
		innerW = 10
	}
	if entry.Kind == "user_input" {
		return renderRawDetailLines(entry.Content, innerW, lipgloss.NewStyle().Foreground(tlSelPrevFg))
	}
	tp := thinklink.ParseThoughtPlan(entry.Content)
	if tp.Empty() {
		// 旧 markdown / 非结构化：原文按词折行渲染（兜底，不丢内容）。
		return renderRawDetailLines(entry.Content, innerW, tlFieldValueStyle)
	}
	return renderThoughtPlanLines(tp, innerW)
}

// renderRawDetailLines 将纯文本按词折行并逐行套用 style。
func renderRawDetailLines(content string, innerW int, style lipgloss.Style) []string {
	wrapped := wrapWords(strings.TrimSpace(content), innerW)
	out := make([]string, 0, len(wrapped))
	for _, l := range wrapped {
		if l == "" {
			out = append(out, "")
			continue
		}
		out = append(out, style.Render(l))
	}
	return out
}

// renderThoughtPlanLines 渲染结构化 thought 块：
// GOAL / STEP / REASON 三个带标签字段 + PLAN 清单（状态图标 + 进度计数）。
func renderThoughtPlanLines(tp thinklink.ThoughtPlan, innerW int) []string {
	var out []string
	out = append(out, renderFieldLines("GOAL", tp.Goal, innerW, tlGoalValueStyle)...)
	out = append(out, renderFieldLines("STEP", tp.Step, innerW, tlFieldValueStyle)...)
	out = append(out, renderFieldLines("REASON", tp.Reason, innerW, tlFieldValueStyle)...)

	if len(tp.Plan) > 0 {
		if len(out) > 0 {
			out = append(out, "")
		}
		out = append(out, renderPlanHeader(tp.Plan, innerW))
		for _, item := range tp.Plan {
			out = append(out, renderPlanItemLines(item, innerW)...)
		}
	}
	if len(out) == 0 {
		out = []string{""}
	}
	return out
}

// renderFieldLines 渲染一个 "标签  值" 字段，值按词折行、续行对齐到标签槽之后。
// 值为空时返回 nil（不占行）。
func renderFieldLines(label, value string, innerW int, valStyle lipgloss.Style) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	textW := innerW - thinklinkLabelGutter
	if textW < 8 {
		textW = 8
	}
	wrapped := wrapWords(value, textW)
	out := make([]string, 0, len(wrapped))
	pad := thinklinkLabelGutter - len(label)
	if pad < 1 {
		pad = 1
	}
	labelPart := tlFieldLabelStyle.Render(label) + strings.Repeat(" ", pad)
	gutter := strings.Repeat(" ", thinklinkLabelGutter)
	for i, l := range wrapped {
		if i == 0 {
			out = append(out, labelPart+valStyle.Render(l))
			continue
		}
		out = append(out, gutter+valStyle.Render(l))
	}
	return out
}

// renderPlanHeader 渲染 PLAN 段标题：左侧 "PLAN"，右侧右对齐的 "已完成/总数" 计数。
func renderPlanHeader(items []thinklink.PlanItem, innerW int) string {
	done := 0
	for _, it := range items {
		if it.Status == thinklink.PlanDone {
			done++
		}
	}
	left := tlFieldLabelStyle.Render("PLAN")
	right := thinklinkDimStyle.Render(fmt.Sprintf("%d/%d ", done, len(items)))
	fill := innerW - lipgloss.Width(left) - lipgloss.Width(right)
	if fill < 1 {
		fill = 1
	}
	return left + strings.Repeat(" ", fill) + right
}

// renderPlanItemLines 渲染单条计划：状态图标（✓/▸/○）+ 词折行文本，续行缩进对齐。
func renderPlanItemLines(item thinklink.PlanItem, innerW int) []string {
	switch item.Status {
	case thinklink.PlanDone:
		return renderPlanItem("✓", tlDoneGlyphStyle, item.Text, tlDoneTextStyle, innerW)
	case thinklink.PlanCurrent:
		return renderPlanItem("▸", tlCurGlyphStyle, item.Text, tlCurTextStyle, innerW)
	default:
		return renderPlanItem("○", tlPendGlyphStyle, item.Text, tlPendTextStyle, innerW)
	}
}

// renderPlanItem 计划条目布局：2 空格缩进 + 图标(1) + 空格(1) = 4 列前缀，文本占余宽。
func renderPlanItem(mark string, glyphStyle lipgloss.Style, text string, textStyle lipgloss.Style, innerW int) []string {
	const indent = "  "
	prefix := indent + glyphStyle.Render(mark) + " "
	contPrefix := indent + "  " + " " // 与文本起点对齐（图标宽 1 + 空格 1）
	textW := innerW - 4
	if textW < 8 {
		textW = 8
	}
	wrapped := wrapWords(strings.TrimSpace(text), textW)
	out := make([]string, 0, len(wrapped))
	for i, l := range wrapped {
		if i == 0 {
			out = append(out, prefix+textStyle.Render(l))
			continue
		}
		out = append(out, contPrefix+textStyle.Render(l))
	}
	return out
}

// thinklinkListPreview 返回列表行预览文本：
// 用户输入取首个非空行；结构化 thought 优先取进行中条目，其次 GOAL；
// 非结构化兜底取首个非空行。
func thinklinkListPreview(entry ThinklinkEntry) string {
	if entry.Kind == "user_input" {
		return firstThinklinkLine(entry.Content)
	}
	tp := thinklink.ParseThoughtPlan(entry.Content)
	if !tp.Empty() {
		for _, it := range tp.Plan {
			if it.Status == thinklink.PlanCurrent && strings.TrimSpace(it.Text) != "" {
				return it.Text
			}
		}
		if strings.TrimSpace(tp.Goal) != "" {
			return tp.Goal
		}
	}
	return firstThinklinkLine(entry.Content)
}

// thinklinkHintCells 底部 keycap 提示，按重要性排列：终端越窄越先裁掉后面的键帽，
// 整行必须留在终端宽度内（折一行就把详情面板的起始位置顶掉，整屏错位）。
var thinklinkHintCells = [][2]string{
	{"↑/↓", "select"},
	{"enter", "scroll detail"},
	{"g", "top"},
	{"f", "filter agent"},
	{"esc/q", "close"},
}

// thinklinkHintLine 渲染底部提示条（与 dashboard 的 dashKeycap 风格一致）。
func thinklinkHintLine(width int) string {
	var sb strings.Builder
	sb.WriteString(" ")
	used := 1
	for _, c := range thinklinkHintCells {
		cell := dashKeycap(c[0], c[1])
		w := lipgloss.Width(cell)
		if used+w > width {
			break
		}
		sb.WriteString(cell)
		used += w
	}
	return thinklinkDimStyle.Render(sb.String())
}

// thinklinkHeader 渲染标题行：◆ Thinklink + 副标题/Agent 过滤（左），条目计数（右对齐）。
func thinklinkHeader(m *model, width int) string {
	diamond := lipgloss.NewStyle().Foreground(tlAccent).Bold(true).Render("◆ ")
	title := thinklinkTitleStyle.Render("Thinklink")
	sub := "  user inputs & thought plans"
	if m.thinklinkAgentFilter != "" {
		sub += " · " + m.thinklinkAgentFilter
	}
	left := diamond + title + thinklinkDimStyle.Render(sub)

	visible := len(thinklinkVisibleEntries(m))
	countText := fmt.Sprintf("%d", visible)
	if m.thinklinkAgentFilter != "" {
		countText = fmt.Sprintf("%d/%d", visible, len(m.thinklinkEntries))
	}
	count := lipgloss.NewStyle().Bold(true).Foreground(tlTitleFg).Render(countText)
	right := count + thinklinkDimStyle.Render(" entries ")

	fill := width - lipgloss.Width(right)
	if fill < lipgloss.Width(left) {
		fill = lipgloss.Width(left)
	}
	line := lipgloss.NewStyle().Width(fill).Render(left) + right
	if lipgloss.Width(line) > width {
		line = lipgloss.NewStyle().MaxWidth(width).Render(line)
	}
	return line
}

// thinklinkFullscreenUpdate thinklink 全屏模式下拦截所有消息。
func thinklinkFullscreenUpdate(msg tea.Msg, m *model) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "q":
			exitThinklinkMode(m)
			return m, nil
		case "up", "k":
			if m.thinklinkDetailActive && m.thinklinkVP != nil {
				m.thinklinkVP.ScrollUp(1)
			} else if m.thinklinkCursor > 0 {
				m.thinklinkCursor--
				syncThinklinkDetail(m)
			}
			return m, nil
		case "down", "j":
			if m.thinklinkDetailActive && m.thinklinkVP != nil {
				m.thinklinkVP.ScrollDown(1)
			} else if m.thinklinkCursor < len(thinklinkVisibleEntries(m))-1 {
				m.thinklinkCursor++
				syncThinklinkDetail(m)
			}
			return m, nil
		case "enter":
			// 切换详情滚动模式（列表导航 ↔ 详情滚动）
			m.thinklinkDetailActive = !m.thinklinkDetailActive
			return m, nil
		case "f":
			// 按 Agent 过滤：一次委派里 Director/Coding/Browser 的块会混排，
			// Browser 一轮就能贡献上百条，不过滤基本没法读。
			cycleThinklinkAgentFilter(m)
			return m, nil
		case "g":
			// 回顶部：滚动模式滚详情，否则跳到第一条
			if m.thinklinkDetailActive && m.thinklinkVP != nil {
				m.thinklinkVP.GotoTop()
			} else {
				m.thinklinkCursor = 0
				syncThinklinkDetail(m)
			}
			return m, nil
		}
	case tea.WindowSizeMsg:
		// 全屏模式下自行处理 resize（不透传给主 Update）
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		initThinklinkViewport(m)
		syncThinklinkDetail(m)
		m.invalidateFooterCache()
		return m, nil
	case taskEventMsg:
		// 全屏模式必须维持任务事件链：透传给 handler（handler 内部有防御检查
		// 且自行续链 listenForEvents），若在此丢弃事件，listenForEvents 链断裂，
		// 退出全屏后主界面永久失去刷新（agent 输出不显示）。
		// 对齐主 Update 的 dialog 弹窗分支策略。
		return m.handleTaskEventMsg(msg)
	case taskCompleteMsg:
		// 任务完成消息若在全屏期间被丢弃，m.taskRunning 会永久卡在 true。
		// 先自动退出全屏回主界面，确保 handler 可能弹出的完成/错误对话框可见、
		// 按键不被全屏吞掉，再透传给 handler 做收尾（重置 taskRunning、动画等）。
		exitThinklinkMode(m)
		return m.handleTaskCompleteMsg(msg)
	case tickMsg:
		// 全屏模式必须维持 tick 链：只续 tick 循环但绝不调用 handleTickMsg
		//（它会直接操作被全屏隐藏的主界面 viewport，造成 UI 错乱）。
		// 对齐主 Update 的 dialog 弹窗分支策略。
		if m.taskRunning {
			return m, m.armTick()
		}
		m.stopTick()
		return m, nil
	}
	// 其余未知消息（如 MouseMsg、未匹配任何 case 的消息，以及 KeyMsg 中
	// 未匹配已知按键的情况）：与主 Update 的 dialog 弹窗分支 default 策略
	// 一致——任务运行时同时续上事件链与 tick 链，否则退出全屏后主界面
	// 永久失去刷新（TUI 冻结）。
	if m.taskRunning {
		return m, tea.Batch(listenForEvents(m.eventCh), m.ensureTick())
	}
	return m, nil
}

// renderThinklinkFullscreenView 渲染 thinklink 全屏视图：
// 标题行 + 发丝线 + 条目列表（窗口切片） + 圆角详情面板 + 底部 keycap 提示。
func renderThinklinkFullscreenView(m *model) tea.View {
	width, height := m.termWidth, m.termHeight
	var b strings.Builder

	b.WriteString(thinklinkHeader(m, width) + "\n")
	b.WriteString(thinklinkHairStyle.Render(strings.Repeat("─", width)) + "\n")

	// 空态：标题 + 发丝线 + 居中提示 + 底部提示
	visible := thinklinkVisibleEntries(m)
	if len(visible) == 0 {
		emptyText := "◇  No thinklink entries yet"
		if m.thinklinkAgentFilter != "" {
			emptyText = "◇  No entries from " + m.thinklinkAgentFilter
		}
		emptyMsg := lipgloss.NewStyle().Foreground(tlDimFg).Italic(true).Render(emptyText)
		pad := (width - lipgloss.Width(emptyMsg)) / 2
		if pad < 0 {
			pad = 0
		}
		midRows := height - 3 // 标题1 + 发丝线1 + 提示1
		top := midRows / 2
		if top < 0 {
			top = 0
		}
		for i := 0; i < top; i++ {
			b.WriteString("\n")
		}
		b.WriteString(strings.Repeat(" ", pad) + emptyMsg + "\n")
		for i := top + 1; i < midRows; i++ {
			b.WriteString("\n")
		}
		b.WriteString(thinklinkHintLine(width))
		return tea.View{AltScreen: true, Content: b.String()}
	}

	clampThinklinkCursor(m)
	detailH := thinklinkDetailHeight(m)
	// 标题1 + 发丝线1 + 详情面板(detailH+2) + 提示1
	listH := height - 5 - detailH
	if listH < 1 {
		listH = 1
	}

	// ── 列表区：窗口切片保证 cursor 可见（优先靠底） ──
	n := len(visible)
	start := 0
	if n > listH {
		start = m.thinklinkCursor - listH + 1
		if start < 0 {
			start = 0
		}
		if start > n-listH {
			start = n - listH
		}
	}
	end := start + listH
	if end > n {
		end = n
	}
	for i := start; i < end; i++ {
		b.WriteString(renderThinklinkListLine(m, visible[i], i, width) + "\n")
	}
	for i := end - start; i < listH; i++ {
		b.WriteString("\n") // 列表不满时补空行，保持详情区位置稳定
	}

	// ── 圆角详情面板 ──
	entry := visible[m.thinklinkCursor]
	for _, l := range renderThinklinkDetailPanel(m, entry, width, detailH) {
		b.WriteString(l + "\n")
	}

	// ── 底部 keycap 提示（最后一行不带换行） ──
	b.WriteString(thinklinkHintLine(width))

	return tea.View{AltScreen: true, Content: b.String()}
}

// thinklinkPanelTop 面板顶边框，标题内嵌在左轨：╭─ ◆ TITLE · meta ─────╮。
func thinklinkPanelTop(title, meta string, width int) string {
	label := lipgloss.NewStyle().Foreground(tlAccent).Bold(true).Render(" ◆ ") +
		lipgloss.NewStyle().Bold(true).Foreground(tlTitleFg).Render(title)
	// meta 里现在带着 Agent 名，窄终端下必须让它先让步，否则长划归零、整行溢出宽度。
	if meta != "" {
		room := width - 3 - lipgloss.Width(label) - 4
		if room < 1 {
			meta = ""
		} else {
			meta = truncateThinklinkRunes(meta, room)
		}
	}
	if meta != "" {
		label += lipgloss.NewStyle().Foreground(tlDimFg).Render(" · " + meta + " ")
	} else {
		label += " " // 无副标题时也留一格，免得标题直接贴上长划线
	}
	fill := width - 3 - lipgloss.Width(label)
	if fill < 0 {
		fill = 0
	}
	return thinklinkBorderStyle.Render("╭─") + label +
		thinklinkBorderStyle.Render(strings.Repeat("─", fill)+"╮")
}

// thinklinkPanelRows 给每行套上左右边框与内边距，超过 innerW 的行被截断。
func thinklinkPanelRows(inner []string, innerW int) []string {
	rows := make([]string, 0, len(inner))
	padStyle := lipgloss.NewStyle().Width(innerW)
	for _, l := range inner {
		if lipgloss.Width(l) > innerW {
			l = lipgloss.NewStyle().MaxWidth(innerW).Render(l)
		}
		rows = append(rows, thinklinkBorderStyle.Render("│ ")+
			padStyle.Render(l)+thinklinkBorderStyle.Render(" │"))
	}
	return rows
}

// thinklinkPanelBottom 面板底边框：╰────╯。
func thinklinkPanelBottom(width int) string {
	return thinklinkBorderStyle.Render("╰" + strings.Repeat("─", width-2) + "╯")
}

// renderThinklinkDetailPanel 渲染带标题的圆角详情面板，
// 返回恰好 detailH+2 行、每行显示宽度为 width 的字符串切片。
func renderThinklinkDetailPanel(m *model, entry ThinklinkEntry, width, detailH int) []string {
	innerW := width - 4 // 边框(2) + 内边距(2)
	if innerW < 1 {
		innerW = 1
	}

	detailLabel := fmt.Sprintf("TP-%02d", m.thinklinkCursor+1)
	if entry.Kind == "user_input" {
		detailLabel = "USER INPUT"
	}
	scrollMode := "static"
	if m.thinklinkDetailActive {
		scrollMode = "scroll"
	}
	// 未经过滤时列表里混排着各 Agent 的块，Agent 名只在详情标题里点出来。
	meta := detailLabel
	if entry.Agent != "" {
		meta += " · " + entry.Agent
	}
	top := thinklinkPanelTop("DETAIL", meta+" · "+scrollMode, width)

	// 内容行
	var inner []string
	if m.thinklinkDetailActive && m.thinklinkVP != nil {
		inner = strings.Split(m.thinklinkVP.View(), "\n")
	} else {
		lines := thinklinkDetailLines(entry, innerW)
		if len(lines) > detailH {
			lines = lines[:detailH]
			if len(lines) > 0 {
				last := lines[len(lines)-1]
				suffix := thinklinkDimStyle.Render(" …more (enter to scroll)")
				room := innerW - lipgloss.Width(last) - lipgloss.Width(suffix)
				if room < 0 {
					last = lipgloss.NewStyle().MaxWidth(innerW - lipgloss.Width(suffix)).Render(last)
				}
				lines[len(lines)-1] = last + suffix
			}
		}
		inner = lines
	}
	// 截断/补齐到 detailH 行
	if len(inner) > detailH {
		inner = inner[:detailH]
	}
	for len(inner) < detailH {
		inner = append(inner, "")
	}

	lines := make([]string, 0, detailH+2)
	lines = append(lines, top)
	lines = append(lines, thinklinkPanelRows(inner, innerW)...)
	lines = append(lines, thinklinkPanelBottom(width))
	return lines
}

// renderThinklinkListLine 渲染单行条目：导轨标记 + 序号 + 类型 chip + 时间 + 步数 + 预览。
// 选中行以亮色 ▌ 导轨与更亮的文字突出，未选中行为暗色 │ 导轨。
func renderThinklinkListLine(m *model, e ThinklinkEntry, pos, width int) string {
	selected := pos == m.thinklinkCursor

	// 导轨
	rail := lipgloss.NewStyle().Foreground(tlRailFg).Render("│")
	if selected {
		rail = lipgloss.NewStyle().Foreground(tlAccent).Bold(true).Render("▌")
	}

	// 序号
	idxStr := fmt.Sprintf("%02d", pos+1)
	idxStyle := lipgloss.NewStyle().Foreground(tlDimFg)
	if selected {
		idxStyle = lipgloss.NewStyle().Bold(true).Foreground(tlTitleFg)
	}

	// 时间 + 步数（step 固定 7 列宽保证预览起点大体对齐）
	timeStyle := lipgloss.NewStyle().Foreground(tlDimFg)
	if selected {
		timeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	}
	stepStr := "  —    "
	if e.Step > 0 {
		stepStr = fmt.Sprintf("%-7s", fmt.Sprintf("step %d", e.Step))
	}

	// 纯文本前缀（用于计算预览可用宽度）：
	// rail(1)+sp(1)+idx(2)+sp(2)+chip(4)+sp(2)+time(8)+sp(2)+step(7)+sp(2)
	plainPrefixLen := 1 + 1 + 2 + 2 + 4 + 2 + 8 + 2 + 7 + 2
	avail := width - plainPrefixLen - 1
	if avail < 0 {
		avail = 0
	}
	preview := truncateThinklinkRunes(thinklinkListPreview(e), avail)
	prevStyle := lipgloss.NewStyle().Foreground(tlPreviewFg)
	if selected {
		prevStyle = lipgloss.NewStyle().Foreground(tlSelPrevFg)
	}

	var sb strings.Builder
	sb.WriteString(rail)
	sb.WriteString(" ")
	sb.WriteString(idxStyle.Render(idxStr))
	sb.WriteString("  ")
	sb.WriteString(thinklinkChip(e.Kind, selected))
	sb.WriteString("  ")
	sb.WriteString(timeStyle.Render(e.Timestamp.Format("15:04:05")))
	sb.WriteString("  ")
	sb.WriteString(timeStyle.Render(stepStr))
	sb.WriteString("  ")
	sb.WriteString(prevStyle.Render(preview))

	line := sb.String()
	if lipgloss.Width(line) > width {
		line = lipgloss.NewStyle().MaxWidth(width).Render(line)
	}
	return line
}
