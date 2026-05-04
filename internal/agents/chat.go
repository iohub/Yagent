package agents

import (
	"context"
	_ "embed"
	"encoding/json"

	"codeactor/internal/tools"
	"codeactor/internal/globalctx"

	"codeactor/internal/llm"
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
	var toolDefs []ToolDefinition
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

func (a *ChatAgent) Run(ctx context.Context, input string) (string, error) {
	cfg := ExecutorConfig{
		SystemPrompt: a.GlobalCtx.FormatPrompt(chatPrompt),
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
