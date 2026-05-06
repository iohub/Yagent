package compact

import (
	"context"
	"strings"
	"testing"
	"time"

	"codeactor/internal/llm"
)

// mockSummaryClient 用于测试的 mock 摘要客户端
type mockSummaryClient struct {
	summary string
	err     error
	called  int
}

func (m *mockSummaryClient) GenerateSummary(ctx context.Context, messages []llm.Message) (string, error) {
	m.called++
	if m.err != nil {
		return "", m.err
	}
	return m.summary, nil
}

// TestEngine_NoCompression 测试未超限时不压缩
func TestEngine_NoCompression(t *testing.T) {
	cfg := &DefaultConfig
	cfg.MaxContextTokens = 10000
	cfg.Strategy = StrategyBalanced

	engine, err := NewEngine(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	// 短消息，不触发压缩
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "System prompt"},
		{Role: llm.RoleUser, Content: "Hello"},
		{Role: llm.RoleAssistant, Content: "Hi there!"},
	}

	result, err := engine.Compress(context.Background(), messages)
	if err != nil {
		t.Fatal(err)
	}

	// 应该不压缩
	if result.OriginalTokens != result.CompressedTokens {
		t.Errorf("Expected no compression, got ratio %.2f", result.CompressionRatio)
	}

	if result.StrategyUsed != "balanced" {
		t.Errorf("Expected strategy 'balanced', got '%s'", result.StrategyUsed)
	}
}

// TestEngine_Conservative 测试保守策略
func TestEngine_Conservative(t *testing.T) {
	cfg := &Config{
		MaxContextTokens:   500,
		Strategy:           StrategyConservative,
		L1Threshold:        400,
		L2Threshold:        300,
		L3Threshold:        200,
		KeepRecentRounds:   2,
	}

	engine, err := NewEngine(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	// 包含超长tool输出，总token数要超过L2Threshold
	// L2Compress 只在 >3000 字符时截断，所以这里用 4000 字符
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "System prompt for the assistant"},
		{Role: llm.RoleUser, Content: "User request with some details"},
		{Role: llm.RoleTool, Content: strings.Repeat("x", 4000)}, // >3000 字符才会截断
		{Role: llm.RoleAssistant, Content: "Done processing"},
		{Role: llm.RoleUser, Content: "More content"},
		{Role: llm.RoleAssistant, Content: "Final response"},
	}

	result, err := engine.Compress(context.Background(), messages)
	if err != nil {
		t.Fatal(err)
	}

	// 找到被截断的tool输出
	foundTruncated := false
	for _, msg := range result.CompressedMessages {
		if msg.Role == llm.RoleTool && strings.Contains(msg.Content, "[...TRUNCATED...]") {
			foundTruncated = true
			break
		}
	}
	if !foundTruncated {
		t.Error("Tool output should be truncated with [..TRUNCATED..]")
	}
}

// TestEngine_Balanced 测试平衡策略
func TestEngine_Balanced(t *testing.T) {
	cfg := &Config{
		MaxContextTokens:   500,
		Strategy:           StrategyBalanced,
		L1Threshold:        400,
		L2Threshold:        300,
		KeepRecentRounds:   2,
	}

	engine, err := NewEngine(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	// 长对话
	messages := make([]llm.Message, 0, 20)
	messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: "System"})
	messages = append(messages, llm.Message{Role: llm.RoleUser, Content: "User request"})

	for i := 0; i < 10; i++ {
		messages = append(messages, llm.Message{
			Role:    llm.RoleAssistant,
			Content: strings.Repeat("a", 100),
		})
		messages = append(messages, llm.Message{
			Role:    llm.RoleTool,
			Content: strings.Repeat("b", 100),
		})
	}

	result, err := engine.Compress(context.Background(), messages)
	if err != nil {
		t.Fatal(err)
	}

	// System和User应该被保留
	if result.CompressedMessages[0].Role != llm.RoleSystem {
		t.Error("System message should be preserved")
	}
	if result.CompressedMessages[1].Role != llm.RoleUser {
		t.Error("User message should be preserved")
	}
}

// TestEngine_Aggressive 测试激进策略
func TestEngine_Aggressive(t *testing.T) {
	cfg := &Config{
		MaxContextTokens:   200,
		Strategy:           StrategyAggressive,
		L1Threshold:        500,
		L2Threshold:        400,
		L3Threshold:        300,
		KeepRecentRounds:   2,
	}

	engine, err := NewEngine(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	// 很长对话
	messages := make([]llm.Message, 0, 30)
	messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: "System"})
	messages = append(messages, llm.Message{Role: llm.RoleUser, Content: "User"})

	for i := 0; i < 15; i++ {
		messages = append(messages, llm.Message{
			Role:    llm.RoleAssistant,
			Content: strings.Repeat("a", 50),
		})
		messages = append(messages, llm.Message{
			Role:    llm.RoleTool,
			Content: strings.Repeat("b", 50),
		})
	}

	result, err := engine.Compress(context.Background(), messages)
	if err != nil {
		t.Fatal(err)
	}

	// 验证压缩比
	if result.CompressionRatio >= 1.0 {
		t.Errorf("Expected compression ratio < 1.0, got %.2f", result.CompressionRatio)
	}

	// System消息应该被保留（L3Compress 始终保留第一条消息）
	if result.CompressedMessages[0].Role != llm.RoleSystem {
		t.Error("System message should be preserved")
	}
}

// TestEngine_EmptyMessages 测试空消息列表
func TestEngine_EmptyMessages(t *testing.T) {
	cfg := &Config{
		MaxContextTokens: 1000,
		Strategy:         StrategyBalanced,
		L1Threshold:      800,
		L2Threshold:      600,
		L3Threshold:      400,
	}

	engine, err := NewEngine(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	result, err := engine.Compress(context.Background(), []llm.Message{})
	if err != nil {
		t.Fatal(err)
	}

	if result.OriginalTokens != 0 {
		t.Error("Expected 0 tokens for empty messages")
	}
}

// TestEngine_CountTokens 测试token计数
func TestEngine_CountTokens(t *testing.T) {
	cfg := &Config{
		MaxContextTokens: 1000,
		Strategy:         StrategyBalanced,
		L1Threshold:      800,
		L2Threshold:      600,
		L3Threshold:      400,
	}

	engine, err := NewEngine(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "Hello world"},
		{Role: llm.RoleUser, Content: "How are you?"},
	}

	tokens, err := engine.CountTokens(messages)
	if err != nil {
		t.Fatal(err)
	}

	if tokens <= 0 {
		t.Errorf("Expected positive token count, got %d", tokens)
	}
}

// TestPriority_CalculatePriorities 测试优先级计算
func TestPriority_CalculatePriorities(t *testing.T) {
	cfg := &Config{
		KeepRecentRounds: 3,
	}

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "System"},
		{Role: llm.RoleUser, Content: "User"},
		{Role: llm.RoleAssistant, Content: "Assistant"},
		{Role: llm.RoleUser, Content: "Recent user"},
	}

	calc := NewPriorityCalculator(DefaultPriorityWeights)
	priorities := calc.CalculatePriorities(context.Background(), messages, cfg)

	// System应该有最高优先级
	if priorities[0].Score <= priorities[2].Score {
		t.Error("System message should have highest priority")
	}

	// 最近的消息（索引3，User）应该比早期的Assistant（索引2）优先级高
	// 因为User基础分(8.0) > Assistant基础分(4.0)，且时间衰减会进一步提升
	if priorities[3].Score <= priorities[2].Score {
		t.Error("Recent User message should have higher priority than older assistant")
	}
}

// TestPriority_Intermediate 测试"优先压缩中间"策略
func TestPriority_Intermediate(t *testing.T) {
	cfg := &Config{
		KeepRecentRounds: 3,
	}

	// 模拟10条消息
	messages := make([]llm.Message, 10)
	messages[0] = llm.Message{Role: llm.RoleSystem, Content: "System"}
	messages[1] = llm.Message{Role: llm.RoleUser, Content: "User"}
	for i := 2; i < 10; i++ {
		if i%2 == 0 {
			messages[i] = llm.Message{Role: llm.RoleAssistant, Content: strings.Repeat("a", 100)}
		} else {
			messages[i] = llm.Message{Role: llm.RoleTool, Content: strings.Repeat("b", 100)}
		}
	}

	calc := NewPriorityCalculator(DefaultPriorityWeights)
	priorities := calc.CalculatePriorities(context.Background(), messages, cfg)

	// 中间区域的消息（索引3-6）应该是中间对话
	for i := 3; i <= 6; i++ {
		if !priorities[i].IsIntermediate {
			t.Errorf("Message %d should be intermediate", i)
		}
	}

	// 最近的消息（索引7-9）应该是近期保留
	for i := 7; i <= 9; i++ {
		if !priorities[i].IsRecent {
			t.Errorf("Message %d should be recent", i)
		}
	}

	// 早期消息（索引2）应该是早期对话
	if !priorities[2].IsEarly {
		t.Error("Message 2 should be early")
	}
}

// TestLLMSummarizer_Basic 测试LLM摘要器基本功能（使用 mock client）
func TestLLMSummarizer_Basic(t *testing.T) {
	cfg := &Config{
		KeepRecentRounds:            2,
		SummarizationTimeout:        5 * time.Second,
		SummarizationMaxInputTokens: 8000,
	}

	mockClient := &mockSummaryClient{
		summary: "This conversation discussed implementing a user authentication system using JWT tokens.",
	}

	summarizer := NewLLMSummarizer(mockClient, cfg)

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "You are a helpful assistant."},
		{Role: llm.RoleUser, Content: "Help me implement auth"},
		{Role: llm.RoleAssistant, Content: "I'll help you with that. Let me first check the codebase."},
		{Role: llm.RoleTool, Content: strings.Repeat("tool output ", 500)},
		{Role: llm.RoleAssistant, Content: "Found the auth module. I'll modify the login function."},
		{Role: llm.RoleUser, Content: "Also add refresh token support"},
	}

	priorities := []MessagePriority{
		{Index: 0, Score: 10.0, IsSystem: true},
		{Index: 1, Score: 8.0, IsUser: true},
		{Index: 2, Score: 4.0, IsIntermediate: true},
		{Index: 3, Score: 2.0, IsIntermediate: true},
		{Index: 4, Score: 4.0, IsIntermediate: true},
		{Index: 5, Score: 8.0, IsUser: true},
	}

	result, err := summarizer.Summarize(context.Background(), messages, priorities)
	if err != nil {
		t.Fatal(err)
	}

	// 应该返回系统消息 + 摘要消息 + 保留区消息
	if len(result) < 3 {
		t.Errorf("Expected at least 3 messages, got %d", len(result))
	}

	// 第一条是原始System消息
	if result[0].Role != llm.RoleSystem {
		t.Error("First message should be system message")
	}

	// 第二条是摘要消息
	if result[1].Role != llm.RoleSystem {
		t.Error("Second message should be summary system message")
	}
	if !strings.Contains(result[1].Content, "[CONTEXT SUMMARY]") {
		t.Error("Summary should contain [CONTEXT SUMMARY] prefix")
	}

	// mock client应该被调用
	if mockClient.called != 1 {
		t.Errorf("Expected mock client to be called once, got %d", mockClient.called)
	}
}

// TestLLMSummarizer_NoClient 测试 nil 客户端时 L1 降级
func TestLLMSummarizer_NoClient(t *testing.T) {
	cfg := &Config{
		KeepRecentRounds: 2,
	}

	// nil client
	summarizer := NewLLMSummarizer(nil, cfg)

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "System"},
		{Role: llm.RoleUser, Content: "User"},
		{Role: llm.RoleAssistant, Content: "Assistant"},
	}

	result, err := summarizer.Summarize(context.Background(), messages, nil)
	if err != nil {
		t.Fatal(err)
	}

	// 应该返回原始消息，不做任何改动
	if len(result) != len(messages) {
		t.Errorf("Expected %d messages, got %d", len(messages), len(result))
	}
	for i, msg := range messages {
		if result[i].Content != msg.Content {
			t.Errorf("Message %d content changed", i)
		}
	}
}

// TestLLMSummarizer_Segmentation 测试消息分段逻辑
func TestLLMSummarizer_Segmentation(t *testing.T) {
	cfg := &Config{
		KeepRecentRounds:            0,
		SummarizationTimeout:        5 * time.Second,
		SummarizationMaxInputTokens: 200, // 很小，强制分多段
	}

	mockClient := &mockSummaryClient{
		summary: "Summary for batch",
	}

	summarizer := NewLLMSummarizer(mockClient, cfg)

	// 创建带 System 和 User 的完整消息列表
	messages := make([]llm.Message, 0, 22)
	messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: "System prompt"})
	messages = append(messages, llm.Message{Role: llm.RoleUser, Content: "User message"})

	// 添加大量中间消息（待摘要）
	for i := 0; i < 20; i++ {
		messages = append(messages, llm.Message{
			Role:    llm.RoleTool,
			Content: strings.Repeat("x", 200), // 每条约50 tokens
		})
	}

	// 构造优先级（前2条保留，后面全部可摘要）
	priorities := make([]MessagePriority, len(messages))
	priorities[0] = MessagePriority{Index: 0, Score: 10.0, IsSystem: true}
	priorities[1] = MessagePriority{Index: 1, Score: 8.0, IsUser: true}
	for i := 2; i < len(priorities); i++ {
		priorities[i] = MessagePriority{
			Index:          i,
			Score:          2.0,
			IsIntermediate: true,
		}
	}

	result, err := summarizer.Summarize(context.Background(), messages, priorities)
	if err != nil {
		t.Fatal(err)
	}

	// 应该返回：System + Summary + User = 至少3条消息
	if len(result) < 3 {
		t.Errorf("Expected at least 3 messages (system + summary + user), got %d", len(result))
	}

	// 验证 mock client 被调用了（因为消息多，应该分段）
	if mockClient.called < 1 {
		t.Errorf("Expected mock client to be called at least once, got %d", mockClient.called)
	}
}

// TestEngine_WithSummarizer 完整的 Engine + Mock Summarizer 集成测试
func TestEngine_WithSummarizer(t *testing.T) {
	cfg := &Config{
		MaxContextTokens:            300,
		Strategy:                    StrategyBalanced,
		L1Threshold:                 250,
		L2Threshold:                 200,
		L3Threshold:                 150,
		KeepRecentRounds:            2,
		SummarizationTimeout:        5 * time.Second,
		SummarizationMaxInputTokens: 8000,
	}

	mockClient := &mockSummaryClient{
		summary: "Summarized context: The conversation covered file operations and debugging.",
	}

	engine, err := NewEngine(cfg, mockClient)
	if err != nil {
		t.Fatal(err)
	}

	// 创建长对话 - 确保token数超过阈值
	messages := make([]llm.Message, 0, 15)
	messages = append(messages, llm.Message{Role: llm.RoleSystem, Content: "System prompt for the assistant"})
	messages = append(messages, llm.Message{Role: llm.RoleUser, Content: "Help me with the project"})

	for i := 0; i < 7; i++ {
		messages = append(messages, llm.Message{
			Role:    llm.RoleAssistant,
			Content: strings.Repeat("a", 200), // 每条约50 tokens
		})
		messages = append(messages, llm.Message{
			Role:    llm.RoleTool,
			Content: strings.Repeat("b", 200), // 每条约50 tokens
		})
	}
	// 保留最近一轮
	messages = append(messages, llm.Message{
		Role:    llm.RoleUser,
		Content: "Final question",
	})

	result, err := engine.Compress(context.Background(), messages)
	if err != nil {
		t.Fatal(err)
	}

	// 验证压缩比 < 1（说明有压缩发生）
	if result.CompressionRatio >= 1.0 {
		t.Errorf("Expected compression ratio < 1.0 with summarizer, got %.2f", result.CompressionRatio)
	}

	// 验证 System 和 User 消息被保留
	if result.CompressedMessages[0].Role != llm.RoleSystem {
		t.Error("System message should be preserved")
	}

	// 验证压缩统计信息包含 L1
	if !strings.Contains(result.CompressionStats, "L1") {
		t.Error("Compression stats should mention L1")
	}

	// 验证 mock client 被调用
	if mockClient.called == 0 {
		t.Error("Mock summarization client should have been called")
	}
}

// TestRuleCompressor_L1WithNilSummarizer 测试 RuleCompressor L1 在 summarizer 为 nil 时降级
func TestRuleCompressor_L1WithNilSummarizer(t *testing.T) {
	cfg := &Config{
		MaxContextTokens: 1000,
		Strategy:         StrategyBalanced,
	}

	// 不传入 summarizer
	rc := NewRuleCompressor(cfg, nil)

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "System"},
		{Role: llm.RoleUser, Content: "User"},
		{Role: llm.RoleAssistant, Content: "Assistant"},
	}

	result, err := rc.L1Compress(context.Background(), messages, nil)
	if err != nil {
		t.Fatal(err)
	}

	// 应该返回原始消息
	if len(result) != len(messages) {
		t.Errorf("Expected %d messages, got %d", len(messages), len(result))
	}
}

// TestRuleCompressor_L1WithSummarizer 测试 RuleCompressor L1 在 summarizer 存在时正常工作
func TestRuleCompressor_L1WithSummarizer(t *testing.T) {
	cfg := &Config{
		KeepRecentRounds:            1,
		SummarizationTimeout:        5 * time.Second,
		SummarizationMaxInputTokens: 8000,
	}

	mockClient := &mockSummaryClient{
		summary: "Summarized: project structure and auth module",
	}

	summarizer := NewLLMSummarizer(mockClient, cfg)
	rc := NewRuleCompressor(cfg, summarizer)

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "System"},
		{Role: llm.RoleUser, Content: "User"},
		{Role: llm.RoleAssistant, Content: strings.Repeat("x", 500)},
		{Role: llm.RoleTool, Content: strings.Repeat("y", 500)},
		{Role: llm.RoleUser, Content: "Final question"},
	}

	priorities := []MessagePriority{
		{Index: 0, Score: 10.0, IsSystem: true},
		{Index: 1, Score: 8.0, IsUser: true},
		{Index: 2, Score: 4.0, IsIntermediate: true},
		{Index: 3, Score: 2.0, IsIntermediate: true},
		{Index: 4, Score: 8.0, IsUser: true},
	}

	result, err := rc.L1Compress(context.Background(), messages, priorities)
	if err != nil {
		t.Fatal(err)
	}

	// 应该包含摘要消息
	foundSummary := false
	for _, msg := range result {
		if strings.Contains(msg.Content, "[CONTEXT SUMMARY]") {
			foundSummary = true
			break
		}
	}
	if !foundSummary {
		t.Error("Result should contain summary message")
	}
}

// FuzzEngine 模糊测试
func FuzzEngine(f *testing.F) {
	cfg := &Config{
		MaxContextTokens: 1000,
		Strategy:         StrategyBalanced,
		L1Threshold:      800,
		L2Threshold:      600,
		L3Threshold:      400,
		KeepRecentRounds: 2,
	}

	f.Add("system", "user", "assistant", "tool")
	f.Add("", "", "", "")

	engine, err := NewEngine(cfg, nil)
	if err != nil {
		f.Fatal(err)
	}

	f.Fuzz(func(t *testing.T, s1, s2, s3, s4 string) {
		messages := []llm.Message{
			{Role: llm.RoleSystem, Content: s1},
			{Role: llm.RoleUser, Content: s2},
			{Role: llm.RoleAssistant, Content: s3},
			{Role: llm.RoleTool, Content: s4},
		}

		_, err := engine.Compress(context.Background(), messages)
		if err != nil {
			t.Fatalf("Unexpected error: %v", err)
		}
	})
}
