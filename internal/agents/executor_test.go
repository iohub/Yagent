package agents

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/messaging"
	"yagent/internal/tools"
)

// ─── Mock Engine ──────────────────────────────────────────────────────────────

type mockLLM struct {
	responses []*llm.Response
	callCount int
}

func (m *mockLLM) GenerateContent(ctx context.Context, messages []llm.Message, tools []llm.ToolDef, opts *llm.CallOptions) (*llm.Response, error) {
	if m.callCount >= len(m.responses) {
		return nil, fmt.Errorf("mockLLM: unexpected call #%d (only %d responses configured)", m.callCount, len(m.responses))
	}
	resp := m.responses[m.callCount]
	m.callCount++
	return resp, nil
}

func (m *mockLLM) Model() string {
	return "mock-model"
}

func (m *mockLLM) CloseIdleConnections() {
	// no-op for mock
}

// ─── Test Helpers ────────────────────────────────────────────────────────────

// newBlockingAdapter 创建一个会阻塞直到 context 超时的适配器
func newBlockingAdapter(name, description string) *tools.Adapter {
	return tools.NewAdapter(name, description, func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		// 阻塞直到 context 取消
		<-ctx.Done()
		return nil, ctx.Err()
	})
}

// newFastAdapter 创建一个快速返回的适配器
func newFastAdapter(name, description string, result string) *tools.Adapter {
	return tools.NewAdapter(name, description, func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		return result, nil
	})
}

// restoreToolTimeout 把 defaultToolTimeout 临时调短，让超时路径在测试里可触发；
// 恢复交给 t.Cleanup，因此测试函数不要用 defer 还原（会与并行测试交错）。
func restoreToolTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := defaultToolTimeout
	defaultToolTimeout = d
	t.Cleanup(func() { defaultToolTimeout = old })
}

// ─── Test Cases ──────────────────────────────────────────────────────────────

// TestRunAgentLoop_ToolCallTimeout 测试工具调用超时保护机制
// 验证当工具调用超时时，系统能正确返回超时错误信息
func TestRunAgentLoop_ToolCallTimeout(t *testing.T) {
	// 用 executor 自己的超时 seam，避免让测试等真实墙钟
	restoreToolTimeout(t, 50*time.Millisecond)

	// 创建 blocking adapter：模拟 run_bash 卡死
	blockingAdapter := newBlockingAdapter("run_bash", "run bash command")

	// 创建 mock LLM，第一次返回 tool_call，第二次返回文本
	mock := &mockLLM{
		responses: []*llm.Response{
			{
				Choices: []llm.Choice{
					{
						ToolCalls: []llm.ToolCall{
							{
								ID:   "call_timeout_test",
								Type: "function",
								Function: llm.FunctionCall{
									Name:      "run_bash",
									Arguments: `{"command": "sleep 100", "is_background": false, "is_dangerous": true}`,
								},
							},
						},
					},
				},
			},
			{
				Choices: []llm.Choice{
					{
						Content: "工具调用超时，已处理完成",
					},
				},
			},
		},
	}

	cfg := ExecutorConfig{
		SystemPrompt: "You are a test agent.",
		UserInput:    "Run a bash command.",
		Surface:      tools.NewSurfaceFromAdapters("test-agent", blockingAdapter),
		LLM:          mock,
		MaxSteps:     3,
		StopOnFinish: true,
		AgentName:    "timeout-test",
	}

	result, err := RunAgentLoop(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunAgentLoop returned error: %v", err)
	}

	// 验证结果包含超时信息
	if result.Text == "" {
		t.Error("Expected non-empty result text, got empty")
	}

	// 验证历史中包含了工具超时信息
	foundTimeout := false
	for _, msg := range result.History {
		if msg.Role == llm.RoleTool && msg.ToolName == "run_bash" {
			if !strings.Contains(msg.Content, "timed out") {
				t.Errorf("Expected tool result to contain 'timed out', got: %s", msg.Content)
			}
			foundTimeout = true
			t.Logf("Tool result content: %s", msg.Content)
		}
	}

	if !foundTimeout {
		t.Error("Expected tool call result in history with timeout message, but none found")
	}
}

// TestRunAgentLoop_ToolCallNormal 测试正常工具调用（不超时）
// 验证当工具正常返回时，系统能正确处理结果
func TestRunAgentLoop_ToolCallNormal(t *testing.T) {
	// 使用较长的 context 超时
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// 创建快速返回的 adapter
	fastAdapter := newFastAdapter("get_weather", "get weather information", "Sunny, 25°C")

	// 创建 mock LLM，返回 tool_call 后返回最终文本
	mock := &mockLLM{
		responses: []*llm.Response{
			{
				Choices: []llm.Choice{
					{
						ToolCalls: []llm.ToolCall{
							{
								ID:   "call_weather_test",
								Type: "function",
								Function: llm.FunctionCall{
									Name:      "get_weather",
									Arguments: `{"location": "Beijing"}`,
								},
							},
						},
					},
				},
			},
			{
				Choices: []llm.Choice{
					{
						Content: "北京天气晴朗，气温25摄氏度。",
					},
				},
			},
		},
	}

	cfg := ExecutorConfig{
		SystemPrompt: "You are a helpful weather assistant.",
		UserInput:    "What's the weather in Beijing?",
		Surface:      tools.NewSurfaceFromAdapters("test-agent", fastAdapter),
		LLM:          mock,
		MaxSteps:     3,
		StopOnFinish: true,
		AgentName:    "normal-test",
	}

	result, err := RunAgentLoop(ctx, cfg)
	if err != nil {
		t.Fatalf("RunAgentLoop returned error: %v", err)
	}

	// 验证最终文本
	if result.Text != "北京天气晴朗，气温25摄氏度。" {
		t.Errorf("Expected final text '北京天气晴朗，气温25摄氏度。', got: %s", result.Text)
	}

	// 验证历史中包含工具调用和结果
	foundToolCall := false
	foundToolResult := false
	for _, msg := range result.History {
		if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) > 0 {
			foundToolCall = true
			if msg.ToolCalls[0].Function.Name != "get_weather" {
				t.Errorf("Expected tool call name 'get_weather', got: %s", msg.ToolCalls[0].Function.Name)
			}
		}
		if msg.Role == llm.RoleTool && msg.ToolName == "get_weather" {
			foundToolResult = true
			if !strings.Contains(msg.Content, "Sunny") {
				t.Errorf("Expected tool result to contain 'Sunny', got: %s", msg.Content)
			}
		}
	}

	if !foundToolCall {
		t.Error("Expected tool call in history")
	}
	if !foundToolResult {
		t.Error("Expected tool result in history")
	}
}

// TestRunAgentLoop_MultipleToolCalls 测试多个工具调用场景
// 验证当一个工具超时时，其他工具仍能正常执行
func TestRunAgentLoop_MultipleToolCalls(t *testing.T) {
	// 用 executor 自己的超时 seam：阻塞工具在 50ms 内超时，快速工具不受影响
	restoreToolTimeout(t, 50*time.Millisecond)

	// 创建 blocking adapter：模拟某个工具卡死
	blockingAdapter := newBlockingAdapter("slow_tool", "a slow tool that will timeout")

	// 创建快速返回的 adapter
	fastAdapter := newFastAdapter("fast_tool", "a fast tool", "Fast result: OK")

	// 创建 mock LLM：
	// 第1次：返回两个 tool_call（一个阻塞，一个快速）
	// 第2次：返回最终文本
	mock := &mockLLM{
		responses: []*llm.Response{
			{
				Choices: []llm.Choice{
					{
						ToolCalls: []llm.ToolCall{
							{
								ID:   "call_slow_tool",
								Type: "function",
								Function: llm.FunctionCall{
									Name:      "slow_tool",
									Arguments: `{"param": "test"}`,
								},
							},
							{
								ID:   "call_fast_tool",
								Type: "function",
								Function: llm.FunctionCall{
									Name:      "fast_tool",
									Arguments: `{"param": "test"}`,
								},
							},
						},
					},
				},
			},
			{
				Choices: []llm.Choice{
					{
						Content: "处理完成，fast_tool 返回正常结果",
					},
				},
			},
		},
	}

	cfg := ExecutorConfig{
		SystemPrompt: "You are a test agent.",
		UserInput:    "Run multiple tools.",
		Surface:      tools.NewSurfaceFromAdapters("test-agent", blockingAdapter, fastAdapter),
		LLM:          mock,
		MaxSteps:     3,
		StopOnFinish: true,
		AgentName:    "multiple-test",
	}

	result, err := RunAgentLoop(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunAgentLoop returned error: %v", err)
	}

	// 验证最终文本
	if !strings.Contains(result.Text, "fast_tool") {
		t.Errorf("Expected final text to mention 'fast_tool', got: %s", result.Text)
	}

	// 验证历史中包含两种工具结果
	foundTimeout := false
	foundFastResult := false
	for _, msg := range result.History {
		if msg.Role == llm.RoleTool {
			t.Logf("Tool message: name=%s, content=%s", msg.ToolName, msg.Content)
			if msg.ToolName == "slow_tool" {
				if !strings.Contains(msg.Content, "timed out") {
					t.Errorf("Expected slow_tool result to contain 'timed out', got: %s", msg.Content)
				}
				foundTimeout = true
			}
			if msg.ToolName == "fast_tool" {
				if !strings.Contains(msg.Content, "OK") {
					t.Errorf("Expected fast_tool result to contain 'OK', got: %s", msg.Content)
				}
				foundFastResult = true
			}
		}
	}

	if !foundTimeout {
		t.Error("Expected slow_tool timeout result in history")
	}
	if !foundFastResult {
		t.Error("Expected fast_tool result in history")
	}
}

// TestRunAgentLoop_NoToolCalls 测试无工具调用的纯文本交互
func TestRunAgentLoop_NoToolCalls(t *testing.T) {
	ctx := context.Background()

	// 创建 mock LLM，直接返回文本
	mock := &mockLLM{
		responses: []*llm.Response{
			{
				Choices: []llm.Choice{
					{
						Content: "Hello, this is a direct response.",
					},
				},
			},
		},
	}

	cfg := ExecutorConfig{
		SystemPrompt: "You are a test agent.",
		UserInput:    "Say hello.",
		Surface:      tools.NewSurfaceFromAdapters("test-agent"), // 没有适配器
		LLM:          mock,
		MaxSteps:     3,
		StopOnFinish: true,
		AgentName:    "no-tool-test",
	}

	result, err := RunAgentLoop(ctx, cfg)
	if err != nil {
		t.Fatalf("RunAgentLoop returned error: %v", err)
	}

	if result.Text != "Hello, this is a direct response." {
		t.Errorf("Expected 'Hello, this is a direct response.', got: %s", result.Text)
	}

	// 验证历史中只有 system、user 和 assistant 消息
	if len(result.History) != 3 {
		t.Errorf("Expected 3 messages in history, got: %d", len(result.History))
	}
}

// TestRunAgentLoop_ContextCancellation 测试 context 取消后的行为
func TestRunAgentLoop_ContextCancellation(t *testing.T) {
	// 创建一个立即取消的 context
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消

	blockingAdapter := newBlockingAdapter("run_bash", "run bash command")

	mock := &mockLLM{
		responses: []*llm.Response{
			{
				Choices: []llm.Choice{
					{
						ToolCalls: []llm.ToolCall{
							{
								ID:   "call_cancel_test",
								Type: "function",
								Function: llm.FunctionCall{
									Name:      "run_bash",
									Arguments: `{"command": "echo hello"}`,
								},
							},
						},
					},
				},
			},
		},
	}

	cfg := ExecutorConfig{
		SystemPrompt: "You are a test agent.",
		UserInput:    "Run a command.",
		Surface:      tools.NewSurfaceFromAdapters("test-agent", blockingAdapter),
		LLM:          mock,
		MaxSteps:     3,
		StopOnFinish: true,
		AgentName:    "cancel-test",
	}

	// context 已取消，工具调用应该立即失败
	_, err := RunAgentLoop(ctx, cfg)
	if err == nil {
		t.Error("Expected error due to context cancellation, got nil")
	}
}

// TestRunAgentLoop_MaxStepsExceeded 测试超出最大步骤限制
func TestRunAgentLoop_MaxStepsExceeded(t *testing.T) {
	ctx := context.Background()

	// 创建 mock LLM，一直返回 tool_call，永不返回文本
	mock := &mockLLM{
		responses: []*llm.Response{
			{
				Choices: []llm.Choice{
					{
						ToolCalls: []llm.ToolCall{
							{
								ID:   "call_loop",
								Type: "function",
								Function: llm.FunctionCall{
									Name:      "get_weather",
									Arguments: `{}`,
								},
							},
						},
					},
				},
			},
			{
				Choices: []llm.Choice{
					{
						ToolCalls: []llm.ToolCall{
							{
								ID:   "call_loop2",
								Type: "function",
								Function: llm.FunctionCall{
									Name:      "get_weather",
									Arguments: `{}`,
								},
							},
						},
					},
				},
			},
			{
				Choices: []llm.Choice{
					{
						ToolCalls: []llm.ToolCall{
							{
								ID:   "call_loop3",
								Type: "function",
								Function: llm.FunctionCall{
									Name:      "get_weather",
									Arguments: `{}`,
								},
							},
						},
					},
				},
			},
		},
	}

	fastAdapter := newFastAdapter("get_weather", "get weather", "result")

	cfg := ExecutorConfig{
		SystemPrompt: "You are a test agent.",
		UserInput:    "Keep asking weather.",
		Surface:      tools.NewSurfaceFromAdapters("test-agent", fastAdapter),
		LLM:          mock,
		MaxSteps:     3,
		StopOnFinish: false,
		AgentName:    "max-steps-test",
	}

	_, err := RunAgentLoop(ctx, cfg)
	if err == nil {
		t.Error("Expected error for max steps exceeded, got nil")
	}
	if !strings.Contains(err.Error(), "max steps") {
		t.Errorf("Expected error to mention 'max steps', got: %v", err)
	}
}

// TestRunAgentLoop_NilUsageEstimatesTokens 测试当 LLM 返回 Usage==nil 时，
// ai_response 事件仍发布带估算 usage 的 metadata（确保 dashboard 能统计子 agent token）
func TestRunAgentLoop_NilUsageEstimatesTokens(t *testing.T) {
	ctx := context.Background()

	// Mock LLM 返回 Usage==nil（模拟本地模型不返回 usage）
	mock := &mockLLM{
		responses: []*llm.Response{
			{
				Choices: []llm.Choice{
					{
						Content: "这是一条测试回复内容",
					},
				},
				// Usage 为 nil，模拟本地模型
			},
		},
	}

	// 使用 MessageDispatcher + legacy consumer 捕获事件
	dispatcher := messaging.NewMessageDispatcher(10000)
	consumer := &eventCapturingConsumer{arrived: make(chan struct{}, 64)}
	dispatcher.RegisterConsumer(consumer)
	publisher := messaging.NewMessagePublisher(dispatcher)
	defer dispatcher.Shutdown()

	cfg := ExecutorConfig{
		SystemPrompt: "You are a test agent.",
		UserInput:    "Say hello.",
		Surface:      tools.NewSurfaceFromAdapters("test-agent"),
		LLM:          mock,
		MaxSteps:     3,
		StopOnFinish: true,
		AgentName:    "test-agent",
		Publisher:    publisher,
	}

	_, err := RunAgentLoop(ctx, cfg)
	if err != nil {
		t.Fatalf("RunAgentLoop returned error: %v", err)
	}

	// dispatcher 在独立 goroutine 上回调 consumer：等到事件出现再断言，而不是睡固定的 50ms
	ev := consumer.waitFor("ai_response", 2*time.Second)
	if ev == nil {
		t.Fatal("Expected ai_response event to be published with usage metadata")
	}

	// 验证发布了 ai_response 事件且带 usage metadata
	if ev.Metadata == nil {
		t.Fatal("ai_response event should have non-nil metadata")
	}
	usageData, ok := ev.Metadata["usage"]
	if !ok {
		t.Fatal("ai_response event metadata should contain 'usage' key")
	}
	usageMap, ok := usageData.(map[string]interface{})
	if !ok {
		t.Fatalf("usage should be a map, got %T", usageData)
	}
	// 验证估算值 > 0
	promptTokens, ok := usageMap["prompt_tokens"]
	if !ok {
		t.Fatal("usage should contain 'prompt_tokens'")
	}
	var promptFloat float64
	switch v := promptTokens.(type) {
	case float64:
		promptFloat = v
	case int:
		promptFloat = float64(v)
	default:
		t.Fatalf("prompt_tokens should be float64 or int, got %T", promptTokens)
	}
	if promptFloat <= 0 {
		t.Errorf("prompt_tokens should be > 0, got %v", promptTokens)
	}
	completionTokens, ok := usageMap["completion_tokens"]
	if !ok {
		t.Fatal("usage should contain 'completion_tokens'")
	}
	var completionFloat float64
	switch v := completionTokens.(type) {
	case float64:
		completionFloat = v
	case int:
		completionFloat = float64(v)
	default:
		t.Fatalf("completion_tokens should be float64 or int, got %T", completionTokens)
	}
	if completionFloat <= 0 {
		t.Errorf("completion_tokens should be > 0, got %v", completionTokens)
	}
	// 验证 estimated 标记
	estimated, ok := usageMap["estimated"]
	if !ok {
		t.Fatal("usage should contain 'estimated' key")
	}
	if estimatedBool, ok := estimated.(bool); !ok || !estimatedBool {
		t.Errorf("estimated should be true, got %v", estimated)
	}
}

// eventCapturingConsumer 捕获所有事件，用于测试。
// Consume 由 dispatcher 的 goroutine 调用，读写共享切片必须过锁。
type eventCapturingConsumer struct {
	mu      sync.Mutex
	events  []*messaging.MessageEvent
	arrived chan struct{}
}

func (c *eventCapturingConsumer) Consume(event *messaging.MessageEvent) error {
	c.mu.Lock()
	c.events = append(c.events, event)
	c.mu.Unlock()
	select {
	case c.arrived <- struct{}{}:
	default:
	}
	return nil
}

// waitFor 等到第一个指定类型的事件；超时返回 nil（不静默通过，调用方按缺失处理）。
func (c *eventCapturingConsumer) waitFor(evType string, timeout time.Duration) *messaging.MessageEvent {
	deadline := time.Now().Add(timeout)
	for {
		c.mu.Lock()
		for _, ev := range c.events {
			if string(ev.Type) == evType {
				c.mu.Unlock()
				return ev
			}
		}
		c.mu.Unlock()
		if time.Now().After(deadline) {
			return nil
		}
		select {
		case <-c.arrived:
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// ─── Rollout 终止事件 ─────────────────────────────────────────────────────────

// TestRunAgentLoop_WritesTerminalEventWithoutOnAgentExit 回归：终止事件必须无条件落盘。
//
// task_complete/turn_aborted 的写入此前被包在 `cfg.OnAgentExit != nil` 分支内，未挂该
// hook 的运行一条都不写——存量 rollout 里 146 个可读文件有 89 个因此缺失终止事件，
// 恢复侧无从判断会话是正常结束还是被切断（RolloutInfo.FinishState 只能退报 Unknown）。
func TestRunAgentLoop_WritesTerminalEventWithoutOnAgentExit(t *testing.T) {
	w, err := memory.NewRolloutWriter("terminal-event-agent", "task-terminal-event", "terminal-event-test")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	path := w.FilePath()
	ctx := memory.WithRolloutWriter(context.Background(), w)

	cfg := ExecutorConfig{
		SystemPrompt: "You are a test agent.",
		UserInput:    "say something",
		LLM:          &mockLLM{responses: []*llm.Response{newTextResponse("done")}},
		MaxSteps:     2,
		StopOnFinish: true,
		AgentName:    "terminal-event-agent",
		// OnAgentExit 故意不设置：这正是此前漏写终止事件的场景
	}
	_, runErr := RunAgentLoop(ctx, cfg)
	w.Close()
	if runErr != nil {
		t.Fatalf("RunAgentLoop: %v", runErr)
	}

	_, info, err := memory.ReadRolloutMemory(path, memory.DefaultRolloutReadOptions())
	if err != nil {
		t.Fatalf("ReadRolloutMemory: %v", err)
	}
	if !info.Finished {
		t.Errorf("task_complete 未落盘: finished=%v aborted=%v state=%v",
			info.Finished, info.Aborted, info.FinishState())
	}
	if got := info.FinishState(); got != memory.FinishComplete {
		t.Errorf("FinishState = %v, want complete", got)
	}
}

// TestRunAgentLoop_RecordsAbortedOnMaxSteps 回归：跑满 max steps 的运行必须记
// turn_aborted，而不是 task_complete。
//
// 终止分类此前只看 agentErr（仅覆盖 panic 与 OnAgentExit hook 失败）；步数耗尽走的是
// 正常 return + retErr 路径，于是被误记成"完成"。恢复侧 RolloutInfo.FinishState 据此
// 把中断会话判为 FinishComplete，TUI 因此不再提示断点续跑。
func TestRunAgentLoop_RecordsAbortedOnMaxSteps(t *testing.T) {
	w, err := memory.NewRolloutWriter("max-steps-agent", "task-max-steps", "terminal-event-test")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	path := w.FilePath()
	ctx := memory.WithRolloutWriter(context.Background(), w)

	toolResp := func(id string) *llm.Response {
		return &llm.Response{Choices: []llm.Choice{{ToolCalls: []llm.ToolCall{{
			ID:   id,
			Type: "function",
			Function: llm.FunctionCall{
				Name:      "echo_tool",
				Arguments: `{}`,
			},
		}}}}}
	}

	cfg := ExecutorConfig{
		SystemPrompt: "You are a test agent.",
		UserInput:    "keep working",
		Surface:      tools.NewSurfaceFromAdapters("test-agent", newFastAdapter("echo_tool", "echo", "OK")),
		LLM:          &mockLLM{responses: []*llm.Response{toolResp("c1"), toolResp("c2")}},
		MaxSteps:     2,
		AgentName:    "max-steps-agent",
	}
	_, runErr := RunAgentLoop(ctx, cfg)
	w.Close()

	if runErr == nil || !strings.Contains(runErr.Error(), "exceeded max steps") {
		t.Fatalf("RunAgentLoop error = %v, want exceeded max steps", runErr)
	}

	_, info, err := memory.ReadRolloutMemory(path, memory.DefaultRolloutReadOptions())
	if err != nil {
		t.Fatalf("ReadRolloutMemory: %v", err)
	}
	if got := info.FinishState(); got != memory.FinishInterrupted {
		t.Errorf("FinishState = %v, want interrupted (finished=%v aborted=%v)",
			got, info.Finished, info.Aborted)
	}
	if info.Steps != 2 || info.MaxSteps != 2 {
		t.Errorf("Steps = %d MaxSteps = %d, want 2/2", info.Steps, info.MaxSteps)
	}
	if !strings.Contains(info.AbortReason, "exceeded max steps") {
		t.Errorf("AbortReason = %q, want contains %q", info.AbortReason, "exceeded max steps")
	}
}
