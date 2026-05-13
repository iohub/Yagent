package tui

import (
	"fmt"
	"os"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func (m model) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}

	// History mode: render fullscreen history browser
	if m.historyMode {
		return renderHistoryView(&m)
	}

	// ====== 新组件：检查弹窗栈 ======
	if m.dialogStack != nil && m.dialogStack.Len() > 0 {
		overlay := m.dialogStack.Overlay(m.termWidth, m.termHeight)
		if overlay != "" {
			return tea.NewView(overlay)
		}
	}

	// When confirmation dialog is open (fallback for old dialog), render it as an overlay
	if m.confirmDialog.open {
		return tea.NewView(m.renderConfirmDialog())
	}

	// When help dialog is open in command mode (fallback for old dialog), render it
	if m.showHelpDialog {
		return tea.NewView(m.renderHelpDialog())
	}

	// When quit confirmation dialog is open (fallback for old dialog), render it
	if m.confirmQuitDialog.open {
		return tea.NewView(m.renderConfirmQuitDialog())
	}

	// When cancel task confirmation dialog is open (fallback for old dialog), render it
	if m.confirmCancelDialog.open {
		return tea.NewView(m.renderConfirmCancelDialog())
	}

	// When task complete dialog is open (fallback for old dialog), render it
	if m.taskCompleteDialog.open {
		return tea.NewView(m.renderTaskCompleteDialog())
	}

	var b strings.Builder

	// Main content area: scrollable viewport
	footerHeight := m.computeFooterHeight()
	vpHeight := m.termHeight - footerHeight
	if vpHeight < 3 {
		vpHeight = 3
	}
	if m.viewport.Height() != vpHeight {
		(&m.viewport).SetHeight(vpHeight)
	}
	b.WriteString(m.viewport.View())

	// Separator
	sepWidth := m.termWidth
	if sepWidth < 40 {
		sepWidth = 40
	}
	b.WriteString(logSeparatorStyle.Render(strings.Repeat("─", sepWidth)))
	b.WriteString("\n")

	// ── Input area: command mode hidden, edit mode with textarea ──
	var footer strings.Builder
	if !m.commandMode {
		// ── Edit mode: textarea with dark background (via Base style), no bar ──
		m.input.SetWidth(m.computeFieldWidth())
		m.input.SetHeight(m.computeInputHeight())
		inputLine := m.input.View()
		footer.WriteString(lipgloss.NewStyle().MarginTop(1).Render(inputLine))
		footer.WriteString("\n")

		// Inline skill autocomplete suggestions (below textarea)
		if m.skillAutoComplete && len(m.skillSuggestions) > 0 {
			suggestionStyle := lipgloss.NewStyle().
				Foreground(lipgloss.Color("245")).
				PaddingLeft(4)
			highlightSuggestionStyle := lipgloss.NewStyle().
				Foreground(lipgloss.Color("39")).
				Bold(true).
				PaddingLeft(4)

			var suggestionParts []string
			for i, name := range m.skillSuggestions {
				displayName := name
				if skill, ok := m.assistant.SkillRegistry.Get(name); ok && skill.Description != "" {
					displayName = name + " - " + skill.Description
				}
				if i == m.skillSuggestionIdx {
					suggestionParts = append(suggestionParts, highlightSuggestionStyle.Render("▶ "+displayName))
				} else {
					suggestionParts = append(suggestionParts, suggestionStyle.Render("  "+displayName))
				}
			}
			footer.WriteString(strings.Join(suggestionParts, "\n"))
			footer.WriteString("\n")
			// Hint line
			hintStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).PaddingLeft(4)
			footer.WriteString(hintStyle.Render("Tab 切换  Enter 选择并执行  Esc 关闭"))
			footer.WriteString("\n")
		}
	}

	// Error message
	if m.errMsg != "" {
		footer.WriteString(lipgloss.NewStyle().MarginLeft(2).Render(errorStyle.Render("✖ " + m.errMsg)))
		footer.WriteString("\n")
	}

	// Token consumption display (before status line)
	footer.WriteString(m.renderTokenDashboard())

	// Status line: [COMMAND] tag (if in command mode) + Running indicator + mode indicator
	var runningBadge string
	if m.taskRunning {
		if m.currentModel != "" {
			runningBadge = logStatusStyle.Render("Running [" + m.currentModel + "]...")
		} else {
			runningBadge = logStatusStyle.Render("Running...")
		}
	}

	// Ensure status line always starts on a new line
	footer.WriteString("\n")
	// Add extra spacing before status line: empty line in edit mode
	if !m.commandMode {
		footer.WriteString("\n")
	}

	var statusLine string
	if m.commandMode {
		commandTag := commandModeBarStyle.Render("[COMMAND]")
		// Running badge is now inside token dashboard, not in status line
		statusLine = footerStyle.Render(commandTag + " " + langManager.GetText("CommandModeIdleTips"))
	} else {
		if runningBadge != "" {
			statusLine = footerStyle.Render(runningBadge + " " + langManager.GetText("EditModeTips"))
		} else {
			statusLine = footerStyle.Render(langManager.GetText("EditModeTips"))
		}
	}
	// Compact margin for command mode: only left margin, no extra top/bottom spacing
	footer.WriteString(lipgloss.NewStyle().MarginTop(0).MarginBottom(0).MarginLeft(2).Render(statusLine))

	b.WriteString(footer.String())

	return tea.NewView(b.String())
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
	right.WriteString(welcomeDimStyle.Render("─── Self-Evolving Agents. Flawless Code."))
	right.WriteString("\n")

	// Compute responsive widths
	panelWidth := m.computeFieldWidth() + 4
	innerWidth := panelWidth - 6 // 2 border + 4 padding
	leftWidth := 38
	if innerWidth < 65 {
		// Narrow terminal: stack vertically
		boxInner := leftContent + "\n\n" + welcomeDimStyle.Render(strings.Repeat("─", 38)) + "\n\n" + right.String()
		return welcomePanelStyle.Width(innerWidth).Render(boxInner)
	}
	rightWidth := innerWidth - leftWidth - 3 // 3 for " │ "
	if rightWidth < 20 {
		rightWidth = 20
	}

	separator := welcomeDimStyle.Render(" │ ")

	leftStyled := lipgloss.NewStyle().Width(leftWidth).Render(leftContent)
	rightStyled := lipgloss.NewStyle().Width(rightWidth).Render(right.String())

	inner := lipgloss.JoinHorizontal(lipgloss.Top, leftStyled, separator, rightStyled)
	return welcomePanelStyle.Width(innerWidth).Render(inner)
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

// formatToken formats a token count with k/m suffixes (e.g. "1.2k", "1.5m")
func formatToken(n int64) string {
	switch {
	case n >= 1000000:
		return fmt.Sprintf("%.1fm", float64(n)/1000000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// renderTokenLine renders the token consumption line in the footer.
// Format: "In: 1.2k | Out: 3.5k"
func (m model) renderTokenLine() string {
	tokenStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("241")) // muted gray
	inStr := formatToken(m.inputTokens)
	outStr := formatToken(m.outputTokens)
	return tokenStyle.Render(fmt.Sprintf("In: %s | Out: %s", inStr, outStr))
}

// renderTokenDashboard renders a dashboard-style token consumption display.
// Shows total tokens in a highlighted row, followed by per-agent breakdown sorted by total.
func (m model) renderTokenDashboard() string {
	totalTokens := m.inputTokens + m.outputTokens
	if totalTokens == 0 {
		// No data: no task submitted yet, don't show useless info
		return ""
	}

	// Build dashboard with border
	dashStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("237")).
		Padding(0, 1).
		Width(m.termWidth - 2) // account for viewport padding

	// Header — left-aligned "Total" label + token summary
	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("240"))
	var header string

	// Token column width for alignment
	const maxAgentNameWidth = 10

	inStr := formatToken(m.inputTokens)
	outStr := formatToken(m.outputTokens)
	sumStr := formatToken(totalTokens)

	// Total line — highlighted
	inputStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("111")) // light blue for input
	outputStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("114")) // light green for output
	sumStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("243")) // medium gray for sum

	// Combined header with token summary
	header = headerStyle.Render(fmt.Sprintf("%-*s", maxAgentNameWidth, "Total")) + " " +
		inputStyle.Render(fmt.Sprintf("In: %s  ", inStr)) +
		outputStyle.Render(fmt.Sprintf("Out: %s  ", outStr))
	if m.cacheReadInputTokens > 0 {
		header += fmt.Sprintf("Cache: %s  ", formatToken(m.cacheReadInputTokens))
	}
	header += sumStyle.Render(fmt.Sprintf("Σ %s", sumStr))

	// Separator
	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("236"))

	// Per-agent rows — sort by total token count descending
	agents := make([]*AgentTokenUsage, 0, len(m.tokenUsagePerAgent))
	for _, au := range m.tokenUsagePerAgent {
		if au.InputTokens+au.OutputTokens > 0 {
			agents = append(agents, au)
		}
	}
	sort.Slice(agents, func(i, j int) bool {
		return (agents[i].InputTokens + agents[i].OutputTokens) > (agents[j].InputTokens + agents[j].OutputTokens)
	})

	var lines []string

	// In command mode with running task, show running badge at top of dashboard
	if m.commandMode && m.taskRunning {
		var runningLine string
		if m.currentModel != "" {
			runningLine = logStatusStyle.Render("Running [" + m.currentModel + "]...")
		} else {
			runningLine = logStatusStyle.Render("Running...")
		}
		lines = append(lines, runningLine)
	}
	lines = append(lines, header)
	lines = append(lines, sepStyle.Render(strings.Repeat("─", 48)))

	for _, au := range agents {
		agentLabel := au.AgentName
		if len(agentLabel) > maxAgentNameWidth {
			agentLabel = agentLabel[:maxAgentNameWidth-1] + "…"
		}
		// Pad agent name to fixed width
		paddedName := fmt.Sprintf("%-*s", maxAgentNameWidth, agentLabel)

		agentIn := formatToken(au.InputTokens)
		agentOut := formatToken(au.OutputTokens)

		// Dimmer style for agent rows
		agentStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
		agentInStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
		agentOutStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

		agentLine := agentStyle.Render(paddedName)
		agentLine += " " + agentInStyle.Render(fmt.Sprintf("In: %s  ", agentIn))
		agentLine += agentOutStyle.Render(fmt.Sprintf("Out: %s", agentOut))
		if au.CacheReadInputTokens > 0 {
			agentLine += "  " + agentInStyle.Render(fmt.Sprintf("Cache: %s", formatToken(au.CacheReadInputTokens)))
		}
		lines = append(lines, agentLine)
	}

	return dashStyle.Render(strings.Join(lines, "\n"))
}

// renderOverlay 渲染弹窗叠加层
// 如果 dialogStack 为空则返回空字符串，否则返回完整的覆盖层渲染结果
func (m model) renderOverlay() string {
	if m.dialogStack == nil || m.dialogStack.Len() == 0 {
		return ""
	}
	return m.dialogStack.Overlay(m.termWidth, m.termHeight)
}
