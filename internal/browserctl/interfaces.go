package browserctl

import "context"

// Runner 是一次浏览器委派独占的工作区。由 *Session 实现。
// 抽象出来是为了让工具层能在不启动浏览器的情况下测试参数映射与失败整形。
type Runner interface {
	Run(ctx context.Context, command string, args ...string) (*Result, error)
	Dir() string
	Release()
}

// Controller 是浏览器执行层的入口。由 *Manager 实现。
type Controller interface {
	Probe() (bool, string)
	Acquire(ctx context.Context, delegationID string) (Runner, error)
}
