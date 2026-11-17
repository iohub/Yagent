package llm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"yagent/internal/config"
)

// 流式响应的"活着没有"三层兜底：
//
//	首字节      http.Transport.ResponseHeaderTimeout（engine_*.go 构造期）
//	流内空闲    本文件的 idleWatchdog（每读到任意 chunk/SSE 事件就续期）
//	总预算      ctx 的 deadline（调用方给，见 ApplyCallTimeout）
//
// 只有总预算时的缺陷：连接被中间设备静默丢弃、或 provider 卡在推理里不吐字节，
// 读循环会**永久阻塞**到总预算耗尽（默认 10 分钟），表现为"死机之后才超时"。
// 空闲看门狗把"已经死了"的流在 [llm].stream_idle_timeout（默认 90s）内判出来。

// ErrStreamStalled 是"流在阈值内一个字节都没来"的哨兵错误。
//
// 它**刻意不包裹** context.Canceled/DeadlineExceeded：看门狗确实取消了本次尝试的
// 子 ctx，但"我们主动判死"与"调用方取消"是两件不同的事 —— 混成同一个判据，上层就
// 再也分不出"用户按了停止"和"网络静默超时"（isTransientNetworkError 正是靠
// context.Canceled 这条判据把用户取消排除在重试之外的）。
var ErrStreamStalled = errors.New("stream stalled")

// streamStalledError 带上具体阈值，好让日志直接看出配置值。
type streamStalledError struct {
	idle time.Duration
}

func (e *streamStalledError) Error() string {
	return fmt.Sprintf("stream stalled: no data received for %s", e.idle)
}

// Unwrap 暴露哨兵，errors.Is(err, ErrStreamStalled) 因此可用。
func (e *streamStalledError) Unwrap() error { return ErrStreamStalled }

// idleWatchdog 在 idle 时间内没有 touch 时就 cancel 本次尝试的子 ctx。
//
// nil 看门狗上的所有方法都是空操作，调用点不必到处写 `if wd != nil`。
type idleWatchdog struct {
	idle   time.Duration
	cancel context.CancelFunc

	mu      sync.Mutex
	timer   *time.Timer
	fired   bool
	stopped bool
}

// newIdleWatchdog 为一次尝试派生可取消的 ctx 并挂上看门狗。
// idle <= 0 时返回原 ctx 与 nil 看门狗（"不设看门狗"只在本包内部用于不可能取到 0
// 的场合；生产值经 config.EffectiveStreamIdleTimeout 折过，恒 > 0）。
func newIdleWatchdog(parent context.Context, idle time.Duration) (context.Context, *idleWatchdog) {
	if idle <= 0 {
		return parent, nil
	}
	ctx, cancel := context.WithCancel(parent)
	w := &idleWatchdog{idle: idle, cancel: cancel}
	w.mu.Lock()
	// 用 AfterFunc 而不是 time.NewTimer：这里不需要"选一个通道"，只需要"到点干活"，
	// 于是也就不存在忘了 drain 那个坑（AfterFunc 计时器没有可 drain 的通道）。
	w.timer = time.AfterFunc(idle, w.fire)
	w.mu.Unlock()
	return ctx, w
}

// fire 由计时器在 idle 无数据后触发。
func (w *idleWatchdog) fire() {
	w.mu.Lock()
	if w.stopped || w.fired {
		w.mu.Unlock()
		return
	}
	w.fired = true
	w.mu.Unlock()
	// cancel 放到锁外：它会同步唤醒所有等待者，持锁调用等于把阻塞把锁让给别人。
	w.cancel()
}

// touch 记录"又收到数据了"：把计时器推后一个 idle 窗口。
//
// 已 fire / 已 release 之后不再复活计时器：Reset 一个已经到点的 AfterFunc 计时器
// 会再跑一次 fire，而那次 fire 属于上一轮尝试。这里选择安全的一侧 —— 宁可少续一期，
// 也不让一次迟到的 fire 去取消新一轮的请求。
func (w *idleWatchdog) touch() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.fired || w.stopped || w.timer == nil {
		return
	}
	w.timer.Stop()
	w.timer.Reset(w.idle)
}

// err 报告看门狗是否已经判死，并给出对应的错误。
func (w *idleWatchdog) err() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.fired {
		return nil
	}
	return &streamStalledError{idle: w.idle}
}

// release 停掉计时器并取消本次尝试的 ctx。调用点一律 `defer wd.release()`。
//
// AfterFunc 计时器没有可排空的通道：Stop 返回 false 只代表 fire 已经在别的 goroutine
// 上跑起来了，而它第一步就是看 stopped —— 所以这里既不需要、也无从 drain。
func (w *idleWatchdog) release() {
	if w == nil {
		return
	}
	w.mu.Lock()
	w.stopped = true
	timer := w.timer
	w.timer = nil
	w.mu.Unlock()
	if timer != nil {
		timer.Stop()
	}
	w.cancel()
}

// withAttemptTimeout 给一次尝试套独立上限（外层兜底）。
//
// limit <= 0 表示不额外限制：此时单次尝试只受 parent 的总预算约束。
// parent 已有更紧的 deadline 时原样返回 parent —— 配置值是上限，不是"至少"，
// 二次 WithTimeout 只会把调用方的预算悄悄放宽。
func withAttemptTimeout(parent context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	if limit <= 0 {
		return parent, func() {}
	}
	if deadline, ok := parent.Deadline(); ok && time.Until(deadline) <= limit {
		return parent, func() {}
	}
	return context.WithTimeout(parent, limit)
}

// notifyStream 把一条人类可读的状态提示（回退 / 重试）送进流式通道。
//
// 返回值必须忽略：生产里的 handler 返回错误表示"用户中断"，而一条推不进去的提示
// 不该让整次请求失败（提示是可选的，请求不是）。流式一切正常时不输出任何内容。
func notifyStream(ctx context.Context, handler StreamHandler, text string) {
	if handler == nil || text == "" {
		return
	}
	_ = handler(ctx, []byte(text))
}

// 提示文案集中在这里：TUI 与 llm 日志排查时按同一串字符能对上。
const (
	streamFallbackNotice = "\n\n[provider 流式响应失败，已回退到非流式重试]\n"
)

// retryNoticeText 形如 "\n[重试 2/5]\n"。
func retryNoticeText(attempt, maxRetries int) string {
	return fmt.Sprintf("\n[重试 %d/%d]\n", attempt, maxRetries)
}

// resolveStreamIdleTimeout / resolveFirstByteTimeout 是引擎侧的取值口：
// 显式配置 > 全局值 > 默认值（恒 > 0，见 config.Effective*）。
func resolveStreamIdleTimeout(cfg config.LLMConfig) time.Duration {
	return config.EffectiveStreamIdleTimeout(cfg.StreamIdleTimeout)
}

func resolveFirstByteTimeout(cfg config.LLMConfig) time.Duration {
	return config.EffectiveFirstByteTimeout(cfg.FirstByteTimeout)
}
