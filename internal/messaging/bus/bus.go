package bus

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Event 是 P2P 消息信封
type Event struct {
	ID            string            `json:"id"`
	CorrelationID string            `json:"correlation_id"`
	Topic         string            `json:"topic"`
	Source        string            `json:"source"`
	Target        string            `json:"target"`
	Payload       []byte            `json:"payload"`
	Headers       map[string]string `json:"headers"`
	Timestamp     time.Time         `json:"timestamp"`
	Type          EventType         `json:"type"`
	Version       int64             `json:"version"`
}

type EventType int

const (
	EventPublish  EventType = iota
	EventRequest
	EventResponse
	EventError
)

// EventHandler 处理事件
type EventHandler func(ctx context.Context, ev *Event) error

// EventFilter 返回 true 表示事件应投递
type EventFilter func(*Event) bool

// Metrics 原子计数器
type Metrics struct {
	Published atomic.Int64
	Delivered atomic.Int64
	Dropped   atomic.Int64
}

// EventBus goroutine-safe 进程内 topic-based pub/sub
type EventBus struct {
	mu            sync.RWMutex
	subscriptions map[string]map[string]*subscription // topic → subID → sub
	observerMu    sync.RWMutex
	observers     map[string]EventHandler
	closed        atomic.Bool
	Metrics       Metrics
}

// Subscription 代表一个已注册的订阅
type Subscription interface {
	Topic() string
	Closed() bool
	Unsubscribe() error
}

type subscription struct {
	id        string
	topic     string
	agentID   string
	filter    EventFilter
	ch        chan *Event
	handler   EventHandler
	done      chan struct{}
	bus       *EventBus
	closed    atomic.Bool
	dropped   atomic.Int64
	processed atomic.Int64
}

func (s *subscription) Topic() string { return s.topic }
func (s *subscription) Closed() bool  { return s.closed.Load() }
func (s *subscription) Unsubscribe() error {
	return s.bus.Unsubscribe(s.topic, s.id)
}

// NewEventBus 创建一个新的 EventBus
func NewEventBus() *EventBus {
	return &EventBus{
		subscriptions: make(map[string]map[string]*subscription),
		observers:     make(map[string]EventHandler),
	}
}

// Subscribe 创建订阅。bufferSize 控制背压（默认 64）。
func (b *EventBus) Subscribe(topic, subID string, bufferSize int, filter EventFilter, handler EventHandler) (*subscription, error) {
	if b.closed.Load() {
		return nil, errors.New("eventbus: closed")
	}
	if bufferSize <= 0 {
		bufferSize = 64
	}
	sub := &subscription{
		id:      subID,
		topic:   topic,
		filter:  filter,
		ch:      make(chan *Event, bufferSize),
		handler: handler,
		done:    make(chan struct{}),
		bus:     b,
	}
	b.mu.Lock()
	if b.subscriptions[topic] == nil {
		b.subscriptions[topic] = make(map[string]*subscription)
	}
	if _, exists := b.subscriptions[topic][subID]; exists {
		b.mu.Unlock()
		return nil, fmt.Errorf("eventbus: subscription %q already exists on topic %q", subID, topic)
	}
	b.subscriptions[topic][subID] = sub
	b.mu.Unlock()

	go b.deliver(sub)
	return sub, nil
}

func (b *EventBus) deliver(sub *subscription) {
	defer close(sub.done)
	for ev := range sub.ch {
		if sub.closed.Load() {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := sub.handler(ctx, ev); err != nil {
			// 静默处理 handler 错误，避免影响其他订阅者
			_ = err
		}
		cancel()
		b.Metrics.Delivered.Add(1)
		sub.processed.Add(1)
	}
}

// Publish 广播事件到 topic 所有订阅者 + 通知 Observer。非阻塞。
func (b *EventBus) Publish(ctx context.Context, ev *Event) error {
	if b.closed.Load() {
		return errors.New("eventbus: closed")
	}
	b.Metrics.Published.Add(1)

	// 1. 通知 Observer —— 异步非阻塞
	b.notifyObservers(ctx, ev)

	// 2. 扇出到 topic 订阅者
	b.mu.RLock()
	subs := b.subscriptions[ev.Topic]
	subCopy := make([]*subscription, 0, len(subs))
	for _, s := range subs {
		subCopy = append(subCopy, s)
	}
	b.mu.RUnlock()

	for _, sub := range subCopy {
		if sub.closed.Load() {
			continue
		}
		if sub.filter != nil && !sub.filter(ev) {
			continue
		}
		// 定向消息检查
		if ev.Target != "" && ev.Target != sub.agentID && sub.agentID != "" {
			continue
		}
		select {
		case sub.ch <- ev:
		default:
			// 缓冲满 → 丢弃 + 计数
			sub.dropped.Add(1)
			b.Metrics.Dropped.Add(1)
		}
	}
	return nil
}

// AddObserver 注册全局观察者，接收所有 topic 的事件副本（用于 Conductor 态势感知）
func (b *EventBus) AddObserver(observerID string, handler EventHandler) error {
	b.observerMu.Lock()
	defer b.observerMu.Unlock()
	if _, exists := b.observers[observerID]; exists {
		return fmt.Errorf("eventbus: observer %q already exists", observerID)
	}
	b.observers[observerID] = handler
	return nil
}

// RemoveObserver 移除观察者
func (b *EventBus) RemoveObserver(observerID string) {
	b.observerMu.Lock()
	defer b.observerMu.Unlock()
	delete(b.observers, observerID)
}

func (b *EventBus) notifyObservers(ctx context.Context, ev *Event) {
	b.observerMu.RLock()
	handlers := make([]EventHandler, 0, len(b.observers))
	for _, h := range b.observers {
		handlers = append(handlers, h)
	}
	b.observerMu.RUnlock()

	for _, h := range handlers {
		go func(handler EventHandler, event *Event) {
			obsCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = handler(obsCtx, event) // 观察者错误不影响发布
		}(h, ev)
	}
}

// Unsubscribe 移除订阅
func (b *EventBus) Unsubscribe(topic, subID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	subs, ok := b.subscriptions[topic]
	if !ok {
		return fmt.Errorf("eventbus: topic %q not found", topic)
	}
	sub, ok := subs[subID]
	if !ok {
		return fmt.Errorf("eventbus: subscription %q not found on topic %q", subID, topic)
	}
	sub.closed.Store(true)
	close(sub.ch)
	delete(subs, subID)
	if len(subs) == 0 {
		delete(b.subscriptions, topic)
	}
	return nil
}

// Close 关闭总线，释放所有资源
func (b *EventBus) Close() error {
	if !b.closed.CompareAndSwap(false, true) {
		return nil
	}
	b.mu.Lock()
	for topic, subs := range b.subscriptions {
		for _, sub := range subs {
			sub.closed.Store(true)
			close(sub.ch)
		}
		delete(b.subscriptions, topic)
	}
	b.mu.Unlock()
	return nil
}
