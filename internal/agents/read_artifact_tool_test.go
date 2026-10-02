package agents

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"yagent/internal/artifact"
)

// saveTestArtifact 测试辅助：隔离 artifact 根目录并落盘 fullText，返回引用。
func saveTestArtifact(t *testing.T, fullText string) artifact.Ref {
	t.Helper()
	t.Setenv("YAGENT_ARTIFACT_ROOT", t.TempDir())
	ref, err := artifact.DefaultStore().Save("default", "coding", "test task", "", fullText)
	if err != nil {
		t.Fatalf("artifact Save failed: %v", err)
	}
	return ref
}

// callReadArtifact 测试辅助：直接调用 handler（同包访问，绕过 Adapter.Call 的 JSON 层）。
func callReadArtifact(t *testing.T, params map[string]interface{}) string {
	t.Helper()
	out, err := executeReadArtifact(context.Background(), params)
	if err != nil {
		t.Fatalf("executeReadArtifact returned error: %v", err)
	}
	s, ok := out.(string)
	if !ok {
		t.Fatalf("executeReadArtifact returned %T, want string", out)
	}
	return s
}

// multLines 构造 n 行文本 "line 1".."line n"（\n 连接，无尾随换行）。
func multLines(n int) string {
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	return strings.Join(lines, "\n")
}

// TestReadArtifactFullRead 全量读取：头部正确、50 行全带行号前缀、无 more 提示。
func TestReadArtifactFullRead(t *testing.T) {
	ref := saveTestArtifact(t, multLines(50))
	out := callReadArtifact(t, map[string]interface{}{"id": ref.ID})

	wantHeader := fmt.Sprintf("artifact %s | agent: coding | 50 lines | showing 1-50", ref.ID)
	if !strings.HasPrefix(out, wantHeader) {
		t.Errorf("header mismatch:\nwant prefix: %q\ngot:\n%s", wantHeader, out)
	}
	for i := 1; i <= 50; i++ {
		// 行号宽度右对齐（total=50 → 宽度 2）。
		if !strings.Contains(out, fmt.Sprintf("%2d: line %d", i, i)) {
			t.Errorf("line %d with number prefix missing from output", i)
		}
	}
	if strings.Contains(out, "[more:") {
		t.Errorf("unexpected [more: hint in full read:\n%s", out)
	}
}

// TestReadArtifactPaging 分页：showing 10-29、more 提示含 start_line=30。
func TestReadArtifactPaging(t *testing.T) {
	ref := saveTestArtifact(t, multLines(50))
	out := callReadArtifact(t, map[string]interface{}{
		"id":         ref.ID,
		"start_line": float64(10), // LLM 经 JSON 调用：数字反序列化为 float64
		"max_lines":  float64(20),
	})

	wantHeader := fmt.Sprintf("artifact %s | agent: coding | 50 lines | showing 10-29", ref.ID)
	if !strings.HasPrefix(out, wantHeader) {
		t.Errorf("header mismatch:\nwant prefix: %q\ngot:\n%s", wantHeader, out)
	}
	for i := 10; i <= 29; i++ {
		if !strings.Contains(out, fmt.Sprintf("%2d: line %d", i, i)) {
			t.Errorf("line %d missing from paged output", i)
		}
	}
	if strings.Contains(out, " 9: line 9\n") || strings.Contains(out, "30: line 30\n") {
		t.Errorf("lines outside [10,29] leaked into output")
	}
	wantMore := "[more: 21 lines left; continue with start_line=30]"
	if !strings.Contains(out, wantMore) {
		t.Errorf("more hint mismatch: want %q in output:\n%s", wantMore, out)
	}
}

// TestReadArtifactStartLineOutOfRange start_line 越界（超总行数）：空正文 + 友好提示，不 panic。
func TestReadArtifactStartLineOutOfRange(t *testing.T) {
	ref := saveTestArtifact(t, multLines(50))
	out := callReadArtifact(t, map[string]interface{}{
		"id":         ref.ID,
		"start_line": float64(60),
	})

	if strings.Contains(out, "60: line 60") {
		t.Errorf("unexpected content for out-of-range start_line:\n%s", out)
	}
	if !strings.Contains(out, "exceeds total lines") {
		t.Errorf("friendly hint missing:\n%s", out)
	}
	if !strings.Contains(out, "50 lines") {
		t.Errorf("total lines info missing:\n%s", out)
	}
}

// TestReadArtifactMaxLinesClamp max_lines=1000 超硬上限 500：被钳制。
func TestReadArtifactMaxLinesClamp(t *testing.T) {
	ref := saveTestArtifact(t, multLines(600))
	out := callReadArtifact(t, map[string]interface{}{
		"id":        ref.ID,
		"max_lines": float64(1000),
	})

	wantHeader := fmt.Sprintf("artifact %s | agent: coding | 600 lines | showing 1-500", ref.ID)
	if !strings.HasPrefix(out, wantHeader) {
		t.Errorf("header mismatch (clamp to 500 expected):\nwant: %q\ngot:\n%s", wantHeader, out)
	}
	wantMore := "[more: 100 lines left; continue with start_line=501]"
	if !strings.Contains(out, wantMore) {
		t.Errorf("more hint mismatch: want %q in output", wantMore)
	}
}

// TestReadArtifactInvalidID 非法 ID（路径穿越、空串）：返回 invalid id 文本 + 纠正提示。
func TestReadArtifactInvalidID(t *testing.T) {
	_ = saveTestArtifact(t, multLines(5)) // 仅隔离根目录

	for _, id := range []string{"../etc/passwd", ""} {
		out := callReadArtifact(t, map[string]interface{}{"id": id})
		if !strings.Contains(out, "invalid artifact id") {
			t.Errorf("id %q: want invalid id text, got:\n%s", id, out)
		}
		if !strings.Contains(out, "请核对委派结果头部中的 artifact id") {
			t.Errorf("id %q: correction hint missing:\n%s", id, out)
		}
	}
}

// TestReadArtifactNotFound 合法格式但不存在的 ID：返回 not found 文本。
func TestReadArtifactNotFound(t *testing.T) {
	_ = saveTestArtifact(t, multLines(5)) // 仅隔离根目录

	out := callReadArtifact(t, map[string]interface{}{"id": "20260101-000000-deadbeef"})
	if !strings.Contains(out, "artifact not found") {
		t.Errorf("want not found text, got:\n%s", out)
	}
}

// TestReadArtifactLongLine 长单行（10000 rune）：被截断展示、总返回长度受控。
func TestReadArtifactLongLine(t *testing.T) {
	long := strings.Repeat("x", 10000)
	full := "short head\n" + long + "\nshort tail"
	ref := saveTestArtifact(t, full)

	out := callReadArtifact(t, map[string]interface{}{"id": ref.ID})

	if !strings.Contains(out, fmt.Sprintf("[line 2 truncated, %d runes total]", len([]rune(long)))) {
		t.Errorf("truncation marker missing:\n%s", out)
	}
	// 总返回长度受控：头部 + 3 行（含截断标注），远小于全文 10000+ rune。
	if runes := len([]rune(out)); runes > 5000 {
		t.Errorf("output too long: %d runes (want <= 5000)", runes)
	}
	// 短行不受影响。
	if !strings.Contains(out, "1: short head") || !strings.Contains(out, "3: short tail") {
		t.Errorf("short lines corrupted:\n%s", out)
	}
}

// TestReadArtifactIntParams 兼容 int 直传（非 JSON 程序化调用路径的稳健性）。
func TestReadArtifactIntParams(t *testing.T) {
	ref := saveTestArtifact(t, multLines(50))
	out := callReadArtifact(t, map[string]interface{}{
		"id":         ref.ID,
		"start_line": 10,
		"max_lines":  20,
	})
	wantHeader := fmt.Sprintf("artifact %s | agent: coding | 50 lines | showing 10-29", ref.ID)
	if !strings.HasPrefix(out, wantHeader) {
		t.Errorf("int params not handled:\nwant prefix: %q\ngot:\n%s", wantHeader, out)
	}
}
