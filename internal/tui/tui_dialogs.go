package tui

import (
	"fmt"
	"strings"
	"time"

	"codeactor/internal/messaging"
	"codeactor/internal/tui/components"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

func (m *model) openConfirmDialog(event *messaging.MessageEvent) {
	content, ok := event.Content.(map[string]interface{})
	if !ok {
		return
	}

	// 优先解析结构化字段
	toolName, _ := content["tool_name"].(string)
	reason, _ := content["reason"].(string)
	requestID, _ := content["request_id"].(string)
	command, _ := content["command"].(string)
	warning, _ := content["warning"].(string)

	if toolName == "" && reason == "" {
		// 向后兼容旧格式：从 question 字段解析
		question, _ := content["question"].(string)
		if question == "" {
			return
		}
		toolName, reason = parseConfirmQuestion(question)
		command = reason
		warning = ""
	}

	if m.dialogStack != nil {
		// 通过 DialogStack 打开确认弹窗
		d := components.NewConfirmDialog(toolName, command, warning, components.Language(m.currentLang))
		d.SetBounds(m.termWidth, m.termHeight)
		m.dialogStack.Push(d)
	} else {
		// 降级：使用原有 confirmDialog
		m.confirmDialog = confirmDialog{
			open:           true,
			toolName:       toolName,
			reason:         reason,
			requestID:      requestID,
			selectedOption: 0, // default: Allow
		}
	}
}

// respondToAuth publishes the user response and closes the dialog.
func (m *model) respondToAuth(response string) {
	if m.publisher != nil {
		// 优先从 DialogStack 获取 requestID
		requestID := ""
		if m.dialogStack != nil && m.dialogStack.Len() > 0 {
			m.dialogStack.Pop()
		} else {
			requestID = m.confirmDialog.requestID
		}
		m.publisher.Publish("user_help_response", map[string]interface{}{
			"response":   response,
			"request_id": requestID,
		}, "User")
	}
	if m.dialogStack == nil || m.dialogStack.Len() == 0 {
		m.confirmDialog.open = false
	}

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

// parseConfirmQuestion extracts toolName and detail body from the old question string.
// Only used for backward compatibility with old workspace_guard format.
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
	parts := strings.SplitN(q, "\n\n", 2)
	if len(parts) >= 2 {
		body = parts[1]
	} else {
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
	// border(4字符) + 内部padding(4字符) = 8字符额外开销
	innerWidth := dialogWidth - 8
	if innerWidth < 20 {
		innerWidth = 20
	}

	// ── 标题行 ──
	// 关键原则：先构建纯文本，在纯文本上截断，再应用样式
	titlePrefix := langManager.GetText("ConfirmAuthTitle")
	rawTitle := "⚡ " + titlePrefix + " — " + m.confirmDialog.toolName
	if lipgloss.Width(rawTitle) > innerWidth {
		runes := []rune(rawTitle)
		if len(runes) > innerWidth-3 {
			rawTitle = string(runes[:innerWidth-3]) + "..."
		}
	}
	toolLine := confirmToolStyle.Render(rawTitle)

	// ── 详情区域 ──
	var bodyContent string
	if m.confirmDialog.reason != "" {
		bodyContent = m.confirmDialog.reason + "\n\n"
	}
	bodyContent += langManager.GetText("ConfirmAuthWarning")
	detailWidth := innerWidth
	if detailWidth < 20 {
		detailWidth = 20
	}
	detail := wrapText(bodyContent, detailWidth)
	detail = confirmDetailStyle.Render(detail)

	// ── 选项列表 ──
	options := getConfirmOptions()
	const indicatorOn = "▶"
	const indicatorOff = "  "
	const stylePadding = 2 // Padding(0,1) = 左右各1 = 总共2字符

	var optionLines []string
	for i, opt := range options {
		// 步骤1：构建纯文本（无任何 ANSI 样式）
		indicator := indicatorOff
		if m.confirmDialog.selectedOption == i {
			indicator = indicatorOn
		}
		plainLabel := indicator + " " + opt.label

		// 步骤2：计算可用宽度
		shortcutPlain := opt.shortcut
		shortcutWidth := lipgloss.Width(shortcutPlain)
		// label 可用宽度 = innerWidth - shortcutWidth - 1(间距) - stylePadding
		maxPlainWidth := innerWidth - shortcutWidth - 1 - stylePadding
		if maxPlainWidth < 10 {
			maxPlainWidth = 10
		}

		// 步骤3：纯文本截断（在应用样式之前！）
		truncatedPlain := plainLabel
		if lipgloss.Width(plainLabel) > maxPlainWidth {
			runes := []rune(plainLabel)
			if maxPlainWidth > 1 {
				if len(runes) > maxPlainWidth-1 {
					truncatedPlain = string(runes[:maxPlainWidth-1]) + "…"
				} else {
					truncatedPlain = string(runes[:maxPlainWidth])
				}
			} else {
				truncatedPlain = string(runes[:maxPlainWidth])
			}
		}

		// 步骤4：应用样式（这是唯一一次渲染）
		var styledLabel string
		if m.confirmDialog.selectedOption == i {
			styledLabel = confirmOptionFocused.Render(truncatedPlain)
		} else {
			styledLabel = confirmOptionBlurred.Render(truncatedPlain)
		}

		// 步骤5：拼接 label 和 shortcut（不再用 Width/Align 约束 ANSI 字符串）
		line := lipgloss.JoinHorizontal(lipgloss.Left, styledLabel, shortcutPlain)
		optionLines = append(optionLines, line)
	}
	optionsBlock := lipgloss.JoinVertical(lipgloss.Left, optionLines...)

	// ── 帮助文字 ──
	help := confirmHelpStyle.Render(langManager.GetText("ConfirmDialogHelp"))

	// ── 分隔线 ──
	sep := lipgloss.NewStyle().
		Foreground(lipgloss.Color("237")).
		Width(innerWidth).
		Render(strings.Repeat("─", innerWidth))

	// ── 组装 ──
	content := lipgloss.JoinVertical(lipgloss.Left,
		toolLine,
		"",
		detail,
		"",
		sep,
		optionsBlock,
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
	titleLine := taskCompleteTitleStyle.Render(langManager.GetText("TaskCompleteTitle"))

	// ── OK Button ──
	okBtn := taskCompleteButtonFocused.Render(langManager.GetText("TaskCompleteOK"))

	// ── Help text ──
	help := confirmHelpStyle.Render(langManager.GetText("TaskCompleteHelp"))

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
	help := confirmHelpStyle.Render(langManager.GetText("ConfirmQuitHelp"))

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
	help := confirmHelpStyle.Render(langManager.GetText("ConfirmCancelHelp"))

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

	// ── Skills section (if any skills are loaded) ──
	var skillsSection string
	if m.assistant.SkillRegistry != nil && m.assistant.SkillRegistry.Count() > 0 {
		skillNames := m.assistant.SkillRegistry.List()
		skillsContent := "  Skills (type / in edit mode to select):\n"
		for _, name := range skillNames {
			if skill, ok := m.assistant.SkillRegistry.Get(name); ok && skill.Description != "" {
				skillsContent += fmt.Sprintf("    /%s  %s\n", name, skill.Description)
			} else {
				skillsContent += fmt.Sprintf("    /%s\n", name)
			}
		}
		skillsSection = skillsContent
	}

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
	var dialogContent string
	if skillsSection != "" {
		dialogContent = lipgloss.JoinVertical(lipgloss.Left,
			titleLine,
			"",
			content,
			"",
			contentStyle.Render(skillsSection),
			"",
			sep,
			"",
			hint,
		)
	} else {
		dialogContent = lipgloss.JoinVertical(lipgloss.Left,
			titleLine,
			"",
			content,
			"",
			sep,
			"",
			hint,
		)
	}

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

// confirmOption represents a single option in the authorization confirmation dialog.
type confirmOption struct {
	label    string // 显示文字
	shortcut string // 快捷键提示
	action   string // 响应动作
}

// 授权确认弹窗的5个选项 — 动态构建以支持国际化
func getConfirmOptions() []confirmOption {
	return []confirmOption{
		{label: langManager.GetText("ConfirmOptionAllow"), shortcut: langManager.GetText("ConfirmShortcutAllow"), action: "allow"},
		{label: langManager.GetText("ConfirmOptionAllowTool"), shortcut: langManager.GetText("ConfirmShortcutAllowTool"), action: "allow_session"},
		{label: langManager.GetText("ConfirmOptionAllowSession"), shortcut: langManager.GetText("ConfirmShortcutAllowSession"), action: "allow_all_session"},
		{label: langManager.GetText("ConfirmOptionAllowProject"), shortcut: langManager.GetText("ConfirmShortcutAllowProject"), action: "allow_all_project"},
		{label: langManager.GetText("ConfirmOptionDeny"), shortcut: langManager.GetText("ConfirmShortcutDeny"), action: "deny"},
	}
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

	// 选中行样式：醒目橙色背景 + 白色粗体文字
	confirmOptionFocused = lipgloss.NewStyle().
				Foreground(lipgloss.Color("0")).
				Background(lipgloss.Color("214")).
				Bold(true).
				Padding(0, 1)

	// 未选中行样式：灰色文字
	confirmOptionBlurred = lipgloss.NewStyle().
				Foreground(lipgloss.Color("244")).
				Padding(0, 1)

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
