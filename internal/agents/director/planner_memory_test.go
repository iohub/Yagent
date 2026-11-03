// planner_memory_test.go — 本轮用户输入落库（seedHumanInput）的契约。
//
// 这里原本还单独钉着 startsFirstTurn：那是"要不要给本轮注入工作区根知识文件"的唯一判据，
// 那条注入整体删除之后它也没有消费者了。留下来的是写入本身 —— 同一条输入不许落进 memory
// 两遍（http 路径在提交前自己预写一次，internal/http/websocket.go:169），nil memory 不许 panic。
package director

import (
	"testing"

	"yagent/internal/memory"
)

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
	if contentsOfType(nil, memory.MessageTypeHuman) != nil {
		t.Error("nil memory 不该产出消息")
	}
}

func contentsOfType(mem *memory.ConversationMemory, kind memory.MessageType) []string {
	if mem == nil {
		return nil
	}
	var out []string
	for _, m := range mem.GetMessages() {
		if m.Type == kind {
			out = append(out, m.Content)
		}
	}
	return out
}
