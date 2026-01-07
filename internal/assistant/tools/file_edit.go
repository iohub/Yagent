package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"codeactor/internal/util"
)

// ReplaceBlockTool implements the search_replace tool for file editing.
type ReplaceBlockTool struct {
	workingDir string
}

func NewReplaceBlockTool(workingDir string) *ReplaceBlockTool {
	return &ReplaceBlockTool{workingDir: workingDir}
}

func (t *ReplaceBlockTool) Name() string {
	return "search_replace"
}

func (t *ReplaceBlockTool) Description() string {
	return "Replace a block of code in a file. Input: file_path, search_block, replace_block."
}

func (t *ReplaceBlockTool) resolveFilePath(filePath string) string {
	if filepath.IsAbs(filePath) {
		return filePath
	}
	return filepath.Join(t.workingDir, filePath)
}

// ExecuteReplaceBlock implements the replace_block tool logic with robust validation
func (t *ReplaceBlockTool) ExecuteReplaceBlock(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	// Validate required parameters
	filePath, ok := params["file_path"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("file_path parameter must be a string"), "ExecuteReplaceBlock")
	}

	// Handle search_block (allow it to be empty for file creation if desired, but check type)
	var searchBlock string
	if val, exists := params["search_block"]; exists {
		if valStr, ok := val.(string); ok {
			searchBlock = valStr
		} else {
			return nil, util.WrapError(ctx, fmt.Errorf("search_block parameter must be a string"), "ExecuteReplaceBlock")
		}
	} else {
		// If missing, return error as it's required by schema usually
		return nil, util.WrapError(ctx, fmt.Errorf("search_block parameter is required"), "ExecuteReplaceBlock")
	}

	replaceBlock, ok := params["replace_block"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("replace_block parameter must be a string"), "ExecuteReplaceBlock")
	}

	if filePath == "" {
		return nil, util.WrapError(ctx, fmt.Errorf("file_path cannot be empty"), "ExecuteReplaceBlock")
	}

	// Security check
	if strings.Contains(filePath, "..") || strings.Contains(filePath, "~") {
		return nil, util.WrapError(ctx, fmt.Errorf("file_path contains invalid characters"), "ExecuteReplaceBlock")
	}

	fullPath := t.resolveFilePath(filePath)

	// Case 1: Create new file (searchBlock is empty)
	if searchBlock == "" {
		if _, err := os.Stat(fullPath); err == nil {
			return map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("file already exists: %s", filePath),
			}, nil
		}

		parentDir := filepath.Dir(fullPath)
		if err := os.MkdirAll(parentDir, 0755); err != nil {
			return nil, util.WrapError(ctx, err, "ExecuteReplaceBlock::MkdirAll")
		}

		if len(replaceBlock) > 10*1024*1024 {
			return map[string]interface{}{
				"success": false,
				"error":   "new file content is too large (max 10MB)",
			}, nil
		}

		if err := ioutil.WriteFile(fullPath, []byte(replaceBlock), 0644); err != nil {
			return nil, util.WrapError(ctx, err, "ExecuteReplaceBlock::WriteFile")
		}

		return map[string]interface{}{
			"success": true,
			"file":    filePath,
			"message": "New file created successfully",
		}, nil
	}

	// Case 2: Replace in existing file
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("file does not exist: %s", filePath),
		}, nil
	}

	data, err := ioutil.ReadFile(fullPath)
	if err != nil {
		return nil, util.WrapError(ctx, err, "ExecuteReplaceBlock::ReadFile")
	}

	if len(data) > 10*1024*1024 {
		return map[string]interface{}{
			"success": false,
			"error":   "file is too large (max 10MB)",
		}, nil
	}

	content := string(data)

	if !strings.Contains(content, searchBlock) {
		return map[string]interface{}{
			"success": false,
			"error":   "search_block not found in file",
		}, nil
	}

	occurrences := strings.Count(content, searchBlock)
	if occurrences > 1 {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("search_block appears %d times in file, please provide more context to make it unique", occurrences),
		}, nil
	}

	newContent := strings.Replace(content, searchBlock, replaceBlock, 1)

	if err := ioutil.WriteFile(fullPath, []byte(newContent), 0644); err != nil {
		return nil, util.WrapError(ctx, err, "ExecuteReplaceBlock::WriteFile")
	}

	return map[string]interface{}{
		"success": true,
		"file":    filePath,
		"message": "Successfully replaced block",
	}, nil
}

// Call handles the raw input string, parsing it and calling ExecuteReplaceBlock
func (t *ReplaceBlockTool) Call(ctx context.Context, input string) (string, error) {
	var params map[string]interface{}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", util.WrapError(ctx, err, "ReplaceBlockTool: invalid input json")
	}

	result, err := t.ExecuteReplaceBlock(ctx, params)
	if err != nil {
		return "", err
	}

	resBytes, err := json.Marshal(result)
	if err != nil {
		return "", util.WrapError(ctx, err, "ReplaceBlockTool: failed to marshal result")
	}
	return string(resBytes), nil
}
