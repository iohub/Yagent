package tui

// tui_resume_restore_test.go — 会话恢复落地（applyRestoredMemory / applyRolloutRunState）测试。
//
// 核心验收点是「恢复出的条目与实时路径同构」：此前恢复用的是 user_input/tool_result
// 这类实时路径不存在的 eventType，全部落到 formatLogEntry 的 default 分支，渲染成
// "● tool_result …" 单行文本，丢失用户消息框、工具卡片、状态图标与 diff。

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"yagent/internal/http"
	"yagent/internal/memory"
	"yagent/internal/messaging"

	"charm.land/lipgloss/v2"
)

// ─── fixture ───

func restoreTestModel(t *testing.T) *model {
	t.Helper()
	InitLangManager() // 状态栏文案依赖全局 langManager，测试环境需显式初始化
	m := newTestModel()
	m.taskManager = http.NewTaskManager(nil, 0)
	m.toolCallEntries = make(map[string]*ToolEntry)
	m.tokenUsagePerAgent = make(map[string]*AgentTokenUsage)
	return m
}

func restoreStrPtr(s string) *string { return &s }

func restoreAssistant(text string, calls ...memory.ToolCallData) memory.ChatMessage {
	return memory.ChatMessage{
		Type:      memory.MessageTypeAssistant,
		Content:   text,
		ToolCalls: calls,
		Timestamp: time.Now(),
	}
}

func restoreCall(id, name, args string) memory.ToolCallData {
	return memory.ToolCallData{
		ID:   id,
		Type: "function",
		Function: memory.ToolCallFunction{
			Name:      name,
			Arguments: json.RawMessage(args),
		},
	}
}

func restoreToolResult(id, content string, meta map[string]interface{}) memory.ChatMessage {
	return memory.ChatMessage{
		Type:       memory.MessageTypeTool,
		Content:    content,
		ToolCallID: restoreStrPtr(id),
		Metadata:   meta,
		Timestamp:  time.Now(),
	}
}

// syntheticMeta 复刻 rollout reader 为悬空调用合成占位输出时打的标记。
func syntheticMeta() map[string]interface{} {
	return map[string]interface{}{"synthetic": true, "synthetic_reason": "missing_tool_output"}
}

func restoreMemory(msgs ...memory.ChatMessage) *memory.ConversationMemory {
	mem := memory.NewConversationMemory(300)
	mem.Messages = msgs
	return mem
}

// ─── 条目形状 ───

// TestApplyRestoredMemory_EntryShapeMatchesLivePath 恢复出的 eventType 必须是实时
// 路径的同名类型，且一次工具调用只产一条条目（结果就地并入，不另起 tool_call_result）。
func TestApplyRestoredMemory_EntryShapeMatchesLivePath(t *testing.T) {
	m := restoreTestModel(t)
	mem := restoreMemory(
		memory.ChatMessage{Type: memory.MessageTypeHuman, Content: "帮我看下 main.go", Timestamp: time.Now()},
		restoreAssistant("正在读取", restoreCall("c1", "read_file", `{"target_file":"main.go"}`)),
		restoreToolResult("c1", "package main\n", map[string]interface{}{"exit_code": float64(0), "duration_seconds": float64(2)}),
		restoreAssistant("看完了，是个入口文件"),
	)

	applyRestoredMemory(m, mem, "task-1", "/repo", "Resumed")

	want := []string{"user_message", "ai_response", "tool_call_start", "ai_response"}
	if len(m.logEntries) != len(want) {
		t.Fatalf("logEntries = %d, want %d: %v", len(m.logEntries), len(want), entryTypes(m.logEntries))
	}
	for i, w := range want {
		if got := m.logEntries[i].eventType; got != w {
			t.Fatalf("entry[%d].eventType = %q, want %q (全部: %v)", i, got, w, entryTypes(m.logEntries))
		}
	}

	tool := m.logEntries[2]
	if tool.toolEntry == nil {
		t.Fatal("tool entry has no toolEntry — 会退回 legacy emoji 渲染路径")
	}
	if tool.toolEntry.Status != ToolStatusSuccess {
		t.Errorf("tool status = %v, want success", tool.toolEntry.Status)
	}
	if tool.toolName != "read_file" || tool.toolCallID != "c1" {
		t.Errorf("tool identity = %q/%q, want read_file/c1", tool.toolName, tool.toolCallID)
	}
	// 结果就地并入 start 条目（对齐实时路径 tool_call_result 分支的更新语义）
	if !strings.Contains(tool.content, "package main") {
		t.Errorf("tool content = %q, want result merged in", tool.content)
	}
	if !tool.isVerbose {
		t.Error("tool entry should be verbose（主视图隐藏、时间线面板展示）")
	}
	if tool.executionSummary == "" {
		t.Error("executionSummary empty — 工具卡片会缺少 · 后的路径摘要")
	}
	if got := tool.toolEntry.Call.Arguments; got == "" {
		t.Error("toolEntry.Call.Arguments empty")
	}
}

// entryTypes 返回条目类型序列，便于失败信息定位。
func entryTypes(entries []logEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.eventType
	}
	return out
}

// TestApplyRestoredMemory_NoDefaultBranchRendering 直接验收用户可见的 bug：
// 恢复出的条目渲染后不得出现 default 分支的 "● <eventType>" 前缀。
func TestApplyRestoredMemory_NoDefaultBranchRendering(t *testing.T) {
	m := restoreTestModel(t)
	mem := restoreMemory(
		memory.ChatMessage{Type: memory.MessageTypeHuman, Content: "do it", Timestamp: time.Now()},
		memory.ChatMessage{Type: memory.MessageTypeSystem, Content: strings.Repeat("SYS PROMPT ", 500), Timestamp: time.Now()},
		restoreAssistant("working", restoreCall("c1", "run_bash", `{"command":"go build"}`)),
		restoreToolResult("c1", "ok", nil),
		restoreAssistant("done"),
	)
	applyRestoredMemory(m, mem, "task-2", "/repo", "Resumed")

	for _, e := range m.logEntries {
		rendered := formatLogEntry(e, 100)
		for _, bad := range []string{"● user_input", "● tool_result", "● system", "● assistant"} {
			if strings.Contains(rendered, bad) {
				t.Errorf("entry %q rendered via default branch (%q found)", e.eventType, bad)
			}
		}
		// system prompt 不得进日志（实时路径从不渲染 system 消息）
		if strings.Contains(rendered, "SYS PROMPT") {
			t.Error("system prompt leaked into the log — 会刷屏 15k 字符")
		}
	}
	if len(m.logEntries) != 4 {
		t.Fatalf("logEntries = %d, want 4 (system skipped): %v", len(m.logEntries), entryTypes(m.logEntries))
	}
}

// ─── 断点：悬空调用 ───

// TestApplyRestoredMemory_DanglingCallMarkedCanceled reader 为无输出的调用合成占位，
// 恢复后必须标为 Canceled（而非 Success）——这正是断点位置。
func TestApplyRestoredMemory_DanglingCallMarkedCanceled(t *testing.T) {
	m := restoreTestModel(t)
	mem := restoreMemory(
		memory.ChatMessage{Type: memory.MessageTypeHuman, Content: "build it", Timestamp: time.Now()},
		restoreAssistant("running", restoreCall("c1", "delegate_devops", `{"task":"check"}`)),
		restoreToolResult("c1", "会话中断前未返回结果", syntheticMeta()),
	)
	applyRestoredMemory(m, mem, "task-3", "/repo", "Resumed")

	var tool *logEntry
	for i := range m.logEntries {
		if m.logEntries[i].eventType == "tool_call_start" {
			tool = &m.logEntries[i]
		}
	}
	if tool == nil || tool.toolEntry == nil {
		t.Fatalf("no tool entry with toolEntry: %v", entryTypes(m.logEntries))
	}
	if tool.toolEntry.Status != ToolStatusCanceled {
		t.Errorf("toolEntry.Status = %v, want canceled", tool.toolEntry.Status)
	}
	if len(m.timelineEntries) != 1 {
		t.Fatalf("timelineEntries = %d, want 1", len(m.timelineEntries))
	}
	if got := m.timelineEntries[0].Status; got != ToolStatusCanceled {
		t.Errorf("timeline status = %v, want canceled", got)
	}
	if got := m.timelineEntries[0].Name; got != "delegate_devops" {
		t.Errorf("timeline name = %q, want delegate_devops", got)
	}
}

// ─── 时间线重建 ───

// TestApplyRestoredMemory_TimelineRebuildAndMerge 时间线条目要按实时路径的合并规则重建，
// 否则恢复后 20 次 read_file 显示 20 行、而实时只显示 1 行合并组。
func TestApplyRestoredMemory_TimelineRebuildAndMerge(t *testing.T) {
	m := restoreTestModel(t)
	msgs := []memory.ChatMessage{
		{Type: memory.MessageTypeHuman, Content: "go", Timestamp: time.Now()},
	}
	// 3 次可合并的 read_file + 1 次不可合并的 run_bash
	for i, name := range []string{"read_file", "read_file", "read_file"} {
		id := string(rune('a' + i))
		msgs = append(msgs,
			restoreAssistant("", restoreCall("c"+id, name, `{"target_file":"f"}`)),
			restoreToolResult("c"+id, "body", nil),
		)
	}
	msgs = append(msgs,
		restoreAssistant("", restoreCall("c9", "run_bash", `{"command":"ls"}`)),
		restoreToolResult("c9", "ok", nil),
	)

	applyRestoredMemory(m, restoreMemory(msgs...), "task-4", "/repo", "Resumed")

	if len(m.timelineEntries) != 2 {
		t.Fatalf("timelineEntries = %d, want 2 (3 merged read_file + 1 run_bash): %+v",
			len(m.timelineEntries), timelineNames(m.timelineEntries))
	}
	merged := m.timelineEntries[0]
	if merged.Name != "read_file" || merged.MergedCount() != 3 {
		t.Errorf("merged group = %q count=%d, want read_file ×3", merged.Name, merged.MergedCount())
	}
	if got := m.timelineEntries[1].Name; got != "run_bash" {
		t.Errorf("second timeline entry = %q, want run_bash", got)
	}
	if m.timelineEntries[1].MergedCount() != 1 {
		t.Error("run_bash must not merge")
	}
}

func timelineNames(entries []*TimelineEntry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Name
	}
	return out
}

// TestApplyRestoredMemory_ToolErrorStatus Error: 前缀的结果要标为错误状态。
func TestApplyRestoredMemory_ToolErrorStatus(t *testing.T) {
	m := restoreTestModel(t)
	mem := restoreMemory(
		restoreAssistant("", restoreCall("c1", "run_bash", `{"command":"false"}`)),
		restoreToolResult("c1", "Error: exit status 1", nil),
	)
	applyRestoredMemory(m, mem, "task-5", "/repo", "Resumed")

	if len(m.timelineEntries) != 1 || m.timelineEntries[0].Status != ToolStatusError {
		t.Fatalf("timeline = %+v, want one error entry", m.timelineEntries)
	}
	if !m.timelineEntries[0].IsError {
		t.Error("IsError = false, want true")
	}
}

// ─── 重复恢复不叠加 ───

func TestApplyRestoredMemory_SecondRestoreReplacesState(t *testing.T) {
	m := restoreTestModel(t)
	first := restoreMemory(
		restoreAssistant("", restoreCall("c1", "read_file", `{}`)),
		restoreToolResult("c1", "a", nil),
	)
	second := restoreMemory(
		memory.ChatMessage{Type: memory.MessageTypeHuman, Content: "new session", Timestamp: time.Now()},
		restoreAssistant("hi"),
	)

	applyRestoredMemory(m, first, "task-a", "/repo", "Resumed")
	applyRestoredMemory(m, second, "task-b", "/repo", "Resumed")

	if len(m.timelineEntries) != 0 {
		t.Errorf("timelineEntries = %d, want 0 (上一个会话的时间线必须清空)", len(m.timelineEntries))
	}
	if len(m.logEntries) != 2 {
		t.Errorf("logEntries = %d, want 2: %v", len(m.logEntries), entryTypes(m.logEntries))
	}
	if m.currentTask == nil || m.currentTask.ID != "task-b" {
		t.Errorf("currentTask = %+v, want task-b", m.currentTask)
	}
}

// ─── thinklink 重建 ───

func TestApplyRestoredMemory_ThinklinkRebuilt(t *testing.T) {
	m := restoreTestModel(t)
	plan := "## Thought & Plan\n### Thought Process\n* **Current Goal**: 修 resume\n---\n### Plan Update\n* [>] 1. A1"
	mem := restoreMemory(
		memory.ChatMessage{Type: memory.MessageTypeHuman, Content: "修 resume", Timestamp: time.Now()},
		restoreAssistant(plan),
	)
	applyRestoredMemory(m, mem, "task-6", "/repo", "Resumed")

	var inputs, plans int
	for _, e := range m.thinklinkEntries {
		switch e.Kind {
		case "user_input":
			inputs++
			if e.Content != "修 resume" {
				t.Errorf("user_input content = %q", e.Content)
			}
		case "thought_plan":
			plans++
			if !strings.Contains(e.Content, "Thought & Plan") {
				t.Errorf("thought_plan content = %q", e.Content)
			}
			if e.Step != 1 {
				t.Errorf("thought_plan step = %d, want 1", e.Step)
			}
		default:
			t.Errorf("unknown thinklink kind %q", e.Kind)
		}
	}
	if inputs != 1 || plans != 1 {
		t.Fatalf("thinklink entries = %d inputs / %d plans, want 1/1 (全部 %+v)",
			inputs, plans, m.thinklinkEntries)
	}
}

// ─── rollout 运行态 ───

func TestApplyRolloutRunState(t *testing.T) {
	m := restoreTestModel(t)
	// 模拟上一个会话的残留用量，验证会被覆盖而非叠加
	m.inputTokens = 999
	m.outputTokens = 999
	m.tokenUsagePerAgent["stale-agent"] = &AgentTokenUsage{AgentName: "stale-agent", InputTokens: 1}

	// 走真实调用顺序：restoreFromRollout 先 applyRestoredMemory（负责清残留）再落运行态
	applyRestoredMemory(m, restoreMemory(
		memory.ChatMessage{Type: memory.MessageTypeHuman, Content: "q", Timestamp: time.Now()},
	), "task-tokens", "/repo", "Resumed")

	info := memory.RolloutInfo{
		Model: "deepseek-v3",
		Tokens: memory.RolloutTokenTotals{
			Input: 592667, CachedInput: 522240, Output: 6554, Total: 599221,
			LastContextTokens: 86413,
		},
	}
	applyRolloutRunState(m, info, "")

	if m.inputTokens != 592667 || m.outputTokens != 6554 || m.cacheReadInputTokens != 522240 {
		t.Errorf("tokens = in:%d out:%d cached:%d, want 592667/6554/522240",
			m.inputTokens, m.outputTokens, m.cacheReadInputTokens)
	}
	// provider 口径总输入 = input + cacheRead（cache_creation 未落盘）
	if m.totalInputTokens != 592667+522240 {
		t.Errorf("totalInputTokens = %d, want %d", m.totalInputTokens, 592667+522240)
	}
	if m.currentAgentRunTokens.ContextTokens != 86413 {
		t.Errorf("ContextTokens = %d, want 86413（上下文进度条依赖它）", m.currentAgentRunTokens.ContextTokens)
	}
	if m.currentAgent != "director" || m.currentAgentRunTokens.AgentName != "director" {
		t.Errorf("agent = %q/%q, want director", m.currentAgent, m.currentAgentRunTokens.AgentName)
	}
	if m.currentModel != "deepseek-v3" {
		t.Errorf("currentModel = %q, want deepseek-v3", m.currentModel)
	}
	if !m.taskStarted {
		t.Error("taskStarted = false — 输入框边框与 dashboard 可见性会判错")
	}
	if _, ok := m.tokenUsagePerAgent["stale-agent"]; ok {
		t.Error("stale per-agent usage survived restore")
	}
	if u, ok := m.tokenUsagePerAgent["director"]; !ok || u.InputTokens != 592667 {
		t.Errorf("per-agent usage = %+v, want director/592667", m.tokenUsagePerAgent)
	}
	// 缓存被失效（View 会按需重算），重算结果必须带上恢复出的用量
	if m.tokenDashboardValid {
		t.Error("tokenDashboardValid = true, want invalidated so View recomputes")
	}
	if dash := m.renderTokenDashboard(); !strings.Contains(dash, "592.7k") {
		t.Errorf("dashboard = %q, want it to contain the restored total", dash)
	}
}

// TestApplyRolloutRunState_SubAgentName sub-agent 会话的 agent 名要保留（token 面板按 agent 分色）。
func TestApplyRolloutRunState_SubAgentName(t *testing.T) {
	m := restoreTestModel(t)
	applyRolloutRunState(m, memory.RolloutInfo{Model: "m"}, "coding")
	if m.currentAgent != "coding" {
		t.Errorf("currentAgent = %q, want coding", m.currentAgent)
	}
	if _, ok := m.tokenUsagePerAgent["coding"]; !ok {
		t.Errorf("per-agent usage keys = %v, want coding", keysOf(m.tokenUsagePerAgent))
	}
}

// TestApplyRolloutRunState_StatusBarShowsRestoredIdentity 恢复出的 model/agent 要出现在
// 状态栏——这是用户判断"接上了哪个会话"的主要视觉线索。
func TestApplyRolloutRunState_StatusBarShowsRestoredIdentity(t *testing.T) {
	m := restoreTestModel(t)
	m.termWidth = 160
	m.currentProvider = "deepseek"
	applyRolloutRunState(m, memory.RolloutInfo{Model: "DeepSeek-V3.2"}, "repo")

	bar := m.renderAirlineStatusBar()
	for _, want := range []string{"DeepSeek-V3.2", "deepseek"} {
		if !strings.Contains(bar, want) {
			t.Errorf("status bar missing %q:\n%s", want, bar)
		}
	}
	// 宽度必须与终端一致，否则会与 viewport/滚动条拼接错位
	if got := lipgloss.Width(bar); got != m.termWidth {
		t.Errorf("status bar width = %d, want %d", got, m.termWidth)
	}
}

func keysOf(m map[string]*AgentTokenUsage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ─── 恢复态 vs 实时态渲染一致性 ───

// liveEventEntry 用实时路径（formatEventAsEntry）构造等价条目，作为保真度对照基准。
func liveEventEntry(eventType, from string, content interface{}, ts time.Time) logEntry {
	return formatEventAsEntry(&messaging.MessageEvent{
		Type:      messaging.EventType(eventType),
		From:      from,
		Content:   content,
		Timestamp: ts,
	})
}

// TestApplyRestoredMemory_RenderParityWithLivePath A2 的核心契约：同样内容下，恢复出的
// 条目与实时事件构造的条目必须渲染成同一串字符。此前恢复走的是 user_input/tool_result
// 等实时路径不存在的 eventType，落到 default 分支渲染成 "● tool_result …"。
func TestApplyRestoredMemory_RenderParityWithLivePath(t *testing.T) {
	ts := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	m := restoreTestModel(t)

	const (
		userText = "帮我修 resume"
		aiText   = "读完了，问题在 applyRestoredMemory"
		toolName = "read_file"
		toolArgs = `{"target_file":"internal/tui/tui_resume.go"}`
		toolRes  = "package tui\n"
	)

	mem := restoreMemory(
		memory.ChatMessage{Type: memory.MessageTypeHuman, Content: userText, Timestamp: ts},
		restoreAssistant(aiText, restoreCall("c1", toolName, toolArgs)),
		restoreToolResult("c1", toolRes, nil),
	)
	applyRestoredMemory(m, mem, "task-parity", "/repo", "Resumed")

	if len(m.logEntries) != 3 {
		t.Fatalf("logEntries = %d, want 3: %v", len(m.logEntries), entryTypes(m.logEntries))
	}

	live := []logEntry{
		liveEventEntry("user_message", "", userText, ts),
		liveEventEntry("ai_response", "Assistant", aiText, ts),
		func() logEntry {
			// 实时路径：start 条目 + result 就地并入（见 handleTaskEventMsg 的
			// tool_call_result 分支），恢复路径必须落到同一终态
			e := liveEventEntry("tool_call_start", "Assistant", map[string]interface{}{
				"tool_name":    toolName,
				"tool_call_id": "c1",
				"arguments":    toolArgs,
			}, ts)
			e.content = toolRes
			e.isToolRunning = false
			e.diffText = extractDiffFromResult(toolRes)
			e.toolEntry.SetResult(ToolResultInfo{
				ToolCallID: "c1", Name: toolName, Content: toolRes, IsError: false,
			})
			return e
		}(),
	}

	for _, width := range []int{40, 60, 80, 100, 120, 200} {
		for i := range live {
			got := formatLogEntry(m.logEntries[i], width)
			want := formatLogEntry(live[i], width)
			if got != want {
				t.Errorf("width=%d entry[%d](%s) 渲染与实时路径不一致\n got: %q\nwant: %q",
					width, i, m.logEntries[i].eventType, got, want)
			}
		}
	}
}

// TestApplyRestoredMemory_RenderWidthSweepNoPanic 各宽度下渲染不得 panic，
// 且时间线面板不得出现格式化错误。
func TestApplyRestoredMemory_RenderWidthSweepNoPanic(t *testing.T) {
	m := restoreTestModel(t)
	mem := restoreMemory(
		memory.ChatMessage{Type: memory.MessageTypeHuman, Content: strings.Repeat("很长的任务描述 ", 30), Timestamp: time.Now()},
		restoreAssistant(strings.Repeat("# 标题\n\n- 列表项 with a fairly long line of markdown content\n", 8)),
		restoreAssistant("calling", restoreCall("c1", "search_replace_in_file", `{"file_path":"/some/very/long/path/to/file.go","old_string":"aaaa","new_string":"bbbb"}`)),
		restoreToolResult("c1", "--- a/x.go\n+++ b/x.go\n@@ -1 +1 @@\n-aaaa\n+bbbb\n", nil),
		restoreAssistant("done", restoreCall("c2", "run_bash", `{"command":"go test ./..."}`)),
		restoreToolResult("c2", "会话中断前未返回结果", syntheticMeta()),
	)
	applyRestoredMemory(m, mem, "task-sweep", "/repo", "Resumed")

	for _, width := range []int{20, 40, 60, 80, 100, 120, 200} {
		for i, e := range m.logEntries {
			rendered := formatLogEntry(e, width)
			if strings.Contains(rendered, "%!") {
				t.Errorf("width=%d entry[%d](%s) has format errors: %q", width, i, e.eventType, rendered)
			}
		}
		panel := RenderTimeline(m.timelineEntries, false, true, width-4, nil)
		if strings.Contains(panel, "%!") {
			t.Errorf("width=%d timeline render has format errors: %q", width, panel)
		}
		// 悬空调用在面板里要有可见标记，不能静默显示成已完成
		if !strings.Contains(panel, "delegate") && !strings.Contains(panel, "run_bash") {
			t.Errorf("width=%d timeline missing tool names: %q", width, panel)
		}
	}
}
