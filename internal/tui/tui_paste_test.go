package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"yagent/internal/protocol"
)

// TestPaste_LandsInAskAnswerBox 锁定求助作答的粘贴链路。
//
// 前身是 TestUpdate_PasteMsgRoutedToTopDialog：那时 user-help 是个带 textarea 的弹窗，
// 而全局弹窗守卫只放行少数消息类型，tea.PasteMsg（bracketed-paste 的独立消息类型，
// 不是 KeyMsg）曾被吞掉，表现为"对话框里粘不进报错原文"。
// 现在作答走主输入框（docs/ask-panel-redesign.md D1），它本来就在守卫之外，
// 于是这条链路变成"粘贴 → 输入框 → alt+s → 总线上那条回答"。断言的是终点，不是中间某一步。
func TestPaste_LandsInAskAnswerBox(t *testing.T) {
	m, rec := askTestModel(t, 100, 30)
	m.taskRunning = true
	p := feedHelpAsk(m, "贴出报错原文", "", nil, protocol.InteractionInput)
	if !p.inputMode {
		t.Fatal("input 模式应当从输入态开始")
	}

	pasted := "panic: runtime error: invalid memory address\ngoroutine 1 [running]:"
	updated, _ := m.Update(tea.PasteMsg{Content: pasted})
	m = updated.(*model)
	if got := m.input.Value(); got != pasted {
		t.Fatalf("粘贴没有落进答案框：got %q want %q", got, pasted)
	}

	if handled, _ := m.handleAskKey("alt+s"); !handled {
		t.Fatal("alt+s 在输入态没有认领这张键")
	}
	select {
	case sent := <-rec.ch:
		if sent["response"] != pasted {
			t.Errorf("发出去的答案是 %v，want 粘贴的原文", sent["response"])
		}
		if sent["cancelled"] == true {
			t.Error("有答案的回答不该带 cancelled")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("alt+s 之后 2s 内没有发布 user_help_response")
	}
	// 答案发出去之后输入框要还原成求助弹出前的样子，不能留着这段草稿。
	if m.input.Value() != "" {
		t.Errorf("答完之后答案草稿没清掉: %q", m.input.Value())
	}
}
