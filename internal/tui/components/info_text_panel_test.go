package components

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// TextPanel 是 `/plasma`、`/proposals` 的共用输出面。它只借 infoDialogBase 的几何与
// 按键，所以**必须**跟着那套矩阵一起被钉住：多行文本一旦换行，行数就不再等于条目数，
// 而超出终端的那几行在 AltScreen 里既看不见也滚不回去（internal/tui/AGENTS.md 硬约束 11）。

func textPanelLines(n int) []string {
	lines := make([]string, 0, n)
	for i := 0; i < n; i++ {
		// 超长行 + 中文混排：Ellipsize 必须按显示宽度截断而不是按 rune 数。
		lines = append(lines, fmt.Sprintf("%02d  fixtures.structural[%d]  PLS-501 结构夹具失败 %s",
			i, i, strings.Repeat("很长的说明", 12)))
	}
	return lines
}

func TestTextPanel_SizeMatrix(t *testing.T) {
	widths := []int{30, 40, 60, 88, 120, 240}
	heights := []int{5, 18, 24, 40}
	cases := map[string]*TextPanel{
		"rows":  NewTextPanel("t1", "◆ plasma", textPanelLines(30), ""),
		"one":   NewTextPanel("t2", "◆ plasma", textPanelLines(1), ""),
		"empty": NewTextPanel("t3", "◆ plasma", nil, ""),
	}
	for name, p := range cases {
		for _, w := range widths {
			for _, h := range heights {
				p.SetBounds(w, h)
				out := p.View()
				if out == "" {
					continue // 尺寸守卫：窄于 40 列或矮于 5 行不渲染（与 /skills 同一套）
				}
				lines := strings.Split(out, "\n")
				if len(lines) > h {
					t.Errorf("%s w=%d h=%d: 渲染 %d 行，超过终端高度", name, w, h, len(lines))
				}
				for i, line := range lines {
					if got := lipgloss.Width(line); got > w {
						t.Errorf("%s w=%d h=%d: 第 %d 行宽 %d，超过终端宽度", name, w, h, i, got)
					}
				}
			}
		}
	}
}

// 面板必须能滚到底：这是"多行输出不进日志条目"这条选择的前提 ——
// 放不下的内容得能看见，否则等于换个方式截断。
func TestTextPanel_ScrollsToEnd(t *testing.T) {
	p := NewTextPanel("t", "◆ proposals", textPanelLines(40), "")
	p.SetBounds(100, 24)

	first := p.View()
	if !strings.Contains(first, "00  fixtures") {
		t.Fatalf("首屏没有第一条：\n%s", first)
	}
	for i := 0; i < 60; i++ {
		if p.HandleInfoKey(keyPress("j")) {
			t.Fatal("向下滚动被当成了关闭请求")
		}
	}
	last := p.View()
	if !strings.Contains(last, "39  fixtures") {
		t.Errorf("滚到底之后看不见最后一条：\n%s", last)
	}
	if n := len(strings.Split(last, "\n")); n > 24 {
		t.Errorf("滚到底之后 %d 行，超过终端高度", n)
	}
	// Esc 是"该退了"，Pop 由 model 做（与 /skills、/mcp 同形）。
	if !p.HandleInfoKey(keyPress("esc")) {
		t.Error("Esc 没有被当成关闭请求")
	}
}

// 空内容不许画一个空框：那是"面板坏了"与"真的没有内容"无法区分的形态。
func TestTextPanel_EmptyPlaceholderAndSetLines(t *testing.T) {
	p := NewTextPanel("t", "◆ plasma", nil, "")
	if got := p.Lines(); len(got) != 1 || got[0] != "（空）" {
		t.Fatalf("空内容 Lines() = %v, want 一行占位", got)
	}

	p.SetLines([]string{"a", "b"})
	if got := p.Lines(); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("SetLines 后 Lines() = %v, want [a b]", got)
	}
	// 换一批行必须把滚动位置复位：停在旧 offset 上会让人以为新内容"只剩一半"。
	p.SetBounds(100, 24)
	p.HandleInfoKey(keyPress("j"))
	p.SetLines([]string{"x"})
	if p.offset != 0 {
		t.Errorf("SetLines 后 offset = %d, want 0", p.offset)
	}
	if got := p.Lines(); len(got) != 1 || got[0] != "x" {
		t.Errorf("SetLines 后 Lines() = %v, want [x]", got)
	}
}

// 实现 InfoPanel 才会被 model 的按键分发自动认领（漏了它，Esc 关不掉面板）。
func TestTextPanel_ImplementsInfoPanel(t *testing.T) {
	var panel InfoPanel = NewTextPanel("t", "◆ plasma", []string{"x"}, "")
	if panel.ID() != "t" {
		t.Errorf("ID() = %q, want t", panel.ID())
	}
}
