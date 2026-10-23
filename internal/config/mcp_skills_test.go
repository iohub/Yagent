package config

// mcp_test.go / skills_test.go 共用的部分：两组配置各有一条"改值必须变"的断言链。
//
// 每一条都对应一个"错了不报错"的形状：
//   - enable 缺省"开"而零值是 false（坑 26）；
//   - server 名字要进工具名前缀，拼错只是"工具名对不上"；
//   - inject_into 写不认识的名字必须 warn 后丢掉，不能静默当没写；
//   - kill-switch 只认精确的 "0"。

import (
	"reflect"
	"strings"
	"testing"
)

func loadMCPCfg(t *testing.T, section string) *Config {
	t.Helper()
	t.Cleanup(func() {
		SetGlobalMCP(DefaultMCPConfig(), MCPPresence{})
	})
	cfg, err := LoadFromFile(minimalConfig(t, section))
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	return cfg
}

func loadSkillsCfg(t *testing.T, section string) *Config {
	t.Helper()
	t.Cleanup(func() {
		SetGlobalSkills(DefaultSkillsConfig(), SkillsPresence{})
	})
	cfg, err := LoadFromFile(minimalConfig(t, section))
	if err != nil {
		t.Fatalf("LoadFromFile: %v", err)
	}
	return cfg
}

func TestMCPConfigDefaultsWhenSectionAbsent(t *testing.T) {
	cfg := loadMCPCfg(t, "")
	d := DefaultMCPConfig()

	if cfg.mcpSectionDefined {
		t.Error("没有 [mcp] 段时 mcpSectionDefined 该是 false")
	}
	active := GlobalMCP()
	if !active.Enable {
		t.Error("整段缺省被读成 Enable=false —— 外部工具会静默整体退出")
	}
	if active.StartupTimeout != d.StartupTimeout || active.ReconnectBackoff != d.ReconnectBackoff {
		t.Errorf("超时 = %v/%v, want 默认 %v/%v", active.StartupTimeout, active.ReconnectBackoff, d.StartupTimeout, d.ReconnectBackoff)
	}
	if active.MaxReconnects != d.MaxReconnects || active.MaxToolsPerServer != d.MaxToolsPerServer {
		t.Errorf("上限 = %d/%d, want 默认 %d/%d", active.MaxReconnects, active.MaxToolsPerServer, d.MaxReconnects, d.MaxToolsPerServer)
	}
	if !active.StatusLine {
		t.Error("status_line 缺省必须是 true：没有它，模型看不见 server 为什么缺失")
	}
	if !MCPEnabled() {
		t.Error("MCPEnabled() = false，但配置里根本没提过这个特性")
	}
}

// 段写了但没写 enable：仍然必须是"开"（归一化有 !Section 与 !Enable 两条路径）。
func TestMCPConfigEnableFalseIsHonored(t *testing.T) {
	loadMCPCfg(t, "\n[mcp]\nmax_reconnects = 1\n")
	if !GlobalMCP().Enable {
		t.Error("写了 [mcp] 段但没写 enable，被当成关掉（坑 26）")
	}

	loadMCPCfg(t, "\n[mcp]\nenable = false\nstatus_line = false\nmax_reconnects = 1\n")
	active := GlobalMCP()
	if active.Enable {
		t.Error("显式 enable = false 没有生效")
	}
	if active.StatusLine {
		t.Error("显式 status_line = false 没有生效")
	}
	if MCPEnabled() {
		t.Error("MCPEnabled() 在 enable=false 时仍为真")
	}
}

func TestMCPEnabledKillSwitch(t *testing.T) {
	t.Cleanup(func() {
		SetGlobalMCP(DefaultMCPConfig(), MCPPresence{})
	})
	SetGlobalMCP(DefaultMCPConfig(), MCPPresence{Section: true, Enable: true})

	for _, value := range []string{"1", "", "false", "off"} {
		t.Setenv("YAGENT_MCP", value)
		if !MCPEnabled() {
			t.Errorf("YAGENT_MCP=%q 关掉了机制，want 只认精确的 \"0\"", value)
		}
	}
	t.Setenv("YAGENT_MCP", "0")
	if MCPEnabled() {
		t.Error("YAGENT_MCP=0 没有关掉机制")
	}
}

func TestMCPServerNameValidation(t *testing.T) {
	cases := []struct {
		name    string
		section string
		pointAt string
	}{
		{
			name:    "duplicate",
			section: "\n[mcp]\n[[mcp.servers]]\nname = \"dup\"\ncommand = \"x\"\n[[mcp.servers]]\nname = \"dup\"\ncommand = \"y\"\n",
			pointAt: "duplicate server name",
		},
		{
			name:    "illegal char",
			section: "\n[mcp]\n[[mcp.servers]]\nname = \"My Server\"\ncommand = \"x\"\n",
			pointAt: "must match",
		},
		{
			name:    "reserved name",
			section: "\n[mcp]\n[[mcp.servers]]\nname = \"codeseek\"\ncommand = \"x\"\n",
			pointAt: "reserved",
		},
		{
			name:    "missing command",
			section: "\n[mcp]\n[[mcp.servers]]\nname = \"npx-one\"\n",
			pointAt: "command is required",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() { SetGlobalMCP(DefaultMCPConfig(), MCPPresence{}) })
			_, err := LoadFromFile(minimalConfig(t, tc.section))
			if err == nil {
				t.Fatalf("配置应该被拒收（%s）", tc.name)
			}
			if !strings.Contains(err.Error(), tc.pointAt) {
				t.Errorf("err = %v, want 含 %q（点名哪一个 server）", err, tc.pointAt)
			}
		})
	}

	// 反向：合法条目必须过，字段原样到达。
	cfg := loadMCPCfg(t, `
[mcp]
[[mcp.servers]]
name = "github"
command = "npx"
args = ["-y", "server-github"]
env = { TOKEN = "x" }
enabled = false
allow_write = true
read_only = ["list_issues"]
tools = ["list_issues"]
exclude = ["delete_repo"]
request_timeout = 9
`)
	if len(cfg.MCP.Servers) != 1 {
		t.Fatalf("servers = %d, want 1", len(cfg.MCP.Servers))
	}
	s := cfg.MCP.Servers[0]
	if s.IsEnabled() {
		t.Error("enabled = false 没有生效（指针判据）")
	}
	if s.Command != "npx" || len(s.Args) != 2 || s.Env["TOKEN"] != "x" || s.RequestTimeout != 9 {
		t.Errorf("server 字段 = %+v, want 原样到达", s)
	}
	if !s.AllowWrite || len(s.ReadOnly) != 1 || len(s.Tools) != 1 || len(s.Exclude) != 1 {
		t.Errorf("策略字段 = %+v, want 原样到达", s)
	}
	// enabled 缺省是 true：没写这个键的条目必须启用。
	cfg = loadMCPCfg(t, "\n[mcp]\n[[mcp.servers]]\nname = \"plain\"\ncommand = \"x\"\n")
	if !cfg.MCP.Servers[0].IsEnabled() {
		t.Error("没写 enabled 的 server 被当成禁用")
	}
}

func TestSkillsConfigDefaultsWhenSectionAbsent(t *testing.T) {
	cfg := loadSkillsCfg(t, "")
	if cfg.skillsSectionDefined {
		t.Error("没有 [skills] 段时 skillsSectionDefined 该是 false")
	}
	active := GlobalSkills()
	if !active.Enable {
		t.Error("整段缺省被读成 Enable=false")
	}
	if got := strings.Join(active.InjectInto, ","); got != "director,coding,repo,chat" {
		t.Errorf("inject_into = %q", got)
	}
	if !reflect.DeepEqual(active.Dirs, DefaultSkillsConfig().Dirs) {
		t.Errorf("dirs = %v, want 默认（nil = 只用两级标准目录）", active.Dirs)
	}
	if !SkillsEnabled() || !SkillsInjectedInto("coding") {
		t.Error("SkillsEnabled/SkillsInjectedInto 在缺省配置下应为真")
	}
}

// inject_into 里不认识的名字必须 warn 后丢掉；显式 [] 是"一处都不注入"。
func TestSkillsConfigInjectIntoUnknownIsDropped(t *testing.T) {
	loadSkillsCfg(t, "\n[skills]\ninject_into = [\"coding\", \"browser\", \"CODING\", \" \"]\n")
	if got := strings.Join(GlobalSkills().InjectInto, ","); got != "coding" {
		t.Errorf("inject_into = %q, want 只剩 coding（不认识的名字被丢掉、重复被折叠）", got)
	}
	if SkillsInjectedInto("browser") {
		t.Error("browser 不在名单里却被判成要注入")
	}

	loadSkillsCfg(t, "\n[skills]\ninject_into = []\n")
	if len(GlobalSkills().InjectInto) != 0 {
		t.Errorf("显式空数组 = 一处都不注入，得到 %v", GlobalSkills().InjectInto)
	}
	if SkillsInjectedInto("coding") {
		t.Error("显式 [] 之后仍判成注入")
	}
}

// 每个键改值都必须逐字到达消费者（"写了没人读"由 template_test 与这条一起拦）。
func TestSkillsConfigEachKeyChangesBehavior(t *testing.T) {
	loadSkillsCfg(t, `
[skills]
enable = false
dirs = ["/tmp/skills-a", "/tmp/skills-b"]
inject_into = ["repo"]
catalog_max_tokens = 111
catalog_line_chars = 22
body_max_tokens = 333
`)
	active := GlobalSkills()
	if active.Enable {
		t.Error("enable 没生效")
	}
	if SkillsEnabled() {
		t.Error("SkillsEnabled() 在 enable=false 时仍为真")
	}
	if !reflect.DeepEqual(active.Dirs, []string{"/tmp/skills-a", "/tmp/skills-b"}) {
		t.Errorf("dirs = %v", active.Dirs)
	}
	if active.CatalogMaxTokens != 111 || active.CatalogLineChars != 22 || active.BodyMaxTokens != 333 {
		t.Errorf("预算 = %d/%d/%d, want 111/22/333", active.CatalogMaxTokens, active.CatalogLineChars, active.BodyMaxTokens)
	}

	loadSkillsCfg(t, "\n[skills]\nenable = false\n")
	active = GlobalSkills()
	if active.CatalogMaxTokens != DefaultSkillsConfig().CatalogMaxTokens {
		t.Errorf("<=0 应归默认，得到 %d", active.CatalogMaxTokens)
	}

	loadSkillsCfg(t, "\n[skills]\nenable = false\n")
	t.Setenv("YAGENT_SKILLS", "0")
	if SkillsEnabled() {
		t.Error("YAGENT_SKILLS=0 没有关掉机制")
	}
	t.Setenv("YAGENT_SKILLS", "false")
	loadSkillsCfg(t, "\n[skills]\nenable = true\n")
	if !SkillsEnabled() {
		t.Error("YAGENT_SKILLS=false 被当成关掉，want 只认精确的 \"0\"")
	}
}
