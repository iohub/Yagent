package messaging

import (
	"time"
)

// MessagePublisher 消息发布者
type MessagePublisher struct {
	dispatcher *MessageDispatcher
}

// NewMessagePublisher 创建新的消息发布者
func NewMessagePublisher(dispatcher *MessageDispatcher) *MessagePublisher {
	return &MessagePublisher{
		dispatcher: dispatcher,
	}
}

// SetDispatcher 更新内部 dispatcher 引用，用于在 Publisher 创建后切换 dispatcher
func (p *MessagePublisher) SetDispatcher(dispatcher *MessageDispatcher) {
	p.dispatcher = dispatcher
}

// Publish 发布消息（新接口，返回 error）
func (p *MessagePublisher) Publish(eventType string, content interface{}, from string) error {
	if p.dispatcher == nil {
		return nil
	}
	return p.dispatcher.Publish(&Event{
		Type:      EventType(eventType),
		Source:    from,
		Content:   content,
		Timestamp: time.Now(),
	})
}

// PublishWithMetadata 发布带元数据的消息
func (p *MessagePublisher) PublishWithMetadata(eventType string, content interface{}, from string, metadata map[string]interface{}) error {
	if p.dispatcher == nil {
		return nil
	}
	return p.dispatcher.Publish(&Event{
		Type:      EventType(eventType),
		Source:    from,
		Content:   content,
		Timestamp: time.Now(),
		Metadata:  metadata,
	})
}