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

func TestNormalizeMessages_NormalFlow(t *testing.T) {
	// 1. 正常流程不受影响：[system, user, assistant(TC), tool, assistant] → 原样返回
	tc1 := []Message{
		systemMsg("you are helpful"),
		userMsg("hello"),
		assistantMsg("let me check", ToolCall{ID: "tc1", Type: "function"}),
		toolMsg("tc1", "result"),
		assistantMsg("done"),
	}
	got := NormalizeMessages(tc1)
	if !msgsEqual(got, tc1) {
		t.Errorf("normal flow: got %d msgs, want %d", len(got), len(tc1))
		for i, g := range got {
			t.Logf("got[%d] = Role=%s Content=%q", i, g.Role, g.Content)
		}
		for i, w := range tc1 {
			t.Logf("want[%d] = Role=%s Content=%q", i, w.Role, w.Content)
		}
	}
}

func TestNormalizeMessages_TwoConsecutiveAssistantsMerged(t *testing.T) {
	// 2. 两个连续assistant被合并：[user, assistant("A"), assistant("B")] → [user, assistant("A\n\nB")]
	input := []Message{userMsg("hi"), assistantMsg("A"), assistantMsg("B")}
	want := []Message{userMsg("hi"), assistantMsg("A\n\nB")}
	got := NormalizeMessages(input)
	if !msgsEqual(got, want) {
		t.Errorf("merge two assistants: got %+v, want %+v", got, want)
	}
}

func TestNormalizeMessages_IncompleteToolCall(t *testing.T) {
	// 3. 不完整tool_call组：[user, assistant(Content="try", ToolCalls=[tc1])]（无tool response）
	// → [user, assistant(Content="try")]（ToolCalls被strip）
	tc1 := ToolCall{ID: "tc1", Type: "function", Function: FunctionCall{Name: "fn", Arguments: "{}"}}
	input := []Message{userMsg("hi"), assistantMsg("try", tc1)}
	want := []Message{userMsg("hi"), assistantMsg("try")}
	got := NormalizeMessages(input)
	if !msgsEqual(got, want) {
		t.Errorf("incomplete tool_call: got %+v, want %+v", got, want)
	}
}

func TestNormalizeMessages_IncompleteWithPrecedingAssistant(t *testing.T) {
	// 4. 不完整组+前一个assistant：
	// [user, assistant("first"), assistant(Content="try", ToolCalls=[tc1])]
	// → [user, assistant("first\n\ntry")]
	tc1 := ToolCall{ID: "tc1", Type: "function", Function: FunctionCall{Name: "fn", Arguments: "{}"}}
	input := []Message{userMsg("hi"), assistantMsg("first"), assistantMsg("try", tc1)}
	want := []Message{userMsg("hi"), assistantMsg("first\n\ntry")}
	got := NormalizeMessages(input)
	if !msgsEqual(got, want) {
		t.Errorf("incomplete with preceding: got %+v, want %+v", got, want)
	}
}

func TestNormalizeMessages_OrphanToolDropped(t *testing.T) {
	// 5. 孤立tool消息被删除：
	// [user, tool(ToolCallID="orphan"), assistant("hi")] → [user, assistant("hi")]
	input := []Message{userMsg("hi"), toolMsg("orphan", "result"), assistantMsg("hi")}
	want := []Message{userMsg("hi"), assistantMsg("hi")}
	got := NormalizeMessages(input)
	if !msgsEqual(got, want) {
		t.Errorf("orphan tool: got %+v, want %+v", got, want)
	}
}

func TestNormalizeMessages_CompleteToolCallUnaffected(t *testing.T) {
	// 6. 完整tool_call组不受影响：
	// [user, assistant(ToolCalls=[tc1]), tool(ToolCallID="tc1"), assistant("done")]
	tc1 := ToolCall{ID: "tc1", Type: "function", Function: FunctionCall{Name: "fn", Arguments: "{}"}}
	input := []Message{
		userMsg("hi"),
		assistantMsg("", tc1),
		toolMsg("tc1", "result"),
		assistantMsg("done"),
	}
	got := NormalizeMessages(input)
	if !msgsEqual(got, input) {
		t.Errorf("complete tool_call: got %+v, want %+v", got, input)
	}
}

func TestNormalizeMessages_MultipleIncompleteGroups(t *testing.T) {
	// 7. 多个不完整组连续处理：
	// [user, assistant("A",ToolCalls=[tc1]), assistant("B",ToolCalls=[tc2]), assistant("C")]
	// → [user, assistant("A\n\nB\n\nC")]
	tc1 := ToolCall{ID: "tc1", Type: "function", Function: FunctionCall{Name: "fn", Arguments: "{}"}}
	tc2 := ToolCall{ID: "tc2", Type: "function", Function: FunctionCall{Name: "fn2", Arguments: "{}"}}
	input := []Message{
		userMsg("hi"),
		assistantMsg("A", tc1),
		assistantMsg("B", tc2),
		assistantMsg("C"),
	}
	want := []Message{userMsg("hi"), assistantMsg("A\n\nB\n\nC")}
	got := NormalizeMessages(input)
	if !msgsEqual(got, want) {
		t.Errorf("multiple incomplete: got %+v, want %+v", got, want)
	}
}

func TestNormalizeMessages_EmptyMessageRemoved(t *testing.T) {
	// 8. 空消息被删除：
	// [user, assistant(Content=""), assistant("hi")] → [user, assistant("hi")]
	input := []Message{userMsg("hi"), assistantMsg(""), assistantMsg("hi")}
	want := []Message{userMsg("hi"), assistantMsg("hi")}
	got := NormalizeMessages(input)
	if !msgsEqual(got, want) {
		t.Errorf("empty message: got %+v, want %+v", got, want)
	}
}

func TestNormalizeMessages_LeadingToolDropped(t *testing.T) {
	// 9. 开头是tool消息：[tool(ToolCallID="x"), user("hello")] → [user("hello")]
	input := []Message{toolMsg("x", "result"), userMsg("hello")}
	want := []Message{userMsg("hello")}
	got := NormalizeMessages(input)
	if !msgsEqual(got, want) {
		t.Errorf("leading tool: got %+v, want %+v", got, want)
	}
}

func TestNormalizeMessages_Idempotency(t *testing.T) {
	// 10. 幂等性：对任意输入，NormalizeMessages(NormalizeMessages(x)) == NormalizeMessages(x)
	testCases := [][]Message{
		{},
		{systemMsg("sys")},
		{userMsg("hi"), assistantMsg("A"), assistantMsg("B")},
		{userMsg("hi"), assistantMsg("try", ToolCall{ID: "tc1", Type: "function", Function: FunctionCall{Name: "fn", Arguments: "{}"}})},
		{userMsg("hi"), toolMsg("orphan", "result"), assistantMsg("hi")},
		{userMsg("hi"), assistantMsg("", ToolCall{ID: "tc1", Type: "function", Function: FunctionCall{Name: "fn", Arguments: "{}"}}), assistantMsg("B", ToolCall{ID: "tc2", Type: "function", Function: FunctionCall{Name: "fn2", Arguments: "{}"}}), assistantMsg("C")},
		{toolMsg("x", "result"), userMsg("hello")},
	}

	for i, input := range testCases {
		once := NormalizeMessages(input)
		twice := NormalizeMessages(once)
		if !msgsEqual(once, twice) {
			t.Errorf("idempotency test %d: NormalizeMessages(NormalizeMessages(x)) != NormalizeMessages(x)\n  once:  %+v\n  twice: %+v", i, once, twice)
		}
	}
}

func TestNormalizeMessages_EmptyList(t *testing.T) {
	// 11. 空列表：NormalizeMessages([]Message{}) → 空列表
	got := NormalizeMessages([]Message{})
	if len(got) != 0 {
		t.Errorf("empty list: got %d messages, want 0", len(got))
	}
}

func TestNormalizeMessages_OnlySystem(t *testing.T) {
	// 12. 只有system消息：[system("you are helpful")] → 原样返回
	input := []Message{systemMsg("you are helpful")}
	got := NormalizeMessages(input)
	if !msgsEqual(got, input) {
		t.Errorf("only system: got %+v, want %+v", got, input)
	}
}

func TestNormalizeMessages_ThreeConsecutiveAssistants(t *testing.T) {
	// 13. 三个连续assistant全合并：
	// [user, assistant("A"), assistant("B"), assistant("C")] → [user, assistant("A\n\nB\n\nC")]
	input := []Message{userMsg("hi"), assistantMsg("A"), assistantMsg("B"), assistantMsg("C")}
	want := []Message{userMsg("hi"), assistantMsg("A\n\nB\n\nC")}
	got := NormalizeMessages(input)
	if !msgsEqual(got, want) {
		t.Errorf("three consecutive: got %+v, want %+v", got, want)
	}
}

// ---- MergeConsecutiveAssistants specific tests ----

func TestMergeConsecutiveAssistants_NoMerge(t *testing.T) {
	// Non-adjacent assistants should not be merged
	input := []Message{
		assistantMsg("A"),
		userMsg("hi"),
		assistantMsg("B"),
	}
	got := MergeConsecutiveAssistants(input)
	if !msgsEqual(got, input) {
		t.Errorf("no merge: got %+v, want %+v", got, input)
	}
}

func TestMergeConsecutiveAssistants_OnlyTwo(t *testing.T) {
	input := []Message{assistantMsg("X"), assistantMsg("Y")}
	want := []Message{assistantMsg("X\n\nY")}
	got := MergeConsecutiveAssistants(input)
	if !msgsEqual(got, want) {
		t.Errorf("only two: got %+v, want %+v", got, want)
	}
}

func TestMergeConsecutiveAssistants_Empty(t *testing.T) {
	got := MergeConsecutiveAssistants([]Message{})
	if len(got) != 0 {
		t.Errorf("empty: got %d, want 0", len(got))
	}
}

func TestMergeConsecutiveAssistants_Single(t *testing.T) {
	input := []Message{assistantMsg("alone")}
	got := MergeConsecutiveAssistants(input)
	if !msgsEqual(got, input) {
		t.Errorf("single: got %+v, want %+v", got, input)
	}
}

func TestMergeConsecutiveAssistants_ReasoningMerged(t *testing.T) {
	// Reasoning content should also be merged with \n\n separator
	input := []Message{
		assistantMsg("content A", ToolCall{}),
		assistantMsg("content B", ToolCall{ID: "tc1", Type: "function", Function: FunctionCall{Name: "fn", Arguments: "{}"}}),
	}
	// When current has tool calls, it should be skipped (not merged)
	want := []Message{
		assistantMsg("content A", ToolCall{}),
	}
	got := MergeConsecutiveAssistants(input)
	if !msgsEqual(got, want) {
		t.Errorf("reasoning skip: got %+v, want %+v", got, want)
	}
}

func TestMergeConsecutiveAssistants_PreviousToolCallCurrentText(t *testing.T) {
	// Previous has ToolCalls, current is text-only → append content
	tc1 := ToolCall{ID: "tc1", Type: "function", Function: FunctionCall{Name: "fn", Arguments: "{}"}}
	input := []Message{
		assistantMsg("before tool", tc1),
		assistantMsg("after text"),
	}
	want := []Message{assistantMsg("before tool\n\nafter text", tc1)}
	got := MergeConsecutiveAssistants(input)
	if !msgsEqual(got, want) {
		t.Errorf("toolcall+text: got %+v, want %+v", got, want)
	}
}

func TestMergeConsecutiveAssistants_CurrentToolCallSkipped(t *testing.T) {
	// Current has tool calls → skip current (keep previous as-is)
	tc1 := ToolCall{ID: "tc1", Type: "function", Function: FunctionCall{Name: "fn", Arguments: "{}"}}
	input := []Message{
		assistantMsg("text only"),
		assistantMsg("with tool call", tc1),
	}
	want := []Message{assistantMsg("text only")}
	got := MergeConsecutiveAssistants(input)
	if !msgsEqual(got, want) {
		t.Errorf("current toolcall skipped: got %+v, want %+v", got, want)
	}
}

func TestDropEmptyMessages_SystemKept(t *testing.T) {
	input := []Message{systemMsg(""), systemMsg("hi")}
	got := dropEmptyMessages(input)
	if len(got) != 2 {
		t.Errorf("system kept: got %d, want 2", len(got))
	}
}

func TestDropEmptyMessages_EmptyAssistantDropped(t *testing.T) {
	input := []Message{assistantMsg("")}
	got := dropEmptyMessages(input)
	if len(got) != 0 {
		t.Errorf("empty assistant dropped: got %d, want 0", len(got))
	}
}

func TestEnsureValidStart_LeadingTool(t *testing.T) {
	input := []Message{toolMsg("x", "result"), userMsg("hello")}
	got := ensureValidStart(input)
	want := []Message{userMsg("hello")}
	if !msgsEqual(got, want) {
		t.Errorf("leading tool: got %+v, want %+v", got, want)
	}
}

func TestEnsureValidStart_FirstSystemKept(t *testing.T) {
	input := []Message{systemMsg("sys"), userMsg("hello")}
	got := ensureValidStart(input)
	if !msgsEqual(got, input) {
		t.Errorf("first system kept: got %+v, want %+v", got, input)
	}
}

// ---- repairToolCallPairs specific tests ----

func TestRepairToolCallPairs_Complete(t *testing.T) {
	tc1 := ToolCall{ID: "tc1", Type: "function", Function: FunctionCall{Name: "fn", Arguments: "{}"}}
	input := []Message{
		userMsg("hi"),
		assistantMsg("", tc1),
		toolMsg("tc1", "result"),
	}
	got := repairToolCallPairs(input)
	if !msgsEqual(got, input) {
		t.Errorf("complete: got %+v, want %+v", got, input)
	}
}

func TestRepairToolCallPairs_IncompleteStrip(t *testing.T) {
	tc1 := ToolCall{ID: "tc1", Type: "function", Function: FunctionCall{Name: "fn", Arguments: "{}"}}
	input := []Message{userMsg("hi"), assistantMsg("try", tc1)}
	want := []Message{userMsg("hi"), assistantMsg("try")}
	got := repairToolCallPairs(input)
	if !msgsEqual(got, want) {
		t.Errorf("incomplete strip: got %+v, want %+v", got, want)
	}
}

func TestRepairToolCallPairs_OrphanTool(t *testing.T) {
	input := []Message{userMsg("hi"), toolMsg("orphan", "result"), assistantMsg("hi")}
	want := []Message{userMsg("hi"), assistantMsg("hi")}
	got := repairToolCallPairs(input)
	if !msgsEqual(got, want) {
		t.Errorf("orphan tool: got %+v, want %+v", got, want)
	}
}

func TestRepairToolCallPairs_Empty(t *testing.T) {
	got := repairToolCallPairs([]Message{})
	if len(got) != 0 {
		t.Errorf("empty: got %d, want 0", len(got))
	}
}

// ---- Edge cases ----

func TestNormalizeMessages_MixedContentWithReasoning(t *testing.T) {
	// Test that reasoning is preserved and merged
	input := []Message{
		userMsg("hi"),
		assistantMsg("content A", ToolCall{}),
		assistantMsg("content B"),
	}
	got := NormalizeMessages(input)
	// repairToolCallPairs strips ToolCalls from first (incomplete), leaving just content A
	// MergeConsecutiveAssistants merges "content A" and "content B"
	want := []Message{userMsg("hi"), assistantMsg("content A\n\ncontent B")}
	if !msgsEqual(got, want) {
		t.Errorf("mixed reasoning: got %+v, want %+v", got, want)
	}
}

func TestNormalizeMessages_MultipleToolCallsOneIncomplete(t *testing.T) {
	// Assistant with multiple tool calls, only one has response → incomplete
	tc1 := ToolCall{ID: "tc1", Type: "function", Function: FunctionCall{Name: "fn1", Arguments: "{}"}}
	tc2 := ToolCall{ID: "tc2", Type: "function", Function: FunctionCall{Name: "fn2", Arguments: "{}"}}
	input := []Message{
		userMsg("hi"),
		assistantMsg("call both", tc1, tc2),
		toolMsg("tc1", "result1"),
		// tc2 response missing → incomplete
	}
	got := NormalizeMessages(input)
	// Should strip ToolCalls from assistant, keep "call both"
	want := []Message{userMsg("hi"), assistantMsg("call both")}
	if !msgsEqual(got, want) {
		t.Errorf("multiple tool calls partial: got %+v, want %+v", got, want)
	}
}

func TestNormalizeMessages_NilSlice(t *testing.T) {
	got := NormalizeMessages(nil)
	if got != nil {
		t.Errorf("nil slice: got %v, want nil", got)
	}
}

func TestMergeConsecutiveAssistants_NilSlice(t *testing.T) {
	got := MergeConsecutiveAssistants(nil)
	if got != nil {
		t.Errorf("nil slice: got %v, want nil", got)
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

// Ensure msgsEqual is used correctly (compilation check)
func TestMsgsEqualSanity(t *testing.T) {
	a := []Message{userMsg("x")}
	b := []Message{userMsg("x")}
	c := []Message{userMsg("y")}
	if !msgsEqual(a, b) {
		t.Error("equal messages should be equal")
	}
	if msgsEqual(a, c) {
		t.Error("different messages should not be equal")
	}
}

// Additional table-driven test for common patterns
func TestNormalizeMessages_TableDriven(t *testing.T) {
	type testCase struct {
		name string
		input []Message
		want  []Message
	}

	tc1 := ToolCall{ID: "tc1", Type: "function", Function: FunctionCall{Name: "fn", Arguments: "{}"}}
	tc2 := ToolCall{ID: "tc2", Type: "function", Function: FunctionCall{Name: "fn2", Arguments: "{}"}}

	tests := []testCase{
		{
			name:  "system_only",
			input: []Message{systemMsg("sys")},
			want:  []Message{systemMsg("sys")},
		},
		{
			name:  "user_only",
			input: []Message{userMsg("hi")},
			want:  []Message{userMsg("hi")},
		},
		{
			name:  "complete_roundtrip",
			input: []Message{userMsg("hi"), assistantMsg("thinking", tc1), toolMsg("tc1", "ok"), assistantMsg("done")},
			want:  []Message{userMsg("hi"), assistantMsg("thinking", tc1), toolMsg("tc1", "ok"), assistantMsg("done")},
		},
		{
			name:  "incomplete_assistant_keeps_content",
			input: []Message{userMsg("hi"), assistantMsg("partial", tc1)},
			want:  []Message{userMsg("hi"), assistantMsg("partial")},
		},
		{
			name:  "multiple_incomplete_merge",
			input: []Message{userMsg("hi"), assistantMsg("A", tc1), assistantMsg("B", tc2)},
			want:  []Message{userMsg("hi"), assistantMsg("A\n\nB")},
		},
		{
			name:  "leading_system_skipped_second_kept",
			input: []Message{systemMsg("sys1"), systemMsg("sys2"), userMsg("hi")},
			want:  []Message{systemMsg("sys1"), systemMsg("sys2"), userMsg("hi")},
		},
		{
			name:  "empty_content_assistant_dropped",
			input: []Message{userMsg("hi"), assistantMsg("")},
			want:  []Message{userMsg("hi")},
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
