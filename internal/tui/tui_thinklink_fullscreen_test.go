package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

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
// handler 内部自行续链 listenForEvents（cmd 非 nil），事件数据不被丢弃。
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

// 测试 4：WindowSizeMsg 仍由全屏自行处理——重建 thinklinkVP 并同步尺寸。
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
	if w := got.thinklinkVP.Width(); w != 120 {
		t.Fatalf("thinklinkVP width = %d, want 120", w)
	}
	if h := got.thinklinkVP.Height(); h != thinklinkDetailHeight(got) {
		t.Fatalf("thinklinkVP height = %d, want %d", h, thinklinkDetailHeight(got))
	}
}
