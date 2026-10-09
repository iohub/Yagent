package toolbatch

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestRun_ParallelExecution_ConcurrencyEvidence 并发实证:
// 3 个只读工具各 sleep 50ms,并行组内应出现 ≥2 的重叠(实际并发 >1)。
func TestRun_ParallelExecution_ConcurrencyEvidence(t *testing.T) {
	var cur, maxCur atomic.Int64

	seg := Segment{Parallel: true, Indices: []int{0, 1, 2}}
	Run(context.Background(), seg, 4,
		func(ctx context.Context, i int) int {
			c := cur.Add(1)
			for {
				old := maxCur.Load()
				if c <= old || maxCur.CompareAndSwap(old, c) {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			cur.Add(-1)
			return i
		},
		func(i int, res int) {},
	)

	if maxCur.Load() < 2 {
		t.Fatalf("expected max concurrency >= 2, got %d", maxCur.Load())
	}
}

// TestRun_OrderPreserved_OnCommit join 后按原索引序 commit,
// 即使完成顺序故意错乱(前面的元素完成得最晚)。
func TestRun_OrderPreserved_OnCommit(t *testing.T) {
	seg := Segment{Parallel: true, Indices: []int{0, 1, 2, 3}}
	var mu sync.Mutex
	var commitOrder []int

	Run(context.Background(), seg, 4,
		func(ctx context.Context, i int) int {
			// 反向 sleep:i=0 睡最久,i=3 最快 → 完成顺序故意错乱
			time.Sleep(time.Duration(4-i) * 30 * time.Millisecond)
			return i * 10
		},
		func(i int, res int) {
			mu.Lock()
			defer mu.Unlock()
			commitOrder = append(commitOrder, i)
			if res != i*10 {
				t.Errorf("result slot mismatch for %d: got %d", i, res)
			}
		},
	)

	want := []int{0, 1, 2, 3}
	mu.Lock()
	got := append([]int(nil), commitOrder...)
	mu.Unlock()
	if len(got) != len(want) {
		t.Fatalf("commit count = %d, want %d (%v)", len(got), len(want), got)
	}
	for k := range want {
		if got[k] != want[k] {
			t.Fatalf("commit order = %v, want %v", got, want)
		}
	}
}

// TestRun_PanicIsolation 组内一个元素 panic 不影响兄弟:全部照常 commit,
// panic 槽位落到 T 零值;WaitGroup 正常 join(不死锁、不崩溃)。
func TestRun_PanicIsolation(t *testing.T) {
	seg := Segment{Parallel: true, Indices: []int{0, 1, 2}}
	var committed []int
	var mu sync.Mutex

	Run(context.Background(), seg, 4,
		func(ctx context.Context, i int) string {
			if i == 1 {
				panic("boom")
			}
			return "ok"
		},
		func(i int, res string) {
			mu.Lock()
			defer mu.Unlock()
			committed = append(committed, i)
			switch i {
			case 0, 2:
				if res != "ok" {
					t.Errorf("element %d: got %q, want \"ok\"", i, res)
				}
			case 1:
				if res != "" {
					t.Errorf("panicked element should commit zero value, got %q", res)
				}
			}
		},
	)

	mu.Lock()
	defer mu.Unlock()
	if len(committed) != 3 {
		t.Fatalf("all elements must commit, got %v", committed)
	}
}

// TestRun_CtxCancellation_AllElementsRun 父 ctx 取消后,信号量排队中
// 尚未启动的调用也要照常启动(以已取消 ctx),每个元素都 exec+commit
// (与串行现状一致:每个 tool_call 必有对应产物)。
func TestRun_CtxCancellation_AllElementsRun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	seg := Segment{Parallel: true, Indices: []int{0, 1, 2, 3, 4, 5}}

	var execCalls, committed atomic.Int64

	Run(ctx, seg, 2, // 限流 2:后半部分必然在取消后排队
		func(cctx context.Context, i int) int {
			execCalls.Add(1)
			if i == 0 {
				cancel() // 首元素启动后立即取消父 ctx
			}
			time.Sleep(10 * time.Millisecond)
			return i
		},
		func(i int, res int) { committed.Add(1) },
	)

	if execCalls.Load() != 6 {
		t.Fatalf("all 6 elements must be executed (queued ones too), got %d", execCalls.Load())
	}
	if committed.Load() != 6 {
		t.Fatalf("all 6 elements must commit even after cancellation, got %d", committed.Load())
	}
}

// TestRun_Throttle_MaxConcurrency 限流:6 个只读工具、并行度 2 →
// 实际最大并发必须 == 2(不超过信号量容量)。
func TestRun_Throttle_MaxConcurrency(t *testing.T) {
	var cur, maxCur atomic.Int64
	seg := Segment{Parallel: true, Indices: []int{0, 1, 2, 3, 4, 5}}

	Run(context.Background(), seg, 2,
		func(ctx context.Context, i int) int {
			c := cur.Add(1)
			for {
				old := maxCur.Load()
				if c <= old || maxCur.CompareAndSwap(old, c) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
			cur.Add(-1)
			return i
		},
		func(i, res int) {},
	)

	if m := maxCur.Load(); m != 2 {
		t.Fatalf("expected max concurrency exactly 2 (throttled), got %d", m)
	}
}

// TestRun_ParallelOne_KillSwitch 并行度 1 = kill-switch:严格串行,
// 最大并发精确为 1,执行序 = 原索引序。
func TestRun_ParallelOne_KillSwitch(t *testing.T) {
	var cur, maxCur atomic.Int64
	var mu sync.Mutex
	var execOrder []int
	seg := Segment{Parallel: true, Indices: []int{0, 1, 2}}

	Run(context.Background(), seg, 1,
		func(ctx context.Context, i int) int {
			c := cur.Add(1)
			for {
				old := maxCur.Load()
				if c <= old || maxCur.CompareAndSwap(old, c) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			mu.Lock()
			execOrder = append(execOrder, i)
			mu.Unlock()
			cur.Add(-1)
			return i
		},
		func(i, res int) {},
	)

	if m := maxCur.Load(); m != 1 {
		t.Fatalf("max concurrency must be exactly 1 under kill-switch, got %d", m)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(execOrder) != 3 || execOrder[0] != 0 || execOrder[1] != 1 || execOrder[2] != 2 {
		t.Fatalf("serial exec order expected [0 1 2], got %v", execOrder)
	}
}

// TestRun_SerialSegment 快路径:非并行段逐元素同步 exec+commit。
func TestRun_SerialSegment(t *testing.T) {
	seg := Segment{Parallel: false, Indices: []int{7}}
	var order []string

	Run(context.Background(), seg, 4,
		func(ctx context.Context, i int) string {
			if i != 7 {
				t.Fatalf("unexpected index %d", i)
			}
			return "serial"
		},
		func(i int, res string) { order = append(order, res) },
	)

	if len(order) != 1 || order[0] != "serial" {
		t.Fatalf("serial segment commit mismatch: %v", order)
	}
}

// TestRun_GroupSizeOne_FastPath 组大小 1(即使 Parallel=true)走无 goroutine 快路径。
func TestRun_GroupSizeOne_FastPath(t *testing.T) {
	seg := Segment{Parallel: true, Indices: []int{5}}
	done := make(chan struct{})

	Run(context.Background(), seg, 4,
		func(ctx context.Context, i int) string { return "fast" },
		func(i int, res string) {
			if i != 5 || res != "fast" {
				t.Fatalf("unexpected commit (%d,%q)", i, res)
			}
			close(done)
		},
	)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("fast path did not run")
	}
}

// TestRun_WaitGroupIntegrity 并发压力:反复 Run 确认 WaitGroup/信号量无泄漏死锁。
func TestRun_WaitGroupIntegrity(t *testing.T) {
	for round := 0; round < 20; round++ {
		seg := Segment{Parallel: true, Indices: []int{0, 1, 2, 3}}
		var execCalls atomic.Int64
		Run(context.Background(), seg, 3,
			func(ctx context.Context, i int) int {
				execCalls.Add(1)
				return i
			},
			func(i int, res int) {},
		)
		if n := execCalls.Load(); n != 4 {
			t.Fatalf("round %d: exec must be invoked exactly 4 times, got %d", round, n)
		}
	}
}
