package tui

import (
	"strings"
	"testing"
)

// thinkingTestBody 走 View() 的那一步：事件路径只标脏，正文缓存由 View 落地，
// 不先 rebuild 就会读到合并之前那一帧的旧 contentParts（而不是"渲染错了"）。
func thinkingTestBody(m *model) string {
	m.rebuildContentCache()
	return stripANSI(m.bodyText(0, m.bodyTotalLines()))
}

// ── 折叠态的 thinking 契约（docs/thinking-collapse-design.md §2）──────────────
//
// 旧契约是"一段思考一行、同段后续行走缩进续行"，续行没有图标 —— 于是 💭 次数
// 与条目数不相等，且合并进来的后续段落根本渲染不出来。新契约：
// ① 每个 thinking 条目恰好一行，行首恒为 "💭 ×N │ "（N = 段数）；
// ② 空白 part（ai_stream 占位）不产生任何行；
// ③ 展开态（thinkDetailOpen）才逐段编号折行。

// TestThinkingMerge_ContinuousThinkingWithVerboseInterleaved：
// 连续 thinking 被 verbose 条目（llm_call_start/end）打断时各成一条目，
// 折叠态每条一行、每行一个 💭，段与段之间不产生空行。
func TestThinkingMerge_ContinuousThinkingWithVerboseInterleaved(t *testing.T) {
	m := newTestModel()
	m.anim = NewAnim(10)
	m.llmCallActiveEntries = make(map[string]int)

	m = feedEvent(m, buildEvent("thinking", "director", "第一个思考内容"))
	m = feedEvent(m, buildEvent("llm_call_start", "director", map[string]interface{}{"model": "test-model"}))
	m = feedEvent(m, buildEvent("llm_call_end", "director", map[string]interface{}{"model": "test-model"}))
	m = feedEvent(m, buildEvent("thinking", "director", "第二个思考内容"))
	m = feedEvent(m, buildEvent("llm_call_start", "director", map[string]interface{}{"model": "test-model"}))
	m = feedEvent(m, buildEvent("llm_call_end", "director", map[string]interface{}{"model": "test-model"}))
	m = feedEvent(m, buildEvent("thinking", "director", "第三个思考内容"))

	nThinking := 0
	for _, e := range m.logEntries {
		if e.eventType == "thinking" {
			nThinking++
		}
	}
	if nThinking != 3 {
		t.Fatalf("thinking 条目数 = %d, want 3（verbose 条目会断开合并）", nThinking)
	}

	output := m.bodyText(0, m.bodyTotalLines())
	plain := stripANSI(output)
	if got := strings.Count(plain, "💭"); got != nThinking {
		t.Errorf("💭 出现 %d 次, want %d（每条目一行、每行一个图标）\n%s", got, nThinking, plain)
	}
	for _, want := range []string{"第一个思考内容", "第二个思考内容", "第三个思考内容"} {
		if !strings.Contains(plain, want) {
			t.Errorf("折叠行缺少 %q\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "\n\n") {
		t.Errorf("折叠态不应出现空行\n%s", plain)
	}
}

// TestThinkingMerge_AdjacentThinkingMergesIntoOneEntryCount：
// 相邻同 Agent 的 thinking 合并成一条目，折叠态仍是一行，但 ×N 必须数出段数 ——
// 这是"信息没丢、只是收起来"的唯一可见凭据。
func TestThinkingMerge_AdjacentThinkingMergesIntoOneEntryCount(t *testing.T) {
	m := newTestModel()
	m = feedEvent(m, buildEvent("thinking", "director", "思考一"))
	m = feedEvent(m, buildEvent("thinking", "director", "思考二"))
	m = feedEvent(m, buildEvent("thinking", "director", "思考三"))

	if n := len(m.logEntries); n != 1 {
		t.Fatalf("logEntries = %d, want 1（相邻 thinking 必须合并）", n)
	}
	plain := thinkingTestBody(m)
	// 行宽 = 视口内宽，preview 放得下时整段首行原样出现在同一行上。
	if !strings.Contains(plain, "💭 ×3 │ 思考一") {
		t.Errorf("折叠行应数出 3 段并只露首段\n%q", plain)
	}
	if strings.Contains(plain, "思考二") {
		t.Errorf("折叠态不该把后续段落摊进同一行\n%s", plain)
	}
}

// TestThinkingMerge_DifferentFromNotMerged 验证不同 from 的 thinking 不应合并（应显示 badge）。
func TestThinkingMerge_DifferentFromNotMerged(t *testing.T) {
	m := newTestModel()
	m.anim = NewAnim(10)
	m.llmCallActiveEntries = make(map[string]int)

	m = feedEvent(m, buildEvent("thinking", "director", "director思考"))
	m = feedEvent(m, buildEvent("thinking", "repo", "repo思考"))
	m = feedEvent(m, buildEvent("thinking", "coding", "coding思考"))

	output := thinkingTestBody(m)
	if n := strings.Count(output, "💭"); n != 3 {
		t.Fatalf("期望💭出现3次（不合并），实际出现%d次\n%s", n, output)
	}
	if !strings.Contains(output, "◈") {
		t.Fatal("期望输出中包含badge（\"◈\"符号），但未找到")
	}
}

// TestThinkingMerge_EmptyPartBetweenThinking 验证空 part（ai_stream 占位条目）
// 不产生行：三条 thinking 各占一行，中间没有空行。
func TestThinkingMerge_EmptyPartBetweenThinking(t *testing.T) {
	m := newTestModel()
	m.logEntries = []logEntry{
		{eventType: "thinking", from: "director", content: "思考1"},
		{eventType: "ai_stream", from: "director", streamContent: "", streaming: false},
		{eventType: "thinking", from: "director", content: "思考2"},
		{eventType: "ai_stream", from: "director", streamContent: "", streaming: false},
		{eventType: "thinking", from: "director", content: "思考3"},
	}
	m.contentParts = make([]string, len(m.logEntries))
	for i := range m.logEntries {
		m.contentParts[i] = m.renderSingleEntry(&m.logEntries[i], m.viewport.Width())
	}
	m.bodyInvalidate(0)

	plain := thinkingTestBody(m)
	if n := strings.Count(plain, "💭"); n != 3 {
		t.Errorf("💭出现次数 = %d, want 3（每个 thinking 条目一行）\n%s", n, plain)
	}
	if strings.Contains(plain, "\n\n") {
		t.Errorf("期望输出中无空行（\\n\\n），空白 part 不该产生行\n%s", plain)
	}
}

// TestThinkingMerge_BlankPartBetweenThinking 验证 thinking 之间穿插纯空白 ai_stream
// 占位条目时正文不出现空行：事件驱动路径（chunk 累积成 "\n\n"、" "）与
// TestThinkingMerge_EmptyPartBetweenThinking 的直接构造路径互补。
func TestThinkingMerge_BlankPartBetweenThinking(t *testing.T) {
	tests := []struct {
		name   string
		chunks []string
	}{
		{name: "blank \\n\\n chunk", chunks: []string{"\n\n"}},
		{name: "single space chunk", chunks: []string{" "}},
		{name: "two spaces chunk", chunks: []string{"  "}},
		{name: "accumulated blank chunks", chunks: []string{"\n", " \n"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel()
			m = feedEvent(m, buildEvent("thinking", "director", "思考1"))
			m = feedEvent(m, buildEvent("ai_stream_start", "director", ""))
			for _, c := range tt.chunks {
				m = feedEvent(m, buildEvent("ai_chunk", "director", c))
			}
			m = feedEvent(m, buildEvent("thinking", "director", "思考2"))

			plain := thinkingTestBody(m)
			if n := strings.Count(plain, "💭"); n != 2 {
				t.Errorf("💭出现次数 = %d, want 2（空白 part 不产生行，两个 thinking 各一行）\n%s", n, plain)
			}
			if strings.Contains(plain, "\n\n") {
				t.Errorf("期望输出中无连续空行（\\n\\n），空白 part 不该产生行\n%s", plain)
			}
		})
	}
}

// TestThinkingMerge_DirectBlankPart 直接用构造路径验证排版块对空白 part 的处理。
func TestThinkingMerge_DirectBlankPart(t *testing.T) {
	m := newTestModel()
	m.logEntries = []logEntry{
		{eventType: "thinking", from: "director", content: "思考1"},
		{eventType: "ai_stream", from: "director", streamContent: "\n\n", streaming: false},
		{eventType: "thinking", from: "director", content: "思考2"},
	}
	m.contentParts = make([]string, len(m.logEntries))
	for i := range m.logEntries {
		m.contentParts[i] = m.renderSingleEntry(&m.logEntries[i], m.viewport.Width())
	}
	m.bodyInvalidate(0)

	plain := thinkingTestBody(m)
	if n := strings.Count(plain, "💭"); n != 2 {
		t.Fatalf("期望💭出现2次，实际出现%d次\n%s", n, plain)
	}
	if strings.Contains(plain, "\n\n") {
		t.Fatalf("期望输出中无空行（\\n\\n），但找到空行\n%s", plain)
	}
}

// TestThinkingMerge_DirectSingleSpaceBlank 验证单个空格作为 ai_stream part 也被跳过。
func TestThinkingMerge_DirectSingleSpaceBlank(t *testing.T) {
	m := newTestModel()
	m.logEntries = []logEntry{
		{eventType: "thinking", from: "director", content: "思考A"},
		{eventType: "ai_stream", from: "director", streamContent: " ", streaming: false},
		{eventType: "thinking", from: "director", content: "思考B"},
	}
	m.contentParts = make([]string, len(m.logEntries))
	for i := range m.logEntries {
		m.contentParts[i] = m.renderSingleEntry(&m.logEntries[i], m.viewport.Width())
	}
	m.bodyInvalidate(0)

	plain := thinkingTestBody(m)
	if n := strings.Count(plain, "💭"); n != 2 {
		t.Fatalf("期望💭出现2次，实际出现%d次\n%s", n, plain)
	}
	if strings.Contains(plain, "\n\n") {
		t.Fatalf("期望输出中无空行（\\n\\n）\n%s", plain)
	}
}
