package compact

import (
	"time"
)

// Config 压缩配置
type Config struct {
	// MaxContextTokens 最大上下文token数，默认198000
	MaxContextTokens int `toml:"max_context_tokens"`

	// EnableAutoCompact 是否自动触发压缩
	EnableAutoCompact bool `toml:"enable_auto_compact"`

	// SummarizationModel 用于摘要的轻量模型
	SummarizationModel string `toml:"summarization_model"`

	// SummarizationProvider 摘要使用的 LLM provider 名称
	SummarizationProvider string `toml:"summarization_provider"`

	// SummarizationTimeout 摘要超时时间
	SummarizationTimeout time.Duration `toml:"summarization_timeout"`

	// SummarizationMaxInputTokens 摘要时单批次最大输入token数
	SummarizationMaxInputTokens int `toml:"summarization_max_input_tokens"`

	// SummarizationPrompt 自定义摘要提示词（可选，空则用默认）
	SummarizationPrompt string `toml:"summarization_prompt"`

	// KeepRecentRounds 始终保留的最近对话轮数（用于优先级计算）
	KeepRecentRounds int `toml:"keep_recent_rounds"`

	// AsyncCompactEnabled 是否启用异步压缩（默认 true）
	AsyncCompactEnabled bool `toml:"async_compact_enabled"`

	// CompactTriggerThreshold 触发压缩的 token 使用率阈值，范围 0.0~1.0
	// 当当前 token 数达到 MaxContextTokens * 该阈值时启动压缩（默认 0.75）
	CompactTriggerThreshold float64 `toml:"compact_trigger_threshold"`

	// MaxConcurrentSummaries 并发 LLM 摘要调用上限（默认 3）
	MaxConcurrentSummaries int `toml:"max_concurrent_summaries"`

	// CompactionRetryAttempts 压缩失败重试次数（默认 2）
	CompactionRetryAttempts int `toml:"compaction_retry_attempts"`

	// SummaryStackMaxDepth 摘要栈最大深度（默认 5）
	SummaryStackMaxDepth int `toml:"summary_stack_max_depth"`

	// CompactWorkerInterval 异步 worker 轮询间隔（默认 100ms）
	CompactWorkerInterval time.Duration `toml:"compact_worker_interval"`
}

// DefaultConfig 默认配置
var DefaultConfig = Config{
	MaxContextTokens:            198000, // 198k
	EnableAutoCompact:           true,
	SummarizationModel:          "gpt-3.5-turbo", // 或claude-3-haiku
	SummarizationTimeout:        15 * time.Second,
	SummarizationMaxInputTokens: 8000,  // 单批次最大输入
	KeepRecentRounds:            3, // 保留最近3轮完整对话
	// 异步/增量压缩配置
	AsyncCompactEnabled:         true,
	CompactTriggerThreshold:     0.75,
	MaxConcurrentSummaries:      3,
	CompactionRetryAttempts:     2,
	SummaryStackMaxDepth:        5,
	CompactWorkerInterval:       100 * time.Millisecond,
}

func (c *Config) Validate() error {
	if c.MaxContextTokens <= 0 {
		return nil // 允许0值，表示使用默认
	}
	return nil
}

// ConfigFrom 从外部配置结构创建 compact.Config
// 用于打破 config -> compact -> llm -> config 的循环依赖
func ConfigFrom(maxTokens int, enableAuto bool, model string, summarizationProvider string,
	timeoutSec, summaryMaxInputTokens int, summaryPrompt string, keepRecentRounds int) *Config {
	return &Config{
		MaxContextTokens:            maxTokens,
		EnableAutoCompact:           enableAuto,
		SummarizationModel:          model,
		SummarizationProvider:       summarizationProvider,
		SummarizationTimeout:        time.Duration(timeoutSec) * time.Second,
		SummarizationMaxInputTokens: summaryMaxInputTokens,
		SummarizationPrompt:         summaryPrompt,
		KeepRecentRounds:            keepRecentRounds,
	}
}

// ConfigFromV2 从外部配置结构创建 compact.Config（支持异步/增量压缩参数）
// 用于打破 config -> compact -> llm -> config 的循环依赖
func ConfigFromV2(maxTokens int, enableAuto bool, model string, summarizationProvider string,
	timeoutSec, summaryMaxInputTokens int, summaryPrompt string, keepRecentRounds int,
	asyncEnabled bool, triggerThreshold float64, maxConcurrentSummaries int,
	retryAttempts int, summaryStackDepth int, workerInterval time.Duration) *Config {
	return &Config{
		MaxContextTokens:            maxTokens,
		EnableAutoCompact:           enableAuto,
		SummarizationModel:          model,
		SummarizationProvider:       summarizationProvider,
		SummarizationTimeout:        time.Duration(timeoutSec) * time.Second,
		SummarizationMaxInputTokens: summaryMaxInputTokens,
		SummarizationPrompt:         summaryPrompt,
		KeepRecentRounds:            keepRecentRounds,
		// 异步/增量压缩配置
		AsyncCompactEnabled:       asyncEnabled,
		CompactTriggerThreshold:   triggerThreshold,
		MaxConcurrentSummaries:    maxConcurrentSummaries,
		CompactionRetryAttempts:   retryAttempts,
		SummaryStackMaxDepth:      summaryStackDepth,
		CompactWorkerInterval:     workerInterval,
	}
}
