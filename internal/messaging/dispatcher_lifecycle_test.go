package messaging

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// dispatcher_test.go 钉住了路由过滤、保序、满载不阻塞、关键旁路 FIFO 与 Shutdown 幂等。
// 这里补的是另外三类"坏了不报错、只是界面冻结"的路径：
// 消费者异常（panic/error）、Publisher 与 dispatcher 的重新绑定、以及默认值归一。

// panicConsumer 在第一件事上 panic，之后正常记录。
type panicConsumer struct {
	id      string
	mu      sync.Mutex
	got     []*Event
	panicOn EventType
}

func (c *panicConsumer) ID() string         { return c.id }
func (c *panicConsumer) Types() []EventType { return nil }
func (c *panicConsumer) Consume(_ context.Context, e *Event) error {
	if e.Type == c.panicOn {
		panic("消费者故意 panic")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, e)
	return nil
}
func (c *panicConsumer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.got)
}

// signalGateConsumer 与 gateConsumer 一样卡在 gate 上，但会额外广播"我进来了"，
// 让依赖"消费 goroutine 已被卡住"的用例变成确定性的。
type signalGateConsumer struct {
	id      string
	gate    chan struct{}
	started chan struct{}
	once    sync.Once
	mu      sync.Mutex
	got     []*Event
}

func (c *signalGateConsumer) ID() string         { return c.id }
func (c *signalGateConsumer) Types() []EventType { return nil }
func (c *signalGateConsumer) Consume(_ context.Context, e *Event) error {
	c.once.Do(func() { close(c.started) })
	<-c.gate
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, e)
	return nil
}

// errConsumer 每条都返回错误。
type errConsumer struct {
	mu     sync.Mutex
	got    int
	errRet error
}

func (c *errConsumer) ID() string         { return "err" }
func (c *errConsumer) Types() []EventType { return nil }
func (c *errConsumer) Consume(context.Context, *Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got++
	return c.errRet
}
func (c *errConsumer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.got
}

// legacyConsumer 走旧版 MessageConsumer 接口（广播，无 Types 过滤）。
type legacyConsumer struct {
	mu       sync.Mutex
	got      []*MessageEvent
	err      error
	attempts int32
}

func (c *legacyConsumer) Consume(e *MessageEvent) error {
	atomic.AddInt32(&c.attempts, 1)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.got = append(c.got, e)
	return c.err
}
func (c *legacyConsumer) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.got)
}

// ---------- 消费者异常：不能让某个消费者把整条订阅打死 ----------

// deliver() 里的 recover 是刻意的：一次 panic 若杀死消费 goroutine，该消费者
// 从此静默 —— TUI 表现为"事件不再刷新"，而发布方一切正常。
func TestDispatcherConsumerPanicIsRecoveredAndLoopSurvives(t *testing.T) {
	d := NewMessageDispatcher(64)
	defer d.Shutdown()

	c := &panicConsumer{id: "panicky", panicOn: EventType("boom")}
	d.Subscribe(c)

	if err := d.Publish(newEvt(EventType("boom"), 1)); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	// panic 之后同一订阅必须还能收到后续事件。
	if err := d.Publish(newEvt(EventType("info"), 2)); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	waitFor(t, "post-panic event to still be delivered", func() bool { return c.count() == 1 })
	if got := c.snapshotLike(); len(got) != 1 || got[0].Content != 2 {
		t.Errorf("收到的事件 = %+v, want 只有 panic 之后那条", got)
	}
}

func (c *panicConsumer) snapshotLike() []*Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*Event(nil), c.got...)
}

// 消费者返回 error 同样不能中断后续投递（当前实现只记 warn，不重试）。
func TestDispatcherConsumerErrorDoesNotStopDelivery(t *testing.T) {
	d := NewMessageDispatcher(64)
	defer d.Shutdown()

	c := &errConsumer{errRet: errors.New("消费者拒绝")}
	d.Subscribe(c)

	for i := 0; i < 5; i++ {
		_ = d.Publish(newEvt(EventType("info"), i))
	}
	waitFor(t, "all 5 events attempted", func() bool { return c.count() == 5 })
	if got := c.count(); got != 5 {
		t.Errorf("收到 %d 条, want 5（返回 error 之后仍要继续投递）", got)
	}

	// 旧接口同样如此：Consume 返回 error 只记日志。
	leg := &legacyConsumer{err: errors.New("legacy 拒绝")}
	d.RegisterConsumer(leg)
	_ = d.Publish(newEvt(EventType("info"), "x"))
	waitFor(t, "legacy consumer to keep receiving", func() bool { return leg.count() == 1 })
	if got := leg.count(); got != 1 {
		t.Errorf("旧接口收到 %d 条, want 1", got)
	}
	// error 既不重试也不吞事件：每条只被投递一次（重试机制若被启用，这里会变成 >1）。
	if n := atomic.LoadInt32(&leg.attempts); n != 1 {
		t.Errorf("Consume 被调用 %d 次, want 1（当前实现不重试）", n)
	}
}

// ---------- 旧接口的适配形状 ----------

// MessageEvent 是 Event 的类型别名，所以 toMessageEvent 实际是**造一份裁剪过的副本**：
// Source 被搬到 From，而 ID/Target/Priority/TraceID/MaxRetries 不随副本走。
// 这条现状必须写死：任何"直接传原指针"或"少拷 Metadata"的改动都会改变旧消费者
// 看到的内容，而旧消费者（HTTP WS 广播）就靠 Metadata 里的 request_id 配对。
func TestLegacyConsumerReceivesAdaptedCopy(t *testing.T) {
	d := NewMessageDispatcher(8)
	defer d.Shutdown()

	leg := &legacyConsumer{}
	d.RegisterConsumer(leg)

	original := &Event{
		ID:       "evt-42",
		Type:     EventType("ai_chunk"),
		Source:   "director",
		Target:   "tui",
		Content:  "hello",
		Priority: PriorityHigh,
		TraceID:  "trace-1",
		Metadata: map[string]interface{}{"request_id": "r-1"},
	}
	_ = d.Publish(original)
	waitFor(t, "legacy delivery", func() bool { return leg.count() == 1 })

	leg.mu.Lock()
	got := leg.got[0]
	leg.mu.Unlock()

	if got.Content != "hello" || got.Type != EventType("ai_chunk") {
		t.Errorf("内容/类型没有传达到旧接口: %+v", got)
	}
	if got.From != "director" {
		t.Errorf("From = %q, want Source 被映射成 deprecated 的 From 字段", got.From)
	}
	if rid, _ := got.Metadata["request_id"].(string); rid != "r-1" {
		t.Errorf("Metadata = %v, want 保留 request_id（授权回路靠它配对）", got.Metadata)
	}
	if got.ID != "" {
		t.Errorf("ID = %q, want 旧接口副本不带 ID（现状：toMessageEvent 只搬 5 个字段）", got.ID)
	}
	// 副本语义正是"并发只读"约定的边界：旧消费者拿到的对象与原始事件不同，
	// 改动它不会污染其它消费者。
	if got == original {
		t.Error("旧接口拿到了原始指针, want toMessageEvent 造的副本")
	}

	// 新式消费者拿到的是同一个指针（不拷贝）——两种接口的隔离性不同，必须钉住。
	rec := &recConsumer{id: "new"}
	d.Subscribe(rec)
	_ = d.Publish(original)
	waitFor(t, "new consumer delivery", func() bool { return rec.count() == 1 })
	if snap := rec.snapshot(); snap[0] != original {
		t.Error("新式消费者拿到了副本, want 与发布方共享同一 *Event（所以消费者必须只读）")
	}
}

// ---------- 默认值归一 ----------

// Publish 会补齐 ID/Timestamp/Priority/MaxRetries。ID 是 user_help 请求-响应配对的
// 依据之一，MaxRetries 影响 ShouldRetry；补齐必须只针对零值。
func TestPublishFillsDefaultsAndPreservesExplicit(t *testing.T) {
	d := NewDispatcher(DispatcherOptions{BufferSize: 8, MaxRetries: 7})
	defer d.Shutdown()

	c := &recConsumer{id: "defaults"}
	d.Subscribe(c)

	_ = d.Publish(&Event{Type: EventType("info"), Content: "auto"})
	stamp := time.Now().Add(-time.Hour)
	_ = d.Publish(&Event{
		ID:         "keep-me",
		Type:       EventType("info"),
		Content:    "explicit",
		Priority:   PriorityCritical,
		Timestamp:  stamp,
		MaxRetries: 1,
	})

	waitFor(t, "two events", func() bool { return c.count() == 2 })
	got := c.snapshot()

	first := got[0]
	if first.ID == "" {
		t.Error("ID 没有被补齐")
	}
	if first.Timestamp.IsZero() {
		t.Error("Timestamp 没有被补齐")
	}
	if first.Priority != PriorityNormal {
		t.Errorf("Priority = %v, want 零值补成 PriorityNormal", first.Priority)
	}
	if first.MaxRetries != 7 {
		t.Errorf("MaxRetries = %d, want 取调度器默认 7", first.MaxRetries)
	}

	second := got[1]
	if second.ID != "keep-me" {
		t.Errorf("显式 ID 被改写 = %q", second.ID)
	}
	if !second.Timestamp.Equal(stamp) {
		t.Errorf("显式 Timestamp 被改写 = %v", second.Timestamp)
	}
	if second.Priority != PriorityCritical {
		t.Errorf("显式 Priority = %v, want 保留 critical", second.Priority)
	}
	if second.MaxRetries != 1 {
		t.Errorf("显式 MaxRetries = %d, want 保留 1", second.MaxRetries)
	}

	// nil 事件必须是错误而不是 panic。
	if err := d.Publish(nil); err == nil {
		t.Error("Publish(nil) = nil error, want 报错")
	}
}

// 零值 DispatcherOptions 必须落到可用默认：BufferSize=0 会让每个消费者的队列
// 容量为 0，于是每一条普通事件都被丢弃 —— 界面彻底冻结而不报任何错。
func TestDispatcherOptionsNormalizeZeroValues(t *testing.T) {
	d := NewDispatcher(DispatcherOptions{})
	defer d.Shutdown()

	c := &recConsumer{id: "buf"}
	d.Subscribe(c)
	for i := 0; i < 50; i++ {
		_ = d.Publish(newEvt(EventType("info"), i))
	}
	waitFor(t, "50 events through a default-sized buffer", func() bool { return c.count() == 50 })

	if got := d.defaultMaxRetries; got != 3 {
		t.Errorf("defaultMaxRetries = %d, want 3", got)
	}
	if got := d.perConsumerBuf; got != 1000 {
		t.Errorf("perConsumerBuf = %d, want 1000", got)
	}
	// 负数同样按"没写"处理。
	neg := NewDispatcher(DispatcherOptions{BufferSize: -5, MaxRetries: -1})
	defer neg.Shutdown()
	if neg.perConsumerBuf != 1000 || neg.defaultMaxRetries != 3 {
		t.Errorf("负值未归一: buf=%d retries=%d", neg.perConsumerBuf, neg.defaultMaxRetries)
	}
}

// ---------- 订阅者之间的隔离 ----------

// bufferSize 是**每消费者**容量（与旧版共享主 channel 的语义不同）。
// 一个卡住的订阅者不能把别人的队列也占满 —— 否则一个慢 WS 连接会冻结 TUI。
func TestDispatcherBuffersArePerConsumer(t *testing.T) {
	d := NewMessageDispatcher(8)
	defer d.Shutdown()

	blocked := &gateConsumer{id: "slow", gate: make(chan struct{})}
	fast := &recConsumer{id: "fast"}
	d.Subscribe(blocked)
	d.Subscribe(fast)

	for i := 0; i < 8; i++ {
		_ = d.Publish(newEvt(EventType("info"), i))
	}
	waitFor(t, "fast consumer to receive all 8", func() bool { return fast.count() == 8 })
	if got := fast.count(); got != 8 {
		t.Errorf("健康订阅者只收到 %d/8 条, want 全部（另一个订阅者卡住不该波及它）", got)
	}

	close(blocked.gate)
	waitFor(t, "blocked consumer to drain its own queue", func() bool { return blocked.count() >= 1 })
	if got := blocked.count(); got == 0 {
		t.Error("卡住的订阅者解开后一条都没收到")
	}
	if got := fast.count(); got != 8 {
		t.Errorf("慢订阅者解开之后，健康订阅者变成 %d/8, want 仍是 8", got)
	}
}

// 普通队列满载时，关键事件仍然必须送达（它根本不走那条 channel）。
func TestCriticalEventSurvivesFullNormalQueue(t *testing.T) {
	d := NewMessageDispatcher(1)
	defer d.Shutdown()

	blocked := &gateConsumer{id: "blocked", gate: make(chan struct{})}
	d.Subscribe(blocked)

	// 普通队列容量 1：消费 goroutine 卡在第一条上，其余全部被丢弃。
	for i := 0; i < 200; i++ {
		_ = d.Publish(newEvt(EventType("info"), i))
	}

	for i := 0; i < 20; i++ {
		_ = d.Publish(newEvt(EventType("user_help_needed"), i))
	}

	close(blocked.gate)
	waitFor(t, "all 20 critical events to arrive", func() bool {
		blocked.mu.Lock()
		defer blocked.mu.Unlock()
		n := 0
		for _, e := range blocked.got {
			if e.Type == EventType("user_help_needed") {
				n++
			}
		}
		return n == 20
	})

	blocked.mu.Lock()
	defer blocked.mu.Unlock()
	if len(blocked.got) == 220 {
		t.Error("普通事件一条没丢 —— 满载路径没有被走到")
	}
}

// 旁路有 4096 的防御上限，超限 drop-oldest（保新弃旧）。
// 用户交互回路一次最多一问一答，走到这里说明有消费者彻底停了 ——
// 但即便如此也不能无界吃内存。
func TestCriticalBypassDropsOldestOverLimit(t *testing.T) {
	d := NewMessageDispatcher(1)
	defer d.Shutdown()

	blocked := &signalGateConsumer{id: "blocked", gate: make(chan struct{}), started: make(chan struct{})}
	d.Subscribe(blocked)

	// 第一条被消费 goroutine 取走并卡在 gate 上，其余全留在旁路。
	// 必须等到它真的进去了再灌洪峰，否则"哪几条被丢掉"取决于调度。
	_ = d.Publish(newEvt(EventType("user_help_needed"), 0))
	select {
	case <-blocked.started:
	case <-time.After(3 * time.Second):
		t.Fatal("消费 goroutine 没有进入第一条关键事件")
	}
	const total = criticalBypassLimit + 50
	for i := 1; i < total; i++ {
		_ = d.Publish(newEvt(EventType("user_help_needed"), i))
	}

	close(blocked.gate)
	waitFor(t, "blocked consumer to finish draining", func() bool {
		blocked.mu.Lock()
		defer blocked.mu.Unlock()
		return len(blocked.got) >= criticalBypassLimit
	})

	blocked.mu.Lock()
	defer blocked.mu.Unlock()
	if len(blocked.got) != criticalBypassLimit+1 {
		t.Fatalf("收到 %d 条, want %d（在途 1 条 + 旁路保住的 %d 条）",
			len(blocked.got), criticalBypassLimit+1, criticalBypassLimit)
	}
	// drop-oldest：最先被丢掉的是旁路里的 1 号，所以第一条旁路事件是 50 号之后。
	if first, _ := blocked.got[1].Content.(int); first != total-criticalBypassLimit {
		t.Errorf("旁路首条 = %d, want %d（drop-oldest 保新弃旧）", first, total-criticalBypassLimit)
	}
}

// ---------- 生命周期 ----------

// Shutdown 之后注册必须被拒绝：entryLoop 已经不再启动，
// 静默收下只会让调用方以为订阅成功、然后永远收不到事件。
func TestRegistrationAfterShutdownIsIgnored(t *testing.T) {
	d := NewMessageDispatcher(8)
	d.Shutdown()

	c := &recConsumer{id: "late"}
	d.Subscribe(c)
	leg := &legacyConsumer{}
	d.RegisterConsumer(leg)

	_ = d.Publish(newEvt(EventType("info"), "x"))
	if err := d.Publish(newEvt(EventType("info"), "y")); err != nil {
		t.Errorf("Publish after shutdown 返回错误 %v, want nil（丢弃是合法收尾）", err)
	}
	time.Sleep(50 * time.Millisecond)
	if c.count() != 0 || leg.count() != 0 {
		t.Errorf("Shutdown 后注册的消费者收到了事件: new=%d legacy=%d, want 0/0", c.count(), leg.count())
	}
	if n := len(d.entries); n != 0 {
		t.Errorf("entries = %d, want 0（注册被拒绝而不是留下一个没有 goroutine 的条目）", n)
	}
}

// Shutdown 时旁路里还压着的关键事件必须被排空交付 —— 否则用户已经答完的问题
// 会在任务结束的那一刻凭空消失。
func TestShutdownDrainsCriticalBypass(t *testing.T) {
	d := NewMessageDispatcher(4)
	c := &recConsumer{id: "drain-crit"}
	d.Subscribe(c)

	for i := 0; i < 30; i++ {
		_ = d.Publish(newEvt(EventType("user_help_response"), i))
	}
	d.Shutdown() // 不 sleep，直接关：drainRemaining 负责收尾

	if got := c.count(); got != 30 {
		t.Fatalf("Shutdown 后排空只交付了 %d/30 条关键事件", got)
	}
	snap := c.snapshot()
	for i, e := range snap {
		if v, _ := e.Content.(int); v != i {
			t.Fatalf("关键事件顺序在收尾时被打破: index %d → %#v", i, e.Content)
		}
	}
}

// ---------- 丢弃日志限频 ----------

// 满载丢弃的 warn 是每消费者 ≤1 条/秒：一个卡死的 WS 连接每秒能制造上万次丢弃，
// 不限频就等于用日志把进程打死。
func TestDropWarningIsRateLimited(t *testing.T) {
	h := &countHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })

	d := NewMessageDispatcher(1)
	blocked := &gateConsumer{id: "blocked", gate: make(chan struct{})}
	d.Subscribe(blocked)

	for i := 0; i < 500; i++ {
		_ = d.Publish(newEvt(EventType("ai_chunk"), i))
	}
	h.mu.Lock()
	drops := h.dropped
	h.mu.Unlock()

	close(blocked.gate)
	d.Shutdown()

	if drops == 0 {
		t.Fatal("队列满载却没有一条丢弃日志 —— 丢弃路径静默失效")
	}
	if drops > 5 {
		t.Errorf("丢弃日志 %d 条, want 限频后是个位数（500 次丢弃）", drops)
	}
}

type countHandler struct {
	mu      sync.Mutex
	dropped int
}

func (h *countHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *countHandler) Handle(_ context.Context, r slog.Record) error {
	if strings.Contains(r.Message, "queue full, event dropped") {
		h.mu.Lock()
		h.dropped++
		h.mu.Unlock()
	}
	return nil
}

func (h *countHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *countHandler) WithGroup(string) slog.Handler      { return h }

// ---------- Publisher ----------

// app.go:531 每个任务都 SetDispatcher 重指全局 publisher。若赋值没生效，
// 事件继续流向已死的旧 dispatcher —— 表现为界面永久停在上一轮。
func TestPublisherSetDispatcherTakesEffect(t *testing.T) {
	first := NewMessageDispatcher(8)
	second := NewMessageDispatcher(8)

	a := &recConsumer{id: "on-first"}
	b := &recConsumer{id: "on-second"}
	first.Subscribe(a)
	second.Subscribe(b)

	p := NewMessagePublisher(first)
	if err := p.Publish("info", "1", "src"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	p.SetDispatcher(second)
	if err := p.Publish("info", "2", "src"); err != nil {
		t.Fatalf("Publish: %v", err)
	}

	waitFor(t, "first dispatcher got exactly one", func() bool { return a.count() == 1 })
	waitFor(t, "second dispatcher got exactly one", func() bool { return b.count() == 1 })

	if got := a.snapshot()[0]; got.Source != "src" || got.Content != "1" {
		t.Errorf("第一条事件 = %+v, want Source=src Content=1", got)
	}
	if got := b.snapshot()[0]; got.Content != "2" {
		t.Errorf("重指之后的事件 = %+v, want Content=2", got)
	}

	first.Shutdown()
	second.Shutdown()
}

// 带元数据的发布是授权回路（request_id）的载体：Metadata 必须原样传下去。
func TestPublisherWithMetadata(t *testing.T) {
	d := NewMessageDispatcher(8)
	defer d.Shutdown()

	c := &recConsumer{id: "meta"}
	d.Subscribe(c)

	p := NewMessagePublisher(d)
	md := map[string]interface{}{"request_id": "r-9", "tool": "run_bash"}
	if err := p.PublishWithMetadata("user_help_needed", "content", "coding", md); err != nil {
		t.Fatalf("PublishWithMetadata: %v", err)
	}
	waitFor(t, "metadata event", func() bool { return c.count() == 1 })

	got := c.snapshot()[0]
	if got.Type != EventType("user_help_needed") || got.Source != "coding" {
		t.Errorf("事件形状 = %+v", got)
	}
	if rid, _ := got.Metadata["request_id"].(string); rid != "r-9" {
		t.Errorf("Metadata = %v, want 原样带上 request_id", got.Metadata)
	}
}

// dispatcher 为 nil 时发布必须是 no-op：Agent 在未装配事件总线时仍然要能跑完。
func TestPublisherNilDispatcherIsNoOp(t *testing.T) {
	p := NewMessagePublisher(nil)
	if err := p.Publish("info", "x", "src"); err != nil {
		t.Errorf("Publish with nil dispatcher = %v, want nil", err)
	}
	if err := p.PublishWithMetadata("info", "x", "src", nil); err != nil {
		t.Errorf("PublishWithMetadata with nil dispatcher = %v, want nil", err)
	}
	// PublishCompat 的忽略错误语义：nil 事件不 panic。
	d := NewMessageDispatcher(4)
	defer d.Shutdown()
	d.PublishCompat(nil)
}
