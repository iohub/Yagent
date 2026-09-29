package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// thinklink 全屏视图样式。
var (
	thinklinkTitleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	thinklinkUserStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))  // USER INPUT 徽标
	thinklinkPlanStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("214")) // THOUGHT PLAN 徽标
	thinklinkCursorStyle = lipgloss.NewStyle().Reverse(true)                                // 选中行整行反色
	thinklinkDimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))            // 次要信息灰
)

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

// initThinklinkViewport 初始化/重置详情 viewport 尺寸。
func initThinklinkViewport(m *model) {
	vp := viewport.New()
	vp.SetWidth(m.termWidth)
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
	m.thinklinkVP.SetContent(wrapThinklinkContent(content, m.termWidth))
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

// firstThinklinkLine 返回内容中第一个非空行（去空白），用于列表行预览。
func firstThinklinkLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			return trimmed
		}
	}
	return "(empty)"
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
// 标题栏 + 条目列表（窗口切片） + 详情分隔标签 + 详情区 + 底部提示。
func renderThinklinkFullscreenView(m *model) tea.View {
	width, height := m.termWidth, m.termHeight
	var b strings.Builder

	b.WriteString(thinklinkTitleStyle.Render(
		fmt.Sprintf(" Thinklink — User Inputs & Thought & Plan Blocks (%d entries) ",
			len(m.thinklinkEntries))) + "\n")

	// 空态：标题 + 居中提示 + 底部提示
	if len(m.thinklinkEntries) == 0 {
		emptyMsg := thinklinkDimStyle.Render("No thinklink entries yet.")
		pad := (width - lipgloss.Width(emptyMsg)) / 2
		if pad < 0 {
			pad = 0
		}
		midRows := height - 2
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
		b.WriteString(thinklinkDimStyle.Render(
			" ↑/↓ select • enter scroll detail • g top • esc/q close "))
		return tea.View{AltScreen: true, Content: b.String()}
	}

	clampThinklinkCursor(m)
	detailH := thinklinkDetailHeight(m)
	listH := height - 3 - detailH // 标题1 + 详情标签1 + 提示1
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

	// ── 详情分隔标签 ──
	entry := m.thinklinkEntries[m.thinklinkCursor]
	detailLabel := fmt.Sprintf("TP-%02d", m.thinklinkCursor+1)
	if entry.Kind == "user_input" {
		detailLabel = "USER INPUT"
	}
	scrollMode := "static"
	if m.thinklinkDetailActive {
		scrollMode = "scroll"
	}
	sepPlain := fmt.Sprintf("── Detail: [%s] (%s · enter toggles scroll) ", detailLabel, scrollMode)
	if fill := width - lipgloss.Width(sepPlain) - 1; fill > 0 {
		sepPlain += strings.Repeat("─", fill)
	}
	b.WriteString(thinklinkDimStyle.Render(sepPlain) + "\n")

	// ── 详情区 ──
	if m.thinklinkDetailActive && m.thinklinkVP != nil {
		b.WriteString(m.thinklinkVP.View())
		b.WriteString("\n")
	} else {
		wrapped := strings.Split(wrapThinklinkContent(entry.Content, width), "\n")
		truncated := false
		if len(wrapped) > detailH {
			wrapped = wrapped[:detailH]
			truncated = true
		}
		for i, l := range wrapped {
			if i == len(wrapped)-1 {
				// 最后一行不带换行，可能追加截断提示
				b.WriteString(l)
				if truncated {
					b.WriteString(thinklinkDimStyle.Render(" …more (enter to scroll)"))
				}
			} else {
				b.WriteString(l + "\n")
			}
		}
		for i := len(wrapped); i < detailH; i++ {
			b.WriteString("\n") // 不足 detailH 行时补空行
		}
		b.WriteString("\n") // 结束详情最后一行，进入提示行
	}

	// ── 底部提示 ──
	b.WriteString(thinklinkDimStyle.Render(
		" ↑/↓ select • enter scroll detail • g top • esc/q close "))

	return tea.View{AltScreen: true, Content: b.String()}
}

// renderThinklinkListLine 渲染单行条目；cursor 行整行反色高亮，其余行徽标着色。
func renderThinklinkListLine(m *model, idx, width int) string {
	e := m.thinklinkEntries[idx]
	badge := "THOUGHT PLAN"
	if e.Kind == "user_input" {
		badge = "USER INPUT"
	}
	prefix := fmt.Sprintf("  %02d [%s] %s (step %d)  ",
		idx+1, badge, e.Timestamp.Format("15:04:05"), e.Step)
	avail := width - len([]rune(prefix)) - 1
	if avail < 0 {
		avail = 0
	}
	line := prefix + truncateThinklinkRunes(firstThinklinkLine(e.Content), avail)

	if idx == m.thinklinkCursor {
		return thinklinkCursorStyle.Render(line)
	}
	// 非 cursor 行：徽标按类型着色
	badgeStyled := thinklinkPlanStyle.Render(badge)
	if e.Kind == "user_input" {
		badgeStyled = thinklinkUserStyle.Render(badge)
	}
	return strings.Replace(line, "["+badge+"]", "["+badgeStyled+"]", 1)
}
