package memory

// SharedMemory 的快照落盘走 util.AtomicWrite（I21）。
// 这块存储今天唯一的用户是 repo_memory 的 KV，蓝图 R7 会连它一起删；
// 在它还在的这段时间里，"落盘的是一份完整 JSON、且不留中间态"必须有测试撑着。
//
// 计时走 Close() 的确定性排干（它会 flush 后 join goroutine），不靠 sleep。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSharedMemoryPersistIsWholeDocumentAndLeavesNoTmp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "shared_memory.json")

	sm := NewSharedMemory(100)
	// ticker 间隔取一小时：这条测试要的是一次完整的退出前排干，不是周期落盘。
	if err := sm.EnablePersistence(time.Hour, path); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}
	if err := sm.SetKey("repo_memory:/tmp/proj", strings.Repeat("# 记忆正文\n", 500)); err != nil {
		t.Fatalf("SetKey: %v", err)
	}
	if err := sm.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Close 没有落盘 %s: %v", path, err)
	}
	var snap SharedMemorySnapshot
	if err := json.Unmarshal(raw, &snap); err != nil {
		t.Fatalf("快照不是合法 JSON（半份？）: %v", err)
	}
	if got := snap.KV["repo_memory:/tmp/proj"]; len(got) == 0 || !strings.HasSuffix(got, "# 记忆正文\n") {
		t.Errorf("KV 内容不完整: len=%d", len(got))
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("临时文件残留: %s", e.Name())
		}
	}
}

// Close 的注释承诺"可以重复调用、没启用持久化时也可调用"。这条承诺没有测试就等于没有：
// 修之前它是 panic（goroutine 读到被置 nil 的 ticker）+ 永久阻塞（读到 nil channel）。
func TestSharedMemoryCloseIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "shared_memory.json")

	sm := NewSharedMemory(10)
	if err := sm.Close(); err != nil {
		t.Fatalf("未启用持久化时 Close 应直接返回: %v", err)
	}

	if err := sm.EnablePersistence(time.Hour, path); err != nil {
		t.Fatalf("EnablePersistence: %v", err)
	}
	if err := sm.SetKey("k", "v"); err != nil {
		t.Fatalf("SetKey: %v", err)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 3; i++ {
			if err := sm.Close(); err != nil {
				t.Errorf("Close #%d: %v", i+1, err)
			}
		}
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("Close 卡住：persistLoop 还在 select 一个已被置 nil 的 channel")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("Close 没落盘: %v", err)
	}
	if !strings.Contains(string(raw), `"k": "v"`) {
		t.Errorf("快照内容不符: %s", raw)
	}
}
