package agents

import (
	"context"

	"codeactor/internal/llm"
	"codeactor/pkg/messaging"
)

// Agent defines the interface for all agents in the system.
type Agent interface {
	Name() string
	Run(ctx context.Context, input string) (string, error)
}

// BaseAgent holds common dependencies for agents.
type BaseAgent struct {
	LLM       llm.Engine
	Publisher *messaging.MessagePublisher
}
