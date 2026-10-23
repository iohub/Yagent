package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// seedOverflowBody 灌入必然溢出的正文（40 段 400 字符的连续长行）。
// 走 SetContent 而不是造条目：这里要钉的是列宽口径，不是条目重排。
func seedOverflowBody(m *model) {
	text := strings.TrimSuffix(strings.Repeat(strings.Repeat("x", 400)+"\n", 120), "\n")
	m.viewport.SetContent(text)
	m.updateViewportCache()
}

// runningWithDashboard 把模型放到"宽屏 + 运行中 + 有用量"的形状上，
// 这是 dashboard 列与滚动条列同时存在、最容易算错的那一档宽度。
func runningWithDashboard(m *model) {
	m.taskRunning = true
	m.dashboardCollapsed = false
	m.inputTokens = 500000
	m.outputTokens = 120000
}

// TestBodyWidthIsOneSource 钉住"视口宽度只有一个口径"：凡是改写 viewport 尺寸
// 的路径（resize、切标签页），写完之后 View 必须原样接受。
// View 一纠正就说明正文刚按另一个宽度渲染过一遍；而旧实现除了纠正还要逐行再补齐
// 一次 —— viewport 自己已经把每一行填到列宽（见 TestViewportViewPadsEveryLine），
// 那是把同一件事做第二遍，并且满宽的行会被再折一次：行数翻倍、footer 顶出屏幕。
func TestBodyWidthIsOneSource(t *testing.T) {
	sizes := []struct{ w, h int }{{120, 40}, {140, 45}, {160, 50}, {200, 60}}
	paths := []struct {
		name string
		run  func(*model)
	}{
		{"resizeViewport", func(m *model) { m.resizeViewport() }},
		{"tab-switch", func(m *model) {
			m.initSessionTabs()
			m.newSessionTabAction()
			m.switchSessionTab(0)
		}},
		{"WindowSizeMsg", func(m *model) {
			m.handleWindowSizeMsg(tea.WindowSizeMsg{Width: m.termWidth, Height: m.termHeight})
		}},
	}

	for _, sz := range sizes {
		for _, p := range paths {
			m := chromeTestModel(t, sz.w, sz.h)
			runningWithDashboard(m)
			seedLog(m, strings.Repeat("log line\n", 200))
			m.View()
			seedOverflowBody(m)

			p.run(m)
			afterSetter := m.viewport.Width()

			content := m.View().Content
			settled := m.viewport.Width()
			if afterSetter != settled {
				t.Errorf("%s path w=%d h=%d: View corrected viewport width %d → %d, 宽度口径不止一处",
					p.name, sz.w, sz.h, afterSetter, settled)
			}
			// viewport 交给 View 的每一行必须已经是这个宽度 —— 拼接才不需要再加工。
			for i, line := range strings.Split(m.cachedViewportView, "\n") {
				if got := lipgloss.Width(line); got != settled {
					t.Errorf("%s path w=%d h=%d cached body line %d: viewport 交付宽度 %d, 实际 %d\n%q",
						p.name, sz.w, sz.h, i, settled, got, stripANSI(line))
					break
				}
			}

			lines := strings.Split(content, "\n")
			vpHeight := sz.h - m.computeFooterHeight() - tabBarHeight
			if vpHeight < 3 {
				vpHeight = 3
			}
			if len(lines) != sz.h {
				t.Errorf("%s path w=%d h=%d: view has %d lines, want %d\n----\n%s----",
					p.name, sz.w, sz.h, len(lines), sz.h, truncateForTest(stripANSI(content)))
				continue
			}
			for i := tabBarHeight; i < tabBarHeight+vpHeight; i++ {
				if got := lipgloss.Width(lines[i]); got != sz.w {
					t.Errorf("%s path w=%d h=%d composed row %d: width=%d, want %d\n%q",
						p.name, sz.w, sz.h, i, got, sz.w, stripANSI(lines[i]))
				}
			}
		}
	}
}

// TestViewportViewPadsEveryLine 是 View 直接按列拼接的前提：
// bubbles viewport 的 View() 必须返回恰好 height 行、每行恰好 width 列。
// 这条第三方行为一旦变化，要补的是显式补齐，而不是回到逐行重排。
func TestViewportViewPadsEveryLine(t *testing.T) {
	for _, w := range []int{39, 64, 89, 140} {
		vp := viewport.New(viewport.WithWidth(w), viewport.WithHeight(5))
		vp.SetContent(strings.Repeat("x", 300) + "\nshort")
		lines := strings.Split(vp.View(), "\n")
		if len(lines) != 5 {
			t.Fatalf("width %d: viewport returned %d lines, want 5", w, len(lines))
		}
		for i, line := range lines {
			if got := lipgloss.Width(line); got != w {
				t.Errorf("width %d line %d: width=%d, want %d\n%q", w, i, got, w, line)
			}
		}
	}
}
