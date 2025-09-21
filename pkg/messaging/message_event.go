package messaging

import (
	"time"
)

type MessageEvent struct {
	Type      string                 `json:"type"`
	Content   interface{}            `json:"content"`
	Timestamp time.Time              `json:"timestamp"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

func NewMessageEvent(eventType string, content interface{}) *MessageEvent {
	return &MessageEvent{
		Type:      eventType,
		Content:   content,
		Timestamp: time.Now(),
		Metadata:  make(map[string]interface{}),
	}
}