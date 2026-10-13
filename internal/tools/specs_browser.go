package tools

import (
	"yagent/internal/toolspec"
)

// 14 个浏览器工具的能力标签。argv 组装与结果渲染仍在 browser_operations.go 的
// browserToolSpecs 表里，name/description/parameters 在 schemas.json 里，
// 三者由 Validate() 与本文件的注册循环互相对账。
//
// 首版全部标 Serial：它们共享同一条 CDP 连接与"当前标签页"这一会话状态，
// 并发改页面状态会互相踩（历史 nonParallelToolNames 只硬挡了 open/tabs/eval，
// 其余靠"没进只读白名单"侥幸串行，这里改成显式声明）。
func init() {
	for _, s := range []struct {
		name    string
		eff     toolspec.Effect
		freq    toolspec.Frequency
		trunc   toolspec.TruncTier
		mutates bool
		brief   string
	}{
		{"browser_status", toolspec.EffectBrowserObserve, toolspec.Hot, toolspec.TruncFirst, false, "查会话状态：CDP 端点/版本/当前页"},
		{"browser_open", toolspec.EffectBrowserAct, toolspec.Hot, toolspec.TruncNormal, true, "在当前标签页导航到 URL"},
		{"browser_inspect", toolspec.EffectBrowserObserve, toolspec.Hot, toolspec.TruncFirst, false, "读页面可见文本与可交互元素"},
		{"browser_snapshot", toolspec.EffectBrowserObserve, toolspec.Hot, toolspec.TruncFirst, false, "刷新 @ref 元素句柄"},
		{"browser_click", toolspec.EffectBrowserAct, toolspec.Hot, toolspec.TruncNormal, true, "点击 @ref/选择器/坐标"},
		{"browser_fill", toolspec.EffectBrowserAct, toolspec.Hot, toolspec.TruncNormal, true, "写入输入框并触发事件"},
		{"browser_type", toolspec.EffectBrowserAct, toolspec.Cold, toolspec.TruncNormal, true, "在当前焦点插入文本"},
		{"browser_press", toolspec.EffectBrowserAct, toolspec.Cold, toolspec.TruncNormal, true, "按键或组合键"},
		{"browser_scroll", toolspec.EffectBrowserAct, toolspec.Cold, toolspec.TruncNormal, true, "滚动页面或内部容器"},
		{"browser_select", toolspec.EffectBrowserAct, toolspec.Cold, toolspec.TruncNormal, true, "选择 <select> 选项"},
		{"browser_tabs", toolspec.EffectBrowserAct, toolspec.Cold, toolspec.TruncNormal, true, "列举/新建/切换/关闭标签页"},
		{"browser_wait", toolspec.EffectBrowserObserve, toolspec.Hot, toolspec.TruncNormal, false, "等待加载/网络空闲/元素出现"},
		{"browser_screenshot", toolspec.EffectBrowserArtifact, toolspec.Warm, toolspec.TruncNormal, true, "截图存到会话工作区"},
		{"browser_eval", toolspec.EffectBrowserEval, toolspec.Hot, toolspec.TruncFirst, true, "在当前页面执行 JS"},
	} {
		browserName := s.name
		toolspec.MustRegister(toolspec.Spec{
			Name:      browserName,
			Effect:    s.eff,
			Frequency: s.freq,
			Trunc:     s.trunc,
			Mutates:   s.mutates,
			Serial:    true,
			NeedsAuth: s.eff.Has(toolspec.EffectBrowserEval),
			Brief:     s.brief,
			Available: browserAvailable,
			Build:     buildBrowserTool(browserName),
		})
	}
}

func browserAvailable(d *toolspec.Deps) (bool, string) {
	if d.Browser == nil {
		return false, "浏览器执行层未装配"
	}
	return true, ""
}

// buildBrowserTool 从 BrowserResolver 取该名的执行器。argv/render 侧缺实现时
// 这里返回错误，Surface 会记录跳过原因，而不是注册一个空转适配器。
func buildBrowserTool(name string) func(*toolspec.Deps) (toolspec.ToolFunc, error) {
	return func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
		if err := require(d.Browser, "Browser"); err != nil {
			return nil, err
		}
		fn, ok := d.Browser.ToolFunc(name)
		if !ok {
			return nil, errUnimplemented{name}
		}
		return fn, nil
	}
}

type errUnimplemented struct{ name string }

func (e errUnimplemented) Error() string {
	return e.name + " 在 browserToolSpecs 里没有实现"
}
