package compact

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

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
		state:        NewEmptyCompressionState(), // 初始化空状态（v2）
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

		// 创建新状态（v2：使用 AnchorSet）
		newState = NewCompressionStateWithMessages("", len(messages))
		// 构建初始摘要栈
		newState.AppendSummary(SummaryBlock{
			Summary:          extractSummaryFromResult(result),
			TokenCount:       result.CompressedTokens,
			CompressionLevel: 1,
			SourceRange:      AnchorRange{StartIndex: 0, EndIndex: len(messages)},
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
		"total_messages", len(messages),
		"unsummarized_count", state.Anchors.UnsummarizedCount())

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

// incrementalCompress 增量压缩核心逻辑（v3：AnchorSet + ExtractRecentMessagesV2 + 锁修复）
func (e *Engine) incrementalCompress(
	ctx context.Context,
	messages []llm.Message,
	state *CompressionState,
) (*incrementalResult, error) {
	// 锁定状态：增量压缩是独占操作
	state.Lock()
	defer state.Unlock()

	// 1. 提取 System 消息
	systemMsg := ExtractSystemMessage(messages)

	// 2. 通过 AnchorSet 确定下一个未摘要区间
	if state.Anchors == nil || state.Anchors.IsEmpty() {
		// 没有锚点，初始化
		state.Anchors = NewAnchorSet(len(messages))
	}

	startIdx, endIdx, hasUnsummarized := state.Anchors.NextUnsummarizedRange(0)
	if !hasUnsummarized {
		// 所有消息都已摘要，只做整合
		return e.consolidateSummaryStack(state, messages)
	}

	// 3. 使用 ExtractRecentMessagesV2 确定保留区和可压缩区间
	extractResult := ExtractRecentMessagesV2(messages, e.config.KeepRecentRounds)

	// 4. 确定实际可压缩范围：AnchorSet 未摘要区间 ∩ 可压缩区间
	compressStart := startIdx
	if compressStart < extractResult.CompressibleStartIndex {
		compressStart = extractResult.CompressibleStartIndex
	}
	compressEnd := endIdx
	if compressEnd > extractResult.CompressibleEndIndex {
		compressEnd = extractResult.CompressibleEndIndex
	}

	if compressStart >= compressEnd {
		// 没有可压缩的消息，直接整合
		return e.consolidateSummaryStack(state, messages)
	}

	// 5. 提取待压缩的消息
	var newMsgs []llm.Message
	for i := compressStart; i < compressEnd; i++ {
		msg := messages[i]
		if msg.Role != llm.RoleSystem && !IsSummaryMessage(&msg) {
			newMsgs = append(newMsgs, msg)
		}
	}

	if len(newMsgs) == 0 {
		return e.consolidateSummaryStack(state, messages)
	}

	// 6. 对增量消息做摘要
	summary, tokenCount, err := e.summarizer.SummarizeSegment(ctx, newMsgs)
	if err != nil {
		slog.Warn("Incremental summarization failed, falling back to full", "error", err)
		// 降级到全量压缩
		state.Unlock() // 临时解锁避免死锁
		result, err := e.Compress(ctx, messages)
		state.Lock()
		if err != nil {
			return nil, fmt.Errorf("fallback full compression also failed: %w", err)
		}
		return &incrementalResult{
			CompressResult: *result,
			newState:       state,
		}, nil
	}

	if summary == "" {
		return e.consolidateSummaryStack(state, messages)
	}

	// 7. 追加新摘要块到摘要栈
	block := SummaryBlock{
		Summary:          summary,
		TokenCount:       tokenCount,
		CompressionLevel: 1,
		SourceRange:      AnchorRange{StartIndex: compressStart, EndIndex: compressEnd},
		CreatedAt:        time.Now(),
	}
	state.AppendSummary(block)

	// 8. 更新锚点
	state.Anchors.MarkSummarized(compressStart, compressEnd, len(state.SummaryStack)-1)

	// 9. 如果摘要栈过深，合并底层摘要（先解锁再合并，避免持有锁调用 LLM）
	if len(state.SummaryStack) > e.config.SummaryStackMaxDepth {
		state.Unlock()
		e.mergeDeepSummaries(ctx, state)
		state.Lock()
	}

	// 10. 构建缓存友好的消息布局
	constraints := state.ConstraintsBlock
	if constraints == "" && extractResult.CompressibleStartIndex > 0 {
		constraints = FormatConstraintsFromMessages(messages)
		state.ConstraintsBlock = constraints
	}

	recentMsgs := extractResult.Recent
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
			CompressionStats: fmt.Sprintf("Incremental compression | Range [%d:%d] | Summary stack: %d blocks | Tokens: %d",
				compressStart, compressEnd, len(state.SummaryStack), compressedTokens),
		},
		newState: state,
	}, nil
}

// consolidateSummaryStack 整合摘要栈（当没有新消息时构建消息布局）
// 注意：调用方应已持有 state 的读锁或写锁
func (e *Engine) consolidateSummaryStack(
	state *CompressionState,
	messages []llm.Message,
) (*incrementalResult, error) {
	systemMsg := ExtractSystemMessage(messages)
	
	// 使用 V2 获取保留区（不需要待压缩区）
	extractResult := ExtractRecentMessagesV2(messages, e.config.KeepRecentRounds)
	recentMsgs := extractResult.Recent

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
			CompressionStats: fmt.Sprintf("Summary stack consolidated | Stack depth: %d | Tokens: %d",
				len(state.SummaryStack), compressedTokens),
		},
		newState: state,
	}, nil
}

// mergeDeepSummaries 合并底层摘要（当栈过深时）
// 注意：调用方需要在传入前决定锁的状态
// - 如果调用时已持有锁，此函数不会额外加锁（避免重复加锁）
// - 如果调用时未持有锁，此函数会自己加锁
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

	// 合并后的 SourceRange 覆盖两个块的范围
	mergedRange := AnchorRange{
		StartIndex: a.SourceRange.StartIndex,
		EndIndex:   b.SourceRange.EndIndex,
	}

	newBlock := SummaryBlock{
		Summary:          merged,
		TokenCount:       tokenCount,
		CompressionLevel: 2,
		SourceRange:      mergedRange,
		CreatedAt:        time.Now(),
	}

	// 替换前两个块为合并后的块
	state.Lock()
	state.SummaryStack = append(
		[]SummaryBlock{newBlock},
		state.SummaryStack[2:]...,
	)
	state.Unlock()

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
