package agents

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"

	"codeactor/internal/globalctx"
	"codeactor/internal/knowledge"
	"codeactor/internal/messaging"
	"codeactor/internal/tools"

	"codeactor/internal/llm"
)

//go:embed repo.prompt.md
var repoPrompt string

type RepoAgent struct {
	BaseAgent
	GlobalCtx *globalctx.GlobalCtx
	Adapters  []*tools.Adapter
	maxSteps  int

	// [NEW] 记忆系统（可选，nil 表示禁用）
	memStore *RepoMemoryStore
	worker   *ConsolidationWorker
}

func NewRepoAgent(globalCtx *globalctx.GlobalCtx, llm llm.Engine, publisher *messaging.MessagePublisher, maxSteps int) *RepoAgent {
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
	tools.SetGuardOnAdapters(adapters, globalCtx.Guard)

	// 注册知识整理/维护工具（需要 llm engine + CodeSeekMCP）
	knowledgeAdapters := createKnowledgeToolAdapters(globalCtx, llm, "repo_agent", "repo_retrieval")
	if len(knowledgeAdapters) > 0 {
		tools.SetGuardOnAdapters(knowledgeAdapters, globalCtx.Guard)
		adapters = append(adapters, knowledgeAdapters...)
	}

	return &RepoAgent{
		BaseAgent: BaseAgent{
			LLM:       llm,
			Publisher: publisher,
		},
		GlobalCtx: globalCtx,
		Adapters:  adapters,
		maxSteps:  maxSteps,
	}
}

func (a *RepoAgent) Name() string {
	return "Repo-Agent"
}

// SetMemory 注入记忆系统依赖。记忆系统可选，不设置时 Run() 行为与改造前一致。
func (a *RepoAgent) SetMemory(store *RepoMemoryStore, worker *ConsolidationWorker) {
	a.memStore = store
	a.worker = worker
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

	cfg := DefaultExecutorConfig()
	cfg.SystemPrompt = systemPrompt
	cfg.UserInput = input
	cfg.Adapters = a.Adapters
	cfg.LLM = a.LLM
	cfg.MaxSteps = a.maxSteps
	cfg.Publisher = a.Publisher
	cfg.AgentName = a.Name()
	cfg.SystemAsHuman = true // RepoAgent uses Human role for its prompt

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
	if a.GlobalCtx.KnowledgeInjector != nil {
		autoConsolidateSubtask(a.GlobalCtx, "repo_agent", "repo_retrieval", input, agentResult.Text)
	}

	return agentResult, nil
}
