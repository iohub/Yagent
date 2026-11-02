package tools

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"yagent/internal/diff"
	"yagent/internal/util"
)

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

var defaultIgnoredDirs = map[string]bool{
	".git": true, ".svn": true, ".hg": true, ".bzr": true,

	"node_modules": true, "bower_components": true, "jspm_packages": true,
	"vendor": true, "venv": true, ".venv": true, "env": true,
	".cargo": true, ".npm": true, ".yarn": true,
	"Pods": true, "Carthage": true, ".swiftpm": true,

	"build": true, "dist": true, "out": true, "target": true,
	"Debug": true, "Release": true,
	".next": true, ".nuxt": true, ".svelte-kit": true,
	".gradle": true, "cmake-build-debug": true, "cmake-build-release": true,
	".dart_tool": true, "bin": true, "obj": true,

	"__pycache__": true, ".pytest_cache": true, ".mypy_cache": true,
	".ruff_cache": true, ".tox": true, ".eggs": true,
	".ipynb_checkpoints": true,

	".cache": true, ".parcel-cache": true, ".turbo": true,
	"tmp": true, "temp": true, ".tmp": true,
	".sass-cache": true, ".eslintcache": true,

	"coverage": true, ".nyc_output": true,

	".idea": true, ".vscode": true, ".vs": true,
	".project": true, ".settings": true,

	".terraform": true, ".serverless": true, ".vercel": true,

	"logs": true, "log": true,
}

var defaultIgnoredFiles = map[string]bool{
	".DS_Store":   true,
	"Thumbs.db":   true,
	"desktop.ini": true,
}

var defaultIgnoredExts = map[string]bool{
	".pyc": true, ".pyo": true, ".pyd": true,
	".class": true,
	".o":     true, ".a": true, ".so": true, ".dylib": true,
	".exe": true, ".dll": true, ".lib": true,
	".obj": true, ".pdb": true, ".idb": true,
}

func hasIgnoredExt(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return defaultIgnoredExts[ext]
}

var maxFileSizeForRead int64 = 500 * 1024 * 1024

var maxReadLines = 1500
var maxReadContentBytes = 50 * 1024

const binarySniffBytes = 8 * 1024

const readChunkSize = 64 * 1024

var newlineBytes = []byte{'\n'}

type treeEntry struct {
	name  string
	isDir bool
}

func (t *FileOperationsTool) ExecuteReadFile(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	targetFile, ok := params["target_file"].(string)
	if !ok || targetFile == "" {
		return nil, util.WrapError(ctx, fmt.Errorf("target_file parameter must be a non-empty string"), "executeReadFile")
	}

	startLine := 1
	if v, ok := params["start_line"].(float64); ok && int(v) > 1 {
		startLine = int(v)
	}
	maxLines := maxReadLines
	cappedWindow := true
	if v, ok := params["max_lines"].(float64); ok && int(v) >= 1 && int(v) < maxReadLines {
		maxLines = int(v)
		cappedWindow = false
	}

	fullPath := t.resolveFilePath(targetFile)
	info, err := os.Stat(fullPath)
	if err != nil {
		return nil, util.WrapError(ctx, err, "executeReadFile::Stat")
	}
	if info.IsDir() {
		return nil, util.WrapError(ctx, fmt.Errorf("'%s' is a directory, not a file", targetFile), "executeReadFile")
	}
	fileSize := info.Size()

	if fileSize > maxFileSizeForRead {
		return map[string]interface{}{
			"error": fmt.Sprintf(
				"File size %s (%d bytes) exceeds absolute maximum %s (%d bytes). Refusing to read. Use grep or other search tools to locate relevant sections first.",
				humanizeBytes(fileSize), fileSize,
				humanizeBytes(maxFileSizeForRead), maxFileSizeForRead,
			),
			"file_size_bytes": fileSize,
			"suggestion":      "Use search_by_regex or grep to find the relevant sections, then read specific line ranges.",
		}, nil
	}

	f, err := os.Open(fullPath)
	if err != nil {
		return nil, util.WrapError(ctx, err, "executeReadFile::Open")
	}
	defer f.Close()

	scan, err := readFileInRange(f, startLine, maxLines, maxReadContentBytes, cappedWindow)
	if err != nil {
		return nil, util.WrapError(ctx, fmt.Errorf("error reading file %s: %w", targetFile, err), "executeReadFile::Read")
	}

	if scan.binary {
		return map[string]interface{}{
			"error":           fmt.Sprintf("'%s' looks like a binary file (NUL byte in the first %s); refusing to read it as text.", targetFile, humanizeBytes(binarySniffBytes)),
			"file_size_bytes": fileSize,
			"suggestion":      "Use search_by_regex for a text file, or an appropriate viewer for binary content.",
		}, nil
	}

	if scan.lines == 0 && startLine > scan.totalLines && scan.totalLines > 0 {
		return map[string]interface{}{
			"error":           fmt.Sprintf("start_line (%d) exceeds file total lines (%d)", startLine, scan.totalLines),
			"file_size_bytes": fileSize,
			"total_lines":     scan.totalLines,
			"suggestion":      fmt.Sprintf("Use start_line between 1 and %d", scan.totalLines),
		}, nil
	}

	result := map[string]interface{}{
		"content":            scan.content,
		"lines":              scan.lines,
		"start_line":         startLine,
		"end_line":           scan.endLine,
		"total_lines":        scan.totalLines,
		"lines_before_range": max(0, startLine-1),
		"lines_after_range":  max(0, scan.totalLines-scan.endLine),
		"file_size_bytes":    fileSize,
		"truncated":          scan.truncated,
	}

	var warnings []string
	if scan.totalLines == 0 {
		warnings = append(warnings, "File is empty (0 lines).")
	}
	if scan.truncated {
		warnings = append(warnings, fmt.Sprintf(
			"Truncated at %s / %d lines (file has %d lines). Continue with start_line=%d.",
			humanizeBytes(int64(len(scan.content))), maxLines, scan.totalLines, scan.endLine+1,
		))
	}
	if len(scan.content) > 0 && !utf8.ValidString(scan.content) {
		warnings = append(warnings, "content contains invalid UTF-8; bytes may be mojibake")
	}
	if len(warnings) > 0 {
		result["warning"] = strings.Join(warnings, "; ")
	}

	return result, nil
}

type fileScan struct {
	content    string
	lines      int
	endLine    int
	totalLines int
	truncated  bool
	binary     bool
}

func readFileInRange(f *os.File, startLine, maxLines, budget int, cappedWindow bool) (fileScan, error) {
	var (
		out               fileScan
		chunk             = make([]byte, readChunkSize)
		acc               []byte
		lineBuf           []byte
		lineOver          bool
		got               int
		contentLen        int
		endLine           int
		lineNo            int
		pending           bool
		collecting        = true
		sniffed           = false
		stoppedByBudget   bool
		stoppedByLineCap  bool
		linePrefixCutSeen bool
	)

	take := func(ln int, line []byte, cut bool) bool {
		if ln < startLine {
			return true
		}
		sep := 0
		if got > 0 {
			sep = 1
		}
		if contentLen+sep+len(line) > budget {
			stoppedByBudget = true
			return false
		}
		if sep == 1 {
			acc = append(acc, '\n')
		}
		acc = append(acc, line...)
		contentLen += sep + len(line)
		got++
		endLine = ln
		if cut {
			linePrefixCutSeen = true
		}
		if got >= maxLines {
			stoppedByLineCap = true
			return false
		}
		return true
	}

	addSeg := func(seg []byte) {
		if len(seg) == 0 {
			return
		}
		pending = true
		if lineOver {
			return
		}
		if len(lineBuf)+len(seg) <= budget {
			lineBuf = append(lineBuf, seg...)
			return
		}
		if room := budget - len(lineBuf); room > 0 {
			lineBuf = append(lineBuf, seg[:room]...)
		}
		lineOver = true
	}

	for {
		n, rerr := f.Read(chunk)
		if n > 0 {
			data := chunk[:n]
			if !sniffed {
				sniffed = true
				win := data
				if len(win) > binarySniffBytes {
					win = win[:binarySniffBytes]
				}
				if bytes.IndexByte(win, 0) >= 0 {
					out.binary = true
					return out, nil
				}
			}

			pos := 0
			for collecting && pos < len(data) {
				idx := bytes.IndexByte(data[pos:], '\n')
				if idx < 0 {
					addSeg(data[pos:])
					pos = len(data)
					break
				}
				addSeg(data[pos : pos+idx])
				pos += idx + 1
				lineNo++
				pending = false
				if !take(lineNo, lineBuf, lineOver) {
					collecting = false
				}
				lineBuf = lineBuf[:0]
				lineOver = false
			}
			if !collecting && pos < len(data) {
				rest := data[pos:]
				lineNo += bytes.Count(rest, newlineBytes)
				pending = rest[len(rest)-1] != '\n'
			}
		}
		if rerr != nil {
			if rerr == io.EOF {
				break
			}
			return out, rerr
		}
	}

	if pending {
		lineNo++
		if collecting {
			take(lineNo, lineBuf, lineOver)
		}
	}

	out.content = string(acc)
	out.lines = got
	out.endLine = endLine
	out.totalLines = lineNo
	out.truncated = stoppedByBudget || linePrefixCutSeen ||
		(stoppedByLineCap && cappedWindow && lineNo > endLine)
	return out, nil
}

func (t *FileOperationsTool) ExecuteDeleteFile(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	if paths, ok := params["file_paths"].([]interface{}); ok {
		var deletedFiles []string
		var errors []string
		for _, p := range paths {
			if pathStr, ok := p.(string); ok {
				fullPath := t.resolveFilePath(pathStr)
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

	if _, err := os.Stat(fullOldPath); os.IsNotExist(err) {
		return nil, util.WrapError(ctx, fmt.Errorf("source file does not exist: %s", filePath), "executeRenameFile")
	}

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

		if info.IsDir() && ignoredDirs[info.Name()] {
			result = append(result, fmt.Sprintf("%s/", relPath))
			return filepath.SkipDir
		}

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

	if _, err := os.Stat(fullPath); err == nil {
		return map[string]interface{}{
			"success": false,
			"error":   "file already exists",
		}, nil
	}

	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, util.WrapError(ctx, err, "executeCreateFile::MkdirAll")
	}

	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		return nil, util.WrapError(ctx, err, "executeCreateFile::WriteFile")
	}

	diffText := diff.GenerateUnifiedDiff(targetFile, "", content)

	return map[string]interface{}{
		"success": true,
		"file":    targetFile,
		"message": "File created successfully",
		"diff":    diffText,
	}, nil
}

func (t *FileOperationsTool) ExecutePrintDirTree(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	dirPath, ok := params["dir_path"].(string)
	if !ok || dirPath == "" {
		return nil, util.WrapError(ctx, fmt.Errorf("dir_path parameter must be a valid string"), "executePrintDirTree")
	}

	maxDepth := 2
	if d, ok := params["max_depth"].(float64); ok {
		maxDepth = int(d)
		if maxDepth > 2 {
			maxDepth = 2
		}
	}

	showFiles := true
	if v, ok := params["show_files"]; ok {
		if b, ok := v.(bool); ok {
			showFiles = b
		}
	}

	maxItems := 50
	if v, ok := params["max_items"]; ok {
		if f, ok := v.(float64); ok && f > 0 {
			maxItems = int(f)
		}
	}

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

	fullPath := t.resolveFilePath(dirPath)
	info, err := os.Stat(fullPath)
	if err != nil {
		return nil, util.WrapError(ctx, err, "executePrintDirTree::Stat")
	}
	if !info.IsDir() {
		return nil, util.WrapError(ctx, fmt.Errorf("path is not a directory: %s", dirPath), "executePrintDirTree")
	}

	var sb strings.Builder

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
		sb.WriteString(fmt.Sprintf("%s[error: %v]\n", prefix, err))
		return nil
	}

	var items []treeEntry
	for _, entry := range entries {
		name := entry.Name()

		if entry.Type()&os.ModeSymlink != 0 {
			continue
		}

		if entry.IsDir() {
			if defaultIgnoredDirs[name] || extraIgnores[name] {
				continue
			}
			items = append(items, treeEntry{name: name, isDir: true})
		} else {
			if defaultIgnoredFiles[name] || hasIgnoredExt(name) {
				continue
			}
			if showFiles {
				items = append(items, treeEntry{name: name, isDir: false})
			}
		}
	}

	sort.SliceStable(items, func(i, j int) bool {
		return items[i].name < items[j].name
	})

	total := len(items)
	truncated := false
	if maxItems > 0 && total > maxItems {
		items = items[:maxItems]
		truncated = true
	}

	for i, e := range items {
		isLast := i == len(items)-1 && !truncated

		connector := "├── "
		if isLast {
			connector = "└── "
		}

		displayName := e.name
		if e.isDir {
			displayName += "/"

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

	if truncated {
		remaining := total - maxItems
		sb.WriteString(fmt.Sprintf(
			"%s└── ... %d more items omitted (max_items=%d)\n",
			prefix, remaining, maxItems,
		))
	}

	return nil
}

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
