package components

import (
	"regexp"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSICodes(s string) string { return ansiRE.ReplaceAllString(s, "") }

// testModelOptions 取自真实配置形态：provider 名长短不一、模型名带 org 前缀、
// 上下文窗口从 128k 到 1M，另有一个目录查不到（Context=0）的私有模型。
func testModelOptions(n int) []ModelOption {
	base := []ModelOption{
		{Provider: "autodl_glm53flash", Model: "GLM-5.3-flash", Context: 516000},
		{Provider: "autodl_dsv41_flash", Model: "DeepSeek-V4.1-Flash", Context: 516000},
		{Provider: "localai", Model: "Qwen3.8-27B-GSQ-RCO", Context: 126000},
		{Provider: "qn_dsv4_flash", Model: "deepseek/deepseek-v4-flash", Context: 1000000},
		{Provider: "qn_glm51", Model: "z-ai/glm-5.1", Context: 200000},
		{Provider: "siliconflow_ds4_flash", Model: "deepseek-ai/DeepSeek-V4-Flash", Context: 1000000},
		{Provider: "siliconflow_dsv32", Model: "deepseek-ai/DeepSeek-V3.2", Context: 128000},
		{Provider: "siliconflow_qwen35a3b", Model: "Qwen/Qwen3.5-35B-A3B", Context: 216000},
		{Provider: "private_endpoint", Model: "my-custom-llm-v2", Context: 0},
	}
	if n <= len(base) {
		return base[:n]
	}
	opts := append([]ModelOption{}, base...)
	for i := len(base); i < n; i++ {
		opts = append(opts, ModelOption{Provider: "extra_provider_" + string(rune('a'+i-len(base))), Model: "extra-model", Context: 32768})
	}
	return opts
}

// TestModelSelectDialog_VisualRender 打印渲染结果，供人工核对版式。
func TestModelSelectDialog_VisualRender(t *testing.T) {
	d := NewModelSelectDialog(testModelOptions(9), "siliconflow_dsv32")
	d.SetBounds(120, 30)
	t.Logf("\n%s\n", d.View())

	// 长列表在矮终端下的滚动形态
	d2 := NewModelSelectDialog(testModelOptions(20), "extra_provider_c")
	d2.SetBounds(100, 18)
	t.Logf("\n%s\n", d2.View())
}

// TestModelSelectDialog_ShowsContextSize 每行都应带上上下文窗口列，未知的显示 "-"。
func TestModelSelectDialog_ShowsContextSize(t *testing.T) {
	d := NewModelSelectDialog(testModelOptions(9), "")
	d.SetBounds(120, 30)
	out := stripANSICodes(d.View())

	for _, want := range []string{"CONTEXT", "516.0k", "1.0m", "200.0k", "128.0k", "216.0k", "126.0k"} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered dialog missing %q\n%s", want, out)
		}
	}
	// Context=0 的私有模型显示占位符而不是空白，保证列对齐
	if !strings.Contains(out, "my-custom-llm-v2") || !strings.Contains(out, "-") {
		t.Errorf("unknown context should render as \"-\"\n%s", out)
	}
	// 未指定当前 provider 时不应出现 ACTIVE 标记
	if strings.Contains(out, "ACTIVE") {
		t.Errorf("no active provider configured, badge should be absent\n%s", out)
	}
}

// TestModelSelectDialog_FitsTerminal 渲染高度不得超过终端高度（滚动窗口生效的前提）。
func TestModelSelectDialog_FitsTerminal(t *testing.T) {
	for _, count := range []int{1, 3, 9, 15, 20, 40} {
		for _, size := range [][2]int{{120, 30}, {100, 24}, {80, 20}, {60, 16}, {44, 12}} {
			w, h := size[0], size[1]
			d := NewModelSelectDialog(testModelOptions(count), "")
			d.SetBounds(w, h)
			out := d.View()
			if out == "" {
				continue // 尺寸低于渲染下限
			}
			if got := lipgloss.Height(out); got > h {
				t.Errorf("providers=%d size=%dx%d: rendered height %d exceeds terminal height %d", count, w, h, got, h)
			}
			if got := lipgloss.Width(out); got > w {
				t.Errorf("providers=%d size=%dx%d: rendered width %d exceeds terminal width %d", count, w, h, got, w)
			}
		}
	}
}

// TestModelSelectDialog_InitialCursorOnActive 打开时游标应落在当前生效的 provider 上。
func TestModelSelectDialog_InitialCursorOnActive(t *testing.T) {
	d := NewModelSelectDialog(testModelOptions(9), "qn_glm51")
	if d.cursor != 4 {
		t.Errorf("cursor = %d, want 4 (qn_glm51 的下标)", d.cursor)
	}
	d.SetBounds(120, 30)
	if !strings.Contains(stripANSICodes(d.View()), "✓ ACTIVE") {
		t.Error("active provider should carry the ACTIVE badge")
	}

	// 未知 provider 时回落到首行
	if got := NewModelSelectDialog(testModelOptions(9), "nope").cursor; got != 0 {
		t.Errorf("cursor for unknown provider = %d, want 0", got)
	}
}

// TestModelSelectDialog_Navigation 覆盖 vim 键、翻页、首尾跳转与首尾相接。
func TestModelSelectDialog_Navigation(t *testing.T) {
	newDialog := func() *ModelSelectDialog {
		d := NewModelSelectDialog(testModelOptions(9), "")
		d.SetBounds(120, 30)
		return d
	}
	key := func(d *ModelSelectDialog, code rune) {
		d.Update(tea.KeyPressMsg{Code: code})
	}

	t.Run("j/k 移动", func(t *testing.T) {
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
		key(d, 'j') // 从末行向下应绕回首行
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
		if d.Selected != "autodl_dsv41_flash" {
			t.Errorf("Selected = %q, want %q", d.Selected, "autodl_dsv41_flash")
		}
	})

	t.Run("数字键直选", func(t *testing.T) {
		d := newDialog()
		key(d, '5')
		if d.cursor != 4 || d.Selected != "qn_glm51" {
			t.Errorf("digit 5: cursor = %d, Selected = %q, want 4 / %q", d.cursor, d.Selected, "qn_glm51")
		}
	})

	t.Run("越界数字键忽略", func(t *testing.T) {
		d := NewModelSelectDialog(testModelOptions(3), "")
		d.SetBounds(120, 30)
		key(d, '7')
		if d.Selected != "" || d.cursor != 0 {
			t.Errorf("digit 7 with 3 options: Selected = %q, cursor = %d, want \"\" / 0", d.Selected, d.cursor)
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

// TestModelSelectDialog_Scrolling 列表超出可见行数时应滚动并提示被裁剪的条数，
// 且游标行始终可见。
func TestModelSelectDialog_Scrolling(t *testing.T) {
	d := NewModelSelectDialog(testModelOptions(20), "")
	d.SetBounds(100, 16)

	rows, showMore := d.listRows()
	if rows >= 20 {
		t.Fatalf("listRows() = %d, want < 20 so scrolling kicks in", rows)
	}
	if !showMore {
		t.Error("listRows() showMore = false, want true for a clipped list")
	}

	// 游标移到末尾：最后一条必须可见，并出现向上的 "more" 提示
	for i := 0; i < 19; i++ {
		d.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if d.cursor != 19 {
		t.Fatalf("cursor = %d, want 19", d.cursor)
	}
	out := stripANSICodes(d.View())
	if !strings.Contains(out, "extra_provider_j") {
		t.Errorf("cursor row not visible after scrolling to the end\n%s", out)
	}
	if !strings.Contains(out, "more") {
		t.Errorf("expected a scroll indicator\n%s", out)
	}

	start, end := d.scrollWindow(rows)
	if start != 20-rows {
		t.Errorf("scrollWindow start = %d, want %d", start, 20-rows)
	}
	if end != 20 {
		t.Errorf("scrollWindow end = %d, want 20", end)
	}
}

// TestModelSelectDialog_SingleItem 只有一个 provider 时不应出现滚动提示，也不提示数字键。
func TestModelSelectDialog_SingleItem(t *testing.T) {
	d := NewModelSelectDialog(testModelOptions(1), "")
	d.SetBounds(100, 24)
	out := stripANSICodes(d.View())
	if strings.Contains(out, "more") {
		t.Errorf("single item should not render a scroll indicator\n%s", out)
	}
	if strings.Contains(out, "1-9 jump") {
		t.Errorf("single item should not advertise number-key jump\n%s", out)
	}
}

// TestModelSelectDialog_EmptyAndSmall 空列表或过小的终端返回空串，不 panic。
func TestModelSelectDialog_EmptyAndSmall(t *testing.T) {
	if got := NewModelSelectDialog(nil, "").View(); got != "" {
		t.Errorf("empty options before SetBounds = %q, want \"\"", got)
	}
	d := NewModelSelectDialog(nil, "")
	d.SetBounds(120, 30)
	if got := d.View(); got != "" {
		t.Errorf("empty options = %q, want \"\"", got)
	}
	small := NewModelSelectDialog(testModelOptions(3), "")
	small.SetBounds(20, 30)
	if got := small.View(); got != "" {
		t.Errorf("narrow terminal = %q, want \"\"", got)
	}
}

func TestEllipsize(t *testing.T) {
	tests := []struct {
		in   string
		w    int
		want string
	}{
		{"abc", 6, "abc   "},
		{"abcdef", 6, "abcdef"},
		{"abcdefgh", 6, "abc…gh"},
		{"abc", 0, ""},
		{"", 3, "   "},
		// 同前缀名在窄列下必须仍可区分（中间省略保留尾部）
		{"siliconflow_ds4_flash", 15, "silicon…4_flash"},
		{"siliconflow_dsv32", 15, "silicon…w_dsv32"},
	}
	for _, tt := range tests {
		if got := ellipsize(tt.in, tt.w); got != tt.want {
			t.Errorf("ellipsize(%q, %d) = %q, want %q", tt.in, tt.w, got, tt.want)
		}
		if tt.w > 0 && lipgloss.Width(ellipsize(tt.in, tt.w)) != tt.w {
			t.Errorf("ellipsize(%q, %d) width = %d, want %d", tt.in, tt.w, lipgloss.Width(ellipsize(tt.in, tt.w)), tt.w)
		}
	}
}

func TestAllocateColumns(t *testing.T) {
	// 自然宽度放得下时原样返回
	if p, m := allocateColumns(60, 20, 30); p != 20 || m != 30 {
		t.Errorf("allocateColumns(60,20,30) = %d,%d want 20,30", p, m)
	}
	// 放不下时按 2:3 分配且不溢出总宽
	p, m := allocateColumns(50, 80, 80)
	if p+m > 50 {
		t.Errorf("allocateColumns(50,80,80) = %d,%d exceeds 50", p, m)
	}
	if p < 6 || m < 6 {
		t.Errorf("allocateColumns(50,80,80) = %d,%d below the 6-cell floor", p, m)
	}
}
