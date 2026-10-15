package agents

import (
	"context"
	"strings"
	"testing"

	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/thinklink"
	"yagent/internal/tools"
)

// 本包的测试二进制不调用 models.Load，目录未加载时 ContextWindow 恒为 0，
// 因此 ResolveThreshold 会直接采用 ContextCompressionThreshold —— 下面的极小阈值可用。

func ultimateEvents(t *testing.T, pub *spyPublisher) []map[string]interface{} {
	t.Helper()
	var out []map[string]interface{}
	for _, e := range pub.snapshot() {
		if e.evType != "context_ultimate_compressed" {
			continue
		}
		m, ok := e.content.(map[string]interface{})
		if !ok {
			t.Fatalf("context_ultimate_compressed payload type = %T", e.content)
		}
		out = append(out, m)
	}
	return out
}

func hasEvent(pub *spyPublisher, evType string) bool {
	for _, e := range pub.snapshot() {
		if e.evType == evType {
			return true
		}
	}
	return false
}

// textOnlyMock 返回一步就收尾的引擎（StopOnFinish 下文本回复即结束）。
func textOnlyMock(text string) *mockLLM {
	return &mockLLM{responses: []*llm.Response{{Choices: []llm.Choice{{Content: text}}}}}
}

// TestRunAgentLoop_CompressionEventFeedsTUIPanel 把生产端与展示端钉在一条真实链上。
//
// 这条测试的存在理由是一次真 bug：TUI 按 `[]interface{}` 断言 truncated_tools，
// 而两个发布方送的是 `[]map[string]interface{}`（总线在 TUI 这条路上不做 JSON 往返），
// 于是 ⧉ 面板的 cut: 榜与 ctrl+l 的截断明细在真机上永远为空 —— 而两侧各自的单测都是绿的。
// 只有让**真实的 RunAgentLoop** 送出事件，再检查它的具体 Go 类型与 rollout 字段，
// 这类"接口对得上、形状对不上"的错误才无处藏身。
func TestRunAgentLoop_CompressionEventFeedsTUIPanel(t *testing.T) {
	pub := &spyPublisher{}
	bigResult := strings.Repeat("observed output line that is quite long. ", 120)
	mock := &mockLLM{responses: []*llm.Response{
		{Choices: []llm.Choice{{ToolCalls: []llm.ToolCall{{
			ID:       "call_trunc_1",
			Type:     "function",
			Function: llm.FunctionCall{Name: "probe_tool", Arguments: `{}`},
		}}}}},
		{Choices: []llm.Choice{{Content: "done"}}},
	}}

	w, err := memory.NewRolloutWriter("compress-contract-agent", "task-compress-contract", "compress-contract-test")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	path := w.FilePath()
	ctx := memory.WithRolloutWriter(context.Background(), w)

	cfg := ExecutorConfig{
		SystemPrompt:                "You are a test agent.",
		UserInput:                   "read something big then report",
		Adapters:                    []*tools.Adapter{newFastAdapter("probe_tool", "probe", bigResult)},
		LLM:                         mock,
		MaxSteps:                    2,
		StopOnFinish:                true,
		AgentName:                   "compress-contract-agent",
		Publisher:                   pub,
		EnableContextCompression:    true,
		ContextCompressionThreshold: 60,
		ToolResultKeepTokens:        8,
	}
	if _, err := RunAgentLoop(ctx, cfg); err != nil {
		t.Fatalf("RunAgentLoop: %v", err)
	}
	w.Close()

	var content map[string]interface{}
	for _, e := range pub.snapshot() {
		if e.evType != "context_compressed" {
			continue
		}
		m, ok := e.content.(map[string]interface{})
		if !ok {
			t.Fatalf("context_compressed payload type = %T", e.content)
		}
		content = m
		break
	}
	if content == nil {
		t.Fatal("第一档没被触发：夹具的阈值或工具结果体积需要调整")
	}

	// 1) 发布方送出的具体类型，必须就是 TUI 的 compressionToolEntries 吃得到的形状
	truncated, ok := content["truncated_tools"].([]map[string]interface{})
	if !ok {
		t.Fatalf("truncated_tools = %T, want []map[string]interface{}", content["truncated_tools"])
	}
	if len(truncated) == 0 {
		t.Fatal("truncated_tools 为空，cut: 榜不会有数据")
	}
	for i, tm := range truncated {
		if name, _ := tm["tool_name"].(string); name != "probe_tool" {
			t.Errorf("第 %d 条 tool_name = %T %v, want probe_tool", i, tm["tool_name"], tm["tool_name"])
		}
		if omitted, ok := tm["omitted_tokens"].(int); !ok || omitted <= 0 {
			t.Errorf("第 %d 条 omitted_tokens = %T %v, want 正整数", i, tm["omitted_tokens"], tm["omitted_tokens"])
		}
	}
	for _, key := range []string{"original_tokens", "compressed_tokens", "saved_tokens", "truncated_count"} {
		if v, ok := content[key].(int); !ok {
			t.Errorf("%s = %T, want int（parseEventInt 能处理，但键名/类型就是这里的契约）", key, content[key])
		} else if key == "saved_tokens" && v <= 0 {
			t.Errorf("saved_tokens = %d, want > 0", v)
		}
	}

	// 2) rollout 必须带 kind/saved_tokens —— 否则 /resume 后 ⧉ 段只能靠 Summary 前缀猜
	_, info, err := memory.ReadRolloutMemory(path, memory.DefaultRolloutReadOptions())
	if err != nil {
		t.Fatalf("ReadRolloutMemory: %v", err)
	}
	if len(info.Compressions) == 0 {
		t.Fatal("compacted 记录没被读取端收集（/resume 后 ⧉ 段会是空的）")
	}
	rec := info.Compressions[0]
	if rec.Kind != memory.CompactionKindRule {
		t.Errorf("kind = %q, want %q", rec.Kind, memory.CompactionKindRule)
	}
	if rec.Saved <= 0 {
		t.Errorf("saved = %d, want > 0", rec.Saved)
	}
	if rec.TokensBefore <= rec.TokensAfter {
		t.Errorf("tokens %d → %d，压缩前的规模没记进去", rec.TokensBefore, rec.TokensAfter)
	}
}

// TestRunAgentLoop_UltimateCompressionNeedsFlagAndStore 第三档的两个条件缺一不可：
// 只给 store 会让 [enhanced_commander] enable_ultimate_compression = false 变成空头承诺，
// 只给 flag 则没有重建原料。
func TestRunAgentLoop_UltimateCompressionNeedsFlagAndStore(t *testing.T) {
	cases := []struct {
		name       string
		flag       bool
		withStore  bool
		wantResult bool
	}{
		{"flag+store 才可达", true, true, true},
		{"flag=false 时不可达", false, true, false},
		{"无 store 时不可达", true, false, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pub := &spyPublisher{}
			mock := textOnlyMock("done")

			cfg := ExecutorConfig{
				// 系统提示必须大到第二档也压不进阈值：第二档只截 user 消息，
				// 截完仍超限才会把第三档的条件逼出来。
				SystemPrompt:                strings.Repeat("system rule that must stay. ", 60),
				UserInput:                   strings.Repeat("long delegated task description. ", 80),
				Adapters:                    []*tools.Adapter{newFastAdapter("probe_tool", "probe", "ok")},
				LLM:                         mock,
				MaxSteps:                    1,
				StopOnFinish:                true,
				AgentName:                   "compression-test",
				Publisher:                   pub,
				EnableContextCompression:    true,
				ContextCompressionThreshold: 8,
				ToolResultKeepTokens:        200,
				EnableUltimateCompression:   tc.flag,
			}
			if tc.withStore {
				cfg.UltimateThinkLink = thinklink.NewStore(0)
			}

			if _, err := RunAgentLoop(context.Background(), cfg); err != nil {
				t.Fatalf("RunAgentLoop: %v", err)
			}

			// 第二档应当始终可达（它只依赖 EnableContextCompression）。
			if !hasEvent(pub, "context_emergency_compressed") {
				t.Fatal("expected emergency compression to run; threshold too high for the fixture?")
			}
			got := len(ultimateEvents(t, pub))
			if tc.wantResult && got == 0 {
				t.Error("ultimate compression did not fire although flag+store are both set")
			}
			if !tc.wantResult && got != 0 {
				t.Errorf("ultimate compression fired %d times but must not", got)
			}
			if mock.callCount != 1 {
				t.Errorf("main LLM calls = %d, want 1", mock.callCount)
			}
		})
	}
}

// TestRunAgentLoop_RecordsThoughtPlanIntoThinkLink 主循环必须把 assistant 的 <thought>
// 块写进注入的 store 并发 thinklink_entry —— 这是第三档唯一的重建原料，
// 也是 TUI thinklink 面板的数据来源。
func TestRunAgentLoop_RecordsThoughtPlanIntoThinkLink(t *testing.T) {
	pub := &spyPublisher{}
	store := thinklink.NewStore(0)
	mock := &mockLLM{responses: []*llm.Response{
		{Choices: []llm.Choice{{
			Content: "<thought>\n" +
				"goal: verify thinklink recording\n" +
				"step: none yet\n" +
				"reason: need one action to probe the store\n" +
				"plan:\n- [>] call probe tool\n- [ ] report\n" +
				"</thought>",
			ToolCalls: []llm.ToolCall{{
				ID:       "call_thought_1",
				Type:     "function",
				Function: llm.FunctionCall{Name: "probe_tool", Arguments: `{}`},
			}},
		}}},
		{Choices: []llm.Choice{{Content: "done"}}},
	}}

	cfg := ExecutorConfig{
		SystemPrompt:      "You are a test agent.",
		UserInput:         "record my thoughts",
		Adapters:          []*tools.Adapter{newFastAdapter("probe_tool", "probe", "ok")},
		LLM:               mock,
		MaxSteps:          2,
		StopOnFinish:      true,
		AgentName:         "thinklink-test",
		Publisher:         pub,
		UltimateThinkLink: store,
	}

	if _, err := RunAgentLoop(context.Background(), cfg); err != nil {
		t.Fatalf("RunAgentLoop: %v", err)
	}
	if got := store.Count(thinklink.KindThoughtPlan); got != 1 {
		t.Errorf("store T&P count = %d, want 1", got)
	}
	if !hasEvent(pub, "thinklink_entry") {
		t.Error("thinklink_entry must be published so the TUI panel can render it")
	}
}

// TestBrowserAgent_UltimateCompressionIsPerDelegation BrowserAgent 实例被 Director 与
// Coding 的 delegate_browser 共用，而 RebuildPrompt 会保留全部用户输入：
// store 若跨委派复用，第二次终极压缩会把第一次的任务一起塞回上下文。
func TestBrowserAgent_UltimateCompressionIsPerDelegation(t *testing.T) {
	pub := &spyPublisher{}
	mock := &mockLLM{responses: []*llm.Response{
		{Choices: []llm.Choice{{Content: "first done"}}},
		{Choices: []llm.Choice{{Content: "second done"}}},
	}}

	agent := NewBrowserAgent(BrowserAgentParams{
		Publisher:  pub,
		Env:        stubEnv{},
		Flow:       stubFlow{},
		Browser:    &stubController{ok: true},
		BrowserCfg: config.DefaultBrowserConfig(),
		EnhancedCommanderCfg: config.EnhancedCommanderConfig{
			EnableContextCompression:    true,
			ContextCompressionThreshold: 8,
			ToolResultKeepTokens:        200,
			EnableUltimateCompression:   true,
		},
		LLM:      mock,
		MaxSteps: 1,
	})

	if _, err := agent.Run(context.Background(), strings.Repeat("first delegated browser task. ", 80)); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if _, err := agent.Run(context.Background(), strings.Repeat("second delegated browser task. ", 80)); err != nil {
		t.Fatalf("second Run: %v", err)
	}

	evs := ultimateEvents(t, pub)
	if len(evs) != 2 {
		t.Fatalf("ultimate events = %d, want 2 (one per delegation)", len(evs))
	}
	for i, e := range evs {
		if got := e["user_inputs"]; got != 1 {
			t.Errorf("delegation %d rebuilt from %v user inputs, want 1 (store must not leak across delegations)", i+1, got)
		}
	}
}
