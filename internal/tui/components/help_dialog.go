package components

import (
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// HelpDialog shows vim-like keybindings help.
type HelpDialog struct {
	width       int
	height      int
	content     string
	borderStyle lipgloss.Style
}

// NewHelpDialog creates a new help dialog with the current language.
func NewHelpDialog(lang Language) *HelpDialog {
	content := getHelpContent(lang)
	return &HelpDialog{
		content: content,
		borderStyle: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("39")).
			Padding(1, 2),
	}
}

// ID returns the unique identifier for this dialog.
func (d *HelpDialog) ID() string { return "help_dialog" }

// Type returns the dialog type.
func (d *HelpDialog) Type() DialogType { return DialogModal }

// Init initializes the component. No setup needed.
func (d *HelpDialog) Init() tea.Cmd { return nil }

// Update processes incoming messages and returns the updated component.
func (d *HelpDialog) Update(msg tea.Msg) (Component, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}

	switch keyMsg.String() {
	case "esc", "i", "I":
		return d, nil // Close handled by model
	case "ctrl+c":
		return d, tea.Quit
	}

	return d, nil
}

// View renders the dialog as a string.
func (d *HelpDialog) View() string {
	if d.width < 50 || d.height < 10 {
		return ""
	}

	const maxDialogWidth = 50
	dialogWidth := maxDialogWidth
	if d.width-4 < dialogWidth {
		dialogWidth = d.width - 4
	}

	// ── Title ──
	titleStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("39")).
		Bold(true)
	titleLine := titleStyle.Render("?  " + "Help")

	// ── Content ──
	contentStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252"))
	renderedContent := contentStyle.Render(d.content)

	// ── Dismiss hint ──
	hintStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240"))
	hint := hintStyle.Render("Press any key to dismiss")

	// ── Assemble ──
	dialogContent := lipgloss.JoinVertical(lipgloss.Left,
		titleLine,
		"",
		renderedContent,
		"",
		hint,
	)

	dialog := d.borderStyle.Width(dialogWidth).Render(dialogContent)

	return lipgloss.Place(d.width, d.height,
		lipgloss.Center, lipgloss.Center,
		dialog,
	)
}

// IsFocused reports whether this dialog has keyboard focus.
func (d *HelpDialog) IsFocused() bool { return true }

// Focus sets the component as focused.
func (d *HelpDialog) Focus() tea.Cmd { return nil }

// Blur removes focus from this component.
func (d *HelpDialog) Blur() {}

// SetBounds sets the component's dimensions.
func (d *HelpDialog) SetBounds(w, h int) { d.width = w; d.height = h }

// Bounds returns the component's current width and height.
func (d *HelpDialog) Bounds() (int, int) { return d.width, d.height }

// IsVisible reports whether this dialog is visible.
func (d *HelpDialog) IsVisible() bool { return true }

// SetVisible sets the visibility of this dialog.
func (d *HelpDialog) SetVisible(bool) {}

// ── Helper function ──

// getHelpContent returns the help dialog content for the given language.
func getHelpContent(lang Language) string {
	if lang == LanguageZh {
		return "  导航:\n" +
			"    j / ↓          向下滚动一行\n" +
			"    k / ↑          向上滚动一行\n" +
			"    f / PageDown    向下翻页\n" +
			"    b / PageUp      向上翻页\n" +
			"    gg              跳到开头\n" +
			"    G               跳到末尾\n" +
			"  模式:\n" +
			"    i               进入编辑模式\n" +
			"    ctrl+e          进入命令模式\n" +
			"  命令行:\n" +
			"    :q              退出程序\n" +
			"    :help           显示命令帮助\n" +
			"    /pattern        搜索日志\n" +
			"  其他:\n" +
			"    ?               显示此帮助\n" +
			"    ctrl+c          强制退出"
	}
	return "  Navigation:\n" +
		"    j / ↓          scroll down one line\n" +
		"    k / ↑          scroll up one line\n" +
		"    f / PageDown   page down\n" +
		"    b / PageUp     page up\n" +
		"    gg             go to top\n" +
		"    G              go to bottom\n" +
		"  Mode:\n" +
		"    i              enter edit mode\n" +
		"    ctrl+e         enter command mode\n" +
		"  Command line:\n" +
		"    :q             quit\n" +
		"    :help          show command help\n" +
		"    /pattern       search log\n" +
		"  Other:\n" +
		"    ?              show this help\n" +
		"    ctrl+c         force quit"
}
