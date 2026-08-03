package knowledge

import (
	"context"
	"testing"

	"codeactor/internal/config"
	"codeactor/internal/mcp"
)

// ============================================================================
// BuildQuery 测试
// ============================================================================

func TestBuildQuery_Basic(t *testing.T) {
	k := &KnowledgeInjector{}
	injCtx := InjectionContext{
		UserMessage: "fix the login bug in auth.go",
	}
	query := k.BuildQuery(injCtx)
	if query != "fix the login bug in auth.go" {
		t.Errorf("BuildQuery = %q, want %q", query, "fix the login bug in auth.go")
	}
}

func TestBuildQuery_WithTargetFiles(t *testing.T) {
	k := &KnowledgeInjector{}
	injCtx := InjectionContext{
		UserMessage:   "fix the login bug",
		TargetFiles:   []string{"/path/to/auth.go", "/path/to/session.go"},
	}
	query := k.BuildQuery(injCtx)
	// filepath.Base 取文件名
	if query != "fix the login bug 涉及文件：auth.go 涉及文件：session.go" {
		t.Errorf("BuildQuery = %q, want %q", query, "fix the login bug 涉及文件：auth.go 涉及文件：session.go")
	}
}

func TestBuildQuery_EmptyInput(t *testing.T) {
	k := &KnowledgeInjector{}
	injCtx := InjectionContext{}
	query := k.BuildQuery(injCtx)
	if query != "" {
		t.Errorf("BuildQuery(empty) = %q, want empty", query)
	}
}

func TestBuildQuery_NoTruncation(t *testing.T) {
	k := &KnowledgeInjector{}
	// 构造超过 500 rune 的输入
	longMsg := ""
	for i := 0; i < 600; i++ {
		longMsg += "中"
	}
	injCtx := InjectionContext{
		UserMessage: longMsg,
	}
	query := k.BuildQuery(injCtx)
	// 不应截断，应返回完整原始输入
	if len([]rune(query)) != 600 {
		t.Errorf("BuildQuery: got %d runes, want exactly 600 (no truncation)", len([]rune(query)))
	}
	if query != longMsg {
		t.Errorf("BuildQuery: mismatch with original input")
	}
}

// ============================================================================
// FormatKnowledgeBlock 测试
// ============================================================================

func TestFormatKnowledgeBlock_SingleResult(t *testing.T) {
	k := &KnowledgeInjector{}
	results := []mcp.KnowledgeSearchResult{
		{
			KnowledgeRecord: mcp.KnowledgeRecord{
				Type:         "repo_retrieval",
				Title:        "Auth Flow",
				Content:      "The auth flow uses JWT tokens.",
				Tags:         []string{"auth", "jwt"},
				RelatedFiles: []string{"auth.go"},
				Confidence:   0.9,
			},
			FinalScore: 0.95,
		},
	}
	block := k.FormatKnowledgeBlock(results)

	if block == "" {
		t.Fatal("FormatKnowledgeBlock returned empty string")
	}
	// 验证标签
	if !containsStr(block, "<knowledge_context>") {
		t.Error("FormatKnowledgeBlock: missing <knowledge_context> opening tag")
	}
	if !containsStr(block, "</knowledge_context>") {
		t.Error("FormatKnowledgeBlock: missing </knowledge_context> closing tag")
	}
	if !containsStr(block, "[检索]") {
		t.Error("FormatKnowledgeBlock: missing [检索] tag for repo_retrieval")
	}
	if !containsStr(block, "**置信度**: 0.90") {
		t.Error("FormatKnowledgeBlock: missing confidence")
	}
	if !containsStr(block, "**得分**: 0.950") {
		t.Error("FormatKnowledgeBlock: missing score")
	}
	if !containsStr(block, "**相关文件**: auth.go") {
		t.Error("FormatKnowledgeBlock: missing related files")
	}
}

func TestFormatKnowledgeBlock_CodingModification(t *testing.T) {
	k := &KnowledgeInjector{}
	results := []mcp.KnowledgeSearchResult{
		{
			KnowledgeRecord: mcp.KnowledgeRecord{
				Type:      "coding_modification",
				Title:     "Refactor DB",
				Content:   "Changed connection pool size.",
				Confidence: 0.7,
			},
			FinalScore: 0.8,
		},
	}
	block := k.FormatKnowledgeBlock(results)
	if !containsStr(block, "[编码]") {
		t.Error("FormatKnowledgeBlock: missing [编码] tag for coding_modification")
	}
	if containsStr(block, "[检索]") {
		t.Error("FormatKnowledgeBlock: should not have [检索] for coding_modification")
	}
}

func TestFormatKnowledgeBlock_MultipleResults(t *testing.T) {
	k := &KnowledgeInjector{}
	results := []mcp.KnowledgeSearchResult{
		{
			KnowledgeRecord: mcp.KnowledgeRecord{
				Type:         "repo_retrieval",
				Title:        "First",
				Content:      "Content 1",
				Confidence:   0.8,
			},
			FinalScore: 0.9,
		},
		{
			KnowledgeRecord: mcp.KnowledgeRecord{
				Type:         "repo_retrieval",
				Title:        "Second",
				Content:      "Content 2",
				Confidence:   0.6,
			},
			FinalScore: 0.7,
		},
	}
	block := k.FormatKnowledgeBlock(results)
	if !containsStr(block, "First") {
		t.Error("FormatKnowledgeBlock: missing first result title")
	}
	if !containsStr(block, "Second") {
		t.Error("FormatKnowledgeBlock: missing second result title")
	}
}

func TestFormatKnowledgeBlock_NilRerankScore(t *testing.T) {
	k := &KnowledgeInjector{}
	results := []mcp.KnowledgeSearchResult{
		{
			KnowledgeRecord: mcp.KnowledgeRecord{
				Type:       "repo_retrieval",
				Title:      "Test",
				Content:    "Content",
				Confidence: 0.5,
			},
			FinalScore: 0.5,
			// RerankScore is nil
		},
	}
	block := k.FormatKnowledgeBlock(results)
	// 应使用 FinalScore 而非 nil RerankScore
	if !containsStr(block, "**得分**: 0.500") {
		t.Errorf("FormatKnowledgeBlock: expected score 0.500, got block: %s", block)
	}
}

// ============================================================================
// TruncateToTokenBudget 测试
// ============================================================================

func TestTruncateToTokenBudget_NoTruncation(t *testing.T) {
	k := &KnowledgeInjector{}
	text := "short content"
	result := k.TruncateToTokenBudget(text, 1000)
	if result != text {
		t.Errorf("TruncateToTokenBudget(no truncation) = %q, want %q", result, text)
	}
}

func TestTruncateToTokenBudget_WithTruncation(t *testing.T) {
	k := &KnowledgeInjector{}
	// 构造长文本：约 600 字符，预算 200 token（约 400 rune）
	longContent := ""
	for i := 0; i < 600; i++ {
		longContent += "中"
	}
	text := "### Header\n" + longContent
	result := k.TruncateToTokenBudget(text, 200)
	// 应被截断
	if len(result) >= len(text) {
		t.Errorf("TruncateToTokenBudget: expected truncation, got same length")
	}
	// 应保留 </knowledge_context> 闭合标签
	if !containsStr(result, "</knowledge_context>") {
		t.Error("TruncateToTokenBudget: missing closing tag after truncation")
	}
}

func TestTruncateToTokenBudget_EmptyText(t *testing.T) {
	k := &KnowledgeInjector{}
	result := k.TruncateToTokenBudget("", 100)
	if result != "" {
		t.Errorf("TruncateToTokenBudget(empty) = %q, want empty", result)
	}
}

// ============================================================================
// Inject fail-safe 测试
// ============================================================================

func TestInject_NilMCPClient(t *testing.T) {
	k := &KnowledgeInjector{mcpClient: nil}
	injCtx := InjectionContext{UserMessage: "test"}
	block, err := k.Inject(context.Background(), injCtx)
	if err != nil {
		t.Errorf("Inject(nil mcp) returned error: %v", err)
	}
	if block != "" {
		t.Errorf("Inject(nil mcp) = %q, want empty", block)
	}
}

func TestInject_Disabled(t *testing.T) {
	k := &KnowledgeInjector{
		cfg: config.KnowledgeConfig{Enabled: false},
	}
	injCtx := InjectionContext{UserMessage: "test"}
	block, err := k.Inject(context.Background(), injCtx)
	if err != nil {
		t.Errorf("Inject(disabled) returned error: %v", err)
	}
	if block != "" {
		t.Errorf("Inject(disabled) = %q, want empty", block)
	}
}

func TestInject_EmptyUserMessage(t *testing.T) {
	k := &KnowledgeInjector{
		mcpClient: nil, // nil 触发 fail-safe 路径
		cfg:       config.KnowledgeConfig{Enabled: true},
	}
	injCtx := InjectionContext{}
	block, err := k.Inject(context.Background(), injCtx)
	if err != nil {
		t.Errorf("Inject(empty) returned error: %v", err)
	}
	if block != "" {
		t.Errorf("Inject(empty) = %q, want empty", block)
	}
}

// ============================================================================
// 辅助函数
// ============================================================================

func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
