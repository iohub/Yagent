package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

// ⧗ LATENCY 段的排版闸门。网格、让位序、时间序、冷静规则、⊘N —— 每一条都对应
// docs/dashboard-latency-panel.md 里的一个决定，改掉决定必须让这里红，
// 而不是"看起来也还行"。

// latencyFixture 造一个宽屏模型：有 token 用量、有 timeline、窗口里 n 条样本。
func latencyFixture(t *testing.T, durs []time.Duration) *model {
	t.Helper()
	testLang(t)
	m := newTestModel()
	m.termWidth, m.termHeight = 160, 40
	m.taskRunning = true
	m.inputTokens = 900000
	m.outputTokens = 120000
	m.tokenUsagePerAgent = map[string]*AgentTokenUsage{
		"Director": dashboardAgentUsage("Director", 300000, 60000, 0, 0),
	}
	m.addTimelineEntry(&TimelineEntry{ID: "1", Kind: TimelineKindTool,
		Status: ToolStatusSuccess, Name: "read_file", Duration: time.Second})
	base := baseTime()
	for i, d := range durs {
		m.latency.observe(latencySample{
			id:    "s",
			label: "read_file",
			dur:   d,
			ts:    base.Add(time.Duration(i) * time.Second),
			agent: "Coding",
		})
	}
	return m
}

// ── 几何 ──

// TestLatencyGlyphsAreOneCell 段名与两个行首标记都必须占一格。
// ⏱/💭 那类 emoji 形态是两格，换上去就整行错位 —— 而肉眼看不出来。
func TestLatencyGlyphsAreOneCell(t *testing.T) {
	for _, g := range []string{"⧗", "▲", "◉", "⊘"} {
		if w := lipgloss.Width(g); w != 1 {
			t.Errorf("%q 占 %d 格, want 1", g, w)
		}
	}
}

// TestLatencyRows_NeverExceedInner 每一行都不许超过内宽：lipgloss 的 Width()
// 会把超宽的行再折一次，多出来的行在 AltScreen 里看不见也滚不回去
// （internal/tui/AGENTS.md 硬约束 11）。
func TestLatencyRows_NeverExceedInner(t *testing.T) {
	s := latencyFixture(t, []time.Duration{
		400 * time.Millisecond, 2 * time.Second, 8900 * time.Millisecond,
	}).latency.snapshot(baseTime())

	for _, inner := range []int{20, 24, 28, 32, 36, 40, 46, 60, 80, 120, 240} {
		for tier := latencyTierFull; tier <= latencyTierLast; tier++ {
			for _, row := range renderLatencyTier(s, latencyPanelSpec{innerWidth: inner}, tier) {
				if w := lipgloss.Width(row); w > inner {
					t.Errorf("inner=%d tier=%d 的行宽 %d 超出：%q", inner, tier, w, stripANSI(row))
				}
			}
		}
	}
}

// TestLatencySectionRows_SumToPanelWidth 侧栏那档（inner 恒 46）里，带子与分隔线
// 必须逐格铺满 —— 短一截就是视觉裂缝。
func TestLatencySectionRows_SumToPanelWidth(t *testing.T) {
	s := latencyFixture(t, []time.Duration{time.Second, 6 * time.Second}).latency.snapshot(baseTime())
	rows := renderLatencyTier(s, latencyPanelSpec{innerWidth: dashboardPanelWidth - 4}, latencyTierFull)
	if len(rows) < 2 {
		t.Fatalf("至少要有分隔线+带子，实际 %d 行", len(rows))
	}
	if w := lipgloss.Width(rows[0]); w != 46 {
		t.Errorf("分隔线宽 %d, want 46", w)
	}
	if w := lipgloss.Width(rows[1]); w != 46 {
		t.Errorf("带子行宽 %d, want 46：%q", w, stripANSI(rows[1]))
	}
}

// ── 分布的形状 ──

// TestLatencyHeat_IsChronologicalNotSorted 高度必须按**输入顺序**归一，不许先排序。
// 排序后永远是同一道楼梯，形状就没信息了；这个决定只能靠这条测试守住。
func TestLatencyHeat_IsChronologicalNotSorted(t *testing.T) {
	m := latencyFixture(t, []time.Duration{
		3 * time.Second, 1 * time.Second, 2 * time.Second,
	})
	s := m.latency.snapshot(baseTime())
	// lo=1s hi=3s，span=2s → (3,1,2) 归一到 (7,0,4)（八档里取 0..7）
	want := []int{7, 0, 4}
	if len(s.levels) != len(want) {
		t.Fatalf("levels = %v, want %v", s.levels, want)
	}
	for i := range want {
		if s.levels[i] != want[i] {
			t.Fatalf("levels = %v, want %v —— 时间序被打乱（排序后就是 %v）",
				s.levels, want, []int{0, 4, 7})
		}
	}
}

// TestLatencyCalmWindowHasNoHotCell 整批都低于冷静门槛时一律最暗档：
// 高速会话里 400ms 的 read_file 不该被染成"慢"。
func TestLatencyCalmWindowHasNoHotCell(t *testing.T) {
	m := latencyFixture(t, []time.Duration{
		100 * time.Millisecond, 400 * time.Millisecond, 900 * time.Millisecond, 1900 * time.Millisecond,
	})
	s := m.latency.snapshot(baseTime())
	if s.maxDur >= latencyCalmMax {
		t.Fatalf("夹具没落在冷静区：max=%v", s.maxDur)
	}
	for i, b := range s.bands {
		if b != 0 {
			t.Errorf("第 %d 条档位 = %d, want 0（冷静窗口里不许出现暖色）", i, b)
		}
	}

	// 反向的一半：越过门槛就必须开始上色，否则"冷静"其实是"永不上色"。
	hot := latencyFixture(t, []time.Duration{100 * time.Millisecond, 15 * time.Second}).latency.snapshot(baseTime())
	if hot.bands[1] == 0 {
		t.Errorf("15s 那条还在最暗档：bands=%v", hot.bands)
	}
}

// TestLatencyBands_EachThresholdIsReachable 四档各有一条越界数据（根 AGENTS.md §11 坑 15：
// 校验规则本身必须能被触发）。
func TestLatencyBands_EachThresholdIsReachable(t *testing.T) {
	for _, tc := range []struct {
		dur  time.Duration
		want int
	}{
		{500 * time.Millisecond, 0},
		{999 * time.Millisecond, 0},
		{1 * time.Second, 1},
		{2999 * time.Millisecond, 1},
		{3 * time.Second, 2},
		{9999 * time.Millisecond, 2},
		{10 * time.Second, 3},
		{90 * time.Second, 3},
	} {
		if got := latencyBand(tc.dur); got != tc.want {
			t.Errorf("latencyBand(%v) = %d, want %d", tc.dur, got, tc.want)
		}
	}
}

// TestLatencyP50IsMedianNotMax n=9 时 p90 会退化成 max；中位数不会。
// 断言的是"报的数不是榜首那个数"，换回 p90 即红。
func TestLatencyP50IsMedianNotMax(t *testing.T) {
	// 前 8 条都在毫秒级：写成裸整数就是纳秒，中位数会跟着错一个量级。
	durs := []time.Duration{
		100 * time.Millisecond, 200 * time.Millisecond, 300 * time.Millisecond,
		400 * time.Millisecond, 500 * time.Millisecond, 600 * time.Millisecond,
		700 * time.Millisecond, 800 * time.Millisecond, 12 * time.Second,
	}
	s := latencyFixture(t, durs).latency.snapshot(baseTime())
	if s.count != 9 {
		t.Fatalf("n = %d, want 9", s.count)
	}
	if s.p50 == s.maxDur {
		t.Errorf("p50 与 max 同为 %v —— 分位数退化成了榜首那个数", s.p50)
	}
	if s.p50 != 500*time.Millisecond {
		t.Errorf("p50 = %v, want 500ms（9 条里的第 5 小）", s.p50)
	}
}

// ── 两榜的归属 ──

// TestLatencyNestedDelegateStaysOffBoard 子事件在窗口里可见的那些委派不进具名榜：
// 它会压过一切叶子，而它自己已经由 ◉ 行承载。
func TestLatencyNestedDelegateStaysOffBoard(t *testing.T) {
	base := baseTime()
	var l latencyStats
	l.observe(latencySample{id: "call_a", label: "delegate_coding", dur: 12 * time.Second,
		ts: base, agent: "director", isDelegate: true})
	l.observe(latencySample{id: "r1", label: "read_file", dur: time.Second,
		ts: base, agent: "Coding", delegation: "call_a"})

	s := l.snapshot(base)
	for _, b := range s.board {
		if b.id == "call_a" {
			t.Errorf("有子事件的委派进了具名榜：%+v", s.board)
		}
	}
	if len(s.board) != 1 || s.board[0].id != "r1" {
		t.Errorf("具名榜 = %+v, want 只剩 read_file", s.board)
	}
}

// TestLatencyOrphanDelegateStaysOnBoard 反向的一半：恢复出来的会话没有子事件
// （子 Agent 的调用在另一个 rollout 文件里），这时委派必须仍然上榜 ——
// 否则 sparkline 有尖峰而榜上没有名字。
func TestLatencyOrphanDelegateStaysOnBoard(t *testing.T) {
	base := baseTime()
	var l latencyStats
	l.observe(latencySample{id: "call_a", label: "delegate_coding", dur: 12 * time.Second,
		ts: base, agent: "director", isDelegate: true})

	s := l.snapshot(base)
	if len(s.board) != 1 || s.board[0].id != "call_a" {
		t.Errorf("没有子事件时委派被错排除了：board=%+v", s.board)
	}
	if s.runs[0].nested() {
		t.Error("一个子事件都没观察到却被当成嵌套容器")
	}
}

// TestLatencyRunRowCarriesOwnStats ◉ 行的轮内计数与轮内最慢值来自各自那一轮，
// 两行互不污染。
func TestLatencyRunRowCarriesOwnStats(t *testing.T) {
	base := baseTime()
	var l latencyStats
	l.observe(latencySample{id: "call_a", label: "delegate_coding", dur: 12 * time.Second,
		ts: base, agent: "director", isDelegate: true})
	l.observe(latencySample{id: "call_b", label: "delegate_repo", dur: 8 * time.Second,
		ts: base.Add(time.Second), agent: "director", isDelegate: true})
	l.observe(latencySample{id: "r1", label: "read_file", dur: 8900 * time.Millisecond,
		ts: base, agent: "Coding", delegation: "call_a"})
	l.observe(latencySample{id: "r2", label: "list_dir", dur: 300 * time.Millisecond,
		ts: base, agent: "Coding", delegation: "call_a"})
	l.observe(latencySample{id: "r3", label: "grep", dur: 2 * time.Second,
		ts: base, agent: "Repo", delegation: "call_b"})

	s := l.snapshot(base)
	rows := strings.Join(stripANSILines(latencyRunRows(s, 2, dashboardPanelWidth-4)), "\n")
	if !strings.Contains(rows, "2c") || !strings.Contains(rows, "▲8.9s") {
		t.Errorf("call_a 那行没带上它自己的账：%q", rows)
	}
	if !strings.Contains(rows, "1c ▲2.0s") {
		t.Errorf("call_b 那行没带上它自己的账：%q", rows)
	}
	// 名字来自子事件的 From，不是从工具名截出来的
	if !strings.Contains(rows, "Coding·") || !strings.Contains(rows, "Repo·") {
		t.Errorf("轮次名字不对：%q", rows)
	}
}

// TestLatencyOverlapsStampParallelRuns 并行才标 ⊘N：串行时各行时长可以相加，
// 界面上就不该出现一个谁都会误读的符号。
func TestLatencyOverlapsStampParallelRuns(t *testing.T) {
	base := baseTime()

	serial := latencyStats{}
	serial.observe(latencySample{id: "a", label: "delegate_coding", dur: 5 * time.Second,
		ts: base, agent: "director", isDelegate: true})
	serial.observe(latencySample{id: "b", label: "delegate_repo", dur: 5 * time.Second,
		ts: base.Add(5 * time.Second), agent: "director", isDelegate: true})
	if got := serial.snapshot(base.Add(time.Minute)).parallel; got != 1 {
		t.Errorf("两段首尾相接被判成并行：parallel=%d, want 1（⊘ 会假报）", got)
	}

	parallel := latencyStats{}
	parallel.observe(latencySample{id: "a", label: "delegate_coding", dur: 8 * time.Second,
		ts: base.Add(8 * time.Second), agent: "director", isDelegate: true})
	parallel.observe(latencySample{id: "b", label: "delegate_repo", dur: 6 * time.Second,
		ts: base.Add(6 * time.Second), agent: "director", isDelegate: true})
	if got := parallel.snapshot(base.Add(time.Minute)).parallel; got != 2 {
		t.Errorf("两段同时在跑却没标出来：parallel=%d, want 2", got)
	}

	// 而标出来必须真的落到带子上
	m := latencyFixture(t, []time.Duration{time.Second, 3 * time.Second})
	m.latency.observe(latencySample{id: "a", label: "delegate_coding", dur: 8 * time.Second,
		ts: base.Add(8 * time.Second), agent: "director", isDelegate: true})
	m.latency.observe(latencySample{id: "b", label: "delegate_repo", dur: 6 * time.Second,
		ts: base.Add(6 * time.Second), agent: "director", isDelegate: true})
	band := stripANSI(latencyTitleRow(m.latency.snapshot(base.Add(time.Minute)), 46))
	if !strings.Contains(band, "⊘2") {
		t.Errorf("带子右段没标重叠：%q", band)
	}
}

// ── 让位阶梯与行预算 ──

// TestLatencyTierLadderRows 每一档砍掉正好一行，砍的顺序是 轮次 → 事件 → 分布。
// 多一行就会把 Activity 挤掉，少一行就是白让 —— 所以逐档数行数。
// 夹具喂满 2 轮 + 4 条样本，让 T0 真的能长满 7 行。
func TestLatencyTierLadderRows(t *testing.T) {
	base := baseTime()
	var l latencyStats
	l.observe(latencySample{id: "call_a", label: "delegate_coding", dur: 20 * time.Second,
		ts: base.Add(20 * time.Second), agent: "director", isDelegate: true})
	l.observe(latencySample{id: "call_b", label: "delegate_repo", dur: 18 * time.Second,
		ts: base.Add(18 * time.Second), agent: "director", isDelegate: true})
	for i, d := range []time.Duration{15 * time.Second, 9 * time.Second, time.Second, 500 * time.Millisecond} {
		l.observe(latencySample{id: "s", label: "read_file", dur: d,
			ts: base.Add(time.Duration(i) * time.Second), agent: "Coding", delegation: "call_a"})
	}
	// call_b 也得有一个子事件：没有子事件、又不知道是哪个 Agent 的轮次
	// 不占 ◉ 行（那时长已经在具名榜上了），阶梯行数就对不上。
	l.observe(latencySample{id: "s2", label: "grep", dur: 6 * time.Second,
		ts: base, agent: "Repo", delegation: "call_b"})
	s := l.snapshot(base)
	if len(s.board) < 2 {
		t.Fatalf("夹具没喂满：board=%d runs=%d", len(s.board), len(s.runs))
	}

	for tier, want := range map[int]int{
		latencyTierFull:     7, // 分隔 带 分布 ▲▲ ◉◉
		latencyTierNoRun2:   6,
		latencyTierNoBoard2: 5,
		latencyTierNoHeat:   4,
		latencyTierNoRun1:   3,
		latencyTierBandOnly: 2,
	} {
		got := len(renderLatencyTier(s, latencyPanelSpec{innerWidth: 46}, tier))
		if got != want {
			t.Errorf("tier=%d 给 %d 行, want %d：%q", tier, got, want,
				stripANSILines(renderLatencyTier(s, latencyPanelSpec{innerWidth: 46}, tier)))
		}
	}

	// 预算决定档位：给 2 行就只能剩"分隔+带子"
	rows := buildLatencyBlock(s, latencyPanelSpec{innerWidth: 46, maxRows: 2})
	if len(rows) != 2 || !strings.HasPrefix(stripANSI(rows[1]), "⧗ LATENCY") {
		t.Errorf("预算 2 行时形态不对：%q", stripANSILines(rows))
	}
	// 预算 1 行：整段让位，绝不画空壳
	if rows := buildLatencyBlock(s, latencyPanelSpec{innerWidth: 46, maxRows: 1}); rows != nil {
		t.Errorf("预算 1 行时该整段消失，实际 %q", stripANSILines(rows))
	}
}

// TestLatencyIdleCostsExactlyTwoRows 一条时长都没测到时：恰好"分隔线 + 带子"两行，
// 带子上写着 total —（这段存在、还没数据必须是看得见的）。
func TestLatencyIdleCostsExactlyTwoRows(t *testing.T) {
	m := latencyFixture(t, nil)
	s := m.latency.snapshot(baseTime())
	if s.hasData() {
		t.Fatal("夹具不该有样本")
	}
	rows := buildLatencyBlock(s, latencyPanelSpec{innerWidth: 46, maxRows: 20})
	if len(rows) != 2 {
		t.Fatalf("空闲态 = %d 行, want 2：%q", len(rows), stripANSILines(rows))
	}
	joined := strings.Join(stripANSILines(rows), "\n")
	if !strings.Contains(joined, "⧗ LATENCY") || !strings.Contains(joined, "total —") {
		t.Errorf("空闲态带子不对：%q", joined)
	}
	for _, unwanted := range []string{"▲", "◉", "last "} {
		if strings.Contains(joined, unwanted) {
			t.Errorf("空闲态不该有 %q：%q", unwanted, joined)
		}
	}
}

// TestLatency_NeverStarvesTokensAndActivity 优先级是 token > Activity > ⧗/⧉：
// 加第四个抢行方之后，矮终端里 ◆ 与 ⚙ 仍必须在，而 ⧗ 只会整段消失。
// 这条与 TestDashboard_ActivityNeverStarvedByCompress 同一手法扫 height。
func TestLatency_NeverStarvesTokensAndActivity(t *testing.T) {
	for height := 8; height <= 24; height++ {
		m := latencyFixture(t, []time.Duration{
			500 * time.Millisecond, 3 * time.Second, 12 * time.Second, 8 * time.Second,
			2 * time.Second, 6 * time.Second, 1 * time.Second,
		})
		// 一条在跑，避开"运行中绕过缓存"那条分支的干扰
		m.timelineEntries[0].Status = ToolStatusSuccess
		lines := dashboardLines(m, dashboardPanelWidth, height)
		joined := strings.Join(lines, "\n")

		if !strings.Contains(joined, "◆ Tokens") {
			t.Errorf("height=%d 时 ◆ 段不见了\n----\n%s----", height, joined)
		}
		if !strings.Contains(joined, "⚙ Activity") {
			t.Errorf("height=%d 时 ⚙ Activity 被 ⧗ 段挤掉了\n----\n%s----", height, joined)
		}
		// ⧗ 的带子不许变成孤零零一行（要么带着自己的分隔线，要么后面有内容）——
		// 与 ⧉ 段同一条禁令，复用它的三个 helper 而不是再抄一份。
		if row := indexOfLine(lines, "⧗ LATENCY"); row >= 0 {
			hasSepBefore := row >= 1 && isPureSeparator(panelContent(lines[row-1]))
			hasContentAfter := row+1 <= len(lines)-2 && panelContent(lines[row+1]) != ""
			if !hasSepBefore && !hasContentAfter {
				t.Errorf("height=%d 时 ⧗ 段是孤零零一行带子（row=%d）\n----\n%s----", height, row, joined)
			}
		}
	}
}

// TestLatencySection_EntersDashboardCacheKey 只动耗时账、其他一个都不改，
// 面板必须重画。删掉 key += "|lat:" 那一行，这条立刻红。
func TestLatencySection_EntersDashboardCacheKey(t *testing.T) {
	m := latencyFixture(t, []time.Duration{time.Second, 2 * time.Second})
	// 没有运行中的条目，缓存这条路才会被走到
	m.taskRunning = false

	first := stripANSI(m.renderDashboard(dashboardPanelWidth, 30))
	if !strings.Contains(first, "⧗ LATENCY") {
		t.Fatalf("第一段渲染里就没有 ⧗：\n%s", first)
	}

	m.latency.observe(latencySample{id: "slow", label: "run_bash", dur: 45 * time.Second,
		ts: baseTime(), agent: "Coding"})
	second := stripANSI(m.renderDashboard(dashboardPanelWidth, 30))
	if second == first {
		t.Errorf("耗时账变了而面板逐字不变 —— ⧗ 的签名没进缓存键\n----\n%s----", second)
	}
	if !strings.Contains(second, "run_bash") {
		t.Errorf("新样本没上具名榜：\n%s", second)
	}
}
