// Package common provides shared UI components, styles, and utilities for the TUI.
package common

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"image/color"
)

// ── Tool icon styles ──

var (
	IconPending  = lipgloss.NewStyle().Foreground(lipgloss.Color("108")).Render("●")
	IconSuccess  = lipgloss.NewStyle().Foreground(lipgloss.Color("114")).Render("✓")
	IconError    = lipgloss.NewStyle().Foreground(lipgloss.Color("167")).Render("×")
	IconCanceled = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("●")
)

// ── Tool name background color palette ──

var toolBgColors = []string{
	"52", "17", "22", "94", "53", "18", "23", "58",
	"95", "24", "88", "59", "131", "60", "96", "97",
}

// ToolBgColor returns a stable background color for the given tool name.
func ToolBgColor(name string) string {
	h := 0
	for _, c := range name {
		h = h*31 + int(c)
	}
	if h < 0 {
		h = -h
	}
	return toolBgColors[h%len(toolBgColors)]
}

// RenderToolName renders a tool name with its unique background highlight.
func RenderToolName(name string) string {
	bg := ToolBgColor(name)
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color("15")).
		Background(lipgloss.Color(bg)).
		Bold(true).
		Padding(0, 1).
		Render(name)
}

// DisplayToolName maps internal tool names to their display names.
func DisplayToolName(name string) string {
	switch name {
	case "search_replace_in_file":
		return "edit_file"
	case "llm_call":
		return "think"
	default:
		return name
	}
}

// ── Styles struct — centralized theme ──

// Styles holds all UI styles organized by category.
type Styles struct {
	// Banner / Welcome
	BannerPad      lipgloss.Style
	PromptFocused  lipgloss.Style
	PromptBlurred  lipgloss.Style
	WelcomePanel   lipgloss.Style
	WelcomeTitle   lipgloss.Style
	WelcomeSub     lipgloss.Style
	WelcomeDim     lipgloss.Style

	// Message log
	LogTime      lipgloss.Style
	LogAIRes     lipgloss.Style
	LogSeparator lipgloss.Style

	// User message
	UserPrefix       lipgloss.Style
	UserMsgContent   lipgloss.Style
	UserMsgBoxBorder lipgloss.Style
	UserMsgBoxText   lipgloss.Style

	// Input panel
	InputPanel        lipgloss.Style
	InputPanelBlurred lipgloss.Style
	InputSeparator    lipgloss.Style

	// Diff
	DiffHunk      lipgloss.Style
	DiffAdd       lipgloss.Style
	DiffDel       lipgloss.Style
	DiffCtx       lipgloss.Style
	DiffNoNewline lipgloss.Style

	// Tool status
	ToolRunning lipgloss.Style
	ToolDone    lipgloss.Style
	ToolError   lipgloss.Style

	// LLM call
	LLMCall    lipgloss.Style
	LLMCallEnd lipgloss.Style

	// Command mode
	CommandPrefix lipgloss.Style
	CommandBar    lipgloss.Style

	// Error / Info
	Error   lipgloss.Style
	InfoMsg lipgloss.Style
	Footer  lipgloss.Style

	// Tool rendering (from styles.go)
	IconPendingStr  string
	IconSuccessStr  string
	IconErrorStr    string
	IconCanceledStr string

	NameNormal lipgloss.Style
	NameNested lipgloss.Style
	ParamMain  lipgloss.Style
	ParamKey   lipgloss.Style

	StateRunning  lipgloss.Style
	StateCanceled lipgloss.Style
	StateWaiting  lipgloss.Style

	ErrorTag     lipgloss.Style
	ErrorMessage lipgloss.Style

	Body         lipgloss.Style
	ContentLine  lipgloss.Style
	ContentTrunc lipgloss.Style

	DiffHeader lipgloss.Style

	// Prefix styles
	Time   lipgloss.Style
	AIRes  lipgloss.Style
	Status lipgloss.Style
	Help   lipgloss.Style

	// Tool call borders
	ToolCallBorderTop    lipgloss.Style
	ToolCallBorderBottom lipgloss.Style

	// Separator
	Separator lipgloss.Style

	// Collapse hint
	CollapseHintLine lipgloss.Style
	CollapseHintText lipgloss.Style

	// Airline status bar
	AirlineNormalMode  lipgloss.Style
	AirlineRunMode     lipgloss.Style
	AirlineCommandMode lipgloss.Style
	AirlineInfo        lipgloss.Style
	AirlineInfoAlt     lipgloss.Style
	AirlineAccent      lipgloss.Style
	AirlineFiller      lipgloss.Style

	// Dynamic styles
}

// Theme represents the color theme for the TUI.
type Theme int

const (
	// ThemeDark is the dark (default) theme.
	ThemeDark Theme = iota
	// ThemeLight is the light theme.
	ThemeLight
)

// NewStyles creates a Styles instance for the given theme.
func NewStyles(theme ...Theme) *Styles {
	t := ThemeDark
	if len(theme) > 0 {
		t = theme[0]
	}
	switch t {
	case ThemeLight:
		return newLightStyles()
	default:
		return newDarkStyles()
	}
}

// newDarkStyles creates the dark theme styles.
func newDarkStyles() *Styles {
	s := &Styles{
		BannerPad:     lipgloss.NewStyle().Padding(0, 1),
		PromptFocused: lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true),
		PromptBlurred: lipgloss.NewStyle().Foreground(lipgloss.Color("244")),

		WelcomePanel: lipgloss.NewStyle().Padding(1, 2),
		WelcomeTitle: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252")),
		WelcomeSub:   lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		WelcomeDim:   lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true),

		Error:   lipgloss.NewStyle().Foreground(lipgloss.Color("167")).Bold(true),
		InfoMsg: lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
		Footer:  lipgloss.NewStyle().Foreground(lipgloss.Color("240")),

		LogTime:      lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Faint(true),
		LogAIRes:     lipgloss.NewStyle().Foreground(lipgloss.Color("252")),
		LogSeparator: lipgloss.NewStyle().Foreground(lipgloss.Color("240")),

		UserPrefix: lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true),
		UserMsgContent: lipgloss.NewStyle().
			Foreground(lipgloss.Color("222")).
			BorderLeft(true).
			BorderForeground(lipgloss.Color("214")).
			PaddingLeft(1),
		UserMsgBoxBorder: lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
		UserMsgBoxText:   lipgloss.NewStyle().Foreground(lipgloss.Color("222")),

		InputPanel: lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("39")).
			Padding(0, 1).
			MarginTop(1),
		InputPanelBlurred: lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1).
			MarginTop(1),
		InputSeparator: lipgloss.NewStyle().Foreground(lipgloss.Color("240")),

		DiffHunk:      lipgloss.NewStyle().Foreground(lipgloss.Color("39")),
		DiffAdd:       lipgloss.NewStyle().Foreground(lipgloss.Color("114")),
		DiffDel:       lipgloss.NewStyle().Foreground(lipgloss.Color("167")),
		DiffCtx:       lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		DiffNoNewline: lipgloss.NewStyle().Foreground(lipgloss.Color("245")),

		ToolRunning: lipgloss.NewStyle().Foreground(lipgloss.Color("228")),
		ToolDone:    lipgloss.NewStyle().Foreground(lipgloss.Color("114")),
		ToolError:   lipgloss.NewStyle().Foreground(lipgloss.Color("167")),

		LLMCall:    lipgloss.NewStyle().Foreground(lipgloss.Color("141")),
		LLMCallEnd: lipgloss.NewStyle().Foreground(lipgloss.Color("111")),

		CommandPrefix: lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true),
		CommandBar: lipgloss.NewStyle().
			Background(lipgloss.Color("214")).
			Foreground(lipgloss.Color("0")).
			Bold(true),

		// Tool rendering (from styles.go)
		IconPendingStr:  IconPending,
		IconSuccessStr:  IconSuccess,
		IconErrorStr:    IconError,
		IconCanceledStr: IconCanceled,

		NameNormal: lipgloss.NewStyle().Foreground(lipgloss.Color("39")),
		NameNested: lipgloss.NewStyle().Foreground(lipgloss.Color("75")),
		ParamMain:  lipgloss.NewStyle().Foreground(lipgloss.Color("247")),
		ParamKey:   lipgloss.NewStyle().Foreground(lipgloss.Color("245")),

		StateRunning:  lipgloss.NewStyle().Foreground(lipgloss.Color("252")),
		StateCanceled: lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		StateWaiting:  lipgloss.NewStyle().Foreground(lipgloss.Color("252")),

		ErrorTag:     lipgloss.NewStyle().Background(lipgloss.Color("167")).Foreground(lipgloss.Color("15")).Bold(true),
		ErrorMessage: lipgloss.NewStyle().Foreground(lipgloss.Color("167")),

		Body:         lipgloss.NewStyle().Foreground(lipgloss.Color("252")).PaddingLeft(2),
		ContentLine:  lipgloss.NewStyle().Foreground(lipgloss.Color("245")).PaddingLeft(1),
		ContentTrunc: lipgloss.NewStyle().Foreground(lipgloss.Color("243")).PaddingLeft(1),

		DiffHeader: lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true),

		Time:   lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Faint(true),
		AIRes:  lipgloss.NewStyle().Foreground(lipgloss.Color("252")),
		Status: lipgloss.NewStyle().Foreground(lipgloss.Color("36")),
		Help:   lipgloss.NewStyle().Foreground(lipgloss.Color("228")),

		ToolCallBorderTop:    lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
		ToolCallBorderBottom: lipgloss.NewStyle().Foreground(lipgloss.Color("240")),

		CollapseHintLine: lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
		CollapseHintText: lipgloss.NewStyle().Foreground(lipgloss.Color("245")),

		Separator: lipgloss.NewStyle().Foreground(lipgloss.Color("237")),

		AirlineNormalMode: lipgloss.NewStyle().
			Background(lipgloss.Color("24")).
			Foreground(lipgloss.Color("15")).
			Bold(true).Padding(0, 1),
		AirlineRunMode: lipgloss.NewStyle().
			Background(lipgloss.Color("76")).
			Foreground(lipgloss.Color("15")).
			Bold(true).Padding(0, 1),
		AirlineCommandMode: lipgloss.NewStyle().
			Background(lipgloss.Color("214")).
			Foreground(lipgloss.Color("0")).
			Bold(true).Padding(0, 1),
		AirlineInfo: lipgloss.NewStyle().
			Background(lipgloss.Color("236")).
			Foreground(lipgloss.Color("250")).
			Padding(0, 1),
		AirlineInfoAlt: lipgloss.NewStyle().
			Background(lipgloss.Color("238")).
			Foreground(lipgloss.Color("250")).
			Padding(0, 1),
		AirlineAccent: lipgloss.NewStyle().
			Background(lipgloss.Color("166")).
			Foreground(lipgloss.Color("15")).
			Padding(0, 1),
		AirlineFiller: lipgloss.NewStyle().
			Background(lipgloss.Color("236")).
			Foreground(lipgloss.Color("250")),
	}
	return s
}

// newLightStyles creates the light theme styles.
func newLightStyles() *Styles {
	s := &Styles{
		BannerPad:     lipgloss.NewStyle().Padding(0, 1),
		PromptFocused: lipgloss.NewStyle().Foreground(lipgloss.Color("27")).Bold(true),
		PromptBlurred: lipgloss.NewStyle().Foreground(lipgloss.Color("243")),

		WelcomePanel: lipgloss.NewStyle().Padding(1, 2),
		WelcomeTitle: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("235")),
		WelcomeSub:   lipgloss.NewStyle().Foreground(lipgloss.Color("242")),
		WelcomeDim:   lipgloss.NewStyle().Foreground(lipgloss.Color("27")).Bold(true),

		Error:   lipgloss.NewStyle().Foreground(lipgloss.Color("160")).Bold(true),
		InfoMsg: lipgloss.NewStyle().Foreground(lipgloss.Color("243")),
		Footer:  lipgloss.NewStyle().Foreground(lipgloss.Color("244")),

		LogTime:      lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Faint(true),
		LogAIRes:     lipgloss.NewStyle().Foreground(lipgloss.Color("235")),
		LogSeparator: lipgloss.NewStyle().Foreground(lipgloss.Color("248")),

		UserPrefix: lipgloss.NewStyle().Foreground(lipgloss.Color("172")).Bold(true),
		UserMsgContent: lipgloss.NewStyle().
			Foreground(lipgloss.Color("238")).
			BorderLeft(true).
			BorderForeground(lipgloss.Color("172")).
			PaddingLeft(1),
		UserMsgBoxBorder: lipgloss.NewStyle().Foreground(lipgloss.Color("248")),
		UserMsgBoxText:   lipgloss.NewStyle().Foreground(lipgloss.Color("238")),

		InputPanel: lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("27")).
			Padding(0, 1).
			MarginTop(1),
		InputPanelBlurred: lipgloss.NewStyle().
			Border(lipgloss.NormalBorder()).
			BorderForeground(lipgloss.Color("248")).
			Padding(0, 1).
			MarginTop(1),
		InputSeparator: lipgloss.NewStyle().Foreground(lipgloss.Color("248")),

		DiffHunk:      lipgloss.NewStyle().Foreground(lipgloss.Color("27")),
		DiffAdd:       lipgloss.NewStyle().Foreground(lipgloss.Color("28")),
		DiffDel:       lipgloss.NewStyle().Foreground(lipgloss.Color("160")),
		DiffCtx:       lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
		DiffNoNewline: lipgloss.NewStyle().Foreground(lipgloss.Color("244")),

		ToolRunning: lipgloss.NewStyle().Foreground(lipgloss.Color("178")),
		ToolDone:    lipgloss.NewStyle().Foreground(lipgloss.Color("28")),
		ToolError:   lipgloss.NewStyle().Foreground(lipgloss.Color("160")),

		LLMCall:    lipgloss.NewStyle().Foreground(lipgloss.Color("93")),
		LLMCallEnd: lipgloss.NewStyle().Foreground(lipgloss.Color("26")),

		CommandPrefix: lipgloss.NewStyle().Foreground(lipgloss.Color("172")).Bold(true),
		CommandBar: lipgloss.NewStyle().
			Background(lipgloss.Color("172")).
			Foreground(lipgloss.Color("15")).
			Bold(true),

		IconPendingStr:  IconPending,
		IconSuccessStr:  IconSuccess,
		IconErrorStr:    IconError,
		IconCanceledStr: IconCanceled,

		NameNormal: lipgloss.NewStyle().Foreground(lipgloss.Color("27")),
		NameNested: lipgloss.NewStyle().Foreground(lipgloss.Color("31")),
		ParamMain:  lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
		ParamKey:   lipgloss.NewStyle().Foreground(lipgloss.Color("243")),

		StateRunning:  lipgloss.NewStyle().Foreground(lipgloss.Color("235")),
		StateCanceled: lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
		StateWaiting:  lipgloss.NewStyle().Foreground(lipgloss.Color("235")),

		ErrorTag:     lipgloss.NewStyle().Background(lipgloss.Color("160")).Foreground(lipgloss.Color("15")).Bold(true),
		ErrorMessage: lipgloss.NewStyle().Foreground(lipgloss.Color("160")),

		Body:         lipgloss.NewStyle().Foreground(lipgloss.Color("235")).PaddingLeft(2),
		ContentLine:  lipgloss.NewStyle().Foreground(lipgloss.Color("243")).PaddingLeft(1),
		ContentTrunc: lipgloss.NewStyle().Foreground(lipgloss.Color("245")).PaddingLeft(1),

		DiffHeader: lipgloss.NewStyle().Foreground(lipgloss.Color("235")).Bold(true),

		Time:   lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Faint(true),
		AIRes:  lipgloss.NewStyle().Foreground(lipgloss.Color("235")),
		Status: lipgloss.NewStyle().Foreground(lipgloss.Color("29")),
		Help:   lipgloss.NewStyle().Foreground(lipgloss.Color("178")),

		ToolCallBorderTop:    lipgloss.NewStyle().Foreground(lipgloss.Color("248")),
		ToolCallBorderBottom: lipgloss.NewStyle().Foreground(lipgloss.Color("248")),

		CollapseHintLine: lipgloss.NewStyle().Foreground(lipgloss.Color("248")),
		CollapseHintText: lipgloss.NewStyle().Foreground(lipgloss.Color("244")),

		Separator: lipgloss.NewStyle().Foreground(lipgloss.Color("250")),

		AirlineNormalMode: lipgloss.NewStyle().
			Background(lipgloss.Color("25")).
			Foreground(lipgloss.Color("15")).
			Bold(true).Padding(0, 1),
		AirlineRunMode: lipgloss.NewStyle().
			Background(lipgloss.Color("28")).
			Foreground(lipgloss.Color("15")).
			Bold(true).Padding(0, 1),
		AirlineCommandMode: lipgloss.NewStyle().
			Background(lipgloss.Color("172")).
			Foreground(lipgloss.Color("15")).
			Bold(true).Padding(0, 1),
		AirlineInfo: lipgloss.NewStyle().
			Background(lipgloss.Color("253")).
			Foreground(lipgloss.Color("235")).
			Padding(0, 1),
		AirlineInfoAlt: lipgloss.NewStyle().
			Background(lipgloss.Color("250")).
			Foreground(lipgloss.Color("235")).
			Padding(0, 1),
		AirlineAccent: lipgloss.NewStyle().
			Background(lipgloss.Color("166")).
			Foreground(lipgloss.Color("15")).
			Padding(0, 1),
		AirlineFiller: lipgloss.NewStyle().
			Background(lipgloss.Color("253")).
			Foreground(lipgloss.Color("235")),
	}
	return s
}

// FormatTokenCount formats a token count with comma separators.
func FormatTokenCount(n int) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var result []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			result = append(result, ',')
		}
		result = append(result, byte(c))
	}
	return string(result)
}

// ── Color Tokens ──
// ColorTokens holds the 256-color ANSI palette for the design system.
type ColorTokens struct {
	Primary       color.Color // 39  — blue, main accent
	PrimaryDim    color.Color // 24  — dimmed blue
	PrimaryBright color.Color // 75  — bright blue for highlights
	Accent        color.Color // 214 — orange, warnings & emphasis
	Success       color.Color // 114 — green, positive actions
	Warning       color.Color // 220 — yellow, caution
	Danger        color.Color // 196 — red, dangerous actions
	DangerDim     color.Color // 131 — dimmed red
	TextPrimary   color.Color // 252 — primary text
	TextSecondary color.Color // 248 — secondary text
	TextMuted     color.Color // 244 — muted/hint text
	TextInverted  color.Color // 236 — text on bright backgrounds
	Surface       color.Color // 236 — dialog background
	SurfaceRaised color.Color // 238 — raised elements
	SurfaceHover  color.Color // 240 — hover/focus background
	Overlay       color.Color // 234 — backdrop overlay
	Border        color.Color // 240 — default borders
	BorderActive  color.Color // 39  — active/focused borders
}

// DarkModeColors returns the dark theme color tokens.
func DarkModeColors() ColorTokens {
	return ColorTokens{
		Primary:       lipgloss.Color("39"),
		PrimaryDim:    lipgloss.Color("24"),
		PrimaryBright: lipgloss.Color("75"),
		Accent:        lipgloss.Color("214"),
		Success:       lipgloss.Color("114"),
		Warning:       lipgloss.Color("220"),
		Danger:        lipgloss.Color("196"),
		DangerDim:     lipgloss.Color("131"),
		TextPrimary:   lipgloss.Color("252"),
		TextSecondary: lipgloss.Color("248"),
		TextMuted:     lipgloss.Color("244"),
		TextInverted:  lipgloss.Color("236"),
		Surface:       lipgloss.Color("236"),
		SurfaceRaised: lipgloss.Color("238"),
		SurfaceHover:  lipgloss.Color("240"),
		Overlay:       lipgloss.Color("234"),
		Border:        lipgloss.Color("240"),
		BorderActive:  lipgloss.Color("39"),
	}
}

// LightModeColors returns the light theme color tokens.
func LightModeColors() ColorTokens {
	return ColorTokens{
		Primary:       lipgloss.Color("27"),
		PrimaryDim:    lipgloss.Color("25"),
		PrimaryBright: lipgloss.Color("33"),
		Accent:        lipgloss.Color("172"),
		Success:       lipgloss.Color("28"),
		Warning:       lipgloss.Color("178"),
		Danger:        lipgloss.Color("160"),
		DangerDim:     lipgloss.Color("131"),
		TextPrimary:   lipgloss.Color("235"),
		TextSecondary: lipgloss.Color("238"),
		TextMuted:     lipgloss.Color("243"),
		TextInverted:  lipgloss.Color("15"),
		Surface:       lipgloss.Color("253"),
		SurfaceRaised: lipgloss.Color("250"),
		SurfaceHover:  lipgloss.Color("248"),
		Overlay:       lipgloss.Color("235"),
		Border:        lipgloss.Color("248"),
		BorderActive:  lipgloss.Color("27"),
	}
}

// SafetyLevel represents the risk level of a permission option.
type SafetyLevel int

const (
	SafetySafe     SafetyLevel = iota // Deny
	SafetyLow                         // Allow Once
	SafetyMedium                      // Allow Tool
	SafetyHigh                        // Allow Session
	SafetyCritical                    // Allow Project
)

// Colors returns the foreground and background colors for a safety level.
func (s SafetyLevel) Colors(c ColorTokens) (fg, bg color.Color) {
	switch s {
	case SafetySafe:
		return c.TextInverted, c.Success
	case SafetyLow:
		return c.TextInverted, c.Warning
	case SafetyMedium:
		return c.TextInverted, c.Accent
	case SafetyHigh:
		return c.TextInverted, c.DangerDim
	case SafetyCritical:
		return c.TextInverted, c.Danger
	default:
		return c.TextPrimary, c.SurfaceHover
	}
}

// Icon returns an icon character for the safety level.
func (s SafetyLevel) Icon() string {
	switch s {
	case SafetySafe:
		return "🛡️"
	case SafetyLow:
		return "⚡"
	case SafetyMedium:
		return "⚠️"
	case SafetyHigh:
		return "🔥"
	case SafetyCritical:
		return "⛔"
	default:
		return "●"
	}
}

// AccentColor returns the primary color representing this safety level.
func (s SafetyLevel) AccentColor(c ColorTokens) color.Color {
	switch s {
	case SafetySafe:
		return c.Success
	case SafetyLow:
		return c.Warning
	case SafetyMedium:
		return c.Accent
	case SafetyHigh:
		return c.DangerDim
	case SafetyCritical:
		return c.Danger
	default:
		return c.TextPrimary
	}
}

// ── Reusable Style Primitives ──

// SectionHeaderStyle returns a styled section title for grouping elements.
func SectionHeaderStyle(c ColorTokens) lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(c.Primary).
		Bold(true).
		PaddingLeft(2).
		MarginTop(1).
		MarginBottom(0)
}

// HelpTextStyle returns the standard help/keybinding text style.
func HelpTextStyle(c ColorTokens) lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(c.TextMuted)
}

// CodeBlockStyle returns a style for displaying code/tool commands.
func CodeBlockStyle(c ColorTokens, width int) lipgloss.Style {
	innerWidth := width - 6
	if innerWidth < 10 {
		innerWidth = 10
	}
	return lipgloss.NewStyle().
		Width(innerWidth).
		Padding(0, 1).
		Foreground(c.TextPrimary).
		Background(c.SurfaceRaised).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(c.Border).
		BorderBackground(c.SurfaceRaised)
}

// BadgeStyle returns a compact label style (e.g., "ACTIVE", provider tag).
func BadgeStyle(c ColorTokens, bgColor color.Color) lipgloss.Style {
	return lipgloss.NewStyle().
		Padding(0, 1).
		Foreground(c.TextInverted).
		Background(bgColor).
		Bold(true)
}

// FocusedButtonStyle creates a button-like style for focused items.
func FocusedButtonStyle(c ColorTokens, level SafetyLevel) lipgloss.Style {
	fg, bg := level.Colors(c)
	return lipgloss.NewStyle().
		Padding(0, 2).
		Foreground(fg).
		Background(bg).
		Bold(true).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(bg).
		BorderBackground(bg)
}

// NormalOptionStyle creates a muted style for non-focused option items.
func NormalOptionStyle(c ColorTokens, level SafetyLevel) lipgloss.Style {
	return lipgloss.NewStyle().
		Padding(0, 1).
		Foreground(c.TextSecondary).
		PaddingLeft(4)
}

// CursorIndicatorStyle returns a style for the cursor indicator "▶".
func CursorIndicatorStyle(c ColorTokens) lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(c.Primary).
		Bold(true)
}

// FocusBarStyle returns a style for the focused option's left indicator bar (┃).
func FocusBarStyle(c ColorTokens, level SafetyLevel) lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(level.AccentColor(c)).
		Bold(true)
}

// FocusedOptionTextStyle returns a style for the focused option's label text.
func FocusedOptionTextStyle(c ColorTokens) lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(c.TextPrimary).
		Bold(true)
}

// UnfocusedOptionTextStyle returns a style for the unfocused option's label text.
func UnfocusedOptionTextStyle(c ColorTokens) lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(c.TextMuted)
}

// RadioFocusedStyle returns a style for the focused radio button indicator (◉).
func RadioFocusedStyle(c ColorTokens, level SafetyLevel) lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(level.AccentColor(c)).
		Bold(true)
}

// RadioUnfocusedStyle returns a style for the unfocused radio button indicator (○).
func RadioUnfocusedStyle(c ColorTokens) lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(c.TextMuted)
}

// OptionLabelFocusedStyle returns a style for the focused option's label text.
func OptionLabelFocusedStyle(c ColorTokens) lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(c.TextPrimary).
		Bold(true)
}

// OptionLabelUnfocusedStyle returns a style for the unfocused option's label text.
func OptionLabelUnfocusedStyle(c ColorTokens) lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(c.TextMuted)
}

// OptionKeyFocusedStyle returns a style for the focused option's shortcut key.
func OptionKeyFocusedStyle(c ColorTokens, level SafetyLevel) lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(level.AccentColor(c))
}

// OptionKeyUnfocusedStyle returns a style for the unfocused option's shortcut key.
func OptionKeyUnfocusedStyle(c ColorTokens) lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(c.Border)
}

// FocusedKeyHintStyle returns a style for the focused option's key hint letter.
func FocusedKeyHintStyle(c ColorTokens, level SafetyLevel) lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(level.AccentColor(c))
}

// UnfocusedKeyHintStyle returns a style for the unfocused option's key hint letter.
func UnfocusedKeyHintStyle(c ColorTokens) lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(c.Border)
}

// DialogOverlayStyle returns a style that creates a dimmed overlay covering the screen.
func DialogOverlayStyle(c ColorTokens) lipgloss.Style {
	return lipgloss.NewStyle().
		Background(c.Overlay)
}

// DialogBorderStyle returns a rounded border style for dialogs.
func DialogBorderStyle(c ColorTokens) lipgloss.Style {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(c.BorderActive).
		BorderBackground(c.Surface).
		Background(c.Surface).
		Padding(1, 2)
}

// ── FuzzyMatch ──

// FuzzyMatchResult holds the result of a fuzzy search match.
type FuzzyMatchResult struct {
	Matched bool
	Indices []int // positions of matched runes in the target
	Score   int   // higher is better
}

// FuzzyMatch performs case-insensitive fuzzy matching.
// Characters in query must appear in order within target.
func FuzzyMatch(query, target string) FuzzyMatchResult {
	if query == "" {
		return FuzzyMatchResult{Matched: true, Indices: nil, Score: 0}
	}

	qRunes := []rune(query)
	tRunes := []rune(target)

	qi := 0
	var indices []int

	for ti, tr := range tRunes {
		if qi < len(qRunes) && toLowerRune(tr) == toLowerRune(qRunes[qi]) {
			indices = append(indices, ti)
			qi++
		}
	}

	if qi != len(qRunes) {
		return FuzzyMatchResult{Matched: false}
	}

	score := 0
	for i, idx := range indices {
		if i > 0 && indices[i] == indices[i-1]+1 {
			score += 15 // consecutive bonus
		}
		if idx == 0 {
			score += 10 // start-of-word bonus
		}
		if idx > 0 && (tRunes[idx-1] == ' ' || tRunes[idx-1] == '/' || tRunes[idx-1] == '-') {
			score += 8 // word-boundary bonus
		}
		pos := 5 - idx/3
		if pos < 1 {
			pos = 1
		}
		score += pos
	}

	return FuzzyMatchResult{
		Matched: true,
		Indices: indices,
		Score:   score,
	}
}

func toLowerRune(r rune) rune {
	if r >= 'A' && r <= 'Z' {
		return r + 32
	}
	return r
}

// HighlightMatch renders target with matched runes highlighted.
// matchedRunes should contain the indices of matched characters.
// If matchedRunes is nil or empty, target is returned as-is.
func HighlightMatch(target string, matchedRunes []int, matchStyle lipgloss.Style) string {
	if len(matchedRunes) == 0 {
		return target
	}

	runes := []rune(target)
	matchedSet := make(map[int]bool)
	for _, idx := range matchedRunes {
		if idx >= 0 && idx < len(runes) {
			matchedSet[idx] = true
		}
	}

	var result strings.Builder
	for i, r := range runes {
		if matchedSet[i] {
			result.WriteString(matchStyle.Render(string(r)))
		} else {
			result.WriteRune(r)
		}
	}

	return result.String()
}
