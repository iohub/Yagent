package http

// task_shutdown_test.go — 信号 → 任务取消这条链唯一端到端的一钉。
// 真的给测试进程发 SIGTERM：shutdown.Install 装了处理器，所以它不会把测试进程打死。

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"

	"yagent/internal/pause"
	"yagent/internal/shutdown"
)

func TestSignalCancelsTaskContext(t *testing.T) {
	_, stopSignals := shutdown.Install(shutdown.Config{})
	t.Cleanup(stopSignals)

	taskCtx, cancelTask := NewTaskContext(0)
	t.Cleanup(cancelTask)

	// 换根不许把闸门弄丢：Task.Gate() 仍然从同一个 ctx 上读回来。
	if pause.From(taskCtx) == nil {
		t.Fatal("任务 ctx 上没有暂停闸门")
	}

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("发 SIGTERM 失败: %v", err)
	}

	select {
	case <-taskCtx.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("SIGTERM 没有取消任务 ctx —— NewTaskContext 的根不在 shutdown.Root() 上")
	}
	if !errors.Is(taskCtx.Err(), context.Canceled) {
		t.Errorf("任务 ctx 的结束原因 = %v，期望 context.Canceled（信号取消不是超时）", taskCtx.Err())
	}
}
