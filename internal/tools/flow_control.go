package tools

import (
	"context"
	"fmt"

	"codeactor/internal/util"
)

// FlowControlTool 实现流程控制相关工具
type FlowControlTool struct {
	workingDir     string
	UserConfirmMgr *UserConfirmManager
}

func NewFlowControlTool(workingDir string) *FlowControlTool {
	return &FlowControlTool{
		workingDir: workingDir,
	}
}

// ExecuteAgentExit 实现agent_exit工具
func (t *FlowControlTool) ExecuteAgentExit(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	reason, ok := params["reason"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("reason parameter must be a string"), "executeFinish")
	}
	return map[string]interface{}{
		"finished": true,
		"reason":   reason,
	}, nil
}

// ExecuteAskUserForHelp 实现ask_user_for_help工具
// 当 UserConfirmMgr 可用时，会发布 user_help_needed 事件并阻塞等待用户响应。
// 当 UserConfirmMgr 不可用时，降级为返回请求参数（兼容旧模式）。
func (t *FlowControlTool) ExecuteAskUserForHelp(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	reason, ok := params["reason"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("reason parameter must be a string"), "executeAskUserForHelp")
	}

	specificQuestion, ok := params["specific_question"].(string)
	if !ok {
		return nil, util.WrapError(ctx, fmt.Errorf("specific_question parameter must be a string"), "executeAskUserForHelp")
	}

	suggestedOptions, _ := params["suggested_options"].(string)

	// 如果 UserConfirmMgr 可用，进入交互模式：等待用户实际响应
	if t.UserConfirmMgr != nil {
		userResponse, err := t.UserConfirmMgr.RequestConfirmation(ctx, specificQuestion, suggestedOptions)
		if err != nil {
			return nil, util.WrapError(ctx, fmt.Errorf("user confirmation failed: %w", err), "executeAskUserForHelp")
		}

		return map[string]interface{}{
			"user_help_requested": true,
			"reason":              reason,
			"specific_question":   specificQuestion,
			"user_response":       userResponse,
		}, nil
	}

	// 降级模式：返回请求参数（无交互）
	result := map[string]interface{}{
		"user_help_requested": true,
		"reason":              reason,
		"specific_question":   specificQuestion,
	}

	if suggestedOptions != "" {
		result["suggested_options"] = suggestedOptions
	}

	return result, nil
}