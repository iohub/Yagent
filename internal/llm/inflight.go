package llm

import (
	"context"
	"fmt"
	"sync/atomic"
)

// 进程级在飞请求闸门（docs/structured-concurrency-design.md §8.4）。
//
// 为什么是进程级而不是每引擎一份：429 是 provider 的全局属性。两个引擎各放 8 格
// 并不等于"最多 8 个请求在这个账号上飞"，而并发委派一开，同一账号上的引擎数
// 恰好是没人数过的那部分。
//
// 为什么挂在 GenerateContent 入口而不是 transport 层：transport 的
// MaxIdleConns/MaxIdleConnsPerHost 管的是连接复用，不是并发闸门；而重试
// （retryChatCompletion / retryRequest）在引擎内部循环，只有这里能保证"一次逻辑请求
// 只占一格、退避期间不额外放行请求"。

type inFlightLimiter struct {
	limit int
	// sem 为 nil 表示不限。释放用的是 acquire 时抓到的那个 limiter，所以改限制不会把
	// 旧的在飞请求记到新的闸门上（代价：换限制的那一瞬间计数会重新起算）。
	sem chan struct{}
}

var (
	inFlight     atomic.Pointer[inFlightLimiter]
	inFlightBusy atomic.Int64
)

func init() {
	// 默认不限：没经过配置装配的调用方（测试、直接构造引擎的工具）行为与今天一致。
	inFlight.Store(&inFlightLimiter{})
}

// SetInFlightLimit 设置同时在飞的 provider 调用上限；n<=0 表示不限。
// n 与当前相同则什么都不做 —— 运行中换 provider 会重建引擎并再次调用这里，
// 若每次都重建闸门，那一个瞬间的在飞请求会全部不被计数。
func SetInFlightLimit(n int) {
	if n < 0 {
		n = 0
	}
	if cur := inFlight.Load(); cur != nil && cur.limit == n {
		return
	}
	next := &inFlightLimiter{limit: n}
	if n > 0 {
		next.sem = make(chan struct{}, n)
	}
	inFlight.Store(next)
}

// InFlightLimit 返回当前上限，0 表示不限。
func InFlightLimit() int {
	lim := inFlight.Load()
	if lim == nil {
		return 0
	}
	return lim.limit
}

// InFlightBusy 返回当前占用的格子数（不限时恒为 0）。观测与测试用。
func InFlightBusy() int {
	return int(inFlightBusy.Load())
}

// acquireInFlight 占一格并返回释放函数；ctx 先结束则返回 ctx.Err()。
//
// 排队发生在调用超时之前：等格子不是"provider 慢"，把它算进 timeout 会让一次
// 正常的排队在真正发出去之前就被判死。
func acquireInFlight(ctx context.Context) (func(), error) {
	lim := inFlight.Load()
	if lim == nil || lim.sem == nil {
		return func() {}, nil
	}
	select {
	case lim.sem <- struct{}{}:
		inFlightBusy.Add(1)
		return func() {
			inFlightBusy.Add(-1)
			<-lim.sem
		}, nil
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for an in-flight LLM slot: %w", ctx.Err())
	}
}
