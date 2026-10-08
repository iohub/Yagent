package agents

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"yagent/internal/config"
	"yagent/internal/knowledge"
	"yagent/internal/mcp"
	"yagent/internal/thinklink"
	"yagent/internal/tools"

	"yagent/internal/llm"
)

//go:embed coding.prompt.md
var codingPrompt string

// backtick 是反引号字符，用于在 Go 字符串中嵌入代码标记
const backtick = "`"

// gitCheckpointPromptSection 是 Git Checkpoint 章节的提示词内容
// 仅在项目是 git 仓库且配置启用时才会追加到 system prompt 中
const gitCheckpointPromptSection = "### Git Checkpoint Mechanism\n" +
	"The agent has a built-in Git Checkpoint system that:\n" +
	"1. Creates a separate " + backtick + "agent/" + backtick + " branch for isolated coding work\n" +
	"2. Stashes dirty worktree before starting (restored on the agent branch)\n" +
	"3. **You decide when to create checkpoints** using " + backtick + "git_checkpoint_create" + backtick + "\n" +
	"4. Performs a squash merge at the end with a professional Conventional Commits message\n" +
	"\n" +
	"#### When to Create Checkpoints\n" +
	"You are responsible for deciding when checkpoints are needed. **Be strategic — create checkpoints at meaningful moments, not after every step.**\n" +
	"\n" +
	"**Create a checkpoint BEFORE:**\n" +
	"- Major refactoring (restructuring modules, changing interfaces, renaming widely-used symbols)\n" +
	"- Risky or destructive operations (deleting files, rewriting large sections, changing build configs)\n" +
	"- Complex experiments where the approach is uncertain and you might need to backtrack\n" +
	"- Modifying critical infrastructure (authentication, database schemas, CI pipelines, shared utilities)\n" +
	"\n" +
	"**Create a checkpoint AFTER:**\n" +
	"- Completing a significant feature or module (provides a known-good state to return to)\n" +
	"- Successfully resolving a tricky bug (preserves the fix before moving on)\n" +
	"- Any milestone you wouldn't want to redo from scratch\n" +
	"\n" +
	"**When NOT to create a checkpoint:**\n" +
	"- After trivial changes (formatting, typo fixes, minor adjustments)\n" +
	"- After every single step (creates noise, wastes tag space)\n" +
	"- When you're confident the change is small and easily reproducible\n" +
	"\n" +
	"#### Checkpoint Tools\n" +
	"- " + backtick + "git_checkpoint_create" + backtick + " — Create a checkpoint at the current state. **Always provide a descriptive " + backtick + "message" + backtick + "** explaining what milestone this represents.\n" +
	"  Example: " + backtick + "git_checkpoint_create(message=\"before refactoring auth middleware\")" + backtick + "\n" +
	"- " + backtick + "git_checkpoint_list" + backtick + " — List all available checkpoints (use before rollback to find the right target)\n" +
	"- " + backtick + "git_checkpoint_rollback" + backtick + " — Roll back to a specific checkpoint if something goes wrong\n" +
	"\n" +
	"#### Rollback Workflow\n" +
	"If a change produces unexpected results:\n" +
	"1. Use " + backtick + "git_checkpoint_list" + backtick + " to see available checkpoints\n" +
	"2. Use " + backtick + "git_checkpoint_rollback" + backtick + " with the tag name to return to a known-good state\n" +
	"3. Attempt a different approach\n" +
	"\n" +
	"**Remember:** It is better to create a checkpoint you don't need than to need one you didn't create. When in doubt, checkpoint."

type CodingAgent struct {
	BaseAgent
	promptFmt        PromptFormatter
	env              Env
	microAgent       MicroAgentRunner
	repoCtx          RepoContextStore
	knowledge        KnowledgeProvider
	mcpClient        *mcp.MCPClient
	gitCheckpointCfg *config.GitCheckpointConfig
	ecCfg            config.EnhancedCommanderConfig
	guard            *tools.WorkspaceGuard
	Adapters         []*tools.Adapter
	BrowserAgent     *BrowserAgent
	maxSteps         int
	registry         *tools.Registry // 工具注册表引用
	thinkLink        *thinklink.Store // 终极压缩支撑存储，实时记录用户原始输入，与 Director 模式一致，与 agent 同生命周期

}

// NewCodingAgent 构造 CodingAgent，仅声明其所需窄依赖：
// prompt 格式化、事件总线、环境视图（ProjectPath/FullYoloMode）、
// 完整工具集（文件/编辑/搜索/系统/仓库理解/流程/思考/深度思考/微代理）、
// 工作区守护、仓库上下文存储（读取）、知识注入器、MCP 客户端、
// Git Checkpoint 配置与上下文压缩配置。
func NewCodingAgent(formatter PromptFormatter, publisher EventBus, env Env, files FileToolSet, search SearchToolSet, sys SysToolSet, edit EditToolSet, repoOps RepoToolSet, flow FlowToolSet, thinker Thinker, deepThinker DeepThinker, microAgent MicroAgentRunner, guard *tools.WorkspaceGuard, repoCtx RepoContextStore, knowledge KnowledgeProvider, mcpClient *mcp.MCPClient, gitCheckpointCfg *config.GitCheckpointConfig, ecCfg config.EnhancedCommanderConfig, llm llm.Engine, maxSteps int, browser *BrowserAgent) *CodingAgent {
	// 从 tools.json 加载工具定义
	var toolDefs []tools.ToolDefinition
	if err := json.Unmarshal(ToolsJSON, &toolDefs); err != nil {
		slog.Error("Failed to unmarshal coding tools", "error", err)
	}

	// thinklink 存储提前创建：TodoWrite 工具注册需要引用（闭包持有 store 与
	// 事件发布器）；与 return 值为同一实例（跨任务累积的条目必须被
	// 终极压缩重建看到），与 Agent 同生命周期。
	codingThinkLink := thinklink.NewStore(0)

	// 创建适配器（工具名 → 执行函数的映射）
	adapters := make([]*tools.Adapter, 0, len(toolDefs))
	for _, def := range toolDefs {
		fn := lookupToolFunc(def.Name, env, files, search, sys, edit, repoOps, flow, thinker, deepThinker, microAgent, codingThinkLink, publisher)
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

	tools.SetGuardOnAdapters(adapters, guard)

	// 注册知识整理/维护工具（需要 llm engine + CodeSeekMCP，不来自 tools.json 自动加载）
	knowledgeAdapters := createKnowledgeToolAdapters(mcpClient, llm, "coding_agent", "coding_modification")
	if len(knowledgeAdapters) > 0 {
		tools.SetGuardOnAdapters(knowledgeAdapters, guard)
		adapters = append(adapters, knowledgeAdapters...)
	}

	// 创建 Registry 并注册所有工具
	registry := tools.NewRegistry()
	for _, adapter := range adapters {
		registry.MustRegister(adapter)
	}

	return &CodingAgent{
		BaseAgent: BaseAgent{
			LLM:       llm,
			Publisher: publisher,
		},
		promptFmt:        formatter,
		env:              env,
		microAgent:       microAgent,
		repoCtx:          repoCtx,
		knowledge:        knowledge,
		mcpClient:        mcpClient,
		gitCheckpointCfg: gitCheckpointCfg,
		ecCfg:            ecCfg,
		guard:            guard,
		Adapters:         adapters,
		maxSteps:         maxSteps,
		BrowserAgent:     browser,
		registry:         registry,
		thinkLink:        codingThinkLink, // 容量上限 0 = 使用包默认值（200 条），与 Director 一致
	}
}

// lookupToolFunc 根据工具名查找执行函数（替代外部 switch-case 硬编码）
// 返回 nil 表示该工具需要特殊处理（如 delegate_browser）
func lookupToolFunc(name string, env Env, files FileToolSet, search SearchToolSet, sys SysToolSet, edit EditToolSet, repoOps RepoToolSet, flow FlowToolSet, thinker Thinker, deepThinker DeepThinker, microAgent MicroAgentRunner, todoStore *thinklink.Store, publisher EventBus) tools.ToolFunc {
	switch name {
	case "read_file":
		return files.ExecuteReadFile
	case "search_replace_in_file":
		return edit.ExecuteReplaceBlock
	case "create_file":
		return files.ExecuteCreateFile
	case "run_bash":
		return sys.ExecuteRunBash
	case "search_by_regex":
		return search.ExecuteGrepSearch
	case "delete_file":
		return files.ExecuteDeleteFile
	case "rename_file":
		return files.ExecuteRenameFile
	case "list_dir":
		return files.ExecuteListDir
	case "print_dir_tree":
		return files.ExecutePrintDirTree
	case "semantic_search":
		return repoOps.ExecuteSemanticSearch
	case "query_code_skeleton":
		return repoOps.ExecuteQueryCodeSkeleton
	case "query_code_snippet":
		return repoOps.ExecuteQueryCodeSnippet
	case "find_function_callee":
		return repoOps.ExecuteFindFunctionCallees
	case "find_function_caller":
		return repoOps.ExecuteFindFunctionCallers
	case "micro_agent":
		return microAgent.Execute
	case "deepthinking":
		return deepThinker.Execute
	case "agent_exit":
		return flow.ExecuteAgentExit
	case "ask_user_for_help":
		if env.FullYoloMode() {
			return nil
		}
		return flow.ExecuteAskUserForHelp
	case "TodoWrite":
		// TodoWrite 任务清单工具：闭包持有 thinklink 存储与事件发布器
		//（schema 由调用方从 tools.json 统一提供，见 NewCodingAgent 注册循环）
		return TodoWriteToolFunc(todoStore, publisher)
	// delegate_browser 需要 browser agent 引用，返回 nil 由调用方特殊处理
	default:
		return nil
	}
}

func (a *CodingAgent) Name() string {
	return "Coding-Agent"
}

func (a *CodingAgent) Run(ctx context.Context, input string) (AgentResult, error) {
	// === Git Checkpoint Integration ===

	// 先检查项目是否是 git 仓库
	isGitRepo := IsGitRepository(a.env.ProjectPath())
	gitCheckpointEnabled := isGitRepo && a.gitCheckpointCfg != nil && a.gitCheckpointCfg.Enabled

	var gcm *GitCheckpointManager
	if gitCheckpointEnabled {
		gitCfg := ConvertConfig(a.gitCheckpointCfg)
		gcm = NewGitCheckpointManager(
			gitCfg,
			a.env.ProjectPath(),
			input,
			func(ctx context.Context, diff string, taskSummary string) (string, error) {
				systemPrompt := `You are an expert software engineer writing a Git commit message following the Conventional Commits specification.

Analyze the code changes below and the task context to generate a comprehensive, professional commit message.

## Format

<type>(<scope>): <summary>

<body>

[BREAKING CHANGE: <details>]

## Rules

1. **Type** (required): Choose the most appropriate:
   - feat: A new feature for the user
   - fix: A bug fix
   - refactor: Code restructuring without changing external behavior
   - perf: Performance improvement
   - docs: Documentation only changes
   - style: Formatting, whitespace, etc. (no code meaning change)
   - test: Adding or correcting tests
   - build: Build system or external dependency changes
   - ci: CI configuration changes
   - chore: Maintenance tasks, no production code change

2. **Scope** (optional but recommended): The module or package most affected (e.g., auth, api, parser, config)

3. **Summary** (required):
   - Imperative mood: "add feature" not "added feature"
   - Lowercase first letter
   - No period at end
   - Maximum 72 characters
   - Be specific, not generic

4. **Body** (required):
   - Separate from summary with blank line
   - Explain WHAT changed and WHY, not HOW
   - If multiple logical changes exist, organize with bullet points
   - Wrap at 100 characters

5. **Breaking Change** (only if applicable):
   - Include "BREAKING CHANGE:" in the footer section
   - Describe what breaks and the migration path

CRITICAL: Do NOT mention AI, agents, automation, or tools. Write as if a human engineer made these changes.

Output ONLY the commit message text. No explanations, no markdown fences, no commentary.`

				task := fmt.Sprintf("Task: %s\n\nDiff:\n%s", taskSummary, diff)

				result, err := a.microAgent.Execute(ctx, map[string]interface{}{
					"system_prompt": systemPrompt,
					"task":          task,
				})
				if err != nil {
					return "", fmt.Errorf("micro agent commit message generation failed: %w", err)
				}

				resultStr, ok := result.(string)
				if !ok {
					return "", fmt.Errorf("micro agent returned non-string result")
				}

				msg := strings.TrimSpace(resultStr)

				// 清理 markdown 代码围栏和常见前缀
				if strings.HasPrefix(msg, "```") {
					closeIdx := strings.Index(msg[3:], "```")
					if closeIdx != -1 {
						content := msg[closeIdx+6:]
						if idx := strings.Index(content, "\n"); idx != -1 {
							content = content[idx+1:]
						}
						content = strings.TrimSuffix(content, "```")
						msg = strings.TrimSpace(content)
					}
				} else {
					msg = strings.TrimSuffix(msg, "```")
					msg = strings.TrimPrefix(msg, "text")
				}

				// 移除残留的 markdown 格式
				msg = strings.ReplaceAll(msg, "**", "")
				msg = strings.ReplaceAll(msg, "*", "")
				msg = strings.ReplaceAll(msg, "`", "")
				msg = strings.TrimSpace(msg)

				if msg == "" {
					return "", fmt.Errorf("commit message is empty after generation")
				}
				return msg, nil
			},
		)
	}
	// === END ===

	// === thinklink 终极压缩记录 ===
	a.thinkLink.AddUserInput(input, 0) // 记录用户原始输入（自带去重）

	systemPrompt := a.promptFmt.FormatPrompt(codingPrompt)

	// 如果是 git 仓库且 checkpoint 启用，追加 Git Checkpoint 章节到提示词
	if gitCheckpointEnabled {
		systemPrompt += "\n" + gitCheckpointPromptSection
	}

	// [知识管理] 对话前动态知识检索注入（TargetFiles 留 nil，由 Injector 从 UserMessage 中提取）
	if a.knowledge != nil {
		injCtx := knowledge.InjectionContext{
			UserMessage: input,
			TargetFiles: nil,
			AgentName:   a.Name(),
			Domains:     []string{"repo", "coding"}, // Coding-Agent 检索 repo + coding domain 知识
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
	cfg.StopOnFinish = true
	// NoActionRetryLimit: Allow up to 2 retries when LLM outputs text-only without tool calls to prevent premature termination on plan-only responses
	cfg.NoActionRetryLimit = 2
	cfg.RepoContext = a.repoCtx.Get()

	// 上下文压缩配置（tool 结果截断）
	ec := a.ecCfg
	cfg.EnableContextCompression = ec.Enable && ec.EnableContextCompression
	cfg.ContextCompressionThreshold = ec.ContextCompressionThreshold
	cfg.ToolResultKeepTokens = ec.ToolResultKeepTokens

	// 三级终极压缩配置
	cfg.UltimateThinkLink = a.thinkLink // 三级终极压缩重建源（thinklink 只读窄接口）
	cfg.UltimateKeepPlans = 0 // 0=全部保留

	// 如果是 git 仓库且 checkpoint 启用，设置回调和添加工具
	if gitCheckpointEnabled {
		cfg.OnAgentStart = func(ctx context.Context) error {
			return gcm.OnAgentStart(ctx)
		}
		cfg.OnAgentExit = func(ctx context.Context, agentErr error) error {
			return gcm.OnAgentExit(ctx, agentErr)
		}
		cfg.OnStepEnd = func(ctx context.Context, stepInfo StepInfo) error {
			return gcm.OnStepEnd(ctx, stepInfo)
		}

		// Add checkpoint tools to the adapter list
		checkpointAdapters := createCheckpointToolAdapters(gcm)
		tools.SetGuardOnAdapters(checkpointAdapters, a.guard)
		cfg.Adapters = append(cfg.Adapters, checkpointAdapters...)
	}

	result, err := RunAgentLoop(ctx, cfg)
	if err != nil {
		return AgentResult{}, err
	}
	// Full 变体：Text 被程序化消费（下方 consolidation 提交依赖完整输出）；
	// LLM 上下文由 Director 层 FormatForDirector 走分级摘要。
	agentResult := FinalizeResultFull(a.Name(), input, result)
	// [知识管理] 子任务完成后自动沉淀到知识库（非阻塞）
	if a.knowledge != nil {
		autoConsolidateSubtask(a.mcpClient, a.LLM, "coding_agent", "coding_modification", input, agentResult.Text)
	}
	return agentResult, nil
}

// Registry 返回工具注册表引用（供外部访问）
func (a *CodingAgent) Registry() *tools.Registry {
	return a.registry
}

// pruneSchema 是 prune_history 工具的 JSON Schema，提取为包级变量供多处复用。
var pruneSchema = map[string]interface{}{
	"type": "object",
	"properties": map[string]interface{}{
		"action": map[string]interface{}{
			"type":        "string",
			"enum":        []string{"list", "merge", "delete"},
			"description": "Operation type: list=List entries, merge=Merge similar entries, delete=Delete by ID",
		},
		"limit": map[string]interface{}{
			"type":        "integer",
			"description": "Max entries to read for list/merge, default 50",
			"default":     50,
		},
		"type": map[string]interface{}{
			"type":        "string",
			"description": "Filter by knowledge type (optional, empty=all)",
		},
		"tag": map[string]interface{}{
			"type":        "string",
			"description": "Filter by tag (optional)",
		},
		"ids": map[string]interface{}{
			"type":        "array",
			"items":       map[string]interface{}{"type": "string"},
			"description": "Required for delete: list of entry IDs to delete",
		},
		"similarity_threshold": map[string]interface{}{
			"type":        "number",
			"description": "Similarity threshold for merge, default 0.80",
			"default":     0.80,
		},
	},
	"required": []string{"action"},
}

// createKnowledgeToolAdapters creates tool adapters for knowledge management operations.
// sourceAgent/knowledgeType 非空时注册 consolidate_knowledge，为空时仅注册 prune_history（用于 Director）。
func createKnowledgeToolAdapters(mcpClient *mcp.MCPClient, llm llm.Engine, sourceAgent, knowledgeType string) []*tools.Adapter {
	pruneTool := tools.NewPruneHistoryTool(mcpClient, llm)

	adapters := []*tools.Adapter{
		tools.NewAdapter("prune_history", "Maintain knowledge base health: list current entries, merge similar entries, or delete stale entries by ID. Run merge periodically to deduplicate.", pruneTool.Execute).WithSchema(pruneSchema),
	}

	// sourceAgent/knowledgeType 为空时不注册 consolidate_knowledge（如 Director）
	if sourceAgent == "" || knowledgeType == "" {
		return adapters
	}

	consolidateTool := tools.NewConsolidateKnowledgeTool(mcpClient, llm, sourceAgent, knowledgeType)

	consolidateSchema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"title": map[string]interface{}{
				"type":        "string",
				"description": "Knowledge entry title (≤200 characters)",
				"maxLength":   200,
			},
			"content": map[string]interface{}{
				"type":        "string",
				"description": "Knowledge content (≤1500 characters). Keep file paths, function names, and symbol names.",
				"maxLength":   1500,
			},
			"tags": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"minItems":    1,
				"description": "Knowledge tag list, at least 1 tag",
			},
			"related_files": map[string]interface{}{
				"type":        "array",
				"items":       map[string]interface{}{"type": "string"},
				"description": "Related file paths (optional)",
			},
			"task_id": map[string]interface{}{
				"type":        "string",
				"description": "Associated task ID (optional)",
			},
			"confidence": map[string]interface{}{
				"type":        "number",
				"description": "Confidence 0-1, default 1.0",
				"default":     1.0,
			},
		},
		"required": []string{"title", "content", "tags"},
	}

	adapters = append(adapters,
		tools.NewAdapter("consolidate_knowledge", "Consolidate key knowledge from the current task and write it into the knowledge base. Type and source are automatically tagged by the system — no need to provide them. Suitable for: (1) distilling domain knowledge after code analysis; (2) recording key change decisions after code modifications; (3) capturing important architecture patterns or design rules. Condense content to ≤1500 characters and provide at least 1 tag before execution.", consolidateTool.Execute).WithSchema(consolidateSchema),
	)

	return adapters
}

// createCheckpointToolAdapters creates tool adapters for manual git checkpoint operations.
func createCheckpointToolAdapters(gcm *GitCheckpointManager) []*tools.Adapter {
	// git_checkpoint_list
	listAdapter := tools.NewAdapter("git_checkpoint_list",
		"List all available git checkpoints for the current coding session. Use this to see what rollback points are available. Each checkpoint represents a saved state of the codebase that you can return to if something goes wrong.",
		func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			checkpoints, err := gcm.ListCheckpoints(ctx)
			if err != nil {
				return "", err
			}
			if len(checkpoints) == 0 {
				return "No checkpoints available yet. Checkpoints are created automatically after file-modifying steps.", nil
			}
			var sb strings.Builder
			sb.WriteString("Available checkpoints:\n")
			for i, cp := range checkpoints {
				sb.WriteString(fmt.Sprintf("%d. [%s] %s — %s\n", i+1, cp.Date, cp.Tag, cp.Message))
			}
			sb.WriteString("\nUse git_checkpoint_rollback with the tag name to roll back.")
			return sb.String(), nil
		}).WithSchema(map[string]interface{}{
		"type":       "object",
		"properties": map[string]interface{}{},
		"required":   []string{},
	})

	// git_checkpoint_rollback
	rollbackAdapter := tools.NewAdapter("git_checkpoint_rollback",
		"Roll back the working tree to a specific checkpoint. WARNING: This discards all changes made AFTER the checkpoint. Use git_checkpoint_list first to see available checkpoints. The checkpoint_tag parameter should be the full tag name from the list.",
		func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			tagRaw, ok := params["checkpoint_tag"]
			if !ok {
				return "", fmt.Errorf("checkpoint_tag is required")
			}
			tag, ok := tagRaw.(string)
			if !ok || tag == "" {
				return "", fmt.Errorf("checkpoint_tag must be a non-empty string")
			}
			if err := gcm.RollbackToCheckpoint(ctx, tag); err != nil {
				return "", err
			}
			return fmt.Sprintf("Successfully rolled back to checkpoint: %s", tag), nil
		}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"checkpoint_tag": map[string]interface{}{
				"type":        "string",
				"description": "The tag name of the checkpoint to roll back to (from git_checkpoint_list)",
			},
		},
		"required": []string{"checkpoint_tag"},
	})

	// git_checkpoint_create
	createAdapter := tools.NewAdapter("git_checkpoint_create",
		"Manually create a git checkpoint at the current state. Use this before attempting risky operations like large refactors, complex merges, or experimental changes. A checkpoint allows you to roll back if something goes wrong.",
		func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			msgRaw, ok := params["message"]
			if !ok {
				return "", fmt.Errorf("message is required")
			}
			msg, ok := msgRaw.(string)
			if !ok || msg == "" {
				return "", fmt.Errorf("message must be a non-empty string")
			}
			tag, err := gcm.CreateManualCheckpoint(ctx, msg)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("Checkpoint created successfully: %s", tag), nil
		}).WithSchema(map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"message": map[string]interface{}{
				"type":        "string",
				"description": "A brief description of why this checkpoint is being created",
			},
		},
		"required": []string{"message"},
	})

	return []*tools.Adapter{listAdapter, rollbackAdapter, createAdapter}
}
