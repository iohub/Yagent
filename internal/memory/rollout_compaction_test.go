package memory

import (
	"encoding/json"
	"testing"
)

// rrCompactedFull 新格式 compacted payload（带 kind/saved_tokens）。
// 直接 marshal 结构体：这样 JSON tag 的线上格式也被测试钉住。
func rrCompactedFull(t *testing.T, summary, kind string, before, after, saved int) string {
	t.Helper()
	return rrMarshal(t, CompactedPayload{
		Summary: summary, TokensBefore: before, TokensAfter: after,
		Kind: kind, SavedTokens: saved,
	})
}

// rrCompactedLegacy 老格式 compacted payload：只有 summary/tokens_*。
func rrCompactedLegacy(t *testing.T, summary string, before, after int) string {
	t.Helper()
	return rrMarshal(t, CompactedPayload{Summary: summary, TokensBefore: before, TokensAfter: after})
}

func rrMarshal(t *testing.T, p CompactedPayload) string {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal CompactedPayload: %v", err)
	}
	return string(b)
}

// TestReadRolloutMemory_CompactionRecords 钉住 §8.2/§8.3：
//   - 压缩记录在**任何** Compaction 模式下都要收集（TUI 的 ⧉ 段靠它重建）；
//   - 老 rollout 缺 kind 时按 Summary 前缀归类，缺 saved 时用 before-after 回落；
//   - 归类不了的整条跳过，不猜成 rule。
func TestReadRolloutMemory_CompactionRecords(t *testing.T) {
	lines := []string{
		rrLine(rrTs1, "session_meta", rrMeta("sess-1", "/repo", "main")),
		rrLine(rrTs2, "response_item", rrUserItem("u1", "before")),
		rrLine(rrTs3, "response_item", rrAssistantItem("a1", "answer")),
		// 新格式：三档各一条
		rrLine(rrTs4, "compacted", rrCompactedFull(t, "tool-result truncation: 2 results compacted", "rule", 9000, 6000, 3000)),
		rrLine(rrTs5, "compacted", rrCompactedFull(t, "emergency compression: 3 blocks extracted", "emergency", 6000, 4000, 2000)),
		rrLine(rrTs6, "compacted", rrCompactedFull(t, "ultimate compression: context rebuilt", "ultimate", 4000, 1000, 3000)),
		// 老格式：靠 Summary 前缀认档，saved 用 before-after
		rrLine(rrTs7, "compacted", rrCompactedLegacy(t, "emergency compression: 9 blocks extracted, 8 summarized, 3 kept, saved 1200 tokens (5000 → 3800)", 5000, 3800)),
		rrLine(rrTs7, "compacted", rrCompactedLegacy(t, "tool-result truncation: 1 results compacted, saved 200 tokens (1000 → 800)", 1000, 800)),
		// 未来版本写来的陌生 kind 值：退回前缀判定，不要整条丢掉
		rrLine(rrTs7, "compacted", rrCompactedFull(t, "emergency compression: 1 block kept", "some-future-kind", 700, 300, 400)),
		// 认不出来：整条跳过
		rrLine(rrTs7, "compacted", rrCompactedLegacy(t, "manual snapshot of something else", 100, 1)),
		rrLine(rrTs7, "response_item", rrUserItem("u2", "after")),
	}
	path := writeRolloutFixture(t, "compactions.jsonl", lines)

	want := []CompactionRecord{
		{Kind: "rule", Saved: 3000, TokensBefore: 9000, TokensAfter: 6000},
		{Kind: "emergency", Saved: 2000, TokensBefore: 6000, TokensAfter: 4000},
		{Kind: "ultimate", Saved: 3000, TokensBefore: 4000, TokensAfter: 1000},
		{Kind: "emergency", Saved: 1200, TokensBefore: 5000, TokensAfter: 3800},
		{Kind: "rule", Saved: 200, TokensBefore: 1000, TokensAfter: 800},
		{Kind: "emergency", Saved: 400, TokensBefore: 700, TokensAfter: 300},
	}

	for _, tc := range []struct {
		name string
		opts RolloutReadOptions
	}{
		{"full", DefaultRolloutReadOptions()},
		{"summary-tail", func() RolloutReadOptions {
			o := DefaultRolloutReadOptions()
			o.Compaction = CompactionSummaryTail
			return o
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, info, err := ReadRolloutMemory(path, tc.opts)
			if err != nil {
				t.Fatalf("ReadRolloutMemory: %v", err)
			}
			if len(info.Compressions) != len(want) {
				t.Fatalf("压缩记录 = %d 条, want %d：%+v", len(info.Compressions), len(want), info.Compressions)
			}
			for i, w := range want {
				if got := info.Compressions[i]; got != w {
					t.Errorf("第 %d 条 = %+v, want %+v", i, got, w)
				}
			}
		})
	}
}

// TestRolloutWriter_CompactedRoundTrip 走真实写入路径，钉住 JSON tag 与档位常量：
// 写入端的 kind 与读取端认的 kind 必须是同一份词表，否则 /resume 会静默丢掉整档统计。
func TestRolloutWriter_CompactedRoundTrip(t *testing.T) {
	isolateRolloutHome(t)
	writer, err := NewRolloutWriter("director", "task-cmp-1", "proj")
	if err != nil {
		t.Fatalf("NewRolloutWriter: %v", err)
	}
	if err := writer.WriteSessionMeta(SessionMeta{ID: "s1", SessionID: "s1", Cwd: "/repo"}); err != nil {
		t.Fatalf("WriteSessionMeta: %v", err)
	}
	if err := writer.WriteResponseItem(ResponseItem{
		Type: "message", Role: "user",
		Content: []MessageContentItem{{Type: "input_text", Text: "do the thing"}},
	}); err != nil {
		t.Fatalf("WriteResponseItem: %v", err)
	}
	for _, p := range []CompactedPayload{
		{Summary: "tool-result truncation: 3 results compacted", TokensBefore: 9000, TokensAfter: 6000,
			Kind: CompactionKindRule, SavedTokens: 3000},
		{Summary: "emergency compression: 4 blocks extracted", TokensBefore: 6000, TokensAfter: 2000,
			Kind: CompactionKindEmergency, SavedTokens: 4000},
	} {
		if err := writer.WriteCompacted(p); err != nil {
			t.Fatalf("WriteCompacted: %v", err)
		}
	}
	if err := writer.WriteRunComplete(RunState{Steps: 2, StopReason: "agent_exit"}); err != nil {
		t.Fatalf("WriteRunComplete: %v", err)
	}
	path := writer.FilePath()
	if err := writer.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, info, err := ReadRolloutMemory(path, DefaultRolloutReadOptions())
	if err != nil {
		t.Fatalf("ReadRolloutMemory: %v", err)
	}
	want := []CompactionRecord{
		{Kind: CompactionKindRule, Saved: 3000, TokensBefore: 9000, TokensAfter: 6000},
		{Kind: CompactionKindEmergency, Saved: 4000, TokensBefore: 6000, TokensAfter: 2000},
	}
	if len(info.Compressions) != len(want) {
		t.Fatalf("records = %+v, want %d 条", info.Compressions, len(want))
	}
	for i, w := range want {
		if got := info.Compressions[i]; got != w {
			t.Errorf("第 %d 条 = %+v, want %+v", i, got, w)
		}
	}
}

// TestReadRolloutMemory_CompactionRecordKeepsZeroSaved 盯住边界：saved_tokens 缺失
// 且 before==after（压缩没省到东西）时记录仍然成立，只是 saved=0 ——
// 触发次数本身就是有价值的信息。
func TestReadRolloutMemory_CompactionRecordKeepsZeroSaved(t *testing.T) {
	lines := []string{
		rrLine(rrTs1, "session_meta", rrMeta("sess-2", "/repo", "main")),
		rrLine(rrTs2, "response_item", rrUserItem("u1", "q")),
		rrLine(rrTs3, "response_item", rrAssistantItem("a1", "a")),
		rrLine(rrTs4, "compacted", rrCompactedLegacy(t, "ultimate compression: context rebuilt", 500, 500)),
	}
	path := writeRolloutFixture(t, "compaction-zero.jsonl", lines)

	_, info, err := ReadRolloutMemory(path, DefaultRolloutReadOptions())
	if err != nil {
		t.Fatalf("ReadRolloutMemory: %v", err)
	}
	if len(info.Compressions) != 1 {
		t.Fatalf("records = %+v, want 1 条", info.Compressions)
	}
	if got := info.Compressions[0]; got.Kind != "ultimate" || got.Saved != 0 {
		t.Errorf("record = %+v, want kind ultimate / saved 0", got)
	}
}
