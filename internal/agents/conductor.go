package agents

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	conductor "codeactor/internal/agents/conductor"
	"codeactor/internal/compact"
	"codeactor/internal/config"
	"codeactor/internal/globalctx"
	"codeactor/internal/llm"
	"codeactor/internal/memory"
	"codeactor/internal/tools"
)

//go:embed conductor.prompt.md
var conductorPrompt string

// CustomAgent stores a dynamically designed agent created by Meta-Agent.
// Once registered, it becomes available as a permanent delegate tool.
type CustomAgent struct {
	Name         string   // snake_case identifier used for the delegate tool name
	DisplayName  string   // human-readable agent name
	SystemPrompt string   // the full system prompt designed by Meta-Agent
	ToolsUsed    []string // tool names this agent was designed to use
	Description  string   // short description for the LLM
}

// metaAgentResult parses the JSON output from Meta-Agent.
type metaAgentResult struct {
	Thinking     string   `json:"thinking"`
	AgentName    string   `json:"agent_name"`
	AgentDesign  string   `json:"agent_design"`
	ToolsUsed    []string `json:"tools_used"`
	TaskForAgent string   `json:"task_for_agent"`
}

// ProjectContextFile represents a successfully loaded project context file
type ProjectContextFile struct {
	FileName string `json:"file_name"`
	Content  string `json:"content"`
}

// ProjectContextLoadResult represents the result of loading project context files
type ProjectContextLoadResult struct {
	LoadedFiles []ProjectContextFile `json:"loaded_files"`
	Content     string               `json:"content"`
}

type ConductorAgent struct {
	BaseAgent
	RepoAgent      *RepoAgent
	CodingAgent    *CodingAgent
	ChatAgent      *ChatAgent
	MetaAgent      *MetaAgent
	DevOpsAgent    *DevOpsAgent
	BrowserAgent   *BrowserAgent
	GlobalCtx      *globalctx.GlobalCtx
	Adapters       []*tools.Adapter
	maxSteps       int
	metaRetryCount int                             // max retries for Meta-Agent JSON parse failures
	toolDefMap     map[string]tools.ToolDefinition // tool name → definition from tools.json
	customAgents   map[string]*CustomAgent         // delegate_<name> → agent design
	compactEngine  *compact.Engine                 // 上下文压缩引擎
	compactConfig  *compact.Config                 // 压缩配置
	adapter        *ConductorAdapter               // 新旧整合适配器
	summaryEngine  llm.Engine                      // 独立的摘要 LLM 引擎（nil 则复用主引擎）

	// 新增：异步增量压缩字段
	asyncCompactor *compact.AsyncCompactor   // 异步压缩管理器
	compState      *compact.CompressionState // 增量压缩状态
	pendingCompRes *compact.CompactJobResult // 待应用的压缩结果（异步）

	cachedProjectContext *ProjectContextLoadResult // 缓存项目上下文文件（同一会话只加载一次）
	commitManager        *CommitManager            // commit 学习器管理器
	hasDelegated         bool                      // 标记是否已委派过 agent
	delegationAttempts   int                       // 委派尝试次数统计

	currentMemory         *memory.ConversationMemory // 当前正在使用的 memory（Run 期间设置）
	pendingSubAgentMemory *AgentResult               // 最近一次 delegate 调用的完整结果（用于 memory 注入）

	// LLM 兜底机制字段
	stepRetries                int           // 步骤重试次数（从 config.LLM.StepRetries 读取）
	circuitBreakerThreshold    int           // 熔断阈值（连续失败次数），0=不启用
	circuitBreakerResetTimeout time.Duration // 熔断恢复时间
	consecutiveLLMFailures     int           // 连续 LLM 调用失败计数
	lastLLMFailureTime         time.Time     // 最近一次 LLM 失败时间

	// EnhancedCommander 增强型配置
	EnhancedCommanderCfg config.EnhancedCommanderConfig
	// resultCompressor 结果压缩器（nil 表示不启用压缩）
	resultCompressor *ResultCompressor
}

// loadProjectContext 读取工作区目录下的项目上下文文件（CODEACTOR.md、CLAUDE.md、AGENTS.md），
// 将成功读取的文件内容格式化后组合返回。文件按顺序尝试，不存在或读取失败时忽略。
// 返回加载的文件列表和组合后的内容。
func (a *ConductorAgent) loadProjectContext() *ProjectContextLoadResult {
	// 如果已经加载过，直接返回缓存（同一 Agent 实例会话内只加载一次）
	if a.cachedProjectContext != nil {
		return a.cachedProjectContext
	}
	result := &ProjectContextLoadResult{
		LoadedFiles: []ProjectContextFile{},
	}
	var sb strings.Builder
	contextFiles := []string{"CODEACTOR.md", "CLAUDE.md", "AGENTS.md"}

	for _, fname := range contextFiles {
		fullPath := filepath.Join(a.GlobalCtx.ProjectPath, fname)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			// 文件不存在或读取失败，忽略并继续尝试下一个
			continue
		}
		if len(data) > 0 {
			result.LoadedFiles = append(result.LoadedFiles, ProjectContextFile{
				FileName: fname,
				Content:  string(data),
			})
			sb.WriteString(fmt.Sprintf("\n### %s\n```\n%s\n```\n", fname, string(data)))
		}
	}
	result.Content = sb.String()
	// 缓存加载结果，避免后续调用重复读取文件
	a.cachedProjectContext = result
	return result
}

func NewConductorAgent(globalCtx *globalctx.GlobalCtx, engine llm.Engine, repo *RepoAgent, coding *CodingAgent, chat *ChatAgent, meta *MetaAgent, devops *DevOpsAgent, browser *BrowserAgent, maxSteps int, disabledAgents map[string]bool, metaRetryCount int, compactCfg *compact.Config, summaryEngine llm.Engine, cfg config.Config, llmClient *llm.Client) *ConductorAgent {
	// self-reference for closures that need the ConductorAgent after construction
	var self *ConductorAgent

	delegateRepo := tools.NewAdapter("delegate_repo", "Delegate analysis task to Repo-Agent", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		result, err := repo.Run(ctx, task)
		// 使用增强型 Commander 处理结果（压缩 + 注册）
		return self.applyEnhancedCommander("repo", task, result, err)
	}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{"type": "string", "description": "The task description for Repo-Agent"},
		},
		"required": []string{"task"},
	})

	delegateCoding := tools.NewAdapter("delegate_coding", "Delegate coding task to Coding-Agent", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		// RepoSummary is no longer injected into the task here — it is now passed
		// via ExecutorConfig.RepoContext and appended to the sub-agent's system prompt,
		// keeping the user message (task) variable and the system prompt cacheable.
		result, err := coding.Run(ctx, task)
		// 使用增强型 Commander 处理结果（压缩 + 注册）
		return self.applyEnhancedCommander("coding", task, result, err)
	}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{"type": "string", "description": "The task description for Coding-Agent"},
		},
		"required": []string{"task"},
	})

	delegateChat := tools.NewAdapter("delegate_chat", "Delegate general conversation, explanation, or non-coding tasks to Chat-Agent", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		result, err := chat.Run(ctx, task)
		// 使用增强型 Commander 处理结果（压缩 + 注册）
		return self.applyEnhancedCommander("chat", task, result, err)
	}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{"type": "string", "description": "The message or question for Chat-Agent"},
		},
		"required": []string{"task"},
	})

	delegateDevOps := tools.NewAdapter("delegate_devops", "Delegate operational and system administration tasks to DevOps-Agent. DevOps-Agent can run shell commands, inspect files, check logs, manage processes, and perform any non-coding infrastructure work. Use this for tasks like checking disk usage, finding files, running diagnostics, inspecting configurations, or executing ad-hoc shell commands.", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		result, err := devops.Run(ctx, task)
		// 使用增强型 Commander 处理结果（压缩 + 注册）
		return self.applyEnhancedCommander("devops", task, result, err)
	}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{"type": "string", "description": "The operational task for DevOps-Agent, e.g., 'check disk usage', 'find all log files modified today', 'check if port 8080 is in use'."},
		},
		"required": []string{"task"},
	})

	delegateBrowser := tools.NewAdapter("delegate_browser",
		"Delegate browser automation tasks to Browser-Agent. Browser-Agent controls a headless Chrome browser using go-rod to navigate websites, click elements, fill forms, extract data, take screenshots, generate PDFs, execute JavaScript (with user confirmation), and manage cookies. Use this for tasks like: 'screenshot https://example.com', 'extract text from https://example.com/article', 'fill and submit the login form at https://example.com/login', 'check if website is reachable', 'get the current URL after navigation'. The agent handles all browser lifecycle and page management internally.",
		func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			task, ok := params["task"].(string)
			if !ok {
				return nil, fmt.Errorf("task parameter required")
			}
			// RepoSummary is no longer injected into the task here — it is now passed
			// via ExecutorConfig.RepoContext and appended to the sub-agent's system prompt.
			result, err := browser.Run(ctx, task)
			// 使用增强型 Commander 处理结果（压缩 + 注册）
			return self.applyEnhancedCommander("browser", task, result, err)
		}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{
				"type":        "string",
				"description": "The browser automation task for Browser-Agent, e.g., 'screenshot https://example.com homepage', 'extract article text from https://example.com/blog/post-1', 'fill the login form and submit', 'navigate to https://example.com and return the page title'.",
			},
		},
		"required": []string{"task"},
	})

	delegateMeta := tools.NewAdapter("delegate_meta", "Delegate to Meta-Agent to DESIGN a custom specialized agent. Meta-Agent will craft a tailored system prompt using prompt engineering best practices and select appropriate tools. The designed agent is automatically registered and immediately executed to complete the task. After this, the new agent becomes a permanent delegate tool for future use.", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		slog.Info("Conductor delegating to Meta-Agent (design)", "task", task)

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
			// 存储 meta-agent memory（如果后续执行了自定义 agent，会被覆盖为自定义 agent 的 memory）
			self.pendingSubAgentMemory = &metaResult
			lastRawOutput = metaResult.Text

			systemPrompt, execResult, parseErr := parseMetaAgentOutput(metaResult.Text)
			if parseErr != nil {
				slog.Warn("Meta-Agent JSON parse failed, retrying", "attempt", attempt+1, "maxRetries", maxRetries, "error", parseErr)
				continue
			}

			// ── Parse succeeded ──
			// Register the newly designed agent if it has a valid name and prompt
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

				// Use task_for_agent (clean task without meta-design instructions) if available;
				// otherwise fall back to the original task.
				agentTask := execResult.TaskForAgent
				if agentTask == "" {
					agentTask = task
				}

				// ── Immediately execute the newly registered agent ──
				// Find the just-created delegate tool and call it
				delegateName := "delegate_" + snakeName
				for _, ad := range self.Adapters {
					if ad.Name() == delegateName {
						slog.Info("Conductor executing newly designed agent", "delegate", delegateName, "display_name", execResult.AgentName)
						callResult, callErr := ad.Call(ctx, fmt.Sprintf(`{"task": %q}`, agentTask))
						if callErr != nil {
							return nil, fmt.Errorf("new agent %s execution failed: %w", execResult.AgentName, callErr)
						}
						// ad.Call returns JSON-encoded string, unmarshal to get the raw result
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

			// Parse succeeded but no agent to register
			return fmt.Sprintf("[Meta-Agent Design Result]\nAgent could not be registered (missing name or design). Raw output: %s", metaResult.Text), nil
		}

		// All retries exhausted
		slog.Warn("Meta-Agent JSON parse failed after all retries, returning raw output")
		return lastRawOutput, nil
	}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{"type": "string", "description": "Detailed task description for Meta-Agent. Include: what needs to be accomplished, why existing agents are insufficient, and what the expected output format should be."},
		},
		"required": []string{"task"},
	})

	adapters := []*tools.Adapter{
		tools.NewAdapter("agent_exit", "Exit the agent with a reason. Use this when you are done — whether the task completed successfully, failed, needs clarification, or must be terminated. The reason must explain WHY the agent is exiting.", globalCtx.FlowOps.ExecuteAgentExit).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"reason": map[string]interface{}{"type": "string", "description": "The reason the agent is exiting, e.g., task completed, cannot proceed, blocked by missing information, or must terminate."},
			},
			"required": []string{"reason"},
		}),
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
			fn = globalCtx.SearchOps.ExecuteGrepSearch
		case "list_dir":
			fn = globalCtx.FileOps.ExecuteListDir
		case "read_file":
			fn = globalCtx.FileOps.ExecuteReadFile
		case "print_dir_tree":
			fn = globalCtx.FileOps.ExecutePrintDirTree
		case "deepthinking":
			fn = globalCtx.DeepThinkingTool.Execute
		default:
			continue
		}

		adapter := tools.NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters)
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
	tools.SetGuardOnAdapters(adapters, globalCtx.Guard)
	tools.SetGuardOnAdapters(delegateAdapters, globalCtx.Guard)

	// 创建 commit 管理器（用于后台自动学习和查询，不再暴露为 Agent 工具）
	var commitManager *CommitManager
	if llmClient != nil {
		commitManager = NewCommitManager(cfg, engine, llmClient, globalCtx)
	} else {
		// fallback to no dedicated engine
		commitManager = NewCommitManager(cfg, engine, nil, globalCtx)
	}

	allAdapters := append(adapters, delegateAdapters...)

	// Strangler Fig: 创建适配器桥接层，开始使用新组件（Metrics + CircuitBreaker）
	adapterCfg := conductor.DefaultRecoveryConfig()
	adapterCfg.MaxRetries = maxSteps // 使用 maxSteps 作为重试次数
	adapterCfg.CircuitBreakerThreshold = cfg.LLM.CircuitBreakerThreshold
	adapterCfg.CircuitBreakerResetTimeout = cfg.LLM.CircuitBreakerResetTimeout
	conductorAdapter := NewConductorAdapter(true, adapterCfg) // enabled=true 启动 Metrics

	self = &ConductorAgent{
		BaseAgent:          BaseAgent{LLM: engine, Publisher: globalCtx.Publisher},
		RepoAgent:          repo,
		CodingAgent:        coding,
		ChatAgent:          chat,
		MetaAgent:          meta,
		DevOpsAgent:        devops,
		BrowserAgent:       browser,
		GlobalCtx:          globalCtx,
		Adapters:           allAdapters,
		maxSteps:           maxSteps,
		metaRetryCount:     metaRetryCount,
		toolDefMap:         toolDefMap,
		customAgents:       make(map[string]*CustomAgent),
		compactEngine:      nil, // 将在 Run 方法中根据配置初始化
		compactConfig:      compactCfg,
		adapter:            conductorAdapter,
		summaryEngine:      summaryEngine,
		commitManager:      commitManager, // 设置 commit 学习器管理器
		hasDelegated:       false,         // 初始未委派过
		delegationAttempts: 0,             // 委派尝试次数初始为0

		// LLM 兜底机制配置
		stepRetries:                cfg.LLM.StepRetries,
		circuitBreakerThreshold:    cfg.LLM.CircuitBreakerThreshold,
		circuitBreakerResetTimeout: cfg.LLM.CircuitBreakerResetTimeout,
		// consecutiveLLMFailures 和 lastLLMFailureTime 保持零值即可

		// EnhancedCommander 配置
		EnhancedCommanderCfg: cfg.EnhancedCommander,
		resultCompressor: NewResultCompressor(
			cfg.EnhancedCommander.CompressionThreshold,
			cfg.EnhancedCommander.SummaryMaxLength,
		),
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
	slog.Info("Conductor tool definitions initialized",
		"hash", toolHash,
		"tool_count", len(toolDefsForHash),
		"tool_names", names)

	return self
}

func (a *ConductorAgent) Name() string {
	return "Conductor"
}

// getToolFunc returns the ToolFunc implementation for a given tool name.
// This is used when constructing tool adapters for dynamically created agents.
func (a *ConductorAgent) getToolFunc(name string) tools.ToolFunc {
	switch name {
	case "read_file":
		return a.GlobalCtx.FileOps.ExecuteReadFile
	case "search_replace_in_file":
		return a.GlobalCtx.ReplaceTool.ExecuteReplaceBlock
	case "create_file":
		return a.GlobalCtx.FileOps.ExecuteCreateFile
	case "run_bash":
		return a.GlobalCtx.SysOps.ExecuteRunBash
	case "search_by_regex":
		return a.GlobalCtx.SearchOps.ExecuteGrepSearch
	case "delete_file":
		return a.GlobalCtx.FileOps.ExecuteDeleteFile
	case "rename_file":
		return a.GlobalCtx.FileOps.ExecuteRenameFile
	case "list_dir":
		return a.GlobalCtx.FileOps.ExecuteListDir
	case "print_dir_tree":
		return a.GlobalCtx.FileOps.ExecutePrintDirTree
	case "semantic_search":
		return a.GlobalCtx.RepoOps.ExecuteSemanticSearch
	case "query_code_skeleton":
		return a.GlobalCtx.RepoOps.ExecuteQueryCodeSkeleton
	case "query_code_snippet":
		return a.GlobalCtx.RepoOps.ExecuteQueryCodeSnippet
	case "thinking":
		return func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			inputBytes, _ := json.Marshal(params)
			return a.GlobalCtx.ThinkingTool.Call(ctx, string(inputBytes))
		}
	case "micro_agent":
		return a.GlobalCtx.MicroAgentTool.Execute
	case "deepthinking":
		return a.GlobalCtx.DeepThinkingTool.Execute
	case "agent_exit":
		return a.GlobalCtx.FlowOps.ExecuteAgentExit
	case "ask_user_for_help":
		return a.GlobalCtx.FlowOps.ExecuteAskUserForHelp
	default:
		return nil
	}
}

// GetCommitContext 获取与用户输入最匹配的 commit 上下文
//
// 该方法用于在系统提示中注入相关的历史 commit 信息，帮助 LLM
// 了解相似的历史变更，从而提高代码生成的准确性。
//
// 参数:
//   - ctx: 上下文
//   - userInput: 用户输入文本
//
// 返回值:
//   - string: 格式化的 commit 摘要文本（空字符串表示无可用的 commit 上下文）
func (a *ConductorAgent) GetCommitContext(ctx context.Context, userInput string) string {
	if a.commitManager == nil || !a.commitManager.Enabled() {
		return ""
	}

	learner, err := a.commitManager.GetLearner()
	if err != nil || learner == nil {
		return ""
	}

	summaries, err := learner.SearchSimilar(ctx, userInput, learner.Config().TopK)
	if err != nil {
		slog.Warn("[CommitLearner] Search error", "error", err)
		return ""
	}

	if len(summaries) == 0 {
		return ""
	}

	result := FormatSummaryAsText(summaries)

	// 发布 commit 知识加载事件到 TUI
	if a.Publisher != nil {
		commitEvent := map[string]interface{}{
			"count":     len(summaries),
			"summaries": summaries,
		}
		a.Publisher.Publish("commit_context_loaded", commitEvent, a.Name())
	}

	return result
}

// parseMetaAgentOutput extracts and validates the JSON object from Meta-Agent's raw output.
// It strips markdown code fences and surrounding text to find the JSON.
func parseMetaAgentOutput(output string) (systemPrompt string, execResult *metaAgentResult, err error) {
	jsonStr := extractJSONObject(output)
	if jsonStr == "" {
		return "", nil, fmt.Errorf("no JSON object found in Meta-Agent output")
	}

	execResult = &metaAgentResult{}
	if err := json.Unmarshal([]byte(jsonStr), execResult); err != nil {
		return "", nil, fmt.Errorf("failed to parse Meta-Agent JSON: %w", err)
	}

	// Validate required fields
	if execResult.AgentName == "" {
		return "", nil, fmt.Errorf("agent_name is empty in Meta-Agent JSON")
	}
	if execResult.AgentDesign == "" {
		return "", nil, fmt.Errorf("agent_design is empty in Meta-Agent JSON")
	}

	return execResult.AgentDesign, execResult, nil
}

// extractJSONObject finds the outermost JSON object in a string.
// It strips markdown code fences and handles surrounding text.
func extractJSONObject(s string) string {
	raw := s

	// Strip markdown code fences: ```json ... ``` or ``` ... ```
	if idx := strings.Index(raw, "```"); idx != -1 {
		endFence := strings.Index(raw[idx+3:], "```")
		if endFence != -1 {
			inner := raw[idx+3 : idx+3+endFence]
			// Skip optional language tag after opening ```
			if newline := strings.Index(inner, "\n"); newline != -1 {
				inner = inner[newline+1:]
			}
			raw = inner
		}
	}

	// Find the outermost { ... }
	start := strings.Index(raw, "{")
	if start == -1 {
		return ""
	}
	// Walk braces to find the matching close brace
	depth := 0
	for i := start; i < len(raw); i++ {
		switch raw[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return raw[start : i+1]
			}
		}
	}
	return ""
}

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
// and adds it to the Conductor's Adapters list. The agent becomes permanently available.
func (a *ConductorAgent) registerCustomAgent(ca *CustomAgent) {
	delegateName := "delegate_" + ca.Name

	// Check if already registered
	if _, exists := a.customAgents[delegateName]; exists {
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
		adapter := tools.NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters)
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
	tools.SetGuardOnAdapters(customAdapters, a.GlobalCtx.Guard)

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
			return a.executeCustomAgent(ctx, agentRef, adaptersRef, task)
		}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{"type": "string", "description": "The task description for " + ca.DisplayName},
		},
		"required": []string{"task"},
	})

	a.Adapters = append(a.Adapters, delegateAdapter)
	a.customAgents[delegateName] = ca

	slog.Info("Custom agent registered", "delegate_name", delegateName, "display_name", ca.DisplayName, "tools", ca.ToolsUsed)
}

// executeCustomAgent runs a custom agent with its designed system prompt and selected tools.
// Uses the unified AgentExecutor.
func (a *ConductorAgent) executeCustomAgent(ctx context.Context, ca *CustomAgent, adapters []*tools.Adapter, task string) (string, error) {
	systemPrompt := a.GlobalCtx.FormatPrompt(ca.SystemPrompt)

	cfg := DefaultExecutorConfig()
	cfg.SystemPrompt = systemPrompt
	cfg.UserInput = task
	cfg.Adapters = adapters
	cfg.LLM = a.LLM
	cfg.MaxSteps = 15
	cfg.Publisher = a.Publisher
	cfg.AgentName = ca.DisplayName
	cfg.StopOnFinish = true
	// EnableCollaboration 已默认 true
	result, err := RunAgentLoop(ctx, cfg)
	if err != nil {
		return "", err
	}
	// 存储自定义 agent memory 供 Run 方法注入
	agentResult := AgentResult{
		Text:   result.Text,
		Memory: ConvertLLMHistoryToMemory(result.History),
	}
	a.pendingSubAgentMemory = &agentResult
	return result.Text, nil
}

// injectSubAgentMemory 将 sub-agent 的执行结果摘要注入到 Conductor memory 中
// Phase 1: 只注入摘要，不再注入 sub-agent 的完整对话历史
// Phase 3+ : Sub-agent 的关键发现通过 SharedMemory 发布/订阅机制共享
func (a *ConductorAgent) injectSubAgentMemory(result AgentResult, toolCallID string, toolName string) {
	if a.currentMemory == nil {
		return
	}

	// 只注入摘要消息（result.Text），不注入完整历史
	// sub-agent 的完整对话历史保留在其自身的 LocalMemory 中（Phase 3）
	if result.Text != "" {
		summaryMsg := memory.ChatMessage{
			Type:       memory.MessageTypeAssistant,
			Content:    fmt.Sprintf("[Sub-Agent Result: %s]\n%s", toolName, result.Text),
			Timestamp:  time.Now(),
			GroupID:    fmt.Sprintf("%s_summary_%d", toolName, time.Now().UnixNano()),
			ParentID:   toolCallID,
			IsSubAgent: true,
			Metadata: map[string]interface{}{
				"type":      "sub_agent_summary",
				"tool":      toolName,
				"msg_count": len(result.Memory),
			},
		}
		a.currentMemory.Messages = append(a.currentMemory.Messages, summaryMsg)
	}

	// 重要：result.Memory（sub-agent 的完整对话历史）不再注入到 Conductor 的 memory 中
	// 这避免了 Conductor 上下文快速膨胀和 Compact Engine 频繁压缩造成的信息丢失
	// sub-agent 内部消息保留在 sub-agent 本地，通过 SharedMemory 的 publish/subscribe 机制共享关键信息（Phase 3）
}

// applyEnhancedCommander 对子 Agent 执行结果应用增强型 Commander 功能。
// 包含：结果压缩（如果启用）、结果注册（如果启用）
// agentType: 子 Agent 类型（如 "repo", "coding"）
// task: 委派的任务描述
// result: Agent 执行结果
// err: Agent 执行错误
// 返回: 处理后的结果文本和错误
func (a *ConductorAgent) applyEnhancedCommander(
	agentType string,
	task string,
	result AgentResult,
	err error,
) (string, error) {
	// 始终存储 sub-agent memory（保持现有行为）
	a.pendingSubAgentMemory = &result

	if err != nil {
		return "", err
	}

	cfg := a.EnhancedCommanderCfg
	if !cfg.Enable {
		return result.Text, nil
	}

	text := result.Text

	// 结果压缩（如果启用）
	if cfg.EnableResultCompression && a.resultCompressor != nil {
		compResult := a.resultCompressor.Compress(agentType, task, text)
		if compResult.Compressed {
			text = compResult.Content
			slog.Debug("Result compressed",
				"agent", agentType,
				"original_size", compResult.OriginalSize,
				"compressed_size", compResult.CompressedSize,
				"storage_key", compResult.StorageKey,
			)
		}
	}

	return text, nil
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

func (a *ConductorAgent) Run(ctx context.Context, input string, mem *memory.ConversationMemory) (string, error) {
	// 设置当前 memory（delegate 闭包通过 a.currentMemory 访问）
	a.currentMemory = mem
	defer func() { a.currentMemory = nil }()

	// 将 commit 上下文移出 system prompt，前置到用户输入中，
	// 确保 system prompt 完全静态，提高 LLM Prompt Cache 命中率。
	if input != "" {
		if commitCtx := a.GetCommitContext(ctx, input); commitCtx != "" {
			input = fmt.Sprintf("### Recent Relevant Commits\n%s\n\n### Current Task\n%s", commitCtx, input)
			slog.Debug("Commit context prepended to user input")
		}
	}

	if mem != nil {
		// Check if the last message is the same as input to avoid duplication
		// because handleChatMessage might have already added it.
		lastMsg := mem.GetLastMessage()
		if lastMsg == nil || lastMsg.Content != input || lastMsg.Type != memory.MessageTypeHuman {
			mem.AddHumanMessage(input)
		}
	}

	// ═══════ 初始化上下文压缩引擎 ═══════
	if a.compactEngine == nil && a.compactConfig != nil && a.compactConfig.EnableAutoCompact {
		summaryClient := a.createSummaryClient()
		engine, err := compact.NewEngine(a.compactConfig, summaryClient)
		if err != nil {
			slog.Warn("Failed to create compact engine", "error", err)
		} else {
			a.compactEngine = engine
			slog.Info("Context compact engine initialized",
				"max_tokens", a.compactConfig.MaxContextTokens,
				"summarization_model", a.compactConfig.SummarizationModel)
		}
	}

	// ═══════ 初始化异步压缩管理器 ═══════
	if a.compactEngine != nil && a.compactConfig != nil && a.compactConfig.AsyncCompactEnabled {
		a.asyncCompactor = compact.NewAsyncCompactor(a.compactEngine, a.compactConfig)
		a.asyncCompactor.Start(ctx)
		a.compState = &compact.CompressionState{}
		slog.Info("Async compactor started",
			"trigger_threshold", a.compactConfig.CompactTriggerThreshold)
	}

	// ═══════ 初始化 CommitLearner ═══════
	if a.commitManager != nil {
		if err := a.commitManager.Initialize(ctx, a.GlobalCtx.ProjectPath); err != nil {
			slog.Warn("CommitLearner initialization failed", "error", err)
		}
	}

	var messages []llm.Message

	// Always start with System Prompt (with any registered custom agents appended)
	systemPrompt := a.GlobalCtx.FormatPrompt(conductorPrompt)
	var projectContext string
	// 只在首次对话时加载项目上下文文件（CODEACTOR.md、CLAUDE.md、AGENTS.md），
	// 同一会话的后续追问无需重复注入，避免浪费 token。
	// memory 中不存储 system 消息，因此 len(mem.GetMessages()) == 0 即可判断是否为首次对话。
	if mem == nil || len(mem.GetMessages()) == 0 {
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

	// 自定义 Agent 描述
	if len(a.customAgents) > 0 {
		systemPrompt += "\n\n### Custom Agents\nThe following specialized agents have been designed by Meta-Agent and are permanently available for delegation:\n\n"
		for _, ca := range a.customAgents {
			systemPrompt += fmt.Sprintf("- **%s** (`delegate_%s`): %s\n", ca.DisplayName, ca.Name, ca.Description)
		}
		systemPrompt += "\nUse these agents via their delegate tools for tasks matching their specializations.\n"
	}

	// 追加项目上下文（放在所有静态内容之后，确保缓存命中率）
	if projectContext != "" {
		systemPrompt += projectContext
	}

	messages = append(messages, llm.Message{
		Role:    llm.RoleSystem,
		Content: systemPrompt,
	})

	if mem != nil {
		for _, m := range mem.GetMessages() {
			if m.Type == memory.MessageTypeSystem {
				continue
			}
			// 过滤 sub-agent 内部消息，避免破坏 tool_calls → tool 消息配对规则
			// sub-agent 消息由 injectSubAgentMemory() 注入，仅用于内存记录，不应发送给 LLM
			if m.IsSubAgent {
				continue
			}
			messages = append(messages, memory.ConvertMemoryMessageToLLMSMessage(m))
		}
	} else {
		messages = append(messages, llm.Message{
			Role:    llm.RoleUser,
			Content: input,
		})
	}

	toolDefs := make([]llm.ToolDef, len(a.Adapters))
	for i, ad := range a.Adapters {
		toolDefs[i] = ad.ToToolDef()
	}
	tools.SortToolDefs(toolDefs)

	// Publish model info so the TUI can display it in the status bar.
	if a.Publisher != nil {
		a.Publisher.Publish("model_info", map[string]interface{}{
			"model": a.LLM.Model(),
			"agent": a.Name(),
		}, a.Name())
	}

	for i := 0; i < a.maxSteps; i++ {
		// ═══════════════════════════════════════════════════════════
		// CONTEXT COMPACT GATEWAY（增强版：支持异步增量压缩）
		// ═══════════════════════════════════════════════════════════
		if a.compactEngine != nil && a.compactConfig.EnableAutoCompact {
			// 1. 检查是否有异步压缩结果待应用
			if a.pendingCompRes != nil {
				result := a.pendingCompRes
				a.pendingCompRes = nil

				if result.Err == nil && len(result.CompressedMessages) > 0 {
					// 替换消息列表（保留最新的未处理消息）
					if len(result.CompressedMessages) < len(messages) {
						messages = result.CompressedMessages
						if result.NewState != nil {
							a.compState = result.NewState
						}
						slog.Info("Async compression result applied",
							"stats", result.Stats,
							"duration", result.Duration)

						if a.Publisher != nil {
							a.Publisher.Publish("context_compressed", map[string]interface{}{
								"compressed":   len(result.CompressedMessages),
								"stats":        result.Stats,
								"duration_sec": result.Duration.Seconds(),
							}, a.Name())
						}
					}
				} else if result.Err != nil {
					slog.Warn("Async compression failed, skipping", "error", result.Err)
				}
			}

			// 2. 计算当前 token 数
			originalTokens, err := a.compactEngine.CountTokens(messages)
			if err != nil {
				slog.Warn("Failed to count tokens", "error", err)
			} else {
				// 3. 检查是否超限 — 如果接近硬上限则同步压缩（紧急降级）
				if originalTokens > a.compactConfig.MaxContextTokens {
					slog.Warn("Context exceeded hard limit, emergency sync compression",
						"tokens", originalTokens,
						"max", a.compactConfig.MaxContextTokens)

					// 尝试异步（如果有且未触发）
					if a.asyncCompactor != nil && a.asyncCompactor.IsRunning() && a.pendingCompRes == nil {
						// 快照当前消息
						snap := make([]llm.Message, len(messages))
						copy(snap, messages)
						stateCopy := a.compState.DeepCopy()
						job := &compact.CompactJob{
							MessageSnapshot: snap,
							State:           stateCopy,
							ResultCh:        make(chan *compact.CompactJobResult, 1),
						}
						a.asyncCompactor.SubmitJob(job)
						// 等待结果（带超时）
						select {
						case res := <-job.ResultCh:
							if res.Err == nil && len(res.CompressedMessages) > 0 {
								messages = res.CompressedMessages
								if res.NewState != nil {
									a.compState = res.NewState
								}
								slog.Info("Emergency async compression applied",
									"tokens_after", len(res.CompressedMessages))
							}
						case <-time.After(10 * time.Second):
							slog.Warn("Emergency async compression timed out")
							// 降级：对现有消息做同步全量压缩
							result, err := a.compactEngine.Compress(ctx, messages)
							if err == nil {
								messages = result.CompressedMessages
							}
						}
					} else {
						// 没有异步管理器，走同步全量压缩（原逻辑）
						slog.Info("Context exceeds limit, triggering sync compression",
							"original_tokens", originalTokens,
							"max_tokens", a.compactConfig.MaxContextTokens)
						result, err := a.compactEngine.Compress(ctx, messages)
						if err != nil {
							slog.Warn("Context compression failed", "error", err)
						} else {
							messages = result.CompressedMessages
							slog.Info("Context compressed",
								"compressed_tokens", result.CompressedTokens,
								"ratio", fmt.Sprintf("%.2f%%", result.CompressionRatio*100))
							if a.Publisher != nil {
								a.Publisher.Publish("context_compressed", map[string]interface{}{
									"original_tokens":   result.OriginalTokens,
									"compressed_tokens": result.CompressedTokens,
									"ratio":             fmt.Sprintf("%.2f%%", result.CompressionRatio*100),
								}, a.Name())
							}
						}
					}
				} else if a.asyncCompactor != nil && a.asyncCompactor.IsRunning() && a.pendingCompRes == nil {
					// 4. 未超限，但超过阈值时提前触发异步压缩（预压缩）
					threshold := int(float64(a.compactConfig.MaxContextTokens) * a.compactConfig.CompactTriggerThreshold)
					if originalTokens > threshold {
						slog.Debug("Triggering async pre-compression",
							"tokens", originalTokens,
							"threshold", threshold)

						snap := make([]llm.Message, len(messages))
						copy(snap, messages)
						stateCopy := a.compState.DeepCopy()
						job := &compact.CompactJob{
							MessageSnapshot: snap,
							State:           stateCopy,
							ResultCh:        make(chan *compact.CompactJobResult, 1),
						}
						if a.asyncCompactor.SubmitJob(job) {
							// 启动一个 goroutine 等待结果
							go func(j *compact.CompactJob) {
								res := <-j.ResultCh
								a.pendingCompRes = res
							}(job)
						}
					}
				}
			}
		}
		// ═══════════════════════════════════════════════════════════

		// --- 熔断检查（通过适配器委托到新组件）---
		if a.adapter != nil && a.adapter.IsCircuitBreakerOpen() {
			slog.Error("Circuit breaker open, too many consecutive LLM failures",
				"step", i)
			return "", fmt.Errorf("circuit breaker open: LLM calls blocked")
		}

		// --- 步骤级重试 ---
		maxRetries := a.stepRetries
		var resp *llm.Response
		var llmErr error
		for attempt := 0; attempt <= maxRetries; attempt++ {
			if attempt > 0 {
				// 指数退避：1s, 2s, 4s, 8s, ... 最大30s
				wait := time.Duration(1<<(attempt-1)) * time.Second
				if wait > 30*time.Second {
					wait = 30 * time.Second
				}
				slog.Warn("ConductorAgent retrying LLM call", "step", i, "attempt", attempt, "wait", wait)
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(wait):
				}
			}

			// 验证并修复 tool_call/tool_response 配对完整性
			messages = validateAndRepairToolCallPairs(messages)

			slog.Debug("ConductorAgent calling LLM", "step", i, "messages", messages)

			// Publish llm_call_start event
			if a.Publisher != nil {
				a.Publisher.Publish("llm_call_start", map[string]interface{}{
					"model": a.LLM.Model(),
					"agent": a.Name(),
				}, a.Name())
			}

			llmStartTime := time.Now()
			resp, llmErr = a.LLM.GenerateContent(ctx, messages, toolDefs, nil)
			llmDuration := time.Since(llmStartTime).Seconds()

			// 记录 LLM 耗时指标
			if a.adapter != nil {
				a.adapter.RecordLLMDuration(time.Since(llmStartTime))
			}

			// Publish thinking event (reasoning content) before llm_call_end
			if llmErr == nil && a.Publisher != nil && len(resp.Choices) > 0 {
				reasoning := resp.Choices[0].Reasoning
				if reasoning != "" {
					a.Publisher.Publish("thinking", map[string]interface{}{
						"content": reasoning,
						"model":   a.LLM.Model(),
						"agent":   a.Name(),
					}, a.Name())
				}
			}

			// Publish llm_call_end event
			if a.Publisher != nil {
				metadata := map[string]interface{}{
					"model":            a.LLM.Model(),
					"agent":            a.Name(),
					"duration_seconds": llmDuration,
				}
				if llmErr != nil {
					metadata["error"] = llmErr.Error()
				}
				a.Publisher.PublishWithMetadata("llm_call_end", "", a.Name(), metadata)
			}

			if llmErr == nil {
				// 通过适配器记录成功（重置熔断器状态）
				if a.adapter != nil {
					a.adapter.RecordLLMSuccess()
				}
				break
			}
			// 通过适配器记录失败（可能触发熔断）
			if a.adapter != nil {
				a.adapter.RecordLLMFailure()
			}
			a.consecutiveLLMFailures++
			a.lastLLMFailureTime = time.Now()
			slog.Warn("ConductorAgent LLM error, will retry",
				"error", llmErr, "step", i, "attempt", attempt,
				"consecutive_failures", a.consecutiveLLMFailures)
		}

		if llmErr != nil {
			slog.Error("ConductorAgent LLM error after all retries",
				"error", llmErr, "step", i)
			return "", llmErr
		}

		choice := resp.Choices[0]
		slog.Debug("ConductorAgent LLM response", "step", i, "content", choice.Content, "tool_calls", len(choice.ToolCalls))

		if choice.Content != "" {
			if a.Publisher != nil {
				metadata := map[string]interface{}{}
				if resp.Usage != nil {
					metadata["usage"] = map[string]interface{}{
						"prompt_tokens":               resp.Usage.PromptTokens,
						"completion_tokens":           resp.Usage.CompletionTokens,
						"total_tokens":                resp.Usage.TotalTokens,
						"cache_creation_input_tokens": resp.Usage.CacheCreationInputTokens,
						"cache_read_input_tokens":     resp.Usage.CacheReadInputTokens,
					}
				}
				if len(metadata) > 0 {
					a.Publisher.PublishWithMetadata("ai_response", choice.Content, a.Name(), metadata)
				} else {
					a.Publisher.Publish("ai_response", choice.Content, a.Name())
				}
			}
		}

		if mem != nil {
			mem.AddAssistantMessage(choice.Content, convertToolCalls(choice.ToolCalls))
		}

		messages = append(messages, llm.Message{
			Role:      llm.RoleAssistant,
			Content:   choice.Content,
			Reasoning: choice.Reasoning,
			ToolCalls: choice.ToolCalls,
		})

		if len(choice.ToolCalls) == 0 {
			// LLM 没有调用任何委派工具，强制它调用工具
			messages = append(messages, llm.Message{
				Role:    llm.RoleUser,
				Content: "You must use a delegate tool (like delegate_repo, delegate_coding, agent_exit etc.) to proceed. Please do not just return text.",
			})
			continue // 进入下一次循环，让 LLM 重新生成包含工具调用的响应
		}

		for _, tc := range choice.ToolCalls {
			var toolResult string
			var err error
			found := false

			if a.Publisher != nil {
				a.Publisher.Publish("tool_call_start", map[string]interface{}{
					"tool_name":    tc.Function.Name,
					"arguments":    tc.Function.Arguments,
					"tool_call_id": tc.ID,
				}, a.Name())
			}
			for _, t := range a.Adapters {
				if t.Name() == tc.Function.Name {
					found = true
					toolResult, err = t.Call(ctx, tc.Function.Arguments)

					// 注入 sub-agent memory（delegate 闭包中设置了 pendingSubAgentMemory）
					if a.pendingSubAgentMemory != nil {
						a.injectSubAgentMemory(*a.pendingSubAgentMemory, tc.ID, tc.Function.Name)
						a.pendingSubAgentMemory = nil
					}

					// 检测是否是 delegate 工具，无论成功失败都记录尝试次数
					if strings.HasPrefix(t.Name(), "delegate_") {
						a.delegationAttempts++
						if err == nil {
							a.hasDelegated = true
						}
					}

					if err != nil {
						// 截断过长的错误消息，避免污染上下文
						errMsg := err.Error()
						if len(errMsg) > 1000 {
							errMsg = errMsg[:1000] + "... [truncated]"
						}
						toolResult = fmt.Sprintf("Error: %s", errMsg)
					} else if t.Name() == "delegate_repo" {
						// toolResult is a JSON string (e.g. "\"summary...\""), so we need to unmarshal it
						// to get the actual text content
						var summary string
						if err := json.Unmarshal([]byte(toolResult), &summary); err == nil {
							a.GlobalCtx.RepoSummary = summary
						} else {
							a.GlobalCtx.RepoSummary = toolResult
						}
					}
					break
				}
			}
			if !found {
				toolResult = fmt.Sprintf("Tool %s not found", tc.Function.Name)
			}

			if a.Publisher != nil {
				a.Publisher.Publish("tool_call_result", map[string]interface{}{
					"tool_name":    tc.Function.Name,
					"result":       toolResult,
					"tool_call_id": tc.ID,
				}, a.Name())
			}

			if mem != nil {
				mem.AddToolMessage(toolResult, tc.ID)
			}

			messages = append(messages, llm.Message{
				Role:       llm.RoleTool,
				Content:    toolResult,
				ToolCallID: tc.ID,
				ToolName:   tc.Function.Name,
			})
			if tc.Function.Name == "agent_exit" {
				return "Task completed successfully", nil
			}

		}
	}

	// ═══════ 清理异步压缩资源 ═══════
	if a.asyncCompactor != nil {
		a.asyncCompactor.Stop()
	}

	return "", fmt.Errorf("ConductorAgent exceeded max steps")
}

// createSummaryClient 创建用于上下文摘要的轻量LLM客户端
// 如果配置了独立的 summaryEngine 则优先使用，否则复用主引擎
func (a *ConductorAgent) createSummaryClient() compact.SummarizationClient {
	engine := a.LLM
	if a.summaryEngine != nil {
		engine = a.summaryEngine
	}
	return &summaryClientAdapter{
		LLM:         engine,
		Model:       a.compactConfig.SummarizationModel,
		Temperature: 0.1,  // 摘要使用低温，确保一致性
		MaxTokens:   2000, // 摘要输出限制
	}
}

// summaryClientAdapter 将 llm.Engine 适配为 compact.SummarizationClient
type summaryClientAdapter struct {
	LLM         llm.Engine
	Model       string
	Temperature float64
	MaxTokens   int
}

func (s *summaryClientAdapter) GenerateSummary(ctx context.Context, messages []llm.Message) (string, error) {
	// 构造摘要请求：System prompt + 待摘要消息
	allMessages := append([]llm.Message{
		{
			Role:    llm.RoleSystem,
			Content: getSummarizationPrompt(),
		},
	}, messages...)

	opts := &llm.CallOptions{
		MaxTokens:   s.MaxTokens,
		Temperature: s.Temperature,
	}
	resp, err := s.LLM.GenerateContent(ctx, allMessages, nil, opts)
	if err != nil {
		return "", fmt.Errorf("summarization failed: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("summarization returned empty response")
	}
	return resp.Choices[0].Content, nil
}

// getSummarizationPrompt 返回默认摘要提示词（英文版本）
func getSummarizationPrompt() string {
	return `# Role
You are a **Conversation Summarizer** for an AI-powered coding assistant system. Your task is to compress conversation history without losing any critical context needed for ongoing development work.

# Task
Extract the following from the provided conversation fragment:

1. **Task Progress**: What tasks have been completed? What is currently in progress?
2. **Key Decisions**: What important architectural or design decisions were made? Why?
3. **Code Changes**: Which files were modified? What are the key code patterns introduced?
4. **Errors & Fixes**: What problems were encountered? How were they resolved?
5. **Critical Discoveries**: Important facts about the codebase — file structure, dependencies, tech stack, conventions, etc.

# Rules
- **Preserve Identifiers**: Retain ALL specific identifiers — file names, function names, class names, variable names, paths.
- **Preserve Error Details**: Keep concrete error messages and their corresponding fix strategies verbatim.
- **Ignore Redundancy**: Skip duplicated tool output content; keep only the meaningful results.
- **Be Complete**: Do NOT omit any context that could be useful for continuing the work.
- **Be Concise**: Summarize efficiently; prefer bullet points over verbose prose.

# Output Format
- Use clear, structured Markdown.
- Output in **English**.
- Organize extracted information under the 5 categories listed above.`
}

// validateAndRepairToolCallPairs 检查 messages 中的 tool_call/tool_response 配对完整性
// 如果发现孤立的 tool_calls（assistant 有 tool_calls 但缺少对应的 tool 响应），自动修复：
// 1. 将缺少 tool 响应的 assistant 消息降级为纯文本消息（添加说明）
// 2. 移除孤立的 tool 响应（没有对应 assistant 的消息）
func validateAndRepairToolCallPairs(messages []llm.Message) []llm.Message {
	result := make([]llm.Message, 0, len(messages))

	for i := 0; i < len(messages); i++ {
		msg := messages[i]

		if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) > 0 {
			// 收集这个 assistant 消息的所有 tool_call_ids
			expectedIDs := make(map[string]bool)
			for _, tc := range msg.ToolCalls {
				expectedIDs[tc.ID] = true
			}

			// 向后扫描，找到所有 tool 响应
			foundIDs := make(map[string]bool)
			j := i + 1
			for j < len(messages) && len(foundIDs) < len(expectedIDs) {
				if messages[j].Role == llm.RoleTool && messages[j].ToolCallID != "" {
					if expectedIDs[messages[j].ToolCallID] {
						foundIDs[messages[j].ToolCallID] = true
					}
				} else if messages[j].Role == llm.RoleAssistant || messages[j].Role == llm.RoleUser {
					// 遇到新的 assistant 或 user 消息，停止扫描
					break
				}
				j++
			}

			// 检查是否所有 tool_calls 都有对应的 tool 响应
			if len(foundIDs) < len(expectedIDs) {
				// 有缺失的 tool 响应！修复：将 assistant 降级为纯文本
				missingIDs := make([]string, 0)
				for id := range expectedIDs {
					if !foundIDs[id] {
						missingIDs = append(missingIDs, id)
					}
				}

				repairedContent := msg.Content
				if repairedContent == "" {
					repairedContent = "[系统提示：以下工具调用结果因上下文压缩而不可用]"
				} else {
					repairedContent += "\n\n[系统提示：以下工具调用结果因上下文压缩而不可用：" + strings.Join(missingIDs, ", ") + "]"
				}

				result = append(result, llm.Message{
					Role:    llm.RoleAssistant,
					Content: repairedContent,
				})

				// 将已找到的 tool 响应也添加到结果中
				for k := i + 1; k < j; k++ {
					if messages[k].Role == llm.RoleTool && messages[k].ToolCallID != "" {
						if foundIDs[messages[k].ToolCallID] {
							result = append(result, messages[k])
						}
					}
				}

				i = j - 1 // 跳过已处理的消息
				continue
			}
		}

		// 检查孤立的 tool 响应（前面没有匹配的 assistant）
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
				// 孤立的 tool 响应，跳过
				continue
			}
		}

		result = append(result, msg)
	}

	return result
}
