package components

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"yagent/internal/tui/common"
)

// TimeoutOption 是超时对话框的一个预设选项。
type TimeoutOption struct {
	Value string // 规范 duration 字符串（如 "10m"），TUI 层用 time.ParseDuration 解析
	Desc  string // 说明（如 "Default"）
}

// TimeoutInfo 是渲染超时对话框所需的只读快照（由 tui 层从 config 全局值映射，
// 避免 components 包依赖 config）。
type TimeoutInfo struct {
	CurrentLabel string          // 当前值展示文本（config.FormatLLMTimeout 生成）
	CurrentRaw   string          // 当前值规范字符串（用于游标定位与徽标匹配）
	Options      []TimeoutOption // 预设选项
}

// LLMTimeoutDialog 让用户查看当前 LLM 调用超时并在预设间选择。
// ↑/↓ 选择预设，Enter 临时应用（仅运行时），Space 应用并写入配置，
// Esc 取消。
type LLMTimeoutDialog struct {
	info     TimeoutInfo
	cursor   int // Options 下标
	width    int
	height   int
	Selected string // 确认后为所选 Value；取消时为 ""
	Persist  bool   // Space 确认为 true（写入 config.toml）
}

// NewLLMTimeoutDialog 创建对话框，游标初始落在与当前值匹配的选项上（找不到则 0）。
func NewLLMTimeoutDialog(info TimeoutInfo) *LLMTimeoutDialog {
	cursor := 0
	for i, o := range info.Options {
		if o.Value == info.CurrentRaw {
			cursor = i
			break
		}
	}
	return &LLMTimeoutDialog{info: info, cursor: cursor}
}

func (d *LLMTimeoutDialog) ID() string         { return "timeout_dialog" }
func (d *LLMTimeoutDialog) Type() DialogType   { return DialogModal }
func (d *LLMTimeoutDialog) Init() tea.Cmd      { return nil }
func (d *LLMTimeoutDialog) IsFocused() bool    { return true }
func (d *LLMTimeoutDialog) Focus() tea.Cmd     { return nil }
func (d *LLMTimeoutDialog) Blur()              {}
func (d *LLMTimeoutDialog) IsVisible() bool    { return true }
func (d *LLMTimeoutDialog) SetVisible(bool)    {}
func (d *LLMTimeoutDialog) Bounds() (int, int) { return d.width, d.height }
func (d *LLMTimeoutDialog) SetBounds(w, h int) { d.width = w; d.height = h }

func (d *LLMTimeoutDialog) Update(msg tea.Msg) (Component, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}
	key := keyMsg.String()

	// 空选项：只响应取消/退出，避免取模除零。
	if len(d.info.Options) == 0 {
		switch key {
		case "esc", "q", "Q":
			d.Selected = ""
			d.Persist = false
		case "ctrl+c":
			return d, tea.Quit
		}
		return d, nil
	}

	switch key {
	case "up", "k", "ctrl+p":
		d.cursor = (d.cursor + len(d.info.Options) - 1) % len(d.info.Options)
	case "down", "j", "ctrl+n", "tab":
		d.cursor = (d.cursor + 1) % len(d.info.Options)
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		if idx := int(key[0] - '1'); idx < len(d.info.Options) {
			d.cursor = idx
		}
	case "enter":
		d.Selected = d.info.Options[d.cursor].Value
		d.Persist = false
	case " ", "space":
		d.Selected = d.info.Options[d.cursor].Value
		d.Persist = true
	case "esc", "q", "Q":
		d.Selected = ""
		d.Persist = false
	case "ctrl+c":
		return d, tea.Quit
	}
	return d, nil
}

func (d *LLMTimeoutDialog) View() string {
	if d.width < 40 || d.height < 8 {
		return ""
	}

	c := common.DarkModeColors()
	borderStyle := common.DialogBorderStyle(c)
	titleStyle := common.SectionHeaderStyle(c)
	helpStyle := common.HelpTextStyle(c)
	cursorStyle := common.CursorIndicatorStyle(c)

	dialogWidth, innerWidth := dialogWidths(d.width)

	// ── 预设列表 ──
	header := lipgloss.NewStyle().Foreground(c.TextMuted).Bold(true).Render("TIMEOUT PRESET")
	currentLine := lipgloss.NewStyle().Foreground(c.Success).Bold(true).
		Render("CURRENT: " + d.info.CurrentLabel)

	valueW := 0
	for _, o := range d.info.Options {
		if w := lipgloss.Width(o.Value); w > valueW {
			valueW = w
		}
	}
	badgeText := "✓ CURRENT"
	badgeW := lipgloss.Width(common.BadgeStyle(c, c.Success).Render(badgeText)) + 1

	lines := []string{header, currentLine, ""}
	for i, o := range d.info.Options {
		focused := i == d.cursor
		active := o.Value == d.info.CurrentRaw

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
			Render(fmt.Sprintf("%-*s", valueW, o.Value))
		// 描述预算：内宽 − 游标(2) − 值列(valueW) − 间隔(1) − 徽标（仅当前项）。
		descBudget := innerWidth - 2 - valueW - 1
		if active {
			descBudget -= badgeW
		}
		desc := lipgloss.NewStyle().Foreground(c.TextMuted).Render(ellipsize(o.Desc, max(descBudget, 4)))
		row := marker + label + " " + desc
		if active {
			row += " " + common.BadgeStyle(c, c.Success).Render(badgeText)
		}
		lines = append(lines, row)
	}

	hint := "↑/↓ select · Enter apply · Space save · Esc cancel"
	if lipgloss.Width(hint) > innerWidth {
		hint = "↑/↓ · Enter · Space · Esc"
	}

	content := lipgloss.JoinVertical(lipgloss.Left,
		titleStyle.Render("LLM Call Timeout"),
		"",
		strings.Join(lines, "\n"),
		"",
		helpStyle.Render(hint),
	)
	dialog := borderStyle.Width(dialogWidth).Render(content)
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, dialog)
}
