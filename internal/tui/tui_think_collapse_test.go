package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// ── 思考折叠闸门（docs/thinking-collapse-design.md §7）───────────────────────
//
// 折叠态是正文的默认形态，所以这里的每一条都在钉"少占行不许以丢内容为代价"：
// 行数由 thinkDetailOpen 决定，内容永远完整，读全文的路是 alt+h 与 ctrl+g。

const collapseLongThought = "<thought>\n" +
	"goal: Analyze request and delegate coding\n" +
	"step: Repo-Agent returned the auth module map\n" +
	"reason: Need Coding-Agent to patch the token validator\n" +
	"plan:\n" +
	"- [x] Gather context via Repo-Agent\n" +
	"- [>] Delegate patch to Coding-Agent\n" +
	"- [ ] Verify with go build\n" +
	"</thought>\n\nAnswer: the validator is patched."

func feedThinkingRun(m *model, agent string, paras int) *model {
	for i := 1; i <= paras; i++ {
		m = feedEvent(m, buildEvent("thinking", agent,
			strings.Repeat("word ", 30)+string(rune('a'+i%26))+", 这是一段足够长的中文思考用来验证折行"))
	}
	return m
}

// TestThinkingCollapse_OneLinePerEntry：折叠态每个 thinking 条目恰好一行，
// 且行宽不超过视口内宽（宽度矩阵）。
func TestThinkingCollapse_OneLinePerEntry(t *testing.T) {
	for _, width := range []int{30, 60, 100, 240} {
		m := newTestModel()
		m.viewport.SetWidth(width)
		m.termWidth = width
		m = feedThinkingRun(m, "coding", 4)
		m.rebuildContentCache()

		lines := strings.Split(thinkingTestBody(m), "\n")
		got := 0
		for _, l := range lines {
			if strings.Contains(l, "💭") {
				got++
			}
		}
		if got != 1 {
			t.Errorf("width=%d: 💭 行数 = %d, want 1（四段合并进一个条目 = 一行）", width, got)
		}
		if !strings.Contains(thinkingTestBody(m), "×4") {
			t.Errorf("width=%d: 折叠行没数出 4 段\n%s", width, thinkingTestBody(m))
		}
		for i, l := range lines {
			if w := lipgloss.Width(l); w > width-2 {
				t.Errorf("width=%d line %d: %d > 内宽 %d", width, i, w, width-2)
			}
		}
	}
}

// TestThinkingCollapse_ExpandedShowsFullText：展开态全文可见（无省略号），
// 段号连续，行宽不越内宽。
func TestThinkingCollapse_ExpandedShowsFullText(t *testing.T) {
	m := newTestModel()
	m = feedThinkingRun(m, "coding", 4)
	m.rebuildContentCache()
	collapsed := thinkingTestBody(m)

	m.toggleThinkDetail()
	expanded := thinkingTestBody(m)

	if strings.Contains(expanded, "…") {
		t.Errorf("展开态不该出现截断省略号\n%s", expanded)
	}
	for n := 1; n <= 4; n++ {
		if !strings.Contains(expanded, "  "+string(rune('0'+n))+" │ ") {
			t.Errorf("缺少第 %d 段的编号导轨\n%s", n, expanded)
		}
	}
	if strings.Count(expanded, "word word") <= strings.Count(collapsed, "word word") {
		t.Errorf("展开态内容没有比折叠态多（折叠=%d 次 / 展开=%d 次）",
			strings.Count(collapsed, "word word"), strings.Count(expanded, "word word"))
	}
	for _, width := range []int{30, 60, 100, 240} {
		w := newTestModel()
		w.viewport.SetWidth(width)
		w.termWidth = width
		w = feedThinkingRun(w, "coding", 2)
		w.rebuildContentCache()
		w.toggleThinkDetail()
		for i, l := range strings.Split(thinkingTestBody(w), "\n") {
			// 正文落在 viewport 的 Padding(0,1) 之内，预算是内宽。
			if got := lipgloss.Width(l); got > width-2 {
				t.Errorf("width=%d line %d: %d > 内宽 %d", width, i, got, width-2)
			}
		}
	}
}

// TestThinkToggle_RoundTripKeepsScroll：切换改变行数、切回逐字复原，
// 且贴底的窗口仍然贴底。
func TestThinkToggle_RoundTripKeepsScroll(t *testing.T) {
	m := newTestModel()
	m = feedThinkingRun(m, "coding", 3)
	m = feedEvent(m, buildEvent("ai_response", "coding", "结论：布局已经改好。"))
	m.rebuildContentCache()

	before := thinkingTestBody(m)
	beforeLines := m.bodyTotalLines()
	if !m.bodyAtBottom() {
		t.Fatal("夹具应贴着底（生产流式期间就在这个状态）")
	}

	m.toggleThinkDetail()
	expandedLines := m.bodyTotalLines()
	if expandedLines <= beforeLines {
		t.Fatalf("展开后行数 %d 没有增加（折叠态 %d）", expandedLines, beforeLines)
	}
	if !m.bodyAtBottom() {
		t.Error("展开后窗口脱离了尾部")
	}

	m.toggleThinkDetail()
	if after := m.bodyTotalLines(); after != beforeLines {
		t.Errorf("切回后行数 = %d, want %d", after, beforeLines)
	}
	if got := thinkingTestBody(m); got != before {
		t.Errorf("切回后正文与初始不逐字相同\n---- now ----\n%s\n---- was ----\n%s", got, before)
	}
}

// TestThoughtOneLine_ShapeAndYield：折叠态的 thought 行只留 goal + 当前 plan 项 + 进度；
// 窄宽度先丢 plan 段；展开态与圆角面板逐字相同。
func TestThoughtOneLine_ShapeAndYield(t *testing.T) {
	m := newTestModel()
	e := &logEntry{eventType: "ai_response", from: "director", content: collapseLongThought}

	one := stripANSI(m.renderSingleEntry(e, 100))
	if n := strings.Count(one, "◆ THOUGHT"); n != 1 {
		t.Errorf("折叠态 thought 行数 = %d, want 1\n%s", n, one)
	}
	for _, want := range []string{"◆ THOUGHT", "Analyze request and delegate coding",
		"Delegate patch to Coding-Agent", "1/3"} {
		if !strings.Contains(one, want) {
			t.Errorf("折叠行缺少 %q\n%s", want, one)
		}
	}
	for _, forbidden := range []string{"GOAL", "REASON", "STEP", "╭", "</thought>"} {
		if strings.Contains(one, forbidden) {
			t.Errorf("折叠行漏出展开态内容 %q\n%s", forbidden, one)
		}
	}

	// 窄宽度：plan 段整段让位，goal 仍在，行宽不越界。
	narrow := stripANSI(renderThinklinkPanelOneLine(
		strings.TrimSuffix(strings.TrimPrefix(collapseLongThought, "<thought>\n"), "\n\nAnswer: the validator is patched."),
		30))
	if strings.Contains(narrow, "▸") {
		t.Errorf("宽度 30 时 plan 段应整段丢弃\n%s", narrow)
	}
	if w := lipgloss.Width(narrow); w > 30 {
		t.Errorf("窄行宽 %d > 30：%q", w, narrow)
	}

	// 展开态 == 圆角面板（同一渲染函数，不许分叉）。
	m.thinkDetailOpen = true
	e.clearRenderCache()
	expanded := m.renderSingleEntry(e, 100)
	blocks := strings.Split(strings.SplitN(collapseLongThought, "</thought>", 2)[0], "<thought>")[1]
	want := renderThinklinkPanel(strings.TrimPrefix(blocks, "\n"), 100-thinklinkPanelGutter)
	if !strings.Contains(expanded, want) {
		t.Errorf("展开态与 renderThinklinkPanel 不逐字相同\n---- got ----\n%s\n---- want ----\n%s",
			stripANSI(expanded), stripANSI(want))
	}
}

// TestAgentSeparatorFitsInnerWidth：agent 分隔行必须按视口内宽排（Padding(0,1) 之内），
// 多出的 2 列会把整块正文顶宽并被 viewport 折出去。
func TestAgentSeparatorFitsInnerWidth(t *testing.T) {
	for _, width := range []int{40, 80, 100, 240} {
		m := newTestModel()
		m.viewport.SetWidth(width)
		m.termWidth = width
		m = feedEvent(m, buildEvent("ai_response", "director", "结论。"))
		m.rebuildContentCache()

		body := m.bodyText(0, m.bodyTotalLines())
		found := false
		for _, l := range strings.Split(body, "\n") {
			if !strings.Contains(l, "director") || !strings.Contains(l, "──") {
				continue
			}
			found = true
			if got := lipgloss.Width(l); got > width-2 {
				t.Errorf("width=%d: 分隔行 %d 列 > 内宽 %d", width, got, width-2)
			}
		}
		if !found {
			t.Errorf("width=%d: 没找到 director 的分隔行\n%s", width, stripANSI(body))
		}
	}
}
