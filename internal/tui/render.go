package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ── Constants ──

const (
	MaxBodyLines    = 10  // max lines shown when collapsed
	MaxContentWidth = 120 // max width for content rendering
)

// skipBodyTools lists tools whose result body is just a status confirmation
// and should not be rendered — only the header (icon + name + file path) is shown.
var skipBodyTools = map[string]bool{
	"read_file":       true,
	"delete_file":     true,
	"rename_file":     true,
	"list_dir":        true,
	"search_by_regex": true,
}

// ── Tool Header Rendering ──

// RenderPending renders a running tool with animation.
// Output: "● tool_name · summary ████████~!@...."
func RenderPending(name string, summary string, anim *Anim) string {
	icon := IconPending
	toolName := RenderToolName(name)

	var parts []string
	parts = append(parts, icon, toolName)

	if summary != "" {
		parts = append(parts, " ", ParamMain.Render(summary))
	}

	parts = append(parts, " ", anim.Render())

	return strings.Join(parts, "")
}

// RenderHeader renders the tool header line with icon, name, and params.
// Output: "✓ tool_name · file_path" or "× tool_name · file_path — error_msg"
func RenderHeader(status ToolStatus, name string, params string, errBrief string) string {
	icon := ToolIcon(status, false)
	toolName := RenderToolName(name)

	var parts []string
	parts = append(parts, icon, toolName)

	if params != "" {
		parts = append(parts, " ", ParamMain.Render(params))
	}

	if status == ToolStatusError && errBrief != "" {
		parts = append(parts, " ", ErrorMessage.Render("— "+errBrief))
	}

	return strings.Join(parts, "")
}

// RenderEarlyState renders early termination states (canceled, waiting).
func RenderEarlyState(status ToolStatus, name string, params string) (string, bool) {
	switch status {
	case ToolStatusCanceled:
		icon := IconCanceled
		toolName := RenderToolName(name)
		line := fmt.Sprintf("%s %s", icon, toolName)
		if params != "" {
			line += " " + ParamMain.Render(params)
		}
		line += " " + StateCanceled.Render("Canceled.")
		return line, true
	case ToolStatusPending:
		icon := IconPending
		toolName := RenderToolName(name)
		line := fmt.Sprintf("%s %s", icon, toolName)
		if params != "" {
			line += " " + ParamMain.Render(params)
		}
		line += " " + StateWaiting.Render("Waiting for tool response...")
		return line, true
	default:
		return "", false
	}
}

// ── Tool Body Rendering ──

// RenderToolLine renders the complete tool display: header + optional body.
// This is the main entry point for rendering a tool entry.
func RenderToolLine(entry *ToolEntry, anim *Anim, width int) string {
	params := entry.Call.Summary
	if params == "" {
		params = formatToolParams(entry.Call.Name, entry.Call.Arguments)
	}

	// Check for early states first
	if early, ok := RenderEarlyState(entry.Status, entry.Call.Name, params); ok {
		return early
	}

	var errBrief string
	if entry.Status == ToolStatusError && entry.Result != nil {
		errBrief = formatErrorBrief(entry.Result.Content)
	}

	header := RenderHeader(entry.Status, entry.Call.Name, params, errBrief)

	// If still running, this should have been handled by RenderPending — fallback
	if entry.Status == ToolStatusRunning {
		return header
	}

	// Tools whose result body is just status JSON — skip body rendering,
	// only show the tool name + file path in the header.
	if skipBodyTools[entry.Call.Name] && entry.Status == ToolStatusSuccess {
		return header
	}

	// Render body if we have a result
	if entry.Result != nil && entry.Result.Content != "" {
		body := RenderResultBody(entry.Result.Content, width)
		if body != "" {
			return header + "\n" + body
		}
	}

	return header
}

// RenderResultBody renders the tool result content with smart detection.
func RenderResultBody(content string, width int) string {
	// Determine content width
	bodyWidth := width - 4 // account for padding
	if bodyWidth > MaxContentWidth {
		bodyWidth = MaxContentWidth
	}
	if bodyWidth < 30 {
		bodyWidth = 30
	}

	// 1. Try JSON — check for embedded fields first
	if isJSON(content) {
		// Check if JSON contains a "diff" field — extract and render as colored diff
		if diff := extractDiffField(content); diff != "" {
			return RenderDiffContent(diff, bodyWidth)
		}
		// Check if JSON contains an "output" field — extract and render as plain text
		if output := extractOutputField(content); output != "" {
			return renderPlainContent(output, bodyWidth)
		}
		// Otherwise pretty-print the JSON
		pretty, err := jsonPrettyPrint(content)
		if err == nil {
			return renderCodeLines(pretty, "result.json", bodyWidth)
		}
	}

	// 2. Try unified diff
	if isUnifiedDiff(content) {
		return RenderDiffContent(content, bodyWidth)
	}

	// 3. Try markdown detection
	if looksLikeMarkdown(content) {
		return renderCodeLines(content, "result.md", bodyWidth)
	}

	// 4. Fallback: plain text
	return renderPlainContent(content, bodyWidth)
}

// extractDiffField tries to extract a "diff" field from a JSON result string.
func extractDiffField(jsonStr string) string {
	if !strings.Contains(jsonStr, `"diff"`) {
		return ""
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return ""
	}
	if diff, ok := parsed["diff"].(string); ok && diff != "" {
		return diff
	}
	return ""
}

// extractOutputField tries to extract an "output" field from a JSON result string.
func extractOutputField(jsonStr string) string {
	if !strings.Contains(jsonStr, `"output"`) {
		return ""
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return ""
	}
	if output, ok := parsed["output"].(string); ok && output != "" {
		return output
	}
	return ""
}

// RenderDiffContent renders a unified diff string with ANSI color styling.
func RenderDiffContent(diffText string, maxWidth int) string {
	lines := strings.Split(diffText, "\n")

	// Truncate to max visible lines
	visibleLines := lines
	truncated := false
	if len(lines) > MaxBodyLines {
		visibleLines = lines[:MaxBodyLines]
		truncated = true
	}

	var styledLines []string
	for _, line := range visibleLines {
		styled := styleDiffLine(line, maxWidth)
		styled = Body.Render(styled)
		styledLines = append(styledLines, styled)
	}

	if truncated {
		hidden := len(lines) - MaxBodyLines
		msg := fmt.Sprintf("... (%d lines hidden)", hidden)
		styledLines = append(styledLines, ContentTrunc.Render(msg))
	}

	return strings.Join(styledLines, "\n")
}

// styleDiffLine applies color styling to a single diff line.
func styleDiffLine(line string, maxWidth int) string {
	truncated := truncateLine(line, maxWidth)
	switch {
	case strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ "):
		return DiffHeader.Render(truncated)
	case strings.HasPrefix(line, "@@"):
		return DiffHunk.Render(truncated)
	case strings.HasPrefix(line, "+"):
		return DiffAdd.Render(truncated)
	case strings.HasPrefix(line, "-"):
		return DiffDel.Render(truncated)
	case strings.HasPrefix(line, `\`):
		return DiffNoNewline.Render(truncated)
	default:
		return DiffCtx.Render(truncated)
	}
}

// ── Internal helpers ──

// renderPlainContent renders plain text output with line prefix and truncation.
func renderPlainContent(content string, width int) string {
	lines := strings.Split(content, "\n")

	truncated := false
	visibleLines := lines
	if len(lines) > MaxBodyLines {
		visibleLines = lines[:MaxBodyLines]
		truncated = true
	}

	var rendered []string
	for _, line := range visibleLines {
		truncated := truncateLine(line, width-1)
		rendered = append(rendered, ContentLine.Render(truncated))
	}

	if truncated {
		hidden := len(lines) - MaxBodyLines
		msg := fmt.Sprintf("... (%d lines hidden)", hidden)
		rendered = append(rendered, ContentTrunc.Render(msg))
	}

	return strings.Join(rendered, "\n")
}

// renderCodeLines renders content as code with optional syntax context.
func renderCodeLines(content string, filename string, width int) string {
	lines := strings.Split(content, "\n")

	truncated := false
	visibleLines := lines
	if len(lines) > MaxBodyLines {
		visibleLines = lines[:MaxBodyLines]
		truncated = true
	}

	// Line number width
	numWidth := len(fmt.Sprintf("%d", len(visibleLines)))
	if numWidth < 2 {
		numWidth = 2
	}
	codeWidth := width - numWidth - 2 // line number + margin + padding
	if codeWidth < 20 {
		codeWidth = 20
	}

	numStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Faint(true)
	codeLineStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))

	var rendered []string
	for i, line := range visibleLines {
		num := numStyle.Render(fmt.Sprintf("%*d", numWidth, i+1))
		code := codeLineStyle.Render(truncateLine(line, codeWidth))
		rendered = append(rendered, " "+num+"  "+code)
	}

	if truncated {
		hidden := len(lines) - MaxBodyLines
		msg := fmt.Sprintf("... (%d lines hidden)", hidden)
		rendered = append(rendered, ContentTrunc.Render(msg))
	}

	return strings.Join(rendered, "\n")
}

// ── Content Detection ──

func isJSON(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) == 0 {
		return false
	}
	if s[0] == '{' || s[0] == '[' {
		var v interface{}
		return json.Unmarshal([]byte(s), &v) == nil
	}
	return false
}

func isUnifiedDiff(s string) bool {
	return strings.Contains(s, "--- a/") && strings.Contains(s, "+++ b/") ||
		strings.Contains(s, "--- /") && strings.Contains(s, "+++ /") ||
		strings.Contains(s, "diff --git ")
}

func looksLikeMarkdown(s string) bool {
	indicators := []string{"# ", "## ", "**", "```", "- ", "1. ", "> ", "---", "***"}
	for _, ind := range indicators {
		if strings.Contains(s, ind) {
			return true
		}
	}
	return false
}

// ── Formatting Helpers ──

func jsonPrettyPrint(jsonStr string) (string, error) {
	var v interface{}
	if err := json.Unmarshal([]byte(jsonStr), &v); err != nil {
		return "", err
	}
	pretty, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(pretty), nil
}

func formatToolParams(toolName string, argsJSON string) string {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}
	switch toolName {
	case "read_file", "edit_file", "search_replace_in_file", "create_file", "delete_file":
		if fp, ok := args["file_path"].(string); ok && fp != "" {
			return fp
		}
		if fp, ok := args["target_file"].(string); ok && fp != "" {
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
		if pattern, ok := args["query"].(string); ok && pattern != "" {
			if len(pattern) > 40 {
				return pattern[:37] + "..."
			}
			return pattern
		}
		if pattern, ok := args["pattern"].(string); ok && pattern != "" {
			if len(pattern) > 40 {
				return pattern[:37] + "..."
			}
			return pattern
		}
	case "list_dir", "print_dir_tree":
		if path, ok := args["dir_path"].(string); ok && path != "" {
			return path
		}
		if path, ok := args["absolute_path"].(string); ok && path != "" {
			return path
		}
	case "rename_file":
		from, _ := args["file_path"].(string)
		to, _ := args["rename_file_path"].(string)
		if from != "" && to != "" {
			return from + " → " + to
		}
	case "file_search":
		if q, ok := args["query"].(string); ok && q != "" {
			return q
		}
	}
	// For delegate tools
	if strings.HasPrefix(toolName, "delegate_") {
		if task, ok := args["task"].(string); ok && task != "" {
			if len(task) > 60 {
				return task[:57] + "..."
			}
			return task
		}
	}
	// For other tools, show first value or empty
	return ""
}

func formatErrorBrief(result string) string {
	errMsg := strings.TrimPrefix(result, "Error: ")
	errMsg = strings.TrimSpace(errMsg)
	if len(errMsg) > 50 {
		return errMsg[:47] + "..."
	}
	return errMsg
}

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
