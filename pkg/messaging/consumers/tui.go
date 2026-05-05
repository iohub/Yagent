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
	writer    io.Writer
	reader    *bufio.Reader
	publisher *messaging.MessagePublisher
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
		writer:    writer,
		reader:    bufio.NewReader(os.Stdin),
		publisher: publisher,
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
		prefixRendered = toolPrefixStyle.Render("▶️  Tool Start") + " " + buildToolBadge(toolName)
		wrappedContent = contentStyle.Copy().Width(w - 6).Render(contentStr)
	case "tool_call_result":
		toolName = getToolNameFromContent(event.Content)
		prefixRendered = toolPrefixStyle.Render("⏹️  Tool Result") + " " + buildToolBadge(toolName)
		// Check for diff content and render with ANSI colors
		diffText := extractDiffContent(event.Content)
		if diffText != "" {
			wrappedContent = renderDiffContent(diffText, w-6)
		} else {
			wrappedContent = contentStyle.Copy().Width(w - 6).Render(contentStr)
		}
	default:
		prefixRendered = labelStyle.Render("📝 " + event.Type)
		wrappedContent = contentStyle.Copy().Width(w - 6).Render(contentStr)
	}

	timestamp := timestampStyle.Render(event.Timestamp.Format("15:04:05"))
	header := lipgloss.JoinHorizontal(lipgloss.Top, "[", timestamp, "] ", headerStyle.Render(prefixRendered))
	panel := lipgloss.JoinVertical(lipgloss.Left, header, wrappedContent)
	panel = containerStyle.Width(w - 2).Render(panel)

	_, err := fmt.Fprintln(t.writer, panel)
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
