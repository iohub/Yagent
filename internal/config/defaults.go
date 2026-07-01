package config

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
	}
}

// DefaultCommitLearnerConfig 返回默认的 commit 学习器配置
func DefaultCommitLearnerConfig() *CommitLearnerConfig {
	return &CommitLearnerConfig{
		Enabled:               true,
		MaxCommits:            50,
		SimilarityThreshold:   0.75,
		TopK:                  3,
		Trigger:               "both",
		CacheTTL:              3600,
		SummarizationProvider: "",
		LLMSystemPrompt:       "",
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
		Timeout:                 0,
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
		AgentBranchPrefix:      "agent/coding",
		CheckpointTagPrefix:    "checkpoint/coding",
		StashDirtyWorktree:     true,
		CleanupAgentBranch:     true,
		CleanupCheckpointTags:  true,
	}
}