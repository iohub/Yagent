package messaging

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestBacklog_PushPop(t *testing.T) {
	b := NewBacklog(10)
	e1 := NewEvent("test", "src", "msg1")
	e2 := NewEvent("test", "src", "msg2")

	assert.NoError(t, b.Push(e1))
	assert.NoError(t, b.Push(e2))
	assert.Equal(t, 2, b.Len())

	pop1, ok := b.Pop()
	assert.True(t, ok)
	assert.Equal(t, "msg1", pop1.Content)

	pop2, ok := b.Pop()
	assert.True(t, ok)
	assert.Equal(t, "msg2", pop2.Content)

	_, ok = b.Pop()
	assert.False(t, ok)
}

func TestBacklog_Priority(t *testing.T) {
	b := NewBacklog(10)
	eLow := NewEvent("test", "src", "low")
	eLow.Priority = PriorityLow
	eHigh := NewEvent("test", "src", "high")
	eHigh.Priority = PriorityHigh

	b.Push(eLow)
	b.Push(eHigh)

	first, _ := b.Pop()
	assert.Equal(t, PriorityHigh, first.Priority) // 高优先级先出
}

func TestBacklog_Full(t *testing.T) {
	b := NewBacklog(2)
	e1 := NewEvent("test", "src", "a")
	e2 := NewEvent("test", "src", "b")
	e3 := NewEvent("test", "src", "c")

	assert.NoError(t, b.Push(e1))
	assert.NoError(t, b.Push(e2))
	assert.ErrorIs(t, b.Push(e3), ErrBacklogFull)
}

func TestBacklog_PopN(t *testing.T) {
	b := NewBacklog(10)
	for i := 0; i < 5; i++ {
		b.Push(NewEvent("test", "src", "msg"))
	}

	events := b.PopN(3)
	assert.Equal(t, 3, len(events))
	assert.Equal(t, 2, b.Len())
}

func TestBacklog_Clear(t *testing.T) {
	b := NewBacklog(10)
	b.Push(NewEvent("test", "src", "a"))
	b.Push(NewEvent("test", "src", "b"))
	b.Clear()
	assert.Equal(t, 0, b.Len())
}

func TestDeadLetterQueue_PushPeek(t *testing.T) {
	q := NewDeadLetterQueue(10)
	e := NewEvent("test", "src", "data")
	q.Push(e, assert.AnError)

	assert.Equal(t, 1, q.Len())
	entries := q.Peek()
	assert.Equal(t, 1, len(entries))
	assert.Contains(t, entries[0].Error, "assert.AnError")
}

func TestDeadLetterQueue_Pop(t *testing.T) {
	q := NewDeadLetterQueue(10)
	q.Push(NewEvent("test", "src", "a"), assert.AnError)
	q.Push(NewEvent("test", "src", "b"), assert.AnError)

	entry := q.Pop()
	assert.NotNil(t, entry)
	assert.Equal(t, 1, q.Len())
}

func TestDeadLetterQueue_MaxSize(t *testing.T) {
	q := NewDeadLetterQueue(2)
	q.Push(NewEvent("test", "src", "a"), assert.AnError)
	q.Push(NewEvent("test", "src", "b"), assert.AnError)
	q.Push(NewEvent("test", "src", "c"), assert.AnError) // 淘汰 a

	assert.Equal(t, 2, q.Len())
	entries := q.Peek()
	assert.Equal(t, "b", entries[0].Event.Content)
}

func TestDeadLetterQueue_Handler(t *testing.T) {
	called := false
	q := NewDeadLetterQueue(10)
	q.OnDeadLetter(func(entry *DeadLetterEntry) {
		called = true
	})

	q.Push(NewEvent("test", "src", "x"), assert.AnError)
	assert.True(t, called)
}

func TestBacklog_Notify(t *testing.T) {
	b := NewBacklog(10)

	// Push 后应收到通知
	b.Push(NewEvent("test", "src", "a"))

	select {
	case <-b.NotifyChan():
		// 成功收到通知
	case <-time.After(time.Second):
		t.Fatal("expected notification after Push")
	}
}
