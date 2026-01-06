package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"codeactor/internal/assistant/tools"

	"github.com/tmc/langchaingo/llms"
)

type CodingAgent struct {
	BaseAgent
	Adapters []*tools.Adapter
}

func NewCodingAgent(llm llms.LLM, fileOps *tools.FileOperationsTool, sysOps *tools.SystemOperationsTool, replaceTool *tools.ReplaceBlockTool, thinkingTool *tools.ThinkingTool) *CodingAgent {
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
		tools.NewAdapter("replace_block", replaceTool.Description(), func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			inputBytes, _ := json.Marshal(params)
			return replaceTool.Call(ctx, string(inputBytes))
		}).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"file_path":     map[string]interface{}{"type": "string", "description": "File path to modify"},
				"search_block":  map[string]interface{}{"type": "string", "description": "Exact code block to replace"},
				"replace_block": map[string]interface{}{"type": "string", "description": "New code block"},
			},
			"required": []string{"file_path", "search_block", "replace_block"},
		}),
		tools.NewAdapter("write_file", "Create or overwrite file", fileOps.ExecuteWriteFile).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"file_path": map[string]interface{}{"type": "string", "description": "File path to write"},
				"content":   map[string]interface{}{"type": "string", "description": "File content"},
			},
			"required": []string{"file_path", "content"},
		}),
		tools.NewAdapter("run_shell_command", "Run shell command", sysOps.ExecuteRunTerminalCmd).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{"type": "string", "description": "Command to run"},
			},
			"required": []string{"command"},
		}),
		tools.NewAdapter("thinking", thinkingTool.Description(), func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			inputBytes, _ := json.Marshal(params)
			return thinkingTool.Call(ctx, string(inputBytes))
		}).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"error_message":  map[string]interface{}{"type": "string", "description": "The error encountered"},
				"current_action": map[string]interface{}{"type": "string", "description": "Action that failed"},
				"observation":    map[string]interface{}{"type": "string", "description": "What happened"},
			},
			"required": []string{"error_message", "current_action"},
		}),
	}

	return &CodingAgent{
		BaseAgent: BaseAgent{
			LLM: llm,
		},
		Adapters: adapters,
	}
}

func (a *CodingAgent) Name() string {
	return "Coding-Agent"
}

func (a *CodingAgent) Run(ctx context.Context, input string) (string, error) {
	messages := []llms.MessageContent{
		{
			Role: llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{llms.TextPart(`You are the Coding-Agent. Your role is to write code, run tests, and fix errors.
You have access to tools to read files, modify files (replace_block), and run shell commands.
CRITICAL: If a tool execution fails (e.g., test failed, compilation error), you MUST use the 'thinking' tool to analyze the error before retrying.
Do not blindly retry. Analyze -> Plan -> Fix.`)},
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

	maxSteps := 20
	for i := 0; i < maxSteps; i++ {
		slog.Debug("CodingAgent calling LLM", "step", i)
		resp, err := a.LLM.GenerateContent(ctx, messages, llms.WithTools(llmTools))
		if err != nil {
			slog.Error("CodingAgent LLM error", "error", err, "step", i)
			return "", err
		}

		msg := resp.Choices[0]

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
						toolResult = fmt.Sprintf("Error: %v", err)
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

	return "", fmt.Errorf("CodingAgent exceeded max steps")
}
