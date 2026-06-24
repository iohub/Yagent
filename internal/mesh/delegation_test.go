package mesh

import (
	"testing"
	"time"
)

// ===================== NewDelegationContext 测试 =====================

func TestNewDelegationContext(t *testing.T) {
	ctx := NewDelegationContext("task-1", "bb-1", "anchor-agent")

	if ctx.TaskID != "task-1" {
		t.Errorf("TaskID = %q, want %q", ctx.TaskID, "task-1")
	}
	if ctx.InitiatorID != "anchor-agent" {
		t.Errorf("InitiatorID = %q, want %q", ctx.InitiatorID, "anchor-agent")
	}
	if ctx.BlackboardID != "bb-1" {
		t.Errorf("BlackboardID = %q, want %q", ctx.BlackboardID, "bb-1")
	}
	if ctx.Depth != 0 {
		t.Errorf("Depth = %d, want 0", ctx.Depth)
	}
	if ctx.MaxDepth != DefaultMaxDepth {
		t.Errorf("MaxDepth = %d, want %d", ctx.MaxDepth, DefaultMaxDepth)
	}
	if len(ctx.Chain) != 1 || ctx.Chain[0] != "anchor-agent" {
		t.Errorf("Chain = %v, want [anchor-agent]", ctx.Chain)
	}
	if ctx.Visited["anchor-agent"] != 1 {
		t.Errorf("Visited[anchor-agent] = %d, want 1", ctx.Visited["anchor-agent"])
	}
	if ctx.RemainingTime() <= 0 || ctx.RemainingTime() > DefaultTimeout {
		t.Errorf("RemainingTime = %v, want (0, %v]", ctx.RemainingTime(), DefaultTimeout)
	}
}

// ===================== CanDelegateTo 测试 =====================

func TestCanDelegateTo_SelfDelegation(t *testing.T) {
	ctx := NewDelegationContext("task-1", "bb-1", "agent-A")
	err := ctx.CanDelegateTo("agent-A", "agent-A")
	if err != ErrSelfDelegation {
		t.Errorf("expected ErrSelfDelegation, got %v", err)
	}
}

func TestCanDelegateTo_MaxDepthExceeded(t *testing.T) {
	ctx := NewDelegationContext("task-1", "bb-1", "agent-A")

	// 深度增至 4（等于 MaxDepth）
	ctx.Depth = 4
	err := ctx.CanDelegateTo("agent-B", "agent-A")
	if err != ErrMaxDepthExceeded {
		t.Errorf("expected ErrMaxDepthExceeded, got %v", err)
	}
}

func TestCanDelegateTo_CycleDetected(t *testing.T) {
	ctx := NewDelegationContext("task-1", "bb-1", "agent-A")
	ctx.Visited["agent-B"] = 2 // 已访问过 agent-B 两次

	err := ctx.CanDelegateTo("agent-B", "agent-C")
	if err != ErrCycleDetected {
		t.Errorf("expected ErrCycleDetected, got %v", err)
	}
}

func TestCanDelegateTo_DeadlineExceeded(t *testing.T) {
	ctx := &DelegationContext{
		TaskID:   "task-1",
		Chain:    []string{"agent-A"},
		Depth:    0,
		MaxDepth: DefaultMaxDepth,
		Visited:  map[string]int{"agent-A": 1},
		Deadline: time.Now().Add(-1 * time.Second), // 已过期
	}

	err := ctx.CanDelegateTo("agent-B", "agent-A")
	if err != ErrDeadlineExceeded {
		t.Errorf("expected ErrDeadlineExceeded, got %v", err)
	}
}

func TestCanDelegateTo_Success(t *testing.T) {
	ctx := NewDelegationContext("task-1", "bb-1", "agent-A")
	err := ctx.CanDelegateTo("agent-B", "agent-A")
	if err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

// ===================== Fork 测试 =====================

func TestFork_DeepCopy(t *testing.T) {
	original := NewDelegationContext("task-1", "bb-1", "agent-A")

	// 委派给 agent-B
	forked := original.Fork("agent-B")

	// 验证 forked 的属性
	if forked.TaskID != original.TaskID {
		t.Error("TaskID should be copied")
	}
	if forked.InitiatorID != original.InitiatorID {
		t.Error("InitiatorID should be copied")
	}
	if forked.Depth != 1 {
		t.Errorf("forked Depth = %d, want 1", forked.Depth)
	}
	if forked.MaxDepth != original.MaxDepth {
		t.Error("MaxDepth should be copied")
	}
	if len(forked.Chain) != 2 {
		t.Errorf("forked Chain length = %d, want 2", len(forked.Chain))
	}
	if forked.Chain[0] != "agent-A" || forked.Chain[1] != "agent-B" {
		t.Errorf("forked Chain = %v, want [agent-A, agent-B]", forked.Chain)
	}
	if forked.Visited["agent-A"] != 1 {
		t.Errorf("forked Visited[agent-A] = %d, want 1", forked.Visited["agent-A"])
	}
	if forked.Visited["agent-B"] != 1 {
		t.Errorf("forked Visited[agent-B] = %d, want 1", forked.Visited["agent-B"])
	}
}

func TestFork_Isolation(t *testing.T) {
	// 验证 Fork 深拷贝 — 修改 forked 不应影响 original
	original := NewDelegationContext("task-1", "bb-1", "agent-A")
	forked := original.Fork("agent-B")

	// 再次 fork 产生第三个上下文
	forked2 := forked.Fork("agent-C")

	// 验证 original 未受影响
	if len(original.Chain) != 1 {
		t.Errorf("original Chain length = %d, want 1 (not affected by fork)", len(original.Chain))
	}
	if len(original.Visited) != 1 {
		t.Errorf("original Visited length = %d, want 1", len(original.Visited))
	}

	// 验证 forked 未受 forked2 影响
	if len(forked.Chain) != 2 {
		t.Errorf("forked Chain length = %d, want 2", len(forked.Chain))
	}
	if forked.Visited["agent-C"] != 0 {
		t.Errorf("forked Visited[agent-C] = %d, want 0", forked.Visited["agent-C"])
	}

	// 验证 forked2 正确
	if len(forked2.Chain) != 3 {
		t.Errorf("forked2 Chain length = %d, want 3", len(forked2.Chain))
	}
	if forked2.Visited["agent-B"] != 1 {
		t.Errorf("forked2 Visited[agent-B] = %d, want 1", forked2.Visited["agent-B"])
	}
	if forked2.Visited["agent-C"] != 1 {
		t.Errorf("forked2 Visited[agent-C] = %d, want 1", forked2.Visited["agent-C"])
	}
}

// ===================== RemainingTime 和 IsExpired 测试 =====================

func TestRemainingTime(t *testing.T) {
	ctx := NewDelegationContext("task-1", "bb-1", "agent-A")
	rem := ctx.RemainingTime()
	if rem <= 0 || rem > DefaultTimeout {
		t.Errorf("RemainingTime = %v, want (0, %v]", rem, DefaultTimeout)
	}
}

func TestRemainingTime_Expired(t *testing.T) {
	ctx := &DelegationContext{
		Deadline: time.Now().Add(-1 * time.Second),
	}
	rem := ctx.RemainingTime()
	if rem != 0 {
		t.Errorf("RemainingTime = %v, want 0", rem)
	}
}

func TestIsExpired(t *testing.T) {
	ctx := NewDelegationContext("task-1", "bb-1", "agent-A")
	if ctx.IsExpired() {
		t.Error("new context should not be expired")
	}

	expiredCtx := &DelegationContext{
		Deadline: time.Now().Add(-1 * time.Second),
	}
	if !expiredCtx.IsExpired() {
		t.Error("expired context should return true")
	}
}

// ===================== ChainString 测试 =====================

func TestChainString(t *testing.T) {
	ctx := NewDelegationContext("task-1", "bb-1", "agent-A")
	ctx.Chain = []string{"anchor", "B", "C"}

	s := ctx.ChainString()
	expected := "anchor → B → C"
	if s != expected {
		t.Errorf("ChainString = %q, want %q", s, expected)
	}
}

func TestChainString_Empty(t *testing.T) {
	ctx := &DelegationContext{Chain: []string{}}
	s := ctx.ChainString()
	if s != "(empty)" {
		t.Errorf("ChainString for empty chain = %q, want %q", s, "(empty)")
	}
}

// ===================== Summary 测试 =====================

func TestSummary(t *testing.T) {
	ctx := NewDelegationContext("task-1", "bb-1", "agent-A")
	ctx.Chain = []string{"agent-A", "agent-B"}
	ctx.Visited = map[string]int{"agent-A": 1, "agent-B": 1}

	summary := ctx.Summary()

	if summary["task_id"] != "task-1" {
		t.Errorf("summary task_id = %v, want 'task-1'", summary["task_id"])
	}
	if summary["initiator_id"] != "agent-A" {
		t.Errorf("summary initiator_id = %v, want 'agent-A'", summary["initiator_id"])
	}
	if summary["depth"] != 0 {
		t.Errorf("summary depth = %v, want 0", summary["depth"])
	}
	if summary["max_depth"] != DefaultMaxDepth {
		t.Errorf("summary max_depth = %v, want %d", summary["max_depth"], DefaultMaxDepth)
	}
	if summary["is_expired"] != false {
		t.Errorf("summary is_expired = %v, want false", summary["is_expired"])
	}
	if summary["blackboard_id"] != "bb-1" {
		t.Errorf("summary blackboard_id = %v, want 'bb-1'", summary["blackboard_id"])
	}
}

// ===================== String 测试 =====================

func TestString(t *testing.T) {
	ctx := NewDelegationContext("task-1", "bb-1", "agent-A")
	s := ctx.String()
	if len(s) == 0 {
		t.Error("String() returned empty string")
	}
}

// ===================== WithMaxDepth / WithTimeout 测试 =====================

func TestWithMaxDepth(t *testing.T) {
	ctx := NewDelegationContext("task-1", "bb-1", "agent-A")
	newCtx := ctx.WithMaxDepth(2)

	if newCtx.MaxDepth != 2 {
		t.Errorf("newCtx MaxDepth = %d, want 2", newCtx.MaxDepth)
	}
	if ctx.MaxDepth != DefaultMaxDepth {
		t.Errorf("original ctx MaxDepth = %d, should not be modified", ctx.MaxDepth)
	}
}

func TestWithMaxDepth_Zero(t *testing.T) {
	ctx := NewDelegationContext("task-1", "bb-1", "agent-A")
	newCtx := ctx.WithMaxDepth(0) // 零值应回退到默认值

	if newCtx.MaxDepth != DefaultMaxDepth {
		t.Errorf("newCtx MaxDepth with 0 input = %d, want %d", newCtx.MaxDepth, DefaultMaxDepth)
	}
}

func TestWithTimeout(t *testing.T) {
	ctx := NewDelegationContext("task-1", "bb-1", "agent-A")
	newCtx := ctx.WithTimeout(60 * time.Second)

	rem1 := ctx.RemainingTime()
	rem2 := newCtx.RemainingTime()

	// 原始上下文剩余时间应约 120s，新上下文约 60s
	if rem1 < 100*time.Second {
		t.Errorf("original RemainingTime = %v, want >= 100s", rem1)
	}
	if rem2 > 80*time.Second || rem2 < 50*time.Second {
		t.Errorf("newCtx RemainingTime = %v, want ~60s", rem2)
	}
}

// ===================== VisitCount / UniqueAgents 测试 =====================

func TestVisitCount(t *testing.T) {
	ctx := NewDelegationContext("task-1", "bb-1", "agent-A")

	if ctx.VisitCount("agent-A") != 1 {
		t.Errorf("VisitCount(agent-A) = %d, want 1", ctx.VisitCount("agent-A"))
	}
	if ctx.VisitCount("agent-B") != 0 {
		t.Errorf("VisitCount(agent-B) = %d, want 0", ctx.VisitCount("agent-B"))
	}
}

func TestUniqueAgents(t *testing.T) {
	ctx := NewDelegationContext("task-1", "bb-1", "agent-A")
	if ctx.UniqueAgents() != 1 {
		t.Errorf("UniqueAgents() = %d, want 1", ctx.UniqueAgents())
	}
}

// ===================== DelegationContextView 测试 =====================

func TestDelegationContextView(t *testing.T) {
	ctx := NewDelegationContext("task-1", "bb-1", "agent-A")
	view := NewDelegationContextView(ctx)

	summary := view.SafeSummary()
	if summary["task_id"] != "task-1" {
		t.Error("view SafeSummary task_id mismatch")
	}

	err := view.SafeCanDelegateTo("agent-B", "agent-A")
	if err != nil {
		t.Errorf("view SafeCanDelegateTo returned error: %v", err)
	}
}

// ===================== 完整委派链模拟 =====================

func TestFullDelegationChain(t *testing.T) {
	ctx := NewDelegationContext("task-100", "bb-100", "anchor")

	// 模拟完整委派链: anchor → B → C → D → E (应被阻止)
	agents := []string{"agent-B", "agent-C", "agent-D", "agent-E"}

	for i, target := range agents {
		from := "anchor"
		if i > 0 {
			from = agents[i-1]
		}

		// 检查是否能委派
		err := ctx.CanDelegateTo(target, from)
		if err != nil {
			t.Errorf("Step %d (→%s): CanDelegateTo returned %v", i, target, err)
		}

		// fork 创建新上下文
		ctx = ctx.Fork(target)
	}

	// 第 5 次委派应失败（深度 = 4 = MaxDepth）
	err := ctx.CanDelegateTo("agent-F", "agent-E")
	if err != ErrMaxDepthExceeded {
		t.Errorf("Expected ErrMaxDepthExceeded on 5th delegation, got %v", err)
	}

	// 验证最终状态
	if ctx.Depth != 4 {
		t.Errorf("Final Depth = %d, want 4", ctx.Depth)
	}
	if len(ctx.Chain) != 5 {
		t.Errorf("Final Chain length = %d, want 5", len(ctx.Chain))
	}
	if ctx.UniqueAgents() != 5 {
		t.Errorf("Final UniqueAgents = %d, want 5", ctx.UniqueAgents())
	}
}

// ===================== 循环检测完整场景 =====================

func TestCycleDetection(t *testing.T) {
	// 模拟: A → B → C → A (应检测到循环)
	ctx := NewDelegationContext("cycle-task", "bb-1", "A")

	// A → B
	ctx = ctx.Fork("B")
	// B → C
	ctx = ctx.Fork("C")

	// C 尝试委派给 A（A 已在 Visited 中计数为 1）
	err := ctx.CanDelegateTo("A", "C")
	if err != nil {
		t.Errorf("CanDelegateTo(A, C) returned %v, expected nil (A visited once is OK)", err)
	}

	// 委派 C → A（现在 Visited[A] = 2）
	ctx = ctx.Fork("A")

	// 此时 Visited[A] = 2
	// 从 D 尝试委派给 A（A 已访问 2 次 → 应报循环错误）
	err = ctx.CanDelegateTo("A", "D")
	if err != ErrCycleDetected {
		t.Errorf("Expected ErrCycleDetected on second visit to A from different agent, got %v", err)
	}
}

// ===================== 并发测试 =====================

func TestForkConcurrent(t *testing.T) {
	ctx := NewDelegationContext("concurrent-task", "bb-1", "anchor")

	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(id int) {
			forked := ctx.Fork(idString(id))
			_ = forked.Summary()
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func idString(i int) string {
	if i < 0 {
		return "agent-NEG"
	}
	return "agent-" + string(rune('0'+i))
}
