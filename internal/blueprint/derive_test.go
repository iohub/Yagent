package blueprint

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 本文件钉 §5.1 的成本自适应与 §14/I15 的并发纪律：
// 派生是**唯一写者**、指针一次替换、请求方永不阻塞、扫描失败不许把旧快照清空。

// cheapBudget/cachedBudget 是给 deriveBudget 这个 seam 用的两个极端值：
// 一次空扫描的真实耗时不可能等于 1 小时，也不可能小于 1 纳秒，
// 所以两条分支都是**确定性的**，不靠 sleep 等出来。
var cheapBudget = time.Hour

var cachedBudget = time.Nanosecond

func TestDeriveNonGitRepoStillDerives(t *testing.T) {
	defer setBudget(t, cheapBudget)()

	root := t.TempDir()
	// CheckProjectRoot（I16）要求 git 或 manifest：给一份 go.mod，非 git 也照样该能用。
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/x\n\ngo 1.25\n"), 0o600); err != nil {
		t.Fatalf("夹具写入失败: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "pkg", "a"), 0o700); err != nil {
		t.Fatalf("夹具目录失败: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "a", "a.go"), []byte("package a\n"), 0o600); err != nil {
		t.Fatalf("夹具写入失败: %v", err)
	}

	d := NewDeriver(DeriveSpec{
		ProjectPath: root,
		RepoToken:   func() string { return "" }, // 非 git：调用方的退化读数（mtime）
		ScanFn:      Scan,
	})
	if err := d.DeriveOnce(context.Background()); err != nil {
		t.Fatalf("非 git 仓库派生失败: %v", err)
	}
	snap := d.Snapshot()
	if snap == nil {
		t.Fatalf("派生成功却没有快照")
	}
	if len(snap.Leaves) == 0 {
		t.Errorf("非 git 仓库扫出 0 片: %+v", snap)
	}
	st := d.CurrentStatus()
	if !st.Ready || st.LastErr != "" {
		t.Errorf("账目没记成就绪: %+v", st)
	}
	if st.CachedMode {
		t.Errorf("一次空扫描被判成缓存模式（阈值 %v）", deriveBudget)
	}
}

// I15：注入侧"只读快照、不等派生"。这里钉两件事 ——
// Request 永不阻塞（哪怕扫描正卡在 IO 上），以及没就绪时快照就是 nil。
func TestDeriveRequestNeverBlocks(t *testing.T) {
	defer setBudget(t, cheapBudget)()

	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	d := NewDeriver(DeriveSpec{
		ProjectPath: "/repo",
		ScanFn: func(string, Options) (*Snapshot, error) {
			once.Do(func() { close(entered) })
			<-release
			return testSnapshot(testLeaf("a", 1)), nil
		},
	})

	done := make(chan struct{})
	go func() { d.Run(context.Background()); close(done) }()

	<-entered // 第一次扫描已经在跑，此刻 wake 通道是空的
	if st := d.CurrentStatus(); st.Ready || !st.Scanning {
		t.Errorf("扫描进行中却报就绪: %+v", st)
	}
	if d.Snapshot() != nil {
		t.Errorf("扫描还没完成就有快照了")
	}

	// 三次请求：第一次进通道，后两次必须走 default 分支立即返回。
	blocked := make(chan struct{})
	go func() {
		for i := 0; i < 3; i++ {
			d.Request()
		}
		close(blocked)
	}()
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Errorf("Request 在扫描期间卡住了（I15：注入侧绝不阻塞等派生）")
	}

	close(release)
	// 合并语义：扫描期间到达的多次请求合并成一次后续扫描 —— 等它完成再断言。
	deadline := time.After(2 * time.Second)
	for {
		if d.Snapshot() != nil && !d.CurrentStatus().Scanning {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("释放后扫描没落地: %+v", d.CurrentStatus())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	d.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Errorf("Close 之后 Run 没退出（--prompt 路径会因此挂住）")
	}
}

// 成本自适应的两条分支都要有能响的夹具（§5.1 的到期条件写在注释里，判据在这里）。
func TestDeriveModeFollowsCost(t *testing.T) {
	cases := []struct {
		name     string
		budget   time.Duration
		wantMode bool
	}{
		{"低于阈值 → 每轮重派生", cheapBudget, false},
		{"达到阈值 → 缓存 + 读数失效", cachedBudget, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer setBudget(t, tc.budget)()
			d := NewDeriver(DeriveSpec{
				ProjectPath: "/repo",
				ScanFn:      func(string, Options) (*Snapshot, error) { return testSnapshot(testLeaf("a", 1)), nil },
			})
			if err := d.DeriveOnce(context.Background()); err != nil {
				t.Fatalf("派生失败: %v", err)
			}
			if got := d.CurrentStatus().CachedMode; got != tc.wantMode {
				t.Errorf("CachedMode = %v, 要 %v", got, tc.wantMode)
			}
		})
	}
}

// 缓存模式：读数没变就不重扫；变了才重扫。
// 这条是"没有缓存就少一整类 bug"的另一半 —— 一旦真的缓存了，失效判据就是唯一防线。
func TestCachedModeInvalidatesOnRepoToken(t *testing.T) {
	defer setBudget(t, cachedBudget)()

	var scans atomic.Int32
	head := atomic.Value{}
	head.Store("sha-1")
	d := NewDeriver(DeriveSpec{
		ProjectPath: "/repo",
		RepoToken:   func() string { return head.Load().(string) },
		ScanFn: func(string, Options) (*Snapshot, error) {
			scans.Add(1)
			return testSnapshot(testLeaf("a", 1)), nil
		},
	})
	if err := d.DeriveOnce(context.Background()); err != nil {
		t.Fatalf("派生失败: %v", err)
	}
	if scans.Load() != 1 {
		t.Fatalf("夹具: 初始应扫一次, got %d", scans.Load())
	}

	// 同一个 head：请求被处理了，但因为"仓库没动"跳过扫描。
	// 断言走 Requests/Skips 两个计数器而不是 sleep 之后数扫描次数 ——
	// 跳过的那条路径什么都不会发生，只有账目能证明它真的跑过。
	done := make(chan struct{})
	go func() { d.Run(context.Background()); close(done) }()
	d.Request()
	waitFor(t, func() bool {
		st := d.CurrentStatus()
		return st.Requests == 1 && st.Skips == 1
	}, "缓存模式下无变化的请求没被记账")
	if got := scans.Load(); got != 1 {
		t.Errorf("仓库读数没变却重扫了: scans=%d", got)
	}

	// 换 head：必须重扫。
	head.Store("sha-2")
	d.Request()
	waitFor(t, func() bool { return scans.Load() == 2 }, "换了 HEAD 却没重扫")
	if st := d.CurrentStatus(); st.Skips != 1 {
		t.Errorf("跳过计数被后续请求改写了: %+v", st)
	}

	d.Close()
	<-done
}

// 每轮重派生模式（默认形态）：每次请求都真的重扫一次。
func TestCheapModeRescansOnEveryRequest(t *testing.T) {
	defer setBudget(t, cheapBudget)()

	var scans atomic.Int32
	d := NewDeriver(DeriveSpec{
		ProjectPath: "/repo",
		ScanFn: func(string, Options) (*Snapshot, error) {
			scans.Add(1)
			return testSnapshot(testLeaf("a", 1)), nil
		},
	})
	done := make(chan struct{})
	go func() { d.Run(context.Background()); close(done) }()

	for i := 1; i <= 3; i++ {
		waitFor(t, func() bool { return int(scans.Load()) == i }, "每轮重派生的请求没被兑现")
		if i < 3 {
			d.Request()
		}
	}
	d.Close()
	<-done

	// 直接的失败断言（自定义等待辅助不算断言，check_test_hygiene.sh 守这条）：
	// 三次请求 = 初始一次 + 两次重扫，多一次就是"请求没被合并"，少一次就是"cheap 模式退成了缓存"。
	if got := scans.Load(); got != 3 {
		t.Errorf("扫描次数 = %d, 要 3（初始 1 次 + 2 次请求）", got)
	}
	if st := d.CurrentStatus(); !st.Ready || st.Requests != 2 {
		t.Errorf("账目不对: %+v", st)
	}
}

// 扫描失败**不清空旧快照**：一次 git 读数失败或临时 IO 错误不该让整个索引消失
// （与 §5.2"指纹不符不删除"是同一条理由）。
func TestDeriveFailureKeepsLastSnapshot(t *testing.T) {
	defer setBudget(t, cheapBudget)()

	boom := errors.New("rev-parse failed")
	var fail atomic.Bool
	d := NewDeriver(DeriveSpec{
		ProjectPath: "/repo",
		ScanFn: func(string, Options) (*Snapshot, error) {
			if fail.Load() {
				return nil, boom
			}
			return testSnapshot(testLeaf("a", 7)), nil
		},
	})
	if err := d.DeriveOnce(context.Background()); err != nil {
		t.Fatalf("派生失败: %v", err)
	}
	first := d.Snapshot()
	fail.Store(true)
	if err := d.DeriveOnce(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("失败没向上报: %v", err)
	}
	if d.Snapshot() != first {
		t.Errorf("扫描失败把旧快照清空了")
	}
	if st := d.CurrentStatus(); st.LastErr == "" || !st.Ready {
		t.Errorf("账目既没记错误也没保留就绪态: %+v", st)
	}
}

// Close 之后不再接受新请求（否则 Run 会被一个迟到的 wake 叫醒）。
func TestDeriveCloseStopsAcceptingRequests(t *testing.T) {
	defer setBudget(t, cheapBudget)()

	var scans atomic.Int32
	d := NewDeriver(DeriveSpec{
		ProjectPath: "/repo",
		ScanFn: func(string, Options) (*Snapshot, error) {
			scans.Add(1)
			return testSnapshot(testLeaf("a", 1)), nil
		},
	})
	if err := d.DeriveOnce(context.Background()); err != nil {
		t.Fatalf("派生失败: %v", err)
	}
	d.Close()
	for i := 0; i < 5; i++ {
		d.Request()
	}
	if got := scans.Load(); got != 1 {
		t.Errorf("Close 之后仍接受请求: scans=%d", got)
	}
}

func setBudget(t *testing.T, d time.Duration) func() {
	t.Helper()
	old := deriveBudget
	deriveBudget = d
	return func() { deriveBudget = old }
}

// waitFor 等一个确定性条件；超时是**失败**而不是重试的借口。
func waitFor(t *testing.T, ok func() bool, why string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s（2s 超时）", why)
}
