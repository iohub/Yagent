package components

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ModelSelectDialog provides a keyboard-navigable list of LLM providers.
// The user can arrow up/down and press Enter to select, or Esc to cancel.
type ModelSelectDialog struct {
	providers     []string          // provider names (keys from config)
	providerDescs map[string]string // provider name → "model_name" description
	cursor        int               // currently highlighted index
	width         int
	height        int
	Selected      string // set to the selected provider name on Enter; "" if cancelled
	CurrentProv   string // currently active provider name (for marking)
	borderStyle   lipgloss.Style
	titleStyle    lipgloss.Style
	itemStyle     lipgloss.Style
	cursorStyle   lipgloss.Style
	currentStyle  lipgloss.Style
	helpStyle     lipgloss.Style
}

// NewModelSelectDialog creates a new model selection dialog.
// providers: list of provider names (from config.GetProviderNames())
// providerDescs: map of provider name → description (e.g., "aliyun/qwen3-coder-plus")
// currentProv: the currently active provider name
func NewModelSelectDialog(providers []string, providerDescs map[string]string, currentProv string) *ModelSelectDialog {
	return &ModelSelectDialog{
		providers:     providers,
		providerDescs: providerDescs,
		cursor:        0,
		Selected:      "",
		CurrentProv:   currentProv,
		borderStyle: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("39")).
			Padding(1, 2),
		titleStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("39")).
			Bold(true),
		itemStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			PaddingLeft(2),
		cursorStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("39")).
			Bold(true).
			PaddingLeft(2),
		currentStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("114")).
			PaddingLeft(2),
		helpStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")),
	}
}

// ID returns the unique identifier for this dialog.
func (d *ModelSelectDialog) ID() string { return "model_select_dialog" }

// Type returns the dialog type.
func (d *ModelSelectDialog) Type() DialogType { return DialogModal }

// Init initializes the component.
func (d *ModelSelectDialog) Init() tea.Cmd { return nil }

// Update processes incoming messages.
func (d *ModelSelectDialog) Update(msg tea.Msg) (Component, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}

	switch keyMsg.String() {
	case "up", "k", "ctrl+p":
		if d.cursor > 0 {
			d.cursor--
		}
		return d, nil
	case "down", "j", "ctrl+n":
		if d.cursor < len(d.providers)-1 {
			d.cursor++
		}
		return d, nil
	case "enter", " ":
		if len(d.providers) > 0 && d.cursor >= 0 && d.cursor < len(d.providers) {
			d.Selected = d.providers[d.cursor]
		}
		return d, nil
	case "esc", "q", "Q":
		d.Selected = "" // cancelled
		return d, nil
	case "ctrl+c":
		return d, tea.Quit
	}

	return d, nil
}

// View renders the dialog as a string.
func (d *ModelSelectDialog) View() string {
	if d.width < 40 || d.height < 5 {
		return ""
	}

	const maxDialogWidth = 55
	dialogWidth := maxDialogWidth
	if d.width-4 < dialogWidth {
		dialogWidth = d.width - 4
	}

	// ── Title ──
	titleLine := d.titleStyle.Render("Select Model Provider (↑↓ navigate, Enter select, Esc cancel)")

	// ── Items ──
	var items []string
	for i, prov := range d.providers {
		desc := prov
		if descStr, ok := d.providerDescs[prov]; ok && descStr != "" {
			desc = descStr
		}

		var line string
		if prov == d.CurrentProv {
			// Current provider - green with checkmark
			marker := "✓"
			if i == d.cursor {
				line = d.cursorStyle.Render("▶ " + marker + " " + desc + " (active)")
			} else {
				line = d.currentStyle.Render("  " + marker + " " + desc + " (active)")
			}
		} else {
			if i == d.cursor {
				line = d.cursorStyle.Render("▶ " + desc)
			} else {
				line = d.itemStyle.Render("  " + desc)
			}
		}
		items = append(items, line)
	}

	itemsStr := strings.Join(items, "\n")

	// ── Dismiss hint ──
	hint := d.helpStyle.Render("↑/↓ navigate · Enter select · Esc cancel")

	// ── Assemble ──
	dialogContent := lipgloss.JoinVertical(lipgloss.Left,
		titleLine,
		"",
		itemsStr,
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
func (d *ModelSelectDialog) IsFocused() bool { return true }

// Focus sets the component as focused.
func (d *ModelSelectDialog) Focus() tea.Cmd { return nil }

// Blur removes focus from this component.
func (d *ModelSelectDialog) Blur() {}

// SetBounds sets the component's dimensions.
func (d *ModelSelectDialog) SetBounds(width, height int) {
	d.width = width
	d.height = height
}

// Bounds returns the component's current width and height.
func (d *ModelSelectDialog) Bounds() (int, int) { return d.width, d.height }

// IsVisible reports whether this component is currently visible.
func (d *ModelSelectDialog) IsVisible() bool { return true }

// SetVisible sets the visibility of this component.
func (d *ModelSelectDialog) SetVisible(v bool) {}

// FormatProviderDesc returns a formatted description string for a provider.
func FormatProviderDesc(name string, model string) string {
	if model == "" {
		return name
	}
	return fmt.Sprintf("%s (model: %s)", name, model)
}
