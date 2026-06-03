package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"codeactor/internal/config"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
)

// OpenAIEngine implements Engine using the official OpenAI Go SDK.
type OpenAIEngine struct {
	client          *openai.Client
	model           string
	cfg             config.LLMConfig
	reasoningEffort string // provider-level default reasoning effort
}

// NewOpenAIEngine creates a new OpenAIEngine.
// baseURL is optional - if empty, uses OpenAI's default API endpoint.
func NewOpenAIEngine(baseURL, apiKey, model string, cfg config.LLMConfig, reasoningEffort string) *OpenAIEngine {
	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	client := openai.NewClient(opts...)
	return &OpenAIEngine{client: &client, model: model, cfg: cfg, reasoningEffort: reasoningEffort}
}

// Model returns the model name this engine is configured to use.
func (e *OpenAIEngine) Model() string {
	return e.model
}

// GenerateContent implements Engine.
func (e *OpenAIEngine) GenerateContent(ctx context.Context, messages []Message, tools []ToolDef, opts *CallOptions) (*Response, error) {
	// Apply timeout if configured
	if e.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.cfg.Timeout)
		defer cancel()
	}

	params := e.buildParams(messages, tools, opts)

	if opts != nil && opts.StreamHandler != nil {
		return e.generateStreaming(ctx, params, opts.StreamHandler)
	}

	completion, err := e.retryChatCompletion(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("openai chat completion: %w", err)
	}

	return e.toResponse(completion), nil
}

func (e *OpenAIEngine) buildParams(messages []Message, tools []ToolDef, opts *CallOptions) openai.ChatCompletionNewParams {
	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(e.model),
		Messages: e.convertMessages(messages),
	}

	if len(tools) > 0 {
		params.Tools = e.convertTools(tools)
	}

	// Resolve reasoning effort: per-call override takes precedence over provider default
	effort := e.reasoningEffort
	if opts != nil {
		if opts.MaxTokens > 0 {
			params.MaxCompletionTokens = openai.Int(int64(opts.MaxTokens))
		}
		if opts.Temperature > 0 {
			params.Temperature = openai.Float(opts.Temperature)
		}
		if opts.ReasoningEffort != "" {
			effort = opts.ReasoningEffort
		}
	}

	if effort != "" {
		params.ReasoningEffort = shared.ReasoningEffort(effort)
		params.SetExtraFields(map[string]any{
			"thinking": map[string]string{"type": "enabled"},
		})
	}

	return params
}

func (e *OpenAIEngine) generateStreaming(ctx context.Context, params openai.ChatCompletionNewParams, handler StreamHandler) (*Response, error) {
	stream := e.client.Chat.Completions.NewStreaming(ctx, params)
	defer stream.Close()

	var content string
	var reasoning string
	var toolCalls []ToolCall

	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta
			content += delta.Content

			// Extract reasoning_content from raw JSON if present (DeepSeek thinking mode)
			if raw := delta.RawJSON(); raw != "" {
				var rawDelta map[string]any
				if err := json.Unmarshal([]byte(raw), &rawDelta); err == nil {
					if rc, ok := rawDelta["reasoning_content"].(string); ok {
						reasoning += rc
					}
				}
			}

			// Accumulate tool call deltas
			for _, tc := range delta.ToolCalls {
				idx := int(tc.Index)
				if idx >= len(toolCalls) {
					// Extend slice if needed
					for len(toolCalls) <= idx {
						toolCalls = append(toolCalls, ToolCall{Type: "function"})
					}
				}

				if tc.ID != "" {
					toolCalls[idx].ID = tc.ID
				}
				if tc.Function.Name != "" {
					toolCalls[idx].Function.Name = tc.Function.Name
				}
				toolCalls[idx].Function.Arguments += tc.Function.Arguments
			}

			// Call handler with content chunk
			if handler != nil && delta.Content != "" {
				if err := handler(ctx, []byte(delta.Content)); err != nil {
					return nil, err
				}
			}
		}
	}

	if err := stream.Err(); err != nil {
		if isRetriableError(err) {
			// 回退到非流式调用
			completion, retryErr := e.retryChatCompletion(ctx, params)
			if retryErr != nil {
				return nil, fmt.Errorf("openai streaming failed and non-streaming fallback also failed: %w", retryErr)
			}
			return e.toResponse(completion), nil
		}
		return nil, fmt.Errorf("openai streaming: %w", err)
	}

	// Extract real token usage from streaming accumulator
	var usage *TokenUsage
	if acc := stream.Current(); acc.Usage.PromptTokens > 0 || acc.Usage.CompletionTokens > 0 {
		usage = &TokenUsage{
			PromptTokens:     acc.Usage.PromptTokens,
			CompletionTokens: acc.Usage.CompletionTokens,
			TotalTokens:      acc.Usage.TotalTokens,
		}

		// Extract cache-related tokens from prompt_tokens_details.cached_tokens (OpenAI format)
		if acc.Usage.PromptTokensDetails.CachedTokens > 0 {
			usage.CacheReadInputTokens = acc.Usage.PromptTokensDetails.CachedTokens
		}

		// Also try to extract cache fields from raw JSON (for Anthropic-compatible APIs)
		if raw := acc.Usage.RawJSON(); raw != "" {
			var rawUsage map[string]any
			if err := json.Unmarshal([]byte(raw), &rawUsage); err == nil {
				if cacheRead, ok := rawUsage["cache_read_input_tokens"].(float64); ok && cacheRead > 0 {
					usage.CacheReadInputTokens = int64(cacheRead)
				}
				if cacheCreate, ok := rawUsage["cache_creation_input_tokens"].(float64); ok && cacheCreate > 0 {
					usage.CacheCreationInputTokens = int64(cacheCreate)
				}
			}
		}
	}

	return &Response{
		Choices: []Choice{{
			Content:   content,
			Reasoning: reasoning,
			ToolCalls: toolCalls,
		}},
		Usage: usage,
	}, nil
}

func (e *OpenAIEngine) convertMessages(messages []Message) []openai.ChatCompletionMessageParamUnion {
	result := make([]openai.ChatCompletionMessageParamUnion, 0, len(messages))
	for _, msg := range messages {
		result = append(result, e.convertMessage(msg))
	}
	return result
}

func (e *OpenAIEngine) convertMessage(msg Message) openai.ChatCompletionMessageParamUnion {
	switch msg.Role {
	case RoleSystem:
		return openai.SystemMessage(msg.Content)
	case RoleUser:
		return openai.UserMessage(msg.Content)
	case RoleAssistant:
		if len(msg.ToolCalls) > 0 {
			return e.buildAssistantWithToolCalls(msg)
		}
		content := msg.Content
		if msg.Reasoning != "" {
			// DeepSeek thinking mode: reasoning_content must be echoed back.
			// openai-go types don't expose reasoning_content, so inject via SetExtraFields.
			assistant := &openai.ChatCompletionAssistantMessageParam{
				Content: openai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: param.NewOpt(content),
				},
			}
			assistant.SetExtraFields(map[string]any{
				"reasoning_content": msg.Reasoning,
			})
			return openai.ChatCompletionMessageParamUnion{
				OfAssistant: assistant,
			}
		}
		return openai.AssistantMessage(content)
	case RoleTool:
		return openai.ToolMessage(msg.Content, msg.ToolCallID)
	default:
		return openai.UserMessage(msg.Content)
	}
}

func (e *OpenAIEngine) buildAssistantWithToolCalls(msg Message) openai.ChatCompletionMessageParamUnion {
	toolCalls := make([]openai.ChatCompletionMessageToolCallUnionParam, 0, len(msg.ToolCalls))
	for _, tc := range msg.ToolCalls {
		toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallUnionParam{
			OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
				ID:   tc.ID,
				Type: "function",
				Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			},
		})
	}

	contentVal := msg.Content
	assistant := &openai.ChatCompletionAssistantMessageParam{
		Content: openai.ChatCompletionAssistantMessageParamContentUnion{
			OfString: param.NewOpt(contentVal),
		},
		ToolCalls: toolCalls,
	}
	if msg.Reasoning != "" {
		assistant.SetExtraFields(map[string]any{
			"reasoning_content": msg.Reasoning,
		})
	}
	return openai.ChatCompletionMessageParamUnion{
		OfAssistant: assistant,
	}
}

func (e *OpenAIEngine) convertTools(tools []ToolDef) []openai.ChatCompletionToolUnionParam {
	result := make([]openai.ChatCompletionToolUnionParam, 0, len(tools))
	for _, t := range tools {
		if t.Type == "function" {
			result = append(result, openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
				Name:        t.Function.Name,
				Description: openai.String(t.Function.Description),
				Parameters:  shared.FunctionParameters(t.Function.Parameters),
			}))
		}
	}
	return result
}

func (e *OpenAIEngine) toResponse(completion *openai.ChatCompletion) *Response {
	resp := &Response{}
	for _, choice := range completion.Choices {
		c := Choice{
			Content: choice.Message.Content,
		}

		// Extract tool calls
		for _, tc := range choice.Message.ToolCalls {
			fn := tc.AsFunction()
			if fn.ID != "" {
				c.ToolCalls = append(c.ToolCalls, ToolCall{
					ID:   fn.ID,
					Type: "function",
					Function: FunctionCall{
						Name:      fn.Function.Name,
						Arguments: fn.Function.Arguments,
					},
				})
			}
		}

		// Extract reasoning_content from raw JSON if present
		if raw := choice.Message.RawJSON(); raw != "" {
			var rawMsg map[string]any
			if err := json.Unmarshal([]byte(raw), &rawMsg); err == nil {
				if rc, ok := rawMsg["reasoning_content"].(string); ok {
					c.Reasoning = rc
				}
			}
		}

		resp.Choices = append(resp.Choices, c)
	}

	// Extract real token usage from API response
	if completion.Usage.PromptTokens > 0 || completion.Usage.CompletionTokens > 0 {
		resp.Usage = &TokenUsage{
			PromptTokens:     completion.Usage.PromptTokens,
			CompletionTokens: completion.Usage.CompletionTokens,
			TotalTokens:      completion.Usage.TotalTokens,
		}

		// Extract cache-related tokens from prompt_tokens_details.cached_tokens (OpenAI format)
		if completion.Usage.PromptTokensDetails.CachedTokens > 0 {
			resp.Usage.CacheReadInputTokens = completion.Usage.PromptTokensDetails.CachedTokens
		}

		// Also try to extract cache fields from raw JSON (for Anthropic-compatible APIs)
		if raw := completion.Usage.RawJSON(); raw != "" {
			var rawUsage map[string]any
			if err := json.Unmarshal([]byte(raw), &rawUsage); err == nil {
				if cacheRead, ok := rawUsage["cache_read_input_tokens"].(float64); ok && cacheRead > 0 {
					resp.Usage.CacheReadInputTokens = int64(cacheRead)
				}
				if cacheCreate, ok := rawUsage["cache_creation_input_tokens"].(float64); ok && cacheCreate > 0 {
					resp.Usage.CacheCreationInputTokens = int64(cacheCreate)
				}
			}
		}
	}

	return resp
}

// isRetriableError checks if the error can be retried.
// Retriable errors include:
// - HTTP 429 Too Many Requests
// - HTTP 5xx Server Errors (500, 502, 503, 504)
// - Context deadline exceeded (timeout)
// - Network timeout errors
func isRetriableError(err error) bool {
	if err == nil {
		return false
	}

	// Check for openai.Error with status code
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		status := apiErr.StatusCode
		// 429 Too Many Requests - definitely retriable
		if status == 429 {
			return true
		}
		// 5xx Server Errors - retriable
		if status >= 500 && status < 600 {
			return true
		}
	}

	// Context deadline exceeded (timeout from context)
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	// Network timeout errors (e.g., from http.Client)
	if os.IsTimeout(err) {
		return true
	}

	// net.Error with Timeout() or Temporary() method (e.g., connection reset, DNS failure)
	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return true
	}

	return false
}

// retryChatCompletion wraps the chat completion API call with exponential backoff retry.
// It retries on 429 (rate limit), 5xx server errors, and timeout errors.
// The backoff sequence is: 10s, 20s, 40s, 80s, 160s (max 5 retries).
func (e *OpenAIEngine) retryChatCompletion(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	// Use configured max retries, default to 5
	maxRetries := e.cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 5
	}
	baseDelay := 10 * time.Second

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		// On retry attempts, wait with exponential backoff before making the request
		if attempt > 0 {
			delay := baseDelay * (1 << (attempt - 1)) // 2^(attempt-1) * 10s: 10, 20, 40, 80, 160
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, fmt.Errorf("retry aborted: context cancelled during backoff: %w", ctx.Err())
			case <-timer.C:
			}
		}

		completion, err := e.client.Chat.Completions.New(ctx, params)
		if err == nil {
			return completion, nil
		}

		if !isRetriableError(err) {
			return nil, err
		}

		lastErr = err
		// Log the retry (attempt number and error)
		// Note: we don't have a logger here, the error propagation will provide context
	}

	return nil, fmt.Errorf("max retries (%d) exceeded: %w", maxRetries, lastErr)
}
