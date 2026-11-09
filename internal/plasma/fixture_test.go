package plasma_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"yagent/internal/plasma"
)

// stubRunner 是 5a 的诊断侧探针：它不执行任何东西，只按夹具 ID 回一个观测。
// VerifyStructural 的判据（码分派、Path、Reason 非空、一条失败不影响其余）全在这里钉，
// 真执行由 agents 侧的 PlasmaFixtureRunner 负责。
type stubRunner struct {
	fn func(plasma.Fixture) (plasma.Outcome, error)
}

func (s stubRunner) RunStructural(_ context.Context, _ *plasma.LinkPlan, f plasma.Fixture) (plasma.Outcome, error) {
	if s.fn != nil {
		return s.fn(f)
	}
	return plasma.Outcome{FixtureID: f.ID, Passed: true}, nil
}

func (s stubRunner) RunBehavioral(_ context.Context, _ *plasma.LinkPlan, f plasma.Fixture) (plasma.Outcome, error) {
	return plasma.Outcome{FixtureID: f.ID, Passed: false, Reason: "not run"}, nil
}

func fixtureIDs(fs []plasma.Fixture) []string {
	out := make([]string, 0, len(fs))
	for _, f := range fs {
		out = append(out, f.ID)
	}
	return out
}

// 四条夹具的 ID、形状、步数与生成幂等必须一起钉：任何一条改了，测试里失败的
// 是"形状"而不是"某个断言恰好没触发"。
func TestStructuralFixtures_Generated(t *testing.T) {
	b := cleanBundle()
	b.Runtime.MaxSteps = 7

	got := plasma.StructuralFixtures(b)
	want := []string{
		plasma.FixtureExitContract,
		plasma.FixtureStepBudget,
		plasma.FixtureToolContainment,
		plasma.FixturePaletteContainment,
	}
	if ids := fixtureIDs(got); !reflect.DeepEqual(ids, want) {
		t.Fatalf("夹具 ID/顺序 = %v, want %v", ids, want)
	}
	for _, f := range got {
		if f.Workspace != "" {
			t.Errorf("%s: 结构夹具不需要沙箱目录，Workspace = %q", f.ID, f.Workspace)
		}
	}

	exit := got[0]
	// 一条响应里既带正文又带 agent_exit：循环在"没有 tool_calls"时直接按纯文本
	// 收尾，所以两步写法走不到第二步（夹具会以"stop_kind 不匹配"的假象失败）。
	if len(exit.Script) != 1 {
		t.Fatalf("exit-contract 脚本 %d 步, want 1", len(exit.Script))
	}
	if exit.Script[0].Text != plasma.FixtureDeliverableBody || exit.Script[0].Tool != "agent_exit" {
		t.Errorf("exit-contract 那一步应同时带正文 %q 与 agent_exit，得到 %+v",
			plasma.FixtureDeliverableBody, exit.Script[0])
	}
	// 交付内容必须是内容级断言：只判非空的话，deliverableText 的 JSON 回落也满足。
	if exit.Expect.DeliverableEquals != plasma.FixtureDeliverableBody {
		t.Errorf("exit-contract 必须比对交付内容，得到 %q", exit.Expect.DeliverableEquals)
	}
	if exit.Expect.StopKind != plasma.FixtureStopKindAgentExit {
		t.Errorf("exit-contract StopKind = %q", exit.Expect.StopKind)
	}
	if !reflect.DeepEqual(exit.Expect.ExitShape, []string{"agent_exit", "handoff"}) {
		t.Errorf("exit-contract ExitShape = %v", exit.Expect.ExitShape)
	}

	budget := got[1]
	// 恰好 max_steps+1 步：多一步证明预算生效，少一步脚本先耗尽。
	if len(budget.Script) != b.Runtime.MaxSteps+1 {
		t.Fatalf("step-budget 脚本 %d 步, want %d（max_steps+1）",
			len(budget.Script), b.Runtime.MaxSteps+1)
	}
	if budget.Expect.MaxSteps != b.Runtime.MaxSteps {
		t.Errorf("step-budget Expect.MaxSteps = %d, want %d",
			budget.Expect.MaxSteps, b.Runtime.MaxSteps)
	}
	if budget.Expect.StopKind != plasma.FixtureStopKindMaxSteps {
		t.Errorf("step-budget StopKind = %q", budget.Expect.StopKind)
	}
	for i, s := range budget.Script {
		if s.Tool != "thinking" {
			t.Errorf("step-budget script[%d] 用 %q，应为 thinking", i, s.Tool)
		}
	}

	if again := plasma.StructuralFixtures(b); !reflect.DeepEqual(got, again) {
		t.Error("StructuralFixtures 不幂等：同一份 bundle 两次生成不同")
	}
}

// 作者提供的夹具一条都不许被覆盖（ADR-5：夹具一经落盘即不可变）。
func TestStructuralFixtures_CompileKeepsAuthorFixtures(t *testing.T) {
	b := cleanBundle()
	b.Fixtures = &plasma.Fixtures{
		Provenance: plasma.Provenance{Author: "human", Lineage: "human-authored"},
		Structural: []plasma.Fixture{{ID: "author-made", Input: "x"}},
	}
	if r := (&plasma.Compiler{Palette: testPalette}).Compile(context.Background(), b); r.HasErrors() {
		t.Fatalf("干净 bundle 不该有编译错误: %s", r.Text())
	}
	if len(b.Fixtures.Structural) != 1 || b.Fixtures.Structural[0].ID != "author-made" {
		t.Fatalf("作者夹具被覆盖了: %v", fixtureIDs(b.Fixtures.Structural))
	}
	if b.Fixtures.Provenance.Author != "human" || b.Fixtures.Provenance.Lineage != "human-authored" {
		t.Errorf("作者谱系被改写: %+v", b.Fixtures.Provenance)
	}
}

// 自动生成的夹具必须落进 hash，并且能被 store 接受（provenance 补齐）。
func TestStructuralFixtures_HashCoversFixtures(t *testing.T) {
	ctx := context.Background()
	c := &plasma.Compiler{Palette: testPalette}

	b1 := cleanBundle()
	if r := c.Compile(ctx, b1); r.HasErrors() {
		t.Fatalf("编译: %s", r.Text())
	}
	if b1.Fixtures == nil || len(b1.Fixtures.Structural) != 4 {
		t.Fatalf("Compile 没有把 4 条结构夹具写进 bundle: %+v", b1.Fixtures)
	}
	if b1.Fixtures.Provenance.Lineage != plasma.FixtureLineageCompiler {
		t.Errorf("自动夹具谱系 = %+v，store 会按「自己出题自己考」拒收", b1.Fixtures.Provenance)
	}
	h1, err := plasma.HashBundle(b1)
	if err != nil {
		t.Fatal(err)
	}

	// 改一条夹具 ⇒ 换 hash ⇒ 新目录 ⇒ grade 不继承，这条链不靠额外逻辑。
	b2 := cleanBundle()
	if r := c.Compile(ctx, b2); r.HasErrors() {
		t.Fatalf("编译: %s", r.Text())
	}
	b2.Fixtures.Structural[0].Expect.DeliverableEquals = "tampered"
	h2, err := plasma.HashBundle(b2)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Error("改了夹具但 hash 没变 —— 夹具不可变这条保证失效")
	}

	s, _ := newStore(t)
	if _, err := s.Put(b1, &plasma.Report{Grade: plasma.GradeProvisional}); err != nil {
		t.Fatalf("自动夹具的 bundle 被 store 拒收: %v", err)
	}
}

func TestOutcomes_Queries(t *testing.T) {
	outs := plasma.Outcomes{
		{FixtureID: "a", Passed: true},
		{FixtureID: "b", Passed: false, Reason: "boom"},
	}
	if outs.AllPassed() {
		t.Error("含失败项的集合不该 AllPassed")
	}
	if failed := outs.Failed(); len(failed) != 1 || failed[0].FixtureID != "b" {
		t.Errorf("Failed() = %+v", failed)
	}
	if got, ok := outs.Get("b"); !ok || got.Reason != "boom" {
		t.Errorf("Get(b) = %+v, %v", got, ok)
	}
	if _, ok := outs.Get("nope"); ok {
		t.Error("Get 不该命中不存在的 ID")
	}
	// 空集是全称量化的真值（Ratchet 另外判 len==0，两件事分开）。
	if !plasma.Outcomes(nil).AllPassed() {
		t.Error("空集 AllPassed 应为 true")
	}
}

// ADR-9 的四个臂 + 两条"没跑不等于通过"。
func TestRatchet_Table(t *testing.T) {
	passed := plasma.Outcomes{{FixtureID: "a", Passed: true}}
	failed := plasma.Outcomes{{FixtureID: "a", Passed: false, Reason: "boom"}}
	compileErr := &plasma.Report{Diagnostics: []plasma.Diagnostic{{
		Code: plasma.CodeName, Severity: plasma.SeverityError,
	}}}
	escalating := &plasma.Report{Grade: plasma.GradeProvisional, EscalationRequired: true}

	cases := []struct {
		name     string
		r        *plasma.Report
		st, bh   plasma.Outcomes
		approved bool
		want     plasma.Grade
	}{
		{"compile-error", compileErr, passed, passed, true, ""},
		{"nil-report", nil, passed, passed, true, ""},
		{"structural-failed", &plasma.Report{}, failed, passed, true, ""},
		{"structural-missing", &plasma.Report{}, nil, passed, true, ""},
		{"5a-only", &plasma.Report{}, passed, nil, false, plasma.GradeProvisional},
		{"5a+5b", &plasma.Report{}, passed, passed, false, plasma.GradeVerified},
		{"behavioral-failed", &plasma.Report{}, passed, failed, false, plasma.GradeProvisional},
		{"escalation-unapproved", escalating, passed, passed, false, plasma.GradeProvisional},
		{"escalation-approved", escalating, passed, passed, true, plasma.GradeVerified},
	}
	for _, tc := range cases {
		if got := plasma.Ratchet(tc.r, tc.st, tc.bh, tc.approved); got != tc.want {
			t.Errorf("%s: Ratchet = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestCanPromote_Boundaries(t *testing.T) {
	cases := []struct {
		grade      plasma.Grade
		runs, min  int
		wantPromot bool
	}{
		{plasma.GradeVerified, 4, 5, false},     // 差一次
		{plasma.GradeVerified, 5, 5, true},      // 正好达标
		{plasma.GradeVerified, 6, 5, true},      // 超额
		{plasma.GradeVerified, 0, 5, false},     // 还没跑过生产
		{plasma.GradeVerified, 5, 0, false},     // 阈值没配，不许自动升
		{plasma.GradeProvisional, 10, 5, false}, // 未 verified
	}
	for _, tc := range cases {
		if got := plasma.CanPromote(tc.grade, tc.runs, tc.min); got != tc.wantPromot {
			t.Errorf("CanPromote(%q, %d, %d) = %v, want %v",
				tc.grade, tc.runs, tc.min, got, tc.wantPromot)
		}
	}
}

// 一条失败不影响其余：3 条夹具里 2 红 1 绿，Outcomes 长度必须是 3，
// 诊断恰好 2 条且 Path 指向真实下标。
func TestVerifyStructural_CollectsAllFailures(t *testing.T) {
	b := cleanBundle()
	b.Fixtures = &plasma.Fixtures{Structural: []plasma.Fixture{
		{ID: "first", Input: "x"},
		{ID: "second", Input: "x"},
		{ID: "third", Input: "x"},
	}}
	runner := stubRunner{fn: func(f plasma.Fixture) (plasma.Outcome, error) {
		if f.ID == "second" {
			return plasma.Outcome{FixtureID: f.ID, Passed: true}, nil
		}
		return plasma.Outcome{FixtureID: f.ID, Passed: false, Reason: "boom-" + f.ID}, nil
	}}

	outs, rep := (&plasma.Compiler{Palette: testPalette}).VerifyStructural(
		context.Background(), b, &plasma.LinkPlan{}, runner)

	if len(outs) != 3 {
		t.Fatalf("Outcomes 长度 = %d, want 3（一条失败不该中断后续）", len(outs))
	}
	if outs[1].Passed != true || outs[2].Reason != "boom-third" {
		t.Errorf("后续夹具没有按顺序执行: %+v", outs)
	}
	if len(rep.Diagnostics) != 2 {
		t.Fatalf("诊断 = %d 条, want 2: %s", len(rep.Diagnostics), rep.Text())
	}
	if rep.Diagnostics[0].Path != "fixtures.structural[0]" ||
		rep.Diagnostics[1].Path != "fixtures.structural[2]" {
		t.Errorf("诊断 Path 没指向夹具下标: %+v", rep.Diagnostics)
	}
	for _, d := range rep.Diagnostics {
		if d.Severity != plasma.SeverityError || d.Note == "" {
			t.Errorf("失败诊断必须 error 档且 note 带 Reason: %+v", d)
		}
	}
}

// 五个 5xx 码各一条：按 FixtureID 分派，未登记的一类回落 501。
func TestVerifyStructural_CodePerFixture(t *testing.T) {
	b := cleanBundle()
	b.Fixtures = &plasma.Fixtures{Structural: append(
		plasma.StructuralFixtures(b),
		plasma.Fixture{ID: "author-fixture", Input: "x"},
	)}
	runner := stubRunner{fn: func(f plasma.Fixture) (plasma.Outcome, error) {
		return plasma.Outcome{FixtureID: f.ID, Passed: false, Reason: "stub said no"}, nil
	}}

	_, rep := (&plasma.Compiler{Palette: testPalette}).VerifyStructural(
		context.Background(), b, &plasma.LinkPlan{}, runner)

	counts := map[string]int{}
	for _, d := range rep.Diagnostics {
		counts[d.Code]++
	}
	for _, code := range []string{
		plasma.CodeFixtureExit, plasma.CodeFixtureStepBudget,
		plasma.CodeFixtureToolScope, plasma.CodeFixturePalette,
		plasma.CodeFixtureFailed,
	} {
		if counts[code] != 1 {
			t.Errorf("%s 出现 %d 次, want 1（完整分布 %v）", code, counts[code], counts)
		}
	}
}

// runner 缺失、runner 返回 error、runner 不给 Reason —— 三种"信息缺失"都必须是
// 失败且 Reason 非空，绝不允许静默通过。
func TestVerifyStructural_MissingEvidenceFailsLoudly(t *testing.T) {
	b := cleanBundle()
	b.Fixtures = &plasma.Fixtures{Structural: []plasma.Fixture{{ID: "a", Input: "x"}}}
	c := &plasma.Compiler{Palette: testPalette}

	if outs, rep := c.VerifyStructural(context.Background(), b, &plasma.LinkPlan{}, nil); len(outs) != 1 ||
		outs[0].Passed || !strings.Contains(outs[0].Reason, "FixtureRunner") || len(rep.Diagnostics) != 1 {
		t.Fatalf("缺 runner 必须点名失败: %+v / %s", outs, rep.Text())
	}
	if outs, _ := c.VerifyStructural(context.Background(), b, nil, stubRunner{}); len(outs) != 1 ||
		outs[0].Passed || !strings.Contains(outs[0].Reason, "LinkPlan") {
		t.Fatalf("缺 LinkPlan 必须点名失败: %+v", outs)
	}

	errRunner := stubRunner{fn: func(f plasma.Fixture) (plasma.Outcome, error) {
		return plasma.Outcome{FixtureID: f.ID}, errors.New("engine exploded")
	}}
	outs, rep := c.VerifyStructural(context.Background(), b, &plasma.LinkPlan{}, errRunner)
	if len(outs) != 1 || outs[0].Passed || !strings.Contains(outs[0].Reason, "engine exploded") {
		t.Fatalf("runner 返回 error 必须落进 Reason: %+v", outs)
	}
	if len(rep.Diagnostics) != 1 || rep.Diagnostics[0].Note == "" {
		t.Fatalf("error 必须变成一条诊断: %s", rep.Text())
	}

	silent := stubRunner{fn: func(f plasma.Fixture) (plasma.Outcome, error) {
		return plasma.Outcome{FixtureID: f.ID, Passed: false}, nil
	}}
	outs, _ = c.VerifyStructural(context.Background(), b, &plasma.LinkPlan{}, silent)
	if outs[0].Reason == "" {
		t.Error("失败但没给 Reason 时，VerifyStructural 必须补一句可读原因")
	}
}

// 没有夹具（bundle 为空）时 5a 不产诊断，也不该伪造一条。
func TestVerifyStructural_NoFixturesNoDiagnostics(t *testing.T) {
	c := &plasma.Compiler{Palette: testPalette}
	outs, rep := c.VerifyStructural(context.Background(), cleanBundle(), &plasma.LinkPlan{}, stubRunner{})
	if len(outs) != 0 || len(rep.Diagnostics) != 0 {
		t.Errorf("无夹具时不该有结果或诊断: %+v / %s", outs, rep.Text())
	}
}
