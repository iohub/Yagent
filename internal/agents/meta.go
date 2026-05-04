package agents

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"

	"codeactor/internal/globalctx"
	"codeactor/internal/llm"
)

//go:embed meta.prompt.md
var metaPrompt string

// MetaAgent designs specialized agents on-the-fly using prompt engineering best practices.
// It is a pure designer — it makes a single LLM call (no tools) to produce an agent design
// JSON. The Conductor then registers and executes the designed agent.
type MetaAgent struct {
	BaseAgent
	GlobalCtx *globalctx.GlobalCtx
}

func NewMetaAgent(globalCtx *globalctx.GlobalCtx, llm llm.Engine) *MetaAgent {
	return &MetaAgent{
		BaseAgent: BaseAgent{
			LLM:       llm,
			Publisher: globalCtx.Publisher,
		},
		GlobalCtx: globalCtx,
	}
}

func (a *MetaAgent) Name() string {
	return "Meta-Agent"
}

// Run makes a single LLM call (no tools) to design a specialized agent.
// It returns the raw JSON design output from the LLM.
func (a *MetaAgent) Run(ctx context.Context, input string) (string, error) {
	systemPrompt := a.GlobalCtx.FormatPrompt(metaPrompt)

	messages := []llm.Message{
		{
			Role:    llm.RoleSystem,
			Content: systemPrompt,
		},
		{
			Role:    llm.RoleUser,
			Content: input,
		},
	}

	slog.Debug("MetaAgent calling LLM (design-only, no tools)", "input", input)
	resp, err := a.LLM.GenerateContent(ctx, messages, nil, nil)
	if err != nil {
		slog.Error("MetaAgent LLM error", "error", err)
		return "", fmt.Errorf("MetaAgent LLM call failed: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("MetaAgent returned empty response")
	}

	content := resp.Choices[0].Content
	if content == "" {
		return "", fmt.Errorf("MetaAgent returned empty content")
	}

	if a.Publisher != nil {
		a.Publisher.Publish("ai_response", content, a.Name())
	}

	return content, nil
}
