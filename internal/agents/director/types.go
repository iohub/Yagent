// Package director provides the orchestration engine for the multi-agent system.
// It is responsible for task decomposition, agent routing, memory management,
// error recovery, and observability metrics.
package director

import (
	"context"
	"time"

	"codeactor/internal/llm"
)

// AgentRunner defines the interface for any sub-agent that Director can delegate to.
// This breaks the circular dependency between director and agents packages.
type AgentRunner interface {
	// Name returns the agent's display name.
	Name() string
	// Run executes a task and returns the result text.
	Run(ctx context.Context, task string) (AgentRunnerResult, error)
}

// AgentRunnerResult encapsulates a sub-agent execution result.
type AgentRunnerResult struct {
	Text string
}

// LLMClient abstracts the LLM interaction for the director sub-components.
type LLMClient interface {
	GenerateContent(ctx context.Context, messages []llm.Message, tools []llm.ToolDef, opts *llm.CallOptions) (*llm.Response, error)
	Model() string
}

// --- Event types for observability ---

// ToolCallEvent represents a tool call start event.
type ToolCallEvent struct {
	ToolName   string `json:"tool_name"`
	Arguments  string `json:"arguments"`
	ToolCallID string `json:"tool_call_id"`
}

// ToolResultEvent represents a tool call result event.
type ToolResultEvent struct {
	ToolName   string `json:"tool_name"`
	Result     string `json:"result"`
	ToolCallID string `json:"tool_call_id"`
}

// LLMCallEvent represents LLM call metrics.
type LLMCallEvent struct {
	Model           string                 `json:"model"`
	DurationSeconds float64                `json:"duration_seconds"`
	Error           string                 `json:"error,omitempty"`
	Usage           map[string]interface{} `json:"usage,omitempty"`
}

// --- Custom Agent types ---

// CustomAgent stores a dynamically designed agent created by Meta-Agent.
type CustomAgent struct {
	Name         string   // snake_case identifier used for the delegate tool name
	DisplayName  string   // human-readable agent name
	SystemPrompt string   // the full system prompt designed by Meta-Agent
	ToolsUsed    []string // tool names this agent was designed to use
	Description  string   // short description for the LLM
}

// MetaAgentResult parses the JSON output from Meta-Agent.
type MetaAgentResult struct {
	Thinking     string   `json:"thinking"`
	AgentName    string   `json:"agent_name"`
	AgentDesign  string   `json:"agent_design"`
	ToolsUsed    []string `json:"tools_used"`
	TaskForAgent string   `json:"task_for_agent"`
}

// --- Project Context types ---

// ProjectContextFile represents a successfully loaded project context file.
type ProjectContextFile struct {
	FileName string `json:"file_name"`
	Content  string `json:"content"`
}

// ProjectContextLoadResult represents the result of loading project context files.
type ProjectContextLoadResult struct {
	LoadedFiles []ProjectContextFile `json:"loaded_files"`
	Content     string               `json:"content"`
}

// --- Memory types ---

// SubAgentMemory holds the result and history of a sub-agent execution for memory injection.
type SubAgentMemory struct {
	Text   string
	Memory []ChatMessage
}

// ChatMessage is a simplified version for memory injection.
type ChatMessage struct {
	Type       string
	Content    string
	GroupID    string
	ParentID   string
	IsSubAgent bool
}

// --- Metrics types ---

// MetricsSnapshot is a point-in-time snapshot of director metrics.
type MetricsSnapshot struct {
	TaskCount     int                `json:"task_count"`
	ToolCallCount int                `json:"tool_call_count"`
	ErrorCount    map[string]int     `json:"error_count"`
	DurationMs    map[string]float64 `json:"duration_ms"`
	Timestamp     time.Time          `json:"timestamp"`
}
