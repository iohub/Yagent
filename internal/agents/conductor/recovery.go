package conductor

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// RecoveryConfig 恢复配置。
type RecoveryConfig struct {
	MaxRetries                 int           // 步骤重试次数
	CircuitBreakerThreshold    int           // 熔断阈值，0=不启用
	CircuitBreakerResetTimeout time.Duration // 熔断恢复时间
}

// DefaultRecoveryConfig 返回默认恢复配置。
func DefaultRecoveryConfig() RecoveryConfig {
	return RecoveryConfig{
		MaxRetries:                 3,
		CircuitBreakerThreshold:    5,
		CircuitBreakerResetTimeout: 30 * time.Second,
	}
}

// CircuitBreaker 熔断器，防止连续失败耗尽资源。
type CircuitBreaker struct {
	state        string
	failures     int
	threshold    int
	resetTimeout time.Duration
	lastFailure  time.Time
	mu           sync.Mutex
}

// NewCircuitBreaker 创建熔断器。
func NewCircuitBreaker(threshold int, resetTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		state:        "closed",
		threshold:    threshold,
		resetTimeout: resetTimeout,
	}
}

// Allow 检查是否允许执行请求。
func (cb *CircuitBreaker) Allow() bool {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case "closed":
		return true
	case "open":
		if time.Since(cb.lastFailure) > cb.resetTimeout {
			cb.state = "half-open"
			slog.Info("[Conductor] Circuit breaker half-open, allowing trial request")
			return true
		}
		return false
	case "half-open":
		return true
	default:
		return true
	}
}

// Success 记录成功，关闭熔断器。
func (cb *CircuitBreaker) Success() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	switch cb.state {
	case "half-open":
		cb.state = "closed"
		cb.failures = 0
		slog.Info("[Conductor] Circuit breaker closed (recovered)")
	case "open":
		cb.state = "closed"
		cb.failures = 0
		slog.Info("[Conductor] Circuit breaker closed (recovered from open)")
	case "closed":
		cb.failures = 0
	}
}

// Failure 记录失败，可能打开熔断器。
func (cb *CircuitBreaker) Failure() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.failures++
	cb.lastFailure = time.Now()

	switch cb.state {
	case "closed":
		if cb.failures >= cb.threshold {
			cb.state = "open"
			slog.Warn("[Conductor] Circuit breaker opened", "failures", cb.failures, "threshold", cb.threshold)
		}
	case "half-open":
		cb.state = "open"
		slog.Warn("[Conductor] Circuit breaker re-opened (half-open trial failed)")
	}
}

// State 返回当前状态。
func (cb *CircuitBreaker) State() string {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// Reset 重置熔断器到初始状态。
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.state = "closed"
	cb.failures = 0
}

// RecoveryHandler 管理错误恢复与重试策略。
type RecoveryHandler struct {
	config       RecoveryConfig
	breaker      *CircuitBreaker
	stepFailures map[string]int // stepID → failure count
	mu           sync.Mutex
}

// NewRecoveryHandler 创建恢复管理器。
func NewRecoveryHandler(cfg RecoveryConfig) *RecoveryHandler {
	var breaker *CircuitBreaker
	if cfg.CircuitBreakerThreshold > 0 {
		breaker = NewCircuitBreaker(cfg.CircuitBreakerThreshold, cfg.CircuitBreakerResetTimeout)
	}

	return &RecoveryHandler{
		config:       cfg,
		breaker:      breaker,
		stepFailures: make(map[string]int),
	}
}

// RecordLLMSuccess 记录 LLM 调用成功。
func (r *RecoveryHandler) RecordLLMSuccess() {
	if r.breaker != nil {
		r.breaker.Success()
	}
}

// RecordLLMFailure 记录 LLM 调用失败。
func (r *RecoveryHandler) RecordLLMFailure() {
	if r.breaker != nil {
		r.breaker.Failure()
	}
}

// IsCircuitBreakerOpen 检查熔断器是否打开。
func (r *RecoveryHandler) IsCircuitBreakerOpen() bool {
	if r.breaker == nil {
		return false
	}
	return !r.breaker.Allow()
}

// ComputeBackoff 计算指数退避延迟。
// attempt 从 0 开始，延迟为 2^attempt 秒，上限 30 秒。
func ComputeBackoff(attempt int) time.Duration {
	wait := time.Duration(1<<uint(attempt)) * time.Second
	if wait > 30*time.Second {
		wait = 30 * time.Second
	}
	return wait
}

// RetryWithBackoff 执行带指数退避的重试。
// fn 是重试的函数，maxRetries 是最大重试次数。
// 返回 (成功标志, 最终错误)。
func RetryWithBackoff(ctx context.Context, maxRetries int, fn func(attempt int) error) error {
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			wait := ComputeBackoff(attempt - 1)
			slog.Warn("[Conductor] Retrying operation", "attempt", attempt, "max_retries", maxRetries, "wait", wait)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(wait):
			}
		}

		lastErr = fn(attempt)
		if lastErr == nil {
			return nil
		}

		slog.Warn("[Conductor] Operation failed", "attempt", attempt, "error", lastErr)
	}

	return fmt.Errorf("operation failed after %d retries: %w", maxRetries, lastErr)
}

// RecordStepFailure 记录步骤失败，返回是否超过最大重试次数。
func (r *RecoveryHandler) RecordStepFailure(stepID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.stepFailures[stepID]++
	return r.stepFailures[stepID] > r.config.MaxRetries
}

// RecordStepSuccess 清除步骤失败记录。
func (r *RecoveryHandler) RecordStepSuccess(stepID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.stepFailures, stepID)
}

// GetFailureStats 获取失败统计。
func (r *RecoveryHandler) GetFailureStats() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()

	stats := make(map[string]int)
	for k, v := range r.stepFailures {
		stats[k] = v
	}
	return stats
}

// Reset 重置恢复管理器状态（步骤失败计数 + 熔断器）
func (r *RecoveryHandler) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for k := range r.stepFailures {
		delete(r.stepFailures, k)
	}
	if r.breaker != nil {
		r.breaker.Reset()
	}
}

// IsLLMFailureTransient 判断 LLM 错误是否为可重试的瞬态错误。
func IsLLMFailureTransient(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	transientPatterns := []string{
		"timeout",
		"rate limit",
		"too many requests",
		"temporarily unavailable",
		"server error",
		"connection reset",
		"deadline exceeded",
	}
	for _, pattern := range transientPatterns {
		if contains(errStr, pattern) {
			return true
		}
	}
	return false
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchSubstring(s, substr)
}

func searchSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
