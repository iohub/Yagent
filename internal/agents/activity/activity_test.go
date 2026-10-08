package activity

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

// eventually 每 5ms 轮询 cond，最多等 timeout，返回 cond 最终是否满足。
func eventually(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return cond()
}

// 1) 无心跳：ctx 被 cancel 且 Cause 为 *IdleTimeoutError，字段值正确。
func TestIdleTimeoutNoHeartbeat(t *testing.T) {
	ctx, cancel := WithIdleTimeout(context.Background(), Options{
		IdleTimeout:   50 * time.Millisecond,
		CheckInterval: 10 * time.Millisecond,
	})
	defer cancel()

	select {
	case <-ctx.Done():
		cause := context.Cause(ctx)
		var idleErr *IdleTimeoutError
		if !errors.As(cause, &idleErr) {
			t.Fatalf("cause = %v (%T), want *IdleTimeoutError", cause, cause)
		}
		if idleErr.Threshold != 50*time.Millisecond {
			t.Errorf("Threshold = %v, want 50ms", idleErr.Threshold)
		}
		if idleErr.IdleFor < 50*time.Millisecond || idleErr.IdleFor > 300*time.Millisecond {
			t.Errorf("IdleFor = %v, want ≈ threshold (50ms) + at most a few ticks", idleErr.IdleFor)
		}
		if idleErr.LastActivityKind != "start" {
			t.Errorf("LastActivityKind = %q, want %q", idleErr.LastActivityKind, "start")
		}
		if idleErr.LastActivityAgo != idleErr.IdleFor {
			t.Errorf("LastActivityAgo = %v, want == IdleFor %v", idleErr.LastActivityAgo, idleErr.IdleFor)
		}
		if idleErr.TotalElapsed < idleErr.IdleFor {
			t.Errorf("TotalElapsed = %v, want >= IdleFor %v", idleErr.TotalElapsed, idleErr.IdleFor)
		}
		if idleErr.Error() == "" {
			t.Error("Error() message empty")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ctx not cancelled within 2s without heartbeat")
	}
}

// 2) 持续心跳不触发；停止心跳后从最后一次心跳起再过阈值+CheckInterval 才 cancel。
func TestHeartbeatKeepsAliveThenTimesOutAfterStop(t *testing.T) {
	const idleTimeout = 50 * time.Millisecond
	ctx, cancel := WithIdleTimeout(context.Background(), Options{
		IdleTimeout:   idleTimeout,
		CheckInterval: 10 * time.Millisecond,
	})
	defer cancel()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(30 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				Heartbeat(ctx, "hb")
			}
		}
	}()

	// 持续心跳 5×阈值（250ms）不取消。
	time.Sleep(5 * idleTimeout)
	select {
	case <-ctx.Done():
		t.Fatalf("ctx cancelled while heartbeating: %v", context.Cause(ctx))
	default:
	}

	// 停止心跳 → 从最后一次心跳起算再过 idleTimeout+CheckInterval 才 cancel。
	close(stop)
	wg.Wait()
	time.Sleep(idleTimeout + 2*10*time.Millisecond + 30*time.Millisecond)
	select {
	case <-ctx.Done():
		cause := context.Cause(ctx)
		var idleErr *IdleTimeoutError
		if !errors.As(cause, &idleErr) {
			t.Fatalf("cause = %v (%T), want *IdleTimeoutError", cause, cause)
		}
		if idleErr.IdleFor < idleTimeout {
			t.Errorf("IdleFor = %v, want >= %v", idleErr.IdleFor, idleTimeout)
		}
		if idleErr.LastActivityKind != "hb" {
			t.Errorf("LastActivityKind = %q, want %q", idleErr.LastActivityKind, "hb")
		}
	default:
		t.Fatal("ctx not cancelled after heartbeat stopped")
	}
}

// 3) 心跳正常但 TotalTimeout 到期 → *TotalTimeoutError（带最近动作标签）。
func TestTotalTimeoutTriggers(t *testing.T) {
	ctx, cancel := WithIdleTimeout(context.Background(), Options{
		IdleTimeout:   time.Second, // 不会触发
		TotalTimeout:  100 * time.Millisecond,
		CheckInterval: 10 * time.Millisecond,
	})
	defer cancel()

	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for ctx.Err() == nil {
			<-ticker.C
			Heartbeat(ctx, "hb:llm:done")
		}
	}()

	select {
	case <-ctx.Done():
		cause := context.Cause(ctx)
		var totalErr *TotalTimeoutError
		if !errors.As(cause, &totalErr) {
			t.Fatalf("cause = %v (%T), want *TotalTimeoutError", cause, cause)
		}
		if totalErr.Limit != 100*time.Millisecond {
			t.Errorf("Limit = %v, want 100ms", totalErr.Limit)
		}
		if totalErr.TotalElapsed < 100*time.Millisecond {
			t.Errorf("TotalElapsed = %v, want >= 100ms", totalErr.TotalElapsed)
		}
		if totalErr.LastActivityKind != "hb:llm:done" {
			t.Errorf("LastActivityKind = %q, want heartbeat label", totalErr.LastActivityKind)
		}
		if totalErr.Error() == "" {
			t.Error("Error() message empty")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ctx not cancelled within 2s (TotalTimeout=100ms)")
	}
}

// 4) TotalTimeout=0 时永不总超时（短窗口验证）。
func TestNoTotalTimeoutWhenZero(t *testing.T) {
	ctx, cancel := WithIdleTimeout(context.Background(), Options{
		IdleTimeout:   time.Second, // 不会触发
		TotalTimeout:  0,           // 禁用总上限
		CheckInterval: 10 * time.Millisecond,
	})
	defer cancel()

	go func() {
		ticker := time.NewTicker(15 * time.Millisecond)
		defer ticker.Stop()
		for ctx.Err() == nil {
			<-ticker.C
			Heartbeat(ctx, "hb")
		}
	}()

	// 3×CheckInterval×若干：150ms（15 个 tick）内不取消。
	time.Sleep(150 * time.Millisecond)
	select {
	case <-ctx.Done():
		t.Fatalf("ctx cancelled with TotalTimeout=0: %v", context.Cause(ctx))
	default:
	}
}

// 5) cancel 后监视 goroutine 退出：100 个监视器后 NumGoroutine 回归基线（±10）。
func TestMonitorGoroutineExitsOnCancel(t *testing.T) {
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	const n = 100
	cancels := make([]context.CancelFunc, n)
	for i := 0; i < n; i++ {
		_, c := WithIdleTimeout(context.Background(), Options{
			IdleTimeout:   time.Hour, // 不会自动触发
			CheckInterval: 10 * time.Millisecond,
		})
		cancels[i] = c
	}
	for _, c := range cancels {
		c()
	}
	if !eventually(t, 2*time.Second, func() bool {
		return runtime.NumGoroutine() <= baseline+10
	}) {
		t.Fatalf("goroutine leak on cancel path: baseline=%d, now=%d", baseline, runtime.NumGoroutine())
	}
}

// 5b) 自动触发超时路径的 goroutine 同样退出。
func TestMonitorGoroutineExitsOnIdleTrigger(t *testing.T) {
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	const n = 100
	for i := 0; i < n; i++ {
		_, _ = WithIdleTimeout(context.Background(), Options{
			IdleTimeout:   30 * time.Millisecond, // 自动触发
			CheckInterval: 10 * time.Millisecond,
		})
	}
	if !eventually(t, 3*time.Second, func() bool {
		return runtime.NumGoroutine() <= baseline+10
	}) {
		t.Fatalf("goroutine leak on idle-trigger path: baseline=%d, now=%d", baseline, runtime.NumGoroutine())
	}
}

// 6) cancel 后再 Heartbeat 不 panic。
func TestHeartbeatAfterCancelNoPanic(t *testing.T) {
	ctx, cancel := WithIdleTimeout(context.Background(), Options{
		IdleTimeout:   50 * time.Millisecond,
		CheckInterval: 10 * time.Millisecond,
	})
	cancel()
	time.Sleep(50 * time.Millisecond) // 给监视 goroutine 退出时间
	Heartbeat(ctx, "late")
	Heartbeat(ctx, "late-again")
}

// 7) 嵌套链式：仅对内层 ctx 心跳 → 外层不 cancel（链式上抛）；停止心跳后两层均取消。
func TestNestedChainHeartbeatRenewsOuter(t *testing.T) {
	const idleTimeout = 200 * time.Millisecond
	outerCtx, outerCancel := WithIdleTimeout(context.Background(), Options{
		IdleTimeout:   idleTimeout,
		CheckInterval: 10 * time.Millisecond,
	})
	defer outerCancel()
	innerCtx, innerCancel := WithIdleTimeout(outerCtx, Options{
		IdleTimeout:   idleTimeout,
		CheckInterval: 10 * time.Millisecond,
	})
	defer innerCancel()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(30 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-innerCtx.Done():
				return
			case <-ticker.C:
				Heartbeat(innerCtx, "inner-hb") // 仅内层心跳，须上抛续期外层
			}
		}
	}()

	// 仅内层心跳持续 3×外层阈值（600ms）→ 外层不 cancel。
	time.Sleep(3 * idleTimeout)
	select {
	case <-outerCtx.Done():
		t.Fatalf("outer ctx cancelled while inner heartbeating: %v", context.Cause(outerCtx))
	default:
	}
	select {
	case <-innerCtx.Done():
		t.Fatalf("inner ctx cancelled while heartbeating: %v", context.Cause(innerCtx))
	default:
	}

	// 停止心跳 → 内层 cancel、外层随后 cancel（均被 IdleTimeoutError 取消）。
	close(stop)
	wg.Wait()
	if !eventually(t, 2*time.Second, func() bool { return innerCtx.Err() != nil }) {
		t.Fatal("inner ctx not cancelled after heartbeat stopped")
	}
	var innerErr *IdleTimeoutError
	if cause := context.Cause(innerCtx); !errors.As(cause, &innerErr) {
		t.Fatalf("inner cause = %v (%T), want *IdleTimeoutError", cause, cause)
	}
	if !eventually(t, 2*time.Second, func() bool { return outerCtx.Err() != nil }) {
		t.Fatal("outer ctx not cancelled after heartbeat stopped (chain renewal broken)")
	}
	var outerErr *IdleTimeoutError
	if cause := context.Cause(outerCtx); !errors.As(cause, &outerErr) {
		t.Fatalf("outer cause = %v (%T), want *IdleTimeoutError", cause, cause)
	}
}

// 8) 无 sink 的普通 ctx 调用 Heartbeat 为 no-op（不 panic 不影响）。
func TestHeartbeatNoSinkNoop(t *testing.T) {
	Heartbeat(context.Background(), "no-sink")
	cctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	Heartbeat(cctx, "no-sink-with-cancel")
}

// 9) 多 goroutine 并发 Heartbeat 同一 ctx 无竞态（-race 验证）。
func TestConcurrentHeartbeatNoRace(t *testing.T) {
	ctx, cancel := WithIdleTimeout(context.Background(), Options{
		IdleTimeout:   2 * time.Second,
		CheckInterval: 10 * time.Millisecond,
	})
	defer cancel()

	var wg sync.WaitGroup
	const workers = 8
	const perWorker = 500
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < perWorker; j++ {
				Heartbeat(ctx, fmt.Sprintf("worker:%d:step:%d", id, j))
			}
		}(i)
	}
	wg.Wait()
	select {
	case <-ctx.Done():
		t.Fatalf("ctx cancelled unexpectedly: %v", context.Cause(ctx))
	default:
	}
}

// IdleTimeout ≤ 0 属编程错误，必须 panic。
func TestPanicOnInvalidIdleTimeout(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on IdleTimeout <= 0")
		}
	}()
	_, _ = WithIdleTimeout(context.Background(), Options{IdleTimeout: 0})
}

// 错误消息格式：含阈值/空闲/最后动作/总耗时，tool:start: 形态被美化。
func TestErrorMessages(t *testing.T) {
	e := &IdleTimeoutError{
		IdleFor:          10*time.Minute + 3*time.Second,
		Threshold:        10 * time.Minute,
		TotalElapsed:     27*time.Minute + 40*time.Second,
		LastActivityKind: "tool:start:apply_patch",
		LastActivityAgo:  10*time.Minute + 3*time.Second,
	}
	want := "no activity for 10m3s (idle threshold 10m0s); last activity: tool 'apply_patch' started 10m3s ago; total elapsed 27m40s"
	if got := e.Error(); got != want {
		t.Errorf("IdleTimeoutError.Error() =\n  %q\nwant\n  %q", got, want)
	}

	te := &TotalTimeoutError{
		Limit:            time.Hour,
		TotalElapsed:     time.Hour + time.Second,
		LastActivityKind: "llm done",
		LastActivityAgo:  12 * time.Second,
	}
	wantTe := "total limit 1h0m0s exceeded; last activity: llm done 12s ago; total elapsed 1h0m1s"
	if got := te.Error(); got != wantTe {
		t.Errorf("TotalTimeoutError.Error() =\n  %q\nwant\n  %q", got, wantTe)
	}
}
