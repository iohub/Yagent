package llm

import (
	"context"
)

// Role represents the role of a message in a conversation.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Message represents a single message in a conversation.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolName   string     `json:"name,omitempty"`
	Reasoning  string     `json:"reasoning_content,omitempty"` // thinking/reasoning from models that support it
}

// ToolDef defines a tool available to the LLM.
type ToolDef struct {
	Type     string      `json:"type"` // "function"
	Function FunctionDef `json:"function"`
}

// FunctionDef defines a function tool's signature.
//
// ⚠️ IMPORTANT: The current implementation relies on encoding/json's deterministic
// sorting of map keys (alphabetical order). If migrating to sonic, jsoniter, or
// another JSON library in the future, ensure SortMapKeys is enabled to maintain
// deterministic key ordering and prevent prompt cache fragmentation.
type FunctionDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

// ToolCall represents a tool call from the LLM.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // "function"
	Function FunctionCall `json:"function"`
}

// FunctionCall represents the function name and arguments in a tool call.
type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// TokenUsage contains token usage information returned by the LLM API.
type TokenUsage struct {
	PromptTokens             int64 `json:"prompt_tokens"`
	CompletionTokens         int64 `json:"completion_tokens"`
	TotalTokens              int64 `json:"total_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens,omitempty"`
}

// Response represents the LLM's response to a GenerateContent call.
type Response struct {
	Choices []Choice    `json:"choices"`
	Usage   *TokenUsage `json:"usage,omitempty"`
}

// Choice represents a single choice in the LLM response.
type Choice struct {
	Content   string     `json:"content,omitempty"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
	Reasoning string     `json:"reasoning_content,omitempty"` // thinking/reasoning content
}

// CallOptions holds optional parameters for LLM calls.
type CallOptions struct {
	MaxTokens     int
	Temperature   float64
	StreamHandler StreamHandler
}

// StreamHandler is called for each chunk during streaming.
type StreamHandler func(ctx context.Context, chunk []byte) error

// Engine is the core LLM abstraction.
type Engine interface {
	// GenerateContent sends messages and tools to the LLM and returns the response.
	GenerateContent(ctx context.Context, messages []Message, tools []ToolDef, opts *CallOptions) (*Response, error)

	// Model returns the model name this engine is configured to use.
	Model() string
}
