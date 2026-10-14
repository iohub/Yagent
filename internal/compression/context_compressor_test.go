package compression

import (
	// 截断优先级现在由工具注册表派生（Spec.Trunc）。注册表靠各包 init() 自注册，
	// 而本包在生产链路里总是和 tools/agents 一起被链接；单元测试二进制不链接它们就会
	// 查不到声明、把 deepthinking 当未注册工具返回常规优先级，所以这里显式拉进来。
	"strings"
	"testing"
	_ "yagent/internal/tools"

	"yagent/internal/llm"
	"yagent/internal/tokenutil"
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
