package tui

// R2 的渲染闸门：行首两格从"选中符/空白"换成"状态符"之后，行的显示宽度必须
// 一字不变 —— 这次改动承诺的是"只加判据、不动几何"，几何回归（F1）留给 R3。

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"yagent/internal/memory"

	"charm.land/lipgloss/v2"
)

func stopIndexFixture(t *testing.T) (*model, []memory.RolloutSessionInfo) {
	t.Helper()
	testLang(t)
	base := time.Now()
	items := []memory.RolloutSessionInfo{
		{Path: "/r/main.jsonl", TaskID: "T1", AgentName: "director", Model: "qwen3-coder",
			ProjectID: "7f3a2b", Preview: "重构 resume 界面", StartedAt: base, ModTime: base},
		{Path: "/r/main_coding.jsonl", TaskID: "T1", AgentName: "coding", Model: "deepseek-v3",
			ProjectID: "7f3a2b", Preview: "改列宽分配", StartedAt: base.Add(time.Minute), ModTime: base.Add(time.Minute)},
		{Path: "/r/old.jsonl", AgentName: "director", Model: "gpt-4.1",
			Preview: "已经跑完的那个任务", StartedAt: base.Add(-time.Hour), ModTime: base.Add(-time.Hour)},
	}
	m := newTestModel()
	return m, items
}

func TestResumeRowKeepsGroupedByStateThenByRecency(t *testing.T) {
	m, items := stopIndexFixture(t)
	m.resumeStops = resumeStopIndex{
		// 最新的一组是"跑完了"，较旧的一组才可原样重放
		"/r/main.jsonl":        {Found: true, Finished: true},
		"/r/main_coding.jsonl": {Found: true, Aborted: true, StopReason: memory.StopKindPaused},
		"/r/old.jsonl":         {},
	}
	m.resumeGroups = sortResumeGroupsByState(buildResumeGroups(items), m.resumeStops)
	m.resumeExpanded = map[string]bool{}
	m.rebuildResumeRows()

	if len(m.resumeRows) != 2 {
		t.Fatalf("rows = %d, want 2（T1 一组 + old 一组）", len(m.resumeRows))
	}
	// T1 的**主会话**判成"跑完了"（rank 4），但它的 coding 子文件还带着可重放停点
	// （rank 0）；/r/old.jsonl 无判据 = 未知（rank 2）。
	// 只看主会话判据的实现会把 old 排在前面 —— 那正是这条断言要区分开的。
	if got := m.resumeRows[0].item.Path; got != "/r/main.jsonl" {
		t.Errorf("first group = %s, want /r/main.jsonl（groupRank 必须看成员，不能只看主会话）", got)
	}
}

// 顶栏摘要（renderPanelTop 的 meta 段）必须带可续计数，且在任何面板宽度下
// 都不越过 panelW —— 越过的话整行会被再折一次，行数预算当场失效。
func TestResumeMetaCarriesCountsAndFitsTheRail(t *testing.T) {
	m, items := stopIndexFixture(t)
	m.resumeGroups = buildResumeGroups(items)
	m.resumeExpanded = map[string]bool{}
	m.resumeStops = resumeStopIndex{
		"/r/main.jsonl": {Found: true, Aborted: true, StopReason: memory.StopKindCancelled},
	}

	for _, w := range []int{44, 60, 80, 104, 160} {
		m.termWidth, m.termHeight = w, 20
		panelW, inner, _ := m.resumeLayout()
		rail := renderPanelTop(m.resumeLabel(inner), m.resumeMeta(inner), panelW, lipgloss.Color("141"))
		if got := lipgloss.Width(rail); got != panelW {
			t.Errorf("W=%d 顶栏宽 = %d, want 精确等于 panelW %d\n%q", w, got, panelW, stripANSI(rail))
		}
		if !strings.Contains(stripANSI(rail), "1/2") {
			t.Errorf("W=%d 顶栏丢了可续计数（应当有 1 组可续 / 共 2 组）：%q", w, stripANSI(rail))
		}
	}
}

// 计数必须"分子分母同类"：分母是**组**（任务），不是展开后的行数。
// 混用会读出 "1/6 可续" 这种把主会话和它的子文件当成两个可续对象的数。
func TestResumeMetaCountsTasksNotRows(t *testing.T) {
	m, items := stopIndexFixture(t)
	m.resumeStops = resumeStopIndex{
		"/r/main.jsonl":        {Found: true, Aborted: true, StopReason: memory.StopKindCancelled},
		"/r/main_coding.jsonl": {Found: true, Aborted: true, StopReason: memory.StopKindPaused},
	}
	m.resumeExpanded = map[string]bool{}
	for _, g := range buildResumeGroups(items) {
		m.resumeExpanded[g.key] = true // 展开：行数远多于组数
	}
	m.resumeGroups = buildResumeGroups(items)
	m.rebuildResumeRows()
	if len(m.resumeRows) <= len(m.resumeGroups) {
		t.Fatalf("夹具没展开出额外行（rows=%d groups=%d），测不到这个区分", len(m.resumeRows), len(m.resumeGroups))
	}

	m.termWidth, m.termHeight = 100, 20
	_, inner, _ := m.resumeLayout()
	meta := m.resumeMeta(inner)
	want := strconv.Itoa(resumableGroups(m.resumeStops, m.resumeGroups)) + "/" +
		strconv.Itoa(len(m.resumeGroups))
	if !strings.Contains(meta, want) {
		t.Errorf("meta = %q, want 按组计数 %q（不是按行）", meta, want)
	}
}
