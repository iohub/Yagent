package memory

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// LocalMemory is agent-specific scratch pad memory.
// Not visible to other agents unless explicitly promoted to SharedMemory.
type LocalMemory struct {
	agentID  string
	messages []ChatMessage
	maxSize  int               // max message count (default: 200)
	mu       sync.RWMutex
	metadata map[string]interface{}
}

// NewLocalMemory creates a new local memory for a specific agent.
func NewLocalMemory(agentID string, maxSize int) *LocalMemory {
	if maxSize <= 0 {
		maxSize = 200
	}
	return &LocalMemory{
		agentID:  agentID,
		messages: make([]ChatMessage, 0),
		maxSize:  maxSize,
		metadata: make(map[string]interface{}),
	}
}

// AddMessage adds a message to local memory.
func (lm *LocalMemory) AddMessage(msg ChatMessage) error {
	lm.mu.Lock()
	defer lm.mu.Unlock()

	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now()
	}
	lm.messages = append(lm.messages, msg)

	// Trim if exceeds max size (keep recent messages)
	if len(lm.messages) > lm.maxSize {
		overflow := len(lm.messages) - lm.maxSize
		lm.messages = lm.messages[overflow:]
	}

	return nil
}

// GetMessages returns all messages (thread-safe copy).
func (lm *LocalMemory) GetMessages() []ChatMessage {
	lm.mu.RLock()
	defer lm.mu.RUnlock()

	result := make([]ChatMessage, len(lm.messages))
	copy(result, lm.messages)
	return result
}

// GetContext returns formatted context string for LLM consumption.
func (lm *LocalMemory) GetContext() string {
	lm.mu.RLock()
	defer lm.mu.RUnlock()

	if len(lm.messages) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("--- Local Context (Agent: %s) ---\n", lm.agentID))
	for _, msg := range lm.messages {
		prefix := string(msg.Type)
		sb.WriteString(fmt.Sprintf("[%s] %s\n", prefix, msg.Content))
	}
	return sb.String()
}

// Clear clears all messages.
func (lm *LocalMemory) Clear() error {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	lm.messages = make([]ChatMessage, 0)
	return nil
}

// Size returns the number of messages.
func (lm *LocalMemory) Size() int {
	lm.mu.RLock()
	defer lm.mu.RUnlock()
	return len(lm.messages)
}

// AgentID returns the agent ID this memory belongs to.
func (lm *LocalMemory) AgentID() string {
	return lm.agentID
}

// SetMetadata sets a metadata key-value pair.
func (lm *LocalMemory) SetMetadata(key string, value interface{}) {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	lm.metadata[key] = value
}

// GetMetadata gets a metadata value by key.
func (lm *LocalMemory) GetMetadata(key string) (interface{}, bool) {
	lm.mu.RLock()
	defer lm.mu.RUnlock()
	val, ok := lm.metadata[key]
	return val, ok
}

// FilterByType returns messages of a specific type.
func (lm *LocalMemory) FilterByType(msgType MessageType) []ChatMessage {
	lm.mu.RLock()
	defer lm.mu.RUnlock()

	var filtered []ChatMessage
	for _, msg := range lm.messages {
		if msg.Type == msgType {
			filtered = append(filtered, msg)
		}
	}
	return filtered
}

// Trim keeps only the last N messages.
func (lm *LocalMemory) Trim(keepLast int) error {
	if keepLast <= 0 {
		return fmt.Errorf("keepLast must be positive")
	}
	lm.mu.Lock()
	defer lm.mu.Unlock()

	if len(lm.messages) > keepLast {
		lm.messages = lm.messages[len(lm.messages)-keepLast:]
	}
	return nil
}

// ToLLMMessages converts local memory to LLM message format.
func (lm *LocalMemory) ToLLMMessages() []ChatMessage {
	return lm.GetMessages()
}
