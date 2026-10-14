package director

// planner_rollout.go — Director 终止/压缩记录的构造。
//
// runState 原本是 planner_run.go 里的一个闭包。主循环那个文件按行数冻结
// （scripts/check_file_sizes.sh 的 PENDING），而 P1 要在循环顶部加暂停闸门，
// 所以把它挪到这里：它是纯状态投影，不属于循环体，放在外面也更合适。

import (
	"yagent/internal/memory"
)

// runState 投影当前步数与停止原因，供 turn_aborted / task_complete 记录使用。
// HasDelegated 一并带上：恢复侧靠它区分"什么也没干就被停住"和"干到一半被停住"。
func (p *Planner) runState(stopReason string) memory.RunState {
	return memory.RunState{
		Steps:        p.state.Step,
		MaxSteps:     p.cfg.MaxSteps,
		StopReason:   stopReason,
		HasDelegated: p.state.HasDelegated,
	}
}
