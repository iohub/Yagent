package tools

import (
	"context"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
)

func TestExecutePrintDirTree(t *testing.T) {
	// Create a temporary directory structure
	tmpDir, err := ioutil.TempDir("", "test_file_ops")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create some files and directories
	os.Mkdir(filepath.Join(tmpDir, "app"), 0755)
	ioutil.WriteFile(filepath.Join(tmpDir, "app", "main.py"), []byte("print('hello')"), 0644)
	ioutil.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte("*.pyc"), 0644)

	tool := NewFileOperationsTool(tmpDir)
	ctx := context.Background()

	params := map[string]interface{}{
		"dir_path":  ".",
		"max_depth": float64(2),
	}

	result, err := tool.ExecutePrintDirTree(ctx, params)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	// Verify the result is a map
	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected map result, got %T", result)
	}

	output, ok := resMap["output"].(string)
	if !ok {
		t.Fatalf("Expected 'output' field to be string")
	}

	// Verify content roughly
	expectedSubstrings := []string{
		"├── .gitignore",
		"└── app",
		"    └── main.py",
	}

	for _, s := range expectedSubstrings {
		if !contains(output, s) {
			t.Errorf("Expected output to contain %q, but it didn't. Output:\n%s", s, output)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[0:len(substr)] == substr || contains(s[1:], substr)))
}

func TestExecutePrintDirTreeMaxDepthCap(t *testing.T) {
	tmpDir, err := ioutil.TempDir("", "test_file_ops_depth_cap")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a three-level directory structure: tmpDir/app/sub/deep.txt
	os.MkdirAll(filepath.Join(tmpDir, "app", "sub"), 0755)
	ioutil.WriteFile(filepath.Join(tmpDir, "app", "sub", "deep.txt"), []byte("deep content"), 0644)

	tool := NewFileOperationsTool(tmpDir)
	ctx := context.Background()

	params := map[string]interface{}{
		"dir_path":  ".",
		"max_depth": float64(100),
	}

	result, err := tool.ExecutePrintDirTree(ctx, params)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected map result, got %T", result)
	}

	output, ok := resMap["output"].(string)
	if !ok {
		t.Fatalf("Expected 'output' field to be string")
	}

	// Depth 1-2 should be visible (app and sub)
	if !contains(output, "app") {
		t.Errorf("Expected output to contain 'app' (depth 1), but it didn't. Output:\n%s", output)
	}
	if !contains(output, "sub") {
		t.Errorf("Expected output to contain 'sub' (depth 2), but it didn't. Output:\n%s", output)
	}

	// Depth 3 content (deep.txt) must NOT appear — proves clamp at 2
	if contains(output, "deep.txt") {
		t.Errorf("Expected output NOT to contain 'deep.txt' (depth 3), but it did. Output:\n%s", output)
	}
}
