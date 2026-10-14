package components

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

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
	// Context=0 的私有模型显示占位符 "-" 而不是空白，保证列对齐。
	// 不能对整段 View() 断言 Contains("-") —— 提示行里的 "1-9 jump" 自带连字符，恒为真。
	// 该行 provider/model 名（private_endpoint / my-custom-llm-v2）共含 3 个连字符，
	// 第 4 个只能来自 context 占位符（截断省略号用 "…"，不贡献 "-"）。
	privateRow := lineContaining(out, "my-custom-llm-v2")
	if strings.Count(privateRow, "-") < 4 {
		t.Errorf("unknown context should render as \"-\", got %q", privateRow)
	}
	// 未指定当前 provider 时不应出现 ACTIVE 标记
	if strings.Contains(out, "ACTIVE") {
		t.Errorf("no active provider configured, badge should be absent\n%s", out)
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

// TestModelSelectDialog_ScrollWindowMath 只钉共享契约表达不了的那一层：
// listRows()/scrollWindow() 的窗口算术（listDialog 接口不含这两个方法，
// 契约文件只能从 View() 的可见性侧覆盖）。游标行可见、"more" 提示、
// 终端尺寸矩阵等可观察行为由 select_dialog_contract_test.go 统一覆盖。
func TestModelSelectDialog_ScrollWindowMath(t *testing.T) {
	const count = 20
	d := NewModelSelectDialog(testModelOptions(count), "")
	d.SetBounds(100, 16)

	rows, showMore := d.listRows()
	if rows >= count {
		t.Fatalf("listRows() = %d, want < %d so scrolling kicks in", rows, count)
	}
	if !showMore {
		t.Error("listRows() showMore = false, want true for a clipped list")
	}

	// 游标移到末行：窗口应贴到列表尾部，即 [count-rows, count)
	for i := 0; i < count-1; i++ {
		d.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	}
	if d.cursor != count-1 {
		t.Fatalf("cursor = %d, want %d", d.cursor, count-1)
	}

	start, end := d.scrollWindow(rows)
	if start != count-rows {
		t.Errorf("scrollWindow start = %d, want %d", start, count-rows)
	}
	if end != count {
		t.Errorf("scrollWindow end = %d, want %d", end, count)
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

// TestModelSelectDialog_PersistFlag 空格确认置位 Persist（永久切换语义，写回
// config.toml）；Enter / 数字直选 / Esc 取消保持 false（仅临时切换）。
func TestModelSelectDialog_PersistFlag(t *testing.T) {
	newDialog := func() *ModelSelectDialog {
		d := NewModelSelectDialog(testModelOptions(9), "")
		d.SetBounds(120, 30)
		return d
	}

	t.Run("空格确认→Persist=true", func(t *testing.T) {
		d := newDialog()
		d.Update(tea.KeyPressMsg{Code: ' '})
		if d.Selected != "autodl_glm53flash" || !d.Persist {
			t.Errorf("space: Selected = %q, Persist = %v, want %q / true",
				d.Selected, d.Persist, "autodl_glm53flash")
		}
	})

	t.Run("Enter确认→Persist=false", func(t *testing.T) {
		d := newDialog()
		d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		if d.Selected != "autodl_glm53flash" || d.Persist {
			t.Errorf("enter: Selected = %q, Persist = %v, want %q / false",
				d.Selected, d.Persist, "autodl_glm53flash")
		}
	})

	t.Run("数字直选→Persist=false", func(t *testing.T) {
		d := newDialog()
		d.Update(tea.KeyPressMsg{Code: '5'})
		if d.Selected != "qn_glm51" || d.Persist {
			t.Errorf("digit: Selected = %q, Persist = %v, want %q / false",
				d.Selected, d.Persist, "qn_glm51")
		}
	})

	t.Run("Esc取消→Persist=false", func(t *testing.T) {
		d := newDialog()
		d.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
		if d.Selected != "" || d.Persist {
			t.Errorf("esc: Selected = %q, Persist = %v, want \"\" / false", d.Selected, d.Persist)
		}
	})
}

// TestModelDialogHint 提示文案层级：含 Space save，窄终端逐级缩短。
func TestModelDialogHint(t *testing.T) {
	multi := modelDialogHint(100, true)
	if !strings.Contains(multi, "1-9 jump") || !strings.Contains(multi, "Space save") {
		t.Errorf("wide multi hint missing parts: %q", multi)
	}
	single := modelDialogHint(100, false)
	if strings.Contains(single, "1-9 jump") {
		t.Errorf("single hint should not advertise number keys: %q", single)
	}
	if !strings.Contains(single, "Space save") {
		t.Errorf("single hint missing Space save: %q", single)
	}
	// 最窄一级仍是单行完整提示
	narrow := modelDialogHint(12, true)
	if strings.Contains(narrow, "\n") {
		t.Errorf("narrow hint should stay single-line: %q", narrow)
	}
	if !strings.Contains(narrow, "Space") {
		t.Errorf("even the narrowest hint should mention Space: %q", narrow)
	}
}
