package tui

import (
	"fmt"
	"image/color"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
)

// Token 面板的唯一口径与排版层。
//
// 侧栏 dashboard、底部面板、底部折叠三个表面都经由 buildTokenPanel 产出；
// 数字只在 tokenSnapshot 里算一次，排版层不碰 model 上的任何计数器 —— 这是
// 之前三个表面各自取数、同一个 "In" 给出两种口径的根因。

// 列网格：dot+name+gap+run+arrow+total+gap 固定，余量全部给 ⊕ CACHE 段。
const (
	tokenColDot   = 2
	tokenColName  = 9
	tokenColGap   = 1
	tokenColRun   = 7
	tokenColArrow = 3
	tokenColTotal = 7
	tokenColCache = 16
	// tokenColsLeft = dot+name+gap+run+arrow+total+gap；inner=46 时余量正好是基准的 16 列。
	tokenColsLeft = tokenColDot + tokenColName + tokenColGap + tokenColRun + tokenColArrow + tokenColTotal + tokenColGap
)

// 宽度阶梯：低于门槛逐级去细节，高于门槛逐级补细节。inner=46（侧栏恒定）落在中间档。
const (
	tokenNoBarBelow = 40 // 低于此宽度：计量条不带进度条
	tokenHeaderFrom = 46 // 从此宽度起：显示列头
	// tokenFlowFrom：从此宽度起 agent 按 46 列一段横排。
	// 门槛与列头同值 —— 侧栏（inner 恒 46）就是这段几何的原点，
	// 再宽也只是多排几段，不会因为把缓存列拉满而在 TOTAL 与 ⊕ 之间掏出几十列空白。
	tokenFlowFrom       = 46
	tokenMeterCells     = 10
	tokenMeterCellsMid  = 16
	tokenMeterCellsWide = 20
	// 计量条格数的升档门槛（与上面的横条排布门槛无关，各自独立）
	tokenMeterCellsMidFrom  = 60
	tokenMeterCellsWideFrom = 90
)

// 高度阶梯的降级档位（每档只砍固定行里的一项；行数才是预算单位，行内裁剪不省行）。
const (
	tokenTierFull          = 0 // 完整
	tokenTierNoHeader      = 1 // 砍列头
	tokenTierNoSep         = 2 // 砍 agent 段前的分隔线
	tokenTierCacheInline   = 3 // 砍 ⊕ 计量条，命中率内联进 Σ 行
	tokenTierAgentsSummary = 4 // 全部 agent 折成一行 "┈ +N agents"
	tokenTierNoSep1        = 5 // 砍标题后的分隔线：极限 3 行（标题 / Σ / ⛁）
	tokenTierLast          = tokenTierNoSep1
)

var (
	tokenLabelStyle  = lipgloss.NewStyle().Foreground(dashLabelFg)
	tokenInStyle     = lipgloss.NewStyle().Foreground(dashInFg).Bold(true)
	tokenOutStyle    = lipgloss.NewStyle().Foreground(dashOutFg).Bold(true)
	tokenSigmaStyle  = lipgloss.NewStyle().Foreground(dashTitleFg).Bold(true)
	tokenTotalStyle  = lipgloss.NewStyle().Foreground(dashTitleFg).Bold(true)
	tokenRunStyle    = lipgloss.NewStyle().Foreground(dashInFg).Bold(true)
	tokenAccentStyle = lipgloss.NewStyle().Foreground(dashAccent)
	tokenSepStyle    = lipgloss.NewStyle().Foreground(dashSepFg)
	tokenMoreStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("243")).Italic(true)
	tokenHeaderStyle = lipgloss.NewStyle().Foreground(dashLabelFg).Bold(true)
)

// formatToken 把 token 数缩写成面板里显示的字符串。
// m 档用 2 位小数：Σ 与 in 常常只差几个百分点，1 位小数会让两者印成同一个数。
func formatToken(n int64) string {
	switch {
	case n >= 1000000:
		return fmt.Sprintf("%.2fm", float64(n)/1000000)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// padLeft 在左侧补空格（右对齐用）；入参可以是已上色的字符串。
func padLeft(s string, width int) string {
	if n := width - lipgloss.Width(s); n > 0 {
		return strings.Repeat(" ", n) + s
	}
	return s
}

// padRight 在右侧补空格（左对齐到固定列宽）。
func padRight(s string, width int) string {
	if n := width - lipgloss.Width(s); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

func padCenter(s string, width int) string {
	n := width - lipgloss.Width(s)
	if n <= 0 {
		return s
	}
	left := n / 2
	return strings.Repeat(" ", left) + s + strings.Repeat(" ", n-left)
}

// truncateCells 按显示宽度截断 —— 按字节或按 rune 都会把中文 agent 名的列宽算错。
func truncateCells(s string, width int) string {
	if width <= 0 || lipgloss.Width(s) <= width {
		return s
	}
	var out strings.Builder
	used := 0
	for _, r := range s {
		cw := lipgloss.Width(string(r))
		if used+cw > width-1 {
			break
		}
		out.WriteRune(r)
		used += cw
	}
	return out.String() + "…"
}

// normalizedInput 取 provider 归一化后的输入总量：OpenAI 的 prompt_tokens 已含 cached，
// Anthropic 的 input_tokens 不含 cache，只有 TotalInputTokens 在两条路径上含义一致。
// total 为 0 表示上游没给（估算路径 / 老 rollout），回落到三项相加。
func normalizedInput(total, prompt, cacheRead, cacheNew int64) int64 {
	if total > 0 {
		return total
	}
	return prompt + cacheRead + cacheNew
}

// cacheHitRatio 返回命中率（0..1）。ok=false 表示没有可展示的缓存活动。
func cacheHitRatio(cacheRead, cacheNew, totalIn int64) (float64, bool) {
	if totalIn <= 0 || (cacheRead <= 0 && cacheNew <= 0) {
		return 0, false
	}
	return float64(cacheRead) / float64(totalIn), true
}

func ratioPct(ratio float64) string {
	if ratio > 1 {
		ratio = 1
	}
	if ratio < 0 {
		ratio = 0
	}
	return fmt.Sprintf("%.0f%%", ratio*100)
}

// agentTokens 是单个 agent 的两级视角：本轮用量与会话累计。
type agentTokens struct {
	name      string
	runIn     int64
	runOut    int64
	totalIn   int64
	totalOut  int64
	cacheRead int64
	cacheNew  int64
}

func (a agentTokens) run() int64   { return a.runIn + a.runOut }
func (a agentTokens) total() int64 { return a.totalIn + a.totalOut }

// tokenSnapshot 是渲染器的唯一输入。
type tokenSnapshot struct {
	totalIn   int64
	totalOut  int64
	cacheRead int64
	cacheNew  int64
	ctxTokens int64
	ctxWindow int
	agents    []agentTokens // 已按 TOTAL 降序（同值按名字），保证渲染与缓存键稳定
	active    agentTokens   // 活跃 agent 的那一行；name 为空表示没有活跃 agent
}

func (s tokenSnapshot) hasUsage() bool { return s.totalIn+s.totalOut > 0 }

// signature 把快照里的全部数字摊平成一行字符串，用作侧栏渲染的缓存键。
// 依赖 agents 已稳定排序 —— 否则 map 迭代顺序会让键每次都变。
func (s tokenSnapshot) signature() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d/%d/%d/%d", s.totalIn, s.totalOut, s.cacheRead, s.cacheNew)
	for _, a := range s.agents {
		fmt.Fprintf(&b, " %s=%d/%d/%d/%d/%d/%d", a.name,
			a.runIn, a.runOut, a.totalIn, a.totalOut, a.cacheRead, a.cacheNew)
	}
	return b.String()
}

// tokenSnapshot 从 model 的计数器组装渲染输入。
func (m *model) tokenSnapshot() tokenSnapshot {
	snap := tokenSnapshot{
		totalIn:   normalizedInput(m.totalInputTokens, m.inputTokens, m.cacheReadInputTokens, m.cacheCreationInputTokens),
		totalOut:  m.outputTokens,
		cacheRead: m.cacheReadInputTokens,
		cacheNew:  m.cacheCreationInputTokens,
		ctxWindow: m.contextWindow,
	}
	if rt := m.runTokensPerAgent[m.runAgent]; rt != nil {
		snap.ctxTokens = rt.ContextTokens
	}
	for name, au := range m.tokenUsagePerAgent {
		at := agentTokens{
			name:      name,
			totalIn:   normalizedInput(au.TotalInputTokens, au.InputTokens, au.CacheReadInputTokens, au.CacheCreationInputTokens),
			totalOut:  au.OutputTokens,
			cacheRead: au.CacheReadInputTokens,
			cacheNew:  au.CacheCreationInputTokens,
		}
		if rt := m.runTokensPerAgent[name]; rt != nil {
			at.runIn = normalizedInput(rt.TotalInputTokens, rt.InputTokens, rt.CacheReadInputTokens, rt.CacheCreationInputTokens)
			at.runOut = rt.OutputTokens
		}
		if at.total() == 0 && at.run() == 0 {
			continue
		}
		snap.agents = append(snap.agents, at)
		if name == m.runAgent {
			snap.active = at
		}
	}
	sort.Slice(snap.agents, func(i, j int) bool {
		if ti, tj := snap.agents[i].total(), snap.agents[j].total(); ti != tj {
			return ti > tj
		}
		return snap.agents[i].name < snap.agents[j].name
	})
	return snap
}

// ── 本轮（run）用量存取 ──

func (m *model) runTokens(agent string) *AgentRunTokens {
	if m.runTokensPerAgent == nil {
		m.runTokensPerAgent = make(map[string]*AgentRunTokens)
	}
	if agent == "" {
		agent = "Unknown"
	}
	rt, ok := m.runTokensPerAgent[agent]
	if !ok {
		rt = &AgentRunTokens{AgentName: agent}
		m.runTokensPerAgent[agent] = rt
	}
	return rt
}

// currentRun 是 ⛁ 行的数据源：当前活跃 agent 的本轮用量。
func (m *model) currentRun() *AgentRunTokens { return m.runTokens(m.runAgent) }

// setRun 整体替换某个 agent 的本轮用量并把它设为活跃 run。
func (m *model) setRun(rt AgentRunTokens) {
	if rt.AgentName == "" {
		rt.AgentName = "Unknown"
	}
	copied := rt
	m.runTokens(rt.AgentName)
	*m.runTokensPerAgent[rt.AgentName] = copied
	m.runAgent = rt.AgentName
}

// resetRunTokens 在每次用户提交时清掉 RUN 列；Σ 与每 agent 的 TOTAL 不受影响。
func (m *model) resetRunTokens() {
	m.runTokensPerAgent = make(map[string]*AgentRunTokens)
	m.runAgent = ""
	m.invalidateFooterCache()
}

// setContextTokens 是 ContextTokens 的唯一写入者。
//
// 覆写（不是累加）该 agent 的上下文占用：ContextTokens 的含义是"最近一次调用的输入规模"，
// 压缩事件之后必须立刻反映压缩结果。顺带采一个趋势点给 ⧉ 段的 sparkline。
func (m *model) setContextTokens(agentName string, contextTokens int64) {
	rt := m.runTokens(agentName)
	m.runAgent = rt.AgentName
	rt.ContextTokens = contextTokens
	m.compress.sampleContext(contextTokens)
	m.invalidateFooterCache()
}

// ── 排版 ──

type tokenPanelSpec struct {
	innerWidth int
	maxRows    int // 0 = 不限高
	keycap     string
	collapsed  bool
}

// buildTokenPanel 产出 token 段的若干行（不含边框），每行显示宽度 <= spec.innerWidth。
// 无用量时返回 nil，调用方据此整段跳过。
func buildTokenPanel(snap tokenSnapshot, spec tokenPanelSpec) []string {
	if spec.innerWidth <= 0 || !snap.hasUsage() {
		return nil
	}
	if spec.collapsed {
		return []string{collapsedTokenLine(snap, spec)}
	}
	for tier := tokenTierFull; ; tier++ {
		rows := renderTokenTier(snap, spec, tier)
		if spec.maxRows <= 0 || len(rows) <= spec.maxRows || tier >= tokenTierLast {
			return rows
		}
	}
}

// 计量条格数随面板宽度增长：40–59 → 10 格，60–89 → 16 格，≥90 → 20 格。
// 侧栏恒为 inner=46，所以批准的 S1 视觉稿落在 10 格档。
func tokenMeterCellsFor(inner int) int {
	switch {
	case inner < tokenNoBarBelow:
		return 0
	case inner >= tokenMeterCellsWideFrom:
		return tokenMeterCellsWide
	case inner >= tokenMeterCellsMidFrom:
		return tokenMeterCellsMid
	default:
		return tokenMeterCells
	}
}

func tokenSep(inner int) string {
	return tokenSepStyle.Render(strings.Repeat("─", inner))
}

func meterColorStyle(c color.Color) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(c).Bold(true)
}

func fillBar(ratio float64, cells int) string {
	if ratio > 1 {
		ratio = 1
	}
	if ratio < 0 {
		ratio = 0
	}
	filled := int(ratio*float64(cells) + 0.5)
	if filled > cells {
		filled = cells
	}
	return strings.Repeat("▰", filled) + strings.Repeat("▱", cells-filled)
}

// contextBarColor 沿用原 ⛁ 行的用量梯度：<60% 绿、60–85% 黄、>85% 红。
func contextBarColor(pct float64) color.Color {
	switch {
	case pct > 85:
		return lipgloss.Color("203")
	case pct >= 60:
		return lipgloss.Color("214")
	default:
		return lipgloss.Color("79")
	}
}

// meterLine 组装计量条："<glyph> <bar> <pct> …右对齐明细…"；cells<=0 时不带进度条。
// 左段对 ⊕ 与 ⛁ 完全同构，两条的 bar 起点与 pct 因此天然对齐。
// 明细右对齐到 inner，所以返回值恒为 inner 宽 —— 只能整行使用；
// 要塞进一行摘要里请用 meterInline。
func meterLine(glyph string, ratio float64, detail string, inner, cells int, meterColor color.Color) string {
	left := meterInline(glyph, ratio, "", cells, meterColor)
	if detail == "" {
		return left
	}
	if lipgloss.Width(left)+1+lipgloss.Width(detail) > inner {
		return left
	}
	return padRight(left, inner-lipgloss.Width(detail)) + detail
}

// meterInline 是不拉伸的计量条片段，供折叠行这类"一行装三段"的场合复用。
func meterInline(glyph string, ratio float64, detail string, cells int, meterColor color.Color) string {
	left := tokenAccentStyle.Render(glyph) + " "
	if cells > 0 {
		left += meterColorStyle(meterColor).Render(fillBar(ratio, cells)) + " "
	}
	left += meterColorStyle(meterColor).Render(ratioPct(ratio))
	if detail == "" {
		return left
	}
	return left + " " + detail
}

func tokenTitleRow(keycap string, inner int) string {
	left := lipgloss.NewStyle().Foreground(dashAccent).Bold(true).Render("◆ ") +
		lipgloss.NewStyle().Foreground(dashTitleFg).Bold(true).Render("Tokens")
	if keycap == "" {
		return left
	}
	if lipgloss.Width(left)+1+lipgloss.Width(keycap) > inner {
		return left
	}
	return padRight(left, inner-lipgloss.Width(keycap)) + keycap
}

// tokenSigmaRow 是英雄行：Σ 总量左置，in/out 明细右对齐；高度不足时明细里换上命中率。
func tokenSigmaRow(snap tokenSnapshot, inner int, withDetail bool, inlineRate float64, inlineCache bool) string {
	left := tokenLabelStyle.Render("Σ ") + tokenSigmaStyle.Render(formatToken(snap.totalIn+snap.totalOut))
	detail := ""
	if withDetail {
		sep := " · "
		if inner < tokenNoBarBelow {
			sep = "·"
		}
		detail = tokenLabelStyle.Render("in ") + tokenInStyle.Render(formatToken(snap.totalIn)) +
			tokenLabelStyle.Render(sep+"out ") + tokenOutStyle.Render(formatToken(snap.totalOut))
	}
	if inlineCache {
		detail += tokenAccentStyle.Render("  ⊕" + ratioPct(inlineRate))
	}
	if detail == "" || lipgloss.Width(left)+1+lipgloss.Width(detail) > inner {
		return left
	}
	return padRight(left, inner-lipgloss.Width(detail)) + detail
}

func tokenCacheMeterRow(snap tokenSnapshot, inner, cells int) string {
	ratio, ok := cacheHitRatio(snap.cacheRead, snap.cacheNew, snap.totalIn)
	if !ok {
		return ""
	}
	detail := tokenLabelStyle.Render("hit ") + tokenAccentStyle.Render(formatToken(snap.cacheRead))
	if snap.cacheNew > 0 {
		detail += tokenLabelStyle.Render(" · new ") + tokenAccentStyle.Render(formatToken(snap.cacheNew))
	}
	return meterLine("⊕", ratio, detail, inner, cells, lipgloss.Color("141"))
}

func tokenCtxMeterRow(snap tokenSnapshot, inner, cells int) string {
	ratio, detail, barColor, ok := ctxMeterParts(snap)
	if !ok {
		return ""
	}
	return meterLine("⛁", ratio, detail, inner, cells, barColor)
}

// ctxMeterParts 拆出 ⛁ 行的三段，供整行与折叠行共用。
func ctxMeterParts(snap tokenSnapshot) (ratio float64, detail string, barColor color.Color, ok bool) {
	if snap.ctxTokens <= 0 {
		return 0, "", nil, false
	}
	if snap.ctxWindow > 0 {
		ratio = float64(snap.ctxTokens) / float64(snap.ctxWindow)
	}
	detail = tokenLabelStyle.Render(formatToken(snap.ctxTokens))
	if snap.ctxWindow > 0 {
		detail += tokenLabelStyle.Render(" / ") + tokenLabelStyle.Render(formatToken(int64(snap.ctxWindow)))
	}
	return ratio, detail, contextBarColor(ratio * 100), true
}

func tokenColHeader(cacheCol int) string {
	return padRight("", tokenColDot) +
		padRight(tokenHeaderStyle.Render("AGENT"), tokenColName) + " " +
		padLeft(tokenHeaderStyle.Render("RUN"), tokenColRun) +
		padCenter(tokenSepStyle.Render("▸"), tokenColArrow) +
		padLeft(tokenHeaderStyle.Render("TOTAL"), tokenColTotal) + " " +
		padLeft(tokenHeaderStyle.Render("⊕ CACHE"), cacheCol)
}

// tokenAgentRow 渲染一行 agent：本轮 RUN ▸ 累计 TOTAL，右端是该 agent 的命中率。
// 缓存明细（hit/new）只在 ⛁/⊕ 那条整行计量条上出现一次，agent 行只给百分比。
func tokenAgentRow(a agentTokens, cacheCol int) string {
	name := padRight(dashNameStyle(a.name).Render(truncateCells(a.name, tokenColName)), tokenColName)
	run := padLeft(tokenRunStyle.Render(formatToken(a.run())), tokenColRun)
	total := padLeft(tokenTotalStyle.Render(formatToken(a.total())), tokenColTotal)

	head := dashDot(a.name) + " " + name + " " + run + padCenter(tokenSepStyle.Render("▸"), tokenColArrow) + total
	ratio, ok := cacheHitRatio(a.cacheRead, a.cacheNew, a.totalIn)
	if !ok {
		return head
	}
	return head + " " + padLeft(tokenAccentStyle.Render("⊕"+ratioPct(ratio)), cacheCol)
}

func dashNameStyle(agent string) lipgloss.Style {
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(AgentColor(agent)))
}

// renderTokenTier 按降级档位渲染；tier 越高，固定行越少。
func renderTokenTier(snap tokenSnapshot, spec tokenPanelSpec, tier int) []string {
	inner := spec.innerWidth
	cells := tokenMeterCellsFor(inner)
	cacheRate, cacheOK := cacheHitRatio(snap.cacheRead, snap.cacheNew, snap.totalIn)
	inlineCache := tier >= tokenTierCacheInline && cacheOK

	var rows []string
	rows = append(rows, tokenTitleRow(spec.keycap, inner))
	if tier < tokenTierNoSep1 {
		rows = append(rows, tokenSep(inner))
	}
	rows = append(rows, tokenSigmaRow(snap, inner, tier < tokenTierLast, cacheRate, inlineCache))
	if cacheOK && !inlineCache {
		rows = append(rows, tokenCacheMeterRow(snap, inner, cells))
	}
	if ctx := tokenCtxMeterRow(snap, inner, cells); ctx != "" {
		rows = append(rows, ctx)
	}
	return append(rows, tokenAgentBlock(snap, spec, tier, inner, len(rows))...)
}

// agentFlowPerRow 是宽档每行能塞下的 agent 段数
// （段宽 = 网格 + 基准缓存列，段间留 2 列）。
func agentFlowPerRow(inner int) int {
	n := inner / (tokenColsLeft + tokenColCache + 2)
	if n < 1 {
		return 1
	}
	return n
}

// tokenAgentBlock 负责 agent 行的三种排布：横条（宽）/ 单列（中）/ 截断（窄）。
// fixedRows 是调用方已产出的行数：预算必须连着固定行一起算，否则极限档会超行。
//
// 顺序很重要：先算能不能装下"至少一行 agent 内容"，装得下才产出分隔线与列头 ——
// 反过来会画出"有列头、下面空着"的残缺表格。
func tokenAgentBlock(snap tokenSnapshot, spec tokenPanelSpec, tier, inner, fixedRows int) []string {
	if len(snap.agents) == 0 {
		return nil
	}
	flow := inner >= tokenFlowFrom

	wantSep := tier < tokenTierNoSep
	wantHeader := tier < tokenTierNoHeader && inner >= tokenHeaderFrom
	summary := tier >= tokenTierAgentsSummary // 高度极限档：所有 agent 折成一行
	if summary {
		wantHeader = false
	}

	limit := len(snap.agents)
	more := false
	if spec.maxRows > 0 {
		budget := spec.maxRows - fixedRows
		if budget <= 0 {
			return nil
		}
		// 先给分隔线与列头留位；留不出内容行就逐级让位（先列头后分隔线）
		overhead := 0
		if wantSep {
			overhead++
		}
		if wantHeader {
			overhead++
		}
		for budget-overhead < 1 {
			if wantHeader {
				wantHeader = false
			} else if wantSep {
				wantSep = false
			} else {
				return nil
			}
			overhead = 0
			if wantSep {
				overhead++
			}
			if wantHeader {
				overhead++
			}
		}
		room := budget - overhead
		if summary {
			// 摘要只占一行：留不下"分隔线+摘要"两行时，连分隔线也省掉
			if room < 1 {
				return nil
			}
			if wantSep && room < 2 {
				wantSep = false
			}
			limit = 0
		} else if room < limit {
			if room >= 2 {
				limit = room - 1
				more = true
			} else {
				limit = 0
			}
			if limit == 0 {
				summary = true
				wantHeader = false
			}
		}
	}

	var rows []string
	if wantSep {
		rows = append(rows, tokenSep(inner))
	}
	if wantHeader {
		if flow {
			// 横条档：列头按段重复，才能和下面每一段的 RUN/TOTAL/⊕ 对齐
			seg := padRight(tokenColHeader(tokenColCache), tokenColsLeft+tokenColCache)
			rows = append(rows, strings.TrimRight(strings.Repeat(seg, agentFlowPerRow(inner)), " "))
		} else {
			rows = append(rows, tokenColHeader(inner-tokenColsLeft))
		}
	}

	if summary {
		return append(rows, tokenAgentsSummary(snap, len(snap.agents)))
	}

	switch {
	case flow:
		rows = append(rows, flowAgentSegments(snap.agents[:limit], inner)...)
	case inner >= tokenNoBarBelow:
		for _, a := range snap.agents[:limit] {
			rows = append(rows, tokenAgentRow(a, inner-tokenColsLeft))
		}
	default:
		for _, a := range snap.agents[:limit] {
			rows = append(rows, tokenMoreStyle.Render(truncateCells(plainAgentSegment(a), inner)))
		}
	}

	if more {
		rows = append(rows, tokenMoreStyle.Render(fmt.Sprintf("┈ +%d more", len(snap.agents)-limit)))
	}
	return rows
}

// plainAgentSegment 是窄档与折叠行共用的无格式文本。
func plainAgentSegment(a agentTokens) string {
	seg := "● " + a.name + " " + formatToken(a.run()) + "▸" + formatToken(a.total())
	if ratio, ok := cacheHitRatio(a.cacheRead, a.cacheNew, a.totalIn); ok {
		seg += " ⊕" + ratioPct(ratio)
	}
	return seg
}

// flowAgentSegments 在宽面板里把 agent 横排：每段沿用 46 列网格，段间留 2 列。
func flowAgentSegments(agents []agentTokens, inner int) []string {
	segWidth := tokenColsLeft + tokenColCache
	perRow := agentFlowPerRow(inner)
	var rows []string
	for i := 0; i < len(agents); i += perRow {
		end := i + perRow
		if end > len(agents) {
			end = len(agents)
		}
		segs := make([]string, 0, end-i)
		for _, a := range agents[i:end] {
			segs = append(segs, padRight(tokenAgentRow(a, tokenColCache), segWidth))
		}
		rows = append(rows, strings.TrimRight(strings.Join(segs, "  "), " "))
	}
	return rows
}

// tokenAgentsSummary 是高度极限档：一行交代还有多少 agent、总命中率。
func tokenAgentsSummary(snap tokenSnapshot, count int) string {
	line := fmt.Sprintf("┈ +%d agents", count)
	if ratio, ok := cacheHitRatio(snap.cacheRead, snap.cacheNew, snap.totalIn); ok {
		line += " · ⊕" + ratioPct(ratio)
	}
	return tokenMoreStyle.Render(line)
}

// collapsedTokenLine 是 alt+t 折叠后的单行摘要：活跃 agent → 全局 Σ → ⛁ 上下文。
func collapsedTokenLine(snap tokenSnapshot, spec tokenPanelSpec) string {
	inner := spec.innerWidth
	var parts []string
	if snap.active.name != "" {
		parts = append(parts, tokenLabelStyle.Render("["+snap.active.name+"] ")+
			tokenRunStyle.Render("▸"+formatToken(snap.active.run()))+
			tokenAccentStyle.Render(cacheSuffix(snap.active)))
	}
	global := tokenLabelStyle.Render("Σ ") + tokenSigmaStyle.Render(formatToken(snap.totalIn+snap.totalOut))
	if ratio, ok := cacheHitRatio(snap.cacheRead, snap.cacheNew, snap.totalIn); ok {
		global += tokenAccentStyle.Render(" ⊕" + ratioPct(ratio))
	}
	parts = append(parts, global)
	if ratio, _, barColor, ok := ctxMeterParts(snap); ok {
		// 折叠行只带当前用量，不带分母 —— 一行要塞三段，宽度优先给数字。
		parts = append(parts, meterInline("⛁", ratio, tokenLabelStyle.Render(formatToken(snap.ctxTokens)), tokenMeterCells, barColor))
	}

	line := strings.Join(parts, tokenSepStyle.Render("   ·   "))
	if spec.keycap != "" && lipgloss.Width(line)+1+lipgloss.Width(spec.keycap) <= inner {
		line = padRight(line, inner-lipgloss.Width(spec.keycap)) + spec.keycap
	}
	return truncateCells(line, inner)
}

func cacheSuffix(a agentTokens) string {
	if ratio, ok := cacheHitRatio(a.cacheRead, a.cacheNew, a.totalIn); ok {
		return " ⊕" + ratioPct(ratio)
	}
	return ""
}

// footerInnerWidth 是底部面板的内容宽度：Width(termWidth-2) 再去掉边框 2 与 padding 2。
func (m *model) footerInnerWidth() int { return m.termWidth - 6 }

// renderFooterTokenPanel 渲染状态栏上方的底部 token 面板 —— 只在侧栏不可见时使用，
// 与侧栏走同一个 buildTokenPanel，所以 Σ / RUN / ⊕ / ⛁ 的口径不可能再分叉。
func (m *model) renderFooterTokenPanel() string {
	snap := m.tokenSnapshot()
	inner := m.footerInnerWidth()
	if !snap.hasUsage() || inner <= 0 {
		return ""
	}
	label := langManager.GetText("DashboardCollapseHint")
	if m.tokenDashboardCollapsed {
		label = langManager.GetText("DashboardExpandHint")
	}
	rows := buildTokenPanel(snap, tokenPanelSpec{
		innerWidth: inner,
		keycap:     dashKeycap("alt+t", label),
		collapsed:  m.tokenDashboardCollapsed,
	})
	if len(rows) == 0 {
		return ""
	}
	// ⧉ 压缩摘要只给 1 行，且属于同一个面板（不另起边框、不前置空行）：
	// 这样 lipgloss.Height 会自己跟着长，computeFooterHeight 不必新增分支。
	// 折叠态一行都塞不下，直接不给。
	if !m.tokenDashboardCollapsed {
		if summary := compressSummaryRow(m.compressSnapshot(), inner); summary != "" {
			rows = append(rows, summary)
		}
	}
	return lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(dashBorderFg).
		Padding(0, 1).
		Width(m.termWidth - 2).
		Render(strings.Join(rows, "\n"))
}
