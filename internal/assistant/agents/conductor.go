package agents

import (
	"context"
	"fmt"
	"log/slog"

	"codeactor/internal/assistant/tools"
	"codeactor/pkg/messaging"

	"github.com/tmc/langchaingo/llms"
)

type ConductorAgent struct {
	BaseAgent
	RepoAgent   *RepoAgent
	CodingAgent *CodingAgent
	Adapters    []*tools.Adapter
	maxSteps    int
}

func NewConductorAgent(llm llms.LLM, publisher *messaging.MessagePublisher, repo *RepoAgent, coding *CodingAgent, maxSteps int) *ConductorAgent {
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
		return coding.Run(ctx, task)
	}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{"type": "string", "description": "The task description for Coding-Agent"},
		},
		"required": []string{"task"},
	})

	fileOps := tools.NewFileOperationsTool(repo.projectDir)
	sysOps := tools.NewSystemOperationsTool(repo.projectDir)
	searchOps := tools.NewSearchOperationsTool(repo.projectDir)

	adapters := []*tools.Adapter{
		tools.NewAdapter("read_file", "Read file content", fileOps.ExecuteReadFile).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"target_file":                    map[string]interface{}{"type": "string", "description": "The path of the file to read"},
				"start_line_one_indexed":         map[string]interface{}{"type": "integer", "description": "Start line (1-indexed)"},
				"end_line_one_indexed_inclusive": map[string]interface{}{"type": "integer", "description": "End line (inclusive)"},
			},
			"required": []string{"target_file"},
		}),
		tools.NewAdapter("list_dir", "List directory", sysOps.ExecuteListDir).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"absolute_path": map[string]interface{}{"type": "string", "description": "Absolute path to list"},
			},
			"required": []string{"absolute_path"},
		}),
		tools.NewAdapter("grep_search", "Search code using grep", searchOps.ExecuteGrepSearch).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query":           map[string]interface{}{"type": "string", "description": "Regex query"},
				"include_pattern": map[string]interface{}{"type": "string", "description": "File pattern to include"},
			},
			"required": []string{"query"},
		}),
		tools.NewAdapter("file_search", "Find file paths", searchOps.ExecuteFileSearch).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"query": map[string]interface{}{"type": "string", "description": "Filename query"},
			},
			"required": []string{"query"},
		}),
	}

	return &ConductorAgent{
		BaseAgent:   BaseAgent{LLM: llm, Publisher: publisher},
		RepoAgent:   repo,
		CodingAgent: coding,
		Adapters:    append(adapters, delegateRepo, delegateCoding),
		maxSteps:    maxSteps,
	}
}

func (a *ConductorAgent) Name() string {
	return "Conductor"
}

func (a *ConductorAgent) Run(ctx context.Context, input string) (string, error) {
	messages := []llms.MessageContent{
		{
			Role: llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{llms.TextPart(`You are the Conductor Agent. You manage the software development process.
Your responsibilities:
1. Analyze the user's request.
2. Use 'delegate_repo' to analyze the codebase and gather context.
3. Plan the implementation.
4. Use 'delegate_coding' to implement changes and fix errors.
5. Review the results.

Always start by analyzing the repo if you need context.
Do not write code yourself. Delegate to Coding-Agent.`)},
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
			a.Publisher.Publish("status_update", fmt.Sprintf("ConductorAgent is thinking (step %d/%d)...", i+1, a.maxSteps))
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
				a.Publisher.Publish("ai_response", msg.Content)
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
				})
			}

			for _, t := range a.Adapters {
				if t.Name() == tc.FunctionCall.Name {
					found = true
					toolResult, err = t.Call(ctx, tc.FunctionCall.Arguments)
					if err != nil {
						toolResult = fmt.Sprintf("Error: %v", err)
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
				})
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
		}
	}

	return "", fmt.Errorf("ConductorAgent exceeded max steps")
}
