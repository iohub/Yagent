package agents

// askUserToolName 是交互式用户求助工具的注册名。
// 工具实现见 internal/tools/flow_control.go 的 ExecuteAskUserForHelp，
// 由 coding/repo/devops/chat/browser 等子 agent 与 Director 以此名称注册。
const askUserToolName = "ask_user_for_help"

// isInteractiveUserTool 判断给定工具是否为阻塞等待用户交互响应的工具。
// 这类工具（如 ask_user_for_help）的耗时取决于用户响应速度，必须无限等待用户：
// 调用侧不能包 WithTimeout deadline，否则用户尚未响应，调用就会因
// context.DeadlineExceeded 被自动取消。父 context 取消（用户主动中止任务）
// 仍会传播到工具调用，可正常打断等待。
func isInteractiveUserTool(name string) bool {
	return name == askUserToolName
}
