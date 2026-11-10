package llm

import (
	"context"
	"time"
)

// ApplyCallTimeout 只在 ctx 尚无 deadline 时套一层 configured 超时。
// 调用方已经给了 deadline 就原样返回：工具（如 deepthinking）的预算可能远大于
// 全局默认值，在这里二次收紧会把长任务直接砍断。
// 返回的 cancel 永远非 nil，调用方直接 defer 即可。
func ApplyCallTimeout(ctx context.Context, configured time.Duration) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	// 尊重调用方预算：只补"完全没有预算"的那种调用（一次普通 step 的推理）。
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	if configured <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, configured)
}
