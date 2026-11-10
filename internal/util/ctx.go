package util

import (
	"context"
	"time"
)

// detachedDeadline 继承 parent 的取消信号与 Value，只把 deadline 换成零值。
type detachedDeadline struct{ context.Context }

// Deadline 恒为零值：本类型的全部意义就是让派生上下文不继承父级 deadline。
// Done/Err/Value 直接沿嵌入的 parent —— 取消信号必须继续传播。
func (detachedDeadline) Deadline() (time.Time, bool) { return time.Time{}, false }

// DetachDeadline 返回继承 parent 取消信号、但丢弃其 deadline 的派生上下文。
//
// 用途：长耗时子任务需要自己的预算，同时必须能被调用方打断。
// context.WithoutCancel 会把取消信号一起丢掉（Done() 返回 nil），
// 用它派生的子任务既躲开父级 deadline，也躲开停止键——这正是
// deepthinking/design 曾经"按了停止还在跑"的根因。
//
// 之后套 context.WithTimeout(DetachDeadline(p), d) 是安全的：WithTimeout 的
// propagateCancel 认 parent.Done()/Err()，而这两个方法原样转发给 p。
func DetachDeadline(parent context.Context) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	return detachedDeadline{parent}
}
