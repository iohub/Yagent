package director

import (
	"context"
	"errors"
	"strings"
	"testing"

	"yagent/internal/llm"
	"yagent/internal/thinklink"
)

// planner 侧与子 Agent 循环同形：拿不到 turn 必须是错误，截断必须让模型看见。
// 判据只有一份（llm.Choice），这里钉的是"planner 真的调用了它"。

func TestPlannerRun_NoChoiceResponseIsError(t *testing.T) {
	eng := &fakePlannerLLM{steps: []fakeLLMStep{{resp: &llm.Response{}}}}
	tools := &fakePlannerTools{results: map[string]string{}}
	pub := &fakePlannerPublisher{}

	p := newTestPlanner(3, newState(false), eng, tools, pub, thinklink.NewStore(0))
	_, err := p.Run(context.Background(), PlanInput{Input: "T"})
	if err == nil {
		t.Fatal("零 choice 的响应被当成成功（旧形状是在这里 resp.Choices[0] 崩掉）")
	}
	if !errors.Is(err, llm.ErrEmptyCompletion) {
		t.Errorf("err = %v, want errors.Is(llm.ErrEmptyCompletion)", err)
	}
}

func TestPlannerRun_TruncatedTurnCarriesNotices(t *testing.T) {
	eng := &fakePlannerLLM{steps: []fakeLLMStep{
		{resp: &llm.Response{Choices: []llm.Choice{{
			Content:      "想到一半",
			FinishReason: llm.FinishLength,
			ToolCalls: []llm.ToolCall{{
				ID:       "call-cut",
				Type:     "function",
				Function: llm.FunctionCall{Name: "echo_tool", Arguments: `{"input":"半条`},
			}},
		}}}},
		{resp: toolCallResp("call-2", "agent_exit", `{"reason":"done"}`)},
	}}
	tools := &fakePlannerTools{results: map[string]string{
		"echo_tool":  "ECHOED",
		"agent_exit": "EXITED",
	}}
	pub := &fakePlannerPublisher{}

	p := newTestPlanner(3, newState(false), eng, tools, pub, thinklink.NewStore(0))
	if _, err := p.Run(context.Background(), PlanInput{Input: "T"}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(eng.requests) < 2 {
		t.Fatalf("requests = %d, want 第二轮才看得到上一轮的 turn", len(eng.requests))
	}

	var assistant, tool string
	for _, m := range eng.requests[1] {
		switch m.Role {
		case llm.RoleAssistant:
			if strings.Contains(m.Content, "想到一半") {
				assistant = m.Content
			}
		case llm.RoleTool:
			tool = m.Content
		}
	}
	if assistant == "" || tool == "" {
		t.Fatalf("第二轮请求缺 assistant 或 tool 消息: assistant=%q tool=%q", assistant, tool)
	}
	if !strings.Contains(assistant, "max_tokens") {
		t.Errorf("assistant 轮没有截断标记，模型会以为自己说完了: %q", assistant)
	}
	if !strings.Contains(tool, "ECHOED") || !strings.Contains(tool, "arguments incomplete") {
		t.Errorf("工具回执 = %q, want 照常执行并点名参数被截断", tool)
	}
}
