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
