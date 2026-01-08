package tools

import (
	"context"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
)

func TestExecuteReadFile_DefaultRange(t *testing.T) {
	// Setup
	tmpDir, err := ioutil.TempDir("", "test_tools")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	testFile := filepath.Join(tmpDir, "test.txt")
	content := "line1\nline2\nline3"
	err = ioutil.WriteFile(testFile, []byte(content), 0644)
	if err != nil {
		t.Fatal(err)
	}

	tool := NewFileOperationsTool(tmpDir)
	ctx := context.Background()

	// Test case 1: No range specified (should read all)
	params := map[string]interface{}{
		"target_file": "test.txt",
	}

	result, err := tool.ExecuteReadFile(ctx, params)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected map result")
	}

	if resMap["content"] != content {
		t.Errorf("Expected content %q, got %q", content, resMap["content"])
	}

	// Test case 2: Start line specified, End line missing
	params2 := map[string]interface{}{
		"target_file":            "test.txt",
		"start_line_one_indexed": 2.0,
	}

	result2, err := tool.ExecuteReadFile(ctx, params2)
	if err != nil {
		t.Fatalf("Expected no error for partial read, got %v", err)
	}

	resMap2, ok := result2.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected map result")
	}

	expected2 := "line2\nline3"
	if resMap2["content"] != expected2 {
		t.Errorf("Expected content %q, got %q", expected2, resMap2["content"])
	}
}
