package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"yagent/internal/memory"

	"charm.land/lipgloss/v2"
)

// 上下文压缩块：把三档压缩真正做了什么（触发过哪几档、各省了多少、哪些工具被截断、
// 上下文占用的趋势）摊成面板上的一个区块。
//
// 与 token 段同构：数字只在 compressSnapshot 里算一次，侧栏与底部从同一份快照排版，
// 口径不可能分叉。
//
// 三个事件名是 TUI 侧唯一的字面量副本 —— 上游在 executor.go 与 director/planner_run.go
// 各写了两遍，且都不在 protocol/agent-events.yaml 这个 SSOT 里。新增档位必须同时改
// compressTierByEvent、compressTierByKind 与 compressTierLabel。

// 档位下标（tiers 数组的顺序，也是徽标 T1/T2/T3）。
const (
	tierRule = iota
	tierEmergency
	tierUltimate
	compressTierCount
)

// 高度阶梯：从富到简逐档试，每档只砍一整行。
const (
	compressTierFull     = iota // 分隔+标题+三档+采样+榜 = 7 行
	compressTierNoIdle          // 砍掉"高于已触发档"的空档行
	compressTierNoSample        // 砍 ⛁ 采样行（与 token 段的 ⛁ 计量条重复度最高）
	compressTierNoCut           // 砍 cut: 榜
	compressTierNoSep           // 砍分隔线
	compressTierSummary         // 只留 分隔线 + 标题行
	compressTierLast     = compressTierSummary
)

// 列网格：indent+label+gap+saved+gap+count+gap 固定，余量全部给进度条。
const (
	compressColIndent = 2
	compressColLabel  = 13
	compressColSaved  = 9
	compressColCount  = 3
	compressColsLeft  = compressColIndent + compressColLabel + 1 + compressColSaved + 1 + compressColCount + 1
	compressMinBar    = 8 // 进度条不足 8 格就去掉条形
	compressCutMin    = 30

	// 底部摘要行的宽度门槛：<30 去百分比，<40 去截断榜。
	compressSummaryPctFrom = 30
	compressSummaryCutFrom = 40

	// maxCtxSamples 是 ⛁ 采样窗口的容量；超出后丢最旧。
	maxCtxSamples = 8
	// compressCutDisplay 是 cut: 榜最多列几项。
	compressCutDisplay = 2
)

// compressReserveRows 是给 ⧉ 段最低成本形态（分隔线 + 标题）预留的行数。
// 只在真的有压缩记录时才占用；token 段自己保底 3 行。
const compressReserveRows = 2

var (
	compressLabelStyle = lipgloss.NewStyle().Foreground(dashLabelFg)
	compressSavedStyle = lipgloss.NewStyle().Foreground(dashOutFg).Bold(true)
	compressIdleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	// 已触发档位的 label 配色：T1 中性、T2/T3 沿用 ⛁ 计量条的两档告警色。
	compressTierStyles = [compressTierCount]lipgloss.Style{
		lipgloss.NewStyle().Foreground(dashTitleFg).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true),
	}
)

// ctxSpark 从矮到高八档，采样行用它画趋势。
var ctxSpark = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

// tierStat 是单档压缩的累计效果。
type tierStat struct {
	count    int
	saved    int64
	original int64 // 该档历次事件里最大的 original_tokens
}

type cutStat struct {
	tool    string
	count   int
	omitted int64
}

// compressStats 是本任务历次上下文压缩的累计效果。
// 生命周期与 Σ 一致（会话累计，不是本轮），所以 resetRunTokens 不得清它。
type compressStats struct {
	tiers       [compressTierCount]tierStat
	cutByTool   map[string]*cutStat // 懒初始化
	ctxSamples  []int64             // 追加序，保留最近 maxCtxSamples 个
	peakCtx     int64               // 采样窗口外的历史峰值，pct 分母的回落
	maxOriginal int64               // 历次事件中最大的 original_tokens（pct 分母）
}

// clone 给 tab 快照用：两个 tab 绝不能共享同一张 cut 表或同一块采样数组 ——
// 事件会原地改写它们，切个标签页就串了。
func (c compressStats) clone() compressStats {
	out := c
	if c.cutByTool != nil {
		out.cutByTool = make(map[string]*cutStat, len(c.cutByTool))
		for k, v := range c.cutByTool {
			copied := *v
			out.cutByTool[k] = &copied
		}
	}
	if c.ctxSamples != nil {
		out.ctxSamples = make([]int64, len(c.ctxSamples))
		copy(out.ctxSamples, c.ctxSamples)
	}
	return out
}

func (c *compressStats) cuts() map[string]*cutStat {
	if c.cutByTool == nil {
		c.cutByTool = make(map[string]*cutStat)
	}
	return c.cutByTool
}

// addTier 并入一次压缩：一次事件 = 一次计数，不做去重。
func (c *compressStats) addTier(idx int, saved, original int64) {
	c.tiers[idx].count++
	c.tiers[idx].saved += saved
	if original > c.tiers[idx].original {
		c.tiers[idx].original = original
	}
	if original > c.maxOriginal {
		c.maxOriginal = original
	}
}

// sampleContext 采一个趋势点：连续同值不记录，否则一个平台期会吃光整个窗口。
func (c *compressStats) sampleContext(v int64) {
	if v <= 0 {
		return
	}
	if n := len(c.ctxSamples); n > 0 && c.ctxSamples[n-1] == v {
		return
	}
	c.ctxSamples = append(c.ctxSamples, v)
	if n := len(c.ctxSamples); n > maxCtxSamples {
		c.ctxSamples = c.ctxSamples[n-maxCtxSamples:]
	}
	if v > c.peakCtx {
		c.peakCtx = v
	}
}

func compressTierByEvent(eventType string) (int, bool) {
	switch eventType {
	case "context_compressed":
		return tierRule, true
	case "context_emergency_compressed":
		return tierEmergency, true
	case "context_ultimate_compressed":
		return tierUltimate, true
	}
	return 0, false
}

// compressTierByKind 认 rollout 里的 kind 字段（词表是 memory 的那三个常量，写入端与
// 读取端共用）。读取端已把老记录的 Summary 前缀归一成 kind，并丢掉认不出来的条目；
// 这里的 false 分支只是防御。
func compressTierByKind(kind string) (int, bool) {
	switch kind {
	case memory.CompactionKindRule:
		return tierRule, true
	case memory.CompactionKindEmergency:
		return tierEmergency, true
	case memory.CompactionKindUltimate:
		return tierUltimate, true
	}
	return 0, false
}

// compressionToolEntries 把 compressed 事件里的 truncated_tools[] 摊平成统一的条目视图。
//
// 两个发布方给的具体类型是 `[]map[string]interface{}`（executor.go 与 planner_run.go 里的
// `make([]map[string]interface{}, …)`），而消息总线在 TUI 这条路上**不做 JSON 往返**，
// 所以到 TUI 手里还是这个类型；只有经 HTTP/WebSocket 或测试构造才会是 `[]interface{}`。
// 只断言后者的话，cut: 榜与 ctrl+l 的截断明细会静默变空 —— 编译与单测都发现不了。
func compressionToolEntries(content map[string]interface{}) []map[string]interface{} {
	switch tools := content["truncated_tools"].(type) {
	case []map[string]interface{}:
		return tools
	case []interface{}:
		out := make([]map[string]interface{}, 0, len(tools))
		for _, ti := range tools {
			if tm, ok := ti.(map[string]interface{}); ok {
				out = append(out, tm)
			}
		}
		return out
	}
	return nil
}

// recordCompression 把一个压缩事件并入累计统计。
// original/saved 一律以上游的键为准，不自己算 original-compressed。
func (m *model) recordCompression(eventType string, content map[string]interface{}) {
	idx, ok := compressTierByEvent(eventType)
	if !ok {
		return
	}
	m.compress.addTier(idx,
		int64(parseEventInt(content["saved_tokens"], 0)),
		int64(parseEventInt(content["original_tokens"], 0)))

	// truncated_tools[] 是**每条被截断的 tool 消息**一项（见
	// compression.TruncateToolResultsToBudget 里 append 的位置：它在逐条截断的循环内），
	// 同一次事件里同一工具可以有多个条目 —— 所以 ×N 的含义是"N 次截断"，omitted 逐条累加。
	// 设计稿 §3.2 说"一次事件里同一工具只算 1 次"，其前提（条目已按工具汇总）与代码不符，
	// 且与 §2.2 的 golden（T1 ×2 却 read_file×7）自相矛盾；这里按数据实际形态取前者。
	cuts := m.compress.cuts()
	for _, tm := range compressionToolEntries(content) {
		name, _ := tm["tool_name"].(string)
		if name == "" {
			continue
		}
		cut := cuts[name]
		if cut == nil {
			cut = &cutStat{tool: name}
			cuts[name] = cut
		}
		cut.count++
		cut.omitted += int64(parseEventInt(tm["omitted_tokens"], 0))
	}
	m.invalidateFooterCache()
}

// tierLog 是单档的渲染输入（ratio 由快照算好，排版层不再做除法）。
type tierLog struct {
	count int
	saved int64
	ratio float64
}

// compressSnapshot 是压缩块的唯一输入。
type compressSnapshot struct {
	tiers   [compressTierCount]tierLog
	maxTier int // 到过第几档（0=从没压缩）→ 徽标
	saved   int64
	// pct = Σsaved / maxOriginal。三档是串行嵌套的，Σoriginal 会把同一批 token
	// 重复计进分母，所以分母只能取单次最大的 original_tokens。
	pct     float64
	samples []int // 最近 ≤8 个采样点，已归一化成 ctxSpark 的下标
	ctxNow  int64
	ctxWin  int
	cuts    []cutStat
	cutMore int
}

// any 报告有没有压缩记录。**没有**时区块不是消失，而是退到最低档形态
// （分隔线 + 标题两行，徽标 T0）—— 让"这一段存在但还没数据"是可见的，
// 而不是要等真触发过一次压缩才发现功能在不在。
func (s compressSnapshot) any() bool {
	for _, t := range s.tiers {
		if t.count > 0 {
			return true
		}
	}
	return false
}

// signature 摊平成一行字符串，进侧栏渲染缓存键。档位顺序固定、榜已排序。
func (s compressSnapshot) signature() string {
	var b strings.Builder
	for _, t := range s.tiers {
		fmt.Fprintf(&b, "%d:%d;", t.count, t.saved)
	}
	fmt.Fprintf(&b, "max=%d;pct=%.4f;", s.maxTier, s.pct)
	for _, c := range s.cuts {
		fmt.Fprintf(&b, "%s:%d;", c.tool, c.count)
	}
	fmt.Fprintf(&b, "more=%d;", s.cutMore)
	for _, lv := range s.samples {
		fmt.Fprintf(&b, "%d", lv)
	}
	return b.String()
}

func (m *model) compressSnapshot() compressSnapshot {
	var s compressSnapshot
	for i, st := range m.compress.tiers {
		s.tiers[i] = tierLog{count: st.count, saved: st.saved}
		s.saved += st.saved
		if st.count > 0 && i+1 > s.maxTier {
			s.maxTier = i + 1
		}
	}
	if s.saved > 0 {
		for i := range s.tiers {
			s.tiers[i].ratio = float64(s.tiers[i].saved) / float64(s.saved)
		}
	}
	// 分母优先用事件里的峰值 original_tokens；事件没带（或被截到 0）时回落到采样峰值。
	denom := m.compress.maxOriginal
	if denom <= 0 {
		denom = m.compress.peakCtx
	}
	// 三档串行嵌套，Σsaved 会超过任何单次 original；封顶到 100% 才不会被误读成
	// "省掉了比上下文还多的 token"。
	if denom > 0 {
		s.pct = clampRatio(float64(s.saved) / float64(denom))
	}

	if rt := m.runTokensPerAgent[m.runAgent]; rt != nil {
		s.ctxNow = rt.ContextTokens
	}
	s.ctxWin = m.contextWindow
	s.samples = ctxSampleLevels(m.compress.ctxSamples)
	s.cuts, s.cutMore = m.compress.topCuts()
	return s
}

func clampRatio(ratio float64) float64 {
	if ratio > 1 {
		return 1
	}
	if ratio < 0 {
		return 0
	}
	return ratio
}

// ctxSampleLevels 把采样归一化到窗口内的 min/max —— 除以 contextWindow 会把
// 形状压成一条直线；绝对值由同一行右段的数字给出。点数 < 2 画不出趋势，返回 nil。
func ctxSampleLevels(samples []int64) []int {
	if len(samples) < 2 {
		return nil
	}
	lo, hi := samples[0], samples[0]
	for _, v := range samples[1:] {
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	levels := make([]int, len(samples))
	// 采样写入时对连续同值去重，所以点数>=2 必然 lo<hi；这个分支只是防御。
	if hi == lo {
		for i := range levels {
			levels[i] = len(ctxSpark) - 1
		}
		return levels
	}
	span := float64(hi - lo)
	for i, v := range samples {
		levels[i] = int(float64(v-lo)/span*float64(len(ctxSpark)-1) + 0.5)
	}
	return levels
}

// topCuts 按次数降序（同次数按 omitted 降序，再同按名字）取前两项。
func (c *compressStats) topCuts() ([]cutStat, int) {
	if len(c.cutByTool) == 0 {
		return nil, 0
	}
	all := make([]cutStat, 0, len(c.cutByTool))
	for _, v := range c.cutByTool {
		all = append(all, *v)
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].count != all[j].count {
			return all[i].count > all[j].count
		}
		if all[i].omitted != all[j].omitted {
			return all[i].omitted > all[j].omitted
		}
		return all[i].tool < all[j].tool
	})
	if len(all) > compressCutDisplay {
		return all[:compressCutDisplay], len(all) - compressCutDisplay
	}
	return all, 0
}

// ── 排版 ──

type compressPanelSpec struct {
	innerWidth int
	maxRows    int // 0 = 不限高
}

func compressTierLabel(idx int) string {
	switch idx {
	case tierRule:
		return "T1 rule"
	case tierEmergency:
		return "T2 emergency"
	default:
		return "T3 ultimate"
	}
}

// compressSavedStr：省了 0 就不写负号 —— "−0" 读起来像欠了似的。
func compressSavedStr(saved int64) string {
	if saved <= 0 {
		return "0"
	}
	return "−" + formatToken(saved)
}

func compressPctLabel(pct float64) string {
	return fmt.Sprintf("(%.0f%%)", pct*100)
}

// buildCompressBlock 产出压缩块的若干行（不含边框）。
//
// 没有压缩记录时不是返回 nil，而是直接从最低档（分隔线 + 标题，徽标 T0）起步：
// "这一段存在、还没数据"必须是看得见的，否则要等真触发过一次压缩才知道功能在不在。
// 面板高度实在容不下这 2 行时仍然整段让位（宁可少一段，也不画空壳）。
func buildCompressBlock(s compressSnapshot, spec compressPanelSpec) []string {
	if spec.innerWidth <= 0 {
		return nil
	}
	start := compressTierFull
	if !s.any() {
		start = compressTierSummary
	}
	for tier := start; tier < compressTierLast; tier++ {
		rows := renderCompressTier(s, spec, tier)
		if spec.maxRows <= 0 || len(rows) <= spec.maxRows {
			return rows
		}
	}
	rows := renderCompressTier(s, spec, compressTierLast)
	if spec.maxRows > 0 && len(rows) > spec.maxRows {
		// 连"分隔线 + 标题"都放不下：整块让位，绝不画半截空壳。
		return nil
	}
	return rows
}

// renderCompressTier 按高度阶梯档位渲染。阶梯是累积的：档位越高砍掉的行越多，
// 已砍掉的不会在更高档又回来。
func renderCompressTier(s compressSnapshot, spec compressPanelSpec, tier int) []string {
	inner := spec.innerWidth
	var rows []string
	if tier != compressTierNoSep {
		rows = append(rows, tokenSep(inner))
	}
	rows = append(rows, compressTitleRow(s, inner))
	if tier == compressTierSummary {
		return rows
	}

	keepIdle := tier < compressTierNoIdle
	// bar 不足 8 格就去掉条形（宽度阶梯，与高度阶梯正交）
	if cells := inner - compressColsLeft; cells >= compressMinBar {
		for i, t := range s.tiers {
			if !keepIdle && i+1 > s.maxTier {
				continue
			}
			rows = append(rows, compressTierBarRow(t, i, cells))
		}
	} else {
		for i, t := range s.tiers {
			if !keepIdle && i+1 > s.maxTier {
				continue
			}
			rows = append(rows, compressTierCompactRow(t, i))
		}
	}

	if tier < compressTierNoSample && len(s.samples) >= 2 {
		rows = append(rows, compressSampleRow(s, inner))
	}
	if tier < compressTierNoCut && len(s.cuts) > 0 && inner >= compressCutMin {
		rows = append(rows, compressCutRow(s, inner))
	}
	return rows
}

// compressTitleRow：左段是节省量与比例，右段徽标是"到过第几档"。
// T0 = 还没压缩过 —— 空闲态也要有这一行，否则"这段存在但还没数据"只能靠
// 真触发一次压缩才能确认。
func compressTitleRow(s compressSnapshot, inner int) string {
	detail := dashBand(compressSavedStyle).Render(compressSavedStr(s.saved)) +
		dashBandDim.Render(" "+compressPctLabel(s.pct))
	badge := dashBand(tokenAccentStyle).Render("T" + strconv.Itoa(s.maxTier))
	return dashSectionRow("⧉", "COMPRESS", detail, badge, inner)
}

// compressRowStyles 决定一行的配色：未触发档位整行暗色，触发过的档位 label 用该档的
// 告警色、数字统一 244。
func compressRowStyles(t tierLog, idx int) (label, detail lipgloss.Style) {
	if t.count == 0 {
		return compressIdleStyle, compressIdleStyle
	}
	return compressTierStyles[idx], compressLabelStyle
}

func compressTierBarRow(t tierLog, idx, cells int) string {
	labelStyle, detailStyle := compressRowStyles(t, idx)
	line := strings.Repeat(" ", compressColIndent) +
		padRight(labelStyle.Render(compressTierLabel(idx)), compressColLabel) +
		" " + padLeft(detailStyle.Render(compressSavedStr(t.saved)), compressColSaved) +
		" " + padLeft(detailStyle.Render("×"+strconv.Itoa(t.count)), compressColCount) +
		" " + labelStyle.Render(fillBar(t.ratio, cells))
	return line
}

// compressTierCompactRow 是窄档：去条形，只留 label / saved / count。
func compressTierCompactRow(t tierLog, idx int) string {
	labelStyle, detailStyle := compressRowStyles(t, idx)
	return strings.Repeat(" ", compressColIndent) +
		labelStyle.Render(compressTierLabel(idx)) + "  " +
		detailStyle.Render(compressSavedStr(t.saved)) + " " +
		detailStyle.Render("×"+strconv.Itoa(t.count))
}

// compressSampleRow：左段是 sparkline，右段与 token 段的 ⛁ 计量条同一条口径。
func compressSampleRow(s compressSnapshot, inner int) string {
	ratio := 0.0
	if s.ctxWin > 0 {
		ratio = float64(s.ctxNow) / float64(s.ctxWin)
	}
	barColor := contextBarColor(clampRatio(ratio) * 100)

	left := strings.Repeat(" ", compressColIndent) +
		tokenAccentStyle.Render("⛁") + " " +
		compressLabelStyle.Render("ctx ") +
		lipgloss.NewStyle().Foreground(barColor).Render(sparkline(s.samples))

	detail := compressLabelStyle.Render(formatToken(s.ctxNow))
	if s.ctxWin > 0 {
		detail += compressLabelStyle.Render(" / " + formatToken(int64(s.ctxWin)))
	}
	if lipgloss.Width(left)+1+lipgloss.Width(detail) > inner {
		return left
	}
	return padRight(left, inner-lipgloss.Width(detail)) + detail
}

func sparkline(levels []int) string {
	var b strings.Builder
	for _, lv := range levels {
		if lv < 0 {
			lv = 0
		}
		if lv >= len(ctxSpark) {
			lv = len(ctxSpark) - 1
		}
		b.WriteRune(ctxSpark[lv])
	}
	return b.String()
}

// compressCutRow 是被截断工具的责任榜。TruncProtected 的工具（deepthinking/design/
// load_tool）永远不会出现在这里 —— 上游不截它们。
func compressCutRow(s compressSnapshot, inner int) string {
	parts := make([]string, 0, len(s.cuts))
	for _, c := range s.cuts {
		parts = append(parts, lipgloss.NewStyle().Foreground(dashTitleFg).Render(c.tool)+
			compressLabelStyle.Render("×"+strconv.Itoa(c.count)))
	}
	line := strings.Repeat(" ", compressColIndent) +
		compressLabelStyle.Render("cut: ") +
		strings.Join(parts, " ")
	if s.cutMore > 0 {
		line += tokenMoreStyle.Render(fmt.Sprintf(" ┈ +%d", s.cutMore))
	}
	return truncateCells(line, inner)
}

// compressSummaryRow 是底部面板那 1 行；折叠态不给行（一行塞不下三段以上）。
// 空闲态同样给这一行（T0），与侧栏的 2 行空闲形态同一条口径。
func compressSummaryRow(s compressSnapshot, inner int) string {
	if inner <= 0 {
		return ""
	}
	line := "  " + tokenAccentStyle.Render("⧉ ") + compressSavedStyle.Render(compressSavedStr(s.saved))
	if inner >= compressSummaryPctFrom {
		line += compressLabelStyle.Render(" " + compressPctLabel(s.pct))
	}
	line += tokenAccentStyle.Render(" T" + strconv.Itoa(s.maxTier))
	if inner >= compressSummaryCutFrom && len(s.cuts) > 0 {
		line += tokenSepStyle.Render("  ·  ") +
			compressLabelStyle.Render("cut "+s.cuts[0].tool+"×"+strconv.Itoa(s.cuts[0].count))
	}
	return truncateCells(line, inner)
}
