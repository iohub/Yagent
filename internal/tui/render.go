package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
)

// ── Constants ──

const (
	MaxContentWidth = 120 // max width for content rendering
)

// skipBodyTools lists tools whose result body is just a status confirmation
// and should not be rendered — only the header (icon + name + file path) is shown.
var skipBodyTools = map[string]bool{
	"read_file":          true,
	"delete_file":        true,
	"rename_file":        true,
	"list_dir":           true,
	"print_dir_tree":     true,
	"search_by_regex":    true,
	"semantic_search":    true,
	"get_repo_overview":  true,  // 主视口仅显示 header，详情页通过 RenderResultBody 补充
}

// ── Tool Header Rendering ──

// RenderPending renders a running tool with animation.
// Output: "● tool_name · summary ████████~!@...."
func RenderPending(name string, summary string, anim *Anim) string {
	icon := IconPending
	toolName := RenderToolName(DisplayToolName(name))

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
	toolName := RenderToolName(DisplayToolName(name))

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
		toolName := RenderToolName(DisplayToolName(name))
		line := fmt.Sprintf("%s %s", icon, toolName)
		if params != "" {
			line += " " + ParamMain.Render(params)
		}
		line += " " + StateCanceled.Render("Canceled.")
		return line, true
	case ToolStatusPending:
		icon := IconPending
		toolName := RenderToolName(DisplayToolName(name))
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
	// For read_file and search_by_regex, skip borders as well; other tools keep borders.
	if skipBodyTools[entry.Call.Name] && entry.Status == ToolStatusSuccess {
		if entry.Call.Name == "read_file" || entry.Call.Name == "search_by_regex" || entry.Call.Name == "semantic_search" || entry.Call.Name == "get_repo_overview" {
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
		// Detect codexray tool results by JSON structure (not tool name)
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
		// If JSON contains "output" field but it's empty, return empty string
		// instead of pretty-printing a meaningless JSON
		if strings.Contains(content, `"output"`) {
			return ""
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

// tryFormatCodebaseResult detects codexray tool result JSON by structure and formats it.
// Returns empty string if the JSON doesn't match any known codexray result pattern.
func tryFormatCodebaseResult(content string, width int) string {
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return ""
	}

	data, hasData := parsed["data"].(map[string]interface{})
	if !hasData {
		return ""
	}

	// Pattern 1: semantic_search — data.results array with file_path/score
	if resultsRaw, ok := data["results"]; ok {
		if results, ok := resultsRaw.([]interface{}); ok && len(results) > 0 {
			// Verify it looks like semantic_search (has file_path or score)
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

		if score, ok := item["score"].(float64); ok {
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
	for _, cl := range codeLines {
		truncated := truncateLine(cl, width-2)
		lines = append(lines, "  "+codeLineStyle.Render(truncated))
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

	return strings.Join(lines, "\n")
}

// RenderDiffContent renders a unified diff string with ANSI color styling.
func RenderDiffContent(diffText string, maxWidth int) string {
	lines := strings.Split(diffText, "\n")
	var styledLines []string
	for _, line := range lines {
		if isDiffHeaderLine(line) {
			continue
		}
		styled := styleDiffLine(line, maxWidth)
		styled = Body.Render(styled)
		styledLines = append(styledLines, styled)
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
	var rendered []string
	for _, line := range lines {
		truncated := truncateLine(line, width-1)
		rendered = append(rendered, ContentLine.Render(truncated))
	}
	return strings.Join(rendered, "\n")
}

// renderCodeLines renders content as code with optional syntax context.
func renderCodeLines(content string, filename string, width int) string {
	lines := strings.Split(content, "\n")
	numWidth := len(fmt.Sprintf("%d", len(lines)))
	if numWidth < 2 {
		numWidth = 2
	}
	codeWidth := width - numWidth - 2
	if codeWidth < 20 {
		codeWidth = 20
	}
	numStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Faint(true)
	codeLineStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	var rendered []string
	for i, line := range lines {
		num := numStyle.Render(fmt.Sprintf("%*d", numWidth, i+1))
		code := codeLineStyle.Render(truncateLine(line, codeWidth))
		rendered = append(rendered, " "+num+"  "+code)
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
	case "semantic_search":
		if query, ok := args["query"].(string); ok && query != "" {
			if len(query) > 40 {
				return query[:37] + "..."
			}
			return query
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

// ── Agent Attribution Functions ──

// maxAgentTagLen limits agent name display length in compact tags.
const maxAgentTagLen = 12

// RenderAgentTag produces a compact, colored agent name string.
// Returns empty string if agent is empty.
// Visual: "Conductor" in agent's unique color, bold, no background.
func RenderAgentTag(agent string) string {
	if agent == "" {
		return ""
	}
	name := agent
	if len(name) > maxAgentTagLen {
		name = name[:maxAgentTagLen-1] + "…"
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(AgentColor(agent))).
		Bold(true).
		Render(name)
}

// RenderAgentBadge renders a compact agent indicator for non-ai_response entries.
// Unlike RenderAgentSeparator (full-width line), this is a single-line colored badge
// for light context signaling when tool entries switch to a new agent.
// Visual: "  ◈ AgentName"
func RenderAgentBadge(agent string) string {
	if agent == "" {
		return ""
	}
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(AgentColor(agent))).
		Bold(true).
		PaddingLeft(2).
		Render("◈ " + agent)
}

// RenderAgentSeparator produces a thin separator line with agent name
// for AI response blocks.
// Visual: "── Conductor ──────────────────────"
// If agent is empty, returns empty string.
func RenderAgentSeparator(agent string, width int) string {
	if agent == "" || width <= 0 {
		return ""
	}

	color := lipgloss.Color(AgentColor(agent))
	name := lipgloss.NewStyle().
		Foreground(color).
		Bold(true).
		Render(agent)

	lineStyle := lipgloss.NewStyle().
		Foreground(color).
		Faint(true)

	// Layout: "── Agent ──────"
	prefix := lineStyle.Render("── ")
	suffix := lineStyle.Render(" ──")
	nameLen := lipgloss.Width(name)
	prefixLen := lipgloss.Width(prefix)
	suffixLen := lipgloss.Width(suffix)

	dashes := width - prefixLen - nameLen - suffixLen
	if dashes < 0 {
		dashes = 0
	}
	middle := lineStyle.Render(strings.Repeat("─", dashes))

	return prefix + name + middle + suffix
}

// ── Timeline Rendering ──

// timelineNodeFor renders the node symbol + color for a TimelineEntry.
// DEPRECATED: Prefer timelineNodeForStatus for merged entries.
func timelineNodeFor(e *TimelineEntry) string {
	return timelineNodeForStatus(e, e.Status)
}

// timelineNodeForStatus renders the node symbol + color for a TimelineEntry
// using an explicitly-provided status. This is needed for merged entries where
// the effective (aggregated) status may differ from e.Status.
func timelineNodeForStatus(e *TimelineEntry, status ToolStatus) string {
	if e.Kind == TimelineKindLLMCall {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Bold(true).Render("◇") // blue diamond
	}
	if e.Kind == TimelineKindContextEvent {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Bold(true).Render("⊛") // magenta asterisk
	}
	// Tool call
	switch status {
	case ToolStatusRunning:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("228")).Bold(true).Render("●") // yellow
	case ToolStatusSuccess:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("114")).Bold(true).Render("●") // green
	case ToolStatusError:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("167")).Bold(true).Render("●") // red
	case ToolStatusPending:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Bold(true).Render("○") // gray
	case ToolStatusCanceled:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Bold(true).Render("●") // dim gray
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Bold(true).Render("○")
	}
}

// formatTimelineDuration formats a duration for timeline display.
func formatTimelineDuration(d time.Duration) string {
	if d == 0 {
		return ""
	}
	if d < time.Millisecond {
		return fmt.Sprintf("%dµs", d.Microseconds())
	}
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%.1fm", d.Minutes())
}

// RenderTimeline renders the full tool timeline panel.
// When expanded is false, only shows the currently running tool (or last completed tool).
// When expanded is true, shows up to maxTimelineEntries entries.
func RenderTimeline(entries []*TimelineEntry, expanded bool, width int, anim *Anim) string {
	const maxTimelineEntries = 20
	if width < 30 {
		width = 30
	}

	// Determine visible entries
	var visible []*TimelineEntry
	if len(entries) == 0 {
		// Idle state
		line := lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Render("○ idle")
		return addTimelineTopBorder(line, width)
	}

	if expanded {
		// Show up to maxTimelineEntries most recent
		start := 0
		if len(entries) > maxTimelineEntries {
			start = len(entries) - maxTimelineEntries
		}
		visible = entries[start:]
	} else {
		// 折叠模式：展示最近3条记录（优先包含运行中的工具）
		const defaultCollapsedCount = 3
		visible = selectCollapsedEntries(entries, defaultCollapsedCount)
	}

	// Build timeline lines
	var lines []string
	for i, e := range visible {
		isLast := i == len(visible)-1
		lines = append(lines, renderTimelineRow(e, width, anim))

		// Vertical connector between entries
		if !isLast && expanded {
			connector := timelineLineStyle.Render("│")
			lines = append(lines, " "+connector)
		}
	}

	content := strings.Join(lines, "\n")
	return addTimelineTopBorder(content, width)
}

// timeline visual styles
var (
	timelineLineStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	timelineBorderStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("237"))
	timelineNameStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true)
	timelineDetailStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	timelineDurationStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	timelineRunningStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("228")) // yellow for running
)

// addTimelineTopBorder wraps content with a thin top border line.
func addTimelineTopBorder(content string, width int) string {
	border := timelineBorderStyle.Render(strings.Repeat("─", width))
	return border + "\n" + content
}

// renderTimelineRow renders a single timeline entry row.
// Format: " ● read_file  /path/to/file          1.2s"
func renderTimelineRow(e *TimelineEntry, width int, anim *Anim) string {
	// Use EffectiveStatus() for merged entries
	effectiveStatus := e.EffectiveStatus()
	node := timelineNodeForStatus(e, effectiveStatus)

	// Name styling
	nameStr := e.Name
	if displayName := DisplayToolName(e.Name); displayName != e.Name {
		nameStr = displayName
	}

	// Show merged count for merged entries
	if e.MergedCount() > 1 {
		nameStr = fmt.Sprintf("%s ×%d", nameStr, e.MergedCount())
	}

	nameStyle := timelineNameStyle
	var name string
	if effectiveStatus == ToolStatusRunning {
		name = timelineRunningStyle.Render(nameStr)
	} else if e.IsError || effectiveStatus == ToolStatusError {
		name = lipgloss.NewStyle().Foreground(lipgloss.Color("167")).Render(nameStr)
	} else {
		name = nameStyle.Render(nameStr)
	}

	// Build animation string (for running tools)
	var animStr string
	if effectiveStatus == ToolStatusRunning {
		if anim != nil {
			animStr = " " + anim.Render()
		} else {
			animStr = " " + timelineRunningStyle.Render("running")
		}
	}

	// Build duration string (for completed tools only)
	var durStr string
	if e.Duration > 0 {
		dur := formatTimelineDuration(e.Duration)
		durStr = " " + timelineDurationStyle.Render(dur)
	}

	// Build base line: node + name + animation + detail
	leftPart := fmt.Sprintf(" %s %s", node, name)
	leftPart += animStr
	if e.Detail != "" {
		// Truncate detail to fit
		detailMax := 40
		detail := e.Detail
		if len(detail) > detailMax {
			detail = detail[:detailMax-1] + "…"
		}
		leftPart += " " + timelineDetailStyle.Render("· "+detail)
	}

	// Right-align duration (rightPart is purely duration, no animation)
	rightPart := durStr
	leftWidth := lipgloss.Width(leftPart)
	rightWidth := lipgloss.Width(rightPart)
	padding := width - leftWidth - rightWidth - 2 // -2 for safety margin
	if padding < 1 {
		padding = 1
	}

	return leftPart + strings.Repeat(" ", padding) + rightPart
}

// selectCollapsedEntries 选择折叠模式要显示的条目。
// 优先包含运行中的工具，然后用最近的非运行条目填满 maxCount。
func selectCollapsedEntries(entries []*TimelineEntry, maxCount int) []*TimelineEntry {
	n := len(entries)
	if n == 0 {
		return nil
	}
	if n <= maxCount {
		return entries
	}

	// 从末尾开始收集，优先包含运行中的工具
	selected := make([]bool, n)
	count := 0

	// 第一遍：从最近的开始，收集运行中的工具
	for i := n - 1; i >= 0 && count < maxCount; i-- {
		if entries[i].Kind == TimelineKindTool && entries[i].Status == ToolStatusRunning && !selected[i] {
			selected[i] = true
			count++
		}
	}

	// 第二遍：从最近的开始，填充到 maxCount
	for i := n - 1; i >= 0 && count < maxCount; i-- {
		if !selected[i] {
			selected[i] = true
			count++
		}
	}

	// 按原始顺序构建结果
	result := make([]*TimelineEntry, 0, count)
	for i := 0; i < n; i++ {
		if selected[i] {
			result = append(result, entries[i])
		}
	}

	return result
}

// renderTimelineRowWithCursor 渲染带光标和选中高亮的timeline行。
// 用于全屏模式的左侧列表，使用与非全屏一致的 dot+name+duration 样式。
func renderTimelineRowWithCursor(e *TimelineEntry, width int, anim *Anim, isCursor bool) string {
	// Use EffectiveStatus() to get the aggregated status (correct for merged entries)
	effectiveStatus := e.EffectiveStatus()
	node := timelineNodeForStatus(e, effectiveStatus)

	// Name styling
	nameStr := e.Name
	if displayName := DisplayToolName(e.Name); displayName != e.Name {
		nameStr = displayName
	}

	// Show merged count: "read_file ×3"
	if e.MergedCount() > 1 {
		nameStr = fmt.Sprintf("%s ×%d", nameStr, e.MergedCount())
	}

	nameStyle := timelineNameStyle
	var name string
	if effectiveStatus == ToolStatusRunning {
		name = timelineRunningStyle.Render(nameStr)
	} else if e.IsError || effectiveStatus == ToolStatusError {
		name = lipgloss.NewStyle().Foreground(lipgloss.Color("167")).Render(nameStr)
	} else {
		name = nameStyle.Render(nameStr)
	}

	// Build animation string (for running tools)
	var animStr string
	if effectiveStatus == ToolStatusRunning {
		if anim != nil {
			animStr = " " + anim.Render()
		} else {
			animStr = " " + timelineRunningStyle.Render("running")
		}
	}

	// Build duration string (for completed tools only)
	var durStr string
	if e.Duration > 0 {
		dur := formatTimelineDuration(e.Duration)
		durStr = " " + timelineDurationStyle.Render(dur)
	}

	// Build base line — fullscreen sidebar no longer shows detail summary
	leftPart := fmt.Sprintf(" %s %s", node, name)
	leftPart += animStr

	// Right-align duration
	rightPart := durStr
	leftWidth := lipgloss.Width(leftPart)
	rightWidth := lipgloss.Width(rightPart)
	padding := width - leftWidth - rightWidth - 4 // -4 for cursor(2) + margin(2)
	if padding < 1 {
		padding = 1
	}

	content := leftPart + strings.Repeat(" ", padding) + rightPart

	// 光标指示符
	var cursorStr string
	if isCursor {
		cursorStr = "▸ "
	} else {
		cursorStr = "  "
	}

	line := cursorStr + content

	// 选中行高亮（暗色背景）
	if isCursor {
		line = lipgloss.NewStyle().
			Background(lipgloss.Color("236")).
			Foreground(lipgloss.Color("255")).
			Width(width).
			Render(line)
	}

	return line
}
