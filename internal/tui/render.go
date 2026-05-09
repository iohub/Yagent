package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
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

	// Check for early states first — no borders
	if early, ok := RenderEarlyState(entry.Status, entry.Call.Name, params); ok {
		return early
	}

	var errBrief string
	if entry.Status == ToolStatusError && entry.Result != nil {
		errBrief = formatErrorBrief(entry.Result.Content)
	}

	header := RenderHeader(entry.Status, entry.Call.Name, params, errBrief)

	// If still running, no borders — animation is in progress
	if entry.Status == ToolStatusRunning {
		return header
	}

	// Tools whose result body is just status JSON — skip body rendering,
	// only show the tool name + file path in the header.
	// For read_file, skip borders as well; other tools keep borders.
	if skipBodyTools[entry.Call.Name] && entry.Status == ToolStatusSuccess {
		if entry.Call.Name == "read_file" {
			return header
		}
		return addToolCallBorders(header, width)
	}

	// Render body if we have a result
	if entry.Result != nil && entry.Result.Content != "" {
		body := RenderResultBody(entry.Call.Name, entry.Result.Content, width)
		if body != "" {
			// Border only wraps header; body goes below the border
			return addToolCallBorders(header, width) + "\n" + body
		}
	}

	return addToolCallBorders(header, width)
}

// addToolCallBorders wraps header with a thin top and bottom border line.
// Body content should be appended OUTSIDE the border.
func addToolCallBorders(header string, width int) string {
	topBorder := ToolCallBorderTop.Render(makeBorderLine('─', width))
	bottomBorder := ToolCallBorderBottom.Render(makeBorderLine('─', width))
	return topBorder + "\n" + header + "\n" + bottomBorder
}

// RenderResultBody renders the tool result content with smart detection.
func RenderResultBody(toolName string, content string, width int) string {
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
		// Detect codebase tool results by JSON structure (not tool name)
		if formatted := tryFormatCodebaseResult(content, bodyWidth); formatted != "" {
			return formatted
		}
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

// tryFormatCodebaseResult detects codebase tool result JSON by structure and formats it.
// Returns empty string if the JSON doesn't match any known codebase result pattern.
func tryFormatCodebaseResult(content string, width int) string {
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return ""
	}

	data, hasData := parsed["data"].(map[string]interface{})
	if !hasData {
		return ""
	}

	// Pattern 1: semantic_search — data.results array with file_path/semantic_distance
	if resultsRaw, ok := data["results"]; ok {
		if results, ok := resultsRaw.([]interface{}); ok && len(results) > 0 {
			// Verify it looks like semantic_search (has file_path or semantic_distance)
			if first, ok := results[0].(map[string]interface{}); ok {
				if _, hasPath := first["file_path"]; hasPath {
					return formatSemanticSearchResults(results, width)
				}
			}
		}
	}

	// Pattern 2: query_code_snippet — data.code_snippet with filepath
	if snippet, ok := data["code_snippet"].(string); ok && snippet != "" {
		filepath, _ := data["filepath"].(string)
		funcName, _ := data["function_name"].(string)
		lineStart, _ := data["line_start"].(float64)
		lineEnd, _ := data["line_end"].(float64)
		language, _ := data["language"].(string)
		return formatCodeSnippetResult(filepath, funcName, snippet, int(lineStart), int(lineEnd), language, width)
	}

	// Pattern 3: query_code_skeleton — data.skeletons array
	if skelsRaw, ok := data["skeletons"]; ok {
		if skels, ok := skelsRaw.([]interface{}); ok && len(skels) > 0 {
			return formatCodeSkeletonResults(skels, width)
		}
	}

	return ""
}

// formatSemanticSearchResults formats semantic_search results array in a human-readable way.
func formatSemanticSearchResults(results []interface{}, width int) string {
	if len(results) == 0 {
		return ContentLine.Render("  (no results)")
	}

	idxStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Faint(true)
	fileStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	scoreStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	codeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	symbolStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Faint(true)

	maxResults := MaxBodyLines
	totalResults := len(results)
	if totalResults > maxResults {
		results = results[:maxResults]
	}

	var lines []string
	for i, r := range results {
		item, ok := r.(map[string]interface{})
		if !ok {
			continue
		}

		var headerParts []string
		num := idxStyle.Render(fmt.Sprintf("#%d", i+1))

		filePath := ""
		if fp, ok := item["file_path"].(string); ok {
			filePath = fp
		}
		maxPathLen := width - 25
		if maxPathLen < 20 {
			maxPathLen = 20
		}
		if len(filePath) > maxPathLen {
			filePath = "..." + filePath[len(filePath)-maxPathLen+3:]
		}
		filePart := fileStyle.Render(filePath)
		headerParts = append(headerParts, num, " ", filePart)

		if score, ok := item["semantic_distance"].(float64); ok {
			headerParts = append(headerParts, "  ", scoreStyle.Render(fmt.Sprintf("(score: %.3f)", score)))
		}

		if sym, ok := item["symbol_name"].(string); ok && sym != "" && !strings.HasPrefix(sym, "anon-") {
			headerParts = append(headerParts, "  ", symbolStyle.Render(sym))
		}

		lines = append(lines, strings.Join(headerParts, ""))

		if cb, ok := item["code_block"].(string); ok && cb != "" {
			firstLine := strings.SplitN(cb, "\n", 2)[0]
			firstLine = strings.TrimSpace(firstLine)
			maxCodeLen := width - 6
			if maxCodeLen < 30 {
				maxCodeLen = 30
			}
			if len(firstLine) > maxCodeLen {
				firstLine = firstLine[:maxCodeLen-1] + "…"
			}
			lines = append(lines, "  "+codeStyle.Render("  "+firstLine))
		}
	}

	if totalResults > maxResults {
		hidden := totalResults - maxResults
		lines = append(lines, ContentTrunc.Render(fmt.Sprintf("... (%d more results hidden)", hidden)))
	}

	return strings.Join(lines, "\n")
}

// formatCodeSnippetResult formats a query_code_snippet result in a human-readable way.
func formatCodeSnippetResult(filepath, funcName, snippet string, lineStart, lineEnd int, language string, width int) string {
	fileStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	funcStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	langStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Faint(true)
	locStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Faint(true)
	codeLineStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))

	var lines []string

	// Header: filepath  funcName  (language)  L{start}-{end}
	headerParts := []string{
		fileStyle.Render(filepath),
		"  ",
		funcStyle.Render(funcName),
	}
	if language != "" {
		headerParts = append(headerParts, "  ", langStyle.Render(language))
	}
	if lineStart > 0 {
		lineRange := fmt.Sprintf("L%d-%d", lineStart, lineEnd)
		headerParts = append(headerParts, "  ", locStyle.Render(lineRange))
	}
	lines = append(lines, strings.Join(headerParts, ""))

	// Code content — show each line with prefix
	codeLines := strings.Split(snippet, "\n")
	maxLines := MaxBodyLines - 1 // reserve one line for header
	truncated := false
	if len(codeLines) > maxLines {
		codeLines = codeLines[:maxLines]
		truncated = true
	}
	for _, cl := range codeLines {
		truncated := truncateLine(cl, width-2)
		lines = append(lines, "  "+codeLineStyle.Render(truncated))
	}
	if truncated {
		hidden := len(strings.Split(snippet, "\n")) - maxLines
		lines = append(lines, ContentTrunc.Render(fmt.Sprintf("... (%d more lines hidden)", hidden)))
	}

	return strings.Join(lines, "\n")
}

// formatCodeSkeletonResults formats query_code_skeleton results array in a human-readable way.
func formatCodeSkeletonResults(skels []interface{}, width int) string {
	if len(skels) == 0 {
		return ContentLine.Render("  (no skeletons)")
	}

	idxStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Faint(true)
	fileStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	langStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Faint(true)
	codeStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))

	maxResults := MaxBodyLines
	totalResults := len(skels)
	if totalResults > maxResults {
		skels = skels[:maxResults]
	}

	var lines []string
	for i, s := range skels {
		item, ok := s.(map[string]interface{})
		if !ok {
			continue
		}

		num := idxStyle.Render(fmt.Sprintf("#%d", i+1))
		filepath, _ := item["filepath"].(string)
		language, _ := item["language"].(string)

		// Truncate filepath
		maxPathLen := width - 20
		if maxPathLen < 20 {
			maxPathLen = 20
		}
		if len(filepath) > maxPathLen {
			filepath = "..." + filepath[len(filepath)-maxPathLen+3:]
		}

		headerParts := []string{num, " ", fileStyle.Render(filepath)}
		if language != "" {
			headerParts = append(headerParts, "  ", langStyle.Render(language))
		}
		lines = append(lines, strings.Join(headerParts, ""))

		// Skeleton text
		if st, ok := item["skeleton_text"].(string); ok && st != "" {
			firstLine := strings.SplitN(st, "\n", 2)[0]
			firstLine = strings.TrimSpace(firstLine)
			maxCodeLen := width - 6
			if maxCodeLen < 30 {
				maxCodeLen = 30
			}
			if len(firstLine) > maxCodeLen {
				firstLine = firstLine[:maxCodeLen-1] + "…"
			}
			lines = append(lines, "  "+codeStyle.Render("  "+firstLine))
		}
	}

	if totalResults > maxResults {
		hidden := totalResults - maxResults
		lines = append(lines, ContentTrunc.Render(fmt.Sprintf("... (%d more skeletons hidden)", hidden)))
	}

	return strings.Join(lines, "\n")
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
		if isDiffHeaderLine(line) {
			continue
		}
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

// makeBorderLine generates a compact border line using half-block characters.
func makeBorderLine(char rune, width int) string {
	runes := make([]rune, width)
	for i := range runes {
		runes[i] = char
	}
	return string(runes)
}

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

// isDiffHeaderLine returns true if the line is a diff file path header ("--- a/" or "+++ b/").
func isDiffHeaderLine(line string) bool {
	return strings.HasPrefix(line, "--- a/") || strings.HasPrefix(line, "+++ b/") ||
		strings.HasPrefix(line, "--- /") || strings.HasPrefix(line, "+++ /")
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
