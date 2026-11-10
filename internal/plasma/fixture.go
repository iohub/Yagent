package plasma

import (
	"context"
	"fmt"
	"strings"

	"yagent/internal/toolspec"
)

// 本文件是阶段 5a：结构夹具的自动生成、执行结果的形状、以及 grade 棘轮（ADR-9）。
//
// 分工是叶子约束的必然结果：plasma 只声明 FixtureRunner 窄接口，真正跑脚本与装配
// 工具面的实现在 internal/agents（`plasma_fixture.go`）。裁决者认识被裁决者的执行
// 细节，就没有独立判据可言 —— 所以 Outcome 里**不许出现 llm.***，`Script` 用 IR 自己的
// ScriptStep 表达。

// 四个结构夹具的 ID 是稳定契约：测试、诊断与 `yagent plasma test --fixture <id>`
// 都按 ID 定位，改名会让"哪个夹具红了"这条信息对不上。
const (
	FixtureExitContract       = "exit-contract"
	FixtureStepBudget         = "step-budget"
	FixtureToolContainment    = "tool-containment"
	FixturePaletteContainment = "palette-containment"
)

// 5a 的 stop_kind 词表与 memory.StopKind*（rollout_types.go）逐字同值。plasma 是叶子包，
// 白名单不含 memory，所以这里只能写字面量；两侧由 agents 侧
// TestPlasmaFixtureRunner_StopKindVocabulary 对账 —— 一处改名那里就红。
const (
	FixtureStopKindAgentExit = "agent_exit"
	FixtureStopKindMaxSteps  = "max_steps"
)

// exit-contract 的三段固定文本。交付文本必须是**内容级**常量：`deliverableText` 在
// 回落时返回的是 `{"finished":true,…}` 这段 JSON，只断言"非空"永远为真（设计稿 §4.3）。
const (
	FixtureInput             = "structural fixture"
	FixtureDeliverableBody   = "structural fixture: deliverable body"
	FixtureExitReason        = "done"
	FixtureHandoffConclusion = "structural fixture completed"
	// fixtureThinkingTool 是"只消耗一步、不产生副作用"的工具。降级/超时/装配
	// 问题都不会让它变成零步：循环按 tool_call 计步，结果成败不影响计数。
	fixtureThinkingTool = "thinking"
)

// 自动生成夹具的出题谱系。必须与 agent 自己的 lineage.mutation 不同，否则
// store.checkFixtureProvenance 会按"自己出题自己考"拒收（ADR-5 防 Goodhart）。
const (
	FixtureAuthorAuto      = "plasma-compiler-auto"
	FixtureLineageCompiler = "compiler"
)

// Outcome 是一条夹具的观测结果。Reason 在 Passed=false 时**必须**非空且可读：
// 生成侧要靠它决定下一轮改什么，一句"failed"等于没跑。
type Outcome struct {
	FixtureID string
	Passed    bool
	Reason    string
	Steps     int
	TokensIn  int
	TokensOut int
	StopKind  string
	// Rubric 是出题者给的评审要点，**不是** Go 侧的评分依据：Go 只做能机械判定的
	// Expect，主观部分交给 judge（P11 的 Python 侧）。放在这里是因为它属于
	// "这一条夹具的观测"，评分侧要拿到它才知道该说什么。
	Rubric []string
	// DeliverableChars 是本次交付文本的字符数。评分侧靠它判 deliverable_nonempty ——
	// 只传 Passed 的话，judge 只能拿夹具结论当交付质量，而"夹具过了但交付是空"是
	// 一条真实存在的路径（Expect 里没有 DeliverableContains 时就会这样）。
	DeliverableChars int
}

// Outcomes 是有序的观测集合（顺序 = 夹具声明顺序，诊断按同一顺序发射）。
type Outcomes []Outcome

// AllPassed 对空集返回 true（全称量化的自然语义）。注意 Ratchet 会**另外**判
// len==0：空集表示"5a 没跑"，不等于"5a 通过"。
func (o Outcomes) AllPassed() bool {
	for _, item := range o {
		if !item.Passed {
			return false
		}
	}
	return true
}

// Failed 返回失败的观测（保持原顺序）。
func (o Outcomes) Failed() []Outcome {
	out := make([]Outcome, 0, len(o))
	for _, item := range o {
		if !item.Passed {
			out = append(out, item)
		}
	}
	return out
}

// Get 按夹具 ID 取观测。
func (o Outcomes) Get(id string) (Outcome, bool) {
	for _, item := range o {
		if item.FixtureID == id {
			return item, true
		}
	}
	return Outcome{}, false
}

// FixtureRunner 是 5a 的执行接缝。plasma 只声明它，agents 侧实现它
// （那边才 import tools/executor/scripted）。RunBehavioral 在 P6 里是占位：
// 5b 属 P11，本阶段不许在这里发起真 LLM 调用。
type FixtureRunner interface {
	RunStructural(ctx context.Context, plan *LinkPlan, f Fixture) (Outcome, error)
	RunBehavioral(ctx context.Context, plan *LinkPlan, f Fixture) (Outcome, error)
}

// StructuralFixtures 生成 §4.3 的四条结构夹具。
//
// 幂等且不读 bundle 之外的任何状态：同样的 bundle（MaxSteps 决定 step-budget 的
// 脚本长度）永远得到逐字段相同的四条。作者提供的夹具由 ensureStructuralFixtures
// 保护，不在这里判断（ADR-5：夹具一经落盘即不可变）。
func StructuralFixtures(b *Bundle) []Fixture {
	if b == nil {
		return nil
	}
	maxSteps := b.Runtime.MaxSteps
	if maxSteps < MaxStepsMin {
		// 越界由阶段 3 报 PLS-203；这里只保证生成出来的脚本形状仍然可读。
		maxSteps = MaxStepsMin
	}
	budgetScript := make([]ScriptStep, 0, maxSteps+1)
	for i := 0; i <= maxSteps; i++ {
		budgetScript = append(budgetScript, ScriptStep{
			Tool: fixtureThinkingTool,
			Args: map[string]interface{}{},
		})
	}

	return []Fixture{
		{
			ID:    FixtureExitContract,
			Input: FixtureInput,
			// **一条响应**里同时带正文与 agent_exit：执行循环在"没有 tool_calls"时
			// 直接按纯文本收尾，所以"先文本、后退出"的两步脚本走不到第二步。
			// 这条形状仍然是 §4.3 要验的那件事：交付文本取自那条 assistant 消息，
			// 而不是 agent_exit 返回的 {"finished":true,…}。
			Script: []ScriptStep{
				{Text: FixtureDeliverableBody, Tool: toolspec.ExitToolName, Args: map[string]interface{}{
					"reason": FixtureExitReason,
					"handoff": map[string]interface{}{
						"conclusion": FixtureHandoffConclusion,
						"status":     "ok",
					},
				}},
			},
			Expect: Expect{
				MustCall:          []string{toolspec.ExitToolName},
				StopKind:          FixtureStopKindAgentExit,
				ExitShape:         []string{FixtureStopKindAgentExit, "handoff"},
				DeliverableEquals: FixtureDeliverableBody,
			},
		},
		{
			ID:    FixtureStepBudget,
			Input: FixtureInput,
			// 恰好 max_steps+1 步：预算若没被应用（MaxSteps 用了零值或写死的默认值），
			// 脚本会先耗尽，stop_kind 就不是 max_steps —— 这条夹具因此能抓到"LinkPlan
			// 的 Config 没被真的读走"。
			Script: budgetScript,
			Expect: Expect{
				MaxSteps: maxSteps,
				StopKind: FixtureStopKindMaxSteps,
			},
		},
		{
			// 静态断言：判据写在 runner 里（要装配真实 Surface，plasma 不认识 tools）。
			ID:    FixtureToolContainment,
			Input: FixtureInput,
		},
		{
			// 静态断言：逐个工具查 Effect 是否落在 effective palette 内。
			ID:    FixturePaletteContainment,
			Input: FixtureInput,
		},
	}
}

// ensureStructuralFixtures 只在**为空**时填充结构夹具，作者提供的一条都不覆盖。
//
// 同时补齐 provenance：有夹具而没有出题谱系，store.Put 会直接拒收
// （checkFixtureProvenance），而"编译能过、存不进去"是最难查的一类失败。
func ensureStructuralFixtures(b *Bundle) {
	if b == nil {
		return
	}
	if b.Fixtures == nil {
		b.Fixtures = &Fixtures{}
	}
	if len(b.Fixtures.Structural) > 0 {
		return
	}
	b.Fixtures.Structural = StructuralFixtures(b)
	if strings.TrimSpace(b.Fixtures.Provenance.Author) == "" {
		b.Fixtures.Provenance.Author = FixtureAuthorAuto
	}
	if strings.TrimSpace(b.Fixtures.Provenance.Lineage) == "" {
		b.Fixtures.Provenance.Lineage = FixtureLineageCompiler
	}
}

// VerifyStructural 逐条执行结构夹具并生成诊断。
//
// 一条失败不影响后续：生成侧要一轮看到**所有**问题，否则每修一条就要一次
// trampoline 往返（硬约束 6 的同一条理由）。诊断码按夹具 ID 分派，Path 指向夹具下标。
func (c *Compiler) VerifyStructural(ctx context.Context, b *Bundle, plan *LinkPlan, runner FixtureRunner) (Outcomes, *Report) {
	rep := &Report{}
	if b == nil || b.Fixtures == nil {
		return nil, rep
	}
	out := make(Outcomes, 0, len(b.Fixtures.Structural))
	for i, f := range b.Fixtures.Structural {
		path := fmt.Sprintf("fixtures.structural[%d]", i)
		var got Outcome
		switch {
		case runner == nil:
			got = Outcome{Reason: "未注入 FixtureRunner：结构夹具没有执行器，5a 等于没跑"}
		case plan == nil:
			got = Outcome{Reason: "LinkPlan 为 nil：结构夹具必须对着可装配的计划跑"}
		default:
			observed, err := runner.RunStructural(ctx, plan, f)
			got = observed
			if err != nil {
				got.Passed = false
				if strings.TrimSpace(got.Reason) == "" {
					got.Reason = err.Error()
				}
			}
		}
		if got.FixtureID == "" {
			got.FixtureID = f.ID
		}
		if !got.Passed && strings.TrimSpace(got.Reason) == "" {
			got.Reason = "夹具失败但执行器没有给出原因"
		}
		out = append(out, got)
		if !got.Passed {
			rep.add(fixtureCode(f.ID), SeverityError, path,
				fmt.Sprintf("结构夹具 %q 失败", f.ID), got.Reason)
		}
	}
	return out, rep
}

// fixtureCode 把夹具 ID 映射到诊断码。新增夹具时要在这里显式落一条，未登记的
// 一类回落到 PLS-501 —— 诊断码是稳定契约，绝不因为"懒得分类"而共用一条。
func fixtureCode(id string) string {
	switch id {
	case FixtureExitContract:
		return CodeFixtureExit
	case FixtureStepBudget:
		return CodeFixtureStepBudget
	case FixtureToolContainment:
		return CodeFixtureToolScope
	case FixturePaletteContainment:
		return CodeFixturePalette
	default:
		return CodeFixtureFailed
	}
}

// Ratchet 是 ADR-9 的三档棘轮：只回答"这份 bundle 本轮最高能拿到哪一档"。
//
//   - 编译有错、或 5a 不是全绿、或 5a 根本没跑 → ""（连 provisional 都不到）。
//     与硬约束 7「编译失败必须清空 Grade」同源：留着 provisional 会让调用方
//     以为可以装配。
//   - 仅 5a 通过 → provisional（可人工点名委派，不可自动委派）。
//   - 5a + 5b 全通过 + 升级已批准 → verified。
//   - promoted 不在这里判：它要生产样本计数，属 store 侧（CanPromote 只回答"够不够"）。
func Ratchet(r *Report, structural Outcomes, behavioral Outcomes, escalationApproved bool) Grade {
	if r == nil || r.HasErrors() {
		return ""
	}
	// 空集 = 没跑，不是"全通过"：忘了生成夹具必须掉档，而不是白拿 provisional。
	if len(structural) == 0 || !structural.AllPassed() {
		return ""
	}
	if r.EscalationRequired && !escalationApproved {
		return GradeProvisional
	}
	if len(behavioral) == 0 || !behavioral.AllPassed() {
		return GradeProvisional
	}
	return GradeVerified
}

// CanPromote 回答"verified 的这份够不够 promote"。三个条件都是硬条件：
// runs=0（还没跑过生产）与 minRuns=0（没配阈值）都不许升 —— 后者会让
// "配置漏填"变成"自动升级"。
func CanPromote(grade Grade, runs, minRuns int) bool {
	return grade == GradeVerified && runs > 0 && minRuns > 0 && runs >= minRuns
}
