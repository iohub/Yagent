package agents

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"

	"codeactor/internal/tools"
	"codeactor/internal/globalctx"

	"codeactor/internal/llm"
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

func NewCodingAgent(globalCtx *globalctx.GlobalCtx, llm llm.Engine, maxSteps int, implPlanAgent *ImplPlanAgent) *CodingAgent {
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
		case "run_bash":
			fn = globalCtx.SysOps.ExecuteRunBash
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
		case "micro_agent":
			fn = globalCtx.MicroAgentTool.Execute
		case "agent_exit":
			fn = globalCtx.FlowOps.ExecuteAgentExit
		case "ask_user_for_help":
			fn = globalCtx.FlowOps.ExecuteAskUserForHelp
		default:
			slog.Warn("Unknown tool in tools.json", "name", def.Name)
			continue
		}

		adapter := tools.NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters)
		adapters = append(adapters, adapter)
	}

	// Add delegate_impl_plan tool to delegate design tasks to ImplPlanAgent
	delegateDesign := tools.NewAdapter("delegate_impl_plan",
		"Delegate to the Implementation Plan Agent to analyze a coding task and generate a structured implementation plan. The agent analyzes codebase context and produces a detailed plan document covering architecture design, module breakdown, interface definitions, data flow, implementation order, error handling, and testing strategy.",
		func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			task, ok := params["task"].(string)
			if !ok {
				return nil, fmt.Errorf("task parameter required")
			}
			contextInfo, _ := params["context"].(string) // optional
			return implPlanAgent.Run(ctx, task, contextInfo)
		}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task":    map[string]interface{}{"type": "string", "description": "The coding task description that needs a design plan"},
			"context": map[string]interface{}{"type": "string", "description": "Additional context information, such as repository analysis results, relevant code background, etc."},
		},
		"required": []string{"task"},
	})
	adapters = append(adapters, delegateDesign)

	tools.SetGuardOnAdapters(adapters, globalCtx.Guard)

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
	systemPrompt := a.GlobalCtx.FormatPrompt(codingPrompt)

	cfg := ExecutorConfig{
		SystemPrompt: systemPrompt,
		UserInput:    input,
		Adapters:     a.Adapters,
		LLM:          a.LLM,
		MaxSteps:     a.maxSteps,
		Publisher:    a.Publisher,
		AgentName:    a.Name(),
	}
	return RunAgentLoop(ctx, cfg)
}
