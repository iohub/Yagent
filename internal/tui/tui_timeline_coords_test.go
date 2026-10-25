package tui

import (
	"testing"
	"time"
)

// 耗时面板（⧗ LATENCY）的"这一轮 agent 里发生了什么"只能按 DelegationID 归组 ——
// 子 Agent 的调用不进 Director 的记忆。所以每一条时间线都必须带着自己的坐标出生。
//
// 这条闸门刻意走**生产路径**（m.Update(taskEventMsg)），不自己拼 TimelineEntry：
// 七个构造点里漏填一个，这里就是红的；只在测试里造带坐标的夹具等于没测
// （根 AGENTS.md §11 坑 33）。

// TestTimelineEntryCarriesDelegationCoordinates 每一种会新建条目的事件都要产出
// 带 Agent + DelegationID 的条目。
func TestTimelineEntryCarriesDelegationCoordinates(t *testing.T) {
	// 事件类型 → 能让该分支真的产出条目的 content。
	// 少一个用例不是"少测一种"，而是那一处构造点从此没人看守。
	cases := []struct {
		name    string
		typ     string
		content interface{}
	}{
		{"llm_call_start", "llm_call_start", map[string]interface{}{"model": "test-model"}},
		{"thinking", "thinking", "一段思考"},
		{"commit_context", "commit_context_loaded", map[string]interface{}{"count": float64(3)}},
		{"rule", "context_compressed", map[string]interface{}{
			"original_tokens": 9000, "compressed_tokens": 6000, "saved_tokens": 3000}},
		{"emergency", "context_emergency_compressed", map[string]interface{}{
			"original_tokens": 9000, "compressed_tokens": 6000, "saved_tokens": 3000}},
		{"ultimate", "context_ultimate_compressed", map[string]interface{}{
			"original_tokens": 9000, "compressed_tokens": 6000, "saved_tokens": 3000}},
		{"tool_call_start", "tool_call_start", map[string]interface{}{
			"tool_name": "read_file", "tool_call_id": "call_tool_1", "arguments": `{"path":"a.go"}`}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newTestModel()
			e := buildEvent(tc.typ, "Coding", tc.content)
			e.DelegationID = "call_delegate_1"
			feedEvent(m, e)

			if len(m.timelineEntries) != 1 {
				t.Fatalf("%s 没有产出时间线条目（共 %d 条）—— 该分支本身没跑到，坐标断言就是空跑",
					tc.name, len(m.timelineEntries))
			}
			got := m.timelineEntries[0]
			if got.Agent != "Coding" {
				t.Errorf("%s 的 Agent = %q, want \"Coding\"（构造点漏填 msg.event.From）", tc.name, got.Agent)
			}
			if got.DelegationID != "call_delegate_1" {
				t.Errorf("%s 的 DelegationID = %q, want \"call_delegate_1\"（构造点漏填 event.DelegationID）",
					tc.name, got.DelegationID)
			}
		})
	}
}

// TestDirectorOwnEventsCarryEmptyDelegation Director 自己的调用没有委派坐标，
// 那两列必须是空的而不是"某个子 Agent 的 ID" —— 否则轮次归组会把 Director
// 自己的动作算进某一轮里，那一轮的计数与最慢值都跟着错。
func TestDirectorOwnEventsCarryEmptyDelegation(t *testing.T) {
	m := newTestModel()
	e := buildEvent("tool_call_start", "director", map[string]interface{}{
		"tool_name": "read_file", "tool_call_id": "call_own_1", "arguments": `{"path":"a.go"}`})
	feedEvent(m, e)

	if len(m.timelineEntries) != 1 {
		t.Fatalf("条目数 = %d, want 1", len(m.timelineEntries))
	}
	if got := m.timelineEntries[0].DelegationID; got != "" {
		t.Errorf("DelegationID = %q, want 空（Director 的调用不属于任何一轮委派）", got)
	}
	if got := m.timelineEntries[0].Agent; got != "director" {
		t.Errorf("Agent = %q, want \"director\"", got)
	}
}

// TestParallelSameToolDoesNotMergeAcrossDelegations 两个 Coding 同时 read_file
// 不能串成同一个节点：那两件事对"哪一轮慢"是完全不同的答案。
// 旧实现只看"最后一条 + 同名"，并行委派下必然合并。
func TestParallelSameToolDoesNotMergeAcrossDelegations(t *testing.T) {
	m := newTestModel()
	for i, delegation := range []string{"call_coding_a", "call_coding_b"} {
		e := buildEvent("tool_call_start", "Coding", map[string]interface{}{
			"tool_name":    "read_file",
			"tool_call_id": "call_tool_1",
			"arguments":    `{"path":"a.go"}`,
		})
		e.DelegationID = delegation
		e.Timestamp = time.Now().Add(time.Duration(i) * time.Second)
		feedEvent(m, e)
	}

	if len(m.timelineEntries) != 2 {
		names := make([]string, 0, len(m.timelineEntries))
		for _, te := range m.timelineEntries {
			names = append(names, te.Name)
		}
		t.Fatalf("条目数 = %d, want 2 —— 不同轮委派的同名工具被合并成了一个节点：%v",
			len(m.timelineEntries), names)
	}
	if m.timelineEntries[0].MergedCount() != 1 || m.timelineEntries[1].MergedCount() != 1 {
		t.Errorf("两条都该只有自己：×%d 与 ×%d",
			m.timelineEntries[0].MergedCount(), m.timelineEntries[1].MergedCount())
	}
}

// TestSameDelegationStillMergesSameTool 反向的一半：坐标相同时要照旧合并，
// 否则"只让位不合并"会把这条闸门变成恒真断言。
func TestSameDelegationStillMergesSameTool(t *testing.T) {
	m := newTestModel()
	for i, id := range []string{"call_tool_1", "call_tool_2"} {
		e := buildEvent("tool_call_start", "Coding", map[string]interface{}{
			"tool_name":    "read_file",
			"tool_call_id": id,
			"arguments":    `{"path":"a.go"}`,
		})
		e.DelegationID = "call_coding_a"
		e.Timestamp = time.Now().Add(time.Duration(i) * time.Second)
		feedEvent(m, e)
	}

	if len(m.timelineEntries) != 1 {
		t.Fatalf("条目数 = %d, want 1（同一轮的连续同类工具必须合并）", len(m.timelineEntries))
	}
	if got := m.timelineEntries[0].MergedCount(); got != 2 {
		t.Errorf("MergedCount = %d, want 2", got)
	}
}

// TestLLMCallEndClosesOwnAgentRow A 的 llm_call_end 不许关掉 B 那一行。
// 旧实现找的是"最后一条 running 的 LLMCall"、不看身份，并行委派下时长互串。
func TestLLMCallEndClosesOwnAgentRow(t *testing.T) {
	m := newTestModel()

	start := func(agent, delegation string) {
		e := buildEvent("llm_call_start", agent, map[string]interface{}{"model": "m"})
		e.DelegationID = delegation
		e.Timestamp = time.Now()
		feedEvent(m, e)
	}
	start("Coding", "call_a")
	start("Repo", "call_b")
	if n := len(m.timelineEntries); n != 2 {
		t.Fatalf("条目数 = %d, want 2", n)
	}

	end := buildEvent("llm_call_end", "Coding", map[string]interface{}{"model": "m"})
	end.DelegationID = "call_a"
	end.Metadata = map[string]interface{}{"duration_seconds": 2.5}
	feedEvent(m, end)

	if got := m.timelineEntries[0].Status; got != ToolStatusSuccess {
		t.Errorf("Coding 那一行 Status = %v, want Success", got)
	}
	if got := m.timelineEntries[0].Duration; got != 2500*time.Millisecond {
		t.Errorf("Coding 那一行 Duration = %v, want 2.5s", got)
	}
	if got := m.timelineEntries[1].Status; got != ToolStatusRunning {
		t.Errorf("Repo 那一行被别人的 llm_call_end 关掉了：Status = %v, want 仍是 Running", got)
	}
}

// TestAddTimelineEntryInvalidatesPanelCache 收口那六遍手写的失效：走 addTimelineEntry
// 之后浮层缓存必须作废，否则"事件到了、浮层还停在旧内容"。
func TestAddTimelineEntryInvalidatesPanelCache(t *testing.T) {
	m := newTestModel()
	m.timelineCacheKey = "stale"
	m.addTimelineEntry(&TimelineEntry{ID: "x", Kind: TimelineKindTool, Name: "read_file"})
	if m.timelineCacheKey != "" {
		t.Errorf("timelineCacheKey = %q, want 空（新条目必须让浮层缓存作废）", m.timelineCacheKey)
	}
	if n := len(m.timelineEntries); n != 1 {
		t.Fatalf("条目数 = %d, want 1", n)
	}
}
