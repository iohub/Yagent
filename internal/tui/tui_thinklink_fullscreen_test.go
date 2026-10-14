package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"yagent/internal/messaging"
)

// thinklink 全屏模式消息处理测试。
//
// 背景：thinklinkFullscreenUpdate 拦截所有消息，若只处理 KeyMsg/WindowSizeMsg
// 而丢弃 taskEventMsg/tickMsg，listenForEvents 与 tick 两条链会在全屏期间断裂，
// 退出全屏后主界面永久失去刷新（TUI 冻结）。本组测试锁定修复后的链保持行为。

// newThinklinkTestModel 构造进入 thinklink 全屏模式的最小 model，
// dialogStack 为空栈（Len()==0），与真实全屏场景一致。
func newThinklinkTestModel() *model {
	m := newTestModel()
	m.thinklinkMode = true
	m.thinklinkCursor = 0
	m.thinklinkDetailActive = false
	return m
}

// 测试 1：taskRunning=true 时 taskEventMsg 必须透传给 handleTaskEventMsg，
// handler 内部自行续链 listenForEvents（cmd 非 nil），且事件真的被消费进日志
// （不是被全屏静默丢弃）。
// 事件类型用 "info"，避开 ai_stream_end / ai_response 的 dialog 穿透特例。
func TestThinklinkFullscreen_TaskEventMsgKeepsChainAlive(t *testing.T) {
	m := newThinklinkTestModel()
	m.taskRunning = true

	evt := &messaging.MessageEvent{
		Type:      messaging.EventType("info"),
		From:      "agent",
		Content:   "hello from agent",
		Timestamp: time.Now(),
	}
	newModel, cmd := thinklinkFullscreenUpdate(taskEventMsg{event: evt}, m)
	got := newModel.(*model)

	if !got.thinklinkMode {
		t.Fatalf("thinklinkMode should stay true after taskEventMsg passthrough")
	}
	if cmd == nil {
		t.Fatalf("expected non-nil cmd (listenForEvents chain must stay alive), got nil")
	}
	// 事件数据不被丢弃：handleTaskEventMsg 的兜底分支会把它落成一条日志。
	if len(got.logEntries) != 1 {
		t.Fatalf("logEntries = %d, want 1 (event dropped on the floor while fullscreen)", len(got.logEntries))
	}
	e := got.logEntries[0]
	if e.eventType != "info" || e.from != "agent" || e.content != "hello from agent" {
		t.Errorf("logged entry = {type:%q from:%q content:%q}, want {info agent \"hello from agent\"}",
			e.eventType, e.from, e.content)
	}
}

// 测试 2：tickMsg 与主 Update dialog 分支策略一致——
// taskRunning=true 时续 tick 链（cmd 非 nil），否则停止（cmd 为 nil）。
func TestThinklinkFullscreen_TickMsgReschedules(t *testing.T) {
	// 运行中：续 tick 链
	m := newThinklinkTestModel()
	m.taskRunning = true
	_, cmd := thinklinkFullscreenUpdate(tickMsg{}, m)
	if cmd == nil {
		t.Fatalf("taskRunning=true: expected non-nil tick cmd, got nil")
	}

	// 空闲：不续 tick 链
	m2 := newThinklinkTestModel()
	m2.taskRunning = false
	_, cmd2 := thinklinkFullscreenUpdate(tickMsg{}, m2)
	if cmd2 != nil {
		t.Fatalf("taskRunning=false: expected nil cmd, got non-nil")
	}
}

// 测试 3：esc 键退出全屏（既有行为不回归）。
func TestThinklinkFullscreen_EscExitsMode(t *testing.T) {
	m := newThinklinkTestModel()

	newModel, cmd := thinklinkFullscreenUpdate(
		tea.KeyPressMsg{Code: tea.KeyEscape}, m)
	got := newModel.(*model)

	if cmd != nil {
		t.Fatalf("esc exit should return nil cmd, got non-nil")
	}
	if got.thinklinkMode {
		t.Fatalf("thinklinkMode should be false after esc, got true")
	}
	if got.thinklinkVP != nil {
		t.Fatalf("thinklinkVP should be nil after exit, got non-nil")
	}
}

// 测试 4：`f` 在 All ↔ 各 Agent 之间循环。
// 背景：一次委派里 Director/Coding/Browser 的块混排在同一条时间线里，
// Browser 一轮就能贡献上百条，不过滤基本读不了；列表、计数、详情都要跟着过滤走。
func TestThinklinkFullscreen_AgentFilterCycles(t *testing.T) {
	m := newThinklinkTestModel()
	block := "goal: verify the pricing page\nreason: need the rendered text\nplan:\n- [>] inspect"
	m.thinklinkEntries = []ThinklinkEntry{
		{ID: "1", Kind: "thought_plan", Agent: "Director", Content: block},
		{ID: "2", Kind: "user_input", Agent: "Browser-Agent", Content: "open example.com"},
		{ID: "3", Kind: "thought_plan", Agent: "Browser-Agent", Content: block},
	}

	steps := []struct {
		wantFilter string
		wantCount  int
	}{
		{"Director", 1},
		{"Browser-Agent", 2},
		{"", 3}, // 走到最后一个 Agent 后回到 All
	}
	for i, s := range steps {
		_, cmd := thinklinkFullscreenUpdate(keyMsgFor("f"), m)
		if cmd != nil {
			t.Fatalf("step %d: f must be handled locally (nil cmd), got non-nil", i)
		}
		if m.thinklinkAgentFilter != s.wantFilter {
			t.Errorf("step %d: filter = %q, want %q", i, m.thinklinkAgentFilter, s.wantFilter)
		}
		if got := len(thinklinkVisibleEntries(m)); got != s.wantCount {
			t.Errorf("step %d: visible = %d, want %d", i, got, s.wantCount)
		}
		if m.thinklinkCursor < 0 || m.thinklinkCursor >= s.wantCount {
			t.Errorf("step %d: cursor = %d out of visible range [0,%d)", i, m.thinklinkCursor, s.wantCount)
		}
	}
}

// 测试 5：/resume 恢复的旧 rollout 没有 Agent 标记，必须留在 All 视图里，
// 不能被任何过滤条件吞掉。
func TestThinklinkFilter_KeepsUnlabeledEntriesInAll(t *testing.T) {
	m := newThinklinkTestModel()
	m.thinklinkEntries = []ThinklinkEntry{
		{ID: "1", Kind: "user_input", Content: "restored input"},
		{ID: "2", Kind: "thought_plan", Agent: "Director", Content: "goal: x\nreason: y"},
	}
	if got := thinklinkAgentOptions(m.thinklinkEntries); len(got) != 1 || got[0] != "Director" {
		t.Fatalf("agent options = %v, want [Director]", got)
	}
	m.thinklinkAgentFilter = "Director"
	if got := len(thinklinkVisibleEntries(m)); got != 1 {
		t.Errorf("visible under Director filter = %d, want 1", got)
	}
	m.thinklinkAgentFilter = ""
	if got := len(thinklinkVisibleEntries(m)); got != 2 {
		t.Errorf("visible in All view = %d, want 2 (unlabeled entries must stay reachable)", got)
	}
}

// 测试 6：过滤态下的全屏渲染在宽度矩阵内不得溢出终端宽度
// （详情面板标题现在带着 Agent 名，窄终端里靠 thinklinkPanelTop 的 meta 截断兜底）。
func TestThinklinkFullscreen_FilteredRenderWidthMatrix(t *testing.T) {
	block := "goal: verify the pricing page\nreason: need the rendered text\nplan:\n- [>] inspect"
	for _, w := range []int{30, 60, 120, 240} {
		m := newThinklinkTestModel()
		m.termWidth, m.termHeight = w, 40
		m.thinklinkEntries = []ThinklinkEntry{
			{ID: "1", Kind: "thought_plan", Agent: "Director", Content: block, Timestamp: time.Now()},
			{ID: "2", Kind: "user_input", Agent: "Browser-Agent", Content: "open example.com", Timestamp: time.Now()},
		}
		m.thinklinkAgentFilter = "Browser-Agent"
		m.thinklinkCursor = 0

		view := renderThinklinkFullscreenView(m)
		for i, line := range strings.Split(view.Content, "\n") {
			if lw := lipgloss.Width(line); lw > w {
				t.Errorf("width %d: line %d overflows (%d cells): %q", w, i, lw, line)
			}
		}
		// 30 列下头部与详情标题都会被裁短，Agent 名只在放得下的宽度上断言。
		if w >= 60 && !strings.Contains(view.Content, "Browser-Agent") {
			t.Errorf("width %d: filtered view should name the active agent", w)
		}
	}
}

// 测试 7：WindowSizeMsg 仍由全屏自行处理——重建 thinklinkVP 并同步尺寸。
func TestThinklinkFullscreen_WindowSizeHandledLocally(t *testing.T) {
	m := newThinklinkTestModel()
	m.thinklinkEntries = []ThinklinkEntry{
		{Content: "entry one"},
		{Content: "entry two"},
	}
	m.thinklinkCursor = 1

	newModel, cmd := thinklinkFullscreenUpdate(
		tea.WindowSizeMsg{Width: 120, Height: 40}, m)
	got := newModel.(*model)

	if cmd != nil {
		t.Fatalf("WindowSizeMsg should be handled locally with nil cmd, got non-nil")
	}
	if got.termWidth != 120 || got.termHeight != 40 {
		t.Fatalf("term size not updated: got %dx%d, want 120x40",
			got.termWidth, got.termHeight)
	}
	if got.thinklinkVP == nil {
		t.Fatalf("thinklinkVP should be re-created on resize, got nil")
	}
	if w := got.thinklinkVP.Width(); w != thinklinkDetailWidth(got) {
		t.Fatalf("thinklinkVP width = %d, want %d", w, thinklinkDetailWidth(got))
	}
	if h := got.thinklinkVP.Height(); h != thinklinkDetailHeight(got) {
		t.Fatalf("thinklinkVP height = %d, want %d", h, thinklinkDetailHeight(got))
	}
}
