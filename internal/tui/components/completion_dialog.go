package components

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// TaskCompleteDialog is the task completion/failure dialog.
type TaskCompleteDialog struct {
	Success   bool
	Message   string
	Closed    bool
	width     int
	height    int
	borderStyle lipgloss.Style
	titleStyle  lipgloss.Style
	buttonStyle lipgloss.Style
	helpStyle   lipgloss.Style
}

// NewTaskCompleteDialog creates a new task completion dialog.
func NewTaskCompleteDialog(success bool, message string, lang Language) *TaskCompleteDialog {
	// Build title based on language and success state
	if lang == LanguageZh {
		_ = message // message used in View()
	} else {
		_ = message // message used in View()
	}

	return &TaskCompleteDialog{
		Success: success,
		Message: message,
		borderStyle: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("36")). // cyan-green for success
			Padding(0, 2),
		titleStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("36")). // cyan
			Bold(true),
		buttonStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("0")).
			Background(lipgloss.Color("36")). // cyan bg
			Bold(true).
			Padding(0, 4),
		helpStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")),
	}
}

// ID returns the unique identifier for this dialog.
func (d *TaskCompleteDialog) ID() string { return "task_complete" }

// Type returns the dialog type.
func (d *TaskCompleteDialog) Type() DialogType { return DialogModal }

// Init initializes the component. No setup needed.
func (d *TaskCompleteDialog) Init() tea.Cmd { return nil }

// Update processes incoming messages and returns the updated component.
func (d *TaskCompleteDialog) Update(msg tea.Msg) (Component, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}

	switch keyMsg.String() {
	case "enter", " ", "esc":
		d.Closed = true
		return d, nil
	case "ctrl+c":
		return d, tea.Quit
	}

	return d, nil
}

// View renders the dialog as a string.
func (d *TaskCompleteDialog) View() string {
	if d.width < 40 || d.height < 5 {
		return ""
	}

	const maxDialogWidth = 40
	dialogWidth := maxDialogWidth
	if d.width-4 < dialogWidth {
		dialogWidth = d.width - 4
	}
	innerWidth := dialogWidth - 4

	// ── Title ──
	titleLine := d.titleStyle.Render("✓ " + "Task Completed")

	// ── Message ──
	var msgLine string
	if d.Message != "" {
		msgLine = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			MaxWidth(50).
			Render(d.Message)
	} else {
		msgLine = ""
	}

	// ── OK Button ──
	okBtn := d.buttonStyle.Render("OK")

	// ── Help text ──
	help := d.helpStyle.Render("Press ENTER or SPACE to close")

	// ── Separator ──
	sep := lipgloss.NewStyle().
		Foreground(lipgloss.Color("237")).
		Width(innerWidth).
		Render(strings.Repeat("─", innerWidth))

	// ── Assemble ──
	var content string
	if msgLine != "" {
		content = lipgloss.JoinVertical(lipgloss.Left,
			titleLine,
			"",
			msgLine,
			"",
			sep,
			"",
			lipgloss.NewStyle().Width(innerWidth).Align(lipgloss.Center).Render(okBtn),
			"",
			help,
		)
	} else {
		content = lipgloss.JoinVertical(lipgloss.Left,
			titleLine,
			"",
			sep,
			"",
			lipgloss.NewStyle().Width(innerWidth).Align(lipgloss.Center).Render(okBtn),
			"",
			help,
		)
	}

	dialog := d.borderStyle.Width(dialogWidth).Render(content)

	return lipgloss.Place(d.width, d.height,
		lipgloss.Center, lipgloss.Center,
		dialog,
	)
}

// IsFocused reports whether this dialog has keyboard focus.
func (d *TaskCompleteDialog) IsFocused() bool { return true }

// Focus sets the component as focused.
func (d *TaskCompleteDialog) Focus() tea.Cmd { return nil }

// Blur removes focus from this component.
func (d *TaskCompleteDialog) Blur() {}

// SetBounds sets the component's dimensions.
func (d *TaskCompleteDialog) SetBounds(w, h int) { d.width = w; d.height = h }

// Bounds returns the component's current width and height.
func (d *TaskCompleteDialog) Bounds() (int, int) { return d.width, d.height }

// IsVisible reports whether this dialog is visible.
func (d *TaskCompleteDialog) IsVisible() bool { return true }

// SetVisible sets the visibility of this dialog.
func (d *TaskCompleteDialog) SetVisible(bool) {}

// WasClosed returns whether the dialog has been closed by the user.
func (d *TaskCompleteDialog) WasClosed() bool { return d.Closed }
