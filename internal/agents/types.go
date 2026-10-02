package agents

import (
	"context"

	"yagent/internal/artifact"
	"yagent/internal/llm"
	"yagent/internal/memory"
)

// AgentResult 封装 sub-agent 的完整执行结果
type AgentResult struct {
	Text        string               // 最终文本输出（给 Director 作为 tool_result）；下一批次（原子切换）删除，由 Summary 收敛
	Summary     string               // 分级摘要（<500 token，给 Director 上下文；未截断/未落盘时等于全文）
	ArtifactRef *artifact.Ref        // 完整结果落盘引用（按 ID 回查）；nil 表示未截断/未落盘
	Memory      []memory.ChatMessage // sub-agent 的完整内部对话历史（IsSubAgent=true，GroupID/ParentID 待 Director 填入）
}

// Agent defines the interface for all agents in the system.
type Agent interface {
	Name() string
	Run(ctx context.Context, input string) (AgentResult, error)
}

// BaseAgent holds common dependencies for agents.
type BaseAgent struct {
	LLM       llm.Engine
	Publisher EventBus
}
