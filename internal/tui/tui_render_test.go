package tui

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TestRenderThinkingContinuation 钉住续行渲染契约：8 列缩进前缀 + 单行内容，
// 显示宽度按 maxWidth-10 预算（下限 20）截断并以 "..." 结尾，整行可见宽度精确可算。
func TestRenderThinkingContinuation(t *testing.T) {
	tests := []struct {
		name          string
		content       string
		maxWidth      int
		wantTruncated bool
		wantWidth     int // lipgloss.Width of the whole rendered line
	}{
		{
			name:      "short content",
			content:   "this is a short thought",
			maxWidth:  80,
			wantWidth: 8 + len("this is a short thought"),
		},
		{
			name:      "content with newlines collapsed to spaces",
			content:   "line1\nline2\nline3",
			maxWidth:  80,
			wantWidth: 8 + len("line1 line2 line3"),
		},
		{
			name:          "long content truncated",
			content:       strings.Repeat("a", 200),
			maxWidth:      80,
			wantTruncated: true,
			// indent 8 + (80-10) 列内容（含 3 列省略号）
			wantWidth: 8 + 70,
		},
		{
			name:      "narrow width keeps content under the 20-cell floor",
			content:   "hello world",
			maxWidth:  30,
			wantWidth: 8 + len("hello world"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			entry := logEntry{content: tt.content}
			result := renderThinkingContinuation(entry, tt.maxWidth)
			plain := stripANSI(result)

			if !strings.HasPrefix(result, "      │ ") {
				t.Errorf("prefix = %q, want \"      │ \"", result[:min(len(result), 8)])
			}
			if h := lipgloss.Height(result); h != 1 {
				t.Errorf("渲染为 %d 行, want 1（续行必须单行）", h)
			}
			if got := lipgloss.Width(result); got != tt.wantWidth {
				t.Errorf("可见宽度 = %d, want %d（%q）", got, tt.wantWidth, plain)
			}
			if w := lipgloss.Width(result); w > tt.maxWidth {
				t.Errorf("可见宽度 %d 超过 maxWidth %d", w, tt.maxWidth)
			}
			if truncated := strings.HasSuffix(plain, "..."); truncated != tt.wantTruncated {
				t.Errorf("尾部省略号 = %v, want %v（%q）", truncated, tt.wantTruncated, plain)
			}
		})
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
