package agents

import (
	"context"
	"fmt"
	"log/slog"

	"codeactor/internal/tools"
	"codeactor/pkg/messaging"

	"github.com/tmc/langchaingo/llms"
)

// ExecutorConfig holds the configuration for running an LLM-tool agent loop.
type ExecutorConfig struct {
	SystemPrompt string
	UserInput    string
	Adapters     []*tools.Adapter
	LLM          llms.LLM
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
	systemRole := llms.ChatMessageTypeSystem
	if cfg.SystemAsHuman {
		systemRole = llms.ChatMessageTypeHuman
	}

	messages := []llms.MessageContent{
		{
			Role:  systemRole,
			Parts: []llms.ContentPart{llms.TextPart(cfg.SystemPrompt)},
		},
		{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart(cfg.UserInput)},
		},
	}

	llmTools := make([]llms.Tool, len(cfg.Adapters))
	for i, ad := range cfg.Adapters {
		llmTools[i] = ad.ToLLMSTool()
	}

	for i := 0; i < cfg.MaxSteps; i++ {
		slog.Debug("AgentExecutor calling LLM", "agent", cfg.AgentName, "step", i)
		resp, err := cfg.LLM.GenerateContent(ctx, messages, llms.WithTools(llmTools))
		if err != nil {
			slog.Error("AgentExecutor LLM error", "agent", cfg.AgentName, "error", err, "step", i)
			return "", err
		}

		msg := resp.Choices[0]
		if msg.Content != "" && cfg.Publisher != nil {
			cfg.Publisher.Publish("ai_response", msg.Content, cfg.AgentName)
		}

		parts := []llms.ContentPart{llms.TextPart(msg.Content)}
		for _, tc := range msg.ToolCalls {
			parts = append(parts, tc)
		}

		messages = append(messages, llms.MessageContent{
			Role:  llms.ChatMessageTypeAI,
			Parts: parts,
		})

		if len(msg.ToolCalls) == 0 {
			return msg.Content, nil
		}

		for _, tc := range msg.ToolCalls {
			var toolResult string
			var callErr error
			found := false

			if cfg.Publisher != nil {
				cfg.Publisher.Publish("tool_call_start", map[string]interface{}{
					"tool_name":    tc.FunctionCall.Name,
					"arguments":    tc.FunctionCall.Arguments,
					"tool_call_id": tc.ID,
				}, cfg.AgentName)
			}

			for _, t := range cfg.Adapters {
				if t.Name() == tc.FunctionCall.Name {
					found = true
					toolResult, callErr = t.Call(ctx, tc.FunctionCall.Arguments)
					if callErr != nil {
						toolResult = fmt.Sprintf("Error: %v", callErr)
					}
					break
				}
			}
			if !found {
				toolResult = fmt.Sprintf("Tool %s not found", tc.FunctionCall.Name)
			}

			if cfg.OnToolResult != nil {
				cfg.OnToolResult(tc.FunctionCall.Name, toolResult)
			}

			if cfg.Publisher != nil {
				cfg.Publisher.Publish("tool_call_result", map[string]interface{}{
					"tool_name":    tc.FunctionCall.Name,
					"result":       toolResult,
					"tool_call_id": tc.ID,
				}, cfg.AgentName)
			}

			messages = append(messages, llms.MessageContent{
				Role: llms.ChatMessageTypeTool,
				Parts: []llms.ContentPart{
					llms.ToolCallResponse{
						ToolCallID: tc.ID,
						Name:       tc.FunctionCall.Name,
						Content:    toolResult,
					},
				},
			})

			if cfg.StopOnFinish && tc.FunctionCall.Name == "agent_exit" {
				return toolResult, nil
			}
		}
	}

	return "", fmt.Errorf("%s exceeded max steps (%d)", cfg.AgentName, cfg.MaxSteps)
}
