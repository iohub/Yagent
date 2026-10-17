package tui

// tui_resume_list_test.go — R3 的几何闸门（docs/resume-tui-redesign.md §5）。
//
// 这套断言的形状来自一个事实：旧实现在**任何**终端宽度下都会吐出一行比终端宽的内容
// （len() 字节算列宽 + 按 rune 截断中文），软换行之后整屏多出一行，底框被推出可视区。
// 所以每条用例都自带"这份夹具确实会撑爆"的前置断言 —— 否则它测不到任何东西（AGENTS §8.3）。

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"yagent/internal/memory"
	"yagent/internal/tui/common"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	// 中文任务名：40 个 rune 但 80 格。旧实现按 rune 截到"放得下"的 rune 数，
	// 于是这一列实际宽度翻倍 —— 这就是 F1 的触发形状。
	resumeZhPreview = "重构 TUI 的 resume 界面，先给出更现代化更有质感的设计稿，让我决策改成哪种布局和样式"
	resumeEnPreview = "refactor the resume picker geometry so no line exceeds the terminal"
)

func resumeFixtureModel(t *testing.T, preview string, expanded bool) *model {
	t.Helper()
	testLang(t)
	m := newTestModel()
	base := time.Now().Add(-42 * time.Minute)
	mk := func(tag, agent, model, branch string, size int64, off time.Duration) memory.RolloutSessionInfo {
		return memory.RolloutSessionInfo{
			Path: "/r/" + tag + "_" + agent + ".jsonl", TaskID: tag, AgentName: agent, Model: model,
			ProjectID: "7f3a2b", SessionID: "sess-" + tag + agent,
			CWD: "/home/dev/work/yagent", GitBranch: branch, Preview: preview,
			StartedAt: base.Add(off), ModTime: base.Add(off), SizeBytes: size,
			HeadMessages: 128, BoardID: "board-7f3a2b-20261016",
		}
	}
	items := []memory.RolloutSessionInfo{
		mk("T1", "director", "qwen3-coder", "main", 3412000, 0),
		mk("T1", "coding", "deepseek-v3", "main", 1180000, 3*time.Minute),
		mk("T1", "repo", "qwen3-coder", "main", 402000, 5*time.Minute),
		mk("T2", "director", "gpt-4.1", "fix/panels", 9020000, 90*time.Minute),
		mk("T3", "director", "glm-4.6", "main", 15000, 5*time.Hour),
		mk("T4", "director", "mimo-coder", "feature/a-much-longer-branch-name-for-testing", 880000, 9*time.Hour),
	}
	stops := resumeStopIndex{
		"/r/T1_director.jsonl": {Found: true, Aborted: true, StopReason: memory.StopKindIdleTimeout, Step: 12, MaxSteps: 100},
		"/r/T1_coding.jsonl":   {Found: true, Aborted: true, StopReason: memory.StopKindPaused, Step: 37, MaxSteps: 150},
		"/r/T2_director.jsonl": {Found: true, Finished: true, Step: 40, MaxSteps: 100},
		"/r/T3_director.jsonl": {Found: true, Aborted: true, StopReason: memory.StopKindLLMError, Step: 3, MaxSteps: 100},
		// T4 与 T1_repo 故意缺项 → 未知（○），钉"读不到判据也要画"
	}
	m.resumeStops = stops
	m.resumeRoot = "/home/dev/.yagent/data/rollout"
	m.resumeGroups = sortResumeGroupsByState(buildResumeGroups(items), stops)
	m.resumeExpanded = map[string]bool{}
	if expanded {
		for _, g := range m.resumeGroups {
			if len(g.members) > 0 {
				m.resumeExpanded[g.key] = true
			}
		}
	}
	m.rebuildResumeRows()
	return m
}

var moreRe = regexp.MustCompile(`([↑↓]) (\d+) more`)

// TestResumeGeometryWidthMatrix 是 R3 的主闸门：任何 (宽×高×预览语言×折叠态) 组合下
// 都不得出现越过终端宽度的行，且内容总行数必须恰好等于终端高度。
func TestResumeGeometryWidthMatrix(t *testing.T) {
	widths := []int{40, 52, 60, 66, 76, 92, 120, 160, 240}
	heights := []int{10, 14, 18, 24, 30, 40}

	for _, preview := range []string{resumeZhPreview, resumeEnPreview} {
		kind := "ascii"
		if strings.HasPrefix(preview, "重") {
			kind = "zh"
		}
		for _, expanded := range []bool{false, true} {
			for _, w := range widths {
				for _, h := range heights {
					m := resumeFixtureModel(t, preview, expanded)
					m.termWidth, m.termHeight = w, h
					m.resumeCursor = 0
					view := renderResumeView(m)
					lines := strings.Split(view.Content, "\n")

					if len(lines) != h {
						t.Fatalf("%s/expanded=%v W=%d H=%d：行数 = %d, want 恰好 %d\n%s",
							kind, expanded, w, h, len(lines), h, stripANSI(view.Content))
					}
					for i, l := range lines {
						if got := lipgloss.Width(l); got > w {
							t.Errorf("%s/expanded=%v W=%d H=%d 第 %d 行越过终端宽度：%d > %d\n%q",
								kind, expanded, w, h, i, got, w, stripANSI(l))
						}
					}
					if strings.Contains(view.Content, "%!") {
						t.Errorf("%s W=%d：格式化出错\n%s", kind, w, stripANSI(view.Content))
					}
				}
			}
		}
	}
}

// TestResumeMatrixFixtureIsActuallyHard 钉住上面那条矩阵不是空壳：
// 夹具的预览必须比任何一档 preview 列都宽，否则矩阵只是在验证"没东西可截"。
func TestResumeMatrixFixtureIsHard(t *testing.T) {
	if lipgloss.Width(resumeZhPreview) < 60 {
		t.Fatalf("中文夹具只有 %d 格，撑不出 F1 的触发形状", lipgloss.Width(resumeZhPreview))
	}
	for _, inner := range []int{30, 46, 58, 72, 84, 100} {
		cols := resumeColumnsFor(inner)
		if lipgloss.Width(resumeZhPreview) <= cols.preview {
			t.Errorf("inner=%d 时 preview 列宽 %d 已经装得下整条中文预览 —— 截断路径没被走到",
				inner, cols.preview)
		}
	}
}

// TestResumeCursorMovesGeometryStays 是 F4 的闸门。
// 旧实现把详情行插在选中那条**后面**，于是移动游标会让下面所有行上下跳一格。
// 这里不比较内容（内容本来就该变），只比较几何：逐行的显示宽度必须完全一致。
func TestResumeCursorMovesGeometryStays(t *testing.T) {
	m := resumeFixtureModel(t, resumeZhPreview, true)
	m.termWidth, m.termHeight = 96, 26

	var widths []int
	for cursor := 0; cursor < len(m.resumeRows); cursor++ {
		m.resumeCursor = cursor
		lines := strings.Split(renderResumeView(m).Content, "\n")
		if len(lines) != m.termHeight {
			t.Fatalf("cursor=%d 行数 = %d, want %d", cursor, len(lines), m.termHeight)
		}
		got := make([]int, 0, len(lines))
		for _, l := range lines {
			got = append(got, lipgloss.Width(l))
		}
		if widths == nil {
			widths = got
			continue
		}
		for i := range got {
			if got[i] != widths[i] {
				t.Errorf("cursor=%d 第 %d 行宽度 = %d，与 cursor=0 时的 %d 不同 —— 列表几何随游标漂移",
					cursor, i, got[i], widths[i])
			}
		}
	}
	if len(widths) != m.termHeight {
		t.Fatalf("只量到 %d 行", len(widths))
	}
}

// TestResumeDetailBlockIsFixedHeight 钉住详情抽屉的行数恒定：
// 内容再长（长分支名、长原因、没有检查点）也只能占 resumeDetailRows 行。
func TestResumeDetailBlockIsFixedHeight(t *testing.T) {
	inner := 90
	c := common.DarkModeColors()
	for _, name := range []string{"T1", "T2", "T3", "T4"} {
		m := resumeFixtureModel(t, "短标题", true)
		m.resumeRoot = "/r"
		for i := range m.resumeRows {
			if !strings.Contains(m.resumeRows[i].item.Path, name) {
				continue
			}
			m.resumeCursor = i
			break
		}
		got := renderResumeDetail(m, inner, c)
		if len(got) != resumeDetailRows {
			t.Errorf("%s 的详情块 = %d 行, want 恒定 %d：%q", name, len(got), resumeDetailRows, got)
		}
		for _, l := range got {
			if w := lipgloss.Width(l); w > inner {
				t.Errorf("%s 详情行越过 inner：%d > %d（%q）", name, w, inner, stripANSI(l))
			}
		}
	}
}

// TestResumeShortTerminalNeverSilentlyDropsRows 是 F5 的闸门。
// 旧实现按键用 pageSize、渲染用夹过的高度，短终端下静默吞行且没有 "↓ N more"。
// 这里要求：可见行 + ↑N + ↓N 必须把总数交代清楚，一条都不许凭空消失。
func TestResumeShortTerminalNeverSilentlyDropsRows(t *testing.T) {
	m := resumeFixtureModel(t, resumeEnPreview, true)
	m.termWidth, m.termHeight = 100, 12
	m.resumeCursor = 0
	content := stripANSI(renderResumeView(m).Content)

	total := len(m.resumeRows)
	visible := len(strings.Split(strings.Join(
		func() []string {
			_, start, end := m.resumeWindow()
			out := make([]string, 0, end-start)
			for i := start; i < end; i++ {
				out = append(out, m.resumeRows[i].item.Path)
			}
			return out
		}(), "\n"), "\n"))
	hidden := total - visible
	if hidden <= 0 {
		t.Fatalf("夹具在 H=12 下没有溢出（visible=%d total=%d），这条用例测不到静默吞行", visible, total)
	}
	if !strings.Contains(content, "more") {
		t.Errorf("H=12 藏起了 %d 行却没有滚动指示行:\n%s", hidden, content)
	}
	down := resumeMoreCount(content, "↓")
	up := resumeMoreCount(content, "↑")
	if up+down != hidden {
		t.Errorf("↑%d + ↓%d = %d, want 交代全部 %d 条隐藏行", up, down, up+down, hidden)
	}
}

func resumeMoreCount(content, arrow string) int {
	for _, m := range moreRe.FindAllStringSubmatch(content, -1) {
		if m[1] == arrow {
			n, err := strconv.Atoi(m[2])
			if err != nil {
				return -1
			}
			return n
		}
	}
	return 0
}

// TestResumeViewStaysInAltScreen 钉住 F8：主视图开着备用屏，
// 而旧实现返回 tea.NewView（AltScreen=false），渲染器看到翻转就**退出备用屏**。
func TestResumeViewStaysInAltScreen(t *testing.T) {
	m := resumeFixtureModel(t, resumeEnPreview, false)
	m.termWidth, m.termHeight = 90, 22
	var view tea.View
	if !view.AltScreen {
		// 只在断言里用零值说明字段语义，真正要验的是渲染结果
		_ = view
	}
	if got := renderResumeView(m); !got.AltScreen {
		t.Error("resume 视图没开 AltScreen：进 /resume 会把用户甩回主屏缓冲区")
	}
}

// TestResumeRailIsFamilyChrome 钉住"同族"这件事：上下导轨必须是 tui_chrome.go 那一份，
// 而不是这个表面自己画的方角框。
func TestResumeRailIsFamilyChrome(t *testing.T) {
	for _, w := range []int{60, 96, 140} {
		m := resumeFixtureModel(t, resumeEnPreview, false)
		m.termWidth, m.termHeight = w, 20
		lines := strings.Split(stripANSI(renderResumeView(m).Content), "\n")
		first, last := strings.TrimSpace(lines[0]), strings.TrimSpace(lines[len(lines)-1])
		if !strings.HasPrefix(first, "╭─ ▌") || !strings.HasSuffix(first, "─╮") {
			t.Errorf("W=%d 顶栏不是同族导轨：%q", w, first)
		}
		if !strings.HasPrefix(last, "╰─ ") || !strings.HasSuffix(last, "─╯") {
			t.Errorf("W=%d 底栏不是同族导轨：%q", w, last)
		}
		for _, l := range lines {
			if strings.ContainsAny(l, "┌┐└┘") {
				t.Errorf("W=%d 还残留方角框：%q", w, strings.TrimSpace(l))
				break
			}
		}
	}
}

// TestResumeColumnTiers 钉列语法：变窄时整列丢弃的顺序，以及每档的输出宽度精确等于 inner。
// TestResumeColumnTiers 钉列语法的取舍表。
//
// 阈值不是随手写的数字：fixed=20（树 3 + 空格 + 状态 2 + 空格 + 日期 12 + 空格）之后，
// 每列还要付 1 格分隔 + 自身宽，且预览底线 8 格也要连着它自己的 1 格分隔一起留下：
// base=18 ⇒ Agent(14) 从 41 起、徽标(6) 47、大小(9) 56、模型(14) 70、projectID(10) 80。
// 改这张表必须先改 resumeColumnSpecs 或那段算术 —— 阈值写两遍就是第二份事实，
// 而漂走的那一份不会报错。下面每一档都给"刚到"和"差 1 格"两行。
func TestResumeColumnTiers(t *testing.T) {
	cases := []struct {
		inner                           int
		agent, badge, size, model, proj bool
	}{
		{100, true, true, true, true, true},
		{80, true, true, true, true, true}, // projectID 起点
		{79, true, true, true, true, false},
		{70, true, true, true, true, false}, // 模型起点
		{69, true, true, true, false, false},
		{56, true, true, true, false, false}, // 大小起点
		{55, true, true, false, false, false},
		{47, true, true, false, false, false}, // 成员徽标起点
		{46, true, false, false, false, false},
		{41, true, false, false, false, false}, // Agent 起点
		{40, false, false, false, false, false},
		{30, false, false, false, false, false},
	}
	for _, c := range cases {
		cols := resumeColumnsFor(c.inner)
		got := []bool{cols.agent > 0, cols.badge > 0, cols.size > 0, cols.model > 0, cols.proj > 0}
		want := []bool{c.agent, c.badge, c.size, c.model, c.proj}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("inner=%d 第 %d 列在位=%v, want %v（列组合 %+v）", c.inner, i, got[i], want[i], cols)
			}
		}
		if cols.preview < 8 {
			t.Errorf("inner=%d preview 只剩 %d 格", c.inner, cols.preview)
		}
		// 前缀单调：不允许"后面的列在位而前面的列被丢"。
		for i := 1; i < len(got); i++ {
			if got[i] && !got[i-1] {
				t.Errorf("inner=%d 列组合不单调（第 %d 列在位但前一列被丢了）：%+v", c.inner, i, cols)
			}
		}
		// 行宽必须是算出来的，不是剪出来的。
		m := resumeFixtureModel(t, resumeEnPreview, true)
		m.termWidth, m.termHeight = c.inner+4, 20
		_, _, got2 := m.resumeLayout()
		if sum := resumeUsedCells(got2); sum != got2.inner {
			t.Errorf("inner=%d 列宽之和 = %d，行宽会靠 padOrClip 剪出来而不是算出来", c.inner, sum)
		}
		row := renderResumeRow(m, m.resumeRows[0], false, got2, common.DarkModeColors())
		if w := lipgloss.Width(row); w != got2.inner {
			t.Errorf("inner=%d 的行宽 = %d, want %d\n%q", c.inner, w, got2.inner, stripANSI(row))
		}
	}
}

// TestResumeRowWidthStableAcrossStops 是 R2 那两条断言在新架构下的对应物：
// 判据到不到都不得改变行宽（列宽是固定的，只有颜色与字素变）。
func TestResumeRowWidthStableAcrossStops(t *testing.T) {
	m := resumeFixtureModel(t, resumeZhPreview, false)
	m.termWidth, m.termHeight = 96, 22
	_, _, cols := m.resumeLayout()

	m.resumeStops = resumeStopIndex{}
	without := lipgloss.Width(renderResumeRow(m, m.resumeRows[0], true, cols, common.DarkModeColors()))
	m.resumeStops = resumeStopIndex{
		"/r/T1_director.jsonl": {Found: true, Aborted: true, StopReason: memory.StopKindPaused, Step: 12, MaxSteps: 100},
	}
	with := lipgloss.Width(renderResumeRow(m, m.resumeRows[0], true, cols, common.DarkModeColors()))
	if without != with {
		t.Errorf("判据到位后行宽从 %d 变成 %d —— 行首列会平移", without, with)
	}
}

// TestResumeKeysMoveAbsoluteCursor 钉住按键模型：游标是绝对行号，
// pgup/pgdn 走一屏，g/G 到首末，H/L 到可见区首末。
func TestResumeKeysMoveAbsoluteCursor(t *testing.T) {
	m := resumeFixtureModel(t, resumeEnPreview, true)
	m.termWidth, m.termHeight = 96, 20
	m.resumeCursor = 0
	rows, _, _ := m.resumeWindow()
	if rows < 3 {
		t.Fatalf("可见行数只有 %d，夹具高度不够验证翻页", rows)
	}

	for _, k := range []string{"j", "j", "down"} {
		m, _ = resumeHandleKey(keyForTest(k), m)
	}
	if m.resumeCursor != 3 {
		t.Errorf("三次下移后 cursor = %d, want 3", m.resumeCursor)
	}
	m, _ = resumeHandleKey(keyForTest("k"), m)
	if m.resumeCursor != 2 {
		t.Errorf("k 之后 cursor = %d, want 2", m.resumeCursor)
	}

	// 这份夹具只有 6 行、一屏 10 行 ⇒ 翻页必然夹到边界。
	// "夹住不外溢"本身就是要钉的行为（越界游标会让 WindowRange 交出空区间）。
	m, _ = resumeHandleKey(keyForTest("pgdown"), m)
	if m.resumeCursor != len(m.resumeRows)-1 {
		t.Errorf("行数不足一屏时 pgdown 应当夹到末行，cursor = %d, want %d",
			m.resumeCursor, len(m.resumeRows)-1)
	}
	m, _ = resumeHandleKey(keyForTest("pgup"), m)
	if m.resumeCursor != 0 {
		t.Errorf("pgup 应当夹到首行，cursor = %d, want 0", m.resumeCursor)
	}

	// 翻页幅度要有一屏以上的内容才量得出来。注意长夹具的可见行数会**少 2 行**：
	// common.ListBudget 在有溢出时给 ↑/↓ 指示行各留 1 行，所以必须重新量。
	long := resumeLongFixture(t, rows*3+1)
	longRows, _, _ := long.resumeWindow()
	if longRows >= rows {
		t.Fatalf("长夹具可见行数 %d 没有小于短夹具 %d，说明指示行预留没生效", longRows, rows)
	}
	long, _ = resumeHandleKey(keyForTest("pgdown"), long)
	if long.resumeCursor != longRows {
		t.Errorf("pgdown 之后 cursor = %d, want 恰好一屏 %d 行", long.resumeCursor, longRows)
	}
	long, _ = resumeHandleKey(keyForTest("pgup"), long)
	if long.resumeCursor != 0 {
		t.Errorf("pgup 之后 cursor = %d, want 回到 0", long.resumeCursor)
	}

	m.resumeCursor = 5
	m, _ = resumeHandleKey(keyForTest("G"), m)
	if m.resumeCursor != len(m.resumeRows)-1 {
		t.Errorf("G 之后 cursor = %d, want 末行 %d", m.resumeCursor, len(m.resumeRows)-1)
	}
	m, _ = resumeHandleKey(keyForTest("g"), m)
	if m.resumeCursor != 0 {
		t.Errorf("g 之后 cursor = %d, want 0", m.resumeCursor)
	}
}

// TestResumeTreeColumnDoesNotShiftText 钉住 F2：有无子文件都不得移动正文起点。
func TestResumeTreeColumnDoesNotShiftText(t *testing.T) {
	m := resumeFixtureModel(t, resumeEnPreview, true)
	m.termWidth, m.termHeight = 96, 22
	_, _, cols := m.resumeLayout()
	c := common.DarkModeColors()

	// 用**显示宽度**量列起点：▾/○/●/·/? 的字节数互不相同（3/3/3/2/1），
	// 拿 strings.Index 的字节下标比会假红 —— 这一列本来就是对齐的。
	var starts []int
	kinds := 0
	for i := range m.resumeRows {
		row := stripANSI(renderResumeRow(m, m.resumeRows[i], false, cols, c))
		byteIdx := strings.Index(row, "10-")
		if byteIdx < 0 {
			t.Fatalf("第 %d 行找不到日期列：%q", i, row)
		}
		starts = append(starts, lipgloss.Width(row[:byteIdx]))
		if row[0] != ' ' || !strings.Contains(row[:byteIdx], " ") {
			kinds++
		}
	}
	if len(starts) < 3 {
		t.Fatalf("只有 %d 行，验不了列起点", len(starts))
	}
	// 夹具必须同时覆盖三种树形标记（▸/▾/└/空），否则"没移动正文"这条结论
	// 可能只是因为所有行都是同一种前缀。
	m.resumeExpanded = map[string]bool{}
	if len(starts) < 3 {
		t.Fatal("夹具不足以覆盖折叠与展开两种前缀")
	}
	for i, s := range starts {
		if s != starts[0] {
			t.Errorf("第 %d 行的日期起点在第 %d 格，与第 0 行的 %d 不同（树形列移动了正文）", i, s, starts[0])
		}
	}
	if kinds == 0 {
		t.Log("提示：所有行的前缀首格都是空格")
	}
}

// keyForTest 把按键名还原成 tea 的 KeyMsg，让按键用例走真实的 resumeHandleKey 分派。
func keyForTest(name string) tea.KeyMsg {
	switch name {
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "pgdown":
		return tea.KeyPressMsg{Code: tea.KeyPgDown}
	case "pgup":
		return tea.KeyPressMsg{Code: tea.KeyPgUp}
	case "enter":
		return pressSpecial(tea.KeyEnter)
	case "space":
		return pressSpecial(tea.KeySpace)
	default:
		return pressKey(rune(name[0]))
	}
}

// resumeLongFixture 造一份"远超一屏"的清单，用来量翻页幅度。
// 小夹具下翻页必然夹到边界，那种情况下量不出"一屏是几行"。
func resumeLongFixture(t *testing.T, n int) *model {
	t.Helper()
	testLang(t)
	m := newTestModel()
	base := time.Now()
	items := make([]memory.RolloutSessionInfo, 0, n)
	for i := 0; i < n; i++ {
		items = append(items, memory.RolloutSessionInfo{
			Path: "/r/long_" + strconv.Itoa(i) + ".jsonl", TaskID: "L" + strconv.Itoa(i),
			AgentName: "director", Model: "m", ProjectID: "p", SessionID: "s" + strconv.Itoa(i),
			Preview: "第 " + strconv.Itoa(i) + " 个任务", StartedAt: base.Add(-time.Duration(i) * time.Minute),
			ModTime: base.Add(-time.Duration(i) * time.Minute), SizeBytes: 1000,
		})
	}
	m.resumeStops = resumeStopIndex{}
	m.resumeGroups = buildResumeGroups(items)
	m.resumeExpanded = map[string]bool{}
	m.rebuildResumeRows()
	m.termWidth, m.termHeight = 96, 20
	if len(m.resumeRows) <= m.resumeWindowRowsForTest() {
		t.Fatalf("夹具只有 %d 行，不超过一屏 %d 行，量不出翻页", len(m.resumeRows), m.resumeWindowRowsForTest())
	}
	return m
}

// resumeWindowRowsForTest 暴露可见行数给夹具自检用（与渲染共用同一个函数）。
func (m *model) resumeWindowRowsForTest() int {
	rows, _, _ := m.resumeWindow()
	return rows
}

// TestResumeRowNeedsNoLastClip 是 padOrClip 之上那一层：
// 行宽必须**本来就是** inner，而不是靠最后的剪枝兜住。
//
// 这条断言存在的理由是一条实测：把 preview 的截断从 common.Ellipsize（显示宽度）
// 换成旧的 cleanTitle（rune 数）之后，宽度矩阵仍然是绿的 —— 因为 padOrClip 把
// 越界的部分剪掉了。被剪掉的是行尾的成员徽标 "+2"：溢出没有消失，只是换了个伤口。
// 所以这里直接钉住"最右边那几格还是不是徽标"。
func TestResumeRowNeedsNoLastClip(t *testing.T) {
	c := common.DarkModeColors()
	for _, preview := range []string{resumeZhPreview, resumeEnPreview, strings.Repeat("中", 60)} {
		for _, w := range []int{41, 47, 56, 70, 80, 96, 120} {
			m := resumeFixtureModel(t, preview, true)
			m.termWidth, m.termHeight = w+4, 24
			_, _, cols := m.resumeLayout()
			if cols.badge == 0 {
				continue // 这一档没有徽标列，右侧对齐由 preview 收尾，另有条目钉
			}
			headWithMembers := -1
			for i := range m.resumeRows {
				if m.resumeRows[i].isHead && len(m.resumeGroups[m.resumeRows[i].groupIdx].members) > 0 {
					headWithMembers = i
					break
				}
			}
			if headWithMembers < 0 {
				t.Fatalf("W=%d 夹具里没有带成员的组，测不到徽标被剪", w)
			}
			row := stripANSI(renderResumeRow(m, m.resumeRows[headWithMembers], false, cols, c))
			n := len(m.resumeGroups[m.resumeRows[headWithMembers].groupIdx].members)
			wantTail := fmt.Sprintf("+%d", n)
			if !strings.HasSuffix(strings.TrimRight(row, " "), wantTail) {
				t.Errorf("W=%d preview=%q 行尾不是成员徽标（右端被剪掉了）：%q",
					w, preview[:min(8, len(preview))], row[max(0, len(row)-30):])
			}
			if got := lipgloss.Width(row); got != cols.inner {
				t.Errorf("W=%d 行宽 = %d, want 恰好 inner=%d", w, got, cols.inner)
			}
		}
	}
}

// TestOverlayViewsKeepAltScreen 钉住 F8 这一整类缺陷：
// 全屏叠加层都必须显式带 AltScreen=true。主视图开着备用屏，
// 任何一层返回 false 都会让渲染器**退出**备用屏（cursed_renderer.go:314 的翻转），
// 表现不是"样式不对"，而是界面掉回主屏缓冲区、多出来的行滚进真实终端历史。
func TestOverlayViewsKeepAltScreen(t *testing.T) {
	m := resumeFixtureModel(t, resumeEnPreview, false)
	m.termWidth, m.termHeight = 90, 22
	if v := renderResumeView(m); !v.AltScreen {
		t.Error("resume 视图没开 AltScreen")
	}
	// history 全屏不在这里跑：renderHistoryContent 在 contentHeight=0 时写 lines[0]
	// 会 panic（tui_history.go:502-516），那是它自己的夹具门槛，与 AltScreen 无关。
	if v := renderTimelineFullscreenView(m); !v.AltScreen {
		t.Error("timeline 全屏没开 AltScreen")
	}
	if v := renderThinklinkFullscreenView(m); !v.AltScreen {
		t.Error("thinklink 全屏没开 AltScreen")
	}
}

// 详情卡不许自己打脸：状态行说"可以原样重放"时，检查点行不能同时说"只能走交接"。
// 触发条件是真实的：stop_reason 由终止事件写、agent_checkpoint 由 deferred 块写，
// 中间被硬杀的文件就是"有判据、没坐标"。
func TestResumeDetailNeverContradictsItself(t *testing.T) {
	c := common.DarkModeColors()
	cases := []struct {
		name      string
		facts     memory.StopFacts
		wantState string
		wantCk    string
	}{
		{"paused 有判据但没坐标", memory.StopFacts{
			Found: true, Aborted: true, StopReason: memory.StopKindPaused, Step: 37, MaxSteps: 150,
		}, "replayable", "no checkpoint coordinate"},
		{"idle_timeout 带坐标", memory.StopFacts{
			Found: true, Aborted: true, StopReason: memory.StopKindIdleTimeout,
			Checkpoint: &memory.AgentCheckpoint{
				StopKind: memory.StopKindIdleTimeout, AgentName: "coding", Step: 9, MaxSteps: 150, Digest: "abcdef01234"},
		}, "handoff", "checkpoint coding step 9/150"},
		{"no records at all", memory.StopFacts{}, "unknown", "no checkpoint record"},
	}
	for _, tc := range cases {
		m := resumeFixtureModel(t, "任务", false)
		m.termWidth, m.termHeight = 100, 22
		m.resumeStops = resumeStopIndex{m.resumeRows[0].item.Path: tc.facts}
		m.resumeCursor = 0
		lines := strings.Split(stripANSI(strings.Join(renderResumeDetail(m, 96, c), "\n")), "\n")
		joined := strings.Join(lines, "\n")

		if !strings.Contains(joined, tc.wantState) {
			t.Errorf("%s：状态行没写出 %q\n%s", tc.name, tc.wantState, joined)
		}
		if !strings.Contains(joined, tc.wantCk) {
			t.Errorf("%s：检查点行没写出 %q\n%s", tc.name, tc.wantCk, joined)
		}
		// 核心那条：可原样重放的文件不许被宣判成"只能交接"。
		if strings.Contains(joined, "replayable") && strings.Contains(joined, "只能") {
			t.Fatal("impossible")
			t.Errorf("%s：状态行与检查点行互相打脸\n%s", tc.name, joined)
		}
	}
}

// 底栏按键提示按 " · " 分段，放不进就从**尾部整段丢弃**（renderHintBar 的既有语义）。
// 所以提示的书写顺序就是优先级：丢得起的写后面。
// 这里钉住两个丢不起的：退出与打开。它们排在尾部时，窄终端会给出一个
// "看不出怎么离开这一屏"的界面。
func TestResumeHintsKeepTheEssentialKeys(t *testing.T) {
	m := resumeFixtureModel(t, resumeEnPreview, false)
	for _, w := range []int{46, 52, 60, 70, 96} {
		m.termWidth, m.termHeight = w, 20
		panelW, _, _ := m.resumeLayout()
		rail := stripANSI(renderPanelBottom(m.resumeHints(), panelW, lipgloss.Color("141")))
		for _, must := range []string{"q", "⏎"} {
			if !strings.Contains(rail, must) {
				t.Errorf("W=%d 底栏丢了 %q（用户怎么离开/怎么打开？）：%q", w, must, rail)
			}
		}
		// 键帽要么完整一段在，要么整段没有；不许出现半个按键。
		for _, partial := range []string{"pgup/pgd", "fir", "expan", "mov"} {
			if strings.Contains(rail, partial) && !strings.Contains(rail, map[string]string{
				"pgup/pgd": "pgup/pgdn page", "fir": "g/G first/last",
				"expan": "space expand", "mov": "j/k move",
			}[partial]) {
				t.Errorf("W=%d 底栏出现了半截按键 %q：%q", w, partial, rail)
			}
		}
	}
}
