package http

// task_gate.go — 任务上下文与暂停闸门（A 层）的装配。
//
// 闸门必须与 ctx 同时创建：执行侧只认 ctx（RunAgentLoop 与 Director Planner 在
// step 边界上 pause.From(ctx).Wait），而 /pause、/continue 是外部动作，需要一个
// 不经过 ctx 就能碰到闸门的落点 —— 那是 Task.Gate()，它从 ctx 上读回来，
// 所以只有这一份，不存在"换了 ctx 忘了换闸门"的第二处状态。
//
// 每个建任务 ctx 的地方都必须走这个函数：漏一处就是那条路径上的任务永久无法暂停。
// 那不会静默失败（/pause 会拒绝并说"这个任务的 ctx 没有闸门"），但也修不回来 —— 表现是
// "这一条路径上的任务从来停不了"，而它正是最常被新增的那条路径。

import (
	"context"
	"time"

	"yagent/internal/pause"
)

// NewTaskContext 建一个可取消的任务 ctx，并在上面挂好暂停闸门。
// timeout ≤ 0 表示不设任务级超时（沿用原先只有 WithCancel 的那条分支）。
//
// 闸门只从这里返回 ctx 这一个通道出去，不另存一份字段：Task.Gate() 是从 ctx 读回来的。
// 两处各存一份的话，任何"换了 ctx 忘了换闸门"的地方都会让 /pause 停在一个没人在等的
// 闸门上 —— 而那条路径没有报错，只是什么都不发生。
func NewTaskContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	var ctx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), timeout)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	return pause.WithGate(ctx, pause.NewGate()), cancel
}
