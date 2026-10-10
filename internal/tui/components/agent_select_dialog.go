package components

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"yagent/internal/tui/common"
)

// 目标分组标签，由调用方按语义填写，对话框统一大写展示。
const (
	TargetGroupGlobal = "global"
	TargetGroupAgents = "agents"
	TargetGroupTools  = "tools"
)

// ConfigEntry 是目标选择器中的一行：global / 某个 agent / 某个 tool，
// 连同它当前生效的 provider、模型与上下文窗口。
type ConfigEntry struct {
	Target   string // "global"、agent 名（如 "director"）或 tool 名（如 "deepthinking"）
	Group    string // 分组标签，见 TargetGroup*；相邻同组条目只渲染一次组标题
	Provider string // 当前生效的 provider，"" = 未能解析
	Model    string // 当前生效的模型名
	Context  int    // 上下文窗口（token），0 = 未知
}

// AgentSelectDialog 是"给哪个目标换模型"的选择列表：按分组展示各目标当前生效的
// provider/model/context，↑/↓（或 j/k）移动，数字键 1-9 直选，Enter 确认，Esc 取消。
// 条目与组标题超出可用高度时按游标滚动并显示被裁剪的行数。
type AgentSelectDialog struct {
	entries  []ConfigEntry
	cursor   int
	width    int
	height   int
	Selected string // 确认后为 entries[cursor].Target；取消时为 ""
}

const (
	// targetHeader 目标列的表头，同时作为该列的最小宽度。
	targetHeader = "TARGET"
	// providerHeader 与 modelHeader 分别是 provider / model 列的表头与最小宽度。
	providerHeader = "PROVIDER"
	modelHeader    = "MODEL"
)

// NewAgentSelectDialog 创建选择器，游标落在首行（global）。
func NewAgentSelectDialog(entries []ConfigEntry) *AgentSelectDialog {
	return &AgentSelectDialog{entries: entries}
}

// ID returns the unique identifier for this dialog.
func (d *AgentSelectDialog) ID() string { return "agent_select_dialog" }

// Type returns the dialog type.
func (d *AgentSelectDialog) Type() DialogType { return DialogModal }

// Init initializes the component. No setup needed.
func (d *AgentSelectDialog) Init() tea.Cmd { return nil }

// Update processes incoming messages and returns the updated component.
func (d *AgentSelectDialog) Update(msg tea.Msg) (Component, tea.Cmd) {
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
		d.cursor = len(d.entries) - 1
	case "enter", " ":
		d.confirm()
	case "esc", "q", "Q":
		d.Selected = "" // cancelled
	case "ctrl+c":
		return d, tea.Quit
	default:
		d.selectByDigit(key)
	}
	d.cursor = max(min(d.cursor, len(d.entries)-1), 0)
	return d, nil
}

// move 按 delta 移动游标，首尾相接。组标题不可聚焦，故直接按条目下标移动。
func (d *AgentSelectDialog) move(delta int) {
	n := len(d.entries)
	if n == 0 || delta == 0 {
		return
	}
	d.cursor = ((d.cursor+delta)%n + n) % n
}

// pageStep 翻页步长，取可见行数的一半。
func (d *AgentSelectDialog) pageStep() int {
	rows, _ := listBudget(d.height, len(d.buildRows()))
	return max(rows/2, 1)
}

// confirm 确认当前游标处的目标。
func (d *AgentSelectDialog) confirm() {
	if d.cursor >= 0 && d.cursor < len(d.entries) {
		d.Selected = d.entries[d.cursor].Target
	}
}

// selectByDigit 处理数字键直选：按键数字与条目序号一致（仅前 9 项）。
func (d *AgentSelectDialog) selectByDigit(key string) {
	if len(key) != 1 || key[0] < '1' || key[0] > '9' {
		return
	}
	idx := int(key[0] - '1')
	if idx >= len(d.entries) || idx >= dialogQuickKeys {
		return
	}
	d.cursor = idx
	d.Selected = d.entries[idx].Target
}

// View renders the dialog with the shared list-dialog layout.
func (d *AgentSelectDialog) View() string {
	if d.width < 40 || d.height < 5 || len(d.entries) == 0 {
		return ""
	}

	c := common.DarkModeColors()
	borderStyle := common.DialogBorderStyle(c)
	titleStyle := common.SectionHeaderStyle(c)
	helpStyle := common.HelpTextStyle(c)
	cursorStyle := common.CursorIndicatorStyle(c)

	dialogWidth, innerWidth := dialogWidths(d.width)

	// ── 行序列与滚动窗口 ──
	// 组标题与条目同处一个序列里参与行数预算，否则标题行会把对话框顶出终端。
	allRows := d.buildRows()
	rows, showMore := listBudget(d.height, len(allRows))
	start, end := windowRange(len(allRows), d.focusRow(allRows), rows)

	// ── 列宽 ──
	// 用全部条目（而非仅可见条目）计算，滚动时列宽不会跳动。
	idxW := len(strconv.Itoa(len(d.entries)))
	ctxW := lipgloss.Width(contextHeader)
	targetW := lipgloss.Width(targetHeader)
	provNatural, modelNatural := lipgloss.Width(providerHeader), lipgloss.Width(modelHeader)
	for _, e := range d.entries {
		ctxW = max(ctxW, lipgloss.Width(contextLabel(e.Context)))
		targetW = max(targetW, lipgloss.Width(e.Target))
		provNatural = max(provNatural, lipgloss.Width(unsetLabel(e.Provider)))
		modelNatural = max(modelNatural, lipgloss.Width(unsetLabel(e.Model)))
	}
	// 固定开销：游标 2 + 序号 + 空格 + 三处列间距 (2+2+1) + context
	fixed := 2 + idxW + 1 + 2 + 2 + 1 + ctxW
	flex := innerWidth - fixed
	// TARGET 列取自然宽度，但最多占三分之一，保证 provider/model 仍可辨识
	targetW = min(targetW, max(flex/3, 1))
	provW, modelW := allocateColumns(flex-targetW, provNatural, modelNatural)

	// ── 表头 ──
	headerStyle := lipgloss.NewStyle().Foreground(c.TextMuted)
	gutter := 2 + idxW + 1
	header := strings.Repeat(" ", gutter) +
		headerStyle.Render(ellipsize(targetHeader, targetW)) + "  " +
		headerStyle.Render(ellipsize(providerHeader, provW)) + "  " +
		headerStyle.Render(ellipsize(modelHeader, modelW)) + " " +
		headerStyle.Render(padLeft(contextHeader, ctxW))

	// ── 列表 ──
	lines := make([]string, 0, len(allRows)+2)
	lines = append(lines, header)
	for _, r := range allRows[start:end] {
		if r.header != "" {
			lines = append(lines, groupHeaderLine(r.header, innerWidth, c))
			continue
		}
		lines = append(lines, d.renderRow(r.entry, cursorStyle, c, idxW, targetW, provW, modelW, ctxW))
	}
	if showMore && start > 0 {
		lines = append(lines, moreLine(start, true, innerWidth, c))
	}
	if showMore && end < len(allRows) {
		lines = append(lines, moreLine(len(allRows)-end, false, innerWidth, c))
	}

	dialogContent := lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render("Select Target to Change Model"),
		"",
		strings.Join(lines, "\n"),
		"",
		helpStyle.Render(dialogHint(innerWidth, len(d.entries) > 1)),
	)

	dialog := borderStyle.Width(dialogWidth).Render(dialogContent)

	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, dialog)
}

// agentRow 是列表中的一行：分组标题，或一个条目。
type agentRow struct {
	header string // 非空表示分组标题行，此时 entry 无意义
	entry  int    // header 为空时为 d.entries 的下标
}

// buildRows 把条目按 Group 变化切分成"组标题 + 条目"的扁平行序列。
// 组标题与条目同处一个序列，才能一起参与行数预算与滚动窗口计算。
func (d *AgentSelectDialog) buildRows() []agentRow {
	rows := make([]agentRow, 0, len(d.entries)+3)
	for i, e := range d.entries {
		if (i == 0 || d.entries[i-1].Group != e.Group) && e.Group != "" {
			rows = append(rows, agentRow{header: e.Group})
		}
		rows = append(rows, agentRow{entry: i})
	}
	return rows
}

// focusRow 返回游标条目在行序列中的位置，用于滚动时让它保持可见。
func (d *AgentSelectDialog) focusRow(rows []agentRow) int {
	for i, r := range rows {
		if r.header == "" && r.entry == d.cursor {
			return i
		}
	}
	return 0
}

// renderRow 渲染单行：游标 + 序号 + 目标 + provider + model + context。
func (d *AgentSelectDialog) renderRow(
	index int, cursorStyle lipgloss.Style, c common.ColorTokens,
	idxW, targetW, provW, modelW, ctxW int,
) string {
	e := d.entries[index]
	focused := index == d.cursor

	marker := "  "
	targetColor, detailColor, ctxColor := c.TextSecondary, c.TextMuted, c.TextMuted
	if focused {
		marker = cursorStyle.Render("▶ ")
		targetColor, detailColor, ctxColor = c.TextPrimary, c.TextSecondary, c.PrimaryBright
	}

	return marker +
		lipgloss.NewStyle().Foreground(c.TextMuted).Render(padLeft(strconv.Itoa(index+1), idxW)) + " " +
		lipgloss.NewStyle().Foreground(targetColor).Bold(focused).Render(ellipsize(e.Target, targetW)) + "  " +
		lipgloss.NewStyle().Foreground(detailColor).Render(ellipsize(unsetLabel(e.Provider), provW)) + "  " +
		lipgloss.NewStyle().Foreground(detailColor).Render(ellipsize(unsetLabel(e.Model), modelW)) + " " +
		lipgloss.NewStyle().Foreground(ctxColor).Render(padLeft(contextLabel(e.Context), ctxW))
}

// groupHeaderLine 渲染分组标题行：大写字母 + 填满剩余宽度的细分隔线。
// 从内容区左边缘起排（不缩进到 TARGET 列），否则会被读成该列的一个取值。
func groupHeaderLine(label string, innerWidth int, c common.ColorTokens) string {
	text := strings.ToUpper(label)
	line := lipgloss.NewStyle().Foreground(c.Primary).Bold(true).Render(text)
	if rule := innerWidth - lipgloss.Width(text) - 1; rule > 0 {
		line += " " + lipgloss.NewStyle().Foreground(c.Border).Render(strings.Repeat("─", rule))
	}
	return line
}

// unsetLabel 把未解析出的 provider/model 显示为占位符，保证列不塌成空白。
func unsetLabel(s string) string {
	if s == "" || s == "unknown" {
		return "-"
	}
	return s
}

// IsFocused reports whether this dialog has keyboard focus.
func (d *AgentSelectDialog) IsFocused() bool { return true }

// Focus sets the component as focused.
func (d *AgentSelectDialog) Focus() tea.Cmd { return nil }

// Blur removes focus from this component.
func (d *AgentSelectDialog) Blur() {}

// SetBounds sets the component's dimensions.
func (d *AgentSelectDialog) SetBounds(w, h int) { d.width = w; d.height = h }

// Bounds returns the component's current width and height.
func (d *AgentSelectDialog) Bounds() (int, int) { return d.width, d.height }

// IsVisible reports whether this component is currently visible.
func (d *AgentSelectDialog) IsVisible() bool { return true }

// SetVisible sets the visibility of this component.
func (d *AgentSelectDialog) SetVisible(bool) {}
