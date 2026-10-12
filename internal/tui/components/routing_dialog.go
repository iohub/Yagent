package components

import (
	"fmt"
	"image/color"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"yagent/internal/tui/common"
)

// RoutingTierView 是自动路由某一档的展示数据（由 tui 层从 llm.RouteSnapshot 映射，
// 避免 components 包依赖 llm）。
type RoutingTierView struct {
	Label    string // "HIGH" / "BALANCED" / "FAST"
	Provider string
	Model    string
	Score    float64
}

// RoutingInfo 是渲染路由对话框所需的只读快照。
type RoutingInfo struct {
	Enabled bool   // 自动路由当前是否生效
	Mode    string // "auto" | "fixed"
	Reason  string // Enabled=false 时的原因
	Tiers   []RoutingTierView
}

// RoutingDialog 让用户在 AUTO / FIXED 两种路由模式间切换，并展示 auto 模式下
// 三档模型及其 ai_score。↑/↓ 选择模式，Enter 临时应用，Space 应用并写入配置，
// Esc 取消。禁用时改为展示禁用原因。
type RoutingDialog struct {
	info     RoutingInfo
	cursor   int // 0=AUTO, 1=FIXED
	width    int
	height   int
	Selected string // 确认后为 "auto"/"fixed"；取消时为 ""
	Persist  bool   // Space 确认为 true（写入 config.toml）
}

var routingModes = []struct {
	value string
	label string
	desc  string
}{
	{"auto", "AUTO", "Route each sub-task by LLM-judged difficulty"},
	{"fixed", "FIXED", "Use each agent's configured provider (no routing)"},
}

// NewRoutingDialog 创建对话框，游标初始落在当前模式上。
func NewRoutingDialog(info RoutingInfo) *RoutingDialog {
	cursor := 0
	for i, m := range routingModes {
		if strings.EqualFold(m.value, info.Mode) {
			cursor = i
			break
		}
	}
	return &RoutingDialog{info: info, cursor: cursor}
}

func (d *RoutingDialog) ID() string         { return "routing_dialog" }
func (d *RoutingDialog) Type() DialogType   { return DialogModal }
func (d *RoutingDialog) Init() tea.Cmd      { return nil }
func (d *RoutingDialog) IsFocused() bool    { return true }
func (d *RoutingDialog) Focus() tea.Cmd     { return nil }
func (d *RoutingDialog) Blur()              {}
func (d *RoutingDialog) IsVisible() bool    { return true }
func (d *RoutingDialog) SetVisible(bool)    {}
func (d *RoutingDialog) Bounds() (int, int) { return d.width, d.height }
func (d *RoutingDialog) SetBounds(w, h int) { d.width = w; d.height = h }

func (d *RoutingDialog) Update(msg tea.Msg) (Component, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}
	switch key := keyMsg.String(); key {
	case "up", "k", "ctrl+p":
		d.cursor = (d.cursor + len(routingModes) - 1) % len(routingModes)
	case "down", "j", "ctrl+n", "tab":
		d.cursor = (d.cursor + 1) % len(routingModes)
	case "1":
		d.cursor = 0
	case "2":
		d.cursor = 1
	case "enter":
		d.Selected = routingModes[d.cursor].value
		d.Persist = false
	case " ", "space":
		d.Selected = routingModes[d.cursor].value
		d.Persist = true
	case "esc", "q", "Q":
		d.Selected = ""
		d.Persist = false
	case "ctrl+c":
		return d, tea.Quit
	}
	return d, nil
}

func (d *RoutingDialog) View() string {
	if d.width < 40 || d.height < 8 {
		return ""
	}

	c := common.DarkModeColors()
	borderStyle := common.DialogBorderStyle(c)
	titleStyle := common.SectionHeaderStyle(c)
	helpStyle := common.HelpTextStyle(c)
	cursorStyle := common.CursorIndicatorStyle(c)

	dialogWidth, innerWidth := dialogWidths(d.width)

	// ── 模式选择 ──
	modeHeader := lipgloss.NewStyle().Foreground(c.TextMuted).Bold(true).Render("ROUTING MODE")
	modeLines := []string{modeHeader}
	badgeText := "✓ CURRENT"
	badgeW := lipgloss.Width(common.BadgeStyle(c, c.Success).Render(badgeText)) + 1
	for i, m := range routingModes {
		focused := i == d.cursor
		active := strings.EqualFold(m.value, d.info.Mode)

		marker := "  "
		if focused {
			marker = cursorStyle.Render("▶ ")
		}
		labelColor := c.TextSecondary
		switch {
		case active:
			labelColor = c.Success
		case focused:
			labelColor = c.TextPrimary
		}
		label := lipgloss.NewStyle().Foreground(labelColor).Bold(focused || active).
			Render(fmt.Sprintf("%-6s", m.label))
		// 描述预算：内宽 − 游标(2) − 标签(6) − 间隔(1) − 徽标（仅当前项）。
		descBudget := innerWidth - 2 - 6 - 1
		if active {
			descBudget -= badgeW
		}
		desc := lipgloss.NewStyle().Foreground(c.TextMuted).Render(ellipsize(m.desc, max(descBudget, 4)))
		row := marker + label + " " + desc
		if active {
			row += " " + common.BadgeStyle(c, c.Success).Render(badgeText)
		}
		modeLines = append(modeLines, row)
	}

	// ── 三档信息 ──
	tierLines := d.renderTiers(c, innerWidth)

	hint := "↑/↓ select · Enter apply · Space save · Esc cancel"
	if lipgloss.Width(hint) > innerWidth {
		hint = "↑/↓ · Enter · Space · Esc"
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render("Model Auto-Routing"),
		"",
		strings.Join(modeLines, "\n"),
		"",
		strings.Join(tierLines, "\n"),
		"",
		helpStyle.Render(hint),
	)
	dialog := borderStyle.Width(dialogWidth).Render(content)
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, dialog)
}

// renderTiers 渲染三档模型 + 分数；未启用时渲染禁用原因。所有行都收敛到 innerWidth。
func (d *RoutingDialog) renderTiers(c common.ColorTokens, innerWidth int) []string {
	headerStyle := lipgloss.NewStyle().Foreground(c.TextMuted).Bold(true)

	if !d.info.Enabled {
		reason := d.info.Reason
		if reason == "" {
			reason = "auto-routing unavailable"
		}
		return []string{
			headerStyle.Render("AUTO-ROUTE TIERS"),
			lipgloss.NewStyle().Foreground(c.Warning).Render("⚠ " + ellipsize(reason, max(innerWidth-2, 8))),
			lipgloss.NewStyle().Foreground(c.TextMuted).Render(ellipsize("Falling back to each agent's configured provider.", innerWidth)),
		}
	}

	lines := []string{headerStyle.Render("AUTO-ROUTE TIERS")}
	tierColor := func(label string) color.Color {
		switch strings.ToUpper(label) {
		case "HIGH":
			return lipgloss.Color("214")
		case "BALANCED":
			return lipgloss.Color("114")
		case "FAST":
			return lipgloss.Color("111")
		default:
			return c.TextMuted
		}
	}
	// 每行固定开销：缩进 2 + 标签 9 + 分数 8 + 两个间隔 2 = 21，其余分给 model / provider。
	nameBudget := max(innerWidth-21, 8)
	modelW := max(nameBudget*62/100, 4)
	provW := max(nameBudget-modelW-1, 4)
	for _, t := range d.info.Tiers {
		label := lipgloss.NewStyle().Foreground(tierColor(t.Label)).Bold(true).Render(fmt.Sprintf("%-9s", t.Label))
		model := lipgloss.NewStyle().Foreground(c.TextPrimary).Render(ellipsize(t.Model, modelW))
		prov := lipgloss.NewStyle().Foreground(c.TextMuted).Render(ellipsize("("+t.Provider+")", provW))
		score := lipgloss.NewStyle().Foreground(c.PrimaryBright).Bold(true).
			Render(fmt.Sprintf("%8.1f", t.Score))
		lines = append(lines, "  "+label+model+" "+prov+" "+score)
	}
	lines = append(lines, lipgloss.NewStyle().Foreground(c.TextMuted).
		Render(ellipsize("ai_score = Σ(benchmark × weight) on the candidate intersection", innerWidth)))
	return lines
}
