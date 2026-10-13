package app

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"yagent/internal/agents"
	"yagent/internal/browserctl"
	"yagent/internal/config"
	"yagent/internal/embedbin"
	"yagent/internal/globalctx"
	"yagent/internal/knowledge"
	"yagent/internal/llm"
	"yagent/internal/mcp"
	"yagent/internal/memory"
	"yagent/internal/messaging"
	"yagent/internal/skills"
	"yagent/internal/tools"
)

// Yagent is the main entry point for the agent system.
type Yagent struct {
	engine               llm.Engine  // default engine (backward-compatible)
	client               *llm.Client // LLM client for per-agent/tool engine resolution
	config               *config.Config
	director             *agents.DirectorAgent
	dispatcher           *messaging.MessageDispatcher
	mu                   sync.Mutex
	userResponseChannels map[string]chan string
	logger               *slog.Logger

	globalCtx      *globalctx.GlobalCtx
	DisabledAgents string // comma-separated list of agent names to disable (e.g. "repo,coding,chat")
	YoloMode       bool   // YOLO模式：所有agent授权自动通过
	FullYoloMode   bool   // FULL-YOLO模式：隐含YoloMode + 移除ask_user_for_help + 自主决策
	ForceQuit      bool   // ForceQuit：强制退出模式，agent_exit 时直接退出，不等待 codeseek 进程安全退出

	// browserMgr 浏览器执行层：管理外部 browser-control CLI 与它启动的 Chrome 生命周期
	browserMgr *browserctl.Manager

	SkillRegistry *skills.SkillRegistry // 技能注册表，加载 .yagent/skills/ 下的 .md 文件

	// [NEW] 记忆系统
	sharedMemory        *memory.SharedMemory
	consolidationWorker *agents.ConsolidationWorker

	// embeddedBinFS 嵌入的二进制文件系统（用于自动提取 codeseek 等工具）
	embeddedBinFS embed.FS

	// initOnce 保证 Init() 只执行一次，防止重复初始化导致资源泄漏
	initOnce sync.Once
}

// NewYagent creates a new Yagent.
func NewYagent(client *llm.Client) (*Yagent, error) {
	ca := &Yagent{
		userResponseChannels: make(map[string]chan string),
		logger:               slog.Default().With("component", "coding_assistant"),
		engine:               client.Engine,
		client:               client,
		config:               client.Config,
	}
	return ca, nil
}

// SetEmbeddedBinaries 设置嵌入的二进制文件系统，用于自动提取 codeseek 等工具
func (ca *Yagent) SetEmbeddedBinaries(fs embed.FS) {
	ca.embeddedBinFS = fs
}

// CloseIdleConnections 关闭 LLM engine 的 HTTP 空闲连接。
// 在任务取消时调用，加速底层连接释放和 context 取消传播。
func (ca *Yagent) CloseIdleConnections() {
	if ca.engine != nil {
		ca.engine.CloseIdleConnections()
	}
}

// Init initializes the assistant with Engine and creates agents.
// Uses per-agent and per-tool engine resolution from the LLM client.
func (ca *Yagent) Init(engine llm.Engine, workDir string) {
	// 始终更新 engine（后续调用也需要更新 engine）
	ca.engine = engine

	// initOnce 保证只执行一次完整初始化（创建 Agents、启动 MCP 客户端等）
	ca.initOnce.Do(func() {
		// Initialize agents
		publisher := messaging.NewMessagePublisher(ca.dispatcher)

		userConfirmMgr := tools.NewUserConfirmManager()

		// Resolve tool-specific engine for micro_agent
		microAgentEngine := engine
		if ca.client != nil {
			microAgentEngine = ca.client.GetToolEngine("micro_agent")
		}

		// Resolve tool-specific engine for deepthinking
		// 自动路由启用时，deepthinking 固定使用高智商档位引擎。
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

		// Resolve tool-specific engine for design
		// design 不挂自动路由档位：走纯配置（[tools.llm.design]），
		// 保留用户独立指定编码前方案设计模型的能力。
		designEngine := engine
		if ca.client != nil {
			designEngine = ca.client.GetToolEngine("design")
		}

		// ── 确定 codeseek 二进制路径并启动 MCP 客户端 ──
		codeseekBinaryPath := ""

		// 优先级1：配置中显式指定的路径（用户手动设置）
		if ca.config != nil && ca.config.CodeSeek.BinaryPath != "" {
			codeseekBinaryPath = ca.config.CodeSeek.BinaryPath
			slog.Info("Using configured CodeSeek binary path", "path", codeseekBinaryPath)
		} else if ca.config != nil {
			// 优先级2：从嵌入的二进制中自动解压
			binDir, err := embedbin.ExtractBinaries(ca.embeddedBinFS, "dist/bin")
			if err != nil {
				slog.Debug("No embedded binaries to extract, CodeSeek MCP will not be available", "error", err)
			} else {
				candidatePath := filepath.Join(binDir, "codeseek")
				if _, statErr := os.Stat(candidatePath); statErr == nil {
					codeseekBinaryPath = candidatePath
					slog.Info("Auto-extracted embedded CodeSeek binary", "path", codeseekBinaryPath)
				} else {
					slog.Debug("Embedded codeseek binary not found in extracted files, CodeSeek MCP will not be available")
				}
			}
		}

		// ── 启动 CodeSeek MCP 客户端 ──
		var codeSeekMCP *mcp.MCPClient
		if codeseekBinaryPath != "" {
			// 解析 MCP 参数，默认使用 ["serve", "--mcp"]
			mcpArgs := []string{"serve", "--mcp"}
			requestTimeout := 30
			if ca.config != nil {
				if len(ca.config.CodeSeek.MCPArgs) > 0 {
					mcpArgs = ca.config.CodeSeek.MCPArgs
				}
				if ca.config.CodeSeek.RequestTimeout > 0 {
					requestTimeout = ca.config.CodeSeek.RequestTimeout
				}
			}

			codeSeekMCP = mcp.NewMCPClient(mcp.MCPClientConfig{
				BinaryPath:     codeseekBinaryPath,
				Args:           mcpArgs,
				WorkingDir:     workDir,
				RequestTimeout: time.Duration(requestTimeout) * time.Second,
			})
			// 异步启动 MCP 客户端，不阻塞等待 codeseek init 完成
			// 子进程启动成功后立即返回，初始化在后台 goroutine 中执行
			if err := codeSeekMCP.Start(context.Background()); err != nil {
				slog.Warn("Failed to start CodeSeek MCP client, repo exploration tools will be unavailable", "error", err)
				codeSeekMCP = nil
			} else {
				slog.Info("CodeSeek MCP client starting in background (codeseek init runs async)")
				// 后台监控初始化结果（仅用于日志记录）
				go func() {
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
					defer cancel()
					if err := codeSeekMCP.WaitForReady(ctx); err != nil {
						slog.Warn("CodeSeek MCP client initialization failed", "error", err)
					} else {
						slog.Info("CodeSeek MCP client initialized and ready for code analysis")
					}
				}()
			}
		}

		gctx := globalctx.GlobalCtx{
			SpeakLang:   ca.config.Agent.SpeakLang,
			ProjectPath: workDir,
			OS:          runtime.GOOS,
			Arch:        runtime.GOARCH,
			// Global utility
			Publisher: publisher,

			// Tools
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

			// CodeSeekMCP MCP 客户端（用于代码分析，nil=未启用）
			CodeSeekMCP: codeSeekMCP,

			// Git Checkpoint config
			GitCheckpointCfg: &ca.config.GitCheckpoint,

			// EnhancedCommander 配置
			EnhancedCommander: ca.config.EnhancedCommander,
		}
		ca.globalCtx = &gctx

		// [知识管理] 创建 KnowledgeInjector（依赖 CodeSeekMCP，在 MCP 客户端就绪后生效）
		// publisher 用于向 TUI 发布 knowledge_injected 概要事件（injector 内部 fail-safe）
		if ca.config != nil && ca.config.CodeSeek.Knowledge.Enabled && ca.globalCtx.CodeSeekMCP != nil {
			ca.globalCtx.KnowledgeInjector = knowledge.NewKnowledgeInjector(ca.globalCtx.CodeSeekMCP, ca.config.CodeSeek.Knowledge, publisher)
			slog.Info("KnowledgeInjector initialized", "enabled", true)
		}

		// Wire up UserConfirmManager: register as consumer and set publisher
		userConfirmMgr.SetPublisher(publisher)
		gctx.FlowOps.UserConfirmMgr = userConfirmMgr

		// Create workspace guard for authorizing dangerous operations
		guard := tools.NewWorkspaceGuard(workDir, userConfirmMgr)
		gctx.Guard = guard
		if ca.dispatcher != nil {
			ca.dispatcher.RegisterConsumer(userConfirmMgr)
		}

		// Determine FullYoloMode and apply to GlobalCtx
		isFullYolo := ca.FullYoloMode || (ca.config != nil && ca.config.Agent.FullYoloMode)
		gctx.FullYoloMode = isFullYolo

		// FullYolo implies Yolo
		isYolo := ca.YoloMode || (ca.config != nil && ca.config.Agent.YoloMode) || isFullYolo

		// Apply YOLO mode
		if isYolo {
			guard.SetYoloMode(true)
			slog.Info("🚀 YOLO mode enabled — all authorization checks bypassed")
		}
		if isFullYolo {
			slog.Info("🔥 FULL-YOLO mode enabled — autonomous decision-making, ask_user_for_help removed")
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

		// Resolve per-agent engines
		// 自动路由启用时，每个 agent 拿到一个绑定角色基线的 RoutingEngine；
		// 实际档位在每次 delegate 时由难度分类器写入 ctx，RunAgentLoop 起始处解析。
		directorEngine := ca.agentEngine("director", engine)
		repoEngine := ca.agentEngine("repo", engine)
		codingEngine := ca.agentEngine("coding", engine)
		chatEngine := ca.agentEngine("chat", engine)
		metaEngine := ca.agentEngine("meta", engine)
		devopsEngine := ca.agentEngine("devops", engine)
		browserEngine := ca.agentEngine("browser", engine)

		// 创建 RepoContextStore（带锁仓库上下文存储：Director 写入 delegate_repo 结果，
		// coding/browser agent 读取）与 EnvView（运行时环境字段动态读取视图）。
		// 替代原 GlobalCtx.RepoSummary 共享可变字段，消除数据竞争。
		repoCtx := globalctx.NewRepoContextStore()
		env := globalctx.NewEnvView(&gctx)

		// ── 浏览器执行层：外部 browser-control CLI（Go 侧不引浏览器依赖）──
		browserMgr := browserctl.NewManager(browserctl.FromAppConfig(ca.config.Browser))
		if ok, reason := browserMgr.Probe(); ok {
			slog.Info("Browser-Agent 就绪（browser-control 可用）")
		} else {
			slog.Warn("Browser-Agent 不可用，delegate_browser 描述已降级", "reason", reason)
		}
		// 回收上次崩溃遗留的浏览器实例，并清理过期工作区（含 traces 与截图）
		browserMgr.ReapStale(context.Background())
		browserMgr.CleanupExpiredWorkspaces()
		ca.browserMgr = browserMgr

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
			Knowledge:            gctx.KnowledgeInjector,
			MCPClient:            gctx.CodeSeekMCP,
			EnhancedCommanderCfg: ca.config.EnhancedCommander,
			LLM:                  repoEngine,
			MaxSteps:             repoMaxSteps,
		})

		// [NEW] 初始化 RepoAgent 记忆系统
		{
			ca.sharedMemory = memory.NewSharedMemory(100)
			// 启用文件持久化，保存到 $HOME/.yagent/data/shared_memory/{projectid}/
			sharedMemPath := ca.getSharedMemoryPath()
			sharedMemDir := filepath.Dir(sharedMemPath)
			if err := os.MkdirAll(sharedMemDir, 0700); err != nil {
				slog.Warn("Failed to create shared memory directory", "path", sharedMemDir, "error", err)
			} else if err := ca.sharedMemory.EnablePersistence(5*time.Second, sharedMemPath); err != nil {
				slog.Warn("Shared memory persistence not available (first run?)", "path", sharedMemPath, "error", err)
			} else {
				slog.Info("Shared memory persistence enabled", "path", sharedMemPath, "interval", "5s")
			}
			repoID := ca.globalCtx.ProjectPath
			repoMemStore := agents.NewRepoMemoryStore(repoID, ca.sharedMemory)
			if err := repoMemStore.Load(context.Background()); err != nil {
				slog.Warn("RepoAgent memory preload failed, continuing without memory",
					"error", err,
				)
			}
			consolidationWorker := agents.NewConsolidationWorker(repoMemStore, repoEngine, ca.globalCtx.CodeSeekMCP)
			consolidationWorker.Start()
			ca.consolidationWorker = consolidationWorker
			repoAgent.SetMemory(repoMemStore, consolidationWorker)
			slog.Info("RepoAgent memory system initialized",
				"repo_id", repoID,
				"has_memory", !repoMemStore.IsEmpty(),
			)
		}

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
		})
		stepRetries := 0
		if ca.config != nil {
			stepRetries = ca.config.LLM.StepRetries
		}
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
		})
		// 注入 DevOps-Agent，使 Repo-Agent 的 delegate_devops 工具可用
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
			Knowledge:            gctx.KnowledgeInjector,
			MCPClient:            gctx.CodeSeekMCP,
			GitCheckpointCfg:     &ca.config.GitCheckpoint,
			EnhancedCommanderCfg: ca.config.EnhancedCommander,
			LLM:                  codingEngine,
			MaxSteps:             codingMaxSteps,
			Browser:              browserAgent,
		})

		// Create DirectorAgent
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
			Knowledge:      gctx.KnowledgeInjector,
			MCPClient:      gctx.CodeSeekMCP,
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
		})
	})
}

func (ca *Yagent) IntegrateMessaging(dispatcher *messaging.MessageDispatcher) {
	ca.dispatcher = dispatcher
	// 同步更新 publisher 的 dispatcher 引用，确保已创建的 Publisher 能正确路由事件
	if ca.globalCtx != nil && ca.globalCtx.Publisher != nil {
		ca.globalCtx.Publisher.SetDispatcher(dispatcher)
	}
	// 注册 UserConfirmManager 到新的 dispatcher 上，使其能收到 user_help_response 事件
	if ca.globalCtx != nil && ca.globalCtx.UserConfirmMgr != nil {
		dispatcher.RegisterConsumer(ca.globalCtx.UserConfirmMgr)
	}
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
	// resumed 本次请求是否跑在恢复出来的历史会话上（TUI /resume 或 history 加载后
	// 的首次追问）。Director 据此在续跑第一轮强制注入项目上下文：恢复的 memory 里
	// 本就没有 system 消息（reader 默认丢弃、datamanager 从不存储），非首次对话判定
	// 会漏掉 YAGENT.md/CLAUDE.md/AGENTS.md，LLM 等于蒙着眼睛接手半截任务。
	resumed bool
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

// ProcessCodingTaskWithCallback executes the task using the agent system.
func (ca *Yagent) ProcessCodingTaskWithCallback(req *TaskRequest) (string, error) {
	ca.Init(ca.engine, req.projectDir)
	ca.director.SetTaskID(req.taskID)
	ca.director.SetResumedTask(req.resumed)
	res, err := ca.director.Run(memory.WithConversationMemory(req.ctx, req.memory), req.taskDesc)
	return res.Text, err
}

// ProcessConversation handles chat messages.
func (ca *Yagent) ProcessConversation(req *TaskRequest) (string, error) {
	ca.Init(ca.engine, req.projectDir)
	ca.director.SetTaskID(req.taskID)
	ca.director.SetResumedTask(req.resumed)
	res, err := ca.director.Run(memory.WithConversationMemory(req.ctx, req.memory), req.userMessage)
	return res.Text, err
}

// SwitchProvider dynamically switches the LLM provider for all agents.
// The change takes effect on the next task execution (since Init() is called
// at the start of ProcessCodingTaskWithCallback / ProcessConversation).
func (ca *Yagent) SwitchProvider(providerName string) error {
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
func (ca *Yagent) GetClient() *llm.Client {
	return ca.client
}

// SetRoutingMode 运行时切换自动路由模式（"auto" | "fixed"），立即生效。
func (ca *Yagent) SetRoutingMode(mode string) error {
	if ca.client == nil {
		return fmt.Errorf("LLM client not available")
	}
	return ca.client.SetRoutingMode(mode)
}

// PersistRoutingMode 将路由模式写回 config.toml。
func (ca *Yagent) PersistRoutingMode(mode string) error {
	if ca.client == nil {
		return fmt.Errorf("LLM client not available")
	}
	return ca.client.PersistRoutingMode(mode)
}

// GetRouteSnapshot 返回自动路由的只读快照，供 TUI 展示。
func (ca *Yagent) GetRouteSnapshot() llm.RouteSnapshot {
	if ca.client == nil {
		return llm.RouteSnapshot{Mode: "fixed", Reason: "LLM client not available"}
	}
	return ca.client.GetRouter().Snapshot()
}

// agentEngine 解析某个 agent 应使用的引擎：始终返回 RoutingEngine 包装，
// 它在自动路由启用时按档位分派，固定模式 / 路由不可用时透明回退到该 agent 的
// provider 级引擎，因此运行时切换 auto/fixed 无需重建 agent。
func (ca *Yagent) agentEngine(agentName string, fallback llm.Engine) llm.Engine {
	if ca.client == nil {
		return fallback
	}
	return llm.NewRoutingEngine(ca.client, agentName)
}

// SetAgentProvider 为指定 agent 设置运行时 provider 覆盖（通过 LLM Client）
func (ca *Yagent) SetAgentProvider(agentName, providerName string) error {
	if ca.client == nil {
		return fmt.Errorf("LLM client not available")
	}
	return ca.client.SetAgentProvider(agentName, providerName)
}

// GetAgentProvider 返回指定 agent 当前生效的 provider 名称和模型名
func (ca *Yagent) GetAgentProvider(agentName string) (string, string) {
	if ca.client == nil {
		return "", ""
	}
	return ca.client.GetAgentProvider(agentName)
}

// GetAllAgentOverrides 返回所有运行时 agent provider 覆盖的副本
func (ca *Yagent) GetAllAgentOverrides() map[string]string {
	if ca.client == nil {
		return nil
	}
	return ca.client.GetAllAgentOverrides()
}

// SetToolProvider 为指定工具设置运行时 provider 覆盖。
// 对 "deepthinking"/"design" 额外执行热替换：重新解析其 engine 并替换
// globalCtx 中对应工具的 LLM，保证后续 Execute 立即使用新模型。
func (ca *Yagent) SetToolProvider(toolName, providerName string) error {
	if ca.client == nil {
		return fmt.Errorf("LLM client not available")
	}
	if err := ca.client.SetToolProvider(toolName, providerName); err != nil {
		return err
	}
	if toolName == "deepthinking" && ca.globalCtx != nil && ca.globalCtx.DeepThinkingTool != nil {
		ca.globalCtx.DeepThinkingTool.LLM = ca.client.GetToolEngine("deepthinking")
		slog.Info("Hot-swapped deepthinking tool engine",
			"provider", providerName,
			"model", ca.globalCtx.DeepThinkingTool.LLM.Model())
	}
	if toolName == "design" && ca.globalCtx != nil && ca.globalCtx.DesignTool != nil {
		ca.globalCtx.DesignTool.LLM = ca.client.GetToolEngine("design")
		slog.Info("Hot-swapped design tool engine",
			"provider", providerName,
			"model", ca.globalCtx.DesignTool.LLM.Model())
	}
	return nil
}

// GetToolProvider 返回指定工具的运行时 provider 覆盖及是否生效
func (ca *Yagent) GetToolProvider(toolName string) (string, string, bool) {
	if ca.client == nil {
		return "", "", false
	}
	return ca.client.GetToolProvider(toolName)
}

// GetToolProviderInfo 返回指定工具当前生效的 provider 名称和模型名
// 优先返回运行时覆盖，其次返回配置中的解析结果
func (ca *Yagent) GetToolProviderInfo(toolName string) (string, string) {
	if ca.client == nil {
		return "", ""
	}
	return ca.client.GetToolProviderInfo(toolName)
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
func (ca *Yagent) Close() {
	// 停止 consolidation worker
	if ca.consolidationWorker != nil {
		slog.Info("Stopping consolidation worker...")
		ca.consolidationWorker.Stop()
	}
	// 停止 Browser-Agent 启动的 Chrome。上游 stop 自带归属校验
	//（命令行需含 --remote-debugging-port 且 user-data-dir 匹配），不会误杀用户自己的浏览器。
	if ca.browserMgr != nil {
		if err := ca.browserMgr.Close(context.Background()); err != nil {
			slog.Warn("停止浏览器失败", "error", err)
		}
	}
	// 关闭 CodeSeek MCP 客户端
	if ca.globalCtx != nil && ca.globalCtx.CodeSeekMCP != nil {
		if ca.ForceQuit {
			slog.Info("Force quitting CodeSeek MCP client...")
			ca.globalCtx.CodeSeekMCP.ForceShutdown()
		} else {
			slog.Info("Shutting down CodeSeek MCP client...")
			ca.globalCtx.CodeSeekMCP.Shutdown()
		}
	}
}

// getProjectID generates a filesystem-safe, unique project identifier
// from the project path. Format: {sanitized_basename}_{short_hash}
// Example: "/home/user/my-app" → "my_app_a1b2c3d4e5f6"
func getProjectID(projectPath string) string {
	// Use the last path component as the human-readable prefix
	base := filepath.Base(projectPath)
	if base == "." || base == "/" {
		base = "root"
	}

	// Sanitize: keep only alphanumeric characters, replace others with underscore
	sanitized := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, base)

	// Truncate to leave room for "_" + 12-char hash (filesystem limit: 255 bytes per component)
	const maxBaseLen = 242
	if len(sanitized) > maxBaseLen {
		sanitized = sanitized[:maxBaseLen]
	}

	// Generate short hash for global uniqueness
	h := sha256.Sum256([]byte(projectPath))
	shortHash := hex.EncodeToString(h[:])[:12]

	return sanitized + "_" + shortHash
}

// getSharedMemoryPath returns the shared memory file path.
// Preferred: $HOME/.yagent/data/shared_memory/{projectID}/shared_memory.json
// Fallback:  {ProjectPath}/.shared_memory.json (if home dir is unavailable)
func (ca *Yagent) getSharedMemoryPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fallback to project-local path (preserves old behavior)
		slog.Warn("Cannot determine home directory, falling back to project-local shared memory", "error", err)
		return filepath.Join(ca.globalCtx.ProjectPath, ".shared_memory.json")
	}

	projectID := getProjectID(ca.globalCtx.ProjectPath)
	return filepath.Join(homeDir, ".yagent", "data", "shared_memory", projectID, "shared_memory.json")
}
