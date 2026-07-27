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
)

// ============================================================================
// MCPClient Configuration
// ============================================================================

// MCPClientConfig holds the configuration for creating an MCPClient.
type MCPClientConfig struct {
	// BinaryPath is the path to the codeseek MCP server binary.
	BinaryPath string

	// Args are the command-line arguments passed to the MCP server.
	Args []string

	// WorkingDir is the working directory for the MCP server process.
	// If empty, the current directory is used.
	WorkingDir string

	// RequestTimeout is the maximum duration to wait for a response.
	// Defaults to 30 seconds if zero.
	RequestTimeout time.Duration
}

// ============================================================================
// MCPClient Implementation
// ============================================================================

// MCPClient manages communication with a codeseek MCP server via stdio JSON-RPC 2.0.
// It handles subprocess lifecycle, request/response matching, and concurrent access.
type MCPClient struct {
	cfg     MCPClientConfig
	logger  *slog.Logger
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	out     *bufio.Reader

	// requestMap maps request IDs to channels for response matching.
	mu        sync.Mutex
	requests  map[int]chan *JSONRPCResponse
	nextID    atomic.Int64

	alive        atomic.Bool
	initialized  atomic.Bool
	stopped      atomic.Bool

	// 异步初始化支持
	readyCh    chan struct{}      // 关闭信号：初始化完成（成功或失败）
	initErr    error              // 初始化失败原因（在 readyCh 关闭前设置，之后只读）
	closeReady sync.Once          // 保证 readyCh 只关闭一次
	cancelInit context.CancelFunc // 取消初始化 goroutine 的 context
}

// NewMCPClient creates a new MCP client with the given configuration.
// The client is not started until Start() is called.
//
// Parameters:
//   - cfg: Configuration for the MCP client including binary path and timeout settings.
//
// Returns:
//   - *MCPClient: A new MCPClient instance ready to be started.
func NewMCPClient(cfg MCPClientConfig) *MCPClient {
	timeout := cfg.RequestTimeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}

	return &MCPClient{
		cfg:     cfg,
		logger:  slog.Default().With("component", "mcp"),
		requests: make(map[int]chan *JSONRPCResponse),
		readyCh: make(chan struct{}),
	}
}

// Start launches the MCP server subprocess and begins asynchronous initialization.
// The initialization sequence runs in the background:
//
//	1. Launch the subprocess with stdio pipes connected.
//	2. Send an "initialize" JSON-RPC request with client capabilities and info.
//	3. Receive the server's initialization result.
//	4. Send a "notifications/initialized" notification to complete setup.
//
// Start() returns immediately without waiting for initialization to complete.
// Use WaitForReady() or IsReady() to check initialization status.
//
// Parameters:
//   - ctx: Context for controlling the subprocess lifecycle during startup.
//
// Returns:
//   - error: Non-nil if the subprocess cannot be started. Initialization errors
//     are returned via WaitForReady() or InitError().
func (c *MCPClient) Start(ctx context.Context) error {
	// Build the command for the MCP server binary.
	cmdArgs := append([]string{}, c.cfg.Args...)
	cmd := exec.CommandContext(ctx, c.cfg.BinaryPath, cmdArgs...)

	if c.cfg.WorkingDir != "" {
		cmd.Dir = c.cfg.WorkingDir
	}

	// Capture stderr to help with debugging server issues.
	cmd.Stderr = &captureWriter{logger: c.logger}

	// Create pipes for JSON-RPC communication.
	stdinPipe, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdin pipe: %w", err)
	}

	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe: %w", err)
	}

	// Start the subprocess.
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start MCP server: %w", err)
	}

	c.cmd = cmd
	c.stdin = stdinPipe
	c.out = bufio.NewReaderSize(stdoutPipe, 1024*1024) // 1MB buffer

	// Mark the client as alive.
	c.alive.Store(true)

	// Launch the response reader goroutine.
	go c.readLoop()

	// ── 异步初始化：不阻塞等待 codeseek init 完成 ──
	// 使用独立的 background context，避免调用者的 context 生命周期过短
	initCtx, cancel := context.WithCancel(context.Background())
	c.cancelInit = cancel

	go func() {
		defer cancel()
		c.logger.Info("MCP client initialization started in background")

		if err := c.performInitialize(initCtx); err != nil {
			c.markReady(fmt.Errorf("initialization failed: %w", err))
		} else {
			c.markReady(nil)
		}
	}()

	return nil
}

// performInitialize executes the MCP initialization handshake sequence.
func (c *MCPClient) performInitialize(ctx context.Context) error {
	// Send the initialize request.
	initReq := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      int(c.nextID.Add(1)),
		Method:  "initialize",
		Params: mustMarshal(InitializeParams{
			ProtocolVersion: "2024-11-05",
			Capabilities:    ClientCapabilities{},
			ClientInfo: ClientInfo{
				Name:    "codeactor",
				Version: "0.1.0",
			},
		}),
	}

	result, err := c.doRequest(ctx, initReq)
	if err != nil {
		return fmt.Errorf("initialize request failed: %w", err)
	}

	// Parse the server's initialization result.
	var initResult InitializeResult
	if err := json.Unmarshal(result.Result, &initResult); err != nil {
		return fmt.Errorf("failed to unmarshal initialize result: %w", err)
	}

	c.logger.Info("MCP server info",
		"name", initResult.ServerInfo.Name,
		"version", initResult.ServerInfo.Version,
		"protocol", initResult.ProtocolVersion,
	)

	// Send the initialized notification (no response expected).
	notif := JSONRPCNotification{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	if err := c.sendJSON(notif); err != nil {
		return fmt.Errorf("failed to send initialized notification: %w", err)
	}

	return nil
}

// markReady 标记初始化完成（成功或失败）。
// 使用 sync.Once 保证 readyCh 只关闭一次，避免 panic。
// 并发安全：可从 init goroutine 和 Shutdown() 安全调用。
// err == nil 表示成功，err != nil 表示失败。
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

// CallTool invokes an MCP tool by name with the given arguments.
//
// Parameters:
//   - ctx: Context for controlling the call timeout and cancellation.
//   - toolName: The name of the tool to call (e.g., "code_search").
//   - arguments: Optional arguments to pass to the tool as a map.
//
// Returns:
//   - *ToolCallResult: The tool's response content, or nil on error.
//   - error: Non-nil if the tool call fails or times out.
func (c *MCPClient) CallTool(ctx context.Context, toolName string, arguments map[string]interface{}) (*ToolCallResult, error) {
	if !c.IsAlive() {
		return nil, errors.New("MCP client is not alive")
	}

	// 等待初始化完成（如果尚未就绪）
	if !c.initialized.Load() {
		select {
		case <-c.readyCh:
			if c.initErr != nil {
				return nil, fmt.Errorf("MCP client initialization failed: %w", c.initErr)
			}
			// initErr == nil 意味着 initialized == true
		case <-ctx.Done():
			return nil, fmt.Errorf("MCP client is still initializing: %w", ctx.Err())
		}
	}

	// 以下为原有逻辑，保持不变
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

	// Parse the tool call result.
	var toolResult ToolCallResult
	if err := json.Unmarshal(result.Result, &toolResult); err != nil {
		return nil, fmt.Errorf("failed to unmarshal tool result: %w", err)
	}

	return &toolResult, nil
}

// IsAlive reports whether the MCP client is currently running and responsive.
func (c *MCPClient) IsAlive() bool {
	if c.stopped.Load() {
		return false
	}
	if !c.alive.Load() {
		return false
	}
	// Check if the subprocess is still running.
	if c.cmd != nil {
		return c.cmd.ProcessState == nil || c.cmd.ProcessState.Exited() == false
	}
	return false
}

// Shutdown gracefully terminates the MCP client and its subprocess.
// It sends a shutdown request (best effort), waits for the process to exit,
// and cleans up all resources.
func (c *MCPClient) Shutdown() {
	if c.stopped.Swap(true) {
		return // already shut down
	}

	// 取消正在进行的初始化
	if c.cancelInit != nil {
		c.cancelInit()
	}

	// 解除所有等待者的阻塞（如果尚未就绪）
	c.markReady(errors.New("MCP client shut down before initialization completed"))

	// 原有的清理逻辑
	c.alive.Store(false)

	// Send a best-effort shutdown request.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	shutdownReq := JSONRPCRequest{
		JSONRPC: "2.0",
		ID:      int(c.nextID.Add(1)),
		Method:  "shutdown",
	}
	_, _ = c.doRequest(ctx, shutdownReq) // ignore errors during shutdown

	// Close the stdin pipe to signal EOF to the subprocess.
	if c.stdin != nil {
		c.stdin.Close()
	}

	// Wait for the subprocess to exit.
	if c.cmd != nil && c.cmd.ProcessState == nil {
		c.cmd.Wait()
	}

	c.logger.Info("MCP client shut down")
}

// ForceShutdown forcefully terminates the MCP client and its subprocess
// without waiting for graceful shutdown. It kills the subprocess immediately.
// Use this when force quit is enabled and you don't want to wait for codeseek.
func (c *MCPClient) ForceShutdown() {
	if c.stopped.Swap(true) {
		return // already shut down
	}

	// 取消正在进行的初始化
	if c.cancelInit != nil {
		c.cancelInit()
	}

	// 解除所有等待者的阻塞
	c.markReady(errors.New("MCP client force shut down"))

	c.alive.Store(false)

	// Close the stdin pipe.
	if c.stdin != nil {
		c.stdin.Close()
	}

	// Force kill the subprocess immediately.
	if c.cmd != nil && c.cmd.Process != nil {
		if err := c.cmd.Process.Kill(); err != nil {
			c.logger.Warn("Failed to kill MCP subprocess", "error", err)
		} else {
			c.logger.Info("MCP subprocess forcefully killed")
		}
		// Wait for the process to be reaped (don't wait long, just clean up)
		_ = c.cmd.Wait()
	}

	c.logger.Info("MCP client force shut down")
}

// WaitForReady 阻塞等待 MCP 客户端初始化完成。
// 返回 nil 表示初始化成功，非 nil 表示初始化失败或 context 取消。
// 可安全并发调用；在初始化完成后调用会立即返回。
func (c *MCPClient) WaitForReady(ctx context.Context) error {
	select {
	case <-c.readyCh:
		return c.initErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

// IsReady 返回 MCP 客户端是否已完成初始化且可用。
// 非阻塞检查，适用于状态探针。
func (c *MCPClient) IsReady() bool {
	return c.initialized.Load()
}

// InitError 返回初始化失败的错误信息。
// 如果初始化成功或尚未完成，返回 nil。
func (c *MCPClient) InitError() error {
	select {
	case <-c.readyCh:
		return c.initErr
	default:
		return nil
	}
}

// ============================================================================
// Internal Methods
// ============================================================================

// doRequest sends a JSON-RPC request and waits for the corresponding response.
// It handles the registration of response channels and timeout management.
func (c *MCPClient) doRequest(ctx context.Context, req JSONRPCRequest) (*JSONRPCResponse, error) {
	// Create a response channel for this request.
	ch := make(chan *JSONRPCResponse, 1)

	c.mu.Lock()
	c.requests[req.ID] = ch
	c.mu.Unlock()

	// Clean up the channel on return.
	defer func() {
		c.mu.Lock()
		delete(c.requests, req.ID)
		c.mu.Unlock()
	}()

	// Send the request to the subprocess.
	if err := c.sendJSON(req); err != nil {
		return nil, fmt.Errorf("failed to send request: %w", err)
	}

	// Wait for the response or context cancellation.
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

// readLoop continuously reads from the subprocess stdout and dispatches
// responses to the appropriate request channels or handles notifications.
func (c *MCPClient) readLoop() {
	defer func() {
		c.alive.Store(false)
		c.mu.Lock()
		// Notify all pending requests that the connection is closed.
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

		// Parse the JSON message.
		var msg map[string]interface{}
		if err := json.Unmarshal(line, &msg); err != nil {
			c.logger.Warn("MCP invalid JSON", "data", string(line))
			continue
		}

		// Determine message type by checking for "method" (notification) or "id" (response).
		if method, ok := msg["method"].(string); ok {
			// Handle notification messages (no ID).
			c.handleNotification(method, msg)
			continue
		}

		// Parse as a response.
		var resp JSONRPCResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			c.logger.Warn("MCP invalid response JSON", "data", string(line))
			continue
		}

		// Dispatch to the waiting request channel.
		c.mu.Lock()
		ch, found := c.requests[resp.ID]
		c.mu.Unlock()

		if found {
			select {
			case ch <- &resp:
			default:
				// Channel full or already consumed; this should not happen.
			}
		} else {
			c.logger.Warn("MCP response for unknown request ID", "id", resp.ID)
		}
	}
}

// handleNotification processes incoming MCP server notifications.
func (c *MCPClient) handleNotification(method string, msg map[string]interface{}) {
	c.logger.Debug("MCP notification received", "method", method)

	switch method {
	case "notifications/tools/list_changed":
		c.logger.Info("MCP tools list changed notification received")
	default:
		c.logger.Debug("MCP unknown notification", "method", method)
	}
}

// sendJSON marshals and writes a JSON value to the subprocess stdin.
func (c *MCPClient) sendJSON(v interface{}) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("failed to marshal JSON: %w", err)
	}

	_, err = c.stdin.Write(append(data, '\n'))
	if err != nil {
		return fmt.Errorf("failed to write to stdin: %w", err)
	}
	return nil
}

// ============================================================================
// Helper Functions
// ============================================================================

// mustMarshal is a helper that marshals an interface to json.RawMessage,
// panicking on error (use only when errors are truly unexpected).
func mustMarshal(v interface{}) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("mcp: failed to marshal: %v", err))
	}
	return data
}

// ============================================================================
// CaptureWriter - Captures subprocess stderr for logging
// ============================================================================

// captureWriter writes subprocess stderr lines to the MCP client logger.
type captureWriter struct {
	logger *slog.Logger
}

func (w *captureWriter) Write(p []byte) (int, error) {
	// Log each line from the subprocess stderr.
	for _, line := range bytesLines(p) {
		w.logger.Warn("MCP server stderr", "line", string(line))
	}
	return len(p), nil
}

// bytesLines splits a byte slice into lines (splitting on \n).
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
