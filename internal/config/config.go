package config

import (
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"
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
}

type TopLevelConfig struct {
	LLM *GlobalLLMConfig `toml:"llm"`
}

type AgentsConfig struct {
	LLM AgentsLLMConfig `toml:"llm"`
}

type ToolsConfig struct {
	LLM ToolsLLMConfig `toml:"llm"`
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

type Config struct {
	Global      TopLevelConfig `toml:"global"`
	Agents      AgentsConfig   `toml:"agents"`
	Tools       ToolsConfig    `toml:"tools"`
	App         AppConfig      `toml:"app"`
	Agent       AgentConfig    `toml:"agent"`
	LLM         LLMConfig      `toml:"llm" json:"llm" yaml:"llm"`
	Browser     BrowserConfig  `toml:"browser"`
	Keywords    KeywordsConfig `toml:"keywords"`
	TaskTimeout time.Duration  `toml:"task_timeout" json:"task_timeout" yaml:"task_timeout"`

	GitCheckpoint GitCheckpointConfig `toml:"git_checkpoint"`

	CodeSeek CodeSeekConfig `toml:"codeseek"`

	EnhancedCommander EnhancedCommanderConfig `toml:"enhanced_commander" json:"enhanced_commander"`

	TUI TUIConfig `toml:"tui"`
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

type BrowserConfig struct {
	Headless           bool     `toml:"headless"`
	BrowserPath        string   `toml:"browser_path"`
	UserDataDir        string   `toml:"user_data_dir"`
	ViewportWidth      int      `toml:"viewport_width"`
	ViewportHeight     int      `toml:"viewport_height"`
	AllowedDomains     []string `toml:"allowed_domains"`
	BlockedDomains     []string `toml:"blocked_domains"`
	TimeoutSeconds     int      `toml:"timeout_seconds"`
	TaskTimeoutSeconds int      `toml:"task_timeout_seconds"`
	MaxConcurrentPages int      `toml:"max_concurrent_pages"`
	AutoLaunch         bool     `toml:"auto_launch"`
	IdleTimeout        string   `toml:"idle_timeout"`
	AllowNoSandbox     bool     `toml:"allow_no_sandbox"`
	ExtraArgs          []string `toml:"extra_args"`
	EnableBrowserAgent bool     `toml:"enable_browser_agent"`
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
