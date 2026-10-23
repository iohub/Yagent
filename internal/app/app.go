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

// Yagent is the main entry point for the agent system.
type Yagent struct {
	engine llm.Engine // default engine (backward-compatible)
	// engineMu 保护 engine：TUI 的模型切换会在任务运行中替换它，而任务线程每轮都读。
	engineMu sync.RWMutex

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

	// mcpMgr 是全部 MCP server 的生命周期（codeseek 是其中一个内建 server）。
	// nil = [mcp] 关掉、或没有任何 server。
	mcpMgr *mcp.Manager
	// mcpConfigs 按 server 名保存策略（白名单/allow_write/curated），
	// BuildExternalSpecs 需要它，而 Manager 只认启动参数。
	mcpConfigs map[string]mcp.ServerConfig

	// specMu 保护外部工具规格快照。读侧是每轮重建工具面的六个 Agent，
	// 写侧是 Manager 的状态回调 —— 所以是 RWMutex 而不是 atomic.Pointer 数组。
	specMu          sync.RWMutex
	externalSpecs   []*toolspec.Spec
	externalAccount tools.ExternalAccount

	// skills 是本进程的技能注册表（nil = 技能被关掉；use_skill 会解释为什么没有）。
	// 加载在 Init 里做一次：三个入口（TUI / HTTP / --prompt）共用同一份合并语义。
	skills       *skills.Registry
	skillAccount skills.LoadAccount

	// blueprint 是本进程的仓库蓝图运行时（nil = 这个项目没有蓝图）。
	// 唯一持有者：派生循环、存储根、以及三处注入视图都从它出，
	// 于是"关掉整套机制"只需要一处判据（I8）。
	blueprint *blueprintRuntime

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
		engine:               client.DefaultEngine(),
		client:               client,
		config:               client.Config,
	}
	return ca, nil
}

// setEngine 与 defaultEngine 是 engine 字段唯一的读写入口（锁保护）。
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

// SetEmbeddedBinaries 设置嵌入的二进制文件系统，用于自动提取 codeseek 等工具
func (ca *Yagent) SetEmbeddedBinaries(fs embed.FS) {
	ca.embeddedBinFS = fs
}

// CloseIdleConnections 关闭 LLM engine 的 HTTP 空闲连接。
// 在任务取消时调用，加速底层连接释放和 context 取消传播。
func (ca *Yagent) CloseIdleConnections() {
	if e := ca.defaultEngine(); e != nil {
		e.CloseIdleConnections()
	}
}

// checkRegistry 把静态注册表自检的问题逐条打进日志。
// 抽成函数只为一件事：Validate 的问题无法从外部注入，"这条自检真的会被调用"
// 只能靠一个能喂假问题的入口来证明（TestInitRunsRegistrySelfCheck）。
func checkRegistry(validate func() []string, logger *slog.Logger) {
	if problems := validate(); len(problems) > 0 {
		logger.Error("tool registry self-check failed", "count", len(problems), "problems", problems)
	}
}

// Init initializes the assistant with Engine and creates agents.
// Uses per-agent and per-tool engine resolution from the LLM client.
func (ca *Yagent) Init(engine llm.Engine, workDir string) {
	// 始终更新 engine（后续调用也需要更新 engine）
	ca.setEngine(engine)

	// initOnce 保证只执行一次完整初始化（创建 Agents、启动 MCP 客户端等）
	ca.initOnce.Do(func() {
		// 静态注册表自检。spec.go 的 Validate 注释承诺"装配期调用"，而在此之前全仓
		// 只有测试调它 —— 生产从不检查。逐条打日志而不是退出：构建期问题已被
		// registry_gate_test 拦住，运行期退出只会让"配置坏了"与"代码坏了"同一个症状。
		checkRegistry(toolspec.Validate, slog.Default())

		// 便签板只增不减，所以在装配的最开始清一次。这里传 nil 作为"活动 board"名单是刻意的：
		// 进程刚起来、Director 还没构造，本就没有正在写的便签板；本次会话的板是这一刻才创建的，
		// mtime 新鲜，TTL 与条数上限都碰不到它。
		if blackboard.Enabled() {
			cfg := config.GlobalBlackboard()
			if _, _, err := blackboard.GC(blackboard.GCOptions{
				TTL:              cfg.TTL,
				MaxNotesPerBoard: cfg.MaxNotesPerBoard,
			}, nil); err != nil {
				// 清理失败绝不能阻断启动：磁盘占用是可容忍的问题，装配失败才是事故。
				slog.Warn("blackboard: gc failed", "error", err)
			}
		}

		// ── 技能：三个入口共用一份加载器（合并 user → project → [skills] dirs）──
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

		// ── 提取嵌入的二进制（独立于 codeseek 配置）──
		// 无论用户是否配置了 [codeseek] binary_path，都要把嵌入的 codeseek/fzf/rg/browser-control
		// 解出到 ~/.yagent/bin：browserctl 等模块的二级查找（~/.yagent/bin/<name>）依赖这些文件。
		binDir := ""
		if ca.config != nil {
			if dir, err := embedbin.ExtractBinaries(ca.embeddedBinFS, "dist/bin"); err != nil {
				slog.Debug("Failed to extract embedded binaries; tools will fall back to PATH lookup", "error", err)
			} else {
				binDir = dir
			}
		}

		// ── 确定 codeseek 二进制路径并启动 MCP 客户端 ──
		codeseekBinaryPath := ""

		// 优先级1：配置中显式指定的路径（用户手动设置）
		if ca.config != nil && ca.config.CodeSeek.BinaryPath != "" {
			codeseekBinaryPath = ca.config.CodeSeek.BinaryPath
			slog.Info("Using configured CodeSeek binary path", "path", codeseekBinaryPath)
		} else if binDir != "" {
			// 优先级2：使用启动时解出的嵌入二进制
			candidatePath := filepath.Join(binDir, "codeseek")
			if _, statErr := os.Stat(candidatePath); statErr == nil {
				codeseekBinaryPath = candidatePath
				slog.Info("Auto-extracted embedded CodeSeek binary", "path", codeseekBinaryPath)
			} else {
				slog.Debug("Embedded codeseek binary not found in extracted files, CodeSeek MCP will not be available")
			}
		}

		// ── MCP Manager：codeseek（内建）+ [[mcp.servers]]（用户）──
		// codeseek 从"唯一特例"变成第一个内建 server：它的 6 个手写桥继续由 Go 侧
		// curated 名单占用，其余工具（init/status/list）走通用 tools/list 通道。
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
			// Start 只 spawn 子进程，initialize 与 tools/list 在后台跑；
			// **这里绝不等它们** —— 等待会把"终端启动卡在 MCP"变成用户可感知的故障，
			// 而 codeseek 冷启动本来就要建索引（改动前也是同步 spawn + 后台 initialize）。
			ca.mcpMgr = mgr
			if err := mgr.Start(context.Background()); err != nil {
				slog.Warn("Failed to start MCP manager", "error", err)
			}
			// 既有 RepoOperationsTool 一行不改：它在 Init 这一刻捕获客户端指针，
			// 所以 Manager 必须在 Start 返回前就把进程起好（见 mcp/manager.go 的 Start）。
			codeSeekMCP = mgr.Client("codeseek")
			ca.rebuildExternalSpecs()
			slog.Info("mcp starting", "servers", len(cfgs), "lines", mgr.StatusLines())
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

		// [llm] step_retries：Director 走 planner 的 Recovery、Meta 走 JSON 重试，
		// 子 Agent 现在也吃同一个值（默认 0 = 关，因此对现有配置零差异）。
		stepRetries := 0
		if ca.config != nil {
			stepRetries = ca.config.LLM.StepRetries
		}

		// [仓库蓝图] 启动装配。返回 nil = "这个项目没有蓝图"（关掉、或根护栏拒绝），
		// 于是三处注入、read_blueprint、落盘钩子拿到的都是 nil —— 退回改动前的行为，
		// 不需要每一处再问一遍开关（I8：启用判据只有一处）。
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

		// Director 比 CodingAgent 晚构造，便签写入器只能事后回注：
		// 它让 Coding→Browser 的二级委派也上便签板（索引前置 + 写便签）。
		codingAgent.SetBoardWriter(ca.director)
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
	// sessionID 决定便签板（boardID）的作用域：同一会话内的多次任务共享一块板，
	// 于是前一次委派写下的结论能被下一次直接读到，而不必重新调查一遍。
	// 空串 ⇒ 回落到 taskID（HTTP 未带会话标识、--prompt 单发就是这个形态）。
	sessionID string
	// resumed 本次请求是否跑在恢复出来的历史会话上（TUI /resume 或 history 加载后
	// 的首次追问）。Director 据此在续跑第一轮强制注入项目上下文：恢复的 memory 里
	// 本就没有 system 消息（reader 默认丢弃、datamanager 从不存储），非首次对话判定
	// 会漏掉 YAGENT.md/CLAUDE.md/AGENTS.md，LLM 等于蒙着眼睛接手半截任务。
	resumed bool
	// resumption 是"从子 Agent 的那一步继续"的落点（TUI 的 /resume 选项、将来 HTTP
	// 的 continue 都走这里）。nil = 正常新任务或"从 Director 继续"。
	resumption *agents.DelegationStop
	// resumedDelegationPath 是"从被停住的那次委派继续"的**指路牌**而不是结果：
	// TUI 只知道"我刚恢复的是哪份 Director 会话"，扫落点、算降级判据都在 app/agents 这边。
	// 这样 TUI 不必 import internal/agents（它至今只认识 app）。
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

// WithResumption 直接指定落点（测试与 HTTP 用，调用方已经扫过一遍）。
func (r *TaskRequest) WithResumption(stop *agents.DelegationStop) *TaskRequest {
	r.resumption = stop
	return r
}

// WithResumedDelegation 指定"从这份 Director 会话里被停住的那次委派继续"。
// 落点由 resolveResumption 在提交时算，调用方只交出它刚恢复的那个 rollout 路径。
func (r *TaskRequest) WithResumedDelegation(directorRolloutPath string) *TaskRequest {
	r.resumedDelegationPath = directorRolloutPath
	return r
}

// resolveResumption 把请求里的两种表达归一成 Director 门面要的那一个。
// 找不到落点时返回 nil 并告警 —— "用户点了从子 Agent 继续，结果静默变成从
// Director 重来"是必须能在工作日志里看到的事。
func resolveResumption(req *TaskRequest) *agents.DelegationStop {
	if req.resumption != nil {
		return req.resumption
	}
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

// WithSessionID 指定便签板作用域。同一会话（TUI 的一个 tab、HTTP 的一个 chat session）
// 内的多次任务共享一块板；不指定则回落到 taskID，行为与改动前一致。
func (r *TaskRequest) WithSessionID(sessionID string) *TaskRequest {
	r.sessionID = sessionID
	return r
}

// ResumableDelegation 报告"这份 Director 会话是不是停在一次委派上、能不能接着做完"。
//
// 返回的是 TUI 需要的那点信息（Agent 名、步数、能不能重放、不能的话为什么），
// 而不是 agents.DelegationStop —— 否则 TUI 就得 import internal/agents，
// 而它今天只认识 app。
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

// applyResumption 把请求里的"从子 Agent 继续"落到 Director 门面上。
// 每轮都显式设置（含 nil），否则上一次的落点会漏到下一次任务里。
func (ca *Yagent) applyResumption(req *TaskRequest) {
	stop := resolveResumption(req)
	ca.director.SetPendingResumptionOpt(stop)
}

// ProcessCodingTaskWithCallback executes the task using the agent system.
func (ca *Yagent) ProcessCodingTaskWithCallback(req *TaskRequest) (string, error) {
	ca.Init(ca.defaultEngine(), req.projectDir)
	ca.director.SetBoardScope(req.sessionID, req.taskID)
	ca.director.SetResumedTask(req.resumed)
	ca.applyResumption(req)
	res, err := ca.director.Run(memory.WithConversationMemory(req.ctx, req.memory), req.taskDesc)
	return res.Text, err
}

// ProcessConversation handles chat messages.
func (ca *Yagent) ProcessConversation(req *TaskRequest) (string, error) {
	ca.Init(ca.defaultEngine(), req.projectDir)
	ca.director.SetBoardScope(req.sessionID, req.taskID)
	ca.director.SetResumedTask(req.resumed)
	ca.applyResumption(req)
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
	ca.setEngine(ca.client.DefaultEngine())
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
	// 停蓝图派生循环并等它退。放在最前面：它是最短的一条后台活（一次扫描最坏 2s），
	// 而 `--prompt` 那条一次性路径的收尾时间全靠它别拖住退出。
	ca.blueprint.stop()
	// 停止 Browser-Agent 启动的 Chrome。上游 stop 自带归属校验
	//（命令行需含 --remote-debugging-port 且 user-data-dir 匹配），不会误杀用户自己的浏览器。
	if ca.browserMgr != nil {
		if err := ca.browserMgr.Close(context.Background()); err != nil {
			slog.Warn("停止浏览器失败", "error", err)
		}
	}
	// 关闭 MCP Manager（它会逐个收掉 codeseek 与用户配置的 server）。
	// 次序在 Drain 之前：Manager 的 goroutine 属于它自己的域，等它先退干净，
	// 根域剩下的才是真正的"故意不归任何一次任务管"的后台活。
	if ca.mcpMgr != nil {
		if ca.ForceQuit {
			slog.Info("Force quitting MCP manager...")
			ca.mcpMgr.ForceClose()
		} else {
			slog.Info("Shutting down MCP manager...")
			ca.mcpMgr.Close()
		}
	}

	// 最后一步：等进程根域里剩下的 goroutine（各家清理已经各自等过自己的子域，
	// 走到这里的都是"故意不归任何一次任务收尾管"的后台活，比如 MCP 初始化看门狗、
	// 后台命令的僵尸回收）。预算很短 —— 退出不能因为它们而挂住，
	// 但一定要把"还有什么在跑"报名出来。
	if leaked := supervise.Root().Drain(3 * time.Second); len(leaked) > 0 {
		slog.Warn("退出时仍有 goroutine 在跑", "count", len(leaked), "leases", supervise.LeaseNames(leaked))
	}
}

// resolveBlueprintRoot 决定本项目蓝图的存储根，是 [blueprint] enable 的唯一读取点。
//
// 三种结果要能被调用方区分开：
//   - ("", nil)   整套机制被关掉（配置或 YAGENT_BLUEPRINT=0）—— 什么都不做是对的
//   - ("", err)   开着，但这个目录不该建蓝图：I16 的根护栏（$HOME、/、既非 git 又无 manifest），
//     或 I7（存储根落进了项目里）
//   - (root, nil) 就绪
func (ca *Yagent) resolveBlueprintRoot() (string, error) {
	if !config.BlueprintEnabled() {
		return "", nil
	}
	return blueprint.Prepare(ca.globalCtx.ProjectPath)
}
