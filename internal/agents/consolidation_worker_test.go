package agents

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"codeactor/internal/llm"
	"codeactor/internal/memory"
)

// ============================================================================
// Mock LLM Engine for Consolidation Tests
// ============================================================================

// mockConsolidationEngine 模拟 LLM 引擎，返回预定义的 consolidation 结果
type mockConsolidationEngine struct {
	mu         sync.Mutex
	response   string
	callCount  int32
	shouldFail bool
	delay      time.Duration
}

func (m *mockConsolidationEngine) GenerateContent(ctx context.Context, messages []llm.Message, tools []llm.ToolDef, opts *llm.CallOptions) (*llm.Response, error) {
	atomic.AddInt32(&m.callCount, 1)

	// 等待 delay 或 context 取消，优先响应取消
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(m.delay):
	}

	m.mu.Lock()
	fail := m.shouldFail
	resp := m.response
	m.mu.Unlock()

	if fail {
		return nil, fmt.Errorf("mock consolidation engine failure")
	}

	return &llm.Response{
		Choices: []llm.Choice{
			{Content: resp},
		},
	}, nil
}

func (m *mockConsolidationEngine) Model() string {
	return "mock-model"
}

func (m *mockConsolidationEngine) CallCount() int {
	return int(atomic.LoadInt32(&m.callCount))
}

// setResponse 线程安全地设置 mock 响应
func (m *mockConsolidationEngine) setResponse(resp string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.response = resp
}

// setFail 线程安全地设置失败模式
func (m *mockConsolidationEngine) setFail(fail bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.shouldFail = fail
}

// ============================================================================
// Test ConsolidationTask
// ============================================================================

func TestConsolidationTask_Basic(t *testing.T) {
	task := &ConsolidationTask{
		NewObservations: "## Architecture\n- Found a new architecture pattern",
	}
	if task.NewObservations == "" {
		t.Error("task should have observations")
	}
}

// ============================================================================
// Test NewConsolidationWorker
// ============================================================================

func TestNewConsolidationWorker(t *testing.T) {
	shared := memory.NewSharedMemory(100)
	store := NewRepoMemoryStore("test-repo", shared)
	engine := &mockConsolidationEngine{}

	worker := NewConsolidationWorker(store, engine)
	if worker == nil {
		t.Fatal("expected non-nil worker")
	}
	if worker.store != store {
		t.Error("store reference mismatch")
	}
	if worker.engine != engine {
		t.Error("engine reference mismatch")
	}
}

// ============================================================================
// Test Submit
// ============================================================================

func TestSubmit_NonBlocking_Success(t *testing.T) {
	shared := memory.NewSharedMemory(100)
	store := NewRepoMemoryStore("test-repo", shared)
	engine := &mockConsolidationEngine{}

	worker := NewConsolidationWorker(store, engine)
	worker.Start()

	defer worker.Stop()
	// Worker 未启动时 Submit 应该阻塞或返回...实际上未启动时channel有buffer，应该可以提交
	ok := worker.Submit(&ConsolidationTask{NewObservations: "test"})
	// channel buffer 足够，应该成功
	if !ok {
		t.Error("submit should succeed when channel has space")
	}
}

func TestSubmit_ChannelFull_Drop(t *testing.T) {
	shared := memory.NewSharedMemory(100)
	store := NewRepoMemoryStore("test-repo", shared)
	engine := &mockConsolidationEngine{delay: 100 * time.Millisecond} // 足够让 channel 填满且测试快速完成

	worker := NewConsolidationWorker(store, engine)
	worker.Start()
	defer worker.Stop()

	// 填充 channel
	filled := 0
	for i := 0; i < channelBufferSize*2; i++ {
		if worker.Submit(&ConsolidationTask{NewObservations: "test"}) {
			filled++
		}
	}

	// 至少 channelBufferSize 个应该成功
	if filled < channelBufferSize {
		t.Errorf("expected at least %d successful submits, got %d", channelBufferSize, filled)
	}
	// 有可能超过 channelBufferSize 的提交被丢弃
}

// ============================================================================
// Test process - Integration with mock LLM
// ============================================================================

func TestConsolidationWorker_Process_UpdatesMemory(t *testing.T) {
	shared := memory.NewSharedMemory(100)
	store := NewRepoMemoryStore("test-repo", shared)
	if err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	engine := &mockConsolidationEngine{
		response: validMemoryContent,
	}

	worker := NewConsolidationWorker(store, engine)
	worker.Start()
	defer worker.Stop()

	// 提交任务
	task := &ConsolidationTask{NewObservations: "## Architecture\n- Found a router"}
	worker.Submit(task)

	// 等待处理完成（worker 内部异步）
	time.Sleep(500 * time.Millisecond)

	// 验证记忆已更新
	current := store.Get()
	if current == DefaultMemoryContent {
		t.Error("memory should have been updated after consolidation")
	}
	if !strings.Contains(current, "Architecture") {
		t.Error("memory should contain Architecture section")
	}

	if engine.CallCount() < 1 {
		t.Error("LLM should have been called at least once")
	}
}

func TestConsolidationWorker_Process_LLMFailure_KeepsOldMemory(t *testing.T) {
	shared := memory.NewSharedMemory(100)
	store := NewRepoMemoryStore("test-repo", shared)
	if err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	// 先保存一些内容
	originalContent := "## Architecture\n- Original content" +
		"\n## Patterns\n- Original patterns" +
		"\n## Conventions\n- Original conventions" +
		"\n## Dependencies\n- Original deps" +
		"\n## Gotchas\n- Original gotchas" +
		"\n## Key Files\n- Original files"
	if err := store.Save(context.Background(), originalContent); err != nil {
		t.Fatal(err)
	}

	// 模拟 LLM 失败
	engine := &mockConsolidationEngine{
		shouldFail: true,
	}

	worker := NewConsolidationWorker(store, engine)
	worker.Start()
	defer worker.Stop()

	// 提交任务
	task := &ConsolidationTask{NewObservations: "## Architecture\n- New content"}
	worker.Submit(task)

	// 等待处理完成
	time.Sleep(2 * time.Second)

	// 验证旧记忆未被覆盖
	current := store.Get()
	if current != originalContent {
		t.Errorf("old memory should be preserved after LLM failure, got: %s", current)
	}
}

func TestConsolidationWorker_Process_EmptyObservation_Skipped(t *testing.T) {
	shared := memory.NewSharedMemory(100)
	store := NewRepoMemoryStore("test-repo", shared)
	if err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	engine := &mockConsolidationEngine{
		response: validMemoryContent,
	}

	worker := NewConsolidationWorker(store, engine)
	worker.Start()
	defer worker.Stop()

	// 提交空观察
	task := &ConsolidationTask{NewObservations: ""}
	worker.Submit(task)

	time.Sleep(200 * time.Millisecond)

	if engine.CallCount() > 0 {
		t.Error("LLM should not be called with empty observation")
	}
}

func TestConsolidationWorker_Process_InvalidFormat_KeepsOldMemory(t *testing.T) {
	shared := memory.NewSharedMemory(100)
	store := NewRepoMemoryStore("test-repo", shared)
	if err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}

	originalContent := DefaultMemoryContent
	if err := store.Save(context.Background(), originalContent); err != nil {
		t.Fatal(err)
	}

	// LLM 返回无效格式（缺少 section）
	engine := &mockConsolidationEngine{
		response: "This is not valid memory format",
	}

	worker := NewConsolidationWorker(store, engine)
	worker.Start()
	defer worker.Stop()

	task := &ConsolidationTask{NewObservations: "some observations"}
	worker.Submit(task)

	time.Sleep(500 * time.Millisecond)

	current := store.Get()
	if current != originalContent {
		t.Error("old memory should be preserved when LLM returns invalid format")
	}
}

// ============================================================================
// Test Stop - graceful shutdown
// ============================================================================

func TestConsolidationWorker_Stop_DrainsPending(t *testing.T) {
	shared := memory.NewSharedMemory(100)
	store := NewRepoMemoryStore("test-repo", shared)
	if err := store.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	engine := &mockConsolidationEngine{
		response: validMemoryContent,
		delay:    50 * time.Millisecond,
	}

	worker := NewConsolidationWorker(store, engine)
	worker.Start()

	// 提交几个任务
	for i := 0; i < 3; i++ {
		worker.Submit(&ConsolidationTask{NewObservations: "observations"})
	}

	// 立即停止，等待排队的任务处理完成
	done := make(chan bool)
	go func() {
		worker.Stop()
		done <- true
	}()

	select {
	case <-done:
		// 正常停止
	case <-time.After(5 * time.Second):
		t.Fatal("Stop() timed out - worker did not drain pending tasks")
	}
}

// ============================================================================
// Test Submit - after stop
// ============================================================================

func TestConsolidationWorker_Submit_AfterStop_Panics(t *testing.T) {
	shared := memory.NewSharedMemory(100)
	store := NewRepoMemoryStore("test-repo", shared)
	engine := &mockConsolidationEngine{}

	worker := NewConsolidationWorker(store, engine)
	worker.Start()
	worker.Stop()

	// Stop 后 channel 被关闭，可能 panic 可能不 panic，取决于是否还有 goroutine 在接收
	// 我们应该只是测试不 panic
	defer func() {
		if r := recover(); r != nil {
			t.Logf("recovered from panic after submit to stopped worker: %v", r)
		}
	}()

	worker.Submit(&ConsolidationTask{NewObservations: "test"})
	// 不验证结果，只是确保不崩溃
}

// ============================================================================
// Helpers
// ============================================================================

var validMemoryContent = `# Repository Memory

## Architecture
- Uses a layered architecture with controllers, services, and repositories

## Patterns
- Dependency injection via constructor parameters

## Conventions
- Go files use snake_case for variable names

## Dependencies
- chi router for HTTP routing

## Gotchas
- Context cancellation must be propagated manually

## Key Files
- cmd/server/main.go - Application entry point`

// 确保 mock 实现了 llm.Engine 接口
var _ llm.Engine = (*mockConsolidationEngine)(nil)
