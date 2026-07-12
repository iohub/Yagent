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
	"codeactor/internal/compact"
	"codeactor/internal/config"
	"codeactor/internal/embedbin"
	"codeactor/internal/globalctx"
	"codeactor/internal/llm"
	"codeactor/internal/logging"
	"codeactor/internal/memory"
	"codeactor/internal/mcp"
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
	// TODO: [Codexray] CodexrayPort field removed — re-add when codexray is re-integrated

	SkillRegistry      *skills.SkillRegistry // 技能注册表，加载 .codeactor/skills/ 下的 .md 文件

	// [NEW] 记忆系统
	sharedMemory                *memory.SharedMemory
	consolidationWorker         *agents.ConsolidationWorker
	sharedConsolidationRunner   *agents.SharedMemoryConsolidationRunner

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
		Publisher:   publisher,
		// TODO: [Codexray] CodexrayURL field removed — re-add when codexray is re-integrated

		// Tools
		FileOps:          tools.NewFileOperationsTool(workDir),
		SearchOps:        tools.NewSearchOperationsTool(workDir),
		SysOps:           tools.NewSystemOperationsTool(workDir),
		ReplaceTool:      tools.NewReplaceBlockTool(workDir),
		ThinkingTool:     tools.NewThinkingTool(),
		MicroAgentTool:   tools.NewMicroAgentTool(microAgentEngine),
		FlowOps:          tools.NewFlowControlTool(workDir),
		RepoOps:          tools.NewRepoOperationsTool(codeSeekMCP, workDir, 0),
		UserConfirmMgr:   userConfirmMgr,
		DeepThinkingTool: tools.NewDeepThinkingTool(deepthinkingEngine),

		// BrowserMgr 浏览器管理器（单例，管理 Chromium 浏览器实例生命周期）
		BrowserMgr: nil,

		// CodeSeekMCP MCP 客户端（用于代码分析，nil=未启用）
		CodeSeekMCP: codeSeekMCP,

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

	// [NEW] Initialize Shared Memory System (3 dimensions: user, feedback, reference)
	{
		sharedDimStore := memory.NewSharedDimensionStore(ca.sharedMemory)
		sharedDimInjector := memory.NewSharedMemoryInjector(sharedDimStore)
		sharedDimUpdater := memory.NewSharedDimensionUpdater(sharedDimStore, memory.DefaultRestraintPolicy())
		// 添加操作日志记录器
		sharedDimUpdater.Logger = memory.NewSharedMemoryLogger()
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

		// Create update_shared_memory tool function (field-targeted API)
		updateMemFn := func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			dimStr, _ := params["dimension"].(string)
			actionStr, _ := params["action"].(string)
			fieldStr, _ := params["field"].(string)
			reason, _ := params["reason"].(string)
			value := params["value"] // 可为 nil
			itemID, _ := params["item_id"].(string)

			if dimStr == "" || actionStr == "" || fieldStr == "" || reason == "" {
				return "Missing required parameters: dimension, field, action, and reason.", nil
			}

			// Use field registry to validate
			registry := memory.NewDimensionFieldRegistry()
			result := registry.Validate(dimStr, fieldStr, actionStr, value, itemID)
			if !result.Valid {
				if len(result.Errors) > 0 {
					return fmt.Sprintf("❌ Validation error: %s\nHint: %s", result.Errors[0].Message, result.Errors[0].Hint), nil
				}
				return "❌ Validation failed", nil
			}

			resolved := result.Resolved

			// Build legacy-style payload for backward compatibility with existing updater
			payload := make(map[string]interface{})
			switch resolved.Action {
			case "set":
				payload[resolved.Field] = resolved.Value
			case "add":
				payload[resolved.Field] = resolved.Value
			case "remove":
				payload[resolved.Field] = resolved.Value
				payload["_remove_item_id"] = resolved.ItemID
			}

			proposal := &memory.MemoryUpdateProposal{
				Dimension:  memory.Dimension(resolved.Dimension),
				Action:     memory.UpdateAction(resolved.Action),
				Payload:    payload,
				Reason:     reason,
				ProposedBy: "agent",
				Metadata: map[string]interface{}{
					"user_id":    "default",
					"project_id": projectPath,
				},
			}

			updateResult := sharedDimUpdater.ApplyUpdate(proposal)

			// If auto-corrected, append a note to the result
			if result.Corrected {
				corr := result.Corrections[0]
				return fmt.Sprintf("%s\n⚠️ Note: '%s' was auto-corrected to '%s'. Please use '%s' in future calls.",
					updateResult.Reason, corr.Original, corr.Corrected, corr.Corrected), nil
			}

			return updateResult.Reason, nil
		}

		// Build adapter with field-targeted schema
		updateMemAdapter := tools.NewAdapter("update_shared_memory",
			"Update the shared cross-agent memory system. Use this to persist important user information (user dimension), feedback (feedback dimension), or reference resources (reference dimension) across all agents and conversations.",
			updateMemFn,
		).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"dimension": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"user", "feedback", "reference"},
					"description": "Which memory dimension to update",
				},
				"field": map[string]interface{}{
					"type":        "string",
					"description": "Target field name for the dimension. Use memory_query_schema to see valid fields. Examples: user: name/role/team/seniority/expertise/language/detail_level/code_style/response_format/metadata; feedback: corrections/endorsements; reference: resources",
				},
				"action": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"set", "add", "remove"},
					"description": "Operation type: set=replace scalar or set full array, add=append single item to array, remove=remove array item by item_id",
				},
				"value": map[string]interface{}{
					"description": "Value to set or add. For scalar fields (name/role/team/status etc.): a string. For array fields (expertise/corrections/objectives etc.): a single item object or string. Omit for action=remove.",
				},
				"item_id": map[string]interface{}{
					"type":        "string",
					"description": "ID of the array item to remove. Only used when action=remove. Get item IDs from memory query output.",
				},
				"reason": map[string]interface{}{
					"type":        "string",
					"description": "Why this update matters (min 3 chars). Be specific, e.g. 'user prefers concise responses' or 'added new team member'",
				},
			},
			"required": []string{"dimension", "field", "action", "reason"},
		})

		// Register tool on all agents that use Adapters
		codingAgent.Adapters = append(codingAgent.Adapters, updateMemAdapter)
		chatAgent.Adapters = append(chatAgent.Adapters, updateMemAdapter)
		devopsAgent.Adapters = append(devopsAgent.Adapters, updateMemAdapter)
		repoAgent.Adapters = append(repoAgent.Adapters, updateMemAdapter)
		browserAgent.Adapters = append(browserAgent.Adapters, updateMemAdapter)

		slog.Info("Shared memory system initialized",
			"dimensions", []string{"user", "feedback", "reference"},
		)
	}

	// Create DirectorAgent
	ca.director = agents.NewDirectorAgent(ca.globalCtx, directorEngine, repoAgent, codingAgent, chatAgent, metaAgent, devopsAgent, browserAgent, directorMaxSteps, disabledAgents, metaRetryCount, compactCfg, summaryEngine, *ca.config, ca.client)

	// Set shared memory on DirectorAgent (created after shared memory block, so we set it here)
	ca.director.BaseAgent.MemoryInjector = memory.NewSharedMemoryInjector(
		memory.NewSharedDimensionStore(ca.sharedMemory),
	)
	ca.director.BaseAgent.MemoryUpdater = memory.NewSharedDimensionUpdater(
		memory.NewSharedDimensionStore(ca.sharedMemory),
		memory.DefaultRestraintPolicy(),
	)
	// 添加操作日志记录器
	ca.director.BaseAgent.MemoryUpdater.Logger = memory.NewSharedMemoryLogger()

	// Register update_shared_memory tool on DirectorAgent (field-targeted API)
	{
		updateMemFn := func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			dimStr, _ := params["dimension"].(string)
			actionStr, _ := params["action"].(string)
			fieldStr, _ := params["field"].(string)
			reason, _ := params["reason"].(string)
			value := params["value"] // 可为 nil
			itemID, _ := params["item_id"].(string)

			if dimStr == "" || actionStr == "" || fieldStr == "" || reason == "" {
				return "Missing required parameters: dimension, field, action, and reason.", nil
			}

			// Use field registry to validate
			registry := memory.NewDimensionFieldRegistry()
			result := registry.Validate(dimStr, fieldStr, actionStr, value, itemID)
			if !result.Valid {
				if len(result.Errors) > 0 {
					return fmt.Sprintf("❌ Validation error: %s\nHint: %s", result.Errors[0].Message, result.Errors[0].Hint), nil
				}
				return "❌ Validation failed", nil
			}

			resolved := result.Resolved

			// Build legacy-style payload for backward compatibility with existing updater
			payload := make(map[string]interface{})
			switch resolved.Action {
			case "set":
				payload[resolved.Field] = resolved.Value
			case "add":
				payload[resolved.Field] = resolved.Value
			case "remove":
				payload[resolved.Field] = resolved.Value
				payload["_remove_item_id"] = resolved.ItemID
			}

			// Use Director's MemoryUpdater from BaseAgent
			updater := ca.director.BaseAgent.MemoryUpdater
			if updater == nil {
				return "Memory updater not available.", nil
			}

			proposal := &memory.MemoryUpdateProposal{
				Dimension:  memory.Dimension(resolved.Dimension),
				Action:     memory.UpdateAction(resolved.Action),
				Payload:    payload,
				Reason:     reason,
				ProposedBy: "director",
				Metadata: map[string]interface{}{
					"user_id":    "default",
					"project_id": ca.globalCtx.ProjectPath,
				},
			}

			updateResult := updater.ApplyUpdate(proposal)

			// If auto-corrected, append a note to the result
			if result.Corrected {
				corr := result.Corrections[0]
				return fmt.Sprintf("%s\n⚠️ Note: '%s' was auto-corrected to '%s'. Please use '%s' in future calls.",
					updateResult.Reason, corr.Original, corr.Corrected, corr.Corrected), nil
			}

			return updateResult.Reason, nil
		}

		directorMemAdapter := tools.NewAdapter("update_shared_memory",
			"Update the shared cross-agent memory system. Use this to persist important user information, feedback, or reference resources across all agents and conversations.",
			updateMemFn,
		).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"dimension": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"user", "feedback", "reference"},
					"description": "Which memory dimension to update",
				},
				"field": map[string]interface{}{
					"type":        "string",
					"description": "Target field name for the dimension. Use memory_query_schema to see valid fields. Examples: user: name/role/team/seniority/expertise/language/detail_level/code_style/response_format/metadata; feedback: corrections/endorsements; reference: resources",
				},
				"action": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"set", "add", "remove"},
					"description": "Operation type: set=replace scalar or set full array, add=append single item to array, remove=remove array item by item_id",
				},
				"value": map[string]interface{}{
					"description": "Value to set or add. For scalar fields (name/role/team/status etc.): a string. For array fields (expertise/corrections/objectives etc.): a single item object or string. Omit for action=remove.",
				},
				"item_id": map[string]interface{}{
					"type":        "string",
					"description": "ID of the array item to remove. Only used when action=remove. Get item IDs from memory query output.",
				},
				"reason": map[string]interface{}{
					"type":        "string",
					"description": "Why this update matters (min 3 chars). Be specific, e.g. 'user prefers Go' or 'important note about architecture'",
				},
			},
			"required": []string{"dimension", "field", "action", "reason"},
		})

		ca.director.Adapters = append(ca.director.Adapters, directorMemAdapter)
		slog.Info("DirectorAgent update_shared_memory tool registered")
	}

	// ============================================================
	// memory_query_schema 工具 — 查询 payload 字段结构 (registry-backed)
	// ============================================================
	{
		querySchemaFn := func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			dimStr, _ := params["dimension"].(string)
			registry := memory.NewDimensionFieldRegistry()

			if dimStr != "" {
				schemaJSON := registry.FormatSchemaAsJSON(dimStr)
				if schemaJSON == "" {
					return fmt.Sprintf("Unknown dimension: %s. Available: user, feedback, reference", dimStr), nil
				}
				return fmt.Sprintf("📋 Schema for '%s' dimension:\n\n%s\n\nTip: Use 'field' parameter with one of the valid fields above when calling update_shared_memory.", dimStr, schemaJSON), nil
			}

			// Return summary of all dimensions
			var result string
			for _, dim := range registry.GetAllDimensions() {
				schema := registry.GetDimensionSchema(dim)
				fields := make([]string, 0, len(schema.Fields))
				for name, f := range schema.Fields {
					fields = append(fields, fmt.Sprintf("  - %s (%s): %s", name, f.Type, f.Description))
				}
				result += fmt.Sprintf("=== %s ===\n%s\n\n", dim, strings.Join(fields, "\n"))
			}
			result += "Pass 'dimension' parameter to get detailed JSON schema for a specific dimension."
			return result, nil
		}

		querySchemaAdapter := tools.NewAdapter("memory_query_schema",
			"Query the payload schema for any memory dimension. Use this to check valid field names and types before calling update_shared_memory.",
			querySchemaFn,
		).WithSchema(map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"dimension": map[string]interface{}{
					"type":        "string",
					"enum":        []string{"user", "feedback", "reference"},
					"description": "Dimension to query. Empty returns all schemas.",
				},
			},
		})

		// Register to all agents
		codingAgent.Adapters = append(codingAgent.Adapters, querySchemaAdapter)
		chatAgent.Adapters = append(chatAgent.Adapters, querySchemaAdapter)
		devopsAgent.Adapters = append(devopsAgent.Adapters, querySchemaAdapter)
		repoAgent.Adapters = append(repoAgent.Adapters, querySchemaAdapter)
		browserAgent.Adapters = append(browserAgent.Adapters, querySchemaAdapter)
		ca.director.Adapters = append(ca.director.Adapters, querySchemaAdapter)

		slog.Info("memory_query_schema tool registered for all agents")
	}

	// [NEW] Start shared memory consolidation runner (periodic LLM deep consolidation)
	{
		sharedDimStore := memory.NewSharedDimensionStore(ca.sharedMemory)
		consolidationRunner := agents.NewSharedMemoryConsolidationRunner(
			sharedDimStore,
			repoEngine, // 复用 repoEngine 进行整合
			30*time.Minute,
			"default",
			ca.globalCtx.ProjectPath,
		)
		consolidationRunner.Start()
		ca.sharedConsolidationRunner = consolidationRunner
		slog.Info("Shared memory consolidation runner started",
			"interval", "30m",
		)
	}
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
	// 停止共享记忆整合运行器
	if ca.sharedConsolidationRunner != nil {
		slog.Info("Stopping shared memory consolidation runner...")
		ca.sharedConsolidationRunner.Stop()
	}
	// 关闭 CodeSeek MCP 客户端
	if ca.globalCtx != nil && ca.globalCtx.CodeSeekMCP != nil {
		slog.Info("Shutting down CodeSeek MCP client...")
		ca.globalCtx.CodeSeekMCP.Shutdown()
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
