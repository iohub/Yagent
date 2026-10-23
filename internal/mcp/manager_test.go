package mcp

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Manager 的测试全部对着假服务器（重新执行测试二进制），不依赖 codeseek。
// 重连退避走 reconnectBackoffBase / ManagerOptions.ReconnectBackoff 两个 seam，
// 没有一条用例靠 time.Sleep 等真实退避。

func startManager(t *testing.T, cfgs []ServerConfig, opts ManagerOptions) *Manager {
	t.Helper()
	if opts.ReconnectBackoff == 0 {
		opts.ReconnectBackoff = 2 * time.Millisecond
	}
	if opts.MaxReconnects == 0 {
		opts.MaxReconnects = 2
	}
	m := NewManager(cfgs, opts)
	t.Cleanup(m.ForceClose)

	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.WaitForReady(ctx); err != nil {
		t.Fatalf("WaitForReady: %v", err)
	}
	return m
}

func fakeServerConfig(name, mode string) ServerConfig {
	return ServerConfig{
		Name:    name,
		Command: os.Args[0],
		Env:     map[string]string{fakeServerEnv: mode, fakeServerEnv + "_TAG": name},
		Enabled: true,
	}
}

// waitFor 轮询直到 cond 为真；超时只返回 false，由调用方断言。
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

func TestManager_ReadyExposesTools(t *testing.T) {
	m := startManager(t, []ServerConfig{fakeServerConfig("alpha", "tools-list")}, ManagerOptions{})

	tools := m.DiscoveredTools()
	if len(tools) != 3 {
		t.Fatalf("DiscoveredTools() = %d 条, want 3", len(tools))
	}
	for _, st := range tools {
		if st.Server != "alpha" {
			t.Errorf("%s 的归属 = %q, want alpha", st.Tool.Name, st.Server)
		}
	}
	h := m.Status("alpha")
	if h.State != StateReady {
		t.Errorf("State = %v, want ready（err=%q）", h.State, h.Error)
	}
	if h.Tools != 3 || h.Curated != 0 {
		t.Errorf("Health = %+v, want 3 tools / 0 curated", h)
	}
	if !strings.Contains(m.StatusLines(), "alpha: ready — 3 tools") {
		t.Errorf("StatusLines() = %q", m.StatusLines())
	}
	// instructions 收下了，而不是像改动前那样丢掉。
	if h.Instructions == "" {
		t.Error("Instructions 为空：initialize 带回的说明被丢了")
	}
}

func TestManager_DegradedOnEmptyList(t *testing.T) {
	m := startManager(t, []ServerConfig{fakeServerConfig("empty", "empty-list")}, ManagerOptions{})

	h := m.Status("empty")
	if h.State != StateDegraded {
		t.Fatalf("State = %v, want degraded（进程活着但 0 个工具不能被当成 ready）", h.State)
	}
	if len(m.DiscoveredTools()) != 0 {
		t.Error("degraded server 不该暴露任何工具")
	}
	if !strings.Contains(m.StatusLines(), "degraded") {
		t.Errorf("StatusLines() = %q, want 点名 degraded", m.StatusLines())
	}
}

func TestManager_DisabledServerStaysOutOfFaces(t *testing.T) {
	cfg := fakeServerConfig("off", "tools-list")
	cfg.Enabled = false
	m := startManager(t, []ServerConfig{cfg}, ManagerOptions{})

	h := m.Status("off")
	if h.State != StateDisabled {
		t.Errorf("State = %v, want disabled", h.State)
	}
	if got := m.DiscoveredTools(); len(got) != 0 {
		t.Errorf("disabled server 暴露了 %d 个工具", len(got))
	}
	if !strings.Contains(m.StatusLines(), "off: disabled by config") {
		t.Errorf("StatusLines() = %q, want 写明 disabled by config", m.StatusLines())
	}
	// disabled 的 server 没有客户端：codeseek 桥拿到的必须是 nil，而不是一条死管道。
	if m.Client("off") != nil {
		t.Error("disabled server 竟然有客户端")
	}
}

// 进程崩了 → 转 offline → 退避重连 → 重新 tools/list → 工具集恢复。
// 只测"重连成功"不够：重连之后**没有重新发现**的话，工具面会永远空着。
func TestManager_ReconnectsAfterCrash(t *testing.T) {
	changed := make(chan struct{}, 4)
	m := startManager(t, []ServerConfig{fakeServerConfig("crashy", "tools-list")}, ManagerOptions{
		OnToolsChanged: func() {
			select {
			case changed <- struct{}{}:
			default:
			}
		},
	})

	client := m.Client("crashy")
	if client == nil || client.cmd == nil || client.cmd.Process == nil {
		t.Fatal("没有可杀的子进程，夹具失效")
	}
	if err := client.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}

	if !waitFor(3*time.Second, func() bool { return m.Status("crashy").State == StateOffline }) {
		t.Fatalf("进程已死，状态仍是 %v", m.Status("crashy").State)
	}
	if !waitFor(5*time.Second, func() bool {
		return m.Status("crashy").State == StateReady && len(m.DiscoveredTools()) == 3
	}) {
		t.Fatalf("没有重连成功：%+v", m.Status("crashy"))
	}
	select {
	case <-changed:
	default:
		// 重连前后工具集相同 ⇒ diff 为空 ⇒ 不通知装配层重建。这是刻意的（省一次 sha256），
		// 所以这里不断言一定要收到信号。
	}
	// 重连之后 attempt 必须归零，否则状态行会一直显示"第 3/3 次重连"。
	if h := m.Status("crashy"); h.Attempt != 0 || h.Error != "" {
		t.Errorf("重连成功后仍留着失败痕迹：%+v", h)
	}
}

func TestManager_GivesUpAfterMaxAttempts(t *testing.T) {
	cfg := ServerConfig{Name: "ghost", Command: "/nonexistent/yagent-fake-mcp", Enabled: true}
	m := startManager(t, []ServerConfig{cfg}, ManagerOptions{MaxReconnects: 2, ReconnectBackoff: time.Millisecond})

	if !waitFor(3*time.Second, func() bool {
		return strings.Contains(m.Status("ghost").Error, "gave up")
	}) {
		t.Fatalf("重连次数用尽后状态 = %+v, want 写明 gave up", m.Status("ghost"))
	}
	h := m.Status("ghost")
	if h.State != StateOffline {
		t.Errorf("State = %v, want offline", h.State)
	}
	if !strings.Contains(m.StatusLines(), "offline") || !strings.Contains(m.StatusLines(), "unavailable this run") {
		t.Errorf("StatusLines() = %q, want 一行可读的离线账", m.StatusLines())
	}
}

func TestManager_CallRoutesToRightServer(t *testing.T) {
	m := startManager(t, []ServerConfig{
		fakeServerConfig("srv-a", "tools-list"),
		fakeServerConfig("srv-b", "tools-list"),
	}, ManagerOptions{})

	for _, srv := range []string{"srv-a", "srv-b"} {
		res, err := m.Call(context.Background(), srv, "echo", map[string]interface{}{"payload": "p"})
		if err != nil {
			t.Fatalf("Call(%s): %v", srv, err)
		}
		if got := res.Content[0].Text; got != srv+":echo:p" {
			t.Errorf("Call(%s) = %q, want 回显带上自己的 server 标记（串台会把 A 的结果发给 B）", srv, got)
		}
	}

	if _, err := m.Call(context.Background(), "no-such-server", "echo", nil); err == nil ||
		!strings.Contains(err.Error(), "no-such-server") {
		t.Errorf("未知 server 的 err = %v, want 点名 server", err)
	}
}

func TestManager_CallToOfflineServerExplains(t *testing.T) {
	m := startManager(t, []ServerConfig{fakeServerConfig("dying", "tools-list")}, ManagerOptions{})
	client := m.Client("dying")
	if err := client.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill: %v", err)
	}
	if !waitFor(3*time.Second, func() bool {
		_, err := m.Call(context.Background(), "dying", "echo", nil)
		return err != nil
	}) {
		t.Fatal("进程已死，Call 仍然成功")
	}
	// 把 server 钉在离线态：重连会把状态推回 ready，这里只看"离线时怎么说"。
	m.cancel()
	_, err := m.Call(context.Background(), "dying", "echo", nil)
	if err == nil || !strings.Contains(err.Error(), "dying") {
		t.Errorf("离线时 err = %v, want 点名 server 与状态", err)
	}
}

func TestManager_ConcurrentCallsStayFramed(t *testing.T) {
	m := startManager(t, []ServerConfig{fakeServerConfig("busy", "tools-list")}, ManagerOptions{})

	const n = 24
	var wg sync.WaitGroup
	errs := make([]error, n)
	got := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := m.Call(context.Background(), "busy", fmt.Sprintf("t%d", i), map[string]interface{}{"payload": fmt.Sprintf("p%d", i)})
			if err != nil {
				errs[i] = err
				return
			}
			got[i] = res.Content[0].Text
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Errorf("并发调用 %d: %v", i, errs[i])
			continue
		}
		want := fmt.Sprintf("busy:t%d:p%d", i, i)
		if got[i] != want {
			t.Errorf("并发调用 %d = %q, want %q（串台会把结果配错）", i, got[i], want)
		}
	}
}

// 状态行、面板、启动日志必须来自同一份快照与同一个渲染函数。
// 各渲染一份的结果是一边漂走，而用户看到的是两个互相矛盾的界面。
func TestManager_StatusLinesIsSingleSource(t *testing.T) {
	m := startManager(t, []ServerConfig{fakeServerConfig("one", "tools-list")}, ManagerOptions{})

	first := m.StatusLines()
	if second := m.StatusLines(); first != second {
		t.Errorf("同一状态两次渲染不一致：%q vs %q", first, second)
	}
	if want := RenderStatusLines(m.Health(), 0); first != want {
		t.Errorf("StatusLines() 与 RenderStatusLines(Health()) 不一致：%q vs %q", first, want)
	}
	if !strings.HasPrefix(first, "mcp: 1 servers, 3 tools (3 external + 0 curated), 0 offline") {
		t.Errorf("摘要行 = %q", strings.SplitN(first, "\n", 2)[0])
	}
}

// Start 只 spawn，不等发现：这是"终端启动卡在 MCP"的回归闸门。
// 假服务器的 initialize 慢 700ms（像 codeseek 冷启动），阈值 150ms —— 把 discover
// 挪回 Start 里，这条必然红，而不是碰运气。
func TestManager_StartDoesNotBlockOnDiscovery(t *testing.T) {
	m := NewManager([]ServerConfig{fakeServerConfig("slow", "slow-initialize")}, ManagerOptions{
		StartupTimeout: 5 * time.Second,
		ReadyTimeout:   5 * time.Second,
	})
	t.Cleanup(m.ForceClose)

	start := time.Now()
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Errorf("Start 花了 %v, want 只付 spawn 的成本（发现必须在后台）", elapsed)
	}
	if h := m.Status("slow"); h.State != StateStarting {
		t.Errorf("Start 返回时状态 = %v, want starting（发现还没回来）", h.State)
	}
	// 客户端在 Start 返回时就该可取：手写桥在装配期捕获这个指针，
	// 晚一步就是永久 nil（那比慢更糟）。
	if m.Client("slow") == nil {
		t.Error("Start 返回后 Client() 仍为 nil —— codeseek 那类桥接会永久捕获 nil 客户端")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.WaitForReady(ctx); err != nil {
		t.Fatalf("WaitForReady: %v", err)
	}
	if h := m.Status("slow"); h.State != StateReady || h.Tools != 3 {
		t.Errorf("后台发现结束后 = %+v, want ready + 3 tools", h)
	}
}

// 进程活着但 tools/list 失败/超时 ⇒ degraded，而且**不重开进程**：
// 一个能 tools/call 却不实现 tools/list 的 server，重开是无限循环 + 白烧索引。
func TestManager_LiveServerWithFailedListIsDegradedNotRestarted(t *testing.T) {
	m := NewManager([]ServerConfig{fakeServerConfig("slow", "slow-list")}, ManagerOptions{
		StartupTimeout: 50 * time.Millisecond,
		ReadyTimeout:   5 * time.Second,
	})
	t.Cleanup(m.ForceClose)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	first := m.Client("slow")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.WaitForReady(ctx); err != nil {
		t.Fatalf("WaitForReady: %v", err)
	}
	if h := m.Status("slow"); h.State != StateDegraded {
		t.Fatalf("State = %v, want degraded", h.State)
	}
	if m.Client("slow") != first {
		t.Error("degraded 之后客户端被换掉了：慢启动的 server 会被反复重开")
	}
	if !first.IsAlive() {
		t.Error("degraded 判死了进程：它只是没交工具清单")
	}
}

// initialize 迟迟不回来（codeseek 在建索引）时不许判死：留在 starting，
// 进程保留，状态行说"还在启动"。
func TestManager_SlowInitializeStaysStarting(t *testing.T) {
	m := NewManager([]ServerConfig{fakeServerConfig("quiet", "no-initialize")}, ManagerOptions{
		StartupTimeout: 50 * time.Millisecond,
		ReadyTimeout:   100 * time.Millisecond,
	})
	t.Cleanup(m.ForceClose)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := m.WaitForReady(ctx); err == nil || !strings.Contains(err.Error(), "quiet") {
		t.Fatalf("WaitForReady = %v, want 超时并点名还在 starting 的 server", err)
	}
	h := m.Status("quiet")
	if h.State != StateStarting {
		t.Errorf("State = %v, want starting（判死并重开会把一次慢索引变成 N 次）", h.State)
	}
	if c := m.Client("quiet"); c == nil || !c.IsAlive() {
		t.Error("initialize 慢就被杀了：进程应当保留")
	}
}

func TestManager_ListChangedRefreshesTools(t *testing.T) {
	changed := make(chan struct{}, 4)
	m := startManager(t, []ServerConfig{fakeServerConfig("hot", "list-changed")}, ManagerOptions{
		OnToolsChanged: func() {
			select {
			case changed <- struct{}{}:
			default:
			}
		},
	})

	// 首次发现只有 1 个工具；通知之后应当变成 2 个并触发一次重建。
	if !waitFor(3*time.Second, func() bool { return len(m.DiscoveredTools()) == 2 }) {
		t.Fatalf("tools/list_changed 之后工具集没刷新：%d 条", len(m.DiscoveredTools()))
	}
	select {
	case <-changed:
	case <-time.After(time.Second):
		t.Error("工具集真的变了，却没有通知装配层重建动态规格")
	}
}

// ---------- 客户端层：分页、说明、页数上限 ----------

func TestMCPClient_ListToolsPaginates(t *testing.T) {
	c := startFakeClient(t, "tools-list-paged")
	tools, instructions, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	if strings.Join(names, ",") != "alpha,beta" {
		t.Errorf("分页结果 = %v, want 两页都收到（只读第一页会静默少工具）", names)
	}
	if instructions == "" {
		t.Error("instructions 为空：initialize 带回的说明被丢了")
	}
}

func TestMCPClient_ListToolsPageCapErrors(t *testing.T) {
	c := startFakeClient(t, "tools-list-endless")
	if _, _, err := c.ListTools(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "nextCursor") {
		t.Fatalf("err = %v, want 页数上限报错而不是静默截断", err)
	}
}

func TestMCPClient_ListToolsRequiresReady(t *testing.T) {
	c := NewMCPClient(MCPClientConfig{BinaryPath: os.Args[0]})
	if _, _, err := c.ListTools(context.Background()); err == nil {
		t.Fatal("未启动的客户端 ListTools 却成功了")
	}
}

// 注解是能力判定的唯一来源，三态必须原样带出来（P3 依赖它区分"没声明"与"声明为 false"）。
func TestMCPClient_ListToolsCarriesAnnotations(t *testing.T) {
	c := startFakeClient(t, "tools-list")
	tools, _, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	byName := map[string]DiscoveredTool{}
	for _, tool := range tools {
		byName[tool.Name] = tool
	}
	if a, ok := byName["alpha"]; !ok || a.Annotations == nil || a.Annotations.ReadOnlyHint == nil || !*a.Annotations.ReadOnlyHint {
		t.Errorf("alpha annotations = %+v, want readOnlyHint=true", byName["alpha"].Annotations)
	}
	if b, ok := byName["beta"]; !ok || b.Annotations == nil || b.Annotations.ReadOnlyHint == nil || *b.Annotations.ReadOnlyHint {
		t.Errorf("beta annotations = %+v, want readOnlyHint=false（与'没声明'必须可区分）", byName["beta"].Annotations)
	}
	if g, ok := byName["gamma"]; !ok || g.Annotations != nil {
		t.Errorf("gamma annotations = %+v, want nil", byName["gamma"].Annotations)
	}
}

// 还没发现完的 server 不许被写成 "ready — 0 tools"：状态行是用户唯一能读到的解释，
// 说谎的那一行会让人以为这个 server 本来就没有工具。
func TestManager_StartingIsNotReportedAsReady(t *testing.T) {
	m := NewManager([]ServerConfig{fakeServerConfig("quiet", "no-initialize")}, ManagerOptions{
		ReadyTimeout: time.Hour,
	})
	t.Cleanup(m.ForceClose)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}

	line := m.StatusLines()
	if !strings.Contains(line, "quiet: starting") {
		t.Errorf("状态行 = %q, want 写明 starting", line)
	}
	if strings.Contains(line, "ready — 0 tools") {
		t.Errorf("状态行把未发现的 server 说成 ready：%q", line)
	}
	if !strings.Contains(line, "mcp: 1 servers, 0 tools") {
		t.Errorf("摘要把还没算出的工具计了数：%q", line)
	}
}
