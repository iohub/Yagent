package config

import (
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type ProviderConfig struct {
	Model         string  `toml:"model"`
	Temperature   float64 `toml:"temperature"`
	MaxTokens     int     `toml:"max_tokens"`
	ContextWindow int     `toml:"context_window"`
	APIBaseURL    string  `toml:"api_base_url"`
	APIKey        string  `toml:"api_key"`
	AWSRegion     string  `toml:"aws_region,omitempty"`
	AWSProfile    string  `toml:"aws_profile,omitempty"`
	ModelProvider string  `toml:"model_provider,omitempty"`

	ReasoningEffort string `toml:"reasoning_effort,omitempty"`

	ApiFormat string `toml:"api_format,omitempty"`

	FallbackProviders []FallbackProvider `toml:"fallback_providers,omitempty"`
}

func (p *ProviderConfig) IsAnthropic() bool {
	return strings.EqualFold(p.ApiFormat, "anthropic")
}

func (p *ProviderConfig) GetSortedFallbackProviders(allProviders map[string]ProviderConfig) []FallbackProvider {
	if len(p.FallbackProviders) == 0 {
		return nil
	}
	sorted := make([]FallbackProvider, 0, len(p.FallbackProviders))
	hasWeight := false
	for _, fp := range p.FallbackProviders {
		if _, exists := allProviders[fp.Provider]; !exists {
			continue
		}
		if fp.Weight != 0 {
			hasWeight = true
		}
		sorted = append(sorted, fp)
	}
	if hasWeight {
		sort.SliceStable(sorted, func(i, j int) bool {
			return sorted[i].Weight > sorted[j].Weight
		})
	}
	return sorted
}

type FallbackProvider struct {
	Provider string `toml:"provider"`
	Weight   int    `toml:"weight"`
}

func (fp *FallbackProvider) UnmarshalText(text []byte) error {
	fp.Provider = string(text)
	fp.Weight = 0
	return nil
}

type AppConfig struct {
	EnableStreaming bool `toml:"enable_streaming"`
}

type AgentConfig struct {
	YoloMode         bool `toml:"yolo_mode"`
	FullYoloMode     bool `toml:"full_yolo_mode"`
	DirectorMaxSteps int  `toml:"director_max_steps"`
	CodingMaxSteps   int  `toml:"coding_max_steps"`
	ChatMaxSteps     int  `toml:"chat_max_steps"`
	RepoMaxSteps     int  `toml:"repo_max_steps"`
	DevOpsMaxSteps   int  `toml:"devops_max_steps"`
	BrowserMaxSteps  int  `toml:"browser_max_steps"`
	MetaMaxSteps     int  `toml:"meta_max_steps"`
	MetaRetryCount   int  `toml:"meta_retry_count"`

	DelegateIdleTimeout time.Duration `toml:"delegate_idle_timeout" json:"delegate_idle_timeout" yaml:"delegate_idle_timeout"`

	DelegateTotalTimeout time.Duration `toml:"delegate_total_timeout" json:"delegate_total_timeout" yaml:"delegate_total_timeout"`

	MaxParallelReadOnlyTools int `toml:"max_parallel_readonly_tools" json:"max_parallel_readonly_tools" yaml:"max_parallel_readonly_tools"`

	SpeakLang string `toml:"lang"`
}

type GitCheckpointConfig struct {
	Enabled               bool   `toml:"enabled"`
	AutoCheckpoint        bool   `toml:"auto_checkpoint"`
	CheckpointInterval    int    `toml:"checkpoint_interval"`
	MaxCheckpoints        int    `toml:"max_checkpoints"`
	SquashOnExit          bool   `toml:"squash_on_exit"`
	GenerateCommitMessage bool   `toml:"generate_commit_message"`
	AgentBranchPrefix     string `toml:"agent_branch_prefix"`
	CheckpointTagPrefix   string `toml:"checkpoint_tag_prefix"`
	StashDirtyWorktree    bool   `toml:"stash_dirty_worktree"`
	CleanupAgentBranch    bool   `toml:"cleanup_agent_branch"`
	CleanupCheckpointTags bool   `toml:"cleanup_checkpoint_tags"`
	AutoMergeOnExit       bool   `toml:"auto_merge_on_exit"`
}

type GlobalLLMConfig struct {
	UseProvider string                    `toml:"use_provider"`
	Providers   map[string]ProviderConfig `toml:"providers"`
}

type AgentLLMOverride struct {
	UseProvider string `toml:"use_provider"`
}

type AgentsLLMConfig struct {
	UseProvider string            `toml:"use_provider"`
	Director    *AgentLLMOverride `toml:"director,omitempty"`
	Coding      *AgentLLMOverride `toml:"coding,omitempty"`
	Repo        *AgentLLMOverride `toml:"repo,omitempty"`
	Chat        *AgentLLMOverride `toml:"chat,omitempty"`
	Meta        *AgentLLMOverride `toml:"meta,omitempty"`
	DevOps      *AgentLLMOverride `toml:"devops,omitempty"`
}

type ToolLLMOverride struct {
	UseProvider string `toml:"use_provider"`
}

type ToolsLLMConfig struct {
	UseProvider  string           `toml:"use_provider"`
	MicroAgent   *ToolLLMOverride `toml:"micro_agent,omitempty"`
	Thinking     *ToolLLMOverride `toml:"thinking,omitempty"`
	DeepThinking *ToolLLMOverride `toml:"deepthinking,omitempty"`
	Design       *ToolLLMOverride `toml:"design,omitempty"`
}

type TopLevelConfig struct {
	LLM *GlobalLLMConfig `toml:"llm"`
}

type AgentsConfig struct {
	LLM AgentsLLMConfig `toml:"llm"`
}

type ToolsConfig struct {
	LLM      ToolsLLMConfig      `toml:"llm"`
	Deferred DeferredToolsConfig `toml:"deferred"`
}

type LLMConfig struct {
	Timeout time.Duration `toml:"timeout" json:"timeout" yaml:"timeout"`

	MaxRetries int `toml:"max_retries" json:"max_retries" yaml:"max_retries"`

	StepRetries int `toml:"step_retries" json:"step_retries" yaml:"step_retries"`

	CircuitBreakerThreshold int `toml:"circuit_breaker_threshold" json:"circuit_breaker_threshold" yaml:"circuit_breaker_threshold"`

	CircuitBreakerResetTimeout time.Duration `toml:"circuit_breaker_reset_timeout" json:"circuit_breaker_reset_timeout" yaml:"circuit_breaker_reset_timeout"`

	EnableFallback bool `toml:"enable_fallback" json:"enable_fallback" yaml:"enable_fallback"`

	FallbackMaxRetries int `toml:"fallback_max_retries" json:"fallback_max_retries" yaml:"fallback_max_retries"`
}

// RoutingConfig 控制模型自动路由（[routing] 节）。
type RoutingConfig struct {
	// Mode 为 "auto"（默认，空值等同 auto）或 "fixed"（关闭路由，沿用各 provider 配置）。
	Mode string `toml:"mode"`
	// Baselines 覆盖各 agent 的角色基线档位，键为 agent 名，值为 "fast"|"balanced"|"high"。
	Baselines map[string]string `toml:"baselines"`
}

// IsAuto 报告是否启用自动路由（空 Mode 默认按 auto 处理）。
func (r RoutingConfig) IsAuto() bool {
	return !strings.EqualFold(strings.TrimSpace(r.Mode), "fixed")
}

type Config struct {
	Global      TopLevelConfig `toml:"global"`
	Agents      AgentsConfig   `toml:"agents"`
	Tools       ToolsConfig    `toml:"tools"`
	App         AppConfig      `toml:"app"`
	Agent       AgentConfig    `toml:"agent"`
	LLM         LLMConfig      `toml:"llm" json:"llm" yaml:"llm"`
	Routing     RoutingConfig  `toml:"routing"`
	Keywords    KeywordsConfig `toml:"keywords"`
	TaskTimeout time.Duration  `toml:"task_timeout" json:"task_timeout" yaml:"task_timeout"`

	GitCheckpoint GitCheckpointConfig `toml:"git_checkpoint"`

	CodeSeek CodeSeekConfig `toml:"codeseek"`

	Browser BrowserConfig `toml:"browser"`

	Blackboard BlackboardConfig `toml:"blackboard"`

	EnhancedCommander EnhancedCommanderConfig `toml:"enhanced_commander" json:"enhanced_commander"`

	TUI TUIConfig `toml:"tui"`

	// browserSectionDefined 记录配置文件里是否出现过 [browser] 段（由 LoadFromFile 写入），
	// 用于区分"用户显式配置"与"整表缺省"。
	browserSectionDefined bool

	// blackboardSectionDefined 同理，记录 [blackboard] 段是否被显式写过。
	blackboardSectionDefined bool
}

// staleKeysIn 列出某个表里没被当前 schema 解析掉的键。
func staleKeysIn(md toml.MetaData, section string) []string {
	var out []string
	for _, key := range md.Undecoded() {
		if len(key) > 0 && key[0] == section {
			out = append(out, strings.Join(key, "."))
		}
	}
	return out
}

// staleBrowserKeys 列出 [browser] 段里没被新 schema 解析掉的键。
// 被删掉的旧浏览器栈配置（headless / browser_path 等）会落在这里。
func staleBrowserKeys(md toml.MetaData) []string {
	var out []string
	for _, key := range md.Undecoded() {
		if len(key) > 0 && key[0] == "browser" {
			out = append(out, strings.Join(key, "."))
		}
	}
	return out
}

// sectionHasDecodedKey 判断某个表里是否至少有一个**被当前 schema 认识**的键。
// 只剩失效键的段落等同于没写过：那种情况按整表缺省处理，
// 否则某个 bool 的零值会把新能力静默关掉（[browser] enabled 踩过这个坑）。
func sectionHasDecodedKey(md toml.MetaData, section string) bool {
	undecoded := make(map[string]bool)
	for _, key := range md.Undecoded() {
		undecoded[strings.Join(key, ".")] = true
	}
	for _, key := range md.Keys() {
		// len>1：跳过表头本身（如 `[browser]`），只看段内的字段
		if len(key) > 1 && key[0] == section && !undecoded[strings.Join(key, ".")] {
			return true
		}
	}
	return false
}

func browserSectionConfigured(md toml.MetaData) bool {
	return sectionHasDecodedKey(md, "browser")
}

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

func (c *Config) resolveAgentProvider(agentName string) string {
	if override := c.getAgentOverride(agentName); override != nil && override.UseProvider != "" {
		return override.UseProvider
	}
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

func (c *Config) resolveToolProvider(toolName string) string {
	if override := c.getToolOverride(toolName); override != nil && override.UseProvider != "" {
		return override.UseProvider
	}
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
	case "design":
		return c.Tools.LLM.Design
	default:
		return nil
	}
}

func (c *Config) ResolveProvider(agentName, toolName string) (*ProviderConfig, error) {
	if toolName != "" {
		if name := c.resolveToolProvider(toolName); name != "" {
			return c.GetProvider(name)
		}
	}

	if agentName != "" {
		if name := c.resolveAgentProvider(agentName); name != "" {
			return c.GetProvider(name)
		}
	}

	if c.Global.LLM != nil && c.Global.LLM.UseProvider != "" {
		return c.GetProvider(c.Global.LLM.UseProvider)
	}

	return nil, fmt.Errorf("no LLM provider configured")
}

func (c *Config) GetProviderNames() []string {
	if c.Global.LLM == nil {
		return []string{}
	}
	names := make([]string, 0, len(c.Global.LLM.Providers))
	for name := range c.Global.LLM.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func DetectBedrockProvider(modelID string) string {
	modelID = strings.ToLower(modelID)

	if strings.Contains(modelID, ".nova-") {
		return "amazon"
	}

	if strings.Contains(modelID, "anthropic") {
		return "anthropic"
	}

	if strings.Contains(modelID, "meta") {
		return "meta"
	}

	if strings.Contains(modelID, "cohere") {
		return "cohere"
	}

	if strings.Contains(modelID, "ai21") {
		return "ai21"
	}

	return "amazon"
}

func LoadConfig(path string) (*Config, error) {
	return LoadFromFile(path)
}

// LoadFromFile 从文件加载配置（提取自 LoadConfig，不执行 validate）
func LoadFromFile(path string) (*Config, error) {
	cfg := &Config{}
	md, err := toml.DecodeFile(path, cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to decode config file: %w", err)
	}
	// 老配置没有 [browser] 段（或只剩旧浏览器栈的失效键）时按"整表缺省"填默认值：
	// enabled 的零值是 false，靠它判断等于把新能力静默关掉。
	cfg.browserSectionDefined = browserSectionConfigured(md)
	if stale := staleBrowserKeys(md); len(stale) > 0 {
		// 旧浏览器栈留下的 [browser] 键（headless / browser_path 等）在新 schema 里不存在，
		// 静默忽略会让用户以为自己的配置还在生效。
		slog.Warn("[browser] 段存在已失效的键，将被忽略；请按新的 [browser] 配置改写",
			"stale_keys", strings.Join(stale, ", "),
			"hint", "headless→mode，browser_path→chrome_path，新栈还需 binary_path 指向 browser-control")
	}
	// [blackboard] 里有三个键允许显式取 0，且各有语义（关索引注入 / 不裁剪索引行 / 不清理），
	// 而"没写这一段"解析出来同样是 0 —— 所以缺省判定必须看键的存在性，不能看零值。
	cfg.blackboardSectionDefined = sectionHasDecodedKey(md, "blackboard")
	if stale := staleKeysIn(md, "blackboard"); len(stale) > 0 {
		slog.Warn("[blackboard] 段存在不认识的键，将被忽略",
			"stale_keys", strings.Join(stale, ", "),
			"hint", blackboardKeyHint)
	}
	SetGlobalBlackboard(cfg.Blackboard, BlackboardPresence{
		Section:        cfg.blackboardSectionDefined,
		IndexMaxNotes:  md.IsDefined("blackboard", "index_max_notes"),
		IndexLineChars: md.IsDefined("blackboard", "index_line_chars"),
		TTL:            md.IsDefined("blackboard", "ttl"),
	})
	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}
	return cfg, nil
}

func (c *Config) resolveEffectiveProviderName() string {
	if c.Tools.LLM.UseProvider != "" {
		return c.Tools.LLM.UseProvider
	}
	if c.Agents.LLM.UseProvider != "" {
		return c.Agents.LLM.UseProvider
	}
	if c.Global.LLM != nil && c.Global.LLM.UseProvider != "" {
		return c.Global.LLM.UseProvider
	}
	return ""
}

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

	if activeProvider.Model == "" {
		return fmt.Errorf("model must be specified for provider '%s'", effectiveProvider)
	}

	if strings.HasPrefix(effectiveProvider, "bedrock") {
		if activeProvider.AWSRegion == "" {
			return fmt.Errorf("aws_region must be specified for Bedrock provider")
		}
		return nil
	}

	if activeProvider.APIKey == "" {
		return fmt.Errorf("api_key must be specified for provider '%s'", effectiveProvider)
	}

	if activeProvider.APIBaseURL == "" {
		return fmt.Errorf("api_base_url must be specified for provider '%s'", effectiveProvider)
	}

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

	if c.Agent.DelegateIdleTimeout < 0 {
		c.Agent.DelegateIdleTimeout = 0
	}
	if c.Agent.DelegateTotalTimeout < 0 {
		c.Agent.DelegateTotalTimeout = 0
	}
	if c.Agent.DelegateIdleTimeout > 0 && c.LLM.Timeout > 0 && c.Agent.DelegateIdleTimeout <= c.LLM.Timeout {
		slog.Warn("delegate_idle_timeout 不大于 LLM 调用超时，存在正常长推理被误杀的风险，建议 ≥ max(10m, llm_timeout+5m)",
			"delegate_idle_timeout", c.Agent.DelegateIdleTimeout,
			"llm_timeout", c.LLM.Timeout,
		)
	}
	if c.Agent.DelegateTotalTimeout > 0 {
		idleThreshold := c.Agent.DelegateIdleTimeout
		if idleThreshold == 0 {
			idleThreshold = DeriveDelegateIdleTimeout(c.LLM.Timeout)
		}
		if c.Agent.DelegateTotalTimeout < idleThreshold {
			slog.Warn("delegate_total_timeout 小于 delegate_idle_timeout，总上限将先于空闲阈值触发",
				"delegate_total_timeout", c.Agent.DelegateTotalTimeout,
				"delegate_idle_timeout", c.Agent.DelegateIdleTimeout,
			)
		}
	}

	llmDefaults := DefaultLLMConfig()
	if c.LLM.MaxRetries == 0 {
		c.LLM.MaxRetries = llmDefaults.MaxRetries
	}
	if c.LLM.CircuitBreakerResetTimeout == 0 && c.LLM.CircuitBreakerThreshold > 0 {
		c.LLM.CircuitBreakerResetTimeout = 60 * time.Second
	}
	if c.LLM.FallbackMaxRetries == 0 {
		c.LLM.FallbackMaxRetries = c.LLM.MaxRetries
	}

	if !c.hasKeywordsConfig() {
		homeDir, _ := os.UserHomeDir()
		c.Keywords.DefaultPath = homeDir + "/.yagent/keywords.txt"
		c.Keywords.HotReload = false
		c.Keywords.Dicts = []DictConfig{
			{
				Name:        "autocomplete",
				Files:       []string{},
				Type:        DictTypePrefix,
				BuiltinType: "default",
			},
		}
	}

	if c.EnhancedCommander.MaxDelegationDepth == 0 {
		c.EnhancedCommander.MaxDelegationDepth = 3
	}
	if c.EnhancedCommander.ToolResultKeepTokens == 0 {
		c.EnhancedCommander.ToolResultKeepTokens = 200
	}
	if !c.EnhancedCommander.EnableUltimateCompression {
		c.EnhancedCommander.EnableUltimateCompression = true
	}

	if !c.browserSectionDefined {
		c.Browser = DefaultBrowserConfig()
	} else {
		c.Browser.applyDefaults()
	}
	// 执行层会把这些下限当作保底，配置得更小时只会告警并按下限生效。
	if c.Browser.CDPTimeout < MinBrowserCDPTimeout {
		slog.Warn("browser.cdp_timeout 低于保底下限，将按下限生效（需覆盖最长 wait 60s）",
			"configured", c.Browser.CDPTimeout, "floor", MinBrowserCDPTimeout)
		c.Browser.CDPTimeout = MinBrowserCDPTimeout
	}
	if c.Browser.CommandTimeout <= c.Browser.CDPTimeout {
		slog.Warn("browser.command_timeout 不大于 cdp_timeout，会被抬高到 cdp_timeout+10s（否则外层判不到上游超时）",
			"configured", c.Browser.CommandTimeout, "cdp_timeout", c.Browser.CDPTimeout)
		c.Browser.CommandTimeout = c.Browser.CDPTimeout + 10*time.Second
	}

	gitCfgDefaults := DefaultGitCheckpointConfig()
	if c.GitCheckpoint.AgentBranchPrefix == "" {
		c.GitCheckpoint = gitCfgDefaults
	} else {
		if c.GitCheckpoint.CheckpointInterval == 0 {
			c.GitCheckpoint.CheckpointInterval = gitCfgDefaults.CheckpointInterval
		}
		if c.GitCheckpoint.MaxCheckpoints == 0 {
			c.GitCheckpoint.MaxCheckpoints = gitCfgDefaults.MaxCheckpoints
		}
	}

	if c.TUI.Keybindings.Edit.SubmitTask == "" {
		c.TUI.Keybindings.Edit.SubmitTask = "alt+s"
	}
	if c.TUI.Keybindings.Edit.CommandMode == "" {
		c.TUI.Keybindings.Edit.CommandMode = "ctrl+e"
	}
	if c.TUI.Keybindings.Edit.ToggleHelp == "" {
		c.TUI.Keybindings.Edit.ToggleHelp = "ctrl+h"
	}
	if c.TUI.Keybindings.Edit.ToggleTimeline == "" {
		c.TUI.Keybindings.Edit.ToggleTimeline = "ctrl+l"
	}
	if c.TUI.Keybindings.Edit.PageDown == "" {
		c.TUI.Keybindings.Edit.PageDown = "ctrl+f"
	}
	if c.TUI.Keybindings.Edit.PageUp == "" {
		c.TUI.Keybindings.Edit.PageUp = "ctrl+b"
	}
	if c.TUI.Keybindings.Edit.Quit == "" {
		c.TUI.Keybindings.Edit.Quit = "ctrl+c"
	}
	if c.TUI.Keybindings.Edit.SwitchModel == "" {
		c.TUI.Keybindings.Edit.SwitchModel = "alt+m"
	}
	if c.TUI.Keybindings.Edit.ToggleDashboard == "" {
		c.TUI.Keybindings.Edit.ToggleDashboard = "alt+d"
	}
	if c.TUI.Keybindings.Edit.NewTab == "" {
		c.TUI.Keybindings.Edit.NewTab = "ctrl+t"
	}
	if c.TUI.Keybindings.Edit.PrevTab == "" {
		c.TUI.Keybindings.Edit.PrevTab = "alt+["
	}
	if c.TUI.Keybindings.Edit.NextTab == "" {
		c.TUI.Keybindings.Edit.NextTab = "alt+]"
	}
	if c.TUI.Keybindings.Edit.ClearSession == "" {
		c.TUI.Keybindings.Edit.ClearSession = "alt+c"
	}
	if c.TUI.Keybindings.Edit.CloseTab == "" {
		c.TUI.Keybindings.Edit.CloseTab = "alt+w"
	}
	for i := 1; i <= 9; i++ {
		var ptr *string
		switch i {
		case 1:
			ptr = &c.TUI.Keybindings.Edit.Tab1
		case 2:
			ptr = &c.TUI.Keybindings.Edit.Tab2
		case 3:
			ptr = &c.TUI.Keybindings.Edit.Tab3
		case 4:
			ptr = &c.TUI.Keybindings.Edit.Tab4
		case 5:
			ptr = &c.TUI.Keybindings.Edit.Tab5
		case 6:
			ptr = &c.TUI.Keybindings.Edit.Tab6
		case 7:
			ptr = &c.TUI.Keybindings.Edit.Tab7
		case 8:
			ptr = &c.TUI.Keybindings.Edit.Tab8
		case 9:
			ptr = &c.TUI.Keybindings.Edit.Tab9
		}
		if *ptr == "" {
			*ptr = fmt.Sprintf("alt+%d", i)
		}
	}

	if c.TUI.Keybindings.Command.ScrollDown == "" {
		c.TUI.Keybindings.Command.ScrollDown = "j"
	}
	if c.TUI.Keybindings.Command.ScrollUp == "" {
		c.TUI.Keybindings.Command.ScrollUp = "k"
	}
	if c.TUI.Keybindings.Command.PageDown == "" {
		c.TUI.Keybindings.Command.PageDown = "f"
	}
	if c.TUI.Keybindings.Command.PageUp == "" {
		c.TUI.Keybindings.Command.PageUp = "b"
	}
	if c.TUI.Keybindings.Command.EditMode == "" {
		c.TUI.Keybindings.Command.EditMode = "i"
	}
	if c.TUI.Keybindings.Command.CmdToggleHelp == "" {
		c.TUI.Keybindings.Command.CmdToggleHelp = "?"
	}
	if c.TUI.Keybindings.Command.ToggleTokenPanel == "" {
		c.TUI.Keybindings.Command.ToggleTokenPanel = "alt+t"
	}
	if c.TUI.Keybindings.Command.SwitchModel == "" {
		c.TUI.Keybindings.Command.SwitchModel = "alt+m"
	}
	if c.TUI.Keybindings.Command.ToggleDashboard == "" {
		c.TUI.Keybindings.Command.ToggleDashboard = "alt+d"
	}
	if c.TUI.Keybindings.Command.Quit == "" {
		c.TUI.Keybindings.Command.Quit = "ctrl+c"
	}

	klDefaults := DefaultKnowledgeConfig()
	if !c.CodeSeek.Knowledge.Enabled && klDefaults.Enabled {
		c.CodeSeek.Knowledge.Enabled = klDefaults.Enabled
	}
	if c.CodeSeek.Knowledge.InjectionMaxTokens == 0 {
		c.CodeSeek.Knowledge.InjectionMaxTokens = klDefaults.InjectionMaxTokens
	}
	if c.CodeSeek.Knowledge.InjectionMaxEntries == 0 {
		c.CodeSeek.Knowledge.InjectionMaxEntries = klDefaults.InjectionMaxEntries
	}
	if c.CodeSeek.Knowledge.InjectionMinScore == 0 {
		c.CodeSeek.Knowledge.InjectionMinScore = klDefaults.InjectionMinScore
	}
	if !c.CodeSeek.Knowledge.InjectionRerank && klDefaults.InjectionRerank {
		c.CodeSeek.Knowledge.InjectionRerank = klDefaults.InjectionRerank
	}

	return nil
}

func (c *Config) hasKeywordsConfig() bool {
	if len(c.Keywords.Dicts) > 0 {
		return true
	}
	if c.Keywords.DefaultPath != "" {
		return true
	}
	return false
}

type EnhancedCommanderConfig struct {
	Enable bool `toml:"enable" json:"enable"`

	MaxDelegationDepth int `toml:"max_delegation_depth" json:"max_delegation_depth"`

	EnableContextCompression bool `toml:"enable_context_compression" json:"enable_context_compression"`

	ContextCompressionThreshold int `toml:"context_compression_threshold" json:"context_compression_threshold"`

	ToolResultKeepTokens int `toml:"tool_result_keep_tokens" json:"tool_result_keep_tokens"`

	EnableUltimateCompression bool `toml:"enable_ultimate_compression" json:"enable_ultimate_compression"`

	UltimateCompressionKeepPlans int `toml:"ultimate_compression_keep_plans" json:"ultimate_compression_keep_plans"`
}

type CodeSeekConfig struct {
	BinaryPath     string          `toml:"binary_path"`
	MCPArgs        []string        `toml:"mcp_args"`
	RequestTimeout int             `toml:"request_timeout"`
	Knowledge      KnowledgeConfig `toml:"knowledge"`
}

// 浏览器超时的保底下限。上游内部 CDP 超时必须覆盖最长 wait（browser_wait 上限 60s），
// 外层单次调用超时又要比它更大，否则抢在内层之前把进程杀掉，模型只看到"命令超时"。
const (
	MinBrowserCDPTimeout     = 65 * time.Second
	MinBrowserCommandTimeout = 75 * time.Second
)

// BrowserConfig 控制 Browser-Agent 如何通过外部 browser-control CLI 驱动 Chrome。
// Go 侧不直接讲 CDP：所有浏览器动作都是 exec 一次 CLI 子命令。
type BrowserConfig struct {
	Enabled bool `toml:"enabled"`

	// BinaryPath 为空时按 ~/.yagent/bin/browser-control → $PATH 依次查找。
	BinaryPath string `toml:"binary_path"`
	// ChromePath 透传给 BROWSER_CONTROL_CHROME；为空则由 CLI 自动探测 Chrome/Chromium。
	ChromePath string `toml:"chrome_path"`

	// Mode: headless | headful | attach。attach 表示只连 CDPURL，不启动浏览器。
	Mode string `toml:"mode"`
	// CDPURL 为 attach 模式的端点，如 http://127.0.0.1:9222。
	CDPURL string `toml:"cdp_url"`
	// CDPPortBase 是自启端口起点，递增找空闲。刻意避开 9222/9223：
	// 上游在没有任何 CDP 环境变量时会硬扫这两个端口，会串到别的浏览器实例上。
	CDPPortBase int `toml:"cdp_port_base"`

	CDPTimeout     time.Duration `toml:"cdp_timeout"`     // → BROWSER_CONTROL_TIMEOUT（上游默认仅 15s）
	CommandTimeout time.Duration `toml:"command_timeout"` // 单次 CLI 调用上限

	IdleTTL   time.Duration `toml:"idle_ttl"`   // 空闲超过它就关掉我们自己启动的 Chrome
	MaxChrome int           `toml:"max_chrome"` // 进程内 Chrome 实例上限

	WorkspaceRoot string        `toml:"workspace_root"` // 空 → ~/.yagent/data/browser
	WorkspaceTTL  time.Duration `toml:"workspace_ttl"`  // 过期工作区清理

	MaxOutputBytes int    `toml:"max_output_bytes"`
	DialogPolicy   string `toml:"dialog_policy"` // accept | dismiss | ask

	AllowedDomains []string `toml:"allowed_domains"`
	DeniedDomains  []string `toml:"denied_domains"`

	ProfileDir   string `toml:"profile_dir"`
	EnableSkills bool   `toml:"enable_skills"`
}

// applyDefaults 逐项兜底零值字段（用户只写了一半的 [browser] 段时仍能工作）。
func (b *BrowserConfig) applyDefaults() {
	d := DefaultBrowserConfig()
	if b.Mode == "" {
		b.Mode = d.Mode
	}
	if b.CDPPortBase <= 0 {
		b.CDPPortBase = d.CDPPortBase
	}
	if b.CDPTimeout <= 0 {
		b.CDPTimeout = d.CDPTimeout
	}
	if b.CommandTimeout <= 0 {
		b.CommandTimeout = d.CommandTimeout
	}
	if b.IdleTTL <= 0 {
		b.IdleTTL = d.IdleTTL
	}
	if b.MaxChrome <= 0 {
		b.MaxChrome = d.MaxChrome
	}
	if b.WorkspaceTTL <= 0 {
		b.WorkspaceTTL = d.WorkspaceTTL
	}
	if b.MaxOutputBytes <= 0 {
		b.MaxOutputBytes = d.MaxOutputBytes
	}
	if b.DialogPolicy == "" {
		b.DialogPolicy = d.DialogPolicy
	}
	if b.DeniedDomains == nil {
		b.DeniedDomains = d.DeniedDomains
	}
}

// DefaultBrowserConfig 是 [browser] 整表缺省时的取值。
// cdp_timeout/command_timeout 取的是执行层保底下限（65s/75s）：
// 上游内部 CDP 超时必须覆盖最长 wait（60s），否则 `wait load 60` 会提前失败，
// 而外层 command_timeout 又要比它更大，否则判定权被内层抢走。
func DefaultBrowserConfig() BrowserConfig {
	return BrowserConfig{
		Enabled:        true,
		Mode:           "headless",
		CDPPortBase:    9333,
		CDPTimeout:     MinBrowserCDPTimeout,
		CommandTimeout: MinBrowserCommandTimeout,
		IdleTTL:        15 * time.Minute,
		MaxChrome:      1,
		WorkspaceTTL:   72 * time.Hour,
		MaxOutputBytes: 24000,
		DialogPolicy:   "accept",
		DeniedDomains: []string{
			"*.local", "localhost", "127.0.0.1", "::1",
			"192.168.0.0/16", "10.0.0.0/8", "172.16.0.0/12", "169.254.0.0/16",
		},
	}
}

type KnowledgeConfig struct {
	Enabled             bool    `toml:"enabled"`
	InjectionMaxTokens  int     `toml:"injection_max_tokens"`
	InjectionMaxEntries int     `toml:"injection_max_entries"`
	InjectionMinScore   float64 `toml:"injection_min_score"`
	InjectionRerank     bool    `toml:"injection_rerank"`
}

const (
	DictTypePrefix = "prefix"
	DictTypeExact  = "exact"
)

type DictConfig struct {
	Name        string   `toml:"name"`
	Files       []string `toml:"files"`
	Type        string   `toml:"type"`
	BuiltinType string   `toml:"builtin_type"`
}

type KeywordsConfig struct {
	DefaultPath       string       `toml:"default_path"`
	HotReload         bool         `toml:"hot_reload"`
	DisableCompletion bool         `toml:"disable_completion"`
	Dicts             []DictConfig `toml:"dict"`
}

type TUIConfig struct {
	Keybindings KeybindingsConfig `toml:"keybindings"`
}

type KeybindingsConfig struct {
	Edit    EditKeybindings    `toml:"edit"`
	Command CommandKeybindings `toml:"command"`
}

type EditKeybindings struct {
	SubmitTask      string `toml:"submit_task"`
	CommandMode     string `toml:"command_mode"`
	ToggleHelp      string `toml:"toggle_help"`
	ToggleTimeline  string `toml:"toggle_timeline"`
	PageDown        string `toml:"page_down"`
	PageUp          string `toml:"page_up"`
	Quit            string `toml:"quit"`
	SwitchModel     string `toml:"switch_model"`
	ToggleDashboard string `toml:"toggle_dashboard"`
	NewTab          string `toml:"new_tab"`
	PrevTab         string `toml:"prev_tab"`
	NextTab         string `toml:"next_tab"`
	ClearSession    string `toml:"clear_session"`
	CloseTab        string `toml:"close_tab"`
	Tab1            string `toml:"tab_1"`
	Tab2            string `toml:"tab_2"`
	Tab3            string `toml:"tab_3"`
	Tab4            string `toml:"tab_4"`
	Tab5            string `toml:"tab_5"`
	Tab6            string `toml:"tab_6"`
	Tab7            string `toml:"tab_7"`
	Tab8            string `toml:"tab_8"`
	Tab9            string `toml:"tab_9"`
}

type CommandKeybindings struct {
	ScrollDown       string `toml:"scroll_down"`
	ScrollUp         string `toml:"scroll_up"`
	PageDown         string `toml:"page_down"`
	PageUp           string `toml:"page_up"`
	EditMode         string `toml:"edit_mode"`
	CmdToggleHelp    string `toml:"toggle_help"`
	ToggleTokenPanel string `toml:"toggle_token_panel"`
	SwitchModel      string `toml:"switch_model"`
	ToggleDashboard  string `toml:"toggle_dashboard"`
	Quit             string `toml:"quit"`
}
