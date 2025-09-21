package messaging

// MessageConsumer defines the interface for consuming message events.
type MessageConsumer interface {
	Consume(event *MessageEvent) error
}