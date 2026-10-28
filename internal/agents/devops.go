package agents

import (
	"context"
	_ "embed"
	"yagent/internal/blackboard"
	"yagent/internal/toolspec"

	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/thinklink"
	"yagent/internal/tools"
)

//go:embed devops.prompt.md
var devopsPrompt string

type DevOpsAgent struct {
	BaseAgent
	promptFmt   PromptFormatter
	params      DevOpsAgentParams
	maxSteps    int
	stepRetries int
	ecCfg       config.EnhancedCommanderConfig
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

	LLM         llm.Engine
	MaxSteps    int
	StepRetries int
	// Blueprint 是仓库蓝图视图（nil = 这个项目没有蓝图）。
	// 这三个 Agent 不做常驻注入，但 read_blueprint 在它们的工具面上要能真读到东西 ——
	// 绑 nil 会让工具回答"没有蓝图"，那是一句谎话。
	Blueprint *BlueprintView

	// External 是外部工具规格与技能通道（见 external.go）。
	External External
}

// NewDevOpsAgent 构造 DevOpsAgent：装配运维工具集后返回。
func NewDevOpsAgent(p DevOpsAgentParams) *DevOpsAgent {
	return &DevOpsAgent{
		BaseAgent: BaseAgent{
			LLM:       p.LLM,
			Publisher: p.Publisher,
		},
		promptFmt:   p.Formatter,
		params:      p,
		maxSteps:    p.MaxSteps,
		stepRetries: p.StepRetries,
		ecCfg:       p.EnhancedCommanderCfg,
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
	// DevOps 的画像：Shell + 只读文件 + 两个认知工具 + 流程控制；没有写文件能力。
	deps := depsFor("devops", a.params.Env, a.params.Guard, blackboard.BoardIDFrom(ctx), a.params.Blueprint, a.params.External)
	deps.Sys = a.params.Sys
	deps.Files = a.params.Files
	deps.Search = a.params.Search
	deps.Flow = a.params.Flow
	deps.Thinker = a.params.Thinker
	deps.Micro = a.params.MicroAgent
	cfg.Surface = surfaceFor(toolspec.ProfileDevOps, deps, a.Name())
	cfg.SkillCatalog = skillCatalogFor("devops", a.params.External.Skills)
	cfg.LLM = a.LLM
	cfg.MaxSteps = a.maxSteps
	cfg.StepRetries = a.stepRetries
	cfg.Publisher = a.Publisher
	cfg.AgentName = a.Name()
	cfg.StopOnFinish = true
	// 一次委派一个 store：RebuildPrompt 保留全部用户输入，本实例会被多次委派复用。
	store := thinklink.NewStore(0)
	store.AddUserInput(input, 0)
	cfg.UltimateThinkLink = store
	// 上下文压缩（三档）的唯一装配点：见 compression_options.go。零值 = 不压缩。
	cfg.Compression = compressionOptionsFromConfig(a.ecCfg)
	// EnableCollaboration 已默认 true
	result, err := RunAgentLoop(ctx, cfg)
	if err != nil {
		return AgentResult{}, err
	}
	// 普通版：Text 无程序化消费点（delegate_devops 返回值由 Director 层
	// FormatForDirector 接管；repo 内部嵌套委派仅作 LLM tool_result）。
	return FinalizeResult(a.Name(), input, result), nil
}
