package tui

import (
	"strings"
	"testing"

	"yagent/internal/thinklink"

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

// ─── 主页面（viewport）里的 thought 渲染 ──────────────────────────────────

// 主页面消息正文：Director 每轮回复都是"结构化 thought 块 + 回答"。
const mainPageMessage = "<thought>\n" +
	"goal: Analyze request and delegate coding\n" +
	"step: Repo-Agent returned the auth module map\n" +
	"reason: Need Coding-Agent to patch the token validator\n" +
	"plan:\n" +
	"- [x] Gather context via Repo-Agent\n" +
	"- [>] Delegate patch to Coding-Agent\n" +
	"- [ ] Verify with go build\n" +
	"</thought>\n\n" +
	"Answer: the validator is patched, verifying now."

// panelContentRows 取圆角面板的正文行（│ 开头），剥掉左右边框与内边距。
func panelContentRows(rendered string) []string {
	var rows []string
	for _, line := range strings.Split(rendered, "\n") {
		runes := []rune(line)
		if len(runes) < 4 || runes[0] != '│' || runes[len(runes)-1] != '│' {
			continue
		}
		rows = append(rows, strings.TrimRight(string(runes[2:len(runes)-2]), " "))
	}
	return rows
}

// 主页面的 thought 面板正文必须与全屏详情逐行一致 —— "样式一样"靠共用同一
// 渲染函数成立，这条测试钉住它不被分叉成两套。
func TestMainPage_ThoughtPanelMatchesFullscreenDetail(t *testing.T) {
	const width = 100
	m := newTestModel()
	e := logEntry{eventType: "ai_response", from: "director", content: mainPageMessage}
	rendered := stripANSIForChrome(m.renderSingleEntry(&e, width))

	blocks, _ := thinklink.SplitThoughtBlocks(mainPageMessage)
	if len(blocks) != 1 {
		t.Fatalf("fixture should carry one thought block, got %d", len(blocks))
	}
	want := thinklinkDetailLines(
		ThinklinkEntry{Kind: "thought_plan", Content: blocks[0]}, width-thinklinkPanelGutter-4)
	for i, l := range want {
		want[i] = strings.TrimRight(stripANSIForChrome(l), " ")
	}
	got := panelContentRows(rendered)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("panel body differs from fullscreen detail\nmain: %q\ndetail: %q", got, want)
	}

	for _, want := range []string{"◆ THOUGHT", "GOAL", "REASON", "PLAN", "✓", "▸", "○", "1/3"} {
		if !strings.Contains(rendered, want) {
			t.Errorf("panel missing %q:\n%s", want, rendered)
		}
	}
	for _, forbidden := range []string{"<thought>", "</thought>", "goal: Analyze"} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("panel leaked raw block text %q:\n%s", forbidden, rendered)
		}
	}
	if !strings.Contains(rendered, "the validator is patched") {
		t.Errorf("answer text lost next to the panel:\n%s", rendered)
	}
}

// 面板每行的显示宽度不得超过视口内容区，否则折行会把圆角边框撕开。
func TestMainPage_ThoughtPanelWidthMatrix(t *testing.T) {
	for _, width := range []int{30, 40, 50, 60, 80, 100, 120, 160, 200, 240} {
		m := newTestModel()
		m.viewport.SetWidth(width)
		m.glamourRenderer = m.getGlamourRenderer(width - 6)
		e := logEntry{eventType: "ai_response", from: "director", content: mainPageMessage}
		for i, line := range strings.Split(m.renderSingleEntry(&e, width), "\n") {
			if w := lipgloss.Width(line); w > width-2 {
				t.Fatalf("width=%d line %d: %d > %d: %q", width, i, w, width-2, stripANSIForChrome(line))
			}
		}
	}
}

// 没有 thought 块时不该出现任何面板痕迹。
func TestMainPage_PlainResponseKeepsMarkdownPath(t *testing.T) {
	m := newTestModel()
	e := logEntry{eventType: "ai_response", from: "director", content: "**plain** answer, no thinking"}
	rendered := stripANSIForChrome(m.renderSingleEntry(&e, 100))
	if strings.Contains(rendered, "THOUGHT") || strings.Contains(rendered, "╭") {
		t.Errorf("plain answer got a thought panel:\n%s", rendered)
	}
	if !strings.Contains(rendered, "plain") {
		t.Errorf("plain answer lost:\n%s", rendered)
	}
}

// 流式正文：块没闭合就先原样显示，闭合后立刻换成面板（与 markdown 的节奏一致）。
func TestMainPage_StreamedBlockRendersOnceClosed(t *testing.T) {
	m := newTestModel()
	const width = 100
	open := logEntry{eventType: "ai_stream", from: "director", streaming: true,
		streamContent: "<thought>\ngoal: g\nreason: r\n"}
	if out := stripANSIForChrome(m.renderSingleEntry(&open, width)); strings.Contains(out, "THOUGHT") {
		t.Errorf("unterminated block should stay raw:\n%s", out)
	}

	closed := logEntry{eventType: "ai_stream", from: "director", streaming: true,
		streamContent: "<thought>\ngoal: g\nreason: r\n</thought>\n\nhalf an answer"}
	out := stripANSIForChrome(m.renderSingleEntry(&closed, width))
	if !strings.Contains(out, "◆ THOUGHT") || !strings.Contains(out, "GOAL") {
		t.Errorf("closed block should render as a panel:\n%s", out)
	}
	if !strings.Contains(out, "half an answer▌") {
		t.Errorf("stream cursor should stay at the tail:\n%s", out)
	}
}

// 一条消息里的多个块各自成面板，正文合并到面板之后。
func TestMainPage_MultipleBlocksStackPanels(t *testing.T) {
	m := newTestModel()
	e := logEntry{eventType: "ai_response", from: "director",
		content: "<thought>\ngoal: a\nreason: b\n</thought>\nmid\n" +
			"<thought>\ngoal: c\nreason: d\n</thought>\ntail"}
	rendered := stripANSIForChrome(m.renderSingleEntry(&e, 100))
	if n := strings.Count(rendered, "◆ THOUGHT"); n != 2 {
		t.Errorf("want 2 panels, got %d:\n%s", n, rendered)
	}
	if !strings.Contains(rendered, "mid") || !strings.Contains(rendered, "tail") {
		t.Errorf("prose lost between panels:\n%s", rendered)
	}
}
