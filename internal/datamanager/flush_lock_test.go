package datamanager

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"yagent/internal/memory"
)

// FlushAll 不许持着 dm.mu 做 IO。dm.mu 是每个任务 SaveTaskMemory 都要过的入口
// （getOrCreateWriter），而原来整个循环都在锁里逐任务写文件 + parse 索引：
// 退出时"把最后几条刷干净"这一步会把所有还在写的任务一起顶住 ——
// 并行委派下那就是一整批任务在门外等，且不报错。

func TestFlushAllReleasesManagerLockBeforeIO(t *testing.T) {
	dm := newTestDataManager(t)
	// 防抖窗口拉到一小时：刷盘只由 FlushAll 触发，timer 不参与计时。
	shrinkDebounce(t, time.Hour)

	for _, id := range []string{"slow-task", "other-task"} {
		mem := memory.NewConversationMemory(32)
		mem.AddHumanMessage("payload for " + id)
		if err := dm.SaveTaskMemory(id, mem); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
	}

	var once sync.Once
	entered := make(chan struct{})
	release := make(chan struct{})
	prevHook := flushHook
	flushHook = func(path string) {
		if strings.HasSuffix(path, "slow-task.jsonl") {
			once.Do(func() { close(entered) })
			<-release
		}
	}
	t.Cleanup(func() {
		flushHook = prevHook
		once.Do(func() { close(release) })
	})

	flushAllDone := make(chan error, 1)
	go func() { flushAllDone <- dm.FlushAll() }()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("FlushAll 没走到那条慢写 —— 钩子或路径判据漂了，本用例没有钉住任何东西")
	}

	// 另一条任务的入口必须在 FlushAll 还卡在第一条上时就能用。
	probe := make(chan error, 1)
	go func() {
		mem := memory.NewConversationMemory(32)
		mem.AddHumanMessage("third task arrives mid-flush")
		probe <- dm.SaveTaskMemory("third-task", mem)
	}()
	select {
	case err := <-probe:
		if err != nil {
			t.Fatalf("SaveTaskMemory(third-task): %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("FlushAll 把 dm.mu 压在 IO 底下 —— 别的任务连登记都进不来")
	}

	close(release)
	select {
	case err := <-flushAllDone:
		if err != nil {
			t.Errorf("FlushAll: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("放行之后 FlushAll 没有收尾")
	}

	// 改成"锁外刷"不许变成"少刷一条"。
	for _, id := range []string{"slow-task", "other-task"} {
		if got := flushedLines(t, filepath.Join(dm.dataDir, id+".jsonl")); got < 1 {
			t.Errorf("%s 落盘 %d 行, want ≥1", id, got)
		}
	}
}

// 第一条失败不许放弃剩下的任务：退出时"刷盘失败"应当是**刷完能刷的、报第一错**，
// 而不是让后面 N-1 条任务记忆凭空留在内存里蒸发。
func TestFlushAllKeepsGoingAfterFirstFailure(t *testing.T) {
	dm := newTestDataManager(t)
	shrinkDebounce(t, time.Hour)

	for _, id := range []string{"broken-task", "healthy-task"} {
		mem := memory.NewConversationMemory(32)
		mem.AddHumanMessage("payload for " + id)
		if err := dm.SaveTaskMemory(id, mem); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
	}
	// 把坏任务的目标路径占成目录：os.Create 必然失败（"is a directory"），
	// 而另一条照常可写 —— 不需要为失败注入钩子。
	broken := filepath.Join(dm.dataDir, "broken-task.jsonl")
	if err := os.Mkdir(broken, 0o755); err != nil {
		t.Fatalf("mkdir to occupy the path: %v", err)
	}

	err := dm.FlushAll()
	if err == nil {
		t.Error("坏任务刷盘失败而 FlushAll 回报 nil")
	}
	if got := flushedLines(t, filepath.Join(dm.dataDir, "healthy-task.jsonl")); got < 1 {
		t.Errorf("healthy-task 落盘 %d 行, want ≥1 —— 第一条失败把剩下的也一起放弃了", got)
	}
}

func flushedLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Count(strings.TrimRight(string(data), "\n"), "\n") + 1
}
