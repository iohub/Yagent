// planner_memory.go — 本轮用户输入落进 ConversationMemory 的唯一一处。
//
// 这里原本是 planner_run.go 主循环开头那几行。搬出来的理由没变：那个文件的行数被
// scripts/check_file_sizes.sh 钉着（只许减不许增），而"输入必须先进 memory、再重建送给
// 模型的 messages"这条顺序陷阱需要在旁边留注释。搬迁逐字等价，不要在这里加任何"顺手"的改动。

package director

import "yagent/internal/memory"

// seedHumanInput 把本轮用户输入写进对话记忆。mem 为 nil 时什么都不做；
// 上一轮末尾已经是同一条 human 消息时跳过（http 路径在提交任务前自己先写过一次，
// internal/http/websocket.go:169 —— 为了让断线/崩溃能从这份记忆恢复，那个预写是有意设计），
// 避免同一条输入在 memory 里出现两遍。
//
// 顺序要紧：它跑在 cfg.Prompts 与"从 memory 重建 messages"之前，所以本轮输入只会经由
// memory 进入模型视野一次。
func seedHumanInput(mem *memory.ConversationMemory, input string) {
	if mem == nil {
		return
	}
	lastMsg, hasLast := mem.LastMessage()
	if !hasLast || lastMsg.Content != input || lastMsg.Type != memory.MessageTypeHuman {
		mem.AddHumanMessage(input)
	}
}
