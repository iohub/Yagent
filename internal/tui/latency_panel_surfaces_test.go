package tui

import (
	"strings"
	"testing"
	"time"

	"yagent/internal/memory"

	"charm.land/lipgloss/v2"
)

// 两个"次要表面"的闸门：侧栏不可见时底部那 1 行，以及 /resume 恢复路径的喂数。

// ── 底部那 1 行 ──

func footerLatencyModel(t *testing.T, w, h int) *model {
	t.Helper()
	m := chromeTestModel(t, w, h)
	m.taskStarted = true
	m.inputTokens = 120000
	m.outputTokens = 34000
	m.totalInputTokens = 154000
	m.tokenUsagePerAgent = map[string]*AgentTokenUsage{
		"Director": {AgentName: "Director", TotalInputTokens: 154000, OutputTokens: 34000},
	}
	base := time.Now()
	m.latency.taskStart = base.Add(-210 * time.Second)
	m.latency.endedAt = base
	for i, d := range []time.Duration{900 * time.Millisecond, 8900 * time.Millisecond, 21 * time.Second} {
		m.latency.observe(latencySample{id: "s", label: "search_by_regex", dur: d,
			ts: base.Add(time.Duration(i) * time.Second), agent: "Coding"})
	}
	return m
}

// TestLatencyFooterRow_AppearsWhenSidebarHidden 窄屏（侧栏不可见）时底部面板必须带上
// ⧗ 那 1 行 —— 否则耗时信息在最需要它的屏幕上整个消失，而没有任何提示。
func TestLatencyFooterRow_AppearsWhenSidebarHidden(t *testing.T) {
	m := footerLatencyModel(t, 100, 34)
	if m.dashboardVisible() {
		t.Fatal("夹具本该让侧栏不可见，footer 这条路才走得到")
	}
	block := m.tokenDashboardBlock()
	if !strings.Contains(stripANSI(block), "⧗") {
		t.Errorf("底部面板没有 ⧗ 那行：\n%s", stripANSI(block))
	}
	// total 是这一行唯一必然存在的数字
	if !strings.Contains(stripANSI(block), "total 3.5m") {
		t.Errorf("底部那行没带 total：\n%s", stripANSI(block))
	}
}

// TestLatencyFooterRow_NeverExceedsInner 宽度矩阵：那 1 行不许撑破面板内容宽，
// 也不许让 footer 的行数与 computeFooterHeight 算出的不一致（差一行整屏抖动）。
func TestLatencyFooterRow_NeverExceedsInner(t *testing.T) {
	for _, w := range []int{30, 40, 44, 50, 62, 80, 120, 240} {
		m := footerLatencyModel(t, w, 30)
		inner := m.footerInnerWidth()
		row := latencySummaryRow(m.latencySnapshot(), inner)
		if row == "" {
			t.Errorf("w=%d 时那行为空（total 明明有 3.5m）", w)
			continue
		}
		if got := lipgloss.Width(row); got > inner {
			t.Errorf("w=%d inner=%d 行宽 %d 超出：%q", w, inner, got, stripANSI(row))
		}
	}
}

// TestLatencyFooterRow_CountedInFooterHeight 那一行必须被 computeFooterHeight 数到：
// 两处不一致的代价是 viewport 高度算错、整屏在滚动时抖动（internal/tui/AGENTS.md 硬约束 3）。
// 断言的是"加了 ⧗ 行之后 footer 恰好长 1 行"，与面板怎么拼无关。
func TestLatencyFooterRow_CountedInFooterHeight(t *testing.T) {
	with := footerLatencyModel(t, 100, 34)
	with.invalidateFooterCache()
	hWith := with.computeFooterHeight()

	without := footerLatencyModel(t, 100, 34)
	without.latency = latencyStats{}
	without.invalidateFooterCache()
	hWithout := without.computeFooterHeight()

	if !strings.Contains(stripANSI(with.tokenDashboardBlock()), "⧗") {
		t.Fatal("夹具的底部面板没有 ⧗ 行，下面的比较是空跑")
	}
	if strings.Contains(stripANSI(without.tokenDashboardBlock()), "⧗") {
		t.Fatal("清空耗时账后底部仍给 ⧗ 行，行数差就不是这一行造成的")
	}
	if got := hWith - hWithout; got != 1 {
		t.Errorf("加了 ⧗ 行后 footer 长了 %d 行, want 1（computeFooterHeight 没数到它 → 整屏会抖）", got)
	}
}

// TestLatencyFooterWidthLadder 窄档逐级去细节，最窄时仍要说清"跑了多久"。
func TestLatencyFooterWidthLadder(t *testing.T) {
	m := footerLatencyModel(t, 100, 34)
	s := m.latencySnapshot()

	narrow := stripANSI(latencySummaryRow(s, 28))
	if !strings.Contains(narrow, "total") {
		t.Errorf("28 列时连 total 都没了：%q", narrow)
	}
	if strings.Contains(narrow, "▲") || strings.Contains(narrow, "p50") {
		t.Errorf("28 列还留着细节段：%q", narrow)
	}

	wide := stripANSI(latencySummaryRow(s, 100))
	for _, want := range []string{"▲", "p50"} {
		if !strings.Contains(wide, want) {
			t.Errorf("100 列少了 %q：%q", want, wide)
		}
	}
}

// TestLatencyFooterEmptyWhenNothingToShow 空闲态底部不给行：那里没有"这段存在"
// 的可见性诉求（侧栏才有），多一行空 ⧗ 只是把面板抬高一行。
func TestLatencyFooterEmptyWhenNothingToShow(t *testing.T) {
	m := chromeTestModel(t, 100, 34)
	if got := latencySummaryRow(m.latencySnapshot(), 94); got != "" {
		t.Errorf("空闲态底部给了行：%q", stripANSI(got))
	}
}

// ── /resume 喂数 ──

// TestApplyRestoredMemory_LatencySeededWithTimeline 与
// TestApplyRestoredMemory_TimelineRebuildAndMerge 成对：恢复出来的时间线有几条带时长的，
// ⧗ 段的窗口就得有几条样本。只重建时间线不记耗时账的话，恢复的会话里这段永远显示空闲态，
// 而时间线明明有 30 条 —— 测试与生产不走同一个载体的老病（根 AGENTS.md §11 坑 33）。
func TestApplyRestoredMemory_LatencySeededWithTimeline(t *testing.T) {
	m := restoreTestModel(t)
	msgs := []memory.ChatMessage{
		{Type: memory.MessageTypeHuman, Content: "go", Timestamp: time.Now()},
	}
	// 3 次可合并的 read_file（各带时长）+ 1 次 run_bash + 1 次没有时长的悬空调用
	for i, d := range []float64{1.2, 8.9, 0.4} {
		id := string(rune('a' + i))
		msgs = append(msgs,
			restoreAssistant("", restoreCall("c"+id, "read_file", `{"target_file":"f"}`)),
			restoreToolResult("c"+id, "body", map[string]interface{}{"duration_seconds": d}),
		)
	}
	msgs = append(msgs,
		restoreAssistant("", restoreCall("c9", "run_bash", `{"command":"ls"}`)),
		restoreToolResult("c9", "ok", map[string]interface{}{"duration_seconds": 21.0}),
		restoreAssistant("", restoreCall("c10", "list_dir", `{"path":"."}`)),
		restoreToolResult("c10", "ok", nil), // 没有时长：不该进窗口
	)

	applyRestoredMemory(m, restoreMemory(msgs...), "task-lat", "/repo", "Resumed")

	var timed int
	for _, e := range m.timelineEntries {
		if e.Duration > 0 {
			timed++
		}
		for _, sub := range e.SubEntries {
			if sub.Duration > 0 {
				timed++
			}
		}
	}
	if timed == 0 {
		t.Fatal("恢复的时间线一条时长都没有 —— 夹具失效，下面的断言会是空跑")
	}
	if len(m.latency.samples) != timed {
		t.Errorf("窗口 = %d 条样本, want %d（时间线里有 %d 条带时长）",
			len(m.latency.samples), timed, timed)
	}

	// 合并组里每一条都要单独进窗口：只按组记一次的话，分母就不是事件数了。
	s := m.latency.snapshot(time.Now())
	if s.count != timed {
		t.Errorf("签名里的分母 n = %d, want %d", s.count, timed)
	}

	// 恢复的会话没有子事件（在另一个 rollout 文件里），所以时长最慢的那条
	// 必须能在具名榜上叫出名字 —— 否则 sparkline 有尖峰而界面说不出是谁。
	if len(s.board) == 0 {
		t.Fatalf("具名榜是空的，而窗口里有 %d 条样本：%+v", len(s.samples), s.samples)
	}
	if s.board[0].label != "run_bash" {
		t.Errorf("榜首 = %q, want run_bash（21s 那条）", s.board[0].label)
	}

	// 而 total 保持"没计时"：恢复的会话我们不知道它原来跑了多久。
	if !m.latency.taskStart.IsZero() {
		t.Errorf("恢复路径不该盖任务起点：%v", m.latency.taskStart)
	}
	if got := latencyTotalText(s.total); got != "—" {
		t.Errorf("恢复会话的 total = %q, want —", got)
	}
}
