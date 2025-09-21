package messaging

import (
	"context"
	"sync"
)

type MessageDispatcher struct {
	queue      chan *MessageEvent
	consumers  []MessageConsumer
	mu         sync.RWMutex
	ctx        context.Context
	cancelFunc context.CancelFunc
}

func NewMessageDispatcher(bufferSize int) *MessageDispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	dispatcher := &MessageDispatcher{
		queue:      make(chan *MessageEvent, bufferSize),
		consumers:  make([]MessageConsumer, 0),
		ctx:        ctx,
		cancelFunc: cancel,
	}
	go dispatcher.start()
	return dispatcher
}

func (d *MessageDispatcher) start() {
	for {
		select {
		case event := <-d.queue:
			d.dispatch(event)
		case <-d.ctx.Done():
			// Gracefully drain the queue before shutdown
			for event := range d.queue {
				d.dispatch(event)
			}
			return
		}
	}
}

func (d *MessageDispatcher) dispatch(event *MessageEvent) {
	d.mu.RLock()
	defer d.mu.RUnlock()

	for _, consumer := range d.consumers {
		// Consume in a non-blocking manner to avoid slowing down the queue
		go func(c MessageConsumer, e *MessageEvent) {
			if err := c.Consume(e); err != nil {
				// Log error but don't block the dispatcher
				// In a real system, you might want to use a logger here
				// For now, we'll just ignore errors to keep the system running
			}
		}(consumer, event)
	}
}

func (d *MessageDispatcher) RegisterConsumer(consumer MessageConsumer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.consumers = append(d.consumers, consumer)
}

func (d *MessageDispatcher) Publish(event *MessageEvent) {
	select {
	case d.queue <- event:
		// Event published successfully
	default:
		// Queue is full, drop the event to avoid blocking
		// In a production system, you might want to log this or implement backpressure
	}
}

func (d *MessageDispatcher) Shutdown() {
	d.cancelFunc()
}