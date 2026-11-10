package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"yagent/internal/compression"
	"yagent/internal/llm"
	"yagent/internal/plasma"
	"yagent/internal/tools"
	"yagent/internal/toolspec"
)

// 本文件是阶段 5b：真 LLM 行为夹具（ADR-3 的下半层）。
//
// 与 5a 的分工：5a 用 scripted 引擎验**接线**（工具解析、exit 契约、步数预算、
// palette 包含），5b 用注入的**真引擎**验**prompt 质量**。两者的判据是同一份
// `evaluateExpect`（不写第二份）—— 差别只在引擎从哪来、以及在 Expect 之外多两条
// 预算（ADR-3 的补偿控制）。
//
// 四条纪律：
//  1. 引擎只由调用方注入（`PlasmaFixtureRunner.LLM`）。runner 自己构造引擎会让
//     "测试用的是 scripted、生产偷偷用了另一个"这类差异无从复现；
//  2. 工作区**必须**先复制到临时目录（ADR-5：store 里的快照不可变）。见
//     plasma.CopyTree；
//  3. 双预算（token + 墙钟）任一耗尽 ⇒ `Passed=false`，Reason 明写"未验证"。
//     超预算**绝不**静默通过 —— 那是 ADR-3 里唯一一条能让人放心的补偿控制；
//  4. `fixture_run` 由本文件强制置真（emitFixtureMetrics），评分侧只统计 false。

// RunBehavioral 执行一条行为夹具。
//
// 返回 error 只用于"程序性"故障（工作区不可用等），并且同时给出可读的 Reason；
// 而"未注入引擎 / 没配预算 / 超预算 / 超时"一律走 `Passed=false` + Reason：
// error 会被上层当成一次可重试的失败，而这四种情况重试一万次也是同一个结果。
func (r *PlasmaFixtureRunner) RunBehavioral(ctx context.Context, plan *plasma.LinkPlan, f plasma.Fixture) (plasma.Outcome, error) {
	out := plasma.Outcome{FixtureID: f.ID, Rubric: append([]string(nil), f.Rubric...)}
	switch {
	case plan == nil:
		out.Reason = "未验证：LinkPlan 为 nil，没有可装配的计划"
		return out, nil
	case r.LLM == nil:
		// 不返回 error：离线才跑 5b，而"没注入引擎"是可判定的配置事实，不是故障。
		out.Reason = "未验证：没有注入 LLM 引擎（5b 只在离线跑：`yagent plasma compile --behavioral`）"
		return out, nil
	case r.BehavioralTokenBudget <= 0:
		// fail-closed：<=0 不是"不限"，而是"没配"。把它当不限就等于让一次忘配
		// 变成一次白送的 verified。
		out.Reason = "未验证：没有配置 token 预算（[plasma] behavioral_token_budget，<=0 视为非法）"
		return out, nil
	}

	if r.BehavioralTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, r.BehavioralTimeout)
		defer cancel()
	}

	sandbox, cleanup, err := r.behavioralSandbox(f)
	if err != nil {
		out.Reason = "未验证：夹具工作区不可用（" + err.Error() + "）"
		return out, err
	}
	defer cleanup()

	cfg := r.behavioralConfig(plan, f, sandbox)
	exec, runErr := RunAgentLoop(ctx, cfg)

	stepsDone := exec.Steps
	kind := stopKindFor(exec, runErr, assistantTurns(exec.History), cfg.MaxSteps)
	if stepsDone == 0 && kind == StopKindMaxSteps {
		stepsDone = cfg.MaxSteps
	}
	deliverable := deliverableText(exec)
	passed, reason := evaluateExpect(exec, stepsDone, f.Expect, kind)

	out.Steps = stepsDone
	out.TokensIn = int(exec.Usage.PromptTokens)
	out.TokensOut = int(exec.Usage.CompletionTokens)
	out.StopKind = kind
	out.DeliverableChars = utf8.RuneCountInString(strings.TrimSpace(deliverable))
	out.Passed = passed
	out.Reason = reason

	// 预算判定在 Expect 之后、且在下面覆盖 Reason：两类原因要一起说出来 ——
	// "契约也对不上 + 还超了预算"比只报一个更接近事实，而只报预算会把
	// "这份 prompt 本身就不合格"这条信息吞掉。
	used := int(exec.Usage.TotalTokens)
	if alt := int(exec.Usage.PromptTokens + exec.Usage.CompletionTokens); alt > used {
		used = alt
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		out.Passed = false
		out.Reason = joinReason(reason, fmt.Sprintf(
			"未验证：墙钟超时（上限 %s，已跑 %d 步）；超时的夹具不算通过", r.timeoutLabel(), stepsDone))
	} else if used > r.BehavioralTokenBudget {
		out.Passed = false
		out.Reason = joinReason(reason, fmt.Sprintf(
			"未验证：token 预算耗尽（%d/%d）；超预算的夹具不算通过", used, r.BehavioralTokenBudget))
	} else if runErr != nil && !strings.Contains(reason, runErr.Error()) {
		out.Reason = joinReason(reason, "循环返回错误: "+runErr.Error())
	}

	r.emitFixtureMetrics(plan, f, out, runErr)
	return out, nil
}

// behavioralConfig 把 LinkPlan 的旋钮搬进 ExecutorConfig（与 5a 的 runScript 同源）。
//
// 唯一不同的是 Surface 的 Deps：文件/检索类工具必须指向**沙箱**，否则模型的
// read_file 会读到 store 里的快照（甚至工作树），夹具就变成了"在真机上跑"。
func (r *PlasmaFixtureRunner) behavioralConfig(plan *plasma.LinkPlan, f plasma.Fixture, sandbox string) ExecutorConfig {
	cfg := DefaultExecutorConfig()
	// SystemPrompt 原样用 bundle 的：5b 验的就是这份 prompt 的质量，再经一次
	// FormatPrompt 就把被验对象换成了模板的输出。
	cfg.SystemPrompt = plan.Agent.SystemPrompt
	cfg.UserInput = fixtureInput(f)
	// 引擎只有这一处来源：调用方注入的 r.LLM。漏了这一行的表现不是"跑错了模型"，
	// 而是循环第一次调 GenerateContent 就 nil panic —— 与 5a 的 scripted 分支
	// 恰好相反（那边自己建引擎，所以从没暴露过这个空档）。
	cfg.LLM = r.LLM
	cfg.Surface = r.surfaceWithDeps(plan, r.sandboxDeps(plan, sandbox))
	cfg.Publisher = r.Publisher
	cfg.AgentName = plan.Agent.DisplayName
	cfg.MaxSteps = plan.Config.MaxSteps
	cfg.StopOnFinish = plan.Config.StopOnFinish
	cfg.LLMTimeout = plan.Config.LLMTimeout
	cfg.MaxParallelReadOnlyTools = plan.Config.MaxParallelReadOnlyTools
	cfg.Compression = compression.Options{
		Enabled:    plan.Config.CompressionEnable,
		KeepTokens: plan.Config.KeepToolResultTokens,
	}
	return cfg
}

// behavioralSandbox 建一个空沙箱，并在夹具声明了 workspace 时把快照复制进去。
// 返回的 cleanup 一定非 nil（defer 可以无条件调）。
func (r *PlasmaFixtureRunner) behavioralSandbox(f plasma.Fixture) (string, func(), error) {
	dir, err := os.MkdirTemp("", "plasma-behavioral-*")
	if err != nil {
		return "", func() {}, fmt.Errorf("plasma: 建行为夹具沙箱: %w", err)
	}
	// MkdirTemp 的权限受 umask 影响；0700 是事实而不是"通常如此"（与 workdir 同一约定）。
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", func() {}, fmt.Errorf("plasma: chmod 沙箱 %s: %w", dir, err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	src, err := plasma.FixtureWorkspacePath(r.WorkspaceRoot, f.Workspace)
	if err != nil || src == "" {
		return dir, cleanup, err
	}
	if _, err := plasma.CopyTree(src, dir, plasma.DefaultCopyLimits()); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return dir, cleanup, nil
}

// sandboxDeps 在 fixtureDeps 之上把文件/检索/项目路径指向沙箱。
//
// 只覆盖这三样：编辑/Shell/浏览器都不在 PlasmaPalette 里（ADR-4），给它们装沙箱
// 是给一条走不通的通道做准备，而"装了但用不到"会让下一个人以为它们可用。
func (r *PlasmaFixtureRunner) sandboxDeps(plan *plasma.LinkPlan, sandbox string) *toolspec.Deps {
	d := r.fixtureDeps(plan)
	if strings.TrimSpace(sandbox) == "" {
		return d
	}
	clone := *d
	clone.ProjectPath = sandbox
	clone.Files = tools.NewFileOperationsTool(sandbox)
	clone.Search = tools.NewSearchOperationsTool(sandbox)
	return &clone
}

func (r *PlasmaFixtureRunner) timeoutLabel() string {
	if r.BehavioralTimeout <= 0 {
		return "调用方 ctx"
	}
	return r.BehavioralTimeout.String()
}

// fixtureInput 是 5b 的题面：作者写了 input 就用它，没写才回落结构夹具的固定串。
// 5a 的脚本夹具刻意**不**读 input（它验的是接线，题面必须是常量），5b 相反 ——
// 题面就是被验对象的一部分。
func fixtureInput(f plasma.Fixture) string {
	if s := strings.TrimSpace(f.Input); s != "" {
		return s
	}
	return plasma.FixtureInput
}

// assistantTurns 数历史里的 assistant 轮次，作为"引擎被调用了几次"的代理。
// 5b 用的是真引擎，没有 scripted 的调用记录可读，而 stopKindFor 需要这个数
// 才能把"预算耗尽"与"引擎报错"区分开。
func assistantTurns(history []llm.Message) int {
	n := 0
	for _, m := range history {
		if m.Role == llm.RoleAssistant {
			n++
		}
	}
	return n
}

// joinReason 拼两段原因：任一段为空时不留分隔符（否则 Reason 会以"；"开头，
// 读的人会以为前面还有内容被截掉了）。
func joinReason(a, b string) string {
	switch {
	case strings.TrimSpace(a) == "":
		return b
	case strings.TrimSpace(b) == "":
		return a
	default:
		return a + "；" + b
	}
}
