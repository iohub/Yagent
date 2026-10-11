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

// DevOpsAgentParams 聚合 NewDevOpsAgent 所需的全部依赖。
// 用具名字段替代原先 13 个位置参数，让调用点自文档化。
type DevOpsAgentParams struct {
	Formatter  PromptFormatter
	Publisher  EventBus
	Env        Env
	Files      FileToolSet
	Search     SearchToolSet
	Sys        SysToolSet
	Flow       FlowToolSet
	Thinker    Thinker
	MicroAgent MicroAgentRunner
	Guard      *tools.WorkspaceGuard

	EnhancedCommanderCfg config.EnhancedCommanderConfig

	LLM      llm.Engine
	MaxSteps int
}

// NewDevOpsAgent 构造 DevOpsAgent：装配运维工具集后返回。
func NewDevOpsAgent(p DevOpsAgentParams) *DevOpsAgent {
	return &DevOpsAgent{
		BaseAgent: BaseAgent{
			LLM:       p.LLM,
			Publisher: p.Publisher,
		},
		promptFmt: p.Formatter,
		Adapters:  buildDevOpsAdapters(p),
		maxSteps:  p.MaxSteps,
		ecCfg:     p.EnhancedCommanderCfg,
	}
}

// buildDevOpsAdapters 装配 DevOps-Agent 的运维工具集：
// run_bash（命令执行）、文件工具（巡检）、thinking/micro_agent（分析与自纠错）。
func buildDevOpsAdapters(p DevOpsAgentParams) []*tools.Adapter {
	var toolDefs []tools.ToolDefinition
	if err := json.Unmarshal(ToolsJSON, &toolDefs); err != nil {
		// Non-fatal: agent falls back to no-tool mode.
	}

	adapters := make([]*tools.Adapter, 0, len(toolDefs))
	for _, def := range toolDefs {
		var fn tools.ToolFunc
		switch def.Name {
		case "run_bash":
			fn = p.Sys.ExecuteRunBash
		case "read_file":
			fn = p.Files.ExecuteReadFile
		case "list_dir":
			fn = p.Files.ExecuteListDir
		case "print_dir_tree":
			fn = p.Files.ExecutePrintDirTree
		case "search_by_regex":
			fn = p.Search.ExecuteGrepSearch
		case "thinking":
			fn = func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
				inputBytes, _ := json.Marshal(params)
				return p.Thinker.Call(ctx, string(inputBytes))
			}
		case "micro_agent":
			fn = p.MicroAgent.Execute
		case "agent_exit":
			fn = p.Flow.ExecuteAgentExit
		case "ask_user_for_help":
			if p.Env.FullYoloMode() {
				continue
			}
			fn = p.Flow.ExecuteAskUserForHelp
		default:
			continue
		}

		adapter := tools.NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters).WithReadOnlyIfKnown()
		adapters = append(adapters, adapter)
	}
	tools.SetGuardOnAdapters(adapters, p.Guard)

	return adapters
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
	// 普通版：Text 无程序化消费点（delegate_devops 返回值由 Director 层
	// FormatForDirector 接管；repo 内部嵌套委派仅作 LLM tool_result）。
	return FinalizeResult(a.Name(), input, result), nil
}
