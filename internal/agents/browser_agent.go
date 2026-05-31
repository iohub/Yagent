package agents

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"time"

	"codeactor/internal/browser"
	browsertools "codeactor/internal/tools/browser"
	"codeactor/internal/tools"
	"codeactor/internal/globalctx"

	"codeactor/internal/llm"
)

//go:embed browser.prompt.md
var browserPrompt string

// BrowserAgent 浏览器自动化 Agent
// 使用 go-rod 控制无头 Chrome 浏览器执行网页任务
type BrowserAgent struct {
	BaseAgent
	GlobalCtx      *globalctx.GlobalCtx
	Adapters       []*tools.Adapter
	maxSteps       int
	browserMgr     *browser.Manager
}

// NewBrowserAgent 创建 Browser-Agent
func NewBrowserAgent(
	globalCtx *globalctx.GlobalCtx,
	browserMgr *browser.Manager,
	llm llm.Engine,
	maxSteps int,
) *BrowserAgent {
	if maxSteps <= 0 {
		maxSteps = 15 // 浏览器任务通常需要较多步骤
	}

	// 获取工作区目录
	workspaceDir := globalCtx.ProjectPath

	// 从 browser 工具包获取所有浏览器工具
	browserAdapters := browsertools.BrowserTools(workspaceDir)

	// 添加 agent_exit 工具以允许 Agent 正常退出
	agentExitAdapter := tools.NewAdapter("agent_exit",
		"退出 Browser-Agent 并返回最终结果。在任务完成或无法继续时调用此工具。",
		globalCtx.FlowOps.ExecuteAgentExit,
	).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"reason": map[string]interface{}{
				"type":        "string",
				"description": "退出原因：任务完成、无法继续、需要澄清等",
			},
		},
		"required": []string{"reason"},
	})

	// 添加 ask_user_for_help 工具（用于 evaluate_js 等高风险操作的确认）
	askUserAdapter := tools.NewAdapter("ask_user_for_help",
		"请求用户帮助或确认。用于高风险操作（如 evaluate_js）需要用户明确批准时。",
		globalCtx.FlowOps.ExecuteAskUserForHelp,
	).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"question": map[string]interface{}{
				"type":        "string",
				"description": "需要用户确认的问题",
			},
		},
		"required": []string{"question"},
	})

	// 合并所有 adapters
	allAdapters := append(browserAdapters, agentExitAdapter, askUserAdapter)

	// 设置工作区守卫
	tools.SetGuardOnAdapters(allAdapters, globalCtx.Guard)

	return &BrowserAgent{
		BaseAgent: BaseAgent{
			LLM:       llm,
			Publisher: globalCtx.Publisher,
		},
		GlobalCtx:      globalCtx,
		Adapters:       allAdapters,
		maxSteps:       maxSteps,
		browserMgr:     browserMgr,
	}
}

// Name 返回 Agent 名称
func (a *BrowserAgent) Name() string {
	return "BrowserAgent"
}

// Run 执行浏览器任务
// 输入：用户的任务描述（如 "截图 https://example.com 首页"）
// 输出：任务结果摘要
func (a *BrowserAgent) Run(ctx context.Context, input string) (AgentResult, error) {
	log.Printf("[BrowserAgent] 开始执行任务: %s", truncateForLog(input, 100))

	// 检查浏览器管理器是否可用
	if !a.browserMgr.IsRunning() && !a.browserMgr.GetConfig().AutoLaunch {
		return AgentResult{}, fmt.Errorf("浏览器未启动且 AutoLaunch 被禁用")
	}

	// 从浏览器配置读取任务超时，默认180秒
	taskTimeout := 180 * time.Second
	if cfg := a.browserMgr.GetConfig(); cfg.TaskTimeoutSeconds > 0 {
		taskTimeout = time.Duration(cfg.TaskTimeoutSeconds) * time.Second
	}

	// 创建带超时的上下文
	taskCtx, cancel := context.WithTimeout(ctx, taskTimeout)
	defer cancel()

	// 从浏览器管理器获取页面
	page, release, err := a.browserMgr.AcquirePage(taskCtx)
	if err != nil {
		log.Printf("[BrowserAgent] 获取页面失败: %v", err)
		return AgentResult{}, fmt.Errorf("获取浏览器页面失败: %w", err)
	}
	defer release()

	log.Printf("[BrowserAgent] 页面获取成功")

	// 将页面注入到上下文，供浏览器工具使用
	// 使用 context.WithValue 存储页面引用，浏览器工具通过 GetPage() 获取
	pageCtx := context.WithValue(taskCtx, browsertools.PageCtxKey, page)

	// 构建环境上下文的系统提示词
	systemPrompt := a.GlobalCtx.FormatPrompt(browserPrompt)

	// 构建执行配置
	cfg := ExecutorConfig{
		SystemPrompt: systemPrompt,
		UserInput:    input,
		Adapters:     a.Adapters,
		LLM:          a.LLM,
		MaxSteps:     a.maxSteps,
		Publisher:    a.Publisher,
		AgentName:    "browser",
		StopOnFinish: true, // agent_exit 时立即返回
	}

	// 运行 Agent 循环
	log.Printf("[BrowserAgent] 开始 LLM 推理循环 (maxSteps=%d)", a.maxSteps)
	result, err := RunAgentLoop(pageCtx, cfg)
	if err != nil {
		log.Printf("[BrowserAgent] 执行失败: %v", err)
		return AgentResult{}, fmt.Errorf("Browser-Agent 执行失败: %w", err)
	}

	log.Printf("[BrowserAgent] 任务完成")
	return AgentResult{
		Text:   result.Text,
		Memory: ConvertLLMHistoryToMemory(result.History),
	}, nil
}

// GetBrowserManager 获取浏览器管理器（供外部使用）
func (a *BrowserAgent) GetBrowserManager() *browser.Manager {
	return a.browserMgr
}

// truncateForLog 截断日志字符串
func truncateForLog(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
