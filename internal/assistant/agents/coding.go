package agents

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"

	"codeactor/internal/assistant/tools"
	"codeactor/internal/globalctx"

	"github.com/tmc/langchaingo/llms"
)

//go:embed coding.prompt.md
var codingPrompt string

type ToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

type CodingAgent struct {
	BaseAgent
	GlobalCtx *globalctx.GlobalCtx
	Adapters  []*tools.Adapter
	maxSteps  int
}

func NewCodingAgent(globalCtx *globalctx.GlobalCtx, llm llms.LLM, maxSteps int) *CodingAgent {
	var toolDefs []ToolDefinition
	if err := json.Unmarshal(ToolsJSON, &toolDefs); err != nil {
		slog.Error("Failed to unmarshal coding tools", "error", err)
	}

	adapters := make([]*tools.Adapter, 0, len(toolDefs))
	for _, def := range toolDefs {
		var fn tools.ToolFunc
		switch def.Name {
		case "read_file":
			fn = globalCtx.FileOps.ExecuteReadFile
		case "search_replace_in_file":
			fn = globalCtx.ReplaceTool.ExecuteReplaceBlock
		case "create_file":
			fn = globalCtx.FileOps.ExecuteCreateFile
		case "run_terminal_cmd":
			fn = globalCtx.SysOps.ExecuteRunTerminalCmd
		case "search_by_regex":
			fn = globalCtx.SearchOps.ExecuteGrepSearch
		case "delete_file":
			fn = globalCtx.FileOps.ExecuteDeleteFile
		case "rename_file":
			fn = globalCtx.FileOps.ExecuteRenameFile
		case "list_dir":
			fn = globalCtx.FileOps.ExecuteListDir
		case "print_dir_tree":
			fn = globalCtx.FileOps.ExecutePrintDirTree
		case "semantic_search":
			fn = globalCtx.RepoOps.ExecuteSemanticSearch
		case "query_code_skeleton":
			fn = globalCtx.RepoOps.ExecuteQueryCodeSkeleton
		case "query_code_snippet":
			fn = globalCtx.RepoOps.ExecuteQueryCodeSnippet
		case "thinking":
			fn = func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
				inputBytes, _ := json.Marshal(params)
				return globalCtx.ThinkingTool.Call(ctx, string(inputBytes))
			}
		default:
			slog.Warn("Unknown tool in tools.json", "name", def.Name)
			continue
		}

		adapter := tools.NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters)
		adapters = append(adapters, adapter)
	}

	return &CodingAgent{
		BaseAgent: BaseAgent{
			LLM:       llm,
			Publisher: globalCtx.Publisher,
		},
		Adapters:  adapters,
		maxSteps:  maxSteps,
		GlobalCtx: globalCtx,
	}
}

func (a *CodingAgent) Name() string {
	return "Coding-Agent"
}

func (a *CodingAgent) Run(ctx context.Context, input string) (string, error) {
	fmt.Println("Coding-Agent input:", input)
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
