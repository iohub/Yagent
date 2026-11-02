package tui

import (
	"fmt"
	"image/color"
	"sort"
	"strings"
	"time"

	"yagent/internal/messaging"
	"yagent/internal/toolspec"

	"charm.land/lipgloss/v2"
)

const (
	latencyWindow      = 20
	latencyCalmMax     = 2 * time.Second
	latencyReserveRows = 2
)

var latencyBandUpper = [3]time.Duration{time.Second, 3 * time.Second, 10 * time.Second}

var latencyBandColors = [4]color.Color{
	lipgloss.Color("244"),
	lipgloss.Color("111"),
	lipgloss.Color("214"),
	lipgloss.Color("203"),
}

type latencySample struct {
	id         string
	label      string
	dur        time.Duration
	ts         time.Time
	agent      string
	delegation string
	isDelegate bool
}

type runStat struct {
	id      string
	agent   string
	dur     time.Duration
	start   time.Time
	end     time.Time
	events  int
	slowest time.Duration
}

func (r *runStat) nested() bool { return r.events > 0 }

type latencyStats struct {
	samples    []latencySample
	runs       map[string]*runStat
	taskStart  time.Time
	paused     time.Duration
	pauseStart time.Time
	endedAt    time.Time
}

func (l *latencyStats) runFor(id string) *runStat {
	if id == "" {
		return nil
	}
	if l.runs == nil {
		l.runs = make(map[string]*runStat)
	}
	r := l.runs[id]
	if r == nil {
		r = &runStat{id: id}
		l.runs[id] = r
	}
	return r
}

func (l latencyStats) clone() latencyStats {
	out := l
	if l.samples != nil {
		out.samples = make([]latencySample, len(l.samples))
		copy(out.samples, l.samples)
	}
	if l.runs != nil {
		out.runs = make(map[string]*runStat, len(l.runs))
		for k, v := range l.runs {
			copied := *v
			out.runs[k] = &copied
		}
	}
	return out
}

func (l *latencyStats) observe(s latencySample) {
	if s.dur <= 0 {
		return
	}
	l.samples = append(l.samples, s)
	if n := len(l.samples); n > latencyWindow {
		evicted := l.samples[0]
		l.samples = append(l.samples[:0], l.samples[n-latencyWindow:]...)
		if evicted.isDelegate && evicted.id != "" {
			delete(l.runs, evicted.id)
		}
	}

	if s.isDelegate {
		r := l.runFor(s.id)
		if r == nil {
			return
		}
		r.dur = s.dur
		r.end = s.ts
		r.start = s.ts.Add(-s.dur)
		if r.agent == "" {
			r.agent = childAgent(l.samples, s.id)
		}
		return
	}
	if s.delegation == "" {
		return
	}
	r := l.runFor(s.delegation)
	if r == nil {
		return
	}
	if r.agent == "" && s.agent != "" {
		r.agent = s.agent
	}
	r.events++
	if s.dur > r.slowest {
		r.slowest = s.dur
	}
}

func childAgent(samples []latencySample, delegation string) string {
	for _, s := range samples {
		if s.delegation == delegation && s.agent != "" {
			return s.agent
		}
	}
	return ""
}

func (l latencyStats) elapsed(now time.Time) time.Duration {
	if l.taskStart.IsZero() {
		return 0
	}
	end := now
	if !l.endedAt.IsZero() {
		end = l.endedAt
	}
	d := end.Sub(l.taskStart) - l.paused
	if !l.pauseStart.IsZero() && end.After(l.pauseStart) {
		d -= end.Sub(l.pauseStart)
	}
	if d < 0 {
		return 0
	}
	return d
}

func (m *model) markTaskStarted() {
	m.taskStarted = true
	now := time.Now()
	m.latency.taskStart = now
	m.latency.endedAt = time.Time{}
	m.latency.paused = 0
	m.latency.pauseStart = time.Time{}
	m.invalidateFooterCache()
}

func (m *model) accountPause(now time.Time) {
	blocked := false
	if m.currentTask != nil {
		if gate := m.currentTask.Gate(); gate != nil {
			blocked = gate.Paused() && gate.Blocked()
		}
	}
	switch {
	case blocked && m.latency.pauseStart.IsZero():
		m.latency.pauseStart = now
	case !blocked && !m.latency.pauseStart.IsZero():
		m.latency.paused += now.Sub(m.latency.pauseStart)
		m.latency.pauseStart = time.Time{}
	}
}

func (m *model) freezeTotal(now time.Time) {
	m.accountPause(now)
	m.latency.endedAt = now
	m.invalidateFooterCache()
}

func (s latencySnapshot) signature() string {
	var b strings.Builder
	fmt.Fprintf(&b, "n=%d;cnt=%d;total=%d;max=%d;p50=%d;par=%d;",
		s.count, len(s.samples), s.total, s.maxDur, s.p50, s.parallel)
	for i, x := range s.samples {
		lv, bd := -1, -1
		if len(s.levels) == len(s.samples) {
			lv = s.levels[i]
		}
		if len(s.bands) == len(s.samples) {
			bd = s.bands[i]
		}
		fmt.Fprintf(&b, "%s/%d/%d/%d;", x.label, x.dur, lv, bd)
	}
	for _, x := range s.board {
		fmt.Fprintf(&b, "B%s/%d;", x.label, x.dur)
	}
	for _, r := range s.runs {
		fmt.Fprintf(&b, "R%s/%d/%d/%d;", r.id, r.dur, r.events, r.slowest)
	}
	return b.String()
}

type latencySnapshot struct {
	samples  []latencySample
	levels   []int
	bands    []int
	board    []latencySample
	runs     []runStat
	maxDur   time.Duration
	p50      time.Duration
	total    time.Duration
	parallel int
	count    int
}

func (s latencySnapshot) hasData() bool { return s.count > 0 }

func (m *model) latencySnapshot() latencySnapshot {
	return m.latency.snapshot(time.Now())
}

func (l latencyStats) snapshot(now time.Time) latencySnapshot {
	s := latencySnapshot{
		samples: append([]latencySample(nil), l.samples...),
		count:   len(l.samples),
		total:   l.elapsed(now),
	}
	for _, r := range l.runs {
		s.runs = append(s.runs, *r)
	}
	sort.Slice(s.runs, func(i, j int) bool {
		if s.runs[i].dur != s.runs[j].dur {
			return s.runs[i].dur > s.runs[j].dur
		}
		return s.runs[i].id < s.runs[j].id
	})

	var lo time.Duration
	for _, x := range s.samples {
		if x.dur > s.maxDur {
			s.maxDur = x.dur
		}
		if lo == 0 || x.dur < lo {
			lo = x.dur
		}
	}

	s.levels = latencyLevels(s.samples)
	calm := s.maxDur < latencyCalmMax
	s.bands = make([]int, len(s.samples))
	for i, x := range s.samples {
		if calm {
			continue
		}
		s.bands[i] = latencyBand(x.dur)
	}

	durs := make([]time.Duration, len(s.samples))
	for i, x := range s.samples {
		durs[i] = x.dur
	}
	sort.Slice(durs, func(i, j int) bool { return durs[i] < durs[j] })
	if n := len(durs); n > 0 {
		s.p50 = durs[rankIndex(n, 0.50)]
	}

	nested := make(map[string]bool, len(s.runs))
	for _, r := range s.runs {
		if r.nested() {
			nested[r.id] = true
		}
	}
	for _, x := range s.samples {
		if x.isDelegate && nested[x.id] {
			continue
		}
		s.board = append(s.board, x)
	}
	sort.Slice(s.board, func(i, j int) bool {
		if s.board[i].dur != s.board[j].dur {
			return s.board[i].dur > s.board[j].dur
		}
		return s.board[i].label < s.board[j].label
	})

	s.parallel = maxConcurrentRuns(s.runs)
	return s
}

func rankIndex(n int, q float64) int {
	idx := int(float64(n)*q+0.999999) - 1
	if idx < 0 {
		return 0
	}
	if idx > n-1 {
		return n - 1
	}
	return idx
}

func maxConcurrentRuns(runs []runStat) int {
	if len(runs) < 2 {
		return len(runs)
	}
	type ev struct {
		t     time.Time
		delta int
	}
	evs := make([]ev, 0, len(runs)*2)
	for _, r := range runs {
		if r.start.IsZero() || !r.end.After(r.start) {
			continue
		}
		evs = append(evs, ev{r.start, 1}, ev{r.end, -1})
	}
	if len(evs) == 0 {
		return 0
	}
	sort.Slice(evs, func(i, j int) bool {
		if !evs[i].t.Equal(evs[j].t) {
			return evs[i].t.Before(evs[j].t)
		}
		return evs[i].delta < evs[j].delta
	})
	cur, peak := 0, 0
	for _, e := range evs {
		cur += e.delta
		if cur > peak {
			peak = cur
		}
	}
	return peak
}

func latencyBand(d time.Duration) int {
	for i, upper := range latencyBandUpper {
		if d < upper {
			return i
		}
	}
	return len(latencyBandUpper)
}

func latencyLevels(samples []latencySample) []int {
	if len(samples) < 2 {
		return nil
	}
	lo, hi := samples[0].dur, samples[0].dur
	for _, s := range samples[1:] {
		if s.dur < lo {
			lo = s.dur
		}
		if s.dur > hi {
			hi = s.dur
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
	for i, s := range samples {
		levels[i] = int(float64(s.dur-lo)/span*float64(len(ctxSpark)-1) + 0.5)
	}
	return levels
}

func isDelegationTool(name string) bool {
	return toolspec.EffectOf(name)&toolspec.EffectDelegate != 0
}

func (m *model) recordLatency(s latencySample) {
	m.latency.observe(s)
	m.invalidateFooterCache()
}

func (m *model) recordToolLatency(event *messaging.MessageEvent, id, name string, start time.Time, dur time.Duration) {
	if dur <= 0 {
		return
	}
	agent, delegation := timelineCoords(event)
	m.recordLatency(latencySample{
		id:         id,
		label:      DisplayToolName(name),
		dur:        dur,
		ts:         start.Add(dur),
		agent:      agent,
		delegation: delegation,
		isDelegate: isDelegationTool(name),
	})
}

func (m *model) recordLLMLatency(event *messaging.MessageEvent, end time.Time, dur time.Duration) {
	if dur <= 0 {
		return
	}
	agent, delegation := timelineCoords(event)
	m.recordLatency(latencySample{
		label:      DisplayToolName("llm_call"),
		dur:        dur,
		ts:         end,
		agent:      agent,
		delegation: delegation,
	})
}

const (
	latencyTierFull = iota
	latencyTierNoRun2
	latencyTierNoBoard2
	latencyTierNoHeat
	latencyTierNoRun1
	latencyTierBandOnly
	latencyTierLast = latencyTierBandOnly
)

const (
	latColIndent = 2
	latColGlyph  = 2
	latColGap    = 1
	latColName   = 16
	latColRun    = 11
	latColDur    = 6
	latColTail   = 10
	latHeatLabel = 8
)

const (
	latencyBoardCount = 2
	latencyRunCount   = 2
)

type latencyPanelSpec struct {
	innerWidth int
	maxRows    int
}

func buildLatencyBlock(s latencySnapshot, spec latencyPanelSpec) []string {
	if spec.innerWidth <= 0 {
		return nil
	}
	for tier := latencyTierFull; tier < latencyTierLast; tier++ {
		if !s.hasData() {
			break
		}
		rows := renderLatencyTier(s, spec, tier)
		if spec.maxRows <= 0 || len(rows) <= spec.maxRows {
			return rows
		}
	}
	rows := renderLatencyTier(s, spec, latencyTierBandOnly)
	if spec.maxRows > 0 && len(rows) > spec.maxRows {
		return nil
	}
	return rows
}

func renderLatencyTier(s latencySnapshot, spec latencyPanelSpec, tier int) []string {
	inner := spec.innerWidth
	rows := []string{tokenSep(inner), latencyTitleRow(s, inner)}
	if !s.hasData() || tier >= latencyTierLast {
		return rows
	}

	board := latencyBoardCount
	if tier >= latencyTierNoBoard2 {
		board = 1
	}
	runs := latencyRunCount
	if tier >= latencyTierNoRun2 {
		runs = 1
	}
	if tier >= latencyTierNoRun1 {
		runs = 0
	}

	if tier < latencyTierNoHeat {
		if heat := latencyHeatRow(s, inner); heat != "" {
			rows = append(rows, heat)
		}
	}
	for _, b := range topLatencyBoard(s, board) {
		rows = append(rows, latencyEventRow(b, s.maxDur, inner))
	}
	if runs > 0 {
		rows = append(rows, latencyRunRows(s, runs, inner)...)
	}
	return rows
}

func topLatencyBoard(s latencySnapshot, n int) []latencySample {
	if n > len(s.board) {
		n = len(s.board)
	}
	return s.board[:n]
}

func latencyTitleRow(s latencySnapshot, inner int) string {
	right := dashBandDim.Render(" total ") + dashBand(latencyTotalStyle).Render(latencyTotalText(s.total))
	if s.parallel > 1 {
		right += dashBandDim.Render(fmt.Sprintf(" ⊘%d", s.parallel))
	}
	return dashSectionRow("⧗", "LATENCY", "", right, inner)
}

func latencyTotalText(total time.Duration) string {
	if total <= 0 {
		return "—"
	}
	return formatTimelineDuration(total)
}

func latencyHeatRow(s latencySnapshot, inner int) string {
	if len(s.samples) < 2 {
		return ""
	}
	right := latencyLabelStyle.Render(fmt.Sprintf("n=%d", s.count))
	if s.p50 > 0 {
		right += latencyLabelStyle.Render(" p50 ") + latencyValueStyle.Render(formatTimelineDuration(s.p50))
	}
	avail := inner - latColIndent - latHeatLabel - 2 - lipgloss.Width(right)
	if avail < 1 {
		return ""
	}
	start := 0
	if n := len(s.samples); n > avail {
		start = n - avail
	}
	var b strings.Builder
	b.WriteString(strings.Repeat(" ", latColIndent))
	b.WriteString(latencyLabelStyle.Render(padRight(fmt.Sprintf("last %d", s.count), latHeatLabel-1)))
	b.WriteString(" ")
	for i := start; i < len(s.samples); i++ {
		level := 0
		if len(s.levels) == len(s.samples) {
			level = s.levels[i]
		}
		b.WriteString(lipgloss.NewStyle().Foreground(latencyBandColors[s.bands[i]]).
			Render(string(ctxSpark[clampLatencyLevel(level)])))
	}
	left := b.String()
	return padRight(left, inner-lipgloss.Width(right)) + right
}

func clampLatencyLevel(v int) int {
	if v < 0 {
		return 0
	}
	if v >= len(ctxSpark) {
		return len(ctxSpark) - 1
	}
	return v
}

func latencyEventRow(s latencySample, maxDur time.Duration, inner int) string {
	nameW, barW := latencyEventCells(inner)
	line := strings.Repeat(" ", latColIndent) +
		latencyHotStyle(s.dur, maxDur).Render("▲") + " " +
		padRight(dashNameStyle(s.label).Render(truncateCells(s.label, nameW)), nameW) + " " +
		padLeft(latencyValueStyle.Render(formatTimelineDuration(s.dur)), latColDur)
	if barW <= 0 {
		return truncateCells(line, inner)
	}
	return line + " " + latencyLabelStyle.Render(fillBar(clampRatio(latencyRatio(s.dur, maxDur)), barW))
}

func latencyRunRows(s latencySnapshot, count, inner int) []string {
	runW, barW, tailW := latencyRunCells(inner)
	lines := make([]string, 0, count)
	for _, r := range s.runs {
		if len(lines) >= count {
			break
		}
		if r.agent == "" && r.events == 0 {
			continue
		}
		name := latencyRunName(r)
		line := strings.Repeat(" ", latColIndent) +
			lipgloss.NewStyle().Foreground(dashInFg).Bold(true).Render("◉") + " " +
			padRight(dashNameStyle(name).Render(truncateCells(name, runW)), runW) + " " +
			padLeft(latencyValueStyle.Render(formatTimelineDuration(r.dur)), latColDur)
		if barW > 0 {
			line += " " + latencyLabelStyle.Render(fillBar(clampRatio(latencyRatio(r.dur, s.maxDur)), barW))
		}
		if tailW > 0 {
			t := ""
			if r.events > 0 {
				t = fmt.Sprintf("%dc", r.events)
			}
			if r.slowest > 0 {
				if t != "" {
					t += " "
				}
				t += "▲" + formatTimelineDuration(r.slowest)
			}
			if t != "" {
				line += " " + padLeft(latencyLabelStyle.Render(t), tailW)
			}
		}
		lines = append(lines, truncateCells(line, inner))
	}
	return lines
}

func latencyRunName(r runStat) string {
	tail := r.id
	if len(tail) > 4 {
		tail = tail[len(tail)-4:]
	}
	if r.agent == "" {
		return "run·" + tail
	}
	return r.agent + "·" + tail
}

func latencyEventCells(inner int) (nameW, barW int) {
	nameW = latColName
	barW = inner - (latColIndent + latColGlyph + nameW + latColGap + latColDur + latColGap)
	if barW < compressMinBar {
		barW = 0
		if inner < 28 {
			nameW = 8
		}
	}
	return nameW, barW
}

func latencyRunCells(inner int) (runW, barW, tailW int) {
	runW, tailW = latColRun, latColTail
	barW = inner - (latColIndent + latColGlyph + runW + latColGap + latColDur + latColGap + latColGap + tailW)
	if barW < compressMinBar {
		barW = 0
		tailW = 0
		if inner < 28 {
			runW = 6
		}
	}
	return runW, barW, tailW
}

func latencyRatio(dur, maxDur time.Duration) float64 {
	if maxDur <= 0 {
		return 0
	}
	return float64(dur) / float64(maxDur)
}

func latencyHotStyle(dur, maxDur time.Duration) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(latencyBandColors[latencyBandOf(dur, maxDur)]).Bold(true)
}

func latencyBandOf(dur, maxDur time.Duration) int {
	if maxDur < latencyCalmMax {
		return 0
	}
	return latencyBand(dur)
}

var (
	latencyLabelStyle = lipgloss.NewStyle().Foreground(dashLabelFg)
	latencyValueStyle = lipgloss.NewStyle().Foreground(dashTitleFg).Bold(true)
	latencyTotalStyle = lipgloss.NewStyle().Foreground(dashOutFg).Bold(true)
)

const (
	latencySummaryPctFrom = 30
	latencySummaryTopFrom = 44
	latencySummaryRunFrom = 62
)

func latencySummaryRow(s latencySnapshot, inner int) string {
	if inner <= 0 || (!s.hasData() && s.total <= 0) {
		return ""
	}
	line := "  " + tokenAccentStyle.Render("⧗ ") +
		latencyLabelStyle.Render("total ") + latencyTotalStyle.Render(latencyTotalText(s.total))
	if s.parallel > 1 {
		line += latencyLabelStyle.Render(fmt.Sprintf(" ⊘%d", s.parallel))
	}
	if inner >= latencySummaryPctFrom && s.p50 > 0 {
		line += tokenSepStyle.Render("  ·  ") + latencyLabelStyle.Render("p50 ") +
			latencyValueStyle.Render(formatTimelineDuration(s.p50))
	}
	if inner >= latencySummaryTopFrom && len(s.board) > 0 {
		line += tokenSepStyle.Render("  ·  ") +
			latencyHotStyle(s.board[0].dur, s.maxDur).Render("▲"+truncateCells(s.board[0].label, 16)) +
			latencyLabelStyle.Render(" "+formatTimelineDuration(s.board[0].dur))
	}
	if inner >= latencySummaryRunFrom && len(s.runs) > 0 {
		r := s.runs[0]
		if r.agent != "" || r.events > 0 {
			line += tokenSepStyle.Render("  ·  ") +
				lipgloss.NewStyle().Foreground(dashInFg).Bold(true).Render("◉"+latencyRunName(r)) +
				latencyLabelStyle.Render(" "+formatTimelineDuration(r.dur))
		}
	}
	return truncateCells(line, inner)
}
