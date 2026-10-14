package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireRg 把"这套测试依赖 ripgrep 二进制"这件事说明白：
// 内嵌的 dist/bin/rg 解不出来、系统 PATH 里也没有 rg 时直接跳过并给出原因，
// 而不是让每个用例都败在一句 "exec: rg: executable file not found" 上。
func requireRg(t *testing.T) {
	t.Helper()
	path := rgPath()
	if filepath.IsAbs(path) {
		if _, err := os.Stat(path); err == nil {
			return
		}
	}
	if _, err := exec.LookPath(path); err != nil {
		t.Skipf("需要 ripgrep（内嵌 dist/bin 或 $PATH 里的 rg）：找不到 %q，跳过 grep 搜索测试", path)
	}
}

func TestExecuteGrepSearch(t *testing.T) {
	requireRg(t)

	cases := []struct {
		name string
		// files 是相对工作区根的路径 → 内容，父目录会自动创建。
		files map[string]string
		// query 原样传给 `rg -e`，所以正则用例不需要额外开关。
		query     string
		searchDir string
		wantCount int
		// wantContains 的每一项都必须出现在某一条匹配里（文件名、行号或子目录路径）。
		wantContains []string
	}{
		{
			name:         "basic match",
			files:        map[string]string{"test.txt": "hello world\nthis is a test"},
			query:        "hello",
			wantCount:    1,
			wantContains: []string{"test.txt", ":1:"},
		},
		{
			name: "multiple files",
			files: map[string]string{
				"file1.go": "package main\n\nfunc Hello() {\n\tprintln(\"hello\")\n}\n",
				"file2.go": "package main\n\nfunc Hello() {\n\tprintln(\"hello\")\n}\n",
				"file3.go": "package main\n\nfunc Hello() {\n\tprintln(\"hello\")\n}\n",
			},
			query:        "Hello",
			wantCount:    3,
			wantContains: []string{"file1.go", "file2.go", "file3.go"},
		},
		{
			name:         "multiple lines",
			files:        map[string]string{"log.txt": "line 1: error occurred\nline 2: normal text\nline 3: error found\nline 4: another error\n"},
			query:        "error",
			wantCount:    3,
			wantContains: []string{"log.txt", ":1:", ":3:", ":4:"},
		},
		{
			name:         "no match",
			files:        map[string]string{"test.txt": "hello world"},
			query:        "xyz_not_exist_pattern",
			wantCount:    0,
			wantContains: nil,
		},
		{
			name:         "regexp case class",
			files:        map[string]string{"greetings.txt": "Hello world\nhello universe\nHELLO galaxy\nnormal line\n"},
			query:        "[Hh]ello",
			wantCount:    2,
			wantContains: []string{"greetings.txt"},
		},
		{
			name: "regexp digits",
			files: map[string]string{
				"greetings.txt": "Hello world\nhello universe\nHELLO galaxy\nnormal line\n",
				"numbers.txt":   "item 123\nitem 456\nitem abc\n",
			},
			query:        `\d+`,
			wantCount:    2,
			wantContains: []string{"numbers.txt"},
		},
		{
			name:         "search subdirectory by relative path",
			files:        map[string]string{"src/main.go": "func main() {\n\tprintln(\"hello\")\n}"},
			query:        "main",
			searchDir:    "src",
			wantCount:    1,
			wantContains: []string{"src" + string(filepath.Separator), "main.go"},
		},
		{
			name: "special characters in filenames",
			files: map[string]string{
				"file with spaces.txt":  "hello spaces\n",
				"文件中文.txt":              "hello chinese\n",
				"special-chars_v1.0.go": "hello special chars\n",
			},
			query:        "hello",
			wantCount:    3,
			wantContains: []string{"file with spaces.txt", "文件中文.txt", "special-chars_v1.0.go"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			for rel, content := range tc.files {
				fpath := filepath.Join(tmpDir, rel)
				if err := os.MkdirAll(filepath.Dir(fpath), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(fpath, []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			}

			tool := NewSearchOperationsTool(tmpDir)
			params := map[string]interface{}{"query": tc.query}
			if tc.searchDir != "" {
				params["search_directory"] = tc.searchDir
			}

			result, err := tool.ExecuteGrepSearch(context.Background(), params)
			if err != nil {
				t.Fatalf("Expected no error, got %v", err)
			}
			matches := decodeGrepResult(t, result, tc.wantCount)

			joined := strings.Join(matches, "\n")
			for _, want := range tc.wantContains {
				if !strings.Contains(joined, want) {
					t.Errorf("expected a match containing %q, got:\n%s", want, joined)
				}
			}
		})
	}
}

// decodeGrepResult 把 {matches []string, count int} 的契约一次性校验完：
// 类型对不上、或者 count 与 len(matches) 不一致，任何用例都不该走到后面的内容断言。
func decodeGrepResult(t *testing.T, result interface{}, wantCount int) []string {
	t.Helper()

	resMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected map result, got %T", result)
	}
	count, ok := resMap["count"].(int)
	if !ok {
		t.Fatalf("Expected count to be int, got %T", resMap["count"])
	}
	if count != wantCount {
		t.Errorf("Expected count=%d, got %d", wantCount, count)
	}
	matches, ok := resMap["matches"].([]string)
	if !ok {
		t.Fatalf("Expected matches to be []string, got %T", resMap["matches"])
	}
	if len(matches) != count {
		t.Fatalf("count=%d 与 len(matches)=%d 不一致", count, len(matches))
	}
	return matches
}
