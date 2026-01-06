package agents

import (
	"context"

	"github.com/tmc/langchaingo/llms"
	"github.com/tmc/langchaingo/tools"
)

// Agent defines the interface for all agents in the system.
type Agent interface {
	Name() string
	Run(ctx context.Context, input string) (string, error)
}

// BaseAgent holds common dependencies for agents.
type BaseAgent struct {
	LLM   llms.LLM
	Tools []tools.Tool
}
