package agents

import "yagent/internal/toolspec"

// External 是"外部能力"两件套的注入点。刻意做成一个值而不是两个入参：
// depsFor 已经有 5 个入参，而这两件事的装配来源是同一处（app.Init）——
// 加一个参数就是编译错误，少绑一处不会静默失效。
type External struct {
	// Specs 返回当前的外部工具规格快照。必须是**只读快照的取值函数**：
	// SpawnForDelegation 是浅拷贝，两次并行委派共享同一个 External，
	// 在里面 lazily 建可变缓存就会互相踩（CodingAgent.thinkLink 当年那个病）。
	Specs func() []*toolspec.Spec
	// Skills 是技能读通道。nil = 没有技能（use_skill 仍会在场并解释为什么没有）。
	Skills toolspec.SkillSource
	// Status 返回 MCP 状态账（一 server 一行）。只有 Director 会把它注入 system prompt：
	// 子 Agent 不需要知道 server 拓扑，它们的工具面里没有就是没有。
	Status func() string
}

// specsOrNil 保持 nil 与空切片可区分：Surface.rebuild 对两者的处理一致，
// 但填空 slice 会多出一种"看起来有、其实没有"的状态。
func (e External) specsOrNil() []*toolspec.Spec {
	if e.Specs == nil {
		return nil
	}
	return e.Specs()
}

// ExternalProvider 是装配层对"外部能力"的只读视图。由 *app.Yagent 天然实现。
type ExternalProvider interface {
	ExternalSpecs() []*toolspec.Spec
	SkillSource() toolspec.SkillSource
	MCPStatusLines() string
}

// ExternalFrom 把装配层的能力提供者转成子 Agent 用的注入值。
// 走函数而不是值：工具面每轮重建，specs 的缓存可能在重连后失效。
func ExternalFrom(p ExternalProvider) External {
	if p == nil {
		return External{}
	}
	return External{Specs: p.ExternalSpecs, Skills: p.SkillSource(), Status: p.MCPStatusLines}
}
