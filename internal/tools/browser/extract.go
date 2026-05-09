package browser

import (
	"context"
	"fmt"
	"strings"
)

// ExtractTextTool 提取页面文本工具
type ExtractTextTool struct{}

func (t *ExtractTextTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	maxChars := 50000
	if mc, ok := params["max_chars"].(float64); ok && mc > 0 {
		maxChars = int(mc)
	}

	// 清理选择器
	selector := sanitizeSelector("")
	if s, ok := params["selector"].(string); ok {
		selector = sanitizeSelector(s)
	}

	// 如果指定了选择器，提取该元素的文本
	if selector != "" {
		el, err := page.Timeout(getTimeout(params)).Element(selector)
		if err != nil {
			return nil, fmt.Errorf("未找到元素 '%s': %w", selector, err)
		}
		text, err := el.Text()
		if err != nil {
			return nil, fmt.Errorf("提取文本失败: %w", err)
		}
		if len(text) > maxChars {
			text = text[:maxChars] + fmt.Sprintf("\n\n... [截断: 已显示 %d/%d 字符]", maxChars, len(text))
		}
		return map[string]interface{}{
			"text":      text,
			"length":    len(text),
			"truncated": len(text) >= maxChars,
			"selector":  selector,
		}, nil
	}

	// 提取整个 body 文本
	el, err := page.Timeout(getTimeout(params)).Element("body")
	if err != nil {
		// 降级：使用 JS 获取
		result, err := page.Eval("() => document.body ? document.body.innerText : ''")
		if err != nil {
			return nil, fmt.Errorf("提取页面文本失败: %w", err)
		}
		text := result.Value.String()
		if len(text) > maxChars {
			text = text[:maxChars] + fmt.Sprintf("\n\n... [截断: 已显示 %d 字符]", maxChars)
		}
		return map[string]interface{}{
			"text":      text,
			"length":    len(text),
			"truncated": len(text) >= maxChars,
		}, nil
	}

	text, err := el.Text()
	if err != nil {
		return nil, fmt.Errorf("提取文本失败: %w", err)
	}

	if len(text) > maxChars {
		text = text[:maxChars] + fmt.Sprintf("\n\n... [截断: 已显示 %d 字符]", maxChars)
	}

	return map[string]interface{}{
		"text":      text,
		"length":    len(text),
		"truncated": len(text) >= maxChars,
	}, nil
}

// ExtractHTMLTool 提取页面 HTML 工具
type ExtractHTMLTool struct{}

func (t *ExtractHTMLTool) Execute(ctx context.Context, params map[string]interface{}) (interface{}, error) {
	page, err := GetPage(ctx)
	if err != nil {
		return nil, err
	}

	maxChars := 100000
	if mc, ok := params["max_chars"].(float64); ok && mc > 0 {
		maxChars = int(mc)
	}

	// 清理选择器
	selector := sanitizeSelector("")
	if s, ok := params["selector"].(string); ok {
		selector = sanitizeSelector(s)
	}

	// 如果指定了选择器，提取该元素的 outerHTML
	if selector != "" {
		el, err := page.Timeout(getTimeout(params)).Element(selector)
		if err != nil {
			return nil, fmt.Errorf("未找到元素 '%s': %w", selector, err)
		}
		html, err := el.HTML()
		if err != nil {
			return nil, fmt.Errorf("提取 HTML 失败: %w", err)
		}
		if len(html) > maxChars {
			html = html[:maxChars] + fmt.Sprintf("\n<!-- 截断: 已显示 %d/%d 字符 -->", maxChars, len(html))
		}
		return map[string]interface{}{
			"html":      html,
			"length":    len(html),
			"truncated": len(html) >= maxChars,
			"selector":  selector,
		}, nil
	}

	// 提取整个页面 HTML
	html, err := page.HTML()
	if err != nil {
		return nil, fmt.Errorf("提取页面 HTML 失败: %w", err)
	}

	if len(html) > maxChars {
		html = html[:maxChars] + fmt.Sprintf("\n<!-- 截断: 已显示 %d 字符 -->", maxChars)
	}

	return map[string]interface{}{
		"html":      html,
		"length":    len(html),
		"truncated": len(html) >= maxChars,
	}, nil
}

// truncateText 辅助截断函数
func truncateText(text string, maxLen int) string {
	if len(text) <= maxLen {
		return text
	}
	return text[:maxLen] + "... [截断]"
}

// sanitizeSelector 清理选择器字符串
func sanitizeSelector(selector string) string {
	// 移除可能导致问题的字符
	selector = strings.TrimSpace(selector)
	if len(selector) > 500 {
		selector = selector[:500]
	}
	return selector
}
