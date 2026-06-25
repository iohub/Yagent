package tui

import (
	"fmt"
	"image/color"
	"os"
	"sort"
	"strings"

	"codeactor/internal/tui/common"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// =============================================================================
// Powerline separator helpers
// =============================================================================

const (
	powerlineRightSep = "\ue0b0" //  — right-pointing solid triangle
	powerlineLeftSep  = "\ue0b2" //  — left-pointing solid triangle
)

// makeRightSep renders  transitioning from prevBg (left) to nextBg (right).
// The triangle uses prevBg as foreground fill, nextBg as background.
func makeRightSep(prevBg, nextBg color.Color) string {
	return lipgloss.NewStyle().
		Foreground(prevBg).
		Background(nextBg).
		Render(powerlineRightSep)
}

// makeRightSepToEnd renders  transitioning from prevBg to terminal default background.
func makeRightSepToEnd(prevBg color.Color) string {
	return lipgloss.NewStyle().
		Foreground(prevBg).
		Render(powerlineRightSep)
}

// makeLeftSep renders  for right-side segments.
// The triangle uses rightBg as foreground fill, leftBg as background.
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

	// 新增：在终端尺寸初始化前返回空视图
	if m.termWidth <= 0 || m.termHeight <= 0 {
		return tea.View{AltScreen: true}
	}

	// 全屏 timeline 模式：分屏展示时间线条目和详细内容
	if m.timelineFullscreenMode {
		return renderTimelineFullscreenView(m)
	}

	// ====== Dialog overlay: takes priority over history mode ======
	if m.dialogStack != nil && m.dialogStack.Len() > 0 {
		overlay := m.dialogStack.Overlay(m.termWidth, m.termHeight)
		if overlay != "" {
			return tea.View{AltScreen: true, Content: overlay}
		}
	}

	// History mode: render fullscreen history browser
	if m.historyMode {
		return renderHistoryView(m)
	}

	var b strings.Builder

	// Main content area: scrollable viewport
	footerHeight := m.computeFooterHeight()
	vpHeight := m.termHeight - footerHeight
	if vpHeight < 3 {
		vpHeight = 3
	}
	if m.viewport.Height() != vpHeight {
		m.viewport.SetHeight(vpHeight)
		m.viewportViewValid = false
	}

	// Scrollbar: reserve 2 columns if content exceeds viewport
	scrollbarWidth := 0
	totalLines := m.viewport.TotalLineCount()
	if totalLines > vpHeight {
		scrollbarWidth = 2
	}
	contentWidth := m.termWidth - scrollbarWidth

	// 仅在以下情况重建内容：脏标记、宽度变化或新条目到达
	if m.hasDirtyEntries() || contentWidth != m.prevViewportWidth ||
		len(m.contentParts) != len(m.logEntries) {
		// Resize viewport to make room for scrollbar
		if m.viewport.Width() != contentWidth {
			m.viewport.SetWidth(contentWidth)
		}
		m.rebuildContentCache()
		m.viewportViewValid = false
	}

	// 使用缓存的 viewport 视图
	if !m.viewportViewValid {
		m.cachedViewportView = m.viewport.View()
		m.viewportViewValid = true
		m.prevViewportYOffset = m.viewport.YOffset()
		m.prevViewportHeight = m.viewport.Height()
	}

	// Render viewport with optional scrollbar
	if scrollbarWidth > 0 {
		sbStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
		trackStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("237"))
		scrollbar := common.Scrollbar(sbStyle, trackStyle,
			vpHeight, totalLines, vpHeight, m.viewport.YOffset())

		vpLines := strings.Split(m.cachedViewportView, "\n")
		sbLines := strings.Split(scrollbar, "\n")

		var combinedLines []string
		maxLines := len(vpLines)
		if len(sbLines) > maxLines {
			maxLines = len(sbLines)
		}
		for i := 0; i < maxLines; i++ {
			vpLine := ""
			sbLine := ""
			if i < len(vpLines) {
				vpLine = vpLines[i]
			}
			if i < len(sbLines) {
				sbLine = sbLines[i]
			}
			// Pad vpLine to contentWidth then add scrollbar
			vpPadded := lipgloss.NewStyle().Width(contentWidth).Render(vpLine)
			combinedLines = append(combinedLines, vpPadded+sbLine)
		}
		b.WriteString(strings.Join(combinedLines, "\n"))
	} else {
		b.WriteString(m.cachedViewportView)
	}

	// ── Tool Timeline Panel (between viewport and separator) ──
	if m.taskRunning || len(m.timelineEntries) > 0 {
		timelineContent := m.renderTimelinePanel(m.termWidth)
		if timelineContent != "" {
			b.WriteString("\n")
			b.WriteString(timelineContent)
		}
	}

	// Separator (cached)
	if m.cachedSeparator == "" {
		sepWidth := m.termWidth
		if sepWidth < 40 {
			sepWidth = 40
		}
		m.cachedSeparator = inputSeparatorStyle.Render(strings.Repeat("─", sepWidth))
	}
	b.WriteString(m.cachedSeparator)
	b.WriteString("\n")

	// ── Input area: command mode hidden, edit mode with textarea ──
	var footer strings.Builder
	if !m.commandMode {
		// ── Edit mode: textarea with dark background (via Base style), no bar ──
		m.input.SetWidth(m.computeFieldWidth())
		m.input.SetHeight(m.computeInputHeight())
		inputLine := m.input.View()
		// Wrap textarea in a bordered panel to visually distinguish from message body
		// Use blue border when task has started, gray border when idle
		panelStyle := inputPanelIdleStyle
		if m.taskStarted {
			panelStyle = inputPanelStyle
		}
		footer.WriteString(panelStyle.Render(inputLine))
		footer.WriteString("\n")

		// Inline skill autocomplete suggestions (below textarea)
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
			// Hint line
			footer.WriteString(m.skillHintStyle.Render("Tab 切换  Enter 选择  Esc 关闭"))
			footer.WriteString("\n")
		}
	}

	// Inline keyword autocomplete suggestions (below textarea)
	if m.keywordAutoComplete && len(m.keywordSuggestions) > 0 {
		var suggestionParts []string
		for i, suggestion := range m.keywordSuggestions {
			if i == m.keywordSuggestionIdx {
				// Highlight selected suggestion
				suggestionParts = append(suggestionParts,
					m.keywordHighlightStyle.Render(suggestion))
			} else {
				// Dim non-selected suggestions
				suggestionParts = append(suggestionParts,
					m.keywordSuggestionStyle.Render(suggestion))
			}
		}
		footer.WriteString(strings.Join(suggestionParts, "  "))
		footer.WriteString("\n")
	}

	// Error message
	if m.errMsg != "" {
		footer.WriteString(lipgloss.NewStyle().MarginLeft(2).Render(errorStyle.Render("✖ " + m.errMsg)))
		footer.WriteString("\n")
	}

	// Token consumption display (use cached render to skip expensive computation)
	// The cache is maintained in Update() when token counts change.
	tokenDash := m.cachedTokenDashboard
	if !m.tokenDashboardValid {
		tokenDash = m.renderTokenDashboard()
	}
	footer.WriteString(tokenDash)

	// Status line: nvim airline-style segmented bar
	// The cache is maintained in Update() on each tick.
	statusBar := m.cachedStatusBar
	if !m.statusBarValid {
		statusBar = m.renderAirlineStatusBar()
	}
	footer.WriteString("\n")
	// Add extra spacing before status line: empty line in edit mode
	if !m.commandMode {
		footer.WriteString("\n")
	}
	footer.WriteString(statusBar)

	b.WriteString(footer.String())

	return tea.View{AltScreen: true, Content: b.String()}
}

// renderTimelinePanel renders the tool timeline panel with caching.
func (m *model) renderTimelinePanel(width int) string {
	// 检查是否有正在运行的条目 — 有则跳过缓存以支持动画
	hasRunning := false
	for _, e := range m.timelineEntries {
		if e.Status == ToolStatusRunning {
			hasRunning = true
			break
		}
	}

	// Build cache key: entries count + last entry ID + expanded state + width
	var lastID string
	if len(m.timelineEntries) > 0 {
		lastID = m.timelineEntries[len(m.timelineEntries)-1].ID
	}
	expandedStr := "0"
	if m.timelineExpanded {
		expandedStr = "1"
	}
	key := fmt.Sprintf("%d|%s|%s|%d", len(m.timelineEntries), lastID, expandedStr, width)

	// 只在没有运行条目时使用缓存（动画帧会变化，不能用缓存）
	if !hasRunning && key == m.timelineCacheKey && m.timelineCache != "" {
		return m.timelineCache
	}

	// 渲染 timeline 内容（传 width-4 给内部 padding 空间）
	content := RenderTimeline(m.timelineEntries, m.timelineExpanded, width-4, m.anim)
	if content == "" {
		return ""
	}

	// 去掉 addTimelineTopBorder 添加的顶部边框（面板已有 lipgloss border）
	lines := strings.Split(content, "\n")
	if len(lines) > 1 {
		content = strings.Join(lines[1:], "\n")
	} else {
		return ""
	}

	// 构建提示行
	hint := timelineHintStyle.Render(" ctrl+l " + langManager.GetText("TimelineDetailHint") + " │ ctrl+v " + langManager.GetText("TimelineExpandHint") + " ")

	// 组装面板内容
	panelContent := content + "\n" + hint

	// 应用面板样式
	panelWidth := width - 2
	if panelWidth < 30 {
		panelWidth = 30
	}
	rendered := timelinePanelStyle.Width(panelWidth).Render(panelContent)

	m.timelineCache = rendered
	m.timelineCacheKey = key
	return m.timelineCache
}

// renderAirlineStatusBar renders an nvim airline-style segmented status bar.
// Layout: [Mode][Filler─────]([RightSeg1][RightSeg2]...)
func (m *model) renderAirlineStatusBar() string {
	width := m.termWidth
	if width <= 0 {
		width = 80 // fallback before WindowSizeMsg
	}

	// ── Determine mode segment with gradient text ──
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
	} else if m.taskRunning {
		// Gradient mode indicator for running state
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

	// ── Build left part: mode +  transition to filler ─────────────────
	leftPart := modeSeg + makeRightSep(modeBg, airlineColorInfoBg)

	// ── Build right segments (only in running state) ────────────────────
	type segDef struct {
		bg    color.Color
		style lipgloss.Style
		text  string
	}

	var rightSegs []segDef

	// Show provider/model info in both running and idle states
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
		// Spinner animation — always present when running
		rightSegs = append(rightSegs, segDef{
			bg:    airlineColorInfoBg,
			style: airlineInfoStyle,
			text:  m.anim.Render(),
		})
	}

	// ── Render right part ───────────────────────────────────────────────
	var rightPart string
	if len(rightSegs) > 0 {
		var rParts []string
		// First : transition from filler bg to first right segment bg
		rParts = append(rParts, makeLeftSep(airlineColorInfoBg, rightSegs[0].bg))
		for i, seg := range rightSegs {
			rParts = append(rParts, seg.style.Render(seg.text))
			if i < len(rightSegs)-1 {
				rParts = append(rParts, makeLeftSep(seg.bg, rightSegs[i+1].bg))
			}
		}
		rightPart = strings.Join(rParts, "")
	}

	// ── Edge case: very narrow terminal ────────────────────────────────
	leftRenderedWidth := lipgloss.Width(leftPart)
	if leftRenderedWidth >= width {
		return modeSeg + makeRightSepToEnd(modeBg)
	}

	rightRenderedWidth := lipgloss.Width(rightPart)
	fillerWidth := width - leftRenderedWidth - rightRenderedWidth
	if fillerWidth < 0 {
		// Can't fit right segments — drop them
		rightPart = ""
		rightRenderedWidth = 0
		fillerWidth = width - leftRenderedWidth
		if fillerWidth < 0 {
			fillerWidth = 0
		}
	}

	// ── Render filler segment ───────────────────────────────────────────
	fillerPart := airlineFillerStyle.Width(fillerWidth).Render(tipsText)

	// ── End transition ──────────────────────────────────────────────────
	var endSep string
	if len(rightSegs) == 0 {
		endSep = makeRightSepToEnd(airlineColorInfoBg)
	}

	// ── Assemble full bar ───────────────────────────────────────────────
	// Layout: [Mode][Filler─────](→end | [RightSegs])
	return leftPart + fillerPart + endSep + rightPart
}

// renderWelcomePanel renders the welcome panel.
// In the initial state (no log entries), it centers the logo on screen.
// After tasks have run, it falls back to the original left/right layout.
func (m *model) renderWelcomePanel() string {
	if len(m.logEntries) == 0 {
		// Initial startup state: center logo in the viewport
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
	// After tasks have run: restore the original layout
	return m.renderWelcomePanelLayout()
}

// renderCenteredStartupScreen renders the startup screen with logo centered
// both horizontally and vertically in the available viewport.
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
	// Use gradient for the tagline
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

// renderWelcomePanelLayout renders the original left/right panel layout.
// This is used when there are log entries (tasks have run).
func (m *model) renderWelcomePanelLayout() string {
	// --- 项目路径（将在左+右面板下方占整行显示）---
	cwd := m.projectDir
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(cwd, home) {
		cwd = "~" + strings.TrimPrefix(cwd, home)
	}
	cwdLine := welcomeSubStyle.Render(cwd)

	// Build left panel: logo ONLY (cwd removed)
	var left strings.Builder
	left.WriteString(renderBanner())
	left.WriteString("\n\n")

	leftContent := welcomeLeftStyle.Render(left.String())

	// Build right panel: recent activity
	var right strings.Builder
	tagline := common.ApplyGrad("─── A Repository-Aware, Self-Evolving Agent")
	right.WriteString(tagline)
	right.WriteString("\n")

	// Compute responsive widths
	panelWidth := m.computeFieldWidth() + 4
	innerWidth := panelWidth - 4 // 4 padding
	leftWidth := 38
	if innerWidth < 65 {
		// Narrow terminal: stack vertically
		boxInner := leftContent + "\n\n" + welcomeDimStyle.Render(strings.Repeat("─", 38)) + "\n\n" + right.String()
		// 在下方添加占整行的项目路径
		boxInner += "\n\n" + cwdLine
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
	// 在左+右面板下方添加占整行的项目路径
	inner += "\n\n" + cwdLine
	return welcomePanelStyle.Width(innerWidth).Render(inner)
}
func renderBanner() string {
	asciiLogo := []string{
		"╔═╗┌─┐┌┬┐┌─┐  ╔═╗┌─┐┌┬┐┌─┐┬─┐  ╔═╗╦",
		"║  │ │ ││├┤   ╠═╣│   │ │ │├┬┘  ╠═╣║",
		"╚═╝└─┘─┴┘└─┘  ╩ ╩└─┘ ┴ └─┘┴└─  ╩ ╩╩",
	}

	// Use gradient text (blue → cyan) instead of fixed rainbow colors
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

// formatCacheHitRate 计算并格式化缓存命中率
// 返回格式: "命中率: XX.X%(cache)"，例如 "命中率: 50.0%(0.5k)"
// 当 inputTokens 为 0 时返回空字符串
func formatCacheHitRate(cacheTokens, inputTokens int64) string {
	if inputTokens == 0 {
		return ""
	}
	// 仅在 cacheTokens > 0 时有意义
	if cacheTokens <= 0 {
		return ""
	}
	rate := float64(cacheTokens) / float64(inputTokens) * 100
	cacheStr := formatToken(cacheTokens)
	return fmt.Sprintf("Cache: %.1f%%(%s)", rate, cacheStr)
}

// renderTokenDashboard renders a dashboard-style token consumption display.
// Shows total tokens in a highlighted row, followed by per-agent breakdown sorted by total.
func (m *model) renderTokenDashboard() string {
	// 折叠模式：只显示当前 agent 本次运行的 token 统计
	if m.tokenDashboardCollapsed {
		return m.renderCollapsedTokenDashboard()
	}

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
		hitRate := formatCacheHitRate(m.cacheReadInputTokens, m.inputTokens)
		if hitRate != "" {
			header += inputStyle.Render(hitRate + "  ")
		}
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
			hitRate := formatCacheHitRate(au.CacheReadInputTokens, au.InputTokens)
			if hitRate != "" {
				agentLine += "  " + agentInStyle.Render(hitRate)
			}
		}
		lines = append(lines, agentLine)
	}

	return dashStyle.Render(strings.Join(lines, "\n"))
}

// renderCollapsedTokenDashboard 渲染折叠状态的 token 仪表盘
// 只显示当前 agent 本次运行的 token 消耗和 cache 命中率
func (m *model) renderCollapsedTokenDashboard() string {
	rt := m.currentAgentRunTokens

	// 如果没有任何 token 数据，返回空字符串
	if rt.InputTokens == 0 && rt.OutputTokens == 0 {
		return ""
	}

	// 处理 agent 名称
	agentName := rt.AgentName
	if agentName == "" {
		agentName = "—"
	}

	// 计算总输入 token（用于 cache 命中率计算）
	totalInput := rt.InputTokens + rt.CacheReadInputTokens + rt.CacheCreationInputTokens

	inStr := formatToken(totalInput)
	outStr := formatToken(rt.OutputTokens)

	// 计算 cache 命中率
	cacheStr := ""
	if rt.CacheReadInputTokens > 0 && totalInput > 0 {
		rate := float64(rt.CacheReadInputTokens) / float64(totalInput) * 100
		cacheStr = fmt.Sprintf("Cache: %.1f%%(%s)", rate, formatToken(rt.CacheReadInputTokens))
	}

	// 使用与现有 UI 一致的样式
	dashStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.NormalBorder()).
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

	line += style.Render("(ctrl+t: expand)")

	return dashStyle.Render(line)
}

