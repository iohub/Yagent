package agents

import (
	"context"
	_ "embed"
	"fmt"
	"log"
	"time"

	"yagent/internal/browser"
	"yagent/internal/tools"
	browsertools "yagent/internal/tools/browser"

	"yagent/internal/llm"
)

//go:embed browser.prompt.md
var browserPrompt string

// BrowserAgent 浏览器自动化 Agent
// 使用 go-rod 控制无头 Chrome 浏览器执行网页任务
type BrowserAgent struct {
	BaseAgent
	promptFmt  PromptFormatter
	repoCtx    RepoContextStore
	Adapters   []*tools.Adapter
	maxSteps   int
	browserMgr *browser.Manager
}

// BrowserAgentParams 聚合 NewBrowserAgent 所需的全部依赖。
// 用具名字段替代原先 9 个位置参数，让调用点自文档化。
type BrowserAgentParams struct {
	Formatter  PromptFormatter
	Publisher  EventBus
	Env        Env
	Flow       FlowToolSet
	Guard      *tools.WorkspaceGuard
	RepoCtx    RepoContextStore
	BrowserMgr *browser.Manager
	LLM        llm.Engine
	MaxSteps   int
}

// NewBrowserAgent 创建 Browser-Agent：装配浏览器工具集后返回。
func NewBrowserAgent(p BrowserAgentParams) *BrowserAgent {
	maxSteps := p.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 15 // 浏览器任务通常需要较多步骤
	}

	return &BrowserAgent{
		BaseAgent: BaseAgent{
			LLM:       p.LLM,
			Publisher: p.Publisher,
		},
		promptFmt:  p.Formatter,
		repoCtx:    p.RepoCtx,
		Adapters:   buildBrowserAdapters(p),
		maxSteps:   maxSteps,
		browserMgr: p.BrowserMgr,
	}
}

// buildBrowserAdapters 装配 Browser-Agent 的工具集：
// 浏览器工具（来自 browser 工具包）+ agent_exit + ask_user_for_help（full-yolo 模式跳过）。
func buildBrowserAdapters(p BrowserAgentParams) []*tools.Adapter {
	// 获取工作区目录（构造期读取，与原 GlobalCtx.ProjectPath 语义一致）
	workspaceDir := p.Env.ProjectPath()

	// 从 browser 工具包获取所有浏览器工具
	browserAdapters := browsertools.BrowserTools(workspaceDir)

	// 添加 agent_exit 工具以允许 Agent 正常退出
	agentExitAdapter := tools.NewAdapter("agent_exit",
		"退出 Browser-Agent 并返回最终结果。在任务完成或无法继续时调用此工具。",
		p.Flow.ExecuteAgentExit,
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
	if !p.Env.FullYoloMode() {
		askUserAdapter := tools.NewAdapter("ask_user_for_help",
			"When you encounter uncertainty, need user confirmation or authorization during browser task execution, use this tool to request help from the user. Supports three interaction modes: confirm, select, and input.",
			p.Flow.ExecuteAskUserForHelp,
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
					"type":        "string",
					"enum":        []interface{}{"confirm", "select", "input"},
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
	tools.SetGuardOnAdapters(allAdapters, p.Guard)

	return allAdapters
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
	systemPrompt := a.promptFmt.FormatPrompt(browserPrompt)

	// 构建执行配置
	cfg := DefaultExecutorConfig()
	cfg.SystemPrompt = systemPrompt
	cfg.UserInput = input
	cfg.Adapters = a.Adapters
	cfg.LLM = a.LLM
	cfg.MaxSteps = a.maxSteps
	cfg.Publisher = a.Publisher
	cfg.AgentName = a.Name()
	cfg.StopOnFinish = true // agent_exit 时立即返回
	cfg.RepoContext = a.repoCtx.Get()
	// EnableCollaboration 已默认 true

	// 运行 Agent 循环
	log.Printf("[BrowserAgent] 开始 LLM 推理循环 (maxSteps=%d)", a.maxSteps)
	result, err := RunAgentLoop(pageCtx, cfg)
	if err != nil {
		log.Printf("[BrowserAgent] 执行失败: %v", err)
		return AgentResult{}, fmt.Errorf("Browser-Agent 执行失败: %w", err)
	}

	log.Printf("[BrowserAgent] 任务完成")
	// 普通版：Text 无程序化消费点（delegate_browser 返回值由 Director 层
	// FormatForDirector 接管；coding 内部嵌套委派仅作 LLM tool_result）。
	return FinalizeResult(a.Name(), input, result), nil
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
