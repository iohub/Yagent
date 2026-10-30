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

// launchHandler 让 fake 的 launch 回一个端点，其余子命令一律成功。
func launchHandler() func(SpawnOpts) (SpawnOutput, error) {
	return func(opts SpawnOpts) (SpawnOutput, error) {
		if len(opts.Args) > 0 && opts.Args[0] == "launch" {
			return okOutput("BROWSER_CONTROL_CDP_URL=http://127.0.0.1:9333\n"), nil
		}
		return okOutput("ok\n"), nil
	}
}

// TestAcquire_DefaultProfileDirIsShared：不设 profile_dir 也必须共享 profile ——
// 每个工作区一份全新 profile 等于用户已登录的站点下次委派还要重新登录。
func TestAcquire_DefaultProfileDirIsShared(t *testing.T) {
	m, fake := newTestManager(t, func(c *Config) {
		c.Mode = ModeHeadless
		c.CDPURL = ""
	})
	fake.handler = launchHandler()

	session, err := m.Acquire(context.Background(), "default-profile")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer session.Release()

	want := filepath.Join(os.Getenv("HOME"), ".yagent", "data", "browser-profile")
	link := filepath.Join(session.Dir(), ".browser-control", "profile")
	dest, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("profile should be a symlink to the default shared profile dir: %v", err)
	}
	if dest != want {
		t.Errorf("symlink target = %s, want %s", dest, want)
	}
	if fi, err := os.Stat(want); err != nil || !fi.IsDir() {
		t.Errorf("default shared profile dir should exist as a directory: err = %v", err)
	}
	// 共享目录必须落在 workspaceRoot 之外，否则会被 workspace_ttl 连同登录态一起清掉。
	root := m.Config().WorkspaceRoot
	if want == root || strings.HasPrefix(want, root+string(filepath.Separator)) {
		t.Errorf("shared profile dir %s must live outside workspace root %s", want, root)
	}
}

// TestAcquire_MigratesLegacyWorkspaceProfile：旧版每个工作区一份真实 profile 目录，
// 里面可能就有用户上一次的 cookie —— 必须迁进共享目录，而不是丢弃。
func TestAcquire_MigratesLegacyWorkspaceProfile(t *testing.T) {
	m, fake := newTestManager(t, func(c *Config) {
		c.Mode = ModeHeadless
		c.CDPURL = ""
	})
	fake.handler = launchHandler()

	legacy := filepath.Join(m.WorkspaceDir("legacy"), ".browser-control", "profile")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatalf("mkdir legacy profile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "cookies.txt"), []byte("sessionid=abc"), 0o600); err != nil {
		t.Fatalf("write cookie: %v", err)
	}

	session, err := m.Acquire(context.Background(), "legacy")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer session.Release()

	dest, err := os.Readlink(filepath.Join(session.Dir(), ".browser-control", "profile"))
	if err != nil {
		t.Fatalf("legacy profile dir should be replaced by a symlink: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "cookies.txt"))
	if err != nil {
		t.Fatalf("cookie should have been migrated into %s: %v", dest, err)
	}
	if string(got) != "sessionid=abc" {
		t.Errorf("migrated cookie = %q, want %q", got, "sessionid=abc")
	}
}

// TestAcquire_KeepsWorkspaceProfileWhenSharedOccupied：共享目录非空时 rename 会失败，
// 这时必须降级为工作区独立 profile —— 既不删用户数据，也不阻断浏览器启动。
func TestAcquire_KeepsWorkspaceProfileWhenSharedOccupied(t *testing.T) {
	m, fake := newTestManager(t, func(c *Config) {
		c.Mode = ModeHeadless
		c.CDPURL = ""
	})
	fake.handler = launchHandler()

	shared := filepath.Join(os.Getenv("HOME"), ".yagent", "data", "browser-profile")
	if err := os.MkdirAll(shared, 0o700); err != nil {
		t.Fatalf("mkdir shared: %v", err)
	}
	holder := filepath.Join(shared, "holder.txt")
	if err := os.WriteFile(holder, []byte("in use"), 0o600); err != nil {
		t.Fatalf("write holder: %v", err)
	}

	legacy := filepath.Join(m.WorkspaceDir("occupied"), ".browser-control", "profile")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatalf("mkdir workspace profile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "cookies.txt"), []byte("local"), 0o600); err != nil {
		t.Fatalf("write cookie: %v", err)
	}

	session, err := m.Acquire(context.Background(), "occupied")
	if err != nil {
		t.Fatalf("an un-migratable profile must not fail the acquire: %v", err)
	}
	defer session.Release()

	link := filepath.Join(session.Dir(), ".browser-control", "profile")
	if _, err := os.Readlink(link); err == nil {
		t.Error("profile must stay a real directory when the shared dir cannot take it")
	}
	if _, err := os.Stat(filepath.Join(legacy, "cookies.txt")); err != nil {
		t.Errorf("workspace profile must be kept, not deleted: %v", err)
	}
	if got, err := os.ReadFile(holder); err != nil || string(got) != "in use" {
		t.Errorf("shared profile contents must be untouched: %q (err %v)", got, err)
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

// TestCleanupExpiredWorkspaces_KeepsSharedProfileBehindSymlink：工作区里的
// `.browser-control/profile` 只是指向共享目录的 symlink，清理过期工作区必须删链接本身，
// 而不是顺着它把用户的登录态清空 —— 那正是本次修复要保住的东西。
func TestCleanupExpiredWorkspaces_KeepsSharedProfileBehindSymlink(t *testing.T) {
	m, fake := newTestManager(t, func(c *Config) {
		c.Mode = ModeHeadless
		c.CDPURL = ""
		c.WorkspaceTTL = time.Hour
	})
	fake.handler = launchHandler()

	session, err := m.Acquire(context.Background(), "expired")
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	session.Release()

	shared := filepath.Join(os.Getenv("HOME"), ".yagent", "data", "browser-profile")
	if err := os.WriteFile(filepath.Join(shared, "cookies.txt"), []byte("keepme"), 0o600); err != nil {
		t.Fatalf("write cookie: %v", err)
	}

	dir := m.WorkspaceDir("expired")
	past := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(dir, past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	m.CleanupExpiredWorkspaces()

	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("expired workspace should be removed, stat err = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(shared, "cookies.txt"))
	if err != nil {
		t.Fatalf("shared profile must survive workspace TTL cleanup: %v", err)
	}
	if string(got) != "keepme" {
		t.Errorf("shared profile contents = %q, want %q", got, "keepme")
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
