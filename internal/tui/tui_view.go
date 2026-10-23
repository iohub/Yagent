package tui

import (
	"fmt"
	"image/color"
	"os"
	"strings"
	"time"

	"yagent/internal/tui/common"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

const (
	powerlineRightSep = "\ue0b0"
	powerlineLeftSep  = "\ue0b2"
)

// 滚动条两段的配色是常量，没必要每帧重造样式。
var (
	scrollbarThumbStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("62"))
	scrollbarTrackStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("236"))
)

// bodyLayout 是正文那几列的唯一口径：视口行高、dashboard 列、滚动条列、内容列宽。
// View 与所有会改写 viewport 尺寸的路径（resize、切标签页）都必须从这里取值，
// 否则正文按 A 列渲染、按 B 列拼接，边界只能靠事后重排来擦。
type bodyLayout struct {
	height       int
	dashWidth    int
	scrollbarCol int
	contentWidth int
	totalLines   int
}

// computeBodyLayout 收 totalLines 而不是自己读 m.viewport：每个标签页各有一份内容，
// 溢不溢出（要不要占那一列滚动条）只有被改尺寸的那个 viewport 自己知道。
func (m *model) computeBodyLayout(totalLines int) bodyLayout {
	l := bodyLayout{
		height:     m.termHeight - m.computeFooterHeight() - tabBarHeight,
		dashWidth:  m.dashboardWidth(),
		totalLines: totalLines,
	}
	if l.height < 3 {
		l.height = 3
	}
	if totalLines > l.height {
		l.scrollbarCol = 1
	}
	l.contentWidth = m.termWidth - l.scrollbarCol - l.dashWidth
	return l
}

func makeRightSep(prevBg, nextBg color.Color) string {
	return lipgloss.NewStyle().
		Foreground(prevBg).
		Background(nextBg).
		Render(powerlineRightSep)
}

func makeRightSepToEnd(prevBg color.Color) string {
	return lipgloss.NewStyle().
		Foreground(prevBg).
		Render(powerlineRightSep)
}

func makeLeftSep(leftBg, rightBg color.Color) string {
	return lipgloss.NewStyle().
		Foreground(rightBg).
		Background(leftBg).
		Render(powerlineLeftSep)
}

func (m *model) View() tea.View {
	if m.quitting {
		return tea.View{AltScreen: true}
	}

	if m.termWidth <= 0 || m.termHeight <= 0 {
		return tea.View{AltScreen: true}
	}

	if m.setupActive && m.wizard != nil {
		return m.setupView()
	}

	if m.timelineFullscreenMode {
		return renderTimelineFullscreenView(m)
	}

	if m.thinklinkMode {
		return renderThinklinkFullscreenView(m)
	}

	if m.dialogStack != nil && m.dialogStack.Len() > 0 {
		overlay := m.dialogStack.Overlay(m.termWidth, m.termHeight)
		if overlay != "" {
			return tea.View{AltScreen: true, Content: overlay}
		}
	}

	if m.historyMode {
		return renderHistoryView(m)
	}

	if m.resumeMode {
		return renderResumeView(m)
	}

	var b strings.Builder

	body := m.computeBodyLayout(m.viewport.TotalLineCount())
	vpHeight := body.height
	if m.viewport.Height() != vpHeight {
		heightWasAtBottom := m.viewport.AtBottom()
		m.viewport.SetHeight(vpHeight)
		m.viewportViewValid = false
		if heightWasAtBottom {
			m.viewport.GotoBottom()
		}
	}

	// 宽度只在这里定一次：其他路径算错了就由这里纠正，并且必须让正文缓存作废，
	// 否则拼出来的列边界是旧宽度。
	if m.viewport.Width() != body.contentWidth {
		m.viewport.SetWidth(body.contentWidth)
		m.viewportViewValid = false
	}

	if m.hasDirtyEntries() || body.contentWidth != m.prevViewportWidth ||
		len(m.contentParts) != len(m.logEntries) {
		structural := body.contentWidth != m.prevViewportWidth ||
			len(m.contentParts) != len(m.logEntries)
		// 一次重排是 O(整个 transcript) 的活（拆块 + 面板 + 拼接 + 让 viewport 重新换行），
		// 而 bubbletea 每条消息都会调 View（tea.go 的 Update 后直接 render）。
		// 流式追加不在这里挡掉，重排次数就等于 flush 次数 —— 而 flush 是按 chunk 数
		// 触发的（见 aiChunkRenderThreshold），不是按时间。攒到下一拍再排。
		if !structural && m.streamDirtyHeld() {
			// viewportDirty 保持为真：tick（100ms）会再来问一次
		} else {
			wasAtBottom := m.viewport.AtBottom()
			m.rebuildContentCache()
			if wasAtBottom {
				m.viewport.GotoBottom()
			}
			m.viewportViewValid = false
			m.lastViewportRepaint = time.Now()
		}
	}

	if !m.viewportViewValid {
		m.syncSearchMatches()
		m.cachedViewportView = m.viewport.View()
		m.viewportViewValid = true
		m.prevViewportYOffset = m.viewport.YOffset()
		m.prevViewportHeight = m.viewport.Height()
	}

	if body.dashWidth > 0 {
		_ = m.renderDashboard(body.dashWidth, vpHeight)
	}

	b.WriteString(m.renderTabBar())
	b.WriteString("\n")

	if body.scrollbarCol == 0 && body.dashWidth == 0 {
		b.WriteString(m.cachedViewportView)
	} else {
		var scrollbar, dashboard string
		if body.scrollbarCol > 0 {
			scrollbar = common.Scrollbar(scrollbarThumbStyle, scrollbarTrackStyle,
				vpHeight, body.totalLines, vpHeight, m.viewport.YOffset())
		}
		if body.dashWidth > 0 {
			dashboard = m.dashboardCache
		}
		b.WriteString(joinColumns(m.cachedViewportView, scrollbar, dashboard))
	}

	if !m.dashboardVisible() && (m.taskRunning || len(m.timelineEntries) > 0) {
		timelineContent := m.renderTimelinePanel(m.termWidth)
		if timelineContent != "" {
			b.WriteString("\n")
			b.WriteString(timelineContent)
		}
	}

	// footer 由若干区块纵向拼接，每块自身不带首尾空行，块之间恰好一个 "\n"。
	// computeFooterHeight 按同样的顺序逐块累加行数，两处必须保持一致，
	// 否则 viewport 高度算错会让整屏在滚动时抖动。
	var blocks []string
	if m.commandMode {
		blocks = append(blocks, m.renderCmdlinePanel())
	} else {
		blocks = append(blocks, m.renderEditorPanel())
		if s := m.renderSkillSuggestions(); s != "" {
			blocks = append(blocks, s)
		}
	}
	if s := m.renderKeywordSuggestions(); s != "" {
		blocks = append(blocks, s)
	}
	if m.errMsg != "" {
		blocks = append(blocks,
			lipgloss.NewStyle().MarginLeft(2).Render(errorStyle.Render("✖ "+m.errMsg)))
	}
	if s := m.renderInfoLine(); s != "" {
		blocks = append(blocks, s)
	}
	if dash := m.tokenDashboardBlock(); dash != "" {
		blocks = append(blocks, dash)
	}

	statusBar := m.cachedStatusBar
	if !m.statusBarValid {
		statusBar = m.renderAirlineStatusBar()
	}

	// 两个 "\n\n" 的后一个才真正是空行：视口文本与 blocks 都不以换行结尾，
	// 单个 "\n" 只会终止上一行。computeFooterHeight 里的 "1 + blocks + 2"
	// 就是按 空行 / blocks / 空行 / 状态栏 四段算的。
	b.WriteString("\n\n")
	b.WriteString(strings.Join(blocks, "\n"))
	b.WriteString("\n\n")
	b.WriteString(statusBar)

	return tea.View{AltScreen: true, Content: b.String()}
}

// joinColumns 按行把若干列左右拼接。每一列都已由自己的渲染器补齐到列宽
// （viewport 的 View 内部就是 lipgloss.Width(w).Height(h)，见 TestViewportViewPadsEveryLine），
// 所以这里只拼接：再套一次 Width() 就是把同一件事做第二遍，还会把铺满列宽的行再折一道、
// 整屏多出一倍行数。比最长列短的列，缺的行留空。
func joinColumns(columns ...string) string {
	split := make([][]string, 0, len(columns))
	rows := 0
	for _, col := range columns {
		if col == "" {
			continue
		}
		lines := strings.Split(col, "\n")
		split = append(split, lines)
		rows = max(rows, len(lines))
	}
	joined := make([]string, rows)
	for _, lines := range split {
		for i, line := range lines {
			joined[i] += line
		}
	}
	return strings.Join(joined, "\n")
}

// renderSkillSuggestions 渲染技能补全列表（含末行操作提示），共 len+1 行。
func (m *model) renderSkillSuggestions() string {
	if !m.skillAutoComplete || len(m.skillSuggestions) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m.skillSuggestions))
	for i, item := range m.skillSuggestions {
		// 角标把"内置命令"与"技能"分开：两者混排且没有标记时，撞名看起来就像同一个东西。
		badge := "[cmd]"
		display := item.name + "  " + badge
		if item.kind == completionSkill {
			badge = "[skill]"
			display = item.name + "  " + badge
			if item.desc != "" {
				display += " — " + item.desc
			}
		}
		if i == m.skillSuggestionIdx {
			parts = append(parts, m.skillHighlightStyle.Render("▶ "+display))
		} else {
			parts = append(parts, m.skillSuggestionStyle.Render("  "+display))
		}
	}
	parts = append(parts, m.skillHintStyle.Render("Tab 切换  Enter 选择  Esc 关闭"))
	return strings.Join(parts, "\n")
}

// renderKeywordSuggestions 渲染关键词补全行（单行，横向排布）。
func (m *model) renderKeywordSuggestions() string {
	if !m.keywordAutoComplete || len(m.keywordSuggestions) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m.keywordSuggestions))
	for i, suggestion := range m.keywordSuggestions {
		if i == m.keywordSuggestionIdx {
			parts = append(parts, m.keywordHighlightStyle.Render(suggestion))
		} else {
			parts = append(parts, m.keywordSuggestionStyle.Render(suggestion))
		}
	}
	return strings.Join(parts, "  ")
}

func (m *model) renderTimelinePanel(width int) string {
	hasRunning := false
	for _, e := range m.timelineEntries {
		if e.Status == ToolStatusRunning {
			hasRunning = true
			break
		}
	}

	var lastID string
	if len(m.timelineEntries) > 0 {
		lastID = m.timelineEntries[len(m.timelineEntries)-1].ID
	}
	key := fmt.Sprintf("%d|%s|%d", len(m.timelineEntries), lastID, width)

	if !hasRunning && key == m.timelineCacheKey && m.timelineCache != "" {
		return m.timelineCache
	}

	// 进度动画已由底部 airline 状态栏的 spinner 段表达，
	// 此折叠模式下的时间线浮层不再重复渲染动画（与右上角 dashboard 面板一致）。
	content := RenderTimeline(m.timelineEntries, false, true, width-4, nil)
	if content == "" {
		return ""
	}

	lines := strings.Split(content, "\n")
	if len(lines) > 1 {
		content = strings.Join(lines[1:], "\n")
	} else {
		return ""
	}

	hint := timelineHintStyle.Render(" ctrl+l " + langManager.GetText("TimelineDetailHint") + " ")

	panelContent := content + "\n" + hint

	panelWidth := width - 2
	if panelWidth < 30 {
		panelWidth = 30
	}
	rendered := timelinePanelStyle.Width(panelWidth).Render(panelContent)

	m.timelineCache = rendered
	m.timelineCacheKey = key
	return m.timelineCache
}

func (m *model) renderAirlineStatusBar() string {
	width := m.termWidth
	if width <= 0 {
		width = 80
	}

	modeSeg, modeBg := m.airlineModeSegment()

	leftPart := modeSeg + makeRightSep(modeBg, airlineColorInfoBg)
	rightSegs := m.airlineRightSegments()

	// 右段组放不下时从组头逐个丢弃（丢的是低优先级的搜索/滚动信息，
	// 保留模型名与 spinner），而不是整组一起消失。
	rightPart, rightW := renderAirlineRight(width-lipgloss.Width(leftPart)-2, rightSegs)

	endSep := ""
	if rightPart == "" {
		endSep = makeRightSepToEnd(airlineColorInfoBg)
	}
	fillerW := width - lipgloss.Width(leftPart) - rightW - lipgloss.Width(endSep)
	if fillerW < 0 {
		fillerW = 0
	}
	// 中段只是一条纯色填充带：底部栏不写快捷键说明，它们分别挂在 tab 行
	// （全局开关）与面板底边框（当前模式的动作）上，这里只留模式与运行时信息。
	fillerPart := airlineFillerStyle.Width(fillerW).Render("")

	return leftPart + fillerPart + endSep + rightPart
}

// airlineModeSegment 返回状态栏最左的模式段与其背景色。
// 语义与输入面板一致：commandMode 就是 vim 的 NORMAL，能打字时是 INSERT
// —— 旧实现在非 commandMode 也显示 NORMAL，与"正在输入"的实际状态相矛盾。
func (m *model) airlineModeSegment() (string, color.Color) {
	chip := func(bg, fg color.Color) lipgloss.Style {
		return lipgloss.NewStyle().Background(bg).Foreground(fg).Bold(true).Padding(0, 1)
	}

	switch {
	case m.commandMode:
		return chip(airlineColorNormalBg, airlineColorNormalFg).Render("NORMAL"),
			airlineColorNormalBg

	case m.taskCancelled:
		return chip(airlineColorCancelBg, airlineColorCancelFg).Render("● CANCELLED"),
			airlineColorCancelBg

	case m.taskRunning:
		// padding 必须加在渐变外层：ApplyBoldForegroundGrad 是逐字着色，
		// 把带 Padding 的样式传进去会让每个字符各自缩进一次。
		grad := common.ApplyBoldForegroundGrad(
			lipgloss.NewStyle().Background(airlineColorRunBg),
			"● RUN",
			common.DefaultGradFrom,
			common.DefaultGradTo,
		)
		return lipgloss.NewStyle().Background(airlineColorRunBg).Padding(0, 1).Render(grad),
			airlineColorRunBg

	default:
		return chip(airlineColorInsertBg, airlineColorInsertFg).Render("INSERT"),
			airlineColorInsertBg
	}
}

// airlineSegment 是状态栏右对齐组里的一个色块。
type airlineSegment struct {
	bg, fg color.Color
	bold   bool
	text   string
}

func (s airlineSegment) render() string {
	st := lipgloss.NewStyle().Background(s.bg).Foreground(s.fg).Padding(0, 1)
	if s.bold {
		st = st.Bold(true)
	}
	return st.Render(s.text)
}

// airlineRightSegments 按优先级从低到高收集右段（放不下时从头部丢弃）：
// 搜索命中 → 滚动位置 → 路由模式 → 模型 → 当前 agent → spinner。
func (m *model) airlineRightSegments() []airlineSegment {
	var segs []airlineSegment

	if label := m.searchLabel(); label != "" {
		segs = append(segs, airlineSegment{
			bg: airlineColorSearchBg, fg: airlineColorSearchFg, bold: true, text: label,
		})
	}

	if ruler := m.viewportRuler(); ruler != "" {
		segs = append(segs, airlineSegment{
			bg: airlineColorInfoAltBg, fg: airlineColorInfoAltFg, text: ruler,
		})
	}

	if m.autoRouting {
		segs = append(segs, airlineSegment{
			bg: airlineColorRunBg, fg: airlineColorRunFg, bold: true,
			text: "⚡ AUTO",
		})
	}

	if model := m.airlineModelLabel(); model != "" {
		segs = append(segs, airlineSegment{
			bg: airlineColorAccentBg, fg: airlineColorAccentFg, text: model,
		})
	}

	if m.taskRunning {
		if m.currentAgent != "" {
			segs = append(segs, airlineSegment{
				bg: airlineColorInfoAltBg, fg: airlineColorInfoAltFg, text: m.currentAgent,
			})
		}
		// 暂停时 spinner 是假的"还在干活"：换成暂停标记，并区分"正在停"与"已停住"
		// —— 一个在飞的工具最长要等完 defaultToolTimeout(180s) 才到 step 边界。
		if label := m.airlinePauseLabel(); label != "" {
			segs = append(segs, airlineSegment{
				bg: airlineColorRunBg, fg: airlineColorRunFg, bold: true, text: label,
			})
		} else {
			segs = append(segs, airlineSegment{
				bg: airlineColorInfoBg, fg: airlineColorInfoFg, text: m.anim.Render(),
			})
		}
	}

	return segs
}

// airlinePauseLabel 只在闸门按下时给文案；未暂停返回空串（该段整条不出现）。
func (m *model) airlinePauseLabel() string {
	gate := m.currentTask.Gate()
	if gate == nil || !gate.Paused() {
		return ""
	}
	if gate.Blocked() {
		return "⏸ PAUSED"
	}
	return "⏸ PAUSING"
}

// viewportRuler 是 vim ruler 式的滚动位置指示：贴边时直接说 TOP/BOT，
// 避免内容不足一屏时百分比恒为 0% 造成的误导。
func (m *model) viewportRuler() string {
	total := m.viewport.TotalLineCount()
	if total == 0 {
		return ""
	}
	switch {
	case m.viewport.AtTop():
		return "TOP"
	case m.viewport.AtBottom():
		return "BOT"
	default:
		return fmt.Sprintf("%d%%", int(m.viewport.ScrollPercent()*100))
	}
}

func (m *model) airlineModelLabel() string {
	switch {
	case m.currentProvider != "" && m.currentModel != "":
		return m.currentProvider + "/" + m.currentModel
	case m.currentModel != "":
		return m.currentModel
	default:
		return m.currentProvider
	}
}

// renderAirlineRight 渲染右对齐 segment 组（含 ◀ 衔接符），从组头丢弃放不下的项。
// 返回渲染结果与其占用宽度；全部放不下时返回 ("", 0)。
func renderAirlineRight(maxWidth int, segs []airlineSegment) (string, int) {
	for len(segs) > 0 {
		w := airlineRightWidth(segs)
		if w <= maxWidth {
			return buildAirlineRight(segs), w
		}
		segs = segs[1:]
	}
	return "", 0
}

func airlineRightWidth(segs []airlineSegment) int {
	total := 1 // 与填充区衔接的 ◀
	for i, s := range segs {
		total += lipgloss.Width(s.render())
		if i < len(segs)-1 {
			total++ // 段与段之间的 ◀
		}
	}
	return total
}

func buildAirlineRight(segs []airlineSegment) string {
	var b strings.Builder
	b.WriteString(makeLeftSep(airlineColorInfoBg, segs[0].bg))
	for i, s := range segs {
		b.WriteString(s.render())
		if i < len(segs)-1 {
			b.WriteString(makeLeftSep(s.bg, segs[i+1].bg))
		}
	}
	return b.String()
}

// renderWelcomePanel 是 transcript 顶部那块固定头部（banner + tagline + cwd）。
// 每次整段重排它都会被 assembleViewportContent 要一遍，而它的输入只有宽度、cwd 和
// "有没有日志" —— 不缓存就是每个刷新都把 banner 的逐字渐变与面板排版重算一遍。
func (m *model) renderWelcomePanel() string {
	empty := len(m.logEntries) == 0

	// 已有日志时头部只跟字段宽度与 cwd 有关；空白启动屏还要行高（它要垂直居中）。
	// 行高别在头部分支算：computeFooterHeight 会走 token 面板与 langManager，
	// 那条路上本来不需要它。
	key := fmt.Sprintf("header|%d|%s", m.computeFieldWidth(), m.projectDir)
	if empty {
		width, height := m.startupScreenSize()
		key = fmt.Sprintf("startup|%d|%d|%s", width, height, m.projectDir)
	}
	if m.welcomeCacheKey == key {
		return m.welcomeCache
	}

	out := m.renderWelcomePanelLayout()
	if empty {
		width, height := m.startupScreenSize()
		out = m.renderCenteredStartupScreen(width, height)
	}
	m.welcomeCacheKey, m.welcomeCache = key, out
	return out
}

// startupScreenSize 是空白启动屏的画布：宽度兜底 80 列，行高至少 8 行才放得下 banner。
func (m *model) startupScreenSize() (width, height int) {
	width = m.termWidth
	if width <= 0 {
		width = 80
	}
	height = m.termHeight - m.computeFooterHeight()
	if height < 8 {
		height = 8
	}
	return width, height
}

func (m *model) renderCenteredStartupScreen(width, height int) string {
	banner := renderBanner()

	cwd := m.projectDir
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(cwd, home) {
		cwd = "~" + strings.TrimPrefix(cwd, home)
	}

	var block strings.Builder
	block.WriteString(banner)
	block.WriteString("\n")
	tagline := common.ApplyGrad("A Repository-Aware, Self-Evolving Agent")
	block.WriteString(tagline)
	block.WriteString("\n\n")
	block.WriteString(welcomeSubStyle.Render(cwd))

	return lipgloss.Place(
		width,
		height,
		lipgloss.Center,
		lipgloss.Center,
		block.String(),
	)
}

func (m *model) renderWelcomePanelLayout() string {
	cwd := m.projectDir
	home, _ := os.UserHomeDir()
	if strings.HasPrefix(cwd, home) {
		cwd = "~" + strings.TrimPrefix(cwd, home)
	}
	cwdLine := welcomeSubStyle.Render(cwd)

	banner := renderBanner()
	var left strings.Builder
	left.WriteString(banner)
	left.WriteString("\n\n")

	leftContent := left.String()

	var right strings.Builder
	tagline := common.ApplyGrad("─── A Repository-Aware, Self-Evolving Agent")
	right.WriteString(tagline)
	right.WriteString("\n")

	panelWidth := m.computeFieldWidth() + 4
	innerWidth := panelWidth - 4
	leftWidth := max(lipgloss.Width(banner), 38)
	narrowThreshold := max(65, leftWidth+23)
	if innerWidth < narrowThreshold {
		boxInner := leftContent + "\n\n" + welcomeDimStyle.Render(strings.Repeat("─", 38)) + "\n\n" + right.String()
		boxInner += "\n\n" + cwdLine
		return welcomePanelStyle.Width(innerWidth).Render(boxInner)
	}
	rightWidth := innerWidth - leftWidth - 3
	if rightWidth < 20 {
		rightWidth = 20
	}

	separator := welcomeDimStyle.Render(" │ ")

	leftStyled := lipgloss.NewStyle().Width(leftWidth).Render(leftContent)
	rightStyled := lipgloss.NewStyle().Width(rightWidth).Render(right.String())

	inner := lipgloss.JoinHorizontal(lipgloss.Top, leftStyled, separator, rightStyled)
	inner += "\n\n" + cwdLine
	return welcomePanelStyle.Width(innerWidth).Render(inner)
}
func renderBanner() string {
	asciiLogo := []string{
		"██  ██   ▄████▄  ▄▄▄▄ ▄▄▄▄▄ ▄▄  ▄▄ ▄▄▄▄▄▄",
		" ▀██▀    ██▄▄██ ██ ▄▄ ██▄▄  ███▄██   ██",
		"  ██     ██  ██ ▀███▀ ██▄▄▄ ██ ▀██   ██",
	}

	gradFrom := common.DefaultGradFrom
	gradTo := common.DefaultGradTo

	var rendered []string
	for _, line := range asciiLogo {
		gradLine := common.ApplyBoldForegroundGrad(
			lipgloss.NewStyle(),
			line,
			gradFrom,
			gradTo,
		)
		rendered = append(rendered, gradLine)
	}
	return bannerPadStyle.Render(lipgloss.JoinVertical(lipgloss.Left, rendered...))
}
