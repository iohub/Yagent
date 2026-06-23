package messaging

import "context"

// MessageConsumer defines the interface for consuming message events.
// 旧版接口，保持向后兼容
type MessageConsumer interface {
	// Consume 处理事件，返回 error 触发重试机制
	Consume(event *MessageEvent) error
}

// Consumer 是增强版消费者接口（新代码使用）
// 支持事件类型过滤、生命周期管理和重试机制
type Consumer interface {
	// Consume 处理事件，返回 error 触发重试机制
	Consume(ctx context.Context, event *Event) error
	// Types 声明订阅的事件类型（空切片或 nil = 订阅所有）
	Types() []EventType
	// ID 返回消费者唯一标识
	ID() string
}