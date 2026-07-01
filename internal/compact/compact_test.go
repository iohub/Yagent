package compact

import (
	"context"
	"strings"
	"sync"
	"testing"

	"codeactor/internal/llm"
)

// mockSummaryClient 用于测试的 mock 摘要客户端
type mockSummaryClient struct {
	mu      sync.Mutex
	summary string
	err     error
	called  int
}

func (m *mockSummaryClient) GenerateSummary(ctx context.Context, messages []llm.Message) (string, error) {
	m.mu.Lock()
	m.called++
	m.mu.Unlock()
	if m.err != nil {
		return "", m.err
	}
	return m.summary, nil
}

// TestEngine_NoCompression 测试未超限时不压缩
func TestEngine_NoCompression(t *testing.T) {
	cfg := &Config{
		MaxContextTokens:            10000,
		EnableAutoCompact:           true,
		KeepRecentRounds:            DefaultConfig.KeepRecentRounds,
		SummarizationTimeout:        DefaultConfig.SummarizationTimeout,
		SummarizationMaxInputTokens: DefaultConfig.SummarizationMaxInputTokens,
	}

	engine, err := NewEngine(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "System prompt"},
		{Role: llm.RoleUser, Content: "Hello"},
		{Role: llm.RoleAssistant, Content: "Hi there!"},
	}

	result, err := engine.Compress(context.Background(), messages)
	if err != nil {
		t.Fatal(err)
	}

	if result.OriginalTokens != result.CompressedTokens {
		t.Errorf("Expected no compression, got ratio %.2f", result.CompressionRatio)
	}

	if result.CompressionStats != "No compression needed" {
		t.Errorf("Expected stats 'No compression needed', got '%s'", result.CompressionStats)
	}
}

// TestEngine_EmptyMessages 测试空消息列表
func TestEngine_EmptyMessages(t *testing.T) {
	cfg := &Config{
		MaxContextTokens:            1000,
		EnableAutoCompact:           true,
		KeepRecentRounds:            DefaultConfig.KeepRecentRounds,
		SummarizationTimeout:        DefaultConfig.SummarizationTimeout,
		SummarizationMaxInputTokens: DefaultConfig.SummarizationMaxInputTokens,
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
		MaxContextTokens:            1000,
		EnableAutoCompact:           true,
		KeepRecentRounds:            DefaultConfig.KeepRecentRounds,
		SummarizationTimeout:        DefaultConfig.SummarizationTimeout,
		SummarizationMaxInputTokens: DefaultConfig.SummarizationMaxInputTokens,
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

// TestEngine_NoSummarizer 测试没有摘要器时降级为返回 recent 消息
func TestEngine_NoSummarizer(t *testing.T) {
	cfg := &Config{
		MaxContextTokens:            10,
		EnableAutoCompact:           true,
		KeepRecentRounds:            DefaultConfig.KeepRecentRounds,
		SummarizationTimeout:        DefaultConfig.SummarizationTimeout,
		SummarizationMaxInputTokens: DefaultConfig.SummarizationMaxInputTokens,
	}

	engine, err := NewEngine(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "System prompt"},
		{Role: llm.RoleUser, Content: "Hello, this is a longer message that should trigger compression"},
		{Role: llm.RoleAssistant, Content: "Hi there! How can I help you today?"},
	}

	result, err := engine.Compress(context.Background(), messages)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.CompressedMessages) == 0 {
		t.Error("Expected some messages in compressed result")
	}

	if result.SummaryInfo != "" {
		t.Error("Expected empty SummaryInfo when no summarizer")
	}
}

// TestEngine_WithSummarizer 测试有摘要器时的压缩
func TestEngine_WithSummarizer(t *testing.T) {
	cfg := &Config{
		MaxContextTokens:            10,
		EnableAutoCompact:           true,
		KeepRecentRounds:            1, // 只保留1轮，让older有消息
		SummarizationTimeout:        60 * 1000000000,
		SummarizationMaxInputTokens: 100000,
	}

	mockClient := &mockSummaryClient{
		summary: "This is a test summary of the conversation history.",
	}

	engine, err := NewEngine(cfg, mockClient)
	if err != nil {
		t.Fatal(err)
	}

	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "System prompt"},
		{Role: llm.RoleUser, Content: "First user message (long content that will be compressed)"},
		{Role: llm.RoleAssistant, Content: "First assistant response (long content too)"},
		{Role: llm.RoleUser, Content: "Second user message"},
		{Role: llm.RoleAssistant, Content: "Second assistant response"},
	}

	result, err := engine.Compress(context.Background(), messages)
	if err != nil {
		t.Fatal(err)
	}

	if mockClient.called != 1 {
		t.Errorf("Expected summarizer to be called once, got %d", mockClient.called)
	}

	if len(result.CompressedMessages) < 2 {
		t.Errorf("Expected at least 2 messages, got %d", len(result.CompressedMessages))
	}

	if result.CompressedMessages[0].Role != llm.RoleSystem {
		t.Error("Expected first message to be System")
	}
}

// TestEngine_ExtractRecentMessages 测试消息分区
func TestEngine_ExtractRecentMessages(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "System"},
		{Role: llm.RoleUser, Content: "Round 1 User"},
		{Role: llm.RoleAssistant, Content: "Round 1 Assistant"},
		{Role: llm.RoleUser, Content: "Round 2 User"},
		{Role: llm.RoleAssistant, Content: "Round 2 Assistant"},
		{Role: llm.RoleUser, Content: "Round 3 User"},
		{Role: llm.RoleAssistant, Content: "Round 3 Assistant"},
	}

	keepRounds := 2
	recent, older := extractRecentMessages(messages, keepRounds)

	if len(recent) != keepRounds*2 {
		t.Errorf("Expected %d recent messages, got %d", keepRounds*2, len(recent))
	}

	for i, msg := range older {
		if msg.Role == llm.RoleSystem {
			t.Errorf("System message found in older at index %d", i)
		}
	}
}

// TestEngine_ToolCallAtomicity 测试 Tool-Call 原子性保护
func TestEngine_ToolCallAtomicity(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleUser, Content: "First user message"},
		{Role: llm.RoleAssistant, Content: "First assistant response"},
		{Role: llm.RoleUser, Content: "Second user message (long content that will be compressed)"},
		{Role: llm.RoleAssistant, Content: "Second assistant response"},
		{Role: llm.RoleUser, Content: "Third user message (also long)"},
		{Role: llm.RoleAssistant, Content: "Calling tool", ToolCalls: []llm.ToolCall{
			{ID: "call-1", Type: "function", Function: llm.FunctionCall{Name: "test_tool", Arguments: "{}"}},
		}},
		{Role: llm.RoleTool, Content: "Tool result", ToolCallID: "call-1"},
		{Role: llm.RoleUser, Content: "Fourth user message"},
		{Role: llm.RoleAssistant, Content: "Fourth assistant response"},
	}

	keepRounds := 2
	recent, older := extractRecentMessages(messages, keepRounds)

	hasToolCallInRecent := false
	hasToolResponseInRecent := false
	for _, msg := range recent {
		if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) > 0 {
			hasToolCallInRecent = true
		}
		if msg.Role == llm.RoleTool && msg.ToolCallID != "" {
			hasToolResponseInRecent = true
		}
	}

	// 检查 tool_call 和 tool_response 没有被分割
	hasToolCallInOlder := false
	hasToolResponseInOlder := false
	for _, msg := range older {
		if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) > 0 {
			hasToolCallInOlder = true
		}
		if msg.Role == llm.RoleTool && msg.ToolCallID != "" {
			hasToolResponseInOlder = true
		}
	}

	// 如果 tool_response 在 older 中，那么对应的 tool_call 也应该在 older 中
	if hasToolResponseInOlder && !hasToolCallInOlder {
		t.Error("Tool response in older but corresponding tool_call was split to recent")
	}

	// 如果 tool_call 在 recent 中，对应的 tool_response 也应该在 recent 中
	if hasToolCallInRecent && !hasToolResponseInRecent {
		t.Error("Tool call in recent but corresponding tool_response was split to older")
	}
}

// TestEngine_CleanSummaryOutput 测试摘要输出清洗
func TestEngine_CleanSummaryOutput(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "empty input",
			input:    "",
			expected: "",
		},
		{
			name:     "remove markdown fence",
			input:    "```markdown\nSome content\n```",
			expected: "Some content",
		},
		{
			name:     "remove courtesy prefix",
			input:    "Sure, here is the summary.\nActual content",
			expected: "Actual content",
		},
		{
			name:     "compact whitespace",
			input:    "Line 1\n\n\n\nLine 2",
			expected: "Line 1\n\nLine 2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := CleanSummaryOutput(tt.input)
			if result != tt.expected {
				t.Errorf("CleanSummaryOutput(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// TestEngine_BuildCacheAwareMessages 测试缓存友好布局
func TestEngine_BuildCacheAwareMessages(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "System prompt"},
		{Role: llm.RoleUser, Content: "Old message 1"},
		{Role: llm.RoleAssistant, Content: "Old message 2"},
		{Role: llm.RoleUser, Content: "Recent user"},
		{Role: llm.RoleAssistant, Content: "Recent assistant"},
	}

	summary := "This is a summary of old messages"
	keepRounds := 1

	result := buildCacheAwareMessages(messages, summary, keepRounds)

	if len(result) != 4 {
		t.Errorf("Expected 4 messages, got %d", len(result))
	}

	if result[0].Role != llm.RoleSystem || !strings.Contains(result[0].Content, "System prompt") {
		t.Error("First message should be original System")
	}

	if result[1].Role != llm.RoleSystem || !strings.Contains(result[1].Content, "[CONTEXT SUMMARY]") {
		t.Error("Second message should be Summary")
	}

	if result[2].Role != llm.RoleUser {
		t.Error("Third message should be recent User")
	}
}

// TestDefaultConfig 测试默认配置值
func TestDefaultConfig(t *testing.T) {
	// 验证 DefaultConfig 的默认值
	if DefaultConfig.MaxContextTokens != 128000 {
		t.Errorf("DefaultConfig.MaxContextTokens = %d, want 128000", DefaultConfig.MaxContextTokens)
	}
	if !DefaultConfig.EnableAutoCompact {
		t.Error("DefaultConfig.EnableAutoCompact should be true")
	}
	if DefaultConfig.KeepRecentRounds != 3 {
		t.Errorf("DefaultConfig.KeepRecentRounds = %d, want 3", DefaultConfig.KeepRecentRounds)
	}

	// 验证 NewEngine 使用默认值时正确设置配置
	cfg := &Config{
		MaxContextTokens: 0, // 0 表示使用默认值
	}
	engine, err := NewEngine(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if engine.config.MaxContextTokens != 128000 {
		t.Errorf("Expected MaxContextTokens=128000 after NewEngine, got %d", engine.config.MaxContextTokens)
	}
}

// TestEngine_IncrementalCompression 测试增量压缩
func TestEngine_IncrementalCompression(t *testing.T) {
	cfg := &Config{
		MaxContextTokens:            10,
		EnableAutoCompact:           true,
		KeepRecentRounds:            1, // 只保留1轮，让older有消息
		SummarizationTimeout:        DefaultConfig.SummarizationTimeout,
		SummarizationMaxInputTokens: DefaultConfig.SummarizationMaxInputTokens,
	}

	mockClient := &mockSummaryClient{
		summary: "Merged summary of all messages.",
	}

	engine, err := NewEngine(cfg, mockClient)
	if err != nil {
		t.Fatal(err)
	}

	// 第一轮：触发全量压缩
	msg1 := []llm.Message{
		{Role: llm.RoleSystem, Content: "System prompt"},
		{Role: llm.RoleUser, Content: "First message that is long enough to trigger compression and will be compressed"},
		{Role: llm.RoleAssistant, Content: "First response"},
		{Role: llm.RoleUser, Content: "Second message that is also long enough to be compressed"},
		{Role: llm.RoleAssistant, Content: "Second response"},
	}
	result1, err := engine.Compress(context.Background(), msg1)
	if err != nil {
		t.Fatal(err)
	}

	// 验证：第一轮调用了一次 summarizer
	if mockClient.called != 1 {
		t.Errorf("Expected 1 summarizer call, got %d", mockClient.called)
	}

	// 验证 frozenSummary 已设置
	if engine.frozenSummary == "" {
		t.Error("Expected frozenSummary to be set after first compression")
	}

	// 验证压缩结果包含摘要消息
	if len(result1.CompressedMessages) < 2 {
		t.Errorf("Expected at least 2 messages (system + summary), got %d", len(result1.CompressedMessages))
	}
	if !strings.Contains(result1.CompressedMessages[1].Content, "[CONTEXT SUMMARY]") {
		t.Error("Expected second message to contain [CONTEXT SUMMARY]")
	}

	// 第二轮：更多消息到达，触发增量压缩
	msg2 := []llm.Message{
		{Role: llm.RoleSystem, Content: "System prompt"},
		{Role: llm.RoleSystem, Content: "[CONTEXT SUMMARY]\n" + engine.frozenSummary},
		{Role: llm.RoleUser, Content: "Third message"},
		{Role: llm.RoleAssistant, Content: "Third response"},
		{Role: llm.RoleUser, Content: "Fourth message"},
		{Role: llm.RoleAssistant, Content: "Fourth response"},
		{Role: llm.RoleUser, Content: "Fifth message"},
		{Role: llm.RoleAssistant, Content: "Fifth response"},
	}

	// 重置 mock 计数
	beforeCall := mockClient.called
	result2, err := engine.Compress(context.Background(), msg2)
	if err != nil {
		t.Fatal(err)
	}

	// 验证：第二轮调用了 summarizer（增量压缩）
	if mockClient.called <= beforeCall {
		t.Error("Expected summarizer to be called for incremental compression")
	}

	// 验证压缩结果包含摘要消息
	if len(result2.CompressedMessages) < 2 {
		t.Errorf("Expected at least 2 messages (system + summary), got %d", len(result2.CompressedMessages))
	}
	if !strings.Contains(result2.CompressedMessages[1].Content, "[CONTEXT SUMMARY]") {
		t.Error("Expected second message to contain [CONTEXT SUMMARY]")
	}
}

// TestEngine_AnchoredMessagesProtected 测试锚定消息保护
func TestEngine_AnchoredMessagesProtected(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "System"},
		{Role: llm.RoleUser, Content: "Normal message 1", IsAnchored: false},
		{Role: llm.RoleAssistant, Content: "Normal response 1"},
		{Role: llm.RoleUser, Content: "IMPORTANT: This message must never be compressed!", IsAnchored: true},
		{Role: llm.RoleAssistant, Content: "Important response", IsAnchored: true},
		{Role: llm.RoleUser, Content: "Normal message 2"},
		{Role: llm.RoleAssistant, Content: "Normal response 2"},
	}

	// 使用 keepRounds=1 确保只有最后2条消息在 recent 中
	keepRounds := 1
	recent, older := extractRecentMessages(messages, keepRounds)

	// 验证锚定消息不在 older 中
	for _, msg := range older {
		if msg.IsAnchored {
			t.Errorf("Anchored message found in older: %s", msg.Content)
		}
	}

	// 验证锚定消息在 recent 中
	anchoredFound := false
	for _, msg := range recent {
		if msg.IsAnchored {
			anchoredFound = true
			break
		}
	}
	if !anchoredFound {
		t.Error("Expected anchored messages to be in recent")
	}
}
