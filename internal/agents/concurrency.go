package agents

import (
	"context"

	"yagent/internal/config"
)

// 这一文件是"并发放量"的判据集中地（docs/structured-concurrency-design.md §8.2/§10）。
//
// 两条闸门各自属于一个**循环**，不是进程：
//   - 只读工具批：一次 RunAgentLoop / 一次 planner 回合里能同时跑几个只读工具；
//   - 委派池：一次 Director 运行里能同时跑几个子 Agent。
// 分成 per-Run 而不是进程级，是为了让"嵌套委派"不可能自己等自己 ——
// Repo→delegate_devops 用的是 Repo 那一轮的池子，与 Director 的池子无关，
// 所以不存在"父占着一格、子在同一格里排队"的死锁形状。

const (
	// DefaultMaxParallelDelegations 是没配置时一次 Director 运行里的并发委派上限。
	DefaultMaxParallelDelegations = 2
	// MaxMaxParallelDelegations 是钳位上限：每个子 Agent 都要引擎、便签写盘与
	// 一整条 LLM 循环，放开到十几个只会把 429 和内存一起招来。
	MaxMaxParallelDelegations = 8
)

// NormalizeMaxParallelDelegations 与只读工具那套用的是同一套约定：
// 1 = 严格串行；≤0 = 没配（取默认）；>8 钳到 8。
// 急停开关与只读批共用同一个判据（YAGENT_CONCURRENCY=0 → 1）。
func NormalizeMaxParallelDelegations(n int) int {
	if !config.ConcurrencyEnabled() {
		return 1
	}
	switch {
	case n == 1:
		return 1
	case n <= 0:
		return DefaultMaxParallelDelegations
	case n > MaxMaxParallelDelegations:
		return MaxMaxParallelDelegations
	default:
		return n
	}
}

// newDelegatePool 按并行度建池。串行用 size-1 的通道表达（不是"没有池"），
// 所以 n 至少为 1；没配的值先经 NormalizeMaxParallelDelegations 折成默认。
func newDelegatePool(n int) chan struct{} {
	if n < 1 {
		n = 1
	}
	return make(chan struct{}, n)
}

// acquireDelegateSlot 占一格并返回释放函数。
//
// ctx 已结束时**不排队、也不做释放接收** —— 与 toolbatch.Run 里那段注释同一个理由：
// 从未取得的一格若去 `<-pool`，会与其他释放方的接收互相吃掉，把整批卡在
// WaitGroup 上。取不到格子时由调用方产出一条错误 tool 结果，配对不变量照守。
func acquireDelegateSlot(ctx context.Context, pool chan struct{}) (func(), bool) {
	if pool == nil {
		return func() {}, true
	}
	select {
	case pool <- struct{}{}:
		return func() { <-pool }, true
	case <-ctx.Done():
		return nil, false
	}
}
