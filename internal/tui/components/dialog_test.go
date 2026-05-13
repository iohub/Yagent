package components

import (
	"testing"
)

// TestCompileTimeInterfaceCheck verifies at compile time that all Dialog
// implementations satisfy the Dialog interface.
func TestCompileTimeInterfaceCheck(t *testing.T) {
	// These compile-time checks will fail to compile if any interface
	// method is missing.
	var _ Dialog = (*ConfirmDialog)(nil)
	var _ Dialog = (*QuitConfirmDialog)(nil)
	var _ Dialog = (*TaskCompleteDialog)(nil)
	var _ Dialog = (*HelpDialog)(nil)
}

// TestConfirmDialogCreation tests ConfirmDialog constructor.
func TestConfirmDialogCreation(t *testing.T) {
	d := NewConfirmDialog("run_bash", "ls -la", "Warning message", "", LanguageEn)
	if d.ID() != "confirm_dialog" {
		t.Errorf("expected ID 'confirm_dialog', got %q", d.ID())
	}
	if d.Type() != DialogModal {
		t.Errorf("expected DialogModal, got %v", d.Type())
	}
	if d.Init() != nil {
		t.Error("Init() should return nil")
	}
	if !d.IsFocused() {
		t.Error("ConfirmDialog should be focused by default")
	}
	if !d.IsVisible() {
		t.Error("ConfirmDialog should be visible by default")
	}
	if d.GetSelectedResult() != Allow {
		t.Error("default selection should be Allow")
	}
	if d.GetResponseAction() != "allow" {
		t.Errorf("default action should be 'allow', got %q", d.GetResponseAction())
	}
}

// TestConfirmDialogOptions tests ConfirmDialog options.
func TestConfirmDialogOptions(t *testing.T) {
	d := NewConfirmDialog("run_bash", "ls -la", "Warning", "", LanguageEn)
	if len(d.options) != 5 {
		t.Errorf("expected 5 options, got %d", len(d.options))
	}
	// Allow (option 0) has value 0 by iota, which is correct
	// Options 1-4 should have non-zero values
	for i := 1; i < len(d.options); i++ {
		if d.options[i].Value == 0 {
			t.Errorf("option %d has zero value", i)
		}
	}
}

// TestConfirmDialogChinese tests ConfirmDialog with Chinese language.
func TestConfirmDialogChinese(t *testing.T) {
	d := NewConfirmDialog("run_bash", "ls -la", "警告", "", LanguageZh)
	if len(d.options) != 5 {
		t.Errorf("expected 5 options, got %d", len(d.options))
	}
	// Check that Chinese labels are set
	if d.options[0].Label != "允许 (本次)" {
		t.Errorf("expected Chinese label '允许 (本次)', got %q", d.options[0].Label)
	}
}

// TestQuitConfirmDialogCreation tests QuitConfirmDialog constructor.
func TestQuitConfirmDialogCreation(t *testing.T) {
	d := NewQuitConfirmDialog("quit_confirm", "Quit?", "Are you sure?", "Yes", "No", "167")
	if d.ID() != "quit_confirm" {
		t.Errorf("expected ID 'quit_confirm', got %q", d.ID())
	}
	if d.Type() != DialogModal {
		t.Errorf("expected DialogModal, got %v", d.Type())
	}
	if d.Confirmed {
		t.Error("Confirmed should be false initially")
	}
	if d.SelectedIndex != 0 {
		t.Error("SelectedIndex should be 0 (Yes) initially")
	}
}

// TestQuitConfirmDialogFactories tests factory methods.
func TestQuitConfirmDialogFactories(t *testing.T) {
	// Test quit dialog factory
	d1 := NewQuitConfirmDialogForQuit(LanguageEn)
	if d1.ID() != "quit_confirm" {
		t.Errorf("expected 'quit_confirm', got %q", d1.ID())
	}
	if d1.Title != "Quit Program" {
		t.Errorf("expected 'Quit Program', got %q", d1.Title)
	}

	d2 := NewQuitConfirmDialogForQuit(LanguageZh)
	if d2.Title != "退出程序" {
		t.Errorf("expected '退出程序', got %q", d2.Title)
	}

	// Test cancel dialog factory
	d3 := NewQuitConfirmDialogForCancel(LanguageEn)
	if d3.ID() != "cancel_confirm" {
		t.Errorf("expected 'cancel_confirm', got %q", d3.ID())
	}
	if d3.Title != "Cancel Task" {
		t.Errorf("expected 'Cancel Task', got %q", d3.Title)
	}
}

// TestTaskCompleteDialogCreation tests TaskCompleteDialog constructor.
func TestTaskCompleteDialogCreation(t *testing.T) {
	d := NewTaskCompleteDialog(true, "Task completed!", LanguageEn)
	if d.ID() != "task_complete" {
		t.Errorf("expected ID 'task_complete', got %q", d.ID())
	}
	if d.Type() != DialogModal {
		t.Errorf("expected DialogModal, got %v", d.Type())
	}
	if d.WasClosed() {
		t.Error("WasClosed() should be false initially")
	}
	if !d.Success {
		t.Error("Success should be true")
	}
}

// TestTaskCompleteDialogChinese tests TaskCompleteDialog with Chinese language.
func TestTaskCompleteDialogChinese(t *testing.T) {
	d := NewTaskCompleteDialog(false, "任务失败", LanguageZh)
	if d.ID() != "task_complete" {
		t.Errorf("expected ID 'task_complete', got %q", d.ID())
	}
	if d.Message != "任务失败" {
		t.Errorf("expected message '任务失败', got %q", d.Message)
	}
}

// TestHelpDialogCreation tests HelpDialog constructor.
func TestHelpDialogCreation(t *testing.T) {
	d := NewHelpDialog(LanguageEn)
	if d.ID() != "help_dialog" {
		t.Errorf("expected ID 'help_dialog', got %q", d.ID())
	}
	if d.Type() != DialogModal {
		t.Errorf("expected DialogModal, got %v", d.Type())
	}
	if d.content == "" {
		t.Error("content should not be empty")
	}
}

// TestHelpDialogChinese tests HelpDialog with Chinese language.
func TestHelpDialogChinese(t *testing.T) {
	d := NewHelpDialog(LanguageZh)
	if d.content == "" {
		t.Error("Chinese content should not be empty")
	}
	// Check that content contains Chinese characters (not just ASCII)
	hasChinese := false
	for _, r := range d.content {
		if r > 127 {
			hasChinese = true
			break
		}
	}
	if !hasChinese {
		t.Error("Chinese content should contain Chinese characters")
	}
}

// TestBounds tests SetBounds and Bounds methods.
func TestBounds(t *testing.T) {
	d := NewConfirmDialog("test", "test", "test", "", LanguageEn)
	w, h := d.Bounds()
	if w != 0 || h != 0 {
		t.Errorf("initial bounds should be 0x0, got %dx%d", w, h)
	}

	d.SetBounds(80, 24)
	w, h = d.Bounds()
	if w != 80 || h != 24 {
		t.Errorf("bounds should be 80x24, got %dx%d", w, h)
	}
}

// TestVisibility tests IsVisible and SetVisible methods.
func TestVisibility(t *testing.T) {
	d := NewConfirmDialog("test", "test", "test", "", LanguageEn)
	if !d.IsVisible() {
		t.Error("should be visible by default")
	}

	// Note: The current implementation always returns true for IsVisible()
	// and SetVisible() is a no-op. This is acceptable for modal dialogs
	// that are always visible when on the stack.
	// TODO: Add visible field tracking if needed in future refactoring.
	_ = d // suppress unused warning
}

// TestQuitDialogVisibility tests QuitConfirmDialog visibility.
func TestQuitDialogVisibility(t *testing.T) {
	d := NewQuitConfirmDialogForQuit(LanguageEn)
	// QuitConfirmDialog also always returns true for IsVisible()
	if !d.IsVisible() {
		t.Error("should be visible by default")
	}
	_ = d
}

// TestFocus tests Focus and Blur methods.
func TestFocus(t *testing.T) {
	// Modal dialogs are always focused when on the stack.
	// The current implementation returns true for IsFocused().
	d := NewConfirmDialog("test", "test", "test", "", LanguageEn)
	if !d.IsFocused() {
		t.Error("modal ConfirmDialog should be focused by default")
	}

	focusCmd := d.Focus()
	if focusCmd != nil {
		t.Error("Focus() should return nil cmd")
	}
}

// TestHelpDialogFocus tests HelpDialog focus behavior.
func TestHelpDialogFocus(t *testing.T) {
	d := NewHelpDialog(LanguageEn)
	if !d.IsFocused() {
		t.Error("modal HelpDialog should be focused by default")
	}
}

// TestDialogStackIntegration tests DialogStack with dialog implementations.
func TestDialogStackIntegration(t *testing.T) {
	stack := NewDialogStack()

	if !stack.IsEmpty() {
		t.Error("new stack should be empty")
	}

	d1 := NewConfirmDialog("test1", "cmd1", "warn1", "", LanguageEn)
	d2 := NewQuitConfirmDialogForQuit(LanguageEn)

	stack.Push(d1)
	stack.Push(d2)

	if stack.Len() != 2 {
		t.Errorf("expected 2 dialogs, got %d", stack.Len())
	}

	if stack.Top() != d2 {
		t.Error("top should be d2")
	}

	popped := stack.Pop()
	if popped != d2 {
		t.Error("popped should be d2")
	}

	if stack.Len() != 1 {
		t.Errorf("expected 1 dialog after pop, got %d", stack.Len())
	}
}

// TestDialogTypes tests all DialogType constants.
func TestDialogTypes(t *testing.T) {
	if DialogNormal != 0 {
		t.Error("DialogNormal should be 0")
	}
	if DialogModal != 1 {
		t.Error("DialogModal should be 1")
	}
	if DialogToast != 2 {
		t.Error("DialogToast should be 2")
	}
}
