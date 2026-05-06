package agents

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"

	"codeactor/internal/globalctx"
	"codeactor/internal/llm"
	"codeactor/internal/tools"
	"codeactor/pkg/messaging"
)

//go:embed impl_plan.prompt.md
var implPlanPrompt string

// ImplPlanAgent is a specialized agent that generates structured implementation
// plan documents for coding tasks. It is read-only and uses LLM + repo tools
// to analyze codebase context and produce design documents.
type ImplPlanAgent struct {
	BaseAgent
	GlobalCtx *globalctx.GlobalCtx
	Adapters  []*tools.Adapter
	maxSteps  int
}

// NewImplPlanAgent creates a new ImplPlanAgent with read-only repo tools
// and an agent_exit tool.
func NewImplPlanAgent(globalCtx *globalctx.GlobalCtx, llm llm.Engine, publisher *messaging.MessagePublisher, maxSteps int) *ImplPlanAgent {
	var toolDefs []ToolDefinition
	if err := json.Unmarshal(ToolsJSON, &toolDefs); err != nil {
		slog.Error("Failed to unmarshal tools", "error", err)
	}

	adapters := make([]*tools.Adapter, 0)

	// Map read-only repo tools
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
		default:
			continue
		}

		adapter := tools.NewAdapter(def.Name, def.Description, fn).WithSchema(def.Parameters)
		adapters = append(adapters, adapter)
	}

	// Add agent_exit tool
	for _, def := range toolDefs {
		if def.Name == "agent_exit" {
			adapter := tools.NewAdapter(def.Name, def.Description, globalCtx.FlowOps.ExecuteAgentExit).WithSchema(def.Parameters)
			adapters = append(adapters, adapter)
			break
		}
	}

	tools.SetGuardOnAdapters(adapters, globalCtx.Guard)

	return &ImplPlanAgent{
		BaseAgent: BaseAgent{
			LLM:       llm,
			Publisher: publisher,
		},
		GlobalCtx: globalCtx,
		Adapters:  adapters,
		maxSteps:  maxSteps,
	}
}

// Name returns the agent's display name.
func (a *ImplPlanAgent) Name() string {
	return "ImplPlan-Agent"
}

// Run executes the ImplPlanAgent: it formats the system prompt with environment
// context and optional caller-provided context, builds an ExecutorConfig,
// and runs the standard agent loop.
// It does NOT perform pre-investigation (context is provided by the caller).
func (a *ImplPlanAgent) Run(ctx context.Context, task string, contextInfo string) (string, error) {
	if a.GlobalCtx.ProjectPath == "" {
		return "", fmt.Errorf("project_dir is empty")
	}

	systemPrompt := a.GlobalCtx.FormatPrompt(implPlanPrompt)

	// Append caller-provided context if non-empty
	if contextInfo != "" {
		systemPrompt += "\n\n### Caller-Provided Context\n" + contextInfo
	}

	slog.Info("ImplPlanAgent starting", "project_dir", a.GlobalCtx.ProjectPath)

	cfg := ExecutorConfig{
		SystemPrompt: systemPrompt,
		UserInput:    task,
		Adapters:     a.Adapters,
		LLM:          a.LLM,
		MaxSteps:     a.maxSteps,
		Publisher:    a.Publisher,
		AgentName:    a.Name(),
		StopOnFinish: true, // Stop when agent_exit is called
	}
	return RunAgentLoop(ctx, cfg)
}
