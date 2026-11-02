package tui

// tui_startup_screen_test.go — 空白启动屏（品牌区 + 左右双卡片）的渲染闸门。
//
// 两条底线：① 卡片几何必须用**显示宽度**（中文是双宽，len() 会算成一半）；
// ② 任何档位下每行宽 <= 画布宽、总行数 <= 画布高 —— lipgloss.Place 在内容超出时是
// noop，不会替我们夹，一旦溢出就是整屏折行 / footer 被顶出可视区。

import (
	"strings"
	"testing"
	"time"

	"yagent/internal/datamanager"
	"yagent/internal/tui/common"

	"charm.land/lipgloss/v2"
)

// startupFixtureModel 造一个能渲染启动屏的 model：语言固定中文（断言的文案是中文），
// projectDir 固定（cwd 行可预测）。
func startupFixtureModel(t *testing.T, w, h int) *model {
	t.Helper()
	InitLangManager()
	m := newTestModel()
	m.termWidth, m.termHeight = w, h
	m.currentLang = LangChinese
	m.projectDir = "/tmp/yagent-startup-fixture"
	return m
}

// startupFixtureRecents 造 4 条最近会话：时间戳覆盖"小时 / 昨天 / 天"三档，
// 让相对时间的截断/宽度分配真的被走到。
func startupFixtureRecents(now time.Time) []datamanager.TaskHistoryItem {
	return []datamanager.TaskHistoryItem{
		{TaskID: "t1", Title: "修复启动页布局", CreatedAt: now.Add(-3 * time.Hour), UpdatedAt: now.Add(-2 * time.Hour), MessageCount: 18},
		{TaskID: "t2", Title: "蓝图导出层设计", CreatedAt: now.Add(-30 * time.Hour), UpdatedAt: now.Add(-26 * time.Hour), MessageCount: 42},
		{TaskID: "t3", Title: "MCP 工具注册排查", CreatedAt: now.Add(-80 * time.Hour), UpdatedAt: now.Add(-72 * time.Hour), MessageCount: 7},
		{TaskID: "t4", Title: "blackboard 索引优化", CreatedAt: now.Add(-130 * time.Hour), UpdatedAt: now.Add(-120 * time.Hour), MessageCount: 26},
	}
}

// renderStartupPanel 走真实入口（renderWelcomePanel），并给出断言的画布尺寸。
func renderStartupPanel(t *testing.T, m *model) (out string, canvasW, canvasH int) {
	t.Helper()
	out = m.renderWelcomePanel()
	w, h := m.startupScreenSize()
	return out, w, h
}

// TestStartupScreen_SideBySideCards 宽屏（120x40）必须是并排双卡：两个卡片标题落在同一行，
// 并且两卡的 hint 都在（左卡教怎么用，右卡指路 /resume）。
func TestStartupScreen_SideBySideCards(t *testing.T) {
	m := startupFixtureModel(t, 120, 40)
	m.startupRecents = startupFixtureRecents(time.Now())

	plain := stripANSI(m.renderWelcomePanel())
	for _, want := range []string{
		"⚡ 开始", "◷ 最近会话",
		"直接输入任务开始对话 ↓", "/resume 选择并恢复会话",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("启动屏缺少 %q\n----\n%s", want, plain)
		}
	}

	sameLine := false
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "⚡ 开始") && strings.Contains(line, "◷ 最近会话") {
			sameLine = true
		}
	}
	if !sameLine {
		t.Errorf("120x40 应并排两卡（两个标题同一行）\n----\n%s", plain)
	}
}

// TestStartupScreen_EmptyRecentsShowsEmptyState 没有历史会话时右卡换成两行引导文案。
func TestStartupScreen_EmptyRecentsShowsEmptyState(t *testing.T) {
	m := startupFixtureModel(t, 120, 40)

	plain := stripANSI(m.renderWelcomePanel())
	for _, want := range []string{"还没有历史会话", "输入你的第一个任务，开始对话 ↓"} {
		if !strings.Contains(plain, want) {
			t.Errorf("空态缺少 %q\n----\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "修复启动页布局") {
		t.Error("空态不该出现任何会话标题")
	}
}

// TestStartupScreen_RecentsRowsShowItems 4 条会话的标题与轮数都要落到屏上
// （标题被截断时用省略号收尾，但短标题必须完整）。
func TestStartupScreen_RecentsRowsShowItems(t *testing.T) {
	m := startupFixtureModel(t, 120, 40)
	items := startupFixtureRecents(time.Now())
	m.startupRecents = items

	plain := stripANSI(m.renderWelcomePanel())
	for i, item := range items {
		if !strings.Contains(plain, item.Title) {
			t.Errorf("第 %d 条会话 %q 没出现在启动屏\n----\n%s", i+1, item.Title, plain)
		}
	}
	if !strings.Contains(plain, "18轮") {
		t.Errorf("会话轮数没显示（期望 18轮）\n----\n%s", plain)
	}
}

// TestStartupScreen_NeverOverflowsCanvas 是防溢出回归的核心：五档尺寸，空态与有数据各一遍，
// 逐行断言显示宽 <= 画布宽，并断言总行数 <= 画布高。
func TestStartupScreen_NeverOverflowsCanvas(t *testing.T) {
	sizes := []struct {
		name string
		w, h int
	}{
		{"wide", 120, 40},
		{"medium", 80, 24},
		{"narrow", 70, 20},
		{"short", 100, 12},
		{"tiny", 60, 8},
	}
	for _, sz := range sizes {
		for _, withRecents := range []bool{false, true} {
			m := startupFixtureModel(t, sz.w, sz.h)
			if withRecents {
				m.startupRecents = startupFixtureRecents(time.Now())
			}
			out, canvasW, canvasH := renderStartupPanel(t, m)
			lines := strings.Split(out, "\n")
			label := sz.name + "/empty"
			if withRecents {
				label = sz.name + "/recents"
			}
			if len(lines) > canvasH {
				t.Errorf("%s %dx%d: 启动屏 %d 行，超出画布 %d 行", label, sz.w, sz.h, len(lines), canvasH)
			}
			for i, line := range lines {
				if got := lipgloss.Width(line); got > canvasW {
					t.Errorf("%s %dx%d: 第 %d 行宽 %d 超过画布 %d\n%q",
						label, sz.w, sz.h, i, got, canvasW, stripANSI(line))
					break
				}
			}
		}
	}
}

// TestStartupScreen_NarrowStacksCards 窄终端（70 宽）必须上下堆叠：两个标题出现在不同行。
func TestStartupScreen_NarrowStacksCards(t *testing.T) {
	m := startupFixtureModel(t, 70, 24)
	m.startupRecents = startupFixtureRecents(time.Now())

	plain := stripANSI(m.renderWelcomePanel())
	startLine, recentLine := -1, -1
	for i, line := range strings.Split(plain, "\n") {
		if startLine < 0 && strings.Contains(line, "⚡ 开始") {
			startLine = i
		}
		if recentLine < 0 && strings.Contains(line, "◷ 最近会话") {
			recentLine = i
		}
	}
	if startLine < 0 || recentLine < 0 {
		t.Fatalf("堆叠档缺卡片标题（start=%d recent=%d）\n----\n%s", startLine, recentLine, plain)
	}
	if startLine == recentLine {
		t.Errorf("70 宽应上下堆叠，两个标题却落在同一行 %d\n----\n%s", startLine, plain)
	}
	if recentLine < startLine {
		t.Errorf("堆叠顺序反了：最近会话在第 %d 行、开始卡在第 %d 行", recentLine, startLine)
	}
}

// TestStartupScreen_CacheKeyTracksRecents 只改 startupRecents（不动宽度/高度/cwd）时，
// 启动屏也必须重画 —— 这就是 memo key 里那个 recentsFP 的作用。
func TestStartupScreen_CacheKeyTracksRecents(t *testing.T) {
	m := startupFixtureModel(t, 120, 40)
	before := m.renderWelcomePanel()
	if !strings.Contains(stripANSI(before), "还没有历史会话") {
		t.Fatalf("夹具前提不成立：初始应是空态\n----\n%s", stripANSI(before))
	}

	m.startupRecents = startupFixtureRecents(time.Now())
	after := m.renderWelcomePanel()
	if before == after {
		t.Error("startupRecents 变了但启动屏命中了旧缓存（memo key 缺指纹）")
	}
	if !strings.Contains(stripANSI(after), "修复启动页布局") {
		t.Errorf("重画后仍没有第 1 条会话\n----\n%s", stripANSI(after))
	}
}

// TestUpdateLandsStartupRecentsMsg 主 Update 必须有这条 case：漏了会被 default 静默丢弃，
// 右卡永远停在空态（且没有任何编译错误）。
func TestUpdateLandsStartupRecentsMsg(t *testing.T) {
	m := startupFixtureModel(t, 120, 40)
	_ = m.renderWelcomePanel() // 先让空态落进 memo
	items := startupFixtureRecents(time.Now())

	_, _ = m.Update(startupRecentsMsg{items: items})
	if len(m.startupRecents) != len(items) {
		t.Fatalf("startupRecents 没落地：%d 条，期望 %d 条（消息被丢弃了）", len(m.startupRecents), len(items))
	}
	if strings.Contains(stripANSI(m.welcomeCache), "还没有历史会话") {
		t.Error("落地后启动屏仍是空态：没有触发重绘")
	}
	if !strings.Contains(stripANSI(m.welcomeCache), items[0].Title) {
		t.Errorf("落地后启动屏没有第 1 条会话\n----\n%s", stripANSI(m.welcomeCache))
	}
}

// TestRenderTitledCardWidthIsExact 卡片的每一行（含标题行、hint 行、空内容行、中文超宽内容）
// 都必须恰好等于 width：多一列会把边框挤到下一行，少一列会让两卡拼不齐。
func TestRenderTitledCardWidthIsExact(t *testing.T) {
	c := common.DarkModeColors()
	titles := []string{"⚡ 开始", "◷ Recent sessions", "很长很长很长很长很长很长很长的标题", ""}
	bodies := [][]string{
		{"Alt+S   发送消息", "Ctrl+E  命令模式"},
		{"中文中文中文中文中文中文中文中文中文中文中文中文中文中文中文中文中文中文"},
		{},
	}
	for _, width := range []int{20, 33, 40, 57} {
		for _, title := range titles {
			for _, body := range bodies {
				card := renderTitledCard(title, "直接输入任务开始对话 ↓", body, width, 8, c)
				lines := strings.Split(card, "\n")
				if len(lines) != 8 {
					t.Fatalf("w=%d title=%q: %d 行，期望 8 行", width, title, len(lines))
				}
				for i, line := range lines {
					if got := lipgloss.Width(line); got != width {
						t.Errorf("w=%d title=%q 第 %d 行宽 %d，期望 %d：%q",
							width, title, i, got, width, stripANSI(line))
					}
				}
			}
		}
	}
}

// TestFitStartupCardNeverExceedsBudget 收缩器的不变量：任何预算下 height <= budget，
// 且 height == 2 + items + blank + hint。放不下最小形态时返回零值（调用方省略整张卡）。
func TestFitStartupCardNeverExceedsBudget(t *testing.T) {
	for items := 1; items <= 4; items++ {
		for budget := 0; budget <= 12; budget++ {
			plan := fitStartupCard(items, budget)
			if plan.height == 0 {
				if budget >= items+2 {
					t.Errorf("items=%d budget=%d: 预算够（>=%d）却返回零值", items, budget, items+2)
				}
				continue
			}
			if plan.height > budget {
				t.Errorf("items=%d budget=%d: 计划高 %d 超出预算", items, budget, plan.height)
			}
			want := 2 + plan.items
			if plan.blank {
				want++
			}
			if plan.hint {
				want++
			}
			if plan.height != want {
				t.Errorf("items=%d budget=%d: height=%d 与 2+items+blank+hint=%d 不一致",
					items, budget, plan.height, want)
			}
			if plan.items < 1 || plan.items > items {
				t.Errorf("items=%d budget=%d: 条目数 %d 越界", items, budget, plan.items)
			}
		}
	}
}

// TestStartupScreen_PlanItemsReachTheCard 收缩后的条目数必须真的画出来：renderTitledCard 见到
// 非空 hint 就占一行，调用方若不管 plan.hint，最后一条会被 bodyRows 静默挤掉（不报错、不少行）。
// 70x24 的预算刚好落在"两卡紧贴、都不画 hint"那一档，正是会踩到这条的尺寸。
func TestStartupScreen_PlanItemsReachTheCard(t *testing.T) {
	m := startupFixtureModel(t, 70, 24)
	m.startupRecents = startupFixtureRecents(time.Now())

	plain := stripANSI(m.renderWelcomePanel())
	for _, want := range []string{"Alt+S", "Ctrl+E", "?"} {
		if !strings.Contains(plain, want) {
			t.Errorf("左卡少了 %q（条目被 hint 行挤掉了）\n----\n%s", want, plain)
		}
	}
	if !strings.Contains(plain, "修复启动页布局") || !strings.Contains(plain, "蓝图导出层设计") {
		t.Errorf("右卡少了条目\n----\n%s", plain)
	}
}

// TestStartupScreen_EnglishLabels 英文界面只换文案、不换几何：标题与 hint 都换成英文，
// 卡片宽度仍然精确。
func TestStartupScreen_EnglishLabels(t *testing.T) {
	m := startupFixtureModel(t, 120, 40)
	m.currentLang = LangEnglish
	m.startupRecents = startupFixtureRecents(time.Now())

	out := m.renderWelcomePanel()
	plain := stripANSI(out)
	for _, want := range []string{"⚡ Start", "◷ Recent sessions", "/resume to pick a session"} {
		if !strings.Contains(plain, want) {
			t.Errorf("英文启动屏缺少 %q\n----\n%s", want, plain)
		}
	}
	w, h := m.startupScreenSize()
	lines := strings.Split(out, "\n")
	if len(lines) > h {
		t.Errorf("英文启动屏 %d 行，超出画布 %d 行", len(lines), h)
	}
	for i, line := range lines {
		if got := lipgloss.Width(line); got > w {
			t.Errorf("英文启动屏第 %d 行宽 %d 超过画布 %d", i, got, w)
			break
		}
	}
}

// TestView_EmptySessionFitsExactlyOneScreen 空会话的整屏也必须正好一屏，且空态不该出现
// 滚动条：启动屏比 viewport 高一行的话，computeBodyLayout 会判"溢出"并让出滚动条列，
// 内容宽从 termWidth 变成 termWidth-1，于是满宽的启动屏每一行都被 viewport 折一次。
// 这条钉住 startupScreenSize 与 computeBodyLayout 同源（减掉 tabBarHeight）。
func TestView_EmptySessionFitsExactlyOneScreen(t *testing.T) {
	sizes := []struct{ w, h int }{{120, 40}, {80, 24}, {70, 20}}
	for _, sz := range sizes {
		m := chromeTestModel(t, sz.w, sz.h)
		m.currentLang = LangChinese
		m.startupRecents = startupFixtureRecents(time.Now())

		lines := strings.Split(m.View().Content, "\n")
		if len(lines) != sz.h {
			t.Errorf("%dx%d: 空会话整屏 %d 行，期望 %d 行\n----\n%s",
				sz.w, sz.h, len(lines), sz.h, stripANSI(strings.Join(lines, "\n")))
		}
		for i, line := range lines {
			if got := lipgloss.Width(line); got > sz.w {
				t.Errorf("%dx%d: 第 %d 行宽 %d 超过终端宽\n%q", sz.w, sz.h, i, got, stripANSI(line))
				break
			}
		}
		if got := m.viewport.Width(); got != sz.w {
			t.Errorf("%dx%d: 空态正文列宽 %d，期望 %d（启动屏比 viewport 高，导致误判出滚动条列）",
				sz.w, sz.h, got, sz.w)
		}
	}
}

// TestStartupRelTimeBoundaries 覆盖分钟 / 小时 / 昨天 / 天 / 周的边界。
// 用 startupRelTimeAt 注入"现在"，否则跨零点跑时"昨天"与"3h"会互相串位。
func TestStartupRelTimeBoundaries(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.Local)
	cases := []struct {
		name string
		at   time.Time
		want string
	}{
		{"零值", time.Time{}, "—"},
		{"30 秒前", now.Add(-30 * time.Second), "刚刚"},
		{"12 分钟前", now.Add(-12 * time.Minute), "12m"},
		{"59 分钟前", now.Add(-59 * time.Minute), "59m"},
		{"3 小时前", now.Add(-3 * time.Hour), "3h"},
		{"11 小时前（同一天）", now.Add(-11 * time.Hour), "11h"},
		{"昨天", time.Date(2026, 11, 1, 20, 0, 0, 0, time.Local), "昨天"},
		{"3 天前", time.Date(2026, 10, 30, 9, 0, 0, 0, time.Local), "3d"},
		{"13 天前", now.AddDate(0, 0, -13), "13d"},
		{"14 天前", now.AddDate(0, 0, -14), "2w"},
		{"30 天前", now.AddDate(0, 0, -30), "4w"},
		{"未来（时钟回拨）", now.Add(2 * time.Hour), "刚刚"},
	}
	for _, c := range cases {
		if got := startupRelTimeAt(c.at, now, LangChinese); got != c.want {
			t.Errorf("%s: 得到 %q，期望 %q", c.name, got, c.want)
		}
	}
	// 英文界面只换词，不换分档：分钟/小时/天/周仍是同一套数字。
	if got := startupRelTimeAt(time.Date(2026, 11, 1, 20, 0, 0, 0, time.Local), now, LangEnglish); got != "yesterday" {
		t.Errorf("英文 yesterday: 得到 %q", got)
	}
	if got := startupRelTimeAt(now.Add(-12*time.Minute), now, LangEnglish); got != "12m" {
		t.Errorf("英文 12m: 得到 %q", got)
	}
}
