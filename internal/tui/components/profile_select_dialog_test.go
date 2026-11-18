package components

// ProfileSelectDialog 的边界闸门：越界数字键、空列表、取消语义、超长档名与窄终端、
// 以及"忘了 SetBounds"的防御。
//
// 这五条都是"看起来没事、真出问题时没有任何报错"的那一类：越界数字键若直接索引会 panic
// （而 panic 发生在 TUI 按键处理里，表现是整屏崩溃）；空列表若不守 cursor，渲染就会
// 越界取下标；未 SetBounds 时若按 0×0 渲染，View 会返回堆叠的空行把整屏顶歪。
//
// 组件层不碰磁盘、不碰配置：本文件只用 ProfileOption 造数据。
//
// 后半段（TestProfileSelectDialogNotCreated*）钉的是 "not created yet" 标注：它曾经与
// opt.Path 拼进同一个可省略单元格，被 common.Ellipsize 的**中间省略**切成
// "…t created yet — run yagent setup)"（真实 40 字符 home 下必现）。现在标注独占一行，
// 于是这几条断言可以直接要求**完整文案**，而不必只认尾部片段。

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// profileKeyMsg 构造按键消息：与真实终端送进来的形状一致（可打印字符带 Text）。
func profileKeyMsg(key string) tea.KeyMsg {
	switch key {
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "up":
		return tea.KeyPressMsg{Code: tea.KeyUp}
	case "end":
		return tea.KeyPressMsg{Code: tea.KeyEnd}
	default:
		r := []rune(key)
		return tea.KeyPressMsg{Code: r[0], Text: key}
	}
}

func profileOptionsForTest(names ...string) []ProfileOption {
	opts := make([]ProfileOption, 0, len(names))
	for i, name := range names {
		opts = append(opts, ProfileOption{
			Name:   name,
			Path:   "/home/u/.yagent/config/" + name + ".toml",
			Active: i == 0,
		})
	}
	return opts
}

// TestProfileSelectDialogDigitOutOfRange 数字键直选必须越界安全。
//
// 列表只有 1 项时按 5 / 9 / 2 都不许改状态；只有 2 项时按 3 同理。
// 反过来按 2（第 2 项存在）必须真的确认 —— 否则"越界安全"可以靠"数字键全不生效"骗过去。
func TestProfileSelectDialogDigitOutOfRange(t *testing.T) {
	t.Run("single row ignores every out of range digit", func(t *testing.T) {
		d := NewProfileSelectDialog(profileOptionsForTest("default"), LanguageEn)
		d.SetBounds(100, 30)

		for _, key := range []string{"5", "9", "2"} {
			d.Update(profileKeyMsg(key))
			if d.Confirmed || d.Selected != "" {
				t.Fatalf("key %q selected %q (confirmed=%v) on a 1-row list, want no-op",
					key, d.Selected, d.Confirmed)
			}
			if d.cursor != 0 {
				t.Errorf("key %q moved cursor to %d, want 0", key, d.cursor)
			}
		}
	})

	t.Run("in range digit confirms", func(t *testing.T) {
		d := NewProfileSelectDialog(profileOptionsForTest("default", "work"), LanguageEn)
		d.SetBounds(100, 30)

		d.Update(profileKeyMsg("3")) // 越界：只有 2 项
		if d.Confirmed {
			t.Fatalf("key 3 confirmed %q on a 2-row list, want no-op", d.Selected)
		}

		d.Update(profileKeyMsg("2"))
		if !d.Confirmed || d.Selected != "work" {
			t.Errorf("Selected=%q Confirmed=%v after key 2, want work/true", d.Selected, d.Confirmed)
		}
	})
}

// TestProfileSelectDialogEmptyListIsSafe 空列表既不能渲染，也不能被按键改成非法游标。
func TestProfileSelectDialogEmptyListIsSafe(t *testing.T) {
	d := NewProfileSelectDialog(nil, LanguageEn)
	d.SetBounds(100, 30)

	if out := d.View(); out != "" {
		t.Errorf("empty list rendered %d bytes, want the empty string:\n%s", len(out), out)
	}

	// end / G 会把 cursor 设成 len-1 = -1，组件必须在 Update 末尾把它收回来。
	for _, key := range []string{"down", "up", "j", "k", "enter", "end", "G", "5"} {
		d.Update(profileKeyMsg(key))
		if d.cursor != 0 {
			t.Fatalf("key %q left cursor at %d on an empty list, want 0", key, d.cursor)
		}
		if d.Confirmed || d.Selected != "" {
			t.Fatalf("key %q confirmed %q on an empty list, want no-op", key, d.Selected)
		}
	}

	// 空列表上回车不该产生任何 cmd（尤其不能 tea.Quit）。
	if _, cmd := d.Update(profileKeyMsg("enter")); cmd != nil {
		t.Errorf("enter on an empty list returned cmd %v, want nil", cmd)
	}
}

// TestProfileSelectDialogEscCancels 取消必须同时清掉 Selected 与 Confirmed。
//
// 只清一个的后果：调用方看到 Selected 非空就去落盘（用户按了 Esc 却切换了档），
// 或者看到 Confirmed 仍为 true 而把上一次的选择重复应用一遍。
func TestProfileSelectDialogEscCancels(t *testing.T) {
	for _, key := range []string{"esc", "q", "Q"} {
		t.Run(key, func(t *testing.T) {
			d := NewProfileSelectDialog(profileOptionsForTest("default", "work"), LanguageEn)
			d.SetBounds(100, 30)

			d.Update(profileKeyMsg("enter")) // 先确认一次，确保取消要清的是"已确认"状态
			if !d.Confirmed || d.Selected != "default" {
				t.Fatalf("precondition: Selected=%q Confirmed=%v, want default/true", d.Selected, d.Confirmed)
			}

			_, cmd := d.Update(profileKeyMsg(key))
			if cmd != nil {
				t.Errorf("%s returned cmd %v, want nil (只有 ctrl+c 才退出)", key, cmd)
			}
			if d.Confirmed {
				t.Errorf("%s left Confirmed=true, want false", key)
			}
			if d.Selected != "" {
				t.Errorf("%s left Selected=%q, want the empty string", key, d.Selected)
			}
		})
	}
}

// TestProfileSelectDialogLongNameFitsNarrowTerminal 63 字符的档名（文法上限）在窄终端下
// 既不能撑破宽度，也不能让整行消失。
//
// 列宽上限 profileNameMaxWidth=16 是设计决定：档名超出部分由 CLI 的 `profile list` 兜住。
func TestProfileSelectDialogLongNameFitsNarrowTerminal(t *testing.T) {
	longName := strings.Repeat("a", 63)
	opts := []ProfileOption{
		{Name: longName, Path: "/home/u/.yagent/config/" + longName + ".toml", Active: true},
		{Name: "work", Path: "/home/u/.yagent/config/work.toml"},
	}

	for _, width := range []int{40, 60, 100, 200} {
		d := NewProfileSelectDialog(opts, LanguageEn)
		d.SetBounds(width, 30)

		out := stripANSICodes(d.View())
		if out == "" {
			t.Fatalf("width=%d: rendered nothing, want a dialog", width)
		}
		for _, line := range strings.Split(out, "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Fatalf("width=%d: a line is %d cells wide, want <= %d:\n%s", width, got, width, line)
			}
		}
		// 63 字符必然被省略：不省略就会把路径列挤掉。
		if !strings.Contains(out, "…") {
			t.Errorf("width=%d: long name was not ellipsized:\n%s", width, out)
		}
		if !strings.Contains(out, "work") {
			t.Errorf("width=%d: the short row disappeared:\n%s", width, out)
		}
	}
}

// TestProfileSelectDialogUnsetBoundsRendersNothing 忘了 SetBounds 时必须渲染空串，
// 而不是按 0×0 排版出一堆空行（那会把整屏顶歪，且症状离原因很远）。
func TestProfileSelectDialogUnsetBoundsRendersNothing(t *testing.T) {
	d := NewProfileSelectDialog(profileOptionsForTest("default"), LanguageEn)
	if out := d.View(); out != "" {
		t.Errorf("View() before SetBounds = %d bytes, want the empty string", len(out))
	}
	if w, h := d.Bounds(); w != 0 || h != 0 {
		t.Errorf("Bounds() = (%d,%d), want (0,0) before SetBounds", w, h)
	}

	// 两个阈值都是硬编码的守卫，逐个踩到边界两侧：宽度 40、高度 5。
	d.SetBounds(39, 30)
	if out := d.View(); out != "" {
		t.Errorf("width=39 rendered %d bytes, want nothing (guard is width<40)", len(out))
	}
	d.SetBounds(40, 4)
	if out := d.View(); out != "" {
		t.Errorf("height=4 rendered %d bytes, want nothing (guard is height<5)", len(out))
	}
	d.SetBounds(40, 5)
	if out := d.View(); out == "" {
		t.Error("width=40 height=5 rendered nothing, want the minimal dialog")
	}
}

// TestProfileSelectDialogCursorStartsOnActiveRow 打开时光标落在当前档上；
// 找不到 active（例如 --config 指向了目录外的文件）则退回首行而不是 -1。
func TestProfileSelectDialogCursorStartsOnActiveRow(t *testing.T) {
	opts := []ProfileOption{
		{Name: "default", Path: "/h/config.toml"},
		{Name: "work", Path: "/h/work.toml", Active: true},
		{Name: "infra", Path: "/h/infra.toml"},
	}
	if d := NewProfileSelectDialog(opts, LanguageEn); d.cursor != 1 {
		t.Errorf("cursor = %d, want 1 (the active row)", d.cursor)
	}

	noActive := []ProfileOption{
		{Name: "default", Path: "/h/config.toml"},
		{Name: "work", Path: "/h/work.toml"},
	}
	if d := NewProfileSelectDialog(noActive, LanguageEn); d.cursor != 0 {
		t.Errorf("cursor = %d with no active row, want 0", d.cursor)
	}
}

// profileLongPath 比 88 宽面板的路径列（63 格）还长 —— 用来同时证明两件事：
// 路径**确实**被省略了，而标注**没有**被省略。
//
// 形状刻意贴近真实：macOS 的 home 是 /Users/<username>，较长的用户名加 "Library/Preferences"
// 就能顶到 70+ 格，不需要造畸形串。
const profileLongPath = "/Users/someverylongusername/Library/Preferences/yagent/config/config.toml"

// profileLineContaining 返回第一条包含 sub 的渲染行。
//
// 按行取而不是整段 contains：这条缺陷的外观正是"串还在、中间被吃掉一截"，
// 只有把标注从它所在的那一行单独拎出来，才能判断它旁边有没有省略号。
func profileLineContaining(rendered, sub string) (string, bool) {
	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, sub) {
			return line, true
		}
	}
	return "", false
}

// TestProfileSelectDialogNotCreatedNoteIsNeverEllipsized 标注是"事实"，任何合法宽度下都
// 不许被切。这正是上一版的缺陷：路径太长时 ellipsize 把 "not created yet" 的首尾各留一半，
// 屏幕上出现 "…t created yet — run yagent setup)" —— 一句读不通的话。
//
// 四个宽度覆盖 dialogWidths 的整个合法区间：40 是 View 的下限（innerWidth 恰好取到
// common.PanelWidths 的 30 下限，最窄的一档），200 则被 dialogMaxWidth=88 顶住。
func TestProfileSelectDialogNotCreatedNoteIsNeverEllipsized(t *testing.T) {
	opts := []ProfileOption{
		{Name: "default", Path: profileLongPath, Active: true, NotCreated: true},
		{Name: "work", Path: "/home/u/.yagent/config/work.toml"},
	}

	for _, width := range []int{40, 60, 88, 200} {
		d := NewProfileSelectDialog(opts, LanguageEn)
		d.SetBounds(width, 20)

		out := stripANSICodes(d.View())
		if out == "" {
			t.Fatalf("width=%d: rendered nothing, want a dialog", width)
		}
		for _, line := range strings.Split(out, "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Fatalf("width=%d: a line is %d cells wide, want <= %d:\n%s", width, got, width, line)
			}
		}

		noteLine, ok := profileLineContaining(out, "not created yet")
		if !ok {
			t.Fatalf("width=%d: 完整的 \"not created yet\" 不在渲染结果里（标注又被切了）:\n%s", width, out)
		}
		// 省略号是"标注被截断"的唯一外观特征，它出现在标注行上就是回归。
		if strings.Contains(noteLine, "…") {
			t.Errorf("width=%d: 标注行被省略了，want 完整标注:\n%s", width, noteLine)
		}

		switch {
		case width >= 60:
			// 放得下时后半句（怎么修）也必须完整：前半句完整不足以说明没丢信息。
			if !strings.Contains(noteLine, "not created yet — run yagent setup") {
				t.Errorf("width=%d: 标注不完整，want \"not created yet — run yagent setup\":\n%s", width, noteLine)
			}
		default:
			// 40 格放不下整条（innerWidth 30 - 缩进 6 = 24 < 35）：规则是**整段丢弃**
			// 后半句，而不是把它切成半截（与 common.HintBar 同一取向）。
			if strings.Contains(out, "run yagent setup") {
				t.Errorf("width=%d: 后半句放不下时应整段丢弃，而不是显示一部分:\n%s", width, out)
			}
		}
	}
}

// TestProfileSelectDialogNotCreatedNoteOwnsItsLine 标注必须在档位行的**下一行**、
// 且是该行唯一的内容。
//
// 这一条钉的是修复的形态本身：只要标注还和路径共用一格，路径的省略就迟早会吃掉标注
//（两个信息的可见性耦合在一起）。同时证明路径的省略仍在生效 —— 修法不是"把省略关掉"。
func TestProfileSelectDialogNotCreatedNoteOwnsItsLine(t *testing.T) {
	opts := []ProfileOption{{Name: "default", Path: profileLongPath, Active: true, NotCreated: true}}
	d := NewProfileSelectDialog(opts, LanguageEn)
	d.SetBounds(88, 20)

	out := stripANSICodes(d.View())
	lines := strings.Split(out, "\n")
	pathIdx, noteIdx := -1, -1
	for i, line := range lines {
		if pathIdx < 0 && strings.Contains(line, "/Users/") {
			pathIdx = i
		}
		if noteIdx < 0 && strings.Contains(line, "not created yet") {
			noteIdx = i
		}
	}
	if pathIdx < 0 {
		t.Fatalf("路径列不见了:\n%s", out)
	}
	if noteIdx < 0 {
		t.Fatalf("标注不见了:\n%s", out)
	}
	if noteIdx != pathIdx+1 {
		t.Fatalf("标注应在档位行的下一行（path 行 %d，标注行 %d）:\n%s", pathIdx, noteIdx, out)
	}

	// 标注行前面只许有边框与缩进：有别的字符就说明它还挂在别的单元格里。
	if idx := strings.Index(lines[noteIdx], "not created yet"); idx > 0 {
		if prefix := strings.TrimLeft(lines[noteIdx][:idx], " │"); prefix != "" {
			t.Errorf("标注行前面还有别的内容 %q，want 只有边框与缩进:\n%s", prefix, lines[noteIdx])
		}
	}

	// 路径该省略还得省略：73 格塞不进 63 格的路径列。
	if !strings.Contains(lines[pathIdx], "…") {
		t.Errorf("超长路径没有被省略（省略失效了）:\n%s", lines[pathIdx])
	}
}

// TestProfileSelectDialogNotCreatedNoteFitsHeightBudget 标注行必须算进高度预算。
//
// 扫一整段高度而不是只挑一档：溢出只发生在"行预算刚好被终端按死"的那几个高度上
// （6 条档位下实测 12 与 16 两处无标注不溢出、带标注溢出），单挑一档很容易挑到不溢出的那一档
// 而让闸门变成摆设。11 行是物理下限（chrome 10 + 1 行列表），加标注就是 12 行 —— 那一档
// 任何实现都装不下，所以从 12 行起要求。
func TestProfileSelectDialogNotCreatedNoteFitsHeightBudget(t *testing.T) {
	opts := make([]ProfileOption, 0, 6)
	for i := 0; i < 6; i++ {
		opts = append(opts, ProfileOption{Name: fmt.Sprintf("p%d", i), Path: "/h/p.toml"})
	}
	opts[0].NotCreated = true

	for height := 12; height <= 26; height++ {
		d := NewProfileSelectDialog(opts, LanguageEn)
		d.SetBounds(88, height)
		out := stripANSICodes(d.View())
		if !strings.Contains(out, "not created yet") {
			t.Fatalf("height=%d: 标注没渲染出来，这条闸门就成了摆设:\n%s", height, out)
		}
		if got := lipgloss.Height(out); got > height {
			t.Fatalf("height=%d: 渲染 %d 行，比终端高 —— 标注行没进高度预算:\n%s", height, got, out)
		}
	}

	// 对照：不带标注时同样不溢出，说明上面那条不是靠"把列表压到只剩一行"骗过去的。
	for i := range opts {
		opts[i].NotCreated = false
	}
	d := NewProfileSelectDialog(opts, LanguageEn)
	d.SetBounds(88, 16)
	if out := stripANSICodes(d.View()); lipgloss.Height(out) > 16 {
		t.Fatalf("对照组（无标注）在 16 行终端里渲染 %d 行:\n%s", lipgloss.Height(out), out)
	}
}

// TestProfileSelectDialogNotCreatedNoteFollowsLanguage 两套文案都要完整出现。
//
// 标注是**界面语言**（同标题、同提示条），不是数据（档名/路径不翻译）。中文缺失时
// 中文面板会夹一句英文，与"档名与路径是数据"的既有约定并不冲突 —— 那句话是提示。
func TestProfileSelectDialogNotCreatedNoteFollowsLanguage(t *testing.T) {
	opts := []ProfileOption{{Name: "default", Path: "/h/config.toml", Active: true, NotCreated: true}}

	for _, tc := range []struct {
		lang Language
		want string
	}{
		{LanguageEn, "not created yet — run yagent setup"},
		{LanguageZh, "尚未创建 — 运行 yagent setup"},
	} {
		d := NewProfileSelectDialog(opts, tc.lang)
		d.SetBounds(88, 20)
		if out := stripANSICodes(d.View()); !strings.Contains(out, tc.want) {
			t.Errorf("lang=%v: 缺完整标注 %q:\n%s", tc.lang, tc.want, out)
		}
	}
}
