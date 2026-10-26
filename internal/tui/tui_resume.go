package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"yagent/internal/app"
	"yagent/internal/compression"
	"yagent/internal/http"
	"yagent/internal/memory"
	"yagent/internal/tui/components"

	tea "charm.land/bubbletea/v2"
)

type resumeSessionsMsg struct {
	items []memory.RolloutSessionInfo
	// stops 是按 rollout 路径索引的终止事实（尾读得到）。缺失即"未知"，
	// 不是"不能续" —— 渲染侧必须把两者分开画。
	stops resumeStopIndex
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
		m.resumeErr = ""
		if msg.err != nil {
			m.resumeErr = fmt.Sprintf("List rollout sessions: %v", msg.err)
		}
		if len(msg.items) == 0 && msg.err == nil {
			m.setInfoMsg("No rollout sessions found")
		}
		m.resumeStops = msg.stops
		if m.resumeStops == nil {
			m.resumeStops = resumeStopIndex{}
		}
		groups := sortResumeGroupsByState(buildResumeGroups(msg.items), m.resumeStops)
		m.resumeGroups = groups
		m.resumeExpanded = make(map[string]bool, len(groups))
		m.rebuildResumeRows()
		return m, nil

	case rolloutRestoreMsg:
		m.resumeLoading = false
		if msg.err != nil {
			m.resumeErr = fmt.Sprintf("Restore session: %v", msg.err)
			return m, nil
		}
		if msg.mem == nil || msg.mem.Size() == 0 {
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

// resumeHandleKey 处理选择器的按键。
//
// 游标是**绝对行号**，滚动由 common.WindowRange 在渲染时做居中 —— 不再有
// "offset + 页内游标"两套坐标（旧实现按键用 pageSize、渲染用夹过的高度，
// 短终端会静默吞行，docs/resume-tui-redesign.md F5）。
func resumeHandleKey(key tea.KeyMsg, m *model) (*model, tea.Cmd) {
	k := key.String()

	if k == "esc" || k == "q" || k == "ctrl+c" {
		exitResumeMode(m)
		return m, nil
	}

	if m.resumeLoading {
		return m, nil
	}

	rows, _, endIdx := m.resumeWindow()
	last := len(m.resumeRows) - 1

	switch k {
	case "j", "down":
		m.resumeCursor = min(m.resumeCursor+1, last)
		return m, nil

	case "k", "up":
		m.resumeCursor = max(m.resumeCursor-1, 0)
		return m, nil

	case " ", "space", "l", "right":
		if row := m.resumeRowAtCursor(); row != nil && row.isHead && len(m.resumeGroups[row.groupIdx].members) > 0 {
			g := m.resumeGroups[row.groupIdx]
			m.resumeExpanded[g.key] = !m.resumeExpanded[g.key]
			m.rebuildResumeRows()
			m.resumeClampCursor()
		}
		return m, nil

	case "h", "left":
		if row := m.resumeRowAtCursor(); row != nil {
			g := m.resumeGroups[row.groupIdx]
			if m.resumeExpanded[g.key] {
				m.resumeExpanded[g.key] = false
				m.rebuildResumeRows()
				m.resumeClampCursor()
			}
		}
		return m, nil

	case "pgdown", "pgdn", "pagedown", "ctrl+d", "ctrl+f":
		m.resumeCursor = min(m.resumeCursor+max(rows, 1), last)
		return m, nil

	case "pgup", "pageup", "ctrl+u", "ctrl+b":
		m.resumeCursor = max(m.resumeCursor-max(rows, 1), 0)
		return m, nil

	case "g":
		m.resumeCursor = 0
		return m, nil

	case "G":
		m.resumeCursor = last
		return m, nil

	case "H":
		_, start, _ := m.resumeWindow()
		m.resumeCursor = start
		return m, nil

	case "L":
		m.resumeCursor = max(endIdx-1, 0)
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
			m.resumeClampCursor()
			return m, nil
		}
		m.resumeLoading = true
		return m, loadRolloutCmd(m, row.item)
	}

	return m, nil
}

// resumeRowAtCursor 是绝对行号下的当前行 —— 不再有"页内游标"这层间接。
func (m *model) resumeRowAtCursor() *resumeRow {
	if m.resumeCursor < 0 || m.resumeCursor >= len(m.resumeRows) {
		return nil
	}
	return &m.resumeRows[m.resumeCursor]
}

func loadResumeListCmd(m *model) tea.Cmd {
	return func() tea.Msg {
		items, err := memory.ListRolloutSessions(memory.RolloutRootDir(), 0)
		if err != nil {
			return resumeSessionsMsg{err: err}
		}
		// 尾读与清单同批交付：分成两个 msg 会让列表先渲染出一屏"没有状态"的行，
		// 再整体重排 —— 那是第二次几何跳动，而几何稳定正是这次重设计要买的东西。
		return resumeSessionsMsg{items: items, stops: readResumeStopFacts(items)}
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
	m.resumeStops = nil
	m.resumeCursor = 0
	m.resumeLoading = true
	m.resumeErr = ""
	m.lastKey = ""
	m.resumeRoot = memory.RolloutRootDir()

	return loadResumeListCmd(m)
}

func exitResumeMode(m *model) {
	m.resumeMode = false
	m.resumeGroups = nil
	m.resumeRows = nil
	m.resumeExpanded = nil
	m.resumeCursor = 0
	m.resumeLoading = false
	m.resumeErr = ""
	m.resumeRoot = ""
	m.resumeStops = nil
	m.lastKey = ""
}

func restoreFromRollout(m *model, mem *memory.ConversationMemory, info memory.RolloutInfo, path, agentName string) {
	if mem == nil || mem.Size() == 0 {
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
	// 接管这块会话原来的便签板。没有这一步，恢复出来的任务会拿到一个刚生成的
	// sessionID ⇒ 旧结论全部不可见，只能把已经调查过的东西再做一遍。
	m.adoptRestoredBoard(info.BoardID, taskID)

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
		pushResumeDialog(m, info, path, agentName)
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
	// 一个 call_id 有两条 output = 那次委派先被打断、后来又恢复完成。
	// 说清楚，否则用户会以为恢复把上一轮的失败也带了回来。
	if info.SupersededOutputs > 0 {
		notes = append(notes, fmt.Sprintf("%d tool outputs superseded by a resumed call", info.SupersededOutputs))
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

func pushResumeDialog(m *model, info memory.RolloutInfo, rolloutPath, agentName string) {
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
	// 多一个选项："从 coding step 37 继续"（B/C 路径），而不是只能从 Director 重来。
	// 只有主会话那份 rollout 才谈得上"停在哪个委派上"；子 Agent 的文件本身不是入口。
	m.resumedDelegationPath = ""
	if agentName == "" && m.assistant != nil {
		if stop := m.assistant.ResumableDelegation(rolloutPath); stop != nil {
			d.SetAgentOption(resumeAgentOptionLabel(components.Language(m.currentLang), stop))
			m.resumedDelegationPath = rolloutPath
		}
	}

	d.SetBounds(m.termWidth, m.termHeight)
	m.dialogStack.Push(d)
}

// resumeAgentOptionLabel 拼"从 <agent> step <n> 继续"。
// 不可重放时也说清楚（走 C：把上一次的真实交付交回 Director 重新委派），
// 而不是悄悄把这一档藏起来 —— 用户点了"继续"却发现从头再做，最需要一句解释。
func resumeAgentOptionLabel(lang components.Language, stop *app.ResumableDelegation) string {
	step := ""
	if stop.MaxSteps > 0 {
		step = fmt.Sprintf(" %d/%d", stop.Step, stop.MaxSteps)
	} else if stop.Step > 0 {
		step = fmt.Sprintf(" %d", stop.Step)
	}
	label := ""
	if lang == components.LanguageZh {
		label = "从 " + stop.AgentName + step + " 继续"
		if !stop.Replayable {
			label += "（交接）"
		}
		return label
	}
	label = "Resume " + stop.AgentName + step
	if !stop.Replayable {
		label += " (handoff)"
	}
	return label
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

// adoptRestoredBoard 让恢复出来的会话接管它原来的便签板。
//
// boardID 优先（rollout 里记的 board_id）；存量 rollout 没有这个字段时退回 taskID ——
// 那正好是 board 作用域升级前的取值规则，所以旧会话照样能读到自己当年的便签。
func (m *model) adoptRestoredBoard(boardID, taskID string) {
	if boardID != "" {
		m.sessionID = boardID
		return
	}
	m.sessionID = taskID
}

func applyRestoredMemory(m *model, mem *memory.ConversationMemory, taskID, projectDir, sourceLabel string) string {
	m.logEntries = nil
	// 正文的排版状态整份作废。contentParts / dirtyEntryIndices / body 索引都是按**上一个
	// 会话**的条目建的：只清 logEntries 就留下"条目比渲染缓存短"的失配，而下一次排版
	// （View 纠正视口宽度那一步）会按缓存的长度去索引 logEntries —— 恢复一个更短的历史
	// 就是这么崩的。口径与 clearCurrentSession 一致，末尾的 buildViewportContent 负责
	// 从空缓存整段重排，这里不需要自己渲染。
	m.contentParts = nil
	m.dirtyEntryIndices = make(map[int]struct{})
	m.needFullRebuild = true
	m.prevViewportWidth = -1 // 逼一次"宽度变了"的全量重排，不复用上一个会话的渲染缓存
	m.body = bodyIndex{}     // 恢复的会话从"跟随尾部"起步（scrolledUp 的零值语义，见 tui_body.go）
	m.body.forget()
	m.timelineEntries = nil
	m.timelineCache = ""
	m.timelineCacheKey = ""
	m.thinklinkEntries = nil
	m.thinklinkAgentFilter = ""
	if m.toolCallEntries == nil {
		m.toolCallEntries = make(map[string]*ToolEntry)
	} else {
		clear(m.toolCallEntries)
	}
	resetTokenState(m)

	// 一次快照、多处遍历共用同一份拷贝：下面要取 &msgs[i] 存进 map，
	// 分多次 Snapshot 会得到不同的 slice，指针与遍历就错位了。
	msgs := mem.Snapshot()

	toolResults := make(map[string]*memory.ChatMessage)
	for i := range msgs {
		msg := &msgs[i]
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
	for i := range msgs {
		msg := &msgs[i]

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
	for _, msg := range msgs {
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

	// 任务 ctx 一律带闸门：恢复出来的会话首轮就能被 /pause 停住。
	ctx, cancel := http.NewTaskContext(0)
	m.taskManager.AddTask(&http.Task{
		ID:         taskID,
		Status:     "finished",
		Result:     fmt.Sprintf("Session restored: %d messages", len(msgs)),
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
		entry.timestamp, status, isError, duration, from)
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

// appendRestoredTimeline 是恢复侧的追加入口，与实时的 addTimelineEntry 同形。
//
// DelegationID 一律留空：恢复读的是 Director 那一层的记忆，子 Agent 的调用在**另一个**
// rollout 文件里，不在其中。所以恢复出来的会话有叶子时长、没有轮次归属 ——
// 耗时面板因此按"窗口里有没有看到该委派的子事件"来决定委派算不算嵌套容器，
// 而不是按工具名硬判（见 latency_panel.go 的 isNestedRun）。
func appendRestoredTimeline(m *model, id, name, detail string, ts time.Time,
	status ToolStatus, isError bool, duration time.Duration, agent string) {
	// 恢复的会话也要喂耗时账：只重建时间线而不记这里，⧗ 段就会在"时间线有 30 条"
	// 的会话里永远显示空闲态 —— 测试与生产不走同一个载体的老病（根 AGENTS.md §11 坑 33）。
	if duration > 0 {
		m.recordLatency(latencySample{
			id:         id,
			label:      DisplayToolName(name),
			dur:        duration,
			ts:         ts,
			agent:      agent,
			isDelegate: isDelegationTool(name),
		})
	}
	if n := len(m.timelineEntries); n > 0 {
		last := m.timelineEntries[n-1]
		if last.Kind == TimelineKindTool && last.Name == name &&
			last.Agent == agent && IsMergeableTool(name) {
			last.SubEntries = append(last.SubEntries, &TimelineEntry{
				ID:        id,
				Kind:      TimelineKindTool,
				Timestamp: ts,
				Status:    status,
				Name:      name,
				Detail:    detail,
				Duration:  duration,
				IsError:   isError,
				Agent:     agent,
			})
			m.timelineCacheKey = ""
			return
		}
	}
	m.addTimelineEntry(&TimelineEntry{
		ID:        id,
		Kind:      TimelineKindTool,
		Timestamp: ts,
		Status:    status,
		Name:      name,
		Detail:    detail,
		Duration:  duration,
		IsError:   isError,
		Agent:     agent,
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
	m.runTokensPerAgent = make(map[string]*AgentRunTokens)
	m.runAgent = ""
	m.compress = compressStats{}
	m.latency = latencyStats{}
	for k := range m.tokenUsagePerAgent {
		delete(m.tokenUsagePerAgent, k)
	}
}

// applyRolloutCompressions 用 rollout 的 compacted 记录重建 ⧉ 段的累计统计。
// 老 rollout 没有 kind/saved_tokens 时，读取端已按 Summary 前缀与 before-after 归一好了。
// cut 榜与 ctx 采样没有落盘（§8.4），恢复后这两行自然不渲染。
//
// 开头先清零：这是"重建"而不是"并入"，恢复流程里被调用两次也不该把统计翻倍。
func applyRolloutCompressions(m *model, info memory.RolloutInfo) {
	m.compress = compressStats{}
	m.latency = latencyStats{}
	for _, rec := range info.Compressions {
		idx, ok := compressTierByKind(rec.Kind)
		if !ok {
			continue
		}
		m.compress.addTier(idx, rec.Saved, rec.TokensBefore)
	}
	m.invalidateFooterCache()
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
	// RUN 列不恢复（本轮语义从 0 开始），但 ⛁ 行依赖的 ContextTokens 必须接上 rollout 末值。
	m.setRun(AgentRunTokens{
		AgentName:     agentName,
		ContextTokens: info.Tokens.LastContextTokens,
	})
	if m.tokenUsagePerAgent == nil {
		m.tokenUsagePerAgent = make(map[string]*AgentTokenUsage)
	}
	m.tokenUsagePerAgent[agentName] = &usage

	applyRolloutCompressions(m, info)

	m.currentAgent = agentName
	if info.Model != "" {
		m.currentModel = info.Model
		m.refreshContextWindow()
	}
	// 这是"界面已经进入有过任务的状态"的输入框边框旗标，不是一次新执行的起点 ——
	// 所以**不要**换成 markTaskStarted()：恢复出来的会话我们没计时，
	// ⧗ 段带子上的 total 保持 — 比编一个数诚实。
	m.taskStarted = true

	m.invalidateFooterCache()
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
