// Package director — planner_hooks_test.go — P0-Step 2b：Planner.Run 新接线能力单测。
//
// 覆盖 P0-Step 2b 接线的 7 个新能力（PlannerConfig：StopOnFinish/SystemAsHuman/
// RepoContext/OnAgentStart/OnAgentExit/OnStepEnd/OnToolResult + PlanResult.History）。
// 复用 planner_test.go 的 fake 基建（包内可见：fakePlannerLLM/fakePlannerTools/
// newState/textResp/toolCallResp）；newTestPlanner 不含新 hooks 字段，故本文件
// 用 NewPlanner(PlannerConfig{...}, state) 直接构造（模式复制 newTestPlanner），
// 经 mutate 闭包注入新字段。脚本统一「delegate_sub_agent → 纯文本」避免强制
// 委派提醒干扰（除非场景本身以 agent_exit/echo_tool 结束）。
package director

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"yagent/internal/llm"
	"yagent/internal/thinklink"
)

// ─── 测试基建扩展 ────────────────────────────────────────────────────────────

// newHookTestPlanner 复制 newTestPlanner 模式构造（LLM/Publisher nil/Journal/
// Tools/Prompts 固定 "SYSTEM_PROMPT"/MaxSteps=5/LLMTimeout=30s），另经 mutate
// 闭包注入 P0-Step 2b 新字段（StopOnFinish/SystemAsHuman/RepoContext/hooks）。
func newHookTestPlanner(state *RunState, eng *fakePlannerLLM, tools ToolRunner, mutate func(*PlannerConfig)) *Planner {
	cfg := PlannerConfig{
		LLM:        eng,
		Journal:    thinklink.NewStore(0),
		Tools:      tools,
		Prompts:    func(_ context.Context, _ PromptInput) (string, error) { return "SYSTEM_PROMPT", nil },
		MaxSteps:   5,
		LLMTimeout: 30 * time.Second,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	return NewPlanner(cfg, state)
}

// exitHookRecorder 记录 OnAgentExit 调用次数与每次收到的 runErr。
type exitHookRecorder struct {
	calls   int
	runErrs []error
}

func (r *exitHookRecorder) hook(_ context.Context, runErr error) error {
	r.calls++
	r.runErrs = append(r.runErrs, runErr)
	return nil
}

// panicToolRunner 包装 fakePlannerTools：Call 记录后 panic（触发主循环 panic
// 路径，验证 Run 的 OnAgentExit defer recover）。
type panicToolRunner struct {
	inner *fakePlannerTools
}

func (t *panicToolRunner) Specs() []llm.ToolDef { return t.inner.Specs() }

func (t *panicToolRunner) Call(ctx context.Context, name, argsJSON string) (string, error) {
	_, _ = t.inner.Call(ctx, name, argsJSON) // 记录调用后 panic
	panic("tool exploded")
}

// historyHasRole 返回 history 中是否存在指定角色的消息。
func historyHasRole(history []llm.Message, role llm.Role) bool {
	for _, m := range history {
		if m.Role == role {
			return true
		}
	}
	return false
}

// ─── P0-Step 2b：SystemAsHuman / RepoContext ────────────────────────────────

// TestPlannerRunSystemAsHuman SystemAsHuman=true → 首次 LLM 请求无 RoleSystem
// 消息、system 内容（"SYSTEM_PROMPT"）出现在 RoleUser 消息中（mem==nil 时
// user input 也是 RoleUser，按内容区分）；对照 false → 首条消息 RoleSystem
// 且 Content=="SYSTEM_PROMPT"。
func TestPlannerRunSystemAsHuman(t *testing.T) {
	// 对照组：SystemAsHuman=false（零值）→ 首条消息 RoleSystem/SYSTEM_PROMPT
	engOff := &fakePlannerLLM{steps: []fakeLLMStep{
		{resp: toolCallResp("call-1", "delegate_sub_agent", `{"agent":"coder"}`)},
		{resp: textResp("OFF_ANSWER")},
	}}
	toolsOff := &fakePlannerTools{state: newState(false), results: map[string]string{"delegate_sub_agent": "OK"}}
	pOff := newHookTestPlanner(toolsOff.state, engOff, toolsOff, nil)
	resOff, err := pOff.Run(context.Background(), PlanInput{Input: "S2H_TASK"})
	if err != nil {
		t.Fatalf("SystemAsHuman=false: Run() error = %v, want nil", err)
	}
	if resOff.Text != "OFF_ANSWER" {
		t.Errorf("SystemAsHuman=false: Text = %q, want %q", resOff.Text, "OFF_ANSWER")
	}
	if len(engOff.requests) == 0 {
		t.Fatal("SystemAsHuman=false: no LLM requests recorded")
	}
	if first := engOff.requests[0]; len(first) == 0 || first[0].Role != llm.RoleSystem || first[0].Content != "SYSTEM_PROMPT" {
		t.Errorf("SystemAsHuman=false: first message = %+v, want RoleSystem/SYSTEM_PROMPT", first[0])
	}

	// SystemAsHuman=true → 请求中无 RoleSystem 消息；"SYSTEM_PROMPT" 以 RoleUser 出现
	engOn := &fakePlannerLLM{steps: []fakeLLMStep{
		{resp: toolCallResp("call-1", "delegate_sub_agent", `{"agent":"coder"}`)},
		{resp: textResp("ON_ANSWER")},
	}}
	toolsOn := &fakePlannerTools{state: newState(false), results: map[string]string{"delegate_sub_agent": "OK"}}
	pOn := newHookTestPlanner(toolsOn.state, engOn, toolsOn, func(c *PlannerConfig) { c.SystemAsHuman = true })
	if _, err := pOn.Run(context.Background(), PlanInput{Input: "S2H_TASK"}); err != nil {
		t.Fatalf("SystemAsHuman=true: Run() error = %v, want nil", err)
	}
	if len(engOn.requests) == 0 {
		t.Fatal("SystemAsHuman=true: no LLM requests recorded")
	}
	hasSystem, hasSystemAsUser := false, false
	for _, m := range engOn.requests[0] {
		if m.Role == llm.RoleSystem {
			hasSystem = true
		}
		if m.Role == llm.RoleUser && m.Content == "SYSTEM_PROMPT" {
			hasSystemAsUser = true
		}
	}
	if hasSystem {
		t.Errorf("SystemAsHuman=true: request #1 contains RoleSystem message, want none; messages = %+v", engOn.requests[0])
	}
	if !hasSystemAsUser {
		t.Errorf("SystemAsHuman=true: request #1 has no RoleUser message with content %q; messages = %+v", "SYSTEM_PROMPT", engOn.requests[0])
	}
}

// TestPlannerRunRepoContext RepoContext="REPO_SUMMARY" → 首条系统消息 Content
// 以 "\n\nREPO_SUMMARY" 结尾；空串 → Content=="SYSTEM_PROMPT"（原行为不变）。
func TestPlannerRunRepoContext(t *testing.T) {
	// RepoContext 非空 → system prompt 以 "\n\nREPO_SUMMARY" 结尾
	eng := &fakePlannerLLM{steps: []fakeLLMStep{
		{resp: toolCallResp("call-1", "delegate_sub_agent", `{"agent":"coder"}`)},
		{resp: textResp("CTX_ANSWER")},
	}}
	tools := &fakePlannerTools{state: newState(false), results: map[string]string{"delegate_sub_agent": "OK"}}
	p := newHookTestPlanner(tools.state, eng, tools, func(c *PlannerConfig) { c.RepoContext = "REPO_SUMMARY" })
	if _, err := p.Run(context.Background(), PlanInput{Input: "CTX_TASK"}); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if len(eng.requests) == 0 {
		t.Fatal("no LLM requests recorded")
	}
	first := eng.requests[0][0]
	if first.Role != llm.RoleSystem {
		t.Fatalf("first message role = %q, want %q", first.Role, llm.RoleSystem)
	}
	if !strings.HasSuffix(first.Content, "\n\nREPO_SUMMARY") {
		t.Errorf("RepoContext set: system content = %q, want suffix %q", first.Content, "\n\nREPO_SUMMARY")
	}
	if first.Content != "SYSTEM_PROMPT\n\nREPO_SUMMARY" {
		t.Errorf("RepoContext set: system content = %q, want exactly %q (PromptBuilder 输出 + \"\\n\\n\" + RepoContext)", first.Content, "SYSTEM_PROMPT\n\nREPO_SUMMARY")
	}

	// RepoContext 空串 → system content 保持原样
	engEmpty := &fakePlannerLLM{steps: []fakeLLMStep{
		{resp: toolCallResp("call-1", "delegate_sub_agent", `{"agent":"coder"}`)},
		{resp: textResp("EMPTY_ANSWER")},
	}}
	toolsEmpty := &fakePlannerTools{state: newState(false), results: map[string]string{"delegate_sub_agent": "OK"}}
	pEmpty := newHookTestPlanner(toolsEmpty.state, engEmpty, toolsEmpty, nil)
	if _, err := pEmpty.Run(context.Background(), PlanInput{Input: "CTX_TASK"}); err != nil {
		t.Fatalf("RepoContext empty: Run() error = %v, want nil", err)
	}
	if got := engEmpty.requests[0][0].Content; got != "SYSTEM_PROMPT" {
		t.Errorf("RepoContext empty: system content = %q, want %q", got, "SYSTEM_PROMPT")
	}
}

// ─── P0-Step 2b：OnAgentStart / OnAgentExit ─────────────────────────────────

// TestPlannerRunOnAgentStartFailure OnAgentStart 返回 error → Run 返回 error
// 含 "OnAgentStart hook failed"；OnAgentExit 恰被调 1 次且 runErr==nil（defer
// 先行注册，终止路径仍走 defer，此时无 panic → runErr 为 nil）；LLM 未被调用。
func TestPlannerRunOnAgentStartFailure(t *testing.T) {
	eng := &fakePlannerLLM{} // 断言：LLM 不被调用
	tools := &fakePlannerTools{}
	rec := &exitHookRecorder{}
	p := newHookTestPlanner(newState(false), eng, tools, func(c *PlannerConfig) {
		c.OnAgentStart = func(_ context.Context) error { return errors.New("start boom") }
		c.OnAgentExit = rec.hook
	})

	res, err := p.Run(context.Background(), PlanInput{Input: "START_FAIL_TASK"})

	if err == nil || !strings.Contains(err.Error(), "OnAgentStart hook failed") {
		t.Fatalf("Run() error = %v, want contains %q", err, "OnAgentStart hook failed")
	}
	if rec.calls != 1 {
		t.Errorf("OnAgentExit calls = %d, want 1", rec.calls)
	}
	if rec.calls == 1 && rec.runErrs[0] != nil {
		t.Errorf("OnAgentExit runErr = %v, want nil (no panic before termination)", rec.runErrs[0])
	}
	if len(eng.requests) != 0 {
		t.Errorf("LLM calls = %d, want 0 (must fail before main loop)", len(eng.requests))
	}
	_ = res // 终止路径返回零值 PlanResult（history 已积累但不作断言要求）
}

// TestPlannerRunOnAgentExitNormal 正常 plain_text 结束 → OnAgentExit 恰被调
// 1 次、runErr==nil；hook 自身返回 error 时 Run 的返回值不受影响（仅 slog.Warn）。
func TestPlannerRunOnAgentExitNormal(t *testing.T) {
	// 正常路径：OnAgentExit 恰 1 次、runErr==nil
	eng := &fakePlannerLLM{steps: []fakeLLMStep{
		{resp: toolCallResp("call-1", "delegate_sub_agent", `{"agent":"coder"}`)},
		{resp: textResp("EXIT_NORMAL_FINAL")},
	}}
	tools := &fakePlannerTools{state: newState(false), results: map[string]string{"delegate_sub_agent": "OK"}}
	rec := &exitHookRecorder{}
	p := newHookTestPlanner(tools.state, eng, tools, func(c *PlannerConfig) { c.OnAgentExit = rec.hook })
	res, err := p.Run(context.Background(), PlanInput{Input: "EXIT_NORMAL_TASK"})

	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if res.Text != "EXIT_NORMAL_FINAL" || res.StopReason != "plain_text" {
		t.Errorf("Text = %q StopReason = %q, want EXIT_NORMAL_FINAL/plain_text", res.Text, res.StopReason)
	}
	if rec.calls != 1 {
		t.Errorf("OnAgentExit calls = %d, want 1", rec.calls)
	}
	if rec.calls == 1 && rec.runErrs[0] != nil {
		t.Errorf("OnAgentExit runErr = %v, want nil on normal path", rec.runErrs[0])
	}

	// hook 返回 error → 仅 Warn，Run 返回值不受影响
	eng2 := &fakePlannerLLM{steps: []fakeLLMStep{
		{resp: toolCallResp("call-1", "delegate_sub_agent", `{"agent":"coder"}`)},
		{resp: textResp("EXIT_HOOK_ERR_FINAL")},
	}}
	tools2 := &fakePlannerTools{state: newState(false), results: map[string]string{"delegate_sub_agent": "OK"}}
	called := 0
	failingHook := func(_ context.Context, _ error) error {
		called++
		return errors.New("exit boom")
	}
	p2 := newHookTestPlanner(tools2.state, eng2, tools2, func(c *PlannerConfig) { c.OnAgentExit = failingHook })
	res2, err2 := p2.Run(context.Background(), PlanInput{Input: "EXIT_NORMAL_TASK"})

	if err2 != nil {
		t.Errorf("hook error leaked into Run() error = %v, want nil", err2)
	}
	if res2.Text != "EXIT_HOOK_ERR_FINAL" {
		t.Errorf("Text = %q, want %q (hook error must not affect result)", res2.Text, "EXIT_HOOK_ERR_FINAL")
	}
	if called != 1 {
		t.Errorf("OnAgentExit calls = %d, want 1", called)
	}
}

// TestPlannerRunOnAgentExitPanic 工具执行 panic → Run 不向上抛 panic（defer
// recover），OnAgentExit 恰被调 1 次、runErr 非 nil 且含 "agent panic"。
func TestPlannerRunOnAgentExitPanic(t *testing.T) {
	inner := &fakePlannerTools{results: map[string]string{"echo_tool": "OK"}}
	eng := &fakePlannerLLM{steps: []fakeLLMStep{
		{resp: toolCallResp("call-1", "echo_tool", `{"x":1}`)},
	}}
	rec := &exitHookRecorder{}
	p := newHookTestPlanner(newState(false), eng, &panicToolRunner{inner: inner}, func(c *PlannerConfig) {
		c.OnAgentExit = rec.hook
	})

	res, err := p.Run(context.Background(), PlanInput{Input: "PANIC_TASK"}) // 不得向上抛 panic

	// Go 语义：recover 后未命名返回值为零值 → err 为 nil、res 为零值
	if err != nil {
		t.Errorf("Run() error = %v, want nil (panic must not propagate; runErr delivered to hook)", err)
	}
	if res.StopReason != "" {
		t.Errorf("StopReason = %q, want empty (zero PlanResult after panic recovery)", res.StopReason)
	}
	if rec.calls != 1 {
		t.Fatalf("OnAgentExit calls = %d, want 1", rec.calls)
	}
	if rec.runErrs[0] == nil || !strings.Contains(rec.runErrs[0].Error(), "agent panic") {
		t.Errorf("OnAgentExit runErr = %v, want non-nil containing %q", rec.runErrs[0], "agent panic")
	}
}

// ─── P0-Step 2b：OnStepEnd / StopOnFinish ───────────────────────────────────

// TestPlannerRunOnStepEnd 脚本「工具调用→纯文本」两步 → OnStepEnd 恰被调 1 次
//（仅工具步骤，纯文本路径不调用）、step==1（1-based）、runErr==nil；hook 返回
// error 时仅 Warn，Run 仍正常完成。
func TestPlannerRunOnStepEnd(t *testing.T) {
	// 正常路径
	var steps []int
	var stepErrs []error
	eng := &fakePlannerLLM{steps: []fakeLLMStep{
		{resp: toolCallResp("call-1", "delegate_sub_agent", `{"agent":"coder"}`)},
		{resp: textResp("STEP_END_FINAL")},
	}}
	tools := &fakePlannerTools{state: newState(false), results: map[string]string{"delegate_sub_agent": "OK"}}
	p := newHookTestPlanner(tools.state, eng, tools, func(c *PlannerConfig) {
		c.OnStepEnd = func(_ context.Context, step int, runErr error) error {
			steps = append(steps, step)
			stepErrs = append(stepErrs, runErr)
			return nil
		}
	})
	res, err := p.Run(context.Background(), PlanInput{Input: "STEP_END_TASK"})

	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if res.Text != "STEP_END_FINAL" || res.StopReason != "plain_text" || res.Steps != 2 {
		t.Errorf("result = %+v, want Text STEP_END_FINAL / plain_text / Steps 2", res)
	}
	if len(steps) != 1 {
		t.Fatalf("OnStepEnd calls = %d (steps=%v), want 1 (tool step only; plain-text step excluded)", len(steps), steps)
	}
	if steps[0] != 1 {
		t.Errorf("OnStepEnd step = %d, want 1 (1-based)", steps[0])
	}
	if stepErrs[0] != nil {
		t.Errorf("OnStepEnd runErr = %v, want nil (tools executed successfully)", stepErrs[0])
	}

	// hook 返回 error → 仅 Warn，Run 仍正常完成
	called := 0
	eng2 := &fakePlannerLLM{steps: []fakeLLMStep{
		{resp: toolCallResp("call-1", "delegate_sub_agent", `{"agent":"coder"}`)},
		{resp: textResp("STEP_HOOK_ERR_FINAL")},
	}}
	tools2 := &fakePlannerTools{state: newState(false), results: map[string]string{"delegate_sub_agent": "OK"}}
	p2 := newHookTestPlanner(tools2.state, eng2, tools2, func(c *PlannerConfig) {
		c.OnStepEnd = func(_ context.Context, _ int, _ error) error {
			called++
			return errors.New("step boom")
		}
	})
	res2, err2 := p2.Run(context.Background(), PlanInput{Input: "STEP_END_TASK"})

	if err2 != nil {
		t.Errorf("hook error leaked into Run() error = %v, want nil", err2)
	}
	if res2.Text != "STEP_HOOK_ERR_FINAL" || res2.StopReason != "plain_text" {
		t.Errorf("Text = %q StopReason = %q, want STEP_HOOK_ERR_FINAL/plain_text (hook error must not affect result)", res2.Text, res2.StopReason)
	}
	if called != 1 {
		t.Errorf("OnStepEnd calls = %d, want 1", called)
	}
}

// TestPlannerRunStopOnFinish StopOnFinish=true + 脚本「echo_tool→agent_exit」→
// agent_exit 返回后立即返回 Text=agent_exit 的 toolResult、StopReason=="agent_exit"、
// History 非空且含 RoleTool 消息；对照 false → Text=="Task completed successfully"。
func TestPlannerRunStopOnFinish(t *testing.T) {
	// StopOnFinish=true：agent_exit 的 toolResult 直接作为最终文本
	eng := &fakePlannerLLM{steps: []fakeLLMStep{
		{resp: toolCallResp("call-1", "echo_tool", `{"x":1}`)},
		{resp: toolCallResp("call-2", "agent_exit", `{"reason":"done"}`)},
	}}
	tools := &fakePlannerTools{results: map[string]string{"echo_tool": "ECHO_OK", "agent_exit": "EXIT_SIGNAL"}}
	p := newHookTestPlanner(newState(false), eng, tools, func(c *PlannerConfig) { c.StopOnFinish = true })
	res, err := p.Run(context.Background(), PlanInput{Input: "STOP_TASK"})

	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if res.Text != "EXIT_SIGNAL" {
		t.Errorf("StopOnFinish=true: Text = %q, want %q (toolResult of agent_exit)", res.Text, "EXIT_SIGNAL")
	}
	if res.StopReason != "agent_exit" {
		t.Errorf("StopOnFinish=true: StopReason = %q, want %q", res.StopReason, "agent_exit")
	}
	if res.Steps != 2 {
		t.Errorf("StopOnFinish=true: Steps = %d, want 2", res.Steps)
	}
	if len(tools.calls) != 2 || len(eng.requests) != 2 {
		t.Errorf("StopOnFinish=true: tool calls = %d LLM calls = %d, want 2/2 (loop stops right after agent_exit)", len(tools.calls), len(eng.requests))
	}
	if len(res.History) == 0 {
		t.Error("StopOnFinish=true: History is empty, want non-empty")
	}
	if !historyHasRole(res.History, llm.RoleTool) {
		t.Error("StopOnFinish=true: History has no RoleTool message")
	}

	// 对照：StopOnFinish=false（零值）→ 固定文本
	engOff := &fakePlannerLLM{steps: []fakeLLMStep{
		{resp: toolCallResp("call-1", "agent_exit", `{"reason":"done"}`)},
	}}
	toolsOff := &fakePlannerTools{results: map[string]string{"agent_exit": "EXIT_SIGNAL"}}
	pOff := newHookTestPlanner(newState(false), engOff, toolsOff, nil)
	resOff, errOff := pOff.Run(context.Background(), PlanInput{Input: "STOP_TASK"})

	if errOff != nil {
		t.Fatalf("StopOnFinish=false: Run() error = %v, want nil", errOff)
	}
	if resOff.Text != "Task completed successfully" {
		t.Errorf("StopOnFinish=false: Text = %q, want %q", resOff.Text, "Task completed successfully")
	}
	if resOff.StopReason != "agent_exit" {
		t.Errorf("StopOnFinish=false: StopReason = %q, want %q", resOff.StopReason, "agent_exit")
	}
}

// ─── P0-Step 2b：PlanResult.History ─────────────────────────────────────────

// TestPlannerRunHistory plain_text 正常结束（脚本「工具→文本」）→ History 首条
// 为 system（"SYSTEM_PROMPT"）、第二条 user（"TASK_INPUT"）、含 assistant 消息、
// 含 RoleTool 消息（工具消息同步追加）。
func TestPlannerRunHistory(t *testing.T) {
	eng := &fakePlannerLLM{steps: []fakeLLMStep{
		{resp: toolCallResp("call-1", "delegate_sub_agent", `{"agent":"coder"}`)},
		{resp: textResp("HISTORY_FINAL")},
	}}
	tools := &fakePlannerTools{state: newState(false), results: map[string]string{"delegate_sub_agent": "OK"}}
	p := newHookTestPlanner(tools.state, eng, tools, nil)
	res, err := p.Run(context.Background(), PlanInput{Input: "TASK_INPUT"})

	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if res.Text != "HISTORY_FINAL" || res.StopReason != "plain_text" {
		t.Errorf("Text = %q StopReason = %q, want HISTORY_FINAL/plain_text", res.Text, res.StopReason)
	}
	if len(res.History) < 4 {
		t.Fatalf("History length = %d, want >= 4 (system+user+assistant+tool); history = %+v", len(res.History), res.History)
	}
	if h := res.History[0]; h.Role != llm.RoleSystem || h.Content != "SYSTEM_PROMPT" {
		t.Errorf("History[0] = %+v, want RoleSystem/SYSTEM_PROMPT", h)
	}
	if h := res.History[1]; h.Role != llm.RoleUser || h.Content != "TASK_INPUT" {
		t.Errorf("History[1] = %+v, want RoleUser/TASK_INPUT", h)
	}
	if !historyHasRole(res.History, llm.RoleAssistant) {
		t.Error("History has no assistant message")
	}
	if !historyHasRole(res.History, llm.RoleTool) {
		t.Error("History has no RoleTool message (tool messages must sync-append)")
	}
}
