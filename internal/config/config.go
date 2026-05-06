package config

import (
	"context"
	"fmt"
	"strings"

	"codeactor/internal/util"

	"github.com/BurntSushi/toml"
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
}

// AppConfig contains application-level configuration
type AppConfig struct {
	EnableStreaming bool `toml:"enable_streaming"`
}

// AgentConfig contains agent-specific configuration
type AgentConfig struct {
	ConductorMaxSteps int    `toml:"conductor_max_steps"`
	CodingMaxSteps    int    `toml:"coding_max_steps"`
	ChatMaxSteps      int    `toml:"chat_max_steps"`
	RepoMaxSteps      int    `toml:"repo_max_steps"`
	DevOpsMaxSteps    int    `toml:"devops_max_steps"`
	ImplPlanMaxSteps  int    `toml:"impl_plan_max_steps"`
	MetaMaxSteps      int    `toml:"meta_max_steps"`
	MetaRetryCount    int    `toml:"meta_retry_count"`
	SpeakLang         string `toml:"lang"`
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
	UseProvider string                    `toml:"use_provider"` // default for all agents
	Conductor   *AgentLLMOverride         `toml:"conductor,omitempty"`
	Coding      *AgentLLMOverride         `toml:"coding,omitempty"`
	Repo        *AgentLLMOverride         `toml:"repo,omitempty"`
	Chat        *AgentLLMOverride         `toml:"chat,omitempty"`
	Meta        *AgentLLMOverride         `toml:"meta,omitempty"`
	DevOps      *AgentLLMOverride         `toml:"devops,omitempty"`
	ImplPlan    *AgentLLMOverride         `toml:"impl_plan,omitempty"`
}

// ToolLLMOverride selects a provider for a specific tool.
type ToolLLMOverride struct {
	UseProvider string `toml:"use_provider"`
}

// ToolsLLMConfig holds per-tool LLM overrides.
// Priority: per-tool > tools.default > agent > global.
type ToolsLLMConfig struct {
	UseProvider string                    `toml:"use_provider"` // default for all tools
	MicroAgent  *ToolLLMOverride          `toml:"micro_agent,omitempty"`
	Thinking    *ToolLLMOverride          `toml:"thinking,omitempty"`
	ImplPlan    *ToolLLMOverride          `toml:"impl_plan,omitempty"`
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

// Config is the root configuration structure
type Config struct {
	Global  TopLevelConfig          `toml:"global"`  // [global.llm]
	Agents  AgentsConfig            `toml:"agents"`  // [agents.llm] + per-agent overrides
	Tools   ToolsConfig             `toml:"tools"`   // [tools.llm] + per-tool overrides
	App     AppConfig               `toml:"app"`
	Agent   AgentConfig             `toml:"agent"`
	Compact ContextCompactConfig    `toml:"context"` // [context] - 上下文压缩配置
}

// getProvider returns a provider config by name from the shared provider pool.
func (c *Config) getProvider(name string) (*ProviderConfig, error) {
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
	case "conductor-agent", "conductor":
		return c.Agents.LLM.Conductor
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
	case "impl_plan-agent", "impl_plan":
		return c.Agents.LLM.ImplPlan
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
	case "impl_plan":
		return c.Tools.LLM.ImplPlan
	default:
		return nil
	}
}

// ResolveProvider resolves the effective provider for a given context.
// Priority chain (highest first):
//   1. tools.llm.<tool>.use_provider
//   2. tools.llm.use_provider
//   3. agents.llm.<agent>.use_provider
//   4. agents.llm.use_provider
//   5. global.llm.use_provider
//
// agentName and toolName can be empty strings when no context is applicable.
func (c *Config) ResolveProvider(agentName, toolName string) (*ProviderConfig, error) {
	// 1-2. Tool-level override (highest priority)
	if toolName != "" {
		if name := c.resolveToolProvider(toolName); name != "" {
			return c.getProvider(name)
		}
	}

	// 3-4. Agent-level override
	if agentName != "" {
		if name := c.resolveAgentProvider(agentName); name != "" {
			return c.getProvider(name)
		}
	}

	// 5. Global override
	if c.Global.LLM != nil && c.Global.LLM.UseProvider != "" {
		return c.getProvider(c.Global.LLM.UseProvider)
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
	config := &Config{}
	ctx := context.Background()

	// Read and parse the config file
	if _, err := toml.DecodeFile(path, config); err != nil {
		return nil, util.WrapError(ctx, err, "LoadConfig::DecodeFile")
	}

	// Validate configuration
	if err := config.validate(); err != nil {
		return nil, util.WrapError(ctx, err, "LoadConfig::validate")
	}

	return config, nil
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

	activeProvider, err := c.getProvider(effectiveProvider)
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

	return nil
}

// ContextCompactConfig 上下文压缩配置
// 用于TOML解析，通过 compact.ConfigFrom() 转换为 compact.Config
type ContextCompactConfig struct {
	// MaxContextTokens 最大上下文token数，默认198000
	MaxContextTokens int `toml:"max_context_tokens"`

	// Strategy 压缩策略: conservative | balanced | aggressive
	Strategy string `toml:"compression_strategy"`

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

	// SummarizationTimeout L1摘要超时时间（秒）
	SummarizationTimeout int `toml:"summarization_timeout"`

	// KeepRecentRounds 始终保留的最近对话轮数
	KeepRecentRounds int `toml:"keep_recent_rounds"`

	// KeepTaskConclusions 保留已完成任务的结论数
	KeepTaskConclusions int `toml:"keep_task_conclusions"`
}
