package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"yagent/internal/messaging"
)

// ─────────────────────────────────────────────────────────────────────────────
// 阶段 B2：树行派生接入全屏 timeline 的渲染与光标。
//
// 关键语义变更：timelineFullscreenCursor 不再是"扁平条目下标"，而是"可见树行下标"
// （m.timelineTreeRows）。因此凡是用光标定位条目的地方，都必须经
// rows[cursor].entryIndex 映射回 m.timelineEntries / m.timelineDetailOffsets。
// 本组测试锁定这条映射（尤其 thinking 被树行过滤后造成的错位）与渲染不变量。
// ─────────────────────────────────────────────────────────────────────────────

// ztEntry 是构造 []*TimelineEntry 的简写，带可选 Name。
func ztEntry(id string, kind TimelineKind, delegationID, name string) *TimelineEntry {
	return &TimelineEntry{ID: id, Kind: kind, DelegationID: delegationID, Name: name}
}

// TestTimelineTreeCursorMapsDetailOffsetToEntryIndex 覆盖核心错位：
// 光标是树行下标，详情定位必须落到"该行 entryIndex 对应的 offset"，
// 而不是把树行下标当扁平条目下标直接索引 offsets。
func TestTimelineTreeCursorMapsDetailOffsetToEntryIndex(t *testing.T) {
	m := newTestModel()
	m.termWidth, m.termHeight = 120, 24
	m.timelineFullscreenMode = true

	ts := time.Now()
	mk := func(id string, kind TimelineKind, delegationID string) *TimelineEntry {
		e := ztEntry(id, kind, delegationID, id)
		e.Timestamp = ts
		e.Status = ToolStatusSuccess
		return e
	}

	// 扁平下标 0..8；其中 1、4 是 Thinking（不产树行）。
	entries := []*TimelineEntry{
		mk("dir", TimelineKindLLMCall, ""),           // 0 根
		mk("dir_t", TimelineKindThinking, ""),        // 1 Thinking 跳过
		mk("call1", TimelineKindTool, ""),            // 2 根（委派工具）
		mk("sub_llm", TimelineKindLLMCall, "call1"),  // 3 call1 的子，嵌套行
		mk("sub_t", TimelineKindThinking, "call1"),   // 4 Thinking 跳过
		mk("sub_llm2", TimelineKindLLMCall, "call1"), // 5 call1 的子
		mk("tail1", TimelineKindTool, ""),            // 6 根
		mk("tail2", TimelineKindTool, ""),            // 7 根
		mk("tail3", TimelineKindTool, ""),            // 8 根
	}
	m.timelineEntries = entries

	// 真实生产路径：打开全屏时重建树行 + 初始化详情 viewport（内部 buildAllTimelineDetails）。
	rebuildTimelineTreeRows(m)
	initTimelineDetailViewport(m)

	if m.timelineDetailVP == nil || len(m.timelineDetailOffsets) != len(entries) {
		t.Fatalf("详情 viewport/offsets 未就绪: vp=%v offsets=%d entries=%d",
			m.timelineDetailVP != nil, len(m.timelineDetailOffsets), len(entries))
	}

	// 定位嵌套子条目（扁平下标 3）对应的树行。
	nestedTreeRow := -1
	for i, r := range m.timelineTreeRows {
		if r.entryIndex == 3 {
			nestedTreeRow = i
			break
		}
	}
	if nestedTreeRow < 0 {
		t.Fatalf("树行里没有 entryIndex=3: rows=%+v", m.timelineTreeRows)
	}
	// 关键前提：树行下标 != 扁平下标（Thinking 被过滤），否则测不出错位。
	if nestedTreeRow == 3 {
		t.Fatalf("期望树行下标与扁平下标错位，got==3 说明过滤未生效")
	}

	m.timelineFullscreenCursor = nestedTreeRow
	syncDetailToCursor(m)

	got := m.timelineDetailVP.YOffset()
	want := m.timelineDetailOffsets[3]              // 正确：经 entryIndex 映射
	wrong := m.timelineDetailOffsets[nestedTreeRow] // 错误：直接拿树行下标索引 offsets

	if want >= m.timelineDetailVP.Height() {
		t.Fatalf("目标 offset(%d) 太靠前，文档不够长无法区分错位", want)
	}
	if got != want {
		t.Errorf("详情定位错位: YOffset=%d, want offsets[entryIndex=3]=%d", got, want)
	}
	if wrong != want && got == wrong {
		t.Errorf("YOffset 命中了'树行下标当扁平下标'的错误 offset=%d（应=%d）", got, want)
	}
}

// TestTimelineTreeLeftHidesThinkingRightKeepsFullText 同时断言：
//   - 左列树形渲染里没有 Thinking 条目（被 buildTimelineTreeRows 过滤）；
//   - 右列详情里仍能看到这条 Thinking 的完整正文（被合并进父 LLMCall）。
func TestTimelineTreeLeftHidesThinkingRightKeepsFullText(t *testing.T) {
	const marker = "THEMARKER_zz_thinking_full_text"

	m := newTestModel()
	m.termWidth, m.termHeight = 240, 60
	m.timelineFullscreenMode = true

	ts := time.Now()
	dir := ztEntry("dir", TimelineKindLLMCall, "", "llm_call")
	dir.Timestamp = ts
	dir.Status = ToolStatusSuccess
	think := ztEntry("dir_think", TimelineKindThinking, "", "thinking")
	think.Timestamp = ts // findFullThinkingContent 按 eventType=="thinking" + Equal(ts) 反查

	m.timelineEntries = []*TimelineEntry{dir, think}

	m.logEntries = append(m.logEntries, logEntry{
		eventType: "thinking",
		timestamp: ts,
		content:   marker,
	})

	rebuildTimelineTreeRows(m)
	initTimelineDetailViewport(m)

	m.timelineFullscreenCursor = 0
	syncDetailToCursor(m)

	layout := computeTimelineLayout(m.termWidth, m.termHeight)
	left := renderTimelineFullscreenList(m, layout.LeftW, layout.ContentH)
	right := m.timelineDetailVP.View()

	if strings.Contains(left, marker) {
		t.Errorf("左列不应含被过滤的 Thinking 正文，却含 marker: %q", left)
	}
	if !strings.Contains(right, marker) {
		t.Errorf("右列详情应含合并的 Thinking 全文 marker，实际未含:\n%s", right)
	}
	if strings.Contains(left, "dir_think") {
		t.Errorf("左列不应出现 thinking 条目名 dir_think: %q", left)
	}
}

// TestTimelineFullscreenTreeWidthMatrix 渲染不变量：跨宽度 x 高度，每行显示宽 <= termW，
// 且 AltScreen 恒为 true。走真实 renderTimelineFullscreenView（含 guides 前缀的左列）。
func TestTimelineFullscreenTreeWidthMatrix(t *testing.T) {
	widths := []int{30, 40, 60, 80, 100, 120, 240}
	heights := []int{20, 24, 40, 60}

	for _, w := range widths {
		for _, h := range heights {
			m := newTestModel()
			m.termWidth, m.termHeight = w, h
			m.timelineFullscreenMode = true
			m.timelineEntries = []*TimelineEntry{
				ztEntry("dir", TimelineKindLLMCall, "", "llm_call"),
				ztEntry("call1", TimelineKindTool, "", "delegate_coding"),
				ztEntry("sub", TimelineKindLLMCall, "call1", "read_file"),
				ztEntry("sub2", TimelineKindLLMCall, "call1", "search_by_regex"),
				ztEntry("deep", TimelineKindTool, "call1", "run_bash"),
			}
			rebuildTimelineTreeRows(m)
			initTimelineDetailViewport(m)

			v := renderTimelineFullscreenView(m)
			if !v.AltScreen {
				t.Fatalf("w%d_h%d: AltScreen=false", w, h)
			}
			for _, line := range strings.Split(v.Content, "\n") {
				if got := lipgloss.Width(line); got > w {
					t.Fatalf("w%d_h%d: 行宽 %d > %d，行=%q", w, h, got, w, line)
				}
			}
		}
	}
}

// TestTimelineFullscreenTreeConsumesTaskEvents 全屏期间 taskEventMsg 仍被消费并续监听：
// 走真实 m.Update 分发（timelineFullscreenMode 命中 timelineFullscreenUpdate）。
func TestTimelineFullscreenTreeConsumesTaskEvents(t *testing.T) {
	m := newTestModel()
	m.termWidth, m.termHeight = 120, 40
	m.timelineFullscreenMode = true
	m.timelineFullscreenFocus = "list"
	rebuildTimelineTreeRows(m)
	initTimelineDetailViewport(m)
	m.taskRunning = true

	before := len(m.logEntries)
	updated, cmd := m.Update(taskEventMsg{event: &messaging.MessageEvent{
		Type:      messaging.EventType("info"),
		From:      "coding_agent",
		Content:   "hello while fullscreen",
		Timestamp: time.Now(),
	}})
	got := updated.(*model)

	if !got.timelineFullscreenMode {
		t.Fatalf("全屏模式应在全事件消费后保持 true")
	}
	if cmd == nil {
		t.Fatalf("taskEventMsg 必须续监听，cmd 不应为 nil")
	}
	if len(got.logEntries) <= before {
		t.Fatalf("事件应被消费进日志（logEntries 未增长），before=%d after=%d", before, len(got.logEntries))
	}
}

// TestTimelineFullscreenOpenWithEmptyEntries 空 entries 打开全屏：不 panic、cursor==0。
func TestTimelineFullscreenOpenWithEmptyEntries(t *testing.T) {
	m := newTestModel()
	m.termWidth, m.termHeight = 80, 24
	m.timelineEntries = nil

	m.toggleTimelineFullscreen() // 不应 panic

	if !m.timelineFullscreenMode {
		t.Fatalf("toggle 后应进入全屏")
	}
	if m.timelineFullscreenCursor != 0 {
		t.Errorf("空树 cursor 应为 0, got %d", m.timelineFullscreenCursor)
	}
	if len(m.timelineTreeRows) != 0 {
		t.Errorf("空 entries 应派生 0 行, got %d", len(m.timelineTreeRows))
	}
	v := renderTimelineFullscreenView(m)
	if !v.AltScreen {
		t.Errorf("空树全屏视图 AltScreen 应为 true")
	}
}
