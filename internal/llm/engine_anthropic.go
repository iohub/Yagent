package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"yagent/internal/config"
)

// ─── Anthropic Messages API Types ────────────────────────────────────────────

// anthropicRequest is the request body for Anthropic Messages API.
type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature float64            `json:"temperature,omitempty"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMsg     `json:"messages"`
	Tools       []anthropicTool    `json:"tools,omitempty"`
	Thinking    *anthropicThinking `json:"thinking,omitempty"`
	Stream      bool               `json:"stream,omitempty"`
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
	streamClient    *http.Client
	transport       *http.Transport
	streamTransport *http.Transport
	baseURL         string
	apiKey          string
	model           string
	maxTokens       int
	temperature     float64 // provider 默认；<=0 表示不发，让服务端用自己的默认
	cfg             config.LLMConfig
	reasoningEffort string
}

// defaultAnthropicMaxTokens 只在 provider 没写 max_tokens 时兜底。
const defaultAnthropicMaxTokens = 8192

// NewAnthropicEngine creates a new AnthropicEngine from a provider configuration.
// baseURL is the Anthropic API base URL (e.g., "https://api.anthropic.com/v1").
//
// 与 NewOpenAIEngine 同理：收整个 provider，配置里新增的键才不会"写了没人读"。
func NewAnthropicEngine(provider *config.ProviderConfig, llmCfg config.LLMConfig) *AnthropicEngine {
	plainTransport := &http.Transport{
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

	// 流式 / 非流式两个 transport（与 OpenAI 引擎同理由，见 engine_openai.go）：
	// 只有流式那份设 ResponseHeaderTimeout —— 非流式的响应头要等整轮生成完才写，
	// 同一个字段在那边等于"整轮生成上限"。
	streamTransport := plainTransport.Clone()
	streamTransport.ResponseHeaderTimeout = resolveFirstByteTimeout(llmCfg)

	// 刻意**不设** http.Client.Timeout：它覆盖"发请求到读完响应体"的总时长，
	// 会把正常的长流（thinking 模型连续吐 token 几分钟）就地杀掉。分层见
	// stream_watchdog.go 的注释。

	maxTokens := provider.MaxTokens
	if maxTokens <= 0 {
		maxTokens = defaultAnthropicMaxTokens
	}

	return &AnthropicEngine{
		client:          &http.Client{Transport: plainTransport},
		streamClient:    &http.Client{Transport: streamTransport},
		transport:       plainTransport,
		streamTransport: streamTransport,
		baseURL:         strings.TrimRight(provider.APIBaseURL, "/"),
		apiKey:          provider.APIKey,
		model:           provider.Model,
		maxTokens:       maxTokens,
		temperature:     provider.Temperature,
		cfg:             llmCfg,
		reasoningEffort: provider.ReasoningEffort,
	}
}

// Model returns the model name.
func (e *AnthropicEngine) Model() string { return e.model }

// CloseIdleConnections closes idle HTTP connections.
func (e *AnthropicEngine) CloseIdleConnections() {
	if e.transport != nil {
		e.transport.CloseIdleConnections()
	}
	if e.streamTransport != nil {
		e.streamTransport.CloseIdleConnections()
	}
}

// ─── Engine Interface ────────────────────────────────────────────────────────

// GenerateContent implements Engine.
func (e *AnthropicEngine) GenerateContent(ctx context.Context, messages []Message, tools []ToolDef, opts *CallOptions) (*Response, error) {
	// 与 OpenAI 引擎共用同一个进程级闸门：429 记在账号上，不记在某个引擎指针上。
	// 等格子的时间在超时之前（inflight.go）。
	release, err := acquireInFlight(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	// Apply timeout if configured.
	// 取值优先级：全局运行时值（TUI 模型设置面板可动态修改）> 构造期局部配置。
	// 全局值未初始化时为 0，回落 e.cfg.Timeout，保证既有显式配置行为不变。
	// 只在 ctx 还没有 deadline 时才套这一层：调用方（deepthinking 的
	// [tools.llm.deepthinking].timeout 等）给的预算必须原样生效，二次收紧会把长任务砍断。
	timeout := e.cfg.Timeout
	if t := config.GlobalLLMTimeout(); t > 0 {
		timeout = t
	}
	ctx, cancel := ApplyCallTimeout(ctx, timeout)
	defer cancel()

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
				// 只读 tc，绝不回写 msg.ToolCalls 的元素（Message 是值拷贝、ToolCalls
				// 共享底层数组，原地改会同时污染 history/rollout 那一份）。
				var input map[string]any
				if err := json.Unmarshal([]byte(tc.Function.Arguments), &input); err != nil {
					input = map[string]any{"raw": tc.Function.Arguments}
				} else if !UsableToolArguments(tc.Function.Arguments) {
					// 解析成功却仍"不可用"的只有字面量 null：它留下 nil map，原样 marshal
					// 就是 "input": null，而 tool_use.input 必须是对象 —— 一个空参数因此
					// 让整次请求 400。判据与 UsableToolArguments 同形（AGENTS.md 硬约束 13）。
					input = map[string]any{}
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

	// Apply call options：provider 默认在前，单次调用覆盖在后。
	if e.temperature > 0 {
		req.Temperature = e.temperature
	}
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
	// budget==0 表示 effort 认不出来。这时不能发出 {type:enabled, budget_tokens:0}：
	// provider 要求 budget_tokens ≥1024，一个拼错的 effort 会让此后每次调用都 400；
	// 也不该连带把 temperature 钉成 1.0。
	if budget := reasoningEffortToBudget(effort); budget > 0 {
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

	// 流式走 streamClient：只有它的 transport 带 ResponseHeaderTimeout（首字节兜底）。
	client := e.client
	if req.Stream && e.streamClient != nil {
		client = e.streamClient
	}
	return client.Do(httpReq)
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
			// 提示放在退避**之前**（理由同 engine_openai.go 的 retryChatCompletion）：
			// 退避从 10s 起跳，那段时间用户没有任何别的信号，静默的重试在 UI 上
			// 与卡死无法区分。
			notifyStream(ctx, handler, retryNoticeText(attempt, maxRetries))

			delay := baseDelay * (1 << (attempt - 1))

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

			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, fmt.Errorf("anthropic retry aborted during backoff: %w", ctx.Err())
			case <-timer.C:
			}
		}

		resp, retriable, err := e.attempt(ctx, req, isStream, handler)
		if err == nil {
			return resp, nil
		}
		if !retriable {
			return nil, err
		}
		lastErr = err
	}

	return nil, fmt.Errorf("anthropic max retries (%d) exceeded: %w", maxRetries, lastErr)
}

// attempt 执行一次完整尝试（发请求 + 解析响应），并套上单次尝试上限（外层兜底）。
//
// retriable 仍由既有的两套判据决定（isHTTPRetriableError / isRetriableHTTPStatus），
// 重试语义没有改动 —— 这里只是把"一次尝试"从循环里提出来，好让单次上限有一个
// 明确的作用域（total budget 仍在 ctx 上，二者是"外层兜底 / 总闸"的关系）。
//
// 流内空闲看门狗判死（stream stalled）返回的是不可重试错误：它已经等满一整个
// idle 窗口，重试只会把同样的等待再来一遍；上层看到的是一个可识别的哨兵，
// 而不是一句 context canceled。
func (e *AnthropicEngine) attempt(ctx context.Context, req *anthropicRequest, isStream bool, handler StreamHandler) (*Response, bool, error) {
	attemptCtx, cancelAttempt := withAttemptTimeout(ctx, e.cfg.RetryAttemptTimeout)
	defer cancelAttempt()

	// 流内空闲看门狗必须建在**发请求之前**：它 cancel 的是本次尝试的 ctx，而被
	// 阻塞的正是那次请求 body 的读（cancel 一个子 ctx 不会波及父 ctx，所以看门狗
	// 的 ctx 必须就是 executeRequest 收到的那一个）。非流式没有"流内静默"这回事，
	// 也没有续期点，不挂看门狗。
	var wd *idleWatchdog
	if isStream {
		attemptCtx, wd = newIdleWatchdog(attemptCtx, resolveStreamIdleTimeout(e.cfg))
		defer wd.release()
	}

	httpResp, err := e.executeRequest(attemptCtx, req)
	if err != nil {
		return nil, isHTTPRetriableError(err), err
	}

	if httpResp.StatusCode != 200 {
		errResp := e.parseErrorResponse(httpResp)
		httpResp.Body.Close()
		return nil, isRetriableHTTPStatus(httpResp.StatusCode), errResp
	}

	var resp *Response
	if isStream {
		resp, err = e.parseStreamResponse(attemptCtx, httpResp.Body, handler, wd)
	} else {
		resp, err = e.parseNonStreamResponse(httpResp.Body)
	}
	httpResp.Body.Close()
	if err != nil {
		return nil, isHTTPRetriableError(err), err
	}
	return resp, false, nil
}

// ─── Response Parsing ────────────────────────────────────────────────────────

func (e *AnthropicEngine) parseNonStreamResponse(body io.Reader) (*Response, error) {
	var ar anthropicResponse
	if err := json.NewDecoder(body).Decode(&ar); err != nil {
		return nil, fmt.Errorf("anthropic decode response: %w", err)
	}
	return e.convertResponse(&ar)
}

func (e *AnthropicEngine) convertResponse(ar *anthropicResponse) (*Response, error) {
	if len(ar.Content) == 0 {
		return nil, fmt.Errorf("%w: anthropic sent no content blocks (stop_reason=%q)",
			ErrEmptyCompletion, ar.StopReason)
	}

	resp := &Response{
		Choices: []Choice{{FinishReason: normalizeFinishReason(ar.StopReason)}},
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

	if err := validateResponse(e.model, resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ─── SSE Stream Parsing ──────────────────────────────────────────────────────

// wd 是本次尝试的空闲看门狗（可为 nil，nil 上所有方法都是空操作）：每读到一行
// SSE 就续期一次 —— 看门狗只关心"有没有字节"，ping、空行、注释行都算活着。
func (e *AnthropicEngine) parseStreamResponse(ctx context.Context, body io.Reader, handler StreamHandler, wd *idleWatchdog) (*Response, error) {
	scanner := bufio.NewScanner(body)
	// 1MB 上限（原来是 512KB）：SSE 的单行可能非常长 —— 一次 input_json_delta 里
	// 塞进整段工具参数、或网关把整轮内容压成一行，都会触发
	// `bufio.Scanner: token too long`，而那个错误的表象只是"流莫名其妙断了"。
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

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
		// stop_reason 只出现在 message_delta 上（message_start 那份是 null）。
		finish FinishReason
	)

	for scanner.Scan() {
		line := scanner.Text()
		// 任意一行到账都算链路活着（包括 event:/data:/空行/ping 事件）：
		// 看门狗判的是"有没有字节到"，不是"有没有正文到"。慢思考阶段
		// provider 通常只发 ping，那正是它该被续期的时候。
		wd.touch()

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
				if json.Unmarshal([]byte(data), &evt) == nil {
					if evt.Delta.StopReason != "" {
						finish = normalizeFinishReason(evt.Delta.StopReason)
					}
					if evt.Usage != nil {
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
		// 看门狗判死优先：取消子 ctx 会让这个读返回，此时 scanner.Err() 报的是
		// "我们自己的取消"，直接透出就是一句 context canceled —— 真正原因（静默）
		// 就彻底看不见了。
		if stalled := wd.err(); stalled != nil {
			return nil, stalled
		}
		return nil, fmt.Errorf("anthropic stream scan: %w", err)
	}

	// Build tool calls ordered by block index. 不能用"从 0 连续递增"来遍历：
	// toolCallByIndex 只收 tool_use，而 Anthropic 的块索引里混着 text/thinking，
	// 工具块通常从 1 或 2 开始 —— 连续性遍历会在 index 0 就 break，把工具整批丢掉
	// （表现是模型明确调了工具，Agent 却收到空 tool_calls）。
	indexes := make([]int, 0, len(toolCallByIndex))
	for i := range toolCallByIndex {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)

	var toolCalls []ToolCall
	for _, i := range indexes {
		tc := toolCallByIndex[i]
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

	resp := &Response{
		Choices: []Choice{{
			Content:      contentBuilder.String(),
			Reasoning:    reasoningBuilder.String(),
			ToolCalls:    toolCalls,
			FinishReason: finish,
		}},
		Usage: finalUsage,
	}
	// 什么都没产出的流不是"空回答"：循环会把空白 assistant 轮读成一次成功的收尾。
	choice := resp.Choices[0]
	if choice.Content == "" && choice.Reasoning == "" && len(choice.ToolCalls) == 0 {
		return nil, fmt.Errorf("%w: anthropic stream ended with stop_reason=%q", ErrEmptyCompletion, finish)
	}
	if err := validateResponse(e.model, resp); err != nil {
		return nil, err
	}
	return resp, nil
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
// 复用统一的瞬时网络错误分类（connection reset/refused、EOF、GOAWAY 等）。
func isHTTPRetriableError(err error) bool {
	return isTransientNetworkError(err)
}
