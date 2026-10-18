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
	// probeAt 是 half-open 探针放出去的时刻；zero = 没人欠着回报。
	// 只放一个探针的理由（§9）：provider 刚恢复时，"所有在飞请求一起冲进去"
	// 会把刚回来的一方再打回去，而其中任何一个失败都会立刻重开熔断。
	probeAt time.Time
	mu      sync.Mutex
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
			cb.probeAt = time.Now()
			slog.Info("Circuit breaker half-open, allowing one trial request")
			return true
		}
		return false
	case "half-open":
		// 单探针：第一个越过 open→half-open 的调用者拿到试探权，其余一律拒绝，
		// 直到那次试探 Success/Failure 回报（probeAt 被清零）为止。
		// 探针没回来（调用方死在半路）时不能永远锁死，所以过了 resetTimeout
		// 之后重新允许一次试探 —— 最坏是多等一个 reset 周期，而不是永久拒绝。
		if cb.probeAt.IsZero() {
			cb.probeAt = time.Now()
			return true
		}
		if time.Since(cb.probeAt) > cb.resetTimeout {
			cb.probeAt = time.Now()
			slog.Info("Circuit breaker half-open 探针超时未回报，重新放行一次试探")
			return true
		}
		return false
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
		// 试探已有结论，探针额度立刻还给下一个调用者。
		cb.probeAt = time.Time{}
		slog.Info("Circuit breaker closed (recovered)")
	case "open":
		cb.state = "closed"
		cb.failures = 0
		cb.probeAt = time.Time{}
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
		cb.probeAt = time.Time{}
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
	cb.probeAt = time.Time{}
}

// SetLastFailure 设置上次失败时间（用于测试）。
func (cb *CircuitBreaker) SetLastFailure(t time.Time) {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.lastFailure = t
}
