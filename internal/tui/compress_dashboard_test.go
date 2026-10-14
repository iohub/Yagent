package tui

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
)

// compressDashboardModel 是侧栏 dashboard 的 fixture：有累计用量、有时间线、
// 三档压缩都留下过记录。
func compressDashboardModel() *model {
	m := s1Model()
	m.termWidth = 140
	m.termHeight = 40
	m.taskRunning = true
	m.dashboardCollapsed = false
	m.timelineEntries = []*TimelineEntry{
		{ID: "t1", Kind: TimelineKindTool, Name: "read_file", Status: ToolStatusSuccess},
		{ID: "t2", Kind: TimelineKindLLMCall, Name: "llm_call", Status: ToolStatusSuccess},
	}
	m.compress = compressStats{
		tiers: [compressTierCount]tierStat{
			{count: 2, saved: 180200, original: 900000},
			{count: 1, saved: 1240000, original: 3750000},
		},
		cutByTool: map[string]*cutStat{
			"read_file":       {tool: "read_file", count: 7, omitted: 120000},
			"search_by_regex": {tool: "search_by_regex", count: 2, omitted: 30000},
		},
		ctxSamples:  []int64{100000, 114286, 142857, 185714, 200000},
		peakCtx:     200000,
		maxOriginal: 3750000,
	}
	return m
}

func dashboardLines(m *model, width, height int) []string {
	return strings.Split(stripANSI(m.renderDashboard(width, height)), "\n")
}

// TestDashboardCompressBlock_BelowActivity 钉住位置：⧉ 段在 ⚙ Activity 之下、
// 面板底边框之前 —— 这是"压缩是事后复盘"的排版表达，不是可选项。
func TestDashboardCompressBlock_BelowActivity(t *testing.T) {
	initLangManager()
	m := compressDashboardModel()

	lines := dashboardLines(m, dashboardPanelWidth, 30)
	if len(lines) != 30 {
		t.Fatalf("面板应有 30 行，实际 %d", len(lines))
	}
	activity, comp := -1, -1
	for i, l := range lines {
		if strings.Contains(l, "⚙ Activity") {
			activity = i
		}
		if strings.Contains(l, "⧉ COMPRESS") {
			comp = i
		}
	}
	if activity < 0 {
		t.Fatalf("Activity 段不见了\n----\n%s----", strings.Join(lines, "\n"))
	}
	if comp < 0 {
		t.Fatalf("⧉ 压缩段不见了\n----\n%s----", strings.Join(lines, "\n"))
	}
	if comp < activity {
		t.Errorf("⧉ 段(row %d)必须在 ⚙ Activity(row %d) 之下", comp, activity)
	}
	if comp >= len(lines)-1 {
		t.Errorf("⧉ 段必须是最后一段（row %d / 共 %d 行）", comp, len(lines))
	}
	// token 段在 Activity 之前
	for i, l := range lines[:activity] {
		if strings.Contains(l, "◆ Tokens") {
			if i >= activity {
				t.Errorf("◆ Tokens 必须在 Activity 之前")
			}
			break
		}
	}
}

// TestDashboardCompressBlock_ExactBorderGeometry 带压缩数据时边框几何不许破
// （§10.2 对 TestDashboardBorder_* 的要求）。
func TestDashboardCompressBlock_ExactBorderGeometry(t *testing.T) {
	initLangManager()
	for height := 7; height <= 34; height++ {
		m := compressDashboardModel()
		lines := dashboardLines(m, dashboardPanelWidth, height)
		if len(lines) != height {
			t.Fatalf("height=%d 实际 %d 行", height, len(lines))
		}
		for i, l := range lines {
			if w := lipgloss.Width(l); w != dashboardPanelWidth {
				t.Errorf("height=%d line %d 宽度 = %d, want %d：%q", height, i, w, dashboardPanelWidth, l)
			}
		}
		if !strings.HasPrefix(lines[0], "╭") || !strings.HasPrefix(lines[height-1], "╰") {
			t.Errorf("height=%d 边框破了：%q / %q", height, lines[0], lines[height-1])
		}
	}
}

// TestDashboard_ActivityNeverStarvedByCompress 钉住行预算优先级
// token > Activity > COMPRESS：只要有 timeline 条目，⚙ Activity 标题就必须在。
// 空闲态也扫一遍 —— 区块现在常驻，它更没资格把 Activity 挤掉。
//
// 扫描下界是 8 而不是更小：面板内容容量是 height-2，token 段自己保底 3 行，
// Activity 最少要 3 行（分隔线+标题+提示），所以 height<=7 时物理上放不下，
// 这时面板让位给 token 段（宁可少一段，也不像以前那样多排 2 行再被截掉）。
func TestDashboard_ActivityNeverStarvedByCompress(t *testing.T) {
	initLangManager()
	for height := 8; height <= 24; height++ {
		for _, tc := range []struct {
			name  string
			tweak func(*model)
		}{
			{"有压缩记录", func(m *model) {}},
			{"空闲态", func(m *model) { m.compress = compressStats{} }},
		} {
			m := compressDashboardModel()
			tc.tweak(m)
			joined := strings.Join(dashboardLines(m, dashboardPanelWidth, height), "\n")
			label := fmt.Sprintf("height=%d %s", height, tc.name)
			if !strings.Contains(joined, "⚙ Activity") {
				t.Errorf("%s 时 Activity 标题被 ⧉ 段挤掉了\n----\n%s----", label, joined)
			}
			if !strings.Contains(joined, "◆ Tokens") {
				t.Errorf("%s 时 token 段不见了\n----\n%s----", label, joined)
			}
			// 空壳禁令：⧉ 标题要么带着自己的分隔线（最低档形态），
			// 要么后面真的跟着内容行 —— 孤零零一行标题不算渲染成功。
			lines := dashboardLines(m, dashboardPanelWidth, height)
			if row := indexOfLine(lines, "⧉ COMPRESS"); row >= 0 {
				hasSepBefore := row >= 1 && isPureSeparator(panelContent(lines[row-1]))
				hasContentAfter := row+1 <= len(lines)-2 && panelContent(lines[row+1]) != ""
				if !hasSepBefore && !hasContentAfter {
					t.Errorf("%s 时 ⧉ 段成了孤零零一行标题（row=%d）\n----\n%s----", label, row, joined)
				}
			}
		}
	}
}

// TestDashboard_IdleBlockCostsExactlyTwoRows 空闲态的代价必须可核算：
// 只多"分隔线 + 标题"两行，其余段落的位置与内容都不受影响，且绝不留下空壳。
func TestDashboard_IdleBlockCostsExactlyTwoRows(t *testing.T) {
	initLangManager()

	idle := compressDashboardModel()
	idle.compress = compressStats{}
	lines := dashboardLines(idle, dashboardPanelWidth, 30)
	joined := strings.Join(lines, "\n")

	if !strings.Contains(joined, "⧉ COMPRESS 0 (0%)") {
		t.Errorf("空闲态侧栏也要有 ⧉ 段\n----\n%s----", joined)
	}
	if strings.Contains(joined, "T1 rule") || strings.Contains(joined, "cut:") || strings.Contains(joined, "ctx ▁") {
		t.Errorf("空闲态不该有档位/榜/采样行\n----\n%s----", joined)
	}

	// 空闲段恰好 2 行：分隔线 + 标题，其后只剩边框补白
	titleRow := indexOfLine(lines, "⧉ COMPRESS")
	if titleRow < 1 {
		t.Fatalf("找不到 ⧉ 段标题\n----\n%s----", joined)
	}
	if sep := panelContent(lines[titleRow-1]); !isPureSeparator(sep) {
		t.Errorf("⧉ 段标题的上一行必须是分隔线，实际 %q", sep)
	}
	if !strings.Contains(lines[titleRow], "T0") {
		t.Errorf("空闲段标题应带 T0 徽标：%q", panelContent(lines[titleRow]))
	}
	for i := titleRow + 1; i < len(lines)-1; i++ {
		if c := panelContent(lines[i]); c != "" {
			t.Errorf("空闲段之后不该再有内容行（第 %d 行 %q）—— 段必须只占 2 行", i, c)
		}
	}

	// token 段与 Activity 段一个字都不该因为常驻而变样
	withData := dashboardLines(compressDashboardModel(), dashboardPanelWidth, 30)
	idleHead := strings.Join(lines[:indexOfLine(lines, "⚙ Activity")], "\n")
	dataHead := strings.Join(withData[:indexOfLine(withData, "⚙ Activity")], "\n")
	if idleHead != dataHead {
		t.Errorf("token 段被空闲态区块改写了\nidle:\n%s\nwithData:\n%s", idleHead, dataHead)
	}
}

// panelContent 去掉一行面板的左右边框与空白；补白行返回空串。
func panelContent(line string) string {
	return strings.Trim(line, "│ ")
}

func isPureSeparator(s string) bool {
	return s != "" && strings.Trim(s, "─") == ""
}

func indexOfLine(lines []string, needle string) int {
	for i, l := range lines {
		if strings.Contains(l, needle) {
			return i
		}
	}
	return -1
}

// TestDashboardCacheKey_IncludesCompressStats 是"事件到了面板确实刷新"的证据：
// 压缩事件既不动 timeline 也不动任何累计计数器，只有压缩签名能打破缓存。
func TestDashboardCacheKey_IncludesCompressStats(t *testing.T) {
	initLangManager()
	m := s1Model()
	m.termWidth = 140
	m.taskRunning = true
	m.dashboardCollapsed = false
	m.timelineEntries = []*TimelineEntry{
		{ID: "t1", Kind: TimelineKindTool, Name: "read_file", Status: ToolStatusSuccess},
	}

	first := m.renderDashboard(dashboardPanelWidth, 24)
	// 第二次渲染必须命中缓存（同一份数据）
	m.renderDashboard(dashboardPanelWidth, 24)
	if m.dashboardCacheKey == "" {
		t.Fatal("renderDashboard 没写缓存键")
	}
	keyBefore := m.dashboardCacheKey

	m.recordCompression("context_emergency_compressed", map[string]interface{}{
		"original_tokens": 5000, "compressed_tokens": 1000, "saved_tokens": 4000,
	})
	if m.dashboardCacheKey == keyBefore {
		t.Error("压缩事件之后缓存键必须改变（invalidateFooterCache 没生效？）")
	}
	second := m.renderDashboard(dashboardPanelWidth, 24)
	if first == second {
		t.Errorf("压缩事件后面板内容必须变化\n----\n%s----", stripANSI(second))
	}
	if !strings.Contains(stripANSI(second), "⧉ COMPRESS") {
		t.Errorf("压缩段没渲染出来\n----\n%s----", stripANSI(second))
	}
}

// TestDashboard_CompressSignatureStable 防回归：签名稳定排序，map 迭代顺序不会
// 让缓存键每次都变（否则面板每帧都重绘）。
func TestDashboard_CompressSignatureStable(t *testing.T) {
	m := compressDashboardModel()
	a := m.compressSnapshot().signature()
	b := m.compressSnapshot().signature()
	if a != b {
		t.Errorf("同一份数据的签名不稳定：%q vs %q", a, b)
	}
	if !strings.Contains(a, "read_file:7") {
		t.Errorf("签名应包含 cut 榜：%q", a)
	}
}
