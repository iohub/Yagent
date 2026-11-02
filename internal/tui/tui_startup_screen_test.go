package tui

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

func startupFixtureModel(t *testing.T, w, h int) *model {
	t.Helper()
	InitLangManager()
	m := newTestModel()
	m.termWidth, m.termHeight = w, h
	m.currentLang = LangChinese
	m.projectDir = "/tmp/yagent-startup-fixture"
	return m
}

func startupFixtureRecents(now time.Time) []startupRecentItem {
	return []startupRecentItem{
		{Label: "修复启动页布局", When: now.Add(-2 * time.Hour), Turns: 18},
		{Label: "蓝图导出层设计", When: now.Add(-26 * time.Hour), Turns: 42},
		{Label: "MCP 工具注册排查", When: now.Add(-72 * time.Hour), Turns: 7},
		{Label: "blackboard 索引优化", When: now.Add(-120 * time.Hour), Turns: 26},
	}
}

func (m *model) setStartupRecents(source startupRecentsSource, items []startupRecentItem) {
	m.startupRecents, m.startupRecentsSource = items, source
}

func startupFixtureWithRecents(t *testing.T, w, h int) *model {
	t.Helper()
	m := startupFixtureModel(t, w, h)
	m.setStartupRecents(startupRecentsFromRollout, startupFixtureRecents(time.Now()))
	return m
}

func renderStartupPanel(t *testing.T, m *model) (out string, canvasW, canvasH int) {
	t.Helper()
	out = m.renderWelcomePanel()
	w, h := m.startupScreenSize()
	return out, w, h
}

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
			if len(lines) != canvasH {
				t.Errorf("%s %dx%d: 启动屏 %d 行，期望恰好 %d 行（Place 会补齐，不该少也不该多）\n----\n%s",
					label, sz.w, sz.h, len(lines), canvasH, stripANSI(out))
			}
			for i, line := range lines {
				if got := lipgloss.Width(line); got != canvasW {
					t.Errorf("%s %dx%d: 第 %d 行宽 %d，期望恰好 %d\n%q",
						label, sz.w, sz.h, i, got, canvasW, stripANSI(line))
					break
				}
			}
		}
	}
}

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

func TestUpdateLandsStartupRecentsMsg(t *testing.T) {
	m := startupFixtureModel(t, 120, 40)
	_ = m.renderWelcomePanel()
	items := startupFixtureRecents(time.Now())

	_, _ = m.Update(startupRecentsMsg{items: items, source: startupRecentsFromTasks})
	if len(m.startupRecents) != len(items) {
		t.Fatalf("startupRecents 没落地：%d 条，期望 %d 条（消息被丢弃了）", len(m.startupRecents), len(items))
	}
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
	if got := startupRelTimeAt(time.Date(2026, 11, 1, 20, 0, 0, 0, time.Local), now, LangEnglish); got != "yesterday" {
		t.Errorf("英文 yesterday: 得到 %q", got)
	}
	if got := startupRelTimeAt(now.Add(-12*time.Minute), now, LangEnglish); got != "12m" {
		t.Errorf("英文 12m: 得到 %q", got)
	}
}

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

func TestStartupScreen_EmptyRecentsHasNoHint(t *testing.T) {
	m := startupFixtureModel(t, 120, 40)

	plain := stripANSI(m.renderWelcomePanel())
	for _, absent := range []string{"选择并恢复会话", "浏览并载入历史会话"} {
		if strings.Contains(plain, absent) {
			t.Errorf("空态不该出现 hint %q\n----\n%s", absent, plain)
		}
	}
}

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

func TestStartupRecentItemsFromRollout_LabelFallback(t *testing.T) {
	now := time.Now()
	sessions := []memory.RolloutSessionInfo{
		{Preview: "有预览", SessionID: "s1", ModTime: now, HeadMessages: 2},
		{Preview: "", AgentName: "director", SessionID: "s2", ModTime: now, HeadMessages: 3},
		{Preview: "   ", AgentName: "", SessionID: "s3", ModTime: now, HeadMessages: 4},
		{Preview: "", AgentName: "", SessionID: "", ModTime: now, HeadMessages: 5},
		{Preview: "空会话", SessionID: "s5", ModTime: now, Empty: true},
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

	m := startupFixtureModel(t, 120, 40)
	m.setStartupRecents(startupRecentsFromRollout, []startupRecentItem{{When: now, Turns: 5}})
	plain := stripANSI(m.renderWelcomePanel())
	if !strings.Contains(plain, "(无标题)") {
		t.Errorf("空 Label 没有兜底成 (无标题)\n----\n%s", plain)
	}
}

func TestStartupScreen_StackKeepsGapBetweenCards(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{70, 26}, {70, 24}, {70, 20}} {
		m := startupFixtureWithRecents(t, sz.w, sz.h)
		plain := stripANSI(m.renderWelcomePanel())
		lines := strings.Split(plain, "\n")

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
			continue
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

func startupIndent(line string) (left, right int) {
	return len(line) - len(strings.TrimLeft(line, " ")),
		len(line) - len(strings.TrimRight(line, " "))
}

func TestStartupScreen_WideTerminalCentersCards(t *testing.T) {
	m := startupFixtureWithRecents(t, 120, 40)
	out := m.renderWelcomePanel()
	plain := stripANSI(out)
	lines := strings.Split(plain, "\n")

	topIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "╭─ ⚡") {
			topIdx = i
			break
		}
	}
	if topIdx < 0 {
		t.Fatalf("120x40 找不到左卡顶边框\n----\n%s", plain)
	}

	for _, probe := range []struct {
		name  string
		idx   int
		match string
	}{{"顶边框", topIdx, "╭─ ⚡"}} {
		left, right := startupIndent(lines[probe.idx])
		if left < 6 {
			t.Errorf("%s左侧只有 %d 列空白，应 >= 6（卡片区要收拢到 %d 宽并居中）\n%s",
				probe.name, left, startupCardsMaxWidth, lines[probe.idx])
		}
		if d := left - right; d < -1 || d > 1 {
			t.Errorf("%s左右空白不对称：左 %d 右 %d（差 %d > 1 列）\n%s",
				probe.name, left, right, d, lines[probe.idx])
		}
	}

	bottomIdx := -1
	for i := topIdx; i < len(lines); i++ {
		if strings.Contains(lines[i], "╰") {
			bottomIdx = i
		}
	}
	if bottomIdx < 0 {
		t.Fatalf("120x40 找不到卡片底边框\n----\n%s", plain)
	}
	topLeft, topRight := startupIndent(lines[topIdx])
	botLeft, botRight := startupIndent(lines[bottomIdx])
	if topLeft != botLeft || topRight != botRight {
		t.Errorf("卡片顶/底边框缩进不一致：顶(%d,%d) 底(%d,%d)——卡片是斜的",
			topLeft, topRight, botLeft, botRight)
	}

	below := len(lines) - 1 - bottomIdx
	if below < startupCardsBottomMargin {
		t.Errorf("卡片底边框下方只有 %d 行空白，期望 >= %d（别贴着标签栏）\n----\n%s",
			below, startupCardsBottomMargin, plain)
	}
}

func TestStartupScreen_BrandGapAboveCards(t *testing.T) {
	m := startupFixtureWithRecents(t, 120, 40)
	plain := stripANSI(m.renderWelcomePanel())
	lines := strings.Split(plain, "\n")

	cwdIdx, topIdx := -1, -1
	for i, line := range lines {
		if cwdIdx < 0 && strings.Contains(line, "yagent-startup-fixture") {
			cwdIdx = i
		}
		if strings.Contains(line, "╭─ ⚡") {
			topIdx = i
			break
		}
	}
	if cwdIdx < 0 || topIdx < 0 {
		t.Fatalf("找不到 cwd 行（%d）或卡片顶边框（%d）\n----\n%s", cwdIdx, topIdx, plain)
	}
	if gap := topIdx - cwdIdx - 1; gap != startupBrandGap {
		t.Errorf("品牌区与卡片区之间 %d 行空白，期望 %d 行（cwd 在第 %d 行、卡顶在第 %d 行）\n----\n%s",
			gap, startupBrandGap, cwdIdx, topIdx, plain)
	}
	for i := cwdIdx + 1; i < topIdx; i++ {
		if strings.TrimSpace(lines[i]) != "" {
			t.Errorf("第 %d 行本该是呼吸空行，却有内容：%q", i, lines[i])
		}
	}
}

func TestStartupScreen_NarrowTerminalIgnoresCardsMaxWidth(t *testing.T) {
	for _, sz := range []struct{ w, h int }{{80, 24}, {70, 24}, {70, 20}} {
		m := startupFixtureWithRecents(t, sz.w, sz.h)
		plain := stripANSI(m.renderWelcomePanel())
		lines := strings.Split(plain, "\n")

		topIdx := -1
		for i, line := range lines {
			if strings.Contains(line, "╭─ ⚡") {
				topIdx = i
				break
			}
		}
		if topIdx < 0 {
			t.Fatalf("%dx%d 找不到左卡顶边框\n----\n%s", sz.w, sz.h, plain)
		}
		fieldW := m.computeFieldWidth()
		if fieldW >= startupCardsMaxWidth {
			t.Fatalf("%dx%d 夹具前提不成立：字段宽 %d 不该 >= 上界 %d", sz.w, sz.h, fieldW, startupCardsMaxWidth)
		}
		left, right := startupIndent(lines[topIdx])
		wantLeft := (sz.w - fieldW) / 2
		if left != wantLeft {
			t.Errorf("%dx%d: 卡片左缩进 %d 列，期望 %d（窄终端不该被收拢）\n%s",
				sz.w, sz.h, left, wantLeft, lines[topIdx])
		}
		if d := left - right; d < -1 || d > 1 {
			t.Errorf("%dx%d: 左右空白不对称：左 %d 右 %d\n%s", sz.w, sz.h, left, right, lines[topIdx])
		}
	}
}

func TestCenterStartupCards(t *testing.T) {
	rows := []string{"╭──────╮", "", "│ 中文 │", strings.Repeat("x", 30)}
	fieldW, cardsW := 20, 10
	got := centerStartupCards(rows, fieldW, cardsW)
	if len(got) != len(rows) {
		t.Fatalf("行数变了：%d，期望 %d", len(got), len(rows))
	}
	padLeft := (fieldW - cardsW) / 2
	for i, row := range got {
		if w := lipgloss.Width(row); w != fieldW {
			t.Errorf("第 %d 行宽 %d，期望恰为 %d（padLeft 没计入整块宽度）", i, w, fieldW)
		}
		if strings.TrimSpace(row) == "" {
			continue
		}
		if left, _ := startupIndent(row); left != padLeft {
			t.Errorf("第 %d 行左缩进 %d，期望 %d", i, left, padLeft)
		}
		if content := lipgloss.Width(strings.TrimSpace(row)); content > cardsW {
			t.Errorf("第 %d 行内容宽 %d，超过卡片区上界 %d", i, content, cardsW)
		}
	}

	for _, row := range centerStartupCards([]string{"╭──╮"}, 8, 104) {
		if w := lipgloss.Width(row); w != 8 {
			t.Errorf("窄终端行宽 %d，期望 8", w)
		}
		if left, _ := startupIndent(row); left != 0 {
			t.Errorf("窄终端左缩进 %d，期望 0", left)
		}
	}

	if got := centerStartupCards(nil, 20, 10); got != nil {
		t.Errorf("无卡片时应返回 nil，得到 %#v", got)
	}
}
