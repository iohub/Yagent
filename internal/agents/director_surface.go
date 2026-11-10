package agents

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"yagent/internal/blackboard"
	"yagent/internal/config"
	"yagent/internal/memory"
	"yagent/internal/plasma"
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
	// spawn 每次调用返回一个**新实例**（D2）：内建 Agent 是进程级单例，
	// 并发委派复用同一份自身状态就是竞态；今天的实际例外是 Coding 的 thinkLink。
	spawn func(ctx context.Context) builtinAgent
}

// delegateTargets 列出可委派的内建目标。
//
// 每个 spawn 都必须先判**具体类型**的 nil 再返回：`return a.RepoAgent`（或
// `a.RepoAgent.SpawnForDelegation(ctx)`）在指针为 nil 时返回的是"装着 nil 的 interface"，
// 它 != nil —— delegateTo 里 `agent == nil` 那条"not wired"守卫于是永远不成立，
// 未装配的子 Agent 会一路走到 nil 解引用。这个坑在原实现里就存在（只是生产总是把五个都接上）。
func (a *DirectorAgent) delegateTargets() map[string]delegateSpec {
	return map[string]delegateSpec{
		"repo": {"repo", "repo_agent", func(ctx context.Context) builtinAgent {
			if a.RepoAgent == nil {
				return nil
			}
			return a.RepoAgent.SpawnForDelegation(ctx)
		}},
		"coding": {"coding", "coding_agent", func(ctx context.Context) builtinAgent {
			if a.CodingAgent == nil {
				return nil
			}
			return a.CodingAgent.SpawnForDelegation(ctx)
		}},
		"chat": {"chat", "chat_agent", func(ctx context.Context) builtinAgent {
			if a.ChatAgent == nil {
				return nil
			}
			return a.ChatAgent.SpawnForDelegation(ctx)
		}},
		"devops": {"devops", "devops_agent", func(ctx context.Context) builtinAgent {
			if a.DevOpsAgent == nil {
				return nil
			}
			return a.DevOpsAgent.SpawnForDelegation(ctx)
		}},
		"browser": {"browser", "browser_agent", func(ctx context.Context) builtinAgent {
			if a.BrowserAgent == nil {
				return nil
			}
			return a.BrowserAgent.SpawnForDelegation(ctx)
		}},
	}
}

// toolDeps 组装 Director 一次工具装配所需的依赖。
//
// boardID 是按会话变化的（SetBoardScope 里写），所以 Deps 与 Surface 都在运行期重建，
// 不能在构造期固化 —— 那正是历史上 read_note/read_artifact 只能在 executor 里
// 运行期注入的原因，现在有了正规位置。
func (a *DirectorAgent) toolDeps() *toolspec.Deps {
	d := depsFor("director", a.env, a.guard, a.boardID, a.blueprint, a.external)
	d.Files = a.files
	d.Search = a.search
	d.Flow = a.flow
	d.Deep = a.deepThinker
	d.Repo = a.repo
	d.Delegate = a.delegateTo
	d.Disabled = a.disabledAgents
	// 生成器可用性：装配事实（gen != nil）由这一侧决定，toolspec 只负责问。
	// 降级原因随端口一起走，Surface.Skipped 才有话可说（ADR-15）。
	d.Plasma = plasmaGate{gen: a.plasmaGen, reason: a.plasmaWhy}
	return d
}

// rebuildSurface 按当前依赖重建工具面。
//
// 重建后必须把**已注册的生成式 Agent**重新挂回去：`registerCustomAgent` 追加到的是
// "当前那一份 Surface"，而本函数会把它整份换掉。漏了这一步的症状是"加载期装进来的
// Agent 在第二轮对话里从工具面消失"（不报错、注册表里还在、模型就是看不见）——
// P9a 期间 `runMetaDesign` 生成的 Agent 正是这样活不过一个 Run。
//
// 遍历的是 metaHandler（唯一注册表）而不是某份快照：两份列表必然漂走。
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
	a.mountRegisteredAgents()
	return s
}

// mountRegisteredAgents 把注册表里的生成式 Agent 挂到当前 Surface 上。
// 顺序是 metaHandler.List 的字典序（跨进程确定，prompt 前缀缓存靠它）。
func (a *DirectorAgent) mountRegisteredAgents() {
	if a.surface == nil || a.metaHandler == nil {
		return
	}
	for _, ca := range a.metaHandler.List() {
		if skipped := a.surface.Add(customAgentSpec(ca)); len(skipped) > 0 {
			// 同名/依赖缺失都会落到这里。不是致命错误（工具面仍然可用），但必须可见。
			slog.Warn("custom agent tool mount skipped", "delegate", "delegate_"+ca.Name,
				"reasons", describeSkipped(skipped))
		}
	}
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

	agent := spec.spawn(ctx)
	if agent == nil {
		return nil, fmt.Errorf("sub-agent %q is not wired", target)
	}

	packedTask, unresolved := a.packDelegateTask(task, params, target)
	ctx = blackboard.WithBoardID(ctx, a.boardID)
	if rolloutWriter := a.createRolloutWriter(ctx, target, task); rolloutWriter != nil {
		defer rolloutWriter.Close()
		ctx = memory.WithRolloutWriter(ctx, rolloutWriter)
	}
	ctx = a.routeCtx(ctx, target, task)

	result, err := agent.Run(ctx, packedTask)
	// 结果交给本次调用的载体，由 directorToolRunner 在 ad.Call 返回后注入记忆并写 RunState。
	// 这里不直接碰 Director 上的全局槽 —— 并行委派下那会串味（见 delegationOutcome）。
	recordDelegationOutcome(ctx, result)
	noteRef := a.writeDelegateNote(spec.noteAgent, "delegate_"+target, task, result, err)
	// 便签引用进遥测账本（ADR-6 的 note_ref）：只记工具名的话，事后无法从一次委派
	// 追到它写下的结论。
	recordDelegationNoteRef(ctx, noteRef)
	return a.applyEnhancedCommander(target, task, result, err, noteRef, unresolved)
}

// delegate_meta 的实现已整体搬到 plasma_meta.go（runMetaDesign）：生成职责移交
// Python 之后，这条路径变成"驱动器 + 判定 + 装配"，留在本文件会让 Surface 装配
// 与生成事务混在两件事里。

// runCustomAgent 用 plan 解析出的工具名清单装配该 Agent 的工具面并跑一轮。
// 步数/超时/压缩等运行参数不在这里决定：它们来自 bundle.runtime（LinkPlan.Config），
// 由 executeCustomAgent 的 applyExecutorConfig 套上去。
func (a *DirectorAgent) runCustomAgent(ctx context.Context, ca *CustomAgent, task string) (string, error) {
	// 升级门在装配**之前**：未批准时越权工具根本不在清单里，装出来的 Agent 是个
	// 能力残缺的版本 —— 先让它跑起来再失败，烧掉的是步数与 token，而用户什么也没看到。
	if err := a.ensureEscalationApproved(ctx, ca.Name); err != nil {
		return "", err
	}

	// 生效工具面与能力上限：plasma 产物读加载期登记的坐标（批准后会换成复核过的版本），
	// 老路径（没有坐标）回落到 CustomAgent 自身字段 + PlasmaPalette。
	toolsUsed := ca.ToolsUsed
	palette := plasma.PlasmaPalette
	if info, ok := a.plasmaAgentInfoOf(ca.Name); ok {
		toolsUsed = info.Tools
		palette = info.Palette
	}

	deps := a.toolDeps()
	selected, unknown, rejected := toolspec.MatchNames(toolsUsed, palette, deps)
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

	// Surface.Add 是 Custom Agent 唯一的注册通道：它挂的就是"模型下一步要看的东西"，
	// 所以同名工具重复注册会被拒绝并记进返回值，而不是静默换一个执行器。
	if a.surface == nil {
		a.rebuildSurface()
		return true // rebuildSurface 会从注册表里把它挂上
	}
	if skipped := a.surface.Add(customAgentSpec(ca)); len(skipped) > 0 {
		slog.Warn("custom agent tool registration skipped", "delegate", delegateName,
			"reasons", describeSkipped(skipped))
	}
	slog.Info("Custom agent registered", "delegate_name", delegateName, "display_name", ca.DisplayName, "tools", ca.ToolsUsed)
	return true
}

// customAgentSpec 是一个生成式 Agent 的**唯一** Spec 表达式。
//
// 两处消费：registerCustomAgent（本回合立即可用）与 mountRegisteredAgents（每次
// rebuildSurface 重新挂回）。写两份的后果是"注册时能跑、下一轮换个描述/少了校验"，
// 而那种漂移只会在某个回合突然表现成"工具不见了"。
func customAgentSpec(ca *CustomAgent) *toolspec.Spec {
	delegateName := "delegate_" + ca.Name
	return &toolspec.Spec{
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
//
// 三条纪律：
//   - 顺序是 metaHandler.List 的**字典序**（跨进程确定）。这一段拼在
//     blueprint / skills / mcp **之后（system prompt 末尾）**（run() 的拼装点），是唯一
//     随 Agent 注册表变化的最易变段：字典序保证跨进程确定性，而放在末尾使抖动代价仅限于
//     本段自身，不波及前面各段的前缀缓存。
//   - 每行按 `[plasma] catalog_line_chars` 截断（显式 0 = 不裁剪）。描述来自 bundle，
//     长度不可控：一个 Agent 写 800 字描述就会把目录撑成正文，而模型只需要认得出它。
//   - 截断必须留**省略账**：哪几行被剪了、阈值是多少、去哪看全文。静默剪短与静默丢失同形。
func (a *DirectorAgent) customAgentPromptSection() string {
	agents := a.metaHandler.List()
	if len(agents) == 0 {
		return ""
	}
	lineChars := config.GlobalPlasma().CatalogLineChars
	var b strings.Builder
	b.WriteString("\n\n### Custom Agents\nThese agents were generated by plasma and are available for delegation:\n\n")
	clipped := 0
	for _, ca := range agents {
		desc, cut := clipCatalogLine(ca.Description, lineChars)
		if cut {
			clipped++
		}
		fmt.Fprintf(&b, "- **%s** (`delegate_%s`): %s\n", ca.DisplayName, ca.Name, desc)
	}
	if clipped > 0 {
		fmt.Fprintf(&b, "\n(%d of %d descriptions truncated to %d chars by [plasma] catalog_line_chars; "+
			"full text: `yagent plasma list`)\n", clipped, len(agents), lineChars)
	}
	b.WriteString("\nUse these agents via their delegate tools for tasks matching their specializations.\n")
	return b.String()
}
