package agents

// executor_agent_exit_test.go — 子 Agent 循环的终止判据必须看**执行成败**，不看工具名。
//
// 缺陷：executor.go 的终止判定只读 `tc.Function.Name == "agent_exit"`，而 runToolCall
// 把 callErr 折成 "Error: %v" 字符串后就再也不往上返回失败事实。于是一次参数校验失败的
// agent_exit 会以 "Error: agent_exit: required parameter ..." 作为最终 Text 收尾，并被
// stopKind 记成 agent_exit 成功收尾 —— 任务产出静默丢失，而账本上说它成功了。
// 修复后：execErr != nil 不收尾，错误已经作为 tool 结果回到模型手里，让它自己重试；
// 反复失败由既有 MaxSteps 兜底（不新造第二套上限）。

import (
	"context"
	"strings"
	"testing"

	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/tools"
)

// newProductionAgentExit 用**生产**的 agent_exit 处理器而不是桩：它的失败形态（缺 reason
// → 返回 error）正是这条链上真实发生过的故障。换成永不失败的桩，测的就只是桩的行为。
func newProductionAgentExit(t *testing.T) *tools.Adapter {
	t.Helper()
	return tools.NewAdapter("agent_exit", "finish the run",
		tools.NewFlowControlTool(t.TempDir()).ExecuteAgentExit)
}

// agentExitCall 构造一次 agent_exit 调用（makeCall 把参数固定成 `{}`，这里要能分别给
// "缺 reason"与"合法"两种形态）。
func agentExitCall(id, args string) llm.ToolCall {
	return llm.ToolCall{
		ID:       id,
		Type:     "function",
		Function: llm.FunctionCall{Name: "agent_exit", Arguments: args},
	}
}

// readStopKind 关掉 writer 并把终止事件读回来（与 executor_stopkind_test.go 同一口径：
// 终止原因只有 rollout 里那份是恢复侧真正会读的东西）。
func readStopKind(t *testing.T, w *memory.RolloutWriter) memory.RolloutInfo {
	t.Helper()
	path := w.FilePath()
	if err := w.Close(); err != nil {
		t.Fatalf("close rollout: %v", err)
	}
	_, info, err := memory.ReadRolloutMemory(path, memory.DefaultRolloutReadOptions())
	if err != nil {
		t.Fatalf("ReadRolloutMemory: %v", err)
	}
	return info
}

// historyToolContents 取出 history 里的工具回执（模型下一步实际看到的东西）。
// 名字避开 toolResults：包里已有一份同名辅助（delegate_concurrency_test.go:116），
// 它读的是 ConversationMemory，口径不同，混用会静默改断言对象。
func historyToolContents(history []llm.Message) []string {
	var out []string
	for _, m := range history {
		if m.Role == llm.RoleTool {
			out = append(out, m.Content)
		}
	}
	return out
}

// TestExecutor_AgentExitToolError_DoesNotStopAsSuccess 失败的 agent_exit 不许收尾：
// 循环必须再问一次模型，让它看见错误并自行补一次合法收尾。
func TestExecutor_AgentExitToolError_DoesNotStopAsSuccess(t *testing.T) {
	w, err := memory.NewRolloutWriter("retry-exit-agent", "task-retry-exit", "agent-exit-test")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	ctx := memory.WithRolloutWriter(context.Background(), w)

	script := &mockLLM{responses: []*llm.Response{
		// 第 1 次：缺 reason → 生产处理器报错。
		newToolResponse(agentExitCall("c1", `{}`)),
		// 第 2 次：合法收尾。
		newToolResponse(agentExitCall("c2", `{"reason":"done"}`)),
	}}

	res, runErr := RunAgentLoop(ctx, ExecutorConfig{
		SystemPrompt: "sp",
		UserInput:    "ui",
		Surface:      tools.NewSurfaceFromAdapters("retry-exit-agent", newProductionAgentExit(t)),
		LLM:          script,
		MaxSteps:     4,
		StopOnFinish: true,
		AgentName:    "retry-exit-agent",
	})
	if runErr != nil {
		t.Fatalf("RunAgentLoop: %v", runErr)
	}
	if script.callCount < 2 {
		t.Errorf("LLM called %d times, want >=2: 失败的 agent_exit 被当成收尾，模型没机会重试", script.callCount)
	}
	if strings.HasPrefix(res.Text, "Error:") {
		t.Errorf("Text = %q, want 合法收尾的 JSON 而不是错误回执", res.Text)
	}
	if !strings.Contains(res.Text, `"finished":true`) || !strings.Contains(res.Text, "done") {
		t.Errorf("Text = %q, want the successful agent_exit payload", res.Text)
	}

	// 失败事实必须回到模型手里（tool 回执里那条 Error），否则"继续循环"只是空转。
	var sawError bool
	for _, c := range historyToolContents(res.History) {
		if strings.Contains(c, "Error:") && strings.Contains(c, "reason") {
			sawError = true
		}
	}
	if !sawError {
		t.Errorf("history = %v, want a tool result carrying the agent_exit error", historyToolContents(res.History))
	}

	if info := readStopKind(t, w); info.StopReason != StopKindAgentExit {
		t.Errorf("StopReason = %q, want %q（重试之后仍然要认领 agent_exit）",
			info.StopReason, StopKindAgentExit)
	}
}

// TestExecutor_AgentExitSuccess_StopsImmediately 反向夹具：合法收尾必须**立刻**停。
// 没有这条，"永远不因 agent_exit 收尾"也能让上一条测试变绿。
func TestExecutor_AgentExitSuccess_StopsImmediately(t *testing.T) {
	w, err := memory.NewRolloutWriter("clean-exit-agent", "task-clean-exit", "agent-exit-test")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	ctx := memory.WithRolloutWriter(context.Background(), w)

	script := &mockLLM{responses: []*llm.Response{
		newToolResponse(agentExitCall("c1", `{"reason":"done"}`)),
		newTextResponse("should never be reached"),
	}}

	res, runErr := RunAgentLoop(ctx, ExecutorConfig{
		SystemPrompt: "sp",
		UserInput:    "ui",
		Surface:      tools.NewSurfaceFromAdapters("clean-exit-agent", newProductionAgentExit(t)),
		LLM:          script,
		MaxSteps:     4,
		StopOnFinish: true,
		AgentName:    "clean-exit-agent",
	})
	if runErr != nil {
		t.Fatalf("RunAgentLoop: %v", runErr)
	}
	if script.callCount != 1 {
		t.Errorf("LLM called %d times, want 1（成功的 agent_exit 必须当步收尾）", script.callCount)
	}
	if !strings.Contains(res.Text, `"finished":true`) {
		t.Errorf("Text = %q, want the agent_exit payload", res.Text)
	}
	if info := readStopKind(t, w); info.StopReason != StopKindAgentExit {
		t.Errorf("StopReason = %q, want %q", info.StopReason, StopKindAgentExit)
	}
}

// TestExecutor_AgentExitKeepsFailing_BoundedByMaxSteps 一直失败的 agent_exit 由既有
// MaxSteps 兜底：不许死循环，也不许记成 agent_exit 成功收尾。
func TestExecutor_AgentExitKeepsFailing_BoundedByMaxSteps(t *testing.T) {
	w, err := memory.NewRolloutWriter("doomed-exit-agent", "task-doomed-exit", "agent-exit-test")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	ctx := memory.WithRolloutWriter(context.Background(), w)

	// 三次都是缺 reason 的 agent_exit，配 MaxSteps=3：耗尽后按 max_steps 结束。
	script := &mockLLM{responses: []*llm.Response{
		newToolResponse(agentExitCall("c1", `{}`)),
		newToolResponse(agentExitCall("c2", `{}`)),
		newToolResponse(agentExitCall("c3", `{}`)),
	}}

	_, runErr := RunAgentLoop(ctx, ExecutorConfig{
		SystemPrompt: "sp",
		UserInput:    "ui",
		Surface:      tools.NewSurfaceFromAdapters("doomed-exit-agent", newProductionAgentExit(t)),
		LLM:          script,
		MaxSteps:     3,
		StopOnFinish: true,
		AgentName:    "doomed-exit-agent",
	})
	if runErr == nil {
		t.Fatal("RunAgentLoop 返回 nil：反复失败的 agent_exit 被当成跑完了")
	}
	if !strings.Contains(runErr.Error(), "exceeded max steps") {
		t.Errorf("error = %v, want the MaxSteps bound (%q)", runErr, "exceeded max steps")
	}
	if script.callCount != 3 {
		t.Errorf("LLM called %d times, want 3 (= MaxSteps, 没有死循环也没有提前收尾)", script.callCount)
	}
	info := readStopKind(t, w)
	if info.StopReason == StopKindAgentExit {
		t.Errorf("StopReason = %q, want max_steps（失败的收尾不许冒领成功）", info.StopReason)
	}
	if info.StopReason != StopKindMaxSteps {
		t.Errorf("StopReason = %q, want %q", info.StopReason, StopKindMaxSteps)
	}
	if info.Finished {
		t.Errorf("Finished = true, want false：%+v", info)
	}
}
