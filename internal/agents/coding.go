package agents

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"

	"codeactor/internal/globalctx"
	"codeactor/internal/tools"

	"codeactor/internal/llm"
)

//go:embed coding.prompt.md
var codingPrompt string

type CodingAgent struct {
	BaseAgent
	GlobalCtx    *globalctx.GlobalCtx
	Adapters     []*tools.Adapter
	BrowserAgent *BrowserAgent
	maxSteps     int
	registry     *tools.Registry // 工具注册表引用
}

func NewCodingAgent(globalCtx *globalctx.GlobalCtx, llm llm.Engine, maxSteps int, browser *BrowserAgent) *CodingAgent {
	// 从 tools.json 加载工具定义
	var toolDefs []tools.ToolDefinition
	if err := json.Unmarshal(ToolsJSON, &toolDefs); err != nil {
		slog.Error("Failed to unmarshal coding tools", "error", err)
	}

	// 创建适配器（工具名 → 执行函数的映射）
	adapters := make([]*tools.Adapter, 0, len(toolDefs))
	for _, def := range toolDefs {
		fn := lookupToolFunc(def.Name, globalCtx)
		if fn == nil {
			// delegate_browser 需要特殊处理，跳过
			continue
		}
		adapter := tools.NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters)
		adapters = append(adapters, adapter)
	}

	// 特殊处理 delegate_browser（需要 browser agent 引用）
	if browser != nil {
		for _, def := range toolDefs {
			if def.Name == "delegate_browser" {
				browserFn := func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
					task, ok := params["task"].(string)
					if !ok || task == "" {
						return nil, fmt.Errorf("delegate_browser requires a non-empty 'task' parameter")
					}
					result, err := browser.Run(ctx, task)
					if err != nil {
						return nil, err
					}
					return result.Text, nil
				}
				adapter := tools.NewAdapter(def.Name, def.Description, browserFn).WithSchema(def.Parameters)
				adapters = append(adapters, adapter)
				break
			}
		}
	}

	tools.SetGuardOnAdapters(adapters, globalCtx.Guard)

	// 创建 Registry 并注册所有工具
	registry := tools.NewRegistry()
	for _, adapter := range adapters {
		registry.MustRegister(adapter)
	}

	return &CodingAgent{
		BaseAgent: BaseAgent{
			LLM:       llm,
			Publisher: globalCtx.Publisher,
		},
		Adapters:     adapters,
		maxSteps:     maxSteps,
		BrowserAgent: browser,
		GlobalCtx:    globalCtx,
		registry:     registry,
	}
}

// lookupToolFunc 根据工具名查找执行函数（替代外部 switch-case 硬编码）
// 返回 nil 表示该工具需要特殊处理（如 delegate_browser）
func lookupToolFunc(name string, gctx *globalctx.GlobalCtx) tools.ToolFunc {
	switch name {
	case "read_file":
		return gctx.FileOps.ExecuteReadFile
	case "search_replace_in_file":
		return gctx.ReplaceTool.ExecuteReplaceBlock
	case "create_file":
		return gctx.FileOps.ExecuteCreateFile
	case "run_bash":
		return gctx.SysOps.ExecuteRunBash
	case "search_by_regex":
		return gctx.SearchOps.ExecuteGrepSearch
	case "delete_file":
		return gctx.FileOps.ExecuteDeleteFile
	case "rename_file":
		return gctx.FileOps.ExecuteRenameFile
	case "list_dir":
		return gctx.FileOps.ExecuteListDir
	case "print_dir_tree":
		return gctx.FileOps.ExecutePrintDirTree
	case "semantic_search":
		return gctx.RepoOps.ExecuteSemanticSearch
	case "query_code_skeleton":
		return gctx.RepoOps.ExecuteQueryCodeSkeleton
	case "query_code_snippet":
		return gctx.RepoOps.ExecuteQueryCodeSnippet
	case "find_function_callee":
		return gctx.RepoOps.ExecuteFindFunctionCallees
	case "find_function_caller":
		return gctx.RepoOps.ExecuteFindFunctionCallers
	case "thinking":
		return func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			inputBytes, _ := json.Marshal(params)
			return gctx.ThinkingTool.Call(ctx, string(inputBytes))
		}
	case "micro_agent":
		return gctx.MicroAgentTool.Execute
	case "deepthinking":
		return gctx.DeepThinkingTool.Execute
	case "agent_exit":
		return gctx.FlowOps.ExecuteAgentExit
	case "ask_user_for_help":
		return gctx.FlowOps.ExecuteAskUserForHelp
	// delegate_browser 需要 browser agent 引用，返回 nil 由调用方特殊处理
	default:
		return nil
	}
}

func (a *CodingAgent) Name() string {
	return "Coding-Agent"
}

func (a *CodingAgent) Run(ctx context.Context, input string) (AgentResult, error) {
	systemPrompt := a.GlobalCtx.FormatPrompt(codingPrompt)

	cfg := DefaultExecutorConfig()
	cfg.SystemPrompt = systemPrompt
	cfg.UserInput = input
	cfg.Adapters = a.Adapters
	cfg.LLM = a.LLM
	cfg.MaxSteps = a.maxSteps
	cfg.Publisher = a.Publisher
	cfg.AgentName = a.Name()
	cfg.StopOnFinish = true
	cfg.RepoContext = a.GlobalCtx.RepoSummary
	// EnableCollaboration 已默认 true
	result, err := RunAgentLoop(ctx, cfg)
	if err != nil {
		return AgentResult{}, err
	}
	return AgentResult{
		Text:   result.Text,
		Memory: ConvertLLMHistoryToMemory(result.History),
	}, nil
}

// Registry 返回工具注册表引用（供外部访问）
func (a *CodingAgent) Registry() *tools.Registry {
	return a.registry
}
