package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"yagent/internal/messaging"

	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
)

func (m *model) computeFooterHeight() int {
	if m.footerHeightValid {
		return m.cachedFooterHeight
	}

	height := 1

	if !m.dashboardVisible() && (m.taskRunning || len(m.timelineEntries) > 0) {
		height += 6
	}

	if !m.commandMode {
		height += 3 + m.computeInputHeight()
		height += 1
		if m.skillAutoComplete && len(m.skillSuggestions) > 0 {
			height += len(m.skillSuggestions) + 1
		}
		if m.keywordAutoComplete && len(m.keywordSuggestions) > 0 {
			height += 1
		}
	}

	if m.errMsg != "" {
		height += 1
	}

	totalTokens := m.inputTokens + m.outputTokens
	if !m.dashboardVisible() {
		if m.tokenDashboardCollapsed {
			if dash := m.renderTokenDashboard(); dash != "" {
				height += lipgloss.Height(dash)
			}
		} else if totalTokens > 0 {
			height += 4
			if m.commandMode && m.taskRunning {
				height++
			}
			for _, au := range m.tokenUsagePerAgent {
				if au.InputTokens+au.OutputTokens > 0 {
					height++
				}
			}
		}
	}

	if m.commandMode {
		height += 1
		height += 1
	} else {
		height += 2
		height += 1
	}

	m.cachedFooterHeight = height
	m.footerHeightValid = true
	return height
}

func (m *model) updateViewportCache() {
	m.cachedViewportView = m.viewport.View()
	m.viewportViewValid = true
	m.prevViewportYOffset = m.viewport.YOffset()
	m.prevViewportHeight = m.viewport.Height()
}

func (m *model) invalidateFooterCache() {
	m.footerHeightValid = false
	m.cachedSeparator = ""
	m.cachedStatusBar = ""
	m.statusBarValid = false
	m.cachedTokenDashboard = ""
	m.tokenDashboardValid = false
	m.dashboardCache = ""
	m.dashboardCacheKey = ""
}

func (m *model) getGlamourRenderer(width int) *glamour.TermRenderer {
	if width <= 0 {
		width = 80
	}
	if m.glamourRenderers == nil {
		m.glamourRenderers = make(map[int]*glamour.TermRenderer)
	}
	if r, ok := m.glamourRenderers[width]; ok {
		return r
	}
	glamourStyle := "dark"
	if !m.useDarkStyle {
		glamourStyle = "light"
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(glamourStyle),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		r, _ = glamour.NewTermRenderer(
			glamour.WithStandardStyle(glamourStyle),
			glamour.WithWordWrap(80),
		)
	}
	m.glamourRenderers[width] = r
	return r
}

func (m *model) resizeViewport() {
	footerHeight := m.cachedFooterHeight
	if !m.footerHeightValid {
		footerHeight = m.computeFooterHeight()
	}
	vpHeight := m.termHeight - footerHeight
	if vpHeight < 3 {
		vpHeight = 3
	}
	m.viewport.SetWidth(m.termWidth - m.dashboardWidth())
	m.viewport.SetHeight(vpHeight)

	if m.termWidth > 0 && m.viewport.Width() > 0 {
		frameSize := m.viewport.Style.GetHorizontalFrameSize()
		const glamourGutter = 4
		glamourWidth := m.viewport.Width() - frameSize - glamourGutter
		if glamourWidth < 40 {
			glamourWidth = 40
		}
		m.glamourRenderer = m.getGlamourRenderer(glamourWidth)
	}
	m.updateViewportCache()
}

func (m *model) invalidateRenderedCache() {
	for i := range m.logEntries {
		m.logEntries[i].clearRenderCache()
		if m.logEntries[i].toolEntry != nil {
			m.logEntries[i].toolEntry.InvalidateCache()
		}
	}
	m.glamourRenderers = nil
	m.markAllEntriesDirty()
}

func (m *model) buildViewportContent() {
	if m.termWidth <= 0 || m.termHeight <= 0 {
		return
	}
	m.rebuildContentCache()
	m.viewport.SetContent(m.contentCache.String())
	m.viewport.GotoBottom()
	m.updateViewportCache()
}

func (m *model) rebuildViewportPreservingScroll() {
	if m.termWidth <= 0 || m.termHeight <= 0 {
		return
	}
	yOffset := m.viewport.YOffset()
	m.rebuildContentCache()
	m.viewport.SetContent(m.contentCache.String())
	totalLines := m.viewport.TotalLineCount()
	visibleLines := m.viewport.Height()
	maxOffset := totalLines - visibleLines
	if maxOffset < 0 {
		maxOffset = 0
	}
	if yOffset > maxOffset {
		yOffset = maxOffset
	}
	m.viewport.SetYOffset(yOffset)
	m.updateViewportCache()
}

func (m *model) rebuildViewportScrollLock() {
	if m.termWidth <= 0 || m.termHeight <= 0 {
		return
	}
	wasAtBottom := m.viewport.AtBottom()
	m.rebuildContentCache()
	if wasAtBottom {
		m.viewport.GotoBottom()
	}
	m.updateViewportCache()
}

func (m *model) rebuildContentCache() {
	if m.termWidth <= 0 || m.termHeight <= 0 {
		return
	}

	currentWidth := m.viewport.Width()
	widthChanged := currentWidth != m.prevViewportWidth

	if widthChanged {
		for i := range m.logEntries {
			m.logEntries[i].clearRenderCache()
		}
		m.contentParts = make([]string, len(m.logEntries))
		for i := range m.logEntries {
			entry := &m.logEntries[i]
			m.contentParts[i] = m.renderSingleEntry(entry, currentWidth)
		}
		m.prevViewportWidth = currentWidth
		m.needFullRebuild = false
		for k := range m.dirtyEntryIndices {
			delete(m.dirtyEntryIndices, k)
		}
		m.rebuildLinePrefix()
		m.assembleViewportContent()
		m.viewport.SetContent(m.contentCache.String())
		return
	}

	contentChanged := false

	if m.needFullRebuild {
		m.contentParts = make([]string, len(m.logEntries))
		for i := range m.logEntries {
			entry := &m.logEntries[i]
			m.contentParts[i] = m.renderSingleEntry(entry, currentWidth)
		}
		m.needFullRebuild = false
		for k := range m.dirtyEntryIndices {
			delete(m.dirtyEntryIndices, k)
		}
		contentChanged = true
	} else {
		if len(m.dirtyEntryIndices) > 0 {
			for idx := range m.dirtyEntryIndices {
				if idx >= 0 && idx < len(m.logEntries) {
					entry := &m.logEntries[idx]
					entry.clearRenderCache()
					m.contentParts[idx] = m.renderSingleEntry(entry, currentWidth)
				}
				delete(m.dirtyEntryIndices, idx)
			}
			m.rebuildLinePrefix()
			contentChanged = true
		}

		if len(m.contentParts) < len(m.logEntries) {
			oldLen := len(m.contentParts)
			newParts := make([]string, len(m.logEntries))
			copy(newParts, m.contentParts)
			for i := oldLen; i < len(m.logEntries); i++ {
				entry := &m.logEntries[i]
				newParts[i] = m.renderSingleEntry(entry, currentWidth)
			}
			m.contentParts = newParts
			m.appendLinePrefix(oldLen)
			contentChanged = true
		}
	}

	if contentChanged {
		m.assembleViewportContent()
		m.viewport.SetContent(m.contentCache.String())
	}
}

func (m *model) assembleViewportContent() {
	m.contentCache.Reset()
	estCap := len(m.contentParts)*200 + 1024
	if estCap > m.contentCache.Cap() {
		m.contentCache.Grow(estCap)
	}
	m.contentCache.WriteString(m.renderWelcomePanel())
	if len(m.contentParts) > 0 {
		m.contentCache.WriteString("\n")

		var parts []string
		lastAgent := ""
		lastPartIsThinking := false

		for i, part := range m.contentParts {
			entry := &m.logEntries[i]

			if entry.isVerbose {
				continue
			}

			if part == "" || strings.TrimSpace(part) == "" {
				continue
			}

			agentChanged := false
			if entry.from != "" && entry.from != lastAgent {
				if entry.eventType == "ai_response" {
					sep := RenderAgentSeparator(entry.from, m.viewport.Width())
					if sep != "" {
						parts = append(parts, sep)
					}
				} else {
					badge := RenderAgentBadge(entry.from)
					if badge != "" {
						parts = append(parts, badge)
					}
				}
				lastAgent = entry.from
				agentChanged = true
			}

			isThinking := entry.eventType == "thinking"
			mergeAsContinuation := isThinking && lastPartIsThinking && !agentChanged
			if mergeAsContinuation {
				part = renderThinkingContinuation(*entry, m.viewport.Width())
			}
			lastPartIsThinking = isThinking

			parts = append(parts, part)
		}

		m.contentCache.WriteString(strings.Join(parts, "\n"))
	}
}

func (m *model) renderSingleEntry(entry *logEntry, width int) string {
	if entry.toolEntry != nil && entry.toolEntry.Status == ToolStatusRunning {
		return renderToolEntryWithAnim(*entry, width, m.anim)
	}

	if entry.eventType == "llm_call_start" && entry.isToolRunning {
		return renderLLMCallWithAnim(*entry, width, m.anim)
	}

	if entry.eventType == "ai_stream" {
		content := entry.streamContent
		if entry.streaming && content != "" {
			content += "▌"
		}
		return content
	}

	if cached, ok := entry.getCachedRender(width); ok {
		return cached
	}

	if entry.toolEntry != nil {
		if entry.toolEntry.Call.Name == "deepthinking" &&
			entry.toolEntry.Status == ToolStatusSuccess &&
			entry.toolEntry.Result != nil &&
			entry.toolEntry.Result.Content != "" &&
			m.glamourRenderer != nil {
			if cached, ok := m.getGlamourCached(entry.toolEntry.Result.Content); ok {
				entry.setCachedRender(cached, width)
				return cached
			}
			rendered := m.renderDeepThinkingEntry(entry)
			m.putGlamourCached(entry.toolEntry.Result.Content, rendered)
			entry.setCachedRender(rendered, width)
			return rendered
		}
		rendered := renderToolEntry(*entry, width)
		entry.setCachedRender(rendered, width)
		return rendered
	}

	if entry.diffText != "" {
		rendered := renderDiff(entry)
		entry.setCachedRender(rendered, width)
		return rendered
	}

	if entry.eventType == "ai_response" && m.glamourRenderer != nil {
		if cached, ok := m.getGlamourCached(entry.content); ok {
			entry.setCachedRender(cached, width)
			return cached
		}
		rendered, err := m.glamourRenderer.Render(entry.content)
		if err == nil {
			m.putGlamourCached(entry.content, rendered)
			entry.setCachedRender(rendered, width)
			return rendered
		}
	}

	if entry.eventType == "user_message" {
		rendered := renderUserMessageBox(entry.content, width)
		entry.setCachedRender(rendered, width)
		return rendered
	}

	if entry.eventType == "thinking" {
		rendered := formatLogEntry(*entry, width)
		entry.setCachedRender(rendered, width)
		return rendered
	}

	formatted := formatLogEntry(*entry, width)
	entry.setCachedRender(formatted, width)
	return formatted
}

func (m *model) renderEntryTo(entry *logEntry, b *strings.Builder) {
	width := m.viewport.Width()

	if entry.toolEntry != nil && entry.toolEntry.Status == ToolStatusRunning {
		b.WriteString(renderToolEntryWithAnim(*entry, width, m.anim))
		return
	}

	if entry.eventType == "llm_call_start" && entry.isToolRunning {
		b.WriteString(renderLLMCallWithAnim(*entry, width, m.anim))
		return
	}

	if cached, ok := entry.getCachedRender(width); ok {
		b.WriteString(cached)
		return
	}

	b.WriteString(m.renderSingleEntry(entry, width))
}

func (m *model) renderDeepThinkingEntry(entry *logEntry) string {
	maxWidth := m.viewport.Width()

	toolEntry := entry.toolEntry
	params := toolEntry.Call.Summary
	if params == "" {
		params = formatToolParams(toolEntry.Call.Name, toolEntry.Call.Arguments)
	}

	header := RenderHeader(toolEntry.Status, toolEntry.Call.Name, params, "")

	mdContent := decodeIfJSONString(toolEntry.Result.Content)
	renderedBody, err := m.getGlamourRenderer(maxWidth - 4).Render(mdContent)
	if err != nil {
		return renderToolEntry(*entry, maxWidth)
	}

	return addToolCallBorders(header, maxWidth) + "\n" + renderedBody
}

func (m *model) appendLogEntry(entry *logEntry) {
	m.viewportDirty = true
	wasAtBottom := m.viewport.AtBottom()

	if m.termWidth > 0 && m.termHeight > 0 {
		currentWidth := m.viewport.Width()
		rendered := m.renderSingleEntry(entry, currentWidth)
		oldLen := len(m.contentParts)
		m.contentParts = append(m.contentParts, rendered)
		m.appendLinePrefix(oldLen)
		m.assembleViewportContent()
	} else {
		if m.contentCache.Len() > 0 {
			m.contentCache.WriteString("\n")
		}
		m.renderEntryTo(entry, m.contentCache)
	}

	m.viewport.SetContent(m.contentCache.String())
	if wasAtBottom {
		m.viewport.GotoBottom()
	}
	m.updateViewportCache()
}

func extractToolSummary(toolName string, argsJSON string) string {
	var args map[string]interface{}
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}
	switch toolName {
	case "read_file", "edit_file", "search_replace_in_file", "create_file", "delete_file":
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
			if len(cmd) > 80 {
				return cmd[:77] + "..."
			}
			return cmd
		}
	case "search_by_regex", "grep_search":
		if pattern, ok := args["pattern"].(string); ok && pattern != "" {
			if len(pattern) > 60 {
				return pattern[:57] + "..."
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
	case "thinking", "agent_exit":
		if reason, ok := args["reason"].(string); ok && reason != "" {
			return reason
		}
	case "find_function_callee", "find_function_caller":
		if fn, ok := args["function_name"].(string); ok && fn != "" {
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

func extractResultBrief(toolName string, result string) string {
	if strings.HasPrefix(result, "Error:") {
		errMsg := strings.TrimPrefix(result, "Error: ")
		if len(errMsg) > 50 {
			return errMsg[:47] + "..."
		}
		return errMsg
	}
	switch toolName {
	case "read_file":
		lines := strings.Count(result, "\n")
		if lines > 0 {
			return fmt.Sprintf("%d lines", lines)
		}
		return fmt.Sprintf("%d bytes", len(result))
	case "list_dir", "print_dir_tree":
		return ""
	case "run_bash":
		var r struct {
			Output string `json:"output"`
		}
		if err := json.Unmarshal([]byte(result), &r); err == nil {
			if r.Output == "" {
				return ""
			}
			trimmed := strings.TrimSpace(r.Output)
			if len(trimmed) > 60 {
				return trimmed[:57] + "..."
			}
			return trimmed
		}
		trimmed := strings.TrimSpace(result)
		if len(trimmed) > 60 {
			return trimmed[:57] + "..."
		}
		return trimmed
	case "search_by_regex", "grep_search":
		lines := strings.Count(result, "\n")
		return fmt.Sprintf("%d matches", lines)
	case "find_function_callee":
		var r struct {
			Callees []interface{} `json:"callees"`
		}
		if err := json.Unmarshal([]byte(result), &r); err == nil {
			return fmt.Sprintf("%d callees", len(r.Callees))
		}
		return ""
	case "find_function_caller":
		var r struct {
			Callers []interface{} `json:"callers"`
		}
		if err := json.Unmarshal([]byte(result), &r); err == nil {
			return fmt.Sprintf("%d callers", len(r.Callers))
		}
		return ""
	default:
		if strings.HasPrefix(toolName, "delegate_") {
			return ""
		}
		return ""
	}
}

func isVerboseEventType(eventType string) bool {
	switch eventType {
	case "tool_call_start", "tool_call_result",
		"llm_call_start", "llm_call_end",
		"commit_context_loaded", "context_compressed", "context_ultimate_compressed",
		"knowledge_injected",
		"model_info", "thinking":
		return true
	}
	return false
}

func isEditFileTool(toolName string) bool {
	return toolName == "search_replace_in_file"
}

func formatEventAsEntry(event *messaging.MessageEvent) logEntry {
	entry := logEntry{
		timestamp: event.Timestamp,
		eventType: string(event.Type),
		from:      event.From,
	}

	switch event.Type {
	case "ai_stream_start", "ai_chunk", "ai_stream_end":
		return entry
	}

	switch event.Type {
	case "ai_response":
		if s, ok := event.Content.(string); ok {
			entry.content = s
		} else {
			entry.content = fmt.Sprintf("%v", event.Content)
		}
	case "tool_call_start":
		if m, ok := event.Content.(map[string]interface{}); ok {
			if name, ok := m["tool_name"].(string); ok {
				entry.toolName = name
			}
			if id, ok := m["tool_call_id"].(string); ok {
				entry.toolCallID = id
			}
			if args, ok := m["arguments"].(string); ok {
				entry.content = args
				entry.executionSummary = extractToolSummary(entry.toolName, args)
				entry.toolEntry = NewToolEntry(ToolCallInfo{
					ID:        entry.toolCallID,
					Name:      entry.toolName,
					Arguments: args,
					Summary:   entry.executionSummary,
				})
			}
		}
		entry.isToolRunning = true
		if entry.content == "" {
			entry.content = fmt.Sprintf("%v", event.Content)
		}
		if entry.toolEntry == nil {
			entry.toolEntry = NewToolEntry(ToolCallInfo{
				ID:   entry.toolCallID,
				Name: entry.toolName,
			})
		}
	case "tool_call_result":
		if m, ok := event.Content.(map[string]interface{}); ok {
			if name, ok := m["tool_name"].(string); ok {
				entry.toolName = name
			}
			if id, ok := m["tool_call_id"].(string); ok {
				entry.toolCallID = id
			}
			if result, ok := m["result"].(string); ok {
				entry.content = result
				entry.resultBrief = extractResultBrief(entry.toolName, result)
				entry.diffText = extractDiffFromResult(result)
			}
		}
		entry.isToolRunning = false
		if entry.content == "" {
			entry.content = fmt.Sprintf("%v", event.Content)
		}
		isErr := strings.HasPrefix(entry.content, "Error:")
		entry.toolEntry = NewToolEntry(ToolCallInfo{
			ID:      entry.toolCallID,
			Name:    entry.toolName,
			Summary: extractToolSummary(entry.toolName, ""),
		})
		if isErr {
			entry.toolEntry.SetResult(ToolResultInfo{
				ToolCallID: entry.toolCallID,
				Name:       entry.toolName,
				Content:    entry.content,
				IsError:    true,
			})
		} else {
			entry.toolEntry.SetResult(ToolResultInfo{
				ToolCallID: entry.toolCallID,
				Name:       entry.toolName,
				Content:    entry.content,
				IsError:    false,
			})
		}
	case "user_help_needed":
		if s, ok := event.Content.(string); ok {
			entry.content = "HELP: " + s
		} else {
			entry.content = fmt.Sprintf("HELP: %v", event.Content)
		}
	case "commit_context_loaded":
		if m, ok := event.Content.(map[string]interface{}); ok {
			if summaries, ok := m["summaries"].([]interface{}); ok && len(summaries) > 0 {
				var parts []string
				maxShow := len(summaries)
				if maxShow > 3 {
					maxShow = 3
				}
				for i := 0; i < maxShow; i++ {
					sum, ok := summaries[i].(map[string]interface{})
					if !ok {
						continue
					}
					hash := ""
					if h, ok := sum["hash"].(string); ok {
						hash = h[:8]
					}
					req := ""
					if r, ok := sum["requirement"].(string); ok {
						req = r
					}
					if hash != "" && req != "" {
						parts = append(parts, fmt.Sprintf("[%s] %s", hash, req))
					} else if hash != "" {
						parts = append(parts, fmt.Sprintf("[%s]", hash))
					}
				}
				if len(parts) > 0 {
					entry.content = fmt.Sprintf("📦 Commit Knowledge (%d total): %s", len(summaries), strings.Join(parts, " | "))
				} else {
					entry.content = fmt.Sprintf("📦 Loaded %d relevant commit(s)", len(summaries))
				}
			} else {
				if count, ok := m["count"].(float64); ok {
					entry.content = fmt.Sprintf("📦 Loaded %d relevant commit(s)", int(count))
				}
			}
		}
		if entry.content == "" {
			entry.content = fmt.Sprintf("%v", event.Content)
		}
	case "context_compressed", "context_ultimate_compressed":
		if m, ok := event.Content.(map[string]interface{}); ok {
			origTokens := 0
			if v, ok := m["original_tokens"].(int); ok {
				origTokens = v
			} else if v, ok := m["original_tokens"].(float64); ok {
				origTokens = int(v)
			}
			compTokens := 0
			if v, ok := m["compressed_tokens"].(int); ok {
				compTokens = v
			} else if v, ok := m["compressed_tokens"].(float64); ok {
				compTokens = int(v)
			}
			savedPercent := 0.0
			if v, ok := m["saved_percent"].(float64); ok {
				savedPercent = v
			}
			if event.Type == "context_ultimate_compressed" {
				totalPlans := 0
				if v, ok := m["total_plans"].(int); ok {
					totalPlans = v
				} else if v, ok := m["total_plans"].(float64); ok {
					totalPlans = int(v)
				}
				keptPlans := 0
				if v, ok := m["kept_plans"].(int); ok {
					keptPlans = v
				} else if v, ok := m["kept_plans"].(float64); ok {
					keptPlans = int(v)
				}
				userInputs := 0
				if v, ok := m["user_inputs"].(int); ok {
					userInputs = v
				} else if v, ok := m["user_inputs"].(float64); ok {
					userInputs = int(v)
				}
				truncated, _ := m["truncated"].(bool)
				entry.content = fmt.Sprintf("⚡ Ultimate compression: %d → %d tokens, kept %d/%d plans, user_inputs=%d, truncated=%v",
					origTokens, compTokens, keptPlans, totalPlans, userInputs, truncated)
			} else {
				entry.content = fmt.Sprintf("🧠 Context compressed: %d → %d tokens (%.1f%%)", origTokens, compTokens, savedPercent)
			}
		}
		if entry.content == "" {
			entry.content = fmt.Sprintf("%v", event.Content)
		}
	case "llm_call_start":
		if m, ok := event.Content.(map[string]interface{}); ok {
			modelName, _ := m["model"].(string)
			entry.content = fmt.Sprintf("[%s]", modelName)
		} else {
			entry.content = fmt.Sprintf("%v", event.Content)
		}
	case "llm_call_end":
		if durationRaw, ok := event.Metadata["duration_seconds"]; ok {
			var duration float64
			switch v := durationRaw.(type) {
			case float64:
				duration = v
			case int:
				duration = float64(v)
			}

			modelName, _ := event.Metadata["model"].(string)
			if modelName == "" {
				if m, ok := event.Content.(map[string]interface{}); ok {
					modelName, _ = m["model"].(string)
				}
			}

			hasError := false
			if errStr, ok := event.Metadata["error"]; ok && errStr != "" {
				hasError = true
			}

			if hasError {
				entry.content = fmt.Sprintf("✗ [%s] · %.2fs", modelName, duration)
			} else {
				entry.content = fmt.Sprintf("✓ [%s] · %.2fs", modelName, duration)
			}
		} else {
			entry.content = "✓ LLM call completed"
		}
	default:
		if s, ok := event.Content.(string); ok {
			entry.content = s
		} else {
			entry.content = fmt.Sprintf("%v", event.Content)
		}
	}

	entry.isVerbose = isVerboseEventType(string(event.Type))

	if entry.isVerbose && entry.eventType == "tool_call_start" && entry.toolEntry != nil {
		if isEditFileTool(entry.toolEntry.Call.Name) {
			entry.isVerbose = false
		}
	}

	return entry
}

func formatLogEntry(entry logEntry, maxWidth int) string {
	var prefix string
	var contentStyle lipgloss.Style

	switch entry.eventType {
	case "sub_agent_header":
		separator := strings.Repeat("─", maxWidth-2)
		return logSeparatorStyle.Render(" " + separator + " ")

	case "ai_response":
		if entry.from != "" {
			prefix = RenderAgentTag(entry.from) + " "
		} else {
			prefix = "AI  "
		}
		contentStyle = logAIResStyle
	case "user_message":
		return renderUserMessageBox(entry.content, maxWidth)
	case "tool_call_start":
		if entry.toolEntry != nil {
			return renderToolEntry(entry, maxWidth)
		}
		if entry.toolName != "" {
			prefix = "🔘 " + RenderToolName(DisplayToolName(entry.toolName))
		} else {
			prefix = "🔘 TOOL"
		}
		contentStyle = toolRunningStyle
	case "tool_call_result":
		if entry.toolEntry != nil {
			return renderToolEntry(entry, maxWidth)
		}
		if strings.HasPrefix(entry.content, "Error:") {
			if entry.toolName != "" {
				prefix = "❌ " + RenderToolName(DisplayToolName(entry.toolName))
			} else {
				prefix = "❌ RESULT"
			}
			contentStyle = toolErrorStyle
		} else {
			if entry.toolName != "" {
				prefix = "✅ " + RenderToolName(DisplayToolName(entry.toolName))
			} else {
				prefix = "✅ RESULT"
			}
			contentStyle = toolDoneStyle
		}
	case "llm_call_start":
		prefix = ""
		contentStyle = llmCallStyle
	case "llm_call_end":
		prefix = ""
		contentStyle = llmCallEndStyle
	case "thinking":
		prefix = thinkIconStyle.String()
		contentStyle = thinkTextStyle
		contentWidth := maxWidth - 8
		if contentWidth < 20 {
			contentWidth = 20
		}
		runes := []rune(entry.content)
		if len(runes) > contentWidth {
			entry.content = string(runes[:contentWidth-3]) + "..."
		}
		entry.content = strings.ReplaceAll(entry.content, "\n", " ")
	case "error":
		prefix = "✖ ERROR"
		contentStyle = logErrorLogStyle
	case "user_help_needed":
		prefix = "? HELP"
		contentStyle = logToolStyle
	case "context_compressed":
		prefix = ""
		contentStyle = logStatusStyle
	default:
		prefix = "● " + entry.eventType
		contentStyle = logStatusStyle
	}

	var displayContent string
	if entry.executionSummary != "" {
		displayContent = entry.executionSummary
		if entry.resultBrief != "" {
			displayContent += " · " + entry.resultBrief
		}
	} else {
		displayContent = strings.ReplaceAll(entry.content, "\n", " ")
	}

	if entry.prefix != "" {
		displayContent = entry.prefix + displayContent
	}

	contentWidth := maxWidth - 10
	if contentWidth < 20 {
		contentWidth = 20
	}
	if lipgloss.Width(displayContent) > contentWidth {
		runes := []rune(displayContent)
		if len(runes) > contentWidth-3 {
			displayContent = string(runes[:contentWidth-3]) + "..."
		}
	}

	return prefix + " " + contentStyle.Render(displayContent)
}

func renderThinkingContinuation(entry logEntry, maxWidth int) string {
	content := strings.ReplaceAll(entry.content, "\n", " ")
	contentWidth := maxWidth - 10
	if contentWidth < 20 {
		contentWidth = 20
	}
	if lipgloss.Width(content) > contentWidth {
		runes := []rune(content)
		if len(runes) > contentWidth-3 {
			content = string(runes[:contentWidth-3]) + "..."
		}
	}
	indent := "      │ "
	return indent + thinkTextStyle.Render(content)
}

func renderUserMessageBox(content string, maxWidth int) string {
	boxWidth := maxWidth - 4
	if boxWidth < 20 {
		wrapped := wrapText(content, boxWidth)
		wrappedLines := strings.Split(wrapped, "\n")
		var lines []string
		lines = append(lines, userPrefixStyle.Render("You:")+" "+wrappedLines[0])
		for i := 1; i < len(wrappedLines); i++ {
			lines = append(lines, "     "+wrappedLines[i])
		}
		return strings.Join(lines, "\n")
	}

	innerWidth := boxWidth - 2
	if innerWidth < 4 {
		innerWidth = 4
	}
	textWidth := innerWidth - 2

	var msgLines []string
	for _, para := range strings.Split(content, "\n") {
		if para == "" {
			msgLines = append(msgLines, "")
		} else {
			wrapped := wrapText(para, textWidth)
			msgLines = append(msgLines, strings.Split(wrapped, "\n")...)
		}
	}
	if len(msgLines) == 0 {
		msgLines = []string{""}
	}

	label := " You "
	labelWidth := lipgloss.Width(label)
	dashCount := boxWidth - labelWidth - 3
	if dashCount < 0 {
		dashCount = 0
	}
	topLine := userMsgBoxBorderStyle.Render("┌─") +
		userPrefixStyle.Render(label) +
		userMsgBoxBorderStyle.Render(strings.Repeat("─", dashCount)+"┐")

	var interior []string
	for _, line := range msgLines {
		lineWidth := lipgloss.Width(line)
		padding := textWidth - lineWidth
		if padding < 0 {
			padding = 0
		}
		interiorLine := userMsgBoxBorderStyle.Render("│") +
			userMsgBoxTextStyle.Render(" "+line+strings.Repeat(" ", padding)+" ") +
			userMsgBoxBorderStyle.Render("│")
		interior = append(interior, interiorLine)
	}

	bottomLine := userMsgBoxBorderStyle.Render("└" + strings.Repeat("─", boxWidth-2) + "┘")

	return topLine + "\n" + strings.Join(interior, "\n") + "\n" + bottomLine
}

func wrapText(text string, maxWidth int) string {
	if maxWidth <= 0 {
		return text
	}
	lines := strings.Split(text, "\n")
	var result []string
	for _, line := range lines {
		if line == "" {
			result = append(result, "")
			continue
		}
		words := strings.Fields(line)
		if len(words) == 0 {
			result = append(result, "")
			continue
		}
		var current string
		var currentWidth int
		for _, word := range words {
			w := lipgloss.Width(word)
			if current == "" {
				current = word
				currentWidth = w
			} else if currentWidth+1+w <= maxWidth {
				current += " " + word
				currentWidth += 1 + w
			} else {
				result = append(result, current)
				current = word
				currentWidth = w
			}
		}
		if current != "" {
			result = append(result, current)
		}
	}
	return strings.Join(result, "\n")
}

func renderToolEntry(entry logEntry, maxWidth int) string {
	if entry.toolEntry == nil {
		return formatLogEntry(entry, maxWidth)
	}

	contentWidth := maxWidth - 4
	if contentWidth < 30 {
		contentWidth = 30
	}
	full := RenderToolLine(entry.toolEntry, nil, contentWidth)

	header, body, hasBody := splitToolRendered(full)
	if !hasBody {
		return full
	}
	return header + "\n" + body
}

func splitToolRendered(full string) (header, body string, hasBody bool) {
	lines := strings.SplitN(full, "\n", 4)
	if len(lines) <= 3 {
		return full, "", false
	}
	header = strings.Join(lines[:3], "\n")
	body = lines[3]
	return header, body, true
}
func renderToolEntryWithAnim(entry logEntry, maxWidth int, anim *Anim) string {
	if entry.toolEntry == nil {
		return formatLogEntry(entry, maxWidth)
	}
	params := entry.toolEntry.Call.Summary
	if params == "" {
		params = entry.executionSummary
	}
	return RenderPending(entry.toolEntry.Call.Name, params, anim)
}

func extractDiffFromResult(result string) string {
	if !strings.Contains(result, `"diff"`) {
		return ""
	}
	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(result), &parsed); err != nil {
		return ""
	}
	if diff, ok := parsed["diff"].(string); ok && diff != "" {
		return diff
	}
	return ""
}

func renderDiff(entry *logEntry) string {
	var prefix string
	icon := IconSuccess
	if entry.isToolRunning {
		icon = IconPending
	}
	prefix = icon + " " + RenderToolName(DisplayToolName(entry.toolName))
	if entry.executionSummary != "" {
		prefix += " " + ParamMain.Render("· "+entry.executionSummary)
	}

	diffContent := RenderDiffContent(entry.diffText, 100)
	return prefix + "\n" + diffContent
}

func decodeIfJSONString(s string) string {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) >= 2 && trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"' {
		var decoded string
		if err := json.Unmarshal([]byte(trimmed), &decoded); err == nil {
			return decoded
		}
	}
	return s
}

func renderLLMCallWithAnim(entry logEntry, maxWidth int, anim *Anim) string {
	icon := IconPending
	content := strings.ReplaceAll(entry.content, "\n", " ")
	contentWidth := maxWidth - 20
	if contentWidth < 20 {
		contentWidth = 20
	}
	if lipgloss.Width(content) > contentWidth {
		runes := []rune(content)
		if len(runes) > contentWidth-3 {
			content = string(runes[:contentWidth-3]) + "..."
		}
	}
	return icon + " " + llmCallStyle.Render(content) + " " + anim.Render()
}
