package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"codeactor/internal/messaging"

	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
)

// computeFooterHeight calculates the actual footer height based on current state.
// Uses caching to avoid repeated expensive computation.
func (m *model) computeFooterHeight() int {
	if m.footerHeightValid {
		return m.cachedFooterHeight
	}

	height := 1 // separator line

	// Input area (only in edit mode; hidden in command mode)
	if !m.commandMode {
		// inputPanelStyle = Border(top+bottom=2) + MarginTop(1) → 3 extra lines
		height += 3 + m.computeInputHeight()
		// Newline after input panel
		height += 1
		// Skill autocomplete suggestions
		if m.skillAutoComplete && len(m.skillSuggestions) > 0 {
			height += len(m.skillSuggestions) + 1 // suggestion lines + hint line
		}
		// Keyword autocomplete suggestions
		if m.keywordAutoComplete && len(m.keywordSuggestions) > 0 {
			height += 1
		}
	}

	// Error message
	if m.errMsg != "" {
		height += 1
	}

	// Token dashboard
	totalTokens := m.inputTokens + m.outputTokens
	if totalTokens > 0 {
		height += 4 // 2 borders + 1 header + 1 separator
		if m.commandMode && m.taskRunning {
			height++
		}
		for _, au := range m.tokenUsagePerAgent {
			if au.InputTokens+au.OutputTokens > 0 {
				height++
			}
		}
	}

	// Spacing + Status line
	if m.commandMode {
		height += 1 // spacing before status
		height += 1 // status bar
	} else {
		height += 2 // two newlines before status bar
		height += 1 // status bar
	}

	m.cachedFooterHeight = height
	m.footerHeightValid = true
	return height
}

// updateViewportCache refreshes the cached viewport view.
// Call this whenever viewport state changes (scroll, resize, content update).
func (m *model) updateViewportCache() {
	m.cachedViewportView = m.viewport.View()
	m.viewportViewValid = true
	m.prevViewportYOffset = m.viewport.YOffset()
	m.prevViewportHeight = m.viewport.Height()
}

// invalidateFooterCache marks the footer cache as stale.
// Call this whenever state affecting footer height changes.
func (m *model) invalidateFooterCache() {
	m.footerHeightValid = false
	m.cachedSeparator = ""
	// Status bar also needs recalculation
	m.cachedStatusBar = ""
	m.statusBarValid = false
	// Token dashboard too
	m.cachedTokenDashboard = ""
	m.tokenDashboardValid = false
}

// getGlamourRenderer returns a cached glamour renderer for the given width.
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
		// Return a fallback with safe defaults
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
	m.viewport.SetWidth(m.termWidth)
	m.viewport.SetHeight(vpHeight)

	// Update glamour renderer reference for backward compat
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

// invalidateRenderedCache clears cached rendered output on all log entries.
// Called on terminal resize since rendering depends on viewport width.
func (m *model) invalidateRenderedCache() {
	for i := range m.logEntries {
		m.logEntries[i].clearRenderCache()
		if m.logEntries[i].toolEntry != nil {
			m.logEntries[i].toolEntry.InvalidateCache()
		}
	}
	// Clear glamour renderer cache on resize
	m.glamourRenderers = nil
	m.markAllEntriesDirty()
}

// buildViewportContent rebuilds the full viewport content from scratch.
// Used for initial load, terminal resize, or conversation switch.
func (m *model) buildViewportContent() {
	// Skip rebuild if dimensions are not initialized
	if m.termWidth <= 0 || m.termHeight <= 0 {
		return
	}
	m.rebuildContentCache()
	m.viewport.SetContent(m.contentCache.String())
	m.viewport.GotoBottom()
	m.updateViewportCache()
}

// rebuildViewportPreservingScroll rebuilds viewport content but preserves
// the current scroll position. Used for animation tick updates so that
// scrolling up to read history isn't interrupted by SetContent+GotoBottom.
func (m *model) rebuildViewportPreservingScroll() {
	// Skip rebuild if dimensions are not initialized
	if m.termWidth <= 0 || m.termHeight <= 0 {
		return
	}
	yOffset := m.viewport.YOffset()
	m.rebuildContentCache()
	m.viewport.SetContent(m.contentCache.String())
	// Restore Y offset, clamped to avoid overscroll
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

// rebuildViewportScrollLock rebuilds viewport content and scrolls to bottom
// only if the user was already at the bottom. Used when tool call results
// arrive — new content should be shown to users who are following along,
// but shouldn't interrupt users reading history.
func (m *model) rebuildViewportScrollLock() {
	// Skip rebuild if dimensions are not initialized
	if m.termWidth <= 0 || m.termHeight <= 0 {
		return
	}
	wasAtBottom := m.viewport.AtBottom()
	m.rebuildContentCache()
	m.viewport.SetContent(m.contentCache.String())
	if wasAtBottom {
		m.viewport.GotoBottom()
	}
	m.updateViewportCache()
}

// rebuildContentCache 增量重建viewport内容缓存。
// 仅在以下情况重建：
//   1. 宽度变化 → 完全重建（所有条目重新渲染）
//   2. needFullRebuild → 完全重建
//   3. dirtyEntryIndices 非空 → 仅重新渲染脏条目
//   4. 新条目到达 (len(contentParts) < len(logEntries)) → 追加渲染
// 其他情况跳过，避免不必要的计算。
func (m *model) rebuildContentCache() {
	if m.termWidth <= 0 || m.termHeight <= 0 {
		return
	}

	currentWidth := m.viewport.Width()
	widthChanged := currentWidth != m.prevViewportWidth

	// ── 宽度变化：完全重建 ──
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
		// 清除所有脏标记
		for k := range m.dirtyEntryIndices {
			delete(m.dirtyEntryIndices, k)
		}
		m.rebuildLinePrefix()
		m.assembleViewportContent()
		return
	}

	contentChanged := false

	// ── needFullRebuild：完全重建（如对话切换）──
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
		// ── 细粒度脏标记：仅重新渲染变更的条目 ──
		if len(m.dirtyEntryIndices) > 0 {
			for idx := range m.dirtyEntryIndices {
				if idx >= 0 && idx < len(m.logEntries) {
					entry := &m.logEntries[idx]
					entry.clearRenderCache()
					m.contentParts[idx] = m.renderSingleEntry(entry, currentWidth)
				}
				delete(m.dirtyEntryIndices, idx)
			}
			// 增量更新前缀和（比全量 rebuildLinePrefix 更高效）
			m.rebuildLinePrefix()
			contentChanged = true
		}

		// ── 增量追加：新条目到达 ──
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
	}
}

// assembleViewportContent 将contentParts组装为完整viewport内容
// Uses strings.Join for O(n) single-pass concatenation instead of WriteString loop.
func (m *model) assembleViewportContent() {
	m.contentCache.Reset()
	// Estimate capacity to avoid reallocations
	estCap := len(m.contentParts)*200 + 1024
	if estCap > m.contentCache.Cap() {
		m.contentCache.Grow(estCap)
	}
	m.contentCache.WriteString(m.renderWelcomePanel())
	if len(m.contentParts) > 0 {
		m.contentCache.WriteString("\n")
		m.contentCache.WriteString(strings.Join(m.contentParts, "\n"))
	}
}

// renderSingleEntry 渲染单个logEntry并返回字符串，使用宽度键控缓存
func (m *model) renderSingleEntry(entry *logEntry, width int) string {
	// For running tool entries, never cache (animation changes each frame)
	if entry.toolEntry != nil && entry.toolEntry.Status == ToolStatusRunning {
		return renderToolEntryWithAnim(*entry, width, m.anim)
	}

	// For running LLM call entries, render with animation (single line)
	if entry.eventType == "llm_call_start" && entry.isToolRunning {
		return renderLLMCallWithAnim(*entry, width, m.anim)
	}

	// Check width-keyed cache
	if cached, ok := entry.getCachedRender(width); ok {
		return cached
	}

	// Context compression rendering
	if entry.compactData != nil {
		rendered := renderContextCompressed(*entry, width)
		entry.setCachedRender(rendered, width)
		return rendered
	}

	// Tool entry rendering (non-running) — use new renderer
	if entry.toolEntry != nil {
		// Special case: deepthinking output should be rendered as formatted Markdown via Glamour
		if entry.toolEntry.Call.Name == "deepthinking" &&
			entry.toolEntry.Status == ToolStatusSuccess &&
			entry.toolEntry.Result != nil &&
			entry.toolEntry.Result.Content != "" &&
			m.glamourRenderer != nil {
			rendered := m.renderDeepThinkingEntry(entry)
			entry.setCachedRender(rendered, width)
			return rendered
		}
		rendered := renderToolEntry(*entry, width)
		entry.setCachedRender(rendered, width)
		return rendered
	}

	// Diff rendering takes priority
	if entry.diffText != "" {
		rendered := renderDiff(entry)
		entry.setCachedRender(rendered, width)
		return rendered
	}

	if entry.eventType == "ai_response" && m.glamourRenderer != nil {
		rendered, err := m.glamourRenderer.Render(entry.content)
		if err == nil {
			collapsed := collapseAndHint(rendered, entry, DefaultCollapseLines)
			entry.setCachedRender(collapsed, width)
			return collapsed
		}
	}

	// User message with collapse support
	if entry.eventType == "user_message" {
		rendered := renderUserMessageBoxCollapsible(entry.content, width, entry)
		entry.setCachedRender(rendered, width)
		return rendered
	}

	// Fallback to simple text rendering
	formatted := formatLogEntry(*entry, width)
	entry.setCachedRender(formatted, width)
	return formatted
}

// renderEntryTo renders a single log entry into the builder, caching the result
// in the entry for reuse. Uses glamour for ai_response, diff styling for diffs,
// plain formatting otherwise.
//
// Deprecated: prefer renderSingleEntry which uses width-keyed cache.
func (m *model) renderEntryTo(entry *logEntry, b *strings.Builder) {
	width := m.viewport.Width()

	// For running tool entries, never cache (animation changes each frame)
	if entry.toolEntry != nil && entry.toolEntry.Status == ToolStatusRunning {
		b.WriteString(renderToolEntryWithAnim(*entry, width, m.anim))
		return
	}

	// For running LLM call entries, render with animation (single line)
	if entry.eventType == "llm_call_start" && entry.isToolRunning {
		b.WriteString(renderLLMCallWithAnim(*entry, width, m.anim))
		return
	}

	// Use width-keyed cache
	if cached, ok := entry.getCachedRender(width); ok {
		b.WriteString(cached)
		return
	}

	// Delegate to renderSingleEntry which handles caching
	b.WriteString(m.renderSingleEntry(entry, width))
}

// renderDeepThinkingEntry renders a deepthinking tool result with Glamour markdown formatting.
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

// appendLogEntry renders a single new entry and appends it incrementally to the viewport.
// Uses scroll lock: only auto-scrolls to bottom if the user was already at the bottom.
func (m *model) appendLogEntry(entry *logEntry) {
	m.viewportDirty = true
	wasAtBottom := m.viewport.AtBottom()

	// Step 2 增量构建：将新条目渲染到 contentParts
	if m.termWidth > 0 && m.termHeight > 0 {
		currentWidth := m.viewport.Width()
		rendered := m.renderSingleEntry(entry, currentWidth)
		oldLen := len(m.contentParts)
		m.contentParts = append(m.contentParts, rendered)
		m.appendLinePrefix(oldLen)
		m.assembleViewportContent()
	} else {
		// Fallback: direct append to contentCache (old behavior)
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
	}
	// For delegate tools, show task summary
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

// extractResultBrief generates a short result summary for the finished line.
func extractResultBrief(toolName string, result string) string {
	if strings.HasPrefix(result, "Error:") {
		// Short error message
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
		// Parse JSON to extract the output field.
		var r struct {
			Output string `json:"output"`
		}
		if err := json.Unmarshal([]byte(result), &r); err == nil {
			// Successfully parsed JSON. Show output if non-empty.
			if r.Output == "" {
				return ""
			}
			trimmed := strings.TrimSpace(r.Output)
			if len(trimmed) > 60 {
				return trimmed[:57] + "..."
			}
			return trimmed
		}
		// Fallback for non-JSON (e.g., raw error string).
		trimmed := strings.TrimSpace(result)
		if len(trimmed) > 60 {
			return trimmed[:57] + "..."
		}
		return trimmed
	case "search_by_regex", "grep_search":
		lines := strings.Count(result, "\n")
		return fmt.Sprintf("%d matches", lines)
	default:
		if strings.HasPrefix(toolName, "delegate_") {
			return ""
		}
		return ""
	}
}

// formatEventAsEntry converts a MessageEvent to a logEntry.
func formatEventAsEntry(event *messaging.MessageEvent) logEntry {
	entry := logEntry{
		timestamp: event.Timestamp,
		eventType: event.Type,
		from:      event.From,
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
				// Create ToolEntry for new-style rendering
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
			// Fallback: create ToolEntry even without parsed args
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
				// Try to extract diff from JSON result
				entry.diffText = extractDiffFromResult(result)
			}
		}
		entry.isToolRunning = false
		if entry.content == "" {
			entry.content = fmt.Sprintf("%v", event.Content)
		}
		// Create a ToolEntry for standalone results so they use new-style
		// rendering (✓ read_file · /path/to/file) instead of the legacy
		// emoji path (✅ read_file).
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
	case "context_compressed":
		if m, ok := event.Content.(map[string]interface{}); ok {
			origTokens := 0
			if v, ok := m["original_tokens"].(float64); ok {
				origTokens = int(v)
			}
			compTokens := 0
			if v, ok := m["compressed_tokens"].(float64); ok {
				compTokens = int(v)
			}
			ratioStr := ""
			if v, ok := m["ratio"].(string); ok {
				ratioStr = v
			}
			statsStr := ""
			if v, ok := m["compression_stats"].(string); ok {
				statsStr = v
			}
			ratioVal := 0.0
			if ratioStr != "" {
				s := strings.TrimSuffix(ratioStr, "%")
				if f, err := strconv.ParseFloat(s, 64); err == nil {
					ratioVal = f
				}
			}
			entry.compactData = &CompactData{
				OriginalTokens:   origTokens,
				CompressedTokens: compTokens,
				Ratio:            ratioVal,
				Stats:            statsStr,
			}
			entry.content = fmt.Sprintf("上下文压缩 %s → %s (%s)",
				FormatTokenCount(origTokens), FormatTokenCount(compTokens), ratioStr)
		}
		if entry.content == "" {
			entry.content = fmt.Sprintf("%v", event.Content)
		}
	case "llm_call_start":
		if m, ok := event.Content.(map[string]interface{}); ok {
			modelName, _ := m["model"].(string)
			agentName, _ := m["agent"].(string)
			displayAgent := agentName
			if displayAgent == "" {
				displayAgent = "Agent"
			}
			entry.content = fmt.Sprintf("▸ %s  [%s]", displayAgent, modelName)
		} else {
			entry.content = fmt.Sprintf("▸ %v", event.Content)
		}
	case "llm_call_end":
		// Extract duration from metadata
		if durationRaw, ok := event.Metadata["duration_seconds"]; ok {
			var duration float64
			switch v := durationRaw.(type) {
			case float64:
				duration = v
			case int:
				duration = float64(v)
			}

			// Also get model and agent from metadata
			modelName, _ := event.Metadata["model"].(string)
			agentName, _ := event.Metadata["agent"].(string)
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
				entry.content = fmt.Sprintf("◂ %s  [%s]  ✗ %.2fs", agentName, modelName, duration)
			} else {
				entry.content = fmt.Sprintf("◂ %s  [%s]  ✓ %.2fs", agentName, modelName, duration)
			}
		} else {
			entry.content = "◂ LLM call completed"
		}
	default:
		if s, ok := event.Content.(string); ok {
			entry.content = s
		} else {
			entry.content = fmt.Sprintf("%v", event.Content)
		}
	}

	// Unified pre-collapse: auto-collapse long content for all types that may produce it.
	switch event.Type {
	case "ai_response", "user_message", "tool_call_result":
		if s, ok := event.Content.(string); ok {
			if strings.Count(s, "\n") >= DefaultCollapseLines {
				entry.collapsed = true
			}
		}
	}

	return entry
}

// formatLogEntry renders a single log entry as a styled line.
func formatLogEntry(entry logEntry, maxWidth int) string {
	var prefix string
	var contentStyle lipgloss.Style

	switch entry.eventType {
	case "sub_agent_header":
		// Render a dim separator line for sub-agent group headers
		separator := strings.Repeat("─", maxWidth-2)
		return logSeparatorStyle.Render(" " + separator + " ")

	case "ai_response":
		prefix = "AI  "
		contentStyle = logAIResStyle
	case "user_message":
		return renderUserMessageBox(entry.content, maxWidth)
	case "tool_call_start":
		// Use new-style rendering if ToolEntry is available
		if entry.toolEntry != nil {
			return renderToolEntry(entry, maxWidth)
		}
		// Fallback: legacy rendering
		if entry.toolName != "" {
			prefix = "🔘 " + RenderToolName(DisplayToolName(entry.toolName))
		} else {
			prefix = "🔘 TOOL"
		}
		contentStyle = toolRunningStyle
	case "tool_call_result":
		// Use new-style rendering if ToolEntry is available (via parent start entry)
		// standalone entries use legacy rendering
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
	case "context_compressed":
		if entry.compactData != nil {
			return renderContextCompressed(entry, maxWidth)
		}
		prefix = "🗜️ 上下文压缩"
		contentStyle = StatusStyle
	case "llm_call_start":
		prefix = ""
		contentStyle = llmCallStyle
	case "llm_call_end":
		prefix = ""
		contentStyle = llmCallEndStyle
	case "error":
		prefix = "✖ ERROR"
		contentStyle = logErrorLogStyle
	case "user_help_needed":
		prefix = "? HELP"
		contentStyle = logToolStyle
	default:
		prefix = "● " + entry.eventType
		contentStyle = logStatusStyle
	}

	// Build display content: prefer execution summary + result brief
	var displayContent string
	if entry.executionSummary != "" {
		displayContent = entry.executionSummary
		if entry.resultBrief != "" {
			displayContent += " · " + entry.resultBrief
		}
	} else {
		displayContent = strings.ReplaceAll(entry.content, "\n", " ")
	}

	// Prepend prefix (indentation for sub-agent messages) to display content
	if entry.prefix != "" {
		displayContent = entry.prefix + displayContent
	}

	// Truncate long content
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

// renderUserMessageBox renders a user message as a simple read-only textbox
// with a "You" title label in the top border, supporting multi-line content
// and automatic word wrapping.
func renderUserMessageBox(content string, maxWidth int) string {
	// Available width for the box (accounting for left padding in viewport)
	boxWidth := maxWidth - 4
	if boxWidth < 20 {
		// Very narrow terminal: fallback to simple prefix + wrapped text
		wrapped := wrapText(content, boxWidth)
		wrappedLines := strings.Split(wrapped, "\n")
		var lines []string
		lines = append(lines, userPrefixStyle.Render("You:")+" "+wrappedLines[0])
		for i := 1; i < len(wrappedLines); i++ {
			lines = append(lines, "     "+wrappedLines[i])
		}
		return strings.Join(lines, "\n")
	}

	// Inner width of the box (between the ││ border pipes)
	innerWidth := boxWidth - 2 // subtract 2 for the left and right border pipes
	if innerWidth < 4 {
		innerWidth = 4
	}
	textWidth := innerWidth - 2 // subtract 1 space padding on each side

	// Wrap the message content, preserving explicit newlines
	var msgLines []string
	for _, para := range strings.Split(content, "\n") {
		if para == "" {
			msgLines = append(msgLines, "") // preserve blank lines
		} else {
			wrapped := wrapText(para, textWidth)
			msgLines = append(msgLines, strings.Split(wrapped, "\n")...)
		}
	}
	if len(msgLines) == 0 {
		msgLines = []string{""}
	}

	// ---- Build the textbox ----

	// Top border with "You" title label
	// ┌─ You ───────────────┐
	label := " You "
	labelWidth := lipgloss.Width(label)
	// ┌─ (2) + label + dashes + ┐ (1) = boxWidth
	dashCount := boxWidth - labelWidth - 3
	if dashCount < 0 {
		dashCount = 0
	}
	topLine := userMsgBoxBorderStyle.Render("┌─") +
		userPrefixStyle.Render(label) +
		userMsgBoxBorderStyle.Render(strings.Repeat("─", dashCount)+"┐")

	// Interior lines: │  content text with padding  │
	var interior []string
	for _, line := range msgLines {
		lineWidth := lipgloss.Width(line)
		padding := textWidth - lineWidth
		if padding < 0 {
			padding = 0
		}
		// │ + space + text + space-fill + space + │
		interiorLine := userMsgBoxBorderStyle.Render("│") +
			userMsgBoxTextStyle.Render(" "+line+strings.Repeat(" ", padding)+" ") +
			userMsgBoxBorderStyle.Render("│")
		interior = append(interior, interiorLine)
	}

	// Bottom border
	// └────────────────────────┘
	// └ (1) + dashes + ┘ (1) = boxWidth
	bottomLine := userMsgBoxBorderStyle.Render("└" + strings.Repeat("─", boxWidth-2) + "┘")

	// Assemble
	return topLine + "\n" + strings.Join(interior, "\n") + "\n" + bottomLine
}

// renderUserMessageBoxCollapsible renders a user message box with collapse support.
// When the message exceeds DefaultCollapseLines and the entry is collapsed, only the
// first DefaultCollapseLines are shown with an expand hint inside the box.
func renderUserMessageBoxCollapsible(content string, maxWidth int, entry *logEntry) string {
	boxWidth := maxWidth - 4
	if boxWidth < 20 {
		// Very narrow terminal: no collapse support, just use simple rendering
		return renderUserMessageBox(content, maxWidth)
	}

	innerWidth := boxWidth - 2
	if innerWidth < 4 {
		innerWidth = 4
	}
	textWidth := innerWidth - 2

	// Wrap the message content
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

	// Apply collapse if entry is collapsed and content exceeds threshold
	hidden := 0
	if len(msgLines) > DefaultCollapseLines && entry.collapsed {
		hidden = len(msgLines) - DefaultCollapseLines
		msgLines = msgLines[:DefaultCollapseLines]
	}

	// Build the textbox
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

	// Add collapse hint inside the box if collapsed
	if hidden > 0 {
		hintText := fmt.Sprintf(" ▼ Expand (%d more lines) — Ctrl+O ", hidden)
		hintWidth := lipgloss.Width(hintText)
		hintPadding := textWidth - hintWidth
		if hintPadding < 0 {
			hintPadding = 0
		}
		// Separator line
		sepLine := userMsgBoxBorderStyle.Render("├") +
			collapseHintLineStyle.Render(strings.Repeat("╌", textWidth+2)) +
			userMsgBoxBorderStyle.Render("┤")
		// Hint line
		hintLine := userMsgBoxBorderStyle.Render("│") +
			collapseHintTextStyle.Render(" "+hintText+strings.Repeat(" ", hintPadding)+" ") +
			userMsgBoxBorderStyle.Render("│")
		interior = append(interior, sepLine, hintLine)
	}

	bottomLine := userMsgBoxBorderStyle.Render("└" + strings.Repeat("─", boxWidth-2) + "┘")

	return topLine + "\n" + strings.Join(interior, "\n") + "\n" + bottomLine
}

// wrapText word-wraps text to fit within maxWidth columns.
// Preserves existing newlines and wraps long lines at word boundaries.
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

// toggleCollapseAll toggles ALL collapsible entries at once.
// If ANY entry is expanded, collapses all; otherwise expands all.
// Returns true if any toggle was performed.
func (m *model) toggleCollapseAll() bool {
	if len(m.logEntries) == 0 {
		return false
	}
	// Determine current state: if any entry is expanded, we collapse all.
	anyExpanded := false
	for i := range m.logEntries {
		entry := &m.logEntries[i]
		if !entry.collapsed {
			anyExpanded = true
			break
		}
	}
	// Toggle all: if any expanded, collapse all; otherwise expand all.
	for i := range m.logEntries {
		entry := &m.logEntries[i]
		entry.collapsed = anyExpanded
		entry.clearRenderCache()
	}
	m.markAllEntriesDirty()
	m.rebuildViewportPreservingScroll()
	return true
}

// renderCollapseHint builds the expand/collapse indicator line.
func renderCollapseHint(hidden int, expanded bool) string {
	var icon, label string
	if expanded {
		icon = "▲"
		label = "Collapse"
	} else {
		icon = "▼"
		label = fmt.Sprintf("Expand (%d more lines)", hidden)
	}
	prefix := collapseHintLineStyle.Render("╌╌╌")
	suffix := collapseHintLineStyle.Render("╌╌╌")
	text := collapseHintTextStyle.Render(fmt.Sprintf(" %s %s (Ctrl+O) ", icon, label))
	return " " + prefix + text + suffix
}

// renderToolEntry renders a logEntry using the new tool rendering pipeline.
func renderToolEntry(entry logEntry, maxWidth int) string {
	if entry.toolEntry == nil {
		return formatLogEntry(entry, maxWidth)
	}

	contentWidth := maxWidth - 4
	if contentWidth < 30 {
		contentWidth = 30
	}
	full := RenderToolLine(entry.toolEntry, nil, contentWidth)

	// Split rendered output into header (border+header+border) and body,
	// then apply collapse only to the body.
	header, body, hasBody := splitToolRendered(full)
	if !hasBody {
		return full
	}
	collapsedBody := collapseAndHint(body, &entry, DefaultCollapseLines)
	return header + "\n" + collapsedBody
}

// splitToolRendered splits RenderToolLine output into header (topBorder\nheader\nbottomBorder)
// and body. Format: topBorder\nheader\nbottomBorder\nbody
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

func renderContextCompressed(entry logEntry, width int) string {
	data := entry.compactData
	if data == nil {
		return formatLogEntry(entry, width)
	}

	contentWidth := width - 4
	if contentWidth < 30 {
		contentWidth = 30
	}

	icon := IconSuccess
	badge := CompactBadgeStyle.Render("上下文压缩")
	original := CompactTokenStyle.Render(FormatTokenCount(data.OriginalTokens))
	arrow := CompactArrowStyle.Render("→")
	compressed := CompactTokenStyle.Render(FormatTokenCount(data.CompressedTokens))
	ratioStr := fmt.Sprintf("%.2f%%", data.Ratio)
	ratio := CompactRatioStyle(data.Ratio).Render(ratioStr)

	header := fmt.Sprintf("%s %s  %s %s %s  %s", icon, badge, original, arrow, compressed, ratio)

	return addToolCallBorders(header, contentWidth)
}

// extractDiffFromResult attempts to parse a JSON result string and extract the "diff" field.
func extractDiffFromResult(result string) string {
	// Quick check: does it look like JSON with a "diff" field?
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

// renderDiff renders a unified diff string with ANSI color styling.
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
	// Apply collapse to diff content.
	collapsed := collapseAndHint(diffContent, entry, DefaultCollapseLines)
	return prefix + "\n" + collapsed
}

// decodeIfJSONString attempts to decode a JSON-encoded string back to its original value.
// If s begins and ends with '"' and can be successfully unmarshalled as a JSON string,
// the decoded value is returned. Otherwise s is returned unchanged.
// This is needed because Adapter.Call applies json.Marshal to all tool results,
// which wraps plain strings in quotes and escapes special characters.
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

// renderLLMCallWithAnim renders a running LLM call entry with animation characters.
func renderLLMCallWithAnim(entry logEntry, maxWidth int, anim *Anim) string {
	icon := IconPending
	content := strings.ReplaceAll(entry.content, "\n", " ")
	// Truncate if too long
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
