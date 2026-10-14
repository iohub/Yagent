package http

// task_pause_test.go — 暂停/继续的对外状态机（docs/pause-resume-design.md §8.1、§8.3）。
//
// 这里钉的是"接口不许答应做不到的事"。闸门停在 step 边界那部分由
// internal/agents/e2e_pause_test.go 钉；本文件钉的是外面这一层：每个拒绝都必须
// 是真的拒绝。把"没有闸门""从没暂停过""已经取消了"回成 200，客户端就会显示
// 已暂停/已继续，而实际上什么都没发生 —— 那正是本仓库反复踩的坑。

import (
	"context"
	"testing"
	"time"

	"yagent/internal/pause"
)

func newPauseTask(tm *TaskManager, id, status string, withGate bool) *Task {
	ctx := context.Background()
	if withGate {
		ctx, _ = NewTaskContext(0)
	}
	task := &Task{
		ID:        id,
		Status:    status,
		Context:   ctx,
		UpdatedAt: time.Now(),
	}
	tm.AddTask(task)
	return task
}

// TestTaskContextCarriesGate 钉住 ctx 与 Task.Gate() 是同一个对象。
// 分成两份状态的话，任何"换了 Context 忘了换闸门"的地方都会让 /pause 停在一个
// 没人在等的闸门上 —— 不报错，只是什么都没发生。
func TestTaskContextCarriesGate(t *testing.T) {
	ctx, cancel := NewTaskContext(0)
	defer cancel()

	gate := pause.From(ctx)
	if gate == nil {
		t.Fatal("NewTaskContext 返回的 ctx 上没有闸门")
	}
	task := &Task{ID: "t", Context: ctx}
	if task.Gate() != gate {
		t.Errorf("Task.Gate() = %p, want the same gate %p that is on the ctx", task.Gate(), gate)
	}

	// 外部按下闸门，执行侧从 ctx 取的必须是同一个 —— 这是整条链唯一的接线点。
	task.Gate().Pause("/pause")
	if !gate.Paused() {
		t.Error("从 Task 按下的 Pause 没有反映到 ctx 上的闸门")
	}

	// 绕过构造函数的 ctx 必须报"没有闸门"，而不是返回一个新建的假闸门。
	bare := &Task{ID: "bare", Context: context.Background()}
	if bare.Gate() != nil {
		t.Error("没有闸门的 ctx 必须读出 nil")
	}
	var nilTask *Task
	if nilTask.Gate() != nil {
		t.Error("nil Task 的 Gate() 必须返回 nil 而不是 panic")
	}
}

func TestPauseTaskRefusesWhatItCannotDo(t *testing.T) {
	tm := NewTaskManager(nil, 0)

	if tm.PauseTask("ghost") {
		t.Error("PauseTask 对不存在的任务返回 true")
	}
	if tm.ContinueTask("ghost") {
		t.Error("ContinueTask 对不存在的任务返回 true")
	}

	// 没有闸门的任务（ctx 不是走 NewTaskContext 建的）：两条都必须拒绝。
	noGate := newPauseTask(tm, "no-gate", TaskStatusRunning, false)
	if tm.PauseTask(noGate.ID) {
		t.Error("PauseTask 接受了没有闸门的任务")
	}
	if noGate.Status != TaskStatusRunning {
		t.Errorf("被拒绝的暂停把状态改成了 %q", noGate.Status)
	}
	if tm.ContinueTask(noGate.ID) {
		t.Error("ContinueTask 接受了没有闸门的任务")
	}

	// 已经结束的任务停不了，也没法继续。
	done := newPauseTask(tm, "done", TaskStatusFinished, true)
	if tm.PauseTask(done.ID) {
		t.Error("PauseTask 接受了已结束的任务")
	}
	if tm.ContinueTask(done.ID) {
		t.Error("ContinueTask 接受了已结束的任务")
	}

	// 从没暂停过的运行中任务没什么可继续的。
	running := newPauseTask(tm, "running", TaskStatusRunning, true)
	if tm.ContinueTask(running.ID) {
		t.Error("ContinueTask 把没暂停过的任务报成了已继续")
	}
}

func TestPauseContinueRoundTrip(t *testing.T) {
	tm := NewTaskManager(nil, 0)
	task := newPauseTask(tm, "round-trip", TaskStatusRunning, true)
	gate := task.Gate()

	if !tm.PauseTask(task.ID) {
		t.Fatal("PauseTask 拒绝了一个运行中、带闸门的任务")
	}
	if task.Status != TaskStatusPaused {
		t.Errorf("Status = %q, want %q", task.Status, TaskStatusPaused)
	}
	if !gate.Paused() {
		t.Error("状态说已暂停，但 ctx 上的闸门没被按下")
	}
	// 刚按下时必须还没停住：一个在飞的工具最长要等完 defaultToolTimeout 才到边界。
	if gate.Blocked() {
		t.Error("Blocked() 在按下瞬间就为真，UI 会显示已停住而实际上没有")
	}

	// 重复暂停是幂等的，状态不许被改写成别的东西。
	if !tm.PauseTask(task.ID) {
		t.Error("重复 PauseTask 返回 false")
	}
	if task.Status != TaskStatusPaused {
		t.Errorf("重复暂停后 Status = %q", task.Status)
	}

	if !tm.ContinueTask(task.ID) {
		t.Fatal("ContinueTask 拒绝了一个已暂停的任务")
	}
	if task.Status != TaskStatusRunning {
		t.Errorf("Status = %q, want %q", task.Status, TaskStatusRunning)
	}
	if gate.Paused() {
		t.Error("状态说在跑，但闸门还按着")
	}
	// 第二次 continue 必须拒绝：已经没有暂停可解除了。
	if tm.ContinueTask(task.ID) {
		t.Error("重复 ContinueTask 返回 true")
	}
}

// TestContinueRefusesCancelledWhilePaused：暂停后被取消 —— 闸门仍停在 paused 上，
// 但 ctx 已经死了。A 层只能在进程内继续（跨进程是 P2 的 B 层），所以这种任务必须
// 拒绝，而不是放开闸门让循环往下跑一步再立刻失败。
func TestContinueRefusesCancelledWhilePaused(t *testing.T) {
	tm := NewTaskManager(nil, 0)
	task := newPauseTask(tm, "abandoned", TaskStatusRunning, true)
	gate := task.Gate()

	if !tm.PauseTask(task.ID) {
		t.Fatal("PauseTask 被拒绝")
	}

	// 模拟取消收尾：CancelTask 把状态改成 cancelled 并解开 ctx，闸门没人放开。
	tm.CancelTask(task.ID)
	if !gate.Paused() {
		t.Fatal("夹具失效：取消应当不碰闸门")
	}
	if tm.ContinueTask(task.ID) {
		t.Error("对已取消的任务返回已继续")
	}
	if task.Status != TaskStatusCancelled {
		t.Errorf("被拒绝的继续把状态改成了 %q", task.Status)
	}
}
