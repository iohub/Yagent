package tui

import (
	"log/slog"
	"strings"

	"yagent/internal/skills"
	"yagent/internal/tui/common"

	"charm.land/lipgloss/v2"
)

// completionKind 区分"内置命令"与"技能"：今天两者混在一个 []string 里，
// 靠显示层的硬编码特判区分，撞名时会静默吞掉技能。
type completionKind string

const (
	completionCmd   completionKind = "cmd"
	completionSkill completionKind = "skill"
)

// completionItem 是斜杠补全列表的一行。
type completionItem struct {
	name string
	kind completionKind
	desc string
}

// slashBuiltins 是 `/` 前缀认的内置命令（显示顺序即此序）。
// 必须与应用命令逐条对上：斜杠前缀默认是搜索，不在这里列出来的命令会变成搜字符串。
var slashBuiltins = []struct {
	name string
	desc string
}{
	{"history", "浏览历史任务"},
	{"resume", "恢复会话"},
	{"rs", "恢复会话（简写）"},
	{"pause", "暂停当前任务"},
	{"continue", "继续当前任务"},
	{"skills", "列出已加载的技能"},
	{"mcp", "查看 MCP server 状态"},
}

// skillCompletionItems 组装斜杠补全列表：内置命令在前，技能在后。
// 撞名时**内置命令胜**（与 TUI 键位抢键的既有规则同取向，根 AGENTS.md §11 坑 29），
// 并且只 warn 一次 —— 补全每敲一个字都会重建，每次 warn 会刷屏。
func skillCompletionItems(registry *skills.Registry, query string, warned map[string]bool) []completionItem {
	items := make([]completionItem, 0, len(slashBuiltins)+8)
	builtins := make(map[string]bool, len(slashBuiltins))
	for _, b := range slashBuiltins {
		builtins[b.name] = true
		if hasPrefixIgnoreCase(b.name, query) {
			items = append(items, completionItem{name: b.name, kind: completionCmd, desc: b.desc})
		}
	}
	if registry == nil {
		return items
	}

	for _, name := range registry.List() {
		if !hasPrefixIgnoreCase(name, query) {
			continue
		}
		if builtins[name] {
			if warned != nil && !warned[name] {
				warned[name] = true
				slog.Warn("skills: 技能名与内置命令撞名，补全里保留内置命令",
					"component", "tui", "name", name)
			}
			continue
		}
		desc := ""
		if skill, ok := registry.Get(name); ok {
			desc = skill.Description
		}
		items = append(items, completionItem{name: name, kind: completionSkill, desc: desc})
	}
	return items
}

// selectedCompletionItem 返回高亮的那一行（越界/关闭时 ok=false）。
func (m *model) selectedCompletionItem() (completionItem, bool) {
	if !m.skillAutoComplete || len(m.skillSuggestions) == 0 {
		return completionItem{}, false
	}
	if m.skillSuggestionIdx < 0 || m.skillSuggestionIdx >= len(m.skillSuggestions) {
		return completionItem{}, false
	}
	return m.skillSuggestions[m.skillSuggestionIdx], true
}

// splitSlashSkill 把 `/name rest of args` 拆成 (name, args)。
// 只认首个 token 精确命中技能名（见 processCommand）：技能名是 kebab-case，
// 搜索词通常不是，所以误判成技能的概率远低于漏判。
func splitSlashSkill(cmd string) (name, args string) {
	rest := strings.TrimPrefix(strings.TrimSpace(cmd), "/")
	name, args, _ = strings.Cut(rest, " ")
	name = strings.TrimSpace(name)
	return name, strings.TrimSpace(args)
}

// ── 补全面板的分组与几何 ──
//
// 面板的**行数**由 renderSkillSuggestions（画）与 editorBlockHeight（算）两处共用同一份
// 分组口径。footer 的块序与 computeFooterHeight 必须逐块一致（AGENTS.md 硬约束 3），
// 两各算一套就是差一行、整屏滚动抖动。

// completionIndent 是补全行的左缩进：输入面板正文从第 4 列起（左右边框 + 内边距各 1），
// 面板贴在输入框下面，缩进不同就会往左探出输入框。
const completionIndent = 4

const (
	// completionCursorWidth 是光标列的宽度：选中换 "▶"、未选中换空格，列数不变，
	// 名字列才不会随高亮行左右跳。
	completionCursorWidth = 2
	// completionNameGap 是名字列与说明列之间的固定间隔。
	completionNameGap = 1
	// completionMinBand 是内容区下限：再窄就摆不下"光标列 + 一个字符的名字"。
	// 与 computeFieldWidth 的 minField 同取向 —— 那种宽度的终端连输入面板本身都放不下，
	// 这里只保证不算出负宽度、不 panic。
	completionMinBand = 3
	// completionFallbackWidth 是 termWidth 未就绪时的回落宽度，与 computeFieldWidth
	// 和 renderAirlineStatusBar 同一口径（80）。
	completionFallbackWidth = 80
)

// 配色取 common 的语义令牌（AGENTS.md §15：全仓只有一套深色），只有 lavender 141 例外：
// 它在 ColorTokens 里没有对应项，而 141 这一档在本包里已经是"标题/强调"一族
// （活动 tab、thinklink、LLM 调用）。
var (
	completionTokens = common.DarkModeColors()

	completionIndentStyle = lipgloss.NewStyle().PaddingLeft(completionIndent)

	// 两组各用一个图形符号 + 一档蓝：原来的 [cmd]/[skill] 角标把 7 行里的 6 行
	// 贴成同一个无样式标签，等于没给信息。
	completionGroupCmdStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("141")).Bold(true)
	completionGroupSkillStyle = lipgloss.NewStyle().Foreground(completionTokens.PrimaryBright).Bold(true)
	completionGroupRuleStyle  = lipgloss.NewStyle().Foreground(completionTokens.SurfaceRaised)

	completionCursorStyle = lipgloss.NewStyle().Foreground(completionTokens.Accent).Bold(true)
	completionNameStyle   = lipgloss.NewStyle().Foreground(completionTokens.TextSecondary)
	completionDescStyle   = lipgloss.NewStyle().Foreground(completionTokens.TextMuted)

	// 选中行整行铺 236 底色，说明列必须从 244 提到 252：244 压在 236 上只有 3.3:1，
	// 达不到本仓"压在底色上的字 ≥4.5:1"那条底线（见 TestCategoryRows_OnBandTextIsReadable）。
	completionNameSelStyle = lipgloss.NewStyle().Foreground(completionTokens.Primary).Bold(true)
	completionDescSelStyle = lipgloss.NewStyle().Foreground(completionTokens.TextPrimary)
	completionSelRowStyle  = lipgloss.NewStyle().Background(completionTokens.Surface)
)

// completionRow 是面板里的一行条目，带着它在 m.skillSuggestions 里的下标：
// m.skillSuggestionIdx 是**全局**下标（Tab 按它取模循环，见 tui_update_keys.go），
// 分组只改呈现顺序，不能改"选中"的含义。
type completionRow struct {
	index int
	item  completionItem
}

// completionGroup 是面板里的一组（内置命令或技能）。
type completionGroup struct {
	kind completionKind
	rows []completionRow
}

// skillCompletionGroups 把补全列表按 kind 分成两组，命令在前、技能在后；**空组不出现**
// —— 输入 `/h` 只剩 history 时不该冒出一个空的"◇ 技能"标题。
// 组内保持 m.skillSuggestions 的原始顺序（数据层已经是命令在前、技能在后）。
func skillCompletionGroups(items []completionItem) []completionGroup {
	var cmdRows, skillRows []completionRow
	for i, item := range items {
		if item.kind == completionSkill {
			skillRows = append(skillRows, completionRow{index: i, item: item})
			continue
		}
		cmdRows = append(cmdRows, completionRow{index: i, item: item})
	}

	groups := make([]completionGroup, 0, 2)
	if len(cmdRows) > 0 {
		groups = append(groups, completionGroup{kind: completionCmd, rows: cmdRows})
	}
	if len(skillRows) > 0 {
		groups = append(groups, completionGroup{kind: completionSkill, rows: skillRows})
	}
	return groups
}

// skillSuggestionHeaderCount 是"当前这次会渲染出几行组头"的唯一口径：
// 命令组非空算一行、技能组非空算一行。renderSkillSuggestions 与 editorBlockHeight
// 都必须走它 —— 在这里写死 +2，只在窄查询下（只剩一组）就会多算一行高度。
func (m *model) skillSuggestionHeaderCount() int {
	if !m.skillAutoComplete || len(m.skillSuggestions) == 0 {
		return 0
	}
	return len(skillCompletionGroups(m.skillSuggestions))
}

// completionBandWidth 是补全行去掉缩进后的内容区宽度：组头的分隔线补满它，
// 选中行的底色铺满整行（缩进 + 内容区）也是由它推出来的。
func (m *model) completionBandWidth() int {
	width := m.termWidth
	if width <= 0 {
		width = completionFallbackWidth
	}
	band := width - completionIndent
	if band < completionMinBand {
		band = completionMinBand
	}
	return band
}

// completionGroupHeader 渲染组头 `◆ 命令 ─────…`。分隔线吃掉剩下的宽度；
// 窄屏放不下线时只留标题（宁可不补线，也不能让 strings.Repeat 收到负宽度），
// 连标题都放不下时按显示宽度截断 —— 组头折成两行就等于 footer 少算一行，整屏抖。
func completionGroupHeader(kind completionKind, band int) string {
	var style lipgloss.Style
	var label string
	switch kind {
	case completionSkill:
		style, label = completionGroupSkillStyle, "◇ 技能"
	default:
		style, label = completionGroupCmdStyle, "◆ 命令"
	}

	head := style.Render(truncateCells(label, band))
	if dashes := band - lipgloss.Width(head) - 1; dashes > 0 {
		head += " " + completionGroupRuleStyle.Render(strings.Repeat(panelEdgeH, dashes))
	}
	return completionIndentStyle.Render(head)
}

// completionGroupNameCol 是该组的名字列宽 = 组内最长的名字（按显示宽度，CJK 算两列），
// 但绝不挤掉光标列；下限 1 保证 truncateCells 不会收到 0（它把 width<=0 当"不截断"）。
func completionGroupNameCol(rows []completionRow, band int) int {
	longest := 0
	for _, row := range rows {
		if w := lipgloss.Width(row.item.name); w > longest {
			longest = w
		}
	}
	if limit := band - completionCursorWidth; longest > limit {
		longest = limit
	}
	if longest < 1 {
		longest = 1
	}
	return longest
}

// completionItemRow 渲染一行条目：`[2 列光标][名字补齐][1 空格][说明]`。
// 截断一律在**上色之前**做：带 ANSI 的串按列截会把转义字节也算进宽度。
func completionItemRow(row completionRow, nameCol, band int, selected bool) string {
	name := truncateCells(row.item.name, nameCol)

	// 说明列的预算：内容区 - 光标列 - 名字列 - 间隔。放不下就整段不写，
	// 不留那个空格（尾随空格在选中行会被涂成一段没内容的色块）。
	descCol := band - completionCursorWidth - nameCol - completionNameGap
	desc := ""
	if row.item.desc != "" && descCol > 0 {
		desc = truncateCells(row.item.desc, descCol)
	}

	// 名字只有配上说明才需要补到列宽：没有说明列时补白只是尾随空格。
	nameCell := name
	if desc != "" {
		nameCell = padRight(name, nameCol)
	}

	nameStyle, descStyle := completionNameStyle, completionDescStyle
	if selected {
		nameStyle, descStyle = completionNameSelStyle, completionDescSelStyle
	}

	if !selected {
		// 光标列两格（两个空格），与选中行的 "▶" + 间隔同宽：列数一致，
		// 名字列才不会随高亮行左右跳。
		body := strings.Repeat(" ", completionCursorWidth) + nameStyle.Render(nameCell)
		if desc != "" {
			body += " " + descStyle.Render(desc)
		}
		return completionIndentStyle.Render(body)
	}

	// 选中行**逐段**涂底，而不是只在最外层上一次背景：lipgloss 每个内层片段末尾都发
	// \x1b[m，那一下连背景一起关掉，于是光标与名字之间的裸空格会露出一格没铺满的缝
	// （与侧栏横带同一个坑，判据见测试里的 bandGaps）。
	// 宽度也在这里自己算死：整行 = 缩进 + 内容区，多一格就会被折行、footer 行数就对不上。
	used := completionCursorWidth + lipgloss.Width(nameCell)
	if desc != "" {
		used += completionNameGap + lipgloss.Width(desc)
	}

	pieces := []string{
		completionSelRowStyle.Render(strings.Repeat(" ", completionIndent)),
		completionSelRowStyle.Render(completionCursorStyle.Render("▶")),
		completionSelRowStyle.Render(" "),
		completionSelRowStyle.Render(nameStyle.Render(nameCell)),
	}
	if desc != "" {
		pieces = append(pieces,
			completionSelRowStyle.Render(" "),
			completionSelRowStyle.Render(descStyle.Render(desc)),
		)
	}
	// 缩进段已经单独发过一次（4 格），这里的 used 只数内容区，补白 = 内容区 - used。
	if fill := band - used; fill > 0 {
		pieces = append(pieces, completionSelRowStyle.Render(strings.Repeat(" ", fill)))
	}
	return strings.Join(pieces, "")
}

// completionHintItems 是面板底部的操作提示；按键部分走输入面板同一套键帽样式。
var completionHintItems = []string{"Tab 切换", "⏎ 选择", "Esc 关闭"}

// completionHintBar 渲染提示行。宽度不够时从尾部整段丢弃（与 renderHintBar 同一取向）：
// 少一个提示只是信息少了，折成两行则是 footer 行数与 computeFooterHeight 对不上。
func completionHintBar(band int) string {
	const sep = "   "
	sepWidth := lipgloss.Width(sep)

	var b strings.Builder
	used := 0
	for _, item := range completionHintItems {
		rendered := renderHintItem(item)
		cost := lipgloss.Width(rendered)
		if b.Len() > 0 {
			cost += sepWidth
		}
		if used+cost > band {
			break
		}
		if b.Len() > 0 {
			b.WriteString(sep)
		}
		b.WriteString(rendered)
		used += cost
	}
	return completionIndentStyle.Render(b.String())
}
