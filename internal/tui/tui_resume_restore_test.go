package tui

// tui_resume_restore_test.go — 会话恢复落地（applyRestoredMemory / applyRolloutRunState）测试。
//
// 核心验收点是「恢复出的条目与实时路径同构」：此前恢复用的是 user_input/tool_result
// 这类实时路径不存在的 eventType，全部落到 formatLogEntry 的 default 分支，渲染成
// "● tool_result …" 单行文本，丢失用户消息框、工具卡片、状态图标与 diff。

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"yagent/internal/http"
	"yagent/internal/memory"
	"yagent/internal/messaging"
	"yagent/internal/tui/components"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ─── fixture ───

func restoreTestModel(t *testing.T) *model {
	t.Helper()
	testLang(t) // 状态栏文案依赖全局 langManager，测试环境需显式初始化（走生产入口）
	m := newTestModel()
	m.taskManager = http.NewTaskManager(nil, 0)
	m.toolCallEntries = make(map[string]*ToolEntry)
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
	mem.Replace(msgs)
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
	if m.currentRun().ContextTokens != 86413 {
		t.Errorf("ContextTokens = %d, want 86413（上下文进度条依赖它）", m.currentRun().ContextTokens)
	}
	if m.currentAgent != "director" || m.currentRun().AgentName != "director" {
		t.Errorf("agent = %q/%q, want director", m.currentAgent, m.currentRun().AgentName)
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
	// 缓存被失效（View 会按需重算），重算结果必须带上恢复出的用量。
	if m.tokenDashboardValid {
		t.Error("tokenDashboardValid = true, want invalidated so View recomputes")
	}
	// Σ 用归一化输入：input+cached = 1114907，再加 output 6554 → 1.12m。
	// 旧断言钉的是 592.7k（只算 inputTokens），那正是 Anthropic 路径漏掉缓存的口径 bug。
	m.termWidth = 100
	if dash := m.renderFooterTokenPanel(); !strings.Contains(dash, "1.12m") {
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
		t.Errorf("per-agent usage keys = %v, want coding", slices.Sorted(maps.Keys(m.tokenUsagePerAgent)))
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

// ─── B3: 一键续跑确认框 ──────────────────────────────────────────────────────

func TestPushResumeDialog(t *testing.T) {
	interrupted := memory.RolloutInfo{
		Aborted:     true,
		AbortReason: "context cancelled",
		Steps:       12,
		MaxSteps:    30,
		DanglingToolCalls: []memory.DanglingToolCall{
			{ID: "c1", Name: "delegate_coding"},
			{ID: "c2", Name: "delegate_coding"},
			{ID: "c3", Name: "run_bash"},
		},
	}

	t.Run("interrupted_pushes_dialog", func(t *testing.T) {
		m := restoreTestModel(t)
		pushResumeDialog(m, interrupted, "", "")
		if m.dialogStack.Len() != 1 {
			t.Fatalf("dialogStack.Len() = %d, want 1", m.dialogStack.Len())
		}
		d, ok := m.dialogStack.Top().(*components.ResumeDialog)
		if !ok {
			t.Fatalf("top dialog type = %T, want *components.ResumeDialog", m.dialogStack.Top())
		}
		if d.Reason != "context cancelled" {
			t.Errorf("Reason = %q, want abort reason", d.Reason)
		}
		if d.StepInfo != "step 12/30" {
			t.Errorf("StepInfo = %q, want step 12/30", d.StepInfo)
		}
		wantTools := []string{"delegate_coding", "run_bash"}
		if !reflect.DeepEqual(d.DanglingTools, wantTools) {
			t.Errorf("DanglingTools = %v, want %v (dedup, order preserved)", d.DanglingTools, wantTools)
		}
	})

	t.Run("complete_does_not_push", func(t *testing.T) {
		m := restoreTestModel(t)
		pushResumeDialog(m, memory.RolloutInfo{Finished: true}, "", "")
		if m.dialogStack.Len() != 0 {
			t.Errorf("dialogStack.Len() = %d, want 0 for finished session", m.dialogStack.Len())
		}
	})

	t.Run("skipped_when_task_running", func(t *testing.T) {
		m := restoreTestModel(t)
		m.taskRunning = true
		pushResumeDialog(m, interrupted, "", "")
		if m.dialogStack.Len() != 0 {
			t.Errorf("dialogStack.Len() = %d, want 0 while task running", m.dialogStack.Len())
		}
	})

	t.Run("skipped_when_other_dialog_open", func(t *testing.T) {
		m := restoreTestModel(t)
		m.dialogStack.Push(components.NewQuitConfirmDialog("quit_confirm", "t", "m", "y", "n", "167"))
		pushResumeDialog(m, interrupted, "", "")
		if m.dialogStack.Len() != 1 {
			t.Errorf("dialogStack.Len() = %d, want 1 (pre-existing dialog untouched)", m.dialogStack.Len())
		}
	})
}

func TestUnfinishedPlanItems(t *testing.T) {
	plan := strings.Join([]string{
		"## Thought & Plan",
		"* [x] 1. [已完成] 读取文件",
		"* [>] 2. [当前] 修复 bug",
		"* [ ] 3. [待处理] 跑测试",
		"* [ ] 4. [待处理] 提交",
	}, "\n")
	entries := []ThinklinkEntry{
		{Kind: "user_input", Content: "fix the bug"},
		{Kind: "thought_plan", Content: plan},
	}
	got := unfinishedPlanItems(entries)
	want := []string{"[>] 2. [当前] 修复 bug", "[ ] 3. [待处理] 跑测试", "[ ] 4. [待处理] 提交"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unfinishedPlanItems = %v, want %v", got, want)
	}

	// 全部完成 → nil
	donePlan := "* [x] 1. [已完成] 全部做完"
	if got := unfinishedPlanItems([]ThinklinkEntry{{Kind: "thought_plan", Content: donePlan}}); got != nil {
		t.Errorf("all-done plan → %v, want nil", got)
	}
	// 无计划块 → nil
	if got := unfinishedPlanItems([]ThinklinkEntry{{Kind: "user_input", Content: "hi"}}); got != nil {
		t.Errorf("no plan block → %v, want nil", got)
	}
}

func TestBuildResumeContinuationPrompt(t *testing.T) {
	d := components.NewResumeDialog("resume_confirm", "max_steps", "step 30/30",
		[]string{"[ ] 3. [待处理] 跑测试"}, []string{"delegate_coding"}, components.LanguageEn)
	prompt := buildResumeContinuationPrompt(d)
	for _, want := range []string{"max_steps", "step 30/30", "Continue this task", "delegate_coding", "Do not repeat"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("continuation prompt missing %q:\n%s", want, prompt)
		}
	}
}

// TestRestoreFromRollout_ThinklinkFromEvents B4：rollout 落盘的 thinklink 条目
// 优先于消息块重解析；无落盘条目的存量文件保持重解析回退。
func TestRestoreFromRollout_ThinklinkFromEvents(t *testing.T) {
	newMem := func() *memory.ConversationMemory {
		mem := memory.NewConversationMemory(10)
		mem.AddHumanMessage("fix the login bug")
		mem.AddAssistantMessage("## Thought & Plan\n* [ ] 1. [待处理] 定位问题\nDone.", nil)
		return mem
	}

	t.Run("events_take_precedence", func(t *testing.T) {
		m := restoreTestModel(t)
		info := memory.RolloutInfo{
			SessionID: "sess-tl",
			Finished:  true,
			ThinklinkEntries: []memory.RolloutThinklinkEntry{
				{Kind: "user_input", Content: "fix the login bug", Step: 0},
				{Kind: "thought_plan", Content: "## Thought & Plan\n* [>] 1. 定位问题", Step: 3},
			},
		}
		restoreFromRollout(m, newMem(), info, "/fake/sess-tl.jsonl", "director")
		if len(m.thinklinkEntries) != 2 {
			t.Fatalf("thinklinkEntries len = %d, want 2", len(m.thinklinkEntries))
		}
		if m.thinklinkEntries[0].Kind != "user_input" || m.thinklinkEntries[0].Step != 0 {
			t.Errorf("entry[0] = %+v", m.thinklinkEntries[0])
		}
		if m.thinklinkEntries[1].Step != 3 || !strings.Contains(m.thinklinkEntries[1].Content, "[>]") {
			t.Errorf("entry[1] = %+v", m.thinklinkEntries[1])
		}
	})

	t.Run("legacy_files_fall_back_to_parsing", func(t *testing.T) {
		m := restoreTestModel(t)
		info := memory.RolloutInfo{SessionID: "sess-tl-legacy", Finished: true}
		restoreFromRollout(m, newMem(), info, "/fake/sess-tl-legacy.jsonl", "director")
		// 回退路径：human 消息 → user_input，assistant 的 Thought & Plan 块 → thought_plan
		var kinds []string
		for _, e := range m.thinklinkEntries {
			kinds = append(kinds, e.Kind)
		}
		if len(kinds) < 2 || kinds[0] != "user_input" || kinds[1] != "thought_plan" {
			t.Errorf("fallback kinds = %v, want [user_input thought_plan ...]", kinds)
		}
	})
}

// ─── B5: resume 列表按 taskID 归并 ───────────────────────────────────────────

func mkItem(path, taskID, agent string, mod time.Time) memory.RolloutSessionInfo {
	return memory.RolloutSessionInfo{
		Path: path, TaskID: taskID, AgentName: agent,
		ModTime: mod, StartedAt: mod, Preview: "preview:" + path,
	}
}

func TestBuildResumeGroups(t *testing.T) {
	base := time.Now()
	items := []memory.RolloutSessionInfo{
		mkItem("/r/new_sub_coder.jsonl", "T1", "coder", base), // 子文件先出现在清单里
		mkItem("/r/new_director.jsonl", "T1", "director", base.Add(time.Second)),
		mkItem("/r/legacy.jsonl", "", "", base.Add(2*time.Second)), // 存量：自成组
	}
	groups := buildResumeGroups(items)
	if len(groups) != 2 {
		t.Fatalf("groups len = %d, want 2", len(groups))
	}
	// 组间按 head mtime 倒序：legacy（最新）在前
	if groups[0].key != "file:/r/legacy.jsonl" {
		t.Errorf("groups[0].key = %q, want legacy file key first", groups[0].key)
	}
	// T1 组：head 应为主会话文件（尽管子文件先到），成员为子文件
	var g1 resumeGroup
	for _, g := range groups {
		if g.key == "task:T1" {
			g1 = g
		}
	}
	if g1.head.Path != "/r/new_director.jsonl" {
		t.Errorf("T1 head = %q, want main session file", g1.head.Path)
	}
	if len(g1.members) != 1 || g1.members[0].Path != "/r/new_sub_coder.jsonl" {
		t.Errorf("T1 members = %+v, want coder sub file", g1.members)
	}
}

func TestResumeExpandAndRows(t *testing.T) {
	base := time.Now()
	// 每个子测试拿自己的 model：旧写法四个子测试串用同一个 m 且严格依赖执行顺序
	// （折叠→展开→点成员→收起），顺序一变就会掩盖失败。
	// expanded 就是"上一条子测试留下的状态"，现在改成显式前置条件。
	newRowsModel := func(t *testing.T, expanded bool) *model {
		t.Helper()
		m := restoreTestModel(t)
		m.resumeGroups = buildResumeGroups([]memory.RolloutSessionInfo{
			mkItem("/r/a_director.jsonl", "T1", "director", base),
			mkItem("/r/a_sub_coder.jsonl", "T1", "coder", base.Add(time.Second)),
			mkItem("/r/a_sub_repo.jsonl", "T1", "repo", base.Add(2*time.Second)),
		})
		m.resumeExpanded = make(map[string]bool)
		if expanded {
			m.resumeExpanded["task:T1"] = true
		}
		m.rebuildResumeRows()
		return m
	}

	t.Run("collapsed_shows_head_only", func(t *testing.T) {
		m := newRowsModel(t, false)
		if len(m.resumeRows) != 1 || !m.resumeRows[0].isHead {
			t.Fatalf("rows = %+v, want single head row", m.resumeRows)
		}
	})

	t.Run("enter_on_head_expands", func(t *testing.T) {
		m := newRowsModel(t, false)
		m.resumeCursor = 0
		m, _ = resumeHandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}, m)
		if len(m.resumeRows) != 3 {
			t.Fatalf("rows after expand = %d, want 3", len(m.resumeRows))
		}
		if m.resumeLoading {
			t.Error("loading should stay false on expand")
		}
	})

	t.Run("enter_on_member_loads_file", func(t *testing.T) {
		m := newRowsModel(t, true) // 第一行是组头，第二行是成员
		m.resumeCursor = 1
		m, _ = resumeHandleKey(tea.KeyPressMsg{Code: tea.KeyEnter}, m)
		if !m.resumeLoading {
			t.Fatal("enter on member should trigger load (resumeLoading=true)")
		}
	})

	t.Run("space_toggles", func(t *testing.T) {
		m := newRowsModel(t, true)
		m.resumeCursor = 0
		m, _ = resumeHandleKey(tea.KeyPressMsg{Code: tea.KeySpace}, m)
		if len(m.resumeRows) != 1 {
			t.Fatalf("rows after collapse = %d, want 1", len(m.resumeRows))
		}
	})
}

// ─── 恢复更短的历史 ──────────────────────────────────────────────────────────

// TestApplyRestoredMemory_ShorterHistoryAfterLongerOne 恢复一个条目比当前会话更少的历史：
// applyRestoredMemory 清了 logEntries，但排版缓存 contentParts 与行索引是按上一批条目建的，
// 不清就是"条目 2 条、缓存 6 条"的失配现场。生产里的崩溃点是 View 纠正视口宽度之后的
// 第一次 bodySync —— 修前 index out of range [2] with length 2。
func TestApplyRestoredMemory_ShorterHistoryAfterLongerOne(t *testing.T) {
	defer failOnPanic(t)

	m := restoreTestModel(t)
	applyRestoredMemory(m, restoreMemory(
		memory.ChatMessage{Type: memory.MessageTypeHuman, Content: "长会话的任务", Timestamp: time.Now()},
		restoreAssistant("先看第一个文件", restoreCall("c1", "read_file", `{"target_file":"a.go"}`)),
		restoreToolResult("c1", "package a\n", nil),
		restoreAssistant("再看第二个文件", restoreCall("c2", "read_file", `{"target_file":"b.go"}`)),
		restoreToolResult("c2", "package b\n", nil),
		restoreAssistant("两个都看完了"),
	), "task-long", "/repo", "Resumed")

	longEntries := len(m.logEntries)
	if longEntries < 4 || len(m.contentParts) != longEntries {
		t.Fatalf("夹具的长会话恢复后应等长且够长：entries=%d parts=%d", longEntries, len(m.contentParts))
	}

	applyRestoredMemory(m, restoreMemory(
		memory.ChatMessage{Type: memory.MessageTypeHuman, Content: "换一个短会话", Timestamp: time.Now()},
		restoreAssistant("这次只有一条回复"),
	), "task-short", "/repo", "Resumed")

	if len(m.logEntries) >= longEntries {
		t.Fatalf("夹具要求第二个会话条目更少：%d vs %d", len(m.logEntries), longEntries)
	}
	if len(m.contentParts) != len(m.logEntries) {
		t.Errorf("contentParts=%d，logEntries=%d：恢复必须把上一批的排版缓存一起清掉",
			len(m.contentParts), len(m.logEntries))
	}
	if len(m.dirtyEntryIndices) != 0 {
		t.Errorf("dirtyEntryIndices=%v：残留的脏下标属于另一批条目", m.dirtyEntryIndices)
	}

	// 复刻 View 的首次排版：按 bodyLayout 纠正宽度，然后 bodySync（tui_view.go 的那对调用）。
	m.viewport.SetWidth(m.viewport.Width() - 6)
	m.bodySync()

	total := m.bodyTotalLines()
	text := m.bodyText(0, total)
	if got := len(strings.Split(text, "\n")); got != total {
		t.Errorf("行数统计与取词不同源：bodyTotalLines=%d 但全文 %d 行", total, got)
	}
	if !strings.Contains(text, "这次只有一条回复") {
		t.Errorf("恢复出来的回复没落到正文里：\n%s", stripANSI(text))
	}
	if strings.Contains(text, "先看第一个文件") {
		t.Error("上一个会话的残留条目还在正文里")
	}
}

// TestApplyRestoredMemory_ViewMatrix 把恢复接进完整的那条生产链：View() 才是崩溃现场
// （bodyTotalLines → bodySync → bodyBlockFor）。长 / 短 / 更长 / 空四种形状各渲染两次，
// 每一帧的行数必须正好等于终端高度，且不许留着上一个会话的条目。
func TestApplyRestoredMemory_ViewMatrix(t *testing.T) {
	defer failOnPanic(t)

	long := restoreMemory(
		memory.ChatMessage{Type: memory.MessageTypeHuman, Content: "长会话的任务", Timestamp: time.Now()},
		restoreAssistant("先看第一个文件", restoreCall("c1", "read_file", `{"target_file":"a.go"}`)),
		restoreToolResult("c1", "package a\n", nil),
		restoreAssistant("再看第二个文件", restoreCall("c2", "read_file", `{"target_file":"b.go"}`)),
		restoreToolResult("c2", "package b\n", nil),
		restoreAssistant("两个都看完了"),
	)
	short := restoreMemory(
		memory.ChatMessage{Type: memory.MessageTypeHuman, Content: "短会话的任务", Timestamp: time.Now()},
		restoreAssistant("这次只有一条回复"),
	)

	for _, s := range []struct {
		name string
		mem  *memory.ConversationMemory
	}{
		{"long", long},
		{"short", short},
		{"empty", restoreMemory()},
		{"long-again", long},
	} {
		// 每轮都从"已经恢复了一个长会话"的 model 起步：那才是会留下排版缓存残留的现场。
		m := chromeTestModel(t, 120, 36)
		m.taskManager = http.NewTaskManager(nil, 0)
		applyRestoredMemory(m, long, "task-seed", "/repo", "Resumed")
		applyRestoredMemory(m, s.mem, "task-"+s.name, "/repo", "Resumed")

		if len(m.contentParts) != len(m.logEntries) {
			t.Errorf("%s: contentParts=%d，logEntries=%d", s.name, len(m.contentParts), len(m.logEntries))
		}
		// 连续两帧：第一帧重排，第二帧走缓存 —— 两步都不许崩，行数也不许漂。
		for frame := 0; frame < 2; frame++ {
			lines := strings.Split(m.View().Content, "\n")
			if len(lines) != 36 {
				t.Errorf("%s frame=%d: 整屏 %d 行，want 36", s.name, frame, len(lines))
			}
		}
		if s.name != "long" && s.name != "long-again" {
			if got := stripANSI(m.View().Content); strings.Contains(got, "先看第一个文件") {
				t.Errorf("%s: 正文里还留着上一个会话的条目", s.name)
			}
		}
	}
}
