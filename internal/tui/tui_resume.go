package tui

// tui_resume.go — /resume（别名 /rs，兼容 :resume/:rs）全屏会话恢复模式（范式对齐 tui_history.go）。
//
// 职责：
//   - 列举 rollout 会话文件（memory.ListRolloutSessions，mtime 倒序）并全屏展示；
//   - Enter 异步恢复选中会话（memory.ReadRolloutMemory），落地为可续聊任务
//     （taskManager upsert + m.currentTask + viewport 重建），复用
//     submitFollowUp → executeFollowUpCmd(m.currentTask.ID + Memory) 续聊；
//   - applyRestoredMemory 由 history 与 resume 两条恢复路径共用
//     （行为等价迁移自原 restoreSession 的 memory→TUI 落地段）。

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"yagent/internal/compression"
	"yagent/internal/http"
	"yagent/internal/memory"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ── Resume 样式缓存 ──

// resumeStyles 预计算的样式集合，避免每帧重建（模式对齐 historyStyles）。
type resumeStyles struct {
	badgeBgColor   string         // 选中行背景色 (lipgloss.Color string)
	indicatorStyle lipgloss.Style // 选中指示器 "● "
	dateStyle      lipgloss.Style // 日期样式
	titleStyle     lipgloss.Style // 标题样式
	spacerStyle    lipgloss.Style // 空白填充
	normalStyle    lipgloss.Style // 普通状态
	dimStyle       lipgloss.Style // 弱化文本（详情行等）
	agentStyle     lipgloss.Style // agent 徽标
}

// initResumeStyles 预创建样式对象（在 enterResumeMode 中初始化）。
func (m *model) initResumeStyles() *resumeStyles {
	bgColor := lipgloss.Color("36") // 青绿色（与 history 的蓝色 39 区分模式）
	fgColor := lipgloss.Color("0")  // 黑色
	dimFg := lipgloss.Color("245")
	normalFg := lipgloss.Color("252")
	agentFg := lipgloss.Color("80")

	return &resumeStyles{
		badgeBgColor: "36",
		indicatorStyle: lipgloss.NewStyle().
			Background(bgColor).
			Foreground(fgColor).
			Bold(true),
		dateStyle: lipgloss.NewStyle().
			Background(bgColor).
			Foreground(fgColor).
			Bold(true),
		titleStyle: lipgloss.NewStyle().
			Background(bgColor).
			Foreground(fgColor).
			Bold(true),
		spacerStyle: lipgloss.NewStyle().
			Foreground(dimFg),
		normalStyle: lipgloss.NewStyle().
			Foreground(normalFg),
		dimStyle: lipgloss.NewStyle().
			Foreground(dimFg),
		agentStyle: lipgloss.NewStyle().
			Foreground(agentFg),
	}
}

// ── Resume message types ──

// resumeSessionsMsg is sent when async rollout session listing completes.
type resumeSessionsMsg struct {
	items []memory.RolloutSessionInfo
	err   error
}

// rolloutRestoreMsg is sent when async rollout memory restoring completes.
type rolloutRestoreMsg struct {
	mem  *memory.ConversationMemory
	info memory.RolloutInfo
	err  error
	path string
	// agentName 来自会话清单的文件名 agent 段（主会话为空）：rollout 内容里没有
	// agent 身份，而 token 记账与状态栏都需要它。
	agentName string
}

// ── Resume Update ──

// resumeUpdate processes all messages in resume mode.
func resumeUpdate(msg tea.Msg, m *model) (*model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return resumeHandleKey(msg, m)

	case resumeSessionsMsg:
		m.resumeItems = msg.items
		m.resumeCursor = 0
		m.resumeOffset = 0
		m.resumeLoading = false
		m.resumeErr = ""
		if msg.err != nil {
			m.resumeErr = fmt.Sprintf("List rollout sessions: %v", msg.err)
		}
		if len(msg.items) == 0 && msg.err == nil {
			m.infoMsg = "No rollout sessions found"
		}
		return m, nil

	case rolloutRestoreMsg:
		m.resumeLoading = false
		if msg.err != nil {
			m.resumeErr = fmt.Sprintf("Restore session: %v", msg.err)
			return m, nil
		}
		if msg.mem == nil || len(msg.mem.Messages) == 0 {
			// 防御（ReadRolloutMemory 契约下理论不可达）：留在列表展示错误
			m.resumeErr = "No messages in this session"
			return m, nil
		}
		// 成功：落地为可续聊任务并退出 resume 模式
		//（顺序对齐 historyUpdate 的 restoreSession + exitHistoryMode）
		restoreFromRollout(m, msg.mem, msg.info, msg.path, msg.agentName)
		exitResumeMode(m)
		return m, nil

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		return m, nil

	default:
		return m, nil
	}
}

// resumeHandleKey processes keyboard input in resume mode.
func resumeHandleKey(key tea.KeyMsg, m *model) (*model, tea.Cmd) {
	k := key.String()

	// Exit resume mode on escape/q/ctrl+c
	if k == "esc" || k == "q" || k == "ctrl+c" {
		exitResumeMode(m)
		return m, nil
	}

	// Skip all other keys while loading (esc already handled above)
	if m.resumeLoading {
		return m, nil
	}

	// Navigation
	switch k {
	case "j", "down":
		// Cursor down within current page (no auto-page)
		_, endIdx := m.resumeVisibleRange()
		visibleCount := endIdx - m.resumeOffset
		if visibleCount > 0 && m.resumeCursor < visibleCount-1 {
			m.resumeCursor++
		}
		return m, nil

	case "k", "up":
		// Cursor up within current page (no auto-page)
		if m.resumeCursor > 0 {
			m.resumeCursor--
		}
		return m, nil

	case "pgdown", "pgdn", "pagedown", "ctrl+d":
		// Next page
		pageSize := m.resumePageSize
		if pageSize <= 0 {
			pageSize = defaultPageSize
		}
		if m.resumeOffset+pageSize < len(m.resumeItems) {
			m.resumeOffset += pageSize
			m.resumeCursor = 0
		}
		return m, nil

	case "pgup", "pageup", "ctrl+u":
		// Previous page
		pageSize := m.resumePageSize
		if pageSize <= 0 {
			pageSize = defaultPageSize
		}
		if m.resumeOffset > 0 {
			m.resumeOffset -= pageSize
			if m.resumeOffset < 0 {
				m.resumeOffset = 0
			}
			m.resumeCursor = 0
		}
		return m, nil

	case "r":
		// Refresh the session list
		m.resumeLoading = true
		m.resumeErr = ""
		return m, loadResumeListCmd(m)

	case "enter":
		if len(m.resumeItems) == 0 || m.resumeLoading {
			return m, nil
		}
		// Restore the selected session using absolute cursor index
		startIdx, _ := m.resumeVisibleRange()
		absIdx := startIdx + m.resumeCursor
		if absIdx < 0 || absIdx >= len(m.resumeItems) {
			return m, nil
		}
		item := m.resumeItems[absIdx]
		m.resumeLoading = true
		return m, loadRolloutCmd(m, item)
	}

	return m, nil
}

// ── Resume 分页辅助（offset = page×pageSize 语义，对齐 history 分页窗口）──

// resumeVisibleRange returns the start and end indices (exclusive) of visible
// items on the current page. resumeOffset is the absolute start index of the
// page（等价于 historyPage*historyPageSize）。
func (m *model) resumeVisibleRange() (startIdx, endIdx int) {
	total := len(m.resumeItems)
	pageSize := m.resumePageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}

	// Clamp page-start offset
	maxOffset := 0
	if total > 0 {
		maxOffset = ((total - 1) / pageSize) * pageSize
	}
	if m.resumeOffset < 0 || total == 0 {
		m.resumeOffset = 0
	}
	if m.resumeOffset > maxOffset {
		m.resumeOffset = maxOffset
	}

	startIdx = m.resumeOffset
	endIdx = startIdx + pageSize
	if endIdx > total {
		endIdx = total
	}
	return startIdx, endIdx
}

// resumeTotalNumPages calculates the total number of pages for resume items.
func (m *model) resumeTotalNumPages() int {
	if len(m.resumeItems) == 0 {
		return 1
	}
	pageSize := m.resumePageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	pages := len(m.resumeItems) / pageSize
	if len(m.resumeItems)%pageSize > 0 {
		pages++
	}
	return pages
}

// resumePageNum returns the 1-based page number for the title/status bar.
func (m *model) resumePageNum() int {
	pageSize := m.resumePageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	return m.resumeOffset/pageSize + 1
}

// resumeClampCursor ensures cursor is within valid range for the current page.
func (m *model) resumeClampCursor() {
	_, endIdx := m.resumeVisibleRange()
	count := endIdx - m.resumeOffset
	if count <= 0 {
		m.resumeCursor = 0
		return
	}
	if m.resumeCursor >= count {
		m.resumeCursor = count - 1
	}
	if m.resumeCursor < 0 {
		m.resumeCursor = 0
	}
}

// ── Resume Commands ──

// loadResumeListCmd asynchronously loads the rollout session list.
func loadResumeListCmd(m *model) tea.Cmd {
	return func() tea.Msg {
		items, err := memory.ListRolloutSessions(memory.RolloutRootDir(), 0)
		if err != nil {
			return resumeSessionsMsg{err: err}
		}
		return resumeSessionsMsg{items: items}
	}
}

// loadRolloutCmd asynchronously restores the conversation memory from a rollout file.
func loadRolloutCmd(m *model, info memory.RolloutSessionInfo) tea.Cmd {
	return func() tea.Msg {
		opts := memory.DefaultRolloutReadOptions()
		// sub-agent 会话做文件级 SubAgent 标记；director rollout 是主会话，不标记。
		if info.AgentName != "" && info.AgentName != "director" {
			opts.SubAgent = true
		}
		mem, rinfo, err := memory.ReadRolloutMemory(info.Path, opts)
		return rolloutRestoreMsg{
			mem:       mem,
			info:      rinfo,
			err:       err,
			path:      info.Path,
			agentName: info.AgentName,
		}
	}
}

// ── Resume Mode Entry/Exit ──

// enterResumeMode enters rollout session resume mode, loading the list asynchronously.
func enterResumeMode(m *model) tea.Cmd {
	// 防御：与其它全屏模式互斥（history/timeline/thinklink）
	m.historyMode = false
	m.timelineFullscreenMode = false
	m.thinklinkMode = false

	m.resumeMode = true
	m.resumeItems = nil
	m.resumeCursor = 0
	m.resumeOffset = 0
	m.resumePageSize = defaultPageSize
	m.resumeLoading = true
	m.resumeErr = ""
	m.lastKey = ""
	m.resumeRoot = memory.RolloutRootDir()

	// 初始化或重置 resume 样式
	m.resumeStyles = m.initResumeStyles()

	return loadResumeListCmd(m)
}

// exitResumeMode exits resume mode, resetting all resume-related fields.
func exitResumeMode(m *model) {
	m.resumeMode = false
	m.resumeItems = nil
	m.resumeCursor = 0
	m.resumeOffset = 0
	m.resumePageSize = 0
	m.resumeLoading = false
	m.resumeErr = ""
	m.resumeRoot = ""
	m.lastKey = ""
}

// ── Restore Wiring ──

// restoreFromRollout wires a restored rollout memory into the TUI session as a
// continuable task: upserts taskManager（AddTask 对重复 ID 是覆盖语义）,
// sets m.currentTask, rebuilds the viewport, and produces the info message
// with rollout restore stats. exitResumeMode is invoked by the caller
// (resumeUpdate), mirroring the historyUpdate → restoreSession + exitHistoryMode ordering.
func restoreFromRollout(m *model, mem *memory.ConversationMemory, info memory.RolloutInfo, path, agentName string) {
	if mem == nil || len(mem.Messages) == 0 {
		m.resumeErr = "No messages in this session"
		return
	}

	// 1. taskID 优先 session_meta.task_id（写侧落盘的原任务 ID），回退 SessionID，
	//    再回退文件名 stem。用原任务 ID 才能让续聊写回同一份 tasks/{id}.jsonl 并派生
	//    同一 blackboard boardID；老文件没有 task_id，只能回退（B1 起新会话不再如此）。
	taskID := rolloutFirstNonEmptyStr(info.TaskID, info.SessionID)
	if taskID == "" {
		taskID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}

	// 2. projectDir = CWD（为空回退 m.projectDir）。
	projectDir := info.CWD
	if projectDir == "" {
		projectDir = m.projectDir
	}

	// 3-5. Memory→TUI 落地（与 history 恢复共用 applyRestoredMemory）。
	infoMsg := applyRestoredMemory(m, mem, taskID, projectDir, "Resumed")

	// 6. rollout 独有的运行态：token 用量、模型、结束状态。
	applyRolloutRunState(m, info, agentName)

	// 7. 结束状态回写 Task：中断的会话标 failed 并带上原因，续跑确认框（B3）据此判定。
	if state := info.FinishState(); state == memory.FinishInterrupted {
		if task, ok := m.taskManager.GetTask(taskID); ok {
			task.Status = http.TaskStatusFailed
			task.Result = restoredInterruptSummary(info)
		}
		infoMsg += " · " + restoredInterruptSummary(info)
	}

	// 8. 追加恢复统计（来自 RolloutInfo）。
	infoMsg += fmt.Sprintf(" (%d messages, %d tool calls)", info.Messages, info.ToolCalls)
	var notes []string
	if info.SkippedReasoning > 0 {
		notes = append(notes, fmt.Sprintf("%d reasoning entries skipped", info.SkippedReasoning))
	}
	if info.SkippedBadLines > 0 {
		notes = append(notes, fmt.Sprintf("%d bad lines skipped", info.SkippedBadLines))
	}
	if info.SkippedSystemMessages > 0 {
		notes = append(notes, fmt.Sprintf("%d system prompt skipped", info.SkippedSystemMessages))
	}
	if len(info.DanglingToolCalls) > 0 {
		notes = append(notes, fmt.Sprintf("%d dangling tool calls", len(info.DanglingToolCalls)))
	} else if info.SyntheticOutputs > 0 {
		notes = append(notes, fmt.Sprintf("%d synthetic tool outputs", info.SyntheticOutputs))
	}
	if info.DroppedOrphanOutputs > 0 {
		notes = append(notes, fmt.Sprintf("%d orphan outputs dropped", info.DroppedOrphanOutputs))
	}
	if len(notes) > 0 {
		infoMsg += " · " + strings.Join(notes, ", ")
	}
	m.infoMsg = infoMsg
}

// rolloutFirstNonEmptyStr 返回第一个非空字符串。
func rolloutFirstNonEmptyStr(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

// restoredInterruptSummary 生成中断会话的一句话摘要（Task.Result 与信息栏共用）。
func restoredInterruptSummary(info memory.RolloutInfo) string {
	reason := info.AbortReason
	if reason == "" {
		reason = "no terminal event recorded"
	}
	step := info.MaxStepObserved
	if step == 0 {
		step = info.AssistantTurns
	}
	return fmt.Sprintf("interrupted at step %d (%s)", step, reason)
}

// ── Shared Session Restore (history & resume) ──

// applyRestoredMemory lands a fully loaded conversation memory into the current
// TUI session: rebuilds logEntries **and the execution state those entries imply**
// (tool timeline, thinklink journal), upserts the taskManager task（map 覆盖写，
// 重复 ID 直接替换）、sets m.currentTask, rebuilds the viewport, and returns the
// info message. sourceLabel distinguishes the entry point ("Loaded" for history,
// "Resumed" for rollout).
//
// 条目形状与实时路径逐项对齐（formatEventAsEntry / handleTaskEventMsg），否则恢复出的
// 会话会落到 formatLogEntry 的 default 分支，渲染成 "● tool_result …" 这样的单行文本，
// 丢失用户消息框、工具卡片、✅/❌ 状态与 diff。关键约定：
//   - 一次工具调用只产一条 tool_call_start 条目，其结果就地并入该条目——实时路径的
//     tool_call_result 分支同样是更新既有条目而非追加新条目；
//   - 悬空调用（reader 合成了"会话中断前未返回结果"占位）标为 Canceled，这正是断点位置；
//   - system 消息不进日志：实时路径从不渲染 system 消息，且 reader 默认已将其丢弃。
func applyRestoredMemory(m *model, mem *memory.ConversationMemory, taskID, projectDir, sourceLabel string) string {
	// 1. 清空视图与执行态：二次恢复不能叠加在上一个会话的时间线/计划之上。
	m.logEntries = nil
	m.timelineEntries = nil
	m.timelineCache = ""
	m.timelineCacheKey = ""
	m.thinklinkEntries = nil
	if m.toolCallEntries == nil {
		m.toolCallEntries = make(map[string]*ToolEntry)
	} else {
		clear(m.toolCallEntries)
	}
	resetTokenState(m)

	// 2. 按 call_id 索引工具结果，供 assistant 的 ToolCalls 配对。
	toolResults := make(map[string]*memory.ChatMessage)
	for i := range mem.Messages {
		msg := &mem.Messages[i]
		if msg.Type != memory.MessageTypeTool || msg.ToolCallID == nil {
			continue
		}
		if _, dup := toolResults[*msg.ToolCallID]; !dup {
			toolResults[*msg.ToolCallID] = msg
		}
	}
	consumed := make(map[string]bool, len(toolResults))

	appendEntry := func(e logEntry) {
		e.isVerbose = isVerboseEventType(e.eventType)
		m.logEntries = append(m.logEntries, e)
	}

	lastGroupID := "" // track GroupID changes for inserting sub-agent headers
	assistantTurn := 0
	for i := range mem.Messages {
		msg := &mem.Messages[i]

		// 检测 GroupID 变化，插入分组头部
		if msg.IsSubAgent && msg.GroupID != "" && msg.GroupID != lastGroupID {
			appendEntry(logEntry{
				eventType: "sub_agent_header",
				from:      "System",
				content:   "── Sub-Agent ──",
			})
			lastGroupID = msg.GroupID
		} else if !msg.IsSubAgent {
			lastGroupID = ""
		}

		// Sub-agent 消息加上前缀和缩进标记
		var prefix, from string
		if msg.IsSubAgent {
			prefix = "  │ "
			from = "Sub-Agent"
		}

		switch msg.Type {
		case memory.MessageTypeHuman:
			if !msg.IsSubAgent {
				from = "You"
			}
			// 实时路径同名 eventType → renderUserMessageBox
			appendEntry(logEntry{
				timestamp: msg.Timestamp,
				eventType: "user_message",
				from:      from,
				content:   msg.Content,
				prefix:    prefix,
			})
			m.thinklinkEntries = append(m.thinklinkEntries, ThinklinkEntry{
				ID:        fmt.Sprintf("restored-input-%d", len(m.thinklinkEntries)),
				Kind:      "user_input",
				Content:   msg.Content,
				Timestamp: msg.Timestamp,
			})

		case memory.MessageTypeAssistant:
			if !msg.IsSubAgent {
				from = "Assistant"
			}
			assistantTurn++
			if msg.Content != "" {
				appendEntry(logEntry{
					timestamp: msg.Timestamp,
					eventType: "ai_response",
					from:      from,
					content:   msg.Content,
					prefix:    prefix,
					agentName: from,
				})
			}
			// thinklink：复用后端的抽取函数（compression 包的正则），不自己造解析
			for _, block := range compression.ExtractThoughtAndPlanBlocks(msg.Content) {
				m.thinklinkEntries = append(m.thinklinkEntries, ThinklinkEntry{
					ID:        fmt.Sprintf("restored-plan-%d", len(m.thinklinkEntries)),
					Kind:      "thought_plan",
					Content:   block,
					Timestamp: msg.Timestamp,
					Step:      assistantTurn,
				})
			}
			for _, tc := range msg.ToolCalls {
				appendRestoredToolCall(m, appendEntry, tc, msg, toolResults, consumed, prefix, from)
			}

		case memory.MessageTypeTool:
			// 已并入对应 tool_call_start 条目的结果不再单独成条
			if msg.ToolCallID != nil && consumed[*msg.ToolCallID] {
				continue
			}
			if !msg.IsSubAgent {
				from = "Tool"
			}
			appendRestoredStandaloneResult(m, appendEntry, msg, prefix, from)

		case memory.MessageTypeSystem:
			// 不进日志：实时路径从不渲染 system 消息。压缩摘要（IsAnchored）的呈现
			// 留给 B4——彼时摘要经 compacted 事件恢复，而非 role=system 消息。
			continue

		default:
			if !msg.IsSubAgent {
				from = "Unknown"
			}
			appendEntry(logEntry{
				timestamp: msg.Timestamp,
				eventType: string(msg.Type),
				from:      from,
				content:   msg.Content,
				prefix:    prefix,
			})
		}
	}

	// 3. Extract title from first human message
	title := taskID
	for _, msg := range mem.Messages {
		if msg.Type == memory.MessageTypeHuman {
			r := []rune(msg.Content)
			if len(r) > 40 {
				title = string(r[:40]) + "…"
			} else {
				title = msg.Content
			}
			break
		}
	}

	// 4. Add task to task manager（覆盖写 map：同 ID 直接替换，upsert 语义）
	ctx, cancel := context.WithCancel(context.Background())
	m.taskManager.AddTask(&http.Task{
		ID:         taskID,
		Status:     "finished",
		Result:     fmt.Sprintf("Session restored: %d messages", len(mem.Messages)),
		ProjectDir: projectDir,
		Memory:     mem,
		Context:    ctx,
		CancelFunc: cancel,
	})

	// 5. Set as current task
	if task, ok := m.taskManager.GetTask(taskID); ok {
		m.currentTask = task
	}

	// 6. Rebuild viewport content
	m.buildViewportContent()

	// 7. Return info message（sourceLabel 区分入口：Loaded / Resumed）
	return fmt.Sprintf("%s session: %s", sourceLabel, title)
}

// appendRestoredToolCall 为一次已完成的工具调用构造条目：一条 tool_call_start 日志
// 条目（结果就地并入，形状对齐实时路径的 start+result 合并语义）+ 一条时间线条目。
func appendRestoredToolCall(
	m *model,
	appendEntry func(logEntry),
	tc memory.ToolCallData,
	host *memory.ChatMessage,
	toolResults map[string]*memory.ChatMessage,
	consumed map[string]bool,
	prefix, from string,
) {
	args := string(tc.Function.Arguments)
	summary := extractToolSummary(tc.Function.Name, args)

	entry := logEntry{
		timestamp:        host.Timestamp,
		eventType:        "tool_call_start",
		from:             from,
		prefix:           prefix,
		toolName:         tc.Function.Name,
		toolCallID:       tc.ID,
		content:          args,
		executionSummary: summary,
	}

	te := NewToolEntry(ToolCallInfo{
		ID:        tc.ID,
		Name:      tc.Function.Name,
		Arguments: args,
		Summary:   summary,
	})
	te.Timestamp = host.Timestamp

	status := ToolStatusSuccess
	isError := false
	var duration time.Duration

	if res := toolResults[tc.ID]; res != nil {
		consumed[tc.ID] = true
		isError = strings.HasPrefix(res.Content, "Error:")
		entry.timestamp = res.Timestamp
		entry.content = res.Content
		entry.resultBrief = extractResultBrief(tc.Function.Name, res.Content)
		entry.diffText = extractDiffFromResult(res.Content)
		duration = restoredToolDuration(res.Metadata)

		te.SetResult(ToolResultInfo{
			ToolCallID: tc.ID,
			Name:       tc.Function.Name,
			Content:    res.Content,
			IsError:    isError,
		})
		switch {
		case isSyntheticToolOutput(res):
			// reader 为悬空调用合成的占位输出 → 断点位置，标 Canceled 而非 Success
			te.SetCanceled()
			status = ToolStatusCanceled
			isError = false
		case isError:
			status = ToolStatusError
		}
	} else {
		// 防御：reader 的配对归一化保证不会出现无输出的调用
		te.SetCanceled()
		status = ToolStatusCanceled
	}

	entry.toolEntry = te
	appendEntry(entry)

	appendRestoredTimeline(m, tc.ID, tc.Function.Name, summary,
		entry.timestamp, status, isError, duration)
}

// appendRestoredStandaloneResult 为无法配对到调用的工具结果构造独立条目
//（对齐 formatEventAsEntry 的 tool_call_result 分支，含新式 ToolEntry 渲染）。
func appendRestoredStandaloneResult(m *model, appendEntry func(logEntry), msg *memory.ChatMessage, prefix, from string) {
	callID := ""
	if msg.ToolCallID != nil {
		callID = *msg.ToolCallID
	}
	isError := strings.HasPrefix(msg.Content, "Error:")
	entry := logEntry{
		timestamp:   msg.Timestamp,
		eventType:   "tool_call_result",
		from:        from,
		prefix:      prefix,
		toolCallID:  callID,
		content:     msg.Content,
		resultBrief: extractResultBrief("", msg.Content),
		diffText:    extractDiffFromResult(msg.Content),
	}
	te := NewToolEntry(ToolCallInfo{ID: callID, Summary: extractToolSummary("", "")})
	te.Timestamp = msg.Timestamp
	te.SetResult(ToolResultInfo{
		ToolCallID: callID,
		Content:    msg.Content,
		IsError:    isError,
	})
	if isSyntheticToolOutput(msg) {
		te.SetCanceled()
	}
	entry.toolEntry = te
	appendEntry(entry)
}

// appendRestoredTimeline 追加工具时间线条目，连续同名可合并工具收进 SubEntries——
// 合并规则与实时路径一致，否则恢复后 20 次 read_file 会显示 20 行而实时只显示 1 行。
func appendRestoredTimeline(m *model, id, name, detail string, ts time.Time,
	status ToolStatus, isError bool, duration time.Duration) {
	if n := len(m.timelineEntries); n > 0 {
		last := m.timelineEntries[n-1]
		if last.Kind == TimelineKindTool && last.Name == name && IsMergeableTool(name) {
			last.SubEntries = append(last.SubEntries, &TimelineEntry{
				ID:        id,
				Kind:      TimelineKindTool,
				Timestamp: ts,
				Status:    status,
				Name:      name,
				Detail:    detail,
				Duration:  duration,
				IsError:   isError,
			})
			return
		}
	}
	m.timelineEntries = append(m.timelineEntries, &TimelineEntry{
		ID:        id,
		Kind:      TimelineKindTool,
		Timestamp: ts,
		Status:    status,
		Name:      name,
		Detail:    detail,
		Duration:  duration,
		IsError:   isError,
	})
}

// isSyntheticToolOutput 判断工具消息是否为 reader 为悬空调用合成的占位输出。
func isSyntheticToolOutput(msg *memory.ChatMessage) bool {
	if msg == nil || msg.Metadata == nil {
		return false
	}
	synthetic, _ := msg.Metadata["synthetic"].(bool)
	return synthetic
}

// restoredToolDuration 从恢复出的 metadata 取工具耗时（写侧目前恒记 0，B1 起记真实值）。
func restoredToolDuration(md map[string]interface{}) time.Duration {
	seconds, _ := md["duration_seconds"].(float64)
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

// resetTokenState 清空 token 记账，避免上一个会话的用量残留到恢复出的会话里。
func resetTokenState(m *model) {
	m.inputTokens = 0
	m.outputTokens = 0
	m.cacheReadInputTokens = 0
	m.cacheCreationInputTokens = 0
	m.totalInputTokens = 0
	m.currentAgentRunTokens = AgentRunTokens{}
	for k := range m.tokenUsagePerAgent {
		delete(m.tokenUsagePerAgent, k)
	}
}

// applyRolloutRunState 落地 rollout 独有的运行态：token 用量、模型、当前 agent。
// history 路径（datamanager 的 tasks/*.jsonl）没有这些证据，故不共用本函数。
//
// 字段口径逐项对齐实时路径 handleTaskEventMsg 的 ai_response 分支；差异见
// memory.RolloutTokenTotals 的注释（cache_creation 与 total_input 未落盘，按 TUI
// 既有回退公式近似）。
func applyRolloutRunState(m *model, info memory.RolloutInfo, agentName string) {
	if agentName == "" {
		agentName = "director"
	}

	m.inputTokens = info.Tokens.Input
	m.outputTokens = info.Tokens.Output
	m.cacheReadInputTokens = info.Tokens.CachedInput
	// provider 口径总输入：缺 total_input_tokens 时的既有回退公式（input+cacheRead+cacheCreation）
	m.totalInputTokens = info.Tokens.Input + info.Tokens.CachedInput

	usage := AgentTokenUsage{
		AgentName:            agentName,
		InputTokens:          info.Tokens.Input,
		OutputTokens:         info.Tokens.Output,
		CacheReadInputTokens: info.Tokens.CachedInput,
		TotalInputTokens:     m.totalInputTokens,
	}
	m.currentAgentRunTokens = AgentRunTokens{
		AgentName:            usage.AgentName,
		InputTokens:          usage.InputTokens,
		OutputTokens:         usage.OutputTokens,
		CacheReadInputTokens: usage.CacheReadInputTokens,
		TotalInputTokens:     usage.TotalInputTokens,
		ContextTokens:        info.Tokens.LastContextTokens,
	}
	if m.tokenUsagePerAgent == nil {
		m.tokenUsagePerAgent = make(map[string]*AgentTokenUsage)
	}
	m.tokenUsagePerAgent[agentName] = &usage

	m.currentAgent = agentName
	if info.Model != "" {
		m.currentModel = info.Model
		m.refreshContextWindow()
	}
	// 恢复出的会话已有产出：taskStarted 决定输入框边框配色与 dashboard 可见性
	m.taskStarted = true

	// 只失效缓存即可：View 会按需重算 token 仪表盘与状态栏。
	//（不要先赋值 cachedTokenDashboard 再失效——invalidateFooterCache 会把它清空，
	// 实时路径里那对赋值/失效语句的前半段其实是无效功。）
	m.invalidateFooterCache()
}

// ── Resume View Rendering ──

// renderResumeView renders the fullscreen rollout session browser UI
// (structure aligned with renderHistoryView).
func renderResumeView(m *model) tea.View {
	width := m.termWidth
	height := m.termHeight
	if width < 40 {
		width = 40
	}
	if height < 8 {
		height = 8
	}

	// ── Height calculation（对齐 history）:
	//   Top border (1) + Title bar (1) + Content area (?) + Status bar (1) + Bottom border (1) = height
	//   => contentHeight = height - 4
	contentHeight := height - 4
	if contentHeight < 1 {
		contentHeight = 1
	}

	// Item lines per page = min(pageSize, contentHeight)（选中条目再占一行详情）
	effectivePageSize := m.resumePageSize
	if effectivePageSize > contentHeight {
		effectivePageSize = contentHeight
	}
	if effectivePageSize < 1 {
		effectivePageSize = 1
	}

	var b strings.Builder

	// ── Top border ──
	topLeft := "┌" + strings.Repeat("─", width-2) + "┐"
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(topLeft))
	b.WriteString("\n")

	// ── Title bar ──
	titleBar := renderResumeTitleBar(m, width)
	b.WriteString(titleBar)
	b.WriteString("\n")

	// ── Content area ──
	contentArea := renderResumeContent(m, width, contentHeight, effectivePageSize)
	b.WriteString(contentArea)

	// ── Status bar ──
	statusBar := renderResumeStatusBar(m, width)
	b.WriteString(statusBar)
	b.WriteString("\n")

	// ── Bottom border ──
	bottomLeft := "└" + strings.Repeat("─", width-2) + "┘"
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(bottomLeft))

	return tea.NewView(b.String())
}

// renderResumeTitleBar renders the top title bar of the resume view.
func renderResumeTitleBar(m *model, width int) string {
	pageNum := m.resumePageNum() // 1-based for display
	totalPages := m.resumeTotalNumPages()
	titleText := fmt.Sprintf(" Resume Session     Page %d/%d ", pageNum, totalPages)
	rightText := "esc: back  enter: resume  r: refresh"

	// Calculate available width for right text
	contentWidth := width - 2 // account for border
	leftWidth := len(titleText)
	rightWidth := len(rightText)
	paddingNeeded := contentWidth - leftWidth - rightWidth
	if paddingNeeded < 1 {
		// Truncate right text to fit
		maxRight := contentWidth - leftWidth - 1
		if maxRight > 3 {
			rightText = rightText[:maxRight] + "…"
		} else {
			rightText = "..."
		}
	} else {
		rightText = strings.Repeat(" ", paddingNeeded) + rightText
	}

	combined := titleText + rightText

	style := lipgloss.NewStyle().
		Background(lipgloss.Color("36")).
		Foreground(lipgloss.Color("0")).
		Bold(true).
		Width(width)

	return style.Render(combined)
}

// renderResumeContent renders the page-based content area. It renders exactly
// `height` lines: item lines（选中条目下方多一行缩进详情）+ empty fill lines.
// effectivePageSize caps the item count per page.
func renderResumeContent(m *model, width, height, effectivePageSize int) string {
	bgStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("234")).
		Foreground(lipgloss.Color("252")).
		Width(width)

	if len(m.resumeItems) == 0 {
		var centerText string
		if m.resumeLoading {
			centerText = "Loading rollout sessions…"
		} else if m.resumeErr != "" {
			centerText = m.resumeErr
		} else {
			centerText = fmt.Sprintf("No rollout sessions yet. Dir: %s", shrinkPath(m.resumeRoot, max(10, width-30)))
		}
		padding := strings.Repeat(" ", (width-len(centerText))/2)
		lines := make([]string, height)
		lines[0] = bgStyle.Render(padding + centerText)
		for i := 1; i < height; i++ {
			lines[i] = bgStyle.Render("")
		}
		return strings.Join(lines, "\n")
	}

	// Calculate visible range and clamp cursor for this page
	startIdx, endIdx := m.resumeVisibleRange()
	m.resumeClampCursor()
	visibleItems := m.resumeItems[startIdx:endIdx]

	// Build lines: item lines + one extra indented detail line for the selected item
	lines := make([]string, 0, height)
	for i, item := range visibleItems {
		if len(lines) >= height {
			break
		}
		isSelected := i == m.resumeCursor
		lines = append(lines, renderResumeItem(m, item, isSelected, width))
		if isSelected && len(lines) < height {
			lines = append(lines, renderResumeItemDetail(m, item, width))
		}
	}

	// Empty fill lines to exact height
	for len(lines) < height {
		lines = append(lines, bgStyle.Render(""))
	}

	return strings.Join(lines, "\n")
}

// renderResumeItem renders a single rollout session item line:
//
//	MM-DD HH:MM  [projID]  <agentName 徽标>  <model|->  preview…
func renderResumeItem(m *model, item memory.RolloutSessionInfo, selected bool, width int) string {
	dateStr := item.StartedAt.Format("01-02 15:04")
	if item.StartedAt.IsZero() {
		dateStr = strings.Repeat("-", 11)
	}

	// projID（root 直下文件为空则省略方括号段）
	proj := ""
	if item.ProjectID != "" {
		proj = "[" + item.ProjectID + "] "
	}

	// agent 徽标：文件名无 agent 段 → 主会话 "main"；否则 "sub:<agentName>"
	agent := item.AgentName
	if agent == "" {
		agent = "main"
	} else {
		agent = "sub:" + agent
	}

	modelStr := item.Model
	if modelStr == "" {
		modelStr = "-"
	}

	// 剩余宽度给 preview（前缀段均为 ASCII，len 即显示宽度）
	fixed := 2 + len(dateStr) + 2 + len(proj) + len(agent) + 1 + len(modelStr) + 2
	maxPreviewWidth := width - fixed
	if maxPreviewWidth < 8 {
		maxPreviewWidth = 8
	}
	preview := cleanTitle(item.Preview, maxPreviewWidth)
	if preview == "" {
		preview = "(no preview)"
	}

	if selected {
		return renderResumeSelectedItem(m, dateStr, proj, agent, modelStr, preview, width)
	}
	return renderResumeNormalItem(m, dateStr, proj, agent, modelStr, preview, width)
}

// renderResumeSelectedItem renders the highlighted selected item line.
func renderResumeSelectedItem(m *model, dateStr, proj, agent, modelStr, preview string, width int) string {
	s := m.resumeStyles
	if s == nil {
		s = m.initResumeStyles()
	}

	left := s.indicatorStyle.Render("● ") +
		s.dateStyle.Render(dateStr) + "  " +
		s.titleStyle.Render(proj+agent) + " " +
		s.titleStyle.Render(modelStr) + "  " +
		s.titleStyle.Render(preview)

	displayWidth := lipgloss.Width(left)
	if displayWidth < width {
		left += strings.Repeat(" ", width-displayWidth)
	}

	return lipgloss.NewStyle().
		Background(lipgloss.Color(s.badgeBgColor)).
		Foreground(lipgloss.Color("0")).
		Bold(true).
		Width(width).
		Render(left)
}

// renderResumeNormalItem renders an unselected item line.
func renderResumeNormalItem(m *model, dateStr, proj, agent, modelStr, preview string, width int) string {
	s := m.resumeStyles
	if s == nil {
		s = m.initResumeStyles()
	}

	indicator := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Render("  ")
	datePart := lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Render(dateStr)
	metaPart := s.dimStyle.Render(proj + agent + " " + modelStr)
	previewPart := lipgloss.NewStyle().
		Foreground(lipgloss.Color("252")).
		Render(preview)

	left := indicator + datePart + "  " + metaPart + "  " + previewPart

	displayWidth := lipgloss.Width(left)
	if displayWidth < width {
		left += strings.Repeat(" ", width-displayWidth)
	}

	return left
}

// renderResumeItemDetail renders the indented second line under the selected
// item: `sessionID(短) · cwd · git:branch · 大小`.
func renderResumeItemDetail(m *model, item memory.RolloutSessionInfo, width int) string {
	s := m.resumeStyles
	if s == nil {
		s = m.initResumeStyles()
	}

	// sessionID(短)：取前 8 rune；缺失时回退文件名 stem
	sid := item.SessionID
	if sid == "" {
		sid = strings.TrimSuffix(filepath.Base(item.Path), filepath.Ext(item.Path))
	}
	if r := []rune(sid); len(r) > 8 {
		sid = string(r[:8])
	}

	var parts []string
	parts = append(parts, sid)
	if item.CWD != "" {
		parts = append(parts, shrinkPath(item.CWD, 32))
	}
	if item.GitBranch != "" {
		parts = append(parts, "git:"+cleanTitle(item.GitBranch, 20))
	}
	parts = append(parts, formatResumeBytes(item.SizeBytes))

	const detailIndent = "    "
	avail := width - len(detailIndent)
	if avail < 4 {
		avail = 4
	}
	content := cleanTitle(strings.Join(parts, " · "), avail)

	return s.dimStyle.Render(detailIndent + content)
}

// renderResumeStatusBar renders the bottom pagination status bar (hint strip).
func renderResumeStatusBar(m *model, width int) string {
	var statusText string
	var commandHint string

	if len(m.resumeItems) == 0 {
		if m.resumeLoading {
			statusText = "Loading…"
		} else if m.resumeErr != "" {
			statusText = "Error"
		} else {
			statusText = "No rollout sessions"
		}
		commandHint = ""
	} else {
		startIdx, endIdx := m.resumeVisibleRange()
		statusText = fmt.Sprintf("Page %d/%d · %d-%d of %d",
			m.resumePageNum(), m.resumeTotalNumPages(),
			startIdx+1, endIdx,
			len(m.resumeItems))

		commandHint = "j/k:select  pgup/pgdn:page  r:refresh  enter:resume  esc:back"
	}

	separator := "── "
	suffix := " ──"
	contentWidth := width - len(separator) - len(suffix)

	var line string
	if commandHint != "" {
		// "statusText ── commandHint"
		// Put status on left, commands on right
		statusWidth := len(statusText)
		cmdWidth := len(commandHint)
		if contentWidth > statusWidth+cmdWidth+4 {
			padding := contentWidth - statusWidth - cmdWidth - 4
			line = separator + statusText + strings.Repeat(" ", padding) + "  " + commandHint + suffix
		} else {
			line = separator + statusText + suffix
		}
	} else {
		line = separator + statusText + strings.Repeat(" ", contentWidth-len(statusText)) + suffix
	}

	style := lipgloss.NewStyle().
		Foreground(lipgloss.Color("240")).
		Background(lipgloss.Color("234")).
		Width(width)

	return style.Render(line)
}

// ── Formatting helpers ──

// shrinkPath keeps the tail of an over-long path (directory names are at the end).
func shrinkPath(p string, maxRunes int) string {
	r := []rune(p)
	if len(r) <= maxRunes {
		return p
	}
	if maxRunes < 2 {
		return string(r[:maxRunes])
	}
	return "…" + string(r[len(r)-maxRunes+1:])
}

// formatResumeBytes formats a byte size in human-readable units.
func formatResumeBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGTPE"[exp])
}
