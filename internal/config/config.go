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
	MetaMaxSteps      int    `toml:"meta_max_steps"`
	MetaRetryCount    int    `toml:"meta_retry_count"`
	SpeakLang         string `toml:"lang"`
}

// Config is the root configuration structure
type Config struct {
	LLM   LLMConfig   `toml:"llm"`
	App   AppConfig   `toml:"app"`
	Agent AgentConfig `toml:"agent"`
}

// GetActiveProvider returns the currently active provider configuration
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

// validate validates the configuration
func (c *Config) validate() error {
	if c.LLM.UseProvider == "" {
		return fmt.Errorf("'use_provider' must be specified in LLM configuration")
	}

	if len(c.LLM.Providers) == 0 {
		return fmt.Errorf("no providers configured in LLM section")
	}

	activeProvider, err := c.GetActiveProvider()
	if err != nil {
		return err
	}

	// Validate active provider configuration
	if activeProvider.Model == "" {
		return fmt.Errorf("model must be specified for provider '%s'", c.LLM.UseProvider)
	}

	// Special validation for Bedrock provider
	if c.LLM.UseProvider == "bedrock" {
		// Bedrock doesn't require API key in config (uses AWS credentials)
		if activeProvider.AWSRegion == "" {
			return fmt.Errorf("aws_region must be specified for Bedrock provider")
		}
		// AWS credentials can be provided via environment variables or AWS profile
		return nil
	}

	// For non-Bedrock providers, require API key and base URL
	if activeProvider.APIKey == "" {
		return fmt.Errorf("api_key must be specified for provider '%s'", c.LLM.UseProvider)
	}

	if activeProvider.APIBaseURL == "" {
		return fmt.Errorf("api_base_url must be specified for provider '%s'", c.LLM.UseProvider)
	}

	return nil
}
