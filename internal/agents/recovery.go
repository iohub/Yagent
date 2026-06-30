package agents

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// FailureRecord 失败记录
type FailureRecord struct {
	TaskID      string    `json:"task_id"`
	AgentID     string    `json:"agent_id"`
	Error       string    `json:"error"`
	Attempts    int       `json:"attempts"`
	LastAttempt time.Time `json:"last_attempt"`
	Recoverable bool      `json:"recoverable"`
}

// RecoveryConfig 恢复配置
type RecoveryConfig struct {
	MaxRetries                 int           // 单个任务最大重试次数（默认 3）
	RetryDelay                 time.Duration // 重试基础延迟（默认 1s）
	CircuitBreakerThreshold    int           // 熔断阈值，连续失败次数（默认 5）
	CircuitBreakerResetTimeout time.Duration // 熔断恢复时间（默认 30s）
}

// DefaultRecoveryConfig 默认恢复配置
func DefaultRecoveryConfig() RecoveryConfig {
	return RecoveryConfig{
		MaxRetries:                 3,
		RetryDelay:                 1 * time.Second,
		CircuitBreakerThreshold:    5,
		CircuitBreakerResetTimeout: 30 * time.Second,
	}
}

// CircuitBreaker 熔断器
type CircuitBreaker struct {
	state        string // "closed", "open", "half-open"
	failures     int
	threshold    int
	resetTimeout time.Duration
	lastFailure  time.Time
	mu           sync.Mutex
}

// NewCircuitBreaker 创建熔断器
func NewCircuitBreaker(threshold int, resetTimeout time.Duration) *CircuitBreaker {
	return &CircuitBreaker{
		state:        "closed",
		threshold:    threshold,
		resetTimeout: resetTimeout,
	}
}

// Allow 检查是否允许执行
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

// Success 记录成功
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

// Failure 记录失败
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

// State 返回当前状态
func (cb *CircuitBreaker) State() string {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.state
}

// RecoveryAction 恢复动作
type RecoveryAction struct {
	Type   string        // "retry", "reassign", "skip", "abort"
	Target string        // 重试或重新分配的 Agent ID
	Delay  time.Duration // 执行前的延迟
	Reason string
}

// Recovery 错误恢复管理器
type Recovery struct {
	config     RecoveryConfig
	failures   map[string]*FailureRecord
	breakers   map[string]*CircuitBreaker // agent ID → 熔断器
	stateStore StateStore
	mu         sync.Mutex
}

// NewRecovery 创建错误恢复管理器
func NewRecovery(config RecoveryConfig, stateStore StateStore) *Recovery {
	return &Recovery{
		config:     config,
		failures:   make(map[string]*FailureRecord),
		breakers:   make(map[string]*CircuitBreaker),
		stateStore: stateStore,
	}
}

// HandleAgentFailure 处理 Agent 失败
func (r *Recovery) HandleAgentFailure(ctx context.Context, agentID string, taskID string, err error) *RecoveryAction {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 获取或创建熔断器
	breaker, ok := r.breakers[agentID]
	if !ok {
		breaker = NewCircuitBreaker(r.config.CircuitBreakerThreshold, r.config.CircuitBreakerResetTimeout)
		r.breakers[agentID] = breaker
	}
	breaker.Failure()

	// 记录失败
	record, ok := r.failures[taskID]
	if !ok {
		record = &FailureRecord{
			TaskID:   taskID,
			AgentID:  agentID,
			Attempts: 0,
		}
		r.failures[taskID] = record
	}
	record.Attempts++
	record.Error = err.Error()
	record.LastAttempt = time.Now()

	// 决策
	action := r.decide(record, breaker)
	slog.Warn("Agent failure handled",
		"agent_id", agentID,
		"task_id", taskID,
		"attempts", record.Attempts,
		"action", action.Type,
		"error", err,
	)

	return action
}

// decide 决定恢复策略
func (r *Recovery) decide(record *FailureRecord, breaker *CircuitBreaker) *RecoveryAction {
	// 1. 检查熔断
	if breaker.State() == "open" {
		return &RecoveryAction{
			Type:   "abort",
			Reason: fmt.Sprintf("circuit breaker open for agent %s", record.AgentID),
		}
	}

	// 2. 检查重试次数
	if record.Attempts <= r.config.MaxRetries {
		delay := r.config.RetryDelay * time.Duration(1<<uint(record.Attempts-1))
		return &RecoveryAction{
			Type:   "retry",
			Target: record.AgentID,
			Delay:  delay,
			Reason: fmt.Sprintf("retry attempt %d/%d", record.Attempts, r.config.MaxRetries),
		}
	}

	// 3. 超过重试次数，标记为不可恢复
	record.Recoverable = false
	return &RecoveryAction{
		Type:   "skip",
		Reason: fmt.Sprintf("max retries (%d) exceeded for task %s", r.config.MaxRetries, record.TaskID),
	}
}

// HandleTaskSuccess 记录任务成功
func (r *Recovery) HandleTaskSuccess(agentID string, taskID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// 清除失败记录
	delete(r.failures, taskID)

	// 通知熔断器
	if breaker, ok := r.breakers[agentID]; ok {
		breaker.Success()
	}
}

// HandleTimeout 处理超时
func (r *Recovery) HandleTimeout(ctx context.Context, agentID string, taskID string, timeout time.Duration) *RecoveryAction {
	slog.Warn("Task timeout", "agent_id", agentID, "task_id", taskID, "timeout", timeout)
	return r.HandleAgentFailure(ctx, agentID, taskID,
		fmt.Errorf("task %s timed out after %v", taskID, timeout))
}

// RecoverFromCrash 从崩溃中恢复（通过 StateStore）
func (r *Recovery) RecoverFromCrash(ctx context.Context, sessionID string) (*DirectorState, error) {
	if r.stateStore == nil {
		return nil, fmt.Errorf("state store not available")
	}

	state, err := r.stateStore.Load(sessionID)
	if err != nil {
		return nil, fmt.Errorf("load state for recovery: %w", err)
	}

	slog.Info("Recovered from crash",
		"session_id", sessionID,
		"phase", state.Phase,
		"active_tasks", len(state.ActiveTasks),
	)

	return state, nil
}

// GetFailureStats 获取失败统计
func (r *Recovery) GetFailureStats() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()

	stats := make(map[string]int)
	for _, record := range r.failures {
		stats[record.AgentID]++
	}
	for id, breaker := range r.breakers {
		if breaker.State() != "closed" {
			stats["breaker_"+id] = 1
		}
	}
	return stats
}
