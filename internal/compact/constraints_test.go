package compact

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"codeactor/internal/llm"
)

// ─────────────────────────────────────────────────────────
// MockPromptTemplate 用于测试的提示词模板 mock
// ─────────────────────────────────────────────────────────

type MockPromptTemplate struct {
	constraintPrompt string
}

func (m *MockPromptTemplate) SegmentPrompt() string           { return "" }
func (m *MockPromptTemplate) MergePrompt() string             { return "" }
func (m *MockPromptTemplate) FullCompressPrompt() string      { return "" }
func (m *MockPromptTemplate) ConstraintPrompt() string        { return m.constraintPrompt }

// ─────────────────────────────────────────────────────────
// MockSummarizationClient 用于测试的 LLM 客户端 mock
// ─────────────────────────────────────────────────────────

type MockSummarizationClient struct {
	summary string
	err     error
}

func (m *MockSummarizationClient) GenerateSummary(ctx context.Context, messages []llm.Message) (string, error) {
	if m.err != nil {
		return "", m.err
	}
	return m.summary, nil
}

// ─────────────────────────────────────────────────────────
// Test: RuleBasedConstraintExtractor 基本提取
// ─────────────────────────────────────────────────────────

func TestRuleBasedConstraintExtractor_BasicExtract(t *testing.T) {
	extractor := NewRuleBasedConstraintExtractor()

	messages := []llm.Message{
		{Role: llm.RoleUser, Content: "I want to build a web app using Go and Redis"},
		{Role: llm.RoleAssistant, Content: "That sounds great!"},
		{Role: llm.RoleUser, Content: "It should use PostgreSQL as the database"},
	}

	ctx := context.Background()
	constraints, err := extractor.Extract(ctx, messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if constraints == nil {
		t.Fatal("expected non-nil constraints")
	}

	// 检查是否提取到了技术约束
	if len(constraints.Technical) == 0 {
		t.Error("expected some technical constraints, got none")
	}

	// 检查名称
	if extractor.Name() != "rule_based" {
		t.Errorf("expected name 'rule_based', got '%s'", extractor.Name())
	}

	t.Logf("Extracted technical constraints: %v", constraints.Technical)
}

// ─────────────────────────────────────────────────────────
// Test: 空消息列表返回空约束
// ─────────────────────────────────────────────────────────

func TestRuleBasedConstraintExtractor_EmptyMessages(t *testing.T) {
	extractor := NewRuleBasedConstraintExtractor()

	ctx := context.Background()
	constraints, err := extractor.Extract(ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error on nil messages: %v", err)
	}

	if constraints == nil {
		t.Fatal("expected non-nil constraints")
	}

	if len(constraints.Technical) != 0 || len(constraints.Business) != 0 ||
		len(constraints.Preferences) != 0 || len(constraints.Format) != 0 ||
		len(constraints.Prohibitions) != 0 {
		t.Error("expected all constraint slices to be empty for nil messages")
	}

	// 测试空消息列表
	emptyConstraints, err := extractor.Extract(ctx, []llm.Message{})
	if err != nil {
		t.Fatalf("unexpected error on empty messages: %v", err)
	}

	if len(emptyConstraints.Technical) != 0 {
		t.Error("expected empty constraints for empty message list")
	}
}

// ─────────────────────────────────────────────────────────
// Test: 多种约束类型同时提取
// ─────────────────────────────────────────────────────────

func TestRuleBasedConstraintExtractor_MultipleTypes(t *testing.T) {
	extractor := NewRuleBasedConstraintExtractor()

	messages := []llm.Message{
		{
			Role: llm.RoleUser,
			Content: `Requirements:
- Use Go 1.21 as the programming language
- Must implement REST API endpoints
- Prefer snake_case for naming
- Return as JSON format
- Don't use global variables
- 禁止使用反射`,
		},
	}

	ctx := context.Background()
	constraints, err := extractor.Extract(ctx, messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 验证各类型约束
	if len(constraints.Technical) == 0 {
		t.Error("expected technical constraints (language, api)")
	}

	if len(constraints.Business) == 0 {
		t.Error("expected business constraints (must, implement)")
	}

	if len(constraints.Preferences) == 0 {
		t.Error("expected preference constraints (prefer, naming)")
	}

	if len(constraints.Format) == 0 {
		t.Error("expected format constraints (json format)")
	}

	if len(constraints.Prohibitions) == 0 {
		t.Error("expected prohibition constraints (do not, 禁止)")
	}

	t.Logf("Technical: %v", constraints.Technical)
	t.Logf("Business: %v", constraints.Business)
	t.Logf("Preferences: %v", constraints.Preferences)
	t.Logf("Format: %v", constraints.Format)
	t.Logf("Prohibitions: %v", constraints.Prohibitions)
}

// ─────────────────────────────────────────────────────────
// Test: 去重逻辑
// ─────────────────────────────────────────────────────────

func TestRuleBasedConstraintExtractor_Deduplication(t *testing.T) {
	extractor := NewRuleBasedConstraintExtractor()

	messages := []llm.Message{
		{
			Role: llm.RoleUser,
			Content: `Use Go for the backend. Use Python for the API. Use Go for the CLI.`,
		},
	}

	ctx := context.Background()
	constraints, err := extractor.Extract(ctx, messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 检查是否有重复（"Go" 应该只出现一次）
	seen := make(map[string]int)
	for _, s := range constraints.Technical {
		seen[s]++
	}
	for s, count := range seen {
		if count > 1 {
			t.Errorf("duplicate constraint found: %q appeared %d times", s, count)
		}
	}

	t.Logf("Deduplicated technical constraints: %v", constraints.Technical)
}

// ─────────────────────────────────────────────────────────
// Test: MatchConfidence 置信度计算
// ─────────────────────────────────────────────────────────

func TestRuleBasedConstraintExtractor_MatchConfidence(t *testing.T) {
	extractor := NewRuleBasedConstraintExtractor()

	// 测试空约束的置信度
	emptyConstraints := &Constraints{}
	confidence := extractor.MatchConfidence(emptyConstraints)
	if confidence != 0 {
		t.Errorf("expected confidence 0 for empty constraints, got %f", confidence)
	}

	// 测试有约束的置信度
	messages := []llm.Message{
		{Role: llm.RoleUser, Content: "Use Go and must implement REST API"},
	}

	ctx := context.Background()
	constraints, err := extractor.Extract(ctx, messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	confidence = extractor.MatchConfidence(constraints)
	if confidence < 0 || confidence > 1 {
		t.Errorf("expected confidence in range [0, 1], got %f", confidence)
	}

	// 置信度应该大于 0（因为有匹配）
	if confidence == 0 {
		t.Error("expected non-zero confidence for constrained messages")
	}

	t.Logf("Confidence for constrained messages: %.4f", confidence)
}

// ─────────────────────────────────────────────────────────
// Test: LLMConstraintExtractor parseLLMResponse 解析
// ─────────────────────────────────────────────────────────

func TestLLMConstraintExtractor_ParseLLMResponse(t *testing.T) {
	extractor := NewLLMConstraintExtractor(nil, nil)

	// 测试正常解析
	testCases := []struct {
		name     string
		input    string
		expected map[string][]string
	}{
		{
			name: "分类约束",
			input: `Technical:
- Go 1.21
- PostgreSQL
Business:
- Must implement authentication
Format:
- Return as JSON`,
			expected: map[string][]string{
				"technical": {"Go 1.21", "PostgreSQL"},
				"business":  {"Must implement authentication"},
				"format":    {"Return as JSON"},
			},
		},
		{
			name: "空输入",
			input: "",
			expected: map[string][]string{},
		},
		{
			name: "无约束提示",
			input: "No specific constraints found.",
			expected: map[string][]string{},
		},
		{
			name: "列表格式",
			input: `Technical:
- Prefer Go
Business:
- Must use REST API
Prohibitions:
- Do not use reflection`,
			expected: map[string][]string{
				"technical":     {"Prefer Go"},
				"business":      {"Must use REST API"},
				"prohibition":   {"Do not use reflection"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := extractor.parseLLMResponse(tc.input)

			// 验证技术约束
			for _, expected := range tc.expected["technical"] {
				found := false
				for _, actual := range result.Technical {
					if actual == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected technical constraint %q not found, got %v", expected, result.Technical)
				}
			}

			// 验证业务约束
			for _, expected := range tc.expected["business"] {
				found := false
				for _, actual := range result.Business {
					if actual == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected business constraint %q not found, got %v", expected, result.Business)
				}
			}

			// 验证偏好约束
			for _, expected := range tc.expected["preferences"] {
				found := false
				for _, actual := range result.Preferences {
					if actual == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected preference constraint %q not found, got %v", expected, result.Preferences)
				}
			}

			// 验证格式约束
			for _, expected := range tc.expected["format"] {
				found := false
				for _, actual := range result.Format {
					if actual == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected format constraint %q not found, got %v", expected, result.Format)
				}
			}

			// 验证禁止约束
			for _, expected := range tc.expected["prohibition"] {
				found := false
				for _, actual := range result.Prohibitions {
					if actual == expected {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected prohibition constraint %q not found, got %v", expected, result.Prohibitions)
				}
			}

			t.Logf("Parsed constraints: %+v", result)
		})
	}
}

// ─────────────────────────────────────────────────────────
// Test: LLMConstraintExtractor Name
// ─────────────────────────────────────────────────────────

func TestLLMConstraintExtractor_Name(t *testing.T) {
	extractor := NewLLMConstraintExtractor(nil, nil)
	if extractor.Name() != "llm_based" {
		t.Errorf("expected name 'llm_based', got '%s'", extractor.Name())
	}
}

// ─────────────────────────────────────────────────────────
// Test: LLMConstraintExtractor Extract with nil client
// ─────────────────────────────────────────────────────────

func TestLLMConstraintExtractor_Extract_NilClient(t *testing.T) {
	extractor := NewLLMConstraintExtractor(nil, nil)

	ctx := context.Background()
	_, err := extractor.Extract(ctx, []llm.Message{
		{Role: llm.RoleUser, Content: "Use Go"},
	})

	if err == nil {
		t.Error("expected error when client is nil")
	}

	if err.Error() != "LLM client not available" {
		t.Errorf("expected 'LLM client not available' error, got %q", err.Error())
	}
}

// ─────────────────────────────────────────────────────────
// Test: HybridConstraintExtractor 基本功能
// ─────────────────────────────────────────────────────────

func TestHybridConstraintExtractor_Basic(t *testing.T) {
	ruleExtractor := NewRuleBasedConstraintExtractor()
	// 不传 LLM 客户端，仅测试规则提取部分

	hybrid := NewHybridConstraintExtractor(ruleExtractor, nil, 0.3, false)

	messages := []llm.Message{
		{Role: llm.RoleUser, Content: "Use Go and Redis for caching"},
	}

	ctx := context.Background()
	constraints, err := hybrid.Extract(ctx, messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if constraints == nil {
		t.Fatal("expected non-nil constraints")
	}

	if hybrid.Name() != "hybrid" {
		t.Errorf("expected name 'hybrid', got '%s'", hybrid.Name())
	}

	t.Logf("Hybrid extracted: %+v", constraints)
}

// ─────────────────────────────────────────────────────────
// Test: HybridConstraintExtractor LLM 增强
// ─────────────────────────────────────────────────────────

func TestHybridConstraintExtractor_LLMEnhancement(t *testing.T) {
	ruleExtractor := NewRuleBasedConstraintExtractor()

	// 模拟 LLM 返回更多约束
	mockClient := &MockSummarizationClient{
		summary: `Technical:
- Go 1.21
- PostgreSQL

Business:
- Must support multi-tenant`,
	}
	mockTemplate := &MockPromptTemplate{
		constraintPrompt: "Extract constraints",
	}
	llmExtractor := NewLLMConstraintExtractor(mockClient, mockTemplate)

	hybrid := NewHybridConstraintExtractor(ruleExtractor, llmExtractor, 0.3, true)

	messages := []llm.Message{
		{Role: llm.RoleUser, Content: "Build an app"},
	}

	ctx := context.Background()
	constraints, err := hybrid.Extract(ctx, messages)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 合并后应该包含 LLM 补充的约束
	if len(constraints.Technical) < 1 {
		t.Error("expected at least one technical constraint after LLM enhancement")
	}

	t.Logf("Enhanced constraints: %+v", constraints)
}

// ─────────────────────────────────────────────────────────
// Test: HybridConstraintExtractor 默认阈值
// ─────────────────────────────────────────────────────────

func TestHybridConstraintExtractor_DefaultThreshold(t *testing.T) {
	// 阈值 <= 0 或 > 1 时应使用默认值 0.3
	hybrid := NewHybridConstraintExtractor(
		NewRuleBasedConstraintExtractor(),
		nil,
		-1.0,
		false,
	)
	if hybrid.confidenceThreshold != 0.3 {
		t.Errorf("expected default threshold 0.3 for invalid value, got %f", hybrid.confidenceThreshold)
	}

	hybrid2 := NewHybridConstraintExtractor(
		NewRuleBasedConstraintExtractor(),
		nil,
		1.5,
		false,
	)
	if hybrid2.confidenceThreshold != 0.3 {
		t.Errorf("expected default threshold 0.3 for value > 1, got %f", hybrid2.confidenceThreshold)
	}

	// 有效值应保留
	hybrid3 := NewHybridConstraintExtractor(
		NewRuleBasedConstraintExtractor(),
		nil,
		0.5,
		false,
	)
	if hybrid3.confidenceThreshold != 0.5 {
		t.Errorf("expected threshold 0.5, got %f", hybrid3.confidenceThreshold)
	}
}

// ─────────────────────────────────────────────────────────
// Test: mergeConstraints 合并逻辑
// ─────────────────────────────────────────────────────────

func TestMergeConstraints(t *testing.T) {
	a := &Constraints{
		Technical:    []string{"Go", "Redis"},
		Business:     []string{"must auth"},
		Preferences:  []string{"prefer clean"},
		Format:       []string{"as JSON"},
		Prohibitions: []string{"no global"},
		Source:       map[int]string{0: "msg 0 context"},
	}

	b := &Constraints{
		Technical:    []string{"PostgreSQL", "Go"}, // Go 重复
		Business:     []string{"must deploy"},
		Preferences:  []string{},
		Format:       []string{"as JSON"}, // 重复
		Prohibitions: []string{"no global"}, // 重复
		Source:       map[int]string{1: "msg 1 context"},
	}

	merged := mergeConstraints(a, b)

	// 验证技术约束合并并去重
	if len(merged.Technical) != 3 { // Go, Redis, PostgreSQL
		t.Errorf("expected 3 technical constraints, got %d: %v", len(merged.Technical), merged.Technical)
	}

	// 验证 Source 合并
	if len(merged.Source) != 2 {
		t.Errorf("expected 2 source entries, got %d", len(merged.Source))
	}

	// 测试 nil 处理
	if mergeConstraints(nil, a) != a {
		t.Error("expected mergeConstraints(nil, a) == a")
	}

	if mergeConstraints(a, nil) != a {
		t.Error("expected mergeConstraints(a, nil) == a")
	}
}

// ─────────────────────────────────────────────────────────
// Test: LLMConstraintExtractor Extract with empty user messages
// ─────────────────────────────────────────────────────────

func TestLLMConstraintExtractor_Extract_NoUserMessages(t *testing.T) {
	mockClient := &MockSummarizationClient{
		summary: "Some summary",
	}
	mockTemplate := &MockPromptTemplate{
		constraintPrompt: "Extract constraints",
	}
	extractor := NewLLMConstraintExtractor(mockClient, mockTemplate)

	ctx := context.Background()
	constraints, err := extractor.Extract(ctx, []llm.Message{
		{Role: llm.RoleSystem, Content: "system msg"},
		{Role: llm.RoleAssistant, Content: "assistant msg"},
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if constraints == nil {
		t.Fatal("expected non-nil constraints")
	}

	if len(constraints.Technical) != 0 || len(constraints.Business) != 0 {
		t.Error("expected empty constraints when no user messages")
	}
}

// ─────────────────────────────────────────────────────────
// Test: LLMConstraintExtractor Extract truncates long message list
// ─────────────────────────────────────────────────────────

func TestLLMConstraintExtractor_Extract_TruncateMessages(t *testing.T) {
	mockClient := &MockSummarizationClient{
		summary: "Summary of constraints",
	}
	mockTemplate := &MockPromptTemplate{
		constraintPrompt: "Extract constraints",
	}
	extractor := NewLLMConstraintExtractor(mockClient, mockTemplate)

	// 创建超过 5 条的用户消息
	var messages []llm.Message
	for i := 0; i < 10; i++ {
		messages = append(messages, llm.Message{
			Role:    llm.RoleUser,
			Content: fmt.Sprintf("Message %d: Use Go", i),
		})
	}

	ctx := context.Background()
	_, err := extractor.Extract(ctx, messages)

	// 不检查是否报错，只验证不会 panic
	if err != nil {
		// 因为 mock client 可能返回任何内容，这里只验证不会 panic
		t.Logf("Got error (expected with mock): %v", err)
	}
}

// ─────────────────────────────────────────────────────────
// Test: addPattern 无效模式不崩溃
// ─────────────────────────────────────────────────────────

func TestRuleBasedConstraintExtractor_InvalidPattern(t *testing.T) {
	// 直接测试 addPattern 处理无效正则
	extractor := &RuleBasedConstraintExtractor{
		patterns: make(map[string][]ConstraintPattern),
	}

	// 这个测试通过检查日志输出来验证不会 panic
	// regexp 包会返回错误，addPattern 应该跳过并记录日志
	extractor.addPattern("test", "[invalid", 0.5)

	if len(extractor.patterns["test"]) != 0 {
		t.Error("expected no patterns added for invalid regex")
	}
}

// ─────────────────────────────────────────────────────────
// Test: extractContext 上下文提取
// ─────────────────────────────────────────────────────────

func TestExtractContext(t *testing.T) {
	content := "This is a long message with some important keywords like Go and Redis for caching purposes"

	re := regexp.MustCompile(`(?i)\bGo\b`)
	context := extractContext(content, re)

	if context == "" {
		t.Error("expected non-empty context")
	}

	// 上下文应该包含匹配关键字
	if len(context) < len("Go") {
		t.Error("context should be at least as long as the matched text")
	}

	t.Logf("Extracted context: %q", context)
}

// ─────────────────────────────────────────────────────────
// Test: uniqueStrings 边界情况
// ─────────────────────────────────────────────────────────

func TestUniqueStrings(t *testing.T) {
	// 空切片
	result := uniqueStrings([]string{})
	if result == nil || len(result) != 0 {
		t.Error("expected empty result for empty input")
	}

	// nil 切片 - 返回 nil 是可以接受的（长度仍为 0）
	result = uniqueStrings(nil)
	if result != nil && len(result) != 0 {
		t.Error("expected empty result for nil input")
	}

	// 全部重复
	result = uniqueStrings([]string{"a", "a", "a"})
	if len(result) != 1 || result[0] != "a" {
		t.Errorf("expected ['a'], got %v", result)
	}

	// 无重复
	result = uniqueStrings([]string{"a", "b", "c"})
	if len(result) != 3 {
		t.Errorf("expected 3 unique elements, got %d", len(result))
	}
}

// ─────────────────────────────────────────────────────────
// Test: Constraints.IsEmpty
// ─────────────────────────────────────────────────────────

func TestConstraints_IsEmpty(t *testing.T) {
	empty := &Constraints{}
	if !empty.IsEmpty() {
		t.Error("expected empty constraints to be empty")
	}

	notEmpty := &Constraints{
		Technical: []string{"Go"},
	}
	if notEmpty.IsEmpty() {
		t.Error("expected non-empty constraints to not be empty")
	}
}
