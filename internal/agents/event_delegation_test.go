package agents

// event_delegation_test.go — C5 的接线闸门：委派坐标必须**自动**出现在子 Agent 的每条事件上。
//
// 为什么钉这一条而不是逐个调用点检查：绑定发生在 RunAgentLoop 入口一次，
// 之后每个 Publish 都经过同一个发布者。所以"漏一个点"这件事在这里被一次性封住 ——
// 断言的是"我收到的**每一条**事件都带坐标"，而不是"某一条带了"。

import (
	"context"
	"sync"
	"testing"

	"yagent/internal/llm"
	"yagent/internal/memory"
	"yagent/internal/messaging"
	"yagent/internal/tools"
)

type envelopeSpy struct {
	mu     sync.Mutex
	events []*messaging.Event
}

func (s *envelopeSpy) Consume(_ context.Context, e *messaging.Event) error {
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
	return nil
}
func (s *envelopeSpy) Types() []messaging.EventType { return nil }
func (s *envelopeSpy) ID() string                   { return "envelope-spy" }

func (s *envelopeSpy) snapshot() []*messaging.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*messaging.Event(nil), s.events...)
}

func TestRunAgentLoopStampsDelegationOnEveryEvent(t *testing.T) {
	d := messaging.NewDispatcher(messaging.DispatcherOptions{BufferSize: 1000, MaxRetries: 3})
	spy := &envelopeSpy{}
	d.Subscribe(spy)
	defer d.Shutdown()

	pub := messaging.NewMessagePublisher(d)
	// 委派坐标由 Director 的 runner 挂在 ctx 上（memory.WithDelegationID），
	// 子 Agent 这边只认 ctx —— 它不该知道也不该记得逐个事件去传。
	ctx := memory.WithDelegationID(context.Background(), "call_42")

	engine := &mockLLM{responses: []*llm.Response{
		newToolResponse(makeCall("c1", "ro_tool")),
		newTextResponse("done"),
	}}
	ro := tools.NewAdapter("ro_tool", "只读", func(context.Context, map[string]interface{}) (interface{}, error) {
		return "ok", nil
	}).WithReadOnly(true)

	cfg := ExecutorConfig{
		SystemPrompt: "test",
		UserInput:    "stamp me",
		Surface:      tools.NewSurfaceFromAdapters("coding", ro),
		LLM:          engine,
		MaxSteps:     3,
		Publisher:    pub,
		AgentName:    "coding",
	}
	if _, err := RunAgentLoop(ctx, cfg); err != nil {
		t.Fatalf("RunAgentLoop: %v", err)
	}

	got := spy.snapshot()
	if len(got) == 0 {
		t.Fatal("一条事件都没收到，这条测试什么也没钉住")
	}
	var missing []string
	for _, e := range got {
		if e.DelegationID != "call_42" {
			missing = append(missing, string(e.Type))
		}
	}
	if len(missing) > 0 {
		t.Errorf("%d/%d 条事件没有委派坐标（%v）—— 并行委派时它们会跟别的流粘在一起",
			len(missing), len(got), missing)
	}
}
