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

// OpenAIEngine implements Engine using the official OpenAI Go SDK.
type OpenAIEngine struct {
	client          *openai.Client
	transport       *http.Transport // 保持引用以便清理
	model           string
	cfg             config.LLMConfig
	reasoningEffort string // provider-level default reasoning effort
	// provider 级默认值。零值（MaxTokens<=0 / Temperature<=0）表示"不发这个字段"，
	// 让服务端用自己的默认 —— temperature=0 与"没写"无法区分，而显式发 0 会
	// 覆盖服务端默认（与 engine.go 的 CallOptions 同一套约定）。
	maxTokens   int
	temperature float64
}

// NewOpenAIEngine creates a new OpenAIEngine from a provider configuration.
// baseURL is optional - if empty, uses OpenAI's default API endpoint.
//
// 收整个 provider 而不是四个字段：引擎需要的东西只会往 ProviderConfig 里加，
// 拆成散参数时每加一个键就多一处"配置写了但没人读"的静默失效（max_tokens
// 与 temperature 曾经就是这样）。
func NewOpenAIEngine(provider *config.ProviderConfig, llmCfg config.LLMConfig) *OpenAIEngine {
	baseURL := provider.APIBaseURL
	apiKey := provider.APIKey
	// Custom HTTP Transport with reasonable timeout parameters
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout: 10 * time.Second,
		// No ResponseHeaderTimeout — controlled by context.WithTimeout
		ExpectContinueTimeout: 1 * time.Second,
		MaxIdleConns:          10,
		MaxIdleConnsPerHost:   5,
		IdleConnTimeout:       90 * time.Second,
		DisableKeepAlives:     false,
	}

	httpClient := &http.Client{
		Transport: transport,
		// No Timeout — controlled by context.WithTimeout in GenerateContent
	}

	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithHTTPClient(httpClient), // inject custom client
		option.WithMaxRetries(0),          // disable SDK internal retries (we have our own in retryChatCompletion)
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

// Model returns the model name this engine is configured to use.
func (e *OpenAIEngine) Model() string {
	return e.model
}

// CloseIdleConnections 关闭传输层的空闲连接。
// 在任务取消时调用，加速底层连接释放。
func (e *OpenAIEngine) CloseIdleConnections() {
	if e.transport != nil {
		e.transport.CloseIdleConnections()
	}
}

// GenerateContent implements Engine.
func (e *OpenAIEngine) GenerateContent(ctx context.Context, messages []Message, tools []ToolDef, opts *CallOptions) (*Response, error) {
	// 在飞闸门先于超时：等一个格子的时间不该算进"这次调用超时了"（inflight.go）。
	release, err := acquireInFlight(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	// Apply timeout if configured.
	// 取值优先级：全局运行时值（TUI 模型设置面板可动态修改）> 构造期局部配置。
	// 全局值未初始化时为 0，回落 e.cfg.Timeout，保证既有显式配置行为不变。
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

	// Resolve per-call values: CallOptions 覆盖 provider 默认，两者都零值时不发这个字段。
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
	// Request token usage (incl. cached_tokens) in the final stream chunk.
	// Use a copy so the non-streaming fallback keeps the original params
	// (stream_options is only valid for streaming requests).
	streamParams := params
	streamParams.StreamOptions = openai.ChatCompletionStreamOptionsParam{
		IncludeUsage: openai.Bool(true),
	}
	stream := e.client.Chat.Completions.NewStreaming(ctx, streamParams)
	defer stream.Close()

	var content string
	var reasoning string
	var toolCalls []ToolCall
	// finish 取最后一个非空 finish_reason：OpenAI 只在收尾的 chunk 上带它。
	var finish FinishReason

	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) > 0 {
			if fr := chunk.Choices[0].FinishReason; fr != "" {
				finish = normalizeFinishReason(fr)
			}
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
			return e.toResponse(completion)
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

		// OpenAI 的 prompt_tokens 已包含 cached_tokens，直接作为 TotalInputTokens
		usage.TotalInputTokens = acc.Usage.PromptTokens

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

	// 空串兜底放在收尾装配处，而不是上面每个分片的 += 处：在那里补 "{}" 会被后续分片
	// 当作前缀继续拼接，产出脏参数。与 engine_anthropic.go parseStreamResponse 的收尾
	// 装配对称。toolCalls 是本函数新建的切片，改写不涉及 history/rollout 的别名污染。
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
	// 什么都没产出的流不是"空回答"：循环会把空白 assistant 轮读成一次成功的收尾。
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
		// 只规范化发出去的这一份，绝不回写 tc.Function.Arguments / msg.ToolCalls：
		// Message 是值拷贝但 ToolCalls 共享底层数组，原地改会连 history 与 rollout
		// 那一份一起污染（AGENTS.md 硬约束 7）。
		// 空串与非法 JSON 在出口统一折成 "{}"：OpenAI 兼容网关收到 "arguments": ""
		// 直接 400，一次幻觉因此永久毒化上下文；resume 回放历史也走这里。
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

		// Extract tool calls
		for _, tc := range choice.Message.ToolCalls {
			fn := tc.AsFunction()
			if fn.ID != "" {
				// 空串兜底，与 engine_anthropic.go convertResponse 的 argsStr 分支对称：
				// provider 不发 arguments 字段时这里恒为空串，而 Agent 侧按 JSON 解析
				// 工具参数，空串让一次工具分发直接报错。只补空串 —— 非法 JSON 原样留着，
				// 截断/幻觉的诊断信息不在读取侧抹掉。
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

		// OpenAI 的 prompt_tokens 已包含 cached_tokens，直接作为 TotalInputTokens
		resp.Usage.TotalInputTokens = completion.Usage.PromptTokens

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

	if err := validateResponse(e.model, resp); err != nil {
		return nil, err
	}
	return resp, nil
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

	// 瞬时网络/传输错误（connection reset/refused、EOF、HTTP/2 GOAWAY 等）
	if isTransientNetworkError(err) {
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
		// Check context before each attempt
		if ctx.Err() != nil {
			return nil, fmt.Errorf("retry aborted: context cancelled before attempt %d: %w", attempt, ctx.Err())
		}

		// On retry attempts, wait with exponential backoff before making the request
		if attempt > 0 {
			delay := baseDelay * (1 << (attempt - 1)) // 2^(attempt-1) * 10s: 10, 20, 40, 80, 160

			// 钳制退避时间：不超过 ctx 剩余预算，避免最后一次重试被 sleep 吞掉
			if deadline, ok := ctx.Deadline(); ok {
				if remain := time.Until(deadline); remain < delay {
					delay = remain
				}
			}
			if delay <= 0 {
				// 预算已耗尽，交给循环顶部的 ctx 检查终止重试
				continue
			}

			// Backoff wait that can be interrupted by context cancellation
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

		// Check for context cancellation/deadline to stop retrying immediately
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, fmt.Errorf("llm call cancelled (attempt %d): %w", attempt, err)
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
