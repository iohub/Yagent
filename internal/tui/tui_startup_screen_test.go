package tui

// tui_startup_screen_test.go — 空白启动屏（品牌区 + 左右双卡片）的渲染闸门。
//
// 两条底线：① 卡片几何必须用**显示宽度**（中文是双宽，len() 会算成一半）；
// ② 任何档位下每行宽 <= 画布宽、总行数 <= 画布高 —— lipgloss.Place 在内容超出时是
// noop，不会替我们夹，一旦溢出就是整屏折行 / footer 被顶出可视区。

import (
	"errors"
	"strings"
	"testing"
	"time"

	"yagent/internal/datamanager"
	"yagent/internal/memory"
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
//
// 造的是统一投影 startupRecentItem（不是某个账本的原生结构）：右卡渲染层只认它，
// 账本差异只体现在 startupRecentsSource（决定 hint 文案）。
func startupFixtureRecents(now time.Time) []startupRecentItem {
	return []startupRecentItem{
		{Label: "修复启动页布局", When: now.Add(-2 * time.Hour), Turns: 18},
		{Label: "蓝图导出层设计", When: now.Add(-26 * time.Hour), Turns: 42},
		{Label: "MCP 工具注册排查", When: now.Add(-72 * time.Hour), Turns: 7},
		{Label: "blackboard 索引优化", When: now.Add(-120 * time.Hour), Turns: 26},
	}
}

// setStartupRecents 是测试侧的落地助手：条目与来源必须一起设。
// 只设条目会让 hint 读到上一个夹具残留的 source —— 那正是本文件要防的那类错配。
func (m *model) setStartupRecents(source startupRecentsSource, items []startupRecentItem) {
	m.startupRecents, m.startupRecentsSource = items, source
}

// startupFixtureWithRecents 装一个已带 4 条 rollout 来源会话的启动屏 model。
func startupFixtureWithRecents(t *testing.T, w, h int) *model {
	t.Helper()
	m := startupFixtureModel(t, w, h)
	m.setStartupRecents(startupRecentsFromRollout, startupFixtureRecents(time.Now()))
	return m
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
	m.setStartupRecents(startupRecentsFromRollout, startupFixtureRecents(time.Now()))

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
	m.setStartupRecents(startupRecentsFromRollout, items)

	plain := stripANSI(m.renderWelcomePanel())
	for i, item := range items {
		if !strings.Contains(plain, item.Label) {
			t.Errorf("第 %d 条会话 %q 没出现在启动屏\n----\n%s", i+1, item.Label, plain)
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
				m.setStartupRecents(startupRecentsFromRollout, startupFixtureRecents(time.Now()))
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
	m.setStartupRecents(startupRecentsFromRollout, startupFixtureRecents(time.Now()))

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

	m.setStartupRecents(startupRecentsFromRollout, startupFixtureRecents(time.Now()))
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

	_, _ = m.Update(startupRecentsMsg{items: items, source: startupRecentsFromTasks})
	if len(m.startupRecents) != len(items) {
		t.Fatalf("startupRecents 没落地：%d 条，期望 %d 条（消息被丢弃了）", len(m.startupRecents), len(items))
	}
	// 来源必须与条目一起落地：只落 items 会让 hint 说回默认的 /resume。
	if m.startupRecentsSource != startupRecentsFromTasks {
		t.Errorf("startupRecentsSource 没落地：%v，期望 tasks", m.startupRecentsSource)
	}
	if strings.Contains(stripANSI(m.welcomeCache), "还没有历史会话") {
		t.Error("落地后启动屏仍是空态：没有触发重绘")
	}
	if !strings.Contains(stripANSI(m.welcomeCache), items[0].Label) {
		t.Errorf("落地后启动屏没有第 1 条会话\n----\n%s", stripANSI(m.welcomeCache))
	}
	if !strings.Contains(stripANSI(m.welcomeCache), ":hist") {
		t.Errorf("tasks 来源的右卡 hint 应该是 :hist 版本\n----\n%s", stripANSI(m.welcomeCache))
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

// TestStartupScreen_NarrowStackShowsPlanItems 70x24 堆叠档的行预算刚好落在"两卡最小形态
// + 1 行间隔"那一档：左卡 3 条键位一条都不能少（hint 行不许把条目挤掉），右卡至少一条。
//
// 这里**故意不钉死右卡条数**：新让位序是"先砍右卡条目（2→1）再整块省略右卡"，
// 条数由预算算出来，钉死具体条数等于把预算表抄进测试（预算一变就假红）。
// "计划条数与实际渲染一致"由 TestStartupCardBodyHonorsPlanItems 直接钉渲染层。
func TestStartupScreen_NarrowStackShowsPlanItems(t *testing.T) {
	m := startupFixtureWithRecents(t, 70, 24)

	plain := stripANSI(m.renderWelcomePanel())
	for _, want := range []string{"Alt+S", "Ctrl+E", "?"} {
		if !strings.Contains(plain, want) {
			t.Errorf("左卡少了 %q（条目被 hint 行挤掉了）\n----\n%s", want, plain)
		}
	}
	if !strings.Contains(plain, "修复启动页布局") {
		t.Errorf("右卡一条会话都没有\n----\n%s", plain)
	}
	if !strings.Contains(plain, "◷ 最近会话") {
		t.Errorf("右卡整块消失了（这一档应能放下右卡最小形态）\n----\n%s", plain)
	}
}

// TestStartupCardBodyHonorsPlanItems 钉住"body 行数 == plan 承诺的行数"：
// renderTitledCard 见到非空 hint 就占一行，body 若比 plan.items 多画一条，
// 最后一条会被 bodyRows 静默裁掉（不报错、行数不变），用户看到的是少一条历史。
func TestStartupCardBodyHonorsPlanItems(t *testing.T) {
	m := startupFixtureWithRecents(t, 120, 40)
	c := common.DarkModeColors()
	txt := startupTextsFor(m.currentLang)

	for items := 1; items <= 4; items++ {
		plan := startupCardPlan{height: items + 2, items: items}
		if body := m.startupRecentsCardBody(plan, txt, c, 40); len(body) != items {
			t.Errorf("plan.items=%d 无空行: body %d 行，期望 %d 行", items, len(body), items)
		}
		plan = startupCardPlan{height: items + 4, items: items, blank: true, hint: true}
		if body := m.startupRecentsCardBody(plan, txt, c, 40); len(body) != items+1 {
			t.Errorf("plan.items=%d 满配: body %d 行，期望 %d 行（条目 + 空行）", items, len(body), items+1)
		}
	}
}

// TestStartupScreen_EnglishLabels 英文界面只换文案、不换几何：标题与 hint 都换成英文，
// 卡片宽度仍然精确。
func TestStartupScreen_EnglishLabels(t *testing.T) {
	m := startupFixtureModel(t, 120, 40)
	m.currentLang = LangEnglish
	m.setStartupRecents(startupRecentsFromRollout, startupFixtureRecents(time.Now()))

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
		m.setStartupRecents(startupRecentsFromRollout, startupFixtureRecents(time.Now()))

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

// ── 双源与 hint（本次修复的闸门） ───────────────────────────────────────────

// TestStartupScreen_RecentsHintFollowsSource 是"功能性欺骗"的核心闸门：
// 右卡列的是哪个账本的会话，hint 就必须指向那个账本的入口。
// 一句固定的 "/resume ..." 在 tasks 档会骗人 —— /resume 列的是 rollout 账本，另一批会话。
func TestStartupScreen_RecentsHintFollowsSource(t *testing.T) {
	cases := []struct {
		name       string
		source     startupRecentsSource
		wantHint   string
		absentHint string
	}{
		{"rollout→/resume", startupRecentsFromRollout, "/resume 选择并恢复会话", ":hist 浏览并载入历史会话"},
		{"tasks→:hist", startupRecentsFromTasks, ":hist 浏览并载入历史会话", "/resume 选择并恢复会话"},
	}
	for _, c := range cases {
		m := startupFixtureModel(t, 120, 40)
		m.setStartupRecents(c.source, startupFixtureRecents(time.Now()))

		plain := stripANSI(m.renderWelcomePanel())
		if !strings.Contains(plain, c.wantHint) {
			t.Errorf("%s: 右卡 hint 缺少 %q\n----\n%s", c.name, c.wantHint, plain)
		}
		if strings.Contains(plain, c.absentHint) {
			t.Errorf("%s: 右卡出现了不属于该来源的 hint %q\n----\n%s", c.name, c.absentHint, plain)
		}
	}
}

// TestStartupScreen_EmptyRecentsHasNoHint 空态不指路：两源皆空时没有任何会话可恢复，
// "/resume ..." / ":hist ..." 都是指向不存在的东西（空态已有两行引导语）。
func TestStartupScreen_EmptyRecentsHasNoHint(t *testing.T) {
	m := startupFixtureModel(t, 120, 40)

	plain := stripANSI(m.renderWelcomePanel())
	for _, absent := range []string{"选择并恢复会话", "浏览并载入历史会话"} {
		if strings.Contains(plain, absent) {
			t.Errorf("空态不该出现 hint %q\n----\n%s", absent, plain)
		}
	}
}

// TestStartupRecentsLedgerFallback 钉住 loadStartupRecentsCmd 的决策链：
// rollout 有数据就用 rollout（绝不混入 tasks）；rollout 不可用（root 不存在 / 过滤后为空 /
// 读失败）才回退 tasks，并把来源标成 tasks，让 hint 跟着换成 :hist。
func TestStartupRecentsLedgerFallback(t *testing.T) {
	now := time.Now()
	rolloutSessions := []memory.RolloutSessionInfo{
		{SessionID: "sess-a", Preview: "rollout 里的任务", ModTime: now.Add(-time.Hour), HeadMessages: 12},
	}
	tasks := []datamanager.TaskHistoryItem{
		{TaskID: "t1", Title: "tasks 里的任务", UpdatedAt: now.Add(-2 * time.Hour), MessageCount: 4},
	}
	rolloutErr := errors.New("rollout root 不存在")

	cases := []struct {
		name     string
		sessions []memory.RolloutSessionInfo
		rollErr  error
		tasks    []datamanager.TaskHistoryItem
		tasksErr error
		want     startupRecentsSource
		wantLen  int
		wantItem string
	}{
		{"rollout 有数据 → 用 rollout", rolloutSessions, nil, tasks, nil, startupRecentsFromRollout, 1, "rollout 里的任务"},
		{"rollout root 不存在 → 回退 tasks", nil, rolloutErr, tasks, nil, startupRecentsFromTasks, 1, "tasks 里的任务"},
		{"rollout 全是 Empty → 回退 tasks",
			[]memory.RolloutSessionInfo{{SessionID: "x", Empty: true, ModTime: now}}, nil,
			tasks, nil, startupRecentsFromTasks, 1, "tasks 里的任务"},
		{"两源皆空 → None", nil, nil, nil, nil, startupRecentsFromNone, 0, ""},
		{"两源皆读失败 → None", nil, rolloutErr, nil, errors.New("tasks 读失败"), startupRecentsFromNone, 0, ""},
	}
	for _, c := range cases {
		// 与 loadStartupRecentsCmd 同一条决策链：先取 rollout，拿不到才去读 tasks
		//（生产代码里这一步还省掉了一次 tasks 索引构建）。
		msg := startupRecentsMsgFromRollout(c.sessions, c.rollErr, startupRecentsLimit)
		if msg.source != startupRecentsFromRollout {
			msg = startupRecentsMsgFromTasks(c.tasks, c.tasksErr, startupRecentsLimit)
		}
		if msg.source != c.want {
			t.Errorf("%s: source=%v，期望 %v", c.name, msg.source, c.want)
		}
		if len(msg.items) != c.wantLen {
			t.Errorf("%s: %d 条，期望 %d 条", c.name, len(msg.items), c.wantLen)
		}
		if c.wantItem != "" && (len(msg.items) == 0 || msg.items[0].Label != c.wantItem) {
			t.Errorf("%s: 第 1 条 = %+v，期望 Label=%q", c.name, msg.items, c.wantItem)
		}
	}
}

// TestStartupRecentItemsFromRollout_LabelFallback Label 不许是空串：
// Preview → AgentName → SessionID 依次回退；三者皆空时由渲染层兜底成 (无标题)。
// 空 Label 会让那一行只剩序号与时间，用户认不出是哪一次会话。
func TestStartupRecentItemsFromRollout_LabelFallback(t *testing.T) {
	now := time.Now()
	sessions := []memory.RolloutSessionInfo{
		{Preview: "有预览", SessionID: "s1", ModTime: now, HeadMessages: 2},
		{Preview: "", AgentName: "director", SessionID: "s2", ModTime: now, HeadMessages: 3},
		{Preview: "   ", AgentName: "", SessionID: "s3", ModTime: now, HeadMessages: 4},
		{Preview: "", AgentName: "", SessionID: "", ModTime: now, HeadMessages: 5},
		{Preview: "空会话", SessionID: "s5", ModTime: now, Empty: true}, // 必须被过滤
	}
	items := startupRecentItemsFromRollout(sessions, startupRecentsLimit)
	if len(items) != 4 {
		t.Fatalf("投影出 %d 条，期望 4 条（Empty 会话应被过滤）", len(items))
	}
	wantLabels := []string{"有预览", "director", "s3", ""}
	for i, want := range wantLabels {
		if items[i].Label != want {
			t.Errorf("第 %d 条 Label=%q，期望 %q", i+1, items[i].Label, want)
		}
	}

	// 最后兜底：Label 空串必须渲染成 (无标题)，而不是一列空白。
	m := startupFixtureModel(t, 120, 40)
	m.setStartupRecents(startupRecentsFromRollout, []startupRecentItem{{When: now, Turns: 5}})
	plain := stripANSI(m.renderWelcomePanel())
	if !strings.Contains(plain, "(无标题)") {
		t.Errorf("空 Label 没有兜底成 (无标题)\n----\n%s", plain)
	}
}

// TestStartupScreen_StackKeepsGapBetweenCards 堆叠档两卡之间必须有**恰好一行完全空白**。
// 上卡 ╯ 紧接下卡 ╭ 会把两张卡糊成一张大框（没有空行 = 没有分组），
// 所以这一行不参与预算压缩：宁可少一条右卡条目、甚至整块省略右卡。
func TestStartupScreen_StackKeepsGapBetweenCards(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{70, 26}, {70, 24}, {70, 20}} {
		m := startupFixtureWithRecents(t, sz.w, sz.h)
		plain := stripANSI(m.renderWelcomePanel())
		lines := strings.Split(plain, "\n")

		// 反向检查：任何一档都不许出现"上卡 ╯ 紧接下卡 ╭"。
		for i := 1; i < len(lines); i++ {
			if strings.Contains(lines[i-1], "╯") && strings.Contains(lines[i], "╭") {
				t.Errorf("%dx%d: 第 %d 行（╯）与第 %d 行（╭）紧贴，两卡之间没有空行\n----\n%s",
					sz.w, sz.h, i-1, i, plain)
			}
		}

		rightTop := -1
		for i, line := range lines {
			if strings.Contains(line, "◷ 最近会话") {
				rightTop = i
			}
		}
		if rightTop < 0 {
			continue // 该档整块省略右卡：只由上面的"绝不紧贴"约束保证质感
		}
		leftBottom := -1
		for i := 0; i < rightTop; i++ {
			if strings.Contains(lines[i], "╰") {
				leftBottom = i
			}
		}
		if leftBottom < 0 {
			t.Fatalf("%dx%d: 找不到左卡底边框（右卡标题在第 %d 行）\n----\n%s", sz.w, sz.h, rightTop, plain)
		}
		if rightTop-leftBottom != 2 {
			t.Errorf("%dx%d: 两卡之间应恰好 1 行空行（左卡底 %d 行、右卡顶 %d 行）\n----\n%s",
				sz.w, sz.h, leftBottom, rightTop, plain)
		}
		if strings.TrimSpace(lines[leftBottom+1]) != "" {
			t.Errorf("%dx%d: 两卡之间那一行不是空白：%q", sz.w, sz.h, lines[leftBottom+1])
		}
	}
}
