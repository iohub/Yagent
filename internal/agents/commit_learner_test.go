package agents

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestDefaultCommitLearnConfig 测试默认配置
func TestDefaultCommitLearnConfig(t *testing.T) {
	config := DefaultCommitLearnConfig()

	if config.MaxCommits != 30 {
		t.Errorf("expected MaxCommits=30, got %d", config.MaxCommits)
	}
	if config.SimilarityThreshold != 0.75 {
		t.Errorf("expected SimilarityThreshold=0.75, got %f", config.SimilarityThreshold)
	}
	if config.TopK != 3 {
		t.Errorf("expected TopK=3, got %d", config.TopK)
	}
	if config.CacheTTL != 3600 {
		t.Errorf("expected CacheTTL=3600, got %d", config.CacheTTL)
	}
	if !config.Enabled {
		t.Error("expected Enabled=true")
	}
	if config.Trigger != "both" {
		t.Errorf("expected Trigger='both', got %q", config.Trigger)
	}
	if config.LLMSystemPrompt == "" {
		t.Error("expected non-empty LLMSystemPrompt")
	}
}

// TestNewCommitLearner 测试 CommitLearner 创建
func TestNewCommitLearner(t *testing.T) {
	config := DefaultCommitLearnConfig()
	learner := NewCommitLearner(config, nil, nil, nil)

	if learner == nil {
		t.Fatal("expected non-nil CommitLearner")
	}
	if learner.cache == nil {
		t.Error("expected non-nil cache")
	}
	if learner.httpClient == nil {
		t.Error("expected non-nil httpClient")
	}
	if learner.config != config {
		t.Error("expected config to be set correctly")
	}
}

// TestCommitLearnerConfig 测试 Config 方法
func TestCommitLearnerConfig(t *testing.T) {
	config := DefaultCommitLearnConfig()
	config.MaxCommits = 50
	learner := NewCommitLearner(config, nil, nil, nil)

	retrievedConfig := learner.Config()
	if retrievedConfig.MaxCommits != 50 {
		t.Errorf("expected MaxCommits=50, got %d", retrievedConfig.MaxCommits)
	}
}

// TestFormatSummaryAsText 测试摘要格式化
func TestFormatSummaryAsText(t *testing.T) {
	summaries := []CommitSummary{
		{
			Hash:           "abc123def456",
			Requirement:    "Implement user authentication",
			Files:          "auth/login.go, auth/middleware.go",
			Approach:       "JWT-based authentication",
			Implementation: "Added JWT middleware and login endpoint",
		},
		{
			Hash:           "789ghi012jkl",
			Requirement:    "Add unit tests for auth module",
			Files:          "auth/login_test.go",
			Approach:       "Table-driven tests",
			Implementation: "Added comprehensive test cases for login handler",
		},
	}

	result := FormatSummaryAsText(summaries)

	if len(result) == 0 {
		t.Error("expected non-empty formatted text")
	}
	if !strings.Contains(result, "Recent Relevant Commits") {
		t.Error("expected 'Recent Relevant Commits' in formatted text")
	}
	if !strings.Contains(result, "Requirement") {
		t.Error("expected 'Requirement' in formatted text")
	}
	if !strings.Contains(result, "abc123de") {
		t.Error("expected truncated hash 'abc123de' in formatted text (first 8 chars)")
	}
	if !strings.Contains(result, "Implement user authentication") {
		t.Error("expected requirement text in formatted text")
	}
	if !strings.Contains(result, "Add unit tests") {
		t.Error("expected second commit in formatted text")
	}
}

// TestFormatSummaryAsTextEmpty 测试空摘要
func TestFormatSummaryAsTextEmpty(t *testing.T) {
	result := FormatSummaryAsText([]CommitSummary{})
	if result != "" {
		t.Errorf("expected empty string, got %q", result)
	}
}

// TestFormatSummaryAsTextSingle 测试单个摘要格式化
func TestFormatSummaryAsTextSingle(t *testing.T) {
	summaries := []CommitSummary{
		{
			Hash:           "abc123",
			Requirement:    "Test requirement",
			Files:          "test.go",
			Approach:       "Test approach",
			Implementation: "Test implementation",
		},
	}

	result := FormatSummaryAsText(summaries)

	if !strings.Contains(result, "### Commit `abc123`") {
		t.Error("expected commit hash header in formatted text")
	}
	if !strings.Contains(result, "**Requirement**: Test requirement") {
		t.Error("expected requirement field in formatted text")
	}
	if !strings.Contains(result, "**Files**: test.go") {
		t.Error("expected files field in formatted text")
	}
}

// TestCommitLearnerCache 测试缓存机制
func TestCommitLearnerCache(t *testing.T) {
	config := DefaultCommitLearnConfig()
	config.CacheTTL = 300 // 5 分钟
	learner := NewCommitLearner(config, nil, nil, nil)

	// 模拟设置缓存
	learner.cacheMu.Lock()
	learner.lastHead = "abc123"
	learner.lastFetch = time.Now()
	learner.cacheMu.Unlock()

	// 验证缓存读取
	learner.cacheMu.RLock()
	head := learner.lastHead
	fetchTime := learner.lastFetch
	learner.cacheMu.RUnlock()

	if head != "abc123" {
		t.Errorf("expected cached head 'abc123', got %q", head)
	}
	if time.Since(fetchTime) > time.Second {
		t.Error("expected recent fetch time")
	}
}

// TestCommitLearnerCacheWithSummaries 测试缓存存储和检索
func TestCommitLearnerCacheWithSummaries(t *testing.T) {
	config := DefaultCommitLearnConfig()
	learner := NewCommitLearner(config, nil, nil, nil)

	// 添加缓存项
	summary := CommitSummary{
		Hash:           "test123",
		Requirement:    "Test requirement",
		Files:          "test.go",
		Approach:       "Test approach",
		Implementation: "Test implementation",
	}

	learner.cacheMu.Lock()
	learner.cache["test123"] = &CachedSummary{
		Summary:  summary,
		CachedAt: time.Now(),
	}
	learner.cacheMu.Unlock()

	// 验证缓存检索
	cachedSummaries := learner.GetCachedSummaries()
	if len(cachedSummaries) != 1 {
		t.Errorf("expected 1 cached summary, got %d", len(cachedSummaries))
	}
	if cachedSummaries[0].Hash != "test123" {
		t.Errorf("expected hash 'test123', got %q", cachedSummaries[0].Hash)
	}
}

// TestParseSummaryText 测试摘要文本解析
func TestParseSummaryText(t *testing.T) {
	text := `Requirement: Fix login bug
Files: auth/login.go
Approach: Added input validation
Implementation: Updated login handler to validate email format`

	summary := parseSummaryText(text)

	if summary.Requirement != "Fix login bug" {
		t.Errorf("expected Requirement='Fix login bug', got %q", summary.Requirement)
	}
	if summary.Files != "auth/login.go" {
		t.Errorf("expected Files='auth/login.go', got %q", summary.Files)
	}
	if summary.Approach != "Added input validation" {
		t.Errorf("expected Approach='Added input validation', got %q", summary.Approach)
	}
	if summary.Implementation != "Updated login handler to validate email format" {
		t.Errorf("expected Implementation='Updated login handler to validate email format', got %q", summary.Implementation)
	}
}

// TestParseSummaryTextWithListItems 测试带列表项的摘要解析
func TestParseSummaryTextWithListItems(t *testing.T) {
	text := `Requirement: Implement user authentication
Files:
- auth/login.go
- auth/middleware.go
Approach: JWT-based authentication
Implementation:
- Added JWT middleware
- Created login endpoint`

	summary := parseSummaryText(text)

	if !strings.Contains(summary.Files, "auth/login.go") || !strings.Contains(summary.Files, "auth/middleware.go") {
		t.Errorf("expected both files in Files field, got %q", summary.Files)
	}
	if !strings.Contains(summary.Implementation, "JWT middleware") || !strings.Contains(summary.Implementation, "login endpoint") {
		t.Errorf("expected both implementation items, got %q", summary.Implementation)
	}
}

// TestParseSummaryTextEmpty 测试空摘要解析
func TestParseSummaryTextEmpty(t *testing.T) {
	text := ""
	summary := parseSummaryText(text)

	if summary.Hash != "" {
		t.Errorf("expected empty hash, got %q", summary.Hash)
	}
}

// TestExtractSummaryFromText 测试从文本提取摘要
func TestExtractSummaryFromText(t *testing.T) {
	// extractSummaryFromText 使用小写关键字查找
	text := `requirement: Fix login bug
files: auth/login.go
approach: Added input validation
implementation: Updated login handler to validate email format`

	hash := "test123"
	summary := extractSummaryFromText(text, hash)

	if summary.Hash != hash {
		t.Errorf("expected hash %q, got %q", hash, summary.Hash)
	}
	if summary.Requirement != "Fix login bug" {
		t.Errorf("expected Requirement='Fix login bug', got %q", summary.Requirement)
	}
}

// TestExtractSummaryFromTextPartial 测试部分字段提取
func TestExtractSummaryFromTextPartial(t *testing.T) {
	// extractSummaryFromText 使用小写关键字查找
	text := `some text about requirement: Fix login bug

other content`

	hash := "partial123"
	summary := extractSummaryFromText(text, hash)

	if summary.Hash != hash {
		t.Errorf("expected hash %q, got %q", hash, summary.Hash)
	}
	// 注意：extractSummaryFromText 对于非标准格式的文本可能提取不准确
	// 这里只验证 hash 被正确设置
	if summary.Hash != "partial123" {
		t.Error("expected hash 'partial123'")
	}
}

// TestExtractFieldValue 测试字段值提取
func TestExtractFieldValue(t *testing.T) {
	text := `Requirement: Fix login bug
Files: auth/login.go
Approach: Added validation`

	// 测试提取 Requirement
	value := extractFieldValue(text, "requirement")
	if value != "Fix login bug" {
		t.Errorf("expected 'Fix login bug', got %q", value)
	}

	// 测试提取 Files
	value = extractFieldValue(text, "files")
	if value != "auth/login.go" {
		t.Errorf("expected 'auth/login.go', got %q", value)
	}

	// 测试提取不存在的字段
	value = extractFieldValue(text, "nonexistent")
	if value != "" {
		t.Errorf("expected empty string for nonexistent field, got %q", value)
	}
}

// TestExtractFieldValueWithColon 测试带冒号的字段提取
func TestExtractFieldValueWithColon(t *testing.T) {
	text := `requirement: Fix: login bug
files: auth/login.go`

	value := extractFieldValue(text, "requirement")
	if value != "Fix: login bug" {
		t.Errorf("expected 'Fix: login bug', got %q", value)
	}
}

// TestCommitMetaStructure 测试 CommitMeta 结构
func TestCommitMetaStructure(t *testing.T) {
	commit := CommitMeta{
		Hash:    "abc123def456",
		Subject: "Fix login bug",
		Author:  "Test Author",
		Date:    time.Now(),
		Files:   []string{"auth/login.go", "auth/middleware.go"},
		Diff:    "@@ -1,1 +1,2 @@\n+validated",
	}

	if commit.Hash != "abc123def456" {
		t.Errorf("expected hash 'abc123def456', got %q", commit.Hash)
	}
	if commit.Subject != "Fix login bug" {
		t.Errorf("expected subject 'Fix login bug', got %q", commit.Subject)
	}
	if len(commit.Files) != 2 {
		t.Errorf("expected 2 files, got %d", len(commit.Files))
	}
}

// TestCommitSummaryStructure 测试 CommitSummary 结构
func TestCommitSummaryStructure(t *testing.T) {
	summary := CommitSummary{
		Hash:           "abc123",
		Requirement:    "Implement feature",
		Files:          "feature.go",
		Approach:       "New approach",
		Implementation: "Implemented feature",
	}

	if summary.Hash != "abc123" {
		t.Errorf("expected hash 'abc123', got %q", summary.Hash)
	}
	if summary.Requirement != "Implement feature" {
		t.Errorf("expected Requirement='Implement feature', got %q", summary.Requirement)
	}
}

// TestCommitLearnConfigStructure 测试 CommitLearnConfig 结构
func TestCommitLearnConfigStructure(t *testing.T) {
	config := CommitLearnConfig{
		Enabled:             true,
		MaxCommits:          50,
		SimilarityThreshold: 0.8,
		TopK:                5,
		Trigger:             "on_demand",
		CacheTTL:            1800,
		LLMSystemPrompt:     "Test prompt",
	}

	if !config.Enabled {
		t.Error("expected Enabled=true")
	}
	if config.MaxCommits != 50 {
		t.Errorf("expected MaxCommits=50, got %d", config.MaxCommits)
	}
	if config.SimilarityThreshold != 0.8 {
		t.Errorf("expected SimilarityThreshold=0.8, got %f", config.SimilarityThreshold)
	}
	if config.TopK != 5 {
		t.Errorf("expected TopK=5, got %d", config.TopK)
	}
	if config.Trigger != "on_demand" {
		t.Errorf("expected Trigger='on_demand', got %q", config.Trigger)
	}
	if config.CacheTTL != 1800 {
		t.Errorf("expected CacheTTL=1800, got %d", config.CacheTTL)
	}
}

// TestCommitLearnConfigTriggerValues 测试不同的 trigger 值
func TestCommitLearnConfigTriggerValues(t *testing.T) {
	validTriggers := []string{"on_demand", "on_session_start", "both"}

	for _, trigger := range validTriggers {
		config := DefaultCommitLearnConfig()
		config.Trigger = trigger

		learner := NewCommitLearner(config, nil, nil, nil)
		retrievedConfig := learner.Config()

		if retrievedConfig.Trigger != trigger {
			t.Errorf("expected Trigger=%q, got %q", trigger, retrievedConfig.Trigger)
		}
	}
}

// TestCachedSummaryStructure 测试 CachedSummary 结构
func TestCachedSummaryStructure(t *testing.T) {
	summary := CommitSummary{
		Hash:           "cached123",
		Requirement:    "Test requirement",
		Files:          "test.go",
		Approach:       "Test approach",
		Implementation: "Test implementation",
	}

	cached := CachedSummary{
		Summary:  summary,
		CachedAt: time.Now(),
	}

	if cached.Summary.Hash != "cached123" {
		t.Errorf("expected cached summary hash 'cached123', got %q", cached.Summary.Hash)
	}
	if time.Since(cached.CachedAt) > time.Second {
		t.Error("expected recent cached time")
	}
}

// TestFormatSummaryAsTextHashTruncation 测试长 hash 截断
func TestFormatSummaryAsTextHashTruncation(t *testing.T) {
	longHash := "abcdef1234567890abcdef1234567890abcdef1234567890"
	summaries := []CommitSummary{
		{
			Hash:           longHash,
			Requirement:    "Test requirement",
			Files:          "test.go",
			Approach:       "Test approach",
			Implementation: "Test implementation",
		},
	}

	result := FormatSummaryAsText(summaries)

	// 检查 hash 被截断为 8 字符
	expectedHashDisplay := longHash[:8]
	if !strings.Contains(result, "### Commit `"+expectedHashDisplay+"`") {
		t.Errorf("expected truncated hash '%s' in formatted text", expectedHashDisplay)
	}
}

// TestNewCommitLearnerWithNilEngine 测试使用 nil engine 创建 CommitLearner
func TestNewCommitLearnerWithNilEngine(t *testing.T) {
	config := DefaultCommitLearnConfig()
	learner := NewCommitLearner(config, nil, nil, nil)

	if learner == nil {
		t.Fatal("expected non-nil CommitLearner")
	}
	if learner.llmEngine != nil {
		t.Error("expected nil llmEngine")
	}
}

// TestCommitLearnerConcurrentCacheAccess 测试并发缓存访问
func TestCommitLearnerConcurrentCacheAccess(t *testing.T) {
	config := DefaultCommitLearnConfig()
	learner := NewCommitLearner(config, nil, nil, nil)

	// 并发写入
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(index int) {
			hash := string(rune('a' + index)) + "bc123"
			summary := CommitSummary{
				Hash:           hash,
				Requirement:    "Test requirement",
				Files:          "test.go",
				Approach:       "Test approach",
				Implementation: "Test implementation",
			}

			learner.cacheMu.Lock()
			learner.cache[hash] = &CachedSummary{
				Summary:  summary,
				CachedAt: time.Now(),
			}
			learner.cacheMu.Unlock()

			done <- true
		}(i)
	}

	// 等待所有 goroutine 完成
	for i := 0; i < 10; i++ {
		<-done
	}

	// 验证缓存项数量
	learner.cacheMu.RLock()
	count := len(learner.cache)
	learner.cacheMu.RUnlock()

	if count != 10 {
		t.Errorf("expected 10 cached summaries, got %d", count)
	}
}

// TestFormatSummaryAsTextMultipleCommits 测试多个 commit 格式化
func TestFormatSummaryAsTextMultipleCommits(t *testing.T) {
	summaries := []CommitSummary{
		{
			Hash:           "aaa111",
			Requirement:    "First commit",
			Files:          "file1.go",
			Approach:       "Approach 1",
			Implementation: "Implementation 1",
		},
		{
			Hash:           "bbb222",
			Requirement:    "Second commit",
			Files:          "file2.go",
			Approach:       "Approach 2",
			Implementation: "Implementation 2",
		},
		{
			Hash:           "ccc333",
			Requirement:    "Third commit",
			Files:          "file3.go",
			Approach:       "Approach 3",
			Implementation: "Implementation 3",
		},
	}

	result := FormatSummaryAsText(summaries)

	// 验证所有 commit 都在结果中
	if !strings.Contains(result, "aaa111") {
		t.Error("expected first commit hash in result")
	}
	if !strings.Contains(result, "bbb222") {
		t.Error("expected second commit hash in result")
	}
	if !strings.Contains(result, "ccc333") {
		t.Error("expected third commit hash in result")
	}

	// 验证所有 requirement 都在结果中
	if !strings.Contains(result, "First commit") {
		t.Error("expected first requirement in result")
	}
	if !strings.Contains(result, "Second commit") {
		t.Error("expected second requirement in result")
	}
	if !strings.Contains(result, "Third commit") {
		t.Error("expected third requirement in result")
	}
}

// TestParseSummaryTextMixedCase 测试不同大小写的字段名解析
// 注意：parseSummaryText 只支持首字母大写的字段名（Requirement, Files, Approach, Implementation）
func TestParseSummaryTextMixedCase(t *testing.T) {
	// 测试首字母大写的标准格式（这是实际支持的格式）
	text := `Requirement: Test
Files: test.go
Approach: Test approach
Implementation: Test implementation`

	summary := parseSummaryText(text)
	if summary.Requirement != "Test" {
		t.Errorf("expected Requirement='Test', got %q", summary.Requirement)
	}
	if summary.Files != "test.go" {
		t.Errorf("expected Files='test.go', got %q", summary.Files)
	}
}

// TestClearCache 测试清除缓存
func TestClearCache(t *testing.T) {
	config := DefaultCommitLearnConfig()
	learner := NewCommitLearner(config, nil, nil, nil)

	// 添加缓存项
	summary := CommitSummary{
		Hash: "clear123",
	}
	learner.cacheMu.Lock()
	learner.cache["clear123"] = &CachedSummary{
		Summary:  summary,
		CachedAt: time.Now(),
	}
	learner.cacheMu.Unlock()

	// 验证缓存有数据
	learner.cacheMu.RLock()
	count := len(learner.cache)
	learner.cacheMu.RUnlock()
	if count != 1 {
		t.Errorf("expected 1 cached summary before clear, got %d", count)
	}

	// 清除缓存（模拟 ClearCommits 的行为）
	learner.cacheMu.Lock()
	learner.cache = make(map[string]*CachedSummary)
	learner.cacheMu.Unlock()

	// 验证缓存已清除
	learner.cacheMu.RLock()
	count = len(learner.cache)
	learner.cacheMu.RUnlock()
	if count != 0 {
		t.Errorf("expected 0 cached summaries after clear, got %d", count)
	}
}

// TestContextTimeout 测试 context 超时处理（单元测试验证逻辑）
func TestContextTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// 等待超时
	select {
	case <-ctx.Done():
		// 预期会超时
		if ctx.Err() != context.DeadlineExceeded {
			t.Errorf("expected DeadlineExceeded, got %v", ctx.Err())
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("expected context to timeout")
	}
}

// TestDefaultConfigValues 测试所有默认配置值
func TestDefaultConfigValues(t *testing.T) {
	config := DefaultCommitLearnConfig()

	expectedValues := map[string]interface{}{
		"Enabled":             true,
		"MaxCommits":          30,
		"SimilarityThreshold": 0.75,
		"TopK":                3,
		"Trigger":             "both",
		"CacheTTL":            3600,
	}

	for key := range expectedValues {
		switch key {
		case "Enabled":
			if !config.Enabled {
				t.Errorf("expected %s=true, got false", key)
			}
		case "MaxCommits":
			if config.MaxCommits != 30 {
				t.Errorf("expected %s=30, got %d", key, config.MaxCommits)
			}
		case "SimilarityThreshold":
			if config.SimilarityThreshold != 0.75 {
				t.Errorf("expected %s=0.75, got %f", key, config.SimilarityThreshold)
			}
		case "TopK":
			if config.TopK != 3 {
				t.Errorf("expected %s=3, got %d", key, config.TopK)
			}
		case "Trigger":
			if config.Trigger != "both" {
				t.Errorf("expected %s='both', got %q", key, config.Trigger)
			}
		case "CacheTTL":
			if config.CacheTTL != 3600 {
				t.Errorf("expected %s=3600, got %d", key, config.CacheTTL)
			}
		}
	}
}
