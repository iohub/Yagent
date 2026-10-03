package compression

import (
	"fmt"

	"yagent/internal/llm"
	"yagent/internal/tokenutil"
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
	OriginalTokens   int               `json:"original_tokens"`
	CompressedTokens int               `json:"compressed_tokens"`
	SavedTokens      int               `json:"saved_tokens"`
	SavedPercent     float64           `json:"saved_percent"`
	TruncatedCount   int               `json:"truncated_count"`
	TruncatedTools   []TruncatedToolInfo `json:"truncated_tools"`
}

// TruncateToTokenBudget 将文本内容截断至不超过 keepTokens 个 token。
// 实现策略：先用粗粒度估算（keepTokens*4 字符）裁剪，再用二分/递减微调至精确满足 token 预算。
// 保证不 panic：长度处理均有边界检查。
func TruncateToTokenBudget(content string, keepTokens int) string {
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
			return content[:min(10, len(content))] + "\n\n[truncated: 内容已截断，原始约1 tokens, 保留 " + fmt.Sprintf("%d", keepTokens) + " tokens]"
		}
		return content
	}
	return content[:lo]
}

// EstimateMessagesTokens 遍历消息列表，累加各消息的 token 估算值。
// 对 assistant 消息，除 Content 外还将 ToolCalls[].Function.Arguments 文本计入。
func EstimateMessagesTokens(messages []llm.Message) int {
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
//   - 0 = 高优先级（最先被截断）：文件读写类（create_file / search_replace_in_file / read_file / run_bash）
//     以及 Repo-Agent 高开销查询类（semantic_search / query_code_skeleton / query_code_snippet /
//     print_dir_tree / search_by_regex / query_call_graph / find_function_callee / find_function_caller）
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

// TruncateToolResultsToBudget 按优先级截断 tool 执行结果，确保总 token 数不超过 maxTokens。
// 返回截断后的消息列表及压缩统计信息。如果未超预算，则原样返回。
func TruncateToolResultsToBudget(messages []llm.Message, maxTokens, keepTokens int) ([]llm.Message, *ContextCompressionStats) {
	currentTokens := EstimateMessagesTokens(messages)
	if currentTokens <= maxTokens {
		return messages, nil
	}

	// 收集所有 assistant 消息中的 tool 结果，按优先级排序
	type toolResultInfo struct {
		msgIndex   int
		callIndex  int
		toolName   string
		result     string
		priority   int
		origTokens int
	}
	var toolResults []toolResultInfo

	for i, msg := range messages {
		if msg.Role == llm.RoleAssistant && msg.ToolResult != nil {
			info := toolResultInfo{
				msgIndex:   i,
				callIndex:  -1, // ToolResult 是单条的
				toolName:   msg.ToolResult.Name,
				result:     msg.ToolResult.Content,
				priority:   toolTruncationPriority(msg.ToolResult.Name),
				origTokens: tokenutil.EstimateTokens(msg.ToolResult.Content),
			}
			toolResults = append(toolResults, info)
		}
	}

	// 按优先级排序（优先级数字越小越先被截断）
	sort.Slice(toolResults, func(i, j int) bool {
		if toolResults[i].priority != toolResults[j].priority {
			return toolResults[i].priority < toolResults[j].priority
		}
		return toolResults[i].origTokens > toolResults[j].origTokens // 同优先级下，token 多的优先被截
	})

	var truncatedTools []TruncatedToolInfo
	originalTokens := currentTokens

	// 遍历并截断，直到满足预算或多条结果都被截断
	for _, info := range toolResults {
		if currentTokens <= maxTokens {
			break
		}
		// deepthinking 被保护，跳过
		if info.priority == -1 {
			continue
		}
		// 截断该 tool 结果
		truncatedContent := TruncateToTokenBudget(info.result, keepTokens)
		truncatedTokens := tokenutil.EstimateTokens(truncatedContent)
		saved := info.origTokens - truncatedTokens

		// 更新 message
		messages[info.msgIndex].ToolResult.Content = truncatedContent

		truncatedTools = append(truncatedTools, TruncatedToolInfo{
			ToolName:    info.toolName,
			OriginalTokens: info.origTokens,
			KeptTokens:  truncatedTokens,
			OmittedTokens: saved,
		})

		currentTokens -= saved
	}

	compressedTokens := EstimateMessagesTokens(messages)
	stats := &ContextCompressionStats{
		OriginalTokens:   originalTokens,
		CompressedTokens: compressedTokens,
		SavedTokens:      originalTokens - compressedTokens,
		SavedPercent:     0.0,
		TruncatedCount:   len(truncatedTools),
		TruncatedTools:   truncatedTools,
	}
	if originalTokens > 0 {
		stats.SavedPercent = float64(stats.SavedTokens) / float64(originalTokens) * 100
	}

	return messages, stats
}