package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

func (m model) renderHistoryPanel() string {
	panelWidth := m.termWidth - 4
	if panelWidth < 40 {
		panelWidth = 40
	}

	var b strings.Builder

	// ── Header: ◆ title │ filter │ counter ──
	{
		htStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
		hdStyle := lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("244"))

		var parts []string
		parts = append(parts, htStyle.Render("◆ "+langManager.GetText("HistoryTitle")))

		if m.historyFilter != "" {
			cur := lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Render("▌")
			parts = append(parts, hdStyle.Render("│")+" "+lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(m.historyFilter)+cur)
		} else {
			parts = append(parts, hdStyle.Render("│ "+langManager.GetText("HistoryFilterPlaceholder")))
		}
		parts = append(parts, hdStyle.Render(fmt.Sprintf("%d/%d", m.historyIndex+1, len(m.filteredItems))))

		hbStyle := lipgloss.NewStyle().
			Border(lipgloss.NormalBorder(), false, false, true, false).
			BorderForeground(lipgloss.Color("237")).
			Width(panelWidth).
			Padding(0, 1)

		b.WriteString(hbStyle.Render(strings.Join(parts, "  ")))
		b.WriteString("\n")
	}

	// ── Body: single-line items ──
	bodyHeight := m.termHeight - 8 // header(~2) + footer(~6)
	if bodyHeight < 4 {
		bodyHeight = 4
	}

	if len(m.filteredItems) == 0 {
		empty := lipgloss.NewStyle().
			Foreground(lipgloss.Color("244")).
			Width(panelWidth).
			Padding(2, 2).
			Render("  " + langManager.GetText("HistoryEmpty"))
		b.WriteString(empty)
	} else {
		// Edge-triggered scroll: update scrollStart only when selection leaves visible area
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
			indicator := lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("244")).
				Width(panelWidth).Padding(0, 2).
				Render(fmt.Sprintf("▲ %s", fmt.Sprintf(langManager.GetText("HistoryMoreAbove"), scrollStart)))
			b.WriteString(indicator)
			b.WriteString("\n")
		}

		// Column layout: date(11) + title + count(Nm)
		const dateWidth = 11
		const countArea = 6
		const selMarker = 2
		const spacing = 2
		titleMaxWidth := panelWidth - dateWidth - countArea - selMarker - spacing - 2
		if titleMaxWidth < 15 {
			titleMaxWidth = 15
		}

		dateStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Faint(true)
		titleStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
		countStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Faint(true)
		selStyle := lipgloss.NewStyle().
			Background(lipgloss.Color("39")).
			Foreground(lipgloss.Color("15")).
			Width(panelWidth).
			Padding(0, 1)

		normalStyle := lipgloss.NewStyle().
			Width(panelWidth).
			Padding(0, 1)

		for i := scrollStart; i < end; i++ {
			item := m.filteredItems[i]
			selected := i == m.historyIndex

			// Title is pre-truncated to 30 chars; further truncate for narrow terminals
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
				b.WriteString(selStyle.Render(line))
			} else {
				line := fmt.Sprintf("  %s  %s  %s",
					dateStyle.Render(dateStr),
					titleStyle.Render(titlePadded),
					countStyle.Render(countStr))
				b.WriteString(normalStyle.Render(line))
			}
			b.WriteString("\n")
		}

		// "more below" indicator
		if end < len(m.filteredItems) {
			remaining := len(m.filteredItems) - end
			indicator := lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("244")).
				Width(panelWidth).Padding(0, 2).
				Render(fmt.Sprintf("▼ %s", fmt.Sprintf(langManager.GetText("HistoryMoreBelow"), remaining)))
			b.WriteString(indicator)
			b.WriteString("\n")
		}
	}

	// ── Footer: key hints ──
	var hintText string
	if m.historyConfirmDelete {
		hintText = lipgloss.NewStyle().Foreground(lipgloss.Color("167")).Bold(true).Render(langManager.GetText("HistoryConfirmDelete"))
	} else {
		hints := []string{
			lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true).Render(langManager.GetText("HistoryKeyContinue")),
			lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("245")).Render(langManager.GetText("HistoryKeyDelete")),
			lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("245")).Render(langManager.GetText("HistoryKeyBack")),
			lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("245")).Render(langManager.GetText("HistoryKeyClearFilter")),
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
