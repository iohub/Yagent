package agents

import (
	"context"
	_ "embed"
	"encoding/json"

	"codeactor/internal/globalctx"
	"codeactor/internal/llm"
	"codeactor/internal/tools"
)

//go:embed devops.prompt.md
var devopsPrompt string

type DevOpsAgent struct {
	BaseAgent
	GlobalCtx *globalctx.GlobalCtx
	Adapters  []*tools.Adapter
	maxSteps  int
}

func NewDevOpsAgent(globalCtx *globalctx.GlobalCtx, llm llm.Engine, maxSteps int) *DevOpsAgent {
	var toolDefs []ToolDefinition
	if err := json.Unmarshal(ToolsJSON, &toolDefs); err != nil {
		// Non-fatal: agent falls back to no-tool mode.
	}

	// DevOps agent uses a curated set of tools for operational tasks:
	// run_bash for command execution, file tools for inspection, and
	// thinking/micro_agent for analysis and self-correction.
	adapters := make([]*tools.Adapter, 0, len(toolDefs))
	for _, def := range toolDefs {
		var fn tools.ToolFunc
		switch def.Name {
		case "run_bash":
			fn = globalCtx.SysOps.ExecuteRunBash
		case "read_file":
			fn = globalCtx.FileOps.ExecuteReadFile
		case "list_dir":
			fn = globalCtx.FileOps.ExecuteListDir
		case "print_dir_tree":
			fn = globalCtx.FileOps.ExecutePrintDirTree
		case "search_by_regex":
			fn = globalCtx.SearchOps.ExecuteGrepSearch
		case "thinking":
			fn = func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
				inputBytes, _ := json.Marshal(params)
				return globalCtx.ThinkingTool.Call(ctx, string(inputBytes))
			}
		case "micro_agent":
			fn = globalCtx.MicroAgentTool.Execute
		case "agent_exit":
			fn = globalCtx.FlowOps.ExecuteAgentExit
		default:
			continue
		}

		adapter := tools.NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters)
		adapters = append(adapters, adapter)
	}
	tools.SetGuardOnAdapters(adapters, globalCtx.Guard)

	return &DevOpsAgent{
		BaseAgent: BaseAgent{
			LLM:       llm,
			Publisher: globalCtx.Publisher,
		},
		GlobalCtx: globalCtx,
		Adapters:  adapters,
		maxSteps:  maxSteps,
	}
}

func (a *DevOpsAgent) Name() string {
	return "DevOps-Agent"
}

func (a *DevOpsAgent) Run(ctx context.Context, input string) (string, error) {
	cfg := ExecutorConfig{
		SystemPrompt: a.GlobalCtx.FormatPrompt(devopsPrompt),
		UserInput:    input,
		Adapters:     a.Adapters,
		LLM:          a.LLM,
		MaxSteps:     a.maxSteps,
		Publisher:    a.Publisher,
		AgentName:    a.Name(),
		StopOnFinish: true,
	}
	return RunAgentLoop(ctx, cfg)
}
