package director

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// 熔断器本身的状态机契约在 internal/recovery/circuit_breaker_test.go。
// 这里只测 RecoveryHandler 如何使唤它 —— 也就是 Director 实际依赖的闸门语义。

// IsCircuitBreakerOpen() == !Allow()，而 Allow() 在 open 期过了 resetTimeout 会
// 直接把状态迁移到 half-open 并放行。所以"查询闸门"这个动作本身就是半开试探：
// 打开时查到 true，窗口过后查到 false，且试探成功后必须真的闭合。
func TestRecoveryHandler_IsOpenQueryIsTheTrialRequest(t *testing.T) {
	// 窗口取 20ms：足够让下面那次"刚打开"的查询确定性地返回 true，
	// 又能在用例内的轮询里等到半开放行。取 1ms 会让首个断言取决于调度。
	rh := NewRecoveryHandler(RecoveryConfig{
		MaxRetries:                 3,
		CircuitBreakerThreshold:    2,
		CircuitBreakerResetTimeout: 20 * time.Millisecond,
	})

	rh.RecordLLMFailure()
	rh.RecordLLMFailure()
	if !rh.IsCircuitBreakerOpen() {
		t.Fatal("IsCircuitBreakerOpen() = false right after hitting the threshold, want true")
	}

	deadline := time.Now().Add(200 * time.Millisecond)
	granted := false
	for time.Now().Before(deadline) {
		if !rh.IsCircuitBreakerOpen() {
			granted = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !granted {
		t.Error("IsCircuitBreakerOpen() stayed true past resetTimeout, want false (trial allowed)")
	}
	// 这一次查询已经把半开期唯一试探权拿走了：在有人回报之前，第二次查询必须还在
	// 闸门外面。生产里 Director 是"查询→同一步调用→回报"，所以这条不会挡住真实调用；
	// 它挡的是"查询本身就是试探"这一含义被写宽 —— 那样 half-open 会对每个调用者放行。
	if !rh.IsCircuitBreakerOpen() {
		t.Error("第二次查询又拿到一次试探权 —— 单探针失效，惊群回来了")
	}
	// 试探成功后必须真的闭合；否则下一轮又在半开与打开之间抖。
	rh.RecordLLMSuccess()
	if rh.IsCircuitBreakerOpen() {
		t.Error("IsCircuitBreakerOpen() = true after a successful trial, want false")
	}
}

// circuit_breaker_threshold 的默认值是 0（config/defaults.go 与仓库里的
// config.toml 都是 0），此时不该创建熔断器：Director 绝不能因为网络抖动停摆。
// 把 `> 0` 写成 `>= 0` 会让默认配置下也建出 threshold=0 的熔断器 —— 一次失败就
// failures >= 0 成立，永久拒绝服务。
func TestRecoveryHandler_BreakerDisabledAtThresholdZero(t *testing.T) {
	rh := NewRecoveryHandler(RecoveryConfig{MaxRetries: 3, CircuitBreakerThreshold: 0})
	// "不启用"的实现是不创建熔断器，而不是创建一个空转的。这个区别有可观测后果：
	// threshold=0 时任何一次 Failure 都满足 failures >= 0，而 resetTimeout=0 又让
	// Allow() 立刻放行 —— 于是计数在暗处一路增长，日志与 metrics 全错。
	if rh.breaker != nil {
		t.Error("breaker was constructed with threshold 0, want nil (disabled)")
	}

	for i := 0; i < 50; i++ {
		rh.RecordLLMFailure()
	}
	if rh.IsCircuitBreakerOpen() {
		t.Error("IsCircuitBreakerOpen() = true with threshold 0, want false (disabled)")
	}
	// 负数同样不该启用（配置写错时宁可少熔断，也不要锁死 Director）。
	neg := NewRecoveryHandler(RecoveryConfig{MaxRetries: 3, CircuitBreakerThreshold: -1})
	if neg.breaker != nil {
		t.Error("breaker was constructed with a negative threshold, want nil")
	}
	for i := 0; i < 5; i++ {
		neg.RecordLLMFailure()
	}
	if neg.IsCircuitBreakerOpen() {
		t.Error("IsCircuitBreakerOpen() = true with a negative threshold, want false")
	}
	// 对照组：阈值 1 必须真的建出熔断器，否则上面的 nil 断言在"永不创建"的实现下也通过。
	// resetTimeout 必须显式给非零值：留 0 的话 open 立刻被 Allow() 判成过期，断言取决于时钟。
	on := NewRecoveryHandler(RecoveryConfig{
		MaxRetries:                 3,
		CircuitBreakerThreshold:    1,
		CircuitBreakerResetTimeout: 30 * time.Second,
	})
	if on.breaker == nil {
		t.Fatal("breaker is nil with threshold 1, want a constructed breaker")
	}
	on.RecordLLMFailure()
	if !on.IsCircuitBreakerOpen() {
		t.Error("IsCircuitBreakerOpen() = false after reaching threshold 1, want true")
	}
}

// RecordLLMFailureStats / ConsecutiveLLMFailures 是 lift-and-shift 的日志计数，
// 刻意与熔断器计数分离：成功不重置它（原 DirectorAgent 字段语义）。
// "顺手修成"真正的连续计数会改掉日志含义，所以钉住现状。
func TestRecoveryHandler_FailureStatsNotResetBySuccess(t *testing.T) {
	rh := NewRecoveryHandler(DefaultRecoveryConfig())
	if !rh.LastLLMFailureTime().IsZero() {
		t.Error("LastLLMFailureTime() should be zero before any failure")
	}

	rh.RecordLLMFailureStats()
	rh.RecordLLMFailureStats()
	rh.RecordLLMSuccess()

	if got := rh.ConsecutiveLLMFailures(); got != 2 {
		t.Errorf("ConsecutiveLLMFailures() = %d after 2 failures + 1 success, want 2", got)
	}
	if rh.LastLLMFailureTime().IsZero() {
		t.Error("LastLLMFailureTime() = zero after RecordLLMFailureStats(), want a timestamp")
	}
}

func TestRecoveryHandler_LLMFailureTracking(t *testing.T) {
	cfg := RecoveryConfig{
		MaxRetries:                 3,
		CircuitBreakerThreshold:    3,
		CircuitBreakerResetTimeout: 30 * time.Second,
	}
	rh := NewRecoveryHandler(cfg)

	if rh.IsCircuitBreakerOpen() {
		t.Error("circuit breaker should not be open initially")
	}

	rh.RecordLLMFailure()
	rh.RecordLLMFailure()
	if rh.IsCircuitBreakerOpen() {
		t.Error("circuit breaker should not be open after 2 failures")
	}

	rh.RecordLLMFailure()
	if !rh.IsCircuitBreakerOpen() {
		t.Error("circuit breaker should be open after 3 failures")
	}

	rh.RecordLLMSuccess()
	if rh.IsCircuitBreakerOpen() {
		t.Error("circuit breaker should recover after success")
	}
}

func TestRecoveryHandler_StepFailureTracking(t *testing.T) {
	cfg := DefaultRecoveryConfig()
	cfg.MaxRetries = 2
	rh := NewRecoveryHandler(cfg)

	if rh.RecordStepFailure("step_1") {
		t.Error("first failure should not exceed max retries")
	}
	if rh.RecordStepFailure("step_1") {
		t.Error("second failure should not exceed max retries")
	}
	if !rh.RecordStepFailure("step_1") {
		t.Error("third failure should exceed max retries (2)")
	}

	// Different step should not be affected
	if rh.RecordStepFailure("step_2") {
		t.Error("first failure of step_2 should not exceed max retries")
	}

	// Record success should clear failures
	rh.RecordStepSuccess("step_1")
	if rh.RecordStepFailure("step_1") {
		t.Error("after success, first failure should not exceed max retries")
	}
}

// ComputeBackoff 的精确序列由 director_characterization_test.go 的
// TestCharacterizationComputeBackoff 逐值钉住；首次成功 / maxRetries=0 /
// ctx 取消三条语义由同文件的 TestCharacterizationRetryWithBackoff 覆盖。
// 这里只补它没覆盖的两条：重试到达上限的次数，与耗尽后返回最后一次错误。
//
// maxRetries 刻意取 1：RetryWithBackoff 直接 sleep 生产的指数退避（1s、2s…），
// 没有注入点，多一次重试就要多等 2s。
func TestRetryWithBackoff_EventualSuccess(t *testing.T) {
	attempts := 0
	err := RetryWithBackoff(context.Background(), 1, func(attempt int) error {
		attempts++
		if attempt < 1 {
			return errors.New("transient error")
		}
		return nil
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
}

func TestRetryWithBackoff_AllFail(t *testing.T) {
	attempts := 0
	err := RetryWithBackoff(context.Background(), 1, func(attempt int) error {
		attempts++
		return errors.New("persistent error")
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "persistent error") {
		t.Errorf("err = %v, want the last attempt's error", err)
	}
	if attempts != 2 { // 0, 1 = 2 attempts total (maxRetries=1)
		t.Errorf("attempts = %d, want 2", attempts)
	}
}

func TestIsLLMFailureTransient(t *testing.T) {
	tests := []struct {
		err    error
		expect bool
	}{
		{errors.New("timeout error"), true},
		{errors.New("rate limit exceeded"), true},
		{errors.New("too many requests"), true},
		{errors.New("temporarily unavailable"), true},
		{errors.New("server error"), true},
		{errors.New("connection reset by peer"), true},
		{errors.New("deadline exceeded"), true},
		{errors.New("invalid API key"), false},
		{errors.New("bad request"), false},
		{nil, false},
	}

	for _, tc := range tests {
		got := IsLLMFailureTransient(tc.err)
		if got != tc.expect {
			t.Errorf("IsLLMFailureTransient(%v) = %v, want %v", tc.err, got, tc.expect)
		}
	}
}

func TestGetFailureStats(t *testing.T) {
	rh := NewRecoveryHandler(DefaultRecoveryConfig())

	rh.RecordStepFailure("step_a")
	rh.RecordStepFailure("step_a")
	rh.RecordStepFailure("step_b")

	stats := rh.GetFailureStats()
	if stats["step_a"] != 2 {
		t.Errorf("stats[step_a] = %d, want 2", stats["step_a"])
	}
	if stats["step_b"] != 1 {
		t.Errorf("stats[step_b] = %d, want 1", stats["step_b"])
	}
}

// Reset 走的是 Director 新任务开始时的复位路径：步骤计数与熔断器都要归零。
func TestRecoveryHandler_ResetClearsBothCounters(t *testing.T) {
	cfg := DefaultRecoveryConfig()
	cfg.MaxRetries = 1
	rh := NewRecoveryHandler(cfg)

	rh.RecordStepFailure("step_a")
	if !rh.RecordStepFailure("step_a") {
		t.Fatal("step_a should have exceeded MaxRetries=1 before Reset()")
	}
	for i := 0; i < cfg.CircuitBreakerThreshold; i++ {
		rh.RecordLLMFailure()
	}
	if !rh.IsCircuitBreakerOpen() {
		t.Fatal("breaker should be open before Reset()")
	}

	rh.Reset()

	if rh.IsCircuitBreakerOpen() {
		t.Error("IsCircuitBreakerOpen() = true after Reset(), want false")
	}
	if rh.RecordStepFailure("step_a") {
		t.Error("RecordStepFailure after Reset() should start from 1, not carry over")
	}
	if got := rh.GetFailureStats()["step_a"]; got != 1 {
		t.Errorf("GetFailureStats()[step_a] = %d after Reset()+1 failure, want 1", got)
	}
}
