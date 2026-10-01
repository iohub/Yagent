package agents

import (
	"context"
	_ "embed"
	"encoding/json"

	"yagent/internal/globalctx"
	"yagent/internal/tools"

	"yagent/internal/config"
	"yagent/internal/llm"
)

//go:embed chat.prompt.md
var chatPrompt string

type ChatAgent struct {
	BaseAgent
	GlobalCtx *globalctx.GlobalCtx
	Adapters  []*tools.Adapter
	maxSteps  int

	// timeouts 统一超时配置（P0 Step 10：构造时 Normalize，ToolTimeout 接线用）
	timeouts config.TimeoutsConfig
}

func NewChatAgent(globalCtx *globalctx.GlobalCtx, llm llm.Engine, maxSteps int, timeouts config.TimeoutsConfig) *ChatAgent {
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
			fn = globalCtx.MicroAgentTool.Execute
		case "thinking":
			fn = func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
				inputBytes, _ := json.Marshal(params)
				return globalCtx.ThinkingTool.Call(ctx, string(inputBytes))
			}
		case "agent_exit":
			fn = globalCtx.FlowOps.ExecuteAgentExit
		case "deepthinking":
			fn = globalCtx.DeepThinkingTool.Execute
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

	return &ChatAgent{
		BaseAgent: BaseAgent{
			LLM:       llm,
			Publisher: globalCtx.Publisher,
		},
		GlobalCtx: globalCtx,
		Adapters:  adapters,
		maxSteps:  maxSteps,
		timeouts:  timeouts.Normalize(), // P0 Step 10：零值回退统一默认值（幂等）
	}
}

func (a *ChatAgent) Name() string {
	return "Chat-Agent"
}

func (a *ChatAgent) Run(ctx context.Context, input string) (AgentResult, error) {
	// P0 Step 5：迁移至 runSubAgentLoop（统一内核 director.Planner）。
	// LLMTimeout 零值透传：→ runSubAgentLoop 内兜底 5min（等价 executor.go:118）；
	// P0 Step 10：ToolTimeout 接入统一配置 a.timeouts.SubAgentTool（构造时
	// Normalize 非零）；StopOnFinish=true 保持现状；
	// RolloutCollabMode="single" 由 runSubAgentLoop 内置（EnableCollaboration 等价）。
	systemPrompt := a.GlobalCtx.FormatPrompt(chatPrompt)
	outcome, err := runSubAgentLoop(ctx, SubAgentLoopConfig{
		SystemPrompt: systemPrompt,
		UserInput:    input,
		Adapters:     a.Adapters,
		LLM:          a.LLM,
		MaxSteps:     a.maxSteps,
		Publisher:    a.Publisher,
		AgentName:    a.Name(),
		StopOnFinish: true,
		ToolTimeout:  a.timeouts.SubAgentTool,
	})
	if err != nil {
		return AgentResult{}, err
	}
	return AgentResult{
		Text:   outcome.Text,
		Memory: ConvertLLMHistoryToMemory(outcome.History),
	}, nil
}
