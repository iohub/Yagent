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

// TestThinkingMerge_BlankPartBetweenThinking 验证纯空白ai_stream part（如"\n\n"）破坏thinking合并：
// 这是用户报告的bug：LLM流式输出以换行开头时，streamContent为纯空白，
// assembleViewportContent只跳过part==""，不跳过纯空白part，导致：
// 1. 产生连续空行（\n\n）
// 2. 重置lastPartIsThinking，后续thinking不再合并
func TestThinkingMerge_BlankPartBetweenThinking(t *testing.T) {
	m := newTestModel()
	m.anim = NewAnim(10)
	m.llmCallActiveEntries = make(map[string]int)

	// 场景A：thinking → ai_stream_start → ai_chunk("\n\n") → thinking
	m = feedEvent(m, buildEvent("thinking", "director", "Now I need to find the function that contains line 911."))
	m = feedEvent(m, buildEvent("ai_stream_start", "director", ""))
	m = feedEvent(m, buildEvent("ai_chunk", "director", "\n\n"))
	m = feedEvent(m, buildEvent("thinking", "director", "Now I can see that the function starts at line 905."))

	output := m.contentCache.String()
	t.Logf("=== TestThinkingMerge_BlankPart 场景A 完整输出 ===\n%s\n=== 结束 ===", output)
	t.Logf("logEntries数量: %d", len(m.logEntries))
	for i, e := range m.logEntries {
		t.Logf("  entry[%d]: eventType=%q from=%q streamContent=%q isVerbose=%v", i, e.eventType, e.from, e.streamContent, e.isVerbose)
	}
	t.Logf("contentParts数量: %d", len(m.contentParts))
	for i, p := range m.contentParts {
		t.Logf("  part[%d]: %q", i, p)
	}

	// 断言：💭图标只出现1次（两个thinking应合并）
	emojiCount := strings.Count(output, "💭")
	t.Logf("💭出现次数: %d", emojiCount)
	if emojiCount != 1 {
		t.Logf("⚠ BUG复现：💭出现%d次（期望1次），thinking未合并", emojiCount)
		// 不fatal，继续检查其他断言
	}

	// 断言：应包含缩进续行
	if !strings.Contains(output, "      │ ") {
		t.Log("⚠ BUG复现：输出中无缩进续行，第二个thinking未渲染为续行")
	}

	// 断言：输出中不含连续空行（\n\n）
	if strings.Contains(output, "\n\n") {
		t.Logf("⚠ BUG复现：输出中包含连续空行（\\n\\n），part[%d]=%q 产生了空行",
			findBlankPartIndex(m.contentParts), m.contentParts[findBlankPartIndex(m.contentParts)])
	}

	t.Logf("✓ 场景A测试完成，💭次数=%d，含续行=%v，含空行=%v",
		emojiCount, strings.Contains(output, "      │ "), strings.Contains(output, "\n\n"))
}

// TestThinkingMerge_BlankPart_SingleSpace 验证单个空格作为streamContent也破坏合并。
func TestThinkingMerge_BlankPart_SingleSpace(t *testing.T) {
	m := newTestModel()
	m.anim = NewAnim(10)
	m.llmCallActiveEntries = make(map[string]int)

	// thinking → ai_stream_start → ai_chunk(" ") → thinking
	m = feedEvent(m, buildEvent("thinking", "director", "思考A"))
	m = feedEvent(m, buildEvent("ai_stream_start", "director", ""))
	m = feedEvent(m, buildEvent("ai_chunk", "director", " "))
	m = feedEvent(m, buildEvent("thinking", "director", "思考B"))

	output := m.contentCache.String()
	t.Logf("=== TestThinkingMerge_BlankPart_SingleSpace 完整输出 ===\n%s\n=== 结束 ===", output)
	t.Logf("contentParts数量: %d", len(m.contentParts))
	for i, p := range m.contentParts {
		t.Logf("  part[%d]: %q", i, p)
	}

	emojiCount := strings.Count(output, "💭")
	t.Logf("💭出现次数: %d", emojiCount)
	if emojiCount != 1 {
		t.Logf("⚠ BUG复现：💭出现%d次（期望1次）", emojiCount)
	}
	if strings.Contains(output, "      │ ") {
		t.Log("✓ 续行存在")
	} else {
		t.Log("⚠ BUG：无续行")
	}
}

// TestThinkingMerge_BlankPart_TwoSpaces 验证两个空格作为streamContent。
func TestThinkingMerge_BlankPart_TwoSpaces(t *testing.T) {
	m := newTestModel()
	m.anim = NewAnim(10)
	m.llmCallActiveEntries = make(map[string]int)

	m = feedEvent(m, buildEvent("thinking", "director", "思考X"))
	m = feedEvent(m, buildEvent("ai_stream_start", "director", ""))
	m = feedEvent(m, buildEvent("ai_chunk", "director", "  "))
	m = feedEvent(m, buildEvent("thinking", "director", "思考Y"))

	output := m.contentCache.String()
	t.Logf("=== TestThinkingMerge_BlankPart_TwoSpaces 完整输出 ===\n%s\n=== 结束 ===", output)
	t.Logf("contentParts数量: %d", len(m.contentParts))
	for i, p := range m.contentParts {
		t.Logf("  part[%d]: %q", i, p)
	}

	emojiCount := strings.Count(output, "💭")
	t.Logf("💭出现次数: %d", emojiCount)
	if emojiCount != 1 {
		t.Logf("⚠ BUG复现：💭出现%d次（期望1次）", emojiCount)
	}
}

// TestThinkingMerge_BlankPart_MultipleBlankChunks 验证多个空白chunk累积也破坏合并。
func TestThinkingMerge_BlankPart_MultipleBlankChunks(t *testing.T) {
	m := newTestModel()
	m.anim = NewAnim(10)
	m.llmCallActiveEntries = make(map[string]int)

	// 多个空白chunk累积
	m = feedEvent(m, buildEvent("thinking", "director", "思考1"))
	m = feedEvent(m, buildEvent("ai_stream_start", "director", ""))
	m = feedEvent(m, buildEvent("ai_chunk", "director", "\n"))
	m = feedEvent(m, buildEvent("ai_chunk", "director", " \n"))
	m = feedEvent(m, buildEvent("thinking", "director", "思考2"))

	output := m.contentCache.String()
	t.Logf("=== TestThinkingMerge_BlankPart_Multiple 完整输出 ===\n%s\n=== 结束 ===", output)
	t.Logf("logEntries数量: %d", len(m.logEntries))
	for i, e := range m.logEntries {
		t.Logf("  entry[%d]: eventType=%q streamContent=%q", i, e.eventType, e.streamContent)
	}
	t.Logf("contentParts数量: %d", len(m.contentParts))
	for i, p := range m.contentParts {
		t.Logf("  part[%d]: %q", i, p)
	}

	emojiCount := strings.Count(output, "💭")
	t.Logf("💭出现次数: %d", emojiCount)
	if emojiCount != 1 {
		t.Logf("⚠ BUG复现：💭出现%d次（期望1次）", emojiCount)
	}
	if strings.Contains(output, "\n\n") {
		t.Log("⚠ BUG复现：输出含连续空行")
	}
}

// TestThinkingMerge_NonBlankPartBetweenThinking 验证非空白流内容后紧跟thinking时，
// thinking不合并是合理的（中间有实际AI回复内容）。
func TestThinkingMerge_NonBlankPartBetweenThinking(t *testing.T) {
	m := newTestModel()
	m.anim = NewAnim(10)
	m.llmCallActiveEntries = make(map[string]int)

	// thinking → ai_stream_start → ai_chunk("实际内容") → ai_response("最终回复") → thinking
	m = feedEvent(m, buildEvent("thinking", "director", "思考1"))
	m = feedEvent(m, buildEvent("ai_stream_start", "director", ""))
	m = feedEvent(m, buildEvent("ai_chunk", "director", "Hello"))
	m = feedEvent(m, buildEvent("ai_response", "director", "Hello world"))
	m = feedEvent(m, buildEvent("thinking", "director", "思考2"))

	output := m.contentCache.String()
	t.Logf("=== TestThinkingMerge_NonBlank 完整输出 ===\n%s\n=== 结束 ===", output)
	t.Logf("logEntries数量: %d", len(m.logEntries))
	for i, e := range m.logEntries {
		t.Logf("  entry[%d]: eventType=%q from=%q content=%q", i, e.eventType, e.from, e.content)
	}
	t.Logf("contentParts数量: %d", len(m.contentParts))
	for i, p := range m.contentParts {
		t.Logf("  part[%d]: %q", i, p)
	}

	// 非空白流内容后紧跟thinking：💭出现2次是合理的（中间有ai_response隔开）
	emojiCount := strings.Count(output, "💭")
	t.Logf("💭出现次数: %d", emojiCount)
	if emojiCount == 2 {
		t.Log("✓ 非空白流场景：💭出现2次符合预期（中间有ai_response，不合并是合理的）")
	} else {
		t.Logf("⚠ 意外：💭出现%d次", emojiCount)
	}
}

// TestThinkingMerge_DirectBlankPart 直接用direct构造验证assembleViewportContent对空白part的处理。
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

	emojiCount := strings.Count(output, "💭")
	t.Logf("💭出现次数: %d", emojiCount)
	if emojiCount != 1 {
		t.Logf("⚠ BUG复现：💭出现%d次（期望1次），空白part破坏了合并", emojiCount)
	}

	if strings.Contains(output, "\n\n") {
		t.Log("⚠ BUG复现：输出含连续空行（\\n\\n）")
	}

	if !strings.Contains(output, "      │ ") {
		t.Log("⚠ BUG复现：无缩进续行")
	}
}

// findBlankPartIndex 返回contentParts中第一个非空但trimmed为空字符串的索引。
func findBlankPartIndex(parts []string) int {
	for i, p := range parts {
		if p != "" && strings.TrimSpace(p) == "" {
			return i
		}
	}
	return -1
}
