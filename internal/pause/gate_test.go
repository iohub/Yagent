package pause

// 闸门自身的闸门（docs/pause-resume-design.md §4.1 的并发契约）。
//
// 这里最要命的两类失败都是"看起来没事"：丢了唤醒 → 任务永远停着；
// 唤醒错了对象 → 用户以为停住了，其实还在跑。所以每条都用通道而不是 sleep 判定，
// 并且 waiters 直接读（本文件是同包测试）。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func eventually(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

func blockedCount(g *Gate) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.waiters
}

// 未暂停时 Wait 必须立刻放行：接入点是每个 step 都走的一行，
// 它一旦有延迟，正常跑的任务就被拖成"每步等一次"。
func TestWaitPassesThroughWhenNotPaused(t *testing.T) {
	g := NewGate()
	done := make(chan error, 1)
	go func() { done <- g.Wait(context.Background()) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait() = %v, want nil", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Wait blocked although nothing was paused")
	}
	if g.Blocked() {
		t.Error("Blocked() = true after a pass-through Wait")
	}
}

// nil receiver 必须是 no-op：六个子 Agent、Custom Agent 与仓内几十处测试调用点
// 都不建闸门，接入点不能要求它们先判空。
func TestNilGateIsNoop(t *testing.T) {
	var g *Gate
	if err := g.Wait(context.Background()); err != nil {
		t.Errorf("nil gate Wait() = %v, want nil", err)
	}
	g.Pause("x") // 不得 panic
	g.Resume()
	if g.Paused() || g.Blocked() || g.Reason() != "" {
		t.Error("nil gate reports state")
	}
}

func TestFromContextRoundTrip(t *testing.T) {
	g := NewGate()
	if From(context.Background()) != nil {
		t.Error("From() on a ctx with no gate must be nil")
	}
	ctx := WithGate(context.Background(), g)
	if got := From(ctx); got != g {
		t.Errorf("From() = %p, want %p", got, g)
	}
	// 嵌套 ctx 继承同一个闸门：委派链往下传 ctx 就够了，不需要重新接线。
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	if got := From(child); got != g {
		t.Errorf("From() on a derived ctx = %p, want the same gate %p", got, g)
	}
	if From(nil) != nil {
		t.Error("From(nil) must be nil, not a panic")
	}
}

// 暂停 → 停住 → 恢复 → 放行，并且 Blocked 与 Paused 必须能区分"正在停"与"已停住"。
func TestBlocksUntilResume(t *testing.T) {
	g := NewGate()
	g.Pause("/pause")
	if !g.Paused() {
		t.Fatal("Paused() = false right after Pause")
	}
	if g.Reason() != "/pause" {
		t.Errorf("Reason() = %q, want \"/pause\"", g.Reason())
	}

	done := make(chan error, 1)
	go func() { done <- g.Wait(context.Background()) }()
	if !eventually(t, time.Second, func() bool { return blockedCount(g) == 1 }) {
		t.Fatalf("waiter never registered: Blocked()=%v waiters=%d", g.Blocked(), blockedCount(g))
	}
	if !g.Blocked() {
		t.Error("Blocked() = false while a loop is parked on the gate")
	}

	select {
	case err := <-done:
		t.Fatalf("Wait() returned %v while paused", err)
	case <-time.After(50 * time.Millisecond):
	}

	g.Resume()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait() after Resume = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Resume never woke the parked loop")
	}
	if g.Paused() || g.Blocked() {
		t.Errorf("after Resume: Paused()=%v Blocked()=%v, want both false", g.Paused(), g.Blocked())
	}
}

// 反复暂停-恢复必须都能停住：Wait 醒来后回到循环顶部重读状态，
// 只判一次的话第二次 Pause 就成了空操作。
func TestRepeatedPauseResumeCycles(t *testing.T) {
	g := NewGate()
	for i := 0; i < 3; i++ {
		g.Pause("cycle")
		done := make(chan error, 1)
		go func() { done <- g.Wait(context.Background()) }()
		if !eventually(t, time.Second, func() bool { return blockedCount(g) == 1 }) {
			t.Fatalf("cycle %d never parked", i)
		}
		g.Resume()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("cycle %d: Wait() = %v, want nil", i, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("cycle %d: Resume never woke the loop", i)
		}
	}
}

// 恢复发生在到达之前不能吃掉唤醒：Pause 后立刻 Resume，随后才进来的 Wait 必须放行。
// 实现里若 Resume 无条件重建 resumeCh，正在等的循环会挂在一个永不关闭的通道上。
func TestResumeBeforeWaiterArrives(t *testing.T) {
	g := NewGate()
	g.Pause("x")
	g.Resume()

	done := make(chan error, 1)
	go func() { done <- g.Wait(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait() = %v, want nil", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("a Resume that landed before the waiter never took effect")
	}
}

// 嵌套委派：Director 与它派出的子 Agent 停在同一个闸门上，一次 Resume 必须全都唤醒。
func TestResumeWakesEveryParkedLoop(t *testing.T) {
	g := NewGate()
	g.Pause("user /pause")

	const n = 3
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() { errs <- g.Wait(context.Background()) }()
	}
	if !eventually(t, time.Second, func() bool { return blockedCount(g) == n }) {
		t.Fatalf("only %d/%d loops registered on the gate", blockedCount(g), n)
	}

	g.Resume()
	for i := 0; i < n; i++ {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatalf("waiter %d: Wait() = %v, want nil", i, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("only %d of %d loops woke on Resume", i, n)
		}
	}
	if blockedCount(g) != 0 {
		t.Errorf("waiters = %d after everyone woke, want 0 (leaked count)", blockedCount(g))
	}
}

// 暂停中被取消：必须是 AbandonedError（区别于裸取消），又必须仍能 errors.Is 到
// context.Canceled —— 取消传播那套（toolbatch、delegateTo 分类、文本匹配）都认它。
func TestAbandonedPauseKeepsCancelSemantics(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	g := NewGate()
	g.Pause("/pause")

	done := make(chan error, 1)
	go func() { done <- g.Wait(ctx) }()
	if !eventually(t, time.Second, func() bool { return blockedCount(g) == 1 }) {
		t.Fatal("loop never parked on the gate")
	}
	cancel()

	var err error
	select {
	case err = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling a parked run never returned from Wait")
	}

	var abandoned *AbandonedError
	if !errors.As(err, &abandoned) {
		t.Fatalf("Wait() = %v (%T), want *AbandonedError", err, err)
	}
	if abandoned.Reason != "/pause" {
		t.Errorf("Reason = %q, want \"/pause\"", abandoned.Reason)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("errors.Is(err, context.Canceled) = false for %q — cancel propagation would silently break", err)
	}
	if !errors.Is(err, ctx.Err()) {
		t.Errorf("errors.Is(err, ctx.Err()) = false for %q", err)
	}
	// 文案里必须留着底层原因：恢复侧有按文本认取消的地方
	// （工具错误早被格式化成字符串，errors.Is 在那条链上无从谈起）。
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("Error() = %q, want it to carry the underlying cause", err.Error())
	}
	if blockedCount(g) != 0 {
		t.Errorf("waiters = %d after abandonment, want 0", blockedCount(g))
	}
}

// 取消原因来自 activity 超时（WithCancelCause）时，cause 必须原样带上，
// 否则"暂停中被 idle 超时杀掉"会被认成用户取消。
func TestAbandonedCarriesCustomCause(t *testing.T) {
	boom := errors.New("no activity for 1m")
	ctx, cancelCause := context.WithCancelCause(context.Background())
	g := NewGate()
	g.Pause("/pause")

	done := make(chan error, 1)
	go func() { done <- g.Wait(ctx) }()
	if !eventually(t, time.Second, func() bool { return blockedCount(g) == 1 }) {
		t.Fatal("loop never parked on the gate")
	}
	cancelCause(boom)

	var err error
	select {
	case err = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling with a cause never returned from Wait")
	}
	if !errors.Is(err, boom) {
		t.Errorf("errors.Is(err, boom) = false for %q", err)
	}
	var abandoned *AbandonedError
	if !errors.As(err, &abandoned) {
		t.Fatalf("Wait() = %v (%T), want *AbandonedError", err, err)
	}
	if abandoned.Cause != boom {
		t.Errorf("Cause = %v, want the injected cause", abandoned.Cause)
	}
}

// 幂等：重复 Pause 保留第一个原因，重复 Resume 不制造状态。
// 原因被后一次按键改写的话，"为什么停"就答不出来了。
func TestPauseResumeAreIdempotent(t *testing.T) {
	g := NewGate()
	g.Pause("first")
	g.Pause("second")
	if got := g.Reason(); got != "first" {
		t.Errorf("Reason() = %q after a second Pause, want \"first\"", got)
	}

	done := make(chan error, 1)
	go func() { done <- g.Wait(context.Background()) }()
	if !eventually(t, time.Second, func() bool { return blockedCount(g) == 1 }) {
		t.Fatal("loop never parked")
	}
	g.Resume()
	g.Resume() // 第二次不得吃掉唤醒或留下无人关闭的通道
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait() = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("double Resume stranded the parked loop")
	}
	if blockedCount(g) != 0 {
		t.Errorf("waiters = %d, want 0", blockedCount(g))
	}
}
