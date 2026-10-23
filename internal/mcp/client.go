package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"yagent/internal/supervise"
)

var ErrIndexBuilding = errors.New("codeseek index is still building")

const IndexBuildingMessage = "索引构建中，所有 repo 检索工具（semantic_search/query_code_skeleton/query_code_snippet/find_function_caller/find_function_callee/query_call_graph）暂不可用。请改用标准文件工具：read_file（读取文件内容）、list_dir（列出目录）、search_by_regex（正则搜索文件内容）、print_dir_tree（查看目录结构）继续工作。索引完成后 repo 工具将自动恢复可用，无需反复重试。"

type MCPClientConfig struct {
	BinaryPath string

	Args []string

	WorkingDir string

	// Env 是追加给子进程的环境变量（覆盖同名项）。第三方 server（github 这类）
	// 靠它拿 token；nil = 继承当前进程环境。
	Env map[string]string

	// RequestTimeout 是**单条 JSON-RPC 请求**自己的 deadline，与调用方的 ctx 取小。
	// 它存在的理由：CallTool 从工具线程上被调用，调用方常常只给了任务 ctx（没有
	// deadline）。子进程不回包时，"永远转圈"的那条 goroutine 会把整个委派挂住。
	// <=0 → defaultMCPRequestTimeout（两份配置模板承诺的 "≤0 → 30" 就是这一句）。
	RequestTimeout time.Duration
}

// defaultMCPRequestTimeout 是调用方没给上限时的兜底 deadline。
const defaultMCPRequestTimeout = 30 * time.Second

type MCPClient struct {
	cfg    MCPClientConfig
	logger *slog.Logger
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	out    *bufio.Reader

	mu       sync.Mutex
	requests map[int]chan *JSONRPCResponse
	nextID   atomic.Int64
	// writeMu 保护 stdin 管道写入：os.File.Write 只对 ≤PIPE_BUF 保证原子，
	// 并发只读工具（semantic_search 那批）的大请求会交错成非法 JSON-RPC。
	writeMu sync.Mutex

	alive       atomic.Bool
	initialized atomic.Bool
	stopped     atomic.Bool

	// instructions 是 initialize 带回的"这个 server 怎么用"。
	// 单独用 atomic.Value 存：IsAlive 只准读两个原子位，不能顺手把这条也读进去。
	instructions atomic.Value // string

	// hookMu 保护 onListChanged：readLoop 每次收到通知都要读它，
	// 而 Manager 在启动前设置一次。
	hookMu        sync.RWMutex
	onListChanged func()
	// listChangedPending 记住"连接还没落定时收到过 list_changed"。
	// 没有它的话，initialize 期间到达的通知会被 refreshTools 里的
	// "entry.client != client" 守卫丢掉 —— 那个窗口正是通知最可能到达的时候。
	listChangedPending atomic.Bool

	readyCh    chan struct{}
	initErr    error
	closeReady sync.Once
	cancelInit context.CancelFunc
	// grp 拥有读循环与后台 initialize。原来 Shutdown 从不 join 它们：
	// 于是"退出了但还有一条 goroutine 抓着 stdout 管道"是可能状态，
	// 而表现是子进程僵在那里、日志里却没有任何线索。
	grp *supervise.Group
}

func NewMCPClient(cfg MCPClientConfig) *MCPClient {
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = defaultMCPRequestTimeout
	}
	grp, _ := supervise.New(context.Background(), "mcp")
	return &MCPClient{
		cfg:      cfg,
		logger:   slog.Default().With("component", "mcp"),
		requests: make(map[int]chan *JSONRPCResponse),
		readyCh:  make(chan struct{}),
		grp:      grp,
	}
}

func (c *MCPClient) Start(ctx context.Context) error {
	cmdArgs := append([]string{}, c.cfg.Args...)
	cmd := exec.CommandContext(ctx, c.cfg.BinaryPath, cmdArgs...)

	if c.cfg.WorkingDir != "" {
		cmd.Dir = c.cfg.WorkingDir
	}
	if len(c.cfg.Env) > 0 {
		env := append([]string{}, os.Environ()...)
		for k, v := range c.cfg.Env {
			env = append(env, k+"="+v)
		}
		cmd.Env = env
	}

	cmd.Stderr = &captureWriter{logger: c.logger}

	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start MCP server: %w", err)
	}

	c.cmd = cmd
	c.stdin = stdinPipe
	c.out = bufio.NewReaderSize(stdoutPipe, 1024*1024)

	c.alive.Store(true)

	c.grp.Go("mcp/read-loop", func(context.Context) { c.readLoop() })

	initCtx, cancel := context.WithCancel(context.Background())
	c.cancelInit = cancel

	c.grp.Go("mcp/initialize", func(context.Context) {
		defer cancel()
		c.logger.Info("MCP client initialization started in background")

		if err := c.performInitialize(initCtx); err != nil {
			c.markReady(fmt.Errorf("initialization failed: %w", err))
		} else {
			c.markReady(nil)
		}
	})

	return nil
}

func (c *MCPClient) performInitialize(ctx context.Context) error {
	initReq := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      int(c.nextID.Add(1)),
		Method:  "initialize",
		Params: mustMarshal(InitializeParams{
			ProtocolVersion: "2024-11-05",
			Capabilities:    ClientCapabilities{},
			ClientInfo: ClientInfo{
				Name:    "yagent",
				Version: "0.1.0",
			},
		}),
	}

	result, err := c.doRequest(ctx, initReq)
	if err != nil {
		return fmt.Errorf("initialize request failed: %w", err)
	}

	var initResult InitializeResult
	if err := json.Unmarshal(result.Result, &initResult); err != nil {
		return fmt.Errorf("failed to unmarshal initialize result: %w", err)
	}

	if initResult.Instructions != "" {
		c.instructions.Store(initResult.Instructions)
	}
	c.logger.Info("MCP server info",
		"name", initResult.ServerInfo.Name,
		"version", initResult.ServerInfo.Version,
		"protocol", initResult.ProtocolVersion,
		"has_instructions", initResult.Instructions != "",
	)

	notif := JSONRPCNotification{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	if err := c.sendJSON(notif); err != nil {
		return fmt.Errorf("failed to send initialized notification: %w", err)
	}

	return nil
}

func (c *MCPClient) markReady(err error) {
	c.closeReady.Do(func() {
		if err != nil {
			c.initErr = err
			c.logger.Warn("MCP client initialization failed", "error", err)
		} else {
			c.initialized.Store(true)
			c.logger.Info("MCP client initialized", "binary", c.cfg.BinaryPath, "args", c.cfg.Args)
		}
		close(c.readyCh)
	})
}

func (c *MCPClient) CallTool(ctx context.Context, toolName string, arguments map[string]interface{}) (*ToolCallResult, error) {
	if !c.IsAlive() {
		return nil, errors.New("MCP client is not alive")
	}

	if !c.initialized.Load() {
		return nil, fmt.Errorf("%w: %s", ErrIndexBuilding, IndexBuildingMessage)
	}
	params := ToolCallParams{
		Name:      toolName,
		Arguments: arguments,
	}

	req := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      int(c.nextID.Add(1)),
		Method:  "tools/call",
		Params:  mustMarshal(params),
	}

	result, err := c.doRequest(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("tool call failed: %w", err)
	}

	var toolResult ToolCallResult
	if err := json.Unmarshal(result.Result, &toolResult); err != nil {
		return nil, fmt.Errorf("failed to unmarshal tool result: %w", err)
	}

	return &toolResult, nil
}

// Instructions 返回 initialize 带回的服务端说明（没有就是空串）。
func (c *MCPClient) Instructions() string {
	v, _ := c.instructions.Load().(string)
	return v
}

// TakeListChangedPending 取走"连接落定前收到过 list_changed"的标记。
// Manager 在 markConnected 之后用它补一次重新发现。
func (c *MCPClient) TakeListChangedPending() bool { return c.listChangedPending.Swap(false) }

// SetOnListChanged 注册 tools/list_changed 通知的回调。
// Manager 用它触发"重新发现 + diff"，而不是让通知停在一条日志里。
func (c *MCPClient) SetOnListChanged(fn func()) {
	c.hookMu.Lock()
	c.onListChanged = fn
	c.hookMu.Unlock()
}

// maxToolsListPages 是分页跟随的硬上限。超过就报错而不是静默截断 ——
// "少了一批工具"与"这个 server 就这些"在界面上长得一模一样。
const maxToolsListPages = 20

// ListTools 取回服务端的全部工具声明，自动跟随 nextCursor 分页。
// 第二个返回值是 initialize 时缓存的服务端说明。
//
// 请求本身全部走既有的 doRequest：deadline 取小、writeMu 写锁、乱序配对都继承，
// 传输层一行不用重写。
func (c *MCPClient) ListTools(ctx context.Context) ([]DiscoveredTool, string, error) {
	if !c.IsAlive() {
		return nil, "", errors.New("MCP client is not alive")
	}
	if !c.initialized.Load() {
		return nil, "", errors.New("MCP client is not initialized yet")
	}

	var (
		all    []DiscoveredTool
		cursor string
	)
	for page := 0; page < maxToolsListPages; page++ {
		req := JSONRPCRequest{
			JSONRPC: "2.0",
			ID:      int(c.nextID.Add(1)),
			Method:  "tools/list",
			Params:  mustMarshal(ListToolsParams{Cursor: cursor}),
		}
		resp, err := c.doRequest(ctx, req)
		if err != nil {
			return nil, "", fmt.Errorf("tools/list failed: %w", err)
		}
		var result ListToolsResult
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			return nil, "", fmt.Errorf("failed to unmarshal tools/list result: %w", err)
		}
		all = append(all, result.Tools...)
		if result.NextCursor == "" {
			return all, c.Instructions(), nil
		}
		cursor = result.NextCursor
	}
	return nil, "", fmt.Errorf("tools/list exceeded %d pages — server keeps returning nextCursor", maxToolsListPages)
}

// IsAlive 只读那两个原子位。原来这里还读 `cmd.ProcessState` —— 而那个字段是
// `cmd.Wait()` 写的（Shutdown/ForceShutdown 里），任何线程查活都可能在 Wait
// 正在落笔时读它，是一次数据竞态；而它要给的结论（进程已经没了）readLoop 看到
// stdout EOF 时已经写进 alive 了，所以删掉它不丢信息。
// c.cmd 也不再在这里读：Start 之前 alive 恒为 false，语义不变。
func (c *MCPClient) IsAlive() bool {
	return c.alive.Load() && !c.stopped.Load()
}

func (c *MCPClient) Shutdown() {
	if c.stopped.Swap(true) {
		return
	}

	if c.cancelInit != nil {
		c.cancelInit()
	}

	c.markReady(errors.New("MCP client shut down before initialization completed"))

	c.alive.Store(false)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	shutdownReq := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      int(c.nextID.Add(1)),
		Method:  "shutdown",
	}
	_, _ = c.doRequest(ctx, shutdownReq)

	if c.stdin != nil {
		c.stdin.Close()
	}

	if c.cmd != nil && c.cmd.ProcessState == nil {
		c.cmd.Wait()
	}

	c.drainDomain()
	c.logger.Info("MCP client shut down")
}

// drainDomain 等读循环与后台 initialize 真的退出。
// 两条 Shutdown 路径都必须等：readLoop 抓着 stdout 管道时，子进程可能因为
// 还连着 stdin 而不退出，而日志里只会留下"MCP client shut down"这句假话。
func (c *MCPClient) drainDomain() {
	if leaked := c.grp.Drain(3 * time.Second); len(leaked) > 0 {
		c.logger.Warn("MCP 客户端已停止，但仍有 goroutine 在跑", "leases", supervise.LeaseNames(leaked))
	}
}

func (c *MCPClient) ForceShutdown() {
	if c.stopped.Swap(true) {
		return
	}

	if c.cancelInit != nil {
		c.cancelInit()
	}

	c.markReady(errors.New("MCP client force shut down"))

	c.alive.Store(false)

	if c.stdin != nil {
		c.stdin.Close()
	}

	if c.cmd != nil && c.cmd.Process != nil {
		if err := c.cmd.Process.Kill(); err != nil {
			c.logger.Warn("Failed to kill MCP subprocess", "error", err)
		} else {
			c.logger.Info("MCP subprocess forcefully killed")
		}
		_ = c.cmd.Wait()
	}

	c.drainDomain()
	c.logger.Info("MCP client force shut down")
}

func (c *MCPClient) WaitForReady(ctx context.Context) error {
	select {
	case <-c.readyCh:
		return c.initErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *MCPClient) IsReady() bool {
	return c.initialized.Load()
}

func (c *MCPClient) InitError() error {
	select {
	case <-c.readyCh:
		return c.initErr
	default:
		return nil
	}
}

func (c *MCPClient) doRequest(ctx context.Context, req JSONRPCRequest) (*JSONRPCResponse, error) {
	// 每条请求自己的 deadline（取小：调用方给得更短时听调用方的）。没有这一句的话，
	// 子进程不回包时这条 select 就只能等调用方那个往往没有 deadline 的 ctx ——
	// 表现是那条委派永远转圈。
	if c.cfg.RequestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.cfg.RequestTimeout)
		defer cancel()
	}

	ch := make(chan *JSONRPCResponse, 1)

	c.mu.Lock()
	c.requests[req.ID] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.requests, req.ID)
		c.mu.Unlock()
	}()

	if err := c.sendJSON(req); err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	select {
	case resp, ok := <-ch:
		if !ok {
			return nil, errors.New("response channel closed")
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("RPC error [%d]: %s", resp.Error.Code, resp.Error.Message)
		}
		return resp, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("request timed out or cancelled: %w", ctx.Err())
	}
}

func (c *MCPClient) readLoop() {
	defer func() {
		c.alive.Store(false)
		c.mu.Lock()
		for id, ch := range c.requests {
			close(ch)
			delete(c.requests, id)
		}
		c.mu.Unlock()
		c.logger.Info("MCP read loop ended")
	}()

	for {
		line, err := c.out.ReadBytes('\n')
		if err != nil {
			if err != io.EOF {
				c.logger.Error("MCP read error", "error", err)
			}
			return
		}

		var msg map[string]interface{}
		if err := json.Unmarshal(line, &msg); err != nil {
			c.logger.Warn("MCP invalid JSON", "data", string(line))
			continue
		}

		if method, ok := msg["method"].(string); ok {
			c.handleNotification(method, msg)
			continue
		}

		var resp JSONRPCResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			c.logger.Warn("MCP invalid response JSON", "data", string(line))
			continue
		}

		c.mu.Lock()
		ch, found := c.requests[resp.ID]
		c.mu.Unlock()

		if found {
			select {
			case ch <- &resp:
			default:
			}
		} else {
			c.logger.Warn("MCP response for unknown request ID", "id", resp.ID)
		}
	}
}

func (c *MCPClient) handleNotification(method string, msg map[string]interface{}) {
	c.logger.Debug("MCP notification received", "method", method)

	switch method {
	case "notifications/tools/list_changed":
		c.logger.Info("MCP tools list changed notification received")
		c.listChangedPending.Store(true)
		c.hookMu.RLock()
		hook := c.onListChanged
		c.hookMu.RUnlock()
		if hook != nil {
			// 不能同步调用：回调里会发 tools/list，而响应要靠本方法所在的 readLoop 去读 ——
			// 同步调就是"读循环等自己"的死锁，表现是重发现永远超时。
			c.grp.Go("mcp/list-changed", func(context.Context) { hook() })
		}
	default:
		c.logger.Debug("MCP unknown notification", "method", method)
	}
}

func (c *MCPClient) sendJSON(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	// 并发只读工具（semantic_search / find_function_* 等）会同时往同一条管道写请求。
	// 超过 PIPE_BUF 的单次 write 不保证原子，两条消息会交错成非法 JSON-RPC，
	// readLoop 只能报 "invalid JSON" 并把两个在飞请求一起拖到超时。
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err = c.stdin.Write(append(data, '\n'))
	if err != nil {
		return fmt.Errorf("failed to write to stdin: %w", err)
	}
	return nil
}

func mustMarshal(v interface{}) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mcp: failed to marshal: %v", err))
	}
	return data
}

type captureWriter struct {
	logger *slog.Logger
}

func (w *captureWriter) Write(p []byte) (int, error) {
	for _, line := range bytesLines(p) {
		w.logger.Warn("MCP server stderr", "line", string(line))
	}
	return len(p), nil
}

func bytesLines(data []byte) [][]byte {
	var lines [][]byte
	var current []byte
	for _, b := range data {
		if b == '\n' {
			if len(current) > 0 {
				lines = append(lines, current)
			}
			current = nil
		} else {
			current = append(current, b)
		}
	}
	if len(current) > 0 {
		lines = append(lines, current)
	}
	return lines
}
