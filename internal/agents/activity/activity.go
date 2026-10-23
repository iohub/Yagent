// Package activity 提供"活动感知空闲超时"监视机制：固定总时长超时必然误杀长任务，故有动作就续 idle、持续无动作才取消。
package activity

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"yagent/internal/supervise"
)

// DefaultCheckInterval 是 CheckInterval 未配置（≤0）时的默认监视轮询间隔。
const DefaultCheckInterval = time.Second

type Options struct {
	// IdleTimeout 必填 > 0：持续无动作超过此值即取消 context（≤0 属编程错误，panic）。
	IdleTimeout time.Duration
	// TotalTimeout 可选：0 = 禁用总上限；> 0 时自创建起总耗时超过即取消。
	TotalTimeout time.Duration
	// CheckInterval 监视轮询间隔，≤0 时取 DefaultCheckInterval（默认 1s，测试可调小）。
	CheckInterval time.Duration

	// now 测试钩子，默认 time.Now（非导出字段，包内测试直接构造）。
	now func() time.Time
}

// ctxKey 用非导出 struct 类型（而非导出类型或字符串），避免与其他包的 context.Value key 冲突。
type ctxKey struct{}

// record 构造后只读（不可变），沿 sink 链共享同一指针是安全的。
type record struct {
	at   time.Time
	kind string
}

// sink 的 parent 指向嵌套 delegate 场景下外层 delegate 的槽位，心跳沿链向上逐层续期。
// 暂停计时用两个原子 int64 而非 mutex：Resume 结算并清零 pausedAt，所以 Pause/Resume 各自幂等，
// 嵌套两层闸门重复冻结同一链条也只结算一次。
type sink struct {
	parent      *sink
	last        atomic.Pointer[record]
	pausedAt    atomic.Int64
	pausedTotal atomic.Int64
}

// IdleTimeoutError 经 context.WithCancelCause 注入，调用方用 context.Cause / errors.As 取回。
type IdleTimeoutError struct {
	IdleFor      time.Duration
	Threshold    time.Duration
	TotalElapsed time.Duration
	// LastActivityKind 最后一个动作标签（如 "tool:start:run_bash"、"llm done"）。
	LastActivityKind string
	LastActivityAgo  time.Duration
}

func (e *IdleTimeoutError) Error() string {
	return fmt.Sprintf("no activity for %s (idle threshold %s); last activity: %s %s ago; total elapsed %s",
		e.IdleFor, e.Threshold, describeKind(e.LastActivityKind), e.LastActivityAgo, e.TotalElapsed)
}

// TotalTimeoutError 表示总时长上限触发的取消原因（与是否空闲无关）。
type TotalTimeoutError struct {
	Limit            time.Duration
	TotalElapsed     time.Duration
	LastActivityKind string
	LastActivityAgo  time.Duration
}

func (e *TotalTimeoutError) Error() string {
	return fmt.Sprintf("total limit %s exceeded; last activity: %s %s ago; total elapsed %s",
		e.Limit, describeKind(e.LastActivityKind), e.LastActivityAgo, e.TotalElapsed)
}

// describeKind 认 "tool:start:NAME" 形态（心跳标签约定）生成 "tool 'NAME' started"，其余形态原样输出。
func describeKind(kind string) string {
	if kind == "" {
		return "<unknown>"
	}
	if name, ok := strings.CutPrefix(kind, "tool:start:"); ok && name != "" {
		return fmt.Sprintf("tool '%s' started", name)
	}
	return kind
}

// WithIdleTimeout 创建活动感知空闲超时监视器：有动作（Heartbeat）就重置计时器，持续无动作超过 opts.IdleTimeout 才取消。
// IdleTimeout ≤ 0 属编程错误直接 panic；取消原因首因胜出（监视器注入的超时与调用方先到的 cancel 谁先谁生效）。
func WithIdleTimeout(parent context.Context, opts Options) (context.Context, context.CancelFunc) {
	if opts.IdleTimeout <= 0 {
		panic(fmt.Sprintf("activity: IdleTimeout must be > 0, got %v", opts.IdleTimeout))
	}
	nowFn := opts.now
	if nowFn == nil {
		nowFn = time.Now
	}
	checkInterval := opts.CheckInterval
	if checkInterval <= 0 {
		checkInterval = DefaultCheckInterval
	}

	ctx, cancelCause := context.WithCancelCause(parent)
	// 返回类型是 context.CancelFunc，所以主动取消走 cancelCause(nil)；监视器超时则直接调 cancelCause 注入原因。
	cancel := context.CancelFunc(func() { cancelCause(nil) })

	// 链接既有父 sink（嵌套 delegate）：内层心跳沿链上抛续期外层监视器。
	var parentSink *sink
	if p, ok := parent.Value(ctxKey{}).(*sink); ok {
		parentSink = p
	}
	startTime := nowFn()
	s := &sink{parent: parentSink}
	s.last.Store(&record{at: startTime, kind: "start"})
	ctx = context.WithValue(ctx, ctxKey{}, s)

	idleThreshold := opts.IdleTimeout
	totalLimit := opts.TotalTimeout

	// 监视器挂到**父** ctx 的域上（不是本函数派生出来的那个 ctx —— 它会自己取消，
	// 而取消与被等退出是两件事）。fn 不接收参数：内部的 select 认的是外层派生 ctx。
	supervise.From(parent).Go("activity/monitor", func(context.Context) {
		ticker := time.NewTicker(checkInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// cancel 后最多再处理一次 tick 即退出，保证"必然退出"。
				if ctx.Err() != nil {
					return
				}
				// 时间与标签取自同一原子快照，保证错误消息内一致。
				last := s.last.Load()
				now := nowFn()
				// idleFor 不必再扣暂停时长：Resume 会把空闲基准推到恢复时刻。
				idleFor := now.Sub(last.at)
				totalElapsed := now.Sub(startTime) - time.Duration(s.pausedTotal.Load())
				if pa := s.pausedAt.Load(); pa != 0 {
					// 暂停中：这段既不算空闲（否则 60s 阈值照样把停住的任务杀掉），
					// 也不算总耗时（total 的起点是创建时刻固定的，心跳续不了它）。
					totalElapsed -= now.Sub(time.Unix(0, pa))
					idleFor = 0
				}
				if idleFor > idleThreshold {
					cancelCause(&IdleTimeoutError{
						IdleFor:          idleFor,
						Threshold:        idleThreshold,
						TotalElapsed:     totalElapsed,
						LastActivityKind: last.kind,
						LastActivityAgo:  now.Sub(last.at),
					})
					return
				}
				if totalLimit > 0 && totalElapsed > totalLimit {
					cancelCause(&TotalTimeoutError{
						Limit:            totalLimit,
						TotalElapsed:     totalElapsed,
						LastActivityKind: last.kind,
						LastActivityAgo:  now.Sub(last.at),
					})
					return
				}
			}
		}
	})

	return ctx, cancel
}

// Heartbeat 向 ctx 关联的监视链上报一次活动（有动作就重置计时器），沿 parent 链向上逐层续期。
// 必须 non-blocking（纯原子写，不发送 channel、不持锁等待）；晚于 cancel 到达的心跳无害：只写槽位，无人消费。
func Heartbeat(ctx context.Context, detail string) {
	s, ok := ctx.Value(ctxKey{}).(*sink)
	if !ok || s == nil {
		return
	}
	r := &record{at: time.Now(), kind: detail}
	for cur := s; cur != nil; cur = cur.parent {
		cur.last.Store(r)
	}
}

// Pause 冻结 ctx 所在监视链上的 idle 与 total 计时（沿 parent 链逐层置位）。
// 必须由暂停方显式调用而不是靠心跳：Heartbeat 只能续 idle —— totalElapsed 取自创建时固定的
// startTime，持续心跳也照样会在 delegate_total_timeout（默认 20m）之后把停住的任务杀掉。
func Pause(ctx context.Context) {
	s, ok := ctx.Value(ctxKey{}).(*sink)
	if !ok || s == nil {
		return
	}
	now := time.Now().UnixNano()
	for cur := s; cur != nil; cur = cur.parent {
		cur.pausedAt.CompareAndSwap(0, now)
	}
}

// Resume 解冻监视链并把空闲基准推到恢复时刻 —— 不重置的话恢复后的第一个 tick 就算出
// idleFor = 暂停时长，刚恢复的任务立刻被杀掉。
// 未处于暂停状态时 no-op（不续 idle，否则重复 Resume 变成免费心跳）。
func Resume(ctx context.Context) {
	s, ok := ctx.Value(ctxKey{}).(*sink)
	if !ok || s == nil {
		return
	}
	now := time.Now()
	for cur := s; cur != nil; cur = cur.parent {
		if pa := cur.pausedAt.Swap(0); pa != 0 {
			cur.pausedTotal.Add(now.UnixNano() - pa)
			cur.last.Store(&record{at: now, kind: "resumed"})
		}
	}
}
