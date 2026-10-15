package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"yagent/internal/config"
)

// Anthropic Messages 协议与 OpenAI 形状差别很大：system 是顶层字段、tool 结果
// 是 user 角色里的 tool_result 块、思考是 thinking 块。这里的基线钉的是
// "发出去的字节"与"从字节里读回来的东西"，任何一方写错的表现都是 provider 400
// 或 Agent 看不见工具。

func anthropicEngine(baseURL string, cfg config.LLMConfig) *AnthropicEngine {
	return NewAnthropicEngine(testProvider(baseURL+"/v1", "sk-test", "claude-sonnet-4"), cfg)
}

const okAnthropic = `{"id":"m1","type":"message","role":"assistant",
"content":[{"type":"text","text":"hello"}],
"usage":{"input_tokens":10,"output_tokens":5}}`

func TestAnthropicEngine_Model(t *testing.T) {
	var e Engine = NewAnthropicEngine(testProvider("http://127.0.0.1:1/v1", "k", "claude-x"), config.LLMConfig{})
	if e.Model() != "claude-x" {
		t.Errorf("Model() = %q, want claude-x", e.Model())
	}
	e.CloseIdleConnections()
}

// ---------- 请求体形状 ----------

// system 必须被提出 messages 之外并拼成一个顶层字段（多条 system 用空行分隔）。
// 把 system 当普通消息发出去，Messages API 会直接拒绝。
func TestAnthropicBuildRequest_SystemIsHoisted(t *testing.T) {
	e := anthropicEngine("http://127.0.0.1:1", config.LLMConfig{})
	req := e.buildRequestBody([]Message{
		{Role: RoleSystem, Content: "A"},
		{Role: RoleSystem, Content: "B"},
		{Role: RoleUser, Content: "hi"},
	}, nil, nil)

	if req.System != "A\n\nB" {
		t.Errorf("system = %q, want \"A\\n\\nB\"", req.System)
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v, want 只剩那一条 user", req.Messages)
	}
	if got := string(req.Messages[0].Content); got != `"hi"` {
		t.Errorf("user content = %s, want JSON 字符串 \"hi\"", got)
	}
	// 没有 system 消息时字段必须整个缺席（omitempty）。
	none := e.buildRequestBody([]Message{{Role: RoleUser, Content: "x"}}, nil, nil)
	if none.System != "" {
		t.Errorf("system = %q, want 空", none.System)
	}
}

// 特殊字符必须走 JSON 转义：内容里有引号/反斜杠/换行时，手拼字符串会产生非法 JSON，
// 而 provider 报的错是一句无法定位的 "body parse failed"。
func TestAnthropicBuildRequest_EscapesContent(t *testing.T) {
	e := anthropicEngine("http://127.0.0.1:1", config.LLMConfig{})
	req := e.buildRequestBody([]Message{
		{Role: RoleUser, Content: "他说\"走\"\n然后\t制表"},
	}, nil, nil)

	// 必须是合法 JSON 且能还原出原文。
	var back string
	if err := json.Unmarshal(req.Messages[0].Content, &back); err != nil {
		t.Fatalf("user content 不是合法 JSON: %v (%s)", err, req.Messages[0].Content)
	}
	if back != "他说\"走\"\n然后\t制表" {
		t.Errorf("还原结果 = %q, want 原文", back)
	}
}

// RoleTool 在 Anthropic 侧不存在：它必须变成 user 角色里的 tool_result 块，
// 并带上 tool_use_id。写成 role:"tool" 会被 400 拒掉。
func TestAnthropicBuildRequest_ToolResult(t *testing.T) {
	e := anthropicEngine("http://127.0.0.1:1", config.LLMConfig{})
	req := e.buildRequestBody([]Message{
		{Role: RoleAssistant, ToolCalls: []ToolCall{{
			ID: "tu-1", Type: "function",
			Function: FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`},
		}}},
		{Role: RoleTool, ToolCallID: "tu-1", Content: "package main"},
	}, nil, nil)

	if len(req.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(req.Messages))
	}

	var blocks []anthropicContentBlock
	if err := json.Unmarshal(req.Messages[0].Content, &blocks); err != nil {
		t.Fatalf("assistant content 不是块数组: %v (%s)", err, req.Messages[0].Content)
	}
	if len(blocks) != 1 || blocks[0].Type != "tool_use" {
		t.Fatalf("assistant blocks = %+v, want 一个 tool_use", blocks)
	}
	if blocks[0].ID != "tu-1" || blocks[0].Name != "read_file" {
		t.Errorf("tool_use = %+v, want id/name 齐全", blocks[0])
	}
	// input 必须是对象（不是字符串）：provider 按 JSON Schema 校验它。
	var input map[string]any
	if err := json.Unmarshal(blocks[0].Input, &input); err != nil {
		t.Errorf("tool_use.input = %s, want JSON 对象: %v", blocks[0].Input, err)
	}
	if input["path"] != "a.go" {
		t.Errorf("input = %v, want path=a.go", input)
	}

	if req.Messages[1].Role != "user" {
		t.Errorf("tool 结果的角色 = %q, want user", req.Messages[1].Role)
	}
	var res []anthropicContentBlock
	if err := json.Unmarshal(req.Messages[1].Content, &res); err != nil {
		t.Fatalf("tool_result content 不是块数组: %v", err)
	}
	if res[0].Type != "tool_result" || res[0].ToolUseID != "tu-1" {
		t.Errorf("tool_result = %+v, want type + tool_use_id", res[0])
	}
	var content string
	if err := json.Unmarshal(res[0].Content, &content); err != nil || content != "package main" {
		t.Errorf("tool_result.content = %s, want \"package main\"（err=%v）", res[0].Content, err)
	}
}

// 模型给出的 arguments 不一定是合法 JSON（截断/幻觉都会破坏它）。
// 这里必须退化成 {"raw": ...} 而不是把空 input 发出去 —— 空 input 会让
// 下一轮请求整体 400，把一次幻觉放大成整个任务失败。
func TestAnthropicBuildRequest_MalformedToolArguments(t *testing.T) {
	e := anthropicEngine("http://127.0.0.1:1", config.LLMConfig{})
	req := e.buildRequestBody([]Message{{
		Role: RoleAssistant,
		ToolCalls: []ToolCall{{ID: "tu-2", Function: FunctionCall{
			Name: "run_bash", Arguments: `{"cmd": "ls`,
		}}},
	}}, nil, nil)

	var blocks []anthropicContentBlock
	if err := json.Unmarshal(req.Messages[0].Content, &blocks); err != nil {
		t.Fatalf("content 不是块数组: %v", err)
	}
	var input map[string]any
	if err := json.Unmarshal(blocks[0].Input, &input); err != nil {
		t.Fatalf("非法 arguments 产出了不合法的 input: %v (%s)", err, blocks[0].Input)
	}
	if input["raw"] != `{"cmd": "ls` {
		t.Errorf("input = %v, want {raw: 原始串}", input)
	}
}

// thinking 块必须回灌（Extended Thinking 要求带 signature 的块原样返回，
// 至少不能丢文本），且顺序是 text 之后。
func TestAnthropicBuildRequest_ThinkingBlockEchoed(t *testing.T) {
	e := anthropicEngine("http://127.0.0.1:1", config.LLMConfig{})
	req := e.buildRequestBody([]Message{{
		Role: RoleAssistant, Content: "答案", Reasoning: "推理过程",
	}}, nil, nil)

	// 两个块 → 走数组分支
	var blocks []anthropicContentBlock
	if err := json.Unmarshal(req.Messages[0].Content, &blocks); err != nil {
		t.Fatalf("content = %s, want 块数组: %v", req.Messages[0].Content, err)
	}
	if len(blocks) != 2 || blocks[0].Type != "text" || blocks[1].Type != "thinking" {
		t.Fatalf("blocks = %+v, want [text, thinking]", blocks)
	}
	if blocks[1].Thinking != "推理过程" {
		t.Errorf("thinking = %q, want 推理过程", blocks[1].Thinking)
	}

	// 只有一个 text 块时应退化成裸字符串（provider 两种都接受，但老形状更省）
	single := e.buildRequestBody([]Message{{Role: RoleAssistant, Content: "只有文本"}}, nil, nil)
	if got := string(single.Messages[0].Content); got != `"只有文本"` {
		t.Errorf("单个 text 块应退化成字符串, got %s", got)
	}
}

func TestAnthropicBuildRequest_MaxTokensAndTools(t *testing.T) {
	e := anthropicEngine("http://127.0.0.1:1", config.LLMConfig{})

	// 默认 8192
	base := e.buildRequestBody([]Message{{Role: RoleUser, Content: "x"}}, nil, nil)
	if base.MaxTokens != 8192 {
		t.Errorf("默认 max_tokens = %d, want 8192", base.MaxTokens)
	}

	// CallOptions 覆盖，且 temperature 只在 >0 时下发（0 会覆盖掉服务端默认）
	opts := e.buildRequestBody([]Message{{Role: RoleUser, Content: "x"}}, nil,
		&CallOptions{MaxTokens: 20000, Temperature: 0.7})
	if opts.MaxTokens != 20000 || opts.Temperature != 0.7 {
		t.Errorf("opts = %+v, want 20000/0.7", opts)
	}
	cold := e.buildRequestBody([]Message{{Role: RoleUser, Content: "x"}}, nil, &CallOptions{})
	if cold.Temperature != 0 {
		t.Errorf("未指定 temperature 时下发了 %v, want 缺席", cold.Temperature)
	}

	// tools：input_schema 是必填字段，Parameters 为 nil 时必须补空对象。
	toolReq := e.buildRequestBody([]Message{{Role: RoleUser, Content: "x"}}, []ToolDef{
		{Type: "function", Function: FunctionDef{Name: "no_params"}},
		{Type: "function", Function: FunctionDef{Name: "with_params", Parameters: map[string]any{"type": "object"}}},
		{Type: "other", Function: FunctionDef{Name: "skipped"}},
	}, nil)
	if len(toolReq.Tools) != 2 {
		t.Fatalf("tools = %+v, want 只发 function 类型两条", toolReq.Tools)
	}
	if toolReq.Tools[0].Name != "no_params" {
		t.Errorf("第一条 tools = %+v, want 保持声明顺序", toolReq.Tools[0])
	}
	if got, _ := json.Marshal(toolReq.Tools[0].InputSchema); string(got) != `{"type":"object"}` {
		t.Errorf("input_schema 缺省补位 = %s, want {\"type\":\"object\"}（Messages API 视缺失为非法）", got)
	}
	if got, _ := json.Marshal(toolReq.Tools[1].InputSchema); string(got) != `{"type":"object"}` {
		t.Errorf("显式 input_schema = %s, want 原样透传", got)
	}
}

// provider 的 max_tokens / temperature 必须进请求体。此前引擎里写死 8192，
// 配置值只在日志里出现过 —— 表现是"改了 max_tokens 什么也没变"。
// thinking 那一支仍然由 budget+4096 兜底：provider 的上限低于思考预算时必须抬上去，
// 否则 thinking 被输出上限吃掉，provider 直接 400。
func TestAnthropicBuildRequest_ProviderDefaults(t *testing.T) {
	provider := testProvider("http://127.0.0.1:1/v1", "k", "claude-x")
	provider.MaxTokens = 20000
	provider.Temperature = 0.4
	e := NewAnthropicEngine(provider, config.LLMConfig{})

	base := e.buildRequestBody([]Message{{Role: RoleUser, Content: "x"}}, nil, &CallOptions{})
	if base.MaxTokens != 20000 {
		t.Errorf("max_tokens = %d, want provider 默认 20000", base.MaxTokens)
	}
	if base.Temperature != 0.4 {
		t.Errorf("temperature = %v, want provider 默认 0.4", base.Temperature)
	}

	// 单次调用优先于 provider 默认。
	over := e.buildRequestBody([]Message{{Role: RoleUser, Content: "x"}}, nil,
		&CallOptions{MaxTokens: 60000, Temperature: 0.9})
	if over.MaxTokens != 60000 || over.Temperature != 0.9 {
		t.Errorf("opts = %+v, want 60000/0.9 覆盖 provider 默认", over)
	}

	// provider 上限低于 high 档思考预算（24576）→ 抬到 budget+4096。
	think := e.buildRequestBody([]Message{{Role: RoleUser, Content: "x"}}, nil,
		&CallOptions{ReasoningEffort: "high"})
	if think.Thinking == nil || think.Thinking.BudgetTokens != 24576 {
		t.Fatalf("thinking = %+v, want {enabled, 24576}", think.Thinking)
	}
	if think.MaxTokens != 24576+4096 {
		t.Errorf("max_tokens = %d, want budget+4096=%d（provider 的 20000 装不下思考预算）",
			think.MaxTokens, 24576+4096)
	}

	// 没配 max_tokens 时保持 8192 兜底，别突然不发这个字段（Messages API 要求必填）。
	bare := NewAnthropicEngine(testProvider("http://127.0.0.1:1/v1", "k", "claude-x"), config.LLMConfig{})
	if got := bare.buildRequestBody([]Message{{Role: RoleUser, Content: "x"}}, nil, nil); got.MaxTokens != 8192 {
		t.Errorf("默认 max_tokens = %d, want 8192", got.MaxTokens)
	}
}

// reasoning_effort → thinking budget 的映射，是"配置里写 high 就能思考"的唯一实现。
func TestReasoningEffortToBudget(t *testing.T) {
	cases := map[string]int{
		"low": 1024, "medium": 8192, "high": 24576, "max": 32000,
		"HIGH": 24576, " medium ": 0, "": 0, "bogus": 0,
	}
	for effort, want := range cases {
		if got := reasoningEffortToBudget(effort); got != want {
			t.Errorf("reasoningEffortToBudget(%q) = %d, want %d", effort, got, want)
		}
	}
}

// 开启 thinking 时三件事必须同时成立：budget 映射、max_tokens 必须大于 budget
// （否则思考预算被输出上限吃掉）、temperature 强制 1.0（provider 在 thinking
// 模式下拒绝其它值）。
func TestAnthropicBuildRequest_ThinkingEnabled(t *testing.T) {
	e := anthropicEngine("http://127.0.0.1:1", config.LLMConfig{})

	req := e.buildRequestBody([]Message{{Role: RoleUser, Content: "x"}}, nil,
		&CallOptions{ReasoningEffort: "high", MaxTokens: 1000, Temperature: 0.2})
	if req.Thinking == nil || req.Thinking.Type != "enabled" || req.Thinking.BudgetTokens != 24576 {
		t.Fatalf("thinking = %+v, want {enabled, 24576}", req.Thinking)
	}
	if req.MaxTokens <= req.Thinking.BudgetTokens {
		t.Errorf("max_tokens = %d 没有超过 budget %d", req.MaxTokens, req.Thinking.BudgetTokens)
	}
	if req.MaxTokens != 24576+4096 {
		t.Errorf("max_tokens = %d, want budget+4096=%d", req.MaxTokens, 24576+4096)
	}
	if req.Temperature != 1.0 {
		t.Errorf("temperature = %v, want 强制 1.0（thinking 模式下 provider 拒绝其它值）", req.Temperature)
	}

	// 预算已经大于 budget 时不要无理放大。
	big := e.buildRequestBody([]Message{{Role: RoleUser, Content: "x"}}, nil,
		&CallOptions{ReasoningEffort: "low", MaxTokens: 60000})
	if big.MaxTokens != 60000 {
		t.Errorf("max_tokens = %d, want 保留 60000", big.MaxTokens)
	}

	// 认不出的 effort：budget=0。此时绝不能再发 thinking ——
	// budget_tokens < 1024 会让每一次调用都被 provider 拒绝。
	junk := e.buildRequestBody([]Message{{Role: RoleUser, Content: "x"}}, nil,
		&CallOptions{ReasoningEffort: "bogus"})
	if junk.Thinking != nil {
		t.Errorf("无法识别的 effort 仍然发出了 thinking = %+v, want 整个字段缺席", junk.Thinking)
	}
	if junk.Temperature != 0 {
		t.Errorf("没开 thinking 时 temperature 被改成 %v, want 不动", junk.Temperature)
	}

	// 完全没设 effort → 不发 thinking。
	off := e.buildRequestBody([]Message{{Role: RoleUser, Content: "x"}}, nil, nil)
	if off.Thinking != nil {
		t.Errorf("未配置 effort 时 thinking = %+v, want nil", off.Thinking)
	}
}

// ---------- 响应与流解析 ----------

func TestAnthropicConvertResponse(t *testing.T) {
	e := anthropicEngine("http://127.0.0.1:1", config.LLMConfig{})
	var ar anthropicResponse
	body := `{"content":[
		{"type":"thinking","thinking":"先想想"},
		{"type":"text","text":"第一段"},
		{"type":"text","text":"第二段"},
		{"type":"redacted_thinking","data":"xx"},
		{"type":"tool_use","id":"tu-9","name":"run_bash","input":{"cmd":"ls"}}],
		"usage":{"input_tokens":100,"output_tokens":20,"cache_read_input_tokens":60,"cache_creation_input_tokens":5}}`
	if err := json.Unmarshal([]byte(body), &ar); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}

	resp := e.convertResponse(&ar)
	if len(resp.Choices) != 1 {
		t.Fatalf("choices = %d, want 1", len(resp.Choices))
	}
	c := resp.Choices[0]
	if c.Content != "第一段第二段" {
		t.Errorf("content = %q, want 两段 text 拼接", c.Content)
	}
	if !strings.Contains(c.Reasoning, "先想想") || !strings.Contains(c.Reasoning, "[redacted thinking]") {
		t.Errorf("reasoning = %q, want 同时含 thinking 与 redacted 占位", c.Reasoning)
	}
	if len(c.ToolCalls) != 1 || c.ToolCalls[0].ID != "tu-9" || c.ToolCalls[0].Function.Name != "run_bash" {
		t.Fatalf("tool_calls = %+v, want 一条 tu-9", c.ToolCalls)
	}
	if c.ToolCalls[0].Type != "function" {
		t.Errorf("tool_call.type = %q, want function", c.ToolCalls[0].Type)
	}
	if c.ToolCalls[0].Function.Arguments != `{"cmd":"ls"}` {
		t.Errorf("arguments = %q, want input 原样序列化", c.ToolCalls[0].Function.Arguments)
	}

	// 账目：Anthropic 的 input_tokens 不含缓存，TotalInputTokens 必须三者相加。
	u := resp.Usage
	if u.PromptTokens != 100 || u.CompletionTokens != 20 {
		t.Errorf("usage = %+v, want 100/20", u)
	}
	if u.TotalTokens != 120 {
		t.Errorf("total_tokens = %d, want 120（input+output）", u.TotalTokens)
	}
	if u.TotalInputTokens != 165 {
		t.Errorf("TotalInputTokens = %d, want 165=100+60+5（与 OpenAI 口径不同，混用会让 TUI 少算缓存命中）", u.TotalInputTokens)
	}
	if u.CacheReadInputTokens != 60 || u.CacheCreationInputTokens != 5 {
		t.Errorf("cache 字段 = %+v, want 60/5", u)
	}
}

// tool_use 的 input 为空时必须补 "{}"：Agent 侧按 JSON 解析 arguments，
// 空串会让工具分发直接报错。
func TestAnthropicConvertResponse_EmptyToolInput(t *testing.T) {
	e := anthropicEngine("http://127.0.0.1:1", config.LLMConfig{})
	var ar anthropicResponse
	if err := json.Unmarshal([]byte(`{"content":[{"type":"tool_use","id":"t","name":"thinking"}],"usage":{}}`), &ar); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	resp := e.convertResponse(&ar)
	if got := resp.Choices[0].ToolCalls[0].Function.Arguments; got != "{}" {
		t.Errorf("arguments = %q, want {}", got)
	}
}

func TestAnthropicParseStreamResponse(t *testing.T) {
	e := anthropicEngine("http://127.0.0.1:1", config.LLMConfig{})
	sse := strings.Join([]string{
		"event: message_start",
		`data: {"type":"message_start","message":{"usage":{"input_tokens":42}}}`,
		"",
		"event: ping",
		`data: {"type":"ping"}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"thinking","thinking":""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"thinking_delta","thinking":"想了想"}}`,
		"",
		"event: content_block_start",
		`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"tu-3","name":"read_file","input":{}}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\""}}`,
		"",
		"event: content_block_delta",
		`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":": \"a.go\"}"}}`,
		"",
		"event: message_delta",
		`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9,"cache_read_input_tokens":7}}`,
		"",
		"event: message_stop",
		`data: {"type":"message_stop"}`,
		"",
	}, "\n")

	var streamed strings.Builder
	resp, err := e.parseStreamResponse(context.Background(), strings.NewReader(sse),
		func(_ context.Context, chunk []byte) error {
			streamed.Write(chunk)
			return nil
		})
	if err != nil {
		t.Fatalf("parseStreamResponse: %v", err)
	}

	c := resp.Choices[0]
	if c.Content != "Hello" {
		t.Errorf("content = %q, want Hello", c.Content)
	}
	if got := streamed.String(); got != "Hello" {
		t.Errorf("handler 收到 = %q, want Hello（TUI 逐字渲染依赖它）", got)
	}
	if c.Reasoning != "想了想" {
		t.Errorf("reasoning = %q, want 想了想", c.Reasoning)
	}
	if len(c.ToolCalls) != 1 {
		t.Fatalf("tool_calls = %+v, want 按 index 拼成 1 条", c.ToolCalls)
	}
	if c.ToolCalls[0].ID != "tu-3" || c.ToolCalls[0].Function.Name != "read_file" {
		t.Errorf("tool_call = %+v, want content_block_start 给的 id/name", c.ToolCalls[0])
	}
	if c.ToolCalls[0].Function.Arguments != `{"path": "a.go"}` {
		t.Errorf("arguments = %q, want 两段 partial_json 拼出的完整 JSON", c.ToolCalls[0].Function.Arguments)
	}

	// message_start 的 input 与 message_delta 的 output/cache 必须合成一份完整账目。
	u := resp.Usage
	if u.PromptTokens != 42 || u.CompletionTokens != 9 || u.TotalTokens != 51 {
		t.Errorf("usage = %+v, want 42/9/51", u)
	}
	if u.CacheReadInputTokens != 7 {
		t.Errorf("cache_read = %d, want 7", u.CacheReadInputTokens)
	}
	if u.TotalInputTokens != 49 {
		t.Errorf("TotalInputTokens = %d, want 49=42+7", u.TotalInputTokens)
	}
}

// 没有 handler 时（非流式内部路径）解析仍要工作；未知事件必须被忽略而不是报错。
func TestAnthropicParseStreamResponse_NoHandlerAndUnknownEvents(t *testing.T) {
	e := anthropicEngine("http://127.0.0.1:1", config.LLMConfig{})
	sse := "event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":1}}}` + "\n\n" +
		"event: some_future_event\n" +
		`data: {"type":"some_future_event","whatever":true}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}` + "\n\n"

	resp, err := e.parseStreamResponse(context.Background(), strings.NewReader(sse), nil)
	if err != nil {
		t.Fatalf("未知 SSE 事件导致失败: %v", err)
	}
	if resp.Choices[0].Content != "ok" {
		t.Errorf("content = %q, want ok", resp.Choices[0].Content)
	}
	if resp.Usage == nil || resp.Usage.PromptTokens != 1 {
		t.Errorf("usage = %+v, want 带 message_start 的 input_tokens", resp.Usage)
	}
}

// ---------- HTTP 往返 ----------

// 一次完整往返：路径、鉴权头、响应体解析成 Choices。
func TestAnthropicEngine_RoundTrip(t *testing.T) {
	var got capturedRequest
	srv, _ := newFakeLLM(t, func(req capturedRequest) (int, string) {
		got = req
		return 200, okAnthropic
	})
	e := anthropicEngine(srv.URL, config.LLMConfig{})

	resp, err := e.GenerateContent(context.Background(), []Message{{Role: RoleUser, Content: "hi"}}, nil, nil)
	if err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	if got.Path != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages", got.Path)
	}
	if resp.Usage == nil || resp.Usage.TotalInputTokens != 10 {
		t.Errorf("usage = %+v, want 从 response_item 解析出的 10", resp.Usage)
	}
	if len(resp.Choices) != 1 || resp.Choices[0].Content != "hello" {
		t.Errorf("choices = %+v, want 一条 hello", resp.Choices)
	}
}

func TestAnthropicEngine_Headers(t *testing.T) {
	var got capturedRequest
	srv, _ := newFakeLLM(t, func(req capturedRequest) (int, string) {
		got = req
		return 200, okAnthropic
	})
	// 构造方给的 baseURL 以 / 结尾时必须吃掉，否则拼成 //v1/messages。
	e := NewAnthropicEngine(testProvider(srv.URL+"/v1/", "sk-test", "claude-sonnet-4"), config.LLMConfig{})

	if _, err := e.GenerateContent(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil, nil); err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	if got.Path != "/v1/messages" {
		t.Errorf("path = %q, want /v1/messages（尾斜杠没被吃掉）", got.Path)
	}
	if got.Header.Get("x-api-key") != "sk-test" {
		t.Errorf("x-api-key = %q, want sk-test", got.Header.Get("x-api-key"))
	}
	if v := got.Header.Get("anthropic-version"); v != "2023-06-01" {
		t.Errorf("anthropic-version = %q, want 2023-06-01", v)
	}
	if got.Header.Get("anthropic-beta") != "" {
		t.Error("未开 thinking 时不该带 anthropic-beta 头")
	}
	if ct := got.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("content-type = %q", ct)
	}
	if body := got.Body; body["stream"] == true {
		t.Error("非流式调用却发了 stream=true")
	}
}

// interleaved thinking 是 beta 能力：开了 thinking 必须同时带上 beta 头，
// 否则 provider 忽略 thinking 块，表现为"配了 high 但没有思考过程"。
func TestAnthropicEngine_ThinkingSendsBetaHeader(t *testing.T) {
	var got capturedRequest
	srv, _ := newFakeLLM(t, func(req capturedRequest) (int, string) {
		got = req
		return 200, okAnthropic
	})
	provider := testProvider(srv.URL+"/v1", "k", "claude-x")
	provider.ReasoningEffort = "high"
	e := NewAnthropicEngine(provider, config.LLMConfig{})

	if _, err := e.GenerateContent(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil, nil); err != nil {
		t.Fatalf("GenerateContent: %v", err)
	}
	if v := got.Header.Get("anthropic-beta"); !strings.Contains(v, "interleaved-thinking") {
		t.Errorf("anthropic-beta = %q, want 含 interleaved-thinking", v)
	}
	th, _ := got.Body["thinking"].(map[string]any)
	if th["type"] != "enabled" {
		t.Errorf("请求体 thinking = %v, want enabled", got.Body["thinking"])
	}
}

func TestAnthropicEngine_StreamingRequestShape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if strings.Contains(string(raw), `"stream":true`) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(200)
			_, _ = io.WriteString(w, "event: content_block_delta\n"+
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}`+"\n\n")
			return
		}
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":{"type":"invalid_request_error","message":"not streaming"}}`)
	}))
	defer srv.Close()

	e := anthropicEngine(srv.URL, config.LLMConfig{})
	var streamed strings.Builder
	resp, err := e.GenerateContent(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil,
		&CallOptions{StreamHandler: func(_ context.Context, c []byte) error {
			streamed.Write(c)
			return nil
		}})
	if err != nil {
		t.Fatalf("流式调用失败: %v", err)
	}
	if streamed.String() != "hi" || resp.Choices[0].Content != "hi" {
		t.Errorf("streamed=%q content=%q, want 都是 hi", streamed.String(), resp.Choices[0].Content)
	}
}

// 错误响应体必须带出 provider 的 type/message；400 一类客户端错误不重试。
func TestAnthropicEngine_ErrorPaths(t *testing.T) {
	t.Run("400 原样冒泡且不重试", func(t *testing.T) {
		srv, count := newFakeLLM(t, func(capturedRequest) (int, string) {
			return 400, `{"type":"error","error":{"type":"invalid_request_error","message":"messages: at least one message is required"}}`
		})
		e := anthropicEngine(srv.URL, config.LLMConfig{MaxRetries: 5, Timeout: time.Second})

		_, err := e.GenerateContent(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil, nil)
		if err == nil {
			t.Fatal("400 被当成成功")
		}
		if !strings.Contains(err.Error(), "at least one message is required") {
			t.Errorf("err = %v, want 带 provider 原文", err)
		}
		if !strings.Contains(err.Error(), "invalid_request_error") {
			t.Errorf("err = %v, want 带 error type", err)
		}
		if n := int(count.Load()); n != 1 {
			t.Errorf("请求次数 = %d, want 1（客户端错误重试无意义）", n)
		}
	})

	t.Run("错误体不是 JSON 时退回原文", func(t *testing.T) {
		srv, _ := newFakeLLM(t, func(capturedRequest) (int, string) {
			return 403, `<html>Forbidden</html>`
		})
		e := anthropicEngine(srv.URL, config.LLMConfig{})
		_, err := e.GenerateContent(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil, nil)
		if err == nil || !strings.Contains(err.Error(), "Forbidden") || !strings.Contains(err.Error(), "403") {
			t.Errorf("err = %v, want 带状态码与原文", err)
		}
	})

	t.Run("5xx 退避重试且 SDK 不叠加", func(t *testing.T) {
		srv, count := newFakeLLM(t, func(capturedRequest) (int, string) {
			return 503, `{"error":{"type":"overloaded_error","message":"Overloaded"}}`
		})
		e := anthropicEngine(srv.URL, config.LLMConfig{Timeout: 120 * time.Millisecond})

		start := time.Now()
		_, err := e.GenerateContent(context.Background(), []Message{{Role: RoleUser, Content: "x"}}, nil, nil)
		if err == nil {
			t.Fatal("持续 503 却返回成功")
		}
		if n := int(count.Load()); n != 1 {
			t.Errorf("服务端收到 %d 次请求, want 1（退避被 ctx 打断；次数>1 说明重试计数叠加了）", n)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("耗时 %v, want 受调用超时钳制", elapsed)
		}
	})
}

func TestIsRetriableHTTPStatus(t *testing.T) {
	retriable := map[int]bool{
		400: false, 401: false, 403: false, 404: false, 409: false, 422: false,
		429: true, 500: true, 502: true, 503: true, 504: true, 599: true, 600: false,
		200: false,
	}
	for status, want := range retriable {
		if got := isRetriableHTTPStatus(status); got != want {
			t.Errorf("isRetriableHTTPStatus(%d) = %v, want %v", status, got, want)
		}
	}
}
