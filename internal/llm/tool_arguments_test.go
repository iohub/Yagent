package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"yagent/internal/config"
)

// tool_call 的 arguments 有两个边界会被写成"不能用的形状"：provider 压根不发
// arguments（读回来是空串），以及截断/幻觉产出的非法 JSON 与字面量 null。
// 这里的基线钉三件事：
//  1. 读进来的空串补成 "{}"（与 engine_anthropic.go 的两处兜底对称）；
//  2. 发出去的不可用参数折成 "{}"（网关收到 "arguments": "" 或 "input": null 直接 400，
//     一次幻觉因此永久毒化上下文）；
//  3. 交进来的 llm.Message 与 provider 原字节一个都不许改（AGENTS.md 硬约束 13：
//     rollout 指纹与 llm 日志吃的是原字节）。

// ---------- Layer 0：唯一判据 ----------

func TestUsableToolArguments_Table(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want bool
	}{
		{"空串", "", false},
		{"字面量_null_unmarshal_成功但得到_nil_map", "null", false},
		{"截断的对象", `{"a":`, false},
		{"数组不是对象", "[]", false},
		{"标量不是对象", "123", false},
		{"纯空白", "   ", false},
		{"空对象", "{}", true},
		{"合法对象", `{"reason":"x"}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := UsableToolArguments(c.raw); got != c.want {
				t.Errorf("UsableToolArguments(%q) = %v, want %v", c.raw, got, c.want)
			}
		})
	}
}

// ---------- Layer 1：OpenAI 响应侧空串兜底 ----------

// 流式：provider 一个 arguments 分片都不发时，累加结果恒为空串。兜底必须在收尾装配
// 处 —— 在分片 += 处补 "{}" 会被后续分片当前缀继续拼接。
func TestOpenAIStream_EmptyArguments_BecomesEmptyObject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, sseChunks(t,
			`{"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[`+
				`{"index":0,"id":"call-1","type":"function","function":{"name":"agent_exit"}},`+
				`{"index":1,"id":"call-2","type":"function","function":{"name":"read_file","arguments":"{\"target_"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{"tool_calls":[`+
				`{"index":1,"function":{"arguments":"file\":\"a.go\"}"}}]}}]}`,
			`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":8,"completion_tokens":3,"total_tokens":11}}`,
		))
	}))
	defer srv.Close()

	e := openAIEngine(srv.URL, config.LLMConfig{})
	resp, err := e.GenerateContent(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil,
		&CallOptions{StreamHandler: func(context.Context, []byte) error { return nil }})
	if err != nil {
		t.Fatalf("GenerateContent streaming: %v", err)
	}
	if len(resp.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(resp.Choices))
	}
	calls := resp.Choices[0].ToolCalls
	if len(calls) != 2 {
		t.Fatalf("tool_calls = %+v, want 2 条", calls)
	}
	if got := calls[0].Function.Arguments; got != "{}" {
		t.Errorf("arguments = %q, want {} —— 空串会让 Agent 侧的工具分发直接报错", got)
	}
	if calls[0].ID != "call-1" || calls[0].Function.Name != "agent_exit" {
		t.Errorf("tool_call = %+v, want id/name 来自首个分片", calls[0])
	}
	// 只补空串：有真实分片的调用必须原样拼回，不能被收尾兜底碰到。
	if got := calls[1].Function.Arguments; got != `{"target_file":"a.go"}` {
		t.Errorf("arguments = %q, want 跨分片拼接后的完整 JSON", got)
	}
}

// 非流式：provider 省略 arguments 字段时同样是空串。非法 JSON 原样留着（诊断信息
// 属于工具层与截断标记，不在读取侧抹掉）。
func TestOpenAINonStream_EmptyArguments_BecomesEmptyObject(t *testing.T) {
	srv, _ := newFakeLLM(t, func(capturedRequest) (int, string) {
		return 200, `{"id":"c1","object":"chat.completion","model":"mimo-x","choices":[{"index":0,
"message":{"role":"assistant","content":"","tool_calls":[
{"index":0,"id":"call-1","type":"function","function":{"name":"agent_exit"}},
{"index":1,"id":"call-2","type":"function","function":{"name":"read_file","arguments":"{\"target_file\":\"a.go\"}"}},
{"index":2,"id":"call-3","type":"function","function":{"name":"run_bash","arguments":"{\"cmd\": \"ls"}}
]},"finish_reason":"tool_calls"}],
"usage":{"prompt_tokens":5,"completion_tokens":5,"total_tokens":10}}`
	})
	e := openAIEngine(srv.URL, config.LLMConfig{})

	resp, err := e.GenerateContent(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil, nil)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	calls := resp.Choices[0].ToolCalls
	if len(calls) != 3 {
		t.Fatalf("tool_calls = %+v, want 3 条", calls)
	}
	if got := calls[0].Function.Arguments; got != "{}" {
		t.Errorf("省略 arguments 的工具调用 = %q, want {}（与 engine_anthropic.go 的空串兜底对称）", got)
	}
	if got := calls[1].Function.Arguments; got != `{"target_file":"a.go"}` {
		t.Errorf("合法 arguments = %q, want 原样透传", got)
	}
	if got := calls[2].Function.Arguments; got != `{"cmd": "ls` {
		t.Errorf("非法 JSON = %q, want 原样留着 —— 只补空串，截断的证据不能在这里消失", got)
	}
}

// ---------- Layer 2：请求出口防毒化 ----------

// firstToolCallArguments 从假服务捕获的请求体里取第一条 tool_call 的 arguments。
func firstToolCallArguments(t *testing.T, body map[string]any) string {
	t.Helper()
	msgs, _ := body["messages"].([]any)
	for _, raw := range msgs {
		msg, _ := raw.(map[string]any)
		calls, _ := msg["tool_calls"].([]any)
		if len(calls) == 0 {
			continue
		}
		call, _ := calls[0].(map[string]any)
		fn, _ := call["function"].(map[string]any)
		args, _ := fn["arguments"].(string)
		return args
	}
	t.Fatalf("请求体里没有 tool_calls: %v", body)
	return ""
}

// 历史里的空/非法 arguments 经 resume 回放发出去时必须是个合法对象，
// 而且交进来的那份 llm.Message 不能被改写（ToolCalls 与 history/rollout 共享底层数组）。
func TestOpenAIRequest_InvalidArguments_SentAsEmptyObject_SourceUnchanged(t *testing.T) {
	cases := []struct {
		name string
		args string
	}{
		{"空串", ""},
		{"字面量_null", "null"},
		{"截断的_JSON", `{"a":`},
		{"数组", "[]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got capturedRequest
			srv, _ := newFakeLLM(t, func(req capturedRequest) (int, string) {
				got = req
				return 200, okCompletion
			})
			e := openAIEngine(srv.URL, config.LLMConfig{})

			messages := []Message{
				{Role: RoleUser, Content: "收工"},
				{Role: RoleAssistant, ToolCalls: []ToolCall{{
					ID: "call-1", Type: "function",
					Function: FunctionCall{Name: "agent_exit", Arguments: c.args},
				}}},
				{Role: RoleTool, ToolCallID: "call-1", Content: "done"},
			}

			if _, err := e.GenerateContent(context.Background(), messages, nil, nil); err != nil {
				t.Fatalf("GenerateContent: %v", err)
			}

			// (a) 发出去的必须是空对象，不能是原样的空串/非法 JSON。
			if out := firstToolCallArguments(t, got.Body); out != "{}" {
				t.Errorf("请求体 arguments = %q, want {} —— 原样发出去会让网关 400 并永久毒化上下文", out)
			}
			// (b) 原字节未改写。
			if still := messages[1].ToolCalls; len(still) != 1 {
				t.Fatalf("源消息的 tool_calls 被规范化掉了: %+v", still)
			}
			if still := messages[1].ToolCalls[0].Function.Arguments; still != c.args {
				t.Errorf("provider 原字节被改写: %q, want %q", still, c.args)
			}
		})
	}
}

// Anthropic 请求侧：字面量 null 会 unmarshal 成功并留下 nil map，原样 marshal 就是
// "input": null —— tool_use.input 必须是对象，一个空参数因此让整次请求 400。
func TestAnthropicRequest_NullInput_SentAsEmptyObject(t *testing.T) {
	e := anthropicEngine("http://127.0.0.1:1", config.LLMConfig{})
	messages := []Message{{
		Role: RoleAssistant,
		ToolCalls: []ToolCall{{
			ID: "tu-1", Type: "function",
			Function: FunctionCall{Name: "agent_exit", Arguments: "null"},
		}},
	}}

	req := e.buildRequestBody(messages, nil, nil)

	var blocks []anthropicContentBlock
	if err := json.Unmarshal(req.Messages[0].Content, &blocks); err != nil {
		t.Fatalf("assistant content 不是块数组: %v (%s)", err, req.Messages[0].Content)
	}
	if len(blocks) != 1 || blocks[0].Type != "tool_use" {
		t.Fatalf("assistant blocks = %+v, want 一个 tool_use", blocks)
	}
	if string(blocks[0].Input) != "{}" {
		t.Errorf("tool_use.input = %s, want 空对象 {}（null 会让请求非法）", blocks[0].Input)
	}
	var input map[string]any
	if err := json.Unmarshal(blocks[0].Input, &input); err != nil || input == nil {
		t.Errorf("tool_use.input 不是可用对象: %s (%v)", blocks[0].Input, err)
	}
	// 原字节不改写：与 OpenAI 出口同一套原则。
	if got := messages[0].ToolCalls[0].Function.Arguments; got != "null" {
		t.Errorf("provider 原字节被改写: %q, want null", got)
	}
}

// 合法的 arguments 必须照旧原样进请求体（兜底不是"重写一切"）。
func TestAnthropicRequest_ValidInput_SentVerbatim_SourceUnchanged(t *testing.T) {
	e := anthropicEngine("http://127.0.0.1:1", config.LLMConfig{})
	const args = `{"reason":"done"}`
	messages := []Message{{
		Role: RoleAssistant,
		ToolCalls: []ToolCall{{
			ID: "tu-2", Type: "function",
			Function: FunctionCall{Name: "agent_exit", Arguments: args},
		}},
	}}

	req := e.buildRequestBody(messages, nil, nil)

	var blocks []anthropicContentBlock
	if err := json.Unmarshal(req.Messages[0].Content, &blocks); err != nil {
		t.Fatalf("assistant content 不是块数组: %v (%s)", err, req.Messages[0].Content)
	}
	var input map[string]any
	if err := json.Unmarshal(blocks[0].Input, &input); err != nil {
		t.Fatalf("tool_use.input 不合法: %s (%v)", blocks[0].Input, err)
	}
	if input["reason"] != "done" {
		t.Errorf("input = %v, want reason=done（合法参数被兜底吃掉了）", input)
	}
	if got := messages[0].ToolCalls[0].Function.Arguments; got != args {
		t.Errorf("provider 原字节被改写: %q, want %q", got, args)
	}
}
