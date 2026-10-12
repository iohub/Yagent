package agents

import (
	"context"
	"fmt"

	"yagent/internal/llm"
)

// BrowserAgent 浏览器自动化 Agent —— 重构占位。
// 原实现（go-rod Manager + browser_* 工具集 + RunAgentLoop 装配）已整体清空。
type BrowserAgent struct {
	BaseAgent
	maxSteps int
}

// BrowserAgentParams 聚合 NewBrowserAgent 所需的全部依赖。
type BrowserAgentParams struct {
	Publisher EventBus
	LLM       llm.Engine
	MaxSteps  int
}

// NewBrowserAgent 创建 Browser-Agent 占位实例。
func NewBrowserAgent(p BrowserAgentParams) *BrowserAgent {
	maxSteps := p.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 15
	}

	return &BrowserAgent{
		BaseAgent: BaseAgent{
			LLM:       p.LLM,
			Publisher: p.Publisher,
		},
		maxSteps: maxSteps,
	}
}

// Name 返回 Agent 名称
func (a *BrowserAgent) Name() string {
	return "BrowserAgent"
}

// Run 执行浏览器任务。
func (a *BrowserAgent) Run(ctx context.Context, input string) (AgentResult, error) {
	return AgentResult{}, fmt.Errorf("Browser-Agent 尚未实现：浏览器栈重构中 (maxSteps=%d)", a.maxSteps)
}

// TODO(browser): 重构要点
//  1. 浏览器生命周期：进程发现/启动、页面获取与并发上限、空闲回收。
//  2. 工具集：以 Adapter 形式暴露浏览器动作，只读动作需登记到 knownReadOnlyTools 才能并行。
//  3. 执行循环：复用 RunAgentLoop，页面引用经 context 注入工具。
//  4. 配置：新增 [browser] 表并在 config/persist.go 保留原子写回。
//  5. 安全：域名黑白名单与工作区边界（路径型产物如截图/PDF 需经 WorkspaceGuard）。
