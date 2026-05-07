package tui

import "github.com/charmbracelet/lipgloss"

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
