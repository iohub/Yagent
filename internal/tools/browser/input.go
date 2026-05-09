package browser

import (
	"context"
	"fmt"
)

// InputTool 表单输入工具
type InputTool struct{}

func (t *InputTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	selector, ok := params["selector"].(string)
	if !ok || selector == "" {
		return nil, fmt.Errorf("参数 'selector' 是必需的且必须为字符串")
	}

	text, ok := params["text"].(string)
	if !ok {
		return nil, fmt.Errorf("参数 'text' 是必需的且必须为字符串")
	}

	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	timeout := getTimeout(params)

	// 等待元素
	el, err := page.Timeout(timeout).Element(selector)
	if err != nil {
		return nil, fmt.Errorf("未找到元素 '%s': %w", selector, err)
	}

	// 检查元素是否可见
	visible, err := el.Visible()
	if err != nil {
		return nil, fmt.Errorf("检查元素可见性失败: %w", err)
	}
	if !visible {
		return nil, fmt.Errorf("元素 '%s' 不可见", selector)
	}

	// 清空现有内容并输入新文本
	if err := el.Input(text); err != nil {
		return nil, fmt.Errorf("输入文本到 '%s' 失败: %w", selector, err)
	}

	return map[string]interface{}{
		"status":   "success",
		"selector": selector,
		"text":     text,
		"length":   len(text),
	}, nil
}
