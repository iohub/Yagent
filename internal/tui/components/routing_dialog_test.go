package components

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func enabledRoutingInfo() RoutingInfo {
	return RoutingInfo{
		Enabled: true,
		Mode:    "auto",
		Tiers: []RoutingTierView{
			{Label: "HIGH", Provider: "deepseek_v4_pro", Model: "deepseek-v4-pro", Score: 312.5},
			{Label: "BALANCED", Provider: "aliyun", Model: "qwen3-coder-plus", Score: 180.0},
			{Label: "FAST", Provider: "local", Model: "Qwen3-30B-A3B", Score: 95.0},
		},
	}
}

func TestRoutingDialog_VisualRender(t *testing.T) {
	d := NewRoutingDialog(enabledRoutingInfo())
	d.SetBounds(120, 30)
	t.Logf("\n%s\n", d.View())

	// 禁用形态：展示原因而非三档。
	d2 := NewRoutingDialog(RoutingInfo{
		Enabled: false,
		Mode:    "auto",
		Reason:  "insufficient scorable candidates: need at least 3 configured providers",
	})
	d2.SetBounds(100, 24)
	t.Logf("\n%s\n", d2.View())
}

func TestRoutingDialog_ShowsTiersAndScores(t *testing.T) {
	d := NewRoutingDialog(enabledRoutingInfo())
	d.SetBounds(120, 30)
	out := stripANSICodes(d.View())

	for _, want := range []string{"HIGH", "BALANCED", "FAST", "deepseek-v4-pro", "qwen3-coder-plus", "Qwen3-30B-A3B", "312.5", "180.0", "95.0"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q\n%s", want, out)
		}
	}
}

func TestRoutingDialog_DisabledShowsReason(t *testing.T) {
	d := NewRoutingDialog(RoutingInfo{Enabled: false, Mode: "auto", Reason: "empty benchmark intersection"})
	d.SetBounds(110, 24)
	out := stripANSICodes(d.View())
	if !strings.Contains(out, "empty benchmark intersection") {
		t.Errorf("expected disable reason in view:\n%s", out)
	}
	if strings.Contains(out, "312.5") {
		t.Error("disabled dialog should not show tier scores")
	}
}

func TestRoutingDialog_ModeSelectAndPersist(t *testing.T) {
	d := NewRoutingDialog(enabledRoutingInfo()) // Mode=auto → cursor starts on AUTO
	d.SetBounds(120, 30)

	if d.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 (AUTO)", d.cursor)
	}

	// ↓ 移动到 FIXED，Enter 临时应用。
	d.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if d.cursor != 1 {
		t.Fatalf("after down cursor = %d, want 1", d.cursor)
	}
	d.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if d.Selected != "fixed" || d.Persist {
		t.Errorf("Enter → Selected=%q Persist=%v, want fixed/false", d.Selected, d.Persist)
	}

	// 重新打开，Space 应用并持久化。
	d2 := NewRoutingDialog(enabledRoutingInfo())
	d2.SetBounds(120, 30)
	d2.Update(tea.KeyPressMsg{Code: tea.KeyEnter}) // AUTO, temp
	if d2.Selected != "auto" || d2.Persist {
		t.Errorf("Enter on AUTO → Selected=%q Persist=%v, want auto/false", d2.Selected, d2.Persist)
	}
	d3 := NewRoutingDialog(enabledRoutingInfo())
	d3.SetBounds(120, 30)
	d3.Update(tea.KeyPressMsg{Code: ' '})
	if d3.Selected != "auto" || !d3.Persist {
		t.Errorf("Space → Selected=%q Persist=%v, want auto/true", d3.Selected, d3.Persist)
	}
}

func TestRoutingDialog_EscCancels(t *testing.T) {
	d := NewRoutingDialog(enabledRoutingInfo())
	d.SetBounds(120, 30)
	d.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if d.Selected != "" {
		t.Errorf("Esc → Selected=%q, want empty (cancelled)", d.Selected)
	}
}

// TestRoutingDialog_SizeMatrix 确保在多种终端尺寸下都不 panic 且不超过宽度预算。
func TestRoutingDialog_SizeMatrix(t *testing.T) {
	widths := []int{40, 60, 80, 100, 120, 200}
	heights := []int{8, 12, 20, 30, 50}
	for _, w := range widths {
		for _, h := range heights {
			d := NewRoutingDialog(enabledRoutingInfo())
			d.SetBounds(w, h)
			out := d.View()
			if w < 40 || h < 8 {
				if out != "" {
					t.Errorf("(%dx%d) expected empty view, got content", w, h)
				}
				continue
			}
			for _, line := range strings.Split(stripANSICodes(out), "\n") {
				if len([]rune(line)) > w {
					t.Errorf("(%dx%d) line exceeds width: %d chars", w, h, len([]rune(line)))
				}
			}
		}
	}
}
