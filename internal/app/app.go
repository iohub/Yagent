package app

import (
	"context"
	"embed"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"yagent/internal/agents"
	"yagent/internal/blackboard"
	"yagent/internal/blueprint"
	"yagent/internal/browserctl"
	"yagent/internal/config"
	"yagent/internal/embedbin"
	"yagent/internal/globalctx"
	"yagent/internal/llm"
	"yagent/internal/mcp"
	"yagent/internal/memory"
	"yagent/internal/messaging"
	"yagent/internal/skills"
	"yagent/internal/supervise"
	"yagent/internal/tools"
	"yagent/internal/toolspec"
)

type Yagent struct {
	engine   llm.Engine
	engineMu sync.RWMutex

	client               *llm.Client
	config               *config.Config
	director             *agents.DirectorAgent
	dispatcher           *messaging.MessageDispatcher
	mu                   sync.Mutex
	userResponseChannels map[string]chan string
	logger               *slog.Logger

	globalCtx      *globalctx.GlobalCtx
	DisabledAgents string
	YoloMode       bool
	FullYoloMode   bool
	ForceQuit      bool

	browserMgr *browserctl.Manager

	mcpMgr     *mcp.Manager
	mcpConfigs map[string]mcp.ServerConfig

	specMu          sync.RWMutex
	externalSpecs   []*toolspec.Spec
	externalAccount tools.ExternalAccount

	skills       *skills.Registry
	skillAccount skills.LoadAccount

	blueprint *blueprintRuntime

	embeddedBinFS embed.FS

	initOnce sync.Once
}

func NewYagent(client *llm.Client) (*Yagent, error) {
	ca := &Yagent{
		userResponseChannels: make(map[string]chan string),
		logger:               slog.Default().With("component", "coding_assistant"),
		engine:               client.DefaultEngine(),
		client:               client,
		config:               client.Config,
	}
	return ca, nil
}

func (ca *Yagent) setEngine(e llm.Engine) {
	ca.engineMu.Lock()
	ca.engine = e
	ca.engineMu.Unlock()
}

func (ca *Yagent) defaultEngine() llm.Engine {
	ca.engineMu.RLock()
	defer ca.engineMu.RUnlock()
	return ca.engine
}

func (ca *Yagent) SetEmbeddedBinaries(fs embed.FS) {
	ca.embeddedBinFS = fs
}

func (ca *Yagent) CloseIdleConnections() {
	if e := ca.defaultEngine(); e != nil {
		e.CloseIdleConnections()
	}
}

func checkRegistry(validate func() []string, logger *slog.Logger) {
	if problems := validate(); len(problems) > 0 {
		logger.Error("tool registry self-check failed", "count", len(problems), "problems", problems)
	}
}

func (ca *Yagent) Init(engine llm.Engine, workDir string) {
	ca.setEngine(engine)

	ca.initOnce.Do(func() {
		checkRegistry(toolspec.Validate, slog.Default())

		if blackboard.Enabled() {
			cfg := config.GlobalBlackboard()
			if _, _, err := blackboard.GC(blackboard.GCOptions{
				TTL:              cfg.TTL,
				MaxNotesPerBoard: cfg.MaxNotesPerBoard,
			}, nil); err != nil {
				slog.Warn("blackboard: gc failed", "error", err)
			}
		}

		if config.SkillsEnabled() {
			dirs := skillDirs(workDir)
			registry, account := skills.Load(dirs)
			cfg := config.GlobalSkills()
			registry.SetOptions(skills.Options{
				LineChars:     cfg.CatalogLineChars,
				BodyMaxTokens: cfg.BodyMaxTokens,
			})
			ca.skills = registry
			ca.skillAccount = account
			slog.Info("skills loaded", "line", account.String(), "dirs", strings.Join(dirs, ", "))
		}

		publisher := messaging.NewMessagePublisher(ca.dispatcher)

		userConfirmMgr := tools.NewUserConfirmManager()

		microAgentEngine := engine
		if ca.client != nil {
			microAgentEngine = ca.client.GetToolEngine("micro_agent")
		}

		deepthinkingEngine := engine
		if ca.client != nil {
			if r := ca.client.GetRouter(); r.Enabled() {
				if hi := r.EngineFor(llm.TierHigh); hi != nil {
					deepthinkingEngine = hi
				} else {
					deepthinkingEngine = ca.client.GetToolEngine("deepthinking")
				}
			} else {
				deepthinkingEngine = ca.client.GetToolEngine("deepthinking")
			}
		}

		designEngine := engine
		if ca.client != nil {
			designEngine = ca.client.GetToolEngine("design")
		}

		binDir := ""
		if ca.config != nil {
			if dir, err := embedbin.ExtractBinaries(ca.embeddedBinFS, "dist/bin"); err != nil {
				slog.Debug("Failed to extract embedded binaries; tools will fall back to PATH lookup", "error", err)
			} else {
				binDir = dir
			}
		}

		codeseekBinaryPath := ""

		if ca.config != nil && ca.config.CodeSeek.BinaryPath != "" {
			codeseekBinaryPath = ca.config.CodeSeek.BinaryPath
			slog.Info("Using configured CodeSeek binary path", "path", codeseekBinaryPath)
		} else if binDir != "" {
			candidatePath := filepath.Join(binDir, "codeseek")
			if _, statErr := os.Stat(candidatePath); statErr == nil {
				codeseekBinaryPath = candidatePath
				slog.Info("Auto-extracted embedded CodeSeek binary", "path", codeseekBinaryPath)
			} else {
				slog.Debug("Embedded codeseek binary not found in extracted files, CodeSeek MCP will not be available")
			}
		}

		var codeSeekMCP *mcp.MCPClient
		ca.mcpConfigs = ca.mcpServerConfigs(codeseekBinaryPath, workDir)
		if config.MCPEnabled() && len(ca.mcpConfigs) > 0 {
			mcpCfg := config.GlobalMCP()
			cfgs := make([]mcp.ServerConfig, 0, len(ca.mcpConfigs))
			for _, cfg := range ca.mcpConfigs {
				cfgs = append(cfgs, cfg)
			}
			sort.Slice(cfgs, func(i, j int) bool { return cfgs[i].Name < cfgs[j].Name })

			mgr := mcp.NewManager(cfgs, mcp.ManagerOptions{
				StartupTimeout:   mcpCfg.StartupTimeout,
				ReconnectBackoff: mcpCfg.ReconnectBackoff,
				MaxReconnects:    mcpCfg.MaxReconnects,
				StatusMaxLines:   mcpCfg.StatusMaxLines,
				OnToolsChanged:   ca.rebuildExternalSpecs,
				OnStatusChange:   ca.publishMCPStatus,
			})
			ca.mcpMgr = mgr
			if err := mgr.Start(context.Background()); err != nil {
				slog.Warn("Failed to start MCP manager", "error", err)
			}
			codeSeekMCP = mgr.Client("codeseek")
			ca.rebuildExternalSpecs()
			slog.Info("mcp starting", "servers", len(cfgs), "lines", mgr.StatusLines())
		}

		gctx := globalctx.GlobalCtx{
			SpeakLang:   ca.config.Agent.SpeakLang,
			ProjectPath: workDir,
			OS:          runtime.GOOS,
			Arch:        runtime.GOARCH,
			Publisher:   publisher,

			FileOps:        tools.NewFileOperationsTool(workDir),
			SearchOps:      tools.NewSearchOperationsTool(workDir),
			SysOps:         tools.NewSystemOperationsTool(workDir),
			ReplaceTool:    tools.NewReplaceBlockTool(workDir),
			ThinkingTool:   tools.NewThinkingTool(),
			MicroAgentTool: tools.NewMicroAgentTool(microAgentEngine),
			FlowOps:        tools.NewFlowControlTool(workDir),
			RepoOps:        tools.NewRepoOperationsTool(codeSeekMCP, workDir, 0),
			UserConfirmMgr: userConfirmMgr,
			DeepThinkingTool: func() *tools.DeepThinkingTool {
				dt := tools.NewDeepThinkingTool(deepthinkingEngine)
				if publisher != nil {
					dt.StreamHandler = func(ctx context.Context, chunk []byte) error {
						if len(chunk) > 0 {
							publisher.Publish("ai_chunk", map[string]interface{}{
								"content": string(chunk),
								"agent":   "deepthinking",
							}, "deepthinking")
						}
						return nil
					}
				}
				return dt
			}(),

			DesignTool: func() *tools.DesignTool {
				design := tools.NewDesignTool(designEngine)
				if publisher != nil {
					design.StreamHandler = func(ctx context.Context, chunk []byte) error {
						if len(chunk) > 0 {
							publisher.Publish("ai_chunk", map[string]interface{}{
								"content": string(chunk),
								"agent":   "design",
							}, "design")
						}
						return nil
					}
				}
				return design
			}(),

			CodeSeekMCP: codeSeekMCP,

			GitCheckpointCfg: &ca.config.GitCheckpoint,

			EnhancedCommander: ca.config.EnhancedCommander,
		}
		ca.globalCtx = &gctx

		userConfirmMgr.SetPublisher(publisher)
		gctx.FlowOps.UserConfirmMgr = userConfirmMgr

		guard := tools.NewWorkspaceGuard(workDir, userConfirmMgr)
		gctx.Guard = guard
		if ca.dispatcher != nil {
			ca.dispatcher.RegisterConsumer(userConfirmMgr)
		}

		isFullYolo := ca.FullYoloMode || (ca.config != nil && ca.config.Agent.FullYoloMode)
		gctx.FullYoloMode = isFullYolo

		isYolo := ca.YoloMode || (ca.config != nil && ca.config.Agent.YoloMode) || isFullYolo

		if isYolo {
			guard.SetYoloMode(true)
			slog.Info("🚀 YOLO mode enabled — all authorization checks bypassed")
		}
		if isFullYolo {
			slog.Info("🔥 FULL-YOLO mode enabled — autonomous decision-making, ask_user_for_help removed")
		}
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
		metaRetryCount := defaultSteps.MetaRetry
		if ca.config != nil && ca.config.Agent.MetaRetryCount > 0 {
			metaRetryCount = ca.config.Agent.MetaRetryCount
		}

		disabledAgents := parseDisabledAgents(ca.DisabledAgents)

		directorEngine := ca.agentEngine("director", engine)
		repoEngine := ca.agentEngine("repo", engine)
		codingEngine := ca.agentEngine("coding", engine)
		chatEngine := ca.agentEngine("chat", engine)
		metaEngine := ca.agentEngine("meta", engine)
		devopsEngine := ca.agentEngine("devops", engine)
		browserEngine := ca.agentEngine("browser", engine)

		repoCtx := globalctx.NewRepoContextStore()
		env := globalctx.NewEnvView(&gctx)

		browserMgr := browserctl.NewManager(browserctl.FromAppConfig(ca.config.Browser))
		if ok, reason := browserMgr.Probe(); ok {
			slog.Info("Browser-Agent 就绪（browser-control 可用）")
		} else {
			slog.Warn("Browser-Agent 不可用，delegate_browser 描述已降级", "reason", reason)
		}
		browserMgr.ReapStale(context.Background())
		browserMgr.CleanupExpiredWorkspaces()
		ca.browserMgr = browserMgr

		stepRetries := 0
		if ca.config != nil {
			stepRetries = ca.config.LLM.StepRetries
		}

		ca.blueprint = ca.buildBlueprint()

		repoAgent := agents.NewRepoAgent(agents.RepoAgentParams{
			Formatter:            &gctx,
			Publisher:            publisher,
			Env:                  env,
			Files:                gctx.FileOps,
			Search:               gctx.SearchOps,
			RepoOps:              gctx.RepoOps,
			Flow:                 gctx.FlowOps,
			DeepThinker:          gctx.DeepThinkingTool,
			Guard:                guard,
			EnhancedCommanderCfg: ca.config.EnhancedCommander,
			Blueprint:            ca.blueprint.viewFor("repo"),
			External:             agents.ExternalFrom(ca),
			LLM:                  repoEngine,
			MaxSteps:             repoMaxSteps,
			StepRetries:          stepRetries,
		})

		chatAgent := agents.NewChatAgent(agents.ChatAgentParams{
			Formatter:   &gctx,
			Publisher:   publisher,
			Env:         env,
			Flow:        gctx.FlowOps,
			Thinker:     gctx.ThinkingTool,
			DeepThinker: gctx.DeepThinkingTool,
			MicroAgent:  gctx.MicroAgentTool,
			Guard:       guard,
			LLM:         chatEngine,
			MaxSteps:    chatMaxSteps,
			StepRetries: stepRetries,
			Blueprint:   ca.blueprint.viewFor("chat"),
			External:    agents.ExternalFrom(ca),
		})
		metaAgent := agents.NewMetaAgent(&gctx, publisher, metaEngine, stepRetries)
		devopsAgent := agents.NewDevOpsAgent(agents.DevOpsAgentParams{
			Formatter:            &gctx,
			Publisher:            publisher,
			Env:                  env,
			Files:                gctx.FileOps,
			Search:               gctx.SearchOps,
			Sys:                  gctx.SysOps,
			Flow:                 gctx.FlowOps,
			Thinker:              gctx.ThinkingTool,
			MicroAgent:           gctx.MicroAgentTool,
			Guard:                guard,
			EnhancedCommanderCfg: ca.config.EnhancedCommander,
			LLM:                  devopsEngine,
			MaxSteps:             devopsMaxSteps,
			StepRetries:          stepRetries,
			Blueprint:            ca.blueprint.viewFor("devops"),
			External:             agents.ExternalFrom(ca),
		})
		repoAgent.SetDevOpsAgent(devopsAgent)
		browserAgent := agents.NewBrowserAgent(agents.BrowserAgentParams{
			Formatter:            &gctx,
			Publisher:            publisher,
			Env:                  env,
			Flow:                 gctx.FlowOps,
			Guard:                guard,
			Browser:              browserMgr,
			BrowserCfg:           ca.config.Browser,
			EnhancedCommanderCfg: ca.config.EnhancedCommander,
			LLM:                  browserEngine,
			MaxSteps:             browserMaxSteps,
			StepRetries:          stepRetries,
			Blueprint:            ca.blueprint.viewFor("browser"),
			External:             agents.ExternalFrom(ca),
		})

		codingAgent := agents.NewCodingAgent(agents.CodingAgentParams{
			Formatter:            &gctx,
			Publisher:            publisher,
			Env:                  env,
			Files:                gctx.FileOps,
			Search:               gctx.SearchOps,
			Sys:                  gctx.SysOps,
			Edit:                 gctx.ReplaceTool,
			RepoOps:              gctx.RepoOps,
			Flow:                 gctx.FlowOps,
			Thinker:              gctx.ThinkingTool,
			DeepThinker:          gctx.DeepThinkingTool,
			Designer:             gctx.DesignTool,
			MicroAgent:           gctx.MicroAgentTool,
			Guard:                guard,
			RepoCtx:              repoCtx,
			GitCheckpointCfg:     &ca.config.GitCheckpoint,
			EnhancedCommanderCfg: ca.config.EnhancedCommander,
			Blueprint:            ca.blueprint.viewFor("coding"),
			External:             agents.ExternalFrom(ca),
			LLM:                  codingEngine,
			MaxSteps:             codingMaxSteps,
			StepRetries:          stepRetries,
			Browser:              browserAgent,
		})

		ca.director = agents.NewDirectorAgent(agents.DirectorAgentParams{
			Formatter:      &gctx,
			Publisher:      publisher,
			Env:            env,
			Files:          gctx.FileOps,
			Search:         gctx.SearchOps,
			Sys:            gctx.SysOps,
			Edit:           gctx.ReplaceTool,
			Flow:           gctx.FlowOps,
			Thinker:        gctx.ThinkingTool,
			MicroAgent:     gctx.MicroAgentTool,
			DeepThinker:    gctx.DeepThinkingTool,
			Repo:           gctx.RepoOps,
			Guard:          guard,
			RepoCtx:        repoCtx,
			LLM:            directorEngine,
			RepoAgent:      repoAgent,
			CodingAgent:    codingAgent,
			ChatAgent:      chatAgent,
			MetaAgent:      metaAgent,
			DevOpsAgent:    devopsAgent,
			BrowserAgent:   browserAgent,
			MaxSteps:       directorMaxSteps,
			DisabledAgents: disabledAgents,
			MetaRetryCount: metaRetryCount,
			Config:         *ca.config,
			LLMClient:      ca.client,
			Blueprint:      ca.blueprint.viewFor("director"),
			External:       agents.ExternalFrom(ca),
		})

		codingAgent.SetBoardWriter(ca.director)
	})
}

func (ca *Yagent) IntegrateMessaging(dispatcher *messaging.MessageDispatcher) {
	ca.dispatcher = dispatcher
	if ca.globalCtx != nil && ca.globalCtx.Publisher != nil {
		ca.globalCtx.Publisher.SetDispatcher(dispatcher)
	}
	if ca.globalCtx != nil && ca.globalCtx.UserConfirmMgr != nil {
		dispatcher.RegisterConsumer(ca.globalCtx.UserConfirmMgr)
	}
}

type TaskRequest struct {
	ctx                   context.Context
	taskID                string
	projectDir            string
	taskDesc              string
	memory                *memory.ConversationMemory
	wsCallback            func(string, string)
	publisher             *messaging.MessagePublisher
	userMessage           string
	sessionID             string
	resumed               bool
	resumedDelegationPath string
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

func (r *TaskRequest) WithResumed(resumed bool) *TaskRequest {
	r.resumed = resumed
	return r
}

func (r *TaskRequest) WithResumedDelegation(directorRolloutPath string) *TaskRequest {
	r.resumedDelegationPath = directorRolloutPath
	return r
}

func resolveResumption(req *TaskRequest) *agents.DelegationStop {
	if req.resumedDelegationPath == "" {
		return nil
	}
	stops, err := agents.ScanDelegationStops(memory.RolloutRootDir())
	if err != nil {
		slog.Warn("resume: failed to scan delegation stops", "error", err)
		return nil
	}
	for _, s := range stops {
		if s.DirectorRolloutPath == req.resumedDelegationPath {
			return &s
		}
	}
	slog.Warn("resume: no stopped delegation found in this session", "rollout", req.resumedDelegationPath)
	return nil
}

func (r *TaskRequest) WithSessionID(sessionID string) *TaskRequest {
	r.sessionID = sessionID
	return r
}

type ResumableDelegation struct {
	AgentName  string
	ToolName   string
	Step       int
	MaxSteps   int
	Replayable bool
	Reasons    []string
}

func (ca *Yagent) ResumableDelegation(directorRolloutPath string) *ResumableDelegation {
	if directorRolloutPath == "" {
		return nil
	}
	stops, err := agents.ScanDelegationStops(memory.RolloutRootDir())
	if err != nil {
		slog.Warn("resume: failed to scan delegation stops", "error", err)
		return nil
	}
	for _, s := range stops {
		if s.DirectorRolloutPath == directorRolloutPath {
			return &ResumableDelegation{
				AgentName: s.AgentName, ToolName: s.ToolName, Step: s.Step, MaxSteps: s.MaxSteps,
				Replayable: s.Replayable, Reasons: s.Reasons,
			}
		}
	}
	return nil
}

func (ca *Yagent) applyResumption(req *TaskRequest) {
	stop := resolveResumption(req)
	ca.director.SetPendingResumptionOpt(stop)
}

func (ca *Yagent) ProcessCodingTaskWithCallback(req *TaskRequest) (string, error) {
	ca.Init(ca.defaultEngine(), req.projectDir)
	ca.director.SetBoardScope(req.sessionID, req.taskID)
	ca.director.SetResumedTask(req.resumed)
	ca.applyResumption(req)
	res, err := ca.director.Run(memory.WithConversationMemory(req.ctx, req.memory), req.taskDesc)
	return res.Text, err
}

func (ca *Yagent) ProcessConversation(req *TaskRequest) (string, error) {
	ca.Init(ca.defaultEngine(), req.projectDir)
	ca.director.SetBoardScope(req.sessionID, req.taskID)
	ca.director.SetResumedTask(req.resumed)
	ca.applyResumption(req)
	res, err := ca.director.Run(memory.WithConversationMemory(req.ctx, req.memory), req.userMessage)
	return res.Text, err
}

func (ca *Yagent) SwitchProvider(providerName string) error {
	if ca.client == nil {
		return fmt.Errorf("LLM client not available")
	}
	if err := ca.client.SwitchProvider(providerName); err != nil {
		return err
	}
	ca.setEngine(ca.client.DefaultEngine())
	return nil
}

func (ca *Yagent) GetClient() *llm.Client {
	return ca.client
}

func (ca *Yagent) SetRoutingMode(mode string) error {
	if ca.client == nil {
		return fmt.Errorf("LLM client not available")
	}
	return ca.client.SetRoutingMode(mode)
}

func (ca *Yagent) PersistRoutingMode(mode string) error {
	if ca.client == nil {
		return fmt.Errorf("LLM client not available")
	}
	return ca.client.PersistRoutingMode(mode)
}

func (ca *Yagent) GetRouteSnapshot() llm.RouteSnapshot {
	if ca.client == nil {
		return llm.RouteSnapshot{Mode: "fixed", Reason: "LLM client not available"}
	}
	return ca.client.GetRouter().Snapshot()
}

func (ca *Yagent) agentEngine(agentName string, fallback llm.Engine) llm.Engine {
	if ca.client == nil {
		return fallback
	}
	return llm.NewRoutingEngine(ca.client, agentName)
}

func (ca *Yagent) SetAgentProvider(agentName, providerName string) error {
	if ca.client == nil {
		return fmt.Errorf("LLM client not available")
	}
	return ca.client.SetAgentProvider(agentName, providerName)
}

func (ca *Yagent) GetAgentProvider(agentName string) (string, string) {
	if ca.client == nil {
		return "", ""
	}
	return ca.client.GetAgentProvider(agentName)
}

func (ca *Yagent) GetAllAgentOverrides() map[string]string {
	if ca.client == nil {
		return nil
	}
	return ca.client.GetAllAgentOverrides()
}

func (ca *Yagent) SetToolProvider(toolName, providerName string) error {
	if ca.client == nil {
		return fmt.Errorf("LLM client not available")
	}
	if err := ca.client.SetToolProvider(toolName, providerName); err != nil {
		return err
	}
	if toolName == "deepthinking" && ca.globalCtx != nil && ca.globalCtx.DeepThinkingTool != nil {
		e := ca.client.GetToolEngine("deepthinking")
		ca.globalCtx.DeepThinkingTool.SetEngine(e)
		slog.Info("Hot-swapped deepthinking tool engine",
			"provider", providerName,
			"model", e.Model())
	}
	if toolName == "design" && ca.globalCtx != nil && ca.globalCtx.DesignTool != nil {
		e := ca.client.GetToolEngine("design")
		ca.globalCtx.DesignTool.SetEngine(e)
		slog.Info("Hot-swapped design tool engine",
			"provider", providerName,
			"model", e.Model())
	}
	return nil
}

func (ca *Yagent) GetToolProvider(toolName string) (string, string, bool) {
	if ca.client == nil {
		return "", "", false
	}
	return ca.client.GetToolProvider(toolName)
}

func (ca *Yagent) GetToolProviderInfo(toolName string) (string, string) {
	if ca.client == nil {
		return "", ""
	}
	return ca.client.GetToolProviderInfo(toolName)
}

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

func (ca *Yagent) Close() {
	ca.blueprint.stop()
	if ca.browserMgr != nil {
		if err := ca.browserMgr.Close(context.Background()); err != nil {
			slog.Warn("停止浏览器失败", "error", err)
		}
	}
	if ca.mcpMgr != nil {
		if ca.ForceQuit {
			slog.Info("Force quitting MCP manager...")
			ca.mcpMgr.ForceClose()
		} else {
			slog.Info("Shutting down MCP manager...")
			ca.mcpMgr.Close()
		}
	}

	if leaked := supervise.Root().Drain(3 * time.Second); len(leaked) > 0 {
		slog.Warn("退出时仍有 goroutine 在跑", "count", len(leaked), "leases", supervise.LeaseNames(leaked))
	}
}

func (ca *Yagent) resolveBlueprintRoot() (string, error) {
	if !config.BlueprintEnabled() {
		return "", nil
	}
	return blueprint.Prepare(ca.globalCtx.ProjectPath)
}
