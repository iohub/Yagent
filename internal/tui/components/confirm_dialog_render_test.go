package components

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"codeactor/internal/tui/common"
)

// TestConfirmDialogVisualRender_Chinese tests the ConfirmDialog rendering with Chinese language.
func TestConfirmDialogVisualRender_Chinese(t *testing.T) {
	t.Log("━━━ 中文版授权界面 ━━━")

	styles := common.NewStyles(common.ThemeDark)
	d := NewConfirmDialog(styles, "SystemCommand", "rm -rf /tmp/test_cache",
		"此操作可能影响工作空间外的文件或系统环境", "req-auth-001", LanguageZh)

	d.SetBounds(80, 24)
	rendered := d.View()

	if rendered == "" {
		t.Fatal("rendered output is empty")
	}
	t.Logf("\n%s\n", rendered)
}

// TestConfirmDialogVisualRender_English tests the ConfirmDialog rendering with English language.
func TestConfirmDialogVisualRender_English(t *testing.T) {
	t.Log("━━━ English Version Auth Dialog ━━━")

	styles := common.NewStyles(common.ThemeDark)
	d := NewConfirmDialog(styles, "SystemCommand", "rm -rf /tmp/test_cache",
		"This operation may affect files outside the workspace", "req-auth-002", LanguageEn)

	d.SetBounds(80, 24)
	rendered := d.View()

	if rendered == "" {
		t.Fatal("rendered output is empty")
	}
	t.Logf("\n%s\n", rendered)
}

// TestConfirmDialogNavigation tests the keyboard navigation (up/down) in ConfirmDialog.
func TestConfirmDialogNavigation(t *testing.T) {
	styles := common.NewStyles(common.ThemeDark)
	d := NewConfirmDialog(styles, "SystemCommand", "rm -rf /tmp/test_cache",
		"此操作可能影响工作空间外的文件或系统环境", "req-auth-nav", LanguageZh)

	d.SetBounds(80, 24)

	// Test cases for navigation
	tests := []struct {
		name     string
		key      tea.KeyMsg
		expected int
	}{
		{"初始状态 (默认 Allow)", nil, 0},
		{"按 ↓ 后 (AllowTool 选中)", tea.KeyPressMsg{Code: tea.KeyDown}, 1},
		{"按 ↓ 后 (AllowSession 选中)", tea.KeyPressMsg{Code: tea.KeyDown}, 2},
		{"按 ↓ 后 (AllowProject 选中)", tea.KeyPressMsg{Code: tea.KeyDown}, 3},
		{"按 ↓ 后 (Deny 选中)", tea.KeyPressMsg{Code: tea.KeyDown}, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.key != nil {
				_, _ = d.Update(tt.key)
			}
			rendered := d.View()
			if rendered == "" {
				t.Fatal("rendered output is empty")
			}
			t.Logf("\n▶ %s\n%s\n", tt.name, rendered)
			if d.selectedIndex != tt.expected {
				t.Errorf("selectedIndex = %d, want %d", d.selectedIndex, tt.expected)
			}
		})
	}
}

// TestConfirmDialogShortcuts tests the keyboard shortcuts for jumping to specific options.
func TestConfirmDialogShortcuts(t *testing.T) {
	styles := common.NewStyles(common.ThemeDark)
	d := NewConfirmDialog(styles, "SystemCommand", "rm -rf /tmp/test_cache",
		"此操作可能影响工作空间外的文件或系统环境", "req-auth-shortcut", LanguageZh)

	d.SetBounds(80, 24)

	tests := []struct {
		name     string
		key      tea.KeyMsg
		expected int
	}{
		{"按 'a' → Allow", tea.KeyPressMsg{Code: 'a'}, 0},
		{"按 't' → AllowTool", tea.KeyPressMsg{Code: 't'}, 1},
		{"按 's' → AllowSession", tea.KeyPressMsg{Code: 's'}, 2},
		{"按 'p' → AllowProject", tea.KeyPressMsg{Code: 'p'}, 3},
		{"按 'd' → Deny", tea.KeyPressMsg{Code: 'd'}, 4},
		{"按 'esc' → Deny", tea.KeyPressMsg{Code: tea.KeyEsc}, 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _ = d.Update(tt.key)
			rendered := d.View()
			if rendered == "" {
				t.Fatal("rendered output is empty")
			}
			if d.selectedIndex != tt.expected {
				t.Errorf("selectedIndex = %d, want %d", d.selectedIndex, tt.expected)
			}
			t.Logf("\n▶ %s\n%s\n", tt.name, rendered)
		})
	}
}

// TestConfirmDialogAllOptionsRendered verifies that all 5 options appear in the rendered output.
func TestConfirmDialogAllOptionsRendered(t *testing.T) {
	styles := common.NewStyles(common.ThemeDark)
	d := NewConfirmDialog(styles, "SystemCommand", "rm -rf /tmp/test_cache",
		"此操作可能影响工作空间外的文件或系统环境", "req-auth-icons", LanguageZh)

	d.SetBounds(80, 24)
	rendered := d.View()

	if rendered == "" {
		t.Fatal("rendered output is empty")
	}

	// Check all labels are present
	expectedLabels := []string{"允许 (本次)", "允许 (本工具)", "允许 (本次会话全部)", "允许 (本项目全部)", "拒绝"}
	for _, label := range expectedLabels {
		if !strings.Contains(rendered, label) {
			t.Errorf("expected label %q not found in rendered output", label)
		}
	}

	// Check radio button symbols are present
	if !strings.Contains(rendered, "◉") {
		t.Error("expected focused radio button ◉ not found")
	}
	if !strings.Contains(rendered, "○") {
		t.Error("expected unfocused radio button ○ not found")
	}

	t.Logf("\n━━━ 所有选项渲染测试 ━━━\n%s\n", rendered)
}
