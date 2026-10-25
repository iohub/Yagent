package tui

import (
	"testing"
	"time"

	"yagent/internal/http"
)

// ⧗ 段带子右段的 total 是唯一每 tick 会变的数字，它有三条必须守住的口径：
// 起点由提交任务那一刻决定、暂停那段时间不算工作量、任务结束时定格。
// 三条各对应一条会红的测试（docs/dashboard-latency-panel.md §6）。

func TestLatencyMarkTaskStartedResetsPerTask(t *testing.T) {
	m := newTestModel()
	first := time.Now().Add(-time.Hour)
	m.latency.taskStart = first
	m.latency.endedAt = first.Add(90 * time.Second)
	m.latency.paused = 30 * time.Second

	m.markTaskStarted()

	if m.latency.taskStart.Equal(first) {
		t.Error("第二次提交没有重算起点 —— 两次任务之间的空闲会被算进工作量")
	}
	if !m.latency.endedAt.IsZero() {
		t.Errorf("结束后仍留着上一次的定格时刻：endedAt=%v", m.latency.endedAt)
	}
	if m.latency.paused != 0 {
		t.Errorf("上一任务的暂停累计没有清零：%v", m.latency.paused)
	}
	if !m.taskStarted {
		t.Error("markTaskStarted 没有置起 taskStarted（输入框边框色依赖它）")
	}
	if got := m.latency.elapsed(time.Now()); got > time.Second {
		t.Errorf("刚起点的 total = %v, want ≈0", got)
	}
}

// TestLatencyPauseDoesNotAdvanceTotal 暂停不是取消（根 AGENTS.md §11 坑 17）：
// 闸门停住那段时间必须从 total 里扣掉，而"已经按下暂停、还有工具在飞"那段
// 不算暂停 —— 那是真在工作。
func TestLatencyPauseDoesNotAdvanceTotal(t *testing.T) {
	m := newTestModel()
	task := gatedTask(http.TaskStatusRunning)
	m.currentTask = task
	m.taskRunning = true
	base := time.Now()
	m.latency.taskStart = base

	// 没暂停：什么都不该记
	m.accountPause(base)
	if !m.latency.pauseStart.IsZero() || m.latency.paused != 0 {
		t.Errorf("未暂停却被记账：pauseStart=%v paused=%v", m.latency.pauseStart, m.latency.paused)
	}

	// PAUSING：请求已按下，但还没有循环停在 step 边界上 —— 这 10 秒是工作时间。
	task.Gate().Pause("test")
	m.accountPause(base)
	if m.latency.paused != 0 || !m.latency.pauseStart.IsZero() {
		t.Errorf("PAUSING 被当成了暂停：paused=%v pauseStart=%v", m.latency.paused, m.latency.pauseStart)
	}

	parked := parkOnGate(task.Gate())
	waitFor(t, "循环停在闸门上", func() bool { return task.Gate().Blocked() })

	// 真的停住了：从 base+10s 起算暂停
	m.accountPause(base.Add(10 * time.Second))
	if got := m.latency.elapsed(base.Add(30 * time.Second)); got != 10*time.Second {
		t.Errorf("停着 20 秒后 total = %v, want 10s（只该算停住之前那 10 秒）", got)
	}

	// 放开闸门：那段被结算成累计，之后的时间重新计薪
	task.Gate().Resume()
	select {
	case err := <-parked:
		if err != nil {
			t.Errorf("放开闸门后 Wait 返回错误：%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("闸门没放开")
	}
	m.accountPause(base.Add(30 * time.Second))
	if m.latency.paused != 20*time.Second {
		t.Errorf("暂停累计 = %v, want 20s", m.latency.paused)
	}
	if !m.latency.pauseStart.IsZero() {
		t.Errorf("恢复后还留着开区间：%v", m.latency.pauseStart)
	}
	if got := m.latency.elapsed(base.Add(40 * time.Second)); got != 20*time.Second {
		t.Errorf("恢复后再跑 10 秒，total = %v, want 20s", got)
	}
}

// TestLatencyTickAccountsForPause 结算点接在 handleTickMsg 里，而且在早退之前 ——
// 暂停时 taskRunning 仍为真，tick 不停；接错位置的话暂停中的面板会一路涨 total。
func TestLatencyTickAccountsForPause(t *testing.T) {
	m := newTestModel()
	task := gatedTask(http.TaskStatusRunning)
	m.currentTask = task
	m.taskRunning = true
	m.latency.taskStart = time.Now().Add(-time.Minute)

	task.Gate().Pause("test")
	parked := parkOnGate(task.Gate())
	waitFor(t, "循环停在闸门上", func() bool { return task.Gate().Blocked() })

	m.Update(tickMsg{})
	if m.latency.pauseStart.IsZero() {
		t.Error("tick 没有结算暂停状态（accountPause 没接进 handleTickMsg，或接在早退之后）")
	}

	task.Gate().Resume()
	<-parked
}

// TestLatencyTotalFreezesOnTaskComplete 收尾必须定格 total：不定格的话空闲态
// 每秒仍在重画整段面板，而停 tick 的条件永远不满足 —— 静置时界面在转。
func TestLatencyTotalFreezesOnTaskComplete(t *testing.T) {
	// handleTaskCompleteMsg 结尾会 SetValue 清空输入框：newTestModel 没有可用的
	// textarea，那条路会 nil panic 而不是断言失败，所以用 chromeTestModel。
	m := chromeTestModel(t, 140, 40)
	m.taskRunning = true
	m.latency.taskStart = time.Now().Add(-30 * time.Second)

	if _, cmd := m.Update(taskCompleteMsg{taskID: "t", result: "done"}); cmd != nil {
		// 收尾只推一个对话框，不该发起异步动作；返回 cmd 说明走了别的路。
		t.Logf("taskCompleteMsg 返回了 cmd（非预期但不致命），继续断言定格")
	}
	if m.latency.endedAt.IsZero() {
		t.Fatal("收尾没有把 total 定格（freezeTotal 没接进 handleTaskCompleteMsg）")
	}
	first := m.latency.elapsed(time.Now().Add(time.Hour))
	second := m.latency.elapsed(time.Now().Add(2 * time.Hour))
	if first != second {
		t.Errorf("定格之后还在增长：%v → %v", first, second)
	}
	if first < 29*time.Second || first > 2*time.Minute {
		t.Errorf("定格的 total = %v, want ≈30s", first)
	}
}

// TestLatencyClearSessionDropsTotal 清空会话 = 换一块板：total 与窗口都要归零。
func TestLatencyClearSessionDropsTotal(t *testing.T) {
	m := chromeTestModel(t, 140, 40)
	testLang(t)
	m.initSessionTabs()
	m.latency.taskStart = time.Now()
	m.latency.observe(latencySample{id: "r", label: "read_file", dur: time.Second,
		ts: time.Now(), agent: "Coding"})

	m.clearCurrentSession()

	if !m.latency.taskStart.IsZero() {
		t.Error("清空会话后 total 还在从旧起点计时")
	}
	if len(m.latency.samples) != 0 {
		t.Errorf("清空会话后窗口还有 %d 条样本", len(m.latency.samples))
	}
}
