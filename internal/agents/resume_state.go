package agents

// resume_state.go — 重放入口：把"暂停前的那一段会话"交回 RunAgentLoop（§6.2）。
//
// 为什么走 ctx 而不是给 ExecutorConfig 加字段、或改六个子 Agent 的 Run 签名：
// 与 memory.WithRolloutWriter 同一个形状，接线只发生在构造 ctx 的那一处，
// coding/repo/chat/devops/browser/meta 与 Custom Agent 全都只是把 ctx 往下传 ——
// 它们一行都不用改，也就不存在"漏改一个 Agent，那条路径的恢复静默失效"。
//
// 为什么必须由代码决定怎么恢复（D7）：Director 的模型只该看到"那次委派成功返回了"。
// 把恢复交给模型判断，结果是同样的停点有时重做、有时接着跑 —— 不可复现的行为
// 比不能恢复更难排查。

import (
	"context"

	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/thinklink"
)

type resumeStateKey struct{}

// ResumeState 是一次重放所需的执行状态。
type ResumeState struct {
	// Messages 是暂停前喂给模型的完整序列（含 system 与首条 user）。
	// 它同时也是指纹校验的对照物：不等就该降级，而不是硬跑。
	Messages []llm.Message
	// StartStep 是已完成步数，也是重放的起点（for i := StartStep; …）。
	StartStep int
	// Materialized 是暂停前已经取回过的冷工具，恢复后不该让模型再 load 一次。
	Materialized []string
	// ThinkLink 是 ultimate 压缩的重建原料。少了它，恢复后的会话走到第三档压缩
	// 就只能拿原始任务输入去重建 —— 中间所有已完成的步骤从模型视野里消失。
	// 用 memory 的本地形状而不是 thinklink.Entry：读侧（memory）与执行侧（这里）
	// 之间只转换一次，kind 的识别失败也集中在那一处。
	ThinkLink []memory.ReplayThinkLinkEntry
	// DelegationID 是父 Director 那一次 delegate_* 的 tool_call_id。
	DelegationID string
	// Digest 是检查点里记的指纹，由调用方与 Messages 的实际内容对账。
	Digest string
}

// WithResumeState 把重放状态挂到 ctx 上。
func WithResumeState(ctx context.Context, s *ResumeState) context.Context {
	return context.WithValue(ctx, resumeStateKey{}, s)
}

// resumeStateFrom 取回重放状态；没有则返回 nil（正常起跑）。
func resumeStateFrom(ctx context.Context) *ResumeState {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(resumeStateKey{}).(*ResumeState)
	return s
}

// SeedThinkLink 把重放回来的 T&P 条目灌进一个 store。
// 单独成函数是因为六个子 Agent 里只有三个建了 store，判空逻辑不该在每处重复。
func SeedThinkLink(store ThinklinkSeeder, entries []memory.ReplayThinkLinkEntry) int {
	if store == nil || len(entries) == 0 {
		return 0
	}
	return store.Seed(thinklinkFromReplay(entries))
}

// thinklinkFromReplay 把 memory 的本地形状转成 thinklink 条目。
// 认不出来的 kind 整条丢弃并计数：把一条 thought_plan 错认成 user_input，
// RebuildPrompt 就会把"上一步的思考"当成"用户的原始需求"重建进上下文。
func thinklinkFromReplay(entries []memory.ReplayThinkLinkEntry) []thinklink.SeedEntry {
	out := make([]thinklink.SeedEntry, 0, len(entries))
	for _, e := range entries {
		kind, ok := thinklink.ParseKind(e.Kind)
		if !ok {
			continue
		}
		out = append(out, thinklink.SeedEntry{Kind: kind, Content: e.Content, Step: e.Step})
	}
	return out
}

// ThinklinkSeeder 是 executor 需要的最小 store 能力。
// 用接口而不是直接吃 *thinklink.Store：ExecutorConfig 已经通过
// compression.ThinkLinkStore 与它打交道，这里再要一个 Seed 是同一套解耦做法。
type ThinklinkSeeder interface {
	Seed(entries []thinklink.SeedEntry) int
}
