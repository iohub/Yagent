package agents

import (
	"context"
	"errors"
	"strings"
	"testing"

	"yagent/internal/llm"
	"yagent/internal/tools"
)

// 循环对 LLM 响应的两条契约：拿不到 turn 必须是错误而不是 panic，
// 被 max_tokens 掐断必须让模型自己看见（标记只加在交回给模型的那一份上）。

func TestRunAgentLoop_NoChoiceResponseIsError(t *testing.T) {
	mock := &mockLLM{responses: []*llm.Response{{}}}
	cfg := ExecutorConfig{
		SystemPrompt: "You are a test agent.",
		UserInput:    "say something",
		Surface:      tools.NewSurfaceFromAdapters("test-agent", newFastAdapter("echo", "echo back", "ECHOED")),
		LLM:          mock,
		MaxSteps:     2,
		AgentName:    "no-choice",
	}

	_, err := RunAgentLoop(context.Background(), cfg)
	if err == nil {
		t.Fatal("零 choice 的响应被当成成功（旧形状是在这里 resp.Choices[0] 崩掉）")
	}
	if !errors.Is(err, llm.ErrEmptyCompletion) {
		t.Errorf("err = %v, want errors.Is(llm.ErrEmptyCompletion)", err)
	}
}

func TestRunAgentLoop_TruncatedTurnCarriesNotices(t *testing.T) {
	mock := &mockLLM{responses: []*llm.Response{
		{Choices: []llm.Choice{{
			Content:      "写到一半",
			FinishReason: llm.FinishLength,
			ToolCalls: []llm.ToolCall{{
				ID:       "call-cut",
				Type:     "function",
				Function: llm.FunctionCall{Name: "echo", Arguments: `{"text":"半条`},
			}},
		}}},
		{Choices: []llm.Choice{{Content: "done"}}},
	}}
	cfg := ExecutorConfig{
		SystemPrompt: "You are a test agent.",
		UserInput:    "write it out",
		Surface:      tools.NewSurfaceFromAdapters("test-agent", newFastAdapter("echo", "echo back", "ECHOED")),
		LLM:          mock,
		MaxSteps:     3,
		AgentName:    "truncated",
	}

	res, err := RunAgentLoop(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunAgentLoop: %v", err)
	}

	var assistant, tool *llm.Message
	for i := range res.History {
		switch res.History[i].Role {
		case llm.RoleAssistant:
			if strings.Contains(res.History[i].Content, "写到一半") && assistant == nil {
				assistant = &res.History[i]
			}
		case llm.RoleTool:
			tool = &res.History[i]
		}
	}
	if assistant == nil || tool == nil {
		t.Fatalf("history = %+v, want 一条 assistant 与一条 tool", res.History)
	}

	// 模型看不到自己被掐断的话，下一步就是直接收尾。
	if !strings.Contains(assistant.Content, "max_tokens") {
		t.Errorf("assistant 轮没有截断标记: %q", assistant.Content)
	}
	if !strings.HasPrefix(assistant.Content, "写到一半") {
		t.Errorf("正文被改写到前面去了: %q", assistant.Content)
	}
	// 残缺参数照常执行了（工具层把它当裸字符串），回执必须点名截断。
	if !strings.Contains(tool.Content, "ECHOED") {
		t.Errorf("工具没有照常执行: %q", tool.Content)
	}
	if !strings.Contains(tool.Content, "arguments incomplete") {
		t.Errorf("工具回执没有说明参数被截断: %q", tool.Content)
	}
}

// 被掐断的那一轮直接收尾时，交付正文也必须带标记——否则 Director 读到的是"它说完了"。
func TestRunAgentLoop_TruncatedPlainTextDeliverableKeepsNotice(t *testing.T) {
	mock := &mockLLM{responses: []*llm.Response{
		{Choices: []llm.Choice{{Content: "只写了一半的报告", FinishReason: llm.FinishLength}}},
	}}
	cfg := ExecutorConfig{
		SystemPrompt: "You are a test agent.",
		UserInput:    "report",
		Surface:      tools.NewSurfaceFromAdapters("test-agent", newFastAdapter("echo", "echo back", "ECHOED")),
		LLM:          mock,
		MaxSteps:     2,
		AgentName:    "truncated-text",
	}

	res, err := RunAgentLoop(context.Background(), cfg)
	if err != nil {
		t.Fatalf("RunAgentLoop: %v", err)
	}
	if !strings.HasPrefix(res.Text, "只写了一半的报告") || !strings.Contains(res.Text, "max_tokens") {
		t.Errorf("交付正文 = %q, want 原正文加截断标记", res.Text)
	}
}
