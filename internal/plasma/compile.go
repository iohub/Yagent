package plasma

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"yagent/internal/toolspec"
)

// 旋钮范围。max_parallel_readonly_tools 的上限与 agents.MaxMaxParallelReadOnlyTools
// 及根 AGENTS.md §7 的">8 钳到 8"同源；plasma 不能 import agents（会成环），
// 所以这里钉一份并由 TestKnobLimitsMatchUpstream 在装配侧对账。
const (
	MaxStepsMin      = 1
	MaxStepsMax      = 500
	LLMTimeoutMin    = time.Second
	LLMTimeoutMax    = time.Hour
	ParallelToolsMin = 1
	ParallelToolsMax = 8
)

var snakeCasePattern = regexp.MustCompile(`^[a-z0-9]+(_[a-z0-9]+)*$`)

// Compiler 的 Palette 由调用方注入：P0 用测试画像，P1 起是 PlasmaPalette。
// 刻意不给默认值 —— 一个"忘了配 palette"的编译器应当拒掉一切，而不是放行一切。
type Compiler struct {
	Palette toolspec.Effect
}

// Compile 跑阶段 1-3（Parse / Resolve / Typecheck）。阶段 4 属 P2、5 属 P6、6 属 P4。
// 一次跑完全部阶段而不是遇错即停：生成侧需要一轮拿到所有问题，否则每修一处就要一次往返。
func (c *Compiler) Compile(ctx context.Context, b *Bundle) *Report {
	r := &Report{Grade: GradeProvisional}
	if b == nil {
		r.errorf(CodeBadJSON, "bundle", "bundle 为 nil", "")
		return r
	}

	c.parse(b, r)
	selected, rejected := c.resolve(b, r)
	c.typecheck(b, selected, rejected, r)

	if r.HasErrors() {
		r.Grade = ""
	}
	return r
}

// 阶段 1：结构与必填。
func (c *Compiler) parse(b *Bundle, r *Report) {
	if b.SchemaVersion != SchemaVersion {
		r.errorf(CodeSchemaVersion, "bundle.schema_version",
			fmt.Sprintf("schema_version = %d，只支持 %d", b.SchemaVersion, SchemaVersion),
			"读侧不认得的版本必须拒载，而不是尽力解析")
	}
	if b.Name == "" {
		r.errorf(CodeName, "bundle.name", "name 为空", "")
	} else if !snakeCasePattern.MatchString(b.Name) {
		r.errorf(CodeName, "bundle.name",
			fmt.Sprintf("name %q 不是 snake_case", b.Name),
			"它要拼成 delegate_<name> 工具名，字符集受 provider 约束")
	}
	if b.Prompt.System == "" {
		r.errorf(CodePromptEmpty, "bundle.prompt.system", "system prompt 为空", "")
	}
	if len(b.Tools) == 0 {
		r.errorf(CodeToolsEmpty, "bundle.tools", "tools 为空",
			"至少要能收尾；agent_exit 由 MatchNames 自动追加，但 tools 不能整份为空")
	}
}

// 阶段 2：对着活注册表解析工具名。MatchNames 忽略 deps（profile.go:175 的 `_ = deps`），
// 所以传 nil 是安全的；它同时自动追加 agent_exit（:196），那条规则只此一份。
func (c *Compiler) resolve(b *Bundle, r *Report) (selected []*toolspec.Spec, rejected []string) {
	selected, unknown, rejected := toolspec.MatchNames(b.Tools, c.Palette, nil)
	for _, name := range unknown {
		note := ""
		if s := suggestTool(name); s != "" {
			note = fmt.Sprintf("did you mean %q?", s)
		}
		r.errorf(CodeUnknownTool, fmt.Sprintf("bundle.tools[%d]", indexOfTool(b.Tools, name)),
			fmt.Sprintf("unknown tool %q", name), note)
	}
	return selected, rejected
}

// 阶段 3：权限包含、收尾通道、旋钮范围、deny 自洽。
func (c *Compiler) typecheck(b *Bundle, selected []*toolspec.Spec, rejected []string, r *Report) {
	for _, name := range rejected {
		r.errorf(CodeOutsidePalette, fmt.Sprintf("bundle.tools[%d]", indexOfTool(b.Tools, name)),
			fmt.Sprintf("tool %q 的能力 %v 超出 palette %v", name,
				toolspec.EffectOf(name).Names(), c.Palette.Names()),
			"生成式 Agent 默认只读；要写/shell 必须走升级门并由用户批准")
	}

	if !hasSpec(selected, toolspec.ExitToolName) {
		r.errorf(CodeNoExit, "bundle.tools",
			"工具面里没有 agent_exit",
			"没有收尾通道时循环会烧到 max_steps，交付物也取不回来")
	}

	if b.Runtime.MaxSteps < MaxStepsMin || b.Runtime.MaxSteps > MaxStepsMax {
		r.errorf(CodeMaxSteps, "bundle.runtime.max_steps",
			fmt.Sprintf("%d 超出范围 [%d,%d]", b.Runtime.MaxSteps, MaxStepsMin, MaxStepsMax), "")
	}
	if b.Runtime.LLMTimeout < LLMTimeoutMin || b.Runtime.LLMTimeout > LLMTimeoutMax {
		r.errorf(CodeLLMTimeout, "bundle.runtime.llm_timeout",
			fmt.Sprintf("%v 超出范围 [%v,%v]", b.Runtime.LLMTimeout, LLMTimeoutMin, LLMTimeoutMax), "")
	}
	if b.Runtime.MaxParallelReadOnlyTools < ParallelToolsMin || b.Runtime.MaxParallelReadOnlyTools > ParallelToolsMax {
		r.errorf(CodeParallelTools, "bundle.runtime.max_parallel_readonly_tools",
			fmt.Sprintf("%d 超出范围 [%d,%d]", b.Runtime.MaxParallelReadOnlyTools, ParallelToolsMin, ParallelToolsMax), "")
	}

	deny, unknownEffects := toolspec.EffectFromNames(b.Policy.Deny)
	for _, name := range unknownEffects {
		r.errorf(CodeUnknownEffect, "bundle.policy.deny",
			fmt.Sprintf("认不出的能力名 %q", name),
			"词表见 toolspec.Effect.Names；拼错会让 deny 静默少一项")
	}
	for _, s := range selected {
		if s.Effect.Has(deny) {
			r.errorf(CodeDenyConflict, fmt.Sprintf("bundle.tools[%d]", indexOfTool(b.Tools, s.Name)),
				fmt.Sprintf("tool %q 的能力 %v 与 policy.deny %v 冲突",
					s.Name, s.Effect.Names(), b.Policy.Deny),
				"声明放弃又选进来，两者必有一处是笔误")
		}
	}
}

func hasSpec(specs []*toolspec.Spec, name string) bool {
	for _, s := range specs {
		if s.Name == name {
			return true
		}
	}
	return false
}

// indexOfTool 返回名字在原始 tools 里的下标，让诊断指向生成侧真正写的那一处。
// MatchNames 会去重与追加，所以不能拿它的结果下标当原始下标。
func indexOfTool(tools []string, name string) int {
	for i, t := range tools {
		if t == name {
			return i
		}
	}
	return 0
}
