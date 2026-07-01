package tui

import (
	"fmt"

	"charm.land/lipgloss/v2"
)

// ── Tool icon styles ──
var (
	IconPending  = lipgloss.NewStyle().Foreground(lipgloss.Color("108")).Render("●") // dim green
	IconSuccess  = lipgloss.NewStyle().Foreground(lipgloss.Color("114")).Render("✓") // green
	IconError    = lipgloss.NewStyle().Foreground(lipgloss.Color("167")).Render("×") // red
	IconCanceled = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("●") // gray
)

// ── Tool name background color palette ──
// Each tool gets a distinct background color based on its name hash.
// All colors are from the 256-color palette, chosen to be dark enough
// for white (color 15) text to remain readable.
var toolBgColors = []string{
	"52",  // dark red
	"17",  // dark blue
	"22",  // dark green
	"94",  // brown
	"53",  // plum
	"18",  // navy
	"23",  // teal
	"58",  // olive
	"95",  // mauve
	"24",  // steel blue
	"88",  // crimson
	"59",  // slate
	"131", // purple
	"60",  // blue-gray
	"96",  // sage
	"97",  // warm gray
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

// DisplayToolName maps internal tool names to their display names in the TUI.
// This allows us to show a user-friendly name without changing the actual tool definition.
func DisplayToolName(name string) string {
	switch name {
	case "search_replace_in_file":
		return "edit_file"
	default:
		return name
	}
}

// ── Tool name styles ──
var (
	NameNormal = lipgloss.NewStyle().Foreground(lipgloss.Color("39")) // blue
	NameNested = lipgloss.NewStyle().Foreground(lipgloss.Color("75")) // lighter blue
)

// ── Parameter styles ──
var (
	ParamMain = lipgloss.NewStyle().Foreground(lipgloss.Color("247")) // light gray
	ParamKey  = lipgloss.NewStyle().Foreground(lipgloss.Color("245")) // dim gray
)

// ── Status message styles ──
var (
	StateRunning  = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	StateCanceled = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	StateWaiting  = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
)

// ── Error styles ──
var (
	ErrorTag     = lipgloss.NewStyle().Background(lipgloss.Color("167")).Foreground(lipgloss.Color("15")).Bold(true)
	ErrorMessage = lipgloss.NewStyle().Foreground(lipgloss.Color("167"))
)

// ── Body/content styles ──
var (
	Body         = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).PaddingLeft(2)
	ContentLine  = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).PaddingLeft(1)
	ContentTrunc = lipgloss.NewStyle().Foreground(lipgloss.Color("243")).PaddingLeft(1)
)

// ── Diff styles ──
var (
	DiffAdd       = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	DiffDel       = lipgloss.NewStyle().Foreground(lipgloss.Color("167"))
	DiffHunk      = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	DiffHeader    = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true)
	DiffCtx       = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	DiffNoNewline = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
)

// ── Prefix styles for different message types ──
var (
	TimeStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Faint(true)
	AIResStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	StatusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("36"))
	ErrorStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("167")).Bold(true)
	HelpStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("228"))
)

// ── Tool call area borders ──
var (
	ToolCallBorderTop    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	ToolCallBorderBottom = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
)

// ── Separator ──
var SeparatorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("237"))

// ── Timeline panel (floating panel) styles ──
// timelinePanelStyle 悬浮面板样式（类似 tokenDashboard）
var timelinePanelStyle = lipgloss.NewStyle().
	BorderStyle(lipgloss.NormalBorder()).
	BorderForeground(lipgloss.Color("62")).
	Padding(0, 1)

// timelineHintStyle 提示行样式
var timelineHintStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("241")).
	Italic(true)

// ── Animation styles ──
var (
	AnimGradFrom = lipgloss.Color("240")
	AnimGradTo   = lipgloss.Color("250")
)

// ToolIcon returns the appropriate icon string for a tool status.
func ToolIcon(status ToolStatus, nested bool) string {
	switch status {
	case ToolStatusSuccess:
		return IconSuccess
	case ToolStatusError:
		return IconError
	case ToolStatusCanceled:
		return IconCanceled
	case ToolStatusRunning, ToolStatusPending:
		return IconPending
	default:
		return IconPending
	}
}

// ToolNameStyle returns the name style for a given nesting level.
func ToolNameStyle(nested bool) lipgloss.Style {
	if nested {
		return NameNested
	}
	return NameNormal
}

// inputPanelIdleStyle is used for the input panel border when no task has been
// submitted yet — a dim, non-highlighted gray border.
var inputPanelIdleStyle = lipgloss.NewStyle().
	Border(lipgloss.NormalBorder()).
	BorderForeground(lipgloss.Color("237")). // very dim gray, barely visible
	Padding(0, 1).
	MarginTop(1)

// ── Context compression styles ──
var (
	CompactBadgeStyle = lipgloss.NewStyle().
				Background(lipgloss.Color("23")).
				Foreground(lipgloss.Color("15")).
				Bold(true).
				Padding(0, 1)

	CompactTokenStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("247"))
	CompactArrowStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
)

// CompactRatioStyle returns a style with color based on compression ratio.
func CompactRatioStyle(ratio float64) lipgloss.Style {
	var color string
	switch {
	case ratio < 30:
		color = "114"
	case ratio < 60:
		color = "228"
	default:
		color = "167"
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true)
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

// ── Agent color palette ──
// Each agent gets a distinct foreground color for its compact tag.
// Colors are 256-color ANSI codes, chosen for readability on dark terminals.
var agentColors = []string{
	"135", // soft purple
	"75",  // soft blue
	"108", // muted green
	"214", // warm gold
	"141", // lavender
	"73",  // teal
	"209", // coral
	"186", // olive
	"140", // mauve
	"117", // light blue
}

// AgentColor returns a stable foreground color for the given agent name.
// Uses the same hash function pattern as ToolBgColor.
func AgentColor(name string) string {
	if name == "" {
		return "240" // dim gray for empty/unknown
	}
	h := 0
	for _, c := range name {
		h = h*31 + int(c)
	}
	if h < 0 {
		h = -h
	}
	return agentColors[h%len(agentColors)]
}

// ── Thinking styles ──
// Purple icon and warm italic text for agent thinking/reasoning content.
var (
	thinkIconStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("141")).SetString("💭 ")
	thinkTextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("251")).Italic(true).Faint(true)
	thinkDimStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
)
