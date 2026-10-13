package browserctl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProbe_ReportsDisabled(t *testing.T) {
	m, _ := newTestManager(t, func(c *Config) { c.Enabled = false })

	ok, reason := m.Probe()
	if ok {
		t.Fatal("disabled config must not be available")
	}
	if !strings.Contains(reason, "enabled = false") {
		t.Errorf("reason should name the config switch, got %q", reason)
	}
}

func TestProbe_ReportsMissingBinary(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", "")

	m := NewManager(Config{Enabled: true, BinaryPath: ""}, WithSpawner(&fakeSpawner{}))
	ok, reason := m.Probe()
	if ok {
		t.Fatal("missing binary must not be available")
	}
	if !strings.Contains(reason, BinaryName) {
		t.Errorf("reason should name the binary, got %q", reason)
	}
}

func TestResolveBinaryPath_ConfiguredTakesPriority(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	installFakeBinary(t, home)

	custom := filepath.Join(t.TempDir(), "my-browser-control")
	if err := os.WriteFile(custom, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write custom binary: %v", err)
	}

	got, err := ResolveBinaryPath(custom)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != custom {
		t.Errorf("configured path should win, got %s want %s", got, custom)
	}

	// 非可执行文件必须被拒绝
	plain := filepath.Join(t.TempDir(), "plain")
	if err := os.WriteFile(plain, []byte("x"), 0o644); err != nil {
		t.Fatalf("write plain file: %v", err)
	}
	if _, err := ResolveBinaryPath(plain); err == nil {
		t.Error("non-executable binary_path must be rejected")
	}
}

func TestAcquire_AttachModeInjectsEnvAndNeverLaunches(t *testing.T) {
	m, fake := newTestManager(t, nil)

	session, err := m.Acquire(context.Background(), "deleg-1")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer session.Release()

	concrete, ok := session.(*Session)
	if !ok {
		t.Fatalf("manager must return *Session, got %T", session)
	}

	for i := 0; i < fake.callCount(); i++ {
		call := fake.callAt(i)
		if len(call.Args) > 0 && call.Args[0] == "launch" {
			t.Fatalf("attach mode must not launch a browser: %+v", call.Args)
		}
	}

	env := strings.Join(concrete.env, "\n")
	if !strings.Contains(env, "BROWSER_CONTROL_CDP_URL=http://127.0.0.1:9222") {
		t.Errorf("CDP endpoint must be injected explicitly:\n%s", env)
	}
	if !strings.Contains(env, "BROWSER_CONTROL_TIMEOUT=65") {
		t.Errorf("upstream CDP timeout must be raised above its 15s default:\n%s", env)
	}
	if session.Dir() != m.WorkspaceDir("deleg-1") {
		t.Errorf("session dir = %s, want %s", session.Dir(), m.WorkspaceDir("deleg-1"))
	}
	if !strings.Contains(session.Dir(), "deleg-1") {
		t.Errorf("workspace dir should carry the delegation id: %s", session.Dir())
	}
}

func TestAcquire_LaunchesOnceThenReuses(t *testing.T) {
	lg := "BROWSER_CONTROL_CDP_URL=http://127.0.0.1:9333\n"
	m, fake := newTestManager(t, func(c *Config) {
		c.Mode = ModeHeadless
		c.CDPURL = ""
	})
	fake.handler = func(opts SpawnOpts) (SpawnOutput, error) {
		if len(opts.Args) > 0 && opts.Args[0] == "launch" {
			return okOutput(lg), nil
		}
		return okOutput("ok\n"), nil
	}

	first, err := m.Acquire(context.Background(), "a")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	second, err := m.Acquire(context.Background(), "b")
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}

	launches := 0
	var launchArgs []string
	for i := 0; i < fake.callCount(); i++ {
		call := fake.callAt(i)
		if len(call.Args) > 0 && call.Args[0] == "launch" {
			launches++
			launchArgs = call.Args
		}
	}
	if launches != 1 {
		t.Fatalf("browser should be launched exactly once, got %d", launches)
	}
	if !containsArg(launchArgs, "--headless") {
		t.Errorf("headless mode must pass --headless: %v", launchArgs)
	}
	if !containsArg(launchArgs, "--port") {
		t.Errorf("launch must pin a port: %v", launchArgs)
	}
	// 第二个委派有自己的工作区，但复用同一个浏览器
	if first.Dir() == second.Dir() {
		t.Errorf("each delegation needs its own workspace: %s", first.Dir())
	}
	secondConcrete := second.(*Session)
	if env := strings.Join(secondConcrete.env, "\n"); !strings.Contains(env, lg) {
		t.Errorf("second session should reuse the launched endpoint:\n%s", env)
	}
}

func TestAcquire_LaunchFailureSurfacesOutput(t *testing.T) {
	m, fake := newTestManager(t, func(c *Config) {
		c.Mode = ModeHeadless
		c.CDPURL = ""
	})
	fake.handler = func(opts SpawnOpts) (SpawnOutput, error) {
		return failOutput("Chrome not found; set BROWSER_CONTROL_CHROME"), nil
	}

	_, err := m.Acquire(context.Background(), "x")
	if err == nil {
		t.Fatal("expected acquire to fail")
	}
	if !strings.Contains(err.Error(), "Chrome not found") {
		t.Errorf("error should carry the upstream hint, got %v", err)
	}
}

func TestWorkspaceDir_SanitizesDelegationID(t *testing.T) {
	m, _ := newTestManager(t, nil)

	dir := m.WorkspaceDir("../../etc/passwd")
	if strings.Contains(dir, "..") {
		t.Fatalf("path traversal must be neutralized: %s", dir)
	}
	if !strings.HasPrefix(dir, m.workspaceRoot()) {
		t.Fatalf("workspace must stay under the configured root: %s", dir)
	}
	if got := SanitizeDelegationID(""); got != "task" {
		t.Errorf("empty id should fall back to a literal, got %q", got)
	}
	if got := SanitizeDelegationID(strings.Repeat("a", 100)); len(got) != 32 {
		t.Errorf("id should be capped at 32 chars, got %d", len(got))
	}
}

func TestFindFreePort_SkipsDebugPorts(t *testing.T) {
	port, err := findFreePort(9222)
	if err != nil {
		t.Fatalf("findFreePort: %v", err)
	}
	if port == 9222 || port == 9223 {
		t.Fatalf("must skip the ports upstream falls back to scanning, got %d", port)
	}
}

func TestClose_StopsOwnedBrowserInWorkspace(t *testing.T) {
	m, fake := newTestManager(t, func(c *Config) {
		c.Mode = ModeHeadless
		c.CDPURL = ""
	})
	fake.handler = func(opts SpawnOpts) (SpawnOutput, error) {
		if len(opts.Args) > 0 && opts.Args[0] == "launch" {
			return okOutput("BROWSER_CONTROL_CDP_URL=http://127.0.0.1:9333\n"), nil
		}
		return okOutput("ok\n"), nil
	}

	if _, err := m.Acquire(context.Background(), "owner"); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}

	last := fake.lastCall()
	if len(last.Args) == 0 || last.Args[0] != "stop" {
		t.Fatalf("expected a stop call, got %+v", last.Args)
	}
	if !strings.Contains(last.Dir, "owner") {
		t.Errorf("stop must run in the owner workspace, got %s", last.Dir)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Errorf("second close should be a no-op, got %v", err)
	}
}

// TestClose_AttachModeLeavesUserBrowserAlone：attach 连的是用户自己的浏览器，
// 关掉它等于替用户做了一个他没同意的决定。
func TestClose_AttachModeLeavesUserBrowserAlone(t *testing.T) {
	m, fake := newTestManager(t, nil) // 默认 attach + cdp_url
	if _, err := m.Acquire(context.Background(), "attached"); err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}

	for i := 0; i < fake.callCount(); i++ {
		if args := fake.callAt(i).Args; len(args) > 0 && args[0] == "stop" {
			t.Fatalf("attach mode must never stop the user's browser: %+v", args)
		}
	}
}

// TestAcquire_ProfileDirIsSharedIntoWorkspace 覆盖登录态复用：上游把 --user-data-dir
// 硬编码成 `.browser-control/profile`，只能用 symlink 把它接到用户指定的 profile_dir。
func TestAcquire_ProfileDirIsSharedIntoWorkspace(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "chrome-profile")
	m, fake := newTestManager(t, func(c *Config) {
		c.Mode = ModeHeadless
		c.CDPURL = ""
		c.ProfileDir = shared
	})
	fake.handler = func(opts SpawnOpts) (SpawnOutput, error) {
		if len(opts.Args) > 0 && opts.Args[0] == "launch" {
			return okOutput("BROWSER_CONTROL_CDP_URL=http://127.0.0.1:9333\n"), nil
		}
		return okOutput("ok\n"), nil
	}

	session, err := m.Acquire(context.Background(), "logged-in")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer session.Release()

	link := filepath.Join(session.Dir(), ".browser-control", "profile")
	dest, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("profile should be a symlink to profile_dir: %v", err)
	}
	if dest != shared {
		t.Errorf("symlink target = %s, want %s", dest, shared)
	}
	if _, err := os.Stat(shared); err != nil {
		t.Errorf("profile_dir should have been created: %v", err)
	}
}

func TestAcquire_RejectsRelativeProfileDir(t *testing.T) {
	m, _ := newTestManager(t, func(c *Config) {
		c.Mode = ModeHeadless
		c.CDPURL = ""
		c.ProfileDir = "relative/profile"
	})
	if _, err := m.Acquire(context.Background(), "bad-profile"); err == nil {
		t.Fatal("a relative profile_dir must be rejected instead of silently ignored")
	}
}

// TestAcquire_WaitsUntilEndpointIsReady：上游 launch 只 spawn 不等，
// Chrome 调试端口要稍后才 bind——必须轮询到端点应答才交回会话。
func TestAcquire_WaitsUntilEndpointIsReady(t *testing.T) {
	m, fake := newTestManager(t, nil)
	var statusCalls int
	fake.handler = func(opts SpawnOpts) (SpawnOutput, error) {
		if len(opts.Args) > 0 && opts.Args[0] == "status" {
			statusCalls++
			if statusCalls < 3 {
				return failOutput("DevToolsActivePort not found"), nil
			}
			return okOutput("cdp: ok\n"), nil
		}
		return okOutput("ok\n"), nil
	}

	if _, err := m.Acquire(context.Background(), "race"); err != nil {
		t.Fatalf("acquire should retry until ready: %v", err)
	}
	if statusCalls < 3 {
		t.Errorf("should keep polling until the endpoint answers, got %d status calls", statusCalls)
	}

	// 已就绪的端点不该每次 Acquire 都重新探测
	before := fake.callCount()
	if _, err := m.Acquire(context.Background(), "second"); err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	for i := before; i < fake.callCount(); i++ {
		if args := fake.callAt(i).Args; len(args) > 0 && args[0] == "status" {
			t.Errorf("a ready endpoint should not be re-probed: %+v", args)
		}
	}
}

func TestAcquire_ReadinessTimeout(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	installFakeBinary(t, home)

	cfg := DefaultConfig()
	cfg.WorkspaceRoot = t.TempDir()
	cfg.Mode = ModeAttach
	cfg.CDPURL = "http://127.0.0.1:9222" // attach：不走 launch，直接测就绪等待
	fake := &fakeSpawner{handler: func(opts SpawnOpts) (SpawnOutput, error) {
		return failOutput("DevToolsActivePort not found"), nil
	}}
	m := NewManager(cfg, WithSpawner(fake), WithReadyTimeout(300*time.Millisecond))

	_, err := m.Acquire(context.Background(), "slow")
	if err == nil {
		t.Fatal("an endpoint that never answers must fail the acquire")
	}
	if !strings.Contains(err.Error(), "未就绪") {
		t.Errorf("error should name the readiness problem, got %v", err)
	}
}

func TestReapStale_RemovesDeadPidRecord(t *testing.T) {
	m, _ := newTestManager(t, nil)

	dir := m.WorkspaceDir("stale")
	bcDir := filepath.Join(dir, ".browser-control")
	if err := os.MkdirAll(bcDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	pidPath := filepath.Join(bcDir, "chrome.pid")
	if err := os.WriteFile(pidPath, []byte("999999"), 0o600); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	m.ReapStale(context.Background())

	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("dead pid record should be removed, stat err = %v", err)
	}
}

func TestCleanupExpiredWorkspaces(t *testing.T) {
	m, _ := newTestManager(t, func(c *Config) { c.WorkspaceTTL = time.Hour })

	old := m.WorkspaceDir("old")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(old, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	fresh := m.WorkspaceDir("fresh")
	if err := os.MkdirAll(fresh, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	m.CleanupExpiredWorkspaces()

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("expired workspace should be removed, stat err = %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("fresh workspace should survive: %v", err)
	}
}

func TestParseCDPURL(t *testing.T) {
	got := parseCDPURL("some noise\nBROWSER_CONTROL_CDP_URL=http://127.0.0.1:9333 \n")
	if got != "http://127.0.0.1:9333" {
		t.Errorf("parseCDPURL = %q", got)
	}
	if got := parseCDPURL("no endpoint here"); got != "" {
		t.Errorf("parseCDPURL should return empty, got %q", got)
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}
