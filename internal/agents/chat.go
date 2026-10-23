package agents

import (
	"context"
	_ "embed"

	"yagent/internal/blackboard"
	"yagent/internal/tools"
	"yagent/internal/toolspec"

	"yagent/internal/llm"
)

//go:embed chat.prompt.md
var chatPrompt string

type ChatAgent struct {
	BaseAgent
	promptFmt   PromptFormatter
	params      ChatAgentParams
	maxSteps    int
	stepRetries int
}

// ChatAgentParams 聚合 NewChatAgent 所需的全部依赖。
// 用具名字段替代原先 10 个位置参数，让调用点自文档化。
type ChatAgentParams struct {
	Formatter   PromptFormatter
	Publisher   EventBus
	Env         Env
	Flow        FlowToolSet
	Thinker     Thinker
	DeepThinker DeepThinker
	MicroAgent  MicroAgentRunner
	Guard       *tools.WorkspaceGuard
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

// NewChatAgent 构造 ChatAgent：装配轻量工具集后返回。
func NewChatAgent(p ChatAgentParams) *ChatAgent {
	return &ChatAgent{
		BaseAgent: BaseAgent{
			LLM:       p.LLM,
			Publisher: p.Publisher,
		},
		promptFmt:   p.Formatter,
		params:      p,
		maxSteps:    p.MaxSteps,
		stepRetries: p.StepRetries,
	}
}

func (a *ChatAgent) Name() string {
	return "Chat-Agent"
}

func (a *ChatAgent) Run(ctx context.Context, input string) (AgentResult, error) {
	cfg := DefaultExecutorConfig()
	systemPrompt := a.promptFmt.FormatPrompt(chatPrompt)
	cfg.SystemPrompt = systemPrompt
	cfg.UserInput = input
	// 工具面由注册表按画像装配：Chat 的画像只给认知族 + 流程控制 + 便签回读，
	// 没有任何文件系统能力（历史上这是 buildChatAdapters 里那 5 个 case）。
	deps := depsFor("chat", a.params.Env, a.params.Guard, blackboard.BoardIDFrom(ctx), a.params.Blueprint, a.params.External)
	deps.Flow = a.params.Flow
	deps.Thinker = a.params.Thinker
	deps.Micro = a.params.MicroAgent
	deps.Deep = a.params.DeepThinker
	cfg.Surface = surfaceFor(toolspec.ProfileChat, deps, a.Name())
	cfg.SkillCatalog = skillCatalogFor("chat", a.params.External.Skills)
	cfg.LLM = a.LLM
	cfg.MaxSteps = a.maxSteps
	cfg.StepRetries = a.stepRetries
	cfg.Publisher = a.Publisher
	cfg.AgentName = a.Name()
	cfg.StopOnFinish = true
	// EnableCollaboration 已默认 true
	result, err := RunAgentLoop(ctx, cfg)
	if err != nil {
		return AgentResult{}, err
	}
	// 普通版：Text 无程序化消费点（delegate_chat 返回值由 Director 层
	// FormatForDirector 接管）。
	return FinalizeResult(a.Name(), input, result), nil
}
