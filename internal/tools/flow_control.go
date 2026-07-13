package tools

import (
	"context"
	"fmt"
	"strings"

	"codeactor/internal/protocol"
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

// ExecuteAskUserForHelp implements the ask_user_for_help tool
// 支持三种交互模式：confirm（确认）、select（选择）、input（输入）
// 交互模式可通过 interaction_type 参数显式指定，或根据 suggested_options 自动推断
func (t *FlowControlTool) ExecuteAskUserForHelp(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	reason, _ := params["reason"].(string)
	if reason == "" {
		return nil, util.WrapError(ctx, fmt.Errorf("reason parameter is required"), "executeAskUserForHelp")
	}

	specificQuestion, _ := params["specific_question"].(string)
	if specificQuestion == "" {
		return nil, util.WrapError(ctx, fmt.Errorf("specific_question parameter is required"), "executeAskUserForHelp")
	}

	// 解析选项 —— 兼容 string 和 []interface{} 两种格式
	var options []string
	if opts, ok := params["suggested_options"].([]interface{}); ok {
		for _, opt := range opts {
			if s, ok := opt.(string); ok {
				options = append(options, s)
			}
		}
	} else if opts, ok := params["suggested_options"].(string); ok && opts != "" {
		// 旧格式：用 / 分隔的字符串
		for _, opt := range strings.Split(opts, "/") {
			opt = strings.TrimSpace(opt)
			if opt != "" {
				options = append(options, opt)
			}
		}
	}

	// 解析交互类型
	interactionType := protocol.InferInteractionType(options)
	if itStr, ok := params["interaction_type"].(string); ok && itStr != "" {
		switch protocol.InteractionType(itStr) {
		case protocol.InteractionConfirm, protocol.InteractionSelect, protocol.InteractionInput:
			interactionType = protocol.InteractionType(itStr)
		}
	}

	// 解析可选参数
	defaultValue, _ := params["default_value"].(string)
	placeholder, _ := params["placeholder"].(string)
	allowCustom := true // 默认允许
	if ac, ok := params["allow_custom"].(bool); ok {
		allowCustom = ac
	}

	// 如果 UserConfirmMgr 可用，进入交互模式
	if t.UserConfirmMgr != nil {
		helpData := &protocol.UserHelpNeededData{
			Question:        specificQuestion,
			Context:         reason,
			InteractionType: interactionType,
			Options:         options,
			DefaultValue:    defaultValue,
			Placeholder:     placeholder,
			AllowCustom:     allowCustom,
		}

		userResponse, err := t.UserConfirmMgr.RequestUserHelp(ctx, helpData)
		if err != nil {
			return nil, util.WrapError(ctx, fmt.Errorf("user help failed: %w", err), "executeAskUserForHelp")
		}

		result := map[string]interface{}{
			"user_help_requested": true,
			"reason":              reason,
			"specific_question":   specificQuestion,
			"user_response":       userResponse.Response,
		}

		// 如果取消，添加标记
		if userResponse.Cancelled {
			result["cancelled"] = true
		}

		return result, nil
	}

	// 降级模式：返回请求参数（无交互）
	result := map[string]interface{}{
		"user_help_requested": true,
		"reason":              reason,
		"specific_question":   specificQuestion,
	}

	if len(options) > 0 {
		result["suggested_options"] = options
	}

	return result, nil
}