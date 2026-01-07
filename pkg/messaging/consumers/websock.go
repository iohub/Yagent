package messaging

import (
	"encoding/json"
	"codeactor/pkg/messaging"
)

type WebSocketConsumer struct {
	callback func([]byte) error
}

func NewWebSocketConsumer(callback func([]byte) error) *WebSocketConsumer {
	return &WebSocketConsumer{
		callback: callback,
	}
}

func (w *WebSocketConsumer) Consume(event *messaging.MessageEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return w.callback(data)
}