package util

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 这些断言钉住的是 deepthinking/design"按了停止还在跑"的根因：
// context.WithoutCancel 丢掉的正是 Done/Err，而 DetachDeadline 只丢 deadline。
func TestDetachDeadlineDropsDeadlineKeepsCancel(t *testing.T) {
	parent, cancelParent := context.WithTimeout(context.Background(), time.Hour)
	defer cancelParent()

	detached := DetachDeadline(parent)
	if d, ok := detached.Deadline(); ok || !d.IsZero() {
		t.Fatalf("Deadline() = (%v, %v), want 零值 false（父级 deadline 必须被丢掉）", d, ok)
	}
	if detached.Err() != nil {
		t.Errorf("派生上下文一开始就 Err = %v, want nil", detached.Err())
	}
	if detached.Done() == nil {
		t.Fatal("Done() = nil，说明取消信号也一起丢了（这正是 WithoutCancel 的病）")
	}

	cancelParent()
	select {
	case <-detached.Done():
	case <-time.After(time.Second):
		t.Fatal("父级取消后派生上下文的 Done() 没有关闭：停止键失效")
	}
	if !errors.Is(detached.Err(), context.Canceled) {
		t.Errorf("Err() = %v, want context.Canceled", detached.Err())
	}
}

// WithTimeout 建在 DetachDeadline 之上时必须照常工作：它的 propagateCancel 依赖
// parent.Done()/Err()，而这两个方法走的是嵌入字段的转发。
func TestDetachDeadlineStillPropagatesIntoWithTimeout(t *testing.T) {
	parent, cancelParent := context.WithTimeout(context.Background(), time.Hour)
	defer cancelParent()

	child, cancel := context.WithTimeout(DetachDeadline(parent), 30*time.Minute)
	defer cancel()

	// 自己那一层 30 分钟的预算必须真的挂上（否则长任务没有任何上限）。
	if d, ok := child.Deadline(); !ok {
		t.Fatal("child 没有 deadline：WithTimeout 没生效")
	} else if remain := time.Until(d); remain < 29*time.Minute || remain > 31*time.Minute {
		t.Errorf("child 剩余预算 = %v, want ≈30m", remain)
	}
	// 而父级取消仍要能穿透进来。
	cancelParent()
	select {
	case <-child.Done():
	case <-time.After(time.Second):
		t.Fatal("父级取消没有传播到 WithTimeout 的子 ctx：停止键在这种写法下失效")
	}
}

// nil parent 不许 panic：装配层/测试里传 nil 是常见形状。
func TestDetachDeadlineNilParent(t *testing.T) {
	detached := DetachDeadline(nil)
	if _, ok := detached.Deadline(); ok {
		t.Error("nil parent 的派生上下文不该有 deadline")
	}
	if detached.Done() != nil {
		t.Error("nil parent 换成 Background 后 Done() 应为 nil（永不取消）")
	}
}
