package agents

import (
	"context"
	"strings"
	"testing"

	"yagent/internal/llm"
	"yagent/internal/tools"
)

// 纯文本收尾的提醒有两条文案：通用的"你还没动手"（Coding 那类执行型 Agent），
// 和收尾契约（Repo 那类以报告为交付物的分析型 Agent）。把文本用 **history 里真的出现**
// 来断言，而不是断一个 getter —— 提醒没发出去就等于这条分支在生产里不存在。

// textOnlyThenText 造一个"两次都只回文本"的引擎：循环必然走到提醒那条分支。
func textOnlyThenText(replies ...string) *mockLLM {
	resp := make([]*llm.Response, 0, len(replies))
	for _, r := range replies {
		resp = append(resp, &llm.Response{Choices: []llm.Choice{{Content: r}}})
	}
	return &mockLLM{responses: resp}
}

func runTextOnly(t *testing.T, cfg ExecutorConfig, replies ...string) ExecutorResult {
	t.Helper()
	cfg.SystemPrompt = "You are a test agent."
	cfg.UserInput = "answer something"
	cfg.Surface = tools.NewSurfaceFromAdapters("test-agent")
	cfg.LLM = textOnlyThenText(replies...)
	cfg.MaxSteps = 5
	cfg.StopOnFinish = true
	cfg.AgentName = "text-only-test"
	if cfg.Surface == nil {
		t.Fatalf("没有工具面")
	}
	res, err := RunAgentLoop(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunAgentLoop 报错: %v", err)
	}
	return res
}

func reminderIn(res ExecutorResult) string {
	for _, m := range res.History {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "SYSTEM REMINDER") {
			return m.Content
		}
	}
	return ""
}

// 没设 NoActionReminder 时必须逐字用回通用那条 —— 覆盖是 opt-in，
// 否则"给 Repo 改文案"会顺手改掉所有子 Agent 的行为。
func TestPlainTextReminderDefaultsToGenericActionNudge(t *testing.T) {
	res := runTextOnly(t, ExecutorConfig{NoActionRetryLimit: 1}, "第一版答案", "第二版答案")
	got := reminderIn(res)
	if got == "" {
		t.Fatalf("一次提醒都没发出去，纯文本收尾就此静默: %+v", res.History)
	}
	for _, want := range []string{"explore/edit/run/verify", "take immediate action"} {
		if !strings.Contains(got, want) {
			t.Errorf("通用提醒少了 %q，实得:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Call `agent_exit` now") {
		t.Errorf("收尾契约的文案漏进了默认分支:\n%s", got)
	}
}

// 设了就用它。
func TestPlainTextReminderCanBeOverridden(t *testing.T) {
	res := runTextOnly(t, ExecutorConfig{
		NoActionRetryLimit: 1,
		NoActionReminder:   "SYSTEM REMINDER: finish with agent_exit instead.",
	}, "报告一", "报告二")
	got := reminderIn(res)
	if !strings.Contains(got, "finish with agent_exit") {
		t.Errorf("自定义提醒没发出去: %q", got)
	}
	if strings.Contains(got, "explore/edit/run/verify") {
		t.Errorf("通用文案和自定义文案混在一条里:\n%s", got)
	}
}

// 上限是真的：RetryLimit=1 就只提醒一次，模型坚持用文本交付时不无限追。
func TestPlainTextReminderIsBounded(t *testing.T) {
	res := runTextOnly(t, ExecutorConfig{NoActionRetryLimit: 1}, "a", "b", "c")
	n := 0
	for _, m := range res.History {
		if m.Role == llm.RoleUser && strings.Contains(m.Content, "SYSTEM REMINDER") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("提醒发了 %d 次, 要 1 次（NoActionRetryLimit=1）", n)
	}
	if res.Text != "b" {
		t.Errorf("第二次仍是纯文本，交付物 = %q, 要 \"b\"", res.Text)
	}
}

// Repo 的收尾契约必须说清"报告已经收到"，而不是叫它再去探索一轮。
func TestRepoFinishReminderDoesNotSendItExploringAgain(t *testing.T) {
	for _, bad := range []string{"explore/edit/run/verify", "Do not repeat your plan"} {
		if strings.Contains(repoFinishReminder, bad) {
			t.Errorf("Repo 的提醒里出现了 %q —— 它的报告就是交付物，催它再执行一轮等于重做分析", bad)
		}
	}
	for _, want := range []string{"Call `agent_exit` now", "blueprint", "handoff"} {
		if !strings.Contains(repoFinishReminder, want) {
			t.Errorf("Repo 的收尾提醒少了 %q", want)
		}
	}
}
