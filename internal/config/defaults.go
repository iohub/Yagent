package config

import "time"

// DefaultMaxSteps 定义各 agent 的默认最大步数
// 来源说明: default_config.toml 中的值作为"推荐值"，此处为"程序保证值"
// 两者应保持一致。如果用户未在 TOML 中配置，使用此处定义的默认值。
var DefaultMaxSteps = struct {
	Director  int
	Coding    int
	Repo      int
	Chat      int
	DevOps    int
	Browser   int
	Meta      int
	MetaRetry int
}{
	Director:  100,
	Coding:    150,
	Repo:      50,
	Chat:      50,
	DevOps:    50,
	Browser:   200,
	Meta:      50,
	MetaRetry: 5,
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

// DefaultLLMCallTimeout 是 LLM API 调用超时的程序保证默认值（用户未配置时的兜底）。
// 运行期真实值以 GlobalLLMTimeout()（llm_timeout.go）为准：TUI 模型设置面板
// 可动态修改全局值；无全局值时各调用方回落到本常量。
const DefaultLLMCallTimeout = 10 * time.Minute

// DefaultLLMConfig 返回默认的 LLM 推理兜底配置
func DefaultLLMConfig() *LLMConfig {
	return &LLMConfig{
		Timeout: DefaultLLMCallTimeout,
		MaxRetries:                 5,
		StepRetries:                0,
		CircuitBreakerThreshold:    0,
		CircuitBreakerResetTimeout: 0,
		EnableFallback:             false,
		FallbackMaxRetries:         2,
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
		Enabled:               true,
		AutoCheckpoint:        true,
		CheckpointInterval:    1,
		MaxCheckpoints:        50,
		SquashOnExit:          true,
		GenerateCommitMessage: true,
		AgentBranchPrefix:     "agent",
		CheckpointTagPrefix:   "checkpoint/coding",
		StashDirtyWorktree:    true,
		CleanupAgentBranch:    true,
		CleanupCheckpointTags: true,
		AutoMergeOnExit:       false,
	}
}

// DefaultKnowledgeConfig 返回默认的知识管理配置
func DefaultKnowledgeConfig() KnowledgeConfig {
	return KnowledgeConfig{
		Enabled:             true,
		InjectionMaxTokens:  2000,
		InjectionMaxEntries: 8,
		InjectionMinScore:   0.3,
		InjectionRerank:     true,
	}
}

// DefaultDelegateIdleTimeoutFloor delegate 空闲超时派生值的下限。
const DefaultDelegateIdleTimeoutFloor = 10 * time.Minute

// DefaultDelegateIdleTimeoutLLMBuffer 在单步 LLM 推理最长无心跳时间之上额外预留的缓冲。
const DefaultDelegateIdleTimeoutLLMBuffer = 5 * time.Minute

// DefaultDelegateIdleTimeoutLLMFallback delegate 空闲超时派生中 llmTimeout ≤0 时的兜底值
// （保留 5 分钟，属 delegate 语义，独立于 LLM 调用超时默认值 DefaultLLMCallTimeout）。
const DefaultDelegateIdleTimeoutLLMFallback = 5 * time.Minute

// DeriveDelegateIdleTimeout 派生 delegate 空闲超时默认值。
// 规则：max(10 分钟, llmTimeout + 5 分钟)。llmTimeout ≤0 时按
// DefaultDelegateIdleTimeoutLLMFallback（5 分钟）兜底。
// 语义：空闲阈值必须显著大于子代理单步最长无心跳时间（单次 LLM 推理最长
// llmTimeout），否则正常长推理会被误杀。
// 注意：输入 llmTimeout 的取值来源（配置/全局运行时值）变化属预期，派生逻辑保持不变。
func DeriveDelegateIdleTimeout(llmTimeout time.Duration) time.Duration {
	if llmTimeout <= 0 {
		llmTimeout = DefaultDelegateIdleTimeoutLLMFallback
	}
	derived := llmTimeout + DefaultDelegateIdleTimeoutLLMBuffer
	if derived < DefaultDelegateIdleTimeoutFloor {
		derived = DefaultDelegateIdleTimeoutFloor
	}
	return derived
}
