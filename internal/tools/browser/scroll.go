package browser

import (
	"context"
	"fmt"
	"time"

	"github.com/go-rod/rod"
)

// ScrollTool 页面滚动工具
type ScrollTool struct{}

func (t *ScrollTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	x := 0
	y := 0
	if xVal, ok := params["x"].(float64); ok {
		x = int(xVal)
	}
	if yVal, ok := params["y"].(float64); ok {
		y = int(yVal)
	}

	// 使用 JavaScript 滚动
	js := fmt.Sprintf("window.scrollTo(%d, %d);", x, y)
	if _, err := page.Eval(js); err != nil {
		return nil, fmt.Errorf("滚动失败: %w", err)
	}

	return map[string]interface{}{
		"status":   "success",
		"scroll_x": x,
		"scroll_y": y,
	}, nil
}

// ScrollToElementTool 滚动到指定元素工具
type ScrollToElementTool struct{}

func (t *ScrollToElementTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
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

	// 使用 JavaScript 滚动到元素
	js := fmt.Sprintf(`document.querySelector(%q).scrollIntoView({behavior: 'auto', block: 'center', inline: 'center'})`, selector)
	if _, err := page.Eval(js); err != nil {
		return nil, fmt.Errorf("滚动到元素失败: %w", err)
	}

	return map[string]interface{}{
		"status":   "success",
		"selector": selector,
	}, nil
}

// ScrollToTopTool 滚动到页面顶部工具
type ScrollToTopTool struct{}

func (t *ScrollToTopTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	// 使用 JavaScript 滚动到顶部
	if _, err := page.Eval("window.scrollTo(0, 0)"); err != nil {
		return nil, fmt.Errorf("滚动到顶部失败: %w", err)
	}

	return map[string]interface{}{
		"status": "success",
	}, nil
}

// ScrollByTool 相对滚动工具
type ScrollByTool struct{}

func (t *ScrollByTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	x := 0
	y := 0
	if xVal, ok := params["x"].(float64); ok {
		x = int(xVal)
	}
	if yVal, ok := params["y"].(float64); ok {
		y = int(yVal)
	}

	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	// 获取当前滚动位置
	current, err := page.Evaluate(&rod.EvalOptions{
		JS: "JSON.stringify({x: window.scrollX, y: window.scrollY})",
	})
	if err != nil {
		return nil, fmt.Errorf("获取当前滚动位置失败: %w", err)
	}
	_ = current

	// 使用 JavaScript 相对滚动
	js := fmt.Sprintf("window.scrollBy(%d, %d);", x, y)
	if _, err := page.Eval(js); err != nil {
		return nil, fmt.Errorf("相对滚动失败: %w", err)
	}

	return map[string]interface{}{
		"status":   "success",
		"scroll_x": x,
		"scroll_y": y,
	}, nil
}

// ScrollUntilVisibleTool 滚动直到元素可见工具
type ScrollUntilVisibleTool struct{}

func (t *ScrollUntilVisibleTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	selector, ok := params["selector"].(string)
	if !ok || selector == "" {
		return nil, fmt.Errorf("参数 'selector' 是必需的且必须为字符串")
	}

	maxScroll := 5000 // 最大滚动距离
	step := 200       // 每次滚动步长

	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	// 使用 JavaScript 检查元素是否可见并滚动
	js := fmt.Sprintf(`
		(function() {
			const element = document.querySelector(%q);
			if (!element) return {found: false, visible: false};
			
			const rect = element.getBoundingClientRect();
			const isVisible = (
				rect.top >= 0 && 
				rect.left >= 0 && 
				rect.bottom <= (window.innerHeight || document.documentElement.clientHeight) &&
				rect.right <= (window.innerWidth || document.documentElement.clientWidth)
			);
			
			return {
				found: true,
				visible: isVisible,
				top: rect.top,
				height: rect.height
			};
		})()
	`, selector)

	result, err := page.Eval(js)
	if err != nil {
		return nil, fmt.Errorf("检查元素可见性失败: %w", err)
	}

	_ = result
	_ = maxScroll
	_ = step

	// 简单的滚动直到看到元素
	for i := 0; i < maxScroll/step; i++ {
		// 检查是否可见
		checkJs := fmt.Sprintf(`
			(function() {
				const element = document.querySelector(%q);
				if (!element) return {found: false, visible: false};
				const rect = element.getBoundingClientRect();
				const isVisible = (rect.top >= 0 && rect.top < window.innerHeight);
				return {found: true, visible: isVisible};
			})()
		`, selector)
		
		res, err := page.Eval(checkJs)
		if err != nil {
			continue
		}
		_ = res

		// 滚动
		if _, err := page.Eval(fmt.Sprintf("window.scrollBy(0, %d);", step)); err != nil {
			return nil, fmt.Errorf("滚动失败: %w", err)
		}

		// 等待一下让页面更新
		time.Sleep(50 * time.Millisecond)
	}

	return map[string]interface{}{
		"status":      "success",
		"selector":    selector,
		"maxScroll":   maxScroll,
	}, nil
}
