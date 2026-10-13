package agents

import "yagent/internal/toolspec"

// isInteractiveUserTool 判断给定工具是否会阻塞等待用户响应。
// 这类工具（今天只有 ask_user_for_help）的耗时取决于人什么时候回答，调用侧不能包
// WithTimeout deadline，否则用户还没答，调用就因 context.DeadlineExceeded 被取消。
// 父 context 取消（用户主动中止任务）仍会传播到工具调用，可以正常打断等待。
//
// 判据来自注册表的 Spec.BlocksOnUser，不再是本文件里硬写的名字 ——
// 新增一个"要等人"的工具时，只需在它的 Spec 上声明，超时豁免自动生效。
func isInteractiveUserTool(name string) bool {
	return toolspec.IsInteractive(name)
}
