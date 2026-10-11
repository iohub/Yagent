package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// thinklink 全屏视图调色板 —— 与 dashboard 的设计语言保持一致：
// 薰衣草强调色 + 靛蓝圆角边框 + dim 标签 / 亮色数值分层。
var (
	tlAccent    = lipgloss.Color("141") // ◆ 图标、选中行导轨
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
)

var (
	thinklinkTitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(tlTitleFg)
	thinklinkDimStyle    = lipgloss.NewStyle().Foreground(tlDimFg)
	thinklinkHairStyle   = lipgloss.NewStyle().Foreground(tlHairFg)
	thinklinkBorderStyle = lipgloss.NewStyle().Foreground(tlBorderFg)
)

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
	if n := len(m.thinklinkEntries); n > 0 {
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

// syncThinklinkDetail 将 cursor 所指条目全文写入详情 viewport 并回到顶部。
func syncThinklinkDetail(m *model) {
	clampThinklinkCursor(m)
	if m.thinklinkVP == nil {
		return
	}
	content := ""
	if m.thinklinkCursor >= 0 && m.thinklinkCursor < len(m.thinklinkEntries) {
		content = m.thinklinkEntries[m.thinklinkCursor].Content
	}
	m.thinklinkVP.SetContent(wrapThinklinkContent(content, thinklinkDetailWidth(m)))
	m.thinklinkVP.GotoTop()
}

// clampThinklinkCursor 保证 cursor 落在条目列表的合法范围内。
func clampThinklinkCursor(m *model) {
	n := len(m.thinklinkEntries)
	if m.thinklinkCursor >= n {
		m.thinklinkCursor = n - 1
	}
	if m.thinklinkCursor < 0 {
		m.thinklinkCursor = 0
	}
}

// wrapThinklinkContent 将内容按 rune 硬折行到指定宽度（不引入额外依赖）。
func wrapThinklinkContent(s string, width int) string {
	if width < 10 {
		width = 10
	}
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line == "" {
			out = append(out, "")
			continue
		}
		runes := []rune(line)
		for len(runes) > width {
			out = append(out, string(runes[:width]))
			runes = runes[width:]
		}
		out = append(out, string(runes))
	}
	return strings.Join(out, "\n")
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

// thinklinkHintBar 渲染底部 keycap 提示条（与 dashboard 的 dashKeycap 风格一致）。
func thinklinkHintBar() string {
	return " " +
		dashKeycap("↑/↓", "select") +
		dashKeycap("enter", "scroll detail") +
		dashKeycap("g", "top") +
		dashKeycap("esc/q", "close")
}

// thinklinkHeader 渲染标题行：◆ Thinklink + 副标题（左），条目计数（右对齐）。
func thinklinkHeader(m *model, width int) string {
	diamond := lipgloss.NewStyle().Foreground(tlAccent).Bold(true).Render("◆ ")
	title := thinklinkTitleStyle.Render("Thinklink")
	sub := thinklinkDimStyle.Render("  user inputs & thought plans")
	left := diamond + title + sub

	count := lipgloss.NewStyle().Bold(true).Foreground(tlTitleFg).
		Render(fmt.Sprintf("%d", len(m.thinklinkEntries)))
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
			} else if m.thinklinkCursor < len(m.thinklinkEntries)-1 {
				m.thinklinkCursor++
				syncThinklinkDetail(m)
			}
			return m, nil
		case "enter":
			// 切换详情滚动模式（列表导航 ↔ 详情滚动）
			m.thinklinkDetailActive = !m.thinklinkDetailActive
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
			return m, tickCmd()
		}
		return m, nil
	}
	// 其余未知消息（如 MouseMsg、未匹配任何 case 的消息，以及 KeyMsg 中
	// 未匹配已知按键的情况）：与主 Update 的 dialog 弹窗分支 default 策略
	// 一致——任务运行时同时续上事件链与 tick 链，否则退出全屏后主界面
	// 永久失去刷新（TUI 冻结）。
	if m.taskRunning {
		return m, tea.Batch(listenForEvents(m.eventCh), tickCmd())
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
	if len(m.thinklinkEntries) == 0 {
		emptyMsg := lipgloss.NewStyle().Foreground(tlDimFg).Italic(true).
			Render("◇  No thinklink entries yet")
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
		b.WriteString(thinklinkDimStyle.Render(thinklinkHintBar()))
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
	n := len(m.thinklinkEntries)
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
		b.WriteString(renderThinklinkListLine(m, i, width) + "\n")
	}
	for i := end - start; i < listH; i++ {
		b.WriteString("\n") // 列表不满时补空行，保持详情区位置稳定
	}

	// ── 圆角详情面板 ──
	entry := m.thinklinkEntries[m.thinklinkCursor]
	for _, l := range renderThinklinkDetailPanel(m, entry, width, detailH) {
		b.WriteString(l + "\n")
	}

	// ── 底部 keycap 提示（最后一行不带换行） ──
	b.WriteString(thinklinkDimStyle.Render(thinklinkHintBar()))

	return tea.View{AltScreen: true, Content: b.String()}
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

	// 顶边框：╭─ ◆ DETAIL · TP-02 · static ─────╮
	label := lipgloss.NewStyle().Foreground(tlAccent).Bold(true).Render(" ◆ ") +
		lipgloss.NewStyle().Bold(true).Foreground(tlTitleFg).Render("DETAIL") +
		lipgloss.NewStyle().Foreground(tlDimFg).Render(fmt.Sprintf(" · %s · %s ", detailLabel, scrollMode))
	fill := width - 3 - lipgloss.Width(label)
	if fill < 0 {
		fill = 0
	}
	top := thinklinkBorderStyle.Render("╭─") + label +
		thinklinkBorderStyle.Render(strings.Repeat("─", fill)+"╮")

	// 内容行
	var inner []string
	if m.thinklinkDetailActive && m.thinklinkVP != nil {
		inner = strings.Split(m.thinklinkVP.View(), "\n")
	} else {
		wrapped := strings.Split(wrapThinklinkContent(entry.Content, innerW), "\n")
		truncated := false
		if len(wrapped) > detailH {
			wrapped = wrapped[:detailH]
			truncated = true
		}
		if truncated && len(wrapped) > 0 {
			last := wrapped[len(wrapped)-1]
			suffix := thinklinkDimStyle.Render(" …more (enter to scroll)")
			room := innerW - lipgloss.Width(last) - lipgloss.Width(suffix)
			if room < 0 {
				last = lipgloss.NewStyle().MaxWidth(innerW - lipgloss.Width(suffix)).Render(last)
			}
			wrapped[len(wrapped)-1] = last + suffix
		}
		inner = wrapped
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
	padStyle := lipgloss.NewStyle().Width(innerW)
	for _, l := range inner {
		if lipgloss.Width(l) > innerW {
			l = lipgloss.NewStyle().MaxWidth(innerW).Render(l)
		}
		lines = append(lines,
			thinklinkBorderStyle.Render("│ ")+padStyle.Render(l)+thinklinkBorderStyle.Render(" │"))
	}
	lines = append(lines, thinklinkBorderStyle.Render("╰"+strings.Repeat("─", width-2)+"╯"))
	return lines
}

// renderThinklinkListLine 渲染单行条目：导轨标记 + 序号 + 类型 chip + 时间 + 步数 + 预览。
// 选中行以亮色 ▌ 导轨与更亮的文字突出，未选中行为暗色 │ 导轨。
func renderThinklinkListLine(m *model, idx, width int) string {
	e := m.thinklinkEntries[idx]
	selected := idx == m.thinklinkCursor

	// 导轨
	rail := lipgloss.NewStyle().Foreground(tlRailFg).Render("│")
	if selected {
		rail = lipgloss.NewStyle().Foreground(tlAccent).Bold(true).Render("▌")
	}

	// 序号
	idxStr := fmt.Sprintf("%02d", idx+1)
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
	preview := truncateThinklinkRunes(firstThinklinkLine(e.Content), avail)
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
