package components

import (
	"testing"
)

// TestConfirmDialogCreation tests ConfirmDialog constructor.
func TestConfirmDialogCreation(t *testing.T) {
	d := NewConfirmDialog(nil, "run_bash", "ls -la", "Warning message", "", LanguageEn)
	if d.ID() != "confirm_dialog" {
		t.Errorf("expected ID 'confirm_dialog', got %q", d.ID())
	}
	if d.Type() != DialogModal {
		t.Errorf("expected DialogModal, got %v", d.Type())
	}
	if d.Init() != nil {
		t.Error("Init() should return nil")
	}
	if d.GetSelectedResult() != Allow {
		t.Error("default selection should be Allow")
	}
	if d.GetResponseAction() != "allow" {
		t.Errorf("default action should be 'allow', got %q", d.GetResponseAction())
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

// TestHelpDialogCreation tests HelpDialog constructor.
func TestHelpDialogCreation(t *testing.T) {
	d := NewHelpDialog(LanguageEn)
	if d.ID() != "help_dialog" {
		t.Errorf("expected ID 'help_dialog', got %q", d.ID())
	}
	if d.Type() != DialogModal {
		t.Errorf("expected DialogModal, got %v", d.Type())
	}
	if len(d.content.Sections) == 0 {
		t.Error("content should have sections")
	}
	if len(d.content.Title) == 0 {
		t.Error("content title should not be empty")
	}
}

// TestHelpDialogChinese tests HelpDialog with Chinese language.
func TestHelpDialogChinese(t *testing.T) {
	d := NewHelpDialog(LanguageZh)
	if len(d.content.Sections) == 0 {
		t.Error("Chinese content should have sections")
	}
	// Check that content contains Chinese characters (not just ASCII)
	hasChinese := false
	for _, section := range d.content.Sections {
		for _, r := range section.Title {
			if r > 127 {
				hasChinese = true
				break
			}
		}
		if hasChinese {
			break
		}
		for _, item := range section.Items {
			for _, r := range item.Desc {
				if r > 127 {
					hasChinese = true
					break
				}
			}
			if hasChinese {
				break
			}
		}
		if hasChinese {
			break
		}
	}
	if !hasChinese {
		t.Error("Chinese content should contain Chinese characters")
	}
}

// TestDialogStackIntegration tests DialogStack with dialog implementations.
func TestDialogStackIntegration(t *testing.T) {
	stack := NewDialogStack()

	if !stack.IsEmpty() {
		t.Error("new stack should be empty")
	}

	d1 := NewConfirmDialog(nil, "test1", "cmd1", "warn1", "", LanguageEn)
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
