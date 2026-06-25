// Package common provides shared UI components, styles, and utilities for the TUI.
package common

import (
	"fmt"

	"charm.land/lipgloss/v2"
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

	// Context compression
	CompactBadge  lipgloss.Style
	CompactToken  lipgloss.Style
	CompactArrow  lipgloss.Style

	// Airline status bar
	AirlineNormalMode  lipgloss.Style
	AirlineRunMode     lipgloss.Style
	AirlineCommandMode lipgloss.Style
	AirlineInfo        lipgloss.Style
	AirlineInfoAlt     lipgloss.Style
	AirlineAccent      lipgloss.Style
	AirlineFiller      lipgloss.Style

	// Dynamic styles
	CompactRatio func(ratio float64) lipgloss.Style
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

		CompactBadge: lipgloss.NewStyle().
			Background(lipgloss.Color("23")).
			Foreground(lipgloss.Color("15")).
			Bold(true).
			Padding(0, 1),
		CompactToken: lipgloss.NewStyle().Foreground(lipgloss.Color("247")),
		CompactArrow: lipgloss.NewStyle().Foreground(lipgloss.Color("243")),

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

		CompactRatio: func(ratio float64) lipgloss.Style {
			var c string
			switch {
			case ratio < 30:
				c = "114"
			case ratio < 60:
				c = "228"
			default:
				c = "167"
			}
			return lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Bold(true)
		},
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

		CompactBadge: lipgloss.NewStyle().
			Background(lipgloss.Color("30")).
			Foreground(lipgloss.Color("15")).
			Bold(true).
			Padding(0, 1),
		CompactToken: lipgloss.NewStyle().Foreground(lipgloss.Color("242")),
		CompactArrow: lipgloss.NewStyle().Foreground(lipgloss.Color("244")),

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

		CompactRatio: func(ratio float64) lipgloss.Style {
			var c string
			switch {
			case ratio < 30:
				c = "28"
			case ratio < 60:
				c = "178"
			default:
				c = "160"
			}
			return lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Bold(true)
		},
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
