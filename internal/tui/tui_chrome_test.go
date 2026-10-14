package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

var ansiStripper = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSIForChrome(s string) string {
	return ansiStripper.ReplaceAllString(s, "")
}

// chromeTestModel 构造一个可以完整跑 View() 的模型：输入框、viewport 与
// 生产环境同源（newTaskInput / bindViewportHooks），只是不接后端。
func chromeTestModel(t *testing.T, w, h int) *model {
	t.Helper()
	InitLangManager()

	m := newTestModel()
	m.termWidth, m.termHeight = w, h
	m.viewport = viewport.New(viewport.WithWidth(w), viewport.WithHeight(h))
	m.viewport.Style = lipgloss.NewStyle().Padding(0, 1)
	m.input = newTaskInput("")
	m.anim = NewAnim(10)
	m.tokenUsagePerAgent = make(map[string]*AgentTokenUsage)
	m.bindViewportHooks()
	return m
}

// seedLog 往 viewport 里灌入 n 行纯文本日志，返回其内容用于断言。
func seedLog(m *model, text string) {
	m.viewport.SetContent(text)
	m.contentCache.Reset()
	m.contentCache.WriteString(text)
}

// TestStatusBar_WidthIsExact 覆盖窄到宽的终端：状态栏必须恒等于 termWidth，
// 否则会与 viewport/滚动条拼接错位（多出来的行会让整屏抖动）。
func TestStatusBar_WidthIsExact(t *testing.T) {
	for _, w := range []int{30, 40, 60, 80, 100, 120, 160, 200, 240} {
		for _, mode := range []struct {
			name          string
			commandMode   bool
			taskRunning   bool
			taskCancelled bool
		}{
			{"insert", false, false, false},
			{"normal", true, false, false},
			{"run", false, true, false},
			{"cancelled", false, false, true},
		} {
			m := chromeTestModel(t, w, 40)
			m.commandMode = mode.commandMode
			m.taskRunning = mode.taskRunning
			m.taskCancelled = mode.taskCancelled
			m.currentProvider = "siliconflow"
			m.currentModel = "DeepSeek-V3.2"
			m.autoRouting = true
			m.currentAgent = "CodingAgent"

			bar := m.renderAirlineStatusBar()
			if got := lipgloss.Width(bar); got != w {
				t.Errorf("width=%d mode=%s: status bar width = %d, want %d\n%s",
					w, mode.name, got, w, stripANSIForChrome(bar))
			}
			if n := len(strings.Split(bar, "\n")); n != 1 {
				t.Errorf("width=%d mode=%s: status bar has %d lines, want 1", w, mode.name, n)
			}
		}
	}
}

// TestStatusBar_ModeSemantics NORMAL 与 INSERT 必须各自出现在对应模式下。
func TestStatusBar_ModeSemantics(t *testing.T) {
	m := chromeTestModel(t, 120, 40)

	m.commandMode = true
	if got := m.renderAirlineStatusBar(); !strings.Contains(got, "NORMAL") {
		t.Errorf("command mode should render NORMAL:\n%s", stripANSIForChrome(got))
	}

	m.commandMode = false
	m.taskRunning = false
	m.taskCancelled = false
	if got := m.renderAirlineStatusBar(); !strings.Contains(got, "INSERT") {
		t.Errorf("edit mode should render INSERT:\n%s", stripANSIForChrome(got))
	}
}

// TestStatusBar_SearchProgress 活动搜索要在状态栏里报出 [当前/总数]。
func TestStatusBar_SearchProgress(t *testing.T) {
	m := chromeTestModel(t, 160, 40)
	seedLog(m, "alpha\nbeta gamma\ndelta gamma\nzeta")
	m.runSearch("gamma")

	bar := m.renderAirlineStatusBar()
	if !strings.Contains(bar, "[1/2]") {
		t.Errorf("status bar should show search progress [1/2]:\n%s", stripANSIForChrome(bar))
	}

	m.searchStep(1)
	if !strings.Contains(m.renderAirlineStatusBar(), "[2/2]") {
		t.Error("status bar should advance to [2/2] after n")
	}

	m.clearSearch()
	if strings.Contains(stripANSIForChrome(m.renderAirlineStatusBar()), "[") {
		t.Error("cleared search should not leave a progress chip")
	}
}

// TestPanelChrome_RowsAlign 面板的顶边框、正文、底边框必须同宽且圆角成对。
func TestPanelChrome_RowsAlign(t *testing.T) {
	for _, w := range []int{42, 60, 80, 100, 120, 160, 200} {
		m := chromeTestModel(t, w, 40)
		want := m.panelWidth()

		for _, tc := range []struct {
			name  string
			block func() string
		}{
			{"editor", m.renderEditorPanel},
			{"cmdline-idle", func() string { m.commandMode = true; return m.renderCmdlinePanel() }},
			{"cmdline-ex", func() string {
				m.commandMode = true
				m.commandBuffer = ":q"
				return m.renderCmdlinePanel()
			}},
			{"cmdline-search", func() string {
				m.commandMode = true
				m.commandBuffer = "/hello world"
				return m.renderCmdlinePanel()
			}},
		} {
			lines := strings.Split(tc.block(), "\n")
			for i, line := range lines {
				if got := lipgloss.Width(line); got != want {
					t.Errorf("w=%d case=%s line %d: width = %d, want %d\n%q",
						w, tc.name, i, got, want, stripANSIForChrome(line))
				}
			}
			top := stripANSIForChrome(lines[0])
			bottom := stripANSIForChrome(lines[len(lines)-1])
			if !strings.HasPrefix(top, "╭") || !strings.HasSuffix(top, "╮") {
				t.Errorf("w=%d case=%s: top row not rounded: %q", w, tc.name, top)
			}
			if !strings.HasPrefix(bottom, "╰") || !strings.HasSuffix(bottom, "╯") {
				t.Errorf("w=%d case=%s: bottom row not rounded: %q", w, tc.name, bottom)
			}
		}
	}
}

// TestCmdline_VisibleBuffer 是本次改动的核心回归点：以前 NORMAL 模式下
// commandBuffer 完全不渲染，用户是在盲打。
func TestCmdline_VisibleBuffer(t *testing.T) {
	m := chromeTestModel(t, 100, 40)
	m.commandMode = true

	if h := len(strings.Split(m.renderCmdlinePanel(), "\n")); h != 2 {
		t.Errorf("empty buffer should render a closed 2-row bar, got %d rows", h)
	}

	m.commandBuffer = ":help"
	out := stripANSIForChrome(m.renderCmdlinePanel())
	if h := len(strings.Split(out, "\n")); h != 3 {
		t.Errorf("active buffer should open a 3-row cmdline, got %d rows\n%s", h, out)
	}
	if !strings.Contains(out, ":help") {
		t.Errorf("cmdline must show what was typed:\n%s", out)
	}
	if !strings.Contains(out, "COMMAND") {
		t.Error("cmdline label should read COMMAND for ':' prefix")
	}

	m.commandBuffer = "/错误处理"
	out = stripANSIForChrome(m.renderCmdlinePanel())
	if !strings.Contains(out, "/错误处理") {
		t.Errorf("cmdline must show multibyte input:\n%s", out)
	}
	if !strings.Contains(out, "SEARCH") {
		t.Error("cmdline label should read SEARCH for '/' prefix")
	}
}

// TestCmdline_HistoryRecall ↑ 召回上一条命令，走回最新时恢复草稿。
func TestCmdline_HistoryRecall(t *testing.T) {
	m := chromeTestModel(t, 100, 40)
	m.commandMode = true
	m.pushCmdHistory(":help")
	m.pushCmdHistory(":q")

	m.commandBuffer = ":"
	m.recallCmdHistory(-1)
	if m.commandBuffer != ":q" {
		t.Errorf("first recall = %q, want :q", m.commandBuffer)
	}
	m.recallCmdHistory(-1)
	if m.commandBuffer != ":help" {
		t.Errorf("second recall = %q, want :help", m.commandBuffer)
	}
	m.recallCmdHistory(1)
	m.recallCmdHistory(1)
	if m.commandBuffer != ":" {
		t.Errorf("recalling past the newest should restore the draft, got %q", m.commandBuffer)
	}
}

// TestCmdline_BackspaceIsRuneSafe 中文命令里退格不能切坏半个字符。
func TestCmdline_BackspaceIsRuneSafe(t *testing.T) {
	m := chromeTestModel(t, 100, 40)
	m.commandMode = true
	m.commandBuffer = "/修复"

	step := func() bool {
		_, _, handled := m.handleCmdlineKey("backspace", keyMsgFor("backspace"))
		return handled
	}

	step()
	if m.commandBuffer != "/修" {
		t.Errorf("after one rune-backspace: %q, want %q", m.commandBuffer, "/修")
	}
	if !utf8Valid(m.commandBuffer) {
		t.Errorf("buffer is not valid UTF-8: %q", m.commandBuffer)
	}

	step()
	if m.commandBuffer != "/" {
		t.Errorf("after two backspaces: %q, want /", m.commandBuffer)
	}

	// 只剩前缀时再退一次即关闭命令行。
	step()
	if m.commandBuffer != "" {
		t.Errorf("third backspace should close the cmdline, got %q", m.commandBuffer)
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '\uFFFD' {
			return false
		}
	}
	return true
}

// keyMsgFor 构造按键消息：特殊键走 Code，可打印字符同时带上 Text，
// 与真实终端送进来的形状一致（handleCmdlineKey 读的就是 msg.Key().Text）。
func keyMsgFor(key string) tea.KeyMsg {
	switch key {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "backspace":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	default:
		r := []rune(key)
		return tea.KeyPressMsg{Code: r[0], Text: key}
	}
}

// TestView_ExactHeight 是整屏的总闸门：View() 的行数必须正好等于终端高度，
// computeFooterHeight 与 View 的 footer 拼接只要差一行就会在这里暴露。
func TestView_ExactHeight(t *testing.T) {
	sizes := []struct{ w, h int }{
		{24, 20}, {30, 20}, {42, 20}, {60, 24}, {80, 24},
		{100, 30}, {120, 36}, {140, 45}, {160, 50}, {200, 60},
	}
	for _, sz := range sizes {
		for _, mode := range []struct {
			name        string
			commandMode bool
			buffer      string
		}{
			{"insert", false, ""},
			{"normal", true, ""},
			{"normal-cmdline", true, ":help"},
			{"normal-search", true, "/some pattern"},
		} {
			m := chromeTestModel(t, sz.w, sz.h)
			m.commandMode = mode.commandMode
			m.commandBuffer = mode.buffer
			m.taskStarted = true
			seedLog(m, strings.Repeat("log line\n", 200))

			view := m.View()
			lines := strings.Split(view.Content, "\n")
			if len(lines) != sz.h {
				t.Errorf("w=%d h=%d mode=%s: view has %d lines, want %d\n----\n%s----",
					sz.w, sz.h, mode.name, len(lines), sz.h,
					truncateForTest(stripANSIForChrome(view.Content)))
			}
			for i, line := range lines {
				if got := lipgloss.Width(line); got > sz.w {
					t.Errorf("w=%d h=%d mode=%s line %d overflows: width=%d\n%q",
						sz.w, sz.h, mode.name, i, got, stripANSIForChrome(line))
				}
			}
		}
	}
}

func truncateForTest(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= 40 {
		return s
	}
	return strings.Join(lines[:40], "\n") + "\n...(truncated)"
}

// TestEditorCursorCoordinate 面板右上角要报出输入框的 行:列。
func TestEditorCursorCoordinate(t *testing.T) {
	m := chromeTestModel(t, 100, 40)
	m.input.SetValue("first\nsecond")
	m.input.MoveToBegin()

	out := stripANSIForChrome(m.renderEditorPanel())
	if !strings.Contains(out, "1:1") {
		t.Errorf("top border should show 1:1 at buffer start:\n%s", out)
	}

	m.input.MoveToEnd()
	out = stripANSIForChrome(m.renderEditorPanel())
	if !strings.Contains(out, "2:7") {
		t.Errorf("top border should show 2:7 at buffer end:\n%s", out)
	}
}

// TestHintBar_FitsAndDegrades 提示条放不进窄面板时要整段丢弃而不是溢出。
func TestHintBar_FitsAndDegrades(t *testing.T) {
	hints := "j/k 滚动 · u/d 半页 · gg/G 首尾 · / 搜索 · : 命令 · i 编辑"
	for _, budget := range []int{10, 20, 30, 40, 60, 80, 120} {
		got := renderHintBar(hints, budget)
		if w := lipgloss.Width(got); w > budget {
			t.Errorf("budget=%d: hint bar width = %d, want <= %d (%q)", budget, w, budget, got)
		}
	}
	if got := renderHintBar(hints, 3); got != "" {
		t.Errorf("tiny budget should yield no hints, got %q", got)
	}
}

// TestSearch_JumpsAndCycles /pattern 要真的跳过去，n/N 在命中间循环。
func TestSearch_JumpsAndCycles(t *testing.T) {
	m := chromeTestModel(t, 100, 20)
	// 让命中行落在初始视野之外，才能验证"跳转"确实发生了。
	var b strings.Builder
	for i := 0; i < 60; i++ {
		if i == 45 {
			b.WriteString("needle here\n")
		} else {
			b.WriteString("filler\n")
		}
	}
	seedLog(m, b.String())
	m.updateViewportCache()

	m.runSearch("needle")
	if len(m.searchLines) != 1 || m.searchLines[0] != 45 {
		t.Fatalf("expected one match on line 45, got %v", m.searchLines)
	}
	if m.viewport.YOffset() > 45 || m.viewport.YOffset()+m.viewport.Height() <= 45 {
		t.Errorf("match should be scrolled into view, YOffset=%d", m.viewport.YOffset())
	}
	if got := m.searchLabel(); got != "[1/1]" {
		t.Errorf("search label = %q, want [1/1]", got)
	}
}

// TestSearch_StripANSIBeforeMatching 直接对带转义的原文匹配会让 "38"、"0m"
// 命中颜色码，所以匹配前必须 strip。
func TestSearch_StripANSIBeforeMatching(t *testing.T) {
	m := chromeTestModel(t, 100, 20)
	colored := lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Render("plain text")
	seedLog(m, colored)

	m.runSearch("39")
	if len(m.searchLines) != 0 {
		t.Errorf("color-code digits must not match: got lines %v", m.searchLines)
	}

	m.runSearch("plain")
	if len(m.searchLines) != 1 {
		t.Errorf("visible text should match, got %d lines", len(m.searchLines))
	}
}

// TestSearch_HighlightWired 命中行必须真的带上背景转义。
// viewport.SetHighlights 走不了（它按 ansi.Strip 后的字节位解析却用原文判行），
// 所以我们自己用 StyleLineFunc 上色 —— 这条测试就是那个选择的回归闸门。
func TestSearch_HighlightWired(t *testing.T) {
	m := chromeTestModel(t, 100, 34)
	seedLog(m, "alpha\nbeta needle\ngamma\ndelta\nepsilon\nzeta needle\neta")
	m.viewport.SetHeight(20)
	m.updateViewportCache()

	m.runSearch("needle")
	if len(m.searchLines) != 2 {
		t.Fatalf("expected 2 match lines, got %v", m.searchLines)
	}

	if got := m.searchLineStyle(m.searchLines[0]).Render("X"); !strings.Contains(got, "48;5;94") {
		t.Errorf("current match should carry the strong background, got %q", got)
	}
	if got := m.searchLineStyle(m.searchLines[1]).Render("X"); !strings.Contains(got, "48;5;238") {
		t.Errorf("other matches should carry the dim background, got %q", got)
	}
	if got := m.searchLineStyle(0).Render("X"); strings.Contains(got, "48;") {
		t.Errorf("non-match line must stay unstyled, got %q", got)
	}

	rendered := m.viewport.View()
	if !strings.Contains(rendered, "48;5;94m") || !strings.Contains(rendered, "48;5;238m") {
		t.Error("viewport render must actually emit the highlight escapes")
	}

	m.clearSearch()
	if got := m.searchLineStyle(1).Render("X"); strings.Contains(got, "48;") {
		t.Errorf("cleared search must stop styling lines, got %q", got)
	}
}

// TestSearch_NoMatchReports 无命中要给出去得知的提示，而不是静默。
func TestSearch_NoMatchReports(t *testing.T) {
	m := chromeTestModel(t, 100, 34)
	seedLog(m, "alpha\nbeta\ngamma")
	m.updateViewportCache()

	m.runSearch("nothinghere")
	if m.infoMsg == "" {
		t.Error("a failed search should surface a message")
	}
	if m.hasActiveSearch() {
		t.Error("a failed search should not leave an active highlight state")
	}
}

// TestSearch_RefreshesAfterContentChange 流式输出插入新行会让旧行号失效，
// 命中表必须跟着重算。
func TestSearch_RefreshesAfterContentChange(t *testing.T) {
	m := chromeTestModel(t, 100, 20)
	seedLog(m, "alpha\nbeta\ngamma")
	m.updateViewportCache()
	m.runSearch("gamma")
	if len(m.searchLines) != 1 || m.searchLines[0] != 2 {
		t.Fatalf("initial match = %v, want [2]", m.searchLines)
	}

	seedLog(m, "intro\nintro2\nalpha\nbeta\ngamma")
	m.syncSearchMatches()
	if len(m.searchLines) != 1 || m.searchLines[0] != 4 {
		t.Errorf("match line should follow the new content, got %v", m.searchLines)
	}
}

// TestNormalMode_CountScroll 数字前缀要按倍数滚动。
func TestNormalMode_CountScroll(t *testing.T) {
	m := chromeTestModel(t, 100, 20)
	m.commandMode = true
	seedLog(m, strings.Repeat("line\n", 200))
	m.viewport.SetHeight(10)
	m.viewport.GotoTop()

	press := func(k string) {
		m.handleCommandModeKey(keyMsgFor(k))
	}
	press("1")
	press("0")
	press("j")

	if got := m.viewport.YOffset(); got != 10 {
		t.Errorf("10j scrolled to %d, want 10", got)
	}
	if m.pendingCount != "" {
		t.Errorf("count prefix should be consumed, got %q", m.pendingCount)
	}

	press("k")
	if got := m.viewport.YOffset(); got != 9 {
		t.Errorf("plain k should scroll by 1, YOffset=%d want 9", got)
	}
}

// TestNormalMode_EditablesOnlyInCmdline 普通模式下随手敲的字母不该攒进 buffer。
func TestNormalMode_PrintableDoesNotPolluteBuffer(t *testing.T) {
	m := chromeTestModel(t, 100, 20)
	m.commandMode = true
	seedLog(m, strings.Repeat("line\n", 100))

	m.handleCommandModeKey(keyMsgFor("x"))
	if m.commandBuffer != "" {
		t.Errorf("unbound printable should not enter the buffer, got %q", m.commandBuffer)
	}

	m.handleCommandModeKey(keyMsgFor(":"))
	m.handleCommandModeKey(keyMsgFor("q"))
	if m.commandBuffer != ":q" {
		t.Errorf("cmdline should accumulate, got %q", m.commandBuffer)
	}
}

// TestEditMode_EscEntersNormalMode Esc 是进入普通模式的主路径。
func TestEditMode_EscEntersNormalMode(t *testing.T) {
	m := chromeTestModel(t, 100, 20)
	seedLog(m, "line\n")

	m.handleEditModeKey(keyMsgFor("esc"))
	if !m.commandMode {
		t.Error("esc in edit mode should enter NORMAL mode")
	}

	// 补全展开时 Esc 先收起补全，不能越级改模式。
	m.commandMode = false
	m.skillAutoComplete = true
	m.skillSuggestions = []string{"demo"}
	m.handleEditModeKey(keyMsgFor("esc"))
	if m.commandMode {
		t.Error("esc should only close the completion popup first")
	}
	if m.skillAutoComplete {
		t.Error("completion popup should be closed")
	}
}

// TestView_ExactHeightWithPanels 时间线浮层、token 面板、右侧 dashboard
// 都会改写 footer 的行数——computeFooterHeight 重写后必须在这几种组合下都对得上。
func TestView_ExactHeightWithPanels(t *testing.T) {
	variants := []struct {
		name  string
		tweak func(*model)
	}{
		{"timeline", func(m *model) {
			m.taskRunning = true
			m.timelineEntries = []*TimelineEntry{
				{ID: "t1", Kind: TimelineKindTool, Name: "read_file", Status: ToolStatusSuccess},
				{ID: "t2", Kind: TimelineKindLLMCall, Name: "llm_call", Status: ToolStatusRunning},
			}
		}},
		{"token-dashboard-collapsed", func(m *model) {
			m.taskStarted = true
			m.inputTokens = 120000
			m.outputTokens = 34000
			m.tokenDashboardCollapsed = true
			m.setRun(AgentRunTokens{AgentName: "Director", InputTokens: 120000, OutputTokens: 34000})
		}},
		{"token-dashboard-expanded", func(m *model) {
			m.taskStarted = true
			m.inputTokens = 120000
			m.outputTokens = 34000
			m.tokenUsagePerAgent["Director"] = &AgentTokenUsage{AgentName: "Director", InputTokens: 100000, OutputTokens: 30000}
			m.tokenUsagePerAgent["Coding"] = &AgentTokenUsage{AgentName: "Coding", InputTokens: 20000, OutputTokens: 4000}
		}},
		{"side-dashboard", func(m *model) {
			m.taskRunning = true
			m.inputTokens = 500000
			m.outputTokens = 120000
			m.dashboardCollapsed = false
		}},
		{"err-and-info", func(m *model) {
			m.errMsg = "project dir is not writable"
			m.setInfoMsg("cannot switch model while running")
		}},
	}

	for _, sz := range []struct{ w, h int }{{100, 34}, {120, 40}, {160, 50}} {
		for _, v := range variants {
			for _, commandMode := range []bool{false, true} {
				m := chromeTestModel(t, sz.w, sz.h)
				v.tweak(m)
				m.commandMode = commandMode
				seedLog(m, strings.Repeat("streaming output line\n", 300))

				view := m.View()
				lines := strings.Split(view.Content, "\n")
				label := v.name
				if commandMode {
					label += "/normal"
				} else {
					label += "/insert"
				}
				if len(lines) != sz.h {
					t.Errorf("w=%d h=%d variant=%s: %d lines, want %d",
						sz.w, sz.h, label, len(lines), sz.h)
				}
				for i, line := range lines {
					if got := lipgloss.Width(line); got > sz.w {
						t.Errorf("w=%d h=%d variant=%s line %d overflows to %d: %q",
							sz.w, sz.h, label, i, got, stripANSIForChrome(line))
					}
				}
			}
		}
	}
}

// TestChrome_InfoLineRenders infoMsg 过去只被写入、从不渲染。
func TestChrome_InfoLineRenders(t *testing.T) {
	m := chromeTestModel(t, 100, 40)
	if m.renderInfoLine() != "" {
		t.Error("empty infoMsg should render nothing")
	}
	m.setInfoMsg("cannot do that")
	got := stripANSIForChrome(m.renderInfoLine())
	if !strings.Contains(got, "cannot do that") || !strings.Contains(got, "ℹ") {
		t.Errorf("info line should render with a marker, got %q", got)
	}
	if h := m.computeFooterHeight(); h < 1 {
		t.Errorf("footer should grow for the info line, height=%d", h)
	}
}

// tabTestModel 带会话 tab 的模型：tab 行右端的键帽组只在有 tab 时才渲染。
func tabTestModel(t *testing.T, w, h int) *model {
	t.Helper()
	m := chromeTestModel(t, w, h)
	m.maxTabs = 3
	m.initSessionTabs()
	return m
}

// TestTabBar_GlobalKeycaps 模型/路由的提示挂在 tab 行右端，与 ctrl+t、alt+w 同行。
func TestTabBar_GlobalKeycaps(t *testing.T) {
	m := tabTestModel(t, 140, 40)
	row := stripANSIForChrome(m.renderTabBar())
	for _, key := range []string{"ctrl+t", "alt+w", "alt+m", "alt+r"} {
		if !strings.Contains(row, key) {
			t.Errorf("tab row should advertise %s, got %q", key, row)
		}
	}

	// dashboard 没画出来时（无用量）alt+d 也补位；面板自己的标题栏一旦可见就不重复报。
	if !strings.Contains(row, "alt+d") {
		t.Errorf("tab row should advertise alt+d while the dashboard is hidden, got %q", row)
	}
	m.inputTokens = 1000
	if strings.Contains(stripANSIForChrome(m.renderTabBar()), "alt+d") {
		t.Error("dashboard header already shows alt+d; the tab row should not repeat it")
	}
}

// TestTabBar_KeycapsDegradeByPrefix 空间不够时从尾部丢，不能出现"少了前面的键、留着后面的键"。
func TestTabBar_KeycapsDegradeByPrefix(t *testing.T) {
	order := []string{"ctrl+t", "alt+w", "alt+m", "alt+r", "alt+d"}
	for _, w := range []int{40, 60, 80, 100, 120, 140, 200} {
		m := tabTestModel(t, w, 40)
		m.maxTabs = 10
		for i := 2; i <= 4; i++ {
			m.sessionTabs = append(m.sessionTabs, m.newSessionTab(i, m.generateTabTitle(i)))
		}
		row := stripANSIForChrome(m.renderTabBar())

		seen := -1
		for i, key := range order {
			if strings.Contains(row, key) {
				if i != seen+1 {
					t.Errorf("width=%d: %s shown while an earlier key is missing, row=%q", w, key, row)
				}
				seen = i
			}
		}
	}
}

// TestTabBar_WidthMatrix tab 行必须恒为 1 行且不超过终端宽度。
func TestTabBar_WidthMatrix(t *testing.T) {
	for _, w := range []int{30, 40, 60, 80, 100, 120, 160, 200, 240} {
		m := tabTestModel(t, w, 40)
		row := m.renderTabBar()
		if n := len(strings.Split(row, "\n")); n != 1 {
			t.Errorf("width=%d: tab row has %d lines, want 1", w, n)
		}
		if got := lipgloss.Width(row); got > w {
			t.Errorf("width=%d: tab row width = %d, want <= %d\n%s", w, got, w, stripANSIForChrome(row))
		}
	}
}

// TestStatusBar_CarriesNoKeyHints 底部栏只剩模式与运行时信息：快捷键提示分别属于
// tab 行和面板底边框，写在这里就又是重复广告。
func TestStatusBar_CarriesNoKeyHints(t *testing.T) {
	m := tabTestModel(t, 140, 40)
	m.currentProvider = "siliconflow"
	m.currentModel = "DeepSeek-V3.2"

	for _, mode := range []struct {
		name          string
		commandMode   bool
		taskRunning   bool
		taskCancelled bool
	}{
		{"insert", false, false, false},
		{"normal", true, false, false},
		{"run", false, true, false},
		{"cancelled", false, false, true},
	} {
		m.commandMode, m.taskRunning, m.taskCancelled = mode.commandMode, mode.taskRunning, mode.taskCancelled
		bar := stripANSIForChrome(m.renderAirlineStatusBar())
		for _, unwanted := range []string{"alt+", "ctrl+", "token", "timeline"} {
			if strings.Contains(bar, unwanted) {
				t.Errorf("%s: status bar should not contain %q, got %q", mode.name, unwanted, bar)
			}
		}
	}
}

// TestPanelHints_CtrlLOrdering ctrl+l 挂在面板底边框：编辑面板里排在
// ctrl+h 之前，命令行里排在最后一个动作 i 之后 —— 两处都保证窄面板从尾部丢弃时
// 先掉帮助/动作提示、留住视图开关。
func TestPanelHints_CtrlLOrdering(t *testing.T) {
	prev := langManager.currentLang
	t.Cleanup(func() { langManager.SetLanguage(prev) })

	lastMotion := map[Language]string{LangChinese: "i 编辑", LangEnglish: "i edit"}
	for _, lang := range []Language{LangChinese, LangEnglish} {
		langManager.SetLanguage(lang)

		editor := langManager.GetText("EditorHintEdit")
		li, hi := strings.Index(editor, "ctrl+l"), strings.Index(editor, "ctrl+h")
		if li < 0 || hi < 0 {
			t.Fatalf("lang=%s: editor hint bar should carry both toggles, got %q", lang, editor)
		}
		if li > hi {
			t.Errorf("lang=%s: ctrl+l must precede ctrl+h, got %q", lang, editor)
		}

		cmdline := langManager.GetText("CmdlineHintIdle")
		iIdx, clIdx := strings.Index(cmdline, lastMotion[lang]), strings.Index(cmdline, "ctrl+l")
		if iIdx < 0 || clIdx < 0 {
			t.Fatalf("lang=%s: cmdline hint bar should carry i and ctrl+l, got %q", lang, cmdline)
		}
		if clIdx < iIdx {
			t.Errorf("lang=%s: ctrl+l must follow the last motion, got %q", lang, cmdline)
		}
	}
}

// TestDashboard_ActivityChainsEvents 侧边 dashboard 的 Activity 段既没有进度动画
// 也没有 "running" 文字（进度条在底部栏），只把事件按 git timeline 那样串起来。
func TestDashboard_ActivityChainsEvents(t *testing.T) {
	m := chromeTestModel(t, 140, 40)
	m.taskRunning = true
	m.inputTokens = 1000
	m.tokenUsagePerAgent["Director"] = &AgentTokenUsage{
		AgentName: "Director", InputTokens: 500, OutputTokens: 200,
	}
	m.timelineEntries = []*TimelineEntry{
		{ID: "1", Kind: TimelineKindTool, Status: ToolStatusSuccess, Name: "search_by_regex", Duration: 1200 * time.Millisecond},
		{ID: "2", Kind: TimelineKindTool, Status: ToolStatusRunning, Name: "read_file"},
	}

	dash := stripANSIForChrome(m.renderDashboard(dashboardPanelWidth, 20))
	if strings.ContainsAny(dash, "░▒█") {
		t.Errorf("dashboard should not render the progress animation, got:\n%s", dash)
	}
	if strings.Contains(dash, "running") {
		t.Errorf("dashboard should not spell out a running label, got:\n%s", dash)
	}
	// 脊柱：节点之间必须有连线，且两个工具名都在。
	if !strings.Contains(dash, "\n│  │") {
		t.Errorf("dashboard should chain the two events with a connector, got:\n%s", dash)
	}
	for _, name := range []string{"search_by_regex", "read_file"} {
		if !strings.Contains(dash, name) {
			t.Errorf("dashboard lost the %q entry, got:\n%s", name, dash)
		}
	}

	// 高度不足会截断时间线，截断处不能留下半截连线（孤立的 │ 紧贴 ctrl+l 提示）。
	// 宽松高度下先确认提示确实渲染了，否则下面的断言会是空跑。
	if !strings.Contains(dash, "ctrl+l") {
		t.Errorf("dashboard should show the detail hint at height 20, got:\n%s", dash)
	}
	for _, h := range []int{10, 12, 14, 16, 18, 20, 24} {
		lines := strings.Split(stripANSIForChrome(m.renderDashboard(dashboardPanelWidth, h)), "\n")
		for i, l := range lines {
			if i == 0 || !strings.Contains(l, "ctrl+l") {
				continue
			}
			if inner := strings.TrimSpace(strings.Trim(lines[i-1], "│")); inner == "│" {
				t.Errorf("height=%d: dangling connector above the hint:\n%s", h,
					strings.Join(lines, "\n"))
			}
		}
	}

	// 动画没有整体消失：底部栏仍给出进度条。
	m.currentAgent = "Director"
	if bar := m.renderAirlineStatusBar(); !strings.ContainsAny(bar, "░▒█") {
		t.Errorf("status bar should still carry the progress animation:\n%s",
			stripANSIForChrome(bar))
	}
}
