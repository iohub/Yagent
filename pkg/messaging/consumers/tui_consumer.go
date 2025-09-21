package messaging

import (
	"bufio"
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
	"run_terminal_cmd":  lipgloss.NewStyle().Foreground(lipgloss.Color("#FFE66D")), // yellow
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
		contentStr := fmt.Sprintf("%v", event.Content)
		fmt.Fprintf(t.writer, "\n[需要用户帮助] %s\n", contentStr)
		fmt.Fprintf(t.writer, "请输入您的回复（输入 'cancel' 取消）: ")

		userInput, err := t.reader.ReadString('\n')
		if err != nil {
			return err
		}
		userInput = strings.TrimSpace(userInput)

		if strings.ToLower(userInput) == "cancel" {
			fmt.Fprintf(t.writer, "已取消用户帮助请求。\n")
			return nil
		}

		if t.publisher != nil {
			var taskID interface{}
			if event.Metadata != nil {
				taskID = event.Metadata["task_id"]
			}
			// Fallback: try to fetch from content map if provided there
			if taskID == nil {
				if m, ok := event.Content.(map[string]interface{}); ok {
					taskID = m["task_id"]
				}
			}
			if taskIDStr, ok := taskID.(string); ok && taskIDStr != "" {
				t.publisher.Publish("user_help_response", map[string]interface{}{
					"task_id":  taskIDStr,
					"response": userInput,
				})
			} else {
				// Publish without task_id to avoid panic; upstream may ignore
				t.publisher.Publish("user_help_response", map[string]interface{}{
					"response": userInput,
				})
			}
		}
		fmt.Fprintf(t.writer, "已发送回复，等待任务继续...\n")
		return nil
	}

	// For regular events, build a styled panel
	w := terminalWidth()
	contentStr := fmt.Sprintf("%v", event.Content)
	wrappedContent := contentStyle.Copy().Width(w - 6).Render(contentStr)
	// header prefix and badge
	var prefixRendered string
	var toolName string

	switch event.Type {
	case "ai_response":
		prefixRendered = aiPrefixStyle.Render("🤖 AI")
	case "status_update":
		prefixRendered = statusPrefixStyle.Render("ℹ️  Status")
	case "ai_stream_start":
		prefixRendered = aiPrefixStyle.Render("🚀 AI Stream Started")
	case "ai_chunk":
		prefixRendered = chunkPrefixStyle.Render("💬 AI Chunk")
	case "ai_stream_end":
		prefixRendered = aiPrefixStyle.Render("🏁 AI Stream Ended")
	case "tool_call":
		toolName = getToolNameFromContent(event.Content)
		prefixRendered = toolPrefixStyle.Render("🛠️  Tool") + " " + buildToolBadge(toolName)
	case "tool_call_start":
		toolName = getToolNameFromContent(event.Content)
		prefixRendered = toolPrefixStyle.Render("▶️  Tool Start") + " " + buildToolBadge(toolName)
	case "tool_call_result":
		toolName = getToolNameFromContent(event.Content)
		prefixRendered = toolPrefixStyle.Render("⏹️  Tool Result") + " " + buildToolBadge(toolName)
	default:
		prefixRendered = labelStyle.Render("📝 " + event.Type)
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
	fmt.Println("getToolNameFromContent", content)
	if contentMap, ok := content.(map[string]interface{}); ok {
		if name, ok := contentMap["tool_name"]; ok {
			if nameStr, ok := name.(string); ok {
				return nameStr
			}
		}
	}
	return ""
}
