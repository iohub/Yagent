package agents

import (
	"context"
	"strings"
	"testing"

	"yagent/internal/plasma"
)

// fixtureSpecJSON 是 5a 的被测对象：一份只读、单工具、能收尾的最小 spec。
// 走 Elaborate→Compile→Link 与生产完全同一条路径 —— 夹具里手工拼一个 LinkPlan
// 会让"bundle.runtime 有没有被读走"这件事变得测不到。
const fixtureSpecJSON = `{"agent_name":"Fixture Auditor",` +
	`"agent_design":"You are a fixture auditor.\nUse ` + "`read_file`" + ` to inspect code, ` +
	`then call ` + "`agent_exit`" + ` with reason and a handoff conclusion.\n` +
	`<thought>\ngoal: verify fixture wiring\nreason: structural fixture\n` +
	`plan:\n[>] read the file\n[ ] exit\n</thought>",` +
	`"tools_used":["read_file"],"task_for_agent":"run the structural fixture"}`

func fixturePlan(t *testing.T) (*plasma.Bundle, *plasma.LinkPlan) {
	t.Helper()
	b, err := plasma.Elaborate([]byte(fixtureSpecJSON), plasma.DefaultDefaults())
	if err != nil {
		t.Fatalf("Elaborate: %v", err)
	}
	c := &plasma.Compiler{Palette: plasma.PlasmaPalette}
	if r := c.Compile(context.Background(), b); r.HasErrors() {
		t.Fatalf("fixture bundle 编译失败:\n%s", r.Text())
	}
	plan, err := c.Link(b, "fixture-hash")
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	return b, plan
}

func structuralFixture(t *testing.T, b *plasma.Bundle, id string) plasma.Fixture {
	t.Helper()
	if b.Fixtures == nil {
		t.Fatal("bundle 没有夹具：Compile 的结构夹具生成没生效")
	}
	for _, f := range b.Fixtures.Structural {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("bundle 里没有夹具 %q", id)
	return plasma.Fixture{}
}

// 词表是跨包契约：plasma 是叶子包写不了 memory 常量，所以必须在这里对账。
// 任一侧改名，这条先红 —— 而不是等到某条夹具以"stop_kind 不匹配"的误导形态失败。
func TestPlasmaFixtureRunner_StopKindVocabulary(t *testing.T) {
	if plasma.FixtureStopKindAgentExit != StopKindAgentExit {
		t.Errorf("plasma agent_exit 词 = %q, agents = %q",
			plasma.FixtureStopKindAgentExit, StopKindAgentExit)
	}
	if plasma.FixtureStopKindMaxSteps != StopKindMaxSteps {
		t.Errorf("plasma max_steps 词 = %q, agents = %q",
			plasma.FixtureStopKindMaxSteps, StopKindMaxSteps)
	}
}

// exit-contract 绿：脚本走通、交付文本是那条 assistant 文本、handoff 形状合规。
// 红：把期望交付换成另一个串 —— 必须失败，并报出**实际**交付文本。
func TestPlasmaFixtureRunner_ExitContract(t *testing.T) {
	b, plan := fixturePlan(t)
	runner := &PlasmaFixtureRunner{}
	f := structuralFixture(t, b, plasma.FixtureExitContract)

	out, err := runner.RunStructural(context.Background(), plan, f)
	if err != nil {
		t.Fatalf("RunStructural: %v", err)
	}
	if !out.Passed {
		t.Fatalf("exit-contract 应通过: %s", out.Reason)
	}
	if out.StopKind != StopKindAgentExit {
		t.Errorf("StopKind = %q, want %q", out.StopKind, StopKindAgentExit)
	}
	if out.Steps != 1 {
		t.Errorf("Steps = %d, want 1（正文与 exit 在同一条响应里）", out.Steps)
	}

	// 负样本：比对的是内容而不是"非空"。deliverableText 在回落到 JSON 时也非空。
	bad := f
	bad.Expect.DeliverableEquals = "another deliverable body"
	got, err := runner.RunStructural(context.Background(), plan, bad)
	if err != nil {
		t.Fatalf("RunStructural: %v", err)
	}
	if got.Passed {
		t.Fatal("交付内容不符必须失败 —— 只断言非空就是一条永真测试")
	}
	if !strings.Contains(got.Reason, plasma.FixtureDeliverableBody) {
		t.Errorf("失败原因要带上实际交付文本 %q，得到: %s",
			plasma.FixtureDeliverableBody, got.Reason)
	}
}

// step-budget 绿：脚本 max_steps+1 步 ⇒ 循环在预算处停。
// 两条红样本分别证明"预算真的来自 LinkPlan"与"步数真的被测量"。
func TestPlasmaFixtureRunner_StepBudget(t *testing.T) {
	b, plan := fixturePlan(t)
	runner := &PlasmaFixtureRunner{}
	f := structuralFixture(t, b, plasma.FixtureStepBudget)

	out, err := runner.RunStructural(context.Background(), plan, f)
	if err != nil {
		t.Fatalf("RunStructural: %v", err)
	}
	if !out.Passed {
		t.Fatalf("step-budget 应通过: %s", out.Reason)
	}
	if out.StopKind != plasma.FixtureStopKindMaxSteps {
		t.Errorf("StopKind = %q, want %q", out.StopKind, plasma.FixtureStopKindMaxSteps)
	}
	if out.Steps != plan.Config.MaxSteps {
		t.Errorf("Steps = %d, want %d", out.Steps, plan.Config.MaxSteps)
	}

	// 红 1：把预算放大到脚本长度之外 ⇒ 脚本先耗尽 ⇒ 纯文本收尾。
	// 这条只有"MaxSteps 真取自 plan.Config"时才可能绿；写死默认值会让它变红。
	bigger := *plan
	bigger.Config.MaxSteps = plan.Config.MaxSteps + 5
	got, err := runner.RunStructural(context.Background(), &bigger, f)
	if err != nil {
		t.Fatalf("RunStructural: %v", err)
	}
	if got.Passed {
		t.Fatal("预算被放大后 step-budget 必须失败（脚本先耗尽）")
	}
	if !strings.Contains(got.Reason, "stop_kind") {
		t.Errorf("失败原因要点名 stop_kind，得到: %s", got.Reason)
	}

	// 红 2：把期望上界收紧 ⇒ 实际步数必须被报出来。
	tight := f
	tight.Expect.MaxSteps = 1
	got2, err := runner.RunStructural(context.Background(), plan, tight)
	if err != nil {
		t.Fatalf("RunStructural: %v", err)
	}
	if got2.Passed || !strings.Contains(got2.Reason, "超出预算") {
		t.Fatalf("步数超限必须失败并说明: %+v", got2)
	}
}

// tool-containment 绿：Surface 恰好是声明工具 + agent_exit。
// 红（多一个）：往 Profile.Names 塞一个已注册但未声明的工具 ⇒ 点名 list_dir。
// 红（少一个）：从 Agent.Tools 删掉 read_file ⇒ 点名 read_file。
func TestPlasmaFixtureRunner_ToolContainment(t *testing.T) {
	b, plan := fixturePlan(t)
	runner := &PlasmaFixtureRunner{}
	f := structuralFixture(t, b, plasma.FixtureToolContainment)

	out, err := runner.RunStructural(context.Background(), plan, f)
	if err != nil {
		t.Fatalf("RunStructural: %v", err)
	}
	if !out.Passed {
		t.Fatalf("正常 bundle 的工具面应恰好是声明 + agent_exit: %s", out.Reason)
	}

	extra := *plan
	extra.Profile.Names = append(append([]string(nil), plan.Profile.Names...), "list_dir")
	got, err := runner.RunStructural(context.Background(), &extra, f)
	if err != nil {
		t.Fatalf("RunStructural: %v", err)
	}
	if got.Passed || !strings.Contains(got.Reason, "list_dir") {
		t.Fatalf("未声明工具混进 Surface 必须失败并点名，得到: %+v", got)
	}

	missing := *plan
	missing.Agent.Tools = []string{"agent_exit"} // Surface 仍会装上 read_file
	got2, err := runner.RunStructural(context.Background(), &missing, f)
	if err != nil {
		t.Fatalf("RunStructural: %v", err)
	}
	if got2.Passed || !strings.Contains(got2.Reason, "read_file") {
		t.Fatalf("声明与实际不一致必须失败并点名，得到: %+v", got2)
	}
}

// palette-containment 绿：只读 bundle 不越界。
// 红：把 run_bash 塞进 Names ⇒ 点名它并说出越界能力（证明 PlasmaPalette 真的收窄）。
func TestPlasmaFixtureRunner_PaletteContainment(t *testing.T) {
	b, plan := fixturePlan(t)
	runner := &PlasmaFixtureRunner{}
	f := structuralFixture(t, b, plasma.FixturePaletteContainment)

	out, err := runner.RunStructural(context.Background(), plan, f)
	if err != nil {
		t.Fatalf("RunStructural: %v", err)
	}
	if !out.Passed {
		t.Fatalf("只读 bundle 不该越界: %s", out.Reason)
	}

	bad := *plan
	bad.Profile.Names = append(append([]string(nil), plan.Profile.Names...), "run_bash")
	got, err := runner.RunStructural(context.Background(), &bad, f)
	if err != nil {
		t.Fatalf("RunStructural: %v", err)
	}
	if got.Passed {
		t.Fatal("越权工具必须失败 —— PlasmaPalette 的收窄没有生效")
	}
	if !strings.Contains(got.Reason, "run_bash") || !strings.Contains(got.Reason, "shell") {
		t.Errorf("失败原因要点名工具与越界能力，得到: %s", got.Reason)
	}
}

// 5b 在 P6 里是占位：不许真调 LLM，也不许白送"通过"。
func TestPlasmaFixtureRunner_BehavioralIsPlaceholder(t *testing.T) {
	_, plan := fixturePlan(t)
	out, err := (&PlasmaFixtureRunner{}).RunBehavioral(
		context.Background(), plan, plasma.Fixture{ID: "behavioral-1"})
	if err != nil {
		t.Fatalf("占位不该返回 error（error 会被当成一次失败的重试）: %v", err)
	}
	if out.Passed {
		t.Fatal("5b 未实现却报通过 —— verified 的门被白送")
	}
	if !strings.Contains(out.Reason, "offline") {
		t.Errorf("Reason 要说明只在离线跑，得到: %q", out.Reason)
	}
}

// 端到端：真实 runner 跑完四条结构夹具 → 无诊断 → 棘轮停在 provisional（在线不跑 5b）。
func TestPlasmaFixtureRunner_VerifyAllAndRatchet(t *testing.T) {
	ctx := context.Background()
	b, plan := fixturePlan(t)
	c := &plasma.Compiler{Palette: plasma.PlasmaPalette}

	outs, rep := c.VerifyStructural(ctx, b, plan, &PlasmaFixtureRunner{})
	if len(outs) != 4 {
		t.Fatalf("Outcomes = %d 条, want 4: %+v", len(outs), outs)
	}
	if failed := outs.Failed(); len(failed) != 0 {
		t.Fatalf("正常 bundle 的 5a 不该有失败项: %+v", failed)
	}
	if len(rep.Diagnostics) != 0 {
		t.Fatalf("5a 全绿却出了诊断:\n%s", rep.Text())
	}

	compileReport := c.Compile(ctx, b)
	// behavioral 传 nil：在线不跑 5b，未跑 ≠ 通过。
	if grade := plasma.Ratchet(compileReport, outs, nil, false); grade != plasma.GradeProvisional {
		t.Errorf("5a 通过 + 未跑 5b 的 grade = %q, want provisional", grade)
	}
}
