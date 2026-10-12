package components

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func timeoutInfo() TimeoutInfo {
	return TimeoutInfo{
		CurrentLabel: "10 minutes",
		CurrentRaw:   "10m",
		Options: []TimeoutOption{
			{Value: "1m", Desc: "Fast"},
			{Value: "5m", Desc: "Conservative"},
			{Value: "10m", Desc: "Default"},
			{Value: "30m", Desc: "Long"},
		},
	}
}

func TestLLMTimeoutDialog_VisualRender(t *testing.T) {
	d := NewLLMTimeoutDialog(timeoutInfo())
	d.SetBounds(120, 30)
	t.Logf("\n%s\n", d.View())
}

func TestLLMTimeoutDialog_CursorStartsOnCurrent(t *testing.T) {
	d := NewLLMTimeoutDialog(timeoutInfo())
	if d.cursor != 2 {
		t.Fatalf("cursor = %d, want 2 (CurrentRaw=10m)", d.cursor)
	}

	// 无匹配时游标回落到 0。
	d2 := NewLLMTimeoutDialog(TimeoutInfo{
		CurrentLabel: "7 minutes",
		CurrentRaw:   "7m",
		Options:      timeoutInfo().Options,
	})
	if d2.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 (no match)", d2.cursor)
	}
}

func TestLLMTimeoutDialog_EnterSelectsTemporarily(t *testing.T) {
	d := NewLLMTimeoutDialog(timeoutInfo())
	d.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // 10m → 30m
	d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if d.Selected != "30m" || d.Persist {
		t.Errorf("Enter → Selected=%q Persist=%v, want 30m/false", d.Selected, d.Persist)
	}
}

func TestLLMTimeoutDialog_SpaceSelectsAndPersists(t *testing.T) {
	d := NewLLMTimeoutDialog(timeoutInfo())
	d.Update(tea.KeyPressMsg{Code: ' '})
	if d.Selected != "10m" || !d.Persist {
		t.Errorf("Space → Selected=%q Persist=%v, want 10m/true", d.Selected, d.Persist)
	}
}

func TestLLMTimeoutDialog_EscCancels(t *testing.T) {
	d := NewLLMTimeoutDialog(timeoutInfo())
	d.Update(tea.KeyPressMsg{Code: ' '}) // 先选中，再取消
	d.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if d.Selected != "" || d.Persist {
		t.Errorf("Esc → Selected=%q Persist=%v, want empty/false", d.Selected, d.Persist)
	}
	for _, k := range []rune{'q', 'Q'} {
		d2 := NewLLMTimeoutDialog(timeoutInfo())
		d2.Update(tea.KeyPressMsg{Code: k})
		if d2.Selected != "" {
			t.Errorf("%q → Selected=%q, want empty", k, d2.Selected)
		}
	}
}

func TestLLMTimeoutDialog_UpdownCycle(t *testing.T) {
	d := NewLLMTimeoutDialog(timeoutInfo()) // cursor=2
	d.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if d.cursor != 1 {
		t.Errorf("after up cursor = %d, want 1", d.cursor)
	}
	d.Update(tea.KeyPressMsg{Code: 'k'})
	if d.cursor != 0 {
		t.Errorf("after k cursor = %d, want 0", d.cursor)
	}
	d.Update(tea.KeyPressMsg{Code: tea.KeyUp}) // 0 → 3（循环）
	if d.cursor != 3 {
		t.Errorf("after wrap up cursor = %d, want 3", d.cursor)
	}
	d.Update(tea.KeyPressMsg{Code: tea.KeyDown}) // 3 → 0（循环）
	if d.cursor != 0 {
		t.Errorf("after wrap down cursor = %d, want 0", d.cursor)
	}
	d.Update(tea.KeyPressMsg{Code: 'j'})
	if d.cursor != 1 {
		t.Errorf("after j cursor = %d, want 1", d.cursor)
	}
	d.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if d.cursor != 2 {
		t.Errorf("after tab cursor = %d, want 2", d.cursor)
	}
}

func TestLLMTimeoutDialog_NumberJump(t *testing.T) {
	d := NewLLMTimeoutDialog(timeoutInfo())
	for _, c := range []rune{'1', '3', '4'} {
		d.Update(tea.KeyPressMsg{Code: c})
	}
	if d.cursor != 3 {
		t.Errorf("after digits 1/3/4 cursor = %d, want 3", d.cursor)
	}
	// 越界数字不改变游标。
	d.Update(tea.KeyPressMsg{Code: '9'})
	if d.cursor != 3 {
		t.Errorf("digit 9 (out of range) cursor = %d, want 3 (unchanged)", d.cursor)
	}
}

func TestLLMTimeoutDialog_MovementIgnoredAfterSelection(t *testing.T) {
	// 选中后移动游标不应影响已选值。
	d := NewLLMTimeoutDialog(timeoutInfo())
	d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	d.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if d.Selected != "10m" {
		t.Errorf("Selected = %q, want 10m (unchanged after cursor move)", d.Selected)
	}
}

func TestLLMTimeoutDialog_EmptyOptionsNoPanic(t *testing.T) {
	d := NewLLMTimeoutDialog(TimeoutInfo{CurrentLabel: "10m", CurrentRaw: ""})
	d.SetBounds(120, 30)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic with empty options: %v", r)
		}
	}()
	for _, k := range []tea.KeyPressMsg{
		{Code: tea.KeyDown}, {Code: tea.KeyUp}, {Code: tea.KeyEnter},
		{Code: ' '}, {Code: tea.KeyEscape}, {Code: 'j'}, {Code: '1'},
	} {
		d.Update(k)
	}
	if d.Selected != "" {
		t.Errorf("empty options: Selected = %q, want empty (enter/space must not select)", d.Selected)
	}
}

func TestLLMTimeoutDialog_SizeGuard(t *testing.T) {
	d := NewLLMTimeoutDialog(timeoutInfo())
	d.SetBounds(39, 30)
	if out := d.View(); out != "" {
		t.Errorf("width 39: expected empty view (guard <40), got content")
	}
	d.SetBounds(120, 7)
	if out := d.View(); out != "" {
		t.Errorf("height 7: expected empty view (guard <8), got content")
	}
	d.SetBounds(120, 30)
	out := stripANSICodes(d.View())
	for _, want := range []string{"LLM Call Timeout", "CURRENT: 10 minutes", "1m", "10m", "Default", "✓ CURRENT"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q\n%s", want, out)
		}
	}
}

// TestLLMTimeoutDialog_WidthMatrix 确保多种宽度下折行/截断不超过宽度预算。
func TestLLMTimeoutDialog_WidthMatrix(t *testing.T) {
	for _, w := range []int{40, 60, 80, 100, 120, 200} {
		d := NewLLMTimeoutDialog(timeoutInfo())
		d.SetBounds(w, 24)
		for _, line := range strings.Split(stripANSICodes(d.View()), "\n") {
			if len([]rune(line)) > w {
				t.Errorf("(w=%d) line exceeds width: %d chars", w, len([]rune(line)))
			}
		}
	}
}
