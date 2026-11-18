package components

// ProfileSelectDialog 的边界闸门：越界数字键、空列表、取消语义、超长档名与窄终端、
// 以及"忘了 SetBounds"的防御。
//
// 这五条都是"看起来没事、真出问题时没有任何报错"的那一类：越界数字键若直接索引会 panic
// （而 panic 发生在 TUI 按键处理里，表现是整屏崩溃）；空列表若不守 cursor，渲染就会
// 越界取下标；未 SetBounds 时若按 0×0 渲染，View 会返回堆叠的空行把整屏顶歪。
//
// 组件层不碰磁盘、不碰配置：本文件只用 ProfileOption 造数据。

import (
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
