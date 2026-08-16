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

	"codeactor/internal/agents"
	"codeactor/internal/browser"
	"codeactor/internal/config"
	"codeactor/internal/embedbin"
	"codeactor/internal/globalctx"
	"codeactor/internal/knowledge"
	"codeactor/internal/llm"
	"codeactor/internal/mcp"
	"codeactor/internal/memory"
	"codeactor/internal/messaging"
	"codeactor/internal/skills"
	"codeactor/internal/tools"
)

// CodeActor is the main entry point for the agent system.
type CodeActor struct {
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

	SkillRegistry *skills.SkillRegistry // 技能注册表，加载 .codeactor/skills/ 下的 .md 文件

	// [NEW] 记忆系统
	sharedMemory        *memory.SharedMemory
	consolidationWorker *agents.ConsolidationWorker

	// embeddedBinFS 嵌入的二进制文件系统（用于自动提取 codeseek 等工具）
	embeddedBinFS embed.FS

	// initOnce 保证 Init() 只执行一次，防止重复初始化导致资源泄漏
	initOnce sync.Once
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

// SetEmbeddedBinaries 设置嵌入的二进制文件系统，用于自动提取 codeseek 等工具
func (ca *CodeActor) SetEmbeddedBinaries(fs embed.FS) {
	ca.embeddedBinFS = fs
}

// CloseIdleConnections 关闭 LLM engine 的 HTTP 空闲连接。
// 在任务取消时调用，加速底层连接释放和 context 取消传播。
func (ca *CodeActor) CloseIdleConnections() {
	if ca.engine != nil {
		ca.engine.CloseIdleConnections()
	}
}

// Init initializes the assistant with Engine and creates agents.
// Uses per-agent and per-tool engine resolution from the LLM client.
func (ca *CodeActor) Init(engine llm.Engine, workDir string) {
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
		deepthinkingEngine := engine
		if ca.client != nil {
			deepthinkingEngine = ca.client.GetToolEngine("deepthinking")
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

			// BrowserMgr 浏览器管理器（单例，管理 Chromium 浏览器实例生命周期）
			BrowserMgr: nil,

			// CodeSeekMCP MCP 客户端（用于代码分析，nil=未启用）
			CodeSeekMCP: codeSeekMCP,

			// Git Checkpoint config
			GitCheckpointCfg: &ca.config.GitCheckpoint,

			// EnhancedCommander 配置
			EnhancedCommander: ca.config.EnhancedCommander,
		}
		ca.globalCtx = &gctx

		// [知识管理] 创建 KnowledgeInjector（依赖 CodeSeekMCP，在 MCP 客户端就绪后生效）
		if ca.config != nil && ca.config.CodeSeek.Knowledge.Enabled && ca.globalCtx.CodeSeekMCP != nil {
			ca.globalCtx.KnowledgeInjector = knowledge.NewKnowledgeInjector(ca.globalCtx.CodeSeekMCP, ca.config.CodeSeek.Knowledge)
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

		repoAgent := agents.NewRepoAgent(ca.globalCtx, repoEngine, publisher, repoMaxSteps)

		// [NEW] 初始化 RepoAgent 记忆系统
		{
			ca.sharedMemory = memory.NewSharedMemory(100)
			// 启用文件持久化，保存到 $HOME/.codeactor/data/shared_memory/{projectid}/
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

		codingAgent := agents.NewCodingAgent(ca.globalCtx, codingEngine, codingMaxSteps, browserAgent)

		// Create DirectorAgent
		ca.director = agents.NewDirectorAgent(ca.globalCtx, directorEngine, repoAgent, codingAgent, chatAgent, metaAgent, devopsAgent, browserAgent, directorMaxSteps, disabledAgents, metaRetryCount, *ca.config, ca.client)
	})
}

func (ca *CodeActor) IntegrateMessaging(dispatcher *messaging.MessageDispatcher) {
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
	ca.director.SetTaskID(req.taskID)
	return ca.director.Run(req.ctx, req.taskDesc, req.memory)
}

// ProcessConversation handles chat messages.
func (ca *CodeActor) ProcessConversation(req *TaskRequest) (string, error) {
	ca.Init(ca.engine, req.projectDir)
	ca.director.SetTaskID(req.taskID)
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
	if ca.globalCtx != nil && ca.globalCtx.BrowserMgr != nil {
		slog.Info("Closing browser manager...")
		if err := ca.globalCtx.BrowserMgr.Close(); err != nil {
			slog.Warn("Failed to close browser manager", "error", err)
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
// Preferred: $HOME/.codeactor/data/shared_memory/{projectID}/shared_memory.json
// Fallback:  {ProjectPath}/.shared_memory.json (if home dir is unavailable)
func (ca *CodeActor) getSharedMemoryPath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		// Fallback to project-local path (preserves old behavior)
		slog.Warn("Cannot determine home directory, falling back to project-local shared memory", "error", err)
		return filepath.Join(ca.globalCtx.ProjectPath, ".shared_memory.json")
	}

	projectID := getProjectID(ca.globalCtx.ProjectPath)
	return filepath.Join(homeDir, ".codeactor", "data", "shared_memory", projectID, "shared_memory.json")
}
