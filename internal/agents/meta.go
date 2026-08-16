package agents

import (
	"context"
	_ "embed"
	"fmt"
	"log/slog"
	"time"

	"codeactor/internal/globalctx"
	"codeactor/internal/llm"
)

//go:embed meta.prompt.md
var metaPrompt string

// MetaAgent designs specialized agents on-the-fly using prompt engineering best practices.
// It is a pure designer — it makes a single LLM call (no tools) to produce an agent design
// JSON. The Director then registers and executes the designed agent.
type MetaAgent struct {
	BaseAgent
	GlobalCtx   *globalctx.GlobalCtx
	StepRetries int // 步骤重试次数，0=不重试
}

func NewMetaAgent(globalCtx *globalctx.GlobalCtx, llm llm.Engine, stepRetries int) *MetaAgent {
	return &MetaAgent{
		BaseAgent: BaseAgent{
			LLM:       llm,
			Publisher: globalCtx.Publisher,
		},
		GlobalCtx:   globalCtx,
		StepRetries: stepRetries,
	}
}

func (a *MetaAgent) Name() string {
	return "Meta-Agent"
}

// Run makes a single LLM call (no tools) to design a specialized agent.
// It returns the raw JSON design output from the LLM.
func (a *MetaAgent) Run(ctx context.Context, input string) (AgentResult, error) {
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

	// Create opts with streaming handler for real-time output
	opts := &llm.CallOptions{}
	if a.Publisher != nil {
		opts.StreamHandler = func(ctx context.Context, chunk []byte) error {
			if len(chunk) > 0 {
				a.Publisher.Publish("ai_chunk", map[string]interface{}{
					"content": string(chunk),
					"agent":   a.Name(),
				}, a.Name())
			}
			return nil
		}
	}

	maxRetries := a.StepRetries
	var resp *llm.Response
	var err error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			// 指数退避：1s, 2s, 4s, ..., 上限30s
			wait := time.Duration(1<<(attempt-1)) * time.Second
			if wait > 30*time.Second {
				wait = 30 * time.Second
			}
			slog.Warn("MetaAgent retrying LLM call", "attempt", attempt, "maxRetries", maxRetries, "wait", wait)
			select {
			case <-ctx.Done():
				return AgentResult{}, fmt.Errorf("MetaAgent LLM call aborted: %w", ctx.Err())
			case <-time.After(wait):
			}
		}

		// Publish ai_stream_start before LLM call
		if a.Publisher != nil {
			a.Publisher.Publish("ai_stream_start", map[string]interface{}{
				"agent": a.Name(),
			}, a.Name())
		}

		resp, err = a.LLM.GenerateContent(ctx, messages, nil, opts)

		// Publish ai_stream_end after LLM call (regardless of error)
		if a.Publisher != nil {
			metadata := map[string]interface{}{
				"agent": a.Name(),
			}
			if err == nil && resp != nil && resp.Usage != nil {
				metadata["usage"] = map[string]interface{}{
					"prompt_tokens":               resp.Usage.PromptTokens,
					"completion_tokens":           resp.Usage.CompletionTokens,
					"total_tokens":                resp.Usage.TotalTokens,
					"cache_creation_input_tokens": resp.Usage.CacheCreationInputTokens,
					"cache_read_input_tokens":     resp.Usage.CacheReadInputTokens,
					"total_input_tokens":          resp.Usage.TotalInputTokens,
				}
			}
			a.Publisher.PublishWithMetadata("ai_stream_end", "", a.Name(), metadata)
		}

		if err == nil {
			break
		}
		slog.Warn("MetaAgent LLM error, will retry", "error", err, "attempt", attempt)
	}

	if err != nil {
		slog.Error("MetaAgent LLM error after all retries", "error", err, "maxRetries", maxRetries)
		return AgentResult{}, fmt.Errorf("MetaAgent LLM call failed after %d retries: %w", maxRetries, err)
	}

	if len(resp.Choices) == 0 {
		return AgentResult{}, fmt.Errorf("MetaAgent returned empty response")
	}

	content := resp.Choices[0].Content
	if content == "" {
		return AgentResult{}, fmt.Errorf("MetaAgent returned empty content")
	}

	if a.Publisher != nil {
		metadata := map[string]interface{}{}
		if resp.Usage != nil {
			metadata["usage"] = map[string]interface{}{
				"prompt_tokens":               resp.Usage.PromptTokens,
				"completion_tokens":           resp.Usage.CompletionTokens,
				"total_tokens":                resp.Usage.TotalTokens,
				"cache_creation_input_tokens": resp.Usage.CacheCreationInputTokens,
				"cache_read_input_tokens":     resp.Usage.CacheReadInputTokens,
				"total_input_tokens":          resp.Usage.TotalInputTokens,
			}
		} else {
			// Local models may not return usage — estimate from message content
			promptTokens := EstimateTokens(systemPrompt) + EstimateTokens(input)
			completionTokens := EstimateTokens(content)
			metadata["usage"] = map[string]interface{}{
				"prompt_tokens":     promptTokens,
				"completion_tokens": completionTokens,
				"total_tokens":      promptTokens + completionTokens,
				"estimated":         true,
			}
		}
		a.Publisher.PublishWithMetadata("ai_response", content, a.Name(), metadata)
	}

	// Build memory from the single-round conversation
	history := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: input},
		{Role: llm.RoleAssistant, Content: content},
	}

	return AgentResult{
		Text:   content,
		Memory: ConvertLLMHistoryToMemory(history),
	}, nil
}
