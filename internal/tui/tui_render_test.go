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
