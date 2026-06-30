package agents

import (
	"context"

	"codeactor/internal/llm"
	"codeactor/internal/memory"
	"codeactor/internal/messaging"
)

// AgentResult 封装 sub-agent 的完整执行结果
type AgentResult struct {
	Text   string                // 最终文本输出（给 Director 作为 tool_result）
	Memory []memory.ChatMessage  // sub-agent 的完整内部对话历史（IsSubAgent=true，GroupID/ParentID 待 Director 填入）
}

// Agent defines the interface for all agents in the system.
type Agent interface {
	Name() string
	Run(ctx context.Context, input string) (AgentResult, error)
}

// BaseAgent holds common dependencies for agents.
type BaseAgent struct {
	LLM       llm.Engine
	Publisher *messaging.MessagePublisher
}
