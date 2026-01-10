package memory

import (
	"encoding/json"
	"testing"
)

func TestConversationMemory(t *testing.T) {
	mem := NewConversationMemory(5)

	// Test adding system message
	mem.AddSystemMessage("System prompt")
	if len(mem.Messages) != 1 {
		t.Errorf("Expected 1 message, got %d", len(mem.Messages))
	}
	if mem.Messages[0].Type != MessageTypeSystem {
		t.Errorf("Expected system message, got %s", mem.Messages[0].Type)
	}

	// Test adding human message
	mem.AddHumanMessage("Hello")
	if len(mem.Messages) != 2 {
		t.Errorf("Expected 2 messages, got %d", len(mem.Messages))
	}

	// Test adding assistant message
	mem.AddAssistantMessage("Hi there", nil)
	if len(mem.Messages) != 3 {
		t.Errorf("Expected 3 messages, got %d", len(mem.Messages))
	}

	// Test message overflow
	mem.AddHumanMessage("msg 4")
	mem.AddAssistantMessage("msg 5", nil)
	mem.AddHumanMessage("msg 6") // This should trigger eviction

	// Total messages should be 5 (max size)
	// But implementation details:
	// NewConversationMemory(5) -> MaxSize = 5
	// When adding 6th message:
	// System message (1) is kept.
	// Remaining slots = 4.
	// We have 5 non-system messages: "Hello", "Hi there", "msg 4", "msg 5", "msg 6".
	// We keep the latest 4: "Hi there", "msg 4", "msg 5", "msg 6".
	// Total: System + 4 = 5 messages.

	if len(mem.Messages) != 5 {
		t.Errorf("Expected 5 messages, got %d", len(mem.Messages))
	}

	// First message should still be System
	if mem.Messages[0].Type != MessageTypeSystem {
		t.Errorf("First message should be system, got %s", mem.Messages[0].Type)
	}

	// Last message should be "msg 6"
	if mem.Messages[4].Content != "msg 6" {
		t.Errorf("Last message should be 'msg 6', got '%s'", mem.Messages[4].Content)
	}
}

func TestJSONSerialization(t *testing.T) {
	mem := NewConversationMemory(10)
	mem.AddSystemMessage("sys")
	mem.AddHumanMessage("human")

	data, err := json.Marshal(mem)
	if err != nil {
		t.Fatalf("Failed to marshal memory: %v", err)
	}

	var mem2 ConversationMemory
	if err := json.Unmarshal(data, &mem2); err != nil {
		t.Fatalf("Failed to unmarshal memory: %v", err)
	}

	if len(mem2.Messages) != 2 {
		t.Errorf("Expected 2 messages after unmarshal, got %d", len(mem2.Messages))
	}
	if mem2.Messages[0].Content != "sys" {
		t.Errorf("Expected content 'sys', got '%s'", mem2.Messages[0].Content)
	}
}
