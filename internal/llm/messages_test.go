package llm

import (
	"testing"
)

// helper: create a Message shortcut
func userMsg(content string) Message { return Message{Role: RoleUser, Content: content} }
func assistantMsg(content string, tc ...ToolCall) Message {
	return Message{Role: RoleAssistant, Content: content, ToolCalls: tc}
}
func toolMsg(id string, content string) Message {
	return Message{Role: RoleTool, ToolCallID: id, Content: content}
}
func systemMsg(content string) Message {
	return Message{Role: RoleSystem, Content: content}
}

func msgsEqual(a, b []Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Role != b[i].Role || a[i].Content != b[i].Content || a[i].ToolCallID != b[i].ToolCallID || a[i].Reasoning != b[i].Reasoning {
			return false
		}
		if len(a[i].ToolCalls) != len(b[i].ToolCalls) {
			return false
		}
		for j := range a[i].ToolCalls {
			if a[i].ToolCalls[j].ID != b[i].ToolCalls[j].ID ||
				a[i].ToolCalls[j].Type != b[i].ToolCalls[j].Type ||
				a[i].ToolCalls[j].Function.Name != b[i].ToolCalls[j].Function.Name ||
				a[i].ToolCalls[j].Function.Arguments != b[i].ToolCalls[j].Function.Arguments {
				return false
			}
		}
	}
	return true
}

func assistantReasoning(content, reasoning string) Message {
	m := assistantMsg(content)
	m.Reasoning = reasoning
	return m
}

var (
	tc1 = ToolCall{ID: "tc1", Type: "function", Function: FunctionCall{Name: "fn", Arguments: "{}"}}
	tc2 = ToolCall{ID: "tc2", Type: "function", Function: FunctionCall{Name: "fn2", Arguments: "{}"}}
)

// TestNormalizeMessages 是四阶段管线的行为表：丢空 → 补 tool_call 配对 → 合并连续 assistant → 校正开头。
func TestNormalizeMessages(t *testing.T) {
	tests := []struct {
		name  string
		input []Message
		want  []Message
	}{
		{
			name: "normal_flow_untouched",
			input: []Message{
				systemMsg("you are helpful"),
				userMsg("hello"),
				assistantMsg("let me check", tc1),
				toolMsg("tc1", "result"),
				assistantMsg("done"),
			},
			want: []Message{
				systemMsg("you are helpful"),
				userMsg("hello"),
				assistantMsg("let me check", tc1),
				toolMsg("tc1", "result"),
				assistantMsg("done"),
			},
		},
		{
			name:  "empty_list",
			input: []Message{},
			want:  []Message{},
		},
		{
			name:  "only_system",
			input: []Message{systemMsg("you are helpful")},
			want:  []Message{systemMsg("you are helpful")},
		},
		{
			name:  "every_system_message_kept",
			input: []Message{systemMsg("sys1"), systemMsg("sys2"), userMsg("hi")},
			want:  []Message{systemMsg("sys1"), systemMsg("sys2"), userMsg("hi")},
		},
		{
			name:  "user_only",
			input: []Message{userMsg("hi")},
			want:  []Message{userMsg("hi")},
		},
		{
			name:  "merge_two_consecutive_assistants",
			input: []Message{userMsg("hi"), assistantMsg("A"), assistantMsg("B")},
			want:  []Message{userMsg("hi"), assistantMsg("A\n\nB")},
		},
		{
			name:  "merge_three_consecutive_assistants",
			input: []Message{userMsg("hi"), assistantMsg("A"), assistantMsg("B"), assistantMsg("C")},
			want:  []Message{userMsg("hi"), assistantMsg("A\n\nB\n\nC")},
		},
		{
			name:  "incomplete_tool_call_strips_tool_calls",
			input: []Message{userMsg("hi"), assistantMsg("try", tc1)},
			want:  []Message{userMsg("hi"), assistantMsg("try")},
		},
		{
			name:  "incomplete_group_merges_into_preceding_assistant",
			input: []Message{userMsg("hi"), assistantMsg("first"), assistantMsg("try", tc1)},
			want:  []Message{userMsg("hi"), assistantMsg("first\n\ntry")},
		},
		{
			name: "multiple_incomplete_groups_merge_to_one",
			input: []Message{
				userMsg("hi"),
				assistantMsg("A", tc1),
				assistantMsg("B", tc2),
				assistantMsg("C"),
			},
			want: []Message{userMsg("hi"), assistantMsg("A\n\nB\n\nC")},
		},
		{
			name:  "orphan_tool_message_dropped",
			input: []Message{userMsg("hi"), toolMsg("orphan", "result"), assistantMsg("hi")},
			want:  []Message{userMsg("hi"), assistantMsg("hi")},
		},
		{
			name:  "leading_tool_message_dropped",
			input: []Message{toolMsg("x", "result"), userMsg("hello")},
			want:  []Message{userMsg("hello")},
		},
		{
			name:  "empty_assistant_dropped",
			input: []Message{userMsg("hi"), assistantMsg(""), assistantMsg("hi")},
			want:  []Message{userMsg("hi"), assistantMsg("hi")},
		},
		{
			name: "partial_tool_call_responses_stripped",
			input: []Message{
				userMsg("hi"),
				assistantMsg("call both", tc1, tc2),
				toolMsg("tc1", "result1"), // tc2 的响应缺失 → 整组算不完整
			},
			want: []Message{userMsg("hi"), assistantMsg("call both")},
		},
		{
			name: "complete_group_survives_adjacent_assistant",
			input: []Message{
				userMsg("hi"),
				assistantMsg("", tc1),
				toolMsg("tc1", "result"),
				assistantMsg("done"),
			},
			want: []Message{
				userMsg("hi"),
				assistantMsg("", tc1),
				toolMsg("tc1", "result"),
				assistantMsg("done"),
			},
		},
		{
			// 合并连续 assistant 时 Reasoning 也按 \n\n 拼接（messages.go 的 reasoning 分支）。
			name: "reasoning_merged_with_content",
			input: []Message{
				userMsg("hi"),
				assistantReasoning("A", "think A"),
				assistantReasoning("B", "think B"),
			},
			want: []Message{userMsg("hi"), assistantReasoning("A\n\nB", "think A\n\nthink B")},
		},
		{
			name: "reasoning_of_current_taken_when_previous_has_none",
			input: []Message{
				userMsg("hi"),
				assistantMsg("A"),
				assistantReasoning("B", "think B"),
			},
			want: []Message{userMsg("hi"), assistantReasoning("A\n\nB", "think B")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeMessages(tt.input)
			if !msgsEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// 幂等性是四阶段管线的核心不变量：二次归一化不能再改动任何一条消息。
func TestNormalizeMessages_Idempotency(t *testing.T) {
	testCases := [][]Message{
		{},
		{systemMsg("sys")},
		{userMsg("hi"), assistantMsg("A"), assistantMsg("B")},
		{userMsg("hi"), assistantMsg("try", tc1)},
		{userMsg("hi"), toolMsg("orphan", "result"), assistantMsg("hi")},
		{userMsg("hi"), assistantMsg("", tc1), assistantMsg("B", tc2), assistantMsg("C")},
		{toolMsg("x", "result"), userMsg("hello")},
		{userMsg("hi"), assistantReasoning("A", "r1"), assistantReasoning("B", "r2")},
	}

	for i, input := range testCases {
		once := NormalizeMessages(input)
		if twice := NormalizeMessages(once); !msgsEqual(once, twice) {
			t.Errorf("idempotency test %d: once %+v, twice %+v", i, once, twice)
		}
	}
}

func TestMergeConsecutiveAssistants(t *testing.T) {
	tests := []struct {
		name  string
		input []Message
		want  []Message
	}{
		{
			name:  "empty",
			input: []Message{},
			want:  []Message{},
		},
		{
			name:  "single_kept",
			input: []Message{assistantMsg("alone")},
			want:  []Message{assistantMsg("alone")},
		},
		{
			name:  "non_adjacent_not_merged",
			input: []Message{assistantMsg("A"), userMsg("hi"), assistantMsg("B")},
			want:  []Message{assistantMsg("A"), userMsg("hi"), assistantMsg("B")},
		},
		{
			name:  "two_text_only_merged",
			input: []Message{assistantMsg("X"), assistantMsg("Y")},
			want:  []Message{assistantMsg("X\n\nY")},
		},
		{
			// 前一条带 tool calls、当前纯文本：文本并入前一条，tool calls 保留。
			name:  "previous_with_tool_calls_absorbs_text",
			input: []Message{assistantMsg("before tool", tc1), assistantMsg("after text")},
			want:  []Message{assistantMsg("before tool\n\nafter text", tc1)},
		},
		{
			// 当前带 tool calls：整条丢弃，前一条原样保留。
			name:  "current_with_tool_calls_dropped",
			input: []Message{assistantMsg("text only"), assistantMsg("with tool call", tc1)},
			want:  []Message{assistantMsg("text only")},
		},
		{
			name:  "reasoning_concatenated",
			input: []Message{assistantReasoning("A", "r1"), assistantReasoning("B", "r2")},
			want:  []Message{assistantReasoning("A\n\nB", "r1\n\nr2")},
		},
		{
			name:  "reasoning_taken_from_current_only",
			input: []Message{assistantMsg("A"), assistantReasoning("B", "r2")},
			want:  []Message{assistantReasoning("A\n\nB", "r2")},
		},
		{
			name:  "empty_current_content_keeps_previous",
			input: []Message{assistantMsg("A"), assistantMsg("")},
			want:  []Message{assistantMsg("A")},
		},
		{
			name:  "empty_previous_content_taken_from_current",
			input: []Message{assistantMsg(""), assistantMsg("B")},
			want:  []Message{assistantMsg("B")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MergeConsecutiveAssistants(tt.input)
			if !msgsEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestDropEmptyMessages(t *testing.T) {
	tests := []struct {
		name  string
		input []Message
		want  []Message
	}{
		{
			name:  "system_messages_always_kept",
			input: []Message{systemMsg(""), systemMsg("hi")},
			want:  []Message{systemMsg(""), systemMsg("hi")},
		},
		{
			name:  "empty_assistant_dropped",
			input: []Message{assistantMsg("")},
			want:  []Message{},
		},
		{
			name:  "empty_role_dropped",
			input: []Message{{Content: "no role"}, userMsg("hi")},
			want:  []Message{userMsg("hi")},
		},
		{
			name:  "tool_call_only_assistant_kept",
			input: []Message{assistantMsg("", tc1), toolMsg("tc1", "r")},
			want:  []Message{assistantMsg("", tc1), toolMsg("tc1", "r")},
		},
		{
			name:  "tool_message_with_id_kept",
			input: []Message{userMsg("hi"), toolMsg("tc1", "")},
			want:  []Message{userMsg("hi"), toolMsg("tc1", "")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dropEmptyMessages(tt.input)
			if !msgsEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestEnsureValidStart(t *testing.T) {
	tests := []struct {
		name  string
		input []Message
		want  []Message
	}{
		{
			name:  "leading_tool_removed",
			input: []Message{toolMsg("x", "result"), userMsg("hello")},
			want:  []Message{userMsg("hello")},
		},
		{
			name:  "first_system_kept",
			input: []Message{systemMsg("sys"), userMsg("hello")},
			want:  []Message{systemMsg("sys"), userMsg("hello")},
		},
		{
			name:  "assistant_start_is_valid",
			input: []Message{assistantMsg("hi")},
			want:  []Message{assistantMsg("hi")},
		},
		{
			name:  "empty",
			input: []Message{},
			want:  []Message{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ensureValidStart(tt.input)
			if !msgsEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRepairToolCallPairs(t *testing.T) {
	tests := []struct {
		name  string
		input []Message
		want  []Message
	}{
		{
			name:  "empty",
			input: []Message{},
			want:  []Message{},
		},
		{
			name:  "complete_pair_kept",
			input: []Message{userMsg("hi"), assistantMsg("", tc1), toolMsg("tc1", "result")},
			want:  []Message{userMsg("hi"), assistantMsg("", tc1), toolMsg("tc1", "result")},
		},
		{
			name:  "incomplete_pair_stripped",
			input: []Message{userMsg("hi"), assistantMsg("try", tc1)},
			want:  []Message{userMsg("hi"), assistantMsg("try")},
		},
		{
			name:  "orphan_tool_dropped",
			input: []Message{userMsg("hi"), toolMsg("orphan", "result"), assistantMsg("hi")},
			want:  []Message{userMsg("hi"), assistantMsg("hi")},
		},
		{
			name:  "incomplete_without_content_dropped",
			input: []Message{userMsg("hi"), assistantMsg("", tc1), assistantMsg("after")},
			want:  []Message{userMsg("hi"), assistantMsg("after")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := repairToolCallPairs(tt.input)
			if !msgsEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

// Benchmark tests
func BenchmarkNormalizeMessages_NormalFlow(b *testing.B) {
	input := []Message{
		systemMsg("you are helpful"),
		userMsg("hello"),
		assistantMsg("hi there"),
		toolMsg("tc1", "result"),
		assistantMsg("done"),
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		NormalizeMessages(input)
	}
}

func BenchmarkNormalizeMessages_MergeAssistants(b *testing.B) {
	input := []Message{
		userMsg("hi"),
		assistantMsg("A"),
		assistantMsg("B"),
		assistantMsg("C"),
		assistantMsg("D"),
		assistantMsg("E"),
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		NormalizeMessages(input)
	}
}

func BenchmarkMergeConsecutiveAssistants(b *testing.B) {
	input := []Message{
		assistantMsg("A"),
		assistantMsg("B"),
		assistantMsg("C"),
		assistantMsg("D"),
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		MergeConsecutiveAssistants(input)
	}
}
