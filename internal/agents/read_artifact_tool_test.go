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

// TestReadArtifactMaxLinesClamp max_lines=3000 超硬上限 2000:被钳制。
// 行内容用纯数字 (每行输出约 10 rune),确保 2000 行总输出 < 24000 rune 预算，
// 预算不先触发，从而真正验证行数硬上限钳制 (而非预算截断)。
func TestReadArtifactMaxLinesClamp(t *testing.T) {
	lines := make([]string, 2500)
	for i := range lines {
		lines[i] = fmt.Sprintf("%d", i+1)
	}
	ref := saveTestArtifact(t, strings.Join(lines, "\n"))
	out := callReadArtifact(t, map[string]interface{}{
		"id":        ref.ID,
		"max_lines": float64(3000),
	})

	wantHeader := fmt.Sprintf("artifact %s | agent: coding | 2500 lines | showing 1-2000", ref.ID)
	if !strings.HasPrefix(out, wantHeader) {
		t.Errorf("header mismatch (clamp to 2000 expected):\nwant: %q\ngot:\n%s", wantHeader, out)
	}
	wantMore := "[more: 500 lines left; continue with start_line=2001]"
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

// TestReadArtifactDynamicFullRead 动态默认一次读完 (不传 max_lines,剩余≤2000 则一次读完)
func TestReadArtifactDynamicFullRead(t *testing.T) {
	// 构造 1200 行 artifact
	ref := saveTestArtifact(t, multLines(1200))

	// 不传 max_lines,应动态计算为 min(1200, 2000)=1200,一次读完
	out := callReadArtifact(t, map[string]interface{}{
		"id": ref.ID,
	})

	// 断言头部显示 showing 1-1200
	wantHeader := fmt.Sprintf("artifact %s | agent: coding | 1200 lines | showing 1-1200", ref.ID)
	if !strings.HasPrefix(out, wantHeader) {
		t.Errorf("header mismatch:\nwant prefix: %q\ngot:\n%s", wantHeader, out)
	}

	// 断言最后一行已输出
	if !strings.Contains(out, "1200: line 1200") {
		t.Error("expected to include last line '1200: line 1200'")
	}

	// 断言不含 more 提示 (一次读完)
	if strings.Contains(out, "[more:") {
		t.Errorf("expected no [more: hint when full read, but got:\n%s", out)
	}
}

// TestReadArtifactTwoPassRead 超大 artifact 最多 2 次读完
// rune 预算核算：使用极短行"%d"形式 (1-4000)
// - 行号前缀：4000 行宽 4 位，平均每行约 3.5 位 + ": " 2 = 5.5 rune
// - 实际内容："1"→"4000"，平均约 3.5 rune
// - 总计：≈9 rune/行 × 2000 行 = 18000 rune < 24000 预算 (安全)
func TestReadArtifactTwoPassRead(t *testing.T) {
	// 构造 4000 行，行内容极短："1","2",...,"4000"
	lines := make([]string, 4000)
	for i := 0; i < 4000; i++ {
		lines[i] = fmt.Sprintf("%d", i+1)
	}
	fullText := strings.Join(lines, "\n")
	ref := saveTestArtifact(t, fullText)

	// 第一次：不传 max_lines 和 start_line,应被硬上限钳制到 2000
	out1 := callReadArtifact(t, map[string]interface{}{
		"id": ref.ID,
	})

	// 断言第一次头部 showing 1-2000 (被 2000 硬上限钳制)
	wantHeader1 := fmt.Sprintf("artifact %s | agent: coding | 4000 lines | showing 1-2000", ref.ID)
	if !strings.HasPrefix(out1, wantHeader1) {
		t.Errorf("first pass header mismatch:\nwant prefix: %q\ngot:\n%s", wantHeader1, out1)
	}

	// 断言第一次包含 more 提示，且有正确的 start_line=2001
	if !strings.Contains(out1, "[more: 2000 lines left; continue with start_line=2001]") {
		t.Errorf("expected [more: 2000 lines left; continue with start_line=2001] but got:\n%s", out1)
	}

	// 第二次：传入 start_line=2001,剩余 2000 行 ≤ 2000,应一次读完
	out2 := callReadArtifact(t, map[string]interface{}{
		"id":         ref.ID,
		"start_line": float64(2001),
	})

	// 断言第二次头部 showing 2001-4000
	wantHeader2 := fmt.Sprintf("artifact %s | agent: coding | 4000 lines | showing 2001-4000", ref.ID)
	if !strings.HasPrefix(out2, wantHeader2) {
		t.Errorf("second pass header mismatch:\nwant prefix: %q\ngot:\n%s", wantHeader2, out2)
	}

	// 断言第二次包含最后一行 "4000: 4000"
	if !strings.Contains(out2, "4000: 4000") {
		t.Error("expected to include last line '4000: 4000' in second pass")
	}

	// 断言第二次不含 more 提示
	if strings.Contains(out2, "[more:") {
		t.Errorf("expected no [more: hint in second pass, but got:\n%s", out2)
	}
}

// TestReadArtifactRuneBudget rune 预算提前截断
// 预算核算：100 行 × 500 rune/行 = 50000 rune > 24000 预算
// 行号前缀占用：100 行宽 3 位 + ": " 2 = 5 rune
// 每行实际输出 ≈ 5 + 500 = 505 rune
// 预计可输出 ≈ 24000/505 ≈ 47行 (含 header 共 48 行)
func TestReadArtifactRuneBudget(t *testing.T) {
	// 构造 100 行，每行 500 rune (不含行号前缀)
	longLine := strings.Repeat("x", 500)
	lines := make([]string, 100)
	for i := 0; i < 100; i++ {
		lines[i] = longLine
	}
	fullText := strings.Join(lines, "\n")
	ref := saveTestArtifact(t, fullText)

	// 不传 max_lines,应动态计算为 min(100, 2000)=100,但因 rune 预算会提前截断
	out := callReadArtifact(t, map[string]interface{}{
		"id": ref.ID,
	})

	// 断言输出行数 < 100 (被 rune 预算截断)
	// 输出格式：1 行 header + N 行内容，总行数 = N+1
	totalLines := strings.Count(out, "\n")
	// 预估：24000/505 ≈ 47 行内容 + 1 行 header = 48 行 (约数)
	if totalLines >= 100 {
		t.Errorf("expected output lines < 100 due to rune budget, but got %d lines", totalLines)
	}

	// 断言末尾包含 [more: 提示且有 continue with start_line=
	if !strings.Contains(out, "[more:") {
		t.Errorf("expected [more: hint due to rune budget truncation, but got:\n%s", out)
	}
	if !strings.Contains(out, "continue with start_line=") {
		t.Errorf("expected 'continue with start_line=' in [more: hint, but got:\n%s", out)
	}

	// 断言头部 showing 数字与实际输出一致
	// 总行数为 totalLines，其中 1 行为 header，内容为 totalLines-1 行
	contentLines := totalLines - 1 // 减去 header 行
	// 找到 "showing 1-N" 中的 N
	if !strings.Contains(out, fmt.Sprintf("showing 1-%d", contentLines)) {
		t.Errorf("expected header showing 1-%d, but got:\n%s", contentLines, out)
	}
}
