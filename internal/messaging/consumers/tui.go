package messaging

import (
	"bufio"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"strconv"
	"strings"

	"yagent/internal/messaging"

	"charm.land/lipgloss/v2"
)

type TUIConsumer struct {
	writer           io.Writer
	reader           *bufio.Reader
	publisher        *messaging.MessagePublisher
	pendingToolCalls map[string]pendingToolCall // tool_call_id → pending entry
}

type pendingToolCall struct {
	toolName string
	summary  string
}

// Define tool-specific color styles
var toolStyles = map[string]lipgloss.Style{
	"planning":          lipgloss.NewStyle().Foreground(lipgloss.Color("#2496ED")), // blue
	"list_dir":          lipgloss.NewStyle().Foreground(lipgloss.Color("#7B42BC")), // purple
	"ask_user_for_help": lipgloss.NewStyle().Foreground(lipgloss.Color("#32CD32")), // lime green
	"edit_file":         lipgloss.NewStyle().Foreground(lipgloss.Color("#FF6B6B")), // red
	"read_file":         lipgloss.NewStyle().Foreground(lipgloss.Color("#4ECDC4")), // teal
	"run_bash":          lipgloss.NewStyle().Foreground(lipgloss.Color("#FFE66D")), // yellow
	"grep_search":       lipgloss.NewStyle().Foreground(lipgloss.Color("#1A535C")), // dark cyan
	"file_search":       lipgloss.NewStyle().Foreground(lipgloss.Color("#FF9F1C")), // coral
	"create_file":       lipgloss.NewStyle().Foreground(lipgloss.Color("#2ECC71")), // green
}

// displayToolNameAliases maps internal tool names to their display names.
// This is used to show user-friendly names without changing tool definitions.
var displayToolNameAliases = map[string]string{
	"search_replace_in_file": "edit_file",
}

// DisplayToolName returns the display name for a tool.
func DisplayToolName(name string) string {
	if displayName, ok := displayToolNameAliases[name]; ok {
		return displayName
	}
	return name
}

// Color palette for fallback colors
var colorPalette = []string{"#FF6B6B", "#4ECDC4", "#FFE66D", "#1A535C", "#FF9F1C", "#2ECC71", "#9B59B6", "#E74C3C", "#3498DB", "#27AE60"}

// Define detail text style (smaller font approximation)
var detailStyle = lipgloss.NewStyle().
	Foreground(lipgloss.Color("240")).
	Italic(true)

// Tool status styles for compact single-line display
var (
	toolRunningStyle2 = lipgloss.NewStyle().Foreground(lipgloss.Color("228")) // gold — running
	toolDoneStyle2    = lipgloss.NewStyle().Foreground(lipgloss.Color("114")) // green — done
	toolErrorStyle2   = lipgloss.NewStyle().Foreground(lipgloss.Color("167")) // red — error
	toolSummaryStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245")) // dim summary
)

// Additional styles for beautified UI
var (
	containerStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1).
			MarginTop(1)

	headerStyle = lipgloss.NewStyle().
			Bold(true)

	timestampStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("244")).
			Faint(true)

	labelStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("246")).
			Bold(true)

	aiPrefixStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("205")).
			Bold(true)

	statusPrefixStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("36")).
				Bold(true)

	toolPrefixStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("229")).
			Bold(true)

	contentStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			MarginTop(1)
)

// Get tool-specific style with fallback
func getToolStyle(toolName string) lipgloss.Style {
	if style, ok := toolStyles[toolName]; ok {
		return style
	}
	// Generate deterministic color for unknown tools
	h := fnv.New32a()
	h.Write([]byte(toolName))
	colorIndex := int(h.Sum32()) % len(colorPalette)
	return lipgloss.NewStyle().Foreground(lipgloss.Color(colorPalette[colorIndex]))
}

func NewTUIConsumer(writer io.Writer, publisher *messaging.MessagePublisher) *TUIConsumer {
	return &TUIConsumer{
		writer:           writer,
		reader:           bufio.NewReader(os.Stdin),
		publisher:        publisher,
		pendingToolCalls: make(map[string]pendingToolCall),
	}
}

// terminalWidth returns the current terminal width or a sensible default.
func terminalWidth() int {
	if colsStr := os.Getenv("COLUMNS"); colsStr != "" {
		if w, err := strconv.Atoi(colsStr); err == nil && w > 0 {
			return w
		}
	}
	return 100
}

// buildToolBadge builds a colored badge for tool names.
func buildToolBadge(toolName string) string {
	if toolName == "" {
		return ""
	}
	displayName := DisplayToolName(toolName)
	style := getToolStyle(displayName)
	badge := lipgloss.NewStyle().
		Background(style.GetForeground()).
		Foreground(lipgloss.Color("0")).
		Bold(true).
		Padding(0, 1)
	return badge.Render(displayName)
}

func (t *TUIConsumer) Consume(event *messaging.MessageEvent) error {
	// Handle interactive help specially
	switch event.Type {
	case "user_help_needed":
		t.showUserInputDialog(event)
		return nil
	}

	// For regular events, build a styled panel
	w := terminalWidth()
	contentStr := fmt.Sprintf("%v", event.Content)
	// header prefix and badge
	var prefixRendered string
	var toolName string
	var wrappedContent string

	switch event.Type {
	case "ai_response":
		prefixRendered = aiPrefixStyle.Render("🤖 AI")
		wrappedContent = contentStyle.Copy().Width(w - 6).Render(contentStr)
	case "status_update":
		prefixRendered = statusPrefixStyle.Render("ℹ️  Status")
		wrappedContent = contentStyle.Copy().Width(w - 6).Render(contentStr)
	case "ai_stream_start":
		// 流式开始：输出 agent 名称，不换行
		agentName := ""
		if contentMap, ok := event.Content.(map[string]interface{}); ok {
			if a, ok := contentMap["agent"].(string); ok {
				agentName = a
			}
		}
		if agentName != "" {
			fmt.Fprintf(t.writer, "\n%s ", aiPrefixStyle.Render("● "+agentName))
		} else {
			fmt.Fprint(t.writer, "\n● ")
		}
		return nil
	case "ai_chunk":
		// 流式内容：直接输出，不换行
		contentStr := ""
		if contentMap, ok := event.Content.(map[string]interface{}); ok {
			if c, ok := contentMap["content"].(string); ok {
				contentStr = c
			}
		}
		if contentStr != "" {
			fmt.Fprint(t.writer, contentStr)
		}
		return nil
	case "ai_stream_end":
		// 流式结束：换行
		fmt.Fprintln(t.writer)
		return nil
	case "tool_call":
		toolName = getToolNameFromContent(event.Content)
		prefixRendered = toolPrefixStyle.Render("🛠️  Tool") + " " + buildToolBadge(toolName)
		wrappedContent = contentStyle.Copy().Width(w - 6).Render(contentStr)
	case "tool_call_start":
		toolName = getToolNameFromContent(event.Content)
		callID := getToolCallIDFromContent(event.Content)
		argsJSON := getArgumentsFromContent(event.Content)
		summary := extractToolSummary(toolName, argsJSON)
		// Track pending call
		if callID != "" {
			t.pendingToolCalls[callID] = pendingToolCall{toolName: toolName, summary: summary}
		}
		// Compact running line: 🔘 display_tool_name · summary
		displayToolName := DisplayToolName(toolName)
		prefixRendered = toolRunningStyle2.Render("🔘 " + displayToolName)
		if summary != "" {
			prefixRendered += " " + toolSummaryStyle.Render("· "+summary)
		}
		wrappedContent = ""
	case "context_loaded":
		prefixRendered = statusPrefixStyle.Render("📄 项目上下文")
		// 解析加载的文件列表
		contentMap, ok := event.Content.(map[string]interface{})
		if !ok {
			wrappedContent = contentStyle.Copy().Width(w - 6).Render(contentStr)
		} else {
			// 获取加载的文件列表
			loadedFiles, ok := contentMap["loaded_files"].([]interface{})
			var fileNames []string
			if ok {
				for _, f := range loadedFiles {
					if fileMap, ok := f.(map[string]interface{}); ok {
						if fileName, ok := fileMap["file_name"].(string); ok {
							fileNames = append(fileNames, fileName)
						}
					}
				}
			}
			if len(fileNames) > 0 {
				// 显示加载的文件名
				wrappedContent = contentStyle.Copy().Width(w - 6).Render("✅ 已加载 " + strings.Join(fileNames, "、") + " 文件")
			} else {
				wrappedContent = contentStyle.Copy().Width(w - 6).Render("⚠️ 未找到项目上下文文件")
			}
		}
	case "tool_call_result":
		toolName = getToolNameFromContent(event.Content)
		callID := getToolCallIDFromContent(event.Content)
		// Look up pending call for summary
		var summary string
		if callID != "" {
			if pending, ok := t.pendingToolCalls[callID]; ok {
				summary = pending.summary
				delete(t.pendingToolCalls, callID)
			}
		}
		resultStr := getResultFromContent(event.Content)
		isError := strings.HasPrefix(resultStr, "Error:")

		// Compact done line: ✅ display_tool_name · summary  or  ❌ display_tool_name · summary — error
		displayToolName := DisplayToolName(toolName)
		if isError {
			prefixRendered = toolErrorStyle2.Render("❌ " + displayToolName)
		} else {
			prefixRendered = toolDoneStyle2.Render("✅ " + displayToolName)
		}
		if summary != "" {
			prefixRendered += " " + toolSummaryStyle.Render("· "+summary)
		}
		// Show brief error message
		if isError {
			errBrief := strings.TrimPrefix(resultStr, "Error: ")
			if len(errBrief) > 40 {
				errBrief = errBrief[:37] + "..."
			}
			prefixRendered += " " + toolErrorStyle2.Render("— "+errBrief)
		}

		// Check for diff content and render with ANSI colors
		diffText := extractDiffContent(event.Content)
		if diffText != "" {
			wrappedContent = renderDiffContent(diffText, w-6)
		} else {
			wrappedContent = ""
		}
	default:
		prefixRendered = labelStyle.Render("📝 " + string(event.Type))
		wrappedContent = contentStyle.Copy().Width(w - 6).Render(contentStr)
	}

	timestamp := timestampStyle.Render(event.Timestamp.Format("15:04:05"))
	if wrappedContent != "" {
		header := lipgloss.JoinHorizontal(lipgloss.Top, "[", timestamp, "] ", headerStyle.Render(prefixRendered))
		panel := lipgloss.JoinVertical(lipgloss.Left, header, wrappedContent)
		panel = containerStyle.Width(w - 2).Render(panel)
		_, err := fmt.Fprintln(t.writer, panel)
		return err
	}
	// Compact single-line output (no content body)
	line := lipgloss.JoinHorizontal(lipgloss.Top, "[", timestamp, "] ", prefixRendered)
	_, err := fmt.Fprintln(t.writer, line)
	return err
}

// Extract tool name from content (assuming content is a map with "name" field for tool calls)
func getToolNameFromContent(content interface{}) string {
	if contentMap, ok := content.(map[string]interface{}); ok {
		if name, ok := contentMap["tool_name"]; ok {
			if nameStr, ok := name.(string); ok {
				return nameStr
			}
		}
	}
	return ""
}

// getToolCallIDFromContent extracts the tool_call_id field.
func getToolCallIDFromContent(content interface{}) string {
	if m, ok := content.(map[string]interface{}); ok {
		if id, ok := m["tool_call_id"]; ok {
			if idStr, ok := id.(string); ok {
				return idStr
			}
		}
	}
	return ""
}

// getArgumentsFromContent extracts the arguments field.
func getArgumentsFromContent(content interface{}) string {
	if m, ok := content.(map[string]interface{}); ok {
		if args, ok := m["arguments"]; ok {
			if argsStr, ok := args.(string); ok {
				return argsStr
			}
		}
	}
	return ""
}

// getResultFromContent extracts the result field.
func getResultFromContent(content interface{}) string {
	if m, ok := content.(map[string]interface{}); ok {
		if result, ok := m["result"]; ok {
			if resultStr, ok := result.(string); ok {
				return resultStr
			}
		}
	}
	return ""
}

// extractToolSummary generates a short summary from tool arguments for display.
func extractToolSummary(toolName string, argsJSON string) string {
	if argsJSON == "" {
		return ""
	}
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}
	switch toolName {
	case "read_file", "edit_file", "search_replace_in_file", "create_file", "delete_file":
		// 键名顺序与 internal/tui/tui_render.go 的那份保持一致：read_file/create_file
		// 用 target_file，编辑类用 file_path —— 少一个键不报错，只是活动行空着。
		if fp, ok := args["target_file"].(string); ok && fp != "" {
			return fp
		}
		if fp, ok := args["file_path"].(string); ok && fp != "" {
			return fp
		}
		if fp, ok := args["path"].(string); ok && fp != "" {
			return fp
		}
	case "run_bash":
		if cmd, ok := args["command"].(string); ok && cmd != "" {
			if len(cmd) > 60 {
				return cmd[:57] + "..."
			}
			return cmd
		}
	case "search_by_regex", "grep_search":
		if pattern, ok := args["pattern"].(string); ok && pattern != "" {
			if len(pattern) > 40 {
				return pattern[:37] + "..."
			}
			return pattern
		}
	case "list_dir", "print_dir_tree":
		if path, ok := args["path"].(string); ok && path != "" {
			return path
		}
		if dir, ok := args["directory"].(string); ok && dir != "" {
			return dir
		}
	case "rename_file":
		from, _ := args["from"].(string)
		to, _ := args["to"].(string)
		if from != "" && to != "" {
			return from + " → " + to
		}
	case "load_tool":
		// load_tool 参数为 names（string 数组）：列出本次取回 schema 的工具名
		if raw, ok := args["names"].([]interface{}); ok && len(raw) > 0 {
			names := make([]string, 0, len(raw))
			for _, v := range raw {
				if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
					names = append(names, s)
				}
			}
			if len(names) > 0 {
				summary := fmt.Sprintf("加载 %d 个工具: %s", len(names), strings.Join(names, "、"))
				if len(summary) > 80 {
					return summary[:77] + "..."
				}
				return summary
			}
		}
	}
	if strings.HasPrefix(toolName, "delegate_") {
		if task, ok := args["task"].(string); ok && task != "" {
			if len(task) > 60 {
				return task[:57] + "..."
			}
			return task
		}
	}
	return ""
}

// showUserInputDialog displays a user input dialog and dispatches based on interaction_type
func (t *TUIConsumer) showUserInputDialog(event *messaging.MessageEvent) {
	content, ok := event.Content.(map[string]interface{})
	if !ok {
		return
	}

	question, _ := content["question"].(string)
	if question == "" {
		// fallback: use old format
		contentStr := fmt.Sprintf("%v", event.Content)
		question = contentStr
	}

	requestID, _ := content["request_id"].(string)
	interactionType, _ := content["interaction_type"].(string)

	// Parse options
	var options []string
	if opts, ok := content["options"].([]interface{}); ok {
		for _, opt := range opts {
			if s, ok := opt.(string); ok {
				options = append(options, s)
			}
		}
	}

	defaultValue, _ := content["default_value"].(string)

	switch interactionType {
	case "confirm":
		t.showCLIConfirm(question, options, requestID)
	case "select":
		t.showCLISelect(question, options, requestID)
	default:
		t.showCLIInput(question, defaultValue, requestID)
	}
}

// ── Help interaction design tokens ──
// Same family style as the TUI dialogs: rounded border (62), lavender
// accent (141), ◆ title, ○ option rows with right-aligned keycaps.

const helpPanelMaxWidth = 64

var (
	helpPanelStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("62")).Padding(0, 1)
	helpTitleStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Bold(true)
	helpQuestionStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	helpLabelStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("246"))
	helpCustomStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Italic(true)
	helpDimStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	helpKeyStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("247")).Bold(true)
	helpMarkerStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	helpErrorStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("167")).Italic(true)
	helpPromptStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Bold(true)
	helpOKStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("114")).Bold(true)
	helpSentStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)

// helpPanelWidth returns the outer panel width for CLI help dialogs.
func helpPanelWidth() int {
	w := terminalWidth() - 4
	if w > helpPanelMaxWidth {
		w = helpPanelMaxWidth
	}
	if w < 36 {
		w = 36
	}
	return w
}

// helpContentWidth returns the usable text width inside the panel
// (outer width minus 2 border cols and 2 padding cols).
func helpContentWidth(panelWidth int) int {
	w := panelWidth - 4
	if w < 10 {
		w = 10
	}
	return w
}

// helpHardWrap wraps text to the given display width. Latin words are kept
// whole when possible; CJK runes (and over-long words) break per character.
// Blank lines are preserved.
func helpHardWrap(s string, width int) []string {
	if width < 1 {
		width = 1
	}
	var lines []string
	for _, para := range strings.Split(s, "\n") {
		if para == "" {
			lines = append(lines, "")
			continue
		}
		var line strings.Builder
		lineW := 0
		flush := func() {
			lines = append(lines, strings.TrimRight(line.String(), " "))
			line.Reset()
			lineW = 0
		}
		for _, tok := range helpWrapTokens(para) {
			tokW := lipgloss.Width(tok)
			switch {
			case strings.Trim(tok, " ") == "":
				if lineW == 0 {
					continue // drop leading spaces after a break
				}
				if lineW+tokW > width {
					flush()
					continue
				}
			case tokW > width:
				// single token wider than the line: break it per rune
				for _, r := range tok {
					rw := lipgloss.Width(string(r))
					if lineW > 0 && lineW+rw > width {
						flush()
					}
					line.WriteRune(r)
					lineW += rw
				}
				continue
			default:
				if lineW+tokW > width {
					flush()
				}
			}
			line.WriteString(tok)
			lineW += tokW
		}
		if line.Len() > 0 {
			flush()
		}
	}
	return lines
}

// helpWrapTokens splits a paragraph into wrappable tokens: space runs,
// single wide (CJK) runes, and runs of narrow non-space runes (latin words).
func helpWrapTokens(para string) []string {
	runes := []rune(para)
	var tokens []string
	i := 0
	for i < len(runes) {
		r := runes[i]
		switch {
		case r == ' ':
			j := i
			for j < len(runes) && runes[j] == ' ' {
				j++
			}
			tokens = append(tokens, string(runes[i:j]))
			i = j
		case lipgloss.Width(string(r)) > 1:
			tokens = append(tokens, string(r))
			i++
		default:
			j := i
			for j < len(runes) && runes[j] != ' ' && lipgloss.Width(string(runes[j])) == 1 {
				j++
			}
			tokens = append(tokens, string(runes[i:j]))
			i = j
		}
	}
	return tokens
}

// helpPanel renders a rounded-border panel: "◆ title", blank line, body rows.
// body lines are assumed to be pre-wrapped to helpContentWidth(width).
// lipgloss v2 Width() is border-box: it includes padding and border.
func helpPanel(title string, body []string, width int) string {
	content := helpTitleStyle.Render("◆ " + title)
	if len(body) > 0 {
		content += "\n\n" + strings.Join(body, "\n")
	}
	return helpPanelStyle.Width(width).Render(content)
}

// helpQuestionBody hard-wraps and styles the question text.
func helpQuestionBody(question string, contentW int) []string {
	wrapped := helpHardWrap(question, contentW)
	body := make([]string, 0, len(wrapped))
	for _, line := range wrapped {
		body = append(body, helpQuestionStyle.Render(line))
	}
	return body
}

// helpOptionRow renders " ○ label …… key" padded to contentW.
func helpOptionRow(label, key string, contentW int, labelStyle lipgloss.Style) string {
	maxLabel := contentW - lipgloss.Width(key) - 6
	if maxLabel < 4 {
		maxLabel = 4
	}
	renderedLabel := labelStyle.Render(helpTruncate(label, maxLabel))
	marker := helpMarkerStyle.Render("○")
	left := " " + marker + " " + renderedLabel + " "
	gap := contentW - lipgloss.Width(left) - lipgloss.Width(key)
	if gap < 0 {
		gap = 0
	}
	return left + strings.Repeat(" ", gap) + helpKeyStyle.Render(key)
}

// helpTruncate shortens s to maxW display columns, appending "…" when cut.
func helpTruncate(s string, maxW int) string {
	if maxW < 1 || lipgloss.Width(s) <= maxW {
		return s
	}
	var b strings.Builder
	w := 0
	for _, r := range s {
		rw := lipgloss.Width(string(r))
		if w+rw > maxW-1 {
			break
		}
		b.WriteRune(r)
		w += rw
	}
	return b.String() + "…"
}

// helpPrompt renders the input prompt line below the panel.
func helpPrompt(label string) string {
	return "  " + helpPromptStyle.Render("›") + " " + helpDimStyle.Render(label)
}

// publishHelpResponse publishes a user response to the help response topic
func (t *TUIConsumer) publishHelpResponse(response string, requestID string) {
	if t.publisher != nil {
		content := map[string]interface{}{
			"response":   response,
			"request_id": requestID,
		}
		t.publisher.Publish("user_help_response", content, "User")
		fmt.Fprintln(t.writer, "  "+helpOKStyle.Render("✓")+" "+helpSentStyle.Render("已发送回复，等待任务继续..."))
	}
}

// showCLIInput handles free-form text input with optional default value
func (t *TUIConsumer) showCLIInput(question string, defaultValue string, requestID string) {
	width := helpPanelWidth()
	contentW := helpContentWidth(width)

	body := helpQuestionBody(question, contentW)
	if defaultValue != "" {
		body = append(body, "", helpDimStyle.Render("ℹ 默认: "+defaultValue))
	}
	fmt.Fprintln(t.writer, "\n"+helpPanel("Input", body, width))
	fmt.Fprint(t.writer, helpPrompt("请输入: "))

	input, err := t.reader.ReadString('\n')
	if err != nil {
		return
	}
	input = strings.TrimSpace(input)
	if input == "" && defaultValue != "" {
		input = defaultValue
	}

	t.publishHelpResponse(input, requestID)
}

// showCLIConfirm handles Yes/No confirmation prompts
func (t *TUIConsumer) showCLIConfirm(question string, options []string, requestID string) {
	yesLabel := "Yes"
	noLabel := "No"
	if len(options) >= 2 {
		yesLabel = options[0]
		noLabel = options[1]
	}

	width := helpPanelWidth()
	contentW := helpContentWidth(width)
	errMsg := ""

	for {
		body := helpQuestionBody(question, contentW)
		body = append(body, "",
			helpOptionRow(yesLabel, "Y", contentW, helpLabelStyle),
			helpOptionRow(noLabel, "N", contentW, helpLabelStyle),
		)
		fmt.Fprintln(t.writer, "\n"+helpPanel("Confirm", body, width))
		if errMsg != "" {
			fmt.Fprintln(t.writer, "  "+helpErrorStyle.Render("✗ "+errMsg))
			errMsg = ""
		}
		fmt.Fprint(t.writer, helpPrompt("请选择 (Y/N): "))

		input, err := t.reader.ReadString('\n')
		if err != nil {
			return
		}
		input = strings.TrimSpace(strings.ToLower(input))

		switch input {
		case "y", "yes":
			t.publishHelpResponse(yesLabel, requestID)
			return
		case "n", "no":
			t.publishHelpResponse(noLabel, requestID)
			return
		default:
			errMsg = "无效输入，请输入 Y 或 N"
		}
	}
}

// showCLISelect handles selection from a list of options with custom input support
func (t *TUIConsumer) showCLISelect(question string, options []string, requestID string) {
	width := helpPanelWidth()
	contentW := helpContentWidth(width)
	customIdx := len(options) + 1
	errMsg := ""

	for {
		body := helpQuestionBody(question, contentW)
		body = append(body, "")
		for i, opt := range options {
			body = append(body, helpOptionRow(opt, strconv.Itoa(i+1), contentW, helpLabelStyle))
		}
		body = append(body, helpOptionRow("✎ Custom input…", strconv.Itoa(customIdx), contentW, helpCustomStyle))
		fmt.Fprintln(t.writer, "\n"+helpPanel("Choose", body, width))
		if errMsg != "" {
			fmt.Fprintln(t.writer, "  "+helpErrorStyle.Render("✗ "+errMsg))
			errMsg = ""
		}
		fmt.Fprint(t.writer, helpPrompt(fmt.Sprintf("请输入序号 (1-%d): ", customIdx)))

		input, err := t.reader.ReadString('\n')
		if err != nil {
			return
		}
		input = strings.TrimSpace(input)

		num, err := strconv.Atoi(input)
		if err != nil || num < 1 || num > customIdx {
			errMsg = "无效序号，请重新输入"
			continue
		}

		if num == customIdx {
			fmt.Fprint(t.writer, helpPrompt("请输入自定义内容: "))
			custom, err := t.reader.ReadString('\n')
			if err != nil {
				return
			}
			custom = strings.TrimSpace(custom)
			if custom != "" {
				t.publishHelpResponse(custom, requestID)
				return
			}
			errMsg = "内容为空，请重新输入"
		} else {
			t.publishHelpResponse(options[num-1], requestID)
			return
		}
	}
}

// extractDiffContent extracts the "diff" field from a tool_call_result event content.
func extractDiffContent(content interface{}) string {
	m, ok := content.(map[string]interface{})
	if !ok {
		return ""
	}
	result, ok := m["result"].(string)
	if !ok || !strings.Contains(result, `"diff"`) {
		return ""
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		return ""
	}
	if diff, ok := parsed["diff"].(string); ok {
		return diff
	}
	return ""
}

// renderDiffContent renders a unified diff string with ANSI color styling for terminal output.
func renderDiffContent(diffText string, maxWidth int) string {
	// Diff color styles
	addStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
	delStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("167"))
	hunkStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	ctxStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	lines := strings.Split(diffText, "\n")
	var styledLines []string
	for _, line := range lines {
		if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			continue
		}
		var styled string
		switch {
		case strings.HasPrefix(line, "@@"):
			styled = hunkStyle.Render(truncateLine(line, maxWidth))
		case strings.HasPrefix(line, "+"):
			styled = addStyle.Render(truncateLine(line, maxWidth))
		case strings.HasPrefix(line, "-"):
			styled = delStyle.Render(truncateLine(line, maxWidth))
		case strings.HasPrefix(line, `\`):
			styled = ctxStyle.Render(truncateLine(line, maxWidth))
		default:
			styled = ctxStyle.Render(truncateLine(line, maxWidth))
		}
		styledLines = append(styledLines, styled)
	}
	return strings.Join(styledLines, "\n")
}

// truncateLine truncates a line to fit within maxWidth.
func truncateLine(line string, maxWidth int) string {
	if maxWidth <= 0 {
		return line
	}
	runes := []rune(line)
	if len(runes) <= maxWidth {
		return line
	}
	return string(runes[:maxWidth-1]) + "…"
}
