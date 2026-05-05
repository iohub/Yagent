package tools

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"codeactor/internal/diff"
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
	if end <= 0 || end > len(lines) {
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

// ExecuteDeleteFile 实现delete_file工具
func (t *FileOperationsTool) ExecuteDeleteFile(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	// 尝试获取 file_paths (数组)
	if paths, ok := params["file_paths"].([]interface{}); ok {
		var deletedFiles []string
		var errors []string
		for _, p := range paths {
			if pathStr, ok := p.(string); ok {
				fullPath := t.resolveFilePath(pathStr)
				// 使用 RemoveAll 以支持目录删除
				if err := os.RemoveAll(fullPath); err != nil {
					errors = append(errors, fmt.Sprintf("%s: %v", pathStr, err))
				} else {
					deletedFiles = append(deletedFiles, pathStr)
				}
			}
		}
		if len(errors) > 0 {
			return map[string]interface{}{
				"success": false,
				"deleted": deletedFiles,
				"errors":  errors,
			}, nil
		}
		return map[string]interface{}{
			"success": true,
			"deleted": deletedFiles,
			"message": "Files deleted successfully",
		}, nil
	}

	targetFile, ok := params["target_file"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("target_file or file_paths parameter missing"), "executeDeleteFile")
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

// ExecuteRenameFile 实现rename_file工具
func (t *FileOperationsTool) ExecuteRenameFile(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	filePath, ok := params["file_path"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("file_path parameter must be a string"), "executeRenameFile")
	}
	renameFilePath, ok := params["rename_file_path"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("rename_file_path parameter must be a string"), "executeRenameFile")
	}

	fullOldPath := t.resolveFilePath(filePath)
	fullNewPath := t.resolveFilePath(renameFilePath)

	// 检查源文件是否存在
	if _, err := os.Stat(fullOldPath); os.IsNotExist(err) {
		return nil, util.WrapError(ctx, fmt.Errorf("source file does not exist: %s", filePath), "executeRenameFile")
	}

	// 确保目标目录存在
	newDir := filepath.Dir(fullNewPath)
	if err := os.MkdirAll(newDir, 0755); err != nil {
		return nil, util.WrapError(ctx, err, "executeRenameFile::MkdirAll")
	}

	if err := os.Rename(fullOldPath, fullNewPath); err != nil {
		return nil, util.WrapError(ctx, err, "executeRenameFile::Rename")
	}

	return map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("Renamed %s to %s", filePath, renameFilePath),
	}, nil
}

// ExecuteListDir 实现list_dir工具
func (t *FileOperationsTool) ExecuteListDir(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	dirPath, ok := params["dir_path"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("dir_path parameter must be a string"), "executeListDir")
	}

	maxDepth := 3
	if d, ok := params["max_depth"].(float64); ok {
		maxDepth = int(d)
	}

	fullPath := t.resolveFilePath(dirPath)
	var result []string

	ignoredDirs := map[string]bool{
		".git":         true,
		"node_modules": true,
		".idea":        true,
		".vscode":      true,
		"__pycache__":  true,
	}

	err := filepath.Walk(fullPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(fullPath, path)
		if err != nil {
			return nil
		}

		if relPath == "." {
			return nil
		}

		// Check for ignored directories
		if info.IsDir() && ignoredDirs[info.Name()] {
			result = append(result, fmt.Sprintf("%s/", relPath))
			return filepath.SkipDir
		}

		// 计算深度
		depth := strings.Count(relPath, string(os.PathSeparator)) + 1
		if depth > maxDepth {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		tail := ""
		if info.IsDir() {
			tail = "/"
		}
		result = append(result, fmt.Sprintf("%s%s", relPath, tail))
		return nil
	})

	if err != nil {
		return nil, util.WrapError(ctx, err, "executeListDir::Walk")
	}

	return map[string]interface{}{
		"files": result,
		"count": len(result),
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

	// Generate diff for new file (empty → content)
	diffText := diff.GenerateUnifiedDiff(targetFile, "", content)

	return map[string]interface{}{
		"success": true,
		"file":    targetFile,
		"message": "File created successfully",
		"diff":    diffText,
	}, nil
}

// ExecutePrintDirTree 实现print_dir_tree工具
func (t *FileOperationsTool) ExecutePrintDirTree(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	dirPath, ok := params["dir_path"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("dir_path parameter must be a string"), "executePrintDirTree")
	}

	maxDepth := 3
	if d, ok := params["max_depth"].(float64); ok {
		maxDepth = int(d)
	}

	fullPath := t.resolveFilePath(dirPath)

	// Check if directory exists
	info, err := os.Stat(fullPath)
	if err != nil {
		return nil, util.WrapError(ctx, err, "executePrintDirTree::Stat")
	}
	if !info.IsDir() {
		return nil, util.WrapError(ctx, fmt.Errorf("path is not a directory: %s", dirPath), "executePrintDirTree")
	}

	ignoredDirs := map[string]bool{
		".git":         true,
		"node_modules": true,
		".idea":        true,
		".vscode":      true,
		"__pycache__":  true,
		".DS_Store":    true,
	}

	var buildTree func(path string, prefix string, depth int) (string, error)
	buildTree = func(path string, prefix string, depth int) (string, error) {
		if depth > maxDepth {
			return "", nil
		}

		entries, err := ioutil.ReadDir(path)
		if err != nil {
			return "", err
		}

		// Filter entries
		var filtered []os.FileInfo
		for _, entry := range entries {
			if ignoredDirs[entry.Name()] {
				continue
			}
			filtered = append(filtered, entry)
		}

		var result strings.Builder
		for i, entry := range filtered {
			isLast := i == len(filtered)-1
			connector := "├── "
			if isLast {
				connector = "└── "
			}

			result.WriteString(fmt.Sprintf("%s%s%s\n", prefix, connector, entry.Name()))

			if entry.IsDir() {
				newPrefix := prefix
				if isLast {
					newPrefix += "    "
				} else {
					newPrefix += "│   "
				}
				subTree, err := buildTree(filepath.Join(path, entry.Name()), newPrefix, depth+1)
				if err != nil {
					return "", err
				}
				result.WriteString(subTree)
			}
		}
		return result.String(), nil
	}

	tree, err := buildTree(fullPath, "", 1)
	if err != nil {
		return nil, util.WrapError(ctx, err, "executePrintDirTree::buildTree")
	}

	return map[string]interface{}{
		"output": tree,
	}, nil
}
