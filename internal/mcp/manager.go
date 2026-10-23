package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"yagent/internal/supervise"
)

// reconnectBackoffBase 是重连退避的起点（之后指数翻倍，上限 5m）。
// 包级变量是测试的 seam：重连类测试不许 sleep 真实退避（根 AGENTS.md §8.3）。
var reconnectBackoffBase = 5 * time.Second

const (
	// maxReconnectBackoff 是退避上限：再长就等于放弃，只是状态看起来还在重试。
	maxReconnectBackoff = 5 * time.Minute
	// defaultStartupTimeout / defaultMaxReconnects / defaultMaxTools 是配置缺省值。
	// 只在装配层（config）与这里各有一份：config 的模板承诺与这里的兜底必须同值。
	defaultStartupTimeout = 30 * time.Second
	defaultMaxReconnects  = 3
	defaultMaxTools       = 40
	// defaultReadyTimeout 是等 initialize 完成的预算。它必须很长：codeseek 冷启动在
	// 建索引，改动前的实现就是拿 15 分钟的看门狗等它 —— 用 30s 去等会把一个健康的
	// server 判死并重开，而重开意味着索引从头再建。
	defaultReadyTimeout = 15 * time.Minute
)

// ServerConfig 是 Manager 启动一个 server 需要的全部信息。
// Manager 自己不读 config、不读环境变量：这些值由装配层算好传进来
// （与 internal/blueprint 不读环境变量是同一个决定）。
type ServerConfig struct {
	Name    string
	Command string
	Args    []string
	Env     map[string]string
	// WorkingDir 是子进程的工作目录（"" = 继承当前进程）。
	WorkingDir string
	// RequestTimeout 是单条 JSON-RPC 请求的 deadline；<=0 由 NewMCPClient 兜底 30s。
	RequestTimeout time.Duration
	// Enabled=false 的 server 不进任何面，状态行写明 disabled by config。
	Enabled bool

	// AllowWrite 决定写类工具是否注册（默认 false：写类不进任何画像）。
	AllowWrite bool
	// ReadOnly 是"虽然没有 annotations 但确实只读"的显式名单。
	ReadOnly []string
	// Tools 非空时是白名单；Exclude 是黑名单；MaxTools 是单 server 的工具上限。
	Tools    []string
	Exclude  []string
	MaxTools int
	// Curated 是"已由 Go 手写桥接、动态注册时要排除"的服务端工具名。
	Curated map[string]bool
}

// ServerState 是一个 server 的当前状态。
type ServerState int

const (
	// StateDisabled：配置里 enabled=false，不启动。
	StateDisabled ServerState = iota
	// StateStarting：进程已起，initialize / tools/list 还没完成。
	StateStarting
	// StateReady：进程活着且拿到了工具清单。
	StateReady
	// StateDegraded：进程活着但 tools/list 返回 0 个工具。
	// 与 ready 分开：宁缺毋滥，但必须在状态行里可辨（否则看起来像"这个 server 没有工具"）。
	StateDegraded
	// StateOffline：进程不在，正在重连（或重连次数用尽）。
	StateOffline
)

func (s ServerState) String() string {
	switch s {
	case StateDisabled:
		return "disabled"
	case StateStarting:
		return "starting"
	case StateReady:
		return "ready"
	case StateDegraded:
		return "degraded"
	default:
		return "offline"
	}
}

// CountsTools 报告该状态下工具数是否已经算出来了。
// starting（进程在、initialize/tools/list 还没回来）不算：把还没发生的事报成
// "ready — 0 tools" 是让状态行说谎，而状态行正是用户唯一能看到的解释。
func (s ServerState) CountsTools() bool { return s == StateReady || s == StateDegraded }

// ServerHealth 是一个 server 状态的快照。StatusLines/面板/事件都从它渲染，
// 不许在别处再拼一份格式（一个字符串三处出口，根 AGENTS.md §6.4）。
type ServerHealth struct {
	Name         string
	State        ServerState
	Error        string
	Attempt      int
	MaxAttempts  int
	Tools        int
	Curated      int
	Instructions string
}

// ServerTool 是"哪个 server 的哪个工具"。归属包在这里而不是塞进 DiscoveredTool：
// 后者是 tools/list 的线上形状，加字段就是撒谎。
type ServerTool struct {
	Server string
	Tool   DiscoveredTool
}

// ManagerOptions 是 Manager 的运行参数与装配钩子。
type ManagerOptions struct {
	// StartupTimeout 是**发现阶段**（tools/list）的上界，不是启动阻塞时间：
	// Start 只 spawn 子进程，initialize 与 tools/list 都在后台跑。
	StartupTimeout time.Duration
	// ReadyTimeout 是等 initialize 完成的预算（默认 15 分钟，见 defaultReadyTimeout）。
	ReadyTimeout     time.Duration
	ReconnectBackoff time.Duration
	MaxReconnects    int
	// StatusMaxLines 是状态账允许的 server 行数（含 disabled/offline），
	// 超出合并成一行 "+N more servers"。0 = 不限。
	StatusMaxLines int
	// OnStatusChange 在每次状态快照变化时被调用（装配层据此发事件）。
	// 回调在锁外调用，且不得阻塞 —— 它跑在状态迁移的那条 goroutine 上。
	OnStatusChange func(ServerHealth)
	// OnToolsChanged 在工具集真的变了（含重连后重新发现）时被调用，
	// 装配层据此让 BuildExternalSpecs 的缓存失效。
	OnToolsChanged func()
}

func (o ManagerOptions) withDefaults() ManagerOptions {
	if o.StartupTimeout <= 0 {
		o.StartupTimeout = defaultStartupTimeout
	}
	if o.ReadyTimeout <= 0 {
		o.ReadyTimeout = defaultReadyTimeout
	}
	if o.ReconnectBackoff <= 0 {
		o.ReconnectBackoff = reconnectBackoffBase
	}
	if o.MaxReconnects <= 0 {
		o.MaxReconnects = defaultMaxReconnects
	}
	return o
}

// serverEntry 是一个 server 的可变状态。所有字段都由 Manager.mu 保护。
type serverEntry struct {
	cfg   ServerConfig
	state ServerState
	err   string

	client       *MCPClient
	tools        []DiscoveredTool
	instructions string

	attempt      int
	reconnecting bool
	// stopped 表示 Manager 已关闭，重连循环必须退出。
	stopped bool
}

// Manager 管理多个 MCP server 的进程、状态与工具发现。
// 它不 import toolspec/tools：只吐 DiscoveredTool 这种纯数据，如何处理由装配层决定。
type Manager struct {
	mu      sync.RWMutex
	servers map[string]*serverEntry
	opts    ManagerOptions
	logger  *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc
	grp    *supervise.Group

	// changed 是状态变更的广播通道：每次快照变化就关闭它并换一只新的。
	// WaitForReady 靠它等待，不用 sleep 轮询。
	changed   chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

// NewManager 构造 Manager。Enabled=false 的 serve 直接落 StateDisabled。
func NewManager(cfgs []ServerConfig, opts ManagerOptions) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	grp, _ := supervise.New(ctx, "mcp-manager")
	m := &Manager{
		servers: make(map[string]*serverEntry, len(cfgs)),
		opts:    opts.withDefaults(),
		logger:  slog.Default().With("component", "mcp-manager"),
		ctx:     ctx,
		cancel:  cancel,
		grp:     grp,
		changed: make(chan struct{}),
	}
	for _, cfg := range cfgs {
		state := StateStarting
		if !cfg.Enabled {
			state = StateDisabled
		}
		// 同名后者覆盖前者是不可发生的：配置校验会拒绝重名（config.validate）。
		// Manager 不重复校验，但也不会静默合并两份配置。
		m.servers[cfg.Name] = &serverEntry{cfg: cfg, state: state, attempt: 0}
	}
	return m
}

// Start 为每个 enabled 的 server **同步** spawn 子进程，然后把 initialize/tools/list
// 丢给后台 goroutine。两件事分开是硬要求：
//   - 调用方（app.Init）拿到 Manager 后立刻要 `Client("codeseek")` —— 手写桥捕获的是那个
//     指针，晚一步就是永久 nil（改动前 app 也是同步 spawn + 后台 initialize）；
//   - spawn 只有 fork/exec 的成本，而 initialize 可能几分钟 —— 放在这里就是"启动卡在 MCP"。
//
// 进程起不来的 server 在 Start 里就落 offline 并排队重连（那条路径不会阻塞）。
func (m *Manager) Start(ctx context.Context) error {
	m.startOnce.Do(func() {
		m.mu.RLock()
		names := make([]string, 0, len(m.servers))
		for name, entry := range m.servers {
			if entry.state != StateDisabled {
				names = append(names, name)
			}
		}
		m.mu.RUnlock()
		sort.Strings(names)

		for _, name := range names {
			m.spawn(name)
		}
		m.logger.Info("mcp manager started", "servers", len(names))
	})
	return nil
}

// spawn 创建客户端并启动进程；成功即登记（状态仍是 starting），发现交给后台 goroutine。
func (m *Manager) spawn(name string) bool {
	m.mu.RLock()
	entry, ok := m.servers[name]
	m.mu.RUnlock()
	if !ok {
		return false
	}

	client := m.newClient(name)
	if err := client.Start(m.ctx); err != nil {
		m.markOffline(name, err.Error())
		m.scheduleReconnect(name)
		return false
	}

	m.mu.Lock()
	entry.client = client
	if entry.state != StateReady && entry.state != StateDegraded {
		entry.state = StateStarting
	}
	m.mu.Unlock()

	m.grp.Go("mcp/discover-"+name, func(context.Context) {
		m.discover(name, client)
	})
	return true
}

// newClient 构造客户端并挂上 list_changed 钩子。
func (m *Manager) newClient(name string) *MCPClient {
	cfg := m.configOf(name)
	client := NewMCPClient(MCPClientConfig{
		BinaryPath:     cfg.Command,
		Args:           cfg.Args,
		WorkingDir:     cfg.WorkingDir,
		Env:            cfg.Env,
		RequestTimeout: cfg.RequestTimeout,
	})
	client.SetOnListChanged(func() { m.refreshTools(name, client) })
	return client
}

func (m *Manager) configOf(name string) ServerConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if e, ok := m.servers[name]; ok {
		return e.cfg
	}
	return ServerConfig{}
}

// discover 等 initialize 完成后跑 tools/list。三条出口：
//   - 进程已死 → offline + 重连（重开是对的，管道已经不在了）；
//   - 进程活着但 tools/list 失败/超时 → degraded（不重开：一个能 tools/call
//     却不实现 tools/list 的 server 值得保留，只是没有动态工具）；
//   - 成功 → ready（0 个工具同样落 degraded，宁缺毋滥但在状态行里可辨）。
func (m *Manager) discover(name string, client *MCPClient) {
	readyCtx, cancelReady := context.WithTimeout(m.ctx, m.opts.ReadyTimeout)
	defer cancelReady()
	if err := client.WaitForReady(readyCtx); err != nil {
		if client.IsAlive() {
			// initialize 没回来但进程还在：留在 starting，让状态行说"正在启动"。
			// 判死并重开会把一次慢索引变成 N 次慢索引。
			m.logger.Warn("mcp server still initializing", "server", name, "error", err)
			return
		}
		m.markOffline(name, err.Error())
		m.scheduleReconnect(name)
		return
	}

	listCtx, cancelList := context.WithTimeout(m.ctx, m.opts.StartupTimeout)
	defer cancelList()
	tools, instructions, err := client.ListTools(listCtx)
	if err != nil {
		if !client.IsAlive() {
			m.markOffline(name, err.Error())
			m.scheduleReconnect(name)
			return
		}
		m.markDegraded(name, client, err.Error())
		return
	}
	m.markConnected(name, client, tools, instructions)
}

// WaitForReady 等到所有 server 落定（ready / degraded / offline），上界就是调用方的 ctx。
// 没有内部超时：装配路径**不等它**（见 Start 的注释），要用它的是测试与"我就是想要
// 一个同步点"的调用方，预算该由他们定。
// 只要没有 server 还停在 starting 就返回 nil —— 连不上不是 Manager 的错误，
// 状态行与 Skipped 账目才是它的出口。
func (m *Manager) WaitForReady(ctx context.Context) error {
	timeoutCtx := ctx

	for {
		m.mu.RLock()
		starting := false
		for _, e := range m.servers {
			if e.state == StateStarting {
				starting = true
				break
			}
		}
		changed := m.changed
		m.mu.RUnlock()
		if !starting {
			return nil
		}

		select {
		case <-changed:
		case <-timeoutCtx.Done():
			m.mu.RLock()
			pending := make([]string, 0, 2)
			for name, e := range m.servers {
				if e.state == StateStarting {
					pending = append(pending, name)
				}
			}
			m.mu.RUnlock()
			sort.Strings(pending)
			return fmt.Errorf("mcp wait timed out, still starting: %s (%w)",
				strings.Join(pending, ", "), ctx.Err())
		}
	}
}

// Client 返回某个 server 的客户端（供 codeseek 手写桥复用）。
// nil = 没有这个 server，或它当前没有活着的客户端。
func (m *Manager) Client(name string) *MCPClient {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if e, ok := m.servers[name]; ok {
		return e.client
	}
	return nil
}

// DiscoveredTools 返回全部 Ready/Degraded server 的工具（带归属）。
// 结果按 server 名、再按工具名排序：确定性是缓存与 golden 的前提。
func (m *Manager) DiscoveredTools() []ServerTool {
	m.refreshStates()

	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []ServerTool
	names := make([]string, 0, len(m.servers))
	for name, e := range m.servers {
		if e.state == StateReady || e.state == StateDegraded {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		tools := append([]DiscoveredTool(nil), m.servers[name].tools...)
		sort.SliceStable(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
		for _, t := range tools {
			out = append(out, ServerTool{Server: name, Tool: t})
		}
	}
	return out
}

// Call 把一次工具调用路由到对应的 server。
// server 名或工具名写错都返回错误 —— 静默落到别的 server 是这里最坏的失效形状。
func (m *Manager) Call(ctx context.Context, server, tool string, args map[string]interface{}) (*ToolCallResult, error) {
	m.mu.RLock()
	entry, ok := m.servers[server]
	if !ok {
		m.mu.RUnlock()
		return nil, fmt.Errorf("unknown MCP server %q", server)
	}
	client := entry.client
	state := entry.state
	reason := entry.err
	m.mu.RUnlock()

	if state != StateReady && state != StateDegraded {
		if reason == "" {
			reason = state.String()
		}
		return nil, fmt.Errorf("MCP server %q is %s: %s", server, state, reason)
	}
	if client == nil {
		return nil, fmt.Errorf("MCP server %q has no live client", server)
	}

	result, err := client.CallTool(ctx, tool, args)
	if err != nil && !client.IsAlive() {
		// 进程没了：立刻转 offline 并开始重连，别让下一次调用再撞一次死管道。
		m.markOffline(server, "process exited")
	}
	return result, err
}

// Status 返回一个 server 的状态快照（未知名字返回 StateDisabled + 原因）。
func (m *Manager) Status(name string) ServerHealth {
	m.refreshStates()
	m.mu.RLock()
	defer m.mu.RUnlock()
	e, ok := m.servers[name]
	if !ok {
		return ServerHealth{Name: name, State: StateDisabled, Error: "not configured"}
	}
	return m.healthOf(e)
}

// Health 返回全部 server 的状态快照，按名字排序。
func (m *Manager) Health() []ServerHealth {
	m.refreshStates()

	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]ServerHealth, 0, len(m.servers))
	for _, e := range m.servers {
		out = append(out, m.healthOf(e))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// healthOf 必须持锁调用。
func (m *Manager) healthOf(e *serverEntry) ServerHealth {
	h := ServerHealth{
		Name:         e.cfg.Name,
		State:        e.state,
		Error:        e.err,
		Attempt:      e.attempt,
		MaxAttempts:  m.opts.MaxReconnects,
		Tools:        len(e.tools),
		Instructions: e.instructions,
	}
	for _, t := range e.tools {
		if e.cfg.Curated[t.Name] {
			h.Curated++
		}
	}
	return h
}

// StatusLines 是状态账的唯一渲染实现：启动日志、/mcp 面板、system prompt 状态行
// 三处共用同一段文本（各渲染一份的结果是一边漂走，根 AGENTS.md §6.4）。
func (m *Manager) StatusLines() string {
	return RenderStatusLines(m.Health(), m.opts.StatusMaxLines)
}

// RenderStatusLines 把健康快照渲染成一行摘要 + 每 server 一行。
// 单独导出是为了让测试与装配层能对同一份快照断言"逐字相同"。
// maxLines <= 0 表示不限；超出时末尾写一行 "+N more servers"（截断必须写进正文）。
func RenderStatusLines(health []ServerHealth, maxLines int) string {
	if len(health) == 0 {
		return "mcp: no servers configured"
	}
	var (
		servers, tools, external, curated, offline int
		lines                                      []string
	)
	for i, h := range health {
		servers++
		if h.State == StateOffline {
			offline++
		}
		if h.State.CountsTools() {
			tools += h.Tools
			curated += h.Curated
			external += h.Tools - h.Curated
		}
		if maxLines > 0 && i >= maxLines {
			continue
		}
		lines = append(lines, "  "+FormatServerLine(h))
	}
	header := fmt.Sprintf("mcp: %d servers, %d tools (%d external + %d curated), %d offline",
		servers, tools, external, curated, offline)
	if hidden := len(health) - maxLines; maxLines > 0 && hidden > 0 {
		lines = append(lines, fmt.Sprintf("  +%d more servers", hidden))
	}
	return strings.Join(append([]string{header}, lines...), "\n")
}

// FormatServerLine 渲染单个 server 的一行状态。
// 状态账与 TUI 的事件行共用它 —— 各渲染一份的结果是同一件事两种说法（根 AGENTS.md §6.4）。
func FormatServerLine(h ServerHealth) string {
	switch h.State {
	case StateDisabled:
		return fmt.Sprintf("%s: disabled by config", h.Name)
	case StateOffline:
		return fmt.Sprintf("%s: offline — %s (attempt %d/%d); its %d tools are unavailable this run",
			h.Name, h.Error, h.Attempt, h.MaxAttempts, h.Tools)
	case StateDegraded:
		return fmt.Sprintf("%s: degraded — process alive but tools/list returned nothing", h.Name)
	case StateStarting:
		return fmt.Sprintf("%s: starting — initializing, tools not discovered yet", h.Name)
	default:
		return fmt.Sprintf("%s: ready — %d tools (%d curated, %d external)",
			h.Name, h.Tools, h.Curated, h.Tools-h.Curated)
	}
}

// ServerStateFromString 解析事件/配置里的状态名。认不出 → offline（fail-safe）：
// 把一个未知状态当成 ready 会让界面报"可用"而工具实际不来。
func ServerStateFromString(s string) ServerState {
	switch s {
	case "disabled":
		return StateDisabled
	case "starting":
		return StateStarting
	case "ready":
		return StateReady
	case "degraded":
		return StateDegraded
	default:
		return StateOffline
	}
}

// Close 优雅关闭全部 server，并等重连/连接 goroutine 真的退出。
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		m.cancel()
		m.mu.Lock()
		clients := make([]*MCPClient, 0, len(m.servers))
		for _, e := range m.servers {
			e.stopped = true
			if e.client != nil {
				clients = append(clients, e.client)
				e.client = nil
			}
		}
		m.mu.Unlock()

		for _, c := range clients {
			c.Shutdown()
		}
		m.drain()
	})
}

// ForceClose 逐个强杀（--force-quit）。语义与 MCPClient.ForceShutdown 对齐。
func (m *Manager) ForceClose() {
	m.closeOnce.Do(func() {
		m.cancel()
		m.mu.Lock()
		clients := make([]*MCPClient, 0, len(m.servers))
		for _, e := range m.servers {
			e.stopped = true
			if e.client != nil {
				clients = append(clients, e.client)
				e.client = nil
			}
		}
		m.mu.Unlock()

		for _, c := range clients {
			c.ForceShutdown()
		}
		m.drain()
	})
}

func (m *Manager) drain() {
	if leaked := m.grp.Drain(3 * time.Second); len(leaked) > 0 {
		m.logger.Warn("MCP manager 已停止，但仍有 goroutine 在跑", "leases", supervise.LeaseNames(leaked))
	}
}

// ---------- 状态迁移（全部在锁内接装配、锁外发通知）----------

// refreshTools 在 tools/list_changed 之后重新发现，并 diff 工具集。
// 变化非空才通知装配层重建动态规格 —— 空 diff 重算一遍 sha256 没有意义。
func (m *Manager) refreshTools(name string, client *MCPClient) {
	ctx, cancel := context.WithTimeout(m.ctx, m.opts.StartupTimeout)
	defer cancel()
	tools, instructions, err := client.ListTools(ctx)
	if err != nil {
		m.logger.Warn("mcp tools/list refresh failed", "server", name, "error", err)
		return
	}

	m.mu.Lock()
	entry, ok := m.servers[name]
	if !ok || entry.client != client {
		m.mu.Unlock()
		return
	}
	before := toolNames(entry.tools)
	entry.tools = tools
	entry.instructions = instructions
	if entry.state == StateReady && len(tools) == 0 {
		entry.state = StateDegraded
	}
	after := toolNames(tools)
	changed := entry.state == StateReady || entry.state == StateDegraded
	health := m.healthOf(entry)
	m.broadcastLocked()
	m.mu.Unlock()

	if !changed {
		return
	}
	m.emitStatus(health)
	if added, removed := diffNames(before, after); len(added) > 0 || len(removed) > 0 {
		m.logger.Info("mcp tool set changed", "server", name, "added", added, "removed", removed)
		if m.opts.OnToolsChanged != nil {
			m.opts.OnToolsChanged()
		}
	}
}

// markConnected 记录一次成功的连接/重连。
func (m *Manager) markConnected(name string, client *MCPClient, tools []DiscoveredTool, instructions string) {
	m.mu.Lock()
	entry, ok := m.servers[name]
	if !ok || entry.stopped {
		m.mu.Unlock()
		client.ForceShutdown()
		return
	}
	before := toolNames(entry.tools)
	old := entry.client
	entry.client = client
	entry.tools = tools
	entry.instructions = instructions
	entry.err = ""
	entry.attempt = 0
	entry.reconnecting = false
	if len(tools) == 0 {
		entry.state = StateDegraded
	} else {
		entry.state = StateReady
	}
	after := toolNames(tools)
	health := m.healthOf(entry)
	m.broadcastLocked()
	m.mu.Unlock()

	if old != nil && old != client {
		old.ForceShutdown()
	}
	m.emitStatus(health)

	// 连接落定之前到达的 list_changed 在 refreshTools 里被守卫跳过了（那时 entry.client
	// 还是 nil）。补一次重新发现，否则"启动瞬间工具变了"会一直停在旧清单上。
	if client.TakeListChangedPending() {
		m.grp.Go("mcp/refresh-"+name, func(context.Context) { m.refreshTools(name, client) })
	}

	if added, removed := diffNames(before, after); len(added) > 0 || len(removed) > 0 {
		m.logger.Info("mcp tool set changed", "server", name, "added", added, "removed", removed)
		if m.opts.OnToolsChanged != nil {
			m.opts.OnToolsChanged()
		}
	}
}

// markDegraded 用于"进程活着但没拿到工具清单"：不重开进程（那对一个不实现
// tools/list 的 server 是死循环），但必须在状态行里说清楚——静默当 ready
// 的表现是"这个 server 就是没有工具"。
func (m *Manager) markDegraded(name string, client *MCPClient, reason string) {
	m.mu.Lock()
	entry, ok := m.servers[name]
	if !ok || entry.stopped || entry.client != client {
		m.mu.Unlock()
		return
	}
	entry.state = StateDegraded
	entry.err = reason
	entry.tools = nil
	health := m.healthOf(entry)
	m.broadcastLocked()
	m.mu.Unlock()
	m.emitStatus(health)
}

// markOffline 把 server 转到 offline（幂等：已在 offline 时只更新原因）。
func (m *Manager) markOffline(name, reason string) {
	m.mu.Lock()
	entry, ok := m.servers[name]
	if !ok || entry.stopped {
		m.mu.Unlock()
		return
	}
	if entry.state == StateOffline && entry.err == reason {
		m.mu.Unlock()
		return
	}
	entry.state = StateOffline
	entry.err = reason
	client := entry.client
	entry.client = nil
	health := m.healthOf(entry)
	m.broadcastLocked()
	m.mu.Unlock()

	if client != nil {
		client.ForceShutdown()
	}
	m.emitStatus(health)
}

// refreshStates 在快照求值时顺带检查进程是否还活着。
// 不做轮询：readLoop 的 EOF 已经把 alive 置 false，这里只是"看一眼"。
// 需要重连时在锁外起 goroutine（锁内起 goroutine 会让调度不可预期）。
func (m *Manager) refreshStates() {
	type dead struct {
		name   string
		client *MCPClient
	}
	var found []dead

	m.mu.RLock()
	for name, e := range m.servers {
		if (e.state == StateReady || e.state == StateDegraded) && e.client != nil && !e.client.IsAlive() {
			found = append(found, dead{name: name, client: e.client})
		}
	}
	m.mu.RUnlock()

	for _, d := range found {
		m.markOffline(d.name, "process exited")
		m.scheduleReconnect(d.name)
	}
}

// scheduleReconnect 起一条具名重连 goroutine（同一 server 只允许一条）。
func (m *Manager) scheduleReconnect(name string) {
	m.mu.Lock()
	entry, ok := m.servers[name]
	if !ok || entry.stopped || entry.reconnecting || entry.cfg.Command == "" {
		m.mu.Unlock()
		return
	}
	entry.reconnecting = true
	m.mu.Unlock()

	m.grp.Go("mcp/reconnect-"+name, func(context.Context) {
		m.reconnectLoop(name)
	})
}

func (m *Manager) reconnectLoop(name string) {
	backoff := m.opts.ReconnectBackoff
	// base 是第一次失败的原因：每一轮都往它后面拼，而不是往上一轮的结果后面拼
	// （否则状态行会变成 "… (retry 1/3) (retry 2/3) (retry 3/3)"）。
	m.mu.RLock()
	entry0, ok := m.servers[name]
	base := ""
	if ok {
		base = entry0.err
	}
	m.mu.RUnlock()

	for attempt := 1; attempt <= m.opts.MaxReconnects; attempt++ {
		m.mu.Lock()
		entry, ok := m.servers[name]
		if !ok || entry.stopped {
			m.mu.Unlock()
			return
		}
		entry.attempt = attempt
		entry.err = fmt.Sprintf("%s (retry %d/%d)", base, attempt, m.opts.MaxReconnects)
		health := m.healthOf(entry)
		m.broadcastLocked()
		m.mu.Unlock()
		m.emitStatus(health)
		m.logger.Info("mcp reconnecting", "server", name, "attempt", attempt, "in", backoff)

		select {
		case <-time.After(backoff):
		case <-m.ctx.Done():
			return
		}
		backoff *= 2
		if backoff > maxReconnectBackoff {
			backoff = maxReconnectBackoff
		}

		if m.tryConnect(name) {
			return
		}
	}

	m.mu.Lock()
	entry, ok := m.servers[name]
	if !ok || entry.stopped {
		m.mu.Unlock()
		return
	}
	entry.reconnecting = false
	entry.err = fmt.Sprintf("gave up after %d attempts: %s", m.opts.MaxReconnects, entry.err)
	entry.state = StateOffline
	health := m.healthOf(entry)
	m.broadcastLocked()
	m.mu.Unlock()
	m.emitStatus(health)
	m.logger.Warn("mcp reconnect gave up", "server", name, "attempts", m.opts.MaxReconnects)
}

// tryConnect 重开进程并就地等发现完成（重连循环需要"这一轮到底成没成"的答复，
// 所以不像 Start 那样把 discover 丢到后台）。
func (m *Manager) tryConnect(name string) bool {
	m.mu.RLock()
	entry, ok := m.servers[name]
	stopped := entry != nil && entry.stopped
	m.mu.RUnlock()
	if !ok || stopped {
		return false
	}

	client := m.newClient(name)
	if err := client.Start(m.ctx); err != nil {
		m.logger.Warn("mcp reconnect: start failed", "server", name, "error", err)
		return false
	}
	m.mu.Lock()
	entry.client = client
	m.mu.Unlock()

	if err := m.awaitDiscovery(name, client); err != nil {
		m.logger.Warn("mcp reconnect: discovery failed", "server", name, "error", err)
		client.ForceShutdown()
		return false
	}
	return true
}

// awaitDiscovery 是 discover 的同步版本（重连循环用）。
func (m *Manager) awaitDiscovery(name string, client *MCPClient) error {
	readyCtx, cancelReady := context.WithTimeout(m.ctx, m.opts.ReadyTimeout)
	defer cancelReady()
	if err := client.WaitForReady(readyCtx); err != nil {
		return err
	}
	listCtx, cancelList := context.WithTimeout(m.ctx, m.opts.StartupTimeout)
	defer cancelList()
	tools, instructions, err := client.ListTools(listCtx)
	if err != nil {
		return err
	}
	m.markConnected(name, client, tools, instructions)
	return nil
}

// broadcastLocked 关闭当前广播通道并换一只新的。必须持锁调用。
func (m *Manager) broadcastLocked() {
	close(m.changed)
	m.changed = make(chan struct{})
}

func (m *Manager) emitStatus(h ServerHealth) {
	if m.opts.OnStatusChange != nil {
		m.opts.OnStatusChange(h)
	}
}

func toolNames(tools []DiscoveredTool) map[string]bool {
	out := make(map[string]bool, len(tools))
	for _, t := range tools {
		out[t.Name] = true
	}
	return out
}

func diffNames(before, after map[string]bool) (added, removed []string) {
	for name := range after {
		if !before[name] {
			added = append(added, name)
		}
	}
	for name := range before {
		if !after[name] {
			removed = append(removed, name)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

// DefaultMaxToolsPerServer 是装配层构造 ServerConfig 时的工具数缺省值。
func DefaultMaxToolsPerServer() int { return defaultMaxTools }
