package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// ProviderConfig contains configuration for a specific LLM provider
type ProviderConfig struct {
	Model       string  `toml:"model"`
	Temperature float64 `toml:"temperature"`
	MaxTokens   int     `toml:"max_tokens"`
	APIBaseURL  string  `toml:"api_base_url"`
	APIKey      string  `toml:"api_key"`
	// Bedrock-specific fields
	AWSRegion     string `toml:"aws_region,omitempty"`
	AWSProfile    string `toml:"aws_profile,omitempty"`
	ModelProvider string `toml:"model_provider,omitempty"` // Explicit provider for Bedrock (e.g., "anthropic", "amazon", "meta")

	// ReasoningEffort controls DeepSeek thinking mode intensity ("high" or "max").
	// Empty string means thinking mode is disabled (default, backward-compatible).
	ReasoningEffort string `toml:"reasoning_effort,omitempty"`
}

// AppConfig contains application-level configuration
type AppConfig struct {
	EnableStreaming bool `toml:"enable_streaming"`
}

// AgentConfig contains agent-specific configuration
type AgentConfig struct {
	DirectorMaxSteps int    `toml:"director_max_steps"`
	CodingMaxSteps    int    `toml:"coding_max_steps"`
	ChatMaxSteps      int    `toml:"chat_max_steps"`
	RepoMaxSteps      int    `toml:"repo_max_steps"`
	DevOpsMaxSteps    int    `toml:"devops_max_steps"`
	BrowserMaxSteps   int    `toml:"browser_max_steps"`
	MetaMaxSteps      int    `toml:"meta_max_steps"`
	MetaRetryCount    int    `toml:"meta_retry_count"`
	SpeakLang         string `toml:"lang"`
}

// GitCheckpointConfig holds configuration for the git checkpoint mechanism.
type GitCheckpointConfig struct {
	Enabled                bool   `toml:"enabled"`
	// Deprecated: Checkpoint creation is now LLM-driven via git_checkpoint_create tool.
	// This field is ignored but still parsed for backward compatibility.
	AutoCheckpoint bool `toml:"auto_checkpoint"`
	// Deprecated: No longer used. Checkpoint timing is determined by the agent via git_checkpoint_create.
	// This field is ignored but still parsed for backward compatibility.
	CheckpointInterval int `toml:"checkpoint_interval"`
	MaxCheckpoints         int    `toml:"max_checkpoints"`
	SquashOnExit           bool   `toml:"squash_on_exit"`
	GenerateCommitMessage  bool   `toml:"generate_commit_message"`
	AgentBranchPrefix      string `toml:"agent_branch_prefix"`
	CheckpointTagPrefix    string `toml:"checkpoint_tag_prefix"`
	StashDirtyWorktree     bool   `toml:"stash_dirty_worktree"`
	CleanupAgentBranch     bool   `toml:"cleanup_agent_branch"`
	CleanupCheckpointTags  bool   `toml:"cleanup_checkpoint_tags"`
}

// ── Three-tier LLM overrides ──

// GlobalLLMConfig is the global default LLM provider selection.
type GlobalLLMConfig struct {
	UseProvider string                    `toml:"use_provider"`
	Providers   map[string]ProviderConfig `toml:"providers"`
}

// AgentLLMOverride selects a provider for a specific agent.
type AgentLLMOverride struct {
	UseProvider string `toml:"use_provider"`
}

// AgentsLLMConfig holds per-agent LLM overrides.
// Priority: per-agent > agents.default > global.
type AgentsLLMConfig struct {
	UseProvider string            `toml:"use_provider"` // default for all agents
	Director   *AgentLLMOverride `toml:"director,omitempty"`
	Coding      *AgentLLMOverride `toml:"coding,omitempty"`
	Repo        *AgentLLMOverride `toml:"repo,omitempty"`
	Chat        *AgentLLMOverride `toml:"chat,omitempty"`
	Meta        *AgentLLMOverride `toml:"meta,omitempty"`
	DevOps      *AgentLLMOverride `toml:"devops,omitempty"`
}

// ToolLLMOverride selects a provider for a specific tool.
type ToolLLMOverride struct {
	UseProvider string `toml:"use_provider"`
}

// ToolsLLMConfig holds per-tool LLM overrides.
// Priority: per-tool > tools.default > agent > global.
type ToolsLLMConfig struct {
	UseProvider  string           `toml:"use_provider"` // default for all tools
	MicroAgent   *ToolLLMOverride `toml:"micro_agent,omitempty"`
	Thinking     *ToolLLMOverride `toml:"thinking,omitempty"`
	DeepThinking *ToolLLMOverride `toml:"deepthinking,omitempty"`
}

// TopLevelConfig groups the [global] section.
type TopLevelConfig struct {
	LLM *GlobalLLMConfig `toml:"llm"` // [global.llm]
}

// AgentsConfig wraps the agents LLM section: [agents.llm]
type AgentsConfig struct {
	LLM AgentsLLMConfig `toml:"llm"`
}

// ToolsConfig wraps the tools LLM section: [tools.llm]
type ToolsConfig struct {
	LLM ToolsLLMConfig `toml:"llm"`
}

// LLMConfig contains LLM resilience and fallback configuration
type LLMConfig struct {
	// Timeout 单次 LLM 调用超时，0=不启用
	Timeout time.Duration `toml:"timeout" json:"timeout" yaml:"timeout"`

	// MaxRetries 底层引擎重试次数（默认5，保持原行为）
	MaxRetries int `toml:"max_retries" json:"max_retries" yaml:"max_retries"`

	// StepRetries 步骤重试次数（executor/director/meta），0=不重试
	StepRetries int `toml:"step_retries" json:"step_retries" yaml:"step_retries"`

	// CircuitBreakerThreshold 熔断阈值（连续失败次数），0=不启用
	CircuitBreakerThreshold int `toml:"circuit_breaker_threshold" json:"circuit_breaker_threshold" yaml:"circuit_breaker_threshold"`

	// CircuitBreakerResetTimeout 熔断恢复时间（仅当阈值>0时有效）
	CircuitBreakerResetTimeout time.Duration `toml:"circuit_breaker_reset_timeout" json:"circuit_breaker_reset_timeout" yaml:"circuit_breaker_reset_timeout"`
}

// Config is the root configuration structure
type Config struct {
	Global        TopLevelConfig       `toml:"global"` // [global.llm]
	Agents        AgentsConfig         `toml:"agents"` // [agents.llm] + per-agent overrides
	Tools         ToolsConfig          `toml:"tools"`  // [tools.llm] + per-tool overrides
	App           AppConfig            `toml:"app"`
	Agent         AgentConfig          `toml:"agent"`
	LLM           LLMConfig            `toml:"llm" json:"llm" yaml:"llm"`    // [llm] - LLM 推理兜底配置
	Compact       ContextCompactConfig `toml:"context"`        // [context] - 上下文压缩配置
	Browser       BrowserConfig        `toml:"browser"`        // [browser] - 浏览器配置
	CommitLearner CommitLearnerConfig  `toml:"commit_learner"` // [commit_learner] - commit 学习器配置
	Keywords      KeywordsConfig       `toml:"keywords"`       // [keywords] - 关键词词典配置
	TaskTimeout   time.Duration        `toml:"task_timeout" json:"task_timeout" yaml:"task_timeout"` // 全局任务超时，0=不启用

	// GitCheckpoint git checkpoint 机制配置
	GitCheckpoint GitCheckpointConfig `toml:"git_checkpoint"`

	// EnhancedCommander 增强型 Commander 配置
	EnhancedCommander EnhancedCommanderConfig `toml:"enhanced_commander" json:"enhanced_commander"`
}

// GetProvider returns a provider config by name from the shared provider pool.
func (c *Config) GetProvider(name string) (*ProviderConfig, error) {
	if name == "" {
		return nil, fmt.Errorf("empty provider name")
	}
	if c.Global.LLM == nil {
		return nil, fmt.Errorf("provider '%s' not found in configuration", name)
	}
	provider, exists := c.Global.LLM.Providers[name]
	if !exists {
		return nil, fmt.Errorf("provider '%s' not found in configuration", name)
	}
	return &provider, nil
}

// resolveAgentProvider returns the provider name for a specific agent.
// Priority: agents.<agent>.use_provider > agents.llm.use_provider.
func (c *Config) resolveAgentProvider(agentName string) string {
	// Per-agent override
	if override := c.getAgentOverride(agentName); override != nil && override.UseProvider != "" {
		return override.UseProvider
	}
	// Agents default
	if c.Agents.LLM.UseProvider != "" {
		return c.Agents.LLM.UseProvider
	}
	return ""
}

func (c *Config) getAgentOverride(agentName string) *AgentLLMOverride {
	switch strings.ToLower(agentName) {
	case "conductor-agent", "director-agent", "director":
		return c.Agents.LLM.Director
	case "coding-agent", "coding":
		return c.Agents.LLM.Coding
	case "repo-agent", "repo":
		return c.Agents.LLM.Repo
	case "chat-agent", "chat":
		return c.Agents.LLM.Chat
	case "meta-agent", "meta":
		return c.Agents.LLM.Meta
	case "devops-agent", "devops":
		return c.Agents.LLM.DevOps
	default:
		return nil
	}
}

// resolveToolProvider returns the provider name for a specific tool.
// Priority: tools.<tool>.use_provider > tools.llm.use_provider.
func (c *Config) resolveToolProvider(toolName string) string {
	// Per-tool override
	if override := c.getToolOverride(toolName); override != nil && override.UseProvider != "" {
		return override.UseProvider
	}
	// Tools default
	if c.Tools.LLM.UseProvider != "" {
		return c.Tools.LLM.UseProvider
	}
	return ""
}

func (c *Config) getToolOverride(toolName string) *ToolLLMOverride {
	switch strings.ToLower(toolName) {
	case "micro_agent":
		return c.Tools.LLM.MicroAgent
	case "thinking":
		return c.Tools.LLM.Thinking
	case "deepthinking":
		return c.Tools.LLM.DeepThinking
	default:
		return nil
	}
}

// ResolveProvider resolves the effective provider for a given context.
// Priority chain (highest first):
//  1. tools.llm.<tool>.use_provider
//  2. tools.llm.use_provider
//  3. agents.llm.<agent>.use_provider
//  4. agents.llm.use_provider
//  5. global.llm.use_provider
//
// agentName and toolName can be empty strings when no context is applicable.
func (c *Config) ResolveProvider(agentName, toolName string) (*ProviderConfig, error) {
	// 1-2. Tool-level override (highest priority)
	if toolName != "" {
		if name := c.resolveToolProvider(toolName); name != "" {
			return c.GetProvider(name)
		}
	}

	// 3-4. Agent-level override
	if agentName != "" {
		if name := c.resolveAgentProvider(agentName); name != "" {
			return c.GetProvider(name)
		}
	}

	// 5. Global override
	if c.Global.LLM != nil && c.Global.LLM.UseProvider != "" {
		return c.GetProvider(c.Global.LLM.UseProvider)
	}

	// 6. No provider configured
	return nil, fmt.Errorf("no LLM provider configured")
}

// GetProviderNames returns a list of all available provider names
func (c *Config) GetProviderNames() []string {
	if c.Global.LLM == nil {
		return []string{}
	}
	names := make([]string, 0, len(c.Global.LLM.Providers))
	for name := range c.Global.LLM.Providers {
		names = append(names, name)
	}
	return names
}

// DetectBedrockProvider detects the provider from Bedrock model ID
func DetectBedrockProvider(modelID string) string {
	modelID = strings.ToLower(modelID)

	// Nova models
	if strings.Contains(modelID, ".nova-") {
		return "amazon"
	}

	// Anthropic models
	if strings.Contains(modelID, "anthropic") {
		return "anthropic"
	}

	// Meta models
	if strings.Contains(modelID, "meta") {
		return "meta"
	}

	// Cohere models
	if strings.Contains(modelID, "cohere") {
		return "cohere"
	}

	// AI21 models
	if strings.Contains(modelID, "ai21") {
		return "ai21"
	}

	// Default to Amazon for other models
	return "amazon"
}

// LoadConfig loads configuration from a TOML file
func LoadConfig(path string) (*Config, error) {
	return LoadFromFile(path)
}

// resolveEffectiveProviderName returns the effective provider name using the full
// priority chain. Used during validation to find the fallback provider to validate.
func (c *Config) resolveEffectiveProviderName() string {
	// tools default
	if c.Tools.LLM.UseProvider != "" {
		return c.Tools.LLM.UseProvider
	}
	// agents default
	if c.Agents.LLM.UseProvider != "" {
		return c.Agents.LLM.UseProvider
	}
	// global
	if c.Global.LLM != nil && c.Global.LLM.UseProvider != "" {
		return c.Global.LLM.UseProvider
	}
	// No provider configured
	return ""
}

// validate validates the configuration
func (c *Config) validate() error {
	effectiveProvider := c.resolveEffectiveProviderName()
	if effectiveProvider == "" {
		return fmt.Errorf("'use_provider' must be specified (in global.llm, agents.llm, or tools.llm)")
	}

	if c.Global.LLM == nil || len(c.Global.LLM.Providers) == 0 {
		return fmt.Errorf("no providers configured in LLM section")
	}

	activeProvider, err := c.GetProvider(effectiveProvider)
	if err != nil {
		return err
	}

	// Validate active provider configuration
	if activeProvider.Model == "" {
		return fmt.Errorf("model must be specified for provider '%s'", effectiveProvider)
	}

	// Special validation for Bedrock provider
	if strings.HasPrefix(effectiveProvider, "bedrock") {
		if activeProvider.AWSRegion == "" {
			return fmt.Errorf("aws_region must be specified for Bedrock provider")
		}
		return nil
	}

	// For non-Bedrock providers, require API key and base URL
	if activeProvider.APIKey == "" {
		return fmt.Errorf("api_key must be specified for provider '%s'", effectiveProvider)
	}

	if activeProvider.APIBaseURL == "" {
		return fmt.Errorf("api_base_url must be specified for provider '%s'", effectiveProvider)
	}

	// ═══════ Agent MaxSteps 默认值设置 ═══════
	// 为各 agent 的最大步数设置默认值（如果未在 TOML 中配置）
	defaultSteps := DefaultMaxSteps
	if c.Agent.DirectorMaxSteps == 0 {
		c.Agent.DirectorMaxSteps = defaultSteps.Director
	}
	if c.Agent.CodingMaxSteps == 0 {
		c.Agent.CodingMaxSteps = defaultSteps.Coding
	}
	if c.Agent.RepoMaxSteps == 0 {
		c.Agent.RepoMaxSteps = defaultSteps.Repo
	}
	if c.Agent.ChatMaxSteps == 0 {
		c.Agent.ChatMaxSteps = defaultSteps.Chat
	}
	if c.Agent.DevOpsMaxSteps == 0 {
		c.Agent.DevOpsMaxSteps = defaultSteps.DevOps
	}
	if c.Agent.BrowserMaxSteps == 0 {
		c.Agent.BrowserMaxSteps = defaultSteps.Browser
	}
	if c.Agent.MetaMaxSteps == 0 {
		c.Agent.MetaMaxSteps = defaultSteps.Meta
	}
	if c.Agent.MetaRetryCount == 0 {
		c.Agent.MetaRetryCount = defaultSteps.MetaRetry
	}

	// ═══════ LLM 推理兜底默认值设置 ═══════
	llmDefaults := DefaultLLMConfig()
	if c.LLM.MaxRetries == 0 {
		c.LLM.MaxRetries = llmDefaults.MaxRetries
	}
	// 如果 CircuitBreakerResetTimeout == 0 且 CircuitBreakerThreshold > 0，设置为 60s
	if c.LLM.CircuitBreakerResetTimeout == 0 && c.LLM.CircuitBreakerThreshold > 0 {
		c.LLM.CircuitBreakerResetTimeout = 60 * time.Second
	}

	// ═══════ CommitLearner 默认值设置 ═══════
	commitDefaults := DefaultCommitLearnerConfig()
	if c.CommitLearner.MaxCommits == 0 {
		c.CommitLearner.MaxCommits = commitDefaults.MaxCommits
	}
	if c.CommitLearner.SimilarityThreshold == 0 {
		c.CommitLearner.SimilarityThreshold = commitDefaults.SimilarityThreshold
	}
	if c.CommitLearner.TopK == 0 {
		c.CommitLearner.TopK = commitDefaults.TopK
	}
	if c.CommitLearner.Trigger == "" {
		c.CommitLearner.Trigger = commitDefaults.Trigger
	}
	if c.CommitLearner.CacheTTL == 0 {
		c.CommitLearner.CacheTTL = commitDefaults.CacheTTL
	}
	// Enabled 默认为 true（零值为 true 时无需设置）
	// LLMSystemPrompt 为空时在 agents 包中使用默认值

	// ═══════ Keywords 默认值设置（向后兼容） ═══════
	// 如果 config.toml 中不存在 [keywords] 段，则创建默认配置
	if !c.hasKeywordsConfig() {
		homeDir, _ := os.UserHomeDir()
		c.Keywords.DefaultPath = homeDir + "/.codeactor/keywords.txt"
		c.Keywords.HotReload = false
		c.Keywords.Dicts = []DictConfig{
			{
				Name:        "autocomplete",
				Files:       []string{}, // 空表示使用 DefaultPath
				Type:        DictTypePrefix,
				BuiltinType: "default",
			},
		}
	}

	// ═══════ Enhanced Commander 默认值设置 ═══════
	if c.EnhancedCommander.CompressionThreshold == 0 {
		c.EnhancedCommander.CompressionThreshold = 4096
	}
	if c.EnhancedCommander.SummaryMaxLength == 0 {
		c.EnhancedCommander.SummaryMaxLength = 2048
	}
	if c.EnhancedCommander.MaxDelegationDepth == 0 {
		c.EnhancedCommander.MaxDelegationDepth = 3
	}

	// ═══════ Git Checkpoint 默认值设置 ═══════
	gitCfgDefaults := DefaultGitCheckpointConfig()
	if c.GitCheckpoint.AgentBranchPrefix == "" {
		// [git_checkpoint] 段完全缺失 → 应用完整默认值
		c.GitCheckpoint = gitCfgDefaults
	} else {
		// 段存在，按需补充零值字段
		if c.GitCheckpoint.CheckpointInterval == 0 {
			c.GitCheckpoint.CheckpointInterval = gitCfgDefaults.CheckpointInterval
		}
		if c.GitCheckpoint.MaxCheckpoints == 0 {
			c.GitCheckpoint.MaxCheckpoints = gitCfgDefaults.MaxCheckpoints
		}
	}

	// ═══════ Context Compact 默认值设置（三层防御的第二层） ═══════
	// 如果 Compact 配置的所有字段都为零值（用户完全未配置 [context] 段），
	// 则使用完整默认配置。否则逐个字段补充零值。
	c.applyContextDefaults()

	return nil
}

// hasKeywordsConfig 检查是否已配置 [keywords] 段
// 用于向后兼容：如果用户没有显式配置 keywords，则使用默认值
func (c *Config) hasKeywordsConfig() bool {
	// 如果 Dicts 非空，说明用户有显式配置
	if len(c.Keywords.Dicts) > 0 {
		return true
	}
	// 如果 DefaultPath 非空，说明用户有显式配置
	if c.Keywords.DefaultPath != "" {
		return true
	}
	return false
}

// applyContextDefaults 为 ContextCompactConfig 字段应用默认值。
//
// 这是三层防御的第二层（配置层）：确保即使 TOML 文件中缺少 [context] 段
// 或某些字段未配置（零值），也会使用合理的默认值。
//
// 逻辑：
// 1. 如果所有 8 个字段都为零值 → 整体替换为 DefaultCompactConfig()
// 2. 否则逐个字段检查，零值时填充默认值
func (c *Config) applyContextDefaults() {
	defaults := DefaultCompactConfig()

	// 检查是否所有字段都为零值（用户完全未配置 [context] 段）
	allZero := c.Compact.MaxContextTokens == 0 &&
		!c.Compact.EnableAutoCompact &&
		c.Compact.SummarizationModel == "" &&
		c.Compact.SummarizationProvider == "" &&
		c.Compact.SummarizationTimeout == 0 &&
		c.Compact.SummarizationMaxInputTokens == 0 &&
		c.Compact.SummarizationPrompt == "" &&
		c.Compact.KeepRecentRounds == 0

	if allZero {
		// 整体替换为默认配置
		c.Compact.MaxContextTokens = defaults.MaxContextTokens
		c.Compact.EnableAutoCompact = defaults.EnableAutoCompact
		c.Compact.SummarizationModel = defaults.SummarizationModel
		c.Compact.SummarizationProvider = defaults.SummarizationProvider
		c.Compact.SummarizationTimeout = defaults.SummarizationTimeout
		c.Compact.SummarizationMaxInputTokens = defaults.SummarizationMaxInputTokens
		c.Compact.SummarizationPrompt = defaults.SummarizationPrompt
		c.Compact.KeepRecentRounds = defaults.KeepRecentRounds
		return
	}

	// 部分配置：逐个字段补充零值
	if c.Compact.MaxContextTokens == 0 {
		c.Compact.MaxContextTokens = defaults.MaxContextTokens
	}
	// EnableAutoCompact: 零值为 false，但如果用户只配置了其他字段而没配这个，
	// 我们应该保持用户的 false（显式禁用）。所以不在此兜底。
	if c.Compact.SummarizationTimeout == 0 {
		c.Compact.SummarizationTimeout = defaults.SummarizationTimeout
	}
	if c.Compact.SummarizationMaxInputTokens == 0 {
		c.Compact.SummarizationMaxInputTokens = defaults.SummarizationMaxInputTokens
	}
	if c.Compact.KeepRecentRounds == 0 {
		c.Compact.KeepRecentRounds = defaults.KeepRecentRounds
	}
	// SummarizationModel, SummarizationProvider, SummarizationPrompt 为空字符串
	// 是合法的（表示使用默认行为），无需兜底
}

// ContextCompactConfig 上下文压缩配置
// 用于TOML解析，通过 compact.ConfigFrom() 转换为 compact.Config
type ContextCompactConfig struct {
	// MaxContextTokens 最大上下文token数，默认198000
	MaxContextTokens int `toml:"max_context_tokens"`

	// EnableAutoCompact 是否自动触发压缩
	EnableAutoCompact bool `toml:"enable_auto_compact"`

	// SummarizationModel 用于摘要的轻量模型
	SummarizationModel string `toml:"summarization_model"`

	// SummarizationProvider 用于摘要的LLM provider名称（可选，指向 providers 中定义的 provider）
	// 为空则复用主 agent 的 LLM 引擎
	SummarizationProvider string `toml:"summarization_provider"`

	// SummarizationTimeout 摘要超时时间（秒）
	SummarizationTimeout int `toml:"summarization_timeout"`

	// SummarizationMaxInputTokens 摘要时单批次最大输入token数
	SummarizationMaxInputTokens int `toml:"summarization_max_input_tokens"`

	// SummarizationPrompt 自定义摘要提示词（可选）
	SummarizationPrompt string `toml:"summarization_prompt"`

	// KeepRecentRounds 始终保留的最近对话轮数（用于优先级计算）
	KeepRecentRounds int `toml:"keep_recent_rounds"`
}

// CommitLearnerConfig commit 学习器配置
// 用于 TOML 解析，在 agents 包中转换为 CommitLearnConfig
type CommitLearnerConfig struct {
	Enabled               bool    `toml:"enabled"`                // 是否启用 commit 学习功能
	MaxCommits            int     `toml:"max_commits"`            // 最大获取 commit 数量
	SimilarityThreshold   float64 `toml:"similarity_threshold"`   // 相似度阈值（0.0-1.0）
	TopK                  int     `toml:"top_k"`                  // 搜索结果数量
	Trigger               string  `toml:"trigger"`                // 触发方式："on_demand", "on_session_start", "both"
	CacheTTL              int     `toml:"cache_ttl"`              // 缓存有效期（秒）
	LLMSystemPrompt       string  `toml:"llm_system_prompt"`      // LLM 系统提示词（空时使用默认值）
	SummarizationProvider string  `toml:"summarization_provider"` // 专用的 LLM provider 名称，空时使用全局默认
}

// BrowserConfig 浏览器配置
type BrowserConfig struct {
	Headless           bool     `toml:"headless"`             // 无头模式，默认 true
	BrowserPath        string   `toml:"browser_path"`         // 浏览器可执行文件路径（空=自动查找/下载）
	UserDataDir        string   `toml:"user_data_dir"`        // 用户数据目录（空=临时目录）
	ViewportWidth      int      `toml:"viewport_width"`       // 视口宽度，默认 1280
	ViewportHeight     int      `toml:"viewport_height"`      // 视口高度，默认 720
	AllowedDomains     []string `toml:"allowed_domains"`      // 允许访问的域名列表（空=全部允许）
	BlockedDomains     []string `toml:"blocked_domains"`      // 阻止访问的域名列表
	TimeoutSeconds     int      `toml:"timeout_seconds"`      // 单个操作超时秒数，默认 30
	TaskTimeoutSeconds int      `toml:"task_timeout_seconds"` // 单个浏览器任务总超时秒数，默认 300
	MaxConcurrentPages int      `toml:"max_concurrent_pages"` // 最大并发页面数，默认 4
	AutoLaunch         bool     `toml:"auto_launch"`          // 首次请求时自动启动浏览器，默认 true
	IdleTimeout        string   `toml:"idle_timeout"`         // 空闲超时（如 "5m"），空=不自动关闭
	AllowNoSandbox     bool     `toml:"allow_no_sandbox"`     // 允许 --no-sandbox（Docker环境需要），默认 false
	ExtraArgs          []string `toml:"extra_args"`           // 额外的 Chrome 命令行参数
	EnableBrowserAgent bool     `toml:"enable_browser_agent"` // 是否启用 Browser-Agent，默认 true
}

// ═══════════════════════════════════════════════════════════════
// Enhanced Commander 配置
// ═══════════════════════════════════════════════════════════════

// EnhancedCommanderConfig 增强型 Commander 配置
// 所有选项默认关闭，启用时需显式设置，确保零影响现有行为
type EnhancedCommanderConfig struct {
	// Enable 总开关，关闭时所有增强功能不生效
	Enable bool `toml:"enable" json:"enable"`

	// EnableResultCompression 是否启用结果压缩
	EnableResultCompression bool `toml:"enable_result_compression" json:"enable_result_compression"`

	// CompressionThreshold 结果压缩阈值（字节），默认 4096
	CompressionThreshold int `toml:"compression_threshold" json:"compression_threshold"`

	// SummaryMaxLength 摘要最大长度（字符），默认 2048
	SummaryMaxLength int `toml:"summary_max_length" json:"summary_max_length"`

	// MaxDelegationDepth 最大委派深度，默认 3
	MaxDelegationDepth int `toml:"max_delegation_depth" json:"max_delegation_depth"`
}

// ═══════════════════════════════════════════════════════════════
// 关键词词典配置
// ═══════════════════════════════════════════════════════════════

// 词典类型: "prefix" (前缀匹配/补全) 或 "exact" (精确匹配/扫描)
const (
	DictTypePrefix = "prefix"
	DictTypeExact  = "exact"
)

// DictConfig 词典配置项
type DictConfig struct {
	Name        string   `toml:"name"`
	Files       []string `toml:"files"`
	Type        string   `toml:"type"`         // "prefix" or "exact"
	BuiltinType string   `toml:"builtin_type"` // "default", "none"
}

// KeywordsConfig 关键词词典配置
type KeywordsConfig struct {
	DefaultPath       string       `toml:"default_path"`       // 用户默认关键词文件路径
	HotReload         bool         `toml:"hot_reload"`         // 是否启用热重载
	DisableCompletion bool         `toml:"disable_completion"` // 禁用关键词自动补全（默认 false 表示启用，保持向后兼容）
	Dicts             []DictConfig `toml:"dict"`               // 词典列表
}
