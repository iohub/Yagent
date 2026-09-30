// Package messaging —— MessageDispatcher 消息分发器。全局约定：
//   - 并发只读：同一条 *Event（及转换出的 *MessageEvent）可能并发分发给多个消费者，消费者必须只读，禁止修改字段。
//   - 普通事件满载即丢：非阻塞发送，队列满则丢弃并累计计数（丢弃日志限频 ≤1 条/秒）。
//   - 关键事件（用户交互回路 user_help_needed / user_help_response）永不丢：走独立旁路（mutex+slice FIFO，
//     防御上限 4096，超限 drop-oldest 并记 error 日志），发布方零阻塞。
//   - 顺序：同一消费者、同一类别（普通 or 关键）内 FIFO 保序；跨类别、跨消费者不承诺顺序。
//
// 与旧实现差异：bufferSize 现为每消费者独立队列容量（旧版为共享主 channel）；WAL/Backlog/DLQ/重试已随
// 对应类型移除。生命周期上绝不 close 事件 channel（并发 send 会 panic），只 close stop + 原子标志，收尾时
// 由消费 goroutine 排空残留事件。
package messaging

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// criticalBypassLimit 每消费者关键旁路防御上限，超限 drop-oldest。
	criticalBypassLimit = 4096
	// dropWarnInterval 普通事件丢弃 warn 日志限频间隔（每消费者 ≤1 条/秒）。
	dropWarnInterval = time.Second
)

// criticalEventTypes 关键事件类型（用户交互回路），走旁路永不丢弃。
var criticalEventTypes = map[EventType]struct{}{
	EventType("user_help_needed"):   {},
	EventType("user_help_response"): {},
}

func isCriticalEvent(t EventType) bool {
	_, ok := criticalEventTypes[t]
	return ok
}

// DispatcherOptions 调度器配置（保留旧结构保证 API 兼容）。
// BacklogSize/DLQSize/WALPath/EnableWAL/RetryDelay/ConsumerBufSize/DrainBacklogInterval 已废弃不生效。
type DispatcherOptions struct {
	BufferSize           int           // 每个消费者独立事件队列容量（默认 1000）
	ConsumerBufSize      int           // 已废弃
	BacklogSize          int           // 已废弃
	DLQSize              int           // 已废弃
	WALPath              string        // 已废弃
	EnableWAL            bool          // 已废弃
	RetryDelay           time.Duration // 已废弃
	MaxRetries           int           // Publish 填充 event.MaxRetries 的默认值（默认 3）
	DrainBacklogInterval time.Duration // 已废弃
}

// DefaultDispatcherOptions 返回默认配置
func DefaultDispatcherOptions() DispatcherOptions {
	return DispatcherOptions{
		BufferSize:           1000,
		ConsumerBufSize:      1000,
		BacklogSize:          10000,
		DLQSize:              1000,
		RetryDelay:           100 * time.Millisecond,
		MaxRetries:           3,
		DrainBacklogInterval: 50 * time.Millisecond,
	}
}

// dispatcherEntry 单个消费者的分发单元：过滤集合 + 独立事件队列 + 关键旁路。
type dispatcherEntry struct {
	id string

	// 二选一：新式 Consumer 或旧式 MessageConsumer（经 toMessageEvent 适配）。
	consumer Consumer
	legacy   MessageConsumer

	types map[EventType]struct{} // nil/空 = 订阅所有事件类型

	ch     chan *Event // 普通事件队列（容量 = bufferSize，满则丢弃）
	critMu sync.Mutex
	crit   []*Event      // 关键事件旁路（FIFO，永不丢）
	wake   chan struct{} // cap=1，旁路唤醒信号

	stop     chan struct{}
	dropped  atomic.Int64 // 普通事件累计丢弃数
	lastWarn atomic.Int64 // 上次丢弃 warn 的 UnixNano（限频用）
}

func (e *dispatcherEntry) match(t EventType) bool {
	if len(e.types) == 0 { // Types() 为 nil/空 = 订阅所有事件
		return true
	}
	_, ok := e.types[t]
	return ok
}

// push 投递事件：关键事件走旁路（锁内 append + 非阻塞 wake，永不丢），普通事件
// 非阻塞发送（满则丢弃 + 原子计数 + 限频 warn）。任何路径不阻塞，重入安全。
func (e *dispatcherEntry) push(event *Event) {
	if isCriticalEvent(event.Type) {
		e.critMu.Lock()
		if len(e.crit) >= criticalBypassLimit {
			oldest := e.crit[0]
			copy(e.crit, e.crit[1:])
			e.crit[len(e.crit)-1] = nil
			e.crit = e.crit[:len(e.crit)-1]
			slog.Error("MessageDispatcher: critical bypass overflow, drop-oldest",
				"consumer", e.id, "dropped_event_id", oldest.ID, "limit", criticalBypassLimit)
		}
		e.crit = append(e.crit, event)
		e.critMu.Unlock()
		select { // 非阻塞唤醒；信号可合并，drain 以 crit 内容为准
		case e.wake <- struct{}{}:
		default:
		}
		return
	}
	select {
	case e.ch <- event:
	default:
		total := e.dropped.Add(1)
		now := time.Now().UnixNano()
		for { // CAS 限频：每消费者丢弃 warn ≤1 条/秒
			last := e.lastWarn.Load()
			if now-last < int64(dropWarnInterval) {
				break
			}
			if e.lastWarn.CompareAndSwap(last, now) {
				slog.Warn("MessageDispatcher: consumer queue full, event dropped",
					"consumer", e.id, "event_type", string(event.Type), "dropped_total", total)
				break
			}
		}
	}
}

// MessageDispatcher 消息分发器 - 核心类。
type MessageDispatcher struct {
	mu      sync.RWMutex
	entries []*dispatcherEntry

	stopped           atomic.Bool // Shutdown 标志；置位后 Publish 丢弃、注册被拒
	perConsumerBuf    int         // 每消费者独立队列容量（= bufferSize）
	defaultMaxRetries int

	shutdownOnce sync.Once
	wg           sync.WaitGroup
}

// NewMessageDispatcher 创建带默认配置的调度器（向后兼容的旧接口）。
// bufferSize = 每个消费者独立的事件队列容量（与旧版共享主 channel 语义不同）。
func NewMessageDispatcher(bufferSize int) *MessageDispatcher {
	opts := DefaultDispatcherOptions()
	opts.BufferSize = bufferSize
	return NewDispatcher(opts)
}

// NewDispatcher 创建带完整配置的调度器（新接口；WAL/Backlog/DLQ 相关字段已废弃）。
func NewDispatcher(opts DispatcherOptions) *MessageDispatcher {
	if opts.BufferSize <= 0 {
		opts.BufferSize = 1000
	}
	if opts.MaxRetries <= 0 {
		opts.MaxRetries = 3
	}
	return &MessageDispatcher{
		perConsumerBuf:    opts.BufferSize,
		defaultMaxRetries: opts.MaxRetries,
	}
}

// Publish 发布事件。任何路径不阻塞；Shutdown 后丢弃并 warn、返回 nil 不 panic。
// 正常路径恒返回 nil（错误仅在 event 为 nil 时发生）。
func (d *MessageDispatcher) Publish(event *Event) error {
	if event == nil {
		return fmt.Errorf("cannot publish nil event")
	}
	if event.ID == "" {
		event.ID = fmt.Sprintf("evt-%d", time.Now().UnixNano())
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}
	if event.Priority == 0 {
		event.Priority = PriorityNormal
	}
	if event.MaxRetries <= 0 {
		event.MaxRetries = d.defaultMaxRetries
	}
	if d.stopped.Load() {
		slog.Warn("MessageDispatcher: publish after shutdown, event dropped",
			"event_id", event.ID, "event_type", string(event.Type))
		return nil
	}
	d.mu.RLock() // 快照后立刻放锁：Consume 内重入 Publish 不会死锁
	entries := make([]*dispatcherEntry, len(d.entries))
	copy(entries, d.entries)
	d.mu.RUnlock()
	for _, e := range entries {
		if e.match(event.Type) {
			e.push(event)
		}
	}
	return nil
}

// RegisterConsumer 注册消费者（旧接口，向后兼容）：广播接收全部事件类型。
func (d *MessageDispatcher) RegisterConsumer(consumer MessageConsumer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped.Load() {
		slog.Warn("MessageDispatcher: RegisterConsumer after shutdown, ignored",
			"consumer", fmt.Sprintf("%T", consumer))
		return
	}
	e := &dispatcherEntry{
		id:     fmt.Sprintf("legacy:%T", consumer),
		legacy: consumer,
		ch:     make(chan *Event, d.perConsumerBuf),
		wake:   make(chan struct{}, 1),
		stop:   make(chan struct{}),
	}
	d.entries = append(d.entries, e)
	d.wg.Add(1)
	go d.entryLoop(e)
}

// Subscribe 注册新式 Consumer（Types() 为 nil/空 = 订阅所有事件类型）。
func (d *MessageDispatcher) Subscribe(consumer Consumer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped.Load() {
		slog.Warn("MessageDispatcher: Subscribe after shutdown, ignored", "consumer", consumer.ID())
		return
	}
	var types map[EventType]struct{}
	if ts := consumer.Types(); len(ts) > 0 {
		types = make(map[EventType]struct{}, len(ts))
		for _, t := range ts {
			types[t] = struct{}{}
		}
	}
	e := &dispatcherEntry{
		id:       consumer.ID(),
		consumer: consumer,
		types:    types,
		ch:       make(chan *Event, d.perConsumerBuf),
		wake:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
	}
	d.entries = append(d.entries, e)
	d.wg.Add(1)
	go d.entryLoop(e)
}

// PublishCompat 兼容旧接口（void 返回），内部调用 Publish 并忽略返回值。
func (d *MessageDispatcher) PublishCompat(event *Event) {
	_ = d.Publish(event)
}

// Shutdown 优雅关闭（幂等）：置标志 → close 所有 stop → 等待 goroutine 排空剩余事件退出。
func (d *MessageDispatcher) Shutdown() {
	d.shutdownOnce.Do(func() {
		d.mu.Lock()
		d.stopped.Store(true)
		for _, e := range d.entries {
			close(e.stop) // 绝不 close 事件 channel（并发 send 会 panic）
		}
		d.mu.Unlock()
		d.wg.Wait()
	})
}

// entryLoop 每消费者独立消费 goroutine：优先 drain 关键旁路，再 select 等待。
func (d *MessageDispatcher) entryLoop(e *dispatcherEntry) {
	defer d.wg.Done()
	for {
		d.drainCritical(e) // 优先处理关键事件（wake 可能残留信号，drain 幂等）
		select {
		case event := <-e.ch:
			d.deliver(e, event)
		case <-e.wake: // 关键事件已到达，回循环顶部 drain 旁路
		case <-e.stop:
			d.drainRemaining(e)
			return
		}
	}
}

// drainCritical 批量摘取旁路后于锁外逐条交付（避免与重入 Publish 死锁）。
func (d *MessageDispatcher) drainCritical(e *dispatcherEntry) {
	for {
		batch := d.takeCriticalBatch(e)
		if len(batch) == 0 {
			return
		}
		for _, event := range batch {
			d.deliver(e, event)
		}
	}
}

func (d *MessageDispatcher) takeCriticalBatch(e *dispatcherEntry) []*Event {
	e.critMu.Lock()
	batch := e.crit
	e.crit = nil
	e.critMu.Unlock()
	return batch
}

// drainRemaining 关闭后收尾：排空普通队列与关键旁路中剩余事件后返回。stop 关闭
// 瞬间仍在途的 push 存在极小遗留窗口，属可接受的关闭期语义，不 panic 不阻塞。
func (d *MessageDispatcher) drainRemaining(e *dispatcherEntry) {
	for {
		for {
			select {
			case event := <-e.ch:
				d.deliver(e, event)
				continue
			default:
			}
			break
		}
		batch := d.takeCriticalBatch(e)
		for _, event := range batch {
			d.deliver(e, event)
		}
		if len(batch) == 0 {
			return
		}
	}
}

// deliver 同步交付事件；recover 防止单次 panic 杀死 goroutine 导致消费者永久静默。
// 不做重试（重试/DLQ 已随旧类型移除），超时由消费者自行控制。
func (d *MessageDispatcher) deliver(e *dispatcherEntry, event *Event) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("MessageDispatcher: consumer panic recovered",
				"consumer", e.id, "event_id", event.ID, "event_type", string(event.Type), "panic", r)
		}
	}()
	if e.legacy != nil {
		if err := e.legacy.Consume(toMessageEvent(event)); err != nil {
			slog.Warn("MessageDispatcher: legacy consumer error",
				"consumer", e.id, "event_id", event.ID, "err", err)
		}
		return
	}
	if err := e.consumer.Consume(context.Background(), event); err != nil {
		slog.Warn("MessageDispatcher: consumer error",
			"consumer", e.id, "event_id", event.ID, "err", err)
	}
}

// toMessageEvent 将 *Event 适配为旧接口的 *MessageEvent（字段映射照抄旧版）。
func toMessageEvent(event *Event) *MessageEvent {
	return &MessageEvent{
		Type:      event.Type,
		From:      event.Source,
		Content:   event.Content,
		Timestamp: event.Timestamp,
		Metadata:  event.Metadata,
	}
}
