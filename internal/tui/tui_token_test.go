package tui

import (
	"strings"
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

// TestTokenCounting_NoUsageMetadataFallback 验证 ai_response 事件不带 usage metadata 时，
// TUI 按 content 长度估算 output tokens 并计入 tokenUsagePerAgent
func TestTokenCounting_NoUsageMetadataFallback(t *testing.T) {
	m := newTestModel()
	m.tokenUsagePerAgent = make(map[string]*AgentTokenUsage)

	// 发送不带 usage metadata 的 ai_response（模拟本地模型场景）
	noUsageEvent := &messaging.MessageEvent{
		Type:      "ai_response",
		From:      "Repo-Agent",
		Content:   "这是一段测试回复内容，包含多个字符用于估算 token",
		Timestamp: time.Now(),
		Metadata:  map[string]interface{}{}, // 无 usage key
	}
	m = feedEvent(m, noUsageEvent)

	// 全局 outputTokens 应按 content 长度估算（len/4）
	contentStr := noUsageEvent.Content.(string)
	expectedOutput := int64(len(contentStr) / 4)
	if m.outputTokens != expectedOutput {
		t.Errorf("outputTokens 期望 %d（%d/4），实际 %d", expectedOutput, len(contentStr), m.outputTokens)
	}

	// tokenUsagePerAgent 应出现对应 agent
	if len(m.tokenUsagePerAgent) != 1 {
		t.Fatalf("期望1个agent，实际 %d", len(m.tokenUsagePerAgent))
	}
	agentUsage, ok := m.tokenUsagePerAgent["Repo-Agent"]
	if !ok {
		t.Fatal("缺少 Repo-Agent agent统计")
	}
	if agentUsage.OutputTokens != expectedOutput {
		t.Errorf("Repo-Agent OutputTokens 期望 %d，实际 %d", expectedOutput, agentUsage.OutputTokens)
	}
	// input 无法估算，应为 0
	if agentUsage.InputTokens != 0 {
		t.Errorf("Repo-Agent InputTokens 期望0，实际 %d", agentUsage.InputTokens)
	}
}

// TestFormatCacheInfo 验证 formatCacheInfo 长格式输出
func TestFormatCacheInfo(t *testing.T) {
	cases := []struct {
		name          string
		cacheRead     int64
		cacheCreation int64
		totalInput    int64
		wantContains  []string
		wantEmpty     bool
	}{
		{"both read and creation", 300, 200, 1500, []string{"Cache:", "CacheW:"}, false},
		{"only read", 300, 0, 1500, []string{"Cache:"}, false},
		{"only creation", 0, 200, 1500, []string{"CacheW: 200"}, false},
		{"none", 0, 0, 1500, nil, true},
		{"zero total", 300, 200, 0, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatCacheInfo(tc.cacheRead, tc.cacheCreation, tc.totalInput)
			if tc.wantEmpty {
				if got != "" {
					t.Errorf("期望空字符串，实际 %q", got)
				}
				return
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("输出 %q 应包含 %q", got, want)
				}
			}
		})
	}
}

// TestFormatCacheShort 验证 formatCacheShort 短格式输出
func TestFormatCacheShort(t *testing.T) {
	cases := []struct {
		name          string
		cacheRead     int64
		cacheCreation int64
		totalInput    int64
		wantContains  []string
		wantEmpty     bool
	}{
		{"both read and creation", 300, 200, 1500, []string{"⊕", "W:"}, false},
		{"only read", 300, 0, 1500, []string{"⊕20%"}, false},
		{"only creation", 0, 200, 1500, []string{"W:200"}, false},
		{"none", 0, 0, 1500, nil, true},
		{"zero total", 300, 200, 0, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatCacheShort(tc.cacheRead, tc.cacheCreation, tc.totalInput)
			if tc.wantEmpty {
				if got != "" {
					t.Errorf("期望空字符串，实际 %q", got)
				}
				return
			}
			for _, want := range tc.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("输出 %q 应包含 %q", got, want)
				}
			}
		})
	}
}

// TestTotalInputTokens_OpenAICompatibility 验证 OpenAI 兼容 API 路径下 totalInputTokens 正确累计
// 场景：prompt_tokens=1000 已包含 cache，其中 cache_read=300, cache_creation=200
// 正确的 totalInputTokens 应为 1000（不是 1000+300+200=1500）
// 命中率应为 300/1000 = 30%（不是 300/1500 = 20%）
func TestTotalInputTokens_OpenAICompatibility(t *testing.T) {
	m := newTestModel()
	m.tokenUsagePerAgent = make(map[string]*AgentTokenUsage)

	// 模拟 OpenAI 兼容 API 返回（prompt_tokens 已包含 cached_tokens）
	event := &messaging.MessageEvent{
		Type:      "ai_response",
		From:      "Director",
		Content:   "测试内容",
		Timestamp: time.Now(),
		Metadata: map[string]interface{}{
			"usage": map[string]interface{}{
				"prompt_tokens":               1000, // 已包含 cache
				"completion_tokens":           500,
				"total_tokens":                1500,
				"cache_creation_input_tokens": 200,
				"cache_read_input_tokens":     300,
				"total_input_tokens":          1000, // provider 口径的总输入
			},
		},
	}
	m = feedEvent(m, event)

	// 验证 totalInputTokens 正确累计（1000 而非 1500）
	if m.totalInputTokens != 1000 {
		t.Errorf("totalInputTokens 期望1000（provider 口径），实际%d", m.totalInputTokens)
	}
	if m.inputTokens != 1000 {
		t.Errorf("inputTokens 期望1000，实际%d", m.inputTokens)
	}
	if m.cacheReadInputTokens != 300 {
		t.Errorf("cacheReadInputTokens 期望300，实际%d", m.cacheReadInputTokens)
	}
	if m.cacheCreationInputTokens != 200 {
		t.Errorf("cacheCreationInputTokens 期望200，实际%d", m.cacheCreationInputTokens)
	}

	// 验证 per-agent 统计
	agentUsage, ok := m.tokenUsagePerAgent["Director"]
	if !ok {
		t.Fatal("缺少 Director agent 统计")
	}
	if agentUsage.TotalInputTokens != 1000 {
		t.Errorf("Director TotalInputTokens 期望1000，实际%d", agentUsage.TotalInputTokens)
	}

	// 验证 currentAgentRunTokens 统计
	if m.currentAgentRunTokens.TotalInputTokens != 1000 {
		t.Errorf("currentAgentRunTokens.TotalInputTokens 期望1000，实际%d", m.currentAgentRunTokens.TotalInputTokens)
	}
}

// TestTotalInputTokens_BackwardCompatibility 验证向后兼容：无 total_input_tokens 时回退旧公式
func TestTotalInputTokens_BackwardCompatibility(t *testing.T) {
	m := newTestModel()
	m.tokenUsagePerAgent = make(map[string]*AgentTokenUsage)

	// 模拟旧版 API（不含 total_input_tokens 字段）
	event := &messaging.MessageEvent{
		Type:      "ai_response",
		From:      "Repo-Agent",
		Content:   "测试内容",
		Timestamp: time.Now(),
		Metadata: map[string]interface{}{
			"usage": map[string]interface{}{
				"prompt_tokens":               2000,
				"completion_tokens":           800,
				"total_tokens":                2800,
				"cache_creation_input_tokens": 200,
				"cache_read_input_tokens":     300,
				// 注意：没有 total_input_tokens 字段
			},
		},
	}
	m = feedEvent(m, event)

	// 回退到旧公式：totalInput = prompt + cacheRead + cacheCreation = 2500
	if m.totalInputTokens != 2500 {
		t.Errorf("totalInputTokens 期望2500（旧公式回退），实际%d", m.totalInputTokens)
	}
	if m.inputTokens != 2000 {
		t.Errorf("inputTokens 期望2000，实际%d", m.inputTokens)
	}
}

// TestTotalInputTokens_ZeroFallback 验证 totalInputTokens=0 时回退旧公式
func TestTotalInputTokens_ZeroFallback(t *testing.T) {
	m := newTestModel()
	m.tokenUsagePerAgent = make(map[string]*AgentTokenUsage)

	// 第一个事件设置 totalInputTokens=0（模拟未初始化场景）
	event1 := &messaging.MessageEvent{
		Type:      "ai_response",
		From:      "Director",
		Content:   "内容1",
		Timestamp: time.Now(),
		Metadata: map[string]interface{}{
			"usage": map[string]interface{}{
				"prompt_tokens":     100,
				"completion_tokens": 50,
				"total_tokens":      150,
				"total_input_tokens": 0, // 模拟未设置
			},
		},
	}
	m = feedEvent(m, event1)

	// totalInputTokens 应为0，因为事件中没有设置
	if m.totalInputTokens != 0 {
		t.Errorf("totalInputTokens 期望0（事件无 total_input_tokens），实际%d", m.totalInputTokens)
	}

	// 第二个事件有 total_input_tokens
	event2 := &messaging.MessageEvent{
		Type:      "ai_response",
		From:      "Director",
		Content:   "内容2",
		Timestamp: time.Now(),
		Metadata: map[string]interface{}{
			"usage": map[string]interface{}{
				"prompt_tokens":     200,
				"completion_tokens": 100,
				"total_tokens":      300,
				"total_input_tokens": 200,
			},
		},
	}
	m = feedEvent(m, event2)

	// 累计：0 + 200 = 200
	if m.totalInputTokens != 200 {
		t.Errorf("totalInputTokens 期望200，实际%d", m.totalInputTokens)
	}
}

// TestCacheHitRate_OpenAIPath 验证 OpenAI 路径下命中率计算正确（100% 命中不封顶 50%）
func TestCacheHitRate_OpenAIPath(t *testing.T) {
	m := newTestModel()
	m.tokenUsagePerAgent = make(map[string]*AgentTokenUsage)

	// 模拟 100% cache 命中场景：prompt_tokens=1000 全部是 cached_tokens
	event := &messaging.MessageEvent{
		Type:      "ai_response",
		From:      "Director",
		Content:   "测试内容",
		Timestamp: time.Now(),
		Metadata: map[string]interface{}{
			"usage": map[string]interface{}{
				"prompt_tokens":               1000,
				"completion_tokens":           500,
				"total_tokens":                1500,
				"cache_creation_input_tokens": 0,
				"cache_read_input_tokens":     1000,
				"total_input_tokens":          1000,
			},
		},
	}
	m = feedEvent(m, event)

	// 验证 totalInputTokens 正确
	if m.totalInputTokens != 1000 {
		t.Errorf("totalInputTokens 期望1000，实际%d", m.totalInputTokens)
	}

	// 验证命中率计算：formatCacheInfo 应该显示 100%
	cacheInfo := formatCacheInfo(m.cacheReadInputTokens, m.cacheCreationInputTokens, m.totalInputTokens)
	if cacheInfo == "" {
		t.Fatal("cacheInfo 不应为空")
	}
	if !strings.Contains(cacheInfo, "100.0%") {
		t.Errorf("cacheInfo 应包含 100.0%%，实际 %q", cacheInfo)
	}
}
