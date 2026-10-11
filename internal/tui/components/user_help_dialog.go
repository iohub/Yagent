package components

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"yagent/internal/protocol"
	"yagent/internal/tui/common"
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

// UserHelpDialog is the interactive dialog for the ask_user_for_help tool.
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
	buttonIndex int    // 0=Submit, 1=Cancel
	inputError  string // Input模式空值校验错误提示

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
	default:
		// Only forward specific message types to textInput.
		// PasteMsg 是 bracketed-paste 的独立消息类型，必须转发，否则无法粘贴。
		if d.state == stateInputText || d.state == stateSelectCustom {
			switch msg.(type) {
			case tea.MouseMsg, tea.FocusMsg, tea.BlurMsg, tea.PasteMsg:
				var cmd tea.Cmd
				d.textInput, cmd = d.textInput.Update(msg)
				return d, cmd
			}
		}
		return d, nil
	}
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
	rawTitle := getHelpTitle(d.interaction)
	if lipgloss.Width(rawTitle) > innerWidth-4 {
		runes := []rune(rawTitle)
		if len(runes) > innerWidth-6 {
			rawTitle = string(runes[:innerWidth-6]) + "…"
		}
	}
	titleLine := titleStyle.Render(rawTitle)

	// ── 问题（按显示宽度折行，长问题不再溢出截断；CJK 友好） ──
	questionStyle := lipgloss.NewStyle().Foreground(c.TextPrimary).Bold(true)
	questionLine := questionStyle.Render(
		strings.Join(hardWrapWidth(d.data.Question, innerWidth), "\n"))

	// ── 上下文（折行至多 3 行，超出以省略号收尾） ──
	var contextLine string
	if d.data.Context != "" {
		// InfoBox 内容宽 = Width(innerWidth-4) − 边框(2) − padding(2)
		ctxWrapWidth := innerWidth - 8 - 2 // 预留 "ℹ " 前缀 / 续行缩进
		if ctxWrapWidth < 10 {
			ctxWrapWidth = 10
		}
		ctxLines := hardWrapWidth(d.data.Context, ctxWrapWidth)
		const maxCtxLines = 3
		if len(ctxLines) > maxCtxLines {
			ctxLines = ctxLines[:maxCtxLines]
			ctxLines[maxCtxLines-1] = truncateToWidth(ctxLines[maxCtxLines-1], ctxWrapWidth)
		}
		ctxStr := "ℹ " + strings.Join(ctxLines, "\n  ")
		// 用 InfoBoxStyle 包裹上下文文本
		infoBox := common.InfoBoxStyle(c).
			Width(innerWidth - 4).
			Render(ctxStr)
		contextLine = infoBox
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
	case "left", "h", "up", "k", "shift+tab":
		d.confirmIndex = (d.confirmIndex - 1 + 2) % 2
	case "right", "l", "down", "j", "tab":
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
		if strings.TrimSpace(value) == "" {
			d.inputError = "⚠ Input cannot be empty. Type a value or press Esc to cancel."
			return d, nil
		}
		d.inputError = ""
		d.submitInput(value)
		return d, nil
	}

	// 其他按键交给 textarea 处理（清除错误提示）
	d.inputError = ""
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

	// 纵向 radio 行列表，与 ConfirmDialog（授权对话框）同款布局：
	// ◉ label                             key
	var optionLines []string
	for i, label := range options {
		// 首个选项视为"执行"（warning 色），其余视为"拒绝"（success 色）
		level := common.SafetySafe
		if i == 0 {
			level = common.SafetyLow
		}
		key := fmt.Sprintf("%d", i+1)
		labelText := truncateToWidth(label, innerWidth-6)

		var radio, labelSeg, keySeg string
		if i == d.confirmIndex {
			radio = common.RadioFocusedStyle(c, level).Render("◉")
			labelSeg = common.OptionLabelFocusedStyle(c).Render(labelText)
			keySeg = common.OptionKeyFocusedStyle(c, level).Render(key)
		} else {
			radio = common.RadioUnfocusedStyle(c).Render("○")
			labelSeg = common.OptionLabelUnfocusedStyle(c).Render(labelText)
			keySeg = common.OptionKeyUnfocusedStyle(c).Render(key)
		}

		lineWidth := lipgloss.Width(radio) + 1 + lipgloss.Width(labelText) + 1 + lipgloss.Width(key)
		padding := innerWidth - lineWidth
		if padding < 0 {
			padding = 0
		}
		optionLines = append(optionLines, radio+" "+labelSeg+strings.Repeat(" ", padding)+" "+keySeg)
	}
	body := lipgloss.JoinVertical(lipgloss.Left, optionLines...)

	// 底部添加帮助提示行（MaxWidth 兜底，防止窄对话框下被 lipgloss 折行）
	helpLine := lipgloss.NewStyle().MaxWidth(innerWidth).
		Render(renderHelpHint(d.interaction, d.state, c))
	return lipgloss.JoinVertical(lipgloss.Left, body, "", helpLine)
}

func (d *UserHelpDialog) renderSelect(innerWidth int, c common.ColorTokens) string {
	var items []string
	for i, opt := range d.options {
		selected := i == d.selectIndex
		inListMode := selected && d.state == stateSelectList
		inCustomMode := selected && d.state == stateSelectCustom

		// 截断过长选项：radio(2) + label + pad + key(2)
		keyHint := selectKeyHint(i)
		labelText := truncateToWidth(opt.Label, innerWidth-6)

		level := common.SafetyLow
		var radio, labelSeg, keySeg string
		if inListMode || inCustomMode {
			radio = common.RadioFocusedStyle(c, level).Render("◉")
			keySeg = common.OptionKeyFocusedStyle(c, level).Render(keyHint)
			if opt.IsCustom {
				labelSeg = lipgloss.NewStyle().
					Foreground(c.Accent).Bold(true).Italic(true).
					Render(labelText)
			} else {
				labelSeg = common.OptionLabelFocusedStyle(c).Render(labelText)
			}
		} else {
			radio = common.RadioUnfocusedStyle(c).Render("○")
			keySeg = common.OptionKeyUnfocusedStyle(c).Render(keyHint)
			if opt.IsCustom {
				labelSeg = lipgloss.NewStyle().
					Foreground(c.Accent).Italic(true).
					Render(labelText)
			} else {
				labelSeg = common.OptionLabelUnfocusedStyle(c).Render(labelText)
			}
		}

		lineWidth := lipgloss.Width(radio) + 1 + lipgloss.Width(labelText) + 1 + lipgloss.Width(keyHint)
		padding := innerWidth - lineWidth
		if padding < 0 {
			padding = 0
		}
		itemLine := radio + " " + labelSeg + strings.Repeat(" ", padding) + " " + keySeg

		// 如果在自定义输入状态且当前项是 Custom，渲染输入框
		if opt.IsCustom && inCustomMode {
			inputBox := lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(c.BorderActive).
				Padding(0, 1).
				Width(innerWidth - 4).
				Render(d.textInput.View())
			itemLine = lipgloss.JoinVertical(lipgloss.Left, itemLine, "", inputBox)
		}

		items = append(items, itemLine)
	}

	// 底部添加帮助提示行（MaxWidth 兜底，防止窄对话框下被 lipgloss 折行）
	helpLine := lipgloss.NewStyle().MaxWidth(innerWidth).
		Render(renderHelpHint(d.interaction, d.state, c))

	body := lipgloss.JoinVertical(lipgloss.Left, items...)
	return lipgloss.JoinVertical(lipgloss.Left, body, "", helpLine)
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

	// 错误提示（空值校验失败时显示）
	if d.inputError != "" {
		parts = append(parts, common.ErrorTextStyle(c).Render(d.inputError))
	}

	// 按钮：聚焦态使用家族 FocusedButtonStyle 色块，非聚焦态暗面底
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

	// 只有在 stateInputButtons 时才高亮，否则两个按钮均为未选中态
	if d.state == stateInputButtons {
		if d.buttonIndex == 0 {
			submitStyle = common.FocusedButtonStyle(c, common.SafetyLow)
		} else {
			cancelStyle = common.FocusedButtonStyle(c, common.SafetySafe)
		}
	}

	buttons = append(buttons, submitStyle.Render("Submit"))
	buttons = append(buttons, " ")
	buttons = append(buttons, cancelStyle.Render("Cancel"))
	parts = append(parts, lipgloss.JoinHorizontal(lipgloss.Center, buttons...))

	// 底部添加帮助提示行（MaxWidth 兜底，防止窄对话框下被 lipgloss 折行）
	helpLine := lipgloss.NewStyle().MaxWidth(innerWidth).
		Render(renderHelpHint(d.interaction, d.state, c))

	body := lipgloss.JoinVertical(lipgloss.Left, parts...)
	return lipgloss.JoinVertical(lipgloss.Left, body, "", helpLine)
}

// ── 辅助函数 ──

func getHelpTitle(it protocol.InteractionType) string {
	switch it {
	case protocol.InteractionConfirm:
		return "◆ Confirm"
	case protocol.InteractionSelect:
		return "◆ Choose"
	case protocol.InteractionInput:
		return "◆ Input"
	default:
		return "◆ Ask"
	}
}

// selectKeyHint 返回选项右侧对齐的快捷键提示（1-9 数字，10-35 字母 a-z，36+ 为空）。
func selectKeyHint(i int) string {
	if i < 9 {
		return fmt.Sprintf("%d", i+1)
	}
	if i < 35 {
		return fmt.Sprintf("%c", 'a'+i-9)
	}
	return ""
}

// truncateToWidth 按显示宽度截断字符串，超出部分以 "…" 结尾。
// 与 hardWrapWidth 配套：兜底防止溢出。
func truncateToWidth(s string, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxW {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && lipgloss.Width(string(r))+1 > maxW {
		r = r[:len(r)-1]
	}
	return string(r) + "…"
}

// hardWrapWidth 按显示宽度硬折行（CJK 友好：wrapText 按空格分词，
// 对无空格中文文本无效）。保留原有换行，空行原样输出。
func hardWrapWidth(s string, width int) []string {
	if width < 4 {
		width = 4
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if para == "" {
			out = append(out, "")
			continue
		}
		var line strings.Builder
		lineW := 0
		for _, r := range para {
			rw := lipgloss.Width(string(r))
			if lineW+rw > width && lineW > 0 {
				out = append(out, line.String())
				line.Reset()
				lineW = 0
			}
			line.WriteRune(r)
			lineW += rw
		}
		out = append(out, line.String())
	}
	return out
}

// renderHelpHint 使用 KeyHintStyle 渲染键帽提示行。
func renderHelpHint(it protocol.InteractionType, state userHelpState, c common.ColorTokens) string {
	hints := getHintParts(it, state)
	if len(hints) == 0 {
		return ""
	}
	var parts []string
	for _, h := range hints {
		keyBadge := common.KeyHintStyle(c).Render(h.Keys)
		desc := lipgloss.NewStyle().Foreground(c.TextMuted).Render(h.Desc)
		parts = append(parts, keyBadge+desc)
	}
	return lipgloss.JoinHorizontal(lipgloss.Left, parts...)
}

// getHintParts 返回各交互模式下的键帽提示分段。
// 文案保持紧凑：提示行总宽必须落在窄对话框的 innerWidth 内，避免 lipgloss 折行。
func getHintParts(it protocol.InteractionType, state userHelpState) []struct{ Keys, Desc string } {
	switch it {
	case protocol.InteractionConfirm:
		return []struct{ Keys, Desc string }{
			{" ↑↓ ", " Move "},
			{" Enter ", " Confirm "},
			{" y/n ", " Quick "},
			{" Esc ", " Cancel "},
		}
	case protocol.InteractionSelect:
		if state == stateSelectCustom {
			return []struct{ Keys, Desc string }{
				{" Enter ", " Submit "},
				{" Esc ", " Back "},
				{" Ctrl+U ", " Clear "},
			}
		}
		return []struct{ Keys, Desc string }{
			{" ↑↓ ", " Move "},
			{" Enter ", " Select "},
			{" 1-9 ", " Quick "},
			{" Esc ", " Cancel "},
		}
	case protocol.InteractionInput:
		if state == stateInputButtons {
			return []struct{ Keys, Desc string }{
				{" ←→ ", " Move "},
				{" Enter ", " Confirm "},
				{" ↑ ", " Input "},
				{" Esc ", " Cancel "},
			}
		}
		return []struct{ Keys, Desc string }{
			{" Enter ", " Submit "},
			{" Tab ", " Buttons "},
			{" Ctrl+U ", " Clear "},
			{" Esc ", " Cancel "},
		}
	default:
		return nil
	}
}
