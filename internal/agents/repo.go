package agents

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"

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
	promptFmt PromptFormatter
	env       Env
	knowledge KnowledgeProvider
	mcpClient *mcp.MCPClient
	ecCfg     config.EnhancedCommanderConfig
	Adapters  []*tools.Adapter
	maxSteps  int

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

	LLM      llm.Engine
	MaxSteps int
}

// NewRepoAgent 构造 RepoAgent：先填充结构体字段，再装配工具适配器
// （tools.json 常规工具 + delegate_devops + 知识维护工具）。
func NewRepoAgent(p RepoAgentParams) *RepoAgent {
	self := &RepoAgent{
		BaseAgent: BaseAgent{
			LLM:       p.LLM,
			Publisher: p.Publisher,
		},
		promptFmt: p.Formatter,
		env:       p.Env,
		knowledge: p.Knowledge,
		mcpClient: p.MCPClient,
		ecCfg:     p.EnhancedCommanderCfg,
		maxSteps:  p.MaxSteps,
	}
	self.Adapters = self.buildAdapters(p)
	return self
}

// buildAdapters 装配 Repo-Agent 的工具适配器列表。
// delegate_devops 闭包在运行时读取 a.devops（由 SetDevOpsAgent 注入，晚于构造）。
func (a *RepoAgent) buildAdapters(p RepoAgentParams) []*tools.Adapter {
	var toolDefs []tools.ToolDefinition
	if err := json.Unmarshal(ToolsJSON, &toolDefs); err != nil {
		slog.Error("Failed to unmarshal tools", "error", err)
	}

	adapters := make([]*tools.Adapter, 0, len(toolDefs)+1)
	for _, def := range toolDefs {
		var fn tools.ToolFunc
		switch def.Name {
		case "read_file":
			fn = p.Files.ExecuteReadFile
		case "search_by_regex":
			fn = p.Search.ExecuteGrepSearch
		case "list_dir":
			fn = p.Files.ExecuteListDir
		case "print_dir_tree":
			fn = p.Files.ExecutePrintDirTree
		case "semantic_search":
			fn = p.RepoOps.ExecuteSemanticSearch
		case "query_code_skeleton":
			fn = p.RepoOps.ExecuteQueryCodeSkeleton
		case "query_code_snippet":
			fn = p.RepoOps.ExecuteQueryCodeSnippet
		case "find_function_callee":
			fn = p.RepoOps.ExecuteFindFunctionCallees
		case "find_function_caller":
			fn = p.RepoOps.ExecuteFindFunctionCallers
		case "query_call_graph":
			fn = p.RepoOps.ExecuteCallGraph
		case "deepthinking":
			fn = p.DeepThinker.Execute
		case "ask_user_for_help":
			if p.Env.FullYoloMode() {
				continue
			}
			fn = p.Flow.ExecuteAskUserForHelp
		default:
			continue
		}

		adapter := tools.NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters).WithReadOnlyIfKnown()
		adapters = append(adapters, adapter)
	}

	// delegate_devops：将运维类任务委托给 DevOps-Agent（与 Director 中同名工具保持一致的名称与参数 schema）
	delegateDevOps := tools.NewAdapter("delegate_devops", "Delegate operational and system administration tasks to DevOps-Agent. DevOps-Agent can run shell commands, inspect files, check logs, manage processes, and perform any non-coding infrastructure work. Use this for tasks like checking disk usage, finding files, running diagnostics, inspecting configurations, or executing ad-hoc shell commands.", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		if a.devops == nil {
			return "[DevOps-Agent unavailable] DevOps-Agent has not been wired to Repo-Agent. Complete the analysis with the information you have and note that the operational task could not be delegated.", nil
		}
		result, err := a.devops.Run(ctx, task)
		if err != nil {
			// 友好地把错误信息返回给 LLM，而不是中断执行循环
			return fmt.Sprintf("[DevOps-Agent execution failed] task: %q, error: %v. You may retry with a clearer task description, or note the failure in your analysis result.", task, err), nil
		}
		return result.Text, nil
	}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"task": map[string]interface{}{"type": "string", "description": "The operational task for DevOps-Agent, e.g., 'check disk usage', 'find all log files modified today', 'check if port 8080 is in use'."},
		},
		"required": []string{"task"},
	})
	adapters = append(adapters, delegateDevOps)
	tools.SetGuardOnAdapters(adapters, p.Guard)

	// 注册知识整理/维护工具（需要 llm engine + CodeSeekMCP）
	knowledgeAdapters := createKnowledgeToolAdapters(p.MCPClient, p.LLM, "repo_agent", "repo_retrieval")
	if len(knowledgeAdapters) > 0 {
		tools.SetGuardOnAdapters(knowledgeAdapters, p.Guard)
		adapters = append(adapters, knowledgeAdapters...)
	}

	return adapters
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
	cfg.Adapters = a.Adapters
	cfg.LLM = a.LLM
	cfg.MaxSteps = a.maxSteps
	cfg.Publisher = a.Publisher
	cfg.AgentName = a.Name()
	cfg.SystemAsHuman = true // RepoAgent uses Human role for its prompt

	// 上下文压缩配置（tool 结果截断）
	ec := a.ecCfg
	cfg.EnableContextCompression = ec.Enable && ec.EnableContextCompression
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
