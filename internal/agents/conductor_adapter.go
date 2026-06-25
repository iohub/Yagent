package agents

import (
	"context"
	"sync"
	"time"

	conductor "codeactor/internal/agents/conductor"
)

// ConductorAdapter 桥接旧 facade (conductor.go) 与新模块化组件 (conductor/ 子目录)
// 实现 Strangler Fig 模式的第一步过渡：Metrics 收集和 CircuitBreaker 逻辑迁移。
//
// 旧代码在 conductor.go 中内联实现了熔断器和 Metrics 收集，
// 新代码在 conductor/ 子目录中提供了模块化组件。
// 本适配器将旧代码中的内联逻辑委托到新组件，保持向后兼容。
//
// 使用方式：
//
//	adapter := NewConductorAdapter(true, conductor.DefaultRecoveryConfig())
//	adapter.RecordTaskStart()
//	if adapter.IsCircuitBreakerOpen() {
//	    // 熔断器打开，拒绝请求
//	}
type ConductorAdapter struct {
	metrics  *conductor.MetricsCollector
	recovery *conductor.RecoveryHandler
	mu       sync.RWMutex
}

// NewConductorAdapter 创建适配器。
//
// Parameters:
//   - enabled: 是否启用 Metrics 收集（true 时记录所有指标）
//   - cfg: 恢复配置（熔断阈值、超时、重试次数等）
func NewConductorAdapter(enabled bool, cfg conductor.RecoveryConfig) *ConductorAdapter {
	return &ConductorAdapter{
		metrics:  conductor.NewMetricsCollector(enabled),
		recovery: conductor.NewRecoveryHandler(cfg),
	}
}

// ============================================================
// Metrics 适配方法
// ============================================================

// RecordTaskStart 记录任务开始
func (a *ConductorAdapter) RecordTaskStart() {
	a.metrics.RecordTaskStart()
}

// RecordToolCall 记录工具调用及耗时
func (a *ConductorAdapter) RecordToolCall(toolName string, duration time.Duration) {
	a.metrics.RecordToolCall(toolName, duration)
}

// RecordError 记录错误
func (a *ConductorAdapter) RecordError(source string) {
	a.metrics.RecordError(source)
}

// RecordLLMDuration 记录 LLM 调用耗时
func (a *ConductorAdapter) RecordLLMDuration(d time.Duration) {
	a.metrics.RecordLLMDuration(d)
}

// ============================================================
// CircuitBreaker / Recovery 适配方法
// ============================================================

// RecordLLMSuccess 记录 LLM 调用成功（重置熔断器状态）
func (a *ConductorAdapter) RecordLLMSuccess() {
	a.recovery.RecordLLMSuccess()
}

// RecordLLMFailure 记录 LLM 调用失败，可能触发熔断器打开
func (a *ConductorAdapter) RecordLLMFailure() {
	a.recovery.RecordLLMFailure()
}

// IsCircuitBreakerOpen 检查熔断器是否打开（拒绝请求）
func (a *ConductorAdapter) IsCircuitBreakerOpen() bool {
	return a.recovery.IsCircuitBreakerOpen()
}

// RecordStepFailure 记录步骤失败，返回是否超过最大重试次数
func (a *ConductorAdapter) RecordStepFailure(stepID string) bool {
	return a.recovery.RecordStepFailure(stepID)
}

// RecordStepSuccess 清除步骤失败记录
func (a *ConductorAdapter) RecordStepSuccess(stepID string) {
	a.recovery.RecordStepSuccess(stepID)
}

// ComputeBackoff 计算指数退避延迟（委托到新组件）
// attempt 从 0 开始，延迟为 2^attempt 秒，上限 30 秒。
func (a *ConductorAdapter) ComputeBackoff(attempt int) time.Duration {
	return conductor.ComputeBackoff(attempt)
}

// RetryWithBackoff 执行带指数退避的重试（委托到新组件）
// fn 是重试的函数，maxRetries 是最大重试次数。
func (a *ConductorAdapter) RetryWithBackoff(ctx context.Context, maxRetries int, fn func(attempt int) error) error {
	return conductor.RetryWithBackoff(ctx, maxRetries, fn)
}

// ============================================================
// 辅助方法（用于迁移过程中的过渡）
// ============================================================

// Snapshot 获取 Metrics 快照（仅当 adapter 存在时可用）
func (a *ConductorAdapter) Snapshot() conductor.MetricsSnapshot {
	return a.metrics.Snapshot()
}

// Reset 重置所有状态（Metrics + CircuitBreaker）
func (a *ConductorAdapter) Reset() {
	a.metrics.Reset()
	a.recovery.Reset()
}
