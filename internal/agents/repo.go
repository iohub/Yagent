package agents

import (
	"context"
	_ "embed"
	"fmt"
	"yagent/internal/blackboard"
	"yagent/internal/toolspec"

	"yagent/internal/config"
	"yagent/internal/knowledge"
	"yagent/internal/mcp"
	"yagent/internal/thinklink"
	"yagent/internal/tools"

	"yagent/internal/llm"
)

//go:embed repo.prompt.md
var repoPrompt string

type RepoAgent struct {
	BaseAgent
	promptFmt   PromptFormatter
	env         Env
	knowledge   KnowledgeProvider
	mcpClient   *mcp.MCPClient
	ecCfg       config.EnhancedCommanderConfig
	params      RepoAgentParams
	maxSteps    int
	stepRetries int

	// DevOps-Agent（可选，nil 表示未注入，delegate_devops 将返回友好提示）
	devops *DevOpsAgent

	// [NEW] 记忆系统（可选，nil 表示禁用）
	memStore *RepoMemoryStore
	worker   *ConsolidationWorker
}

// RepoAgentParams 聚合 NewRepoAgent 所需的全部依赖。
// 用具名字段替代原先 14 个位置参数，让调用点自文档化，且增删依赖不再破坏函数签名。
type RepoAgentParams struct {
	Formatter   PromptFormatter
	Publisher   EventBus
	Env         Env
	Files       FileToolSet
	Search      SearchToolSet
	RepoOps     RepoToolSet
	Flow        FlowToolSet
	DeepThinker DeepThinker
	Guard       *tools.WorkspaceGuard
	Knowledge   KnowledgeProvider
	MCPClient   *mcp.MCPClient

	EnhancedCommanderCfg config.EnhancedCommanderConfig

	LLM         llm.Engine
	MaxSteps    int
	StepRetries int
}

// NewRepoAgent 构造 RepoAgent：先填充结构体字段，再装配工具适配器
// （tools.json 常规工具 + delegate_devops + 知识维护工具）。
func NewRepoAgent(p RepoAgentParams) *RepoAgent {
	self := &RepoAgent{
		BaseAgent: BaseAgent{
			LLM:       p.LLM,
			Publisher: p.Publisher,
		},
		promptFmt:   p.Formatter,
		env:         p.Env,
		knowledge:   p.Knowledge,
		mcpClient:   p.MCPClient,
		ecCfg:       p.EnhancedCommanderCfg,
		maxSteps:    p.MaxSteps,
		stepRetries: p.StepRetries,
	}
	self.params = p
	return self
}

// devopsDelegate 是 Repo 自己的委派实现：只认 devops 一个目标，并沿用历史的
// "友好降级"文案（DevOps 未接线/执行失败都不抛错，而是把说明回给模型）。
// 与 Director 的委派流水线不同：这里不写便签、不做档位路由、不落 artifact。
func (a *RepoAgent) devopsDelegate(ctx context.Context, target string, params map[string]interface{}) (interface{}, error) {
	if target != "devops" {
		return nil, fmt.Errorf("Repo-Agent 只能委派给 devops，收到 %q", target)
	}
	task, ok := params["task"].(string)
	if !ok {
		return nil, fmt.Errorf("task parameter required")
	}
	// 每次调用一个新实例：共享单例的 DevOps 一旦被两条并发 Repo 委派同时用，
	// 今天没有可变状态不代表以后没有（见 spawn.go 的不变量）。
	devops := a.devopsFor(ctx)
	if devops == nil {
		return "[DevOps-Agent unavailable] DevOps-Agent has not been wired to Repo-Agent. Complete the analysis with the information you have and note that the operational task could not be delegated.", nil
	}
	result, err := devops.Run(ctx, task)
	if err != nil {
		return fmt.Sprintf("[DevOps-Agent execution failed] task: %q, error: %v. You may retry with a clearer task description, or note the failure in your analysis result.", task, err), nil
	}
	return result.Text, nil
}

func (a *RepoAgent) Name() string {
	return "Repo-Agent"
}

// SetMemory 注入记忆系统依赖。记忆系统可选，不设置时 Run() 行为与改造前一致。
func (a *RepoAgent) SetMemory(store *RepoMemoryStore, worker *ConsolidationWorker) {
	a.memStore = store
	a.worker = worker
}

// SetDevOpsAgent 注入 DevOps-Agent 依赖，使 delegate_devops 工具可用。
// DevOps-Agent 可选，不设置时 delegate_devops 调用会返回友好提示。
func (a *RepoAgent) SetDevOpsAgent(devops *DevOpsAgent) {
	a.devops = devops
}

func (a *RepoAgent) Run(ctx context.Context, input string) (AgentResult, error) {
	systemPrompt := repoPrompt

	if a.env.ProjectPath() == "" {
		return AgentResult{}, fmt.Errorf("project_dir is empty")
	}

	systemPrompt = a.promptFmt.FormatPrompt(systemPrompt)

	if a.memStore != nil {
		memContent := a.memStore.Get()
		if injection := RenderMemoryForInjection(memContent); injection != "" {
			systemPrompt += injection
		}
	}

	// [知识管理] 对话前动态知识检索注入
	if a.knowledge != nil {
		injCtx := knowledge.InjectionContext{
			UserMessage: input,
			TargetFiles: nil,
			AgentName:   a.Name(),
			Domains:     []string{"repo"}, // Repo-Agent 只检索 repo domain 知识
		}
		if knowledgeBlock, err := a.knowledge.Inject(ctx, injCtx); err == nil && knowledgeBlock != "" {
			systemPrompt += knowledgeBlock
		}
	}

	cfg := DefaultExecutorConfig()
	cfg.SystemPrompt = systemPrompt
	cfg.UserInput = input
	// 工具面：只读 + 调用图 + deepthinking + 委派 devops + 知识维护 + 便签回读。
	deps := depsFor("repo", a.params.Env, a.params.Guard, blackboard.BoardIDFrom(ctx))
	deps.Files = a.params.Files
	deps.Search = a.params.Search
	deps.Repo = a.params.RepoOps
	deps.Flow = a.params.Flow
	deps.Deep = a.params.DeepThinker
	deps.Delegate = a.devopsDelegate
	if a.mcpClient != nil {
		deps.Prune = tools.NewPruneHistoryTool(a.mcpClient, a.LLM).Execute
		deps.Consolidate = tools.NewConsolidateKnowledgeTool(a.mcpClient, a.LLM, "repo_agent", "repo_retrieval").Execute
	}
	cfg.Surface = surfaceFor(toolspec.ProfileRepo, deps, a.Name())
	cfg.LLM = a.LLM
	cfg.MaxSteps = a.maxSteps
	cfg.StepRetries = a.stepRetries
	cfg.Publisher = a.Publisher
	cfg.AgentName = a.Name()
	cfg.SystemAsHuman = true // RepoAgent uses Human role for its prompt
	// repo.prompt.md 要求模型以 agent_exit 收尾（handoff 是它的一部分）；
	// 不置这个标志时 agent_exit 只是"又执行了一次工具"，循环继续烧到 MaxSteps，
	// 而交付物变成后续那几轮的文本 —— 便签正文与 artifact 全文跟着全错。
	cfg.StopOnFinish = true

	// 上下文压缩配置（tool 结果截断）
	ec := a.ecCfg
	cfg.EnableContextCompression = ec.EnableContextCompression
	cfg.ContextCompressionThreshold = ec.ContextCompressionThreshold
	cfg.ToolResultKeepTokens = ec.ToolResultKeepTokens

	// 一次委派一个 store：RebuildPrompt 保留全部用户输入，本实例会被多次委派复用。
	store := thinklink.NewStore(0)
	store.AddUserInput(input, 0)
	cfg.UltimateThinkLink = store
	cfg.UltimateKeepPlans = ec.UltimateCompressionKeepPlans // 0=全部保留
	cfg.EnableUltimateCompression = ec.EnableUltimateCompression

	result, err := RunAgentLoop(ctx, cfg)
	if err != nil {
		return AgentResult{}, err
	}

	// Full 变体：Text 被程序化消费（下方 consolidation 提交 + Director 层
	// delegate_repo 结果 → RepoSummary 注入 coding/browser 系统提示），
	// 需恒为完整输出；LLM 上下文由 Director 层 FormatForDirector 走分级摘要。
	agentResult := FinalizeResultFull(a.Name(), input, result)

	// [NEW] Step 2: 异步提交记忆整理任务（非阻塞）
	if a.worker != nil && agentResult.Text != "" {
		a.worker.Submit(&ConsolidationTask{
			NewObservations: agentResult.Text,
		})
	}

	// [知识管理] 子任务完成后自动沉淀到知识库（非阻塞）
	if a.knowledge != nil {
		autoConsolidateSubtask(a.mcpClient, a.LLM, "repo_agent", "repo_retrieval", input, agentResult.Text)
	}

	return agentResult, nil
}
