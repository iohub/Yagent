package messaging

import (
	"context"
	"sync"
)

type MessageDispatcher struct {
	queue       chan *MessageEvent
	consumerChs []chan *MessageEvent
	mu          sync.RWMutex
	ctx         context.Context
	cancelFunc  context.CancelFunc
}

func NewMessageDispatcher(bufferSize int) *MessageDispatcher {
	ctx, cancel := context.WithCancel(context.Background())
	dispatcher := &MessageDispatcher{
		queue:       make(chan *MessageEvent, bufferSize),
		consumerChs: make([]chan *MessageEvent, 0),
		ctx:         ctx,
		cancelFunc:  cancel,
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

	for _, ch := range d.consumerChs {
		select {
		case ch <- event:
		default:
			// Channel is full, drop the event to avoid blocking
			// In a production system, you might want to log this
		}
	}
}

func (d *MessageDispatcher) RegisterConsumer(consumer MessageConsumer) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Create a buffered channel for the consumer
	ch := make(chan *MessageEvent, 1000)
	d.consumerChs = append(d.consumerChs, ch)

	// Start a worker goroutine for this consumer
	go func() {
		for {
			select {
			case event := <-ch:
				if err := consumer.Consume(event); err != nil {
					// Log error but don't stop the worker
				}
			case <-d.ctx.Done():
				return
			}
		}
	}()
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