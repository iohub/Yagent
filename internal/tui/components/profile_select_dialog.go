package components

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"yagent/internal/tui/common"
)

// ProfileOption 是配置档选择器中的一行。
//
// Path 由调用方算好（config.ProfileConfigPath）：组件层不该知道 profile 的布局规则，
// 它只负责把一行文字排版好。NotCreated 只对 default 有实际含义 —— 它是内置档，
// 逻辑上永远存在，但它的 config.toml 可能还没生成，此时必须标出来。
type ProfileOption struct {
	Name       string
	Path       string
	Active     bool
	NotCreated bool
}

// ProfileSelectDialog 是配置档选择列表：↑/↓（或 j/k）移动，数字键 1-9 直选，
// Enter 确认，Esc 取消。确认只**回传**选择（Selected + Confirmed），
// 真正的落盘与"重启生效"提示由 TUI 层负责 —— 组件层不碰配置，也不碰文件。
//
// 刻意不照抄 ModelSelectDialog 的 Persist 字段：model 有"临时切换 / 写入 config.toml"
// 两态，而 profile 只有一态（写偏好 + 下次启动生效）。留一个恒为 true 的布尔
// 只会让人误以为还有另一条路径。
type ProfileSelectDialog struct {
	options  []ProfileOption
	cursor   int
	width    int
	height   int
	lang     Language
	Selected string // 确认后为档名；取消时为 ""
	// Confirmed 与 Selected 配套：Selected 非空但 Confirmed 为 false 的情形不该出现，
	// 拆成两个字段是为了让调用方读到"用户真的按了 Enter"，而不是"游标恰好停在哪"。
	Confirmed bool
}

const (
	// profileNameMaxWidth 档名列的上限。profile 名文法上限 64 字符，但那是文件名余量，
	// 面板里超过 16 格就把它挤掉了路径列 —— 长名字本身在 CLI 的 list 里随时可查。
	profileNameMaxWidth = 16
	// profilePathMinWidth 路径列的最小可读宽度；窄于此值就整列舍弃，
	// 与 ModelSelectDialog 在 innerWidth < 60 时舍弃 ACTIVE 徽标列同一取向。
	profilePathMinWidth = 12
	// profileHeader / profilePathHeader 是两列的表头。
	profileHeader     = "PROFILE"
	profilePathHeader = "PATH"
	// profileActiveMark 当前激活档的标记，与 CLI `profile list` 的 `*` 同一符号。
	profileActiveMark = "*"
	// profileNoteNotCreated 与 CLI `runProfileList` 的文案保持一致：
	// 同一个事实在两处用两种说法，用户会以为是两件事。
	profileNoteNotCreated = " (not created yet — run yagent setup)"
)

// NewProfileSelectDialog 创建选择器，游标初始落在当前激活的档上（未找到则为首行）。
//
// lang 只影响标题与按键提示：档名与路径是数据，不做翻译。
func NewProfileSelectDialog(options []ProfileOption, lang Language) *ProfileSelectDialog {
	cursor := 0
	for i, o := range options {
		if o.Active {
			cursor = i
			break
		}
	}
	return &ProfileSelectDialog{options: options, cursor: cursor, lang: lang}
}

// ID returns the unique identifier for this dialog.
func (d *ProfileSelectDialog) ID() string { return "profile_select_dialog" }

// Type returns the dialog type.
func (d *ProfileSelectDialog) Type() DialogType { return DialogModal }

// Init initializes the component.
func (d *ProfileSelectDialog) Init() tea.Cmd { return nil }

// Update processes incoming messages.
//
// 键位与 ModelSelectDialog 逐条对齐（用户已经学会那一套），只去掉空格 ——
// profile 没有"临时 / 永久"二态，多留一个确认键只会让人猜它有什么别的意思。
func (d *ProfileSelectDialog) Update(msg tea.Msg) (Component, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}

	switch key := keyMsg.String(); key {
	case "up", "k", "ctrl+p":
		d.move(-1)
	case "down", "j", "ctrl+n":
		d.move(1)
	case "pgup", "ctrl+u":
		d.move(-d.pageStep())
	case "pgdown", "ctrl+d":
		d.move(d.pageStep())
	case "home", "g":
		d.cursor = 0
	case "end", "G":
		d.cursor = len(d.options) - 1
	case "enter":
		d.confirm()
	case "esc", "q", "Q":
		d.Selected = "" // cancelled
		d.Confirmed = false
	case "ctrl+c":
		return d, tea.Quit
	default:
		d.selectByDigit(key)
	}
	if len(d.options) == 0 {
		d.cursor = 0
	}
	return d, nil
}

// move 按 delta 移动游标，首尾相接（长列表里比硬停更容易到达目标）。
func (d *ProfileSelectDialog) move(delta int) {
	n := len(d.options)
	if n == 0 || delta == 0 {
		return
	}
	d.cursor = ((d.cursor+delta)%n + n) % n
}

// pageStep 翻页步长，取可见行数的一半。
func (d *ProfileSelectDialog) pageStep() int {
	rows, _ := d.listRows()
	step := rows / 2
	if step < 1 {
		step = 1
	}
	return step
}

// confirm 确认当前游标处的档。
func (d *ProfileSelectDialog) confirm() {
	if d.cursor >= 0 && d.cursor < len(d.options) {
		d.Selected = d.options[d.cursor].Name
		d.Confirmed = true
	}
}

// selectByDigit 处理数字键直选：按键数字与列表行号一致（仅前 9 项）。
// 数字直选也是一次确认（与 ModelSelectDialog 不同：那边直选是"临时切换"，
// 这里没有临时档这回事）。
func (d *ProfileSelectDialog) selectByDigit(key string) {
	if len(key) != 1 || key[0] < '1' || key[0] > '9' {
		return
	}
	idx := int(key[0] - '1')
	if idx >= len(d.options) || idx >= dialogQuickKeys {
		return
	}
	d.cursor = idx
	d.Selected = d.options[idx].Name
	d.Confirmed = true
}

// isZh 报告是否中文界面。语言在创建时快照，之后不随 langManager 变化。
func (d *ProfileSelectDialog) isZh() bool { return d.lang == LanguageZh }

// View renders the dialog as a string.
func (d *ProfileSelectDialog) View() string {
	if d.width < 40 || d.height < 5 || len(d.options) == 0 {
		return ""
	}

	c := common.DarkModeColors()
	borderStyle := common.DialogBorderStyle(c)
	titleStyle := common.SectionHeaderStyle(c)
	helpStyle := common.HelpTextStyle(c)
	cursorStyle := common.CursorIndicatorStyle(c)
	activeStyle := lipgloss.NewStyle().Foreground(c.Success).Bold(true)
	headerStyle := lipgloss.NewStyle().Foreground(c.TextMuted)
	idxStyle := lipgloss.NewStyle().Foreground(c.TextMuted)

	dialogWidth, innerWidth := dialogWidths(d.width)

	rows, showMore := d.listRows()
	start, end := d.scrollWindow(rows)
	visible := d.options[start:end]

	// ── 列宽 ──
	// 用全部条目（而非仅可见条目）计算，滚动时列宽不会跳动。
	idxW := len(strconv.Itoa(len(d.options)))
	nameW := lipgloss.Width(profileHeader)
	for _, o := range d.options {
		nameW = max(nameW, lipgloss.Width(o.Name))
	}
	nameW = min(nameW, profileNameMaxWidth)

	// 固定开销：游标 2 + 序号 idxW + 空格 + 激活标记 1 + 空格 + 列间距 2
	fixed := 2 + idxW + 1 + 1 + 1 + 2
	pathW := innerWidth - fixed - nameW
	showPath := pathW >= profilePathMinWidth
	if !showPath {
		// 窄终端：把整行让给档名。路径在 `yagent profile list` 里随时可查，
		// 而在面板里挤成 "~/…/x" 也认不出是哪个文件。
		nameW = max(innerWidth-fixed, 8)
		pathW = 0
	}

	// ── 表头 ──
	header := strings.Repeat(" ", 2+idxW+1+1+1) + headerStyle.Render(ellipsize(profileHeader, nameW))
	if showPath {
		header += "  " + headerStyle.Render(ellipsize(profilePathHeader, min(pathW, lipgloss.Width(profilePathHeader))))
	}

	// ── 列表 ──
	lines := make([]string, 0, len(visible)+2)
	lines = append(lines, header)
	for i, opt := range visible {
		index := start + i
		focused := index == d.cursor

		marker := "  "
		if focused {
			marker = cursorStyle.Render("▶ ")
		}
		mark := " "
		if opt.Active {
			mark = activeStyle.Render(profileActiveMark)
		}

		nameColor := c.TextSecondary
		switch {
		case opt.Active:
			nameColor = c.Success
		case focused:
			nameColor = c.TextPrimary
		}
		nameStyle := lipgloss.NewStyle().Foreground(nameColor).Bold(focused || opt.Active)
		pathStyle := lipgloss.NewStyle().Foreground(c.TextMuted)
		if focused {
			pathStyle = pathStyle.Foreground(c.TextSecondary)
		}

		row := marker + idxStyle.Render(padLeft(strconv.Itoa(index+1), idxW)) + " " +
			mark + " " + nameStyle.Render(ellipsize(opt.Name, nameW))
		if showPath {
			cell := opt.Path
			if opt.NotCreated {
				cell += profileNoteNotCreated
			}
			row += "  " + pathStyle.Render(ellipsize(cell, pathW))
		}
		lines = append(lines, row)
	}
	if showMore && start > 0 {
		lines = append(lines, moreLine(start, true, innerWidth, c))
	}
	if showMore && end < len(d.options) {
		lines = append(lines, moreLine(len(d.options)-end, false, innerWidth, c))
	}

	// ── 提示 ──
	// 提示换行会撑破高度预算，所以一律经 HintBar 单行裁切（顺序即优先级，esc 在最前）。
	hint := helpStyle.Render(profileDialogHint(innerWidth, d.isZh(), len(d.options) > 1))

	title := "Select Config Profile"
	if d.isZh() {
		title = "选择配置档"
	}

	dialogContent := lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render(title),
		"",
		strings.Join(lines, "\n"),
		"",
		hint,
	)

	dialog := borderStyle.Width(dialogWidth).Render(dialogContent)

	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, dialog)
}

// profileDialogHint 是档位选择器的按键提示。
//
// "重启生效"写进提示而不是只写在 footer：用户在这个面板里做的决定，后果就在这
// 一屏上；等信息行出现时他已经离开面板了。esc 依然排第一（HintBar 宽度不够时
// 从尾部整段丢弃，退出通道必须最先活下来）。
func profileDialogHint(innerWidth int, zh, multi bool) string {
	var hints string
	switch {
	case zh && multi:
		hints = "esc 取消 · ⏎ 选择（重启后生效） · 1-9 跳转 · j/k ↑/↓ 移动"
	case zh:
		hints = "esc 取消 · ⏎ 选择（重启后生效） · j/k ↑/↓ 移动"
	case multi:
		hints = "esc cancel · ⏎ select (applies on restart) · 1-9 jump · j/k ↑/↓ move"
	default:
		hints = "esc cancel · ⏎ select (applies on restart) · j/k ↑/↓ move"
	}
	return common.HintBar(hints, innerWidth)
}

// listRows 返回列表可占用的行数，以及是否渲染上下滚动指示行。
func (d *ProfileSelectDialog) listRows() (int, bool) {
	return listBudget(d.height, len(d.options))
}

// scrollWindow 返回可见条目的 [start, end) 区间，游标尽量居中。
func (d *ProfileSelectDialog) scrollWindow(rows int) (int, int) {
	return windowRange(len(d.options), d.cursor, rows)
}

// IsFocused reports whether this dialog has keyboard focus.
func (d *ProfileSelectDialog) IsFocused() bool { return true }

// Focus sets the component as focused.
func (d *ProfileSelectDialog) Focus() tea.Cmd { return nil }

// Blur removes focus from this component.
func (d *ProfileSelectDialog) Blur() {}

// SetBounds sets the component's dimensions.
func (d *ProfileSelectDialog) SetBounds(width, height int) {
	d.width = width
	d.height = height
}

// Bounds returns the component's current width and height.
func (d *ProfileSelectDialog) Bounds() (int, int) { return d.width, d.height }

// IsVisible reports whether this component is currently visible.
func (d *ProfileSelectDialog) IsVisible() bool { return true }

// SetVisible sets the visibility of this component.
func (d *ProfileSelectDialog) SetVisible(v bool) {}
