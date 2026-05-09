package browser

import (
	"context"
	"fmt"

	"github.com/go-rod/rod/lib/proto"
)

// GetCookiesTool 获取 Cookie 工具
type GetCookiesTool struct{}

func (t *GetCookiesTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	// 获取当前页面的所有 cookie
	cookies, err := page.Cookies([]string{})
	if err != nil {
		return nil, fmt.Errorf("获取 Cookie 失败: %w", err)
	}

	cookieList := make([]map[string]interface{}, 0, len(cookies))
	for _, c := range cookies {
		cookieList = append(cookieList, map[string]interface{}{
			"name":      c.Name,
			"value":     "[REDACTED]", // 出于安全考虑，不暴露原始值
			"domain":    c.Domain,
			"path":      c.Path,
			"expires":   c.Expires,
			"httpOnly":  c.HTTPOnly,
			"secure":    c.Secure,
			"session":   c.Session,
			"sameSite":  c.SameSite,
		})
	}

	return map[string]interface{}{
		"cookies": cookieList,
		"count":   len(cookieList),
	}, nil
}

// SetCookiesTool 设置 Cookie 工具
type SetCookiesTool struct{}

func (t *SetCookiesTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	cookiesParam, ok := params["cookies"].([]interface{})
	if !ok {
		return nil, fmt.Errorf("参数 'cookies' 是必需的且必须为数组")
	}

	setCount := 0
	for _, c := range cookiesParam {
		cookieMap, ok := c.(map[string]interface{})
		if !ok {
			continue
		}

		name, _ := cookieMap["name"].(string)
		value, _ := cookieMap["value"].(string)
		domain, _ := cookieMap["domain"].(string)
		path, _ := cookieMap["path"].(string)

		if name == "" || value == "" {
			continue
		}

		cookie := &proto.NetworkCookieParam{
			Name:   name,
			Value:  value,
			Domain: domain,
			Path:   path,
		}

		if path == "" {
			cookie.Path = "/"
		}

		if httpOnly, ok := cookieMap["http_only"].(bool); ok {
			cookie.HTTPOnly = httpOnly
		}
		if secure, ok := cookieMap["secure"].(bool); ok {
			cookie.Secure = secure
		}

		if err := page.SetCookies([]*proto.NetworkCookieParam{cookie}); err != nil {
			return nil, fmt.Errorf("设置 Cookie '%s' 失败: %w", name, err)
		}
		setCount++
	}

	return map[string]interface{}{
		"status": "success",
		"count":  setCount,
	}, nil
}
