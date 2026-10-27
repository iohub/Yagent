// planner_memory_test.go — 本轮输入落库与首轮判定的契约。
//
// startsFirstTurn 是项目上下文注入的唯一判据（门面层 director.go:932 消费
// PromptInput.FirstConversation），所以它必须被单独钉住：planner 层的用例只覆盖
// "整条链路对不对"，判据本身少一个分支（例如漏掉 IsSubAgent 过滤）在那里是测不出来的。
package director

import (
	"testing"

	"yagent/internal/memory"
)

func TestStartsFirstTurn(t *testing.T) {
	tests := []struct {
		name  string
		build func(*memory.ConversationMemory)
		want  bool
	}{
		{
			name:  "空 memory＝TUI 新任务首轮",
			build: func(_ *memory.ConversationMemory) {},
			want:  true,
		},
		{
			name:  "http 预写的单条 human 仍是首轮（websocket.go:169）",
			build: func(m *memory.ConversationMemory) { m.AddHumanMessage("HTTP_TASK") },
			want:  true,
		},
		{
			name:  "只有 system 消息也不影响首轮",
			build: func(m *memory.ConversationMemory) { m.AddSystemMessage("SYSTEM") },
			want:  true,
		},
		{
			name: "子 Agent 内部历史不算本任务已经对话过",
			build: func(m *memory.ConversationMemory) {
				m.Append(memory.ChatMessage{Type: memory.MessageTypeAssistant, Content: "SUB", IsSubAgent: true})
				m.Append(memory.ChatMessage{Type: memory.MessageTypeTool, Content: "SUB", ToolCallID: strPtr("call-sub"), IsSubAgent: true})
			},
			want: true,
		},
		{
			name:  "Director 自己回过一句＝追问",
			build: func(m *memory.ConversationMemory) { m.AddAssistantMessage("REPLY", nil) },
			want:  false,
		},
		{
			name:  "有 tool 结果＝追问",
			build: func(m *memory.ConversationMemory) { m.AddToolMessage("RESULT", "call-1") },
			want:  false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mem := memory.NewConversationMemory(300)
			tt.build(mem)
			if got := startsFirstTurn(mem); got != tt.want {
				t.Errorf("startsFirstTurn() = %v, want %v", got, tt.want)
			}
		})
	}

	// 没有 memory 的老调用方必须仍被判为首轮：注入判据不该因为无处可查就失效。
	if !startsFirstTurn(nil) {
		t.Error("startsFirstTurn(nil) = false, want true")
	}
}

func TestSeedHumanInputDedupesPrewrittenMessage(t *testing.T) {
	// http 路径在提交前自己写过一次（websocket.go:169），同一条输入不许落进 memory 两遍。
	mem := memory.NewConversationMemory(300)
	seedHumanInput(mem, "HTTP_TASK")
	seedHumanInput(mem, "HTTP_TASK")
	if got := contentsOfType(mem, memory.MessageTypeHuman); len(got) != 1 || got[0] != "HTTP_TASK" {
		t.Errorf("human 消息 = %v, want [HTTP_TASK]（去重）", got)
	}

	// 末尾不是同一条 human 时（追问、或上一轮以 assistant/tool 收尾）必须照写。
	mem.AddAssistantMessage("REPLY", nil)
	seedHumanInput(mem, "FOLLOW_UP")
	if got := contentsOfType(mem, memory.MessageTypeHuman); len(got) != 2 || got[1] != "FOLLOW_UP" {
		t.Errorf("human 消息 = %v, want [HTTP_TASK FOLLOW_UP]", got)
	}

	// nil memory 不许 panic，也不产出任何消息。
	seedHumanInput(nil, "NO_MEMORY")
}

func contentsOfType(mem *memory.ConversationMemory, kind memory.MessageType) []string {
	var out []string
	for _, m := range mem.GetMessages() {
		if m.Type == kind {
			out = append(out, m.Content)
		}
	}
	return out
}

func strPtr(s string) *string { return &s }
