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
// 总量是**增量**记账的：每截一条只重算那一条。
// 旧写法每截一条都对整份消息重跑两遍 tiktoken，200 条工具结果的会话上要花 9.3s
// （docs/compression-trigger-design.md R2）。
//
// prefix 是 provider 上一步报的计量；没有计量时 Prefix{} 就等价于改动前的全量本地估算。
// 判定与 stats 用两个口径，理由见函数体里的注释。
func TruncateToolResultsToBudget(messages []llm.Message, prefix Prefix, maxTokens, keepTokens int) ([]llm.Message, *ContextCompressionStats) {
	gate := prefix.Total(messages)
	if gate <= maxTokens {
		return messages, nil
	}

	// stats 一律用本地口径：TUI 的 ⧉ 面板按 saved/original 算百分比，把 provider 基数
	// 混进去会让"省了多少"与"一共多少"不同源。没有计量时 gate 本来就是本地值，不必重算。
	original := gate
	if prefix.usable(messages) {
		original = EstimateMessagesTokens(messages)
	}

	stats := &ContextCompressionStats{
		OriginalTokens: original,
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

	// 逐条截断，达标即停。projected 跟着判定口径走（它预测的是"下一步还会不会超"），
	// compressed 跟着本地口径走（stats 与 TUI 面板用它）—— 两者减的是同一个 delta。
	projected, compressed := gate, original
	for _, entry := range toTruncate {
		msg := entry.msg
		originalContent := msg.Content
		before := msgTokens(msg)
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
		// 只重算被截断的那一条，整份消息不需要（旧写法在这里对全量跑两遍 tiktoken）。
		saved := before - msgTokens(msg)
		if saved < 0 {
			saved = 0
		}
		projected -= saved
		compressed -= saved
		stats.TruncatedCount++
		stats.TruncatedTools = append(stats.TruncatedTools, TruncatedToolInfo{
			ToolName:       msg.ToolName,
			OriginalTokens: originalTokens,
			KeptTokens:     keptTokens,
			OmittedTokens:  omittedTokens,
		})
		if projected <= maxTokens {
			break
		}
	}
	// 所有可截断消息均已截断时，projected 仍可能高于 maxTokens —— 那是要交给下一档的信号，
	// 达标与不达标的收尾算法相同，所以这里只有一条路径。
	stats.CompressedTokens = compressed
	stats.SavedTokens = stats.OriginalTokens - stats.CompressedTokens
	if stats.SavedTokens < 0 {
		stats.SavedTokens = 0
	}
	if stats.OriginalTokens > 0 {
		stats.SavedPercent = float64(stats.SavedTokens) / float64(stats.OriginalTokens) * 100
	}
	return messages, stats
}
