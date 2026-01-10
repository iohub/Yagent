package assistant

import (
	"codeactor/internal/memory"
	"os"
	"testing"
)

func TestDataManager(t *testing.T) {
	// Create a temporary directory for testing
	tempDir, err := os.MkdirTemp("", "codeactor_test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	dm := &DataManager{
		dataDir: tempDir,
	}

	// Create a memory instance
	mem := memory.NewConversationMemory(10)
	mem.AddSystemMessage("sys")
	mem.AddHumanMessage("human")

	taskID := "test-task"

	// Save memory
	if err := dm.SaveTaskMemory(taskID, mem); err != nil {
		t.Fatalf("Failed to save memory: %v", err)
	}

	// Load memory
	loadedMem, err := dm.LoadTaskMemory(taskID)
	if err != nil {
		t.Fatalf("Failed to load memory: %v", err)
	}

	if len(loadedMem.Messages) != 2 {
		t.Errorf("Expected 2 messages, got %d", len(loadedMem.Messages))
	}
	if loadedMem.Messages[0].Content != "sys" {
		t.Errorf("Expected 'sys', got '%s'", loadedMem.Messages[0].Content)
	}
}
