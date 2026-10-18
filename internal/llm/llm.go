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

	"yagent/internal/config"
	"yagent/internal/logging"
	"yagent/internal/util"

	"log/slog"
)

var llmLogger *slog.Logger
var llmLogFile *os.File

var llmErrorLogger *slog.Logger
var llmErrorLogFile *os.File

func getLLMLogFile() string {
	logDir := logging.GetTaskLogDir(logging.GetCurrentTaskID())
	dateStr := time.Now().Format("2006-01-02")
	return filepath.Join(logDir, fmt.Sprintf("llm-%s.log", dateStr))
}

func getLLMErrorLogFile() string {
	logDir := logging.GetTaskLogDir(logging.GetCurrentTaskID())
	dateStr := time.Now().Format("2006-01-02")
	return filepath.Join(logDir, fmt.Sprintf("llm-error-%s.log", dateStr))
}

func ensureLLMLogFile() {
	currentPath := ""
	if llmLogFile != nil {
		currentPath = llmLogFile.Name()
	}
	if newPath := getLLMLogFile(); currentPath != newPath {
		if llmLogFile != nil {
			llmLogFile.Close()
			llmLogFile = nil
		}
		logDir := logging.GetTaskLogDir(logging.GetCurrentTaskID())
		if err := os.MkdirAll(logDir, 0755); err != nil {
			slog.Warn("Failed to create LLM log directory", "dir", logDir, "error", err)
			return
		}
		var err error
		llmLogFile, err = os.OpenFile(newPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			slog.Error("Failed to open LLM log file", "path", newPath, "error", err)
			llmLogFile = nil
			return
		}
		level := slog.LevelInfo
		if os.Getenv("LLM_DEBUG_LOG") == "1" {
			level = slog.LevelDebug
		}
		handler := slog.NewTextHandler(llmLogFile, &slog.HandlerOptions{Level: level})
		llmLogger = slog.New(handler)
	}
}

func ensureLLMErrorLogFile() {
	currentPath := ""
	if llmErrorLogFile != nil {
		currentPath = llmErrorLogFile.Name()
	}
	if newPath := getLLMErrorLogFile(); currentPath != newPath {
		if llmErrorLogFile != nil {
			llmErrorLogFile.Close()
			llmErrorLogFile = nil
		}
		logDir := logging.GetTaskLogDir(logging.GetCurrentTaskID())
		if err := os.MkdirAll(logDir, 0755); err != nil {
			slog.Warn("Failed to create LLM error log directory", "dir", logDir, "error", err)
			return
		}
		var err error
		llmErrorLogFile, err = os.OpenFile(newPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			slog.Error("Failed to open LLM error log file", "path", newPath, "error", err)
			llmErrorLogFile = nil
			return
		}
		handler := slog.NewTextHandler(llmErrorLogFile, &slog.HandlerOptions{Level: slog.LevelError})
		llmErrorLogger = slog.New(handler)
	}
}

func initLLMLogger() error {
	if err := os.MkdirAll(logging.GetTaskLogDir(logging.GetCurrentTaskID()), 0755); err != nil {
		return util.WrapError(context.Background(), err, "failed to create logs directory")
	}

	logFilePath := getLLMLogFile()
	var err error
	llmLogFile, err = os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		return util.WrapError(context.Background(), err, "failed to open LLM log file")
	}

	level := slog.LevelInfo
	if os.Getenv("LLM_DEBUG_LOG") == "1" {
		level = slog.LevelDebug
	}

	handler := slog.NewTextHandler(llmLogFile, &slog.HandlerOptions{
		Level: level,
	})
	llmLogger = slog.New(handler)

	if err := initLLMErrorLogger(); err != nil {
		slog.Warn("Failed to initialize LLM error logger, LLM errors will fallback", "error", err)
	}
	return nil
}

func initLLMErrorLogger() error {
	if err := os.MkdirAll(logging.GetTaskLogDir(logging.GetCurrentTaskID()), 0755); err != nil {
		return util.WrapError(context.Background(), err, "failed to create logs directory for LLM error")
	}

	logFilePath := getLLMErrorLogFile()
	var err error
	llmErrorLogFile, err = os.OpenFile(logFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		return util.WrapError(context.Background(), err, "failed to open LLM error log file")
	}

	handler := slog.NewTextHandler(llmErrorLogFile, &slog.HandlerOptions{
		Level: slog.LevelError,
	})
	llmErrorLogger = slog.New(handler)
	return nil
}

func LogLLMContent(title string, content string) {
	ensureLLMLogFile()
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

func LogLLMError(msg string, args ...any) {
	ensureLLMErrorLogFile()
	if llmErrorLogger != nil {
		llmErrorLogger.Error(msg, args...)
		return
	}
	if llmLogger != nil {
		llmLogger.Error(msg, args...)
		return
	}
	slog.Error(msg, args...)
}

type LoggingEngine struct {
	inner Engine
}

func (l *LoggingEngine) CloseIdleConnections() {
	if l.inner != nil {
		l.inner.CloseIdleConnections()
	}
}

func (l *LoggingEngine) GenerateContent(ctx context.Context, messages []Message, tools []ToolDef, opts *CallOptions) (*Response, error) {
	if len(messages) > 0 {
		lastMsg := messages[len(messages)-1]
		if lastMsgJSON, err := json.MarshalIndent(lastMsg, "", "  "); err == nil {
			LogLLMContent("LLM Input (last message)", string(lastMsgJSON))
		}
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
		LogLLMError("LLM GenerateContent error",
			"error", err,
		)
		LogLLMContent("LLM Error", err.Error())
	}
	return resp, err
}

func (l *LoggingEngine) Model() string {
	return l.inner.Model()
}

type Client struct {
	Engine                 Engine
	Config                 *config.Config
	ConfigPath             string
	engines                map[string]Engine
	mu                     sync.RWMutex
	agentProviderOverrides map[string]string
	toolProviderOverrides  map[string]string
	router                 *Router
}

// GetRouter 返回启动时构建并缓存的自动路由器（可能未启用）。
func (c *Client) GetRouter() *Router {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.router
}

// CloseIdleConnections 关闭默认引擎与所有已缓存 provider 引擎的空闲连接。
func (c *Client) CloseIdleConnections() {
	c.mu.RLock()
	engines := make([]Engine, 0, len(c.engines)+1)
	if c.Engine != nil {
		engines = append(engines, c.Engine)
	}
	for _, e := range c.engines {
		engines = append(engines, e)
	}
	c.mu.RUnlock()
	for _, e := range engines {
		e.CloseIdleConnections()
	}
}

func LoadConfig(configPath string) (*config.Config, error) {
	slog.Debug("Decoding TOML configuration file", "config_path", configPath)

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		slog.Error("Failed to decode TOML configuration file",
			"error", err,
			"config_path", configPath)
		return nil, err
	}

	// 加载成功后初始化全局 LLM 调用超时运行时值（运行时唯一事实源，TUI
	// 模型设置面板可动态修改；仅当配置值 > 0 时生效，未配置时各调用方回落到
	// config.DefaultLLMCallTimeout）。
	if cfg.LLM.Timeout > 0 {
		config.SetGlobalLLMTimeout(cfg.LLM.Timeout)
	}
	config.SetGlobalDeferredTools(cfg.Tools.Deferred)
	SetInFlightLimit(cfg.LLM.MaxInFlightRequests)

	activeProvider, err := cfg.ResolveProvider("", "")
	if err != nil {
		slog.Error("Failed to get active provider configuration", "error", err)
		return nil, err
	}

	slog.Debug("Configuration loaded successfully",
		"provider", cfg.Global.LLM.UseProvider,
		"model", activeProvider.Model,
		"api_base_url", activeProvider.APIBaseURL,
		"temperature", activeProvider.Temperature,
		"max_tokens", activeProvider.MaxTokens)

	return cfg, nil
}

func NewEngine(provider *config.ProviderConfig, llmCfg config.LLMConfig) Engine {
	if provider.IsAnthropic() {
		return NewAnthropicEngine(provider, llmCfg)
	}
	return NewOpenAIEngine(provider, llmCfg)
}

func NewClient(cfg *config.Config) (*Client, error) {
	ctx := context.Background()

	if err := initLLMLogger(); err != nil {
		slog.Error("Failed to initialize LLM logger", "error", err)
		return nil, util.WrapError(ctx, err, "failed to initialize LLM logger")
	}

	// 启动时初始化全局 LLM 调用超时运行时值（覆盖未经 LoadConfig 的直接
	// 构造路径；仅当配置值 > 0 时生效，TUI 模型设置面板可动态修改）。
	if cfg.LLM.Timeout > 0 {
		config.SetGlobalLLMTimeout(cfg.LLM.Timeout)
	}
	config.SetGlobalDeferredTools(cfg.Tools.Deferred)
	SetInFlightLimit(cfg.LLM.MaxInFlightRequests)

	defaultProvider, err := cfg.ResolveProvider("", "")
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

	engine := NewEngine(defaultProvider, cfg.LLM)
	loggingEngine := &LoggingEngine{inner: engine}

	client := &Client{
		Engine:                 loggingEngine,
		Config:                 cfg,
		engines:                make(map[string]Engine),
		agentProviderOverrides: make(map[string]string),
		toolProviderOverrides:  make(map[string]string),
	}
	// 启动时计算并缓存模型三档分类（候选池 = 配置中的所有 provider）。
	client.BuildRouter()
	return client, nil
}

func (c *Client) getOrCreateEngine(provider *config.ProviderConfig, providerName string) Engine {
	c.mu.RLock()
	if eng, ok := c.engines[providerName]; ok {
		c.mu.RUnlock()
		return eng
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if eng, ok := c.engines[providerName]; ok {
		return eng
	}

	slog.Info("Creating engine for provider", "provider", providerName, "model", provider.Model)
	engine := NewEngine(provider, c.Config.LLM)
	loggingEngine := &LoggingEngine{inner: engine}

	var finalEngine Engine = loggingEngine
	if c.Config.LLM.EnableFallback && len(provider.FallbackProviders) > 0 {
		slog.Info("Fallback enabled, creating FallbackEngine", "provider", providerName)
		finalEngine = NewFallbackEngine(
			loggingEngine,
			providerName,
			provider.FallbackProviders,
			c.Config.Global.LLM.Providers,
			c.Config.LLM,
		)
	}

	c.engines[providerName] = finalEngine
	return finalEngine
}

func (c *Client) SetAgentProvider(agentName, providerName string) error {
	_, err := c.Config.GetProvider(providerName)
	if err != nil {
		return fmt.Errorf("SetAgentProvider: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.agentProviderOverrides == nil {
		c.agentProviderOverrides = make(map[string]string)
	}
	c.agentProviderOverrides[agentName] = providerName
	slog.Info("Set runtime agent provider override", "agent", agentName, "provider", providerName)
	return nil
}

func (c *Client) GetAllAgentOverrides() map[string]string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make(map[string]string, len(c.agentProviderOverrides))
	for k, v := range c.agentProviderOverrides {
		result[k] = v
	}
	return result
}

func (c *Client) GetAgentProvider(agentName string) (providerName string, modelName string) {
	c.mu.RLock()
	override, hasOverride := c.agentProviderOverrides[agentName]
	c.mu.RUnlock()

	if hasOverride {
		provider, err := c.Config.GetProvider(override)
		if err == nil {
			return override, provider.Model
		}
	}

	provider, err := c.Config.ResolveProvider(agentName, "")
	if err != nil {
		return "", ""
	}
	for name, p := range c.Config.Global.LLM.Providers {
		if p.APIBaseURL == provider.APIBaseURL && p.Model == provider.Model {
			return name, provider.Model
		}
	}
	return "unknown", provider.Model
}

func (c *Client) SetToolProvider(toolName, providerName string) error {
	if _, err := c.Config.GetProvider(providerName); err != nil {
		return fmt.Errorf("SetToolProvider: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.toolProviderOverrides == nil {
		c.toolProviderOverrides = make(map[string]string)
	}
	c.toolProviderOverrides[toolName] = providerName
	slog.Info("Set runtime tool provider override", "tool", toolName, "provider", providerName)
	return nil
}

func (c *Client) GetToolProvider(toolName string) (providerName string, modelName string, hasOverride bool) {
	c.mu.RLock()
	override, hasOverride := c.toolProviderOverrides[toolName]
	c.mu.RUnlock()

	if hasOverride {
		if provider, err := c.Config.GetProvider(override); err == nil {
			return override, provider.Model, true
		}
	}

	return "", "", false
}

func (c *Client) GetToolProviderInfo(toolName string) (providerName string, modelName string) {
	c.mu.RLock()
	override, hasOverride := c.toolProviderOverrides[toolName]
	c.mu.RUnlock()

	if hasOverride {
		if provider, err := c.Config.GetProvider(override); err == nil {
			return override, provider.Model
		}
	}

	provider, err := c.Config.ResolveProvider("", toolName)
	if err != nil {
		return "", ""
	}
	for name, p := range c.Config.Global.LLM.Providers {
		if p.APIBaseURL == provider.APIBaseURL && p.Model == provider.Model {
			return name, provider.Model
		}
	}
	return "unknown", provider.Model
}

func (c *Client) PersistProviderChoice(kind, name, provider string) error {
	if c.ConfigPath == "" {
		return fmt.Errorf("PersistProviderChoice: config path is empty (set Client.ConfigPath to enable persistence)")
	}

	var section string
	switch kind {
	case "global":
		section = "global.llm"
	case "agent":
		if name == "" {
			return fmt.Errorf("PersistProviderChoice: agent name is empty")
		}
		section = "agents.llm." + name
	case "tool":
		if name == "" {
			return fmt.Errorf("PersistProviderChoice: tool name is empty")
		}
		section = "tools.llm." + name
	default:
		return fmt.Errorf("PersistProviderChoice: unknown kind %q", kind)
	}

	if err := config.PersistUseProvider(c.ConfigPath, section, provider); err != nil {
		return fmt.Errorf("PersistProviderChoice: %w", err)
	}
	slog.Info("Persisted provider choice to config file",
		"kind", kind, "name", name, "provider", provider, "config_path", c.ConfigPath)
	return nil
}

// SetRoutingMode 在运行时切换自动路由模式（"auto" | "fixed"），更新内存配置并
// 重建路由器。不写配置文件（持久化用 PersistRoutingMode）。
func (c *Client) SetRoutingMode(mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "auto" && mode != "fixed" {
		return fmt.Errorf("SetRoutingMode: invalid mode %q (want auto|fixed)", mode)
	}
	c.mu.Lock()
	c.Config.Routing.Mode = mode
	c.mu.Unlock()

	c.BuildRouter()
	slog.Info("Routing mode switched", "mode", mode, "enabled", c.GetRouter().Enabled())
	return nil
}

// PersistRoutingMode 将当前路由模式写回 config.toml 的 [routing] 段。
func (c *Client) PersistRoutingMode(mode string) error {
	if c.ConfigPath == "" {
		return fmt.Errorf("PersistRoutingMode: config path is empty (set Client.ConfigPath to enable persistence)")
	}
	if err := config.PersistRoutingMode(c.ConfigPath, mode); err != nil {
		return fmt.Errorf("PersistRoutingMode: %w", err)
	}
	slog.Info("Persisted routing mode to config file", "mode", mode, "config_path", c.ConfigPath)
	return nil
}

// SetLLMTimeout 运行时更新 LLM API 调用超时：写入内存配置（mu 保护）并同步
// 全局运行时值（config 包 atomic），对所有引擎/执行层立即生效。
// 不写配置文件（持久化用 PersistLLMTimeout）。
func (c *Client) SetLLMTimeout(d time.Duration) error {
	if d <= 0 {
		return fmt.Errorf("SetLLMTimeout: invalid timeout %s (must be positive)", d)
	}
	c.mu.Lock()
	c.Config.LLM.Timeout = d
	c.mu.Unlock()
	config.SetGlobalLLMTimeout(d)
	slog.Info("LLM call timeout updated", "timeout", config.FormatLLMTimeout(d))
	return nil
}

// PersistLLMTimeout 将 LLM 调用超时写回 config.toml 的 [llm] 段 timeout 键。
func (c *Client) PersistLLMTimeout(d time.Duration) error {
	if c.ConfigPath == "" {
		return fmt.Errorf("PersistLLMTimeout: config path is empty (set Client.ConfigPath to enable persistence)")
	}
	if err := config.PersistLLMTimeout(c.ConfigPath, d); err != nil {
		return fmt.Errorf("PersistLLMTimeout: %w", err)
	}
	slog.Info("Persisted LLM timeout to config file", "timeout", config.FormatLLMTimeout(d), "config_path", c.ConfigPath)
	return nil
}

// overrideEngine 返回该 agent 的运行时 provider 覆盖对应的引擎，没有覆盖时返回 nil。
func (c *Client) overrideEngine(agentName string) Engine {
	c.mu.RLock()
	override, hasOverride := c.agentProviderOverrides[agentName]
	c.mu.RUnlock()
	if !hasOverride {
		return nil
	}
	provider, err := c.Config.GetProvider(override)
	if err != nil {
		slog.Warn("Runtime agent override provider not found, falling back to config",
			"agent", agentName, "provider", override, "error", err)
		return nil
	}
	return c.getOrCreateEngine(provider, override)
}

// HasAgentOverride 报告该 agent 是否被设了运行时 provider 覆盖。
func (c *Client) HasAgentOverride(agentName string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, has := c.agentProviderOverrides[agentName]
	return has
}

func (c *Client) GetAgentEngine(agentName string) Engine {
	if eng := c.overrideEngine(agentName); eng != nil {
		return eng
	}

	provider, err := c.Config.ResolveProvider(agentName, "")
	if err != nil {
		slog.Warn("Failed to resolve agent provider, falling back to default", "agent", agentName, "error", err)
		return c.DefaultEngine()
	}

	providerName := c.resolveProviderName(provider)
	if providerName == "" {
		return c.DefaultEngine()
	}

	defaultProvider, defaultErr := c.Config.ResolveProvider("", "")
	if defaultErr == nil && providersEqual(provider, defaultProvider) {
		return c.DefaultEngine()
	}

	return c.getOrCreateEngine(provider, providerName)
}

func (c *Client) GetToolEngine(toolName string) Engine {
	c.mu.RLock()
	override, hasOverride := c.toolProviderOverrides[toolName]
	c.mu.RUnlock()

	if hasOverride {
		provider, err := c.Config.GetProvider(override)
		if err == nil {
			return c.getOrCreateEngine(provider, override)
		}
		slog.Warn("Runtime tool override provider not found, falling back to config",
			"tool", toolName, "provider", override, "error", err)
	}

	provider, err := c.Config.ResolveProvider("", toolName)
	if err != nil {
		slog.Warn("Failed to resolve tool provider, falling back to default", "tool", toolName, "error", err)
		return c.DefaultEngine()
	}

	providerName := c.resolveProviderName(provider)
	if providerName == "" {
		return c.DefaultEngine()
	}

	defaultProvider, defaultErr := c.Config.ResolveProvider("", "")
	if defaultErr == nil && providersEqual(provider, defaultProvider) {
		return c.DefaultEngine()
	}

	return c.getOrCreateEngine(provider, providerName)
}

func (c *Client) resolveProviderName(provider *config.ProviderConfig) string {
	for name, p := range c.Config.Global.LLM.Providers {
		if p.APIBaseURL == provider.APIBaseURL && p.Model == provider.Model {
			return name
		}
	}
	return ""
}

func providersEqual(a, b *config.ProviderConfig) bool {
	if a == nil || b == nil {
		return false
	}
	return a.APIBaseURL == b.APIBaseURL && a.APIKey == b.APIKey && a.Model == b.Model
}

// DefaultEngine 锁内返回默认引擎：SwitchProvider 会在 TUI 线程整体替换它，而 agent 线程随时在读。
func (c *Client) DefaultEngine() Engine {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.Engine
}

func (c *Client) SwitchProvider(providerName string) error {
	provider, err := c.Config.GetProvider(providerName)
	if err != nil {
		return fmt.Errorf("SwitchProvider: %w", err)
	}

	if provider.APIKey == "" {
		return fmt.Errorf("SwitchProvider: API key is empty for provider '%s'", providerName)
	}

	slog.Info("Switching default provider",
		"provider", providerName,
		"model", provider.Model,
		"api_base_url", provider.APIBaseURL)

	engine := NewEngine(provider, c.Config.LLM)
	loggingEngine := &LoggingEngine{inner: engine}

	c.mu.Lock()
	c.Engine = loggingEngine
	c.engines = make(map[string]Engine)
	c.mu.Unlock()

	return nil
}

func (c *Client) GetCurrentProviderInfo() (providerName string, modelName string) {
	provider, err := c.Config.ResolveProvider("", "")
	if err != nil {
		return "unknown", "unknown"
	}
	for name, p := range c.Config.Global.LLM.Providers {
		if p.APIBaseURL == provider.APIBaseURL && p.Model == provider.Model {
			return name, provider.Model
		}
	}
	return "unknown", provider.Model
}
