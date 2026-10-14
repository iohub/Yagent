package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// validate() 承担两件事：必填项检查 + 缺省值归一化。它跑在每次启动的必经之路上，
// 所以任何"提前 return"都会让归一化整段没执行 —— 表现不是报错，而是各消费方
// 拿到零值（max steps=0、[browser] 缺省没填、TUI 键位为空）。

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

const twoProviders = `
[global.llm]
use_provider = "main"

[global.llm.providers.main]
model = "m-main"
api_base_url = "http://127.0.0.1:1"
api_key = "k"

[global.llm.providers.other]
model = "m-other"
api_base_url = "http://127.0.0.1:1"
api_key = "k"
`

func TestLoadFromFile_RequiredFields(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "没有任何 use_provider",
			body: `
[global.llm.providers.main]
model = "m"
api_base_url = "http://127.0.0.1:1"
api_key = "k"
`,
			want: "use_provider",
		},
		{
			name: "一个 provider 都没配",
			body: "[global.llm]\nuse_provider = \"main\"\n",
			want: "no providers configured",
		},
		{
			name: "use_provider 指到不存在的名字",
			body: "[global.llm]\nuse_provider = \"ghost\"\n\n[global.llm.providers.main]\nmodel = \"m\"\napi_base_url = \"http://127.0.0.1:1\"\napi_key = \"k\"\n",
			want: "provider 'ghost' not found",
		},
		{
			name: "provider 没有 model",
			body: "[global.llm]\nuse_provider = \"main\"\n\n[global.llm.providers.main]\napi_base_url = \"http://127.0.0.1:1\"\napi_key = \"k\"\n",
			want: "model must be specified",
		},
		{
			name: "provider 没有 api_key",
			body: "[global.llm]\nuse_provider = \"main\"\n\n[global.llm.providers.main]\nmodel = \"m\"\napi_base_url = \"http://127.0.0.1:1\"\n",
			want: "api_key must be specified",
		},
		{
			name: "provider 没有 api_base_url",
			body: "[global.llm]\nuse_provider = \"main\"\n\n[global.llm.providers.main]\nmodel = \"m\"\napi_key = \"k\"\n",
			want: "api_base_url must be specified",
		},
		{
			name: "bedrock 没有 aws_region",
			body: "[global.llm]\nuse_provider = \"bedrock\"\n\n[global.llm.providers.bedrock]\nmodel = \"anthropic.claude-v2\"\n",
			want: "aws_region must be specified",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadFromFile(writeCfg(t, tc.body))
			if err == nil {
				t.Fatalf("LoadFromFile 接受了非法配置, want 含 %q 的错误", tc.want)
			}
			// 错误必须点名具体缺项：用户只有一行 slog 可看。
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %q, want 含 %q", err.Error(), tc.want)
			}
			if !strings.Contains(err.Error(), "config validation failed") {
				t.Errorf("err = %q, want 带 \"config validation failed\" 前缀（区分加载失败与校验失败）", err.Error())
			}
		})
	}
}

// Bedrock 用 AWS 凭证而不是 api_key/base_url，所以这两项检查必须跳过 ——
// 但**只有这两项**。归一化（max steps / [browser] 缺省 / LLM 重试 / TUI 键位）
// 与协议无关，不能被提前 return 掉。
func TestLoadFromFile_BedrockSkipsOnlyKeyChecks(t *testing.T) {
	cfg, err := LoadFromFile(writeCfg(t, `
[global.llm]
use_provider = "bedrock"

[global.llm.providers.bedrock]
model = "anthropic.claude-v2"
aws_region = "us-west-2"
`))
	if err != nil {
		t.Fatalf("bedrock 配置应当合法（无 api_key/base_url）: %v", err)
	}

	// 归一化必须照常发生。
	if cfg.Agent.DirectorMaxSteps != DefaultMaxSteps.Director {
		t.Errorf("DirectorMaxSteps = %d, want %d", cfg.Agent.DirectorMaxSteps, DefaultMaxSteps.Director)
	}
	if cfg.Agent.CodingMaxSteps != DefaultMaxSteps.Coding {
		t.Errorf("CodingMaxSteps = %d, want %d", cfg.Agent.CodingMaxSteps, DefaultMaxSteps.Coding)
	}
	if cfg.LLM.MaxRetries != DefaultLLMConfig().MaxRetries {
		t.Errorf("LLM.MaxRetries = %d, want %d", cfg.LLM.MaxRetries, DefaultLLMConfig().MaxRetries)
	}
	// [browser] 整段缺省 → DefaultBrowserConfig()。零值会把 Browser-Agent 静默关掉。
	if cfg.Browser.Mode != DefaultBrowserConfig().Mode {
		t.Errorf("Browser.Mode = %q, want %q（缺省没填等于新能力被静默关闭）",
			cfg.Browser.Mode, DefaultBrowserConfig().Mode)
	}
	if cfg.Browser.CDPTimeout < MinBrowserCDPTimeout {
		t.Errorf("Browser.CDPTimeout = %v, want ≥ 下限 %v", cfg.Browser.CDPTimeout, MinBrowserCDPTimeout)
	}
	if cfg.TUI.Keybindings.Edit.SubmitTask == "" {
		t.Error("TUI 键位没有被归一化，界面快捷键会全部失效")
	}
	if cfg.EnhancedCommander.ToolResultKeepTokens != 200 {
		t.Errorf("ToolResultKeepTokens = %d, want 200", cfg.EnhancedCommander.ToolResultKeepTokens)
	}
}

// validate() 是缺省值的唯一写入点：每个"用户没写"的字段都必须在这里补齐，
// 消费方才有资格假设非零。
func TestLoadFromFile_AppliesDefaultsForAbsentSections(t *testing.T) {
	cfg, err := LoadFromFile(writeCfg(t, twoProviders))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if got, want := cfg.Agent, DefaultMaxSteps; got.DirectorMaxSteps != want.Director ||
		got.CodingMaxSteps != want.Coding || got.RepoMaxSteps != want.Repo ||
		got.ChatMaxSteps != want.Chat || got.DevOpsMaxSteps != want.DevOps ||
		got.BrowserMaxSteps != want.Browser || got.MetaMaxSteps != want.Meta ||
		got.MetaRetryCount != want.MetaRetry {
		t.Errorf("max steps 未全部归一化: %+v", got)
	}

	// 写了的不能被覆盖。
	custom, err := LoadFromFile(writeCfg(t, twoProviders+"[agent]\ncoding_max_steps = 7\n"))
	if err != nil {
		t.Fatalf("load custom: %v", err)
	}
	if custom.Agent.CodingMaxSteps != 7 {
		t.Errorf("CodingMaxSteps = %d, want 保留用户写的 7", custom.Agent.CodingMaxSteps)
	}
	if custom.Agent.DirectorMaxSteps != DefaultMaxSteps.Director {
		t.Errorf("同段里没写的 DirectorMaxSteps = %d, want 默认 %d",
			custom.Agent.DirectorMaxSteps, DefaultMaxSteps.Director)
	}

	// LLM：MaxRetries 缺省补默认；FallbackMaxRetries 缺省跟随 MaxRetries（不是常量）。
	if cfg.LLM.MaxRetries != DefaultLLMConfig().MaxRetries {
		t.Errorf("MaxRetries = %d, want %d", cfg.LLM.MaxRetries, DefaultLLMConfig().MaxRetries)
	}
	if cfg.LLM.FallbackMaxRetries != cfg.LLM.MaxRetries {
		t.Errorf("FallbackMaxRetries = %d, want 跟随 MaxRetries=%d", cfg.LLM.FallbackMaxRetries, cfg.LLM.MaxRetries)
	}
	withRetries, err := LoadFromFile(writeCfg(t, twoProviders+"[llm]\nmax_retries = 9\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if withRetries.LLM.FallbackMaxRetries != 9 {
		t.Errorf("max_retries=9 时 FallbackMaxRetries = %d, want 9", withRetries.LLM.FallbackMaxRetries)
	}

	// 熔断窗口：只有启用了阈值才补 60s；没启用时保持 0（别造出一个没人看的计时器）。
	if cfg.LLM.CircuitBreakerResetTimeout != 0 {
		t.Errorf("threshold=0 时 reset_timeout = %v, want 0", cfg.LLM.CircuitBreakerResetTimeout)
	}
	brk, err := LoadFromFile(writeCfg(t, twoProviders+"[llm]\ncircuit_breaker_threshold = 3\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if brk.LLM.CircuitBreakerResetTimeout != 60*time.Second {
		t.Errorf("threshold=3 时 reset_timeout = %v, want 60s", brk.LLM.CircuitBreakerResetTimeout)
	}
	kept, err := LoadFromFile(writeCfg(t, twoProviders+"[llm]\ncircuit_breaker_threshold = 3\ncircuit_breaker_reset_timeout = \"5s\"\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if kept.LLM.CircuitBreakerResetTimeout != 5*time.Second {
		t.Errorf("显式 5s 被改成 %v, want 原样保留", kept.LLM.CircuitBreakerResetTimeout)
	}

	// 委派超时：负值归零（负 idle timeout 会让 activity.WithIdleTimeout 立刻判死）。
	neg, err := LoadFromFile(writeCfg(t, twoProviders+"[agent]\ndelegate_idle_timeout = \"-5s\"\ndelegate_total_timeout = \"-1m\"\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if neg.Agent.DelegateIdleTimeout != 0 || neg.Agent.DelegateTotalTimeout != 0 {
		t.Errorf("负委派超时 = (%v,%v), want (0,0)", neg.Agent.DelegateIdleTimeout, neg.Agent.DelegateTotalTimeout)
	}

	// git checkpoint：整段没写 → 整份默认；写了名字 → 只补 interval/max 的零值。
	if !cfg.GitCheckpoint.AutoCheckpoint && cfg.GitCheckpoint.CheckpointInterval == 0 {
		t.Error("[git_checkpoint] 缺省没有被填成 DefaultGitCheckpointConfig()")
	}
	partial, err := LoadFromFile(writeCfg(t, twoProviders+"[git_checkpoint]\nenabled = true\nagent_branch_prefix = \"agent/\"\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !partial.GitCheckpoint.Enabled {
		t.Error("[git_checkpoint] enabled=true 被整段默认覆盖掉了")
	}
	if partial.GitCheckpoint.CheckpointInterval != DefaultGitCheckpointConfig().CheckpointInterval {
		t.Errorf("CheckpointInterval = %d, want 默认 %d",
			partial.GitCheckpoint.CheckpointInterval, DefaultGitCheckpointConfig().CheckpointInterval)
	}

	// 词典：没配 → 指向 ~/.yagent/keywords.txt 且热加载关闭。
	if cfg.Keywords.DefaultPath == "" || !strings.HasSuffix(cfg.Keywords.DefaultPath, "/.yagent/keywords.txt") {
		t.Errorf("Keywords.DefaultPath = %q, want 以 /.yagent/keywords.txt 结尾", cfg.Keywords.DefaultPath)
	}
	if len(cfg.Keywords.Dicts) != 1 || cfg.Keywords.Dicts[0].Name != "autocomplete" {
		t.Errorf("Keywords.Dicts = %+v, want 一个 autocomplete 词典", cfg.Keywords.Dicts)
	}

	// TUI 键位 1..9 的 tab 快捷键按 alt+N 补齐。
	for i, key := range []string{cfg.TUI.Keybindings.Edit.Tab1, cfg.TUI.Keybindings.Edit.Tab9} {
		want := []string{"alt+1", "alt+9"}[i]
		if key != want {
			t.Errorf("Tab 键位 = %q, want %q", key, want)
		}
	}

	// 路由：[routing] 缺省即 auto。
	if !cfg.Routing.IsAuto() {
		t.Error("[routing] 缺省时 IsAuto() = false, want true")
	}
	fixed, err := LoadFromFile(writeCfg(t, twoProviders+"[routing]\nmode = \"fixed\"\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if fixed.Routing.IsAuto() {
		t.Error("mode=fixed 时 IsAuto() = true")
	}
}

// enable_ultimate_compression 目前**关不掉**：validate() 见 false 就写回 true。
// 这条断言钉的是现状（与 AGENTS.md §11 坑 4 的"[context] 表根本不解析"同类），
// 想改成"尊重用户配置"就把它改写成一个显式 false 的断言，并同步 AGENTS.md。
func TestLoadFromFile_UltimateCompressionIsForcedOn(t *testing.T) {
	cfg, err := LoadFromFile(writeCfg(t, twoProviders+"[enhanced_commander]\nenable_ultimate_compression = false\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.EnhancedCommander.EnableUltimateCompression {
		t.Error("enable_ultimate_compression=false 被尊重了 —— 与现状不符，请连同 AGENTS.md 一起确认这次语义变更")
	}

	other, err := LoadFromFile(writeCfg(t, twoProviders+"[enhanced_commander]\nmax_delegation_depth = 5\ntool_result_keep_tokens = 333\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if other.EnhancedCommander.MaxDelegationDepth != 5 || other.EnhancedCommander.ToolResultKeepTokens != 333 {
		t.Errorf("显式的同段字段被改写: depth=%d keep=%d, want 5 / 333",
			other.EnhancedCommander.MaxDelegationDepth, other.EnhancedCommander.ToolResultKeepTokens)
	}
}

// LoadConfig 只是 LoadFromFile 的别名：两条路径的校验强度必须一致，
// 否则 --config 与默认路径的行为会不同。
func TestLoadConfigSameAsLoadFromFile(t *testing.T) {
	path := writeCfg(t, "[global.llm]\nuse_provider = \"ghost\"\n")
	if _, err := LoadConfig(path); err == nil {
		t.Error("LoadConfig 跳过了校验")
	}
	if _, err := LoadFromFile(path); err == nil {
		t.Error("LoadFromFile 跳过了校验")
	}

	// 不存在的文件必须是错误，而不是"静默用默认配置"。
	if _, err := LoadFromFile(filepath.Join(t.TempDir(), "nope.toml")); err == nil {
		t.Error("LoadFromFile 对不存在的文件返回了 nil error")
	}
}

// EnsureConfigExists 是首次运行写模板的路径；已存在的文件绝不能被覆盖，
// 否则用户配置在第二次启动时消失。
func TestEnsureConfigExistsDoesNotOverwrite(t *testing.T) {
	path := writeCfg(t, "# 用户自己的配置\n")
	if err := EnsureConfigExists(path); err != nil {
		t.Fatalf("EnsureConfigExists: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "# 用户自己的配置\n" {
		t.Errorf("已存在的配置被覆盖成了 %d 字节的模板", len(got))
	}

	fresh := filepath.Join(t.TempDir(), "sub", "config.toml")
	if err := EnsureConfigExists(fresh); err != nil {
		t.Fatalf("生成缺失的配置: %v", err)
	}
	if st, err := os.Stat(fresh); err != nil || st.Size() == 0 {
		t.Errorf("模板没有生成: err=%v", err)
	}
}
