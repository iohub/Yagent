package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteListDir_IgnoredDirs(t *testing.T) {
	// Create temporary directory
	tmpDir, err := os.MkdirTemp("", "test_listdir")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create structure:
	// .git/config
	// node_modules/pkg/index.js
	// normal/file.txt
	
	dirs := []string{
		".git",
		"node_modules/pkg",
		"normal",
	}
	
	for _, dir := range dirs {
		err := os.MkdirAll(filepath.Join(tmpDir, dir), 0755)
		if err != nil {
			t.Fatal(err)
		}
	}

	files := []string{
		".git/config",
		"node_modules/pkg/index.js",
		"normal/file.txt",
	}

	for _, file := range files {
		err := os.WriteFile(filepath.Join(tmpDir, file), []byte("content"), 0644)
		if err != nil {
			t.Fatal(err)
		}
	}

	tool := NewFileOperationsTool(tmpDir)
	params := map[string]interface{}{
		"dir_path": ".",
	}

	result, err := tool.ExecuteListDir(context.Background(), params)
	if err != nil {
		t.Fatalf("ExecuteListDir failed: %v", err)
	}

	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("Result is not a map")
	}

	fileList, ok := resMap["files"].([]string)
	if !ok {
		t.Fatalf("files is not a []string")
	}

	// Check if we have expected items
	foundGit := false
	foundNodeModules := false
	foundNormal := false
	foundNormalFile := false
	foundGitConfig := false
	foundNodeModulesPkg := false

	for _, f := range fileList {
		if f == "[DIR] .git" {
			foundGit = true
		}
		if f == "[DIR] node_modules" {
			foundNodeModules = true
		}
		if f == "[DIR] normal" {
			foundNormal = true
		}
		if strings.HasSuffix(f, "normal/file.txt") {
			foundNormalFile = true
		}
		if strings.Contains(f, ".git/config") {
			foundGitConfig = true
		}
		if strings.Contains(f, "node_modules/pkg") {
			foundNodeModulesPkg = true
		}
	}

	if !foundGit {
		t.Error("Should list .git directory")
	}
	if !foundNodeModules {
		t.Error("Should list node_modules directory")
	}
	if !foundNormal {
		t.Error("Should list normal directory")
	}
	if !foundNormalFile {
		t.Error("Should list normal/file.txt")
	}
	if foundGitConfig {
		t.Error("Should NOT list contents of .git")
	}
	if foundNodeModulesPkg {
		t.Error("Should NOT list contents of node_modules")
	}
}
