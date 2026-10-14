package config

import (
	"strings"
	"testing"
	"time"
)

// 三级 provider 覆盖是"哪一个模型在跟用户说话"的唯一决定者。它出错的表现不是
// 报错，而是静默换模型 —— 所以每一级的优先级与回落都要逐条钉住。
//
// 契约（AGENTS.md §7）：工具指定 → tools.llm.use_provider →
// Agent 指定 → agents.llm.use_provider → global.llm.use_provider。

func threeTierConfig() *Config {
	providers := map[string]ProviderConfig{
		"globalp": {Model: "global-model", APIBaseURL: "http://127.0.0.1:1", APIKey: "k"},
		"agentp":  {Model: "agent-model", APIBaseURL: "http://127.0.0.1:1", APIKey: "k"},
		"toolp":   {Model: "tool-model", APIBaseURL: "http://127.0.0.1:1", APIKey: "k"},
		"anytool": {Model: "tools-lvl-model", APIBaseURL: "http://127.0.0.1:1", APIKey: "k"},
	}
	return &Config{
		Global: TopLevelConfig{LLM: &GlobalLLMConfig{UseProvider: "globalp", Providers: providers}},
		Agents: AgentsConfig{LLM: AgentsLLMConfig{
			Coding: &AgentLLMOverride{UseProvider: "agentp"},
		}},
		Tools: ToolsConfig{LLM: ToolsLLMConfig{
			MicroAgent: &ToolLLMOverride{UseProvider: "toolp"},
		}},
	}
}

func modelOf(t *testing.T, cfg *Config, agent, tool string) string {
	t.Helper()
	p, err := cfg.ResolveProvider(agent, tool)
	if err != nil {
		t.Fatalf("ResolveProvider(%q,%q): %v", agent, tool, err)
	}
	return p.Model
}

func TestResolveProvider_Precedence(t *testing.T) {
	cfg := threeTierConfig()

	cases := []struct {
		agent, tool, want, why string
	}{
		{"coding", "micro_agent", "tool-model", "工具指定赢过 Agent 指定"},
		{"coding", "thinking", "agent-model", "工具没指定 → Agent 指定"},
		{"repo", "micro_agent", "tool-model", "Agent 没指定也不影响工具指定"},
		{"repo", "thinking", "global-model", "两级都没指定 → global"},
		{"coding", "", "agent-model", "不带工具时走 Agent"},
		{"repo", "", "global-model", "不带工具且 Agent 未覆盖 → global"},
		{"", "micro_agent", "tool-model", "只问工具"},
		{"", "", "global-model", "什么都没给 → global"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.why, func(t *testing.T) {
			if got := modelOf(t, cfg, tc.agent, tc.tool); got != tc.want {
				t.Errorf("ResolveProvider(%q,%q) → model %q, want %q", tc.agent, tc.tool, got, tc.want)
			}
		})
	}
}

// tools.llm.use_provider 是**工具层的兜底**，不是某个工具的专属：一旦设了，
// 所有没单独点名的工具都换到它。这个覆盖面容易被误读成"只管微工具"。
func TestResolveProvider_ToolsLevelFallbackCoversEveryTool(t *testing.T) {
	cfg := threeTierConfig()
	cfg.Tools.LLM.UseProvider = "anytool"

	// 被 [tools.llm.<name>] 点名的工具仍走自己的覆盖。
	if got := modelOf(t, cfg, "", "micro_agent"); got != "tool-model" {
		t.Errorf("micro_agent → %q, want 专属覆盖优先于 tools.llm 兜底", got)
	}
	// 其余工具（包括匹配表里根本没有的名字）一律落到 tools.llm。
	for _, tool := range []string{"thinking", "deepthinking", "design", "unlisted_tool"} {
		if got := modelOf(t, cfg, "", tool); got != "tools-lvl-model" {
			t.Errorf("tool %q → %q, want tools-lvl-model（tools.llm.use_provider 兜底）", tool, got)
		}
	}
	// 没有工具名时完全不受 tools.llm 影响 —— 否则 Agent 会被工具层兜底劫走。
	if got := modelOf(t, cfg, "", ""); got != "global-model" {
		t.Errorf("无工具名 → %q, want global", got)
	}
	if got := modelOf(t, cfg, "coding", ""); got != "agent-model" {
		t.Errorf("coding 无工具名 → %q, want agents 覆盖", got)
	}
}

// Agent 名字有 -agent 后缀的老写法（conductor-agent / coding-agent …），大小写也不统一，
// 甚至可能是空串。匹配表漏掉任何一种写法，该 Agent 就静默回落到 global。
func TestResolveProvider_AgentNameSpellings(t *testing.T) {
	spellings := map[string][]string{
		"director": {"director", "Director", "director-agent", "DIRECTOR-AGENT", "conductor-agent", "Conductor-Agent"},
		"coding":   {"coding", "coding-agent", "Coding", "CODING-AGENT"},
		"repo":     {"repo", "repo-agent"},
		"chat":     {"chat", "chat-agent"},
		"meta":     {"meta", "meta-agent"},
		"devops":   {"devops", "devops-agent", "DevOps-Agent"},
	}
	for canon, names := range spellings {
		cfg := threeTierConfig()
		// 清掉 coding 的默认覆盖，改成只给"当前被测 Agent"设覆盖。
		cfg.Agents.LLM.Coding = nil
		switch canon {
		case "director":
			cfg.Agents.LLM.Director = &AgentLLMOverride{UseProvider: "agentp"}
		case "coding":
			cfg.Agents.LLM.Coding = &AgentLLMOverride{UseProvider: "agentp"}
		case "repo":
			cfg.Agents.LLM.Repo = &AgentLLMOverride{UseProvider: "agentp"}
		case "chat":
			cfg.Agents.LLM.Chat = &AgentLLMOverride{UseProvider: "agentp"}
		case "meta":
			cfg.Agents.LLM.Meta = &AgentLLMOverride{UseProvider: "agentp"}
		case "devops":
			cfg.Agents.LLM.DevOps = &AgentLLMOverride{UseProvider: "agentp"}
		}
		for _, name := range names {
			if got := modelOf(t, cfg, name, ""); got != "agent-model" {
				t.Errorf("%s 的写法 %q → %q, want agent-model（匹配表漏了这一种写法）", canon, name, got)
			}
		}
	}

	// 下划线写法不在匹配表里（只有 "-" 后缀形式）：这是刻意收窄的接受面，
	// 记在这里防止有人"顺手加个 case"把优先级搞乱。
	underscore := threeTierConfig()
	underscore.Agents.LLM.Coding = &AgentLLMOverride{UseProvider: "agentp"}
	if got := modelOf(t, underscore, "coding_agent", ""); got != "global-model" {
		t.Errorf("coding_agent → %q, want 回落 global（匹配表不认下划线）", got)
	}

	// Custom Agent 的名字是运行期动态的，匹配表里没有 → 必须回落 global，
	// 而不是报错或落到某个内建 Agent 的覆盖上。
	cfg := threeTierConfig()
	if got := modelOf(t, cfg, "security_auditor", ""); got != "global-model" {
		t.Errorf("custom agent → %q, want global-model", got)
	}
}

func TestResolveProvider_Errors(t *testing.T) {
	// 名字写了但 providers 里没有：错误必须点名该名字，否则用户无从下手。
	cfg := threeTierConfig()
	cfg.Agents.LLM.Coding = &AgentLLMOverride{UseProvider: "typo"}
	if _, err := cfg.ResolveProvider("coding", ""); err == nil {
		t.Error("unknown provider name accepted")
	} else if !strings.Contains(err.Error(), "typo") {
		t.Errorf("err = %v, want it to name the missing provider", err)
	}

	// 一个 provider 都没有配：不能返回零值 ProviderConfig 让上层去撞 nil。
	empty := &Config{}
	if _, err := empty.ResolveProvider("", ""); err == nil || err.Error() != "no LLM provider configured" {
		t.Errorf("err = %v, want \"no LLM provider configured\"", err)
	}

	if _, err := cfg.GetProvider(""); err == nil {
		t.Error("GetProvider(\"\") = ok, want an error")
	}
	if names := (&Config{}).GetProviderNames(); len(names) != 0 {
		t.Errorf("GetProviderNames() with no LLM section = %v, want empty", names)
	}
}

func TestGetProviderNamesSorted(t *testing.T) {
	cfg := threeTierConfig()
	want := "agentp,anytool,globalp,toolp"
	if got := strings.Join(cfg.GetProviderNames(), ","); got != want {
		t.Errorf("GetProviderNames() = %s, want %s（顺序影响错误提示与 TUI 列表）", got, want)
	}
}

// ---------- fallback providers ----------

func TestIsAnthropic(t *testing.T) {
	cases := map[string]bool{
		"anthropic": true, "Anthropic": true, "ANTHROPIC": true,
		"openai": false, "": false, "anthropic-messages": false,
	}
	for format, want := range cases {
		p := ProviderConfig{ApiFormat: format}
		if got := p.IsAnthropic(); got != want {
			t.Errorf("IsAnthropic() with api_format=%q = %v, want %v", format, got, want)
		}
	}
}

func TestGetSortedFallbackProviders(t *testing.T) {
	all := map[string]ProviderConfig{
		"a": {Model: "a"}, "b": {Model: "b"}, "c": {Model: "c"},
	}

	// 没有配 fallback → nil（而不是空切片：上层用 len() 判空，但 nil 是原语义）。
	p := &ProviderConfig{}
	if got := p.GetSortedFallbackProviders(all); got != nil {
		t.Errorf("empty fallback list → %v, want nil", got)
	}

	// 全部权重为 0：保持声明顺序。加一次"按名字排序"就是行为变更。
	p = &ProviderConfig{FallbackProviders: []FallbackProvider{{Provider: "c"}, {Provider: "a"}, {Provider: "b"}}}
	if got := fallbackNames(p.GetSortedFallbackProviders(all)); got != "c,a,b" {
		t.Errorf("无权重时 → %s, want 保持声明顺序 c,a,b", got)
	}

	// 有权重：降序，且同权重保持声明顺序（SliceStable）。
	p = &ProviderConfig{FallbackProviders: []FallbackProvider{
		{Provider: "a", Weight: 1}, {Provider: "b", Weight: 2}, {Provider: "c", Weight: 1},
	}}
	if got := fallbackNames(p.GetSortedFallbackProviders(all)); got != "b,a,c" {
		t.Errorf("有权重时 → %s, want b,a,c（降序 + 同权重稳定）", got)
	}

	// 引用了不存在的 provider 必须被剔除，否则 FallbackEngine 会在运行期拿到空配置。
	p = &ProviderConfig{FallbackProviders: []FallbackProvider{{Provider: "ghost"}, {Provider: "a"}, {Provider: "b", Weight: 5}}}
	got := p.GetSortedFallbackProviders(all)
	if fallbackNames(got) != "b,a" {
		t.Errorf("未知 provider → %s, want 只剩 b,a（未知项被剔除且 b 权重最高）", fallbackNames(got))
	}
}

func fallbackNames(fs []FallbackProvider) string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.Provider)
	}
	return strings.Join(out, ",")
}

// fallback_providers = ["deepseek"] 的字符串形式与 { provider=..., weight=... }
// 的表形式共用一个类型，靠 UnmarshalText 支持前者。
func TestFallbackProviderUnmarshalText(t *testing.T) {
	var fp FallbackProvider
	if err := fp.UnmarshalText([]byte("mimo")); err != nil {
		t.Fatalf("UnmarshalText: %v", err)
	}
	if fp.Provider != "mimo" || fp.Weight != 0 {
		t.Errorf("解析结果 = %+v, want Provider=mimo Weight=0", fp)
	}
}

// ---------- routing ----------

func TestRoutingIsAuto(t *testing.T) {
	cases := map[string]bool{
		"":        true, // 缺省即 auto（老配置没有 [routing] 段）
		"auto":    true,
		"Auto":    true,
		"  auto":  true,
		"fixed":   false,
		"FIXED":   false,
		" fixed ": false,
		"bogus":   true, // 认不出的值按 auto 处理：写错 mode 不该关掉路由
	}
	for mode, want := range cases {
		if got := (RoutingConfig{Mode: mode}).IsAuto(); got != want {
			t.Errorf("IsAuto() with mode=%q = %v, want %v", mode, got, want)
		}
	}
}

func TestDetectBedrockProvider(t *testing.T) {
	cases := map[string]string{
		"us.amazon.nova-premier-v1:0":    "amazon",
		"anthropic.claude-v2":            "anthropic",
		"us.anthropic.claude-3-5-sonnet": "anthropic",
		"meta.llama3-70b-instruct-v1:0":  "meta",
		"cohere.command-r-plus-v1:0":     "cohere",
		"ai21.j2-mid-v1":                 "ai21",
		"some-unknown-model":             "amazon", // 兜底
		"":                               "amazon",
		"ANTHROPIC.CLAUDE-V2":            "anthropic", // 大小写不敏感
	}
	for id, want := range cases {
		if got := DetectBedrockProvider(id); got != want {
			t.Errorf("DetectBedrockProvider(%q) = %q, want %q", id, got, want)
		}
	}
	// nova 判定必须早于 amazon 兜底、且优先于同串里的其它厂商名。
	if got := DetectBedrockProvider("amazon.nova-micro-v1:0"); got != "amazon" {
		t.Errorf("nova 前缀判定 = %q, want amazon", got)
	}
}

// ---------- 全局单例：LLM 超时与 deferred 开关 ----------

// FormatLLMTimeout 的输出要写进 TOML 并给用户看，所以必须能被 time.ParseDuration
// 读回来；不可解析的输出在下次加载时变成"配置项凭空消失"。
func TestFormatLLMTimeoutParsesBack(t *testing.T) {
	cases := map[time.Duration]string{
		45 * time.Second:        "45s",   // 不足一分钟 → 整秒
		90 * time.Second:        "1m30s", // 满一分钟起改用复数单位
		2 * time.Minute:         "2m",
		90 * time.Minute:        "1h30m",
		1500 * time.Millisecond: "1.5s",
		time.Second:             "1s",
		time.Hour:               "1h",
	}
	for d, want := range cases {
		if got := FormatLLMTimeout(d); got != want {
			t.Errorf("FormatLLMTimeout(%v) = %q, want %q", d, got, want)
		}
		if _, err := time.ParseDuration(FormatLLMTimeout(d)); err != nil {
			t.Errorf("FormatLLMTimeout(%v) = %q 无法被 ParseDuration 读回: %v", d, FormatLLMTimeout(d), err)
		}
	}
	// 非正值必须是 "0s" 而不是 ""：空串写进配置文件会让 TOML 直接解析失败。
	for _, d := range []time.Duration{0, -time.Second} {
		if got := FormatLLMTimeout(d); got != "0s" {
			t.Errorf("FormatLLMTimeout(%v) = %q, want \"0s\"", d, got)
		}
	}
}

// 把全局超时归零等于让 LLM 调用失去超时，所以写入口必须拒绝非正值并保留现值。
func TestSetGlobalLLMTimeoutRejectsNonPositive(t *testing.T) {
	prev := GlobalLLMTimeout()
	t.Cleanup(func() {
		// 全局单例没有"清空"的合法路径，恢复时同样只接受正值。
		if prev > 0 {
			SetGlobalLLMTimeout(prev)
		} else {
			globalLLMTimeoutNS.Store(0)
		}
	})

	SetGlobalLLMTimeout(7 * time.Second)
	if got := GlobalLLMTimeout(); got != 7*time.Second {
		t.Fatalf("GlobalLLMTimeout() = %v, want 7s", got)
	}
	for _, bad := range []time.Duration{0, -time.Minute} {
		SetGlobalLLMTimeout(bad)
		if got := GlobalLLMTimeout(); got != 7*time.Second {
			t.Errorf("SetGlobalLLMTimeout(%v) 之后 = %v, want 保留 7s（非正值被拒绝）", bad, got)
		}
	}
}

// [tools.deferred] 的关法是 disable=true（不是 enabled=false）；
// BudgetTokens=0 表示"没写这一段"→ 归一化成缺省预算，负值才是显式"不设预算"。
func TestGlobalDeferredToolsNormalization(t *testing.T) {
	prev := GlobalDeferredTools()
	t.Cleanup(func() { SetGlobalDeferredTools(prev) })

	if def := DefaultDeferredToolsConfig(); def.Disable || def.BudgetTokens != 2500 {
		t.Errorf("DefaultDeferredToolsConfig() = %+v, want 开启 + 预算 2500", def)
	}

	SetGlobalDeferredTools(DeferredToolsConfig{Disable: true})
	got := GlobalDeferredTools()
	if !got.Disable {
		t.Error("disable=true 写入后 GlobalDeferredTools().Disable = false")
	}
	if got.BudgetTokens != 2500 {
		t.Errorf("没写预算时归一化成 %d, want 2500", got.BudgetTokens)
	}

	SetGlobalDeferredTools(DeferredToolsConfig{BudgetTokens: -1, Hot: []string{"run_bash"}, Cold: []string{"thinking"}})
	got = GlobalDeferredTools()
	if got.BudgetTokens != -1 {
		t.Errorf("显式负预算被改成 %d, want 原样保留 -1（表示不设预算）", got.BudgetTokens)
	}
	if strings.Join(got.Hot, ",") != "run_bash" || strings.Join(got.Cold, ",") != "thinking" {
		t.Errorf("调档名单 = hot%v cold%v, want [run_bash] / [thinking]", got.Hot, got.Cold)
	}
}
