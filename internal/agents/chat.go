package agents

import (
	"context"
	_ "embed"
	"encoding/json"

	"yagent/internal/globalctx"
	"yagent/internal/tools"

	"yagent/internal/llm"
)

//go:embed chat.prompt.md
var chatPrompt string

type ChatAgent struct {
	BaseAgent
	GlobalCtx *globalctx.GlobalCtx
	Adapters  []*tools.Adapter
	maxSteps  int
}

func NewChatAgent(globalCtx *globalctx.GlobalCtx, llm llm.Engine, maxSteps int) *ChatAgent {
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
	}
}

func (a *ChatAgent) Name() string {
	return "Chat-Agent"
}

func (a *ChatAgent) Run(ctx context.Context, input string) (AgentResult, error) {
	// P0 Step 5：迁移至 runSubAgentLoop（统一内核 director.Planner）。
	// 零值透传：LLMTimeout=0 → runSubAgentLoop 内兜底 5min（等价 executor.go:118）；
	// ToolTimeout=0 → 180s，与统一内核现状一致；StopOnFinish=true 保持现状；
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
	})
	if err != nil {
		return AgentResult{}, err
	}
	return AgentResult{
		Text:   outcome.Text,
		Memory: ConvertLLMHistoryToMemory(outcome.History),
	}, nil
}
