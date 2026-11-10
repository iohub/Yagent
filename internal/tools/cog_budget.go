package tools

import (
	"context"
	"errors"
	"fmt"
	"time"

	"yagent/internal/config"
)

// CogToolBudget 由装配层（internal/app）注入，把 [tools.llm.<slot>.timeout]
// 解析成实际预算；返回 ≤0 表示未配置。用包级变量注入而非构造参数，
// 是为了让测试可以直接替换，并把解析留在装配层一处：注入的是
// (*config.Config).CogToolTimeout，显式 [tools.llm.<slot>].timeout 改值要重启；
// 只有它回落时现取的全局 [llm].timeout 是运行期可改的。
var CogToolBudget func(toolName string) time.Duration

// cogToolBudget 返回工具预算：注入的解析器优先，未装配时用兜底常量。
// 兜底直接复用 config.DefaultCogToolTimeout：internal/tools 已经依赖 internal/config
// （config 只依赖 util，不成环），另抄一个十进制的 10 分钟只会让两处漂开。
func cogToolBudget(toolName string) time.Duration {
	if CogToolBudget != nil {
		if d := CogToolBudget(toolName); d > 0 {
			return d
		}
	}
	return config.DefaultCogToolTimeout
}

// cogToolCallError 把认知类工具的 LLM 失败翻译成可诊断的错误。
//
// 为什么值得单独判：旧实现只有一句 "deepthinking LLM call failed"，于是"预算不够"
// 与"按了停止"在日志、工具回执、用户屏幕上长得一模一样 —— 前者要调 timeout，
// 后者是多跑一轮就会重复烧一次昂贵的分析。
func cogToolCallError(toolName string, budget time.Duration, err error) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%s LLM call timed out after %s budget; raise [tools.llm.%s].timeout for slow models: %w",
			toolName, budget, toolName, err)
	case errors.Is(err, context.Canceled):
		return fmt.Errorf("%s LLM call was cancelled by the caller (stop pressed or task cancelled): %w", toolName, err)
	default:
		return fmt.Errorf("%s LLM call failed: %w", toolName, err)
	}
}
