package assistant

import (
	"codee/pkg/messaging"
)

// MessagePublisher provides a way to publish messages to the message dispatcher
type MessagePublisher struct {
	dispatcher *messaging.MessageDispatcher
}

// NewMessagePublisher creates a new message publisher
func NewMessagePublisher(dispatcher *messaging.MessageDispatcher) *MessagePublisher {
	return &MessagePublisher{
		dispatcher: dispatcher,
	}
}

// Publish publishes a message event
func (mp *MessagePublisher) Publish(eventType string, content interface{}) {
	if mp.dispatcher != nil {
		event := messaging.NewMessageEvent(eventType, content)
		mp.dispatcher.Publish(event)
	}
}

// GetPublisher returns the message publisher
func (tr *TaskRequest) GetPublisher() *MessagePublisher {
	return tr.publisher
}