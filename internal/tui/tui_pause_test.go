package tui

// tui_pause_test.go — /pause 与 /continue 的接线（docs/pause-resume-design.md §8.2）。
//
// 钉三件事，每一件都有一个"看起来没事"的坏实现：
//   - 斜杠前缀在本 TUI 里默认是**搜索**，/pause 必须显式列进命令分支，
//     否则用户按下去的是"搜索字符串 pause"，界面上什么都没发生。
//   - 状态栏的暂停标记必须区分"正在停"与"已停住"。spinner 停在暂停的任务上
//     等于骗用户说还在干活；而把"正在停"显示成"已停住"更糟 —— 一个在飞的
//     run_bash 最长要等完 180s 才到边界。
//   - 拒绝必须是真拒绝：没有闸门、没有任务、没暂停过，都不许改状态。

import (
	"context"
	"testing"
	"time"

	"yagent/internal/http"
	"yagent/internal/pause"
)

func gatedTask(status string) *http.Task {
	ctx, cancel := http.NewTaskContext(0)
	return &http.Task{
		ID:         "tui-pause",
		Status:     status,
		Context:    ctx,
		CancelFunc: cancel,
	}
}

func parkOnGate(gate *pause.Gate) chan error {
	parked := make(chan error, 1)
	go func() { parked <- gate.Wait(context.Background()) }()
	return parked
}

// waitFor 轮询到条件成立；超时就把当前值报出来，别让失败变成"没反应"。
func waitFor(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("超时等待：%s", desc)
}

func TestSlashPauseDrivesTheGateOnTheTaskContext(t *testing.T) {
	m := newTestModel()
	task := gatedTask(http.TaskStatusRunning)
	m.currentTask = task
	m.taskRunning = true
	gate := task.Gate()
	if gate == nil {
		t.Fatal("夹具失效：任务 ctx 上没有闸门")
	}

	if cmd := m.processCommand("/pause"); cmd != nil {
		t.Errorf("/pause 返回了 cmd %v，期望 nil（它只是按下闸门，不发起异步动作）", cmd)
	}
	if !gate.Paused() {
		t.Fatal("/pause 没有按下任务 ctx 上的闸门（多半掉进 /搜索 分支了）")
	}
	if task.Status != http.TaskStatusPaused {
		t.Errorf("Status = %q, want %q", task.Status, http.TaskStatusPaused)
	}
	// 还没有循环停在边界上 → 必须是 PAUSING。
	if got := m.airlinePauseLabel(); got != "⏸ PAUSING" {
		t.Errorf("airlinePauseLabel() = %q, want %q", got, "⏸ PAUSING")
	}

	parked := parkOnGate(gate)
	waitFor(t, "循环停在闸门上", func() bool { return gate.Blocked() })
	if got := m.airlinePauseLabel(); got != "⏸ PAUSED" {
		t.Errorf("airlinePauseLabel() = %q, want %q", got, "⏸ PAUSED")
	}

	m.processCommand("/continue")
	select {
	case err := <-parked:
		if err != nil {
			t.Errorf("放开闸门后 Wait 返回错误：%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("/continue 没有放开闸门")
	}
	if task.Status != http.TaskStatusRunning {
		t.Errorf("Status = %q, want %q", task.Status, http.TaskStatusRunning)
	}
	if got := m.airlinePauseLabel(); got != "" {
		t.Errorf("继续之后仍有暂停文案：%q", got)
	}
}

// 冒号与裸写形式必须与斜杠形式走同一条路（processCommand 的两种前缀都要认）。
func TestColonPauseAndContinueMatchSlash(t *testing.T) {
	m := newTestModel()
	task := gatedTask(http.TaskStatusRunning)
	m.currentTask = task
	m.taskRunning = true

	m.processCommand(":pause")
	if !task.Gate().Paused() {
		t.Error(":pause 没有按下闸门，与 /pause 不同路")
	}
	m.processCommand(":continue")
	if task.Gate().Paused() {
		t.Error(":continue 没有放开闸门")
	}

	m.processCommand("pause")
	if !task.Gate().Paused() {
		t.Error("裸 pause 没有按下闸门")
	}
	m.processCommand("unpause")
	if task.Gate().Paused() {
		t.Error("裸 unpause 没有放开闸门")
	}
}

// 三种"做不到"都必须拒绝，而且不许留下骗人的状态或文案。
func TestPauseRefusalsLeaveStateAlone(t *testing.T) {
	// 没有任务。
	idle := newTestModel()
	idle.processCommand("/pause")
	if idle.infoMsg == "" {
		t.Error("无任务时 /pause 没有任何反馈")
	}
	if got := idle.airlinePauseLabel(); got != "" {
		t.Errorf("无任务时仍有暂停文案：%q", got)
	}
	// 没暂停过就 /continue。
	idle.taskRunning = true
	idle.currentTask = gatedTask(http.TaskStatusRunning)
	idle.processCommand("/continue")
	if idle.currentTask.Gate().Paused() {
		t.Error("对没暂停过的任务按 /continue 把闸门按下了")
	}

	// 任务跑完了（taskRunning=false）→ 停不了。
	ended := newTestModel()
	endedTask := gatedTask(http.TaskStatusFinished)
	ended.currentTask = endedTask
	ended.taskRunning = false
	ended.processCommand("/pause")
	if endedTask.Gate().Paused() {
		t.Error("对已结束的任务按下了闸门")
	}
	if endedTask.Status != http.TaskStatusFinished {
		t.Errorf("被拒绝的暂停把状态改成了 %q", endedTask.Status)
	}

	// ctx 上没有闸门（绕过 NewTaskContext 构造的任务）→ 拒绝并说明，不改状态。
	bare := newTestModel()
	bareTask := &http.Task{ID: "bare", Status: http.TaskStatusRunning, Context: context.Background()}
	bare.currentTask = bareTask
	bare.taskRunning = true
	bare.processCommand("/pause")
	if bareTask.Status != http.TaskStatusRunning {
		t.Errorf("没有闸门的任务被改成了 %q，而它根本停不了", bareTask.Status)
	}
	if bare.infoMsg == "" {
		t.Error("没有闸门时 /pause 没有任何反馈")
	}
	if got := bare.airlinePauseLabel(); got != "" {
		t.Errorf("没有闸门时仍有暂停文案：%q", got)
	}
}

// 状态栏必须整体带上这段文案，而不只是模型方法返回它 —— 用户看的是 footer。
// 宽度矩阵在这里的用意：新增的是**可选**段，未暂停时一个字都不该多出来。
func TestAirlineOmitsPauseSegmentWhenIdle(t *testing.T) {
	m := newTestModel()
	m.taskRunning = true
	m.currentAgent = ""

	before := len(m.airlineRightSegments())
	m.currentTask = gatedTask(http.TaskStatusRunning)
	if got := len(m.airlineRightSegments()); got != before {
		t.Errorf("未暂停时段数从 %d 变成 %d（多出来的一段会把宽度矩阵顶歪）", before, got)
	}

	m.currentTask.Gate().Pause("/pause")
	segs := m.airlineRightSegments()
	// 暂停段是**替换** spinner，不是多加一段：段数变化会把宽度矩阵顶歪。
	if len(segs) != before {
		t.Fatalf("暂停后段数 = %d, want %d（暂停段应替换 spinner）", len(segs), before)
	}
	last := segs[len(segs)-1].text
	if last != "⏸ PAUSING" {
		t.Errorf("暂停段文案 = %q, want ⏸ PAUSING", last)
	}
	// spinner 必须被换掉，否则状态栏同时在说"在跑"和"停住了"。
	for _, s := range segs {
		if s.text == m.anim.Render() {
			t.Error("暂停中仍在渲染 spinner")
		}
	}
}
