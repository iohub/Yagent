package messaging

import (
	"time"
)

type MessageEvent struct {
	Type      string                 `json:"type"`
	From      string                 `json:"from"`
	Content   interface{}            `json:"content"`
	Timestamp time.Time              `json:"timestamp"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

func NewMessageEvent(eventType string, content interface{}, from string) *MessageEvent {
	return &MessageEvent{
		Type:      eventType,
		From:      from,
		Content:   content,
		Timestamp: time.Now(),
		Metadata:  make(map[string]interface{}),
	}
}