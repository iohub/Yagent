package tools

import (
	"context"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
)

func TestExecuteReplaceBlock(t *testing.T) {
	// Setup temporary directory
	tmpDir, err := ioutil.TempDir("", "test_replace_block")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	tool := NewReplaceBlockTool(tmpDir)
	ctx := context.Background()

	// Test 1: Create new file
	newFile := "newfile.txt"
	newContent := "Hello, World!"
	paramsCreate := map[string]interface{}{
		"file_path":  newFile,
		"old_string": "",
		"new_string": newContent,
	}

	resCreate, err := tool.ExecuteReplaceBlock(ctx, paramsCreate)
	if err != nil {
		t.Fatalf("Failed to create new file: %v", err)
	}

	resMapCreate, ok := resCreate.(map[string]interface{})
	if !ok || resMapCreate["success"] != true {
		t.Fatalf("Expected success response for file creation")
	}

	// Verify file content
	contentBytes, err := ioutil.ReadFile(filepath.Join(tmpDir, newFile))
	if err != nil {
		t.Fatalf("Failed to read created file: %v", err)
	}
	if string(contentBytes) != newContent {
		t.Errorf("Expected content %q, got %q", newContent, string(contentBytes))
	}

	// Test 2: Replace content
	oldStr := "World"
	newStr := "Go"
	paramsReplace := map[string]interface{}{
		"file_path":  newFile,
		"old_string": oldStr,
		"new_string": newStr,
	}

	resReplace, err := tool.ExecuteReplaceBlock(ctx, paramsReplace)
	if err != nil {
		t.Fatalf("Failed to replace content: %v", err)
	}

	resMapReplace, ok := resReplace.(map[string]interface{})
	if !ok || resMapReplace["success"] != true {
		t.Fatalf("Expected success response for replacement")
	}

	// Verify updated content
	expectedContent := "Hello, Go!"
	contentBytes, err = ioutil.ReadFile(filepath.Join(tmpDir, newFile))
	if err != nil {
		t.Fatalf("Failed to read updated file: %v", err)
	}
	if string(contentBytes) != expectedContent {
		t.Errorf("Expected content %q, got %q", expectedContent, string(contentBytes))
	}

	// Test 3: Error - old_string not found
	paramsNotFound := map[string]interface{}{
		"file_path":  newFile,
		"old_string": "NonExistent",
		"new_string": "Something",
	}
	resNotFound, err := tool.ExecuteReplaceBlock(ctx, paramsNotFound)
	if err != nil {
		// Note: The implementation currently returns {success: false, error: ...} and nil error
		// or wraps error if something system-level fails.
		// Based on my reading, "old_string not found" returns success=false, error=...
		t.Logf("Got error as expected (system error check): %v", err)
	} else {
		resMapNotFound, ok := resNotFound.(map[string]interface{})
		if !ok {
			t.Fatalf("Expected map result")
		}
		if resMapNotFound["success"] == true {
			t.Errorf("Expected failure when old_string is not found")
		}
	}

	// Test 4: Error - Multiple occurrences
	// First prepare file with duplicates
	dupContent := "foo bar foo"
	err = ioutil.WriteFile(filepath.Join(tmpDir, newFile), []byte(dupContent), 0644)
	if err != nil {
		t.Fatal(err)
	}

	paramsDup := map[string]interface{}{
		"file_path":  newFile,
		"old_string": "foo",
		"new_string": "baz",
	}
	resDup, _ := tool.ExecuteReplaceBlock(ctx, paramsDup)
	resMapDup, ok := resDup.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected map result")
	}
	if resMapDup["success"] == true {
		t.Errorf("Expected failure when old_string appears multiple times")
	}
}
