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
			// 以前 provider 名字以 "bedrock" 开头就豁免 api_key/api_base_url、改成要求
			// aws_region；但仓库里没有 Bedrock 客户端，豁免只会把失败推到第一次调用。
			name: "bedrock 命名的 provider 也不再豁免 api_key",
			body: "[global.llm]\nuse_provider = \"bedrock\"\n\n[global.llm.providers.bedrock]\nmodel = \"anthropic.claude-v2\"\n",
			want: "api_key must be specified",
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

// Bedrock 豁免已删除：必填项对每个 provider 一视同仁。这里同时钉住另一半——
// 归一化（max steps / [browser] 缺省 / LLM 重试 / TUI 键位 / 压缩参数）不能被任何
// 校验分支的提前 return 跳过（AGENTS.md §11 坑 14 的教训就是它曾经被跳过）。
func TestLoadFromFile_BedrockNoLongerExempted(t *testing.T) {
	broken := writeCfg(t, `
[global.llm]
use_provider = "bedrock"

[global.llm.providers.bedrock]
model = "anthropic.claude-v2"
aws_region = "us-west-2"
`)
	if _, err := LoadFromFile(broken); err == nil {
		t.Fatal("只带 aws_region 的 bedrock 配置仍然合法 —— 豁免没删干净")
	} else if !strings.Contains(err.Error(), "api_key") {
		t.Errorf("err = %q, want 点名 api_key", err)
	}

	cfg, err := LoadFromFile(writeCfg(t, `
[global.llm]
use_provider = "bedrock"

[global.llm.providers.bedrock]
model = "anthropic.claude-v2"
api_base_url = "https://bedrock-runtime.us-west-2.amazonaws.com/openai/v1"
api_key = "k"
`))
	if err != nil {
		t.Fatalf("指向 OpenAI-compatible 端点的 bedrock 配置应当合法: %v", err)
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
	// 该键的缺省语义是"开"，没写过就必须补成 true（关只能靠显式写 false）。
	if !cfg.EnhancedCommander.EnableUltimateCompression {
		t.Error("没写 enable_ultimate_compression 时第三档 = false, want 缺省为开")
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
		got.BrowserMaxSteps != want.Browser ||
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

	// git checkpoint：整段没写 → 整份默认；写过任一键 → 只补两个前缀，bool 尊重零值。
	if !cfg.GitCheckpoint.Enabled || cfg.GitCheckpoint.AgentBranchPrefix != "agent" {
		t.Errorf("[git_checkpoint] 缺省没有被填成 DefaultGitCheckpointConfig(): %+v", cfg.GitCheckpoint)
	}
	partial, err := LoadFromFile(writeCfg(t, twoProviders+"[git_checkpoint]\nenabled = true\nagent_branch_prefix = \"agent/\"\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !partial.GitCheckpoint.Enabled {
		t.Error("[git_checkpoint] enabled=true 被整段默认覆盖掉了")
	}
	if partial.GitCheckpoint.AgentBranchPrefix != "agent/" {
		t.Errorf("AgentBranchPrefix = %q, want 保留 agent/", partial.GitCheckpoint.AgentBranchPrefix)
	}

	// 这一条是 old-vs-new 的分界：以前"前缀为空就整段换默认值"，于是只写 enabled = false
	// 会被顶回 true —— 想关掉快照的人关不掉且不报错。
	off, err := LoadFromFile(writeCfg(t, twoProviders+"[git_checkpoint]\nenabled = false\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if off.GitCheckpoint.Enabled {
		t.Error("[git_checkpoint] enabled = false 被顶回 true（段级存在性判定失效）")
	}
	if off.GitCheckpoint.AgentBranchPrefix != DefaultGitCheckpointConfig().AgentBranchPrefix {
		t.Errorf("前缀 = %q, want 空值仍兜默认（否则会拼出 /<session> 这种坏分支名）",
			off.GitCheckpoint.AgentBranchPrefix)
	}

	// 词典：只剩 disable_completion 一个键，默认开补全。
	// （default_path / hot_reload / [[keywords.dict]] 没有读者，词典路径是硬编码的）
	if cfg.Keywords.DisableCompletion {
		t.Error("Keywords.DisableCompletion 缺省 = true, want false（补全默认开着）")
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

// enable_ultimate_compression 的缺省是"开"，而 Go 零值是 false —— 所以"没写"与
// "写了 false"必须靠键的存在性区分。以前 validate() 是"见 false 就写回 true"，
// 这个键因此关不掉（两份模板还把它当可配项在说）。
func TestLoadFromFile_UltimateCompressionRespectsFalse(t *testing.T) {
	off, err := LoadFromFile(writeCfg(t, twoProviders+"[enhanced_commander]\nenable_ultimate_compression = false\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if off.EnhancedCommander.EnableUltimateCompression {
		t.Error("enable_ultimate_compression = false 被写回 true —— 存在性判定没接上")
	}

	on, err := LoadFromFile(writeCfg(t, twoProviders+"[enhanced_commander]\ntool_result_keep_tokens = 333\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !on.EnhancedCommander.EnableUltimateCompression {
		t.Error("同段写过别的键但没写这一条时，第三档应当仍是缺省的开")
	}
	if on.EnhancedCommander.ToolResultKeepTokens != 333 {
		t.Errorf("显式的 tool_result_keep_tokens 被改写: %d, want 333", on.EnhancedCommander.ToolResultKeepTokens)
	}
}

// 压缩只认一个开关：[enhanced_commander] enable 已删除（今天它只是与
// enable_context_compression 做 AND 的第二个条件，两个布尔是纯粹的坑）。
func TestLoadFromFile_CompressionSingleKey(t *testing.T) {
	only, err := LoadFromFile(writeCfg(t, twoProviders+"[enhanced_commander]\nenable_context_compression = true\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !only.EnhancedCommander.EnableContextCompression {
		t.Error("enable_context_compression = true 没有被尊重")
	}
	if _, ok := removedKeyHints["enhanced_commander.enable"]; !ok {
		t.Error("enhanced_commander.enable 不在移除表里：旧配置里残留它会被静默当成没写")
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
// 否则用户配置在第二次启动时消失。created 必须如实报告"这次生成了"，
// 首启判定全靠它 —— 模板里的密钥是占位符而校验只查非空。
func TestEnsureConfigExistsDoesNotOverwrite(t *testing.T) {
	path := writeCfg(t, "# 用户自己的配置\n")
	created, err := EnsureConfigExists(path)
	if err != nil {
		t.Fatalf("EnsureConfigExists: %v", err)
	}
	if created {
		t.Error("已存在的配置被报告成\"本次生成\"，会把老用户领进引导页")
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "# 用户自己的配置\n" {
		t.Errorf("已存在的配置被覆盖成了 %d 字节的模板", len(got))
	}

	fresh := filepath.Join(t.TempDir(), "sub", "config.toml")
	created, err = EnsureConfigExists(fresh)
	if err != nil {
		t.Fatalf("生成缺失的配置: %v", err)
	}
	if !created {
		t.Error("新建模板后 created = false，首启引导不会触发")
	}
	st, err := os.Stat(fresh)
	if err != nil || st.Size() == 0 {
		t.Fatalf("模板没有生成: err=%v", err)
	}
	// 模板可能带明文密钥位，权限必须收到 0600。
	if perm := st.Mode().Perm(); perm != 0o600 {
		t.Errorf("生成的模板权限 = %o, want 600", perm)
	}
}
