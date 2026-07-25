package agents

import (
	"context"
	_ "embed"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	director "codeactor/internal/agents/director"
	"codeactor/internal/config"
	"codeactor/internal/globalctx"
	"codeactor/internal/llm"
	"codeactor/internal/memory"
	"codeactor/internal/tools"
)

//go:embed director.prompt.md
var directorPrompt string

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

type DirectorAgent struct {
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
	adapter        *DirectorAdapter                // 新旧整合适配器
	llmClient      *llm.Client                     // LLM客户端引用，用于运行时动态重新解析引擎

	cachedProjectContext *ProjectContextLoadResult // 缓存项目上下文文件（同一会话只加载一次）
	hasDelegated         bool                      // 标记是否已委派过 agent
	delegationAttempts   int                       // 委派尝试次数统计

	currentMemory         *memory.ConversationMemory // 当前正在使用的 memory（Run 期间设置）
	pendingSubAgentMemory *AgentResult               // 最近一次 delegate 调用的完整结果（用于 memory 注入）

	// LLM 兜底机制字段
	stepRetries                int           // 步骤重试次数（从 config.LLM.StepRetries 读取）
	llmTimeout                 time.Duration // LLM调用超时，从配置读取，默认3分钟
	circuitBreakerThreshold    int           // 熔断阈值（连续失败次数），0=不启用
	circuitBreakerResetTimeout time.Duration // 熔断恢复时间
	consecutiveLLMFailures     int           // 连续 LLM 调用失败计数
	lastLLMFailureTime         time.Time     // 最近一次 LLM 失败时间

	// EnhancedCommander 增强型配置
	EnhancedCommanderCfg config.EnhancedCommanderConfig
	// resultCompressor 结果压缩器（nil 表示不启用压缩）
	resultCompressor *ResultCompressor
	// MemoryJSONL 配置
	memoryJSONLCfg config.MemoryJSONLConfig
}

// loadProjectContext 读取工作区目录下的项目上下文文件（CODEACTOR.md、CLAUDE.md、AGENTS.md），
// 将成功读取的文件内容格式化后组合返回。文件按顺序尝试，不存在或读取失败时忽略。
// 返回加载的文件列表和组合后的内容。
func (a *DirectorAgent) loadProjectContext() *ProjectContextLoadResult {
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

func NewDirectorAgent(globalCtx *globalctx.GlobalCtx, engine llm.Engine, repo *RepoAgent, coding *CodingAgent, chat *ChatAgent, meta *MetaAgent, devops *DevOpsAgent, browser *BrowserAgent, maxSteps int, disabledAgents map[string]bool, metaRetryCount int, cfg config.Config, llmClient *llm.Client) *DirectorAgent {
	// self-reference for closures that need the DirectorAgent after construction
	var self *DirectorAgent

	delegateRepo := tools.NewAdapter("delegate_repo", "Delegate analysis task to Repo-Agent", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		// 创建 JSONL Writer 并注入到 context
		if writer := self.createJSONLWriter("repo", task); writer != nil {
			defer writer.Close()
			ctx = memory.WithJSONLWriter(ctx, writer)
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
		// 创建 JSONL Writer 并注入到 context
		if writer := self.createJSONLWriter("coding", task); writer != nil {
			defer writer.Close()
			ctx = memory.WithJSONLWriter(ctx, writer)
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
		// 创建 JSONL Writer 并注入到 context
		if writer := self.createJSONLWriter("chat", task); writer != nil {
			defer writer.Close()
			ctx = memory.WithJSONLWriter(ctx, writer)
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
		// 创建 JSONL Writer 并注入到 context
		if writer := self.createJSONLWriter("devops", task); writer != nil {
			defer writer.Close()
			ctx = memory.WithJSONLWriter(ctx, writer)
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
			// 创建 JSONL Writer 并注入到 context
			if writer := self.createJSONLWriter("browser", task); writer != nil {
				defer writer.Close()
				ctx = memory.WithJSONLWriter(ctx, writer)
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
		// 创建 JSONL Writer 并注入到 context（只在最外层注入）
		if writer := self.createJSONLWriter("meta", task); writer != nil {
			defer writer.Close()
			ctx = memory.WithJSONLWriter(ctx, writer)
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
						slog.Info("Director executing newly designed agent", "delegate", delegateName, "display_name", execResult.AgentName)
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
		case "ask_user_for_help":
			if globalCtx.FullYoloMode {
				continue
			}
			fn = globalCtx.FlowOps.ExecuteAskUserForHelp
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

	allAdapters := append(adapters, delegateAdapters...)

	// Strangler Fig: 创建适配器桥接层，开始使用新组件（Metrics + CircuitBreaker）
	adapterCfg := director.DefaultRecoveryConfig()
	adapterCfg.MaxRetries = maxSteps // 使用 maxSteps 作为重试次数
	adapterCfg.CircuitBreakerThreshold = cfg.LLM.CircuitBreakerThreshold
	adapterCfg.CircuitBreakerResetTimeout = cfg.LLM.CircuitBreakerResetTimeout
	directorAdapter := NewDirectorAdapter(true, adapterCfg) // enabled=true 启动 Metrics

	self = &DirectorAgent{
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
		adapter:            directorAdapter,
		llmClient:          llmClient,
		hasDelegated:       false, // 初始未委派过
		delegationAttempts: 0,             // 委派尝试次数初始为0

		// LLM 兜底机制配置
		stepRetries:                cfg.LLM.StepRetries,
		llmTimeout: func() time.Duration {
			if cfg.LLM.Timeout > 0 {
				return cfg.LLM.Timeout
			}
			return 5 * time.Minute
		}(),
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
	slog.Info("Director tool definitions initialized",
		"hash", toolHash,
		"tool_count", len(toolDefsForHash),
		"tool_names", names)

	return self
}

func (a *DirectorAgent) Name() string {
	return "Director"
}

// getToolFunc returns the ToolFunc implementation for a given tool name.
// This is used when constructing tool adapters for dynamically created agents.
func (a *DirectorAgent) getToolFunc(name string) tools.ToolFunc {
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
	case "list_dir":
		return a.GlobalCtx.FileOps.ExecuteListDir
	case "print_dir_tree":
		return a.GlobalCtx.FileOps.ExecutePrintDirTree
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
		if a.GlobalCtx.FullYoloMode {
			return nil
		}
		return a.GlobalCtx.FlowOps.ExecuteAskUserForHelp
	default:
		return nil
	}
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
// and adds it to the Director's Adapters list. The agent becomes permanently available.
func (a *DirectorAgent) registerCustomAgent(ca *CustomAgent) {
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
func (a *DirectorAgent) executeCustomAgent(ctx context.Context, ca *CustomAgent, adapters []*tools.Adapter, task string) (string, error) {
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
	cfg.LLMTimeout = a.llmTimeout
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

// injectSubAgentMemory 将 sub-agent 的执行结果摘要注入到 Director memory 中
// Phase 1: 只注入摘要，不再注入 sub-agent 的完整对话历史
// Phase 3+ : Sub-agent 的关键发现通过 SharedMemory 发布/订阅机制共享
func (a *DirectorAgent) injectSubAgentMemory(result AgentResult, toolCallID string, toolName string) {
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

	// 重要：result.Memory（sub-agent 的完整对话历史）不再注入到 Director 的 memory 中
	// 这避免了 Director 上下文快速膨胀和 Compact Engine 频繁压缩造成的信息丢失
	// sub-agent 内部消息保留在 sub-agent 本地，通过 SharedMemory 的 publish/subscribe 机制共享关键信息（Phase 3）
}

// SetMemoryJSONLConfig 设置 MemoryJSONL 配置（由 app.go 在初始化后调用）
func (a *DirectorAgent) SetMemoryJSONLConfig(cfg config.MemoryJSONLConfig) {
	a.memoryJSONLCfg = cfg
}

// createJSONLWriter 为 delegate 创建 JSONL 写入器（如果启用）
// 返回 nil 表示未启用或创建失败（失败时仅警告，不阻断执行）
func (a *DirectorAgent) createJSONLWriter(agentName, task string) *memory.JSONLWriter {
	if !a.memoryJSONLCfg.Enable {
		return nil
	}

	// 计算 projectID
	projectID := a.computeProjectID()

	// 将 config.MemoryJSONLConfig 转换为 memory.MemoryJSONLConfig
	memoryCfg := memory.MemoryJSONLConfig{
		Enable:    a.memoryJSONLCfg.Enable,
		OutputDir: a.memoryJSONLCfg.OutputDir,
	}

	writer, err := memory.NewJSONLWriter(memoryCfg, projectID, agentName, task)
	if err != nil {
		slog.Warn("JSONL: failed to create writer, continuing without jsonl logging",
			"agent", agentName,
			"error", err,
		)
		return nil
	}

	slog.Debug("JSONL: writer created for delegate agent",
		"agent", agentName,
		"file", writer.FilePath(),
	)

	return writer
}

// computeProjectID 从项目路径计算文件系统安全的 projectID
func (a *DirectorAgent) computeProjectID() string {
	projectPath := a.GlobalCtx.ProjectPath
	if projectPath == "" {
		return "default"
	}
	base := filepath.Base(projectPath)
	if base == "." || base == "/" {
		base = "root"
	}
	// 保留字母数字，其余替换为下划线
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, base)
	// 限制长度
	if len(sanitized) > 100 {
		sanitized = sanitized[:100]
	}
	// 添加短哈希
	h := sha256.Sum256([]byte(projectPath))
	shortHash := hex.EncodeToString(h[:])[:8]
	return sanitized + "_" + shortHash
}

// applyEnhancedCommander 对子 Agent 执行结果应用增强型 Commander 功能。
// 包含：结果压缩（如果启用）、结果注册（如果启用）
// agentType: 子 Agent 类型（如 "repo", "coding"）
// task: 委派的任务描述
// result: Agent 执行结果
// err: Agent 执行错误
// 返回: 处理后的结果文本和错误
func (a *DirectorAgent) applyEnhancedCommander(
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

func (a *DirectorAgent) Run(ctx context.Context, input string, mem *memory.ConversationMemory) (string, error) {
	// 设置当前 memory（delegate 闭包通过 a.currentMemory 访问）
	a.currentMemory = mem

	// 从 llmClient 刷新引擎，确保 TUI 中切换模型后立即生效
	if a.llmClient != nil {
		newEngine := a.llmClient.GetAgentEngine("director")
		if newEngine != nil {
			a.LLM = newEngine
		}
	}
	defer func() { a.currentMemory = nil }()

	if mem != nil {
		// Check if the last message is the same as input to avoid duplication
		// because handleChatMessage might have already added it.
		lastMsg := mem.GetLastMessage()
		if lastMsg == nil || lastMsg.Content != input || lastMsg.Type != memory.MessageTypeHuman {
			mem.AddHumanMessage(input)
		}
	}

	var messages []llm.Message

	// Always start with System Prompt (with any registered custom agents appended)
	systemPrompt := a.GlobalCtx.FormatPrompt(directorPrompt)
	// Inject shared memory (3 dimensions: user, feedback, reference)
	systemPrompt = a.InjectSharedMemory(systemPrompt, "default", a.GlobalCtx.ProjectPath)
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

	// ═══════ 初始化 Director JSONL Writer ═══════
	var directorWriter *memory.JSONLWriter
	if w := a.createJSONLWriter("director", input); w != nil {
		directorWriter = w
		defer func() {
			if directorWriter != nil {
				directorWriter.Close()
			}
		}()
	}

	writeDirectorJSONL := func(msg llm.Message) {
		if directorWriter == nil {
			return
		}
		if err := directorWriter.WriteMessage(msg); err != nil {
			slog.Warn("JSONL: failed to write director message",
				"error", err,
			)
		}
	}
	// ═══════ END Director JSONL Writer ═══════

	// ═══════ 写入初始消息（system prompt + user input）到 Director JSONL ═══════
	initialMsgCount := len(messages)
	for i := 0; i < initialMsgCount; i++ {
		writeDirectorJSONL(messages[i])
	}

	for i := 0; i < a.maxSteps; i++ {
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
				slog.Warn("DirectorAgent retrying LLM call", "step", i, "attempt", attempt, "wait", wait)
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-time.After(wait):
				}
			}

			// 验证并修复 tool_call/tool_response 配对完整性
			messages = validateAndRepairToolCallPairs(messages)

			slog.Debug("DirectorAgent calling LLM", "step", i, "messages", messages)

			// Publish llm_call_start event
			if a.Publisher != nil {
				a.Publisher.Publish("llm_call_start", map[string]interface{}{
					"model": a.LLM.Model(),
					"agent": a.Name(),
				}, a.Name())
			}

			llmStartTime := time.Now()
			// 使用可配置的 LLM 超时保护，防止远程服务无响应时永久阻塞
			llmCtx, llmCancel := context.WithTimeout(ctx, a.llmTimeout)
			resp, llmErr = a.LLM.GenerateContent(llmCtx, messages, toolDefs, nil)
			llmCancel()
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
			slog.Warn("DirectorAgent LLM error, will retry",
				"error", llmErr, "step", i, "attempt", attempt,
				"consecutive_failures", a.consecutiveLLMFailures)
		}

		if llmErr != nil {
			slog.Error("DirectorAgent LLM error after all retries",
				"error", llmErr, "step", i)
			return "", llmErr
		}

		choice := resp.Choices[0]
		slog.Debug("DirectorAgent LLM response", "step", i, "content", choice.Content, "tool_calls", len(choice.ToolCalls))

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

		// 写入 assistant 消息到 Director JSONL
		writeDirectorJSONL(llm.Message{
			Role:      llm.RoleAssistant,
			Content:   choice.Content,
			Reasoning: choice.Reasoning,
			ToolCalls: choice.ToolCalls,
		})

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

					// Log delegate tool calls with full arguments to dedicated delegate log
					if strings.HasPrefix(t.Name(), "delegate_") {
						agentName := strings.TrimPrefix(t.Name(), "delegate_")
						LogDelegateCall(t.Name(), agentName, tc.Function.Arguments)
					}

					// 工具调用前检查 context
					if ctx.Err() != nil {
						return "", ctx.Err()
					}

					// 为工具调用添加超时保护（防止工具无限阻塞）
					// delegate_* 工具涉及子 agent 完整执行（多轮 LLM + 工具调用），需要更长的超时时间
					// 使用 WithCancel 剥离父 context 的 deadline，再 WithTimeout 添加独立超时
					// 这确保工具获得完整的超时时间，不受父 context 剩余时间限制
					toolTimeout := 120 * time.Second
					if strings.HasPrefix(tc.Function.Name, "delegate_") {
						toolTimeout = 10 * time.Minute // 子 agent 需要更多时间完成多轮交互
					}
					cancelCtx, cancelCtxCancel := context.WithCancel(ctx)
					toolCtx, toolCancel := context.WithTimeout(cancelCtx, toolTimeout)
					toolResult, err = t.Call(toolCtx, tc.Function.Arguments)
					cancelCtxCancel()
					toolCancel()

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
						// 对超时错误给出明确的超时时间提示
						if errors.Is(err, context.DeadlineExceeded) {
							timeoutMinutes := int(toolTimeout.Minutes())
							toolResult = fmt.Sprintf("Error: tool execution timed out after %d seconds", timeoutMinutes*60)
						} else {
							toolResult = fmt.Sprintf("Error: %s", errMsg)
						}
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

			// 写入 tool 消息到 Director JSONL
			writeDirectorJSONL(llm.Message{
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

	return "", fmt.Errorf("DirectorAgent exceeded max steps")
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
