package tui

import (
	"strings"
	"testing"
)

// TestThinkingMerge_ContinuousThinkingWithVerboseInterleaved 验证连续thinking（中间隔verbose条目）应合并：
// 💭图标只出现1次，后续thinking渲染为缩进续行。
func TestThinkingMerge_ContinuousThinkingWithVerboseInterleaved(t *testing.T) {
	m := newTestModel()
	m.anim = NewAnim(10)
	m.llmCallActiveEntries = make(map[string]int)

	// 3个thinking，中间隔llm_call_start/llm_call_end（verbose事件）
	m = feedEvent(m, buildEvent("thinking", "director", "第一个思考内容"))
	m = feedEvent(m, buildEvent("llm_call_start", "director", map[string]interface{}{"model": "test-model"}))
	m = feedEvent(m, buildEvent("llm_call_end", "director", map[string]interface{}{"model": "test-model"}))
	m = feedEvent(m, buildEvent("thinking", "director", "第二个思考内容"))
	m = feedEvent(m, buildEvent("llm_call_start", "director", map[string]interface{}{"model": "test-model"}))
	m = feedEvent(m, buildEvent("llm_call_end", "director", map[string]interface{}{"model": "test-model"}))
	m = feedEvent(m, buildEvent("thinking", "director", "第三个思考内容"))

	output := m.contentCache.String()
	t.Logf("=== TestThinkingMerge 完整输出 ===\n%s\n=== 结束 ===", output)
	t.Logf("logEntries数量: %d", len(m.logEntries))
	for i, e := range m.logEntries {
		t.Logf("  entry[%d]: eventType=%q from=%q isVerbose=%v content=%q", i, e.eventType, e.from, e.isVerbose, e.content)
	}
	t.Logf("contentParts数量: %d", len(m.contentParts))
	for i, p := range m.contentParts {
		t.Logf("  part[%d]: %q", i, p)
	}

	// 断言：💭图标只出现1次
	emojiCount := strings.Count(output, "💭")
	t.Logf("💭出现次数: %d", emojiCount)
	if emojiCount != 1 {
		t.Fatalf("期望💭出现1次，实际出现%d次", emojiCount)
	}

	// 断言：包含renderThinkingContinuation生成的缩进续行（以"      │ "开头）
	if !strings.Contains(output, "      │ ") {
		t.Fatal("期望输出中包含缩进续行（以\"      │ \"开头），但未找到")
	}

	// 断言：应包含2个续行（第2、3个thinking）
	continuationCount := strings.Count(output, "      │ ")
	if continuationCount < 2 {
		t.Fatalf("期望至少2个缩进续行，实际找到%d个", continuationCount)
	}
}

// TestThinkingMerge_DifferentFromNotMerged 验证不同from的thinking不应合并（应显示badge）。
func TestThinkingMerge_DifferentFromNotMerged(t *testing.T) {
	m := newTestModel()
	m.anim = NewAnim(10)
	m.llmCallActiveEntries = make(map[string]int)

	m = feedEvent(m, buildEvent("thinking", "director", "director思考"))
	m = feedEvent(m, buildEvent("thinking", "repo", "repo思考"))
	m = feedEvent(m, buildEvent("thinking", "coding", "coding思考"))

	output := m.contentCache.String()
	t.Logf("=== TestThinkingMerge_DifferentFrom 完整输出 ===\n%s\n=== 结束 ===", output)
	t.Logf("logEntries数量: %d", len(m.logEntries))
	for i, e := range m.logEntries {
		t.Logf("  entry[%d]: eventType=%q from=%q isVerbose=%v content=%q", i, e.eventType, e.from, e.isVerbose, e.content)
	}

	// 断言：💭图标出现3次（不合并）
	emojiCount := strings.Count(output, "💭")
	t.Logf("💭出现次数: %d", emojiCount)
	if emojiCount != 3 {
		t.Fatalf("期望💭出现3次（不合并），实际出现%d次", emojiCount)
	}

	// 断言：包含badge（"◈"符号）
	if !strings.Contains(output, "◈") {
		t.Fatal("期望输出中包含badge（\"◈\"符号），但未找到")
	}
}

// TestThinkingMerge_EmptyPartBetweenThinking 验证空part（ai_stream占位条目）不破坏thinking合并：
// thinking → ai_stream_start（空占位）→ thinking → ai_stream_start（空占位）→ thinking
// 💭只出现1次，无空行（\n\n）。
func TestThinkingMerge_EmptyPartBetweenThinking(t *testing.T) {
	m := newTestModel()
	// 直接构造model：thinking → ai_stream占位(空) → thinking → ai_stream占位(空) → thinking
	m.logEntries = []logEntry{
		{eventType: "thinking", from: "director", content: "思考1", prefix: "  │ ", isVerbose: false},
		{eventType: "ai_stream", from: "director", streamContent: "", streaming: false, isVerbose: false},
		{eventType: "thinking", from: "director", content: "思考2", prefix: "  │ ", isVerbose: false},
		{eventType: "ai_stream", from: "director", streamContent: "", streaming: false, isVerbose: false},
		{eventType: "thinking", from: "director", content: "思考3", prefix: "  │ ", isVerbose: false},
	}
	m.contentParts = make([]string, len(m.logEntries))
	for i := range m.logEntries {
		m.contentParts[i] = m.renderSingleEntry(&m.logEntries[i], m.viewport.Width())
	}
	m.assembleViewportContent()

	output := m.contentCache.String()
	t.Logf("=== TestThinkingMerge_EmptyPart 完整输出 ===\n%s\n=== 结束 ===", output)
	t.Logf("contentParts数量: %d", len(m.contentParts))
	for i, p := range m.contentParts {
		t.Logf("  part[%d]: %q", i, p)
	}

	// 断言：💭只出现1次
	emojiCount := strings.Count(output, "💭")
	t.Logf("💭出现次数: %d", emojiCount)
	if emojiCount != 1 {
		t.Fatalf("期望💭出现1次，实际出现%d次", emojiCount)
	}

	// 断言：包含2个缩进续行
	continuationCount := strings.Count(output, "      │ ")
	t.Logf("缩进续行数量: %d", continuationCount)
	if continuationCount < 2 {
		t.Fatalf("期望至少2个缩进续行，实际找到%d个", continuationCount)
	}

	// 断言：输出中不含连续空行（\n\n），即ai_stream空占位未产生空行
	if strings.Contains(output, "\n\n") {
		t.Fatalf("期望输出中无空行（\\n\\n），但找到空行")
	}
	t.Log("✓ 无空行，thinking条目成功跨越空part合并")
}

// TestThinkingMerge_BlankPartBetweenThinking 验证 thinking 之间穿插纯空白 ai_stream 占位条目时
// 仍能合并："\n\n"、" "、"  "、多个空白 chunk 累积（事件驱动路径，
// 与 DirectBlankPart/DirectSingleSpaceBlank 的直接构造路径互补）都被
// assembleViewportContent 跳过 —— 💭 只出现 1 次、产生缩进续行、无连续空行。
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

			output := m.contentCache.String()
			if emojiCount := strings.Count(output, "💭"); emojiCount != 1 {
				t.Errorf("💭出现次数 = %d, want 1（空白 part 应被跳过、thinking 合并）\n%s", emojiCount, output)
			}
			if !strings.Contains(output, "      │ ") {
				t.Errorf("期望输出包含缩进续行（\"      │ \"），实际没有\n%s", output)
			}
			if strings.Contains(output, "\n\n") {
				t.Errorf("期望输出中无连续空行（\\n\\n），空白 part 不应产生空行\n%s", output)
			}
		})
	}
}

// TestThinkingMerge_DirectBlankPart 直接用direct构造验证assembleViewportContent对空白part的处理。
// 修复后：纯空白 part（如"\n\n"、" "）应被跳过，thinking 条目正常合并。
func TestThinkingMerge_DirectBlankPart(t *testing.T) {
	m := newTestModel()
	// 直接构造：thinking → ai_stream(空白"\n\n") → thinking
	m.logEntries = []logEntry{
		{eventType: "thinking", from: "director", content: "思考1", prefix: "  │ ", isVerbose: false},
		{eventType: "ai_stream", from: "director", streamContent: "\n\n", streaming: false, isVerbose: false},
		{eventType: "thinking", from: "director", content: "思考2", prefix: "  │ ", isVerbose: false},
	}
	m.contentParts = make([]string, len(m.logEntries))
	for i := range m.logEntries {
		m.contentParts[i] = m.renderSingleEntry(&m.logEntries[i], m.viewport.Width())
	}

	t.Logf("=== TestThinkingMerge_DirectBlankPart contentParts ===")
	for i, p := range m.contentParts {
		t.Logf("  part[%d]: %q (len=%d)", i, p, len(p))
	}

	m.assembleViewportContent()
	output := m.contentCache.String()
	t.Logf("=== 完整输出 ===\n%s\n=== 结束 ===", output)

	// 断言：💭只出现1次
	emojiCount := strings.Count(output, "💭")
	t.Logf("💭出现次数: %d", emojiCount)
	if emojiCount != 1 {
		t.Fatalf("期望💭出现1次，实际出现%d次", emojiCount)
	}

	// 断言：输出中不含连续空行（\n\n）
	if strings.Contains(output, "\n\n") {
		t.Fatalf("期望输出中无空行（\\n\\n），但找到空行")
	}

	// 断言：包含缩进续行
	if !strings.Contains(output, "      │ ") {
		t.Fatalf("期望输出中包含缩进续行（以\"      │ \"开头），但未找到")
	}
}

// TestThinkingMerge_DirectSingleSpaceBlank 验证单个空格作为ai_stream part也被跳过。
func TestThinkingMerge_DirectSingleSpaceBlank(t *testing.T) {
	m := newTestModel()
	m.logEntries = []logEntry{
		{eventType: "thinking", from: "director", content: "思考A", prefix: "  │ ", isVerbose: false},
		{eventType: "ai_stream", from: "director", streamContent: " ", streaming: false, isVerbose: false},
		{eventType: "thinking", from: "director", content: "思考B", prefix: "  │ ", isVerbose: false},
	}
	m.contentParts = make([]string, len(m.logEntries))
	for i := range m.logEntries {
		m.contentParts[i] = m.renderSingleEntry(&m.logEntries[i], m.viewport.Width())
	}
	m.assembleViewportContent()

	output := m.contentCache.String()
	t.Logf("=== TestThinkingMerge_DirectSingleSpaceBlank 完整输出 ===\n%s\n=== 结束 ===", output)

	emojiCount := strings.Count(output, "💭")
	if emojiCount != 1 {
		t.Fatalf("期望💭出现1次，实际出现%d次", emojiCount)
	}
	if strings.Contains(output, "\n\n") {
		t.Fatalf("期望输出中无空行（\\n\\n），但找到空行")
	}
	if !strings.Contains(output, "      │ ") {
		t.Fatalf("期望输出中包含缩进续行，但未找到")
	}
}
