package agents

import (
	"context"
	"strings"
	"testing"

	"yagent/internal/config"
	"yagent/internal/llm"
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
			Enable:                      true,
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
