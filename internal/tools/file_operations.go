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
	".o":     true, ".a": true, ".so": true, ".dylib": true,
	".exe": true, ".dll": true, ".lib": true,
	".obj": true, ".pdb": true, ".idb": true,
}

// hasIgnoredExt checks if a file has an ignored extension.
func hasIgnoredExt(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return defaultIgnoredExts[ext]
}

// ═══════════════════════════════════════════════════════════════
// read_file 的预算阶梯（数值口径只有这一处）
// ═══════════════════════════════════════════════════════════════

// maxFileSizeForRead 是任何读取的绝对硬限制：超过这条，成本在"扫一遍文件"
// 而不是"返回多少"，所以直接拒绝。写成 var 是用例的 seam——否则这条分支
// 要么触发不了，要么得造一个真的 500MB 文件。
var maxFileSizeForRead int64 = 500 * 1024 * 1024 // 500 MB

// maxReadLines / maxReadContentBytes 是单次返回的两个上限，先触发的生效。
// 50KB 覆盖本仓 100% 的 .go（实测 max 42.2KB / 1398 行）。两个都是 var，
// 理由同上：用例要把预算调到几 KB 才测得到"到顶之后"的形状。
var maxReadLines = 1500
var maxReadContentBytes = 50 * 1024 // 50 KB

// binarySniffBytes 是 NUL 嗅探窗口：前 8KB 出现 NUL 就当二进制拒读。
const binarySniffBytes = 8 * 1024

// readChunkSize 是流式读取的缓冲。旧写法用 bufio.Scanner，每次调用固定分配
// 1MiB（与文件大小无关，4 路并行就是 4MiB/批）；这里 64KiB 且整个读取只分配一次。
const readChunkSize = 64 * 1024

// newlineBytes 供尾扫的 bytes.Count 使用。
var newlineBytes = []byte{'\n'}

// treeEntry represents a single item in the directory tree output.
type treeEntry struct {
	name  string
	isDir bool
}

// ExecuteReadFile 实现 read_file — 单次读取 = min(maxReadLines 行, maxReadContentBytes 字节)。
//
// 没有"整读/区间"两种模式：只传 target_file 就是从第 1 行读到预算上限。
// truncated 表示"这一段没给全"（要继续用 start_line 接）；区间之外还有多少行
// 由 lines_after_range 说。预算比旧版宽得多是刻意的 —— read_file 的结果是
// TruncFirst（prio-0），第一档压缩最先砍它，所以"多读"可重取、
// 而"多跑一步"要把整个上下文重发一遍。判据见 docs/read-file-redesign.md §1.1。
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
	// cappedWindow：这次要的那一段本来就比上限宽（缺省读，或显式要了超过行帽的行数）。
	// 只有这种情況下撞行帽才算"我们没给全"；装满一个小区间不算，
	// 区间外还有多少由 lines_after_range 说。
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

	// 硬限制：超过这条的成本在"扫一遍文件"而不是"返回多少"，所以直接拒。
	// 判定必须在 Open 之前 —— 否则等于把拒绝的成本自己付掉。
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

	// 区间起点越界：给准合法范围，而不是返回一段空正文。
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
		// 分页提示两种模式共用（旧版只有整读给，区间读不给 → 模型只能猜）。
		warnings = append(warnings, fmt.Sprintf(
			"Truncated at %s / %d lines (file has %d lines). Continue with start_line=%d.",
			humanizeBytes(int64(len(scan.content))), maxLines, scan.totalLines, scan.endLine+1,
		))
	}
	if len(scan.content) > 0 && !utf8.ValidString(scan.content) {
		// 只警告不拒读：latin-1 之类的文本文件是合法的，一刀切会误伤。
		warnings = append(warnings, "content contains invalid UTF-8; bytes may be mojibake")
	}
	if len(warnings) > 0 {
		result["warning"] = strings.Join(warnings, "; ")
	}

	return result, nil
}

// fileScan 是一次流式读取的结果，字段与回执一一对应。
type fileScan struct {
	content    string
	lines      int
	endLine    int
	totalLines int
	truncated  bool
	binary     bool
}

// readFileInRange 分两阶段读一个文件：
//
//	阶段一（收集）逐行切分并收下区间内的行；
//	阶段二（尾扫）只对剩余字节数换行（bytes.Count，memchr 速度、零分配）。
//
// 切换发生在**行边界**，所以尾扫拿到的字节流总是从新的一行开始，行号不会错位。
// 这样 total_lines 保持精确，而代价从"每行都拷出来走一遍"降到"数一遍换行"。
//
// 行尾保真：只按 \n 切，\r 留在行内容里 —— 否则 CRLF 文件的正文照抄进
// search_replace_in_file 必然匹配不上。
//
// 单行不设上限而是按预算裁：旧写法靠 bufio.Scanner 的 1MiB 缓冲，一行超过它
// 整次读取直接失败（即使只要第 1-10 行）；现在超长行收成前缀 + truncated。
//
// cappedWindow 表示"要的那一段本来就比上限宽"（缺省读取，或显式要了超过
// maxReadLines 的行数）：只有这种情況下撞行帽才算截断。显式给了一个装满的
// 区间不算 —— 区间外还有多少由 lines_after_range 说。
func readFileInRange(f *os.File, startLine, maxLines, budget int, cappedWindow bool) (fileScan, error) {
	var (
		out               fileScan
		chunk             = make([]byte, readChunkSize)
		acc               []byte
		lineBuf           []byte
		lineOver          bool // 当前行因预算被裁过
		got               int
		contentLen        int
		endLine           int
		lineNo            int
		pending           bool // 自上个换行以来见过非换行字节
		collecting        = true
		sniffed           = false
		stoppedByBudget   bool
		stoppedByLineCap  bool
		linePrefixCutSeen bool
	)

	// take 尝试把第 ln 行收进正文；返回 false 表示不再收集（原因记在两个 flag 里）。
	take := func(ln int, line []byte, cut bool) bool {
		if ln < startLine {
			return true
		}
		sep := 0
		if got > 0 {
			sep = 1 // 行间分隔符，正文末尾不带换行
		}
		if contentLen+sep+len(line) > budget {
			// 预算已满。首行不需要特例：addSeg 已经把每一行封顶在 budget 以内，
			// 所以"一行比预算还长"在进到这里之前就变成了带前缀的正常一行。
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

	// addSeg 把一段不含换行的字节并进当前行；超出预算就停止累积（行号照数）。
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

	// 末尾没有换行的那一行也算一行。
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

	// --- Parse optional: max_depth (default 2, hard cap at 2) ---
	maxDepth := 2
	if d, ok := params["max_depth"].(float64); ok {
		maxDepth = int(d)
		if maxDepth > 2 {
			maxDepth = 2
		}
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

	// --- Sort: pure alphabetical order, ignoring file/directory type ---
	sort.SliceStable(items, func(i, j int) bool {
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
