package tools

import (
	"context"
	"fmt"

	"codeactor/internal/util"
)

// FlowControlTool 实现流程控制相关工具
type FlowControlTool struct {
	workingDir string
}

func NewFlowControlTool(workingDir string) *FlowControlTool {
	return &FlowControlTool{
		workingDir: workingDir,
	}
}

// ExecuteFinish 实现finish工具
func (t *FlowControlTool) ExecuteFinish(ctx context.Context, params map[string]interface{}) (interface{}, error) {
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