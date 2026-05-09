package browser

import (
	"context"
	"fmt"

	"github.com/go-rod/rod"
)

// GoBackTool 浏览器后退工具
type GoBackTool struct{}

func (t *GoBackTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	if err := page.NavigateBack(); err != nil {
		return nil, fmt.Errorf("后退失败: %w", err)
	}

	// 等待页面加载
	page.WaitLoad()

	// 获取页面信息
	info, err := page.Info()
	if err != nil {
		return map[string]interface{}{
			"status": "error",
			"error":  fmt.Sprintf("获取页面信息失败: %v", err),
		}, nil
	}

	u, title := extractPageInfo(info)
	return map[string]interface{}{
		"status": "success",
		"url":    u,
		"title":  title,
	}, nil
}

// GoForwardTool 浏览器前进工具
type GoForwardTool struct{}

func (t *GoForwardTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	if err := page.NavigateForward(); err != nil {
		return nil, fmt.Errorf("前进失败: %w", err)
	}

	// 等待页面加载
	page.WaitLoad()

	// 获取页面信息
	info, err := page.Info()
	if err != nil {
		return map[string]interface{}{
			"status": "error",
			"error":  fmt.Sprintf("获取页面信息失败: %v", err),
		}, nil
	}

	u, title := extractPageInfo(info)
	return map[string]interface{}{
		"status": "success",
		"url":    u,
		"title":  title,
	}, nil
}

// ReloadTool 页面刷新工具
type ReloadTool struct{}

func (t *ReloadTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	if err := page.Reload(); err != nil {
		return nil, fmt.Errorf("刷新失败: %w", err)
	}

	// 等待页面加载
	page.WaitLoad()

	// 获取页面信息
	info, err := page.Info()
	if err != nil {
		return map[string]interface{}{
			"status": "error",
			"error":  fmt.Sprintf("获取页面信息失败: %v", err),
		}, nil
	}

	u, title := extractPageInfo(info)
	return map[string]interface{}{
		"status": "success",
		"url":    u,
		"title":  title,
	}, nil
}

// IsHistoryNavigatable 检查浏览器历史是否可以后退或前进
// 通过 GetNavigationHistory 获取历史记录状态
func IsHistoryNavigatable(page *rod.Page) (canGoBack bool, canGoForward bool, err error) {
	history, err := page.GetNavigationHistory()
	if err != nil {
		return false, false, fmt.Errorf("获取导航历史失败: %w", err)
	}

	// CurrentIndex 返回当前页面的索引
	index := history.CurrentIndex
	canGoBack = index > 0
	canGoForward = index < len(history.Entries)-1

	return canGoBack, canGoForward, nil
}
