package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"yagent/internal/messaging"
	"yagent/internal/tui/components"

	tea "charm.land/bubbletea/v2"
	"yagent/internal/mcp"
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

// flushStreamBuffers 把节流缓冲里已到达、尚未落进日志的流式文本写入对应条目。
// maxAge<=0 表示无条件落地（任务收尾时用）。
//
// ai_chunk 的 300ms 超时原先只在"下一个 chunk 到达"时才被求值，所以模型一停顿、
// 或流被异常中断（没有 ai_stream_end / ai_response 来兜底）时，缓冲里的那段尾部
// 文字就再也不会显示 —— 直到从历史恢复才看得到这条完整消息。
func (m *model) flushStreamBuffers(maxAge time.Duration) {
	for agent, buf := range m.aiChunkBuffers {
		if buf == nil || buf.content == "" {
			continue
		}
		if maxAge > 0 && time.Since(buf.lastFlush) < maxAge {
			continue
		}
		idx, ok := m.aiStreamActiveEntries[agent]
		if !ok || idx < 0 || idx >= len(m.logEntries) {
			continue
		}
		le := &m.logEntries[idx]
		le.streamContent += buf.content
		le.content = le.streamContent
		buf.content = ""
		buf.count = 0
		buf.lastFlush = time.Now()
		le.clearRenderCache()
		m.markEntryDirty(idx)
		m.viewportDirty = true
	}
}

// opensDialogEvent 报告该事件是否会推送对话框（user-help 求助 / 危险操作确认）。
// 只有它们需要在已有对话框时被忽略，避免叠出第二层弹窗。
func opensDialogEvent(eventType string) bool {
	return eventType == "user_help_needed"
}

func stringFromEventMap(m map[string]interface{}, key string) string {
	s, _ := m[key].(string)
	return s
}

// addTimelineEntry 是 m.timelineEntries 唯一的追加入口。
// 之前是七处各自 append + 手写一遍 m.timelineCacheKey = ""；漏掉那一行不报错，
// 只留下"事件到了、浮层还停在旧内容"。
func (m *model) addTimelineEntry(e *TimelineEntry) {
	m.timelineEntries = append(m.timelineEntries, e)
	m.timelineCacheKey = ""
}

// timelineCoords 把事件的归属抄进条目：谁产出的、属于哪一轮委派。
func timelineCoords(event *messaging.MessageEvent) (agent, delegation string) {
	if event == nil {
		return "", ""
	}
	return event.From, event.DelegationID
}

func (m *model) handleTaskEventMsg(msg taskEventMsg) (tea.Model, tea.Cmd) {
	// 对话框打开时只拦住"会再弹一层对话框"的事件，其余照常消费：日志与时间线
	// 此时并不可见，但丢弃就是永久缺失 —— 实时视图少一条，要等从历史恢复才看得到。
	if m.dialogStack != nil && m.dialogStack.Len() > 0 && opensDialogEvent(string(msg.event.Type)) {
		if m.taskRunning {
			return m, listenForEvents(m.eventCh)
		}
		return m, nil
	}

	// 用户对确认框的回答由 TUI 自己发布，又会经消息总线回送一份：
	// 它不是 agent 的输出，落到日志里只会变成一行 map[...] 噪音。
	if msg.event.Type == "user_help_response" {
		return m, listenForEvents(m.eventCh)
	}

	// MCP 状态迁移：渲染成一行人类可读文本。不处理的话它会落到
	// formatEventAsEntry 的 default 分支，界面上就是一行 map[...]。
	if msg.event.Type == "mcp_server_status" {
		entry := logEntry{
			timestamp: msg.event.Timestamp,
			eventType: "mcp_status",
			from:      msg.event.From,
			content:   formatMCPStatusEvent(msg.event.Content),
		}
		m.logEntries = append(m.logEntries, entry)
		m.viewportDirty = true
		m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
		return m, listenForEvents(m.eventCh)
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

				agentName := msg.event.From
				if agentName == "" {
					agentName = "Unknown"
				}

				rt := m.runTokens(agentName)
				m.runAgent = agentName
				rt.InputTokens += promptVal
				rt.OutputTokens += completionVal
				rt.CacheReadInputTokens += cacheReadVal
				rt.CacheCreationInputTokens += cacheCreationVal
				rt.TotalInputTokens += totalInputVal

				// ContextTokens 是"最近一次调用的输入规模"，必须覆写而非累加：
				// 压缩事件之后，下一次调用要立刻反映压缩结果而不是历史之和。
				if totalInputVal > 0 {
					m.setContextTokens(agentName, totalInputVal)
				}

				if m.taskRunning && msg.event.From != "" {
					m.currentAgent = msg.event.From
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
		}
		// 三个发布方（executor / planner / meta）的 ai_response 总带 usage，所以这里
		// 不再按 content 长度自估 —— 那条兜底分支不可达，且口径与上游不一致。
		// 只失效不预渲染：预渲染会用上过期的 termWidth。
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

	if msg.event.Type == "model_routed" {
		if contentMap, ok := msg.event.Content.(map[string]interface{}); ok {
			agent, _ := contentMap["agent"].(string)
			tier, _ := contentMap["tier"].(string)
			routedModel, _ := contentMap["model"].(string)
			if routedModel != "" {
				m.currentModel = routedModel
			}
			line := fmt.Sprintf("routed %s → %s tier", agent, strings.ToUpper(tier))
			if routedModel != "" {
				if score, ok := contentMap["score"].(float64); ok {
					line += fmt.Sprintf(" (%s, ai_score %.1f)", routedModel, score)
				} else {
					line += fmt.Sprintf(" (%s)", routedModel)
				}
			}
			m.logEntries = append(m.logEntries, logEntry{
				timestamp: msg.event.Timestamp,
				eventType: "status",
				from:      msg.event.From,
				content:   line,
				isVerbose: true,
			})
			m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
		}
		m.invalidateFooterCache()
		return m, listenForEvents(m.eventCh)
	}

	if msg.event.Type == "ai_stream_start" {
		agentName := msg.event.From
		if agentName == "" {
			agentName = "default"
		}
		// 槽键带上委派坐标：并行委派同名 Agent 时只按名字分会把两股流粘成一段文字。
		slotKey := streamSlotKey(msg.event, agentName)

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
		m.aiStreamActiveEntries[slotKey] = idx
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
		// 槽键带上委派坐标：并行委派同名 Agent 时只按名字分会把两股流粘成一段文字。
		slotKey := streamSlotKey(msg.event, agentName)

		var content string
		if contentMap, ok := msg.event.Content.(map[string]interface{}); ok {
			if c, ok := contentMap["content"].(string); ok {
				content = c
			}
		}

		if idx, ok := m.aiStreamActiveEntries[slotKey]; ok && idx >= 0 && idx < len(m.logEntries) {
			if m.aiChunkBuffers == nil {
				m.aiChunkBuffers = make(map[string]*aiChunkBuffer)
			}
			buf := m.aiChunkBuffers[slotKey]
			if buf == nil {
				buf = &aiChunkBuffer{lastFlush: time.Now()}
				m.aiChunkBuffers[slotKey] = buf
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
		m.aiStreamActiveEntries[slotKey] = idx
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
		// 槽键带上委派坐标：并行委派同名 Agent 时只按名字分会把两股流粘成一段文字。
		slotKey := streamSlotKey(msg.event, agentName)

		if idx, ok := m.aiStreamActiveEntries[slotKey]; ok && idx >= 0 && idx < len(m.logEntries) {
			if buf := m.aiChunkBuffers[slotKey]; buf != nil && buf.content != "" {
				le := &m.logEntries[idx]
				le.streamContent += buf.content
				le.content = le.streamContent
				le.clearRenderCache()
				m.markEntryDirty(idx)
				buf.content = ""
				buf.count = 0
			}
			m.logEntries[idx].streaming = false
			m.aiStreamCompletedEntries[slotKey] = idx
			delete(m.aiStreamActiveEntries, slotKey)
			m.markEntryDirty(idx)
			m.viewportDirty = true
		}
		delete(m.aiChunkBuffers, slotKey)
		return m, tea.Batch(listenForEvents(m.eventCh), m.ensureTick())
	}

	if msg.event.Type == "ai_response" {
		agentName := msg.event.From
		if agentName == "" {
			agentName = "default"
		}
		slotKey := streamSlotKey(msg.event, agentName)

		content := ""
		if s, ok := msg.event.Content.(string); ok {
			content = s
		} else {
			content = fmt.Sprintf("%v", msg.event.Content)
		}

		if idx, ok := m.aiStreamCompletedEntries[slotKey]; ok && idx >= 0 && idx < len(m.logEntries) {
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
			delete(m.aiStreamCompletedEntries, slotKey)
			delete(m.aiChunkBuffers, slotKey)
			return m, tea.Batch(listenForEvents(m.eventCh), m.ensureTick())
		}

		if idx, ok := m.aiStreamActiveEntries[slotKey]; ok && idx >= 0 && idx < len(m.logEntries) {
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
			delete(m.aiStreamActiveEntries, slotKey)
			delete(m.aiChunkBuffers, slotKey)
			return m, tea.Batch(listenForEvents(m.eventCh), m.ensureTick())
		}

	}

	if msg.event.Type == "llm_call_start" {
		entry := formatEventAsEntry(msg.event)
		entry.isToolRunning = true

		callID := fmt.Sprintf("llm_%s_%d", msg.event.From, msg.event.Timestamp.UnixNano())
		entry.toolCallID = callID

		agent, delegation := timelineCoords(msg.event)
		m.addTimelineEntry(&TimelineEntry{
			ID:           callID,
			Kind:         TimelineKindLLMCall,
			Timestamp:    msg.event.Timestamp,
			Status:       ToolStatusRunning,
			Name:         "llm_call",
			Detail:       entry.content,
			Agent:        agent,
			DelegationID: delegation,
		})

		m.logEntries = append(m.logEntries, entry)
		m.viewportDirty = true
		m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])

		m.llmCallActiveEntries[msg.event.From] = len(m.logEntries) - 1
		m.activeAnim = true

		return m, listenForEvents(m.eventCh)
	}

	if msg.event.Type == "llm_call_end" {
		// 时长先解析、先记账：llm_call_start 被总线丢掉时，配不上那一行、
		// 日志也没得更新，但这次调用真的花了那么多时间 —— 不该因此从分布里消失。
		var llmDur time.Duration
		if durationRaw, ok := msg.event.Metadata["duration_seconds"]; ok {
			if dur, ok := durationRaw.(float64); ok {
				llmDur = time.Duration(dur * float64(time.Second))
			}
		}
		m.recordLLMLatency(msg.event, msg.event.Timestamp, llmDur)

		agent, delegation := timelineCoords(msg.event)
		if idx, ok := m.llmCallActiveEntries[msg.event.From]; ok && idx >= 0 && idx < len(m.logEntries) {
			delete(m.llmCallActiveEntries, msg.event.From)

			for i := len(m.timelineEntries) - 1; i >= 0; i-- {
				te := m.timelineEntries[i]
				// 按坐标配对，而不是"最后一条 running 的 LLMCall"：并行委派下
				// A 的 llm_call_end 会关掉 B 那一行，两条流的时长就此互串。
				if te.Kind == TimelineKindLLMCall && te.Status == ToolStatusRunning &&
					te.Agent == agent && te.DelegationID == delegation {
					te.Status = ToolStatusSuccess
					te.Duration = llmDur
					te.IsError = false
					if errStr, ok := msg.event.Metadata["error"]; ok && errStr != "" {
						te.Status = ToolStatusError
						te.IsError = true
						te.Detail = fmt.Sprintf("%v", errStr)
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

		agent, delegation := timelineCoords(msg.event)
		m.addTimelineEntry(&TimelineEntry{
			ID:           callID,
			Kind:         TimelineKindThinking,
			Timestamp:    msg.event.Timestamp,
			Status:       ToolStatusSuccess,
			Name:         "thinking",
			Detail:       preview,
			Agent:        agent,
			DelegationID: delegation,
		})

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
				agent, delegation := timelineCoords(msg.event)
				m.addTimelineEntry(&TimelineEntry{
					ID:           fmt.Sprintf("ctx_%d", msg.event.Timestamp.UnixNano()),
					Kind:         TimelineKindContextEvent,
					Timestamp:    msg.event.Timestamp,
					Status:       ToolStatusSuccess,
					Name:         "commit_context",
					Detail:       fmt.Sprintf("Loaded %d commits", countInt),
					Agent:        agent,
					DelegationID: delegation,
				})
				m.logEntries = append(m.logEntries, entry)
				m.viewportDirty = true
				m.appendLogEntry(&m.logEntries[len(m.logEntries)-1])
			}
		}
		return m, listenForEvents(m.eventCh)
	}

	if msg.event.Type == "context_compressed" ||
		msg.event.Type == "context_emergency_compressed" ||
		msg.event.Type == "context_ultimate_compressed" {
		if contentMap, ok := msg.event.Content.(map[string]interface{}); ok {
			if compTokens := parseEventInt(contentMap["compressed_tokens"], 0); compTokens > 0 {
				m.setContextTokens(msg.event.From, int64(compTokens))
			}
			// 三档的累计效果只在这个共享分支统计一次（它不 return）；
			// 下面各档自己的分支只写日志与时间线，不再碰计数器。
			m.recordCompression(string(msg.event.Type), contentMap)
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
			// 两个发布方给的是 []map[string]interface{}，只断言 []interface{} 会让这段
			// 明细在真机上永远只剩一行 "Truncated N tool result(s):" 后面空着。
			for _, tm := range compressionToolEntries(contentMap) {
				toolName := ""
				if tn, ok := tm["tool_name"].(string); ok {
					toolName = tn
				}
				oTokens := parseEventInt(tm["original_tokens"], 0)
				kTokens := parseEventInt(tm["kept_tokens"], 0)
				sTokens := parseEventInt(tm["omitted_tokens"], 0)
				detailLines = append(detailLines, fmt.Sprintf("  %s: %d → %d tokens (saved %d)", toolName, oTokens, kTokens, sTokens))
			}
			detail := strings.Join(detailLines, "\n")
			entry := logEntry{
				timestamp: msg.event.Timestamp,
				eventType: "context_compressed",
				from:      msg.event.From,
				content:   fmt.Sprintf("🧠 Context compressed: %d → %d tokens (%.1f%%)", origTokens, compTokens, savedPercent),
				isVerbose: true,
			}
			agent, delegation := timelineCoords(msg.event)
			m.addTimelineEntry(&TimelineEntry{
				ID:           fmt.Sprintf("ctx_comp_%d", msg.event.Timestamp.UnixNano()),
				Kind:         TimelineKindContextEvent,
				Timestamp:    msg.event.Timestamp,
				Status:       ToolStatusSuccess,
				Name:         "Context Compressed",
				Detail:       detail,
				Agent:        agent,
				DelegationID: delegation,
			})
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
			agent, delegation := timelineCoords(msg.event)
			m.addTimelineEntry(&TimelineEntry{
				ID:           fmt.Sprintf("ctx_emg_%d", msg.event.Timestamp.UnixNano()),
				Kind:         TimelineKindContextEvent,
				Timestamp:    msg.event.Timestamp,
				Status:       ToolStatusSuccess,
				Name:         "Emergency Compression",
				Agent:        agent,
				DelegationID: delegation,
				Detail: fmt.Sprintf("Context: %d → %d tokens (saved %d)",
					origTokens, compTokens, savedTokens),
			})
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
			agent, delegation := timelineCoords(msg.event)
			m.addTimelineEntry(&TimelineEntry{
				ID:           fmt.Sprintf("ctx_ult_%d", msg.event.Timestamp.UnixNano()),
				Kind:         TimelineKindContextEvent,
				Timestamp:    msg.event.Timestamp,
				Status:       ToolStatusSuccess,
				Name:         "Ultimate Compression",
				Agent:        agent,
				DelegationID: delegation,
				Detail: strings.Join([]string{
					detailText,
					fmt.Sprintf("Context: %d → %d tokens (saved %d)", origTokens, compTokens, savedTokens),
					fmt.Sprintf("Plans kept: %d/%d, user inputs: %d, truncated: %v", keptPlans, totalPlans, userInputs, truncated),
				}, "\n"),
			})
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
				Agent:   stringFromEventMap(contentMap, "agent"),
				Content: stringFromEventMap(contentMap, "content"),
			}
			if e.Agent == "" {
				// 事件来源即发布方 Agent 名（Publish 的 from 参数）
				e.Agent = msg.event.Source
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
						m.recordToolLatency(msg.event, callID, toolEntry.Call.Name, entry.Timestamp, entry.Duration)
						break
					}
					found := false
					for j := range entry.SubEntries {
						if entry.SubEntries[j].ID == callID {
							sub := entry.SubEntries[j]
							sub.Status = ToolStatusSuccess
							sub.Duration = time.Since(sub.Timestamp)
							sub.IsError = isError
							if isError {
								sub.Status = ToolStatusError
							}
							sub.Detail = extractToolSummary(toolEntry.Call.Name, toolEntry.Call.Arguments)
							// 合并组里每一条都要单独进窗口：只给整组记一次的话，
							// "近 20 次"会变成"近 20 组"，分布的分母就不是事件数了。
							m.recordToolLatency(msg.event, callID, toolEntry.Call.Name, sub.Timestamp, sub.Duration)
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
						m.recordToolLatency(msg.event, matchedID, matchedEntry.Call.Name, entry.Timestamp, entry.Duration)
						break
					}
					found := false
					for j := range entry.SubEntries {
						if entry.SubEntries[j].ID == matchedID {
							sub := entry.SubEntries[j]
							sub.Status = ToolStatusSuccess
							sub.Duration = time.Since(sub.Timestamp)
							sub.IsError = isError
							if isError {
								sub.Status = ToolStatusError
							}
							sub.Detail = extractToolSummary(matchedEntry.Call.Name, matchedEntry.Call.Arguments)
							m.recordToolLatency(msg.event, matchedID, matchedEntry.Call.Name, sub.Timestamp, sub.Duration)
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
		agent, delegation := timelineCoords(msg.event)
		merged := false
		if len(m.timelineEntries) > 0 {
			lastEntry := m.timelineEntries[len(m.timelineEntries)-1]
			// 合并还要求同一 Agent、同一轮委派：并行委派下两个 Coding 的 read_file
			// 会串成同一个节点，而那两件事对"哪一轮慢"是完全不同的答案。
			if lastEntry.Kind == TimelineKindTool &&
				lastEntry.Name == entry.toolName &&
				lastEntry.Agent == agent &&
				lastEntry.DelegationID == delegation &&
				IsMergeableTool(entry.toolName) {
				lastEntry.SubEntries = append(lastEntry.SubEntries, &TimelineEntry{
					ID:           entry.toolCallID,
					Kind:         TimelineKindTool,
					Timestamp:    entry.timestamp,
					Status:       ToolStatusRunning,
					Name:         entry.toolName,
					Detail:       entry.executionSummary,
					Agent:        agent,
					DelegationID: delegation,
				})
				merged = true
			}
		}

		if !merged {
			m.addTimelineEntry(&TimelineEntry{
				ID:           entry.toolCallID,
				Kind:         TimelineKindTool,
				Timestamp:    entry.timestamp,
				Status:       ToolStatusRunning,
				Name:         entry.toolName,
				Detail:       entry.executionSummary,
				Agent:        agent,
				DelegationID: delegation,
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
	// 收尾这一刻把 total 定格（含把"正停着"那段先结算掉）。不定格的话空闲态
	// 每秒仍在重画整段面板，而停 tick 的条件永远不满足。
	m.freezeTotal(time.Now())
	m.viewportDirty = true
	// 任务结束（含取消/异常）后 tick 链可能就此停下，缓冲里的尾部文字再无机会落地。
	m.flushStreamBuffers(0)
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
			// 旧 cancel 必须先调用再替换：直接覆盖的话那份 ctx 再也没有人能给它的
			// 上游取消（它挂在 context.Background() 下），于是每取消一次任务就漏一个
			// 永远活着的 context —— 不报错，只是内存慢慢涨。
			if m.currentTask.CancelFunc != nil {
				m.currentTask.CancelFunc()
			}
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

// streamSlotKey 是流式缓冲 / 活动条目 / 已完成条目三张表共用的键。
//
// 只按 Agent 名分槽在并行委派下会把两股流粘成一段文字：两个 Coding 同时输出时
// 它们的 ai_chunk 都落到同一个 buf，而 buf 绑的是先建起来的那条 logEntry。
// 委派坐标（delegate_* 的 tool_call_id）由 C5 起随事件信封带下来。
//
// 坐标为空时退回按名字 —— Director 自己的输出、以及 /resume 恢复出来的存量
// rollout（旧事件没有这个字段）都走这条路，与改动前逐字一致。
// 注意：这个键**不能**当显示名用（le.from / currentAgent 一律用 agentName）。
func streamSlotKey(event *messaging.MessageEvent, agentName string) string {
	if event == nil || event.DelegationID == "" {
		return agentName
	}
	return agentName + "#" + event.DelegationID
}

// formatMCPStatusEvent 把 mcp_server_status 的 payload 渲染成一行。
// 措辞必须与 Manager.StatusLines 同源：这里只把 payload 还原成 ServerHealth，
// 真正拼字符串的是 mcp.FormatServerLine（各写一份就是同一件事两种说法）。
func formatMCPStatusEvent(content interface{}) string {
	m, ok := content.(map[string]interface{})
	if !ok {
		return "mcp: status update"
	}
	h := mcp.ServerHealth{
		Name:        stringFromEventMap(m, "server"),
		State:       mcp.ServerStateFromString(stringFromEventMap(m, "state")),
		Error:       stringFromEventMap(m, "error"),
		Attempt:     intFromEventMap(m, "attempt"),
		MaxAttempts: intFromEventMap(m, "max_attempts"),
		Tools:       intFromEventMap(m, "tools"),
		Curated:     intFromEventMap(m, "curated"),
	}
	return mcp.FormatServerLine(h)
}

// intFromEventMap 读一个数值字段。总线上的数字可能是 int（进程内直传）
// 或 float64（过了一遍 JSON），两种都要认。
func intFromEventMap(m map[string]interface{}, key string) int {
	switch v := m[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	default:
		return 0
	}
}
