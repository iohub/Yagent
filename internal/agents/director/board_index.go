package director

import (
	"log/slog"
	"strings"

	"yagent/internal/blackboard"
	"yagent/internal/config"
	"yagent/internal/llm"
)

// messagesForLLMCall 返回喂给本次 LLM 调用的消息列表：原样加上便签板索引，**不改调用方的切片**。
//
// 名字即契约——调用点写成 `GenerateContent(ctx, p.messagesForLLMCall(messages), ...)`，
// 关键是外层 messages 必须保持无索引：否则下一步的压缩会把目录算进预算，
// 紧急压缩还会把它当成一条用户输入折叠进摘要（docs/blackboard-handoff-design.md §6.3）。
//
// 不变量：索引是派生态——每次调用都重扫磁盘，只进 LLM 的入参，
// 绝不写进 ConversationMemory 也绝不写进 rollout。
// 上下文压缩动的是会话历史里的 tool 消息（[Sub-Agent Result] 头上的 note:/artifact 引用
// 正是这么消失的），而动不了每轮重新生成的索引 —— 这就是引用能扛住压缩的全部原因。
func (p *Planner) messagesForLLMCall(messages []llm.Message) []llm.Message {
	if p.cfg.BoardID == "" {
		return messages
	}
	cfg := config.GlobalBlackboard()
	idx, _, err := blackboard.BuildIndex(p.cfg.BoardID,
		blackboard.IndexOptionsFromConfig(cfg.IndexMaxNotes, cfg.IndexLineChars))
	if err != nil {
		slog.Debug("blackboard: director index build failed", "board", p.cfg.BoardID, "error", err)
		return messages
	}
	if idx == "" {
		return messages
	}

	// 幂等：LLM 重试循环里同一步可能多次调用，不能叠出第二份目录。
	base := stripBoardIndex(messages)
	// 附加在末尾而不是"最后一条 user 消息之前"：多步循环里那条 user 消息往往在列表头部，
	// 插到它前面等于把目录塞到几百步历史之前；而末尾永远是安全的插入点
	// （assistant 的 tool_calls 与其响应总是成段出现，不会被追加动作拆开）。
	out := make([]llm.Message, 0, len(base)+1)
	out = append(out, base...)
	out = append(out, llm.Message{Role: llm.RoleUser, Content: idx})
	return out
}

// stripBoardIndex 去掉此前附加过的索引消息（按固定前缀识别），返回新切片。
func stripBoardIndex(messages []llm.Message) []llm.Message {
	out := make([]llm.Message, 0, len(messages))
	for _, m := range messages {
		if m.Role == llm.RoleUser && strings.HasPrefix(strings.TrimSpace(m.Content), blackboard.IndexHeaderPrefix) {
			continue
		}
		out = append(out, m)
	}
	return out
}
