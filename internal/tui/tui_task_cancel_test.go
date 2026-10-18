package tui

// 任务被取消时的 ctx 交接（docs/structured-concurrency-design.md §9 的一项）：
// handleTaskCompleteMsg 会给同一个 Task 对象换上一份新 ctx（好让它能被复用），
// 而旧的那份挂在 context.Background() 下 —— 没有上游会替它取消。
// 覆盖 CancelFunc 而不调用它，等于每取消一次任务就漏一个永远活着的 context。

import (
	"context"
	"testing"

	"yagent/internal/http"
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
