package agents

import (
	"errors"
	"testing"

	"yagent/internal/llm"
)

// 遥测接缝必须在一次委派结束后带着真实指标触发（ADR-6）。
func TestEmitDelegationMetrics_CarriesMetrics(t *testing.T) {
	a := &DirectorAgent{}
	var got DelegationMetrics
	a.SetDelegationMetricsHook(func(m DelegationMetrics) { got = m })

	res := AgentResult{
		Steps:    7,
		Usage:    llm.TokenUsage{PromptTokens: 18400, CompletionTokens: 2100},
		StopKind: StopKindAgentExit,
		Text:     "done",
	}
	a.emitDelegationMetrics("delegate_security_auditor", "call_1", "board#note-3", "", nil, res)

	if got.Agent != "security_auditor" {
		t.Errorf("Agent = %q, want security_auditor（去 delegate_ 前缀）", got.Agent)
	}
	if got.DelegationID != "call_1" {
		t.Errorf("DelegationID = %q", got.DelegationID)
	}
	if got.Steps != 7 || got.TokensIn != 18400 || got.TokensOut != 2100 {
		t.Errorf("指标没透传: %+v", got)
	}
	if got.StopReason != StopKindAgentExit {
		t.Errorf("StopReason = %q", got.StopReason)
	}
	if got.Err {
		t.Error("成功委派不该标 Err")
	}
	if !got.HadResult {
		t.Error("有结果时 HadResult 应为真")
	}
	if got.NoteRef != "board#note-3" {
		t.Errorf("NoteRef = %q，want board#note-3（ADR-6 的 note_ref 要能从委派追到结论）", got.NoteRef)
	}
}

// 唯一失败模式是 panic（去掉 nil 钩子守卫即 nil 解引用）；P9 之前 app 不接线，
// nil 钩子是生产常态，所以这条 fail-safe 必须有。
func TestEmitDelegationMetrics_NilHookIsSafe(t *testing.T) {
	// hygiene:panic-guard
	a := &DirectorAgent{}
	// 没装钩子时不能 panic。
	a.emitDelegationMetrics("delegate_x", "c", "", "", errors.New("boom"), AgentResult{})
}

func TestEmitDelegationMetrics_MarksErrors(t *testing.T) {
	a := &DirectorAgent{}
	var got DelegationMetrics
	a.SetDelegationMetricsHook(func(m DelegationMetrics) { got = m })
	a.emitDelegationMetrics("delegate_x", "c", "", "", errors.New("boom"), AgentResult{StopKind: StopKindLLMError})
	if !got.Err {
		t.Error("失败委派应标 Err")
	}
	if got.StopReason != StopKindLLMError {
		t.Errorf("StopReason = %q", got.StopReason)
	}
}

// TestEmitDelegationMetrics_ExecutorOverridesToolSuffix 钉住 custom 分支的执行者优先：
// `delegate_meta` 的工具名后缀是 "meta"，但真正跑了 LLM 循环的是它刚生成的 Agent。
// 不覆盖的后果是账本里出现一条 "meta 跑了 N 步 / 花了 N token" 的假样本（评分按
// agent 分组，meta 于是带着别人的成绩单进排行榜）。
func TestEmitDelegationMetrics_ExecutorOverridesToolSuffix(t *testing.T) {
	a := &DirectorAgent{}
	var got DelegationMetrics
	a.SetDelegationMetricsHook(func(m DelegationMetrics) { got = m })
	a.emitDelegationMetrics("delegate_meta", "call_2", "", "security_auditor", nil, AgentResult{Steps: 1})
	if got.Agent != "security_auditor" {
		t.Fatalf("Agent = %q，want security_auditor（outcome 记的执行者优先于工具名后缀）", got.Agent)
	}
	if got.Tool != "delegate_meta" {
		t.Errorf("Tool = %q，want delegate_meta（工具名仍然要如实记录）", got.Tool)
	}
}
