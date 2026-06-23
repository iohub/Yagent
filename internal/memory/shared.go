package memory

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// SharedMemory is global context visible to all agents.
// Thread-safe for concurrent access from multiple agents.
type SharedMemory struct {
	messages    []ChatMessage
	maxSize     int                  // default: 500
	subscribers []func(ChatMessage)  // notify on new message
	mu          sync.RWMutex
}

// NewSharedMemory creates a new shared memory.
func NewSharedMemory(maxSize int) *SharedMemory {
	if maxSize <= 0 {
		maxSize = 500
	}
	return &SharedMemory{
		messages: make([]ChatMessage, 0),
		maxSize:  maxSize,
	}
}

// AddMessage adds a message to shared memory and notifies subscribers.
func (sm *SharedMemory) AddMessage(msg ChatMessage) error {
	sm.mu.Lock()

	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	sm.messages = append(sm.messages, msg)

	// Trim if exceeds max size
	if len(sm.messages) > sm.maxSize {
		overflow := len(sm.messages) - sm.maxSize
		sm.messages = sm.messages[overflow:]
	}

	// Copy subscribers list to call outside lock
	subscribers := make([]func(ChatMessage), len(sm.subscribers))
	copy(subscribers, sm.subscribers)
	sm.mu.Unlock()

	// Notify subscribers (outside lock to prevent deadlock)
	for _, fn := range subscribers {
		fn(msg)
	}

	return nil
}

// GetMessages returns all messages (thread-safe copy).
func (sm *SharedMemory) GetMessages() []ChatMessage {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([]ChatMessage, len(sm.messages))
	copy(result, sm.messages)
	return result
}

// GetContext returns formatted context string.
func (sm *SharedMemory) GetContext() string {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	if len(sm.messages) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("--- Shared Context ---\n")
	for _, msg := range sm.messages {
		agentInfo := ""
		if id, ok := msg.Metadata["agent_id"].(string); ok {
			agentInfo = fmt.Sprintf(" (from: %s)", id)
		}
		sb.WriteString(fmt.Sprintf("[%s%s] %s\n", string(msg.Type), agentInfo, msg.Content))
	}
	return sb.String()
}

// Clear clears all messages.
func (sm *SharedMemory) Clear() error {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.messages = make([]ChatMessage, 0)
	return nil
}

// Size returns the number of messages.
func (sm *SharedMemory) Size() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.messages)
}

// Subscribe registers a callback for new messages.
// Returns an unsubscribe function.
func (sm *SharedMemory) Subscribe(fn func(ChatMessage)) func() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	sm.subscribers = append(sm.subscribers, fn)
	index := len(sm.subscribers) - 1

	return func() {
		sm.mu.Lock()
		defer sm.mu.Unlock()
		sm.subscribers = append(sm.subscribers[:index], sm.subscribers[index+1:]...)
	}
}

// FilterByAgent returns messages from a specific agent.
func (sm *SharedMemory) FilterByAgent(agentID string) []ChatMessage {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	var filtered []ChatMessage
	for _, msg := range sm.messages {
		if id, ok := msg.Metadata["agent_id"].(string); ok && id == agentID {
			filtered = append(filtered, msg)
		}
	}
	return filtered
}

// Compact summarizes old messages (placeholder for Phase 2 enhancement).
func (sm *SharedMemory) Compact() error {
	// Future: use LLM to summarize older messages
	return nil
}
