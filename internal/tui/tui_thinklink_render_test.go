package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

const structuredBlock = `goal: Analyze request and delegate coding
step: Repo-Agent returned the auth module map
reason: Need Coding-Agent to patch the token validator
plan:
- [x] Gather context via Repo-Agent
- [>] Delegate patch to Coding-Agent
- [ ] Verify with go build`

// 结构化 thought 块详情渲染：字段标签、进度计数与状态图标都应出现。
func TestThinklinkDetailLines_Structured(t *testing.T) {
	entry := ThinklinkEntry{Kind: "thought_plan", Content: structuredBlock}
	lines := thinklinkDetailLines(entry, 60)
	joined := strings.Join(lines, "\n")

	for _, want := range []string{"GOAL", "STEP", "REASON", "PLAN", "✓", "▸", "○", "1/3"} {
		if !strings.Contains(joined, want) {
			t.Errorf("detail render missing %q\n%s", want, joined)
		}
	}
	if !strings.Contains(joined, "Analyze request and delegate coding") {
		t.Errorf("goal value missing\n%s", joined)
	}
}

// 每行显示宽度不得超过 innerW（否则圆角边框会错位）。
func TestThinklinkDetailLines_WidthBound(t *testing.T) {
	for _, innerW := range []int{20, 40, 60, 100} {
		entry := ThinklinkEntry{Kind: "thought_plan", Content: structuredBlock}
		for i, l := range thinklinkDetailLines(entry, innerW) {
			if w := lipgloss.Width(l); w > innerW {
				t.Errorf("innerW=%d line %d width=%d > %d: %q", innerW, i, w, innerW, l)
			}
		}
	}
}

// 长值按词折行且续行对齐到标签槽之后。
func TestThinklinkDetailLines_WrapsLongValues(t *testing.T) {
	long := "goal: " + strings.Repeat("word ", 30) + "\nreason: short why\nplan:\n- [>] only item"
	entry := ThinklinkEntry{Kind: "thought_plan", Content: long}
	lines := thinklinkDetailLines(entry, 40)
	if len(lines) < 4 {
		t.Fatalf("expected long goal to wrap into multiple lines, got %d", len(lines))
	}
	// 续行以标签槽宽的空格开头（对齐）。
	gutter := strings.Repeat(" ", thinklinkLabelGutter)
	if !strings.HasPrefix(lines[1], gutter) {
		t.Errorf("continuation line not aligned to gutter: %q", lines[1])
	}
}

// 旧 markdown（非结构化）内容走原文兜底渲染，不丢内容。
func TestThinklinkDetailLines_LegacyFallback(t *testing.T) {
	legacy := "## Thought & Plan\n### Thought Process\nI will read the file first."
	entry := ThinklinkEntry{Kind: "thought_plan", Content: legacy}
	joined := strings.Join(thinklinkDetailLines(entry, 60), "\n")
	if !strings.Contains(joined, "I will read the file first.") {
		t.Errorf("legacy content lost in fallback render:\n%s", joined)
	}
	if strings.Contains(joined, "GOAL") {
		t.Errorf("legacy content should not render as structured:\n%s", joined)
	}
}

// 列表行预览：结构化块优先取进行中条目文本。
func TestThinklinkListPreview_CurrentItem(t *testing.T) {
	entry := ThinklinkEntry{Kind: "thought_plan", Content: structuredBlock}
	if got := thinklinkListPreview(entry); got != "Delegate patch to Coding-Agent" {
		t.Errorf("preview = %q, want the in-progress plan item", got)
	}
}

// 列表行预览：无进行中条目时退回 GOAL。
func TestThinklinkListPreview_GoalFallback(t *testing.T) {
	entry := ThinklinkEntry{Kind: "thought_plan", Content: "goal: ship it\nreason: because\nplan:\n- [x] done"}
	if got := thinklinkListPreview(entry); got != "ship it" {
		t.Errorf("preview = %q, want goal", got)
	}
}
