package messaging

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DeadLetterEntry 死信记录
type DeadLetterEntry struct {
	Event    *Event    `json:"event"`
	Error    string    `json:"error"`
	FailedAt time.Time `json:"failed_at"`
	Attempts int       `json:"attempts"`
	AgentID  string    `json:"agent_id,omitempty"`
}

// DeadLetterHandler 死信处理回调
type DeadLetterHandler func(entry *DeadLetterEntry)

// DeadLetterQueue 死信队列
type DeadLetterQueue struct {
	entries       []*DeadLetterEntry
	maxSize       int
	mu            sync.RWMutex
	handlers      []DeadLetterHandler
	persistPath   string // 持久化路径（空=不持久化）
}

// NewDeadLetterQueue 创建死信队列
// size 为 0 表示不限制大小
func NewDeadLetterQueue(size int) *DeadLetterQueue {
	return &DeadLetterQueue{
		entries:  make([]*DeadLetterEntry, 0),
		maxSize:  size,
		handlers: make([]DeadLetterHandler, 0),
	}
}

// NewPersistentDeadLetterQueue 创建持久化的死信队列
func NewPersistentDeadLetterQueue(size int, dir string) *DeadLetterQueue {
	q := NewDeadLetterQueue(size)
	q.persistPath = filepath.Join(dir, "dead_letter.json")

	// 启动时尝试加载已持久化的死信
	if err := q.load(); err != nil {
		slog.Warn("Failed to load persisted DLQ entries", "path", q.persistPath, "err", err)
	}

	return q
}

// Push 添加死信记录
func (q *DeadLetterQueue) Push(event *Event, err error) {
	entry := &DeadLetterEntry{
		Event:    event,
		Error:    err.Error(),
		FailedAt: time.Now(),
		Attempts: event.RetryCount,
	}

	q.mu.Lock()
	if q.maxSize > 0 && len(q.entries) >= q.maxSize {
		// 移除最旧的记录（FIFO 淘汰）
		q.entries = q.entries[1:]
	}
	q.entries = append(q.entries, entry)
	q.mu.Unlock()

	// 触发回调
	for _, handler := range q.handlers {
		handler(entry)
	}

	// 持久化
	if q.persistPath != "" {
		q.persist()
	}

	// 日志告警
	slog.Warn("Event moved to dead letter queue",
		"event_id", event.ID,
		"event_type", event.Type,
		"error", err,
		"retry_count", event.RetryCount,
	)
}

// Pop 弹出最旧的死信
func (q *DeadLetterQueue) Pop() *DeadLetterEntry {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.entries) == 0 {
		return nil
	}
	entry := q.entries[0]
	q.entries = q.entries[1:]
	return entry
}

// PopN 批量弹出最多 n 个死信
func (q *DeadLetterQueue) PopN(n int) []*DeadLetterEntry {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.entries) == 0 {
		return nil
	}
	count := n
	if count > len(q.entries) {
		count = len(q.entries)
	}
	entries := make([]*DeadLetterEntry, count)
	copy(entries, q.entries[:count])
	q.entries = q.entries[count:]
	return entries
}

// Peek 查看所有死信（不弹出）
func (q *DeadLetterQueue) Peek() []*DeadLetterEntry {
	q.mu.RLock()
	defer q.mu.RUnlock()
	result := make([]*DeadLetterEntry, len(q.entries))
	copy(result, q.entries)
	return result
}

// Len 返回死信数量
func (q *DeadLetterQueue) Len() int {
	q.mu.RLock()
	defer q.mu.RUnlock()
	return len(q.entries)
}

// Clear 清空死信队列
func (q *DeadLetterQueue) Clear() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.entries = make([]*DeadLetterEntry, 0)
}

// OnDeadLetter 注册死信回调
func (q *DeadLetterQueue) OnDeadLetter(handler DeadLetterHandler) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.handlers = append(q.handlers, handler)
}

// persist 持久化死信到文件
func (q *DeadLetterQueue) persist() {
	data, err := json.MarshalIndent(q.entries, "", "  ")
	if err != nil {
		slog.Error("Failed to marshal DLQ entries", "err", err)
		return
	}
	dir := filepath.Dir(q.persistPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		slog.Error("Failed to create DLQ dir", "err", err)
		return
	}
	tmpPath := q.persistPath + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		slog.Error("Failed to write DLQ temp file", "err", err)
		return
	}
	if err := os.Rename(tmpPath, q.persistPath); err != nil {
		slog.Error("Failed to rename DLQ file", "err", err)
	}
}

// load 从文件加载死信
func (q *DeadLetterQueue) load() error {
	data, err := os.ReadFile(q.persistPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal(data, &q.entries)
}

// ReplayDeadLetters 将死信重新投递到 dispatcher
func (q *DeadLetterQueue) ReplayDeadLetters(dispatcher *MessageDispatcher) int {
	q.mu.Lock()
	entries := q.entries
	q.entries = make([]*DeadLetterEntry, 0)
	q.mu.Unlock()

	replayed := 0
	for _, entry := range entries {
		entry.Event.RetryCount = 0 // 重置重试计数
		if err := dispatcher.Publish(entry.Event); err == nil {
			replayed++
		} else {
			q.Push(entry.Event, err)
		}
	}
	return replayed
}
