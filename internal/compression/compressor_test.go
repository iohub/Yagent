// Package compression — compressor_test.go — P0-1 Phase 3-0: ContextCompressor 组件单测。

// 独立可测（注入 fake thinklink 窄接口与可控 LLM 引擎），不依赖 DirectorAgent。
// 以 director_characterization_test.go 的断言为参照但独立编写，覆盖：
//  1. 紧急压缩：少块保留/多块 LLM 总结/LLM 失败降级截断/nil memory；
//  2. 终极压缩：重建上下文/thinklink 交互（RebuildPrompt 调用序列）/回调清理/nil memory；
//  3. 阈值边界：ShouldCompress 严格大于语义、userBudget 下限钳位。
package compression

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/thinklink"
)

// ─── fake 实现 ───────────────────────────────────────────────────────────────

// compressorTestEngine 可控 LLM 引擎（实现 llm.Engine，记录调用次数）。
type compressorTestEngine struct {
	calls           int
	responseContent string
	err             error
}

func (e *compressorTestEngine) GenerateContent(_ context.Context, _ []llm.Message, _ []llm.ToolDef, _ *llm.CallOptions) (*llm.Response, error) {
	e.calls++
	if e.err != nil {
		return nil, e.err
	}
	return &llm.Response{Choices: []llm.Choice{{Content: e.responseContent}}}, nil
}
func (e *compressorTestEngine) Model() string         { return "fake-model" }
func (e *compressorTestEngine) CloseIdleConnections() {}

// fakeThinkLink 记录 Count/RebuildPrompt 调用的 thinklink 窄接口 fake。
// RebuildPrompt 语义贴近真实 Store:keepPlans>0 保留最近 N 个 T&P 块，
// keepPlans<=0 全部保留；用户输入区始终全部保留（最后一条标 [CURRENT TASK]）。
type fakeThinkLink struct {
	planCount    int
	inputCount   int
	plans        []string // T&P 块内容（按时间序）
	userInputs   []string // 用户输入内容（按时间序）
	rebuildCalls []int    // 每次 RebuildPrompt 的 keepPlans 参数
}

func (f *fakeThinkLink) Count(kind thinklink.Kind) int {
	if kind == thinklink.KindThoughtPlan {
		return f.planCount
	}
	return f.inputCount
}

func (f *fakeThinkLink) RebuildPrompt(keepPlans int) string {
	f.rebuildCalls = append(f.rebuildCalls, keepPlans)
	var sb strings.Builder
	sb.WriteString("Your previous conversation context has been RESET.\n")
	sb.WriteString("\n=== Original user input(s) ===\n")
	if len(f.userInputs) == 0 {
		sb.WriteString("(no user input recorded)\n")
	}
	for i, ui := range f.userInputs {
		marker := ""
		if i == len(f.userInputs)-1 {
			marker = "[CURRENT TASK] "
		}
		sb.WriteString("\n" + marker + "[USER INPUT " + strconv.Itoa(i+1) + "]\n" + ui + "\n")
	}
	sb.WriteString("\n=== Thought & Plan blocks ===\n")
	plans := f.plans
	offset := 0
	if keepPlans > 0 && len(plans) > keepPlans {
		offset = len(plans) - keepPlans
		plans = plans[offset:]
	}
	if len(plans) == 0 {
		sb.WriteString("(no Thought & Plan blocks recorded)\n")
	}
	for i, p := range plans {
		sb.WriteString("\n[TP-" + strconv.Itoa(offset+i+1) + "]\n" + p + "\n")
	}
	return sb.String()
}

// newCompressorTestEngine 构造可控引擎，responseContent 默认为可识别的总结文本。
func newCompressorTestEngine() *compressorTestEngine {
	return &compressorTestEngine{responseContent: "COMPRESSOR_TEST_SUMMARY"}
}

// newCompressor 构造带计数回调的 ContextCompressor。
func newCompressor(engine llm.Engine, store ThinkLinkStore, callbackCalls *int) *ContextCompressor {
	return NewContextCompressor(engine, "Director", store, func() {
		if callbackCalls != nil {
			*callbackCalls++
		}
	})
}

// assistantMessages 构造 n 条 assistant 文本消息（无 T&P 关键字，紧急压缩时每条整体作为一个块）。
func assistantMessages(n int, prefix string) []llm.Message {
	msgs := make([]llm.Message, 0, n)
	for i := 0; i < n; i++ {
		msgs = append(msgs, llm.Message{
			Role:    llm.RoleAssistant,
			Content: prefix + " " + string(rune('A'+i)),
		})
	}
	return msgs
}

// ─── 组 1:ApplyEmergency ────────────────────────────────────────────────────

// TestCompressorApplyEmergencyFewBlocksKeepsAll 少块（≤ DefaultEmergencyCompressKeepLastN）
// 全部保留且不调用 LLM；memory 覆盖为单条 human 消息且内容与输出末条 user 一致。
func TestCompressorApplyEmergencyFewBlocksKeepsAll(t *testing.T) {
	const (
		systemPrompt = "SYSTEM_KEEP"
		originalTask = "原始任务 XYZ"
	)
	engine := newCompressorTestEngine()
	callbackCalls := 0
	c := newCompressor(engine, &fakeThinkLink{}, &callbackCalls)
	mem := memory.NewConversationMemory(10)
	mem.AddHumanMessage(originalTask)

	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: systemPrompt},
		{Role: llm.RoleUser, Content: "USER_TASK_INLINE"},
	}
	msgs = append(msgs, assistantMessages(2, "assistant block")...)

	out, stats := c.ApplyEmergency(context.Background(), msgs, 100000, mem)

	if engine.calls != 0 {
		t.Errorf("LLM calls = %d, want 0 (few blocks should not call LLM)", engine.calls)
	}
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	if out[0].Role != llm.RoleSystem || out[0].Content != systemPrompt {
		t.Errorf("out[0] should preserve first system message, got role=%q content=%q", out[0].Role, out[0].Content)
	}
	if out[1].Role != llm.RoleUser {
		t.Errorf("out[1].Role = %q, want %q", out[1].Role, llm.RoleUser)
	}
	for _, sub := range []string{"### 原始任务", originalTask, "assistant block A", "assistant block B"} {
		if !strings.Contains(out[1].Content, sub) {
			t.Errorf("compressed user content should contain %q", sub)
		}
	}
	if stats.ExtractedBlocks != 2 || stats.KeptBlocks != 2 || stats.SummarizedByLLM {
		t.Errorf("stats invalid: extracted=%d kept=%d summarizedByLLM=%v, want 2/2/false",
			stats.ExtractedBlocks, stats.KeptBlocks, stats.SummarizedByLLM)
	}
	if stats.OriginalTokens <= 0 || stats.CompressedTokens <= 0 || stats.SavedTokens < 0 {
		t.Errorf("stats tokens invalid: original=%d compressed=%d saved=%d",
			stats.OriginalTokens, stats.CompressedTokens, stats.SavedTokens)
	}
	if len(mem.Messages) != 1 || mem.Messages[0].Type != memory.MessageTypeHuman || mem.Messages[0].Content != out[1].Content {
		t.Errorf("memory should be overwritten to single human message equal to out[1].Content")
	}
}

// TestCompressorApplyEmergencyLLMSummary 多块（> 3）时超出部分交给 LLM 总结
// （保留最近 N 个块），总结文本进入输出，最早块不出现在保留区。
func TestCompressorApplyEmergencyLLMSummary(t *testing.T) {
	const (
		originalTask = "原始任务 SUM"
		summaryText  = "MOCK_SUMMARY_TEXT"
	)
	engine := newCompressorTestEngine()
	engine.responseContent = summaryText
	callbackCalls := 0
	c := newCompressor(engine, &fakeThinkLink{}, &callbackCalls)
	mem := memory.NewConversationMemory(10)
	mem.AddHumanMessage(originalTask)

	msgs := []llm.Message{{Role: llm.RoleSystem, Content: "SYSTEM_KEEP"}}
	msgs = append(msgs, assistantMessages(4, "assistant block")...)

	out, stats := c.ApplyEmergency(context.Background(), msgs, 100000, mem)

	if engine.calls != 1 {
		t.Errorf("LLM calls = %d, want 1", engine.calls)
	}
	if !stats.SummarizedByLLM {
		t.Errorf("stats.SummarizedByLLM = false, want true")
	}
	if stats.SummarizedBlocks != 1 || stats.KeptBlocks != 3 {
		t.Errorf("stats = %d summarized / %d kept, want 1/3", stats.SummarizedBlocks, stats.KeptBlocks)
	}
	if !strings.Contains(out[1].Content, summaryText) {
		t.Errorf("compressed user content should contain LLM summary %q", summaryText)
	}
	if strings.Contains(out[1].Content, "assistant block A") {
		t.Errorf("earliest block should be replaced by LLM summary, not kept")
	}
	for _, sub := range []string{"assistant block B", "assistant block C", "assistant block D"} {
		if !strings.Contains(out[1].Content, sub) {
			t.Errorf("compressed user content should contain kept block %q", sub)
		}
	}
}

// TestCompressorApplyEmergencyLLMFailureFallback LLM 失败时降级为截断拼接：
// beforeBlocks 以原文形式保留（非 LLM 总结），SummarizedByLLM=false。
func TestCompressorApplyEmergencyLLMFailureFallback(t *testing.T) {
	const originalTask = "原始任务 FALLBACK"
	engine := newCompressorTestEngine()
	engine.err = errors.New("forced llm failure for compressor test")
	callbackCalls := 0
	c := newCompressor(engine, &fakeThinkLink{}, &callbackCalls)
	mem := memory.NewConversationMemory(10)
	mem.AddHumanMessage(originalTask)

	msgs := []llm.Message{{Role: llm.RoleSystem, Content: "SYSTEM_KEEP"}}
	msgs = append(msgs, assistantMessages(4, "assistant block")...)

	out, stats := c.ApplyEmergency(context.Background(), msgs, 100000, mem)

	if engine.calls != 1 {
		t.Errorf("LLM calls = %d, want 1", engine.calls)
	}
	if stats.SummarizedByLLM {
		t.Errorf("stats.SummarizedByLLM = true, want false (LLM failed)")
	}
	// 降级时 beforeBlocks 以原文拼接保留
	for _, sub := range []string{"assistant block A", "assistant block B", "assistant block D"} {
		if !strings.Contains(out[1].Content, sub) {
			t.Errorf("fallback content should contain original block %q", sub)
		}
	}
}

// TestCompressorApplyEmergencyNilMemory mem 为 nil 时跳过 memory 操作，
// 原始任务从消息列表第一条 user 消息回退提取（底层 EmergencyCompressMessages 逻辑）。
func TestCompressorApplyEmergencyNilMemory(t *testing.T) {
	const inlineUserMsg = "USER_TASK_FROM_MESSAGES"
	engine := newCompressorTestEngine()
	c := newCompressor(engine, &fakeThinkLink{}, nil)

	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: "SYSTEM_KEEP"},
		{Role: llm.RoleUser, Content: inlineUserMsg},
	}
	msgs = append(msgs, assistantMessages(2, "assistant block")...)

	out, stats := c.ApplyEmergency(context.Background(), msgs, 100000, nil)

	if out == nil || len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2 (nil memory should not panic)", len(out))
	}
	if !strings.Contains(out[1].Content, inlineUserMsg) {
		t.Errorf("original task should fall back to first user message in list, want %q", inlineUserMsg)
	}
	if stats.ExtractedBlocks != 2 {
		t.Errorf("stats.ExtractedBlocks = %d, want 2", stats.ExtractedBlocks)
	}
}

// ─── 组 2:ApplyUltimate ─────────────────────────────────────────────────────

// newUltimateFixtures 构造带 fake thinklink（2 plans + 1 user input）的压缩组件与 memory。
func newUltimateFixtures(t *testing.T, keepPlansLimit int, callbackCalls *int) (*ContextCompressor, *fakeThinkLink, *memory.ConversationMemory, []llm.Message) {
	t.Helper()
	engine := newCompressorTestEngine()
	store := &fakeThinkLink{
		planCount:  2,
		inputCount: 1,
		plans:      []string{"PLAN_ONE", "PLAN_TWO"},
		userInputs: []string{"原始任务 U"},
	}
	c := newCompressor(engine, store, callbackCalls)
	mem := memory.NewConversationMemory(10)
	mem.AddHumanMessage("old stale history")
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: "SYSTEM_PROMPT_KEEP"},
		{Role: llm.RoleUser, Content: "stale user message"},
		{Role: llm.RoleAssistant, Content: "stale assistant message"},
	}
	return c, store, mem, msgs
}

// TestCompressorApplyUltimateRebuildContext 预算充足时保留全部 T&P 块
// （keep == totalPlans），重建消息含原始任务与 [CURRENT TASK] 标记；
// thinklink 交互正确（Count 查询 T&P 与用户输入）；回调执行一次；memory 覆盖。
func TestCompressorApplyUltimateRebuildContext(t *testing.T) {
	callbackCalls := 0
	c, store, mem, msgs := newUltimateFixtures(t, 0, &callbackCalls)

	out, stats := c.ApplyUltimate(msgs, 100000, mem, 0)

	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	if out[0].Role != llm.RoleSystem || out[0].Content != "SYSTEM_PROMPT_KEEP" {
		t.Errorf("out[0] should preserve system message, got role=%q content=%q", out[0].Role, out[0].Content)
	}
	if out[1].Role != llm.RoleUser {
		t.Errorf("out[1].Role = %q, want %q", out[1].Role, llm.RoleUser)
	}
	for _, sub := range []string{"=== Original user input(s) ===", "[CURRENT TASK]", "原始任务 U", "PLAN_ONE", "PLAN_TWO"} {
		if !strings.Contains(out[1].Content, sub) {
			t.Errorf("rebuilt user content should contain %q", sub)
		}
	}
	if stats.Truncated {
		t.Errorf("stats.Truncated = true, want false (under budget)")
	}
	if stats.TotalPlans != 2 || stats.KeptPlans != 2 || stats.UserInputs != 1 {
		t.Errorf("stats invalid: total=%d kept=%d inputs=%d, want 2/2/1",
			stats.TotalPlans, stats.KeptPlans, stats.UserInputs)
	}
	// thinklink 交互：预算充足时 RebuildPrompt 只调用一次（keep=totalPlans）
	if len(store.rebuildCalls) != 1 || store.rebuildCalls[0] != 2 {
		t.Errorf("RebuildPrompt calls = %v, want [2]", store.rebuildCalls)
	}
	// 回调清理：pendingSubAgentMemory 清理回调执行一次
	if callbackCalls != 1 {
		t.Errorf("clearPendingSubAgent callback calls = %d, want 1", callbackCalls)
	}
	// memory 覆盖：清空后只剩一条 human 消息，内容与输出末条 user 一致
	if len(mem.Messages) != 1 || mem.Messages[0].Type != memory.MessageTypeHuman || mem.Messages[0].Content != out[1].Content {
		t.Errorf("memory should be overwritten to single human message equal to out[1].Content")
	}
	if stats.OriginalTokens <= 0 || stats.CompressedTokens <= 0 || stats.SavedTokens < 0 {
		t.Errorf("stats tokens invalid: original=%d compressed=%d saved=%d",
			stats.OriginalTokens, stats.CompressedTokens, stats.SavedTokens)
	}
}

// TestCompressorApplyUltimateTinyBudgetHardTruncates 预算极小时循环递减保留数量
// （RebuildPrompt 调用序列 keep, keep-1, ..., 0）直至硬截断兜底（truncated=true，keep=0），
// 重建内容非空。
func TestCompressorApplyUltimateTinyBudgetHardTruncates(t *testing.T) {
	callbackCalls := 0
	// threshold=1:远小于重建提示的固有体积 → 递减到 0 后硬截断兜底
	c, store, mem, msgs := newUltimateFixtures(t, 0, &callbackCalls)

	out, stats := c.ApplyUltimate(msgs, 1, mem, 0)

	if stats.Truncated != true {
		t.Errorf("stats.Truncated = %v, want true (tiny budget hard-truncates)", stats.Truncated)
	}
	if stats.KeptPlans != 0 {
		t.Errorf("stats.KeptPlans = %d, want 0", stats.KeptPlans)
	}
	// 循环保护：RebuildPrompt 递减调用序列 [2, 1, 0]
	if len(store.rebuildCalls) != 3 || store.rebuildCalls[0] != 2 || store.rebuildCalls[1] != 1 || store.rebuildCalls[2] != 0 {
		t.Errorf("RebuildPrompt calls = %v, want [2 1 0]", store.rebuildCalls)
	}
	if strings.TrimSpace(out[1].Content) == "" {
		t.Errorf("rebuilt user content should not be empty (truncated=%v)", stats.Truncated)
	}
	if callbackCalls != 1 {
		t.Errorf("clearPendingSubAgent callback calls = %d, want 1", callbackCalls)
	}
}

// TestCompressorApplyUltimateKeepPlansLimit 配置指定保留数量上限（0=全部保留）时，
// 取配置值与全部数量的较小值。
func TestCompressorApplyUltimateKeepPlansLimit(t *testing.T) {
	callbackCalls := 0
	c, store, mem, msgs := newUltimateFixtures(t, 1, &callbackCalls)

	out, stats := c.ApplyUltimate(msgs, 100000, mem, 1)

	if stats.Truncated {
		t.Errorf("stats.Truncated = true, want false (budget sufficient)")
	}
	if stats.KeptPlans != 1 {
		t.Errorf("stats.KeptPlans = %d, want 1 (capped by keepPlansLimit)", stats.KeptPlans)
	}
	if len(store.rebuildCalls) != 1 || store.rebuildCalls[0] != 1 {
		t.Errorf("RebuildPrompt calls = %v, want [1]", store.rebuildCalls)
	}
	// 上限生效时只保留最近的块（PLAN_TWO），最早的 PLAN_ONE 不出现
	if !strings.Contains(out[1].Content, "PLAN_TWO") || strings.Contains(out[1].Content, "PLAN_ONE") {
		t.Errorf("rebuilt content should keep only the most recent plan (PLAN_TWO), got: %q", out[1].Content)
	}
}

// TestCompressorApplyUltimateNilMemory mem 为 nil 时跳过 memory 操作，不 panic，
// 回调仍执行。
func TestCompressorApplyUltimateNilMemory(t *testing.T) {
	callbackCalls := 0
	c, _, _, msgs := newUltimateFixtures(t, 0, &callbackCalls)

	out, stats := c.ApplyUltimate(msgs, 100000, nil, 0)

	if out == nil || len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2 (nil memory should not panic)", len(out))
	}
	if stats.Truncated {
		t.Errorf("stats.Truncated = true, want false")
	}
	if callbackCalls != 1 {
		t.Errorf("clearPendingSubAgent callback calls = %d, want 1", callbackCalls)
	}
}

// TestCompressorApplyUltimateZeroTotalPlans thinklink 无 T&P 块时仍可重建
// （只留用户原始输入区），不 panic。
func TestCompressorApplyUltimateZeroTotalPlans(t *testing.T) {
	callbackCalls := 0
	engine := newCompressorTestEngine()
	store := &fakeThinkLink{
		planCount:  0,
		inputCount: 1,
		userInputs: []string{"只有任务没有计划"},
	}
	c := newCompressor(engine, store, &callbackCalls)
	mem := memory.NewConversationMemory(10)
	msgs := []llm.Message{{Role: llm.RoleSystem, Content: "SYSTEM_KEEP"}}

	out, stats := c.ApplyUltimate(msgs, 100000, mem, 0)

	if stats.TotalPlans != 0 || stats.KeptPlans != 0 {
		t.Errorf("stats = total %d kept %d, want 0/0", stats.TotalPlans, stats.KeptPlans)
	}
	if !strings.Contains(out[1].Content, "只有任务没有计划") {
		t.Errorf("rebuilt content should contain the only user input")
	}
	if stats.Truncated {
		t.Errorf("stats.Truncated = true, want false")
	}
}

// ─── 组 3:阈值边界 ──────────────────────────────────────────────────────────

// TestCompressorShouldCompress 触发判断辅助：严格大于语义（等于阈值不触发）。
func TestCompressorShouldCompress(t *testing.T) {
	c := NewContextCompressor(nil, "Director", &fakeThinkLink{}, nil)

	// 空消息 0 token:等于阈值不触发（严格大于语义）
	if c.ShouldCompress(nil, 0) {
		t.Errorf("nil messages (0 tokens) over threshold 0 should not trigger")
	}
	// 小消息大阈值：不触发
	msgs := []llm.Message{{Role: llm.RoleUser, Content: "hi"}}
	if c.ShouldCompress(msgs, 100000) {
		t.Errorf("small messages over big threshold should not trigger")
	}
	// 超阈值：触发（长消息肯定超过 1 token）
	long := []llm.Message{{Role: llm.RoleUser, Content: strings.Repeat("word ", 50)}}
	if !c.ShouldCompress(long, 1) {
		t.Errorf("messages above tiny threshold should trigger")
	}
}

// TestCompressorApplyUltimateNegativeBudgetClamped threshold 小于 system 消息
// 自身 token 时 userBudget 钳位为 0（不产生负预算），硬截断兜底。
func TestCompressorApplyUltimateNegativeBudgetClamped(t *testing.T) {
	callbackCalls := 0
	c, _, mem, msgs := newUltimateFixtures(t, 0, &callbackCalls)

	// threshold=1 而 system 消息 token 已 > 1 → userBudget = 0（钳位）
	out, stats := c.ApplyUltimate(msgs, 1, mem, 0)

	if stats.Truncated != true {
		t.Errorf("stats.Truncated = %v, want true (negative budget clamped to 0, hard truncate)", stats.Truncated)
	}
	if strings.TrimSpace(out[1].Content) == "" {
		t.Errorf("truncated rebuilt content should not be empty")
	}
	if len(mem.Messages) != 1 || mem.Messages[0].Type != memory.MessageTypeHuman {
		t.Errorf("memory should be overwritten to single human message")
	}
}