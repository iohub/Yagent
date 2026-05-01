package tools

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"codeactor/pkg/messaging"

	"github.com/google/uuid"
)

// UserConfirmManager manages user confirmation/interaction flows.
// It bridges agent tool calls with the interactive UI by publishing
// user_help_needed events and waiting for user_help_response events.
// Multiple agents share one manager instance registered as a MessageConsumer.
type UserConfirmManager struct {
	mu        sync.Mutex
	pending   map[string]chan string
	publisher *messaging.MessagePublisher
}

// NewUserConfirmManager creates a new UserConfirmManager.
func NewUserConfirmManager() *UserConfirmManager {
	return &UserConfirmManager{
		pending: make(map[string]chan string),
	}
}

// SetPublisher sets the message publisher used to publish user_help_needed events.
func (m *UserConfirmManager) SetPublisher(p *messaging.MessagePublisher) {
	m.publisher = p
}

// RequestConfirmation publishes a user_help_needed event and blocks until
// a user_help_response is received, or the context is cancelled, or timeout.
func (m *UserConfirmManager) RequestConfirmation(ctx context.Context, question string, options string) (string, error) {
	if m.publisher == nil {
		return "", fmt.Errorf("UserConfirmManager: publisher not set")
	}

	requestID := uuid.New().String()
	ch := make(chan string, 1)

	m.mu.Lock()
	m.pending[requestID] = ch
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.pending, requestID)
		m.mu.Unlock()
	}()

	m.publisher.Publish("user_help_needed", map[string]interface{}{
		"question":   question,
		"options":    options,
		"request_id": requestID,
	}, "Agent")

	slog.Info("UserConfirmManager waiting for user response", "request_id", requestID, "question", question)

	select {
	case response := <-ch:
		slog.Info("UserConfirmManager received response", "request_id", requestID)
		return response, nil
	case <-ctx.Done():
		return "", fmt.Errorf("user confirmation cancelled: %w", ctx.Err())
	case <-time.After(5 * time.Minute):
		return "", fmt.Errorf("user confirmation timed out after 5 minutes")
	}
}

// OnUserResponse delivers a user response to the waiting request channel.
func (m *UserConfirmManager) OnUserResponse(requestID, response string) {
	m.mu.Lock()
	ch, ok := m.pending[requestID]
	m.mu.Unlock()

	if ok {
		select {
		case ch <- response:
		default:
			slog.Warn("UserConfirmManager response channel full", "request_id", requestID)
		}
	} else {
		slog.Warn("UserConfirmManager no pending request for response", "request_id", requestID)
	}
}

// Consume implements messaging.MessageConsumer to receive user_help_response events
// from the message dispatcher and route them to the correct pending request.
func (m *UserConfirmManager) Consume(event *messaging.MessageEvent) error {
	if event.Type != "user_help_response" {
		return nil
	}

	content, ok := event.Content.(map[string]interface{})
	if !ok {
		return nil
	}

	response, _ := content["response"].(string)
	if response == "" {
		return nil
	}

	requestID := ""
	if event.Metadata != nil {
		if id, ok := event.Metadata["request_id"].(string); ok {
			requestID = id
		}
	}
	if requestID == "" {
		if id, ok := content["request_id"].(string); ok {
			requestID = id
		}
	}

	if requestID != "" {
		m.OnUserResponse(requestID, response)
	}

	return nil
}
