package thinklink

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// ─── AddUserInput / AddThoughtPlan：正常添加与空白拒绝 ───────────────────────

func TestAddBasicAndBlankRejection(t *testing.T) {
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
		if _, got := s.AddThoughtPlan(tc.content, 0); got {
			t.Errorf("AddThoughtPlan(%q) must reject blank content", tc.content)
		}
	}
	if s.Len() != 0 {
		t.Fatalf("Len = %d, want 0 after all blank rejections", s.Len())
	}

	// 正常添加
	if _, got := s.AddUserInput("build the parser", 0); !got {
		t.Fatal("AddUserInput should return true on success")
	}
	if _, got := s.AddThoughtPlan("## Thought & Plan\nstep 1", 1); !got {
		t.Fatal("AddThoughtPlan should return true on success")
	}
	entries := s.Snapshot()
	if len(entries) != 2 {
		t.Fatalf("Len = %d, want 2", len(entries))
	}
	if entries[0].Kind != KindUserInput || entries[0].Content != "build the parser" || entries[0].Step != 0 {
		t.Fatalf("unexpected first entry: %+v", entries[0])
	}
	if entries[1].Kind != KindThoughtPlan || entries[1].Step != 1 {
		t.Fatalf("unexpected second entry: %+v", entries[1])
	}
	if entries[0].ID == "" {
		t.Fatal("Entry.ID must be generated (timestamp+sequence)")
	}
	if entries[0].ID == entries[1].ID {
		t.Fatalf("Entry.ID must be unique, got duplicated %q", entries[0].ID)
	}
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

	// 中间隔了一条 T&P 后，相同内容的用户输入允许再次写入
	if _, ok := s.AddThoughtPlan("plan", 1); !ok {
		t.Fatal("thought plan should be added")
	}
	if _, ok := s.AddUserInput("task A", 2); !ok {
		t.Fatal("same user input after a plan entry should be added")
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
		s.AddThoughtPlan(fmt.Sprintf("plan %d", i), i+1)
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
	// 最后一条应是最新的 plan
	last := entries[len(entries)-1]
	if last.Kind != KindThoughtPlan || last.Content != fmt.Sprintf("plan %d", extra-1) {
		t.Fatalf("last entry should be the newest plan, got: %+v", last)
	}
	// 最旧的 plan 应已被淘汰
	for _, e := range entries[1:] {
		if e.Content == "plan 0" {
			t.Fatalf("oldest plan should have been evicted: %+v", e)
		}
	}
}

// ─── Snapshot：深拷贝语义 ────────────────────────────────────────────────────

func TestSnapshotIsDeepCopy(t *testing.T) {
	s := NewStore(0)
	s.AddUserInput("task", 0)

	snap := s.Snapshot()
	snap[0].Content = "mutated"
	snap = append(snap, Entry{Kind: KindThoughtPlan, Content: "fake"})

	fresh := s.Snapshot()
	if fresh[0].Content != "task" {
		t.Fatalf("Snapshot must be a deep copy, mutation leaked: %q", fresh[0].Content)
	}
	if s.Len() != 1 {
		t.Fatalf("append to snapshot must not affect store, Len = %d", s.Len())
	}
}

// ─── RebuildPrompt：说明段 / [CURRENT TASK] / [TP-n] 序号 ────────────────────

func TestRebuildPromptFormat(t *testing.T) {
	s := NewStore(0)
	s.AddUserInput("first task", 0)
	s.AddThoughtPlan("## Thought & Plan\nstep one", 1)
	s.AddThoughtPlan("## Thought & Plan\nstep two", 2)
	s.AddUserInput("second task (current)", 3)

	prompt := s.RebuildPrompt(0)

	// 顶部说明段关键词
	headerKeywords := []string{
		"RESET", "token limit",
		"original requirements", "Thought & Plan progress",
		"Seamlessly continue", "do NOT ask", "do NOT apologize", "do NOT repeat",
	}
	for _, kw := range headerKeywords {
		if !strings.Contains(prompt, kw) {
			t.Fatalf("prompt header missing keyword %q:\n%s", kw, prompt)
		}
	}
	// 区块标题
	for _, kw := range []string{"=== Original user input(s) ===", "=== Thought & Plan blocks ==="} {
		if !strings.Contains(prompt, kw) {
			t.Fatalf("prompt missing section %q:\n%s", kw, prompt)
		}
	}
	// 两条用户输入都在
	if !strings.Contains(prompt, "first task") || !strings.Contains(prompt, "second task (current)") {
		t.Fatalf("prompt missing user inputs:\n%s", prompt)
	}
	// [CURRENT TASK] 必须出现在最后一条用户输入条目之前
	firstIdx := strings.Index(prompt, "[USER INPUT 1]")
	lastIdx := strings.Index(prompt, "[USER INPUT 2]")
	if firstIdx < 0 || lastIdx < 0 {
		t.Fatalf("prompt missing user input numbering:\n%s", prompt)
	}
	markerIdx := strings.Index(prompt, "[CURRENT TASK]")
	if markerIdx < 0 {
		t.Fatalf("prompt missing [CURRENT TASK] marker:\n%s", prompt)
	}
	if markerIdx < firstIdx || markerIdx > lastIdx {
		t.Fatalf("[CURRENT TASK] must appear before the last user input (marker=%d, first=%d, last=%d)",
			markerIdx, firstIdx, lastIdx)
	}
	// T&P 序号与时间戳
	for i := 1; i <= 2; i++ {
		if !strings.Contains(prompt, fmt.Sprintf("[TP-%d]", i)) {
			t.Fatalf("prompt missing T&P numbering [TP-%d]:\n%s", i, prompt)
		}
	}
	if !strings.Contains(prompt, "step one") || !strings.Contains(prompt, "step two") {
		t.Fatalf("prompt missing T&P contents:\n%s", prompt)
	}
	if !strings.Contains(prompt, "(") || !strings.Contains(prompt, "step 1") {
		t.Fatalf("prompt missing T&P timestamp/step metadata:\n%s", prompt)
	}
}

func TestRebuildPromptKeepPlans(t *testing.T) {
	s := NewStore(0)
	s.AddUserInput("task", 0)
	for i := 1; i <= 5; i++ {
		s.AddThoughtPlan(fmt.Sprintf("plan %d", i), i)
	}

	// keepPlans<=0 → 全部保留
	all := s.RebuildPrompt(0)
	for i := 1; i <= 5; i++ {
		if !strings.Contains(all, fmt.Sprintf("[TP-%d]", i)) || !strings.Contains(all, fmt.Sprintf("plan %d", i)) {
			t.Fatalf("keepPlans=0 should keep all plans, missing [TP-%d]:\n%s", i, all)
		}
	}

	// keepPlans=2 → 只出现最近 2 个块（[TP-4]、[TP-5]，全局序号），用户输入始终保留
	trimmed := s.RebuildPrompt(2)
	if !strings.Contains(trimmed, "[TP-4]") || !strings.Contains(trimmed, "plan 4") {
		t.Fatalf("keepPlans=2 should keep plan 4:\n%s", trimmed)
	}
	if !strings.Contains(trimmed, "[TP-5]") || !strings.Contains(trimmed, "plan 5") {
		t.Fatalf("keepPlans=2 should keep plan 5:\n%s", trimmed)
	}
	for i := 1; i <= 3; i++ {
		if strings.Contains(trimmed, fmt.Sprintf("[TP-%d]", i)) {
			t.Fatalf("keepPlans=2 should drop [TP-%d]:\n%s", i, trimmed)
		}
		if strings.Contains(trimmed, fmt.Sprintf("plan %d", i)) {
			t.Fatalf("keepPlans=2 should drop plan %d content:\n%s", i, trimmed)
		}
	}
	if !strings.Contains(trimmed, "task") {
		t.Fatalf("user input must always be kept regardless of keepPlans:\n%s", trimmed)
	}
	if !strings.Contains(trimmed, "[CURRENT TASK]") {
		t.Fatalf("keepPlans trimming must not affect [CURRENT TASK] marker:\n%s", trimmed)
	}

	// keepPlans 超过总数 → 全部保留
	over := s.RebuildPrompt(100)
	if !strings.Contains(over, "[TP-1]") || !strings.Contains(over, "plan 1") ||
		!strings.Contains(over, "[TP-5]") || !strings.Contains(over, "plan 5") {
		t.Fatalf("keepPlans > total should keep all plans:\n%s", over)
	}

	// 负数等价于全部保留
	neg := s.RebuildPrompt(-1)
	if !strings.Contains(neg, "[TP-1]") || !strings.Contains(neg, "[TP-5]") {
		t.Fatalf("keepPlans<0 should keep all plans:\n%s", neg)
	}
}

func TestRebuildPromptEmpty(t *testing.T) {
	s := NewStore(0)
	prompt := s.RebuildPrompt(0)
	if !strings.Contains(prompt, "(no user input recorded)") ||
		!strings.Contains(prompt, "(no Thought & Plan blocks recorded)") {
		t.Fatalf("empty store prompt should contain placeholders:\n%s", prompt)
	}
}

// ─── 并发：10 个 goroutine 各加 50 条混合条目，附加并发读 ────────────────────

func TestConcurrentAccess(t *testing.T) {
	s := NewStore(0)

	var wg sync.WaitGroup
	// 写者：10 个 goroutine，各 50 轮，每轮混合写入一条用户输入 + 一条 T&P
	for w := 0; w < 10; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				s.AddUserInput(fmt.Sprintf("user %d-%d", w, i), i)
				s.AddThoughtPlan(fmt.Sprintf("plan %d-%d", w, i), i)
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
				_ = s.RebuildPrompt(3)
			}
		}()
	}
	wg.Wait()

	// 写入总量 10*50*2 = 1000 条，超容量后应稳定在 200 条
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
	// RebuildPrompt 在满容量下仍可用，且包含受保护的第一条用户输入
	prompt := s.RebuildPrompt(0)
	if !strings.Contains(prompt, entries[0].Content) {
		t.Fatalf("RebuildPrompt must contain the protected first user input %q", entries[0].Content)
	}
	if !strings.Contains(prompt, "[CURRENT TASK]") {
		t.Fatal("RebuildPrompt must contain [CURRENT TASK] marker under capacity pressure")
	}
}

// ─── Count：按 Kind 统计条目数（线程安全） ───────────────────────────────────

func TestCountByKind(t *testing.T) {
	s := NewStore(0)

	if got := s.Count(KindUserInput); got != 0 {
		t.Fatalf("Count(KindUserInput) = %d, want 0 on empty store", got)
	}
	if got := s.Count(KindThoughtPlan); got != 0 {
		t.Fatalf("Count(KindThoughtPlan) = %d, want 0 on empty store", got)
	}

	s.AddUserInput("task", 0)
	for i := 1; i <= 5; i++ {
		s.AddThoughtPlan(fmt.Sprintf("plan %d", i), i)
	}

	if got := s.Count(KindUserInput); got != 1 {
		t.Fatalf("Count(KindUserInput) = %d, want 1", got)
	}
	if got := s.Count(KindThoughtPlan); got != 5 {
		t.Fatalf("Count(KindThoughtPlan) = %d, want 5", got)
	}
	if total := s.Len(); total != s.Count(KindUserInput)+s.Count(KindThoughtPlan) {
		t.Fatalf("Len = %d must equal Count sum = %d",
			total, s.Count(KindUserInput)+s.Count(KindThoughtPlan))
	}

	// 超容量淘汰后，Count 与实际剩余条目保持一致
	for i := 6; i <= 250; i++ {
		s.AddThoughtPlan(fmt.Sprintf("plan %d", i), i)
	}
	// 唯一的用户输入受保护不被淘汰，plans 只剩 maxEntries-1 条
	if got := s.Count(KindUserInput); got != 1 {
		t.Fatalf("Count(KindUserInput) after eviction = %d, want 1 (protected)", got)
	}
	if got := s.Count(KindThoughtPlan); got != DefaultMaxEntries-1 {
		t.Fatalf("Count(KindThoughtPlan) after eviction = %d, want %d",
			got, DefaultMaxEntries-1)
	}
}

// ─── Kind.String：稳定字符串表示 ─────────────────────────────────────────────

func TestKindString(t *testing.T) {
	cases := []struct {
		kind Kind
		want string
	}{
		{KindUserInput, "user_input"},
		{KindThoughtPlan, "thought_plan"},
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
		if _, ok := s.AddThoughtPlan(fmt.Sprintf("plan %d", i), i+1); !ok {
			t.Fatalf("AddThoughtPlan(%d) should succeed", i)
		}
	}
	if got := s.Len(); got != cap {
		t.Fatalf("Len = %d, want %d (custom capacity)", got, cap)
	}
	entries := s.Snapshot()
	if entries[0].Kind != KindUserInput || entries[0].Content != "the original task" {
		t.Fatalf("first user input must survive eviction under custom capacity: %+v", entries[0])
	}
	if entries[len(entries)-1].Content != "plan 24" {
		t.Fatalf("last entry should be newest plan, got: %+v", entries[len(entries)-1])
	}
}
