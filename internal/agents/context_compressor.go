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

// TruncatedToolInfo 记录单条被截断的工具结果的 token 统计信息。
type TruncatedToolInfo struct {
	ToolName       string `json:"tool_name"`
	OriginalTokens int    `json:"original_tokens"`
	KeptTokens     int    `json:"kept_tokens"`
	OmittedTokens  int    `json:"omitted_tokens"`
}

// ContextCompressionStats 记录上下文压缩的整体统计信息。
type ContextCompressionStats struct {
	OriginalTokens  int                    `json:"original_tokens"`
	CompressedTokens int                   `json:"compressed_tokens"`
	SavedTokens     int                    `json:"saved_tokens"`
	SavedPercent    float64                `json:"saved_percent"`
	TruncatedCount  int                    `json:"truncated_count"`
	TruncatedTools  []TruncatedToolInfo    `json:"truncated_tools"`
}

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
	case "create_file", "search_replace_in_file", "read_file", "run_bash",
		"semantic_search", "query_code_skeleton", "query_code_snippet",
		"print_dir_tree", "search_by_regex", "query_call_graph",
		"find_function_callee", "find_function_caller":
		return 0
	case "deepthinking":
		return -1
	default:
		return 1
	}
}

// TruncateToolResultsToBudget 当消息总 token 超过 maxTokens 时，按优先级对 tool 执行结果进行截断。
//   - 若总 token ≤ maxTokens，原样返回（零开销），返回 stats 为 nil；
//   - 最多两轮截断：第一轮处理优先级 0 的工具结果，第二轮处理优先级 1 的工具结果；
//   - 每条 tool 消息仅截断一次（通过 TruncationMarker 判断），已截断过的跳过；
//   - deepthinking 等优先级 -1 的工具结果永不截断；
//   - 每截断一条后重新估算总 token，若达标立即返回；
//   - 若发生截断，返回 (messages, stats) 其中 stats 包含压缩统计信息。
func TruncateToolResultsToBudget(messages []llm.Message, maxTokens, keepTokens int) ([]llm.Message, *ContextCompressionStats) {
	originalTotal := estimateMessagesTokens(messages)
	if originalTotal <= maxTokens {
		return messages, nil
	}

	stats := &ContextCompressionStats{
		OriginalTokens: originalTotal,
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
		keptTokens := tokenutil.EstimateTokens(truncated)
		omittedTokens := originalTokens - keptTokens
		if omittedTokens < 0 {
			omittedTokens = 0
		}
		if msg.TruncationMarker != nil {
			msg.TruncationMarker.TruncationPass++
		} else {
			msg.TruncationMarker = &llm.TruncationMarker{
				ToolName:       msg.ToolName,
				OriginalLen:    len(originalContent),
				OmittedLen:     len(originalContent) - len(truncated),
				TruncationPass: 0,
			}
		}
		msg.Content = truncated + "\n\n[truncated: 内容已截断, 原始 " + fmt.Sprintf("%d", originalTokens) + " tokens, 保留 " + fmt.Sprintf("%d", keepTokens) + " tokens]"
		stats.TruncatedCount++
		stats.TruncatedTools = append(stats.TruncatedTools, TruncatedToolInfo{
			ToolName:       msg.ToolName,
			OriginalTokens: originalTokens,
			KeptTokens:     keptTokens,
			OmittedTokens:  omittedTokens,
		})
		if estimateMessagesTokens(messages) <= maxTokens {
			stats.CompressedTokens = estimateMessagesTokens(messages)
			stats.SavedTokens = stats.OriginalTokens - stats.CompressedTokens
			if stats.SavedTokens < 0 {
				stats.SavedTokens = 0
			}
			if stats.OriginalTokens > 0 {
				stats.SavedPercent = float64(stats.SavedTokens) / float64(stats.OriginalTokens) * 100
			}
			return messages, stats
		}
	}
	// 所有可截断消息均已截断，仍未达标
	stats.CompressedTokens = estimateMessagesTokens(messages)
	stats.SavedTokens = stats.OriginalTokens - stats.CompressedTokens
	if stats.SavedTokens < 0 {
		stats.SavedTokens = 0
	}
	if stats.OriginalTokens > 0 {
		stats.SavedPercent = float64(stats.SavedTokens) / float64(stats.OriginalTokens) * 100
	}
	return messages, stats
}
