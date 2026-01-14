package agents

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"

	"codeactor/internal/assistant/tools"
	"codeactor/internal/globalctx"
	"codeactor/internal/memory"

	"github.com/tmc/langchaingo/llms"
)

//go:embed conductor.prompt.md
var conductorPrompt string

type ConductorAgent struct {
	BaseAgent
	RepoAgent   *RepoAgent
	CodingAgent *CodingAgent
	GlobalCtx   *globalctx.GlobalCtx
	Adapters    []*tools.Adapter
	maxSteps    int
}

func NewConductorAgent(globalCtx *globalctx.GlobalCtx, llm llms.LLM, repo *RepoAgent, coding *CodingAgent, maxSteps int) *ConductorAgent {
	delegateRepo := tools.NewAdapter("delegate_repo", "Delegate analysis task to Repo-Agent", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		return repo.Run(ctx, task)
	}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{"type": "string", "description": "The task description for Repo-Agent"},
		},
		"required": []string{"task"},
	})

	delegateCoding := tools.NewAdapter("delegate_coding", "Delegate coding task to Coding-Agent", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		if globalCtx.RepoSummary != "" {
			task = fmt.Sprintf("%s\n\n#Repository Context:\n%s", task, globalCtx.RepoSummary)
		}
		return coding.Run(ctx, task)
	}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{"type": "string", "description": "The task description for Coding-Agent"},
		},
		"required": []string{"task"},
	})

	adapters := []*tools.Adapter{
		tools.NewAdapter("finish", "Indicate that the current task is finished. The output of this tool call will be a description of why the task is finished, which could be because the task is completed or cannot be completed and must be terminated.", globalCtx.FlowOps.ExecuteFinish).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"reason": map[string]interface{}{"type": "string", "description": "A description of why the task is finished, e.g., task completed, cannot complete, or must terminate."},
			},
			"required": []string{"reason"},
		}),
	}

	var toolDefs []ToolDefinition
	if err := json.Unmarshal(ToolsJSON, &toolDefs); err != nil {
		slog.Error("Failed to unmarshal tools", "error", err)
	}

	for _, def := range toolDefs {
		var fn tools.ToolFunc
		switch def.Name {
		case "search_by_regex":
			fn = globalCtx.SearchOps.ExecuteGrepSearch
		case "list_dir":
			fn = globalCtx.FileOps.ExecuteListDir
		case "read_file":
			fn = globalCtx.FileOps.ExecuteReadFile
		case "print_dir_tree":
			fn = globalCtx.FileOps.ExecutePrintDirTree
		default:
			continue
		}

		adapter := tools.NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters)
		adapters = append(adapters, adapter)
	}

	return &ConductorAgent{
		BaseAgent:   BaseAgent{LLM: llm, Publisher: globalCtx.Publisher},
		RepoAgent:   repo,
		CodingAgent: coding,
		GlobalCtx:   globalCtx,
		Adapters:    append(adapters, delegateRepo, delegateCoding),
		maxSteps:    maxSteps,
	}
}

func (a *ConductorAgent) Name() string {
	return "Conductor"
}

func convertToolCalls(tcs []llms.ToolCall) []memory.ToolCallData {
	var res []memory.ToolCallData
	for _, tc := range tcs {
		res = append(res, memory.ToolCallData{
			ID:   tc.ID,
			Type: string(tc.Type),
			Function: memory.ToolCallFunction{
				Name:      tc.FunctionCall.Name,
				Arguments: json.RawMessage(tc.FunctionCall.Arguments),
			},
		})
	}
	return res
}

func (a *ConductorAgent) Run(ctx context.Context, input string, mem *memory.ConversationMemory) (string, error) {
	if mem != nil {
		mem.AddHumanMessage(input)
		if a.Publisher != nil {
			a.Publisher.Publish("memory_change", nil, a.Name())
		}
	}

	messages := []llms.MessageContent{
		{
			Role:  llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{llms.TextPart(a.GlobalCtx.FormatPrompt(conductorPrompt))},
		},
		{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart(input)},
		},
	}

	llmTools := make([]llms.Tool, len(a.Adapters))
	for i, ad := range a.Adapters {
		llmTools[i] = ad.ToLLMSTool()
	}

	for i := 0; i < a.maxSteps; i++ {
		slog.Debug("ConductorAgent calling LLM", "step", i, "messages", messages)
		if a.Publisher != nil {
			a.Publisher.Publish("status_update", fmt.Sprintf("ConductorAgent is thinking (step %d/%d)...", i+1, a.maxSteps), a.Name())
		}
		resp, err := a.LLM.GenerateContent(ctx, messages, llms.WithTools(llmTools))
		if err != nil {
			slog.Error("ConductorAgent LLM error", "error", err, "step", i)
			return "", err
		}

		msg := resp.Choices[0]
		slog.Debug("ConductorAgent LLM response", "step", i, "message", msg)

		if msg.Content != "" {
			if a.Publisher != nil {
				a.Publisher.Publish("ai_response", msg.Content, a.Name())
			}
		}

		if mem != nil {
			mem.AddAssistantMessage(msg.Content, convertToolCalls(msg.ToolCalls))
			if a.Publisher != nil {
				a.Publisher.Publish("memory_change", nil, a.Name())
			}
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
			var err error
			found := false

			if a.Publisher != nil {
				a.Publisher.Publish("tool_call_start", map[string]interface{}{
					"tool_name": tc.FunctionCall.Name,
					"arguments": tc.FunctionCall.Arguments,
					"tool_call_id": tc.ID,
				}, a.Name())
			}
			for _, t := range a.Adapters {
				if t.Name() == tc.FunctionCall.Name {
					found = true
					toolResult, err = t.Call(ctx, tc.FunctionCall.Arguments)
					if err != nil {
						toolResult = fmt.Sprintf("Error: %v", err)
					} else if t.Name() == "delegate_repo" {
						// toolResult is a JSON string (e.g. "\"summary...\""), so we need to unmarshal it
						// to get the actual text content
						var summary string
						if err := json.Unmarshal([]byte(toolResult), &summary); err == nil {
							a.GlobalCtx.RepoSummary = summary
						} else {
							a.GlobalCtx.RepoSummary = toolResult
						}
					}
					break
				}
			}
			if !found {
				toolResult = fmt.Sprintf("Tool %s not found", tc.FunctionCall.Name)
			}

			if a.Publisher != nil {
				a.Publisher.Publish("tool_call_result", map[string]interface{}{
					"tool_name": tc.FunctionCall.Name,
					"result":    toolResult,
				}, a.Name())
			}

			if mem != nil {
				mem.AddToolMessage(toolResult, tc.ID)
				if a.Publisher != nil {
					a.Publisher.Publish("memory_change", nil, a.Name())
				}
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
			if tc.FunctionCall.Name == "finish" {
				return "Task completed successfully", nil
			}

		}
	}

	return "", fmt.Errorf("ConductorAgent exceeded max steps")
}
