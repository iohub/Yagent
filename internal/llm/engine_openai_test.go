package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"yagent/internal/config"
)

// 协议层写错的表现不是崩溃，而是"模型收不到工具"或"上下文口径少算一截"：
// 请求体的形状由 provider 决定，响应体的解析决定 Agent 能不能看到 tool_calls。
// 这里用 httptest 假服务端钉住两侧的字节形状，改协议必然要改动这里的基线。

// capturedRequest 是一次假 LLM 服务收到的请求。
type capturedRequest struct {
	Path   string
	Header http.Header
	Body   map[string]any
	Raw    string
}

// newFakeLLM 返回一个记录请求并回放 respJSON 的假 OpenAI 兼容服务。
// handler 返回值即为响应体；status 非 200 时按错误返回。
func newFakeLLM(t *testing.T, handler func(capturedRequest) (status int, body string)) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		status, resp := handler(capturedRequest{Path: r.URL.Path, Header: r.Header.Clone(), Body: body, Raw: string(raw)})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &count
}

func openAIEngine(baseURL string, cfg config.LLMConfig) *OpenAIEngine {
	return NewOpenAIEngine(baseURL+"/v1", "test-key", "mimo-x", cfg, "")
}

const okCompletion = `{"id":"c1","object":"chat.completion","model":"mimo-x",
"choices":[{"index":0,"message":{"role":"assistant","content":"hello","tool_calls":[]},"finish_reason":"stop"}],
"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`

func TestOpenAIEngine_ModelAndInterface(t *testing.T) {
	var e Engine = NewOpenAIEngine("http://127.0.0.1:1/v1", "k", "some-model", config.LLMConfig{}, "")
	if e.Model() != "some-model" {
		t.Errorf("Model() = %q, want some-model", e.Model())
	}
	e.CloseIdleConnections() // 不得 panic
}

// 基本往返：路径、鉴权头、model 字段。SDK 的 MaxRetries(0) 也在这里被间接钉住
// （见 TestOpenAIRetrySemantics）。
func TestOpenAIEngine_RequestShape(t *testing.T) {
	var got capturedRequest
	srv, _ := newFakeLLM(t, func(req capturedRequest) (int, string) {
		got = req
		return 200, okCompletion
	})
	e := openAIEngine(srv.URL, config.LLMConfig{Timeout: 5 * time.Second})

	resp, err := e.GenerateContent(context.Background(), []Message{
		{Role: RoleSystem, Content: "you are a director"},
		{Role: RoleUser, Content: "hi"},
	}, nil, &CallOptions{MaxTokens: 1234, Temperature: 0.3})
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}

	if got.Path != "/v1/chat/completions" {
		t.Errorf("path = %q, want /v1/chat/completions", got.Path)
	}
	if auth := got.Header.Get("Authorization"); auth != "Bearer test-key" {
		t.Errorf("Authorization = %q, want \"Bearer test-key\"", auth)
	}
	if got.Body["model"] != "mimo-x" {
		t.Errorf("model = %v, want mimo-x", got.Body["model"])
	}
	if mt, _ := got.Body["max_completion_tokens"].(float64); mt != 1234 {
		t.Errorf("max_completion_tokens = %v, want 1234（CallOptions 没传到请求体）", got.Body["max_completion_tokens"])
	}
	if temp, _ := got.Body["temperature"].(float64); temp != 0.3 {
		t.Errorf("temperature = %v, want 0.3", got.Body["temperature"])
	}

	msgs, _ := got.Body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d 条, want 2", len(msgs))
	}
	first, _ := msgs[0].(map[string]any)
	second, _ := msgs[1].(map[string]any)
	if first["role"] != "system" || first["content"] != "you are a director" {
		t.Errorf("system 消息形状 = %v", first)
	}
	if second["role"] != "user" || second["content"] != "hi" {
		t.Errorf("user 消息形状 = %v", second)
	}

	if len(resp.Choices) != 1 || resp.Choices[0].Content != "hello" {
		t.Errorf("响应解析 = %+v, want 一条 choice 内容为 hello", resp.Choices)
	}
}

// 工具声明必须原样进请求体：漏掉 tools 的表现是模型只回文本、Agent 空转一整轮。
func TestOpenAIEngine_ToolsAreSent(t *testing.T) {
	var got capturedRequest
	srv, _ := newFakeLLM(t, func(req capturedRequest) (int, string) {
		got = req
		return 200, okCompletion
	})
	e := openAIEngine(srv.URL, config.LLMConfig{})

	_, err := e.GenerateContent(context.Background(),
		[]Message{{Role: RoleUser, Content: "go"}},
		[]ToolDef{
			{Type: "function", Function: FunctionDef{
				Name:        "read_file",
				Description: "read a file",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
			}},
			{Type: "not_a_function", Function: FunctionDef{Name: "ignored"}},
		}, nil)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}

	tools, _ := got.Body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %d 条, want 只发 function 类型那一条（%v）", len(tools), got.Body["tools"])
	}
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	if fn["name"] != "read_file" || fn["description"] != "read a file" {
		t.Errorf("function = %v, want name/description 都带上", fn)
	}
	if _, ok := fn["parameters"].(map[string]any)["properties"]; !ok {
		t.Errorf("parameters 缺少 properties: %v", fn["parameters"])
	}
}

// tool 角色与 assistant 的 tool_calls 必须成对回灌，否则 provider 直接 400。
// DeepSeek 类模型还要求把 reasoning_content 原样带回去（SDK 类型没有这个字段，
// 只能走 SetExtraFields —— 这条路径最容易在升级 SDK 时静默丢失）。
func TestOpenAIEngine_ConversationHistoryShape(t *testing.T) {
	var got capturedRequest
	srv, _ := newFakeLLM(t, func(req capturedRequest) (int, string) {
		got = req
		return 200, okCompletion
	})
	e := openAIEngine(srv.URL, config.LLMConfig{})

	_, err := e.GenerateContent(context.Background(), []Message{
		{Role: RoleUser, Content: "读一下 main.go"},
		{Role: RoleAssistant, Content: "", Reasoning: "先看文件", ToolCalls: []ToolCall{{
			ID: "call-1", Type: "function",
			Function: FunctionCall{Name: "read_file", Arguments: `{"path":"main.go"}`},
		}}},
		{Role: RoleTool, Content: "package main", ToolCallID: "call-1", ToolName: "read_file"},
		{Role: RoleAssistant, Content: "这是入口文件"},
	}, nil, nil)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}

	msgs, _ := got.Body["messages"].([]any)
	// NormalizeMessages 会丢掉空 content 且无 tool_calls 的 assistant；这里保留 4 条。
	if len(msgs) != 4 {
		t.Fatalf("messages = %d 条, want 4：%s", len(msgs), got.Raw)
	}

	assistant := msgs[1].(map[string]any)
	calls, _ := assistant["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatalf("assistant.tool_calls = %v, want 1 条", assistant["tool_calls"])
	}
	call := calls[0].(map[string]any)
	if call["id"] != "call-1" || call["type"] != "function" {
		t.Errorf("tool_call = %v, want id/type 齐全", call)
	}
	fn := call["function"].(map[string]any)
	if fn["name"] != "read_file" || fn["arguments"] != `{"path":"main.go"}` {
		t.Errorf("function = %v, want name + 原样 arguments", fn)
	}
	if rc, _ := assistant["reasoning_content"].(string); rc != "先看文件" {
		t.Errorf("assistant.reasoning_content = %q, want 回灌 thinking 内容（extra fields 丢了）", rc)
	}

	tool := msgs[2].(map[string]any)
	if tool["role"] != "tool" || tool["tool_call_id"] != "call-1" {
		t.Errorf("tool 消息 = %v, want role=tool + tool_call_id", tool)
	}
	if tool["content"] != "package main" {
		t.Errorf("tool 内容 = %v, want package main", tool["content"])
	}
}

// reasoning_effort 必须同时带出 SDK 字段与厂商扩展的 thinking 开关：
// 少了后者，DeepSeek 类网关会当成"没开思考"，thinking 白付钱。
func TestOpenAIEngine_ReasoningEffortAndExtraFields(t *testing.T) {
	cases := []struct {
		providerEffort, callEffort string
		wantEffort                 string
	}{
		{"high", "", "high"},
		{"high", "max", "max"}, // 单次调用覆盖 provider 默认
		{"", "low", "low"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(fmt.Sprintf("%s+%s", tc.providerEffort, tc.callEffort), func(t *testing.T) {
			var got capturedRequest
			srv, _ := newFakeLLM(t, func(req capturedRequest) (int, string) {
				got = req
				return 200, okCompletion
			})
			e := NewOpenAIEngine(srv.URL+"/v1", "k", "m", config.LLMConfig{}, tc.providerEffort)

			_, err := e.GenerateContent(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil,
				&CallOptions{ReasoningEffort: tc.callEffort})
			if err != nil {
				t.Fatalf("GenerateContent: %v", err)
			}
			if got.Body["reasoning_effort"] != tc.wantEffort {
				t.Errorf("reasoning_effort = %v, want %v", got.Body["reasoning_effort"], tc.wantEffort)
			}
			thinking, _ := got.Body["thinking"].(map[string]any)
			if thinking["type"] != "enabled" {
				t.Errorf("thinking 扩展字段 = %v, want {type:enabled}", got.Body["thinking"])
			}
		})
	}

	// 没设 effort 时两个字段都不该出现（很多网关对未知字段报错）。
	var got capturedRequest
	srv, _ := newFakeLLM(t, func(req capturedRequest) (int, string) {
		got = req
		return 200, okCompletion
	})
	e := openAIEngine(srv.URL, config.LLMConfig{})
	if _, err := e.GenerateContent(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil, nil); err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	if _, ok := got.Body["reasoning_effort"]; ok {
		t.Error("未配置 effort 却发出了 reasoning_effort")
	}
	if _, ok := got.Body["thinking"]; ok {
		t.Error("未配置 effort 却发出了 thinking 扩展字段")
	}
}

// 响应解析：tool_calls、reasoning_content（裸 JSON 提取）、usage 口径。
// TotalInputTokens 是 TUI 面板与缓存命中率的唯一口径（AGENTS.md §6.8）。
func TestOpenAIEngine_ResponseParsing(t *testing.T) {
	body := `{"choices":[{"index":0,"message":{"role":"assistant",
		"content":"done","reasoning_content":"想了很久",
		"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"run_bash","arguments":"{\"cmd\":\"ls\"}"}}]}}],
		"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,
		"prompt_tokens_details":{"cached_tokens":60}}}`

	srv, _ := newFakeLLM(t, func(capturedRequest) (int, string) { return 200, body })
	e := openAIEngine(srv.URL, config.LLMConfig{})

	resp, err := e.GenerateContent(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil, nil)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(resp.Choices))
	}
	c := resp.Choices[0]
	if c.Content != "done" {
		t.Errorf("content = %q, want done", c.Content)
	}
	if c.Reasoning != "想了很久" {
		t.Errorf("reasoning = %q, want 想了很久（裸 JSON 提取路径失效）", c.Reasoning)
	}
	if len(c.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %+v, want 1 条", c.ToolCalls)
	}
	tc := c.ToolCalls[0]
	if tc.ID != "c1" || tc.Function.Name != "run_bash" || tc.Function.Arguments != `{"cmd":"ls"}` {
		t.Errorf("tool_call = %+v, want id/name/arguments 齐全", tc)
	}
	if tc.Type != "function" {
		t.Errorf("tool_call.type = %q, want function（Agent 侧按这个字段分发）", tc.Type)
	}

	if resp.Usage == nil {
		t.Fatal("usage = nil, want 解析出 token 用量")
	}
	u := resp.Usage
	if u.PromptTokens != 100 || u.CompletionTokens != 20 || u.TotalTokens != 120 {
		t.Errorf("usage = %+v, want 100/20/120", u)
	}
	// OpenAI 的 prompt_tokens 已含 cached_tokens → TotalInputTokens 就等于它。
	if u.TotalInputTokens != 100 {
		t.Errorf("TotalInputTokens = %d, want 100（OpenAI 口径：prompt 已含缓存）", u.TotalInputTokens)
	}
	if u.CacheReadInputTokens != 60 {
		t.Errorf("CacheReadInputTokens = %d, want 60（缓存命中率会算错）", u.CacheReadInputTokens)
	}
}

// 部分 Anthropic 兼容网关在 OpenAI 形状里塞 cache_*_input_tokens，
// 引擎要从裸 JSON 里捞出来，否则缓存账目凭空少一笔。
func TestOpenAIEngine_CacheFieldsFromRawUsage(t *testing.T) {
	body := `{"choices":[{"index":0,"message":{"role":"assistant","content":"x"}}],
		"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12,
		"cache_read_input_tokens":700,"cache_creation_input_tokens":300}}`
	srv, _ := newFakeLLM(t, func(capturedRequest) (int, string) { return 200, body })
	e := openAIEngine(srv.URL, config.LLMConfig{})

	resp, err := e.GenerateContent(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil, nil)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	if resp.Usage.CacheReadInputTokens != 700 {
		t.Errorf("CacheReadInputTokens = %d, want 700（raw usage 提取失效）", resp.Usage.CacheReadInputTokens)
	}
	if resp.Usage.CacheCreationInputTokens != 300 {
		t.Errorf("CacheCreationInputTokens = %d, want 300", resp.Usage.CacheCreationInputTokens)
	}
}

// 没有 usage 的响应（一些网关在流式或错误路径上不返回）必须是 nil，
// 而不是全零 —— 全零会被上层当成真实计数写进 token 面板。
func TestOpenAIEngine_MissingUsageStaysNil(t *testing.T) {
	srv, _ := newFakeLLM(t, func(capturedRequest) (int, string) {
		return 200, `{"choices":[{"index":0,"message":{"role":"assistant","content":"x"}}]}`
	})
	e := openAIEngine(srv.URL, config.LLMConfig{})

	resp, err := e.GenerateContent(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil, nil)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	if resp.Usage != nil {
		t.Errorf("Usage = %+v, want nil", resp.Usage)
	}
}

// ---------- 重试语义 ----------

// 400 不重试，5xx 才重试；而 SDK 自带的重试已被 WithMaxRetries(0) 关掉，
// 所以"请求次数"就是我们自己的重试计数器。这条断言同时钉住两边：
// 一旦有人恢复 SDK 重试，MaxRetries=0 的用例会看到 3 次请求而不是 1 次。
func TestOpenAIRetrySemantics(t *testing.T) {
	t.Run("400 不重试", func(t *testing.T) {
		srv, count := newFakeLLM(t, func(capturedRequest) (int, string) {
			return 400, `{"error":{"message":"bad request","type":"invalid_request_error"}}`
		})
		e := openAIEngine(srv.URL, config.LLMConfig{MaxRetries: 5, Timeout: 2 * time.Second})

		_, err := e.GenerateContent(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil, nil)
		if err == nil {
			t.Fatal("400 被当成成功")
		}
		if n := count.Load(); n != 1 {
			t.Errorf("请求次数 = %d, want 1（客户端错误重试无意义）", n)
		}
	})

	// MaxRetries 的 0 是"没配"而不是"不重试"（回落 5 次）。这里用短超时把
	// 第一次退避（baseDelay 10s）钳制到剩余预算，于是能在几十毫秒内观察到：
	// 引擎只发出了 **1 次**请求 —— 这同时钉住了 option.WithMaxRetries(0)：
	// SDK 自带重试若被打开，同一个 500 会在服务端留下 3 次请求。
	t.Run("500 会退避重试，且 SDK 自带重试是关着的", func(t *testing.T) {
		srv, count := newFakeLLM(t, func(capturedRequest) (int, string) {
			return 500, `{"error":{"message":"boom"}}`
		})
		e := openAIEngine(srv.URL, config.LLMConfig{Timeout: 120 * time.Millisecond})

		start := time.Now()
		_, err := e.GenerateContent(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil, nil)
		if err == nil {
			t.Fatal("持续 500 却返回成功")
		}
		if n := count.Load(); n != 1 {
			t.Errorf("服务端收到 %d 次请求, want 1（>1 说明 SDK 内部重试被打开，与引擎自己的计数器叠加）", n)
		}
		// 退避被调用超时打断的两条合法出口：退避途中 ctx 到期（retry aborted），
		// 或到期后发出去的那一次请求自己撞到 deadline（llm call cancelled）。
		if msg := err.Error(); !strings.Contains(msg, "retry aborted") && !strings.Contains(msg, "llm call cancelled") {
			t.Errorf("err = %v, want 说明退避/请求被调用超时打断", err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("耗时 %v, want 受 ctx 预算钳制（退避不能越过调用超时）", elapsed)
		}
	})

	t.Run("取消立即返回而不是继续重试", func(t *testing.T) {
		srv, count := newFakeLLM(t, func(capturedRequest) (int, string) {
			return 500, `{"error":{"message":"boom"}}`
		})
		e := openAIEngine(srv.URL, config.LLMConfig{MaxRetries: 5})

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := e.GenerateContent(ctx, []Message{{Role: RoleUser, Content: "x"}}, nil, nil)
		if err == nil {
			t.Fatal("已取消的 ctx 却返回成功")
		}
		if n := count.Load(); n != 0 {
			t.Errorf("请求次数 = %d, want 0（取消后不该再发请求）", n)
		}
	})
}

// ---------- 流式 ----------

func sseChunks(t *testing.T, lines ...string) string {
	t.Helper()
	var b strings.Builder
	for _, l := range lines {
		b.WriteString("data: ")
		b.WriteString(l)
		b.WriteString("\n\n")
	}
	b.WriteString("data: [DONE]\n\n")
	return b.String()
}

// 流式请求必须带 stream_options.include_usage，否则最后一个 chunk 没有 usage，
// token 面板与压缩阈值都会以为"这一轮没花 token"。
func TestOpenAIEngine_Streaming(t *testing.T) {
	var sawStreamOptions atomic.Bool
	var sawStream atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		v, _ := body["stream"].(bool)
		sawStream.Store(v)
		if so, ok := body["stream_options"].(map[string]any); ok {
			if b, _ := so["include_usage"].(bool); b {
				sawStreamOptions.Store(true)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, sseChunks(t,
			`{"choices":[{"index":0,"delta":{"content":"He"}}]}`,
			`{"choices":[{"index":0,"delta":{"content":"llo","reasoning_content":"想了想"}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call-9","type":"function","function":{"name":"read_file","arguments":"{\"pa"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a.go\"}"}}]}}]}`,
			`{"choices":[],"usage":{"prompt_tokens":33,"completion_tokens":7,"total_tokens":40,"prompt_tokens_details":{"cached_tokens":11}}}`,
		))
	}))
	defer srv.Close()

	e := openAIEngine(srv.URL, config.LLMConfig{})
	var streamed strings.Builder
	resp, err := e.GenerateContent(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil,
		&CallOptions{StreamHandler: func(_ context.Context, chunk []byte) error {
			streamed.Write(chunk)
			return nil
		}})
	if err != nil {
		t.Fatalf("GenerateContent streaming: %v", err)
	}

	if v, _ := sawStream.Load().(bool); !v {
		t.Error("请求体没有 stream=true")
	}
	if !sawStreamOptions.Load() {
		t.Error("stream_options.include_usage 没有打开（流式的 token 用量会全部丢）")
	}

	if got := streamed.String(); got != "Hello" {
		t.Errorf("handler 收到的增量 = %q, want Hello", got)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(resp.Choices))
	}
	c := resp.Choices[0]
	if c.Content != "Hello" {
		t.Errorf("聚合 content = %q, want Hello", c.Content)
	}
	if c.Reasoning != "想了想" {
		t.Errorf("聚合 reasoning = %q, want 想了想（DeepSeek 思考模式下必须拼回来）", c.Reasoning)
	}
	if len(c.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %+v, want 跨 chunk 拼成 1 条", c.ToolCalls)
	}
	if c.ToolCalls[0].ID != "call-9" || c.ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("tool_call = %+v, want id/name 来自第一个分片", c.ToolCalls[0])
	}
	if c.ToolCalls[0].Function.Arguments != `{"path":"a.go"}` {
		t.Errorf("arguments = %q, want 跨分片拼接后的完整 JSON", c.ToolCalls[0].Function.Arguments)
	}
	if c.ToolCalls[0].Type != "function" {
		t.Errorf("tool_call.type = %q, want function", c.ToolCalls[0].Type)
	}

	if resp.Usage == nil {
		t.Fatal("流式 usage = nil")
	}
	if resp.Usage.PromptTokens != 33 || resp.Usage.CompletionTokens != 7 || resp.Usage.TotalTokens != 40 {
		t.Errorf("usage = %+v, want 33/7/40", resp.Usage)
	}
	if resp.Usage.TotalInputTokens != 33 {
		t.Errorf("TotalInputTokens = %d, want 33", resp.Usage.TotalInputTokens)
	}
	if resp.Usage.CacheReadInputTokens != 11 {
		t.Errorf("CacheReadInputTokens = %d, want 11", resp.Usage.CacheReadInputTokens)
	}
}

// handler 返回错误必须中止并把错误冒泡（TUI 靠这个感知"用户已经取消"）。
func TestOpenAIEngine_StreamHandlerErrorAborts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, sseChunks(t, `{"choices":[{"index":0,"delta":{"content":"a"}}]}`))
	}))
	defer srv.Close()

	e := openAIEngine(srv.URL, config.LLMConfig{})
	wantErr := fmt.Errorf("handler 拒绝")
	_, err := e.GenerateContent(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil,
		&CallOptions{StreamHandler: func(context.Context, []byte) error { return wantErr }})
	if err == nil {
		t.Fatal("handler 错误被吞掉了")
	}
	if !strings.Contains(err.Error(), "handler 拒绝") {
		t.Errorf("err = %v, want 包含 handler 的原始错误", err)
	}
}

// 非 200 的 JSON 错误体要冒泡出 provider 的 message，而不是"unknown error"。
func TestOpenAIEngine_ErrorBodyPropagates(t *testing.T) {
	srv, _ := newFakeLLM(t, func(capturedRequest) (int, string) {
		return 401, `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`
	})
	e := openAIEngine(srv.URL, config.LLMConfig{})

	_, err := e.GenerateContent(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil, nil)
	if err == nil {
		t.Fatal("401 被当成成功")
	}
	if !strings.Contains(err.Error(), "Incorrect API key provided") {
		t.Errorf("err = %v, want 带上 provider 的原文（排障全靠它）", err)
	}
}
