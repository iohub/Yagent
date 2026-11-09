package agents

import (
	"context"
	"testing"

	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/tools"
)

// ADR-13 的补偿控制：跑完一次带 agent_exit 的委派后，三个指标必须都是非零值。
// 只断言"字段存在"是不够的——加了字段没人填正好是这个坑的形状。
func TestRunAgentLoop_ReportsExecutionMetrics(t *testing.T) {
	exitAdapter := newFastAdapter("agent_exit", "exit", `{"finished":true,"reason":"done"}`)
	resp := &llm.Response{
		Choices: []llm.Choice{{
			ToolCalls: []llm.ToolCall{{
				ID:   "call_exit",
				Type: "function",
				Function: llm.FunctionCall{
					Name:      "agent_exit",
					Arguments: `{"reason":"done"}`,
				},
			}},
		}},
		Usage: &llm.TokenUsage{PromptTokens: 120, CompletionTokens: 30, TotalTokens: 150, TotalInputTokens: 120},
	}
	mock := &mockLLM{responses: []*llm.Response{resp, resp}}

	cfg := ExecutorConfig{
		SystemPrompt: "You are a test agent.",
		UserInput:    "do it",
		Surface:      tools.NewSurfaceFromAdapters("metrics-agent", exitAdapter),
		LLM:          mock,
		MaxSteps:     5,
		StopOnFinish: true,
		AgentName:    "metrics-agent",
	}

	result, err := RunAgentLoop(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunAgentLoop: %v", err)
	}
	if result.Steps <= 0 {
		t.Errorf("Steps = %d, want > 0", result.Steps)
	}
	if result.Usage.TotalTokens <= 0 {
		t.Errorf("Usage.TotalTokens = %d, want > 0", result.Usage.TotalTokens)
	}
	if result.Usage.PromptTokens != 120*int64(result.Steps) {
		// 跨步累加：每步都该计入，而不是只留最后一步。
		t.Errorf("Usage.PromptTokens = %d, want %d（跨步累加）", result.Usage.PromptTokens, 120*int64(result.Steps))
	}
	if result.StopKind != StopKindAgentExit {
		t.Errorf("StopKind = %q, want %q", result.StopKind, StopKindAgentExit)
	}

	// AgentResult 必须透传（经 FinalizeResult 的包装，所有返回分支一致）。
	ar := FinalizeResultFull("metrics-agent", "do it", result)
	if ar.Steps != result.Steps || ar.Usage.TotalTokens != result.Usage.TotalTokens || ar.StopKind != result.StopKind {
		t.Errorf("AgentResult 指标没透传: Steps=%d Usage=%d Stop=%q", ar.Steps, ar.Usage.TotalTokens, ar.StopKind)
	}
}

// 纯文本收尾的路径也要带指标（四个返回分支里最容易漏的那个）。
func TestRunAgentLoop_PlainTextCarriesMetrics(t *testing.T) {
	mock := &mockLLM{responses: []*llm.Response{{
		Choices: []llm.Choice{{Content: "这是纯文本交付。"}},
		Usage:   &llm.TokenUsage{PromptTokens: 50, CompletionTokens: 10, TotalTokens: 60},
	}}}
	cfg := ExecutorConfig{
		SystemPrompt: "You are a test agent.",
		UserInput:    "say hi",
		Surface:      tools.NewSurfaceFromAdapters("plain-agent", newFastAdapter("noop", "noop", "ok")),
		LLM:          mock,
		MaxSteps:     3,
		StopOnFinish: true,
		AgentName:    "plain-agent",
	}
	result, err := RunAgentLoop(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunAgentLoop: %v", err)
	}
	if result.StopKind != StopKindPlainTextReply {
		t.Errorf("StopKind = %q, want %q", result.StopKind, StopKindPlainTextReply)
	}
	if result.Steps <= 0 || result.Usage.TotalTokens <= 0 {
		t.Errorf("纯文本收尾也必须带指标: Steps=%d Usage=%d", result.Steps, result.Usage.TotalTokens)
	}
}

// StopKind 词表必须与 rollout 侧同源（ADR-13 要求复用 memory.StopKind*，不另造一套）。
func TestStopKindVocabularyIsSharedWithRollout(t *testing.T) {
	pairs := map[string]string{
		StopKindAgentExit:      memory.StopKindAgentExit,
		StopKindPlainTextReply: memory.StopKindPlainText,
		StopKindMaxSteps:       memory.StopKindMaxSteps,
	}
	for got, want := range pairs {
		if got != want {
			t.Errorf("agents 侧 %q 与 memory 侧 %q 不一致", got, want)
		}
	}
}
