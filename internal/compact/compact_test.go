package compact

import (
	"context"
	"strings"
	"testing"
	"codeactor/internal/llm"
)

// TestEngine_NoCompression 测试未超限时不压缩
func TestEngine_NoCompression(t *testing.T) {
	cfg := &DefaultConfig
	cfg.MaxContextTokens = 10000
	cfg.Strategy = StrategyBalanced
	cfg := &DefaultConfig
	cfg.MaxContextTokens = 10000
	cfg.Strategy = StrategyBalanced
	cfg := &DefaultConfig
	cfg.MaxContextTokens = 10000
	cfg.Strategy = StrategyBalanced
	cfg := &DefaultConfig
	cfg.MaxContextTokens = 10000
	cfg.Strategy = StrategyBalanced
	cfg := &DefaultConfig
	cfg.MaxContextTokens = 10000
	cfg.Strategy = StrategyBalanced

	engine, err := NewEngine(cfg)
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
		L2Threshold:        300,
		KeepRecentRounds:   2,
	}

	engine, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}

	// 包含超长tool输出
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "System"},
		{Role: llm.RoleUser, Content: "User"},
		{Role: llm.RoleTool, Content: strings.Repeat("x", 2000)},
		{Role: llm.RoleAssistant, Content: "Done"},
	}

	result, err := engine.Compress(context.Background(), messages)
	if err != nil {
		t.Fatal(err)
	}

	// Tool输出应该被截断
	if len(result.CompressedMessages[2].Content) >= 2000 {
		t.Error("Tool output should be truncated")
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

	engine, err := NewEngine(cfg)
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

	engine, err := NewEngine(cfg)
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

	// 验证System和User被保留
	if result.CompressedMessages[0].Role != llm.RoleSystem {
		t.Error("System message should be preserved")
	}
	if result.CompressedMessages[1].Role != llm.RoleUser {
		t.Error("User message should be preserved")
	}
}

// TestEngine_EmptyMessages 测试空消息列表
func TestEngine_EmptyMessages(t *testing.T) {
	cfg := &Config{
		MaxContextTokens: 1000,
		Strategy:         StrategyBalanced,
	}

	engine, err := NewEngine(cfg)
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
	}

	engine, err := NewEngine(cfg)
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
		{Role: llm.RoleTool, Content: "Tool"},
	}

	calc := NewPriorityCalculator(DefaultPriorityWeights)
	priorities := calc.CalculatePriorities(context.Background(), messages, cfg)

	// System应该有最高优先级
	if priorities[0].Score <= priorities[2].Score {
		t.Error("System message should have highest priority")
	}

	// 最近的消息（索引3）应该比早期的（索引0）优先级高（除了System）
	if priorities[3].Score <= priorities[2].Score {
		t.Error("Recent message should have higher priority than older assistant")
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

// FuzzEngine 模糊测试
func FuzzEngine(f *testing.F) {
	cfg := &Config{
		MaxContextTokens: 1000,
		Strategy:         StrategyBalanced,
		KeepRecentRounds: 2,
	}

	f.Add("system", "user", "assistant", "tool")
	f.Add("", "", "", "")

	engine, err := NewEngine(cfg)
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
