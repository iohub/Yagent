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
	if m.dashboardCollapsed {
		return false
	}
	return m.termWidth >= 120 && (m.taskRunning || len(m.timelineEntries) > 0 || m.inputTokens+m.outputTokens > 0)
}

// toggleDashboard 切换右上角 dashboard 收缩/展开（alt+d）
func (m *model) toggleDashboard() {
	m.dashboardCollapsed = !m.dashboardCollapsed
	m.invalidateFooterCache()
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

// formatCacheShort 格式化缓存信息短格式（用于右上角 dashboard 紧凑展示）
// 返回格式:
//   - 只有读缓存: "⊕30%"
//   - 只有写缓存: "W:0.8k"
//   - 两者都有:   "⊕30% W:0.8k"
// 没有任何缓存活动时返回空字符串
func formatCacheShort(cacheRead, cacheCreation, totalInput int64) string {
	if totalInput <= 0 {
		return ""
	}
	var parts []string
	if cacheRead > 0 {
		rate := float64(cacheRead) / float64(totalInput) * 100
		parts = append(parts, fmt.Sprintf("⊕%.0f%%", rate))
	}
	if cacheCreation > 0 {
		parts = append(parts, fmt.Sprintf("W:%s", formatToken(cacheCreation)))
	}
	return strings.Join(parts, " ")
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

	// ── Token section (top): 标题左对齐 + 收缩提示右对齐（右上角） ──
	innerWidth := width - 4 // border(2) + padding(2)
	tokenTitle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Italic(true).
		Render("Tokens")
	collapseHint := timelineHintStyle.Render(" alt+d "+langManager.GetText("DashboardCollapseHint")+" ")
	// 用宽度填充实现右对齐；若空间不足则直接拼接不填充
	hintWidth := lipgloss.Width(collapseHint)
	fillWidth := innerWidth - hintWidth
	if fillWidth < lipgloss.Width(tokenTitle) {
		fillWidth = lipgloss.Width(tokenTitle)
	}
	titleLine := lipgloss.NewStyle().Width(fillWidth).Render(tokenTitle) + collapseHint
	lines = append(lines, titleLine)

	if len(agents) > 0 {
		// Space allocation
		timelineReserve := 0
		if len(m.timelineEntries) > 0 {
			timelineReserve = 1 // 1 entry (hint merged into title line)
		}
		maxTokenRows := height - timelineReserve
		if maxTokenRows < 3 {
			maxTokenRows = 3
		}

		// Total row
		inputStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111"))
		outputStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("114"))
		cacheRateStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("141"))

		totalInput := m.inputTokens + m.cacheReadInputTokens + m.cacheCreationInputTokens
		inStr := formatToken(totalInput)
		outStr := formatToken(m.outputTokens)
		nameStyle := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(AgentColor("Total"))).
			Width(10)
		nameRendered := nameStyle.Render("Total")

		totalLine := nameRendered + " " + inputStyle.Render("In: "+inStr+"  ") +
			outputStyle.Render("Out: "+outStr)
		if cacheInfo := formatCacheShort(m.cacheReadInputTokens, m.cacheCreationInputTokens, totalInput); cacheInfo != "" {
			totalLine += "  " + cacheRateStyle.Render(cacheInfo)
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
			// "+N more" 提示行需要额外 1 行，从 agent 行预算中扣除
			if maxAgentRows > 0 {
				maxAgentRows--
			}
			displayAgents = agents[:maxAgentRows]
			moreCount = len(agents) - maxAgentRows
			// 没有空间显示任何 agent 时，也放弃 more 提示（仅保留 标题+Total+分隔线）
			if maxAgentRows == 0 && moreCount > 0 {
				moreCount = 0
			}
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
			if cacheInfo := formatCacheShort(au.CacheReadInputTokens, au.CacheCreationInputTokens, auTotalInput); cacheInfo != "" {
				line += "  " + cacheRateStyle.Render(cacheInfo)
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
