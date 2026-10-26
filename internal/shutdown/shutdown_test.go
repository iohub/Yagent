package shutdown

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"

	"yagent/internal/supervise"
)

// harness 接管两个副作用：信号从哪来、进程退出怎么办。
// 真发信号会打断整个测试二进制，真 os.Exit 会直接杀掉它。
type harness struct {
	sink      chan<- os.Signal // signalSeam 交出来的那条，也就是看门狗在读的那条
	notices   chan string
	hardExit  chan struct{}
	exitCodes chan int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		notices:   make(chan string, 4),
		hardExit:  make(chan struct{}, 4),
		exitCodes: make(chan int, 4),
	}
	oldSeam, oldExit := signalSeam, exitSeam
	signalSeam = func(out chan<- os.Signal) func() {
		h.sink = out
		return func() {}
	}
	exitSeam = func(code int) { h.exitCodes <- code }
	t.Cleanup(func() { signalSeam, exitSeam = oldSeam, oldExit })
	return h
}

func (h *harness) send(t *testing.T, sig os.Signal) {
	t.Helper()
	if h.sink == nil {
		t.Fatal("Install 之前发信号：夹具接线顺序错了，本用例没有测到看门狗")
	}
	h.sink <- sig
}

// 放在最前：Root() 在被装过之前必须就是 Background。
func TestRootWithoutInstallIsBackground(t *testing.T) {
	if Root() != context.Background() {
		t.Fatalf("没装过信号处理器时 Root() 应该是 context.Background()，实际 = %p", Root())
	}
}

func TestFirstSignalCancelsTaskRoot(t *testing.T) {
	h := newHarness(t)
	ctx, stop := Install(Config{
		OnSignal:   func(sig os.Signal) { h.notices <- sig.String() },
		OnHardExit: func() { h.hardExit <- struct{}{} },
	})
	defer stop()

	if ctx.Err() != nil {
		t.Fatalf("刚装好任务 root 就已经结束：%v", ctx.Err())
	}
	h.send(t, syscall.SIGINT)

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("第一次信号没有取消任务 root —— 信号没有落到 ctx 上，委派会继续跑")
	}

	select {
	case got := <-h.notices:
		if got != "interrupt" {
			t.Errorf("OnSignal 收到 %q，期望 interrupt", got)
		}
	case <-time.After(time.Second):
		t.Fatal("OnSignal 没被调用：操作者按了 Ctrl+C 连一句话都收不到")
	}

	// 第一次只停任务，不硬退出。看门狗此刻正等在第二个 select 上，所以这是确定的空检查。
	select {
	case <-h.hardExit:
		t.Error("第一次信号就跑了硬退出钩子")
	case <-h.exitCodes:
		t.Error("第一次信号就把进程退了，收尾序列（FlushAll / Drain / rollout）不会跑")
	default:
	}
}

func TestSecondSignalRunsHardExitHookThenExits(t *testing.T) {
	h := newHarness(t)
	_, stop := Install(Config{
		OnHardExit: func() { h.hardExit <- struct{}{} },
	})
	defer stop()

	h.send(t, syscall.SIGINT)
	h.send(t, syscall.SIGTERM)

	// 顺序是这条路径的全部意义：钩子必须先于 os.Exit —— 硬退出跳过所有 defer，
	// 不在钩子里杀掉自己起的进程组，留下的就是孤儿。
	select {
	case <-h.hardExit:
	case <-time.After(2 * time.Second):
		t.Fatal("第二次信号没有跑硬退出钩子")
	}

	var code int
	select {
	case code = <-h.exitCodes:
	case <-time.After(2 * time.Second):
		t.Fatal("第二次信号没有退出进程")
	}
	if want := 128 + int(syscall.SIGTERM); code != want {
		t.Errorf("退出码 = %d，期望 %d（128+signum）", code, want)
	}
}

func TestStopRetiresWatcherAndRestoresRoot(t *testing.T) {
	newHarness(t)
	ctx, stop := Install(Config{})
	if Root() != ctx {
		t.Error("Install 之后 Root() 不是这一次的任务 root")
	}

	stop()

	if Root() != context.Background() {
		t.Error("收尾之后 Root() 没有退回 Background：下一次装会拿到上一次的残骸")
	}
	if ctx.Err() != nil {
		t.Errorf("收尾不该取消任务 root（那是留给真信号的动作）：%v", ctx.Err())
	}
	// 看门狗必须真的退掉，否则每次正常退出都会在 Drain 里多报一条泄漏。
	if leaked := supervise.Root().Drain(2 * time.Second); len(leaked) != 0 {
		t.Errorf("收尾后根域里还有 goroutine：%v", supervise.LeaseNames(leaked))
	}
}
