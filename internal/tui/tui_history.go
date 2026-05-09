package tui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

func (m model) renderHistoryPanel() string {
	panelWidth := m.termWidth - 4
	if panelWidth < 40 {
		panelWidth = 40
	}

	var b strings.Builder

	// ── Header: ◆ title │ filter │ counter │ page info ──
	{
		var parts []string
		parts = append(parts, historyHeaderTitle.Render("◆ "+langManager.GetText("HistoryTitle")))

		if m.historyFilter != "" {
			parts = append(parts, historyHeaderDim.Render("│")+" "+historyFilterText.Render(m.historyFilter)+historyFilterCursor)
		} else {
			parts = append(parts, historyHeaderDim.Render("│ "+langManager.GetText("HistoryFilterPlaceholder")))
		}

		// Counter with page info
		totalVisible := len(m.filteredItems)
		pageInfo := ""
		bodyHeight := m.termHeight - 9
		if bodyHeight < 4 {
			bodyHeight = 4
		}
		if totalVisible > bodyHeight {
			currentPage := m.historyScrollStart/bodyHeight + 1
			totalPages := (totalVisible + bodyHeight - 1) / bodyHeight
			pageInfo = fmt.Sprintf(" Pg %d/%d", currentPage, totalPages)
		}
		parts = append(parts, historyHeaderDim.Render(fmt.Sprintf("%d/%d%s", m.historyIndex+1, totalVisible, pageInfo)))

		hbStyle := lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, true, false).
			BorderForeground(lipgloss.Color("237")).
			Width(panelWidth).
			Padding(0, 1)

		b.WriteString(hbStyle.Render(strings.Join(parts, "  ")))
		b.WriteString("\n")
	}

	// ── Loading state ──
	if m.historyLoading {
		loading := lipgloss.NewStyle().
			Foreground(lipgloss.Color("244")).
			Width(panelWidth).
			Padding(2, 2).
			Render("  ⏳ Loading...")
		b.WriteString(loading)
		return b.String()
	}

	// ── Body: single-line items with edge-triggered scroll ──
	bodyHeight := m.termHeight - 9
	if bodyHeight < 4 {
		bodyHeight = 4
	}

	if len(m.filteredItems) == 0 {
		empty := historyEmptyStyle.
			Width(panelWidth).
			Padding(2, 2).
			Render("  " + langManager.GetText("HistoryEmpty"))
		b.WriteString(empty)
	} else {
		// Edge-triggered scroll
		topMargin := 2
		btmMargin := 2
		if bodyHeight < topMargin+btmMargin+1 {
			topMargin = 1
			btmMargin = 1
		}
		scrollStart := m.historyScrollStart
		if m.historyIndex < scrollStart+topMargin {
			scrollStart = m.historyIndex - topMargin
		} else if m.historyIndex >= scrollStart+bodyHeight-btmMargin {
			scrollStart = m.historyIndex - bodyHeight + btmMargin + 1
		}
		if scrollStart < 0 {
			scrollStart = 0
		}
		maxStart := len(m.filteredItems) - bodyHeight
		if maxStart < 0 {
			maxStart = 0
		}
		if scrollStart > maxStart {
			scrollStart = maxStart
		}
		m.historyScrollStart = scrollStart

		end := scrollStart + bodyHeight
		if end > len(m.filteredItems) {
			end = len(m.filteredItems)
		}

		// "more above" indicator
		if scrollStart > 0 {
			indicator := historyMoreStyle.
				Width(panelWidth).Padding(0, 2).
				Render(fmt.Sprintf("▲ %s", fmt.Sprintf(langManager.GetText("HistoryMoreAbove"), scrollStart)))
			b.WriteString(indicator)
			b.WriteString("\n")
		}

		// Column layout: selMarker(2) + date(11) + spacing(2) + title + count(6)
		const dateWidth = 11
		const countArea = 6
		const selMarker = 2
		const spacing = 2
		titleMaxWidth := panelWidth - dateWidth - countArea - selMarker - spacing - 2
		if titleMaxWidth < 15 {
			titleMaxWidth = 15
		}

		for i := scrollStart; i < end; i++ {
			item := m.filteredItems[i]
			selected := i == m.historyIndex

			displayTitle := item.Title
			titleRunes := []rune(displayTitle)
			if len(titleRunes) > titleMaxWidth {
				displayTitle = string(titleRunes[:titleMaxWidth-1]) + "…"
			}
			titlePadded := lipgloss.NewStyle().Width(titleMaxWidth).Render(displayTitle)

			dateStr := item.CreatedAt.Format("01-02 15:04")
			countStr := fmt.Sprintf("%dm", item.MessageCount)

			if selected {
				line := fmt.Sprintf("▐ %s  %s  %s", dateStr, titlePadded, countStr)
				b.WriteString(historySelStyle.Width(panelWidth).Padding(0, 1).Render(line))
			} else {
				line := fmt.Sprintf("  %s  %s  %s",
					historyDateStyle.Render(dateStr),
					historyTitleStyle.Render(titlePadded),
					historyCountStyle.Render(countStr))
				b.WriteString(line)
			}
			b.WriteString("\n")
		}

		// "more below" indicator
		if end < len(m.filteredItems) {
			remaining := len(m.filteredItems) - end
			indicator := historyMoreStyle.
				Width(panelWidth).Padding(0, 2).
				Render(fmt.Sprintf("▼ %s", fmt.Sprintf(langManager.GetText("HistoryMoreBelow"), remaining)))
			b.WriteString(indicator)
			b.WriteString("\n")
		}
	}

	// ── Footer: key hints ──
	var hintText string
	if m.historyConfirmDelete {
		hintText = historyDeleteStyle.Render(langManager.GetText("HistoryConfirmDelete"))
	} else {
		hints := []string{
			lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true).Render("↑↓:选择"),
			historyFooterStyle.Render("PgUp/PgDn:翻页"),
			lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true).Render("enter:继续"),
			historyFooterStyle.Render("Home/End:首尾"),
			historyFooterStyle.Render("esc:返回"),
		}
		hintText = strings.Join(hints, "  ")
	}

	footerStyle := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(lipgloss.Color("237")).
		Width(panelWidth).
		Padding(0, 1)

	b.WriteString(footerStyle.Render(hintText))

	return b.String()
}
