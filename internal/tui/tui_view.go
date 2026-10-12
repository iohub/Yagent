package tui

import (
	"fmt"
	"image/color"
	"os"
	"sort"
	"strings"

	"yagent/internal/tui/common"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	powerlineRightSep = "\ue0b0"
	powerlineLeftSep  = "\ue0b2"
)

func makeRightSep(prevBg, nextBg color.Color) string {
	return lipgloss.NewStyle().
		Foreground(prevBg).
		Background(nextBg).
		Render(powerlineRightSep)
}

func makeRightSepToEnd(prevBg color.Color) string {
	return lipgloss.NewStyle().
		Foreground(prevBg).
		Render(powerlineRightSep)
}

func makeLeftSep(leftBg, rightBg color.Color) string {
	return lipgloss.NewStyle().
		Foreground(rightBg).
		Background(leftBg).
		Render(powerlineLeftSep)
}

func (m *model) View() tea.View {
	if m.quitting {
		return tea.View{AltScreen: true}
	}

	if m.termWidth <= 0 || m.termHeight <= 0 {
		return tea.View{AltScreen: true}
	}

	if m.timelineFullscreenMode {
		return renderTimelineFullscreenView(m)
	}

	if m.thinklinkMode {
		return renderThinklinkFullscreenView(m)
	}

	if m.dialogStack != nil && m.dialogStack.Len() > 0 {
		overlay := m.dialogStack.Overlay(m.termWidth, m.termHeight)
		if overlay != "" {
			return tea.View{AltScreen: true, Content: overlay}
		}
	}

	if m.historyMode {
		return renderHistoryView(m)
	}

	if m.resumeMode {
		return renderResumeView(m)
	}

	var b strings.Builder

	footerHeight := m.computeFooterHeight()
	vpHeight := m.termHeight - footerHeight - tabBarHeight
	if vpHeight < 3 {
		vpHeight = 3
	}
	if m.viewport.Height() != vpHeight {
		heightWasAtBottom := m.viewport.AtBottom()
		m.viewport.SetHeight(vpHeight)
		m.viewportViewValid = false
		if heightWasAtBottom {
			m.viewport.GotoBottom()
		}
	}

	scrollbarWidth := 0
	totalLines := m.viewport.TotalLineCount()
	if totalLines > vpHeight {
		scrollbarWidth = 1
	}
	dashW := m.dashboardWidth()
	contentWidth := m.termWidth - scrollbarWidth - dashW

	if m.hasDirtyEntries() || contentWidth != m.prevViewportWidth ||
		len(m.contentParts) != len(m.logEntries) {
		if m.viewport.Width() != contentWidth {
			m.viewport.SetWidth(contentWidth)
		}
		wasAtBottom := m.viewport.AtBottom()
		m.rebuildContentCache()
		if wasAtBottom {
			m.viewport.GotoBottom()
		}
		m.viewportViewValid = false
	}

	if !m.viewportViewValid {
		m.cachedViewportView = m.viewport.View()
		m.viewportViewValid = true
		m.prevViewportYOffset = m.viewport.YOffset()
		m.prevViewportHeight = m.viewport.Height()
	}

	if dashW > 0 {
		_ = m.renderDashboard(dashW, vpHeight)
	}

	b.WriteString(m.renderTabBar() + "\n")

	if scrollbarWidth > 0 {
		sbStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("62"))
		trackStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("236"))
		scrollbar := common.Scrollbar(sbStyle, trackStyle,
			vpHeight, totalLines, vpHeight, m.viewport.YOffset())

		vpLines := strings.Split(m.cachedViewportView, "\n")
		sbLines := strings.Split(scrollbar, "\n")

		var combinedLines []string
		maxLines := len(vpLines)
		if len(sbLines) > maxLines {
			maxLines = len(sbLines)
		}
		if dashW > 0 {
			dashLines := strings.Split(m.dashboardCache, "\n")
			if len(dashLines) > maxLines {
				maxLines = len(dashLines)
			}
			for i := 0; i < maxLines; i++ {
				vpLine := ""
				sbLine := ""
				dashLine := ""
				if i < len(vpLines) {
					vpLine = vpLines[i]
				}
				if i < len(sbLines) {
					sbLine = sbLines[i]
				}
				if i < len(dashLines) {
					dashLine = dashLines[i]
				}
				vpPadded := lipgloss.NewStyle().Width(contentWidth).Render(vpLine)
				combinedLines = append(combinedLines, vpPadded+sbLine+dashLine)
			}
		} else {
			for i := 0; i < maxLines; i++ {
				vpLine := ""
				sbLine := ""
				if i < len(vpLines) {
					vpLine = vpLines[i]
				}
				if i < len(sbLines) {
					sbLine = sbLines[i]
				}
				vpPadded := lipgloss.NewStyle().Width(contentWidth).Render(vpLine)
				combinedLines = append(combinedLines, vpPadded+sbLine)
			}
		}
		b.WriteString(strings.Join(combinedLines, "\n"))
	} else if dashW > 0 {
		dashLines := strings.Split(m.dashboardCache, "\n")
		vpLines := strings.Split(m.cachedViewportView, "\n")
		maxLines := len(vpLines)
		if len(dashLines) > maxLines {
			maxLines = len(dashLines)
		}
		for i := 0; i < maxLines; i++ {
			vpLine := ""
			dashLine := ""
			if i < len(vpLines) {
				vpLine = vpLines[i]
			}
			if i < len(dashLines) {
				dashLine = dashLines[i]
			}
			vpPadded := lipgloss.NewStyle().Width(contentWidth).Render(vpLine)
			b.WriteString(vpPadded + dashLine)
			if i < maxLines-1 {
				b.WriteString("\n")
			}
		}
	} else {
		b.WriteString(m.cachedViewportView)
	}

	if !m.dashboardVisible() && (m.taskRunning || len(m.timelineEntries) > 0) {
		timelineContent := m.renderTimelinePanel(m.termWidth)
		if timelineContent != "" {
			b.WriteString("\n")
			b.WriteString(timelineContent)
		}
	}

	if m.cachedSeparator == "" {
		sepWidth := m.termWidth
		if sepWidth < 40 {
			sepWidth = 40
		}
		m.cachedSeparator = inputSeparatorStyle.Render(strings.Repeat("─", sepWidth))
	}
	b.WriteString(m.cachedSeparator)
	b.WriteString("\n")

	var footer strings.Builder
	if !m.commandMode {
		m.input.SetWidth(m.computeFieldWidth())
		m.input.SetHeight(m.computeInputHeight())
		inputLine := m.input.View()
		panelStyle := inputPanelIdleStyle
		if m.taskStarted {
			panelStyle = inputPanelStyle
		}
		footer.WriteString(panelStyle.Render(inputLine))
		footer.WriteString("\n")

		if m.skillAutoComplete && len(m.skillSuggestions) > 0 {
			var suggestionParts []string
			for i, name := range m.skillSuggestions {
				displayName := name
				if skill, ok := m.assistant.SkillRegistry.Get(name); ok && skill.Description != "" {
					displayName = name + " - " + skill.Description
				}
				if i == m.skillSuggestionIdx {
					suggestionParts = append(suggestionParts, m.skillHighlightStyle.Render("▶ "+displayName))
				} else {
					suggestionParts = append(suggestionParts, m.skillSuggestionStyle.Render("  "+displayName))
				}
			}
			footer.WriteString(strings.Join(suggestionParts, "\n"))
			footer.WriteString("\n")
			footer.WriteString(m.skillHintStyle.Render("Tab 切换  Enter 选择  Esc 关闭"))
			footer.WriteString("\n")
		}
	}

	if m.keywordAutoComplete && len(m.keywordSuggestions) > 0 {
		var suggestionParts []string
		for i, suggestion := range m.keywordSuggestions {
			if i == m.keywordSuggestionIdx {
				suggestionParts = append(suggestionParts,
					m.keywordHighlightStyle.Render(suggestion))
			} else {
				suggestionParts = append(suggestionParts,
					m.keywordSuggestionStyle.Render(suggestion))
			}
		}
		footer.WriteString(strings.Join(suggestionParts, "  "))
		footer.WriteString("\n")
	}

	if m.errMsg != "" {
		footer.WriteString(lipgloss.NewStyle().MarginLeft(2).Render(errorStyle.Render("✖ " + m.errMsg)))
		footer.WriteString("\n")
	}

	if !m.dashboardVisible() {
		tokenDash := m.cachedTokenDashboard
		if !m.tokenDashboardValid {
			tokenDash = m.renderTokenDashboard()
		}
		footer.WriteString(tokenDash)
	}

	statusBar := m.cachedStatusBar
	if !m.statusBarValid {
		statusBar = m.renderAirlineStatusBar()
	}
	footer.WriteString("\n")
	if !m.commandMode {
		footer.WriteString("\n")
	}
	footer.WriteString(statusBar)

	b.WriteString(footer.String())

	return tea.View{AltScreen: true, Content: b.String()}
}

func (m *model) renderTimelinePanel(width int) string {
	hasRunning := false
	for _, e := range m.timelineEntries {
		if e.Status == ToolStatusRunning {
			hasRunning = true
			break
		}
	}

	var lastID string
	if len(m.timelineEntries) > 0 {
		lastID = m.timelineEntries[len(m.timelineEntries)-1].ID
	}
	key := fmt.Sprintf("%d|%s|%d", len(m.timelineEntries), lastID, width)

	if !hasRunning && key == m.timelineCacheKey && m.timelineCache != "" {
		return m.timelineCache
	}

	content := RenderTimeline(m.timelineEntries, false, true, width-4, m.anim)
	if content == "" {
		return ""
	}

	lines := strings.Split(content, "\n")
	if len(lines) > 1 {
		content = strings.Join(lines[1:], "\n")
	} else {
		return ""
	}

	hint := timelineHintStyle.Render(" ctrl+l " + langManager.GetText("TimelineDetailHint") + " ")

	panelContent := content + "\n" + hint

	panelWidth := width - 2
	if panelWidth < 30 {
		panelWidth = 30
	}
	rendered := timelinePanelStyle.Width(panelWidth).Render(panelContent)

	m.timelineCache = rendered
	m.timelineCacheKey = key
	return m.timelineCache
}

func (m *model) renderAirlineStatusBar() string {
	width := m.termWidth
	if width <= 0 {
		width = 80
	}

	var modeSeg string
	var modeBg color.Color
	var tipsText string

	gradModeStyle := lipgloss.NewStyle().Bold(true).Padding(0, 1)

	if m.commandMode {
		modeSeg = gradModeStyle.
			Background(airlineColorCmdBg).
			Foreground(lipgloss.Color("15")).
			Render("COMMAND")
		modeBg = airlineColorCmdBg
		tipsText = langManager.GetText("CommandModeIdleTips")
	} else if m.taskCancelled {
		modeText := lipgloss.NewStyle().
			Background(airlineColorCancelBg).
			Foreground(airlineColorCancelFg).
			Bold(true).
			Padding(0, 1).
			Render("● CANCELLED")
		modeSeg = modeText
		modeBg = airlineColorCancelBg
		tipsText = langManager.GetText("EditModeTips")
	} else if m.taskRunning {
		modeText := common.ApplyBoldForegroundGrad(
			lipgloss.NewStyle().Background(airlineColorRunBg),
			"● RUN",
			common.DefaultGradFrom,
			common.DefaultGradTo,
		)
		modeSeg = modeText
		modeBg = airlineColorRunBg
		tipsText = langManager.GetText("EditModeTips")
	} else {
		modeSeg = gradModeStyle.
			Background(airlineColorNormalBg).
			Foreground(lipgloss.Color("15")).
			Render("NORMAL")
		modeBg = airlineColorNormalBg
		tipsText = langManager.GetText("EditModeTips")
	}

	if m.dashboardCollapsed && m.termWidth >= 120 {
		tipsText += "  alt+d " + langManager.GetText("DashboardExpandHint")
	}
	leftPart := modeSeg + makeRightSep(modeBg, airlineColorInfoBg)

	type segDef struct {
		bg    color.Color
		style lipgloss.Style
		text  string
	}

	var rightSegs []segDef

	if m.autoRouting {
		rightSegs = append(rightSegs, segDef{
			bg: airlineColorRunBg,
			style: lipgloss.NewStyle().
				Background(airlineColorRunBg).
				Foreground(airlineColorRunFg).
				Bold(true).
				Padding(0, 1),
			text: "⚡ AUTO",
		})
	}

	modelDisplay := ""
	if m.currentProvider != "" && m.currentModel != "" {
		modelDisplay = m.currentProvider + "/" + m.currentModel
	} else if m.currentModel != "" {
		modelDisplay = m.currentModel
	} else if m.currentProvider != "" {
		modelDisplay = m.currentProvider
	}
	if modelDisplay != "" {
		rightSegs = append(rightSegs, segDef{
			bg:    airlineColorAccentBg,
			style: airlineAccentStyle,
			text:  modelDisplay,
		})
	}

	if m.taskRunning && !m.commandMode {
		if m.currentAgent != "" {
			rightSegs = append(rightSegs, segDef{
				bg:    airlineColorInfoAltBg,
				style: airlineInfoAltStyle,
				text:  m.currentAgent,
			})
		}
		rightSegs = append(rightSegs, segDef{
			bg:    airlineColorInfoBg,
			style: airlineInfoStyle,
			text:  m.anim.Render(),
		})
	}

	var rightPart string
	if len(rightSegs) > 0 {
		var rParts []string
		rParts = append(rParts, makeLeftSep(airlineColorInfoBg, rightSegs[0].bg))
		for i, seg := range rightSegs {
			rParts = append(rParts, seg.style.Render(seg.text))
			if i < len(rightSegs)-1 {
				rParts = append(rParts, makeLeftSep(seg.bg, rightSegs[i+1].bg))
			}
		}
		rightPart = strings.Join(rParts, "")
	}

	leftRenderedWidth := lipgloss.Width(leftPart)
	if leftRenderedWidth >= width {
		return modeSeg + makeRightSepToEnd(modeBg)
	}

	rightRenderedWidth := lipgloss.Width(rightPart)
	fillerWidth := width - leftRenderedWidth - rightRenderedWidth
	if fillerWidth < 0 {
		rightPart = ""
		rightRenderedWidth = 0
		fillerWidth = width - leftRenderedWidth
		if fillerWidth < 0 {
			fillerWidth = 0
		}
	}

	fillerPart := airlineFillerStyle.Width(fillerWidth).Render(tipsText)

	var endSep string
	if len(rightSegs) == 0 {
		endSep = makeRightSepToEnd(airlineColorInfoBg)
	}

	return leftPart + fillerPart + endSep + rightPart
}

func (m *model) renderWelcomePanel() string {
	if len(m.logEntries) == 0 {
		vpHeight := m.termHeight - m.computeFooterHeight()
		if vpHeight < 8 {
			vpHeight = 8
		}
		width := m.termWidth
		if width <= 0 {
			width = 80
		}
		return m.renderCenteredStartupScreen(width, vpHeight)
	}
	return m.renderWelcomePanelLayout()
}

func (m *model) renderCenteredStartupScreen(width, height int) string {
	banner := renderBanner()

	cwd := m.projectDir
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(cwd, home) {
		cwd = "~" + strings.TrimPrefix(cwd, home)
	}

	var block strings.Builder
	block.WriteString(banner)
	block.WriteString("\n")
	tagline := common.ApplyGrad("A Repository-Aware, Self-Evolving Agent")
	block.WriteString(tagline)
	block.WriteString("\n\n")
	block.WriteString(welcomeSubStyle.Render(cwd))

	return lipgloss.Place(
		width,
		height,
		lipgloss.Center,
		lipgloss.Center,
		block.String(),
	)
}

func (m *model) renderWelcomePanelLayout() string {
	cwd := m.projectDir
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(cwd, home) {
		cwd = "~" + strings.TrimPrefix(cwd, home)
	}
	cwdLine := welcomeSubStyle.Render(cwd)

	banner := renderBanner()
	var left strings.Builder
	left.WriteString(banner)
	left.WriteString("\n\n")

	leftContent := left.String()

	var right strings.Builder
	tagline := common.ApplyGrad("─── A Repository-Aware, Self-Evolving Agent")
	right.WriteString(tagline)
	right.WriteString("\n")

	panelWidth := m.computeFieldWidth() + 4
	innerWidth := panelWidth - 4
	leftWidth := max(lipgloss.Width(banner), 38)
	narrowThreshold := max(65, leftWidth+23)
	if innerWidth < narrowThreshold {
		boxInner := leftContent + "\n\n" + welcomeDimStyle.Render(strings.Repeat("─", 38)) + "\n\n" + right.String()
		boxInner += "\n\n" + cwdLine
		return welcomePanelStyle.Width(innerWidth).Render(boxInner)
	}
	rightWidth := innerWidth - leftWidth - 3
	if rightWidth < 20 {
		rightWidth = 20
	}

	separator := welcomeDimStyle.Render(" │ ")

	leftStyled := lipgloss.NewStyle().Width(leftWidth).Render(leftContent)
	rightStyled := lipgloss.NewStyle().Width(rightWidth).Render(right.String())

	inner := lipgloss.JoinHorizontal(lipgloss.Top, leftStyled, separator, rightStyled)
	inner += "\n\n" + cwdLine
	return welcomePanelStyle.Width(innerWidth).Render(inner)
}
func renderBanner() string {
	asciiLogo := []string{
		"██  ██   ▄████▄  ▄▄▄▄ ▄▄▄▄▄ ▄▄  ▄▄ ▄▄▄▄▄▄",
		" ▀██▀    ██▄▄██ ██ ▄▄ ██▄▄  ███▄██   ██",
		"  ██     ██  ██ ▀███▀ ██▄▄▄ ██ ▀██   ██",
	}

	gradFrom := common.DefaultGradFrom
	gradTo := common.DefaultGradTo

	var rendered []string
	for _, line := range asciiLogo {
		gradLine := common.ApplyBoldForegroundGrad(
			lipgloss.NewStyle(),
			line,
			gradFrom,
			gradTo,
		)
		rendered = append(rendered, gradLine)
	}
	return bannerPadStyle.Render(lipgloss.JoinVertical(lipgloss.Left, rendered...))
}

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

func formatCacheInfo(cacheRead, cacheCreation, totalInput int64) string {
	if totalInput <= 0 {
		return ""
	}
	var parts []string
	if cacheRead > 0 {
		rate := float64(cacheRead) / float64(totalInput) * 100
		parts = append(parts, fmt.Sprintf("Cache: %.1f%%(%s)", rate, formatToken(cacheRead)))
	}
	if cacheCreation > 0 {
		parts = append(parts, fmt.Sprintf("CacheW: %s", formatToken(cacheCreation)))
	}
	return strings.Join(parts, " ")
}

func (m *model) renderTokenDashboard() string {
	if m.tokenDashboardCollapsed {
		return m.renderCollapsedTokenDashboard()
	}

	totalTokens := m.inputTokens + m.outputTokens
	if totalTokens == 0 {
		return ""
	}

	dashStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("237")).
		Padding(0, 1).
		Width(m.termWidth - 2)

	headerStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("240"))
	var header string

	const maxAgentNameWidth = 10

	inStr := formatToken(m.inputTokens)
	outStr := formatToken(m.outputTokens)
	sumStr := formatToken(totalTokens)
	totalInput := m.totalInputTokens
	if totalInput == 0 {
		totalInput = m.inputTokens + m.cacheReadInputTokens + m.cacheCreationInputTokens
	}

	inputStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("111"))
	outputStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("114"))
	sumStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("243"))

	header = headerStyle.Render(fmt.Sprintf("%-*s", maxAgentNameWidth, "Total")) + " " +
		inputStyle.Render(fmt.Sprintf("In: %s  ", inStr)) +
		outputStyle.Render(fmt.Sprintf("Out: %s  ", outStr))
	if cacheInfo := formatCacheInfo(m.cacheReadInputTokens, m.cacheCreationInputTokens, totalInput); cacheInfo != "" {
		header += inputStyle.Render(cacheInfo + "  ")
	}
	header += sumStyle.Render(fmt.Sprintf("Σ %s", sumStr))

	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("236"))

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
	lines = append(lines, header)
	lines = append(lines, sepStyle.Render(strings.Repeat("─", 48)))

	for _, au := range agents {
		agentLabel := au.AgentName
		if len(agentLabel) > maxAgentNameWidth {
			agentLabel = agentLabel[:maxAgentNameWidth-1] + "…"
		}
		paddedName := fmt.Sprintf("%-*s", maxAgentNameWidth, agentLabel)

		agentIn := formatToken(au.InputTokens)
		agentOut := formatToken(au.OutputTokens)

		nameStyle := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(AgentColor(au.AgentName)))
		agentInStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
		agentOutStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

		agentLine := nameStyle.Render(paddedName)
		agentLine += " " + agentInStyle.Render(fmt.Sprintf("In: %s  ", agentIn))
		agentLine += agentOutStyle.Render(fmt.Sprintf("Out: %s", agentOut))
		auTotalInput := au.TotalInputTokens
		if auTotalInput == 0 {
			auTotalInput = au.InputTokens + au.CacheReadInputTokens + au.CacheCreationInputTokens
		}
		if cacheInfo := formatCacheInfo(au.CacheReadInputTokens, au.CacheCreationInputTokens, auTotalInput); cacheInfo != "" {
			agentLine += "  " + agentInStyle.Render(cacheInfo)
		}
		lines = append(lines, agentLine)
	}

	if ctxLine := renderContextLine(m.currentAgentRunTokens.ContextTokens, m.contextWindow, m.termWidth-6); ctxLine != "" {
		lines = append(lines, ctxLine)
	}

	return dashStyle.Render(strings.Join(lines, "\n"))
}

func (m *model) renderCollapsedTokenDashboard() string {
	rt := m.currentAgentRunTokens

	if rt.InputTokens == 0 && rt.OutputTokens == 0 && rt.ContextTokens == 0 {
		return ""
	}

	agentName := rt.AgentName
	if agentName == "" {
		agentName = "—"
	}

	totalInput := rt.TotalInputTokens
	if totalInput == 0 {
		totalInput = rt.InputTokens + rt.CacheReadInputTokens + rt.CacheCreationInputTokens
	}

	inStr := formatToken(totalInput)
	outStr := formatToken(rt.OutputTokens)

	cacheStr := formatCacheInfo(rt.CacheReadInputTokens, rt.CacheCreationInputTokens, totalInput)

	dashStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("237")).
		Padding(0, 1).
		Width(m.termWidth - 2)

	style := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("240"))
	inputStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111"))
	outputStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("114"))

	line := style.Render(fmt.Sprintf("[%s]", agentName)) + " " +
		inputStyle.Render(fmt.Sprintf("In: %s  ", inStr)) +
		outputStyle.Render(fmt.Sprintf("Out: %s  ", outStr))

	if cacheStr != "" {
		line += inputStyle.Render(cacheStr + "  ")
	}

	line += style.Render("(alt+t: expand)")

	lines := []string{line}
	if ctxLine := renderContextLine(rt.ContextTokens, m.contextWindow, m.termWidth-6); ctxLine != "" {
		lines = append(lines, ctxLine)
	}

	return dashStyle.Render(strings.Join(lines, "\n"))
}
