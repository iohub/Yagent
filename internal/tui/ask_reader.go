package tui

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ctrl+o 全屏阅读器（设计稿 §6.3）。
//
// 它是"长内容唯一的那条看全通道"：面板里只留折叠摘要，全文在这里逐字可读。
// 折行口径与面板**共用同一个函数**（wrapKeepingSeparators）—— 两处各写一份的话，
// 同一段上下文会在面板与阅读器里断在不同位置，用户以为是两段不同的内容
// （闸门 TestAskReader_WrapsAtPanelWidth）。
//
// 这里**没有** `/` 搜索：设计稿写了，但它需要一套自己的匹配态（与正文搜索那套
// 游标不能混用），没实现就不把键帽印上去 —— "键帽写着、按下没反应"正是这次要修的病。

func (m *model) askReaderLines() []string {
	p := m.pendingAsk()
	if p == nil {
		return nil
	}
	inner := m.termWidth - 8
	if inner < 12 {
		inner = 12
	}
	var out []string
	head := func(t string) {
		out = append(out, "")
		out = append(out, askRailStyle.Render("── "+t+" ──"))
	}

	head("QUESTION")
	out = append(out, wrapKeepingSeparators(p.question, inner)...)
	if body := p.askBodyText(); body != "" {
		head("CONTEXT")
		out = append(out, wrapKeepingSeparators(body, inner)...)
	}
	if p.warning != "" {
		head("WARNING")
		out = append(out, wrapKeepingSeparators(p.warning, inner)...)
	}
	if opts := p.askOptions(); len(opts) > 0 {
		head("OPTIONS")
		for i, o := range opts {
			key := askQuickKey(i)
			if key == "" {
				key = " "
			}
			lines := wrapKeepingSeparators(o, inner-6)
			out = append(out, fmt.Sprintf("  %s  %s", key, lines[0]))
			for _, cont := range lines[1:] {
				out = append(out, "      "+cont)
			}
		}
	}
	if p.requestID != "" {
		out = append(out, "")
		out = append(out, askFoldStyle.Render("  request "+p.requestID))
	}
	return out
}

func (m *model) ensureAskReaderVP() *viewport.Model {
	if m.askReaderVP == nil {
		vp := viewport.New(viewport.WithWidth(m.readerContentWidth()), viewport.WithHeight(m.readerHeight()))
		m.askReaderVP = &vp
	}
	return m.askReaderVP
}

func (m *model) readerContentWidth() int {
	w := m.termWidth - 6
	if w < 12 {
		w = 12
	}
	return w
}

func (m *model) readerHeight() int {
	h := m.termHeight - 4
	if h < 3 {
		h = 3
	}
	return h
}

// openAskReader 进入全屏阅读态。返回 viewport 的初始化 cmd（没有则 nil）。
func (m *model) openAskReader() tea.Cmd {
	if m.pendingAsk() == nil {
		return nil
	}
	m.askReaderMode = true
	vp := m.ensureAskReaderVP()
	vp.SetWidth(m.readerContentWidth())
	vp.SetHeight(m.readerHeight())
	vp.SetContent(strings.Join(m.askReaderLines(), "\n"))
	vp.GotoTop()
	m.invalidateFooterCache()
	return nil
}

func (m *model) closeAskReader() {
	m.askReaderMode = false
	m.invalidateFooterCache()
}

// renderAskReaderView 是整屏覆盖层：与 timeline / thinklink 全屏同一族。
func renderAskReaderView(m *model) tea.View {
	p := m.pendingAsk()
	if p == nil {
		// 求助被答掉了而阅读器还开着：直接退回主界面，不留一层空壳。
		m.askReaderMode = false
		return m.View()
	}
	w := m.termWidth
	edge := askEdgeColor(p)
	vp := m.ensureAskReaderVP()
	vp.SetWidth(m.readerContentWidth())
	vp.SetHeight(m.readerHeight())
	vp.SetContent(strings.Join(m.askReaderLines(), "\n"))

	meta := p.agent
	if m.askCount() > 1 {
		meta = fmt.Sprintf("%d/%d · %s", m.askActiveIndex+1, m.askCount(), meta)
	}
	content := lipgloss.JoinVertical(lipgloss.Left,
		renderPanelTop("FULL TEXT", meta, w, edge),
		editorBodyStyle(edge, w).Render(vp.View()),
		renderPanelBottom("esc back · ↑↓ j/k scroll · ctrl+d/u page · gg/G ends", w, edge),
	)
	return tea.View{AltScreen: true, Content: content}
}

// askReaderUpdate 拦截阅读器开着时的所有消息。
//
// 分支形状必须与 timeline / thinklink 那两个覆盖层逐条一致（tui_thinklink_fullscreen.go:491-556），
// 少一条都不是"难看"而是永久断流：
//   - taskEventMsg 要透传：事件只有 Update 这一条通路，丢弃就是退出全屏后主界面少几条；
//   - taskCompleteMsg 是 taskRunning 唯一的清零点，吞掉就永久卡在"运行中"；
//   - tickMsg 只续链、绝不调 handleTickMsg（它操作的是被覆盖层藏起来的主 viewport）；
//   - 返回值最终还要过 keepListening（tui_update.go:17-25，根 AGENTS.md §11 坑 6）。
func askReaderUpdate(msg tea.Msg, m *model) (tea.Model, tea.Cmd) {
	if m.pendingAsk() == nil {
		// 求助被答掉/取消了：覆盖层没有存在意义，退回主界面再处理这条消息。
		m.askReaderMode = false
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		vp := m.ensureAskReaderVP()
		switch msg.String() {
		case "esc", "ctrl+o", "q":
			m.closeAskReader()
			return m, nil
		case "up", "k":
			vp.ScrollUp(1)
			return m, nil
		case "down", "j":
			vp.ScrollDown(1)
			return m, nil
		case "ctrl+u":
			vp.ScrollUp(vp.Height() / 2)
			return m, nil
		case "ctrl+d":
			vp.ScrollDown(vp.Height() / 2)
			return m, nil
		case "pgup", "ctrl+b":
			vp.PageUp()
			return m, nil
		case "pgdown", "ctrl+f", " ":
			vp.PageDown()
			return m, nil
		case "g", "home":
			vp.GotoTop()
			return m, nil
		case "G", "end":
			vp.GotoBottom()
			return m, nil
		}
		// 其余按键在覆盖层里不落地：阅读器是只读的，
		// 让 i / : / alt+s 这些键漏到主输入框等于在看不见的时候改答案。
		return m, nil

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		m.resizeViewport()
		m.ensureAskReaderVP().SetWidth(m.readerContentWidth())
		m.ensureAskReaderVP().SetHeight(m.readerHeight())
		m.invalidateFooterCache()
		return m, nil

	case taskEventMsg:
		return m.handleTaskEventMsg(msg)

	case taskCompleteMsg:
		m.closeAskReader()
		return m.handleTaskCompleteMsg(msg)

	case tickMsg:
		if m.taskRunning {
			return m, m.armTick()
		}
		m.stopTick()
		return m, nil
	}

	if m.taskRunning {
		return m, tea.Batch(listenForEvents(m.eventCh), m.ensureTick())
	}
	return m, nil
}
