package agents

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"

	"codeactor/internal/compact"
	"codeactor/internal/globalctx"
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

	// compactConfig 上下文压缩配置（nil=不启用压缩）
	compactConfig *compact.Config
	// compactEngine 懒加载创建的压缩引擎实例（仅在首次 Run 时创建）
	compactEngine *compact.Engine
}

func NewRepoAgent(globalCtx *globalctx.GlobalCtx, llm llm.Engine, publisher *messaging.MessagePublisher, maxSteps int, compactCfg *compact.Config) *RepoAgent {
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
			fn = globalCtx.FlowOps.ExecuteAskUserForHelp
		default:
			continue
		}

		adapter := tools.NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters)
		adapters = append(adapters, adapter)
	}
	tools.SetGuardOnAdapters(adapters, globalCtx.Guard)

	return &RepoAgent{
		BaseAgent: BaseAgent{
			LLM:       llm,
			Publisher: publisher,
		},
		GlobalCtx:     globalCtx,
		Adapters:      adapters,
		maxSteps:      maxSteps,
		compactConfig: compactCfg,
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

	// Inject shared memory (3 dimensions: user, feedback, reference)
	systemPrompt = a.InjectSharedMemory(systemPrompt, "default", a.GlobalCtx.ProjectPath)

	if a.memStore != nil {
		memContent := a.memStore.Get()
		if injection := RenderMemoryForInjection(memContent); injection != "" {
			systemPrompt += injection
		}
	}

	// ─── 懒加载初始化上下文压缩引擎（仅首次 Run 时创建，后续复用）───
	if a.compactConfig != nil && a.compactConfig.EnableAutoCompact && a.compactEngine == nil && a.LLM != nil {
		engine, err := compact.NewEngine(a.compactConfig, &compact.SummaryAdapter{
			LLM:         a.LLM,
			Temperature: 0.1,
			MaxTokens:   12000,
		})
		if err != nil {
			slog.Warn("Failed to create compact engine for RepoAgent", "error", err)
		} else {
			a.compactEngine = engine
			slog.Info("Context compact engine initialized for RepoAgent",
				"max_tokens", a.compactConfig.MaxContextTokens)
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
	cfg.CompactEngine = a.compactEngine

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

	return agentResult, nil
}
