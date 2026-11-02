package tui

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"yagent/internal/memory"

	"charm.land/lipgloss/v2"
)

const (
	tierRule = iota
	tierEmergency
	tierUltimate
	compressTierCount
)

const (
	compressTierFull = iota
	compressTierNoIdle
	compressTierNoSample
	compressTierNoCut
	compressTierNoSep
	compressTierSummary
	compressTierLast = compressTierSummary
)

const (
	compressColIndent = 2
	compressColLabel  = 13
	compressColSaved  = 9
	compressColCount  = 3
	compressColsLeft  = compressColIndent + compressColLabel + 1 + compressColSaved + 1 + compressColCount + 1
	compressMinBar    = 8
	compressCutMin    = 30

	compressSummaryPctFrom = 30
	compressSummaryCutFrom = 40

	maxCtxSamples      = 8
	compressCutDisplay = 2
)

const compressReserveRows = 2

var (
	compressLabelStyle = lipgloss.NewStyle().Foreground(dashLabelFg)
	compressSavedStyle = lipgloss.NewStyle().Foreground(dashOutFg).Bold(true)
	compressIdleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	compressTierStyles = [compressTierCount]lipgloss.Style{
		lipgloss.NewStyle().Foreground(dashTitleFg).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true),
		lipgloss.NewStyle().Foreground(lipgloss.Color("203")).Bold(true),
	}
)

var ctxSpark = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

type tierStat struct {
	count    int
	saved    int64
	original int64
}

type cutStat struct {
	tool    string
	count   int
	omitted int64
}

type compressStats struct {
	tiers       [compressTierCount]tierStat
	cutByTool   map[string]*cutStat
	ctxSamples  []int64
	peakCtx     int64
	maxOriginal int64
}

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

func (m *model) recordCompression(eventType string, content map[string]interface{}) {
	idx, ok := compressTierByEvent(eventType)
	if !ok {
		return
	}
	m.compress.addTier(idx,
		int64(parseEventInt(content["saved_tokens"], 0)),
		int64(parseEventInt(content["original_tokens"], 0)))

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

type tierLog struct {
	count int
	saved int64
	ratio float64
}

type compressSnapshot struct {
	tiers   [compressTierCount]tierLog
	maxTier int
	saved   int64
	pct     float64
	samples []int
	ctxNow  int64
	ctxWin  int
	cuts    []cutStat
	cutMore int
}

func (s compressSnapshot) any() bool {
	for _, t := range s.tiers {
		if t.count > 0 {
			return true
		}
	}
	return false
}

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
	denom := m.compress.maxOriginal
	if denom <= 0 {
		denom = m.compress.peakCtx
	}
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

type compressPanelSpec struct {
	innerWidth int
	maxRows    int
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

func compressSavedStr(saved int64) string {
	if saved <= 0 {
		return "0"
	}
	return "−" + formatToken(saved)
}

func compressPctLabel(pct float64) string {
	return fmt.Sprintf("(%.0f%%)", pct*100)
}

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
		return nil
	}
	return rows
}

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

func compressTitleRow(s compressSnapshot, inner int) string {
	detail := dashBand(compressSavedStyle).Render(compressSavedStr(s.saved)) +
		dashBandDim.Render(" "+compressPctLabel(s.pct))
	badge := dashBand(tokenAccentStyle).Render("T" + strconv.Itoa(s.maxTier))
	return dashSectionRow("⧉", "COMPRESS", detail, badge, inner)
}

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

func compressTierCompactRow(t tierLog, idx int) string {
	labelStyle, detailStyle := compressRowStyles(t, idx)
	return strings.Repeat(" ", compressColIndent) +
		labelStyle.Render(compressTierLabel(idx)) + "  " +
		detailStyle.Render(compressSavedStr(t.saved)) + " " +
		detailStyle.Render("×"+strconv.Itoa(t.count))
}

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
