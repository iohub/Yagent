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

// TestThinkingMerge_DirectModelAssemble 直接构造model验证assembleViewportContent的合并逻辑。
func TestThinkingMerge_DirectModelAssemble(t *testing.T) {
	m := newTestModel()
	m.logEntries = []logEntry{
		{eventType: "thinking", from: "director", content: "思考1", prefix: "  │ ", isVerbose: false},
		{eventType: "llm_call_start", from: "director", content: "[model]", isVerbose: true},
		{eventType: "thinking", from: "director", content: "思考2", prefix: "  │ ", isVerbose: false},
		{eventType: "thinking", from: "director", content: "思考3", prefix: "  │ ", isVerbose: false},
	}
	m.contentParts = make([]string, len(m.logEntries))
	for i := range m.logEntries {
		m.contentParts[i] = m.renderSingleEntry(&m.logEntries[i], m.viewport.Width())
	}
	m.assembleViewportContent()

	output := m.contentCache.String()
	t.Logf("=== TestThinkingMerge_DirectModel 完整输出 ===\n%s\n=== 结束 ===", output)
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
}
