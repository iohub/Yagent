package agents

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"

	"yagent/internal/blackboard"
	"yagent/internal/browserctl"
	"yagent/internal/config"
	"yagent/internal/llm"
	"yagent/internal/thinklink"
	"yagent/internal/tools"
)

//go:embed browser.prompt.md
var browserPrompt string

// BrowserAgent 浏览器自动化 Agent：通过外部 browser-control CLI 驱动真实 Chrome。
// Go 侧不直接讲 CDP——所有动作都是 exec 一次 CLI 子命令，浏览器进程与工作区
// 生命周期由 internal/browserctl 管理。
type BrowserAgent struct {
	BaseAgent
	promptFmt PromptFormatter
	Adapters  []*tools.Adapter
	maxSteps  int
	ecCfg     config.EnhancedCommanderConfig
	browser   BrowserControl
}

// BrowserAgentParams 聚合 NewBrowserAgent 所需的全部依赖。
type BrowserAgentParams struct {
	Formatter PromptFormatter
	Publisher EventBus
	Env       Env
	Flow      FlowToolSet
	Guard     *tools.WorkspaceGuard

	Browser    BrowserControl
	BrowserCfg config.BrowserConfig

	EnhancedCommanderCfg config.EnhancedCommanderConfig

	LLM      llm.Engine
	MaxSteps int
}

// NewBrowserAgent 构造 BrowserAgent：装配浏览器工具集与流程控制工具后返回。
func NewBrowserAgent(p BrowserAgentParams) *BrowserAgent {
	return &BrowserAgent{
		BaseAgent: BaseAgent{
			LLM:       p.LLM,
			Publisher: p.Publisher,
		},
		promptFmt: p.Formatter,
		Adapters:  buildBrowserAdapters(p),
		maxSteps:  p.MaxSteps,
		ecCfg:     p.EnhancedCommanderCfg,
		browser:   p.Browser,
	}
}

// Capability 报告浏览器能力是否可用（供 Director/Coding 的 delegate_browser 描述降级使用）。
func (a *BrowserAgent) Capability() (bool, string) {
	if a == nil || a.browser == nil {
		return false, "浏览器执行层未装配"
	}
	return a.browser.Probe()
}

// buildBrowserAdapters 装配浏览器工具集（声明式表驱动，见 tools.BrowserToolNames）
// 与流程控制工具（agent_exit / ask_user_for_help）。
func buildBrowserAdapters(p BrowserAgentParams) []*tools.Adapter {
	if p.Browser == nil {
		return nil
	}

	var toolDefs []tools.ToolDefinition
	if err := json.Unmarshal(ToolsJSON, &toolDefs); err != nil {
		slog.Error("Failed to unmarshal browser tools", "error", err)
		return nil
	}

	browserSet := tools.NewBrowserOperationsTool(p.Browser, browserctl.FromAppConfig(p.BrowserCfg))
	adapters, errs := browserSet.BuildAdapters(toolDefs, p.Guard)
	for _, e := range errs {
		// 声明与实现不一致属于开发期错误：记录并继续，但绝不静默
		slog.Warn("browser tool declaration mismatch", "error", e)
	}

	if p.Flow == nil {
		return adapters
	}
	for _, def := range toolDefs {
		switch def.Name {
		case "agent_exit":
			adapters = append(adapters,
				tools.NewAdapter(def.Name, def.Description, p.Flow.ExecuteAgentExit).WithSchema(def.Parameters))
		case "ask_user_for_help":
			if p.Env != nil && p.Env.FullYoloMode() {
				continue
			}
			adapters = append(adapters,
				tools.NewAdapter(def.Name, def.Description, p.Flow.ExecuteAskUserForHelp).WithSchema(def.Parameters))
		}
	}
	tools.SetGuardOnAdapters(adapters, p.Guard)

	return adapters
}

func (a *BrowserAgent) Name() string {
	return "Browser-Agent"
}

// Run 执行浏览器任务。
func (a *BrowserAgent) Run(ctx context.Context, input string) (AgentResult, error) {
	if ok, reason := a.Capability(); !ok {
		return AgentResult{}, fmt.Errorf(
			"浏览器能力不可用：%s。修复方式：安装 browser-control（cargo install browser-control-cli）并确保本机有 Chrome，"+
				"或用 [browser] binary_path 指定二进制路径后重启", reason)
	}

	// 一次委派 = 一个浏览器工作区。Director 委派时已带 boardID；
	// Coding 侧委派没有，需要自己生成一个，保证工作区不与其他委派串台。
	delegationID := blackboard.BoardIDFrom(ctx)
	if delegationID == "" {
		delegationID = browserctl.NewDelegationID()
	}
	ctx = browserctl.WithDelegationID(ctx, delegationID)

	cfg := DefaultExecutorConfig()
	if a.promptFmt != nil {
		cfg.SystemPrompt = a.promptFmt.FormatPrompt(browserPrompt)
	} else {
		cfg.SystemPrompt = browserPrompt
	}
	cfg.UserInput = input
	cfg.Adapters = a.Adapters
	cfg.LLM = a.LLM
	cfg.MaxSteps = a.maxSteps
	cfg.Publisher = a.Publisher
	cfg.AgentName = a.Name()
	cfg.StopOnFinish = true

	ec := a.ecCfg
	cfg.EnableContextCompression = ec.Enable && ec.EnableContextCompression
	cfg.ContextCompressionThreshold = ec.ContextCompressionThreshold
	cfg.ToolResultKeepTokens = ec.ToolResultKeepTokens

	// 一次委派一个 store：RebuildPrompt 保留全部用户输入，本实例又被 Director 与 Coding 共用。
	store := thinklink.NewStore(0)
	store.AddUserInput(input, 0)
	cfg.UltimateThinkLink = store
	cfg.UltimateKeepPlans = ec.UltimateCompressionKeepPlans // 0=全部保留
	cfg.EnableUltimateCompression = ec.EnableUltimateCompression

	result, err := RunAgentLoop(ctx, cfg)
	if err != nil {
		return AgentResult{}, err
	}
	return FinalizeResult(a.Name(), input, result), nil
}

// browserDelegateDescription 生成 delegate_browser 的工具描述。
// 能力不可用时必须给出"为什么不可用 + 怎么修"，否则 Director 会反复委派一个必然报错的工具。
func browserDelegateDescription(available bool, reason string) string {
	if available {
		return "Delegate a browser task to Browser-Agent, which drives a real Chrome browser: open pages, click/fill/wait, " +
			"read rendered text, run read-only page scripts, and capture screenshots as evidence. " +
			"Use it when the task needs JS-rendered content, an interactive flow, or visual verification — " +
			"not for static docs you can read with read-only tools or `run_bash curl`. " +
			"The task string must be self-contained: target URL, what to do, and what evidence to return."
	}
	return fmt.Sprintf("Browser-Agent is currently UNAVAILABLE and every call will fail: %s. "+
		"Do not delegate here. To enable it, install the browser-control CLI (cargo install browser-control-cli) "+
		"and ensure Chrome is installed, or set [browser] binary_path in the config and restart. "+
		"Until then, use read-only tools or another sub-agent, and tell the user the capability is unavailable.", reason)
}
