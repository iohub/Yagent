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
	// profileNoteNotCreated / profileNoteSetupHint 是"档位文件尚未创建"的标注。
	//
	// 它**独占一行**渲染，不参与路径列的省略 —— 曾经它与 opt.Path 拼进同一个可省略
	// 单元格，再被 common.Ellipsize 的**中间省略**（保留首尾）切成
	// "…t created yet — run yagent setup)"：40 字符的真实 home 下必现，不是理论边界。
	// 路径可以被截断（尾部随时能在 `yagent profile path` 里查到），事实不能。
	//
	// 拆成"是什么 / 怎么修"两段，是为了窄终端能**整段丢掉**后半句，而不是把整条标注
	// 切成半截：规则与 common.HintBar 同源（放不下就从尾部整段丢弃，书写顺序 = 优先级）。
	// 英文与 CLI `runProfileList` 的文案逐字一致 —— 同一个事实在两处用两种说法，
	// 用户会以为是两件事；中文只在面板里出现（CLI 没有中文），结构刻意与英文同构。
	profileNoteNotCreated   = "not created yet"
	profileNoteSetupHint    = " — run yagent setup"
	profileNoteNotCreatedZh = "尚未创建"
	profileNoteSetupHintZh  = " — 运行 yagent setup"
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
	// 标注行的缩进对齐档名列：它属于上一行，不属于路径列 —— 窄终端会把路径整列舍弃，
	// 而"这个档的文件还没生成"这件事与路径列在不在无关。
	noteIndent := fixed - 2
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
	// 标注自身恒为次要样式：它描述的是一个事实，不该跟着游标变色去抢注意力。
	noteStyle := lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true)
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
			// 这一格里只有路径：把它单独省略，标注在下一行（见紧跟的 if）。
			row += "  " + pathStyle.Render(ellipsize(opt.Path, pathW))
		}
		lines = append(lines, row)

		if opt.NotCreated {
			// 标注**独占一行**，不参与路径的省略。路径与标注同格时，ellipsize 的中间省略
			// 会把 "not created yet" 切成 "…t created yet"（首尾各留一半），而这一行
			// 正是用户在面板里判断"要不要先跑 setup"的唯一依据。
			lines = append(lines, strings.Repeat(" ", noteIndent)+
				noteStyle.Render(d.notCreatedNote(innerWidth-noteIndent)))
		}
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

// notCreatedNote 返回"档位文件尚未创建"的标注：核心句恒完整，后半句（怎么修）在宽度
// 不够时**整段**让位 —— 半个提示比没有提示更糟（既读不懂也不可信）。
//
// 不变式（决定了这里不必再做任何截断）：调用方传入的 avail = innerWidth - noteIndent，
// 而 innerWidth ≥ 30（common.PanelWidths 的下限，dialogWidths 传的就是 30）、
// noteIndent = idxW+5 ≤ 7（档数 ≤ 99），所以 avail ≥ 23 > 核心句的 15 格。
func (d *ProfileSelectDialog) notCreatedNote(avail int) string {
	core, hint := profileNoteNotCreated, profileNoteSetupHint
	if d.isZh() {
		core, hint = profileNoteNotCreatedZh, profileNoteSetupHintZh
	}
	if lipgloss.Width(core+hint) <= avail {
		return core + hint
	}
	return core
}

// noteRowCount 是列表里额外需要一行来放"未创建"标注的条目数。
//
// 按全量条目算，不按可见区算：可见区依赖行预算、行预算又要减去标注行，按可见区算就成了
// 环（答案取决于它自己）。ProfileOption 的注释写明 NotCreated 只对 default 有实际含义，
// 所以这个数是 0 或 1 —— 多算一行的代价只是少显示一行列表，少算一行的后果是对话框
// 比终端高一行（提示换行撑破高度是同一类事故）。
func (d *ProfileSelectDialog) noteRowCount() int {
	n := 0
	for _, o := range d.options {
		if o.NotCreated {
			n++
		}
	}
	return n
}

// listRows 返回列表可占用的行数，以及是否渲染上下滚动指示行。
func (d *ProfileSelectDialog) listRows() (int, bool) {
	// 标注行是跟着条目走的额外行，**不是**固定 chrome：dialogListChrome 是三个选择类
	// 对话框共用的一份事实，为一个面板的条目级附加行去改它，等于把代价摊给另外两个。
	// 这里改成把终端高度按标注数垫低，再走同一套 ListBudget —— 抄一份"扣 chrome、
	// 留指示行"的逻辑会变成第二个会漂走的实现（根 AGENTS.md §11 坑 18）。
	return listBudget(d.height-d.noteRowCount(), len(d.options))
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
