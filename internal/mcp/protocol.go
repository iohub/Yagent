package mcp

import "encoding/json"

// ============================================================================
// JSON-RPC 2.0 Protocol Types
// ============================================================================

// JSONRPCRequest represents a JSON-RPC 2.0 request message.
type JSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// JSONRPCResponse represents a JSON-RPC 2.0 response message.
type JSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

// JSONRPCError represents a JSON-RPC 2.0 error response.
type JSONRPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// JSONRPCNotification represents a JSON-RPC 2.0 notification message (no ID).
type JSONRPCNotification struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// ============================================================================
// MCP Protocol Types - Initialize
// ============================================================================

// InitializeParams represents the parameters sent during MCP initialization.
type InitializeParams struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ClientCapabilities `json:"capabilities"`
	ClientInfo      ClientInfo         `json:"clientInfo"`
}

// ClientCapabilities represents the capabilities a client can support.
type ClientCapabilities struct{}

// ClientInfo contains identifying information for the MCP client.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// InitializeResult represents the result of an MCP initialize request.
type InitializeResult struct {
	ProtocolVersion string             `json:"protocolVersion"`
	Capabilities    ServerCapabilities `json:"capabilities"`
	ServerInfo      ServerInfo         `json:"serverInfo"`
	// Instructions 是服务端自带的"这个引擎怎么用"说明。codeseek 真的会发，
	// 以前被整个丢掉；收下后进状态账与 /mcp 面板，而不是让模型自己猜。
	Instructions string `json:"instructions,omitempty"`
}

// ServerCapabilities represents the capabilities advertised by the MCP server.
type ServerCapabilities struct {
	Tools *ToolsCapability `json:"tools,omitempty"`
}

// ToolsCapability indicates support for MCP tool operations.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ServerInfo contains identifying information for the MCP server.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ============================================================================
// MCP Protocol Types - Tool Call
// ============================================================================

// ToolCallParams represents parameters for calling an MCP tool.
type ToolCallParams struct {
	Name      string                 `json:"name"`
	Arguments map[string]interface{} `json:"arguments,omitempty"`
}

// ToolCallResult represents the result returned by an MCP tool invocation.
type ToolCallResult struct {
	Content []ToolContent `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// ToolContent represents a single content block returned by a tool.
type ToolContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ============================================================================
// MCP Protocol Types - Tool Discovery
// ============================================================================

// ToolAnnotations 是服务端对工具副作用的自我声明。
// 四个 hint 都用 *bool：`false` 与"没声明"在能力判定里走向不同分支 ——
// 裸 bool 分不清这两者，而"没声明按写处理"是安全默认。
type ToolAnnotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    *bool  `json:"readOnlyHint,omitempty"`
	DestructiveHint *bool  `json:"destructiveHint,omitempty"`
	IdempotentHint  *bool  `json:"idempotentHint,omitempty"`
	OpenWorldHint   *bool  `json:"openWorldHint,omitempty"`
}

// DiscoveredTool 是 tools/list 返回的一条工具声明。
// 它是线上形状：不要往里塞 yagent 侧字段（server 归属由 Manager 用 ServerTool 包一层）。
type DiscoveredTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"inputSchema"`
	Annotations *ToolAnnotations       `json:"annotations,omitempty"`
}

// ListToolsParams 是 tools/list 的请求参数。cursor 为空表示第一页。
type ListToolsParams struct {
	Cursor string `json:"cursor,omitempty"`
}

// ListToolsResult 是 tools/list 的一页结果。nextCursor 非空表示还有下一页。
type ListToolsResult struct {
	Tools      []DiscoveredTool `json:"tools"`
	NextCursor string           `json:"nextCursor,omitempty"`
}
