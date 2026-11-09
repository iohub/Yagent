package agents

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"yagent/internal/config"
	"yagent/internal/plasma"
	"yagent/internal/tools"
)

// 本文件是 P9a 的闸门，钉两件事：
//
//  1. delegate_meta 的驱动器（Generate → Link → 5a → Ratchet → Store.Put → 注册 → 执行）
//     的**接线**：判定结论有没有真的被用上、执行参数有没有登记、失败文案指不指得到下一步；
//  2. 生成器不可用 ⇒ delegate_meta 从工具面消失，且原因可在 Surface.Skipped 里读到（ADR-15）。
//
// 为什么用假生成器而不起 Python 子进程：生成质量属 P10 与 5b 夹具，这里钉的是 Go 侧
// 的装配事务；起子进程只会让这组用例依赖解释器与 zipapp 的部署状态。

// plasmaSpecReadOnly 是一条能干净通过 Compile 的最小 spec（只读能力，提示里点名了工具
// 与 agent_exit）。与 plasma 包的 golden 用例同形，改这里等于改"什么算合法 bundle"。
const plasmaSpecReadOnly = `{
  "thinking": "需要只读审计能力",
  "agent_name": "Security Auditor",
  "agent_design": "You are a security auditor. Use read_file and search_by_regex to inspect code, then call agent_exit with reason and a handoff conclusion.",
  "tools_used": ["read_file", "search_by_regex"],
  "task_for_agent": "审计 internal/foo 的注入风险"
}`

// fakePlasmaGenerator 是生成器的假实现：只回答"这次拿到哪个 bundle"。
type fakePlasmaGenerator struct {
	bundle *plasma.Bundle
	report *plasma.Report
	err    error

	calls    int
	lastTask string
	lastNote string
}

func (g *fakePlasmaGenerator) Generate(_ context.Context, task, contextNote string) (*TrampolineResult, error) {
	g.calls++
	g.lastTask, g.lastNote = task, contextNote
	if g.err != nil {
		return nil, g.err
	}
	return &TrampolineResult{Bundle: g.bundle, Report: g.report}, nil
}

// fakeFixtureRunner 桩掉 5a：这些用例要的是"5a 的结论如何进入判定"，不是重跑执行循环。
type fakeFixtureRunner struct {
	failID string
	reason string
}

func (r fakeFixtureRunner) RunStructural(_ context.Context, _ *plasma.LinkPlan, f plasma.Fixture) (plasma.Outcome, error) {
	if r.failID != "" && r.failID == f.ID {
		return plasma.Outcome{FixtureID: f.ID, Reason: r.reason}, nil
	}
	return plasma.Outcome{FixtureID: f.ID, Passed: true}, nil
}

func (r fakeFixtureRunner) RunBehavioral(_ context.Context, _ *plasma.LinkPlan, f plasma.Fixture) (plasma.Outcome, error) {
	return plasma.Outcome{FixtureID: f.ID, Passed: true}, nil
}

// mustElaborate 把最小 spec 走一遍扩充，得到一份能交给 Link 的 bundle。
func mustElaborate(t *testing.T, spec string) *plasma.Bundle {
	t.Helper()
	b, err := plasma.Elaborate([]byte(spec), plasma.DefaultDefaults())
	if err != nil {
		t.Fatalf("Elaborate: %v", err)
	}
	return b
}

// newPlasmaDirector 装配一个"生成器已接上"的 Director：产物仓与夹具执行器都指到测试内，
// 否则一次调用会把 bundle 写进用户 home（~/.yagent/agents）。
func newPlasmaDirector(t *testing.T, gen PlasmaGenerator, runner plasma.FixtureRunner) *DirectorAgent {
	t.Helper()
	gctx := newTestGlobalCtx(t.TempDir())
	a := newDirectorAgentForTest(gctx, &mockEngine{}, nil, nil, nil, gen, nil, nil, 10, nil, 3, config.Config{}, nil)
	a.plasmaStore = plasma.NewStore(t.TempDir())
	a.plasmaRunner = runner
	return a
}

// delegateAdapter 按名字取 Director 当前工具面上的一个执行器。
func delegateAdapter(t *testing.T, a *DirectorAgent, name string) *tools.Adapter {
	t.Helper()
	for _, ad := range a.surface.Adapters() {
		if ad.Name() == name {
			return ad
		}
	}
	t.Fatalf("工具面上没有 %s（现有：%v）", name, a.surface.Names())
	return nil
}

// callDelegateMeta 调一次 delegate_meta 并把结果解回字符串（Adapter.Call 返回的是 JSON 编码值）。
func callDelegateMeta(t *testing.T, ad *tools.Adapter, payload string) (string, error) {
	t.Helper()
	raw, err := ad.Call(context.Background(), payload)
	if err != nil {
		return "", err
	}
	var out string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("结果不是字符串：%v（原文 %s）", err, raw)
	}
	return out, nil
}

// TestDelegateMeta_RegistersAndRunsGeneratedAgent 钉完整事务：注册（可当场委派）、
// 执行参数来自 LinkPlan、产物落盘、结果文案说明 grade 与 hash。
func TestDelegateMeta_RegistersAndRunsGeneratedAgent(t *testing.T) {
	gen := &fakePlasmaGenerator{bundle: mustElaborate(t, plasmaSpecReadOnly)}
	director := newPlasmaDirector(t, gen, fakeFixtureRunner{})
	before := len(director.surface.Adapters())

	ad := delegateAdapter(t, director, "delegate_meta")
	out, err := callDelegateMeta(t, ad, `{"task":"审计 internal/foo 的注入风险"}`)
	if err != nil {
		t.Fatalf("delegate_meta 失败：%v", err)
	}

	if gen.calls != 1 {
		t.Errorf("Generate 调用次数 = %d，want 1", gen.calls)
	}
	if gen.lastTask != "审计 internal/foo 的注入风险" {
		t.Errorf("交给生成器的 task = %q", gen.lastTask)
	}

	ca, ok := director.metaHandler.Get("delegate_security_auditor")
	if !ok {
		t.Fatal("生成的 Agent 没有注册成 delegate_security_auditor")
	}
	if ca.DisplayName != "Security Auditor" {
		t.Errorf("DisplayName = %q", ca.DisplayName)
	}
	if _, found := director.executorConfigFor("security_auditor"); !found {
		t.Error("执行参数没有登记：runCustomAgent 会回落历史硬编码（MaxSteps=15 硬写）")
	}
	if got := len(director.surface.Adapters()); got != before+1 {
		t.Errorf("工具面适配器数量 = %d，want %d（注册后本回合就可见）", got, before+1)
	}
	delegateAdapter(t, director, "delegate_security_auditor")

	if entries := director.plasmaStore.List(); len(entries) == 0 {
		t.Error("产物没有落盘：重启后这个 Agent 会凭空消失")
	}
	for _, want := range []string{"delegate_security_auditor", "Grade: provisional", "Hash:"} {
		if !strings.Contains(out, want) {
			t.Errorf("结果文案缺 %q：\n%s", want, out)
		}
	}
}

// TestDelegateMeta_DuplicateRegistrationPrevented 钉"同名不覆盖"：bundle 不可变，
// 工具执行器也不能被悄悄换掉。
func TestDelegateMeta_DuplicateRegistrationPrevented(t *testing.T) {
	gen := &fakePlasmaGenerator{bundle: mustElaborate(t, plasmaSpecReadOnly)}
	director := newPlasmaDirector(t, gen, fakeFixtureRunner{})
	ad := delegateAdapter(t, director, "delegate_meta")

	if _, err := callDelegateMeta(t, ad, `{"task":"第一次"}`); err != nil {
		t.Fatalf("第一次调用失败：%v", err)
	}
	adapters, registered := len(director.surface.Adapters()), director.metaHandler.Len()

	out, err := callDelegateMeta(t, ad, `{"task":"第二次"}`)
	if err != nil {
		t.Fatalf("第二次调用失败：%v", err)
	}
	if got := len(director.surface.Adapters()); got != adapters {
		t.Errorf("重复注册改了工具面：%d → %d", adapters, got)
	}
	if director.metaHandler.Len() != registered {
		t.Errorf("重复注册改了注册表：%d → %d", registered, director.metaHandler.Len())
	}
	if !strings.Contains(out, "已注册") {
		t.Errorf("第二次调用应说明复用现有 Agent：\n%s", out)
	}
}

// TestDelegateMeta_GenerateFailureIsActionable 钉错误文案：生成失败必须把原因与
// 下一步带出来，且**不许**再出现只说"格式不对"的历史文案。
func TestDelegateMeta_GenerateFailureIsActionable(t *testing.T) {
	gen := &fakePlasmaGenerator{err: errors.New("python 退出码 1：PLS-201 tools[0] 越权")}
	director := newPlasmaDirector(t, gen, fakeFixtureRunner{})
	ad := delegateAdapter(t, director, "delegate_meta")

	_, err := callDelegateMeta(t, ad, `{"task":"随便"}`)
	if err == nil {
		t.Fatal("生成失败必须报错，而不是返回一份空结果")
	}
	msg := err.Error()
	if !strings.Contains(msg, "plasma 生成失败") || !strings.Contains(msg, "PLS-201") {
		t.Errorf("错误没有透出生成侧诊断：%s", msg)
	}
	if strings.Contains(msg, "NOT valid JSON") || strings.Contains(msg, "not valid JSON") {
		t.Errorf("错误退回了只描述格式的历史文案：%s", msg)
	}
	if director.metaHandler.Len() != 0 {
		t.Error("生成失败时不应注册任何 Agent")
	}
}

// TestDelegateMeta_LinkRejectsEscalatedTools 钉"越权工具在装配复核前就被拦住"：
// run_bash 在 PlasmaPalette 之外（ADR-4），错误要带诊断头与修复方向。
func TestDelegateMeta_LinkRejectsEscalatedTools(t *testing.T) {
	spec := strings.Replace(plasmaSpecReadOnly, `["read_file", "search_by_regex"]`, `["read_file", "run_bash"]`, 1)
	gen := &fakePlasmaGenerator{bundle: mustElaborate(t, spec)}
	director := newPlasmaDirector(t, gen, fakeFixtureRunner{})
	ad := delegateAdapter(t, director, "delegate_meta")

	_, err := callDelegateMeta(t, ad, `{"task":"随便"}`)
	if err == nil {
		t.Fatal("越权工具必须被拦下")
	}
	msg := err.Error()
	if !strings.Contains(msg, "装配复核") {
		t.Errorf("错误应指出这是装配复核阶段：%s", msg)
	}
	if !strings.Contains(msg, "修复") && !strings.Contains(msg, "重跑 delegate_meta") {
		t.Errorf("错误没有可执行的下一步：%s", msg)
	}
	if director.metaHandler.Len() != 0 {
		t.Error("越权 bundle 不应注册")
	}
}

// TestDelegateMeta_StructuralFixtureFailureBlocksRegistration 钉 5a 是**在线必跑**：
// 结构夹具红 ⇒ 不注册、不落盘、错误里带夹具原因。
func TestDelegateMeta_StructuralFixtureFailureBlocksRegistration(t *testing.T) {
	gen := &fakePlasmaGenerator{bundle: mustElaborate(t, plasmaSpecReadOnly)}
	reason := "stop_kind = max_steps，want agent_exit（提示里没有 agent_exit 契约？）"
	director := newPlasmaDirector(t, gen, fakeFixtureRunner{failID: plasma.FixtureExitContract, reason: reason})
	ad := delegateAdapter(t, director, "delegate_meta")

	_, err := callDelegateMeta(t, ad, `{"task":"随便"}`)
	if err == nil {
		t.Fatal("5a 红的 bundle 不许注册")
	}
	if msg := err.Error(); !strings.Contains(msg, reason) {
		t.Errorf("错误没有带出夹具原因：%s", msg)
	}
	if director.metaHandler.Len() != 0 {
		t.Error("5a 红时不应注册")
	}
	if entries := director.plasmaStore.List(); len(entries) != 0 {
		t.Errorf("5a 红时不应落盘，store 里有 %d 条", len(entries))
	}
}

// TestDelegateMeta_GatedByPlasmaAvailability 是 ADR-15 的闸门：生成器不可用 ⇒
// delegate_meta **不进工具面**，且原因必须能在 Surface.Skipped 里读到（不许只进 Debug 日志）。
//
// 反面对照（同一份断言结构、只把生成器接上）证明"消失"确实来自可用性判定，
// 而不是别的副产物 —— 只写前半段的话，一条"delegate_meta 被谁误删"的改动也会让它变绿。
func TestDelegateMeta_GatedByPlasmaAvailability(t *testing.T) {
	const why = "plasma 生成器未装配：未找到 plasma.pyz（P10 前属预期）；修复：等 Python 包落地"

	degraded := newPlasmaDirector(t, nil, fakeFixtureRunner{})
	degraded.plasmaWhy = why
	degraded.rebuildSurface()

	for _, def := range degraded.surface.Defs() {
		if def.Function.Name == "delegate_meta" {
			t.Fatal("生成器不可用时 delegate_meta 不许出现在工具面上")
		}
	}
	found := false
	for _, item := range degraded.surface.Skipped() {
		if item.Name == "delegate_meta" {
			found = true
			if !strings.Contains(item.Reason, "plasma.pyz") || !strings.Contains(item.Reason, "修复") {
				t.Errorf("Skipped 原因不可执行：%q", item.Reason)
			}
		}
	}
	if !found {
		t.Fatalf("降级原因没有进 Surface.Skipped（现有：%v）", degraded.surface.Skipped())
	}

	live := newPlasmaDirector(t, &fakePlasmaGenerator{bundle: mustElaborate(t, plasmaSpecReadOnly)}, fakeFixtureRunner{})
	seen := false
	for _, def := range live.surface.Defs() {
		if def.Function.Name == "delegate_meta" {
			seen = true
		}
	}
	if !seen {
		t.Error("生成器已装配时 delegate_meta 必须在工具面上（否则降级判据把能力藏起来了）")
	}
}

// TestPlasmaAvailableWith_ReasonsAreActionable 把判据本身钉死：三条降级原因各说一句话，
// 且每条都带"怎么修"。没有这组用例，plasmaAvailable 退化成恒 true 也无人发现。
func TestPlasmaAvailableWith_ReasonsAreActionable(t *testing.T) {
	interp := filepath.Join(t.TempDir(), "python3")
	if err := os.WriteFile(interp, []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("准备解释器桩失败：%v", err)
	}
	missingPyz := filepath.Join(t.TempDir(), plasmaPyzName)

	cases := []struct {
		name     string
		cfg      config.PlasmaConfig
		pyz      string
		wantWord string
	}{
		{name: "关闭", cfg: config.PlasmaConfig{Enable: false}, pyz: missingPyz, wantWord: "enable"},
		{name: "解释器写错", cfg: config.PlasmaConfig{Enable: true, Python: interp + "-nope"}, pyz: missingPyz, wantWord: "解释器"},
		{name: "缺 zipapp", cfg: config.PlasmaConfig{Enable: true, Python: interp}, pyz: missingPyz, wantWord: plasmaPyzName},
	}
	for _, sc := range cases {
		t.Run(sc.name, func(t *testing.T) {
			ok, reason := plasmaAvailableWith(sc.cfg, func() (string, error) { return sc.pyz, nil })
			if ok {
				t.Fatalf("判定为可用（want 不可用），reason=%q", reason)
			}
			if !strings.Contains(reason, sc.wantWord) {
				t.Errorf("原因 %q 没有指出 %q", reason, sc.wantWord)
			}
			if !strings.Contains(reason, "修复") {
				t.Errorf("原因 %q 没有给出修复方向", reason)
			}
		})
	}

	// 三项齐备 ⇒ 可用，且没有原因（否则降级理由会一直挂在工具面上）。
	ok, reason := plasmaAvailableWith(config.PlasmaConfig{Enable: true, Python: interp},
		func() (string, error) { return interp, nil })
	if !ok || reason != "" {
		t.Fatalf("三项齐备时应可用：ok=%v reason=%q", ok, reason)
	}
}
