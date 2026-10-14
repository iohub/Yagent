package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

// 本包是一个自研的 stdio JSON-RPC 2.0 客户端，对着 codeseek 子进程说话。
// 它没有单元测试脚手架可比：一旦请求/响应配不上对，表现是某个检索工具"永远转圈"
// 或者把 A 工具的结果当成 B 工具的结果交给 LLM —— 后者是会污染上下文的正确性事故。
//
// 假服务器用"重新执行测试二进制"的方式起（TestMain 里认环境变量），
// 不依赖 codeseek 二进制、也不需要 go run。

const fakeServerEnv = "YAGENT_TEST_FAKE_MCP_SERVER"

func TestMain(m *testing.M) {
	if os.Getenv(fakeServerEnv) != "" {
		runFakeMCPServer()
		return
	}
	os.Exit(m.Run())
}

// fakeRequest 是假服务器读到的一条请求。
type fakeRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func writeLine(w *bufio.Writer, payload any) {
	data, _ := json.Marshal(payload)
	_, _ = w.Write(data)
	_ = w.WriteByte('\n')
	_ = w.Flush()
}

func runFakeMCPServer() {
	mode := os.Getenv(fakeServerEnv)
	out := bufio.NewWriterSize(os.Stdout, 1<<20)
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<25)

	var pending []fakeRequest // 供 reverse 模式攒批

	for in.Scan() {
		line := in.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var req fakeRequest
		if err := json.Unmarshal(line, &req); err != nil {
			// 帧被写坏了（并发写交错的典型症状）：把证据留在 stderr。
			fmt.Fprintf(os.Stderr, "MALFORMED_FRAME %d bytes: %v\n", len(line), err)
			continue
		}

		switch req.Method {
		case "initialize":
			writeLine(out, JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: mustMarshal(InitializeResult{
					ProtocolVersion: "2024-11-05",
					ServerInfo:      ServerInfo{Name: "fake-codeseek", Version: "test"},
				}),
			})
		case "notifications/initialized", "no_reply":
			// 通知与"故意不回"都没有响应：前者是协议规定，后者用来测超时路径
		case "shutdown":
			writeLine(out, JSONRPCResponse{JSONRPC: "2.0", ID: req.ID, Result: mustMarshal(map[string]any{"ok": true})})
			os.Exit(0)
		case "tools/call":
			var p ToolCallParams
			_ = json.Unmarshal(req.Params, &p)
			switch mode {
			case "junk-first":
				// 干扰行：非法 JSON、通知、以及一个没人等的响应 ID。
				// 客户端必须全部跳过，且不影响后面的正常响应。
				_, _ = out.WriteString("this is not json\n")
				writeLine(out, JSONRPCNotification{JSONRPC: "2.0", Method: "notifications/message", Params: mustMarshal(map[string]any{"msg": "hi"})})
				writeLine(out, JSONRPCResponse{JSONRPC: "2.0", ID: 999999, Result: mustMarshal(map[string]any{"orphan": true})})
				_ = out.Flush()
			case "reverse":
				pending = append(pending, req)
				if len(pending) < 2 {
					continue // 攒够两条再倒序回，制造确定性的乱序
				}
				for i := len(pending) - 1; i >= 0; i-- {
					writeToolResult(out, pending[i])
				}
				pending = pending[:0]
				continue
			case "slow":
				time.Sleep(300 * time.Millisecond)
			case "die-on-call":
				if p.Name == "die" {
					os.Exit(0) // 不给响应直接消失
				}
			}
			writeToolResult(out, req)
		default:
			writeLine(out, JSONRPCResponse{
				JSONRPC: "2.0", ID: req.ID,
				Error: &JSONRPCError{Code: -32601, Message: "method not found: " + req.Method},
			})
		}
	}
}

// writeToolResult 用**这条请求自己**的 name/payload 回显：乱序模式下两条请求
// 攒在一起统一回复，若用"最后一条的上下文"回显就测不出串台。
func writeToolResult(out *bufio.Writer, req fakeRequest) {
	var p ToolCallParams
	_ = json.Unmarshal(req.Params, &p)
	payload, _ := p.Arguments["payload"].(string)
	writeLine(out, JSONRPCResponse{
		JSONRPC: "2.0",
		ID:      req.ID,
		Result: mustMarshal(ToolCallResult{
			Content: []ToolContent{{Type: "text", Text: fmt.Sprintf("%s:%s", p.Name, payload)}},
		}),
	})
}

// startFakeClient 起假服务器并等到初始化完成。
func startFakeClient(t *testing.T, mode string) *MCPClient {
	t.Helper()
	// 用环境变量而不是命令行 flag 选模式：测试二进制会拒绝未知 -flag。
	c := NewMCPClient(MCPClientConfig{BinaryPath: os.Args[0], RequestTimeout: 2 * time.Second})
	startFakeClientWithEnv(t, c, mode)

	t.Cleanup(func() { c.ForceShutdown() })

	if err := c.WaitForReady(context.Background()); err != nil {
		t.Fatalf("假 MCP 服务器初始化失败: %v", err)
	}
	if !c.IsReady() {
		t.Fatal("IsReady() = false 而 WaitForReady 已返回 nil")
	}
	return c
}

func startFakeClientWithEnv(t *testing.T, c *MCPClient, mode string) {
	t.Helper()
	cmd := exec.Command(c.cfg.BinaryPath)
	cmd.Env = append(os.Environ(), fakeServerEnv+"="+mode)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start fake server: %v", err)
	}

	c.mu.Lock()
	c.cmd = cmd
	c.stdin = stdin
	c.out = bufio.NewReaderSize(stdout, 1<<20)
	c.mu.Unlock()
	c.alive.Store(true)

	go c.readLoop()
	if err := c.performInitialize(context.Background()); err != nil {
		t.Fatalf("performInitialize: %v", err)
	}
	c.markReady(nil)
}

func callTool(t *testing.T, c *MCPClient, name, payload string) string {
	t.Helper()
	res, err := c.CallTool(context.Background(), name, map[string]interface{}{"payload": payload})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	if len(res.Content) != 1 {
		t.Fatalf("content = %+v, want 一块文本", res.Content)
	}
	return res.Content[0].Text
}

// ---------- 正常路径 ----------

func TestMCPClient_InitializeAndCallTool(t *testing.T) {
	c := startFakeClient(t, "plain")

	if got := callTool(t, c, "codeseek_search", "q1"); got != "codeseek_search:q1" {
		t.Errorf("结果 = %q, want 工具名与入参回显", got)
	}

	// 同一个客户端连续调用必须复用同一条管道并按 ID 配对。
	if got := callTool(t, c, "codeseek_callees", "q2"); got != "codeseek_callees:q2" {
		t.Errorf("第二次 = %q, want 各自的回显", got)
	}
	if !c.IsAlive() {
		t.Error("IsAlive() = false，正常调用之后应当仍然存活")
	}
}

// 协议形状：jsonrpc 版本、method、params 里的 name/arguments。
// codeseek 侧只认这三个字段，写错就是"工具存在但永远调不通"。
func TestMCPClient_WireFormat(t *testing.T) {
	c := startFakeClient(t, "plain")
	callTool(t, c, "codeseek_skeleton", "p")

	// 通过下一次调用把观察点搬回进程内：假服务器不解释未知方法，
	// 这里直接检查客户端自己组装的 JSONRPCRequest 结构。
	req := JSONRPCRequest{JSONRPC: "2.0", ID: 7, Method: "tools/call", Params: mustMarshal(ToolCallParams{
		Name: "codeseek_snippet", Arguments: map[string]interface{}{"qualified_name": "Foo.Bar"},
	})}
	var back map[string]any
	if err := json.Unmarshal(mustMarshal(req), &back); err != nil {
		t.Fatalf("请求不可序列化: %v", err)
	}
	if back["jsonrpc"] != "2.0" || back["method"] != "tools/call" {
		t.Errorf("线上格式 = %v, want jsonrpc=2.0 method=tools/call", back)
	}
	params, _ := back["params"].(map[string]any)
	if params["name"] != "codeseek_snippet" {
		t.Errorf("params = %v, want name 为 MCP 侧工具名", params)
	}
	args, _ := params["arguments"].(map[string]any)
	if args["qualified_name"] != "Foo.Bar" {
		t.Errorf("arguments = %v, want 原样透传 yagent 侧参数", args)
	}
	if id, _ := back["id"].(float64); id != 7 {
		t.Errorf("id = %v, want 数字 7（配对的键）", back["id"])
	}
}

// 未知方法必须换成带 code 的错误，而不是 nil error + 空结果。
func TestMCPClient_RPCErrorSurfaces(t *testing.T) {
	c := startFakeClient(t, "plain")
	_, err := c.doRequest(context.Background(), JSONRPCRequest{JSONRPC: "2.0", ID: int(c.nextID.Add(1)), Method: "tools/list"})
	if err == nil {
		t.Fatal("假服务器返回了 error 响应，客户端却报成功")
	}
	if !strings.Contains(err.Error(), "-32601") || !strings.Contains(err.Error(), "method not found") {
		t.Errorf("err = %v, want 带上 code 与 provider 的 message", err)
	}
}

// ---------- 乱序 / 噪声 / 并发 ----------

// 响应乱序到达必须仍按 ID 配回各自的调用方。
// 这是 requests map 的全部意义；配错的后果是把一个工具的检索结果喂给另一个工具。
func TestMCPClient_OutOfOrderResponsesPairCorrectly(t *testing.T) {
	c := startFakeClient(t, "reverse")

	var wg sync.WaitGroup
	got := make([]string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			res, err := c.CallTool(ctx, fmt.Sprintf("tool%d", i), map[string]interface{}{"payload": fmt.Sprintf("p%d", i)})
			if err != nil {
				t.Errorf("CallTool %d: %v", i, err)
				return
			}
			got[i] = res.Content[0].Text
		}(i)
	}
	wg.Wait()

	if got[0] != "tool0:p0" || got[1] != "tool1:p1" {
		t.Errorf("结果 = %q，want 各自配回自己的调用（乱序即串台）", got)
	}
}

// 管道上混进非法 JSON、通知、无人认领的响应时，正常请求不能被带崩。
func TestMCPClient_ToleratesJunkAndNotifications(t *testing.T) {
	c := startFakeClient(t, "junk-first")

	for i := 0; i < 3; i++ {
		if got := callTool(t, c, "codeseek_callers", fmt.Sprintf("n%d", i)); got != fmt.Sprintf("codeseek_callers:n%d", i) {
			t.Errorf("第 %d 次 = %q, want 正常回显", i+1, got)
		}
	}
	if !c.IsAlive() {
		t.Error("噪声行让 readLoop 退出了")
	}
}

// 并发只读工具（toolbatch 会同时发多个检索请求）：大请求交错会让双方都超时。
func TestMCPClient_ConcurrentLargeCallsStayFramed(t *testing.T) {
	c := startFakeClient(t, "plain")

	const n = 40
	big := strings.Repeat("payload-", 512) // ~4KB，远超 PIPE_BUF 的一半量级
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, errs[i] = c.CallTool(ctx, fmt.Sprintf("t%d", i), map[string]interface{}{"payload": fmt.Sprintf("%s#%d", big, i)})
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("并发调用 %d 失败: %v（帧交错会让两条请求一起烂掉）", i, err)
		}
	}
}

// ---------- 超时与进程死亡 ----------

func TestMCPClient_CallTimeoutLeavesClientUsable(t *testing.T) {
	c := startFakeClient(t, "plain")

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// 假服务器对 no_reply 只读不回，于是这条请求必然走到超时分支。
	_, err := c.doRequest(ctx, JSONRPCRequest{JSONRPC: "2.0", ID: 424242, Method: "no_reply"})
	if err == nil {
		t.Fatal("服务器不回应时 doRequest 却成功返回")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), "timed out") {
		t.Errorf("err = %v, want 说明超时/取消", err)
	}

	// 超时后在飞表必须被清掉（defer delete），否则 ID 会一直被占用。
	c.mu.Lock()
	_, stuck := c.requests[424242]
	c.mu.Unlock()
	if stuck {
		t.Error("超时之后 requests map 里仍留着那条在飞请求")
	}

	// 客户端仍然可用。
	if got := callTool(t, c, "after-timeout", "x"); got != "after-timeout:x" {
		t.Errorf("超时后的新调用 = %q", got)
	}
}

// 子进程中途消失时，在飞调用必须被解栈（返回错误而不是永久挂起），
// 且客户端从此报告"不存活"。
func TestMCPClient_ProcessDeathReleasesPendingCalls(t *testing.T) {
	c := startFakeClient(t, "die-on-call")

	_, err := c.CallTool(context.Background(), "die", nil)
	if err == nil {
		t.Fatal("服务器在收到该请求后立刻退出，CallTool 却成功了")
	}
	if !strings.Contains(err.Error(), "response channel closed") && !strings.Contains(err.Error(), "tool call failed") {
		t.Errorf("err = %v, want 说明管道/响应通道已断", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for c.IsAlive() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if c.IsAlive() {
		t.Error("子进程已退出，IsAlive() 仍然报告存活")
	}

	// 之后的调用必须被明确拒绝，而不是发进一条死管道。
	if _, err := c.CallTool(context.Background(), "anything", nil); err == nil {
		t.Error("客户端已死却仍然接受调用")
	}
}

// ---------- 未初始化 / 生命周期 ----------

// 索引构建期间（initialize 还没完成）调用工具，必须回落到
// ErrIndexBuilding + 那句"请改用文件工具"的提示。Agent 靠它决定换工具还是重试。
func TestCallToolBeforeReadyIsIndexBuilding(t *testing.T) {
	// 进程活着但 initialized 还没置上 —— 就是 codeseek 索引构建期的真实形状。
	c := startFakeClient(t, "plain")
	c.initialized.Store(false)

	_, err := c.CallTool(context.Background(), "codeseek_search", nil)
	if !errors.Is(err, ErrIndexBuilding) {
		t.Fatalf("err = %v, want ErrIndexBuilding（包装链不能断，调用方按 errors.Is 判定）", err)
	}
	if !strings.Contains(err.Error(), IndexBuildingMessage) {
		t.Errorf("err = %v, want 带上可执行的替代方案文案", err)
	}
	if !strings.Contains(err.Error(), "read_file") {
		t.Errorf("提示里没有点名可用的文件工具: %v", err)
	}

	// 进程不存活时是另一种错误，不能被误报成"索引构建中"（那会让 Agent 白等）。
	dead := NewMCPClient(MCPClientConfig{BinaryPath: os.Args[0]})
	if _, err := dead.CallTool(context.Background(), "codeseek_search", nil); err == nil ||
		errors.Is(err, ErrIndexBuilding) {
		t.Errorf("未启动的客户端 err = %v, want 非 ErrIndexBuilding 的明确错误", err)
	}
}

func TestMCPClient_ShutdownIsIdempotent(t *testing.T) {
	c := startFakeClient(t, "plain")

	c.Shutdown() // 正常收尾：发 shutdown 请求 → 等进程退出
	c.Shutdown() // 第二次必须直接返回，不得重复 kill/wait

	if c.IsAlive() {
		t.Error("Shutdown 之后 IsAlive() 仍为 true")
	}
	// InitError 只描述"初始化那次尝试"的结论：启动成功过就恒为 nil，
	// Shutdown 不能事后改写它（否则 app 层会把"已就绪后关闭"误判成"从未配上"）。
	if err := c.InitError(); err != nil {
		t.Errorf("InitError() = %v, want nil（初始化当时是成功的）", err)
	}

	// ForceShutdown 在已停的客户端上同样无害。
	c.ForceShutdown()
}

func TestMCPClient_ForceShutdownKillsSubprocess(t *testing.T) {
	c := startFakeClient(t, "plain")
	pid := c.cmd.Process.Pid

	c.ForceShutdown()
	c.ForceShutdown() // 幂等

	// 进程必须真的没了（kill + Wait 会写下 ProcessState），
	// 否则 codeseek 会留下孤儿进程与索引锁。
	// 唯一的强不变量：进程已被 Wait 收尸（ProcessState 非空），不留僵尸/孤儿。
	// 不断言死法 —— stdin 先关闭时子进程可能已经自己退出 0，Kill() 就只是空跑；
	// 真实 codeseek 的退出时机同样不由我们决定。
	if ps := c.cmd.ProcessState; ps == nil {
		t.Errorf("pid %d 的 ProcessState = nil, want 已被 Wait 收尸（否则留下孤儿 codeseek）", pid)
	}
	if c.IsAlive() {
		t.Error("ForceShutdown 之后 IsAlive() = true")
	}
}

func TestNewMCPClientDefaults(t *testing.T) {
	c := NewMCPClient(MCPClientConfig{BinaryPath: "/nope"})
	if c.cfg.RequestTimeout != 0 {
		t.Errorf("cfg.RequestTimeout = %v, want 保持调用方给的零值（默认 30s 只用于内部等待）", c.cfg.RequestTimeout)
	}
	if c.IsReady() {
		t.Error("新建客户端 IsReady() = true")
	}
	if c.IsAlive() {
		t.Error("新建客户端 IsAlive() = true")
	}
	if err := c.InitError(); err != nil {
		t.Errorf("还没尝试初始化就报 InitError: %v", err)
	}
	// WaitForReady 在没人 markReady 时必须能被 ctx 解栈。
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.WaitForReady(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("WaitForReady = %v, want 超时", err)
	}
}

func TestMustMarshalPanicsOnUnmarshalable(t *testing.T) {
	// mustMarshal 的 panic 是刻意的：JSON-RPC 参数无法序列化时不能发半条消息出去。
	defer func() {
		if r := recover(); r == nil {
			t.Error("mustMarshal(不可序列化值) 没有 panic")
		}
	}()
	mustMarshal(map[string]any{"bad": func() {}})
}

func TestCaptureWriterSplitsStderrLines(t *testing.T) {
	h := &collectHandler{}
	c := NewMCPClient(MCPClientConfig{BinaryPath: "/nope"})
	c.logger = slog.New(h)

	w := &captureWriter{logger: c.logger}
	n, err := w.Write([]byte("第一行\n第二行\n\n"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len("第一行\n第二行\n\n") {
		t.Errorf("写入 %d 字节, want 全部（少报会让 io.Copy 认为出错）", n)
	}
	if got := strings.Count(h.joined(), "MCP server stderr"); got != 2 {
		t.Errorf("stderr 被记成 %d 条, want 按行拆成 2 条（空行不该也占一条）", got)
	}
}

func TestBytesLines(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"a", []string{"a"}},
		{"a\nb", []string{"a", "b"}},
		{"a\nb\n", []string{"a", "b"}},
		{"\n\n", nil},
		{"a\n\nb", []string{"a", "b"}},
	}
	for _, tc := range cases {
		got := bytesLines([]byte(tc.in))
		if len(got) != len(tc.want) {
			t.Errorf("bytesLines(%q) = %q, want %q", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if string(got[i]) != tc.want[i] {
				t.Errorf("bytesLines(%q)[%d] = %q, want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

// collectHandler 收集 slog 记录，用来断言"stderr 有没有按行拆"。
type collectHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (h *collectHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *collectHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgs = append(h.msgs, r.Message)
	return nil
}

func (h *collectHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *collectHandler) WithGroup(string) slog.Handler      { return h }

func (h *collectHandler) joined() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return strings.Join(h.msgs, "\n")
}
