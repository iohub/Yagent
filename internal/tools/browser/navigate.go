package browser

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// pageCtxKey 用于从 context 获取 rod.Page（内部类型定义）
type contextKey string

// pageCtxKey 是内部的 context key（未导出）
const pageCtxKey contextKey = "browser_page"

// PageCtxKey 是导出的 context key，供 Agent 创建 context 时使用
// 注意：这个变量的值与 pageCtxKey 相同，但它是导出的
var PageCtxKey interface{} = pageCtxKey

// GetPage 从 context 中获取浏览器页面
func GetPage(ctx context.Context) (*rod.Page, error) {
	page, ok := ctx.Value(pageCtxKey).(*rod.Page)
	if !ok || page == nil {
		return nil, fmt.Errorf("浏览器页面上下文不可用")
	}
	return page, nil
}

// validateHTTPURL 验证 URL 仅允许 http/https 协议
func validateHTTPURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("无效的 URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("仅允许 http/https URL，收到: %s", parsed.Scheme)
	}
	if parsed.Host == "" {
		return fmt.Errorf("URL 缺少主机名: %s", rawURL)
	}
	return nil
}

// getTimeout 从 params 获取超时秒数，默认 30 秒
func getTimeout(params map[string]interface{}) time.Duration {
	if timeoutSec, ok := params["timeout_seconds"].(float64); ok && timeoutSec > 0 {
		return time.Duration(timeoutSec) * time.Second
	}
	return 30 * time.Second
}

// extractPageInfo 从 rod 的 Info 结果中提取页面信息
func extractPageInfo(info *proto.TargetTargetInfo) (string, string) {
	if info == nil {
		return "", ""
	}
	return info.URL, info.Title
}

// NavigateTool 页面导航工具
type NavigateTool struct{}

func (t *NavigateTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	rawURL, ok := params["url"].(string)
	if !ok || rawURL == "" {
		return nil, fmt.Errorf("参数 'url' 是必需的且必须为字符串")
	}

	// URL 安全验证
	if err := validateHTTPURL(rawURL); err != nil {
		return nil, err
	}

	// 获取页面
	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	// 超时控制
	timeout := getTimeout(params)
	navCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 导航
	if err := page.Context(navCtx).Navigate(rawURL); err != nil {
		return nil, fmt.Errorf("导航失败: %w", err)
	}

	// 等待页面加载
	page.WaitLoad()

	// 获取页面信息
	info, err := page.Info()
	if err != nil {
		return map[string]interface{}{
			"url":    rawURL,
			"title":  "",
			"status": "navigated",
		}, nil
	}

	u, title := extractPageInfo(info)
	return map[string]interface{}{
		"title":  title,
		"url":    u,
		"status": "success",
	}, nil
}

// GetCurrentURLTool 获取当前页面 URL
type GetCurrentURLTool struct{}

func (t *GetCurrentURLTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	info, err := page.Info()
	if err != nil {
		return nil, fmt.Errorf("获取页面信息失败: %w", err)
	}

	u, title := extractPageInfo(info)
	return map[string]interface{}{
		"url":   u,
		"title": title,
	}, nil
}
