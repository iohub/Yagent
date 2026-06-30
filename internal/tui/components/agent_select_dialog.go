package components

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"codeactor/internal/tui/common"
)

// ConfigEntry represents a target with its current model configuration.
type ConfigEntry struct {
	Target      string // "global" or agent name (e.g., "director")
	DisplayName string // display name
	Provider    string // current provider
	Model       string // current model
}

// AgentSelectDialog displays a list of agents (and global) with their current
// model configuration, allowing the user to pick one so that a provider
// selection dialog can be shown next.
type AgentSelectDialog struct {
	entries     []ConfigEntry
	cursor      int
	width       int
	height      int
	Selected    string // set to entries[cursor].Target on confirm; "" if cancelled
	styles      *common.Styles

	borderStyle lipgloss.Style
	titleStyle  lipgloss.Style
	itemStyle   lipgloss.Style
	cursorStyle lipgloss.Style
	helpStyle   lipgloss.Style
	valueStyle  lipgloss.Style
	groupStyle  lipgloss.Style
}

// NewAgentSelectDialog creates a new agent-select dialog with the given styles.
func NewAgentSelectDialog(styles *common.Styles, entries []ConfigEntry) *AgentSelectDialog {
	return &AgentSelectDialog{
		entries: entries,
		cursor:  0,
		styles:  styles,
	}
}

// ID returns the unique identifier for this dialog.
func (d *AgentSelectDialog) ID() string { return "agent_select_dialog" }

// Type returns the dialog type.
func (d *AgentSelectDialog) Type() DialogType { return DialogModal }

// Init initializes the component. No setup needed.
func (d *AgentSelectDialog) Init() tea.Cmd { return nil }

// Update processes incoming messages and returns the updated component.
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

// View renders the dialog with the new design system.
func (d *AgentSelectDialog) View() string {
	if d.width < 40 || d.height < 5 {
		return ""
	}

	// ── Color tokens ──
	var c common.ColorTokens
	if d.styles != nil {
		c = common.DarkModeColors()
	} else {
		c = common.DarkModeColors()
	}

	// ── Reusable styles from design system ──
	borderStyle := common.DialogBorderStyle(c)
	titleStyle := common.SectionHeaderStyle(c)
	helpStyle := common.HelpTextStyle(c)
	cursorIndicator := common.CursorIndicatorStyle(c)

	// ── Size calculations ──
	const maxDialogWidth = 60
	dialogWidth := maxDialogWidth
	if d.width-4 < dialogWidth {
		dialogWidth = d.width - 4
	}
	innerWidth := dialogWidth - 6
	if innerWidth < 24 {
		innerWidth = 24
	}

	// ── Title line ──
	titleLine := titleStyle.Render("Select target to change model")

	// ── Group entries: "global" first, then agents ──
	// The caller (showModelSelectionDialog) already builds the list with
	// global first, then agents. We preserve that ordering but detect
	// group boundaries for rendering.
	type groupItem struct {
		label string // group header text
		start int    // first entry index in this group
		end   int    // last entry index in this group (inclusive)
	}

	groups := make([]groupItem, 0)
	currentGroup := ""
	groupStart := -1

	for i := range d.entries {
		e := d.entries[i]
		var grp string
		if e.Target == "global" {
			grp = "global"
		} else {
			grp = "agents"
		}

		if grp != currentGroup {
			// Close previous group
			if currentGroup != "" && groupStart >= 0 {
				groups = append(groups, groupItem{label: currentGroup, start: groupStart, end: i - 1})
			}
			currentGroup = grp
			groupStart = i
		}
	}
	// Close last group
	if currentGroup != "" && groupStart >= 0 {
		groups = append(groups, groupItem{label: currentGroup, start: groupStart, end: len(d.entries) - 1})
	}

	// ── Render items with group headers ──
	var renderedItems []string
	prevWasHeader := false

	for _, g := range groups {
		// Group header
		if prevWasHeader {
			renderedItems = append(renderedItems, "")
		}
		headerStyle := common.SectionHeaderStyle(c)
		renderedItems = append(renderedItems, headerStyle.Render(g.label))
		prevWasHeader = true

		isSingle := (g.start == g.end)

		for i := g.start; i <= g.end && i < len(d.entries); i++ {
			entry := d.entries[i]
			desc := entry.DisplayName
			if desc == "" {
				desc = entry.Target
			}

			modelStr := entry.Provider
			if entry.Model != "" {
				modelStr = fmt.Sprintf("%s/%s", entry.Provider, entry.Model)
			}

			if i == d.cursor {
				// Focused item: ▶ cursor + bold name + dimmed value
				valPart := modelStr

				nameSt := lipgloss.NewStyle().Foreground(c.Primary).Bold(true)
				valSt := lipgloss.NewStyle().Foreground(c.TextSecondary)

				parts := strings.SplitN(valPart, "/", 2)
				var provPart, modelPart string
				if len(parts) == 2 {
					provPart = parts[0]
					modelPart = parts[1]
				} else {
					provPart = parts[0]
				}

				line := cursorIndicator.Render("▶ ") +
					nameSt.Render(provPart)
				if modelPart != "" {
					line += "/" + valSt.Render(modelPart)
				}

				renderedItems = append(renderedItems, line)
			} else {
				// Normal item
				indent := "    "
				if isSingle {
					indent = ""
				}
				itemSt := lipgloss.NewStyle().Foreground(c.TextSecondary)
				renderedItems = append(renderedItems, indent+itemSt.Render(desc+"  "+modelStr))
			}
		}
	}

	itemsBlock := lipgloss.JoinVertical(lipgloss.Left, renderedItems...)

	// ── Help text ──
	hint := helpStyle.Render("↑/↓ navigate  ·  Enter select  ·  Esc cancel")

	// ── Assemble content ──
	content := lipgloss.JoinVertical(lipgloss.Left,
		titleLine,
		"",
		itemsBlock,
		"",
		hint,
	)

	dialog := borderStyle.Width(dialogWidth).Render(content)

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
func (d *AgentSelectDialog) SetBounds(w, h int) { d.width = w; d.height = h }

// Bounds returns the component's current width and height.
func (d *AgentSelectDialog) Bounds() (int, int) { return d.width, d.height }

// IsVisible reports whether this component is currently visible.
func (d *AgentSelectDialog) IsVisible() bool { return true }

// SetVisible sets the visibility of this component.
func (d *AgentSelectDialog) SetVisible(bool) {}
