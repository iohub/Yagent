package tools

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"codeactor/internal/util"
)

// FileOperationsTool 实现文件操作相关工具
type FileOperationsTool struct {
	workingDir string
}

func NewFileOperationsTool(workingDir string) *FileOperationsTool {
	return &FileOperationsTool{
		workingDir: workingDir,
	}
}

func (t *FileOperationsTool) resolveFilePath(filePath string) string {
	if filepath.IsAbs(filePath) {
		return filePath
	}
	return filepath.Join(t.workingDir, filePath)
}

// ExecuteReadFile 实现read_file工具
func (t *FileOperationsTool) ExecuteReadFile(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	targetFile, ok := params["target_file"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("target_file parameter must be a string"), "executeReadFile")
	}

	fullPath := t.resolveFilePath(targetFile)
	shouldReadEntireFile, _ := params["should_read_entire_file"].(bool)
	startLine, _ := params["start_line_one_indexed"].(float64)
	endLine, _ := params["end_line_one_indexed_inclusive"].(float64)

	data, err := ioutil.ReadFile(fullPath)
	if err != nil {
		return nil, util.WrapError(ctx, err, "executeReadFile::ReadFile")
	}

	content := string(data)
	lines := strings.Split(content, "\n")

	if shouldReadEntireFile {
		return map[string]interface{}{
			"content": content,
			"lines":   len(lines),
		}, nil
	}

	// 读取特定行
	start := int(startLine) - 1
	end := int(endLine)

	if start < 0 {
		start = 0
	}
	if end > len(lines) {
		end = len(lines)
	}

	if start >= end {
		return nil, util.WrapError(ctx, fmt.Errorf("invalid line range: start=%d, end=%d", start+1, end), "executeReadFile")
	}

	selectedLines := lines[start:end]
	return map[string]interface{}{
		"content": strings.Join(selectedLines, "\n"),
		"lines":   len(selectedLines),
		"start":   start + 1,
		"end":     end,
	}, nil
}

// ExecuteWriteFile implements write_file tool
func (t *FileOperationsTool) ExecuteWriteFile(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	filePath, ok := params["file_path"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("file_path parameter must be a string"), "executeWriteFile")
	}

	content, ok := params["content"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("content parameter must be a string"), "executeWriteFile")
	}

	fullPath := t.resolveFilePath(filePath)

	// Ensure parent directory exists
	parentDir := filepath.Dir(fullPath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return nil, util.WrapError(ctx, err, "executeWriteFile::MkdirAll")
	}

	// Write file
	if err := ioutil.WriteFile(fullPath, []byte(content), 0644); err != nil {
		return nil, util.WrapError(ctx, err, "executeWriteFile::WriteFile")
	}

	return map[string]interface{}{
		"success": true,
		"file":    filePath,
		"message": "File written successfully",
	}, nil
}

// ExecuteEditFile 实现edit_file工具
func (t *FileOperationsTool) ExecuteEditFile(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	// 验证必需参数
	filePath, ok := params["file_path"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("file_path parameter must be a string"), "executeEditFile")
	}

	oldString, ok := params["old_string"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("old_string parameter must be a string"), "executeEditFile")
	}

	newString, ok := params["new_string"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("new_string parameter must be a string"), "executeEditFile")
	}

	// 验证文件路径不为空
	if filePath == "" {
		return nil, util.WrapError(ctx, fmt.Errorf("file_path cannot be empty"), "executeEditFile")
	}

	// 验证文件路径的安全性（防止路径遍历攻击）
	if strings.Contains(filePath, "..") || strings.Contains(filePath, "~") {
		return nil, util.WrapError(ctx, fmt.Errorf("file_path contains invalid characters"), "executeEditFile")
	}

	// 解析文件路径
	fullPath := t.resolveFilePath(filePath)

	// 检查是否为创建新文件的情况（old_string为空）
	if oldString == "" {
		// 检查文件是否已存在
		if _, err := os.Stat(fullPath); err == nil {
			return map[string]interface{}{
				"success": false,
				"error":   fmt.Sprintf("file already exists: %s", filePath),
			}, nil
		}

		// 确保父目录存在
		parentDir := filepath.Dir(fullPath)
		if err := os.MkdirAll(parentDir, 0755); err != nil {
			return nil, util.WrapError(ctx, err, "executeEditFile::MkdirAll")
		}

		// 检查新文件内容大小限制
		if len(newString) > 10*1024*1024 { // 10MB限制
			return map[string]interface{}{
				"success": false,
				"error":   "new file content is too large (max 10MB)",
			}, nil
		}

		// 创建新文件
		if err := ioutil.WriteFile(fullPath, []byte(newString), 0644); err != nil {
			return nil, util.WrapError(ctx, err, "executeEditFile::WriteFile")
		}

		return map[string]interface{}{
			"success": true,
			"file":    filePath,
			"message": "New file created successfully",
		}, nil
	}

	// 检查文件是否存在（对于编辑现有文件的情况）
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("file does not exist: %s", filePath),
		}, nil
	}

	// 读取文件内容
	data, err := ioutil.ReadFile(fullPath)
	if err != nil {
		return nil, util.WrapError(ctx, err, "executeEditFile::ReadFile")
	}

	// 检查文件大小限制（防止读取过大的文件）
	if len(data) > 10*1024*1024 { // 10MB限制
		return map[string]interface{}{
			"success": false,
			"error":   "file is too large (max 10MB)",
		}, nil
	}

	content := string(data)

	// 检查是否包含要替换的字符串
	if !strings.Contains(content, oldString) {
		return map[string]interface{}{
			"success": false,
			"error":   "old_string not found in file",
		}, nil
	}

	// 计算替换次数，确保只替换一次
	occurrences := strings.Count(content, oldString)
	if occurrences > 1 {
		return map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("old_string appears %d times in file, but edit_file tool can only replace one instance at a time", occurrences),
		}, nil
	}

	// 执行替换（只替换第一次出现）
	newContent := strings.Replace(content, oldString, newString, 1)

	// 写回文件
	if err := ioutil.WriteFile(fullPath, []byte(newContent), 0644); err != nil {
		return nil, util.WrapError(ctx, err, "executeEditFile::WriteFile")
	}

	return map[string]interface{}{
		"success": true,
		"file":    filePath,
		"message": "File updated successfully",
	}, nil
}

// ExecuteDeleteFile 实现delete_file工具
func (t *FileOperationsTool) ExecuteDeleteFile(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	targetFile, ok := params["target_file"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("target_file parameter must be a string"), "executeDeleteFile")
	}

	fullPath := t.resolveFilePath(targetFile)

	if err := os.Remove(fullPath); err != nil {
		return nil, util.WrapError(ctx, err, "executeDeleteFile::Remove")
	}

	return map[string]interface{}{
		"success": true,
		"file":    targetFile,
		"message": "File deleted successfully",
	}, nil
}

// ExecuteCreateFile 实现create_file工具
func (t *FileOperationsTool) ExecuteCreateFile(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	targetFile, ok := params["target_file"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("target_file parameter must be a string"), "executeCreateFile")
	}

	content, ok := params["content"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("content parameter must be a string"), "executeCreateFile")
	}

	fullPath := t.resolveFilePath(targetFile)

	// 检查文件是否已存在
	if _, err := os.Stat(fullPath); err == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "file already exists",
		}, nil
	}

	// 创建目录
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, util.WrapError(ctx, err, "executeCreateFile::MkdirAll")
	}

	// 创建文件
	if err := ioutil.WriteFile(fullPath, []byte(content), 0644); err != nil {
		return nil, util.WrapError(ctx, err, "executeCreateFile::WriteFile")
	}

	return map[string]interface{}{
		"success": true,
		"file":    targetFile,
		"message": "File created successfully",
	}, nil
}