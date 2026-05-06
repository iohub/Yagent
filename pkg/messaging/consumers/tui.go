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

	"codeactor/pkg/messaging"

	"github.com/charmbracelet/lipgloss"
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
	"investigate_repo":  lipgloss.NewStyle().Foreground(lipgloss.Color("#FFA500")), // orange
	"planning":          lipgloss.NewStyle().Foreground(lipgloss.Color("#2496ED")), // blue
	"list_dir":          lipgloss.NewStyle().Foreground(lipgloss.Color("#7B42BC")), // purple
	"ask_user_for_help": lipgloss.NewStyle().Foreground(lipgloss.Color("#32CD32")), // lime green
	"edit_file":         lipgloss.NewStyle().Foreground(lipgloss.Color("#FF6B6B")), // red
	"read_file":         lipgloss.NewStyle().Foreground(lipgloss.Color("#4ECDC4")), // teal
	"run_bash":  lipgloss.NewStyle().Foreground(lipgloss.Color("#FFE66D")), // yellow
	"grep_search":       lipgloss.NewStyle().Foreground(lipgloss.Color("#1A535C")), // dark cyan
	"file_search":       lipgloss.NewStyle().Foreground(lipgloss.Color("#FF9F1C")), // coral
	"create_file":       lipgloss.NewStyle().Foreground(lipgloss.Color("#2ECC71")), // green
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

	chunkPrefixStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("244")).
				Faint(true)

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
	style := getToolStyle(toolName)
	badge := lipgloss.NewStyle().
		Background(style.GetForeground()).
		Foreground(lipgloss.Color("0")).
		Bold(true).
		Padding(0, 1)
	return badge.Render(toolName)
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
		prefixRendered = aiPrefixStyle.Render("🚀 AI Stream Started")
		wrappedContent = contentStyle.Copy().Width(w - 6).Render(contentStr)
	case "ai_chunk":
		prefixRendered = chunkPrefixStyle.Render("💬 AI Chunk")
		wrappedContent = contentStyle.Copy().Width(w - 6).Render(contentStr)
	case "ai_stream_end":
		prefixRendered = aiPrefixStyle.Render("🏁 AI Stream Ended")
		wrappedContent = contentStyle.Copy().Width(w - 6).Render(contentStr)
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
		// Compact running line: 🔘 tool_name · summary
		prefixRendered = toolRunningStyle2.Render("🔘 " + toolName)
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

		// Compact done line: ✅ tool_name · summary  or  ❌ tool_name · summary — error
		if isError {
			prefixRendered = toolErrorStyle2.Render("❌ " + toolName)
		} else {
			prefixRendered = toolDoneStyle2.Render("✅ " + toolName)
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
		prefixRendered = labelStyle.Render("📝 " + event.Type)
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

// showUserInputDialog displays a styled input dialog similar to the image reference
func (t *TUIConsumer) showUserInputDialog(event *messaging.MessageEvent) {
	w := terminalWidth()

	// Parse content to get help details
	contentStr := fmt.Sprintf("%v", event.Content)

	askStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FFA500")).
		Bold(true).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#FFA500")).
		Padding(0, 1).
		Width(w - 4)

	askMsg := askStyle.Render("✨ Agent ask your help")

	// Create help message
	helpStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FFFFFF")).
		MarginTop(1).
		MarginBottom(1)

	helpMsg := helpStyle.Render("● " + contentStr)

	// Display the styled interface
	fmt.Fprintln(t.writer, "")
	fmt.Fprintln(t.writer, askMsg)
	fmt.Fprintln(t.writer, helpMsg)

	// Wait for user input
	fmt.Fprint(t.writer, "\n> ")
	userInput, err := t.reader.ReadString('\n')
	if err != nil {
		fmt.Fprintf(t.writer, "Error reading input: %v\n", err)
		return
	}

	userInput = strings.TrimSpace(userInput)

	if strings.ToLower(userInput) == "cancel" {
		fmt.Fprintf(t.writer, "已取消用户帮助请求。\n")
		return
	}

	// Publish user response
	if t.publisher != nil {
		var taskID interface{}
		var requestID interface{}
		if event.Metadata != nil {
			taskID = event.Metadata["task_id"]
			requestID = event.Metadata["request_id"]
		}
		// Fallback: try to fetch from content map if provided there
		if m, ok := event.Content.(map[string]interface{}); ok {
			if taskID == nil {
				taskID = m["task_id"]
			}
			if requestID == nil {
				requestID = m["request_id"]
			}
		}

		responseContent := map[string]interface{}{
			"response": userInput,
		}
		if taskIDStr, ok := taskID.(string); ok && taskIDStr != "" {
			responseContent["task_id"] = taskIDStr
		}
		if requestIDStr, ok := requestID.(string); ok && requestIDStr != "" {
			responseContent["request_id"] = requestIDStr
		}
		t.publisher.Publish("user_help_response", responseContent, "User")
	}

	fmt.Fprintf(t.writer, "已发送回复，等待任务继续...\n")
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
	headerStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true)
	ctxStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	lines := strings.Split(diffText, "\n")
	var styledLines []string
	for _, line := range lines {
		var styled string
		switch {
		case strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ "):
			styled = headerStyle.Render(truncateLine(line, maxWidth))
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
