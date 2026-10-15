package compression

import (
	// 截断优先级现在由工具注册表派生（Spec.Trunc）。注册表靠各包 init() 自注册，
	// 而本包在生产链路里总是和 tools/agents 一起被链接；单元测试二进制不链接它们就会
	// 查不到声明、把 deepthinking 当未注册工具返回常规优先级，所以这里显式拉进来。
	"strings"
	"testing"
	"time"
	_ "yagent/internal/tools"

	"yagent/internal/llm"
	"yagent/internal/tokenutil"
	"yagent/internal/toolspec"
)

// 截断用例的负载尺寸。断言关心的是「总量相对阈值落在哪一档」以及截断/保护的记账，
// 与绝对体积无关。此前这里用 50000 字符负载 + 10000 字符 filler，而
// TruncateToolResultsToBudget 每截一条都要重新编码整份消息（约 160KB），
// 本包 28s 的墙钟时间几乎全花在这上面。下面等比缩到约 1/25（'x' 在 cl100k 下
// 8 字符/token，'s'/'u' 2 字符/token），阈值改为由实测 token 值推导而非魔法数字，
// 每个用例落在 production 分支上的位置和缩小前完全一致。
const (
	truncPayloadChars = 2000 // → 250 tokens
	truncFillerChars  = 400  // → 200 tokens
	truncKeepTokens   = 8    // 截断后每条 tool 结果保留的 token 预算

	// 截断会在保留前缀后追加 "\n\n[truncated: 内容已截断，原始 N tokens, 保留 M tokens]"，
	// 实测约 28 token；把这条消息的体积算进阈值时需要带上它。
	truncMarkerTokens = 28
)

// truncTokens 返回一条 tool 结果被截断后实际占用的 token（保留前缀 + 截断标记）。
func truncTokens(content string, keepTokens int) int {
	return tokenutil.EstimateTokens(TruncateToTokenBudget(content, keepTokens)) + truncMarkerTokens
}

// TestEstimateMessagesTokens 验证 token 估算函数对各类消息的处理
func TestEstimateMessagesTokens(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system prompt"},
		{Role: llm.RoleUser, Content: "user question"},
		{
			Role:      llm.RoleAssistant,
			Content:   "assistant text",
			ToolCalls: []llm.ToolCall{{Function: llm.FunctionCall{Name: "read_file", Arguments: `{\"path\":\"foo\"}`}}},
		},
		{Role: llm.RoleTool, ToolName: "read_file", Content: "file content here"},
	}
	total := EstimateMessagesTokens(messages)
	if total <= 0 {
		t.Fatalf("expected positive token count, got %d", total)
	}
	// 验证 assistant 的 tool_calls arguments 被计入
	t.Logf("total tokens: %d", total)

	// Reasoning 会被回灌给 provider，必须计入估算，否则压缩触发时机偏晚。
	withReasoning := []llm.Message{{
		Role:      llm.RoleAssistant,
		Content:   "assistant text",
		Reasoning: strings.Repeat("detailed reasoning sentence. ", 40),
	}}
	bare := []llm.Message{{Role: llm.RoleAssistant, Content: "assistant text"}}
	if got, want := EstimateMessagesTokens(withReasoning), EstimateMessagesTokens(bare); got <= want {
		t.Errorf("EstimateMessagesTokens with Reasoning = %d, want > %d (content only)", got, want)
	}
}

// TestTruncateToolResultsToBudget_NotOverThreshold 未超阈值时原样返回
func TestTruncateToolResultsToBudget_NotOverThreshold(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleAssistant, Content: "call read_file"},
		{Role: llm.RoleTool, ToolName: "read_file", Content: "small result"},
	}
	// 阈值设很大，不会触发
	result, stats := TruncateToolResultsToBudget(messages, 999999, 200)
	if len(result) != len(messages) {
		t.Fatalf("expected %d messages, got %d", len(messages), len(result))
	}
	if stats != nil {
		t.Error("expected stats to be nil when not over threshold")
	}
	for i, msg := range result {
		if msg.Content != messages[i].Content {
			t.Errorf("message %d content changed unexpectedly", i)
		}
		if msg.TruncationMarker != nil {
			t.Errorf("message %d should not have TruncationMarker", i)
		}
	}
}

// TestTruncateToolResultsToBudget_Priority 优先级测试：预算只够截一条时 read_file 先被截
func TestTruncateToolResultsToBudget_Priority(t *testing.T) {
	largeContent := strings.Repeat("x", truncPayloadChars)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: strings.Repeat("s", truncFillerChars)},
		{Role: llm.RoleUser, Content: strings.Repeat("u", truncFillerChars)},
		{Role: llm.RoleAssistant, Content: "assistant response"},
		// 优先级 0 工具
		{Role: llm.RoleTool, ToolName: "read_file", Content: largeContent},
		// 优先级 1 工具
		{Role: llm.RoleTool, ToolName: "list_dir", Content: largeContent},
	}
	payloadTokens := tokenutil.EstimateTokens(largeContent)
	// 基础 token 数（不含两条大结果）
	baseTokens := EstimateMessagesTokens([]llm.Message{messages[0], messages[1], messages[2]})
	// 阈值必须严格落在「两条都在」和「只截掉 prio0 那一条」之间，
	// 才能证明截的是优先级而不是先到先截。
	overBudget := baseTokens + 2*payloadTokens
	afterOneCut := baseTokens + payloadTokens + truncTokens(largeContent, truncKeepTokens)
	if afterOneCut >= overBudget {
		t.Fatalf("负载太小导致阈值区间为空: afterOneCut=%d >= overBudget=%d", afterOneCut, overBudget)
	}
	threshold := (overBudget + afterOneCut) / 2

	result, _ := TruncateToolResultsToBudget(messages, threshold, truncKeepTokens)

	// read_file (prio 0) 应被截断
	readFileMsg := result[3]
	if readFileMsg.TruncationMarker == nil {
		t.Error("expected read_file message to be truncated")
	}
	if !strings.Contains(readFileMsg.Content, "[truncated:") {
		t.Error("expected read_file content to contain truncation marker")
	}

	// list_dir (prio 1) 不应被截断（预算只够截一条）
	listDirMsg := result[4]
	if listDirMsg.TruncationMarker != nil {
		t.Error("expected list_dir message NOT to be truncated (budget only allows one)")
	}
}

// TestTruncateToolResultsToBudget_DeepThinkingProtected deepthinking 永不截断
func TestTruncateToolResultsToBudget_DeepThinkingProtected(t *testing.T) {
	largeContent := strings.Repeat("x", truncPayloadChars)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: strings.Repeat("s", truncFillerChars)},
		{Role: llm.RoleUser, Content: strings.Repeat("u", truncFillerChars)},
		{Role: llm.RoleAssistant, Content: "assistant response"},
		{Role: llm.RoleTool, ToolName: "deepthinking", Content: largeContent},
		{Role: llm.RoleTool, ToolName: "list_dir", Content: largeContent},
	}
	payloadTokens := tokenutil.EstimateTokens(largeContent)
	baseTokens := EstimateMessagesTokens([]llm.Message{messages[0], messages[1], messages[2]})
	// 阈值取「基础 + 一条完整负载」，比截完 list_dir 后仍然达不到的量级更低，
	// 所以走的是「可截的都截完了仍不达标」那条出口——受保护的 deepthinking 全程没动过。
	threshold := baseTokens + payloadTokens

	result, _ := TruncateToolResultsToBudget(messages, threshold, truncKeepTokens)

	// deepthinking 不应被截断
	dtMsg := result[3]
	if dtMsg.TruncationMarker != nil {
		t.Error("expected deepthinking message NOT to be truncated")
	}
	if dtMsg.Content != largeContent {
		t.Error("expected deepthinking content to be untouched")
	}
	// list_dir 应被截断（替代 deepthinking）
	listDirMsg := result[4]
	if listDirMsg.TruncationMarker == nil {
		t.Error("expected list_dir message to be truncated (deepthinking is protected)")
	}
}

// TestTruncateToolResultsToBudget_Idempotent 幂等性：对已截断的消息再次调用不重复截断
func TestTruncateToolResultsToBudget_Idempotent(t *testing.T) {
	largeContent := strings.Repeat("x", truncPayloadChars)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: strings.Repeat("s", truncFillerChars)},
		{Role: llm.RoleUser, Content: strings.Repeat("u", truncFillerChars)},
		{Role: llm.RoleAssistant, Content: "assistant response"},
		{Role: llm.RoleTool, ToolName: "read_file", Content: largeContent},
	}
	threshold := 100 // 远低于任何可达状态，肯定触发
	keepTokens := truncKeepTokens

	// 第一次截断
	result1, _ := TruncateToolResultsToBudget(messages, threshold, keepTokens)
	dtMsg := result1[3]
	if dtMsg.TruncationMarker == nil {
		t.Fatal("expected first pass to truncate")
	}
	contentAfterFirst := dtMsg.Content
	markerAfterFirst := dtMsg.TruncationMarker.TruncationPass

	// 第二次截断（幂等）
	result2, _ := TruncateToolResultsToBudget(result1, threshold, keepTokens)
	dtMsg2 := result2[3]
	// 已截断的消息应被跳过，内容和 marker 不应再变化
	if dtMsg2.Content != contentAfterFirst {
		t.Error("idempotent: content changed on second call")
	}
	if dtMsg2.TruncationMarker.TruncationPass != markerAfterFirst {
		t.Errorf("idempotent: TruncationPass changed from %d to %d", markerAfterFirst, dtMsg2.TruncationMarker.TruncationPass)
	}
}

// TestTruncateToolResultsToBudget_AllCut 多条大结果全部被截断
func TestTruncateToolResultsToBudget_AllCut(t *testing.T) {
	largeContent := strings.Repeat("x", truncPayloadChars)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: strings.Repeat("s", truncFillerChars)},
		{Role: llm.RoleAssistant, Content: "assistant"},
		{Role: llm.RoleTool, ToolName: "read_file", Content: largeContent},
		{Role: llm.RoleTool, ToolName: "run_bash", Content: largeContent},
		{Role: llm.RoleTool, ToolName: "list_dir", Content: largeContent},
	}
	threshold := 100 // 即使三条全截完也达不到，必须逐条走到底
	keepTokens := truncKeepTokens

	result, _ := TruncateToolResultsToBudget(messages, threshold, keepTokens)

	// 所有可截断的工具结果都应被截断
	for i, msg := range result {
		if msg.Role != llm.RoleTool {
			continue
		}
		if msg.ToolName == "deepthinking" {
			if msg.TruncationMarker != nil {
				t.Errorf("message %d (%s) should not be truncated", i, msg.ToolName)
			}
			continue
		}
		if msg.TruncationMarker == nil {
			t.Errorf("message %d (%s) should be truncated", i, msg.ToolName)
		}
		if !strings.Contains(msg.Content, "[truncated:") {
			t.Errorf("message %d (%s) missing truncation marker", i, msg.ToolName)
		}
	}
}

// TestTruncateToolResultsToBudget_Stats 验证压缩统计信息正确性
func TestTruncateToolResultsToBudget_Stats(t *testing.T) {
	largeContent := strings.Repeat("x", truncPayloadChars)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: strings.Repeat("s", truncFillerChars)},
		{Role: llm.RoleAssistant, Content: "assistant"},
		{Role: llm.RoleTool, ToolName: "read_file", Content: largeContent},
		{Role: llm.RoleTool, ToolName: "run_bash", Content: largeContent},
	}
	threshold := 100 // 远低于任何可达状态，肯定触发
	keepTokens := truncKeepTokens

	_, stats := TruncateToolResultsToBudget(messages, threshold, keepTokens)
	if stats == nil {
		t.Fatal("expected non-nil stats")
	}
	if stats.TruncatedCount != 2 {
		t.Errorf("expected both tool results truncated, got TruncatedCount = %d", stats.TruncatedCount)
	}
	if stats.SavedTokens <= 0 {
		t.Errorf("expected SavedTokens > 0, got %d", stats.SavedTokens)
	}
	if stats.SavedPercent <= 0 || stats.SavedPercent >= 100 {
		t.Errorf("expected SavedPercent in (0, 100), got %f", stats.SavedPercent)
	}
	if stats.OriginalTokens <= stats.CompressedTokens {
		t.Errorf("expected OriginalTokens (%d) > CompressedTokens (%d)", stats.OriginalTokens, stats.CompressedTokens)
	}
	if len(stats.TruncatedTools) != stats.TruncatedCount {
		t.Errorf("expected TruncatedTools len (%d) == TruncatedCount (%d)", len(stats.TruncatedTools), stats.TruncatedCount)
	}
	for _, tool := range stats.TruncatedTools {
		if tool.OriginalTokens <= 0 {
			t.Errorf("expected OriginalTokens > 0 for tool %s", tool.ToolName)
		}
		if tool.KeptTokens <= 0 {
			t.Errorf("expected KeptTokens > 0 for tool %s", tool.ToolName)
		}
		if tool.OmittedTokens < 0 {
			t.Errorf("expected OmittedTokens >= 0 for tool %s", tool.ToolName)
		}
		if tool.OriginalTokens < tool.KeptTokens {
			t.Errorf("expected OriginalTokens (%d) >= KeptTokens (%d) for tool %s", tool.OriginalTokens, tool.KeptTokens, tool.ToolName)
		}
		// 截断必须真的省了 token：Omitted 等于 Original-Kept，否则统计口径是空的。
		if tool.OmittedTokens != tool.OriginalTokens-tool.KeptTokens {
			t.Errorf("expected OmittedTokens (%d) == Original(%d) - Kept(%d) for tool %s",
				tool.OmittedTokens, tool.OriginalTokens, tool.KeptTokens, tool.ToolName)
		}
	}
	// 总账要对得上：Saved/Compressed 是消息级口径，会比各条 Omitted 之和少掉
	// 每条截断标记自身的开销，所以这里只校验消息级恒等式。
	if stats.SavedTokens != stats.OriginalTokens-stats.CompressedTokens {
		t.Errorf("expected SavedTokens (%d) == Original(%d) - Compressed(%d)",
			stats.SavedTokens, stats.OriginalTokens, stats.CompressedTokens)
	}
}

// truncateReference 是增量记账之前的实现：每截断一条，就用 EstimateMessagesTokens
// 把整份消息重算一遍（收尾那次还连着算两遍）。它只在这里当对照基线 ——
// 增量记账的正确性靠"与它逐字段同形"钉住，而不是靠人读 diff 看出来。
func truncateReference(messages []llm.Message, maxTokens, keepTokens int) ([]llm.Message, *ContextCompressionStats) {
	originalTotal := EstimateMessagesTokens(messages)
	if originalTotal <= maxTokens {
		return messages, nil
	}
	stats := &ContextCompressionStats{OriginalTokens: originalTotal}

	type toolMsg struct {
		index int
		msg   *llm.Message
		prio  int
	}
	var prio0, prio1 []toolMsg
	for i := range messages {
		msg := &messages[i]
		if msg.Role != llm.RoleTool || msg.ToolName == "" || msg.Content == "" {
			continue
		}
		prio := toolspec.TruncationPriority(msg.ToolName)
		if prio == -1 {
			continue
		}
		if msg.TruncationMarker != nil {
			continue
		}
		entry := toolMsg{index: i, msg: msg, prio: prio}
		if prio == 0 {
			prio0 = append(prio0, entry)
		} else {
			prio1 = append(prio1, entry)
		}
	}
	var toTruncate []toolMsg
	toTruncate = append(toTruncate, prio0...)
	toTruncate = append(toTruncate, prio1...)

	for _, entry := range toTruncate {
		msg := entry.msg
		originalContent := msg.Content
		originalTokens := tokenutil.EstimateTokens(originalContent)
		truncated := TruncateToTokenBudget(originalContent, keepTokens)
		keptTokens := tokenutil.EstimateTokens(truncated)
		omittedTokens := originalTokens - keptTokens
		if omittedTokens < 0 {
			omittedTokens = 0
		}
		if msg.TruncationMarker != nil {
			msg.TruncationMarker.TruncationPass++
		} else {
			msg.TruncationMarker = &llm.TruncationMarker{
				ToolName:       msg.ToolName,
				OriginalLen:    len(originalContent),
				OmittedLen:     len(originalContent) - len(truncated),
				TruncationPass: 0,
			}
		}
		msg.Content = truncated + truncationNote(originalTokens, keepTokens)
		stats.TruncatedCount++
		stats.TruncatedTools = append(stats.TruncatedTools, TruncatedToolInfo{
			ToolName:       msg.ToolName,
			OriginalTokens: originalTokens,
			KeptTokens:     keptTokens,
			OmittedTokens:  omittedTokens,
		})
		if EstimateMessagesTokens(messages) <= maxTokens {
			stats.CompressedTokens = EstimateMessagesTokens(messages)
			stats.SavedTokens = stats.OriginalTokens - stats.CompressedTokens
			if stats.SavedTokens < 0 {
				stats.SavedTokens = 0
			}
			if stats.OriginalTokens > 0 {
				stats.SavedPercent = float64(stats.SavedTokens) / float64(stats.OriginalTokens) * 100
			}
			return messages, stats
		}
	}
	stats.CompressedTokens = EstimateMessagesTokens(messages)
	stats.SavedTokens = stats.OriginalTokens - stats.CompressedTokens
	if stats.SavedTokens < 0 {
		stats.SavedTokens = 0
	}
	if stats.OriginalTokens > 0 {
		stats.SavedPercent = float64(stats.SavedTokens) / float64(stats.OriginalTokens) * 100
	}
	return messages, stats
}

// assertSameTruncationOutcome 逐字段比对两份结果：stats 全等，
// 消息列表的正文与截断标记也全等（标记是指针，必须比内容不能比指针）。
func assertSameTruncationOutcome(t *testing.T, refMsgs []llm.Message, ref *ContextCompressionStats,
	gotMsgs []llm.Message, got *ContextCompressionStats) {
	t.Helper()

	if (ref == nil) != (got == nil) {
		t.Fatalf("stats 是否为 nil 不一致：参考 %v, 增量 %v", ref == nil, got == nil)
	}
	if ref == nil {
		return
	}
	// stats 的标量字段逐个比（TruncatedTools 是切片，结构体整体不可比）。
	for _, f := range []struct {
		name     string
		ref, got int
	}{
		{"OriginalTokens", ref.OriginalTokens, got.OriginalTokens},
		{"CompressedTokens", ref.CompressedTokens, got.CompressedTokens},
		{"SavedTokens", ref.SavedTokens, got.SavedTokens},
		{"TruncatedCount", ref.TruncatedCount, got.TruncatedCount},
	} {
		if f.ref != f.got {
			t.Errorf("%s = %d, want 参考实现的 %d", f.name, f.got, f.ref)
		}
	}
	if ref.SavedPercent != got.SavedPercent {
		t.Errorf("SavedPercent = %v, want 参考实现的 %v", got.SavedPercent, ref.SavedPercent)
	}
	if len(ref.TruncatedTools) != len(got.TruncatedTools) {
		t.Fatalf("TruncatedTools 条数 = %d, want %d", len(got.TruncatedTools), len(ref.TruncatedTools))
	}
	for i := range ref.TruncatedTools {
		if ref.TruncatedTools[i] != got.TruncatedTools[i] {
			t.Errorf("TruncatedTools[%d] = %+v, want %+v（截断顺序或记账口径漂了）",
				i, got.TruncatedTools[i], ref.TruncatedTools[i])
		}
	}
	if len(refMsgs) != len(gotMsgs) {
		t.Fatalf("消息条数 = %d, want %d", len(gotMsgs), len(refMsgs))
	}
	for i := range refMsgs {
		if refMsgs[i].Content != gotMsgs[i].Content {
			t.Errorf("messages[%d] 正文不等：参考尾 40 字符 %q, 增量 %q",
				i, tail(refMsgs[i].Content, 40), tail(gotMsgs[i].Content, 40))
		}
		rm, gm := refMsgs[i].TruncationMarker, gotMsgs[i].TruncationMarker
		if (rm == nil) != (gm == nil) {
			t.Errorf("messages[%d] 标记有无不一致：参考 %v, 增量 %v", i, rm, gm)
			continue
		}
		if rm != nil && *rm != *gm {
			t.Errorf("messages[%d] 标记内容 = %+v, want %+v", i, *gm, *rm)
		}
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// TestTruncateIncrementalMatchesFullRecompute 是 D2 的等价性闸门：
// 增量记账必须在所有分支上与原实现逐字段同形 —— 未超阈值、中途达标、
// 全部截完仍不达标、优先级混排、受保护工具、空正文、无工具名、已截断过的历史消息。
// 表现不一致就是 stats 与 TUI ⧉ 面板对不上，或"该截的没截"。
func TestTruncateIncrementalMatchesFullRecompute(t *testing.T) {
	large := strings.Repeat("x", truncPayloadChars) // read_file → prio 0
	mid := strings.Repeat("y", truncPayloadChars)   // create_file → prio 1
	mixed := func() []llm.Message {
		return []llm.Message{
			{Role: llm.RoleSystem, Content: strings.Repeat("s", truncFillerChars)},
			{Role: llm.RoleUser, Content: strings.Repeat("u", truncFillerChars)},
			{Role: llm.RoleAssistant, Content: "assistant",
				Reasoning: strings.Repeat("reasoning line ", 30),
				ToolCalls: []llm.ToolCall{{ID: "c1", Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"a.go"}`}}}},
			{Role: llm.RoleTool, ToolName: "deepthinking", Content: large}, // 受保护，永不截
			{Role: llm.RoleTool, ToolName: "create_file", Content: mid},    // prio 1
			{Role: llm.RoleTool, ToolName: "read_file", Content: large},    // prio 0
			{Role: llm.RoleTool, ToolName: "read_file", Content: ""},       // 空正文：跳过
			{Role: llm.RoleTool, ToolName: "", Content: large},             // 无工具名：跳过
		}
	}
	twice := func() []llm.Message {
		already := llm.Message{Role: llm.RoleTool, ToolName: "read_file", Content: large}
		already.TruncationMarker = &llm.TruncationMarker{ToolName: "read_file", OriginalLen: len(large), OmittedLen: 10, TruncationPass: 1}
		return []llm.Message{
			{Role: llm.RoleSystem, Content: strings.Repeat("s", truncFillerChars)},
			already,
			{Role: llm.RoleTool, ToolName: "create_file", Content: mid},
		}
	}
	small := func() []llm.Message {
		return []llm.Message{
			{Role: llm.RoleSystem, Content: "small"},
			{Role: llm.RoleTool, ToolName: "read_file", Content: "small result"},
		}
	}

	scenarios := []struct {
		name       string
		build      func() []llm.Message
		threshold  int
		mustExceed bool // 夹具必须超阈值，否则两条实现都直接返回 nil，断言等于没测（AGENTS.md §11 坑 15）
	}{
		{"未超阈值原样返回", small, 4000, false},
		{"两条工具_截穿", func() []llm.Message {
			return []llm.Message{
				{Role: llm.RoleSystem, Content: strings.Repeat("s", truncFillerChars)},
				{Role: llm.RoleTool, ToolName: "read_file", Content: large},
				{Role: llm.RoleTool, ToolName: "create_file", Content: mid},
			}
		}, 1, true},
		{"优先级混排_截穿", mixed, 1, true},
		{"优先级混排_中途达标", mixed, 900, true},
		{"无工具名可截_只能截一条", func() []llm.Message {
			return []llm.Message{
				{Role: llm.RoleSystem, Content: strings.Repeat("s", truncFillerChars)},
				{Role: llm.RoleTool, ToolName: "", Content: large},
				{Role: llm.RoleTool, ToolName: "read_file", Content: large},
			}
		}, 600, true},
		{"已截断过_截穿", twice, 1, true},
		{"已截断过_中途达标", twice, 500, true},
	}

	for _, sc := range scenarios {
		sc := sc
		t.Run(sc.name, func(t *testing.T) {
			refMsgs := sc.build()
			gotMsgs := sc.build()
			measured := EstimateMessagesTokens(refMsgs)
			if sc.mustExceed && measured <= sc.threshold {
				t.Fatalf("夹具只有 %d token, want 超过阈值 %d（否则没走截断路径）", measured, sc.threshold)
			}
			if !sc.mustExceed && measured > sc.threshold {
				t.Fatalf("夹具 %d token 超过阈值 %d, want 不超（这条测的就是不触发分支）", measured, sc.threshold)
			}

			_, refStats := truncateReference(refMsgs, sc.threshold, truncKeepTokens)
			_, gotStats := TruncateToolResultsToBudget(gotMsgs, sc.threshold, truncKeepTokens)
			assertSameTruncationOutcome(t, refMsgs, refStats, gotMsgs, gotStats)
			if sc.mustExceed && refStats.TruncatedCount == 0 {
				t.Fatalf("阈值 %d 下一条都没截，这条场景没有覆盖到截断逻辑", sc.threshold)
			}
		})
	}
}

// TestTruncateCostScalesLinearly 钉住 R2：旧的"每截一条就全量重算两遍"在这类
// 会话上要花 9.28s（实测 200 条 / 129k token），增量记账只花一遍。
//
// 夹具规模：条数 × 每条正文长度。这里的 'x' 流是 tiktoken 的最坏情形（约 1MB/s），
// 而参考实现的代价是 2×条数 遍全量扫描 —— 所以尺寸要挑在"比值仍然显著、绝对耗时仍是毫秒级"。
// 判据用**与参考实现的比值**而不是纯绝对值：tiktoken 的每字节成本随机器与负载浮动，
// 绝对秒数会 flaky，而"快一个量级"这件事本身就是被测属性。绝对上界仍留一条，
// 防止哪天参考实现被删掉后这条用例失去意义。
// 先跑增量、再跑参考：任何页缓存带来的优势都归参考，比值因此偏保守。
func TestTruncateCostScalesLinearly(t *testing.T) {
	const (
		count   = 60
		payload = 400 // 每条正文长度
	)
	large := strings.Repeat("x", payload)
	build := func() []llm.Message {
		messages := []llm.Message{{Role: llm.RoleSystem, Content: strings.Repeat("s", payload)}}
		for i := 0; i < count; i++ {
			messages = append(messages, llm.Message{Role: llm.RoleTool, ToolName: "read_file", Content: large})
		}
		return messages
	}
	measured := EstimateMessagesTokens(build())
	if measured < 1000 {
		t.Fatalf("夹具只有 %d token, want 足够大（>1k）才能暴露二次复杂度", measured)
	}

	gotMsgs := build()
	start := time.Now()
	_, stats := TruncateToolResultsToBudget(gotMsgs, 1, truncKeepTokens)
	incremental := time.Since(start)

	refMsgs := build()
	start = time.Now()
	_, refStats := truncateReference(refMsgs, 1, truncKeepTokens)
	fullRecompute := time.Since(start)

	if stats == nil || stats.TruncatedCount != count {
		t.Fatalf("TruncatedCount = %+v, want 全部 %d 条都被截", stats, count)
	}
	// 收尾总量必须与全量重算一致（增量记账漏减 delta 的话这里就红）。
	if want := EstimateMessagesTokens(gotMsgs); stats.CompressedTokens != want {
		t.Errorf("CompressedTokens = %d, want 全量重算的 %d（增量记账漂了）", stats.CompressedTokens, want)
	}
	assertSameTruncationOutcome(t, refMsgs, refStats, gotMsgs, stats)

	if incremental*8 > fullRecompute {
		t.Errorf("增量记账 %s vs 全量重算 %s, want 至少快 8 倍（每截一条全量重算被加回来了？）",
			incremental, fullRecompute)
	}
	if incremental > 2*time.Second {
		t.Errorf("增量记账单步 %s, want < 2s（本地估算开销失控）", incremental)
	}
	t.Logf("%d 条 / %d token：增量 %s，全量重算 %s", count, measured, incremental, fullRecompute)
}
