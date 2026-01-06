package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"strings"

	"codeactor/internal/util"
)

// ReplaceBlockTool implements the replace_block tool for file editing.
type ReplaceBlockTool struct {
	workingDir string
}

func NewReplaceBlockTool(workingDir string) *ReplaceBlockTool {
	return &ReplaceBlockTool{workingDir: workingDir}
}

func (t *ReplaceBlockTool) Name() string {
	return "replace_block"
}

func (t *ReplaceBlockTool) Description() string {
	return "Replace a block of code in a file. Input: file_path, search_block, replace_block."
}

func (t *ReplaceBlockTool) Call(ctx context.Context, input string) (string, error) {
	var params struct {
		FilePath     string `json:"file_path"`
		SearchBlock  string `json:"search_block"`
		ReplaceBlock string `json:"replace_block"`
	}

	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", util.WrapError(ctx, err, "ReplaceBlockTool: invalid input json")
	}

	if params.FilePath == "" || params.SearchBlock == "" {
		return "", fmt.Errorf("file_path and search_block are required")
	}

	// Resolve path (simple version, assuming workingDir is root or using absolute paths)
	// Ideally we use the same path resolution as FileOperationsTool
	fullPath := params.FilePath
	// Note: We might need to handle relative paths if workingDir is set and path is relative.
	// For now, assuming absolute or correct relative path from CWD.

	contentBytes, err := ioutil.ReadFile(fullPath)
	if err != nil {
		return "", util.WrapError(ctx, err, "ReplaceBlockTool: failed to read file")
	}

	content := string(contentBytes)
	
	// Normalize line endings for better matching
	// This is a naive implementation. For robust matching, we might need more complex logic.
	if !strings.Contains(content, params.SearchBlock) {
		return "", fmt.Errorf("search_block not found in file: %s", params.FilePath)
	}

	// Perform replacement (replace only the first occurrence)
	newContent := strings.Replace(content, params.SearchBlock, params.ReplaceBlock, 1)

	if err := ioutil.WriteFile(fullPath, []byte(newContent), 0644); err != nil {
		return "", util.WrapError(ctx, err, "ReplaceBlockTool: failed to write file")
	}

	return fmt.Sprintf("Successfully replaced block in %s", params.FilePath), nil
}
