package agents

import (
	"context"
	"fmt"
	"log/slog"

	"codeactor/internal/llm"
	"codeactor/internal/tools"
	"codeactor/pkg/messaging"
)

// ExecutorConfig holds the configuration for running an LLM-tool agent loop.
type ExecutorConfig struct {
	SystemPrompt string
	UserInput    string
	Adapters     []*tools.Adapter
	LLM          llm.Engine
	MaxSteps     int
	Publisher    *messaging.MessagePublisher
	AgentName    string
	StopOnFinish bool // if true, return immediately when agent_exit tool is called
	// SystemAsHuman places the system prompt in a Human role message instead of System.
	// Used by RepoAgent which prefers this pattern.
	SystemAsHuman bool
	// OnToolResult is an optional callback invoked after each tool executes.
	// Used by Conductor for special handling (e.g. delegate_repo → RepoSummary).
	OnToolResult func(toolName string, result string)
}

// RunAgentLoop runs the standard LLM-tool interaction loop.
// It returns the final text response from the LLM.
func RunAgentLoop(ctx context.Context, cfg ExecutorConfig) (string, error) {
	systemRole := llm.RoleSystem
	if cfg.SystemAsHuman {
		systemRole = llm.RoleUser
	}

	messages := []llm.Message{
		{
			Role:    systemRole,
			Content: cfg.SystemPrompt,
		},
		{
			Role:    llm.RoleUser,
			Content: cfg.UserInput,
		},
	}

	toolDefs := make([]llm.ToolDef, len(cfg.Adapters))
	for i, ad := range cfg.Adapters {
		toolDefs[i] = ad.ToToolDef()
	}

	opts := &llm.CallOptions{}

	for i := 0; i < cfg.MaxSteps; i++ {
		slog.Debug("AgentExecutor calling LLM", "agent", cfg.AgentName, "step", i)
		resp, err := cfg.LLM.GenerateContent(ctx, messages, toolDefs, opts)
		if err != nil {
			slog.Error("AgentExecutor LLM error", "agent", cfg.AgentName, "error", err, "step", i)
			return "", err
		}

		choice := resp.Choices[0]
		if choice.Content != "" && cfg.Publisher != nil {
			cfg.Publisher.Publish("ai_response", choice.Content, cfg.AgentName)
		}

		// Build assistant message
		assistantMsg := llm.Message{
			Role:      llm.RoleAssistant,
			Content:   choice.Content,
			ToolCalls: choice.ToolCalls,
		}
		messages = append(messages, assistantMsg)

		if len(choice.ToolCalls) == 0 {
			return choice.Content, nil
		}

		for _, tc := range choice.ToolCalls {
			var toolResult string
			var callErr error
			found := false

			if cfg.Publisher != nil {
				cfg.Publisher.Publish("tool_call_start", map[string]interface{}{
					"tool_name":    tc.Function.Name,
					"arguments":    tc.Function.Arguments,
					"tool_call_id": tc.ID,
				}, cfg.AgentName)
			}

			for _, t := range cfg.Adapters {
				if t.Name() == tc.Function.Name {
					found = true
					toolResult, callErr = t.Call(ctx, tc.Function.Arguments)
					if callErr != nil {
						toolResult = fmt.Sprintf("Error: %v", callErr)
					}
					break
				}
			}
			if !found {
				toolResult = fmt.Sprintf("Tool %s not found", tc.Function.Name)
			}

			if cfg.OnToolResult != nil {
				cfg.OnToolResult(tc.Function.Name, toolResult)
			}

			if cfg.Publisher != nil {
				cfg.Publisher.Publish("tool_call_result", map[string]interface{}{
					"tool_name":    tc.Function.Name,
					"result":       toolResult,
					"tool_call_id": tc.ID,
				}, cfg.AgentName)
			}

			messages = append(messages, llm.Message{
				Role:       llm.RoleTool,
				Content:    toolResult,
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
			})

			if cfg.StopOnFinish && tc.Function.Name == "agent_exit" {
				return toolResult, nil
			}
		}
	}

	return "", fmt.Errorf("%s exceeded max steps (%d)", cfg.AgentName, cfg.MaxSteps)
}
