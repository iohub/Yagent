package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"yagent/internal/agents"
	"yagent/internal/config"
	"yagent/internal/plasma"
)

// 本文件是 `plasma compile` 与 `plasma test` 的执行体。两者的前半完全相同
// （Elaborate → Compile → Link），差别只在"跑全部结构夹具"与"只跑一条"。

func runPlasmaCompile(bundlePath string, behavioral, asJSON bool, out io.Writer) error {
	settings, cfg := plasmaRuntime()
	b, err := loadPlasmaBundle(bundlePath, plasmaDefaults(cfg))
	if err != nil {
		return err
	}

	compiler := &plasma.Compiler{Palette: plasma.PlasmaPalette}
	report, hash, approved := compileWithEscalation(compiler, b, plasma.NewStore(config.PlasmaStoreDir()))

	res := &plasmaCompileResult{
		Bundle:              b.Name,
		Hash:                hash,
		Escalation:          report.EscalationRequired,
		Approved:            approved,
		Diagnostics:         report.Diagnostics,
		BehavioralRequested: behavioral,
	}
	// 诊断文本是给 Python --fix 与人类看的**同一份**（§6.4），所以先原样打出来。
	// 有 error 就不再跑夹具：对着编译不过的 bundle 跑夹具只会掩盖真正的原因。
	if report.HasErrors() {
		if err := renderPlasmaCompile(out, res, asJSON); err != nil {
			return err
		}
		return fmt.Errorf("编译失败：%d 个 error（见上方诊断）", len(report.Errors()))
	}

	plan, err := compiler.Link(b, hash)
	if err != nil {
		return fmt.Errorf("link %s@%s: %w", b.Name, hash, err)
	}
	// 夹具执行要装配真实 Surface，所以 runner 在 agents 侧（plasma 是叶子包）。
	//
	// **Deps 必须留 nil**：agents 侧的 fixtureDeps 在 Deps==nil 时会走
	// plan.Profile.ProbeDeps() 并**注入真实 Flow 执行器**（ProbeDeps 的 Flow 是空实现）。
	// 显式塞一个 ProbeDeps 进去会让 exit-contract 永远解析不出退出 JSON ——
	// 那不是"契约被违反"，而是夹具自己装错了（agents/plasma_fixture.go:210-221）。
	runner := &agents.PlasmaFixtureRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), settings.StructuralFixtureTimeout)
	defer cancel()
	outcomes, vrep := compiler.VerifyStructural(ctx, b, plan, runner)
	res.Outcomes = outcomes
	res.Diagnostics = append(res.Diagnostics, vrep.Diagnostics...)

	var behavioralOutcomes plasma.Outcomes
	if behavioral {
		for _, f := range b.Fixtures.Behavioral {
			res.BehavioralRuns++
			o, runErr := runner.RunBehavioral(ctx, plan, f)
			if runErr != nil && strings.TrimSpace(o.Reason) == "" {
				o.Reason = runErr.Error()
			}
			behavioralOutcomes = append(behavioralOutcomes, o)
		}
		res.Behavioral = behavioralOutcomes
	}

	merged := &plasma.Report{
		Diagnostics:        res.Diagnostics,
		EscalationRequired: report.EscalationRequired,
	}
	res.Grade = plasma.Ratchet(merged, outcomes, behavioralOutcomes, approved)
	if err := renderPlasmaCompile(out, res, asJSON); err != nil {
		return err
	}
	if !outcomes.AllPassed() {
		return fmt.Errorf("%d 条结构夹具失败", len(outcomes.Failed()))
	}
	return nil
}

// runPlasmaTest 只跑 --fixture 指定的那一条。找不到夹具 ID 是用法错（exit 2）。
func runPlasmaTest(bundlePath, fixtureID string, asJSON bool, out io.Writer) error {
	if strings.TrimSpace(fixtureID) == "" {
		return newUsageError("test 需要 --fixture <id>")
	}
	setting, cfg := plasmaRuntime()
	b, err := loadPlasmaBundle(bundlePath, plasmaDefaults(cfg))
	if err != nil {
		return err
	}
	compiler := &plasma.Compiler{Palette: plasma.PlasmaPalette}
	report, hash, approved := compileWithEscalation(compiler, b, plasma.NewStore(config.PlasmaStoreDir()))
	if report.HasErrors() {
		fmt.Fprint(out, report.Text())
		return fmt.Errorf("编译失败：%d 个 error（见上方诊断）", len(report.Errors()))
	}
	plan, err := compiler.Link(b, hash)
	if err != nil {
		return fmt.Errorf("link %s@%s: %w", b.Name, hash, err)
	}
	var fixture *plasma.Fixture
	for i := range b.Fixtures.Structural {
		if b.Fixtures.Structural[i].ID == fixtureID {
			fixture = &b.Fixtures.Structural[i]
			break
		}
	}
	if fixture == nil {
		return newUsageError("这份 bundle 里没有夹具 %q（可用：%s）",
			fixtureID, strings.Join(plasmaFixtureIDs(b), ", "))
	}

	runner := &agents.PlasmaFixtureRunner{} // Deps 留 nil，理由同 runPlasmaCompile
	ctx, cancel := context.WithTimeout(context.Background(), setting.StructuralFixtureTimeout)
	defer cancel()
	o, runErr := runner.RunStructural(ctx, plan, *fixture)
	res := &plasmaCompileResult{
		Bundle:        b.Name,
		Hash:          hash,
		Approved:      approved,
		Escalation:    report.EscalationRequired,
		Diagnostics:   report.Diagnostics,
		Outcomes:      plasma.Outcomes{o},
		SingleFixture: true,
	}
	if runErr != nil && strings.TrimSpace(o.Reason) == "" {
		o.Reason = runErr.Error()
	}
	if err := renderPlasmaCompile(out, res, asJSON); err != nil {
		return err
	}
	if !o.Passed {
		return fmt.Errorf("夹具 %s 失败: %s", o.FixtureID, o.Reason)
	}
	return nil
}
