package director

// stop_kind.go — Director 侧的停止原因分类。
//
// 本包不能 import yagent/internal/agents（scripts/check_no_agents_import.sh），
// 所以 executor.go 里有一份形态相同的 classifyStop。两份的**取值**来自
// memory.StopKind*（共同依赖），两份的**行为**由 agents 包的
// TestStopKindClassifiersAgree 钉在一起——放在 memory 里合成一份才是正解，
// 但那要让 memory 认识 activity 的错误类型，方向上更糟。

import (
	"context"
	"errors"

	"yagent/internal/agents/activity"
	"yagent/internal/memory"
)

// StopKindOf 把一个取消/超时错误翻译成停止原因。
// 调用点已经知道得更清楚时（熔断器、跑满步数）应当直接传常量，别用它猜。
//
// ctx 优先于 err、cause 优先于 ctx.Err()：取消路径上递过来的往往是裸
// context.Canceled，而 activity 注入的 Idle/TotalTimeoutError 只在 cause 通道上。
// 这个顺序必须与 agents.classifyStop 一致，由 TestStopKindClassifiersAgree 钉住。
func StopKindOf(err error, ctx context.Context) string {
	if ctx != nil {
		if cause := context.Cause(ctx); cause != nil && !errors.Is(cause, context.Canceled) && !errors.Is(cause, context.DeadlineExceeded) {
			err = cause
		} else if err == nil {
			err = ctx.Err()
		}
	}
	var idleErr *activity.IdleTimeoutError
	var totalErr *activity.TotalTimeoutError
	switch {
	case errors.As(err, &idleErr):
		return memory.StopKindIdleTimeout
	case errors.As(err, &totalErr):
		return memory.StopKindTotalTimeout
	case errors.Is(err, context.DeadlineExceeded):
		// 委派之外也有 deadline（单工具 120s / 单次 LLM llmTimeout）。
		// 归到 total 比归到 cancelled 有用：两者都不该原样重放同一步。
		return memory.StopKindTotalTimeout
	case errors.Is(err, context.Canceled):
		return memory.StopKindCancelled
	case err != nil:
		return memory.StopKindLLMError
	default:
		// 结束了却没有任何信号——新加了 return 路径又忘了认领原因。
		return memory.StopKindUnaccounted
	}
}
