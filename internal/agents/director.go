package agents

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"yagent/internal/agents/activity"
	director "yagent/internal/agents/director"
	"yagent/internal/artifact"
	"yagent/internal/blackboard"
	"yagent/internal/compression"
	"yagent/internal/config"
	"yagent/internal/knowledge"
	"yagent/internal/llm"
	"yagent/internal/mcp"
	"yagent/internal/memory"
	"yagent/internal/thinklink"
	"yagent/internal/tools"
)

//go:embed director.prompt.md
var directorPrompt string

// maxNonDelegationPrompts 限制"未委派强制提醒"的最大次数。
// 当 director 未委派任何 agent 就打算以纯文本结束时，以用户角色注入强制消息；
// 若 LLM 持续拒绝委派，达到此上限后放行原内容，防止无限循环。
const maxNonDelegationPrompts = 3

// CustomAgent stores a dynamically designed agent created by Meta-Agent.
// Once registered, it becomes available as a permanent delegate tool.
// Phase 2d：改为 director.CustomAgent 的类型别名（去重：删除 agents 侧重复结构定义，
// 注册表/解析统一使用 director 子包类型；别名保持门面层既有引用与测试构造零改动）。
type CustomAgent = director.CustomAgent

// metaAgentResult 已迁移至 director 子包 types.go（P0-1 Phase 2d 去重：
// 删除 agents 侧重复定义，统一使用 director.MetaAgentResult）。

// ProjectContextFile / ProjectContextLoadResult 已迁移至 director 子包 types.go
// （P0-1 Phase 2a 去重：删除 agents 侧重复定义，统一使用 director 包类型）。

type DirectorAgent struct {
	BaseAgent
	RepoAgent      *RepoAgent
	CodingAgent    *CodingAgent
	ChatAgent      *ChatAgent
	MetaAgent      *MetaAgent
	DevOpsAgent    *DevOpsAgent
	BrowserAgent   *BrowserAgent
	promptFmt      PromptFormatter
	env            Env
	files          FileToolSet
	search         SearchToolSet
	sys            SysToolSet
	edit           EditToolSet
	flow           FlowToolSet
	thinker        Thinker
	microAgent     MicroAgentRunner
	deepThinker    DeepThinker
	guard          *tools.WorkspaceGuard
	repoCtx        RepoContextStore
	knowledge      KnowledgeProvider
	mcpClient      *mcp.MCPClient
	Adapters       []*tools.Adapter
	maxSteps       int
	metaRetryCount int                             // max retries for Meta-Agent JSON parse failures
	toolDefMap     map[string]tools.ToolDefinition // tool name → definition from tools.json
	metaHandler    *director.MetaAgentHandler      // Meta-Agent 动态设计的自定义 agent 注册表（Phase 2d 收敛至 director 子包）
	adapter        *DirectorAdapter                // 新旧整合适配器
	llmClient      *llm.Client                     // LLM客户端引用，用于运行时动态重新解析引擎

	projectCtxLoader *director.ProjectContextLoader // 项目上下文加载器（Phase 2a 抽取，缓存语义在 loader 内）

	currentMemory         *memory.ConversationMemory // 当前正在使用的 memory（Run 期间设置）
	pendingSubAgentMemory *AgentResult               // 最近一次 delegate 调用的完整结果（用于 memory 注入）
	// pendingSubAgentMu 保护 pendingSubAgentMemory 的并发读写：只读工具并发执行
	// （toolbatch 游程组）放开 delegate_* 并发后，多个 delegate 闭包会在各自
	// goroutine 内同时写入该字段（applyEnhancedCommander / executeCustomAgent /
	// delegate_meta）——无锁时为数据竞争。语义保持"最近一次写入胜出"（与串行
	// 时代一致；落账序仍由调用方按 tool_calls 原序保证）。
	pendingSubAgentMu sync.Mutex
	compressor        *compression.ContextCompressor // 上下文压缩编排组件（Phase 3-0 抽取）

	// injectSubAgentMemoryMu 保护 injectSubAgentMemory 对 a.currentMemory 的
	// append：并发 delegate 元素（toolbatch 只读游程组 goroutine）各自在
	// ToolRunner.Call 内执行注入，无锁时 slice append 为数据竞争。
	// 注入顺序 = Call 完成序（并发组内非确定性）——tool 消息/事件/rollout 的
	// 原序落账仍由 Planner/Executor 的 commit 阶段保证（summary 为 IsSubAgent
	// 记录性消息，不进入 LLM 上下文，顺序非确定可接受）。
	injectSubAgentMemoryMu sync.Mutex

	// LLM 兜底机制字段
	// P0-1 Phase 2b：步骤级重试次数、连续 LLM 失败计数、最近失败时间及
	// 熔断阈值/恢复时间配置副本已收编至 director.RecoveryHandler（经 a.adapter 访问），
	// 消除 DirectorAgent 上的残留失败计数状态；仅保留活跃使用的 llmTimeout。
	llmTimeout time.Duration // LLM调用超时，从配置读取，默认3分钟

	// delegate 子代理超时（活动感知空闲超时机制）
	delegateIdleTimeout  time.Duration // delegate 空闲超时（活动感知），0 已在构造时解析为派生值
	delegateTotalTimeout time.Duration // delegate 总时长上限，0=不限制

	// maxParallelReadOnlyTools 只读工具游程组最大并行数（构造时已归一化），
	// 用于 custom agent 的 ExecutorConfig（executeCustomAgent）。
	maxParallelReadOnlyTools int

	// EnhancedCommander 增强型配置
	EnhancedCommanderCfg config.EnhancedCommanderConfig
	// thinkLink 终极压缩支撑存储：实时记录用户原始输入与 Director 的 Thought & Plan 块，
	// 两级常规压缩不足时用于重建上下文（与 DirectorAgent 同生命周期，跨任务累积）
	thinkLink *thinklink.Store
	// taskID 当前任务的 taskID
	taskID string
	// boardID 黑板隔离域 ID（= taskID，由 SetTaskID 设置；构造时兜底生成 adhoc ID）
	boardID string
}

// loadProjectContext 读取工作区目录下的项目上下文文件（YAGENT.md、CLAUDE.md、AGENTS.md），
// 将成功读取的文件内容格式化后组合返回。文件按顺序尝试，不存在或读取失败时忽略。
// 返回加载的文件列表和组合后的内容。
// Phase 2a：加载与缓存逻辑已抽取至 director.ProjectContextLoader，此处仅薄委托，
// 保持原签名与缓存语义（同一实例会话内只加载一次），使 run() 等调用点零改动。
func (a *DirectorAgent) loadProjectContext() *director.ProjectContextLoadResult {
	return a.projectCtxLoader.Load()
}

// NewDirectorAgent 构造 DirectorAgent，仅声明其所需窄依赖：
// prompt 格式化、事件总线、环境视图（ProjectPath/FullYoloMode）、
// 文件/搜索/系统/编辑/流程工具集、认知/微代理/深度思考、
// 工作区守护、仓库上下文存储（写入）、知识注入器与 MCP 客户端；
// 保留既有 engine、六个子代理、maxSteps、disabledAgents、metaRetryCount、
// cfg config.Config 与 llmClient 参数。
func NewDirectorAgent(formatter PromptFormatter, publisher EventBus, env Env, files FileToolSet, search SearchToolSet, sys SysToolSet, edit EditToolSet, flow FlowToolSet, thinker Thinker, microAgent MicroAgentRunner, deepThinker DeepThinker, guard *tools.WorkspaceGuard, repoCtx RepoContextStore, knowledge KnowledgeProvider, mcpClient *mcp.MCPClient, engine llm.Engine, repo *RepoAgent, coding *CodingAgent, chat *ChatAgent, meta *MetaAgent, devops *DevOpsAgent, browser *BrowserAgent, maxSteps int, disabledAgents map[string]bool, metaRetryCount int, cfg config.Config, llmClient *llm.Client) *DirectorAgent {
	// self-reference for closures that need the DirectorAgent after construction
	var self *DirectorAgent

	// 只读标记依据（第二阶段并发 delegate 审计结论）：
	//   - delegate_repo：Repo-Agent 工具集为无共享写副作用工具（7 个基础只读工具全部
	//     命中第一阶段 auto 白名单 + 图查询三件套/认知工具等纯读或纯 LLM 推理）；子 agent
	//     内部循环自带原序调度，对 director 层无共享状态写入（pendingSubAgentMemory
	//     已经 pendingSubAgentMu 加锁）。
	//   - delegate_chat：Chat-Agent 工具集为 micro_agent/thinking/deepthinking（纯
	//     LLM 推理，无包级可变状态）+ agent_exit（flow-control，deny-list 兜底）+
	//     ask_user_for_help（交互豁免，并发谓词排除）。
	//   - delegate_coding/devops/browser/meta 不加标记：编码可写文件、devops 可执行
	//     shell、browser 可提交表单、meta 有自定义 agent 注册表副作用（deny-list 兜底）。
	delegateRepo := tools.NewAdapter("delegate_repo", "Delegate analysis task to Repo-Agent", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		packedTask, unresolved := self.resolveDelegateRefs(ctx, task, params)
		ctx = blackboard.WithBoardID(ctx, self.boardID)
		if rolloutWriter := self.createRolloutWriter("repo", task); rolloutWriter != nil {
			defer rolloutWriter.Close()
			ctx = memory.WithRolloutWriter(ctx, rolloutWriter)
		}
		result, err := repo.Run(ctx, packedTask)
		noteRef := self.writeDelegateNote("repo_agent", "delegate_repo", task, result, err)
		return self.applyEnhancedCommander("repo", task, result, err, noteRef, unresolved)
	}).WithSchema(buildDelegateSchema("The task description for Repo-Agent")).WithReadOnly(true)

	delegateCoding := tools.NewAdapter("delegate_coding", "Delegate coding task to Coding-Agent", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		packedTask, unresolved := self.resolveDelegateRefs(ctx, task, params)
		ctx = blackboard.WithBoardID(ctx, self.boardID)
		if rolloutWriter := self.createRolloutWriter("coding", task); rolloutWriter != nil {
			defer rolloutWriter.Close()
			ctx = memory.WithRolloutWriter(ctx, rolloutWriter)
		}
		result, err := coding.Run(ctx, packedTask)
		noteRef := self.writeDelegateNote("coding_agent", "delegate_coding", task, result, err)
		return self.applyEnhancedCommander("coding", task, result, err, noteRef, unresolved)
	}).WithSchema(buildDelegateSchema("The task description for Coding-Agent"))

	delegateChat := tools.NewAdapter("delegate_chat", "Delegate general conversation, explanation, or non-coding tasks to Chat-Agent", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		packedTask, unresolved := self.resolveDelegateRefs(ctx, task, params)
		ctx = blackboard.WithBoardID(ctx, self.boardID)
		if rolloutWriter := self.createRolloutWriter("chat", task); rolloutWriter != nil {
			defer rolloutWriter.Close()
			ctx = memory.WithRolloutWriter(ctx, rolloutWriter)
		}
		result, err := chat.Run(ctx, packedTask)
		noteRef := self.writeDelegateNote("chat_agent", "delegate_chat", task, result, err)
		return self.applyEnhancedCommander("chat", task, result, err, noteRef, unresolved)
	}).WithSchema(buildDelegateSchema("The message or question for Chat-Agent")).WithReadOnly(true)

	delegateDevOps := tools.NewAdapter("delegate_devops", "Delegate operational and system administration tasks to DevOps-Agent. DevOps-Agent can run shell commands, inspect files, check logs, manage processes, and perform any non-coding infrastructure work. Use this for tasks like checking disk usage, finding files, running diagnostics, inspecting configurations, or executing ad-hoc shell commands.", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		packedTask, unresolved := self.resolveDelegateRefs(ctx, task, params)
		ctx = blackboard.WithBoardID(ctx, self.boardID)
		if rolloutWriter := self.createRolloutWriter("devops", task); rolloutWriter != nil {
			defer rolloutWriter.Close()
			ctx = memory.WithRolloutWriter(ctx, rolloutWriter)
		}
		result, err := devops.Run(ctx, packedTask)
		noteRef := self.writeDelegateNote("devops_agent", "delegate_devops", task, result, err)
		return self.applyEnhancedCommander("devops", task, result, err, noteRef, unresolved)
	}).WithSchema(buildDelegateSchema("The operational task for DevOps-Agent, e.g., 'check disk usage', 'find all log files modified today', 'check if port 8080 is in use'."))

	delegateBrowser := tools.NewAdapter("delegate_browser",
		"Delegate browser automation tasks to Browser-Agent. Browser-Agent controls a headless Chrome browser using go-rod to navigate websites, click elements, fill forms, extract data, take screenshots, generate PDFs, execute JavaScript (with user confirmation), and manage cookies. Use this for tasks like: 'screenshot https://example.com', 'extract text from https://example.com/article', 'fill and submit the login form at https://example.com/login', 'check if website is reachable', 'get the current URL after navigation'. The agent handles all browser lifecycle and page management internally.",
		func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			task, ok := params["task"].(string)
			if !ok {
				return nil, fmt.Errorf("task parameter required")
			}
			packedTask, unresolved := self.resolveDelegateRefs(ctx, task, params)
			ctx = blackboard.WithBoardID(ctx, self.boardID)
			if rolloutWriter := self.createRolloutWriter("browser", task); rolloutWriter != nil {
				defer rolloutWriter.Close()
				ctx = memory.WithRolloutWriter(ctx, rolloutWriter)
			}
			result, err := browser.Run(ctx, packedTask)
			noteRef := self.writeDelegateNote("browser_agent", "delegate_browser", task, result, err)
			return self.applyEnhancedCommander("browser", task, result, err, noteRef, unresolved)
		}).WithSchema(buildDelegateSchema("The browser automation task for Browser-Agent, e.g., 'screenshot https://example.com homepage', 'extract article text from https://example.com/blog/post-1', 'fill the login form and submit', 'navigate to https://example.com and return the page title'."))

	delegateMeta := tools.NewAdapter("delegate_meta", "Delegate to Meta-Agent to DESIGN a custom specialized agent. Meta-Agent will craft a tailored system prompt using prompt engineering best practices and select appropriate tools. The designed agent is automatically registered and immediately executed to complete the task. After this, the new agent becomes a permanent delegate tool for future use.", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		ctx = blackboard.WithBoardID(ctx, self.boardID)
		if rolloutWriter := self.createRolloutWriter("meta", task); rolloutWriter != nil {
			defer rolloutWriter.Close()
			ctx = memory.WithRolloutWriter(ctx, rolloutWriter)
		}
		slog.Info("Director delegating to Meta-Agent (design)", "task", task)

		maxRetries := self.metaRetryCount
		var lastRawOutput string

		for attempt := 0; attempt < maxRetries; attempt++ {
			retryTask := task
			if attempt > 0 {
				retryTask = fmt.Sprintf(
					"%s\n\n[FORMAT CORRECTION — Attempt %d/%d]\nYour previous output was NOT valid JSON or missing required fields. You MUST output ONLY a valid JSON object with these exact top-level keys:\n{\n  \"thinking\": \"...\",\n  \"agent_name\": \"...\",\n  \"agent_design\": \"...\",\n  \"tools_used\": [...],\n  \"task_for_agent\": \"...\"\n}\n\nDo NOT wrap in markdown code fences (```). Do NOT include any text outside the JSON object.",
					task, attempt, maxRetries-1,
				)
			}

			metaResult, err := meta.Run(ctx, retryTask)
			if err != nil {
				return nil, fmt.Errorf("Meta-Agent design failed: %w", err)
			}
			self.setPendingSubAgentMemory(&metaResult)
			lastRawOutput = metaResult.Text

			systemPrompt, execResult, parseErr := director.ParseMetaAgentOutput(metaResult.Text)
			if parseErr != nil {
				slog.Warn("Meta-Agent JSON parse failed, retrying", "attempt", attempt+1, "maxRetries", maxRetries, "error", parseErr)
				continue
			}

			// ── Parse succeeded ──
			if execResult.AgentName != "" && systemPrompt != "" {
				snakeName := toSnakeCase(execResult.AgentName)
				customAgent := &CustomAgent{
					Name:         snakeName,
					DisplayName:  execResult.AgentName,
					SystemPrompt: systemPrompt,
					ToolsUsed:    execResult.ToolsUsed,
					Description:  fmt.Sprintf("Custom agent designed for: %s. Uses tools: %s.", execResult.AgentName, strings.Join(execResult.ToolsUsed, ", ")),
				}
				self.registerCustomAgent(customAgent)

				agentTask := execResult.TaskForAgent
				if agentTask == "" {
					agentTask = task
				}

				delegateName := "delegate_" + snakeName
				for _, ad := range self.Adapters {
					if ad.Name() == delegateName {
						slog.Info("Director executing newly designed agent", "delegate", delegateName, "display_name", execResult.AgentName)
						callResult, callErr := ad.Call(ctx, fmt.Sprintf(`{"task": %q}`, agentTask))
						if callErr != nil {
							return nil, fmt.Errorf("new agent %s execution failed: %w", execResult.AgentName, callErr)
						}
						var rawResult string
						if err := json.Unmarshal([]byte(callResult), &rawResult); err != nil {
							rawResult = callResult
						}
						formattedResult := fmt.Sprintf(
							"[Meta-Agent: Agent Designed and Executed]\nDesigned Agent: %s\nTools: %s\n\n[Execution Result]\n%s\n\n[New Agent Registered]\nA new specialized agent \"%s\" is now available via the `%s` tool for future tasks of this type.",
							execResult.AgentName,
							strings.Join(execResult.ToolsUsed, ", "),
							rawResult,
							execResult.AgentName,
							delegateName,
						)
						return formattedResult, nil
					}
				}
				return nil, fmt.Errorf("newly registered agent %s not found in adapters", delegateName)
			}

			return fmt.Sprintf("[Meta-Agent Design Result]\nAgent could not be registered (missing name or design). Raw output: %s", metaResult.Text), nil
		}

		slog.Warn("Meta-Agent JSON parse failed after all retries, returning raw output")
		return lastRawOutput, nil
	}).WithSchema(buildDelegateSchema("Detailed task description for Meta-Agent. Include: what needs to be accomplished, why existing agents are insufficient, and what the expected output format should be."))

	adapters := []*tools.Adapter{
		tools.NewAdapter("agent_exit", "Exit the agent with a reason. Use this when you are done — whether the task completed successfully, failed, needs clarification, or must be terminated. The reason must explain WHY the agent is exiting.", flow.ExecuteAgentExit).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"reason": map[string]interface{}{"type": "string", "description": "The reason the agent is exiting, e.g., task completed, cannot proceed, blocked by missing information, or must terminate."},
			},
			"required": []string{"reason"},
		}),
		// 委派返回物分级（第三批次）：read_artifact——按 ID 分页回读委派返回的
		// 完整 artifact 内容；id 来自 [Sub-Agent Result] 头部的 artifact: 字段，
		// 用于查看被截断结果的全文。只读工具，不触碰 workspace。
		newReadArtifactAdapter(),
		// 黑板 note 回读：按 noteRef 分页读取被裁剪的 note 全文。
		// 仅 Director 可见（与 read_artifact 对齐，不变式 I8）。
		newReadNoteAdapter(func() string { return self.boardID }),
	}

	var toolDefs []tools.ToolDefinition
	if err := json.Unmarshal(ToolsJSON, &toolDefs); err != nil {
		slog.Error("Failed to unmarshal tools", "error", err)
	}

	// Build a map from tool name to definition for later use by custom agents
	toolDefMap := make(map[string]tools.ToolDefinition, len(toolDefs))
	for _, def := range toolDefs {
		toolDefMap[def.Name] = def
	}

	for _, def := range toolDefs {
		var fn tools.ToolFunc
		switch def.Name {
		case "search_by_regex":
			fn = search.ExecuteGrepSearch
		case "list_dir":
			fn = files.ExecuteListDir
		case "read_file":
			fn = files.ExecuteReadFile
		case "print_dir_tree":
			fn = files.ExecutePrintDirTree
		case "deepthinking":
			fn = deepThinker.Execute
		case "ask_user_for_help":
			if env.FullYoloMode() {
				continue
			}
			fn = flow.ExecuteAskUserForHelp
		default:
			continue
		}

		adapter := tools.NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters).WithReadOnlyIfKnown()
		adapters = append(adapters, adapter)
	}

	// Conditionally register delegate tools based on disabledAgents
	var delegateAdapters []*tools.Adapter
	if !disabledAgents["repo"] {
		delegateAdapters = append(delegateAdapters, delegateRepo)
	}
	if !disabledAgents["coding"] {
		delegateAdapters = append(delegateAdapters, delegateCoding)
	}
	if !disabledAgents["chat"] {
		delegateAdapters = append(delegateAdapters, delegateChat)
	}
	if !disabledAgents["meta"] {
		delegateAdapters = append(delegateAdapters, delegateMeta)
	}
	if !disabledAgents["devops"] {
		delegateAdapters = append(delegateAdapters, delegateDevOps)
	}
	if !disabledAgents["browser"] {
		delegateAdapters = append(delegateAdapters, delegateBrowser)
	}

	// Set workspace guard on all adapters (delegate adapters are not dangerous tools)
	tools.SetGuardOnAdapters(adapters, guard)
	tools.SetGuardOnAdapters(delegateAdapters, guard)

	// 注册知识整理/维护工具（需要 llm engine + CodeSeekMCP）
	knowledgeAdapters := createKnowledgeToolAdapters(mcpClient, engine, "", "")
	var allAdapters []*tools.Adapter
	if len(knowledgeAdapters) > 0 {
		tools.SetGuardOnAdapters(knowledgeAdapters, guard)
		allAdapters = append(adapters, delegateAdapters...)
		allAdapters = append(allAdapters, knowledgeAdapters...)
	} else {
		allAdapters = append(adapters, delegateAdapters...)
	}

	// Strangler Fig: 创建适配器桥接层，开始使用新组件（Metrics + CircuitBreaker）
	adapterCfg := director.DefaultRecoveryConfig()
	adapterCfg.MaxRetries = maxSteps // 使用 maxSteps 作为重试次数
	adapterCfg.CircuitBreakerThreshold = cfg.LLM.CircuitBreakerThreshold
	adapterCfg.CircuitBreakerResetTimeout = cfg.LLM.CircuitBreakerResetTimeout
	adapterCfg.LLMRetries = cfg.LLM.StepRetries             // P0-1 Phase 2b 收编：LLM 步骤级重试次数入 handler（单一实例承载全部失败计数/熔断状态）
	directorAdapter := NewDirectorAdapter(true, adapterCfg) // enabled=true 启动 Metrics

	self = &DirectorAgent{
		BaseAgent:      BaseAgent{LLM: engine, Publisher: publisher},
		RepoAgent:      repo,
		CodingAgent:    coding,
		ChatAgent:      chat,
		MetaAgent:      meta,
		DevOpsAgent:    devops,
		BrowserAgent:   browser,
		promptFmt:      formatter,
		env:            env,
		files:          files,
		search:         search,
		sys:            sys,
		edit:           edit,
		flow:           flow,
		thinker:        thinker,
		microAgent:     microAgent,
		deepThinker:    deepThinker,
		guard:          guard,
		repoCtx:        repoCtx,
		knowledge:      knowledge,
		mcpClient:      mcpClient,
		Adapters:       allAdapters,
		maxSteps:       maxSteps,
		metaRetryCount: metaRetryCount,
		toolDefMap:     toolDefMap,
		metaHandler:    director.NewMetaAgentHandler(),
		adapter:        directorAdapter,
		llmClient:      llmClient,
		// Phase 2a：项目上下文加载器；projectPathFn 每次加载时动态求值，
		// 保持原实现读取 a.GlobalCtx.ProjectPath 的动态语义（运行时可通过 SetProjectPath 变更）
		projectCtxLoader: director.NewProjectContextLoader(func() string { return env.ProjectPath() }),

		// LLM 兜底机制配置（步骤重试/熔断阈值/失败计数已收编至 RecoveryHandler，见上方 adapterCfg 接线）
		llmTimeout: func() time.Duration {
			if cfg.LLM.Timeout > 0 {
				return cfg.LLM.Timeout
			}
			return 5 * time.Minute
		}(),

		// delegate 子代理超时（活动感知空闲超时机制）：idle 优先取显式配置，
		// 否则按 LLM 超时派生（max(10m, llmTimeout+5m)）；total 0=不限制
		delegateIdleTimeout: func() time.Duration {
			if cfg.Agent.DelegateIdleTimeout > 0 {
				return cfg.Agent.DelegateIdleTimeout
			}
			return config.DeriveDelegateIdleTimeout(cfg.LLM.Timeout)
		}(),
		delegateTotalTimeout: cfg.Agent.DelegateTotalTimeout, // 0=不限制

		// 只读工具游程组最大并行数：0/负=默认 4，>8 clamp 8，1=串行 kill-switch
		maxParallelReadOnlyTools: NormalizeMaxParallelReadOnlyTools(cfg.Agent.MaxParallelReadOnlyTools),

		// EnhancedCommander 配置
		EnhancedCommanderCfg: cfg.EnhancedCommander,

		// thinklink 存储：容量上限 0 = 使用包默认值（200 条）
		thinkLink: thinklink.NewStore(0),
	}

	// blackboard: adhoc 兜底——SetTaskID 未被调用时（理论上不应发生），
	// 生成一次性 boardID，保证 note 写入永远有合法目录。
	if self.boardID == "" {
		self.boardID = blackboard.GenerateAdhocID()
	}

	// Phase 3-0：上下文压缩编排组件；thinkLink 窄接口与 pendingSubAgentMemory
	// 清理回调注入。thinkLink 须与 a.thinkLink 同一实例（跨任务累积的条目必须
	// 被压缩重建看到）；agents 类型（AgentResult）经回调由门面适配清理。
	self.compressor = compression.NewContextCompressor(engine, self.Name(), self.thinkLink, func() { self.setPendingSubAgentMemory(nil) })

	// 委派返回物分级（第二批次接线）：注入项目路径提供者，FinalizeResult
	// 落盘 artifact 的 projectID 由此计算；进程级一次性注入（DirectorAgent 单例）。
	if self.env != nil {
		SetProjectPathProvider(self.env.ProjectPath)
	}

	// 计算并记录 Tool Definitions 哈希，用于验证 Prompt Cache 一致性
	toolDefsForHash := make([]llm.ToolDef, len(allAdapters))
	for i, ad := range allAdapters {
		toolDefsForHash[i] = ad.ToToolDef()
	}
	toolHash := tools.ComputeToolDefsHash(toolDefsForHash)
	names := make([]string, len(toolDefsForHash))
	for i, td := range toolDefsForHash {
		names[i] = td.Function.Name
	}
	slog.Info("Director tool definitions initialized",
		"hash", toolHash,
		"tool_count", len(toolDefsForHash),
		"tool_names", names)

	return self
}

func (a *DirectorAgent) Name() string {
	return "Director"
}

// refreshSubAgentEngines 从 llmClient 刷新所有子 Agent 的引擎，
// 确保 TUI 中切换模型（全局或针对特定 agent）后立即生效。
func (a *DirectorAgent) refreshSubAgentEngines() {
	if a.llmClient == nil {
		return
	}
	if a.RepoAgent != nil {
		if e := a.llmClient.GetAgentEngine("repo"); e != nil {
			a.RepoAgent.LLM = e
		}
	}
	if a.CodingAgent != nil {
		if e := a.llmClient.GetAgentEngine("coding"); e != nil {
			a.CodingAgent.LLM = e
		}
	}
	if a.ChatAgent != nil {
		if e := a.llmClient.GetAgentEngine("chat"); e != nil {
			a.ChatAgent.LLM = e
		}
	}
	if a.MetaAgent != nil {
		if e := a.llmClient.GetAgentEngine("meta"); e != nil {
			a.MetaAgent.LLM = e
		}
	}
	if a.DevOpsAgent != nil {
		if e := a.llmClient.GetAgentEngine("devops"); e != nil {
			a.DevOpsAgent.LLM = e
		}
	}
	if a.BrowserAgent != nil {
		if e := a.llmClient.GetAgentEngine("browser"); e != nil {
			a.BrowserAgent.LLM = e
		}
	}
}

// getToolFunc returns the ToolFunc implementation for a given tool name.
// This is used when constructing tool adapters for dynamically created agents.
func (a *DirectorAgent) getToolFunc(name string) tools.ToolFunc {
	switch name {
	case "read_file":
		return a.files.ExecuteReadFile
	case "search_replace_in_file":
		return a.edit.ExecuteReplaceBlock
	case "create_file":
		return a.files.ExecuteCreateFile
	case "run_bash":
		return a.sys.ExecuteRunBash
	case "search_by_regex":
		return a.search.ExecuteGrepSearch
	case "list_dir":
		return a.files.ExecuteListDir
	case "print_dir_tree":
		return a.files.ExecutePrintDirTree
	case "thinking":
		return func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			inputBytes, _ := json.Marshal(params)
			return a.thinker.Call(ctx, string(inputBytes))
		}
	case "micro_agent":
		return a.microAgent.Execute
	case "deepthinking":
		return a.deepThinker.Execute
	case "agent_exit":
		return a.flow.ExecuteAgentExit
	case "ask_user_for_help":
		if a.env.FullYoloMode() {
			return nil
		}
		return a.flow.ExecuteAskUserForHelp
	default:
		return nil
	}
}

// parseMetaAgentOutput / extractJSONObject 已迁移至 director 子包 meta_handler.go
// （P0-1 Phase 2d：控制流/错误文案逐字不变，统一返回 director.MetaAgentResult；
// 门面层调用 director.ParseMetaAgentOutput / director.ExtractJSONObject）。

// toSnakeCase converts a display name like "Security Auditor" to "security_auditor".
func toSnakeCase(name string) string {
	// Lowercase and replace non-alphanumeric characters with underscores
	var result strings.Builder
	for i, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			result.WriteRune(r)
		} else if r == ' ' || r == '-' || r == '_' {
			if i > 0 {
				result.WriteRune('_')
			}
		} else {
			result.WriteRune('_')
		}
	}
	// Trim leading/trailing underscores and collapse consecutive underscores
	raw := result.String()
	// Collapse consecutive underscores
	for strings.Contains(raw, "__") {
		raw = strings.ReplaceAll(raw, "__", "_")
	}
	raw = strings.Trim(raw, "_")
	if raw == "" {
		raw = "custom_agent"
	}
	return raw
}

// registerCustomAgent creates a new delegate_<name> tool for a custom agent designed by Meta-Agent
// and adds it to the Director's Adapters list. The agent becomes permanently available.
func (a *DirectorAgent) registerCustomAgent(ca *CustomAgent) {
	delegateName := "delegate_" + ca.Name

	// Check if already registered（Phase 2d：防重复检查+写入收敛至 director.MetaAgentHandler.Register，
	// 重复时返回 ErrAlreadyRegistered 且原条目不覆盖，与原检查语义一致；日志文案与跳过接线行为不变）
	if err := a.metaHandler.Register(ca); err != nil {
		slog.Info("Custom agent already registered", "name", delegateName)
		return
	}

	// Build tool adapters for the custom agent's selected tools
	customAdapters := make([]*tools.Adapter, 0, len(ca.ToolsUsed))
	for _, toolName := range ca.ToolsUsed {
		fn := a.getToolFunc(toolName)
		if fn == nil {
			slog.Warn("Custom agent references unknown tool", "agent", ca.Name, "tool", toolName)
			continue
		}
		def, ok := a.toolDefMap[toolName]
		if !ok {
			slog.Warn("Tool definition not found in toolDefMap", "tool", toolName)
			continue
		}
		adapter := tools.NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters).WithReadOnlyIfKnown()
		customAdapters = append(customAdapters, adapter)
	}

	// Add agent_exit tool so the custom agent can signal exit
	finishDef, ok := a.toolDefMap["agent_exit"]
	if ok {
		fn := a.getToolFunc("agent_exit")
		adapter := tools.NewAdapter("agent_exit", finishDef.Description, fn).WithSchema(finishDef.Parameters)
		customAdapters = append(customAdapters, adapter)
	}

	// Set workspace guard on the custom agent's adapters
	tools.SetGuardOnAdapters(customAdapters, a.guard)

	// Create the delegate tool that executes the custom agent
	// Capture ca and customAdapters in closure
	agentRef := ca
	adaptersRef := customAdapters

	description := fmt.Sprintf("Delegate to %s — a custom specialized agent designed by Meta-Agent. %s",
		ca.DisplayName, ca.Description)

	delegateAdapter := tools.NewAdapter(delegateName, description,
		func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			task, ok := params["task"].(string)
			if !ok {
				return nil, fmt.Errorf("task parameter required")
			}
			packedTask, unresolved := a.resolveDelegateRefs(ctx, task, params)
			ctx = blackboard.WithBoardID(ctx, a.boardID)
			resultStr, err := a.executeCustomAgent(ctx, agentRef, adaptersRef, packedTask)
			// executeCustomAgent 内部已调用 setPendingSubAgentMemory；取出以写 note 和统一格式化
			agentResult := a.takePendingSubAgentMemory()
			if agentResult == nil {
				agentResult = &AgentResult{Text: resultStr, Summary: resultStr}
			}
			noteRef := a.writeDelegateNote(agentRef.Name+"_agent", delegateName, task, *agentResult, err)
			if err != nil {
				return "", err
			}
			return FormatForDirector(agentRef.Name, *agentResult, noteRef, unresolved), nil
		}).WithSchema(buildDelegateSchema("The task description for " + ca.DisplayName))

	a.Adapters = append(a.Adapters, delegateAdapter)

	slog.Info("Custom agent registered", "delegate_name", delegateName, "display_name", ca.DisplayName, "tools", ca.ToolsUsed)
}

// executeCustomAgent runs a custom agent with its designed system prompt and selected tools.
// Uses the unified AgentExecutor.
func (a *DirectorAgent) executeCustomAgent(ctx context.Context, ca *CustomAgent, adapters []*tools.Adapter, task string) (string, error) {
	systemPrompt := a.promptFmt.FormatPrompt(ca.SystemPrompt)

	cfg := DefaultExecutorConfig()
	cfg.MaxParallelReadOnlyTools = a.maxParallelReadOnlyTools
	cfg.SystemPrompt = systemPrompt
	cfg.UserInput = task
	cfg.Adapters = adapters
	cfg.LLM = a.LLM
	cfg.MaxSteps = 15
	cfg.Publisher = a.Publisher
	cfg.AgentName = ca.DisplayName
	cfg.StopOnFinish = true
	cfg.LLMTimeout = a.llmTimeout
	// EnableCollaboration 已默认 true

	// 创建 Rollout Writer 并注入到 context
	if rolloutWriter := a.createRolloutWriter(ca.DisplayName, task); rolloutWriter != nil {
		defer rolloutWriter.Close()
		ctx = memory.WithRolloutWriter(ctx, rolloutWriter)
	}

	result, err := RunAgentLoop(ctx, cfg)
	if err != nil {
		return "", err
	}
	// 存储自定义 agent memory 供 Run 方法注入
	agentResult := AgentResult{
		Text:   result.Text,
		Memory: ConvertLLMHistoryToMemory(result.History),
	}
	a.setPendingSubAgentMemory(&agentResult)
	return result.Text, nil
}

// injectSubAgentMemory 将 sub-agent 的执行结果摘要注入到 Director memory 中
// Phase 1: 只注入摘要，不再注入 sub-agent 的完整对话历史
// Phase 3+ : Sub-agent 的关键发现通过 SharedMemory 发布/订阅机制共享
func (a *DirectorAgent) injectSubAgentMemory(result AgentResult, toolCallID string, toolName string) {
	// 并发 delegate 注入保护（字段注释详述）：整个函数体（读 a.currentMemory 判空
	// + summaryMsg append）都在锁内，消费共享 ConversationMemory。
	a.injectSubAgentMemoryMu.Lock()
	defer a.injectSubAgentMemoryMu.Unlock()

	if a.currentMemory == nil {
		return
	}

	// 只注入摘要消息（FormatForDirector 统一格式：无 ArtifactRef 时与旧格式
	// "[Sub-Agent Result: {toolName}]\n{Text}" 等价；有 ArtifactRef 时为分级
	// 摘要 + artifact 元信息 + read_artifact 取回指令），不注入完整历史。
	// sub-agent 的完整对话历史保留在其自身的 LocalMemory 中（Phase 3）
	if result.Text != "" {
		metadata := map[string]interface{}{
			"type":      "sub_agent_summary",
			"tool":      toolName,
			"msg_count": len(result.Memory),
		}
		if result.ArtifactRef != nil {
			metadata["artifact_id"] = result.ArtifactRef.ID
		}
		summaryMsg := memory.ChatMessage{
			Type:       memory.MessageTypeAssistant,
			Content:    FormatForDirector(toolName, result, "", 0),
			Timestamp:  time.Now(),
			GroupID:    fmt.Sprintf("%s_summary_%d", toolName, time.Now().UnixNano()),
			ParentID:   toolCallID,
			IsSubAgent: true,
			Metadata:   metadata,
		}
		a.currentMemory.Messages = append(a.currentMemory.Messages, summaryMsg)
	}

	// 重要：result.Memory（sub-agent 的完整对话历史）不再注入到 Director 的 memory 中
	// 这避免了 Director 上下文快速膨胀和 Compact Engine 频繁压缩造成的信息丢失
	// sub-agent 内部消息保留在 sub-agent 本地，通过 SharedMemory 的 publish/subscribe 机制共享关键信息（Phase 3）
}

// SetTaskID 设置当前任务的 taskID，同时派生 boardID（黑板隔离域）。
func (a *DirectorAgent) SetTaskID(taskID string) {
	a.taskID = taskID
	a.boardID = blackboard.SanitizeBoardID(taskID)
}

// createRolloutWriter 为 delegate 创建 Rollout 写入器
// 返回 nil 表示创建失败（失败时仅警告，不阻断执行）
func (a *DirectorAgent) createRolloutWriter(agentName, task string) *memory.RolloutWriter {
	// 计算 projectID
	projectID := a.computeProjectID()

	writer, err := memory.NewRolloutWriter(agentName, a.taskID, projectID)
	if err != nil {
		slog.Warn("Rollout: failed to create writer, continuing without rollout logging",
			"agent", agentName,
			"error", err,
		)
		return nil
	}

	slog.Debug("Rollout: writer created for delegate agent",
		"agent", agentName,
		"file", writer.FilePath(),
	)

	return writer
}

// computeProjectID 从项目路径计算文件系统安全的 projectID
// Phase 2a：计算逻辑已抽取至 director.ComputeProjectID，此处仅薄委托。
func (a *DirectorAgent) computeProjectID() string {
	return director.ComputeProjectID(a.env.ProjectPath())
}

// setPendingSubAgentMemory 锁保护写入 pendingSubAgentMemory（并发 delegate 闭包
// 各自 goroutine 内调用；语义与串行时代一致：最近一次写入胜出）。
func (a *DirectorAgent) setPendingSubAgentMemory(r *AgentResult) {
	a.pendingSubAgentMu.Lock()
	defer a.pendingSubAgentMu.Unlock()
	a.pendingSubAgentMemory = r
}

// takePendingSubAgentMemory 锁保护取出并清空 pendingSubAgentMemory（原
// "读取 → 注入 → 置 nil"三步的原子复合操作，防止与并发写入交叠）。
func (a *DirectorAgent) takePendingSubAgentMemory() *AgentResult {
	a.pendingSubAgentMu.Lock()
	defer a.pendingSubAgentMu.Unlock()
	r := a.pendingSubAgentMemory
	a.pendingSubAgentMemory = nil
	return r
}

// applyEnhancedCommander 处理子 Agent 执行结果。
// 存储 sub-agent memory，并返回 FormatForDirector 统一格式文本（第二批次接线：
// LLM 上下文消费点走分级摘要 + artifact 取回指令，不再原样返回完整结果文本）。
// agentType: 子 Agent 类型（如 "repo", "coding"）
// task: 委派的任务描述
// result: Agent 执行结果
// err: Agent 执行错误
// noteRef: 黑板 note 引用（空=无 note，输出与旧格式一致）
// unresolvedCount: context_refs 中未能解析的引用数
// 返回: 处理后的结果文本和错误
func (a *DirectorAgent) applyEnhancedCommander(
	agentType string,
	task string,
	result AgentResult,
	err error,
	noteRef string,
	unresolvedCount int,
) (string, error) {
	// 始终存储 sub-agent memory（保持现有行为；并发 delegate 闭包路径经锁保护）
	a.setPendingSubAgentMemory(&result)

	if err != nil {
		return "", err
	}

	// LLM 上下文消费点：统一走 FormatForDirector（无 ArtifactRef 时与旧格式
	// "[Sub-Agent Result: {agentType}]\n{Text}" 等价）。
	return FormatForDirector(agentType, result, noteRef, unresolvedCount), nil
}

// ── blackboard helpers ──────────────────────────────────────────────────────

// blackboardEnabled 返回黑板功能是否启用（kill-switch：YAGENT_BLACKBOARD=0 禁用）。
func blackboardEnabled() bool {
	return os.Getenv("YAGENT_BLACKBOARD") != "0"
}

// parseContextRefs 从 delegate params 中提取 context_refs 字符串数组。
func parseContextRefs(raw interface{}) []string {
	arr, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	refs := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			refs = append(refs, s)
		}
	}
	return refs
}

// buildDelegateSchema 构建 delegate 工具的 JSON Schema（含可选 context_refs）。
// 所有 delegate（标准 + 自定义）共用，保证 schema 一致性。
func buildDelegateSchema(taskDesc string) map[string]interface{} {
	return map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{"type": "string", "description": taskDesc},
			"context_refs": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Optional references to prior blackboard notes or artifacts. Formats: 'agent:<name>' (latest notes from agent), 'note:<agent>/<stem>' (specific note), 'artifact:<id>' (existing artifact).",
			},
		},
		"required": []string{"task"},
	}
}

// writeDelegateNote 将 delegate 执行结果写入黑板 note，返回 noteRef。
// 写入失败仅 slog.Warn，不阻断主流程（不变式 I5）。
func (a *DirectorAgent) writeDelegateNote(agentName, toolName, task string, result AgentResult, runErr error) string {
	if !blackboardEnabled() || a.boardID == "" {
		return ""
	}
	status := "ok"
	if runErr != nil {
		status = "error"
	}
	var artifactIDs []string
	if result.ArtifactRef != nil {
		artifactIDs = []string{result.ArtifactRef.ID}
	}
	// body 规则：有 artifact → 摘要（不复制全文）；无 artifact → 完整文本
	body := result.Text
	if result.ArtifactRef != nil {
		body = result.Summary
	}
	noteRef, err := blackboard.WriteNote(blackboard.NoteInput{
		BoardID:     a.boardID,
		Agent:       agentName,
		Tool:        toolName,
		Task:        task,
		Summary:     result.Summary,
		Body:        body,
		ArtifactIDs: artifactIDs,
		Status:      status,
	})
	if err != nil {
		slog.Warn("blackboard: failed to write note", "agent", agentName, "error", err)
		return ""
	}
	slog.Debug("blackboard: note written", "agent", agentName, "noteRef", noteRef, "boardID", a.boardID)
	return noteRef
}

// resolveDelegateRefs 解析 context_refs 并组装 Context Pack，返回拼接后的 task 和未解析数。
func (a *DirectorAgent) resolveDelegateRefs(ctx context.Context, task string, params map[string]interface{}) (packedTask string, unresolvedCount int) {
	if !blackboardEnabled() {
		return task, 0
	}
	refs := parseContextRefs(params["context_refs"])
	if len(refs) == 0 {
		return task, 0
	}
	pack := blackboard.ResolveRefs(a.boardID, refs, a.artifactExists)
	unresolvedCount = len(pack.Unresolved)
	packText := pack.Text()
	if packText == "" {
		return task, unresolvedCount
	}
	slog.Debug("blackboard: context pack assembled", "refs", len(refs), "entries", len(pack.Entries), "unresolved", unresolvedCount, "packChars", len(packText))
	return packText + "\n---\n" + task, unresolvedCount
}

// artifactExists 检查 artifact 是否存在（供 blackboard.ResolveRefs 回调）。
func (a *DirectorAgent) artifactExists(id string) bool {
	projectID := a.computeProjectID()
	_, err := artifact.DefaultStore().Load(projectID, id)
	return err == nil
}

func convertToolCalls(tcs []llm.ToolCall) []memory.ToolCallData {
	var res []memory.ToolCallData
	for _, tc := range tcs {
		res = append(res, memory.ToolCallData{
			ID:   tc.ID,
			Type: tc.Type,
			Function: memory.ToolCallFunction{
				Name:      tc.Function.Name,
				Arguments: json.RawMessage(tc.Function.Arguments),
			},
		})
	}
	return res
}

// 编译期断言：DirectorAgent 实现标准 Agent 接口（types.go）
var _ Agent = (*DirectorAgent)(nil)

// 编译期引用：P0-1 Phase 3-1 Planner 脚手架（director 子包），确保组件类型
// 进入编译覆盖（Phase 3-2 填充实现并切换 run() 调用点后此引用移除）。
var _ = director.NewPlanner

// Run 实现标准 Agent 接口。会话 memory 通过 context 注入
// （memory.WithConversationMemory，与 WithRolloutWriter 同模式）；
// 未注入时 mem 为 nil，沿用旧 Run 对 mem==nil 的既有语义：视为新会话（不新建 ConversationMemory）。
// 错误路径与旧 Run 逐字一致：run 的所有错误分支均返回 ("", err)，
// 因此 AgentResult.Text 为空串，err 原样上抛。
func (a *DirectorAgent) Run(ctx context.Context, input string) (AgentResult, error) {
	mem, _ := memory.GetConversationMemory(ctx)

	text, err := a.run(ctx, input, mem)

	var memHistory []memory.ChatMessage
	if mem != nil {
		memHistory = mem.GetMessages()
	}
	return AgentResult{Text: text, Memory: memHistory}, err
}

// ─── P0-1 Phase 3-2b：门面 ToolRunner 适配 + run() 主循环接线 Planner ─────────

// toSubAgentMemory 将 agents.AgentResult 转换为 memory.SubAgentMemory。
// director 包禁止 import agents（AgentResult 不进入子包），类型转换在门面完成；
// Phase 3-2b Tools 适配闭包调用（解析结果填 per-run 共享 state）。
func toSubAgentMemory(result *AgentResult) *memory.SubAgentMemory {
	if result == nil {
		return nil
	}
	// result.Memory 已是 []memory.ChatMessage（原 director.ChatMessage 为其
	// 字段子集，类型下沉后删除重复定义），直接复用，不再逐条转换。
	return &memory.SubAgentMemory{Text: result.Text, Memory: result.Memory}
}

// directorToolRunner 门面 ToolRunner 适配器（实现 director.ToolRunner）。
// 包装原 run() 工具分发段：a.Adapters 按名查找、delegate_* 活动感知空闲超时 /
// 交互式工具无限等待/LogDelegateCall 委派日志、delegate_repo 结果 JSON 解包
// （RepoSummary 更新）、delegate 检测统计与子 Agent 记忆注入（更新共享 *RunState）。
// tool_call_start/tool_call_result 事件与 not-found 兜底由 Planner 侧负责。
type directorToolRunner struct {
	agent *DirectorAgent
	state *director.RunState
}

// Specs 返回全部工具定义（原 a.Adapters 逐个 ToToolDef；排序由 Planner 统一执行，
// 与原 run() 的 tools.SortToolDefs 时点一致，确保 LLM tools 参数确定性）。
func (r *directorToolRunner) Specs() []llm.ToolDef {
	defs := make([]llm.ToolDef, len(r.agent.Adapters))
	for i, ad := range r.agent.Adapters {
		defs[i] = ad.ToToolDef()
	}
	return defs
}

// Call 按工具名调用工具（原 run() 工具分发段整段搬迁，控制流逐字等价）。
// ctx 已由 Planner 在调用前检查（ctx.Err() 非空时 Planner 直接终止并写 turn_aborted）。
func (r *directorToolRunner) Call(ctx context.Context, name string, argsJSON string) (string, error) {
	a := r.agent

	for _, t := range a.Adapters {
		if t.Name() != name {
			continue
		}

		// Log delegate tool calls with full arguments to dedicated delegate log
		if strings.HasPrefix(t.Name(), "delegate_") {
			agentName := strings.TrimPrefix(t.Name(), "delegate_")
			LogDelegateCall(t.Name(), agentName, argsJSON)
		}

		// 为工具调用添加超时保护（防止非交互工具无限阻塞），按工具类型分派：
		// ask_user_for_help 无限等待（仅 WithCancel）/ delegate_* 活动感知空闲
		// 超时 / 普通工具 120s 固定超时（详见下方 switch 分支说明）。
		toolTimeout := 120 * time.Second
		isDelegate := strings.HasPrefix(name, "delegate_")
		cancelCtx, cancelCtxCancel := context.WithCancel(ctx)
		toolCtx := cancelCtx
		toolCancel := cancelCtxCancel
		switch {
		case isInteractiveUserTool(name):
			// ask_user_for_help：无限等待用户响应，仅保留 WithCancel（不变）。
			// 不能加 deadline，否则用户尚未响应调用就会被 context.DeadlineExceeded
			// 自动取消；任务中止时仍可经父 context 取消打断等待。
		case isDelegate:
			// delegate_*：活动感知空闲超时——子代理有动作（LLM 推理、工具执行，
			// 经 executor.go 心跳埋点上报）就重置计时器，持续无动作超过 idle 阈值
			// 才取消；可选总上限兜底（0=不限制）。废除原硬编码 600s 总时长上限
			// （长任务必然被误杀）。嵌套 delegate 时内层心跳沿 parent 链上抛续期外层。
			// 经 WithCancel 剥离父 context 的 deadline，确保 idle 计时不受父
			// context 剩余时间限制。
			toolCtx, toolCancel = activity.WithIdleTimeout(cancelCtx, activity.Options{
				IdleTimeout:  a.delegateIdleTimeout,
				TotalTimeout: a.delegateTotalTimeout,
			})
		default:
			// 普通工具：保持 120s 固定超时（不变，内部无心跳源）
			toolCtx, toolCancel = context.WithTimeout(cancelCtx, toolTimeout)
		}
		toolResult, err := t.Call(toolCtx, argsJSON)
		cancelCtxCancel()
		toolCancel()

		// 注入 sub-agent memory（delegate 闭包中设置了 pendingSubAgentMemory）。
		// 原 run() 以 LLM 返回的 tool_call_id 作 ParentID；ToolRunner.Call 签名
		// （ctx, name, argsJSON）不含 toolCallID，此处传空串（ParentID 为记录性
		// 字段且无消费逻辑；IsSubAgent 消息不进入 LLM 上下文，差异详见阶段报告）。
		// take(读+清) 原子复合：并发 delegate 闭包写同一字段经锁保护。
		if psm := a.takePendingSubAgentMemory(); psm != nil {
			a.injectSubAgentMemory(*psm, "", name)
			// 解析结果填 per-run 共享 state（门面负责 AgentResult → SubAgentMemory 转换）
			// 并发 delegate 元素在各自 goroutine 内 Call，state 写点经 RunState 内部锁保护
			r.state.SetPendingSubAgentMemory(toSubAgentMemory(psm))
		}

		// 检测是否是 delegate 工具，无论成功失败都记录尝试次数（更新共享 state）
		// 并发 delegate 元素（toolbatch 只读游程组 goroutine）并发计数 → 锁保护
		if strings.HasPrefix(t.Name(), "delegate_") {
			r.state.RecordDelegation(err == nil)
		}

		if err != nil {
			// 超时判定以 toolCtx 的 cause 为权威来源（context.Cause，不依赖子代理
			// 错误包装链）：delegate_* 的活动感知空闲/总时长超时转换为带原因的
			// 格式化错误（Planner 收到后走 "Error: %s" 分支）；普通工具保持 120s
			// 固定超时原行为（DeadlineExceeded → 带秒数提示，Planner 不会以
			// cfg.ToolTimeout 格式化，与原 run() 的 per-tool 超时提示一致）。
			// 非超时错误原样上抛，由 Planner 做 1000 字符截断与 "Error: " 前缀
			// 包装（与原行为一致）。
			cause := context.Cause(toolCtx)
			var idleErr *activity.IdleTimeoutError
			var totalErr *activity.TotalTimeoutError
			switch {
			case errors.As(cause, &idleErr):
				// 活动感知空闲超时：子代理持续无动作（最后动作见消息）
				return "", fmt.Errorf("tool execution timed out (idle): %w", idleErr)
			case errors.As(cause, &totalErr):
				// 总时长上限兜底触发
				return "", fmt.Errorf("tool execution timed out (total): %w", totalErr)
			case errors.Is(err, context.DeadlineExceeded):
				// 普通工具 120s 固定超时路径（保持原行为）
				return "", fmt.Errorf("tool execution timed out after %d seconds", int(toolTimeout.Seconds()))
			}
			return "", err
		} else if t.Name() == "delegate_repo" {
			// 第二批次接线：toolResult 现为 FormatForDirector 包装文本（分级摘要 +
			// artifact 取回指令）的 JSON 编码，不再是裸的 repo 输出。RepoSummary
			//（注入 coding/browser agent 系统提示的程序化消费）应拿完整输出而非
			// 包装文本，故改从 state.PendingSubAgentMemory（上方注入段刚存储的
			// 本次 delegate 完整结果，repo 用 FinalizeResultFull，Text 恒为完整
			// 输出）取 .Text；nil 时回退旧的 toolResult JSON 解包逻辑（防御保留）。
			if psm := r.state.PendingSubAgentMemory; psm != nil {
				a.repoCtx.Set(psm.Text)
			} else {
				var summary string
				if uerr := json.Unmarshal([]byte(toolResult), &summary); uerr == nil {
					a.repoCtx.Set(summary)
				} else {
					a.repoCtx.Set(toolResult)
				}
			}
		}
		return toolResult, nil
	}
	// 防御性兜底（Planner 侧按 Specs() 名单做 not-found 处理，正常不会到达）
	return fmt.Sprintf("Tool %s not found", name), nil
}

// run 执行 Director 主任务循环。P0-1 Phase 3-2b：主循环（LLM 对话循环：系统提示
// 构建 → 多步 LLM 调用 → 工具执行 → 上下文压缩 → 收尾）已搬迁至 director.Planner.Run
// （planner_run.go，lift-and-shift 自旧实现），本门面方法职责收窄为依赖装配与
// 门面专属职责：
//   - currentMemory 设置/清理（delegate 闭包与 injectSubAgentMemory 依赖）；
//   - llmClient 引擎刷新（TUI 切换模型后 director 与子 Agent 引擎立即生效）；
//   - per-run RunState 创建（与 ToolRunner 适配器共享指针）；
//   - Prompts 闭包（系统提示构建段包装）与 directorToolRunner 适配；
//   - rollout writer 创建（Planner 经 cfg.Rollout 接管 session_meta/turn_context/
//     消息/事件写入与 Close）。
//
// 对外行为等价：返回值、事件序列、memory/thinklink 写入、rollout 输出与旧实现
// 一致（characterization/fullchain 测试为验收线）。
func (a *DirectorAgent) run(ctx context.Context, input string, mem *memory.ConversationMemory) (string, error) {
	// 设置当前 memory（delegate 闭包通过 a.currentMemory 访问）
	a.currentMemory = mem
	defer func() { a.currentMemory = nil }()

	// 从 llmClient 刷新引擎，确保 TUI 中切换模型后立即生效
	if a.llmClient != nil {
		newEngine := a.llmClient.GetAgentEngine("director")
		if newEngine != nil {
			a.LLM = newEngine
		}
		// 刷新所有子 Agent 的引擎，确保 TUI 切换模型后子 Agent 也使用新模型
		a.refreshSubAgentEngines()
	}

	// per-run 规划状态（Planner 与 Tools 适配闭包共享指针；HasDelegated/
	// NonDelegationPrompts 在 Planner.Run 开头重置，delegationAttempts 由 state
	// 承载 per-task 语义）
	state := &director.RunState{
		MaxSteps:              a.maxSteps,
		HasDelegated:          false,
		NonDelegationPrompts:  0,
		DelegationAttempts:    0,
		PendingSubAgentMemory: nil,
	}

	// Prompts 闭包：包装原系统提示构建段（GlobalCtx.FormatPrompt + 仅首次对话
	// loadProjectContext + metaHandler.List() 自定义 Agent 列举 + KnowledgeInjector.Inject
	// + context_loaded 事件 + projectContext 延迟追加）。注入失败忽略错误继续
	// （与原行为一致：Inject 失败不追加 block）；FirstConversation 判定由 Planner
	// 按 mem == nil || len(mem.GetMessages()) == 0 计算传入，与原逻辑一致。
	prompts := func(ctx context.Context, in director.PromptInput) (string, error) {
		// Always start with System Prompt (with any registered custom agents appended)
		systemPrompt := a.promptFmt.FormatPrompt(directorPrompt)
		var projectContext string
		// 只在首次对话时加载项目上下文文件（YAGENT.md、CLAUDE.md、AGENTS.md），
		// 同一会话的后续追问无需重复注入，避免浪费 token。
		// memory 中不存储 system 消息，因此 len(mem.GetMessages()) == 0 即可判断是否为首次对话。
		if in.FirstConversation {
			if loadResult := a.loadProjectContext(); loadResult != nil && loadResult.Content != "" {
				// 发送上下文加载完成消息到消息通道
				if a.Publisher != nil {
					a.Publisher.Publish("context_loaded", loadResult, a.Name())
				}
				// 延迟追加：先构建完整的 system prompt（静态前缀 + 环境信息 + 自定义 Agent），
				// 最后才追加项目上下文，确保静态前缀可被 LLM Prompt Cache 复用
				projectContext = fmt.Sprintf("\n\n### Project Workspace Context\n%s\n", loadResult.Content)
			}
		}

		// 自定义 Agent 描述（Phase 2d：director.MetaAgentHandler.List()，按注册序
		// 确定性返回，利于 LLM Prompt Cache 复用）
		if customAgents := a.metaHandler.List(); len(customAgents) > 0 {
			systemPrompt += "\n\n### Custom Agents\nThe following specialized agents have been designed by Meta-Agent and are permanently available for delegation:\n\n"
			for _, ca := range customAgents {
				systemPrompt += fmt.Sprintf("- **%s** (`delegate_%s`): %s\n", ca.DisplayName, ca.Name, ca.Description)
			}
			systemPrompt += "\nUse these agents via their delegate tools for tasks matching their specializations.\n"
		}

		// 追加项目上下文（放在所有静态内容之后，确保缓存命中率）
		if projectContext != "" {
			systemPrompt += projectContext
		}

		// [知识管理] Director 自身 systemPrompt 动态知识检索注入
		if a.knowledge != nil {
			injCtx := knowledge.InjectionContext{
				UserMessage: in.Input,
				TargetFiles: nil,
				AgentName:   a.Name(),
				// Domains 为空 = 检索全部 domain（Director 不限定）
			}
			if knowledgeBlock, err := a.knowledge.Inject(ctx, injCtx); err == nil && knowledgeBlock != "" {
				systemPrompt += knowledgeBlock
			}
		}
		return systemPrompt, nil
	}

	// ═══════ 初始化 Director Rollout Writer（创建留在门面，写入由 Planner 接管） ═══════
	var directorRolloutWriter *memory.RolloutWriter
	if rw := a.createRolloutWriter("director", input); rw != nil {
		directorRolloutWriter = rw
		defer func() {
			if directorRolloutWriter != nil {
				directorRolloutWriter.Close()
			}
		}()
	}

	// ToolRunner 适配：包装 a.Adapters 查找/超时/委派统计/子 Agent 记忆注入（共享 state）
	toolRunner := &directorToolRunner{agent: a, state: state}

	// Recovery/Metrics 经 DirectorAdapter 提取（构造时恒非 nil；防御 nil 时 Planner
	// 侧按"无熔断/0 次重试/跳过指标"处理，与原 if a.adapter != nil 语义一致）
	var recovery *director.RecoveryHandler
	var metrics *director.MetricsCollector
	if a.adapter != nil {
		recovery = a.adapter.recovery
		metrics = a.adapter.metrics
	}

	cfg := director.PlannerConfig{
		// llmClient 刷新后的引擎（llm.Engine 方法集包含 GenerateContent/Model，
		// 天然满足 director.LLMClient；与原 run() 使用刷新后 a.LLM 的行为一致）
		LLM:                       a.LLM,
		Publisher:                 a.Publisher,
		Journal:                   a.thinkLink,
		Rollout:                   directorRolloutWriter,
		Tools:                     toolRunner,
		Prompts:                   prompts,
		Compressor:                a.compressor,
		MaxSteps:                  a.maxSteps,
		LLMTimeout:                a.llmTimeout,
		Recovery:                  recovery,
		Metrics:                   metrics,
		NormalizeMessages:         validateAndRepairToolCallPairs,
		EstimateTokensFn:          EstimateTokens,
		ConvertToolCallsFn:        convertToolCalls,
		CompressEnable:            a.EnhancedCommanderCfg.Enable && a.EnhancedCommanderCfg.EnableContextCompression,
		CompressThreshold:         a.EnhancedCommanderCfg.ContextCompressionThreshold,
		CompressKeepTokens:        a.EnhancedCommanderCfg.ToolResultKeepTokens,
		UltimateCompressEnable:    a.EnhancedCommanderCfg.EnableUltimateCompression,
		UltimateCompressKeepPlans: a.EnhancedCommanderCfg.UltimateCompressionKeepPlans,
		// 普通工具调用超时（120s）；超时保护由 directorToolRunner 执行，Planner 仅
		// 用其做 DeadlineExceeded 错误提示的秒数格式化兜底（delegate_* 的空闲/总时长
		// 超时错误已转换为带原因的格式化错误）
		ToolTimeout: 120 * time.Second,

		// ── 只读工具并发调度（第二阶段：与 agents.RunAgentLoop 同一 toolbatch 调度）──
		// 归一化已在构造时完成（a.maxParallelReadOnlyTools），直接透传。
		MaxParallelReadOnlyTools: a.maxParallelReadOnlyTools,
		// IsParallelizableTool 并行性谓词：见 isToolParallelizable（与
		// executor.go 的 isParallelizable 闭包语义一致，方法化便于链路测试复用）。
		IsParallelizableTool: a.isToolParallelizable,
	}

	planner := director.NewPlanner(cfg, state)
	result, err := planner.Run(ctx, director.PlanInput{
		Input:  input,
		Mem:    mem,
		TaskID: a.taskID,
	})
	// 错误路径等价：Planner 所有错误分支的 PlanResult.Text 均为空串，与旧 run()
	// 各错误分支返回 ("", err) 一致；成功路径 Text 即最终回复文本
	// （plain_text → LLM 文本；agent_exit → "Task completed successfully"）。
	return result.Text, err
}

// isToolParallelizable 单步 tool_calls 的并行性谓词（与 executor.go 的
// isParallelizable 闭包语义一致）：交互工具（ask_user_for_help）与 deny-list
// （agent_exit/delegate_meta）原位串行，其余按 Adapters.IsReadOnly 判定；
// 适配器不存在 fail-safe 串行。delegate_meta 的 registerCustomAgent 注册表
// 副作用由 deny-list 兜底（原位串行）。
func (a *DirectorAgent) isToolParallelizable(name string) bool {
	if isInteractiveUserTool(name) || nonParallelToolNames[name] {
		return false
	}
	for _, t := range a.Adapters {
		if t.Name() == name {
			return t.IsReadOnly()
		}
	}
	return false
}

// applyEmergencyCompression 执行紧急压缩：提取用户原始任务 + 总结/保留 Thought & Plan 历史，
// 覆盖 memory 为单条输入消息后返回压缩后的 messages。
// Phase 3-0：编排逻辑已抽取至 director.ContextCompressor，此处仅薄委托，
// 保持原签名与压缩行为不变（characterization 测试零改动）；memory 依赖以参数传入。
func (a *DirectorAgent) applyEmergencyCompression(ctx context.Context, messages []llm.Message, threshold int) ([]llm.Message, *compression.EmergencyCompressionStats) {
	return a.compressor.ApplyEmergency(ctx, messages, threshold, a.currentMemory)
}

// ─── 终极压缩（第三级，thinklink 重建） ──────────────────────────────────────

// UltimateCompressionStats 记录终极压缩的统计信息（P0-1 Phase 3-0 定义迁移至
// director 包 compressor.go，此处保留类型别名，使 run() 等调用点零改动）。
type UltimateCompressionStats = compression.UltimateCompressionStats

// applyUltimateCompression 终极压缩（第三级）：两级常规压缩后仍超限时，
// 用 thinklink 中保存的用户原始输入 + Thought & Plan 块重建上下文。
//
//   - messages 重置为 [system..., 单条 user 重建消息]；
//   - 同步覆盖 currentMemory（保持 memory 与 messages 一致，参照 applyEmergencyCompression）；
//   - 清理 pendingSubAgentMemory 等可能导致 tool_call/tool_response 配对校验失败的残留；
//   - 循环保护：若重建后的输入自身仍超预算，逐步减少保留的 T&P 块数量
//     （保留最近 N-1、N-2……直至只留用户原始输入），仍超限则对重建内容做硬截断，
//     确保重建后必然低于阈值，绝不进入死循环。
//
// Phase 3-0：编排逻辑已抽取至 director.ContextCompressor，此处仅薄委托，
// 保持原签名与压缩行为不变（characterization 测试零改动）；memory 与配置
// （UltimateCompressionKeepPlans）以参数传入，pendingSubAgentMemory 清理经回调在
// 组件内执行（构造时注入，签名上不引入 agents 类型）。
func (a *DirectorAgent) applyUltimateCompression(messages []llm.Message, threshold int) ([]llm.Message, *UltimateCompressionStats) {
	return a.compressor.ApplyUltimate(messages, threshold, a.currentMemory, a.EnhancedCommanderCfg.UltimateCompressionKeepPlans)
}

// validateAndRepairToolCallPairs 验证并修复 tool_call/tool_response 配对完整性
//
// 如果发现孤立的 tool_calls（assistant 有 tool_calls 但缺少对应的 tool 响应），
// 删除整个不完整的 tool_call 组（assistant + 部分找到的 tool 响应），
// 而不是创建孤立的 tool 消息。
//
// 如果发现孤立的 tool 响应（没有对应 assistant 的消息），直接删除。
func validateAndRepairToolCallPairs(messages []llm.Message) []llm.Message {
	result := make([]llm.Message, 0, len(messages))

	for i := 0; i < len(messages); i++ {
		msg := messages[i]

		// Case 1: Assistant message with tool_calls
		if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) > 0 {
			// Collect all expected tool_call IDs
			expectedIDs := make(map[string]bool, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				expectedIDs[tc.ID] = true
			}

			// Collect consecutive tool responses that follow
			matchedResponses := make(map[string]llm.Message)
			j := i + 1
			for j < len(messages) {
				next := messages[j]
				if next.Role == llm.RoleTool && next.ToolCallID != "" {
					if expectedIDs[next.ToolCallID] {
						matchedResponses[next.ToolCallID] = next
					}
					j++
				} else if next.Role == llm.RoleAssistant {
					// Stop at the next assistant message (regardless of tool_calls)
					break
				} else {
					// User, System, or other non-tool messages — stop scanning
					break
				}
			}

			allResponsesPresent := len(matchedResponses) == len(msg.ToolCalls)

			if allResponsesPresent {
				// Complete, valid tool_call group — keep it
				result = append(result, msg)
				// Append responses in the order of tool_calls for determinism
				for _, tc := range msg.ToolCalls {
					if resp, ok := matchedResponses[tc.ID]; ok {
						result = append(result, resp)
					}
				}
			} else {
				// Incomplete tool_call group — remove ENTIRE group (assistant + partial responses)
				// Do NOT create a new assistant without tool_calls (old bug)
				// Do NOT keep partial tool responses (they'd become orphans)
				missingIDs := make([]string, 0)
				for _, tc := range msg.ToolCalls {
					if _, ok := matchedResponses[tc.ID]; !ok {
						missingIDs = append(missingIDs, tc.ID)
					}
				}
				slog.Warn("Removing incomplete tool_call group due to context compression",
					"expected", len(msg.ToolCalls),
					"found", len(matchedResponses),
					"missing_ids", missingIDs,
				)
				// Preserve assistant text content if available (without tool_calls)
				if msg.Content != "" {
					preserved := msg
					preserved.ToolCalls = nil
					result = append(result, preserved)
				}
			}

			// Skip to the position after the tool responses (j already points past them)
			// unmatched tool responses will be handled by Case 2 (orphan detection)
			i = j - 1
			continue
		}

		// Case 2: Orphan tool message (no preceding assistant with matching tool_calls)
		if msg.Role == llm.RoleTool && msg.ToolCallID != "" {
			hasMatchingAssistant := false
			for k := len(result) - 1; k >= 0; k-- {
				if result[k].Role == llm.RoleAssistant && len(result[k].ToolCalls) > 0 {
					for _, tc := range result[k].ToolCalls {
						if tc.ID == msg.ToolCallID {
							hasMatchingAssistant = true
							break
						}
					}
					break
				}
				if result[k].Role == llm.RoleUser || result[k].Role == llm.RoleAssistant {
					break
				}
			}
			if !hasMatchingAssistant {
				// Orphan tool response — drop it
				slog.Warn("Removing orphan tool message (no matching assistant)",
					"tool_call_id", msg.ToolCallID,
				)
				continue
			}
		}

		// Case 3: Normal message (system, user, assistant without tool_calls, or matched tool)
		result = append(result, msg)
	}

	// Merge consecutive assistant messages into single messages
	result = llm.MergeConsecutiveAssistants(result)

	return result
}
