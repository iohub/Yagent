package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"yagent/internal/datamanager"
	"yagent/internal/memory"
	"yagent/internal/tui/common"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	startupBrandRows = 5

	startupCardGap = 2

	startupCardsMaxWidth = 104

	startupBrandGap = 2

	startupBrandGapMin = 1

	startupCardsBottomMargin = 1

	startupWideWidth = 88

	startupNarrowWidth = 76

	startupLegacyBelow = 10

	startupRecentsLimit = 4
)

const startupTagline = "─── A Repository-Aware, Self-Evolving Agent ───"

type startupTexts struct {
	StartTitle  string
	RecentTitle string
	SendDesc    string
	CommandDesc string
	HelpDesc    string
	ResumeDesc  string
	StartHint   string

	RecentHintResume string
	RecentHintHist   string
	EmptyTitle       string
	EmptyHint        string
	TurnsFormat      string
	Untitled         string
	Now              string
	Yesterday        string
}

func (t startupTexts) recentHint(src startupRecentsSource) string {
	switch src {
	case startupRecentsFromRollout:
		return t.RecentHintResume
	case startupRecentsFromTasks:
		return t.RecentHintHist
	default:
		return ""
	}
}

func startupTextsFor(lang Language) startupTexts {
	if lang == LangChinese {
		return startupTexts{
			StartTitle:       "⚡ 开始",
			RecentTitle:      "◷ 最近会话",
			SendDesc:         "发送消息",
			CommandDesc:      "命令模式",
			HelpDesc:         "帮助",
			ResumeDesc:       "恢复会话",
			StartHint:        "直接输入任务开始对话 ↓",
			RecentHintResume: "/resume 选择并恢复会话",
			RecentHintHist:   ":hist 浏览并载入历史会话",
			EmptyTitle:       "还没有历史会话",
			EmptyHint:        "输入你的第一个任务，开始对话 ↓",
			TurnsFormat:      "%d轮",
			Untitled:         "(无标题)",
			Now:              "刚刚",
			Yesterday:        "昨天",
		}
	}
	return startupTexts{
		StartTitle:       "⚡ Start",
		RecentTitle:      "◷ Recent sessions",
		SendDesc:         "Send message",
		CommandDesc:      "Command mode",
		HelpDesc:         "Help",
		ResumeDesc:       "Resume session",
		StartHint:        "Type a task to start ↓",
		RecentHintResume: "/resume to pick a session",
		RecentHintHist:   ":hist to browse past sessions",
		EmptyTitle:       "No past sessions yet",
		EmptyHint:        "Type your first task to start ↓",
		TurnsFormat:      "%d turns",
		Untitled:         "(untitled)",
		Now:              "just now",
		Yesterday:        "yesterday",
	}
}

func startupRelTime(t time.Time) string {
	return startupRelTimeAt(t, time.Now(), LangChinese)
}

func startupRelTimeLang(t time.Time, lang Language) string {
	if lang == LangChinese {
		return startupRelTime(t)
	}
	return startupRelTimeAt(t, time.Now(), lang)
}

func startupRelTimeAt(t, now time.Time, lang Language) string {
	txt := startupTextsFor(lang)
	if t.IsZero() {
		return "—"
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return txt.Now
	}

	if days := startupCalendarDays(t, now); days > 0 {
		switch {
		case days == 1:
			return txt.Yesterday
		case days < 14:
			return fmt.Sprintf("%dd", days)
		default:
			return fmt.Sprintf("%dw", days/7)
		}
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

func startupCalendarDays(t, now time.Time) int {
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.Date()
	a := time.Date(y1, m1, d1, 0, 0, 0, 0, now.Location())
	b := time.Date(y2, m2, d2, 0, 0, 0, 0, now.Location())
	return int((b.Sub(a) + 12*time.Hour) / (24 * time.Hour))
}

type startupCardPlan struct {
	height int
	items  int
	blank  bool
	hint   bool
}

func fitStartupCard(items, budget int) startupCardPlan {
	if items < 1 || budget < 3 {
		return startupCardPlan{}
	}
	if budget >= items+4 {
		return startupCardPlan{height: items + 4, items: items, blank: true, hint: true}
	}
	if budget >= items+3 {
		return startupCardPlan{height: items + 3, items: items, hint: true}
	}
	if budget >= items+2 {
		return startupCardPlan{height: items + 2, items: items}
	}
	for n := items - 1; n >= 1; n-- {
		if budget >= n+2 {
			return startupCardPlan{height: n + 2, items: n}
		}
	}
	return startupCardPlan{}
}

func renderTitledCard(title, hint string, body []string, width, height int, c common.ColorTokens) string {
	if width < 4 || height < 3 {
		return ""
	}
	border := lipgloss.NewStyle().Foreground(c.Border)
	accent := lipgloss.NewStyle().Foreground(c.Accent)
	muted := lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true)
	inner := width - 4

	rows := make([]string, 0, height)
	if fill := width - 5 - lipgloss.Width(title); title == "" || fill < 0 {
		rows = append(rows, border.Render("╭"+strings.Repeat("─", width-2)+"╮"))
		if title != "" {
			body = append([]string{accent.Render(truncateCells(title, inner))}, body...)
		}
	} else {
		rows = append(rows,
			border.Render("╭─ ")+accent.Render(title)+border.Render(" "+strings.Repeat("─", fill)+"╮"))
	}

	hintRows := 0
	if hint != "" {
		hintRows = 1
	}
	bodyRows := height - 2 - hintRows
	for i := 0; i < bodyRows; i++ {
		content := ""
		if i < len(body) {
			content = body[i]
		}
		rows = append(rows, cardRow(border, content, inner))
	}
	if hintRows == 1 {
		rows = append(rows, cardRow(border, muted.Render(truncateCells(hint, inner)), inner))
	}
	rows = append(rows, border.Render("╰"+strings.Repeat("─", width-2)+"╯"))
	return strings.Join(rows, "\n")
}

func cardRow(border lipgloss.Style, content string, inner int) string {
	if lipgloss.Width(content) > inner {
		content = truncateCells(stripANSIForBorder(content), inner)
	}
	return border.Render("│") + " " + padRight(content, inner) + " " + border.Render("│")
}

func clampStartupBlock(block string, width, height int) string {
	if width < 1 || height < 1 {
		return ""
	}
	lines := strings.Split(block, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, line := range lines {
		if lipgloss.Width(line) > width {
			lines[i] = truncateCells(stripANSIForBorder(line), width)
		}
	}
	return strings.Join(lines, "\n")
}

func (m *model) startupKeyOverrides() map[string]string {
	if m.com == nil || m.com.Config == nil {
		return nil
	}
	return buildHelpKeyOverrides(&m.com.Config.TUI.Keybindings)
}

func startupKeyLabel(overrides map[string]string, def string) string {
	if v, ok := overrides[def]; ok {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return def
}

type startupKeyEntry struct{ key, desc string }

func (m *model) startupKeyEntries(t startupTexts) []startupKeyEntry {
	o := m.startupKeyOverrides()
	return []startupKeyEntry{
		{startupKeyLabel(o, "Alt+S"), t.SendDesc},
		{startupKeyLabel(o, "Ctrl+E"), t.CommandDesc},
		{startupKeyLabel(o, "?"), t.HelpDesc},
		{"/resume", t.ResumeDesc},
	}
}

func (m *model) startupStartCardBody(plan startupCardPlan, t startupTexts, c common.ColorTokens, inner int) []string {
	entries := m.startupKeyEntries(t)
	n := min(plan.items, len(entries))
	if n <= 0 {
		return nil
	}
	keyStyle := common.KeyHintStyle(c)
	nameStyle := lipgloss.NewStyle().Foreground(c.TextPrimary)
	caps := make([]string, n)
	capW := 0
	for i := 0; i < n; i++ {
		caps[i] = keyStyle.Render(entries[i].key)
		capW = max(capW, lipgloss.Width(caps[i]))
	}
	rows := make([]string, 0, n+1)
	for i := 0; i < n; i++ {
		descW := inner - capW - 1
		if descW < 4 {
			rows = append(rows, caps[i]+" "+nameStyle.Render(truncateCells(entries[i].desc, max(inner-capW-1, 1))))
			continue
		}
		rows = append(rows, padRight(caps[i], capW)+" "+nameStyle.Render(truncateCells(entries[i].desc, descW)))
	}
	if plan.blank {
		rows = append(rows, "")
	}
	return rows
}

func (m *model) startupRecentsCardBody(plan startupCardPlan, t startupTexts, c common.ColorTokens, inner int) []string {
	mutedStyle := lipgloss.NewStyle().Foreground(c.TextMuted)
	if len(m.startupRecents) == 0 {
		return []string{
			mutedStyle.Render(truncateCells(t.EmptyTitle, inner)),
			mutedStyle.Render(truncateCells(t.EmptyHint, inner)),
		}
	}
	numStyle := lipgloss.NewStyle().Foreground(c.Accent)
	titleStyle := lipgloss.NewStyle().Foreground(c.TextPrimary)
	n := min(plan.items, len(m.startupRecents))
	rows := make([]string, 0, n+1)
	for i := 0; i < n; i++ {
		item := m.startupRecents[i]

		label := strings.TrimSpace(item.Label)
		if label == "" {
			label = t.Untitled
		}

		meta := startupRelTimeLang(item.When, m.currentLang)
		if item.Turns > 0 {
			meta += " · " + fmt.Sprintf(t.TurnsFormat, item.Turns)
		}
		titleW := inner - 3 - lipgloss.Width(meta)
		if titleW < 4 && item.Turns > 0 {
			meta = startupRelTimeLang(item.When, m.currentLang)
			titleW = inner - 3 - lipgloss.Width(meta)
		}
		if titleW < 4 {
			meta = ""
			titleW = inner - 2
		}
		titleW = max(titleW, 1)
		row := numStyle.Render(fmt.Sprintf("%d", i+1)) + " " +
			titleStyle.Render(padRight(truncateCells(label, titleW), titleW))
		if meta != "" {
			row += " " + mutedStyle.Render(padLeft(meta, lipgloss.Width(meta)))
		}
		rows = append(rows, row)
	}
	if plan.blank {
		rows = append(rows, "")
	}
	return rows
}

func startupDisplayPath(dir string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return dir
	}
	if strings.HasPrefix(dir, home) {
		return "~" + strings.TrimPrefix(dir, home)
	}
	return dir
}

func (m *model) startupBrandLines(w int) []string {
	w = max(w, 1)
	var rows []string
	if banner := strings.Split(renderBanner(), "\n"); lipgloss.Width(banner[0]) <= w {
		rows = append(rows, banner...)
	} else {
		for _, line := range bannerASCIILines() {
			rows = append(rows, truncateCells(line, w))
		}
	}
	if lipgloss.Width(startupTagline) <= w {
		rows = append(rows, common.ApplyGrad(startupTagline))
	}
	rows = append(rows, welcomeSubStyle.Render(truncateCells(startupDisplayPath(m.projectDir), w)))
	for i := range rows {
		rows[i] = padCenter(rows[i], w)
	}
	return rows
}

func (m *model) renderCenteredStartupScreen(width, height int) string {
	t := startupTextsFor(m.currentLang)
	c := common.DarkModeColors()
	w := m.computeFieldWidth()

	cardsW := min(w, startupCardsMaxWidth)

	place := func(rows []string) string {
		block := strings.Join(rows, "\n")
		return clampStartupBlock(
			lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, block),
			width, height,
		)
	}

	if height < startupLegacyBelow {
		return place(m.startupBrandLines(w))
	}

	renderCards := func(budget int) []string {
		switch {
		case w >= startupWideWidth && height >= 18:
			return m.renderStartupCardsRow(t, c, cardsW, budget, 4, 4)
		case w >= startupNarrowWidth:
			return m.renderStartupCardsRow(t, c, cardsW, budget, 3, 4)
		default:
			return m.renderStartupStack(t, c, cardsW, budget, 3, 2)
		}
	}

	cards := renderCards(height - startupBrandRows - startupBrandGapMin)
	gap := startupBrandGapMin
	if len(cards) > 0 &&
		startupBrandRows+startupBrandGap+len(cards)+startupCardsBottomMargin <= height {
		gap = startupBrandGap

		cards = renderCards(height - startupBrandRows - startupBrandGap)
	}

	rows := m.startupBrandLines(w)
	for i := 0; i < gap; i++ {
		rows = append(rows, "")
	}
	return place(append(rows, centerStartupCards(cards, w, cardsW)...))
}

func centerStartupCards(rows []string, fieldW, cardsW int) []string {
	if len(rows) == 0 {
		return nil
	}
	cardsW = min(cardsW, fieldW)
	padLeft := max((fieldW-cardsW)/2, 0)
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if lipgloss.Width(row) > cardsW {
			row = truncateCells(stripANSIForBorder(row), cardsW)
		}
		rowW := lipgloss.Width(row)
		out = append(out, strings.Repeat(" ", padLeft)+row+
			strings.Repeat(" ", max(fieldW-padLeft-rowW, 0)))
	}
	return out
}

func cardHint(plan startupCardPlan, hint string) string {
	if !plan.hint {
		return ""
	}
	return hint
}

func (m *model) startupRecentsHint(t startupTexts) string {
	if len(m.startupRecents) == 0 {
		return ""
	}
	return t.recentHint(m.startupRecentsSource)
}

func (m *model) renderStartupSingleCard(t startupTexts, c common.ColorTokens, w, budget, items int) []string {
	plan := fitStartupCard(items, budget)
	if plan.height == 0 {
		return nil
	}
	card := renderTitledCard(t.StartTitle, cardHint(plan, t.StartHint), m.startupStartCardBody(plan, t, c, w-4), w, plan.height, c)
	return strings.Split(card, "\n")
}

func (m *model) renderStartupCardsRow(t startupTexts, c common.ColorTokens, w, budget, leftItems, rightItems int) []string {
	leftPlan := fitStartupCard(leftItems, budget)
	if leftPlan.height == 0 {
		return nil
	}
	rightPlan := fitStartupCard(rightItems, budget)
	if rightPlan.height == 0 {
		return m.renderStartupSingleCard(t, c, w, budget, leftItems)
	}
	leftW := (w - startupCardGap) / 2
	rightW := w - startupCardGap - leftW
	h := max(leftPlan.height, rightPlan.height)
	left := renderTitledCard(t.StartTitle, cardHint(leftPlan, t.StartHint), m.startupStartCardBody(leftPlan, t, c, leftW-4), leftW, h, c)
	right := renderTitledCard(t.RecentTitle, cardHint(rightPlan, m.startupRecentsHint(t)), m.startupRecentsCardBody(rightPlan, t, c, rightW-4), rightW, h, c)
	gap := strings.Repeat(" ", startupCardGap)
	return strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, left, gap, right), "\n")
}

const startupStackGap = 1

func (m *model) renderStartupStack(t startupTexts, c common.ColorTokens, w, budget, leftItems, rightItems int) []string {
	minLeft := leftItems + 2
	if rightItems < 1 {
		rightItems = 1
	}
	minRight := 1 + 2

	if budget < minLeft+startupStackGap+minRight {
		plan := fitStartupCard(leftItems, budget)
		if plan.height == 0 {
			return nil
		}
		card := renderTitledCard(t.StartTitle, cardHint(plan, t.StartHint), m.startupStartCardBody(plan, t, c, w-4), w, plan.height, c)
		return strings.Split(card, "\n")
	}

	extra := budget - (minLeft + startupStackGap + minRight)

	leftExtra, rightExtra := 0, 0
	if extra > 0 {
		leftExtra, extra = 1, extra-1
	}
	if extra > 0 && rightItems > 1 {
		rightExtra, extra = 1, extra-1
	}
	if extra > 0 {
		leftExtra, extra = leftExtra+1, extra-1
	}
	rightExtra += extra

	leftPlan := fitStartupCard(leftItems, minLeft+leftExtra)
	rightPlan := fitStartupCard(rightItems, minRight+rightExtra)
	if leftPlan.height == 0 {
		return nil
	}

	left := renderTitledCard(t.StartTitle, cardHint(leftPlan, t.StartHint), m.startupStartCardBody(leftPlan, t, c, w-4), w, leftPlan.height, c)
	rows := strings.Split(left, "\n")
	if rightPlan.height == 0 {
		return rows
	}
	right := renderTitledCard(t.RecentTitle, cardHint(rightPlan, m.startupRecentsHint(t)), m.startupRecentsCardBody(rightPlan, t, c, w-4), w, rightPlan.height, c)
	for i := 0; i < startupStackGap; i++ {
		rows = append(rows, "")
	}
	rows = append(rows, strings.Split(right, "\n")...)
	return rows
}

type startupRecentItem struct {
	Label string
	When  time.Time
	Turns int
}

type startupRecentsSource int

const (
	startupRecentsFromRollout startupRecentsSource = iota
	startupRecentsFromTasks
	startupRecentsFromNone
)

type startupRecentsMsg struct {
	items  []startupRecentItem
	source startupRecentsSource
}

func startupRolloutLabel(s memory.RolloutSessionInfo) string {
	for _, cand := range []string{s.Preview, s.AgentName, s.SessionID} {
		if v := strings.TrimSpace(cand); v != "" {
			return v
		}
	}
	return ""
}

func startupRecentItemsFromRollout(sessions []memory.RolloutSessionInfo, limit int) []startupRecentItem {
	items := make([]startupRecentItem, 0, max(limit, 0))
	for _, s := range sessions {
		if limit > 0 && len(items) >= limit {
			break
		}
		if s.Empty {
			continue
		}
		items = append(items, startupRecentItem{
			Label: startupRolloutLabel(s),
			When:  s.ModTime,
			Turns: s.HeadMessages,
		})
	}
	return items
}

func startupRecentItemsFromTasks(tasks []datamanager.TaskHistoryItem, limit int) []startupRecentItem {
	items := make([]startupRecentItem, 0, max(limit, 0))
	for _, task := range tasks {
		if limit > 0 && len(items) >= limit {
			break
		}
		when := task.UpdatedAt
		if when.IsZero() {
			when = task.CreatedAt
		}
		items = append(items, startupRecentItem{
			Label: task.Title,
			When:  when,
			Turns: task.MessageCount,
		})
	}
	return items
}

func startupRecentsMsgFromRollout(sessions []memory.RolloutSessionInfo, err error, limit int) startupRecentsMsg {
	if err != nil {
		return startupRecentsMsg{source: startupRecentsFromNone}
	}
	items := startupRecentItemsFromRollout(sessions, limit)
	if len(items) == 0 {
		return startupRecentsMsg{source: startupRecentsFromNone}
	}
	return startupRecentsMsg{items: items, source: startupRecentsFromRollout}
}

func startupRecentsMsgFromTasks(tasks []datamanager.TaskHistoryItem, err error, limit int) startupRecentsMsg {
	if err != nil {
		return startupRecentsMsg{source: startupRecentsFromNone}
	}
	items := startupRecentItemsFromTasks(tasks, limit)
	if len(items) == 0 {
		return startupRecentsMsg{source: startupRecentsFromNone}
	}
	return startupRecentsMsg{items: items, source: startupRecentsFromTasks}
}

func loadStartupRecentsCmd(m *model) tea.Cmd {
	return func() tea.Msg {
		sessions, rolloutErr := memory.ListRolloutSessions(memory.RolloutRootDir(), startupRecentsLimit)
		if msg := startupRecentsMsgFromRollout(sessions, rolloutErr, startupRecentsLimit); msg.source == startupRecentsFromRollout {
			return msg
		}
		if m.dataManager == nil {
			return startupRecentsMsg{source: startupRecentsFromNone}
		}
		tasks, tasksErr := m.dataManager.ListTaskHistoryFast(startupRecentsLimit)
		return startupRecentsMsgFromTasks(tasks, tasksErr, startupRecentsLimit)
	}
}

func (m *model) startupRecentsFingerprint() string {
	if len(m.startupRecents) == 0 {
		return "0"
	}
	return fmt.Sprintf("%d:%d:%d", len(m.startupRecents), m.startupRecents[0].When.Unix(), m.startupRecentsSource)
}

func (m *model) handleStartupRecentsMsg(msg startupRecentsMsg) (*model, tea.Cmd) {
	m.startupRecents, m.startupRecentsSource = msg.items, msg.source
	m.welcomeCacheKey = ""
	if len(m.logEntries) == 0 {
		m.bodyRepaint(0)
	}
	m.viewportDirty = true
	return m, nil
}
