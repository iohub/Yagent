package agents

import (
	"context"
	_ "embed"
	"fmt"
	"strings"
	"yagent/internal/blackboard"
	"yagent/internal/toolspec"

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
	params           CodingAgentParams
	BrowserAgent     *BrowserAgent
	maxSteps         int
	thinkLink        *thinklink.Store // 终极压缩支撑存储，实时记录用户原始输入，与 Director 模式一致，与 agent 同生命周期

	// boardWriter 让 delegate_browser 的产物也上便签板。Director 在本 Agent 之后才构造，
	// 所以它只能事后用 SetBoardWriter 注入；nil 时退回"直接返回文本"的旧行为。
	boardWriter BoardWriter
}

// SetBoardWriter 注入便签板写入器（由 app 在 Director 构造之后调用）。
func (a *CodingAgent) SetBoardWriter(w BoardWriter) {
	a.boardWriter = w
}

// CodingAgentParams 聚合 NewCodingAgent 所需的全部依赖。
// 用具名字段替代原先 21 个位置参数，让调用点自文档化，且后续增删依赖不再破坏函数签名。
type CodingAgentParams struct {
	Formatter   PromptFormatter
	Publisher   EventBus
	Env         Env
	Files       FileToolSet
	Search      SearchToolSet
	Sys         SysToolSet
	Edit        EditToolSet
	RepoOps     RepoToolSet
	Flow        FlowToolSet
	Thinker     Thinker
	DeepThinker DeepThinker
	Designer    Designer
	MicroAgent  MicroAgentRunner

	Guard   *tools.WorkspaceGuard
	RepoCtx RepoContextStore

	Knowledge KnowledgeProvider
	MCPClient *mcp.MCPClient

	GitCheckpointCfg     *config.GitCheckpointConfig
	EnhancedCommanderCfg config.EnhancedCommanderConfig

	LLM      llm.Engine
	MaxSteps int
	Browser  *BrowserAgent
}

// NewCodingAgent 构造 CodingAgent：按 tools.json 与参数集中的工具集装配适配器。
// 全部依赖通过 CodingAgentParams 传入。
func NewCodingAgent(p CodingAgentParams) *CodingAgent {
	return &CodingAgent{
		BaseAgent: BaseAgent{
			LLM:       p.LLM,
			Publisher: p.Publisher,
		},
		promptFmt:        p.Formatter,
		env:              p.Env,
		microAgent:       p.MicroAgent,
		repoCtx:          p.RepoCtx,
		knowledge:        p.Knowledge,
		mcpClient:        p.MCPClient,
		gitCheckpointCfg: p.GitCheckpointCfg,
		ecCfg:            p.EnhancedCommanderCfg,
		guard:            p.Guard,
		params:           p,
		maxSteps:         p.MaxSteps,
		BrowserAgent:     p.Browser,
		thinkLink:        thinklink.NewStore(0), // 容量 0=全量，与 Director 一致
	}
}

func (a *CodingAgent) surfaceFor(ctx context.Context, gcm *GitCheckpointManager) *tools.Surface {
	p := a.params
	deps := depsFor("coding", a.env, a.guard, blackboard.BoardIDFrom(ctx))
	deps.Files = p.Files
	deps.Search = p.Search
	deps.Sys = p.Sys
	deps.Edit = p.Edit
	deps.Repo = p.RepoOps
	deps.Flow = p.Flow
	deps.Thinker = p.Thinker
	deps.Micro = p.MicroAgent
	deps.Deep = p.DeepThinker
	deps.Design = p.Designer
	if p.MCPClient != nil {
		deps.Prune = tools.NewPruneHistoryTool(p.MCPClient, a.LLM).Execute
		deps.Consolidate = tools.NewConsolidateKnowledgeTool(p.MCPClient, a.LLM, "coding_agent", "coding_modification").Execute
	}
	// Coding 只会委派 browser；其余 delegate_ 由 Spec.Audience 排除在外。
	deps.Delegate = a.browserDelegate
	if gcm != nil {
		deps.CheckpointList = func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			return gcm.ListCheckpoints(ctx)
		}
		deps.CheckpointCreate = func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			return gcm.CreateManualCheckpoint(ctx, stringParam(params, "message"))
		}
		deps.CheckpointRollback = func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
			return nil, gcm.RollbackToCheckpoint(ctx, stringParam(params, "checkpoint_tag"))
		}
	}
	return surfaceFor(toolspec.ProfileCoding, deps, a.Name())
}

// browserDelegate 与历史闭包语义一致：task 必填，返回 BrowserAgent 的文本结果。
// 能力不可用时的描述降级不在这里 —— 由 Spec.Describe 统一处理，Director/Coding 同一条路径。
func (a *CodingAgent) browserDelegate(ctx context.Context, target string, params map[string]interface{}) (interface{}, error) {
	if target != "browser" {
		return nil, fmt.Errorf("Coding-Agent 只能委派给 browser，收到 %q", target)
	}
	if a.BrowserAgent == nil {
		return nil, fmt.Errorf("Browser-Agent 未装配")
	}
	task, ok := params["task"].(string)
	if !ok || task == "" {
		return nil, fmt.Errorf("delegate_browser requires a non-empty 'task' parameter")
	}

	// 带上下文走：便签索引前置到子任务里，Browser 才知道前面已经查到过什么。
	packedTask := task
	if a.boardWriter != nil {
		if packed, _ := a.boardWriter.PackDelegateTask(task, params); packed != "" {
			packedTask = packed
		}
	}

	result, err := a.BrowserAgent.Run(ctx, packedTask)
	if a.boardWriter == nil {
		// 没注入写入器（老装配路径）：保持改动前的行为。
		if err != nil {
			return nil, err
		}
		return result.Text, nil
	}

	// 先落便签再判错：失败的委派也要留下"试过什么、为什么失败"，
	// 否则下一次同类任务会从头再试一遍。
	noteRef := a.boardWriter.WriteDelegateNote("browser_agent", "delegate_browser", task, result, err)
	if err != nil {
		return nil, err
	}
	return FormatForDirector("browser", result, noteRef, 0), nil
}

func stringParam(params map[string]interface{}, key string) string {
	v, _ := params[key].(string)
	return v
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
	cfg.Surface = a.surfaceFor(ctx, gcm)
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
	cfg.UltimateKeepPlans = 0           // 0=全部保留
	cfg.EnableUltimateCompression = ec.EnableUltimateCompression

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

		// 快照工具随 gcm 一起由 surfaceFor 决定是否注册，不再运行期 append
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
