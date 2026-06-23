package memory

import (
	"fmt"
	"strings"
	"sync"
)

// LayeredConfig configures how LayeredMemory merges local and shared context.
type LayeredConfig struct {
	LocalFirst      bool     // local messages first in GetContext (default: true)
	IncludeShared   bool     // include shared in context (default: true)
	SharedTagFilter []string // only include shared messages with these tags
}

// DefaultLayeredConfig returns default layered configuration.
func DefaultLayeredConfig() LayeredConfig {
	return LayeredConfig{
		LocalFirst:    true,
		IncludeShared: true,
	}
}

// LayeredMemory composes LocalMemory + SharedMemory.
// Reads merge both layers; writes go to Local by default.
type LayeredMemory struct {
	local  *LocalMemory
	shared *SharedMemory
	config LayeredConfig
	mu     sync.RWMutex
}

// NewLayeredMemory creates a new layered memory.
func NewLayeredMemory(local *LocalMemory, shared *SharedMemory, config LayeredConfig) *LayeredMemory {
	if local == nil {
		panic("local memory must not be nil")
	}
	return &LayeredMemory{
		local:  local,
		shared: shared,
		config: config,
	}
}

// AddMessage adds a message to local memory.
func (lm *LayeredMemory) AddMessage(msg ChatMessage) error {
	return lm.local.AddMessage(msg)
}

// GetMessages returns merged messages (local + shared).
func (lm *LayeredMemory) GetMessages() []ChatMessage {
	localMsgs := lm.local.GetMessages()

	if !lm.config.IncludeShared || lm.shared == nil {
		return localMsgs
	}

	sharedMsgs := lm.shared.GetMessages()

	// Merge: shared first, then local (or vice versa based on config)
	merged := make([]ChatMessage, 0, len(localMsgs)+len(sharedMsgs))
	if lm.config.LocalFirst {
		merged = append(merged, localMsgs...)
		merged = append(merged, sharedMsgs...)
	} else {
		merged = append(merged, sharedMsgs...)
		merged = append(merged, localMsgs...)
	}

	return merged
}

// GetContext returns formatted context with layer markers.
func (lm *LayeredMemory) GetContext() string {
	var sb strings.Builder

	if lm.config.IncludeShared && lm.shared != nil {
		sharedCtx := lm.shared.GetContext()
		if sharedCtx != "" {
			sb.WriteString(sharedCtx)
			sb.WriteString("\n")
		}
	}

	localCtx := lm.local.GetContext()
	if localCtx != "" {
		sb.WriteString(localCtx)
	}

	return strings.TrimSpace(sb.String())
}

// GetContextWithLayers returns formatted context clearly showing layer separation.
func (lm *LayeredMemory) GetContextWithLayers() string {
	var sb strings.Builder

	if lm.config.IncludeShared && lm.shared != nil {
		sharedMsgs := lm.shared.GetMessages()
		if len(sharedMsgs) > 0 {
			sb.WriteString("─── Shared Context ───\n")
			for _, msg := range sharedMsgs {
				sb.WriteString(fmt.Sprintf("[%s] %s\n", string(msg.Type), msg.Content))
			}
			sb.WriteString("\n")
		}
	}

	localMsgs := lm.local.GetMessages()
	if len(localMsgs) > 0 {
		sb.WriteString(fmt.Sprintf("─── Local Context (Agent: %s) ───\n", lm.local.AgentID()))
		for _, msg := range localMsgs {
			sb.WriteString(fmt.Sprintf("[%s] %s\n", string(msg.Type), msg.Content))
		}
	}

	return strings.TrimSpace(sb.String())
}

// Clear clears local memory only (shared is global).
func (lm *LayeredMemory) Clear() error {
	return lm.local.Clear()
}

// Size returns total message count (local + shared).
func (lm *LayeredMemory) Size() int {
	total := lm.local.Size()
	if lm.config.IncludeShared && lm.shared != nil {
		total += lm.shared.Size()
	}
	return total
}

// LocalSize returns local memory size.
func (lm *LayeredMemory) LocalSize() int {
	return lm.local.Size()
}

// SharedSize returns shared memory size.
func (lm *LayeredMemory) SharedSize() int {
	if lm.shared == nil {
		return 0
	}
	return lm.shared.Size()
}

// GetLocalMemory returns the underlying local memory.
func (lm *LayeredMemory) GetLocalMemory() *LocalMemory {
	return lm.local
}

// GetSharedMemory returns the underlying shared memory.
func (lm *LayeredMemory) GetSharedMemory() *SharedMemory {
	return lm.shared
}

// PromoteToShared promotes a local message to shared memory.
// The message must exist in local memory (matched by content and type).
func (lm *LayeredMemory) PromoteToShared(msg ChatMessage) error {
	if lm.shared == nil {
		return fmt.Errorf("shared memory not available")
	}
	// Tag the message with agent info
	if msg.Metadata == nil {
		msg.Metadata = make(map[string]interface{})
	}
	msg.Metadata["agent_id"] = lm.local.AgentID()
	msg.Metadata["promoted"] = true

	return lm.shared.AddMessage(msg)
}

// PromoteLastToShared promotes the most recent local message to shared memory.
func (lm *LayeredMemory) PromoteLastToShared() error {
	msgs := lm.local.GetMessages()
	if len(msgs) == 0 {
		return fmt.Errorf("no local messages to promote")
	}
	return lm.PromoteToShared(msgs[len(msgs)-1])
}
