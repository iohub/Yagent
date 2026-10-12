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

const maxNonDelegationPrompts = 3

type CustomAgent = director.CustomAgent

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
	metaRetryCount int
	toolDefMap     map[string]tools.ToolDefinition
	metaHandler    *director.MetaAgentHandler
	adapter        *DirectorAdapter
	llmClient      *llm.Client

	projectCtxLoader *director.ProjectContextLoader

	currentMemory         *memory.ConversationMemory
	pendingSubAgentMemory *AgentResult
	pendingSubAgentMu     sync.Mutex
	compressor            *compression.ContextCompressor

	injectSubAgentMemoryMu sync.Mutex

	llmTimeout time.Duration

	delegateIdleTimeout  time.Duration
	delegateTotalTimeout time.Duration

	maxParallelReadOnlyTools int

	EnhancedCommanderCfg config.EnhancedCommanderConfig
	thinkLink            *thinklink.Store
	taskID               string
	resumedTask          bool
	boardID              string
}

func (a *DirectorAgent) loadProjectContext() *director.ProjectContextLoadResult {
	return a.projectCtxLoader.Load()
}

// DirectorAgentParams 聚合 NewDirectorAgent 所需的全部依赖。
// 用具名字段替代原先 27 个位置参数，让调用点自文档化，且增删依赖不再破坏函数签名。
type DirectorAgentParams struct {
	Formatter   PromptFormatter
	Publisher   EventBus
	Env         Env
	Files       FileToolSet
	Search      SearchToolSet
	Sys         SysToolSet
	Edit        EditToolSet
	Flow        FlowToolSet
	Thinker     Thinker
	MicroAgent  MicroAgentRunner
	DeepThinker DeepThinker
	Guard       *tools.WorkspaceGuard
	RepoCtx     RepoContextStore
	Knowledge   KnowledgeProvider
	MCPClient   *mcp.MCPClient
	LLM         llm.Engine

	RepoAgent    *RepoAgent
	CodingAgent  *CodingAgent
	ChatAgent    *ChatAgent
	MetaAgent    *MetaAgent
	DevOpsAgent  *DevOpsAgent
	BrowserAgent *BrowserAgent

	MaxSteps       int
	DisabledAgents map[string]bool
	MetaRetryCount int
	Config         config.Config
	LLMClient      *llm.Client
}

// NewDirectorAgent 构造 DirectorAgent：先填充结构体字段，再装配工具适配器
// （基础/JSON 工具 → 委派工具 → 知识工具），最后完成 blackboard、压缩器等后置初始化。
// 委派闭包在运行时读取接收者字段，因此必须先构造 self 再调用两个 build* 方法。
func NewDirectorAgent(p DirectorAgentParams) *DirectorAgent {
	adapterCfg := director.DefaultRecoveryConfig()
	adapterCfg.MaxRetries = p.MaxSteps
	adapterCfg.CircuitBreakerThreshold = p.Config.LLM.CircuitBreakerThreshold
	adapterCfg.CircuitBreakerResetTimeout = p.Config.LLM.CircuitBreakerResetTimeout
	adapterCfg.LLMRetries = p.Config.LLM.StepRetries

	self := &DirectorAgent{
		BaseAgent:        BaseAgent{LLM: p.LLM, Publisher: p.Publisher},
		RepoAgent:        p.RepoAgent,
		CodingAgent:      p.CodingAgent,
		ChatAgent:        p.ChatAgent,
		MetaAgent:        p.MetaAgent,
		DevOpsAgent:      p.DevOpsAgent,
		BrowserAgent:     p.BrowserAgent,
		promptFmt:        p.Formatter,
		env:              p.Env,
		files:            p.Files,
		search:           p.Search,
		sys:              p.Sys,
		edit:             p.Edit,
		flow:             p.Flow,
		thinker:          p.Thinker,
		microAgent:       p.MicroAgent,
		deepThinker:      p.DeepThinker,
		guard:            p.Guard,
		repoCtx:          p.RepoCtx,
		knowledge:        p.Knowledge,
		mcpClient:        p.MCPClient,
		maxSteps:         p.MaxSteps,
		metaRetryCount:   p.MetaRetryCount,
		metaHandler:      director.NewMetaAgentHandler(),
		adapter:          NewDirectorAdapter(true, adapterCfg),
		llmClient:        p.LLMClient,
		projectCtxLoader: director.NewProjectContextLoader(func() string { return p.Env.ProjectPath() }),

		llmTimeout: func() time.Duration {
			// 三级优先：全局运行时值（TUI :timeout 可动态修改）> 配置文件 > 程序默认值。
			// 构造期快照仅作兜底；运行期真实值见 currentLLMTimeout()。
			if t := config.GlobalLLMTimeout(); t > 0 {
				return t
			}
			if p.Config.LLM.Timeout > 0 {
				return p.Config.LLM.Timeout
			}
			return config.DefaultLLMCallTimeout
		}(),

		delegateIdleTimeout: func() time.Duration {
			if p.Config.Agent.DelegateIdleTimeout > 0 {
				return p.Config.Agent.DelegateIdleTimeout
			}
			return config.DeriveDelegateIdleTimeout(p.Config.LLM.Timeout)
		}(),
		delegateTotalTimeout: p.Config.Agent.DelegateTotalTimeout,

		maxParallelReadOnlyTools: NormalizeMaxParallelReadOnlyTools(p.Config.Agent.MaxParallelReadOnlyTools),

		EnhancedCommanderCfg: p.Config.EnhancedCommander,

		thinkLink: thinklink.NewStore(0),
	}

	adapters := self.buildToolAdapters()
	delegateAdapters := self.buildDelegateAdapters(p.DisabledAgents)

	tools.SetGuardOnAdapters(adapters, p.Guard)
	tools.SetGuardOnAdapters(delegateAdapters, p.Guard)

	knowledgeAdapters := createKnowledgeToolAdapters(p.MCPClient, p.LLM, "", "")
	allAdapters := append(adapters, delegateAdapters...)
	if len(knowledgeAdapters) > 0 {
		tools.SetGuardOnAdapters(knowledgeAdapters, p.Guard)
		allAdapters = append(allAdapters, knowledgeAdapters...)
	}
	self.Adapters = allAdapters

	if self.boardID == "" {
		self.boardID = blackboard.GenerateAdhocID()
	}

	self.compressor = compression.NewContextCompressor(p.LLM, self.Name(), self.thinkLink, func() { self.setPendingSubAgentMemory(nil) })

	if self.env != nil {
		SetProjectPathProvider(self.env.ProjectPath)
	}

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

// buildToolAdapters 装配 Director 的基础工具（agent_exit/read_artifact/read_note）
// 与 tools.json 中声明的只读工具，并顺带填充 a.toolDefMap（供 registerCustomAgent 查询）。
func (a *DirectorAgent) buildToolAdapters() []*tools.Adapter {
	adapters := []*tools.Adapter{
		tools.NewAdapter("agent_exit", "Exit the agent with a reason. Use this when you are done — whether the task completed successfully, failed, needs clarification, or must be terminated. The reason must explain WHY the agent is exiting.", a.flow.ExecuteAgentExit).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"reason": map[string]interface{}{"type": "string", "description": "The reason the agent is exiting, e.g., task completed, cannot proceed, blocked by missing information, or must terminate."},
			},
			"required": []string{"reason"},
		}),
		newReadArtifactAdapter(),
		newReadNoteAdapter(func() string { return a.boardID }),
	}

	var toolDefs []tools.ToolDefinition
	if err := json.Unmarshal(ToolsJSON, &toolDefs); err != nil {
		slog.Error("Failed to unmarshal tools", "error", err)
	}

	toolDefMap := make(map[string]tools.ToolDefinition, len(toolDefs))
	for _, def := range toolDefs {
		toolDefMap[def.Name] = def
	}
	a.toolDefMap = toolDefMap

	for _, def := range toolDefs {
		var fn tools.ToolFunc
		switch def.Name {
		case "search_by_regex":
			fn = a.search.ExecuteGrepSearch
		case "list_dir":
			fn = a.files.ExecuteListDir
		case "read_file":
			fn = a.files.ExecuteReadFile
		case "print_dir_tree":
			fn = a.files.ExecutePrintDirTree
		case "deepthinking":
			fn = a.deepThinker.Execute
		case "ask_user_for_help":
			if a.env.FullYoloMode() {
				continue
			}
			fn = a.flow.ExecuteAskUserForHelp
		default:
			continue
		}

		adapter := tools.NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters).WithReadOnlyIfKnown()
		adapters = append(adapters, adapter)
	}

	return adapters
}

// routeCtx 对 delegate 子任务做难度分类，把选中的档位写入 ctx（供子 agent 的
// RunAgentLoop 解析实际引擎），并发布 model_routed 事件。自动路由未启用时原样返回。
// 分类失败会安全回退到该 agent 的角色基线档位，绝不阻塞委派。
func (a *DirectorAgent) routeCtx(ctx context.Context, agentName, task string) context.Context {
	if a.llmClient == nil {
		return ctx
	}
	r := a.llmClient.GetRouter()
	if !r.Enabled() {
		return ctx
	}
	baseline := r.BaselineOf(agentName)
	tier := r.Classify(ctx, task, baseline)
	ctx = llm.WithTier(ctx, tier)
	if a.Publisher != nil {
		a.Publisher.Publish("model_routed", map[string]interface{}{
			"agent":    agentName,
			"tier":     tier.String(),
			"model":    r.ModelFor(tier),
			"score":    r.ScoreFor(tier),
			"baseline": baseline.String(),
		}, a.Name())
	}
	return ctx
}

// buildDelegateAdapters 装配六个内建 delegate_<agent> 工具，并按 disabledAgents 过滤。
// 闭包在运行时读取接收者字段（boardID/Adapters/metaRetryCount 等），故必须在结构体
// 填充完成后调用。返回顺序固定为：repo, coding, chat, meta, devops, browser。
func (a *DirectorAgent) buildDelegateAdapters(disabledAgents map[string]bool) []*tools.Adapter {
	delegateRepo := tools.NewAdapter("delegate_repo", "Delegate analysis task to Repo-Agent", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		packedTask, unresolved := a.resolveDelegateRefs(ctx, task, params)
		ctx = blackboard.WithBoardID(ctx, a.boardID)
		if rolloutWriter := a.createRolloutWriter("repo", task); rolloutWriter != nil {
			defer rolloutWriter.Close()
			ctx = memory.WithRolloutWriter(ctx, rolloutWriter)
		}
		ctx = a.routeCtx(ctx, "repo", task)
		result, err := a.RepoAgent.Run(ctx, packedTask)
		noteRef := a.writeDelegateNote("repo_agent", "delegate_repo", task, result, err)
		return a.applyEnhancedCommander("repo", task, result, err, noteRef, unresolved)
	}).WithSchema(buildDelegateSchema("The task description for Repo-Agent")).WithReadOnly(true)

	delegateCoding := tools.NewAdapter("delegate_coding", "Delegate coding task to Coding-Agent", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		packedTask, unresolved := a.resolveDelegateRefs(ctx, task, params)
		ctx = blackboard.WithBoardID(ctx, a.boardID)
		if rolloutWriter := a.createRolloutWriter("coding", task); rolloutWriter != nil {
			defer rolloutWriter.Close()
			ctx = memory.WithRolloutWriter(ctx, rolloutWriter)
		}
		ctx = a.routeCtx(ctx, "coding", task)
		result, err := a.CodingAgent.Run(ctx, packedTask)
		noteRef := a.writeDelegateNote("coding_agent", "delegate_coding", task, result, err)
		return a.applyEnhancedCommander("coding", task, result, err, noteRef, unresolved)
	}).WithSchema(buildDelegateSchema("The task description for Coding-Agent"))

	delegateChat := tools.NewAdapter("delegate_chat", "Delegate general conversation, explanation, or non-coding tasks to Chat-Agent", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		packedTask, unresolved := a.resolveDelegateRefs(ctx, task, params)
		ctx = blackboard.WithBoardID(ctx, a.boardID)
		if rolloutWriter := a.createRolloutWriter("chat", task); rolloutWriter != nil {
			defer rolloutWriter.Close()
			ctx = memory.WithRolloutWriter(ctx, rolloutWriter)
		}
		ctx = a.routeCtx(ctx, "chat", task)
		result, err := a.ChatAgent.Run(ctx, packedTask)
		noteRef := a.writeDelegateNote("chat_agent", "delegate_chat", task, result, err)
		return a.applyEnhancedCommander("chat", task, result, err, noteRef, unresolved)
	}).WithSchema(buildDelegateSchema("The message or question for Chat-Agent")).WithReadOnly(true)

	delegateDevOps := tools.NewAdapter("delegate_devops", "Delegate operational and system administration tasks to DevOps-Agent. DevOps-Agent can run shell commands, inspect files, check logs, manage processes, and perform any non-coding infrastructure work. Use this for tasks like checking disk usage, finding files, running diagnostics, inspecting configurations, or executing ad-hoc shell commands.", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		packedTask, unresolved := a.resolveDelegateRefs(ctx, task, params)
		ctx = blackboard.WithBoardID(ctx, a.boardID)
		if rolloutWriter := a.createRolloutWriter("devops", task); rolloutWriter != nil {
			defer rolloutWriter.Close()
			ctx = memory.WithRolloutWriter(ctx, rolloutWriter)
		}
		ctx = a.routeCtx(ctx, "devops", task)
		result, err := a.DevOpsAgent.Run(ctx, packedTask)
		noteRef := a.writeDelegateNote("devops_agent", "delegate_devops", task, result, err)
		return a.applyEnhancedCommander("devops", task, result, err, noteRef, unresolved)
	}).WithSchema(buildDelegateSchema("The operational task for DevOps-Agent, e.g., 'check disk usage', 'find all log files modified today', 'check if port 8080 is in use'."))

	delegateBrowser := tools.NewAdapter("delegate_browser",
		"Delegate browser automation tasks to Browser-Agent. Browser-Agent controls a headless Chrome browser using go-rod to navigate websites, click elements, fill forms, extract data, take screenshots, generate PDFs, execute JavaScript (with user confirmation), and manage cookies. Use this for tasks like: 'screenshot https://example.com', 'extract text from https://example.com/article', 'fill and submit the login form at https://example.com/login', 'check if website is reachable', 'get the current URL after navigation'. The agent handles all browser lifecycle and page management internally.",
		func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			task, ok := params["task"].(string)
			if !ok {
				return nil, fmt.Errorf("task parameter required")
			}
			packedTask, unresolved := a.resolveDelegateRefs(ctx, task, params)
			ctx = blackboard.WithBoardID(ctx, a.boardID)
			if rolloutWriter := a.createRolloutWriter("browser", task); rolloutWriter != nil {
				defer rolloutWriter.Close()
				ctx = memory.WithRolloutWriter(ctx, rolloutWriter)
			}
			ctx = a.routeCtx(ctx, "browser", task)
			result, err := a.BrowserAgent.Run(ctx, packedTask)
			noteRef := a.writeDelegateNote("browser_agent", "delegate_browser", task, result, err)
			return a.applyEnhancedCommander("browser", task, result, err, noteRef, unresolved)
		}).WithSchema(buildDelegateSchema("The browser automation task for Browser-Agent, e.g., 'screenshot https://example.com homepage', 'extract article text from https://example.com/blog/post-1', 'fill the login form and submit', 'navigate to https://example.com and return the page title'."))

	delegateMeta := tools.NewAdapter("delegate_meta", "Delegate to Meta-Agent to DESIGN a custom specialized agent. Meta-Agent will craft a tailored system prompt using prompt engineering best practices and select appropriate tools. The designed agent is automatically registered and immediately executed to complete the task. After this, the new agent becomes a permanent delegate tool for future use.", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		ctx = blackboard.WithBoardID(ctx, a.boardID)
		if rolloutWriter := a.createRolloutWriter("meta", task); rolloutWriter != nil {
			defer rolloutWriter.Close()
			ctx = memory.WithRolloutWriter(ctx, rolloutWriter)
		}
		slog.Info("Director delegating to Meta-Agent (design)", "task", task)
		ctx = a.routeCtx(ctx, "meta", task)

		maxRetries := a.metaRetryCount
		var lastRawOutput string

		for attempt := 0; attempt < maxRetries; attempt++ {
			retryTask := task
			if attempt > 0 {
				retryTask = fmt.Sprintf(
					"%s\n\n[FORMAT CORRECTION — Attempt %d/%d]\nYour previous output was NOT valid JSON or missing required fields. You MUST output ONLY a valid JSON object with these exact top-level keys:\n{\n  \"thinking\": \"...\",\n  \"agent_name\": \"...\",\n  \"agent_design\": \"...\",\n  \"tools_used\": [...],\n  \"task_for_agent\": \"...\"\n}\n\nDo NOT wrap in markdown code fences (```). Do NOT include any text outside the JSON object.",
					task, attempt, maxRetries-1,
				)
			}

			metaResult, err := a.MetaAgent.Run(ctx, retryTask)
			if err != nil {
				return nil, fmt.Errorf("Meta-Agent design failed: %w", err)
			}
			a.setPendingSubAgentMemory(&metaResult)
			lastRawOutput = metaResult.Text

			systemPrompt, execResult, parseErr := director.ParseMetaAgentOutput(metaResult.Text)
			if parseErr != nil {
				slog.Warn("Meta-Agent JSON parse failed, retrying", "attempt", attempt+1, "maxRetries", maxRetries, "error", parseErr)
				continue
			}

			if execResult.AgentName != "" && systemPrompt != "" {
				snakeName := toSnakeCase(execResult.AgentName)
				customAgent := &CustomAgent{
					Name:         snakeName,
					DisplayName:  execResult.AgentName,
					SystemPrompt: systemPrompt,
					ToolsUsed:    execResult.ToolsUsed,
					Description:  fmt.Sprintf("Custom agent designed for: %s. Uses tools: %s.", execResult.AgentName, strings.Join(execResult.ToolsUsed, ", ")),
				}
				a.registerCustomAgent(customAgent)

				agentTask := execResult.TaskForAgent
				if agentTask == "" {
					agentTask = task
				}

				delegateName := "delegate_" + snakeName
				for _, ad := range a.Adapters {
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

	return delegateAdapters
}

func (a *DirectorAgent) Name() string {
	return "Director"
}

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

func toSnakeCase(name string) string {
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
	raw := result.String()
	for strings.Contains(raw, "__") {
		raw = strings.ReplaceAll(raw, "__", "_")
	}
	raw = strings.Trim(raw, "_")
	if raw == "" {
		raw = "custom_agent"
	}
	return raw
}

func (a *DirectorAgent) registerCustomAgent(ca *CustomAgent) {
	delegateName := "delegate_" + ca.Name

	if err := a.metaHandler.Register(ca); err != nil {
		slog.Info("Custom agent already registered", "name", delegateName)
		return
	}

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

	finishDef, ok := a.toolDefMap["agent_exit"]
	if ok {
		fn := a.getToolFunc("agent_exit")
		adapter := tools.NewAdapter("agent_exit", finishDef.Description, fn).WithSchema(finishDef.Parameters)
		customAdapters = append(customAdapters, adapter)
	}

	tools.SetGuardOnAdapters(customAdapters, a.guard)

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
			ctx = a.routeCtx(ctx, agentRef.Name, task)
			resultStr, err := a.executeCustomAgent(ctx, agentRef, adaptersRef, packedTask)
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

// currentLLMTimeout 返回当前生效的 LLM 调用超时：全局运行时值（TUI :timeout
// 可动态修改）优先，未初始化时回落构造期快照 a.llmTimeout。
// 每次构造 ExecutorConfig / PlannerConfig 时求值（而非使用构造期快照），
// 确保 TUI 调整后新发起的 LLM 调用立即使用新值。
func (a *DirectorAgent) currentLLMTimeout() time.Duration {
	if t := config.GlobalLLMTimeout(); t > 0 {
		return t
	}
	if a.llmTimeout > 0 {
		return a.llmTimeout
	}
	return config.DefaultLLMCallTimeout
}

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
	cfg.LLMTimeout = a.currentLLMTimeout()

	if rolloutWriter := a.createRolloutWriter(ca.DisplayName, task); rolloutWriter != nil {
		defer rolloutWriter.Close()
		ctx = memory.WithRolloutWriter(ctx, rolloutWriter)
	}

	result, err := RunAgentLoop(ctx, cfg)
	if err != nil {
		return "", err
	}
	// 统一走委派结果分级构造基础设施：Text=完整子代理输出（delegate 闭包与
	// note 程序化消费），Summary=分级摘要（预算内=全文；超预算时落盘 artifact
	// 并生成带 ID 的摘要）。此前手工构造漏填 Summary，导致 FormatForDirector
	// 注入上下文时子代理输出丢失（回归测试 TestCustomAgentDelegateTool_* 钉住）。
	agentResult := FinalizeResultFull(ca.DisplayName, task, result)
	a.setPendingSubAgentMemory(&agentResult)
	return result.Text, nil
}

func (a *DirectorAgent) injectSubAgentMemory(result AgentResult, toolCallID string, toolName string) {
	a.injectSubAgentMemoryMu.Lock()
	defer a.injectSubAgentMemoryMu.Unlock()

	if a.currentMemory == nil {
		return
	}

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

}

func (a *DirectorAgent) SetTaskID(taskID string) {
	a.taskID = taskID
	a.boardID = blackboard.SanitizeBoardID(taskID)
}

func (a *DirectorAgent) SetResumedTask(resumed bool) {
	a.resumedTask = resumed
}

func (a *DirectorAgent) createRolloutWriter(agentName, task string) *memory.RolloutWriter {
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

func (a *DirectorAgent) computeProjectID() string {
	return director.ComputeProjectID(a.env.ProjectPath())
}

func (a *DirectorAgent) setPendingSubAgentMemory(r *AgentResult) {
	a.pendingSubAgentMu.Lock()
	defer a.pendingSubAgentMu.Unlock()
	a.pendingSubAgentMemory = r
}

func (a *DirectorAgent) takePendingSubAgentMemory() *AgentResult {
	a.pendingSubAgentMu.Lock()
	defer a.pendingSubAgentMu.Unlock()
	r := a.pendingSubAgentMemory
	a.pendingSubAgentMemory = nil
	return r
}

func (a *DirectorAgent) applyEnhancedCommander(
	agentType string,
	task string,
	result AgentResult,
	err error,
	noteRef string,
	unresolvedCount int,
) (string, error) {
	a.setPendingSubAgentMemory(&result)

	if err != nil {
		return "", err
	}

	return FormatForDirector(agentType, result, noteRef, unresolvedCount), nil
}

func blackboardEnabled() bool {
	return os.Getenv("YAGENT_BLACKBOARD") != "0"
}

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

var _ Agent = (*DirectorAgent)(nil)

var _ = director.NewPlanner

func (a *DirectorAgent) Run(ctx context.Context, input string) (AgentResult, error) {
	mem, _ := memory.GetConversationMemory(ctx)

	text, err := a.run(ctx, input, mem)

	var memHistory []memory.ChatMessage
	if mem != nil {
		memHistory = mem.GetMessages()
	}
	return AgentResult{Text: text, Memory: memHistory}, err
}

func toSubAgentMemory(result *AgentResult) *memory.SubAgentMemory {
	if result == nil {
		return nil
	}
	return &memory.SubAgentMemory{Text: result.Text, Memory: result.Memory}
}

type directorToolRunner struct {
	agent *DirectorAgent
	state *director.RunState
}

func (r *directorToolRunner) Specs() []llm.ToolDef {
	defs := make([]llm.ToolDef, len(r.agent.Adapters))
	for i, ad := range r.agent.Adapters {
		defs[i] = ad.ToToolDef()
	}
	return defs
}

func (r *directorToolRunner) Call(ctx context.Context, name string, argsJSON string) (string, error) {
	a := r.agent

	for _, t := range a.Adapters {
		if t.Name() != name {
			continue
		}

		if strings.HasPrefix(t.Name(), "delegate_") {
			agentName := strings.TrimPrefix(t.Name(), "delegate_")
			LogDelegateCall(t.Name(), agentName, argsJSON)
		}

		toolTimeout := 120 * time.Second
		isDelegate := strings.HasPrefix(name, "delegate_")
		cancelCtx, cancelCtxCancel := context.WithCancel(ctx)
		toolCtx := cancelCtx
		toolCancel := cancelCtxCancel
		switch {
		case isInteractiveUserTool(name):
		case isDelegate:
			toolCtx, toolCancel = activity.WithIdleTimeout(cancelCtx, activity.Options{
				IdleTimeout:  a.delegateIdleTimeout,
				TotalTimeout: a.delegateTotalTimeout,
			})
		default:
			toolCtx, toolCancel = context.WithTimeout(cancelCtx, toolTimeout)
		}
		toolResult, err := t.Call(toolCtx, argsJSON)
		cancelCtxCancel()
		toolCancel()

		if psm := a.takePendingSubAgentMemory(); psm != nil {
			a.injectSubAgentMemory(*psm, "", name)
			r.state.SetPendingSubAgentMemory(toSubAgentMemory(psm))
		}

		if strings.HasPrefix(t.Name(), "delegate_") {
			r.state.RecordDelegation(err == nil)
		}

		if err != nil {
			cause := context.Cause(toolCtx)
			var idleErr *activity.IdleTimeoutError
			var totalErr *activity.TotalTimeoutError
			switch {
			case errors.As(cause, &idleErr):
				return "", fmt.Errorf("tool execution timed out (idle): %w", idleErr)
			case errors.As(cause, &totalErr):
				return "", fmt.Errorf("tool execution timed out (total): %w", totalErr)
			case errors.Is(err, context.DeadlineExceeded):
				return "", fmt.Errorf("tool execution timed out after %d seconds", int(toolTimeout.Seconds()))
			}
			return "", err
		} else if t.Name() == "delegate_repo" {
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
	return fmt.Sprintf("Tool %s not found", name), nil
}

func (a *DirectorAgent) run(ctx context.Context, input string, mem *memory.ConversationMemory) (string, error) {
	resumedTask := a.resumedTask
	a.resumedTask = false

	a.currentMemory = mem
	defer func() { a.currentMemory = nil }()

	if a.llmClient != nil {
		newEngine := a.llmClient.GetAgentEngine("director")
		if newEngine != nil {
			a.LLM = newEngine
		}
		a.refreshSubAgentEngines()
	}

	state := &director.RunState{
		MaxSteps:              a.maxSteps,
		HasDelegated:          false,
		NonDelegationPrompts:  0,
		DelegationAttempts:    0,
		PendingSubAgentMemory: nil,
	}

	prompts := func(ctx context.Context, in director.PromptInput) (string, error) {
		systemPrompt := a.promptFmt.FormatPrompt(directorPrompt)
		var projectContext string
		if in.FirstConversation || in.ForceProjectContext {
			if loadResult := a.loadProjectContext(); loadResult != nil && loadResult.Content != "" {
				if a.Publisher != nil {
					a.Publisher.Publish("context_loaded", loadResult, a.Name())
				}
				projectContext = fmt.Sprintf("\n\n### Project Workspace Context\n%s\n", loadResult.Content)
			}
		}

		if customAgents := a.metaHandler.List(); len(customAgents) > 0 {
			systemPrompt += "\n\n### Custom Agents\nThe following specialized agents have been designed by Meta-Agent and are permanently available for delegation:\n\n"
			for _, ca := range customAgents {
				systemPrompt += fmt.Sprintf("- **%s** (`delegate_%s`): %s\n", ca.DisplayName, ca.Name, ca.Description)
			}
			systemPrompt += "\nUse these agents via their delegate tools for tasks matching their specializations.\n"
		}

		if projectContext != "" {
			systemPrompt += projectContext
		}

		if a.knowledge != nil {
			injCtx := knowledge.InjectionContext{
				UserMessage: in.Input,
				TargetFiles: nil,
				AgentName:   a.Name(),
			}
			if knowledgeBlock, err := a.knowledge.Inject(ctx, injCtx); err == nil && knowledgeBlock != "" {
				systemPrompt += knowledgeBlock
			}
		}
		return systemPrompt, nil
	}

	var directorRolloutWriter *memory.RolloutWriter
	if rw := a.createRolloutWriter("director", input); rw != nil {
		directorRolloutWriter = rw
		defer func() {
			if directorRolloutWriter != nil {
				directorRolloutWriter.Close()
			}
		}()
	}

	toolRunner := &directorToolRunner{agent: a, state: state}

	var recovery *director.RecoveryHandler
	var metrics *director.MetricsCollector
	if a.adapter != nil {
		recovery = a.adapter.recovery
		metrics = a.adapter.metrics
	}

	cfg := director.PlannerConfig{
		LLM:                       a.LLM,
		Publisher:                 a.Publisher,
		Journal:                   a.thinkLink,
		Rollout:                   directorRolloutWriter,
		Tools:                     toolRunner,
		Prompts:                   prompts,
		Compressor:                a.compressor,
		MaxSteps:                  a.maxSteps,
		LLMTimeout:                a.currentLLMTimeout(),
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
		ToolTimeout:               120 * time.Second,

		MaxParallelReadOnlyTools: a.maxParallelReadOnlyTools,
		IsParallelizableTool:     a.isToolParallelizable,
	}

	planner := director.NewPlanner(cfg, state)
	result, err := planner.Run(ctx, director.PlanInput{
		Input:   input,
		Mem:     mem,
		TaskID:  a.taskID,
		Resumed: resumedTask,
	})
	return result.Text, err
}

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

func (a *DirectorAgent) applyEmergencyCompression(ctx context.Context, messages []llm.Message, threshold int) ([]llm.Message, *compression.EmergencyCompressionStats) {
	return a.compressor.ApplyEmergency(ctx, messages, threshold, a.currentMemory)
}

type UltimateCompressionStats = compression.UltimateCompressionStats

func (a *DirectorAgent) applyUltimateCompression(messages []llm.Message, threshold int) ([]llm.Message, *UltimateCompressionStats) {
	return a.compressor.ApplyUltimate(messages, threshold, a.currentMemory, a.EnhancedCommanderCfg.UltimateCompressionKeepPlans)
}

func validateAndRepairToolCallPairs(messages []llm.Message) []llm.Message {
	result := make([]llm.Message, 0, len(messages))

	for i := 0; i < len(messages); i++ {
		msg := messages[i]

		if msg.Role == llm.RoleAssistant && len(msg.ToolCalls) > 0 {
			expectedIDs := make(map[string]bool, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				expectedIDs[tc.ID] = true
			}

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
					break
				} else {
					break
				}
			}

			allResponsesPresent := len(matchedResponses) == len(msg.ToolCalls)

			if allResponsesPresent {
				result = append(result, msg)
				for _, tc := range msg.ToolCalls {
					if resp, ok := matchedResponses[tc.ID]; ok {
						result = append(result, resp)
					}
				}
			} else {
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
				if msg.Content != "" {
					preserved := msg
					preserved.ToolCalls = nil
					result = append(result, preserved)
				}
			}

			i = j - 1
			continue
		}

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
				slog.Warn("Removing orphan tool message (no matching assistant)",
					"tool_call_id", msg.ToolCallID,
				)
				continue
			}
		}

		result = append(result, msg)
	}

	result = llm.MergeConsecutiveAssistants(result)

	return result
}
