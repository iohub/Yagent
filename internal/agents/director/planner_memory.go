// planner_memory.go — 本轮用户输入落进 ConversationMemory 的唯一一处。
//
// 这里原本是 planner_run.go 主循环开头那六行。那个文件的行数被
// scripts/check_file_sizes.sh 的 PENDING 冻结（只许减不许增），而 FirstConversation 的
// 修复必须在写入之前取快照、并在旁边留下解释这个顺序陷阱的注释 —— 把写入搬出去，
// 而不是就地让主循环膨胀。搬迁逐字等价，不要在这里加任何"顺手"的改动。

package director

import "yagent/internal/memory"

// seedHumanInput 把本轮用户输入写进对话记忆。mem 为 nil 时什么都不做；
// 上一轮末尾已经是同一条 human 消息时跳过（http 路径在提交任务前自己先写过一次），
// 避免同一条输入在 memory 里出现两遍。
//
// 调用方必须在调用**之前**判定 FirstConversation：这条输入一落进 memory，
// len(GetMessages()) 就恒 ≥ 1，"本轮开始时是否为空"再也问不出来了。
func seedHumanInput(mem *memory.ConversationMemory, input string) {
	if mem == nil {
		return
	}
	lastMsg, hasLast := mem.LastMessage()
	if !hasLast || lastMsg.Content != input || lastMsg.Type != memory.MessageTypeHuman {
		mem.AddHumanMessage(input)
	}
}
