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

// ⧗ LATENCY 段：近期事件的耗时分布、最慢的几件事、各轮 agent 运行的耗时，
// 以及本任务到当前的整体墙钟。
//
// 与 token 段、⧉ 压缩段同构：**数字只在 latencySnapshot 里算一次**，排版层不碰
// model 上的任何计数器，也不做任何除法（internal/tui/AGENTS.md 硬约束 5）。
// 三个表面（侧栏、底部那 1 行、宽屏折叠）都从同一份快照排版，口径不可能分叉。
//
// 设计定稿见 docs/dashboard-latency-panel.md（判据、网格、让位序、闸门清单都在那儿）。

const (
	// latencyWindow 是"近期"的唯一定义：最近这么多条**已测得时长**的事件。
	// 20 沿用 render.go 里那个从未生效的 maxTimelineEntries，把它接成活的口径。
	latencyWindow = 20
	// latencyCalmMax 是冷静规则的门槛：整批都比它快就不许出现暖色。
	// 防的是假警 —— 一次正常的高速会话里 400ms 的 read_file 不该被染成"慢"。
	latencyCalmMax = 2 * time.Second
	// latencyReserveRows 是 ⧗ 段最低成本形态（带子 + 一行内容）占的行数。
	// 预算不足到连它都不够时整段让位，绝不画空壳。
	latencyReserveRows = 2
)

// 绝对档位。上界递增，最后一段是"以上"。
var latencyBandUpper = [3]time.Duration{time.Second, 3 * time.Second, 10 * time.Second}

// 档位配色全部取自既有令牌：<1s 用标签灰，往上依次是 in 值蓝、压缩告警黄、告警红
// （与 contextBarColor 的梯度同色同义，internal/tui/token_panel.go 的 ⛁ 行也用它）。
var latencyBandColors = [4]color.Color{
	lipgloss.Color("244"),
	lipgloss.Color("111"),
	lipgloss.Color("214"),
	lipgloss.Color("203"),
}

// latencySample 是一次已测得时长的事件。
type latencySample struct {
	id         string        // 事件自己的 ID（工具是 tool_call_id）
	label      string        // 显示名，已走 DisplayToolName
	dur        time.Duration // 时长口径见下方 recordLatency 的注释
	ts         time.Time     // 结束时刻
	agent      string        // 产出它的 Agent
	delegation string        // 它所属那轮委派的 tool_call_id；Director 自己为空
	isDelegate bool          // 这一条本身就是一次委派（嵌套容器）
}

// runStat 是一轮 agent 运行（一次 delegate_*）的聚合。
type runStat struct {
	id      string        // == 那次 delegate_* 的 tool_call_id
	agent   string        // 来自子事件的 From；没观察到子事件时为空
	dur     time.Duration // 委派自己的墙钟
	start   time.Time     // end - dur
	end     time.Time
	events  int           // 轮内观察到的事件数
	slowest time.Duration // 轮内单次最慢
}

// nested 报告这一轮的子事件是否也在窗口里被看见。
//
// 它是"委派算不算嵌套容器"的唯一判据，刻意**不看工具名**：
// 恢复出来的会话只有 Director 那一层的记忆（子 Agent 的调用在另一个 rollout 文件里），
// 那时子事件一个都没有，委派就不该被从具名榜里排除掉 —— 否则恢复的会话里
// sparkline 有尖峰、榜上却没有名字。
func (r *runStat) nested() bool { return r.events > 0 }

// latencyStats 是本任务的耗时累计。生命周期与 Σ 一致（会话累计，不是本轮），
// 所以 resetRunTokens 不得清它。
type latencyStats struct {
	samples []latencySample     // 追加序，超出 latencyWindow 丢最旧
	runs    map[string]*runStat // 懒初始化，键 = delegation id
	// 整体墙钟的三段账：起点、暂停累计、结束定格。放进同一个结构体是因为
	// 它们必须随标签页一起存 —— 漏一个就是切个标签页"这轮跑了多久"变了。
	taskStart  time.Time
	paused     time.Duration
	pauseStart time.Time // 非零 = 此刻正停着，这段还没结算进 paused
	endedAt    time.Time // 零值 = 任务还在跑
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

// clone 给标签页快照用：环数组与轮次表都会被事件原地改写，共享底层数组就是
// 两个标签页共用一份耗时账（同 compressStats.clone 的理由）。
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

// observe 并入一条样本：环形淘汰最旧的，并把委派自己的那轮账收进 runs。
// dur <= 0 的一律不进窗口 —— 没有时长就没有可比较的东西，把它画成 0 会让
// 分布的形状说谎（thinking 与 context 两类条目本来就没有 Duration）。
func (l *latencyStats) observe(s latencySample) {
	if s.dur <= 0 {
		return
	}
	l.samples = append(l.samples, s)
	if n := len(l.samples); n > latencyWindow {
		evicted := l.samples[0]
		l.samples = append(l.samples[:0], l.samples[n-latencyWindow:]...)
		// 委派离开窗口时它那一轮一起作废：子事件不可能晚于父事件到达，
		// 所以留着只会攒没人再读的条目。
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

// childAgent 在窗口里找第一条属于该轮次的子事件，用它的名义 Agent 名。
func childAgent(samples []latencySample, delegation string) string {
	for _, s := range samples {
		if s.delegation == delegation && s.agent != "" {
			return s.agent
		}
	}
	return ""
}

// elapsed 是"到目前整体耗时"：任务结束时用定格时刻，否则用现在；减去暂停累计
// ——暂停不是取消，那段时间不该算成工作量（根 AGENTS.md §11 坑 17）。
// 还有一段"此刻正停着、尚未结算"的区间也要扣掉，否则暂停中的面板会让 total
// 一路涨到有人按 Resume 为止。
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

// markTaskStarted 给本任务盖起点，并清掉上一任务的定格值。
//
// 三处 taskStarted=true 必须走这里而不是各写一遍：漏一处的表现是输入框边框已经
// 亮了、带子上的 total 却永远停在 — 。每次新任务重新起算 —— 把两次提交之间的
// 空闲算进"这次跑了多久"是谎报。
func (m *model) markTaskStarted() {
	m.taskStarted = true
	now := time.Now()
	m.latency.taskStart = now
	m.latency.endedAt = time.Time{}
	m.latency.paused = 0
	m.latency.pauseStart = time.Time{}
	m.invalidateFooterCache()
}

// accountPause 结算"闸门处于停住态"的那段墙钟。用显式传入的时刻做差，不数 tick
// —— 数 tick 会把监听链停过的那段时间算成工作时间。
//
// 只认 Blocked()（真的停在 step 边界上）：按下暂停到下一步之间还有人在干活，
// 那段算工作不算暂停。
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

// freezeTotal 在任务收尾时把 total 定格。
//
// 不定格的话：空闲态每秒仍在重画整段面板（total 在涨），而 handleTickMsg 的
// 早退条件永远不满足 —— 停 tick 那件事再也不会发生，静置时界面在转。
func (m *model) freezeTotal(now time.Time) {
	m.accountPause(now)
	m.latency.endedAt = now
	m.invalidateFooterCache()
}

// signature 摊平成一行字符串，进侧栏渲染缓存键。键上的是"这一帧渲染要用的全部输入"，
// 而不是原始账目 —— 少一项就是"事件到了、面板不刷新"，而编译与单测都发现不了。
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

// ── 快照（唯一的口径层）──

// latencySnapshot 是排版层的唯一输入。
type latencySnapshot struct {
	samples []latencySample // 窗口内全部样本，追加序（时间序）
	levels  []int           // 与 samples 同序，已归一到 ctxSpark 下标
	bands   []int           // 与 samples 同序，绝对档位下标；calm 时全 0
	board   []latencySample // 具名榜：按耗时降序，已排除"子事件可见的那些委派"
	runs    []runStat       // 按耗时降序
	maxDur  time.Duration
	// p50 是窗口中位数。刻意不用 p90/p95：n=9 时它会退化成正好等于 max，
	// 报一个和榜首同一个数的"分位数"只是自我重要。中位数在任何 n 上都有意义，
	// 而"最慢的是哪个"已经由具名榜交代了。
	p50      time.Duration
	total    time.Duration
	parallel int // 同时在跑的最多轮数；>1 时榜上的时长不可相加
	count    int // 窗口内样本数，就是行尾那个 n
}

func (s latencySnapshot) hasData() bool { return s.count > 0 }

// latencySnapshotOf 将累计账折算成渲染输入。
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

	// 高度是相对的（窗口内 min/max 归一），颜色是绝对的（固定档位）。
	// 两通道分开算，"整批都慢"才是平顶 + 暖色，"偶发尖峰"才是一个红格。
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

	// 具名榜：子事件可见的委派不进（它会压过一切叶子），其余都进。
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

// rankIndex 把分位数换算成升序数组的下标：至少取到最后一个，n 小的时候不退化成 max。
func rankIndex(n int, q float64) int {
	idx := int(float64(n)*q+0.999999) - 1 // ceil(q*n) - 1
	if idx < 0 {
		return 0
	}
	if idx > n-1 {
		return n - 1
	}
	return idx
}

// maxConcurrentRuns 扫一遍区间求最大并发轮数。
// >1 时榜上各行时长会重叠，相加会超过 total —— 界面上必须点名这件事（§4c）。
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
		return evs[i].delta < evs[j].delta // 同刻先减后加，端点相接不算重叠
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

// latencyBand 返回绝对档位下标（0=最快那档）。
func latencyBand(d time.Duration) int {
	for i, upper := range latencyBandUpper {
		if d < upper {
			return i
		}
	}
	return len(latencyBandUpper)
}

// latencyLevels 把窗口内的时长归一到 ctxSpark 的八档（窗口 min/max，不是
// contextWindow —— 除以窗口上限会把形状压成一条直线）。点数 < 2 画不出趋势。
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

// ── 采集 ──

// isDelegationTool 认"这一条是不是委派"，判据来自注册表的能力标签。
// 不许按 delegate_ 名字前缀硬判（根 AGENTS.md §5.1：不再往任何名字表加条目）。
func isDelegationTool(name string) bool {
	return toolspec.EffectOf(name)&toolspec.EffectDelegate != 0
}

// recordLatency 把一条已测得时长的样本并入账。
//
// 时长有两种钟，本段不假装它们同源：工具与委派是 TUI 侧墙钟（起点的 event
// Timestamp → 收到结果的那一刻），含事件总线排队；LLM 调用是上游给的
// duration_seconds。所以 total 与 ◉ 行只用前者，sparkline 混用是刻意的事实而非疏漏。
func (m *model) recordLatency(s latencySample) {
	m.latency.observe(s)
	m.invalidateFooterCache()
}

// recordToolLatency 收一条已完成的工具或委派调用。id 是它自己的 tool_call_id：
// 委派靠它认领"哪一轮"，子事件靠父级的 DelegationID 落进同一轮。
//
// 结束时刻按 start+dur 反推，不另读一次墙钟：⊘N 要靠 start/end 判重叠，
// 两边取自不同的 now 会把两段其实首尾相接的运行算成重叠。
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

// recordLLMLatency 收一条已完成的 LLM 调用；时长来自上游的 duration_seconds。
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

// ── 排版 ──

// 高度阶梯：从富到简逐档试，每档只砍一整行（与 compressTier* / tokenTier* 同纪律）。
// 让位序 = 轮次 → 事件 → 分布：具体大于抽象，分布条最先砍是因为它没有名字。
const (
	latencyTierFull     = iota // 分隔+带+分布+榜×2+轮×2 = 7 行
	latencyTierNoRun2          // 砍 ◉ 第 2 行
	latencyTierNoBoard2        // 砍 ▲ 第 2 行
	latencyTierNoHeat          // 砍 sparkline
	latencyTierNoRun1          // 砍 ◉ 第 1 行
	latencyTierBandOnly        // 只剩分隔+带子：total 仍可读
	latencyTierLast     = latencyTierBandOnly
)

// 网格：左段固定，余量给条。每行的列宽加起来必须正好等于 inner
// （internal/tui/AGENTS.md 硬约束 11：数过的行数与屏上的行数不是一个数）。
const (
	latColIndent = 2
	latColGlyph  = 2  // "▲ " / "◉ "
	latColGap    = 1  // 段间一格
	latColName   = 16 // ▲ 行的名字列
	latColRun    = 11 // ◉ 行的轮名列
	latColDur    = 6  // 时长列（12.4s / 1.2m）
	latColTail   = 10 // ◉ 行尾段：17c ▲8.9s
	latHeatLabel = 8  // "last 20 "
)

// 两榜各占几行（T0 形态）。
const (
	latencyBoardCount = 2
	latencyRunCount   = 2
)

type latencyPanelSpec struct {
	innerWidth int
	maxRows    int // 0 = 不限高
}

// buildLatencyBlock 产出 ⧗ 段的若干行（不含边框）。
//
// 一条时长都没测到时不返回 nil，而是退到最低档形态（分隔+带子，total 显示 —）：
// "这一段存在、还没数据"必须看得见（与 ⧉ 段同一条口径）。
// 预算连那 2 行都不够时整段让位 —— 宁可少一段，也不画空壳。
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
	// 最低档：分隔 + 带子（带子上永远写着 total）。连它都放不下就整段让位。
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

// topLatencyBoard 取具名榜前 n 条。s.board 已在快照里排除过"子事件可见的委派"
// 并按耗时降序排好，这里只切片。
func topLatencyBoard(s latencySnapshot, n int) []latencySample {
	if n > len(s.board) {
		n = len(s.board)
	}
	return s.board[:n]
}

// latencyTitleRow 是带子：左端段名，右端整体墙钟，并行时补 ⊘N。
// total 是唯一每 tick 会变的数字，显示粒度必须与进缓存键的粒度一致（§6）。
func latencyTitleRow(s latencySnapshot, inner int) string {
	right := dashBandDim.Render(" total ") + dashBand(latencyTotalStyle).Render(latencyTotalText(s.total))
	if s.parallel > 1 {
		// 各行时长会重叠，相加会超过 total —— 这件事必须在界面上点名。
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

// latencyHeatRow 一行交代分布：一格一事件、按时间序（不是排序——排序后永远是
// 同一道楼梯，形状就没有信息了），高度是窗口内相对值，颜色是绝对档位。
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
		start = n - avail // 削最旧的，保住最近的
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
	// 右段对齐到内宽（与 meterLine 同一条路）：不对齐的话这行的尾巴随 cell 数漂，
	// 三段面板的右边就参差不齐。
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

// latencyEventRow 是具名榜的一行：▲ + 名字 + 时长 + 同级比较条。
// 条长只回答"和最慢的比差多少"，绝对值一律看数字，所以榜首恒为满格。
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

// latencyRunRows 是轮次榜的若干行：◉ + 那轮的名字 + 墙钟 + 条 + 轮内计数与轮内最慢。
//
// 返回的是**每行一个字符串**，不是拼好的多行串 —— 面板的行预算是按 len(rows) 算的
// （tui_dashboard.go 的 tlAvailable），一个多行串会被当成一行，于是 ⧗ 段会以为自己
// 只占了 6 行而实际画了 7 行，把 Activity 挤掉。
func latencyRunRows(s latencySnapshot, count, inner int) []string {
	runW, barW, tailW := latencyRunCells(inner)
	lines := make([]string, 0, count)
	for _, r := range s.runs {
		if len(lines) >= count {
			break
		}
		// 既不知道是哪个 Agent、又没观察到任何子事件的轮次不配占一行：
		// 它就是具名榜上那条 delegate_xxx，再说一遍只是占行。
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

// latencyRunName 优先用子事件报的 Agent 名 + 委派 ID 尾 4 位（并行两个 Coding
// 时靠后缀分得开）。没有子事件时（恢复出来的会话就是这样）退回占位前缀。
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

// latencyHotStyle 给榜首上色：整批都低于冷静门槛时一律走最暗档 ——
// 400ms 的 read_file 在高速会话里不该被染成"慢"。
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

// ── 侧栏不可见时的那一行 ──

// 底部面板那 1 行的宽度门槛：<30 只留 total，<44 再去掉中位数，
// <62 去具名榜首，更宽才给轮次榜首。
const (
	latencySummaryPctFrom = 30
	latencySummaryTopFrom = 44
	latencySummaryRunFrom = 62
)

// latencySummaryRow 是底部 token 面板里的那一行（与 ⧉ 那行同一个面板、同一口径）。
// 折叠态不给：一行塞不下四段。
func latencySummaryRow(s latencySnapshot, inner int) string {
	if inner <= 0 || (!s.hasData() && s.total <= 0) {
		return ""
	}
	line := "  " + tokenAccentStyle.Render("⧗ ") +
		latencyLabelStyle.Render("total ") + latencyTotalStyle.Render(latencyTotalText(s.total))
	if s.parallel > 1 {
		line += latencyLabelStyle.Render(fmt.Sprintf(" ⊘%d", s.parallel))
	}
	// 从左到右越来越细：窄档先掉右边的段，剩下的永远是"跑了多久 + 整体快不快"。
	if inner >= latencySummaryPctFrom && s.p50 > 0 {
		line += tokenSepStyle.Render("  ·  ") + latencyLabelStyle.Render("p50 ") +
			latencyValueStyle.Render(formatTimelineDuration(s.p50))
	}
	// 判据是 len(board) 而不是 hasData()：窗口里只剩"有子事件的委派"时 board 是空的
	// （它们被排除在具名榜之外），按 hasData 取值再索引就是越界 panic。
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
