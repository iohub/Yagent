package browser

import (
	"context"
	"fmt"
)

// EvaluateJSTool 执行 JavaScript 工具（高风险，需用户确认）
type EvaluateJSTool struct{}

func (t *EvaluateJSTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	code, ok := params["code"].(string)
	if !ok || code == "" {
		return nil, fmt.Errorf("参数 'code' 是必需的且必须为字符串")
	}

	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	// 在页面中执行 JavaScript
	result, err := page.Eval(code)
	if err != nil {
		return nil, fmt.Errorf("JavaScript 执行失败: %w", err)
	}

	// 安全地获取结果
	var resultStr string
	if result != nil {
		// 尝试获取原始值
		if result.UnserializableValue != "" {
			resultStr = string(result.UnserializableValue)
		} else if val := result.Value.Val(); val != nil {
			resultStr = fmt.Sprintf("%v", val)
		} else {
			resultStr = result.Description
		}
	}

	return map[string]interface{}{
		"status": "success",
		"result": resultStr,
	}, nil
}
