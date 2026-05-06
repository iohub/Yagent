package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"codeactor/internal/config"
	"codeactor/internal/util"

	"log/slog"
)

// llmLogger is a separate logger for LLM responses
var llmLogger *slog.Logger
var llmLogFile *os.File

// initLLMLogger initializes the LLM logger
func initLLMLogger() error {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return util.WrapError(context.Background(), err, "failed to get user home directory")
	}
	logDir := filepath.Join(homeDir, ".codeactor", "logs")

	if err := os.MkdirAll(logDir, 0755); err != nil {
		return util.WrapError(context.Background(), err, "failed to create logs directory")
	}

	dateStr := time.Now().Format("2006-01-02")
	logFileName := fmt.Sprintf("llm-%s.log", dateStr)
	var errFile error
	llmLogFile, errFile = os.OpenFile(filepath.Join(logDir, logFileName), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if errFile != nil {
		return util.WrapError(context.Background(), errFile, "failed to open LLM log file")
	}

	handler := slog.NewTextHandler(llmLogFile, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	llmLogger = slog.New(handler)
	return nil
}

// LogLLMContent writes a raw string to the LLM log file with a header
func LogLLMContent(title string, content string) {
	if llmLogFile == nil {
		return
	}
	timestamp := time.Now().Format("2006-01-02 15:04:05")
	separator := strings.Repeat("-", 80)
	logEntry := fmt.Sprintf("\n%s\n[%s] %s:\n%s\n%s\n", separator, timestamp, title, content, separator)
	if _, err := llmLogFile.WriteString(logEntry); err != nil {
		slog.Error("Failed to write to LLM log file", "error", err)
	}
}

// LoggingEngine wraps an Engine to add logging
type LoggingEngine struct {
	inner Engine
}

func (l *LoggingEngine) GenerateContent(ctx context.Context, messages []Message, tools []ToolDef, opts *CallOptions) (*Response, error) {
	if msgsJSON, err := json.MarshalIndent(messages, "", "  "); err == nil {
		LogLLMContent("LLM Input (messages)", string(msgsJSON))
	}
	if toolsJSON, err := json.MarshalIndent(tools, "", "  "); err == nil {
		LogLLMContent("LLM Input (tools)", string(toolsJSON))
	}

	resp, err := l.inner.GenerateContent(ctx, messages, tools, opts)
	if err == nil && len(resp.Choices) > 0 {
		choice := resp.Choices[0]
		logContent := choice.Content
		if len(choice.ToolCalls) > 0 {
			var toolCallsLog strings.Builder
			if logContent != "" {
				toolCallsLog.WriteString("\n")
			}
			toolCallsLog.WriteString("[Tool Calls]:\n")
			for i, tc := range choice.ToolCalls {
				toolCallsLog.WriteString(fmt.Sprintf("%d. %s(%s)\n", i+1, tc.Function.Name, tc.Function.Arguments))
			}
			logContent += toolCallsLog.String()
		}
		LogLLMContent("LLM Response", logContent)
	} else if err != nil {
		LogLLMContent("LLM Error", err.Error())
	}
	return resp, err
}

// Client represents an LLM client with support for per-agent and per-tool engines.
// Engines are lazily created and cached by provider name.
type Client struct {
	Engine        Engine // default engine (backward-compatible)
	Config        *config.Config
	engines       map[string]Engine // cached engines keyed by provider name
	mu            sync.RWMutex
}

// LoadConfig loads configuration from a TOML file using the multi-provider structure
func LoadConfig(configPath string) (*config.Config, error) {
	slog.Debug("Decoding TOML configuration file", "config_path", configPath)

	config, err := config.LoadConfig(configPath)
	if err != nil {
		slog.Error("Failed to decode TOML configuration file",
			"error", err,
			"config_path", configPath)
		return nil, err
	}

	activeProvider, err := config.GetActiveProvider()
	if err != nil {
		slog.Error("Failed to get active provider configuration", "error", err)
		return nil, err
	}

	slog.Debug("Configuration loaded successfully",
		"provider", config.LLM.UseProvider,
		"model", activeProvider.Model,
		"api_base_url", activeProvider.APIBaseURL,
		"temperature", activeProvider.Temperature,
		"max_tokens", activeProvider.MaxTokens,
		"streaming_enabled", config.App.EnableStreaming)

	return config, nil
}

// NewClient creates a new LLM client from config.
// The default engine is resolved using the full priority chain.
func NewClient(config *config.Config) (*Client, error) {
	ctx := context.Background()

	if err := initLLMLogger(); err != nil {
		slog.Error("Failed to initialize LLM logger", "error", err)
		return nil, util.WrapError(ctx, err, "failed to initialize LLM logger")
	}

	// Resolve the default (global) provider
	defaultProvider, err := config.ResolveProvider("", "")
	if err != nil {
		slog.Error("Failed to resolve default provider", "error", err)
		return nil, util.WrapError(ctx, err, "failed to resolve default provider")
	}

	if defaultProvider.APIKey == "" {
		slog.Error("Cannot create LLM client: API key is empty")
		return nil, util.WrapError(ctx, fmt.Errorf("API key not found in config"), "API key validation failed")
	}

	slog.Info("Creating new LLM client",
		"model", defaultProvider.Model,
		"api_base_url", defaultProvider.APIBaseURL)

	engine := NewOpenAIEngine(defaultProvider.APIBaseURL, defaultProvider.APIKey, defaultProvider.Model)
	loggingEngine := &LoggingEngine{inner: engine}

	return &Client{
		Engine:  loggingEngine,
		Config:  config,
		engines: make(map[string]Engine),
	}, nil
}

// ResolveProviderName resolves the provider name using the full priority chain.
// Exported for logging convenience.
func (c *Client) ResolveProviderName(agentName, toolName string) string {
	provider, err := c.Config.ResolveProvider(agentName, toolName)
	if err != nil {
		return "unknown"
	}
	// Find the provider key from the config
	for name, p := range c.Config.LLM.Providers {
		if p.Model == provider.Model && p.APIBaseURL == provider.APIBaseURL {
			return name
		}
	}
	return "unknown"
}

// getOrCreateEngine returns a cached engine for the given provider, or creates one.
func (c *Client) getOrCreateEngine(provider *config.ProviderConfig, providerName string) Engine {
	c.mu.RLock()
	if eng, ok := c.engines[providerName]; ok {
		c.mu.RUnlock()
		return eng
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	// Double-check after acquiring write lock
	if eng, ok := c.engines[providerName]; ok {
		return eng
	}

	slog.Info("Creating engine for provider", "provider", providerName, "model", provider.Model)
	engine := NewOpenAIEngine(provider.APIBaseURL, provider.APIKey, provider.Model)
	loggingEngine := &LoggingEngine{inner: engine}
	c.engines[providerName] = loggingEngine
	return loggingEngine
}

// GetEngine returns the default (global) engine.
func (c *Client) GetEngine() Engine {
	return c.Engine
}

// GetAgentEngine resolves and returns the engine for a specific agent.
// Uses the priority chain: agents.<agent> > agents.default > global > legacy.
func (c *Client) GetAgentEngine(agentName string) Engine {
	provider, err := c.Config.ResolveProvider(agentName, "")
	if err != nil {
		slog.Warn("Failed to resolve agent provider, falling back to default", "agent", agentName, "error", err)
		return c.Engine
	}

	providerName := c.resolveProviderName(provider)
	if providerName == "" {
		return c.Engine
	}

	// If resolved provider matches the default engine's provider, reuse default
	defaultProvider, defaultErr := c.Config.ResolveProvider("", "")
	if defaultErr == nil && providersEqual(provider, defaultProvider) {
		return c.Engine
	}

	return c.getOrCreateEngine(provider, providerName)
}

// GetToolEngine resolves and returns the engine for a specific tool.
// Uses the priority chain: tools.<tool> > tools.default > agent > global > legacy.
func (c *Client) GetToolEngine(toolName string) Engine {
	provider, err := c.Config.ResolveProvider("", toolName)
	if err != nil {
		slog.Warn("Failed to resolve tool provider, falling back to default", "tool", toolName, "error", err)
		return c.Engine
	}

	providerName := c.resolveProviderName(provider)
	if providerName == "" {
		return c.Engine
	}

	// If resolved provider matches the default engine's provider, reuse default
	defaultProvider, defaultErr := c.Config.ResolveProvider("", "")
	if defaultErr == nil && providersEqual(provider, defaultProvider) {
		return c.Engine
	}

	return c.getOrCreateEngine(provider, providerName)
}

// resolveProviderName finds the provider key in config for a given ProviderConfig.
func (c *Client) resolveProviderName(provider *config.ProviderConfig) string {
	for name, p := range c.Config.LLM.Providers {
		if p.APIBaseURL == provider.APIBaseURL && p.Model == provider.Model {
			return name
		}
	}
	return ""
}

// providersEqual checks if two provider configs refer to the same provider.
func providersEqual(a, b *config.ProviderConfig) bool {
	if a == nil || b == nil {
		return false
	}
	return a.APIBaseURL == b.APIBaseURL && a.APIKey == b.APIKey && a.Model == b.Model
}

// StreamDebugHandler prints each stream output text to stdout and logs to LLM log file
func StreamDebugHandler(ctx context.Context, chunk []byte) error {
	if len(chunk) > 0 {
		fmt.Print(string(chunk))
		os.Stdout.Sync()
		llmLogger.Info("Stream chunk", "type", "stream_chunk", "content", string(chunk))
	}
	return nil
}

// GenerateCompletionWithMemory generates a completion using the provided memory (conversation history)
func (c *Client) GenerateCompletionWithMemory(ctx context.Context, memory []Message, streamHandler func(context.Context, []byte) error) (string, error) {
	slog.Debug("Starting completion generation with memory",
		"memory_length", len(memory),
		"streaming_enabled", c.Config.App.EnableStreaming)

	if memoryJSON, err := json.Marshal(memory); err == nil {
		llmLogger.Info("LLM input memory",
			"type", "input_memory",
			"model", c.Config.LLM.UseProvider,
			"memory_length", len(memory),
			"memory", string(memoryJSON))
	}

	activeProvider, err := c.Config.GetActiveProvider()
	if err != nil {
		slog.Error("Failed to get active provider configuration", "error", err)
		return "", util.WrapError(ctx, err, "failed to get active provider")
	}

	opts := &CallOptions{
		MaxTokens:   activeProvider.MaxTokens,
		Temperature: activeProvider.Temperature,
	}

	if c.Config.App.EnableStreaming && streamHandler != nil {
		opts.StreamHandler = streamHandler
		slog.Debug("Streaming enabled for this request (memory)")
	}

	completion, err := c.Engine.GenerateContent(ctx, memory, nil, opts)
	if err != nil {
		httpResponse := extractHTTPResponse(err)

		slog.Error("Failed to GenerateContent",
			"error", err,
			"model", c.Config.LLM.UseProvider,
			"memory_length", len(memory),
			"http_response", httpResponse)

		llmLogger.Error("LLM completion error",
			"type", "completion_error",
			"model", c.Config.LLM.UseProvider,
			"memory_length", len(memory),
			"error", err.Error(),
			"http_response", httpResponse)

		return "", util.WrapError(ctx, err, "error generating completion (memory)")
	}

	if len(completion.Choices) > 0 {
		result := completion.Choices[0].Content
		slog.Info("Completion generated successfully (memory)",
			"result_length", len(result),
			"choices_count", len(completion.Choices))

		if choicesJSON, err := json.Marshal(completion.Choices); err == nil {
			llmLogger.Info("LLM completion output",
				"type", "completion_output",
				"model", c.Config.LLM.UseProvider,
				"choices_count", len(completion.Choices),
				"memory_length", len(memory),
				"response", result,
				"response.Choices", string(choicesJSON))
		}

		return result, nil
	}

	slog.Warn("No completion choices returned from LLM (memory)",
		"choices_count", len(completion.Choices))

	llmLogger.Warn("LLM returned empty completion (with memory)",
		"type", "empty_completion_memory",
		"model", c.Config.LLM.UseProvider,
		"choices_count", len(completion.Choices),
		"memory_length", len(memory))

	return "", nil
}

// extractHTTPResponse 尝试从错误中提取HTTP响应内容
func extractHTTPResponse(err error) string {
	if err == nil {
		return ""
	}

	return fmt.Sprintf("Error: %s", err.Error())
}
