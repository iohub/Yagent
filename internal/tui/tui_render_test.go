package tui

import (
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestRenderThinkingCollapsed 钉住折叠态契约：每个 thinking 条目恰好一行、
// 以 "💭 ×N │ " 开头、显示宽度 == 内宽预算，preview 是首段且超长时以 "…" 收尾。
func TestRenderThinkingCollapsed(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		maxWidth      int
		wantParagraph int  // ×N 里的 N
		wantDots      bool // 尾部省略号
		wantLines     int  // 折叠态恒 1
	}{
		{
			name:          "single short paragraph",
			content:       "this is a short thought",
			maxWidth:      80,
			wantParagraph: 1,
			wantLines:     1,
		},
		{
			name:          "merged paragraphs counted, only the first shown",
			content:       "line1\nline2\nline3",
			maxWidth:      80,
			wantParagraph: 3,
			wantLines:     1,
		},
		{
			name:          "blank lines are not paragraphs",
			content:       "a\n\n  \nb\n",
			maxWidth:      80,
			wantParagraph: 2,
			wantLines:     1,
		},
		{
			name:          "long content truncated",
			content:       strings.Repeat("a", 200),
			maxWidth:      80,
			wantParagraph: 1,
			wantDots:      true,
			wantLines:     1,
		},
		{
			name:          "narrow terminal still one line",
			content:       "hello world hello world hello world",
			maxWidth:      30,
			wantParagraph: 1,
			wantDots:      true,
			wantLines:     1,
		},
	}

	m := newTestModel()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := &logEntry{eventType: "thinking", content: tt.content}
			result := m.renderThinkingBlock(entry, tt.maxWidth)
			plain := stripANSI(result)

			if h := lipgloss.Height(result); h != tt.wantLines {
				t.Errorf("渲染为 %d 行, want %d（折叠态每条目一行）\n%s", h, tt.wantLines, plain)
			}
			if want := "💭 ×" + strconv.Itoa(tt.wantParagraph) + " │ "; !strings.HasPrefix(plain, want) {
				t.Errorf("前缀 = %q, want %q", plain[:min(len(plain), 12)], want)
			}
			if w := lipgloss.Width(result); w > tt.maxWidth-2 {
				t.Errorf("可见宽度 %d 超过内宽 %d", w, tt.maxWidth-2)
			}
			if dots := strings.HasSuffix(plain, "…"); dots != tt.wantDots {
				t.Errorf("尾部省略号 = %v, want %v（%q）", dots, tt.wantDots, plain)
			}
		})
	}
}

// TestRenderThinkingExpanded 钉住展开态契约：逐段编号、段内折行、全文不截断，
// 首行 "  N │ " 与续行 "    │ " 同列（6 列导轨），行宽不越内宽。
func TestRenderThinkingExpanded(t *testing.T) {
	m := newTestModel()
	m.thinkDetailOpen = true

	content := "first thought about the layout\n\nsecond thought is much longer: " +
		strings.Repeat("word ", 40)
	entry := &logEntry{eventType: "thinking", content: content}
	const maxWidth = 80
	got := m.renderThinkingBlock(entry, maxWidth)
	plain := stripANSI(got)

	if !strings.Contains(plain, "  1 │ first thought about the layout") {
		t.Errorf("缺少首段编号导轨\n%s", plain)
	}
	if !strings.Contains(plain, "  2 │ second thought is much longer:") {
		t.Errorf("空行没被当成段落分隔（第二段应编号 2）\n%s", plain)
	}
	if strings.Contains(plain, "…") {
		t.Errorf("展开态不许截断\n%s", plain)
	}
	if !strings.Contains(plain, "    │ ") {
		t.Errorf("折行续行缺少 6 列导轨\n%s", plain)
	}
	for _, line := range strings.Split(got, "\n") {
		if w := lipgloss.Width(line); w > maxWidth-2 {
			t.Errorf("行宽 %d 超过内宽 %d：%q", w, maxWidth-2, stripANSI(line))
		}
	}
}

// TestRebuildContentCache_TrimsStaleContentParts 钉住 rebuildContentCache 的反向自愈：
// contentParts 比 logEntries 长 = 另一批条目的残留缓存（恢复了一个更短的历史），
// 只能整体重排。修前只有"更短 → 补尾部"这一条分支，失配永远留着，下一次排版就越界。
func TestRebuildContentCache_TrimsStaleContentParts(t *testing.T) {
	defer failOnPanic(t)

	m := chromeTestModel(t, 100, 12)
	entries := mixedTranscript()
	seedBodyEntries(m, entries...)

	m.logEntries = append([]logEntry(nil), entries[:3]...)
	m.prevViewportWidth = m.viewport.Width() // 走"宽度没变"的增量分支：残留只有 shrink 分支能修
	m.body.forget()

	m.rebuildContentCache()

	if len(m.contentParts) != len(m.logEntries) {
		t.Fatalf("contentParts=%d，logEntries=%d：更长的残留缓存必须整体重排",
			len(m.contentParts), len(m.logEntries))
	}
	if len(m.dirtyEntryIndices) != 0 {
		t.Errorf("dirtyEntryIndices=%v：整体重排后不该留脏下标", m.dirtyEntryIndices)
	}
	total := m.bodyTotalLines()
	if got := len(strings.Split(m.bodyText(0, total), "\n")); got != total {
		t.Errorf("行数统计与取词不同源：bodyTotalLines=%d 但全文 %d 行", total, got)
	}
}

// TestRebuildContentCache_AppendedDirtyEntryAlignsFirst 钉住 rebuildContentCache 的分支顺序：
// 新条目**同一拍**被追加又被置脏（logEntries 已经 +1、contentParts 还没跟上）时，
// 长度对齐必须先于脏下标循环。修前顺序相反 —— 守卫是 `idx < len(m.logEntries)`，
// 写的却是短一位的 `m.contentParts[idx]`，直接 index out of range。
// 这条测试红在 panic 上，绿在对齐之后：条目一条不许少、正文必须真被渲染、脏集合必须清空。
func TestRebuildContentCache_AppendedDirtyEntryAlignsFirst(t *testing.T) {
	defer failOnPanic(t)

	m := chromeTestModel(t, 100, 12)
	seedBodyEntries(m, mixedTranscript()...)

	// 追加一条新条目，但**不**往 contentParts 里放正文 —— 派生缓存落后一位就是这个形状。
	idx := len(m.logEntries)
	m.logEntries = append(m.logEntries, logEntry{
		eventType: "ai_response", from: "coding", content: "刚追加、同一拍就被置脏的一条",
	})
	m.markEntryDirty(idx)
	// 宽度与上一帧一致，逼出"增量分支"（整段重排会顺带把长度补齐，掩盖顺序问题）。
	m.prevViewportWidth = m.viewport.Width()

	if len(m.contentParts) != len(m.logEntries)-1 {
		t.Fatalf("夹具需要 contentParts 比 logEntries 恰好短 1：%d vs %d",
			len(m.contentParts), len(m.logEntries))
	}
	want := m.renderSingleEntry(&m.logEntries[idx], m.viewport.Width())
	if want == "" {
		t.Fatalf("夹具失效：新条目渲染为空，无法断言正文落进了缓存")
	}

	m.rebuildContentCache()

	if len(m.contentParts) != len(m.logEntries) {
		t.Fatalf("contentParts=%d，logEntries=%d：长度必须先对齐",
			len(m.contentParts), len(m.logEntries))
	}
	if got := m.contentParts[idx]; got != want {
		t.Errorf("新条目正文 = %q，期望渲染结果 %q", got, want)
	}
	if countBodyLines(m.contentParts[idx]) == 0 {
		t.Errorf("新条目按 0 行计：正文没有真正渲染出来")
	}
	if len(m.dirtyEntryIndices) != 0 {
		t.Errorf("dirtyEntryIndices=%v：处理过的脏下标必须清空", m.dirtyEntryIndices)
	}
	if m.needFullRebuild {
		t.Errorf("needFullRebuild 仍为真：增量路径不该留全量标志")
	}
	// 补齐之后的整段正文仍然自洽（硬约束 3.2 ①：行数统计与取词同源）。
	total := m.bodyTotalLines()
	if got := len(strings.Split(m.bodyText(0, total), "\n")); got != total {
		t.Errorf("行数统计与取词不同源：bodyTotalLines=%d 但全文 %d 行", total, got)
	}
	if got := len(m.body.blocks); got != len(m.logEntries) {
		t.Errorf("排版块 = %d，条目 = %d：索引必须按 logEntries 建", got, len(m.logEntries))
	}
}
