package agents

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"yagent/internal/llm"
	"yagent/internal/memory"
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

func (m *mockConsolidationEngine) CloseIdleConnections() {
	// no-op for mock
}

func (m *mockConsolidationEngine) CallCount() int {
	return int(atomic.LoadInt32(&m.callCount))
}

// ============================================================================
// Test Submit
// ============================================================================

func TestSubmit_NonBlocking_Success(t *testing.T) {
	shared := memory.NewSharedMemory(100)
	store := NewRepoMemoryStore("test-repo", shared)
	engine := &mockConsolidationEngine{}

	worker := NewConsolidationWorker(store, engine, nil)
	worker.Start()

	defer worker.Stop()
	ok := worker.Submit(&ConsolidationTask{NewObservations: "test"})
	if !ok {
		t.Error("submit should succeed when channel has space")
	}
}

func TestSubmit_ChannelFull_DropsExtras(t *testing.T) {
	shared := memory.NewSharedMemory(100)
	store := NewRepoMemoryStore("test-repo", shared)
	engine := &mockConsolidationEngine{}

	// 故意不 Start()：没有消费者取走任务，buffer 语义变成确定性的，
	// 不需要靠引擎 delay 去抢时间窗。
	worker := NewConsolidationWorker(store, engine, nil)

	accepted := 0
	for i := 0; i < channelBufferSize*2; i++ {
		if worker.Submit(&ConsolidationTask{NewObservations: "test"}) {
			accepted++
		}
	}

	if accepted != channelBufferSize {
		t.Errorf("accepted = %d, want exactly %d (buffer 写满后必须丢弃)", accepted, channelBufferSize)
	}
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

	worker := NewConsolidationWorker(store, engine, nil)
	worker.Start()

	// 提交任务
	task := &ConsolidationTask{NewObservations: "## Architecture\n- Found a router"}
	worker.Submit(task)

	// Stop 会等 run 循环把队列里的任务处理完，比睡固定时长确定
	worker.Stop()

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

	worker := NewConsolidationWorker(store, engine, nil)
	worker.Start()

	// 提交任务
	task := &ConsolidationTask{NewObservations: "## Architecture\n- New content"}
	worker.Submit(task)

	// Stop 排干队列后再断言：callConsolidationLLM 的重试要睡 1s+2s，
	// 此前睡 2s 去断言等于在重试完成前就检查，无论怎么写都通过。
	worker.Stop()

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

	worker := NewConsolidationWorker(store, engine, nil)
	worker.Start()

	// 提交空观察
	task := &ConsolidationTask{NewObservations: ""}
	worker.Submit(task)

	worker.Stop()

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

	worker := NewConsolidationWorker(store, engine, nil)
	worker.Start()

	task := &ConsolidationTask{NewObservations: "some observations"}
	worker.Submit(task)

	worker.Stop()

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

	worker := NewConsolidationWorker(store, engine, nil)
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

// ============================================================================
// Test truncateRunes
// ============================================================================

// TestTruncateRunes 固化"按 rune 而非按字节计数"这条契约：
// 中文按字节会提前触发截断（修复前还会 panic），按 rune 才不会。
func TestTruncateRunes(t *testing.T) {
	chinese := strings.Repeat("中文字符测试", 20) // 120 rune = 360 字节
	if len(chinese) <= 200 {
		t.Fatalf("fixture must exceed 200 bytes to pin the byte/rune confusion, got %d bytes", len(chinese))
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"short_mixed", "Hello, 世界！这是简短内容。", "Hello, 世界！这是简短内容。"},
		{"cjk_over_200_bytes_under_200_runes", chinese, chinese},
		{"ascii_over_limit", strings.Repeat("A", 300), strings.Repeat("A", 200) + "..."},
		{"ascii_exact_boundary", strings.Repeat("B", 200), strings.Repeat("B", 200)},
		{"ascii_one_past_boundary", strings.Repeat("C", 201), strings.Repeat("C", 200) + "..."},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateRunes(tc.input, 200)
			if got != tc.want {
				t.Errorf("truncateRunes(%d bytes / %d runes) = %d runes, want %d runes",
					len(tc.input), len([]rune(tc.input)), len([]rune(got)), len([]rune(tc.want)))
			}
		})
	}
}

// Stop 之后 Submit 必须回绝而不是 panic：向关闭中的 channel 发送是 panic，
// 而这条路径挂在正常退出上 —— Yagent.Close 关掉 worker 的同时，某条还在收尾的
// 委派递上来一条整理请求，进程就被自己的清理代码砸掉。
func TestConsolidationWorker_SubmitAfterStopIsRejectedNotPanicked(t *testing.T) {
	worker := NewConsolidationWorker(NewRepoMemoryStore("test-repo", memory.NewSharedMemory(100)), &mockConsolidationEngine{}, nil)
	worker.Start()
	worker.Stop()

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Stop 之后 Submit panic: %v", r)
		}
	}()
	if worker.Submit(&ConsolidationTask{NewObservations: "x"}) {
		t.Error("Stop 之后 Submit 仍接受任务, want false —— 队列已经没有消费者了")
	}
}

// 真实形状是竞态而不是顺序：提交方与收尾方同时跑。没有那把 sendMu 时
// "检查过了"与"发送了"之间可以插进 close —— 偶发 panic，且发生在退出路径上最难复现。
func TestConsolidationWorker_ConcurrentSubmitDuringStopNeverPanics(t *testing.T) {
	worker := NewConsolidationWorker(NewRepoMemoryStore("test-repo", memory.NewSharedMemory(100)), &mockConsolidationEngine{}, nil)
	worker.Start()

	var panics atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					panics.Add(1)
				}
			}()
			for j := 0; j < 50; j++ {
				worker.Submit(&ConsolidationTask{NewObservations: "x"})
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		worker.Stop()
	}()
	wg.Wait()

	if got := panics.Load(); got != 0 {
		t.Errorf("Submit 与 Stop 竞速时 panic 了 %d 次, want 0", got)
	}
}
