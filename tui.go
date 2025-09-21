package main

import (
	"fmt"
	"os"
	"strings"

	"codee/internal/assistant"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Add language support
var currentLanguage = "en" // Default to English

// Global Language Manager
var langManager *LanguageManager

// Global styles
var (
	titleStyle         = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205")).MarginBottom(1)
	labelStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Width(18)
	focusedInputStyle  = lipgloss.NewStyle().BorderStyle(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("205")).Padding(0, 1)
	blurredInputStyle  = lipgloss.NewStyle().BorderStyle(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 1)
	buttonFocusedStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("205")).Padding(0, 2).MarginTop(1)
	buttonBlurredStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).BorderStyle(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(0, 2).MarginTop(1)
	helpStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).MarginTop(1)
	errorStyle         = lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Bold(true)
	infoStyle          = lipgloss.NewStyle().Foreground(lipgloss.Color("36"))
	containerStyle     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("240")).Padding(1, 2)
	// New styles for banner/tips/footer
	bannerShadowStyle = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("240"))
	bannerPadStyle    = lipgloss.NewStyle().Padding(0, 1)
	tipBulletStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("213")).Bold(true)
	tipTextStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	sectionGapStyle   = lipgloss.NewStyle().MarginBottom(1)
	footerStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Background(lipgloss.Color("236")).Padding(0, 1)
	// Field label style (used above inputs, full width, left aligned)
	fieldLabelStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Bold(true)
	// Modal styles
	backdropStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("236"))
	modalBoxStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("213")).Padding(1, 2).Background(lipgloss.Color("235"))
	modalTitleStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("213"))
	itemStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	itemDimStyle    = lipgloss.NewStyle().Faint(true).Foreground(lipgloss.Color("244"))
	itemSelStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("0")).Background(lipgloss.Color("213")).Padding(0, 1)
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

func initialModel() model {
	var inputs []textinput.Model
	cwd, err := os.Getwd()
	if err != nil {
		cwd = ""
	}

	// Check if TASK.md exists in current directory
	taskContent := ""
	taskFile := "TASK.md"
	if data, err := os.ReadFile(taskFile); err == nil {
		taskContent = string(data)
	}

	for i := range []string{langManager.GetText("ProjectDirLabel"), langManager.GetText("TaskDescLabel")} {
		ti := textinput.New()
		ti.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
		if i == 0 {
			ti.Placeholder = langManager.GetText("ProjectDirPlaceholder")
			ti.Focus()
			ti.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
			ti.TextStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))
			ti.CharLimit = 256
			ti.Width = 60
			ti.SetValue(cwd)
		} else {
			ti.Placeholder = langManager.GetText("TaskDescPlaceholder")
			ti.CharLimit = 256
			ti.Width = 60
			if taskContent != "" {
				ti.SetValue(taskContent)
			}
		}

		inputs = append(inputs, ti)
	}

	// history search input
	hSearch := textinput.New()
	hSearch.Placeholder = langManager.GetText("HistorySearchHint")
	hSearch.CharLimit = 256
	hSearch.Width = 60
	hSearch.Cursor.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("205"))

	return model{
		inputs:        inputs,
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
	m.inputs[0].Placeholder = langManager.GetText("ProjectDirPlaceholder")
	m.inputs[1].Placeholder = langManager.GetText("TaskDescPlaceholder")
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
	m.inputs[1].SetValue(selected.Title)
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
				m.projectDir = m.inputs[0].Value()
				m.taskDesc = m.inputs[1].Value()
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
			m.projectDir = m.inputs[0].Value()
			m.taskDesc = m.inputs[1].Value()
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

	// Fancy banner
	b.WriteString(renderBanner())
	b.WriteString("\n")

	// Error or info
	if m.errorMsg != "" {
		b.WriteString("\n" + errorStyle.Render("✖ "+m.errorMsg))
	} else if m.infoMsg != "" {
		b.WriteString("\n" + infoStyle.Render("ℹ "+m.infoMsg))
	}

	// Tips
	b.WriteString("\n" + renderTips())

	// Inputs with labels
	var form strings.Builder
	fieldWidth := m.computeFieldWidth()
	for i := range m.inputs {
		label := langManager.GetText("ProjectDirLabel")
		if i == 1 {
			label = langManager.GetText("TaskDescLabel")
		}

		// label on its own line for clean left alignment
		form.WriteString(fieldLabelStyle.Render(label + ":"))
		form.WriteString("\n")

		// Ensure inputs render with the latest computed width
		m.inputs[i].Width = fieldWidth

		inputView := m.inputs[i].View()
		if m.inputs[i].Focused() {
			inputView = focusedInputStyle.Render(inputView)
		} else {
			inputView = blurredInputStyle.Render(inputView)
		}

		form.WriteString(inputView)
		if i < len(m.inputs)-1 {
			form.WriteString("\n\n")
		}
	}

	// Submit button
	var btn string
	if m.focusIndex == len(m.inputs) {
		btn = buttonFocusedStyle.Render(langManager.GetText("SubmitButton"))
	} else {
		btn = buttonBlurredStyle.Render(langManager.GetText("SubmitButton"))
	}
	form.WriteString("\n" + btn)

	// Language switch option shows current language code
	currentLangLabel := "EN"
	if m.currentLang == LangChinese {
		currentLangLabel = "中文"
	}
	langBtnText := fmt.Sprintf("%s (%s / ctrl+L)", langManager.GetText("LanguageButton"), currentLangLabel)
	if m.focusIndex == len(m.inputs)+1 {
		langBtnText = buttonFocusedStyle.Render(langBtnText)
	} else {
		langBtnText = buttonBlurredStyle.Render(langBtnText)
	}
	form.WriteString("\n" + langBtnText)

	// History button
	hBtnText := fmt.Sprintf("%s (ctrl+H)", langManager.GetText("HistoryButton"))
	if m.focusIndex == len(m.inputs)+2 {
		hBtnText = buttonFocusedStyle.Render(hBtnText)
	} else {
		hBtnText = buttonBlurredStyle.Render(hBtnText)
	}
	form.WriteString("\n" + hBtnText)

	// Left-align the form container with a small margin
	containerWidth := m.computeContainerWidth()
	leftPad := 2
	if m.termWidth > 0 && containerWidth > m.termWidth-2 {
		containerWidth = m.termWidth - 2
	}
	container := containerStyle.Width(containerWidth).Render(form.String())
	leftAligned := lipgloss.NewStyle().MarginLeft(leftPad).Render(container)
	b.WriteString("\n" + leftAligned)
	// Status bar footer
	b.WriteString("\n" + renderStatusBar(m))

	// Render modal if needed
	if m.showHistoryModal {
		b.WriteString("\n")
		b.WriteString(m.renderHistoryModal())
	}

	return b.String()
}

func validateInputs(projectDir, taskDesc string) (bool, string) {
	if strings.TrimSpace(projectDir) == "" {
		return false, langManager.GetText("ValidationErrorEmptyProjectDir")
	}
	if fi, err := os.Stat(projectDir); err != nil || !fi.IsDir() {
		return false, langManager.GetText("ValidationErrorInvalidProjectDir")
	}
	if strings.TrimSpace(taskDesc) == "" {
		return false, langManager.GetText("ValidationErrorEmptyTaskDesc")
	}
	if len([]rune(taskDesc)) < 4 {
		return false, langManager.GetText("ValidationErrorShortTaskDesc")
	}
	return true, ""
}

// 启动TUI界面
func startTUI() (string, string) {
	// Initialize language manager with default English
	langManager = NewLanguageManager()

	p := tea.NewProgram(initialModel())
	if m, err := p.Run(); err != nil {
		fmt.Printf("Alas, there's been an error: %v", err)
		os.Exit(1)
		return "", ""
	} else {
		final := m.(model)
		return final.projectDir, final.taskDesc
	}
}

// renderBanner draws a colorful ASCII banner similar in spirit to the reference image.
func renderBanner() string {
	bannerLines := []string{
		" ██████╗   ██████╗  ██████╗  ███████╗ ███████╗  ██╗",
		"██╔════╝  ██╔═══██╗ ██╔══██╗ ██╔════╝ ██╔════╝  ██║",
		"██║       ██║   ██║ ██║  ██║ █████╗   █████╗    ██║",
		"██║       ██║   ██║ ██║  ██║ ██╔══╝   ██╔══╝    ╚═╝",
		"╚██████╗  ╚██████╔╝ ██████╔╝ ███████╗ ███████╗  ██╗",
		" ╚═════╝   ╚═════╝  ╚═════╝  ╚══════╝ ╚══════╝  ╚═╝",
	}
	palette := []string{"213", "219", "159", "123", "81", "69"}
	var rendered []string
	for i, line := range bannerLines {
		color := palette[i%len(palette)]
		style := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Bold(true)
		shadow := bannerShadowStyle.Render(line)
		rendered = append(rendered, lipgloss.JoinHorizontal(lipgloss.Top, style.Render(line), " ", shadow))
	}
	return bannerPadStyle.Render(lipgloss.JoinVertical(lipgloss.Left, rendered...))
}

// renderTips shows getting-started tips styled like the screenshot.
func renderTips() string {
	tips := []string{
		"1. " + langManager.GetText("AskTips"),
		"2. " + langManager.GetText("BeSpecificTips"),
	}
	var rows []string
	for _, t := range tips {
		rows = append(rows, lipgloss.JoinHorizontal(lipgloss.Top, tipBulletStyle.Render("›"), " ", tipTextStyle.Render(t)))
	}
	return sectionGapStyle.Render(lipgloss.JoinVertical(lipgloss.Left, rows...))
}

// renderStatusBar shows cwd and language in a subtle status bar.
func renderStatusBar(m model) string {
	cwd, _ := os.Getwd()
	lang := "EN"
	if m.currentLang == LangChinese {
		lang = "ZH"
	}
	left := fmt.Sprintf("%s", cwd)
	right := fmt.Sprintf("%s", lang)
	width := 80
	if w := lipgloss.Width(left) + lipgloss.Width(right) + 3; w > width {
		width = w
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

// computeContainerWidth returns a pleasant container width within terminal bounds.
func (m model) computeContainerWidth() int {
	const minWidth = 56
	const maxWidth = 110
	if m.termWidth <= 0 {
		return 80
	}
	// base on field width plus container chrome (padding + borders)
	field := m.computeFieldWidth()
	computed := field + 8
	if computed < minWidth {
		computed = minWidth
	}
	if computed > maxWidth {
		computed = maxWidth
	}
	if computed > m.termWidth-2 {
		computed = m.termWidth - 2
	}
	if computed < 20 {
		computed = 20
	}
	return computed
}

// renderHistoryModal renders a beautiful history selection popup
func (m model) renderHistoryModal() string {
	var out strings.Builder

	// Backdrop block (simple line to separate)
	out.WriteString(backdropStyle.Render(strings.Repeat("⎯", max(40, m.computeContainerWidth()))))
	out.WriteString("\n")

	// Title
	title := modalTitleStyle.Render(langManager.GetText("HistoryTitle"))
	// Search input
	searchWidth := m.computeFieldWidth()
	m.historySearch.Width = searchWidth
	searchLine := focusedInputStyle.Render(m.historySearch.View())

	header := lipgloss.JoinHorizontal(lipgloss.Top, title, "  ", itemDimStyle.Render(langManager.GetText("HistorySearchHint")))
	boxInner := header + "\n\n" + searchLine + "\n\n"

	// List items (cap display for performance)
	maxItems := 12
	if len(m.filteredItems) == 0 {
		boxInner += itemDimStyle.Render(langManager.GetText("HistoryEmpty"))
	} else {
		end := len(m.filteredItems)
		if end > maxItems {
			// ensure selected is visible by computing a window around index
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
				line := fmt.Sprintf("%s  %s", itemDimStyle.Render(it.CreatedAt.Format("2006-01-02 15:04")), it.Title)
				if i == m.historyIndex {
					boxInner += itemSelStyle.Render(line)
				} else {
					boxInner += itemStyle.Render(line)
				}
				if i < end-1 {
					boxInner += "\n"
				}
			}
		} else {
			for i, it := range m.filteredItems {
				line := fmt.Sprintf("%s  %s", itemDimStyle.Render(it.CreatedAt.Format("2006-01-02 15:04")), it.Title)
				if i == m.historyIndex {
					boxInner += itemSelStyle.Render(line)
				} else {
					boxInner += itemStyle.Render(line)
				}
				if i < len(m.filteredItems)-1 {
					boxInner += "\n"
				}
			}
		}
	}

	// Footer actions
	footer := lipgloss.JoinHorizontal(lipgloss.Top,
		buttonFocusedStyle.Render(langManager.GetText("HistoryUseSelected")), "  ",
		buttonBlurredStyle.Render(langManager.GetText("HistoryClose")),
	)
	boxInner += "\n\n" + footer

	// Box
	box := modalBoxStyle.Width(m.computeContainerWidth()).Render(boxInner)
	// Centering by left margin
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
