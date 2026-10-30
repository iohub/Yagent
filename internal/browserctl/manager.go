package browserctl

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"yagent/internal/config"
	"yagent/internal/supervise"
)

const (
	// launchTimeout 是启动浏览器的外层等待上限（含 Chrome 冷启动）。
	launchTimeout = 30 * time.Second
	// policyTimeout 是装载浏览器侧阻断策略的等待上限。
	policyTimeout = 15 * time.Second
	// readyTimeout / readyPoll：上游 launch 是 spawn 完立刻返回的，
	// Chrome 的调试端口要稍后才 bind，第一条命令必然竞态失败（真实 E2E 复现过）。
	readyTimeout = 20 * time.Second
	readyPoll    = 300 * time.Millisecond
	// portScanRange 是从 cdp_port_base 起尝试的端口个数。
	portScanRange = 100
)

// chromeProc 记录我们自己启动的浏览器实例。进程级共享：
// 多个委派工作区复用同一个 Chrome，但各自的 .browser-control/ 相互独立。
type chromeProc struct {
	cdpURL   string
	port     int
	ownerDir string
	attached bool // attach 模式：不是我们启动的，不能由我们负责关闭
	ready    bool // 已确认 CDP 端点可用（避免每次 Acquire 都重新探测）
}

type Manager struct {
	cfg    Config
	spawn  Spawner
	policy *Policy

	// readyTimeout 是等待 CDP 端点应答的上限，用 Option 覆盖以便单元测试。
	readyTimeout time.Duration

	mu             sync.Mutex
	probed         bool
	bin            string
	avail          bool
	reason         string
	chrome         *chromeProc
	lastUse        time.Time
	sweeperStarted bool
	stopCh         chan struct{}
	stopOnce       sync.Once
	// grp 拥有回收协程。原来 Close 只关 stopCh、从不 join —— 于是"回收协程还在跑"
	// 这件事在退出时无人知道，也就没人能判断最后一次 stopChrome 是不是被它抢先做了。
	grp *supervise.Group
}

// Option 用于替换进程执行器（测试注入 fake Spawner）。
type Option func(*Manager)

func WithSpawner(s Spawner) Option { return func(m *Manager) { m.spawn = s } }

// WithReadyTimeout 缩短等待 CDP 端点就绪的上限（仅测试使用）。
func WithReadyTimeout(d time.Duration) Option {
	return func(m *Manager) {
		if d > 0 {
			m.readyTimeout = d
		}
	}
}

func NewManager(cfg Config, opts ...Option) *Manager {
	grp, _ := supervise.New(context.Background(), "browserctl")
	m := &Manager{
		cfg:          cfg,
		spawn:        NewSpawner(),
		policy:       NewPolicy(cfg.AllowedDomains, cfg.DeniedDomains),
		stopCh:       make(chan struct{}),
		readyTimeout: readyTimeout,
		grp:          grp,
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

func (m *Manager) Config() Config  { return m.cfg }
func (m *Manager) Policy() *Policy { return m.policy }

// Probe 解析二进制并校验版本，不启动浏览器。结果被缓存，可重复调用。
func (m *Manager) Probe() (bool, string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.probeLocked()
}

func (m *Manager) probeLocked() (bool, string) {
	if m.probed {
		return m.avail, m.reason
	}
	m.probed = true

	if !m.cfg.Enabled {
		m.avail, m.reason = false, "配置中 [browser] enabled = false"
		return m.avail, m.reason
	}

	path, err := ResolveBinaryPath(m.cfg.BinaryPath)
	if err != nil {
		m.avail, m.reason = false, err.Error()
		return m.avail, m.reason
	}
	m.bin = path
	m.avail, m.reason = true, ""
	slog.Info("browser-control 就绪", "path", path)
	return m.avail, m.reason
}

// Acquire 取得一次委派的工作区会话，必要时启动浏览器。
func (m *Manager) Acquire(ctx context.Context, delegationID string) (Runner, error) {
	if ok, reason := m.Probe(); !ok {
		return nil, fmt.Errorf("浏览器能力不可用：%s", reason)
	}

	dir := m.WorkspaceDir(delegationID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建浏览器工作区失败: %w", err)
	}

	cdpURL, err := m.ensureChrome(ctx, dir)
	if err != nil {
		return nil, err
	}
	m.touch()

	return &Session{mgr: m, dir: dir, env: m.sessionEnv(cdpURL)}, nil
}

// WorkspaceDir 返回某个委派的工作区目录（.browser-control 就落在它下面）。
func (m *Manager) WorkspaceDir(delegationID string) string {
	return filepath.Join(m.workspaceRoot(), SanitizeDelegationID(delegationID))
}

func (m *Manager) workspaceRoot() string {
	if m.cfg.WorkspaceRoot != "" {
		return m.cfg.WorkspaceRoot
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "yagent-browser")
	}
	return filepath.Join(home, ".yagent", "data", "browser")
}

// profileDir 返回共享 Chrome profile 的绝对路径：显式 [browser] profile_dir 优先，
// 否则用默认共享目录 —— 登录态跨任务复用靠它，否则每次委派都要重新登录。
//
// 默认路径必须在 workspaceRoot 之外：CleanupExpiredWorkspaces / ReapStale 按 TTL 删的是
// workspaceRoot 下的子目录，profile 落在里面就会连同登录态被扫走。
func (m *Manager) profileDir() (string, error) {
	if m.cfg.ProfileDir != "" {
		if !filepath.IsAbs(m.cfg.ProfileDir) {
			return "", fmt.Errorf("[browser] profile_dir 必须是绝对路径，当前为 %q", m.cfg.ProfileDir)
		}
		return m.cfg.ProfileDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "yagent-browser-profile"), nil
	}
	return filepath.Join(home, ".yagent", "data", "browser-profile"), nil
}

// ensureChrome 返回可用的 CDP 端点：attach 配置优先，否则复用已启动实例，最后才启动新的。
// 全程持锁：启动浏览器只应发生一次，并发委派排队复用同一个实例。
func (m *Manager) ensureChrome(ctx context.Context, workspaceDir string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.chrome != nil {
		if err := m.makeReadyLocked(ctx, m.chrome, workspaceDir); err != nil {
			return "", err
		}
		return m.chrome.cdpURL, nil
	}

	if m.cfg.CDPURL != "" || strings.EqualFold(m.cfg.Mode, ModeAttach) {
		if m.cfg.CDPURL == "" {
			return "", fmt.Errorf("[browser] mode = \"attach\" 需要同时配置 cdp_url")
		}
		proc := &chromeProc{cdpURL: m.cfg.CDPURL, ownerDir: workspaceDir, attached: true}
		if err := m.makeReadyLocked(ctx, proc, workspaceDir); err != nil {
			return "", fmt.Errorf("连接既有浏览器失败：%w", err)
		}
		m.chrome = proc
		return proc.cdpURL, nil
	}

	port, err := findFreePort(m.cfg.CDPPortBase)
	if err != nil {
		return "", err
	}

	// 上游把 --user-data-dir 硬编码成 `.browser-control/profile`（没有覆盖 flag），
	// 所以跨任务复用登录态只能靠把该目录 symlink 到共享 profile（[browser] profile_dir，
	// 为空则用默认共享目录）。attach 模式在上面就返回了，不会走到这里。
	if err := m.linkProfileLocked(workspaceDir); err != nil {
		return "", err
	}

	args := []string{"launch", "about:blank", "--port", strconv.Itoa(port)}
	if !strings.EqualFold(m.cfg.Mode, ModeHeadful) {
		args = append(args, "--headless")
	}

	out, err := m.spawn.Spawn(ctx, SpawnOpts{
		Name:    m.bin,
		Args:    args,
		Dir:     workspaceDir,
		Env:     m.sessionEnv(""), // launch 阶段还没有端点，靠 --port 指定
		Timeout: launchTimeout,
	})
	if err != nil {
		return "", fmt.Errorf("启动浏览器失败: %w", err)
	}
	if out.ExitCode != 0 {
		return "", fmt.Errorf("启动浏览器失败（exit %d）：%s", out.ExitCode,
			Clip(strings.TrimSpace(out.Stderr+out.Stdout), 400))
	}

	cdpURL := parseCDPURL(out.Stdout)
	if cdpURL == "" {
		return "", fmt.Errorf("launch 未返回 BROWSER_CONTROL_CDP_URL，输出：%s", Clip(out.Stdout, 200))
	}

	proc := &chromeProc{cdpURL: cdpURL, port: port, ownerDir: workspaceDir}
	if err := m.makeReadyLocked(ctx, proc, workspaceDir); err != nil {
		return "", err
	}
	m.chrome = proc
	m.startSweeperLocked()
	slog.Info("已启动浏览器", "cdp", cdpURL, "port", port, "workspace", workspaceDir)
	return cdpURL, nil
}

// makeReadyLocked 确认端点可用后装载一次浏览器侧策略。按 ready 标记幂等。
// 必须在持有 m.mu 时调用。
func (m *Manager) makeReadyLocked(ctx context.Context, proc *chromeProc, workspaceDir string) error {
	if proc.ready {
		return nil
	}
	env := m.sessionEnv(proc.cdpURL)
	if err := m.waitEndpointLocked(ctx, workspaceDir, env); err != nil {
		return err
	}
	proc.ready = true
	m.installPolicyLocked(ctx, workspaceDir, env)
	return nil
}

// waitEndpointLocked 轮询 `status` 直到 CDP 端点应答。
// 上游 launch 只 spawn 不等，Chrome 冷启动与首次建 profile 需要时间，
// 不等就会看到第一条命令报 "DevToolsActivePort not found"。
func (m *Manager) waitEndpointLocked(ctx context.Context, dir string, env []string) error {
	deadline := time.Now().Add(m.readyTimeout)
	var last SpawnOutput
	var lastErr error
	for {
		out, err := m.spawn.Spawn(ctx, SpawnOpts{
			Name:    m.bin,
			Args:    []string{"status"},
			Dir:     dir,
			Env:     env,
			Timeout: 5 * time.Second,
		})
		if err == nil && out.ExitCode == 0 {
			return nil
		}
		last, lastErr = out, err
		if !time.Now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(readyPoll):
		}
	}

	reason := strings.TrimSpace(last.Stderr + " " + last.Stdout)
	if lastErr != nil {
		reason = lastErr.Error()
	}
	return fmt.Errorf("浏览器 CDP 端点在 %s 内未就绪：%s", m.readyTimeout, Clip(reason, 300))
}

// linkProfileLocked 把上游硬编码的 `.browser-control/profile` 指向共享 profile 目录，
// 让登录态跨任务复用。默认（profile_dir 为空）也生效 —— 每个工作区一份新 profile
// 等于每次委派都要重新登录。
//
// 前提：共享 profile 意味着同一时刻只允许一个 Chrome 使用它。本 Manager 自启的 Chrome
// 全程只有一个（ensureChrome 持锁复用 m.chrome），attach 模式在更早的分支返回，不会走到这里。
// 必须在持有 m.mu 时调用。
func (m *Manager) linkProfileLocked(workspaceDir string) error {
	shared, err := m.profileDir()
	if err != nil {
		return err
	}
	explicit := m.cfg.ProfileDir != ""
	if err := os.MkdirAll(shared, 0o700); err != nil {
		return fmt.Errorf("创建 profile 目录失败: %w", err)
	}

	stateDir := filepath.Join(workspaceDir, ".browser-control")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("创建浏览器状态目录失败: %w", err)
	}
	link := filepath.Join(stateDir, "profile")

	if dest, err := os.Readlink(link); err == nil {
		if dest == shared {
			return nil
		}
		if err := os.Remove(link); err != nil {
			return fmt.Errorf("替换 profile symlink 失败: %w", err)
		}
	} else if info, err := os.Lstat(link); err == nil {
		// 旧版每工作区一份真实 profile 目录，里面可能就有用户上一次的 cookie：
		// 迁进共享目录而不是删掉。迁不动（或这东西根本不是目录）就保留本地副本继续工作 ——
		// 绝不删用户数据，也不阻断浏览器启动。
		if !info.IsDir() || !takeOverWorkspaceProfile(link, shared) {
			slog.Warn("共享 profile 无法接管该工作区的独立 profile，继续使用工作区本地副本",
				"path", link, "profile_dir", shared)
			return nil
		}
	}

	if err := os.Symlink(shared, link); err != nil {
		if explicit {
			return fmt.Errorf("链接 profile_dir 失败: %w", err)
		}
		// 默认路径：拿不到 symlink 权限（Windows 常见）时降级为每工作区独立 profile，
		// 不能让浏览器整体不可用。
		slog.Warn("共享 profile symlink 创建失败，本次委派使用工作区独立 profile",
			"path", link, "profile_dir", shared, "error", err)
		return nil
	}
	slog.Info("浏览器登录态 profile 已共享", "profile_dir", shared)
	return nil
}

// takeOverWorkspaceProfile 把旧版留在工作区里的真实 profile 目录迁到共享路径。
// 只接管空共享目录：非空说明别的委派正在用它。
//
// 先 os.Remove 再 os.Rename 而不是直接 rename：macOS 的 rename 不允许目录覆盖任何
// 已存在目录（哪怕空的，实测返回 EEXIST），Linux 允许 —— 两步写法两边都成立。
// 返回 true 表示 link 路径已腾空，可以建 symlink。
func takeOverWorkspaceProfile(link, shared string) bool {
	if entries, err := os.ReadDir(shared); err != nil || len(entries) > 0 {
		return false
	}
	if err := os.Remove(shared); err != nil {
		return false
	}
	if err := os.Rename(link, shared); err != nil {
		// 迁失败也要把共享目录建回来：它刚被删空，留着不存在会让下次 Acquire 的
		// 判据（"共享目录为空"）依赖一次 mkdir。
		if err := os.MkdirAll(shared, 0o700); err != nil {
			slog.Warn("恢复共享 profile 目录失败", "profile_dir", shared, "error", err)
		}
		return false
	}
	return true
}

// installPolicyLocked 让浏览器自身阻断本地文件与内网请求（参数级校验拦不住页面内跳转）。
// 失败只记 Debug：上游该设置是否跨 target/导航持续生效尚未核实，CheckURL 仍是主防线。
func (m *Manager) installPolicyLocked(ctx context.Context, dir string, env []string) {
	payload, err := m.policy.BlockedURLsPayload()
	if err != nil || payload == "" {
		return
	}
	out, err := m.spawn.Spawn(ctx, SpawnOpts{
		Name:    m.bin,
		Args:    []string{"cdp", "Network.setBlockedURLs", payload},
		Dir:     dir,
		Env:     env,
		Timeout: policyTimeout,
	})
	if err != nil || out.ExitCode != 0 {
		slog.Debug("浏览器侧 URL 阻断策略装载失败，退回参数级校验", "error", err, "exit", out.ExitCode)
		return
	}
	slog.Debug("浏览器侧 URL 阻断策略已装载", "blocked", payload)
}

// Close 停止自己启动的浏览器并停掉空闲回收协程。由 Yagent.Close() 调用。
func (m *Manager) Close(ctx context.Context) error {
	m.stopOnce.Do(func() { close(m.stopCh) })
	err := m.stopChrome(ctx, "shutdown")
	// stopCh 关闭后回收协程最迟在一个 ticker 周期内退出；这里等它，等不到就报名字。
	if leaked := m.grp.Drain(2 * time.Second); len(leaked) > 0 {
		slog.Warn("浏览器回收协程未退出", "leases", leaked)
	}
	return err
}

// stopChrome 关掉我们自己启动的浏览器。可重复调用：空闲回收后若再次启动，
// 回收协程仍在跑，会继续盯住新的实例。
func (m *Manager) stopChrome(ctx context.Context, reason string) error {
	m.mu.Lock()
	chrome := m.chrome
	m.chrome = nil
	m.mu.Unlock()

	if chrome == nil || chrome.attached {
		// attach 模式连的是用户自己的浏览器，绝不能由我们关闭
		return nil
	}
	bin := m.binary()
	if bin == "" {
		return nil
	}
	out, err := m.spawn.Spawn(ctx, SpawnOpts{
		Name:    bin,
		Args:    []string{"stop"},
		Dir:     chrome.ownerDir,
		Env:     m.sessionEnv(chrome.cdpURL),
		Timeout: launchTimeout,
	})
	if err != nil {
		return fmt.Errorf("停止浏览器失败: %w", err)
	}
	slog.Info("已停止 browser-control 启动的 Chrome",
		"reason", reason, "exit", out.ExitCode, "owner", chrome.ownerDir)
	return nil
}

// ReapStale 回收上次崩溃遗留的浏览器实例。
// 归属校验由上游 stop 负责（命令行需含 --remote-debugging-port 且 user-data-dir 匹配），
// 因此不会误杀用户自己的 Chrome。
func (m *Manager) ReapStale(ctx context.Context) {
	if runtime.GOOS == "windows" {
		return
	}
	bin := m.binary()
	root := m.workspaceRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		pidPath := filepath.Join(dir, ".browser-control", "chrome.pid")
		data, err := os.ReadFile(pidPath)
		if err != nil {
			continue
		}
		pid := strings.TrimSpace(string(data))
		if pid == "" {
			continue
		}
		// 死 pid 的记录不需要二进制就能清掉；只有活着且自己启动过的才需要 stop
		if !pidAlive(pid) {
			_ = os.Remove(pidPath)
			continue
		}
		if bin == "" {
			continue
		}
		out, err := m.spawn.Spawn(ctx, SpawnOpts{
			Name:    bin,
			Args:    []string{"stop"},
			Dir:     dir,
			Timeout: launchTimeout,
		})
		if err == nil && out.ExitCode == 0 {
			slog.Info("已回收遗留的浏览器实例", "workspace", e.Name(), "pid", pid)
		}
	}
}

// CleanupExpiredWorkspaces 删除超过 WorkspaceTTL 的工作区（含 traces 与截图产物）。
func (m *Manager) CleanupExpiredWorkspaces() {
	if m.cfg.WorkspaceTTL <= 0 {
		return
	}
	root := m.workspaceRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) < m.cfg.WorkspaceTTL {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, e.Name())); err == nil {
			slog.Info("已清理过期浏览器工作区", "workspace", e.Name(), "mtime", info.ModTime())
		}
	}
}

// startSweeperLocked 启动惰性回收：空闲超过 IdleTTL 就关掉浏览器。
// 必须在持有 m.mu 时调用。周期刻意用分钟级——精确回收时机不重要，
// 别让孤儿 Chrome 长期驻留才重要。
func (m *Manager) startSweeperLocked() {
	if m.sweeperStarted {
		return
	}
	m.sweeperStarted = true

	m.grp.Go("browserctl/idle-sweeper", func(context.Context) {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-m.stopCh:
				return
			case <-ticker.C:
				m.mu.Lock()
				hasChrome := m.chrome != nil
				lastUse := m.lastUse
				ttl := m.cfg.IdleTTL
				m.mu.Unlock()
				if !hasChrome || ttl <= 0 || lastUse.IsZero() {
					continue
				}
				if time.Since(lastUse) >= ttl {
					if err := m.stopChrome(context.Background(), "idle"); err != nil {
						slog.Warn("空闲回收浏览器失败", "error", err)
					} else {
						slog.Info("浏览器空闲超时，已关闭", "idle_ttl", ttl)
					}
				}
			}
		}
	})
}

func (m *Manager) touch() {
	m.mu.Lock()
	m.lastUse = time.Now()
	m.mu.Unlock()
}

func (m *Manager) binary() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bin
}

func (m *Manager) spawnOpts(command string, args []string, dir string, env []string) SpawnOpts {
	return SpawnOpts{
		Name:    m.binary(),
		Args:    append([]string{command}, args...),
		Dir:     dir,
		Env:     env,
		Timeout: m.commandTimeout(),
	}
}

// sessionEnv 构造注入给 CLI 的环境变量。
// 必须显式给 CDP 端点：上游在没有任何 CDP 变量时会硬扫 9222/9223，
// 会连到用户自己的调试浏览器或另一个 yagent 任务的页面上。
func (m *Manager) sessionEnv(cdpURL string) []string {
	env := append([]string{}, os.Environ()...)
	if cdpURL != "" {
		env = append(env, "BROWSER_CONTROL_CDP_URL="+cdpURL)
	}
	env = append(env, "BROWSER_CONTROL_TIMEOUT="+strconv.Itoa(int(m.cliTimeout().Seconds())))
	if m.cfg.ChromePath != "" {
		env = append(env, "BROWSER_CONTROL_CHROME="+m.cfg.ChromePath)
	}
	if os.Getenv("YAGENT_BROWSER_DEBUG") != "" {
		env = append(env, "BROWSER_CONTROL_DEBUG=1")
	}
	return env
}

// cliTimeout 是交给上游的内部 CDP 超时。
// 上游默认只有 15s，若不抬高，`wait load 60` 会在 15s 就失败；下限与 config 层共用一个常量。
func (m *Manager) cliTimeout() time.Duration {
	if m.cfg.CDPTimeout < config.MinBrowserCDPTimeout {
		return config.MinBrowserCDPTimeout
	}
	return m.cfg.CDPTimeout
}

// commandTimeout 是外层超时，必须大于内部超时，否则判定权被内层抢走
// ——模型只会看到"命令超时"，看不到上游给出的真实原因。
func (m *Manager) commandTimeout() time.Duration {
	if min := m.cliTimeout() + 10*time.Second; m.cfg.CommandTimeout < min {
		return min
	}
	return m.cfg.CommandTimeout
}

// findFreePort 从 base 起找一个可绑定端口。
// 刻意跳过 9222/9223：上游在没有 CDP 环境变量时会硬扫这两个端口。
func findFreePort(base int) (int, error) {
	for p := base; p < base+portScanRange; p++ {
		if p == 9222 || p == 9223 {
			continue
		}
		if portFree(p) {
			return p, nil
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, fmt.Errorf("找不到可用端口: %w", err)
	}
	defer ln.Close()
	if addr, ok := ln.Addr().(*net.TCPAddr); ok {
		return addr.Port, nil
	}
	return 0, fmt.Errorf("找不到可用端口")
}

func portFree(port int) bool {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// parseCDPURL 从 launch 的输出里取端点（上游固定打印 BROWSER_CONTROL_CDP_URL=http://...）。
func parseCDPURL(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		line = strings.TrimSpace(line)
		if v, ok := strings.CutPrefix(line, "BROWSER_CONTROL_CDP_URL="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// pidAlive 报告 pid 是否存活（signal 0 探测）。
func pidAlive(pid string) bool {
	n, err := strconv.Atoi(pid)
	if err != nil || n <= 0 {
		return false
	}
	proc, err := os.FindProcess(n)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
