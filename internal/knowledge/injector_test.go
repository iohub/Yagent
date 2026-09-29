package knowledge

import (
	"context"
	"fmt"
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
// 知识注入概要事件测试（buildInjectedSummary / publishInjectedSummary）
// ============================================================================

// fakeEventPublisher 记录发布事件的 stub publisher（实现 EventPublisher 最小接口）
type fakeEventPublisher struct {
	events  []fakePublishedEvent
	failErr error // 非 nil 时 Publish 返回该错误
}

type fakePublishedEvent struct {
	eventType string
	content   interface{}
	from      string
}

func (f *fakeEventPublisher) Publish(eventType string, content interface{}, from string) error {
	f.events = append(f.events, fakePublishedEvent{eventType: eventType, content: content, from: from})
	return f.failErr
}

func TestBuildInjectedSummary_WithHits(t *testing.T) {
	hi := 0.92
	results := []mcp.KnowledgeSearchResult{
		{KnowledgeRecord: mcp.KnowledgeRecord{Title: "Auth Flow"}, FinalScore: 0.95, RerankScore: &hi},
		{KnowledgeRecord: mcp.KnowledgeRecord{Title: "Session Mgmt"}, FinalScore: 0.88},
		{KnowledgeRecord: mcp.KnowledgeRecord{Title: "Token Refresh"}, FinalScore: 0.72},
	}
	summary := buildInjectedSummary("coding", "fix login bug", results)

	if summary["agent"] != "coding" {
		t.Errorf("agent = %v, want coding", summary["agent"])
	}
	if summary["query"] != "fix login bug" {
		t.Errorf("query = %v, want fix login bug", summary["query"])
	}
	if hits, ok := summary["hits"].(int); !ok || hits != 3 {
		t.Errorf("hits = %v (%T), want int 3", summary["hits"], summary["hits"])
	}
	// max_score 取 RerankScore 优先（0.92），而非 FinalScore 的最大值 0.95
	if ms, ok := summary["max_score"].(float64); !ok || ms != 0.92 {
		t.Errorf("max_score = %v (%T), want 0.92 (rerank priority)", summary["max_score"], summary["max_score"])
	}
	titles, ok := summary["titles"].([]string)
	if !ok || len(titles) != 3 {
		t.Fatalf("titles = %v (%T), want []string len 3", summary["titles"], summary["titles"])
	}
	if titles[0] != "Auth Flow" || titles[1] != "Session Mgmt" || titles[2] != "Token Refresh" {
		t.Errorf("titles = %v, want [Auth Flow Session Mgmt Token Refresh]", titles)
	}
}

func TestBuildInjectedSummary_TitlesLimitAndTruncation(t *testing.T) {
	// 7 个结果只取前 5 条标题
	var results []mcp.KnowledgeSearchResult
	for i := 0; i < 7; i++ {
		results = append(results, mcp.KnowledgeSearchResult{
			KnowledgeRecord: mcp.KnowledgeRecord{Title: fmt.Sprintf("Title-%d", i)},
			FinalScore:      0.5,
		})
	}
	summary := buildInjectedSummary("repo", "query", results)
	titles, ok := summary["titles"].([]string)
	if !ok || len(titles) != 5 {
		t.Fatalf("titles len = %d (%T), want 5", len(titles), summary["titles"])
	}
	if titles[0] != "Title-0" || titles[4] != "Title-4" {
		t.Errorf("titles = %v, want first 5 (Title-0..Title-4)", titles)
	}

	// 超长标题截断到 60 rune
	long := ""
	for i := 0; i < 100; i++ {
		long += "标"
	}
	summary2 := buildInjectedSummary("repo", "", []mcp.KnowledgeSearchResult{
		{KnowledgeRecord: mcp.KnowledgeRecord{Title: long}},
	})
	titles2 := summary2["titles"].([]string)
	if len(titles2) != 1 || len([]rune(titles2[0])) != 60 {
		t.Errorf("long title not truncated to 60 runes: got %d runes", len([]rune(titles2[0])))
	}
	// 空 agent 回退为 unknown
	summary3 := buildInjectedSummary("", "query", nil)
	if summary3["agent"] != "unknown" {
		t.Errorf("agent = %v, want unknown", summary3["agent"])
	}
}

func TestBuildInjectedSummary_Empty(t *testing.T) {
	summary := buildInjectedSummary("director", "some query", nil)
	if hits, ok := summary["hits"].(int); !ok || hits != 0 {
		t.Errorf("hits = %v (%T), want int 0", summary["hits"], summary["hits"])
	}
	if ms, ok := summary["max_score"].(float64); !ok || ms != 0 {
		t.Errorf("max_score = %v, want 0", summary["max_score"])
	}
	titles, ok := summary["titles"].([]string)
	if !ok || len(titles) != 0 {
		t.Errorf("titles = %v (%T), want empty []string", summary["titles"], summary["titles"])
	}
}

func TestPublishInjectedSummary_PublishesOnHits(t *testing.T) {
	pub := &fakeEventPublisher{}
	k := NewKnowledgeInjector(nil, config.KnowledgeConfig{Enabled: true}, pub)
	results := []mcp.KnowledgeSearchResult{
		{KnowledgeRecord: mcp.KnowledgeRecord{Title: "Auth Flow"}, FinalScore: 0.95},
	}
	k.publishInjectedSummary("coding", "fix login bug", results)

	if len(pub.events) != 1 {
		t.Fatalf("published %d event(s), want 1", len(pub.events))
	}
	ev := pub.events[0]
	if ev.eventType != "knowledge_injected" {
		t.Errorf("eventType = %q, want knowledge_injected", ev.eventType)
	}
	if ev.from != "knowledge" {
		t.Errorf("from = %q, want knowledge", ev.from)
	}
	contentMap, ok := ev.content.(map[string]interface{})
	if !ok {
		t.Fatalf("content type = %T, want map[string]interface{}", ev.content)
	}
	if hits, ok := contentMap["hits"].(int); !ok || hits != 1 {
		t.Errorf("content hits = %v (%T), want int 1", contentMap["hits"], contentMap["hits"])
	}
}

func TestPublishInjectedSummary_PublishesOnZeroHits(t *testing.T) {
	pub := &fakeEventPublisher{}
	k := NewKnowledgeInjector(nil, config.KnowledgeConfig{Enabled: true}, pub)
	// 命中为 0（或全部被过滤）时也要发布一次，让用户感知检索发生过
	k.publishInjectedSummary("repo", "no match query", nil)

	if len(pub.events) != 1 {
		t.Fatalf("published %d event(s), want 1 (zero-hit must still publish)", len(pub.events))
	}
	contentMap := pub.events[0].content.(map[string]interface{})
	if hits, ok := contentMap["hits"].(int); !ok || hits != 0 {
		t.Errorf("content hits = %v (%T), want int 0", contentMap["hits"], contentMap["hits"])
	}
}

func TestPublishInjectedSummary_NilPublisherNoPanic(t *testing.T) {
	k := NewKnowledgeInjector(nil, config.KnowledgeConfig{Enabled: true}, nil)
	// publisher 为 nil 时应静默跳过，不得 panic
	k.publishInjectedSummary("coding", "query", []mcp.KnowledgeSearchResult{
		{KnowledgeRecord: mcp.KnowledgeRecord{Title: "T"}, FinalScore: 0.9},
	})
	// 零值接收者同样安全（publisher 字段为 nil）
	zero := &KnowledgeInjector{}
	zero.publishInjectedSummary("coding", "query", nil)
}

func TestPublishInjectedSummary_PublishErrorSwallowed(t *testing.T) {
	pub := &fakeEventPublisher{failErr: fmt.Errorf("simulated publish failure")}
	k := NewKnowledgeInjector(nil, config.KnowledgeConfig{Enabled: true}, pub)
	// 发布返回错误时仅吞掉，不得 panic
	k.publishInjectedSummary("coding", "query", []mcp.KnowledgeSearchResult{
		{KnowledgeRecord: mcp.KnowledgeRecord{Title: "T"}, FinalScore: 0.9},
	})
	if len(pub.events) != 1 {
		t.Errorf("event should still be recorded despite returned error")
	}
}

// TestInject_SkipPath_NoEventPublished 验证跳过路径（未启用 / mcpClient 为 nil）不发布事件
func TestInject_SkipPath_NoEventPublished(t *testing.T) {
	// 未启用
	pubDisabled := &fakeEventPublisher{}
	k1 := NewKnowledgeInjector(nil, config.KnowledgeConfig{Enabled: false}, pubDisabled)
	block, err := k1.Inject(context.Background(), InjectionContext{UserMessage: "test"})
	if err != nil || block != "" {
		t.Fatalf("Inject(disabled) = (%q, %v), want (\"\", nil)", block, err)
	}
	if len(pubDisabled.events) != 0 {
		t.Errorf("disabled path published %d event(s), want 0", len(pubDisabled.events))
	}

	// mcpClient 为 nil（Enabled=true）
	pubNoClient := &fakeEventPublisher{}
	k2 := NewKnowledgeInjector(nil, config.KnowledgeConfig{Enabled: true}, pubNoClient)
	block2, err2 := k2.Inject(context.Background(), InjectionContext{UserMessage: "test"})
	if err2 != nil || block2 != "" {
		t.Fatalf("Inject(nil client) = (%q, %v), want (\"\", nil)", block2, err2)
	}
	if len(pubNoClient.events) != 0 {
		t.Errorf("skip path published %d event(s), want 0", len(pubNoClient.events))
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
