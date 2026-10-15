package director

// planner_resumption_test.go — 恢复的收尾语义（§4.3 / D6）。
//
// 恢复不是"再问模型一次上次做到哪"。这里钉三件事：
//   1. 重放的结果作为**原 call_id 的第二条** tool output 提交，模型看到的那一条
//      就是它（last-wins 由读侧与 llm 侧共同保证，配对本身由 agents 包的
//      TestToolPairingNormalizersAgree 钉）；
//   2. 这一步不调模型，所以恢复是确定性的、零 token 的；
//   3. 它也不能再去调一次工具 —— 委派已经由门面层跑完了，这里只负责提交结果。

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"yagent/internal/llm"
	"yagent/internal/memory"
)

func TestPlannerCommitsResumedDelegationWithoutCallingLLM(t *testing.T) {
	mem := memory.NewConversationMemory(10)
	mem.AddHumanMessage("fix the flaky test")
	mem.AddAssistantMessage("delegating", []memory.ToolCallData{
		{ID: "call_1", Type: "function", Function: memory.ToolCallFunction{
			Name: "delegate_coding", Arguments: json.RawMessage(`{"task":"fix"}`)}},
	})
	// 取消那一刻提交的那条：恢复之后它必须被真实结果顶掉。
	mem.AddToolMessage("Error: context canceled", "call_1")

	// 收尾用 agent_exit 而不是纯文本：planner 每轮开始会把 HasDelegated 清零，
	// 纯文本第一步会被当成"没干活"从而进入强制委派提醒循环。
	eng := &fakePlannerLLM{steps: []fakeLLMStep{{resp: toolCallResp("c9", "agent_exit", `{"reason":"done"}`)}}}
	runner := &fakePlannerTools{results: map[string]string{"delegate_coding": "MUST_NOT_BE_CALLED"}}
	p := newTestPlanner(4, newState(true), eng, runner, nil, nil)

	res, err := p.Run(context.Background(), PlanInput{
		Input: "fix the flaky test",
		Mem:   mem,
		PendingResumption: &Resumption{
			DelegationID: "call_1",
			ToolName:     "delegate_coding",
			AgentName:    "coding",
			Result:       "RESUMED_DELIVERABLE",
			Degraded:     []string{"5: workspace moved"},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.StopReason != "agent_exit" {
		t.Errorf("StopReason = %q, want agent_exit", res.StopReason)
	}

	// 恢复本身不额外问模型：整个 Run 只有"接下来做什么"这一次调用。
	if len(eng.requests) != 1 {
		t.Errorf("LLM 被调用 %d 次, want 1（恢复不该问模型）", len(eng.requests))
	}
	// 也不该再执行一次委派工具：那会把同一个子任务跑第二遍。
	for _, c := range runner.calls {
		if strings.HasPrefix(c.Name, "delegate_") {
			t.Errorf("恢复阶段又执行了一次委派：%+v", c)
		}
	}

	// 提交出去的是原 call_id 的第二条 output。
	var toolMsgs []llmMessageView
	for _, m := range mem.GetMessages() {
		if m.Type == memory.MessageTypeTool {
			toolMsgs = append(toolMsgs, llmMessageView{content: m.Content, callID: deref(m.ToolCallID)})
		}
	}
	// 取消那条 + 恢复那条 + 收尾 agent_exit 那条。
	if len(toolMsgs) != 3 {
		t.Fatalf("memory 里有 %d 条 tool 消息, want 3: %+v", len(toolMsgs), toolMsgs)
	}
	if toolMsgs[0].content != "Error: context canceled" || toolMsgs[0].callID != "call_1" {
		t.Errorf("取消时那条被改写了：%+v", toolMsgs[0])
	}
	if toolMsgs[1].content != "RESUMED_DELIVERABLE" || toolMsgs[1].callID != "call_1" {
		t.Errorf("提交的恢复结果 = %+v, want call_1 / RESUMED_DELIVERABLE", toolMsgs[1])
	}

	// 交给模型的 messages 里，call_1 的位置上必须是最后写的那条（last-wins）。
	req := eng.requests[0]
	var seen []string
	for _, m := range req {
		if m.Role == llm.RoleTool && m.ToolCallID == "call_1" {
			seen = append(seen, m.Content)
		}
	}
	if len(seen) == 0 || seen[len(seen)-1] != "RESUMED_DELIVERABLE" {
		t.Errorf("发给模型的 call_1 结果末位 = %v, want 以 RESUMED_DELIVERABLE 结尾", seen)
	}
}

type llmMessageView struct {
	content string
	callID  string
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// 没有落点时一切照旧：不许凭空往 memory 里塞 tool 消息。
// 这条断言的价值在于它会把"恢复路径"和"普通路径"分开 —— 前者只在被显式要求时介入。
func TestPlannerWithoutResumptionIsUnchanged(t *testing.T) {
	mem := memory.NewConversationMemory(10)
	eng := &fakePlannerLLM{steps: []fakeLLMStep{{resp: toolCallResp("c9", "agent_exit", `{"reason":"done"}`)}}}
	p := newTestPlanner(3, newState(true), eng, &fakePlannerTools{}, nil, nil)

	if _, err := p.Run(context.Background(), PlanInput{Input: "hello", Mem: mem}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, m := range mem.GetMessages() {
		if m.Type == memory.MessageTypeTool && m.Content != "" {
			t.Fatalf("没有落点时也提交了带内容的 tool 消息：%+v", m)
		}
	}
}

// DelegationID 为空 = 这份 Resumption 是坏的。此时**不能**提交一条没有配对的
// tool 消息 —— 那会让下一次构造 LLM 请求时出现一个悬空的 tool 结果，
// provider 会直接拒掉整个请求（比不恢复更糟）。
func TestPlannerIgnoresResumptionWithoutCallID(t *testing.T) {
	mem := memory.NewConversationMemory(10)
	eng := &fakePlannerLLM{steps: []fakeLLMStep{{resp: toolCallResp("c9", "agent_exit", `{"reason":"done"}`)}}}
	p := newTestPlanner(3, newState(true), eng, &fakePlannerTools{}, nil, nil)

	if _, err := p.Run(context.Background(), PlanInput{
		Input:             "hello",
		Mem:               mem,
		PendingResumption: &Resumption{ToolName: "delegate_coding", Result: "ORPHAN"},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, m := range mem.GetMessages() {
		if m.Type == memory.MessageTypeTool && m.Content == "ORPHAN" {
			t.Errorf("call_id 为空时提交了孤立的 tool 消息：%+v", m)
		}
	}
}
