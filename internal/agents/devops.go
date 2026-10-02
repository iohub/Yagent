package agents

import (
	"context"
	_ "embed"
	"encoding/json"

	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/tools"
)

//go:embed devops.prompt.md
var devopsPrompt string

type DevOpsAgent struct {
	BaseAgent
	promptFmt PromptFormatter
	Adapters  []*tools.Adapter
	maxSteps  int
	ecCfg     config.EnhancedCommanderConfig
}

// NewDevOpsAgent 构造 DevOpsAgent，仅声明其所需窄依赖：
// prompt 格式化、事件总线、环境视图（FullYoloMode）、文件/搜索/系统工具集、
// 流程控制、认知思考、微代理工具、工作区守护与上下文压缩配置。
func NewDevOpsAgent(formatter PromptFormatter, publisher EventBus, env Env, files FileToolSet, search SearchToolSet, sys SysToolSet, flow FlowToolSet, thinker Thinker, microAgent MicroAgentRunner, guard *tools.WorkspaceGuard, ecCfg config.EnhancedCommanderConfig, llm llm.Engine, maxSteps int) *DevOpsAgent {
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
			fn = sys.ExecuteRunBash
		case "read_file":
			fn = files.ExecuteReadFile
		case "list_dir":
			fn = files.ExecuteListDir
		case "print_dir_tree":
			fn = files.ExecutePrintDirTree
		case "search_by_regex":
			fn = search.ExecuteGrepSearch
		case "thinking":
			fn = func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
				inputBytes, _ := json.Marshal(params)
				return thinker.Call(ctx, string(inputBytes))
			}
		case "micro_agent":
			fn = microAgent.Execute
		case "agent_exit":
			fn = flow.ExecuteAgentExit
		case "ask_user_for_help":
			if env.FullYoloMode() {
				continue
			}
			fn = flow.ExecuteAskUserForHelp
		default:
			continue
		}

		adapter := tools.NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters)
		adapters = append(adapters, adapter)
	}
	tools.SetGuardOnAdapters(adapters, guard)

	return &DevOpsAgent{
		BaseAgent: BaseAgent{
			LLM:       llm,
			Publisher: publisher,
		},
		promptFmt: formatter,
		Adapters:  adapters,
		maxSteps:  maxSteps,
		ecCfg:     ecCfg,
	}
}

func (a *DevOpsAgent) Name() string {
	return "DevOps-Agent"
}

func (a *DevOpsAgent) Run(ctx context.Context, input string) (AgentResult, error) {
	cfg := DefaultExecutorConfig()
	systemPrompt := a.promptFmt.FormatPrompt(devopsPrompt)
	cfg.SystemPrompt = systemPrompt
	cfg.UserInput = input
	cfg.Adapters = a.Adapters
	cfg.LLM = a.LLM
	cfg.MaxSteps = a.maxSteps
	cfg.Publisher = a.Publisher
	cfg.AgentName = a.Name()
	cfg.StopOnFinish = true
	// 上下文压缩配置（tool 结果截断）
	ec := a.ecCfg
	cfg.EnableContextCompression = ec.Enable && ec.EnableContextCompression
	cfg.ContextCompressionThreshold = ec.ContextCompressionThreshold
	cfg.ToolResultKeepTokens = ec.ToolResultKeepTokens
	// EnableCollaboration 已默认 true
	result, err := RunAgentLoop(ctx, cfg)
	if err != nil {
		return AgentResult{}, err
	}
	return AgentResult{
		Text:   result.Text,
		Memory: ConvertLLMHistoryToMemory(result.History),
	}, nil
}
