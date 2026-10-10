package memory

// rollout_reader_test.go — ReadRolloutMemory 单元测试（表驱动 + 独立场景）。
//
// fixture 均写入 t.TempDir()，手动构造 rollout JSONL 行（时间戳合法格式，
// 行序模拟 executor 的写入顺序）。核心不变式助手 assertRolloutPairingInvariant
// 遍历 mem.ToMessages() 校验 tool 配对完整性。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yagent/internal/llm"
)

// ─── fixture 行构造 ───

const (
	rrTs1 = "2025-06-01T10:00:00.000Z"
	rrTs2 = "2025-06-01T10:00:01.000Z"
	rrTs3 = "2025-06-01T10:00:02.000Z"
	rrTs4 = "2025-06-01T10:00:03.000Z"
	rrTs5 = "2025-06-01T10:00:04.000Z"
	rrTs6 = "2025-06-01T10:00:05.000Z"
	rrTs7 = "2025-06-01T10:00:06.000Z"
)

// rrLine 生成一条 envelope 行。
func rrLine(ts, typ, payload string) string {
	if payload == "" {
		payload = "{}"
	}
	return `{"timestamp":"` + ts + `","type":"` + typ + `","payload":` + payload + `}`
}

// rrMeta session_meta payload。
func rrMeta(sessionID, cwd, branch string) string {
	return `{"session_id":"` + sessionID + `","cwd":"` + cwd + `","originator":"yagent","source":"cli","git":{"branch":"` + branch + `","sha":"abc1234"}}`
}

// rrTurnCtx turn_context payload。
func rrTurnCtx(model, cwd string) string {
	return `{"turn_id":"t1","cwd":"` + cwd + `","model":"` + model + `"}`
}

// rrUserItem user message payload。
func rrUserItem(id, text string) string {
	return `{"type":"message","role":"user","id":"` + id + `","content":[{"type":"input_text","text":"` + text + `"}]}`
}

// rrAssistantItem assistant message payload。
func rrAssistantItem(id, text string) string {
	return `{"type":"message","role":"assistant","id":"` + id + `","content":[{"type":"output_text","text":"` + text + `"}]}`
}

// rrRawMessageItem 自由构造 message payload（原始 JSON）。
func rrRawMessageItem(raw string) string {
	return raw
}

// rrCallItem function_call payload（args 为内联 JSON 字符串内容）。
func rrCallItem(callID, name, argsJSONStr string) string {
	return `{"type":"function_call","id":"fc_` + callID + `","call_id":"` + callID + `","name":"` + name + `","namespace":"","arguments":"` + argsJSONStr + `"}`
}

// rrCallItemNS function_call payload（带 namespace）。
func rrCallItemNS(callID, name, namespace, argsJSONStr string) string {
	return `{"type":"function_call","id":"fc_` + callID + `","call_id":"` + callID + `","name":"` + name + `","namespace":"` + namespace + `","arguments":"` + argsJSONStr + `"}`
}

// rrCallOutItem function_call_output payload（outputRaw 为 output 字段的 JSON 值，
// 按真实 rollout 形态双重编码为 JSON 字符串后放入 output 字段）。
func rrCallOutItem(callID, outputRaw string) string {
	encoded, _ := json.Marshal(outputRaw) // 把 JSON 值编码为字符串
	return `{"type":"function_call_output","call_id":"` + callID + `","output":` + string(encoded) + `}`
}

// rrReasoningItem reasoning payload。
func rrReasoningItem(summaryText string) string {
	return `{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"` + summaryText + `"}],"content":null,"encrypted_content":"enc-abc"}`
}

// rrCompacted compacted payload。
func rrCompacted(summary string) string {
	return `{"summary":"` + summary + `","tokens_before":100,"tokens_after":40}`
}

// writeRolloutFixture 写入 fixture 文件（t.TempDir 下）并返回路径。
func writeRolloutFixture(t *testing.T, name string, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", name, err)
	}
	return path
}

// ─── 核心不变式断言 ───

// assertRolloutPairingInvariant 校验恢复出的对话满足 tool 配对不变式：
//   - 每条 tool 消息之前必须存在包含其 call_id 的 assistant ToolCalls；
//   - 每条 assistant ToolCall 的 id，其后（到下一条非 tool 消息之前）必须有对应 tool 消息。
func assertRolloutPairingInvariant(t *testing.T, mem *ConversationMemory) {
	t.Helper()
	msgs := mem.ToMessages()
	for i, m := range msgs {
		if m.Role == llm.RoleTool {
			found := false
			for j := 0; j < i; j++ {
				if msgs[j].Role != llm.RoleAssistant {
					continue
				}
				for _, tc := range msgs[j].ToolCalls {
					if tc.ID == m.ToolCallID {
						found = true
					}
				}
			}
			if !found {
				t.Fatalf("invariant violated: tool message[%d] call_id=%q has no preceding assistant tool_call", i, m.ToolCallID)
			}
		}
		if m.Role == llm.RoleAssistant {
			for _, tc := range m.ToolCalls {
				ok := false
				for k := i + 1; k < len(msgs) && msgs[k].Role == llm.RoleTool; k++ {
					if msgs[k].ToolCallID == tc.ID {
						ok = true
						break
					}
				}
				if !ok {
					t.Fatalf("invariant violated: assistant[%d] tool_call %q has no matching tool message in its following output run", i, tc.ID)
				}
			}
		}
	}
}

// ─── 表驱动主测试 ───

// rolloutCase 表驱动场景。
type rolloutCase struct {
	name  string
	lines []string
	opts  RolloutReadOptions
	check func(t *testing.T, mem *ConversationMemory, info RolloutInfo, err error)
}

func TestReadRolloutMemory_Table(t *testing.T) {
	baseLines := func(lines ...string) []string {
		return append([]string{rrLine(rrTs1, "session_meta", rrMeta("sess-1", "/repo", "main"))}, lines...)
	}
	opts := DefaultRolloutReadOptions()

	fullOpts := opts
	fullOpts.AttachReasoningSummary = true

	cases := []rolloutCase{
		{
			// ① user/assistant 往返 + event_msg/world_state/turn_context 均不进消息
			name: "user_assistant_roundtrip",
			lines: baseLines(
				rrLine(rrTs2, "turn_context", rrTurnCtx("test-model", "/repo")),
				rrLine(rrTs3, "event_msg", `{"type":"task_started"}`),
				rrLine(rrTs4, "response_item", rrUserItem("u1", "你好")),
				rrLine(rrTs5, "response_item", rrAssistantItem("a1", "你好，世界")),
				rrLine(rrTs6, "event_msg", `{"type":"agent_message","message":"should be ignored"}`),
				rrLine(rrTs7, "world_state", `{"files":["main.go"],"git_branch":"main"}`),
			),
			opts: opts,
			check: func(t *testing.T, mem *ConversationMemory, info RolloutInfo, err error) {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				if len(mem.Messages) != 2 {
					t.Fatalf("messages = %d, want 2", len(mem.Messages))
				}
				m0, m1 := mem.Messages[0], mem.Messages[1]
				if m0.Type != MessageTypeHuman || m0.Content != "你好" {
					t.Fatalf("msg0 = %+v, want human 你好", m0)
				}
				if m1.Type != MessageTypeAssistant || m1.Content != "你好，世界" {
					t.Fatalf("msg1 = %+v, want assistant", m1)
				}
				// 身份提取（R1/R2）
				if info.SessionID != "sess-1" || info.CWD != "/repo" || info.Originator != "yagent" ||
					info.Source != "cli" || info.GitBranch != "main" || info.GitSHA != "abc1234" {
					t.Fatalf("identity info = %+v", info)
				}
				if info.Model != "test-model" {
					t.Fatalf("info.Model = %q, want test-model", info.Model)
				}
				if info.Entries != 7 {
					t.Fatalf("info.Entries = %d, want 7", info.Entries)
				}
				if info.Messages != 2 {
					t.Fatalf("info.Messages = %d, want 2", info.Messages)
				}
				assertRolloutPairingInvariant(t, mem)
			},
		},
		{
			// ② 单 call 配对 + 嵌套 output 解析 + exit_code/duration 落 Metadata + 多部件 fallback 见后续 case
			name: "single_call_pairing_nested_output",
			lines: baseLines(
				rrLine(rrTs3, "response_item", rrUserItem("u1", "read it")),
				rrLine(rrTs4, "response_item", rrAssistantItem("a1", "reading now")),
				rrLine(rrTs5, "response_item", rrCallItem("c1", "read_file", `{\"path\":\"main.go\"}`)),
				rrLine(rrTs6, "response_item", rrCallOutItem("c1", `{"output":"file body","metadata":{"exit_code":0,"duration_seconds":2}}`)),
			),
			opts: opts,
			check: func(t *testing.T, mem *ConversationMemory, info RolloutInfo, err error) {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				if len(mem.Messages) != 3 {
					t.Fatalf("messages = %d, want 3: %+v", len(mem.Messages), mem.Messages)
				}
				a := mem.Messages[1]
				if a.Type != MessageTypeAssistant || len(a.ToolCalls) != 1 {
					t.Fatalf("assistant tool calls = %+v", a)
				}
				tc := a.ToolCalls[0]
				if tc.ID != "c1" || tc.Function.Name != "read_file" || string(tc.Function.Arguments) != `{"path":"main.go"}` {
					t.Fatalf("tool call = %+v", tc)
				}
				tool := mem.Messages[2]
				if tool.Type != MessageTypeTool || tool.ToolCallID == nil || *tool.ToolCallID != "c1" {
					t.Fatalf("tool msg = %+v", tool)
				}
				if tool.Content != "file body" {
					t.Fatalf("tool content = %q, want 'file body'", tool.Content)
				}
				if tool.Metadata["exit_code"] != float64(0) || tool.Metadata["duration_seconds"] != float64(2) {
					t.Fatalf("tool metadata = %+v", tool.Metadata)
				}
				if info.ToolCalls != 1 {
					t.Fatalf("info.ToolCalls = %d, want 1", info.ToolCalls)
				}
				assertRolloutPairingInvariant(t, mem)
			},
		},
		{
			// ③ 多个连续 call 挂同一条 assistant，输出顺序保留
			name: "multi_call_same_assistant",
			lines: baseLines(
				rrLine(rrTs3, "response_item", rrAssistantItem("a1", "running batch")),
				rrLine(rrTs4, "response_item", rrCallItem("c1", "ro_a", `{}`)),
				rrLine(rrTs5, "response_item", rrCallItem("c2", "ro_b", `{}`)),
				rrLine(rrTs6, "response_item", rrCallOutItem("c1", `"A"`)),
				rrLine(rrTs7, "response_item", rrCallOutItem("c2", `"B"`)),
			),
			opts: opts,
			check: func(t *testing.T, mem *ConversationMemory, info RolloutInfo, err error) {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				// 丢弃的"call 无前导 assistant"不存在：A(c1,c2) 挂同一条。
				if len(mem.Messages) != 3 {
					t.Fatalf("messages = %d, want 3: %+v", len(mem.Messages), mem.Messages)
				}
				a := mem.Messages[0]
				if len(a.ToolCalls) != 2 || a.ToolCalls[0].ID != "c1" || a.ToolCalls[1].ID != "c2" {
					t.Fatalf("tool calls = %+v", a.ToolCalls)
				}
				if mem.Messages[1].Content != `"A"` || mem.Messages[2].Content != `"B"` {
					t.Fatalf("outputs: %q %q", mem.Messages[1].Content, mem.Messages[2].Content)
				}
				assertRolloutPairingInvariant(t, mem)
			},
		},
		{
			// ④ 无前导 assistant 的 call → 合成空 assistant（SyntheticOutputs 不计）
			name: "call_without_leading_assistant",
			lines: baseLines(
				rrLine(rrTs3, "response_item", rrCallItem("c1", "solo_tool", `{}`)),
				rrLine(rrTs4, "response_item", rrCallOutItem("c1", `"done"`)),
			),
			opts: opts,
			check: func(t *testing.T, mem *ConversationMemory, info RolloutInfo, err error) {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				if len(mem.Messages) != 2 {
					t.Fatalf("messages = %d, want 2: %+v", len(mem.Messages), mem.Messages)
				}
				a, tool := mem.Messages[0], mem.Messages[1]
				if a.Type != MessageTypeAssistant || a.Content != "" || len(a.ToolCalls) != 1 || a.ToolCalls[0].ID != "c1" {
					t.Fatalf("synthetic assistant = %+v", a)
				}
				if tool.Type != MessageTypeTool || tool.Content != `"done"` {
					t.Fatalf("tool msg = %+v", tool)
				}
				if info.SyntheticOutputs != 0 {
					t.Fatalf("SyntheticOutputs = %d, want 0 (仅 R12 悬空 call 计数)", info.SyntheticOutputs)
				}
				assertRolloutPairingInvariant(t, mem)
			},
		},
		{
			// ⑤ 文件尾悬空 call → 合成 tool 占位（SyntheticOutputs==1）
			name: "dangling_call_at_eof",
			lines: baseLines(
				rrLine(rrTs3, "response_item", rrUserItem("u1", "go")),
				rrLine(rrTs4, "response_item", rrAssistantItem("a1", "step one")),
				rrLine(rrTs5, "response_item", rrCallItem("c1", "tool1", `{}`)),
				rrLine(rrTs5, "response_item", rrCallOutItem("c1", `"ok"`)),
				rrLine(rrTs6, "response_item", rrAssistantItem("a2", "step two")),
				// c2 有 call 无 output —— 模拟崩溃中断
				rrLine(rrTs7, "response_item", rrCallItem("c2", "tool2", `{}`)),
			),
			opts: opts,
			check: func(t *testing.T, mem *ConversationMemory, info RolloutInfo, err error) {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				if len(mem.Messages) != 5 {
					t.Fatalf("messages = %d, want 5: %+v", len(mem.Messages), mem.Messages)
				}
				last := mem.Messages[4]
				if last.Type != MessageTypeTool || last.Content != "会话中断前未返回结果" || *last.ToolCallID != "c2" {
					t.Fatalf("synthetic tool = %+v", last)
				}
				if last.Metadata["synthetic"] != true {
					t.Fatalf("synthetic flag = %+v", last.Metadata)
				}
				if info.SyntheticOutputs != 1 {
					t.Fatalf("SyntheticOutputs = %d, want 1", info.SyntheticOutputs)
				}
				assertRolloutPairingInvariant(t, mem)
			},
		},
		{
			// ⑥ 孤儿输出 → 丢弃
			name: "orphan_output_dropped",
			lines: baseLines(
				rrLine(rrTs3, "response_item", rrUserItem("u1", "hi")),
				rrLine(rrTs4, "response_item", rrCallOutItem("c_missing", `"ghost"`)),
			),
			opts: opts,
			check: func(t *testing.T, mem *ConversationMemory, info RolloutInfo, err error) {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				if len(mem.Messages) != 1 {
					t.Fatalf("messages = %d, want 1 (orphan dropped): %+v", len(mem.Messages), mem.Messages)
				}
				if mem.Messages[0].Type != MessageTypeHuman {
					t.Fatalf("remaining msg = %+v", mem.Messages[0])
				}
				if info.DroppedOrphanOutputs != 1 {
					t.Fatalf("DroppedOrphanOutputs = %d, want 1", info.DroppedOrphanOutputs)
				}
				assertRolloutPairingInvariant(t, mem)
			},
		},
		{
			// ⑦ 同一 call_id 多条输出 → 保留第一条
			name: "duplicate_output_keep_first",
			lines: baseLines(
				rrLine(rrTs3, "response_item", rrAssistantItem("a1", "call")),
				rrLine(rrTs4, "response_item", rrCallItem("c1", "t", `{}`)),
				rrLine(rrTs5, "response_item", rrCallOutItem("c1", `"first"`)),
				rrLine(rrTs6, "response_item", rrCallOutItem("c1", `"second"`)),
			),
			opts: opts,
			check: func(t *testing.T, mem *ConversationMemory, info RolloutInfo, err error) {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				if len(mem.Messages) != 2 {
					t.Fatalf("messages = %d, want 2: %+v", len(mem.Messages), mem.Messages)
				}
				if mem.Messages[1].Content != `"first"` {
					t.Fatalf("kept output = %q, want first", mem.Messages[1].Content)
				}
				if info.DroppedOrphanOutputs != 1 {
					t.Fatalf("DroppedOrphanOutputs = %d, want 1 (dup counted)", info.DroppedOrphanOutputs)
				}
				assertRolloutPairingInvariant(t, mem)
			},
		},
		{
			// ⑧ 尾部崩溃半行/坏 JSON → 容忍计数
			name: "bad_tail_line_tolerated",
			lines: baseLines(
				rrLine(rrTs3, "response_item", rrUserItem("u1", "hi")),
				`{"timestamp":"` + rrTs4 + `","type":"response_item","paylo`, // 崩溃半行
			),
			opts: opts,
			check: func(t *testing.T, mem *ConversationMemory, info RolloutInfo, err error) {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				if info.SkippedBadLines != 1 {
					t.Fatalf("SkippedBadLines = %d, want 1", info.SkippedBadLines)
				}
				if len(mem.Messages) != 1 {
					t.Fatalf("messages = %d, want 1", len(mem.Messages))
				}
				assertRolloutPairingInvariant(t, mem)
			},
		},
		{
			// ⑨ 未知 envelope type / 未知 response_item 子类型 → 跳过
			name: "unknown_types_skipped",
			lines: baseLines(
				rrLine(rrTs3, "mystery_type", `{"whatever":1}`),
				rrLine(rrTs4, "response_item", `{"type":"future_item","data":{}}`),
				rrLine(rrTs5, "response_item", rrUserItem("u1", "still here")),
			),
			opts: opts,
			check: func(t *testing.T, mem *ConversationMemory, info RolloutInfo, err error) {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				if len(mem.Messages) != 1 || mem.Messages[0].Content != "still here" {
					t.Fatalf("messages = %+v", mem.Messages)
				}
				if info.Entries != 4 {
					t.Fatalf("Entries = %d, want 4", info.Entries)
				}
				assertRolloutPairingInvariant(t, mem)
			},
		},
		{
			// ⑩ reasoning 跳过计数 + AttachReasoningSummary 挂载
			name: "reasoning_summary_attach",
			lines: baseLines(
				rrLine(rrTs3, "response_item", rrReasoningItem("我说了要思考")),
				rrLine(rrTs4, "response_item", rrAssistantItem("a1", "answer")),
			),
			opts: fullOpts,
			check: func(t *testing.T, mem *ConversationMemory, info RolloutInfo, err error) {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				if info.SkippedReasoning != 1 {
					t.Fatalf("SkippedReasoning = %d, want 1", info.SkippedReasoning)
				}
				a := mem.Messages[0]
				if a.Metadata["reasoning_summary"] != "我说了要思考" {
					t.Fatalf("reasoning_summary metadata = %+v", a.Metadata)
				}
				assertRolloutPairingInvariant(t, mem)
			},
		},
		{
			// ⑩b reasoning 跳过但不挂载（默认）
			name: "reasoning_not_attached_by_default",
			lines: baseLines(
				rrLine(rrTs3, "response_item", rrReasoningItem("hidden")),
				rrLine(rrTs4, "response_item", rrAssistantItem("a1", "answer")),
			),
			opts: opts,
			check: func(t *testing.T, mem *ConversationMemory, info RolloutInfo, err error) {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				if len(mem.Messages) != 1 || mem.Messages[0].Content != "answer" {
					t.Fatalf("messages = %+v", mem.Messages)
				}
				if len(mem.Messages[0].Metadata) != 0 {
					t.Fatalf("metadata should be empty by default: %+v", mem.Messages[0].Metadata)
				}
			},
		},
		{
			// 多部件 input_text + 回退拼接
			name: "user_multi_part_and_fallback",
			lines: baseLines(
				rrLine(rrTs3, "response_item", rrRawMessageItem(`{"type":"message","role":"user","id":"u1","content":[{"type":"input_text","text":"part-a"},{"type":"input_text","text":"part-b"}]}`)),
			),
			opts: opts,
			check: func(t *testing.T, mem *ConversationMemory, info RolloutInfo, err error) {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				if mem.Messages[0].Content != "part-a\npart-b" {
					t.Fatalf("multi-part content = %q", mem.Messages[0].Content)
				}
			},
		},
		{
			// 无 input_text 部件 → 回退拼接所有 text
			name: "user_fallback_all_text",
			lines: baseLines(
				rrLine(rrTs3, "response_item", rrRawMessageItem(`{"type":"message","role":"user","id":"u1","content":[{"type":"image","text":"pic-1"},{"type":"misc","text":"txt-2"}]}`)),
			),
			opts: opts,
			check: func(t *testing.T, mem *ConversationMemory, info RolloutInfo, err error) {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				if mem.Messages[0].Content != "pic-1\ntxt-2" {
					t.Fatalf("fallback content = %q", mem.Messages[0].Content)
				}
			},
		},
		{
			// namespace 非空 → 存 Metadata，不污染 name
			name: "tool_namespace_metadata",
			lines: baseLines(
				rrLine(rrTs3, "response_item", rrAssistantItem("a1", "with ns")),
				rrLine(rrTs4, "response_item", rrCallItemNS("c1", "my_tool", "tools_io", `{}`)),
				rrLine(rrTs5, "response_item", rrCallOutItem("c1", `"ok"`)),
			),
			opts: opts,
			check: func(t *testing.T, mem *ConversationMemory, info RolloutInfo, err error) {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				a := mem.Messages[0]
				if a.ToolCalls[0].Function.Name != "my_tool" {
					t.Fatalf("tool name = %q", a.ToolCalls[0].Function.Name)
				}
				if a.Metadata["tool_namespace"] != "tools_io" {
					t.Fatalf("namespace metadata = %+v", a.Metadata)
				}
				assertRolloutPairingInvariant(t, mem)
			},
		},
		{
			// turn_context 最新值生效（两个 turn_context，model m2 覆盖 m1）
			name: "turn_context_latest_wins",
			lines: baseLines(
				rrLine(rrTs2, "turn_context", rrTurnCtx("model-m1", "/repo1")),
				rrLine(rrTs3, "response_item", rrUserItem("u1", "hi")),
				rrLine(rrTs4, "turn_context", rrTurnCtx("model-m2", "/repo2")),
			),
			opts: opts,
			check: func(t *testing.T, mem *ConversationMemory, info RolloutInfo, err error) {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				if info.Model != "model-m2" || info.CWD != "/repo2" {
					t.Fatalf("info = %+v", info)
				}
				if len(mem.Messages) != 1 { // turn_context 不进消息
					t.Fatalf("messages = %+v", mem.Messages)
				}
			},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			path := writeRolloutFixture(t, tc.name+".jsonl", tc.lines)
			mem, info, err := ReadRolloutMemory(path, tc.opts)
			tc.check(t, mem, info, err)
		})
	}
}

// ─── ⑫ 空文件 / 仅 meta ───

func TestReadRolloutMemory_EmptyAndMetaOnly(t *testing.T) {
	// 空文件 → 明确错误
	empty := writeRolloutFixture(t, "empty.jsonl", nil)
	if mem, info, err := ReadRolloutMemory(empty, DefaultRolloutReadOptions()); err == nil {
		t.Fatalf("empty rollout should error, got mem=%v info=%+v", mem, info)
	}

	// 仅有 meta（无任何消息）→ 明确错误，不 panic
	onlyMeta := writeRolloutFixture(t, "meta-only.jsonl", []string{
		rrLine(rrTs1, "session_meta", rrMeta("sess-1", "/repo", "main")),
	})
	mem, info, err := ReadRolloutMemory(onlyMeta, DefaultRolloutReadOptions())
	if err == nil {
		t.Fatalf("meta-only rollout should error")
	}
	if mem != nil {
		t.Fatalf("mem should be nil on error, got %+v", mem)
	}
	if info.Entries != 1 {
		t.Fatalf("info.Entries = %d, want 1", info.Entries)
	}
}

// ─── ⑪ compacted 双模式 ───

func TestReadRolloutMemory_CompactedModes(t *testing.T) {
	lines := []string{
		rrLine(rrTs1, "session_meta", rrMeta("sess-1", "/repo", "main")),
		rrLine(rrTs2, "response_item", rrUserItem("u1", "before-question")),
		rrLine(rrTs3, "response_item", rrAssistantItem("a1", "before-answer")),
		rrLine(rrTs4, "compacted", rrCompacted("这是压缩摘要")),
		rrLine(rrTs5, "response_item", rrUserItem("u2", "after-question")),
		rrLine(rrTs6, "response_item", rrAssistantItem("a2", "after-answer")),
	}

	// CompactionFull：全部恢复（超集）
	pathFull := writeRolloutFixture(t, "compacted-full.jsonl", lines)
	memFull, _, err := ReadRolloutMemory(pathFull, DefaultRolloutReadOptions())
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	if len(memFull.Messages) != 4 {
		t.Fatalf("full messages = %d, want 4: %+v", len(memFull.Messages), memFull.Messages)
	}
	if memFull.Messages[0].Content != "before-question" || memFull.Messages[3].Content != "after-answer" {
		t.Fatalf("full messages = %+v", memFull.Messages)
	}
	assertRolloutPairingInvariant(t, memFull)

	// CompactionSummaryTail：summary 注入 System，仅转换其后条目
	optsTail := DefaultRolloutReadOptions()
	optsTail.Compaction = CompactionSummaryTail
	pathTail := writeRolloutFixture(t, "compacted-tail.jsonl", lines)
	memTail, infoTail, err := ReadRolloutMemory(pathTail, optsTail)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if len(memTail.Messages) != 3 {
		t.Fatalf("tail messages = %d, want 3: %+v", len(memTail.Messages), memTail.Messages)
	}
	sysMsg := memTail.Messages[0]
	if sysMsg.Type != MessageTypeSystem || sysMsg.Content != "这是压缩摘要" || !sysMsg.IsAnchored {
		t.Fatalf("tail system msg = %+v", sysMsg)
	}
	if memTail.Messages[1].Content != "after-question" || memTail.Messages[2].Content != "after-answer" {
		t.Fatalf("tail messages = %+v", memTail.Messages)
	}
	assertRolloutPairingInvariant(t, memTail)

	// tail 且 compacted 后无消息 → 仅剩 System 摘要（不报零消息错误，也不 panic）
	lines2 := lines[:4]
	pathTail2 := writeRolloutFixture(t, "compacted-tail-2.jsonl", lines2)
	memTail2, _, err2 := ReadRolloutMemory(pathTail2, optsTail)
	if err2 != nil {
		t.Fatalf("tail2: %v", err2)
	}
	if len(memTail2.Messages) != 1 || memTail2.Messages[0].Type != MessageTypeSystem {
		t.Fatalf("tail-only messages = %+v", memTail2.Messages)
	}
	_ = infoTail
}

// ─── ⑭ MaxMessages tail-keep ───

func TestReadRolloutMemory_MaxMessagesTailKeep(t *testing.T) {
	// 3 轮完整 call→out（6 条消息），上限 4 → 保留 [A2,T2,A3,T3]，配对仍完整
	var lines []string
	lines = append(lines, rrLine(rrTs1, "session_meta", rrMeta("sess-1", "/repo", "main")))
	tsSeq := []string{rrTs1, rrTs2, rrTs3, rrTs4, rrTs5, rrTs6, rrTs7}
	idx := 1
	for round := 1; round <= 3; round++ {
		callID := callIDForRound(round)
		lines = append(lines,
			rrLine(tsSeq[idx%len(tsSeq)], "response_item", rrAssistantItem(callID+"-a", "round "+callID)),
			rrLine(tsSeq[(idx+1)%len(tsSeq)], "response_item", rrCallItem(callID, "tool", `{}`)),
		)
		idx++
		lines = append(lines, rrLine(tsSeq[idx%len(tsSeq)], "response_item", rrCallOutItem(callID, `"res-`+callID+`"`)))
	}
	opts := DefaultRolloutReadOptions()
	opts.MaxMessages = 4
	path := writeRolloutFixture(t, "tail-keep.jsonl", lines)
	mem, info, err := ReadRolloutMemory(path, opts)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(mem.Messages) != 4 {
		t.Fatalf("messages = %d, want 4: %+v", len(mem.Messages), mem.Messages)
	}
	if info.SyntheticOutputs != 0 || info.DroppedOrphanOutputs != 0 {
		t.Fatalf("complete rounds should have no synthetic/orphan: %+v", info)
	}
	assertRolloutPairingInvariant(t, mem)
}

// callIDForRound 生成轮次 call_id（c-round-N）。
func callIDForRound(round int) string {
	return "c-round-" + string(rune('0'+round))
}

// TestReadRolloutMemory_MaxMessagesTruncateSynthesizes 截头产生悬空 call / 孤儿输出后的归一化。
func TestReadRolloutMemory_MaxMessagesTruncateSynthesizes(t *testing.T) {
	// [U, A(c1), T1, A(c2)] 上限 2 → 截头保留 [T1, A(c2)]：
	//   T1 无前导 assistant → 孤儿丢弃；c2 无输出 → 合成占位 → [A2, T合]
	lines := []string{
		rrLine(rrTs1, "session_meta", rrMeta("sess-1", "/repo", "main")),
		rrLine(rrTs2, "response_item", rrUserItem("u1", "hi")),
		rrLine(rrTs3, "response_item", rrAssistantItem("a1", "two calls across rounds")),
		rrLine(rrTs3, "response_item", rrCallItem("c1", "tool1", `{}`)),
		rrLine(rrTs4, "response_item", rrCallOutItem("c1", `"res1"`)),
		rrLine(rrTs6, "response_item", rrAssistantItem("a2", "next round")),
		rrLine(rrTs7, "response_item", rrCallItem("c2", "tool2", `{}`)),
	}
	opts := DefaultRolloutReadOptions()
	opts.MaxMessages = 2
	path := writeRolloutFixture(t, "tail-synth.jsonl", lines)
	mem, info, err := ReadRolloutMemory(path, opts)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(mem.Messages) != 2 {
		t.Fatalf("messages = %d, want 2: %+v", len(mem.Messages), mem.Messages)
	}
	if mem.Messages[0].Type != MessageTypeAssistant || len(mem.Messages[0].ToolCalls) != 1 || mem.Messages[0].ToolCalls[0].ID != "c2" {
		t.Fatalf("first msg = %+v", mem.Messages[0])
	}
	if mem.Messages[1].Content != "会话中断前未返回结果" {
		t.Fatalf("synthetic content = %q", mem.Messages[1].Content)
	}
	if info.SyntheticOutputs != 1 || info.DroppedOrphanOutputs != 1 {
		t.Fatalf("info = %+v", info)
	}
	assertRolloutPairingInvariant(t, mem)
}

// ─── ⑮ 大工具输出截断 + output 非字符串重序列化 ───

func TestReadRolloutMemory_ToolOutputTruncation(t *testing.T) {
	big := strings.Repeat("x", 100)
	lines := []string{
		rrLine(rrTs1, "session_meta", rrMeta("sess-1", "/repo", "main")),
		rrLine(rrTs2, "response_item", rrAssistantItem("a1", "call big")),
		rrLine(rrTs2, "response_item", rrCallItem("c1", "big_tool", `{}`)),
		rrLine(rrTs3, "response_item", rrCallOutItem("c1", `{"output":"`+big+`","metadata":{"exit_code":0,"duration_seconds":1}}`)),
	}

	opts := DefaultRolloutReadOptions()
	opts.MaxToolOutputRunes = 10
	path := writeRolloutFixture(t, "trunc.jsonl", lines)
	mem, _, err := ReadRolloutMemory(path, opts)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	toolMsg := mem.Messages[1]
	if !strings.Contains(toolMsg.Content, "已截断") {
		t.Fatalf("truncation mark missing: %q", toolMsg.Content)
	}
	if got := len([]rune(toolMsg.Content)); got >= 100 || got <= 10 {
		t.Fatalf("truncated content rune count = %d, want between 11 and 99", got)
	}
	assertRolloutPairingInvariant(t, mem)

	// 负值=不截断
	optsNoTrunc := opts
	optsNoTrunc.MaxToolOutputRunes = -1
	path2 := writeRolloutFixture(t, "no-trunc.jsonl", lines)
	mem2, _, err2 := ReadRolloutMemory(path2, optsNoTrunc)
	if err2 != nil {
		t.Fatalf("unexpected err: %v", err2)
	}
	if len([]rune(mem2.Messages[1].Content)) != 100 {
		t.Fatalf("negative max must not truncate, got %d runes", len([]rune(mem2.Messages[1].Content)))
	}

	// output 非字符串（对象）→ 重新序列化
	linesObj := []string{
		rrLine(rrTs1, "session_meta", rrMeta("sess-1", "/repo", "main")),
		rrLine(rrTs2, "response_item", rrAssistantItem("a1", "call obj")),
		rrLine(rrTs2, "response_item", rrCallItem("c2", "obj_tool", `{}`)),
		rrLine(rrTs3, "response_item", rrCallOutItem("c2", `{"output":{"k":1,"nested":"v"},"metadata":{"exit_code":9,"duration_seconds":0}}`)),
	}
	path3 := writeRolloutFixture(t, "obj-out.jsonl", linesObj)
	mem3, _, err3 := ReadRolloutMemory(path3, DefaultRolloutReadOptions())
	if err3 != nil {
		t.Fatalf("unexpected err: %v", err3)
	}
	if mem3.Messages[1].Content != `{"k":1,"nested":"v"}` {
		t.Fatalf("object output re-serialized = %q", mem3.Messages[1].Content)
	}
	if mem3.Messages[1].Metadata["exit_code"] != float64(9) {
		t.Fatalf("obj metadata = %+v", mem3.Messages[1].Metadata)
	}
	assertRolloutPairingInvariant(t, mem3)
}

// ─── ⑬ 时间戳单调 ───

func TestReadRolloutMemory_TimestampMonotonic(t *testing.T) {
	lines := []string{
		rrLine(rrTs1, "session_meta", rrMeta("sess-1", "/repo", "main")),
		rrLine("not-a-time", "response_item", rrUserItem("u1", "hi")),
		rrLine("", "response_item", rrAssistantItem("a1", "hello")),
		rrLine("garbage", "response_item", rrAssistantItem("a2", "again")),
	}
	path := writeRolloutFixture(t, "mono.jsonl", lines)
	mem, _, err := ReadRolloutMemory(path, DefaultRolloutReadOptions())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(mem.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(mem.Messages))
	}
	prev := mem.Messages[0].Timestamp
	if prev.IsZero() {
		t.Fatalf("first timestamp must not be zero")
	}
	for i, m := range mem.Messages[1:] {
		if m.Timestamp.Before(prev) {
			t.Fatalf("msg[%d] timestamp %v before prev %v", i, m.Timestamp, prev)
		}
		prev = m.Timestamp
	}
	// 解析失败的行兜底 > 基准（meta 合法时间），保证单调推进
	if !mem.Messages[1].Timestamp.After(mem.Messages[0].Timestamp) {
		t.Fatalf("fallback (+1ms) must advance: %v vs %v", mem.Messages[1].Timestamp, mem.Messages[0].Timestamp)
	}
}

// ─── ⑯ 多 session_meta 容忍 ───

func TestReadRolloutMemory_DuplicateSessionMeta(t *testing.T) {
	// 续写场景：文件中再次出现 meta（不同 sessionID/cwd）——首个为基准，消息不重复
	lines := []string{
		rrLine(rrTs1, "session_meta", rrMeta("sess-1", "/repo/one", "main")),
		rrLine(rrTs2, "response_item", rrUserItem("u1", "hi")),
		rrLine(rrTs3, "session_meta", rrMeta("sess-2", "/repo/two", "dev")),
		rrLine(rrTs4, "response_item", rrAssistantItem("a1", "hello")),
	}
	path := writeRolloutFixture(t, "dup-meta.jsonl", lines)
	mem, info, err := ReadRolloutMemory(path, DefaultRolloutReadOptions())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(mem.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(mem.Messages))
	}
	if info.SessionID != "sess-1" || info.CWD != "/repo/one" || info.GitBranch != "main" {
		t.Fatalf("first meta must win: %+v", info)
	}
	assertRolloutPairingInvariant(t, mem)
}

// ─── R13 SubAgent 文件级标记 ───

func TestReadRolloutMemory_SubAgentFlag(t *testing.T) {
	lines := []string{
		rrLine(rrTs1, "session_meta", rrMeta("sess-1", "/repo", "main")),
		rrLine(rrTs2, "response_item", rrUserItem("u1", "sub task")),
		rrLine(rrTs3, "response_item", rrAssistantItem("a1", "working")),
	}
	opts := DefaultRolloutReadOptions()
	opts.SubAgent = true
	path := writeRolloutFixture(t, "sub.jsonl", lines)
	mem, _, err := ReadRolloutMemory(path, opts)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	for i, m := range mem.Messages {
		if !m.IsSubAgent {
			t.Fatalf("msg[%d] not flagged sub-agent: %+v", i, m)
		}
	}
	// SubAgent 消息在 ToMessages() 中被剔除（既有语义），用 GetMessages 校验
	if len(mem.ToMessages()) != 0 {
		t.Fatalf("sub-agent messages must be filtered by ToMessages")
	}
}
