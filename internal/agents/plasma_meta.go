package agents

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"yagent/internal/blackboard"
	"yagent/internal/config"
	"yagent/internal/memory"
	"yagent/internal/plasma"
	"yagent/internal/util"
)

// 本文件是 `delegate_meta` 的**驱动器**（设计稿 §6.2 / ADR-14 / ADR-15）。
//
// 分工：生成策略在 Python（plasma.pyz generate，P10），LLM 采样与流式在 Go
// （PlasmaTrampoline，P8），判定在 plasma（Compile/Link/VerifyStructural/Ratchet），
// 装配与执行在这里。Go 侧**没有任何生成器** —— MetaAgent 已删除，这条路径是唯一的。
//
// 一次调用的事务顺序（改顺序会让"注册了但跑不起来"这种半成品状态出现）：
//
//	Generate → Link(复核) → VerifyStructural(5a) → Ratchet
//	→ Store.Put → registerCustomAgent → setExecutorConfig → RunAgentLoop
//
// 为什么 Link 要再跑一遍：trampoline 里的 Compile 对着的是**生成时**的注册表，
// 而 Link 对着活注册表复核（工具改名/删除都会在这一步被抓住）。两次都是纯计算。

// plasmaGenerator 是生成器的窄接口：Director 只知道"给我一份编译通过的 bundle"。
// 它不认识 spawner/workdir/--fix，所以换实现（假生成器、远程 worker）不需要改这里。
type plasmaGenerator interface {
	Generate(ctx context.Context, task, contextNote string) (*TrampolineResult, error)
}

// PlasmaGenerator 是同一接口的导出别名：装配层（internal/app）要能写这个字段的类型，
// 实现只能是 *PlasmaTrampoline 或测试里的假件。
type PlasmaGenerator = plasmaGenerator

// plasmaVerdict 是一次"生成 + 判定 + 落盘"的结论。四个字段就是结果文案的全部输入：
// 少一个都要再查一遍 store（那会把一次纯读变成 IO）。
type plasmaVerdict struct {
	plan     *plasma.LinkPlan
	hash     string
	outcomes plasma.Outcomes
	grade    plasma.Grade
}

// executorConfigFor 取某个 Custom Agent 的运行时配置。
// found=false 表示这是老路径注册的 Agent（没有 LinkPlan）：调用方必须回落硬编码并
// **说出来**，而不是静默用零值（MaxSteps=0 会让循环立刻停）。
func (a *DirectorAgent) executorConfigFor(name string) (view plasma.ExecutorConfigView, found bool) {
	a.execMu.RLock()
	defer a.execMu.RUnlock()
	view, found = a.execConfigs[name]
	return view, found
}

// setExecutorConfig 登记一份 LinkPlan 带来的运行时配置（同名覆盖）。
func (a *DirectorAgent) setExecutorConfig(name string, view plasma.ExecutorConfigView) {
	if name == "" {
		return
	}
	a.execMu.Lock()
	defer a.execMu.Unlock()
	if a.execConfigs == nil {
		a.execConfigs = make(map[string]plasma.ExecutorConfigView)
	}
	a.execConfigs[name] = view
}

// plasmaReason 是"为什么没有生成器"的单一文案来源。
func (a *DirectorAgent) plasmaReason() string {
	if r := strings.TrimSpace(a.plasmaWhy); r != "" {
		return r
	}
	if ok, reason := plasmaAvailable(); ok {
		return "plasma 生成器可用但未装配（装配层漏接 PlasmaGenerator）"
	} else {
		return reason
	}
}

func (a *DirectorAgent) plasmaStoreFor() *plasma.Store {
	if a.plasmaStore != nil {
		return a.plasmaStore
	}
	return plasma.NewStore(config.PlasmaStoreDir())
}

func (a *DirectorAgent) plasmaFixtureRunner() plasma.FixtureRunner {
	if a.plasmaRunner != nil {
		return a.plasmaRunner
	}
	// Deps 留 nil：与 CLI（cmd_plasma_compile.go）同形 —— 夹具验的是 bundle 的契约，
	// 不是这台机器上某个 MCP server 有没有起来（ProbeDeps + 真 Flow，见 plasma_fixture.go）。
	return &PlasmaFixtureRunner{}
}

// runMetaDesign 用生成器产出一份 bundle，校验、落盘、注册并立即执行一次。
//
// 与历史实现的差别不只是"换了实现"：错误文案现在指向**可执行的下一步**
// （诊断的前几行 + 修复方向），而不是当年的 "your output was NOT valid JSON"。
func (a *DirectorAgent) runMetaDesign(ctx context.Context, task string) (interface{}, error) {
	ctx = blackboard.WithBoardID(ctx, a.boardID)
	if rolloutWriter := a.createRolloutWriter(ctx, "meta", task); rolloutWriter != nil {
		defer rolloutWriter.Close()
		ctx = memory.WithRolloutWriter(ctx, rolloutWriter)
	}
	ctx = a.routeCtx(ctx, "meta", task)

	if a.plasmaGen == nil {
		// 正常情况下这个工具根本不在 Surface 上（Available 已拦）。走到这里说明装配层
		// 绕过了 Available —— 报出来，而不是让模型看见一个永远失败的工具。
		return nil, fmt.Errorf("plasma 生成器不可用：%s", a.plasmaReason())
	}

	res, err := a.plasmaGen.Generate(ctx, task, a.plasmaContextNote(task))
	if err != nil {
		return nil, fmt.Errorf("plasma 生成失败（任务未执行、工具面未改动）：%w", err)
	}
	if res == nil || res.Bundle == nil {
		return nil, fmt.Errorf("plasma 生成器返回空 bundle（实现缺陷，不是生成失败）；修复：检查 PlasmaTrampoline.Generate 的返回值")
	}

	verdict, err := a.verifyPlasmaBundle(ctx, res)
	if err != nil {
		return nil, err
	}

	ca := customAgentFromPlan(verdict.plan)
	if !a.registerCustomAgent(ca) {
		// 同名已注册：不覆盖（bundle 不可变，工具执行器也不能被悄悄换掉）。
		return fmt.Sprintf("[plasma] Agent %s 已注册，复用现有的 delegate_%s（本次未重复注册，grade=%s）。",
			ca.DisplayName, ca.Name, verdict.grade), nil
	}
	// 注册之后立刻登记执行参数：runCustomAgent 下一步就要读它。
	a.setExecutorConfig(ca.Name, verdict.plan.Config)
	// 坐标也要登记（hash/grade/工具面）：遥测账本的 `bundle_hash`/`grade` 与升级门
	// 都从这一份读。漏了它的症状是"账本里这条委派没有 hash、grade 恒空"。
	a.rememberPlasmaAgent(verdict.plan, plasma.Meta{Grade: verdict.grade})

	agentTask := strings.TrimSpace(res.Bundle.TaskForAgent)
	if agentTask == "" {
		agentTask = task
	}
	resultStr, callErr := a.runCustomAgent(ctx, ca, agentTask)
	if callErr != nil {
		return nil, fmt.Errorf("新 Agent %s（delegate_%s）执行失败：%w", ca.DisplayName, ca.Name, callErr)
	}

	return fmt.Sprintf(
		"[plasma：Agent 已生成并执行]\nAgent: %s（工具名 delegate_%s）\nGrade: %s（provisional = 只过了 5a 结构夹具；升级要 5b 行为夹具 + 用户批准）\nHash: %s\nTools: %s\nStructural fixtures: %d 条全部通过\n\n[执行结果]\n%s\n\n[已注册] delegate_%s 在本任务内可用；它是 plasma 产物，不是 Go 内建 Agent。",
		ca.DisplayName, ca.Name,
		verdict.grade, verdict.hash,
		strings.Join(ca.ToolsUsed, ", "), len(verdict.outcomes),
		resultStr,
		ca.Name,
	), nil
}

// verifyPlasmaBundle 是"能不能装配"的唯一判定点：Link → 5a → Ratchet → 落盘。
//
// 在线止于 provisional：behavioral 传空 Outcomes（在线无题可跑，5b 要真 LLM 且属离线），
// escalation 一律未批准（批准回路是 P9b）。Ratchet 因此只会给 provisional。
func (a *DirectorAgent) verifyPlasmaBundle(ctx context.Context, res *TrampolineResult) (plasmaVerdict, error) {
	var zero plasmaVerdict

	compiler := &plasma.Compiler{Palette: plasma.PlasmaPalette}
	// Compile 是幂等的纯计算，这里先跑一次只为归一化字节：阶段 1 会补齐自动生成的结构
	// 夹具（ensureStructuralFixtures），而 hash 必须对**落盘的那份字节**计算 —— 在补齐
	// 之前算，报出去的 hash 与 store 里的不是同一份（差一整组夹具）。Link 下面会重跑一次，
	// 那次才是对着活注册表的复核。
	compiler.Compile(ctx, res.Bundle)
	hash, err := plasma.HashBundle(res.Bundle)
	if err != nil {
		return zero, fmt.Errorf("plasma: 计算 bundle hash 失败：%w", err)
	}
	plan, err := compiler.Link(res.Bundle, hash)
	if err != nil {
		return zero, fmt.Errorf("生成的 bundle 过不了装配复核（Link，对着当前工具注册表）：\n%w\n%s", err, plasmaFixHint)
	}

	outcomes, vrep := compiler.VerifyStructural(ctx, res.Bundle, plan, a.plasmaFixtureRunner())
	merged := &plasma.Report{
		Diagnostics:        appendDiagnostics(res.Report, vrep),
		EscalationRequired: res.Report != nil && res.Report.EscalationRequired,
	}

	// EscalationRequired 时 Ratchet 主动钉 provisional；这里传 false = "在线没有批准回路"。
	grade := plasma.Ratchet(merged, outcomes, nil, false)
	if grade == "" {
		return zero, fmt.Errorf("生成的 Agent 未通过结构夹具（5a），未注册、未执行：\n%s%s",
			plasmaReportHead(merged, 5), plasmaFixHint)
	}
	merged.Grade = grade

	if _, err := a.plasmaStoreFor().Put(res.Bundle, merged); err != nil {
		// 落盘失败就不注册：否则"这次能用、重启后消失"的 Agent 会让人以为它存在。
		return zero, fmt.Errorf("plasma 产物落盘失败（Agent 未注册）：%w", err)
	}
	slog.Info("plasma: 在线生成完成", "agent", plan.Agent.Name, "hash", hash, "grade", grade,
		"tools", len(plan.Profile.Names), "fixtures", len(outcomes))
	return plasmaVerdict{plan: plan, hash: hash, outcomes: outcomes, grade: grade}, nil
}

// appendDiagnostics 把生成期诊断与 5a 诊断合成一份报告。顺序即因果顺序（生成 → 验证），
// 生成侧靠它决定先修哪一处。
func appendDiagnostics(gen *plasma.Report, verify *plasma.Report) []plasma.Diagnostic {
	var out []plasma.Diagnostic
	if gen != nil {
		out = append(out, gen.Diagnostics...)
	}
	if verify != nil {
		out = append(out, verify.Diagnostics...)
	}
	return out
}

// customAgentFromPlan 把 LinkPlan 的视图投影成运行期形状。
// Name 走 SnakeCase：delegate 工具名必须是 `delegate_<snake>`（模型从 prompt 里抄得到）。
func customAgentFromPlan(plan *plasma.LinkPlan) *CustomAgent {
	return &CustomAgent{
		Name:         util.SnakeCase(plan.Agent.Name),
		DisplayName:  plan.Agent.DisplayName,
		SystemPrompt: plan.Agent.SystemPrompt,
		ToolsUsed:    append([]string(nil), plan.Agent.Tools...),
		Description:  plan.Agent.Description,
	}
}

// plasmaContextNote 是给生成器的上下文便条：蓝图散文 + 便签索引。
// 不含 task 本身 —— 它在 req.task 里，重复一遍会让 Python 侧看到两份。
func (a *DirectorAgent) plasmaContextNote(task string) string {
	var b strings.Builder
	b.WriteString(a.blueprint.ProseForTask(task))
	if blackboardEnabled() && a.boardID != "" {
		if idx, _, err := blackboard.BuildIndex(a.boardID, a.indexOpts()); err == nil && idx != "" {
			b.WriteString(idx)
			b.WriteString("\n\n")
		}
	}
	return b.String()
}
