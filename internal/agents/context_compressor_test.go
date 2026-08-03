package agents

import (
	"strings"
	"testing"

	"codeactor/internal/llm"
)

// TestEstimateMessagesTokens 验证 token 估算函数对各类消息的处理
func TestEstimateMessagesTokens(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system prompt"},
		{Role: llm.RoleUser, Content: "user question"},
		{
			Role:      llm.RoleAssistant,
			Content:   "assistant text",
			ToolCalls: []llm.ToolCall{{Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"foo"}`}}},
		},
		{Role: llm.RoleTool, ToolName: "read_file", Content: "file content here"},
	}
	total := estimateMessagesTokens(messages)
	if total <= 0 {
		t.Fatalf("expected positive token count, got %d", total)
	}
	// 验证 assistant 的 tool_calls arguments 被计入
	t.Logf("total tokens: %d", total)
}

// TestToolTruncationPriority 验证优先级映射
func TestToolTruncationPriority(t *testing.T) {
	tests := []struct {
		name     string
		expected int
	}{
		{"read_file", 0},
		{"run_bash", 0},
		{"create_file", 0},
		{"search_replace_in_file", 0},
		{"deepthinking", -1},
		{"list_dir", 1},
		{"semantic_search", 1},
		{"agent_exit", 1},
		{"unknown_tool", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolTruncationPriority(tt.name); got != tt.expected {
				t.Errorf("toolTruncationPriority(%q) = %d, want %d", tt.name, got, tt.expected)
			}
		})
	}
}

// TestTruncateToolResultsToBudget_NotOverThreshold 未超阈值时原样返回
func TestTruncateToolResultsToBudget_NotOverThreshold(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleAssistant, Content: "call read_file"},
		{Role: llm.RoleTool, ToolName: "read_file", Content: "small result"},
	}
	// 阈值设很大，不会触发
	result := TruncateToolResultsToBudget(messages, 999999, 200)
	if len(result) != len(messages) {
		t.Fatalf("expected %d messages, got %d", len(messages), len(result))
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
	// 构造足够大的消息使总 token 超过阈值
	largeContent := strings.Repeat("x", 50000) // 约 12500+ tokens
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: strings.Repeat("s", 10000)},
		{Role: llm.RoleUser, Content: strings.Repeat("u", 10000)},
		{Role: llm.RoleAssistant, Content: "assistant response"},
		// 优先级 0 工具
		{Role: llm.RoleTool, ToolName: "read_file", Content: largeContent},
		// 优先级 1 工具
		{Role: llm.RoleTool, ToolName: "list_dir", Content: largeContent},
	}
	// 计算基础 token 数（不含大结果），阈值设在"截断 read_file 后"与"截断前"之间
	baseTokens := estimateMessagesTokens([]llm.Message{
		{Role: llm.RoleSystem, Content: strings.Repeat("s", 10000)},
		{Role: llm.RoleUser, Content: strings.Repeat("u", 10000)},
		{Role: llm.RoleAssistant, Content: "assistant response"},
	})
	// 截断 read_file 后大约减少 12300 tokens，截断后总量约 base+200+12500
	// 设阈值为 base + 200 + 12000，确保截断 read_file 后达标，但截断前不达标
	threshold := baseTokens + 200 + 12000

	result := TruncateToolResultsToBudget(messages, threshold, 200)

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
	largeContent := strings.Repeat("x", 50000)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: strings.Repeat("s", 10000)},
		{Role: llm.RoleUser, Content: strings.Repeat("u", 10000)},
		{Role: llm.RoleAssistant, Content: "assistant response"},
		{Role: llm.RoleTool, ToolName: "deepthinking", Content: largeContent},
		{Role: llm.RoleTool, ToolName: "list_dir", Content: largeContent},
	}
	baseTokens := estimateMessagesTokens([]llm.Message{
		{Role: llm.RoleSystem, Content: strings.Repeat("s", 10000)},
		{Role: llm.RoleUser, Content: strings.Repeat("u", 10000)},
		{Role: llm.RoleAssistant, Content: "assistant response"},
	})
	threshold := baseTokens + 300

	result := TruncateToolResultsToBudget(messages, threshold, 200)

	// deepthinking 不应被截断
	dtMsg := result[3]
	if dtMsg.TruncationMarker != nil {
		t.Error("expected deepthinking message NOT to be truncated")
	}
	// list_dir 应被截断（替代 deepthinking）
	listDirMsg := result[4]
	if listDirMsg.TruncationMarker == nil {
		t.Error("expected list_dir message to be truncated (deepthinking is protected)")
	}
}

// TestTruncateToolResultsToBudget_Idempotent 幂等性：对已截断的消息再次调用不重复截断
func TestTruncateToolResultsToBudget_Idempotent(t *testing.T) {
	largeContent := strings.Repeat("x", 50000)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: strings.Repeat("s", 10000)},
		{Role: llm.RoleUser, Content: strings.Repeat("u", 10000)},
		{Role: llm.RoleAssistant, Content: "assistant response"},
		{Role: llm.RoleTool, ToolName: "read_file", Content: largeContent},
	}
	threshold := 500 // 很低，肯定触发
	keepTokens := 50

	// 第一次截断
	result1 := TruncateToolResultsToBudget(messages, threshold, keepTokens)
	dtMsg := result1[3]
	if dtMsg.TruncationMarker == nil {
		t.Fatal("expected first pass to truncate")
	}
	contentAfterFirst := dtMsg.Content
	markerAfterFirst := dtMsg.TruncationMarker.TruncationPass

	// 第二次截断（幂等）
	result2 := TruncateToolResultsToBudget(result1, threshold, keepTokens)
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
	largeContent := strings.Repeat("x", 50000)
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: strings.Repeat("s", 10000)},
		{Role: llm.RoleAssistant, Content: "assistant"},
		{Role: llm.RoleTool, ToolName: "read_file", Content: largeContent},
		{Role: llm.RoleTool, ToolName: "run_bash", Content: largeContent},
		{Role: llm.RoleTool, ToolName: "list_dir", Content: largeContent},
	}
	threshold := 100 // 极低阈值
	keepTokens := 50

	result := TruncateToolResultsToBudget(messages, threshold, keepTokens)

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
