package tui

import (
	"testing"
	"time"
)

// ⧗ LATENCY 段的数据层：窗口、轮次归组、随标签页拷贝。
// 排版（sparkline / 两榜 / 降级）的闸门在 latency_panel_render_test.go。

// sampleAt 造一条已结束的工具样本；dur 由 start 与 dur 反推的 end 决定，
// 这样重叠判定与显示用的是同一对端点。
func sampleAt(id, label string, start time.Time, dur time.Duration, agent, delegation string) latencySample {
	return latencySample{
		id:         id,
		label:      label,
		dur:        dur,
		ts:         start.Add(dur),
		agent:      agent,
		delegation: delegation,
		isDelegate: isDelegationTool(label),
	}
}

func baseTime() time.Time { return time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC) }

// TestLatencyRingCapsAtWindow 窗口只留最近 latencyWindow 条，丢最旧。
// 没有上限就会学 timelineEntries 那样无界增长（它只有折叠视图取 3 条）。
func TestLatencyRingCapsAtWindow(t *testing.T) {
	var l latencyStats
	base := baseTime()
	for i := 0; i < latencyWindow+5; i++ {
		l.observe(sampleAt("t", "read_file", base.Add(time.Duration(i)*time.Second),
			time.Duration(i+1)*time.Second, "Coding", ""))
	}
	if len(l.samples) != latencyWindow {
		t.Fatalf("窗口 = %d 条, want %d", len(l.samples), latencyWindow)
	}
	if got := l.samples[0].dur; got != 6*time.Second {
		t.Errorf("最旧一条 = %v, want 6s（第 0~4 条该被淘汰）", got)
	}
}

// TestLatencyIgnoresUnmeasured 没有时长的条目不进窗口 —— 把 0 画进分布
// 会让形状说谎（thinking / context 两类条目本来就没有 Duration）。
func TestLatencyIgnoresUnmeasured(t *testing.T) {
	var l latencyStats
	l.observe(latencySample{label: "read_file", dur: 0, ts: baseTime()})
	if len(l.samples) != 0 {
		t.Errorf("dur=0 的样本进了窗口：%+v", l.samples)
	}
}

// TestLatencyChildrenGroupIntoTheirRun 轮内计数与轮内最慢值按 DelegationID 归组，
// 两轮的账互不污染。
func TestLatencyChildrenGroupIntoTheirRun(t *testing.T) {
	var l latencyStats
	base := baseTime()
	// 两个 Coding 并行，各自 read_file
	l.observe(sampleAt("call_a", "delegate_coding", base, 12*time.Second, "director", ""))
	l.observe(sampleAt("call_b", "delegate_coding", base.Add(time.Second), 9*time.Second, "director", ""))
	l.observe(sampleAt("r1", "read_file", base, 3*time.Second, "Coding", "call_a"))
	l.observe(sampleAt("r2", "read_file", base.Add(time.Second), 8*time.Second, "Coding", "call_a"))
	l.observe(sampleAt("r3", "read_file", base, 1*time.Second, "Coding", "call_b"))

	a, b := l.runs["call_a"], l.runs["call_b"]
	if a == nil || b == nil {
		t.Fatalf("两轮都该有自己的账：a=%v b=%v", a, b)
	}
	if a.events != 2 || a.slowest != 8*time.Second {
		t.Errorf("call_a: events=%d slowest=%v, want 2 / 8s", a.events, a.slowest)
	}
	if b.events != 1 || b.slowest != time.Second {
		t.Errorf("call_b: events=%d slowest=%v, want 1 / 1s（被邻轮的账污染了）", b.events, b.slowest)
	}
	if !a.nested() {
		t.Error("call_a 有子事件却被当成非嵌套 —— 它会被错放进具名榜")
	}
}

// TestLatencyRunAgentFromChildEvents 轮次的名字来自子事件的 From（坐标），
// 不是从工具名里截出来的。
func TestLatencyRunAgentFromChildEvents(t *testing.T) {
	var l latencyStats
	base := baseTime()
	l.observe(sampleAt("call_a", "delegate_coding", base, 5*time.Second, "director", ""))
	if r := l.runs["call_a"]; r == nil || r.agent != "" {
		t.Fatalf("还没观察到子事件时 agent 该是空的，实际 %+v", r)
	}
	l.observe(sampleAt("r1", "read_file", base, time.Second, "Coding", "call_a"))
	if got := l.runs["call_a"].agent; got != "Coding" {
		t.Errorf("轮次 agent = %q, want \"Coding\"", got)
	}
}

// TestLatencyEvictedRunIsDropped 委派离开窗口时那一轮的账一起作废，
// 否则长会话里 runs 表只增不减。
func TestLatencyEvictedRunIsDropped(t *testing.T) {
	var l latencyStats
	base := baseTime()
	l.observe(sampleAt("call_old", "delegate_coding", base, 5*time.Second, "director", ""))
	for i := 0; i < latencyWindow; i++ {
		l.observe(sampleAt("t", "read_file", base.Add(time.Duration(i)*time.Second), time.Second, "Coding", ""))
	}
	if _, alive := l.runs["call_old"]; alive {
		t.Errorf("call_old 已被淘汰出窗口，轮次表里还留着它：%+v", l.runs["call_old"])
	}
}

// TestLatencyStatsClonedPerTab 切标签页不能共享环数组或轮次表：两边都会被事件原地改写。
// 这条同时验 save 与 restore 两个方向，只测 clone() 本身测不到接线漏了一处。
func TestLatencyStatsClonedPerTab(t *testing.T) {
	// initSessionTabs 会经 generateTabTitle 取 i18n 文案；不先初始化就 nil panic。
	// testLang 同时把语言钉成英文，与别的渲染测试同一条路径。
	testLang(t)
	m := newTestModel()
	m.latency.observe(sampleAt("call_a", "delegate_coding", baseTime(), 5*time.Second, "director", ""))
	m.latency.observe(sampleAt("r1", "read_file", baseTime(), time.Second, "Coding", "call_a"))
	m.latency.taskStart = baseTime()
	m.initSessionTabs()
	m.saveActiveSession()
	tab := m.sessionTabs[m.activeSessionIdx]

	if len(tab.latency.samples) != 2 {
		t.Fatalf("标签页快照 = %d 条, want 2（save 那一路没接上）", len(tab.latency.samples))
	}
	if tab.latency.taskStart != baseTime() {
		t.Errorf("taskStart 没随标签页存下来：%v", tab.latency.taskStart)
	}
	// 原 model 继续被事件改写，快照不许跟着变
	m.latency.observe(sampleAt("r2", "read_file", baseTime(), time.Second, "Coding", "call_a"))
	if len(tab.latency.samples) != 2 {
		t.Errorf("标签页快照被原地改写成 %d 条 —— 环数组共享了", len(tab.latency.samples))
	}
	if tab.latency.runs["call_a"].events != 1 {
		t.Errorf("标签页快照的轮次计数被改写成 %d —— runs map 共享了",
			tab.latency.runs["call_a"].events)
	}

	// restore 方向：换一个新 model，从 tab 灌回去
	m2 := newTestModel()
	m2.latency = tab.latency.clone()
	if len(m2.latency.samples) != 2 || m2.latency.runs["call_a"].events != 1 {
		t.Errorf("restore 后 = %d 条 / events=%d, want 2 / 1",
			len(m2.latency.samples), m2.latency.runs["call_a"].events)
	}
	m2.latency.observe(sampleAt("r3", "read_file", baseTime(), time.Second, "Coding", "call_a"))
	if tab.latency.runs["call_a"].events != 1 {
		t.Errorf("restore 后新 model 改写了 tab 的账（events=%d）—— clone 只拷了切片没拷 map",
			tab.latency.runs["call_a"].events)
	}
}

// TestIsDelegationToolReadsRegistry 委派的判据来自注册表能力标签，不是名字前缀。
// 注册表里 delegate_coding 若不再声明 EffectDelegate，这里就该红 ——
// 按前缀硬判的那份实现永远不会红（根 AGENTS.md §5.1）。
func TestIsDelegationToolReadsRegistry(t *testing.T) {
	if !isDelegationTool("delegate_coding") {
		t.Error("delegate_coding 没被认成委派（注册表的 EffectDelegate 判据失效）")
	}
	if isDelegationTool("read_file") {
		t.Error("read_file 被认成了委派")
	}
	// 未注册的名字一律 fail-safe：认不出来就不是委派
	if isDelegationTool("some_future_tool") {
		t.Error("未注册的工具被认成了委派")
	}
}

// TestLatencyRecordedFromLiveEvents 采集点必须吃生产路径：喂一对真的
// tool_call_start/tool_call_result，断样本进了窗口且带着坐标。
// 只在测试里手拼 latencySample 等于没测这两个采集点（根 AGENTS.md §11 坑 33）。
func TestLatencyRecordedFromLiveEvents(t *testing.T) {
	m := newTestModel()
	start := buildEvent("tool_call_start", "Coding", map[string]interface{}{
		"tool_name": "read_file", "tool_call_id": "call_1", "arguments": `{"path":"a.go"}`,
	})
	start.DelegationID = "call_delegate"
	start.Timestamp = time.Now().Add(-3 * time.Second)
	feedEvent(m, start)

	end := buildEvent("tool_call_result", "Coding", map[string]interface{}{
		"tool_name": "read_file", "tool_call_id": "call_1", "result": "ok",
	})
	end.DelegationID = "call_delegate"
	feedEvent(m, end)

	if len(m.latency.samples) != 1 {
		t.Fatalf("窗口 = %d 条, want 1（采集点没接上 tool_call_result）", len(m.latency.samples))
	}
	s := m.latency.samples[0]
	if s.dur < 2*time.Second || s.dur > 4*time.Second {
		t.Errorf("dur = %v, want ≈3s", s.dur)
	}
	if s.delegation != "call_delegate" {
		t.Errorf("delegation = %q, want \"call_delegate\"", s.delegation)
	}
	if s.agent != "Coding" {
		t.Errorf("agent = %q, want \"Coding\"", s.agent)
	}
	if s.isDelegate {
		t.Error("read_file 被标成了委派")
	}
	if r := m.latency.runs["call_delegate"]; r == nil || r.events != 1 {
		t.Errorf("子事件没归进那一轮： %+v", r)
	}
}

// TestLatencyLLMRecordedFromLiveEvents LLM 的时长口径是上游给的 duration_seconds，
// 且它只被所在轮计数、自己不认领轮次。
func TestLatencyLLMRecordedFromLiveEvents(t *testing.T) {
	m := newTestModel()
	e := buildEvent("llm_call_end", "Coding", map[string]interface{}{"model": "m"})
	e.DelegationID = "call_delegate"
	e.Metadata = map[string]interface{}{"duration_seconds": 2.5}
	feedEvent(m, e)

	if len(m.latency.samples) != 1 {
		t.Fatalf("窗口 = %d 条, want 1", len(m.latency.samples))
	}
	if got := m.latency.samples[0].dur; got != 2500*time.Millisecond {
		t.Errorf("dur = %v, want 2.5s（duration_seconds 没被读到）", got)
	}
	if m.latency.samples[0].isDelegate {
		t.Error("llm_call 不该被标成委派")
	}
	if r := m.latency.runs["call_delegate"]; r == nil || r.events != 1 {
		t.Errorf("llm 调用没归进所在轮： %+v", r)
	}
}

// TestLatencyElapsed 整体墙钟：减去暂停、结束后定格。
func TestLatencyElapsed(t *testing.T) {
	base := baseTime()
	l := latencyStats{taskStart: base}
	if got := l.elapsed(base.Add(90 * time.Second)); got != 90*time.Second {
		t.Errorf("elapsed = %v, want 90s", got)
	}
	l.paused = 30 * time.Second
	if got := l.elapsed(base.Add(90 * time.Second)); got != 60*time.Second {
		t.Errorf("暂停没扣掉：%v, want 60s", got)
	}
	l.endedAt = base.Add(45 * time.Second)
	if got := l.elapsed(base.Add(600 * time.Second)); got != 15*time.Second {
		t.Errorf("结束后还在增长：%v, want 15s（45s - 30s 暂停）", got)
	}
	// 暂停长过总时长时不许给出负数
	l2 := latencyStats{taskStart: base, paused: time.Minute}
	if got := l2.elapsed(base.Add(time.Second)); got != 0 {
		t.Errorf("负时长：%v, want 0", got)
	}
	if got := (latencyStats{}).elapsed(base); got != 0 {
		t.Errorf("没有起点时 elapsed = %v, want 0", got)
	}
}

// TestLatencySignatureCoversRenderInputs 签名必须覆盖这一帧要渲染的全部输入：
// 只有本段数据变了、其他计数器一个都不动时，它是唯一能打破面板缓存的东西。
// 手法照 TestDashboardCacheKey_IncludesCompressStats。
func TestLatencySignatureCoversRenderInputs(t *testing.T) {
	base := baseTime()
	a := latencyStats{}
	a.observe(sampleAt("r1", "read_file", base, time.Second, "Coding", ""))
	sa := a.snapshot(base).signature()

	b := a.clone()
	b.observe(sampleAt("r2", "run_bash", base, 7*time.Second, "Coding", ""))
	if b.snapshot(base).signature() == sa {
		t.Error("多了一条更慢的样本而签名没变 —— 面板会永远命中旧缓存")
	}

	c := a.clone()
	// total 只在"过去"的起点上才有正数：snapshot(now) 反推，未来时刻会被夹成 0。
	c.taskStart = base.Add(-90 * time.Second)
	if c.snapshot(base).signature() == sa {
		t.Error("整体墙钟变了而签名没变 —— 带子右段的 total 没进签名")
	}

	d := a.clone()
	d.observe(sampleAt("call_x", "delegate_coding", base, 5*time.Second, "director", ""))
	e := d.clone()
	e.observe(sampleAt("r9", "read_file", base, 5*time.Second, "Coding", "call_x"))
	if e.snapshot(base).signature() == d.snapshot(base).signature() {
		t.Error("轮内计数变了而签名没变 —— ◉ 行的 17c 会永远停在旧值")
	}

	// 轮次表是 map：同样的账每次必须得到同一个串，否则缓存永远命中不了。
	// 快照按 (dur desc, id) 稳定排序，这里连喂两轮再比一次。
	f := a.clone()
	f.observe(sampleAt("call_p", "delegate_coding", base, 4*time.Second, "director", ""))
	f.observe(sampleAt("call_q", "delegate_repo", base, 4*time.Second, "director", ""))
	if f.snapshot(base).signature() != f.clone().snapshot(base).signature() {
		t.Error("相同账目的签名不稳定 —— map 遍历顺序漏进了键")
	}
}
