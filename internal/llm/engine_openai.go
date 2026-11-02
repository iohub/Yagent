package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"yagent/internal/config"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"
)

type OpenAIEngine struct {
	client          *openai.Client
	transport       *http.Transport
	model           string
	cfg             config.LLMConfig
	reasoningEffort string
	maxTokens       int
	temperature     float64
}

func NewOpenAIEngine(provider *config.ProviderConfig, llmCfg config.LLMConfig) *OpenAIEngine {
	baseURL := provider.APIBaseURL
	apiKey := provider.APIKey
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   5,
		IdleConnTimeout:       90 * time.Second,
		DisableKeepAlives:     false,
	}

	httpClient := &http.Client{
		Transport: transport,
	}

	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithHTTPClient(httpClient),
		option.WithMaxRetries(0),
	}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	client := openai.NewClient(opts...)

	return &OpenAIEngine{
		client:          &client,
		transport:       transport,
		model:           provider.Model,
		cfg:             llmCfg,
		reasoningEffort: provider.ReasoningEffort,
		maxTokens:       provider.MaxTokens,
		temperature:     provider.Temperature,
	}
}

func (e *OpenAIEngine) Model() string {
	return e.model
}

func (e *OpenAIEngine) CloseIdleConnections() {
	if e.transport != nil {
		e.transport.CloseIdleConnections()
	}
}

func (e *OpenAIEngine) GenerateContent(ctx context.Context, messages []Message, tools []ToolDef, opts *CallOptions) (*Response, error) {
	release, err := acquireInFlight(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	timeout := e.cfg.Timeout
	if t := config.GlobalLLMTimeout(); t > 0 {
		timeout = t
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	messages = NormalizeMessages(messages)

	params := e.buildParams(messages, tools, opts)

	if opts != nil && opts.StreamHandler != nil {
		return e.generateStreaming(ctx, params, opts.StreamHandler)
	}

	completion, err := e.retryChatCompletion(ctx, params)
	if err != nil {
		return nil, fmt.Errorf("openai chat completion: %w", err)
	}

	return e.toResponse(completion)
}

func (e *OpenAIEngine) buildParams(messages []Message, tools []ToolDef, opts *CallOptions) openai.ChatCompletionNewParams {
	params := openai.ChatCompletionNewParams{
		Model:    shared.ChatModel(e.model),
		Messages: e.convertMessages(messages),
	}

	if len(tools) > 0 {
		params.Tools = e.convertTools(tools)
	}

	effort := e.reasoningEffort
	maxTokens := e.maxTokens
	temperature := e.temperature
	if opts != nil {
		if opts.MaxTokens > 0 {
			maxTokens = opts.MaxTokens
		}
		if opts.Temperature > 0 {
			temperature = opts.Temperature
		}
		if opts.ReasoningEffort != "" {
			effort = opts.ReasoningEffort
		}
	}

	if maxTokens > 0 {
		params.MaxCompletionTokens = openai.Int(int64(maxTokens))
	}
	if temperature > 0 {
		params.Temperature = openai.Float(temperature)
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
	streamParams := params
	streamParams.StreamOptions = openai.ChatCompletionStreamOptionsParam{
		IncludeUsage: openai.Bool(true),
	}
	stream := e.client.Chat.Completions.NewStreaming(ctx, streamParams)
	defer stream.Close()

	var content string
	var reasoning string
	var toolCalls []ToolCall
	var finish FinishReason

	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) > 0 {
			if fr := chunk.Choices[0].FinishReason; fr != "" {
				finish = normalizeFinishReason(fr)
			}
			delta := chunk.Choices[0].Delta
			content += delta.Content

			if raw := delta.RawJSON(); raw != "" {
				var rawDelta map[string]any
				if err := json.Unmarshal([]byte(raw), &rawDelta); err == nil {
					if rc, ok := rawDelta["reasoning_content"].(string); ok {
						reasoning += rc
					}
				}
			}

			for _, tc := range delta.ToolCalls {
				idx := int(tc.Index)
				if idx >= len(toolCalls) {
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

			if handler != nil && delta.Content != "" {
				if err := handler(ctx, []byte(delta.Content)); err != nil {
					return nil, err
				}
			}
		}
	}

	if err := stream.Err(); err != nil {
		if isRetriableError(err) {
			completion, retryErr := e.retryChatCompletion(ctx, params)
			if retryErr != nil {
				return nil, fmt.Errorf("openai streaming failed and non-streaming fallback also failed: %w", retryErr)
			}
			return e.toResponse(completion)
		}
		return nil, fmt.Errorf("openai streaming: %w", err)
	}

	var usage *TokenUsage
	if acc := stream.Current(); acc.Usage.PromptTokens > 0 || acc.Usage.CompletionTokens > 0 {
		usage = &TokenUsage{
			PromptTokens:     acc.Usage.PromptTokens,
			CompletionTokens: acc.Usage.CompletionTokens,
			TotalTokens:      acc.Usage.TotalTokens,
		}

		usage.TotalInputTokens = acc.Usage.PromptTokens

		if acc.Usage.PromptTokensDetails.CachedTokens > 0 {
			usage.CacheReadInputTokens = acc.Usage.PromptTokensDetails.CachedTokens
		}

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

	for i := range toolCalls {
		if toolCalls[i].Function.Arguments == "" {
			toolCalls[i].Function.Arguments = "{}"
		}
	}

	resp := &Response{
		Choices: []Choice{{
			Content:      content,
			Reasoning:    reasoning,
			ToolCalls:    toolCalls,
			FinishReason: finish,
		}},
		Usage: usage,
	}
	if content == "" && reasoning == "" && len(toolCalls) == 0 {
		return nil, fmt.Errorf("%w: openai stream ended with finish_reason=%q", ErrEmptyCompletion, string(finish))
	}
	if err := validateResponse(e.model, resp); err != nil {
		return nil, err
	}
	return resp, nil
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
		args := tc.Function.Arguments
		if !UsableToolArguments(args) {
			args = "{}"
		}
		toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallUnionParam{
			OfFunction: &openai.ChatCompletionMessageFunctionToolCallParam{
				ID:   tc.ID,
				Type: "function",
				Function: openai.ChatCompletionMessageFunctionToolCallFunctionParam{
					Name:      tc.Function.Name,
					Arguments: args,
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

func (e *OpenAIEngine) toResponse(completion *openai.ChatCompletion) (*Response, error) {
	resp := &Response{}
	for _, choice := range completion.Choices {
		c := Choice{
			Content:      choice.Message.Content,
			FinishReason: normalizeFinishReason(choice.FinishReason),
		}

		for _, tc := range choice.Message.ToolCalls {
			fn := tc.AsFunction()
			if fn.ID != "" {
				args := fn.Function.Arguments
				if args == "" {
					args = "{}"
				}
				c.ToolCalls = append(c.ToolCalls, ToolCall{
					ID:   fn.ID,
					Type: "function",
					Function: FunctionCall{
						Name:      fn.Function.Name,
						Arguments: args,
					},
				})
			}
		}

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

	if completion.Usage.PromptTokens > 0 || completion.Usage.CompletionTokens > 0 {
		resp.Usage = &TokenUsage{
			PromptTokens:     completion.Usage.PromptTokens,
			CompletionTokens: completion.Usage.CompletionTokens,
			TotalTokens:      completion.Usage.TotalTokens,
		}

		resp.Usage.TotalInputTokens = completion.Usage.PromptTokens

		if completion.Usage.PromptTokensDetails.CachedTokens > 0 {
			resp.Usage.CacheReadInputTokens = completion.Usage.PromptTokensDetails.CachedTokens
		}

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

	if err := validateResponse(e.model, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

func isRetriableError(err error) bool {
	if err == nil {
		return false
	}

	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		status := apiErr.StatusCode
		if status == 429 {
			return true
		}
		if status >= 500 && status < 600 {
			return true
		}
	}

	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}

	if os.IsTimeout(err) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) && (netErr.Timeout() || netErr.Temporary()) {
		return true
	}

	if isTransientNetworkError(err) {
		return true
	}

	return false
}

func (e *OpenAIEngine) retryChatCompletion(ctx context.Context, params openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	maxRetries := e.cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 5
	}
	baseDelay := 10 * time.Second

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("retry aborted: context cancelled before attempt %d: %w", attempt, ctx.Err())
		}

		if attempt > 0 {
			delay := baseDelay * (1 << (attempt - 1))

			if deadline, ok := ctx.Deadline(); ok {
				if remain := time.Until(deadline); remain < delay {
					delay = remain
				}
			}
			if delay <= 0 {
				continue
			}

			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, fmt.Errorf("retry aborted: context cancelled during backoff (attempt %d): %w", attempt, ctx.Err())
			case <-timer.C:
			}
		}

		completion, err := e.client.Chat.Completions.New(ctx, params)
		if err == nil {
			return completion, nil
		}

		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("llm call cancelled (attempt %d): %w", attempt, err)
		}

		if !isRetriableError(err) {
			return nil, err
		}

		lastErr = err
	}

	return nil, fmt.Errorf("max retries (%d) exceeded: %w", maxRetries, lastErr)
}
