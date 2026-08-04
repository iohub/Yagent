package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"codeactor/internal/tui/components"

	tea "charm.land/bubbletea/v2"
)

const (
	// 流式渲染节流阈值：累积 5 个 chunk（每个 chunk 约等于 1 个 token）才渲染一次
	aiChunkRenderThreshold = 5
	// 单次累积内容超过 64 字节立即渲染（防止单个超大 chunk 长时间不显示）
	aiChunkFlushMaxBytes = 64
	// 慢流兜底：距上次渲染超过 300ms 且有未渲染内容时立即渲染（保证交互反馈）
	aiChunkFlushInterval = 300 * time.Millisecond
)

// parseEventInt 从 event content map 中安全地解析整数，兼容 int/float64/json.Number。
func parseEventInt(v interface{}, fallback int) int {
	switch val := v.(type) {
	case int:
		return val
	case int64:
		return int(val)
	case float64:
		return int(val)
	case json.Number:
		if i, err := val.Int64(); err == nil {
			return int(i)
		}
	}
	return fallback
}

func (m *model) handleTaskEventMsg(msg taskEventMsg) (tea.Model, tea.Cmd) {
	// Don't process task events while any popup/dialog is showing.
	// Keep the event chain alive so the TUI resumes after dialog dismissal.
	if m.dialogStack != nil && m.dialogStack.Len() > 0 {
		// ai_stream_end / ai_response 是"定稿"类事件：仅更新已有流式条目的
		// 状态与内容，不创建 UI 元素、不依赖用户输入，可以安全穿透 dialog 守卫。
		// 若不穿透，任务完成弹窗（TaskCompleteDialog）抢先弹出后，最后一条
		// ai_response 会被丢弃，导致最后一条 agent 消息停留在 ai_stream
		// 纯文本状态、无法渲染为 markdown。
		if msg.event.Type == "ai_stream_end" || msg.event.Type == "ai_response" {
			// 穿透，继续向下处理
		} else if m.taskRunning {
			return m, listenForEvents(m.eventCh)
		} else {
			return m, nil
		}
	}

	// Count tokens for AI response events only (ai_stream_end no longer carries usage)
	// Prefer real token usage from API metadata, fallback to estimation
	if msg.event.Type == "ai_response" {
		if usageData, ok := msg.event.Metadata["usage"]; ok {
			if usageMap, ok := usageData.(map[string]interface{}); ok {
				var completionVal int64
				if completionTokens, ok := usageMap["completion_tokens"]; ok {
					switch v := completionTokens.(type) {
					case float64:
						completionVal = int64(v)
					case int64:
						completionVal = v
					case int:
						completionVal = int64(v)
					}
				}
				m.outputTokens += completionVal

				// Also track input tokens from API (PromptTokens)
				var promptVal int64
				if promptTokens, ok := usageMap["prompt_tokens"]; ok {
					switch v := promptTokens.(type) {
					case float64:
						promptVal = int64(v)
					case int64:
						promptVal = v
					case int:
						promptVal = int64(v)
					}
				}
				m.inputTokens += promptVal

				// Parse cache tokens
				var cacheCreationVal int64
				if cacheCreationTokens, ok := usageMap["cache_creation_input_tokens"]; ok {
					switch v := cacheCreationTokens.(type) {
					case float64:
						cacheCreationVal = int64(v)
					case int64:
						cacheCreationVal = v
					case int:
						cacheCreationVal = int64(v)
					}
				}
				m.cacheCreationInputTokens += cacheCreationVal

				var cacheReadVal int64
				if cacheReadTokens, ok := usageMap["cache_read_input_tokens"]; ok {
					switch v := cacheReadTokens.(type) {
					case float64:
						cacheReadVal = int64(v)
					case int64:
						cacheReadVal = v
					case int:
						cacheReadVal = int64(v)
					}
				}
				m.cacheReadInputTokens += cacheReadVal

				// Parse total_input_tokens (provider 口径，含缓存部分)
				// 若事件中没有该字段，则回退到旧公式计算
				var totalInputVal int64
				if totalInputTokens, ok := usageMap["total_input_tokens"]; ok {
					switch v := totalInputTokens.(type) {
					case float64:
						totalInputVal = int64(v)
					case int64:
						totalInputVal = v
					case int:
						totalInputVal = int64(v)
					}
				} else {
					// 向后兼容：使用旧公式 totalInput = promptVal + cacheReadVal + cacheCreationVal
					totalInputVal = promptVal + cacheReadVal + cacheCreationVal
				}
				m.totalInputTokens += totalInputVal

				// Track current agent run tokens (reset on agent switch)
				agentNameForRun := msg.event.From
				if agentNameForRun == "" {
					agentNameForRun = "Unknown"
				}
				if m.currentAgentRunTokens.AgentName != agentNameForRun {
					// Agent switched — reset run tokens for new agent
					m.currentAgentRunTokens = AgentRunTokens{
						AgentName: agentNameForRun,
					}
				}
				m.currentAgentRunTokens.InputTokens += promptVal
				m.currentAgentRunTokens.OutputTokens += completionVal
				m.currentAgentRunTokens.CacheReadInputTokens += cacheReadVal
				m.currentAgentRunTokens.CacheCreationInputTokens += cacheCreationVal
				m.currentAgentRunTokens.TotalInputTokens += totalInputVal

				// Track current running agent
				if m.taskRunning {
					if msg.event.From != "" {
						m.currentAgent = msg.event.From
					}
				}

				// Per-agent token tracking
				agentName := msg.event.From
				if agentName == "" {
					agentName = "Unknown"
				}
				agentUsage, exists := m.tokenUsagePerAgent[agentName]
				if !exists {
					agentUsage = &AgentTokenUsage{AgentName: agentName}
					m.tokenUsagePerAgent[agentName] = agentUsage
				}
				agentUsage.InputTokens += promptVal
				agentUsage.OutputTokens += completionVal
				agentUsage.CacheCreationInputTokens += cacheCreationVal
				agentUsage.CacheReadInputTokens += cacheReadVal
				agentUsage.TotalInputTokens += totalInputVal
			}
		} else {
			// Fallback: no usage metadata — estimate output tokens from content
			contentStr := fmt.Sprintf("%v", msg.event.Content)
			estimatedOutput := int64(len(contentStr) / 4)
			if estimatedOutput > 0 && m.tokenUsagePerAgent != nil {
				m.outputTokens += estimatedOutput

				// Per-agent token tracking (input unknown, use 0)
				agentName := msg.event.From
				if agentName == "" {
					agentName = "Unknown"
				}
				agentUsage, exists := m.tokenUsagePerAgent[agentName]
				if !exists {
					agentUsage = &AgentTokenUsage{AgentName: agentName}
					m.tokenUsagePerAgent[agentName] = agentUsage
				}
				agentUsage.OutputTokens += estimatedOutput
			}
		}
		// Token counts changed — update cached token dashboard render
		m.cachedTokenDashboard = m.renderTokenDashboard()
		m.tokenDashboardValid = true
		m.invalidateFooterCache()
	}

	// Capture model info for status bar display
	if msg.event.Type == "model_info" {
		if contentMap, ok := msg.event.Content.(map[string]interface{}); ok {
			if modelName, ok := contentMap["model"].(string); ok {
				m.currentModel = modelName
			}
			// Also capture agent name from model_info event
			if agentName, ok := contentMap["agent"].(string); ok && agentName != "" {
				m.currentAgent = agentName
			}
			// Capture provider name if available
			if providerName, ok := contentMap["provider"].(string); ok && providerName != "" {
				m.currentProvider = providerName
			}
		}
		// Model info changed — update status bar cache
		m.invalidateFooterCache()
		return m, listenForEvents(m.eventCh)
	}

	// ═══════════════════════════════════════════════════════════════
	// AI 流式事件处理：流式累积 + ai_response 定稿
	// ═══════════════════════════════════════════════════════════════

	// Handle ai_stream_start — 创建占位条目
	if msg.event.Type == "ai_stream_start" {
		agentName := msg.event.From
		if agentName == "" {
			agentName = "default"
		}

		// 创建占位条目
		entry := logEntry{
			timestamp:     msg.event.Timestamp,
			eventType:     "ai_stream",
			from:          agentName,
			agentName:     agentName,
			streamContent: "",
			streaming:     true,
			isVerbose:     false,
		}
		m.logEntries = append(m.logEntries, entry)
		idx := len(m.logEntries) - 1
		m.aiStreamActiveEntries[agentName] = idx
		m.markEntryDirty(idx)
		m.viewportDirty = true
		m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
		return m, listenForEvents(m.eventCh)
	}

	// Handle ai_chunk — 原地追加内容
	if msg.event.Type == "ai_chunk" {
		agentName := msg.event.From
		if agentName == "" {
			agentName = "default"
		}

		// 从 Content 中提取 content 字段
		var content string
		if contentMap, ok := msg.event.Content.(map[string]interface{}); ok {
			if c, ok := contentMap["content"].(string); ok {
				content = c
			}
		}

		if idx, ok := m.aiStreamActiveEntries[agentName]; ok && idx >= 0 && idx < len(m.logEntries) {
			// 累积缓冲：chunk 先进入 buffer，达到阈值才写入条目并触发渲染（降低 CPU）
			if m.aiChunkBuffers == nil {
				m.aiChunkBuffers = make(map[string]*aiChunkBuffer)
			}
			buf := m.aiChunkBuffers[agentName]
			if buf == nil {
				buf = &aiChunkBuffer{lastFlush: time.Now()}
				m.aiChunkBuffers[agentName] = buf
			}
			buf.content += content
			buf.count++

			// 满足任一条件即 flush：累积达 5 个 chunk（≈5 tokens）/ 内容超 64 字节 / 慢流超时 300ms
			shouldFlush := buf.count >= aiChunkRenderThreshold ||
				len(buf.content) >= aiChunkFlushMaxBytes ||
				(buf.count > 0 && time.Since(buf.lastFlush) >= aiChunkFlushInterval)
			if shouldFlush {
				le := &m.logEntries[idx]
				le.streamContent += buf.content
				le.content = le.streamContent // 同步到 content 字段
				le.clearRenderCache()         // 失效渲染缓存，确保内容变化生效
				m.markEntryDirty(idx)
				m.viewportDirty = true // 触发视图重绘（节流后）
				buf.content = ""
				buf.count = 0
				buf.lastFlush = time.Now()
			}
			return m, listenForEvents(m.eventCh)
		}

		// Fallback: 没有活跃流，创建一个（防御性处理）
		entry := logEntry{
			timestamp:     msg.event.Timestamp,
			eventType:     "ai_stream",
			from:          agentName,
			agentName:     agentName,
			streamContent: content,
			content:       content,
			streaming:     true,
			isVerbose:     false,
		}
		m.logEntries = append(m.logEntries, entry)
		idx := len(m.logEntries) - 1
		m.aiStreamActiveEntries[agentName] = idx
		m.markEntryDirty(idx)
		m.viewportDirty = true
		m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
		return m, listenForEvents(m.eventCh)
	}

	// Handle ai_stream_end — 标记流式完成
	if msg.event.Type == "ai_stream_end" {
		agentName := msg.event.From
		if agentName == "" {
			agentName = "default"
		}

		if idx, ok := m.aiStreamActiveEntries[agentName]; ok && idx >= 0 && idx < len(m.logEntries) {
			// 流结束：强制 flush 剩余累积内容，保证最终内容完整显示
			if buf := m.aiChunkBuffers[agentName]; buf != nil && buf.content != "" {
				le := &m.logEntries[idx]
				le.streamContent += buf.content
				le.content = le.streamContent
				le.clearRenderCache()
				m.markEntryDirty(idx)
				m.viewportDirty = true
				buf.content = ""
				buf.count = 0
			}
			m.logEntries[idx].streaming = false
			m.aiStreamCompletedEntries[agentName] = idx
			delete(m.aiStreamActiveEntries, agentName)
			m.markEntryDirty(idx)
		}
		delete(m.aiChunkBuffers, agentName) // 清理缓冲
		return m, listenForEvents(m.eventCh)
	}

	// Handle ai_response — 定稿：用完整内容替换流式缓冲
	if msg.event.Type == "ai_response" {
		agentName := msg.event.From
		if agentName == "" {
			agentName = "default"
		}

		// 从 Content 中提取文本内容
		content := ""
		if s, ok := msg.event.Content.(string); ok {
			content = s
		} else {
			content = fmt.Sprintf("%v", msg.event.Content)
		}

		// 优先匹配已完成的流式条目
		if idx, ok := m.aiStreamCompletedEntries[agentName]; ok && idx >= 0 && idx < len(m.logEntries) {
			le := &m.logEntries[idx]
			if content != "" {
				le.streamContent = content
				le.content = content
			}
			le.eventType = "ai_response" // 切换为 ai_response 类型，走 Glamour 渲染
			le.finalized = true
			le.streaming = false
			le.from = agentName
			le.clearRenderCache()
			m.markEntryDirty(idx)
			m.viewportDirty = true // 新增：触发视图更新
			delete(m.aiStreamCompletedEntries, agentName)
			delete(m.aiChunkBuffers, agentName) // 防御性清理残留缓冲
			return m, listenForEvents(m.eventCh)
		}

		// Fallback: 也检查 active map（ai_stream_end 丢失的情况）
		if idx, ok := m.aiStreamActiveEntries[agentName]; ok && idx >= 0 && idx < len(m.logEntries) {
			le := &m.logEntries[idx]
			if content != "" {
				le.streamContent = content
				le.content = content
			}
			le.eventType = "ai_response"
			le.finalized = true
			le.streaming = false
			le.from = agentName
			le.clearRenderCache()
			m.markEntryDirty(idx)
			m.viewportDirty = true // 新增：触发视图更新
			delete(m.aiStreamActiveEntries, agentName)
			delete(m.aiChunkBuffers, agentName) // 防御性清理残留缓冲
			return m, listenForEvents(m.eventCh)
		}

		// 无对应流式条目 → 落入通用处理（创建新条目）
		// 不 return，继续走下方逻辑
	}

	// Handle llm_call_start — create a running entry with animation (single line)
	if msg.event.Type == "llm_call_start" {
		entry := formatEventAsEntry(msg.event)
		entry.isToolRunning = true

		// Generate a unique ID for this LLM call (use agent name + timestamp)
		callID := fmt.Sprintf("llm_%s_%d", msg.event.From, msg.event.Timestamp.UnixNano())
		entry.toolCallID = callID

		// Add timeline entry for LLM call
		m.timelineEntries = append(m.timelineEntries, &TimelineEntry{
			ID:        callID,
			Kind:      TimelineKindLLMCall,
			Timestamp: msg.event.Timestamp,
			Status:    ToolStatusRunning,
			Name:      "llm_call",
			Detail:    entry.content,
		})
		m.timelineCacheKey = "" // invalidate cache

		m.logEntries = append(m.logEntries, entry)
		m.viewportDirty = true
		m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])

		// Store the entry index for llm_call_end to update
		m.llmCallActiveEntries[msg.event.From] = len(m.logEntries) - 1
		m.activeAnim = true

		return m, listenForEvents(m.eventCh)
	}

	// Handle llm_call_end — update the matching start entry (no new entry created)
	if msg.event.Type == "llm_call_end" {
		if idx, ok := m.llmCallActiveEntries[msg.event.From]; ok && idx >= 0 && idx < len(m.logEntries) {
			delete(m.llmCallActiveEntries, msg.event.From)

			// Update timeline entry for LLM call end
			for i := len(m.timelineEntries) - 1; i >= 0; i-- {
				if m.timelineEntries[i].Kind == TimelineKindLLMCall && m.timelineEntries[i].Status == ToolStatusRunning {
					var duration time.Duration
					if durationRaw, ok := msg.event.Metadata["duration_seconds"]; ok {
						if dur, ok := durationRaw.(float64); ok {
							duration = time.Duration(dur * float64(time.Second))
						}
					}
					m.timelineEntries[i].Status = ToolStatusSuccess
					m.timelineEntries[i].Duration = duration
					m.timelineEntries[i].IsError = false
					if errStr, ok := msg.event.Metadata["error"]; ok && errStr != "" {
						m.timelineEntries[i].Status = ToolStatusError
						m.timelineEntries[i].IsError = true
						m.timelineEntries[i].Detail = fmt.Sprintf("%v", errStr)
					}
					break
				}
			}
			m.timelineCacheKey = ""

			// Update the log entry with end information
			le := &m.logEntries[idx]
			le.isToolRunning = false

			// Format content like current llm_call_end with duration
			if durationRaw, ok := msg.event.Metadata["duration_seconds"]; ok {
				var duration float64
				switch v := durationRaw.(type) {
				case float64:
					duration = v
				case int:
					duration = float64(v)
				}

				modelName, _ := msg.event.Metadata["model"].(string)
				if modelName == "" {
					if m, ok := msg.event.Content.(map[string]interface{}); ok {
						modelName, _ = m["model"].(string)
					}
				}

				hasError := false
				if errStr, ok := msg.event.Metadata["error"]; ok && errStr != "" {
					hasError = true
				}

				if hasError {
					le.content = fmt.Sprintf("✗ [%s] · %.2fs", modelName, duration)
				} else {
					le.content = fmt.Sprintf("✓ [%s] · %.2fs", modelName, duration)
				}
			} else {
				le.content = "◂ LLM call completed"
			}

			le.clearRenderCache() // invalidate cache
			m.markEntryDirty(idx) // 细粒度：仅标记此条目脏
			m.updateActiveAnim()
			m.viewportDirty = true
			m.rebuildViewportScrollLock()
		}
		return m, listenForEvents(m.eventCh)
	}

	// Handle thinking — display agent thinking/reasoning content
	if msg.event.Type == "thinking" {
		// 从 event.Content 提取 thinking 文本（支持 string 和 map 两种格式）
		thinkingText := ""
		switch content := msg.event.Content.(type) {
		case string:
			thinkingText = content
		case map[string]interface{}:
			if text, ok := content["content"].(string); ok {
				thinkingText = text
			}
		}
		if thinkingText == "" {
			return m, listenForEvents(m.eventCh)
		}

		// 限制存储长度（防止超大 thinking 撑爆内存）
		maxThinkingLen := 10000
		if len(thinkingText) > maxThinkingLen {
			thinkingText = thinkingText[:maxThinkingLen] + "\n\n[...思考内容已截断...]"
		}

		// 合并逻辑：连续 thinking 事件合并到同一个 logEntry 中
		merged := false
		if n := len(m.logEntries); n > 0 && m.logEntries[n-1].eventType == "thinking" && m.logEntries[n-1].from == msg.event.From {
			lastIdx := n - 1
			last := &m.logEntries[lastIdx]
			if len(last.content)+len(thinkingText) <= 2*maxThinkingLen {
				last.content += "\n" + thinkingText
				last.timestamp = msg.event.Timestamp
				last.clearRenderCache()
				m.markEntryDirty(lastIdx)
				m.viewportDirty = true
				merged = true
			}
		}
		if !merged {
			// 创建 logEntry
			entry := logEntry{
				timestamp: msg.event.Timestamp,
				eventType: "thinking",
				from:      msg.event.From,
				content:   thinkingText,
				prefix:    "  │ ",
				isVerbose: false,
			}

			m.logEntries = append(m.logEntries, entry)
			m.viewportDirty = true
			m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
		}

		// 在 timeline 中添加条目（无论合并与否，timeline 保留每个思考事件语义）
		callID := fmt.Sprintf("thinking_%s_%d", msg.event.From, msg.event.Timestamp.UnixNano())

		// 预览：取前 80 个字符，去掉换行
		preview := thinkingText
		preview = strings.ReplaceAll(preview, "\n", " ")
		if len(preview) > 80 {
			preview = preview[:80] + "..."
		}

		m.timelineEntries = append(m.timelineEntries, &TimelineEntry{
			ID:        callID,
			Kind:      TimelineKindThinking,
			Timestamp: msg.event.Timestamp,
			Status:    ToolStatusSuccess,
			Name:      "thinking",
			Detail:    preview,
		})
		m.timelineCacheKey = ""

		return m, listenForEvents(m.eventCh)
	}

	// Intercept user_help_needed to show interactive dialog
	if msg.event.Type == "user_help_needed" {
		// Check if event has interaction_type → new format → UserHelpDialog
		// Otherwise → old format → ConfirmDialog (backward compat)
		if content, ok := msg.event.Content.(map[string]interface{}); ok {
			if _, hasInteractionType := content["interaction_type"]; hasInteractionType {
				m.openUserHelpDialog(msg.event)
			} else {
				m.openConfirmDialog(msg.event)
			}
		} else {
			m.openConfirmDialog(msg.event)
		}
		// Still log the event so it appears in the background
		entry := formatEventAsEntry(msg.event)
		m.logEntries = append(m.logEntries, entry)
		m.viewportDirty = true
		m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
		return m, listenForEvents(m.eventCh)
	}

	// Handle commit context loaded event — log it in the TUI
	if msg.event.Type == "commit_context_loaded" {
		if contentMap, ok := msg.event.Content.(map[string]interface{}); ok {
			if count, ok := contentMap["count"].(float64); ok {
				countInt := int(count)
				entry := logEntry{
					timestamp: msg.event.Timestamp,
					eventType: "commit_context",
					from:      msg.event.From,
					content:   fmt.Sprintf("📦 Loaded %d relevant commit(s) for context", countInt),
				}
				// Add timeline entry for commit context loading
				m.timelineEntries = append(m.timelineEntries, &TimelineEntry{
					ID:        fmt.Sprintf("ctx_%d", msg.event.Timestamp.UnixNano()),
					Kind:      TimelineKindContextEvent,
					Timestamp: msg.event.Timestamp,
					Status:    ToolStatusSuccess,
					Name:      "commit_context",
					Detail:    fmt.Sprintf("Loaded %d commits", countInt),
				})
				m.timelineCacheKey = ""
				m.logEntries = append(m.logEntries, entry)
				m.viewportDirty = true
				m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
			}
		}
		return m, listenForEvents(m.eventCh)
	}

	// Handle context_compressed event — log it in the TUI timeline
	if msg.event.Type == "context_compressed" {
		if contentMap, ok := msg.event.Content.(map[string]interface{}); ok {
			origTokens := parseEventInt(contentMap["original_tokens"], 0)
			compTokens := parseEventInt(contentMap["compressed_tokens"], 0)
			savedTokens := parseEventInt(contentMap["saved_tokens"], 0)
			savedPercent := 0.0
			if sp, ok := contentMap["saved_percent"].(float64); ok {
				savedPercent = sp
			}
			truncCount := parseEventInt(contentMap["truncated_count"], 0)
			var detailLines []string
			detailLines = append(detailLines, fmt.Sprintf("Context compressed: %d → %d tokens (saved %d tokens, %.1f%%)", origTokens, compTokens, savedTokens, savedPercent))
			detailLines = append(detailLines, fmt.Sprintf("Truncated %d tool result(s):", truncCount))
			if tools, ok := contentMap["truncated_tools"].([]interface{}); ok {
				for _, ti := range tools {
					if tm, ok := ti.(map[string]interface{}); ok {
						toolName := ""
						if tn, ok := tm["tool_name"].(string); ok {
							toolName = tn
						}
						oTokens := parseEventInt(tm["original_tokens"], 0)
						kTokens := parseEventInt(tm["kept_tokens"], 0)
						sTokens := parseEventInt(tm["omitted_tokens"], 0)
						detailLines = append(detailLines, fmt.Sprintf("  %s: %d → %d tokens (saved %d)", toolName, oTokens, kTokens, sTokens))
					}
				}
			}
			detail := strings.Join(detailLines, "\n")
			entry := logEntry{
				timestamp:   msg.event.Timestamp,
				eventType:   "context_compressed",
				from:        msg.event.From,
				content:     fmt.Sprintf("🧠 Context compressed: %d → %d tokens (%.1f%%)", origTokens, compTokens, savedPercent),
				isVerbose:   true,
			}
			m.timelineEntries = append(m.timelineEntries, &TimelineEntry{
				ID:        fmt.Sprintf("ctx_comp_%d", msg.event.Timestamp.UnixNano()),
				Kind:      TimelineKindContextEvent,
				Timestamp: msg.event.Timestamp,
				Status:    ToolStatusSuccess,
				Name:      "Context Compressed",
				Detail:    detail,
			})
			m.timelineCacheKey = ""
			m.logEntries = append(m.logEntries, entry)
			m.viewportDirty = true
			m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
		}
		return m, listenForEvents(m.eventCh)
	}

	// ── Tool call result: update the matching running entry ──
	if msg.event.Type == "tool_call_result" {
		callID := getToolCallIDFromEventContent(msg.event.Content)
		if callID != "" {
			if toolEntry, ok := m.toolCallEntries[callID]; ok {
				resultContent := getResultFromEventContent(msg.event.Content)
				isError := strings.HasPrefix(resultContent, "Error:")
				toolEntry.SetResult(ToolResultInfo{
					ToolCallID: callID,
					Name:       toolEntry.Call.Name,
					Content:    resultContent,
					IsError:    isError,
				})
				// Update timeline entry for tool result (search both parent and SubEntries)
				for i := len(m.timelineEntries) - 1; i >= 0; i-- {
					entry := m.timelineEntries[i]
					// Check parent entry
					if entry.ID == callID && entry.Kind == TimelineKindTool {
						entry.Status = ToolStatusSuccess
						entry.Duration = time.Since(entry.Timestamp)
						entry.IsError = isError
						if isError {
							entry.Status = ToolStatusError
						}
						entry.Detail = extractToolSummary(toolEntry.Call.Name, toolEntry.Call.Arguments)
						break
					}
					// Check SubEntries for merged tools
					found := false
					for j := range entry.SubEntries {
						if entry.SubEntries[j].ID == callID {
							entry.SubEntries[j].Status = ToolStatusSuccess
							entry.SubEntries[j].Duration = time.Since(entry.SubEntries[j].Timestamp)
							entry.SubEntries[j].IsError = isError
							if isError {
								entry.SubEntries[j].Status = ToolStatusError
							}
							entry.SubEntries[j].Detail = extractToolSummary(toolEntry.Call.Name, toolEntry.Call.Arguments)
							found = true
							break
						}
					}
					if found {
						break
					}
				}
				m.timelineCacheKey = ""
				// Update the log entry content and diff for backward compat
				if idx := findLogEntryByToolCallID(m.logEntries, callID); idx >= 0 {
					le := &m.logEntries[idx]
					le.content = resultContent
					le.isToolRunning = false
					// 提取 edit_file 的 diff 内容以便在消息面板中展示
					le.diffText = extractDiffFromResult(resultContent)
					le.clearRenderCache() // invalidate cache
					m.markEntryDirty(idx) // 细粒度：仅标记此条目脏
					m.viewportDirty = true
				}
				delete(m.toolCallEntries, callID)
				m.updateActiveAnim()
				m.viewportDirty = true
				m.rebuildViewportScrollLock()
				return m, listenForEvents(m.eventCh)
			}
		}
		// No matching start entry by callID — try matching by tool name
		// as a fallback for the most recent running entry of the same type.
		toolName := getToolNameFromEventContent(msg.event.Content)
		if toolName != "" {
			if matchedID, matchedEntry := findRunningEntryByName(m.toolCallEntries, toolName); matchedEntry != nil {
				resultContent := getResultFromEventContent(msg.event.Content)
				isError := strings.HasPrefix(resultContent, "Error:")
				matchedEntry.SetResult(ToolResultInfo{
					ToolCallID: matchedID,
					Name:       matchedEntry.Call.Name,
					Content:    resultContent,
					IsError:    isError,
				})
				// Update timeline entry for tool result (search both parent and SubEntries)
				for i := len(m.timelineEntries) - 1; i >= 0; i-- {
					entry := m.timelineEntries[i]
					// Check parent entry
					if entry.ID == matchedID && entry.Kind == TimelineKindTool {
						entry.Status = ToolStatusSuccess
						entry.Duration = time.Since(entry.Timestamp)
						entry.IsError = isError
						if isError {
							entry.Status = ToolStatusError
						}
						entry.Detail = extractToolSummary(matchedEntry.Call.Name, matchedEntry.Call.Arguments)
						break
					}
					// Check SubEntries for merged tools
					found := false
					for j := range entry.SubEntries {
						if entry.SubEntries[j].ID == matchedID {
							entry.SubEntries[j].Status = ToolStatusSuccess
							entry.SubEntries[j].Duration = time.Since(entry.SubEntries[j].Timestamp)
							entry.SubEntries[j].IsError = isError
							if isError {
								entry.SubEntries[j].Status = ToolStatusError
							}
							entry.SubEntries[j].Detail = extractToolSummary(matchedEntry.Call.Name, matchedEntry.Call.Arguments)
							found = true
							break
						}
					}
					if found {
						break
					}
				}
				m.timelineCacheKey = ""
				if idx := findLogEntryByToolCallID(m.logEntries, matchedID); idx >= 0 {
					le := &m.logEntries[idx]
					le.content = resultContent
					le.isToolRunning = false
					// 提取 edit_file 的 diff 内容以便在消息面板中展示
					le.diffText = extractDiffFromResult(resultContent)
					le.clearRenderCache()
					m.markEntryDirty(idx) // 细粒度：仅标记此条目脏
					m.viewportDirty = true
				}
				delete(m.toolCallEntries, matchedID)
				m.updateActiveAnim()
				m.viewportDirty = true
				m.rebuildViewportScrollLock()
				return m, listenForEvents(m.eventCh)
			}
		}
		// No matching start entry — add as standalone
	}

	entry := formatEventAsEntry(msg.event)

	// Track running tool calls for status transition
	if entry.eventType == "tool_call_start" && entry.toolCallID != "" {
		m.toolCallEntries[entry.toolCallID] = entry.toolEntry
		m.activeAnim = true
	}

	// Route tool_call_start to timeline
	if entry.eventType == "tool_call_start" && entry.toolCallID != "" && entry.toolEntry != nil {
		// 检查是否可与最后一个 timeline 条目合并
		merged := false
		if len(m.timelineEntries) > 0 {
			lastEntry := m.timelineEntries[len(m.timelineEntries)-1]
			if lastEntry.Kind == TimelineKindTool &&
				lastEntry.Name == entry.toolName &&
				IsMergeableTool(entry.toolName) {
				// 合併：將新調用添加為子條目
				lastEntry.SubEntries = append(lastEntry.SubEntries, &TimelineEntry{
					ID:        entry.toolCallID,
					Kind:      TimelineKindTool,
					Timestamp: entry.timestamp,
					Status:    ToolStatusRunning,
					Name:      entry.toolName,
					Detail:    entry.executionSummary,
				})
				// 更新父條目的 Duration 為從第一個到最新一個的時間跨度
				// 這樣能反映整體合併組的時間範圍
				merged = true
			}
		}

		if !merged {
			m.timelineEntries = append(m.timelineEntries, &TimelineEntry{
				ID:        entry.toolCallID,
				Kind:      TimelineKindTool,
				Timestamp: entry.timestamp,
				Status:    ToolStatusRunning,
				Name:      entry.toolName,
				Detail:    entry.executionSummary,
			})
		}
		m.timelineCacheKey = ""
	}

	m.logEntries = append(m.logEntries, entry)
	m.viewportDirty = true
	m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
	return m, listenForEvents(m.eventCh)
}

// ─────────────────────────────────────────────────────────────────────────────
// handleTaskCompleteMsg — 原 Update case taskCompleteMsg 提取
// ─────────────────────────────────────────────────────────────────────────────

func (m *model) handleTaskCompleteMsg(msg taskCompleteMsg) (tea.Model, tea.Cmd) {
	m.taskRunning = false
	m.viewportDirty = true // 新增：确保最后一次内容更新被渲染
	m.updateActiveAnim()
	if m.anim != nil {
		m.anim.Reset()
	}
	m.currentAgent = ""
	m.dialogStack.CloseDialog("confirm_dialog") // safety: close any stale dialog
	m.invalidateFooterCache()

	// 如果是用户主动取消，不显示错误弹窗或完成弹窗
	if m.taskCancelled {
		m.taskCancelled = false
		// 保留 currentTask 和其 Memory，以便用户取消后可以继续对话
		if m.currentTask != nil {
			// 重置 Context（因为旧的已被取消），以便后续 follow-up 使用
			newCtx, newCancel := context.WithCancel(context.Background())
			m.currentTask.Context = newCtx
			m.currentTask.CancelFunc = newCancel
			m.currentTask.Status = "finished" // 标记为已完成
		}
		return m, nil
	}
	m.taskCancelled = false

	if msg.err != nil {
		m.errMsg = msg.err.Error()
		m.logEntries = append(m.logEntries, logEntry{
			timestamp: time.Now(),
			eventType: "error",
			content:   msg.err.Error(),
		})
		m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
		// Show error dialog via DialogStack
		d := components.NewTaskCompleteDialog(false, "❌ Task Failed\n\n"+msg.err.Error(), components.Language(m.currentLang))
		d.SetBounds(m.termWidth, m.termHeight)
		m.dialogStack.Push(d)
	} else {
		// 保留 currentTask 以支持任务完成后继续对话
		// m.currentTask = nil  // 不再清除
		d := components.NewTaskCompleteDialog(true, "All tasks have been finished.", components.Language(m.currentLang))
		d.SetBounds(m.termWidth, m.termHeight)
		m.dialogStack.Push(d)
	}

	// 清空编辑器输入框
	m.input.SetValue("")
	return m, nil
}
