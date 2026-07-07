package app

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"
	"time"

	"codeactor/internal/agents"
	"codeactor/internal/browser"
	"codeactor/internal/compact"
	"codeactor/internal/config"
	"codeactor/internal/globalctx"
	"codeactor/internal/llm"
	"codeactor/internal/logging"
	"codeactor/internal/memory"
	"codeactor/internal/skills"
	"codeactor/internal/tools"
	"codeactor/internal/messaging"
)

// CodeActor is the main entry point for the agent system.
type CodeActor struct {
	engine               llm.Engine  // default engine (backward-compatible)
	client               *llm.Client // LLM client for per-agent/tool engine resolution
	config               *config.Config
	director *agents.DirectorAgent
	dispatcher           *messaging.MessageDispatcher
	mu                   sync.Mutex
	userResponseChannels map[string]chan string
	logger               *slog.Logger

	globalCtx      *globalctx.GlobalCtx
	DisabledAgents string // comma-separated list of agent names to disable (e.g. "repo,coding,chat")
	CodexrayPort   int    // codebase 服务端口，由 main 函数动态分配

	SkillRegistry      *skills.SkillRegistry // 技能注册表，加载 .codeactor/skills/ 下的 .md 文件

	// [NEW] 记忆系统
	sharedMemory         *memory.SharedMemory
	consolidationWorker  *agents.ConsolidationWorker
}

// NewCodeActor creates a new CodeActor.
func NewCodeActor(client *llm.Client) (*CodeActor, error) {
	ca := &CodeActor{
		userResponseChannels: make(map[string]chan string),
		logger:               slog.Default().With("component", "coding_assistant"),
		engine:               client.Engine,
		client:               client,
		config:               client.Config,
	}
	return ca, nil
}

// Init initializes the assistant with Engine and creates agents.
// Uses per-agent and per-tool engine resolution from the LLM client.
func (ca *CodeActor) Init(engine llm.Engine, workDir string) {
	ca.engine = engine

	// Initialize agents
	publisher := messaging.NewMessagePublisher(ca.dispatcher)

	userConfirmMgr := tools.NewUserConfirmManager()

	// Resolve tool-specific engine for micro_agent
	microAgentEngine := engine
	if ca.client != nil {
		microAgentEngine = ca.client.GetToolEngine("micro_agent")
	}

	// Resolve tool-specific engine for deepthinking
	deepthinkingEngine := engine
	if ca.client != nil {
		deepthinkingEngine = ca.client.GetToolEngine("deepthinking")
	}

	gctx := globalctx.GlobalCtx{
		SpeakLang:   ca.config.Agent.SpeakLang,
		ProjectPath: workDir,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		// Global utility
		Publisher:   publisher,
		CodexrayURL: fmt.Sprintf("http://127.0.0.1:%d", ca.CodexrayPort),

		// Tools
		FileOps:          tools.NewFileOperationsTool(workDir),
		SearchOps:        tools.NewSearchOperationsTool(workDir),
		SysOps:           tools.NewSystemOperationsTool(workDir),
		ReplaceTool:      tools.NewReplaceBlockTool(workDir),
		ThinkingTool:     tools.NewThinkingTool(),
		MicroAgentTool:   tools.NewMicroAgentTool(microAgentEngine),
		FlowOps:          tools.NewFlowControlTool(workDir),
		RepoOps:          tools.NewRepoOperationsTool(fmt.Sprintf("http://127.0.0.1:%d", ca.CodexrayPort), workDir),
		UserConfirmMgr:   userConfirmMgr,
		DeepThinkingTool: tools.NewDeepThinkingTool(deepthinkingEngine),

		// Git Checkpoint config
		GitCheckpointCfg: &ca.config.GitCheckpoint,
	}
	ca.globalCtx = &gctx

	// Wire up UserConfirmManager: register as consumer and set publisher
	userConfirmMgr.SetPublisher(publisher)
	gctx.FlowOps.UserConfirmMgr = userConfirmMgr

	// Create workspace guard for authorizing dangerous operations
	guard := tools.NewWorkspaceGuard(workDir, userConfirmMgr)
	gctx.Guard = guard
	if ca.dispatcher != nil {
		ca.dispatcher.RegisterConsumer(userConfirmMgr)
	}
	// Get max steps from config, default to DefaultMaxSteps if not set
	defaultSteps := config.DefaultMaxSteps
	repoMaxSteps := defaultSteps.Repo
	codingMaxSteps := defaultSteps.Coding
	chatMaxSteps := defaultSteps.Chat
	devopsMaxSteps := defaultSteps.DevOps
	browserMaxSteps := defaultSteps.Browser
	directorMaxSteps := defaultSteps.Director

	if ca.config != nil {
		if ca.config.Agent.RepoMaxSteps > 0 {
			repoMaxSteps = ca.config.Agent.RepoMaxSteps
		}
		if ca.config.Agent.CodingMaxSteps > 0 {
			codingMaxSteps = ca.config.Agent.CodingMaxSteps
		}
		if ca.config.Agent.ChatMaxSteps > 0 {
			chatMaxSteps = ca.config.Agent.ChatMaxSteps
		}
		if ca.config.Agent.DevOpsMaxSteps > 0 {
			devopsMaxSteps = ca.config.Agent.DevOpsMaxSteps
		}
		if ca.config.Agent.BrowserMaxSteps > 0 {
			browserMaxSteps = ca.config.Agent.BrowserMaxSteps
		}
		if ca.config.Agent.DirectorMaxSteps > 0 {
			directorMaxSteps = ca.config.Agent.DirectorMaxSteps
		}
	}
	metaRetryCount := defaultSteps.MetaRetry // default
	if ca.config != nil && ca.config.Agent.MetaRetryCount > 0 {
		metaRetryCount = ca.config.Agent.MetaRetryCount
	}

	// Parse disabled agents from comma-separated string
	disabledAgents := parseDisabledAgents(ca.DisabledAgents)

	// 检查配置文件中的 enable_browser_agent 设置
	// 如果配置明确禁用了 browser agent，则加入禁用列表
	if ca.config != nil && !ca.config.Browser.EnableBrowserAgent {
		disabledAgents["browser"] = true
	}

	// Resolve per-agent engines
	directorEngine := engine
	repoEngine := engine
	codingEngine := engine
	chatEngine := engine
	metaEngine := engine
	devopsEngine := engine
	browserEngine := engine
	if ca.client != nil {
		directorEngine = ca.client.GetAgentEngine("director")
		repoEngine = ca.client.GetAgentEngine("repo")
		codingEngine = ca.client.GetAgentEngine("coding")
		chatEngine = ca.client.GetAgentEngine("chat")
		metaEngine = ca.client.GetAgentEngine("meta")
		devopsEngine = ca.client.GetAgentEngine("devops")
		browserEngine = ca.client.GetAgentEngine("browser")
	}

	// 构建 compact config（需要在创建 RepoAgent 和 CodingAgent 之前）
	var compactCfg *compact.Config
	var summaryEngine llm.Engine
	if ca.config != nil {
		c := &ca.config.Compact
		compactCfg = &compact.Config{
			MaxContextTokens:            c.MaxContextTokens,
			EnableAutoCompact:           c.EnableAutoCompact,
			KeepRecentRounds:            c.KeepRecentRounds,
			SummarizationTimeout:        time.Duration(c.SummarizationTimeout) * time.Second,
			SummarizationMaxInputTokens: c.SummarizationMaxInputTokens,
			CompactLogDir:               logging.GetLogDir(),
		}

		// 为 compact 摘要创建独立的 LLM 引擎（如果配置了 summarization_provider）
		if c.SummarizationProvider != "" {
			provider, err := ca.config.GetProvider(c.SummarizationProvider)
			if err == nil {
				summaryEngine = llm.NewOpenAIEngine(provider.APIBaseURL, provider.APIKey, provider.Model, ca.config.LLM, provider.ReasoningEffort)
				summaryEngine = llm.NewLoggingEngine(summaryEngine)
			}
		}
	}

	repoAgent := agents.NewRepoAgent(ca.globalCtx, repoEngine, publisher, repoMaxSteps, compactCfg)

	// [NEW] 初始化 RepoAgent 记忆系统
	{
		ca.sharedMemory = memory.NewSharedMemory(100)
		repoID := ca.globalCtx.ProjectPath
		repoMemStore := agents.NewRepoMemoryStore(repoID, ca.sharedMemory)
		if err := repoMemStore.Load(context.Background()); err != nil {
			slog.Warn("RepoAgent memory preload failed, continuing without memory",
				"error", err,
			)
		}
		// 使用 consolidation 专用的 LLM engine（复用 repoEngine，轻量模型可在配置中独立设置）
		consolidationWorker := agents.NewConsolidationWorker(repoMemStore, repoEngine)
		consolidationWorker.Start()
		ca.consolidationWorker = consolidationWorker
		repoAgent.SetMemory(repoMemStore, consolidationWorker)
		slog.Info("RepoAgent memory system initialized",
			"repo_id", repoID,
			"has_memory", !repoMemStore.IsEmpty(),
		)
	}

	chatAgent := agents.NewChatAgent(ca.globalCtx, chatEngine, chatMaxSteps)
	stepRetries := 0
	if ca.config != nil {
		stepRetries = ca.config.LLM.StepRetries
	}
	metaAgent := agents.NewMetaAgent(ca.globalCtx, metaEngine, stepRetries)
	devopsAgent := agents.NewDevOpsAgent(ca.globalCtx, devopsEngine, devopsMaxSteps)
	// 合并浏览器配置：从 config 读取，未设置的使用默认值
	browserCfg := browser.DefaultBrowserConfig()
	if ca.config != nil {
		cfg := ca.config.Browser
		if cfg.ViewportWidth > 0 {
			browserCfg.ViewportWidth = cfg.ViewportWidth
		}
		if cfg.ViewportHeight > 0 {
			browserCfg.ViewportHeight = cfg.ViewportHeight
		}
		if cfg.TimeoutSeconds > 0 {
			browserCfg.TimeoutSeconds = cfg.TimeoutSeconds
		}
		if cfg.TaskTimeoutSeconds > 0 {
			browserCfg.TaskTimeoutSeconds = cfg.TaskTimeoutSeconds
		}
		if cfg.MaxConcurrentPages > 0 {
			browserCfg.MaxConcurrentPages = cfg.MaxConcurrentPages
		}
		if cfg.IdleTimeout != "" {
			browserCfg.IdleTimeout = cfg.IdleTimeout
		}
		if cfg.BrowserPath != "" {
			browserCfg.BrowserPath = cfg.BrowserPath
		}
		if cfg.UserDataDir != "" {
			browserCfg.UserDataDir = cfg.UserDataDir
		}
		browserCfg.Headless = cfg.Headless
		browserCfg.AutoLaunch = cfg.AutoLaunch
		browserCfg.AllowNoSandbox = cfg.AllowNoSandbox
		browserCfg.AllowedDomains = cfg.AllowedDomains
		browserCfg.BlockedDomains = cfg.BlockedDomains
		browserCfg.ExtraArgs = cfg.ExtraArgs
	}
	browserMgr := browser.NewManager(browserCfg, browserCfg.AllowedDomains, browserCfg.BlockedDomains)
	ca.globalCtx.BrowserMgr = browserMgr
	browserAgent := agents.NewBrowserAgent(ca.globalCtx, browserMgr, browserEngine, browserMaxSteps)

	codingAgent := agents.NewCodingAgent(ca.globalCtx, codingEngine, codingMaxSteps, browserAgent, compactCfg)

	// [NEW] Initialize Shared Memory System (4 dimensions: user, feedback, project, reference)
	{
		sharedDimStore := memory.NewSharedDimensionStore(ca.sharedMemory)
		sharedDimInjector := memory.NewSharedMemoryInjector(sharedDimStore)
		sharedDimUpdater := memory.NewSharedDimensionUpdater(sharedDimStore, memory.DefaultRestraintPolicy())
		projectPath := ca.globalCtx.ProjectPath

		// Set injector and updater on all agents via BaseAgent
		repoAgent.BaseAgent.MemoryInjector = sharedDimInjector
		repoAgent.BaseAgent.MemoryUpdater = sharedDimUpdater
		chatAgent.BaseAgent.MemoryInjector = sharedDimInjector
		chatAgent.BaseAgent.MemoryUpdater = sharedDimUpdater
		metaAgent.BaseAgent.MemoryInjector = sharedDimInjector
		metaAgent.BaseAgent.MemoryUpdater = sharedDimUpdater
		devopsAgent.BaseAgent.MemoryInjector = sharedDimInjector
		devopsAgent.BaseAgent.MemoryUpdater = sharedDimUpdater
		browserAgent.BaseAgent.MemoryInjector = sharedDimInjector
		browserAgent.BaseAgent.MemoryUpdater = sharedDimUpdater
		codingAgent.BaseAgent.MemoryInjector = sharedDimInjector
		codingAgent.BaseAgent.MemoryUpdater = sharedDimUpdater

		// Create update_shared_memory tool function
		updateMemFn := func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			dimStr, _ := params["dimension"].(string)
			actionStr, _ := params["action"].(string)
			reason, _ := params["reason"].(string)
			payload := params["payload"]

			if dimStr == "" || actionStr == "" || reason == "" || payload == nil {
				return "Missing required parameters: dimension, action, payload, and reason.", nil
			}

			proposal := &memory.MemoryUpdateProposal{
				Dimension:  memory.Dimension(dimStr),
				Action:     memory.UpdateAction(actionStr),
				Payload:    payload,
				Reason:     reason,
				ProposedBy: "agent",
				Metadata: map[string]interface{}{
					"user_id":    "default",
					"project_id": projectPath,
				},
			}

			result := sharedDimUpdater.ApplyUpdate(proposal)
			return result.Reason, nil
		}

		// Build adapter with schema
		updateMemAdapter := tools.NewAdapter("update_shared_memory",
			"Update the shared cross-agent memory system. Use this to persist important user information (user dimension), feedback (feedback dimension), project context (project dimension), or reference resources (reference dimension) across all agents and conversations.",
			updateMemFn,
		).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"dimension": map[string]interface{}{
					"type": "string",
					"enum": []string{"user", "feedback", "project", "reference"},
					"description": "Which memory dimension to update",
				},
				"action": map[string]interface{}{
					"type": "string",
					"enum": []string{"add", "update", "remove"},
					"description": "Type of update operation",
				},
				"payload": map[string]interface{}{
					"type":        "object",
					"description": "Dimension-specific data. For user: {profile, expertise, preferences}. For feedback: {correction, endorsement}. For project: {objective, member, deadline, status}. For reference: {resource}.",
				},
				"reason": map[string]interface{}{
					"type":        "string",
					"description": "Why this update matters (min 10 chars, be specific)",
				},
			},
			"required": []string{"dimension", "action", "payload", "reason"},
		})

		// Register tool on all agents that use Adapters
		codingAgent.Adapters = append(codingAgent.Adapters, updateMemAdapter)
		chatAgent.Adapters = append(chatAgent.Adapters, updateMemAdapter)
		devopsAgent.Adapters = append(devopsAgent.Adapters, updateMemAdapter)
		repoAgent.Adapters = append(repoAgent.Adapters, updateMemAdapter)
		browserAgent.Adapters = append(browserAgent.Adapters, updateMemAdapter)

		slog.Info("Shared memory system initialized",
			"dimensions", []string{"user", "feedback", "project", "reference"},
		)
	}

	ca.director = agents.NewDirectorAgent(ca.globalCtx, directorEngine, repoAgent, codingAgent, chatAgent, metaAgent, devopsAgent, browserAgent, directorMaxSteps, disabledAgents, metaRetryCount, compactCfg, summaryEngine, *ca.config, ca.client)
}

func (ca *CodeActor) IntegrateMessaging(dispatcher *messaging.MessageDispatcher) {
	ca.dispatcher = dispatcher
}

// TaskRequest encapsulates the request context.
type TaskRequest struct {
	ctx         context.Context
	taskID      string
	projectDir  string
	taskDesc    string
	memory      *memory.ConversationMemory
	wsCallback  func(string, string)
	publisher   *messaging.MessagePublisher
	userMessage string
}

func NewTaskRequest(ctx context.Context, taskID string) *TaskRequest {
	return &TaskRequest{
		ctx:    ctx,
		taskID: taskID,
	}
}

func (r *TaskRequest) WithProjectDir(dir string) *TaskRequest {
	r.projectDir = dir
	return r
}

func (r *TaskRequest) WithTaskDesc(desc string) *TaskRequest {
	r.taskDesc = desc
	return r
}

func (r *TaskRequest) WithMemory(mem *memory.ConversationMemory) *TaskRequest {
	r.memory = mem
	return r
}

func (r *TaskRequest) WithWSCallback(cb func(string, string)) *TaskRequest {
	r.wsCallback = cb
	return r
}

func (r *TaskRequest) WithMessagePublisher(p *messaging.MessagePublisher) *TaskRequest {
	r.publisher = p
	return r
}

func (r *TaskRequest) WithUserMessage(msg string) *TaskRequest {
	r.userMessage = msg
	return r
}

// ProcessCodingTaskWithCallback executes the task using the agent system.
func (ca *CodeActor) ProcessCodingTaskWithCallback(req *TaskRequest) (string, error) {
	ca.Init(ca.engine, req.projectDir)

	return ca.director.Run(req.ctx, req.taskDesc, req.memory)
}

// ProcessConversation handles chat messages.
func (ca *CodeActor) ProcessConversation(req *TaskRequest) (string, error) {
	ca.Init(ca.engine, req.projectDir)

	return ca.director.Run(req.ctx, req.userMessage, req.memory)
}

// SwitchProvider dynamically switches the LLM provider for all agents.
// The change takes effect on the next task execution (since Init() is called
// at the start of ProcessCodingTaskWithCallback / ProcessConversation).
func (ca *CodeActor) SwitchProvider(providerName string) error {
	if ca.client == nil {
		return fmt.Errorf("LLM client not available")
	}
	if err := ca.client.SwitchProvider(providerName); err != nil {
		return err
	}
	// Also update the local engine reference to keep it in sync
	ca.engine = ca.client.Engine
	return nil
}

// GetClient returns the underlying LLM client.
func (ca *CodeActor) GetClient() *llm.Client {
	return ca.client
}

// SetAgentProvider 为指定 agent 设置运行时 provider 覆盖（通过 LLM Client）
func (ca *CodeActor) SetAgentProvider(agentName, providerName string) error {
	if ca.client == nil {
		return fmt.Errorf("LLM client not available")
	}
	return ca.client.SetAgentProvider(agentName, providerName)
}

// GetAgentProvider 返回指定 agent 当前生效的 provider 名称和模型名
func (ca *CodeActor) GetAgentProvider(agentName string) (string, string) {
	if ca.client == nil {
		return "", ""
	}
	return ca.client.GetAgentProvider(agentName)
}

// GetAllAgentOverrides 返回所有运行时 agent provider 覆盖的副本
func (ca *CodeActor) GetAllAgentOverrides() map[string]string {
	if ca.client == nil {
		return nil
	}
	return ca.client.GetAllAgentOverrides()
}

// parseDisabledAgents converts a comma-separated string of agent names
// into a map[string]bool for O(1) lookup. Valid agent names: repo, coding, chat, meta, devops, browser.
func parseDisabledAgents(s string) map[string]bool {
	result := make(map[string]bool)
	if s == "" {
		return result
	}
	for _, name := range strings.Split(s, ",") {
		name = strings.TrimSpace(name)
		if name != "" {
			result[name] = true
		}
	}
	return result
}

// Close 清理资源
func (ca *CodeActor) Close() {
	// 停止 consolidation worker
	if ca.consolidationWorker != nil {
		slog.Info("Stopping consolidation worker...")
		ca.consolidationWorker.Stop()
	}
	if ca.globalCtx != nil && ca.globalCtx.BrowserMgr != nil {
		slog.Info("Closing browser manager...")
		if err := ca.globalCtx.BrowserMgr.Close(); err != nil {
			slog.Warn("Failed to close browser manager", "error", err)
		}
	}
}
