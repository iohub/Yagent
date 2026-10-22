package tui

import (
	"fmt"
	"regexp"
	"strings"

	"charm.land/lipgloss/v2"
)

const dashboardPanelWidth = 50

// dashSectionBgHex 是类别行横带的字面量。走 truecolor 而不是 256 索引：索引 16–255 会被
// 终端主题整段重映射（base16 一类方案就把 16–21 派给背景色），同一条带子在一台机器上是
// 深靛蓝、在另一台上可能成了浅色 —— 底色必须是自己说了算的确定值。
// 取值 = dashBorderFg（62 = 95,95,215）同相压到四成亮度：带子与边框是同一支颜色的深浅
// 两级，而不是新添的一种颜色。
const dashSectionBgHex = "#262656"

// ── Dashboard palette ──
// A restrained, cohesive accent set shared by the token/activity panels so the
// dashboard reads as a single modern surface rather than a stack of ad-hoc rows.
var (
	dashBorderFg  = lipgloss.Color("62")             // soft indigo frame
	dashAccent    = lipgloss.Color("141")            // lavender accent (header diamond, cache)
	dashTitleFg   = lipgloss.Color("250")            // near-white title
	dashLabelFg   = lipgloss.Color("244")            // dimmed metric labels
	dashInFg      = lipgloss.Color("111")            // input value (light blue)
	dashOutFg     = lipgloss.Color("114")            // output value (green)
	dashSepFg     = lipgloss.Color("238")            // hairline separators
	dashKeyFg     = lipgloss.Color("247")            // keycap text
	dashSectionBg = lipgloss.Color(dashSectionBgHex) // category-row band
	// dashBandDimFg 是横带上的次要文字（键帽说明、⧉ 行那个百分比）。带外同一角色用的是
	// 241/244，那两个压在这条底色上都不到 4.5:1，所以带上单独提一档。
	dashBandDimFg = lipgloss.Color("246")
)

// dashBand 给任意前景样式补上类别行横带的背景。背景必须逐段涂：lipgloss 在每个
// 内层样式的末尾发 \x1b[m，会把外层背景一并关掉（v2.0.2 实测不会重新注入），
// 所以"整行涂一次"的带子会在第一个彩色字之后断掉。
func dashBand(s lipgloss.Style) lipgloss.Style { return s.Background(dashSectionBg) }

var (
	dashBandGlyph = dashBand(lipgloss.NewStyle().Foreground(dashAccent).Bold(true))
	dashBandName  = dashBand(lipgloss.NewStyle().Foreground(dashTitleFg).Bold(true))
	dashBandDim   = dashBand(lipgloss.NewStyle().Foreground(dashBandDimFg))
	dashBandFill  = dashBand(lipgloss.NewStyle()) // 带子上的空白也是带子
)

// dashSectionRow 是三段类别行（◆ Tokens / ⚙ Activity / ⧉ COMPRESS）的唯一排版：
// 一条铺满 inner 的深色横带，glyph 走 lavender、名称走近白粗体，右端（键帽或徽标）
// 右对齐到带子末端。detail 是紧跟名称的细节段，可空。
// 连 right 都放不下时只留左段 —— 短一截带子好过把内容挤掉。
func dashSectionRow(glyph, name, detail, right string, inner int) string {
	left := dashBandGlyph.Render(glyph+" ") + dashBandName.Render(name)
	if detail != "" {
		left += dashBandFill.Render(" ") + detail
	}
	w := lipgloss.Width(left)
	pad := inner - w
	if right != "" {
		pad -= lipgloss.Width(right)
	}
	if pad < 0 {
		return left
	}
	return left + dashBandFill.Render(strings.Repeat(" ", pad)) + right
}

// renderKeycap 是键帽的唯一排版：亮键 + 斜体暗标签。
func renderKeycap(key, label string, keyStyle, labelStyle lipgloss.Style) string {
	return keyStyle.Render(" "+key+" ") + labelStyle.Render(label+" ")
}

// dashKeycap renders a subtle " alt+d label " hint: bright key + dimmed label.
func dashKeycap(key, label string) string {
	return renderKeycap(key, label,
		lipgloss.NewStyle().Foreground(dashKeyFg).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color("241")).Italic(true))
}

// dashBandKeycap 是压在类别行横带上的同一枚键帽。标签走 dashBandDimFg 而不是带外的
// 241：241 落在这条底色上只剩 3:1 不到的对比，"收缩"这种提示文字会糊成一片。
func dashBandKeycap(key, label string) string {
	return renderKeycap(key, label,
		dashBand(lipgloss.NewStyle().Foreground(dashKeyFg).Bold(true)),
		dashBand(lipgloss.NewStyle().Foreground(dashBandDimFg).Italic(true)))
}

// dashDot renders a coloured agent marker dot.
func dashDot(name string) string {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(AgentColor(name))).Render("●")
}

// hasTokenUsage 判断是否有任何可展示的用量。口径与面板的 Σ 一致：
// 用归一化输入，Anthropic 路径才不把缓存漏掉。
func (m *model) hasTokenUsage() bool {
	totalIn := normalizedInput(m.totalInputTokens, m.inputTokens, m.cacheReadInputTokens, m.cacheCreationInputTokens)
	return totalIn+m.outputTokens > 0
}

// dashboardVisible 判断宽屏模式下是否显示右上角 dashboard
func (m *model) dashboardVisible() bool {
	if m.dashboardCollapsed {
		return false
	}
	return m.termWidth >= 120 && (m.taskRunning || len(m.timelineEntries) > 0 || m.hasTokenUsage())
}

// toggleDashboard 切换右上角 dashboard 收缩/展开（alt+d）
func (m *model) toggleDashboard() {
	m.dashboardCollapsed = !m.dashboardCollapsed
	m.invalidateFooterCache()
}

// dashboardWidth 返回 dashboard 占用宽度（不可见时返回 0）
func (m *model) dashboardWidth() int {
	if !m.dashboardVisible() {
		return 0
	}
	// 保证 viewport 至少 40 列宽
	if m.termWidth-dashboardPanelWidth < 40 {
		return 0
	}
	return dashboardPanelWidth
}

var borderANSIRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// stripANSIForBorder strips ANSI escape sequences from a rendered line so that
// border character checks (HasPrefix "╰" etc.) work correctly.
func stripANSIForBorder(s string) string {
	return borderANSIRe.ReplaceAllString(s, "")
}

// timelineReserveRows 是给底部 Activity 段留的行数：分隔线 + 段标题 + 1 条事件 + 快捷键提示。
// 不留够这 4 行，运行中的时间线会在面板里整个消失（旧实现只留 1 行，实测会挤掉）。
const timelineReserveRows = 4

// renderDashboard 渲染右上角 dashboard（token 优先，timeline 次之）。
// width / height 为面板可用尺寸，返回恰好 height 行的字符串。
func (m *model) renderDashboard(width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}

	// 检查是否有正在运行的条目 — 有则跳过缓存以支持动画
	hasRunning := false
	for _, e := range m.timelineEntries {
		if e.Status == ToolStatusRunning {
			hasRunning = true
			break
		}
	}

	innerWidth := width - 4 // border(2) + padding(2)
	// 三段抢同一个**内容**高度。lipgloss 的 Height(h) 是含上下边框的总高，
	// 所以能放内容的只有 height-2 行；按 height 分配预算会多要 2 行，
	// 末尾那段（现在是 ⧉，改之前是 Activity 的最后几行）会被下面的高度收敛截断悄悄切掉。
	contentRows := height - 2
	if contentRows < 1 {
		contentRows = 1
	}

	cs := m.compressSnapshot()

	// ① token 段预算：给 Activity 留行，再给 ⧉ 段留它最低成本形态的 2 行
	// （空闲态也常驻，见 compress_panel.go 的 any()），但自己至少保 3 行（标题/Σ/⛁）。
	maxTokenRows := contentRows
	if len(m.timelineEntries) > 0 {
		maxTokenRows = contentRows - timelineReserveRows
	}
	maxTokenRows -= compressReserveRows
	if maxTokenRows < 3 {
		maxTokenRows = 3
	}

	snap := m.tokenSnapshot()
	tokenRows := buildTokenPanel(snap, tokenPanelSpec{
		innerWidth:  innerWidth,
		maxRows:     maxTokenRows,
		keycapKey:   "alt+d",
		keycapLabel: langManager.GetText("DashboardCollapseHint"),
	})

	// 缓存键必须覆盖所有会影响渲染的输入。tokenSnapshot 已按 TOTAL 稳定排序，
	// 所以直接拿它的数字串当签名即可。
	//
	// 尾部为什么必须带 ctx：压缩事件（尤其 emergency）可能既不改变 timeline 也不改变
	// 任何累计计数器，ContextTokens 只反映在 ⛁ 行上 —— 不进键的话面板会永远命中旧缓存。
	key := fmt.Sprintf("%d|%s|%d|%d|%s|ctx:%d/%d",
		len(m.timelineEntries), m.timelineLastID(), width, height,
		snap.signature(), snap.ctxTokens, snap.ctxWindow)
	// ⧉ 段的签名必须进键：压缩事件动的只有压缩统计与采样，Σ/RUN/TOTAL 计数器一个都不改，
	// 不进键的话压缩块会永远命中旧缓存 —— 事件到了、面板不刷新，编译测试都发现不了。
	key += "|cmp:" + cs.signature()

	if !hasRunning && key == m.dashboardCacheKey && m.dashboardCache != "" {
		return m.dashboardCache
	}

	lines := append([]string(nil), tokenRows...)

	// ② ⧉ 压缩段：位置在 Activity 之下、面板最后一段，但行预算必须先算 ——
	// 有 timeline 时先给它保底 timelineReserveRows（分隔线+标题+事件+提示），
	// 剩下的才轮到压缩段。装不下最低形态（分隔线+标题）时整段让位。
	//
	// 注意 maxRows<=0 在渲染器里是"不限高"，所以预算不足时必须跳过，不能把 0 传进去
	// —— 那会让压缩段反过来把 Activity 挤掉。
	var compressRows []string
	compressMax := contentRows - len(lines)
	if len(m.timelineEntries) > 0 {
		compressMax -= timelineReserveRows
	}
	if compressMax >= compressReserveRows {
		compressRows = buildCompressBlock(cs, compressPanelSpec{innerWidth: innerWidth, maxRows: compressMax})
	}

	// ── Timeline section ──
	tlAvailable := contentRows - len(lines) - len(compressRows)
	if len(m.timelineEntries) > 0 && tlAvailable > 2 {
		// Separator + title (2 rows)
		lines = append(lines, tokenSep(innerWidth))
		lines = append(lines, dashSectionRow("⚙", "Activity", "", "", innerWidth))

		// Timeline content —— 传 nil 而不是 m.anim：进度动画已经在底部栏的
		// spinner 段里了，46 列的面板再塞一条 20 列的进度条只会挤掉工具名。
		// 运行状态由节点亮色表达，面板这里只把事件串成 git 式的脊柱。
		content := RenderTimeline(m.timelineEntries, false, false, width-4, nil)
		if content != "" {
			tlLines := strings.Split(content, "\n")
			if len(tlLines) > 1 {
				content = strings.Join(tlLines[1:], "\n")
			} else {
				content = ""
			}
		}
		if content != "" {
			tlInnerLines := strings.Split(content, "\n")
			// Reserve 1 row for hint (separator+title+hint = 3 rows consumed from tlAvailable)
			maxTL := tlAvailable - 3
			if maxTL < 0 {
				maxTL = 0
			}
			if len(tlInnerLines) > maxTL {
				tlInnerLines = tlInnerLines[:maxTL]
				// 脊柱行成对出现在节点之后，截断后末尾可能剩半截线。
				if n := len(tlInnerLines); n > 0 && tlInnerLines[n-1] == timelineConnector() {
					tlInnerLines = tlInnerLines[:n-1]
				}
			}
			hint := dashKeycap("ctrl+l", langManager.GetText("TimelineDetailHint"))
			lines = append(lines, tlInnerLines...)
			lines = append(lines, hint)
		}
	}

	// ③ ⧉ 压缩段落在 Activity 之下，是面板的最后一段。行预算已在上面算好并预留。
	lines = append(lines, compressRows...)

	// Trim any lines that exceed innerWidth before applying border.
	// lipgloss v2 MaxWidth preserves ANSI styles and truncates by display width.
	for i, line := range lines {
		if lipgloss.Width(line) > innerWidth {
			lines[i] = lipgloss.NewStyle().MaxWidth(innerWidth).Render(line)
		}
	}

	// Apply rounded border — Height ensures lipgloss auto-pads short content
	// with bordered empty lines; we still truncate manually when content is too long.
	borderStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(dashBorderFg).
		Padding(0, 1).
		Width(width).
		Height(height)

	content := strings.Join(lines, "\n")
	rendered := borderStyle.Render(content)

	// Ensure exactly height lines
	renderedLines := strings.Split(rendered, "\n")
	if len(renderedLines) > height {
		renderedLines = renderedLines[:height]
	}
	// If truncation removed the bottom border, restore it with correct color
	if len(renderedLines) == height {
		last := stripANSIForBorder(renderedLines[height-1])
		if !strings.HasPrefix(last, "╰") {
			renderedLines[height-1] = lipgloss.NewStyle().Foreground(dashBorderFg).Render("╰" + strings.Repeat("─", width-2) + "╯")
		}
	}
	// Defensive width fixup: use lipgloss Width (preserves ANSI) rather than
	// plain string拼接 which would strip colors.
	for i := range renderedLines {
		if w := lipgloss.Width(renderedLines[i]); w != width {
			renderedLines[i] = lipgloss.NewStyle().Width(width).Render(renderedLines[i])
		}
	}

	result := strings.Join(renderedLines, "\n")

	m.dashboardCache = result
	m.dashboardCacheKey = key
	return result
}

// timelineLastID 取时间线最后一条的 ID，用于缓存键。
func (m *model) timelineLastID() string {
	if len(m.timelineEntries) == 0 {
		return ""
	}
	return m.timelineEntries[len(m.timelineEntries)-1].ID
}
