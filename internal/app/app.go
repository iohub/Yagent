package app

import (
	"context"
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"sync"

	"codeactor/internal/agents"
	"codeactor/internal/browser"
	"codeactor/internal/compact"
	"codeactor/internal/config"
	"codeactor/internal/globalctx"
	"codeactor/internal/llm"
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
	conductor            *agents.ConductorAgent
	dispatcher           *messaging.MessageDispatcher
	mu                   sync.Mutex
	userResponseChannels map[string]chan string
	logger               *slog.Logger

	globalCtx      *globalctx.GlobalCtx
	DisabledAgents string // comma-separated list of agent names to disable (e.g. "repo,coding,chat")
	CodebasePort   int    // codebase 服务端口，由 main 函数动态分配

	SkillRegistry *skills.SkillRegistry // 技能注册表，加载 .codeactor/skills/ 下的 .md 文件
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
		CodebaseURL: fmt.Sprintf("http://127.0.0.1:%d", ca.CodebasePort),

		// Tools
		FileOps:          tools.NewFileOperationsTool(workDir),
		SearchOps:        tools.NewSearchOperationsTool(workDir),
		SysOps:           tools.NewSystemOperationsTool(workDir),
		ReplaceTool:      tools.NewReplaceBlockTool(workDir),
		ThinkingTool:     tools.NewThinkingTool(),
		MicroAgentTool:   tools.NewMicroAgentTool(microAgentEngine),
		FlowOps:          tools.NewFlowControlTool(workDir),
		RepoOps:          tools.NewRepoOperationsTool(fmt.Sprintf("http://127.0.0.1:%d", ca.CodebasePort), workDir),
		UserConfirmMgr:   userConfirmMgr,
		DeepThinkingTool: tools.NewDeepThinkingTool(deepthinkingEngine),
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
	// Get max steps from config, default to 10 if not set
	repoMaxSteps := 20
	codingMaxSteps := 30
	chatMaxSteps := 10
	devopsMaxSteps := 15
	browserMaxSteps := 15
	conductorMaxSteps := 20

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
		if ca.config.Agent.ConductorMaxSteps > 0 {
			conductorMaxSteps = ca.config.Agent.ConductorMaxSteps
		}
	}
	metaRetryCount := 5 // default
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
	conductorEngine := engine
	repoEngine := engine
	codingEngine := engine
	chatEngine := engine
	metaEngine := engine
	devopsEngine := engine
	browserEngine := engine
	if ca.client != nil {
		conductorEngine = ca.client.GetAgentEngine("conductor")
		repoEngine = ca.client.GetAgentEngine("repo")
		codingEngine = ca.client.GetAgentEngine("coding")
		chatEngine = ca.client.GetAgentEngine("chat")
		metaEngine = ca.client.GetAgentEngine("meta")
		devopsEngine = ca.client.GetAgentEngine("devops")
		browserEngine = ca.client.GetAgentEngine("browser")
	}

	repoAgent := agents.NewRepoAgent(ca.globalCtx, repoEngine, publisher, repoMaxSteps)

	chatAgent := agents.NewChatAgent(ca.globalCtx, chatEngine, chatMaxSteps)
	metaAgent := agents.NewMetaAgent(ca.globalCtx, metaEngine)
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
	codingAgent := agents.NewCodingAgent(ca.globalCtx, codingEngine, codingMaxSteps, browserAgent)
	// 构建 compact config
	var compactCfg *compact.Config
	var summaryEngine llm.Engine
	if ca.config != nil {
		c := &ca.config.Compact
		compactCfg = compact.ConfigFrom(
			c.MaxContextTokens,
			c.Strategy,
			c.EnableAutoCompact,
			c.SummarizationModel,
			c.SummarizationProvider,
			c.L1Threshold,
			c.L2Threshold,
			c.L3Threshold,
			c.SummarizationTimeout,
			c.KeepRecentRounds,
			c.KeepTaskConclusions,
			c.SummarizationMaxInputTokens,
		)

		// 为 compact 摘要创建独立的 LLM 引擎（如果配置了 summarization_provider）
		if c.SummarizationProvider != "" {
			provider, err := ca.config.GetProvider(c.SummarizationProvider)
			if err == nil {
				summaryEngine = llm.NewOpenAIEngine(provider.APIBaseURL, provider.APIKey, provider.Model)
				summaryEngine = llm.NewLoggingEngine(summaryEngine)
			}
		}
	}

	ca.conductor = agents.NewConductorAgent(ca.globalCtx, conductorEngine, repoAgent, codingAgent, chatAgent, metaAgent, devopsAgent, browserAgent, conductorMaxSteps, disabledAgents, metaRetryCount, compactCfg, summaryEngine)
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

	return ca.conductor.Run(req.ctx, req.taskDesc, req.memory)
}

// ProcessConversation handles chat messages.
func (ca *CodeActor) ProcessConversation(req *TaskRequest) (string, error) {
	ca.Init(ca.engine, req.projectDir)

	return ca.conductor.Run(req.ctx, req.userMessage, req.memory)
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
	if ca.globalCtx != nil && ca.globalCtx.BrowserMgr != nil {
		slog.Info("Closing browser manager...")
		if err := ca.globalCtx.BrowserMgr.Close(); err != nil {
			slog.Warn("Failed to close browser manager", "error", err)
		}
	}
}
