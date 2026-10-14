package tui

import (
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
	// 粘贴是否真的落进 textarea：textInput 是 components 的未导出字段，本包读不到，
	// 改用提交结果验证 —— Enter 提交的就是 textarea 里的值；
	// 若粘贴被吞，Input 模式的空值校验会让 Result 保持 nil。
	top.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if r := top.Result(); r == nil || r.Response != "pasted-content" {
		t.Errorf("submitted result = %v, want Response %q (paste must have reached the textarea)", r, "pasted-content")
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
