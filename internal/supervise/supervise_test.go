package supervise

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// C1 的承重断言在这里：Drain 必须真的等到人、泄漏必须报得出名字、panic 必须死在域内。
// 这三条任何一条漂走，"结构化并发"就退化成"多了一层包装"。

func TestGoRunsAndDrainJoinsIt(t *testing.T) {
	g, ctx := New(context.Background(), "task")
	done := make(chan struct{})

	g.Go("worker", func(ctx context.Context) { close(done) })

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("goroutine never ran")
	}
	if leaked := g.Drain(2 * time.Second); len(leaked) != 0 {
		t.Errorf("Drain after completion = %d leases, want 0: %v", len(leaked), leaked)
	}
	if g.Context() != ctx.Value(ctxKey{}).(*Group).Context() {
		t.Error("ctx-carrying group must expose the same ctx Go hands to fn")
	}
}

func TestGoHandsTheDomainContextToFn(t *testing.T) {
	g, ctx := New(context.Background(), "task")
	type marker struct{}
	ctx = context.WithValue(ctx, marker{}, "seen")
	g, ctx = New(ctx, "delegate_coding")

	got := make(chan context.Context, 1)
	g.Go("probe", func(ctx context.Context) { got <- ctx })

	select {
	case fnCtx := <-got:
		if fnCtx.Value(marker{}) != "seen" {
			t.Error("fn ctx lost values carried by the domain ctx")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("probe never ran")
	}
	if From(ctx) != g {
		t.Error("From(ctx) must resolve the innermost domain")
	}
}

// 域不制造取消，这条是 pause 语义的护栏（AGENTS.md §11 坑 17）：
// "暂停/超时"必须由 fn 自己尊重 ctx，而不是由 supervisor 替它切断。
func TestDrainNeverCancels(t *testing.T) {
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	g, _ := New(base, "task")

	wasCancelled := make(chan bool, 1)
	g.Go("watch", func(ctx context.Context) {
		select {
		case <-ctx.Done():
			wasCancelled <- true
		case <-time.After(150 * time.Millisecond):
			wasCancelled <- false
		}
	})

	if leaked := g.Drain(time.Second); len(leaked) != 0 {
		t.Fatalf("Drain leaked: %v", leaked)
	}
	if got := <-wasCancelled; got {
		t.Error("Drain cancelled the domain ctx; it must only wait")
	}
}

func TestSuperviseRecoversPanickedGoroutine(t *testing.T) {
	g, _ := New(context.Background(), "task")
	after := make(chan struct{})
	g.Go("bomb", func(context.Context) { panic(errors.New("boom")) })
	g.Go("peer", func(context.Context) { close(after) })

	select {
	case <-after:
	case <-time.After(2 * time.Second):
		t.Fatal("a panicking member killed its siblings (or the process)")
	}
	if leaked := g.Drain(2 * time.Second); len(leaked) != 0 {
		t.Errorf("panicked member was not released: %v", leaked)
	}
	if f := g.Failures()["bomb"]; f != 1 {
		t.Errorf("Failures[bomb] = %d, want 1 —— panic 被吃掉且不记账，就是静默丢后台工作", f)
	}
}

func TestDrainReportsNamedLease(t *testing.T) {
	g, _ := New(context.Background(), "task")
	block := make(chan struct{})
	g.Go("stuck-sweeper", func(context.Context) { <-block })

	leaked := g.Drain(80 * time.Millisecond)
	if len(leaked) != 1 {
		t.Fatalf("Drain on a stuck member = %d leases, want 1", len(leaked))
	}
	if leaked[0].Name != "stuck-sweeper" {
		t.Errorf("lease name = %q, want stuck-sweeper —— 报不出是哪一条就等于没具名", leaked[0].Name)
	}
	if !strings.Contains(leaked[0].Owner, "task") {
		t.Errorf("lease owner = %q, want it to name the owning domain", leaked[0].Owner)
	}
	if leaked[0].Key == leaked[0].Name {
		t.Error("lease key must be unique within the domain, not just the readable name")
	}

	close(block)
	if leaked := g.Drain(2 * time.Second); len(leaked) != 0 {
		t.Errorf("Drain after unblock = %v, want empty", leaked)
	}
}

func TestPendingReportsWithoutWaiting(t *testing.T) {
	g, _ := New(context.Background(), "task")
	block := make(chan struct{})
	g.Go("busy", func(context.Context) { <-block })

	if p := g.Pending(); len(p) != 1 || p[0].Name != "busy" {
		t.Errorf("Pending = %v, want one 'busy' lease", p)
	}
	close(block)
	// release 是异步的，排干后 Pending 必须最终归零。
	for i := 0; i < 200; i++ {
		if len(g.Pending()) == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Errorf("Pending never settled: %v", g.Pending())
}

// 这条测试的形状是被变异逼出来的：只断言"泄漏报告里看见了子域成员"的话，
// 一个"报告但不等待"的实现照样绿。父域必须**真的等**子域。
func TestParentDrainWaitsSubgroupMembers(t *testing.T) {
	root, rootCtx := New(context.Background(), "root-task")
	sub, _ := New(rootCtx, "delegate_repo")

	finished := make(chan struct{})
	sub.Go("inner", func(context.Context) {
		time.Sleep(120 * time.Millisecond)
		close(finished)
	})

	start := time.Now()
	leaked := root.Drain(3 * time.Second)
	elapsed := time.Since(start)

	if len(leaked) != 0 {
		t.Fatalf("parent Drain = %v, want 0 —— 父域必须等子域的成员退出", leaked)
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("parent Drain returned in %v, want >=100ms: it listed the subgroup but did not wait it", elapsed)
	}
	select {
	case <-finished:
	default:
		t.Error("Drain returned before the subgroup member finished")
	}
}

func TestPendingNamesTheOwningSubgroup(t *testing.T) {
	root, rootCtx := New(context.Background(), "task")
	block := make(chan struct{})
	sub, _ := New(rootCtx, "delegate_repo")
	sub.Go("inner", func(context.Context) { <-block })

	leaked := root.Pending()
	if len(leaked) != 1 {
		t.Fatalf("Pending = %v, want one lease from the subgroup", leaked)
	}
	if !strings.Contains(leaked[0].Owner, "delegate_repo") {
		t.Errorf("owner path = %q, want it to include the subgroup name", leaked[0].Owner)
	}
	// 路径从进程根域开始拼：泄漏报告要能一路答到"卡在哪次委派"。
	if !strings.Contains(leaked[0].Owner, "root/task/delegate_repo") {
		t.Errorf("owner path = %q, want root-prefixed domain chain", leaked[0].Owner)
	}

	close(block)
	if leaked := sub.Drain(2 * time.Second); len(leaked) != 0 {
		t.Fatalf("subgroup leaked: %v", leaked)
	}
	// 排干的子域必须从父域摘掉，否则父域永远看得见一条空域（"两份事实"）。
	if leaked := root.Drain(2 * time.Second); len(leaked) != 0 {
		t.Errorf("drained subgroup still attached to parent: %v", leaked)
	}
}

// 摘除"已排干的子域"不是洁癖：release 只在 live 与 subs **同时**为空时才唤醒等待者。
// 漏了摘除，父域就永远不 quiet，症状是每次收尾都白等一个 timeout（不是崩溃，是慢）。
func TestParentGoesQuietAfterSubgroupIsForgotten(t *testing.T) {
	root, rootCtx := New(context.Background(), "task")
	sub, _ := New(rootCtx, "delegate_repo")

	block := make(chan struct{})
	root.Go("own", func(context.Context) { <-block })
	if leaked := sub.Drain(time.Second); len(leaked) != 0 {
		t.Fatalf("empty subgroup Drain = %v", leaked)
	}

	close(block)
	start := time.Now()
	if leaked := root.Drain(3 * time.Second); len(leaked) != 0 {
		t.Fatalf("root Drain = %v, want 0", leaked)
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Errorf("root waited %v after its only member finished —— 已排干的子域没被摘走，父域不会 arm quiet", elapsed)
	}
}

// 唤醒丢了的话，正确性靠 deadline 兜住、症状只是"每次收尾都慢半分钟"。
// 没有这条延迟断言，Close 里挂 20 条 goroutine 就会变成 20 次静默等待。
func TestDrainWakesPromptlyWhenQuiet(t *testing.T) {
	g, _ := New(context.Background(), "task")
	block := make(chan struct{})
	g.Go("slow", func(context.Context) { <-block })

	go func() { time.Sleep(20 * time.Millisecond); close(block) }()

	start := time.Now()
	if leaked := g.Drain(5 * time.Second); len(leaked) != 0 {
		t.Fatalf("Drain = %v, want 0", leaked)
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Errorf("Drain took %v after the last member finished —— 唤醒丢了，只是靠 deadline 兜住", elapsed)
	}
}

// 并发新起成员是真实形状（工具批会在排干进行中起 goroutine），
// 这也是本包不用 sync.WaitGroup 的理由：Add-after-Wait 在这里合法。
func TestConcurrentGoDuringDrainIsAllCounted(t *testing.T) {
	g, _ := New(context.Background(), "task")
	var wg sync.WaitGroup

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				g.Go("spawner", func(context.Context) { time.Sleep(time.Millisecond) })
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < 50; j++ {
			_ = g.Drain(time.Millisecond)
		}
	}()
	wg.Wait()

	if leaked := g.Drain(5 * time.Second); len(leaked) != 0 {
		t.Errorf("final Drain = %d leases, want 0", len(leaked))
	}
	if f := g.Failures(); len(f) != 0 {
		t.Errorf("unexpected panics: %v", f)
	}
}

func TestNilReceiverFallsBackToRoot(t *testing.T) {
	var g *Group
	ran := make(chan struct{})
	g.Go("nil-safe", func(context.Context) { close(ran) }) // 不得 panic

	select {
	case <-ran:
	case <-time.After(2 * time.Second):
		t.Fatal("Go on a nil receiver dropped the work instead of running it")
	}
	if g.Drain(0) != nil {
		t.Error("nil receiver Drain must return no leases")
	}
	if From(nil) != Root() {
		t.Error("From(nil) must fall back to Root, not nil")
	}
	if From(context.Background()) != Root() {
		t.Error("From on a bare ctx must fall back to Root")
	}
}

func TestGoWithNilFnPanicsLoudly(t *testing.T) {
	g, _ := New(context.Background(), "task")
	defer func() {
		if r := recover(); r == nil {
			t.Error("nil fn must panic at the call site, not be silently skipped")
		}
	}()
	g.Go("oops", nil)
}

func TestDuplicateNamesGetDistinctKeys(t *testing.T) {
	g, _ := New(context.Background(), "task")
	block := make(chan struct{})
	g.Go("twin", func(context.Context) { <-block })
	g.Go("twin", func(context.Context) { <-block })

	leaked := g.Pending()
	if len(leaked) != 2 {
		t.Fatalf("Pending = %d, want 2", len(leaked))
	}
	if leaked[0].Key == leaked[1].Key {
		t.Errorf("two members share key %q —— release 会互相顶掉，计数就错了", leaked[0].Key)
	}
	for _, l := range leaked {
		if l.Name != "twin" {
			t.Errorf("readable name = %q, want twin（唯一句柄不该污染给人看的名字）", l.Name)
		}
	}
	close(block)
	if leaked := g.Drain(2 * time.Second); len(leaked) != 0 {
		t.Errorf("final Drain = %v", leaked)
	}
}

// 空名不允许变成 map 里的幽灵条目：它照样得有一条唯一的、可报告的记录。
func TestEmptyNameStillTracked(t *testing.T) {
	g, _ := New(context.Background(), "task")
	block := make(chan struct{})
	g.Go("", func(context.Context) { <-block })

	leaked := g.Pending()
	if len(leaked) != 1 {
		t.Fatalf("Pending = %d, want 1", len(leaked))
	}
	if leaked[0].Name != "unnamed" {
		t.Errorf("name = %q, want unnamed", leaked[0].Name)
	}
	close(block)
	g.Drain(2 * time.Second)
}

func TestRootIsSharedAcrossCalls(t *testing.T) {
	block := make(chan struct{})
	Root().Go("root-member", func(context.Context) { <-block })

	if Root() != From(context.Background()) {
		t.Error("From must hand back the same Root, not a fresh group")
	}
	leaked := Root().Drain(80 * time.Millisecond)
	if len(leaked) != 1 || leaked[0].Owner != "root" {
		t.Errorf("Root Drain = %v, want one lease owned by root", leaked)
	}
	close(block)
	if leaked := Root().Drain(2 * time.Second); len(leaked) != 0 {
		t.Errorf("Root must be reusable after a drain: %v", leaked)
	}
}
