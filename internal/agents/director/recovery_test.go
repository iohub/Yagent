package director

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"yagent/internal/recovery"
)

func TestCircuitBreaker_InitialState(t *testing.T) {
	cb := recovery.NewCircuitBreaker(5, 30*time.Second)
	if !cb.Allow() {
		t.Error("new circuit breaker should allow requests")
	}
	if cb.State() != "closed" {
		t.Errorf("state = %q, want 'closed'", cb.State())
	}
}

func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	cb := recovery.NewCircuitBreaker(3, 30*time.Second)

	// Fail 2 times - should still be closed
	cb.Failure()
	cb.Failure()
	if cb.State() != "closed" {
		t.Errorf("after 2 failures: state = %q, want 'closed'", cb.State())
	}

	// Fail 3rd time - should open
	cb.Failure()
	if cb.State() != "open" {
		t.Errorf("after 3 failures: state = %q, want 'open'", cb.State())
	}

	// Should not allow
	if cb.Allow() {
		t.Error("circuit breaker should not allow when open")
	}
}

func TestCircuitBreaker_ResetsAfterTimeout(t *testing.T) {
	cb := recovery.NewCircuitBreaker(1, 50*time.Millisecond)

	cb.Failure()
	if cb.State() != "open" {
		t.Errorf("after 1 failure: state = %q, want 'open'", cb.State())
	}

	// Wait for reset
	time.Sleep(60 * time.Millisecond)

	if !cb.Allow() {
		t.Error("circuit breaker should allow after reset timeout")
	}
	if cb.State() != "half-open" {
		t.Errorf("after timeout: state = %q, want 'half-open'", cb.State())
	}
}

func TestCircuitBreaker_ClosesOnSuccess(t *testing.T) {
	cb := recovery.NewCircuitBreaker(1, 30*time.Second)

	cb.Failure()
	if cb.State() != "open" {
		t.Errorf("state = %q, want 'open'", cb.State())
	}

	// Force half-open
	cb.SetLastFailure(time.Now().Add(-60 * time.Second))
	cb.Allow() // transitions to half-open
	if cb.State() != "half-open" {
		t.Errorf("state = %q, want 'half-open'", cb.State())
	}

	cb.Success()
	if cb.State() != "closed" {
		t.Errorf("state = %q, want 'closed'", cb.State())
	}
}

func TestCircuitBreaker_ReOpensOnHalfOpenFailure(t *testing.T) {
	cb := recovery.NewCircuitBreaker(1, 30*time.Second)

	cb.Failure()
	// Force half-open
	cb.SetLastFailure(time.Now().Add(-60 * time.Second))
	cb.Allow() // transitions to half-open
	if cb.State() != "half-open" {
		t.Errorf("state = %q, want 'half-open'", cb.State())
	}

	cb.Failure()
	if cb.State() != "open" {
		t.Errorf("state = %q, want 'open' after half-open failure", cb.State())
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
