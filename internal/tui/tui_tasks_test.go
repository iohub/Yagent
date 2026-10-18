package tui

// tui_tasks_test.go — 任务收尾的接线顺序（docs/structured-concurrency-design.md C1）。
//
// 这里只钉一件事：先停成员、再等域。defer 是 LIFO，把 drain 注册在 close(stop) 之后
// 就会先执行 —— 那时周期保存还在跑，症状不是崩溃，是每次任务收尾都白等一个 Drain 预算
// 外加一条假泄漏告警。这种错只能靠"顺序写在函数体里 + 一条会因顺序颠倒而红的测试"防住。

import (
	"context"
	"testing"
	"time"

	"yagent/internal/http"
	"yagent/internal/supervise"
)

func TestStopTaskBackgroundStopsMembersBeforeDraining(t *testing.T) {
	// 走生产的构造入口：任务 ctx 必须同时带暂停闸门与并发域。
	ctx, cancel := http.NewTaskContext(0)
	defer cancel()
	task := &http.Task{ID: "order", Context: ctx}

	stop := make(chan struct{})
	ticker := time.NewTicker(time.Hour)
	exited := make(chan struct{})

	supervise.From(ctx).Go("tui/periodic-save", func(context.Context) {
		<-stop
		close(exited)
	})

	start := time.Now()
	stopTaskBackground(task, stop, ticker)
	elapsed := time.Since(start)

	select {
	case <-exited:
	default:
		t.Error("收尾没有把周期保存的成员停下来")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("收尾花了 %v，want <500ms —— drain 跑在 close(stop) 之前了，"+
			"于是每次任务结束都白等一整个 Drain 预算", elapsed)
	}
}

// 任务 ctx 上的域必须是这一条任务自己的：两条任务共用一条域的话，
// A 的收尾会去等 B 还在跑的保存。
func TestEachTaskGetsItsOwnDomain(t *testing.T) {
	ctxA, cancelA := http.NewTaskContext(0)
	defer cancelA()
	ctxB, cancelB := http.NewTaskContext(0)
	defer cancelB()

	block := make(chan struct{})
	supervise.From(ctxA).Go("tui/periodic-save", func(context.Context) { <-block })

	leaked := supervise.From(ctxB).Drain(50 * time.Millisecond)
	if len(leaked) != 0 {
		t.Errorf("B 的收尾看见了 A 的成员: %v", leaked)
	}
	close(block)
	if leaked := supervise.From(ctxA).Drain(2 * time.Second); len(leaked) != 0 {
		t.Errorf("A 的收尾没等到自己的成员: %v", leaked)
	}
}
