package components

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ConfigEntry represents a target with its current model configuration.
type ConfigEntry struct {
	Target      string // "global" or agent name (e.g., "conductor")
	DisplayName string // display name
	Provider    string // current provider
	Model       string // current model
}

// AgentSelectDialog displays a list of agents (and global) with their current
// model configuration, allowing the user to pick one so that a provider
// selection dialog can be shown next.
type AgentSelectDialog struct {
	entries   []ConfigEntry
	cursor    int
	width     int
	height    int
	Selected  string // set to entries[cursor].Target on confirm; "" if cancelled

	borderStyle  lipgloss.Style
	titleStyle   lipgloss.Style
	itemStyle    lipgloss.Style
	cursorStyle  lipgloss.Style
	helpStyle    lipgloss.Style
	valueStyle   lipgloss.Style
}

// NewAgentSelectDialog creates a new agent-select dialog.
func NewAgentSelectDialog(entries []ConfigEntry) *AgentSelectDialog {
	return &AgentSelectDialog{
		entries: entries,
		cursor:  0,
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
		helpStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("240")),
		valueStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("114")),
	}
}

// ID returns the unique identifier for this dialog.
func (d *AgentSelectDialog) ID() string { return "agent_select_dialog" }

// Type returns the dialog type.
func (d *AgentSelectDialog) Type() DialogType { return DialogModal }

// Init initializes the component.
func (d *AgentSelectDialog) Init() tea.Cmd { return nil }

// Update processes incoming messages.
func (d *AgentSelectDialog) Update(msg tea.Msg) (Component, tea.Cmd) {
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
		if d.cursor < len(d.entries)-1 {
			d.cursor++
		}
		return d, nil
	case "enter", " ":
		if len(d.entries) > 0 && d.cursor >= 0 && d.cursor < len(d.entries) {
			d.Selected = d.entries[d.cursor].Target
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
func (d *AgentSelectDialog) View() string {
	if d.width < 40 || d.height < 5 {
		return ""
	}

	const maxDialogWidth = 55
	dialogWidth := maxDialogWidth
	if d.width-4 < dialogWidth {
		dialogWidth = d.width - 4
	}

	// ── Title ──
	titleLine := d.titleStyle.Render("Select target to change model")

	// ── Items ──
	var items []string
	for i, entry := range d.entries {
		desc := entry.DisplayName
		if desc == "" {
			desc = entry.Target
		}

		modelStr := entry.Provider
		if entry.Model != "" {
			modelStr = fmt.Sprintf("%s/%s", entry.Provider, entry.Model)
		}

		line := fmt.Sprintf("%s: %s", desc, modelStr)

		if i == d.cursor {
			// 带 ● 标记的高亮行
			line = d.cursorStyle.Render("  ● " + line)
		} else {
			// 非当前行正常缩进对齐
			line = d.itemStyle.Render("    " + line)
		}
		items = append(items, line)
	}

	itemsStr := strings.Join(items, "\n")

	// ── Help hint ──
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
func (d *AgentSelectDialog) IsFocused() bool { return true }

// Focus sets the component as focused.
func (d *AgentSelectDialog) Focus() tea.Cmd { return nil }

// Blur removes focus from this component.
func (d *AgentSelectDialog) Blur() {}

// SetBounds sets the component's dimensions.
func (d *AgentSelectDialog) SetBounds(width, height int) {
	d.width = width
	d.height = height
}

// Bounds returns the component's current width and height.
func (d *AgentSelectDialog) Bounds() (int, int) { return d.width, d.height }

// IsVisible reports whether this component is currently visible.
func (d *AgentSelectDialog) IsVisible() bool { return true }

// SetVisible sets the visibility of this component.
func (d *AgentSelectDialog) SetVisible(v bool) {}
