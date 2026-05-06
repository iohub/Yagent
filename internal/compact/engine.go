package compact

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"codeactor/internal/llm"
)

// Engine 压缩引擎
type Engine struct {
	config       *Config
	tokenizer    Tokenizer
	priorityCalc *PriorityCalculator
	ruleComp     *RuleCompressor
}

// NewEngine 创建压缩引擎
func NewEngine(config *Config) (*Engine, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid compact config: %w", err)
	}

	return &Engine{
		config:       config,
		tokenizer:    GetGlobalTokenizer(),
		priorityCalc: NewPriorityCalculator(DefaultPriorityWeights),
		ruleComp:     NewRuleCompressor(config),
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
			StrategyUsed:       e.config.Strategy.String(),
			CompressionStats:   "No compression needed",
		}, nil
	}

	// 计算优先级
	priorities := e.priorityCalc.CalculatePriorities(ctx, messages, e.config)

	// 按优先级排序（升序：低分优先被压缩）
	sorted := make([]MessagePriority, len(priorities))
	copy(sorted, priorities)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].Score < sorted[j].Score
	})

	slog.Info("Context compression triggered",
		"original_tokens", originalTokens,
		"max_tokens", e.config.MaxContextTokens,
		"strategy", e.config.Strategy.String())

	// 执行多级压缩
	var currentMessages []llm.Message
	stats := []string{fmt.Sprintf("Strategy: %s", e.config.Strategy.String())}

	switch e.config.Strategy {
	case StrategyConservative:
		currentMessages, stats = e.compressConservative(messages, priorities, originalTokens, stats)
	case StrategyBalanced:
		currentMessages, stats = e.compressBalanced(messages, priorities, originalTokens, stats)
	case StrategyAggressive:
		currentMessages, stats = e.compressAggressive(messages, priorities, originalTokens, stats)
	}

	// 最终校验
	compressedTokens, err := e.CountTokens(currentMessages)
	if err != nil {
		compressedTokens = len(currentMessages) // 降级估算
	}

	stats = append(stats, fmt.Sprintf("Final tokens: %d", compressedTokens))

	return &CompressResult{
		CompressedMessages: currentMessages,
		OriginalTokens:     originalTokens,
		CompressedTokens:   compressedTokens,
		CompressionRatio:   float64(compressedTokens) / float64(originalTokens),
		StrategyUsed:       e.config.Strategy.String(),
		CompressionStats:   strings.Join(stats, " | "),
	}, nil
}

// compressConservative 保守策略
func (e *Engine) compressConservative(
	messages []llm.Message,
	priorities []MessagePriority,
	originalTokens int,
	stats []string,
) ([]llm.Message, []string) {
	current := messages

	// 只执行L2截断
	if originalTokens > e.config.L2Threshold {
		current = e.ruleComp.L2Compress(current)
		stats = append(stats, "L2: Tool output truncated")

		// 检查压缩后是否达标
		tokens, _ := e.CountTokens(current)
		if tokens > e.config.MaxContextTokens {
			// 仍超限，强制L3
			current = e.ruleComp.L3Compress(current, e.config.KeepRecentRounds)
			stats = append(stats, "L3: Early context dropped")
		}
	}

	return current, stats
}

// compressBalanced 平衡策略（默认）
func (e *Engine) compressBalanced(
	messages []llm.Message,
	priorities []MessagePriority,
	originalTokens int,
	stats []string,
) ([]llm.Message, []string) {
	current := messages

	// L1: 尝试摘要压缩
	if originalTokens > e.config.L1Threshold {
		// TODO: 当有SummarizationClient时调用
		stats = append(stats, "L1: Skipped (no summarization client)")
	}

	// L2: 规则截断
	tokens, _ := e.CountTokens(current)
	if tokens > e.config.L2Threshold {
		current = e.ruleComp.L2Compress(current)
		stats = append(stats, "L2: Tool output truncated")

		tokens, _ = e.CountTokens(current)
		if tokens > e.config.MaxContextTokens {
			// L3: 丢弃早期
			current = e.ruleComp.L3Compress(current, e.config.KeepRecentRounds)
			stats = append(stats, "L3: Early context dropped")
		}
	}

	return current, stats
}

// compressAggressive 激进策略
func (e *Engine) compressAggressive(
	messages []llm.Message,
	priorities []MessagePriority,
	originalTokens int,
	stats []string,
) ([]llm.Message, []string) {
	current := messages

	// L1: 尝试摘要
	if originalTokens > e.config.L1Threshold {
		stats = append(stats, "L1: Skipped (no summarization client)")
	}

	// L2: 截断
	tokens, _ := e.CountTokens(current)
	if tokens > e.config.L2Threshold {
		current = e.ruleComp.L2Compress(current)
		stats = append(stats, "L2: Tool output truncated")
	}

	// L3: 丢弃早期
	tokens, _ = e.CountTokens(current)
	if tokens > e.config.L3Threshold {
		current = e.ruleComp.L3Compress(current, e.config.KeepRecentRounds)
		stats = append(stats, "L3: Early context dropped")
	}

	// 最终兜底：如果仍超限，强制保留最近8轮
	tokens, _ = e.CountTokens(current)
	if tokens > e.config.MaxContextTokens {
		current = e.ruleComp.L3Compress(current, 8)
		stats = append(stats, "L3: Force keep recent 8 rounds")
	}

	return current, stats
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

// GetPriorityScores 获取优先级分数
func (e *Engine) GetPriorityScores(messages []llm.Message) map[int]float64 {
	return e.priorityCalc.GetScores(messages, e.config)
}
