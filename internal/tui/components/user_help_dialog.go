package components

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"codeactor/internal/protocol"
	"codeactor/internal/tui/common"
)

// ── 内部状态 ──

// userHelpState 表示 UserHelpDialog 的内部焦点状态
type userHelpState int

const (
	stateConfirmSelect userHelpState = iota // Confirm: 在 Yes/No 按钮间切换
	stateSelectList                         // Select: 在选项列表中导航
	stateSelectCustom                       // Select: 自定义输入框激活
	stateInputText                          // Input: 文本输入框聚焦
	stateInputButtons                       // Input: 在 Submit/Cancel 按钮间切换
)

// ── 选项项 ──

type helpOptionItem struct {
	Label    string
	IsCustom bool // 是否为 "Custom input..." 项
}

// ── UserHelpDialog ──

// UserHelpDialog 是 ask_user_for_help 工具的交互对话框。
// 它支持三种交互模式，由内部状态机统一管理。
type UserHelpDialog struct {
	// 元数据
	id          string
	data        protocol.UserHelpNeededData
	interaction protocol.InteractionType
	state       userHelpState

	// Confirm 模式状态
	confirmIndex int // 0=第一个选项, 1=第二个选项

	// Select 模式状态
	selectIndex int
	options     []helpOptionItem

	// Input 模式状态
	textInput   textarea.Model
	buttonIndex int // 0=Submit, 1=Cancel

	// 布局
	width  int
	height int

	// 样式
	colors common.ColorTokens

	// 结果
	result *protocol.UserHelpResponseData
	closed bool
}

// NewUserHelpDialog 创建一个新的用户帮助对话框。
func NewUserHelpDialog(data protocol.UserHelpNeededData) *UserHelpDialog {
	d := &UserHelpDialog{
		id:          fmt.Sprintf("user_help_%s", data.RequestID),
		data:        data,
		interaction: data.InteractionType,
		colors:      common.DarkModeColors(),
	}

	// 如果 interaction_type 为空，自动推断
	if d.interaction == "" {
		d.interaction = protocol.InferInteractionType(data.Options)
	}

	// 构建选项列表
	d.options = make([]helpOptionItem, 0, len(data.Options)+1)
	for _, opt := range data.Options {
		d.options = append(d.options, helpOptionItem{Label: opt})
	}
	// Select 模式下追加 Custom 选项
	if d.interaction == protocol.InteractionSelect && data.AllowCustom {
		d.options = append(d.options, helpOptionItem{
			Label:    "Custom input...",
			IsCustom: true,
		})
	}

	// 初始化文本输入
	d.textInput = textarea.New()
	d.textInput.Placeholder = data.Placeholder
	if data.Placeholder == "" {
		d.textInput.Placeholder = "Type your answer here..."
	}
	if data.DefaultValue != "" {
		d.textInput.SetValue(data.DefaultValue)
	}
	d.textInput.SetWidth(40)
	d.textInput.SetHeight(3)
	d.textInput.CharLimit = 2000
	d.textInput.ShowLineNumbers = false

	// 初始化状态
	switch d.interaction {
	case protocol.InteractionConfirm:
		d.state = stateConfirmSelect
		d.confirmIndex = 0
		if data.DefaultValue != "" {
			for i, opt := range data.Options {
				if strings.EqualFold(opt, data.DefaultValue) {
					d.confirmIndex = i
					break
				}
			}
		}
	case protocol.InteractionSelect:
		d.state = stateSelectList
		d.selectIndex = 0
		if data.DefaultValue != "" {
			for i, opt := range data.Options {
				if strings.EqualFold(opt, data.DefaultValue) {
					d.selectIndex = i
					break
				}
			}
		}
	case protocol.InteractionInput:
		d.state = stateInputText
	}

	return d
}

// ── Dialog 接口实现 ──

func (d *UserHelpDialog) ID() string { return d.id }

func (d *UserHelpDialog) Type() DialogType { return DialogModal }

func (d *UserHelpDialog) Init() tea.Cmd {
	if d.state == stateInputText || d.state == stateSelectCustom {
		d.textInput.Focus()
		return textarea.Blink
	}
	return nil
}

func (d *UserHelpDialog) Update(msg tea.Msg) (Component, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return d.handleKey(msg)
	case tea.WindowSizeMsg:
		d.width = msg.Width
		d.height = msg.Height
		return d, nil
	}

	// 透传消息给子组件（textarea）
	if d.state == stateInputText || d.state == stateSelectCustom {
		var cmd tea.Cmd
		d.textInput, cmd = d.textInput.Update(msg)
		return d, cmd
	}

	return d, nil
}

func (d *UserHelpDialog) View() string {
	if d.width < 40 || d.height < 8 {
		return ""
	}

	// 尺寸计算
	const maxDialogWidth = 72
	dialogWidth := maxDialogWidth
	if d.width-4 < dialogWidth {
		dialogWidth = d.width - 4
	}
	innerWidth := dialogWidth - 6
	if innerWidth < 24 {
		innerWidth = 24
	}

	c := d.colors

	// ── 标题 ──
	titleStyle := common.SectionHeaderStyle(c)
	rawTitle := "❓ " + getHelpTitle(d.interaction)
	if lipgloss.Width(rawTitle) > innerWidth-4 {
		runes := []rune(rawTitle)
		if len(runes) > innerWidth-6 {
			rawTitle = string(runes[:innerWidth-6]) + "…"
		}
	}
	titleLine := titleStyle.Render(rawTitle)

	// ── 问题 ──
	questionStyle := lipgloss.NewStyle().Foreground(c.TextPrimary).Bold(true)
	questionLine := questionStyle.Render(d.data.Question)

	// ── 上下文 ──
	var contextLine string
	if d.data.Context != "" {
		ctxStr := d.data.Context
		if lipgloss.Width(ctxStr) > innerWidth-2 {
			runes := []rune(ctxStr)
			if len(runes) > innerWidth-4 {
				ctxStr = string(runes[:innerWidth-4]) + "…"
			}
		}
		contextLine = lipgloss.NewStyle().
			Foreground(c.TextSecondary).
			Render("ℹ " + ctxStr)
	}

	// ── 交互区域 ──
	var body string
	switch d.interaction {
	case protocol.InteractionConfirm:
		body = d.renderConfirm(innerWidth, c)
	case protocol.InteractionSelect:
		body = d.renderSelect(innerWidth, c)
	case protocol.InteractionInput:
		body = d.renderInput(innerWidth, c)
	}

	// ── 帮助提示 ──
	helpStyle := common.HelpTextStyle(c)
	helpLine := helpStyle.Render(getHelpHint(d.interaction, d.state))

	// ── 组装 ──
	var parts []string
	parts = append(parts, titleLine)
	parts = append(parts, "")
	parts = append(parts, questionLine)
	if contextLine != "" {
		parts = append(parts, "")
		parts = append(parts, contextLine)
	}
	parts = append(parts, "")
	parts = append(parts, body)
	parts = append(parts, "")
	parts = append(parts, helpLine)

	content := lipgloss.JoinVertical(lipgloss.Left, parts...)
	borderStyle := common.DialogBorderStyle(c)
	dialog := borderStyle.Width(dialogWidth).Render(content)

	return lipgloss.Place(d.width, d.height,
		lipgloss.Center, lipgloss.Center,
		dialog,
	)
}

// ── Component 接口实现 ──

func (d *UserHelpDialog) IsFocused() bool { return true }

func (d *UserHelpDialog) Focus() tea.Cmd {
	if d.state == stateInputText || d.state == stateSelectCustom {
		d.textInput.Focus()
		return textarea.Blink
	}
	return nil
}

func (d *UserHelpDialog) Blur() {
	if d.state == stateInputText || d.state == stateSelectCustom {
		d.textInput.Blur()
	}
}

func (d *UserHelpDialog) SetBounds(w, h int) { d.width = w; d.height = h }

func (d *UserHelpDialog) Bounds() (int, int) { return d.width, d.height }

func (d *UserHelpDialog) IsVisible() bool { return true }

func (d *UserHelpDialog) SetVisible(bool) {}

// ── 新增: 关闭状态和结果 ──

// IsClosed 返回对话框是否已关闭（用户已提交或取消）
func (d *UserHelpDialog) IsClosed() bool { return d.closed }

// Result 返回用户的响应结果
func (d *UserHelpDialog) Result() *protocol.UserHelpResponseData { return d.result }

// ── 键盘事件处理 ──

func (d *UserHelpDialog) handleKey(msg tea.KeyMsg) (Component, tea.Cmd) {
	switch d.state {
	case stateConfirmSelect:
		return d.handleConfirmKey(msg)
	case stateSelectList:
		return d.handleSelectListKey(msg)
	case stateSelectCustom:
		return d.handleSelectCustomKey(msg)
	case stateInputText:
		return d.handleInputTextKey(msg)
	case stateInputButtons:
		return d.handleInputButtonsKey(msg)
	}
	return d, nil
}

func (d *UserHelpDialog) handleConfirmKey(msg tea.KeyMsg) (Component, tea.Cmd) {
	options := d.data.Options
	if len(options) < 2 {
		options = []string{"yes", "no"}
	}

	switch msg.String() {
	case "left", "h", "shift+tab":
		d.confirmIndex = (d.confirmIndex - 1 + 2) % 2
	case "right", "l", "tab":
		d.confirmIndex = (d.confirmIndex + 1) % 2
	case "y", "Y":
		d.confirmIndex = 0
		d.submitConfirm(options)
	case "n", "N":
		d.confirmIndex = 1
		d.submitConfirm(options)
	case "1":
		d.confirmIndex = 0
		d.submitConfirm(options)
	case "2":
		d.confirmIndex = 1
		d.submitConfirm(options)
	case "enter":
		d.submitConfirm(options)
	case "esc":
		d.cancel()
	}
	return d, nil
}

func (d *UserHelpDialog) handleSelectListKey(msg tea.KeyMsg) (Component, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if d.selectIndex > 0 {
			d.selectIndex--
		}
	case "down", "j":
		if d.selectIndex < len(d.options)-1 {
			d.selectIndex++
		}
	case "1", "2", "3", "4", "5", "6", "7", "8", "9":
		idx := int(msg.String()[0] - '1')
		if idx < len(d.options) {
			d.selectIndex = idx
			if d.options[idx].IsCustom {
				d.state = stateSelectCustom
				d.textInput.Focus()
				return d, textarea.Blink
			}
			d.submitSelect()
		}
	case "tab":
		if d.options[d.selectIndex].IsCustom {
			d.state = stateSelectCustom
			d.textInput.Focus()
			return d, textarea.Blink
		}
	case "enter":
		if d.options[d.selectIndex].IsCustom {
			d.state = stateSelectCustom
			d.textInput.Focus()
			return d, textarea.Blink
		}
		d.submitSelect()
	case "esc":
		d.cancel()
	}
	return d, nil
}

func (d *UserHelpDialog) handleSelectCustomKey(msg tea.KeyMsg) (Component, tea.Cmd) {
	switch msg.String() {
	case "esc":
		d.state = stateSelectList
		d.textInput.Blur()
		return d, nil
	case "enter":
		value := d.textInput.Value()
		if strings.TrimSpace(value) != "" {
			d.submitCustomSelect(value)
		}
		return d, nil
	case "ctrl+u":
		d.textInput.SetValue("")
		return d, nil
	}

	// 其他按键交给 textarea 处理
	var cmd tea.Cmd
	d.textInput, cmd = d.textInput.Update(msg)
	return d, cmd
}

func (d *UserHelpDialog) handleInputTextKey(msg tea.KeyMsg) (Component, tea.Cmd) {
	switch msg.String() {
	case "esc":
		d.cancel()
		return d, nil
	case "ctrl+u":
		d.textInput.SetValue("")
		return d, nil
	case "tab":
		d.state = stateInputButtons
		d.textInput.Blur()
		d.buttonIndex = 0
		return d, nil
	case "enter":
		value := d.textInput.Value()
		d.submitInput(value)
		return d, nil
	}

	// 其他按键交给 textarea
	var cmd tea.Cmd
	d.textInput, cmd = d.textInput.Update(msg)
	return d, cmd
}

func (d *UserHelpDialog) handleInputButtonsKey(msg tea.KeyMsg) (Component, tea.Cmd) {
	switch msg.String() {
	case "left", "h":
		if d.buttonIndex > 0 {
			d.buttonIndex--
		}
	case "right", "l":
		if d.buttonIndex < 1 {
			d.buttonIndex++
		}
	case "tab":
		d.buttonIndex = (d.buttonIndex + 1) % 2
	case "enter":
		if d.buttonIndex == 0 {
			d.submitInput(d.textInput.Value())
		} else {
			d.cancel()
		}
	case "esc":
		d.cancel()
	case "up", "k":
		d.state = stateInputText
		d.textInput.Focus()
		return d, textarea.Blink
	}
	return d, nil
}

// ── 提交与取消 ──

func (d *UserHelpDialog) submitConfirm(options []string) {
	selected := options[d.confirmIndex]
	d.result = &protocol.UserHelpResponseData{
		Response:        selected,
		InteractionType: d.interaction,
		RequestID:       d.data.RequestID,
	}
	d.closed = true
}

func (d *UserHelpDialog) submitSelect() {
	selected := d.options[d.selectIndex]
	d.result = &protocol.UserHelpResponseData{
		Response:        selected.Label,
		InteractionType: d.interaction,
		IsCustom:        selected.IsCustom,
		RequestID:       d.data.RequestID,
	}
	d.closed = true
}

func (d *UserHelpDialog) submitCustomSelect(value string) {
	d.result = &protocol.UserHelpResponseData{
		Response:        value,
		InteractionType: d.interaction,
		IsCustom:        true,
		RequestID:       d.data.RequestID,
	}
	d.closed = true
}

func (d *UserHelpDialog) submitInput(value string) {
	d.result = &protocol.UserHelpResponseData{
		Response:        value,
		InteractionType: d.interaction,
		IsCustom:        true,
		RequestID:       d.data.RequestID,
	}
	d.closed = true
}

func (d *UserHelpDialog) cancel() {
	d.result = &protocol.UserHelpResponseData{
		Response:        "",
		InteractionType: d.interaction,
		Cancelled:       true,
		RequestID:       d.data.RequestID,
	}
	d.closed = true
}

// ── 渲染方法 ──

func (d *UserHelpDialog) renderConfirm(innerWidth int, c common.ColorTokens) string {
	options := d.data.Options
	if len(options) < 2 {
		options = []string{"yes", "no"}
	}

	var buttons []string
	for i, label := range options {
		style := lipgloss.NewStyle().
			Foreground(c.TextSecondary).
			Background(c.Surface).
			Padding(0, 3).
			Border(lipgloss.RoundedBorder()).
			BorderForeground(c.Border)

		if i == d.confirmIndex {
			style = common.FocusedButtonStyle(c, common.SafetyLow).
				Padding(0, 3)
		}
		buttons = append(buttons, style.Render(label))
	}

	return lipgloss.JoinHorizontal(lipgloss.Center, buttons...)
}

func (d *UserHelpDialog) renderSelect(innerWidth int, c common.ColorTokens) string {
	var items []string
	for i, opt := range d.options {
		cursor := "  "
		labelStyle := lipgloss.NewStyle().Foreground(c.TextPrimary)

		if i == d.selectIndex && d.state == stateSelectList {
			cursor = "▸ "
			labelStyle = lipgloss.NewStyle().
				Foreground(c.Primary).
				Bold(true)
		}

		label := opt.Label
		if opt.IsCustom {
			label = lipgloss.NewStyle().
				Foreground(c.Accent).
				Italic(true).
				Render(opt.Label)
		}

		// 截断过长选项
		fullText := cursor + label
		if lipgloss.Width(fullText) > innerWidth-2 {
			runes := []rune(fullText)
			if len(runes) > innerWidth-4 {
				fullText = string(runes[:innerWidth-4]) + "…"
			}
		}

		itemLine := labelStyle.Render(fullText)

		// 如果在自定义输入状态且当前项是 Custom，渲染输入框
		if opt.IsCustom && i == d.selectIndex && d.state == stateSelectCustom {
			inputBox := lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(c.Primary).
				Padding(0, 1).
				Width(innerWidth - 4).
				Render(d.textInput.View())
			itemLine = lipgloss.JoinVertical(lipgloss.Left, itemLine, "", inputBox)
		}

		items = append(items, itemLine)
	}

	return lipgloss.JoinVertical(lipgloss.Left, items...)
}

func (d *UserHelpDialog) renderInput(innerWidth int, c common.ColorTokens) string {
	var parts []string

	// 文本输入框
	inputWidth := innerWidth - 6
	if inputWidth < 20 {
		inputWidth = 20
	}
	d.textInput.SetWidth(inputWidth)

	inputBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(c.BorderActive).
		Padding(0, 1).
		Width(inputWidth + 2).
		Render(d.textInput.View())
	parts = append(parts, inputBox)

	// 按钮
	var buttons []string
	submitStyle := lipgloss.NewStyle().
		Foreground(c.TextSecondary).
		Background(c.Surface).
		Padding(0, 2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(c.Border)

	cancelStyle := lipgloss.NewStyle().
		Foreground(c.TextSecondary).
		Background(c.Surface).
		Padding(0, 2).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(c.Border)

	if d.state == stateInputButtons {
		if d.buttonIndex == 0 {
			submitStyle = common.FocusedButtonStyle(c, common.SafetyLow).
				Padding(0, 2)
		} else {
			cancelStyle = common.FocusedButtonStyle(c, common.SafetySafe).
				Padding(0, 2)
		}
	}

	buttons = append(buttons, submitStyle.Render(" Submit "))
	buttons = append(buttons, cancelStyle.Render(" Cancel "))
	parts = append(parts, lipgloss.JoinHorizontal(lipgloss.Center, buttons...))

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// ── 辅助函数 ──

func getHelpTitle(it protocol.InteractionType) string {
	switch it {
	case protocol.InteractionConfirm:
		return "Ask for Help"
	case protocol.InteractionSelect:
		return "Ask for Help"
	case protocol.InteractionInput:
		return "Ask for Help"
	default:
		return "Ask for Help"
	}
}

func getHelpHint(it protocol.InteractionType, state userHelpState) string {
	switch it {
	case protocol.InteractionConfirm:
		return "← → Navigate  ·  Enter Confirm  ·  y/n Quick Select  ·  Esc Cancel"
	case protocol.InteractionSelect:
		if state == stateSelectCustom {
			return "Enter Submit  ·  Esc Back to List  ·  Ctrl+U Clear"
		}
		return "↑ ↓ Navigate  ·  Enter Select  ·  1-9 Quick Select  ·  Esc Cancel"
	case protocol.InteractionInput:
		if state == stateInputButtons {
			return "← → Navigate  ·  Enter Confirm  ·  ↑ Back to Input  ·  Esc Cancel"
		}
		return "Enter Submit  ·  Tab to Buttons  ·  Ctrl+U Clear  ·  Esc Cancel"
	default:
		return ""
	}
}
