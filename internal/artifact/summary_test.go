package artifact

import (
	"fmt"
	"strings"
	"testing"
)

// TestEstTokens token 估算：ASCII 约 4 字符/token、非 ASCII 约 1 字符/token。
func TestEstTokens(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty", "", 0},
		{"pure ascii 8 chars", "abcdefgh", 2},
		{"pure ascii 400 chars", strings.Repeat("a", 400), 100},
		{"pure chinese 7 chars", "天气晴朗很好啊", 7},
		{"mixed", "ab中", 0 + 1}, // 2 ascii -> 2/4=0（整除）+ 1 non-ascii = 1
	}
	for _, c := range cases {
		if got := EstTokens(c.in); got != c.want {
			t.Errorf("%s: EstTokens = %d, want %d", c.name, got, c.want)
		}
	}
}

// TestReduceWithinBudget 恰好等于阈值：不截断、返回全文。
func TestReduceWithinBudget(t *testing.T) {
	// 400 ascii = 100 token == budget。
	text := strings.Repeat("a", 400)
	got, truncated := Reduce(text, "20260219-120000-3fa9c2d1", 100)
	if truncated {
		t.Error("text exactly at budget should not be truncated")
	}
	if got != text {
		t.Error("text within budget should be returned as-is")
	}
}

// TestReduceSlightlyOverBudget 略超 1 token：触发截断。
func TestReduceSlightlyOverBudget(t *testing.T) {
	// 404 ascii = 101 token，budget=100 → 截断。
	text := strings.Repeat("a", 404)
	got, truncated := Reduce(text, "20260219-120000-3fa9c2d1", 100)
	if !truncated {
		t.Fatal("text one token over budget should be truncated")
	}
	if got == text {
		t.Error("truncated summary must differ from full text")
	}
	if !strings.Contains(got, "Full result stored as artifact 20260219-120000-3fa9c2d1.") {
		t.Errorf("summary should contain artifact id marker, got: %s", got)
	}
}

// TestReducePureASCII 纯 ASCII 截断：标记行包含正确 id 与字符数，结果满足预算。
func TestReducePureASCII(t *testing.T) {
	const id = "20260219-120000-3fa9c2d1"
	// 2004 ascii = 501 token，budget=500（target=440 → kept=1763）。
	text := strings.Repeat("a", 2004)
	got, truncated := Reduce(text, id, DefaultBudgetTokens)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if !strings.Contains(got, fmt.Sprintf("showing first %d of %d chars", 1763, 2004)) {
		t.Errorf("marker should contain kept/total char counts, got: %s", got)
	}
	if !strings.Contains(got, id) {
		t.Errorf("marker should contain artifact id %s", id)
	}
	if est := EstTokens(got); est > DefaultBudgetTokens {
		t.Errorf("EstTokens(summary) = %d, want <= %d", est, DefaultBudgetTokens)
	}
	if strings.Count(got, "\n\n[Truncated: showing first") != 1 {
		t.Error("summary should contain exactly one truncation marker")
	}
	if !strings.HasSuffix(got, "]\n") {
		t.Error("marker line should end with newline")
	}
}

// TestReducePureChinese 纯中文截断：rune 计数正确、结果满足预算。
func TestReducePureChinese(t *testing.T) {
	const id = "20260219-120000-00000000"
	// 600 中文 = 600 token，budget=500（target=440 → kept=440 rune）。
	text := strings.Repeat("中", 600)
	got, truncated := Reduce(text, id, DefaultBudgetTokens)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if !strings.Contains(got, fmt.Sprintf("showing first %d of %d chars", 440, 600)) {
		t.Errorf("marker should contain kept/total char counts, got: %s", got)
	}
	if est := EstTokens(got); est > DefaultBudgetTokens {
		t.Errorf("EstTokens(summary) = %d, want <= %d", est, DefaultBudgetTokens)
	}
	// 摘要中文正文应为 440 个 '中'。
	if !strings.HasPrefix(got, strings.Repeat("中", 440)) {
		t.Error("summary should start with the kept chinese prefix")
	}
}

// TestReduceSingleLineNoNewline 单行超长无换行：不 panic、不回退、标记正确。
func TestReduceSingleLineNoNewline(t *testing.T) {
	const id = "20260219-120000-00000001"
	text := strings.Repeat("b", 2004) // 无任何 '\n'
	got, truncated := Reduce(text, id, DefaultBudgetTokens)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if !strings.Contains(got, "showing first 1763 of 2004 chars") {
		t.Errorf("no-newline text should keep the binary-search prefix as-is, got: %s", got)
	}
	if est := EstTokens(got); est > DefaultBudgetTokens {
		t.Errorf("EstTokens(summary) = %d, want <= %d", est, DefaultBudgetTokens)
	}
}

// TestReduceWindowFallbackNewline 截断点落在最后 10% 窗口内时回退到最近的 '\n'。
//
// 构造：100 行 "aaaa\n"（500 ascii = 125 token），budget=100（target=40）。
// 二分 kept=163（163/4=40）；窗口=16，搜索区间 [147,162] 内 '\n' 位置为
// 149/154/159（i%5==4），取最近者 159，kept=160（保留到 '\n' 含）。
func TestReduceWindowFallbackNewline(t *testing.T) {
	const id = "20260219-120000-00000002"
	text := strings.Repeat("aaaa\n", 100)
	got, truncated := Reduce(text, id, 100)
	if !truncated {
		t.Fatal("expected truncation")
	}

	wantKept := text[:160] // 以 '\n' 结尾的回退前缀
	if !strings.HasPrefix(got, wantKept) {
		t.Errorf("summary should start with newline-bounded prefix ending at char 160")
	}
	if !strings.Contains(got, "showing first 160 of 500 chars") {
		t.Errorf("marker should reflect fallback kept count, got: %s", got)
	}
	if est := EstTokens(got); est > 100 {
		t.Errorf("EstTokens(summary) = %d, want <= 100", est)
	}
}

// TestReduceTruncationMarkerFormat 标记行格式逐字段断言。
func TestReduceTruncationMarkerFormat(t *testing.T) {
	const id = "20260219-120000-3fa9c2d1"
	text := strings.Repeat("x", 2004)
	got, truncated := Reduce(text, id, DefaultBudgetTokens)
	if !truncated {
		t.Fatal("expected truncation")
	}
	want := fmt.Sprintf("showing first %d of %d chars. Full result stored as artifact %s.", 1763, 2004, id)
	if !strings.Contains(got, want) {
		t.Errorf("marker mismatch, want substring %q in:\n%s", want, got)
	}
}

// TestReduceDeterministic 同输入多次调用结果一致（无随机性）。
func TestReduceDeterministic(t *testing.T) {
	const id = "20260219-120000-3fa9c2d1"
	text := strings.Repeat("line\n", 300)
	a, _ := Reduce(text, id, DefaultBudgetTokens)
	b, _ := Reduce(text, id, DefaultBudgetTokens)
	if a != b {
		t.Error("Reduce must be deterministic")
	}
}

// TestDisabled kill-switch：YAGENT_ARTIFACT_DISABLE=1 生效。
func TestDisabled(t *testing.T) {
	t.Setenv("YAGENT_ARTIFACT_DISABLE", "1")
	if !Disabled() {
		t.Error("Disabled() = false, want true when YAGENT_ARTIFACT_DISABLE=1")
	}

	t.Setenv("YAGENT_ARTIFACT_DISABLE", "0")
	if Disabled() {
		t.Error("Disabled() = true, want false when YAGENT_ARTIFACT_DISABLE=0")
	}

	t.Setenv("YAGENT_ARTIFACT_DISABLE", "")
	if Disabled() {
		t.Error("Disabled() = true, want false when unset")
	}
}
