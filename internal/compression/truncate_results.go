package compression

import (
	"yagent/internal/llm"
	"yagent/internal/tokenutil"
	"yagent/internal/toolspec"
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
	OriginalTokens   int                 `json:"original_tokens"`
	CompressedTokens int                 `json:"compressed_tokens"`
	SavedTokens      int                 `json:"saved_tokens"`
	SavedPercent     float64             `json:"saved_percent"`
	TruncatedCount   int                 `json:"truncated_count"`
	TruncatedTools   []TruncatedToolInfo `json:"truncated_tools"`
}

// TruncateToolResultsToBudget 当消息总 token 超过 maxTokens 时，按优先级对 tool 执行结果进行截断。
//   - 若总 token ≤ maxTokens，原样返回（零开销），返回 stats 为 nil；
//   - 最多两轮截断：第一轮处理优先级 0 的工具结果，第二轮处理优先级 1 的工具结果；
//   - 每条 tool 消息仅截断一次（通过 TruncationMarker 判断），已截断过的跳过；
//   - deepthinking 等 TruncProtected 的结果永不截断；
//   - 每截断一条后重新估算总 token，若达标立即返回；
//   - 若发生截断，返回 (messages, stats) 其中 stats 包含压缩统计信息。
//
// 总量是**增量**记账的：开头一次遍历得到逐条 token 数，之后每截一条只重算那一条。
// 旧写法每截一条都对整份消息重跑两遍 tiktoken，200 条工具结果的会话上要花 9.3s
// （docs/compression-trigger-design.md R2）。
func TruncateToolResultsToBudget(messages []llm.Message, maxTokens, keepTokens int) ([]llm.Message, *ContextCompressionStats) {
	tokens := make([]int, len(messages))
	originalTotal := 0
	for i := range messages {
		tokens[i] = msgTokens(&messages[i])
		originalTotal += tokens[i]
	}
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
		prio := toolspec.TruncationPriority(msg.ToolName)
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

	// 逐条截断，每截断一条后按增量更新总量，若达标立即返回
	total := originalTotal
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
		msg.Content = truncated + truncationNote(originalTokens, keepTokens)
		// 这条消息自己重算一遍就够了，整份消息不需要。
		before := tokens[entry.index]
		tokens[entry.index] = msgTokens(msg)
		total -= before - tokens[entry.index]
		stats.TruncatedCount++
		stats.TruncatedTools = append(stats.TruncatedTools, TruncatedToolInfo{
			ToolName:       msg.ToolName,
			OriginalTokens: originalTokens,
			KeptTokens:     keptTokens,
			OmittedTokens:  omittedTokens,
		})
		if total <= maxTokens {
			stats.CompressedTokens = total
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
	stats.CompressedTokens = total
	stats.SavedTokens = stats.OriginalTokens - stats.CompressedTokens
	if stats.SavedTokens < 0 {
		stats.SavedTokens = 0
	}
	if stats.OriginalTokens > 0 {
		stats.SavedPercent = float64(stats.SavedTokens) / float64(stats.OriginalTokens) * 100
	}
	return messages, stats
}
