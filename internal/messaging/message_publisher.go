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

// Publish 发布消息
func (p *MessagePublisher) Publish(eventType string, content interface{}, from string) {
	if p.dispatcher != nil {
		p.dispatcher.Publish(&MessageEvent{
			Type:      eventType,
			From:      from,
			Content:   content,
			Timestamp: time.Now(),
		})
	}
}

// PublishWithMetadata 发布带元数据的消息
func (p *MessagePublisher) PublishWithMetadata(eventType string, content interface{}, from string, metadata map[string]interface{}) {
	if p.dispatcher != nil {
		p.dispatcher.Publish(&MessageEvent{
			Type:      eventType,
			From:      from,
			Content:   content,
			Timestamp: time.Now(),
			Metadata:  metadata,
		})
	}
}