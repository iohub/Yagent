package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// read_file 的行为回归网。这个工具在此之前没有任何直接单测
// （file_operations_test.go 只有 print_dir_tree 的两条），所以下面每条都带
// "夹具必须真的触发被测分支"的前置断言 —— 否则上限断言会恒真。

func readFileOrFail(t *testing.T, tool *FileOperationsTool, params map[string]interface{}) map[string]interface{} {
	t.Helper()
	res, err := tool.ExecuteReadFile(context.Background(), params)
	if err != nil {
		t.Fatalf("ExecuteReadFile 返回了 Go error: %v", err)
	}
	m, ok := res.(map[string]interface{})
	if !ok {
		t.Fatalf("返回值是 %T，期望 map", res)
	}
	return m
}

// readFieldInt 读一个整数回执字段；执行器返回的是 Go 原生 int/int64，不是 JSON。
func readFieldInt(t *testing.T, m map[string]interface{}, key string) int64 {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("回执缺字段 %q（现有键：%s）", key, mapKeys(m))
	}
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	}
	t.Fatalf("字段 %q 是 %T，不是整数", key, v)
	return 0
}

func readFieldString(t *testing.T, m map[string]interface{}, key string) string {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("回执缺字段 %q（现有键：%s）", key, mapKeys(m))
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("字段 %q 是 %T，不是字符串", key, v)
	}
	return s
}

func readFieldBool(t *testing.T, m map[string]interface{}, key string) bool {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Fatalf("回执缺字段 %q（现有键：%s）", key, mapKeys(m))
	}
	b, ok := v.(bool)
	if !ok {
		t.Fatalf("字段 %q 是 %T，不是布尔", key, v)
	}
	return b
}

// readErrorPayload 断言"失败也是回执、不是 Go error"这条语义：返回 error 文本。
func readErrorPayload(t *testing.T, m map[string]interface{}) string {
	t.Helper()
	v, ok := m["error"]
	if !ok {
		t.Fatalf("期望 error 回执，实际字段：%s", mapKeys(m))
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("error 字段是 %T，不是字符串", v)
	}
	return s
}

func mapKeys(m map[string]interface{}) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return strings.Join(keys, ", ")
}

// writeNumberedFile 写 lines 行 "NNNNN\n"（每行 6 字节），用于让**行数**帽生效的夹具。
func writeNumberedFile(t *testing.T, dir, name string, lines int) string {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= lines; i++ {
		fmt.Fprintf(&b, "%05d\n", i)
	}
	return writeRaw(t, dir, name, []byte(b.String()))
}

// writeFixedWidthFile 写每行恰好 width 字节的文件，用于让**字节**帽生效的夹具。
func writeFixedWidthFile(t *testing.T, dir, name string, lines, width int) string {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= lines; i++ {
		fmt.Fprintf(&b, "%05d%s\n", i, strings.Repeat("x", width-6))
	}
	return writeRaw(t, dir, name, []byte(b.String()))
}

func writeRaw(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0644); err != nil {
		t.Fatal(err)
	}
	return p
}

// truncateSparse 造一个"声明这么大、但不占磁盘"的文件：尺寸硬限制与 NUL 嗅探
// 都在读到这些字节之前判定，所以用例不需要真的读它们。
func truncateSparse(t *testing.T, dir, name string, size int64) string {
	t.Helper()
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(size); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}

// 预算的两个 seam：把上限调到 KB 级才能测到"到顶之后"的形状，
// 又不必在 CI 里造几百 KB / 500MB 的真文件。
func setReadContentBudget(t *testing.T, n int) {
	t.Helper()
	old := maxReadContentBytes
	maxReadContentBytes = n
	t.Cleanup(func() { maxReadContentBytes = old })
}

func setReadSizeCap(t *testing.T, n int64) {
	t.Helper()
	old := maxFileSizeForRead
	maxFileSizeForRead = n
	t.Cleanup(func() { maxFileSizeForRead = old })
}

func TestExecuteReadFile_ReturnsRequestedLineRange(t *testing.T) {
	dir := t.TempDir()
	writeNumberedFile(t, dir, "f.txt", 20)

	got := readFileOrFail(t, NewFileOperationsTool(dir), map[string]interface{}{
		"target_file": "f.txt",
		"start_line":  float64(5),
		"max_lines":   float64(4),
	})

	want := "00005\n00006\n00007\n00008"
	if content := readFieldString(t, got, "content"); content != want {
		t.Errorf("content = %q, want %q", content, want)
	}
	for key, want := range map[string]int64{
		"lines":              4,
		"total_lines":        20,
		"start_line":         5,
		"end_line":           8,
		"lines_before_range": 4,
		"lines_after_range":  12,
	} {
		if v := readFieldInt(t, got, key); v != want {
			t.Errorf("%s = %d, want %d", key, v, want)
		}
	}
	if readFieldBool(t, got, "truncated") {
		t.Error("读满整个区间不该置 truncated")
	}
	if _, ok := got["warning"]; ok {
		t.Errorf("未截断不该有 warning：%v", got["warning"])
	}
}

func TestExecuteReadFile_DefaultReadSpansMaxReadLines(t *testing.T) {
	dir := t.TempDir()
	writeNumberedFile(t, dir, "f.txt", 3000)

	got := readFileOrFail(t, NewFileOperationsTool(dir), map[string]interface{}{
		"target_file": "f.txt",
	})
	if v := readFieldInt(t, got, "end_line"); v != int64(maxReadLines) {
		t.Errorf("end_line = %d, want %d（缺省读到行数上限）", v, maxReadLines)
	}
	if v := readFieldInt(t, got, "lines"); v != int64(maxReadLines) {
		t.Errorf("lines = %d, want %d", v, maxReadLines)
	}
	if !readFieldBool(t, got, "truncated") {
		t.Error("3000 行只取回 1500 行必须置 truncated")
	}
}

func TestExecuteReadFile_MaxLinesIsClampedToCeiling(t *testing.T) {
	dir := t.TempDir()
	writeNumberedFile(t, dir, "f.txt", 3000)

	got := readFileOrFail(t, NewFileOperationsTool(dir), map[string]interface{}{
		"target_file": "f.txt",
		"start_line":  float64(1),
		"max_lines":   float64(999999),
	})
	if v := readFieldInt(t, got, "end_line"); v != int64(maxReadLines) {
		t.Errorf("max_lines 没钳住：end_line = %d, want %d", v, maxReadLines)
	}
}

// 夹具设计要点：2000 行 × 6B = 12KB，远在字节预算（50KB）之内，
// 所以这条断言测的是**行数**帽而不是字节帽。
func TestExecuteReadFile_LineCapBinds(t *testing.T) {
	dir := t.TempDir()
	writeNumberedFile(t, dir, "f.txt", 2000)

	got := readFileOrFail(t, NewFileOperationsTool(dir), map[string]interface{}{
		"target_file": "f.txt",
	})
	if v := readFieldInt(t, got, "lines"); v != int64(maxReadLines) {
		t.Errorf("lines = %d, want 行帽 %d", v, maxReadLines)
	}
	if v := int64(len(readFieldString(t, got, "content"))); v > int64(maxReadContentBytes) {
		t.Fatalf("夹具本不该碰字节帽，实际正文 %d > 预算 %d", v, maxReadContentBytes)
	}
	if v := readFieldInt(t, got, "total_lines"); v != 2000 {
		t.Errorf("total_lines = %d, want 2000", v)
	}
	if !readFieldBool(t, got, "truncated") {
		t.Error("超过行帽必须置 truncated")
	}
}

// 字节帽是**硬**上限：判定在 append 之后（旧写法在 append 之前，正文能超出整整一行）。
func TestExecuteReadFile_ByteCapIsHardCeiling(t *testing.T) {
	dir := t.TempDir()
	const width = 1000
	p := writeFixedWidthFile(t, dir, "wide.txt", 200, width) // 200KB，行帽 1500 碰不到

	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() <= int64(maxReadContentBytes) {
		t.Fatalf("夹具只有 %d 字节，没超过预算 %d —— 字节帽分支根本没被触发",
			info.Size(), maxReadContentBytes)
	}

	got := readFileOrFail(t, NewFileOperationsTool(dir), map[string]interface{}{
		"target_file": "wide.txt",
	})
	content := readFieldString(t, got, "content")
	if len(content) > maxReadContentBytes {
		t.Errorf("正文 %d 字节超过硬上限 %d —— 判定又回到 append 之前了", len(content), maxReadContentBytes)
	}
	if v := readFieldInt(t, got, "lines"); v >= 200 {
		t.Errorf("lines = %d，字节帽没起作用", v)
	}
	if !readFieldBool(t, got, "truncated") {
		t.Error("被字节帽截断必须置 truncated")
	}
}

// 首行就装满预算（minified bundle）：收下前缀，而不是返回一段空正文。
func TestExecuteReadFile_FirstLineOverBudgetKeepsPrefix(t *testing.T) {
	setReadContentBudget(t, 1024)
	dir := t.TempDir()
	writeRaw(t, dir, "one.txt", []byte(strings.Repeat("y", 5000)))

	got := readFileOrFail(t, NewFileOperationsTool(dir), map[string]interface{}{
		"target_file": "one.txt",
	})
	content := readFieldString(t, got, "content")
	if len(content) != 1024 {
		t.Errorf("content = %d 字节, want 预算前缀 1024", len(content))
	}
	if v := readFieldInt(t, got, "lines"); v != 1 {
		t.Errorf("lines = %d, want 1（首行的前缀也算一行，不能返回空）", v)
	}
	if v := readFieldInt(t, got, "total_lines"); v != 1 {
		t.Errorf("total_lines = %d, want 1", v)
	}
	if !readFieldBool(t, got, "truncated") {
		t.Error("行内被裁必须置 truncated，否则模型以为那一行就这么长")
	}
}

// 超长单行不再让整次读取失败（旧写法靠 Scanner 的 1MiB 缓冲，
// 一行超过它，即使只要第 1-10 行也直接返回 Go error）。
func TestExecuteReadFile_HugeSingleLineDoesNotFail(t *testing.T) {
	setReadContentBudget(t, 1024)
	dir := t.TempDir()
	// 第 1 行 5KB（超预算），第 2 行正常
	writeRaw(t, dir, "f.txt", []byte(strings.Repeat("z", 5000)+"\nsecond\n"))

	got := readFileOrFail(t, NewFileOperationsTool(dir), map[string]interface{}{
		"target_file": "f.txt",
		"start_line":  float64(1),
		"max_lines":   float64(10),
	})
	if v := readFieldInt(t, got, "lines"); v != 1 {
		t.Errorf("lines = %d, want 1", v)
	}
	if v := readFieldInt(t, got, "total_lines"); v != 2 {
		t.Errorf("total_lines = %d, want 2（超长行被裁也要照数行号）", v)
	}
}

func TestExecuteReadFile_TotalLinesExactWhenRangeIsSmall(t *testing.T) {
	dir := t.TempDir()
	writeNumberedFile(t, dir, "f.txt", 5000)

	got := readFileOrFail(t, NewFileOperationsTool(dir), map[string]interface{}{
		"target_file": "f.txt",
		"start_line":  float64(1),
		"max_lines":   float64(10),
	})
	if v := readFieldInt(t, got, "total_lines"); v != 5000 {
		t.Errorf("total_lines = %d, want 5000（只读前 10 行也要报准总行数）", v)
	}
	if v := readFieldInt(t, got, "lines"); v != 10 {
		t.Errorf("lines = %d, want 10", v)
	}
}

// 区间之后的部分靠 bytes.Count 尾扫，跨过 chunk 边界也不能漏。
func TestExecuteReadFile_TotalLinesExactAcrossChunkBoundary(t *testing.T) {
	dir := t.TempDir()
	// 行宽 700B，让 200 行 = 140KB 横跨多个 64KB chunk
	p := writeFixedWidthFile(t, dir, "f.txt", 200, 700)
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	total := int64(strings.Count(string(want), "\n"))

	got := readFileOrFail(t, NewFileOperationsTool(dir), map[string]interface{}{
		"target_file": "f.txt",
		"start_line":  float64(1),
		"max_lines":   float64(2),
	})
	if v := readFieldInt(t, got, "total_lines"); v != total {
		t.Errorf("total_lines = %d, want %d（尾扫跨 chunk 漏计）", v, total)
	}
}

func TestExecuteReadFile_PaginationHintOnTruncation(t *testing.T) {
	dir := t.TempDir()
	writeNumberedFile(t, dir, "f.txt", 2000)

	got := readFileOrFail(t, NewFileOperationsTool(dir), map[string]interface{}{
		"target_file": "f.txt",
	})
	w := readFieldString(t, got, "warning")
	want := fmt.Sprintf("start_line=%d", maxReadLines+1)
	if !strings.Contains(w, want) {
		t.Errorf("warning 没给出确切的下一步 %q，实际：%q", want, w)
	}
	if !strings.Contains(w, "file has 2000 lines") {
		t.Errorf("warning 没报准总行数：%q", w)
	}
}

func TestExecuteReadFile_StartBeyondEOFIsErrorPayload(t *testing.T) {
	dir := t.TempDir()
	writeNumberedFile(t, dir, "f.txt", 10)

	got := readFileOrFail(t, NewFileOperationsTool(dir), map[string]interface{}{
		"target_file": "f.txt",
		"start_line":  float64(500),
		"max_lines":   float64(10),
	})
	msg := readErrorPayload(t, got)
	if !strings.Contains(msg, "exceeds file total lines") {
		t.Errorf("error 文案没点名越界: %q", msg)
	}
	if v := readFieldInt(t, got, "total_lines"); v != 10 {
		t.Errorf("total_lines = %d, want 10", v)
	}
}

func TestExecuteReadFile_PreservesCRLF(t *testing.T) {
	dir := t.TempDir()
	writeRaw(t, dir, "crlf.txt", []byte("alpha\r\nbravo\r\ncharlie\r\n"))

	got := readFileOrFail(t, NewFileOperationsTool(dir), map[string]interface{}{
		"target_file": "crlf.txt",
	})
	// 只按 \n 切、\r 留在行内容里；正文末尾不带换行（与旧写法一致）。
	want := "alpha\r\nbravo\r\ncharlie\r"
	if c := readFieldString(t, got, "content"); c != want {
		t.Errorf("content = %q, want %q（行尾必须字节保真，否则照抄进 search_replace_in_file 匹配不上）", c, want)
	}
	if v := readFieldInt(t, got, "total_lines"); v != 3 {
		t.Errorf("total_lines = %d, want 3", v)
	}
}

func TestExecuteReadFile_BinaryRefusedWithSuggestion(t *testing.T) {
	dir := t.TempDir()
	// ELF 头：第 8 个字节就是 NUL（PNG 签名反而没有 NUL，用它当夹具会测不到嗅探分支）
	writeRaw(t, dir, "blob.bin", append([]byte{0x7f, 'E', 'L', 'F', 0x02, 0x01, 0x01, 0x00}, []byte("junk")...))

	got := readFileOrFail(t, NewFileOperationsTool(dir), map[string]interface{}{
		"target_file": "blob.bin",
	})
	msg := readErrorPayload(t, got)
	if !strings.Contains(msg, "binary") {
		t.Errorf("含 NUL 的文件必须被拒，实际：%q", msg)
	}
	if v, ok := got["suggestion"]; !ok || v == "" {
		t.Error("二进制拒读要给 suggestion，不然模型只能继续猜")
	}
}

// 零填充的稀疏文件（前 8KB 全是 NUL）也走二进制拒读：
// 否则一条 truncate 出来的 11MB 洞会被当成"一行超长文本"扫完。
func TestExecuteReadFile_SparseZerosRefusedAsBinary(t *testing.T) {
	dir := t.TempDir()
	big := truncateSparse(t, dir, "hole.bin", 11*1024*1024)

	got := readFileOrFail(t, NewFileOperationsTool(dir), map[string]interface{}{
		"target_file": filepath.Base(big),
	})
	if msg := readErrorPayload(t, got); !strings.Contains(msg, "binary") {
		t.Errorf("稀疏零文件应走二进制拒读，实际：%q", msg)
	}
}

// 非法 UTF-8 只警告、不拒读：latin-1 之类的文本文件是合法的，一刀切会误伤。
func TestExecuteReadFile_InvalidUTF8WarnsButReturns(t *testing.T) {
	dir := t.TempDir()
	writeRaw(t, dir, "latin.txt", []byte("caf\xe9 au lait\n"))

	got := readFileOrFail(t, NewFileOperationsTool(dir), map[string]interface{}{
		"target_file": "latin.txt",
	})
	if c := readFieldString(t, got, "content"); !strings.Contains(c, "caf") {
		t.Errorf("非法 UTF-8 也要把正文交回去（只 warn），实际 content = %q", c)
	}
	w := readFieldString(t, got, "warning")
	if !strings.Contains(w, "UTF-8") {
		t.Errorf("应给 mojibake 警告，实际 warning = %q", w)
	}
}

func TestExecuteReadFile_FileAboveSizeCapRefused(t *testing.T) {
	setReadSizeCap(t, 1024)
	dir := t.TempDir()
	writeNumberedFile(t, dir, "f.txt", 400) // 2.4KB > seam 的 1KB

	got := readFileOrFail(t, NewFileOperationsTool(dir), map[string]interface{}{
		"target_file": "f.txt",
	})
	if msg := readErrorPayload(t, got); !strings.Contains(msg, "absolute maximum") {
		t.Errorf("超过尺寸硬限必须走拒绝分支，实际：%q", msg)
	}
}

// 生产默认值本身也要有人钉住：常量打错一个数量级时这条会红。
func TestExecuteReadFile_DefaultSizeCapRefuses500MB(t *testing.T) {
	dir := t.TempDir()
	huge := truncateSparse(t, dir, "huge.bin", maxFileSizeForRead+1)

	got := readFileOrFail(t, NewFileOperationsTool(dir), map[string]interface{}{
		"target_file": filepath.Base(huge),
		"start_line":  float64(1),
	})
	if msg := readErrorPayload(t, got); !strings.Contains(msg, "absolute maximum") {
		t.Errorf("超过 500MB 必须走拒绝分支，实际：%q", msg)
	}
	if v := readFieldInt(t, got, "file_size_bytes"); v <= maxFileSizeForRead {
		t.Errorf("file_size_bytes = %d, 应大于 %d", v, maxFileSizeForRead)
	}
}

func TestExecuteReadFile_EmptyFileWarns(t *testing.T) {
	dir := t.TempDir()
	writeRaw(t, dir, "empty.txt", nil)

	got := readFileOrFail(t, NewFileOperationsTool(dir), map[string]interface{}{
		"target_file": "empty.txt",
	})
	if v := readFieldInt(t, got, "total_lines"); v != 0 {
		t.Errorf("total_lines = %d, want 0", v)
	}
	if w := readFieldString(t, got, "warning"); !strings.Contains(w, "empty") {
		t.Errorf("空文件必须给警告，实际 %q", w)
	}
}

func TestExecuteReadFile_LastLineWithoutNewlineCounts(t *testing.T) {
	dir := t.TempDir()
	writeRaw(t, dir, "f.txt", []byte("a\nb"))

	got := readFileOrFail(t, NewFileOperationsTool(dir), map[string]interface{}{
		"target_file": "f.txt",
	})
	if v := readFieldInt(t, got, "total_lines"); v != 2 {
		t.Errorf("total_lines = %d, want 2（末尾没有换行也算一行）", v)
	}
	if c := readFieldString(t, got, "content"); c != "a\nb" {
		t.Errorf("content = %q, want %q", c, "a\nb")
	}
}

func TestExecuteReadFile_RelativePathResolvesToWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	writeRaw(t, dir, filepath.Join("sub", "a.txt"), []byte("hello\n"))

	got := readFileOrFail(t, NewFileOperationsTool(dir), map[string]interface{}{
		"target_file": "sub/a.txt",
	})
	if c := readFieldString(t, got, "content"); c != "hello" {
		t.Errorf("content = %q, want %q", c, "hello")
	}
}

func TestExecuteReadFile_DirectoryReturnsGoError(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileOperationsTool(dir).ExecuteReadFile(context.Background(), map[string]interface{}{
		"target_file": "sub",
	}); err == nil {
		t.Error("读目录必须返回 Go error，而不是空回执")
	}
}

func TestExecuteReadFile_MissingTargetFileReturnsGoError(t *testing.T) {
	dir := t.TempDir()
	if _, err := NewFileOperationsTool(dir).ExecuteReadFile(context.Background(), map[string]interface{}{
		"start_line": float64(1),
	}); err == nil {
		t.Error("缺 target_file 必须返回 Go error")
	}
}

// 预算按"整文件一次给满"算：本仓最大的源文件（实测 42.2KB）必须一次读完。
// 这条钉的是 50KB 这个数字为什么是 50KB —— 调小到 10KB 它就该红。
func TestExecuteReadFile_ReadsLargestRepoFileInOneCall(t *testing.T) {
	dir := t.TempDir()
	body := strings.Repeat("x", 42*1024) + "\n"
	writeRaw(t, dir, "big.go", []byte(body))

	got := readFileOrFail(t, NewFileOperationsTool(dir), map[string]interface{}{
		"target_file": "big.go",
	})
	if readFieldBool(t, got, "truncated") {
		t.Errorf("42KB 的文件不该被截断（预算 %d 字节）", maxReadContentBytes)
	}
	if v := readFieldInt(t, got, "total_lines"); v != readFieldInt(t, got, "lines") {
		t.Errorf("total_lines=%d 而 lines=%d，一次没读完", v, readFieldInt(t, got, "lines"))
	}
}
