package components

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"yagent/internal/tui/common"
)

// ModelOption 是模型选择器中的一行：provider 名 + 其配置的模型 + 上下文窗口。
type ModelOption struct {
	Provider string // 配置中的 provider 名，确认后经 Selected 回传
	Model    string // 该 provider 配置的模型名（可能带 org 前缀）
	Context  int    // 上下文窗口（token），0 = 未知
}

// ModelSelectDialog 是 provider 选择列表：↑/↓（或 j/k）移动，数字键 1-9 直选，
// Enter 确认，Esc 取消。条目超出可用高度时按游标滚动并显示被裁剪的条数。
type ModelSelectDialog struct {
	options     []ModelOption
	cursor      int
	width       int
	height      int
	Selected    string // 确认后为 provider 名；取消时为 ""
	CurrentProv string // 当前生效的 provider，用于标记与初始游标位置
}

const (
	// modelDialogBadgeMinWidth 渲染 ACTIVE 徽标列所需的最小内容宽度，
	// 低于此值时舍弃徽标列，避免 provider/model 名被截断到无法区分。
	modelDialogBadgeMinWidth = 60
	// activeBadgeText 当前生效 provider 的标记文本。
	activeBadgeText = "✓ ACTIVE"
)

// NewModelSelectDialog 创建选择器，游标初始落在当前生效的 provider 上
// （未找到则为首行），免去每次打开都要从头翻找。
func NewModelSelectDialog(options []ModelOption, currentProv string) *ModelSelectDialog {
	cursor := 0
	for i, o := range options {
		if o.Provider == currentProv {
			cursor = i
			break
		}
	}
	return &ModelSelectDialog{
		options:     options,
		cursor:      cursor,
		CurrentProv: currentProv,
	}
}

// ID returns the unique identifier for this dialog.
func (d *ModelSelectDialog) ID() string { return "model_select_dialog" }

// Type returns the dialog type.
func (d *ModelSelectDialog) Type() DialogType { return DialogModal }

// Init initializes the component.
func (d *ModelSelectDialog) Init() tea.Cmd { return nil }

// Update processes incoming messages.
func (d *ModelSelectDialog) Update(msg tea.Msg) (Component, tea.Cmd) {
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
	case "enter", " ":
		d.confirm()
	case "esc", "q", "Q":
		d.Selected = "" // cancelled
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
func (d *ModelSelectDialog) move(delta int) {
	n := len(d.options)
	if n == 0 || delta == 0 {
		return
	}
	d.cursor = ((d.cursor+delta)%n + n) % n
}

// pageStep 翻页步长，取可见行数的一半。
func (d *ModelSelectDialog) pageStep() int {
	rows, _ := d.listRows()
	step := rows / 2
	if step < 1 {
		step = 1
	}
	return step
}

// confirm 确认当前游标处的 provider。
func (d *ModelSelectDialog) confirm() {
	if d.cursor >= 0 && d.cursor < len(d.options) {
		d.Selected = d.options[d.cursor].Provider
	}
}

// selectByDigit 处理数字键直选：按键数字与列表行号一致（仅前 9 项）。
func (d *ModelSelectDialog) selectByDigit(key string) {
	if len(key) != 1 || key[0] < '1' || key[0] > '9' {
		return
	}
	idx := int(key[0] - '1')
	if idx >= len(d.options) || idx >= dialogQuickKeys {
		return
	}
	d.cursor = idx
	d.Selected = d.options[idx].Provider
}

// View renders the dialog as a string.
func (d *ModelSelectDialog) View() string {
	if d.width < 40 || d.height < 5 || len(d.options) == 0 {
		return ""
	}

	c := common.DarkModeColors()
	borderStyle := common.DialogBorderStyle(c)
	titleStyle := common.SectionHeaderStyle(c)
	helpStyle := common.HelpTextStyle(c)
	cursorStyle := common.CursorIndicatorStyle(c)
	badgeStyle := common.BadgeStyle(c, c.Success)

	dialogWidth, innerWidth := dialogWidths(d.width)

	rows, showMore := d.listRows()
	start, end := d.scrollWindow(rows)
	visible := d.options[start:end]

	// ── 列宽 ──
	// 用全部条目（而非仅可见条目）计算，滚动时列宽不会跳动。
	// 窄终端下舍弃 ACTIVE 徽标列，把宽度让给 provider/model（当前项仍以绿色高亮）。
	badgeW := 0
	if innerWidth >= modelDialogBadgeMinWidth {
		badgeW = lipgloss.Width(badgeStyle.Render(activeBadgeText))
	}
	idxW := len(strconv.Itoa(len(d.options)))
	ctxW, provNatural, modelNatural := lipgloss.Width(contextHeader), 0, 0
	for _, o := range d.options {
		ctxW = max(ctxW, lipgloss.Width(contextLabel(o.Context)))
		provNatural = max(provNatural, lipgloss.Width(o.Provider))
		modelNatural = max(modelNatural, lipgloss.Width(o.Model))
	}
	// 固定开销：游标 2 + 序号 + 空格 + 列间距 2 + 空格 + context + 空格 + badge
	fixed := 2 + idxW + 1 + 2 + 1 + ctxW
	if badgeW > 0 {
		fixed += 1 + badgeW
	}
	provW, modelW := allocateColumns(innerWidth-2-fixed, provNatural, modelNatural)

	// ── 表头 ──
	headerStyle := lipgloss.NewStyle().Foreground(c.TextMuted)
	header := strings.Repeat(" ", 2+idxW+1) +
		headerStyle.Render(ellipsize("PROVIDER", provW)) + "  " +
		headerStyle.Render(ellipsize("MODEL", modelW)) + " " +
		headerStyle.Render(padLeft(contextHeader, ctxW))

	// ── 列表 ──
	lines := make([]string, 0, len(visible)+2)
	lines = append(lines, header)
	for i, opt := range visible {
		index := start + i
		lines = append(lines, d.renderRow(index, opt, cursorStyle, badgeStyle, c, idxW, provW, modelW, ctxW, badgeW))
	}
	if showMore && start > 0 {
		lines = append(lines, moreLine(start, true, innerWidth, c))
	}
	if showMore && end < len(d.options) {
		lines = append(lines, moreLine(len(d.options)-end, false, innerWidth, c))
	}

	// ── 提示 ──
	// 逐级缩短，确保单行放得下：提示换行会撑破对话框高度预算。
	hint := dialogHint(innerWidth, len(d.options) > 1)

	dialogContent := lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render("Select Model Provider"),
		"",
		strings.Join(lines, "\n"),
		"",
		helpStyle.Render(hint),
	)

	dialog := borderStyle.Width(dialogWidth).Render(dialogContent)

	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, dialog)
}

// renderRow 渲染单行：游标 + 序号 + provider + model + context + ACTIVE 标记。
func (d *ModelSelectDialog) renderRow(
	index int, opt ModelOption,
	cursorStyle, badgeStyle lipgloss.Style, c common.ColorTokens,
	idxW, provW, modelW, ctxW, badgeW int,
) string {
	focused := index == d.cursor
	active := opt.Provider == d.CurrentProv

	marker := "  "
	if focused {
		marker = cursorStyle.Render("▶ ")
	}

	provColor := c.TextSecondary
	switch {
	case active:
		provColor = c.Success
	case focused:
		provColor = c.TextPrimary
	}
	provStyle := lipgloss.NewStyle().Foreground(provColor).Bold(focused || active)
	modelStyle := lipgloss.NewStyle().Foreground(c.TextMuted)
	if focused {
		modelStyle = modelStyle.Foreground(c.TextSecondary)
	}
	ctxColor := c.TextMuted
	if focused {
		ctxColor = c.PrimaryBright
	}
	idxStyle := lipgloss.NewStyle().Foreground(c.TextMuted)

	row := marker + idxStyle.Render(padLeft(strconv.Itoa(index+1), idxW)) + " " +
		provStyle.Render(ellipsize(opt.Provider, provW)) + "  " +
		modelStyle.Render(ellipsize(opt.Model, modelW)) + " " +
		lipgloss.NewStyle().Foreground(ctxColor).Render(padLeft(contextLabel(opt.Context), ctxW))

	if badgeW > 0 {
		if active {
			row += " " + badgeStyle.Render(activeBadgeText)
		} else {
			row += strings.Repeat(" ", badgeW+1)
		}
	}
	return row
}

// listRows 返回列表可占用的行数，以及是否渲染上下滚动指示行。
func (d *ModelSelectDialog) listRows() (int, bool) {
	return listBudget(d.height, len(d.options))
}

// scrollWindow 返回可见条目的 [start, end) 区间，游标尽量居中。
func (d *ModelSelectDialog) scrollWindow(rows int) (int, int) {
	return windowRange(len(d.options), d.cursor, rows)
}

// IsFocused reports whether this dialog has keyboard focus.
func (d *ModelSelectDialog) IsFocused() bool { return true }

// Focus sets the component as focused.
func (d *ModelSelectDialog) Focus() tea.Cmd { return nil }

// Blur removes focus from this component.
func (d *ModelSelectDialog) Blur() {}

// SetBounds sets the component's dimensions.
func (d *ModelSelectDialog) SetBounds(width, height int) {
	d.width = width
	d.height = height
}

// Bounds returns the component's current width and height.
func (d *ModelSelectDialog) Bounds() (int, int) { return d.width, d.height }

// IsVisible reports whether this component is currently visible.
func (d *ModelSelectDialog) IsVisible() bool { return true }

// SetVisible sets the visibility of this component.
func (d *ModelSelectDialog) SetVisible(v bool) {}
