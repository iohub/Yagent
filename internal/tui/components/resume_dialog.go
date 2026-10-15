package components

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"yagent/internal/tui/common"
)

// ResumeDialog 断点续跑确认框：展示中断原因、步数、未完成计划项与悬空工具调用，
// Enter 续跑 / Esc 仅浏览。由 /resume 恢复出中断会话后弹出。
type ResumeDialog struct {
	DialogID      string
	Reason        string   // 中断原因（stop_reason 分类或 turn_aborted 文本）
	StepInfo      string   // "step 30/30"，无步数信息时为空
	PlanItems     []string // 未完成计划项（[ ] 复选框行，已截断）
	DanglingTools []string // 悬空工具调用名（去重）
	// AgentOption 非空 = 有一个"被停住的委派"可以接着做完，于是多出一个选项：
	// 从子 Agent 的那一步继续（B/C 路径），而不是从 Director 重来。
	// 空 = 这份会话没有可恢复的落点（存量 rollout、停在委派之外、或子文件没同步过来），
	// 选项退回原来的两个 —— 摆一个点了也没用的选项比不摆更糟。
	AgentOption   string
	SelectedIndex int
	Confirmed     bool
	width         int
	height        int
	lang          Language
}

// ResumeChoice 用户最终选的那一条。
type ResumeChoice int

const (
	ResumeChoiceBrowse ResumeChoice = iota
	ResumeChoiceDirector
	ResumeChoiceAgent
)

// options 返回当前可选的按钮标签，顺序即 Tab 的循环顺序，索引即 SelectedIndex。
// "继续"的两档排在"仅浏览"之前：Confirmed 的判据就是"选的不是最后一档"。
func (d *ResumeDialog) options() []string {
	opts := make([]string, 0, 3)
	if d.AgentOption != "" {
		opts = append(opts, d.AgentOption)
	}
	opts = append(opts, getResumeText("ResumeBtnGo", d.lang), getResumeText("ResumeBtnBrowse", d.lang))
	return opts
}

// Choice 把 SelectedIndex 翻成语义。用序号判断会把"仅浏览"和"从 Director 继续"
// 搞混 —— 加了第三个选项之后，索引与含义的关系变了，而两者都是"点了个按钮"。
func (d *ResumeDialog) Choice() ResumeChoice {
	opts := d.options()
	idx := d.SelectedIndex
	if idx < 0 || idx >= len(opts) {
		return ResumeChoiceBrowse
	}
	last := len(opts) - 1
	if idx == last {
		return ResumeChoiceBrowse
	}
	if d.AgentOption != "" && idx == 0 {
		return ResumeChoiceAgent
	}
	return ResumeChoiceDirector
}

// NewResumeDialog 创建断点续跑确认框。
func NewResumeDialog(id, reason, stepInfo string, planItems, danglingTools []string, lang Language) *ResumeDialog {
	return &ResumeDialog{
		DialogID:      id,
		Reason:        reason,
		StepInfo:      stepInfo,
		PlanItems:     planItems,
		DanglingTools: danglingTools,
		SelectedIndex: 0,
		lang:          lang,
	}
}

// SetAgentOption 挂上"从子 Agent 继续"这一档（label 由调用方按语言拼好）。
func (d *ResumeDialog) SetAgentOption(label string) { d.AgentOption = label }

func (d *ResumeDialog) ID() string         { return d.DialogID }
func (d *ResumeDialog) Type() DialogType   { return DialogModal }
func (d *ResumeDialog) Init() tea.Cmd      { return nil }
func (d *ResumeDialog) IsFocused() bool    { return true }
func (d *ResumeDialog) Focus() tea.Cmd     { return nil }
func (d *ResumeDialog) Blur()              {}
func (d *ResumeDialog) SetBounds(w, h int) { d.width = w; d.height = h }
func (d *ResumeDialog) Bounds() (int, int) { return d.width, d.height }
func (d *ResumeDialog) IsVisible() bool    { return true }
func (d *ResumeDialog) SetVisible(bool)    {}

// GetConfirmed 返回用户选择：true=续跑。
func (d *ResumeDialog) GetConfirmed() bool { return d.Confirmed }

// SetConfirmed 设置选择结果。
func (d *ResumeDialog) SetConfirmed(confirmed bool) { d.Confirmed = confirmed }

// Update 处理按键：左右/Tab 切换按钮，Enter 按 SelectedIndex 确认，y 续跑，
// n/Esc 仅浏览。与 QuitConfirmDialog 的交互语义一致。
func (d *ResumeDialog) Update(msg tea.Msg) (Component, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}

	switch keyMsg.String() {
	case "right", "tab", "left":
		n := len(d.options())
		d.SelectedIndex = (d.SelectedIndex + 1) % n
		return d, nil
	case "enter":
		d.setSelected()
		return d, nil
	case "y", "Y":
		d.SelectedIndex = 0
		d.setSelected()
		return d, nil
	case "n", "N", "esc":
		d.SelectedIndex = len(d.options()) - 1
		d.Confirmed = false
		return d, nil
	case "ctrl+c":
		return d, tea.Quit
	}
	return d, nil
}

// setSelected 按当前高亮项落定 Confirmed：最后一档是"仅浏览"，其余都是"继续"。
func (d *ResumeDialog) setSelected() {
	d.Confirmed = d.SelectedIndex < len(d.options())-1
	if !d.Confirmed {
		d.SelectedIndex = len(d.options()) - 1
	}
}

// View 渲染对话框。
func (d *ResumeDialog) View() string {
	if d.width < 40 || d.height < 8 {
		return ""
	}

	const maxDialogWidth = 72
	dialogWidth := maxDialogWidth
	if d.width-4 < dialogWidth {
		dialogWidth = d.width - 4
	}
	innerWidth := dialogWidth - 6
	if innerWidth < 24 {
		innerWidth = 24
	}

	c := common.DarkModeColors()
	borderStyle := common.DialogBorderStyle(c)
	titleStyle := common.SectionHeaderStyle(c)
	helpStyle := common.HelpTextStyle(c)

	// ── 标题 ──
	titleLine := titleStyle.Render(getResumeText("ResumeTitle", d.lang))

	// ── 详情块 ──
	detailStyle := lipgloss.NewStyle().Foreground(c.TextPrimary)
	warnStyle := lipgloss.NewStyle().Foreground(c.Warning)
	mutedStyle := lipgloss.NewStyle().Foreground(c.TextMuted)

	var lines []string
	reasonLine := warnStyle.Render(
		fmt.Sprintf("%s %s", getResumeText("ResumeReason", d.lang), d.Reason))
	if d.StepInfo != "" {
		reasonLine += detailStyle.Render("  " + d.StepInfo)
	}
	lines = append(lines, reasonLine)

	if len(d.PlanItems) > 0 {
		lines = append(lines, "", mutedStyle.Render(getResumeText("ResumePlanHeader", d.lang)))
		for _, item := range d.PlanItems {
			lines = append(lines, detailStyle.Render("  "+item))
		}
	}

	if len(d.DanglingTools) > 0 {
		lines = append(lines, "",
			mutedStyle.Render(getResumeText("ResumeDanglingHeader", d.lang)),
			detailStyle.Render("  "+strings.Join(d.DanglingTools, ", ")))
	}

	// ── 按钮 ──
	focusedBtn := lipgloss.NewStyle().
		Foreground(lipgloss.Color("0")).
		Background(c.Accent).
		Bold(true).Padding(0, 2)
	blurredBtn := lipgloss.NewStyle().Foreground(c.TextMuted).Padding(0, 2)
	renderBtn := func(label string, idx int) string {
		if d.SelectedIndex == idx {
			return focusedBtn.Render(label)
		}
		return blurredBtn.Render(label)
	}
	opts := d.options()
	labels := make([]string, 0, 2*len(opts)-1)
	for i, label := range opts {
		if i > 0 {
			labels = append(labels, "  ")
		}
		labels = append(labels, renderBtn(label, i))
	}
	buttons := lipgloss.JoinHorizontal(lipgloss.Center, labels...)

	// ── 组装 ──
	content := lipgloss.JoinVertical(lipgloss.Left,
		append([]string{titleLine, ""},
			append(lines,
				"",
				lipgloss.NewStyle().Width(innerWidth).Align(lipgloss.Center).Render(buttons),
				"",
				helpStyle.Render(getResumeText("ResumeHelp", d.lang)))...)...)

	dialog := borderStyle.Width(dialogWidth).Render(content)

	return lipgloss.Place(d.width, d.height,
		lipgloss.Center, lipgloss.Center, dialog)
}

// getResumeText 续跑对话框的多语言文案。
func getResumeText(key string, lang Language) string {
	zh := lang == LanguageZh
	switch key {
	case "ResumeTitle":
		if zh {
			return "断点续跑"
		}
		return "Resume from Breakpoint"
	case "ResumeReason":
		if zh {
			return "中断原因:"
		}
		return "Interrupted:"
	case "ResumePlanHeader":
		if zh {
			return "未完成计划项:"
		}
		return "Unfinished plan items:"
	case "ResumeDanglingHeader":
		if zh {
			return "悬空工具调用（结果未知，续跑前需核实效果）:"
		}
		return "Dangling tool calls (results unknown, verify before continuing):"
	case "ResumeBtnGo":
		if zh {
			return "续跑"
		}
		return "Resume"
	case "ResumeBtnBrowse":
		if zh {
			return "仅浏览"
		}
		return "Browse Only"
	case "ResumeHelp":
		if zh {
			return "←/→ 切换 · Enter 确认 · Esc 仅浏览"
		}
		return "←/→ select  ·  Enter confirm  ·  Esc browse only"
	default:
		return key
	}
}
