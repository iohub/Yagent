package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"yagent/internal/messaging"
	"yagent/internal/protocol"
)

// ---------------------------------------------------------------------------
// 场景 1：Publisher 为 nil 时立即返回错误
// ---------------------------------------------------------------------------

func TestUserConfirmManager_PublisherNotSet(t *testing.T) {
	m := NewUserConfirmManager()
	ctx := context.Background()

	// RequestConfirmation 应因 publisher 为 nil 而失败
	_, err := m.RequestConfirmation(ctx, "test question", "allow,deny")
	if err == nil {
		t.Fatal("expected error when publisher is not set")
	}
	if !strings.Contains(err.Error(), "publisher not set") {
		t.Errorf("expected 'publisher not set' error, got: %v", err)
	}

	// RequestUserHelp 也应因 publisher 为 nil 而失败
	_, err = m.RequestUserHelp(ctx, &protocol.UserHelpNeededData{Question: "test"})
	if err == nil {
		t.Fatal("expected error when publisher is not set for RequestUserHelp")
	}
	if !strings.Contains(err.Error(), "publisher not set") {
		t.Errorf("expected 'publisher not set' error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 辅助函数：创建带空 dispatcher 的 publisher（用于需要 publisher 的测试）
// ---------------------------------------------------------------------------

func createMockPublisher(t *testing.T) *messaging.MessagePublisher {
	t.Helper()
	// 创建一个空 dispatcher，Publish 调用不会 panic
	dispatcher := messaging.NewMessageDispatcher(1)
	return messaging.NewMessagePublisher(dispatcher)
}

// ---------------------------------------------------------------------------
// 场景 2：Context 取消时立即返回错误
// ---------------------------------------------------------------------------

func TestUserConfirmManager_ContextCancelled(t *testing.T) {
	publisher := createMockPublisher(t)
	m := NewUserConfirmManager()
	m.SetPublisher(publisher)

	// 创建一个已取消的 context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	// 调用 RequestConfirmation，应该因为 context 已取消而快速返回
	_, err := m.RequestConfirmation(ctx, "test question", "allow,deny")
	if err == nil {
		t.Fatal("expected error when context is cancelled")
	}
	if !strings.Contains(err.Error(), "cancelled") && !strings.Contains(err.Error(), "canceled") {
		t.Errorf("expected 'cancelled' error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 场景 3：RequestUserHelp Context 取消时立即返回错误
// ---------------------------------------------------------------------------

func TestUserConfirmManager_RequestUserHelp_ContextCancelled(t *testing.T) {
	publisher := createMockPublisher(t)
	m := NewUserConfirmManager()
	m.SetPublisher(publisher)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := m.RequestUserHelp(ctx, &protocol.UserHelpNeededData{
		Question:        "test question",
		InteractionType: protocol.InteractionConfirm,
	})
	if err == nil {
		t.Fatal("expected error when context is cancelled")
	}
	if !strings.Contains(err.Error(), "cancelled") && !strings.Contains(err.Error(), "canceled") {
		t.Errorf("expected 'cancelled' error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 场景 4：Context 超时时返回超时错误
// 使用短超时 context 模拟（100ms），且不发送任何响应。
// ---------------------------------------------------------------------------

func TestUserConfirmManager_ContextTimeout(t *testing.T) {
	publisher := createMockPublisher(t)
	m := NewUserConfirmManager()
	m.SetPublisher(publisher)

	// 创建 100ms 超时的 context
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := m.RequestConfirmation(ctx, "test question", "allow,deny")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error when context times out")
	}

	// 确认是在合理的超时时间内返回的（不应超过 1 秒）
	if elapsed > 1*time.Second {
		t.Errorf("context timeout test took too long: %v", elapsed)
	}

	// 错误信息应该包含 context 取消/超时的信息
	if !strings.Contains(err.Error(), "cancelled") &&
		!strings.Contains(err.Error(), "canceled") &&
		!strings.Contains(err.Error(), "timeout") &&
		!strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("expected timeout/cancel error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 场景 5：RequestUserHelp Context 超时时返回超时错误
// ---------------------------------------------------------------------------

func TestUserConfirmManager_RequestUserHelp_ContextTimeout(t *testing.T) {
	publisher := createMockPublisher(t)
	m := NewUserConfirmManager()
	m.SetPublisher(publisher)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := m.RequestUserHelp(ctx, &protocol.UserHelpNeededData{
		Question:        "test question",
		InteractionType: protocol.InteractionConfirm,
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected error when context times out")
	}

	// 确认是在合理的超时时间内返回的（不应超过 1 秒）
	if elapsed > 1*time.Second {
		t.Errorf("context timeout test took too long: %v", elapsed)
	}

	// 错误信息应该包含 context 取消/超时的信息
	if !strings.Contains(err.Error(), "cancelled") &&
		!strings.Contains(err.Error(), "canceled") &&
		!strings.Contains(err.Error(), "timeout") &&
		!strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("expected timeout/cancel error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 场景 6：Consume 方法正确处理 user_help_response 事件
// 这是测试 RequestConfirmation + OnUserResponse 完整流程的间接方式
// ---------------------------------------------------------------------------

func TestUserConfirmManager_ConsumeDeliversResponse(t *testing.T) {
	// 验证 Consume 收到非 user_help_response 类型的事件时不产生副作用
	m := NewUserConfirmManager()

	err := m.Consume(&messaging.MessageEvent{
		Type: "some_other_event",
		Content: map[string]interface{}{
			"response": "should not be delivered",
		},
	})
	if err != nil {
		t.Errorf("unexpected error for unrelated event: %v", err)
	}

	// 验证 Consume 收到空响应的事件时不产生副作用
	err = m.Consume(&messaging.MessageEvent{
		Type: "user_help_response",
		Content: map[string]interface{}{
			"response":   "",
			"request_id": "any-id",
		},
	})
	if err != nil {
		t.Errorf("unexpected error for empty response: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 场景 7：并发调用 RequestConfirmation 各自独立
// ---------------------------------------------------------------------------

func TestUserConfirmManager_ConcurrentRequests(t *testing.T) {
	publisher := createMockPublisher(t)
	m := NewUserConfirmManager()
	m.SetPublisher(publisher)

	const n = 5
	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	var wg sync.WaitGroup
	results := make([]string, n)
	errs := make([]error, n)
	mu := sync.Mutex{}

	for i := 0; i < n; i++ {
		wg.Add(1)
		idx := i
		go func() {
			defer wg.Done()
			r, e := m.RequestConfirmation(ctx, fmt.Sprintf("Question %d", idx), "yes,no")
			mu.Lock()
			results[idx] = r
			errs[idx] = e
			mu.Unlock()
		}()
	}

	wg.Wait()

	// 所有请求都应该因为 context 超时而失败
	// （因为没有响应被发送）
	for i := 0; i < n; i++ {
		if errs[i] == nil {
			t.Errorf("request %d: expected error, got nil", i)
		}
	}
}

// ---------------------------------------------------------------------------
// 场景 8：Consume 忽略缺少 response 字段的事件
// ---------------------------------------------------------------------------

func TestUserConfirmManager_Consume_NoResponse(t *testing.T) {
	m := NewUserConfirmManager()

	// 事件类型为 user_help_response 但缺少 response 字段
	err := m.Consume(&messaging.MessageEvent{
		Type:    "user_help_response",
		Content: map[string]interface{}{},
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// response 为空字符串
	err = m.Consume(&messaging.MessageEvent{
		Type: "user_help_response",
		Content: map[string]interface{}{
			"response": "",
		},
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// Content 不是 map[string]interface{}
	err = m.Consume(&messaging.MessageEvent{
		Type:    "user_help_response",
		Content: "not a map",
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 场景 9：Consume 从 Metadata 中正确提取 request_id
// ---------------------------------------------------------------------------

func TestUserConfirmManager_Consume_ExtractsRequestID(t *testing.T) {
	m := NewUserConfirmManager()

	// request_id 在 metadata 中
	err := m.Consume(&messaging.MessageEvent{
		Type: "user_help_response",
		Content: map[string]interface{}{
			"response": "approved",
		},
		Metadata: map[string]interface{}{
			"request_id": "test-request-123",
		},
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	// request_id 在 content 中（fallback）
	err = m.Consume(&messaging.MessageEvent{
		Type: "user_help_response",
		Content: map[string]interface{}{
			"response":   "denied",
			"request_id": "test-request-456",
		},
	})
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 场景 10：RequestUserHelp 自动分配 RequestID（在 publisher 检查通过后）
// ---------------------------------------------------------------------------

func TestUserConfirmManager_RequestUserHelp_AutoRequestID(t *testing.T) {
	// 验证当 data.RequestID 为空且 publisher 已设置时，会自动分配一个
	publisher := createMockPublisher(t)
	m := NewUserConfirmManager()
	m.SetPublisher(publisher)

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	data := &protocol.UserHelpNeededData{
		Question:        "Test",
		InteractionType: protocol.InteractionConfirm,
		RequestID:       "", // 空 RequestID
	}

	_, err := m.RequestUserHelp(ctx, data)
	if err == nil {
		t.Fatal("expected error due to context timeout")
	}

	// RequestUserHelp 在 publisher 检查通过后会分配 RequestID
	// 所以 data.RequestID 应该已经被设置为非空值
	if data.RequestID == "" {
		t.Error("expected RequestID to be auto-assigned")
	}
}
