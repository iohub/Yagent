package thinklink

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// ─── AddUserInput：正常添加与空白拒绝 ───────────────────────────────────────

func TestAddUserInputBasicAndBlankRejection(t *testing.T) {
	s := NewStore(0)

	// 空白内容拒绝（表驱动）
	blankCases := []struct {
		name    string
		content string
	}{
		{"empty string", ""},
		{"spaces only", "   "},
		{"newline and tab", " \n\t "},
	}
	for _, tc := range blankCases {
		if _, got := s.AddUserInput(tc.content, 0); got {
			t.Errorf("AddUserInput(%q) must reject blank content", tc.content)
		}
	}
	if s.Len() != 0 {
		t.Fatalf("Len = %d, want 0 after all blank rejections", s.Len())
	}

	// 正常添加
	s.AddUserInput("build the parser", 0)
}

// ─── 连续两条相同用户输入去重 ────────────────────────────────────────────────

func TestAddUserInputDedup(t *testing.T) {
	s := NewStore(0)
	if _, ok := s.AddUserInput("task A", 0); !ok {
		t.Fatal("first user input should be added")
	}
	if _, ok := s.AddUserInput("task A", 1); ok {
		t.Fatal("consecutive duplicate user input should be skipped")
	}
	if s.Len() != 1 {
		t.Fatalf("Len = %d, want 1 after dedup", s.Len())
	}

	// 中间隔了一条 TODO 快照后，相同内容的用户输入允许再次写入
	items := []TodoItem{{Content: "temp", ActiveForm: "doing", Status: StatusPending}}
	_, err := s.SetTodos(items, 1)
	if err != nil || s.Len() != 2 {
		t.Fatal("snapshot should be added to break dedup")
	}
	if _, ok := s.AddUserInput("task A", 2); !ok {
		t.Fatal("same user input after a snapshot should be added")
	}
	if s.Len() != 3 {
		t.Fatalf("Len = %d, want 3", s.Len())
	}

	// 不同内容不触发去重
	if _, ok := s.AddUserInput("task B", 2); !ok {
		t.Fatal("different user input should be added")
	}
	if s.Len() != 4 {
		t.Fatalf("Len = %d, want 4", s.Len())
	}
}

// ─── 容量淘汰：超过 200 条后 Len()==200，第一条用户原始输入永久保留 ──────────

func TestCapacityEvictionKeepsFirstUserInput(t *testing.T) {
	s := NewStore(0)
	if _, ok := s.AddUserInput("the original task", 0); !ok {
		t.Fatal("first user input should be added")
	}
	const extra = 250
	for i := 0; i < extra; i++ {
		// 使用不同内容和不同 step 来避免去重
		if _, ok := s.AddUserInput(fmt.Sprintf("input %d", i), i+1); !ok {
			t.Errorf("AddUserInput(%d) failed unexpectedly", i)
		}
	}

	if got := s.Len(); got != DefaultMaxEntries {
		t.Fatalf("Len = %d, want %d (capacity)", got, DefaultMaxEntries)
	}

	entries := s.Snapshot()
	if len(entries) != DefaultMaxEntries {
		t.Fatalf("Snapshot len = %d, want %d", len(entries), DefaultMaxEntries)
	}
	// 第一条必须仍是最初的用户原始输入
	first := entries[0]
	if first.Kind != KindUserInput || first.Content != "the original task" {
		t.Fatalf("first user input was evicted, got: %+v", first)
	}
	// 最后一条应是最新的用户输入
	last := entries[len(entries)-1]
	if last.Kind != KindUserInput || last.Content != fmt.Sprintf("input %d", extra-1) {
		t.Fatalf("last entry should be the newest input, got: %+v", last)
	}
	// 最旧的用户输入应已被淘汰（除了首条 protection）
	for _, e := range entries[1:] {
		if e.Content == "input 0" {
			t.Fatalf("oldest input should have been evicted: %+v", e)
		}
	}
}

// ─── Snapshot：深拷贝语义 ────────────────────────────────────────────────────

func TestSnapshotIsDeepCopy(t *testing.T) {
	s := NewStore(0)
	s.AddUserInput("task", 0)

	snap := s.Snapshot()
	snap[0].Content = "mutated"
	snap = append(snap, Entry{Kind: KindUserInput, Content: "fake"})

	fresh := s.Snapshot()
	if fresh[0].Content != "task" {
		t.Fatalf("Snapshot must be a deep copy, mutation leaked: %q", fresh[0].Content)
	}
	if s.Len() != 1 {
		t.Fatalf("append to snapshot must not affect store, Len = %d", s.Len())
	}
}

// ─── 并发：10 个 goroutine 各加 50 条用户输入，附加并发读 ────────────────────

func TestConcurrentAccess(t *testing.T) {
	s := NewStore(0)

	var wg sync.WaitGroup
	// 写者：10 个 goroutine，各 50 轮，每轮写入一条用户输入（使用不同后缀避免去重）
	for w := 0; w < 10; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				s.AddUserInput(fmt.Sprintf("user %d-%d", w, i), i)
			}
		}(w)
	}
	// 读者：并发调用只读方法，配合 -race 检测数据竞争
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				_ = s.Snapshot()
				_ = s.Len()
			}
		}()
	}
	wg.Wait()

	// 写入总量 10*50 = 500 条，超容量后应稳定在 200 条
	if got := s.Len(); got != DefaultMaxEntries {
		t.Fatalf("Len after concurrent writes = %d, want %d (capacity)", got, DefaultMaxEntries)
	}

	entries := s.Snapshot()
	if len(entries) != DefaultMaxEntries {
		t.Fatalf("Snapshot len = %d, want %d", len(entries), DefaultMaxEntries)
	}
	// 第一条是最早写入的用户原始输入，必须被保留
	if entries[0].Kind != KindUserInput {
		t.Fatalf("first entry should be the protected user input, got kind %v", entries[0].Kind)
	}
	// 条目顺序保持插入序：时间戳单调不减
	for i := 1; i < len(entries); i++ {
		if entries[i].Timestamp.Before(entries[i-1].Timestamp) {
			t.Fatalf("timestamps not ordered at index %d", i)
		}
	}
	// 容量压力下的不变量：首条用户输入必须存在于快照中
	firstContent := entries[0].Content
	found := false
	for _, e := range entries {
		if e.Content == firstContent {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("First user input %q was evicted", firstContent)
	}
}

// ─── Count：按 Kind 统计条目数（线程安全） ───────────────────────────────────

func TestCountByKind(t *testing.T) {
	s := NewStore(0)

	if got := s.Count(KindUserInput); got != 0 {
		t.Fatalf("Count(KindUserInput) = %d, want 0 on empty store", got)
	}
	if got := s.Count(KindTodoSnapshot); got != 0 {
		t.Fatalf("Count(KindTodoSnapshot) = %d, want 0 on empty store", got)
	}

	s.AddUserInput("task", 0)
	for i := 1; i <= 5; i++ {
		items := []TodoItem{{Content: fmt.Sprintf("task %d", i), ActiveForm: "doing", Status: StatusPending}}
		s.SetTodos(items, i)
	}

	if got := s.Count(KindUserInput); got != 1 {
		t.Fatalf("Count(KindUserInput) = %d, want 1", got)
	}
	if got := s.Count(KindTodoSnapshot); got != 5 {
		t.Fatalf("Count(KindTodoSnapshot) = %d, want 5", got)
	}
	if total := s.Len(); total != s.Count(KindUserInput)+s.Count(KindTodoSnapshot) {
		t.Fatalf("Len = %d must equal Count sum = %d",
			total, s.Count(KindUserInput)+s.Count(KindTodoSnapshot))
	}

	// 容量淘汰：1 用户输入 + 250 快照 = 251 > 默认上限 200，
	// 超限部分按"非最新的旧快照优先（最旧先）"淘汰 51 条，Len 收敛回上限
	for i := 6; i <= 250; i++ {
		items := []TodoItem{{Content: fmt.Sprintf("task-%d", i), ActiveForm: "doing", Status: StatusPending}}
		s.SetTodos(items, i)
	}
	// 容量语义是"超上限时收敛"，不是激进修剪：199 个旧快照保留供历史回溯
	if got := s.Len(); got != DefaultMaxEntries {
		t.Fatalf("Len after eviction = %d, want %d (capacity cap)", got, DefaultMaxEntries)
	}
	// 第一条用户输入受保护，永不淘汰
	if got := s.Count(KindUserInput); got != 1 {
		t.Fatalf("Count(KindUserInput) after eviction = %d, want 1 (first user input protected)", got)
	}
	// 51 个最旧非最新快照被淘汰：250-51=199 = 上限-1（首条用户输入占一个坑位）
	if got := s.Count(KindTodoSnapshot); got != DefaultMaxEntries-1 {
		t.Fatalf("Count(KindTodoSnapshot) after eviction = %d, want %d (capacity, not aggressive pruning)",
			got, DefaultMaxEntries-1)
	}
	// 淘汰后 Count 求和仍与 Len 一致（不变量）
	if total := s.Len(); total != s.Count(KindUserInput)+s.Count(KindTodoSnapshot) {
		t.Fatalf("Len = %d must equal Count sum = %d after eviction",
			total, s.Count(KindUserInput)+s.Count(KindTodoSnapshot))
	}

	entries := s.Snapshot()
	// Snapshot 首条仍是受保护的用户输入
	if entries[0].Kind != KindUserInput || entries[0].Content != "task" {
		t.Fatalf("Snapshot()[0] = {Kind: %s, Content: %q}, want protected user input \"task\"",
			entries[0].Kind, entries[0].Content)
	}
	// 最后一条是最新写入的快照（钉住，永不淘汰）
	last := entries[len(entries)-1]
	if last.Kind != KindTodoSnapshot || last.Step != 250 || !strings.Contains(last.Content, "task-250") {
		t.Fatalf("Snapshot() last = {Kind: %s, Step: %d, Content: %q}, want pinned latest snapshot",
			last.Kind, last.Step, last.Content)
	}
	// 最早写入的快照（step 1、6）已被淘汰
	for _, evictedStep := range []int{1, 6} {
		for _, e := range entries {
			if e.Kind == KindTodoSnapshot && e.Step == evictedStep {
				t.Fatalf("snapshot at step %d should have been evicted", evictedStep)
			}
		}
	}
}

// ─── Kind.String：稳定字符串表示 ─────────────────────────────────────────────

func TestKindString(t *testing.T) {
	cases := []struct {
		kind Kind
		want string
	}{
		{KindUserInput, "user_input"},
		{KindTodoSnapshot, "todo_snapshot"},
		{Kind(42), "kind(42)"},
	}
	for _, tc := range cases {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("Kind(%d).String() = %q, want %q", int(tc.kind), got, tc.want)
		}
	}
}

// ─── NewStore：容量参数语义（<=0 回落默认；自定义容量生效且保护首条用户输入） ─

func TestNewStoreCapacity(t *testing.T) {
	// <=0 → DefaultMaxEntries
	for _, c := range []int{0, -1} {
		if s := NewStore(c); s.maxEntries != DefaultMaxEntries {
			t.Fatalf("NewStore(%d).maxEntries = %d, want %d", c, s.maxEntries, DefaultMaxEntries)
		}
	}

	// 自定义容量生效
	const cap = 10
	s := NewStore(cap)
	if s.maxEntries != cap {
		t.Fatalf("NewStore(%d).maxEntries = %d, want %d", cap, s.maxEntries, cap)
	}
	entry, ok := s.AddUserInput("the original task", 0)
	if !ok {
		t.Fatal("AddUserInput should succeed")
	}
	if !strings.Contains(entry.ID, "tl-") {
		t.Fatal("AddUserInput must return the appended entry with an ID")
	}
	for i := 0; i < 25; i++ {
		if _, ok := s.AddUserInput(fmt.Sprintf("input %d", i), i+1); !ok {
			t.Errorf("AddUserInput(%d) failed", i)
		}
	}
	if got := s.Len(); got != cap {
		t.Fatalf("Len = %d, want %d (custom capacity)", got, cap)
	}
	entries := s.Snapshot()
	if entries[0].Kind != KindUserInput || entries[0].Content != "the original task" {
		t.Fatalf("first user input must survive eviction under custom capacity: %+v", entries[0])
	}
	lastContent := entries[len(entries)-1].Content
	if lastContent != "input 24" {
		t.Fatalf("last entry should be newest input, got: %+v", entries[len(entries)-1])
	}
}
