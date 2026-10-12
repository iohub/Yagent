package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"yagent/internal/compression"
	"yagent/internal/http"
	"yagent/internal/memory"
	"yagent/internal/tui/components"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type resumeStyles struct {
	badgeBgColor   string
	indicatorStyle lipgloss.Style
	dateStyle      lipgloss.Style
	titleStyle     lipgloss.Style
	spacerStyle    lipgloss.Style
	normalStyle    lipgloss.Style
	dimStyle       lipgloss.Style
	agentStyle     lipgloss.Style
}

func (m *model) initResumeStyles() *resumeStyles {
	bgColor := lipgloss.Color("36")
	fgColor := lipgloss.Color("0")
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

type resumeSessionsMsg struct {
	items []memory.RolloutSessionInfo
	err   error
}

type rolloutRestoreMsg struct {
	mem       *memory.ConversationMemory
	info      memory.RolloutInfo
	err       error
	path      string
	agentName string
}

type resumeGroup struct {
	key     string
	head    memory.RolloutSessionInfo
	members []memory.RolloutSessionInfo
}

type resumeRow struct {
	groupIdx int
	isHead   bool
	item     memory.RolloutSessionInfo
}

func resumeGroupKey(item memory.RolloutSessionInfo) string {
	if item.TaskID != "" {
		return "task:" + item.TaskID
	}
	return "file:" + item.Path
}

func isMainRolloutFile(item memory.RolloutSessionInfo) bool {
	return item.AgentName == "" || item.AgentName == "director"
}

func buildResumeGroups(items []memory.RolloutSessionInfo) []resumeGroup {
	groups := make([]resumeGroup, 0, len(items))
	idxByKey := make(map[string]int, len(items))
	for _, it := range items {
		key := resumeGroupKey(it)
		gi, ok := idxByKey[key]
		if !ok {
			groups = append(groups, resumeGroup{key: key, head: it})
			idxByKey[key] = len(groups) - 1
			continue
		}
		g := &groups[gi]
		if !isMainRolloutFile(g.head) && isMainRolloutFile(it) {
			old := g.head
			g.head = it
			g.members = append(g.members, old)
		} else {
			g.members = append(g.members, it)
		}
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].head.ModTime.Equal(groups[j].head.ModTime) {
			return groups[i].key < groups[j].key
		}
		return groups[i].head.ModTime.After(groups[j].head.ModTime)
	})
	return groups
}

func (m *model) rebuildResumeRows() {
	rows := make([]resumeRow, 0, len(m.resumeGroups))
	for gi, g := range m.resumeGroups {
		rows = append(rows, resumeRow{groupIdx: gi, isHead: true, item: g.head})
		if m.resumeExpanded[g.key] {
			for _, it := range g.members {
				rows = append(rows, resumeRow{groupIdx: gi, isHead: false, item: it})
			}
		}
	}
	m.resumeRows = rows
}

func resumeUpdate(msg tea.Msg, m *model) (*model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return resumeHandleKey(msg, m)

	case resumeSessionsMsg:
		m.resumeLoading = false
		m.resumeCursor = 0
		m.resumeOffset = 0
		m.resumeErr = ""
		if msg.err != nil {
			m.resumeErr = fmt.Sprintf("List rollout sessions: %v", msg.err)
		}
		if len(msg.items) == 0 && msg.err == nil {
			m.setInfoMsg("No rollout sessions found")
		}
		m.resumeGroups = buildResumeGroups(msg.items)
		m.resumeExpanded = make(map[string]bool, len(m.resumeGroups))
		m.rebuildResumeRows()
		return m, nil

	case rolloutRestoreMsg:
		m.resumeLoading = false
		if msg.err != nil {
			m.resumeErr = fmt.Sprintf("Restore session: %v", msg.err)
			return m, nil
		}
		if msg.mem == nil || len(msg.mem.Messages) == 0 {
			m.resumeErr = "No messages in this session"
			return m, nil
		}
		restoreFromRollout(m, msg.mem, msg.info, msg.path, msg.agentName)
		exitResumeMode(m)
		return m, nil

	case tea.WindowSizeMsg:
		m.termWidth = msg.Width
		m.termHeight = msg.Height
		return m, nil

	case taskEventMsg:
		// 与 historyUpdate 同理：恢复选择器不拥有事件流，丢弃就等于少几条消息。
		_, cmd := m.handleTaskEventMsg(msg)
		return m, cmd

	case taskCompleteMsg:
		_, cmd := m.handleTaskCompleteMsg(msg)
		return m, cmd

	default:
		return m, nil
	}
}

func resumeHandleKey(key tea.KeyMsg, m *model) (*model, tea.Cmd) {
	k := key.String()

	if k == "esc" || k == "q" || k == "ctrl+c" {
		exitResumeMode(m)
		return m, nil
	}

	if m.resumeLoading {
		return m, nil
	}

	switch k {
	case "j", "down":
		_, endIdx := m.resumeVisibleRange()
		visibleCount := endIdx - m.resumeOffset
		if visibleCount > 0 && m.resumeCursor < visibleCount-1 {
			m.resumeCursor++
		}
		return m, nil

	case "k", "up":
		if m.resumeCursor > 0 {
			m.resumeCursor--
		}
		return m, nil

	case " ", "space", "l", "right":
		if row := m.resumeRowAtCursor(); row != nil && row.isHead && len(m.resumeGroups[row.groupIdx].members) > 0 {
			g := m.resumeGroups[row.groupIdx]
			m.resumeExpanded[g.key] = !m.resumeExpanded[g.key]
			m.rebuildResumeRows()
		}
		return m, nil

	case "h", "left":
		if row := m.resumeRowAtCursor(); row != nil {
			g := m.resumeGroups[row.groupIdx]
			if m.resumeExpanded[g.key] {
				m.resumeExpanded[g.key] = false
				m.rebuildResumeRows()
			}
		}
		return m, nil

	case "pgdown", "pgdn", "pagedown", "ctrl+d":
		pageSize := m.resumePageSize
		if pageSize <= 0 {
			pageSize = defaultPageSize
		}
		if m.resumeOffset+pageSize < len(m.resumeRows) {
			m.resumeOffset += pageSize
			m.resumeCursor = 0
		}
		return m, nil

	case "pgup", "pageup", "ctrl+u":
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
		m.resumeLoading = true
		m.resumeErr = ""
		return m, loadResumeListCmd(m)

	case "enter":
		if len(m.resumeRows) == 0 || m.resumeLoading {
			return m, nil
		}
		row := m.resumeRowAtCursor()
		if row == nil {
			return m, nil
		}
		g := m.resumeGroups[row.groupIdx]
		if row.isHead && len(g.members) > 0 && !m.resumeExpanded[g.key] {
			m.resumeExpanded[g.key] = true
			m.rebuildResumeRows()
			return m, nil
		}
		m.resumeLoading = true
		return m, loadRolloutCmd(m, row.item)
	}

	return m, nil
}

func (m *model) resumeRowAtCursor() *resumeRow {
	startIdx, _ := m.resumeVisibleRange()
	absIdx := startIdx + m.resumeCursor
	if absIdx < 0 || absIdx >= len(m.resumeRows) {
		return nil
	}
	return &m.resumeRows[absIdx]
}

func (m *model) resumeVisibleRange() (startIdx, endIdx int) {
	total := len(m.resumeRows)
	pageSize := m.resumePageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}

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

func (m *model) resumeTotalNumPages() int {
	if len(m.resumeRows) == 0 {
		return 1
	}
	pageSize := m.resumePageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	pages := len(m.resumeRows) / pageSize
	if len(m.resumeRows)%pageSize > 0 {
		pages++
	}
	return pages
}

func (m *model) resumePageNum() int {
	pageSize := m.resumePageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	return m.resumeOffset/pageSize + 1
}

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

func loadResumeListCmd(m *model) tea.Cmd {
	return func() tea.Msg {
		items, err := memory.ListRolloutSessions(memory.RolloutRootDir(), 0)
		if err != nil {
			return resumeSessionsMsg{err: err}
		}
		return resumeSessionsMsg{items: items}
	}
}

func loadRolloutCmd(m *model, info memory.RolloutSessionInfo) tea.Cmd {
	return func() tea.Msg {
		opts := memory.DefaultRolloutReadOptions()
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

func enterResumeMode(m *model) tea.Cmd {
	m.historyMode = false
	m.timelineFullscreenMode = false
	m.thinklinkMode = false

	m.resumeMode = true
	m.resumeGroups = nil
	m.resumeRows = nil
	m.resumeExpanded = nil
	m.resumeCursor = 0
	m.resumeOffset = 0
	m.resumePageSize = defaultPageSize
	m.resumeLoading = true
	m.resumeErr = ""
	m.lastKey = ""
	m.resumeRoot = memory.RolloutRootDir()

	m.resumeStyles = m.initResumeStyles()

	return loadResumeListCmd(m)
}

func exitResumeMode(m *model) {
	m.resumeMode = false
	m.resumeGroups = nil
	m.resumeRows = nil
	m.resumeExpanded = nil
	m.resumeCursor = 0
	m.resumeOffset = 0
	m.resumePageSize = 0
	m.resumeLoading = false
	m.resumeErr = ""
	m.resumeRoot = ""
	m.lastKey = ""
}

func restoreFromRollout(m *model, mem *memory.ConversationMemory, info memory.RolloutInfo, path, agentName string) {
	if mem == nil || len(mem.Messages) == 0 {
		m.resumeErr = "No messages in this session"
		return
	}

	taskID := rolloutFirstNonEmptyStr(info.TaskID, info.SessionID)
	if taskID == "" {
		taskID = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}

	projectDir := info.CWD
	if projectDir == "" {
		projectDir = m.projectDir
	}

	infoMsg := applyRestoredMemory(m, mem, taskID, projectDir, "Resumed")

	applyRolloutRunState(m, info, agentName)

	if len(info.ThinklinkEntries) > 0 {
		restored := make([]ThinklinkEntry, 0, len(info.ThinklinkEntries))
		for i, e := range info.ThinklinkEntries {
			restored = append(restored, ThinklinkEntry{
				ID:      fmt.Sprintf("restored-tl-%d", i),
				Kind:    e.Kind,
				Content: e.Content,
				Step:    e.Step,
			})
		}
		m.thinklinkEntries = restored
	}

	if state := info.FinishState(); state == memory.FinishInterrupted {
		if task, ok := m.taskManager.GetTask(taskID); ok {
			task.Status = http.TaskStatusFailed
			task.Result = restoredInterruptSummary(info)
		}
		infoMsg += " · " + restoredInterruptSummary(info)
		pushResumeDialog(m, info)
	}

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
	m.setInfoMsg(infoMsg)
}

func rolloutFirstNonEmptyStr(ss ...string) string {
	for _, s := range ss {
		if s != "" {
			return s
		}
	}
	return ""
}

func pushResumeDialog(m *model, info memory.RolloutInfo) {
	if info.FinishState() != memory.FinishInterrupted {
		return
	}
	if m.taskRunning || m.dialogStack == nil || !m.dialogStack.IsEmpty() {
		return
	}
	reason := rolloutFirstNonEmptyStr(info.StopReason, info.AbortReason)
	if reason == "" {
		reason = "unknown"
	}
	var stepInfo string
	if step := info.BestStep(); step > 0 {
		if info.MaxSteps > 0 {
			stepInfo = fmt.Sprintf("step %d/%d", step, info.MaxSteps)
		} else {
			stepInfo = fmt.Sprintf("step %d", step)
		}
	}
	d := components.NewResumeDialog(
		"resume_confirm",
		reason,
		stepInfo,
		unfinishedPlanItems(m.thinklinkEntries),
		danglingToolNames(info.DanglingToolCalls),
		components.Language(m.currentLang),
	)
	d.SetBounds(m.termWidth, m.termHeight)
	m.dialogStack.Push(d)
}

func unfinishedPlanItems(entries []ThinklinkEntry) []string {
	const (
		maxItems     = 5
		maxItemRunes = 60
	)
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Kind != "thought_plan" {
			continue
		}
		var items []string
		for _, line := range strings.Split(entries[i].Content, "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.Contains(trimmed, "[ ]") && !strings.Contains(trimmed, "[>]") {
				continue
			}
			trimmed = strings.TrimPrefix(trimmed, "* ")
			if r := []rune(trimmed); len(r) > maxItemRunes {
				trimmed = string(r[:maxItemRunes]) + "…"
			}
			items = append(items, trimmed)
			if len(items) >= maxItems {
				break
			}
		}
		return items
	}
	return nil
}

func danglingToolNames(calls []memory.DanglingToolCall) []string {
	seen := make(map[string]bool)
	var names []string
	for _, c := range calls {
		if c.Name == "" || seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		names = append(names, c.Name)
	}
	return names
}

func buildResumeContinuationPrompt(d *components.ResumeDialog) string {
	var b strings.Builder
	b.WriteString("The conversation above was restored from a breakpoint and interrupted before completion.")
	b.WriteString("\nInterrupt reason: " + d.Reason)
	if d.StepInfo != "" {
		b.WriteString(" (" + d.StepInfo + ")")
	}
	if len(d.PlanItems) > 0 {
		b.WriteString("\nUnfinished plan items:")
		for _, item := range d.PlanItems {
			b.WriteString("\n  - " + item)
		}
	}
	if len(d.DanglingTools) > 0 {
		b.WriteString("\nDangling tool calls with unknown results: " + strings.Join(d.DanglingTools, ", ") +
			". Verify their actual effects before relying on them.")
	}
	b.WriteString("\nContinue this task from where it stopped. First verify what has already been done, " +
		"then complete the remaining work. Do not repeat work that is already done.")
	return b.String()
}

func restoredInterruptSummary(info memory.RolloutInfo) string {
	reason := rolloutFirstNonEmptyStr(info.StopReason, info.AbortReason)
	if reason == "" {
		reason = "no terminal event recorded"
	}
	step := info.BestStep()
	switch {
	case step > 0 && info.MaxSteps > 0:
		return fmt.Sprintf("interrupted at step %d/%d (%s)", step, info.MaxSteps, reason)
	case step > 0:
		return fmt.Sprintf("interrupted at step %d (%s)", step, reason)
	default:
		return fmt.Sprintf("interrupted (%s)", reason)
	}
}

func applyRestoredMemory(m *model, mem *memory.ConversationMemory, taskID, projectDir, sourceLabel string) string {
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

	lastGroupID := ""
	assistantTurn := 0
	for i := range mem.Messages {
		msg := &mem.Messages[i]

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
			if msg.ToolCallID != nil && consumed[*msg.ToolCallID] {
				continue
			}
			if !msg.IsSubAgent {
				from = "Tool"
			}
			appendRestoredStandaloneResult(m, appendEntry, msg, prefix, from)

		case memory.MessageTypeSystem:
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

	if task, ok := m.taskManager.GetTask(taskID); ok {
		m.currentTask = task
	}

	m.resumedFromRollout = true

	m.buildViewportContent()

	return fmt.Sprintf("%s session: %s", sourceLabel, title)
}

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
			te.SetCanceled()
			status = ToolStatusCanceled
			isError = false
		case isError:
			status = ToolStatusError
		}
	} else {
		te.SetCanceled()
		status = ToolStatusCanceled
	}

	entry.toolEntry = te
	appendEntry(entry)

	appendRestoredTimeline(m, tc.ID, tc.Function.Name, summary,
		entry.timestamp, status, isError, duration)
}

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

func isSyntheticToolOutput(msg *memory.ChatMessage) bool {
	if msg == nil || msg.Metadata == nil {
		return false
	}
	synthetic, _ := msg.Metadata["synthetic"].(bool)
	return synthetic
}

func restoredToolDuration(md map[string]interface{}) time.Duration {
	seconds, _ := md["duration_seconds"].(float64)
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds * float64(time.Second))
}

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

func applyRolloutRunState(m *model, info memory.RolloutInfo, agentName string) {
	if agentName == "" {
		agentName = "director"
	}

	m.inputTokens = info.Tokens.Input
	m.outputTokens = info.Tokens.Output
	m.cacheReadInputTokens = info.Tokens.CachedInput
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
	m.taskStarted = true

	m.invalidateFooterCache()
}

func renderResumeView(m *model) tea.View {
	width := m.termWidth
	height := m.termHeight
	if width < 40 {
		width = 40
	}
	if height < 8 {
		height = 8
	}

	contentHeight := height - 4
	if contentHeight < 1 {
		contentHeight = 1
	}

	effectivePageSize := m.resumePageSize
	if effectivePageSize > contentHeight {
		effectivePageSize = contentHeight
	}
	if effectivePageSize < 1 {
		effectivePageSize = 1
	}

	var b strings.Builder

	topLeft := "┌" + strings.Repeat("─", width-2) + "┐"
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(topLeft))
	b.WriteString("\n")

	titleBar := renderResumeTitleBar(m, width)
	b.WriteString(titleBar)
	b.WriteString("\n")

	contentArea := renderResumeContent(m, width, contentHeight, effectivePageSize)
	b.WriteString(contentArea)

	statusBar := renderResumeStatusBar(m, width)
	b.WriteString(statusBar)
	b.WriteString("\n")

	bottomLeft := "└" + strings.Repeat("─", width-2) + "┘"
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render(bottomLeft))

	return tea.NewView(b.String())
}

func renderResumeTitleBar(m *model, width int) string {
	pageNum := m.resumePageNum()
	totalPages := m.resumeTotalNumPages()
	titleText := fmt.Sprintf(" Resume Session     Page %d/%d ", pageNum, totalPages)
	rightText := "esc: back  enter: resume  r: refresh"

	contentWidth := width - 2
	leftWidth := len(titleText)
	rightWidth := len(rightText)
	paddingNeeded := contentWidth - leftWidth - rightWidth
	if paddingNeeded < 1 {
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

func renderResumeContent(m *model, width, height, effectivePageSize int) string {
	bgStyle := lipgloss.NewStyle().
		Background(lipgloss.Color("234")).
		Foreground(lipgloss.Color("252")).
		Width(width)

	if len(m.resumeRows) == 0 {
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

	startIdx, endIdx := m.resumeVisibleRange()
	m.resumeClampCursor()
	visibleItems := m.resumeRows[startIdx:endIdx]

	lines := make([]string, 0, height)
	for i, row := range visibleItems {
		if len(lines) >= height {
			break
		}
		isSelected := i == m.resumeCursor
		lines = append(lines, renderResumeRow(m, row, isSelected, width))
		if isSelected && len(lines) < height {
			lines = append(lines, renderResumeItemDetail(m, row.item, width))
		}
	}

	for len(lines) < height {
		lines = append(lines, bgStyle.Render(""))
	}

	return strings.Join(lines, "\n")
}

func renderResumeRow(m *model, row resumeRow, selected bool, width int) string {
	if row.isHead {
		g := m.resumeGroups[row.groupIdx]
		if len(g.members) == 0 {
			return renderResumeItem(m, row.item, selected, width)
		}
		arrow := "▸ "
		if m.resumeExpanded[g.key] {
			arrow = "▾ "
		}
		badge := fmt.Sprintf(" +%d", len(g.members))
		line := renderResumeItem(m, row.item, selected, width-len(arrow)-lipgloss.Width(badge))
		if selected {
			s := m.resumeStyles
			if s == nil {
				s = m.initResumeStyles()
			}
			return lipgloss.NewStyle().Background(lipgloss.Color(s.badgeBgColor)).
				Render(s.indicatorStyle.Render(arrow) + line + s.indicatorStyle.Render(badge))
		}
		return m.resumeStyles.dimStyle.Render(arrow) + line + m.resumeStyles.dimStyle.Render(badge)
	}

	const memberPrefix = "  └ "
	line := renderResumeItem(m, row.item, selected, width-lipgloss.Width(memberPrefix))
	if selected {
		s := m.resumeStyles
		if s == nil {
			s = m.initResumeStyles()
		}
		return lipgloss.NewStyle().Background(lipgloss.Color(s.badgeBgColor)).
			Render(s.indicatorStyle.Render(memberPrefix) + line)
	}
	return m.resumeStyles.dimStyle.Render(memberPrefix) + line
}

func renderResumeItem(m *model, item memory.RolloutSessionInfo, selected bool, width int) string {
	dateStr := item.StartedAt.Format("01-02 15:04")
	if item.StartedAt.IsZero() {
		dateStr = strings.Repeat("-", 11)
	}

	proj := ""
	if item.ProjectID != "" {
		proj = "[" + item.ProjectID + "] "
	}

	agent := item.AgentName
	if isMainRolloutFile(item) {
		agent = "main"
	} else {
		agent = "sub:" + agent
	}

	modelStr := item.Model
	if modelStr == "" {
		modelStr = "-"
	}

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

func renderResumeItemDetail(m *model, item memory.RolloutSessionInfo, width int) string {
	s := m.resumeStyles
	if s == nil {
		s = m.initResumeStyles()
	}

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

func renderResumeStatusBar(m *model, width int) string {
	var statusText string
	var commandHint string

	if len(m.resumeRows) == 0 {
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
			len(m.resumeRows))

		commandHint = "j/k:select  space:expand  pgup/pgdn:page  r:refresh  enter:resume  esc:back"
	}

	separator := "── "
	suffix := " ──"
	contentWidth := width - len(separator) - len(suffix)

	var line string
	if commandHint != "" {
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
