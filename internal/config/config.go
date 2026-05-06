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

// LLMConfig contains configuration for multiple LLM providers
type LLMConfig struct {
	UseProvider string                    `toml:"use_provider"`
	Providers   map[string]ProviderConfig `toml:"providers"`
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
	MetaMaxSteps      int    `toml:"meta_max_steps"`
	MetaRetryCount    int    `toml:"meta_retry_count"`
	SpeakLang         string `toml:"lang"`
}

// ── Three-tier LLM overrides ──

// GlobalLLMConfig is the global default LLM provider selection.
type GlobalLLMConfig struct {
	UseProvider string `toml:"use_provider"`
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
}

// TopLevelConfig groups the [global] section.
type TopLevelConfig struct {
	LLM *GlobalLLMConfig `toml:"llm"` // [global.llm]
}

// Config is the root configuration structure
type Config struct {
	LLM     LLMConfig      `toml:"llm"`     // backward-compat [llm] section
	Global  TopLevelConfig `toml:"global"`  // [global.llm]
	Agents  AgentsLLMConfig `toml:"agents"` // [agents.llm] + per-agent overrides
	Tools   ToolsLLMConfig  `toml:"tools"`  // [tools.llm] + per-tool overrides
	App     AppConfig       `toml:"app"`
	Agent   AgentConfig     `toml:"agent"`
}

// GetActiveProvider returns the currently active provider configuration.
// Uses the backward-compatible [llm] section as the ultimate fallback.
func (c *Config) GetActiveProvider() (*ProviderConfig, error) {
	if c.LLM.UseProvider == "" {
		return nil, fmt.Errorf("no provider selected, please set 'use_provider' in config")
	}

	provider, exists := c.LLM.Providers[c.LLM.UseProvider]
	if !exists {
		return nil, fmt.Errorf("provider '%s' not found in configuration", c.LLM.UseProvider)
	}

	return &provider, nil
}

// getProvider returns a provider config by name from the shared provider pool.
func (c *Config) getProvider(name string) (*ProviderConfig, error) {
	if name == "" {
		return nil, fmt.Errorf("empty provider name")
	}
	provider, exists := c.LLM.Providers[name]
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
	if c.Agents.UseProvider != "" {
		return c.Agents.UseProvider
	}
	return ""
}

func (c *Config) getAgentOverride(agentName string) *AgentLLMOverride {
	switch strings.ToLower(agentName) {
	case "conductor-agent", "conductor":
		return c.Agents.Conductor
	case "coding-agent", "coding":
		return c.Agents.Coding
	case "repo-agent", "repo":
		return c.Agents.Repo
	case "chat-agent", "chat":
		return c.Agents.Chat
	case "meta-agent", "meta":
		return c.Agents.Meta
	case "devops-agent", "devops":
		return c.Agents.DevOps
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
	if c.Tools.UseProvider != "" {
		return c.Tools.UseProvider
	}
	return ""
}

func (c *Config) getToolOverride(toolName string) *ToolLLMOverride {
	switch strings.ToLower(toolName) {
	case "micro_agent":
		return c.Tools.MicroAgent
	case "thinking":
		return c.Tools.Thinking
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
//   6. llm.use_provider (legacy fallback)
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

	// 6. Legacy fallback
	return c.GetActiveProvider()
}

// GetProviderNames returns a list of all available provider names
func (c *Config) GetProviderNames() []string {
	names := make([]string, 0, len(c.LLM.Providers))
	for name := range c.LLM.Providers {
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
	if c.Tools.UseProvider != "" {
		return c.Tools.UseProvider
	}
	// agents default
	if c.Agents.UseProvider != "" {
		return c.Agents.UseProvider
	}
	// global
	if c.Global.LLM != nil && c.Global.LLM.UseProvider != "" {
		return c.Global.LLM.UseProvider
	}
	// legacy
	return c.LLM.UseProvider
}

// validate validates the configuration
func (c *Config) validate() error {
	effectiveProvider := c.resolveEffectiveProviderName()
	if effectiveProvider == "" {
		return fmt.Errorf("'use_provider' must be specified (in global.llm, agents.llm, tools.llm, or llm)")
	}

	if len(c.LLM.Providers) == 0 {
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
