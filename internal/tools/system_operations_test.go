package tools

import (
	"context"
	"io/ioutil"
	"os"
	"testing"
)

func TestExecuteRunBash(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "test_sys_ops")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	tool := NewSystemOperationsTool(tmpDir)
	ctx := context.Background()

	// Test case 1: Foreground command with explanation
	params := map[string]interface{}{
		"command":       "echo 'hello'",
		"is_background": false,
		"explanation":   "Just saying hello",
	}

	result, err := tool.ExecuteRunBash(ctx, params)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected map result")
	}

	if resMap["success"] != true {
		t.Errorf("Expected success=true")
	}

	output, ok := resMap["output"].(string)
	if !ok {
		t.Fatalf("Expected output to be string")
	}
	if output != "hello\n" {
		t.Errorf("Expected output 'hello\\n', got %q", output)
	}

	// Test case 2: Background command
	paramsBg := map[string]interface{}{
		"command":       "sleep 1",
		"is_background": true,
		"explanation":   "Sleeping in background",
	}

	resultBg, err := tool.ExecuteRunBash(ctx, paramsBg)
	if err != nil {
		t.Fatalf("Expected no error for background command, got %v", err)
	}

	resMapBg, ok := resultBg.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected map result for background command")
	}

	if resMapBg["success"] != true {
		t.Errorf("Expected success=true for background command")
	}

	if resMapBg["status"] != "started_background" {
		t.Errorf("Expected status='started_background', got %v", resMapBg["status"])
	}

	if _, ok := resMapBg["pid"].(int); !ok {
		t.Errorf("Expected pid to be an int")
	}
}
