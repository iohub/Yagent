package browser

import (
	"context"

	"codeactor/internal/tools"
)

// BrowserTools 返回所有浏览器工具的 Adapter 列表
// workspaceDir: 工作区目录（用于截图/PDF 文件保存）
func BrowserTools(workspaceDir string) []*tools.Adapter {
	// 创建工作区相关的工具实例
	pdfTool := &PDFTool{WorkspaceDir: workspaceDir}

	toolDefs := []struct {
		name        string
		description string
		schema      map[string]interface{}
		executor    interface{ Execute(context.Context, map[string]interface{}) (interface{}, error) }
	}{
		// 导航类
		{
			name:        "navigate",
			description: "导航到指定 URL。仅允许 http:// 和 https:// 协议。返回页面标题、URL 和状态。",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"url": map[string]interface{}{
						"type":        "string",
						"description": "要导航的 URL（http/https）",
					},
					"timeout_seconds": map[string]interface{}{
						"type":        "number",
						"description": "导航超时秒数，默认 30",
					},
				},
				"required": []string{"url"},
			},
			executor: &NavigateTool{},
		},
		{
			name:        "get_current_url",
			description: "获取当前页面的 URL 和标题。",
			schema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
			executor: &GetCurrentURLTool{},
		},
		// 历史导航类
		{
			name:        "go_back",
			description: "浏览器后退到上一页。",
			schema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
			executor: &GoBackTool{},
		},
		{
			name:        "go_forward",
			description: "浏览器前进到下一页。",
			schema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
			executor: &GoForwardTool{},
		},
		{
			name:        "reload",
			description: "刷新当前页面。",
			schema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
			executor: &ReloadTool{},
		},
		// 交互类
		{
			name:        "click",
			description: "点击页面元素。支持左键(left)、右键(right)、中键(middle)。默认为左键单击。",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"selector": map[string]interface{}{
						"type":        "string",
						"description": "CSS 选择器，用于定位要点击的元素",
					},
					"button": map[string]interface{}{
						"type":        "string",
						"enum":        []string{"left", "right", "middle"},
						"description": "鼠标按钮，默认 left",
					},
					"timeout_seconds": map[string]interface{}{
						"type":        "number",
						"description": "等待元素出现的超时秒数，默认 30",
					},
				},
				"required": []string{"selector"},
			},
			executor: &ClickTool{},
		},
		{
			name:        "input",
			description: "在表单元素中输入文本。先清空现有内容，然后输入新文本。",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"selector": map[string]interface{}{
						"type":        "string",
						"description": "CSS 选择器，定位输入元素",
					},
					"text": map[string]interface{}{
						"type":        "string",
						"description": "要输入的文本内容",
					},
					"timeout_seconds": map[string]interface{}{
						"type":        "number",
						"description": "等待元素出现的超时秒数，默认 30",
					},
				},
				"required": []string{"selector", "text"},
			},
			executor: &InputTool{},
		},
		{
			name:        "scroll",
			description: "滚动页面到指定坐标 (x, y)。",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"x": map[string]interface{}{
						"type":        "integer",
						"description": "水平滚动位置（像素），默认 0",
					},
					"y": map[string]interface{}{
						"type":        "integer",
						"description": "垂直滚动位置（像素），默认 0",
					},
				},
			},
			executor: &ScrollTool{},
		},
		{
			name:        "wait_element",
			description: "等待指定的 CSS 选择器元素出现在页面中。返回元素是否出现及其可见性。",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"selector": map[string]interface{}{
						"type":        "string",
						"description": "CSS 选择器",
					},
					"timeout_seconds": map[string]interface{}{
						"type":        "number",
						"description": "超时秒数，默认 30",
					},
				},
				"required": []string{"selector"},
			},
			executor: &WaitElementTool{},
		},
		{
			name:        "wait",
			description: "等待指定的毫秒数。最大 30000 毫秒（30 秒）。",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"milliseconds": map[string]interface{}{
						"type":        "integer",
						"description": "等待的毫秒数，默认 1000",
					},
				},
			},
			executor: &WaitTool{},
		},
		// 提取类
		{
			name:        "extract_text",
			description: "从页面或指定元素提取文本内容。支持 CSS 选择器和最大字符数限制。",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"selector": map[string]interface{}{
						"type":        "string",
						"description": "CSS 选择器（可选，不指定则提取整个页面文本）",
					},
					"max_chars": map[string]interface{}{
						"type":        "number",
						"description": "最大返回字符数，默认 50000",
					},
					"timeout_seconds": map[string]interface{}{
						"type":        "number",
						"description": "超时秒数，默认 30",
					},
				},
			},
			executor: &ExtractTextTool{},
		},
		{
			name:        "extract_html",
			description: "从页面或指定元素提取 HTML 内容。支持 CSS 选择器和最大字符数限制。",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"selector": map[string]interface{}{
						"type":        "string",
						"description": "CSS 选择器（可选，不指定则提取整个页面 HTML）",
					},
					"max_chars": map[string]interface{}{
						"type":        "number",
						"description": "最大返回字符数，默认 100000",
					},
					"timeout_seconds": map[string]interface{}{
						"type":        "number",
						"description": "超时秒数，默认 30",
					},
				},
			},
			executor: &ExtractHTMLTool{},
		},
		// 输出类
		{
			name:        "screenshot",
			description: "对页面或指定元素截图，保存为 PNG 文件到工作区目录。",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"selector": map[string]interface{}{
						"type":        "string",
						"description": "CSS 选择器（可选，不指定则截取整个页面）",
					},
					"whole_page": map[string]interface{}{
						"type":        "boolean",
						"description": "是否截取整个页面（包括滚动区域），默认 false",
					},
					"output_file": map[string]interface{}{
						"type":        "string",
						"description": "输出文件路径（相对于工作区），默认自动生成",
					},
				},
			},
			executor: &ScreenshotTool{},
		},
		{
			name:        "pdf",
			description: "将当前页面生成 PDF 文件，保存到工作区目录。",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"output_file": map[string]interface{}{
						"type":        "string",
						"description": "输出文件路径（相对于工作区），默认自动生成",
					},
				},
			},
			executor: pdfTool,
		},
		// Cookie 类
		{
			name:        "get_cookies",
			description: "获取当前页面的所有 Cookie（出于安全考虑，值会被脱敏）。",
			schema: map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
			executor: &GetCookiesTool{},
		},
		{
			name:        "set_cookies",
			description: "为当前页面设置 Cookie。",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"cookies": map[string]interface{}{
						"type": "array",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"name":     map[string]interface{}{"type": "string", "description": "Cookie 名称"},
								"value":    map[string]interface{}{"type": "string", "description": "Cookie 值"},
								"domain":   map[string]interface{}{"type": "string", "description": "域名"},
								"path":     map[string]interface{}{"type": "string", "description": "路径，默认 /"},
							},
							"required": []string{"name", "value"},
						},
						"description": "要设置的 Cookie 数组",
					},
				},
				"required": []string{"cookies"},
			},
			executor: &SetCookiesTool{},
		},
		// 高级工具
		{
			name:        "evaluate_js",
			description: "在当前页面执行 JavaScript 代码并返回结果。⚠️ 高风险操作，需用户确认。禁止使用 eval、Function 等危险函数。",
			schema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"code": map[string]interface{}{
						"type":        "string",
						"description": "要执行的 JavaScript 代码",
					},
				},
				"required": []string{"code"},
			},
			executor: &EvaluateJSTool{},
		},
	}

	// 构建 Adapter 列表
	adapters := make([]*tools.Adapter, 0, len(toolDefs))
	for _, td := range toolDefs {
		executor := td.executor
		fn := func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			return executor.Execute(ctx, params)
		}
		adapter := tools.NewAdapter(td.name, td.description, fn).WithSchema(td.schema)
		adapters = append(adapters, adapter)
	}

	return adapters
}
