// loop_test.go — P0 优化 Step 7：旧执行内核 RunAgentLoop（executor.go）删除后，
// 将 executor_test.go 中 3 个有独特价值的测试适配为测 runSubAgentLoop /
// adapterToolRunner（复用原假 LLM/Adapter 构造思路）。
//
// 其余 4 个测试（MultipleToolCalls/NoToolCalls/ContextCancellation/
// MaxStepsExceeded）删除——由 director 包 Planner 侧测试等价覆盖：
//   - NoToolCalls         → director/planner_test.go TestPlannerRunPlainTextExit
//   - MaxStepsExceeded    → director/planner_test.go TestPlannerRunMaxSteps
//   - ContextCancellation → planner Run ctx.Done 路径 + TestPlannerRunLLMError /
//     director/recovery_test.go TestRetryWithBackoff_CancelContext
//   - MultipleToolCalls   → Planner 多工具执行循环
//     （director/planner_test.go TestPlannerRunToolResultBackfill）
package agents

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"yagent/internal/llm"
	"yagent/internal/messaging"
	"yagent/internal/tools"
)

// ─── Mock Engine（复用 executor_test.go 的构造思路）──────────────────────────

type mockLLM struct {
	responses []*llm.Response
	callCount int
}

func (m *mockLLM) GenerateContent(ctx context.Context, messages []llm.Message, toolDefs []llm.ToolDef, opts *llm.CallOptions) (*llm.Response, error) {
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

// ─── Test Helpers（复用 executor_test.go 的构造思路）─────────────────────────

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

// TestAdapterToolRunner_ToolCallTimeout 测试 adapterToolRunner 的工具调用超时
// 保护机制（原 TestRunAgentLoop_ToolCallTimeout 适配）：
//   - 非交互工具超时 → 超时错误转换为格式化 toolResult + nil error
//     （adapterToolRunner.Call 的超时错误转换语义）；
//   - 交互式工具（ask_user_for_help）豁免超时 → 不加 deadline，慢响应正常返回。
func TestAdapterToolRunner_ToolCallTimeout(t *testing.T) {
	t.Run("noninteractive timeout converts to toolResult", func(t *testing.T) {
		// blocking adapter：模拟 run_bash 卡死，等待 1s 超时触发
		r := &adapterToolRunner{
			adapters:  []*tools.Adapter{newBlockingAdapter("run_bash", "run bash command")},
			agentName: "timeout-test",
			timeout:   1 * time.Second,
		}

		result, err := r.Call(context.Background(), "run_bash", `{"command": "sleep 100", "is_background": false, "is_dangerous": true}`)
		if err != nil {
			t.Fatalf("expected nil error (timeout converts to toolResult), got: %v", err)
		}

		// 超时错误转换语义：toolResult 含格式化超时提示文本
		if !strings.Contains(result, "tool execution timed out after 1 seconds") {
			t.Errorf("expected result to contain 'tool execution timed out after 1 seconds', got: %s", result)
		}
	})

	t.Run("interactive tool exempt from timeout", func(t *testing.T) {
		// adapter 睡眠 150ms 后返回（> timeout=50ms），若未豁免将触发超时；
		// isInteractiveUserTool 豁免判断基于工具名（ask_user_for_help），
		// 豁免后仅保留 WithCancel、不加 deadline，慢响应应正常返回。
		slowInteractive := tools.NewAdapter(askUserToolName, "ask user for help",
			func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
				time.Sleep(150 * time.Millisecond)
				return "user replied", nil
			})
		r := &adapterToolRunner{
			adapters:  []*tools.Adapter{slowInteractive},
			agentName: "timeout-test",
			timeout:   50 * time.Millisecond,
		}

		result, err := r.Call(context.Background(), askUserToolName, `{"question": "ok?"}`)
		if err != nil {
			t.Fatalf("interactive tool should be exempt from timeout, got error: %v", err)
		}
		// Adapter.Call 对 ToolFunc 返回值做 JSON 序列化（字符串带引号），用 Contains 断言
		if !strings.Contains(result, "user replied") {
			t.Errorf("expected result to contain 'user replied', got: %s", result)
		}
	})
}

// TestAdapterToolRunner_ToolCallNormal 测试正常工具调用（不超时），
// 验证结果正确传递（原 TestRunAgentLoop_ToolCallNormal 适配）。
func TestAdapterToolRunner_ToolCallNormal(t *testing.T) {
	r := &adapterToolRunner{
		adapters:  []*tools.Adapter{newFastAdapter("get_weather", "get weather information", "Sunny, 25°C")},
		agentName: "normal-test",
		timeout:   5 * time.Second,
	}

	result, err := r.Call(context.Background(), "get_weather", `{"location": "Beijing"}`)
	if err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	// Adapter.Call 对 ToolFunc 返回值做 JSON 序列化（字符串带引号），用 Contains 断言
	if !strings.Contains(result, "Sunny, 25°C") {
		t.Errorf("expected result to contain 'Sunny, 25°C', got: %s", result)
	}
}

// TestRunSubAgentLoop_NilUsageEstimatesTokens 测试当 LLM 返回 Usage==nil 时，
// ai_response 事件仍发布带估算 usage 的 metadata（确保 dashboard 能统计子 agent
// token；原 TestRunAgentLoop_NilUsageEstimatesTokens 适配——验证 EstimateTokensFn
// 注入路径经 runSubAgentLoop → Planner 正常工作、不 panic）。
func TestRunSubAgentLoop_NilUsageEstimatesTokens(t *testing.T) {
	ctx := context.Background()

	// Mock LLM 返回 Usage==nil（模拟本地模型不返回 usage）。
	// 注意：Planner 有 ForceDelegation 检测——未委派 agent 且纯文本结束会被注入
	// 强制提醒（上限 maxNonDelegationPrompts=3），第 4 次纯文本到达上限后才放行，
	// 故需要 4 个响应、MaxSteps≥4；4 次响应均走无 usage 估算路径。
	replyContent := "这是一条测试回复内容"
	mock := &mockLLM{
		responses: []*llm.Response{
			{Choices: []llm.Choice{{Content: replyContent}}},
			{Choices: []llm.Choice{{Content: replyContent}}},
			{Choices: []llm.Choice{{Content: replyContent}}},
			{Choices: []llm.Choice{{Content: replyContent}}},
			// Usage 均为 nil，模拟本地模型
		},
	}

	// 使用 MessageDispatcher + legacy consumer 捕获事件
	dispatcher := messaging.NewMessageDispatcher(10000)
	var capturedEvents []*messaging.MessageEvent
	dispatcher.RegisterConsumer(&eventCapturingConsumer{events: &capturedEvents})
	publisher := messaging.NewMessagePublisher(dispatcher)
	defer dispatcher.Shutdown()

	cfg := SubAgentLoopConfig{
		SystemPrompt: "You are a test agent.",
		UserInput:    "Say hello.",
		Adapters:     []*tools.Adapter{},
		LLM:          mock,
		MaxSteps:     6, // ForceDelegation 提醒 3 次 + 第 4 次放行，≥4 即可
		StopOnFinish: true,
		AgentName:    "test-agent",
		Publisher:    publisher,
	}

	_, err := runSubAgentLoop(ctx, cfg)
	if err != nil {
		t.Fatalf("runSubAgentLoop returned error: %v", err)
	}

	// Give the dispatcher consumer goroutine time to process events
	time.Sleep(50 * time.Millisecond)

	// 验证发布了 ai_response 事件且带 usage metadata
	found := false
	for _, ev := range capturedEvents {
		if ev.Type == "ai_response" {
			found = true
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
			break
		}
	}
	if !found {
		t.Fatal("Expected ai_response event to be published with usage metadata")
	}
}

// eventCapturingConsumer 捕获所有事件，用于测试
type eventCapturingConsumer struct {
	events *[]*messaging.MessageEvent
}

func (c *eventCapturingConsumer) Consume(event *messaging.MessageEvent) error {
	*c.events = append(*c.events, event)
	return nil
}
