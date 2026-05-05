package tui

import "github.com/charmbracelet/lipgloss"

// ── Tool icon styles ──
var (
	IconPending   = lipgloss.NewStyle().Foreground(lipgloss.Color("108")).Render("●") // dim green
	IconSuccess   = lipgloss.NewStyle().Foreground(lipgloss.Color("114")).Render("✓") // green
	IconError     = lipgloss.NewStyle().Foreground(lipgloss.Color("167")).Render("×") // red
	IconCanceled  = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("●") // gray
)

// ── Tool name styles ──
var (
	NameNormal = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))  // blue
	NameNested = lipgloss.NewStyle().Foreground(lipgloss.Color("75"))  // lighter blue
)

// ── Parameter styles ──
var (
	ParamMain = lipgloss.NewStyle().Foreground(lipgloss.Color("247")) // light gray
	ParamKey  = lipgloss.NewStyle().Foreground(lipgloss.Color("245")) // dim gray
)

// ── Status message styles ──
var (
	StateRunning   = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	StateCanceled  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	StateWaiting   = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
)

// ── Error styles ──
var (
	ErrorTag     = lipgloss.NewStyle().Background(lipgloss.Color("167")).Foreground(lipgloss.Color("15")).Bold(true)
	ErrorMessage = lipgloss.NewStyle().Foreground(lipgloss.Color("167"))
)

// ── Body/content styles ──
var (
	Body           = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).PaddingLeft(2)
	ContentLine    = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).PaddingLeft(1)
	ContentTrunc   = lipgloss.NewStyle().Foreground(lipgloss.Color("243")).PaddingLeft(1)
)

// ── Diff styles ──
var (
	DiffAdd     = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	DiffDel     = lipgloss.NewStyle().Foreground(lipgloss.Color("167"))
	DiffHunk    = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	DiffHeader  = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true)
	DiffCtx     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
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
