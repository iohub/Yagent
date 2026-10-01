package agents

import (
	"context"
	_ "embed"
	"encoding/json"

	"yagent/internal/globalctx"
	"yagent/internal/llm"
	"yagent/internal/tools"
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
	var toolDefs []tools.ToolDefinition
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
		case "ask_user_for_help":
			if globalCtx.FullYoloMode {
				continue
			}
			fn = globalCtx.FlowOps.ExecuteAskUserForHelp
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

func (a *DevOpsAgent) Run(ctx context.Context, input string) (AgentResult, error) {
	// P0 Step 5：迁移至 runSubAgentLoop（统一内核 director.Planner）。
	// 零值透传：LLMTimeout=0 → runSubAgentLoop 内兜底 5min（等价 executor.go:118）；
	// ToolTimeout=0 → 180s，与 RunAgentLoop 现状一致；StopOnFinish=true 保持现状；
	// RolloutCollabMode="single" 由 runSubAgentLoop 内置（EnableCollaboration 等价）。
	systemPrompt := a.GlobalCtx.FormatPrompt(devopsPrompt)
	// 上下文压缩配置（tool 结果截断）
	ec := a.GlobalCtx.EnhancedCommander
	outcome, err := runSubAgentLoop(ctx, SubAgentLoopConfig{
		SystemPrompt:       systemPrompt,
		UserInput:          input,
		Adapters:           a.Adapters,
		LLM:                a.LLM,
		MaxSteps:           a.maxSteps,
		Publisher:          a.Publisher,
		AgentName:          a.Name(),
		StopOnFinish:       true,
		CompressEnable:     ec.Enable && ec.EnableContextCompression,
		CompressThreshold:  ec.ContextCompressionThreshold,
		CompressKeepTokens: ec.ToolResultKeepTokens,
	})
	if err != nil {
		return AgentResult{}, err
	}
	return AgentResult{
		Text:   outcome.Text,
		Memory: ConvertLLMHistoryToMemory(outcome.History),
	}, nil
}
