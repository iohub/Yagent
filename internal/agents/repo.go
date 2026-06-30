package agents

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"

	"codeactor/internal/tools"
	"codeactor/internal/globalctx"
	"codeactor/internal/messaging"

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
		case "deepthinking":
			fn = globalCtx.DeepThinkingTool.Execute
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

	// [NEW] Step 1: 从缓存加载记忆并注入 system prompt
	if a.memStore != nil {
		memContent := a.memStore.Get()
		if injection := RenderMemoryForInjection(memContent); injection != "" {
			systemPrompt += injection
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

	return agentResult, nil
}
