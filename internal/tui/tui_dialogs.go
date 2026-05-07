package tui

import (
	"fmt"
	"strings"
	"time"

	"codeactor/pkg/messaging"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func (m *model) openConfirmDialog(event *messaging.MessageEvent) {
	content, ok := event.Content.(map[string]interface{})
	if !ok {
		return
	}
	question, _ := content["question"].(string)
	if question == "" {
		return
	}
	requestID, _ := content["request_id"].(string)

	m.confirmDialog = confirmDialog{
		open:           true,
		question:       question,
		requestID:      requestID,
		selectedOption: 0, // default: Allow
	}
}

// respondToAuth publishes the user response and closes the dialog.
func (m *model) respondToAuth(response string) {
	if m.publisher != nil {
		m.publisher.Publish("user_help_response", map[string]interface{}{
			"response":   response,
			"request_id": m.confirmDialog.requestID,
		}, "User")
	}
	m.confirmDialog.open = false

	// Log the response
	m.logEntries = append(m.logEntries, logEntry{
		timestamp: time.Now(),
		eventType: "status",
		content:   fmt.Sprintf("Auth response: %s", response),
	})
	m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
}

// listenForPublisher waits for the publisher to become available via the channel.
func listenForPublisher(ch chan *messaging.MessagePublisher) tea.Cmd {
	return func() tea.Msg {
		publisher, ok := <-ch
		if !ok {
			return nil
		}
		return publisherReadyMsg{publisher: publisher}
	}
}

// parseConfirmQuestion extracts toolName and detail body from the question string.
func parseConfirmQuestion(question string) (toolName, body string) {
	q := strings.TrimSpace(question)
	// Remove markdown bold
	q = strings.ReplaceAll(q, "**", "")

	// Extract tool name from pattern: 工具 `name` or tool `name`
	toolName = "?"
	if idx := strings.Index(q, "工具 `"); idx >= 0 {
		start := idx + len("工具 `")
		if end := strings.Index(q[start:], "`"); end >= 0 {
			toolName = q[start : start+end]
		}
	} else if idx := strings.Index(q, "tool `"); idx >= 0 {
		start := idx + len("tool `")
		if end := strings.Index(q[start:], "`"); end >= 0 {
			toolName = q[start : start+end]
		}
	}

	// Extract body: after first blank line, before boilerplate explanatory text
	// Split by double newline to separate header / body / footer
	parts := strings.SplitN(q, "\n\n", 3)
	if len(parts) >= 2 {
		// parts[0] = header line, parts[1..] = body (may include boilerplate)
		body = strings.Join(parts[1:], "\n\n")
	} else if len(parts) == 1 {
		body = parts[0]
	}

	// Strip boilerplate suffixes
	boilerplates := []string{
		"此操作可能影响工作空间外的文件或系统环境。是否允许执行？",
		"是否允许执行？",
		"This operation may affect files or the system environment outside the workspace. Allow?",
	}
	for _, bp := range boilerplates {
		body = strings.ReplaceAll(body, "\n\n"+bp, "")
		body = strings.ReplaceAll(body, bp, "")
	}
	body = strings.TrimSpace(body)

	if body == "" {
		body = q
	}
	return toolName, body
}

// renderConfirmDialog renders the authorization confirmation overlay dialog.
func (m model) renderConfirmDialog() string {
	const maxDialogWidth = 64
	dialogWidth := maxDialogWidth
	if m.termWidth-4 < dialogWidth {
		dialogWidth = m.termWidth - 4
	}
	innerWidth := dialogWidth - 4

	toolName, body := parseConfirmQuestion(m.confirmDialog.question)

	// ── Tool name badge ──
	toolLine := confirmToolStyle.Render("⚡ " + toolName)

	// ── Command / detail ──
	detailWidth := innerWidth
	if detailWidth < 20 {
		detailWidth = 20
	}
	detail := wrapText(body, detailWidth)
	detail = confirmDetailStyle.Render(detail)

	// ── Buttons (3 options) ──
	renderBtn := func(label string, idx int) string {
		if m.confirmDialog.selectedOption == idx {
			return confirmButtonFocused.Render(label)
		}
		return confirmButtonBlurred.Render(label)
	}
	buttons := lipgloss.JoinHorizontal(lipgloss.Center,
		renderBtn("Allow", 0),
		"  ",
		renderBtn("Allow All", 1),
		"  ",
		renderBtn("Deny", 2),
	)

	// ── Help ──
	help := confirmHelpStyle.Render(langManager.GetText("ConfirmDialogHelp"))

	// ── Assemble with a horizontal separator between detail and buttons ──
	sep := lipgloss.NewStyle().
		Foreground(lipgloss.Color("237")).
		Width(innerWidth).
		Render(strings.Repeat("─", innerWidth))

	content := lipgloss.JoinVertical(lipgloss.Left,
		toolLine,
		"",
		detail,
		"",
		sep,
		lipgloss.NewStyle().Width(innerWidth).Align(lipgloss.Center).Render(buttons),
		help,
	)

	dialog := confirmBorderStyle.Width(dialogWidth).Render(content)

	return lipgloss.Place(m.termWidth, m.termHeight,
		lipgloss.Center, lipgloss.Center,
		dialog,
	)
}

// renderTaskCompleteDialog renders the task completion overlay dialog.
func (m model) renderTaskCompleteDialog() string {
	const maxDialogWidth = 40
	dialogWidth := maxDialogWidth
	if m.termWidth-4 < dialogWidth {
		dialogWidth = m.termWidth - 4
	}
	innerWidth := dialogWidth - 4

	// ── Title ──
	titleLine := taskCompleteTitleStyle.Render("Task Completed")

	// ── OK Button ──
	okBtn := taskCompleteButtonFocused.Render("OK")

	// ── Help text ──
	help := confirmHelpStyle.Render("Press ENTER or SPACE to close")

	// ── Separator ──
	sep := lipgloss.NewStyle().
		Foreground(lipgloss.Color("237")).
		Width(innerWidth).
		Render(strings.Repeat("─", innerWidth))

	// ── Assemble ──
	content := lipgloss.JoinVertical(lipgloss.Left,
		titleLine,
		"",
		sep,
		"",
		lipgloss.NewStyle().Width(innerWidth).Align(lipgloss.Center).Render(okBtn),
		"",
		help,
	)

	dialog := taskCompleteBorderStyle.Width(dialogWidth).Render(content)

	return lipgloss.Place(m.termWidth, m.termHeight,
		lipgloss.Center, lipgloss.Center,
		dialog,
	)
}

// renderConfirmQuitDialog renders the quit confirmation overlay dialog.
func (m model) renderConfirmQuitDialog() string {
	const maxDialogWidth = 44
	dialogWidth := maxDialogWidth
	if m.termWidth-4 < dialogWidth {
		dialogWidth = m.termWidth - 4
	}
	innerWidth := dialogWidth - 4

	// ── Title ──
	titleLine := confirmQuitTitleStyle.Render(langManager.GetText("ConfirmQuitTitle"))

	// ── Message ──
	message := confirmQuitMessageStyle.Render(langManager.GetText("ConfirmQuitMessage"))

	// ── Buttons (2 options) ──
	renderBtn := func(label string, idx int) string {
		if m.confirmQuitDialog.selectedOption == idx {
			return confirmQuitButtonFocused.Render(label)
		}
		return confirmQuitButtonBlurred.Render(label)
	}
	buttons := lipgloss.JoinHorizontal(lipgloss.Center,
		renderBtn(langManager.GetText("ConfirmDialogYes"), 0),
		"  ",
		renderBtn(langManager.GetText("ConfirmDialogNo"), 1),
	)

	// ── Help ──
	help := confirmHelpStyle.Render("←/→ choose  enter confirm  y/n")

	// ── Separator ──
	sep := lipgloss.NewStyle().
		Foreground(lipgloss.Color("237")).
		Width(innerWidth).
		Render(strings.Repeat("─", innerWidth))

	// ── Assemble ──
	content := lipgloss.JoinVertical(lipgloss.Left,
		titleLine,
		"",
		message,
		"",
		sep,
		"",
		lipgloss.NewStyle().Width(innerWidth).Align(lipgloss.Center).Render(buttons),
		"",
		help,
	)

	dialog := confirmQuitBorderStyle.Width(dialogWidth).Render(content)

	return lipgloss.Place(m.termWidth, m.termHeight,
		lipgloss.Center, lipgloss.Center,
		dialog,
	)
}

// renderConfirmCancelDialog renders the cancel task confirmation overlay dialog.
func (m model) renderConfirmCancelDialog() string {
	const maxDialogWidth = 48
	dialogWidth := maxDialogWidth
	if m.termWidth-4 < dialogWidth {
		dialogWidth = m.termWidth - 4
	}
	innerWidth := dialogWidth - 4

	// ── Title ──
	titleLine := confirmCancelTitleStyle.Render(langManager.GetText("ConfirmCancelTitle"))

	// ── Message ──
	message := confirmQuitMessageStyle.Render(langManager.GetText("ConfirmCancelMessage"))

	// ── Buttons (2 options) ──
	renderBtn := func(label string, idx int) string {
		if m.confirmCancelDialog.selectedOption == idx {
			return confirmCancelButtonFocused.Render(label)
		}
		return confirmCancelButtonBlurred.Render(label)
	}
	buttons := lipgloss.JoinHorizontal(lipgloss.Center,
		renderBtn(langManager.GetText("ConfirmDialogYes"), 0),
		"  ",
		renderBtn(langManager.GetText("ConfirmDialogNo"), 1),
	)

	// ── Help ──
	help := confirmHelpStyle.Render("←/→ choose  enter confirm  y yes  n/esc cancel")

	// ── Separator ──
	sep := lipgloss.NewStyle().
		Foreground(lipgloss.Color("237")).
		Width(innerWidth).
		Render(strings.Repeat("─", innerWidth))

	// ── Assemble ──
	content := lipgloss.JoinVertical(lipgloss.Left,
		titleLine,
		"",
		message,
		"",
		sep,
		"",
		lipgloss.NewStyle().Width(innerWidth).Align(lipgloss.Center).Render(buttons),
		"",
		help,
	)

	dialog := confirmCancelBorderStyle.Width(dialogWidth).Render(content)

	return lipgloss.Place(m.termWidth, m.termHeight,
		lipgloss.Center, lipgloss.Center,
		dialog,
	)
}

// renderHelpDialog renders the vim-like help overlay showing all command mode shortcuts.
func (m model) renderHelpDialog() string {
	const maxDialogWidth = 50
	dialogWidth := maxDialogWidth
	if m.termWidth-4 < dialogWidth {
		dialogWidth = m.termWidth - 4
	}
	innerWidth := dialogWidth - 4

	// ── Title ──
	titleStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("39")).Bold(true)
	titleLine := titleStyle.Render("?  " + langManager.GetText("HelpDialogTitle"))

	// ── Content ──
	contentStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252"))
	content := contentStyle.Render(langManager.GetText("HelpDialogContent"))

	// ── Separator ──
	sepStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("237")).
		Width(innerWidth)
	sep := sepStyle.Render(strings.Repeat("─", innerWidth))

	// ── Dismiss hint ──
	hintStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240"))
	hint := hintStyle.Render("Press any key to dismiss")

	// ── Assemble ──
	dialogContent := lipgloss.JoinVertical(lipgloss.Left,
		titleLine,
		"",
		content,
		"",
		sep,
		"",
		hint,
	)

	dialogStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("39")).
		Padding(1, 2)

	dialog := dialogStyle.Width(dialogWidth).Render(dialogContent)

	return lipgloss.Place(m.termWidth, m.termHeight,
		lipgloss.Center, lipgloss.Center,
		dialog,
	)
}

// confirmDialog styles
var (
	confirmBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("240")).
				Padding(0, 2)

	confirmToolStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("214")).
				Bold(true)

	confirmDetailStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("252"))

	confirmButtonFocused = lipgloss.NewStyle().
				Foreground(lipgloss.Color("0")).
				Background(lipgloss.Color("214")).
				Bold(true).
				Padding(0, 2)

	confirmButtonBlurred = lipgloss.NewStyle().
				Foreground(lipgloss.Color("244")).
				Padding(0, 2)

	confirmHelpStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("240"))
)

// taskCompleteDialog styles
var (
	taskCompleteBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("36")). // 青绿色边框，表示成功
				Padding(0, 2)

	taskCompleteTitleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("36")). // 青绿色
				Bold(true)

	taskCompleteMessageStyle = lipgloss.NewStyle().
					Foreground(lipgloss.Color("252")). // 浅灰色详情文字
					MaxWidth(50)

	taskCompleteButtonFocused = lipgloss.NewStyle().
					Foreground(lipgloss.Color("0")).  // 黑字
					Background(lipgloss.Color("36")). // 青绿色底
					Bold(true).
					Padding(0, 4)

	taskCompleteButtonBlurred = lipgloss.NewStyle().
					Foreground(lipgloss.Color("244")). // 灰色未选中
					Padding(0, 4)
)

// confirmQuitDialog styles
var (
	confirmQuitBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("167")). // red border for warning
				Padding(0, 2)

	confirmQuitTitleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("167")). // red
				Bold(true)

	confirmQuitMessageStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("252")). // light gray
				MaxWidth(50)

	confirmQuitButtonFocused = lipgloss.NewStyle().
					Foreground(lipgloss.Color("0")).
					Background(lipgloss.Color("167")). // red bg
					Bold(true).
					Padding(0, 2)

	confirmQuitButtonBlurred = lipgloss.NewStyle().
					Foreground(lipgloss.Color("244")).
					Padding(0, 2)
)

// confirmCancelDialog styles
var (
	confirmCancelBorderStyle = lipgloss.NewStyle().
					Border(lipgloss.RoundedBorder()).
					BorderForeground(lipgloss.Color("214")). // yellow/orange border for warning
					Padding(0, 2)

	confirmCancelTitleStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("214")). // yellow/orange
				Bold(true)

	confirmCancelButtonFocused = lipgloss.NewStyle().
					Foreground(lipgloss.Color("0")).
					Background(lipgloss.Color("214")). // yellow/orange bg
					Bold(true).
					Padding(0, 2)

	confirmCancelButtonBlurred = lipgloss.NewStyle().
					Foreground(lipgloss.Color("244")).
					Padding(0, 2)
)

// parseConfirmQuestion extracts toolName and detail body from the question string.
