package browserctl

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	elementTextLimit = 60
	budgetHint       = "… 输出已达上限，用 browser_inspect(query=\"关键词\", limit=20) 收窄"
)

type snapshotElement struct {
	Ref  string `json:"ref"`
	Kind string `json:"kind"`
	Text string `json:"text"`
	Href string `json:"href"`
}

type snapshotPayload struct {
	URL      string            `json:"url"`
	Title    string            `json:"title"`
	Elements []snapshotElement `json:"elements"`
}

type inspectPayload struct {
	URL        string            `json:"url"`
	Title      string            `json:"title"`
	ReadyState string            `json:"readyState"`
	Query      string            `json:"query"`
	Text       string            `json:"text"`
	Headings   []string          `json:"headings"`
	Elements   []snapshotElement `json:"elements"`
}

// RenderSnapshot 把 `snapshot --json` 压成紧凑文本：
// 丢掉坐标（LLM 用不上）、按 kind+text+href 去重（导航栏/列表页常见）、文本截断。
func RenderSnapshot(raw string, budget int) string {
	var p snapshotPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return Clip(raw, budget)
	}
	var sb strings.Builder
	if p.URL != "" {
		fmt.Fprintf(&sb, "url: %s\n", p.URL)
	}
	if p.Title != "" {
		fmt.Fprintf(&sb, "title: %s\n", p.Title)
	}
	sb.WriteString("\n")
	// 头两行 + 空行的固定开销先扣掉，剩下的才是元素预算
	lines, hidden, deduped := renderElements(p.Elements, budget-64)
	sb.WriteString(strings.Join(lines, "\n"))
	if len(lines) > 0 && hidden == 0 && deduped == 0 {
		sb.WriteString("\n")
	}
	if deduped > 0 {
		fmt.Fprintf(&sb, "\n（已合并 %d 条重复元素）", deduped)
	}
	if hidden > 0 {
		fmt.Fprintf(&sb, "\n%s（共 %d 个元素，省略 %d 个）", budgetHint, len(p.Elements), hidden)
	}
	return sb.String()
}

// RenderInspect 渲染 `inspect --json`：区块与上游文本模式对齐，便于模型复用习惯。
func RenderInspect(raw string, budget, textChars int) string {
	var p inspectPayload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return Clip(raw, budget)
	}
	var sb strings.Builder
	if p.URL != "" {
		fmt.Fprintf(&sb, "url: %s\n", p.URL)
	}
	if p.Title != "" {
		fmt.Fprintf(&sb, "title: %s\n", p.Title)
	}
	if p.ReadyState != "" {
		fmt.Fprintf(&sb, "readyState: %s\n", p.ReadyState)
	}
	if p.Query != "" {
		fmt.Fprintf(&sb, "query: %s\n", p.Query)
	}
	if text := strings.TrimSpace(p.Text); text != "" {
		if textChars > 0 && utf8.RuneCountInString(text) > textChars {
			text = string([]rune(text)[:textChars]) + "…"
		}
		fmt.Fprintf(&sb, "\ntext:\n%s\n", text)
	}
	if len(p.Headings) > 0 {
		sb.WriteString("\nheadings:\n")
		for _, h := range p.Headings {
			fmt.Fprintf(&sb, "- %s\n", ClipRunes(h, 120))
		}
	}

	consumed := sb.Len()
	lines, hidden, deduped := renderElements(p.Elements, budget-consumed-80)
	if len(lines) > 0 {
		sb.WriteString("\nelements:\n")
		sb.WriteString(strings.Join(lines, "\n"))
		sb.WriteString("\n")
	}
	if deduped > 0 {
		fmt.Fprintf(&sb, "（已合并 %d 条重复元素）\n", deduped)
	}
	if hidden > 0 {
		fmt.Fprintf(&sb, "%s（共 %d 个元素，省略 %d 个）\n", budgetHint, len(p.Elements), hidden)
	}
	return sb.String()
}

// renderElements 渲染元素行，返回 (行, 因预算省略数, 去重数)。
func renderElements(elements []snapshotElement, budget int) ([]string, int, int) {
	seen := map[string]bool{}
	lines := make([]string, 0, len(elements))
	deduped := 0
	for _, e := range elements {
		key := e.Kind + "\x00" + e.Text + "\x00" + e.Href
		if e.Text != "" {
			if seen[key] {
				deduped++
				continue
			}
			seen[key] = true
		}
		lines = append(lines, formatElement(e))
	}

	if budget <= 0 {
		return lines, 0, deduped
	}
	used := 0
	for i, l := range lines {
		used += len(l) + 1
		if used > budget {
			return lines[:i], len(lines) - i, deduped
		}
	}
	return lines, 0, deduped
}

func formatElement(e snapshotElement) string {
	kind := e.Kind
	if kind == "" {
		kind = "element"
	}
	line := fmt.Sprintf("@%s %s %q", e.Ref, kind, ClipRunes(e.Text, elementTextLimit))
	if e.Href != "" {
		line += " -> " + e.Href
	}
	return line
}

// Clip 按字节上限裁剪，并保证不切坏 UTF-8。
func Clip(s string, budget int) string {
	if budget <= 0 || len(s) <= budget {
		return s
	}
	cut := s[:budget]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "\n…（已截断）"
}

// ClipRunes 按字符数裁剪。
func ClipRunes(s string, limit int) string {
	if limit <= 0 {
		return s
	}
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit]) + "…"
}
