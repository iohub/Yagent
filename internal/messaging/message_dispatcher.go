package messaging

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// DispatcherOptions 调度器配置
type DispatcherOptions struct {
	BufferSize           int           // 主 channel 缓冲大小（默认 1000）
	ConsumerBufSize      int           // 每个 consumer channel 缓冲大小（默认 1000）
	BacklogSize          int           // Backlog 最大容量（0=无限制，默认 10000）
	DLQSize              int           // 死信队列大小（0=无限制，默认 1000）
	WALPath              string        // WAL 文件路径（空=不启用 WAL）
	EnableWAL            bool          // 是否启用 WAL
	RetryDelay           time.Duration // 重试基础间隔（默认 100ms）
	MaxRetries           int           // 默认最大重试次数（默认 3）
	DrainBacklogInterval time.Duration // backlog 排空检查间隔（默认 50ms）
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

// legacyConsumerInfo 包装旧的 MessageConsumer
type legacyConsumerInfo struct {
	consumer MessageConsumer
	ch       chan *MessageEvent
}

// MessageDispatcher 消息调度器 - 核心类
// 内部集成了 WAL + Backlog + DLQ + 重试机制
type MessageDispatcher struct {
	// 主队列（使用 Event 而非 MessageEvent）
	mainCh chan *Event

	// consumers 按 EventType 索引
	consumers map[EventType][]Consumer

	// 兼容旧接口的 consumers（用类型名索引）
	legacyConsumers []*legacyConsumerInfo

	// WAL 持久化
	wal WAL

	// Backlog（channel 满时溢出）
	backlog *Backlog

	// 死信队列
	dlq *DeadLetterQueue

	// 配置
	opts DispatcherOptions

	mu         sync.RWMutex
	ctx        context.Context
	cancelFunc context.CancelFunc
	wg         sync.WaitGroup
}

// NewMessageDispatcher 创建带默认配置的调度器（向后兼容的旧接口）
// bufferSize 用于主 channel，其他使用默认值
func NewMessageDispatcher(bufferSize int) *MessageDispatcher {
	opts := DefaultDispatcherOptions()
	opts.BufferSize = bufferSize
	return NewDispatcher(opts)
}

// NewDispatcher 创建带完整配置的调度器（新接口）
func NewDispatcher(opts DispatcherOptions) *MessageDispatcher {
	if opts.BufferSize <= 0 {
		opts.BufferSize = 1000
	}
	if opts.ConsumerBufSize <= 0 {
		opts.ConsumerBufSize = 1000
	}
	if opts.BacklogSize <= 0 {
		opts.BacklogSize = 10000
	}
	if opts.DLQSize <= 0 {
		opts.DLQSize = 1000
	}
	if opts.RetryDelay <= 0 {
		opts.RetryDelay = 100 * time.Millisecond
	}
	if opts.MaxRetries <= 0 {
		opts.MaxRetries = 3
	}
	if opts.DrainBacklogInterval <= 0 {
		opts.DrainBacklogInterval = 50 * time.Millisecond
	}

	ctx, cancel := context.WithCancel(context.Background())

	d := &MessageDispatcher{
		mainCh:          make(chan *Event, opts.BufferSize),
		consumers:       make(map[EventType][]Consumer),
		legacyConsumers: make([]*legacyConsumerInfo, 0),
		backlog:         NewBacklog(opts.BacklogSize),
		dlq:             NewDeadLetterQueue(opts.DLQSize),
		opts:            opts,
		ctx:             ctx,
		cancelFunc:      cancel,
	}

	// 初始化 WAL（如果启用）
	if opts.EnableWAL && opts.WALPath != "" {
		wal, err := NewFileWAL(WALOptions{
			FilePath: opts.WALPath,
		})
		if err != nil {
			slog.Warn("Failed to initialize WAL, continuing without it", "path", opts.WALPath, "err", err)
			d.wal = &NoopWAL{}
		} else {
			d.wal = wal
			// 启动时回放未确认事件
			go d.replayUnacked()
		}
	} else {
		d.wal = &NoopWAL{}
	}

	// 启动主循环
	d.wg.Add(1)
	go d.mainLoop()

	// 启动 backlog 排空协程
	d.wg.Add(1)
	go d.drainBacklogLoop()

	return d
}

// mainLoop 主循环：从 mainCh 读取事件并分发
func (d *MessageDispatcher) mainLoop() {
	defer d.wg.Done()
	for {
		select {
		case event := <-d.mainCh:
			d.dispatchToConsumers(event)
		case <-d.ctx.Done():
			// 优雅关闭前排空
			for {
				select {
				case event := <-d.mainCh:
					d.dispatchToConsumers(event)
				default:
					return
				}
			}
		}
	}
}

// drainBacklogLoop 定期从 backlog 取事件重试分发
func (d *MessageDispatcher) drainBacklogLoop() {
	defer d.wg.Done()
	ticker := time.NewTicker(d.opts.DrainBacklogInterval)
	defer ticker.Stop()

	for {
		select {
		case <-d.ctx.Done():
			return
		case <-d.backlog.NotifyChan():
			d.drainBacklog()
		case <-ticker.C:
			if d.backlog.Len() > 0 {
				d.drainBacklog()
			}
		}
	}
}

func (d *MessageDispatcher) drainBacklog() {
	for {
		event, ok := d.backlog.Pop()
		if !ok {
			return
		}
		if !d.tryDispatch(event) {
			// 还是满，放回 backlog
			d.backlog.Push(event)
			return
		}
	}
}

// Publish 发布事件（新接口，返回 error）
func (d *MessageDispatcher) Publish(event *Event) error {
	if event == nil {
		return fmt.Errorf("cannot publish nil event")
	}

	// 填充默认字段
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
		event.MaxRetries = d.opts.MaxRetries
	}

	// 1. 先写 WAL（如果启用）
	if d.wal != nil {
		if err := d.wal.Append(event); err != nil {
			slog.Warn("WAL append failed, publishing without persistence", "event_id", event.ID, "err", err)
		}
	}

	// 2. 尝试直接投递到 mainCh
	select {
	case d.mainCh <- event:
		return nil
	default:
		// mainCh 满，入 backlog
	}

	// 3. 入 backlog
	if err := d.backlog.Push(event); err != nil {
		// 4. backlog 也满，入死信队列
		d.dlq.Push(event, fmt.Errorf("backlog full: %w", err))
		return fmt.Errorf("event %s moved to DLQ: backlog full", event.ID)
	}

	return nil
}

// tryDispatch 尝试直接分发事件到 consumers
// 返回 true = 成功分发，false = channel 满
func (d *MessageDispatcher) tryDispatch(event *Event) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// 1. 按 EventType 分发给新 Consumer
	if consumers, ok := d.consumers[event.Type]; ok {
		for _, consumer := range consumers {
			// 获取 consumer 的 inbox
			if ep, ok := consumer.(interface{ Inbox() chan *Event }); ok {
				select {
				case ep.Inbox() <- event:
				default:
					return false // channel 满
				}
			}
		}
	}

	// 2. 分发给旧版 legacy consumers（广播给所有）
	for _, li := range d.legacyConsumers {
		// 构造兼容的 MessageEvent
		msgEvent := &MessageEvent{
			Type:      event.Type,
			From:      event.Source,
			Content:   event.Content,
			Timestamp: event.Timestamp,
			Metadata:  event.Metadata,
		}
		select {
		case li.ch <- msgEvent:
		default:
			return false
		}
	}

	return true
}

// dispatchToConsumers 分发事件（从 mainCh 收到后调用）
func (d *MessageDispatcher) dispatchToConsumers(event *Event) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	// 1. 分发给新 Consumer
	if consumers, ok := d.consumers[event.Type]; ok {
		for _, consumer := range consumers {
			d.wg.Add(1)
			go d.consumeWithRetry(consumer, event)
		}
	}

	// 如果没有任何 Consumer 订阅此类型且无 legacy consumer，视为已处理
	if len(d.consumers) == 0 && len(d.legacyConsumers) == 0 {
		return
	}

	// 2. 分发给旧版 legacy consumers（广播所有事件）
	for _, li := range d.legacyConsumers {
		msgEvent := &MessageEvent{
			Type:      event.Type,
			From:      event.Source,
			Content:   event.Content,
			Timestamp: event.Timestamp,
			Metadata:  event.Metadata,
		}
		select {
		case li.ch <- msgEvent:
		default:
			// 旧版在 channel 满时静默 drop（保持原有行为）
		}
	}
}

// consumeWithRetry 带重试的消费
func (d *MessageDispatcher) consumeWithRetry(consumer Consumer, event *Event) {
	defer d.wg.Done()

	var lastErr error
	maxRetries := event.MaxRetries
	if maxRetries <= 0 {
		maxRetries = d.opts.MaxRetries
	}

	for attempt := 0; attempt <= maxRetries; attempt++ {
		// 检查是否过期
		if event.Deadline != nil && time.Now().After(*event.Deadline) {
			slog.Warn("Event expired before processing", "event_id", event.ID)
			return
		}

		// 设置超时 context
		ctx, cancel := context.WithTimeout(d.ctx, 30*time.Second)
		err := consumer.Consume(ctx, event)
		cancel()

		if err == nil {
			return // 成功
		}

		lastErr = err
		event.RetryCount = attempt + 1

		// 指数退避
		if attempt < maxRetries {
			delay := d.opts.RetryDelay * time.Duration(1<<uint(attempt))
			slog.Warn("Consumer failed, will retry",
				"consumer", consumer.ID(),
				"event_id", event.ID,
				"attempt", attempt+1,
				"max_retries", maxRetries,
				"delay", delay,
				"err", err,
			)
			select {
			case <-d.ctx.Done():
				return
			case <-time.After(delay):
			}
		}
	}

	// 超过最大重试次数，入死信队列
	d.dlq.Push(event, fmt.Errorf("max retries exceeded: %w", lastErr))
}

// RegisterConsumer 注册消费者（旧接口，向后兼容）
func (d *MessageDispatcher) RegisterConsumer(consumer MessageConsumer) {
	d.mu.Lock()
	defer d.mu.Unlock()

	ch := make(chan *MessageEvent, d.opts.ConsumerBufSize)
	li := &legacyConsumerInfo{
		consumer: consumer,
		ch:       ch,
	}
	d.legacyConsumers = append(d.legacyConsumers, li)

	// 启动旧式 consumer worker
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		for {
			select {
			case event := <-ch:
				if err := consumer.Consume(event); err != nil {
					slog.Warn("Legacy consumer error", "err", err)
				}
			case <-d.ctx.Done():
				return
			}
		}
	}()
}

// Subscribe 注册新式 Consumer（带事件类型过滤）
func (d *MessageDispatcher) Subscribe(consumer Consumer) {
	d.mu.Lock()
	defer d.mu.Unlock()

	types := consumer.Types()
	if len(types) == 0 {
		// 订阅所有类型
		for eventType := range d.consumers {
			d.consumers[eventType] = append(d.consumers[eventType], consumer)
		}
		// 为未来注册的类型也添加
		if d.consumers == nil {
			d.consumers = make(map[EventType][]Consumer)
		}
		// 记录全局消费者（用空类型标记）
		// 这里简化：监听所有类型
		for _, et := range allEventTypes() {
			d.consumers[et] = append(d.consumers[et], consumer)
		}
		return
	}

	for _, t := range types {
		d.consumers[t] = append(d.consumers[t], consumer)
	}
}

// PublishCompat 兼容旧接口（void 返回）
// 内部调用 Publish 并忽略返回值
func (d *MessageDispatcher) PublishCompat(event *Event) {
	_ = d.Publish(event)
}

// Shutdown 优雅关闭
func (d *MessageDispatcher) Shutdown() {
	d.cancelFunc()
	d.wg.Wait()

	if d.wal != nil {
		if err := d.wal.Close(); err != nil {
			slog.Warn("Error closing WAL", "err", err)
		}
	}
}

// WAL 返回 WAL 实例（用于管理和监控）
func (d *MessageDispatcher) WAL() WAL {
	return d.wal
}

// DLQ 返回死信队列（用于管理和监控）
func (d *MessageDispatcher) DLQ() *DeadLetterQueue {
	return d.dlq
}

// Backlog 返回 backlog（用于管理和监控）
func (d *MessageDispatcher) Backlog() *Backlog {
	return d.backlog
}

// replayUnacked 启动时回放 WAL 中未确认的事件
func (d *MessageDispatcher) replayUnacked() {
	if d.wal == nil {
		return
	}
	// 从序列号 1 开始回放所有事件
	err := d.wal.Replay(d.ctx, 1, func(event *Event) error {
		// 重新发布事件（WAL 中已有的事件会再次进入分发流程）
		select {
		case d.mainCh <- event:
		default:
			// mainCh 满，入 backlog
			d.backlog.Push(event)
		}
		return nil
	})
	if err != nil {
		slog.Warn("WAL replay failed", "err", err)
	}
}

// allEventTypes 返回所有已知事件类型（用于全局消费者注册）
func allEventTypes() []EventType {
	return []EventType{
		"model_info",
		"agent_start",
		"agent_finish",
		"tool_call",
		"tool_result",
		"error",
		"user_help_request",
		"user_help_response",
		"session_start",
		"session_end",
	}
}
