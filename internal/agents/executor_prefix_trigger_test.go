package agents

import (
	"context"
	"strings"
	"testing"

	"yagent/internal/llm"
	"yagent/internal/thinklink"
	"yagent/internal/tokenutil"
	"yagent/internal/tools"
)

// 触发判定的计量口径必须真的接进两个循环。
//
// 为什么不能只测 compression.Prefix：它自己对了不代表线对了 —— 四个判定点原来是抄在
// 循环里的（`EstimateMessagesTokens(messages) > threshold` 四份），漏改其中一处的表现是
// "子 Agent 按真实计量触发、Director 还按本地估算"，两边各自单测都是绿的。
// 所以这里用真实 RunAgentLoop / Planner.Run 各跑一遍。
//
// 夹具的设计约束：本地口径**不超**阈值、计量口径**超**阈值。
// 于是"第一档触发了"这件事只能来自计量口径，测试才有区分度。

func prefixTriggerFixture(toolLocal int) (bigResult string, reported int64, threshold int) {
	reported = 4000
	bigResult = strings.Repeat("observed output line that is quite long. ", toolLocal/10)
	threshold = int(reported) + toolLocal/2
	return
}

func compressionEvent(t *testing.T, pub *spyPublisher) map[string]interface{} {
	t.Helper()
	for _, e := range pub.snapshot() {
		if e.evType != "context_compressed" {
			continue
		}
		m, ok := e.content.(map[string]interface{})
		if !ok {
			t.Fatalf("context_compressed payload type = %T", e.content)
		}
		return m
	}
	return nil
}

// TestRunAgentLoop_TriggerUsesProviderMeasurement 子 Agent 循环：第一步响应带着
// provider 报的输入规模，第二步的压缩判定必须用那个数，而不是本地估算。
// 顺带钉住 stats 仍是本地口径（TUI 的 ⧉ 面板按 saved/original 算百分比，
// 把 4000 的基数混进 original_tokens 会让"省了多少"和"一共多少"不同源）。
func TestRunAgentLoop_TriggerUsesProviderMeasurement(t *testing.T) {
	bigResult, reported, threshold := prefixTriggerFixture(3000)
	toolLocal := tokenutil.EstimateTokens(bigResult)
	if toolLocal < 2000 {
		t.Fatalf("工具结果只有 %d token, want ≥ 2000（截断量不够就区分不了两个口径）", toolLocal)
	}

	pub := &spyPublisher{}
	mock := &mockLLM{responses: []*llm.Response{
		{
			Choices: []llm.Choice{{ToolCalls: []llm.ToolCall{{
				ID: "call_p1", Type: "function",
				Function: llm.FunctionCall{Name: "probe_tool", Arguments: `{}`},
			}}}},
			Usage: &llm.TokenUsage{PromptTokens: reported, TotalInputTokens: reported},
		},
		{Choices: []llm.Choice{{Content: "done"}}},
	}}

	cfg := ExecutorConfig{
		SystemPrompt:                "You are a test agent.",
		UserInput:                   "read something big then report",
		Surface:                     tools.NewSurfaceFromAdapters("test-agent", newFastAdapter("probe_tool", "probe", bigResult)),
		LLM:                         mock,
		MaxSteps:                    2,
		StopOnFinish:                true,
		AgentName:                   "prefix-trigger-test",
		Publisher:                   pub,
		EnableContextCompression:    true,
		ContextCompressionThreshold: threshold,
		ToolResultKeepTokens:        8,
	}
	if _, err := RunAgentLoop(context.Background(), cfg); err != nil {
		t.Fatalf("RunAgentLoop: %v", err)
	}
	if mock.callCount != 2 {
		t.Errorf("主循环 LLM 调用 = %d, want 2（多了说明第二/三档被拖进来了）", mock.callCount)
	}

	content := compressionEvent(t, pub)
	if content == nil {
		t.Fatalf("第一档没触发：本地口径 ≈ %d token、阈值 %d、计量口径 ≈ %d —— 只有后者超阈值",
			toolLocal, threshold, int(reported)+toolLocal)
	}
	if hasEvent(pub, "context_emergency_compressed") {
		t.Error("第二档被触发了：第一档压完应当已经落回阈值之下")
	}
	orig, ok := content["original_tokens"].(int)
	if !ok {
		t.Fatalf("original_tokens = %T, want int", content["original_tokens"])
	}
	if orig >= int(reported) {
		t.Errorf("stats.OriginalTokens = %d, want 本地口径（< 计量基数 %d）—— provider 基数混进了面板数字",
			orig, reported)
	}
	if saved, _ := content["saved_tokens"].(int); saved <= 0 {
		t.Errorf("saved_tokens = %v, want > 0", content["saved_tokens"])
	}
}

// TestRunAgentLoop_MeasurementInvalidatedAfterRewrite 第二档把列表整段重写成
// [system, one user] 两条（emergency.go:216-223），而紧跟其后的第三档判定读的是同一个
// 计量变量。不作废的话，那个基数会被贴到一份刚重建出来的短列表上 ——
// usable 的"Len 越界"兜底挡不住它（列表确实还有 Len 条），
// 表现是每次第二档之后都白跑一次第三档重建，上下文被连压两遍。
func TestRunAgentLoop_MeasurementInvalidatedAfterRewrite(t *testing.T) {
	bigResult, reported, _ := prefixTriggerFixture(1500)
	pub := &spyPublisher{}

	mock := &mockLLM{responses: []*llm.Response{
		{
			Choices: []llm.Choice{{ToolCalls: []llm.ToolCall{{
				ID: "call_r1", Type: "function",
				Function: llm.FunctionCall{Name: "probe_tool", Arguments: `{}`},
			}}}},
			Usage: &llm.TokenUsage{PromptTokens: reported, TotalInputTokens: reported},
		},
		{Choices: []llm.Choice{{Content: "done"}}},
	}}

	cfg := ExecutorConfig{
		SystemPrompt:                "You are a test agent.",
		UserInput:                   "read big things",
		Surface:                     tools.NewSurfaceFromAdapters("test-agent", newFastAdapter("probe_tool", "probe", bigResult)),
		LLM:                         mock,
		MaxSteps:                    2,
		StopOnFinish:                true,
		AgentName:                   "measurement-invalidate-test",
		Publisher:                   pub,
		EnableContextCompression:    true,
		ContextCompressionThreshold: 2000,
		ToolResultKeepTokens:        8,
		EnableUltimateCompression:   true,
		UltimateThinkLink:           thinklink.NewStore(0),
	}
	if _, err := RunAgentLoop(context.Background(), cfg); err != nil {
		t.Fatalf("RunAgentLoop: %v", err)
	}

	if !hasEvent(pub, "context_emergency_compressed") {
		t.Fatalf("第二档没触发，夹具不成立（基数 %d、阈值 2000）", reported)
	}
	if n := len(ultimateEvents(t, pub)); n != 0 {
		t.Errorf("第三档触发 %d 次, want 0 —— 刚被第二档重写的列表又贴上了作废的计量基数", n)
	}
	if mock.callCount != 2 {
		t.Errorf("主循环 LLM 调用 = %d, want 2", mock.callCount)
	}
}

// TestRunAgentLoop_NoMeasurementFallsBackToLocalEstimate 网关没报 usage 时必须回落到
// 本地估算（= 计量口径出现之前的行为）。这条与上一条是一对：只有"有计量才更激进"，
// 没计量时不得凭空触发 —— 回落条件写反的表现是没计量的网关每步都在截断。
func TestRunAgentLoop_NoMeasurementFallsBackToLocalEstimate(t *testing.T) {
	bigResult, reported, threshold := prefixTriggerFixture(3000)
	pub := &spyPublisher{}
	mock := &mockLLM{responses: []*llm.Response{
		{Choices: []llm.Choice{{ToolCalls: []llm.ToolCall{{
			ID: "call_p2", Type: "function",
			Function: llm.FunctionCall{Name: "probe_tool", Arguments: `{}`},
		}}}}}, // Usage 为 nil
		{Choices: []llm.Choice{{Content: "done"}}},
	}}

	cfg := ExecutorConfig{
		SystemPrompt:                "You are a test agent.",
		UserInput:                   "read something big then report",
		Surface:                     tools.NewSurfaceFromAdapters("test-agent", newFastAdapter("probe_tool", "probe", bigResult)),
		LLM:                         mock,
		MaxSteps:                    2,
		StopOnFinish:                true,
		AgentName:                   "no-measurement-test",
		Publisher:                   pub,
		EnableContextCompression:    true,
		ContextCompressionThreshold: threshold,
		ToolResultKeepTokens:        8,
	}
	if _, err := RunAgentLoop(context.Background(), cfg); err != nil {
		t.Fatalf("RunAgentLoop: %v", err)
	}
	if content := compressionEvent(t, pub); content != nil {
		t.Errorf("没报 usage 却触发了第一档：original_tokens = %v, want 回落到本地口径（阈值 %d，基数 %d 不该出现）",
			content["original_tokens"], threshold, reported)
	}
}
