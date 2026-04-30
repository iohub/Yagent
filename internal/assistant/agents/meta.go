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

//go:embed meta.prompt.md
var metaPrompt string

// MetaAgent designs and instantiates specialized agents on-the-fly.
// It uses embedded prompt engineering best practices to craft a custom
// system prompt, select appropriate tools, and execute the task.
type MetaAgent struct {
	BaseAgent
	GlobalCtx *globalctx.GlobalCtx
	Adapters  []*tools.Adapter
	maxSteps  int
}

func NewMetaAgent(globalCtx *globalctx.GlobalCtx, llm llms.LLM, maxSteps int) *MetaAgent {
	var toolDefs []ToolDefinition
	if err := json.Unmarshal(ToolsJSON, &toolDefs); err != nil {
		slog.Error("Failed to unmarshal meta tools", "error", err)
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
		case "finish":
			fn = globalCtx.FlowOps.ExecuteFinish
		default:
			slog.Warn("Unknown tool in meta tools.json", "name", def.Name)
			continue
		}

		adapter := tools.NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters)
		adapters = append(adapters, adapter)
	}

	return &MetaAgent{
		BaseAgent: BaseAgent{
			LLM:       llm,
			Publisher: globalCtx.Publisher,
		},
		Adapters:  adapters,
		maxSteps:  maxSteps,
		GlobalCtx: globalCtx,
	}
}

func (a *MetaAgent) Name() string {
	return "Meta-Agent"
}

// Run executes the meta agent workflow:
//  1. Design a specialized agent using prompt engineering best practices
//  2. Execute the designed agent with appropriate tools
//  3. Return structured JSON with the design and execution result
func (a *MetaAgent) Run(ctx context.Context, input string) (string, error) {
	systemPrompt := a.GlobalCtx.FormatPrompt(metaPrompt)

	messages := []llms.MessageContent{
		{
			Role:  llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{llms.TextPart(systemPrompt)},
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
		slog.Debug("MetaAgent calling LLM", "step", i)
		resp, err := a.LLM.GenerateContent(ctx, messages, llms.WithTools(llmTools))
		if err != nil {
			slog.Error("MetaAgent LLM error", "error", err, "step", i)
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
					"tool_name":    tc.FunctionCall.Name,
					"arguments":    tc.FunctionCall.Arguments,
					"tool_call_id": tc.ID,
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
					"tool_name":    tc.FunctionCall.Name,
					"result":       toolResult,
					"tool_call_id": tc.ID,
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

	return "", fmt.Errorf("MetaAgent exceeded max steps")
}
