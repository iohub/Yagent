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
	return "PROPOSE a command to run on behalf of the user.\nIf you have this tool, note that you DO have the ability to run commands directly on the USER's system.\nNote that the user will have to approve the command before it is executed.\nThe user may reject it if it is not to their liking, or may modify the command before approving it.  If they do change it, take those changes into account.\nThe actual command will NOT execute until the user approves it. The user may not approve it immediately. Do NOT assume the command has started running.\nIf the step is WAITING for user approval, it has NOT started running.\nIn using these tools, adhere to the following guidelines:\n1. Based on the contents of the conversation, you will be told if you are in the same shell as a previous step or a different shell.\n2. If in a new shell, you should `cd` to the appropriate directory and do necessary setup in addition to running the command.\n3. If in the same shell, LOOK IN CHAT HISTORY for your current working directory.\n4. For ANY commands that would require user interaction, ASSUME THE USER IS NOT AVAILABLE TO INTERACT and PASS THE NON-INTERACTIVE FLAGS (e.g. --yes for npx).\n5. If the command would use a pager, append ` | cat` to the command.\n6. For commands that are long running/expected to run indefinitely until interruption, please run them in the background. To run jobs in the background, set `is_background` to true rather than changing the details of the command.\n7. Dont include any newlines in the command."
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
