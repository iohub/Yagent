package components

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func newTestResumeDialog() *ResumeDialog {
	return NewResumeDialog(
		"resume_confirm",
		"max_steps",
		"step 30/30",
		[]string{"[ ] 3. [待处理] 更新调用点", "[ ] 4. [待处理] 跑测试"},
		[]string{"delegate_coding", "delegate_repo"},
		LanguageZh,
	)
}

func keyPress(s string) tea.KeyPressMsg {
	switch s {
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEsc}
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	default:
		return tea.KeyPressMsg{Code: rune(s[0])}
	}
}

// stripANSI 移除 SGR 转义序列（lipgloss 样式输出），便于测量可见宽度。
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		if inEsc {
			if r == 'm' {
				inEsc = false
			}
			continue
		}
		if r == '\x1b' {
			inEsc = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func TestResumeDialog_KeyHandling(t *testing.T) {
	t.Run("enter_on_resume_selects_resume", func(t *testing.T) {
		d := newTestResumeDialog()
		d.SelectedIndex = 0
		d.Update(keyPress("enter"))
		if !d.GetConfirmed() {
			t.Error("GetConfirmed = false, want true")
		}
	})

	t.Run("enter_on_browse_selects_browse", func(t *testing.T) {
		d := newTestResumeDialog()
		d.SelectedIndex = 1
		d.Update(keyPress("enter"))
		if d.GetConfirmed() {
			t.Error("GetConfirmed = true, want false (browse only)")
		}
	})

	t.Run("esc_means_browse_only", func(t *testing.T) {
		d := newTestResumeDialog()
		d.Update(keyPress("esc"))
		if d.GetConfirmed() {
			t.Error("GetConfirmed = true after esc, want false")
		}
		if d.SelectedIndex != 1 {
			t.Errorf("SelectedIndex = %d after esc, want 1 (browse)", d.SelectedIndex)
		}
	})

	t.Run("y_shortcut_resumes", func(t *testing.T) {
		d := newTestResumeDialog()
		d.SelectedIndex = 1
		d.Update(keyPress("y"))
		if !d.GetConfirmed() {
			t.Error("GetConfirmed = false after y, want true")
		}
	})

	t.Run("tab_toggles", func(t *testing.T) {
		d := newTestResumeDialog()
		d.Update(keyPress("tab"))
		if d.SelectedIndex != 1 {
			t.Errorf("SelectedIndex = %d after tab, want 1", d.SelectedIndex)
		}
	})
}

func TestResumeDialog_Render(t *testing.T) {
	d := newTestResumeDialog()

	for _, w := range []int{40, 60, 80, 100, 120, 200} {
		d.SetBounds(w, 40)
		view := d.View()
		if view == "" {
			t.Fatalf("View() empty at width %d", w)
		}
		// 内容完整性：标题、原因、步数、计划项、悬空调用、按钮。
		// 仅在 ≥60 宽度断言——40 宽时内宽不足，长行折行会把 token 拆开，
		// 属 lipgloss 的正常 wrap 行为而非缺陷。
		if w >= 60 {
			for _, want := range []string{"断点续跑", "max_steps", "step 30/30", "更新调用点", "delegate_coding", "续跑", "仅浏览"} {
				if !strings.Contains(view, want) {
					t.Errorf("width %d: View() missing %q", w, want)
				}
			}
		}
		// 对话框盒体不得超宽：底边框行（╰──╯）可见宽度应恰为 dialogWidth+2。
		// 注：Place 会把每行撑满整屏宽，故只能校验盒体自身，不能逐行校验。
		dialogWidth := w - 4
		if dialogWidth > 72 {
			dialogWidth = 72
		}
		var borderSeen bool
		for _, line := range strings.Split(view, "\n") {
			plain := stripANSI(line)
			if strings.Contains(plain, "╰") {
				if lw := lipgloss.Width(strings.TrimSpace(plain)); lw != dialogWidth {
					t.Errorf("width %d: bottom border width %d, want %d", w, lw, dialogWidth)
				}
				borderSeen = true
			}
		}
		if !borderSeen {
			t.Errorf("width %d: no bottom border found in view", w)
		}
	}

	// 最小尺寸不渲染
	small := newTestResumeDialog()
	small.SetBounds(30, 40)
	if small.View() != "" {
		t.Error("View() should be empty below min width")
	}
}

func TestResumeDialog_RenderEn(t *testing.T) {
	d := NewResumeDialog("resume_confirm", "cancelled", "", nil, nil, LanguageEn)
	d.SetBounds(100, 30)
	view := d.View()
	for _, want := range []string{"Resume from Breakpoint", "cancelled", "Browse Only"} {
		if !strings.Contains(view, want) {
			t.Errorf("View() missing %q", want)
		}
	}
	// 空计划/悬空段落不应渲染标题行
	if strings.Contains(view, "Unfinished plan items:") {
		t.Error("empty plan items should not render plan header")
	}
	if strings.Contains(view, "Dangling tool calls") {
		t.Error("empty dangling tools should not render dangling header")
	}
}
