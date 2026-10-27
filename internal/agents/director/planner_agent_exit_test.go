package director

// planner_agent_exit_test.go — Director 的终止判据必须看**执行成败**，不看工具名。
//
// 与子 Agent 那一侧同一个缺陷：commitPlannerToolCall 只读 `tc.Function.Name == "agent_exit"`
// 就 writeComplete(StopKindAgentExit) 并返回 "agent_exit"，于是 p.cfg.Tools.Call 的 err
// （明明已经在手）被丢掉，一次参数校验失败的收尾变成
// PlanResult{Text: "Task completed successfully", StopReason: "agent_exit"} —— 账本上
// 任务成功了，实际那条 Error 只是被当成结果交回了模型。
// 修复后：err != nil 走该文件既有的普通工具错误路径（回执交模型、继续下一轮）。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/thinklink"
)

// flakyAgentExitTools 是让 agent_exit **第一次**报错的 ToolRunner。
// 不复用 planner_test.go 里的 fakePlannerTools.err：那份 err 对所有工具一律生效，
// 于是"失败后仍能正常收尾"这一半永远测不出来。
// state 非 nil 时模拟门面 Tools 包装闭包的委派检测语义（Run 开头会把 HasDelegated 归零，
// 所以想让散文收尾走 plain_text 出口，必须在本次 Run 里真的委派过一次）。
type flakyAgentExitTools struct {
	state   *RunState
	calls   []fakeToolCall
	failFor int // 前 N 次 agent_exit 报错，之后成功
	results map[string]string
}

func (t *flakyAgentExitTools) Specs() []llm.ToolDef {
	return []llm.ToolDef{
		{Type: "function", Function: llm.FunctionDef{Name: "agent_exit"}},
		{Type: "function", Function: llm.FunctionDef{Name: "delegate_coding"}},
	}
}

func (t *flakyAgentExitTools) Call(_ context.Context, name, argsJSON, callID string) (string, error) {
	t.calls = append(t.calls, fakeToolCall{Name: name, Args: argsJSON, CallID: callID})
	if t.state != nil && strings.HasPrefix(name, "delegate_") {
		t.state.HasDelegated = true
	}
	if name == "agent_exit" && t.failFor > 0 {
		t.failFor--
		return "", errors.New(`agent_exit: required parameter "reason" is missing (tool arguments were empty or malformed)`)
	}
	return t.results[name], nil
}

// newJournalPlanner 与 newTestPlanner 同形，但多挂一份真实 rollout writer：
// writeComplete 只在 writer 可用时才落账（planner_rollout.go 的 j.enabled()），
// 而"有没有写 complete"正是本组用例要直接观察的东西。
// writer 由 Planner.Run 的 defer 关闭，测试里不许再关一次。
func newJournalPlanner(maxSteps int, state *RunState, eng *fakePlannerLLM, tools ToolRunner, w *memory.RolloutWriter) *Planner {
	return NewPlanner(PlannerConfig{
		LLM:        eng,
		Journal:    thinklink.NewStore(0),
		Rollout:    w,
		Tools:      tools,
		Prompts:    func(_ context.Context, _ PromptInput) (string, error) { return "SYSTEM_PROMPT", nil },
		MaxSteps:   maxSteps,
		LLMTimeout: 30 * time.Second,
	}, state)
}

func newExitTestWriter(t *testing.T, taskID string) (*memory.RolloutWriter, string) {
	t.Helper()
	w, err := memory.NewRolloutWriter("director", taskID, "planner-agent-exit-test")
	if err != nil || !w.Enabled() {
		t.Skipf("rollout writer unavailable: %v", err)
	}
	return w, w.FilePath()
}

func readDirectorStopReason(t *testing.T, path string) memory.RolloutInfo {
	t.Helper()
	_, info, err := memory.ReadRolloutMemory(path, memory.DefaultRolloutReadOptions())
	if err != nil {
		t.Fatalf("ReadRolloutMemory: %v", err)
	}
	return info
}

// TestPlanner_AgentExitError_DoesNotWriteComplete 失败的 agent_exit 不许 writeComplete
// (StopKindAgentExit)、不许返回 "agent_exit"、不许交回 "Task completed successfully"。
// 模型第二次拿到的是那条 Error 回执，于是能改成散文收尾 —— 收尾原因必须是 plain_text。
func TestPlanner_AgentExitError_DoesNotWriteComplete(t *testing.T) {
	w, path := newExitTestWriter(t, "task-exit-err")

	eng := &fakePlannerLLM{steps: []fakeLLMStep{
		{resp: toolCallResp("call-1", "agent_exit", `{}`)},
		{resp: toolCallResp("call-2", "delegate_coding", `{"task":"keep going"}`)},
		{resp: textResp("FINAL_ANSWER")},
	}}
	state := newState(false) // Run 开头会归零，这里必须是"本轮还没委派"
	tools := &flakyAgentExitTools{
		state:   state,
		failFor: 1,
		results: map[string]string{
			"agent_exit":      `{"finished":true,"reason":"done"}`,
			"delegate_coding": "SUB_AGENT_RESULT",
		},
	}
	p := newJournalPlanner(5, state, eng, tools, w)

	res, err := p.Run(context.Background(), PlanInput{Input: "EXIT_ERR"})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil（失败的收尾只是普通工具错误）", err)
	}
	// 失败的那一次不许终止：模型还得再问两次（一次真委派、一次散文收尾）。
	if len(eng.requests) != 3 {
		t.Fatalf("LLM calls = %d, want 3: 失败的 agent_exit 被当成收尾（或被提醒拖满），循环没按预期继续", len(eng.requests))
	}
	if res.StopReason == "agent_exit" {
		t.Errorf("StopReason = %q, want 非 agent_exit（执行失败的收尾不许冒领）", res.StopReason)
	}
	if res.Text == "Task completed successfully" {
		t.Errorf("Text = %q, want 模型真正给出的答案", res.Text)
	}
	if res.StopReason != "plain_text" || res.Text != "FINAL_ANSWER" {
		t.Errorf("StopReason/Text = %q/%q, want plain_text/FINAL_ANSWER", res.StopReason, res.Text)
	}
	if len(tools.calls) != 2 || tools.calls[0].Name != "agent_exit" || tools.calls[1].Name != "delegate_coding" {
		t.Errorf("tool calls = %+v, want agent_exit 之后继续干活", tools.calls)
	}

	// 失败事实必须回到模型手里：第二轮请求里的 tool 回执就是那条 Error。
	var toolMsg string
	for _, m := range eng.requests[1] {
		if m.Role == llm.RoleTool {
			toolMsg = m.Content
		}
	}
	if !strings.Contains(toolMsg, "Error:") || !strings.Contains(toolMsg, "reason") {
		t.Errorf("第二轮的 tool 回执 = %q, want 带上 agent_exit 的错误", toolMsg)
	}

	// rollout 侧：complete 记的是 plain_text，不是 agent_exit。
	info := readDirectorStopReason(t, path)
	if info.StopReason == "agent_exit" {
		t.Errorf("rollout StopReason = %q, want plain_text（writeComplete 被失败的收尾冒领了）", info.StopReason)
	}
	if info.StopReason != "plain_text" {
		t.Errorf("rollout StopReason = %q, want %q", info.StopReason, "plain_text")
	}
}

// TestPlanner_AgentExitSuccess_WritesCompleteAndStops 反向夹具：合法 agent_exit 必须
// **照旧**当步收尾。没有这条，"agent_exit 永不收尾"也能让上一条用例变绿。
func TestPlanner_AgentExitSuccess_WritesCompleteAndStops(t *testing.T) {
	w, path := newExitTestWriter(t, "task-exit-ok")

	eng := &fakePlannerLLM{steps: []fakeLLMStep{
		{resp: toolCallResp("call-1", "agent_exit", `{"reason":"task done"}`)},
		{resp: textResp("SHOULD_NOT_BE_REACHED")},
	}}
	tools := &flakyAgentExitTools{
		failFor: 0,
		results: map[string]string{"agent_exit": `{"finished":true,"reason":"task done"}`},
	}
	p := newJournalPlanner(5, newState(true), eng, tools, w)

	res, err := p.Run(context.Background(), PlanInput{Input: "EXIT_OK"})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if res.StopReason != "agent_exit" {
		t.Errorf("StopReason = %q, want %q", res.StopReason, "agent_exit")
	}
	if res.Text != "Task completed successfully" {
		t.Errorf("Text = %q, want %q", res.Text, "Task completed successfully")
	}
	if len(eng.requests) != 1 {
		t.Errorf("LLM calls = %d, want 1（成功的收尾必须当步结束）", len(eng.requests))
	}
	if info := readDirectorStopReason(t, path); info.StopReason != "agent_exit" {
		t.Errorf("rollout StopReason = %q, want %q", info.StopReason, "agent_exit")
	}
}
