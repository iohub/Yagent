package components

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"yagent/internal/tui/common"
)

// ── Data Structures ──

// keyItem represents a single keybinding: multiple keys with one description.
type keyItem struct {
	Keys []string
	Desc string
}

// keySection represents a grouped set of keybindings with a title.
type keySection struct {
	Title string
	Items []keyItem
}

// helpData holds the full help dialog data for a given language.
type helpData struct {
	Title    string
	Sections []keySection
	Dismiss  string
}

// ── Constants ──

const (
	maxDialogWidth  = 92 // 对话框内容最大宽度（窄终端自动降级为单列）
	minContentWidth = 40 // 内容最小宽度
	twoColMinWidth  = 92 // 内容宽度 ≥ 此值时启用双栏排版（=maxDialogWidth，确保最长描述不换行）
	colGapWidth     = 3  // 双栏间距
	keyColWidth     = 16

	borderHOffset  = 2 // RoundedBorder 左右各 1 列
	paddingHOffset = 4 // outerPadding(1,2) 左右各 2 列
)

// ── Color Palette (ANSI color codes) ──

const (
	colorKeycapBg     = "235" // 键帽背景（深灰，更有质感）
	colorKeycapFg     = "252" // 键帽前景（浅灰，保持可读）
	colorSectionTitle = "39"  // 分组标题（青）
	colorSectionMark  = "75"  // 分组标题色块标记 ▌（亮蓝）
	colorDescFg       = "250" // 描述文字（提升对比度）
	colorSepLine      = "237" // 分隔线（极浅灰，细腻）
	colorDismissFg    = "240" // 关闭提示（弱化灰）
	colorBorder       = "238" // 对话框外框（低饱和度柔和边框）
)

// ── HelpDialog ──

// HelpDialog shows vim-like keybindings help.
type HelpDialog struct {
	width        int
	height       int
	lang         Language
	cachedView   string
	langCache    Language
	content      helpData
	outerPadding lipgloss.Style
	borderStyle  lipgloss.Style // 柔和 RoundedBorder 外框
	// altKeybindings 可配置快捷键覆盖：原始显示键名 → 自定义键名
	altKeybindings map[string]string
}

// NewHelpDialog creates a new help dialog with the given language.
func NewHelpDialog(lang Language) *HelpDialog {
	content := buildHelpData(lang)
	outerPadding := lipgloss.NewStyle().Padding(1, 2)
	borderStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color(colorBorder))
	return &HelpDialog{
		lang:         lang,
		content:      content,
		outerPadding: outerPadding,
		borderStyle:  borderStyle,
	}
}

// ID returns the unique identifier for this dialog.
func (d *HelpDialog) ID() string { return "help_dialog" }

// Type returns the dialog type.
func (d *HelpDialog) Type() DialogType { return DialogModal }

// Init initializes the component. No setup needed.
func (d *HelpDialog) Init() tea.Cmd { return nil }

// Update processes incoming messages and returns the updated component.
func (d *HelpDialog) Update(msg tea.Msg) (Component, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyMsg)
	if !ok {
		return d, nil
	}

	switch keyMsg.String() {
	case "esc", "i", "I":
		return d, nil // Close handled by model
	case "ctrl+c":
		return d, tea.Quit
	}

	return d, nil
}

// View renders the dialog as a string.
func (d *HelpDialog) View() string {
	if d.width < 50 || d.height < 12 {
		return ""
	}

	// Rebuild cache if language changed.
	if d.lang != d.langCache {
		d.content = buildHelpData(d.lang)
		d.cachedView = ""
		d.langCache = d.lang
	}

	// Apply configurable keybinding overrides.
	if d.altKeybindings != nil && len(d.altKeybindings) > 0 {
		for si, section := range d.content.Sections {
			for ii, item := range section.Items {
				for ki, key := range item.Keys {
					if altKey, ok := d.altKeybindings[key]; ok {
						d.content.Sections[si].Items[ii].Keys[ki] = altKey
					}
				}
			}
		}
	}

	// Return cached view if available.
	if d.cachedView != "" {
		return d.cachedView
	}

	// ── Dynamic content width ──
	// RoundedBorder 占 2 列、outerPadding(1,2) 占 4 列，
	// 可用内容宽 = d.width - 6；上限 maxDialogWidth，下限 minContentWidth。
	contentWidth := d.width - borderHOffset - paddingHOffset
	if contentWidth > maxDialogWidth {
		contentWidth = maxDialogWidth
	}
	if contentWidth < minContentWidth {
		contentWidth = minContentWidth
	}

	// ── Title (centered) ──
	titleStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(colorSectionTitle)).
		Bold(true).
		Width(contentWidth).
		Align(lipgloss.Center)
	titleLine := titleStyle.Render(d.content.Title)

	// ── Gradient separator under main title ──
	gradSep := common.ApplyGrad(strings.Repeat("─", contentWidth))

	// ── Sections (two-column when wide enough, else single column) ──
	var sectionsBlock string
	if contentWidth >= twoColMinWidth {
		leftSecs, rightSecs := splitSectionsBalanced(d.content.Sections)
		leftW := (contentWidth - colGapWidth) / 2
		rightW := contentWidth - colGapWidth - leftW
		leftBlock := renderSections(leftSecs, leftW)
		rightBlock := renderSections(rightSecs, rightW)
		gapCol := lipgloss.NewStyle().Width(colGapWidth).Render("")
		sectionsBlock = lipgloss.JoinHorizontal(lipgloss.Top, leftBlock, gapCol, rightBlock)
	} else {
		sectionsBlock = renderSections(d.content.Sections, contentWidth)
	}

	// ── Dismiss hint (centered, muted) ──
	hintStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(colorDismissFg)).
		Italic(true).
		Width(contentWidth).
		Align(lipgloss.Center)
	hintLine := hintStyle.Render(d.content.Dismiss)

	// ── Assemble ──
	body := lipgloss.JoinVertical(lipgloss.Left,
		titleLine,
		gradSep,
		"",
		sectionsBlock,
		"",
		hintLine,
	)

	padded := d.outerPadding.Render(body)
	bordered := d.borderStyle.Render(padded)

	d.cachedView = lipgloss.Place(d.width, d.height,
		lipgloss.Center, lipgloss.Center,
		bordered,
	)

	return d.cachedView
}

// IsFocused reports whether this dialog has keyboard focus.
func (d *HelpDialog) IsFocused() bool { return true }

// Focus sets the component as focused.
func (d *HelpDialog) Focus() tea.Cmd { return nil }

// Blur removes focus from this component.
func (d *HelpDialog) Blur() {}

// SetBounds sets the component's dimensions.
func (d *HelpDialog) SetBounds(w, h int) { d.width = w; d.height = h }

// Bounds returns the component's current width and height.
func (d *HelpDialog) Bounds() (int, int) { return d.width, d.height }

// IsVisible reports whether this dialog is visible.
func (d *HelpDialog) IsVisible() bool { return true }

// SetVisible sets the visibility of this dialog.
func (d *HelpDialog) SetVisible(bool) {}

// ── Data Builder ──

// buildHelpData returns the help dialog data for the given language.
func buildHelpData(lang Language) helpData {
	if lang == LanguageZh {
		return helpData{
			Title:   "⌨  帮助",
			Dismiss: "按 Esc 或任意键关闭",
			Sections: []keySection{
				{
					Title: "通用导航",
					Items: []keyItem{
						{[]string{"j", "↓"}, "向下滚动一行"},
						{[]string{"k", "↑"}, "向上滚动一行"},
						{[]string{"f", "Ctrl+F"}, "向下翻页"},
						{[]string{"b", "Ctrl+B"}, "向上翻页"},
						{[]string{"gg"}, "跳到开头"},
						{[]string{"G"}, "跳到末尾"},
					},
				},
				{
					Title: "模式切换",
					Items: []keyItem{
						{[]string{"i"}, "进入编辑模式"},
						{[]string{"Ctrl+E"}, "进入命令模式"},
						{[]string{"Ctrl+L"}, "全屏时间线"},
						{[]string{"Ctrl+G"}, "thinklink 视图（命令模式）"},
						{[]string{"?", "Ctrl+H"}, "显示帮助"},
						{[]string{"Esc"}, "返回 / 关闭"},
					},
				},
				{
					Title: "任务与输入（编辑模式）",
					Items: []keyItem{
						{[]string{"Alt+S"}, "提交任务"},
						{[]string{"Tab"}, "自动补全"},
						{[]string{"/"}, "关键词 / 技能补全"},
						{[]string{"@"}, "FZF 文件选择"},
						{[]string{"Enter"}, "确认补全 / 换行"},
					},
				},
				{
					Title: "标签页（编辑模式）",
					Items: []keyItem{
						{[]string{"Ctrl+T"}, "新建标签页"},
						{[]string{"Alt+[", "Alt+]"}, "上一 / 下一标签页"},
						{[]string{"Alt+1~9"}, "跳转指定标签页"},
						{[]string{"Alt+W"}, "关闭标签页"},
						{[]string{"Alt+C"}, "清除会话"},
					},
				},
				{
					Title: "面板与命令",
					Items: []keyItem{
						{[]string{"Alt+D"}, "收缩 / 展开仪表盘"},
						{[]string{"Alt+T"}, "Token 面板（命令模式）"},
						{[]string{"Alt+M"}, "切换模型"},
						{[]string{"Alt+R"}, "模型自动路由（auto / fixed）"},
						{[]string{":q"}, "退出程序"},
						{[]string{":help"}, "显示命令帮助"},
						{[]string{":route"}, "打开路由设置"},
						{[]string{":timeout"}, "查看 / 选择设置 LLM 调用超时（对话框；:timeout 10m 直接设置）"},
						{[]string{"/resume"}, "恢复会话（别名 /rs）"},
						{[]string{"/pattern"}, "搜索日志"},
					},
				},
				{
					Title: "全屏与列表视图",
					Items: []keyItem{
						{[]string{"h", "l"}, "左右切换焦点（时间线全屏）"},
						{[]string{"Space", "l"}, "展开 / 收起分组（恢复视图）"},
						{[]string{"r"}, "刷新列表（恢复视图）"},
						{[]string{"Enter"}, "选中 / 恢复条目"},
						{[]string{"d"}, "删除条目（历史视图）"},
						{[]string{"q", "Esc"}, "退出当前视图"},
					},
				},
				{
					Title: "退出",
					Items: []keyItem{
						{[]string{"Ctrl+C"}, "退出（弹出确认对话框）"},
						{[]string{"q"}, "退出全屏 / 列表视图"},
					},
				},
			},
		}
	}
	return helpData{
		Title:   "⌨  Help",
		Dismiss: "Press Esc or any key to close",
		Sections: []keySection{
			{
				Title: "Navigation",
				Items: []keyItem{
					{[]string{"j", "↓"}, "scroll down one line"},
					{[]string{"k", "↑"}, "scroll up one line"},
					{[]string{"f", "Ctrl+F"}, "page down"},
					{[]string{"b", "Ctrl+B"}, "page up"},
					{[]string{"gg"}, "go to top"},
					{[]string{"G"}, "go to bottom"},
				},
			},
			{
				Title: "Mode Switching",
				Items: []keyItem{
					{[]string{"i"}, "enter edit mode"},
					{[]string{"Ctrl+E"}, "enter command mode"},
					{[]string{"Ctrl+L"}, "fullscreen timeline"},
					{[]string{"Ctrl+G"}, "thinklink view (command mode)"},
					{[]string{"?", "Ctrl+H"}, "show help"},
					{[]string{"Esc"}, "back / close"},
				},
			},
			{
				Title: "Task & Input (Edit Mode)",
				Items: []keyItem{
					{[]string{"Alt+S"}, "submit task"},
					{[]string{"Tab"}, "autocomplete"},
					{[]string{"/"}, "keyword / skill completion"},
					{[]string{"@"}, "FZF file picker"},
					{[]string{"Enter"}, "accept completion / newline"},
				},
			},
			{
				Title: "Tabs (Edit Mode)",
				Items: []keyItem{
					{[]string{"Ctrl+T"}, "new tab"},
					{[]string{"Alt+[", "Alt+]"}, "previous / next tab"},
					{[]string{"Alt+1~9"}, "jump to tab N"},
					{[]string{"Alt+W"}, "close tab"},
					{[]string{"Alt+C"}, "clear session"},
				},
			},
			{
				Title: "Panels & Commands",
				Items: []keyItem{
					{[]string{"Alt+D"}, "collapse / expand dashboard"},
					{[]string{"Alt+T"}, "token panel (command mode)"},
					{[]string{"Alt+M"}, "switch model"},
					{[]string{"Alt+R"}, "model auto-routing (auto / fixed)"},
					{[]string{":q"}, "quit"},
					{[]string{":help"}, "show command help"},
					{[]string{":route"}, "open routing settings"},
					{[]string{":timeout"}, "show / select LLM call timeout (dialog; :timeout 10m sets directly)"},
					{[]string{"/resume"}, "resume session (alias /rs)"},
					{[]string{"/pattern"}, "search log"},
				},
			},
			{
				Title: "Fullscreen & List Views",
				Items: []keyItem{
					{[]string{"h", "l"}, "move focus left / right (timeline)"},
					{[]string{"Space", "l"}, "expand / collapse group (resume)"},
					{[]string{"r"}, "refresh list (resume)"},
					{[]string{"Enter"}, "select / restore entry"},
					{[]string{"d"}, "delete entry (history)"},
					{[]string{"q", "Esc"}, "exit current view"},
				},
			},
			{
				Title: "Quit",
				Items: []keyItem{
					{[]string{"Ctrl+C"}, "quit (confirmation dialog)"},
					{[]string{"q"}, "exit fullscreen / list view"},
				},
			},
		},
	}
}

// ── Rendering Functions ──

// keycapStyle returns a lipgloss Style for rendering a single keycap.
func keycapStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Background(lipgloss.Color(colorKeycapBg)).
		Foreground(lipgloss.Color(colorKeycapFg)).
		Bold(true).
		Padding(0, 1)
}

// renderKeycap renders a single key name as a keycap badge.
func renderKeycap(k string) string {
	return keycapStyle().Render(k)
}

// renderKeyColumn renders multiple synonymous keycaps separated by spaces,
// right-aligned within a fixed-width column.
func renderKeyColumn(item keyItem) string {
	if len(item.Keys) == 0 {
		return strings.Repeat(" ", keyColWidth)
	}

	keycaps := make([]string, len(item.Keys))
	for i, k := range item.Keys {
		keycaps[i] = renderKeycap(k)
	}
	joined := strings.Join(keycaps, " ")

	// Measure total rendered width (with spaces included).
	var totalW int
	for i, k := range item.Keys {
		totalW += lipgloss.Width(renderKeycap(k))
		if i < len(item.Keys)-1 {
			totalW += 1 // one space between keycaps
		}
	}

	padding := keyColWidth - totalW
	if padding > 0 {
		return strings.Repeat(" ", padding) + joined
	}
	return joined
}

// renderRow renders one keybinding row: keycaps column + description,
// padded/wrapped to colWidth so the two-column layout stays aligned.
func renderRow(item keyItem, colWidth int) string {
	desc := lipgloss.NewStyle().
		Foreground(lipgloss.Color(colorDescFg)).
		Render(item.Desc)
	row := renderKeyColumn(item) + "  " + desc
	return lipgloss.NewStyle().Width(colWidth).Render(row)
}

// renderSection renders one section: a color-block marker + title, a thin
// separator line, and all rows. Every line is padded to colWidth so the
// two-column layout stays perfectly aligned.
func renderSection(s keySection, colWidth int) string {
	var lines []string

	// 标题：▌ 色块标记（亮蓝）+ 标题文字（青），层次分明
	mark := lipgloss.NewStyle().
		Foreground(lipgloss.Color(colorSectionMark)).
		Bold(true).
		Render("▌")
	titleText := lipgloss.NewStyle().
		Foreground(lipgloss.Color(colorSectionTitle)).
		Bold(true).
		Render(s.Title)
	titleLine := lipgloss.NewStyle().
		Width(colWidth).
		Render(mark + " " + titleText)
	lines = append(lines, titleLine)

	// 分隔线：细腻，长度与栏宽对齐
	sepStyle := lipgloss.NewStyle().
		Foreground(lipgloss.Color(colorSepLine)).
		Width(colWidth)
	lines = append(lines, sepStyle.Render(strings.Repeat("─", colWidth)))

	for _, item := range s.Items {
		lines = append(lines, renderRow(item, colWidth))
	}

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// renderSections renders all sections as a single vertical block.
// The dialogWidth parameter is the column width each section is padded to.
func renderSections(sections []keySection, dialogWidth int) string {
	var blocks []string
	for _, s := range sections {
		blocks = append(blocks, renderSection(s, dialogWidth))
	}
	return lipgloss.JoinVertical(lipgloss.Left, blocks...)
}

// splitSectionsBalanced 将分组按渲染高度均衡地分为左右两栏（保持相对顺序）。
// 返回的 left/right 分别对应左栏与右栏的分组切片；分组数 ≤1 时全部归左栏。
func splitSectionsBalanced(sections []keySection) (left, right []keySection) {
	n := len(sections)
	if n <= 1 {
		return sections, nil
	}
	heights := make([]int, n)
	total := 0
	for i, s := range sections {
		heights[i] = 2 + len(s.Items) // 标题 + 分隔线 + 条目
		total += heights[i]
	}
	bestK, bestDiff := 1, total
	prefix := 0
	for k := 1; k < n; k++ { // 左右栏各至少保留 1 组
		prefix += heights[k-1]
		diff := absInt(2*prefix - total)
		if diff < bestDiff {
			bestDiff = diff
			bestK = k
		}
	}
	return sections[:bestK], sections[bestK:]
}

// SetAltKeybindings sets display overrides for configurable keybindings.
// key is the default key displayed in the help page (e.g., "j", "Ctrl+E"),
// value is the user-configured key display string (e.g., "Ctrl+J", "Ctrl+Space").
func (d *HelpDialog) SetAltKeybindings(kb map[string]string) {
	d.altKeybindings = kb
	d.cachedView = "" // invalidate cache so View() rebuilds
}
