package tools

import (
	"context"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecuteGrepSearch_BasicMatch(t *testing.T) {
	// 创建临时目录
	tmpDir, err := ioutil.TempDir("", "test_grep_basic")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// 创建包含 "hello world" 的文件
	testFile := filepath.Join(tmpDir, "test.txt")
	if err := ioutil.WriteFile(testFile, []byte("hello world\nthis is a test"), 0644); err != nil {
		t.Fatal(err)
	}

	// 创建工具实例
	tool := NewSearchOperationsTool(tmpDir)
	ctx := context.Background()

	// 搜索 "hello"
	params := map[string]interface{}{
		"query": "hello",
	}

	result, err := tool.ExecuteGrepSearch(ctx, params)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	// 验证返回结果是 map[string]interface{}
	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected map result, got %T", result)
	}

	// 验证 count 字段
	count, ok := resMap["count"].(int)
	if !ok {
		t.Fatalf("Expected count to be int, got %T", resMap["count"])
	}
	if count != 1 {
		t.Errorf("Expected count=1, got %d", count)
	}

	// 验证 matches 字段
	matches, ok := resMap["matches"].([]string)
	if !ok {
		t.Fatalf("Expected matches to be []string, got %T", resMap["matches"])
	}
	if len(matches) != 1 {
		t.Fatalf("Expected 1 match, got %d", len(matches))
	}

	// 验证匹配结果包含文件名和行号
	if !strings.Contains(matches[0], "test.txt") {
		t.Errorf("Expected match to contain filename 'test.txt', got %q", matches[0])
	}
	if !strings.Contains(matches[0], ":1:") {
		t.Errorf("Expected match to contain line number ':1:', got %q", matches[0])
	}
}

func TestExecuteGrepSearch_MultipleFiles(t *testing.T) {
	// 创建临时目录
	tmpDir, err := ioutil.TempDir("", "test_grep_multi_files")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// 创建多个文件包含相同模式
	files := []string{"file1.go", "file2.go", "file3.go"}
	for _, fname := range files {
		content := "package main\n\nfunc Hello() {\n\tprintln(\"hello\")\n}\n"
		if err := ioutil.WriteFile(filepath.Join(tmpDir, fname), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	tool := NewSearchOperationsTool(tmpDir)
	ctx := context.Background()

	params := map[string]interface{}{
		"query": "Hello",
	}

	result, err := tool.ExecuteGrepSearch(ctx, params)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected map result, got %T", result)
	}

	count, ok := resMap["count"].(int)
	if !ok {
		t.Fatalf("Expected count to be int, got %T", resMap["count"])
	}

	// 应该有3个匹配，每个文件一行
	if count != 3 {
		t.Errorf("Expected count=3, got %d", count)
	}

	matches, ok := resMap["matches"].([]string)
	if !ok {
		t.Fatalf("Expected matches to be []string, got %T", resMap["matches"])
	}
	if len(matches) != 3 {
		t.Fatalf("Expected 3 matches, got %d", len(matches))
	}

	// 验证所有文件名都包含在结果中
	foundFiles := make(map[string]bool)
	for _, match := range matches {
		for _, fname := range files {
			if strings.Contains(match, fname) {
				foundFiles[fname] = true
			}
		}
	}
	for _, fname := range files {
		if !foundFiles[fname] {
			t.Errorf("Expected to find %q in matches", fname)
		}
	}
}

func TestExecuteGrepSearch_MultipleLines(t *testing.T) {
	// 创建临时目录
	tmpDir, err := ioutil.TempDir("", "test_grep_multi_lines")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// 创建一个文件在多个行包含同一模式
	content := "line 1: error occurred\nline 2: normal text\nline 3: error found\nline 4: another error\n"
	testFile := filepath.Join(tmpDir, "log.txt")
	if err := ioutil.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewSearchOperationsTool(tmpDir)
	ctx := context.Background()

	params := map[string]interface{}{
		"query": "error",
	}

	result, err := tool.ExecuteGrepSearch(ctx, params)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected map result, got %T", result)
	}

	count, ok := resMap["count"].(int)
	if !ok {
		t.Fatalf("Expected count to be int, got %T", resMap["count"])
	}

	// 应该匹配3行
	if count != 3 {
		t.Errorf("Expected count=3, got %d", count)
	}

	matches, ok := resMap["matches"].([]string)
	if !ok {
		t.Fatalf("Expected matches to be []string, got %T", resMap["matches"])
	}
	if len(matches) != 3 {
		t.Fatalf("Expected 3 matches, got %d", len(matches))
	}

	// 验证行号正确
	expectedLines := []string{"1", "3", "4"}
	for i, expected := range expectedLines {
		if !strings.Contains(matches[i], ":"+expected+":") {
			t.Errorf("Expected match %d to contain line number %q, got %q", i, expected, matches[i])
		}
	}
}

func TestExecuteGrepSearch_NoMatch(t *testing.T) {
	// 创建临时目录
	tmpDir, err := ioutil.TempDir("", "test_grep_no_match")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// 创建文件
	if err := ioutil.WriteFile(filepath.Join(tmpDir, "test.txt"), []byte("hello world"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewSearchOperationsTool(tmpDir)
	ctx := context.Background()

	params := map[string]interface{}{
		"query": "xyz_not_exist_pattern",
	}

	result, err := tool.ExecuteGrepSearch(ctx, params)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected map result, got %T", result)
	}

	// 验证 count 为 0
	count, ok := resMap["count"].(int)
	if !ok {
		t.Fatalf("Expected count to be int, got %T", resMap["count"])
	}
	if count != 0 {
		t.Errorf("Expected count=0, got %d", count)
	}

	// 验证 matches 为空数组
	matches, ok := resMap["matches"].([]string)
	if !ok {
		t.Fatalf("Expected matches to be []string, got %T", resMap["matches"])
	}
	if len(matches) != 0 {
		t.Errorf("Expected empty matches, got %d items", len(matches))
	}
}

func TestExecuteGrepSearch_Regexp(t *testing.T) {
	// 创建临时目录
	tmpDir, err := ioutil.TempDir("", "test_grep_regexp")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// 创建包含不同大小写模式的文件
	content := "Hello world\nhello universe\nHELLO galaxy\nnormal line\n"
	testFile := filepath.Join(tmpDir, "greetings.txt")
	if err := ioutil.WriteFile(testFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewSearchOperationsTool(tmpDir)
	ctx := context.Background()

	// 测试大小写敏感的正则表达式 [Hh]ello
	params := map[string]interface{}{
		"query": "[Hh]ello",
	}

	result, err := tool.ExecuteGrepSearch(ctx, params)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected map result, got %T", result)
	}

	count, ok := resMap["count"].(int)
	if !ok {
		t.Fatalf("Expected count to be int, got %T", resMap["count"])
	}

	// [Hh]ello 应该匹配 "Hello" 和 "hello"，共2行
	if count != 2 {
		t.Errorf("Expected count=2 for [Hh]ello, got %d", count)
	}

	// 测试数字模式 \d+
	contentNums := "item 123\nitem 456\nitem abc\n"
	testFileNum := filepath.Join(tmpDir, "numbers.txt")
	if err := ioutil.WriteFile(testFileNum, []byte(contentNums), 0644); err != nil {
		t.Fatal(err)
	}

	paramsNum := map[string]interface{}{
		"query": "\\d+",
	}

	resultNum, err := tool.ExecuteGrepSearch(ctx, paramsNum)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	resMapNum, ok := resultNum.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected map result, got %T", resultNum)
	}

	countNum, ok := resMapNum["count"].(int)
	if !ok {
		t.Fatalf("Expected count to be int, got %T", resMapNum["count"])
	}

	// \d+ 应该匹配两行数字
	if countNum != 2 {
		t.Errorf("Expected count=2 for \\d+, got %d", countNum)
	}
}

func TestExecuteGrepSearch_SearchDirectory(t *testing.T) {
	// 创建临时目录
	baseDir, err := ioutil.TempDir("", "test_grep_searchdir")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(baseDir)

	// 创建子目录
	subDir := filepath.Join(baseDir, "src")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 在子目录中创建文件
	testFile := filepath.Join(subDir, "main.go")
	if err := ioutil.WriteFile(testFile, []byte("func main() {\n\tprintln(\"hello\")\n}"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewSearchOperationsTool(baseDir)
	ctx := context.Background()

	// 使用相对路径搜索子目录
	params := map[string]interface{}{
		"query":           "main",
		"search_directory": "src",
	}

	result, err := tool.ExecuteGrepSearch(ctx, params)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected map result, got %T", result)
	}

	count, ok := resMap["count"].(int)
	if !ok {
		t.Fatalf("Expected count to be int, got %T", resMap["count"])
	}

	if count != 1 {
		t.Errorf("Expected count=1, got %d", count)
	}

	matches, ok := resMap["matches"].([]string)
	if !ok {
		t.Fatalf("Expected matches to be []string, got %T", resMap["matches"])
	}

	// 验证结果包含子目录路径
	if !strings.Contains(matches[0], "src"+string(filepath.Separator)) {
		t.Errorf("Expected match to contain 'src/' path, got %q", matches[0])
	}
}

func TestExecuteGrepSearch_RelativePath(t *testing.T) {
	// 创建临时目录
	baseDir, err := ioutil.TempDir("", "test_grep_relative")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(baseDir)

	// 创建子目录结构
	subDir := filepath.Join(baseDir, "subdir", "nested")
	if err := os.MkdirAll(subDir, 0755); err != nil {
		t.Fatal(err)
	}

	// 在子目录中创建文件
	testFile := filepath.Join(subDir, "test.go")
	if err := ioutil.WriteFile(testFile, []byte("package nested\n\nconst Value = 42"), 0644); err != nil {
		t.Fatal(err)
	}

	tool := NewSearchOperationsTool(baseDir)
	ctx := context.Background()

	// 使用相对路径搜索
	params := map[string]interface{}{
		"query":           "nested",
		"search_directory": "subdir",
	}

	result, err := tool.ExecuteGrepSearch(ctx, params)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected map result, got %T", result)
	}

	count, ok := resMap["count"].(int)
	if !ok {
		t.Fatalf("Expected count to be int, got %T", resMap["count"])
	}

	// 应该匹配到文件内容和目录名
	if count == 0 {
		t.Error("Expected at least one match, got 0")
	}

	matches, ok := resMap["matches"].([]string)
	if !ok {
		t.Fatalf("Expected matches to be []string, got %T", resMap["matches"])
	}

	// 验证结果包含相对路径
	foundNested := false
	for _, match := range matches {
		if strings.Contains(match, "nested") {
			foundNested = true
			break
		}
	}
	if !foundNested {
		t.Error("Expected to find 'nested' in matches")
	}
}

func TestExecuteGrepSearch_SpecialChars(t *testing.T) {
	// 创建临时目录
	tmpDir, err := ioutil.TempDir("", "test_grep_special")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// 创建包含特殊字符文件名的文件
	specialFiles := map[string]string{
		"file with spaces.txt":  "hello spaces\n",
		"文件中文.txt":          "hello chinese\n",
		"special-chars_v1.0.go": "hello special chars\n",
	}

	for fname, content := range specialFiles {
		fpath := filepath.Join(tmpDir, fname)
		if err := ioutil.WriteFile(fpath, []byte(content), 0644); err != nil {
			t.Fatalf("Failed to create file %q: %v", fname, err)
		}
	}

	tool := NewSearchOperationsTool(tmpDir)
	ctx := context.Background()

	params := map[string]interface{}{
		"query": "hello",
	}

	result, err := tool.ExecuteGrepSearch(ctx, params)
	if err != nil {
		t.Fatalf("Expected no error, got %v", err)
	}

	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected map result, got %T", result)
	}

	count, ok := resMap["count"].(int)
	if !ok {
		t.Fatalf("Expected count to be int, got %T", resMap["count"])
	}

	// 应该匹配到3个文件（每个文件一行）
	if count != 3 {
		t.Errorf("Expected count=3, got %d", count)
	}

	matches, ok := resMap["matches"].([]string)
	if !ok {
		t.Fatalf("Expected matches to be []string, got %T", resMap["matches"])
	}

	// 验证所有特殊字符文件名都在结果中
	expectedFiles := []string{
		"file with spaces.txt",
		"文件中文.txt",
		"special-chars_v1.0.go",
	}

	foundFiles := make(map[string]bool)
	for _, match := range matches {
		for _, ef := range expectedFiles {
			if strings.Contains(match, ef) {
				foundFiles[ef] = true
			}
		}
	}

	for _, ef := range expectedFiles {
		if !foundFiles[ef] {
			t.Errorf("Expected to find special file %q in matches", ef)
		}
	}
}
