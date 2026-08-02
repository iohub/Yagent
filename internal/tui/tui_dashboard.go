package tui

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
)

const dashboardPanelWidth = 46

// dashboardVisible 判断宽屏模式下是否显示右上角 dashboard
func (m *model) dashboardVisible() bool {
	return m.termWidth >= 120 && (m.taskRunning || len(m.timelineEntries) > 0 || m.inputTokens+m.outputTokens > 0)
}

// dashboardWidth 返回 dashboard 占用宽度（不可见时返回 0）
func (m *model) dashboardWidth() int {
	if !m.dashboardVisible() {
		return 0
	}
	// 保证 viewport 至少 40 列宽
	if m.termWidth-dashboardPanelWidth < 40 {
		return 0
	}
	return dashboardPanelWidth
}

// renderDashboard 渲染右上角 dashboard（token 优先，timeline 次之）
// width / height 为面板可用尺寸，返回恰好 height 行的字符串。
func (m *model) renderDashboard(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}

	// 检查是否有正在运行的条目 — 有则跳过缓存以支持动画
	hasRunning := false
	for _, e := range m.timelineEntries {
		if e.Status == ToolStatusRunning {
			hasRunning = true
			break
		}
	}

	// Build cache key
	var lastID string
	if len(m.timelineEntries) > 0 {
		lastID = m.timelineEntries[len(m.timelineEntries)-1].ID
	}

	// Collect and sort agents for token section (also drives the cache signature)
	agents := make([]*AgentTokenUsage, 0, len(m.tokenUsagePerAgent))
	for _, au := range m.tokenUsagePerAgent {
		if au.InputTokens+au.OutputTokens > 0 {
			agents = append(agents, au)
		}
	}
	sort.Slice(agents, func(i, j int) bool {
		return (agents[i].InputTokens + agents[i].OutputTokens) > (agents[j].InputTokens + agents[j].OutputTokens)
	})

	// Build token signature for cache key
	var tokenSig strings.Builder
	for _, au := range agents {
		fmt.Fprintf(&tokenSig, "%s:%d:%d:%d:%d;", au.AgentName, au.InputTokens, au.OutputTokens, au.CacheReadInputTokens, au.CacheCreationInputTokens)
	}

	key := fmt.Sprintf("%d|%s|%d|%d|%v|%d|%d|%d|%s",
		len(m.timelineEntries), lastID, width, height,
		m.tokenDashboardCollapsed,
		m.inputTokens+m.outputTokens,
		m.cacheReadInputTokens,
		m.cacheCreationInputTokens,
		tokenSig.String())

	if !hasRunning && key == m.dashboardCacheKey && m.dashboardCache != "" {
		return m.dashboardCache
	}

	var lines []string

	// ── Token section (top) ──
	tokenTitle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Italic(true).
		Render("⚡ Tokens")
	lines = append(lines, tokenTitle)

	if len(agents) > 0 {
		// Space allocation
		timelineReserve := 0
		if len(m.timelineEntries) > 0 {
			timelineReserve = 2 // 1 entry + hint
		}
		maxTokenRows := height - timelineReserve
		if maxTokenRows < 3 {
			maxTokenRows = 3
		}

		// Total row
		totalLabelStyle := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("240")).
			Width(10).
			Align(lipgloss.Right)
		inputStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111"))
		outputStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("114"))
		sumStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("243"))
		cacheRateStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("141"))

		totalInput := m.inputTokens + m.cacheReadInputTokens + m.cacheCreationInputTokens
		inStr := formatToken(totalInput)
		outStr := formatToken(m.outputTokens)
		sumStr := formatToken(totalInput + m.outputTokens)

		totalLine := totalLabelStyle.Render("Total") + " " +
			inputStyle.Render("In: "+inStr+"  ") +
			outputStyle.Render("Out: "+outStr+"  ") +
			sumStyle.Render("Σ "+sumStr)
		if m.cacheReadInputTokens > 0 && totalInput > 0 {
			rate := float64(m.cacheReadInputTokens) / float64(totalInput) * 100
			totalLine += "  " + cacheRateStyle.Render(fmt.Sprintf("⊕%.0f%%", rate))
		}
		lines = append(lines, totalLine)

		// Separator
		lines = append(lines, lipgloss.NewStyle().
			Foreground(lipgloss.Color("237")).
			Render(strings.Repeat("─", width-4)))

		// Determine display agents (truncate if needed)
		tokenRows := 3 + len(agents) // title + Total + separator + agent rows
		var displayAgents []*AgentTokenUsage
		moreCount := 0
		if tokenRows > maxTokenRows {
			maxAgentRows := maxTokenRows - 3
			if maxAgentRows < 0 {
				maxAgentRows = 0
			}
			displayAgents = agents[:maxAgentRows]
			moreCount = len(agents) - maxAgentRows
			tokenRows = maxTokenRows
		} else {
			displayAgents = agents
		}

		// Agent rows
		for _, au := range displayAgents {
			name := au.AgentName
			if name == "" {
				name = "—"
			}
			if len(name) > 10 {
				name = name[:9] + "…"
			}
			nameStyle := lipgloss.NewStyle().
				Bold(true).
				Foreground(lipgloss.Color(AgentColor(au.AgentName))).
				Width(10)
			nameRendered := nameStyle.Render(name)

			auTotalInput := au.InputTokens + au.CacheReadInputTokens + au.CacheCreationInputTokens
			auInStr := formatToken(auTotalInput)
			auOutStr := formatToken(au.OutputTokens)

			line := nameRendered + " " +
				inputStyle.Render("In: "+auInStr+"  ") +
				outputStyle.Render("Out: "+auOutStr)
			if au.CacheReadInputTokens > 0 && auTotalInput > 0 {
				rate := float64(au.CacheReadInputTokens) / float64(auTotalInput) * 100
				line += "  " + cacheRateStyle.Render(fmt.Sprintf("⊕%.0f%%", rate))
			}
			lines = append(lines, line)
		}

		if moreCount > 0 {
			lines = append(lines, lipgloss.NewStyle().
				Foreground(lipgloss.Color("241")).
				Italic(true).
				Render(fmt.Sprintf("+%d more", moreCount)))
		}
	}

	// ── Timeline section (bottom) ──
	tlAvailable := height - len(lines)
	if len(m.timelineEntries) > 0 && tlAvailable > 2 {
		// Separator + title (2 rows)
		lines = append(lines, lipgloss.NewStyle().
			Foreground(lipgloss.Color("237")).
			Render(strings.Repeat("─", width-4)))
		lines = append(lines, lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Italic(true).
			Render("⚙ Activity"))

		// Timeline content
		content := RenderTimeline(m.timelineEntries, false, false, width-4, m.anim)
		if content != "" {
			tlLines := strings.Split(content, "\n")
			if len(tlLines) > 1 {
				content = strings.Join(tlLines[1:], "\n")
			} else {
				content = ""
			}
		}
		if content != "" {
			tlInnerLines := strings.Split(content, "\n")
			// Reserve 1 row for hint (separator+title+hint = 3 rows consumed from tlAvailable)
			maxTL := tlAvailable - 3
			if maxTL < 0 {
				maxTL = 0
			}
			if len(tlInnerLines) > maxTL {
				tlInnerLines = tlInnerLines[:maxTL]
			}
			hint := timelineHintStyle.Render(" ctrl+l " + langManager.GetText("TimelineDetailHint") + " ")
			lines = append(lines, tlInnerLines...)
			lines = append(lines, hint)
		}
	}

	// Apply border style
	borderStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("62")).
		Padding(0, 1).
		Width(width)

	content := strings.Join(lines, "\n")
	rendered := borderStyle.Render(content)

	// Ensure exactly height lines
	renderedLines := strings.Split(rendered, "\n")
	if len(renderedLines) > height {
		renderedLines = renderedLines[:height]
	}
	for len(renderedLines) < height {
		renderedLines = append(renderedLines, strings.Repeat(" ", width))
	}

	result := strings.Join(renderedLines, "\n")

	m.dashboardCache = result
	m.dashboardCacheKey = key
	return result
}
