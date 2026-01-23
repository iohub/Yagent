package agents

import (
	"context"
	_ "embed"

	"codeactor/internal/globalctx"

	"github.com/tmc/langchaingo/llms"
)

//go:embed chat.prompt.md
var chatPrompt string

type ChatAgent struct {
	BaseAgent
	GlobalCtx *globalctx.GlobalCtx
}

func NewChatAgent(globalCtx *globalctx.GlobalCtx, llm llms.LLM) *ChatAgent {
	return &ChatAgent{
		BaseAgent: BaseAgent{
			LLM:       llm,
			Publisher: globalCtx.Publisher,
		},
		GlobalCtx: globalCtx,
	}
}

func (a *ChatAgent) Name() string {
	return "Chat-Agent"
}

func (a *ChatAgent) Run(ctx context.Context, input string) (string, error) {
	messages := []llms.MessageContent{
		{
			Role:  llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{llms.TextPart(a.GlobalCtx.FormatPrompt(chatPrompt))},
		},
		{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart(input)},
		},
	}

	resp, err := a.LLM.GenerateContent(ctx, messages, llms.WithTemperature(0.7))
	if err != nil {
		return "", err
	}

	if len(resp.Choices) > 0 {
		return resp.Choices[0].Content, nil
	}
	return "", nil
}
