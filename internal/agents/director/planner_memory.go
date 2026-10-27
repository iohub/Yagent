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
// 避免同一条输入在 memory 里出现两遍。预写不再影响首轮判定 —— 判定只看有没有 AI 回应，
// 见 startsFirstTurn。
//
// 调用方必须在调用**之前**判定 FirstConversation：这条输入一落进 memory，
// "本轮开始时的状态"就再也问不出来了。
func seedHumanInput(mem *memory.ConversationMemory, input string) {
	if mem == nil {
		return
	}
	lastMsg, hasLast := mem.LastMessage()
	if !hasLast || lastMsg.Content != input || lastMsg.Type != memory.MessageTypeHuman {
		mem.AddHumanMessage(input)
	}
}

// startsFirstTurn 判定「本轮是不是这个任务的第一次对话」：memory 里还没有任何 AI 回应
// （assistant / tool 消息）就是首轮。门面层据此决定要不要注入项目上下文，所以它必须
// 在 seedHumanInput **之前**取用 —— 判据描述的是本轮开始时的状态。
//
// 为什么不能是"memory 为空"：HTTP/WebUI 路径在提交任务之前就把用户消息写进记忆并立即落盘
// （internal/http/websocket.go:169 —— 为了让断线/崩溃能从这份记忆恢复，那个预写是有意设计），
// 于是新任务首轮 memory 里恒有 1 条 human 消息，按"空"判定 FirstConversation 恒为 false，
// 门面层 director.go:932 的注入守卫永不成立，WebUI 用户完全拿不到 YAGENT.md/CLAUDE.md/AGENTS.md。
//
// 只看有没有 AI 回应能一次覆盖两条入口：TUI 新任务 memory 为空 → true；HTTP 新任务预写了
// 一条 human → 依然 true；同一任务的追问里已经有 assistant/tool → false（不重复注入）。
func startsFirstTurn(mem *memory.ConversationMemory) bool {
	if mem == nil {
		return true // 老调用方不带 memory：注入判据不该因为没处可查就失效
	}
	for _, m := range mem.GetMessages() {
		if m.IsSubAgent {
			continue // 子 Agent 内部历史不构成本任务的"上一轮"（与 planner_run.go 的过滤口径一致）
		}
		if m.Type == memory.MessageTypeAssistant || m.Type == memory.MessageTypeTool {
			return false
		}
	}
	return true
}
