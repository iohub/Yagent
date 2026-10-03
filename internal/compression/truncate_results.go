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
	OriginalTokens   int                   `json:"original_tokens"`
	CompressedTokens int                   `json:"compressed_tokens"`
	SavedTokens      int                   `json:"saved_tokens"`
	SavedPercent     float64               `json:"saved_percent"`
	TruncatedCount   int                   `json:"truncated_count"`
	TruncatedTools   []TruncatedToolInfo   `json:"truncated_tools"`
}

// TruncateToolResultsToBudget 当消息总 token 超过 maxTokens 时，按优先级对 tool 执行结果进行截断。
//   - 若总 token ≤ maxTokens，原样返回（零开销），返回 stats 为 nil；
//   - 最多两轮截断：第一轮处理优先级 0 的工具结果，第二轮处理优先级 1 的工具结果；
//   - 每条 tool 消息仅截断一次（通过 TruncationMarker 判断），已截断过的跳过；
//   - deepthinking 等优先级 -1 的工具结果永不截断；
//   - 每截断一条后重新估算总 token，若达标立即返回；
//   - 若发生截断，返回 (messages, stats) 其中 stats 包含压缩统计信息。
func TruncateToolResultsToBudget(messages []llm.Message, maxTokens, keepTokens int) ([]llm.Message, *ContextCompressionStats) {
	originalTotal := EstimateMessagesTokens(messages)
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
		truncated := TruncateToTokenBudget(originalContent, keepTokens)
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
		msg.Content = truncated + "\n\n[truncated: 内容已截断，原始 " + fmt.Sprintf("%d", originalTokens) + " tokens, 保留 " + fmt.Sprintf("%d", keepTokens) + " tokens]"
		stats.TruncatedCount++
		stats.TruncatedTools = append(stats.TruncatedTools, TruncatedToolInfo{
			ToolName:       msg.ToolName,
			OriginalTokens: originalTokens,
			KeptTokens:     keptTokens,
			OmittedTokens:  omittedTokens,
		})
		if EstimateMessagesTokens(messages) <= maxTokens {
			stats.CompressedTokens = EstimateMessagesTokens(messages)
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
	stats.CompressedTokens = EstimateMessagesTokens(messages)
	stats.SavedTokens = stats.OriginalTokens - stats.CompressedTokens
	if stats.SavedTokens < 0 {
		stats.SavedTokens = 0
	}
	if stats.OriginalTokens > 0 {
		stats.SavedPercent = float64(stats.SavedTokens) / float64(stats.OriginalTokens) * 100
	}
	return messages, stats
}