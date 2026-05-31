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

func TestNewFields_JSONOmitEmpty(t *testing.T) {
	// Test 1: default zero values should be omitted in JSON
	msg := ChatMessage{
		Type:    MessageTypeHuman,
		Content: "hello",
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("Failed to marshal message: %v", err)
	}

	// Should NOT contain group_id, parent_id, is_sub_agent when zero-valued
	if containsJSONKey(data, "group_id") {
		t.Errorf("Expected group_id to be omitted when empty, got: %s", string(data))
	}
	if containsJSONKey(data, "parent_id") {
		t.Errorf("Expected parent_id to be omitted when empty, got: %s", string(data))
	}
	if containsJSONKey(data, "is_sub_agent") {
		t.Errorf("Expected is_sub_agent to be omitted when false, got: %s", string(data))
	}

	// Test 2: non-zero values should appear in JSON
	msg2 := ChatMessage{
		Type:       MessageTypeHuman,
		Content:    "hello",
		GroupID:    "group-123",
		ParentID:   "call_abc",
		IsSubAgent: true,
	}
	data2, err := json.Marshal(msg2)
	if err != nil {
		t.Fatalf("Failed to marshal message: %v", err)
	}

	if !containsJSONKey(data2, "group_id") {
		t.Errorf("Expected group_id to be present when set, got: %s", string(data2))
	}
	if !containsJSONKey(data2, "parent_id") {
		t.Errorf("Expected parent_id to be present when set, got: %s", string(data2))
	}
	if !containsJSONKey(data2, "is_sub_agent") {
		t.Errorf("Expected is_sub_agent to be present when true, got: %s", string(data2))
	}

	// Test 3: round-trip deserialization preserves the new fields
	var msg3 ChatMessage
	if err := json.Unmarshal(data2, &msg3); err != nil {
		t.Fatalf("Failed to unmarshal message: %v", err)
	}
	if msg3.GroupID != "group-123" {
		t.Errorf("Expected GroupID 'group-123', got '%s'", msg3.GroupID)
	}
	if msg3.ParentID != "call_abc" {
		t.Errorf("Expected ParentID 'call_abc', got '%s'", msg3.ParentID)
	}
	if !msg3.IsSubAgent {
		t.Errorf("Expected IsSubAgent to be true")
	}
}

// containsJSONKey checks if a JSON byte slice contains a specific key.
func containsJSONKey(data []byte, key string) bool {
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return false
	}
	_, ok := m[key]
	return ok
}
