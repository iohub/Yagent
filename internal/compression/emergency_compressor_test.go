package compression

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"yagent/internal/llm"
)

// ─── Mock Engine ─────────────────────────────────────────────────────────────

type mockEmergencyLLM struct {
	responses []*llm.Response
	calls     int
	failNext  bool
}

func (m *mockEmergencyLLM) GenerateContent(_ context.Context, _ []llm.Message, _ []llm.ToolDef, _ *llm.CallOptions) (*llm.Response, error) {
	if m.failNext {
		return nil, fmt.Errorf("mock LLM failure")
	}
	if m.calls >= len(m.responses) {
		return &llm.Response{Choices: []llm.Choice{{Content: "fallback summary"}}}, nil
	}
	resp := m.responses[m.calls]
	m.calls++
	return resp, nil
}

func (m *mockEmergencyLLM) Model() string {
	return "mock-emergency"
}

func (m *mockEmergencyLLM) CloseIdleConnections() {}

// ─── TestExtractThoughtAndPlanBlocks ─────────────────────────────────────────

func TestExtractThoughtAndPlanBlocks(t *testing.T) {
	tests := []struct {
		name     string
		content  string
		wantLen  int
		wantSubs []string // each block must contain this substring
	}{
		{
			name:    "no keyword returns nil",
			content: "Just a normal assistant response without any plans.",
			wantLen: 0,
		},
		{
			name: "single Thought & Plan block",
			content: `Here is my thinking:

## Thought & Plan
I will read the file first.

Then I'll write the code.`,
			wantLen:  1,
			wantSubs: []string{"## Thought & Plan"},
		},
		{
			name: "single Thought & Plan block (typo variant)",
			content: `Here is my thinking:

## Thought & Plan
I will read the file first.

Then I'll write the code.`,
			wantLen:  1,
			wantSubs: []string{"## Thought & Plan"},
		},
		{
			name: "multiple blocks mixed case",
			content: `## THOUGHT & PLAN
Step 1: read file

## Thought & Plan
Step 2: write code

## thought & plan
Step 3: test`,
			wantLen:  3,
			wantSubs: []string{"## THOUGHT & PLAN", "## Thought & Plan", "## thought & plan"},
		},
		{
			name:    "empty content",
			content: "",
			wantLen: 0,
		},
		{
			name: "block at end of content",
			content: `Some intro text.

## Thought & Plan
Final plan here.`,
			wantLen:  1,
			wantSubs: []string{"## Thought & Plan"},
		},
		{
			name:     "no whitespace variant",
			content:  "## Thought&Plan\nStep A",
			wantLen:  1,
			wantSubs: []string{"## Thought&Plan"},
		},
		{
			name:     "multiple spaces",
			content:  "## THOUGHT   &   PLAN\nStep B",
			wantLen:  1,
			wantSubs: []string{"## THOUGHT   &   PLAN"},
		},
		{
			name:     "newlines in separator",
			content:  "## Thought\n&\nPlan\nStep C",
			wantLen:  1,
			wantSubs: []string{"## Thought\n&\nPlan"},
		},
		{
			name:     "fullwidth ampersand",
			content:  "## Thought＆Plan\nStep D",
			wantLen:  1,
			wantSubs: []string{"## Thought＆Plan"},
		},
		{
			name:     "html entity amp",
			content:  "## Thought & Plan\nStep E",
			wantLen:  1,
			wantSubs: []string{"## Thought & Plan"},
		},
		{
			name:     "and variant",
			content:  "## Thought and Plan\nStep F",
			wantLen:  1,
			wantSubs: []string{"## Thought and Plan"},
		},
		{
			name: "mixed variant multiple blocks",
			content: `## THOUGHT&PLAN
Step 1

## thought  &  plan
Step 2

## Thought and Plan
Step 3`,
			wantLen:  3,
			wantSubs: []string{"## THOUGHT&PLAN", "## thought  &  plan", "## Thought and Plan"},
		},
		{
			name:    "no separator should not match",
			content: "## Thought Plan\nStep X",
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocks := extractThoughtAndPlanBlocks(tt.content)
			if len(blocks) != tt.wantLen {
				t.Fatalf("expected %d blocks, got %d", tt.wantLen, len(blocks))
			}
			for i, sub := range tt.wantSubs {
				if i >= len(blocks) {
					break
				}
				if !strings.Contains(blocks[i], sub) {
					t.Errorf("block[%d] should contain %q, got:\n%s", i, sub, blocks[i])
				}
			}
		})
	}
}

// ─── TestEmergencyCompressMessages_TodoWritePinning（方案C 5.6）───────────────

// makeTodoWritePair 构造一个 TodoWrite 调用对（assistant tool_call + 紧随 tool result）。
func makeTodoWritePair(callID, result string) []llm.Message {
	return []llm.Message{
		{
			Role: llm.RoleAssistant,
			ToolCalls: []llm.ToolCall{
				{ID: callID, Type: "function", Function: llm.FunctionCall{Name: "TodoWrite", Arguments: `{"todos":[]}`}},
			},
		},
		{Role: llm.RoleTool, ToolCallID: callID, ToolName: "TodoWrite", Content: result},
	}
}

// TestEmergencyCompressMessages_TodoWritePinning 最新 TodoWrite 调用对被钉住
// 原样保留（assistant tool_call + tool result 消息形态不变）；更早的 TodoWrite
// 对被整体替换为紧凑注记；非 TodoWrite 的 assistant 内容按现有压缩规则收集为块。
func TestEmergencyCompressMessages_TodoWritePinning(t *testing.T) {
	oldPair := makeTodoWritePair("old-1", strings.Repeat("old", 2000)) // 旧对（大 tool result）
	latestPair := makeTodoWritePair("new-1", "current todo result")    // 最新对（钉住目标）

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleUser, Content: "task"},
		oldPair[0], oldPair[1],
		{Role: llm.RoleAssistant, Content: "process note one"},
		latestPair[0], latestPair[1],
	}

	mock := &mockEmergencyLLM{}
	threshold := 100000 // 预算充足，不触发强制截断
	newMessages, stats := EmergencyCompressMessages(context.Background(), messages, "task", threshold, mock, "mock-agent", 3)

	// 结构：system + [注记(旧对)] + [最新对原样] + user 重建消息 = 5 条
	if len(newMessages) != 5 {
		t.Fatalf("len(newMessages) = %d, want 5 [system, note, assistant, tool, user], got %v", len(newMessages), newMessages)
	}
	if newMessages[0].Role != llm.RoleSystem {
		t.Errorf("newMessages[0] should be system, got %q", newMessages[0].Role)
	}
	// 旧对被替换为紧凑注记（assistant 文本消息）
	if newMessages[1].Role != llm.RoleAssistant || newMessages[1].Content != legacyTodoWriteNote {
		t.Errorf("old TodoWrite pair should be replaced with compact note, got role=%q content=%q", newMessages[1].Role, newMessages[1].Content)
	}
	// 最新对原样钉住保留（assistant tool_call + tool result 形态与内容不变）
	pinnedAssistant := newMessages[2]
	pinnedTool := newMessages[3]
	if pinnedAssistant.Role != llm.RoleAssistant || len(pinnedAssistant.ToolCalls) != 1 || pinnedAssistant.ToolCalls[0].ID != "new-1" {
		t.Errorf("latest TodoWrite assistant tool_call should be pinned as-is, got %+v", pinnedAssistant)
	}
	if pinnedTool.Role != llm.RoleTool || pinnedTool.Content != "current todo result" || pinnedTool.ToolCallID != "new-1" {
		t.Errorf("latest TodoWrite tool result should be pinned as-is, got role=%q content=%q callID=%q", pinnedTool.Role, pinnedTool.Content, pinnedTool.ToolCallID)
	}
	// user 重建消息在末尾
	if newMessages[4].Role != llm.RoleUser {
		t.Errorf("rebuilt user message should be last, got %q", newMessages[4].Role)
	}
	// 非 TodoWrite 的 assistant 内容进入块收集与重建消息
	if !strings.Contains(newMessages[4].Content, "process note one") {
		t.Errorf("non-TodoWrite assistant content should be kept in rebuilt user message")
	}
	// 旧对的 tool result 大内容不应出现（被注记替换）
	if strings.Contains(newMessages[4].Content, "oldoldold") {
		t.Errorf("old TodoWrite pair tool result should NOT appear in rebuilt content")
	}
	// stats：钉住 1 对、替换 1 对
	if stats.PinnedTodoPairs != 1 || stats.ReplacedTodoPairs != 1 {
		t.Errorf("stats invalid: pinned=%d replaced=%d, want 1/1", stats.PinnedTodoPairs, stats.ReplacedTodoPairs)
	}
}

// TestEmergencyCompressMessages_NoTodoWrite 无 TodoWrite 调用对时输出结构
// 与既有行为一致（[system, user 重建消息]）。
func TestEmergencyCompressMessages_NoTodoWrite(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleAssistant, Content: "normal assistant content"},
	}
	mock := &mockEmergencyLLM{}
	newMessages, stats := EmergencyCompressMessages(context.Background(), messages, "task", 100000, mock, "mock-agent", 3)

	if len(newMessages) != 2 {
		t.Fatalf("len(newMessages) = %d, want 2 [system, user]", len(newMessages))
	}
	if newMessages[1].Role != llm.RoleUser {
		t.Errorf("rebuilt message should be user, got %q", newMessages[1].Role)
	}
	if stats.PinnedTodoPairs != 0 || stats.ReplacedTodoPairs != 0 {
		t.Errorf("stats invalid: pinned=%d replaced=%d, want 0/0", stats.PinnedTodoPairs, stats.ReplacedTodoPairs)
	}
	if !strings.Contains(newMessages[1].Content, "normal assistant content") {
		t.Errorf("non-TodoWrite assistant content should be kept in rebuilt user message")
	}
}

// ─── TestEmergencyCompressMessages_KeepLastN ─────────────────────────────────

func TestEmergencyCompressMessages_KeepLastN(t *testing.T) {
	// 构造 5 个 assistant 块
	blocks := []string{
		"## Thought & Plan\nBlock 1: first iteration",
		"## Thought & Plan\nBlock 2: second iteration",
		"## Thought & Plan\nBlock 3: third iteration",
		"## Thought & Plan\nBlock 4: fourth iteration",
		"## Thought & Plan\nBlock 5: fifth iteration",
	}

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "You are a helpful agent."},
		{Role: llm.RoleUser, Content: "Implement a sorting algorithm."},
		{Role: llm.RoleAssistant, Content: blocks[0]},
		{Role: llm.RoleTool, ToolName: "read_file", Content: strings.Repeat("x", 5000)},
		{Role: llm.RoleAssistant, Content: blocks[1]},
		{Role: llm.RoleTool, ToolName: "read_file", Content: strings.Repeat("y", 5000)},
		{Role: llm.RoleAssistant, Content: blocks[2]},
		{Role: llm.RoleAssistant, Content: blocks[3]},
		{Role: llm.RoleAssistant, Content: blocks[4]},
	}

	mock := &mockEmergencyLLM{
		responses: []*llm.Response{
			{Choices: []llm.Choice{{Content: "执行了文件读取和代码编写，完成了排序算法实现。"}}},
		},
	}

	threshold := 10000 // 合理阈值，避免压缩后强制截断
	newMessages, stats := EmergencyCompressMessages(context.Background(), messages, "Implement a sorting algorithm.", threshold, mock, "mock-agent", 3)

	// 验证返回消息结构
	if len(newMessages) != 2 {
		t.Fatalf("expected 2 messages [system, user], got %d", len(newMessages))
	}
	if newMessages[0].Role != llm.RoleSystem {
		t.Errorf("expected first message role=system, got %s", newMessages[0].Role)
	}
	if newMessages[1].Role != llm.RoleUser {
		t.Errorf("expected second message role=user, got %s", newMessages[1].Role)
	}

	// 验证 stats
	if stats.OriginalTokens <= stats.CompressedTokens {
		t.Errorf("expected OriginalTokens (%d) > CompressedTokens (%d)", stats.OriginalTokens, stats.CompressedTokens)
	}
	if stats.ExtractedBlocks != 5 {
		t.Errorf("expected ExtractedBlocks=5, got %d", stats.ExtractedBlocks)
	}
	if stats.SummarizedBlocks != 2 {
		t.Errorf("expected SummarizedBlocks=2, got %d", stats.SummarizedBlocks)
	}
	if stats.KeptBlocks != 3 {
		t.Errorf("expected KeptBlocks=3, got %d", stats.KeptBlocks)
	}
	if !stats.SummarizedByLLM {
		t.Error("expected SummarizedByLLM=true")
	}

	// 验证 user 内容包含必要部分
	userContent := newMessages[1].Content
	if !strings.Contains(userContent, "Implement a sorting algorithm.") {
		t.Error("user content should contain original task")
	}
	if !strings.Contains(userContent, "执行了文件读取和代码编写，完成了排序算法实现。") {
		t.Error("user content should contain LLM summary")
	}
	// 最后 3 个块应保留
	if !strings.Contains(userContent, "Block 3: third iteration") {
		t.Error("user content should contain kept block 3")
	}
	if !strings.Contains(userContent, "Block 4: fourth iteration") {
		t.Error("user content should contain kept block 4")
	}
	if !strings.Contains(userContent, "Block 5: fifth iteration") {
		t.Error("user content should contain kept block 5")
	}
	// 被总结的块不应出现原始内容
	if strings.Contains(userContent, "Block 1: first iteration") || strings.Contains(userContent, "Block 2: second iteration") {
		t.Error("user content should NOT contain summarized blocks raw content")
	}
}

// ─── TestEmergencyCompressMessages_NoBlocks ─────────────────────────────────

func TestEmergencyCompressMessages_NoBlocks(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleUser, Content: "do something"},
		{Role: llm.RoleAssistant, Content: "I will do it."},
		{Role: llm.RoleTool, ToolName: "run_bash", Content: strings.Repeat("z", 5000)},
	}

	mock := &mockEmergencyLLM{
		responses: []*llm.Response{
			{Choices: []llm.Choice{{Content: "summary"}}},
		},
	}

	threshold := 100
	newMessages, stats := EmergencyCompressMessages(context.Background(), messages, "do something", threshold, mock, "mock-agent", 3)

	if len(newMessages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(newMessages))
	}
	// 无 T&P 块时，assistant 整条内容作为一个保底块，共 1 块 <= 3，不调用 LLM
	if stats.ExtractedBlocks != 1 {
		t.Errorf("expected ExtractedBlocks=1, got %d", stats.ExtractedBlocks)
	}
	// 块数 <= keepLastN，不调用 LLM
	if mock.calls != 0 {
		t.Errorf("expected 0 LLM calls when blocks <= keepLastN, got %d", mock.calls)
	}
}

// ─── TestEmergencyCompressMessages_LLMFailure ───────────────────────────────

func TestEmergencyCompressMessages_LLMFailure(t *testing.T) {
	blocks := []string{
		"## Thought & Plan\nBlock 1",
		"## Thought & Plan\nBlock 2",
		"## Thought & Plan\nBlock 3",
		"## Thought & Plan\nBlock 4",
		"## Thought & Plan\nBlock 5",
	}
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleAssistant, Content: blocks[0]},
		{Role: llm.RoleAssistant, Content: blocks[1]},
		{Role: llm.RoleAssistant, Content: blocks[2]},
		{Role: llm.RoleAssistant, Content: blocks[3]},
		{Role: llm.RoleAssistant, Content: blocks[4]},
	}

	mock := &mockEmergencyLLM{failNext: true}

	threshold := 100
	newMessages, stats := EmergencyCompressMessages(context.Background(), messages, "task", threshold, mock, "mock-agent", 3)

	if len(newMessages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(newMessages))
	}
	if stats.SummarizedByLLM {
		t.Error("expected SummarizedByLLM=false after LLM failure")
	}
	// 降级应使用截断拼接
	userContent := newMessages[1].Content
	if !strings.Contains(userContent, "Block 4") && !strings.Contains(userContent, "Block 5") {
		t.Error("user content should still contain kept blocks after LLM failure fallback")
	}
}

// ─── TestEmergencyCompressMessages_OverBudget ───────────────────────────────

func TestEmergencyCompressMessages_OverBudget(t *testing.T) {
	blocks := []string{
		"## Thought & Plan\nBlock 1",
		"## Thought & Plan\nBlock 2",
		"## Thought & Plan\nBlock 3",
	}
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleAssistant, Content: blocks[0]},
		{Role: llm.RoleAssistant, Content: blocks[1]},
		{Role: llm.RoleAssistant, Content: blocks[2]},
	}

	mock := &mockEmergencyLLM{
		responses: []*llm.Response{
			{Choices: []llm.Choice{{Content: "summary"}}},
		},
	}

	// maxTokens 设得很小，强制触发二次截断
	threshold := 20
	newMessages, stats := EmergencyCompressMessages(context.Background(), messages, "task", threshold, mock, "mock-agent", 3)

	// 压缩后 token 应 <= threshold
	compressedTokens := EstimateMessagesTokens(newMessages)
	if compressedTokens > threshold {
		t.Errorf("expected compressedTokens (%d) <= threshold (%d), got %d", compressedTokens, threshold, compressedTokens)
	}
	if stats.Reason != "forced truncation after emergency compression" {
		t.Errorf("expected Reason='forced truncation after emergency compression', got %q", stats.Reason)
	}
}

// ─── TestEmergencyCompressMessages_FewBlocks ────────────────────────────────

func TestEmergencyCompressMessages_FewBlocks(t *testing.T) {
	blocks := []string{
		"## Thought & Plan\nBlock 1",
		"## Thought & Plan\nBlock 2",
	}
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleUser, Content: "task"},
		{Role: llm.RoleAssistant, Content: blocks[0]},
		{Role: llm.RoleAssistant, Content: blocks[1]},
	}

	mock := &mockEmergencyLLM{
		responses: []*llm.Response{
			{Choices: []llm.Choice{{Content: "summary"}}},
		},
	}

	// keepLastN=3，块数 2 <= 3，不调用 LLM
	threshold := 100
	newMessages, stats := EmergencyCompressMessages(context.Background(), messages, "task", threshold, mock, "mock-agent", 3)

	if mock.calls != 0 {
		t.Errorf("expected 0 LLM calls when blocks <= keepLastN, got %d", mock.calls)
	}
	if stats.KeptBlocks != 2 {
		t.Errorf("expected KeptBlocks=2, got %d", stats.KeptBlocks)
	}
	if stats.SummarizedBlocks != 0 {
		t.Errorf("expected SummarizedBlocks=0, got %d", stats.SummarizedBlocks)
	}

	// 验证两条原始块都保留在 user 消息中
	userContent := newMessages[1].Content
	if !strings.Contains(userContent, "Block 1") {
		t.Error("user content should contain Block 1")
	}
	if !strings.Contains(userContent, "Block 2") {
		t.Error("user content should contain Block 2")
	}
}

// ─── TestEmergencyCompressMessages_OriginalInputFromMessages ─────────────────

func TestEmergencyCompressMessages_OriginalInputFromMessages(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleUser, Content: "Implement quicksort."},
		{Role: llm.RoleAssistant, Content: "## Thought & Plan\nI will implement quicksort."},
	}

	mock := &mockEmergencyLLM{}

	// originalInput 为空，应从 messages 中提取
	newMessages, stats := EmergencyCompressMessages(context.Background(), messages, "", 100, mock, "mock-agent", 3)

	if len(newMessages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(newMessages))
	}
	userContent := newMessages[1].Content
	if !strings.Contains(userContent, "Implement quicksort.") {
		t.Error("user content should contain original task from messages")
	}
	_ = stats
}
