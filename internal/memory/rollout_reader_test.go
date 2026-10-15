package memory

// rollout_reader_test.go — ReadRolloutMemory 单元测试（表驱动 + 独立场景）。
//
// fixture 均写入 t.TempDir()，手动构造 rollout JSONL 行（时间戳合法格式，
// 行序模拟 executor 的写入顺序）。核心不变式助手 assertRolloutPairingInvariant
// 遍历 mem.ToMessages() 校验 tool 配对完整性。
//
// 覆盖 R1~R16：消息重建与配对归一化（表驱动）、压缩双模式、tail-keep、截断、
// 时间戳单调、重复 meta、SubAgent 标记，以及 R16 的运行态恢复（token_count 累计、
// 结束/中断判定、步数与回合数、悬空工具调用、role=system 丢弃）。

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
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
			// ⑦ 同一 call_id 多条输出 → **保留最后一条**（原位覆盖，与 llm 侧 map 语义一致）
			name: "duplicate_output_keep_last",
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
				if mem.Messages[1].Content != `"second"` {
					t.Fatalf("kept output = %q, want the LAST one (`\"second\"`)", mem.Messages[1].Content)
				}
				if info.SupersededOutputs != 1 {
					t.Fatalf("SupersededOutputs = %d, want 1", info.SupersededOutputs)
				}
				if info.DroppedOrphanOutputs != 0 {
					t.Fatalf("DroppedOrphanOutputs = %d, want 0 (覆盖不是丢弃)", info.DroppedOrphanOutputs)
				}
				assertRolloutPairingInvariant(t, mem)
			},
		},
		{
			// ⑦b 覆盖窗口只在同一个 call 组内：组被下一条 assistant 闭掉之后，
			// 同 id 的后到输出仍是孤儿。不钉这条，last-wins 会退化成"全局 last-wins"，
			// 一个属于第 5 轮的结果就能改写第 1 轮的 output。
			name: "duplicate_after_group_close_is_orphan",
			lines: baseLines(
				rrLine(rrTs3, "response_item", rrAssistantItem("a1", "call")),
				rrLine(rrTs4, "response_item", rrCallItem("c1", "t", `{}`)),
				rrLine(rrTs5, "response_item", rrCallOutItem("c1", `"first"`)),
				rrLine(rrTs6, "response_item", rrAssistantItem("a2", "next turn")),
				rrLine(rrTs7, "response_item", rrCallOutItem("c1", `"too late"`)),
			),
			opts: opts,
			check: func(t *testing.T, mem *ConversationMemory, info RolloutInfo, err error) {
				if err != nil {
					t.Fatalf("unexpected err: %v", err)
				}
				if info.SupersededOutputs != 0 {
					t.Fatalf("SupersededOutputs = %d, want 0 (组已闭合，不该再覆盖)", info.SupersededOutputs)
				}
				if info.DroppedOrphanOutputs != 1 {
					t.Fatalf("DroppedOrphanOutputs = %d, want 1", info.DroppedOrphanOutputs)
				}
				for _, m := range mem.Messages {
					if m.Type == MessageTypeTool && m.Content == `"too late"` {
						t.Fatalf("late duplicate leaked into the recovered conversation")
					}
				}
			},
		},
		{
			// ⑧ 尾部崩溃半行/坏 JSON → 容忍计数
			name: "bad_tail_line_tolerated",
			lines: baseLines(
				rrLine(rrTs3, "response_item", rrUserItem("u1", "hi")),
				`{"timestamp":"`+rrTs4+`","type":"response_item","paylo`, // 崩溃半行
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
				rrLine(rrTs3, "response_item", `{"type":"message","role":"user","id":"u1","content":[{"type":"input_text","text":"part-a"},{"type":"input_text","text":"part-b"}]}`),
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
				rrLine(rrTs3, "response_item", `{"type":"message","role":"user","id":"u1","content":[{"type":"image","text":"pic-1"},{"type":"misc","text":"txt-2"}]}`),
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
	memTail, _, err := ReadRolloutMemory(pathTail, optsTail)
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
	return "c-round-" + strconv.Itoa(round)
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

// ─── R16 event_msg 运行态恢复 ───

// rrTokenCount token_count 事件 payload（形状对齐 RolloutWriter.WriteTokenCount：
// total_token_usage 为累计值，last_token_usage 为本次调用值）。
func rrTokenCount(in, cached, out, total, lastIn, lastCached int64) string {
	return `{"type":"token_count","info":{` +
		`"total_token_usage":{"input_tokens":` + strconv.FormatInt(in, 10) +
		`,"cached_input_tokens":` + strconv.FormatInt(cached, 10) +
		`,"output_tokens":` + strconv.FormatInt(out, 10) +
		`,"total_tokens":` + strconv.FormatInt(total, 10) + `},` +
		`"last_token_usage":{"input_tokens":` + strconv.FormatInt(lastIn, 10) +
		`,"cached_input_tokens":` + strconv.FormatInt(lastCached, 10) +
		`,"output_tokens":0,"total_tokens":0}}}`
}

// rrSystemItem role=system message payload（写侧落盘的 Director system prompt）。
func rrSystemItem(id, text string) string {
	return `{"type":"message","role":"system","id":"` + id + `","content":[{"type":"input_text","text":"` + text + `"}]}`
}

// TestReadRolloutMemory_TokenCountRestore token_count 恢复为累计用量：
// total_token_usage 是写侧已累计的值，多条事件必须「后到者胜」而非再次累加。
func TestReadRolloutMemory_TokenCountRestore(t *testing.T) {
	lines := []string{
		rrLine(rrTs1, "session_meta", rrMeta("sess-1", "/repo", "main")),
		rrLine(rrTs2, "event_msg", rrTokenCount(100, 30, 40, 140, 60, 10)),
		rrLine(rrTs3, "response_item", rrUserItem("u1", "hi")),
		rrLine(rrTs4, "event_msg", rrTokenCount(250, 80, 90, 340, 150, 50)),
		rrLine(rrTs5, "response_item", rrAssistantItem("a1", "hello")),
		rrLine(rrTs6, "event_msg", `{"type":"task_complete"}`),
	}
	path := writeRolloutFixture(t, "tokens.jsonl", lines)
	mem, info, err := ReadRolloutMemory(path, DefaultRolloutReadOptions())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// token_count 不得产生消息（response_item 是消息唯一事实源）
	if len(mem.Messages) != 2 {
		t.Fatalf("messages = %d, want 2 (event_msg must not create messages): %+v", len(mem.Messages), mem.Messages)
	}
	want := RolloutTokenTotals{Input: 250, CachedInput: 80, Output: 90, Total: 340, LastContextTokens: 200}
	if info.Tokens != want {
		t.Fatalf("Tokens = %+v, want %+v", info.Tokens, want)
	}
}

// TestReadRolloutMemory_FinishState 结束状态三态判定。
//
// 存量文件里终止事件大面积缺失（executor 曾把 task_complete 写入包在
// cfg.OnAgentExit != nil 条件内），故「无终止事件」不能直接等于「中断」：
// 还要看有没有悬空工具调用、末条是不是最终答复。
func TestReadRolloutMemory_FinishState(t *testing.T) {
	base := []string{
		rrLine(rrTs1, "session_meta", rrMeta("sess-1", "/repo", "main")),
		rrLine(rrTs2, "event_msg", `{"type":"task_started"}`),
		rrLine(rrTs3, "response_item", rrUserItem("u1", "do it")),
		rrLine(rrTs4, "response_item", rrAssistantItem("a1", "这是最终答复")),
	}
	read := func(t *testing.T, name string, lines []string) RolloutInfo {
		t.Helper()
		_, info, err := ReadRolloutMemory(writeRolloutFixture(t, name, lines), DefaultRolloutReadOptions())
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		return info
	}

	t.Run("task_complete", func(t *testing.T) {
		lines := append(append([]string{}, base...), rrLine(rrTs5, "event_msg", `{"type":"task_complete"}`))
		info := read(t, "done.jsonl", lines)
		if !info.Finished || info.Aborted {
			t.Fatalf("evidence = finished:%v aborted:%v, want finished only", info.Finished, info.Aborted)
		}
		if got := info.FinishState(); got != FinishComplete {
			t.Fatalf("FinishState = %v, want complete", got)
		}
	})

	t.Run("turn_aborted", func(t *testing.T) {
		lines := append(append([]string{}, base...),
			rrLine(rrTs5, "event_msg", `{"type":"turn_aborted","reason":"context canceled"}`))
		info := read(t, "aborted.jsonl", lines)
		if !info.Aborted || info.Finished || info.AbortReason != "context canceled" {
			t.Fatalf("evidence = %+v, want aborted with reason", info)
		}
		// turn_aborted 是强证据：即便末条看起来像最终答复也判中断
		if got := info.FinishState(); got != FinishInterrupted {
			t.Fatalf("FinishState = %v, want interrupted", got)
		}
	})

	t.Run("no_terminal_event_but_final_answer", func(t *testing.T) {
		// 存量文件常态：终止事件从未写入，但末条 assistant 已给出最终答复 → 判完成，
		// 不能误报为中断（否则续跑确认框会在正常会话上弹出）
		info := read(t, "legacy-complete.jsonl", base)
		if info.Finished || info.Aborted {
			t.Fatalf("evidence = %+v, want no terminal event", info)
		}
		if !info.EndsWithFinalAnswer {
			t.Fatal("EndsWithFinalAnswer = false, want true")
		}
		if got := info.FinishState(); got != FinishComplete {
			t.Fatalf("FinishState = %v, want complete", got)
		}
	})

	t.Run("no_terminal_event_with_dangling_call", func(t *testing.T) {
		// 崩在工具执行中：无终止事件 + 悬空 call → 中断
		lines := []string{
			rrLine(rrTs1, "session_meta", rrMeta("sess-1", "/repo", "main")),
			rrLine(rrTs2, "event_msg", `{"type":"task_started"}`),
			rrLine(rrTs3, "response_item", rrUserItem("u1", "do it")),
			rrLine(rrTs4, "response_item", rrAssistantItem("a1", "calling")),
			rrLine(rrTs5, "response_item", rrCallItem("c1", "run_bash", `{}`)),
		}
		info := read(t, "crashed-midtool.jsonl", lines)
		if info.Finished || info.Aborted {
			t.Fatalf("evidence = %+v, want no terminal event", info)
		}
		if len(info.DanglingToolCalls) != 1 {
			t.Fatalf("DanglingToolCalls = %+v, want 1", info.DanglingToolCalls)
		}
		if got := info.FinishState(); got != FinishInterrupted {
			t.Fatalf("FinishState = %v, want interrupted", got)
		}
	})

	t.Run("no_terminal_event_no_signal", func(t *testing.T) {
		// 末条是 user（模型还没回）且无悬空 call → 证据不足，老实报 Unknown
		lines := []string{
			rrLine(rrTs1, "session_meta", rrMeta("sess-1", "/repo", "main")),
			rrLine(rrTs2, "response_item", rrUserItem("u1", "do it")),
		}
		info := read(t, "unknown.jsonl", lines)
		if got := info.FinishState(); got != FinishUnknown {
			t.Fatalf("FinishState = %v, want unknown", got)
		}
		if got := info.FinishState().String(); got != "unknown" {
			t.Fatalf("String() = %q, want unknown", got)
		}
	})
}

// TestReadRolloutMemory_StepAndTurnCount 步数近似值：Director 文件用 AssistantTurns，
// sub-agent 文件用 sub_agent_activity 的 MaxStepObserved。
func TestReadRolloutMemory_StepAndTurnCount(t *testing.T) {
	lines := []string{
		rrLine(rrTs1, "session_meta", rrMeta("sess-1", "/repo", "main")),
		rrLine(rrTs2, "response_item", rrUserItem("u1", "task")),
		rrLine(rrTs3, "response_item", rrAssistantItem("a1", "step 1")),
		rrLine(rrTs4, "event_msg", `{"type":"sub_agent_activity","step_number":2,"tool_name":"read_file","success":true}`),
		rrLine(rrTs5, "response_item", rrCallItem("c1", "read_file", `{}`)),
		rrLine(rrTs6, "response_item", rrCallOutItem("c1", `"body"`)),
		// 无正文的 assistant（仅 tool_calls，由 R5 合成）同样代表一次真实回合
		rrLine(rrTs6, "response_item", rrCallItem("c2", "list_dir", `{}`)),
		rrLine(rrTs7, "response_item", rrCallOutItem("c2", `"files"`)),
		rrLine(rrTs7, "event_msg", `{"type":"task_complete"}`),
	}
	path := writeRolloutFixture(t, "steps.jsonl", lines)
	mem, info, err := ReadRolloutMemory(path, DefaultRolloutReadOptions())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if info.MaxStepObserved != 2 {
		t.Fatalf("MaxStepObserved = %d, want 2", info.MaxStepObserved)
	}
	// a1 + c2 的合成宿主 = 2 条 assistant
	if info.AssistantTurns != 2 {
		t.Fatalf("AssistantTurns = %d, want 2: %+v", info.AssistantTurns, mem.Messages)
	}
	if info.LastUserInput != "task" {
		t.Fatalf("LastUserInput = %q, want 'task'", info.LastUserInput)
	}
}

// TestReadRolloutMemory_LastUserInputIsLastTurn 写侧每轮重写完整历史，
// LastUserInput 必须取最后一条 user（= 本轮任务目标），而非首条。
func TestReadRolloutMemory_LastUserInputIsLastTurn(t *testing.T) {
	lines := []string{
		rrLine(rrTs1, "session_meta", rrMeta("sess-1", "/repo", "main")),
		rrLine(rrTs2, "response_item", rrUserItem("u1", "第一轮目标")),
		rrLine(rrTs3, "response_item", rrAssistantItem("a1", "done")),
		rrLine(rrTs4, "response_item", rrUserItem("u2", "第二轮目标")),
		rrLine(rrTs5, "response_item", rrAssistantItem("a2", "working")),
	}
	path := writeRolloutFixture(t, "last-user.jsonl", lines)
	_, info, err := ReadRolloutMemory(path, DefaultRolloutReadOptions())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if info.LastUserInput != "第二轮目标" {
		t.Fatalf("LastUserInput = %q, want 第二轮目标", info.LastUserInput)
	}
}

// TestReadRolloutMemory_DanglingToolCalls 悬空 call 除计数外还要留下 id+name，
// 供 TUI 时间线标记与续跑提示使用。
func TestReadRolloutMemory_DanglingToolCalls(t *testing.T) {
	lines := []string{
		rrLine(rrTs1, "session_meta", rrMeta("sess-1", "/repo", "main")),
		rrLine(rrTs2, "response_item", rrUserItem("u1", "go")),
		rrLine(rrTs3, "response_item", rrAssistantItem("a1", "calling two")),
		rrLine(rrTs4, "response_item", rrCallItem("c1", "read_file", `{}`)),
		rrLine(rrTs5, "response_item", rrCallItem("c2", "run_bash", `{}`)),
		rrLine(rrTs6, "response_item", rrCallOutItem("c1", `"ok"`)),
		// c2 无输出 → 悬空
	}
	path := writeRolloutFixture(t, "dangling.jsonl", lines)
	_, info, err := ReadRolloutMemory(path, DefaultRolloutReadOptions())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if info.SyntheticOutputs != 1 {
		t.Fatalf("SyntheticOutputs = %d, want 1", info.SyntheticOutputs)
	}
	if len(info.DanglingToolCalls) != 1 {
		t.Fatalf("DanglingToolCalls = %+v, want exactly 1", info.DanglingToolCalls)
	}
	got := info.DanglingToolCalls[0]
	if got.ID != "c2" || got.Name != "run_bash" {
		t.Fatalf("dangling call = %+v, want {c2 run_bash}", got)
	}
}

// ─── role=system 消息处理（默认丢弃 / 显式保留） ───

func TestReadRolloutMemory_SystemMessageDroppedByDefault(t *testing.T) {
	lines := []string{
		rrLine(rrTs1, "session_meta", rrMeta("sess-1", "/repo", "main")),
		rrLine(rrTs2, "response_item", rrSystemItem("s1", "You are Director... (15k chars)")),
		rrLine(rrTs3, "response_item", rrUserItem("u1", "hi")),
		rrLine(rrTs4, "response_item", rrAssistantItem("a1", "hello")),
	}

	// 默认：system prompt 不进消息（Planner 续跑时本就跳过 MessageTypeSystem）
	path := writeRolloutFixture(t, "sys-default.jsonl", lines)
	mem, info, err := ReadRolloutMemory(path, DefaultRolloutReadOptions())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(mem.Messages) != 2 {
		t.Fatalf("messages = %d, want 2 (system dropped): %+v", len(mem.Messages), mem.Messages)
	}
	for _, m := range mem.Messages {
		if m.Type == MessageTypeSystem {
			t.Fatalf("system message must be dropped by default: %+v", m)
		}
	}
	if info.SkippedSystemMessages != 1 {
		t.Fatalf("SkippedSystemMessages = %d, want 1", info.SkippedSystemMessages)
	}

	// KeepSystemMessages：对称恢复
	keepOpts := DefaultRolloutReadOptions()
	keepOpts.KeepSystemMessages = true
	path2 := writeRolloutFixture(t, "sys-keep.jsonl", lines)
	mem2, info2, err2 := ReadRolloutMemory(path2, keepOpts)
	if err2 != nil {
		t.Fatalf("keep: %v", err2)
	}
	if len(mem2.Messages) != 3 || mem2.Messages[0].Type != MessageTypeSystem {
		t.Fatalf("keep messages = %+v, want system first", mem2.Messages)
	}
	if info2.SkippedSystemMessages != 0 {
		t.Fatalf("SkippedSystemMessages = %d, want 0 when keeping", info2.SkippedSystemMessages)
	}
}

// TestReadRolloutMemory_SystemPromptOnlyFile 仅有 system prompt 的文件在默认选项下
// 恢复不出消息 → 走既有的零消息错误路径，不 panic。
func TestReadRolloutMemory_SystemPromptOnlyFile(t *testing.T) {
	lines := []string{
		rrLine(rrTs1, "session_meta", rrMeta("sess-1", "/repo", "main")),
		rrLine(rrTs2, "response_item", rrSystemItem("s1", "prompt only")),
	}
	path := writeRolloutFixture(t, "sys-only.jsonl", lines)
	mem, info, err := ReadRolloutMemory(path, DefaultRolloutReadOptions())
	if err == nil {
		t.Fatalf("system-only rollout should error, got %+v", mem)
	}
	if !errors.Is(err, errNoRolloutMessages) {
		t.Fatalf("err = %v, want errNoRolloutMessages wrapped", err)
	}
	// 出错也要带回已解析的状态
	if info.SkippedSystemMessages != 1 || info.Entries != 2 {
		t.Fatalf("info = %+v", info)
	}
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
