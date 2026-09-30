package director

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"yagent/internal/recovery"
)

// RecoveryConfig 恢复配置。
type RecoveryConfig struct {
	MaxRetries                 int           // 步骤重试次数
	LLMRetries                 int           // LLM 调用步骤级重试次数（收编自 DirectorAgent.stepRetries，源自 config.LLM.StepRetries）
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

// RecoveryHandler 管理错误恢复与重试策略。
type RecoveryHandler struct {
	config       RecoveryConfig
	breaker      *recovery.CircuitBreaker
	stepFailures map[string]int // stepID → failure count

	// LLM 兜底机制状态（P0-1 Phase 2b：收编自 DirectorAgent 内联字段，lift-and-shift）
	llmRetries             int       // LLM 调用步骤级重试次数（源自 config.LLM.StepRetries）
	consecutiveLLMFailures int       // 连续 LLM 调用失败计数（成功时不重置，保持原语义）
	lastLLMFailureTime     time.Time // 最近一次 LLM 失败时间

	mu sync.Mutex
}

// NewRecoveryHandler 创建恢复管理器。
func NewRecoveryHandler(cfg RecoveryConfig) *RecoveryHandler {
	var breaker *recovery.CircuitBreaker
	if cfg.CircuitBreakerThreshold > 0 {
		breaker = recovery.NewCircuitBreaker(cfg.CircuitBreakerThreshold, cfg.CircuitBreakerResetTimeout)
	}

	return &RecoveryHandler{
		config:       cfg,
		breaker:      breaker,
		stepFailures: make(map[string]int),
		llmRetries:   cfg.LLMRetries,
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

// LLMRetries 返回 LLM 调用步骤级重试次数。
func (r *RecoveryHandler) LLMRetries() int {
	return r.llmRetries
}

// RecordLLMFailureStats 记录 LLM 失败统计：递增连续失败计数并更新最近失败时间。
// lift-and-shift 自 DirectorAgent 内联语句（consecutiveLLMFailures++ /
// lastLLMFailureTime = time.Now()），语义逐字保持：
// 成功路径（RecordLLMSuccess）不重置该计数——原实现中它在 DirectorAgent
// 生命周期内单调递增，仅用于日志展示。
func (r *RecoveryHandler) RecordLLMFailureStats() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.consecutiveLLMFailures++
	r.lastLLMFailureTime = time.Now()
}

// ConsecutiveLLMFailures 返回连续 LLM 调用失败计数。
func (r *RecoveryHandler) ConsecutiveLLMFailures() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.consecutiveLLMFailures
}

// LastLLMFailureTime 返回最近一次 LLM 失败时间。
func (r *RecoveryHandler) LastLLMFailureTime() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastLLMFailureTime
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
			slog.Warn("[Director] Retrying operation", "attempt", attempt, "max_retries", maxRetries, "wait", wait)
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

		slog.Warn("[Director] Operation failed", "attempt", attempt, "error", lastErr)
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
