package components

import (
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// testConfigEntries 取自真实形态：global + 7 个 agent + deepthinking 工具，
// provider/model 长短不一，并混入一个未解析出 provider 的目标。
func testConfigEntries(n int) []ConfigEntry {
	base := []ConfigEntry{
		{Target: "global", Group: TargetGroupGlobal, Provider: "autodl_glm53flash", Model: "GLM-5.3-flash", Context: 516000},
		{Target: "director", Group: TargetGroupAgents, Provider: "autodl_glm53flash", Model: "GLM-5.3-flash", Context: 516000},
		{Target: "coding", Group: TargetGroupAgents, Provider: "siliconflow_ds4_flash", Model: "deepseek-ai/DeepSeek-V4-Flash", Context: 1000000},
		{Target: "repo", Group: TargetGroupAgents, Provider: "siliconflow_dsv32", Model: "deepseek-ai/DeepSeek-V3.2", Context: 128000},
		{Target: "chat", Group: TargetGroupAgents, Provider: "qn_glm51", Model: "z-ai/glm-5.1", Context: 200000},
		{Target: "meta", Group: TargetGroupAgents, Provider: "", Model: "", Context: 0},
		{Target: "devops", Group: TargetGroupAgents, Provider: "localai", Model: "Qwen3.8-27B-GSQ-RCO", Context: 126000},
		{Target: "browser", Group: TargetGroupAgents, Provider: "private_endpoint", Model: "my-custom-llm-v2", Context: 0},
		{Target: "deepthinking", Group: TargetGroupTools, Provider: "xiaomi", Model: "mimo-v2-flash", Context: 262144},
	}
	if n <= len(base) {
		return base[:n]
	}
	entries := append([]ConfigEntry{}, base...)
	for i := len(base); i < n; i++ {
		entries = append(entries, ConfigEntry{
			Target:   "custom_agent_" + strconv.Itoa(i),
			Group:    TargetGroupAgents,
			Provider: "extra_provider",
			Model:    "extra-model",
			Context:  32768,
		})
	}
	return entries
}

// TestAgentSelectDialog_VisualRender 打印渲染结果，供人工核对版式。
func TestAgentSelectDialog_VisualRender(t *testing.T) {
	d := NewAgentSelectDialog(testConfigEntries(9))
	d.SetBounds(120, 30)
	t.Logf("\n%s\n", d.View())

	// 长列表在矮终端下的滚动形态
	d2 := NewAgentSelectDialog(testConfigEntries(24))
	d2.cursor = 22
	d2.SetBounds(100, 18)
	t.Logf("\n%s\n", d2.View())

	// 窄终端
	d3 := NewAgentSelectDialog(testConfigEntries(9))
	d3.SetBounds(60, 24)
	t.Logf("\n%s\n", d3.View())
}

// TestAgentSelectDialog_ShowsGroupsAndContext 组标题、上下文列与占位符都应正确渲染。
func TestAgentSelectDialog_ShowsGroupsAndContext(t *testing.T) {
	d := NewAgentSelectDialog(testConfigEntries(9))
	d.SetBounds(120, 30)
	out := stripANSICodes(d.View())

	for _, want := range []string{
		"Select Target to Change Model",
		"TARGET", "PROVIDER", "MODEL", "CONTEXT",
		"GLOBAL", "AGENTS", "TOOLS",
		"516.0k", "1.0m", "128.0k", "200.0k", "126.0k", "262.1k",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered dialog missing %q\n%s", want, out)
		}
	}
	// 未解析出 provider/model 或上下文未知的目标显示占位符，保证列对齐
	metaRow := lineContaining(out, "meta")
	if strings.Count(metaRow, "-") < 3 {
		t.Errorf("unset provider/model/context should render as \"-\", got %q", metaRow)
	}
}

// lineContaining 返回渲染结果中第一个包含 s 的行。
func lineContaining(out, s string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, s) {
			return line
		}
	}
	return ""
}

// TestAgentSelectDialog_FocusedRowKeepsTargetName 焦点行与非焦点行的目标名必须一致，
// 旧实现焦点行会把目标名换成 provider，移动游标时首列内容会跳变。
func TestAgentSelectDialog_FocusedRowKeepsTargetName(t *testing.T) {
	d := NewAgentSelectDialog(testConfigEntries(9))
	d.SetBounds(120, 30)

	before := stripANSICodes(d.View())
	d.Update(tea.KeyPressMsg{Code: 'j'})
	after := stripANSICodes(d.View())

	for _, target := range []string{"global", "director", "coding", "repo", "chat", "meta", "devops", "browser", "deepthinking"} {
		if strings.Count(before, target) != strings.Count(after, target) {
			t.Errorf("target %q occurrence changed after moving the cursor: %d → %d\n%s",
				target, strings.Count(before, target), strings.Count(after, target), after)
		}
	}
}

// TestAgentSelectDialog_FitsTerminal 渲染尺寸不得超过终端尺寸（含组标题与滚动指示行）。
func TestAgentSelectDialog_FitsTerminal(t *testing.T) {
	for _, count := range []int{1, 3, 9, 15, 24, 40} {
		for _, size := range [][2]int{{120, 30}, {100, 24}, {80, 20}, {60, 16}, {44, 12}} {
			w, h := size[0], size[1]
			d := NewAgentSelectDialog(testConfigEntries(count))
			d.SetBounds(w, h)
			out := d.View()
			if out == "" {
				continue // 尺寸低于渲染下限
			}
			if got := lipgloss.Height(out); got > h {
				t.Errorf("entries=%d size=%dx%d: rendered height %d exceeds terminal height %d", count, w, h, got, h)
			}
			if got := lipgloss.Width(out); got > w {
				t.Errorf("entries=%d size=%dx%d: rendered width %d exceeds terminal width %d", count, w, h, got, w)
			}
		}
	}
}

// TestAgentSelectDialog_Navigation 覆盖 vim 键、数字直选、确认与取消。
func TestAgentSelectDialog_Navigation(t *testing.T) {
	newDialog := func() *AgentSelectDialog {
		d := NewAgentSelectDialog(testConfigEntries(9))
		d.SetBounds(120, 30)
		return d
	}
	key := func(d *AgentSelectDialog, code rune) {
		d.Update(tea.KeyPressMsg{Code: code})
	}

	t.Run("j/k 移动并跳过组标题", func(t *testing.T) {
		d := newDialog()
		key(d, 'j')
		key(d, 'j')
		if d.cursor != 2 {
			t.Errorf("cursor = %d, want 2", d.cursor)
		}
		key(d, 'k')
		if d.cursor != 1 {
			t.Errorf("cursor = %d, want 1", d.cursor)
		}
	})

	t.Run("首尾相接", func(t *testing.T) {
		d := newDialog()
		key(d, 'k') // 从首行向上应绕到末行
		if d.cursor != 8 {
			t.Errorf("wrap up: cursor = %d, want 8", d.cursor)
		}
		key(d, 'j')
		if d.cursor != 0 {
			t.Errorf("wrap down: cursor = %d, want 0", d.cursor)
		}
	})

	t.Run("g/G 跳转首尾", func(t *testing.T) {
		d := newDialog()
		key(d, 'G')
		if d.cursor != 8 {
			t.Errorf("G: cursor = %d, want 8", d.cursor)
		}
		key(d, 'g')
		if d.cursor != 0 {
			t.Errorf("g: cursor = %d, want 0", d.cursor)
		}
	})

	t.Run("方向键与 Enter 确认", func(t *testing.T) {
		d := newDialog()
		key(d, tea.KeyDown)
		key(d, tea.KeyEnter)
		if d.Selected != "director" {
			t.Errorf("Selected = %q, want %q", d.Selected, "director")
		}
	})

	t.Run("数字键直选", func(t *testing.T) {
		d := newDialog()
		key(d, '9')
		if d.cursor != 8 || d.Selected != "deepthinking" {
			t.Errorf("digit 9: cursor = %d, Selected = %q, want 8 / %q", d.cursor, d.Selected, "deepthinking")
		}
	})

	t.Run("越界数字键忽略", func(t *testing.T) {
		d := NewAgentSelectDialog(testConfigEntries(3))
		d.SetBounds(120, 30)
		key(d, '7')
		if d.Selected != "" || d.cursor != 0 {
			t.Errorf("digit 7 with 3 entries: Selected = %q, cursor = %d, want \"\" / 0", d.Selected, d.cursor)
		}
	})

	t.Run("Esc 取消", func(t *testing.T) {
		d := newDialog()
		key(d, '3')
		key(d, tea.KeyEscape)
		if d.Selected != "" {
			t.Errorf("Selected = %q, want \"\" after Esc", d.Selected)
		}
	})
}

// TestAgentSelectDialog_Scrolling 行序列（含组标题）超出可见行数时应滚动，
// 且游标行始终可见。
func TestAgentSelectDialog_Scrolling(t *testing.T) {
	d := NewAgentSelectDialog(testConfigEntries(24))
	d.SetBounds(100, 16)

	all := d.buildRows()
	if len(all) <= len(d.entries) {
		t.Fatalf("buildRows() = %d rows, want more than %d entries (group headers missing)", len(all), len(d.entries))
	}
	rows, showMore := listBudget(d.height, len(all))
	if !showMore {
		t.Error("listBudget showMore = false, want true for a clipped list")
	}

	// 游标移到末尾：最后一条必须可见，并出现 "more" 提示
	for i := 0; i < 23; i++ {
		d.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if d.cursor != 23 {
		t.Fatalf("cursor = %d, want 23", d.cursor)
	}
	out := stripANSICodes(d.View())
	if !strings.Contains(out, "custom_agent_23") {
		t.Errorf("cursor row not visible after scrolling to the end\n%s", out)
	}
	if !strings.Contains(out, "more") {
		t.Errorf("expected a scroll indicator\n%s", out)
	}

	start, end := windowRange(len(all), d.focusRow(all), rows)
	if end != len(all) {
		t.Errorf("windowRange end = %d, want %d", end, len(all))
	}
	if start >= end {
		t.Errorf("windowRange = [%d,%d), want a non-empty window", start, end)
	}
}

// TestAgentSelectDialog_SingleItem 只有一个目标时不应出现滚动提示，也不提示数字键。
func TestAgentSelectDialog_SingleItem(t *testing.T) {
	d := NewAgentSelectDialog(testConfigEntries(1))
	d.SetBounds(100, 24)
	out := stripANSICodes(d.View())
	if strings.Contains(out, "more") {
		t.Errorf("single entry should not render a scroll indicator\n%s", out)
	}
	if strings.Contains(out, "1-9 jump") {
		t.Errorf("single entry should not advertise number-key jump\n%s", out)
	}
	if !strings.Contains(out, "global") {
		t.Errorf("single entry should still render its target name\n%s", out)
	}
}

// TestAgentSelectDialog_EmptyAndSmall 空列表或过小的终端返回空串，不 panic。
func TestAgentSelectDialog_EmptyAndSmall(t *testing.T) {
	if got := NewAgentSelectDialog(nil).View(); got != "" {
		t.Errorf("empty entries before SetBounds = %q, want \"\"", got)
	}
	d := NewAgentSelectDialog(nil)
	d.SetBounds(120, 30)
	if got := d.View(); got != "" {
		t.Errorf("empty entries = %q, want \"\"", got)
	}
	// 空列表下按键不应 panic，游标保持在 0
	d.Update(tea.KeyPressMsg{Code: 'j'})
	d.Update(tea.KeyPressMsg{Code: 'G'})
	if d.cursor != 0 {
		t.Errorf("cursor on empty entries = %d, want 0", d.cursor)
	}

	small := NewAgentSelectDialog(testConfigEntries(3))
	small.SetBounds(20, 30)
	if got := small.View(); got != "" {
		t.Errorf("narrow terminal = %q, want \"\"", got)
	}
}
