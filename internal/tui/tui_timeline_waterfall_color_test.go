package tui

import (
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────────────────
// 全屏瀑布图条形着色：状态优先，成功再按种类，色号与左侧树列状态图标同源
// （render.go 的 timelineStatusColor / timelineKindColor）。
//
// 真因回归：旧实现只看 e.IsError/e.Status 与 e.Kind，未用 EffectiveStatus，
// 于是"合并组里有一次失败"的条形仍显示成功色；且它另用一套平行色板
// （错误 203、Tool 42、LLM 111、Context 179），与树列状态色 167/114/75/141 漂移。
// 下面用例把"失败必须红 167、且合并组失败也算失败、且与树列同色"全部钉死。
// ─────────────────────────────────────────────────────────────────────────────

// wfEntry 构造指定 Kind/Status 的条目，可选额外属性。
func wfEntry(kind TimelineKind, status ToolStatus) *TimelineEntry {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	return &TimelineEntry{
		ID:        "e",
		Kind:      kind,
		Name:      "e",
		Timestamp: base,
		Duration:  time.Second,
		Status:    status,
	}
}

// 1) 状态优先：表驱动断言精确色号。
func TestTimelineWaterfallBarColor_StatusFirst(t *testing.T) {
	cases := []struct {
		name  string
		entry *TimelineEntry
		want  string
	}{
		{"success+Tool", wfEntry(TimelineKindTool, ToolStatusSuccess), "114"},
		{"success+LLMCall", wfEntry(TimelineKindLLMCall, ToolStatusSuccess), "75"},
		{"success+ContextEvent", wfEntry(TimelineKindContextEvent, ToolStatusSuccess), "141"},
		{"error+Tool", wfEntry(TimelineKindTool, ToolStatusError), "167"},
		{"error+LLMCall", wfEntry(TimelineKindLLMCall, ToolStatusError), "167"},
		{"running+Tool", wfEntry(TimelineKindTool, ToolStatusRunning), "228"},
		{"running+LLMCall", wfEntry(TimelineKindLLMCall, ToolStatusRunning), "228"},
		{"pending", wfEntry(TimelineKindTool, ToolStatusPending), "243"},
		{"canceled", wfEntry(TimelineKindTool, ToolStatusCanceled), "245"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := waterfallBarColor(c.entry); got != c.want {
				t.Errorf("waterfallBarColor = %q, want %q", got, c.want)
			}
		})
	}
}

// 2) 合并组：父 Success 但含失败子条目 → 整体失败（旧实现返回 "42"，本用例必须能抓住）。
func TestTimelineWaterfallBarColor_MergedGroupUsesEffectiveStatus(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	parent := &TimelineEntry{
		ID:        "grp",
		Kind:      TimelineKindTool,
		Name:      "read_file",
		Timestamp: base,
		Duration:  2 * time.Second,
		Status:    ToolStatusSuccess, // 父自身标成功
	}
	parent.SubEntries = []*TimelineEntry{
		waterfallTool("s0", base, time.Second, ToolStatusSuccess),
		waterfallTool("s1", base.Add(time.Second), time.Second, ToolStatusError), // 任一子失败
	}

	if st := waterfallBarStatus(parent); st != ToolStatusError {
		t.Fatalf("合并组 waterfallBarStatus = %v, want ToolStatusError", st)
	}
	if got := waterfallBarColor(parent); got != "167" {
		t.Errorf("合并组含失败子条目时 waterfallBarColor = %q, want \"167\"（旧实现为 \"42\"）", got)
	}
}

// 3) 只置 IsError、不改 Status 的条目：仍按失败处理。
func TestTimelineWaterfallBarColor_IsErrorOnlyEntry(t *testing.T) {
	e := wfEntry(TimelineKindTool, ToolStatusSuccess)
	e.IsError = true
	if st := waterfallBarStatus(e); st != ToolStatusError {
		t.Fatalf("Status=Success 但 IsError=true → waterfallBarStatus = %v, want ToolStatusError", st)
	}
	if got := waterfallBarColor(e); got != "167" {
		t.Errorf("waterfallBarColor = %q, want \"167\"", got)
	}
}

// 4) 聚合测试：瀑布色与树列状态图标色同源，防止两份实现漂移。
func TestTimelineStatusColorPaletteAgreesWithTreeIcons(t *testing.T) {
	statuses := []ToolStatus{
		ToolStatusRunning, ToolStatusSuccess, ToolStatusError,
		ToolStatusPending, ToolStatusCanceled,
	}
	// 4a) 五个状态值逐一构造 Tool 条目：瀑布色 == 状态色。
	seen := map[string]bool{}
	for _, st := range statuses {
		e := wfEntry(TimelineKindTool, st)
		if got, want := waterfallBarColor(e), timelineStatusColor(st); got != want {
			t.Errorf("状态 %v：waterfallBarColor=%q, timelineStatusColor=%q（两份实现漂移）", st, got, want)
		}
		seen[timelineStatusColor(st)] = true
	}

	// 4b) LLMCall / ContextEvent 成功态：瀑布色 == 种类色（与 ◇/⊛ 同色）。
	for _, kind := range []TimelineKind{TimelineKindLLMCall, TimelineKindContextEvent} {
		e := wfEntry(kind, ToolStatusSuccess)
		if got, want := waterfallBarColor(e), timelineKindColor(kind); got != want {
			t.Errorf("种类 %v：waterfallBarColor=%q, timelineKindColor=%q", kind, got, want)
		}
	}

	// 4c) 五个状态色互不相同（防同色塌陷）。
	dedup := map[string]struct{}{}
	for _, s := range []string{"114", "167", "228", "243", "245"} {
		dedup[s] = struct{}{}
	}
	if len(dedup) != 5 {
		t.Errorf("状态色板 {114,167,228,243,245} 去重后 len=%d, want 5", len(dedup))
	}
	for _, st := range statuses {
		if !seen[timelineStatusColor(st)] {
			t.Errorf("状态 %v 的色 %q 未出现在板内", st, timelineStatusColor(st))
		}
	}
}

// 5) 真实渲染行携带状态色：error 行有 167 前景、success 行有 114 前景，且互不包含。
func TestTimelineWaterfallRenderCarriesStatusColor(t *testing.T) {
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	entries := []*TimelineEntry{
		waterfallTool("ok", base, 2*time.Second, ToolStatusSuccess),
		waterfallTool("bad", base, 2*time.Second, ToolStatusError),
	}
	m := newWaterfallModel(entries)

	const width = 21
	win := timelineWindow{start: 0, end: 2}
	got := renderTimelineWaterfall(m, width, win)
	if len(got) != 2 {
		t.Fatalf("renderTimelineWaterfall 行数 = %d, want 2", len(got))
	}

	okRow, badRow := got[0], got[1]
	if !strings.Contains(okRow, "38;5;114") {
		t.Errorf("success 行未携带前景 114：%q", okRow)
	}
	if strings.Contains(okRow, "38;5;167") {
		t.Errorf("success 行不应出现失败色 167：%q", okRow)
	}
	if !strings.Contains(badRow, "38;5;167") {
		t.Errorf("error 行未携带前景 167：%q", badRow)
	}
	if strings.Contains(badRow, "38;5;114") {
		t.Errorf("error 行不应出现成功色 114：%q", badRow)
	}

	// 两行去色后几何一致（同宽同窗口），仅颜色不同。
	if stripANSI(okRow) != stripANSI(badRow) {
		t.Errorf("同宽同窗口下两行几何应一致：ok=%q bad=%q", stripANSI(okRow), stripANSI(badRow))
	}
}

// 6) 全量：失败色不得等于任何成功/运行中色。
func TestTimelineWaterfallErrorColorDistinct(t *testing.T) {
	errColor := waterfallBarColor(wfEntry(TimelineKindTool, ToolStatusError))
	others := []struct {
		name  string
		entry *TimelineEntry
	}{
		{"success-Tool", wfEntry(TimelineKindTool, ToolStatusSuccess)},
		{"success-LLM", wfEntry(TimelineKindLLMCall, ToolStatusSuccess)},
		{"success-Context", wfEntry(TimelineKindContextEvent, ToolStatusSuccess)},
		{"running-Tool", wfEntry(TimelineKindTool, ToolStatusRunning)},
		{"running-LLM", wfEntry(TimelineKindLLMCall, ToolStatusRunning)},
	}
	for _, o := range others {
		if c := waterfallBarColor(o.entry); c == errColor {
			t.Errorf("失败色 %q 与 %s 的色相同（颜色必须分明）", errColor, o.name)
		}
	}
}
