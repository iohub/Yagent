package director

import (
	"context"
	"fmt"
	"log/slog"

	"yagent/internal/compression"
	"yagent/internal/llm"
	"yagent/internal/memory"
)

// applyContextCompression 是 Director 侧的三档压缩：规则截断 → 紧急摘要 → 终极重建。
// 它从 Planner.Run 的循环里搬出来（planner_run.go 的 PENDING 行数债务就是这段），
// 判据统一走 compression.OverThreshold —— 原来同样的比较在两个循环里抄了四份。
//
// measured（provider 计量口径）既要传进来也要传回去：第二、三档会把消息列表整段重写，
// 那时旧基数已经不属于任何一条消息，必须作废，否则同一步里紧跟着的下一档判定会拿着
// 一个不存在的规模做决定（docs/compression-trigger-design.md §5.3 的生命周期表）。
func (p *Planner) applyContextCompression(ctx context.Context, messages []llm.Message,
	measured compression.Prefix, mem *memory.ConversationMemory,
	compact func(kind, summary string, before, after, saved int)) ([]llm.Message, compression.Prefix) {
	if !p.cfg.CompressEnable {
		return messages, measured
	}

	var modelName string
	if p.cfg.LLM != nil {
		modelName = p.cfg.LLM.Model()
	}
	threshold := compression.ResolveThreshold(modelName, p.cfg.CompressThreshold)
	keepTokens := p.cfg.CompressKeepTokens
	if keepTokens <= 0 {
		keepTokens = compression.DefaultToolResultKeepTokens
	}
	var compStats *compression.ContextCompressionStats
	messages, compStats = compression.TruncateToolResultsToBudget(messages, measured, threshold, keepTokens)
	if compStats != nil && compStats.TruncatedCount > 0 && p.cfg.Publisher != nil {
		truncatedTools := make([]map[string]interface{}, len(compStats.TruncatedTools))
		for ti, tool := range compStats.TruncatedTools {
			truncatedTools[ti] = map[string]interface{}{
				"tool_name":       tool.ToolName,
				"original_tokens": tool.OriginalTokens,
				"kept_tokens":     tool.KeptTokens,
				"omitted_tokens":  tool.OmittedTokens,
			}
		}
		_ = p.cfg.Publisher.Publish("context_compressed", map[string]interface{}{
			"original_tokens":   compStats.OriginalTokens,
			"compressed_tokens": compStats.CompressedTokens,
			"saved_tokens":      compStats.SavedTokens,
			"saved_percent":     compStats.SavedPercent,
			"truncated_count":   compStats.TruncatedCount,
			"truncated_tools":   truncatedTools,
		}, "director")
		slog.Debug("context compression applied",
			"original_tokens", compStats.OriginalTokens,
			"compressed_tokens", compStats.CompressedTokens,
			"saved_tokens", compStats.SavedTokens,
			"saved_percent", compStats.SavedPercent,
			"truncated_count", compStats.TruncatedCount)
		compact(memory.CompactionKindRule, fmt.Sprintf(
			"tool-result truncation: %d results compacted, saved %d tokens (%d → %d)",
			compStats.TruncatedCount, compStats.SavedTokens,
			compStats.OriginalTokens, compStats.CompressedTokens),
			compStats.OriginalTokens, compStats.CompressedTokens, compStats.SavedTokens)
	}

	if compression.OverThreshold(messages, measured, threshold) {
		newMessages, emergencyStats := p.cfg.Compressor.ApplyEmergency(ctx, messages, threshold, mem)
		messages = newMessages
		// 列表被整段重写，上一步的计量已经作废。
		measured = compression.Prefix{}
		if emergencyStats != nil && p.cfg.Publisher != nil {
			_ = p.cfg.Publisher.Publish("context_emergency_compressed", map[string]interface{}{
				"original_tokens":   emergencyStats.OriginalTokens,
				"compressed_tokens": emergencyStats.CompressedTokens,
				"saved_tokens":      emergencyStats.SavedTokens,
				"extracted_blocks":  emergencyStats.ExtractedBlocks,
				"summarized_blocks": emergencyStats.SummarizedBlocks,
				"kept_blocks":       emergencyStats.KeptBlocks,
				"summarized_by_llm": emergencyStats.SummarizedByLLM,
			}, "director")
		}
		slog.Warn("emergency context compression applied",
			"original_tokens", emergencyStats.OriginalTokens,
			"compressed_tokens", emergencyStats.CompressedTokens,
			"extracted_blocks", emergencyStats.ExtractedBlocks,
			"kept_blocks", emergencyStats.KeptBlocks,
			"summarized_by_llm", emergencyStats.SummarizedByLLM)
		compact(memory.CompactionKindEmergency, fmt.Sprintf(
			"emergency compression: %d blocks extracted, %d summarized, %d kept, saved %d tokens (%d → %d)",
			emergencyStats.ExtractedBlocks, emergencyStats.SummarizedBlocks,
			emergencyStats.KeptBlocks, emergencyStats.SavedTokens,
			emergencyStats.OriginalTokens, emergencyStats.CompressedTokens),
			emergencyStats.OriginalTokens, emergencyStats.CompressedTokens, emergencyStats.SavedTokens)
	}

	if p.cfg.UltimateCompressEnable && p.cfg.Journal != nil && compression.OverThreshold(messages, measured, threshold) {
		newMessages, ultStats := p.cfg.Compressor.ApplyUltimate(messages, threshold, mem, p.cfg.UltimateCompressKeepPlans)
		messages = newMessages
		measured = compression.Prefix{} // 同 emergency：列表被重建
		if p.cfg.Publisher != nil && ultStats != nil {
			_ = p.cfg.Publisher.Publish("context_ultimate_compressed", map[string]interface{}{
				"original_tokens":   ultStats.OriginalTokens,
				"compressed_tokens": ultStats.CompressedTokens,
				"saved_tokens":      ultStats.SavedTokens,
				"total_plans":       ultStats.TotalPlans,
				"kept_plans":        ultStats.KeptPlans,
				"user_inputs":       ultStats.UserInputs,
				"truncated":         ultStats.Truncated,
				"detail":            "终极压缩(ultimate compression)已触发,上下文已重置为用户原始输入 + Thought & Plan 块",
			}, "director")
			compact(memory.CompactionKindUltimate, fmt.Sprintf(
				"ultimate compression: context rebuilt from %d user inputs + %d/%d Thought & Plan blocks, saved %d tokens (%d → %d)",
				ultStats.UserInputs, ultStats.KeptPlans, ultStats.TotalPlans,
				ultStats.SavedTokens, ultStats.OriginalTokens, ultStats.CompressedTokens),
				ultStats.OriginalTokens, ultStats.CompressedTokens, ultStats.SavedTokens)
		}
	}

	return messages, measured
}
