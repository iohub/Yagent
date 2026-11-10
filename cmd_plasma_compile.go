package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"yagent/internal/agents"
	"yagent/internal/config"
	"yagent/internal/llm"
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
	// 引擎只在"确实有 5b 夹具要跑"时才必须可用：零条 behavior fixtures 时
	// `--behavioral` 的语义是"我已请求 5b、但没有可跑的题"，不是"你要的那一步做不了"。
	// 把这两种情况混成一条会导致一台没配 provider 的机器连"bundle 里没有行为夹具"
	// 都问不出来 —— 而那正是出题阶段最需要看到的答案。
	needEngine := behavioral && b.Fixtures != nil && len(b.Fixtures.Behavioral) > 0
	runner, engineErr := plasmaFixtureRunner(settings, cfg, bundlePath, needEngine)
	ctx, cancel := context.WithTimeout(context.Background(), settings.StructuralFixtureTimeout)
	defer cancel()
	outcomes, vrep := compiler.VerifyStructural(ctx, b, plan, runner)
	res.Outcomes = outcomes
	res.Diagnostics = append(res.Diagnostics, vrep.Diagnostics...)

	if engineErr != nil {
		// 有题要考却拿不到引擎：这不是"未执行"，是"你要的那一步做不了"。
		// 在跑 5b **之前**挡住 —— 否则每条夹具都会先烧一次 ctx 再报"没有引擎"。
		if err := renderPlasmaCompile(out, res, asJSON); err != nil {
			return err
		}
		return fmt.Errorf("--behavioral 需要一份可用的 LLM 配置：%w", engineErr)
	}

	var behavioralOutcomes plasma.Outcomes
	if behavioral {
		behavioralOutcomes = runBehavioralFixtures(b, plan, runner, res)
	}

	merged := &plasma.Report{
		Diagnostics:        res.Diagnostics,
		EscalationRequired: report.EscalationRequired,
	}
	res.Grade = plasma.Ratchet(merged, outcomes, behavioralOutcomes, approved)
	merged.Grade = res.Grade
	merged.Outcomes = append(append(plasma.Outcomes(nil), outcomes...), behavioralOutcomes...)

	// 只有 --behavioral 这条路径落盘：不带它时行为与 P7/P9 完全一致（在线止于
	// provisional、CLI 不写 store）。带了它就是在走 verified 棘轮，而棘轮不落盘
	// 等于每次都要重跑一遍真 LLM。
	if behavioral && res.Grade != "" && outcomes.AllPassed() {
		putHash, putErr := plasma.NewStore(config.PlasmaStoreDir()).Put(b, merged)
		if putErr != nil {
			if err := renderPlasmaCompile(out, res, asJSON); err != nil {
				return err
			}
			return fmt.Errorf("落盘被拒（grade 未生效）：%w", putErr)
		}
		res.Hash = putHash
	}

	if err := renderPlasmaCompile(out, res, asJSON); err != nil {
		return err
	}
	if !outcomes.AllPassed() {
		return fmt.Errorf("%d 条结构夹具失败", len(outcomes.Failed()))
	}
	if behavioral && !behavioralOutcomes.AllPassed() {
		// 5b 红 ⇒ 停在 provisional 并**逐个点名**：一句"行为夹具失败"等于没说。
		return fmt.Errorf("%d 条行为夹具失败（grade 停在 %s）：%s",
			len(behavioralOutcomes.Failed()), gradeLabel(res.Grade), failedFixtureNames(behavioralOutcomes))
	}
	return nil
}

// plasmaEngineFor 是 5b 引擎构造的**唯一**接缝。
//
// 生产走 llm.NewClient；测试注入 `internal/llm/scripted` —— 真 provider 在单测里
// 既不可复现，也不该被调用（与 PlasmaCLI 的接缝同一理由）。抽成变量的另一个
// 理由是 CLI 层要能断言"5b 全过 ⇒ verified"，而那条路径非有引擎不可。
var plasmaEngineFor = func(cfg *config.Config) (llm.Engine, error) {
	if cfg == nil {
		return nil, fmt.Errorf("没有可用的 config.toml（引擎需要 provider）")
	}
	client, err := llm.NewClient(cfg)
	if err != nil {
		return nil, fmt.Errorf("构造客户端: %w", err)
	}
	return client.Engine, nil
}

// plasmaFixtureRunner 装配夹具执行器。
//
// 生产路径上**三样**都必须接上，漏一样就是一条静默的"没跑"：
//   - LLM（5b 的引擎，只由这里注入）；
//   - 双预算（behavioral_token_budget / behavioral_fixture_timeout）；
//   - Metrics（夹具运行也要进账本，且 FixtureRun 由 runner 强制置真）。
//
// needEngine 而不是 behavioral：只有真有 behavior fixtures 要跑时引擎才是必需项。
// 不带 --behavioral、或带了但零条夹具时，CLI 不该因为"这台机器没配 provider"而失败
// —— 5a 与编译是纯本地的。
func plasmaFixtureRunner(settings config.PlasmaConfig, cfg *config.Config, bundlePath string,
	needEngine bool) (*agents.PlasmaFixtureRunner, error) {

	runner := &agents.PlasmaFixtureRunner{
		BehavioralTokenBudget: settings.BehavioralTokenBudget,
		BehavioralTimeout:     settings.BehavioralFixtureTimeout,
		// 夹具 workspace 的相对路径基准是 bundle.json 所在目录（不回落到 CWD）。
		WorkspaceRoot: filepath.Dir(bundlePath),
		Metrics:       agents.PlasmaTelemetrySink(config.PlasmaStoreDir()),
	}
	if !needEngine {
		return runner, nil
	}
	engine, err := plasmaEngineFor(cfg)
	if err != nil {
		return runner, err
	}
	runner.LLM = engine
	return runner, nil
}

// runBehavioralFixtures 逐条跑 5b。每条自带墙钟（runner 内部 WithTimeout），
// 所以这里**不**共用一个总超时：一条慢夹具不该把后面的夹具一起判成超时。
func runBehavioralFixtures(b *plasma.Bundle, plan *plasma.LinkPlan, runner *agents.PlasmaFixtureRunner,
	res *plasmaCompileResult) plasma.Outcomes {

	outcomes := make(plasma.Outcomes, 0, len(b.Fixtures.Behavioral))
	for _, f := range b.Fixtures.Behavioral {
		res.BehavioralRuns++
		o, runErr := runner.RunBehavioral(context.Background(), plan, f)
		if runErr != nil && strings.TrimSpace(o.Reason) == "" {
			o.Reason = runErr.Error()
		}
		outcomes = append(outcomes, o)
	}
	res.Behavioral = outcomes
	return outcomes
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
