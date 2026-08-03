package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"codeactor/internal/config"
)

// ─── Anthropic Messages API Types ────────────────────────────────────────────

// anthropicRequest is the request body for Anthropic Messages API.
type anthropicRequest struct {
	Model       string            `json:"model"`
	MaxTokens   int               `json:"max_tokens"`
	Temperature float64           `json:"temperature,omitempty"`
	System      string            `json:"system,omitempty"`
	Messages    []anthropicMsg    `json:"messages"`
	Tools       []anthropicTool   `json:"tools,omitempty"`
	Thinking    *anthropicThinking `json:"thinking,omitempty"`
	Stream      bool              `json:"stream,omitempty"`
}

type anthropicMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type anthropicContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	Thinking  string          `json:"thinking,omitempty"`
	Data      string          `json:"data,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

type anthropicTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

// anthropicResponse is the response body from Anthropic Messages API.
type anthropicResponse struct {
	ID         string                  `json:"id"`
	Type       string                  `json:"type"`
	Role       string                  `json:"role"`
	Content    []anthropicContentBlock `json:"content"`
	Model      string                  `json:"model"`
	StopReason string                  `json:"stop_reason"`
	Usage      anthropicUsage          `json:"usage"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

type anthropicAPIError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type anthropicErrorBody struct {
	Type  string            `json:"type"`
	Error anthropicAPIError `json:"error"`
}

// ─── AnthropicEngine ─────────────────────────────────────────────────────────

// AnthropicEngine implements Engine using Anthropic's Messages API.
// It supports text generation, tool use, streaming, and extended thinking.
type AnthropicEngine struct {
	client          *http.Client
	transport       *http.Transport
	baseURL         string
	apiKey          string
	model           string
	maxTokens       int
	cfg             config.LLMConfig
	reasoningEffort string
}

// NewAnthropicEngine creates a new AnthropicEngine.
// baseURL is the Anthropic API base URL (e.g., "https://api.anthropic.com/v1").
// apiKey is the Anthropic API key.
// model is the model name (e.g., "claude-sonnet-4-20250514").
func NewAnthropicEngine(baseURL, apiKey, model string, cfg config.LLMConfig, reasoningEffort string) *AnthropicEngine {
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

	return &AnthropicEngine{
		client:          httpClient,
		transport:       transport,
		baseURL:         strings.TrimRight(baseURL, "/"),
		apiKey:          apiKey,
		model:           model,
		maxTokens:       8192, // default, overridden by CallOptions
		cfg:             cfg,
		reasoningEffort: reasoningEffort,
	}
}

// Model returns the model name.
func (e *AnthropicEngine) Model() string { return e.model }

// CloseIdleConnections closes idle HTTP connections.
func (e *AnthropicEngine) CloseIdleConnections() {
	if e.transport != nil {
		e.transport.CloseIdleConnections()
	}
}

// ─── Engine Interface ────────────────────────────────────────────────────────

// GenerateContent implements Engine.
func (e *AnthropicEngine) GenerateContent(ctx context.Context, messages []Message, tools []ToolDef, opts *CallOptions) (*Response, error) {
	if e.cfg.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, e.cfg.Timeout)
		defer cancel()
	}

	messages = NormalizeMessages(messages)

	req := e.buildRequestBody(messages, tools, opts)

	if opts != nil && opts.StreamHandler != nil {
		return e.streamGenerate(ctx, req, opts.StreamHandler)
	}

	return e.nonStreamGenerate(ctx, req)
}

// ─── Request Building ────────────────────────────────────────────────────────

func (e *AnthropicEngine) buildRequestBody(messages []Message, tools []ToolDef, opts *CallOptions) *anthropicRequest {
	var systemParts []string
	var anthropicMsgs []anthropicMsg

	for _, msg := range messages {
		switch msg.Role {
		case RoleSystem:
			systemParts = append(systemParts, msg.Content)

		case RoleUser:
			if msg.Content != "" {
				anthropicMsgs = append(anthropicMsgs, anthropicMsg{
					Role:    "user",
					Content: json.RawMessage(`"` + jsonEscape(msg.Content) + `"`),
				})
			}

		case RoleAssistant:
			var blocks []anthropicContentBlock
			if msg.Content != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "text", Text: msg.Content})
			}
			if msg.Reasoning != "" {
				blocks = append(blocks, anthropicContentBlock{Type: "thinking", Thinking: msg.Reasoning})
			}
			for _, tc := range msg.ToolCalls {
				var input map[string]any
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
					input = map[string]any{"raw": tc.Function.Arguments}
				}
				inputRaw, _ := json.Marshal(input)
				blocks = append(blocks, anthropicContentBlock{
					Type:  "tool_use",
					ID:    tc.ID,
					Name:  tc.Function.Name,
					Input: inputRaw,
				})
			}

			if len(blocks) == 0 {
				anthropicMsgs = append(anthropicMsgs, anthropicMsg{Role: "assistant", Content: jsonMustMarshal("")})
			} else if len(blocks) == 1 && blocks[0].Type == "text" {
				anthropicMsgs = append(anthropicMsgs, anthropicMsg{Role: "assistant", Content: jsonMustMarshal(blocks[0].Text)})
			} else {
				raw, _ := json.Marshal(blocks)
				anthropicMsgs = append(anthropicMsgs, anthropicMsg{Role: "assistant", Content: raw})
			}

		case RoleTool:
			block := anthropicContentBlock{
				Type:      "tool_result",
				ToolUseID: msg.ToolCallID,
			}
			if msg.Content != "" {
				block.Content = json.RawMessage(`"` + jsonEscape(msg.Content) + `"`)
			}
			blocks := []anthropicContentBlock{block}
			raw, _ := json.Marshal(blocks)
			anthropicMsgs = append(anthropicMsgs, anthropicMsg{Role: "user", Content: raw})
		}
	}

	req := &anthropicRequest{
		Model:     e.model,
		MaxTokens: e.maxTokens,
		Messages:  anthropicMsgs,
	}

	if len(systemParts) > 0 {
		req.System = strings.Join(systemParts, "\n\n")
	}

	// Apply call options
	if opts != nil {
		if opts.MaxTokens > 0 {
			req.MaxTokens = opts.MaxTokens
		}
		if opts.Temperature > 0 {
			req.Temperature = opts.Temperature
		}
	}

	// Convert tools
	if len(tools) > 0 {
		req.Tools = make([]anthropicTool, 0, len(tools))
		for _, t := range tools {
			if t.Type == "function" {
				inputSchema := t.Function.Parameters
				if inputSchema == nil {
					inputSchema = map[string]any{"type": "object"}
				}
				req.Tools = append(req.Tools, anthropicTool{
					Name:        t.Function.Name,
					Description: t.Function.Description,
					InputSchema: inputSchema,
				})
			}
		}
	}

	// Extended thinking (reasoning)
	effort := e.reasoningEffort
	if opts != nil && opts.ReasoningEffort != "" {
		effort = opts.ReasoningEffort
	}
	if effort != "" {
		budget := reasoningEffortToBudget(effort)
		if req.MaxTokens <= budget {
			req.MaxTokens = budget + 4096
		}
		req.Thinking = &anthropicThinking{
			Type:         "enabled",
			BudgetTokens: budget,
		}
		req.Temperature = 1.0 // required when thinking is enabled
	}

	return req
}

// ─── Helper Functions ────────────────────────────────────────────────────────

func reasoningEffortToBudget(effort string) int {
	switch strings.ToLower(effort) {
	case "low":
		return 1024
	case "medium":
		return 8192
	case "high":
		return 24576
	case "max":
		return 32000
	default:
		return 0
	}
}

// jsonMustMarshal returns a JSON string value (with quotes).
func jsonMustMarshal(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}

// jsonEscape escapes a string for use in JSON string context (removes surrounding quotes).
func jsonEscape(s string) string {
	b, _ := json.Marshal(s)
	if len(b) >= 2 {
		return string(b[1 : len(b)-1])
	}
	return s
}

// ─── HTTP Execution ──────────────────────────────────────────────────────────

func (e *AnthropicEngine) executeRequest(ctx context.Context, req *anthropicRequest) (*http.Response, error) {
	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic marshal: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", e.baseURL+"/messages", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("anthropic create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", e.apiKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	if req.Stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	}
	if req.Thinking != nil {
		httpReq.Header.Set("anthropic-beta", "interleaved-thinking-2025-05-14")
	}

	return e.client.Do(httpReq)
}

// ─── Non-Streaming ───────────────────────────────────────────────────────────

func (e *AnthropicEngine) nonStreamGenerate(ctx context.Context, req *anthropicRequest) (*Response, error) {
	req.Stream = false
	return e.retryRequest(ctx, req, false, nil)
}

// ─── Streaming ───────────────────────────────────────────────────────────────

func (e *AnthropicEngine) streamGenerate(ctx context.Context, req *anthropicRequest, handler StreamHandler) (*Response, error) {
	req.Stream = true
	return e.retryRequest(ctx, req, true, handler)
}

// ─── Retry Logic ─────────────────────────────────────────────────────────────

func (e *AnthropicEngine) retryRequest(ctx context.Context, req *anthropicRequest, isStream bool, handler StreamHandler) (*Response, error) {
	maxRetries := e.cfg.MaxRetries
	if maxRetries <= 0 {
		maxRetries = 5
	}
	baseDelay := 10 * time.Second

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("anthropic retry aborted: %w", ctx.Err())
		}

		if attempt > 0 {
			delay := baseDelay * (1 << (attempt - 1))
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, fmt.Errorf("anthropic retry aborted during backoff: %w", ctx.Err())
			case <-timer.C:
			}
		}

		httpResp, err := e.executeRequest(ctx, req)
		if err != nil {
			if !isHTTPRetriableError(err) {
				return nil, err
			}
			lastErr = err
			continue
		}

		if httpResp.StatusCode != 200 {
			errResp := e.parseErrorResponse(httpResp)
			httpResp.Body.Close()
			if !isRetriableHTTPStatus(httpResp.StatusCode) {
				return nil, errResp
			}
			lastErr = errResp
			continue
		}

		if isStream {
			resp, err := e.parseStreamResponse(ctx, httpResp.Body, handler)
			httpResp.Body.Close()
			if err != nil {
				if !isHTTPRetriableError(err) {
					return nil, err
				}
				lastErr = err
				continue
			}
			return resp, nil
		} else {
			resp, err := e.parseNonStreamResponse(httpResp.Body)
			httpResp.Body.Close()
			if err != nil {
				if !isHTTPRetriableError(err) {
					return nil, err
				}
				lastErr = err
				continue
			}
			return resp, nil
		}
	}

	return nil, fmt.Errorf("anthropic max retries (%d) exceeded: %w", maxRetries, lastErr)
}

// ─── Response Parsing ────────────────────────────────────────────────────────

func (e *AnthropicEngine) parseNonStreamResponse(body io.Reader) (*Response, error) {
	var ar anthropicResponse
	if err := json.NewDecoder(body).Decode(&ar); err != nil {
		return nil, fmt.Errorf("anthropic decode response: %w", err)
	}
	return e.convertResponse(&ar), nil
}

func (e *AnthropicEngine) convertResponse(ar *anthropicResponse) *Response {
	resp := &Response{
		Choices: []Choice{{}},
		Usage: &TokenUsage{
			PromptTokens:             int64(ar.Usage.InputTokens),
			CompletionTokens:         int64(ar.Usage.OutputTokens),
			TotalTokens:              int64(ar.Usage.InputTokens + ar.Usage.OutputTokens),
			CacheCreationInputTokens: int64(ar.Usage.CacheCreationInputTokens),
			CacheReadInputTokens:     int64(ar.Usage.CacheReadInputTokens),
			// Anthropic 的 input_tokens 不含 cache，TotalInputTokens = 三者之和
			TotalInputTokens: int64(ar.Usage.InputTokens + ar.Usage.CacheReadInputTokens + ar.Usage.CacheCreationInputTokens),
		},
	}

	choice := &resp.Choices[0]
	for _, block := range ar.Content {
		switch block.Type {
		case "text":
			choice.Content += block.Text
		case "tool_use":
			argsStr := string(block.Input)
			if argsStr == "" {
				argsStr = "{}"
			}
			choice.ToolCalls = append(choice.ToolCalls, ToolCall{
				ID:   block.ID,
				Type: "function",
				Function: FunctionCall{
					Name:      block.Name,
					Arguments: argsStr,
				},
			})
		case "thinking":
			choice.Reasoning += block.Thinking
		case "redacted_thinking":
			choice.Reasoning += "[redacted thinking]"
		}
	}

	return resp
}

// ─── SSE Stream Parsing ──────────────────────────────────────────────────────

func (e *AnthropicEngine) parseStreamResponse(ctx context.Context, body io.Reader, handler StreamHandler) (*Response, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 512*1024)

	// streamToolCall tracks partial tool call data during streaming
	type streamToolCall struct {
		id        string
		name      string
		arguments strings.Builder
	}

	var (
		contentBuilder   strings.Builder
		reasoningBuilder strings.Builder
		toolCallByIndex  = make(map[int]*streamToolCall)
		currentEvent     string
		currentData      strings.Builder
		finalUsage       *TokenUsage
	)

	for scanner.Scan() {
		line := scanner.Text()

		if strings.HasPrefix(line, "event: ") {
			currentEvent = strings.TrimPrefix(line, "event: ")
			currentData.Reset()
		} else if strings.HasPrefix(line, "data: ") {
			currentData.WriteString(strings.TrimPrefix(line, "data: "))
		} else if line == "" && currentEvent != "" {
			data := currentData.String()
			if data == "" {
				currentEvent = ""
				continue
			}

			switch currentEvent {
			case "ping":
				// keep-alive, ignore

			case "message_start":
				var evt struct {
					Message *anthropicResponse `json:"message"`
				}
				if json.Unmarshal([]byte(data), &evt) == nil && evt.Message != nil {
					if evt.Message.Usage.InputTokens > 0 {
						finalUsage = &TokenUsage{
							PromptTokens: int64(evt.Message.Usage.InputTokens),
						}
					}
				}

			case "content_block_start":
				var evt struct {
					Index        int                    `json:"index"`
					ContentBlock *anthropicContentBlock `json:"content_block"`
				}
				if json.Unmarshal([]byte(data), &evt) == nil && evt.ContentBlock != nil {
					idx := evt.Index
					cb := evt.ContentBlock
					switch cb.Type {
					case "text":
						contentBuilder.WriteString(cb.Text)
						if handler != nil && cb.Text != "" {
							handler(ctx, []byte(cb.Text))
						}
					case "thinking":
						reasoningBuilder.WriteString(cb.Thinking)
					case "tool_use":
						if _, ok := toolCallByIndex[idx]; !ok {
							toolCallByIndex[idx] = &streamToolCall{
								id:   cb.ID,
								name: cb.Name,
							}
						}
					}
				}

			case "content_block_delta":
				var evt struct {
					Index int             `json:"index"`
					Delta json.RawMessage `json:"delta"`
				}
				if json.Unmarshal([]byte(data), &evt) != nil {
					break
				}

				var deltaType struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(evt.Delta, &deltaType) != nil {
					break
				}

				switch deltaType.Type {
				case "text_delta":
					var d struct {
						Text string `json:"text"`
					}
					if json.Unmarshal(evt.Delta, &d) == nil {
						contentBuilder.WriteString(d.Text)
						if handler != nil && d.Text != "" {
							handler(ctx, []byte(d.Text))
						}
					}
				case "thinking_delta":
					var d struct {
						Thinking string `json:"thinking"`
					}
					if json.Unmarshal(evt.Delta, &d) == nil {
						reasoningBuilder.WriteString(d.Thinking)
					}
				case "input_json_delta":
					var d struct {
						PartialJSON string `json:"partial_json"`
					}
					if json.Unmarshal(evt.Delta, &d) == nil {
						idx := evt.Index
						if _, ok := toolCallByIndex[idx]; !ok {
							toolCallByIndex[idx] = &streamToolCall{}
						}
						toolCallByIndex[idx].arguments.WriteString(d.PartialJSON)
					}
				}

			case "message_delta":
				var evt struct {
					Delta struct {
						StopReason   string `json:"stop_reason"`
						StopSequence string `json:"stop_sequence"`
					} `json:"delta"`
					Usage *anthropicUsage `json:"usage"`
				}
				if json.Unmarshal([]byte(data), &evt) == nil && evt.Usage != nil {
					promptTokens := int64(0)
					if finalUsage != nil {
						promptTokens = finalUsage.PromptTokens
					}
					finalUsage = &TokenUsage{
						PromptTokens:     promptTokens,
						CompletionTokens: int64(evt.Usage.OutputTokens),
						TotalTokens:      promptTokens + int64(evt.Usage.OutputTokens),
					}
					if evt.Usage.CacheReadInputTokens > 0 {
						finalUsage.CacheReadInputTokens = int64(evt.Usage.CacheReadInputTokens)
					}
					if evt.Usage.CacheCreationInputTokens > 0 {
						finalUsage.CacheCreationInputTokens = int64(evt.Usage.CacheCreationInputTokens)
					}
					// Anthropic 的 input_tokens 不含 cache，TotalInputTokens = 三者之和
					finalUsage.TotalInputTokens = promptTokens + finalUsage.CacheReadInputTokens + finalUsage.CacheCreationInputTokens
				}

			case "content_block_stop", "message_stop":
				// no-op

			default:
				// unknown event, ignore
			}

			currentEvent = ""
			currentData.Reset()
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("anthropic stream scan: %w", err)
	}

	// Build tool calls from indexed map (ordered by index)
	var toolCalls []ToolCall
	for i := 0; ; i++ {
		tc, ok := toolCallByIndex[i]
		if !ok {
			break
		}
		args := tc.arguments.String()
		if args == "" {
			args = "{}"
		}
		toolCalls = append(toolCalls, ToolCall{
			ID:   tc.id,
			Type: "function",
			Function: FunctionCall{
				Name:      tc.name,
				Arguments: args,
			},
		})
	}

	if finalUsage == nil {
		finalUsage = &TokenUsage{}
	}

	return &Response{
		Choices: []Choice{{
			Content:   contentBuilder.String(),
			Reasoning: reasoningBuilder.String(),
			ToolCalls: toolCalls,
		}},
		Usage: finalUsage,
	}, nil
}

// ─── Error Handling ──────────────────────────────────────────────────────────

func (e *AnthropicEngine) parseErrorResponse(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	var errBody anthropicErrorBody
	if json.Unmarshal(body, &errBody) == nil && errBody.Error.Message != "" {
		return fmt.Errorf("anthropic API error (status %d): %s - %s", resp.StatusCode, errBody.Error.Type, errBody.Error.Message)
	}
	return fmt.Errorf("anthropic API error (status %d): %s", resp.StatusCode, string(body))
}

// isRetriableHTTPStatus returns true if the HTTP status code indicates a retriable error.
func isRetriableHTTPStatus(statusCode int) bool {
	return statusCode == 429 || (statusCode >= 500 && statusCode < 600)
}

// isHTTPRetriableError checks if the error is retriable (timeout, network, etc.)
func isHTTPRetriableError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}
