package tui

// tui_resume_agent_test.go — "从子 Agent 继续"在 TUI 侧的接线（§8.2、§12 P2.7）。
//
// 这里最要紧的一条不是"选项出来了"，而是"没有落点时那个一次性指路牌必须是空的"：
// m.resumedDelegationPath 一旦被上一轮残留下来，下一次追问就会带着一个旧委派去重放 ——
// 表现为"我明明只是问了个小问题，它把三天前那次委派又跑了一遍"。

import (
	"testing"

	"yagent/internal/app"
	"yagent/internal/memory"
	"yagent/internal/tui/components"
)

func interruptedInfo() memory.RolloutInfo {
	return memory.RolloutInfo{
		Aborted:     true,
		AbortReason: "context canceled",
		StopReason:  "cancelled",
		DanglingToolCalls: []memory.DanglingToolCall{
			{ID: "c1", Name: "delegate_coding"},
		},
	}
}

func TestPushResumeDialogClearsStaleDelegationPath(t *testing.T) {
	m := restoreTestModel(t)
	m.resumedDelegationPath = "/tmp/stale-director.jsonl"

	pushResumeDialog(m, interruptedInfo(), "/tmp/some-director.jsonl", "")

	d, ok := m.dialogStack.Top().(*components.ResumeDialog)
	if !ok {
		t.Fatalf("top dialog = %T, want *components.ResumeDialog", m.dialogStack.Top())
	}
	// assistant 为 nil（没有 app 可问）⇒ 不提供第三档，并且残留的指路牌必须被清掉。
	if d.AgentOption != "" {
		t.Errorf("AgentOption = %q, want empty without an app to ask", d.AgentOption)
	}
	if m.resumedDelegationPath != "" {
		t.Errorf("resumedDelegationPath 残留为 %q，下一次追问会拿它重放一个旧委派", m.resumedDelegationPath)
	}
}

func TestPushResumeDialogIgnoresSubAgentFiles(t *testing.T) {
	m := restoreTestModel(t)
	pushResumeDialog(m, interruptedInfo(), "/tmp/x_coding.jsonl", "coding")
	d := m.dialogStack.Top().(*components.ResumeDialog)
	if d.AgentOption != "" {
		t.Error("子 Agent 那份 rollout 不该提供第三档 —— 入口只有 Director 那份")
	}
}

func TestResumeAgentOptionLabel(t *testing.T) {
	stop := &app.ResumableDelegation{AgentName: "coding", Step: 37, MaxSteps: 150, Replayable: true}

	en := resumeAgentOptionLabel(components.LanguageEn, stop)
	if en != "Resume coding 37/150" {
		t.Errorf("en label = %q", en)
	}
	zh := resumeAgentOptionLabel(components.LanguageZh, stop)
	if zh != "从 coding 37/150 继续" {
		t.Errorf("zh label = %q", zh)
	}

	// 不可精确重放时必须在按钮上就说出来，而不是等用户点完才发现是交接报告。
	stop.Replayable = false
	if got := resumeAgentOptionLabel(components.LanguageEn, stop); got[len(got)-len(" (handoff)"):] != " (handoff)" {
		t.Errorf("不可重放时按钮没有说明它走的是交接： %q", got)
	}
	if got := resumeAgentOptionLabel(components.LanguageZh, stop); got[len(got)-len("（交接）"):] != "（交接）" {
		t.Errorf("中文档没有说明走交接：%q", got)
	}
}

// 没有步数信息时不能渲染成 "step 0/0"。
func TestResumeAgentOptionLabelWithoutSteps(t *testing.T) {
	stop := &app.ResumableDelegation{AgentName: "repo", Replayable: true}
	if got := resumeAgentOptionLabel(components.LanguageEn, stop); got != "Resume repo" {
		t.Errorf("label = %q, want \"Resume repo\" with no step part", got)
	}
}
