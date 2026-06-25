package messaging

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

// createTempWAL 创建一个临时的 FileWAL 实例用于测试
func createTempWAL(t *testing.T) (*FileWAL, string) {
	t.Helper()
	dir := t.TempDir()
	walPath := filepath.Join(dir, "wal.log")

	wal, err := NewFileWAL(WALOptions{
		FilePath:    walPath,
		SyncOnWrite: true,
	})
	if err != nil {
		t.Fatalf("failed to create WAL: %v", err)
	}
	return wal, walPath
}

// makeTestEvent 创建一个测试用的 Event
func makeTestEvent(id string, content string) *Event {
	return &Event{
		ID:        id,
		Type:      EventTypeInfo,
		Source:    "test-source",
		Content:   content,
		Priority:  PriorityNormal,
		Timestamp: time.Now(),
		Metadata:  map[string]interface{}{"test": true},
	}
}

// ---------------------------------------------------------------------------
// 测试用例：TestFileWAL_AppendAndReplay
// 描述：写入3个事件 → 回放验证数据一致
// ---------------------------------------------------------------------------

func TestFileWAL_AppendAndReplay(t *testing.T) {
	wal, _ := createTempWAL(t)
	defer wal.Close()

	// 写入3个事件
	events := []*Event{
		makeTestEvent("event-1", "content-1"),
		makeTestEvent("event-2", "content-2"),
		makeTestEvent("event-3", "content-3"),
	}

	for _, e := range events {
		if err := wal.Append(e); err != nil {
			t.Fatalf("failed to append event: %v", err)
		}
	}

	// 验证序列号
	if got := wal.LastSeqNum(); got != 3 {
		t.Errorf("LastSeqNum = %d, want 3", got)
	}

	// 回放所有事件
	var replayed []*Event
	err := wal.Replay(context.Background(), 1, func(e *Event) error {
		replayed = append(replayed, e.Clone())
		return nil
	})
	if err != nil {
		t.Fatalf("failed to replay: %v", err)
	}

	// 验证回放结果
	if len(replayed) != 3 {
		t.Fatalf("replayed %d events, want 3", len(replayed))
	}

	for i, want := range events {
		got := replayed[i]
		if got.ID != want.ID {
			t.Errorf("event[%d].ID = %q, want %q", i, got.ID, want.ID)
		}
		if got.Type != want.Type {
			t.Errorf("event[%d].Type = %q, want %q", i, got.Type, want.Type)
		}
		if got.SeqNum != uint64(i+1) {
			t.Errorf("event[%d].SeqNum = %d, want %d", i, got.SeqNum, i+1)
		}
	}

	// 验证持久化：重新打开 WAL 也能读到数据
	wal2, err := NewFileWAL(WALOptions{
		FilePath: wal.path,
	})
	if err != nil {
		t.Fatalf("failed to reopen WAL: %v", err)
	}
	defer wal2.Close()

	if got := wal2.LastSeqNum(); got != 3 {
		t.Errorf("reopened WAL LastSeqNum = %d, want 3", got)
	}
}

// ---------------------------------------------------------------------------
// 测试用例：TestFileWAL_AppendAndReplay_AfterTruncate
// 描述：截断后回放只保留新事件
// ---------------------------------------------------------------------------

func TestFileWAL_AppendAndReplay_AfterTruncate(t *testing.T) {
	wal, _ := createTempWAL(t)
	defer wal.Close()

	// 写入5个事件
	for i := 1; i <= 5; i++ {
		e := makeTestEvent(eventID(i), "content-1")
		if err := wal.Append(e); err != nil {
			t.Fatalf("failed to append event %d: %v", i, err)
		}
	}

	// 截断前3个
	if err := wal.Truncate(3); err != nil {
		t.Fatalf("failed to truncate: %v", err)
	}

	// 验证序列号已重置
	if got := wal.LastSeqNum(); got != 0 {
		t.Errorf("LastSeqNum after truncate = %d, want 0", got)
	}

	// 追加2个新事件
	newEvents := []*Event{
		makeTestEvent("new-1", "new-content-1"),
		makeTestEvent("new-2", "new-content-2"),
	}
	for _, e := range newEvents {
		if err := wal.Append(e); err != nil {
			t.Fatalf("failed to append new event: %v", err)
		}
	}

	// 验证序列号
	if got := wal.LastSeqNum(); got != 2 {
		t.Errorf("LastSeqNum after new appends = %d, want 2", got)
	}

	// 回放验证只有新事件
	var replayed []*Event
	err := wal.Replay(context.Background(), 1, func(e *Event) error {
		replayed = append(replayed, e.Clone())
		return nil
	})
	if err != nil {
		t.Fatalf("failed to replay: %v", err)
	}

	if len(replayed) != 2 {
		t.Fatalf("replayed %d events, want 2", len(replayed))
	}

	if replayed[0].ID != "new-1" {
		t.Errorf("first replayed event ID = %q, want 'new-1'", replayed[0].ID)
	}
	if replayed[1].ID != "new-2" {
		t.Errorf("second replayed event ID = %q, want 'new-2'", replayed[1].ID)
	}
}

// ---------------------------------------------------------------------------
// 测试用例：TestFileWAL_RecoverFromCrash
// 描述：模拟崩溃后重启（先写 WAL 不 sync，然后重新打开验证持久化）
// ---------------------------------------------------------------------------

func TestFileWAL_RecoverFromCrash(t *testing.T) {
	dir := t.TempDir()
	walPath := filepath.Join(dir, "wal.log")

	// 创建 WAL，禁用 sync 模拟崩溃场景
	wal1, err := NewFileWAL(WALOptions{
		FilePath:    walPath,
		SyncOnWrite: false, // 禁用 fsync
	})
	if err != nil {
		t.Fatalf("failed to create WAL: %v", err)
	}

	// 写入事件
	events := []*Event{
		makeTestEvent("crash-1", "content-1"),
		makeTestEvent("crash-2", "content-2"),
		makeTestEvent("crash-3", "content-3"),
	}
	for _, e := range events {
		if err := wal1.Append(e); err != nil {
			t.Fatalf("failed to append event: %v", err)
		}
	}

	// 不调用 Sync，直接 Close 模拟崩溃
	wal1.Close()

	// "重启"：重新打开 WAL
	wal2, err := NewFileWAL(WALOptions{
		FilePath: walPath,
	})
	if err != nil {
		t.Fatalf("failed to reopen WAL after crash: %v", err)
	}
	defer wal2.Close()

	// 验证序列号已恢复
	if got := wal2.LastSeqNum(); got != 3 {
		t.Errorf("recovered LastSeqNum = %d, want 3", got)
	}

	// 回放验证数据完整
	var replayed []*Event
	err = wal2.Replay(context.Background(), 1, func(e *Event) error {
		replayed = append(replayed, e.Clone())
		return nil
	})
	if err != nil {
		t.Fatalf("failed to replay after recovery: %v", err)
	}

	if len(replayed) != 3 {
		t.Fatalf("replayed %d events after recovery, want 3", len(replayed))
	}

	for i, want := range events {
		if replayed[i].ID != want.ID {
			t.Errorf("recovered event[%d].ID = %q, want %q", i, replayed[i].ID, want.ID)
		}
	}
}

// ---------------------------------------------------------------------------
// 测试用例：TestFileWAL_NoopWAL
// 描述：空实现不报错
// ---------------------------------------------------------------------------

func TestFileWAL_NoopWAL(t *testing.T) {
	wal := &NoopWAL{}

	// Append 不应报错
	e := makeTestEvent("noop-1", "test")
	if err := wal.Append(e); err != nil {
		t.Errorf("NoopWAL.Append() error = %v", err)
	}

	// Replay 不应报错
	if err := wal.Replay(context.Background(), 1, func(e *Event) error {
		return nil
	}); err != nil {
		t.Errorf("NoopWAL.Replay() error = %v", err)
	}

	// Truncate 不应报错
	if err := wal.Truncate(10); err != nil {
		t.Errorf("NoopWAL.Truncate() error = %v", err)
	}

	// Sync 不应报错
	if err := wal.Sync(); err != nil {
		t.Errorf("NoopWAL.Sync() error = %v", err)
	}

	// LastSeqNum 应返回 0
	if got := wal.LastSeqNum(); got != 0 {
		t.Errorf("NoopWAL.LastSeqNum() = %d, want 0", got)
	}

	// Close 不应报错
	if err := wal.Close(); err != nil {
		t.Errorf("NoopWAL.Close() error = %v", err)
	}

	// 重复 Close 不应报错
	if err := wal.Close(); err != nil {
		t.Errorf("NoopWAL.Close() again error = %v", err)
	}
}

// ---------------------------------------------------------------------------
// 测试用例：TestFileWAL_ReplayFromSeqNum
// 描述：从指定序列号开始回放（过滤前面事件）
// ---------------------------------------------------------------------------

func TestFileWAL_ReplayFromSeqNum(t *testing.T) {
	wal, _ := createTempWAL(t)
	defer wal.Close()

	// 写入5个事件
	for i := 1; i <= 5; i++ {
		e := makeTestEvent(eventID(i), "content-1")
		if err := wal.Append(e); err != nil {
			t.Fatalf("failed to append event: %v", err)
		}
	}

	// 从 seq=3 开始回放
	var replayed []*Event
	err := wal.Replay(context.Background(), 3, func(e *Event) error {
		replayed = append(replayed, e.Clone())
		return nil
	})
	if err != nil {
		t.Fatalf("failed to replay: %v", err)
	}

	// 应该只有3个事件（seq 3, 4, 5）
	if len(replayed) != 3 {
		t.Fatalf("replayed %d events, want 3", len(replayed))
	}

	if replayed[0].SeqNum != 3 {
		t.Errorf("first replayed SeqNum = %d, want 3", replayed[0].SeqNum)
	}
	if replayed[2].SeqNum != 5 {
		t.Errorf("last replayed SeqNum = %d, want 5", replayed[2].SeqNum)
	}
}

// ---------------------------------------------------------------------------
// 测试用例：TestFileWAL_ContextCancellation
// 描述：Replay 时 context 取消应提前终止
// ---------------------------------------------------------------------------

func TestFileWAL_ContextCancellation(t *testing.T) {
	wal, _ := createTempWAL(t)
	defer wal.Close()

	// 写入大量事件
	for i := 1; i <= 100; i++ {
		e := makeTestEvent(eventID(i), "content-1")
		if err := wal.Append(e); err != nil {
			t.Fatalf("failed to append event: %v", err)
		}
	}

	// 创建可取消的 context
	ctx, cancel := context.WithCancel(context.Background())

	// 在回调中立即取消
	var replayed []*Event
	err := wal.Replay(ctx, 1, func(e *Event) error {
		replayed = append(replayed, e)
		cancel() // 立即取消
		return nil
	})

	// 应该返回 context.Canceled
	if err != context.Canceled {
		t.Errorf("Replay error = %v, want context.Canceled", err)
	}

	// 只应该收到1个事件（第一个）
	if len(replayed) != 1 {
		t.Errorf("replayed %d events, want 1", len(replayed))
	}
}

// ---------------------------------------------------------------------------
// 测试用例：TestFileWAL_CloseTwice
// 描述：重复 Close 不应报错
// ---------------------------------------------------------------------------

func TestFileWAL_CloseTwice(t *testing.T) {
	wal, _ := createTempWAL(t)

	// 第一次 Close
	if err := wal.Close(); err != nil {
		t.Errorf("Close() first time error = %v", err)
	}

	// 第二次 Close 不应报错
	if err := wal.Close(); err != nil {
		t.Errorf("Close() second time error = %v", err)
	}
}

// ---------------------------------------------------------------------------
// 测试用例：TestFileWAL_AppendAfterClose
// 描述：关闭后 Append 应报错
// ---------------------------------------------------------------------------

func TestFileWAL_AppendAfterClose(t *testing.T) {
	wal, _ := createTempWAL(t)
	wal.Close()

	e := makeTestEvent("after-close", "test")
	err := wal.Append(e)
	if err == nil {
		t.Error("Append after Close() should return error")
	}
}

// ---------------------------------------------------------------------------
// 测试用例：TestFileWAL_MaxFileSize_Rotate
// 描述：文件超过阈值时触发轮转
// ---------------------------------------------------------------------------

func TestFileWAL_MaxFileSize_Rotate(t *testing.T) {
	dir := t.TempDir()
	walPath := filepath.Join(dir, "wal.log")

	// 设置很小的最大文件大小（100字节）
	wal, err := NewFileWAL(WALOptions{
		FilePath:    walPath,
		SyncOnWrite: true,
		MaxFileSize: 100,
	})
	if err != nil {
		t.Fatalf("failed to create WAL: %v", err)
	}
	defer wal.Close()

	// 写入足够多的事件触发轮转
	for i := 1; i <= 20; i++ {
		e := makeTestEvent(eventID(i), "x") // 小内容
		if err := wal.Append(e); err != nil {
			t.Fatalf("failed to append event: %v", err)
		}
	}

	// 轮转后最后一个事件的序列号应为 1（最后一次 Append 先 rotate 再 lastSeq++）
	if got := wal.LastSeqNum(); got != 1 {
		t.Errorf("LastSeqNum after rotate = %d, want 1", got)
	}

	// 验证原文件存在备份
	backupPath := walPath + ".1"
	if _, err := os.Stat(backupPath); os.IsNotExist(err) {
		t.Errorf("backup file %q does not exist after rotation", backupPath)
	}
}

// ---------------------------------------------------------------------------
// 测试用例：TestFileWAL_Sync
// 描述：Sync 应成功执行
// ---------------------------------------------------------------------------

func TestFileWAL_Sync(t *testing.T) {
	wal, _ := createTempWAL(t)
	defer wal.Close()

	e := makeTestEvent("sync-test", "test")
	if err := wal.Append(e); err != nil {
		t.Fatalf("failed to append event: %v", err)
	}

	// Sync 不应报错
	if err := wal.Sync(); err != nil {
		t.Errorf("Sync() error = %v", err)
	}
}

// ---------------------------------------------------------------------------
// 测试用例：TestFileWAL_ConcurrentAppend
// 描述：并发 Append 不应导致数据竞争
// ---------------------------------------------------------------------------

func TestFileWAL_ConcurrentAppend(t *testing.T) {
	wal, _ := createTempWAL(t)
	defer wal.Close()

	const goroutines = 10
	const eventsPerGoroutine = 50

	var wg sync.WaitGroup
	var mu sync.Mutex
	var allEvents []*Event

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gid int) {
			defer wg.Done()
			for i := 0; i < eventsPerGoroutine; i++ {
				e := makeTestEvent(eventID(gid*1000+i), "content")
				if err := wal.Append(e); err != nil {
					t.Errorf("goroutine %d append error: %v", gid, err)
					return
				}
				mu.Lock()
				allEvents = append(allEvents, e)
				mu.Unlock()
			}
		}(g)
	}

	wg.Wait()

	// 验证总事件数
	totalExpected := goroutines * eventsPerGoroutine
	if len(allEvents) != totalExpected {
		t.Errorf("appended %d events, want %d", len(allEvents), totalExpected)
	}

	// 验证序列号唯一性
	seqNums := make(map[uint64]bool)
	for _, e := range allEvents {
		if seqNums[e.SeqNum] {
			t.Errorf("duplicate SeqNum: %d", e.SeqNum)
		}
		seqNums[e.SeqNum] = true
	}

	// 验证所有事件都可以通过回放获取
	var replayCount int
	wal.Replay(context.Background(), 1, func(e *Event) error {
		replayCount++
		return nil
	})
	if replayCount != totalExpected {
		t.Errorf("replayed %d events, want %d", replayCount, totalExpected)
	}
}

// ---------------------------------------------------------------------------
// 辅助函数
// ---------------------------------------------------------------------------

var idCounter uint64 = 0

func eventID(n int) string {
	return fmt.Sprintf("test-event-%d", n)
}
