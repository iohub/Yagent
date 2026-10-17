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
	// APIKeyEnv 指向一个环境变量名，用来替代明文 api_key（同一份配置在多台机器上
	// 跑、或不想让密钥进 git 时用）。展开只发生在 validate() 一处，见 ResolveAPIKey。
	APIKeyEnv string `toml:"api_key_env,omitempty"`

	ReasoningEffort string `toml:"reasoning_effort,omitempty"`

	ApiFormat string `toml:"api_format,omitempty"`

	FallbackProviders []FallbackProvider `toml:"fallback_providers,omitempty"`
}

func (p *ProviderConfig) IsAnthropic() bool {
	return strings.EqualFold(p.ApiFormat, "anthropic")
}

// ApplyAPIKeyEnv 把 api_key_env 指向的环境变量展开进 APIKey。
// 展开只在这一个地方发生，所以两个引擎、NewClient 的检查、providersEqual 比较读到的
// 都是同一个字段，不需要每处都记得"先看环境变量"。
// 返回错误表示变量没设置；调用方（validate）只对**生效的那个 provider** 硬失败，
// 其余 provider 告警了事 —— 为另一台机器准备的条目不该让本机启动不了。
func (p *ProviderConfig) ApplyAPIKeyEnv() error {
	if p.APIKeyEnv == "" {
		return nil
	}
	v := os.Getenv(p.APIKeyEnv)
	if v == "" {
		return fmt.Errorf("api_key_env=%s is not set in the environment", p.APIKeyEnv)
	}
	p.APIKey = v
	return nil
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

type AgentConfig struct {
	YoloMode         bool `toml:"yolo_mode"`
	FullYoloMode     bool `toml:"full_yolo_mode"`
	DirectorMaxSteps int  `toml:"director_max_steps"`
	CodingMaxSteps   int  `toml:"coding_max_steps"`
	ChatMaxSteps     int  `toml:"chat_max_steps"`
	RepoMaxSteps     int  `toml:"repo_max_steps"`
	DevOpsMaxSteps   int  `toml:"devops_max_steps"`
	BrowserMaxSteps  int  `toml:"browser_max_steps"`
	MetaRetryCount   int  `toml:"meta_retry_count"`

	DelegateIdleTimeout time.Duration `toml:"delegate_idle_timeout" json:"delegate_idle_timeout" yaml:"delegate_idle_timeout"`

	DelegateTotalTimeout time.Duration `toml:"delegate_total_timeout" json:"delegate_total_timeout" yaml:"delegate_total_timeout"`

	MaxParallelReadOnlyTools int `toml:"max_parallel_readonly_tools" json:"max_parallel_readonly_tools" yaml:"max_parallel_readonly_tools"`

	SpeakLang string `toml:"lang"`
}

type GitCheckpointConfig struct {
	Enabled               bool   `toml:"enabled"`
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
	// Browser 补齐第七个 Agent：以前 [agents.llm.browser] 会被报成 undecoded 键并静默忽略，
	// 于是 Browser 只能靠 agents.llm.use_provider 或全局默认来换 provider。
	Browser *AgentLLMOverride `toml:"browser,omitempty"`
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
	Agent       AgentConfig    `toml:"agent"`
	LLM         LLMConfig      `toml:"llm" json:"llm" yaml:"llm"`
	Routing     RoutingConfig  `toml:"routing"`
	Keywords    KeywordsConfig `toml:"keywords"`
	TaskTimeout time.Duration  `toml:"task_timeout" json:"task_timeout" yaml:"task_timeout"`

	GitCheckpoint GitCheckpointConfig `toml:"git_checkpoint"`

	CodeSeek CodeSeekConfig `toml:"codeseek"`

	Browser BrowserConfig `toml:"browser"`

	Blackboard BlackboardConfig `toml:"blackboard"`

	// Resume 是"停止/继续"（检查点重放）的预算与开关。
	Resume ResumeConfig `toml:"resume"`

	// Blueprint 是仓库蓝图（目录级导航 + Repo 提交的散文）的开关与预算。
	Blueprint BlueprintConfig `toml:"blueprint"`

	EnhancedCommander EnhancedCommanderConfig `toml:"enhanced_commander" json:"enhanced_commander"`

	TUI TUIConfig `toml:"tui"`

	// browserSectionDefined 记录配置文件里是否出现过 [browser] 段（由 LoadFromFile 写入），
	// 用于区分"用户显式配置"与"整表缺省"。
	browserSectionDefined bool

	// blackboardSectionDefined 同理，记录 [blackboard] 段是否被显式写过。
	blackboardSectionDefined bool

	// resumeSectionDefined 同理，记录 [resume] 段是否被显式写过。
	resumeSectionDefined bool

	// blueprintSectionDefined 同理，记录 [blueprint] 段是否被显式写过。
	blueprintSectionDefined bool

	// gitCheckpointSectionDefined 同理。这里必须看段的存在性而不是某个字段是否为空：
	// 老写法是"前缀为空就整段换默认值"，于是 enabled = false 会被顶回 true。
	gitCheckpointSectionDefined bool

	// ultimateCompressionKeyDefined 记录 [enhanced_commander] 里是否显式写过
	// enable_ultimate_compression。该键缺省为"开"，因此"没写"与"写了 false"必须可区分。
	ultimateCompressionKeyDefined bool
}

// removedKeyHints 是已被删除的 TOML 路径 → 该改写什么（空串 = 功能已不存在，什么都不必写）。
// 判据是解码器的 Undecoded()，所以老配置里残留的键会被逐条点名，而不是静默当成没写。
// 键名一律小写，与 toml 解码路径一致。
// 只登记**会出现的最长路径**：父表（[app] 段头、[[keywords.dict]] 的元素）本身不进
// Undecoded()，登记了也永远不响；子键靠上面的向上归并落到父表这一行。
var removedKeyHints = map[string]string{
	"app.enable_streaming":                    "两个引擎恒走流式，这个键从来没有读者",
	"keywords.default_path":                   "词典路径固定为 ~/.yagent/keywords.txt 与 <project>/.yagent/keywords.txt",
	"keywords.hot_reload":                     "热加载代码已移除",
	"keywords.dict":                           "词典名单一路径硬编码，自定义条目没有读者",
	"agent.meta_max_steps":                    "Meta 是一次 LLM 调用，没有 step 循环；只有 meta_retry_count 有意义",
	"enhanced_commander.enable":               "压缩只需 enable_context_compression 一个键（以前要两个布尔同时为真）",
	"enhanced_commander.max_delegation_depth": "委派深度不由配置决定",
	"git_checkpoint.auto_checkpoint":          "检查点由 git_checkpoint_create 工具在里程碑处创建",
	"git_checkpoint.checkpoint_interval":      "自动检查点的钩子已删除",
	"git_checkpoint.max_checkpoints":          "没有裁剪标签或分支的代码读它",
	"tools.deferred.hot":                      "调档只走 toolspec 画像的 Promote/Demote",
	"tools.deferred.cold":                     "调档只走 toolspec 画像的 Promote/Demote",
	"browser.enable_skills":                   "站点 playbook 注入未实现",
	"codeseek.knowledge":                      "知识注入当前硬关闭（injector.go 的 early return）；enabled 也从未生效过 —— 它会被写回 true",
}

// removedProviderKeyHints 作用于 [global.llm.providers.<name>] 段内的死键。
// provider 名是用户自定义的，所以按第四段匹配而不是整条路径。
var removedProviderKeyHints = map[string]string{
	"aws_region":     "本仓库没有 Bedrock 专用客户端：请把 api_base_url + api_key 指向 OpenAI-compatible 端点",
	"aws_profile":    "没有读者",
	"model_provider": "没有读者",
}

// removedKeyReports 把 Undecoded() 里命中移除表的键归并成人话列表。
// 命中父表（如整段删掉的 [codeseek.knowledge]）时，其子键不再重复报。
// 单独可调用是为了让测试能真的断言"这条键被点名了" —— 只写进 slog 的话，
// 没有任何测试能让它失败。
func removedKeyReports(md toml.MetaData) []string {
	reported := make(map[string]bool)
	var lines []string
	for _, key := range md.Undecoded() {
		lower := make([]string, len(key))
		for i, s := range key {
			lower[i] = strings.ToLower(s)
		}
		var hint string
		var path string
		var ok bool
		if len(lower) >= 5 && lower[0] == "global" && lower[1] == "llm" && lower[2] == "providers" {
			// 路径是 global.llm.providers.<name>.<key>：键名在第五段，第四段是用户自定义的 provider 名。
			hint, ok = removedProviderKeyHints[lower[4]]
			path = "global.llm.providers." + key[3] + "." + lower[4]
		} else {
			for i := len(lower); i > 0; i-- {
				cand := strings.Join(lower[:i], ".")
				if reported[cand] {
					break
				}
				if h, hit := removedKeyHints[cand]; hit {
					hint, path, ok = h, cand, true
					break
				}
			}
		}
		if !ok || reported[path] {
			continue
		}
		reported[path] = true
		if hint == "" {
			hint = "没有读者，已删除"
		}
		lines = append(lines, path+" → "+hint)
	}
	sort.Strings(lines)
	return lines
}

func warnRemovedKeys(md toml.MetaData) {
	lines := removedKeyReports(md)
	if len(lines) == 0 {
		return
	}
	slog.Warn("配置里存在已删除的键，它们不再有任何作用",
		"removed_keys", strings.Join(lines, "; "),
		"hint", "首启用 `yagent setup` 或 TUI 里的 /setup；手写请看 docs/config-guide.md")
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
	case "browser-agent", "browser":
		return c.Agents.LLM.Browser
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
	// 已删除的键要先点名，否则"我改了它但什么都没发生"只能靠人肉发现。
	warnRemovedKeys(md)
	cfg.gitCheckpointSectionDefined = sectionHasDecodedKey(md, "git_checkpoint")
	cfg.ultimateCompressionKeyDefined = md.IsDefined("enhanced_commander", "enable_ultimate_compression")
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
	// [resume] 与 [blackboard] 同构的一处坑：max_replay_messages 显式写 0 的意思是
	// "不限条数"，而"没写这一段"解析出来也是 0，所以缺省判定要看键的存在性。
	cfg.resumeSectionDefined = sectionHasDecodedKey(md, "resume")
	if stale := staleKeysIn(md, "resume"); len(stale) > 0 {
		slog.Warn("[resume] 段存在不认识的键，将被忽略",
			"stale_keys", strings.Join(stale, ", "),
			"hint", resumeKeyHint)
	}
	SetGlobalResume(cfg.Resume, ResumePresence{
		Section:            cfg.resumeSectionDefined,
		MaxReplayMessages:  md.IsDefined("resume", "max_replay_messages"),
		DegradeOnWorkspace: md.IsDefined("resume", "degrade_on_workspace_change"),
	})
	// [blueprint]：enable 的缺省是"开"，而 Go 零值是 false —— 判定必须看键与段的存在性（坑 26）。
	cfg.blueprintSectionDefined = sectionHasDecodedKey(md, "blueprint")
	if stale := staleKeysIn(md, "blueprint"); len(stale) > 0 {
		slog.Warn("[blueprint] 段存在不认识的键，将被忽略",
			"stale_keys", strings.Join(stale, ", "),
			"hint", blueprintKeyHint)
	}
	SetGlobalBlueprint(cfg.Blueprint, BlueprintPresence{
		Section:    cfg.blueprintSectionDefined,
		Enable:     md.IsDefined("blueprint", "enable"),
		InjectInto: md.IsDefined("blueprint", "inject_into"),
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

	// api_key_env 在唯一一处展开成 APIKey，之后的必填项检查与各消费方无需再区分来源。
	// 生效 provider 缺变量 = 启动失败（点名变量名）；其它 provider 缺变量 = 告警，
	// 因为"为另一台机器准备的条目"不该让本机开不起来。
	providerNames := make([]string, 0, len(c.Global.LLM.Providers))
	for name := range c.Global.LLM.Providers {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)
	for _, name := range providerNames {
		p := c.Global.LLM.Providers[name]
		if err := p.ApplyAPIKeyEnv(); err != nil {
			if name == effectiveProvider {
				return fmt.Errorf("provider '%s': %w", name, err)
			}
			slog.Warn("provider 引用了未设置的环境变量，用到它时才会失败",
				"provider", name, "error", err)
			continue
		}
		// map 的值是拷贝，展开结果必须写回去，否则后面的 GetProvider 拿到的还是空 key。
		c.Global.LLM.Providers[name] = p
	}

	activeProvider, err := c.GetProvider(effectiveProvider)
	if err != nil {
		return err
	}

	if activeProvider.Model == "" {
		return fmt.Errorf("model must be specified for provider '%s'", effectiveProvider)
	}

	// api_key/api_base_url 对所有 provider 一视同仁：本仓库没有 Bedrock 专用客户端，
	// 所谓 Bedrock 配置实际也是打到一个 OpenAI-compatible 端点上（网关或代理）。
	// 以前按 provider 名字前缀豁免这两项，结果"名字不带 bedrock 的真 Bedrock"照样被要求、
	// "名字带 bedrock 的"跳过检查然后在运行时炸（AGENTS.md §11 坑 14）。
	// 这里绝不能提前 return：下面整段是缺省值归一化，
	// 提前返回会让 max steps、[browser] 缺省、TUI 键位、压缩参数全部停在零值。
	if activeProvider.APIKey == "" {
		return fmt.Errorf("api_key must be specified for provider '%s' (or point api_key_env at an environment variable) — every provider needs a base_url + key; there is no Bedrock-specific client here", effectiveProvider)
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

	if c.EnhancedCommander.ToolResultKeepTokens == 0 {
		c.EnhancedCommander.ToolResultKeepTokens = 200
	}
	// enable_ultimate_compression 的缺省语义是"开"，而 Go 零值是 false，所以只能靠键的存在性区分：
	// 没写 → 开；显式写了 false → 真的关。以前 validate() 是"见 false 就写回 true"，
	// 于是这个键关不掉，而两份模板还把它当可配项在说。
	if !c.ultimateCompressionKeyDefined {
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

	// 整段没写过 → 全量默认值。写过任一键 → 只兜两个前缀：空的 agent_branch_prefix 会拼出
	// "/<session>" 这种坏分支名，而 bool 一律尊重用户写下的零值。
	// 以前的写法是 `AgentBranchPrefix == "" 就整段换成默认值`，后果是
	// [git_checkpoint] enabled = false 会被顶回 true —— 想关掉快照的人关不掉且不报错。
	if !c.gitCheckpointSectionDefined {
		c.GitCheckpoint = DefaultGitCheckpointConfig()
	} else {
		gitCfgDefaults := DefaultGitCheckpointConfig()
		if c.GitCheckpoint.AgentBranchPrefix == "" {
			c.GitCheckpoint.AgentBranchPrefix = gitCfgDefaults.AgentBranchPrefix
		}
		if c.GitCheckpoint.CheckpointTagPrefix == "" {
			c.GitCheckpoint.CheckpointTagPrefix = gitCfgDefaults.CheckpointTagPrefix
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

	return nil
}

type EnhancedCommanderConfig struct {
	// Enable 已删：压缩今天要 enable && enable_context_compression 两个布尔同时为真，
	// 而模板把两个都写成 false —— 只留这个描述性的键。
	EnableContextCompression bool `toml:"enable_context_compression" json:"enable_context_compression"`

	ContextCompressionThreshold int `toml:"context_compression_threshold" json:"context_compression_threshold"`

	ToolResultKeepTokens int `toml:"tool_result_keep_tokens" json:"tool_result_keep_tokens"`

	EnableUltimateCompression bool `toml:"enable_ultimate_compression" json:"enable_ultimate_compression"`

	UltimateCompressionKeepPlans int `toml:"ultimate_compression_keep_plans" json:"ultimate_compression_keep_plans"`
}

type CodeSeekConfig struct {
	BinaryPath     string   `toml:"binary_path"`
	MCPArgs        []string `toml:"mcp_args"`
	RequestTimeout int      `toml:"request_timeout"`
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

	ProfileDir string `toml:"profile_dir"`
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

// KeywordsConfig 只剩一个活键：词典路径是硬编码的（~/.yagent/keywords.txt +
// <project>/.yagent/keywords.txt，见 internal/tui/tui_model.go），
// 曾经那些 default_path / hot_reload / [[keywords.dict]] 全都没有读者。
type KeywordsConfig struct {
	DisableCompletion bool `toml:"disable_completion"`
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
