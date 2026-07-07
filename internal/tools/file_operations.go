package tools

import (
	"bufio"
	"context"
	"fmt"
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

// ═══════════════════════════════════════════════════════════════
// read_file 大文件防护常量
// ═══════════════════════════════════════════════════════════════

// MaxFileSizeForRangeRead 是任何读取操作的绝对硬限制。
// 超过此大小的文件将被拒绝读取，建议使用 grep 等搜索工具。
const MaxFileSizeForRangeRead = 500 * 1024 * 1024 // 500 MB

// MaxFileSizeForEntireRead 是 should_read_entire_file=true 的硬限制。
// 超过此大小的文件不允许整文件读取，必须使用行范围读取。
const MaxFileSizeForEntireRead = 10 * 1024 * 1024 // 10 MB

// SoftFileSizeLimit 是软限制阈值，超过此大小会触发警告。
const SoftFileSizeLimit = 2 * 1024 * 1024 // 2 MB

// MaxEntireFileLines 是 entire file 模式下返回的最大行数，超出部分截断。
// 最多返回 500 行。
const MaxEntireFileLines = 500

// MaxEntireFileContentBytes 是 entire file 模式下返回内容的最大字节数。
// 与 MaxEntireFileLines 双重保护：先触发的限制生效。
const MaxEntireFileContentBytes = 10 * 1024 // 10 KB

// MaxLineLength 是 bufio.Scanner 的单行缓冲区大小，用于处理 minified 文件中的超长行。
const MaxLineLength = 1024 * 1024 // 1 MB per line

// MaxLineRangeSize 是行范围读取的最大行数。
const MaxLineRangeSize = 250

// treeEntry represents a single item in the directory tree output.
type treeEntry struct {
	name  string
	isDir bool
}

// ExecuteReadFile 实现read_file工具 — 流式读取 + 分层大文件防护
func (t *FileOperationsTool) ExecuteReadFile(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	// ─────────────────────────────────────────────
	// 1. 参数解析
	// ─────────────────────────────────────────────
	targetFile, ok := params["target_file"].(string)
	if !ok || targetFile == "" {
		return nil, util.WrapError(ctx, fmt.Errorf("target_file parameter must be a non-empty string"), "executeReadFile")
	}

	fullPath := t.resolveFilePath(targetFile)
	shouldReadEntireFile, _ := params["should_read_entire_file"].(bool)
	startLineF, _ := params["start_line_one_indexed"].(float64)
	endLineF, _ := params["end_line_one_indexed_inclusive"].(float64)
	startLine := int(startLineF)
	endLine := int(endLineF)

	// ─────────────────────────────────────────────
	// 2. 文件存在性和大小预检 (os.Stat — 不打开文件)
	// ─────────────────────────────────────────────
	info, err := os.Stat(fullPath)
	if err != nil {
		return nil, util.WrapError(ctx, err, "executeReadFile::Stat")
	}
	if info.IsDir() {
		return nil, util.WrapError(ctx, fmt.Errorf("'%s' is a directory, not a file", targetFile), "executeReadFile")
	}
	fileSize := info.Size()

	// ─────────────────────────────────────────────
	// 3. 硬限制 #1: 绝对最大文件大小 (500MB)
	// ─────────────────────────────────────────────
	if fileSize > MaxFileSizeForRangeRead {
		return map[string]interface{}{
			"error": fmt.Sprintf(
				"File size %s (%d bytes) exceeds absolute maximum %s (%d bytes). Refusing to read. Use grep or other search tools to locate relevant sections first.",
				humanizeBytes(fileSize), fileSize,
				humanizeBytes(MaxFileSizeForRangeRead), MaxFileSizeForRangeRead,
			),
			"file_size_bytes": fileSize,
			"suggestion":      "Use search_by_regex or grep to find the relevant sections, then read specific line ranges.",
		}, nil
	}

	// ─────────────────────────────────────────────
	// 4. 硬限制 #2: entire file 模式的大小限制 (10MB)
	// ─────────────────────────────────────────────
	if shouldReadEntireFile && fileSize > MaxFileSizeForEntireRead {
		return map[string]interface{}{
			"error": fmt.Sprintf(
				"File size %s (%d bytes) exceeds entire-file read limit %s (%d bytes). Use start_line_one_indexed and end_line_one_indexed_inclusive to read in chunks of %d lines.",
				humanizeBytes(fileSize), fileSize,
				humanizeBytes(MaxFileSizeForEntireRead), MaxFileSizeForEntireRead,
				MaxLineRangeSize,
			),
			"file_size_bytes": fileSize,
			"suggestion": fmt.Sprintf(
				"Use line range reads: should_read_entire_file=false, start_line_one_indexed=1, end_line_one_indexed_inclusive=%d, then paginate by adding %d to both values.",
				MaxLineRangeSize, MaxLineRangeSize,
			),
		}, nil
	}

	// ─────────────────────────────────────────────
	// 5. 流式读取 — bufio.Scanner (O(1) 内存)
	// ─────────────────────────────────────────────
	f, err := os.Open(fullPath)
	if err != nil {
		return nil, util.WrapError(ctx, err, "executeReadFile::Open")
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// 调大 buffer 以处理 minified 文件中的超长行 (如 JS bundle)
	scanner.Buffer(make([]byte, 0, MaxLineLength), MaxLineLength)

	var (
		contentLines  []string
		totalLines    int
		truncated     bool
		contentBytes  int
		warnings      []string
		rangeComplete bool
	)

	// 行范围默认值处理
	if !shouldReadEntireFile {
		if startLine < 1 {
			startLine = 1
		}
		if endLine < 1 || endLine < startLine {
			endLine = startLine + MaxLineRangeSize - 1
		}
		// 限制范围大小不超过 MaxLineRangeSize
		if endLine-startLine+1 > MaxLineRangeSize {
			endLine = startLine + MaxLineRangeSize - 1
		}
	}

	for scanner.Scan() {
		totalLines++
		line := scanner.Text()

		if shouldReadEntireFile {
			// 双重限制: 行数 + 内容字节数
			if totalLines <= MaxEntireFileLines && contentBytes < MaxEntireFileContentBytes {
				contentLines = append(contentLines, line)
				contentBytes += len(line) + 1 // +1 for newline
			} else {
				truncated = true
				// 继续扫描仅为了计数总行数（O(1) 内存，不累积内容）
			}
		} else {
			// 行范围模式
			if totalLines >= startLine && totalLines <= endLine {
				contentLines = append(contentLines, line)
			}
			if totalLines >= endLine {
				rangeComplete = true
				// 继续扫描仅为了计数总行数
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, util.WrapError(ctx, fmt.Errorf("error scanning file %s: %w", targetFile, err), "executeReadFile::Scan")
	}

	// ─────────────────────────────────────────────
	// 6. 后置校验 & 警告
	// ─────────────────────────────────────────────

	// 行范围越界检查
	if !shouldReadEntireFile && len(contentLines) == 0 && totalLines > 0 {
		if startLine > totalLines {
			return map[string]interface{}{
				"error":           fmt.Sprintf("start_line_one_indexed (%d) exceeds file total lines (%d)", startLine, totalLines),
				"file_size_bytes": fileSize,
				"total_lines":     totalLines,
				"suggestion":      fmt.Sprintf("Use start_line_one_indexed between 1 and %d", totalLines),
			}, nil
		}
	}

	// 软限制警告 (文件 > 2MB)
	if fileSize > SoftFileSizeLimit {
		warnings = append(warnings, fmt.Sprintf(
			"File size %s exceeds soft limit %s. Consider using line ranges for targeted reads.",
			humanizeBytes(fileSize), humanizeBytes(SoftFileSizeLimit),
		))
	}

	// 截断警告 (entire file 模式)
	if shouldReadEntireFile && truncated {
		warnings = append(warnings, fmt.Sprintf(
			"Only first %d lines (%.1f KB of content) returned out of %d total lines. Use start_line_one_indexed and end_line_one_indexed_inclusive to read remaining content.",
			len(contentLines), float64(contentBytes)/1024, totalLines,
		))
	}

	// 空文件警告
	if totalLines == 0 {
		warnings = append(warnings, "File is empty (0 lines).")
	}

	_ = rangeComplete // 保留以备未来优化

	// ─────────────────────────────────────────────
	// 7. 构建向后兼容的返回值
	// ─────────────────────────────────────────────
	content := strings.Join(contentLines, "\n")

	// 核心字段（向后兼容 — 保留 content 和 lines）
	result := map[string]interface{}{
		"content":         content,
		"lines":           len(contentLines),
		"file_size_bytes": fileSize,
		"total_lines":     totalLines,
		"truncated":       truncated,
	}

	// 模式特有字段
	if shouldReadEntireFile {
		result["read_mode"] = "entire_file"
		if truncated {
			result["max_lines_returned"] = len(contentLines)
			result["max_content_bytes"] = contentBytes
		}
	} else {
		result["read_mode"] = "line_range"
		result["start_line"] = startLine
		result["end_line"] = endLine
		result["lines_before_range"] = max(0, startLine-1)
		result["lines_after_range"] = max(0, totalLines-endLine)
	}

	// 警告信息
	if len(warnings) > 0 {
		result["warning"] = strings.Join(warnings, "; ")
	}

	return result, nil
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
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
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

// humanizeBytes 将字节数转换为人类可读的字符串 (e.g., "1.5MB")
func humanizeBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(b)/float64(div), "KMGTPE"[exp])
}
