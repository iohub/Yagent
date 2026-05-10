package components

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// QuitConfirmDialog is a generic quit/cancel confirmation dialog.
// It supports two options (Yes/No) with keyboard navigation.
type QuitConfirmDialog struct {
	DialogID      string
	Title         string
	YesLabel      string
	NoLabel       string
	SelectedIndex int // 0=Yes, 1=No
	Confirmed     bool
	width         int
	height        int
	borderStyle   lipgloss.Style
	titleStyle    lipgloss.Style
	messageStyle  lipgloss.Style
	focusedBtn    lipgloss.Style
	blurredBtn    lipgloss.Style
	helpStyle     lipgloss.Style
}

// NewQuitConfirmDialog creates a new quit/cancel confirmation dialog.
func NewQuitConfirmDialog(id, title, yes, no string, borderColor string) *QuitConfirmDialog {
	return &QuitConfirmDialog{
		DialogID:      id,
		Title:         title,
		YesLabel:      yes,
		NoLabel:       no,
		SelectedIndex: 0,
		borderStyle: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(borderColor)).
			Padding(0, 2),
		titleStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color(borderColor)).
			Bold(true),
		messageStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			MaxWidth(50),
		focusedBtn: lipgloss.NewStyle().
			Foreground(lipgloss.Color("0")).
			Background(lipgloss.Color(borderColor)).
			Bold(true).
			Padding(0, 2),
		blurredBtn: lipgloss.NewStyle().
			Foreground(lipgloss.Color("244")).
			Padding(0, 2),
		helpStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")),
	}
}

// ID returns the unique identifier for this dialog.
func (d *QuitConfirmDialog) ID() string { return d.DialogID }

// Type returns the dialog type.
func (d *QuitConfirmDialog) Type() DialogType { return DialogModal }

// Init initializes the component. No setup needed.
func (d *QuitConfirmDialog) Init() tea.Cmd { return nil }

// Update processes incoming messages and returns the updated component.
func (d *QuitConfirmDialog) Update(msg tea.Msg) (Component, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}

	switch keyMsg.String() {
	case "right", "tab":
		d.SelectedIndex = (d.SelectedIndex + 1) % 2
		return d, nil
	case "left":
		d.SelectedIndex = (d.SelectedIndex + 1) % 2
		return d, nil
	case "enter":
		if d.SelectedIndex == 0 {
			d.Confirmed = true
		}
		return d, nil
	case "y", "Y":
		d.Confirmed = true
		return d, nil
	case "n", "N", "esc":
		d.Confirmed = false
		d.SelectedIndex = 0
		return d, nil
	case "ctrl+c":
		return d, tea.Quit
	}

	return d, nil
}

// View renders the dialog as a string.
func (d *QuitConfirmDialog) View() string {
	if d.width < 40 || d.height < 5 {
		return ""
	}

	const maxDialogWidth = 48
	dialogWidth := maxDialogWidth
	if d.width-4 < dialogWidth {
		dialogWidth = d.width - 4
	}
	innerWidth := dialogWidth - 4

	// ── Title ──
	titleLine := d.titleStyle.Render(d.Title)

	// ── Message ──
	message := d.messageStyle.Render(d.NoLabel) // reusing NoLabel area as message placeholder
	_ = message

	// ── Buttons (2 options) ──
	renderBtn := func(label string, idx int) string {
		if d.SelectedIndex == idx {
			return d.focusedBtn.Render(label)
		}
		return d.blurredBtn.Render(label)
	}
	buttons := lipgloss.JoinHorizontal(lipgloss.Center,
		renderBtn(d.YesLabel, 0),
		"  ",
		renderBtn(d.NoLabel, 1),
	)

	// ── Help ──
	help := d.helpStyle.Render("←/→ 选择  Enter 确认  y/n")

	// ── Separator ──
	sep := lipgloss.NewStyle().
		Foreground(lipgloss.Color("237")).
		Width(innerWidth).
		Render(strings.Repeat("─", innerWidth))

	// ── Assemble ──
	content := lipgloss.JoinVertical(lipgloss.Left,
		titleLine,
		"",
		message,
		"",
		sep,
		"",
		lipgloss.NewStyle().Width(innerWidth).Align(lipgloss.Center).Render(buttons),
		"",
		help,
	)

	dialog := d.borderStyle.Width(dialogWidth).Render(content)

	return lipgloss.Place(d.width, d.height,
		lipgloss.Center, lipgloss.Center,
		dialog,
	)
}

// IsFocused reports whether this dialog has keyboard focus.
func (d *QuitConfirmDialog) IsFocused() bool { return true }

// Focus sets the component as focused.
func (d *QuitConfirmDialog) Focus() tea.Cmd { return nil }

// Blur removes focus from this component.
func (d *QuitConfirmDialog) Blur() {}

// SetBounds sets the component's dimensions.
func (d *QuitConfirmDialog) SetBounds(w, h int) { d.width = w; d.height = h }

// Bounds returns the component's current width and height.
func (d *QuitConfirmDialog) Bounds() (int, int) { return d.width, d.height }

// IsVisible reports whether this dialog is visible.
func (d *QuitConfirmDialog) IsVisible() bool { return true }

// SetVisible sets the visibility of this dialog.
func (d *QuitConfirmDialog) SetVisible(bool) {}

// GetConfirmed returns whether the user confirmed the action.
func (d *QuitConfirmDialog) GetConfirmed() bool { return d.Confirmed }

// SetConfirmed sets the confirmation result.
func (d *QuitConfirmDialog) SetConfirmed(confirmed bool) { d.Confirmed = confirmed }

// ── Factory methods for specific dialog types ──

// NewQuitConfirmDialogForQuit creates a quit confirmation dialog.
func NewQuitConfirmDialogForQuit(lang Language) *QuitConfirmDialog {
	var title, yes, no string
	if lang == LanguageZh {
		title = "退出程序"
		yes = "是"
		no = "否"
	} else {
		title = "Quit Program"
		yes = "Yes"
		no = "No"
	}
	return NewQuitConfirmDialog("quit_confirm", title, yes, no, "167")
}

// NewQuitConfirmDialogForCancel creates a cancel task confirmation dialog.
func NewQuitConfirmDialogForCancel(lang Language) *QuitConfirmDialog {
	var title, yes, no string
	if lang == LanguageZh {
		title = "取消任务"
		yes = "确定"
		no = "取消"
	} else {
		title = "Cancel Task"
		yes = "Yes"
		no = "No"
	}
	return NewQuitConfirmDialog("cancel_confirm", title, yes, no, "214")
}
