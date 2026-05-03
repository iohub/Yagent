package tools

import (
	"context"
	"fmt"

	"github.com/tmc/langchaingo/llms"
)

// MicroAgentTool enables agents to make raw LLM inference calls as a tool.
// ChatAgent and CodingAgent use this when they need a sub-LLM reasoning step
// with a custom system prompt, independent of the current conversation context.
type MicroAgentTool struct {
	LLM llms.LLM
}

// NewMicroAgentTool creates a new MicroAgentTool with the given LLM client.
func NewMicroAgentTool(llm llms.LLM) *MicroAgentTool {
	return &MicroAgentTool{LLM: llm}
}

// Execute makes a raw LLM call with the provided system prompt and task.
// It returns the model's raw text output.
func (t *MicroAgentTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	systemPrompt, ok := params["system_prompt"].(string)
	if !ok || systemPrompt == "" {
		return nil, fmt.Errorf("system_prompt parameter is required and must be a non-empty string")
	}

	task, ok := params["task"].(string)
	if !ok || task == "" {
		return nil, fmt.Errorf("task parameter is required and must be a non-empty string")
	}

	messages := []llms.MessageContent{
		{
			Role:  llms.ChatMessageTypeSystem,
			Parts: []llms.ContentPart{llms.TextPart(systemPrompt)},
		},
		{
			Role:  llms.ChatMessageTypeHuman,
			Parts: []llms.ContentPart{llms.TextPart(task)},
		},
	}

	resp, err := t.LLM.GenerateContent(ctx, messages)
	if err != nil {
		return nil, fmt.Errorf("micro_agent LLM call failed: %w", err)
	}

	if len(resp.Choices) > 0 {
		return resp.Choices[0].Content, nil
	}
	return "", nil
}
