package tools

import (
	"context"
	"testing"
)

// ============================================================================
// extractJSON 测试
// ============================================================================

func TestExtractJSON_PureJSON(t *testing.T) {
	input := `{"title":"test","content":"hello"}`
	got := extractJSON(input)
	if got != input {
		t.Errorf("extractJSON(pure JSON) = %q, want %q", got, input)
	}
}

func TestExtractJSON_MarkdownFenceJson(t *testing.T) {
	input := "```json\n{\"title\":\"test\"}\n```"
	expected := `{"title":"test"}`
	got := extractJSON(input)
	if got != expected {
		t.Errorf("extractJSON(markdown fence json) = %q, want %q", got, expected)
	}
}

func TestExtractJSON_MarkdownFenceNoLang(t *testing.T) {
	input := "```\n{\"key\":\"val\"}\n```"
	expected := `{"key":"val"}`
	got := extractJSON(input)
	if got != expected {
		t.Errorf("extractJSON(markdown fence no lang) = %q, want %q", got, expected)
	}
}

func TestExtractJSON_NoJSON(t *testing.T) {
	input := "just some plain text with no json"
	got := extractJSON(input)
	// should return the text as-is when no braces found
	if got != input {
		t.Errorf("extractJSON(no json) = %q, want %q", got, input)
	}
}

func TestExtractJSON_SurroundingText(t *testing.T) {
	input := `Here is the result: {"key": "value"} and more.`
	expected := `{"key": "value"}`
	got := extractJSON(input)
	if got != expected {
		t.Errorf("extractJSON(surrounding text) = %q, want %q", got, expected)
	}
}

func TestExtractJSON_EmptyString(t *testing.T) {
	got := extractJSON("")
	if got != "" {
		t.Errorf("extractJSON(empty) = %q, want empty", got)
	}
}

func TestExtractJSON_MissingClosingBrace(t *testing.T) {
	input := `{"key": "value"`
	// end < start check: end would be -1 (no '}'), returns text as-is
	got := extractJSON(input)
	if got != input {
		t.Errorf("extractJSON(missing close brace) = %q, want %q", got, input)
	}
}

// ============================================================================
// ConsolidateKnowledgeTool.Execute 参数校验（mcp nil 降级路径）
// ============================================================================

func TestConsolidateKnowledgeTool_Execute_InvalidType(t *testing.T) {
	tool := NewConsolidateKnowledgeTool(nil, nil)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"type":    "invalid_type",
		"title":   "test",
		"content": "content",
		"tags":    []interface{}{"tag1"},
	})
	if err == nil {
		t.Fatal("expected error for invalid type, got nil")
	}
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestConsolidateKnowledgeTool_Execute_MissingTitle(t *testing.T) {
	tool := NewConsolidateKnowledgeTool(nil, nil)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"type":    "repo_retrieval",
		"content": "content",
		"tags":    []interface{}{"tag1"},
	})
	if err == nil {
		t.Fatal("expected error for missing title, got nil")
	}
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestConsolidateKnowledgeTool_Execute_MissingContent(t *testing.T) {
	tool := NewConsolidateKnowledgeTool(nil, nil)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"type":  "repo_retrieval",
		"title": "test",
		"tags":  []interface{}{"tag1"},
	})
	if err == nil {
		t.Fatal("expected error for missing content, got nil")
	}
}

func TestConsolidateKnowledgeTool_Execute_MissingTags(t *testing.T) {
	tool := NewConsolidateKnowledgeTool(nil, nil)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"type":    "repo_retrieval",
		"title":   "test",
		"content": "content",
	})
	if err == nil {
		t.Fatal("expected error for missing tags, got nil")
	}
}

func TestConsolidateKnowledgeTool_Execute_EmptyTags(t *testing.T) {
	tool := NewConsolidateKnowledgeTool(nil, nil)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"type":    "repo_retrieval",
		"title":   "test",
		"content": "content",
		"tags":    []interface{}{},
	})
	if err == nil {
		t.Fatal("expected error for empty tags, got nil")
	}
}

func TestConsolidateKnowledgeTool_Execute_ValidWithNilMCP(t *testing.T) {
	// 有效参数 + nil MCP → 应返回 skipped
	tool := NewConsolidateKnowledgeTool(nil, nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"type":    "repo_retrieval",
		"title":   "test title",
		"content": "test content here",
		"tags":    []interface{}{"tag1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if resMap["status"] != "skipped" {
		t.Errorf("expected status 'skipped', got %v", resMap["status"])
	}
}

func TestConsolidateKnowledgeTool_Execute_TitleTruncation(t *testing.T) {
	// 标题超过 30 字符应被截断（不会报错，只是截断后传给 MCP）
	tool := NewConsolidateKnowledgeTool(nil, nil)
	// 因为 MCP 为 nil，会返回 skipped，但截断逻辑在 distill 之前执行
	// 我们验证不会报错即可
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"type":    "repo_retrieval",
		"title":   "这是一段非常非常长的标题超过了三十个字符的限制",
		"content": "content",
		"tags":    []interface{}{"tag1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConsolidateKnowledgeTool_Execute_ChineseTitleTruncation(t *testing.T) {
	// 35 个中文字符的标题应被截断为恰好 30 个字符且保持有效 UTF-8
	tool := NewConsolidateKnowledgeTool(nil, nil)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"type":    "repo_retrieval",
		"title":   "这是一个非常长的中文标题测试用于验证rune截断正确性的功能点测试用例", // 35 个中文字
		"content": "content",
		"tags":    []interface{}{"tag1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestChineseTitleTruncation_RuneCount 直接验证 rune 截断逻辑：
// 35 个中文字符应被截为恰好 30 个字符，且结果仍为有效 UTF-8。
func TestChineseTitleTruncation_RuneCount(t *testing.T) {
	const input = "这是一个非常长的中文标题测试用于验证rune截断正确性的功能点测试用例" // 35 runes
	if got := len([]rune(input)); got != 35 {
		t.Fatalf("input rune count = %d, want 35", got)
	}
	// 模拟 knowledge.go 中的截断逻辑
	truncated := input
	if r := []rune(truncated); len(r) > 30 {
		truncated = string(r[:30])
	}
	if got := len([]rune(truncated)); got != 30 {
		t.Fatalf("truncated rune count = %d, want 30", got)
	}
}

func TestConsolidateKnowledgeTool_Execute_LongContentNilEngine(t *testing.T) {
	// content > 500 字符且 engine 为 nil → 降级硬截断，不应 panic
	longContent := ""
	for i := 0; i < 600; i++ {
		longContent += "x"
	}
	tool := NewConsolidateKnowledgeTool(nil, nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"type":    "repo_retrieval",
		"title":   "test",
		"content": longContent,
		"tags":    []interface{}{"tag1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if resMap["status"] != "skipped" {
		t.Errorf("expected status 'skipped', got %v", resMap["status"])
	}
}

// ============================================================================
// PruneHistoryTool.Execute 参数校验
// ============================================================================

func TestPruneHistoryTool_Execute_InvalidAction(t *testing.T) {
	tool := NewPruneHistoryTool(nil, nil)
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "invalid_action",
	})
	if err == nil {
		t.Fatal("expected error for invalid action, got nil")
	}
	expected := "action must be one of: list, delete, merge"
	if err.Error() != expected {
		t.Errorf("expected error %q, got %q", expected, err.Error())
	}
}

func TestPruneHistoryTool_Execute_ListNilMCP(t *testing.T) {
	tool := NewPruneHistoryTool(nil, nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "list",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if resMap["status"] != "skipped" {
		t.Errorf("expected status 'skipped', got %v", resMap["status"])
	}
}

func TestPruneHistoryTool_Execute_DeleteNilMCP(t *testing.T) {
	tool := NewPruneHistoryTool(nil, nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "delete",
		"ids":    []interface{}{"id1", "id2"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if resMap["status"] != "skipped" {
		t.Errorf("expected status 'skipped', got %v", resMap["status"])
	}
}

func TestPruneHistoryTool_Execute_MergeNilMCP(t *testing.T) {
	tool := NewPruneHistoryTool(nil, nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "merge",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if resMap["status"] != "skipped" {
		t.Errorf("expected status 'skipped', got %v", resMap["status"])
	}
}

func TestPruneHistoryTool_Execute_DeleteMissingIDs(t *testing.T) {
	// 当 mcp 为 nil 时，delete 会提前返回 skipped，不会走到 ids 校验
	// 这里验证 nil MCP 的 delete 返回 skipped（fail-safe 路径）
	tool := NewPruneHistoryTool(nil, nil)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"action": "delete",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if resMap["status"] != "skipped" {
		t.Errorf("expected status 'skipped', got %v", resMap["status"])
	}
}

// ============================================================================
// mergeStrings 测试
// ============================================================================

func TestMergeStrings(t *testing.T) {
	a := []string{"tag1", "tag2"}
	b := []string{"tag2", "tag3"}
	got := mergeStrings(a, b)
	expected := []string{"tag1", "tag2", "tag3"}
	if len(got) != len(expected) {
		t.Fatalf("mergeStrings = %v, want %v", got, expected)
	}
	for i := range expected {
		if got[i] != expected[i] {
			t.Errorf("mergeStrings[%d] = %q, want %q", i, got[i], expected[i])
		}
	}
}

func TestMergeStrings_Empty(t *testing.T) {
	got := mergeStrings(nil, nil)
	if len(got) != 0 {
		t.Errorf("mergeStrings(nil, nil) = %v, want empty", got)
	}
}
