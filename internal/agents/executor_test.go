package agents

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"codeactor/internal/llm"
	"codeactor/internal/tools"
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

// ─── Test Cases ──────────────────────────────────────────────────────────────

// TestRunAgentLoop_ToolCallTimeout 测试工具调用超时保护机制
// 验证当工具调用超时时，系统能正确返回超时错误信息
func TestRunAgentLoop_ToolCallTimeout(t *testing.T) {
	// 创建一个短超时的 context（模拟 executor 的 60 秒超时被触发）
	// 由于 executor 内部有 60 秒超时，我们需要一个更短的父 context
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

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
		Adapters:     []*tools.Adapter{blockingAdapter},
		LLM:          mock,
		MaxSteps:     3,
		StopOnFinish: true,
		AgentName:    "timeout-test",
	}

	result, err := RunAgentLoop(ctx, cfg)
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
		Adapters:     []*tools.Adapter{fastAdapter},
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
	// 使用较长的 context 超时
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

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
		Adapters:     []*tools.Adapter{blockingAdapter, fastAdapter},
		LLM:          mock,
		MaxSteps:     3,
		StopOnFinish: true,
		AgentName:    "multiple-test",
	}

	result, err := RunAgentLoop(ctx, cfg)
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
		Adapters:     []*tools.Adapter{}, // 没有适配器
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
		Adapters:     []*tools.Adapter{blockingAdapter},
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
		Adapters:     []*tools.Adapter{fastAdapter},
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
