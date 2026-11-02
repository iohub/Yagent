package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"yagent/internal/datamanager"
	"yagent/internal/memory"
	"yagent/internal/tui/common"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// 空白启动屏（transcript 里一条日志都没有时）的"品牌区 + 左右双卡片"布局。
//
// 三件事集中在本文件，别处不许再抄：
//  ① 卡片几何（renderTitledCard / fitStartupCard）：lipgloss 没有"标题嵌进顶边框"的
//     原语，手拼边框字符时宽度一律走 lipgloss.Width（中文/emoji 是双宽，len() 会算成 1）。
//  ② 启动屏文案（startupTextsFor）：短文案不进 i18n.go 的 translations（那套 69 字段、
//     三处同步），但语言口径仍与界面同源（m.currentLang）。
//  ③ 最近会话的取数与消息（startupRecentsMsg / loadStartupRecentsCmd）：**rollout 优先、
//     tasks 兜底**，两者投影成同一个 startupRecentItem，来源记在 startupRecentsSource 上，
//     右卡 hint 随之切换 —— 卡片列的是哪个账本，就提示那个账本的入口。
//
// 尺寸预算见 renderCenteredStartupScreen：任何档位下每行显示宽 <= 传入 width、总行数
// <= height —— lipgloss.Place 在内容超出时是 noop（gap<=0 直接返回原文），不会替我们夹。

const (
	// startupBrandRows 是品牌区的固定行数：banner 3 行 + tagline 1 行 + cwd 1 行。
	startupBrandRows = 5
	// startupCardGap 是并排两卡之间的空列数（左 1 列 + 右 1 列）。
	startupCardGap = 2
	// startupWideWidth 起用双卡并排 + 左 4 条。
	startupWideWidth = 88
	// startupNarrowWidth 是不再并排（改为上下堆叠）的分界。
	startupNarrowWidth = 76
	// startupLegacyBelow 以下回退老形态（banner + tagline + cwd）：连一张卡都放不下。
	startupLegacyBelow = 10
	// startupRecentsLimit 是右卡要的条数。
	startupRecentsLimit = 4
)

// startupTagline 与 banner 一起构成品牌区。两侧的 ─── 是为了在卡片宽度下把视线从
// ASCII logo 过渡到下方卡片（旧实现只有左侧一段）。
const startupTagline = "─── A Repository-Aware, Self-Evolving Agent ───"

// ── 文案 ────────────────────────────────────────────────────────────────────

// startupTexts 是启动屏的全部文案。零值（Lang 既非中文也非英文，例如夹具里未装配的
// currentLang）按英文走，与 i18n.go 的默认语言一致。
type startupTexts struct {
	StartTitle  string
	RecentTitle string // "◷ 最近会话"
	SendDesc    string // SubmitTask
	CommandDesc string // CommandMode
	HelpDesc    string // 命令模式的 ToggleHelp
	ResumeDesc  string // /resume（斜杠命令，没有可配置键位）
	StartHint   string
	// 右卡 hint 按数据来源分两句：说哪句取决于右卡那 4 条到底是哪个账本的。
	// 一句通用文案必然对其中一套账本撒谎（看到 tasks 列表却提示 /resume，
	// 敲下去是另一批会话）—— 这正是 startupRecentsSource 存在的理由。
	RecentHintResume string // rollout 账本 → /resume（全量恢复，含断点续跑）
	RecentHintHist   string // tasks 账本 → :hist（浏览并载入，弱恢复）
	EmptyTitle       string
	EmptyHint        string
	TurnsFormat      string // fmt.Sprintf 用，中文 "%d轮" / 英文 "%d turns"
	Untitled         string // Label 全空时的兜底，绝不画空串
	Now              string // 刚刚 / just now
	Yesterday        string // 昨天 / yesterday
}

// recentHint 按数据来源选右卡 hint。FromNone（两套账本都空）返回空串：
// 空态已经有两行引导语，再加一句"怎么恢复会话"是指向不存在的东西。
func (t startupTexts) recentHint(src startupRecentsSource) string {
	switch src {
	case startupRecentsFromRollout:
		return t.RecentHintResume
	case startupRecentsFromTasks:
		return t.RecentHintHist
	default:
		return ""
	}
}

func startupTextsFor(lang Language) startupTexts {
	if lang == LangChinese {
		return startupTexts{
			StartTitle:       "⚡ 开始",
			RecentTitle:      "◷ 最近会话",
			SendDesc:         "发送消息",
			CommandDesc:      "命令模式",
			HelpDesc:         "帮助",
			ResumeDesc:       "恢复会话",
			StartHint:        "直接输入任务开始对话 ↓",
			RecentHintResume: "/resume 选择并恢复会话",
			RecentHintHist:   ":hist 浏览并载入历史会话",
			EmptyTitle:       "还没有历史会话",
			EmptyHint:        "输入你的第一个任务，开始对话 ↓",
			TurnsFormat:      "%d轮",
			Untitled:         "(无标题)",
			Now:              "刚刚",
			Yesterday:        "昨天",
		}
	}
	return startupTexts{
		StartTitle:       "⚡ Start",
		RecentTitle:      "◷ Recent sessions",
		SendDesc:         "Send message",
		CommandDesc:      "Command mode",
		HelpDesc:         "Help",
		ResumeDesc:       "Resume session",
		StartHint:        "Type a task to start ↓",
		RecentHintResume: "/resume to pick a session",
		RecentHintHist:   ":hist to browse past sessions",
		EmptyTitle:       "No past sessions yet",
		EmptyHint:        "Type your first task to start ↓",
		TurnsFormat:      "%d turns",
		Untitled:         "(untitled)",
		Now:              "just now",
		Yesterday:        "yesterday",
	}
}

// ── 相对时间 ────────────────────────────────────────────────────────────────

// startupRelTime 是右卡用的极简相对时间：刚刚 / 12m / 3h / 昨天 / 3d / 2w。
//
// 与 /resume 列表的 resumeRelTime（tui_resume_list.go）刻意分成两份：那个函数输出英文
// "12m ago"/"3.5h ago"（带小数），已被 tui_resume_list_test 钉住，改它等于改另一处界面；
// 这里要的是中文人话 + 更短的宽度，两者语义不同，不要合并。
func startupRelTime(t time.Time) string {
	return startupRelTimeAt(t, time.Now(), LangChinese)
}

// startupRelTimeLang 按界面语言取文案；中文走 startupRelTime（默认语言的主入口），
// 其余语言（含未装配的零值）落到英文表，与 startupTextsFor 的分支保持一致。
func startupRelTimeLang(t time.Time, lang Language) string {
	if lang == LangChinese {
		return startupRelTime(t)
	}
	return startupRelTimeAt(t, time.Now(), lang)
}

// startupRelTimeAt 把"现在"作为参数，边界（分钟/小时/昨天/天/周）才可测。
func startupRelTimeAt(t, now time.Time, lang Language) string {
	txt := startupTextsFor(lang)
	if t.IsZero() {
		return "—"
	}
	d := now.Sub(t)
	if d < 0 { // 时钟回拨 / 未来时间：按"刚刚"处理，别显示负数
		d = 0
	}
	if d < time.Minute {
		return txt.Now
	}
	// 先按日历日判定"昨天"：23:50 到次日 00:10 只有 20 分钟，但它确实是昨天。
	if days := startupCalendarDays(t, now); days > 0 {
		switch {
		case days == 1:
			return txt.Yesterday
		case days < 14:
			return fmt.Sprintf("%dd", days)
		default:
			return fmt.Sprintf("%dw", days/7)
		}
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

// startupCalendarDays 是 t 与 now 的日历日差（按 now 所在时区的零点计）。+12h 让夏令时
// 造成的 23/25 小时日长四舍五入回整天。
func startupCalendarDays(t, now time.Time) int {
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.Date()
	a := time.Date(y1, m1, d1, 0, 0, 0, 0, now.Location())
	b := time.Date(y2, m2, d2, 0, 0, 0, 0, now.Location())
	return int((b.Sub(a) + 12*time.Hour) / (24 * time.Hour))
}

// ── 卡片几何 ────────────────────────────────────────────────────────────────

// startupCardPlan 是一张卡在给定行预算下的实际形态。
// 不变量：height == 2(边框) + items + bool(blank) + bool(hint)。
type startupCardPlan struct {
	height int
	items  int
	blank  bool // 条目后是否留一个空行（满配才有）
	hint   bool // 是否画底部 hint
}

// fitStartupCard 在 budget 行里放下一张卡：满配（条目 + 空行 + hint + 上下边框）装不下时
// 按"空行 → hint → 条目"的顺序让位（条目的信息量最大，最后才减）。连"1 条 + 边框"都
// 放不下时返回零值，调用方据此省略整张卡。
func fitStartupCard(items, budget int) startupCardPlan {
	if items < 1 || budget < 3 {
		return startupCardPlan{}
	}
	if budget >= items+4 {
		return startupCardPlan{height: items + 4, items: items, blank: true, hint: true}
	}
	if budget >= items+3 {
		return startupCardPlan{height: items + 3, items: items, hint: true}
	}
	if budget >= items+2 {
		return startupCardPlan{height: items + 2, items: items}
	}
	for n := items - 1; n >= 1; n-- {
		if budget >= n+2 {
			return startupCardPlan{height: n + 2, items: n}
		}
	}
	return startupCardPlan{}
}

// renderTitledCard 画一张"标题嵌进顶边框"的圆角卡片（`╭─ ⚡ 开始 ───╮`）。
//
// lipgloss 没有这个 API，所以手拼边框字符。总宽预算：
//
//	1(╭) + 1(─) + 1(空格) + 标题 + 1(空格) + N(横线) + 1(╮) == width
//	⇒ N = width - 5 - lipgloss.Width(title)
//
// 标题过长（N < 0）时不硬塞：退化成"顶边框不带标题 + 标题占内容第一行"，否则边框会被
// 挤到超出 width。内容行同理，超宽按显示宽度截断（双宽字符不会把行撑破）。
func renderTitledCard(title, hint string, body []string, width, height int, c common.ColorTokens) string {
	if width < 4 || height < 3 {
		return ""
	}
	border := lipgloss.NewStyle().Foreground(c.Border)
	accent := lipgloss.NewStyle().Foreground(c.Accent)
	muted := lipgloss.NewStyle().Foreground(c.TextMuted).Italic(true)
	inner := width - 4 // "│ " + inner + " │"

	rows := make([]string, 0, height)
	if fill := width - 5 - lipgloss.Width(title); title == "" || fill < 0 {
		rows = append(rows, border.Render("╭"+strings.Repeat("─", width-2)+"╮"))
		if title != "" {
			body = append([]string{accent.Render(truncateCells(title, inner))}, body...)
		}
	} else {
		rows = append(rows,
			border.Render("╭─ ")+accent.Render(title)+border.Render(" "+strings.Repeat("─", fill)+"╮"))
	}

	hintRows := 0
	if hint != "" {
		hintRows = 1
	}
	bodyRows := height - 2 - hintRows
	for i := 0; i < bodyRows; i++ {
		content := ""
		if i < len(body) {
			content = body[i]
		}
		rows = append(rows, cardRow(border, content, inner))
	}
	if hintRows == 1 {
		rows = append(rows, cardRow(border, muted.Render(truncateCells(hint, inner)), inner))
	}
	rows = append(rows, border.Render("╰"+strings.Repeat("─", width-2)+"╯"))
	return strings.Join(rows, "\n")
}

// cardRow 画一行卡片内容：`│ ` + 内容补齐到 inner + ` │`，宽度恰好 = inner + 4。
func cardRow(border lipgloss.Style, content string, inner int) string {
	if lipgloss.Width(content) > inner {
		// 上色串已经被截断过就不用走这条路；走到这里说明漏了一段超宽内容，
		// 剥掉颜色再按显示宽截断 —— ANSI 序列被切断会污染整屏，比丢颜色糟。
		content = truncateCells(stripANSIForBorder(content), inner)
	}
	return border.Render("│") + " " + padRight(content, inner) + " " + border.Render("│")
}

// clampStartupBlock 是整块的最后一道闸门：宽/高一旦超出就会顶坏 footer 或让 viewport
// 折行（行数与排版块对不上）。lipgloss.Place 在这里帮不上忙 —— 内容超出目标尺寸时它是
// noop（position.go 的 PlaceHorizontal/PlaceVertical 遇到 gap<=0 直接返回原文）。
func clampStartupBlock(block string, width, height int) string {
	if width < 1 || height < 1 {
		return ""
	}
	lines := strings.Split(block, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for i, line := range lines {
		if lipgloss.Width(line) > width {
			lines[i] = truncateCells(stripANSIForBorder(line), width)
		}
	}
	return strings.Join(lines, "\n")
}

// ── 键位 ────────────────────────────────────────────────────────────────────

// startupKeyOverrides 取用户自定义键位的显示名（默认显示名 → 自定义显示名），口径与
// 帮助页完全一致（buildHelpKeyOverrides + formatKeyDisplayName）。启动页另抄一份键名，
// 症状是用户改过 [tui.keybindings] 之后启动页与帮助页说的是两套键。
func (m *model) startupKeyOverrides() map[string]string {
	if m.com == nil || m.com.Config == nil {
		return nil // 夹具与"配置没装配"的路径：退回默认显示名，不要 nil 解引用
	}
	return buildHelpKeyOverrides(&m.com.Config.TUI.Keybindings)
}

// startupKeyLabel 返回默认显示名在用户配置下的实际显示名。
func startupKeyLabel(overrides map[string]string, def string) string {
	if v, ok := overrides[def]; ok {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return def
}

// startupKeyEntry 是左卡的一行：键帽 + 描述。
type startupKeyEntry struct{ key, desc string }

func (m *model) startupKeyEntries(t startupTexts) []startupKeyEntry {
	o := m.startupKeyOverrides()
	return []startupKeyEntry{
		{startupKeyLabel(o, "Alt+S"), t.SendDesc},     // SubmitTask
		{startupKeyLabel(o, "Ctrl+E"), t.CommandDesc}, // CommandMode（编辑态）
		{startupKeyLabel(o, "?"), t.HelpDesc},         // 命令模式的 ToggleHelp
		{"/resume", t.ResumeDesc},                     // 斜杠命令，没有可配置键位
	}
}

// ── 卡片内容 ────────────────────────────────────────────────────────────────

// startupStartCardBody 组装左卡「⚡ 开始」的内容行。键帽列按最宽那枚对齐，
// 描述按剩余宽度截断（键帽本身不截断：它是这行唯一不可推断的信息）。
func (m *model) startupStartCardBody(plan startupCardPlan, t startupTexts, c common.ColorTokens, inner int) []string {
	entries := m.startupKeyEntries(t)
	n := min(plan.items, len(entries))
	if n <= 0 {
		return nil
	}
	keyStyle := common.KeyHintStyle(c)
	nameStyle := lipgloss.NewStyle().Foreground(c.TextPrimary)
	caps := make([]string, n)
	capW := 0
	for i := 0; i < n; i++ {
		caps[i] = keyStyle.Render(entries[i].key)
		capW = max(capW, lipgloss.Width(caps[i]))
	}
	rows := make([]string, 0, n+1)
	for i := 0; i < n; i++ {
		descW := inner - capW - 1
		if descW < 4 {
			// 窄到放不下两列：不补齐，直接跟在键帽后面。
			rows = append(rows, caps[i]+" "+nameStyle.Render(truncateCells(entries[i].desc, max(inner-capW-1, 1))))
			continue
		}
		rows = append(rows, padRight(caps[i], capW)+" "+nameStyle.Render(truncateCells(entries[i].desc, descW)))
	}
	if plan.blank {
		rows = append(rows, "")
	}
	return rows
}

// startupRecentsCardBody 组装右卡「◷ 最近会话」的内容行：
// 序号(Accent) + 标题(TextPrimary, 按剩余宽截断) + 相对时间·轮数(TextMuted, 右对齐)。
//
// 只认 startupRecentItem 的 Label/When/Turns —— 数据来自 rollout 还是 tasks 由
// startupRecentsSource 决定（只影响 hint 文案），这里不出现任何账本字段名：
// 换数据源不该动渲染代码。
func (m *model) startupRecentsCardBody(plan startupCardPlan, t startupTexts, c common.ColorTokens, inner int) []string {
	mutedStyle := lipgloss.NewStyle().Foreground(c.TextMuted)
	if len(m.startupRecents) == 0 {
		return []string{
			mutedStyle.Render(truncateCells(t.EmptyTitle, inner)),
			mutedStyle.Render(truncateCells(t.EmptyHint, inner)),
		}
	}
	numStyle := lipgloss.NewStyle().Foreground(c.Accent)
	titleStyle := lipgloss.NewStyle().Foreground(c.TextPrimary)
	n := min(plan.items, len(m.startupRecents))
	rows := make([]string, 0, n+1)
	for i := 0; i < n; i++ {
		item := m.startupRecents[i]
		// Label 可能是空串（rollout 文件既无 user 消息又没有可回退的身份），
		// 画成空列的话这行就只剩序号和时间，用户认不出是哪次会话。
		label := strings.TrimSpace(item.Label)
		if label == "" {
			label = t.Untitled
		}
		// 行宽：1(序号) + 1(空格) + 标题 + [1(空格) + 元信息] == inner
		meta := startupRelTimeLang(item.When, m.currentLang)
		if item.Turns > 0 {
			meta += " · " + fmt.Sprintf(t.TurnsFormat, item.Turns)
		}
		titleW := inner - 3 - lipgloss.Width(meta)
		if titleW < 4 && item.Turns > 0 {
			// 元信息太长会挤掉标题：先退成只显示时间。
			meta = startupRelTimeLang(item.When, m.currentLang)
			titleW = inner - 3 - lipgloss.Width(meta)
		}
		if titleW < 4 {
			meta = ""
			titleW = inner - 2
		}
		titleW = max(titleW, 1)
		row := numStyle.Render(fmt.Sprintf("%d", i+1)) + " " +
			titleStyle.Render(padRight(truncateCells(label, titleW), titleW))
		if meta != "" {
			row += " " + mutedStyle.Render(padLeft(meta, lipgloss.Width(meta)))
		}
		rows = append(rows, row)
	}
	if plan.blank {
		rows = append(rows, "")
	}
	return rows
}

// ── 品牌区与布局 ────────────────────────────────────────────────────────────

// startupDisplayPath 把项目目录缩成 ~ 开头的显示路径（启动屏与紧凑头部共用一份）。
func startupDisplayPath(dir string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return dir
	}
	if strings.HasPrefix(dir, home) {
		return "~" + strings.TrimPrefix(dir, home)
	}
	return dir
}

// startupBrandLines 是品牌区那五行（banner 3 + tagline + cwd），每行都已居中到 w，
// 所以整块宽度恒为 w —— 交给 lipgloss.Place 时不会因为它超宽而失灵。
func (m *model) startupBrandLines(w int) []string {
	w = max(w, 1)
	var rows []string
	if banner := strings.Split(renderBanner(), "\n"); lipgloss.Width(banner[0]) <= w {
		rows = append(rows, banner...)
	} else {
		// 窄终端放不下带内边距的渐变 banner：退回未上色的 ASCII，按显示宽截断。
		for _, line := range bannerASCIILines() {
			rows = append(rows, truncateCells(line, w))
		}
	}
	if lipgloss.Width(startupTagline) <= w {
		rows = append(rows, common.ApplyGrad(startupTagline))
	}
	rows = append(rows, welcomeSubStyle.Render(truncateCells(startupDisplayPath(m.projectDir), w)))
	for i := range rows {
		rows[i] = padCenter(rows[i], w)
	}
	return rows
}

// renderCenteredStartupScreen 是空白会话启动屏的唯一入口。
//
// 档位（宽 = m.computeFieldWidth() 即 termWidth-4，高 = startupScreenSize 的正文高）：
//
//	height < 10        → 老形态（banner + tagline + cwd），一张卡都放不下
//	宽 >= 88 且高>=18  → 并排双卡，左 4 条 + 右 4 条
//	宽 >= 76           → 并排双卡，左 3 条 + 右 4 条
//	宽 < 76            → 上下堆叠（各占满可用宽），左 3 条 + 右 2 条
//
// 档位只决定"并排还是堆叠、理想条目数"；实际卡片高度由行预算
// （height - 品牌区 - 1 行间隔）经 fitStartupCard 逐级收缩决定，所以并排/堆叠在预算不足时
// 会自己退成单卡，不会出现"算出来放不下、末尾被裁掉底边框"的形态（设计稿的"高<16 只放左卡"
// 就是这条收缩的结果，比按高度硬切更准：80x24 的正文只有 15 行，但预算够放并排双卡）。
func (m *model) renderCenteredStartupScreen(width, height int) string {
	t := startupTextsFor(m.currentLang)
	c := common.DarkModeColors()
	w := m.computeFieldWidth()

	place := func(rows []string) string {
		block := strings.Join(rows, "\n")
		return clampStartupBlock(
			lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, block),
			width, height,
		)
	}

	if height < startupLegacyBelow {
		return place(m.startupBrandLines(w))
	}

	rows := m.startupBrandLines(w)
	rows = append(rows, "") // 品牌区与卡片区之间的空行
	budget := height - startupBrandRows - 1

	switch {
	case w >= startupWideWidth && height >= 18:
		rows = append(rows, m.renderStartupCardsRow(t, c, w, budget, 4, 4)...)
	case w >= startupNarrowWidth:
		rows = append(rows, m.renderStartupCardsRow(t, c, w, budget, 3, 4)...)
	default:
		rows = append(rows, m.renderStartupStack(t, c, w, budget, 3, 2)...)
	}
	return place(rows)
}

// cardHint 让"计划里没有 hint 行"的卡不要画 hint：renderTitledCard 见到非空 hint 就占掉
// 一行，而 fitStartupCard 可能已经把那一行让给了条目 —— 传空串才是计划的样子，
// 否则最后一条会被 bodyRows 静默挤掉。
func cardHint(plan startupCardPlan, hint string) string {
	if !plan.hint {
		return ""
	}
	return hint
}

// startupRecentsHint 是右卡 hint 的唯一取值口径：**数据来自哪个账本就提示哪个入口**。
//
// 这条映射是本文件的防欺骗底线：右卡曾经用 tasks 账本渲染、却固定写"/resume 选择并恢复会话"，
// 用户敲 /resume 看到的是另一批会话（rollout 账本），而两套列表连条数都对不上。
// 空态（两源皆空）返回空串 —— cardHint 之外的又一层保护：没有任何会话可恢复时，
// 指路文案指向的是不存在的东西。
func (m *model) startupRecentsHint(t startupTexts) string {
	if len(m.startupRecents) == 0 {
		return ""
	}
	return t.recentHint(m.startupRecentsSource)
}

// renderStartupSingleCard 只画左卡（放不下双卡的矮终端：先把"怎么用"讲清楚）。
func (m *model) renderStartupSingleCard(t startupTexts, c common.ColorTokens, w, budget, items int) []string {
	plan := fitStartupCard(items, budget)
	if plan.height == 0 {
		return nil
	}
	card := renderTitledCard(t.StartTitle, cardHint(plan, t.StartHint), m.startupStartCardBody(plan, t, c, w-4), w, plan.height, c)
	return strings.Split(card, "\n")
}

// renderStartupCardsRow 并排渲染左右两卡。两卡同高（JoinHorizontal 逐行拼接），
// 右卡放不下时左卡占满整宽而不是留一半空白。
func (m *model) renderStartupCardsRow(t startupTexts, c common.ColorTokens, w, budget, leftItems, rightItems int) []string {
	leftPlan := fitStartupCard(leftItems, budget)
	if leftPlan.height == 0 {
		return nil
	}
	rightPlan := fitStartupCard(rightItems, budget)
	if rightPlan.height == 0 {
		return m.renderStartupSingleCard(t, c, w, budget, leftItems)
	}
	leftW := (w - startupCardGap) / 2
	rightW := w - startupCardGap - leftW
	h := max(leftPlan.height, rightPlan.height)
	left := renderTitledCard(t.StartTitle, cardHint(leftPlan, t.StartHint), m.startupStartCardBody(leftPlan, t, c, leftW-4), leftW, h, c)
	right := renderTitledCard(t.RecentTitle, cardHint(rightPlan, m.startupRecentsHint(t)), m.startupRecentsCardBody(rightPlan, t, c, rightW-4), rightW, h, c)
	gap := strings.Repeat(" ", startupCardGap)
	return strings.Split(lipgloss.JoinHorizontal(lipgloss.Top, left, gap, right), "\n")
}

// startupStackGap 是堆叠档两卡之间**必须**保留的空行数。
//
// 它不是可让位的预算项：上卡的 ╯ 直接接下卡的 ╭ 时，两张卡在视觉上糊成一张大框
// （没有空行 = 没有分组），"整体和谐有质感"当场破功。所以要塞下右卡时宁可先砍它的条目、
// 再整块省略它，也不吃掉这一行 —— 少一张回顾卡是可接受的降级，糊成一坨不是。
const startupStackGap = 1

// renderStartupStack 在窄终端上下堆叠两卡（各占满可用宽）。
//
// 让位顺序（从轻到重）：左卡空行 → 左卡 hint → 右卡条目 2→1 → **整块省略右卡**；
// 两卡之间的空行恒定 1 行，永不参与压缩。左卡是"怎么用"的入口，条目不减（hint/空行可减）；
// 右卡只是回顾，先让条目再让整卡。
func (m *model) renderStartupStack(t startupTexts, c common.ColorTokens, w, budget, leftItems, rightItems int) []string {
	minLeft := leftItems + 2 // 左卡最小形态：条目 + 上下边框
	if rightItems < 1 {
		rightItems = 1
	}
	minRight := 1 + 2 // 右卡最小形态：1 条 + 上下边框

	if budget < minLeft+startupStackGap+minRight {
		// 空行 + 右卡最小形态装不下：只渲染左卡，把省下的预算还给它的条目/hint/空行。
		plan := fitStartupCard(leftItems, budget)
		if plan.height == 0 {
			return nil
		}
		card := renderTitledCard(t.StartTitle, cardHint(plan, t.StartHint), m.startupStartCardBody(plan, t, c, w-4), w, plan.height, c)
		return strings.Split(card, "\n")
	}

	extra := budget - (minLeft + startupStackGap + minRight)
	// 余量按"信息量"分配，而不是按卡片顺序：
	//  ① 左卡 hint（一行教怎么开始，信息量最高）
	//  ② 右卡补回第 2 条历史（真实内容）
	//  ③ 左卡空行（纯装饰，最后才给）
	//  ④ 剩下的全给右卡（fitStartupCard 会自己封顶）
	leftExtra, rightExtra := 0, 0
	if extra > 0 {
		leftExtra, extra = 1, extra-1
	}
	if extra > 0 && rightItems > 1 {
		rightExtra, extra = 1, extra-1
	}
	if extra > 0 {
		leftExtra, extra = leftExtra+1, extra-1
	}
	rightExtra += extra

	leftPlan := fitStartupCard(leftItems, minLeft+leftExtra)
	rightPlan := fitStartupCard(rightItems, minRight+rightExtra)
	if leftPlan.height == 0 {
		return nil
	}

	left := renderTitledCard(t.StartTitle, cardHint(leftPlan, t.StartHint), m.startupStartCardBody(leftPlan, t, c, w-4), w, leftPlan.height, c)
	rows := strings.Split(left, "\n")
	if rightPlan.height == 0 {
		// minRight 已保证右卡放得下；走到这里说明不变量被破坏，此时也绝不能紧贴 ——
		// 退回"只有左卡"而不是"省掉空行"。
		return rows
	}
	right := renderTitledCard(t.RecentTitle, cardHint(rightPlan, m.startupRecentsHint(t)), m.startupRecentsCardBody(rightPlan, t, c, w-4), w, rightPlan.height, c)
	for i := 0; i < startupStackGap; i++ {
		rows = append(rows, "")
	}
	rows = append(rows, strings.Split(right, "\n")...)
	return rows
}

// ── 数据流 ──────────────────────────────────────────────────────────────────

// startupRecentItem 是两套账本（rollout / tasks）的统一投影：右卡渲染只认
// Label/When/Turns，不认任何账本字段名 —— 换数据源不该动渲染代码。
type startupRecentItem struct {
	Label string    // 会话标题/预览（允许空串，渲染侧兜底成 Untitled）
	When  time.Time // 最近活动时间（rollout = ModTime / tasks = UpdatedAt）
	Turns int       // 轮数（rollout = head-scan 到的 message 条数 / tasks = MessageCount）
}

// startupRecentsSource 说明右卡那几条到底来自哪个账本。它唯一的作用是选 hint 文案：
// **数据来自哪个账本就提示哪个入口**（rollout → /resume；tasks → :hist）。
//
// 少了这层区分，两套账本就会被一句通用文案缝合：右卡列 tasks 的会话、hint 却写 /resume，
// 用户敲下去看到的是另一批列表 —— 功能性欺骗，且两边条数永远对不上。
type startupRecentsSource int

const (
	startupRecentsFromRollout startupRecentsSource = iota // → /resume（全量恢复，含断点续跑）
	startupRecentsFromTasks                               // → :hist（浏览并载入，弱恢复）
	startupRecentsFromNone                                // 两源皆空：走空态，无 hint
)

// startupRecentsMsg 只喂启动屏右卡。刻意不复用 historyItemsMsg：那份列表属于历史浏览
// 模式（enterHistoryMode 会清空 m.historyItems），两者生命周期不同 —— 共用一条消息
// 会让一次 /hist 把启动屏的 4 条换成 100 条。
type startupRecentsMsg struct {
	items  []startupRecentItem
	source startupRecentsSource
}

// startupRolloutLabel 给一条 rollout 会话找"能认出来"的标题。
//
// Preview 为空是常态（首条 user 消息超长被跳过、或这份文件只有 assistant 消息），
// 此时依次回退 AgentName（子 Agent 会话唯一的人话身份）与 SessionID（文件名解析结果）。
// 三者都空才返回空串，交给渲染侧兜底 —— 右卡不接受"只有序号和时间"的空行。
func startupRolloutLabel(s memory.RolloutSessionInfo) string {
	for _, cand := range []string{s.Preview, s.AgentName, s.SessionID} {
		if v := strings.TrimSpace(cand); v != "" {
			return v
		}
	}
	return ""
}

// startupRecentItemsFromRollout 把 rollout 清单投影成右卡条目。
// Empty 的会话直接滤掉：恢复它必然报 "no messages"，列出来等于点一次失败一次。
func startupRecentItemsFromRollout(sessions []memory.RolloutSessionInfo, limit int) []startupRecentItem {
	items := make([]startupRecentItem, 0, max(limit, 0))
	for _, s := range sessions {
		if limit > 0 && len(items) >= limit {
			break
		}
		if s.Empty {
			continue
		}
		items = append(items, startupRecentItem{
			Label: startupRolloutLabel(s),
			When:  s.ModTime,
			Turns: s.HeadMessages,
		})
	}
	return items
}

// startupRecentItemsFromTasks 把 tasks 账本投影成同一形状（回退路径用）。
func startupRecentItemsFromTasks(tasks []datamanager.TaskHistoryItem, limit int) []startupRecentItem {
	items := make([]startupRecentItem, 0, max(limit, 0))
	for _, task := range tasks {
		if limit > 0 && len(items) >= limit {
			break
		}
		when := task.UpdatedAt
		if when.IsZero() {
			when = task.CreatedAt
		}
		items = append(items, startupRecentItem{
			Label: task.Title,
			When:  when,
			Turns: task.MessageCount,
		})
	}
	return items
}

// startupRecentsFromRollout 把"rollout 账本的取数结果"落成一条启动屏消息。
// 读失败与过滤后为空都收敛成 source=None：调用方据此决定要不要回退 tasks。
func startupRecentsMsgFromRollout(sessions []memory.RolloutSessionInfo, err error, limit int) startupRecentsMsg {
	if err != nil {
		return startupRecentsMsg{source: startupRecentsFromNone}
	}
	items := startupRecentItemsFromRollout(sessions, limit)
	if len(items) == 0 {
		return startupRecentsMsg{source: startupRecentsFromNone}
	}
	return startupRecentsMsg{items: items, source: startupRecentsFromRollout}
}

// startupRecentsFromTasks 把"tasks 账本的取数结果"落成一条启动屏消息（回退路径）。
func startupRecentsMsgFromTasks(tasks []datamanager.TaskHistoryItem, err error, limit int) startupRecentsMsg {
	if err != nil {
		return startupRecentsMsg{source: startupRecentsFromNone}
	}
	items := startupRecentItemsFromTasks(tasks, limit)
	if len(items) == 0 {
		return startupRecentsMsg{source: startupRecentsFromNone}
	}
	return startupRecentsMsg{items: items, source: startupRecentsFromTasks}
}

// loadStartupRecentsCmd 启动时取一次最近会话（最多 4 条），**rollout 优先、tasks 兜底**。
//
// ① rollout 是 /resume 与断点续跑的事实来源，且 RolloutWriter 运行中持续写；tasks 副本
// 只在完成/flush 后落盘。同样是"最近"，rollout 的语义更准，而且右卡的 hint 指的就是
// /resume —— 两处读同一份账本，用户敲下去看到的就是卡上那几条。
// ② rollout root 还不存在（从没跑过任务）时 ListRolloutSessions 返回 error：这不算错误，
// 静默回退 tasks，并把 hint 换成 :hist（那条路径确实是 :hist 才能载入）。
// ③ 任何读取失败都按空态处理：启动屏不该因为历史文件读不到而弹错误 —— 那会盖住 banner。
func loadStartupRecentsCmd(m *model) tea.Cmd {
	return func() tea.Msg {
		sessions, rolloutErr := memory.ListRolloutSessions(memory.RolloutRootDir(), startupRecentsLimit)
		if msg := startupRecentsMsgFromRollout(sessions, rolloutErr, startupRecentsLimit); msg.source == startupRecentsFromRollout {
			return msg
		}
		if m.dataManager == nil {
			return startupRecentsMsg{source: startupRecentsFromNone}
		}
		tasks, tasksErr := m.dataManager.ListTaskHistoryFast(startupRecentsLimit)
		return startupRecentsMsgFromTasks(tasks, tasksErr, startupRecentsLimit)
	}
}

// startupRecentsFingerprint 是右卡内容的指纹，进启动屏 memo 的 key。跑完一轮回到空态时
// 宽度/高度/cwd 全都没变，但 startupRecents 换了内容 —— 没有它就会继续显示上一轮的列表。
// source 也进指纹：同一批条目换了账本，hint 文案跟着变，缓存必须失效。
func (m *model) startupRecentsFingerprint() string {
	if len(m.startupRecents) == 0 {
		return "0"
	}
	return fmt.Sprintf("%d:%d:%d", len(m.startupRecents), m.startupRecents[0].When.Unix(), m.startupRecentsSource)
}

// handleStartupRecentsMsg 落地启动屏的最近会话（条目与来源必须一起落地：
// 只更新 items 而丢掉 source，hint 就会退回默认分支说的还是 /resume）。
//
// 必须显式重排一次：空会话时 View() 的重排条件是"有脏条目 / 宽度变了 / 渲染缓存与条目数
// 不等"，三者都不成立，所以只清 memo 的话再没人来读 —— 启动屏会一直停在空态，直到用户
// 敲出第一条日志。有日志之后头部不再显示 recents，就不必为它重排整段正文。
func (m *model) handleStartupRecentsMsg(msg startupRecentsMsg) (*model, tea.Cmd) {
	m.startupRecents, m.startupRecentsSource = msg.items, msg.source
	m.welcomeCacheKey = ""
	if len(m.logEntries) == 0 {
		m.bodyRepaint(0)
	}
	m.viewportDirty = true
	return m, nil
}
