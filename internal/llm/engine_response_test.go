package llm

import (
	"errors"
	"strings"
	"testing"
)

// Response 侧的契约：循环拿到的每一条 turn 都要能问出"为什么停"，
// 而拿不到 turn 时必须是错误 —— 那两处循环是直接 resp.Choices[0] 的。

func TestFirstChoiceRejectsEmptyAndNil(t *testing.T) {
	tests := []struct {
		name string
		resp *Response
		want error
	}{
		{"nil response", nil, ErrEmptyCompletion},
		{"no choices", &Response{}, ErrEmptyCompletion},
		{"refused", &Response{Choices: []Choice{{FinishReason: FinishContentFilter}}}, ErrRefusedCompletion},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := tt.resp.FirstChoice(); !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want errors.Is(%v)", err, tt.want)
			}
		})
	}

	c, err := (&Response{Choices: []Choice{{Content: "x", FinishReason: FinishStop}}}).FirstChoice()
	if err != nil || c.Content != "x" {
		t.Errorf("FirstChoice = %+v, %v; want 那条 choice 且无错误", c, err)
	}
}

// 两套词表在引擎边界归一；认不出的原样留着，既不猜成截断也不猜成说完。
func TestNormalizeFinishReason(t *testing.T) {
	tests := []struct {
		raw  string
		want FinishReason
	}{
		{"", ""},
		{"stop", FinishStop},
		{"end_turn", FinishStop},
		{"length", FinishLength},
		{"max_tokens", FinishLength},
		{"tool_calls", FinishToolCalls},
		{"tool_use", FinishToolCalls},
		{"content_filter", FinishContentFilter},
		{"refusal", FinishContentFilter},
		{"pause_turn", FinishPauseTurn},
		{"Stop", FinishStop},
		{"some_gateway_value", FinishReason("some_gateway_value")},
	}
	for _, tt := range tests {
		if got := normalizeFinishReason(tt.raw); got != tt.want {
			t.Errorf("normalizeFinishReason(%q) = %q, want %q", tt.raw, got, tt.want)
		}
	}
	if !FinishLength.Truncated() || FinishStop.Truncated() || FinishReason("").Truncated() {
		t.Error("Truncated() 的口径错了：只有 length 算截断")
	}
	if !FinishContentFilter.Refused() || FinishStop.Refused() {
		t.Error("Refused() 的口径错了：只有 content_filter 算拒答")
	}
}

// 标记只加在交回给模型的那一份上：provider 原字节留给日志与指纹计算。
func TestContentWithNoticeMarksOnlyTruncatedTurns(t *testing.T) {
	cut := Choice{Content: "半句", FinishReason: FinishLength}
	if got := cut.ContentWithNotice(); !strings.HasPrefix(got, "半句") || !strings.Contains(got, "max_tokens") {
		t.Errorf("截断轮交回 = %q, want 原正文 + 可见标记", got)
	}
	whole := Choice{Content: "半句", FinishReason: FinishStop}
	if got := whole.ContentWithNotice(); got != "半句" {
		t.Errorf("正常轮交回 = %q, want 逐字不变", got)
	}

	msg := cut.AssistantMessage()
	if msg.Role != RoleAssistant || !strings.Contains(msg.Content, "max_tokens") {
		t.Errorf("assistant turn = %+v, want 带标记", msg)
	}
	if msg.ToolCalls != nil {
		t.Errorf("tool_calls = %v, want 原样透传", msg.ToolCalls)
	}
}

// 残缺参数的判据必须与工具层的裸字符串兜底同形（internal/tools adapter.go 的
// Adapter.Call：JSON 解析失败不报错，回落成 {"input": 原始串}），
// 否则"回执说明截断"会挂在一条生产永远不成立的条件上。
func TestToolCallNoteOnlyForCutOffIncompleteArgs(t *testing.T) {
	call := func(args string) Choice {
		return Choice{FinishReason: FinishLength, ToolCalls: []ToolCall{{Function: FunctionCall{Arguments: args}}}}
	}
	cut := call(`{"path":"a.go","content":"func ma`)
	if got := cut.ToolCallNote(cut.ToolCalls[0]); got == "" {
		t.Error("截断轮里的残缺参数没有收到说明")
	}
	complete := call(`{"path":"a.go"}`)
	if got := complete.ToolCallNote(complete.ToolCalls[0]); got != "" {
		t.Errorf("完整参数的回执多了噪声: %q", got)
	}
	// 没被截断时，同一份残缺参数轮不到截断说明（那是工具层自己的错误）。
	broken := Choice{FinishReason: FinishStop, ToolCalls: []ToolCall{{Function: FunctionCall{Arguments: `{"text":"半条`}}}}
	if got := broken.ToolCallNote(broken.ToolCalls[0]); got != "" {
		t.Errorf("非截断轮不该出现截断说明: %q", got)
	}
	bare := call("hello")
	if got := bare.ToolCallNote(bare.ToolCalls[0]); got == "" {
		t.Error("裸字符串入参同样走工具层兜底，截断轮里必须点名")
	}
}

// validateResponse 是引擎边界的闸：拒答变错误，截断留一次可见的日志。
func TestValidateResponsePassesNormalAndFailsRefusal(t *testing.T) {
	if err := validateResponse("m", &Response{Choices: []Choice{{Content: "ok", FinishReason: FinishStop}}}); err != nil {
		t.Errorf("正常响应被闸住: %v", err)
	}
	if err := validateResponse("m", &Response{Choices: []Choice{{FinishReason: FinishLength}}}); err != nil {
		t.Errorf("截断响应应当继续（正文留给模型），不该报错: %v", err)
	}
	if err := validateResponse("m", &Response{}); !errors.Is(err, ErrEmptyCompletion) {
		t.Errorf("err = %v, want errors.Is(ErrEmptyCompletion)", err)
	}
}
