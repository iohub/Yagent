package memory

// rollout_ctx.go — 父子会话之间的两个关联坐标，走 ctx 下传。
//
// 为什么放 memory：写入端分属 internal/agents（executor）与 internal/agents/director
// （planner），而后者不得 import 前者（scripts/check_no_agents_import.sh）；memory 是
// 两边唯一的共同依赖，rollout 的归属包本来就在这条链上。
//
// 为什么用 ctx 而不是构造参数：delegate_* 的调用链要穿过
// Tools.Call → adapter → delegateTo → 子 Agent.Run → RunAgentLoop，
// 每层加一个 id 参数会把六个子 Agent 的签名全改掉。ctx-value 与
// GetRolloutWriter 同构，是这个仓库已有的做法。

import "context"

type delegationIDKey struct{}
type rolloutSessionIDKey struct{}

// WithDelegationID 记下"这一次委派的 tool_call_id"。
// Director 的工具执行层在 delegate_* 分支上注入，子 Agent 写 session_meta 时取用。
func WithDelegationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, delegationIDKey{}, id)
}

// DelegationIDFrom 取回当前委派的 tool_call_id；没有则返回空串。
// 空串同时出现在两种场合：Director 自己的 rollout，以及本次改动之前写的存量文件 ——
// 恢复侧必须把空串当成"这份文件没有坐标，不能原样重放"，而不是"它就是根会话"。
func DelegationIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(delegationIDKey{}).(string)
	return id
}

// WithRolloutSessionID 把"我这一份 rollout 的 session_id"放到 ctx 上，
// 让下一级（被委派的子 Agent）能把它记成自己的 ParentSessionID。
//
// 只有 delegation_id 时父子是单向的：从子 Agent 的文件能定位到 Director 的某一次
// call，但从 Director 的 call 找不到该读哪份子 Agent 文件（一个 session 里同一角色
// 可能被委派多次）。双向指针让恢复不需要靠文件名和时间戳猜。
func WithRolloutSessionID(ctx context.Context, sessionID string) context.Context {
	return context.WithValue(ctx, rolloutSessionIDKey{}, sessionID)
}

// RolloutSessionIDFrom 取回父会话的 session_id；没有则返回空串。
func RolloutSessionIDFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(rolloutSessionIDKey{}).(string)
	return id
}
