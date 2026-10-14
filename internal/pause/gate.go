// Package pause 提供进程内的暂停闸门 —— docs/pause-resume-design.md 的 A 层。
//
// 为什么是"闸门"而不是"cancel + 重建 ctx"：ctx 一旦取消就不可复活，而子 Agent 的
// ctx 已经被 toolbatch.Run、runToolCall 的 cancelCtx 和正在飞的 ad.Call 捕获，
// 重接点分散在四层。闸门是一个纯粹的新增阻塞点，不动任何既有 ctx 语义：暂停时
// ctx 保持存活，循环停在 step 边界，放开后从阻塞点原样往下走 —— 状态零损失。
//
// 闸门挂在 ctx 上（照 memory.WithRolloutWriter 与 activity 的 ctx-value 约定），
// 所以嵌套委派自动继承：Director 暂停时，正在跑的 Coding 以及 Coding 委派的 Browser
// 都会在各自的 step 边界停住，不需要额外接线。
//
// 暂停只能落在 step 边界（设计决策 D2）：run_bash 与流式 LLM 调用只认 ctx，
// 中途停必然留下"副作用做到一半没有"这种不可判定的状态。所以从按下暂停到真的
// 停住，最长要等一个工具的 defaultToolTimeout —— Paused() 与 Blocked() 的差别
// 就是为此存在：UI 用它区分"正在停"和"已经停住"。
package pause

import (
	"context"
	"fmt"
	"sync"

	"yagent/internal/agents/activity"
)

// gateKey 是存入 context.Value 的非导出 key 类型（照 activity 的 ctxKey 约定：
// 非导出 struct 而非字符串或导出类型，避免与其他包的 key 冲突）。
type gateKey struct{}

// AbandonedError 表示"暂停中被取消"：用户先暂停，随后按了停止或退出了进程。
//
// 它必须与裸 context.Canceled 可区分 —— 恢复侧据此知道这一轮停在一个 step 边界上
// （那一步的工具批次已整体提交完），而不是停在某个工具中途；
// 同时又必须能 errors.Is 到 context.Canceled，因为取消的那套传播（toolbatch 的
// 取消短路、delegateTo 的错误分类、looksLikeCancelledContext 的文本匹配）都认它。
// 换成一个孤立的新错误，表现是"取消了但没人认为是取消"，然后 Director 把它当成
// 一次失败的委派继续往下跑。
type AbandonedError struct {
	// Reason 是暂停时给出的原因（如 "/pause"），可能为空。
	Reason string
	// Cause 是 ctx 的取消原因，通常是 context.Canceled 或 activity 的超时错误。
	Cause error
}

func (e *AbandonedError) Error() string {
	cause := "<nil>"
	if e.Cause != nil {
		cause = e.Cause.Error()
	}
	if e.Reason == "" {
		return fmt.Sprintf("paused run abandoned: %s", cause)
	}
	return fmt.Sprintf("paused run abandoned (%s): %s", e.Reason, cause)
}

// Unwrap 把 ctx 的取消原因留在错误链上，errors.Is(err, context.Canceled) 才继续成立。
func (e *AbandonedError) Unwrap() error { return e.Cause }

// Gate 是一个 per-task 的暂停闸门。Pause/Resume 都是短临界区，不持锁等任何 IO；
// 阻塞只发生在 Wait 里，而 Wait 只被循环 goroutine 自己在 step 边界调用。
type Gate struct {
	mu     sync.Mutex
	paused bool
	// reason 是本次暂停的原因，只用于诊断与 UI 文案。
	reason string
	// waiters 是正停在闸门上的循环数。嵌套委派时 Director 与它派出的子 Agent
	// 会同时停在同一个闸门上，所以这是个计数而不是布尔 —— 布尔的话先醒来那个
	// 会把还在停的人报成"没停"。
	waiters int
	// resumeCh 每次 Resume 时关闭并重建；关闭即广播给所有停在闸门上的循环。
	resumeCh chan struct{}
}

// NewGate 创建一个未暂停的闸门。
func NewGate() *Gate {
	return &Gate{resumeCh: make(chan struct{})}
}

// WithGate 把闸门挂到 ctx 上。任务入口（TUI / HTTP）建 ctx 时调用一次，
// 之后整条委派链都能 From(ctx) 取到同一个闸门。
func WithGate(ctx context.Context, g *Gate) context.Context {
	return context.WithValue(ctx, gateKey{}, g)
}

// From 取回 ctx 上的闸门；没有则返回 nil。返回 nil 是常态（单元测试、Custom Agent、
// 非交互 --prompt），所以接入点一律写成 pause.From(ctx).Wait(ctx) ——
// Wait 对 nil receiver 安全，这样没有闸门的调用方一行都不用改。
func From(ctx context.Context) *Gate {
	if ctx == nil {
		return nil
	}
	g, _ := ctx.Value(gateKey{}).(*Gate)
	return g
}

// Wait 在未暂停时立即返回 nil；暂停时阻塞到 Resume，或 ctx 被取消。
//
// 阻塞期间冻结 activity 的 idle 与 total 计时 —— 心跳只能续 idle，total 的起点是
// 创建时固定的，不显式冻结的话暂停超过 delegate_total_timeout（默认 20m）照样被杀，
// "停在这里等人"就成了假承诺。
//
// 返回 *AbandonedError 表示"停着的时候被取消了"；返回 nil 表示可以往下跑。
func (g *Gate) Wait(ctx context.Context) error {
	if g == nil {
		return nil
	}
	for {
		g.mu.Lock()
		if !g.paused {
			g.mu.Unlock()
			return nil
		}
		reason := g.reason
		ch := g.resumeCh
		g.waiters++
		g.mu.Unlock()

		activity.Pause(ctx)
		select {
		case <-ctx.Done():
			activity.Resume(ctx)
			g.leave()
			cause := ctx.Err()
			if c := context.Cause(ctx); c != nil {
				cause = c
			}
			return &AbandonedError{Reason: reason, Cause: cause}
		case <-ch:
			activity.Resume(ctx)
			g.leave()
			// 可能又被 Pause 了：回到循环顶部重读状态。
		}
	}
}

// leave 给停在闸门上的计数减一。单独成函数是因为两条唤醒路径都必须减，
// 漏一条就再也不会返回"没人停着"，UI 于是永远显示"已停住"。
func (g *Gate) leave() {
	g.mu.Lock()
	if g.waiters > 0 {
		g.waiters--
	}
	g.mu.Unlock()
}

// Pause 请求暂停：此后的每个 step 边界都会停住。幂等 —— 已在暂停中则保留第一个
// 原因，不被后续调用改写（否则"为什么停"会答成最后一次按键）。
func (g *Gate) Pause(reason string) {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.paused {
		return
	}
	g.paused = true
	g.reason = reason
	if g.resumeCh == nil {
		g.resumeCh = make(chan struct{})
	}
}

// Resume 放开闸门，停在 step 边界上的循环继续往下跑。幂等；未在暂停中时是 no-op ——
// 尤其不重建 resumeCh，否则会给正在 Wait 的循环留下一个永远不会被关闭的通道。
func (g *Gate) Resume() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.paused {
		return
	}
	g.paused = false
	g.reason = ""
	close(g.resumeCh)
	g.resumeCh = make(chan struct{})
}

// Paused 报告是否请求了暂停。它为真不代表执行已经停下 —— 见 Blocked。
func (g *Gate) Paused() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.paused
}

// Blocked 报告确有循环停在了 step 边界上（真的停住了）。
// pause → 未 blocked 之间那段是"正在停"：还有工具在飞。
func (g *Gate) Blocked() bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.waiters > 0
}

// Reason 返回暂停原因；未暂停时为空串。
func (g *Gate) Reason() string {
	if g == nil {
		return ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.reason
}
