package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"yagent/internal/diff"
	"yagent/internal/util"
)

// ReplaceBlockTool implements the search_replace tool for file editing.
type ReplaceBlockTool struct {
	workingDir string
}

func NewReplaceBlockTool(workingDir string) *ReplaceBlockTool {
	return &ReplaceBlockTool{workingDir: workingDir}
}

func (t *ReplaceBlockTool) Name() string {
	return "search_replace_in_file"
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

	// Handle old_string (allow it to be empty for file creation if desired, but check type)
	var oldString string
	if val, exists := params["old_string"]; exists {
		if valStr, ok := val.(string); ok {
			oldString = valStr
		} else {
			return nil, util.WrapError(ctx, fmt.Errorf("old_string parameter must be a string"), "ExecuteReplaceBlock")
		}
	} else {
		// If missing, return error as it's required by schema usually
		return nil, util.WrapError(ctx, fmt.Errorf("old_string parameter is required"), "ExecuteReplaceBlock")
	}

	newString, ok := params["new_string"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("new_string parameter must be a string"), "ExecuteReplaceBlock")
	}

	if filePath == "" {
		return nil, util.WrapError(ctx, fmt.Errorf("file_path cannot be empty"), "ExecuteReplaceBlock")
	}

	// Security check
	if strings.Contains(filePath, "..") || strings.Contains(filePath, "~") {
		return nil, util.WrapError(ctx, fmt.Errorf("file_path contains invalid characters"), "ExecuteReplaceBlock")
	}

	fullPath := t.resolveFilePath(filePath)

	// Case 1: Create new file (oldString is empty)
	if oldString == "" {
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

		if len(newString) > 10*1024*1024 {
			return map[string]interface{}{
				"success": false,
				"error":   "new file content is too large (max 10MB)",
			}, nil
		}

		if err := ioutil.WriteFile(fullPath, []byte(newString), 0644); err != nil {
			return nil, util.WrapError(ctx, err, "ExecuteReplaceBlock::WriteFile")
		}

		// Generate diff for new file (empty → newString)
		diffText := diff.GenerateUnifiedDiff(filePath, "", newString)

		return map[string]interface{}{
			"success": true,
			"file":    filePath,
			"message": "New file created successfully",
			"diff":    diffText,
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

	newContent, endings, errText := applyReplace(content, oldString, newString)
	if errText != "" {
		return map[string]interface{}{
			"success": false,
			"error":   errText,
		}, nil
	}

	if err := ioutil.WriteFile(fullPath, []byte(newContent), 0644); err != nil {
		return nil, util.WrapError(ctx, err, "ExecuteReplaceBlock::WriteFile")
	}

	// Generate diff between original and new content
	diffText := diff.GenerateUnifiedDiff(filePath, content, newContent)

	result := map[string]interface{}{
		"success": true,
		"file":    filePath,
		"message": "Successfully replaced block",
		"diff":    diffText,
	}
	if endings != "" {
		// 动过行尾就必须看得见：不写这一行，"模型抄了带 \r 的正文、编辑却按 \n 匹配"
		// 只能靠猜（根 AGENTS.md §11 坑 43）。
		result["line_endings"] = endings
	}
	return result, nil
}

const (
	notFoundError     = "old_string not found in file"
	mixedEndingsError = notFoundError + " (the file has mixed line endings; old_string must match byte-for-byte)"
)

// applyReplace 做一次"要求唯一"的字面替换，返回 (新内容, 行尾记账, 错误文本)。
//
// 两件事分开管，因为它们是两类事故：
//
//	**匹配**：read_file 的正文是字节保真的，但模型仍可能把 \r 抄丢。
//	所以一致 CRLF 的文件在精确匹配失败后，允许去掉 \r 再比一次，命中后整体还原 CRLF。
//	混合行尾不回退，而是点名原因 —— 静默把整个文件重写成一种行尾比一次编辑失败更糟。
//
//	**替换内容**：精确命中不等于行尾安全。old_string 是单行时它当然命中，
//	而 new_string 是多行 LF 文本 —— 直接写进去就在 CRLF 文件里造出混合行尾。
//	所以一致 CRLF 的文件里，纯 LF 的 new_string 先转成 CRLF 再插入。
func applyReplace(content, oldString, newString string) (string, string, string) {
	crlfFile := isUniformCRLF(content)

	if strings.Contains(content, oldString) {
		if n := strings.Count(content, oldString); n > 1 {
			return "", "", duplicateOldString(n)
		}
		if crlfFile && strings.Contains(newString, "\n") && !strings.Contains(newString, "\r\n") {
			return strings.Replace(content, oldString,
				strings.ReplaceAll(newString, "\n", "\r\n"), 1), "crlf-applied", ""
		}
		return strings.Replace(content, oldString, newString, 1), "", ""
	}

	if !crlfFile {
		if strings.Contains(content, "\r\n") {
			return "", "", mixedEndingsError
		}
		return "", "", notFoundError
	}

	normContent := strings.ReplaceAll(content, "\r\n", "\n")
	normOld := strings.ReplaceAll(oldString, "\r\n", "\n")
	switch n := strings.Count(normContent, normOld); n {
	case 0:
		return "", "", notFoundError + " (CRLF-normalized comparison also failed)"
	case 1:
	default:
		return "", "", duplicateOldString(n)
	}
	// newString 里的 \r 一律先塌成 \n：最后整体转 CRLF，否则照抄来的 "\r\n" 会变成 "\r\r\n"。
	normNew := strings.ReplaceAll(strings.ReplaceAll(newString, "\r\n", "\n"), "\r", "\n")
	return strings.ReplaceAll(strings.Replace(normContent, normOld, normNew, 1), "\n", "\r\n"), "crlf-normalized", ""
}

// isUniformCRLF 判断行尾是不是清一色 CRLF：每个 \n 前面是 \r，且没有游离的 \r。
func isUniformCRLF(content string) bool {
	nl := strings.Count(content, "\n")
	return nl > 0 && strings.Count(content, "\r\n") == nl && strings.Count(content, "\r") == nl
}

func duplicateOldString(n int) string {
	return fmt.Sprintf("old_string appears %d times in file, please provide more context to make it unique", n)
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
