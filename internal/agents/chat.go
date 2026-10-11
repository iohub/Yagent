package agents

import (
	"context"
	_ "embed"
	"encoding/json"

	"yagent/internal/tools"

	"yagent/internal/llm"
)

//go:embed chat.prompt.md
var chatPrompt string

type ChatAgent struct {
	BaseAgent
	promptFmt PromptFormatter
	Adapters  []*tools.Adapter
	maxSteps  int
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
}

// NewChatAgent 构造 ChatAgent：装配轻量工具集后返回。
func NewChatAgent(p ChatAgentParams) *ChatAgent {
	return &ChatAgent{
		BaseAgent: BaseAgent{
			LLM:       p.LLM,
			Publisher: p.Publisher,
		},
		promptFmt: p.Formatter,
		Adapters:  buildChatAdapters(p),
		maxSteps:  p.MaxSteps,
	}
}

// buildChatAdapters 装配 Chat-Agent 的最小工具集：
// micro_agent（子 LLM 推理）、thinking（认知反思）、agent_exit（退出）、deepthinking。
func buildChatAdapters(p ChatAgentParams) []*tools.Adapter {
	var toolDefs []tools.ToolDefinition
	if err := json.Unmarshal(ToolsJSON, &toolDefs); err != nil {
		// Errors parsing tools.json are logged but non-fatal —
		// ChatAgent falls back to no-tool mode.
	}

	adapters := make([]*tools.Adapter, 0, len(toolDefs))
	for _, def := range toolDefs {
		var fn tools.ToolFunc
		switch def.Name {
		case "micro_agent":
			fn = p.MicroAgent.Execute
		case "thinking":
			fn = func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
				inputBytes, _ := json.Marshal(params)
				return p.Thinker.Call(ctx, string(inputBytes))
			}
		case "agent_exit":
			fn = p.Flow.ExecuteAgentExit
		case "deepthinking":
			fn = p.DeepThinker.Execute
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

func (a *ChatAgent) Name() string {
	return "Chat-Agent"
}

func (a *ChatAgent) Run(ctx context.Context, input string) (AgentResult, error) {
	cfg := DefaultExecutorConfig()
	systemPrompt := a.promptFmt.FormatPrompt(chatPrompt)
	cfg.SystemPrompt = systemPrompt
	cfg.UserInput = input
	cfg.Adapters = a.Adapters
	cfg.LLM = a.LLM
	cfg.MaxSteps = a.maxSteps
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
