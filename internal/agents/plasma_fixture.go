package agents

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"yagent/internal/compression"
	"yagent/internal/llm"
	"yagent/internal/llm/scripted"
	"yagent/internal/plasma"
	"yagent/internal/tools"
	"yagent/internal/toolspec"
)

// 本文件实现 plasma.FixtureRunner（阶段 5a 的执行侧）。
//
// 为什么实现在 agents 而不在 plasma：跑夹具要 Surface、RunAgentLoop 与 scripted 引擎，
// 这三样都在 plasma 的import 白名单之外（叶子约束）。plasma 只声明窄接口，
// "决定要什么"与"怎么装出来"因此不互相污染。
//
// 两类夹具走两条路，不许混：
//   - 静态断言（tool/palette-containment）只装配 Surface，**不建引擎、不跑循环**；
//   - 行为夹具（exit-contract / step-budget）在**生产路径**上跑 RunAgentLoop，
//     模型由 scripted 按固定身份回放（ADR-12）。
type PlasmaFixtureRunner struct {
	// Deps 是装配依赖；nil 时用 plan.Profile.ProbeDeps()（能力齐备、与 golden 同形）。
	Deps *toolspec.Deps
	// Publisher 可为 nil：夹具不污染生产统计与 TUI 事件流。
	Publisher EventBus
	// Demote 是与生产一致的降级名单（可选）。档位不影响可用性，但会影响
	// "哪些工具只出现在目录里"，所以夹具要能复刻生产画像。
	Demote []string
	// Metrics 是夹具运行的遥测出口（nil = 不写）。生产**不设**它：夹具跑在 CLI 与
	// 编译期，不是一次委派。P11 的 5b 与 `yagent plasma test` 设上它，账本里才会有
	// `fixture_run=true` 的记录，评分侧（只统计 false）才分得开这两类样本。
	//
	// FixtureRun 由本文件**强制**置 true，调用方不需要记得 —— 一个需要调用方记住的
	// 标记迟早会在某条新路径上漏掉，而漏掉的表现是"夹具样本污染生产评分"。
	Metrics func(DelegationMetrics)

	// LLM 是 5b 的行为夹具引擎（生产传真引擎、测试传 `internal/llm/scripted`）。
	//
	// **只由调用方注入**：runner 自己 new 一个引擎的后果是"测试跑 scripted、生产跑的
	// 是另一个"，而两侧的差异永远不会在任何一条用例里出现。nil ⇒ 5b 报"未验证"，
	// 绝不静默通过（ADR-3：超预算/未执行都不许白送 verified）。
	LLM llm.Engine
	// BehavioralTokenBudget 是 5b 的 token 预算（[plasma] behavioral_token_budget）。
	// **<=0 = 没配**，5b 直接判"未验证"：把它当"不限"等于让一次忘配变成一次白送的 verified。
	BehavioralTokenBudget int
	// BehavioralTimeout 是单条 5b 的墙钟上限（[plasma] behavioral_fixture_timeout）。
	// <=0 = 只受调用方 ctx 约束；生产一律显式配上（双预算是 ADR-3 的补偿控制）。
	BehavioralTimeout time.Duration
	// WorkspaceRoot 是夹具 `workspace` 相对路径的基准目录（生产上是 bundle.json
	// 所在目录）。空 ⇒ 相对路径的 workspace 直接报错，不回落到 CWD。
	WorkspaceRoot string
}

// 编译期确认：接口漂移在 build 阶段就红，而不是等到某个调用点。
var _ plasma.FixtureRunner = (*PlasmaFixtureRunner)(nil)

// RunStructural 执行一条结构夹具。
func (r *PlasmaFixtureRunner) RunStructural(ctx context.Context, plan *plasma.LinkPlan, f plasma.Fixture) (plasma.Outcome, error) {
	if plan == nil {
		return plasma.Outcome{FixtureID: f.ID, Reason: "LinkPlan 为 nil：没有可装配的计划"}, nil
	}
	switch f.ID {
	case plasma.FixtureToolContainment:
		return r.checkToolContainment(plan), nil
	case plasma.FixturePaletteContainment:
		return r.checkPaletteContainment(plan), nil
	}
	if len(f.Script) == 0 {
		// 既不是四个内建静态断言之一，也没有可回放脚本：这是**作者写错了夹具**，
		// 不是"通过"。给一条能直接改的原因，而不是静默绿。
		return plasma.Outcome{
			FixtureID: f.ID,
			Reason:    "结构夹具没有脚本，ID 也不在四个内建断言里（exit-contract/step-budget/tool-containment/palette-containment）",
		}, nil
	}
	return r.runScript(ctx, plan, f), nil
}

// RunBehavioral 的实现在 plasma_behavioral.go（阶段 5b）：它要沙箱 + 双预算，
// 与 5a 的四条断言不是同一种东西，混在一个文件里会让"5a 用脚本、5b 用真引擎"
// 这条分工变得不明显。

// checkToolContainment 断言 Surface 恰好是 plan.Agent.Tools ∪ {agent_exit}。
//
// 用 Names() 而不是 Defs()：Defs() 会隐藏 Cold 工具（deferred 目录），把
// "档位降级"误判成"少了一个工具"。档位是装配细节，工具集合才是契约。
func (r *PlasmaFixtureRunner) checkToolContainment(plan *plasma.LinkPlan) plasma.Outcome {
	out := plasma.Outcome{FixtureID: plasma.FixtureToolContainment}
	surface := r.surfaceOf(plan)
	extra, missing := diffNames(surface.Names(), wantSurfaceNames(plan))
	if len(extra) == 0 && len(missing) == 0 {
		out.Passed = true
		return out
	}
	parts := make([]string, 0, 2)
	if len(extra) > 0 {
		parts = append(parts, "多余: "+strings.Join(extra, ", "))
	}
	if len(missing) > 0 {
		parts = append(parts, "缺失: "+strings.Join(missing, ", ")+skippedReasons(surface, missing))
	}
	out.Reason = "Surface 与声明工具面不一致（" + strings.Join(parts, "；") + "）"
	return out
}

// checkPaletteContainment 断言每个工具的 Effect 都落在 effective palette 内。
//
// 判据与阶段 3 的 typecheck 同源：`plasma.BeyondBase(spec.Effect, plan.Profile.Palette)`，
// 不在这里另写一套能力数学。未注册的名字跳过 —— 幻觉名由阶段 2 的 PLS-101 负责，
// 一条规则只报一次。
func (r *PlasmaFixtureRunner) checkPaletteContainment(plan *plasma.LinkPlan) plasma.Outcome {
	out := plasma.Outcome{FixtureID: plasma.FixturePaletteContainment}
	palette := plan.Profile.Palette
	if palette == 0 {
		// 不给默认值兜底：那会把"Link 忘了注入 effective palette"变成一条永远绿的夹具。
		out.Reason = "plan.Profile.Palette 为零：Link 必须注入 effective palette（Palette|EscalationApproved）"
		return out
	}
	var offenders []string
	for _, name := range plan.Profile.Names {
		spec, ok := toolspec.Get(name)
		if !ok {
			continue
		}
		if beyond := plasma.BeyondBase(spec.Effect, palette); beyond != 0 {
			offenders = append(offenders, fmt.Sprintf("%s（越界能力 %v）", name, beyond.Names()))
		}
	}
	if len(offenders) == 0 {
		out.Passed = true
		return out
	}
	out.Reason = fmt.Sprintf("工具能力超出 effective palette %v：%s",
		palette.Names(), strings.Join(offenders, "；"))
	return out
}

// runScript 在真实循环上回放一段脚本，再用通用判据求值 Expect。
func (r *PlasmaFixtureRunner) runScript(ctx context.Context, plan *plasma.LinkPlan, f plasma.Fixture) plasma.Outcome {
	steps := make([]scripted.Step, 0, len(f.Script))
	for i, s := range f.Script {
		switch {
		case strings.TrimSpace(s.Tool) != "" && strings.TrimSpace(s.Text) != "":
			// 一条响应里既有正文又有工具调用：exit-contract 必须是这个形状。
			// "先纯文本、后退出"的两步脚本在循环里走不到第二步（纯文本即收尾）。
			steps = append(steps, scripted.TextAndToolCall(s.Text, s.Tool, s.Args))
		case strings.TrimSpace(s.Tool) != "":
			steps = append(steps, scripted.ToolCall(s.Tool, s.Args))
		case strings.TrimSpace(s.Text) != "":
			// Fixed 把一条预构造响应包成 Step：TextResponse 返回的是 *llm.Response，
			// 直接 append 到 []Step 里是类型错误（编译期就挡住）。
			steps = append(steps, scripted.Fixed(scripted.TextResponse(s.Text)))
		default:
			return plasma.Outcome{FixtureID: f.ID,
				Reason: fmt.Sprintf("script[%d] 既没有 tool 也没有 text", i)}
		}
	}
	// 固定身份：生成出来的 prompt 首行不可预测，嗅探必然落 unknown（ADR-12 硬约束 4）。
	engine := scripted.NewForIdentity(plan.Agent.Name, steps)

	cfg := DefaultExecutorConfig()
	// SystemPrompt 原样用 bundle 的：夹具验的是"这份 prompt 装进真实循环会怎样"，
	// 再经一次 FormatPrompt 就把被验对象换成了模板的输出。
	cfg.SystemPrompt = plan.Agent.SystemPrompt
	cfg.UserInput = plasma.FixtureInput
	cfg.Surface = r.surfaceOf(plan)
	cfg.LLM = engine
	cfg.Publisher = r.Publisher
	cfg.AgentName = plan.Agent.DisplayName
	// 四个旋钮全部取自 LinkPlan：夹具存在的理由之一就是证明 bundle.runtime 真的
	// 被读走了（写死默认值会让 step-budget 永远绿）。
	cfg.MaxSteps = plan.Config.MaxSteps
	cfg.StopOnFinish = plan.Config.StopOnFinish
	cfg.LLMTimeout = plan.Config.LLMTimeout
	cfg.MaxParallelReadOnlyTools = plan.Config.MaxParallelReadOnlyTools
	cfg.Compression = compression.Options{
		Enabled:    plan.Config.CompressionEnable,
		KeepTokens: plan.Config.KeepToolResultTokens,
	}

	exec, err := RunAgentLoop(ctx, cfg)
	kind := stopKindFor(exec, err, len(engine.Calls()), cfg.MaxSteps)
	stepsDone := exec.Steps
	if stepsDone == 0 && kind == StopKindMaxSteps {
		// 预算耗尽是错误路径：executor 只返回 error 与零值结果（它不伪造成功结果）。
		// 对报告来说"跑了多少步"仍然是一个事实，用预算值补上。
		stepsDone = cfg.MaxSteps
	}

	passed, reason := evaluateExpect(exec, stepsDone, f.Expect, kind)
	if !passed && err != nil && !strings.Contains(reason, err.Error()) {
		reason += "；循环返回错误: " + err.Error()
	}
	out := plasma.Outcome{
		FixtureID: f.ID,
		Passed:    passed,
		Reason:    reason,
		Steps:     stepsDone,
		TokensIn:  int(exec.Usage.PromptTokens),
		TokensOut: int(exec.Usage.CompletionTokens),
		StopKind:  kind,
		// Rubric / DeliverableChars 与 5b 同一口径：5a 的观测同样会进 outcomes.json，
		// 评分侧读到的两条通道必须同形，否则 judge 要对两种输入写两份解析。
		Rubric:           append([]string(nil), f.Rubric...),
		DeliverableChars: utf8.RuneCountInString(strings.TrimSpace(deliverableText(exec))),
	}
	r.emitFixtureMetrics(plan, f, out, err)
	return out
}

// emitFixtureMetrics 把一次夹具运行喂给遥测出口。FixtureRun 在这里**强制**置 true：
// 夹具样本一旦混进生产评分，`promote_min_runs` 与成功率都会被自己的出题动作抬高（Goodhart）。
func (r *PlasmaFixtureRunner) emitFixtureMetrics(plan *plasma.LinkPlan, f plasma.Fixture, out plasma.Outcome, err error) {
	if r.Metrics == nil || plan == nil {
		return
	}
	m := DelegationMetrics{
		Tool:       "fixture:" + f.ID,
		Agent:      plan.Agent.Name,
		StopReason: out.StopKind,
		Steps:      out.Steps,
		TokensIn:   int64(out.TokensIn),
		TokensOut:  int64(out.TokensOut),
		HadResult:  out.Passed,
		Err:        err != nil,
		FixtureRun: true, // 由本文件强制，不看调用方
	}
	if err != nil {
		m.ErrText = err.Error()
	}
	r.Metrics(m)
}

// surfaceOf 是只此一处的装配入口：生产、夹具、静态断言都经 surfaceFor。
func (r *PlasmaFixtureRunner) surfaceOf(plan *plasma.LinkPlan) *tools.Surface {
	return r.surfaceWithDeps(plan, r.fixtureDeps(plan))
}

// surfaceWithDeps 是 surfaceOf 的可注入形态：5b 把文件/检索依赖换成沙箱版，
// 装配路径仍然只有 surfaceFor 一条（否则"夹具的 Surface"与"生产的 Surface"
// 会成为两份实现，而它们的分歧只会在某个夹具静默通过时暴露）。
func (r *PlasmaFixtureRunner) surfaceWithDeps(plan *plasma.LinkPlan, deps *toolspec.Deps) *tools.Surface {
	return surfaceFor(r.profileFor(plan), deps, plan.Agent.DisplayName)
}

func (r *PlasmaFixtureRunner) profileFor(plan *plasma.LinkPlan) toolspec.Profile {
	p := plan.Profile
	if len(r.Demote) > 0 {
		p.Demote = append(append([]string(nil), p.Demote...), r.Demote...)
	}
	return p
}

func (r *PlasmaFixtureRunner) fixtureDeps(plan *plasma.LinkPlan) *toolspec.Deps {
	if r.Deps != nil {
		return r.Deps
	}
	// ProbeDeps 是"能力齐备"的探针（与 golden 同形，坑 39）。但它的 Flow 是空实现：
	// agent_exit 会返回空结果，exit-contract 就永远解析不出退出 JSON —— 那不是
	// "契约被违反"，而是夹具自己装错了。所以这里换成真实执行器。
	d := plan.Profile.ProbeDeps()
	d.Flow = tools.NewFlowControlTool("")
	return d
}

// wantSurfaceNames 是"这个 Agent 该有哪些工具"的唯一表达式。
// agent_exit 显式补上：即使 Link 的解析结果漏了它，夹具也要期望它存在 ——
// "工具面缺收尾"是最致命的一类缺陷，不能因为期望值也跟着少一项而放过。
func wantSurfaceNames(plan *plasma.LinkPlan) []string {
	seen := make(map[string]bool, len(plan.Agent.Tools)+1)
	out := make([]string, 0, len(plan.Agent.Tools)+1)
	for _, name := range plan.Agent.Tools {
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if !seen[toolspec.ExitToolName] {
		out = append(out, toolspec.ExitToolName)
	}
	sort.Strings(out)
	return out
}

// diffNames 返回 got 里多出的、以及 want 里缺失的名字（两者都已排序）。
func diffNames(got, want []string) (extra, missing []string) {
	g := make(map[string]bool, len(got))
	for _, name := range got {
		g[name] = true
	}
	w := make(map[string]bool, len(want))
	for _, name := range want {
		w[name] = true
	}
	for _, name := range got {
		if !w[name] {
			extra = append(extra, name)
		}
	}
	for _, name := range want {
		if !g[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(extra)
	sort.Strings(missing)
	return extra, missing
}

// skippedReasons 把"缺失"变成"缺的原因"：Surface 已经记了落选原因，
// 不把它带出来等于让排障重跑一遍装配。
func skippedReasons(surface *tools.Surface, missing []string) string {
	reasons := make(map[string]string, len(missing))
	for _, item := range surface.Skipped() {
		reasons[item.Name] = item.Reason
	}
	var parts []string
	for _, name := range missing {
		if reason, ok := reasons[name]; ok {
			parts = append(parts, name+": "+reason)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "（装配跳过原因 " + strings.Join(parts, "；") + "）"
}

// stopKindFor 补齐循环的结束原因。
//
// 两个成功出口（纯文本 / agent_exit）自己带 StopKind；**预算耗尽那条出口是错误路径**，
// executor 只返回 error 与零值结果。所以这里用"引擎被调用的次数是否达到预算"佐证：
// 跑满 MaxSteps 次 ⇒ max_steps；脚本先耗尽则走纯文本出口，exec.StopKind 已非空，
// 根本不会走到这里。刻意不做"任意 error 都算 max_steps" —— 那会把 LLM 报错
// 伪装成预算耗尽，夹具于是为一条完全无关的故障亮绿灯。
func stopKindFor(exec ExecutorResult, err error, calls, maxSteps int) string {
	if exec.StopKind != "" {
		return exec.StopKind
	}
	if err == nil {
		return ""
	}
	if maxSteps > 0 && calls >= maxSteps {
		return StopKindMaxSteps
	}
	return ""
}

// evaluateExpect 是 Expect 的**唯一**求值点（descriptor 语义只在这里实现一次）。
//
// 收集全部不符而不是首个即失败：一次就能看到"少调了 agent_exit、交付文本也不对"，
// 与"一轮拿到所有问题"同一个取向。
func evaluateExpect(exec ExecutorResult, stepsDone int, exp plasma.Expect, stopKind string) (bool, string) {
	var fails []string
	calls := toolCallCounts(exec.History)
	for _, name := range exp.MustCall {
		if calls[name] == 0 {
			fails = append(fails, fmt.Sprintf("must_call %q 未被调用", name))
		}
	}
	for _, name := range exp.MustNotCall {
		if calls[name] > 0 {
			fails = append(fails, fmt.Sprintf("must_not_call %q 被调用了 %d 次", name, calls[name]))
		}
	}
	// MaxSteps 是**上界**：循环永远不会超过它，所以"是否生效"表现为"步数是否被放大"。
	// 用 stepsDone 而不是 exec.Steps：预算耗尽那条出口的 result 是零值，
	// 拿 exec.Steps 直接比会让"步数超限"永远为假 —— 又一条永真测试。
	if exp.MaxSteps > 0 && stepsDone > exp.MaxSteps {
		fails = append(fails, fmt.Sprintf("执行了 %d 步，超出预算 %d", stepsDone, exp.MaxSteps))
	}
	if exp.StopKind != "" && stopKind != exp.StopKind {
		fails = append(fails, fmt.Sprintf("stop_kind = %q，期望 %q", stopKind, exp.StopKind))
	}
	for _, shape := range exp.ExitShape {
		switch strings.TrimSpace(shape) {
		case plasma.FixtureStopKindAgentExit:
			if !isAgentExitResult(exec.Text) {
				fails = append(fails, "退出形状不是 agent_exit（缺 finished:true + reason）")
			}
		case "handoff":
			if ok, why := handoffShapeOK(exec); !ok {
				fails = append(fails, "handoff 形状不合规: "+why)
			}
		default:
			fails = append(fails, fmt.Sprintf("未知 exit_shape %q", shape))
		}
	}

	deliverable := deliverableText(exec)
	if exp.DeliverableEquals != "" &&
		strings.TrimSpace(deliverable) != strings.TrimSpace(exp.DeliverableEquals) {
		fails = append(fails, fmt.Sprintf("交付文本 = %q，期望 %q",
			clipForReason(deliverable), clipForReason(exp.DeliverableEquals)))
	}
	for _, needle := range exp.DeliverableContains {
		if !strings.Contains(deliverable, needle) {
			fails = append(fails, fmt.Sprintf("交付文本缺少 %q（实际 %q）",
				needle, clipForReason(deliverable)))
		}
	}
	if len(fails) > 0 {
		return false, strings.Join(fails, "；")
	}
	return true, ""
}

// handoffShapeOK 校验 handoff 的四条语义：是对象、conclusion 非空字符串、
// status ∈ {ok,partial,error}，且确实以 agent_exit 收尾。
//
// 解析的是 exec.Text（agent_exit 的**返回值**）：tools.ExecuteAgentExit 把 handoff
// 原样回显，所以这份 JSON 与"模型写进参数的那一份"同构；用返回值判据更严 ——
// 回显断了会被抓到，而不是被参数侧的副本掩盖。
func handoffShapeOK(exec ExecutorResult) (bool, string) {
	if !isAgentExitResult(exec.Text) {
		return false, "没有 agent_exit 收尾"
	}
	var probe struct {
		Handoff map[string]interface{} `json:"handoff"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(exec.Text)), &probe); err != nil {
		return false, "退出 JSON 解析失败: " + err.Error()
	}
	if probe.Handoff == nil {
		return false, "缺 handoff 对象"
	}
	conclusion, ok := probe.Handoff["conclusion"].(string)
	if !ok || strings.TrimSpace(conclusion) == "" {
		return false, "handoff.conclusion 必须是非空字符串"
	}
	status, _ := probe.Handoff["status"].(string)
	switch status {
	case "ok", "partial", "error":
		return true, ""
	default:
		return false, fmt.Sprintf("handoff.status = %q，须为 ok/partial/error", status)
	}
}

// toolCallCounts 从历史里统计每条工具被调了几次。只数 assistant 的 tool_calls：
// tool 角色的结果里也有 ToolName，混进来会把一次调用算成两次。
func toolCallCounts(history []llm.Message) map[string]int {
	counts := map[string]int{}
	for _, m := range history {
		if m.Role != llm.RoleAssistant {
			continue
		}
		for _, tc := range m.ToolCalls {
			counts[tc.Function.Name]++
		}
	}
	return counts
}

// clipForReason 截断失败现场：交付文本可能很长，诊断里只要够定位。
func clipForReason(s string) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) > 160 {
		return string(r[:160]) + "…（截断）"
	}
	return s
}
