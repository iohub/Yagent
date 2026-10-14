package tui

import (
	"strings"
	"testing"

	"yagent/internal/memory"

	"charm.land/lipgloss/v2"
)

// compressGoldenModel 是与批准视觉稿等价的数据 fixture：三档累计 + 截断榜 + 8 个采样点。
//
// 采样值刻意选成 lo=100000 / hi=200000，归一化后正好落进 ▁▂▄▇█▃▃▄ 八档。
// Σsaved = 180200 + 1240000 = 1.4202m → 标题 "−1.42m"；pct = 1.4202m / 3.75m = 38%。
func compressGoldenModel() *model {
	return &model{
		contextWindow: 200000,
		runAgent:      "director",
		runTokensPerAgent: map[string]*AgentRunTokens{
			"director": {AgentName: "director", ContextTokens: 128400},
		},
		compress: compressStats{
			tiers: [compressTierCount]tierStat{
				{count: 2, saved: 180200, original: 900000},
				{count: 1, saved: 1240000, original: 3750000},
			},
			cutByTool: map[string]*cutStat{
				"read_file":       {tool: "read_file", count: 7, omitted: 120000},
				"search_by_regex": {tool: "search_by_regex", count: 2, omitted: 30000},
			},
			ctxSamples:  []int64{100000, 114286, 142857, 185714, 200000, 128571, 128571, 142857},
			peakCtx:     200000,
			maxOriginal: 3750000,
		},
	}
}

// compressGoldenWant 是侧栏 inner=46 的批准稿七行。
// 档位行的列网格：indent 2 + label 13 + gap 1 + saved 9(右) + gap 1 + count 3(右) + gap 1 + bar 16 = 46。
func compressGoldenWant() []string {
	return []string{
		"──────────────────────────────────────────────",
		"⧉ COMPRESS −1.42m (38%)                     T2",
		"  T1 rule         −180.2k  ×2 ▰▰▱▱▱▱▱▱▱▱▱▱▱▱▱▱",
		"  T2 emergency     −1.24m  ×1 ▰▰▰▰▰▰▰▰▰▰▰▰▰▰▱▱",
		"  T3 ultimate           0  ×0 ▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱▱",
		"  ⛁ ctx ▁▂▄▇█▃▃▄               128.4k / 200.0k",
		"  cut: read_file×7 search_by_regex×2",
	}
}

// compressFeed 直接喂一个档位事件（绕过 Update），返回并入后的快照。
func compressFeed(m *model, eventType string, original, compressed, saved int) compressSnapshot {
	m.recordCompression(eventType, map[string]interface{}{
		"original_tokens":   original,
		"compressed_tokens": compressed,
		"saved_tokens":      saved,
	})
	return m.compressSnapshot()
}

// TestCompressPanel_Golden 钉住侧栏 inner=46 的七行：逐行 stripANSI 比对，
// 并断言每行恰好 46 列（cut: 行是例外 —— 它不拉伸，36 列）。
func TestCompressPanel_Golden(t *testing.T) {
	rows := buildCompressBlock(compressGoldenModel().compressSnapshot(),
		compressPanelSpec{innerWidth: 46})
	want := compressGoldenWant()

	if len(rows) != len(want) {
		t.Fatalf("行数 = %d, want %d\n----\n%s----", len(rows), len(want),
			strings.Join(stripANSILines(rows), "\n"))
	}
	for i, w := range want {
		got := stripANSILines(rows)[i]
		if got != w {
			t.Errorf("line %d:\n got %q\nwant %q", i, got, w)
			continue
		}
		wantWidth := 46
		if i == len(want)-1 {
			wantWidth = 36 // cut: 行不拉伸
		}
		if lw := lipgloss.Width(rows[i]); lw != wantWidth {
			t.Errorf("line %d 宽度 = %d, want %d（%q）", i, lw, wantWidth, got)
		}
	}
}

// TestCompressPanel_HeightLadder 钉住高度阶梯：每档只砍一整行、行数不超预算、
// 标题永不被砍；连"分隔线+标题"都放不下时整块让位（绝不留空壳）。
func TestCompressPanel_HeightLadder(t *testing.T) {
	snap := compressGoldenModel().compressSnapshot()

	if full := buildCompressBlock(snap, compressPanelSpec{innerWidth: 46}); len(full) != 7 {
		t.Fatalf("不限高时应为 7 行，实际 %d", len(full))
	}

	for maxRows := 1; maxRows <= 7; maxRows++ {
		rows := buildCompressBlock(snap, compressPanelSpec{innerWidth: 46, maxRows: maxRows})
		joined := strings.Join(stripANSILines(rows), "\n")

		if maxRows == 1 {
			// 只剩分隔线或只剩标题都是空壳：整块让位。
			if len(rows) != 0 {
				t.Errorf("maxRows=1 必须整块为空，实际 %d 行\n----\n%s----", len(rows), joined)
			}
			continue
		}
		if len(rows) > maxRows {
			t.Errorf("maxRows=%d 渲染了 %d 行\n----\n%s----", maxRows, len(rows), joined)
		}
		if !strings.Contains(joined, "COMPRESS") {
			t.Errorf("maxRows=%d 标题行永不被砍\n----\n%s----", maxRows, joined)
		}
		if maxRows == 2 {
			if len(rows) != 2 {
				t.Errorf("maxRows=2 应只剩 分隔线+标题（2 行），实际 %d 行\n----\n%s----", len(rows), joined)
			}
			if !strings.HasPrefix(joined, strings.Repeat("─", 46)+"\n⧉ COMPRESS") {
				t.Errorf("maxRows=2 首行必须是分隔线、次行必须是标题\n----\n%s----", joined)
			}
			if strings.Contains(joined, "T1 rule") || strings.Contains(joined, "ctx ▁") || strings.Contains(joined, "cut:") {
				t.Errorf("maxRows=2 只剩标题，内容行必须全被砍掉\n----\n%s----", joined)
			}
		}
	}

	// NoIdle 档：到过 T1 时省掉 T2/T3 两行。
	// 直接比对两个档位（不靠 buildCompressBlock 自动选档），否则 fixture 的固定行
	// 数量会把档位选择结果带偏。
	onlyT1 := (&model{compress: compressStats{
		tiers: [compressTierCount]tierStat{{count: 1, saved: 200, original: 1000}},
	}}).compressSnapshot()
	spec := compressPanelSpec{innerWidth: 46}
	fullRows := stripANSILines(renderCompressTier(onlyT1, spec, compressTierFull))
	noIdleRows := stripANSILines(renderCompressTier(onlyT1, spec, compressTierNoIdle))
	if diff := len(fullRows) - len(noIdleRows); diff != 2 {
		t.Errorf("NoIdle 档应省 2 行，实际省 %d\nfull:\n%s\nnoIdle:\n%s", diff,
			strings.Join(fullRows, "\n"), strings.Join(noIdleRows, "\n"))
	}
	if got := strings.Join(noIdleRows, "\n"); strings.Contains(got, "T2 emergency") {
		t.Errorf("只到过 T1 时不该出现 T2 行\n----\n%s----", got)
	}
}

// TestCompressPanel_PercentUsesPeakOriginal 钉死 §3.3：三档串行嵌套，
// 分母只能取单次最大的 original_tokens；用 Σoriginal 会把 60% 算成 33%。
func TestCompressPanel_PercentUsesPeakOriginal(t *testing.T) {
	m := &model{}
	snap := compressFeed(m, "context_compressed", 1000, 800, 200)
	snap = compressFeed(m, "context_emergency_compressed", 800, 400, 400)

	if snap.pct != 0.6 {
		t.Errorf("pct = %v, want 0.6", snap.pct)
	}
	joined := strings.Join(stripANSILines(
		buildCompressBlock(snap, compressPanelSpec{innerWidth: 46})), "\n")
	if !strings.Contains(joined, "(60%)") {
		t.Errorf("标题应显示 (60%%)\n----\n%s----", joined)
	}
	if strings.Contains(joined, "33%") {
		t.Errorf("分母又退回 Σoriginal 的双算口径\n----\n%s----", joined)
	}
}

// TestCompressPanel_PercentCapsAtHundred 盯住多轮任务：Σsaved 会超过任何单次
// original，不封顶就报出"省掉了比上下文还多的 token"。
func TestCompressPanel_PercentCapsAtHundred(t *testing.T) {
	m := &model{}
	for i := 0; i < 5; i++ {
		compressFeed(m, "context_compressed", 1000, 100, 900)
	}
	snap := m.compressSnapshot()
	if snap.saved != 4500 || snap.pct != 1 {
		t.Fatalf("saved=%d pct=%v, want 4500 / 1", snap.saved, snap.pct)
	}
	if got := compressPctLabel(snap.pct); got != "(100%)" {
		t.Errorf("pct 封顶后应显示 (100%%)，实际 %q", got)
	}
}

// TestCompressPanel_EmergencyHasNoSavedPercent 只喂 emergency（事件里没有
// saved_percent 键）也要得到正确的 saved / pct / 徽标。
func TestCompressPanel_EmergencyHasNoSavedPercent(t *testing.T) {
	m := &model{}
	snap := compressFeed(m, "context_emergency_compressed", 5000, 1000, 4000)

	if snap.tiers[tierEmergency].count != 1 || snap.tiers[tierEmergency].saved != 4000 {
		t.Errorf("emergency 档 = %+v, want count 1 / saved 4000", snap.tiers[tierEmergency])
	}
	if snap.pct != 0.8 {
		t.Errorf("pct = %v, want 0.8", snap.pct)
	}
	if snap.maxTier != 2 {
		t.Errorf("maxTier = %d, want 2（徽标 T2）", snap.maxTier)
	}
	// 徽标 T2：NoIdle 档只砍"高于已触发档"的行 —— T1 虽然没触发过（低于已触发档），
	// 仍然留着当暗色对照，这样"到过 T2 却从没到过 T1"这件事本身才是看得见的。
	rows := stripANSILines(renderCompressTier(snap, compressPanelSpec{innerWidth: 46}, compressTierNoIdle))
	joined := strings.Join(rows, "\n")
	if strings.Contains(joined, "T3 ultimate") {
		t.Errorf("只到过 T2 时不该出现 T3 行\n----\n%s----", joined)
	}
	for _, must := range []string{"T1 rule", "T2 emergency"} {
		if !strings.Contains(joined, must) {
			t.Errorf("NoIdle 不该砍掉不高于已触发档的行 %q\n----\n%s----", must, joined)
		}
	}
}

// TestCompressPanel_CutRankingAndOverflow 验证 cut: 榜的两级排序与溢出提示。
func TestCompressPanel_CutRankingAndOverflow(t *testing.T) {
	m := &model{compress: compressStats{
		// 次数与 omitted 交叉：delete_file 的 omitted 最大却只被截过 1 次，
		// 用它能证明"次数"是主键；print_dir_tree 与 run_bash 同次数，用它们
		// 证明 omitted 是次键。
		tiers: [compressTierCount]tierStat{{count: 1, saved: 100, original: 1000}},
		cutByTool: map[string]*cutStat{
			"read_file":       {tool: "read_file", count: 5, omitted: 10},
			"print_dir_tree":  {tool: "print_dir_tree", count: 4, omitted: 5000},
			"run_bash":        {tool: "run_bash", count: 4, omitted: 999},
			"search_by_regex": {tool: "search_by_regex", count: 3, omitted: 1},
			"delete_file":     {tool: "delete_file", count: 1, omitted: 8000},
		},
	}}
	snap := m.compressSnapshot()

	want := []string{"read_file", "print_dir_tree"}
	if len(snap.cuts) != len(want) {
		t.Fatalf("榜长 = %d, want %d：%+v", len(snap.cuts), len(want), snap.cuts)
	}
	for i, w := range want {
		if snap.cuts[i].tool != w {
			t.Errorf("第 %d 项 = %s, want %s（全榜 %+v）", i, snap.cuts[i].tool, w, snap.cuts)
		}
	}
	if snap.cutMore != 3 {
		t.Errorf("cutMore = %d, want 3", snap.cutMore)
	}

	line := stripANSI(compressCutRow(snap, 46))
	if !strings.Contains(line, " ┈ +3") {
		t.Errorf("溢出提示没出现：%q", line)
	}
	if strings.Contains(line, "delete_file") || strings.Contains(line, "run_bash") {
		t.Errorf("落榜的工具不该出现：%q", line)
	}
	// inner < 30 时整行不渲染
	rows := buildCompressBlock(snap, compressPanelSpec{innerWidth: 29})
	if joined := strings.Join(stripANSILines(rows), "\n"); strings.Contains(joined, "cut:") {
		t.Errorf("inner=29 不该画 cut: 行\n----\n%s----", joined)
	}
}

// TestCompressPanel_CutFeedsFromEvent 验证 truncated_tools[] 的两种具体形状都能解析。
//
// 两个发布方给的是 []map[string]interface{}（总线在 TUI 这条路上不做 JSON 往返），
// 只有经 HTTP/WebSocket 或测试构造才会是 []interface{} —— 只认后者的话 cut: 榜
// 与 ctrl+l 明细在真机上永远是空的。
func TestCompressPanel_CutFeedsFromEvent(t *testing.T) {
	entries := []map[string]interface{}{
		{"tool_name": "read_file", "omitted_tokens": 100},
		{"tool_name": "read_file", "omitted_tokens": 50},
		{"tool_name": "list_dir", "omitted_tokens": 30},
		{"tool_name": "", "omitted_tokens": 9},
	}
	// read_file 在一次事件里有两条被截断的消息 → 记 2 次、omitted 累加：
	// truncated_tools[] 是每条被截断的消息一项，不是按工具汇总。
	const readFileCount = 2

	for _, tc := range []struct {
		name     string
		tools    interface{}
		skipsBad bool
	}{
		{"[]map[string]interface{}（发布方的真实形状）", entries, false},
		{"[]interface{}（经 JSON 往返）", []interface{}{entries[0], entries[1], entries[2], entries[3], "not-a-map"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &model{}
			m.recordCompression("context_compressed", map[string]interface{}{
				"original_tokens": 1000,
				"saved_tokens":    200,
				"truncated_count": 3,
				"truncated_tools": tc.tools,
			})
			got := map[string]cutStat{}
			for _, c := range m.compressSnapshot().cuts {
				got[c.tool] = c
			}
			if len(got) != 2 {
				t.Fatalf("榜应只有 2 个工具（空名与坏条目跳过），实际 %+v", got)
			}
			if c := got["read_file"]; c.count != readFileCount || c.omitted != 150 {
				t.Errorf("read_file = %+v, want count %d / omitted 150", c, readFileCount)
			}
			if c := got["list_dir"]; c.count != 1 || c.omitted != 30 {
				t.Errorf("list_dir = %+v, want count 1 / omitted 30", c)
			}
		})
	}
}

// TestCompressPanel_RealPublisherShapeEndToEnd 用发布方构造事件的同一份代码形状走完整条链
// （Update → 统计 → 侧栏渲染 → timeline 明细）。这是唯一能抓住
// "类型断言写错 ⇒ 面板静默少一行数据"的闸门。
func TestCompressPanel_RealPublisherShapeEndToEnd(t *testing.T) {
	testLang(t)
	m := tabTestModel(t, 140, 40)
	m.inputTokens = 120000
	m.outputTokens = 34000
	m.totalInputTokens = 140000
	m.tokenUsagePerAgent = map[string]*AgentTokenUsage{}

	// 与 internal/agents/executor.go:423 的构造方式逐字一致
	type truncation struct {
		tool                    string
		original, kept, omitted int
	}
	entries := []truncation{
		{"read_file", 4000, 200, 3800},
		{"read_file", 3000, 200, 2800},
		{"search_by_regex", 1200, 150, 1050},
	}
	truncatedTools := make([]map[string]interface{}, len(entries))
	for i, e := range entries {
		truncatedTools[i] = map[string]interface{}{
			"tool_name":       e.tool,
			"original_tokens": e.original,
			"kept_tokens":     e.kept,
			"omitted_tokens":  e.omitted,
		}
	}
	m = feedEvent(m, buildEvent("context_compressed", "Director", map[string]interface{}{
		"original_tokens":   9000,
		"compressed_tokens": 6000,
		"saved_tokens":      3000,
		"saved_percent":     33.3,
		"truncated_count":   len(entries),
		"truncated_tools":   truncatedTools,
	}))

	snap := m.compressSnapshot()
	if len(snap.cuts) != 2 {
		t.Fatalf("cut 榜应有 2 个工具，实际 %+v", snap.cuts)
	}
	if snap.cuts[0].tool != "read_file" || snap.cuts[0].count != 2 {
		t.Errorf("榜首 = %+v, want read_file ×2", snap.cuts[0])
	}

	joined := strings.Join(dashboardLines(m, dashboardPanelWidth, 30), "\n")
	if !strings.Contains(joined, "⧉ COMPRESS") {
		t.Errorf("侧栏没有压缩段\n----\n%s----", joined)
	}
	if !strings.Contains(joined, "cut: read_file×2 search_by_regex×1") {
		t.Errorf("cut: 行没解析出真实事件里的工具\n----\n%s----", joined)
	}

	// ctrl+l 的明细同样依赖这个形状：过去它永远只有一行标题、下面空着
	var detail string
	for _, e := range m.timelineEntries {
		if e.Name == "Context Compressed" {
			detail = e.Detail
		}
	}
	if !strings.Contains(detail, "read_file: 4000 → 200 tokens (saved 3800)") {
		t.Errorf("timeline 明细里缺逐工具的行：%q", detail)
	}
}

// TestCompressPanel_SamplesDedupAndCap 验证采样：连续同值只记一次、窗口只留最近 8 个。
func TestCompressPanel_SamplesDedupAndCap(t *testing.T) {
	m := &model{}

	for i := 0; i < 5; i++ {
		m.setContextTokens("Director", 1000)
	}
	if n := len(m.compress.ctxSamples); n != 1 {
		t.Fatalf("连续同值应只记 1 点，实际 %v", m.compress.ctxSamples)
	}

	// 交替取值才能塞满窗口
	for i := 0; i < 12; i++ {
		v := int64(1000 + i*100)
		if i%2 == 0 {
			v = 50000
		}
		m.setContextTokens("Director", v)
	}
	samples := m.compress.ctxSamples
	if n := len(samples); n != maxCtxSamples {
		t.Fatalf("窗口应封顶 %d，实际 %d：%v", maxCtxSamples, n, samples)
	}
	// i=11 是奇数 → v = 1000+1100 = 2100，它必须是窗口里最新的那一点
	if last := samples[len(samples)-1]; last != 2100 {
		t.Errorf("最后一个采样点 = %d, want 2100（丢的应该是最旧）：%v", last, samples)
	}
	if m.compress.peakCtx != 50000 {
		t.Errorf("peakCtx = %d, want 50000（窗口外峰值要留住）", m.compress.peakCtx)
	}
	if got := m.currentRun().ContextTokens; got != 2100 {
		t.Errorf("setContextTokens 必须继续覆写 ContextTokens，实际 %d", got)
	}
	// 采样点在快照里归一化成 0..7，点数<2 时不渲染趋势行
	if levels := m.compressSnapshot().samples; len(levels) != maxCtxSamples {
		t.Errorf("levels = %v, want %d 个", levels, maxCtxSamples)
	}
	single := &model{}
	single.setContextTokens("Director", 700)
	if lv := single.compressSnapshot().samples; lv != nil {
		t.Errorf("单点画不出趋势，want nil，实际 %v", lv)
	}
}

// TestCompressPanel_IdleStateIsTwoRows 钉住空闲态：没压缩过时区块**不消失**，
// 而是退到最低档形态（分隔线 + 标题，徽标 T0）。"这段存在但还没数据"必须可见。
func TestCompressPanel_IdleStateIsTwoRows(t *testing.T) {
	m := &model{}
	m.setContextTokens("Director", 1000)
	m.setContextTokens("Director", 2000)

	snap := m.compressSnapshot()
	if len(snap.samples) == 0 {
		t.Fatal("fixture 应有采样点（否则这条测试没测到东西）")
	}
	if snap.any() {
		t.Error("没有压缩事件时 any() 不该为真")
	}

	rows := stripANSILines(buildCompressBlock(snap, compressPanelSpec{innerWidth: 46}))
	if len(rows) != 2 {
		t.Fatalf("空闲态应恰好 2 行（分隔线+标题），实际 %d 行\n----\n%s----", len(rows), strings.Join(rows, "\n"))
	}
	if rows[0] != strings.Repeat("─", 46) {
		t.Errorf("首行应是分隔线：%q", rows[0])
	}
	title := rows[1]
	if !strings.HasPrefix(title, "⧉ COMPRESS 0 (0%)") || !strings.HasSuffix(title, "T0") {
		t.Errorf("空闲标题 = %q, want 前缀 \"⧉ COMPRESS 0 (0%%)\" 且以 T0 结尾", title)
	}
	for i, r := range rows {
		if w := lipgloss.Width(r); w != 46 {
			t.Errorf("line %d 宽度 = %d, want 46（%q）", i, w, r)
		}
	}
	// 只有采样点不该长出档位行/采样行/榜行
	joined := strings.Join(rows, "\n")
	for _, must := range []string{"T1 rule", "ctx ▁", "cut:"} {
		if strings.Contains(joined, must) {
			t.Errorf("空闲态不该有 %q\n----\n%s----", must, joined)
		}
	}
	// 高度实在放不下 2 行时整段让位，绝不留孤零零的分隔线或标题
	if rows := buildCompressBlock(snap, compressPanelSpec{innerWidth: 46, maxRows: 1}); len(rows) != 0 {
		t.Errorf("maxRows=1 时空闲态也必须整块为空，实际 %d 行", len(rows))
	}
	// 底部摘要行同样常驻（与侧栏同一条口径）
	if got := stripANSI(compressSummaryRow(snap, 46)); got != "  ⧉ 0 (0%) T0" {
		t.Errorf("空闲摘要 = %q, want \"  ⧉ 0 (0%%) T0\"", got)
	}
}

// TestCompressPanel_FeedsFromAllThreeEvents 验证三个事件名各归各档，
// 且徽标是"到过第几档"而不是"最后一次触发第几档"。
func TestCompressPanel_FeedsFromAllThreeEvents(t *testing.T) {
	m := &model{}
	for _, ev := range []struct {
		eventType string
		original  int
		saved     int
	}{
		{"context_compressed", 1000, 100},
		{"context_emergency_compressed", 900, 300},
		{"context_ultimate_compressed", 800, 400},
	} {
		compressFeed(m, ev.eventType, ev.original, ev.original-ev.saved, ev.saved)
	}
	snap := m.compressSnapshot()

	for i := range snap.tiers {
		if snap.tiers[i].count != 1 {
			t.Errorf("档位 %d count = %d, want 1（%+v）", i+1, snap.tiers[i].count, snap.tiers)
		}
	}
	if snap.maxTier != 3 {
		t.Errorf("maxTier = %d, want 3（徽标 T3）", snap.maxTier)
	}
	if snap.saved != 800 || m.compress.maxOriginal != 1000 {
		t.Errorf("saved=%d maxOriginal=%d, want 800 / 1000", snap.saved, m.compress.maxOriginal)
	}
	// 回落到 T1 之后徽标仍是 T3
	compressFeed(m, "context_compressed", 700, 600, 100)
	if got := m.compressSnapshot(); got.maxTier != 3 || got.tiers[tierRule].count != 2 {
		t.Errorf("徽标应记住到过过的最高档，实际 maxTier=%d T1count=%d", got.maxTier, got.tiers[tierRule].count)
	}
}

// TestCompressPanel_UnknownEventTypeIgnored 防止把别的事件误记成压缩。
func TestCompressPanel_UnknownEventTypeIgnored(t *testing.T) {
	m := &model{}
	m.recordCompression("context_loaded", map[string]interface{}{"original_tokens": 9999, "saved_tokens": 9999})
	if m.compressSnapshot().any() {
		t.Error("未知事件类型不该产生压缩统计")
	}
}

// TestCompressPanel_EventChainFeedsStats 端到端：三个压缩事件穿过 Update 之后，
// 既进了统计，也仍然更新 ⛁ 行、仍然各自产出 timeline 条目（共享分支不能吞掉档位分支）。
func TestCompressPanel_EventChainFeedsStats(t *testing.T) {
	m := newTestModel()
	m.tokenUsagePerAgent = map[string]*AgentTokenUsage{}

	feed := func(eventType string, content map[string]interface{}) {
		m = feedEvent(m, buildEvent(eventType, "Director", content))
	}
	feed("context_compressed", map[string]interface{}{
		"original_tokens":   4000,
		"compressed_tokens": 3000,
		"saved_tokens":      1000,
		"truncated_count":   1,
		"truncated_tools": []interface{}{
			map[string]interface{}{"tool_name": "read_file", "omitted_tokens": 1000},
		},
	})
	feed("context_emergency_compressed", map[string]interface{}{
		"original_tokens":   3000,
		"compressed_tokens": 1200,
		"saved_tokens":      1800,
	})
	feed("context_ultimate_compressed", map[string]interface{}{
		"original_tokens":   1200,
		"compressed_tokens": 600,
		"saved_tokens":      600,
		"total_plans":       4,
		"kept_plans":        2,
	})

	snap := m.compressSnapshot()
	if !snap.any() {
		t.Fatal("事件穿过 Update 后应留下压缩统计")
	}
	for i, w := range []int{1, 1, 1} {
		if snap.tiers[i].count != w {
			t.Errorf("档位 %d count = %d, want %d", i+1, snap.tiers[i].count, w)
		}
	}
	if snap.saved != 3400 {
		t.Errorf("Σsaved = %d, want 3400", snap.saved)
	}
	if got := m.currentRun().ContextTokens; got != 600 {
		t.Errorf("⛁ 行应跟到最后一次压缩的 compressed_tokens，实际 %d", got)
	}
	if len(m.compress.ctxSamples) != 3 {
		t.Errorf("采样点 = %d, want 3：%v", len(m.compress.ctxSamples), m.compress.ctxSamples)
	}
	// 每档仍各产一条 timeline 条目
	names := map[string]bool{}
	for _, e := range m.timelineEntries {
		names[e.Name] = true
	}
	for _, w := range []string{"Context Compressed", "Emergency Compression", "Ultimate Compression"} {
		if !names[w] {
			t.Errorf("timeline 缺少 %q 条目（共 %d 条）", w, len(m.timelineEntries))
		}
	}
}

// TestCompressPanel_RestoredFromRollout 验证 /resume 的重建：三档统计与徽标接上，
// 而 cut 榜与采样序列（没落盘）为空但不报错、不留空壳行。
func TestCompressPanel_RestoredFromRollout(t *testing.T) {
	m := newTestModel()
	m.tokenUsagePerAgent = map[string]*AgentTokenUsage{}

	applyRolloutCompressions(m, memory.RolloutInfo{
		Compressions: []memory.CompactionRecord{
			{Kind: "rule", Saved: 3000, TokensBefore: 9000, TokensAfter: 6000},
			{Kind: "emergency", Saved: 2000, TokensBefore: 6000, TokensAfter: 4000},
			{Kind: "nonsense", Saved: 111, TokensBefore: 1, TokensAfter: 1},
		},
	})

	snap := m.compressSnapshot()
	if snap.tiers[tierRule].count != 1 || snap.tiers[tierEmergency].count != 1 {
		t.Errorf("两档应各重建 1 次，实际 %+v", snap.tiers)
	}
	if snap.tiers[tierUltimate].count != 0 {
		t.Errorf("认不出的 kind 不该被猜成某一档：%+v", snap.tiers)
	}
	if snap.saved != 5000 || snap.maxTier != 2 {
		t.Errorf("saved=%d maxTier=%d, want 5000 / 2", snap.saved, snap.maxTier)
	}
	if len(snap.cuts) != 0 || len(snap.samples) != 0 {
		t.Errorf("恢复后 cut/采样应为空，实际 cuts=%+v samples=%v", snap.cuts, snap.samples)
	}
	rows := stripANSILines(buildCompressBlock(snap, compressPanelSpec{innerWidth: 46}))
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "T2") || !strings.Contains(joined, "T1 rule") {
		t.Errorf("恢复后区块必须仍然渲染并带 T2 徽标\n----\n%s----", joined)
	}
	if strings.Contains(joined, "cut:") || strings.Contains(joined, "ctx ▁") {
		t.Errorf("没有数据的两行不该出现\n----\n%s----", joined)
	}
}

// TestCompressPanel_CloneIsolatesTabs 钉住 tab 快照：两个 tab 不共享 cut 表与采样数组。
func TestCompressPanel_CloneIsolatesTabs(t *testing.T) {
	origin := compressStats{
		tiers:      [compressTierCount]tierStat{{count: 1, saved: 10}},
		cutByTool:  map[string]*cutStat{"read_file": {tool: "read_file", count: 1, omitted: 5}},
		ctxSamples: []int64{100, 200},
	}
	copied := origin.clone()
	copied.cuts()["read_file"].count = 99
	copied.ctxSamples[0] = 999

	if origin.cutByTool["read_file"].count != 1 {
		t.Error("clone 后两个 tab 共享了同一张 cut 表")
	}
	if origin.ctxSamples[0] != 100 {
		t.Error("clone 后两个 tab 共享了同一块采样数组")
	}
	if zero := (compressStats{}).clone(); zero.cutByTool != nil || zero.ctxSamples != nil {
		t.Error("零值 clone 不该凭空造出 map/slice")
	}
}

// TestCompressPanel_RoundTripThroughTabs 走一遍真实的 save/restore，
// 确认 compress 进了 sessionTab 的字段清单。
func TestCompressPanel_RoundTripThroughTabs(t *testing.T) {
	m := tabTestModel(t, 120, 40)

	m.recordCompression("context_emergency_compressed", map[string]interface{}{
		"original_tokens": 5000, "compressed_tokens": 1000, "saved_tokens": 4000,
		"truncated_tools": []interface{}{
			map[string]interface{}{"tool_name": "read_file", "omitted_tokens": 4000},
		},
	})
	m.setContextTokens("Director", 1000)

	m.saveActiveSession()
	m.compress = compressStats{}
	m.restoreSessionTab(m.activeSessionIdx)

	snap := m.compressSnapshot()
	if snap.tiers[tierEmergency].count != 1 || snap.saved != 4000 {
		t.Errorf("restore 后压缩统计丢失：%+v", snap.tiers)
	}
	if len(snap.cuts) != 1 || snap.cuts[0].tool != "read_file" {
		t.Errorf("restore 后 cut 榜丢失：%+v", snap.cuts)
	}

	// 清空会话必须把它一起清掉
	m.clearCurrentSession()
	if m.compressSnapshot().any() {
		t.Error("clearCurrentSession 应清零压缩统计")
	}
}

// TestCompressPanel_NarrowWidthDropsBar 验证宽度阶梯：bar 不足 8 格时去条形。
func TestCompressPanel_NarrowWidthDropsBar(t *testing.T) {
	snap := compressGoldenModel().compressSnapshot()
	rows := stripANSILines(buildCompressBlock(snap, compressPanelSpec{innerWidth: 30}))
	joined := strings.Join(rows, "\n")
	if strings.Contains(joined, "▰") || strings.Contains(joined, "▱") {
		t.Errorf("inner=30 不该有进度条\n----\n%s----", joined)
	}
	if !strings.Contains(joined, "  T1 rule  −180.2k ×2") {
		t.Errorf("紧凑档位行格式不符\n----\n%s----", joined)
	}
	for i, r := range rows {
		if w := lipgloss.Width(r); w > 30 {
			t.Errorf("inner=30 line %d 宽度 %d 超界：%q", i, w, r)
		}
	}
}

// TestCompressPanel_SummaryWidthLadder 钉住 §4.4 的底部摘要行宽度阶梯。
func TestCompressPanel_SummaryWidthLadder(t *testing.T) {
	snap := compressGoldenModel().compressSnapshot()

	if got := stripANSI(compressSummaryRow(snap, 60)); got != "  ⧉ −1.42m (38%) T2  ·  cut read_file×7" {
		t.Errorf("宽档 = %q", got)
	}
	if got := stripANSI(compressSummaryRow(snap, 35)); got != "  ⧉ −1.42m (38%) T2" {
		t.Errorf("中档应去 cut，实际 %q", got)
	}
	if got := stripANSI(compressSummaryRow(snap, 20)); got != "  ⧉ −1.42m T2" {
		t.Errorf("窄档应去 pct，实际 %q", got)
	}
	if got := compressSummaryRow(snap, 0); got != "" {
		t.Errorf("inner=0 不该渲染，实际 %q", got)
	}
	// 没有截断记录时 cut 段整体省略（不是留一个 "· cut"）
	noCuts := compressGoldenModel()
	noCuts.compress.cutByTool = nil
	if got := stripANSI(compressSummaryRow(noCuts.compressSnapshot(), 60)); strings.Contains(got, "cut") {
		t.Errorf("无截断记录时不该有 cut 段：%q", got)
	}
}

// TestCompressPanel_CollapsedFooterHasNoSummary 折叠态（alt+t）不加行。
func TestCompressPanel_CollapsedFooterHasNoSummary(t *testing.T) {
	testLang(t)
	m := s1Model()
	m.compress = compressStats{
		tiers: [compressTierCount]tierStat{{count: 1, saved: 180200, original: 1000000}},
		cutByTool: map[string]*cutStat{
			"read_file": {tool: "read_file", count: 1, omitted: 100},
		},
	}

	m.termWidth = 100
	m.tokenDashboardCollapsed = false
	if got := stripANSI(m.renderFooterTokenPanel()); !strings.Contains(got, "⧉") {
		t.Errorf("展开态底部面板应有压缩摘要\n----\n%s----", got)
	}

	m.tokenDashboardCollapsed = true
	if got := stripANSI(m.renderFooterTokenPanel()); strings.Contains(got, "⧉") {
		t.Errorf("折叠态不该有压缩摘要\n----\n%s----", got)
	}
}

// TestCompressPanel_BlockWidthsAcrossInner 扫宽度矩阵：任何一档都不许超出 inner，
// 标题行恒在。
func TestCompressPanel_BlockWidthsAcrossInner(t *testing.T) {
	snap := compressGoldenModel().compressSnapshot()
	for _, inner := range []int{30, 40, 46, 60, 80, 100, 120, 160, 200, 240} {
		for maxRows := 0; maxRows <= 8; maxRows++ {
			rows := buildCompressBlock(snap, compressPanelSpec{innerWidth: inner, maxRows: maxRows})
			if len(rows) == 0 {
				continue
			}
			for i, r := range rows {
				if w := lipgloss.Width(r); w > inner {
					t.Errorf("inner=%d maxRows=%d line %d 宽度 %d 超界：%q",
						inner, maxRows, i, w, stripANSI(r))
				}
			}
			if !strings.Contains(strings.Join(stripANSILines(rows), "\n"), "COMPRESS") {
				t.Errorf("inner=%d maxRows=%d 标题行丢了", inner, maxRows)
			}
		}
	}
}
