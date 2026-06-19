package compact

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"codeactor/internal/llm"
)

// ─────────────────────────────────────────────────────────
// DegradationTier 降级层级
// ─────────────────────────────────────────────────────────

// DegradationTier 定义压缩降级层级
// 数值越大，降级程度越深
type DegradationTier int

const (
	// TierFull 正常 LLM 摘要（最优质量）
	TierFull DegradationTier = iota // = 0

	// TierCache 使用缓存摘要（上次成功的结果，如果可用）
	TierCache // = 1

	// TierTruncated 截断式降级：保留首尾消息，丢弃中间
	// 不使用 LLM，仅基于优先级规则压缩
	TierTruncated // = 2

	// TierRefused 拒绝压缩：返回原始消息，标记降级
	TierRefused // = 3
)

// String 返回降级层级的可读名称
func (t DegradationTier) String() string {
	switch t {
	case TierFull:
		return "full"
	case TierCache:
		return "cache"
	case TierTruncated:
		return "truncated"
	case TierRefused:
		return "refused"
	default:
		return fmt.Sprintf("unknown(%d)", t)
	}
}

// IsFallback 检查是否为降级/回退模式
func (t DegradationTier) IsFallback() bool {
	return t >= TierCache
}

// IsOperational 检查操作是否成功（即使是降级模式）
func (t DegradationTier) IsOperational() bool {
	return t <= TierTruncated
}

// ─────────────────────────────────────────────────────────
// CircuitBreaker 熔断器
// ─────────────────────────────────────────────────────────

// CircuitBreakerState 熔断器状态
type CircuitBreakerState int32 // 使用 int32 保证 atomic 操作

const (
	StateClosed   CircuitBreakerState = iota // 正常：允许请求
	StateOpen                                // 打开：拒绝请求
	StateHalfOpen                            // 半开：允许探测请求
)

// String 返回熔断器状态的可读名称
func (s CircuitBreakerState) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// CircuitBreakerConfig 熔断器配置
type CircuitBreakerConfig struct {
	// FailureThreshold 连续失败次数阈值（默认 5）
	FailureThreshold int

	// ResetTimeout 熔断器打开后自动切换到 half-open 的超时时间（默认 30s）
	ResetTimeout time.Duration

	// HalfOpenMaxRequests half-open 状态下允许的最大探测请求数（默认 1）
	HalfOpenMaxRequests int

	// OnStateChange 状态变化回调（可选，用于监控）
	OnStateChange func(oldState, newState CircuitBreakerState)
}

// DefaultCircuitBreakerConfig 默认熔断器配置
var DefaultCircuitBreakerConfig = CircuitBreakerConfig{
	FailureThreshold:    5,
	ResetTimeout:        30 * time.Second,
	HalfOpenMaxRequests: 1,
	OnStateChange:       nil,
}

// CircuitBreaker LLM 调用熔断器
type CircuitBreaker struct {
	config CircuitBreakerConfig

	// state 熔断器状态（atomic 读写）
	state atomic.Int32

	// failureCount 连续失败次数（atomic）
	failureCount atomic.Int32

	// lastFailureTime 最近一次失败的时间
	lastFailureTime atomic.Value // time.Time

	// halfOpenRequests half-open 状态下的已处理探测请求数
	halfOpenRequests atomic.Int32

	mu sync.Mutex // 保护状态切换
}

// NewCircuitBreaker 创建熔断器
func NewCircuitBreaker(config CircuitBreakerConfig) *CircuitBreaker {
	if config.FailureThreshold <= 0 {
		config.FailureThreshold = DefaultCircuitBreakerConfig.FailureThreshold
	}
	if config.ResetTimeout <= 0 {
		config.ResetTimeout = DefaultCircuitBreakerConfig.ResetTimeout
	}
	if config.HalfOpenMaxRequests <= 0 {
		config.HalfOpenMaxRequests = DefaultCircuitBreakerConfig.HalfOpenMaxRequests
	}

	cb := &CircuitBreaker{
		config: config,
	}
	cb.state.Store(int32(StateClosed))
	cb.lastFailureTime.Store(time.Time{})
	return cb
}

// State 返回当前熔断器状态（线程安全）
func (cb *CircuitBreaker) State() CircuitBreakerState {
	return CircuitBreakerState(cb.state.Load())
}

// FailureCount 返回连续失败次数
func (cb *CircuitBreaker) FailureCount() int {
	return int(cb.failureCount.Load())
}

// Allow 检查是否允许请求通过
// 返回 true 表示允许，false 表示熔断器打开拒绝请求
func (cb *CircuitBreaker) Allow() bool {
	state := cb.State()

	switch state {
	case StateClosed:
		return true

	case StateOpen:
		// 检查是否超过重置超时 -> 切换到 half-open
		lastFailure := cb.lastFailureTime.Load().(time.Time)
		if time.Since(lastFailure) >= cb.config.ResetTimeout {
			cb.tryTransitionToHalfOpen()
			// half-open 允许一个探测请求
			return cb.tryAcquireHalfOpenSlot()
		}
		return false

	case StateHalfOpen:
		return cb.tryAcquireHalfOpenSlot()

	default:
		return true
	}
}

// RecordSuccess 记录成功调用
func (cb *CircuitBreaker) RecordSuccess() {
	state := cb.State()

	if state == StateHalfOpen {
		cb.transitionTo(StateClosed)
	}

	cb.failureCount.Store(0)
}

// RecordFailure 记录失败调用
func (cb *CircuitBreaker) RecordFailure() {
	cb.lastFailureTime.Store(time.Now())

	state := cb.State()

	if state == StateHalfOpen {
		// half-open 状态下失败 -> 回到 open
		cb.transitionTo(StateOpen)
		return
	}

	// closed 状态下累加失败计数
	count := cb.failureCount.Add(1)
	if count >= int32(cb.config.FailureThreshold) {
		cb.transitionTo(StateOpen)
	}
}

// Reset 重置熔断器（用于手动恢复）
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	oldState := cb.State()
	cb.state.Store(int32(StateClosed))
	cb.failureCount.Store(0)
	cb.halfOpenRequests.Store(0)
	cb.lastFailureTime.Store(time.Time{})

	slog.Info("Circuit breaker manually reset",
		"old_state", oldState,
		"new_state", StateClosed)
}

// tryTransitionToHalfOpen 尝试从 open 切换到 half-open
func (cb *CircuitBreaker) tryTransitionToHalfOpen() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	// double-check
	if cb.State() != StateOpen {
		return
	}

	cb.transitionTo(StateHalfOpen)
}

// tryAcquireHalfOpenSlot 尝试获取 half-open 状态下的探测请求槽位
func (cb *CircuitBreaker) tryAcquireHalfOpenSlot() bool {
	// 检查是否已超过最大探测请求数
	// 使用 atomic compare-and-swap 安全递增
	for {
		current := cb.halfOpenRequests.Load()
		if current >= int32(cb.config.HalfOpenMaxRequests) {
			return false
		}
		if cb.halfOpenRequests.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

// transitionTo 切换熔断器状态（必须在 mu 保护下调用）
func (cb *CircuitBreaker) transitionTo(newState CircuitBreakerState) {
	oldState := cb.State()
	if oldState == newState {
		return
	}

	cb.state.Store(int32(newState))

	if newState == StateOpen || newState == StateClosed {
		cb.halfOpenRequests.Store(0)
	}

	slog.Info("Circuit breaker state changed",
		"old_state", oldState,
		"new_state", newState,
		"failure_count", cb.failureCount.Load())

	if cb.config.OnStateChange != nil {
		cb.config.OnStateChange(oldState, newState)
	}
}

// ─────────────────────────────────────────────────────────
// DegradationResolver 降级解析器
// ─────────────────────────────────────────────────────────

// DegradationConfig 降级配置
type DegradationConfig struct {
	// CircuitBreaker 熔断器实例
	CircuitBreaker *CircuitBreaker

	// FallbackTruncateRatio 截断降级时保留的消息比例
	// 0.3 = 保留首 15% + 尾 15%，丢弃中间 70%
	FallbackTruncateRatio float64

	// EnableCache 是否启用缓存降级
	EnableCache bool

	// MaxCacheSize 缓存大小
	MaxCacheSize int
}

// DefaultDegradationConfig 默认降级配置
var DefaultDegradationConfig = DegradationConfig{
	FallbackTruncateRatio: 0.3,
	EnableCache:           true,
	MaxCacheSize:          10,
}

// DegradationResult 降级执行结果
type DegradationResult struct {
	// Result 执行结果（摘要文本）
	Result string

	// Tier 实际使用的降级层级
	Tier DegradationTier

	// Err 降级过程中的错误（非 nil 表示所有降级都失败）
	Err error

	// Duration 执行耗时
	Duration time.Duration
}

// DegradationResolver 降级解析器
// 封装 LLM 调用和降级策略的完整生命周期
type DegradationResolver struct {
	config     DegradationConfig
	breaker    *CircuitBreaker
	summaryCfg *Config // 用于截断降级时的分段策略
}

// NewDegradationResolver 创建降级解析器
func NewDegradationResolver(config DegradationConfig, summaryCfg *Config) *DegradationResolver {
	if config.CircuitBreaker == nil {
		config.CircuitBreaker = NewCircuitBreaker(DefaultCircuitBreakerConfig)
	}
	if config.FallbackTruncateRatio <= 0 || config.FallbackTruncateRatio > 0.5 {
		config.FallbackTruncateRatio = DefaultDegradationConfig.FallbackTruncateRatio
	}

	return &DegradationResolver{
		config:     config,
		breaker:    config.CircuitBreaker,
		summaryCfg: summaryCfg,
	}
}

// ExecuteWithDegradation 执行带降级策略的 LLM 调用
//
// 降级流程：
//   1. 检查熔断器 → 如果打开则跳过 LLM 调用
//   2. 尝试执行 fn（LLM 调用）
//   3. 成功 → 记录成功，返回 TierFull
//   4. 失败 → 调用 fallback 降级函数，返回对应降级层级
//
// 参数：
//   - ctx: 上下文
//   - operation: 操作名称（用于日志）
//   - fn: 主要的 LLM 调用函数
//   - fallback: 降级回退函数（TierTruncated 级别）
// 返回：
//   - result: 执行结果
//   - tier: 实际使用的降级层级
//   - err: 错误（所有层级都失败时返回非 nil）
func (d *DegradationResolver) ExecuteWithDegradation(
	ctx context.Context,
	operation string,
	fn func(context.Context) (string, error),
	fallback func() string,
) DegradationResult {
	startTime := time.Now()

	// Step 1: 检查熔断器
	if !d.breaker.Allow() {
		slog.Warn("Degradation: circuit breaker open, skipping LLM call",
			"operation", operation)

		// 直接使用 fallback（截断降级）
		if fallback != nil {
			result := fallback()
			return DegradationResult{
				Result:   result,
				Tier:     TierTruncated,
				Duration: time.Since(startTime),
			}
		}

		return DegradationResult{
			Tier:     TierRefused,
			Err:      fmt.Errorf("circuit breaker open for operation: %s", operation),
			Duration: time.Since(startTime),
		}
	}

	// Step 2: 尝试正常执行
	result, err := fn(ctx)
	if err == nil {
		d.breaker.RecordSuccess()
		return DegradationResult{
			Result:   result,
			Tier:     TierFull,
			Duration: time.Since(startTime),
		}
	}

	// Step 3: LLM 调用失败
	d.breaker.RecordFailure()
	slog.Warn("LLM call failed, degradation triggered",
		"operation", operation,
		"error", err,
		"circuit_breaker_state", d.breaker.State(),
		"failure_count", d.breaker.FailureCount())

	// Step 4: 尝试 fallback（截断降级）
	if fallback != nil {
		result := fallback()
		if result != "" {
			return DegradationResult{
				Result:   result,
				Tier:     TierTruncated,
				Duration: time.Since(startTime),
			}
		}
	}

	// Step 5: 所有降级都失败
	return DegradationResult{
		Tier:     TierRefused,
		Err:      fmt.Errorf("all degradation tiers exhausted for %s: %w", operation, err),
		Duration: time.Since(startTime),
	}
}

// CircuitBreaker 返回熔断器实例（用于监控）
func (d *DegradationResolver) CircuitBreaker() *CircuitBreaker {
	return d.breaker
}

// State 返回当前熔断器状态
func (d *DegradationResolver) State() CircuitBreakerState {
	return d.breaker.State()
}

// ─────────────────────────────────────────────────────────
// 截断降级策略
// ─────────────────────────────────────────────────────────

// TruncateMessages 截断降级策略：保留首尾消息，丢弃中间
//
// 策略说明：
//   1. System 消息始终保留
//   2. 保留最近 N 轮对话（尾部）
//   3. 保留最早的一部分消息（头部）
//   4. 丢弃中间区域的消息
//
// 参数：
//   - messages: 完整消息列表
//   - keepRecentRounds: 保留的最近对话轮数
//   - headRatio: 保留的头部消息比例（如 0.15 表示保留前 15%）
// 返回：
//   - 截断后的消息列表
//   - 丢弃的消息数量
func TruncateMessages(
	messages []llm.Message,
	keepRecentRounds int,
	headRatio float64,
) ([]llm.Message, int) {
	if len(messages) <= 4 {
		return messages, 0
	}

	if headRatio <= 0 || headRatio > 0.5 {
		headRatio = 0.15 // 默认保留前 15%
	}

	// 计算保留边界
	keepCount := keepRecentRounds * 2
	if keepCount <= 0 {
		keepCount = 6
	}
	if keepCount > len(messages)/2 {
		keepCount = len(messages) / 2
	}

	// 头部保留
	headCount := int(float64(len(messages)) * headRatio)
	if headCount < 2 {
		headCount = 2
	}

	// 确保不重叠
	if headCount+keepCount > len(messages) {
		headCount = len(messages) - keepCount
		if headCount < 0 {
			headCount = 0
		}
	}

	// 构建结果
	result := make([]llm.Message, 0, headCount+keepCount)

	// System 消息始终保留
	for _, msg := range messages[:headCount] {
		result = append(result, msg)
	}

	// 尾部保留
	tailStart := len(messages) - keepCount
	if tailStart < headCount {
		tailStart = headCount
	}
	result = append(result, messages[tailStart:]...)

	discarded := len(messages) - len(result)

	slog.Debug("Truncate fallback applied",
		"original", len(messages),
		"result", len(result),
		"discarded", discarded,
		"head_ratio", headRatio)

	return result, discarded
}
