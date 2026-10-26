package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// read_file 的性能探针（方案 §6.3）。同一条探针在改动前后各跑一次：
// 夹具尺寸固定，参数按各自的契约写（改动前是 range 读，改动后是 max_lines）。
// 只记录，不做门禁 —— 计时断言在这台机器上会假红。

func benchFixture(b *testing.B, lines, width int) string {
	b.Helper()
	dir, err := os.MkdirTemp("", "readfile-bench")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { os.RemoveAll(dir) })

	var sb strings.Builder
	sb.Grow(lines * (width + 1))
	for i := 1; i <= lines; i++ {
		fmt.Fprintf(&sb, "%05d %s\n", i, strings.Repeat("x", width-6))
	}
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte(sb.String()), 0644); err != nil {
		b.Fatal(err)
	}
	return p
}

// benchRead 把工具与参数压成一行，两个版本各自只改 params 的字面量。
func benchRead(b *testing.B, tool *FileOperationsTool, params map[string]interface{}) {
	b.Helper()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := tool.ExecuteReadFile(context.Background(), params); err != nil {
			b.Fatal(err)
		}
	}
}

// 本仓 p50 源文件：5.6KB / 300 行 —— 改动后应当一次读完。
func BenchmarkReadFile_P50File(b *testing.B) {
	p := benchFixture(b, 300, 19)
	tool := NewFileOperationsTool(filepath.Dir(p))
	rel := filepath.Base(p)
	benchRead(b, tool, map[string]interface{}{
		"target_file":             rel,
		"should_read_entire_file": true,
	})
}

// 本仓 max：42KB / 1400 行 —— 改动前 10KB 帽会截断，改动后一次读完。
func BenchmarkReadFile_LargestFile(b *testing.B) {
	p := benchFixture(b, 1400, 30)
	tool := NewFileOperationsTool(filepath.Dir(p))
	rel := filepath.Base(p)
	benchRead(b, tool, map[string]interface{}{
		"target_file":             rel,
		"should_read_entire_file": true,
	})
}

// 20MB 日志只读前 1000 行：这条钉的是"收满之后还要不要逐行扫描"。
func BenchmarkReadFile_WindowOfHugeFile(b *testing.B) {
	p := benchFixture(b, 300000, 70) // ~21MB
	tool := NewFileOperationsTool(filepath.Dir(p))
	rel := filepath.Base(p)
	benchRead(b, tool, map[string]interface{}{
		"target_file":             rel,
		"should_read_entire_file": false,
		"start_line_one_indexed":  float64(1),
		"end_line_one_indexed":    float64(1000),
	})
}
