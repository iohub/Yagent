package compact

import (
	"errors"
	"strings"
	"time"
)

// Config 压缩配置
type Config struct {
	// MaxContextTokens 最大上下文token数，默认198000
	MaxContextTokens int `toml:"max_context_tokens"`

	// Strategy 压缩策略
	Strategy Strategy `toml:"compression_strategy"`

	// EnableAutoCompact 是否自动触发压缩
	EnableAutoCompact bool `toml:"enable_auto_compact"`

	// SummarizationModel 用于L1摘要的轻量模型
	SummarizationModel string `toml:"summarization_model"`

	// L1Threshold 触发L1压缩的阈值
	L1Threshold int `toml:"l1_token_threshold"`

	// L2Threshold 触发L2压缩的阈值
	L2Threshold int `toml:"l2_token_threshold"`

	// L3Threshold 触发L3压缩的阈值
	L3Threshold int `toml:"l3_token_threshold"`

	// SummarizationTimeout L1摘要超时时间
	SummarizationTimeout time.Duration `toml:"summarization_timeout"`

	// KeepRecentRounds 始终保留的最近对话轮数
	KeepRecentRounds int `toml:"keep_recent_rounds"`

	// KeepTaskConclusions 保留已完成任务的结论数
	KeepTaskConclusions int `toml:"keep_task_conclusions"`
}

// DefaultConfig 默认配置
var DefaultConfig = Config{
	MaxContextTokens:     198000, // 198k
	Strategy:             StrategyBalanced,
	EnableAutoCompact:    true,
	SummarizationModel:   "gpt-3.5-turbo", // 或claude-3-haiku
	L1Threshold:          160000,
	L2Threshold:          130000,
	L3Threshold:          100000,
	SummarizationTimeout: 15 * time.Second,
	KeepRecentRounds:     3, // 保留最近3轮完整对话
	KeepTaskConclusions:  2, // 保留最近2个已完成任务的结论
}

func (c *Config) Validate() error {
	if c.MaxContextTokens <= 0 {
		return errors.New("max_context_tokens must be positive")
	}
	if c.L1Threshold <= c.L2Threshold || c.L2Threshold <= c.L3Threshold {
		return errors.New("thresholds must be strictly decreasing (L1 > L2 > L3)")
	}
	if c.KeepRecentRounds < 1 {
		c.KeepRecentRounds = 3
	}
	return nil
}

// ConfigFrom 从外部配置结构创建 compact.Config
// 用于打破 config -> compact -> llm -> config 的循环依赖
func ConfigFrom(maxTokens int, strategyStr string, enableAuto bool, model string,
	l1, l2, l3 int, timeoutSec, keepRounds, keepConclusions int) *Config {
	return &Config{
		MaxContextTokens:     maxTokens,
		Strategy:             parseStrategy(strategyStr),
		EnableAutoCompact:    enableAuto,
		SummarizationModel:   model,
		L1Threshold:          l1,
		L2Threshold:          l2,
		L3Threshold:          l3,
		SummarizationTimeout: time.Duration(timeoutSec) * time.Second,
		KeepRecentRounds:     keepRounds,
		KeepTaskConclusions:  keepConclusions,
	}
}

// parseStrategy 解析策略字符串
func parseStrategy(s string) Strategy {
	switch strings.ToLower(s) {
	case "conservative":
		return StrategyConservative
	case "aggressive":
		return StrategyAggressive
	default:
		return StrategyBalanced
	}
}
