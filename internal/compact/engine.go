package compact

import (
	"context"
	"fmt"
	"log/slog"
	"codeactor/internal/llm"
)

// Engine 压缩引擎
type Engine struct {
	config     *Config
	tokenizer  Tokenizer
	priorityCalc *PriorityCalculator
	summarizer *LLMSummarizer
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
