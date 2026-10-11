package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"yagent/internal/tui/components"

	tea "charm.land/bubbletea/v2"
)

const (
	aiChunkRenderThreshold = 5
	aiChunkFlushMaxBytes   = 64
	aiChunkFlushInterval   = 300 * time.Millisecond
)

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

func stringFromEventMap(m map[string]interface{}, key string) string {
	s, _ := m[key].(string)
	return s
}

func (m *model) updateAgentContextTokens(agentName string, contextTokens int64) {
	if agentName == "" {
		agentName = "Unknown"
	}
	if m.currentAgentRunTokens.AgentName != agentName {
		m.currentAgentRunTokens = AgentRunTokens{
			AgentName: agentName,
		}
	}
	m.currentAgentRunTokens.ContextTokens = contextTokens
	m.invalidateFooterCache()
}

func (m *model) handleTaskEventMsg(msg taskEventMsg) (tea.Model, tea.Cmd) {
	if m.dialogStack != nil && m.dialogStack.Len() > 0 {
		if msg.event.Type == "ai_stream_end" || msg.event.Type == "ai_response" {
		} else if m.taskRunning {
			return m, listenForEvents(m.eventCh)
		} else {
			return m, nil
		}
	}

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
					totalInputVal = promptVal + cacheReadVal + cacheCreationVal
				}
				m.totalInputTokens += totalInputVal

				agentNameForRun := msg.event.From
				if agentNameForRun == "" {
					agentNameForRun = "Unknown"
				}
				if m.currentAgentRunTokens.AgentName != agentNameForRun {
					m.currentAgentRunTokens = AgentRunTokens{
						AgentName: agentNameForRun,
					}
				}
				m.currentAgentRunTokens.InputTokens += promptVal
				m.currentAgentRunTokens.OutputTokens += completionVal
				m.currentAgentRunTokens.CacheReadInputTokens += cacheReadVal
				m.currentAgentRunTokens.CacheCreationInputTokens += cacheCreationVal
				m.currentAgentRunTokens.TotalInputTokens += totalInputVal

				if totalInputVal > 0 {
					m.updateAgentContextTokens(agentNameForRun, totalInputVal)
				}

				if m.taskRunning {
					if msg.event.From != "" {
						m.currentAgent = msg.event.From
					}
				}

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
			contentStr := fmt.Sprintf("%v", msg.event.Content)
			estimatedOutput := int64(len(contentStr) / 4)
			if estimatedOutput > 0 && m.tokenUsagePerAgent != nil {
				m.outputTokens += estimatedOutput

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
		m.cachedTokenDashboard = m.renderTokenDashboard()
		m.tokenDashboardValid = true
		m.invalidateFooterCache()
	}

	if msg.event.Type == "model_info" {
		if contentMap, ok := msg.event.Content.(map[string]interface{}); ok {
			if modelName, ok := contentMap["model"].(string); ok {
				m.currentModel = modelName
			}
			if agentName, ok := contentMap["agent"].(string); ok && agentName != "" {
				m.currentAgent = agentName
			}
			if providerName, ok := contentMap["provider"].(string); ok && providerName != "" {
				m.currentProvider = providerName
				m.refreshContextWindow()
			}
		}
		m.invalidateFooterCache()
		return m, listenForEvents(m.eventCh)
	}

	if msg.event.Type == "ai_stream_start" {
		agentName := msg.event.From
		if agentName == "" {
			agentName = "default"
		}

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

	if msg.event.Type == "ai_chunk" {
		agentName := msg.event.From
		if agentName == "" {
			agentName = "default"
		}

		var content string
		if contentMap, ok := msg.event.Content.(map[string]interface{}); ok {
			if c, ok := contentMap["content"].(string); ok {
				content = c
			}
		}

		if idx, ok := m.aiStreamActiveEntries[agentName]; ok && idx >= 0 && idx < len(m.logEntries) {
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

			shouldFlush := buf.count >= aiChunkRenderThreshold ||
				len(buf.content) >= aiChunkFlushMaxBytes ||
				(buf.count > 0 && time.Since(buf.lastFlush) >= aiChunkFlushInterval)
			if shouldFlush {
				le := &m.logEntries[idx]
				le.streamContent += buf.content
				le.content = le.streamContent
				le.clearRenderCache()
				m.markEntryDirty(idx)
				m.viewportDirty = true
				buf.content = ""
				buf.count = 0
				buf.lastFlush = time.Now()
			}
			return m, listenForEvents(m.eventCh)
		}

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

	if msg.event.Type == "ai_stream_end" {
		agentName := msg.event.From
		if agentName == "" {
			agentName = "default"
		}

		if idx, ok := m.aiStreamActiveEntries[agentName]; ok && idx >= 0 && idx < len(m.logEntries) {
			if buf := m.aiChunkBuffers[agentName]; buf != nil && buf.content != "" {
				le := &m.logEntries[idx]
				le.streamContent += buf.content
				le.content = le.streamContent
				le.clearRenderCache()
				m.markEntryDirty(idx)
				buf.content = ""
				buf.count = 0
			}
			m.logEntries[idx].streaming = false
			m.aiStreamCompletedEntries[agentName] = idx
			delete(m.aiStreamActiveEntries, agentName)
			m.markEntryDirty(idx)
			m.viewportDirty = true
		}
		delete(m.aiChunkBuffers, agentName)
		return m, tea.Batch(listenForEvents(m.eventCh), tickCmd())
	}

	if msg.event.Type == "ai_response" {
		agentName := msg.event.From
		if agentName == "" {
			agentName = "default"
		}

		content := ""
		if s, ok := msg.event.Content.(string); ok {
			content = s
		} else {
			content = fmt.Sprintf("%v", msg.event.Content)
		}

		if idx, ok := m.aiStreamCompletedEntries[agentName]; ok && idx >= 0 && idx < len(m.logEntries) {
			le := &m.logEntries[idx]
			if content == "" && le.streamContent != "" {
				content = le.streamContent
			}
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
			m.viewportDirty = true
			delete(m.aiStreamCompletedEntries, agentName)
			delete(m.aiChunkBuffers, agentName)
			return m, tea.Batch(listenForEvents(m.eventCh), tickCmd())
		}

		if idx, ok := m.aiStreamActiveEntries[agentName]; ok && idx >= 0 && idx < len(m.logEntries) {
			le := &m.logEntries[idx]
			if content == "" && le.streamContent != "" {
				content = le.streamContent
			}
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
			m.viewportDirty = true
			delete(m.aiStreamActiveEntries, agentName)
			delete(m.aiChunkBuffers, agentName)
			return m, tea.Batch(listenForEvents(m.eventCh), tickCmd())
		}

	}

	if msg.event.Type == "llm_call_start" {
		entry := formatEventAsEntry(msg.event)
		entry.isToolRunning = true

		callID := fmt.Sprintf("llm_%s_%d", msg.event.From, msg.event.Timestamp.UnixNano())
		entry.toolCallID = callID

		m.timelineEntries = append(m.timelineEntries, &TimelineEntry{
			ID:        callID,
			Kind:      TimelineKindLLMCall,
			Timestamp: msg.event.Timestamp,
			Status:    ToolStatusRunning,
			Name:      "llm_call",
			Detail:    entry.content,
		})
		m.timelineCacheKey = ""

		m.logEntries = append(m.logEntries, entry)
		m.viewportDirty = true
		m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])

		m.llmCallActiveEntries[msg.event.From] = len(m.logEntries) - 1
		m.activeAnim = true

		return m, listenForEvents(m.eventCh)
	}

	if msg.event.Type == "llm_call_end" {
		if idx, ok := m.llmCallActiveEntries[msg.event.From]; ok && idx >= 0 && idx < len(m.logEntries) {
			delete(m.llmCallActiveEntries, msg.event.From)

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

			le := &m.logEntries[idx]
			le.isToolRunning = false

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

			le.clearRenderCache()
			m.markEntryDirty(idx)
			m.updateActiveAnim()
			m.viewportDirty = true
			m.rebuildViewportScrollLock()
		}
		return m, listenForEvents(m.eventCh)
	}

	if msg.event.Type == "thinking" {
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

		maxThinkingLen := 10000
		if len(thinkingText) > maxThinkingLen {
			thinkingText = thinkingText[:maxThinkingLen] + "\n\n[...思考内容已截断...]"
		}

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

		callID := fmt.Sprintf("thinking_%s_%d", msg.event.From, msg.event.Timestamp.UnixNano())

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

	if msg.event.Type == "user_help_needed" {
		var focusCmd tea.Cmd
		if content, ok := msg.event.Content.(map[string]interface{}); ok {
			if _, hasInteractionType := content["interaction_type"]; hasInteractionType {
				focusCmd = m.openUserHelpDialog(msg.event)
			} else {
				m.openConfirmDialog(msg.event)
			}
		} else {
			m.openConfirmDialog(msg.event)
		}
		entry := formatEventAsEntry(msg.event)
		m.logEntries = append(m.logEntries, entry)
		m.viewportDirty = true
		m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
		return m, tea.Batch(listenForEvents(m.eventCh), focusCmd)
	}

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

	if msg.event.Type == "knowledge_injected" {
		if contentMap, ok := msg.event.Content.(map[string]interface{}); ok {
			agent := ""
			if a, ok := contentMap["agent"].(string); ok {
				agent = a
			}
			query := ""
			if q, ok := contentMap["query"].(string); ok {
				query = q
			}
			hits := parseEventInt(contentMap["hits"], 0)
			maxScore := 0.0
			if ms, ok := contentMap["max_score"].(float64); ok {
				maxScore = ms
			}
			var titles []string
			if ts, ok := contentMap["titles"].([]interface{}); ok {
				for _, ti := range ts {
					if s, ok := ti.(string); ok {
						titles = append(titles, s)
					}
				}
			}
			entry := logEntry{
				timestamp: msg.event.Timestamp,
				eventType: "knowledge_injected",
				from:      msg.event.From,
			}
			if hits > 0 {
				content := fmt.Sprintf("📚 知识注入[%s]：%d 条命中 | 最高分 %.2f", agent, hits, maxScore)
				if len(titles) > 0 {
					content += " | " + strings.Join(titles, "、")
				}
				entry.content = content
			} else {
				entry.isVerbose = true
				entry.content = fmt.Sprintf("🔍 知识检索无命中[%s] | query=%s", agent, query)
			}
			m.timelineEntries = append(m.timelineEntries, &TimelineEntry{
				ID:        fmt.Sprintf("knowledge_%d", msg.event.Timestamp.UnixNano()),
				Kind:      TimelineKindContextEvent,
				Timestamp: msg.event.Timestamp,
				Status:    ToolStatusSuccess,
				Name:      "knowledge_injected",
				Detail:    entry.content,
			})
			m.timelineCacheKey = ""
			m.logEntries = append(m.logEntries, entry)
			m.viewportDirty = true
			m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
		}
		return m, listenForEvents(m.eventCh)
	}

	if msg.event.Type == "context_compressed" ||
		msg.event.Type == "context_emergency_compressed" ||
		msg.event.Type == "context_ultimate_compressed" {
		if contentMap, ok := msg.event.Content.(map[string]interface{}); ok {
			if compTokens := parseEventInt(contentMap["compressed_tokens"], 0); compTokens > 0 {
				m.updateAgentContextTokens(msg.event.From, int64(compTokens))
			}
		}
	}

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
				timestamp: msg.event.Timestamp,
				eventType: "context_compressed",
				from:      msg.event.From,
				content:   fmt.Sprintf("🧠 Context compressed: %d → %d tokens (%.1f%%)", origTokens, compTokens, savedPercent),
				isVerbose: true,
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

	if msg.event.Type == "context_emergency_compressed" {
		if contentMap, ok := msg.event.Content.(map[string]interface{}); ok {
			origTokens := parseEventInt(contentMap["original_tokens"], 0)
			compTokens := parseEventInt(contentMap["compressed_tokens"], 0)
			savedTokens := parseEventInt(contentMap["saved_tokens"], 0)
			entry := logEntry{
				timestamp: msg.event.Timestamp,
				eventType: "context_emergency_compressed",
				from:      msg.event.From,
				content:   fmt.Sprintf("⚡ Emergency compression: %d → %d tokens (saved %d)", origTokens, compTokens, savedTokens),
				isVerbose: true,
			}
			m.timelineEntries = append(m.timelineEntries, &TimelineEntry{
				ID:        fmt.Sprintf("ctx_emg_%d", msg.event.Timestamp.UnixNano()),
				Kind:      TimelineKindContextEvent,
				Timestamp: msg.event.Timestamp,
				Status:    ToolStatusSuccess,
				Name:      "Emergency Compression",
				Detail: fmt.Sprintf("Context: %d → %d tokens (saved %d)",
					origTokens, compTokens, savedTokens),
			})
			m.timelineCacheKey = ""
			m.logEntries = append(m.logEntries, entry)
			m.viewportDirty = true
			m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
		}
		return m, listenForEvents(m.eventCh)
	}

	if msg.event.Type == "context_ultimate_compressed" {
		if contentMap, ok := msg.event.Content.(map[string]interface{}); ok {
			origTokens := parseEventInt(contentMap["original_tokens"], 0)
			compTokens := parseEventInt(contentMap["compressed_tokens"], 0)
			savedTokens := parseEventInt(contentMap["saved_tokens"], 0)
			totalPlans := parseEventInt(contentMap["total_plans"], 0)
			keptPlans := parseEventInt(contentMap["kept_plans"], 0)
			userInputs := parseEventInt(contentMap["user_inputs"], 0)
			truncated, _ := contentMap["truncated"].(bool)
			detailText, _ := contentMap["detail"].(string)

			entry := logEntry{
				timestamp: msg.event.Timestamp,
				eventType: "context_ultimate_compressed",
				from:      msg.event.From,
				content: fmt.Sprintf("⚡ Ultimate compression: %d → %d tokens, kept %d/%d plans, user_inputs=%d, truncated=%v",
					origTokens, compTokens, keptPlans, totalPlans, userInputs, truncated),
				isVerbose: true,
			}
			m.timelineEntries = append(m.timelineEntries, &TimelineEntry{
				ID:        fmt.Sprintf("ctx_ult_%d", msg.event.Timestamp.UnixNano()),
				Kind:      TimelineKindContextEvent,
				Timestamp: msg.event.Timestamp,
				Status:    ToolStatusSuccess,
				Name:      "Ultimate Compression",
				Detail: strings.Join([]string{
					detailText,
					fmt.Sprintf("Context: %d → %d tokens (saved %d)", origTokens, compTokens, savedTokens),
					fmt.Sprintf("Plans kept: %d/%d, user inputs: %d, truncated: %v", keptPlans, totalPlans, userInputs, truncated),
				}, "\n"),
			})
			m.timelineCacheKey = ""
			m.logEntries = append(m.logEntries, entry)
			m.viewportDirty = true
			m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
		}
		return m, listenForEvents(m.eventCh)
	}

	if msg.event.Type == "thinklink_entry" {
		if contentMap, ok := msg.event.Content.(map[string]interface{}); ok {
			e := ThinklinkEntry{
				ID:      stringFromEventMap(contentMap, "id"),
				Kind:    stringFromEventMap(contentMap, "kind"),
				Content: stringFromEventMap(contentMap, "content"),
			}
			if tsStr, ok := contentMap["timestamp"].(string); ok {
				if parsed, err := time.Parse(time.RFC3339Nano, tsStr); err == nil {
					e.Timestamp = parsed
				} else {
					e.Timestamp = msg.event.Timestamp
				}
			} else {
				e.Timestamp = msg.event.Timestamp
			}
			e.Step = parseEventInt(contentMap["step"], 0)

			if e.Content == "" {
				slog.Debug("TUI: dropped malformed thinklink_entry event",
					"id", e.ID, "kind", e.Kind)
			} else {
				m.thinklinkEntries = append(m.thinklinkEntries, e)
			}
		} else {
			slog.Debug("TUI: dropped thinklink_entry event with unexpected payload type")
		}
		return m, listenForEvents(m.eventCh)
	}

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
				for i := len(m.timelineEntries) - 1; i >= 0; i-- {
					entry := m.timelineEntries[i]
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
				if idx := findLogEntryByToolCallID(m.logEntries, callID); idx >= 0 {
					le := &m.logEntries[idx]
					le.content = resultContent
					le.isToolRunning = false
					le.diffText = extractDiffFromResult(resultContent)
					le.clearRenderCache()
					m.markEntryDirty(idx)
					m.viewportDirty = true
				}
				delete(m.toolCallEntries, callID)
				m.updateActiveAnim()
				m.viewportDirty = true
				m.rebuildViewportScrollLock()
				return m, listenForEvents(m.eventCh)
			}
		}
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
				for i := len(m.timelineEntries) - 1; i >= 0; i-- {
					entry := m.timelineEntries[i]
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
					le.diffText = extractDiffFromResult(resultContent)
					le.clearRenderCache()
					m.markEntryDirty(idx)
					m.viewportDirty = true
				}
				delete(m.toolCallEntries, matchedID)
				m.updateActiveAnim()
				m.viewportDirty = true
				m.rebuildViewportScrollLock()
				return m, listenForEvents(m.eventCh)
			}
		}
	}

	entry := formatEventAsEntry(msg.event)

	if entry.eventType == "tool_call_start" && entry.toolCallID != "" {
		m.toolCallEntries[entry.toolCallID] = entry.toolEntry
		m.activeAnim = true
	}

	if entry.eventType == "tool_call_start" && entry.toolCallID != "" && entry.toolEntry != nil {
		merged := false
		if len(m.timelineEntries) > 0 {
			lastEntry := m.timelineEntries[len(m.timelineEntries)-1]
			if lastEntry.Kind == TimelineKindTool &&
				lastEntry.Name == entry.toolName &&
				IsMergeableTool(entry.toolName) {
				lastEntry.SubEntries = append(lastEntry.SubEntries, &TimelineEntry{
					ID:        entry.toolCallID,
					Kind:      TimelineKindTool,
					Timestamp: entry.timestamp,
					Status:    ToolStatusRunning,
					Name:      entry.toolName,
					Detail:    entry.executionSummary,
				})
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

func (m *model) handleTaskCompleteMsg(msg taskCompleteMsg) (tea.Model, tea.Cmd) {
	m.taskRunning = false
	m.viewportDirty = true
	m.updateActiveAnim()
	if m.anim != nil {
		m.anim.Reset()
	}
	m.currentAgent = ""
	m.dialogStack.CloseDialog("confirm_dialog")
	m.invalidateFooterCache()

	if m.taskCancelled {
		m.taskCancelled = false
		if m.currentTask != nil {
			newCtx, newCancel := context.WithCancel(context.Background())
			m.currentTask.Context = newCtx
			m.currentTask.CancelFunc = newCancel
			m.currentTask.Status = "finished"
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
		d := components.NewTaskCompleteDialog(false, "❌ Task Failed\n\n"+msg.err.Error(), components.Language(m.currentLang))
		d.SetBounds(m.termWidth, m.termHeight)
		m.dialogStack.Push(d)
	} else {
		d := components.NewTaskCompleteDialog(true, "All tasks have been finished.", components.Language(m.currentLang))
		d.SetBounds(m.termWidth, m.termHeight)
		m.dialogStack.Push(d)
	}

	m.input.SetValue("")
	return m, nil
}
