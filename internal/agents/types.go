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

	// Shared Memory System (optional, nil if not configured)
	MemoryInjector *memory.SharedMemoryInjector
	MemoryUpdater  *memory.SharedDimensionUpdater
}

// InjectSharedMemory 将共享记忆注入到system prompt中
// 如果MemoryInjector未配置，返回原始prompt
func (a *BaseAgent) InjectSharedMemory(systemPrompt, userID, projectID string) string {
	if a.MemoryInjector == nil {
		return systemPrompt
	}
	ctx := a.MemoryInjector.InjectContext(userID, projectID)
	if ctx == "" {
		return systemPrompt
	}
	return systemPrompt + "\n" + ctx
}
