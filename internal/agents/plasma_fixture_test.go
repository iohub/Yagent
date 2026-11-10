package agents

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yagent/internal/llm"
	"yagent/internal/llm/scripted"
	"yagent/internal/plasma"
	"yagent/internal/toolspec"
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

// 5b 的引擎/预算是**注入**的：没注入时不许真调 LLM，也不许白送"通过"。
// 三条"没资格判通过"的入口逐条钉住 —— 任何一条静默变绿都等于 verified 的门被白送。
func TestPlasmaFixtureRunner_BehavioralWithoutEngineOrBudget(t *testing.T) {
	_, plan := fixturePlan(t)
	fixture := plasma.Fixture{ID: "behavioral-1", Expect: plasma.Expect{StopKind: plasma.FixtureStopKindAgentExit}}

	cases := []struct {
		name    string
		runner  *PlasmaFixtureRunner
		wantSub string
	}{
		{"没注入引擎", &PlasmaFixtureRunner{BehavioralTokenBudget: 1000}, "没有注入 LLM 引擎"},
		{"没配 token 预算", &PlasmaFixtureRunner{LLM: scripted.NewForIdentity("x", nil)}, "token 预算"},
	}
	for _, tc := range cases {
		out, err := tc.runner.RunBehavioral(context.Background(), plan, fixture)
		if err != nil {
			t.Fatalf("%s: 不该返回 error（那不是可重试的失败）: %v", tc.name, err)
		}
		if out.Passed {
			t.Fatalf("%s: 报通过了 —— verified 的门被白送", tc.name)
		}
		if !strings.Contains(out.Reason, tc.wantSub) {
			t.Errorf("%s: Reason 要说清缺什么，得到 %q", tc.name, out.Reason)
		}
	}
}

// behavioralPlan 把 fixture bundle 的 runtime 调小：5b 的步数预算来自 LinkPlan，
// 而默认 15 步会让"超时"用例跑很久。
func behavioralPlan(t *testing.T) (*plasma.Bundle, *plasma.LinkPlan) {
	t.Helper()
	b, plan := fixturePlan(t)
	plan.Config.MaxSteps = 3
	return b, plan
}

// behavioralSteps 构造"一次响应里带正文 + agent_exit"的脚本（与 5a 同形状）。
func behavioralSteps(body string) []scripted.Step {
	return []scripted.Step{
		scripted.TextAndToolCall(body, toolspec.ExitToolName, map[string]interface{}{
			"reason": "done",
			"handoff": map[string]interface{}{
				"conclusion": "behavioral fixture completed",
				"status":     "ok",
			},
		}),
	}
}

// 5b 绿：注入的引擎走通，Expect 全中，grade 的门因此可以开。
func TestPlasmaFixtureRunner_BehavioralPass(t *testing.T) {
	_, plan := behavioralPlan(t)
	engine := scripted.NewForIdentity(plan.Agent.Name, behavioralSteps("分析完成：给出结论"))
	runner := &PlasmaFixtureRunner{LLM: engine, BehavioralTokenBudget: 60000}

	fixture := plasma.Fixture{
		ID:    "behavioral-pass",
		Input: "跑一遍行为夹具",
		Expect: plasma.Expect{
			MustCall:            []string{toolspec.ExitToolName},
			StopKind:            plasma.FixtureStopKindAgentExit,
			DeliverableContains: []string{"分析完成"},
		},
		Rubric: []string{"是否给出了可核对的结论"},
	}
	out, err := runner.RunBehavioral(context.Background(), plan, fixture)
	if err != nil {
		t.Fatalf("RunBehavioral: %v", err)
	}
	if !out.Passed {
		t.Fatalf("应当通过: %s", out.Reason)
	}
	if out.StopKind != StopKindAgentExit {
		t.Errorf("StopKind = %q, want %q", out.StopKind, StopKindAgentExit)
	}
	if out.DeliverableChars == 0 {
		t.Error("DeliverableChars = 0：评分侧无法判 deliverable_nonempty")
	}
	if len(out.Rubric) != 1 {
		t.Errorf("Rubric 没有带到 Outcome 上（judge 看不到评审要点）: %+v", out.Rubric)
	}
}

// 5b 红：交付内容不符 ⇒ 失败且原因里带实际交付文本（不是一句 "failed"）。
func TestPlasmaFixtureRunner_BehavioralDeliverableMismatch(t *testing.T) {
	_, plan := behavioralPlan(t)
	engine := scripted.NewForIdentity(plan.Agent.Name, behavioralSteps("分析完成：给出结论"))
	runner := &PlasmaFixtureRunner{LLM: engine, BehavioralTokenBudget: 60000}

	fixture := plasma.Fixture{
		ID: "behavioral-mismatch",
		Expect: plasma.Expect{
			StopKind:            plasma.FixtureStopKindAgentExit,
			DeliverableContains: []string{"必须出现的关键结论"},
		},
	}
	out, err := runner.RunBehavioral(context.Background(), plan, fixture)
	if err != nil {
		t.Fatalf("RunBehavioral: %v", err)
	}
	if out.Passed {
		t.Fatal("交付内容不符必须失败 —— 否则 5b 只是「跑了一遍」")
	}
	if !strings.Contains(out.Reason, "必须出现的关键结论") {
		t.Errorf("失败原因要点名缺什么，得到: %s", out.Reason)
	}
	if !strings.Contains(out.Reason, "分析完成") {
		t.Errorf("失败原因要带实际交付文本，得到: %s", out.Reason)
	}
}

// 5b 超预算：引擎报了远超预算的 usage ⇒ **不许**为 true，Reason 明写 used/budget。
func TestPlasmaFixtureRunner_BehavioralTokenBudget(t *testing.T) {
	_, plan := behavioralPlan(t)
	// 超大 usage 必须挂在**第一步**响应上。早先这里用的是 SetExhaust（脚本耗尽后的
	// 兜底），而 behavioralSteps 的第一步就走 agent_exit，于是兜底从未被调用 ——
	// 用例绿了但走的是"预算根本没被观测"的路。给 Step 包一层才真的把 100k 送进循环。
	forced := behavioralSteps("超额的一次响应")[0]
	bigUsage := func(messages []llm.Message, defs []llm.ToolDef) *llm.Response {
		resp := forced(messages, defs)
		if resp != nil {
			resp.Usage = &llm.TokenUsage{PromptTokens: 90000, CompletionTokens: 10000, TotalTokens: 100000}
		}
		return resp
	}
	engine := scripted.NewForIdentity(plan.Agent.Name, []scripted.Step{bigUsage})
	runner := &PlasmaFixtureRunner{LLM: engine, BehavioralTokenBudget: 1000}

	fixture := plasma.Fixture{
		ID:     "behavioral-budget",
		Expect: plasma.Expect{DeliverableContains: []string{"超额"}},
	}
	out, err := runner.RunBehavioral(context.Background(), plan, fixture)
	if err != nil {
		t.Fatalf("RunBehavioral: %v", err)
	}
	if out.Passed {
		t.Fatal("token 超预算必须失败 —— 静默通过会让 verified 变成「花钱买的」")
	}
	if !strings.Contains(out.Reason, "token 预算耗尽") {
		t.Errorf("Reason 要明写预算耗尽，得到: %s", out.Reason)
	}
	if !strings.Contains(out.Reason, "100000/1000") {
		t.Errorf("Reason 要带上 used/budget（诊断靠它），得到: %s", out.Reason)
	}
}

// 5b 超时：seam 把墙上限定成 100ms，脚本 sleep 300ms ⇒ 必须判"未验证：墙钟超时"。
func TestPlasmaFixtureRunner_BehavioralWallClockTimeout(t *testing.T) {
	_, plan := behavioralPlan(t)
	slow := func([]llm.Message, []llm.ToolDef) *llm.Response {
		time.Sleep(300 * time.Millisecond)
		return scripted.TextResponse("慢响应")
	}
	engine := scripted.NewForIdentity(plan.Agent.Name, []scripted.Step{slow})
	runner := &PlasmaFixtureRunner{
		LLM:                   engine,
		BehavioralTokenBudget: 60000,
		BehavioralTimeout:     100 * time.Millisecond,
	}

	fixture := plasma.Fixture{ID: "behavioral-timeout", Expect: plasma.Expect{DeliverableContains: []string{"慢"}}}
	out, err := runner.RunBehavioral(context.Background(), plan, fixture)
	if err != nil {
		t.Fatalf("RunBehavioral: %v", err)
	}
	if out.Passed {
		t.Fatal("墙钟超时必须失败 —— 超时的夹具不算通过（ADR-3 的补偿控制）")
	}
	if !strings.Contains(out.Reason, "墙钟超时") {
		t.Errorf("Reason 要明写超时，得到: %s", out.Reason)
	}
	if !strings.Contains(out.Reason, "100ms") {
		t.Errorf("Reason 要带上限值（排障靠它），得到: %s", out.Reason)
	}
}

// 沙箱：夹具 workspace 必须被**复制**到临时目录，执行后清理，
// 且原始快照一个字节都不能被改动（ADR-5 的夹具不可变）。
func TestPlasmaFixtureRunner_BehavioralSandboxIsCopy(t *testing.T) {
	_, plan := behavioralPlan(t)
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	marker := filepath.Join(workspace, "note.txt")
	if err := os.WriteFile(marker, []byte("原始内容"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	// 模型读一次 note.txt：读到的是沙箱里的副本。脚本里带上绝对路径是不可能的
	// （沙箱路径在运行期才生成），所以断言放在"快照没被改"这一侧。
	engine := scripted.NewForIdentity(plan.Agent.Name, behavioralSteps("读完了"))
	runner := &PlasmaFixtureRunner{
		LLM: engine, BehavioralTokenBudget: 60000, WorkspaceRoot: root,
	}
	fixture := plasma.Fixture{ID: "behavioral-workspace", Workspace: "ws"}

	before, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker: %v", err)
	}
	out, runErr := runner.RunBehavioral(context.Background(), plan, fixture)
	if runErr != nil {
		t.Fatalf("RunBehavioral: %v", runErr)
	}
	if !out.Passed {
		t.Fatalf("应当通过: %s", out.Reason)
	}
	after, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read marker after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("夹具快照被改动了 —— 5b 必须跑在副本上（ADR-5）")
	}

	// workspace 不存在 ⇒ 必须报"未验证"，不许当成"没有工作区"静默跑。
	missing := plasma.Fixture{ID: "behavioral-missing", Workspace: "no-such-dir"}
	got, _ := runner.RunBehavioral(context.Background(), plan, missing)
	if got.Passed || !strings.Contains(got.Reason, "工作区不可用") {
		t.Fatalf("缺工作区必须明确失败，得到: %+v", got)
	}
}

// 夹具运行必须进账本且 fixture_run=true：评分侧只统计 false，漏标就是污染生产评分。
func TestPlasmaFixtureRunner_BehavioralMarksFixtureRun(t *testing.T) {
	_, plan := behavioralPlan(t)
	engine := scripted.NewForIdentity(plan.Agent.Name, behavioralSteps("分析完成"))
	var got []DelegationMetrics
	runner := &PlasmaFixtureRunner{
		LLM: engine, BehavioralTokenBudget: 60000,
		Metrics: func(m DelegationMetrics) { got = append(got, m) },
	}
	if _, err := runner.RunBehavioral(context.Background(), plan,
		plasma.Fixture{ID: "behavioral-metrics"}); err != nil {
		t.Fatalf("RunBehavioral: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("账本行数 = %d, want 1", len(got))
	}
	if !got[0].FixtureRun {
		t.Fatal("fixture_run 没置真 —— 夹具样本会混进生产评分（Goodhart）")
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
