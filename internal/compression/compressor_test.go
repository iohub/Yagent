// Package compression — compressor_test.go — ContextCompressor 组件单测（方案C 批次3 golden 基线）。

// 终极压缩重建（ApplyUltimate）使用真实 *thinklink.Store 构造任务状态
// （用户输入 + TodoWrite 快照 + 完成台账），覆盖设计文档 6.1 compression 层：
//  1. 终极重建 golden：全部用户输入 + Current task list（in_progress 置顶/排序/
//     activeForm 渲染）+ Completed tasks；无快照占位渲染；
//  2. 预算降级顺序：① 可选段落（历史快照轨迹 + legacy T&P 残留）→ ② 完成台账
//     逐条递减（KeepTodoLedgerLimit 截断/裁光占位）→ ③ 硬截断兜底；
//  3. legacy 静默兜底 flag 开（含残留段）/关（不含）；
//  4. 紧急压缩：少块保留/多块 LLM 总结/LLM 失败降级截断/nil memory；
//  5. 阈值边界：ShouldCompress 严格大于语义、userBudget 下限钳位。
package compression

import (
	"context"
	"errors"
	"strings"
	"testing"

	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/thinklink"
)

// ─── 可控 LLM 引擎 ───────────────────────────────────────────────────────────

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

// newCompressorTestEngine 构造可控引擎，responseContent 默认为可识别的总结文本。
func newCompressorTestEngine() *compressorTestEngine {
	return &compressorTestEngine{responseContent: "COMPRESSOR_TEST_SUMMARY"}
}

// newCompressor 构造带计数回调的 ContextCompressor（store 用真实 *thinklink.Store，
// 覆盖快照序列化/反序列化真实路径）。
func newCompressor(engine llm.Engine, store *thinklink.Store, callbackCalls *int) *ContextCompressor {
	return NewContextCompressor(engine, "Director", store, func() {
		if callbackCalls != nil {
			*callbackCalls++
		}
	})
}

// assistantMessages 构造 n 条 assistant 文本消息（紧急压缩时每条整体作为一个块）。
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
	c := newCompressor(engine, thinklink.NewStore(200), &callbackCalls)
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
	c := newCompressor(engine, thinklink.NewStore(200), &callbackCalls)
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
	c := newCompressor(engine, thinklink.NewStore(200), &callbackCalls)
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
	c := newCompressor(engine, thinklink.NewStore(200), nil)

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

// ─── 组 2:ApplyUltimate（方案C golden 基线）──────────────────────────────────

// newUltimateStore 构造填充了用户输入/任务清单快照/完成台账的真实 Store：
//   - 1 条用户输入（"original task"）；
//   - 完成台账含 TASK_A（由 pending → completed 迁移产生，CompletedAtStep=3）；
//   - 当前清单 = TASK_B in_progress + TASK_C pending。
func newUltimateStore(t *testing.T) *thinklink.Store {
	t.Helper()
	store := thinklink.NewStore(200)
	if _, ok := store.AddUserInput("original task", 1); !ok {
		t.Fatalf("AddUserInput should succeed")
	}
	if _, err := store.SetTodos([]thinklink.TodoItem{
		{Content: "TASK_A", ActiveForm: "working on task A", Status: thinklink.StatusPending},
	}, 2); err != nil {
		t.Fatalf("SetTodos failed: %v", err)
	}
	if _, err := store.SetTodos([]thinklink.TodoItem{
		{Content: "TASK_A", ActiveForm: "working on task A", Status: thinklink.StatusCompleted},
		{Content: "TASK_B", ActiveForm: "working on task B", Status: thinklink.StatusInProgress},
		{Content: "TASK_C", ActiveForm: "working on task C", Status: thinklink.StatusPending},
	}, 3); err != nil {
		t.Fatalf("SetTodos failed: %v", err)
	}
	return store
}

// newUltimateFixtures 构造带真实 Store（newUltimateStore）的压缩组件与
// memory/messages（含 system/user/assistant 旧历史）。
func newUltimateFixtures(t *testing.T, callbackCalls *int) (*ContextCompressor, *thinklink.Store, *memory.ConversationMemory, []llm.Message) {
	t.Helper()
	engine := newCompressorTestEngine()
	store := newUltimateStore(t)
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

// TestCompressorApplyUltimateRebuildGolden 预算充足时的 golden 重建基线：
// 重置横幅（含任务清单权威指令）+ 全部用户输入（[CURRENT TASK]）+
// Current task list（in_progress 置顶渲染 activeForm、pending 渲染 content、
// completed 渲染 activeForm 带完成标记，顺序正确）+ Completed tasks
// （台账 content + 完成时间/step）；默认不渲染可选段落；回调执行一次；
// memory 覆盖为单条 human 消息。
func TestCompressorApplyUltimateRebuildGolden(t *testing.T) {
	callbackCalls := 0
	c, _, mem, msgs := newUltimateFixtures(t, &callbackCalls)

	out, stats := c.ApplyUltimate(msgs, 100000, mem, UltimateRebuildOptions{})

	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2", len(out))
	}
	if out[0].Role != llm.RoleSystem || out[0].Content != "SYSTEM_PROMPT_KEEP" {
		t.Errorf("out[0] should preserve system message, got role=%q content=%q", out[0].Role, out[0].Content)
	}
	if out[1].Role != llm.RoleUser {
		t.Errorf("out[1].Role = %q, want %q", out[1].Role, llm.RoleUser)
	}
	content := out[1].Content

	// 重置横幅：上下文已重置说明 + 任务清单权威指令（缺失细节需用工具重新核实）
	for _, sub := range []string{
		"Your previous conversation context has been RESET",
		"authoritative source of task state",
		"re-verify them with tools instead of guessing",
	} {
		if !strings.Contains(content, sub) {
			t.Errorf("rebuilt content should contain banner %q", sub)
		}
	}
	// 全部用户输入（时间戳/step 格式 + [CURRENT TASK] 标记）
	for _, sub := range []string{"=== Original user input(s) ===", "[CURRENT TASK]", "original task", "[USER INPUT 1]", ", step 1)"} {
		if !strings.Contains(content, sub) {
			t.Errorf("rebuilt content should contain user input %q", sub)
		}
	}
	// Current task list：in_progress 置顶渲染 activeForm、pending 渲染 content、
	// completed 渲染 activeForm
	for _, sub := range []string{
		"=== Current task list ===",
		"[IN PROGRESS] working on task B",
		"[ ] TASK_C",
		"[x] working on task A",
	} {
		if !strings.Contains(content, sub) {
			t.Errorf("rebuilt content should contain task list entry %q", sub)
		}
	}
	// 渲染顺序：in_progress → pending → completed → Completed tasks 区
	idxInProgress := strings.Index(content, "[IN PROGRESS] working on task B")
	idxPending := strings.Index(content, "[ ] TASK_C")
	idxCompleted := strings.Index(content, "[x] working on task A")
	idxLedger := strings.Index(content, "=== Completed tasks ===")
	if idxInProgress < 0 || idxPending < 0 || idxCompleted < 0 || idxLedger < 0 {
		t.Fatalf("golden sections missing: inProgress=%d pending=%d completed=%d ledger=%d",
			idxInProgress, idxPending, idxCompleted, idxLedger)
	}
	if !(idxInProgress < idxPending && idxPending < idxCompleted && idxCompleted < idxLedger) {
		t.Errorf("render order invalid: in_progress(%d) < pending(%d) < completed(%d) < ledger(%d)",
			idxInProgress, idxPending, idxCompleted, idxLedger)
	}
	// Completed tasks：台账 content + 完成时间/step
	if !strings.Contains(content, "[x] TASK_A (completed at step 3, ") {
		t.Errorf("rebuilt content should contain ledger entry with completed step, got:\n%s", content)
	}
	// 默认（KeepRecentTodoSnapshots=0 / legacy flag=false）不渲染可选段落
	if strings.Contains(content, "=== Todo history") || strings.Contains(content, "=== Legacy Thought & Plan fragments ===") {
		t.Errorf("rebuilt content should NOT contain optional sections by default")
	}
	// stats
	if stats.Truncated {
		t.Errorf("stats.Truncated = true, want false (under budget)")
	}
	if stats.TotalTodos != 3 || stats.KeptLedger != 1 || stats.UserInputs != 1 {
		t.Errorf("stats invalid: todos=%d ledger=%d inputs=%d, want 3/1/1",
			stats.TotalTodos, stats.KeptLedger, stats.UserInputs)
	}
	// 回调执行一次；memory 覆盖为单条 human 消息且与重建内容一致
	if callbackCalls != 1 {
		t.Errorf("clearPendingSubAgent callback calls = %d, want 1", callbackCalls)
	}
	if len(mem.Messages) != 1 || mem.Messages[0].Type != memory.MessageTypeHuman || mem.Messages[0].Content != out[1].Content {
		t.Errorf("memory should be overwritten to single human message equal to out[1].Content")
	}
	if stats.OriginalTokens <= 0 || stats.CompressedTokens <= 0 || stats.SavedTokens < 0 {
		t.Errorf("stats tokens invalid: original=%d compressed=%d saved=%d",
			stats.OriginalTokens, stats.CompressedTokens, stats.SavedTokens)
	}
}

// TestCompressorApplyUltimateNoTaskList 无 TodoWrite 历史（无快照）时渲染
// "(no task list)"占位行，空台账渲染"(no completed tasks recorded)"。
func TestCompressorApplyUltimateNoTaskList(t *testing.T) {
	callbackCalls := 0
	engine := newCompressorTestEngine()
	store := thinklink.NewStore(200)
	if _, ok := store.AddUserInput("only task", 1); !ok {
		t.Fatalf("AddUserInput should succeed")
	}
	c := newCompressor(engine, store, &callbackCalls)
	mem := memory.NewConversationMemory(10)
	msgs := []llm.Message{{Role: llm.RoleSystem, Content: "SYSTEM_KEEP"}}

	out, stats := c.ApplyUltimate(msgs, 100000, mem, UltimateRebuildOptions{})

	content := out[1].Content
	if !strings.Contains(content, "(no task list)") {
		t.Errorf("no-snapshot rebuild should contain '(no task list)' placeholder, got:\n%s", content)
	}
	if !strings.Contains(content, "(no completed tasks recorded)") {
		t.Errorf("empty ledger should render '(no completed tasks recorded)' placeholder")
	}
	if !strings.Contains(content, "only task") {
		t.Errorf("rebuilt content should contain the only user input")
	}
	if stats.TotalTodos != 0 || stats.KeptLedger != 0 || stats.UserInputs != 1 {
		t.Errorf("stats invalid: todos=%d ledger=%d inputs=%d, want 0/0/1",
			stats.TotalTodos, stats.KeptLedger, stats.UserInputs)
	}
}

// newLedgerTestStore 构造台账 3 条（按序迁移产生，content 为给定文本）+
// 当前清单 TASK_Z in_progress 的 Store。
func newLedgerTestStore(t *testing.T, contents []string) *thinklink.Store {
	t.Helper()
	store := thinklink.NewStore(200)
	if _, ok := store.AddUserInput("original task", 1); !ok {
		t.Fatalf("AddUserInput should succeed")
	}
	pending := make([]thinklink.TodoItem, 0, len(contents))
	for _, c := range contents {
		pending = append(pending, thinklink.TodoItem{Content: c, ActiveForm: "working on " + c, Status: thinklink.StatusPending})
	}
	if _, err := store.SetTodos(pending, 2); err != nil {
		t.Fatalf("SetTodos failed: %v", err)
	}
	completed := make([]thinklink.TodoItem, 0, len(contents)+1)
	for _, c := range contents {
		completed = append(completed, thinklink.TodoItem{Content: c, ActiveForm: "working on " + c, Status: thinklink.StatusCompleted})
	}
	completed = append(completed, thinklink.TodoItem{Content: "TASK_Z", ActiveForm: "working on TASK_Z", Status: thinklink.StatusInProgress})
	if _, err := store.SetTodos(completed, 3); err != nil {
		t.Fatalf("SetTodos failed: %v", err)
	}
	return store
}

// TestCompressorApplyUltimateLedgerLimit KeepTodoLedgerLimit 截断生效：
// 台账 3 条仅保留最近 1 条（配置上限），更早的不出现；0=全部保留。
func TestCompressorApplyUltimateLedgerLimit(t *testing.T) {
	callbackCalls := 0
	engine := newCompressorTestEngine()
	store := newLedgerTestStore(t, []string{"LEDGER_ONE", "LEDGER_TWO", "LEDGER_THREE"})
	c := newCompressor(engine, store, &callbackCalls)
	mem := memory.NewConversationMemory(10)
	msgs := []llm.Message{{Role: llm.RoleSystem, Content: "SYSTEM_KEEP"}}

	out, stats := c.ApplyUltimate(msgs, 100000, mem, UltimateRebuildOptions{KeepTodoLedgerLimit: 1})

	content := out[1].Content
	if !strings.Contains(content, "[x] LEDGER_THREE (completed at step") {
		t.Errorf("ledger limit=1 should keep the most recent entry LEDGER_THREE, got:\n%s", content)
	}
	// 台账截断只影响 Completed tasks 区（台账条目带 "(completed at step" 后缀）；
	// 当前任务清单中 completed 条目仍渲染 activeForm（"working on LEDGER_x"），
	// 此处断言 Completed tasks 区不含更早台账条目
	if strings.Contains(content, "[x] LEDGER_ONE (completed at step") || strings.Contains(content, "[x] LEDGER_TWO (completed at step") {
		t.Errorf("ledger limit=1 should NOT contain earlier ledger entries in Completed tasks section, got:\n%s", content)
	}
	if stats.KeptLedger != 1 {
		t.Errorf("stats.KeptLedger = %d, want 1 (capped by KeepTodoLedgerLimit)", stats.KeptLedger)
	}
	if stats.Truncated {
		t.Errorf("stats.Truncated = true, want false (budget sufficient)")
	}
}

// newDegradationStore 构造台账 3 条大 ASCII 记录（每条约 150 token，
// 触发预算降级链）+ 2 条快照（TodoHistory 可用）的 Store。
func newDegradationStore(t *testing.T) *thinklink.Store {
	t.Helper()
	contents := []string{
		"L1 " + strings.Repeat("t", 598),
		"L2 " + strings.Repeat("t", 598),
		"L3 " + strings.Repeat("t", 598),
	}
	return newLedgerTestStore(t, contents)
}

// TestCompressorApplyUltimateBudgetDegradation 预算不足时的降级顺序（5.5 golden）：
//
//	① 超预算先裁可选段落（历史快照轨迹 + legacy T&P 残留），台账全部保留；
//	② 仍超预算逐条递减台账（部分保留/裁光占位行）；
//	③ 极端兜底硬截断（truncated=true）。
func TestCompressorApplyUltimateBudgetDegradation(t *testing.T) {
	// 构造：1 用户输入 + 台账 3 条大记录 + 当前清单 + 历史快照 + legacy T&P 残留
	store := newDegradationStore(t)
	msgs := []llm.Message{
		{Role: llm.RoleSystem, Content: "SYS"},
		{Role: llm.RoleAssistant, Content: "## Thought & Plan\n" + strings.Repeat("L", 4000)},
	}

	t.Run("step1 optional sections trimmed first, ledger untouched", func(t *testing.T) {
		callbackCalls := 0
		c := newCompressor(newCompressorTestEngine(), store, &callbackCalls)
		// 校准值：base(3 ledger)≈2070 token，legacy 段≈1022，history 段≈970，
		// 完整构建≈4062。threshold=2500：完整构建超预算，移除可选段后（≈2070）
		// 台账 3 条可容纳 → 台账全部保留
		out, stats := c.ApplyUltimate(msgs, 2500, nil, UltimateRebuildOptions{
			KeepRecentTodoSnapshots:         1,
			EnableLegacyThoughtPlanFallback: true,
		})
		content := out[1].Content
		if strings.Contains(content, "=== Legacy Thought & Plan fragments ===") || strings.Contains(content, "=== Todo history") {
			t.Errorf("optional sections should be trimmed before ledger, got:\n%s", content)
		}
		for _, sub := range []string{"[x] L1", "[x] L2", "[x] L3"} {
			if !strings.Contains(content, sub) {
				t.Errorf("all ledger entries should be kept after optional trimming, missing %q", sub)
			}
		}
		if stats.Truncated || stats.KeptLedger != 3 {
			t.Errorf("stats invalid: truncated=%v keptLedger=%d, want false/3", stats.Truncated, stats.KeptLedger)
		}
	})

	t.Run("step2 ledger decremented, still under budget", func(t *testing.T) {
		callbackCalls := 0
		c := newCompressor(newCompressorTestEngine(), store, &callbackCalls)
		// 校准值：threshold=1600：移除可选段后（2070）仍超预算 → 台账逐条递减
		// （3→2→1，1 条≈1465 可容纳）→ 部分保留，未裁光
		out, stats := c.ApplyUltimate(msgs, 1600, nil, UltimateRebuildOptions{
			KeepRecentTodoSnapshots:         1,
			EnableLegacyThoughtPlanFallback: true,
		})
		content := out[1].Content
		if stats.Truncated {
			t.Errorf("truncated=%v, want false (ledger decrement should fit first)", stats.Truncated)
		}
		if stats.KeptLedger >= 3 {
			t.Errorf("keptLedger=%d, want <3 (ledger should be decremented)", stats.KeptLedger)
		}
		if !strings.Contains(content, "[x] L3") {
			t.Errorf("decrement should keep the most recent entries, L3 missing")
		}
	})

	t.Run("step2b ledger emptied renders placeholder", func(t *testing.T) {
		callbackCalls := 0
		c := newCompressor(newCompressorTestEngine(), store, &callbackCalls)
		// 校准值：threshold=1200：台账递减到 0（占位行）后 ≈1088 可容纳 →
		// 不触发硬截断
		out, stats := c.ApplyUltimate(msgs, 1200, nil, UltimateRebuildOptions{
			KeepRecentTodoSnapshots:         1,
			EnableLegacyThoughtPlanFallback: true,
		})
		content := out[1].Content
		if stats.Truncated {
			t.Errorf("truncated=%v, want false (ledger omission should fit before hard truncation)", stats.Truncated)
		}
		if stats.KeptLedger != 0 {
			t.Errorf("keptLedger=%d, want 0 (ledger emptied by decrement)", stats.KeptLedger)
		}
		if !strings.Contains(content, "(completed tasks omitted)") {
			t.Errorf("emptied ledger should render '(completed tasks omitted)' placeholder, got:\n%s", content)
		}
	})

	t.Run("step3 hard truncation fallback", func(t *testing.T) {
		callbackCalls := 0
		c := newCompressor(newCompressorTestEngine(), store, &callbackCalls)
		// 校准值：threshold=800：可选段落裁光 + 台账裁光后（≈1088）仍超预算 →
		// 硬截断兜底
		out, stats := c.ApplyUltimate(msgs, 800, nil, UltimateRebuildOptions{
			KeepRecentTodoSnapshots:         1,
			EnableLegacyThoughtPlanFallback: true,
		})
		if stats.Truncated != true {
			t.Errorf("truncated=%v, want true (all fallbacks exhausted, hard truncate)", stats.Truncated)
		}
		if strings.TrimSpace(out[1].Content) == "" {
			t.Errorf("truncated rebuilt content should not be empty")
		}
	})
}

// TestCompressorApplyUltimateLegacyFallback legacy 静默兜底（5.10-f）：
// flag=true 时重建中追加消息历史残留的旧格式 T&P 块为可选段落（不提示、不依赖）；
// flag=false 时完全忽略。
func TestCompressorApplyUltimateLegacyFallback(t *testing.T) {
	callbackCalls := 0
	c, _, mem, msgs := newUltimateFixtures(t, &callbackCalls)
	msgs = append(msgs, llm.Message{Role: llm.RoleAssistant, Content: "## Thought & Plan\nlegacy plan fragment"})

	// flag 开：含残留段
	out, _ := c.ApplyUltimate(msgs, 100000, mem, UltimateRebuildOptions{EnableLegacyThoughtPlanFallback: true})
	if !strings.Contains(out[1].Content, "=== Legacy Thought & Plan fragments ===") {
		t.Errorf("legacy fallback enabled should contain legacy fragments section, got:\n%s", out[1].Content)
	}
	if !strings.Contains(out[1].Content, "legacy plan fragment") {
		t.Errorf("legacy fallback enabled should contain the residual block")
	}

	// flag 关：完全忽略
	mem2 := memory.NewConversationMemory(10)
	out2, _ := c.ApplyUltimate(msgs, 100000, mem2, UltimateRebuildOptions{})
	if strings.Contains(out2[1].Content, "=== Legacy Thought & Plan fragments ===") {
		t.Errorf("legacy fallback disabled should NOT contain legacy fragments section")
	}
	if strings.Contains(out2[1].Content, "legacy plan fragment") {
		t.Errorf("legacy fallback disabled should ignore the residual block")
	}
}

// TestCompressorApplyUltimateTodoHistorySection KeepRecentTodoSnapshots 控制
// 可选历史快照段：>0 时渲染最近 N 条快照进度轨迹；0（默认）不渲染。
func TestCompressorApplyUltimateTodoHistorySection(t *testing.T) {
	callbackCalls := 0
	c, _, mem, msgs := newUltimateFixtures(t, &callbackCalls) // store 已有 2 条快照

	out, _ := c.ApplyUltimate(msgs, 100000, mem, UltimateRebuildOptions{KeepRecentTodoSnapshots: 1})
	if !strings.Contains(out[1].Content, "=== Todo history (recent progress snapshots) ===") {
		t.Errorf("KeepRecentTodoSnapshots>0 should render todo history section, got:\n%s", out[1].Content)
	}
	if !strings.Contains(out[1].Content, "[IN PROGRESS] working on task B") {
		t.Errorf("todo history section should contain the latest snapshot items")
	}

	mem2 := memory.NewConversationMemory(10)
	out2, _ := c.ApplyUltimate(msgs, 100000, mem2, UltimateRebuildOptions{})
	if strings.Contains(out2[1].Content, "=== Todo history") {
		t.Errorf("default (KeepRecentTodoSnapshots=0) should NOT render todo history section")
	}
}

// TestCompressorApplyUltimateNilMemory mem 为 nil 时跳过 memory 操作，不 panic，
// 回调仍执行。
func TestCompressorApplyUltimateNilMemory(t *testing.T) {
	callbackCalls := 0
	c, _, _, msgs := newUltimateFixtures(t, &callbackCalls)

	out, stats := c.ApplyUltimate(msgs, 100000, nil, UltimateRebuildOptions{})

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

// TestCompressorApplyUltimateNegativeBudgetClamped threshold 小于 system 消息
// 自身 token 时 userBudget 钳位为 0（不产生负预算），硬截断兜底。
func TestCompressorApplyUltimateNegativeBudgetClamped(t *testing.T) {
	callbackCalls := 0
	c, _, mem, msgs := newUltimateFixtures(t, &callbackCalls)

	// threshold=1 而 system 消息 token 已 > 1 → userBudget = 0（钳位）
	out, stats := c.ApplyUltimate(msgs, 1, mem, UltimateRebuildOptions{})

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

// ─── 组 3:阈值边界 ──────────────────────────────────────────────────────────

// TestCompressorShouldCompress 触发判断辅助：严格大于语义（等于阈值不触发）。
func TestCompressorShouldCompress(t *testing.T) {
	c := NewContextCompressor(nil, "Director", thinklink.NewStore(200), nil)

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
