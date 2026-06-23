package messaging

import (
	"container/heap"
	"fmt"
	"sync"
	"time"
)

// ErrBacklogFull backlog 已满错误
var ErrBacklogFull = fmt.Errorf("backlog is full")

// BacklogEntry 包含事件及其入队时间
type BacklogEntry struct {
	Event      *Event
	EnqueuedAt time.Time
	Priority   Priority
}

// backlogHeap 实现 heap.Interface 用于优先级排序
type backlogHeap []*BacklogEntry

func (h backlogHeap) Len() int { return len(h) }
func (h backlogHeap) Less(i, j int) bool {
	// 高优先级在前；同优先级按时间顺序（FIFO）
	if h[i].Priority != h[j].Priority {
		return h[i].Priority > h[j].Priority
	}
	return h[i].EnqueuedAt.Before(h[j].EnqueuedAt)
}
func (h backlogHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *backlogHeap) Push(x interface{}) { *h = append(*h, x.(*BacklogEntry)) }
func (h *backlogHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	old[n-1] = nil // 避免内存泄漏
	*h = old[:n-1]
	return item
}

// Backlog 优先级溢出队列
type Backlog struct {
	heap    backlogHeap
	maxSize int
	mu      sync.Mutex
	notify  chan struct{} // 有事件可消费时通知
}

// NewBacklog 创建 backlog，size=0 表示无限制
func NewBacklog(size int) *Backlog {
	return &Backlog{
		heap:    make(backlogHeap, 0),
		maxSize: size,
		notify:  make(chan struct{}, 1),
	}
}

// Push 将事件加入 backlog
func (b *Backlog) Push(event *Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.maxSize > 0 && b.heap.Len() >= b.maxSize {
		return ErrBacklogFull
	}

	heap.Push(&b.heap, &BacklogEntry{
		Event:      event,
		EnqueuedAt: time.Now(),
		Priority:   event.Priority,
	})

	// 非阻塞通知
	select {
	case b.notify <- struct{}{}:
	default:
	}

	return nil
}

// Pop 弹出最高优先级的事件
func (b *Backlog) Pop() (*Event, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.heap.Len() == 0 {
		return nil, false
	}

	entry := heap.Pop(&b.heap).(*BacklogEntry)
	return entry.Event, true
}

// PopN 批量弹出最多 n 个事件
func (b *Backlog) PopN(n int) []*Event {
	b.mu.Lock()
	defer b.mu.Unlock()

	count := n
	if count > b.heap.Len() {
		count = b.heap.Len()
	}

	events := make([]*Event, 0, count)
	for i := 0; i < count; i++ {
		entry := heap.Pop(&b.heap).(*BacklogEntry)
		events = append(events, entry.Event)
	}
	return events
}

// Len 返回 backlog 中的事件数
func (b *Backlog) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.heap.Len()
}

// NotifyChan 返回通知 channel
func (b *Backlog) NotifyChan() <-chan struct{} {
	return b.notify
}

// Clear 清空 backlog
func (b *Backlog) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.heap = make(backlogHeap, 0)
}
