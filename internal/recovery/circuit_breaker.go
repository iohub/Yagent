package recovery

import (
	"log/slog"
	"sync"
	"time"
)

// CircuitBreaker 熔断器，防止连续失败耗尽资源。
type CircuitBreaker struct {
	state        string // "closed", "open", "half-open"
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
			slog.Info("Circuit breaker half-open, allowing trial request")
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
		slog.Info("Circuit breaker closed (recovered)")
	case "open":
		cb.state = "closed"
		cb.failures = 0
		slog.Info("Circuit breaker closed (recovered from open)")
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
			slog.Warn("Circuit breaker opened", "failures", cb.failures, "threshold", cb.threshold)
		}
	case "half-open":
		cb.state = "open"
		slog.Warn("Circuit breaker re-opened (half-open trial failed)")
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

// SetLastFailure 设置上次失败时间（用于测试）。
func (cb *CircuitBreaker) SetLastFailure(t time.Time) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.lastFailure = t
}
