package messaging

import (
	"context"
	"sync"
	"testing"
	"time"
)

// recConsumer 记录型测试消费者：事件只读，按到达顺序保存指针。
type recConsumer struct {
	id    string
	types []EventType
	mu    sync.Mutex
	got   []*Event
}

func (r *recConsumer) ID() string { return r.id }

func (r *recConsumer) Types() []EventType { return r.types }

func (r *recConsumer) Consume(_ context.Context, e *Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, e)
	return nil
}

func (r *recConsumer) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

func (r *recConsumer) snapshot() []*Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]*Event, len(r.got))
	copy(cp, r.got)
	return cp
}

// newEvt 构造测试事件。
func newEvt(t EventType, content any) *Event {
	return &Event{Type: t, Content: content, Timestamp: time.Now()}
}

// waitFor 轮询等待条件满足（50ms 间隔，总超时 3s），避免裸 sleep 导致 flaky。
func waitFor(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for: %s", desc)
}

// Test1 过滤路由：按 Types() 精确路由，nil 订阅全部。
func Test1_DispatcherFilterRouting(t *testing.T) {
	d := NewMessageDispatcher(64)
	defer d.Shutdown()

	a := &recConsumer{id: "A", types: []EventType{EventType("tool_call")}}
	b := &recConsumer{id: "B"}
	d.Subscribe(a)
	d.Subscribe(b)

	_ = d.Publish(newEvt(EventType("tool_call"), "tc-1"))
	_ = d.Publish(newEvt(EventType("info"), "in-1"))

	waitFor(t, "A to receive 1 event", func() bool { return a.count() == 1 })
	waitFor(t, "B to receive 2 events", func() bool { return b.count() == 2 })

	if ae := a.snapshot()[0]; ae.Type != EventType("tool_call") {
		t.Fatalf("consumer A: want only tool_call, got %s", ae.Type)
	}
}

// Test2 同消费者保序：订阅全部，50 条严格按 0..49 送达（50 < bufferSize 不丢弃）。
func Test2_DispatcherPerConsumerOrder(t *testing.T) {
	d := NewMessageDispatcher(128)
	defer d.Shutdown()

	c := &recConsumer{id: "all"}
	d.Subscribe(c)

	for i := 0; i < 50; i++ {
		_ = d.Publish(newEvt(EventType("task_progress"), i))
	}

	waitFor(t, "consumer to receive 50 events", func() bool { return c.count() == 50 })

	for i, e := range c.snapshot() {
		if v, ok := e.Content.(int); !ok || v != i {
			t.Fatalf("order broken at index %d: got content %#v", i, e.Content)
		}
	}
}

// Test3 普通类超载不阻塞：channel 满载时发布立即返回（丢弃合法）。
func Test3_DispatcherOverflowNonBlocking(t *testing.T) {
	d := NewMessageDispatcher(1)
	defer d.Shutdown()

	c := &recConsumer{id: "slow"}
	d.Subscribe(c)

	start := time.Now()
	for i := 0; i < 1000; i++ {
		_ = d.Publish(newEvt(EventType("info"), i))
	}
	if elapsed := time.Since(start); elapsed >= 2*time.Second {
		t.Fatalf("publishing 1000 events must not block, took %v", elapsed)
	}

	d.Shutdown()
	if got := c.count(); got > 1000 {
		t.Fatalf("received %d events, want <= 1000 (drop is legal)", got)
	}
}

// Test4 关键类洪峰全量按序送达：user_help_response 旁路 FIFO 永不丢。
func Test4_DispatcherCriticalBypassFIFO(t *testing.T) {
	d := NewMessageDispatcher(1)
	defer d.Shutdown()

	c := &recConsumer{id: "crit"}
	d.Subscribe(c)

	for i := 0; i < 500; i++ {
		_ = d.Publish(newEvt(EventType("user_help_response"), i))
	}

	// 等待收齐 500 条，select+time.After(5s) 超时保护。
	deadline := time.After(5 * time.Second)
	for c.count() < 500 {
		select {
		case <-deadline:
			t.Fatalf("timeout: got %d/500 critical events", c.count())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	got := c.snapshot()
	if len(got) != 500 {
		t.Fatalf("want exactly 500 critical events, got %d", len(got))
	}
	for i, e := range got {
		if v, ok := e.Content.(int); !ok || v != i {
			t.Fatalf("critical order broken at index %d: got %#v", i, e.Content)
		}
	}
}

// Test5 Shutdown 幂等与 drain：二次调用不 panic；返回后消息全部送达；此后发布被丢弃。
func Test5_DispatcherShutdownIdempotentDrain(t *testing.T) {
	d := NewMessageDispatcher(64)
	c := &recConsumer{id: "drain"}
	d.Subscribe(c)

	for i := 0; i < 10; i++ {
		_ = d.Publish(newEvt(EventType("info"), i))
	}

	d.Shutdown() // 第一次：drain 完成
	d.Shutdown() // 第二次：幂等，不得 panic

	if got := c.count(); got != 10 {
		t.Fatalf("after drain: want all 10 events delivered, got %d", got)
	}

	// Shutdown 后发布：不 panic、不被送达。
	_ = d.Publish(newEvt(EventType("info"), "after-shutdown"))
	if got := c.count(); got != 10 {
		t.Fatalf("publish after shutdown must be dropped, got %d events", got)
	}
}
