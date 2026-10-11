package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"yagent/internal/messaging"
	"yagent/internal/protocol"
	"yagent/internal/tui/components"
)

// TestUpdate_PasteMsgRoutedToTopDialog 锁定粘贴路由修复：
// tea.PasteMsg 是 bracketed-paste 的独立消息类型（非 KeyMsg），
// 对话框打开时全局弹窗守卫曾将其吞掉，导致 user-help 的输入框无法粘贴。
// 修复后 PasteMsg 必须被路由到栈顶对话框的 textarea。
func TestUpdate_PasteMsgRoutedToTopDialog(t *testing.T) {
	m := newTestModel()
	dlg := components.NewUserHelpDialog(protocol.UserHelpNeededData{
		Question:        "描述问题",
		InteractionType: protocol.InteractionInput,
		RequestID:       "paste-route-001",
	})
	dlg.SetBounds(m.termWidth, m.termHeight)
	dlg.Init() // 模拟入栈时聚焦 textarea
	m.dialogStack.Push(dlg)

	newModel, _ := m.Update(tea.PasteMsg{Content: "pasted-content"})
	got := newModel.(*model)

	top, ok := got.dialogStack.Top().(*components.UserHelpDialog)
	if !ok {
		t.Fatalf("top dialog type = %T, want *components.UserHelpDialog", got.dialogStack.Top())
	}
	if top.IsClosed() {
		t.Fatal("dialog should stay open after paste")
	}
	// textarea 收到了粘贴内容的间接断言：渲染结果包含该文本
	if v := top.View(); !strings.Contains(v, "pasted-content") {
		t.Errorf("dialog view should contain pasted text, got:\n%s", v)
	}
}

// TestUpdate_PasteMsgKeepsChainsAliveWhileRunning 任务运行中粘贴时，
// 事件链与 tick 链必须与弹窗守卫的 default 分支同样保持存活（cmd 非 nil）。
func TestUpdate_PasteMsgKeepsChainsAliveWhileRunning(t *testing.T) {
	m := newTestModel()
	m.taskRunning = true
	m.eventCh = make(chan *messaging.MessageEvent, 1)
	dlg := components.NewUserHelpDialog(protocol.UserHelpNeededData{
		Question:        "描述问题",
		InteractionType: protocol.InteractionInput,
		RequestID:       "paste-route-002",
	})
	dlg.SetBounds(m.termWidth, m.termHeight)
	m.dialogStack.Push(dlg)

	_, cmd := m.Update(tea.PasteMsg{Content: "x"})
	if cmd == nil {
		t.Fatal("expected non-nil cmd (event/tick chains must stay alive), got nil")
	}
}
