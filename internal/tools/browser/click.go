package browser

import (
	"context"
	"fmt"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	"github.com/go-rod/rod/lib/proto"
)

// ClickTool 元素点击工具
type ClickTool struct{}

func (t *ClickTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	selector, ok := params["selector"].(string)
	if !ok || selector == "" {
		return nil, fmt.Errorf("参数 'selector' 是必需的且必须为字符串")
	}

	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	timeout := getTimeout(params)

	// 等待元素可见
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

	// 点击
	button := "left"
	if b, ok := params["button"].(string); ok && b != "" {
		button = b
	}

	var mouseButton proto.InputMouseButton
	var clickCount int

	switch button {
	case "left":
		mouseButton = proto.InputMouseButtonLeft
		clickCount = 1
	case "right":
		mouseButton = proto.InputMouseButtonRight
		clickCount = 1
	case "middle":
		mouseButton = proto.InputMouseButtonMiddle
		clickCount = 1
	default:
		return nil, fmt.Errorf("不支持的鼠标按钮: %s (支持: left, right, middle)", button)
	}

	// 检查双击
	if clickCountVal, ok := params["clickCount"].(float64); ok && clickCountVal > 0 {
		clickCount = int(clickCountVal)
	}

	if err := el.Click(mouseButton, clickCount); err != nil {
		return nil, fmt.Errorf("点击元素 '%s' 失败: %w", selector, err)
	}

	return map[string]interface{}{
		"status":     "success",
		"selector":   selector,
		"button":     button,
		"clickCount": clickCount,
	}, nil
}

// DoubleClickTool 双击工具
type DoubleClickTool struct{}

func (t *DoubleClickTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	selector, ok := params["selector"].(string)
	if !ok || selector == "" {
		return nil, fmt.Errorf("参数 'selector' 是必需的且必须为字符串")
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

	if err := el.Click(proto.InputMouseButtonLeft, 2); err != nil {
		return nil, fmt.Errorf("双击元素 '%s' 失败: %w", selector, err)
	}

	return map[string]interface{}{
		"status":     "success",
		"selector":   selector,
		"clickCount": 2,
	}, nil
}

// RightClickTool 右键点击工具
type RightClickTool struct{}

func (t *RightClickTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	selector, ok := params["selector"].(string)
	if !ok || selector == "" {
		return nil, fmt.Errorf("参数 'selector' 是必需的且必须为字符串")
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

	if err := el.Click(proto.InputMouseButtonRight, 1); err != nil {
		return nil, fmt.Errorf("右键点击元素 '%s' 失败: %w", selector, err)
	}

	return map[string]interface{}{
		"status":   "success",
		"selector": selector,
		"button":   "right",
	}, nil
}

// HoverTool 鼠标悬停工具
type HoverTool struct{}

func (t *HoverTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	selector, ok := params["selector"].(string)
	if !ok || selector == "" {
		return nil, fmt.Errorf("参数 'selector' 是必需的且必须为字符串")
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

	if err := el.Hover(); err != nil {
		return nil, fmt.Errorf("悬停到元素 '%s' 失败: %w", selector, err)
	}

	return map[string]interface{}{
		"status":   "success",
		"selector": selector,
	}, nil
}

// ClearTool 清空输入框工具
type ClearTool struct{}

func (t *ClearTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	selector, ok := params["selector"].(string)
	if !ok || selector == "" {
		return nil, fmt.Errorf("参数 'selector' 是必需的且必须为字符串")
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

	if err := el.Input(""); err != nil {
		return nil, fmt.Errorf("清空元素 '%s' 失败: %w", selector, err)
	}

	return map[string]interface{}{
		"status":   "success",
		"selector": selector,
	}, nil
}

// FocusTool 聚焦元素工具
type FocusTool struct{}

func (t *FocusTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	selector, ok := params["selector"].(string)
	if !ok || selector == "" {
		return nil, fmt.Errorf("参数 'selector' 是必需的且必须为字符串")
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

	if err := el.Focus(); err != nil {
		return nil, fmt.Errorf("聚焦元素 '%s' 失败: %w", selector, err)
	}

	return map[string]interface{}{
		"status":   "success",
		"selector": selector,
	}, nil
}

// PressKeyTool 按键工具
type PressKeyTool struct{}

func (PressKeyTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	keyStr, ok := params["key"].(string)
	if !ok || keyStr == "" {
		return nil, fmt.Errorf("参数 'key' 是必需的且必须为字符串")
	}

	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	// 创建按键操作 - 将字符串转换为 input.Key (rune)
	ka := page.KeyActions()
	if len(keyStr) > 0 {
		ka.Type(input.Key(keyStr[0]))
	}

	if err := ka.Do(); err != nil {
		return nil, fmt.Errorf("按键 '%s' 失败: %w", keyStr, err)
	}

	return map[string]interface{}{
		"status": "success",
		"key":    keyStr,
	}, nil
}

// GetTextTool 获取元素文本工具
type GetTextTool struct{}

func (t *GetTextTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	selector, ok := params["selector"].(string)
	if !ok || selector == "" {
		return nil, fmt.Errorf("参数 'selector' 是必需的且必须为字符串")
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

	text, err := el.Text()
	if err != nil {
		return nil, fmt.Errorf("获取元素文本失败: %w", err)
	}

	return map[string]interface{}{
		"selector": selector,
		"text":     text,
	}, nil
}

// GetAttributeTool 获取元素属性工具
type GetAttributeTool struct{}

func (t *GetAttributeTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	selector, ok := params["selector"].(string)
	if !ok || selector == "" {
		return nil, fmt.Errorf("参数 'selector' 是必需的且必须为字符串")
	}

	attrName, ok := params["attribute"].(string)
	if !ok || attrName == "" {
		return nil, fmt.Errorf("参数 'attribute' 是必需的且必须为字符串")
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

	value, err := el.Attribute(attrName)
	if err != nil {
		return nil, fmt.Errorf("获取属性 '%s' 失败: %w", attrName, err)
	}

	result := map[string]interface{}{
		"selector": selector,
		"attribute": attrName,
	}
	if value != nil {
		result["value"] = *value
	} else {
		result["value"] = nil
	}
	return result, nil
}

// SelectTool 选择下拉选项工具
type SelectTool struct{}

func (t *SelectTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	selector, ok := params["selector"].(string)
	if !ok || selector == "" {
		return nil, fmt.Errorf("参数 'selector' 是必需的且必须为字符串")
	}

	option, ok := params["option"].(string)
	if !ok || option == "" {
		return nil, fmt.Errorf("参数 'option' 是必需的且必须为字符串")
	}

	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	timeout := getTimeout(params)

	// 等待元素（用于验证元素是否存在）
	_, err = page.Timeout(timeout).Element(selector)
	if err != nil {
		return nil, fmt.Errorf("未找到元素 '%s': %w", selector, err)
	}

	// 使用 JavaScript 选择选项
	js := fmt.Sprintf(`(function() {
		const select = document.querySelector(%q);
		for (const option of select.options) {
			if (option.value === %q || option.text === %q) {
				option.selected = true;
				select.dispatchEvent(new Event('change'));
				return true;
			}
		}
		return false;
	})()`, selector, option, option)

	_, err = page.Eval(js)
	if err != nil {
		return nil, fmt.Errorf("选择选项失败: %w", err)
	}
	return map[string]interface{}{
		"status":   "success",
		"selector": selector,
		"option":   option,
	}, nil
}

// FileUploadTool 文件上传工具
type FileUploadTool struct{}

func (t *FileUploadTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	selector, ok := params["selector"].(string)
	if !ok || selector == "" {
		return nil, fmt.Errorf("参数 'selector' 是必需的且必须为字符串")
	}

	filePath, ok := params["filePath"].(string)
	if !ok || filePath == "" {
		return nil, fmt.Errorf("参数 'filePath' 是必需的且必须为字符串")
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

	if err := el.SetFiles([]string{filePath}); err != nil {
		return nil, fmt.Errorf("文件上传失败: %w", err)
	}

	return map[string]interface{}{
		"status":   "success",
		"selector": selector,
		"filePath": filePath,
	}, nil
}

// ScreenshotTool 截图工具
type ScreenshotTool struct{}

func (t *ScreenshotTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	selector := ""
	if s, ok := params["selector"].(string); ok && s != "" {
		selector = s
	}

	timeout := getTimeout(params)

	var screenshot []byte

	if selector != "" {
		// 等待元素
		el, err := page.Timeout(timeout).Element(selector)
		if err != nil {
			return nil, fmt.Errorf("未找到元素 '%s': %w", selector, err)
		}
		screenshot, err = el.Screenshot(proto.PageCaptureScreenshotFormatJpeg, 80)
		if err != nil {
			return nil, fmt.Errorf("截图元素 '%s' 失败: %w", selector, err)
		}
	} else {
		// 全屏截图
		screenshot, err = page.Screenshot(false, nil)
		if err != nil {
			return nil, fmt.Errorf("全屏截图失败: %w", err)
		}
	}

	return map[string]interface{}{
		"status":     "success",
		"selector":   selector,
		"screenshot": screenshot, // 二进制数据，实际使用时需要 base64 编码
	}, nil
}

// Wait tool 等待元素
func waitElement(page *rod.Page, timeout time.Duration, selector string) (*rod.Element, error) {
	return page.Timeout(timeout).Element(selector)
}
