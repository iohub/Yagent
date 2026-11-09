// Package director provides the orchestration engine for the multi-agent system.
// It is responsible for task decomposition, agent routing, memory management,
// error recovery, and observability metrics.
package director

import (
	"context"
	"time"

	"yagent/internal/llm"
)

// LLMClient abstracts the LLM interaction for the director sub-components.
type LLMClient interface {
	GenerateContent(ctx context.Context, messages []llm.Message, tools []llm.ToolDef, opts *llm.CallOptions) (*llm.Response, error)
	Model() string
}

// --- Custom Agent types ---

// CustomAgent 是一个已注册的生成式 Agent 的**运行期形状**。
// 它由 plasma.LinkPlan.Agent（CustomAgentView）投影而来：生成与校验都在 plasma 侧，
// 这里只保留执行需要的东西。MetaAgentResult（Meta-Agent 输出的 5 字段 JSON）已在
// P9a 删除 —— 那份 JSON 现在只是 Python 侧的最小 spec，由 plasma.Elaborate 归一化，
// Go 侧不再解析 LLM 裸输出。
type CustomAgent struct {
	Name         string   // snake_case identifier used for the delegate tool name
	DisplayName  string   // human-readable agent name
	SystemPrompt string   // the full system prompt designed by Meta-Agent
	ToolsUsed    []string // tool names this agent was designed to use
	Description  string   // short description for the LLM
}

// --- Metrics types ---

// MetricsSnapshot is a point-in-time snapshot of director metrics.
type MetricsSnapshot struct {
	DurationMs map[string]float64 `json:"duration_ms"`
	Timestamp  time.Time          `json:"timestamp"`
}
