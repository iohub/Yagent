package recovery

import (
	"sync"
	"testing"
	"time"
)

// 熔断器是 Director 的唯一自动停机闸门：planner_run.go 每轮用
// RecoveryHandler.IsCircuitBreakerOpen()（即 !Allow()）决定是否放弃本轮。
// 它的状态机一旦写错，表现不是报错而是"永远不熔断"（LLM 全挂时继续烧 token）
// 或"过早熔断"（一次网络抖动就把整个 Director 锁死）。
//
// 这些用例原先散在 internal/agents/director/recovery_test.go 里，
// 于是本包的覆盖率显示 0%，而契约的归属也看不见。

func TestCircuitBreaker_InitialStateAllows(t *testing.T) {
	cb := NewCircuitBreaker(5, 30*time.Second)
	if !cb.Allow() {
		t.Error("Allow() = false on a fresh breaker, want true")
	}
	if got := cb.State(); got != "closed" {
		t.Errorf("State() = %q, want closed", got)
	}
}

// 阈值是"累计失败 >= threshold 才打开"，不是 >。写成 > 会让每次熔断都多失败一次。
func TestCircuitBreaker_OpensExactlyAtThreshold(t *testing.T) {
	cb := NewCircuitBreaker(3, 30*time.Second)

	for i := 1; i < 3; i++ {
		cb.Failure()
		if got := cb.State(); got != "closed" {
			t.Fatalf("after %d failures State() = %q, want closed", i, got)
		}
		if !cb.Allow() {
			t.Fatalf("after %d failures Allow() = false, want true", i)
		}
	}

	cb.Failure() // 第 3 次 == threshold
	if got := cb.State(); got != "open" {
		t.Fatalf("after %d failures State() = %q, want open", 3, got)
	}
	if cb.Allow() {
		t.Error("Allow() = true while open before resetTimeout, want false")
	}
}

// 打开后必须持续拒绝，直到 resetTimeout 到点。用 SetLastFailure 拨时钟而不是
// time.Sleep：睡 50ms 只测得到一种超时，把 resetTimeout 改小就直接假红。
func TestCircuitBreaker_StaysOpenUntilResetTimeoutElapses(t *testing.T) {
	const reset = 30 * time.Second
	cb := NewCircuitBreaker(1, reset)
	cb.Failure()

	cb.SetLastFailure(time.Now().Add(-reset + time.Millisecond))
	if cb.Allow() {
		t.Error("Allow() = true just before resetTimeout, want false")
	}
	if got := cb.State(); got != "open" {
		t.Errorf("State() = %q just before resetTimeout, want open", got)
	}

	cb.SetLastFailure(time.Now().Add(-reset - time.Millisecond))
	if !cb.Allow() {
		t.Error("Allow() = false after resetTimeout, want true (trial request)")
	}
	if got := cb.State(); got != "half-open" {
		t.Errorf("State() after resetTimeout = %q, want half-open", got)
	}
}

// Allow() 本身带状态迁移副作用：IsCircuitBreakerOpen() 就是 !Allow()，
// 所以"查询闸门"这个动作就是半开期的试探请求。迁移只能发生一次 ——
// 第二次 Allow() 必须停在 half-open，否则每次轮询都在重新试探。
func TestCircuitBreaker_OpenTransitionsToHalfOpenOnce(t *testing.T) {
	cb := NewCircuitBreaker(1, 10*time.Millisecond)
	cb.Failure()
	cb.SetLastFailure(time.Now().Add(-time.Second))

	if !cb.Allow() {
		t.Fatal("first Allow() after timeout = false, want true")
	}
	if got := cb.State(); got != "half-open" {
		t.Fatalf("State() = %q, want half-open", got)
	}
	if !cb.Allow() {
		t.Error("second Allow() in half-open = false, want true")
	}
	if got := cb.State(); got != "half-open" {
		t.Errorf("State() after a second Allow() = %q, want half-open", got)
	}
}

// 半开试探成功 → 闭合，并且 failures 归零：闭合后必须重新数满阈值才会再打开，
// 只改 state 不清计数的话下一次失败会立刻熔断。
func TestCircuitBreaker_HalfOpenTrialSuccessCloses(t *testing.T) {
	cb := NewCircuitBreaker(3, 30*time.Second)
	cb.Failure()
	cb.Failure()
	cb.Failure()
	cb.SetLastFailure(time.Now().Add(-60 * time.Second))
	cb.Allow()

	cb.Success()
	if got := cb.State(); got != "closed" {
		t.Fatalf("State() = %q after half-open success, want closed", got)
	}
	cb.Failure()
	cb.Failure()
	if got := cb.State(); got != "closed" {
		t.Errorf("State() = %q after 2 failures on a freshly closed breaker (threshold 3), want closed", got)
	}
	cb.Failure()
	if got := cb.State(); got != "open" {
		t.Errorf("State() = %q after a full new run of failures, want open", got)
	}
}

func TestCircuitBreaker_HalfOpenTrialFailureReopens(t *testing.T) {
	cb := NewCircuitBreaker(1, 30*time.Second)
	cb.Failure()
	cb.SetLastFailure(time.Now().Add(-60 * time.Second))
	cb.Allow()
	if got := cb.State(); got != "half-open" {
		t.Fatalf("State() = %q before the trial, want half-open", got)
	}

	cb.Failure()
	if got := cb.State(); got != "open" {
		t.Fatalf("State() = %q after half-open trial failed, want open", got)
	}
	// 重开后重新计时：这次失败把 lastFailure 推到当下。
	if cb.Allow() {
		t.Error("Allow() = true right after re-opening, want false")
	}
}

// open 期直接 Success() 也要能闭合。这条路径在生产里可达：
// 半开试探成功前，另一条并发委派链已先返回。
func TestCircuitBreaker_SuccessWhileOpenCloses(t *testing.T) {
	cb := NewCircuitBreaker(2, 30*time.Second)
	cb.Failure()
	cb.Failure()
	if got := cb.State(); got != "open" {
		t.Fatalf("State() = %q, want open", got)
	}

	cb.Success()
	if got := cb.State(); got != "closed" {
		t.Errorf("State() = %q after Success() while open, want closed", got)
	}
	if !cb.Allow() {
		t.Error("Allow() = false after recovery, want true")
	}
}

// 计数的语义是"连续失败"而不是"累计失败"：中间一次成功必须把计数清零，
// 否则一个长会话里零星失败攒够阈值就会永久熔断。
func TestCircuitBreaker_SuccessClearsConsecutiveCount(t *testing.T) {
	cb := NewCircuitBreaker(3, 30*time.Second)
	cb.Failure()
	cb.Failure()
	cb.Success()

	cb.Failure()
	cb.Failure()
	if got := cb.State(); got != "closed" {
		t.Errorf("State() = %q after 2 failures following a success, want closed", got)
	}
	cb.Failure()
	if got := cb.State(); got != "open" {
		t.Errorf("State() = %q after 3 fresh failures, want open", got)
	}
}

// Reset 是 Director 在新任务开始时走的复位路径。只改 state 不清 failures 的话，
// 下一次单点失败就会立刻熔断（阈值 3、failures 已是 2）。
func TestCircuitBreaker_ResetClearsStateAndFailures(t *testing.T) {
	cb := NewCircuitBreaker(3, 30*time.Second)
	cb.Failure()
	cb.Failure()
	cb.Failure()
	if got := cb.State(); got != "open" {
		t.Fatalf("State() = %q before Reset(), want open", got)
	}

	cb.Reset()
	if got := cb.State(); got != "closed" {
		t.Fatalf("State() = %q after Reset(), want closed", got)
	}
	if !cb.Allow() {
		t.Error("Allow() = false after Reset(), want true")
	}
	cb.Failure()
	if got := cb.State(); got != "closed" {
		t.Errorf("State() = %q after Reset() + 1 failure with threshold 3, want closed", got)
	}
}

// half-open 不做单探针限流：闸门放开期间所有请求都能通过。
// 这是当前实现的事实行为（Allow() 的 half-open 分支无条件返回 true）。
// 若将来改成"只放一个探针"，本用例必须连同注释一起改写，而不是被静默删掉。
func TestCircuitBreaker_HalfOpenAdmitsEveryRequest(t *testing.T) {
	cb := NewCircuitBreaker(1, 30*time.Second)
	cb.Failure()
	cb.SetLastFailure(time.Now().Add(-60 * time.Second))

	for i := 0; i < 8; i++ {
		if !cb.Allow() {
			t.Fatalf("Allow() = false on trial %d of a half-open breaker, want true", i+1)
		}
	}
	if got := cb.State(); got != "half-open" {
		t.Errorf("State() = %q, want half-open", got)
	}
}

// 并发安全：Director 的主循环与委派回调都可能在写。不带 -race 时这个用例主要
// 抓死锁与状态错乱（例如某次改动把 mu 挪出 Allow()，half-open 迁移会丢更新）。
func TestCircuitBreaker_ConcurrentCallsKeepStateCoherent(t *testing.T) {
	cb := NewCircuitBreaker(4, time.Hour)

	const workers = 16
	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				switch (i + j) % 3 {
				case 0:
					cb.Failure()
				case 1:
					cb.Success()
				case 2:
					cb.Allow()
				}
				_ = cb.State()
			}
		}(i)
	}
	wg.Wait()

	switch got := cb.State(); got {
	case "closed", "open", "half-open":
	default:
		t.Errorf("State() = %q after concurrent churn, want one of closed/open/half-open", got)
	}
	// 阈值 4、期间有大量 Failure()，终态只可能是 open 或刚被 Success() 拉回 closed。
	if got := cb.State(); got == "half-open" {
		t.Errorf("State() = half-open with resetTimeout=1h, unreachable: nothing waited an hour")
	}
}
