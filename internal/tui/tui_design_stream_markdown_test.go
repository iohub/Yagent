package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"yagent/internal/messaging"
)

// design / deepthinking 的 StreamHandler 只发 ai_chunk（internal/app/app.go），
// 既没有 ai_stream_start、也没有对应 From 的 ai_stream_end / ai_response：父 Agent
// 那两条用的是父 Agent 名做槽键。结果这条流的条目 streaming 恒为 true，
// renderSingleEntry 永远走原文分支 —— 用户看到的就是 "# Role"、"## 1." 这些裸 markdown。
// 下面这组用例锁住补好的闭环：tool_call_result 收尾 + 定稿后一次性 markdown。

// toolResultEvent 造一条 tool_call_result 事件。result 刻意照抄生产形状：
// internal/tools/adapter.go 用 json.Marshal(result) 序列化正文，所以它是**带引号的
// JSON 字符串字面量**，不是裸字符串。
func toolResultEvent(t *testing.T, agent, toolName, callID, body string) *messaging.MessageEvent {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal tool result: %v", err)
	}
	return buildEvent("tool_call_result", agent, map[string]interface{}{
		"tool_name":    toolName,
		"tool_call_id": callID,
		"result":       string(encoded),
	})
}

// streamEntryIndexes 找出所有 ai_stream 条目（流式条目的 eventType 就是这个）。
func streamEntryIndexes(entries []logEntry) []int {
	var out []int
	for i := range entries {
		if entries[i].eventType == "ai_stream" {
			out = append(out, i)
		}
	}
	return out
}

const (
	designMDPart1 = "## 方案\n\n**要点**：\n\n| 项 | 值 |\n| --- | --- |\n| A | 1 |\n"
	designMDTail  = "TAILMARKER4F17"
)

// 正例：流式中保持原文，tool_call_result 到达后定稿并一次性套上 markdown。
func TestDesignStream_FinalizedByToolResultRendersMarkdown(t *testing.T) {
	m := newTestModel()

	m = feedEvent(m, buildEvent("ai_chunk", "design", designMDPart1))

	streams := streamEntryIndexes(m.logEntries)
	if len(streams) != 1 {
		t.Fatalf("期望 ai_chunk 建 1 条流式条目，实际 %d 条", len(streams))
	}
	idx := streams[0]
	if !m.logEntries[idx].streaming {
		t.Fatalf("流式中条目必须 streaming=true")
	}

	// 流式中：仍是原文，不套样式（否则每 80ms 全量 glamour 重渲）。
	m.rebuildContentCache()
	streaming := m.contentParts[idx]
	if strings.Contains(streaming, "\x1b[") {
		t.Fatalf("流式中不应走 glamour，却拿到 ANSI：%q", streaming)
	}
	if !strings.Contains(streaming, "## 方案") {
		t.Fatalf("流式中的原文应保留 markdown 标记：%q", streaming)
	}

	// 工具结果到达（callID 与 toolCallEntries 配不上，走"未匹配"那条分派路径）。
	m = feedEvent(m, toolResultEvent(t, "CodingAgent", "design", "call-design-1", designMDPart1))

	le := m.logEntries[idx]
	if le.streaming {
		t.Errorf("收尾后 streaming 必须为 false（缺陷：恒为 true → 永远拿不到 markdown）")
	}
	if !le.finalized {
		t.Errorf("收尾后 finalized 应为 true")
	}
	if len(m.aiStreamActiveEntries) != 0 {
		t.Errorf("活动槽位必须清空，实际残留 %v", m.aiStreamActiveEntries)
	}
	if len(m.aiChunkBuffers) != 0 {
		t.Errorf("节流缓冲必须清空，实际残留 %d 个槽", len(m.aiChunkBuffers))
	}

	m.rebuildContentCache()
	rendered := m.contentParts[idx]
	if !strings.Contains(rendered, "\x1b[") {
		t.Fatalf("定稿后应经 glamour 渲染（含 ANSI），实际：%q", rendered)
	}
	if strings.Contains(rendered, "## 方案") {
		t.Fatalf("定稿后不应再看到原始 markdown 标题标记：%q", rendered)
	}
	if !strings.Contains(rendered, "方案") || !strings.Contains(rendered, "要点") {
		t.Fatalf("渲染后正文内容丢失：%q", rendered)
	}
}

// 按 callID 配对成功的那条分派路径（实时匹配到已有条目）同样要收尾。
func TestDesignStream_FinalizesOnCallIDMatchedPath(t *testing.T) {
	m := newTestModel()

	m = feedEvent(m, buildEvent("tool_call_start", "CodingAgent", map[string]interface{}{
		"tool_name":    "design",
		"tool_call_id": "call-design-9",
		"arguments":    `{"context":"c","goal":"g"}`,
	}))
	m = feedEvent(m, buildEvent("ai_chunk", "design", designMDPart1))

	streams := streamEntryIndexes(m.logEntries)
	if len(streams) != 1 {
		t.Fatalf("期望 1 条流式条目，实际 %d", len(streams))
	}
	idx := streams[0]

	m = feedEvent(m, toolResultEvent(t, "CodingAgent", "design", "call-design-9", designMDPart1))

	if m.logEntries[idx].streaming {
		t.Fatalf("callID 配对那条路径也必须收尾 design 的流式条目（streaming 仍为 true）")
	}
	m.rebuildContentCache()
	if !strings.Contains(m.contentParts[idx], "\x1b[") {
		t.Fatalf("该条目定稿后应渲染 markdown：%q", m.contentParts[idx])
	}
}

// 累积回归：第二次 design 调用绝不能把内容追加进第一次那条（跨调用串味）。
func TestDesignStream_TwoCallsDoNotAccumulate(t *testing.T) {
	m := newTestModel()

	first := "## 第一轮\n\n甲段内容"
	second := "## 第二轮\n\n乙段内容"

	m = feedEvent(m, buildEvent("ai_chunk", "design", first))
	m = feedEvent(m, toolResultEvent(t, "CodingAgent", "design", "call-1", first))
	m = feedEvent(m, buildEvent("ai_chunk", "design", second))
	m = feedEvent(m, toolResultEvent(t, "CodingAgent", "design", "call-2", second))

	streams := streamEntryIndexes(m.logEntries)
	if len(streams) != 2 {
		t.Fatalf("两次 design 调用应各建一条流式条目，实际 %d 条（串味就是只剩 1 条）", len(streams))
	}
	for _, i := range streams {
		if m.logEntries[i].streaming {
			t.Errorf("条目 %d 未被收尾，streaming 仍为 true", i)
		}
	}

	m.rebuildContentCache()
	newest := streams[len(streams)-1]
	oldest := streams[0]
	body := m.contentParts[newest]
	if strings.Contains(body, "甲段内容") {
		t.Fatalf("第二轮条目混进了第一轮的内容：%q", body)
	}
	if !strings.Contains(body, "乙段内容") {
		t.Fatalf("第二轮条目丢了自身内容：%q", body)
	}
	if strings.Contains(m.contentParts[oldest], "乙段内容") {
		t.Fatalf("第一轮条目被第二轮内容污染：%q", m.contentParts[oldest])
	}
	if !strings.Contains(body, "\x1b[") {
		t.Fatalf("第二轮条目也应渲染 markdown：%q", body)
	}
}

// 尾部不丢：最后一个 chunk 还留在节流缓冲（未达阈值）时就来 tool_call_result，
// 这截尾巴必须先落地再定稿。
func TestDesignStream_TrailingBufferedChunkNotLost(t *testing.T) {
	m := newTestModel()

	m = feedEvent(m, buildEvent("ai_chunk", "design", "# 计划\n\n开头"))
	streams := streamEntryIndexes(m.logEntries)
	if len(streams) != 1 {
		t.Fatalf("期望 1 条流式条目，实际 %d", len(streams))
	}
	idx := streams[0]

	// 第二个 chunk 进缓冲：1 条、<64 字节、<300ms → 不触发 flush。
	m = feedEvent(m, buildEvent("ai_chunk", "design", designMDTail))
	if strings.Contains(m.logEntries[idx].streamContent, designMDTail) {
		t.Fatalf("用例前提失效：该 chunk 本应留在节流缓冲里")
	}

	m = feedEvent(m, toolResultEvent(t, "CodingAgent", "design", "call-tail", "# 计划\n\n开头"+designMDTail))

	if !strings.Contains(m.logEntries[idx].streamContent, designMDTail) {
		t.Fatalf("收尾前必须把缓冲里的尾部文本落地，streamContent=%q", m.logEntries[idx].streamContent)
	}
	if len(m.aiChunkBuffers) != 0 {
		t.Fatalf("缓冲槽位应清空，实际 %d 个", len(m.aiChunkBuffers))
	}

	m.rebuildContentCache()
	if !strings.Contains(m.contentParts[idx], designMDTail) {
		t.Fatalf("渲染正文丢了尾部文本：%q", m.contentParts[idx])
	}
	if !strings.Contains(m.contentParts[idx], "\x1b[") {
		t.Fatalf("定稿后应渲染 markdown：%q", m.contentParts[idx])
	}
}

// 判据只有 rendersMarkdownBody 一处：同一段 markdown，白名单来源套样式、
// 非白名单来源保持原文。破坏这个判据（恒真或恒假）本用例必红。
func TestDesignMarkdownCriterionIsSingleSource(t *testing.T) {
	cases := map[string]bool{
		"design":       true,
		"deepthinking": true,
		"director":     false,
		"CodingAgent":  false,
		"thinking":     false,
		"micro_agent":  false,
		"read_file":    false,
		"designer":     false, // 前缀相似不算
		"Design":       false, // 大小写敏感
		"":             false,
	}
	for name, want := range cases {
		if got := rendersMarkdownBody(name); got != want {
			t.Errorf("rendersMarkdownBody(%q) = %v, 期望 %v", name, got, want)
		}
	}

	body := "# 标题\n\n**粗体**正文"

	on := newTestModel()
	on = feedEvent(on, buildEvent("ai_chunk", "design", body))
	on = feedEvent(on, toolResultEvent(t, "CodingAgent", "design", "call-on", body))
	on.rebuildContentCache()
	onIdx := streamEntryIndexes(on.logEntries)[0]
	if !strings.Contains(on.contentParts[onIdx], "\x1b[") {
		t.Fatalf("白名单来源定稿后应渲染 markdown：%q", on.contentParts[onIdx])
	}

	off := newTestModel()
	off = feedEvent(off, buildEvent("ai_chunk", "director", body))
	off = feedEvent(off, toolResultEvent(t, "CodingAgent", "director", "call-off", body))
	off.rebuildContentCache()
	offIdx := streamEntryIndexes(off.logEntries)[0]
	if off.logEntries[offIdx].streaming {
		t.Fatalf("收尾对任意同名来源都成立（不只是白名单两个），此处 streaming 仍为 true")
	}
	if strings.Contains(off.contentParts[offIdx], "\x1b[") {
		t.Fatalf("非白名单来源不应被强制 markdown：%q", off.contentParts[offIdx])
	}
	if !strings.Contains(off.contentParts[offIdx], "# 标题") {
		t.Fatalf("非白名单来源应保持原文：%q", off.contentParts[offIdx])
	}
}

// 父 Agent 的正常流不被误伤：ai_stream_end 之后的条目仍走原路径（不强制 markdown），
// 而别的工具的结果也不会替它收尾。
func TestStreamFinalize_LeavesAgentStreamAlone(t *testing.T) {
	m := newTestModel()

	m = feedEvent(m, buildEvent("ai_stream_start", "director", ""))
	m = feedEvent(m, buildEvent("ai_chunk", "director", "# 标题\n\n**粗体**正文"))
	m = feedEvent(m, buildEvent("ai_stream_end", "director", nil))

	streams := streamEntryIndexes(m.logEntries)
	if len(streams) != 1 {
		t.Fatalf("期望 1 条流式条目，实际 %d", len(streams))
	}
	idx := streams[0]
	if m.logEntries[idx].streaming {
		t.Fatalf("ai_stream_end 之后应已定稿")
	}

	// 无关工具的结果到达：不能替 director 收尾、也不能改写它的条目。
	before := len(m.logEntries)
	m = feedEvent(m, toolResultEvent(t, "director", "run_bash", "call-ls", "ok"))
	if got := len(streamEntryIndexes(m.logEntries)); got != 1 {
		t.Fatalf("不该凭空多出流式条目，现有 %d 条", got)
	}
	if m.logEntries[idx].eventType != "ai_stream" {
		t.Errorf("条目类型不该被改写：%q", m.logEntries[idx].eventType)
	}
	if len(m.logEntries) <= before {
		t.Errorf("tool_call_result 本身仍应入日志")
	}

	m.rebuildContentCache()
	if strings.Contains(m.contentParts[idx], "\x1b[") {
		t.Fatalf("director 的流式条目不该被强制 markdown：%q", m.contentParts[idx])
	}
	if !strings.Contains(m.contentParts[idx], "# 标题") {
		t.Fatalf("应保持原文：%q", m.contentParts[idx])
	}
}

// 没有流的工具（绝大多数）：收尾必须安全跳过，不新建条目、不 panic。
func TestStreamFinalize_NoMatchingStreamIsNoop(t *testing.T) {
	m := newTestModel()

	m = feedEvent(m, buildEvent("tool_call_start", "CodingAgent", map[string]interface{}{
		"tool_name":    "write_file",
		"tool_call_id": "call-w",
		"arguments":    `{"file_path":"a.txt"}`,
	}))
	before := len(m.logEntries)

	m = feedEvent(m, toolResultEvent(t, "CodingAgent", "write_file", "call-w", `{"diff":"x"}`))

	if got := len(streamEntryIndexes(m.logEntries)); got != 0 {
		t.Fatalf("该工具没产生流，不该造出 ai_stream 条目，实际 %d 条", got)
	}
	// callID 配对成功 → 就地更新 tool_call_start 那条，不追加新条目。
	if len(m.logEntries) != before {
		t.Fatalf("条目数不该变化（就地更新），实际 %d → %d", before, len(m.logEntries))
	}
	if m.logEntries[0].isToolRunning {
		t.Errorf("结果已到达，条目的 isToolRunning 应为 false")
	}

	// 仍在流式中的父 Agent 条目也不能被无关工具名收尾。
	m2 := newTestModel()
	m2 = feedEvent(m2, buildEvent("ai_stream_start", "Coding", ""))
	m2 = feedEvent(m2, buildEvent("ai_chunk", "Coding", "流式中"))
	m2 = feedEvent(m2, toolResultEvent(t, "Coding", "read_file", "call-r", "内容"))
	if !m2.logEntries[0].streaming {
		t.Fatalf("read_file 的结果不该替 Coding 的流收尾")
	}
	if len(m2.aiStreamActiveEntries) != 1 {
		t.Fatalf("Coding 的槽位应保留，实际 %v", m2.aiStreamActiveEntries)
	}
}

// 判定（因此**不改动** renderTimelineDetailBody）：时间线全屏详情走
// renderTimelineDetailBody → RenderToolLine，而 design 在注册表里不是 DisplayCompact
// （toolspec.IsCompactDisplay = Display==DisplayCompact，schemas.json 的 design 没有
// display 键 → 零值 DisplayFull），所以它的 markdown 正文本来就已经渲染出来。
// 这条用例钉住这个前提：哪天 design 被标成 DisplayCompact（详情只剩 header、正文丢失），
// 本用例立刻红。
func TestDesignTimelineDetailBodyAlreadyRendersMarkdown(t *testing.T) {
	m := newTestModel()

	m = feedEvent(m, buildEvent("tool_call_start", "CodingAgent", map[string]interface{}{
		"tool_name":    "design",
		"tool_call_id": "call-tl",
		"arguments":    `{"context":"c","goal":"g"}`,
	}))
	m = feedEvent(m, toolResultEvent(t, "CodingAgent", "design", "call-tl", designMDPart1))

	if skipBodyTool("design") {
		t.Fatalf("design 被判成 compact 展示：时间线详情只剩 header，需要补渲染分支")
	}

	out := renderTimelineDetailBody(m, &TimelineEntry{
		ID:        "call-tl",
		Kind:      TimelineKindTool,
		Name:      "design",
		Status:    ToolStatusSuccess,
		Timestamp: time.Now(),
	}, 100)

	if !strings.Contains(out, "\x1b[") {
		t.Fatalf("时间线详情应已对 design 正文套 markdown，实际：%q", out)
	}
	if strings.Contains(out, "## 方案") {
		t.Fatalf("时间线详情不该仍显示原始 markdown 标记：%q", out)
	}
}
