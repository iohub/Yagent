package components

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// 两个信息对话框的宽度/高度矩阵：行数与每行宽度都不得超过终端。
// 断言的是精确单元格宽度而不是子串（internal/tui/AGENTS.md 的约定）。
func TestInfoDialog_SizeMatrix(t *testing.T) {
	widths := []int{30, 40, 60, 88, 120, 240}
	heights := []int{18, 24, 40}

	skills := NewSkillsDialog([]SkillRow{
		{Name: "code-review", Description: "对一段 diff 做安全与可读性审查，很长的描述用来触发截断逻辑", Source: "project", FrontMatter: "json"},
		{Name: "release-notes", Description: "从 git log 生成发布说明", Source: "user", FrontMatter: "yaml-subset"},
	})
	mcp := NewMCPDialog([]MCPServerRow{
		{Name: "codeseek", State: "ready", Tools: 9, Curated: 6, Detail: "6 curated, 3 external"},
		{Name: "github", State: "offline", Tools: 12, Detail: "exit status 1"},
	}, "")
	emptySkills := NewSkillsDialog(nil)
	emptyMCP := NewMCPDialog(nil, "mcp: no servers configured")

	cases := []struct {
		name string
		d    *infoDialogBase
	}{
		{"skills", &skills.infoDialogBase},
		{"mcp", &mcp.infoDialogBase},
		{"skills-empty", &emptySkills.infoDialogBase},
		{"mcp-empty", &emptyMCP.infoDialogBase},
	}

	for _, tc := range cases {
		for _, w := range widths {
			for _, h := range heights {
				tc.d.SetBounds(w, h)
				tc.d.offset = 0
				out := tc.d.View()
				if out == "" {
					continue // 尺寸守卫：窄于 40 列或矮于 5 行不渲染
				}
				lines := strings.Split(out, "\n")
				if len(lines) > h {
					t.Errorf("%s w=%d h=%d: 渲染 %d 行，超过终端高度", tc.name, w, h, len(lines))
				}
				for i, line := range lines {
					if got := lipgloss.Width(line); got > w {
						t.Errorf("%s w=%d h=%d: 第 %d 行宽 %d，超过终端宽度", tc.name, w, h, i, got)
					}
				}
			}
		}
	}
}

// 尺寸过小时不渲染，而不是画一个破框（与 model_select_dialog 的守卫同形）。
func TestInfoDialog_SizeGuard(t *testing.T) {
	d := NewSkillsDialog([]SkillRow{{Name: "a", Description: "b", Source: "c", FrontMatter: "none"}})
	d.SetBounds(20, 10)
	if got := d.View(); got != "" {
		t.Errorf("width=20 时渲染了 %q，want 空串", got)
	}
	d.SetBounds(80, 4)
	if got := d.View(); got != "" {
		t.Errorf("height=4 时渲染了 %q，want 空串", got)
	}
}

// 滚动必须能到列表末尾，且滚动指示行不会把总高撑破。
//
// 按的是 HandleInfoKey —— 生产里 model 把 KeyMsg 单独摘出来走 handleDialogStackKey，
// 不会把按键送进对话框的 Update。测 Update 等于钉一条没人走的路（根 AGENTS.md §11 坑 33）。
func TestInfoDialog_ScrollsToEnd(t *testing.T) {
	var rows []SkillRow
	for i := 0; i < 40; i++ {
		rows = append(rows, SkillRow{
			Name: fmt.Sprintf("skill-%02d", i), Description: "d", Source: "user", FrontMatter: "none",
		})
	}
	d := NewSkillsDialog(rows)
	d.SetBounds(100, 24)

	first := d.View()
	if !strings.Contains(first, "skill-00") {
		t.Fatalf("首屏没有第一条：\n%s", first)
	}
	if strings.Contains(first, "skill-39") {
		t.Fatalf("首屏就看见了最后一条，夹具没有触发滚动：\n%s", first)
	}

	for i := 0; i < 50; i++ {
		if d.HandleInfoKey(keyPress("j")) {
			t.Fatal("向下滚动被当成了关闭请求")
		}
	}
	last := d.View()
	if strings.Contains(last, "skill-00") {
		t.Errorf("滚到底之后 skill-00 仍在屏上，滚动没生效：\n%s", last)
	}
	if !strings.Contains(last, "skill-39") {
		t.Errorf("滚到底之后看不见最后一条：\n%s", last)
	}
	if n := len(strings.Split(last, "\n")); n > 24 {
		t.Errorf("滚到底之后 %d 行，超过终端高度", n)
	}

	// 滚动键之外，Esc/q 必须回报"该退了"（Pop 由 model 做）。
	for _, k := range []string{"esc", "q", "Q"} {
		if !d.HandleInfoKey(keyPress(k)) {
			t.Errorf("%q 没有回报关闭", k)
		}
	}
	// 与面板无关的按键既不滚动也不关：交给 model 吞掉，绝不能漏进编辑模式。
	if d.HandleInfoKey(keyPress("x")) {
		t.Error("x 不该关掉面板")
	}
	if !strings.Contains(d.View(), "skill-39") {
		t.Error("x 把滚动窗口挪走了")
	}
}

// MCP 空态必须说清楚"没有 server"，而不是一个空表。
func TestMCPDialog_EmptyExplains(t *testing.T) {
	d := NewMCPDialog(nil, "mcp: no servers configured")
	d.SetBounds(100, 24)
	out := d.View()
	if !strings.Contains(out, "no servers configured") {
		t.Errorf("空态 = %q, want 说明", out)
	}
}
