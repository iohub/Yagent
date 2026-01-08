package agents

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"

	"codeactor/internal/assistant/tools"
	"codeactor/internal/globalctx"
	"codeactor/pkg/messaging"

	"github.com/tmc/langchaingo/llms"
)

//go:embed coding.prompt.md
var codingPrompt string

type CodingAgent struct {
	BaseAgent
	GlobalCtx *globalctx.GlobalCtx
	Adapters  []*tools.Adapter
	maxSteps  int
}

func NewCodingAgent(ctx *globalctx.GlobalCtx, llm llms.LLM, publisher *messaging.MessagePublisher, fileOps *tools.FileOperationsTool, sysOps *tools.SystemOperationsTool, replaceTool *tools.ReplaceBlockTool, thinkingTool *tools.ThinkingTool, maxSteps int) *CodingAgent {
	adapters := []*tools.Adapter{
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
		tools.NewAdapter("search_replace", replaceTool.Description(), replaceTool.ExecuteReplaceBlock).WithSchema(map[string]interface{}{
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
			LLM:       llm,
			Publisher: publisher,
		},
		Adapters:  adapters,
		maxSteps:  maxSteps,
		GlobalCtx: ctx,
	}
}

func (a *CodingAgent) Name() string {
	return "Coding-Agent"
}

func (a *CodingAgent) Run(ctx context.Context, input string) (string, error) {
	messages := []llms.MessageContent{
		{
			Role:  llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{llms.TextPart(a.GlobalCtx.FormatPrompt(codingPrompt))},
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

	for i := 0; i < a.maxSteps; i++ {
		if a.Publisher != nil {
			a.Publisher.Publish("status_update", fmt.Sprintf("CodingAgent is thinking (step %d/%d)...", i+1, a.maxSteps), a.Name())
		}
		resp, err := a.LLM.GenerateContent(ctx, messages, llms.WithTools(llmTools))
		if err != nil {
			slog.Error("CodingAgent LLM error", "error", err, "step", i)
			return "", err
		}

		msg := resp.Choices[0]
		if msg.Content != "" {
			if a.Publisher != nil {
				a.Publisher.Publish("ai_response", msg.Content, a.Name())
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
				}, a.Name())
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
				}, a.Name())
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
