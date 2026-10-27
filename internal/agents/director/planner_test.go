// Package director 的 Planner.Run 单测：注入 fake LLM/ToolRunner/Publisher 与真实 thinklink.Store，不依赖 DirectorAgent。
package director

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

// fakePlannerLLM 按序返回预设响应，序列耗尽后重复最后一个（max_steps 场景需持续工具调用）。
type fakePlannerLLM struct {
	steps    []fakeLLMStep
	idx      int
	requests [][]llm.Message
}

type fakeLLMStep struct {
	resp *llm.Response
	err  error
}

func (e *fakePlannerLLM) GenerateContent(_ context.Context, messages []llm.Message, _ []llm.ToolDef, _ *llm.CallOptions) (*llm.Response, error) {
	e.requests = append(e.requests, cloneMessages(messages))
	if e.idx >= len(e.steps) {
		if len(e.steps) == 0 {
			return nil, errors.New("fakePlannerLLM: no scripted steps")
		}
		e.idx = len(e.steps) - 1
	}
	s := e.steps[e.idx]
	e.idx++
	return s.resp, s.err
}

func (e *fakePlannerLLM) Model() string { return "fake-model" }

func cloneMessages(msgs []llm.Message) []llm.Message {
	out := make([]llm.Message, len(msgs))
	for i, m := range msgs {
		m.ToolCalls = append([]llm.ToolCall(nil), m.ToolCalls...)
		out[i] = m
	}
	return out
}

func textResp(content string) *llm.Response {
	return &llm.Response{Choices: []llm.Choice{{Content: content}}}
}

func toolCallResp(id, name, args string) *llm.Response {
	return &llm.Response{Choices: []llm.Choice{{
		ToolCalls: []llm.ToolCall{{
			ID:       id,
			Type:     "function",
			Function: llm.FunctionCall{Name: name, Arguments: args},
		}},
	}}}
}

// fakePlannerTools 的 state 非 nil 时模拟门面 Tools 包装闭包的委派检测语义
// （planner_run.go 只读 state 驱动提醒控制流）：delegate_* 调用更新 HasDelegated/DelegationAttempts/PendingSubAgentMemory。
type fakePlannerTools struct {
	state   *RunState
	calls   []fakeToolCall
	results map[string]string
	err     error
}

type fakeToolCall struct {
	Name string
	Args string
	// CallID 是 planner 递过来的 tool_call_id。恢复侧全靠它（delegation_id 的源头），
	// 所以假实现必须把它原样记下来，测试才可能发现"传了个空串"这种回归。
	CallID string
}

func (t *fakePlannerTools) Specs() []llm.ToolDef {
	return []llm.ToolDef{
		{Type: "function", Function: llm.FunctionDef{Name: "echo_tool"}},
		{Type: "function", Function: llm.FunctionDef{Name: "delegate_sub_agent"}},
		{Type: "function", Function: llm.FunctionDef{Name: "agent_exit"}},
	}
}

func (t *fakePlannerTools) Call(_ context.Context, name, argsJSON string, callID string) (string, error) {
	t.calls = append(t.calls, fakeToolCall{Name: name, Args: argsJSON, CallID: callID})
	if t.state != nil && strings.HasPrefix(name, "delegate_") {
		t.state.HasDelegated = true
		t.state.DelegationAttempts++
		t.state.PendingSubAgentMemory = &memory.SubAgentMemory{Text: "SUB_AGENT_RESULT"}
	}
	if t.err != nil {
		return "", t.err
	}
	return t.results[name], nil
}

type fakePlannerPublisher struct {
	events []string
}

func (p *fakePlannerPublisher) Publish(event string, _ interface{}, _ string) error {
	p.events = append(p.events, event)
	return nil
}

func (p *fakePlannerPublisher) PublishWithMetadata(event string, _ interface{}, _ string, _ map[string]interface{}) error {
	p.events = append(p.events, event)
	return nil
}

func (p *fakePlannerPublisher) hasEvent(name string) bool {
	for _, e := range p.events {
		if e == name {
			return true
		}
	}
	return false
}

// newState 构造 per-run 状态（HasDelegated 模拟门面 Tools 包装闭包更新后的值）。
func newState(hasDelegated bool) *RunState {
	return &RunState{HasDelegated: hasDelegated}
}

// newTestPlanner 用真实 thinklink.Store 作 Journal、固定 system prompt。
// pub/journal 取接口类型而不是具体指针：传 nil 字面量时接口才是真 nil
// （具体类型指针装箱后产生 typed-nil，使 p.cfg.Publisher != nil 误判为 true）。
func newTestPlanner(maxSteps int, state *RunState, eng *fakePlannerLLM, tools *fakePlannerTools, pub EventPublisher, journal ThinklinkStore) *Planner {
	return NewPlanner(PlannerConfig{
		LLM:        eng,
		Publisher:  pub,
		Journal:    journal,
		Tools:      tools,
		Prompts:    func(_ context.Context, _ PromptInput) (string, error) { return "SYSTEM_PROMPT", nil },
		MaxSteps:   maxSteps,
		LLMTimeout: 30 * time.Second,
	}, state)
}

// userContentsAt 返回第 k 次请求（1-indexed）中 user 角色消息内容列表。
func userContentsAt(t *testing.T, reqs [][]llm.Message, k int) []string {
	t.Helper()
	var out []string
	for _, m := range reqs[k-1] {
		if m.Role == llm.RoleUser {
			out = append(out, m.Content)
		}
	}
	return out
}

// toolMessagesAt 返回第 k 次请求（1-indexed）中 tool 角色消息（无则 Fatal）。
func toolMessagesAt(t *testing.T, reqs [][]llm.Message, k int) []llm.Message {
	t.Helper()
	var out []llm.Message
	for _, m := range reqs[k-1] {
		if m.Role == llm.RoleTool {
			out = append(out, m)
		}
	}
	if len(out) == 0 {
		t.Fatalf("request #%d has no tool messages", k)
	}
	return out
}

func TestPlannerRunPlainTextExit(t *testing.T) {
	eng := &fakePlannerLLM{steps: []fakeLLMStep{
		{resp: toolCallResp("call-1", "delegate_sub_agent", `{"agent":"coder","task":"do it"}`)},
		{resp: textResp("FINAL_ANSWER")},
	}}
	// tools 与 planner 共享同一 state：Run 开始会重置 HasDelegated=false，
	// 须由 fake Tools 的 delegate_* 调用模拟门面包装闭包更新 HasDelegated=true。
	tools := &fakePlannerTools{state: newState(false), results: map[string]string{"delegate_sub_agent": "DELEGATED_OK"}}
	state := tools.state
	pub := &fakePlannerPublisher{}
	journal := thinklink.NewStore(0)

	p := newTestPlanner(5, state, eng, tools, pub, journal)
	res, err := p.Run(context.Background(), PlanInput{Input: "PLAIN_TASK"})

	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if res.Text != "FINAL_ANSWER" {
		t.Errorf("Text = %q, want %q", res.Text, "FINAL_ANSWER")
	}
	if res.StopReason != "plain_text" {
		t.Errorf("StopReason = %q, want %q", res.StopReason, "plain_text")
	}
	if res.Steps != 2 {
		t.Errorf("Steps = %d, want 2", res.Steps)
	}
	if state.Step != 2 {
		t.Errorf("state.Step = %d, want 2 (sync with actual steps)", state.Step)
	}
	if !state.HasDelegated || state.DelegationAttempts != 1 {
		t.Errorf("HasDelegated = %v DelegationAttempts = %d, want true/1 (set by Tools wrapper semantics)", state.HasDelegated, state.DelegationAttempts)
	}
	if state.NonDelegationPrompts != 0 {
		t.Errorf("NonDelegationPrompts = %d, want 0 (delegated, no reminder)", state.NonDelegationPrompts)
	}
	if len(eng.requests) != 2 {
		t.Fatalf("LLM calls = %d, want 2", len(eng.requests))
	}
	if len(tools.calls) != 1 || tools.calls[0].Name != "delegate_sub_agent" {
		t.Errorf("tool calls = %+v, want single delegate_sub_agent call", tools.calls)
	}
	snap := journal.Snapshot()
	if len(snap) != 1 || snap[0].Content != "PLAIN_TASK" || snap[0].Kind != thinklink.KindUserInput {
		t.Errorf("journal snapshot = %+v, want single KindUserInput entry %q", snap, "PLAIN_TASK")
	}
	for _, ev := range []string{"model_info", "llm_call_start", "ai_stream_end", "ai_response", "thinklink_entry"} {
		if !pub.hasEvent(ev) {
			t.Errorf("event %q not published; got %v", ev, pub.events)
		}
	}
}

// TestPlannerRunAgentExit agent_exit 收尾返回固定文本 "Task completed successfully"，不提取 exit reason 参数。
func TestPlannerRunAgentExit(t *testing.T) {
	eng := &fakePlannerLLM{steps: []fakeLLMStep{{resp: toolCallResp("call-1", "agent_exit", `{"reason":"task done"}`)}}}
	tools := &fakePlannerTools{}
	state := newState(false)

	p := newTestPlanner(5, state, eng, tools, nil, nil)
	res, err := p.Run(context.Background(), PlanInput{Input: "EXIT_TASK"})

	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if res.Text != "Task completed successfully" {
		t.Errorf("Text = %q, want %q", res.Text, "Task completed successfully")
	}
	if res.StopReason != "agent_exit" {
		t.Errorf("StopReason = %q, want %q", res.StopReason, "agent_exit")
	}
	if res.Steps != 1 || state.Step != 1 {
		t.Errorf("Steps = %d, state.Step = %d, want both 1", res.Steps, state.Step)
	}
	if len(eng.requests) != 1 {
		t.Fatalf("LLM calls = %d, want 1", len(eng.requests))
	}
	if len(tools.calls) != 1 || tools.calls[0].Name != "agent_exit" || tools.calls[0].Args != `{"reason":"task done"}` {
		t.Errorf("tool calls = %+v, want single agent_exit call with original args", tools.calls)
	}
}

func TestPlannerRunMaxSteps(t *testing.T) {
	eng := &fakePlannerLLM{steps: []fakeLLMStep{{resp: toolCallResp("call-n", "echo_tool", `{"x":1}`)}}}
	tools := &fakePlannerTools{results: map[string]string{"echo_tool": "OK"}}
	state := newState(false)

	p := newTestPlanner(3, state, eng, tools, nil, nil)
	res, err := p.Run(context.Background(), PlanInput{Input: "MAX_STEPS_TASK"})

	if err == nil {
		t.Fatal("Run() error = nil, want max-steps exceeded error")
	}
	if !strings.Contains(err.Error(), "exceeded max steps") {
		t.Errorf("error = %v, want contains %q", err, "exceeded max steps")
	}
	if res.StopReason != "max_steps" {
		t.Errorf("StopReason = %q, want %q", res.StopReason, "max_steps")
	}
	if res.Steps != 3 || state.Step != 3 {
		t.Errorf("Steps = %d, state.Step = %d, want both 3", res.Steps, state.Step)
	}
	if len(tools.calls) != 3 {
		t.Errorf("tool calls = %d, want 3 (one per step)", len(tools.calls))
	}
	if len(eng.requests) != 3 {
		t.Errorf("LLM calls = %d, want 3", len(eng.requests))
	}
}

func TestPlannerRunLLMError(t *testing.T) {
	wantErr := errors.New("boom from llm")
	eng := &fakePlannerLLM{steps: []fakeLLMStep{{err: wantErr}}}
	state := newState(false)

	p := newTestPlanner(5, state, eng, &fakePlannerTools{}, nil, nil)
	res, err := p.Run(context.Background(), PlanInput{Input: "LLM_ERR_TASK"})

	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}
	if res.StopReason != "llm_error" {
		t.Errorf("StopReason = %q, want %q", res.StopReason, "llm_error")
	}
	if res.Steps != 1 || state.Step != 1 {
		t.Errorf("Steps = %d, state.Step = %d, want both 1", res.Steps, state.Step)
	}
	if len(eng.requests) != 1 {
		t.Errorf("LLM calls = %d, want 1 (Recovery nil → zero retries)", len(eng.requests))
	}
}

// TestPlannerRunForceDelegationReminder 未委派纯文本时前 3 次各注入一条 user 角色提醒，上限 3 后放行；提醒不写进 ConversationMemory。
func TestPlannerRunForceDelegationReminder(t *testing.T) {
	const (
		forceMsg1 = "You must delegate an agent to complete the task. Do not reply with plain text — call a delegate_* tool now."
		forceMsg2 = "You still have not delegated any agent. You MUST call a delegate_* tool to complete the task before responding."
	)
	eng := &fakePlannerLLM{steps: []fakeLLMStep{
		{resp: textResp("ANSWER_1")},
		{resp: textResp("ANSWER_2")},
		{resp: textResp("ANSWER_3")},
		{resp: textResp("ANSWER_4_FINAL")},
	}}
	mem := memory.NewConversationMemory(10)
	state := newState(false)

	p := newTestPlanner(10, state, eng, &fakePlannerTools{}, nil, nil)
	res, err := p.Run(context.Background(), PlanInput{Input: "DELEGATE_TASK", Mem: mem})

	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if res.StopReason != "plain_text" || res.Text != "ANSWER_4_FINAL" {
		t.Errorf("StopReason = %q Text = %q, want plain_text / ANSWER_4_FINAL", res.StopReason, res.Text)
	}
	if res.Steps != 4 {
		t.Errorf("Steps = %d, want 4", res.Steps)
	}
	if state.NonDelegationPrompts != 3 {
		t.Errorf("NonDelegationPrompts = %d, want 3 (capped)", state.NonDelegationPrompts)
	}
	if len(eng.requests) != 4 {
		t.Fatalf("LLM calls = %d, want 4", len(eng.requests))
	}
	// 第 2 次调用注入第 1 种文案；第 3/4 次注入第 2 种文案（末条 user 消息）
	wantLast := map[int]string{2: forceMsg1, 3: forceMsg2, 4: forceMsg2}
	for k, want := range wantLast {
		uc := userContentsAt(t, eng.requests, k)
		if len(uc) == 0 || uc[len(uc)-1] != want {
			t.Errorf("request #%d last user message = %v, want ending with forced reminder %q", k, uc, want)
		}
	}
	if uc := userContentsAt(t, eng.requests, 1); len(uc) != 1 || uc[0] != "DELEGATE_TASK" {
		t.Errorf("request #1 user messages = %v, want only original input", uc)
	}
	// 提醒不写入 mem：mem 仅 1 条 human + 4 条 assistant
	msgs := mem.GetMessages()
	if len(msgs) != 5 {
		t.Fatalf("mem messages = %d, want 5", len(msgs))
	}
	for i, m := range msgs {
		if strings.Contains(m.Content, "delegate_* tool") {
			t.Errorf("mem message #%d polluted with forced reminder: %q", i, m.Content)
		}
	}
}

// TestPlannerRunToolResultBackfill 工具结果以 RoleTool 消息（含 ToolCallID/ToolName）回填进下一次 LLM 请求。
func TestPlannerRunToolResultBackfill(t *testing.T) {
	eng := &fakePlannerLLM{steps: []fakeLLMStep{
		{resp: toolCallResp("call-7", "echo_tool", `{"input":"hello"}`)},
		{resp: toolCallResp("call-8", "delegate_sub_agent", `{"agent":"coder"}`)},
		{resp: textResp("DONE_AFTER_TOOL")},
	}}
	tools := &fakePlannerTools{state: newState(false), results: map[string]string{"echo_tool": "ECHO_RESULT"}}

	p := newTestPlanner(5, tools.state, eng, tools, nil, nil)
	res, err := p.Run(context.Background(), PlanInput{Input: "TOOL_TASK"})

	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if res.Text != "DONE_AFTER_TOOL" || res.StopReason != "plain_text" || res.Steps != 3 {
		t.Errorf("result = %+v, want Text DONE_AFTER_TOOL / plain_text / Steps 3", res)
	}
	if len(eng.requests) != 3 {
		t.Fatalf("LLM calls = %d, want 3", len(eng.requests))
	}
	if len(tools.calls) != 2 || tools.calls[0].Name != "echo_tool" || tools.calls[0].Args != `{"input":"hello"}` {
		t.Fatalf("tool calls = %+v, want echo_tool then delegate_sub_agent", tools.calls)
	}
	tms := toolMessagesAt(t, eng.requests, 2)
	if len(tms) != 1 {
		t.Fatalf("request #2 tool messages = %d, want 1", len(tms))
	}
	tm := tms[0]
	if tm.Content != "ECHO_RESULT" || tm.ToolCallID != "call-7" || tm.ToolName != "echo_tool" {
		t.Errorf("backfilled tool message = %+v, want Content ECHO_RESULT / ToolCallID call-7 / ToolName echo_tool", tm)
	}
}

func TestPlannerRunNilTolerance(t *testing.T) {
	eng := &fakePlannerLLM{steps: []fakeLLMStep{
		{resp: toolCallResp("call-1", "delegate_sub_agent", `{"agent":"coder"}`)},
		{resp: textResp("NIL_OK")},
	}}
	tools := &fakePlannerTools{state: newState(false), results: map[string]string{"delegate_sub_agent": "OK"}}
	state := tools.state

	p := newTestPlanner(5, state, eng, tools, nil, nil)
	res, err := p.Run(context.Background(), PlanInput{Input: "NIL_TASK"})

	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if res.Text != "NIL_OK" || res.StopReason != "plain_text" || res.Steps != 2 {
		t.Errorf("result = %+v, want Text NIL_OK / plain_text / Steps 2", res)
	}
	if state.Step != 2 {
		t.Errorf("state.Step = %d, want 2", state.Step)
	}
}

func TestPlannerRunPromptBuilderError(t *testing.T) {
	eng := &fakePlannerLLM{}
	p := NewPlanner(PlannerConfig{
		LLM:      eng,
		Tools:    &fakePlannerTools{},
		Prompts:  func(_ context.Context, _ PromptInput) (string, error) { return "", errors.New("prompt boom") },
		MaxSteps: 5,
	}, newState(false))

	_, err := p.Run(context.Background(), PlanInput{Input: "X"})

	if err == nil || !strings.Contains(err.Error(), "prompt boom") {
		t.Fatalf("Run() error = %v, want prompt boom", err)
	}
	if len(eng.requests) != 0 {
		t.Errorf("LLM calls = %d, want 0 (should fail before LLM call)", len(eng.requests))
	}
}

// TestPlannerRunZeroMaxSteps MaxSteps=0 时不调用 LLM 直接返回 max_steps，
// Steps 与 state.Step 均为 0。
func TestPlannerRunZeroMaxSteps(t *testing.T) {
	eng := &fakePlannerLLM{}
	state := newState(false)

	p := newTestPlanner(0, state, eng, &fakePlannerTools{}, nil, nil)
	res, err := p.Run(context.Background(), PlanInput{Input: "X"})

	if err == nil || !strings.Contains(err.Error(), "exceeded max steps") {
		t.Fatalf("Run() error = %v, want exceeded max steps", err)
	}
	if res.StopReason != "max_steps" || res.Steps != 0 || state.Step != 0 {
		t.Errorf("result = %+v state.Step = %d, want max_steps / 0 / 0", res, state.Step)
	}
	if len(eng.requests) != 0 {
		t.Errorf("LLM calls = %d, want 0", len(eng.requests))
	}
}

// ─── 终止事件落盘断点状态（B1）───────────────────────────────────────────────

// 恢复侧 RolloutInfo.FinishState() 以 stop_reason 为主判据，缺字段就会把中断会话误判为已完成，
// TUI 因此不再提示断点续跑——所以这里按真实 writer/reader 往返断言，而非只查 PlanResult。
func TestPlannerRunPersistsRunState(t *testing.T) {
	tests := []struct {
		name           string
		maxSteps       int
		steps          []fakeLLMStep
		toolResults    map[string]string
		wantStopReason string
		wantFinish     memory.RolloutFinishState
		wantSteps      int
		wantDelegated  bool
		wantRunErr     bool
	}{
		{
			name:     "plain_text_complete",
			maxSteps: 5,
			steps: []fakeLLMStep{
				{resp: toolCallResp("call-1", "delegate_sub_agent", `{"agent":"coder"}`)},
				{resp: textResp("DONE")},
			},
			toolResults:    map[string]string{"delegate_sub_agent": "OK"},
			wantStopReason: "plain_text",
			wantFinish:     memory.FinishComplete,
			wantSteps:      2,
			wantDelegated:  true,
		},
		{
			name:           "max_steps_interrupted",
			maxSteps:       3,
			steps:          []fakeLLMStep{{resp: toolCallResp("call-n", "echo_tool", `{"x":1}`)}},
			toolResults:    map[string]string{"echo_tool": "OK"},
			wantStopReason: "max_steps",
			wantFinish:     memory.FinishInterrupted,
			wantSteps:      3,
			wantRunErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			w, err := memory.NewRolloutWriter("director", "task-planner-runstate", "planner-test")
			if err != nil {
				t.Fatalf("NewRolloutWriter() error = %v", err)
			}

			eng := &fakePlannerLLM{steps: tt.steps}
			tools := &fakePlannerTools{state: newState(false), results: tt.toolResults}
			p := newTestPlanner(tt.maxSteps, tools.state, eng, tools, nil, nil)
			p.cfg.Rollout = w
			path := w.FilePath()

			res, runErr := p.Run(context.Background(), PlanInput{Input: "RUNSTATE_TASK"})
			if (runErr != nil) != tt.wantRunErr {
				t.Fatalf("Run() error = %v, wantRunErr = %v", runErr, tt.wantRunErr)
			}
			if res.StopReason != tt.wantStopReason {
				t.Errorf("StopReason = %q, want %q", res.StopReason, tt.wantStopReason)
			}

			// Run 内的 defer 已 Close；此处直接读回文件。
			_, info, err := memory.ReadRolloutMemory(path, memory.RolloutReadOptions{})
			if err != nil {
				t.Fatalf("ReadRolloutMemory() error = %v", err)
			}
			if info.TaskID != "task-planner-runstate" {
				t.Errorf("TaskID = %q, want writer backfill %q", info.TaskID, "task-planner-runstate")
			}
			if got := info.FinishState(); got != tt.wantFinish {
				t.Errorf("FinishState() = %v, want %v", got, tt.wantFinish)
			}
			if info.StopReason != tt.wantStopReason {
				t.Errorf("restored StopReason = %q, want %q", info.StopReason, tt.wantStopReason)
			}
			if info.Steps != tt.wantSteps || info.BestStep() != tt.wantSteps {
				t.Errorf("Steps = %d BestStep() = %d, want both %d", info.Steps, info.BestStep(), tt.wantSteps)
			}
			if info.MaxSteps != tt.maxSteps {
				t.Errorf("MaxSteps = %d, want %d", info.MaxSteps, tt.maxSteps)
			}
			if info.HasDelegated != tt.wantDelegated {
				t.Errorf("HasDelegated = %v, want %v", info.HasDelegated, tt.wantDelegated)
			}
		})
	}
}

// TestPlannerRunAbortedPersistsReason llm_error 走 writeAborted：中止原因原样落盘，
// 恢复侧 AbortReason 用于 TUI 的中断摘要展示。
func TestPlannerRunAbortedPersistsReason(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	w, err := memory.NewRolloutWriter("director", "task-planner-abort", "planner-test")
	if err != nil {
		t.Fatalf("NewRolloutWriter() error = %v", err)
	}
	path := w.FilePath()

	eng := &fakePlannerLLM{steps: []fakeLLMStep{{err: errors.New("llm went away")}}}
	p := newTestPlanner(5, newState(false), eng, &fakePlannerTools{}, nil, nil)
	p.cfg.Rollout = w

	if _, err := p.Run(context.Background(), PlanInput{Input: "ABORT_TASK"}); err == nil {
		t.Fatal("Run() error = nil, want llm error")
	}

	_, info, err := memory.ReadRolloutMemory(path, memory.RolloutReadOptions{})
	if err != nil {
		t.Fatalf("ReadRolloutMemory() error = %v", err)
	}
	if info.FinishState() != memory.FinishInterrupted {
		t.Errorf("FinishState() = %v, want %v", info.FinishState(), memory.FinishInterrupted)
	}
	if info.StopReason != "llm_error" {
		t.Errorf("StopReason = %q, want %q", info.StopReason, "llm_error")
	}
	if !strings.Contains(info.AbortReason, "llm went away") {
		t.Errorf("AbortReason = %q, want contains %q", info.AbortReason, "llm went away")
	}
	if info.Steps != 1 {
		t.Errorf("Steps = %d, want 1", info.Steps)
	}
}

// TestPlannerRunResumedForcesProjectContext 续跑标记传播：PlanInput.Resumed 必须以
// PromptInput.ForceProjectContext 到达 PromptBuilder——恢复的 memory 无 system 消息，
// 仅按 FirstConversation 判定会让续跑首轮漏掉项目上下文注入（B2）。
func TestPlannerRunResumedForcesProjectContext(t *testing.T) {
	captured := make(chan PromptInput, 1)
	eng := &fakePlannerLLM{steps: []fakeLLMStep{{resp: textResp("OK")}}}
	mem := memory.NewConversationMemory(10)
	// 「非首轮」的形态是**已经有 AI 回应**（assistant/tool），不是"memory 非空"：
	// 单条 human 恰是新任务首轮（http 路径在提交前预写，websocket.go:169）。
	mem.AddHumanMessage("EARLIER")
	mem.AddAssistantMessage("EARLIER_REPLY", nil)

	p := NewPlanner(PlannerConfig{
		LLM:        eng,
		Tools:      &fakePlannerTools{},
		Prompts:    func(_ context.Context, in PromptInput) (string, error) { captured <- in; return "SYSTEM_PROMPT", nil },
		MaxSteps:   5,
		LLMTimeout: 30 * time.Second,
	}, newState(false))

	if _, err := p.Run(context.Background(), PlanInput{Input: "RESUMED_TASK", Mem: mem, Resumed: true}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	in := <-captured
	if in.FirstConversation {
		t.Error("FirstConversation = true 于已有 AI 回应的追问, want false")
	}
	if !in.ForceProjectContext {
		t.Error("ForceProjectContext = false, want true (PlanInput.Resumed must propagate)")
	}
}

// TestPlannerRunFirstConversationSnapshottedBeforeHumanWrite 项目上下文注入的唯一判据：
// FirstConversation 必须是「本轮开始时 memory 里还没有任何 AI 回应」，所以判定要快照在把本轮
// 用户输入写进 memory **之前**（它问的是本轮开始时的状态）。门面层的
// `in.FirstConversation || in.ForceProjectContext` 守卫（director.go:932）据此决定注入，
// 判错一轮新任务就永远拿不到 YAGENT.md/CLAUDE.md/AGENTS.md。
func TestPlannerRunFirstConversationSnapshottedBeforeHumanWrite(t *testing.T) {
	var captured []PromptInput
	// agent_exit 收尾：每轮只调一次 LLM，不触发强制委派重试，也不碰真实 provider。
	newPlanner := func() (*Planner, *fakePlannerLLM) {
		eng := &fakePlannerLLM{steps: []fakeLLMStep{
			{resp: toolCallResp("call-exit", "agent_exit", `{"reason":"done"}`)},
		}}
		return NewPlanner(PlannerConfig{
			LLM:   eng,
			Tools: &fakePlannerTools{},
			Prompts: func(_ context.Context, in PromptInput) (string, error) {
				captured = append(captured, in)
				return "SYSTEM_PROMPT", nil
			},
			MaxSteps:   5,
			LLMTimeout: 30 * time.Second,
		}, newState(false)), eng
	}

	// 首轮：TUI 新任务用的就是这样一份空 memory（internal/tui/tui_tasks.go:87）。
	mem := memory.NewConversationMemory(300)
	p, eng := newPlanner()
	if _, err := p.Run(context.Background(), PlanInput{Input: "FIRST_TURN", Mem: mem}); err != nil {
		t.Fatalf("Run() first turn error = %v", err)
	}
	if len(captured) != 1 {
		t.Fatalf("PromptInput 捕获 = %d 次, want 1", len(captured))
	}
	if !captured[0].FirstConversation {
		t.Error("FirstConversation = false 于空 memory 的首轮, want true（顺序陷阱：写入之后才判定 → 项目上下文永不注入）")
	}
	if captured[0].ForceProjectContext {
		t.Error("ForceProjectContext = true 而 PlanInput.Resumed 为假, want false")
	}
	// 取快照不许顺手改掉写入本身：本轮输入仍要在 message 里送到模型。
	if uc := userContentsAt(t, eng.requests, 1); len(uc) != 1 || uc[0] != "FIRST_TURN" {
		t.Errorf("request #1 user messages = %v, want only FIRST_TURN", uc)
	}

	// 次轮（同一任务追问）：本轮开始时已经有 AI 回应 → 不再重复注入。
	if _, err := p.Run(context.Background(), PlanInput{Input: "FOLLOW_UP", Mem: mem}); err != nil {
		t.Fatalf("Run() follow-up error = %v", err)
	}
	if len(captured) != 2 {
		t.Fatalf("PromptInput 捕获 = %d 次, want 2", len(captured))
	}
	if captured[1].FirstConversation {
		t.Error("FirstConversation = true 于追问轮（memory 非空）, want false")
	}
	var humans []string
	for _, m := range mem.GetMessages() {
		if m.Type == memory.MessageTypeHuman {
			humans = append(humans, m.Content)
		}
	}
	if len(humans) != 2 || humans[0] != "FIRST_TURN" || humans[1] != "FOLLOW_UP" {
		t.Errorf("memory 里的 human 消息 = %v, want [FIRST_TURN FOLLOW_UP]（两轮输入都要落库）", humans)
	}

	// 没有 memory 时（老调用方，PlanInput.Mem == nil）恒判为首轮：注入判据不该因为没带
	// memory 就失效。
	captured = captured[:0]
	p2, _ := newPlanner()
	if _, err := p2.Run(context.Background(), PlanInput{Input: "NO_MEMORY"}); err != nil {
		t.Fatalf("Run() with nil Mem error = %v", err)
	}
	if len(captured) != 1 || !captured[0].FirstConversation {
		t.Errorf("nil Mem 时 captured = %+v, want FirstConversation true", captured)
	}
}

// TestPlannerRunFirstTurnWithPrewrittenHumanInput 钉住第二处断点：HTTP/WebUI 路径在提交任务
// **之前**就把用户消息写进 memory 并立即落盘（internal/http/websocket.go:169，为了让断线/崩溃
// 可恢复，那个预写不许动），于是「memory 为空」这条判据在那条入口上恒为 false，WebUI 用户永远
// 拿不到项目上下文。判据改成「本轮开始时还没有任何 AI 回应」之后，预写一条 human 仍是首轮 ——
// 旧的 len(GetMessages()) == 0 判定下本用例的首个断言必红。
func TestPlannerRunFirstTurnWithPrewrittenHumanInput(t *testing.T) {
	var captured []PromptInput
	eng := &fakePlannerLLM{steps: []fakeLLMStep{
		{resp: toolCallResp("call-exit", "agent_exit", `{"reason":"done"}`)},
	}}
	p := NewPlanner(PlannerConfig{
		LLM:   eng,
		Tools: &fakePlannerTools{},
		Prompts: func(_ context.Context, in PromptInput) (string, error) {
			captured = append(captured, in)
			return "SYSTEM_PROMPT", nil
		},
		MaxSteps:   5,
		LLMTimeout: 30 * time.Second,
	}, newState(false))

	// 复刻 websocket 路径：先 AddHumanMessage，再把同一份 memory 交给 Director。
	mem := memory.NewConversationMemory(300)
	mem.AddHumanMessage("HTTP_TASK")

	if _, err := p.Run(context.Background(), PlanInput{Input: "HTTP_TASK", Mem: mem}); err != nil {
		t.Fatalf("Run() http first turn error = %v", err)
	}
	if len(captured) != 1 {
		t.Fatalf("PromptInput 捕获 = %d 次, want 1", len(captured))
	}
	if !captured[0].FirstConversation {
		t.Error("FirstConversation = false 于 http 预写 human 的新任务首轮, want true（WebUI 用户拿不到项目上下文）")
	}
	if captured[0].ForceProjectContext {
		t.Error("ForceProjectContext = true 而 PlanInput.Resumed 为假, want false")
	}
	// 预写不许顶掉 seedHumanInput 的去重：同一条输入在 memory 里只能出现一遍。
	var humans []string
	for _, m := range mem.GetMessages() {
		if m.Type == memory.MessageTypeHuman {
			humans = append(humans, m.Content)
		}
	}
	if len(humans) != 1 || humans[0] != "HTTP_TASK" {
		t.Errorf("memory 里的 human 消息 = %v, want [HTTP_TASK]（同一条输入不许落两遍）", humans)
	}

	// 同一任务的追问：首轮已经把 assistant/tool 写进 memory → 不再重复注入。
	if _, err := p.Run(context.Background(), PlanInput{Input: "HTTP_FOLLOW_UP", Mem: mem}); err != nil {
		t.Fatalf("Run() http follow-up error = %v", err)
	}
	if len(captured) != 2 {
		t.Fatalf("PromptInput 捕获 = %d 次, want 2", len(captured))
	}
	if captured[1].FirstConversation {
		t.Error("FirstConversation = true 于 http 任务的追问轮, want false")
	}
}

// TestPlannerRunWritesThinklinkEntries B4：journal 条目（user_input / thought_plan）
// 必须随写 rollout，恢复侧据此重建 journal 视图而不必从消息块重解析。
func TestPlannerRunWritesThinklinkEntries(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	w, err := memory.NewRolloutWriter("director", "task-thinklink", "planner-test")
	if err != nil {
		t.Fatalf("NewRolloutWriter() error = %v", err)
	}
	path := w.FilePath()

	eng := &fakePlannerLLM{steps: []fakeLLMStep{{resp: textResp(
		"## Thought & Plan\n* [ ] 1. [待处理] do it\n\nFINAL_ANSWER")}}}
	journal := thinklink.NewStore(200)
	p := NewPlanner(PlannerConfig{
		LLM:        eng,
		Tools:      &fakePlannerTools{},
		Prompts:    func(_ context.Context, _ PromptInput) (string, error) { return "SYSTEM_PROMPT", nil },
		Journal:    journal,
		MaxSteps:   5,
		LLMTimeout: 30 * time.Second,
	}, newState(false))
	p.cfg.Rollout = w

	if _, err := p.Run(context.Background(), PlanInput{Input: "TL_TASK"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	_, info, err := memory.ReadRolloutMemory(path, memory.RolloutReadOptions{})
	if err != nil {
		t.Fatalf("ReadRolloutMemory() error = %v", err)
	}
	// 纯文本响应会触发强制委派重试，同一块可能按步记录多次（AddThoughtPlan 无去重，
	// journal 语义如此），故断言形状而非精确条数。
	if len(info.ThinklinkEntries) < 2 {
		t.Fatalf("ThinklinkEntries len = %d, want >= 2 (user_input + thought_plan)", len(info.ThinklinkEntries))
	}
	if info.ThinklinkEntries[0].Kind != "user_input" || info.ThinklinkEntries[0].Content != "TL_TASK" {
		t.Errorf("entry[0] = %+v, want user_input TL_TASK", info.ThinklinkEntries[0])
	}
	last := info.ThinklinkEntries[len(info.ThinklinkEntries)-1]
	if last.Kind != "thought_plan" || !strings.Contains(last.Content, "Thought & Plan") {
		t.Errorf("last entry = %+v, want thought_plan block", last)
	}
}
