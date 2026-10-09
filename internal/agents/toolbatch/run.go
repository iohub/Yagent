package toolbatch

import (
	"context"
	"log/slog"
	"runtime/debug"
	"sync"
)

// Run 按给定 Segment 调度执行，并对每个元素回调 onCommit：
//
//   - 串行段（Parallel=false）或单元素并行段或并行度 ≤1：
//     走无 goroutine 的串行快路径 —— 逐元素 SyncExec + SyncCommit，
//     与"调用方自己 for 循环串行执行"的行为与事件顺序完全一致；
//   - 多元素并行段：组内并发执行（channel 信号量限流 parallel），每 goroutine
//     只写自己的结果槽（预分配 []T，免锁），WaitGroup join 后在父线程按
//     seg.Indices（=原索引序）依次 onCommit —— 保证 commit 顺序确定，
//     并规避回调方的线程安全问题。
//
// ctx 传播：exec 收到的即传入的父 ctx；单个元素的失败/取消不影响兄弟元素，
// 不做元素间共享 cancel（元素 per-call ctx 由调用方在 exec 闭包内自行派生）。
// 父 ctx 取消时传播到所有在飞工具；信号量排队中尚未启动的调用也会照常启动
// （以已取消 ctx 调用 exec）并正常走 commit —— 保证"每个元素必有对应产物"。
//
// panic 防守（相对纯串行行为的有意变化）：exec 的 panic 在 goroutine 内被
// recover 并 slog 记录堆栈，转化为该元素的失败（T 零值），兄弟元素不受影响。
// 业务侧如需展示 panic 文案，应在自身 exec 闭包内用具名返回值 recover 并
// 格式化（两道防线：本层兜底防进程崩溃，业务层负责用户可见格式）。
//
// maxParallel ≤0 视为并行度 1；=1 时严格串行（kill-switch 语义，事件顺序
// 与串行时代完全一致）。
func Run[T any](ctx context.Context, seg Segment, parallel int,
	exec func(ctx context.Context, i int) T,
	onCommit func(i int, res T)) {

	if len(seg.Indices) == 0 {
		return
	}
	if parallel < 1 {
		parallel = 1
	}

	// 串行快路径：非并行段（原位元素）、组大小 1、并行度 1（kill-switch）。
	// 无 goroutine，逐元素 exec+commit，事件顺序与逐元素串行完全一致。
	if !seg.Parallel || len(seg.Indices) == 1 || parallel == 1 {
		for _, i := range seg.Indices {
			onCommit(i, guardedExec(ctx, i, exec))
		}
		return
	}

	// 并发路径：结果槽预分配，每 goroutine 只写自己下标（免锁）。
	results := make([]T, len(seg.Indices))
	var wg sync.WaitGroup
	sem := make(chan struct{}, parallel)

	for slot, i := range seg.Indices {
		wg.Add(1)
		go func(slot, i int) {
			defer wg.Done()
			// 信号量在 per-call ctx 创建之前获取（设计定稿）。
			// 父 ctx 已取消时不阻塞等待信号量，也【不】执行释放（acquired
			// 标志保证从未获取的 goroutine 不做 `<-sem` 接收，否则会与其他
			// 释放方的接收竞争、令 WaitGroup 永久无法 Done 而死锁）：
			// 直接以已取消 ctx 启动调用，由业务 exec 产出错误结果并照常
			// commit —— 与串行现状一致（每个 tool_call 都有对应 tool 消息），
			// 只是本就取消、立即失败。
			acquired := false
			select {
			case sem <- struct{}{}:
				acquired = true
			case <-ctx.Done():
			}
			if acquired {
				defer func() { <-sem }()
			}
			results[slot] = guardedExec(ctx, i, exec)
		}(slot, i)
	}
	wg.Wait()

	// join 后按原索引序 commit（父线程单线程）。
	for slot, i := range seg.Indices {
		onCommit(i, results[slot])
	}
}

// guardedExec 单元素执行 + panic 兜底 recover。
// 正常路径零开销差异（一次 defer）；panic 路径返回 T 零值并记录堆栈，
// 保证：① goroutine 不崩溃整个进程；② WaitGroup 计数正常 Done；
// ③ 兄弟元素照常执行与 commit。
func guardedExec[T any](ctx context.Context, i int, exec func(context.Context, int) T) (res T) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("toolbatch: recovered panic in parallel tool execution",
				"item_index", i, "panic", r, "stack", string(debug.Stack()))
			var zero T
			res = zero
		}
	}()
	return exec(ctx, i)
}
