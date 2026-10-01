package agents

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"

	"yagent/internal/globalctx"
	"yagent/internal/knowledge"
	"yagent/internal/messaging"
	"yagent/internal/tools"

	"yagent/internal/config"
	"yagent/internal/llm"
)

//go:embed repo.prompt.md
var repoPrompt string

type RepoAgent struct {
	BaseAgent
	GlobalCtx *globalctx.GlobalCtx
	Adapters  []*tools.Adapter
	maxSteps  int

	// DevOps-Agent（可选，nil 表示未注入，delegate_devops 将返回友好提示）
	devops *DevOpsAgent

	// [NEW] 记忆系统（可选，nil 表示禁用）
	memStore *RepoMemoryStore
	worker   *ConsolidationWorker

	// timeouts 统一超时配置（P0 Step 10：构造时 Normalize，ToolTimeout 接线用）
	timeouts config.TimeoutsConfig
}

func NewRepoAgent(globalCtx *globalctx.GlobalCtx, llm llm.Engine, publisher *messaging.MessagePublisher, maxSteps int, timeouts config.TimeoutsConfig) *RepoAgent {
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
			fn = globalCtx.FileOps.ExecuteReadFile
		case "search_by_regex":
			fn = globalCtx.SearchOps.ExecuteGrepSearch
		case "list_dir":
			fn = globalCtx.FileOps.ExecuteListDir
		case "print_dir_tree":
			fn = globalCtx.FileOps.ExecutePrintDirTree
		case "semantic_search":
			fn = globalCtx.RepoOps.ExecuteSemanticSearch
		case "query_code_skeleton":
			fn = globalCtx.RepoOps.ExecuteQueryCodeSkeleton
		case "query_code_snippet":
			fn = globalCtx.RepoOps.ExecuteQueryCodeSnippet
		case "find_function_callee":
			fn = globalCtx.RepoOps.ExecuteFindFunctionCallees
		case "find_function_caller":
			fn = globalCtx.RepoOps.ExecuteFindFunctionCallers
		case "query_call_graph":
			fn = globalCtx.RepoOps.ExecuteCallGraph
		case "deepthinking":
			fn = globalCtx.DeepThinkingTool.Execute
		case "ask_user_for_help":
			if globalCtx.FullYoloMode {
				continue
			}
			fn = globalCtx.FlowOps.ExecuteAskUserForHelp
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
	tools.SetGuardOnAdapters(adapters, globalCtx.Guard)

	// 注册知识整理/维护工具（需要 llm engine + CodeSeekMCP）
	knowledgeAdapters := createKnowledgeToolAdapters(globalCtx, llm, "repo_agent", "repo_retrieval")
	if len(knowledgeAdapters) > 0 {
		tools.SetGuardOnAdapters(knowledgeAdapters, globalCtx.Guard)
		adapters = append(adapters, knowledgeAdapters...)
	}

	self = &RepoAgent{
		BaseAgent: BaseAgent{
			LLM:       llm,
			Publisher: publisher,
		},
		GlobalCtx: globalCtx,
		Adapters:  adapters,
		maxSteps:  maxSteps,
		timeouts:  timeouts.Normalize(), // P0 Step 10：零值回退统一默认值（幂等）
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

	if a.GlobalCtx.ProjectPath == "" {
		return AgentResult{}, fmt.Errorf("project_dir is empty")
	}

	systemPrompt = a.GlobalCtx.FormatPrompt(systemPrompt)

	if a.memStore != nil {
		memContent := a.memStore.Get()
		if injection := RenderMemoryForInjection(memContent); injection != "" {
			systemPrompt += injection
		}
	}

	// [知识管理] 对话前动态知识检索注入
	if a.GlobalCtx.KnowledgeInjector != nil {
		injCtx := knowledge.InjectionContext{
			UserMessage: input,
			TargetFiles: nil,
			AgentName:   a.Name(),
			Domains:     []string{"repo"}, // Repo-Agent 只检索 repo domain 知识
		}
		if knowledgeBlock, err := a.GlobalCtx.KnowledgeInjector.Inject(ctx, injCtx); err == nil && knowledgeBlock != "" {
			systemPrompt += knowledgeBlock
		}
	}

	// P0 Step 4：迁移至 runSubAgentLoop（统一内核 director.Planner）。
	// LLMTimeout 零值透传：→ runSubAgentLoop 内兜底 5min（等价 executor.go:118）；
	// P0 Step 10：ToolTimeout 接入统一配置 a.timeouts.SubAgentTool（构造时
	// Normalize 非零）；StopOnFinish=false 保持现状。
	ec := a.GlobalCtx.EnhancedCommander
	outcome, err := runSubAgentLoop(ctx, SubAgentLoopConfig{
		SystemPrompt:       systemPrompt,
		UserInput:          input,
		Adapters:           a.Adapters,
		LLM:                a.LLM,
		MaxSteps:           a.maxSteps,
		Publisher:          a.Publisher,
		AgentName:          a.Name(),
		SystemAsHuman:      true, // RepoAgent uses Human role for its prompt
		ToolTimeout:        a.timeouts.SubAgentTool,
		CompressEnable:     ec.Enable && ec.EnableContextCompression,
		CompressThreshold:  ec.ContextCompressionThreshold,
		CompressKeepTokens: ec.ToolResultKeepTokens,
	})
	if err != nil {
		return AgentResult{}, err
	}

	agentResult := AgentResult{
		Text:   outcome.Text,
		Memory: ConvertLLMHistoryToMemory(outcome.History),
	}

	// [NEW] Step 2: 异步提交记忆整理任务（非阻塞）
	if a.worker != nil && agentResult.Text != "" {
		a.worker.Submit(&ConsolidationTask{
			NewObservations: agentResult.Text,
		})
	}

	// [知识管理] 子任务完成后自动沉淀到知识库（非阻塞）
	if a.GlobalCtx.KnowledgeInjector != nil {
		autoConsolidateSubtask(a.GlobalCtx, a.LLM, "repo_agent", "repo_retrieval", input, agentResult.Text)
	}

	return agentResult, nil
}
