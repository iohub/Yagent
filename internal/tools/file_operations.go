package tools

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"sort"
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

// defaultIgnoredDirs contains directories that are typically non-informative.
var defaultIgnoredDirs = map[string]bool{
	// Version Control
	".git": true, ".svn": true, ".hg": true, ".bzr": true,

	// Dependencies / Package Managers
	"node_modules": true, "bower_components": true, "jspm_packages": true,
	"vendor": true, "venv": true, ".venv": true, "env": true,
	".cargo": true, ".npm": true, ".yarn": true,
	"Pods": true, "Carthage": true, ".swiftpm": true,

	// Build / Compilation Output
	"build": true, "dist": true, "out": true, "target": true,
	"Debug": true, "Release": true,
	".next": true, ".nuxt": true, ".svelte-kit": true,
	".gradle": true, "cmake-build-debug": true, "cmake-build-release": true,
	".dart_tool": true, "bin": true, "obj": true,

	// Python Cache & Virtual Environments
	"__pycache__": true, ".pytest_cache": true, ".mypy_cache": true,
	".ruff_cache": true, ".tox": true, ".eggs": true,
	".ipynb_checkpoints": true,

	// Cache / Temp
	".cache": true, ".parcel-cache": true, ".turbo": true,
	"tmp": true, "temp": true, ".tmp": true,
	".sass-cache": true, ".eslintcache": true,

	// Test Coverage
	"coverage": true, ".nyc_output": true,

	// IDE / Editor
	".idea": true, ".vscode": true, ".vs": true,
	".project": true, ".settings": true,

	// Infrastructure / Deploy
	".terraform": true, ".serverless": true, ".vercel": true,

	// Logs
	"logs": true, "log": true,
}

// defaultIgnoredFiles contains individual files that are typically non-informative.
var defaultIgnoredFiles = map[string]bool{
	".DS_Store":   true,
	"Thumbs.db":   true,
	"desktop.ini": true,
}

// defaultIgnoredExts contains file extensions for compiled/binary artifacts.
var defaultIgnoredExts = map[string]bool{
	".pyc": true, ".pyo": true, ".pyd": true,
	".class": true,
	".o": true, ".a": true, ".so": true, ".dylib": true,
	".exe": true, ".dll": true, ".lib": true,
	".obj": true, ".pdb": true, ".idb": true,
}

// hasIgnoredExt checks if a file has an ignored extension.
func hasIgnoredExt(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return defaultIgnoredExts[ext]
}

// treeEntry represents a single item in the directory tree output.
type treeEntry struct {
	name  string
	isDir bool
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
	if !ok || dirPath == "" {
		return nil, util.WrapError(ctx, fmt.Errorf("dir_path parameter must be a valid string"), "executePrintDirTree")
	}

	// --- Parse optional: max_depth (default 3) ---
	maxDepth := 3
	if d, ok := params["max_depth"].(float64); ok {
		maxDepth = int(d)
	}

	// --- Parse optional: show_files (default true) ---
	showFiles := true
	if v, ok := params["show_files"]; ok {
		if b, ok := v.(bool); ok {
			showFiles = b
		}
	}

	// --- Parse optional: max_items (default 50, ≤0 means unlimited) ---
	maxItems := 50
	if v, ok := params["max_items"]; ok {
		if f, ok := v.(float64); ok && f > 0 {
			maxItems = int(f)
		}
	}

	// --- Parse optional: ignore_extra (merged with defaults) ---
	extraIgnores := make(map[string]bool)
	if v, ok := params["ignore_extra"]; ok {
		if arr, ok := v.([]interface{}); ok {
			for _, item := range arr {
				if s, ok := item.(string); ok && s != "" {
					extraIgnores[s] = true
				}
			}
		}
	}

	// --- Resolve & validate path ---
	fullPath := t.resolveFilePath(dirPath)
	info, err := os.Stat(fullPath)
	if err != nil {
		return nil, util.WrapError(ctx, err, "executePrintDirTree::Stat")
	}
	if !info.IsDir() {
		return nil, util.WrapError(ctx, fmt.Errorf("path is not a directory: %s", dirPath), "executePrintDirTree")
	}

	// --- Build tree ---
	var sb strings.Builder

	// Root display
	rootDisplay := dirPath
	if !strings.HasSuffix(rootDisplay, "/") {
		rootDisplay += "/"
	}
	sb.WriteString(rootDisplay + "\n")

	if maxDepth > 0 {
		if err := t.walkDirTree(fullPath, 1, maxDepth, showFiles, maxItems, extraIgnores, "", &sb); err != nil {
			return nil, util.WrapError(ctx, err, "executePrintDirTree::walkDirTree")
		}
	}

	return map[string]interface{}{
		"output": sb.String(),
	}, nil
}

// walkDirTree recursively walks a directory and appends tree structure to sb.
func (t *FileOperationsTool) walkDirTree(
	dir string,
	depth, maxDepth int,
	showFiles bool,
	maxItems int,
	extraIgnores map[string]bool,
	prefix string,
	sb *strings.Builder,
) error {
	if depth > maxDepth {
		return nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		// Graceful degradation: show error but don't halt
		sb.WriteString(fmt.Sprintf("%s[error: %v]\n", prefix, err))
		return nil
	}

	// --- Filter & categorize ---
	var items []treeEntry
	for _, entry := range entries {
		name := entry.Name()

		// Skip symlinks to prevent cycles
		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}

		if entry.IsDir() {
			// Skip ignored directories
			if defaultIgnoredDirs[name] || extraIgnores[name] {
				continue
			}
			items = append(items, treeEntry{name: name, isDir: true})
		} else {
			// Skip ignored files and extensions
			if defaultIgnoredFiles[name] || hasIgnoredExt(name) {
				continue
			}
			if showFiles {
				items = append(items, treeEntry{name: name, isDir: false})
			}
			// When !showFiles, files are silently skipped
		}
	}

	// --- Sort: directories first, then alphabetically within each group ---
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].isDir != items[j].isDir {
			return items[i].isDir // directories first
		}
		return items[i].name < items[j].name
	})

	// --- Truncate if exceeding max_items ---
	total := len(items)
	truncated := false
	if maxItems > 0 && total > maxItems {
		items = items[:maxItems]
		truncated = true
	}

	// --- Print items ---
	for i, e := range items {
		isLast := i == len(items)-1 && !truncated

		connector := "├── "
		if isLast {
			connector = "└── "
		}

		displayName := e.name
		if e.isDir {
			displayName += "/"

			// When showFiles is false, show file count per directory
			if !showFiles {
				childPath := filepath.Join(dir, e.name)
				childEntries, err := os.ReadDir(childPath)
				if err == nil {
					fileCount := 0
					for _, ce := range childEntries {
						if ce.IsDir() || ce.Type()&os.ModeSymlink != 0 {
							continue
						}
						cn := ce.Name()
						if defaultIgnoredFiles[cn] || hasIgnoredExt(cn) {
							continue
						}
						fileCount++
					}
					if fileCount > 0 {
						displayName += fmt.Sprintf(" (%d files)", fileCount)
					}
				}
			}
		}

		sb.WriteString(prefix + connector + displayName + "\n")

		// Recurse into subdirectories
		if e.isDir {
			childPrefix := prefix + "│   "
			if isLast {
				childPrefix = prefix + "    "
			}
			if err := t.walkDirTree(
				filepath.Join(dir, e.name),
				depth+1, maxDepth,
				showFiles, maxItems,
				extraIgnores,
				childPrefix, sb,
			); err != nil {
				return err
			}
		}
	}

	// --- Truncation indicator ---
	if truncated {
		remaining := total - maxItems
		sb.WriteString(fmt.Sprintf(
			"%s└── ... %d more items omitted (max_items=%d)\n",
			prefix, remaining, maxItems,
		))
	}

	return nil
}
