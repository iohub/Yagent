package tui

import (
	"image/color"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/lipgloss/v2"
)

// 分组版斜杠补全面板的闸门。核心是**行数契约**：renderSkillSuggestions 画出来的行数
// 必须等于 editorBlockHeight 算出来的行数，差一行 viewport 就抖（AGENTS.md 硬约束 3）。

var (
	panelCmdFixtures = []completionItem{
		{name: "resume", kind: completionCmd, desc: "恢复会话"},
		{name: "history", kind: completionCmd, desc: "浏览历史任务"},
		{name: "rs", kind: completionCmd}, // 空 desc：不许留尾随空格
	}
	panelSkillFixtures = []completionItem{
		{name: "pdf-split", kind: completionSkill, desc: "按页拆分 PDF"},
	}
)

// panelTestModel 造一个补全面板展开的模型（只碰补全相关状态，其余留给被测函数）。
func panelTestModel(t *testing.T, width int, items ...completionItem) *model {
	t.Helper()
	m := chromeTestModel(t, width, 40)
	m.skillAutoComplete = true
	m.skillSuggestions = items
	return m
}

// completionPanelLines 是面板的实际渲染行（去色）；空串 = 整个面板不渲染 = 0 行。
func completionPanelLines(panel string) []string {
	if panel == "" {
		return nil
	}
	return strings.Split(stripANSI(panel), "\n")
}

// countRenderedGroupHeaders 数面板里真正出现的组头行。
// 按图形符号认行：窄终端下组名会被截成 "◆ …"，认全名就会漏数。
func countRenderedGroupHeaders(panel string) int {
	n := 0
	for _, line := range completionPanelLines(panel) {
		text := strings.TrimLeft(line, " ")
		if strings.HasPrefix(text, "◆") || strings.HasPrefix(text, "◇") {
			n++
		}
	}
	return n
}

// panelGroupLines 把面板拆成"组头 + 该组条目行"，用于按组断言列对齐。
// 返回的键是组名（"◆ 命令" / "◇ 技能"），值是去掉组头的条目行。
func panelGroupLines(t *testing.T, panel string) map[string][]string {
	t.Helper()
	rows := map[string][]string{}
	var current string
	lines := completionPanelLines(panel)
	for i, line := range lines {
		text := strings.TrimLeft(line, " ")
		switch {
		case strings.HasPrefix(text, "◆"):
			current = "◆ 命令"
			rows[current] = nil
		case strings.HasPrefix(text, "◇"):
			current = "◇ 技能"
			rows[current] = nil
		case i == len(lines)-1:
			current = "" // 末行是操作提示，不属于任何组
		default:
			if current != "" {
				rows[current] = append(rows[current], line)
			}
		}
	}
	return rows
}

// TestCompletionPanel_ExactLayout 钉住目标外观本身：缩进 4、组头补满分隔线、
// 名字补齐到组内最长、说明前恰好一个间隔、选中行光标 ▶、末行键帽提示。
func TestCompletionPanel_ExactLayout(t *testing.T) {
	const width = 40
	m := panelTestModel(t, width, append(append([]completionItem{}, panelCmdFixtures...), panelSkillFixtures...)...)

	panel := m.renderSkillSuggestions()
	lines := completionPanelLines(panel)
	dashes := strings.Repeat("─", width-completionIndent-lipgloss.Width("◆ 命令")-1)

	want := []string{
		"    ◆ 命令 " + dashes,
		"    ▶ resume  恢复会话",
		"      history 浏览历史任务",
		"      rs",
		"    ◇ 技能 " + strings.Repeat("─", width-completionIndent-lipgloss.Width("◇ 技能")-1),
		"      pdf-split 按页拆分 PDF",
		"    Tab 切换   ⏎ 选择   Esc 关闭",
	}
	if dashes == "" {
		t.Fatal("夹具宽度 40 下组头必须补满分隔线，算出的线数是空的")
	}

	got := make([]string, len(lines))
	for i, line := range lines {
		got[i] = strings.TrimRight(line, " ")
	}
	wantTrimmed := make([]string, len(want))
	for i, line := range want {
		wantTrimmed[i] = strings.TrimRight(line, " ")
	}

	if len(got) != len(wantTrimmed) {
		t.Fatalf("行数 = %d, want %d\n实际:\n%s", len(got), len(wantTrimmed), strings.Join(got, "\n"))
	}
	for i := range wantTrimmed {
		if got[i] != wantTrimmed[i] {
			t.Errorf("第 %d 行 = %q, want %q", i, got[i], wantTrimmed[i])
		}
	}
}

// TestSkillSuggestionHeightMatchesRenderedLines 是防抖测试：
// editorBlockHeight 为面板加的那些行，必须与 renderSkillSuggestions 真正输出的行数一致。
// 覆盖只有命令、只有技能、两者都有、交错、两者都为空五种组合 × 从窄到宽的终端。
func TestSkillSuggestionHeightMatchesRenderedLines(t *testing.T) {
	vars := []struct {
		name        string
		items       []completionItem
		wantHeaders int
	}{
		{"both-empty", nil, 0},
		{"only-cmd", panelCmdFixtures, 1},
		{"only-skill", panelSkillFixtures, 1},
		{"cmd+skill", append(append([]completionItem{}, panelCmdFixtures...), panelSkillFixtures...), 2},
		// 交错：数据层顺序不是"命令在前"时也要分成两组、组头各一行。
		{"interleaved", []completionItem{
			{name: "resume", kind: completionCmd, desc: "恢复会话"},
			{name: "pdf-split", kind: completionSkill, desc: "按页拆分 PDF"},
			{name: "history", kind: completionCmd, desc: "浏览历史任务"},
		}, 2},
	}

	for _, w := range []int{8, 10, 14, 20, 40, 80, 120, 200} {
		for _, v := range vars {
			m := panelTestModel(t, w, v.items...)
			if got := m.skillSuggestionHeaderCount(); got != v.wantHeaders {
				t.Errorf("w=%d variant=%s: 组头数 = %d, want %d", w, v.name, got, v.wantHeaders)
			}

			panel := m.renderSkillSuggestions()
			lines := completionPanelLines(panel)
			if got := countRenderedGroupHeaders(panel); got != v.wantHeaders {
				t.Errorf("w=%d variant=%s: 渲染出的组头行 = %d, want %d\n%s",
					w, v.name, got, v.wantHeaders, strings.Join(lines, "\n"))
			}

			rendered := len(lines)
			withPanel := m.editorBlockHeight()
			m.skillAutoComplete = false
			base := m.editorBlockHeight()
			m.skillAutoComplete = true
			if delta := withPanel - base; delta != rendered {
				t.Errorf("w=%d variant=%s: editorBlockHeight 多算了 %d 行，渲染却有 %d 行\n%s",
					w, v.name, delta, rendered, strings.Join(lines, "\n"))
			}

			// 每一行都不许超出终端宽度：超宽会被 lipgloss 再折一次，
			// 屏上的行数就比这里数的多了（AGENTS.md §11 坑 7）。
			if w < completionIndent {
				continue
			}
			for i, line := range strings.Split(panel, "\n") {
				if panel == "" {
					break
				}
				if got := lipgloss.Width(line); got > w {
					t.Errorf("w=%d variant=%s 第 %d 行宽 %d 超出终端：%q",
						w, v.name, i, got, stripANSI(line))
				}
			}
		}
	}
}

// TestCompletionPanel_GroupHeadersAppearByKind 空组不渲染组头：
// 输入 `/h` 只剩命令时不该冒出一个空的"◇ 技能"，反之同理。
func TestCompletionPanel_GroupHeadersAppearByKind(t *testing.T) {
	tests := []struct {
		name    string
		items   []completionItem
		hasCmd  bool
		hasDash bool
	}{
		{"只有命令", panelCmdFixtures, true, false},
		{"只有技能", panelSkillFixtures, false, true},
		{"两组都有", append(append([]completionItem{}, panelCmdFixtures...), panelSkillFixtures...), true, true},
	}

	for _, tc := range tests {
		m := panelTestModel(t, 60, tc.items...)
		panel := stripANSI(m.renderSkillSuggestions())

		if got := strings.Contains(panel, "◆ 命令"); got != tc.hasCmd {
			t.Errorf("%s: 出现命令组头 = %v, want %v\n%s", tc.name, got, tc.hasCmd, panel)
		}
		if got := strings.Contains(panel, "◇ 技能"); got != tc.hasDash {
			t.Errorf("%s: 出现技能组头 = %v, want %v\n%s", tc.name, got, tc.hasDash, panel)
		}
	}

	// 两者都为空时整个面板不渲染，组头一行也不许留。
	m := panelTestModel(t, 60)
	if panel := m.renderSkillSuggestions(); panel != "" {
		t.Errorf("空建议集应不渲染面板，实际 %q", stripANSI(panel))
	}
	if n := m.skillSuggestionHeaderCount(); n != 0 {
		t.Errorf("空建议集的组头数 = %d, want 0", n)
	}
}

// TestCompletionPanel_DescColumnAligned 同组内说明列必须从同一列起：
// 名字补齐到组内最长名，再加一个间隔 —— 这是"分组版"取代角标后唯一的对齐来源。
func TestCompletionPanel_DescColumnAligned(t *testing.T) {
	m := panelTestModel(t, 80, append(append([]completionItem{}, panelCmdFixtures...), panelSkillFixtures...)...)
	groups := panelGroupLines(t, m.renderSkillSuggestions())

	// 组名 → 该组的名字集合与说明文本，来自上面的夹具定义。
	wantCol := map[string]int{}
	for _, tc := range []struct {
		group string
		items []completionItem
	}{
		{"◆ 命令", panelCmdFixtures},
		{"◇ 技能", panelSkillFixtures},
	} {
		longest := 0
		for _, item := range tc.items {
			if w := lipgloss.Width(item.name); w > longest {
				longest = w
			}
		}
		wantCol[tc.group] = completionIndent + completionCursorWidth + longest + completionNameGap
	}

	for group, rows := range groups {
		for _, item := range func() []completionItem {
			if group == "◇ 技能" {
				return panelSkillFixtures
			}
			return panelCmdFixtures
		}() {
			row := -1
			for i, line := range rows {
				if strings.Contains(line, item.name) {
					row = i
					break
				}
			}
			if row < 0 {
				t.Fatalf("%s 组里没有 %s 这一行：%v", group, item.name, rows)
			}

			if item.desc == "" {
				// 没有说明就不许把间隔补成尾随空格。
				if rows[row] != strings.TrimRight(rows[row], " ") {
					t.Errorf("%s/%s: 空说明的行有尾随空格 %q", group, item.name, rows[row])
				}
				continue
			}
			idx := strings.Index(rows[row], item.desc)
			if idx < 0 {
				t.Fatalf("%s/%s: 行里没有说明 %q（%q）", group, item.name, item.desc, rows[row])
			}
			// 列位按显示宽度算，不是字节下标：名字里的 "▶" 与说明里的中文都是多字节。
			if col := lipgloss.Width(rows[row][:idx]); col != wantCol[group] {
				t.Errorf("%s/%s: 说明起始列 = %d, want %d（行 %q）",
					group, item.name, col, wantCol[group], rows[row])
			}
		}
	}
}

// TestCompletionPanel_SelectedRowBandIsContinuous 选中行必须是一条没断的底色带：
// 缩进、光标与名字之间的裸空格、行尾补白都得涂上 236（与侧栏横带同一套判据）。
// 组头则必须完全没有底色 —— 带子铺到标题上是另一种噪声。
func TestCompletionPanel_SelectedRowBandIsContinuous(t *testing.T) {
	seq := sgrParams(t, lipgloss.NewStyle().Background(completionTokens.Surface), "48;")
	m := panelTestModel(t, 60, append(append([]completionItem{}, panelCmdFixtures...), panelSkillFixtures...)...)
	m.skillSuggestionIdx = 1 // history：光标列之外的片段最多，最容易露出没涂色的缝

	panel := m.renderSkillSuggestions()
	lines := strings.Split(panel, "\n")

	selected := -1
	for i, line := range lines {
		if strings.Contains(stripANSI(line), "▶") {
			selected = i
			break
		}
	}
	if selected < 0 {
		t.Fatalf("面板里没有选中行：%q", stripANSI(panel))
	}
	if gaps := bandGaps(lines[selected], seq); gaps != "" {
		t.Errorf("选中行有 %d 格没铺底色：%q\n原始串 %q", len([]rune(gaps)), gaps, lines[selected])
	}
	if got, want := lipgloss.Width(lines[selected]), m.completionBandWidth()+completionIndent; got != want {
		t.Errorf("选中行宽 = %d, want %d（整行铺满）", got, want)
	}
	if got := lipgloss.Width(lines[0]); got != m.completionBandWidth()+completionIndent {
		t.Errorf("组头行宽 = %d, want %d（分隔线补满内容区）", got, m.completionBandWidth()+completionIndent)
	}

	for i, line := range lines {
		text := strings.TrimLeft(stripANSI(line), " ")
		if !strings.HasPrefix(text, "◆") && !strings.HasPrefix(text, "◇") {
			continue
		}
		if gaps := bandGaps(line, seq); gaps != stripANSI(line) {
			t.Errorf("第 %d 行是组头，不该有底色：%q", i, line)
		}
	}
}

// TestCompletionPanel_SelectionUsesGlobalIndex m.skillSuggestionIdx 是
// m.skillSuggestions 的全局下标（Tab 按它取模循环）；分组只改呈现，不能改选中的含义。
func TestCompletionPanel_SelectionUsesGlobalIndex(t *testing.T) {
	items := append(append([]completionItem{}, panelCmdFixtures...), panelSkillFixtures...)
	m := panelTestModel(t, 60, items...)

	for idx, wantName := range []string{"resume", "history", "rs", "pdf-split"} {
		m.skillSuggestionIdx = idx
		rows := panelGroupLines(t, m.renderSkillSuggestions())

		highlighted := []string{}
		for _, lines := range rows {
			for _, line := range lines {
				if strings.HasPrefix(strings.TrimLeft(line, " "), "▶") {
					highlighted = append(highlighted, line)
				}
			}
		}
		if len(highlighted) != 1 {
			t.Fatalf("idx=%d: 高亮行 %d 行, want 1", idx, len(highlighted))
		}
		if !strings.Contains(highlighted[0], wantName) {
			t.Errorf("idx=%d: 高亮的是 %q, want %s", idx, stripANSI(highlighted[0]), wantName)
		}
	}

	// 越界下标不该 panic，也不该有任何高亮。
	m.skillSuggestionIdx = len(items) + 7
	if strings.Contains(stripANSI(m.renderSkillSuggestions()), "▶") {
		t.Error("越界的 skillSuggestionIdx 不该高亮某一行")
	}
}

// TestCompletionPanel_NarrowTerminalTruncatesDesc 窄终端下说明按显示宽度截断加 …，
// 不许把 CJK 双宽字符切一半（半截 rune 会污染下一列）、更不许折行改行数。
func TestCompletionPanel_NarrowTerminalTruncatesDesc(t *testing.T) {
	items := []completionItem{
		{name: "resume", kind: completionCmd, desc: "恢复会话，接着上一次的任务继续跑"},
		{name: "pdf-split", kind: completionSkill, desc: "按页拆分 PDF，并把每一页回传给调用方"},
	}

	for _, w := range []int{12, 16, 20, 24, 32, 60} {
		m := panelTestModel(t, w, items...)
		panel := m.renderSkillSuggestions()
		lines := completionPanelLines(panel)

		if want := m.skillSuggestionHeaderCount() + len(items) + 1; len(lines) != want {
			t.Fatalf("w=%d: 行数 = %d, want %d\n%s", w, len(lines), want, strings.Join(lines, "\n"))
		}
		for i, line := range strings.Split(panel, "\n") {
			if got := lipgloss.Width(line); got > w {
				t.Errorf("w=%d 第 %d 行宽 %d 超出终端：%q", w, i, got, stripANSI(line))
			}
			if !utf8.ValidString(stripANSI(line)) {
				t.Errorf("w=%d 第 %d 行被截出半个字符：%q", w, i, stripANSI(line))
			}
		}
	}

	// w=20：技能组名字列宽 9，说明只剩 4 列预算 —— 必须截断并留省略号。
	m := panelTestModel(t, 20, items...)
	rows := panelGroupLines(t, m.renderSkillSuggestions())
	skillRow := rows["◇ 技能"][0]
	if strings.Contains(skillRow, "按页拆分 PDF") {
		t.Errorf("w=20 不该塞得下整条说明：%q", skillRow)
	}
	if !strings.HasSuffix(strings.TrimRight(skillRow, " "), "…") {
		t.Errorf("截断必须以省略号收尾，否则看着像说明到此为止：%q", skillRow)
	}

	// w=12：说明列预算是负的，宁可整段不写，也不留一个尾随空格。
	// 选中行例外 —— 它的行尾补白是底色带的一部分（见 TestCompletionPanel_SelectedRowBandIsContinuous），
	// 所以这里高亮技能行、检查未选中的命令行。
	m = panelTestModel(t, 12, items...)
	m.skillSuggestionIdx = 1
	rows = panelGroupLines(t, m.renderSkillSuggestions())
	cmdRow := rows["◆ 命令"][0]
	if strings.Contains(cmdRow, "恢复") || strings.Contains(cmdRow, "…") {
		t.Errorf("w=12 放不下说明，就该整段不写：%q", cmdRow)
	}
	if cmdRow != strings.TrimRight(cmdRow, " ") {
		t.Errorf("没有说明的行留了尾随空格：%q", cmdRow)
	}
	if got := lipgloss.Width(m.renderSkillSuggestions()); got > 12 {
		t.Errorf("面板整体宽度 %d 超出 w=12", got)
	}
}

// TestCompletionPanel_SelectedRowTextIsReadable 选中行压在 236 底色上的两档字
// 都必须达到 4.5:1；未选中那档说明色（244）在底色上不达标，
// 所以它绝不能被顺手搬到选中行上（与 TestCategoryRows_OnBandTextIsReadable 同判据）。
func TestCompletionPanel_SelectedRowTextIsReadable(t *testing.T) {
	bg := completionTokens.Surface
	for _, tc := range []struct {
		what string
		fg   color.Color
	}{
		{"选中行名字（Primary 39）", lipgloss.Color("39")},
		{"选中行说明（TextPrimary 252）", lipgloss.Color("252")},
		{"选中光标（Accent 214）", lipgloss.Color("214")},
		{"未选中名字（TextSecondary 248）", lipgloss.Color("248")},
	} {
		if r := contrast(tc.fg, bg); r < 4.5 {
			t.Errorf("%s 压在 236 底色上只有 %.2f:1（要求 ≥4.5:1）", tc.what, r)
		}
	}

	// 反面：244 在 236 上不达标 —— 这正是选中行必须换掉说明色的理由。
	if r := contrast(lipgloss.Color("244"), bg); r >= 4.5 {
		t.Errorf("244 在底色上有 %.2f:1 —— 那选中行就不必单独提一档说明色了", r)
	}

	// 令牌与渲染必须一致：换掉 completionDescSelStyle 的前景就红。
	if r := contrast(completionDescSelStyle.GetForeground(), bg); r < 4.5 {
		t.Errorf("选中行说明色的实际对比度只有 %.2f:1", r)
	}
}

// TestCompletionPanel_ZeroTermWidthDoesNotPanic termWidth 未就绪（0/负数）时
// 面板仍要产出与高度公式一致的行数，且不算出负宽度。
func TestCompletionPanel_ZeroTermWidthDoesNotPanic(t *testing.T) {
	for _, w := range []int{0, -40, 1, 5} {
		m := chromeTestModel(t, 80, 40)
		m.termWidth = w
		m.skillAutoComplete = true
		m.skillSuggestions = append(append([]completionItem{}, panelCmdFixtures...), panelSkillFixtures...)

		panel := m.renderSkillSuggestions()
		lines := completionPanelLines(panel)
		want := m.skillSuggestionHeaderCount() + len(m.skillSuggestions) + 1
		if len(lines) != want {
			t.Errorf("termWidth=%d: 行数 = %d, want %d\n%s", w, len(lines), want, strings.Join(lines, "\n"))
		}
		if got := m.editorBlockHeight() - func() int {
			m.skillAutoComplete = false
			defer func() { m.skillAutoComplete = true }()
			return m.editorBlockHeight()
		}(); got != want {
			t.Errorf("termWidth=%d: editorBlockHeight 多算了 %d 行, want %d", w, got, want)
		}
	}
}
