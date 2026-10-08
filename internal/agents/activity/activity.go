// Package activity 提供"活动感知空闲超时"监视机制。
//
// 背景：Director 委派子代理的 delegate_* 工具若使用固定总时长超时，长任务
// 必然被误杀。本包改为"有动作就重置计时器、持续无动作才超时"：
//
//   - 监视器（WithIdleTimeout）在独立 goroutine 中以固定间隔轮询最近活动
//     时间戳，空闲超过 IdleTimeout 或总耗时超过 TotalTimeout 时经
//     context.WithCancelCause 注入可诊断错误并取消 context；
//   - 心跳（Heartbeat）由被监视方（子代理各执行环节）上报活动，纯原子写，
//     non-blocking、无竞态、无泄漏；
//   - 嵌套 delegate 场景下，内层子代理的心跳沿 parent 链上抛续期外层监视器。
//
// 设计取舍：心跳是纯原子写（atomic.Store），天然 non-blocking、无竞态、
// 无泄漏；代价是空闲检测延迟至多 CheckInterval（轮询间隔），可忽略。
// 不采用 channel + 可重置 timer 方案（有锁、有 channel 语义负担）。
package activity

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// DefaultCheckInterval 是 CheckInterval 未配置（≤0）时的默认监视轮询间隔。
const DefaultCheckInterval = time.Second

// Options 配置活动感知空闲超时监视器。
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

// ctxKey 是 WithIdleTimeout 存入 context.Value 的私有 key 类型。
// 使用非导出 struct 类型（而非导出类型或字符串），避免与其他包的 key 冲突。
type ctxKey struct{}

// record 单次心跳记录：时间戳 + 动作标签。构造后只读（不可变），
// 沿 sink 链共享同一指针是安全的。
type record struct {
	at   time.Time
	kind string
}

// sink 是存放于 context.Value 的最近活动槽位。parent 指向嵌套 delegate
// 场景下外层 delegate 的 sink；心跳沿 parent 链向上续期各层监视器。
type sink struct {
	parent *sink
	last   atomic.Pointer[record]
}

// IdleTimeoutError 表示持续无动作超过空闲阈值导致的取消原因。
// 经 context.WithCancelCause 注入，调用方用 context.Cause / errors.As 取回。
type IdleTimeoutError struct {
	// IdleFor 触发时实际空闲时长。
	IdleFor time.Duration
	// Threshold 空闲阈值。
	Threshold time.Duration
	// TotalElapsed 本次 delegate（监视器创建起）总耗时。
	TotalElapsed time.Duration
	// LastActivityKind 最后一个动作标签（如 "tool:start:run_bash"、"llm done"）。
	LastActivityKind string
	// LastActivityAgo 最后动作距今时长。
	LastActivityAgo time.Duration
}

// Error 生成可诊断消息，例如：
//
//	no activity for 10m3s (idle threshold 10m0s); last activity: tool 'apply_patch' started 10m3s ago; total elapsed 27m40s
func (e *IdleTimeoutError) Error() string {
	return fmt.Sprintf("no activity for %s (idle threshold %s); last activity: %s %s ago; total elapsed %s",
		e.IdleFor, e.Threshold, describeKind(e.LastActivityKind), e.LastActivityAgo, e.TotalElapsed)
}

// TotalTimeoutError 表示总时长上限触发的取消原因（与是否空闲无关）。
type TotalTimeoutError struct {
	// Limit 总时长上限。
	Limit time.Duration
	// TotalElapsed 触发时总耗时。
	TotalElapsed time.Duration
	// LastActivityKind 最后一个动作标签。
	LastActivityKind string
	// LastActivityAgo 最后动作距今时长。
	LastActivityAgo time.Duration
}

// Error 生成可诊断消息，例如：
//
//	total limit 1h0m0s exceeded; last activity: llm done 12s ago; total elapsed 1h0m1s
func (e *TotalTimeoutError) Error() string {
	return fmt.Sprintf("total limit %s exceeded; last activity: %s %s ago; total elapsed %s",
		e.Limit, describeKind(e.LastActivityKind), e.LastActivityAgo, e.TotalElapsed)
}

// describeKind 把动作标签转为更可读的描述。识别 "tool:start:NAME" 形态
// （第二批次心跳标签约定）生成 "tool 'NAME' started"；其他形态原样输出
// （kind 由调用方定义，如 "llm done" 本身即可读）。
func describeKind(kind string) string {
	if kind == "" {
		return "<unknown>"
	}
	if name, ok := strings.CutPrefix(kind, "tool:start:"); ok && name != "" {
		return fmt.Sprintf("tool '%s' started", name)
	}
	return kind
}

// WithIdleTimeout 创建活动感知空闲超时监视器：
// 有动作（Heartbeat）就重置计时器，持续无动作超过 opts.IdleTimeout 才取消。
//
//   - opts.IdleTimeout ≤ 0 属编程错误，直接 panic；
//   - 返回的 ctx 上已挂载本监视器的活动槽位，供 Heartbeat 上报；
//   - 若 parent ctx 已存在监视器（嵌套 delegate），新监视器链接到外层槽位，
//     内层心跳沿链上抛续期外层监视器；
//   - 返回的 cancel 为主动取消（包装 cancelCause(nil)，cause=nil 时
//     context.Cause 返回 context.Canceled）；监视 goroutine 内部超时触发
//     直接调用 cancelCause 注入错误原因，调用方经 context.Cause/errors.As
//     取回（幂等，首因胜出：监视器注入的超时原因与调用方先到的 cancel
//     谁先谁生效）；
//   - 监视 goroutine 在 cancel / 触发超时后必然退出，无泄漏。
//
// 空闲检测延迟至多 CheckInterval（轮询间隔），属可忽略的固有代价。
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
	// API 约定返回 context.CancelFunc；包装 CancelCauseFunc：cancel() 为
	// 主动取消，等价 cancelCause(nil)（cause=nil 时 context.Cause 返回
	// context.Canceled）。监视 goroutine 内部超时触发不走此包装，直接调用
	// cancelCause 注入错误原因，供调用方经 context.Cause/errors.As 取回。
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

	go func() {
		ticker := time.NewTicker(checkInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				// cancel（调用方或本监视器触发）后必然退出。
				return
			case <-ticker.C:
				// cancel 后最多再处理一次 tick 即退出，保证"必然退出"。
				if ctx.Err() != nil {
					return
				}
				// 时间与标签取自同一原子快照，保证错误消息内一致。
				last := s.last.Load()
				now := nowFn()
				idleFor := now.Sub(last.at)
				totalElapsed := now.Sub(startTime)
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
	}()

	return ctx, cancel
}

// Heartbeat 向 ctx 关联的监视链上报一次活动（有动作就重置计时器）。
//
//   - 无监视器（普通 ctx）时静默 no-op；
//   - 必须 non-blocking：纯原子写，不发送 channel、不持锁等待；
//   - 晚于 cancel 到达的心跳无害：只写槽位，无人消费，不 panic；
//   - 嵌套 delegate：沿 parent 链向上逐层续期（循环实现，非递归调用），
//     保证内层子代理的活动为外层监视器续期。
func Heartbeat(ctx context.Context, detail string) {
	s, ok := ctx.Value(ctxKey{}).(*sink)
	if !ok || s == nil {
		return
	}
	// record 构造后只读，沿链共享同一指针；时间与标签取自同一次构造。
	r := &record{at: time.Now(), kind: detail}
	for cur := s; cur != nil; cur = cur.parent {
		cur.last.Store(r)
	}
}
