package artifact

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
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

// markerSep 是正文与截断标记的分界；标记内部的措辞怎么改都不影响这里的切分。
const markerSep = "\n\n[Truncated:"

// assertReduceContract 钉住截断输出的契约，而不是二分/窗口回退算出来的中间数字
// （那些随 ReserveForMarker 与估算系数变化）：
//   - 前缀必须是原文的逐字前缀，并且尽量占满预算（不是一刀砍到几个字符）；
//   - 标记里的两个数字必须等于实际前缀长度与实际全文长度；
//   - 标记必须带上 artifact id（否则 Director 无从取回全文）；
//   - 整体不超过 budget，且以换行收尾。
func assertReduceContract(t *testing.T, text, got, id string, budget int) (prefix, marker string) {
	t.Helper()

	idx := strings.Index(got, markerSep)
	if idx < 0 {
		t.Fatalf("summary has no %q separator:\n%s", markerSep, got)
	}
	prefix, marker = got[:idx], got[idx:]

	if !strings.HasPrefix(text, prefix) {
		t.Errorf("prefix is not a verbatim prefix of the full text: %q", prefix)
	}
	if kept, total := utf8.RuneCountInString(prefix), utf8.RuneCountInString(text); !strings.Contains(
		marker, fmt.Sprintf("%d of %d chars", kept, total)) {
		t.Errorf("marker counts must match kept=%d total=%d, got: %s", kept, total, marker)
	}
	if !strings.Contains(marker, id) {
		t.Errorf("marker must carry the artifact id %s: %s", id, marker)
	}
	if strings.Count(got, markerSep) != 1 {
		t.Errorf("summary should contain exactly one truncation marker:\n%s", got)
	}
	if est := EstTokens(got); est > budget {
		t.Errorf("EstTokens(summary) = %d, want <= %d", est, budget)
	}
	if want := budget - ReserveForMarker; want > 0 && EstTokens(prefix) < want/2 {
		t.Errorf("summary kept only %d tokens of the %d available: %q", EstTokens(prefix), want, prefix)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("summary should end with a newline:\n%q", got)
	}
	return prefix, marker
}

// TestReducePureASCII 纯 ASCII 截断：满足契约，正文一个换行都没有所以原样截断。
func TestReducePureASCII(t *testing.T) {
	const id = "20260219-120000-3fa9c2d1"
	text := strings.Repeat("a", 2004) // 501 token > DefaultBudgetTokens
	got, truncated := Reduce(text, id, DefaultBudgetTokens)
	if !truncated {
		t.Fatal("expected truncation")
	}
	prefix, _ := assertReduceContract(t, text, got, id, DefaultBudgetTokens)
	if strings.HasSuffix(prefix, "\n") {
		t.Errorf("no-newline text cannot end on a line boundary: %q", prefix)
	}
}

// TestReducePureChinese 纯中文截断：按 rune 计数，契约同样成立。
func TestReducePureChinese(t *testing.T) {
	const id = "20260219-120000-00000000"
	text := strings.Repeat("中", 600) // 600 token > DefaultBudgetTokens
	got, truncated := Reduce(text, id, DefaultBudgetTokens)
	if !truncated {
		t.Fatal("expected truncation")
	}
	prefix, _ := assertReduceContract(t, text, got, id, DefaultBudgetTokens)
	if !strings.HasPrefix(prefix, "中") {
		t.Errorf("summary should start with the kept chinese prefix: %q", prefix)
	}
}

// TestReduceSingleLineNoNewline 单行超长无换行：不 panic、不回退、契约成立。
func TestReduceSingleLineNoNewline(t *testing.T) {
	const id = "20260219-120000-00000001"
	text := strings.Repeat("b", 2004) // 无任何 '\n'
	got, truncated := Reduce(text, id, DefaultBudgetTokens)
	if !truncated {
		t.Fatal("expected truncation")
	}
	prefix, _ := assertReduceContract(t, text, got, id, DefaultBudgetTokens)
	if strings.Contains(prefix, "\n") {
		t.Errorf("no-newline input must not gain one: %q", prefix)
	}
}

// TestReduceWindowFallbackNewline 截断点落在最后 10% 窗口内时回退到最近的 '\n'。
//
// 契约是"回退到**最近**的那个换行"，不是"回退到某个换行"，更不是 kept=160 这个算出来的数：
// 前缀以换行收尾，而下一个换行边界就会超出预算。
func TestReduceWindowFallbackNewline(t *testing.T) {
	const id = "20260219-120000-00000002"
	text := strings.Repeat("aaaa\n", 100) // 500 ascii = 125 token > budget 100
	const budget = 100
	got, truncated := Reduce(text, id, budget)
	if !truncated {
		t.Fatal("expected truncation")
	}
	prefix, _ := assertReduceContract(t, text, got, id, budget)

	if !strings.HasSuffix(prefix, "\n") {
		t.Errorf("kept prefix should fall back to a newline boundary: %q", prefix)
	}
	rest := text[len(prefix):]
	next := strings.Index(rest, "\n")
	if next < 0 {
		t.Fatal("fixture must still have a following line")
	}
	if est := EstTokens(prefix + rest[:next+1]); est <= budget-ReserveForMarker {
		t.Errorf("next newline boundary also fits the target (%d <= %d), so the fallback cut one line too early",
			est, budget-ReserveForMarker)
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
