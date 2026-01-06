package agents

import (
	"context"
	"fmt"
	"log/slog"

	"codeactor/internal/assistant/tools"

	"github.com/tmc/langchaingo/llms"
)

type RepoAgent struct {
	BaseAgent
	Adapters []*tools.Adapter
}

func NewRepoAgent(llm llms.LLM, fileOps *tools.FileOperationsTool, searchOps *tools.SearchOperationsTool, sysOps *tools.SystemOperationsTool) *RepoAgent {
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

	return &RepoAgent{
		BaseAgent: BaseAgent{
			LLM: llm,
		},
		Adapters: adapters,
	}
}

func (a *RepoAgent) Name() string {
	return "Repo-Agent"
}

func (a *RepoAgent) Run(ctx context.Context, input string) (string, error) {
	messages := []llms.MessageContent{
		{
			Role:  llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{llms.TextPart("You are the Repo-Agent. Your role is to analyze the codebase, find definitions, and explain code. You are READ-ONLY. You cannot modify files.")},
		},
		{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart(input)},
		},
	}

	// Convert adapters to llms.Tool
	llmTools := make([]llms.Tool, len(a.Adapters))
	for i, ad := range a.Adapters {
		llmTools[i] = ad.ToLLMSTool()
	}

	maxSteps := 3
	for i := 0; i < maxSteps; i++ {
		slog.Debug("RepoAgent calling LLM", "step", i, "messages", messages)
		resp, err := a.LLM.GenerateContent(ctx, messages, llms.WithTools(llmTools))
		if err != nil {
			slog.Error("RepoAgent LLM error", "error", err, "step", i)
			return "", err
		}

		msg := resp.Choices[0]
		slog.Debug("RepoAgent LLM response", "step", i, "message", msg)
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
			for _, t := range a.Adapters {
				if t.Name() == tc.FunctionCall.Name {
					found = true
					toolResult, err = t.Call(ctx, tc.FunctionCall.Arguments)
					if err != nil {
						toolResult = fmt.Sprintf("Error executing tool %s: %v", t.Name(), err)
					}
					break
				}
			}
			if !found {
				toolResult = fmt.Sprintf("Tool %s not found", tc.FunctionCall.Name)
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

	return "", fmt.Errorf("RepoAgent exceeded max steps")
}
