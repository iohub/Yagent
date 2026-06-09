package common

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

// ── Section — Section header with title + horizontal line fill ─────────

// SectionHeader renders a section header with a title and a horizontal line
// filling the remaining width (adapted from crush's common.Section).
func SectionHeader(title string, width int) string {
	return SectionHeaderWithInfo(title, width)
}

// SectionHeaderWithInfo renders a section header with title, optional info text,
// and a horizontal line fill.
func SectionHeaderWithInfo(title string, width int, info ...string) string {
	char := "─"
	length := lipgloss.Width(title) + 1
	remainingWidth := width - length

	var infoText string
	if len(info) > 0 {
		infoText = strings.Join(info, " ")
		if len(infoText) > 0 {
			infoText = " " + infoText
			remainingWidth -= lipgloss.Width(infoText)
		}
	}

	styledTitle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Render(title)

	if remainingWidth > 0 {
		lineStyle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("237"))
		styledTitle = styledTitle + " " + lineStyle.Render(strings.Repeat(char, remainingWidth)) + infoText
	}
	return styledTitle
}

// ── Section — Bordered content block (original, kept for compat) ──────

// Section renders a titled, bordered content area.
func Section(title string, content string, width int, s *Styles) string {
	titleStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("39")).
		Bold(true).
		Padding(0, 1)

	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("240")).
		Width(width - 2)

	header := titleStyle.Render(title)
	body := borderStyle.Render(content)

	return lipgloss.JoinVertical(lipgloss.Left, header, body)
}

// ── DialogTitle — Dialog headers ───────────────────────────────────────

// DialogTitle renders a dialog title with a highlight bar (original).
func DialogTitle(title string, width int) string {
	titleStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("24")).
		Foreground(lipgloss.Color("15")).
		Bold(true).
		Padding(0, 1).
		Width(width - 2)

	return titleStyle.Render(title)
}

// DialogTitleGrad renders a dialog title with a decorative gradient diagonal
// line fill (adapted from crush's common.DialogTitle).
func DialogTitleGrad(title string, width int, fromColor, toColor color.Color) string {
	char := "╱"
	length := lipgloss.Width(title) + 1
	remainingWidth := width - length
	if remainingWidth > 0 {
		lines := strings.Repeat(char, remainingWidth)
		lines = ApplyForegroundGrad(lipgloss.NewStyle(), lines, fromColor, toColor)
		title = title + " " + lines
	}
	return title
}

// ── Button — Interactive button elements ──────────────────────────────

// Button renders a selectable button. focused=true highlights it.
func Button(label string, focused bool) string {
	if focused {
		return lipgloss.NewStyle().
			Background(lipgloss.Color("39")).
			Foreground(lipgloss.Color("15")).
			Bold(true).
			Padding(0, 2).
			Render("▶ " + label)
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Padding(0, 2).
		Render("  " + label)
}

// ButtonOpts defines the configuration for a styled button (adapted from crush).
type ButtonOpts struct {
	Text           string // button label
	UnderlineIndex int    // 0-based index of character to underline (-1 for none)
	Selected       bool   // whether this button is currently selected
	Padding        int    // inner horizontal padding, defaults to 2
}

// StyledButton creates a button with an underlined character and selection state.
func StyledButton(opts ButtonOpts) string {
	focusedStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("39")).
		Foreground(lipgloss.Color("15")).
		Bold(true)

	blurredStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("236")).
		Foreground(lipgloss.Color("245"))

	style := blurredStyle
	if opts.Selected {
		style = focusedStyle
	}

	if opts.Padding == 0 {
		opts.Padding = 2
	}

	text := opts.Text

	// Clamp underline index
	if opts.UnderlineIndex > -1 && opts.UnderlineIndex > len(text)-1 {
		opts.UnderlineIndex = -1
	}

	text = style.Padding(0, opts.Padding).Render(text)

	if opts.UnderlineIndex != -1 {
		text = lipgloss.StyleRanges(text,
			lipgloss.NewRange(
				opts.Padding+opts.UnderlineIndex,
				opts.Padding+opts.UnderlineIndex+1,
				style.Underline(true),
			),
		)
	}

	return text
}

// ButtonGroup creates a row of styled buttons (adapted from crush).
// spacing is the separator between buttons ("  " for horizontal, "\n" for vertical).
func ButtonGroup(buttons []ButtonOpts, spacing string) string {
	if len(buttons) == 0 {
		return ""
	}
	if spacing == "" {
		spacing = "  "
	}

	parts := make([]string, len(buttons))
	for i, btn := range buttons {
		parts[i] = StyledButton(btn)
	}

	return strings.Join(parts, spacing)
}

// ── Status — Status line rendering (adapted from crush) ─────────────

// StatusOpts defines options for rendering a status line.
type StatusOpts struct {
	Icon             string
	Title            string
	Description      string
	ExtraContent     string // appended after description
}

// StatusLine renders a status line with icon, title, description.
func StatusLine(opts StatusOpts, width int) string {
	icon := opts.Icon
	title := opts.Title
	desc := opts.Description

	titleColor := lipgloss.Color("250")
	descColor := lipgloss.Color("245")

	title = lipgloss.NewStyle().Foreground(titleColor).Render(title)

	if desc != "" {
		extraW := lipgloss.Width(opts.ExtraContent)
		if extraW > 0 {
			extraW += 1
		}
		maxDescW := width - lipgloss.Width(icon) - lipgloss.Width(title) - 2 - extraW
		if maxDescW < 4 {
			maxDescW = 4
		}
		desc = truncate(desc, maxDescW, "…")
		desc = lipgloss.NewStyle().Foreground(descColor).Render(desc)
	}

	var content []string
	if icon != "" {
		content = append(content, icon)
	}
	content = append(content, title)
	if desc != "" {
		content = append(content, desc)
	}
	if opts.ExtraContent != "" {
		content = append(content, opts.ExtraContent)
	}

	return strings.Join(content, " ")
}

// truncate truncates s to maxLen, appending suffix if truncated.
func truncate(s string, maxLen int, suffix string) string {
	if lipgloss.Width(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	result := ""
	currentWidth := 0
	for _, r := range runes {
		runeW := lipgloss.Width(string(r))
		if currentWidth+runeW+lipgloss.Width(suffix) > maxLen {
			break
		}
		result += string(r)
		currentWidth += runeW
	}
	return result + suffix
}

// ── Header — Top header bar ──────────────────────────────────────────────

// Header renders the top bar with logo, provider/model info, and optional hints.
// In compact mode (width <= 100), renders a single line.
// In full mode (width > 100), renders the ASCII logo banner + info line.
func Header(width int, provider, model string, s *Styles) string {
	if width <= 100 {
		return compactHeader(width, provider, model, s)
	}
	return fullHeader(width, provider, model, s)
}

func compactHeader(width int, provider, model string, s *Styles) string {
	info := formatModelInfo(provider, model)

	// Use gradient brand text for "CODE ACTOR"
	brandText := ApplyGrad("CODEACTOR")

	if info == "" {
		return " " + brandText + " "
	}
	return " " + brandText + s.WelcomeSub.Render("  "+info) + " "
}

func fullHeader(width int, provider, model string, s *Styles) string {
	var parts []string

	// ASCII logo
	parts = append(parts, renderASCIILogo())

	// Model info line
	if info := formatModelInfo(provider, model); info != "" {
		parts = append(parts, s.WelcomeSub.Render("  "+info))
	}

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func formatModelInfo(provider, model string) string {
	switch {
	case provider != "" && model != "":
		return provider + "/" + model
	case model != "":
		return model
	case provider != "":
		return provider
	default:
		return ""
	}
}

// renderASCIILogo renders the "CODE ACTOR" ASCII art logo with gradient text.
func renderASCIILogo() string {
	asciiLogo := []string{
		"╔═╗┌─┐┌┬┐┌─┐  ╔═╗┌─┐┌┬┐┌─┐┬─┐  ╔═╗╦",
		"║  │ │ ││├┤   ╠═╣│   │ │ │├┬┘  ╠═╣║",
		"╚═╝└─┘─┴┘└─┘  ╩ ╩└─┘ ┴ └─┘┴└─  ╩ ╩╩",
	}

	// Use gradient colors: blue → cyan → green
	gradFrom := DefaultGradFrom
	gradTo := DefaultGradTo

	var rendered []string
	for _, line := range asciiLogo {
		// Apply gradient per line
		gradLine := ApplyBoldForegroundGrad(
			lipgloss.NewStyle(),
			line,
			gradFrom,
			gradTo,
		)
		rendered = append(rendered, gradLine)
	}
	return lipgloss.NewStyle().Padding(0, 1).Render(lipgloss.JoinVertical(lipgloss.Left, rendered...))
}

// ── StatusBar — Airline-style bottom status bar ──────────────────────────

// StatusSegment represents a single colored segment in the status bar.
type StatusSegment struct {
	Text  string
	Bg    string // lipgloss color string (e.g., "24")
	Fg    string
	Bold  bool
}

// StatusBar renders an airline-style status bar from left and right segments.
// leftSegs: segments anchored to the left
// rightSegs: segments anchored to the right
// fillerBg: background color for the filler space between left and right
// width: total terminal width
func StatusBar(leftSegs, rightSegs []StatusSegment, fillerBg string, width int) string {
	// Build left part
	var leftParts []string
	for _, seg := range leftSegs {
		st := lipgloss.NewStyle().
			Background(lipgloss.Color(seg.Bg)).
			Foreground(lipgloss.Color(seg.Fg)).
			Padding(0, 1)
		if seg.Bold {
			st = st.Bold(true)
		}
		leftParts = append(leftParts, st.Render(seg.Text))
	}

	// Build right part
	var rightParts []string
	for i, seg := range rightSegs {
		st := lipgloss.NewStyle().
			Background(lipgloss.Color(seg.Bg)).
			Foreground(lipgloss.Color(seg.Fg)).
			Padding(0, 1)
		if seg.Bold {
			st = st.Bold(true)
		}

		// Add separator between segments
		if i > 0 || len(leftParts) > 0 {
			prevBg := fillerBg
			if i > 0 {
				prevBg = rightSegs[i-1].Bg
			}
			if len(leftParts) == 0 && i == 0 {
				prevBg = fillerBg
			}
			rightParts = append(rightParts, makeStatusSep(prevBg, seg.Bg, true))
		}
		rightParts = append(rightParts, st.Render(seg.Text))
	}

	leftStr := strings.Join(leftParts, "")
	rightStr := strings.Join(rightParts, "")

	// Filler: fill remaining space
	usedWidth := lipgloss.Width(leftStr) + lipgloss.Width(rightStr)
	fillerW := width - usedWidth
	if fillerW < 0 {
		fillerW = 0
	}

	filler := lipgloss.NewStyle().
		Background(lipgloss.Color(fillerBg)).
		Foreground(lipgloss.Color(fillerBg)).
		Render(strings.Repeat(" ", fillerW))

	return leftStr + filler + rightStr
}

// makeStatusSep creates a powerline separator between two colored segments.
func makeStatusSep(fromBg, toBg string, left bool) string {
	if left {
		// ◀ separator (left-pointing triangle for right-aligned segments)
		return lipgloss.NewStyle().
			Foreground(lipgloss.Color(toBg)).
			Background(lipgloss.Color(fromBg)).
			Render("")
	}
	// ▶ separator (right-pointing triangle for left-aligned segments)
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(toBg)).
		Background(lipgloss.Color(fromBg)).
		Render("")
}

// ── Spacer — Utility spacing ─────────────────────────────────────────────

// Spacer returns a string of n newlines for vertical spacing.
func Spacer(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("\n", n)
}

// ── Gradient helpers ─────────────────────────────────────────────────────

// GradientLine renders a horizontal line with a gradient fill (adapted from crush).
func GradientLine(width int, char string, fromColor, toColor color.Color) string {
	if width <= 0 {
		return ""
	}
	return ApplyForegroundGrad(
		lipgloss.NewStyle(),
		strings.Repeat(char, width),
		fromColor,
		toColor,
	)
}

// ── ConfirmationPrompt — Yes/No prompt ────────────────────────────────────

// ConfirmationPrompt renders a yes/no confirmation line.
// selected: 0 = Yes, 1 = No; -1 = neither focused
func ConfirmationPrompt(message string, selected int, s *Styles) string {
	msgStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	yesBtn := Button("Yes", selected == 0)
	noBtn := Button("No", selected == 1)

	return msgStyle.Render(message) + "\n" +
		lipgloss.JoinHorizontal(lipgloss.Top, yesBtn, "  ", noBtn)
}

// ── FormatTokenShort — Token count with k/m suffix ──────────────────────

// FormatTokenShort formats a token count with k/m suffixes.
func FormatTokenShort(n int64) string {
	switch {
	case n >= 1000000:
		return fmt.Sprintf("%.1fm", float64(n)/1000000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}
