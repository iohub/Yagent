package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m model) View() string {
	if m.quitting {
		return ""
	}

	// When confirmation dialog is open, render it as an overlay on top of the normal view
	if m.confirmDialog.open {
		return m.renderConfirmDialog()
	}

	// When help dialog is open in command mode, render it as an overlay
	if m.showHelpDialog {
		return m.renderHelpDialog()
	}

	// When quit confirmation dialog is open, render it as an overlay
	if m.confirmQuitDialog.open {
		return m.renderConfirmQuitDialog()
	}

	// When cancel task confirmation dialog is open, render it as an overlay
	if m.confirmCancelDialog.open {
		return m.renderConfirmCancelDialog()
	}

	// When task complete dialog is open, render it as an overlay
	if m.taskCompleteDialog.open {
		return m.renderTaskCompleteDialog()
	}

	var b strings.Builder

	// Main content area: history panel or scrollable viewport
	if m.showHistoryPanel {
		b.WriteString(m.renderHistoryPanel())
	} else {
		b.WriteString(m.viewport.View())
	}

	// Separator
	sepWidth := m.termWidth
	if sepWidth < 40 {
		sepWidth = 40
	}
	b.WriteString(logSeparatorStyle.Render(strings.Repeat("─", sepWidth)))
	b.WriteString("\n")

	// ── Input area: edit mode vs command mode ──
	var footer strings.Builder

	if m.commandMode {
		// ── Command mode (vim-like): hidden input, ":" prefix, colored bar ──
		modeBar := lipgloss.NewStyle().
			Background(lipgloss.Color("214")).
			Foreground(lipgloss.Color("214")).
			Render("▊")

		var cmdLine string
		cmdPrefix := commandPrefixStyle.Render(" :")
		cmdLabel := commandLabelStyle.Render(" " + langManager.GetText("CommandModePrompt") + " ")
		if m.commandBuffer != "" {
			cmdBufDisplay := lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(m.commandBuffer)
			cmdCursor := lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Blink(true).Render("▊")
			cmdLine = modeBar + cmdPrefix + cmdLabel + " " + cmdBufDisplay + cmdCursor
		} else {
			cmdCursor := lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Blink(true).Render("▊")
			cmdLine = modeBar + cmdPrefix + cmdLabel + " " + cmdCursor
		}

		var cmdTips string
		if m.taskRunning {
			cmdTips = langManager.GetText("CommandModeTips")
		} else {
			cmdTips = langManager.GetText("CommandModeIdleTips")
		}
		cmdTipsLine := commandHintStyle.Render("  " + cmdTips)

		footer.WriteString(cmdLine + cmdTipsLine)
		footer.WriteString("\n")
	} else {
		// ── Edit mode: textarea with dark background (via Base style), no bar ──
		m.input.SetWidth(m.computeFieldWidth())
		inputLine := m.input.View()
		footer.WriteString(lipgloss.NewStyle().Render(inputLine))
		footer.WriteString("\n")
	}

	// Error message
	if m.errMsg != "" {
		footer.WriteString(lipgloss.NewStyle().MarginLeft(2).Render(errorStyle.Render("✖ " + m.errMsg)))
		footer.WriteString("\n")
	}

	// Status line: mode indicator + task indicator + model name
	taskIndicator := ""
	if m.taskRunning {
		if m.currentModel != "" {
			taskIndicator = logStatusStyle.Render(fmt.Sprintf(" ◷ Running [%s]...", m.currentModel))
		} else {
			taskIndicator = logStatusStyle.Render(" ◷ Running...")
		}
	}
	footer.WriteString("\n")
	var statusLine string
	if m.commandMode {
		statusLine = footerStyle.Render("COMMAND MODE")
	} else {
		statusLine = footerStyle.Render(langManager.GetText("EditModeTips"))
	}
	footer.WriteString(lipgloss.NewStyle().MarginLeft(2).Render(statusLine + taskIndicator))

	b.WriteString(footer.String())

	return b.String()
}

func (m model) renderWelcomePanel() string {
	// Build left panel: logo + cwd
	var left strings.Builder
	left.WriteString(renderBanner())
	left.WriteString("\n\n")
	cwd := m.projectDir
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(cwd, home) {
		cwd = "~" + strings.TrimPrefix(cwd, home)
	}
	left.WriteString(welcomeSubStyle.Render(cwd))

	leftContent := welcomeLeftStyle.Render(left.String())

	// Build right panel: recent activity
	var right strings.Builder
	right.WriteString(welcomeDimStyle.Render("─── Recent activity"))
	right.WriteString("\n")
	right.WriteString(welcomeDimStyle.Render("  Use Ctrl+H to browse history"))

	// Compute responsive widths
	panelWidth := m.computeFieldWidth() + 4
	innerWidth := panelWidth - 4 // 2 border + 2 padding
	leftWidth := 36
	if innerWidth < 65 {
		// Narrow terminal: stack vertically
		boxInner := leftContent + "\n\n" + welcomeDimStyle.Render(strings.Repeat("─", leftWidth)) + "\n\n" + right.String()
		return welcomePanelStyle.Width(panelWidth).Render(boxInner)
	}
	rightWidth := innerWidth - leftWidth - 3 // 3 for " │ "
	if rightWidth < 20 {
		rightWidth = 20
	}

	separator := welcomeDimStyle.Render(" │ ")

	leftStyled := lipgloss.NewStyle().Width(leftWidth).Render(leftContent)
	rightStyled := lipgloss.NewStyle().Width(rightWidth).Render(right.String())

	inner := lipgloss.JoinHorizontal(lipgloss.Top, leftStyled, separator, rightStyled)
	return welcomePanelStyle.Width(panelWidth).Render(inner)
}
func renderBanner() string {
	asciiLogo := []string{

		"╔═╗┌─┐┌┬┐┌─┐  ╔═╗┌─┐┌┬┐┌─┐┬─┐  ╔═╗╦",
		"║  │ │ ││├┤   ╠═╣│   │ │ │├┬┘  ╠═╣║",
		"╚═╝└─┘─┴┘└─┘  ╩ ╩└─┘ ┴ └─┘┴└─  ╩ ╩╩",
	}

	rainbowColors := []string{
		"167", "180", "221", "114", "75", "98", "176",
	}

	var rendered []string
	for _, line := range asciiLogo {
		var chars []string
		for i, r := range line {
			color := rainbowColors[i%len(rainbowColors)]
			style := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true)
			chars = append(chars, style.Render(string(r)))
		}
		rendered = append(rendered, lipgloss.JoinHorizontal(lipgloss.Top, chars...))
	}
	return bannerPadStyle.Render(lipgloss.JoinVertical(lipgloss.Left, rendered...))
}
