package config

import "time"

// DefaultMaxSteps 定义各 agent 的默认最大步数
// 来源说明: default_config.toml 中的值作为"推荐值"，此处为"程序保证值"
// 两者应保持一致。如果用户未在 TOML 中配置，使用此处定义的默认值。
// Meta 不在此列：它是一次 LLM 调用，没有 step 循环（只有 MetaRetryCount 有意义）。
var DefaultMaxSteps = struct {
	Director  int
	Coding    int
	Repo      int
	Chat      int
	DevOps    int
	Browser   int
	MetaRetry int
}{
	Director:  100,
	Coding:    150,
	Repo:      50,
	Chat:      50,
	DevOps:    50,
	Browser:   200,
	MetaRetry: 5,
}

// DefaultLLMCallTimeout 是 LLM API 调用超时的程序保证默认值（用户未配置时的兜底）。
// 运行期真实值以 GlobalLLMTimeout()（llm_timeout.go）为准：TUI 模型设置面板
// 可动态修改全局值；无全局值时各调用方回落到本常量。
const DefaultLLMCallTimeout = 10 * time.Minute

// DefaultMaxInFlightRequests 是同时在飞的 provider 调用上限的程序保证默认值。
// 只在 [llm] max_in_flight_requests 没写时生效；显式写 0 = 不限。
const DefaultMaxInFlightRequests = 8

// DefaultLLMConfig 返回默认的 LLM 推理兜底配置
func DefaultLLMConfig() *LLMConfig {
	return &LLMConfig{
		Timeout:                    DefaultLLMCallTimeout,
		MaxRetries:                 5,
		StepRetries:                0,
		CircuitBreakerThreshold:    0,
		CircuitBreakerResetTimeout: 0,
		EnableFallback:             false,
		FallbackMaxRetries:         2,
		MaxInFlightRequests:        DefaultMaxInFlightRequests,
	}
}

// DefaultGitCheckpointConfig 返回默认的 git checkpoint 配置
func DefaultGitCheckpointConfig() GitCheckpointConfig {
	return GitCheckpointConfig{
		Enabled:               true,
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
