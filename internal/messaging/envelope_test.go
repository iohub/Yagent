package messaging

// envelope_test.go — C5 的信封闸门：序号、委派坐标、以及"丢了要能数出来"。

import (
	"context"
	"sync"
	"testing"
	"time"
)

type collectConsumer struct {
	id    string
	mu    sync.Mutex
	got   []*Event
	block chan struct{} // 非 nil 时 Consume 会等它，用来制造满载
}

func (c *collectConsumer) Consume(_ context.Context, e *Event) error {
	if c.block != nil {
		<-c.block
	}
	c.mu.Lock()
	c.got = append(c.got, e)
	c.mu.Unlock()
	return nil
}
func (c *collectConsumer) Types() []EventType { return nil }
func (c *collectConsumer) ID() string         { return c.id }

func (c *collectConsumer) snapshot() []*Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*Event(nil), c.got...)
}

// 序号严格递增且不留洞：这是"界面少了字"第一次变成一个可以量化的问题。
func TestDispatcherStampsMonotonicSeq(t *testing.T) {
	d := NewDispatcher(DefaultDispatcherOptions())
	consumer := &collectConsumer{id: "collector"}
	d.Subscribe(consumer)

	const n = 200
	for i := 0; i < n; i++ {
		if err := d.Publish(&Event{Type: EventType("ai_chunk"), Source: "coding"}); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}
	if err := waitForLen(t, consumer, n); err != nil {
		t.Fatalf("%v", err)
	}
	d.Shutdown()

	got := consumer.snapshot()
	for i, e := range got {
		if e.Seq != int64(i+1) {
			t.Fatalf("第 %d 条事件的 Seq = %d, want %d —— 序号不连续就无法判断少了哪条", i, e.Seq, i+1)
		}
	}
}

// 绑定的视图与母体共享同一个 dispatcher：任务切换时换的是 dispatcher，
// 如果 ForDelegation 拷走了指针，已绑定的那份会一直发往旧 dispatcher —— 事件静默消失。
func TestForDelegationSharesDispatcherWithParent(t *testing.T) {
	first := NewDispatcher(DefaultDispatcherOptions())
	second := NewDispatcher(DefaultDispatcherOptions())

	pub := NewMessagePublisher(first)
	bound := pub.ForDelegation("call_7")

	colA := &collectConsumer{id: "a"}
	first.Subscribe(colA)
	colB := &collectConsumer{id: "b"}
	second.Subscribe(colB)

	if err := bound.Publish("ai_chunk", "x", "coding"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := waitForLen(t, colA, 1); err != nil {
		t.Fatalf("绑定视图没发到当前 dispatcher: %v", err)
	}
	if e := colA.snapshot()[0]; e.DelegationID != "call_7" {
		t.Errorf("DelegationID = %q, want call_7", e.DelegationID)
	}

	// 换 dispatcher 之后，同一个绑定视图必须跟着走。
	pub.SetDispatcher(second)
	if err := bound.PublishWithMetadata("ai_response", "y", "coding", map[string]interface{}{"k": 1}); err != nil {
		t.Fatalf("publish after swap: %v", err)
	}
	if err := waitForLen(t, colB, 1); err != nil {
		t.Fatalf("换 dispatcher 后绑定视图仍发往旧的（事件会静默消失）: %v", err)
	}
	if e := colB.snapshot()[0]; e.DelegationID != "call_7" {
		t.Errorf("换手后 DelegationID 丢了：%q, want call_7", e.DelegationID)
	}
	// 序号是按 dispatcher 计的，新 dispatcher 从 1 开始
	if e := colB.snapshot()[0]; e.Seq != 1 {
		t.Errorf("新 dispatcher 的 Seq = %d, want 1", e.Seq)
	}

	first.Shutdown()
	second.Shutdown()
}

// 空坐标时 ForDelegation 返回自己，不制造一层没有意义的包装。
func TestForDelegationEmptyIsIdentity(t *testing.T) {
	pub := NewMessagePublisher(NewDispatcher(DefaultDispatcherOptions()))
	if pub.ForDelegation("") != pub {
		t.Error("空坐标不该产生新视图")
	}
}

// 满载丢弃必须能被数出来：并行委派后 ai_chunk 翻倍，丢字是设计内行为，
// 但"丢了"不能是没有痕迹的事。
func TestDroppedCountsOverflow(t *testing.T) {
	d := NewDispatcher(DispatcherOptions{BufferSize: 1, MaxRetries: 3})
	blocked := &collectConsumer{id: "slow", block: make(chan struct{})}
	d.Subscribe(blocked)

	// 第一条被 Consume 卡住，后续把 1 格队列塞满后开始丢。
	for i := 0; i < 20; i++ {
		_ = d.Publish(&Event{Type: EventType("ai_chunk"), Source: "coding"})
	}
	time.Sleep(50 * time.Millisecond)

	if got := d.Dropped(); got == 0 {
		t.Error("Dropped() = 0, want >0 —— 队列只有 1 格，20 条不可能全收下")
	}
	close(blocked.block)
	d.Shutdown()

	if got := d.Dropped(); got <= 0 {
		t.Errorf("Shutdown 之后 Dropped() = %d", got)
	}
}

func waitForLen(t *testing.T, c *collectConsumer, want int) error {
	t.Helper()
	for i := 0; i < 400; i++ {
		if len(c.snapshot()) >= want {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return context.DeadlineExceeded
}
