package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutePrintDirTree(t *testing.T) {
	// Create a temporary directory structure
	tmpDir := t.TempDir()

	// Create some files and directories
	if err := os.Mkdir(filepath.Join(tmpDir, "app"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "app", "main.py"), []byte("print('hello')"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, ".gitignore"), []byte("*.pyc"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewFileOperationsTool(tmpDir)

	result, err := tool.ExecutePrintDirTree(context.Background(), map[string]interface{}{
		"dir_path":  ".",
		"max_depth": float64(2),
	})
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	output := dirTreeOutput(t, result)

	for _, s := range []string{"├── .gitignore", "└── app", "    └── main.py"} {
		if !strings.Contains(output, s) {
			t.Errorf("Expected output to contain %q, but it didn't. Output:\n%s", s, output)
		}
	}
}

func TestExecutePrintDirTreeMaxDepthCap(t *testing.T) {
	tmpDir := t.TempDir()

	// Create a three-level directory structure: tmpDir/app/sub/deep.txt
	if err := os.MkdirAll(filepath.Join(tmpDir, "app", "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "app", "sub", "deep.txt"), []byte("deep content"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewFileOperationsTool(tmpDir)

	result, err := tool.ExecutePrintDirTree(context.Background(), map[string]interface{}{
		"dir_path":  ".",
		"max_depth": float64(100),
	})
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	output := dirTreeOutput(t, result)

	// Depth 1-2 should be visible (app and sub)
	for _, s := range []string{"app", "sub"} {
		if !strings.Contains(output, s) {
			t.Errorf("Expected output to contain %q (depth 1-2), but it didn't. Output:\n%s", s, output)
		}
	}

	// Depth 3 content (deep.txt) must NOT appear — proves clamp at 2
	if strings.Contains(output, "deep.txt") {
		t.Errorf("Expected output NOT to contain 'deep.txt' (depth 3), but it did. Output:\n%s", output)
	}
}

func dirTreeOutput(t *testing.T, result interface{}) string {
	t.Helper()
	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected map result, got %T", result)
	}
	output, ok := resMap["output"].(string)
	if !ok {
		t.Fatalf("Expected 'output' field to be string, got %T", resMap["output"])
	}
	return output
}
