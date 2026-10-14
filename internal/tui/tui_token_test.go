package tui

import (
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"charm.land/lipgloss/v2"
	"yagent/internal/config"
	"yagent/internal/messaging"
	"yagent/internal/models"
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

	if m.currentRun().AgentName != "Director" {
		t.Errorf("currentAgentRunTokens.AgentName 期望 Director，实际 %s", m.currentRun().AgentName)
	}
	if m.currentRun().InputTokens != 1000 {
		t.Errorf("currentAgentRunTokens.InputTokens 期望1000，实际%d", m.currentRun().InputTokens)
	}
	if m.currentRun().OutputTokens != 200 {
		t.Errorf("currentAgentRunTokens.OutputTokens 期望200，实际%d", m.currentRun().OutputTokens)
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
	if m.currentRun().AgentName != "Repo-Agent" {
		t.Errorf("currentAgentRunTokens.AgentName 期望 Repo-Agent，实际 %s", m.currentRun().AgentName)
	}
	if m.currentRun().InputTokens != 500 {
		t.Errorf("currentAgentRunTokens.InputTokens 期望500，实际%d", m.currentRun().InputTokens)
	}
	if m.currentRun().OutputTokens != 100 {
		t.Errorf("currentAgentRunTokens.OutputTokens 期望100，实际%d", m.currentRun().OutputTokens)
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

// TestTokenCounting_NoUsageMetadataIgnored 验证不带 usage 的 ai_response 不再被 TUI 自估。
// 三个发布方（executor.go / planner_run.go / meta.go）都会挂上 usage —— provider 不给
// usage 时上游已经用 EstimateTokens 兜底并标 estimated；TUI 再按 len/4 估一次只会造出
// 第二种口径（而且只估 output，把 input 估成 0）。
func TestTokenCounting_NoUsageMetadataIgnored(t *testing.T) {
	m := newTestModel()
	m.tokenUsagePerAgent = make(map[string]*AgentTokenUsage)

	m = feedEvent(m, &messaging.MessageEvent{
		Type:      "ai_response",
		From:      "Repo-Agent",
		Content:   "这是一段测试回复内容，包含多个字符用于估算 token",
		Timestamp: time.Now(),
		Metadata:  map[string]interface{}{}, // 无 usage key
	})

	if m.outputTokens != 0 || m.inputTokens != 0 {
		t.Errorf("无 usage 时不该累加：in=%d out=%d", m.inputTokens, m.outputTokens)
	}
	if len(m.tokenUsagePerAgent) != 0 {
		t.Errorf("无 usage 时不该建 agent 条目，实际 %v", m.tokenUsagePerAgent)
	}
	if rows := buildTokenPanel(m.tokenSnapshot(), tokenPanelSpec{innerWidth: 46}); rows != nil {
		t.Errorf("无用量时面板不该产出任何行，实际 %d 行", len(rows))
	}
}

// TestCacheHitRatio 钉住命中率口径：分母必须是归一化输入。
// OpenAI 的 prompt_tokens 已含 cached，再把它加进分母第二次就会把命中率算低
// （commit 6a35cbc 修的就是这个），所以这里同时守住"没有缓存活动/没有分母就不画计量条"。
func TestCacheHitRatio(t *testing.T) {
	cases := []struct {
		name   string
		read   int64
		create int64
		total  int64
		want   string // ratioPct 结果；"" = 整条计量条不该出现
	}{
		{"both read and creation", 300, 200, 1500, "20%"},
		{"only read", 300, 0, 1500, "20%"},
		{"only creation", 0, 200, 1500, "0%"},
		{"no cache activity", 0, 0, 1500, ""},
		{"no denominator", 300, 200, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ratio, ok := cacheHitRatio(tc.read, tc.create, tc.total)
			if tc.want == "" {
				if ok {
					t.Errorf("cacheHitRatio(%d,%d,%d) 应判为无可展示缓存，实际 ok=true", tc.read, tc.create, tc.total)
				}
				return
			}
			if !ok {
				t.Fatalf("cacheHitRatio(%d,%d,%d) 应可展示，实际 ok=false", tc.read, tc.create, tc.total)
			}
			if got := ratioPct(ratio); got != tc.want {
				t.Errorf("ratioPct = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTokenCacheMeterRow 验证 ⊕ 计量条：hit 恒在、new 仅在有写缓存时出现、
// 明细塞不下时整段消失（而不是溢出或截半）。
func TestTokenCacheMeterRow(t *testing.T) {
	snap := tokenSnapshot{totalIn: 1500, cacheRead: 300, cacheNew: 200}

	full := tokenCacheMeterRow(snap, 46, tokenMeterCells)
	if w := lipgloss.Width(full); w != 46 {
		t.Errorf("计量条宽度 = %d, want 46", w)
	}
	want := "⊕ ▰▰▱▱▱▱▱▱▱▱ 20%             hit 300 · new 200"
	if got := stripANSI(full); got != want {
		t.Errorf("计量条输出\n got %q\nwant %q", got, want)
	}

	// 只有读缓存时不该出现 new 段
	onlyRead := stripANSI(tokenCacheMeterRow(tokenSnapshot{totalIn: 1500, cacheRead: 300}, 46, tokenMeterCells))
	if strings.Contains(onlyRead, "new") {
		t.Errorf("无写缓存时不该出现 new 段：%q", onlyRead)
	}

	// 宽度不够放明细时，宁可不显示也不越界
	narrow := tokenCacheMeterRow(snap, 24, tokenMeterCells)
	if strings.Contains(stripANSI(narrow), "hit") {
		t.Errorf("窄面板应丢掉明细段：%q", stripANSI(narrow))
	}
	if w := lipgloss.Width(narrow); w > 24 {
		t.Errorf("窄面板计量条宽度 = %d, want <= 24", w)
	}

	// 完全没有缓存活动 → 整行不渲染
	if got := tokenCacheMeterRow(tokenSnapshot{totalIn: 1500}, 46, tokenMeterCells); got != "" {
		t.Errorf("无缓存活动时应返回空串，实际 %q", got)
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
	if m.currentRun().TotalInputTokens != 1000 {
		t.Errorf("currentAgentRunTokens.TotalInputTokens 期望1000，实际%d", m.currentRun().TotalInputTokens)
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
				"prompt_tokens":      100,
				"completion_tokens":  50,
				"total_tokens":       150,
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
				"prompt_tokens":      200,
				"completion_tokens":  100,
				"total_tokens":       300,
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

	// 命中率：1000 全是 cached → 100%，不能被算成 50%
	ratio, ok := cacheHitRatio(m.cacheReadInputTokens, m.cacheCreationInputTokens, m.totalInputTokens)
	if !ok {
		t.Fatal("有缓存命中时计量条应可展示")
	}
	if got := ratioPct(ratio); got != "100%" {
		t.Errorf("命中率 = %q, want 100%%", got)
	}
	// 渲染出口同样要显示满格
	row := stripANSI(tokenCacheMeterRow(m.tokenSnapshot(), 46, tokenMeterCells))
	if !strings.Contains(row, "⊕ ▰▰▰▰▰▰▰▰▰▰ 100%") {
		t.Errorf("计量条应显示满格 100%%，实际 %q", row)
	}
}

// ── 实时 context token（ContextTokens）测试 ──
// 覆盖：覆盖式非累计更新、压缩事件→ai_response 收敛链、agent 切换重置、
// emergency 事件可达性、From 归一化、全零 usage 守卫，以及三处渲染出口。

// ctxTestAiResponse 构造带 total_input_tokens 的 ai_response 事件（测试 ContextTokens 用）
func ctxTestAiResponse(from string, totalInput int64) *messaging.MessageEvent {
	return &messaging.MessageEvent{
		Type:      "ai_response",
		From:      from,
		Content:   "测试内容",
		Timestamp: time.Now(),
		Metadata: map[string]interface{}{
			"usage": map[string]interface{}{
				"prompt_tokens":      totalInput,
				"completion_tokens":  10,
				"total_tokens":       totalInput + 10,
				"total_input_tokens": totalInput,
			},
		},
	}
}

// ctxTestCompression 构造带 compressed_tokens 的上下文压缩事件（三级压缩共用载荷结构）
func ctxTestCompression(eventType, from string, compressed int64) *messaging.MessageEvent {
	return &messaging.MessageEvent{
		Type: messaging.EventType(eventType),
		From: from,
		Content: map[string]interface{}{
			"original_tokens":   compressed * 4,
			"compressed_tokens": compressed,
			"saved_tokens":      compressed * 3,
			"saved_percent":     75.0,
		},
		Timestamp: time.Now(),
	}
}

// TestContextTokens_OverwriteNotAccumulate 验证 ContextTokens 覆盖式更新（非累计）
func TestContextTokens_OverwriteNotAccumulate(t *testing.T) {
	m := newTestModel()
	m.tokenUsagePerAgent = make(map[string]*AgentTokenUsage)

	m = feedEvent(m, ctxTestAiResponse("Director", 1000))
	if m.currentRun().ContextTokens != 1000 {
		t.Errorf("首次 ai_response 后 ContextTokens 期望1000，实际%d", m.currentRun().ContextTokens)
	}

	m = feedEvent(m, ctxTestAiResponse("Director", 800))
	if m.currentRun().ContextTokens != 800 {
		t.Errorf("第二次 ai_response 后 ContextTokens 期望800（覆盖式，非1800累计），实际%d", m.currentRun().ContextTokens)
	}
}

// TestContextTokens_CompressThenCallSequence 验证压缩事件→下一次 ai_response 的收敛链：
// 压缩后立即显示压缩估算值，下一次 LLM 调用的 total_input 覆盖为精确值
func TestContextTokens_CompressThenCallSequence(t *testing.T) {
	m := newTestModel()
	m.tokenUsagePerAgent = make(map[string]*AgentTokenUsage)

	m = feedEvent(m, ctxTestAiResponse("Director", 1000))
	if m.currentRun().ContextTokens != 1000 {
		t.Fatalf("ai_response 后 ContextTokens 期望1000，实际%d", m.currentRun().ContextTokens)
	}

	// 压缩事件：面板立即反映压缩后的真实值
	m = feedEvent(m, ctxTestCompression("context_compressed", "Director", 300))
	if m.currentRun().ContextTokens != 300 {
		t.Errorf("压缩事件后 ContextTokens 期望300，实际%d", m.currentRun().ContextTokens)
	}

	// 下一次 ai_response：覆盖为精确值
	m = feedEvent(m, ctxTestAiResponse("Director", 310))
	if m.currentRun().ContextTokens != 310 {
		t.Errorf("压缩后 ai_response ContextTokens 期望310（精确值覆盖估算），实际%d", m.currentRun().ContextTokens)
	}
}

// TestContextTokens_AgentSwitchResetsRun 验证压缩事件的 From 与当前 agent 不同时重置 run tokens
func TestContextTokens_AgentSwitchResetsRun(t *testing.T) {
	m := newTestModel()
	m.tokenUsagePerAgent = make(map[string]*AgentTokenUsage)

	m = feedEvent(m, ctxTestAiResponse("Director", 500))
	if m.currentRun().AgentName != "Director" {
		t.Fatalf("初始 agent 期望 Director，实际 %s", m.currentRun().AgentName)
	}

	// Repo-Agent 的压缩事件：视为 agent 切换，整体重置
	m = feedEvent(m, ctxTestCompression("context_compressed", "Repo-Agent", 200))

	if m.currentRun().AgentName != "Repo-Agent" {
		t.Errorf("agent 切换后 AgentName 期望 Repo-Agent，实际 %s", m.currentRun().AgentName)
	}
	if m.currentRun().ContextTokens != 200 {
		t.Errorf("agent 切换后 ContextTokens 期望200，实际%d", m.currentRun().ContextTokens)
	}
	if m.currentRun().InputTokens != 0 {
		t.Errorf("agent 切换应重置 InputTokens 期望0，实际%d", m.currentRun().InputTokens)
	}
}

// TestContextTokens_EmergencyCompressedReachable 验证 context_emergency_compressed 事件
// 可到达 TUI 事件处理：ContextTokens 更新、渲染缓存失效，且事件循环仍存活
func TestContextTokens_EmergencyCompressedReachable(t *testing.T) {
	m := newTestModel()
	m.tokenUsagePerAgent = make(map[string]*AgentTokenUsage)

	m = feedEvent(m, ctxTestCompression("context_emergency_compressed", "Director", 250))

	if m.currentRun().ContextTokens != 250 {
		t.Errorf("emergency 压缩后 ContextTokens 期望250，实际%d", m.currentRun().ContextTokens)
	}
	if m.tokenDashboardValid {
		t.Error("写入 ContextTokens 应失效渲染缓存（tokenDashboardValid 期望 false）")
	}
	if len(m.timelineEntries) == 0 {
		t.Error("emergency 压缩应产生 timeline 条目")
	}

	// 随后再发一条 ai_response，确认事件循环未被 emergency 分支的 return 打断
	m = feedEvent(m, ctxTestAiResponse("Director", 400))
	if m.currentRun().ContextTokens != 400 {
		t.Errorf("emergency 后 ai_response ContextTokens 期望400，实际%d", m.currentRun().ContextTokens)
	}
	if m.totalInputTokens != 400 {
		t.Errorf("emergency 后事件循环应存活：totalInputTokens 期望400，实际%d", m.totalInputTokens)
	}
}

// TestContextTokens_EmptyFromNormalized 验证压缩事件 From 为空时归一化为 "Unknown"
func TestContextTokens_EmptyFromNormalized(t *testing.T) {
	m := newTestModel()
	m.tokenUsagePerAgent = make(map[string]*AgentTokenUsage)

	m = feedEvent(m, ctxTestCompression("context_compressed", "", 300))

	if m.currentRun().AgentName != "Unknown" {
		t.Errorf("空 From 应归一化为 Unknown，实际 %s", m.currentRun().AgentName)
	}
	if m.currentRun().ContextTokens != 300 {
		t.Errorf("ContextTokens 期望300，实际%d", m.currentRun().ContextTokens)
	}
}

// TestContextTokens_ZeroUsageGuard 验证全零 usage 的 ai_response 不会清零 ContextTokens
func TestContextTokens_ZeroUsageGuard(t *testing.T) {
	m := newTestModel()
	m.tokenUsagePerAgent = make(map[string]*AgentTokenUsage)

	m = feedEvent(m, ctxTestAiResponse("Director", 500))
	if m.currentRun().ContextTokens != 500 {
		t.Fatalf("前置 ai_response 后 ContextTokens 期望500，实际%d", m.currentRun().ContextTokens)
	}

	// provider 错误路径：全零 usage（无 total_input_tokens 字段，回退公式也为 0）
	zeroEvent := &messaging.MessageEvent{
		Type:      "ai_response",
		From:      "Director",
		Content:   "x",
		Timestamp: time.Now(),
		Metadata: map[string]interface{}{
			"usage": map[string]interface{}{
				"prompt_tokens":      0,
				"completion_tokens":  0,
				"total_tokens":       0,
				"total_input_tokens": 0,
			},
		},
	}
	m = feedEvent(m, zeroEvent)

	if m.currentRun().ContextTokens != 500 {
		t.Errorf("全零 usage 不应清零面板：ContextTokens 期望仍为500，实际%d", m.currentRun().ContextTokens)
	}
}

// TestTokenPanel_CollapsedEmptyWhenNoUsage 验证完全没有用量时不渲染任何行
func TestTokenPanel_CollapsedEmptyWhenNoUsage(t *testing.T) {
	m := newTestModel()
	m.setRun(AgentRunTokens{AgentName: "Director"})

	if rows := buildTokenPanel(m.tokenSnapshot(), tokenPanelSpec{innerWidth: 90, collapsed: true}); rows != nil {
		t.Errorf("无用量时折叠面板应返回 nil，实际 %q", rows)
	}
}

// TestTokenPanel_CollapsedOneLine 验证折叠形态恰好 1 行，并带上活跃 agent、Σ 与 ⛁ 上下文。
func TestTokenPanel_CollapsedOneLine(t *testing.T) {
	m := newTestModel()
	m.totalInputTokens = 1000
	m.outputTokens = 200
	m.contextWindow = 200000
	m.tokenUsagePerAgent = map[string]*AgentTokenUsage{
		"Director": {AgentName: "Director", TotalInputTokens: 1000, InputTokens: 1000, OutputTokens: 200},
	}
	m.setRun(AgentRunTokens{AgentName: "Director", TotalInputTokens: 1000, InputTokens: 1000, OutputTokens: 200, ContextTokens: 5000})

	rows := buildTokenPanel(m.tokenSnapshot(), tokenPanelSpec{
		innerWidth: 90,
		collapsed:  true,
		keycap:     dashKeycap("alt+t", "展开"),
	})
	if len(rows) != 1 {
		t.Fatalf("折叠形态应恰好 1 行，实际 %d 行", len(rows))
	}
	out := stripANSI(rows[0])
	for _, want := range []string{"[Director]", "▸1.2k", "⛁", "5.0k"} {
		if !strings.Contains(out, want) {
			t.Errorf("折叠行缺少 %q：%q", want, out)
		}
	}
	if strings.Contains(out, "Ctx:") {
		t.Errorf("不应再出现旧样式 Ctx: 段：%q", out)
	}
	if w := lipgloss.Width(rows[0]); w > 90 {
		t.Errorf("折叠行宽度 %d 超过 inner 90", w)
	}
}

// TestTokenPanel_CtxLineAppearsAndDisappears 验证展开面板的 ⛁ 行随 ContextTokens 出现/消失。
func TestTokenPanel_CtxLineAppearsAndDisappears(t *testing.T) {
	m := newTestModel()
	m.tokenUsagePerAgent = map[string]*AgentTokenUsage{}
	m.inputTokens = 100
	m.outputTokens = 50
	m.setRun(AgentRunTokens{AgentName: "Director", InputTokens: 100, OutputTokens: 50, ContextTokens: 500})

	expanded := stripANSI(strings.Join(buildTokenPanel(m.tokenSnapshot(), tokenPanelSpec{innerWidth: 46}), "\n"))
	if !strings.Contains(expanded, "⛁") {
		t.Errorf("ContextTokens>0 时应出现 ⛁ 行，实际 %q", expanded)
	}

	m.currentRun().ContextTokens = 0
	expanded = stripANSI(strings.Join(buildTokenPanel(m.tokenSnapshot(), tokenPanelSpec{innerWidth: 46}), "\n"))
	if strings.Contains(expanded, "⛁") {
		t.Errorf("ContextTokens==0 时不应出现 ⛁ 行，实际 %q", expanded)
	}
}

// TestDashboardCacheKey_IncludesContextTokens 验证 dashboard 缓存 key 纳入 ContextTokens：
// 仅修改 ContextTokens（不走 invalidateFooterCache）时面板必须重新渲染
func TestDashboardCacheKey_IncludesContextTokens(t *testing.T) {
	initLangManager()
	m := newTestModel()
	m.inputTokens = 1000
	m.outputTokens = 200
	m.totalInputTokens = 1000
	m.tokenUsagePerAgent = map[string]*AgentTokenUsage{
		"Director": {AgentName: "Director", InputTokens: 1000, OutputTokens: 200, TotalInputTokens: 1000},
	}
	m.setRun(AgentRunTokens{
		AgentName:        "Director",
		InputTokens:      1000,
		OutputTokens:     200,
		TotalInputTokens: 1000,
		ContextTokens:    1000,
	})

	const w, h = dashboardPanelWidth, 30
	first := m.renderDashboard(w, h)

	// 仅修改 ContextTokens，不调用 invalidateFooterCache ——
	// 若 key 未纳入 ctx 值，第二次渲染将命中旧缓存并返回与 first 相同的结果
	m.currentRun().ContextTokens = 5000
	second := m.renderDashboard(w, h)

	if first == second {
		t.Error("ContextTokens 变化后 dashboard 应重新渲染（缓存 key 需纳入 ctx 值）")
	}
	if secondCtx := stripANSI(second); !strings.Contains(secondCtx, "⛁") || !strings.Contains(secondCtx, "5.0k") {
		t.Errorf("第二次渲染应显示更新后的 ⛁ 行 · 5.0k，实际 %q", secondCtx)
	}
}

// TestDashboardRunTokensInvalidateCache 验证本轮 RUN 变化同样会让侧栏重渲染
// —— RUN 进缓存键是新增列能被看到的前提。
func TestDashboardRunTokensInvalidateCache(t *testing.T) {
	initLangManager()
	m := newTestModel()
	m.inputTokens = 1000
	m.outputTokens = 200
	m.totalInputTokens = 1200
	m.tokenUsagePerAgent = map[string]*AgentTokenUsage{
		"Director": {AgentName: "Director", InputTokens: 1000, OutputTokens: 200, TotalInputTokens: 1200},
	}
	m.setRun(AgentRunTokens{AgentName: "Director", InputTokens: 100, OutputTokens: 20, TotalInputTokens: 120})

	const w, h = dashboardPanelWidth, 30
	before := stripANSI(m.renderDashboard(w, h))

	// 只改 ContextTokens 之外的本轮输入：RUN 取的是归一化输入，所以动 TotalInputTokens
	m.currentRun().TotalInputTokens = 5000
	after := stripANSI(m.renderDashboard(w, h))

	if before == after {
		t.Error("RUN 变化后 dashboard 应重新渲染（缓存 key 需纳入 run 值）")
	}
	if !strings.Contains(after, "5.0k ▸") {
		t.Errorf("agent 行的 RUN 列应显示 5.0k，实际\n----\n%s----", after)
	}
}

// TestDashboardCtx_DoesNotExceedPanelWidth 验证极大 ContextTokens 下 dashboard
// 所有行可见宽度恰好等于面板宽度（超宽内容应被截断，不得撑破边框）
func TestDashboardCtx_DoesNotExceedPanelWidth(t *testing.T) {
	initLangManager()
	m := newTestModel()
	m.inputTokens = 5000000
	m.outputTokens = 100000
	m.totalInputTokens = 5150000
	m.contextWindow = 8000000
	m.tokenUsagePerAgent = map[string]*AgentTokenUsage{
		"Director": {
			AgentName:                "Director",
			InputTokens:              5000000,
			OutputTokens:             100000,
			CacheReadInputTokens:     100000,
			CacheCreationInputTokens: 50000,
			TotalInputTokens:         5150000,
		},
	}
	m.setRun(AgentRunTokens{
		AgentName:        "Director",
		InputTokens:      5000000,
		OutputTokens:     100000,
		TotalInputTokens: 5150000,
		ContextTokens:    9999999,
	})

	const w, h = dashboardPanelWidth, 30
	result := m.renderDashboard(w, h)
	lines := strings.Split(result, "\n")
	if len(lines) != h {
		t.Fatalf("期望 %d 行，实际 %d 行", h, len(lines))
	}
	for i, line := range lines {
		if lw := lipgloss.Width(line); lw != w {
			t.Errorf("line %d: 期望宽度 %d，实际 %d（content=%q）", i, w, lw, stripANSI(line))
		}
	}
	resultCtx := stripANSI(result)
	if !strings.Contains(resultCtx, "⛁") || !strings.Contains(resultCtx, "10.00m") {
		t.Errorf("面板应显示 ⛁ 行与格式化后的 9999999 → 10.00m，实际 %q", resultCtx)
	}
}

// TestTokenCtxMeterRow_BarStyle 有窗口上限时输出进度条形态，值与上限都经 formatToken
func TestTokenCtxMeterRow_BarStyle(t *testing.T) {
	snap := tokenSnapshot{totalIn: 1000, totalOut: 100, ctxTokens: 45000, ctxWindow: 200000}
	line := tokenCtxMeterRow(snap, 46, tokenMeterCells)
	out := stripANSI(line)
	for _, want := range []string{"⛁", "▰", "▱", "45.0k", "200.0k", "%"} {
		if !strings.Contains(out, want) {
			t.Errorf("⛁ 行缺少 %q：%q", want, out)
		}
	}
	if w := lipgloss.Width(line); w != 46 {
		t.Errorf("⛁ 行宽度 = %d, want 46", w)
	}
}

// TestTokenCtxMeterRow_NoWindow 窗口未知（<=0）时不显示分母，也不会因为缺上限而报错
func TestTokenCtxMeterRow_NoWindow(t *testing.T) {
	snap := tokenSnapshot{totalIn: 1000, totalOut: 100, ctxTokens: 45000}
	out := stripANSI(tokenCtxMeterRow(snap, 46, tokenMeterCells))
	if strings.Contains(out, " / ") {
		t.Errorf("窗口未知时不应出现分母：%q", out)
	}
	if !strings.Contains(out, "45.0k") {
		t.Errorf("窗口未知时仍应显示当前值：%q", out)
	}
}

// TestTokenCtxMeterRow_Empty tokens<=0 时返回空串（调用方跳过该行）
func TestTokenCtxMeterRow_Empty(t *testing.T) {
	if out := tokenCtxMeterRow(tokenSnapshot{ctxTokens: 0}, 46, tokenMeterCells); out != "" {
		t.Errorf("tokens=0 应返回空串，实际 %q", out)
	}
	if out := tokenCtxMeterRow(tokenSnapshot{ctxTokens: -1}, 46, tokenMeterCells); out != "" {
		t.Errorf("tokens<0 应返回空串，实际 %q", out)
	}
}

// TestContextWindowFromCfg_CatalogFallback provider 未配置 context_window 时回退到
// 内嵌模型目录（models.json）；两处都查不到才返回 0（调用方不渲染进度条）。
func TestContextWindowFromCfg_CatalogFallback(t *testing.T) {
	loadCatalog := func(t *testing.T, content string) {
		t.Helper()
		fsys := fstest.MapFS{models.CatalogPath: &fstest.MapFile{Data: []byte(content)}}
		if err := models.Load(fsys); err != nil {
			t.Fatalf("models.Load() error = %v", err)
		}
	}
	loadCatalog(t, `{"deepseek/deepseek-v3.2": {"limit": {"context": 128000}}}`)
	// 还原为空目录，避免残留状态影响同包其它测试
	t.Cleanup(func() { loadCatalog(t, "{}") })

	cfg := &config.Config{Global: config.TopLevelConfig{LLM: &config.GlobalLLMConfig{
		Providers: map[string]config.ProviderConfig{
			"catalog_only":  {Model: "deepseek-ai/DeepSeek-V3.2"},
			"explicit":      {Model: "deepseek-ai/DeepSeek-V3.2", ContextWindow: 500000},
			"unknown_model": {Model: "acme/private-model"},
		},
	}}}

	tests := []struct {
		name     string
		provider string
		want     int
	}{
		{"未配置 context_window 时取目录值", "catalog_only", 128000},
		{"显式 context_window 优先于目录", "explicit", 500000},
		{"目录未收录的模型返回 0", "unknown_model", 0},
		{"provider 不存在返回 0", "missing_provider", 0},
		{"provider 名为空返回 0", "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := contextWindowFromCfg(cfg, tt.provider); got != tt.want {
				t.Errorf("contextWindowFromCfg(cfg, %q) = %d, want %d", tt.provider, got, tt.want)
			}
		})
	}

	if got := contextWindowFromCfg(nil, "catalog_only"); got != 0 {
		t.Errorf("contextWindowFromCfg(nil, ...) = %d, want 0", got)
	}
}
