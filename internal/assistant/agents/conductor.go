package agents

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"

	"codeactor/internal/assistant/tools"
	"codeactor/pkg/messaging"

	"github.com/tmc/langchaingo/llms"
)

//go:embed conductor.prompt.md
var conductorPrompt string

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
	flowOps := tools.NewFlowControlTool(repo.projectDir)

	adapters := []*tools.Adapter{
		tools.NewAdapter("finish", "Indicate that the current task is finished. The output of this tool call will be a description of why the task is finished, which could be because the task is completed or cannot be completed and must be terminated.", flowOps.ExecuteFinish).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"reason": map[string]interface{}{"type": "string", "description": "A description of why the task is finished, e.g., task completed, cannot complete, or must terminate."},
			},
			"required": []string{"reason"},
		}),
		tools.NewAdapter("read_file", `Read the contents of a file. the output of this tool call will be the 1-indexed file contents from start_line_one_indexed to end_line_one_indexed_inclusive, together with a summary of the lines outside start_line_one_indexed and end_line_one_indexed_inclusive.
Note that this call can view at most 250 lines at a time and 200 lines minimum.

When using this tool to gather information, it's your responsibility to ensure you have the COMPLETE context. Specifically, each time you call this command you should:
1) Assess if the contents you viewed are sufficient to proceed with your task.
2) Take note of where there are lines not shown.
3) If the file contents you have viewed are insufficient, and you suspect they may be in lines not shown, proactively call the tool again to view those lines.
4) When in doubt, call this tool again to gather more information. Remember that partial file views may miss critical dependencies, imports, or functionality.

In some cases, if reading a range of lines is not enough, you may choose to read the entire file.
Reading entire files is often wasteful and slow, especially for large files (i.e. more than a few hundred lines). So you should use this option sparingly.
Reading the entire file is not allowed in most cases. You are only allowed to read the entire file if it has been edited or manually attached to the conversation by the user.`, fileOps.ExecuteReadFile).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"target_file": map[string]interface{}{
					"type":        "string",
					"description": "The path of the file to read. You can use either a relative path in the workspace or an absolute path. If an absolute path is provided, it will be preserved as is.",
				},
				"start_line_one_indexed": map[string]interface{}{
					"type":        "integer",
					"description": "The one-indexed line number to start reading from (inclusive).",
				},
				"end_line_one_indexed_inclusive": map[string]interface{}{
					"type":        "integer",
					"description": "The one-indexed line number to end reading at (inclusive).",
				},
				"should_read_entire_file": map[string]interface{}{
					"type":        "boolean",
					"description": "Whether to read the entire file. Defaults to false.",
				},
				"explanation": map[string]interface{}{
					"type":        "string",
					"description": "One sentence explanation as to why this tool is being used, and how it contributes to the goal.",
				},
			},
			"required": []string{"target_file", "should_read_entire_file", "start_line_one_indexed", "end_line_one_indexed_inclusive"},
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
			Role:  llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{llms.TextPart(conductorPrompt)},
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
			if tc.FunctionCall.Name == "finish" {
				return "Task completed successfully", nil
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
