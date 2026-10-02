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

// NewRepoAgent 构造 RepoAgent，仅声明其所需窄依赖：
// prompt 格式化、事件总线、环境视图（ProjectPath/FullYoloMode）、
// 文件/搜索/仓库理解工具集、流程控制、深度思考、工作区守护、
// 知识注入器、MCP 客户端与上下文压缩配置。
func NewRepoAgent(formatter PromptFormatter, publisher EventBus, env Env, files FileToolSet, search SearchToolSet, repoOps RepoToolSet, flow FlowToolSet, deepThinker DeepThinker, guard *tools.WorkspaceGuard, knowledge KnowledgeProvider, mcpClient *mcp.MCPClient, ecCfg config.EnhancedCommanderConfig, llm llm.Engine, maxSteps int) *RepoAgent {
	// self-reference for the delegate closure that needs the RepoAgent after
	// construction (same pattern as NewDirectorAgent).
	var self *RepoAgent

	var toolDefs []tools.ToolDefinition
	if err := json.Unmarshal(ToolsJSON, &toolDefs); err != nil {
		slog.Error("Failed to unmarshal tools", "error", err)
	}

	adapters := make([]*tools.Adapter, 0)
	for _, def := range toolDefs {
		var fn tools.ToolFunc
		switch def.Name {
		case "read_file":
			fn = files.ExecuteReadFile
		case "search_by_regex":
			fn = search.ExecuteGrepSearch
		case "list_dir":
			fn = files.ExecuteListDir
		case "print_dir_tree":
			fn = files.ExecutePrintDirTree
		case "semantic_search":
			fn = repoOps.ExecuteSemanticSearch
		case "query_code_skeleton":
			fn = repoOps.ExecuteQueryCodeSkeleton
		case "query_code_snippet":
			fn = repoOps.ExecuteQueryCodeSnippet
		case "find_function_callee":
			fn = repoOps.ExecuteFindFunctionCallees
		case "find_function_caller":
			fn = repoOps.ExecuteFindFunctionCallers
		case "query_call_graph":
			fn = repoOps.ExecuteCallGraph
		case "deepthinking":
			fn = deepThinker.Execute
		case "ask_user_for_help":
			if env.FullYoloMode() {
				continue
			}
			fn = flow.ExecuteAskUserForHelp
		default:
			continue
		}

		adapter := tools.NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters)
		adapters = append(adapters, adapter)
	}
	// delegate_devops：将运维类任务委托给 DevOps-Agent（与 Director 中同名工具保持一致的名称与参数 schema）
	delegateDevOps := tools.NewAdapter("delegate_devops", "Delegate operational and system administration tasks to DevOps-Agent. DevOps-Agent can run shell commands, inspect files, check logs, manage processes, and perform any non-coding infrastructure work. Use this for tasks like checking disk usage, finding files, running diagnostics, inspecting configurations, or executing ad-hoc shell commands.", func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		task, ok := params["task"].(string)
		if !ok {
			return nil, fmt.Errorf("task parameter required")
		}
		if self == nil || self.devops == nil {
			return "[DevOps-Agent unavailable] DevOps-Agent has not been wired to Repo-Agent. Complete the analysis with the information you have and note that the operational task could not be delegated.", nil
		}
		result, err := self.devops.Run(ctx, task)
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
	tools.SetGuardOnAdapters(adapters, guard)

	// 注册知识整理/维护工具（需要 llm engine + CodeSeekMCP）
	knowledgeAdapters := createKnowledgeToolAdapters(mcpClient, llm, "repo_agent", "repo_retrieval")
	if len(knowledgeAdapters) > 0 {
		tools.SetGuardOnAdapters(knowledgeAdapters, guard)
		adapters = append(adapters, knowledgeAdapters...)
	}

	self = &RepoAgent{
		BaseAgent: BaseAgent{
			LLM:       llm,
			Publisher: publisher,
		},
		promptFmt: formatter,
		env:       env,
		knowledge: knowledge,
		mcpClient: mcpClient,
		ecCfg:     ecCfg,
		Adapters:  adapters,
		maxSteps:  maxSteps,
	}
	return self
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

	result, err := RunAgentLoop(ctx, cfg)
	if err != nil {
		return AgentResult{}, err
	}

	agentResult := AgentResult{
		Text:   result.Text,
		Memory: ConvertLLMHistoryToMemory(result.History),
	}

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
