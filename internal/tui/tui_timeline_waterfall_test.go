package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
)

// ─────────────────────────────────────────────────────────────────────────────
// 阶段 C：瀑布图（waterfall）时间条列的几何与装配覆盖。
//
// 锁定的语义（与 tui_timeline_fullscreen.go 实现一一对应）：
//   - timelineEntryInterval：Duration>0 → end=Timestamp+Duration；Duration<=0 且
//     Running → end=now（经 timelineNowFunc）；否则 end=Timestamp+1ns；始终 end>s。
//   - renderTimelineWaterfall：跨度取窗口内可见行 minStart/maxEnd，barStart/barEnd
//     映射到 [0,width-1]，每行显示宽严格 == width；空窗口或 width<=0 返回 nil；
//     hasAbove/hasBelow 行输出 ▲/▼。
//   - 树列与瀑布列共用 computeTimelineWindow 的同一窗口，行数严格一致。
//
// 全部用生产函数，时钟经 stub timelineNowFunc 固定，比较前用同包 stripANSI 去色。
// ─────────────────────────────────────────────────────────────────────────────

// waterfallTool 构造一个瀑布测试用的 Tool 条目。
func waterfallTool(id string, ts time.Time, dur time.Duration, status ToolStatus) *TimelineEntry {
	return &TimelineEntry{
		ID:        id,
		Kind:      TimelineKindTool,
		Name:      id,
		Timestamp: ts,
		Duration:  dur,
		Status:    status,
	}
}

// newWaterfallModel 用 newTestModel + buildTimelineTreeRows 装配一个带条目的全屏 model。
// 生产路径（derivations）已在 B1/B2 测过，这里直接赋值 timelineTreeRows。
func newWaterfallModel(entries []*TimelineEntry) *model {
	m := newTestModel()
	m.timelineFullscreenMode = true
	m.timelineEntries = entries
	m.timelineTreeRows = buildTimelineTreeRows(entries)
	return m
}

// barMetrics 返回去色行的 (前导空格数, 紧随其后的 █ 连续段长度)。
// 注意 █ 是多字节 rune，必须按 rune 而非 byte 比较。
func barMetrics(line string) (int, int) {
	r := []rune(stripANSI(line))
	lead := 0
	for lead < len(r) && r[lead] == ' ' {
		lead++
	}
	bars := 0
	for i := lead; i < len(r) && r[i] == '█'; i++ {
		bars++
	}
	return lead, bars
}

// ---------------------------------------------------------------------------
// 1) 逐行精确几何
// ---------------------------------------------------------------------------

func TestTimelineWaterfall_GeometryExact(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	entries := []*TimelineEntry{
		waterfallTool("e0", base, 1*time.Second, ToolStatusSuccess),                    // [base, base+1s]
		waterfallTool("e1", base.Add(1*time.Second), 1*time.Second, ToolStatusSuccess), // [base+1s, base+2s]
		waterfallTool("e2", base.Add(2*time.Second), 2*time.Second, ToolStatusSuccess), // [base+2s, base+4s]
	}
	m := newWaterfallModel(entries)

	const width = 21
	win := timelineWindow{start: 0, end: 3}

	got := renderTimelineWaterfall(m, width, win)
	if len(got) != 3 {
		t.Fatalf("renderTimelineWaterfall 行数 = %d, want 3 (win 0..3)", len(got))
	}

	// 跨度 minStart=base，maxEnd=base+4s → span=4s；colSpan=width-1=20。
	// e0: barStart=0, barEnd=int(1/4*20)=5 → 6 个 █, 0 前导空格, 15 尾空格。
	// e1: barStart=int(1/4*20)=5, barEnd=int(2/4*20)=10 → 6 个 █, 5 前导, 10 尾。
	// e2: barStart=int(2/4*20)=10, barEnd=int(4/4*20)=20 → 11 个 █, 10 前导, 0 尾。
	want := []string{
		strings.Repeat("█", 6) + strings.Repeat(" ", 15),
		strings.Repeat(" ", 5) + strings.Repeat("█", 6) + strings.Repeat(" ", 10),
		strings.Repeat(" ", 10) + strings.Repeat("█", 11),
	}
	for i := range want {
		if gotStrip := stripANSI(got[i]); gotStrip != want[i] {
			t.Errorf("第 %d 行几何不符:\n got=%q\nwant=%q", i, gotStrip, want[i])
		}
		if dw := lipgloss.Width(got[i]); dw != width {
			t.Errorf("第 %d 行 lipgloss.Width = %d, want %d", i, dw, width)
		}
	}
}

// ---------------------------------------------------------------------------
// 2) 零/负时长：兜底 1 列 + 运行中延伸到 now
// ---------------------------------------------------------------------------

func TestTimelineWaterfall_ZeroAndNegativeDuration(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	now := base.Add(3 * time.Second)

	// 固定时钟，保证运行中条目条尾稳定落在 now，断言可成立。
	orig := timelineNowFunc
	timelineNowFunc = func() time.Time { return now }
	defer func() { timelineNowFunc = orig }()

	entries := []*TimelineEntry{
		waterfallTool("zero_success", base, 0, ToolStatusSuccess),             // [base, base+1ns]
		waterfallTool("neg_success", base, -1*time.Second, ToolStatusSuccess), // 同样兜底 1ns
		waterfallTool("zero_running", base, 0, ToolStatusRunning),             // [base, now]
	}
	m := newWaterfallModel(entries)

	const width = 41
	win := timelineWindow{start: 0, end: len(entries)}

	got := renderTimelineWaterfall(m, width, win)
	if len(got) != len(entries) {
		t.Fatalf("行数 = %d, want %d", len(got), len(entries))
	}

	// 跨度 minStart=base，maxEnd=now=base+3s → span=3s；colSpan=40。
	for i, line := range got {
		_, bars := barMetrics(line)
		if bars < 1 {
			t.Errorf("第 %d 行 (%s) 条宽 = %d, want >= 1", i, stripANSI(line), bars)
		}
	}

	// 运行中(Duration=0)条宽度严格大于 完成(Duration=0)条宽度。
	_, runningBars := barMetrics(got[2])
	_, successBars := barMetrics(got[0])
	if runningBars <= successBars {
		t.Errorf("运行中条宽度 %d 未严格大于 完成零时长条宽度 %d（运行中应延伸到 now）",
			runningBars, successBars)
	}
	// 运行中条应铺满到 colSpan（now 即 maxEnd）：barStart=0, barEnd=40 → 41 个 █。
	if runningBars != width {
		t.Errorf("运行中条宽度 = %d, want %d（now 是跨度终点，应铺满）", runningBars, width)
	}

	// 直接核对 timelineEntryInterval 的三条分支语义。
	for _, tc := range []struct {
		name    string
		entries []*TimelineEntry
	}{
		{"dur>0", []*TimelineEntry{waterfallTool("x", base, 2*time.Second, ToolStatusSuccess)}},
		{"dur<=0 running", []*TimelineEntry{waterfallTool("x", base, 0, ToolStatusRunning)}},
		{"dur<=0 success", []*TimelineEntry{waterfallTool("x", base, 0, ToolStatusSuccess)}},
	} {
		e := tc.entries[0]
		s, en := timelineEntryInterval(e, now)
		if !en.After(s) {
			t.Errorf("timelineEntryInterval(%s): end=%v 未严格晚于 start=%v", tc.name, en, s)
		}
	}
}

// ---------------------------------------------------------------------------
// 3) 与树列严格对齐
// ---------------------------------------------------------------------------

func TestTimelineWaterfall_AlignsWithTree(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	entries := make([]*TimelineEntry, 0, 50)
	for i := 0; i < 50; i++ {
		entries = append(entries, waterfallTool(
			"call_"+string(rune('a'+i%26))+itoa(i),
			base.Add(time.Duration(i)*time.Second),
			time.Duration(i%5+1)*time.Second,
			ToolStatusSuccess,
		))
	}
	m := newWaterfallModel(entries)

	const height = 10
	m.timelineFullscreenCursor = 25 // 居中，制造上下溢出

	win := computeTimelineWindow(m, height)
	const width = 30
	wf := renderTimelineWaterfall(m, width, win)

	tree := renderTimelineFullscreenList(m, width, height)
	treeLines := strings.Split(tree, "\n")

	if len(wf) != win.lineCount() {
		t.Errorf("瀑布行数 = %d, want win.lineCount() = %d", len(wf), win.lineCount())
	}
	if len(wf) != len(treeLines) {
		t.Fatalf("瀑布行数 = %d, 树列行数 = %d，两列必须严格一致", len(wf), len(treeLines))
	}

	// 居中光标 + 上下都有溢出：首行 ▲、末行 ▼。
	if first := stripANSI(wf[0]); !strings.HasPrefix(first, "▲") {
		t.Errorf("首行应为 ▲ 占位，got=%q", first)
	}
	if last := stripANSI(wf[len(wf)-1]); !strings.HasPrefix(last, "▼") {
		t.Errorf("末行应为 ▼ 占位，got=%q", last)
	}
	if !win.hasAbove || !win.hasBelow {
		t.Errorf("期望窗口同时上下溢出, got hasAbove=%v hasBelow=%v", win.hasAbove, win.hasBelow)
	}
}

// ---------------------------------------------------------------------------
// 4) 退化窄尺寸：WaterfallW==0 时整段让位
// ---------------------------------------------------------------------------

func TestTimelineWaterfall_DegenerateNarrow(t *testing.T) {
	const termW, termH = 40, 24
	layout := computeTimelineLayout(termW, termH)

	// 前提：此尺寸下瀑布列必须整段让位（== 0）。
	if layout.WaterfallW != 0 {
		t.Fatalf("前提不成立：computeTimelineLayout(%d,%d).WaterfallW = %d, want 0",
			termW, termH, layout.WaterfallW)
	}

	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	entries := []*TimelineEntry{
		waterfallTool("e0", base, 1*time.Second, ToolStatusSuccess),
		waterfallTool("e1", base.Add(time.Second), 1*time.Second, ToolStatusSuccess),
	}
	m := newWaterfallModel(entries)
	m.termWidth, m.termHeight = termW, termH

	win := computeTimelineWindow(m, layout.ContentH)

	// width<=0 → nil。
	if wf := renderTimelineWaterfall(m, 0, win); wf != nil {
		t.Errorf("renderTimelineWaterfall(m,0,win) = %v, want nil", wf)
	}

	// 左列装配退化为纯树：每行显示宽 <= LeftW 且不含 █。
	left := renderTimelineFullscreenLeft(m, layout)
	for i, line := range strings.Split(left, "\n") {
		if dw := lipgloss.Width(line); dw > layout.LeftW {
			t.Errorf("左列第 %d 行宽 = %d, want <= LeftW=%d", i, dw, layout.LeftW)
		}
		if strings.Contains(stripANSI(line), "█") {
			t.Errorf("左列第 %d 行含 █，但 WaterfallW==0 时不应渲染瀑布：%q", i, stripANSI(line))
		}
	}
}

// ---------------------------------------------------------------------------
// 5) 全屏视图宽度矩阵（带瀑布）
// ---------------------------------------------------------------------------

func TestTimelineFullscreenRenderWidthMatrix_WithWaterfall(t *testing.T) {
	widths := []int{60, 80, 100, 120, 160, 240}
	heights := []int{20, 24, 40, 60}

	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	entries := []*TimelineEntry{
		waterfallTool("e0", base, 1*time.Second, ToolStatusSuccess),
		waterfallTool("e1", base.Add(time.Second), 2*time.Second, ToolStatusSuccess),
		waterfallTool("e2", base.Add(3*time.Second), 1*time.Second, ToolStatusSuccess),
	}

	for _, termW := range widths {
		for _, termH := range heights {
			t.Run("w"+itoa(termW)+"_h"+itoa(termH), func(t *testing.T) {
				m := newWaterfallModel(entries)
				m.termWidth, m.termHeight = termW, termH

				v := renderTimelineFullscreenView(m)
				if !v.AltScreen {
					t.Fatalf("AltScreen=false（termW=%d termH=%d）", termW, termH)
				}

				for i, line := range strings.Split(v.Content, "\n") {
					if dw := lipgloss.Width(line); dw > termW {
						t.Fatalf("第 %d 行宽 = %d, want <= termW=%d", i, dw, termW)
					}
				}

				// 瀑布列存在时，视图必须含时间条 █。
				if computeTimelineLayout(termW, termH).WaterfallW > 0 {
					if !strings.Contains(v.Content, "█") {
						t.Errorf("WaterfallW>0 但视图不含 █（termW=%d termH=%d）", termW, termH)
					}
				}
			})
		}
	}
}
