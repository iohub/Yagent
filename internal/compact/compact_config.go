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
//
// Deprecated: 此函数已标记为废弃。新的代码应使用 ConfigFromFull()，它在内部调用
// applySafeDefaults() 确保所有异步/增量压缩字段都有安全默认值。ConfigFrom() 内部
// 也已调用 applySafeDefaults()，因此行为与 ConfigFromFull() 一致。保留此函数仅
// 为向后兼容现有调用方。
func ConfigFrom(maxTokens int, enableAuto bool, model string, summarizationProvider string,
	timeoutSec, summaryMaxInputTokens int, summaryPrompt string, keepRecentRounds int) *Config {
	return ConfigFromFull(maxTokens, enableAuto, model, summarizationProvider,
		timeoutSec, summaryMaxInputTokens, summaryPrompt, keepRecentRounds)
}

// ConfigFromFull 从外部配置结构创建 compact.Config（完整参数版）
// 与 ConfigFrom 参数相同，但内部调用 applySafeDefaults() 确保异步/增量字段非零值。
//
// 这是当前推荐的构造函数。所有零值字段都会被安全默认值覆盖：
//   - AsyncCompactEnabled → true
//   - CompactTriggerThreshold → 0.8
//   - MaxConcurrentSummaries → 3
//   - CompactWorkerInterval → 30s
//   - CompactionRetryAttempts → 2
//   - SummaryStackMaxDepth → 5
//   - SummarizationTimeout → 120s
//   - SummarizationMaxInputTokens → 120000
//   - KeepRecentRounds → 2
//   - MaxContextTokens → 198000
func ConfigFromFull(maxTokens int, enableAuto bool, model string, summarizationProvider string,
	timeoutSec, summaryMaxInputTokens int, summaryPrompt string, keepRecentRounds int) *Config {
	cfg := &Config{
		MaxContextTokens:            maxTokens,
		EnableAutoCompact:           enableAuto,
		SummarizationModel:          model,
		SummarizationProvider:       summarizationProvider,
		SummarizationTimeout:        time.Duration(timeoutSec) * time.Second,
		SummarizationMaxInputTokens: summaryMaxInputTokens,
		SummarizationPrompt:         summaryPrompt,
		KeepRecentRounds:            keepRecentRounds,
		// 异步/增量压缩配置：这些字段在 ConfigFrom 时代码中被遗漏，
		// 现在由 applySafeDefaults() 兜底填充安全默认值。
	}
	cfg.applySafeDefaults()
	return cfg
}

// ConfigFromV2 从外部配置结构创建 compact.Config（支持异步/增量压缩参数）
// 用于打破 config -> compact -> llm -> config 的循环依赖
// 所有传入参数直接使用，零值仍会由 applySafeDefaults() 兜底
func ConfigFromV2(maxTokens int, enableAuto bool, model string, summarizationProvider string,
	timeoutSec, summaryMaxInputTokens int, summaryPrompt string, keepRecentRounds int,
	asyncEnabled bool, triggerThreshold float64, maxConcurrentSummaries int,
	retryAttempts int, summaryStackDepth int, workerInterval time.Duration) *Config {
	cfg := &Config{
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
	// 即使传入完整参数，仍调用 applySafeDefaults() 作为最终兜底
	// 防止传入的零值被直接使用
	cfg.applySafeDefaults()
	return cfg
}

// applySafeDefaults 对运行时零值做兜底填充。
//
// 这是三层防御的第三层（最底层）：即使上层配置层（config.go）和应用层（app.go）
// 都未能正确设置异步/增量压缩字段，本方法也会确保关键参数具有非零安全值。
//
// 所有调用者（ConfigFrom, ConfigFromFull, ConfigFromV2）在创建 Config 后
// 都应调用此方法。
func (c *Config) applySafeDefaults() {
	// MaxContextTokens: 0 表示"未设置"，使用默认值
	if c.MaxContextTokens <= 0 {
		c.MaxContextTokens = 198000
	}

	// EnableAutoCompact: 默认必须为 true（压缩机制开启）
	if !c.EnableAutoCompact {
		c.EnableAutoCompact = true
	}

	// SummarizationTimeout: 0 表示"未设置"，使用默认值
	if c.SummarizationTimeout <= 0 {
		c.SummarizationTimeout = 120 * time.Second
	}

	// SummarizationMaxInputTokens: 0 表示"未设置"，使用默认值
	if c.SummarizationMaxInputTokens <= 0 {
		c.SummarizationMaxInputTokens = 120000
	}

	// KeepRecentRounds: 0 表示"未设置"，使用默认值
	if c.KeepRecentRounds <= 0 {
		c.KeepRecentRounds = 2
	}

	// === 异步/增量压缩字段兜底 ===

	// AsyncCompactEnabled: 默认启用异步压缩
	if !c.AsyncCompactEnabled {
		c.AsyncCompactEnabled = true
	}

	// CompactTriggerThreshold: 0 表示"未设置"，使用默认值 0.8 (80%)
	if c.CompactTriggerThreshold <= 0 {
		c.CompactTriggerThreshold = 0.8
	}

	// MaxConcurrentSummaries: 0 表示"未设置"，使用默认值
	if c.MaxConcurrentSummaries <= 0 {
		c.MaxConcurrentSummaries = 3
	}

	// CompactWorkerInterval: 0 表示"未设置"，使用默认值
	if c.CompactWorkerInterval <= 0 {
		c.CompactWorkerInterval = 30 * time.Second
	}

	// CompactionRetryAttempts: 0 表示"未设置"，使用默认值
	if c.CompactionRetryAttempts <= 0 {
		c.CompactionRetryAttempts = 2
	}

	// SummaryStackMaxDepth: 0 表示"未设置"，使用默认值
	if c.SummaryStackMaxDepth <= 0 {
		c.SummaryStackMaxDepth = 5
	}
}
