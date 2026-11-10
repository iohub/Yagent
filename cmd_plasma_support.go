package main

import (
	"context"
	"os"
	"strings"

	"yagent/internal/agents"
	"yagent/internal/config"
	"yagent/internal/plasma"
	"yagent/internal/toolspec"
)

// 本文件是六个 plasma 子命令的共享装配：从配置派生 Elaborate 默认值、按 hash 读取
// 升级批准、以及 compile / test 共用的"Elaborate → Compile → Link"前半。
//
// 分成独立文件是为了让每个文件都在 250 行以内（任务 §三 的硬要求）：命令树、共享装配、
// 各命令、渲染各占一处，读的人不必在一个 400 行的文件里找自己要改的那段。

// plasmaDefaults 从配置派生 Elaborate 的默认值。
//
// 派生逻辑的唯一实现在 agents.PlasmaDefaults：在内核里再抄一份的后果是"离线编译
// 通过的 bundle，线上跑出来的步数/超时不一样"（它同时被 app 的 trampoline 装配使用）。
func plasmaDefaults(cfg *config.Config) plasma.Defaults { return agents.PlasmaDefaults(cfg) }

// approvedEffects 把"这个 hash 已获升级批准"翻译成一个能力位掩码。
//
// 为什么不是 Bool：Compiler.EscalationApproved 的语义是"用户批准了哪些额外能力位"，
// 而 store 里按 hash 记的批准没有细分到具体位 —— 按 hash 批准就是批准这份 bundle
// 声明的全部 beyond。用 BeyondBase（既有导出函数）算出那一集，不重新写一份能力数学。
func approvedEffects(b *plasma.Bundle, palette toolspec.Effect) toolspec.Effect {
	// 判据只有一份：plasma.EscalationEffects（阶段 3 的 escalation 账目与加载期批准位
	// 也是它）。这里是**委托**而不是第二份实现 —— 抄一份的后果是"CLI 批准的能力位"
	// 与"加载期批准的能力位"在某次改动后不再相同，而两者的症状都是"工具静默消失"。
	return plasma.EscalationEffects(b, palette)
}

// plasmaCompileResult 是 compile / test 共用的观测结果（渲染在 cmd_plasma_render.go）。
type plasmaCompileResult struct {
	Bundle         string              `json:"bundle"`
	Hash           string              `json:"hash,omitempty"`
	Grade          plasma.Grade        `json:"grade"`
	Escalation     bool                `json:"escalation_required"`
	Approved       bool                `json:"escalation_approved"`
	Diagnostics    []plasma.Diagnostic `json:"diagnostics,omitempty"`
	Outcomes       plasma.Outcomes     `json:"structural,omitempty"`
	Behavioral     plasma.Outcomes     `json:"behavioral,omitempty"`
	BehavioralRuns int                 `json:"behavioral_attempted,omitempty"`
	// BehavioralRequested 记录"--behavioral 给过没有"。它与 BehavioralRuns 是两件事：
	// 前者解释后者为 0 的原因（bundle 里没有 behavior fixtures ≠ 用户没请求）。
	// 渲染把两者混成一句，就会把"我请求了但它没跑"报成"我没请求"。
	BehavioralRequested bool `json:"behavioral_requested,omitempty"`
	// SingleFixture 标记"这次只跑了一条夹具"（plasma test）。它的作用只有一个：
	// 让"没算 grade"与"算出来是空档"在输出里可区分。
	SingleFixture bool `json:"single_fixture,omitempty"`
}

// loadPlasmaBundle 走 Elaborate（认最小 spec 与完整 IR 两种形态）。
// 路径与 JSON 错误都是**用法错**：它们说明调用方式不对，不是"这份 bundle 没通过编译"。
func loadPlasmaBundle(path string, d plasma.Defaults) (*plasma.Bundle, error) {
	if strings.TrimSpace(path) == "" {
		return nil, newUsageError("需要 --bundle <path>")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, newUsageError("读不到 bundle %s: %v", path, err)
	}
	b, err := plasma.Elaborate(raw, d)
	if err != nil {
		return nil, err
	}
	return b, nil
}

// compileWithEscalation 跑编译，并在 store 里有该 hash 的批准时重编一次。
//
// 顺序是必须的：ensureStructuralFixtures 在 Compile 内部补夹具 ⇒ hash 只有编译之后
// 才算得准；而 EscalationApproved 又必须在判定 outside-palette 之前注进去。
// 两次 Compile 都是纯计算（夹具已存在，幂等不覆盖），代价可以忽略。
func compileWithEscalation(c *plasma.Compiler, b *plasma.Bundle, store *plasma.Store) (*plasma.Report, string, bool) {
	r := c.Compile(context.Background(), b)
	hash, err := plasma.HashBundle(b)
	if err != nil {
		return r, "", false
	}
	if !r.EscalationRequired {
		return r, hash, false
	}
	_, meta, loadErr := store.Load(b.Name, hash)
	if loadErr != nil || meta == nil || !meta.EscalationApproved {
		return r, hash, false
	}
	c.EscalationApproved = approvedEffects(b, c.Palette)
	return c.Compile(context.Background(), b), hash, true
}

func plasmaFixtureIDs(b *plasma.Bundle) []string {
	if b == nil || b.Fixtures == nil {
		return nil
	}
	ids := make([]string, 0, len(b.Fixtures.Structural))
	for _, f := range b.Fixtures.Structural {
		ids = append(ids, f.ID)
	}
	return ids
}

// unknownTier 是 ParseTier 的"没认出来"哨兵：llm.ParseTier 接受 fallback，
// 传一个不可能的档位就能区分"用户写了 balanced"与"用户写了 blah"。
//
// 刻意不在这里另写一份 tier 词表：词表在 llm.ParseTier 里，抄一份必漂（坑 25）。
