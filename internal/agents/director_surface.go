package agents

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"yagent/internal/agents/director"
	"yagent/internal/blackboard"
	"yagent/internal/memory"
	"yagent/internal/tools"
	"yagent/internal/toolspec"
)

// 本文件取代 director.go 里的三处手工装配：buildToolAdapters（一个 switch）、
// buildDelegateAdapters（六段结构相同的闭包）、getToolFunc（第二套 switch）。
//
// 装配规则、能力标签、授权要求现在都在注册表里；Director 只负责提供依赖
// （toolDeps）和委派流水线（delegateTo）。

// builtinAgent 是一个可被 delegate_ 委派的内建子 Agent。
type builtinAgent interface {
	Run(ctx context.Context, input string) (AgentResult, error)
}

// delegateSpec 描述一个内建委派目标：target 是 delegate_ 去掉前缀后的名字，
// noteAgent 是便签目录名（沿用历史的 "repo_agent"/"coding_agent" 等，别改名，
// 否则 /resume 恢复出来的旧便签路径会对不上）。
type delegateSpec struct {
	target    string
	noteAgent string
	agent     func() builtinAgent
}

func (a *DirectorAgent) delegateTargets() map[string]delegateSpec {
	return map[string]delegateSpec{
		"repo":    {"repo", "repo_agent", func() builtinAgent { return a.RepoAgent }},
		"coding":  {"coding", "coding_agent", func() builtinAgent { return a.CodingAgent }},
		"chat":    {"chat", "chat_agent", func() builtinAgent { return a.ChatAgent }},
		"devops":  {"devops", "devops_agent", func() builtinAgent { return a.DevOpsAgent }},
		"browser": {"browser", "browser_agent", func() builtinAgent { return a.BrowserAgent }},
	}
}

// toolDeps 组装 Director 一次工具装配所需的依赖。
//
// boardID 是按会话变化的（SetBoardScope 里写），所以 Deps 与 Surface 都在运行期重建，
// 不能在构造期固化 —— 那正是历史上 read_note/read_artifact 只能在 executor 里
// 运行期注入的原因，现在有了正规位置。
func (a *DirectorAgent) toolDeps() *toolspec.Deps {
	d := depsFor("director", a.env, a.guard, a.boardID)
	d.Files = a.files
	d.Search = a.search
	d.Flow = a.flow
	d.Deep = a.deepThinker
	d.Repo = a.repo
	d.Delegate = a.delegateTo
	d.Disabled = a.disabledAgents
	// Director 只做知识维护（裁剪），不写入本任务知识：与历史上
	// createKnowledgeToolAdapters(mcp, llm, "", "") 只注册 prune 的行为一致。
	if a.mcpClient != nil {
		d.Prune = tools.NewPruneHistoryTool(a.mcpClient, a.LLM).Execute
	}
	return d
}

// rebuildSurface 按当前依赖重建工具面，并刷新 Adapters 快照（兼容仍读该字段的调用点与测试）。
func (a *DirectorAgent) rebuildSurface() *tools.Surface {
	s := surfaceFor(toolspec.ProfileDirector, a.toolDeps(), a.Name())
	if len(a.materializedAcrossTurns) > 0 {
		names := make([]string, 0, len(a.materializedAcrossTurns))
		for name := range a.materializedAcrossTurns {
			names = append(names, name)
		}
		s.RestoreMaterialized(names)
	}
	a.surface = s
	a.Adapters = s.Adapters()
	return s
}

// carryOverMaterialized 在本轮结束前把已取回的冷工具带回进程级记忆，
// 下一轮重建 Surface 时直接恢复，不必重复 load_tool。
func (a *DirectorAgent) carryOverMaterialized() {
	if a.surface == nil {
		return
	}
	if a.materializedAcrossTurns == nil {
		a.materializedAcrossTurns = map[string]bool{}
	}
	for _, name := range a.surface.Materialized() {
		a.materializedAcrossTurns[name] = true
	}
}

// surfaceOrDefault 返回当前工具面；构造期之后的任何路径都应已有，兜底重建只为防御。
func (a *DirectorAgent) surfaceOrDefault() *tools.Surface {
	if a.surface == nil {
		return a.rebuildSurface()
	}
	return a.surface
}

// delegateTo 是全部 delegate_ 工具的唯一实现：解析 context_refs → 便签板 →
// 子任务 rollout → 档位路由 → 运行 → 写便签 → 分级摘要（超预算落 artifact）。
//
// 历史上这六段闭包逐字重复，只有 target 名字与 noteAgent 不同；
// 新增一个委派工具现在只需要一条 Spec + map 里一行。
func (a *DirectorAgent) delegateTo(ctx context.Context, target string, params map[string]interface{}) (interface{}, error) {
	task, ok := params["task"].(string)
	if !ok || strings.TrimSpace(task) == "" {
		return nil, fmt.Errorf("task parameter required")
	}

	if target == "meta" {
		return a.runMetaDesign(ctx, task)
	}

	spec, known := a.delegateTargets()[target]
	if !known {
		// Meta 在运行期注册的 Custom Agent 走这条路。
		if ca, found := a.metaHandler.Get("delegate_" + target); found {
			return a.runCustomAgent(ctx, ca, task)
		}
		return nil, fmt.Errorf("unknown delegate target %q", target)
	}

	agent := spec.agent()
	if agent == nil {
		return nil, fmt.Errorf("sub-agent %q is not wired", target)
	}

	packedTask, unresolved := a.packDelegateTask(task, params)
	ctx = blackboard.WithBoardID(ctx, a.boardID)
	if rolloutWriter := a.createRolloutWriter(target, task); rolloutWriter != nil {
		defer rolloutWriter.Close()
		ctx = memory.WithRolloutWriter(ctx, rolloutWriter)
	}
	ctx = a.routeCtx(ctx, target, task)

	result, err := agent.Run(ctx, packedTask)
	noteRef := a.writeDelegateNote(spec.noteAgent, "delegate_"+target, task, result, err)
	return a.applyEnhancedCommander(target, task, result, err, noteRef, unresolved)
}

// runMetaDesign 让 Meta-Agent 设计一个新子 Agent：注册 → 立即执行一次 → 返回设计+执行结果。
//
// JSON 解析失败时带格式纠正提示重试（metaRetryCount 次），全部失败则回吐原始输出，
// 这一段语义与历史一致，只是"执行新 Agent"从"在 Adapters 里找到那个适配器再 Call"
// 改成直接走 delegateTo —— 少一次依赖隐式注册顺序的查找。
func (a *DirectorAgent) runMetaDesign(ctx context.Context, task string) (interface{}, error) {
	ctx = blackboard.WithBoardID(ctx, a.boardID)
	if rolloutWriter := a.createRolloutWriter("meta", task); rolloutWriter != nil {
		defer rolloutWriter.Close()
		ctx = memory.WithRolloutWriter(ctx, rolloutWriter)
	}
	ctx = a.routeCtx(ctx, "meta", task)

	if a.MetaAgent == nil {
		return nil, fmt.Errorf("Meta-Agent is not wired")
	}

	maxRetries := a.metaRetryCount
	if maxRetries < 1 {
		maxRetries = 1
	}
	var lastRawOutput string

	for attempt := 0; attempt < maxRetries; attempt++ {
		retryTask := task
		if attempt > 0 {
			retryTask = fmt.Sprintf(
				"%s\n\n[FORMAT CORRECTION — Attempt %d/%d]\nYour previous output was NOT valid JSON or missing required fields. You MUST output ONLY a valid JSON object with these exact top-level keys:\n{\n  \"thinking\": \"...\",\n  \"agent_name\": \"...\",\n  \"agent_design\": \"...\",\n  \"tools_used\": [...],\n  \"task_for_agent\": \"...\"\n}\n\nDo NOT wrap in markdown code fences (```). Do NOT include any text outside the JSON object.",
				task, attempt, maxRetries-1,
			)
		}

		metaResult, err := a.MetaAgent.Run(ctx, retryTask)
		if err != nil {
			return nil, fmt.Errorf("Meta-Agent design failed: %w", err)
		}
		a.setPendingSubAgentMemory(&metaResult)
		lastRawOutput = metaResult.Text

		systemPrompt, execResult, parseErr := director.ParseMetaAgentOutput(metaResult.Text)
		if parseErr != nil {
			slog.Warn("Meta-Agent JSON parse failed, retrying", "attempt", attempt+1, "maxRetries", maxRetries, "error", parseErr)
			continue
		}
		if execResult.AgentName == "" || systemPrompt == "" {
			return fmt.Sprintf("[Meta-Agent Design Result]\nAgent could not be registered (missing name or design). Raw output: %s", metaResult.Text), nil
		}

		customAgent := &CustomAgent{
			Name:         toSnakeCase(execResult.AgentName),
			DisplayName:  execResult.AgentName,
			SystemPrompt: systemPrompt,
			ToolsUsed:    execResult.ToolsUsed,
			Description: fmt.Sprintf("Custom agent designed for: %s. Uses tools: %s.",
				execResult.AgentName, strings.Join(execResult.ToolsUsed, ", ")),
		}
		if !a.registerCustomAgent(customAgent) {
			return fmt.Sprintf("[Meta-Agent Design Result]\nAgent %s already registered; reuse its existing delegate tool.", execResult.AgentName), nil
		}

		agentTask := execResult.TaskForAgent
		if strings.TrimSpace(agentTask) == "" {
			agentTask = task
		}
		resultStr, callErr := a.runCustomAgent(ctx, customAgent, agentTask)
		if callErr != nil {
			return nil, fmt.Errorf("new agent %s execution failed: %w", execResult.AgentName, callErr)
		}
		return fmt.Sprintf(
			"[Meta-Agent: Agent Designed and Executed]\nDesigned Agent: %s\nTools: %s\n\n[Execution Result]\n%s\n\n[New Agent Registered]\nA new specialized agent \"%s\" is now available via the `%s` tool for future tasks of this type.",
			execResult.AgentName,
			strings.Join(execResult.ToolsUsed, ", "),
			resultStr,
			execResult.AgentName,
			"delegate_"+customAgent.Name,
		), nil
	}

	slog.Warn("Meta-Agent JSON parse failed after all retries, returning raw output")
	return lastRawOutput, nil
}

// runCustomAgent 用 Meta 交来的工具名清单装配该 Agent 的工具面并跑一轮。
// MaxSteps=15 保持不变：它是单次定向任务的一次性 Agent，不是常驻角色。
func (a *DirectorAgent) runCustomAgent(ctx context.Context, ca *CustomAgent, task string) (string, error) {
	deps := a.toolDeps()
	selected, unknown, rejected := toolspec.MatchNames(ca.ToolsUsed, toolspec.ProfileDirector.Palette, deps)
	if len(unknown) > 0 {
		slog.Warn("custom agent asked for unregistered tools", "agent", ca.Name, "unknown", unknown)
	}
	if len(rejected) > 0 {
		slog.Warn("custom agent asked for tools outside the director palette", "agent", ca.Name, "rejected", rejected)
	}
	names := make([]string, 0, len(selected)+1)
	for _, s := range selected {
		names = append(names, s.Name)
	}

	profile := toolspec.Profile{Agent: ca.Name, Names: names}
	surface := surfaceFor(profile, deps, ca.DisplayName)

	return a.executeCustomAgent(ctx, ca, surface, task)
}

// registerCustomAgent 把新设计的 Agent 注册成一个 delegate_<name> 工具，
// 当场可执行、同一回合的下一步就对模型可见（Defs() 每步重算）。
// 返回 false 表示同名已存在（metaHandler 拒绝覆盖）。
func (a *DirectorAgent) registerCustomAgent(ca *CustomAgent) bool {
	delegateName := "delegate_" + ca.Name
	if err := a.metaHandler.Register(ca); err != nil {
		slog.Info("Custom agent already registered", "name", delegateName)
		return false
	}

	spec := &toolspec.Spec{
		Name:      delegateName,
		Effect:    toolspec.EffectDelegate,
		Frequency: toolspec.Hot,
		Mutates:   true,
		Audience:  []string{"director"},
		Brief:     "委派给自定义 Agent " + ca.DisplayName,
		Available: func(d *toolspec.Deps) (bool, string) {
			return d.Delegate != nil, "Director 未装配委派能力"
		},
		Describe: func(d *toolspec.Deps) string {
			return fmt.Sprintf("Delegate to %s — a custom specialized agent designed by Meta-Agent. %s",
				ca.DisplayName, ca.Description)
		},
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if d.Delegate == nil {
				return nil, fmt.Errorf("delegate 未装配")
			}
			return func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
				return d.Delegate(ctx, ca.Name, params)
			}, nil
		},
	}

	if a.surface != nil {
		if skipped := a.surface.Add(spec); len(skipped) > 0 {
			slog.Warn("custom agent tool registration skipped", "delegate", delegateName,
				"reasons", describeSkipped(skipped))
		}
		// Adapters 是 Surface 的快照视图，追加后刷新，避免按字段读取的调用点看不到。
		a.Adapters = a.surface.Adapters()
		return true
	}
	// 没有 Surface（手工注入 Adapters 的测试/调用点）：直接追加，不覆盖已有条目。
	fn, err := spec.Build(a.toolDeps())
	if err != nil {
		slog.Warn("custom agent tool registration failed", "delegate", delegateName, "error", err)
		return true
	}
	ad := tools.NewAdapter(spec.Name, spec.Description(a.toolDeps()), fn).
		WithSchema(spec.Parameters()).WithReadOnly(spec.IsReadOnly())
	tools.SetGuardOnAdapters([]*tools.Adapter{ad}, a.guard)
	a.Adapters = append(a.Adapters, ad)
	slog.Info("Custom agent registered", "delegate_name", delegateName, "display_name", ca.DisplayName, "tools", ca.ToolsUsed)
	return true
}

func describeSkipped(skipped []toolspec.SkippedTool) []string {
	out := make([]string, 0, len(skipped))
	for _, s := range skipped {
		out = append(out, s.Name+": "+s.Reason)
	}
	return out
}

// customAgentPromptSection 生成 system prompt 里的自定义 Agent 清单。
// 名字与描述都取自注册表与 metaHandler，不再依赖手写 prompt 片段。
func (a *DirectorAgent) customAgentPromptSection() string {
	agents := a.metaHandler.List()
	if len(agents) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n### Custom Agents\nThe following specialized agents have been designed by Meta-Agent and are permanently available for delegation:\n\n")
	for _, ca := range agents {
		fmt.Fprintf(&b, "- **%s** (`delegate_%s`): %s\n", ca.DisplayName, ca.Name, ca.Description)
	}
	b.WriteString("\nUse these agents via their delegate tools for tasks matching their specializations.\n")
	return b.String()
}
