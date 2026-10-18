package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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

	RequestTimeout time.Duration
}

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
	// knowledge 写入这类大请求并发时会交错成非法 JSON-RPC。
	writeMu sync.Mutex

	alive       atomic.Bool
	initialized atomic.Bool
	stopped     atomic.Bool

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

	c.logger.Info("MCP server info",
		"name", initResult.ServerInfo.Name,
		"version", initResult.ServerInfo.Version,
		"protocol", initResult.ProtocolVersion,
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

func (c *MCPClient) IsAlive() bool {
	if c.stopped.Load() {
		return false
	}
	if !c.alive.Load() {
		return false
	}
	if c.cmd != nil {
		return c.cmd.ProcessState == nil || c.cmd.ProcessState.Exited() == false
	}
	return false
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
