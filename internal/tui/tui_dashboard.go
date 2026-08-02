package tui

import (
	"fmt"
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

// renderDashboard 渲染右上角 dashboard（timeline + token 紧凑形式）
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
	rt := m.currentAgentRunTokens
	key := fmt.Sprintf("%d|%s|%d|%d|%v|%d|%d|%d|%s|%d|%d|%d|%d",
		len(m.timelineEntries), lastID, width, height,
		m.tokenDashboardCollapsed,
		m.inputTokens+m.outputTokens,
		m.cacheReadInputTokens,
		m.cacheCreationInputTokens,
		rt.AgentName,
		rt.InputTokens,
		rt.OutputTokens,
		rt.CacheReadInputTokens,
		rt.CacheCreationInputTokens)

	if !hasRunning && key == m.dashboardCacheKey && m.dashboardCache != "" {
		return m.dashboardCache
	}

	var lines []string

	// ── Title ──
	title := lipgloss.NewStyle().
		Foreground(lipgloss.Color("241")).
		Italic(true).
		Render("⚙ Activity")
	lines = append(lines, title)

	// Compute token section row count for timeline reservation
	tokenSectionRows := 0
	if rt.InputTokens > 0 || rt.OutputTokens > 0 {
		tokenSectionRows = 5 // separator + title + agent + In + Out
		totalInput := rt.InputTokens + rt.CacheReadInputTokens + rt.CacheCreationInputTokens
		if rt.CacheReadInputTokens > 0 && totalInput > 0 {
			tokenSectionRows = 6
		}
	}

	// ── Timeline section ──
	if len(m.timelineEntries) > 0 {
		content := RenderTimeline(m.timelineEntries, false, width-4, m.anim)
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
			maxTL := height - 1 - tokenSectionRows
			if maxTL < 0 {
				maxTL = 0
			}
			if len(tlInnerLines) > maxTL {
				tlInnerLines = tlInnerLines[:maxTL]
			}
			lines = append(lines, tlInnerLines...)
		}
	}

	// ── Token section ──
	if rt.InputTokens > 0 || rt.OutputTokens > 0 {
		// Separator between timeline and token section
		lines = append(lines, lipgloss.NewStyle().
			Foreground(lipgloss.Color("237")).
			Render(strings.Repeat("─", width-4)))

		// Token section title
		tokenTitle := lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			Italic(true).
			Render("⚡ Tokens")
		lines = append(lines, tokenTitle)

		// Agent name
		agentName := rt.AgentName
		if agentName == "" {
			agentName = "—"
		}
		agentLine := lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(AgentColor(rt.AgentName))).
			Render("[" + agentName + "]")
		lines = append(lines, agentLine)

		// Token fields
		totalInput := rt.InputTokens + rt.CacheReadInputTokens + rt.CacheCreationInputTokens
		inStr := formatToken(totalInput)
		outStr := formatToken(rt.OutputTokens)

		inputStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111"))
		outputStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("114"))
		grayStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("243"))

		lines = append(lines,
			lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("In    ") + inputStyle.Render(inStr),
			lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("Out   ") + outputStyle.Render(outStr),
		)

		// Cache hit rate (optional)
		if rt.CacheReadInputTokens > 0 && totalInput > 0 {
			rate := float64(rt.CacheReadInputTokens) / float64(totalInput) * 100
			cacheStr := fmt.Sprintf("%.1f%% (%s)", rate, formatToken(rt.CacheReadInputTokens))
			lines = append(lines,
				lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("Cache ") + grayStyle.Render(cacheStr),
			)
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
