package main

import (
	"fmt"
	"os"
	"strings"

	"codeactor/internal/assistant"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Global Language Manager
var langManager *LanguageManager

// Global styles — Claude Code-like minimalist aesthetic
var (
	// Banner
	bannerPadStyle = lipgloss.NewStyle().Padding(0, 1)

	// Prompt input — ❯ prefix style
	promptFocusedStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	promptBlurredStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	// Input border — used by history modal search
	focusedInputStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("39")).Padding(0, 1)
	blurredInputStyle = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("237")).Padding(0, 1)

	// Welcome panel
	welcomePanelStyle   = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("39")).Padding(1, 2)
	welcomeLeftStyle    = lipgloss.NewStyle().Width(38)
	welcomeTitleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
	welcomeSubStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	welcomeRightTitle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252"))
	welcomeTipStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	welcomeDimStyle     = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("242"))

	// Buttons — text-only, bold accent when focused, dim otherwise
	buttonFocusedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	buttonBlurredStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	// Messages
	errorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("167")).Bold(true)
	infoStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	// Footer
	footerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

	// Modal
	backdropStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("237"))
	modalBoxStyle   = lipgloss.NewStyle().Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("39")).Padding(1, 2)
	modalTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	itemStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	itemDimStyle    = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("244"))
	itemSelStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("39"))
)

// TUI Model
type model struct {
	focusIndex  int
	inputs      []textinput.Model
	projectDir  string
	taskDesc    string
	errorMsg    string
	infoMsg     string
	quitting    bool
	currentLang Language
	// terminal dimensions for responsive layout
	termWidth  int
	termHeight int
	// history modal state
	showHistoryModal bool
	historyItems     []assistant.TaskHistoryItem
	filteredItems    []assistant.TaskHistoryItem
	historyIndex     int
	historySearch    textinput.Model
}

func initialModel(preloadedTaskContent string) model {
	var inputs []textinput.Model

	// 使用预加载的任务内容或尝试从默认文件加载
	taskContent := preloadedTaskContent
	if taskContent == "" {
		// 如果没有预加载内容，检查默认的 TASK.md 文件
		taskFile := "TASK.md"
		if data, err := os.ReadFile(taskFile); err == nil {
			taskContent = string(data)
		}
	}

	// Only task description input
	ti := textinput.New()
	ti.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	ti.Placeholder = langManager.GetText("TaskDescPlaceholder")
	ti.Focus()
	ti.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	ti.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	ti.CharLimit = 256
	ti.Width = 60
	if taskContent != "" {
		ti.SetValue(taskContent)
	}
	inputs = append(inputs, ti)

	// history search input
	hSearch := textinput.New()
	hSearch.Placeholder = langManager.GetText("HistorySearchHint")
	hSearch.CharLimit = 256
	hSearch.Width = 60
	hSearch.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))

	projectDir, _ := os.Getwd()

	return model{
		inputs:        inputs,
		projectDir:    projectDir,
		focusIndex:    0,
		infoMsg:       langManager.GetText("InfoMessage"),
		currentLang:   langManager.currentLang,
		historySearch: hSearch,
	}
}

func (m model) Init() tea.Cmd {
	return textinput.Blink
}

// setFocus blurs the current focused input and focuses the new index if within range.
func (m *model) setFocus(newIndex int) tea.Cmd {
	// Blur all inputs to ensure only one is focused
	for i := range m.inputs {
		m.inputs[i].Blur()
	}

	if newIndex < 0 {
		newIndex = 0
	}
	// allow focusing submit (len), language (len+1), history (len+2)
	if newIndex > len(m.inputs)+2 {
		newIndex = 0
	}

	m.focusIndex = newIndex
	if newIndex < len(m.inputs) {
		return m.inputs[newIndex].Focus()
	}
	return nil
}

func (m *model) toggleLanguage() {
	if m.currentLang == LangEnglish {
		langManager.SetLanguage(LangChinese)
		m.currentLang = LangChinese
	} else {
		langManager.SetLanguage(LangEnglish)
		m.currentLang = LangEnglish
	}
	// refresh placeholders to reflect current language
	m.inputs[0].Placeholder = langManager.GetText("TaskDescPlaceholder")
	m.infoMsg = langManager.GetText("InfoMessage")
	// also refresh history search placeholder (modal might open later)
	m.historySearch.Placeholder = langManager.GetText("HistorySearchHint")
}

func (m *model) openHistoryModal() {
	// load history
	dm, err := assistant.NewDataManager()
	if err == nil {
		items, err2 := dm.ListTaskHistory(50)
		if err2 == nil {
			m.historyItems = items
			m.filteredItems = items
		}
	}
	m.historyIndex = 0
	m.showHistoryModal = true
	m.historySearch.SetValue("")
	m.historySearch.Focus()
}

func (m *model) closeHistoryModal() {
	m.showHistoryModal = false
	m.historySearch.Blur()
}

func (m *model) applyHistorySelection() {
	if len(m.filteredItems) == 0 {
		return
	}
	if m.historyIndex < 0 {
		m.historyIndex = 0
	}
	if m.historyIndex >= len(m.filteredItems) {
		m.historyIndex = len(m.filteredItems) - 1
	}
	selected := m.filteredItems[m.historyIndex]
	// Use the title (first human message) as task description
	m.inputs[0].SetValue(selected.Title)
	m.closeHistoryModal()
	// Move focus to submit for quick execution
	m.setFocus(len(m.inputs))
}

func (m *model) filterHistoryList() {
	query := strings.TrimSpace(m.historySearch.Value())
	if query == "" {
		m.filteredItems = m.historyItems
		m.historyIndex = 0
		return
	}
	qLower := strings.ToLower(query)
	filtered := make([]assistant.TaskHistoryItem, 0, len(m.historyItems))
	for _, it := range m.historyItems {
		txt := strings.ToLower(it.Title + " " + it.TaskID)
		if strings.Contains(txt, qLower) {
			filtered = append(filtered, it)
		}
	}
	m.filteredItems = filtered
	if m.historyIndex >= len(m.filteredItems) {
		m.historyIndex = 0
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		// Keep latest terminal size and update input widths responsively
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		fieldWidth := m.computeFieldWidth()
		for i := range m.inputs {
			m.inputs[i].Width = fieldWidth
		}
		m.historySearch.Width = fieldWidth
		return m, nil
	case tea.KeyMsg:
		// If modal is open, handle modal controls first
		if m.showHistoryModal {
			switch msg.String() {
			case "esc", "ctrl+c":
				m.closeHistoryModal()
				return m, nil
			case "enter":
				m.applyHistorySelection()
				return m, nil
			case "up":
				if m.historyIndex > 0 {
					m.historyIndex--
				}
				return m, nil
			case "down":
				if m.historyIndex < len(m.filteredItems)-1 {
					m.historyIndex++
				}
				return m, nil
			default:
				// update search input and filter
				var cmd tea.Cmd
				m.historySearch, cmd = m.historySearch.Update(msg)
				m.filterHistoryList()
				return m, cmd
			}
		}

		switch msg.String() {
		case "ctrl+c", "esc":
			m.quitting = true
			return m, tea.Quit

		case "tab", "down":
			m.errorMsg = ""
			newIndex := m.focusIndex + 1
			if newIndex > len(m.inputs)+2 {
				newIndex = 0
			}
			cmd := m.setFocus(newIndex)
			return m, cmd

		case "shift+tab", "up":
			m.errorMsg = ""
			newIndex := m.focusIndex - 1
			if newIndex < 0 {
				newIndex = len(m.inputs) + 2
			}
			cmd := m.setFocus(newIndex)
			return m, cmd

		case "enter":
			if m.focusIndex < len(m.inputs) {
				cmd := m.setFocus(m.focusIndex + 1)
				return m, cmd
			} else if m.focusIndex == len(m.inputs) {
				// focus is on submit button
				m.projectDir, _ = os.Getwd()
				m.taskDesc = m.inputs[0].Value()
				if ok, err := validateInputs(m.projectDir, m.taskDesc); ok {
					m.quitting = true
					return m, tea.Quit
				} else {
					m.errorMsg = err
					return m, nil
				}
			} else if m.focusIndex == len(m.inputs)+1 {
				// language button
				m.toggleLanguage()
				return m, nil
			} else if m.focusIndex == len(m.inputs)+2 {
				// history button
				m.openHistoryModal()
				return m, nil
			}

		case "ctrl+s":
			m.projectDir, _ = os.Getwd()
			m.taskDesc = m.inputs[0].Value()
			if ok, err := validateInputs(m.projectDir, m.taskDesc); ok {
				m.quitting = true
				return m, tea.Quit
			} else {
				m.errorMsg = err
				return m, nil
			}

		case "ctrl+l": // Language switch
			if m.focusIndex == len(m.inputs)+1 {
				m.toggleLanguage()
				return m, nil
			}
			cmd := m.setFocus(len(m.inputs) + 1)
			return m, cmd
		case "ctrl+h": // Open history quickly
			m.openHistoryModal()
			return m, nil
		}
	}

	// Handle character input and blinking
	cmd := m.updateInputs(msg)
	return m, cmd
}

func (m *model) updateInputs(msg tea.Msg) tea.Cmd {
	var cmds []tea.Cmd
	for i := range m.inputs {
		// only update focused input
		if m.inputs[i].Focused() {
			newInput, cmd := m.inputs[i].Update(msg)
			m.inputs[i] = newInput
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

func (m model) View() string {
	if m.quitting {
		return langManager.GetText("QuitMessage")
	}

	var b strings.Builder

	// Welcome panel with logo and tips
	b.WriteString(m.renderWelcomePanel())

	// Spacing
	b.WriteString("\n\n")

	// Prompt input line: ❯ [input]
	promptChar := "❯ "
	var promptStyled string
	if m.inputs[0].Focused() {
		promptStyled = promptFocusedStyle.Render(promptChar)
	} else {
		promptStyled = promptBlurredStyle.Render(promptChar)
	}
	fieldWidth := m.computeFieldWidth()
	m.inputs[0].Width = fieldWidth
	inputLine := promptStyled + m.inputs[0].View()
	b.WriteString(lipgloss.NewStyle().MarginLeft(2).Render(inputLine))

	// Error or info message
	b.WriteString("\n")
	if m.errorMsg != "" {
		b.WriteString(lipgloss.NewStyle().MarginLeft(2).Render(errorStyle.Render("✖ " + m.errorMsg)))
	} else {
		b.WriteString(lipgloss.NewStyle().MarginLeft(2).Render(infoStyle.Render(m.infoMsg)))
	}

	// Action buttons
	b.WriteString("\n")
	var buttons strings.Builder
	submitLabel := langManager.GetText("SubmitButton")
	if m.focusIndex == len(m.inputs) {
		buttons.WriteString(buttonFocusedStyle.Render("● " + submitLabel))
	} else {
		buttons.WriteString(buttonBlurredStyle.Render("○ " + submitLabel))
	}
	buttons.WriteString("  ")

	currentLangLabel := "EN"
	if m.currentLang == LangChinese {
		currentLangLabel = "中文"
	}
	langLabel := fmt.Sprintf("%s: %s", langManager.GetText("LanguageButton"), currentLangLabel)
	if m.focusIndex == len(m.inputs)+1 {
		buttons.WriteString(buttonFocusedStyle.Render("● " + langLabel))
	} else {
		buttons.WriteString(buttonBlurredStyle.Render("○ " + langLabel))
	}
	buttons.WriteString("  ")

	histLabel := langManager.GetText("HistoryButton")
	if m.focusIndex == len(m.inputs)+2 {
		buttons.WriteString(buttonFocusedStyle.Render("● " + histLabel))
	} else {
		buttons.WriteString(buttonBlurredStyle.Render("○ " + histLabel))
	}
	b.WriteString(lipgloss.NewStyle().MarginLeft(2).Render(buttons.String()))

	// Spacing before footer
	b.WriteString("\n\n")

	// Status bar
	b.WriteString(renderStatusBar(m))

	// History modal overlay
	if m.showHistoryModal {
		b.WriteString("\n")
		b.WriteString(m.renderHistoryModal())
	}

	return b.String()
}

func (m model) renderWelcomePanel() string {
	// Build left panel: welcome text + logo + model info
	var left strings.Builder
	// left.WriteString(welcomeTitleStyle.Render("      Welcome back!"))
	// left.WriteString("\n\n")
	left.WriteString(renderBanner())
	left.WriteString("\n\n")
	// Show cwd with home abbrev
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

	// Pad left content to fill the column
	// leftContent is already width-styled via welcomeLeftStyle

	separator := welcomeDimStyle.Render(" │ ")

	leftStyled := lipgloss.NewStyle().Width(leftWidth).Render(leftContent)
	rightStyled := lipgloss.NewStyle().Width(rightWidth).Render(right.String())

	inner := lipgloss.JoinHorizontal(lipgloss.Top, leftStyled, separator, rightStyled)
	return welcomePanelStyle.Width(panelWidth).Render(inner)
}

func validateInputs(projectDir, taskDesc string) (bool, string) {
	if strings.TrimSpace(taskDesc) == "" {
		return false, langManager.GetText("ValidationErrorEmptyTaskDesc")
	}
	if len([]rune(taskDesc)) < 4 {
		return false, langManager.GetText("ValidationErrorShortTaskDesc")
	}
	return true, ""
}

// 启动TUI界面
func startTUI(taskFilePath string) (string, string) {
	// Initialize language manager with default English
	langManager = NewLanguageManager()

	// 如果提供了任务文件路径，则从文件加载任务描述
	taskContent := ""
	if taskFilePath != "" {
		if data, err := os.ReadFile(taskFilePath); err == nil {
			taskContent = string(data)
		} else {
			fmt.Printf("无法读取任务文件: %v\n", err)
		}
	}

	p := tea.NewProgram(initialModel(taskContent))
	if m, err := p.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
		return "", ""
	} else {
		final := m.(model)
		return final.projectDir, final.taskDesc
	}
}

// renderBanner draws a rainbow ASCII logo with per-character coloring.
func renderBanner() string {
	asciiLogo := []string{
		"╔╦╗╦  ╦  ╔═╗┌─┐┌┬┐┌─┐",
		" ║║║  ║  ║  │ │ ││├┤ ",
		"═╩╝╩═╝╩  ╚═╝└─┘─┴┘└─┘",
	}

	// RAINBOW_COLORS: muted pastel tones — red, orange, yellow, green, blue, indigo, violet
	rainbowColors := []string{
		"167", // soft red (salmon)
		"180", // soft orange (tan)
		"221", // soft yellow (golden)
		"114", // soft green (mint)
		"75",  // soft blue (sky)
		"98",  // soft indigo (lavender)
		"176", // soft violet (mauve)
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

// renderStatusBar shows cwd and language in a subtle footer.
func renderStatusBar(m model) string {
	cwd, _ := os.Getwd()
	lang := "EN"
	if m.currentLang == LangChinese {
		lang = "ZH"
	}
	left := cwd
	right := fmt.Sprintf("%s │ esc quit", lang)
	// Build a full-width bar
	width := 80
	if m.termWidth > 0 {
		width = m.termWidth
	}
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return footerStyle.Render(left + strings.Repeat(" ", gap) + right)
}

// computeFieldWidth returns a responsive width for input fields.
func (m model) computeFieldWidth() int {
	const minField = 38
	const maxField = 90
	if m.termWidth <= 0 {
		return 60
	}
	// labels are stacked, only container paddings/borders count
	avail := m.termWidth - 8 // container left/right padding + borders
	if avail < minField {
		return minField
	}
	if avail > maxField {
		return maxField
	}
	return avail
}

// renderHistoryModal renders a clean history selection popup
func (m model) renderHistoryModal() string {
	var out strings.Builder

	// Divider
	out.WriteString(backdropStyle.Render(strings.Repeat("─", max(40, m.computeFieldWidth()+8))))
	out.WriteString("\n")

	// Title + search hint
	title := modalTitleStyle.Render("◇ " + langManager.GetText("HistoryTitle"))
	searchWidth := m.computeFieldWidth()
	m.historySearch.Width = searchWidth
	searchLine := focusedInputStyle.Render(m.historySearch.View())

	boxInner := title + "\n" + itemDimStyle.Render(langManager.GetText("HistorySearchHint")) + "\n\n" + searchLine + "\n"

	// List items
	maxItems := 12
	if len(m.filteredItems) == 0 {
		boxInner += "\n" + itemDimStyle.Render(langManager.GetText("HistoryEmpty"))
	} else {
		end := len(m.filteredItems)
		if end > maxItems {
			start := m.historyIndex - maxItems/2
			if start < 0 {
				start = 0
			}
			end = start + maxItems
			if end > len(m.filteredItems) {
				end = len(m.filteredItems)
				start = end - maxItems
				if start < 0 {
					start = 0
				}
			}
			for i := start; i < end; i++ {
				it := m.filteredItems[i]
				line := fmt.Sprintf("%s  %s", itemDimStyle.Render(it.CreatedAt.Format("01-02 15:04")), it.Title)
				if i == m.historyIndex {
					boxInner += "\n" + itemSelStyle.Render(line)
				} else {
					boxInner += "\n" + itemStyle.Render(line)
				}
			}
		} else {
			for i, it := range m.filteredItems {
				line := fmt.Sprintf("%s  %s", itemDimStyle.Render(it.CreatedAt.Format("01-02 15:04")), it.Title)
				if i == m.historyIndex {
					boxInner += "\n" + itemSelStyle.Render(line)
				} else {
					boxInner += "\n" + itemStyle.Render(line)
				}
			}
		}
	}

	// Footer
	footer := lipgloss.JoinHorizontal(lipgloss.Top,
		buttonFocusedStyle.Render("● "+langManager.GetText("HistoryUseSelected")),
		"  ",
		buttonBlurredStyle.Render("○ "+langManager.GetText("HistoryClose")),
	)
	boxInner += "\n\n" + footer

	box := modalBoxStyle.Width(m.computeFieldWidth() + 8).Render(boxInner)
	leftAligned := lipgloss.NewStyle().MarginLeft(2).Render(box)
	out.WriteString(leftAligned)
	out.WriteString("\n")

	return out.String()
}

// small helper since we cannot import math for just Max
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
