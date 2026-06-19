package compact

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codeactor/internal/llm"
)

// ─────────────────────────────────────────────────────────
// DegradationTier 测试
// ─────────────────────────────────────────────────────────

func TestDegradationTier_String(t *testing.T) {
	tests := []struct {
		tier   DegradationTier
		expect string
	}{
		{TierFull, "full"},
		{TierCache, "cache"},
		{TierTruncated, "truncated"},
		{TierRefused, "refused"},
		{DegradationTier(99), "unknown(99)"},
	}

	for _, tt := range tests {
		t.Run(tt.expect, func(t *testing.T) {
			if got := tt.tier.String(); got != tt.expect {
				t.Errorf("DegradationTier(%d).String() = %q, want %q", tt.tier, got, tt.expect)
			}
		})
	}
}

func TestDegradationTier_Fallback(t *testing.T) {
	// TierFull 不是降级模式
	if TierFull.IsFallback() {
		t.Error("TierFull.IsFallback() = true, want false")
	}

	// 其他都是降级模式
	for _, tier := range []DegradationTier{TierCache, TierTruncated, TierRefused} {
		if !tier.IsFallback() {
			t.Errorf("%s.IsFallback() = false, want true", tier)
		}
	}
}

func TestDegradationTier_Operational(t *testing.T) {
	// TierFull, TierCache, TierTruncated 是 operational
	for _, tier := range []DegradationTier{TierFull, TierCache, TierTruncated} {
		if !tier.IsOperational() {
			t.Errorf("%s.IsOperational() = false, want true", tier)
		}
	}

	// TierRefused 不是 operational
	if TierRefused.IsOperational() {
		t.Error("TierRefused.IsOperational() = true, want false")
	}
}

// ─────────────────────────────────────────────────────────
// CircuitBreaker 测试
// ─────────────────────────────────────────────────────────

func TestCircuitBreaker_InitialState(t *testing.T) {
	cb := NewCircuitBreaker(DefaultCircuitBreakerConfig)

	if cb.State() != StateClosed {
		t.Errorf("initial State() = %s, want %s", cb.State(), StateClosed)
	}

	if cb.FailureCount() != 0 {
		t.Errorf("initial FailureCount() = %d, want 0", cb.FailureCount())
	}

	if !cb.Allow() {
		t.Error("initial Allow() = false, want true")
	}
}

func TestCircuitBreaker_FailuresTransitionToOpen(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold: 5,
		ResetTimeout:     30 * time.Second,
	})

	// 前 4 次失败不应触发熔断
	for i := 0; i < 4; i++ {
		if cb.State() != StateClosed {
			t.Errorf("after %d failures: State() = %s, want %s", i+1, cb.State(), StateClosed)
		}
		cb.RecordFailure()
	}

	// 第 5 次失败后应切换到 open
	cb.RecordFailure()
	if cb.State() != StateOpen {
		t.Errorf("after 5 failures: State() = %s, want %s", cb.State(), StateOpen)
	}

	if cb.FailureCount() != 5 {
		t.Errorf("FailureCount() = %d, want 5", cb.FailureCount())
	}
}

func TestCircuitBreaker_OpenRejectsRequests(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold: 3,
		ResetTimeout:     30 * time.Second,
	})

	// 触发熔断
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}

	if cb.State() != StateOpen {
		t.Fatalf("expected StateOpen after failures, got %s", cb.State())
	}

	if cb.Allow() {
		t.Error("Allow() = true in StateOpen, want false")
	}
}

func TestCircuitBreaker_ResetTimeoutToHalfOpen(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold: 3,
		ResetTimeout:     100 * time.Millisecond, // 很短的超时时间
	})

	// 触发熔断
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}

	if cb.State() != StateOpen {
		t.Fatalf("expected StateOpen, got %s", cb.State())
	}

	// 在超时时间内不应允许
	if cb.Allow() {
		t.Error("Allow() = true before reset timeout, want false")
	}

	// 等待超时
	time.Sleep(150 * time.Millisecond)

	// 超过超时后应允许探测请求
	if !cb.Allow() {
		t.Error("Allow() = false after reset timeout, want true")
	}

	if cb.State() != StateHalfOpen {
		t.Errorf("State() = %s, want StateHalfOpen", cb.State())
	}
}

func TestCircuitBreaker_HalfOpenSuccessToClosed(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold:    3,
		ResetTimeout:        100 * time.Millisecond,
		HalfOpenMaxRequests: 1,
	})

	// 触发熔断
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}

	// 等待超时进入 half-open
	time.Sleep(150 * time.Millisecond)

	// Allow 进入 half-open
	if !cb.Allow() {
		t.Error("Allow() = false before timeout, want true after timeout")
	}

	if cb.State() != StateHalfOpen {
		t.Fatalf("expected StateHalfOpen, got %s", cb.State())
	}

	// 记录成功 -> 切换到 closed
	cb.RecordSuccess()

	if cb.State() != StateClosed {
		t.Errorf("after RecordSuccess in half-open: State() = %s, want %s", cb.State(), StateClosed)
	}

	if cb.FailureCount() != 0 {
		t.Errorf("after RecordSuccess: FailureCount() = %d, want 0", cb.FailureCount())
	}
}

func TestCircuitBreaker_HalfOpenFailureToOpen(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold:    3,
		ResetTimeout:        100 * time.Millisecond,
		HalfOpenMaxRequests: 1,
	})

	// 触发熔断
	for i := 0; i < 3; i++ {
		cb.RecordFailure()
	}

	// 等待超时进入 half-open
	time.Sleep(150 * time.Millisecond)

	// Allow 进入 half-open
	if !cb.Allow() {
		t.Fatal("Allow() = false before timeout, want true after timeout")
	}

	if cb.State() != StateHalfOpen {
		t.Fatalf("expected StateHalfOpen, got %s", cb.State())
	}

	// 记录失败 -> 回到 open
	cb.RecordFailure()

	if cb.State() != StateOpen {
		t.Errorf("after RecordFailure in half-open: State() = %s, want %s", cb.State(), StateOpen)
	}
}

func TestCircuitBreaker_ManualReset(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold: 2,
		ResetTimeout:     30 * time.Second,
	})

	// 触发熔断
	cb.RecordFailure()
	cb.RecordFailure()

	if cb.State() != StateOpen {
		t.Fatalf("expected StateOpen, got %s", cb.State())
	}

	// 手动重置
	cb.Reset()

	if cb.State() != StateClosed {
		t.Errorf("after Reset: State() = %s, want %s", cb.State(), StateClosed)
	}

	if cb.FailureCount() != 0 {
		t.Errorf("after Reset: FailureCount() = %d, want 0", cb.FailureCount())
	}
}

func TestCircuitBreaker_ConcurrentSafety(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold: 100,
		ResetTimeout:     1 * time.Millisecond,
	})

	var wg sync.WaitGroup
	var successCount, failCount int64
	const goroutines = 50
	const iterations = 100

	// 并发记录失败
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				if cb.Allow() {
					cb.RecordFailure()
					atomic.AddInt64(&failCount, 1)
				} else {
					atomic.AddInt64(&successCount, 1)
				}
			}
		}()
	}

	wg.Wait()

	// 验证没有 panic 或数据竞争（由 -race 标志检测）
	t.Logf("goroutines=%d, iterations=%d, failures recorded=%d, blocked=%d",
		goroutines, iterations, failCount, successCount)
}

func TestCircuitBreaker_ConcurrentSuccessInHalfOpen(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold:    2,
		ResetTimeout:        50 * time.Millisecond,
		HalfOpenMaxRequests: 1,
	})

	// 触发熔断
	cb.RecordFailure()
	cb.RecordFailure()

	// 等待超时
	time.Sleep(100 * time.Millisecond)

	// 多个 goroutine 并发尝试
	var wg sync.WaitGroup
	var allowedCount int64

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if cb.Allow() {
				atomic.AddInt64(&allowedCount, 1)
			}
		}()
	}

	wg.Wait()

	// 只有 1 个探测请求应该被允许
	if allowedCount != 1 {
		t.Errorf("allowedCount = %d, want 1", allowedCount)
	}
}

func TestCircuitBreaker_StateChangeCallback(t *testing.T) {
	var transitions []string

	cb := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold:    2,
		ResetTimeout:        100 * time.Millisecond,
		HalfOpenMaxRequests: 1,
		OnStateChange: func(oldState, newState CircuitBreakerState) {
			transitions = append(transitions, fmt.Sprintf("%s->%s", oldState, newState))
		},
	})

	// 触发熔断 -> closed->open
	for i := 0; i < 2; i++ {
		cb.RecordFailure()
	}

	// 等待超时 -> open->half-open
	time.Sleep(150 * time.Millisecond)
	cb.Allow()

	// 记录失败 -> half-open->open
	cb.RecordFailure()

	if len(transitions) != 3 {
		t.Errorf("got %d transitions, want 3", len(transitions))
	}

	// 验证转换路径
	expectedPaths := []string{"closed->open", "open->half-open", "half-open->open"}
	for i, expected := range expectedPaths {
		if transitions[i] != expected {
			t.Errorf("transition[%d] = %s, want %s", i, transitions[i], expected)
		}
	}
}

// ─────────────────────────────────────────────────────────
// DegradationResolver 测试
// ─────────────────────────────────────────────────────────

func TestDegradationResolver_Success(t *testing.T) {
	resolver := NewDegradationResolver(DegradationConfig{
		CircuitBreaker: NewCircuitBreaker(DefaultCircuitBreakerConfig),
	}, nil)

	result := resolver.ExecuteWithDegradation(
		context.Background(),
		"test-operation",
		func(ctx context.Context) (string, error) {
			return "success result", nil
		},
		func() string { return "fallback" },
	)

	if result.Tier != TierFull {
		t.Errorf("Tier = %s, want %s", result.Tier, TierFull)
	}

	if result.Result != "success result" {
		t.Errorf("Result = %q, want %q", result.Result, "success result")
	}

	if result.Err != nil {
		t.Errorf("Err = %v, want nil", result.Err)
	}
}

func TestDegradationResolver_FallbackOnFailure(t *testing.T) {
	resolver := NewDegradationResolver(DegradationConfig{
		CircuitBreaker: NewCircuitBreaker(DefaultCircuitBreakerConfig),
	}, nil)

	result := resolver.ExecuteWithDegradation(
		context.Background(),
		"test-fallback",
		func(ctx context.Context) (string, error) {
			return "", fmt.Errorf("LLM timeout")
		},
		func() string { return "truncated result" },
	)

	if result.Tier != TierTruncated {
		t.Errorf("Tier = %s, want %s", result.Tier, TierTruncated)
	}

	if result.Result != "truncated result" {
		t.Errorf("Result = %q, want %q", result.Result, "truncated result")
	}
}

func TestDegradationResolver_CircuitBreakerOpen(t *testing.T) {
	breaker := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold: 1,
		ResetTimeout:     30 * time.Second,
	})

	// 触发熔断
	breaker.RecordFailure()

	resolver := NewDegradationResolver(DegradationConfig{
		CircuitBreaker: breaker,
	}, nil)

	result := resolver.ExecuteWithDegradation(
		context.Background(),
		"test-open",
		func(ctx context.Context) (string, error) {
			t.Error("LLM function should not be called when circuit breaker is open")
			return "", nil
		},
		func() string { return "circuit-open-fallback" },
	)

	if result.Tier != TierTruncated {
		t.Errorf("Tier = %s, want %s", result.Tier, TierTruncated)
	}

	if result.Result != "circuit-open-fallback" {
		t.Errorf("Result = %q, want %q", result.Result, "circuit-open-fallback")
	}
}

func TestDegradationResolver_NoFallback(t *testing.T) {
	breaker := NewCircuitBreaker(CircuitBreakerConfig{
		FailureThreshold: 1,
		ResetTimeout:     30 * time.Second,
	})
	breaker.RecordFailure()

	resolver := NewDegradationResolver(DegradationConfig{
		CircuitBreaker: breaker,
	}, nil)

	result := resolver.ExecuteWithDegradation(
		context.Background(),
		"test-no-fallback",
		func(ctx context.Context) (string, error) {
			t.Error("should not be called")
			return "", nil
		},
		nil, // no fallback
	)

	if result.Tier != TierRefused {
		t.Errorf("Tier = %s, want %s", result.Tier, TierRefused)
	}

	if result.Err == nil {
		t.Error("Err should not be nil")
	}
}

func TestDegradationResolver_AllTiersExhausted(t *testing.T) {
	resolver := NewDegradationResolver(DegradationConfig{
		CircuitBreaker: NewCircuitBreaker(DefaultCircuitBreakerConfig),
	}, nil)

	result := resolver.ExecuteWithDegradation(
		context.Background(),
		"test-exhausted",
		func(ctx context.Context) (string, error) {
			return "", fmt.Errorf("LLM error")
		},
		func() string { return "" }, // fallback returns empty = failure
	)

	if result.Tier != TierRefused {
		t.Errorf("Tier = %s, want %s", result.Tier, TierRefused)
	}

	if result.Err == nil {
		t.Error("Err should not be nil when all tiers exhausted")
	}
}

func TestDegradationResolver_CircuitBreakerAccess(t *testing.T) {
	breaker := NewCircuitBreaker(DefaultCircuitBreakerConfig)
	resolver := NewDegradationResolver(DegradationConfig{
		CircuitBreaker: breaker,
	}, nil)

	if resolver.CircuitBreaker() != breaker {
		t.Error("CircuitBreaker() should return the same instance")
	}

	if resolver.State() != StateClosed {
		t.Errorf("State() = %s, want %s", resolver.State(), StateClosed)
	}
}

// ─────────────────────────────────────────────────────────
// TruncateMessages 测试
// ─────────────────────────────────────────────────────────

func TestTruncateMessages_ShortMessages(t *testing.T) {
	messages := []llm.Message{
		{Role: llm.RoleSystem, Content: "system"},
		{Role: llm.RoleUser, Content: "user1"},
		{Role: llm.RoleAssistant, Content: "assistant1"},
		{Role: llm.RoleUser, Content: "user2"},
	}

	result, discarded := TruncateMessages(messages, 2, 0.15)

	if len(result) != len(messages) {
		t.Errorf("len(result) = %d, want %d", len(result), len(messages))
	}

	if discarded != 0 {
		t.Errorf("discarded = %d, want 0 for short messages", discarded)
	}
}

func TestTruncateMessages_KeepHeadAndTail(t *testing.T) {
	// 创建 20 条消息
	messages := make([]llm.Message, 20)
	for i := 0; i < 20; i++ {
		messages[i] = llm.Message{
			Role:    llm.RoleUser,
			Content: fmt.Sprintf("message-%d", i),
		}
	}
	// 第一条是 system
	messages[0] = llm.Message{Role: llm.RoleSystem, Content: "system-message"}

	result, discarded := TruncateMessages(messages, 3, 0.15)

	expectedTotal := 20 - discarded
	if len(result) != expectedTotal {
		t.Errorf("len(result) = %d, want %d (20 - %d)", len(result), expectedTotal, discarded)
	}

	// 第一条消息（system）应该保留
	if result[0].Content != "system-message" {
		t.Errorf("first message = %q, want %q", result[0].Content, "system-message")
	}

	// 最后一条消息应该保留
	if result[len(result)-1].Content != "message-19" {
		t.Errorf("last message = %q, want %q", result[len(result)-1].Content, "message-19")
	}

	// 丢弃的消息数量应该等于中间被丢弃的部分
	t.Logf("Original: 20, Result: %d, Discarded: %d", len(result), discarded)
}

func TestTruncateMessages_SystemAlwaysKept(t *testing.T) {
	// 创建 10 条消息，system 在头部
	messages := make([]llm.Message, 10)
	for i := 0; i < 10; i++ {
		messages[i] = llm.Message{
			Role:    llm.RoleUser,
			Content: fmt.Sprintf("user-%d", i),
		}
	}
	messages[0] = llm.Message{Role: llm.RoleSystem, Content: "system"}

	result, _ := TruncateMessages(messages, 2, 0.15)

	// 检查系统消息是否在结果中
	found := false
	for _, msg := range result {
		if msg.Content == "system" {
			found = true
			break
		}
	}

	if !found {
		t.Error("system message should always be kept")
	}
}

func TestTruncateMessages_DefaultHeadRatio(t *testing.T) {
	messages := make([]llm.Message, 20)
	for i := 0; i < 20; i++ {
		messages[i] = llm.Message{Content: fmt.Sprintf("msg-%d", i)}
	}

	// 传入无效的 headRatio，应该使用默认值 0.15
	result, _ := TruncateMessages(messages, 2, 0)
	if len(result) >= len(messages) {
		t.Error("messages should be truncated even with default headRatio")
	}

	// 传入过大的 headRatio，应该限制
	result2, _ := TruncateMessages(messages, 2, 0.9)
	if len(result2) >= len(messages) {
		t.Error("messages should be truncated with large headRatio")
	}
}

func TestTruncateMessages_OverlapPrevention(t *testing.T) {
	// 创建只有 6 条消息（接近阈值），keepRecentRounds=3 -> 6 条
	// headCount + keepCount 可能超过总数，应该防止重叠
	messages := make([]llm.Message, 6)
	for i := 0; i < 6; i++ {
		messages[i] = llm.Message{Content: fmt.Sprintf("msg-%d", i)}
	}

	result, _ := TruncateMessages(messages, 3, 0.5)

	if len(result) > len(messages) {
		t.Errorf("result length %d > original %d", len(result), len(messages))
	}

	// 不应该有重复消息
	seen := make(map[string]bool)
	for _, msg := range result {
		if seen[msg.Content] {
			t.Errorf("duplicate message: %q", msg.Content)
		}
		seen[msg.Content] = true
	}
}

func TestCircuitBreaker_String(t *testing.T) {
	tests := []struct {
		state  CircuitBreakerState
		expect string
	}{
		{StateClosed, "closed"},
		{StateOpen, "open"},
		{StateHalfOpen, "half-open"},
		{CircuitBreakerState(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expect, func(t *testing.T) {
			if got := tt.state.String(); got != tt.expect {
				t.Errorf("CircuitBreakerState(%d).String() = %q, want %q", tt.state, got, tt.expect)
			}
		})
	}
}

// TestCircuitBreaker_DefaultConfig 验证默认配置值
func TestCircuitBreaker_DefaultConfig(t *testing.T) {
	cb := NewCircuitBreaker(CircuitBreakerConfig{})

	if cb.config.FailureThreshold != 5 {
		t.Errorf("default FailureThreshold = %d, want 5", cb.config.FailureThreshold)
	}

	if cb.config.ResetTimeout != 30*time.Second {
		t.Errorf("default ResetTimeout = %v, want 30s", cb.config.ResetTimeout)
	}

	if cb.config.HalfOpenMaxRequests != 1 {
		t.Errorf("default HalfOpenMaxRequests = %d, want 1", cb.config.HalfOpenMaxRequests)
	}
}

// BenchmarkCircuitBreaker_Allow 性能基准测试
func BenchmarkCircuitBreaker_Allow(b *testing.B) {
	cb := NewCircuitBreaker(DefaultCircuitBreakerConfig)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cb.Allow()
	}
}

// BenchmarkCircuitBreaker_Failures 性能基准测试
func BenchmarkCircuitBreaker_Failures(b *testing.B) {
	cb := NewCircuitBreaker(DefaultCircuitBreakerConfig)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cb.RecordFailure()
	}
}
