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

// NewChatAgent 构造 ChatAgent，仅声明其所需窄依赖：
// prompt 格式化、事件总线、环境视图（FullYoloMode）、流程控制、
// 认知/深度思考、微代理工具与工作区守护。
func NewChatAgent(formatter PromptFormatter, publisher EventBus, env Env, flow FlowToolSet, thinker Thinker, deepThinker DeepThinker, microAgent MicroAgentRunner, guard *tools.WorkspaceGuard, llm llm.Engine, maxSteps int) *ChatAgent {
	// Build a minimal tool set for ChatAgent: micro_agent for sub-LLM reasoning,
	// thinking for cognitive reflection, and agent_exit for clean termination.
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
			fn = microAgent.Execute
		case "thinking":
			fn = func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
				inputBytes, _ := json.Marshal(params)
				return thinker.Call(ctx, string(inputBytes))
			}
		case "agent_exit":
			fn = flow.ExecuteAgentExit
		case "deepthinking":
			fn = deepThinker.Execute
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

	return &ChatAgent{
		BaseAgent: BaseAgent{
			LLM:       llm,
			Publisher: publisher,
		},
		promptFmt: formatter,
		Adapters:  adapters,
		maxSteps:  maxSteps,
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
	return AgentResult{
		Text:   result.Text,
		Memory: ConvertLLMHistoryToMemory(result.History),
	}, nil
}
