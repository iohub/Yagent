package http

// 任务表的并发形状（docs/structured-concurrency-design.md §9）：
// tm.lock 跨住 Socket.Write / melody.Broadcast，等于让一个浏览器标签的
// TCP 背压去决定"别的任务还能不能报告状态"。这里钉两件事：
// ① 投递在锁外；② 终态任务不会永远堆在表里。

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/olahol/melody"
)

// blockWrites 把唯一的投递出口换成"收到第一笔就卡住"的假出口。
// 它是测试用的 seam（writeSession 本体），不是被测行为。
func blockWrites(t *testing.T) (entered <-chan struct{}, release func()) {
	t.Helper()
	enterCh := make(chan struct{})
	releaseCh := make(chan struct{})
	var once sync.Once
	var closeOnce sync.Once

	prev := writeSession
	writeSession = func(_ *melody.Session, _ []byte) error {
		once.Do(func() { close(enterCh) })
		<-releaseCh
		return nil
	}
	broadcastPrev := melodyBroadcast
	melodyBroadcast = func(_ *melody.Melody, _ []byte) error { return nil }

	t.Cleanup(func() {
		writeSession = prev
		melodyBroadcast = broadcastPrev
		closeOnce.Do(func() { close(releaseCh) })
	})

	return enterCh, func() { closeOnce.Do(func() { close(releaseCh) }) }
}

// 一条卡住的 WS 写不许把整个 TaskManager 顶住：另一条任务的状态更新与
// /api/task_status 的读都必须照常。
func TestSlowSocketDoesNotBlockTaskManager(t *testing.T) {
	entered, release := blockWrites(t)

	tm := NewTaskManager(nil, 0)
	slow := &Task{ID: "slow", Status: TaskStatusRunning, Context: context.Background(),
		Socket: &melody.Session{}, UpdatedAt: time.Now()}
	other := &Task{ID: "other", Status: TaskStatusRunning, Context: context.Background(),
		UpdatedAt: time.Now()}
	tm.AddTask(slow)
	tm.AddTask(other)

	stalled := make(chan struct{})
	go func() { tm.SetTaskResult("slow", "the answer"); close(stalled) }()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("投递出口没有被走到 —— 本用例没有钉住任何东西")
	}

	done := make(chan string, 2)
	go func() { tm.SetTaskProgress("other", "step 3"); done <- "progress" }()
	go func() { _, _ = tm.GetTask("other"); done <- "read" }()

	for i := 0; i < 2; i++ {
		select {
		case which := <-done:
			t.Logf("锁外投递之后 %s 照常返回", which)
		case <-time.After(2 * time.Second):
			t.Fatal("一条卡住的 WebSocket 写把 TaskManager 顶住了 —— Socket.Write 又回到锁里了")
		}
	}

	release()
	select {
	case <-stalled:
	case <-time.After(2 * time.Second):
		t.Fatal("放行之后 SetTaskResult 没有收尾")
	}
}

// 暂停/继续那两条广播同样不许在锁里 —— 它们广播给**所有**连接，
// 比单播更容易被一个慢客户端放大成全局停顿。
//
// 探针必须是一条**没有 socket** 的任务：投递是在 Unlock 之后同步做的，
// 带 socket 的任务自己的 setter 会停在投递上（那是它自己的客户端慢，不是别人的锁），
// 只有不需要投递的路径才能回答"锁还压着吗"。
func TestPauseBroadcastDoesNotHoldTaskManagerLock(t *testing.T) {
	entered, release := blockWrites(t)

	tm := NewTaskManager(nil, 0)
	ctx, cancel := NewTaskContext(0)
	defer cancel()
	victim := &Task{ID: "victim", Status: TaskStatusRunning, Context: ctx,
		Socket: &melody.Session{}, UpdatedAt: time.Now()}
	probe := &Task{ID: "probe", Status: TaskStatusRunning, Context: ctx, UpdatedAt: time.Now()}
	tm.AddTask(victim)
	tm.AddTask(probe)

	paused := make(chan bool, 1)
	go func() { paused <- tm.PauseTask("victim") }()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("PauseTask 没有走到投递出口")
	}

	other := make(chan struct{})
	go func() { tm.SetTaskProgress("probe", "ignored"); close(other) }()
	select {
	case <-other:
	case <-time.After(2 * time.Second):
		t.Fatal("暂停的投递把锁压住了 —— Write/Broadcast 还留在 tm.lock 里")
	}

	release()
	select {
	case ok := <-paused:
		if !ok {
			t.Error("PauseTask = false, want true")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("放行之后 PauseTask 没有收尾")
	}
}

// 表只进不出的话，每个历史 Task 连着整份 ConversationMemory 与结果正文会一路堆到
// 进程重启。清扫挂在 CreateTask 上：没人来建新任务的时候没人需要这份内存，
// 也就不必再多一条要等人等的定时 goroutine。
func TestFinishedTaskIsEvictedAfterRetention(t *testing.T) {
	prev := taskRetention
	taskRetention = 10 * time.Millisecond
	t.Cleanup(func() { taskRetention = prev })

	tm := NewTaskManager(nil, 0)
	seed := func(id, status string, age time.Duration) {
		tm.AddTask(&Task{ID: id, Status: status, Context: context.Background(),
			UpdatedAt: time.Now().Add(-age)})
	}
	seed("old-finished", TaskStatusFinished, time.Hour)
	seed("old-cancelled", TaskStatusCancelled, time.Hour)
	seed("fresh-finished", TaskStatusFinished, 0)
	seed("still-running", TaskStatusRunning, time.Hour)
	seed("still-paused", TaskStatusPaused, time.Hour)

	tm.CreateTask(nil, t.TempDir())

	for _, id := range []string{"old-finished", "old-cancelled"} {
		if _, ok := tm.GetTask(id); ok {
			t.Errorf("%s 还在表里, want 被清掉（终态且过了 retention）", id)
		}
	}
	// 没过期与还能救回来的，一个都不许动。paused 尤其关键：清掉它就是
	// 让 /api/continue_task 面对一个"从未存在过"的任务。
	for _, id := range []string{"fresh-finished", "still-running", "still-paused"} {
		if _, ok := tm.GetTask(id); !ok {
			t.Errorf("%s 被清掉了, want 保留", id)
		}
	}
}
