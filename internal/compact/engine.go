package compact

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"codeactor/internal/llm"
)

// Engine 压缩引擎
type Engine struct {
	config       *Config
	tokenizer    Tokenizer
	priorityCalc *PriorityCalculator
	summarizer   *LLMSummarizer
	state        *CompressionState // 新增：增量压缩状态
	stateMu      sync.Mutex        // 新增：状态并发保护
}

// NewEngine 创建压缩引擎
func NewEngine(config *Config, summarizationClient SummarizationClient) (*Engine, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid compact config: %w", err)
	}

	// 创建LLM摘要器（如果提供了客户端）
	var summarizer *LLMSummarizer
	if summarizationClient != nil {
		summarizer = NewLLMSummarizer(summarizationClient, config)
	}

	return &Engine{
		config:       config,
		tokenizer:    GetGlobalTokenizer(),
		priorityCalc: NewPriorityCalculator(DefaultPriorityWeights),
		summarizer:   summarizer,
		state:        &CompressionState{},  // 初始化空状态
		stateMu:      sync.Mutex{},
	}, nil
}

// Compress 执行压缩
func (e *Engine) Compress(ctx context.Context, messages []llm.Message) (*CompressResult, error) {
	if len(messages) == 0 {
		return &CompressResult{
			CompressedMessages: messages,
			OriginalTokens:     0,
			CompressedTokens:   0,
		}, nil
	}

	// 计算原始token数
	originalTokens, err := e.CountTokens(messages)
	if err != nil {
		return nil, fmt.Errorf("failed to count tokens: %w", err)
	}

	// 未超限直接返回
	if originalTokens <= e.config.MaxContextTokens {
		return &CompressResult{
			CompressedMessages: messages,
			OriginalTokens:     originalTokens,
			CompressedTokens:   originalTokens,
			CompressionRatio:   1.0,
			CompressionStats:   "No compression needed",
		}, nil
	}

	// 如果没有摘要器，返回原始消息
	if e.summarizer == nil {
		slog.Warn("Context compression triggered but no summarizer available")
		return &CompressResult{
			CompressedMessages: messages,
			OriginalTokens:     originalTokens,
			CompressedTokens:   originalTokens,
			CompressionRatio:   1.0,
			CompressionStats:   "No summarizer available",
		}, nil
	}

	// 计算优先级
	priorities := e.priorityCalc.CalculatePriorities(ctx, messages, e.config.KeepRecentRounds)

	slog.Info("Context compression triggered",
		"original_tokens", originalTokens,
		"max_tokens", e.config.MaxContextTokens)

	// 执行LLM摘要压缩
	compressedMessages, err := e.summarizer.Summarize(ctx, messages, priorities)
	if err != nil {
		slog.Error("LLM summarization failed", "error", err)
		return nil, fmt.Errorf("summarization failed: %w", err)
	}

	// 计算压缩后token数
	compressedTokens, err := e.CountTokens(compressedMessages)
	if err != nil {
		compressedTokens = len(compressedMessages) // 降级估算
	}

	compressionRatio := float64(compressedTokens) / float64(originalTokens)

	stats := fmt.Sprintf("LLM summarization applied | Original: %d tokens | Compressed: %d tokens | Ratio: %.2f",
		originalTokens, compressedTokens, compressionRatio)

	return &CompressResult{
		CompressedMessages: compressedMessages,
		OriginalTokens:     originalTokens,
		CompressedTokens:   compressedTokens,
		CompressionRatio:   compressionRatio,
		CompressionStats:   stats,
	}, nil
}

// CountTokens 计算messages的总token数
func (e *Engine) CountTokens(messages []llm.Message) (int, error) {
	var total int
	for _, msg := range messages {
		tokens, err := e.tokenizer.CountTokens(msg.Content)
		if err != nil {
			return 0, err
		}
		total += tokens
	}
	return total, nil
}

// GetPriorities 获取优先级分数
func (e *Engine) GetPriorities(messages []llm.Message) map[int]float64 {
	return e.priorityCalc.GetPriorities(messages, e.config.KeepRecentRounds)
}

// CompressIncremental 增量压缩（核心新增方法）
// 只处理上次压缩后新增的消息，与已有摘要栈合并
// 参数：
//   - ctx: 上下文
//   - messages: 当前全量消息列表
//   - state: 当前压缩状态（如果为 nil 或空则执行全量压缩）
// 返回：
//   - 压缩后的消息列表
//   - 更新后的压缩状态
//   - 错误
func (e *Engine) CompressIncremental(
	ctx context.Context,
	messages []llm.Message,
	state *CompressionState,
) (*CompressResult, *CompressionState, error) {
	if len(messages) == 0 {
		return &CompressResult{
			CompressedMessages: messages,
		}, state, nil
	}

	// 计算原始 token 数
	originalTokens, err := e.CountTokens(messages)
	if err != nil {
		return nil, state, fmt.Errorf("failed to count tokens: %w", err)
	}

	// 未超限直接返回
	if originalTokens <= e.config.MaxContextTokens {
		return &CompressResult{
			CompressedMessages: messages,
			OriginalTokens:     originalTokens,
			CompressedTokens:   originalTokens,
			CompressionRatio:   1.0,
			CompressionStats:   "No compression needed",
		}, state, nil
	}

	// 如果没有摘要器，返回原始消息（降级）
	if e.summarizer == nil {
		slog.Warn("Incremental compression triggered but no summarizer available")
		return &CompressResult{
			CompressedMessages: messages,
			OriginalTokens:     originalTokens,
			CompressedTokens:   originalTokens,
			CompressionRatio:   1.0,
			CompressionStats:   "No summarizer available",
		}, state, nil
	}

	// 判断是首次全量压缩还是增量压缩
	isFirst := state == nil || state.IsEmpty()

	var compressedMessages []llm.Message
	var newState *CompressionState

	if isFirst {
		// 首次压缩：执行全量压缩
		slog.Info("First compression (full)", "original_tokens", originalTokens)

		result, err := e.fullCompress(ctx, messages)
		if err != nil {
			return nil, state, err
		}

		// 创建新状态
		newState = &CompressionState{
			LastCompressedIndex: len(messages),
		}
		// 构建初始摘要栈
		newState.AppendSummary(SummaryBlock{
			StartIndex:       0,
			EndIndex:         len(messages),
			Summary:          extractSummaryFromResult(result),
			TokenCount:       result.CompressedTokens,
			CompressionLevel: 1,
		})

		compressedMessages = result.CompressedMessages
		return &CompressResult{
			CompressedMessages: compressedMessages,
			OriginalTokens:     result.OriginalTokens,
			CompressedTokens:   result.CompressedTokens,
			CompressionRatio:   result.CompressionRatio,
			CompressionStats:   result.CompressionStats,
		}, newState, nil
	}

	// 增量压缩：只处理新增消息
	slog.Info("Incremental compression triggered",
		"original_tokens", originalTokens,
		"last_compressed_index", state.LastCompressedIndex,
		"current_messages", len(messages))

	result, err := e.incrementalCompress(ctx, messages, state)
	if err != nil {
		return nil, state, err
	}

	return &result.CompressResult, result.newState, nil
}

// fullCompress 全量压缩（复用现有逻辑）
func (e *Engine) fullCompress(ctx context.Context, messages []llm.Message) (*CompressResult, error) {
	return e.Compress(ctx, messages)
}

// incrementalResult 增量压缩结果（内部类型）
type incrementalResult struct {
	CompressResult
	newState *CompressionState
}

// incrementalCompress 增量压缩核心逻辑
func (e *Engine) incrementalCompress(
	ctx context.Context,
	messages []llm.Message,
	state *CompressionState,
) (*incrementalResult, error) {

	// 1. 提取 System 消息
	systemMsg := ExtractSystemMessage(messages)

	// 2. 确定增量范围：上次压缩点 → 当前消息末尾
	startIdx := state.LastCompressedIndex
	if startIdx >= len(messages) {
		// 没有新消息，只做摘要栈整合
		return e.consolidateSummaryStack(ctx, messages, state)
	}

	// 3. 提取保留区消息（最近几轮的高优先级消息）
	recentMsgs, compressibleMsgs := ExtractRecentMessages(messages, e.config.KeepRecentRounds)

	// 4. 如果待压缩消息为空，只做整合
	if len(compressibleMsgs) == 0 {
		return e.consolidateSummaryStack(ctx, messages, state)
	}

	// 5. 从 compressibleMsgs 中过滤出真正新增的（从 startIdx 之后）
	var newMsgs []llm.Message
	// 由于 ExtractRecentMessages 返回的是切片（非原始索引），
	// 简化方式：直接使用 startIdx 后的所有非保留消息
	for i := startIdx; i < len(messages)-len(recentMsgs); i++ {
		if i >= 0 && i < len(messages) {
			msg := messages[i]
			if msg.Role != llm.RoleSystem && !IsSummaryMessage(&msg) {
				newMsgs = append(newMsgs, msg)
			}
		}
	}

	if len(newMsgs) == 0 {
		return e.consolidateSummaryStack(ctx, messages, state)
	}

	// 6. 对增量消息做摘要
	summary, tokenCount, err := e.summarizer.SummarizeSegment(ctx, newMsgs)
	if err != nil {
		slog.Warn("Incremental summarization failed, falling back to full", "error", err)
		// 降级到全量压缩
		result, err := e.Compress(ctx, messages)
		if err != nil {
			return nil, fmt.Errorf("fallback full compression also failed: %w", err)
		}
		return &incrementalResult{
			CompressResult: *result,
			newState:       state,
		}, nil
	}

	if summary == "" {
		// 没有生成摘要，直接整合现有摘要栈
		return e.consolidateSummaryStack(ctx, messages, state)
	}

	// 7. 追加新摘要块到摘要栈
	state.AppendSummary(SummaryBlock{
		StartIndex:       startIdx,
		EndIndex:         len(messages) - len(recentMsgs),
		Summary:          summary,
		TokenCount:       tokenCount,
		CompressionLevel: 1,
	})

	// 8. 如果摘要栈过深，合并底层摘要
	if len(state.SummaryStack) > e.config.SummaryStackMaxDepth {
		e.mergeDeepSummaries(ctx, state)
	}

	// 9. 更新压缩索引
	state.LastCompressedIndex = len(messages) - len(recentMsgs)

	// 10. 构建缓存友好的消息布局
	constraints := state.ConstraintsBlock
	if constraints == "" {
		// 首次提取约束
		constraints = FormatConstraintsFromMessages(messages)
		state.ConstraintsBlock = constraints
	}

	compressedMessages := BuildCacheAwareMessages(
		systemMsg,
		constraints,
		state.SummaryStack,
		recentMsgs,
	)

	// 11. 计算压缩统计
	compressedTokens, _ := e.CountTokens(compressedMessages)

	return &incrementalResult{
		CompressResult: CompressResult{
			CompressedMessages: compressedMessages,
			OriginalTokens:     len(messages),
			CompressedTokens:   compressedTokens,
			CompressionRatio:   float64(compressedTokens) / float64(len(messages)),
			CompressionStats:   fmt.Sprintf("Incremental compression | New messages: %d | Summary stack: %d blocks | Tokens: %d",
				len(newMsgs), len(state.SummaryStack), compressedTokens),
		},
		newState: state,
	}, nil
}

// consolidateSummaryStack 整合摘要栈（当没有新消息时压缩摘要栈深度）
func (e *Engine) consolidateSummaryStack(
	ctx context.Context,
	messages []llm.Message,
	state *CompressionState,
) (*incrementalResult, error) {
	systemMsg := ExtractSystemMessage(messages)
	recentMsgs, _ := ExtractRecentMessages(messages, e.config.KeepRecentRounds)

	constraints := state.ConstraintsBlock

	compressedMessages := BuildCacheAwareMessages(
		systemMsg,
		constraints,
		state.SummaryStack,
		recentMsgs,
	)

	compressedTokens, _ := e.CountTokens(compressedMessages)

	return &incrementalResult{
		CompressResult: CompressResult{
			CompressedMessages: compressedMessages,
			OriginalTokens:     len(messages),
			CompressedTokens:   compressedTokens,
			CompressionRatio:   float64(compressedTokens) / float64(len(messages)),
			CompressionStats:   fmt.Sprintf("Summary stack consolidated | Stack depth: %d | Tokens: %d",
				len(state.SummaryStack), compressedTokens),
		},
		newState: state,
	}, nil
}

// mergeDeepSummaries 合并底层摘要（当栈过深时）
func (e *Engine) mergeDeepSummaries(ctx context.Context, state *CompressionState) {
	if len(state.SummaryStack) < 2 || e.summarizer == nil {
		return
	}

	// 合并最底部的两个摘要块（最旧的）
	a := state.SummaryStack[0]
	b := state.SummaryStack[1]

	merged, tokenCount, err := e.summarizer.MergeTwoSummaries(ctx, a.Summary, b.Summary)
	if err != nil {
		slog.Warn("Failed to merge summaries", "error", err)
		return
	}

	state.SummaryStack = append(
		[]SummaryBlock{{
			StartIndex:       a.StartIndex,
			EndIndex:         b.EndIndex,
			Summary:          merged,
			TokenCount:       tokenCount,
			CompressionLevel: 2,
		}},
		state.SummaryStack[2:]...,
	)

	slog.Info("Summary stack merged",
		"previous_depth", len(state.SummaryStack)+1,
		"new_depth", len(state.SummaryStack))
}

// extractSummaryFromResult 从压缩结果中提取摘要文本
func extractSummaryFromResult(result *CompressResult) string {
	for _, msg := range result.CompressedMessages {
		if strings.HasPrefix(msg.Content, "[CONTEXT SUMMARY]") {
			return msg.Content
		}
	}
	return ""
}
