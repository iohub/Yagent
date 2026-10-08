package thinklink

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// manyValidItems 构造 n 条合法条目（pending，非空 content/active_form）。
func manyValidItems(n int) []TodoItem {
	items := make([]TodoItem, n)
	for i := range items {
		items[i] = TodoItem{
			Content:    fmt.Sprintf("task %d", i),
			ActiveForm: fmt.Sprintf("doing %d", i),
			Status:     StatusPending,
		}
	}
	return items
}

// ─── SetTodos：快照追加与 changed 语义 ───────────────────────────────────────

func TestSetTodosAppendAndChanged(t *testing.T) {
	s := NewStore(0)
	items := []TodoItem{
		{Content: "task A", ActiveForm: "doing A", Status: StatusPending},
		{Content: "task B", ActiveForm: "doing B", Status: StatusInProgress},
	}

	changed, err := s.SetTodos(items, 1)
	if err != nil {
		t.Fatalf("SetTodos(valid) failed: %v", err)
	}
	if !changed {
		t.Fatal("first SetTodos must report changed=true")
	}
	if s.TodoRevision() != 1 {
		t.Fatalf("TodoRevision = %d, want 1", s.TodoRevision())
	}

	// 追加了一条 KindTodoSnapshot 条目
	entries := s.Snapshot()
	if len(entries) != 1 || entries[0].Kind != KindTodoSnapshot {
		t.Fatalf("expected 1 KindTodoSnapshot entry, got %+v", entries)
	}

	// 快照 JSON 结构（items/revision/updated_at/step）
	var snap TodoSnapshot
	if err := json.Unmarshal([]byte(entries[0].Content), &snap); err != nil {
		t.Fatalf("snapshot content must be valid TodoSnapshot JSON: %v", err)
	}
	if snap.Revision != 1 || snap.Step != 1 {
		t.Fatalf("snapshot revision/step = %d/%d, want 1/1", snap.Revision, snap.Step)
	}
	if len(snap.Items) != 2 || snap.Items[0].Content != "task A" || snap.Items[0].ActiveForm != "doing A" || snap.Items[0].Status != StatusPending {
		t.Fatalf("unexpected snapshot items: %+v", snap.Items)
	}

	// CurrentTodos 深拷贝：修改副本不影响内部状态
	cur := s.CurrentTodos()
	if len(cur) != 2 || cur[0].Content != "task A" {
		t.Fatalf("unexpected CurrentTodos: %+v", cur)
	}
	cur[0].Status = StatusCompleted
	cur[1].Content = "mutated"
	if again := s.CurrentTodos(); again[0].Status != StatusPending || again[1].Content != "task B" {
		t.Fatalf("CurrentTodos must return a deep copy, got %+v", again)
	}
}

// ─── SetTodos：相同快照不追加（仅更新 revision/step，防刷流） ────────────────

func TestSetTodosSameSnapshotNoAppend(t *testing.T) {
	s := NewStore(0)
	items := []TodoItem{
		{Content: "task A", ActiveForm: "doing A", Status: StatusInProgress},
		{Content: "task B", ActiveForm: "doing B", Status: StatusPending},
	}
	if _, err := s.SetTodos(items, 1); err != nil {
		t.Fatalf("SetTodos failed: %v", err)
	}
	// 中间隔一条 T&P，确认 same 判断针对快照条目而非最后一条
	if _, ok := s.AddThoughtPlan("## Thought & Plan", 1); !ok {
		t.Fatal("AddThoughtPlan should succeed")
	}

	changed, err := s.SetTodos(items, 2)
	if err != nil {
		t.Fatalf("SetTodos(same items) failed: %v", err)
	}
	if changed {
		t.Fatal("identical snapshot must report changed=false")
	}

	// 不追加新条目：仍为 1 snapshot + 1 plan（快照先写入，plan 在后）
	entries := s.Snapshot()
	if len(entries) != 2 {
		t.Fatalf("identical snapshot must not append, entries = %d", len(entries))
	}
	snapEntry := entries[0]
	if snapEntry.Kind != KindTodoSnapshot {
		t.Fatalf("unexpected entry kind: %v", snapEntry.Kind)
	}
	// 该条目的 step 被更新为本次 step
	if snapEntry.Step != 2 {
		t.Fatalf("same snapshot must refresh entry step to 2, got %d", snapEntry.Step)
	}
	// revision 仍递增（"有更新"的版本信号）
	var snap TodoSnapshot
	if err := json.Unmarshal([]byte(snapEntry.Content), &snap); err != nil {
		t.Fatalf("snapshot content must be valid JSON: %v", err)
	}
	if snap.Revision != 2 || s.TodoRevision() != 2 {
		t.Fatalf("revision must increment on refresh, snap=%d store=%d", snap.Revision, s.TodoRevision())
	}
}

// ─── 超容量压力：首条用户输入与最新快照永不被淘汰 ────────────────────────────

func TestSetTodosPressureNeverEvictProtected(t *testing.T) {
	s := NewStore(DefaultMaxEntries)
	if _, ok := s.AddUserInput("original task requirement", 0); !ok {
		t.Fatal("AddUserInput should succeed")
	}
	for i := 0; i < 250; i++ {
		if _, got := s.AddThoughtPlan(fmt.Sprintf("plan for step %d", i), i+1); !got {
			t.Fatalf("AddThoughtPlan(%d) should succeed", i)
		}
		items := []TodoItem{
			{Content: fmt.Sprintf("task-%d", i), ActiveForm: fmt.Sprintf("doing %d", i), Status: StatusInProgress},
		}
		if _, err := s.SetTodos(items, i+1); err != nil {
			t.Fatalf("SetTodos(%d) failed: %v", i, err)
		}
	}

	entries := s.Snapshot()
	if len(entries) > DefaultMaxEntries {
		t.Fatalf("entries = %d, must respect capacity %d", len(entries), DefaultMaxEntries)
	}
	// 首条用户输入永不淘汰
	if entries[0].Kind != KindUserInput || entries[0].Content != "original task requirement" {
		t.Fatalf("first user input must survive eviction, got %+v", entries[0])
	}
	// 最新快照永不淘汰（钉住）：最后写入的 task-249 仍可读
	cur := s.CurrentTodos()
	if len(cur) != 1 || cur[0].Content != "task-249" {
		t.Fatalf("latest snapshot must survive eviction, got %+v", cur)
	}
	// 最后一条条目即最新快照
	last := entries[len(entries)-1]
	if last.Kind != KindTodoSnapshot {
		t.Fatalf("last entry must be the pinned latest snapshot, got kind %v", last.Kind)
	}
	var snap TodoSnapshot
	if err := json.Unmarshal([]byte(last.Content), &snap); err != nil {
		t.Fatalf("last snapshot content must be valid JSON: %v", err)
	}
	if len(snap.Items) != 1 || snap.Items[0].Content != "task-249" {
		t.Fatalf("pinned snapshot must be the latest, got %+v", snap.Items)
	}
	// 旧快照优先于普通条目被淘汰：混合流下旧快照不应大量存活
	snapCount := s.Count(KindTodoSnapshot)
	if snapCount != 1 {
		t.Fatalf("old snapshots must be evicted first, remaining snapshots = %d, want 1", snapCount)
	}
}

// ─── 台账迁移：完成、删除不算完成、重现按 content 去重 ──────────────────────

func TestCompletedLedgerMigration(t *testing.T) {
	s := NewStore(0)
	// 初始：A/B/C 均 pending
	items1 := []TodoItem{
		{Content: "task A", ActiveForm: "doing A", Status: StatusPending},
		{Content: "task B", ActiveForm: "doing B", Status: StatusPending},
		{Content: "task C", ActiveForm: "doing C", Status: StatusPending},
	}
	if _, err := s.SetTodos(items1, 1); err != nil {
		t.Fatalf("SetTodos(1) failed: %v", err)
	}
	if got := s.CompletedLedger(); len(got) != 0 {
		t.Fatalf("no completed yet, ledger = %+v", got)
	}

	// A completed（pending → completed）→ 台账追加 A
	items2 := []TodoItem{
		{Content: "task A", ActiveForm: "doing A", Status: StatusCompleted},
		{Content: "task B", ActiveForm: "doing B", Status: StatusInProgress},
		{Content: "task C", ActiveForm: "doing C", Status: StatusPending},
	}
	if _, err := s.SetTodos(items2, 2); err != nil {
		t.Fatalf("SetTodos(2) failed: %v", err)
	}
	ledger := s.CompletedLedger()
	if len(ledger) != 1 || ledger[0].Content != "task A" || ledger[0].ActiveForm != "doing A" {
		t.Fatalf("ledger after A completed = %+v, want [task A]", ledger)
	}
	if ledger[0].CompletedAtStep != 2 {
		t.Fatalf("CompletedAtStep = %d, want 2", ledger[0].CompletedAtStep)
	}
	if ledger[0].CompletedAtTime.IsZero() {
		t.Fatal("CompletedAtTime must be set")
	}

	// C completed（in_progress 场景外的 pending → completed）+ A 被删除
	//   - A 删除不算"完成"，台账不新增 A 记录
	//   - C 由 pending 变 completed，台账追加 C
	items3 := []TodoItem{
		{Content: "task B", ActiveForm: "doing B", Status: StatusInProgress},
		{Content: "task C", ActiveForm: "doing C", Status: StatusCompleted},
	}
	if _, err := s.SetTodos(items3, 3); err != nil {
		t.Fatalf("SetTodos(3) failed: %v", err)
	}
	ledger = s.CompletedLedger()
	if len(ledger) != 2 {
		t.Fatalf("ledger after A deleted + C completed = %+v, want [task A, task C]", ledger)
	}
	for _, le := range ledger {
		if le.Content == "task A" && le.CompletedAtStep != 2 {
			t.Fatalf("deleted item must not create a new ledger record, got %+v", ledger)
		}
	}

	// A 重新出现且 completed → 按 content 匹配台账去重，不重复记录
	// B in_progress → completed → 台账追加 B
	items4 := []TodoItem{
		{Content: "task A", ActiveForm: "doing A", Status: StatusCompleted},
		{Content: "task B", ActiveForm: "doing B", Status: StatusCompleted},
		{Content: "task C", ActiveForm: "doing C", Status: StatusCompleted},
	}
	if _, err := s.SetTodos(items4, 4); err != nil {
		t.Fatalf("SetTodos(4) failed: %v", err)
	}
	ledger = s.CompletedLedger()
	if len(ledger) != 3 {
		t.Fatalf("ledger after re-appearance = %+v, want 3 unique records", ledger)
	}
	aCount := 0
	for _, le := range ledger {
		if le.Content == "task A" {
			aCount++
		}
	}
	if aCount != 1 {
		t.Fatalf("re-appeared completed item must be deduped by content, task A records = %d, want 1", aCount)
	}

	// 相同快照重复调用 → 迁移幂等，台账不变
	if _, err := s.SetTodos(items4, 5); err != nil {
		t.Fatalf("SetTodos(5, same) failed: %v", err)
	}
	if got := s.CompletedLedger(); len(got) != 3 {
		t.Fatalf("idempotent re-write must not touch ledger, ledger = %+v", got)
	}
}

// ─── 空数组清空 ──────────────────────────────────────────────────────────────

func TestSetTodosClear(t *testing.T) {
	s := NewStore(0)
	if _, err := s.SetTodos(manyValidItems(2), 1); err != nil {
		t.Fatalf("SetTodos(2 items) failed: %v", err)
	}

	changed, err := s.SetTodos([]TodoItem{}, 2)
	if err != nil {
		t.Fatalf("SetTodos(empty) failed: %v", err)
	}
	if !changed {
		t.Fatal("clearing a non-empty list must report changed=true")
	}
	if cur := s.CurrentTodos(); len(cur) != 0 {
		t.Fatalf("CurrentTodos after clear = %+v, want empty", cur)
	}
	// 清空动作本身产生一条空快照（历史记录"清空"）
	if n := s.Count(KindTodoSnapshot); n != 2 {
		t.Fatalf("clear must append an empty snapshot, snapshots = %d, want 2", n)
	}

	// 连续清空：与上一条快照相同 → 不追加
	changed2, err := s.SetTodos([]TodoItem{}, 3)
	if err != nil {
		t.Fatalf("SetTodos(empty again) failed: %v", err)
	}
	if changed2 {
		t.Fatal("clearing an already-empty list must report changed=false")
	}
	if n := s.Count(KindTodoSnapshot); n != 2 {
		t.Fatalf("repeated clear must not append, snapshots = %d, want 2", n)
	}
	if rev := s.TodoRevision(); rev != 3 {
		t.Fatalf("revision must still increment on same-snapshot refresh, got %d, want 3", rev)
	}
}

// ─── 确定性校验：非法输入报错且不写入 ────────────────────────────────────────

func TestSetTodosValidationErrors(t *testing.T) {
	cases := []struct {
		name        string
		items       []TodoItem
		wantErrPart string
	}{
		{
			name: "two in_progress",
			items: []TodoItem{
				{Content: "a", ActiveForm: "doing a", Status: StatusInProgress},
				{Content: "b", ActiveForm: "doing b", Status: StatusInProgress},
			},
			wantErrPart: "at most 1 todo item can be",
		},
		{
			name:        "invalid status",
			items:       []TodoItem{{Content: "a", ActiveForm: "doing a", Status: TodoStatus("done")}},
			wantErrPart: "status must be one of",
		},
		{
			name:        "blank content",
			items:       []TodoItem{{Content: "  ", ActiveForm: "doing", Status: StatusPending}},
			wantErrPart: "content must be a non-empty string",
		},
		{
			name:        "blank active form",
			items:       []TodoItem{{Content: "a", ActiveForm: " \t ", Status: StatusPending}},
			wantErrPart: "active_form must be a non-empty string",
		},
		{
			name:        "too many items",
			items:       manyValidItems(MaxTodoItems + 1),
			wantErrPart: fmt.Sprintf("at most %d items", MaxTodoItems),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := NewStore(0)
			// 预置一条合法快照，验证非法输入不产生任何写入
			if _, err := s.SetTodos(manyValidItems(1), 1); err != nil {
				t.Fatalf("preset SetTodos failed: %v", err)
			}
			before := s.Len()
			revBefore := s.TodoRevision()

			changed, err := s.SetTodos(tc.items, 2)
			if err == nil {
				t.Fatalf("expected validation error, got changed=%v", changed)
			}
			if changed {
				t.Fatal("failed SetTodos must report changed=false")
			}
			if !strings.Contains(err.Error(), tc.wantErrPart) {
				t.Fatalf("error %q must contain %q", err.Error(), tc.wantErrPart)
			}
			if s.Len() != before {
				t.Fatalf("failed SetTodos must not write, entries before/after = %d/%d", before, s.Len())
			}
			if s.TodoRevision() != revBefore {
				t.Fatalf("failed SetTodos must not bump revision, before/after = %d/%d", revBefore, s.TodoRevision())
			}
			if cur := s.CurrentTodos(); len(cur) != 1 || cur[0].Content != "task 0" {
				t.Fatalf("failed SetTodos must not alter current snapshot, got %+v", cur)
			}
		})
	}
}

// ─── TodoHistory：最近 n 条快照（旧→新） ─────────────────────────────────────

func TestTodoHistory(t *testing.T) {
	s := NewStore(0)
	for i := 0; i < 5; i++ {
		if _, err := s.SetTodos(manyValidItems(i+1), i+1); err != nil {
			t.Fatalf("SetTodos(%d) failed: %v", i, err)
		}
	}

	hist := s.TodoHistory(3)
	if len(hist) != 3 {
		t.Fatalf("TodoHistory(3) = %d snapshots, want 3", len(hist))
	}
	// 旧→新：最新一条 items 最长（5 条）
	if len(hist[2].Items) != 5 {
		t.Fatalf("latest history entry must have 5 items, got %d", len(hist[2].Items))
	}
	if len(hist[0].Items) != 3 {
		t.Fatalf("history must be ordered old→new, first returned entry has %d items, want 3", len(hist[0].Items))
	}

	all := s.TodoHistory(0)
	if len(all) != 5 {
		t.Fatalf("TodoHistory(0) must return all, got %d", len(all))
	}
}

// ─── 台账 FIFO 容量 ─────────────────────────────────────────────────────────

func TestCompletedLedgerFIFO(t *testing.T) {
	s := NewStore(0)
	total := DefaultMaxLedgerEntries + 50 // 250 次，台账应 FIFO 裁剪到 200
	for i := 0; i < total; i++ {
		items := []TodoItem{
			{Content: fmt.Sprintf("task-%d", i), ActiveForm: "doing", Status: StatusCompleted},
		}
		if _, err := s.SetTodos(items, i+1); err != nil {
			t.Fatalf("SetTodos(%d) failed: %v", i, err)
		}
	}
	ledger := s.CompletedLedger()
	if len(ledger) != DefaultMaxLedgerEntries {
		t.Fatalf("ledger size = %d, want %d", len(ledger), DefaultMaxLedgerEntries)
	}
	// 最旧的 50 条被 FIFO 淘汰：首条为 task-50，末条为 task-249
	if ledger[0].Content != fmt.Sprintf("task-%d", total-DefaultMaxLedgerEntries) {
		t.Fatalf("FIFO must evict oldest first, first = %q, want %q", ledger[0].Content, fmt.Sprintf("task-%d", total-DefaultMaxLedgerEntries))
	}
	if ledger[len(ledger)-1].Content != fmt.Sprintf("task-%d", total-1) {
		t.Fatalf("FIFO must keep newest last, last = %q", ledger[len(ledger)-1].Content)
	}
}

// ─── CurrentTodos：无快照时返回 nil ─────────────────────────────────────────

func TestCurrentTodosNoSnapshot(t *testing.T) {
	s := NewStore(0)
	if cur := s.CurrentTodos(); cur != nil {
		t.Fatalf("CurrentTodos without any snapshot must be nil, got %+v", cur)
	}
	if cur := s.CurrentTodos(); len(cur) != 0 {
		t.Fatalf("CurrentTodos without any snapshot must be empty, got %d", len(cur))
	}
}
