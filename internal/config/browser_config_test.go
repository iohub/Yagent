package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// minimalConfig 只带一个可用 provider，用于观察 [browser] 的缺省填充行为。
func minimalConfig(t *testing.T, browserSection string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	content := `
[global.llm]
use_provider = "p"

[global.llm.providers.p]
model = "m"
api_base_url = "http://127.0.0.1:1"
api_key = "k"
` + browserSection
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

// TestBrowserDefaultsWhenSectionAbsent 覆盖真实升级场景：老配置没有 [browser] 段，
// TOML 会把 enabled 读成 false——必须由整表缺省判定兜住，否则新能力被静默关掉。
func TestBrowserDefaultsWhenSectionAbsent(t *testing.T) {
	cfg, err := LoadFromFile(minimalConfig(t, ""))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	b := cfg.Browser
	if !b.Enabled {
		t.Error("missing [browser] section must default to enabled, not silently disable the agent")
	}
	if b.Mode != "headless" {
		t.Errorf("mode = %q, want headless", b.Mode)
	}
	if b.CDPPortBase != 9333 {
		t.Errorf("cdp_port_base = %d, want 9333 (避开上游硬扫的 9222/9223)", b.CDPPortBase)
	}
	if b.CDPTimeout < MinBrowserCDPTimeout || b.CommandTimeout <= b.CDPTimeout {
		t.Errorf("timeouts not floored: cdp=%s command=%s", b.CDPTimeout, b.CommandTimeout)
	}
	if len(b.DeniedDomains) == 0 {
		t.Error("private-network denial list must not be empty by default")
	}
}

func TestBrowserExplicitDisableIsRespected(t *testing.T) {
	cfg, err := LoadFromFile(minimalConfig(t, `
[browser]
enabled = false
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Browser.Enabled {
		t.Error("an explicit [browser] section with enabled=false must stay disabled")
	}
	// 其余字段仍要兜到默认，避免半截配置把执行层带进非法状态
	if cfg.Browser.MaxOutputBytes <= 0 || cfg.Browser.DialogPolicy == "" {
		t.Errorf("partial section should still be filled: %+v", cfg.Browser)
	}
}

// TestBrowserTimeoutFloors 保证两个超时的大小关系：外层必须晚于上游内部超时，
// 否则模型只会看到"命令超时"而看不到上游给出的真实原因。
func TestBrowserTimeoutFloors(t *testing.T) {
	cfg, err := LoadFromFile(minimalConfig(t, `
[browser]
enabled = true
cdp_timeout = "1s"
command_timeout = "2s"
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if cfg.Browser.CDPTimeout != MinBrowserCDPTimeout {
		t.Errorf("cdp_timeout = %s, want floor %s", cfg.Browser.CDPTimeout, MinBrowserCDPTimeout)
	}
	if cfg.Browser.CommandTimeout <= cfg.Browser.CDPTimeout {
		t.Errorf("command_timeout (%s) must exceed cdp_timeout (%s)",
			cfg.Browser.CommandTimeout, cfg.Browser.CDPTimeout)
	}
}

func TestBrowserAllowsExplicitLongerTimeouts(t *testing.T) {
	cfg, err := LoadFromFile(minimalConfig(t, `
[browser]
enabled = true
cdp_timeout = "2m"
command_timeout = "3m"
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Browser.CDPTimeout != 2*time.Minute || cfg.Browser.CommandTimeout != 3*time.Minute {
		t.Errorf("explicit values above the floor must be preserved: %+v", cfg.Browser)
	}
}

// TestBrowserLegacyOnlySectionIsTreatedAsAbsent 对应一次真实的"浏览器 agent 不可用"报告：
// 旧浏览器栈的 [browser] 段只剩 headless/browser_path 这类失效键，
// 若按"段存在"处理，enabled 会被读成 false，新能力被静默关掉。
func TestBrowserLegacyOnlySectionIsTreatedAsAbsent(t *testing.T) {
	cfg, err := LoadFromFile(minimalConfig(t, `
[browser]
headless = true
browser_path = "/bin/google-chrome"
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if !cfg.Browser.Enabled {
		t.Error("a [browser] section holding only stale keys must fall back to defaults, not disable the agent")
	}
	if cfg.Browser.Mode != "headless" || cfg.Browser.DialogPolicy == "" {
		t.Errorf("defaults should be applied to the legacy section: %+v", cfg.Browser)
	}
	if cfg.Browser.CommandTimeout <= cfg.Browser.CDPTimeout {
		t.Errorf("timeouts must be floored: cdp=%s command=%s", cfg.Browser.CDPTimeout, cfg.Browser.CommandTimeout)
	}
}

// TestBrowserMixedSectionKeepsExplicitDisable：一旦段里有新 schema 认识的键，
// enabled = false 就是用户显式意图，必须被尊重。
func TestBrowserMixedSectionKeepsExplicitDisable(t *testing.T) {
	cfg, err := LoadFromFile(minimalConfig(t, `
[browser]
enabled = false
mode = "headful"
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.Browser.Enabled {
		t.Error("a configured section with enabled=false must stay disabled")
	}
	if cfg.Browser.Mode != "headful" {
		t.Errorf("mode = %q, want headful", cfg.Browser.Mode)
	}
}
