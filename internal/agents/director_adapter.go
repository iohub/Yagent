package agents

import (
	"time"

	director "codeactor/internal/agents/director"
)

// DirectorAdapter 桥接旧 facade (director.go) 与新模块化组件 (director/ 子目录)
// 实现 Strangler Fig 模式的第一步过渡：Metrics 收集和 CircuitBreaker 逻辑迁移。
//
// 旧代码在 director.go 中内联实现了熔断器和 Metrics 收集，
// 新代码在 director/ 子目录中提供了模块化组件。
// 本适配器将旧代码中的内联逻辑委托到新组件，保持向后兼容。
type DirectorAdapter struct {
	metrics  *director.MetricsCollector
	recovery *director.RecoveryHandler
}

// NewDirectorAdapter 创建适配器。
//
// Parameters:
//   - enabled: 是否启用 Metrics 收集（true 时记录所有指标）
//   - cfg: 恢复配置（熔断阈值、超时、重试次数等）
func NewDirectorAdapter(enabled bool, cfg director.RecoveryConfig) *DirectorAdapter {
	return &DirectorAdapter{
		metrics:  director.NewMetricsCollector(enabled),
		recovery: director.NewRecoveryHandler(cfg),
	}
}

// RecordLLMDuration 记录 LLM 调用耗时
func (a *DirectorAdapter) RecordLLMDuration(d time.Duration) {
	a.metrics.RecordLLMDuration(d)
}

// RecordLLMSuccess 记录 LLM 调用成功（重置熔断器状态）
func (a *DirectorAdapter) RecordLLMSuccess() {
	a.recovery.RecordLLMSuccess()
}

// RecordLLMFailure 记录 LLM 调用失败，可能触发熔断器打开
func (a *DirectorAdapter) RecordLLMFailure() {
	a.recovery.RecordLLMFailure()
}

// IsCircuitBreakerOpen 检查熔断器是否打开（拒绝请求）
func (a *DirectorAdapter) IsCircuitBreakerOpen() bool {
	return a.recovery.IsCircuitBreakerOpen()
}
