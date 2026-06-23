package conductor

import (
	"testing"
)

func TestMemoryManager_SetAndGetPending(t *testing.T) {
	mm := NewMemoryManager()

	if mm.HasPendingMemory() {
		t.Error("new memory manager should not have pending memory")
	}

	mem := &SubAgentMemory{
		Text:   "test result",
		Memory: []ChatMessage{{Type: "human", Content: "hello"}},
	}
	mm.SetPendingMemory(mem)

	if !mm.HasPendingMemory() {
		t.Error("should have pending memory after SetPendingMemory")
	}

	got := mm.GetPendingMemory()
	if got == nil {
		t.Fatal("GetPendingMemory returned nil")
	}
	if got.Text != "test result" {
		t.Errorf("Text = %q, want 'test result'", got.Text)
	}
	if len(got.Memory) != 1 {
		t.Errorf("Memory length = %d, want 1", len(got.Memory))
	}

	// After Get, should be cleared
	if mm.HasPendingMemory() {
		t.Error("should not have pending memory after GetPendingMemory")
	}

	// Second Get should return nil
	if got := mm.GetPendingMemory(); got != nil {
		t.Error("second GetPendingMemory should return nil")
	}
}

func TestMemoryManager_NilMemory(t *testing.T) {
	mm := NewMemoryManager()

	mm.SetPendingMemory(nil)
	if mm.HasPendingMemory() {
		t.Error("SetPendingMemory(nil) should not set HasPendingMemory")
	}

	got := mm.GetPendingMemory()
	if got != nil {
		t.Error("GetPendingMemory should return nil after SetPendingMemory(nil)")
	}
}

func TestMemoryManager_OverwritePending(t *testing.T) {
	mm := NewMemoryManager()

	mm.SetPendingMemory(&SubAgentMemory{Text: "first"})
	mm.SetPendingMemory(&SubAgentMemory{Text: "second"})

	got := mm.GetPendingMemory()
	if got.Text != "second" {
		t.Errorf("Text = %q, want 'second'", got.Text)
	}
}

func TestBuildSubAgentMemoryResult(t *testing.T) {
	messages := []ChatMessage{
		{Type: "human", Content: "task", IsSubAgent: true},
	}
	result := BuildSubAgentMemoryResult("output", messages)

	if result.Text != "output" {
		t.Errorf("Text = %q, want 'output'", result.Text)
	}
	if len(result.Memory) != 1 {
		t.Errorf("Memory length = %d, want 1", len(result.Memory))
	}
	if !result.Memory[0].IsSubAgent {
		t.Error("Message should be marked as sub-agent")
	}
}

func TestConvertToolCalls(t *testing.T) {
	tcs := []ToolCall{
		{
			ID:   "call_1",
			Type: "function",
			Function: FunctionCall{
				Name:      "read_file",
				Arguments: `{"target_file": "test.go"}`,
			},
		},
		{
			ID:   "call_2",
			Type: "function",
			Function: FunctionCall{
				Name:      "search_by_regex",
				Arguments: `{"query": "func"}`,
			},
		},
	}

	data := ConvertToolCalls(tcs)
	if len(data) != 2 {
		t.Fatalf("ConvertToolCalls returned %d items, want 2", len(data))
	}
	if data[0].ID != "call_1" {
		t.Errorf("data[0].ID = %q, want 'call_1'", data[0].ID)
	}
	if data[0].Function.Name != "read_file" {
		t.Errorf("data[0].Function.Name = %q, want 'read_file'", data[0].Function.Name)
	}
	if data[1].ID != "call_2" {
		t.Errorf("data[1].ID = %q, want 'call_2'", data[1].ID)
	}
	if data[1].Function.Name != "search_by_regex" {
		t.Errorf("data[1].Function.Name = %q, want 'search_by_regex'", data[1].Function.Name)
	}
}

func TestConvertToolCalls_Empty(t *testing.T) {
	data := ConvertToolCalls(nil)
	if len(data) != 0 {
		t.Errorf("ConvertToolCalls(nil) = %d, want 0", len(data))
	}

	data = ConvertToolCalls([]ToolCall{})
	if len(data) != 0 {
		t.Errorf("ConvertToolCalls([]) = %d, want 0", len(data))
	}
}

func TestFormatMemoryInjectionContext(t *testing.T) {
	ctx := FormatMemoryInjectionContext("delegate_repo", "call_123")
	if ctx == "" {
		t.Error("FormatMemoryInjectionContext returned empty string")
	}
	if len(ctx) < 20 {
		t.Errorf("FormatMemoryInjectionContext too short: %q", ctx)
	}
}
