package components

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"yagent/internal/tui/common"
)

// 信息类对话框（/skills、/mcp）：只读列表 + 滚动，不选中任何东西。
// 与 HelpDialog 同族（圆角边框 + 标题 + 渐变分隔线 + 弱化提示），
// 但列表几何一律走 common/list_metrics.go —— 不许在这里再写一份。
//
// infoDialogChrome 是除列表外占用的行数：边框 2 + 上下内边距 2 + 标题 1 +
// 分隔线 1 + 表头 1 + 提示前空行 1 + 提示 1。"每多/少一行非列表内容"都要同步改它，
// 否则列表会撑破终端高度（internal/tui/AGENTS.md 硬约束 7）。
const infoDialogChrome = 9

// infoDialogBase 是两个信息对话框的共用体。
type infoDialogBase struct {
	id     string
	title  string
	header []string
	rows   [][]string
	// rowColors[i] 是第 i 行的强调色（nil = 全部次要色）。
	rowColors []lipgloss.Style
	// empty 是 rows 为空时逐行显示的说明（"为什么没有 + 怎么修"）。
	empty   []string
	hint    string
	width   int
	height  int
	offset  int
	visible bool
	tokens  common.ColorTokens
}

func newInfoDialogBase(id, title string, header []string, hint string) infoDialogBase {
	return infoDialogBase{
		id:      id,
		title:   title,
		header:  header,
		hint:    hint,
		visible: true,
		tokens:  common.DarkModeColors(),
	}
}

// View 渲染。尺寸过小时返回空串（与 model_select_dialog.go 的守卫一致）。
func (d *infoDialogBase) View() string {
	if d.width < 40 || d.height < 5 {
		return ""
	}

	_, contentWidth := common.PanelWidths(88, 30, d.width)

	needed := len(d.rows)
	if needed == 0 {
		needed = len(d.empty)
	}
	rows, showMore := common.ListBudget(infoDialogChrome, dialogMaxListRows, d.height, needed)

	total := len(d.rows)
	if total == 0 {
		total = len(d.empty)
	}
	start, end := common.WindowRange(total, d.offset, rows)
	if d.offset > total-1 {
		d.offset = max(total-1, 0)
		start, end = common.WindowRange(total, d.offset, rows)
	}

	c := d.tokens
	titleStyle := lipgloss.NewStyle().Foreground(c.PrimaryBright).Bold(true).
		Width(contentWidth).Align(lipgloss.Center)
	headerStyle := lipgloss.NewStyle().Foreground(c.TextMuted).Bold(true)
	hintStyle := lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true).
		Width(contentWidth).Align(lipgloss.Center)
	bodyStyle := lipgloss.NewStyle().Foreground(c.TextSecondary)

	lines := []string{
		titleStyle.Render(d.title),
		common.ApplyGrad(strings.Repeat("─", contentWidth)),
	}

	if len(d.header) > 0 {
		widths := infoColumnWidths(d.header, d.rows, contentWidth)
		lines = append(lines, renderInfoCells(d.header, widths, headerStyle))
		if len(d.rows) == 0 {
			for _, line := range d.empty[start:end] {
				lines = append(lines, bodyStyle.Render(common.Ellipsize(line, contentWidth)))
			}
		} else {
			for i := start; i < end; i++ {
				style := bodyStyle
				if i < len(d.rowColors) {
					style = d.rowColors[i]
				}
				lines = append(lines, renderInfoCells(d.rows[i], widths, style))
			}
		}
		if showMore {
			switch {
			case start > 0:
				lines = append(lines, moreLine(start, true, contentWidth, c))
			default:
				lines = append(lines, moreLine(total-end, false, contentWidth, c))
			}
		}
	} else {
		// 无表头时（MCP 空态）逐行渲染说明。
		for _, line := range d.empty[start:end] {
			lines = append(lines, bodyStyle.Render(common.Ellipsize(line, contentWidth)))
		}
	}

	lines = append(lines, "", hintStyle.Render(d.hint))

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(c.Border).
		Padding(1, 2).
		Render(lipgloss.JoinVertical(lipgloss.Left, lines...))

	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, box)
}

// infoColumnWidths 在可用宽度内分配列宽：放得下就按自然宽度，放不下就削最宽的那列。
func infoColumnWidths(header []string, rows [][]string, avail int) []int {
	natural := make([]int, len(header))
	for i, h := range header {
		natural[i] = lipgloss.Width(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(natural) {
				if w := lipgloss.Width(cell); w > natural[i] {
					natural[i] = w
				}
			}
		}
	}
	gaps := max(len(header)-1, 0) * 2
	sum := gaps
	for _, w := range natural {
		sum += w
	}
	for sum > avail {
		// 每次削最宽的一列；都到 4 之后放弃（宁可整行截断，也不让宽度算法死循环）。
		widest, idx := 0, -1
		for i, w := range natural {
			if w > widest {
				widest, idx = w, i
			}
		}
		if idx < 0 || natural[idx] <= 4 {
			break
		}
		natural[idx]--
		sum--
	}
	return natural
}

// renderInfoCells 把一行的各列按宽度渲染成一行（列间两个空格）。
func renderInfoCells(cells []string, widths []int, style lipgloss.Style) string {
	parts := make([]string, 0, len(widths))
	for i, w := range widths {
		cell := ""
		if i < len(cells) {
			cell = common.Ellipsize(cells[i], w)
		} else {
			cell = strings.Repeat(" ", w)
		}
		parts = append(parts, style.Render(cell))
	}
	return strings.Join(parts, "  ")
}

// Update 只处理滚动键；关闭由 model 决定（与 HelpDialog 一致）。
func (d *infoDialogBase) Update(msg tea.Msg) (Component, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}
	switch keyMsg.String() {
	case "up", "k":
		d.offset = max(d.offset-1, 0)
	case "down", "j":
		d.offset = min(d.offset+1, max(len(d.rows)-1, 0))
	case "pgup", "ctrl+u":
		d.offset = max(d.offset-10, 0)
	case "pgdown", "ctrl+d":
		d.offset = min(d.offset+10, max(len(d.rows)-1, 0))
	case "home", "g":
		d.offset = 0
	case "end", "G":
		d.offset = max(len(d.rows)-1, 0)
	}
	return d, nil
}

func (d *infoDialogBase) Init() tea.Cmd      { return nil }
func (d *infoDialogBase) IsFocused() bool    { return true }
func (d *infoDialogBase) Focus() tea.Cmd     { return nil }
func (d *infoDialogBase) Blur()              {}
func (d *infoDialogBase) SetBounds(w, h int) { d.width, d.height = w, h }
func (d *infoDialogBase) Bounds() (int, int) { return d.width, d.height }
func (d *infoDialogBase) IsVisible() bool    { return d.visible }
func (d *infoDialogBase) SetVisible(v bool)  { d.visible = v }
func (d *infoDialogBase) ID() string         { return d.id }
func (d *infoDialogBase) Type() DialogType   { return DialogModal }

// ── /skills ──

// SkillRow 是 /skills 面板的一行。
type SkillRow struct {
	Name        string
	Description string
	Source      string
	FrontMatter string
}

// SkillsDialog 列出已加载的技能、来源与 front-matter 档位。
type SkillsDialog struct {
	infoDialogBase
}

func NewSkillsDialog(rows []SkillRow) *SkillsDialog {
	d := &SkillsDialog{infoDialogBase: newInfoDialogBase(
		"skills_dialog", "⌘  Skills", []string{"NAME", "DESCRIPTION", "SOURCE", "FRONT-MATTER"},
		"↑/↓ scroll · Esc close",
	)}
	for _, r := range rows {
		d.rows = append(d.rows, []string{r.Name, r.Description, r.Source, r.FrontMatter})
	}
	if len(rows) == 0 {
		d.empty = []string{
			"no skills found",
			"put them in .yagent/skills/<name>/SKILL.md or ~/.yagent/skills/<name>/SKILL.md",
		}
	}
	return d
}

// ── /mcp ──

// MCPServerRow 是 /mcp 面板的一行。
type MCPServerRow struct {
	Name    string
	State   string
	Detail  string
	Tools   int
	Curated int
}

// MCPDialog 列出每个 MCP server 的状态账（与 system prompt 的状态行同源）。
type MCPDialog struct {
	infoDialogBase
}

func NewMCPDialog(rows []MCPServerRow, statusLines string) *MCPDialog {
	d := &MCPDialog{infoDialogBase: newInfoDialogBase(
		"mcp_dialog", "⚙  MCP servers", []string{"SERVER", "STATE", "TOOLS", "CURATED", "DETAIL"},
		"↑/↓ scroll · Esc close",
	)}
	tokens := common.DarkModeColors()
	stateStyle := func(state string) lipgloss.Style {
		style := lipgloss.NewStyle().Foreground(tokens.TextSecondary)
		switch state {
		case "ready":
			style = style.Foreground(tokens.Success)
		case "degraded":
			style = style.Foreground(tokens.Warning)
		case "offline":
			style = style.Foreground(tokens.Danger)
		default:
			style = style.Foreground(tokens.TextMuted)
		}
		return style
	}
	for _, r := range rows {
		d.rows = append(d.rows, []string{
			r.Name, r.State, strconv.Itoa(r.Tools), strconv.Itoa(r.Curated), r.Detail,
		})
		d.rowColors = append(d.rowColors, stateStyle(r.State))
	}
	if len(rows) == 0 {
		d.empty = []string{"no MCP servers configured"}
		if strings.TrimSpace(statusLines) != "" {
			d.empty = strings.Split(statusLines, "\n")
		}
	}
	return d
}
