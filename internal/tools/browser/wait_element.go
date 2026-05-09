package browser

import (
	"context"
	"fmt"
	"time"
)

// WaitElementTool 等待元素出现工具
type WaitElementTool struct{}

func (t *WaitElementTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	selector, ok := params["selector"].(string)
	if !ok || selector == "" {
		return nil, fmt.Errorf("参数 'selector' 是必需的且必须为字符串")
	}

	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	timeout := getTimeout(params)

	// 等待元素出现
	el, err := page.Timeout(timeout).Element(selector)
	if err != nil {
		return map[string]interface{}{
			"appeared": false,
			"selector": selector,
			"timeout":  timeout.Seconds(),
			"error":    err.Error(),
		}, nil
	}

	visible, _ := el.Visible()
	return map[string]interface{}{
		"appeared": true,
		"visible":  visible,
		"selector": selector,
	}, nil
}

// WaitTool 等待指定毫秒工具
type WaitTool struct{}

func (t *WaitTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	milliseconds := 1000 // 默认 1 秒
	if ms, ok := params["milliseconds"].(float64); ok && ms > 0 {
		milliseconds = int(ms)
	}

	if milliseconds > 30000 {
		return nil, fmt.Errorf("等待时间不能超过 30000 毫秒 (30 秒)")
	}

	select {
	case <-time.After(time.Duration(milliseconds) * time.Millisecond):
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	return map[string]interface{}{
		"status":    "success",
		"waited_ms": milliseconds,
	}, nil
}

// WaitUntilTool 等待条件满足工具
type WaitUntilTool struct{}

func (t *WaitUntilTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	js, ok := params["js"].(string)
	if !ok || js == "" {
		return nil, fmt.Errorf("参数 'js' 是必需的且必须为字符串")
	}

	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	timeout := getTimeout(params)

	// 轮询检查条件
	checkInterval := 100 * time.Millisecond
	if interval, ok := params["interval"].(float64); ok && interval > 0 {
		checkInterval = time.Duration(interval) * time.Millisecond
	}

	start := time.Now()
	for {
		// 检查是否超时
		if time.Since(start) > timeout {
			return map[string]interface{}{
				"met":     false,
				"timeout": timeout.Seconds(),
			}, nil
		}

		// 检查条件
		result, err := page.Eval(js)
		if err != nil {
			time.Sleep(checkInterval)
			continue
		}

		// 检查返回值是否为 true
		if result != nil {
			if val := result.Value.Val(); val != nil {
				if boolVal, ok := val.(bool); ok && boolVal {
					return map[string]interface{}{
						"met":     true,
						"elapsed": time.Since(start).Milliseconds(),
					}, nil
				}
			}
		}

		time.Sleep(checkInterval)
	}
}

// WaitForNetworkTool 等待网络请求完成工具
type WaitForNetworkTool struct{}

func (t *WaitForNetworkTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	timeout := getTimeout(params)

	// 使用 JavaScript 等待网络请求完成
	js := `
		(function() {
			return new Promise((resolve) => {
				const check = () => {
					if (document.readyState === 'complete') {
						resolve(true);
					} else {
						setTimeout(check, 100);
					}
				};
				check();
			});
		})()
	`

	done := make(chan struct{})
	go func() {
		page.Eval(js)
		close(done)
	}()

	select {
	case <-done:
		return map[string]interface{}{
			"status": "success",
			"ready":  true,
		}, nil
	case <-time.After(timeout):
		return map[string]interface{}{
			"status":  "timeout",
			"ready":   false,
			"timeout": timeout.Seconds(),
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// WaitForSelectorTool 等待选择器出现工具 (与 WaitElementTool 相同，别名)
type WaitForSelectorTool struct{}

func (t *WaitForSelectorTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	return (&WaitElementTool{}).Execute(ctx, params)
}

// WaitForStableTool 等待页面稳定工具
type WaitForStableTool struct{}

func (t *WaitForStableTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	timeout := getTimeout(params)

	// 等待页面加载完成
	if err := page.WaitIdle(time.Duration(timeout) * time.Second); err != nil {
		return map[string]interface{}{
			"stable":  false,
			"timeout": timeout.Seconds(),
			"error":   err.Error(),
		}, nil
	}

	return map[string]interface{}{
		"stable": true,
	}, nil
}
