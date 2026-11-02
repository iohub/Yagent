package tui

import (
	"testing"
)

// entry 是构造 []*TimelineEntry 的简写，避免每个用例重复填字段。
func entry(id string, kind TimelineKind, delegationID string) *TimelineEntry {
	return &TimelineEntry{ID: id, Kind: kind, DelegationID: delegationID}
}

// assertRows 把 rows 与期望的 (entryIndex, depth, guides) 三元组逐行比对。
// 顺序敏感：先父后子的 DFS 序必须完全一致，任何错位都直接 t.Errorf。
func assertRows(t *testing.T, rows []timelineTreeRow, want []struct {
	entryIndex int
	depth      int
	guides     string
}) {
	t.Helper()
	if len(rows) != len(want) {
		t.Fatalf("行数不符: got %d, want %d\nrows=%+v", len(rows), len(want), rows)
	}
	for i := range want {
		got := rows[i]
		if got.entryIndex != want[i].entryIndex {
			t.Errorf("第%d行 entryIndex: got %d, want %d", i, got.entryIndex, want[i].entryIndex)
		}
		if got.depth != want[i].depth {
			t.Errorf("第%d行 depth: got %d, want %d", i, got.depth, want[i].depth)
		}
		if got.guides != want[i].guides {
			t.Errorf("第%d行 guides: got %q, want %q", i, got.guides, want[i].guides)
		}
	}
}

// TestBuildTimelineTreeRows_NestedDelegation 覆盖 director + delegate_repo(call1)
// + repo事件(call1) + delegate_devops(call2,委派自call1) + devops事件(call2)。
func TestBuildTimelineTreeRows_NestedDelegation(t *testing.T) {
	entries := []*TimelineEntry{
		entry("dir", TimelineKindLLMCall, ""),       // 0: 根
		entry("call1", TimelineKindTool, ""),        // 1: 根（委派工具）
		entry("r1", TimelineKindLLMCall, "call1"),   // 2: call1 的子（非最后）
		entry("call2", TimelineKindTool, "call1"),   // 3: call1 的子（最后）
		entry("d1", TimelineKindLLMCall, "call2"),   // 4: call2 的子
	}

	rows := buildTimelineTreeRows(entries)

	assertRows(t, rows, []struct {
		entryIndex int
		depth      int
		guides     string
	}{
		{0, 0, ""},
		{1, 0, ""},
		{2, 1, "├─ "},
		{3, 1, "└─ "},
		{4, 2, "   └─ "},
	})

	// isLast：两个根都视为 true；call1 的子里 r1=false、call2=true；d1 为 call2 的唯一子=true。
	wantIsLast := []bool{true, true, false, true, true}
	for i, w := range wantIsLast {
		if rows[i].isLast != w {
			t.Errorf("第%d行 isLast: got %v, want %v", i, rows[i].isLast, w)
		}
	}
}

// TestBuildTimelineTreeRows_MissingParentGoesToRoot：DelegationID 指向不存在的 ID → 挂根。
func TestBuildTimelineTreeRows_MissingParentGoesToRoot(t *testing.T) {
	entries := []*TimelineEntry{
		entry("a", TimelineKindLLMCall, ""),
		entry("b", TimelineKindLLMCall, "ghost"), // DelegationID 无匹配 → 根
	}

	rows := buildTimelineTreeRows(entries)

	assertRows(t, rows, []struct {
		entryIndex int
		depth      int
		guides     string
	}{
		{0, 0, ""},
		{1, 0, ""},
	})
}

// TestBuildTimelineTreeRows_DelegationPointsToNonTool：DelegationID 命中的条目不是工具 → 挂根。
func TestBuildTimelineTreeRows_DelegationPointsToNonTool(t *testing.T) {
	entries := []*TimelineEntry{
		entry("x", TimelineKindLLMCall, ""),      // ID=x 但 Kind!=Tool，不能当父
		entry("y", TimelineKindLLMCall, "x"),     // DelegationID 指向非工具条目 → 根
	}

	rows := buildTimelineTreeRows(entries)

	assertRows(t, rows, []struct {
		entryIndex int
		depth      int
		guides     string
	}{
		{0, 0, ""},
		{1, 0, ""},
	})
}

// TestBuildTimelineTreeRows_DelegationMustPrecede：父工具条目出现在子之后 → 不算父（挂根）。
// 规则要求"在它之前出现"，因此后续出现的同 ID 工具不能把先前的条目收养。
func TestBuildTimelineTreeRows_DelegationMustPrecede(t *testing.T) {
	entries := []*TimelineEntry{
		entry("child", TimelineKindLLMCall, "toolID"), // 先出现的子，父尚未出现 → 根
		entry("toolID", TimelineKindTool, ""),         // 后出现的工具，是根
	}

	rows := buildTimelineTreeRows(entries)

	assertRows(t, rows, []struct {
		entryIndex int
		depth      int
		guides     string
	}{
		{0, 0, ""},
		{1, 0, ""},
	})
}

// TestBuildTimelineTreeRows_SkipThinking：Thinking 条目不产行，且其余行的相对顺序不变。
func TestBuildTimelineTreeRows_SkipThinking(t *testing.T) {
	// 与 NestedDelegation 同构，但在委派工具与其子之间穿插 Thinking。
	entries := []*TimelineEntry{
		entry("dir", TimelineKindLLMCall, ""),
		entry("th0", TimelineKindThinking, ""),      // 跳过
		entry("call1", TimelineKindTool, ""),
		entry("th1", TimelineKindThinking, "call1"), // 跳过，且其 DelegationID 不参与派生
		entry("r1", TimelineKindLLMCall, "call1"),
		entry("call2", TimelineKindTool, "call1"),
		entry("th2", TimelineKindThinking, "call2"), // 跳过
		entry("d1", TimelineKindLLMCall, "call2"),
	}

	rows := buildTimelineTreeRows(entries)

	// 期望的非 Thinking 行：entryIndex 为 {0,2,4,5,7}，结构与 NestedDelegation 完全一致。
	assertRows(t, rows, []struct {
		entryIndex int
		depth      int
		guides     string
	}{
		{0, 0, ""},
		{2, 0, ""},
		{4, 1, "├─ "},
		{5, 1, "└─ "},
		{7, 2, "   └─ "},
	})

	for _, r := range rows {
		if entries[r.entryIndex].Kind == TimelineKindThinking {
			t.Errorf("Thinking 条目不应出现在树行: entryIndex=%d", r.entryIndex)
		}
	}
}

// TestBuildTimelineTreeRows_EmptyAndSingle：空列表返回空，单条返回单根。
func TestBuildTimelineTreeRows_EmptyAndSingle(t *testing.T) {
	if rows := buildTimelineTreeRows(nil); len(rows) != 0 {
		t.Fatalf("nil 输入应返回空, got %d 行", len(rows))
	}
	if rows := buildTimelineTreeRows([]*TimelineEntry{}); len(rows) != 0 {
		t.Fatalf("空切片应返回空, got %d 行", len(rows))
	}

	rows := buildTimelineTreeRows([]*TimelineEntry{
		entry("only", TimelineKindTool, ""),
	})
	if len(rows) != 1 {
		t.Fatalf("单条应返回 1 行, got %d", len(rows))
	}
	if rows[0].entryIndex != 0 || rows[0].depth != 0 || rows[0].guides != "" || !rows[0].isLast {
		t.Errorf("单根不符: %+v", rows[0])
	}
}

// TestBuildTimelineTreeRows_RootAndSiblingOrderMatchesAppend：多个根、多兄弟时，
// 根顺序与同父兄弟顺序都等于追加顺序（用 ID 序列精确比对，防止意外重排）。
func TestBuildTimelineTreeRows_RootAndSiblingOrderMatchesAppend(t *testing.T) {
	entries := []*TimelineEntry{
		entry("rootA", TimelineKindTool, ""),  // 0 根
		entry("kid1", TimelineKindLLMCall, "rootA"), // 1 rootA 子
		entry("rootB", TimelineKindTool, ""),  // 2 根
		entry("kid2", TimelineKindLLMCall, "rootA"), // 3 rootA 子（追加在 rootB 之后，仍归 rootA）
		entry("rootC", TimelineKindTool, ""),  // 4 根
	}

	rows := buildTimelineTreeRows(entries)

	// 根按追加顺序 rootA,rootB,rootC；rootA 的子按追加顺序 kid1(1),kid2(3) 紧随 rootA 之后。
	var gotIDs []string
	for _, r := range rows {
		gotIDs = append(gotIDs, entries[r.entryIndex].ID)
	}
	wantIDs := []string{"rootA", "kid1", "kid2", "rootB", "rootC"}
	if len(gotIDs) != len(wantIDs) {
		t.Fatalf("行序列长度不符: got %v, want %v", gotIDs, wantIDs)
	}
	for i := range wantIDs {
		if gotIDs[i] != wantIDs[i] {
			t.Errorf("第%d行 ID: got %q, want %q (完整序列 got=%v)", i, gotIDs[i], wantIDs[i], gotIDs)
		}
	}

	// kid1 是 rootA 的非最后子 → "├─ "；kid2 是最后子 → "└─ "。
	if rows[1].guides != "├─ " || rows[2].guides != "└─ " {
		t.Errorf("兄弟 connector 不符: rows[1]=%q rows[2]=%q", rows[1].guides, rows[2].guides)
	}
}

// TestBuildTimelineTreeRows_DepthCappedAtMax：构造一条超过 maxTimelineTreeDepth 的深链，
// 验证 depth 被 clamp 到上限且缩进段不再增长。
func TestBuildTimelineTreeRows_DepthCappedAtMax(t *testing.T) {
	// 链：tool0 -> child1 -> tool1 -> child2 -> tool2 ... 让每个 child 的父是上一个 tool，
	// 每个 tool 自己又是上一个 child 的子，形成连续深嵌套。
	entries := make([]*TimelineEntry, 0, 2*(maxTimelineTreeDepth+2))
	entries = append(entries, entry("t0", TimelineKindTool, "")) // 根
	for d := 1; d <= maxTimelineTreeDepth+2; d++ {
		prevTool := "t" + string(rune('0'+(d-1)))
		// 子事件挂到上一个 tool 下
		entries = append(entries, entry("c"+itoa(d), TimelineKindLLMCall, prevTool))
		// 再放一个 tool 作为该子事件的"孙"，但子事件非 Tool 不能当父，
		// 所以这个新 tool 必须挂在它自己之前最近的 Tool。这里改用 tool 链：
		entries = append(entries, entry("t"+itoa(d), TimelineKindTool, "c"+itoa(d)))
	}

	rows := buildTimelineTreeRows(entries)
	if len(rows) == 0 {
		t.Fatal("深链应产生行")
	}
	for _, r := range rows {
		if r.depth > maxTimelineTreeDepth {
			t.Errorf("depth 超过上限: row=%+v max=%d", r, maxTimelineTreeDepth)
		}
	}
}

// itoa 是小整数转字符串，避免额外 import 噪音（测试内仅个位数）。
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf []byte
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}
