package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"yagent/internal/agents"
	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/plasma"
)

// 本文件是 `yagent plasma evolve`：离线进化管线的**驱动器**（§6.8）。
//
// 分工是 ADR-1 的直接结果：Python 产出候选（评分 + 变异），Go 裁决候选
// （编译 1-4 + 5a + 5b + judge）。驱动器只负责把它们串起来并记账，
// **不**判断"这个变异好不好"——那不是 Go 的活。
//
// 三条纪律：
//  1. 门禁在**最前面**：`evolve_enable=false` 立即拒绝（§6.8：它是唯一会在后台改
//     系统行为的开关）。写进 policy.json 之后 Python 侧还会独立再判一次；
//  2. 一个候选失败**不中断整批**：诊断码进该候选的 `lineage.rejected[]`
//     （免费的负样本），其余候选继续跑；
//  3. 本阶段**没有后台调度**：`evolve_interval` 只用于"下次可运行"提示。
//     后台定时器留 P12，并在设计稿 §16 记为偏差。
//
// 落盘条件（全部满足才 Put 成 verified）：5a 全绿 + 谱系分离成立 + 5b 全绿 + judge 判 pass。

// plasmaEvolveOptions 是 `--in/--out/--max-candidates/--dry-run` 的落点。
type plasmaEvolveOptions struct {
	In            string
	Out           string
	MaxCandidates int
	DryRun        bool
}

// plasmaPythonRunner 是离线 Python 子命令的接缝：生产是 agents.PlasmaCLI，
// 测试注入假实现。抽成接口的理由不是"为了可测"这种泛泛之谈，而是**真 Python
// 进程在单测里不可复现**（解释器版本、zipapp 是否重建过都会让用例时红时绿）。
type plasmaPythonRunner interface {
	Run(ctx context.Context, args ...string) ([]byte, error)
}

// evolvePolicy 是写给 Python 的门禁与阈值。它是跨语言契约的一部分：
// 键名改了 Python 侧会读不到，而读不到的表现是"按默认值跑"——所以字段名与
// evolve.py 的 `_load_policy` 一一对应，`policy_schema_version` 变化必须两侧同改。
type evolvePolicy struct {
	SchemaVersion  int `json:"schema_version"`
	EvolveEnable   bool `json:"evolve_enable"`
	PromoteMinRuns int `json:"promote_min_runs"`
	TokenBudget    int `json:"behavioral_token_budget"`
}

const (
	evolvePolicySchemaVersion = 1
	evolvePolicyFileName      = "policy.json"
)

func runPlasmaEvolve(opts plasmaEvolveOptions, out io.Writer) error {
	settings, cfg := plasmaRuntime()
	if !settings.EvolveEnable {
		// 不是用法错（exit 2）：命令用法完全正确，只是这个开关关着。
		return fmt.Errorf("进化默认关闭（[plasma] evolve_enable=false）；" +
			"要运行请在 config.toml 里显式打开 evolve_enable 后重跑")
	}
	py, err := plasmaPython(settings)
	if err != nil {
		return err
	}
	var engine llm.Engine
	if candidate, engineErr := plasmaEngineFor(cfg); engineErr == nil {
		engine = candidate
	} else {
		// 不是门禁：没有引擎时 5b 会记"未验证"，于是所有候选都停在 5b 而非被静默
		// 判成 verified。让人看见这条 WARN 比让整批进化失败更有用。
		fmt.Fprintf(os.Stderr, "yagent plasma evolve: 无法构造引擎，5b 将记为未验证（%v）\n", engineErr)
	}
	return runPlasmaEvolveWith(py, engine, settings, cfg, opts, out)
}

// runPlasmaEvolveWith 是去掉外部依赖之后的执行体（门禁、管线、渲染）。
func runPlasmaEvolveWith(py plasmaPythonRunner, engine llm.Engine, settings config.PlasmaConfig,
	cfg *config.Config, opts plasmaEvolveOptions, out io.Writer) error {

	storeDir := config.PlasmaStoreDir()
	store := plasma.NewStore(storeDir)
	telemetry := strings.TrimSpace(opts.In)
	if telemetry == "" {
		telemetry = filepath.Join(storeDir, "telemetry.jsonl")
	}
	if _, err := os.Stat(telemetry); err != nil {
		// 缺账本是"还没跑过生产"，不是命令用错：说清楚下一步而不是抛 exit 2。
		return fmt.Errorf("读不到遥测账本 %s（%v）；先跑几次生产委派再进化", telemetry, err)
	}
	outDir := strings.TrimSpace(opts.Out)
	if outDir == "" {
		outDir = filepath.Join(storeDir, "evolve")
	}
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return fmt.Errorf("建 evolve 输出目录 %s: %w", outDir, err)
	}
	if err := writeEvolvePolicy(outDir, settings); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), settings.CompileTimeout*4)
	defer cancel()

	maxCandidates := opts.MaxCandidates
	if maxCandidates <= 0 {
		maxCandidates = 3
	}
	raw, err := py.Run(ctx, "evolve",
		"--in", telemetry,
		"--store", storeDir,
		"--out", outDir,
		"--max-candidates", fmt.Sprintf("%d", maxCandidates))
	if err != nil {
		return fmt.Errorf("plasma evolve（Python 侧）失败: %w", err)
	}
	ledger, err := parseEvolveLedger(raw)
	if err != nil {
		return err
	}

	res := &plasmaEvolveResult{
		OutDir:         outDir,
		Telemetry:      telemetry,
		DryRun:         opts.DryRun,
		SamplesUsed:    ledger.SamplesUsed,
		ExcludedFixRun: ledger.ExcludedFixtureRuns,
		Groups:         ledger.Groups,
		NextRunHint:    nextEvolveHint(settings),
	}
	runner := &agents.PlasmaFixtureRunner{
		LLM:                   engine,
		BehavioralTokenBudget: settings.BehavioralTokenBudget,
		BehavioralTimeout:     settings.BehavioralFixtureTimeout,
		// 夹具运行也进账本（FixtureRun 由 runner 强制置真）：评分侧只统计 false，
		// 所以这些行只会出现在"夹具样本"那一栏，不会抬高生产成功率。
		Metrics: agents.PlasmaTelemetrySink(storeDir),
	}
	for _, cand := range ledger.Candidates {
		res.Candidates = append(res.Candidates, evaluateEvolveCandidate(
			ctx, py, runner, store, cand, cfg, settings, outDir, opts.DryRun))
	}
	for _, skip := range ledger.Skipped {
		res.Skipped = append(res.Skipped, skip.Agent+"@"+shortHash(skip.BundleHash)+"："+skip.Reason)
	}
	return renderPlasmaEvolve(out, res)
}

// evaluateEvolveCandidate 跑完一个候选的全部裁决步骤。失败只记账，不返回 error
// —— 一个候选被拒是**预期结果**（`lineage.rejected[]` 的存在理由），
// 把它变成 error 会让整批因为一个坏变异停下。
func evaluateEvolveCandidate(ctx context.Context, py plasmaPythonRunner, runner *agents.PlasmaFixtureRunner,
	store *plasma.Store, cand evolveCandidate, cfg *config.Config, settings config.PlasmaConfig,
	outDir string, dryRun bool) plasmaEvolveCandidate {

	res := plasmaEvolveCandidate{ID: cand.ID, Mutation: cand.Mutation, Path: cand.Path}
	reject := func(reasons ...string) plasmaEvolveCandidate {
		res.Rejected = append(res.Rejected, reasons...)
		res.Grade = ""
		return res
	}

	raw, err := os.ReadFile(cand.Path)
	if err != nil {
		return reject("读不到候选文件: " + err.Error())
	}
	b, err := plasma.Elaborate(raw, agents.PlasmaDefaults(cfg))
	if err != nil {
		return reject("Elaborate 失败: " + err.Error())
	}
	compiler := &plasma.Compiler{Palette: plasma.PlasmaPalette}
	report := compiler.Compile(ctx, b)
	if report.HasErrors() {
		res.DiagnosticCodes = diagCodes(report.Errors())
		return reject("编译失败: " + strings.Join(res.DiagnosticCodes, ","))
	}
	hash, err := plasma.HashBundle(b)
	if err != nil {
		return reject("计算 hash 失败: " + err.Error())
	}
	plan, err := compiler.Link(b, hash)
	if err != nil {
		return reject("Link 失败: " + err.Error())
	}
	structural, srep := compiler.VerifyStructural(ctx, b, plan, runner)
	res.Structural = structural
	if failed := structural.Failed(); len(failed) > 0 {
		res.DiagnosticCodes = diagCodes(srep.Errors())
		return reject("5a 失败: " + strings.Join(res.DiagnosticCodes, ","))
	}

	// 出题：独立谱系（ADR-5 / T3）。author-fixtures 的 provenance 是
	// "author-fixtures"，而候选的 lineage 是 "evolve:<kind>"，两者天然不同 ——
	// 但**不靠这条天然**：下面用 plasma.CheckFixtureProvenance 正面判一次。
	fixtures, provenance, err := authorBehavioralFixtures(ctx, py, cand.Path, outDir, cand.ID)
	if err != nil {
		return reject("出题失败: " + err.Error())
	}
	if len(fixtures) == 0 {
		return reject("出题没有产出任何行为夹具（没有考卷就没有 verified）")
	}
	if b.Fixtures == nil {
		b.Fixtures = &plasma.Fixtures{}
	}
	b.Fixtures.Behavioral = fixtures
	b.Fixtures.Provenance = provenance
	if err := plasma.CheckFixtureProvenance(b); err != nil {
		res.DiagnosticCodes = append(res.DiagnosticCodes, plasma.CodeFixtureProvenance)
		return reject("谱系分离失败: " + err.Error())
	}

	// 夹具进 hash：加了 behavior fixtures 之后必须重新编译 + 重新 Link，
	// 否则落盘的主键与实际内容不是同一份（与 verifyPlasmaBundle 同一理由）。
	report = compiler.Compile(ctx, b)
	if report.HasErrors() {
		res.DiagnosticCodes = diagCodes(report.Errors())
		return reject("加夹具后编译失败: " + strings.Join(res.DiagnosticCodes, ","))
	}
	hash, err = plasma.HashBundle(b)
	if err != nil {
		return reject("计算 hash 失败: " + err.Error())
	}
	plan, err = compiler.Link(b, hash)
	if err != nil {
		return reject("加夹具后 Link 失败: " + err.Error())
	}

	behavioral := make(plasma.Outcomes, 0, len(fixtures))
	for _, f := range fixtures {
		o, runErr := runner.RunBehavioral(ctx, plan, f)
		if runErr != nil && strings.TrimSpace(o.Reason) == "" {
			o.Reason = runErr.Error()
		}
		behavioral = append(behavioral, o)
	}
	res.Behavioral = behavioral

	merged := &plasma.Report{
		Diagnostics:        report.Diagnostics,
		EscalationRequired: report.EscalationRequired,
	}
	grade := plasma.Ratchet(merged, structural, behavioral, false)
	res.Grade = grade

	if !behavioral.AllPassed() {
		return reject("5b 未全绿: " + failedFixtureNames(behavioral))
	}

	// judge 是**独立**的一道：5b 全绿只说明契约成立，分数与判决由 Python 侧给。
	verdict, err := judgeOutcomes(ctx, py, outDir, cand.ID, b, hash, behavioral)
	if err != nil {
		return reject("judge 失败: " + err.Error())
	}
	res.Judge = verdict
	if verdict.Verdict != "pass" {
		return reject("judge 判 fail（score=" + fmt.Sprintf("%d", verdict.Score) + "）: " +
			strings.Join(verdict.Reasons, "；"))
	}
	if grade != plasma.GradeVerified {
		return reject("棘轮只到 " + gradeLabel(grade) + "（升级未批准或 5b 未全绿）")
	}

	if dryRun {
		res.Note = "dry-run：未落盘（正式跑会 Put 成 verified）"
		return res
	}
	merged.Grade = grade
	merged.Outcomes = append(append(plasma.Outcomes(nil), structural...), behavioral...)
	putHash, err := store.Put(b, merged)
	if err != nil {
		return reject("落盘被拒: " + err.Error())
	}
	res.Hash = putHash
	res.Grade = grade
	return res
}

// authorBehavioralFixtures 调 Python 出题并把产物转成 IR 形状。
func authorBehavioralFixtures(ctx context.Context, py plasmaPythonRunner, bundlePath, outDir, id string) ([]plasma.Fixture, plasma.Provenance, error) {
	target := filepath.Join(outDir, "fixtures", id+".json")
	raw, err := py.Run(ctx, "author-fixtures", "--bundle", bundlePath, "--out", target)
	if err != nil {
		return nil, plasma.Provenance{}, err
	}
	var payload struct {
		Provenance plasma.Provenance `json:"provenance"`
		Fixtures   []plasma.Fixture  `json:"fixtures"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, plasma.Provenance{}, fmt.Errorf("author-fixtures 的 stdout 不是合法 JSON: %w", err)
	}
	return payload.Fixtures, payload.Provenance, nil
}

// judgeOutcomes 把观测写成 outcomes.json 再交给 Python judge。
func judgeOutcomes(ctx context.Context, py plasmaPythonRunner, outDir, id string,
	b *plasma.Bundle, hash string, behavioral plasma.Outcomes) (*plasmaJudgeVerdict, error) {

	dir := filepath.Join(outDir, "judge")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	in := filepath.Join(dir, id+"-outcomes.json")
	if err := writeJSONFile(in, evolveOutcomes{
		SchemaVersion: 1,
		Agent:         b.Name,
		BundleHash:    hash,
		Fixtures:      toOutcomesJSON(behavioral),
	}); err != nil {
		return nil, err
	}
	out := filepath.Join(dir, id+"-verdict.json")
	raw, err := py.Run(ctx, "judge", "--in", in, "--out", out)
	if err != nil {
		// exit 1 = verdict fail：Python 仍然把判决写出来了，所以这里**不是**错误，
		// 只有"没有判决可用"才是错误。判据是 stdout 能不能解析。
		if verdict, ok := parseJudgeVerdict(raw); ok {
			return verdict, nil
		}
		return nil, err
	}
	verdict, ok := parseJudgeVerdict(raw)
	if !ok {
		return nil, fmt.Errorf("judge 的 stdout 不是可解析的判决 JSON")
	}
	return verdict, nil
}

func writeEvolvePolicy(outDir string, settings config.PlasmaConfig) error {
	return writeJSONFile(filepath.Join(outDir, evolvePolicyFileName), evolvePolicy{
		SchemaVersion:  evolvePolicySchemaVersion,
		EvolveEnable:   settings.EvolveEnable,
		PromoteMinRuns: settings.PromoteMinRuns,
		TokenBudget:    settings.BehavioralTokenBudget,
	})
}

func writeJSONFile(path string, v interface{}) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}

// plasmaPython 构造生产执行器：解释器与 zipapp 的判据与 delegate_meta 同一份
// （plasmaInterpreter / PlasmaZipappPath），否则"生成器可用"与"进化可用"会漂。
func plasmaPython(settings config.PlasmaConfig) (plasmaPythonRunner, error) {
	pyz, err := agents.PlasmaZipappPath()
	if err != nil {
		return nil, fmt.Errorf("定位 plasma.pyz 失败: %w（修复：重装/重编译，内嵌二进制要解到 ~/.yagent/bin）", err)
	}
	if info, statErr := os.Stat(pyz); statErr != nil || info.IsDir() {
		return nil, fmt.Errorf("plasma.pyz 不存在：%s（修复：bash scripts/build_plasma.sh 后重编译）", pyz)
	}
	return &agents.PlasmaCLI{Python: settings.Python, Pkg: pyz}, nil
}

// nextEvolveHint 是 §6.8 要求的"下次可运行"提示。本阶段**没有**后台定时器：
// 提示是真话（间隔到了该手动跑），而不是假装有个定时器在等。
func nextEvolveHint(settings config.PlasmaConfig) string {
	if settings.EvolveInterval <= 0 {
		return "未配置 evolve_interval（0 = 不提示）"
	}
	return fmt.Sprintf("下次建议运行时间：%s（evolve_interval=%s；本阶段无后台调度，需手动触发）",
		time.Now().UTC().Add(settings.EvolveInterval).Format(time.RFC3339), settings.EvolveInterval)
}

func diagCodes(diags []plasma.Diagnostic) []string {
	out := make([]string, 0, len(diags))
	for _, d := range diags {
		out = append(out, d.Code)
	}
	return out
}

func failedFixtureNames(o plasma.Outcomes) string {
	failed := o.Failed()
	parts := make([]string, 0, len(failed))
	for _, item := range failed {
		parts = append(parts, item.FixtureID)
	}
	return strings.Join(parts, ",")
}

func gradeLabel(g plasma.Grade) string {
	if g == "" {
		return "(none)"
	}
	return string(g)
}
