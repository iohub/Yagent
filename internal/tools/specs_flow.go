package tools

import (
	"yagent/internal/toolspec"
)

// 流程控制族。两者都必须 Hot 且串行：
//   - agent_exit 并发的话，同一批次里后续调用会落在已终止的循环上；
//   - ask_user_for_help 阻塞等人响应，并发等于同时问两个问题。
//
// BlocksOnUser 同时是"豁免 180s 工具超时"的判据（替代历史 isInteractiveUserTool）。
func init() {
	toolspec.MustRegister(toolspec.Spec{
		Name:      "agent_exit",
		Effect:    toolspec.EffectFlow,
		Frequency: toolspec.Hot,
		Serial:    true,
		Brief:     "结束本次任务并给出理由",
		Available: func(d *toolspec.Deps) (bool, string) { return d.Flow != nil, "流程工具未装配" },
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Flow, "Flow"); err != nil {
				return nil, err
			}
			return d.Flow.ExecuteAgentExit, nil
		},
	})

	toolspec.MustRegister(toolspec.Spec{
		Name:         "ask_user_for_help",
		Effect:       toolspec.EffectFlow,
		Frequency:    toolspec.Hot,
		Serial:       true,
		BlocksOnUser: true,
		Brief:        "反问用户：确认/选择/自由输入",
		// --full-yolo 下不注册：历史由各 Agent 的 switch 各自判断，现在收归一处。
		Available: func(d *toolspec.Deps) (bool, string) {
			if d.FullYolo {
				return false, "FULL-YOLO 模式已移除 ask_user_for_help"
			}
			return d.Flow != nil, "流程工具未装配"
		},
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Flow, "Flow"); err != nil {
				return nil, err
			}
			return d.Flow.ExecuteAskUserForHelp, nil
		},
	})
}
