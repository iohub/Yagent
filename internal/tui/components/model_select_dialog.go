package components

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"codeactor/internal/tui/common"
)

// ModelSelectDialog provides a keyboard-navigable list of LLM providers.
// The user can arrow up/down and press Enter to select, or Esc to cancel.
type ModelSelectDialog struct {
	providers     []string          // provider names (keys from config)
	providerDescs map[string]string // provider name → formatted description (e.g., "deepseek (model: deepseek-chat)")
	cursor        int               // currently highlighted index
	width         int
	height        int
	Selected      string // set to the selected provider name on Enter; "" if cancelled
	CurrentProv   string // currently active provider name (for marking)
}

// NewModelSelectDialog creates a new model selection dialog.
// styles: the shared Styles instance for design system access.
// providers: list of provider names (from config.GetProviderNames())
// providerDescs: map of provider name → description (e.g., "deepseek (model: deepseek-chat)")
// currentProv: the currently active provider name
func NewModelSelectDialog(styles *common.Styles, providers []string, providerDescs map[string]string, currentProv string) *ModelSelectDialog {
	return &ModelSelectDialog{
		providers:     providers,
		providerDescs: providerDescs,
		cursor:        0,
		Selected:      "",
		CurrentProv:   currentProv,
	}
}

// extractModelName parses the model name from a FormatProviderDesc string.
// Input: "deepseek (model: deepseek-chat)" → "deepseek-chat"
// Input: "deepseek" → ""
func extractModelName(desc string) string {
	const prefix = "(model: "
	idx := strings.Index(desc, prefix)
	if idx == -1 {
		return ""
	}
	start := idx + len(prefix)
	end := strings.Index(desc[start:], ")")
	if end == -1 {
		return ""
	}
	return desc[start : start+end]
}

// extractProviderName extracts the provider name part from a FormatProviderDesc string.
// Input: "deepseek (model: deepseek-chat)" → "deepseek"
// Input: "deepseek" → "deepseek"
func extractProviderName(desc string) string {
	const prefix = " (model: "
	idx := strings.Index(desc, prefix)
	if idx == -1 {
		return desc
	}
	return desc[:idx]
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

	c := common.DarkModeColors()

	const maxDialogWidth = 55
	dialogWidth := maxDialogWidth
	if d.width-4 < dialogWidth {
		dialogWidth = d.width - 4
	}
	innerWidth := dialogWidth - 6
	if innerWidth < 30 {
		innerWidth = 30
	}

	// ── Design system styles ──
	borderStyle := common.DialogBorderStyle(c)
	titleStyle := common.SectionHeaderStyle(c)
	helpStyle := common.HelpTextStyle(c)
	cursorStyle := common.CursorIndicatorStyle(c)

	// ── Title ──
	titleLine := titleStyle.Render("Select Model Provider")

	// ── Items ──
	var lines []string
	for i, prov := range d.providers {
		desc := prov
		if descStr, ok := d.providerDescs[prov]; ok && descStr != "" {
			desc = descStr
		}

		providerName := extractProviderName(desc)
		modelName := extractModelName(desc)

		// Build display text
		var displayText string
		if modelName != "" {
			displayText = fmt.Sprintf("%s  (model: %s)", providerName, modelName)
		} else {
			displayText = providerName
		}

		// Active badge
		var badge string
		if prov == d.CurrentProv {
			badge = common.BadgeStyle(c, c.Success).Render("✓ ACTIVE")
		}

		// Cursor indicator
		cursorMarker := "  "
		var rowStyle lipgloss.Style
		if i == d.cursor {
			cursorMarker = "▶ "
			rowStyle = cursorStyle
		} else {
			rowStyle = lipgloss.NewStyle().Foreground(c.TextSecondary).PaddingLeft(4)
		}

		// Assemble row: cursor + display + badge (right-aligned if present)
		var row string
		if badge != "" {
			// Calculate available space for display text
			markerWidth := lipgloss.Width(cursorMarker)
			badgeWidth := lipgloss.Width(badge)
			maxTextWidth := innerWidth - markerWidth - badgeWidth - 2
			if maxTextWidth < 10 {
				maxTextWidth = 10
			}

			textStyle := lipgloss.NewStyle().Width(maxTextWidth).MaxWidth(maxTextWidth)
			if i == d.cursor {
				textStyle = textStyle.Foreground(c.TextPrimary).Bold(true)
			} else {
				textStyle = textStyle.Foreground(c.TextSecondary)
			}

			row = cursorMarker + textStyle.Render(displayText) + " " + badge
		} else {
			textStyle := lipgloss.NewStyle().PaddingLeft(2)
			if i == d.cursor {
				textStyle = textStyle.Foreground(c.TextPrimary).Bold(true)
			} else {
				textStyle = textStyle.Foreground(c.TextSecondary)
			}
			row = cursorMarker + textStyle.Render(displayText)
		}

		lines = append(lines, rowStyle.Render(row))
	}

	itemsStr := strings.Join(lines, "\n")

	// ── Help hint ──
	hint := helpStyle.Render("↑/↓ navigate · Enter select · Esc cancel")

	// ── Assemble dialog content ──
	dialogContent := lipgloss.JoinVertical(lipgloss.Left,
		titleLine,
		"",
		itemsStr,
		"",
		hint,
	)

	dialog := borderStyle.Width(dialogWidth).Render(dialogContent)

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
