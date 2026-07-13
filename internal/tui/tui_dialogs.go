package tui

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"codeactor/internal/messaging"
	"codeactor/internal/protocol"
	"codeactor/internal/tui/components"

	tea "charm.land/bubbletea/v2"
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

	d := components.NewConfirmDialog(m.com.Styles, toolName, command, warning, requestID, components.Language(m.currentLang))
	d.SetBounds(m.termWidth, m.termHeight)
	m.dialogStack.Push(d)
}

// respondToAuth publishes the user response and closes the dialog.
func (m *model) respondToAuth(response string) {
	if m.publisher == nil {
		return
	}

	var requestID string
	if dlg, ok := m.dialogStack.Top().(*components.ConfirmDialog); ok {
		requestID = dlg.GetRequestID()
		m.dialogStack.Pop()
	} else if m.dialogStack.CloseDialog("confirm_dialog") {
		requestID = ""
	}

	m.publisher.Publish("user_help_response", map[string]interface{}{
		"response":   response,
		"request_id": requestID,
	}, "User")

	m.logEntries = append(m.logEntries, logEntry{
		timestamp: time.Now(),
		eventType: "status",
		content:   fmt.Sprintf("Auth response: %s", response),
	})
	m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
}

// openUserHelpDialog 解析 user_help_needed 事件并打开 UserHelpDialog
func (m *model) openUserHelpDialog(event *messaging.MessageEvent) {
	content, ok := event.Content.(map[string]interface{})
	if !ok {
		slog.Error("openUserHelpDialog: content type assertion failed",
			"actual_type", fmt.Sprintf("%T", event.Content))
		// Fallback: try to use the event content as a string for question
		question := fmt.Sprintf("%v", event.Content)
		contextStr := ""
		requestID := ""
		m.openConfirmDialogFallback(question, contextStr, requestID)
		return
	}

	// 解析字段
	question, _ := content["question"].(string)
	if question == "" {
		slog.Error("openUserHelpDialog: question field is empty",
			"content_keys", getMapKeys(content))
		// Fallback: use context as question if available, otherwise use a default
		if ctx, ok := content["context"].(string); ok && ctx != "" {
			question = ctx
		} else {
			question = "User help requested"
		}
	}

	contextStr, _ := content["context"].(string)
	requestID, _ := content["request_id"].(string)

	// 解析交互类型（可选，为空时 UserHelpDialog 会自动推断）
	var interactionType protocol.InteractionType
	if it, ok := content["interaction_type"].(string); ok {
		interactionType = protocol.InteractionType(it)
	}

	// 解析选项列表
	var options []string
	if opts, ok := content["options"].([]interface{}); ok {
		for _, opt := range opts {
			if s, ok := opt.(string); ok {
				options = append(options, s)
			}
		}
	}

	// 解析可选字段
	defaultValue, _ := content["default_value"].(string)
	placeholder, _ := content["placeholder"].(string)
	allowCustom := true
	if ac, ok := content["allow_custom"].(bool); ok {
		allowCustom = ac
	}

	// 构造 UserHelpNeededData
	data := protocol.UserHelpNeededData{
		Question:        question,
		Context:         contextStr,
		InteractionType: interactionType,
		Options:         options,
		DefaultValue:    defaultValue,
		Placeholder:     placeholder,
		AllowCustom:     allowCustom,
		RequestID:       requestID,
	}

	// 创建并推入对话框
	d := components.NewUserHelpDialog(data)
	m.dialogStack.Push(d)
}

// respondToUserHelp 发布用户帮助响应并关闭对话框
func (m *model) respondToUserHelp(result *protocol.UserHelpResponseData) {
	if m.publisher == nil {
		return
	}

	// 发布响应事件
	m.publisher.Publish("user_help_response", map[string]interface{}{
		"response":         result.Response,
		"interaction_type": string(result.InteractionType),
		"is_custom":        result.IsCustom,
		"cancelled":        result.Cancelled,
		"request_id":       result.RequestID,
	}, "User")

	// 记录日志
	statusStr := "responded"
	if result.Cancelled {
		statusStr = "cancelled"
	}
	m.logEntries = append(m.logEntries, logEntry{
		timestamp: time.Now(),
		eventType: "status",
		content:   fmt.Sprintf("User help response (%s): %s", statusStr, result.Response),
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
func parseConfirmQuestion(question string) (toolName, body string) {
	q := strings.TrimSpace(question)
	q = strings.ReplaceAll(q, "**", "")

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

	parts := strings.SplitN(q, "\n\n", 2)
	if len(parts) >= 2 {
		body = parts[1]
	} else {
		body = parts[0]
	}

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

// getMapKeys returns the keys of a map for logging purposes
func getMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// openConfirmDialogFallback is a fallback when UserHelpDialog cannot be opened normally
func (m *model) openConfirmDialogFallback(question, contextStr, requestID string) {
	d := components.NewUserHelpDialog(protocol.UserHelpNeededData{
		Question:        question,
		Context:         contextStr,
		InteractionType: protocol.InteractionConfirm,
		Options:         []string{"Yes", "No"},
		AllowCustom:     true,
		RequestID:       requestID,
	})
	m.dialogStack.Push(d)
}
