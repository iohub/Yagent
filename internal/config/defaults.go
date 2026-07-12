package config

import "time"

// DefaultMaxSteps 定义各 agent 的默认最大步数
// 来源说明: default_config.toml 中的值作为"推荐值"，此处为"程序保证值"
// 两者应保持一致。如果用户未在 TOML 中配置，使用此处定义的默认值。
var DefaultMaxSteps = struct {
	Director int
	Coding    int
	Repo      int
	Chat      int
	DevOps    int
	Browser   int
	Meta      int
	MetaRetry int
}{
	Director: 100,
	Coding:    150,
	Repo:      50,
	Chat:      50,
	DevOps:    50,
	Browser:   200,
	Meta:      50,
	MetaRetry: 5,
}

// DefaultCompactConfig 返回默认的上下文压缩配置
// 作为 validate() 中设置默认值的来源，消除死代码 compact.DefaultConfig
func DefaultCompactConfig() *ContextCompactConfig {
	return &ContextCompactConfig{
		MaxContextTokens:            198000,
		EnableAutoCompact:           true,
		SummarizationModel:          "",
		SummarizationProvider:       "",
		SummarizationTimeout:        15,
		SummarizationMaxInputTokens: 8000,
		SummarizationPrompt:         "",
		KeepRecentRounds:            3,

		// Layer 1: Tool Result Budget Control
		MaxToolOutputTokens: 8192,
		ToolPreviewTokens:   512,
		OffloadEnabled:      true,
		OffloadPath:         ".compact-offload",

		// Layer 2: Old Message Pruning
		MinPrunableAge: 5,

		// Layer 3: Micro-compression
		MicroCompressEnabled: true,
		MicroCompressTools:   []string{"run_bash", "read_file", "list_files", "grep", "search"},

		// Layer 4: Context Folding
		FoldEnabled:      true,
		FoldBatchSize:    10,
		FoldStageTimeout: 30,

		// Layer 5: Dynamic Threshold
		SummaryReservedTokens: 20000,
		BufferBandTokens:      13000,
		CompressionDirection:  "auto",

		// Layer 6: State Compensation
		CompensateEnabled: true,

		// Layer 7: Emergency
		EmergencyMaxRetries:         2,
		CircuitBreakerThreshold:     3,
		CircuitBreakerResetDuration: 300,
	}
}

// DefaultBrowserConfig 返回默认的浏览器配置（TOML 层）
// 注意：此处的默认值与 internal/browser/config.go 中的 DefaultBrowserConfig() 应保持一致
func DefaultBrowserConfig() *BrowserConfig {
	return &BrowserConfig{
		Headless:           true,
		BrowserPath:        "",
		UserDataDir:        "",
		ViewportWidth:      1280,
		ViewportHeight:     720,
		AllowedDomains:     nil,
		BlockedDomains:     nil,
		TimeoutSeconds:     120,
		TaskTimeoutSeconds: 300,
		MaxConcurrentPages: 4,
		AutoLaunch:         true,
		IdleTimeout:        "5m",
		AllowNoSandbox:     false,
		ExtraArgs:          nil,
		EnableBrowserAgent: true,
	}
}

// DefaultLLMConfig 返回默认的 LLM 推理兜底配置
func DefaultLLMConfig() *LLMConfig {
	return &LLMConfig{
		Timeout:                 3 * time.Minute,
		MaxRetries:              5,
		StepRetries:             0,
		CircuitBreakerThreshold:           0,
		CircuitBreakerResetTimeout: 0,
	}
}

// DefaultKeywordsConfig 返回默认的关键词词典配置
func DefaultKeywordsConfig() *KeywordsConfig {
	return &KeywordsConfig{
		DefaultPath:       "",
		HotReload:         false,
		DisableCompletion: false,
		Dicts:             nil,
	}
}

// DefaultGitCheckpointConfig 返回默认的 git checkpoint 配置
func DefaultGitCheckpointConfig() GitCheckpointConfig {
	return GitCheckpointConfig{
		Enabled:                true,
		AutoCheckpoint:         true,
		CheckpointInterval:     1,
		MaxCheckpoints:         50,
		SquashOnExit:           true,
		GenerateCommitMessage:  true,
		AgentBranchPrefix:      "agent",
		CheckpointTagPrefix:    "checkpoint/coding",
		StashDirtyWorktree:     true,
		CleanupAgentBranch:     true,
		CleanupCheckpointTags:  true,
		AutoMergeOnExit:        false,
	}
}