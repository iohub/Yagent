package browserctl

import (
	"fmt"
	"strings"
	"testing"
)

func TestRenderSnapshot_DropsCoordinatesAndDedupes(t *testing.T) {
	raw := `{
	  "url": "https://example.com",
	  "title": "Example Domain",
	  "elements": [
	    {"ref":"e1","kind":"link","text":"Home","x":10,"y":20,"w":30,"h":40,"href":"https://example.com/"},
	    {"ref":"e2","kind":"link","text":"Home","x":10,"y":60,"w":30,"h":40,"href":"https://example.com/"},
	    {"ref":"e3","kind":"textbox","text":"Search","x":1,"y":2,"w":3,"h":4}
	  ]
	}`

	got := RenderSnapshot(raw, 4000)

	if !strings.Contains(got, "url: https://example.com") {
		t.Errorf("missing url line:\n%s", got)
	}
	if !strings.Contains(got, `@e1 link "Home" -> https://example.com/`) {
		t.Errorf("missing element line:\n%s", got)
	}
	if strings.Contains(got, "@e2") {
		t.Errorf("duplicate element should be collapsed:\n%s", got)
	}
	if !strings.Contains(got, "已合并 1 条重复元素") {
		t.Errorf("missing dedupe notice:\n%s", got)
	}
	if strings.Contains(got, "\"x\"") || strings.Contains(got, "20") {
		t.Errorf("coordinates must not leak into output:\n%s", got)
	}
}

func TestRenderSnapshot_BudgetTruncatesWithHint(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(`{"url":"https://example.com","title":"t","elements":[`)
	for i := 0; i < 60; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		// 文本必须各不相同，否则会被去重收敛掉，测不到预算截断
		fmt.Fprintf(&sb, `{"ref":"e%d","kind":"link","text":"Item %d %s"}`, i+1, i, strings.Repeat("x", 40))
	}
	sb.WriteString(`]}`)

	got := RenderSnapshot(sb.String(), 600)

	if !strings.Contains(got, "共 60 个元素") {
		t.Errorf("expected truncation hint with total count:\n%s", got)
	}
	if len(got) > 1400 {
		t.Errorf("output not bounded, len=%d", len(got))
	}
}

func TestRenderInspect_SectionsAndTextBudget(t *testing.T) {
	long := strings.Repeat("字", 5000)
	raw := `{"url":"https://a.test","title":"T","readyState":"complete","query":"go",
	  "text":"` + long + `","headings":["H1"],"elements":[{"ref":"e1","kind":"button","text":"Submit"}]}`

	got := RenderInspect(raw, 4000, 100)

	if !strings.Contains(got, "readyState: complete") || !strings.Contains(got, "query: go") {
		t.Errorf("missing metadata:\n%s", got)
	}
	if !strings.Contains(got, "headings:\n- H1") {
		t.Errorf("missing headings:\n%s", got)
	}
	if !strings.Contains(got, "elements:\n@e1 button \"Submit\"") {
		t.Errorf("missing elements section:\n%s", got)
	}
	if strings.Count(got, "字") != 100 || !strings.Contains(got, "…") {
		t.Errorf("text budget not applied, rune count=%d", strings.Count(got, "字"))
	}
}

func TestRenderInspect_InvalidJSONFallsBackToClip(t *testing.T) {
	got := RenderInspect("launch failed: no such file", 200, 100)
	if !strings.Contains(got, "launch failed") {
		t.Errorf("raw output should be surfaced, got %q", got)
	}
}

func TestClip_DoesNotBreakUTF8(t *testing.T) {
	got := Clip(strings.Repeat("中", 10), 7)
	if !strings.Contains(got, "已截断") {
		t.Errorf("expected truncation marker, got %q", got)
	}
	for _, r := range got {
		if r == '\uFFFD' {
			t.Fatalf("clipping broke a multi-byte rune: %q", got)
		}
	}
}

func TestClipRunes(t *testing.T) {
	if got := ClipRunes("abcdef", 3); got != "abc…" {
		t.Errorf("ClipRunes = %q", got)
	}
	if got := ClipRunes("ab", 3); got != "ab" {
		t.Errorf("ClipRunes short = %q", got)
	}
}
