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

	// 合并所有 adapters
	allAdapters := append(browserAdapters, agentExitAdapter)

	// Add ask_user_for_help tool (skipped in full-yolo mode)
	if !globalCtx.FullYoloMode {
		askUserAdapter := tools.NewAdapter("ask_user_for_help",
			"When you encounter uncertainty, need user confirmation or authorization during browser task execution, use this tool to request help from the user. Supports three interaction modes: confirm, select, and input.",
			globalCtx.FlowOps.ExecuteAskUserForHelp,
		).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"reason": map[string]interface{}{
					"type":        "string",
					"description": "A clear explanation of why user help or authorization is needed",
				},
				"specific_question": map[string]interface{}{
					"type":        "string",
					"description": "The specific question to ask the user",
				},
				"suggested_options": map[string]interface{}{
					"type": "array",
					"items": map[string]interface{}{
						"type": "string",
					},
					"description": "Optional suggested answer options. Controls the interaction mode: empty=input mode, ['yes','no']=confirm mode, 2+ options=select mode",
				},
				"interaction_type": map[string]interface{}{
					"type": "string",
					"enum": []interface{}{"confirm", "select", "input"},
					"description": "Optional. Explicitly set the interaction mode, overriding automatic inference",
				},
				"default_value": map[string]interface{}{
					"type":        "string",
					"description": "Optional. Default option or pre-filled text",
				},
				"placeholder": map[string]interface{}{
					"type":        "string",
					"description": "Optional. Placeholder text for the input field (input mode only)",
				},
				"allow_custom": map[string]interface{}{
					"type":        "boolean",
					"description": "Optional. Whether to allow custom input in select mode. Default: true",
				},
			},
			"required": []string{"reason", "specific_question"},
		})
		allAdapters = append(allAdapters, askUserAdapter)
	}

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
	systemPrompt = a.InjectSharedMemory(systemPrompt, "default", a.GlobalCtx.ProjectPath)

	// 构建执行配置
	cfg := DefaultExecutorConfig()
	cfg.SystemPrompt = systemPrompt
	cfg.UserInput = input
	cfg.Adapters = a.Adapters
	cfg.LLM = a.LLM
	cfg.MaxSteps = a.maxSteps
	cfg.Publisher = a.Publisher
	cfg.AgentName = "browser"
	cfg.StopOnFinish = true // agent_exit 时立即返回
	cfg.RepoContext = a.GlobalCtx.RepoSummary
	// EnableCollaboration 已默认 true

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
