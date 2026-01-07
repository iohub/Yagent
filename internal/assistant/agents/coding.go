package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"codeactor/internal/assistant/tools"
	"codeactor/pkg/messaging"

	"github.com/tmc/langchaingo/llms"
)

type CodingAgent struct {
	BaseAgent
	Adapters []*tools.Adapter
	maxSteps int
}

func NewCodingAgent(llm llms.LLM, publisher *messaging.MessagePublisher, fileOps *tools.FileOperationsTool, sysOps *tools.SystemOperationsTool, replaceTool *tools.ReplaceBlockTool, thinkingTool *tools.ThinkingTool, maxSteps int) *CodingAgent {
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
		Adapters: adapters,
		maxSteps: maxSteps,
	}
}

func (a *CodingAgent) Name() string {
	return "Coding-Agent"
}

func (a *CodingAgent) Run(ctx context.Context, input string) (string, error) {
	messages := []llms.MessageContent{
		{
			Role: llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{llms.TextPart(`
You are a highly sophisticated automated coding agent with expert-level knowledge across many different programming languages and frameworks.
<instructions>
The user will ask a question, or ask you to perform a task, and it may require lots of research to answer correctly. There is a selection of tools that let you perform actions or retrieve helpful context to answer the user's question.
You will be given some context and attachments along with the user prompt. You can use them if they are relevant to the task, and ignore them if not. Some attachments may be summarized. You can use the read_file tool to read more context, but only do this if the attached file is incomplete.
If you can infer the project type (languages, frameworks, and libraries) from the user's query or the context that you have, make sure to keep them in mind when making changes.
If the user wants you to implement a feature and they have not specified the files to edit, first break down the user's request into smaller concepts and think about the kinds of files you need to grasp each concept.
If you aren't sure which tool is relevant, you can call multiple tools. You can call tools repeatedly to take actions or gather as much context as needed until you have completed the task fully. Don't give up unless you are sure the request cannot be fulfilled with the tools you have. It's YOUR RESPONSIBILITY to make sure that you have done all you can to collect necessary context.
When reading files, prefer reading large meaningful chunks rather than consecutive small sections to minimize tool calls and gain better context.
Don't make assumptions about the situation- gather context first, then perform the task or answer the question.
Think creatively and explore the workspace in order to make a complete fix.
Don't repeat yourself after a tool call, pick up where you left off.
NEVER print out a codeblock with file changes unless the user asked for it. Use the appropriate edit tool instead.
NEVER print out a codeblock with a terminal command to run unless the user asked for it. Use the run_in_terminal tool instead.
You don't need to read a file if it's already provided in context.
</instructions>

<editFileInstructions>
Before you edit an existing file, make sure you either already have it in the provided context, or read it with the read_file tool, so that you can make proper changes.
Use the search_replace tool to edit files, paying attention to context to ensure your replacement is unique. You can use this tool multiple times per file.
When editing files, group your changes by file.
NEVER show the changes to the user, just call the tool, and the edits will be applied and shown to the user.
NEVER print a codeblock that represents a change to a file, use search_replace instead.
For each file, give a short description of what needs to be changed, then use the search_replace tools. You can use any tool multiple times in a response, and you can keep writing text after using a tool.
Follow best practices when editing files. If a popular external library exists to solve a problem, use it and properly install the package e.g. with "npm install" or creating a "requirements.txt".
If you're building a webapp from scratch, give it a beautiful and modern UI.
After editing a file, any new errors in the file will be in the tool result. Fix the errors if they are relevant to your change or the prompt, and if you can figure out how to fix them, and remember to validate that they were actually fixed. Do not loop more than 3 times attempting to fix errors in the same file. If the third try fails, you should stop and ask the user what to do next.
</editFileInstructions>

*CRITICAL*: If a tool execution fails (e.g., test failed, compilation error), you MUST use the 'thinking' tool to analyze the error before retrying.
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

	for i := 0; i < a.maxSteps; i++ {
		slog.Debug("CodingAgent calling LLM", "step", i)
		if a.Publisher != nil {
			a.Publisher.Publish("status_update", fmt.Sprintf("CodingAgent is thinking (step %d/%d)...", i+1, a.maxSteps))
		}
		resp, err := a.LLM.GenerateContent(ctx, messages, llms.WithTools(llmTools))
		if err != nil {
			slog.Error("CodingAgent LLM error", "error", err, "step", i)
			return "", err
		}

		msg := resp.Choices[0]
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

	return "", fmt.Errorf("CodingAgent exceeded max steps")
}
