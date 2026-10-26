// Package shutdown 把进程信号接成一条 ctx 取消链。
//
// 存在理由（docs/run-bash-hang-fix.md §4）：`run_bash` 起了自己的进程组之后，终端信号就不再
// 天然传到子进程 —— "谁杀子进程"只剩一条答案：ctx 取消，然后由工具层杀它自己起的那个组。
// 所以信号必须落到 ctx 上，而不是落到 os.Exit 上；否则"接管信号"与"留下孤儿"是同一件事。
//
// 依赖方向：只准标准库 + internal/supervise（本包那条 goroutine 的落脚点；守卫见
// scripts/check_shutdown_leaf.sh）。
package shutdown

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"yagent/internal/supervise"
)

// Config 是两次信号各自到点要做什么，都由调用方（main）给：本包不认识界面、日志与子进程。
type Config struct {
	// OnSignal 在第一次信号到达、取消任务 root **之前**调用 —— 给操作者一句话的机会。可为 nil。
	OnSignal func(sig os.Signal)
	// OnHardExit 在第二次信号到达、进程退出**之前**调用 —— 杀掉我们自己起的进程组。可为 nil。
	// 硬退出跳过所有 defer，所以该清理的子进程只能在这里清。
	OnHardExit func()
}

// signalSeam 是"把进程信号搬进 channel"这一步。测试替换它，然后直接往 channel 里塞
// os.Interrupt —— 给测试进程发真信号会打断整个测试二进制。
var signalSeam = func(ch chan<- os.Signal) func() {
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	return func() { signal.Stop(ch) }
}

// exitSeam 让"第二次信号硬退出"这条分支可被观察，而不用真的把测试进程干掉。
var exitSeam = os.Exit

var (
	mu   sync.Mutex
	root = context.Background()
)

// Root 是所有任务 ctx 的根。没装过处理器时它就是 context.Background()，
// 于是测试与库用法拿到的行为与装了之前逐字一致。
func Root() context.Context {
	mu.Lock()
	defer mu.Unlock()
	return root
}

// Install 注册 SIGINT / SIGTERM，返回任务 root 与一个收尾函数。
//
// 第一次信号：取消任务 root —— 在飞的委派在 step 边界停下，现有的收尾序列
// （rollout 落盘、DataManager.FlushAll、yagent.Close 里的 Drain）照常跑完。
// 第二次信号：不等了，先 OnHardExit 再按 128+signum 退出。必须有这条路 —— 收尾最坏要等满
// 一个工具超时（agents.defaultToolTimeout = 180s），操作者不会等那么久。
//
// 收尾函数做三件事：摘掉信号处理器、退掉那条 goroutine、把 Root() 换回装之前那份。
// 它**不取消**任务 root（那是留给真信号的动作），但把 goroutine 收掉是必须的 —— 否则
// 每次正常退出都会在 `supervise.Root().Drain` 里多报一条 "shutdown/signals" 泄漏。
// 一次进程装一次；测试靠返回的收尾函数隔离。
func Install(cfg Config) (context.Context, func()) {
	taskCtx, cancelTasks := context.WithCancel(context.Background())
	watchCtx, stopWatch := context.WithCancel(context.Background())

	mu.Lock()
	prev := root
	root = taskCtx
	mu.Unlock()

	ch := make(chan os.Signal, 4)
	stopNotify := signalSeam(ch)

	supervise.Root().Go("shutdown/signals", func(context.Context) {
		select {
		case sig := <-ch:
			if cfg.OnSignal != nil {
				cfg.OnSignal(sig)
			}
			cancelTasks()
			select {
			case second := <-ch:
				if cfg.OnHardExit != nil {
					cfg.OnHardExit()
				}
				exitSeam(exitCodeFor(second))
			case <-watchCtx.Done():
			}
		case <-watchCtx.Done():
		}
	})

	return taskCtx, func() {
		stopNotify()
		stopWatch()
		mu.Lock()
		// 只撤自己这一次：更晚的 Install 已经把 root 换成别的了，不许被这里覆盖掉。
		if root == taskCtx {
			root = prev
		}
		mu.Unlock()
	}
}

// exitCodeFor 用 shell 的惯例：128 + signum（SIGINT → 130，SIGTERM → 143）。
// 一条公式而不是两张表 —— 加一种信号不该记得再改第二处。
func exitCodeFor(sig os.Signal) int {
	s, ok := sig.(syscall.Signal)
	if !ok {
		return 128 + int(syscall.SIGINT)
	}
	return 128 + int(s)
}
