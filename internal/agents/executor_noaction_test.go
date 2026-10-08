package agents

import (
	"context"
	"strings"
	"testing"

	"yagent/internal/llm"
	"yagent/internal/thinklink"
	"yagent/internal/tools"
)

// ─── NoActionRetry 判定 characterization 测试（方案C·批次2）───────────────────
//
// 覆盖三种回复形态的行为矩阵（设计文档 6.1 executor 层）：
//   - 纯文本（无工具调用）→ NoActionRetry 提醒 + 计数增加（行为与现状一致）；
//   - 仅 TodoWrite（无其他工具调用）→ 注入 TodoWrite 专属提醒 + 计数增加（不重置）；
//   - TodoWrite + 真实其他工具 → 计数重置，无提醒；
//   - executor 不再从自由文本提取 T&P 块（T&P 文本不再写入 thinklink）。

// todoWriteToolCall 构造一个合法的 TodoWrite tool call（参数与 tools.json schema 一致）。
func todoWriteToolCall(id string) llm.ToolCall {
	return llm.ToolCall{
		ID:   id,
		Type: "function",
		Function: llm.FunctionCall{
			Name: "TodoWrite",
			Arguments: `{"todos":[{"content":"Fix the parser bug",` +
				`"active_form":"Fixing the parser bug","status":"in_progress"}]}`,
		},
	}
}

// textOnlyResponse 构造无工具调用的纯文本回复。
func textOnlyResponse(content string) *llm.Response {
	return &llm.Response{
		Choices: []llm.Choice{{Content: content}},
	}
}

// countReminders 统计 history 中 user 角色的 SYSTEM REMINDER 提醒消息数量。
// （history 首条 user 消息为 cfg.UserInput，不含 SYSTEM REMINDER，不会误计。）
func countReminders(history []llm.Message) int {
	n := 0
	for _, msg := range history {
		if msg.Role == llm.RoleUser && strings.Contains(msg.Content, "SYSTEM REMINDER") {
			n++
		}
	}
	return n
}

// TestRunAgentLoop_TextOnlyNoActionReminder 纯文本（无工具调用）回复的
// characterization：NoActionRetry 提醒 + 计数增加，行为与现状一致；
// 提醒文案已从 "a `## Thought & Plan` block alone does not constitute action"
// 更新为 "a `TodoWrite` call alone does not constitute task progress"。
func TestRunAgentLoop_TextOnlyNoActionReminder(t *testing.T) {
	ctx := context.Background()

	mock := &mockLLM{
		responses: []*llm.Response{
			textOnlyResponse("I will now do the work."),
			textOnlyResponse("Doing the work now."),
			textOnlyResponse("All done."),
		},
	}

	cfg := ExecutorConfig{
		SystemPrompt:       "You are a test agent.",
		UserInput:          "Do the work.",
		Adapters:           []*tools.Adapter{},
		LLM:                mock,
		MaxSteps:           10,
		NoActionRetryLimit: 2,
		AgentName:          "noaction-test",
	}

	result, err := RunAgentLoop(ctx, cfg)
	if err != nil {
		t.Fatalf("RunAgentLoop returned error: %v", err)
	}

	// 轮次序列：纯文本 → 提醒(计数1) → 纯文本 → 提醒(计数2) → 纯文本(达上限) → 返回
	if mock.callCount != 3 {
		t.Errorf("mockLLM callCount = %d, want 3", mock.callCount)
	}
	if result.Text != "All done." {
		t.Errorf("result.Text = %q, want %q", result.Text, "All done.")
	}
	if reminders := countReminders(result.History); reminders != 2 {
		t.Errorf("reminder count = %d, want 2", reminders)
	}
	for _, msg := range result.History {
		if msg.Role == llm.RoleUser && strings.Contains(msg.Content, "SYSTEM REMINDER") {
			if !strings.Contains(msg.Content, "a `TodoWrite` call alone does not constitute task progress") {
				t.Errorf("reminder should mention TodoWrite progress semantics, got: %s", msg.Content)
			}
			if strings.Contains(msg.Content, "Thought & Plan") {
				t.Errorf("reminder should not mention Thought & Plan blocks, got: %s", msg.Content)
			}
		}
	}
}

// TestRunAgentLoop_TodoWriteOnlyCountsAsNoProgress 仅 TodoWrite（无其他工具调用）
// 的轮次视为无任务进展：注入 TodoWrite 专属提醒且 NoActionRetry 计数增加（不重置）。
// 若计数被重置，后续纯文本轮将多出一次提醒，mockLLM 会因 responses 耗尽而报错。
func TestRunAgentLoop_TodoWriteOnlyCountsAsNoProgress(t *testing.T) {
	ctx := context.Background()
	store := thinklink.NewStore(0)
	todoAdapter := NewTodoWriteAdapter(store, nil)

	mock := &mockLLM{
		responses: []*llm.Response{
			{
				Choices: []llm.Choice{{
					ToolCalls: []llm.ToolCall{todoWriteToolCall("call_todo_1")},
				}},
			},
			textOnlyResponse("Working on it."),
			textOnlyResponse("Final answer."),
		},
	}

	cfg := ExecutorConfig{
		SystemPrompt:       "You are a test agent.",
		UserInput:          "Do the work.",
		Adapters:           []*tools.Adapter{todoAdapter},
		LLM:                mock,
		MaxSteps:           10,
		NoActionRetryLimit: 2,
		AgentName:          "todo-only-test",
	}

	result, err := RunAgentLoop(ctx, cfg)
	if err != nil {
		t.Fatalf("RunAgentLoop returned error: %v", err)
	}

	// 轮次序列：TodoWrite-only → 提醒(计数1) → 纯文本 → 提醒(计数2) → 纯文本(达上限) → 返回
	if mock.callCount != 3 {
		t.Errorf("mockLLM callCount = %d, want 3 (TodoWrite-only turn must NOT reset the count)", mock.callCount)
	}
	if result.Text != "Final answer." {
		t.Errorf("result.Text = %q, want %q", result.Text, "Final answer.")
	}
	if reminders := countReminders(result.History); reminders != 2 {
		t.Errorf("reminder count = %d, want 2", reminders)
	}

	// 第 1 条提醒为 TodoWrite 专属文案；第 2 条为纯文本提醒
	sawTodoOnlyReminder := false
	sawTextOnlyReminder := false
	for _, msg := range result.History {
		if msg.Role != llm.RoleUser || !strings.Contains(msg.Content, "SYSTEM REMINDER") {
			continue
		}
		if strings.Contains(msg.Content, "only called `TodoWrite` without any other tool calls") {
			sawTodoOnlyReminder = true
		}
		if strings.Contains(msg.Content, "without calling any tools") {
			sawTextOnlyReminder = true
		}
	}
	if !sawTodoOnlyReminder {
		t.Error("expected a TodoWrite-only reminder in history")
	}
	if !sawTextOnlyReminder {
		t.Error("expected a text-only reminder in history")
	}

	// TodoWrite 副作用生效：任务清单快照已写入 store
	todos := store.CurrentTodos()
	if len(todos) != 1 || todos[0].Content != "Fix the parser bug" {
		t.Errorf("TodoWrite side effect missing: current todos = %+v, want 1 item 'Fix the parser bug'", todos)
	}
}

// TestRunAgentLoop_TodoWritePlusRealToolResetsNoActionCount TodoWrite + 真实其他
// 工具混合的轮次视为有进展：NoActionRetry 计数重置，无提醒。若计数未重置，
// 后续纯文本轮会提前达到上限，mockLLM callCount 将为 4 而非 5。
func TestRunAgentLoop_TodoWritePlusRealToolResetsNoActionCount(t *testing.T) {
	ctx := context.Background()
	store := thinklink.NewStore(0)
	todoAdapter := NewTodoWriteAdapter(store, nil)
	weatherAdapter := newFastAdapter("get_weather", "get weather information", "Sunny")

	mixedResp := &llm.Response{
		Choices: []llm.Choice{{
			ToolCalls: []llm.ToolCall{
				todoWriteToolCall("call_todo_2"),
				{
					ID:   "call_weather_1",
					Type: "function",
					Function: llm.FunctionCall{
						Name:      "get_weather",
						Arguments: `{"location": "Beijing"}`,
					},
				},
			},
		}},
	}

	mock := &mockLLM{
		responses: []*llm.Response{
			textOnlyResponse("Let me check the weather and set up my task list."),
			mixedResp,
			textOnlyResponse("Checked the weather."),
			textOnlyResponse("Still working."),
			textOnlyResponse("Final answer."),
		},
	}

	cfg := ExecutorConfig{
		SystemPrompt:       "You are a test agent.",
		UserInput:          "Do the work.",
		Adapters:           []*tools.Adapter{todoAdapter, weatherAdapter},
		LLM:                mock,
		MaxSteps:           10,
		NoActionRetryLimit: 2,
		AgentName:          "todo-mixed-test",
	}

	result, err := RunAgentLoop(ctx, cfg)
	if err != nil {
		t.Fatalf("RunAgentLoop returned error: %v", err)
	}

	// 轮次序列：纯文本 → 提醒(计数1) → 混合(重置0) → 纯文本 → 提醒(计数1) →
	// 纯文本 → 提醒(计数2) → 纯文本(达上限) → 返回
	if mock.callCount != 5 {
		t.Errorf("mockLLM callCount = %d, want 5 (mixed turn must reset the count)", mock.callCount)
	}
	if result.Text != "Final answer." {
		t.Errorf("result.Text = %q, want %q", result.Text, "Final answer.")
	}
	if reminders := countReminders(result.History); reminders != 3 {
		t.Errorf("reminder count = %d, want 3", reminders)
	}

	// 混合轮次不注入 TodoWrite 专属提醒
	for _, msg := range result.History {
		if msg.Role == llm.RoleUser && strings.Contains(msg.Content, "SYSTEM REMINDER") {
			if strings.Contains(msg.Content, "only called `TodoWrite`") {
				t.Errorf("mixed turn must not produce a TodoWrite-only reminder, got: %s", msg.Content)
			}
		}
	}
}

// TestRunAgentLoop_NoThoughtPlanExtraction characterization：executor 不再调用
// compression.ExtractThoughtAndPlanBlocks——回复文本中的旧格式 T&P 块不再被
// 正则提取写入 thinklink（行为断言：store 中无 KindThoughtPlan 条目）。
func TestRunAgentLoop_NoThoughtPlanExtraction(t *testing.T) {
	ctx := context.Background()
	store := thinklink.NewStore(0)
	todoAdapter := NewTodoWriteAdapter(store, nil)

	// 含旧格式 T&P 块的纯文本回复（旧实现会被正则提取写入 thinklink）
	tpText := "## Thought & Plan\n### Thought Process\n* **Current Goal**: fake goal\n" +
		"### Plan Update\n* [ ] 1. fake step"
	mock := &mockLLM{
		responses: []*llm.Response{
			textOnlyResponse(tpText),
			textOnlyResponse("Still here."),
		},
	}

	cfg := ExecutorConfig{
		SystemPrompt:       "You are a test agent.",
		UserInput:          "Do the work.",
		Adapters:           []*tools.Adapter{todoAdapter},
		LLM:                mock,
		MaxSteps:           10,
		NoActionRetryLimit: 1,
		AgentName:          "tp-extract-test",
		UltimateThinkLink:  store,
	}

	if _, err := RunAgentLoop(ctx, cfg); err != nil {
		t.Fatalf("RunAgentLoop returned error: %v", err)
	}

	if got := store.Count(thinklink.KindThoughtPlan); got != 0 {
		t.Errorf("thinklink KindThoughtPlan count = %d, want 0 (T&P extraction removed)", got)
	}
}
