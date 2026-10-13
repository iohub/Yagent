package tools_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yagent/internal/browserctl"
	"yagent/internal/config"
	"yagent/internal/tools"
)

// TestE2EBrowserStack 是设计文档 §11 P0 的验收脚本：
// 走完整链路（tools.json 声明 → 适配器 → browserctl.Session → 真实 browser-control CLI → headless Chrome）。
// 需要本机装了 browser-control 与 Chrome，并且能出网，因此默认跳过。
//
//	YAGENT_BROWSER_E2E=1 go test ./internal/tools/ -run TestE2EBrowserStack -v -timeout 180s
func TestE2EBrowserStack(t *testing.T) {
	if os.Getenv("YAGENT_BROWSER_E2E") != "1" {
		t.Skip("需要真实 browser-control 与 Chrome：设 YAGENT_BROWSER_E2E=1 开启")
	}

	cfg := browserctl.FromAppConfig(config.DefaultBrowserConfig())
	cfg.WorkspaceRoot = t.TempDir()
	cfg.Mode = browserctl.ModeHeadless
	cfg.CDPURL = ""

	mgr := browserctl.NewManager(cfg)
	if ok, reason := mgr.Probe(); !ok {
		t.Skipf("浏览器能力不可用，跳过 E2E：%s", reason)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := mgr.Close(ctx); err != nil {
			t.Errorf("关闭浏览器失败: %v", err)
		}
	})

	set := tools.NewBrowserOperationsTool(mgr, cfg)
	adapters := browserSurface(t, set).Adapters()
	byName := map[string]*tools.Adapter{}
	for _, a := range adapters {
		byName[a.Name()] = a
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ctx = browserctl.WithDelegationID(ctx, "e2e")

	call := func(name, input string) string {
		t.Helper()
		adapter, ok := byName[name]
		if !ok {
			t.Fatalf("tool %s not registered", name)
		}
		raw, err := adapter.Call(ctx, input)
		if err != nil {
			t.Fatalf("%s(%s): %v", name, input, err)
		}
		var payload struct {
			Output string `json:"output"`
		}
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			t.Fatalf("unmarshal %s result: %v", name, err)
		}
		t.Logf("%s → %s", name, firstLines(payload.Output, 6))
		return payload.Output
	}

	status := call("browser_status", `{}`)
	if !strings.Contains(status, "cdp endpoint") {
		t.Errorf("status should report the CDP endpoint:\n%s", status)
	}

	call("browser_open", `{"url":"https://example.com/"}`)

	inspect := call("browser_inspect", `{"query":"information"}`)
	for _, want := range []string{"url: https://example.com/", "Example Domain"} {
		if !strings.Contains(inspect, want) {
			t.Errorf("inspect output missing %q:\n%s", want, inspect)
		}
	}

	snapshot := call("browser_snapshot", `{}`)
	ref := firstRef(snapshot, "link")
	if ref == "" {
		t.Fatalf("snapshot produced no usable link ref:\n%s", snapshot)
	}

	call("browser_click", `{"target":"`+ref+`","wait":5}`)

	after := call("browser_inspect", `{}`)
	if !strings.Contains(after, "iana.org") {
		t.Errorf("clicking the example.com link should land on iana.org:\n%s", after)
	}

	shot := call("browser_screenshot", `{}`)
	path := extractArtifactPath(shot)
	if path == "" {
		t.Fatalf("screenshot should report a path, got:\n%s", shot)
	}
	if info, err := os.Stat(path); err != nil || info.Size() == 0 {
		t.Errorf("screenshot file missing or empty: %v", err)
	}

	// 关键不变量：CLI 的状态目录只能落在会话工作区，绝不能污染被测仓库
	if _, err := os.Stat(".browser-control"); !os.IsNotExist(err) {
		t.Error(".browser-control appeared in the test working directory — cmd.Dir is not being honored")
	}
	workspace := mgr.WorkspaceDir("e2e")
	if _, err := os.Stat(filepath.Join(workspace, ".browser-control")); err != nil {
		t.Errorf("session workspace should hold the CLI state: %v", err)
	}
}

func firstLines(s string, n int) string {
	parts := strings.Split(s, "\n")
	if len(parts) > n {
		parts = append(parts[:n], "…")
	}
	return strings.Join(parts, "\n")
}

// firstRef 从渲染后的快照里取第一个指定 kind 的 ref（形如 `@e1 link "…"`）。
func firstRef(snapshot, kind string) string {
	for _, line := range strings.Split(snapshot, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "@") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == kind {
			return fields[0]
		}
	}
	return ""
}

func extractArtifactPath(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if _, rest, ok := strings.Cut(line, "截图已保存: "); ok {
			return strings.TrimSpace(rest)
		}
		if filepath.IsAbs(strings.TrimSpace(line)) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
