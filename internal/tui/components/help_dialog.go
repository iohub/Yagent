package components

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ── Data Structures ──

// keyItem represents a single keybinding: multiple keys with one description.
type keyItem struct {
	Keys []string
	Desc string
}

// keySection represents a grouped set of keybindings with a title.
type keySection struct {
	Title string
	Items []keyItem
}

// helpData holds the full help dialog data for a given language.
type helpData struct {
	Title   string
	Sections []keySection
	Dismiss string
}

// ── Constants ──

const (
	maxDialogWidth = 50
	keyColWidth    = 16
)

// ── Color Palette (ANSI color codes) ──

const (
	colorKeycapBg     = "236" // dark grey background
	colorKeycapFg     = "252" // light grey text
	colorSectionTitle = "39"  // cyan
	colorDescFg       = "245" // soft grey
	colorSepLine      = "237" // very light grey (separator)
	colorDismissFg    = "240" // pale grey
)

// ── HelpDialog ──

// HelpDialog shows vim-like keybindings help.
type HelpDialog struct {
	width        int
	height       int
	lang         Language
	cachedView   string
	langCache    Language
	content      helpData
	outerPadding lipgloss.Style
	// altKeybindings 可配置快捷键覆盖：原始显示键名 → 自定义键名
	altKeybindings map[string]string
}

// NewHelpDialog creates a new help dialog with the given language.
func NewHelpDialog(lang Language) *HelpDialog {
	content := buildHelpData(lang)
	outerPadding := lipgloss.NewStyle().Padding(1, 2)
	return &HelpDialog{
		lang:         lang,
		content:      content,
		outerPadding: outerPadding,
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

	// Rebuild cache if language changed.
	if d.lang != d.langCache {
		d.content = buildHelpData(d.lang)
		d.cachedView = ""
		d.langCache = d.lang
	}

	// Apply configurable keybinding overrides.
	if d.altKeybindings != nil && len(d.altKeybindings) > 0 {
		for si, section := range d.content.Sections {
			for ii, item := range section.Items {
				for ki, key := range item.Keys {
					if altKey, ok := d.altKeybindings[key]; ok {
						d.content.Sections[si].Items[ii].Keys[ki] = altKey
					}
				}
			}
		}
	}

	// Return cached view if available.
	if d.cachedView != "" {
		return d.cachedView
	}

	// ── Dynamic content width ──
	// outerPadding adds 4 cols (2 left + 2 right), so available content width
	// = d.width - 4. Cap at maxDialogWidth but keep a minimum of 40.
	contentWidth := d.width - 4
	if contentWidth > maxDialogWidth {
		contentWidth = maxDialogWidth
	}
	if contentWidth < 40 {
		contentWidth = 40
	}

	// ── Title (centered) ──
	titleStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(colorSectionTitle)).
		Bold(true).
		Width(contentWidth).
		Align(lipgloss.Center)
	titleLine := titleStyle.Render(d.content.Title)

	// ── Sections (left-aligned) ──
	sectionsLine := renderSections(d.content.Sections, contentWidth)

	// ── Dismiss hint (centered) ──
	hintStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(colorDismissFg)).
		Italic(true).
		Width(contentWidth).
		Align(lipgloss.Center)
	hintLine := hintStyle.Render(d.content.Dismiss)

	// ── Assemble ──
	body := lipgloss.JoinVertical(lipgloss.Left,
		titleLine,
		"",
		sectionsLine,
		"",
		hintLine,
	)

	rendered := d.outerPadding.Render(body)

	d.cachedView = lipgloss.Place(d.width, d.height,
		lipgloss.Center, lipgloss.Center,
		rendered,
	)

	return d.cachedView
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

// ── Data Builder ──

// buildHelpData returns the help dialog data for the given language.
func buildHelpData(lang Language) helpData {
	if lang == LanguageZh {
		return helpData{
			Title:   "⌨  帮助",
			Dismiss: "按任意键关闭",
			Sections: []keySection{
				{
					Title: "导航",
					Items: []keyItem{
						{[]string{"j", "↓"}, "向下滚动一行"},
						{[]string{"k", "↑"}, "向上滚动一行"},
						{[]string{"f", "PageDown"}, "向下翻页"},
						{[]string{"b", "PageUp"}, "向上翻页"},
						{[]string{"gg"}, "跳到开头"},
						{[]string{"G"}, "跳到末尾"},
					},
				},
				{
					Title: "模式",
					Items: []keyItem{
						{[]string{"i"}, "进入编辑模式"},
						{[]string{"Ctrl+E"}, "进入命令模式"},
					},
				},
				{
					Title: "命令行",
					Items: []keyItem{
						{[]string{":q"}, "退出程序"},
						{[]string{":help"}, "显示命令帮助"},
						{[]string{"/pattern"}, "搜索日志"},
					},
				},
				{
					Title: "其他",
					Items: []keyItem{
						{[]string{"?"}, "显示此帮助"},
						{[]string{"Alt+M"}, "切换模型"},
						{[]string{"Ctrl+C"}, "强制退出"},
					},
				},
			},
		}
	}
	return helpData{
		Title:   "⌨  Help",
		Dismiss: "Press any key to dismiss",
		Sections: []keySection{
			{
				Title: "Navigation",
				Items: []keyItem{
					{[]string{"j", "↓"}, "scroll down one line"},
					{[]string{"k", "↑"}, "scroll up one line"},
					{[]string{"f", "PageDown"}, "page down"},
					{[]string{"b", "PageUp"}, "page up"},
					{[]string{"gg"}, "go to top"},
					{[]string{"G"}, "go to bottom"},
				},
			},
			{
				Title: "Mode",
				Items: []keyItem{
					{[]string{"i"}, "enter edit mode"},
					{[]string{"Ctrl+E"}, "enter command mode"},
				},
			},
			{
				Title: "Commands",
				Items: []keyItem{
					{[]string{":q"}, "quit"},
					{[]string{":help"}, "show command help"},
					{[]string{"/pattern"}, "search log"},
				},
			},
			{
				Title: "Other",
				Items: []keyItem{
					{[]string{"?"}, "show this help"},
					{[]string{"Alt+M"}, "switch model"},
					{[]string{"Ctrl+C"}, "force quit"},
				},
			},
		},
	}
}

// ── Rendering Functions ──

// keycapStyle returns a lipgloss Style for rendering a single keycap.
func keycapStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Background(lipgloss.Color(colorKeycapBg)).
		Foreground(lipgloss.Color(colorKeycapFg)).
		Bold(true).
		Padding(0, 1)
}

// renderKeycap renders a single key name as a keycap badge.
func renderKeycap(k string) string {
	return keycapStyle().Render(k)
}

// renderKeyColumn renders multiple synonymous keycaps separated by spaces,
// right-aligned within a fixed-width column.
func renderKeyColumn(item keyItem) string {
	if len(item.Keys) == 0 {
		return strings.Repeat(" ", keyColWidth)
	}

	keycaps := make([]string, len(item.Keys))
	for i, k := range item.Keys {
		keycaps[i] = renderKeycap(k)
	}
	joined := strings.Join(keycaps, " ")

	// Measure total rendered width (with spaces included).
	var totalW int
	for i, k := range item.Keys {
		totalW += lipgloss.Width(renderKeycap(k))
		if i < len(item.Keys)-1 {
			totalW += 1 // one space between keycaps
		}
	}

	padding := keyColWidth - totalW
	if padding > 0 {
		return strings.Repeat(" ", padding) + joined
	}
	return joined
}

// renderRow renders one keybinding row: keycaps column + description.
func renderRow(item keyItem) string {
	return renderKeyColumn(item) + "  " + lipgloss.NewStyle().
		Foreground(lipgloss.Color(colorDescFg)).
		Render(item.Desc)
}

// renderSection renders one section: title, separator line, and all rows.
func renderSection(s keySection, dialogWidth int) string {
	var lines []string

	titleStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(colorSectionTitle)).
		Bold(true).
		Width(dialogWidth)
	lines = append(lines, titleStyle.Render(s.Title))

	sepStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(colorSepLine)).
		Width(dialogWidth)
	lines = append(lines, sepStyle.Render(strings.Repeat("─", dialogWidth)))

	for _, item := range s.Items {
		lines = append(lines, renderRow(item))
	}

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// renderSections renders all sections as a single vertical block.
func renderSections(sections []keySection, dialogWidth int) string {
	var blocks []string
	for _, s := range sections {
		blocks = append(blocks, renderSection(s, dialogWidth))
	}
	return lipgloss.JoinVertical(lipgloss.Left, blocks...)
}

// SetAltKeybindings sets display overrides for configurable keybindings.
// key is the default key displayed in the help page (e.g., "j", "Ctrl+E"),
// value is the user-configured key display string (e.g., "Ctrl+J", "Ctrl+Space").
func (d *HelpDialog) SetAltKeybindings(kb map[string]string) {
	d.altKeybindings = kb
	d.cachedView = "" // invalidate cache so View() rebuilds
}
