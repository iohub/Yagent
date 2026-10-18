package tui

// tui_parallel_stream_test.go — C5 的界面真值闸门。
//
// 钉的是"两股并发流各归各位"以及它的反面：没有委派坐标时行为与改动前逐字一致。
// 后者存在的意义是证明修的是**坐标缺失**，而不是顺手改了别的流式语义。

import (
	"strings"
	"testing"

	"yagent/internal/messaging"
)

func streamEvent(typ, from, delegationID string, content interface{}) *messaging.MessageEvent {
	e := buildEvent(typ, from, content)
	e.DelegationID = delegationID
	return e
}

// 同名 Agent 的两条流必须落在各自的条目里。
//
// 改动之前：ai_chunk 只按 From 分槽，第二个 ai_stream_start 把 slot 覆盖成自己的 idx，
// 于是两股文字一起涌进后建的那条条目，前一条永远空着 —— 用户看到的是"回答粘成一坨"，
// 而日志里没有任何线索。
func TestParallelStreamsSameAgentDoNotMerge(t *testing.T) {
	m := newTestModel()

	m = feedEvent(m, streamEvent("ai_stream_start", "Coding", "call_1", nil))
	m = feedEvent(m, streamEvent("ai_stream_start", "Coding", "call_2", nil))

	for i := 0; i < 2; i++ {
		m = feedEvent(m, streamEvent("ai_chunk", "Coding", "call_1", "AAA"))
		m = feedEvent(m, streamEvent("ai_chunk", "Coding", "call_2", "BBB"))
	}
	m = feedEvent(m, streamEvent("ai_stream_end", "Coding", "call_1", nil))
	m = feedEvent(m, streamEvent("ai_stream_end", "Coding", "call_2", nil))

	got := streamEntryContents(m)
	if len(got) != 2 {
		t.Fatalf("流式条目数 = %d, want 2：%v", len(got), got)
	}
	first, second := got[0], got[1]
	if !strings.Contains(first, "AAA") || strings.Contains(first, "BBB") {
		t.Errorf("第一条流的内容 = %q, 只该有 AAA —— 两股流粘在一起了", first)
	}
	if !strings.Contains(second, "BBB") || strings.Contains(second, "AAA") {
		t.Errorf("第二条流的内容 = %q, 只该有 BBB", second)
	}
	if len(m.aiChunkBuffers) != 0 {
		t.Errorf("两条流结束后还留着 %d 个缓冲槽, want 0（槽会随 tab 切换被带走，攒着就是内存与错位）", len(m.aiChunkBuffers))
	}
}

// 没有委派坐标（Director 自己的输出、/resume 恢复的存量 rollout）时退回按 Agent 名分槽。
// 这条刻意断言"仍然会粘在一起"：它证明改变只来自坐标，不来自别处。
func TestLegacyStreamsWithoutDelegationKeepOldBehavior(t *testing.T) {
	m := newTestModel()

	m = feedEvent(m, buildEvent("ai_stream_start", "Coding", nil))
	m = feedEvent(m, buildEvent("ai_stream_start", "Coding", nil))
	m = feedEvent(m, buildEvent("ai_chunk", "Coding", "AAA"))
	m = feedEvent(m, buildEvent("ai_chunk", "Coding", "BBB"))
	m = feedEvent(m, buildEvent("ai_stream_end", "Coding", nil))

	got := streamEntryContents(m)
	if len(got) != 2 {
		t.Fatalf("流式条目数 = %d, want 2", len(got))
	}
	if got[0] != "" {
		t.Errorf("第一条条目 = %q, want 空 —— 无坐标时不该出现新行为", got[0])
	}
	if got[1] != "AAABBB" {
		t.Errorf("第一条条目 = %q, want AAABBB（按名字共用一个槽）", got[1])
	}
}

func TestStreamSlotKeyShape(t *testing.T) {
	if got := streamSlotKey(&messaging.MessageEvent{DelegationID: ""}, "Coding"); got != "Coding" {
		t.Errorf("无坐标时的键 = %q, want 原名", got)
	}
	if got := streamSlotKey(&messaging.MessageEvent{DelegationID: "call_9"}, "Coding"); got != "Coding#call_9" {
		t.Errorf("有坐标时的键 = %q, want Coding#call_9", got)
	}
	if got := streamSlotKey(nil, "Coding"); got != "Coding" {
		t.Errorf("nil 事件 = %q, want 原名（不能让界面因为一条缺事件panic）", got)
	}
}

// streamEntryContents 返回所有 ai_stream/ai_response 条目的正文，按创建顺序。
func streamEntryContents(m *model) []string {
	var out []string
	for _, le := range m.logEntries {
		if le.eventType == "ai_stream" || le.eventType == "ai_response" {
			c := le.streamContent
			if c == "" {
				c = le.content
			}
			out = append(out, c)
		}
	}
	return out
}
