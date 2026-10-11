package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
)

const (
	MaxContentWidth = 120
)

var skipBodyTools = map[string]bool{
	"read_file":       true,
	"delete_file":     true,
	"rename_file":     true,
	"list_dir":        true,
	"print_dir_tree":  true,
	"search_by_regex": true,
	"semantic_search": true,
}

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

func RenderToolLine(entry *ToolEntry, anim *Anim, width int) string {
	params := entry.Call.Summary
	if params == "" {
		params = formatToolParams(entry.Call.Name, entry.Call.Arguments)
	}

	if early, ok := RenderEarlyState(entry.Status, entry.Call.Name, params); ok {
		return early
	}

	var errBrief string
	if entry.Status == ToolStatusError && entry.Result != nil {
		errBrief = formatErrorBrief(entry.Result.Content)
	}

	header := RenderHeader(entry.Status, entry.Call.Name, params, errBrief)

	if entry.Status == ToolStatusRunning {
		return header
	}

	if skipBodyTools[entry.Call.Name] && entry.Status == ToolStatusSuccess {
		if entry.Call.Name == "read_file" || entry.Call.Name == "search_by_regex" || entry.Call.Name == "semantic_search" {
			return header
		}
		return addToolCallBorders(header, width)
	}

	if entry.Result != nil && entry.Result.Content != "" {
		body := RenderResultBody(entry.Call.Name, entry.Result.Content, width)
		if body != "" {
			return addToolCallBorders(header, width) + "\n" + body
		}
	}

	return addToolCallBorders(header, width)
}

func addToolCallBorders(header string, width int) string {
	topBorder := ToolCallBorderTop.Render(makeBorderLine('─', width))
	bottomBorder := ToolCallBorderBottom.Render(makeBorderLine('─', width))
	return topBorder + "\n" + header + "\n" + bottomBorder
}

func RenderResultBody(toolName string, content string, width int) string {
	bodyWidth := width - 4
	if bodyWidth > MaxContentWidth {
		bodyWidth = MaxContentWidth
	}
	if bodyWidth < 30 {
		bodyWidth = 30
	}

	if decodedStr := tryDecodeJSONString(content); decodedStr != "" {
		return RenderResultBody(toolName, decodedStr, bodyWidth)
	}

	if isJSON(content) {
		if formatted := tryFormatCodebaseResult(content, bodyWidth); formatted != "" {
			return formatted
		}
		if diff := extractDiffField(content); diff != "" {
			return RenderDiffContent(diff, bodyWidth)
		}
		if output := extractOutputField(content); output != "" {
			return renderPlainContent(output, bodyWidth)
		}
		if strings.Contains(content, `"output"`) {
			return ""
		}
		pretty, err := jsonPrettyPrint(content)
		if err == nil {
			if looksLikeMarkdown(pretty) {
				return renderMarkdownContent(pretty, bodyWidth)
			}
			return renderCodeLines(pretty, "result.json", bodyWidth)
		}
	}

	if isUnifiedDiff(content) {
		return RenderDiffContent(content, bodyWidth)
	}

	if looksLikeMarkdown(content) {
		return renderMarkdownContent(content, bodyWidth)
	}

	return renderPlainContent(content, bodyWidth)
}

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

func tryDecodeJSONString(content string) string {
	trimmed := strings.TrimSpace(content)
	if len(trimmed) >= 2 && trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"' {
		var decoded string
		if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
			return decoded
		}
	}
	return ""
}

func tryFormatCodebaseResult(content string, width int) string {
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return ""
	}

	if funcInfo, ok := parsed["function"].(map[string]interface{}); ok {
		if _, hasName := funcInfo["function_name"]; hasName {
			if callees, ok := parsed["callees"].([]interface{}); ok && len(callees) > 0 {
				return formatFunctionRelationResults(funcInfo, "callee", callees, width)
			}
			if callers, ok := parsed["callers"].([]interface{}); ok && len(callers) > 0 {
				return formatFunctionRelationResults(funcInfo, "caller", callers, width)
			}
		}
	}

	data, hasData := parsed["data"].(map[string]interface{})
	if !hasData {
		return ""
	}

	if resultsRaw, ok := data["results"]; ok {
		if results, ok := resultsRaw.([]interface{}); ok && len(results) > 0 {
			if first, ok := results[0].(map[string]interface{}); ok {
				if _, hasPath := first["file_path"]; hasPath {
					return formatSemanticSearchResults(results, width)
				}
			}
		}
	}

	if snippet, ok := data["code_snippet"].(string); ok && snippet != "" {
		filepath, _ := data["filepath"].(string)
		funcName, _ := data["function_name"].(string)
		lineStart, _ := data["line_start"].(float64)
		lineEnd, _ := data["line_end"].(float64)
		language, _ := data["language"].(string)
		return formatCodeSnippetResult(filepath, funcName, snippet, int(lineStart), int(lineEnd), language, width)
	}

	if skelsRaw, ok := data["skeletons"]; ok {
		if skels, ok := skelsRaw.([]interface{}); ok && len(skels) > 0 {
			return formatCodeSkeletonResults(skels, width)
		}
	}

	return ""
}

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

		if score, ok := item["final_score"].(float64); ok {
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

func formatCodeSnippetResult(filepath, funcName, snippet string, lineStart, lineEnd int, language string, width int) string {
	fileStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	funcStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	langStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Faint(true)
	locStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Faint(true)
	codeLineStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))

	var lines []string

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

	codeLines := strings.Split(snippet, "\n")
	for _, cl := range codeLines {
		truncated := truncateLine(cl, width-2)
		lines = append(lines, "  "+codeLineStyle.Render(truncated))
	}

	return strings.Join(lines, "\n")
}

func formatFunctionRelationResults(funcInfo map[string]interface{}, relationType string, relations []interface{}, width int) string {
	fileStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Bold(true)
	funcStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	langStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Faint(true)
	locStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Faint(true)
	relStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Bold(true)
	arrowStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Faint(true)

	var lines []string

	filepath, _ := funcInfo["file_path"].(string)
	funcName, _ := funcInfo["function_name"].(string)
	language, _ := funcInfo["language"].(string)
	lineStart, _ := funcInfo["line_start"].(float64)
	lineEnd, _ := funcInfo["line_end"].(float64)

	headerParts := []string{
		fileStyle.Render(filepath),
		"  ",
		funcStyle.Render(funcName),
	}
	if language != "" {
		headerParts = append(headerParts, "  ", langStyle.Render(language))
	}
	if int(lineStart) > 0 {
		lineRange := fmt.Sprintf("L%d-%d", int(lineStart), int(lineEnd))
		headerParts = append(headerParts, "  ", locStyle.Render(lineRange))
	}
	lines = append(lines, strings.Join(headerParts, ""))

	var arrow string
	if relationType == "callee" {
		arrow = "→"
	} else {
		arrow = "←"
	}

	for _, rel := range relations {
		item, ok := rel.(map[string]interface{})
		if !ok {
			continue
		}

		relFilepath, _ := item["file_path"].(string)
		relFuncName, _ := item["function_name"].(string)

		var relParts []string
		relParts = append(relParts, "  ", arrowStyle.Render(arrow), " ")

		relPath := relFilepath
		if len(relPath) > 40 {
			relPath = relPath[len(relPath)-38:]
		}
		colonIdx := strings.Index(relPath, ":")
		if colonIdx > 0 {
			filePart := relPath[:colonIdx]
			linePart := relPath[colonIdx+1:]
			relParts = append(relParts,
				fileStyle.Render(filePart),
				arrowStyle.Render(":"+linePart),
				"  ",
			)
		} else {
			relParts = append(relParts, fileStyle.Render(relPath), "  ")
		}

		funcDisplay := relFuncName
		if itemSig, ok := item["signature"].(string); ok && itemSig != "" {
			funcDisplay = itemSig
		}
		if funcDisplay != "" && !strings.Contains(funcDisplay, "(") {
			funcDisplay += "()"
		}
		relParts = append(relParts, relStyle.Render(funcDisplay))

		lines = append(lines, strings.Join(relParts, ""))
	}

	return strings.Join(lines, "\n")
}

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

func renderPlainContent(content string, width int) string {
	lines := strings.Split(content, "\n")
	var rendered []string
	for _, line := range lines {
		truncated := truncateLine(line, width-1)
		rendered = append(rendered, ContentLine.Render(truncated))
	}
	return strings.Join(rendered, "\n")
}

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

var (
	glamourRendererCache sync.Map
)

func renderMarkdownContent(content string, width int) string {
	if width <= 0 {
		width = 80
	}

	if cached, ok := glamourRendererCache.Load(width); ok {
		if r, ok := cached.(*glamour.TermRenderer); ok {
			rendered, err := r.Render(content)
			if err == nil {
				return rendered
			}
		}
	}

	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		r, _ = glamour.NewTermRenderer(
			glamour.WithStandardStyle("dark"),
			glamour.WithWordWrap(80),
		)
	}
	if r == nil {
		return renderCodeLines(content, "result.md", width)
	}

	glamourRendererCache.Store(width, r)

	rendered, err := r.Render(content)
	if err != nil {
		return renderCodeLines(content, "result.md", width)
	}
	return rendered
}

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
	case "find_function_callee", "find_function_caller":
		if fn, ok := args["function_name"].(string); ok && fn != "" {
			if len(fn) > 40 {
				return fn[:37] + "..."
			}
			return fn
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

const maxAgentTagLen = 12

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

func timelineNodeFor(e *TimelineEntry) string {
	return timelineNodeForStatus(e, e.Status)
}

func timelineNodeForStatus(e *TimelineEntry, status ToolStatus) string {
	if e.Kind == TimelineKindLLMCall {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("75")).Bold(true).Render("◇")
	}
	if e.Kind == TimelineKindThinking {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Bold(true).Render("💭")
	}
	if e.Kind == TimelineKindContextEvent {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Bold(true).Render("⊛")
	}
	switch status {
	case ToolStatusRunning:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("228")).Bold(true).Render("●")
	case ToolStatusSuccess:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("114")).Bold(true).Render("●")
	case ToolStatusError:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("167")).Bold(true).Render("●")
	case ToolStatusPending:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Bold(true).Render("○")
	case ToolStatusCanceled:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Bold(true).Render("●")
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Bold(true).Render("○")
	}
}

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

func RenderTimeline(entries []*TimelineEntry, expanded bool, showDetail bool, width int, anim *Anim) string {
	const maxTimelineEntries = 20
	if width < 30 {
		width = 30
	}

	var visible []*TimelineEntry
	if len(entries) == 0 {
		line := lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Render("○ idle")
		return addTimelineTopBorder(line, width)
	}

	if expanded {
		start := 0
		if len(entries) > maxTimelineEntries {
			start = len(entries) - maxTimelineEntries
		}
		visible = entries[start:]
	} else {
		const defaultCollapsedCount = 3
		visible = selectCollapsedEntries(entries, defaultCollapsedCount)
	}

	var lines []string
	for i, e := range visible {
		isLast := i == len(visible)-1
		lines = append(lines, renderTimelineRow(e, width, showDetail, anim))

		if !isLast && expanded {
			connector := timelineLineStyle.Render("│")
			lines = append(lines, " "+connector)
		}
	}

	content := strings.Join(lines, "\n")
	return addTimelineTopBorder(content, width)
}

var (
	timelineLineStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	timelineBorderStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("237"))
	timelineNameStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Bold(true)
	timelineDetailStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	timelineDurationStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	timelineRunningStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("228"))
)

func addTimelineTopBorder(content string, width int) string {
	border := timelineBorderStyle.Render(strings.Repeat("─", width))
	return border + "\n" + content
}

func renderTimelineRow(e *TimelineEntry, width int, showDetail bool, anim *Anim) string {
	effectiveStatus := e.EffectiveStatus()
	node := timelineNodeForStatus(e, effectiveStatus)

	nameStr := e.Name
	if displayName := DisplayToolName(e.Name); displayName != e.Name {
		nameStr = displayName
	}

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

	var animStr string
	if effectiveStatus == ToolStatusRunning {
		if anim != nil {
			animStr = " " + anim.Render()
		} else {
			animStr = " " + timelineRunningStyle.Render("running")
		}
	}

	var durStr string
	if e.Duration > 0 {
		dur := formatTimelineDuration(e.Duration)
		durStr = " " + timelineDurationStyle.Render(dur)
	}

	leftPart := fmt.Sprintf(" %s %s", node, name)
	leftPart += animStr
	if showDetail && e.Detail != "" {
		detailMax := 40
		detail := e.Detail
		if len(detail) > detailMax {
			detail = detail[:detailMax-1] + "…"
		}
		leftPart += " " + timelineDetailStyle.Render("· "+detail)
	}

	rightPart := durStr
	leftWidth := lipgloss.Width(leftPart)
	rightWidth := lipgloss.Width(rightPart)
	padding := width - leftWidth - rightWidth - 2
	if padding < 1 {
		padding = 1
	}

	return leftPart + strings.Repeat(" ", padding) + rightPart
}

func selectCollapsedEntries(entries []*TimelineEntry, maxCount int) []*TimelineEntry {
	n := len(entries)
	if n == 0 {
		return nil
	}
	if n <= maxCount {
		return entries
	}

	selected := make([]bool, n)
	count := 0

	for i := n - 1; i >= 0 && count < maxCount; i-- {
		if entries[i].Kind == TimelineKindTool && entries[i].Status == ToolStatusRunning && !selected[i] {
			selected[i] = true
			count++
		}
	}

	for i := n - 1; i >= 0 && count < maxCount; i-- {
		if !selected[i] {
			selected[i] = true
			count++
		}
	}

	result := make([]*TimelineEntry, 0, count)
	for i := 0; i < n; i++ {
		if selected[i] {
			result = append(result, entries[i])
		}
	}

	return result
}

func renderTimelineRowWithCursor(e *TimelineEntry, width int, anim *Anim, isCursor bool) string {
	effectiveStatus := e.EffectiveStatus()
	node := timelineNodeForStatus(e, effectiveStatus)

	nameStr := e.Name
	if displayName := DisplayToolName(e.Name); displayName != e.Name {
		nameStr = displayName
	}

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

	var animStr string
	if effectiveStatus == ToolStatusRunning {
		if anim != nil {
			animStr = " " + anim.Render()
		} else {
			animStr = " " + timelineRunningStyle.Render("running")
		}
	}

	var durStr string
	if e.Duration > 0 {
		dur := formatTimelineDuration(e.Duration)
		durStr = " " + timelineDurationStyle.Render(dur)
	}

	leftPart := fmt.Sprintf(" %s %s", node, name)
	leftPart += animStr

	rightPart := durStr
	leftWidth := lipgloss.Width(leftPart)
	rightWidth := lipgloss.Width(rightPart)
	padding := width - leftWidth - rightWidth - 4
	if padding < 1 {
		padding = 1
	}

	content := leftPart + strings.Repeat(" ", padding) + rightPart

	var cursorStr string
	if isCursor {
		cursorStr = "▸ "
	} else {
		cursorStr = "  "
	}

	line := cursorStr + content

	if isCursor {
		line = lipgloss.NewStyle().
			Background(lipgloss.Color("236")).
			Foreground(lipgloss.Color("255")).
			Width(width).
			Render(line)
	}

	return line
}
