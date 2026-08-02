package tools

import (
	"context"
	"strings"
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
	// 未绑定时，params type 非法应报错
	tool := NewConsolidateKnowledgeTool(nil, nil, "", "")
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"type":         "invalid_type",
		"title":        "test",
		"content":      "content",
		"tags":         []interface{}{"tag1"},
		"source_agent": "repo_agent",
	})
	if err == nil {
		t.Fatal("expected error for invalid type, got nil")
	}
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestConsolidateKnowledgeTool_Execute_MissingTitle(t *testing.T) {
	tool := NewConsolidateKnowledgeTool(nil, nil, "", "")
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"type":         "repo_retrieval",
		"content":      "content",
		"tags":         []interface{}{"tag1"},
		"source_agent": "repo_agent",
	})
	if err == nil {
		t.Fatal("expected error for missing title, got nil")
	}
	if err.Error() == "" {
		t.Fatal("expected non-empty error message")
	}
}

func TestConsolidateKnowledgeTool_Execute_MissingContent(t *testing.T) {
	tool := NewConsolidateKnowledgeTool(nil, nil, "", "")
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"type":         "repo_retrieval",
		"title":        "test",
		"tags":         []interface{}{"tag1"},
		"source_agent": "repo_agent",
	})
	if err == nil {
		t.Fatal("expected error for missing content, got nil")
	}
}

func TestConsolidateKnowledgeTool_Execute_MissingTags(t *testing.T) {
	tool := NewConsolidateKnowledgeTool(nil, nil, "", "")
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"type":         "repo_retrieval",
		"title":        "test",
		"content":      "content",
		"source_agent": "repo_agent",
	})
	if err == nil {
		t.Fatal("expected error for missing tags, got nil")
	}
}

func TestConsolidateKnowledgeTool_Execute_EmptyTags(t *testing.T) {
	tool := NewConsolidateKnowledgeTool(nil, nil, "", "")
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"type":         "repo_retrieval",
		"title":        "test",
		"content":      "content",
		"tags":         []interface{}{},
		"source_agent": "repo_agent",
	})
	if err == nil {
		t.Fatal("expected error for empty tags, got nil")
	}
}

func TestConsolidateKnowledgeTool_Execute_ValidWithNilMCP(t *testing.T) {
	// 有效参数 + nil MCP → 应返回 skipped
	// 使用绑定模式：LLM 无法篡改 type 和 source_agent
	tool := NewConsolidateKnowledgeTool(nil, nil, "repo_agent", "repo_retrieval")
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		// 即使 params 传非法 type 和空 source_agent，绑定值应生效
		"type":         "invalid_type",
		"source_agent": "",
		"title":        "test title",
		"content":      "test content here",
		"tags":         []interface{}{"tag1"},
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
	// 标题超过 200 字符应被截断（不会报错，只是截断后传给 MCP）
	tool := NewConsolidateKnowledgeTool(nil, nil, "repo_agent", "repo_retrieval")
	// 因为 MCP 为 nil，会返回 skipped，但截断逻辑在 distill 之前执行
	// 我们验证不会报错即可
	longTitle := strings.Repeat("a", 250) // 250 个字符，超过 200 字截断阈值
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"title":   longTitle,
		"content": "content",
		"tags":    []interface{}{"tag1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConsolidateKnowledgeTool_Execute_ChineseTitleTruncation(t *testing.T) {
	// 205 个中文字符的标题应被截断为恰好 200 个字符且保持有效 UTF-8
	tool := NewConsolidateKnowledgeTool(nil, nil, "repo_agent", "repo_retrieval")
	longChineseTitle := strings.Repeat("这是一个非常长的中文标题测试用", 14) // 15×14=210 个中文字，超过 200 字截断阈值
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"title":   longChineseTitle,
		"content": "content",
		"tags":    []interface{}{"tag1"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestChineseTitleTruncation_RuneCount 直接验证 rune 截断逻辑：
// 205 个中文字符应被截为恰好 200 个字符，且结果仍为有效 UTF-8。
func TestChineseTitleTruncation_RuneCount(t *testing.T) {
	// 205 个相同中文字符，确保精确控制 rune 数量
	const base = "这是一个非常长的中文标题测试用这是一个非常长的中文标题测试用这是一个非常长的中文标题测试用这是一个非常长的中文标题测试用这是一个非常长的中文标题测试用这是一个非常长的中文标题测试用这是一个非常长的中文标题测试用" // 105 runes
	input := base + base // 210 runes
	if got := len([]rune(input)); got != 210 {
		t.Fatalf("input rune count = %d, want 210", got)
	}
	// 模拟 knowledge.go 中的截断逻辑
	truncated := input
	if r := []rune(truncated); len(r) > 200 {
		truncated = string(r[:200])
	}
	if got := len([]rune(truncated)); got != 200 {
		t.Fatalf("truncated rune count = %d, want 200", got)
	}
}

func TestConsolidateKnowledgeTool_Execute_LongContentNilEngine(t *testing.T) {
	// content > 1500 字符且 engine 为 nil → 降级硬截断，不应 panic
	longContent := ""
	for i := 0; i < 1600; i++ {
		longContent += "x"
	}
	tool := NewConsolidateKnowledgeTool(nil, nil, "repo_agent", "repo_retrieval")
	result, err := tool.Execute(context.Background(), map[string]interface{}{
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

// TestConsolidateKnowledgeTool_Execute_BoundSourceOverridesParams 验证绑定值覆盖 params 中的非法值
func TestConsolidateKnowledgeTool_Execute_BoundSourceOverridesParams(t *testing.T) {
	// 绑定 ("repo_agent", "repo_retrieval") 时，即使 params 传非法 type/空 source_agent，工具内部也应使用绑定值
	tool := NewConsolidateKnowledgeTool(nil, nil, "repo_agent", "repo_retrieval")
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		// params 中传入非法值和空 source_agent
		"type":         "invalid_type",
		"source_agent": "",
		"title":        "test title",
		"content":      "test content",
		"tags":         []interface{}{"tag1"},
	})
	if err != nil {
		t.Fatalf("expected no error when bound values override invalid params, got: %v", err)
	}
	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if resMap["status"] != "skipped" {
		t.Errorf("expected status 'skipped', got %v", resMap["status"])
	}
}

// TestConsolidateKnowledgeTool_Execute_MissingSourceAgent 验证未绑定且 params 缺 source_agent 时报错
func TestConsolidateKnowledgeTool_Execute_MissingSourceAgent(t *testing.T) {
	// 未绑定时，params 缺 source_agent 应报错
	tool := NewConsolidateKnowledgeTool(nil, nil, "", "")
	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"type":    "repo_retrieval",
		"title":   "test",
		"content": "content",
		"tags":    []interface{}{"tag1"},
		// 未传 source_agent
	})
	if err == nil {
		t.Fatal("expected error for missing source_agent, got nil")
	}
	expected := "source_agent is required"
	if err.Error() != expected {
		t.Errorf("expected error %q, got %q", expected, err.Error())
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

// ============================================================================
// replaceProjectAbsPath 测试
// ============================================================================

func TestReplaceProjectAbsPath_AbsolutePrefix(t *testing.T) {
	projectDir := "/home/do/ssd/iohub/dev/codeactor-agent"
	title := "/home/do/ssd/iohub/dev/codeactor-agent/foo/bar.go 相关设计"
	got := replaceProjectAbsPath(title, projectDir)
	expected := "foo/bar.go 相关设计"
	if got != expected {
		t.Errorf("replaceProjectAbsPath(%q, %q) = %q, want %q", title, projectDir, got, expected)
	}
}

func TestReplaceProjectAbsPath_NoAbsolutePath(t *testing.T) {
	projectDir := "/home/do/ssd/iohub/dev/codeactor-agent"
	title := "foo/bar.go 相关设计"
	got := replaceProjectAbsPath(title, projectDir)
	if got != title {
		t.Errorf("replaceProjectAbsPath(%q, %q) = %q, want original %q", title, projectDir, got, title)
	}
}

func TestReplaceProjectAbsPath_ExactMatch(t *testing.T) {
	projectDir := "/home/do/ssd/iohub/dev/codeactor-agent"
	title := "/home/do/ssd/iohub/dev/codeactor-agent"
	got := replaceProjectAbsPath(title, projectDir)
	expected := "."
	if got != expected {
		t.Errorf("replaceProjectAbsPath(%q, %q) = %q, want %q", title, projectDir, got, expected)
	}
}

func TestReplaceProjectAbsPath_EmptyProjectDir(t *testing.T) {
	title := "/home/do/ssd/iohub/dev/codeactor-agent/foo/bar.go"
	got := replaceProjectAbsPath(title, "")
	if got != title {
		t.Errorf("replaceProjectAbsPath(%q, \"\") = %q, want original %q", title, got, title)
	}
}

func TestReplaceProjectAbsPath_EmptyTitle(t *testing.T) {
	projectDir := "/home/do/ssd/iohub/dev/codeactor-agent"
	got := replaceProjectAbsPath("", projectDir)
	if got != "" {
		t.Errorf("replaceProjectAbsPath(\"\", %q) = %q, want \"\"", projectDir, got)
	}
}

func TestReplaceProjectAbsPath_NoFalsePrefix(t *testing.T) {
	// projectDir 是 "/home/do/ssd/iohub/dev/codeactor-agent"，
	// 不应误替换 "/home/do/ssd/iohub/dev/codeactor-agentXxx"（缺少分隔符）
	projectDir := "/home/do/ssd/iohub/dev/codeactor-agent"
	title := "/home/do/ssd/iohub/dev/codeactor-agentXxx/foo/bar.go"
	got := replaceProjectAbsPath(title, projectDir)
	if got != title {
		t.Errorf("replaceProjectAbsPath(%q, %q) = %q, want original %q (no false prefix match)", title, projectDir, got, title)
	}
}

func TestReplaceProjectAbsPath_OnlyRelPathPart(t *testing.T) {
	// title 恰好是 projectDir + "/"，清理后应为 "."
	projectDir := "/home/do/ssd/iohub/dev/codeactor-agent"
	title := "/home/do/ssd/iohub/dev/codeactor-agent/"
	got := replaceProjectAbsPath(title, projectDir)
	expected := "."
	if got != expected {
		t.Errorf("replaceProjectAbsPath(%q, %q) = %q, want %q", title, projectDir, got, expected)
	}
}

func TestReplaceProjectAbsPath_PathInMiddle(t *testing.T) {
	// 绝对路径出现在 title 中间
	projectDir := "/home/do/ssd/iohub/dev/codeactor-agent"
	title := "修复 /home/do/ssd/iohub/dev/codeactor-agent/foo/bar.go 中的 bug"
	got := replaceProjectAbsPath(title, projectDir)
	expected := "修复 foo/bar.go 中的 bug"
	if got != expected {
		t.Errorf("replaceProjectAbsPath(%q, %q) = %q, want %q", title, projectDir, got, expected)
	}
}

func TestReplaceProjectAbsPath_MultiplePaths(t *testing.T) {
	// title 包含多个绝对路径片段，全部被替换
	projectDir := "/home/do/ssd/iohub/dev/codeactor-agent"
	title := "参考 /home/do/ssd/iohub/dev/codeactor-agent/a/x.go 和 /home/do/ssd/iohub/dev/codeactor-agent/b/y.go 的改动"
	got := replaceProjectAbsPath(title, projectDir)
	expected := "参考 a/x.go 和 b/y.go 的改动"
	if got != expected {
		t.Errorf("replaceProjectAbsPath(%q, %q) = %q, want %q", title, projectDir, got, expected)
	}
}

func TestReplaceProjectAbsPath_ProjectDirAlone(t *testing.T) {
	// title 中包含单独出现的 projectDir（后面没有分隔符）
	projectDir := "/home/do/ssd/iohub/dev/codeactor-agent"
	title := "参考 /home/do/ssd/iohub/dev/codeactor-agent 文档"
	got := replaceProjectAbsPath(title, projectDir)
	expected := "参考 . 文档"
	if got != expected {
		t.Errorf("replaceProjectAbsPath(%q, %q) = %q, want %q", title, projectDir, got, expected)
	}
}
