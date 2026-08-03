package agents

import (
	"fmt"

	"codeactor/internal/llm"
	"codeactor/internal/tokenutil"
)

const (
	// DefaultContextCompressionThreshold 触发上下文压缩的 token 阈值，默认 120000
	DefaultContextCompressionThreshold = 120000
	// DefaultToolResultKeepTokens 截断后每条 tool 结果保留的 token 数，默认 200
	DefaultToolResultKeepTokens = 200
)

// truncateToTokenBudget 将文本内容截断至不超过 keepTokens 个 token。
// 实现策略：先用粗粒度估算（keepTokens*4 字符）裁剪，再用二分/递减微调至精确满足 token 预算。
// 保证不 panic：长度处理均有边界检查。
func truncateToTokenBudget(content string, keepTokens int) string {
	if keepTokens <= 0 {
		return "[truncated: 内容已截断]"
	}
	// 估算目标字符数：粗裁 keepTokens*4 字符作为上界
	candidateLen := keepTokens * 4
	if len(content) <= candidateLen {
		// 内容本身就不大，直接估算是否真的超了
		if tokenutil.EstimateTokens(content) <= keepTokens {
			return content
		}
	} else {
		content = content[:candidateLen]
	}
	// 二分查找最大前缀，使其 token 数 ≤ keepTokens
	lo, hi := 0, len(content)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if tokenutil.EstimateTokens(content[:mid]) <= keepTokens {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	// 兜底：lo 可能为 0（连一个 token 都放不下），此时保留少量字符并标记
	if lo == 0 {
		if len(content) > 0 {
			return content[:min(10, len(content))] + "\n\n[truncated: 内容已截断, 原始约1 tokens, 保留 " + fmt.Sprintf("%d", keepTokens) + " tokens]"
		}
		return content
	}
	return content[:lo]
}

// estimateMessagesTokens 遍历消息列表，累加各消息的 token 估算值。
// 对 assistant 消息，除 Content 外还将 ToolCalls[].Function.Arguments 文本计入。
func estimateMessagesTokens(messages []llm.Message) int {
	total := 0
	for _, msg := range messages {
		total += tokenutil.EstimateTokens(msg.Content)
		if msg.Role == llm.RoleAssistant {
			for _, tc := range msg.ToolCalls {
				total += tokenutil.EstimateTokens(tc.Function.Arguments)
			}
		}
	}
	return total
}

// toolTruncationPriority 返回工具名称的截断优先级：
//   - 0 = 高优先级（最先被截断）：create_file / search_replace_in_file / read_file / run_bash 类工具
//   - 1 = 普通可截断：其他工具
//   - -1 = 保护（永不截断）：deepthinking
func toolTruncationPriority(toolName string) int {
	switch toolName {
	case "create_file", "search_replace_in_file", "read_file", "run_bash":
		return 0
	case "deepthinking":
		return -1
	default:
		return 1
	}
}

// TruncateToolResultsToBudget 当消息总 token 超过 maxTokens 时，按优先级对 tool 执行结果进行截断。
//   - 若总 token ≤ maxTokens，原样返回（零开销）；
//   - 最多两轮截断：第一轮处理优先级 0 的工具结果，第二轮处理优先级 1 的工具结果；
//   - 每条 tool 消息仅截断一次（通过 TruncationMarker 判断），已截断过的跳过；
//   - deepthinking 等优先级 -1 的工具结果永不截断；
//   - 每截断一条后重新估算总 token，若达标立即返回。
func TruncateToolResultsToBudget(messages []llm.Message, maxTokens, keepTokens int) []llm.Message {
	if estimateMessagesTokens(messages) <= maxTokens {
		return messages
	}

	// 收集所有可截断的 tool 消息，按优先级分组
	type toolMsg struct {
		index int
		msg   *llm.Message
		prio  int
	}
	var prio0, prio1 []toolMsg
	for i := range messages {
		msg := &messages[i]
		if msg.Role != llm.RoleTool || msg.ToolName == "" || msg.Content == "" {
			continue
		}
		prio := toolTruncationPriority(msg.ToolName)
		if prio == -1 {
			continue // 保护，永不截断
		}
		if msg.TruncationMarker != nil {
			continue // 已截断过，跳过
		}
		entry := toolMsg{index: i, msg: msg, prio: prio}
		if prio == 0 {
			prio0 = append(prio0, entry)
		} else {
			prio1 = append(prio1, entry)
		}
	}

	// 构建截断顺序：先优先级 0，再优先级 1
	var toTruncate []toolMsg
	toTruncate = append(toTruncate, prio0...)
	toTruncate = append(toTruncate, prio1...)

	// 逐条截断，每截断一条后重新估算，达标即返回
	for _, entry := range toTruncate {
		msg := entry.msg
		originalContent := msg.Content
		originalTokens := tokenutil.EstimateTokens(originalContent)
		truncated := truncateToTokenBudget(originalContent, keepTokens)
		omittedLen := len(originalContent) - len(truncated)
		if msg.TruncationMarker != nil {
			msg.TruncationMarker.TruncationPass++
		} else {
			msg.TruncationMarker = &llm.TruncationMarker{
				ToolName:       msg.ToolName,
				OriginalLen:    len(originalContent),
				OmittedLen:     omittedLen,
				TruncationPass: 0,
			}
		}
		msg.Content = truncated + "\n\n[truncated: 内容已截断, 原始 " + fmt.Sprintf("%d", originalTokens) + " tokens, 保留 " + fmt.Sprintf("%d", keepTokens) + " tokens]"
		if estimateMessagesTokens(messages) <= maxTokens {
			return messages
		}
	}
	return messages
}
