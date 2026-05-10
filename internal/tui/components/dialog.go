package components

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// DialogType defines the type of dialog.
type DialogType int

const (
	DialogNormal DialogType = iota // Normal dialog
	DialogModal                     // Modal dialog, intercepts all events
	DialogToast                     // Toast dialog, auto-closes after short display
)

// Dialog is a dialog component interface.
type Dialog interface {
	Component
	ID() string
	Type() DialogType
}

// DialogStack manages a stack of dialogs.
// The last element in the slice is the top of the stack (most recently added).
type DialogStack struct {
	dialogs     []Dialog
	overlayBg   string // overlay background color
}

// NewDialogStack creates an empty DialogStack.
func NewDialogStack() *DialogStack {
	return &DialogStack{
		dialogs:   make([]Dialog, 0),
		overlayBg: "62", // dim gray by default
	}
}

// Len returns the number of dialogs in the stack.
func (ds *DialogStack) Len() int {
	return len(ds.dialogs)
}

// IsEmpty reports whether the stack is empty.
func (ds *DialogStack) IsEmpty() bool {
	return len(ds.dialogs) == 0
}

// Push adds a dialog to the top of the stack.
func (ds *DialogStack) Push(d Dialog) {
	ds.dialogs = append(ds.dialogs, d)
}

// Pop removes and returns the top dialog from the stack.
// Returns nil if the stack is empty.
func (ds *DialogStack) Pop() Dialog {
	if len(ds.dialogs) == 0 {
		return nil
	}
	d := ds.dialogs[len(ds.dialogs)-1]
	ds.dialogs = ds.dialogs[:len(ds.dialogs)-1]
	return d
}

// Top returns the top dialog without removing it.
// Returns nil if the stack is empty.
func (ds *DialogStack) Top() Dialog {
	if len(ds.dialogs) == 0 {
		return nil
	}
	return ds.dialogs[len(ds.dialogs)-1]
}

// CloseDialog finds and removes the dialog with the given ID.
// Returns true if the dialog was found and removed, false otherwise.
func (ds *DialogStack) CloseDialog(id string) bool {
	for i, d := range ds.dialogs {
		if d.ID() == id {
			ds.dialogs = append(ds.dialogs[:i], ds.dialogs[i+1:]...)
			return true
		}
	}
	return false
}

// Clear removes all dialogs from the stack.
func (ds *DialogStack) Clear() {
	ds.dialogs = ds.dialogs[:0]
}

// ReplaceTop replaces the top dialog on the stack with a new one.
// Returns false if the stack is empty, true otherwise.
func (ds *DialogStack) ReplaceTop(d Dialog) bool {
	if len(ds.dialogs) == 0 {
		return false
	}
	ds.dialogs[len(ds.dialogs)-1] = d
	return true
}

// All returns a copy of all dialogs in the stack.
func (ds *DialogStack) All() []Dialog {
	result := make([]Dialog, len(ds.dialogs))
	copy(result, ds.dialogs)
	return result
}

// Update routes the message to the top dialog.
// Returns the Cmd from the top dialog, or nil if the stack is empty.
func (ds *DialogStack) Update(msg tea.Msg) (tea.Cmd, Dialog) {
	if len(ds.dialogs) == 0 {
		return nil, nil
	}

	top := ds.dialogs[len(ds.dialogs)-1]
	if top == nil {
		return nil, nil
	}

	// Route message to top dialog
	newComp, cmd := top.Update(msg)
	if newComp != nil {
		// Cast back to Dialog if the component is a Dialog
		if dialog, ok := newComp.(Dialog); ok {
			ds.dialogs[len(ds.dialogs)-1] = dialog
		}
	}

	return cmd, top
}

// Overlay renders the overlay (background mask + all dialogs in the stack).
// Later-rendered dialogs cover earlier ones, creating a Z-index effect.
func (ds *DialogStack) Overlay(maxWidth, maxHeight int) string {
	if len(ds.dialogs) == 0 {
		return ""
	}

	// Build background overlay
	overlayStyle := lipgloss.NewStyle().
		Width(maxWidth).
		Height(maxHeight).
		Background(lipgloss.Color("236")).
		Align(lipgloss.Center, lipgloss.Center)

	background := overlayStyle.Render("")

	// Render each dialog on top (later dialogs cover earlier ones)
	var dialogLines []string
	for _, d := range ds.dialogs {
		if d == nil || !d.IsVisible() {
			continue
		}
		content := d.View()
		if content != "" {
			dialogLines = append(dialogLines, content)
		}
	}

	if len(dialogLines) == 0 {
		return background
	}

	// Join dialog content vertically
	dialogsContent := strings.Join(dialogLines, "\n")

	// Place dialogs centered over the background
	result := lipgloss.Place(maxWidth, maxHeight,
		lipgloss.Center, lipgloss.Center,
		dialogsContent,
	)

	return result
}
