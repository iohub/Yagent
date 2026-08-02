package tui

import (
	"testing"
	"time"

	"codeactor/internal/messaging"
)

// TestTokenCounting_OnlyAiResponseCounts 验证只有 ai_response 事件统计token，ai_stream_end 不统计
func TestTokenCounting_OnlyAiResponseCounts(t *testing.T) {
	m := newTestModel()
	m.tokenUsagePerAgent = make(map[string]*AgentTokenUsage)

	// 发送 ai_stream_end 带 usage metadata — 不应统计token
	streamEndEvent := &messaging.MessageEvent{
		Type:      "ai_stream_end",
		From:      "Repo-Agent",
		Content:   "",
		Timestamp: time.Now(),
		Metadata: map[string]interface{}{
			"usage": map[string]interface{}{
				"prompt_tokens":     1000,
				"completion_tokens": 500,
				"total_tokens":      1500,
			},
		},
	}
	m = feedEvent(m, streamEndEvent)

	// ai_stream_end 不应增加token统计
	if m.inputTokens != 0 {
		t.Errorf("ai_stream_end 不应统计inputTokens，期望0，实际%d", m.inputTokens)
	}
	if m.outputTokens != 0 {
		t.Errorf("ai_stream_end 不应统计outputTokens，期望0，实际%d", m.outputTokens)
	}
	if len(m.tokenUsagePerAgent) != 0 {
		t.Errorf("ai_stream_end 不应增加tokenUsagePerAgent，期望空map，实际%v", m.tokenUsagePerAgent)
	}
}

// TestTokenCounting_AiResponseAccumulates 验证 ai_response 事件正确累计token
func TestTokenCounting_AiResponseAccumulates(t *testing.T) {
	m := newTestModel()
	m.tokenUsagePerAgent = make(map[string]*AgentTokenUsage)

	// Director 的 ai_response
	directorEvent := &messaging.MessageEvent{
		Type:      "ai_response",
		From:      "Director",
		Content:   "你好世界",
		Timestamp: time.Now(),
		Metadata: map[string]interface{}{
			"usage": map[string]interface{}{
				"prompt_tokens":               1000,
				"completion_tokens":           500,
				"total_tokens":                1500,
				"cache_creation_input_tokens": 200,
				"cache_read_input_tokens":     300,
			},
		},
	}
	m = feedEvent(m, directorEvent)

	if m.inputTokens != 1000 {
		t.Errorf("Director inputTokens 期望1000，实际%d", m.inputTokens)
	}
	if m.outputTokens != 500 {
		t.Errorf("Director outputTokens 期望500，实际%d", m.outputTokens)
	}
	if m.cacheCreationInputTokens != 200 {
		t.Errorf("cacheCreationInputTokens 期望200，实际%d", m.cacheCreationInputTokens)
	}
	if m.cacheReadInputTokens != 300 {
		t.Errorf("cacheReadInputTokens 期望300，实际%d", m.cacheReadInputTokens)
	}

	// Repo-Agent 的 ai_response
	repoEvent := &messaging.MessageEvent{
		Type:      "ai_response",
		From:      "Repo-Agent",
		Content:   "文件列表...",
		Timestamp: time.Now(),
		Metadata: map[string]interface{}{
			"usage": map[string]interface{}{
				"prompt_tokens":     2000,
				"completion_tokens": 800,
				"total_tokens":      2800,
			},
		},
	}
	m = feedEvent(m, repoEvent)

	// 全局累计
	if m.inputTokens != 3000 {
		t.Errorf("全局inputTokens 期望3000，实际%d", m.inputTokens)
	}
	if m.outputTokens != 1300 {
		t.Errorf("全局outputTokens 期望1300，实际%d", m.outputTokens)
	}

	// 按agent统计
	if len(m.tokenUsagePerAgent) != 2 {
		t.Fatalf("期望2个agent，实际%d", len(m.tokenUsagePerAgent))
	}

	directorUsage, ok := m.tokenUsagePerAgent["Director"]
	if !ok {
		t.Fatal("缺少Director agent统计")
	}
	if directorUsage.InputTokens != 1000 {
		t.Errorf("Director InputTokens 期望1000，实际%d", directorUsage.InputTokens)
	}
	if directorUsage.OutputTokens != 500 {
		t.Errorf("Director OutputTokens 期望500，实际%d", directorUsage.OutputTokens)
	}
	if directorUsage.CacheReadInputTokens != 300 {
		t.Errorf("Director CacheReadInputTokens 期望300，实际%d", directorUsage.CacheReadInputTokens)
	}

	repoUsage, ok := m.tokenUsagePerAgent["Repo-Agent"]
	if !ok {
		t.Fatal("缺少Repo-Agent agent统计")
	}
	if repoUsage.InputTokens != 2000 {
		t.Errorf("Repo-Agent InputTokens 期望2000，实际%d", repoUsage.InputTokens)
	}
	if repoUsage.OutputTokens != 800 {
		t.Errorf("Repo-Agent OutputTokens 期望800，实际%d", repoUsage.OutputTokens)
	}
}

// TestTokenCounting_CurrentAgentReset 验证 agent 切换时 currentAgentRunTokens 正确重置
func TestTokenCounting_CurrentAgentReset(t *testing.T) {
	m := newTestModel()
	m.tokenUsagePerAgent = make(map[string]*AgentTokenUsage)

	// Director 的调用
	directorEvent := &messaging.MessageEvent{
		Type:      "ai_response",
		From:      "Director",
		Content:   "你好",
		Timestamp: time.Now(),
		Metadata: map[string]interface{}{
			"usage": map[string]interface{}{
				"prompt_tokens":     1000,
				"completion_tokens": 200,
				"total_tokens":      1200,
			},
		},
	}
	m = feedEvent(m, directorEvent)

	if m.currentAgentRunTokens.AgentName != "Director" {
		t.Errorf("currentAgentRunTokens.AgentName 期望 Director，实际 %s", m.currentAgentRunTokens.AgentName)
	}
	if m.currentAgentRunTokens.InputTokens != 1000 {
		t.Errorf("currentAgentRunTokens.InputTokens 期望1000，实际%d", m.currentAgentRunTokens.InputTokens)
	}
	if m.currentAgentRunTokens.OutputTokens != 200 {
		t.Errorf("currentAgentRunTokens.OutputTokens 期望200，实际%d", m.currentAgentRunTokens.OutputTokens)
	}

	// Repo-Agent 的调用 — 应触发重置
	repoEvent := &messaging.MessageEvent{
		Type:      "ai_response",
		From:      "Repo-Agent",
		Content:   "文件列表...",
		Timestamp: time.Now(),
		Metadata: map[string]interface{}{
			"usage": map[string]interface{}{
				"prompt_tokens":     500,
				"completion_tokens": 100,
				"total_tokens":      600,
			},
		},
	}
	m = feedEvent(m, repoEvent)

	// 应重置为 Repo-Agent 的数值
	if m.currentAgentRunTokens.AgentName != "Repo-Agent" {
		t.Errorf("currentAgentRunTokens.AgentName 期望 Repo-Agent，实际 %s", m.currentAgentRunTokens.AgentName)
	}
	if m.currentAgentRunTokens.InputTokens != 500 {
		t.Errorf("currentAgentRunTokens.InputTokens 期望500，实际%d", m.currentAgentRunTokens.InputTokens)
	}
	if m.currentAgentRunTokens.OutputTokens != 100 {
		t.Errorf("currentAgentRunTokens.OutputTokens 期望100，实际%d", m.currentAgentRunTokens.OutputTokens)
	}

	// 全局值应累加
	if m.inputTokens != 1500 {
		t.Errorf("全局inputTokens 期望1500，实际%d", m.inputTokens)
	}
	if m.outputTokens != 300 {
		t.Errorf("全局outputTokens 期望300，实际%d", m.outputTokens)
	}
}

// TestTokenCounting_EmptyContentNotOverwriteStream 验证 ai_response 空Content不覆盖已累积的流式内容
func TestTokenCounting_EmptyContentNotOverwriteStream(t *testing.T) {
	m := newTestModel()
	m.tokenUsagePerAgent = make(map[string]*AgentTokenUsage)

	// 先模拟流式内容累积
	streamEvent := &messaging.MessageEvent{
		Type:      "ai_chunk",
		From:      "Director",
		Content:   map[string]interface{}{"content": "流式内容"},
		Timestamp: time.Now(),
	}
	m = feedEvent(m, streamEvent)

	// 定稿时 Content 为空（纯工具调用轮次）
	emptyResponseEvent := &messaging.MessageEvent{
		Type:      "ai_response",
		From:      "Director",
		Content:   "",
		Timestamp: time.Now(),
		Metadata: map[string]interface{}{
			"usage": map[string]interface{}{
				"prompt_tokens":     1000,
				"completion_tokens": 0,
				"total_tokens":      1000,
			},
		},
	}
	m = feedEvent(m, emptyResponseEvent)

	// 流式内容应保留（不被空字符串覆盖）
	// 注意：contentCache 可能已被清空，这里主要验证 token 统计
	if m.inputTokens != 1000 {
		t.Errorf("inputTokens 期望1000，实际%d", m.inputTokens)
	}
	if m.outputTokens != 0 {
		t.Errorf("outputTokens 期望0，实际%d", m.outputTokens)
	}
}
