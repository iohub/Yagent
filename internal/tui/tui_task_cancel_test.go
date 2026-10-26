package tui

// 任务被取消时的 ctx 交接（docs/structured-concurrency-design.md §9 的一项）：
// handleTaskCompleteMsg 会给同一个 Task 对象换上一份新 ctx（好让它能被复用），
// 而旧的那份挂在 context.Background() 下 —— 没有上游会替它取消。
// 覆盖 CancelFunc 而不调用它，等于每取消一次任务就漏一个永远活着的 context。

import (
	"context"
	"syscall"
	"testing"
	"time"

	"yagent/internal/http"
	"yagent/internal/shutdown"
)

func TestSupersededTaskReleasesOldContext(t *testing.T) {
	m := chromeTestModel(t, 120, 40)

	oldCtx, oldCancel := context.WithCancel(context.Background())
	t.Cleanup(oldCancel) // 断言失败时不许把这条测试自己的 ctx 也漏出去
	m.currentTask = &http.Task{
		ID:         "t1",
		Status:     "running",
		Context:    oldCtx,
		CancelFunc: oldCancel,
	}
	m.taskRunning = true
	m.taskCancelled = true

	updated, _ := m.Update(taskCompleteMsg{taskID: "t1"})
	m = updated.(*model)

	if oldCtx.Err() == nil {
		t.Error("被换掉的旧 ctx 还活着 —— CancelFunc 被直接覆盖了，这份 ctx 永远不会释放")
	}
	if m.currentTask == nil {
		t.Fatal("currentTask 被清空了，want 换成一份新 ctx")
	}
	if m.currentTask.Context == oldCtx {
		t.Error("没有换上新的 ctx")
	}
	if m.currentTask.Context.Err() != nil {
		t.Errorf("新 ctx 一上来就是死的：%v", m.currentTask.Context.Err())
	}
}

// TestCancelledTaskReplacementStaysUnderShutdownRoot 钉换上去的那份新 ctx 也在
// shutdown.Root() 之下。挂在 Background 上就等于"这条任务从此不随信号取消"——
// 而正常路径（http.NewTaskContext）已经在根上了，两处不一致就是 §11 坑 18 的形状。
// 走真信号：shutdown.Install 装了处理器，所以 SIGTERM 不会把测试进程打死。
func TestCancelledTaskReplacementStaysUnderShutdownRoot(t *testing.T) {
	_, stopSignals := shutdown.Install(shutdown.Config{})
	t.Cleanup(stopSignals)

	m := chromeTestModel(t, 120, 40)
	oldCtx, oldCancel := context.WithCancel(shutdown.Root())
	t.Cleanup(oldCancel)
	m.currentTask = &http.Task{
		ID:         "t2",
		Status:     "running",
		Context:    oldCtx,
		CancelFunc: oldCancel,
	}
	m.taskRunning = true
	m.taskCancelled = true

	updated, _ := m.Update(taskCompleteMsg{taskID: "t2"})
	m = updated.(*model)
	replacement := m.currentTask.Context

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("发 SIGTERM 失败: %v", err)
	}
	select {
	case <-replacement.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("取消后重建的任务 ctx 不随信号结束：它没挂在 shutdown.Root() 上")
	}
}
