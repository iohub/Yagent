package agents

import (
	"fmt"
	"log/slog"
	"strings"

	"yagent/internal/config"
	"yagent/internal/tools"
	"yagent/internal/toolspec"
)

// depsFor 组装一次工具装配的公共依赖。
//
// AgentName 用的是画像键（"chat"/"coding"/…），不是 Name() 的显示名 ——
// Spec.Audience 与 Profile.Agent 都按画像键比对，混进显示名会让受众判定失效。
//
// view 是蓝图注入视图（没有蓝图传 nil）。执行器**恒绑上**：read_blueprint 在
// "这个项目没有蓝图"时给出原因与修复方式，而不是从工具面上静默消失（设计稿 §8）。
// 参数收在签名上而不是各 Agent 自己记得绑：六个调用点少绑一个是编译不过，
// 少记一处是"那个 Agent 的模型以为这个能力不存在"。
func depsFor(profileKey string, env Env, guard *tools.WorkspaceGuard, boardID string, view *BlueprintView, ext External) *toolspec.Deps {
	d := &toolspec.Deps{
		AgentName: profileKey, Guard: guard, BoardID: boardID,
		Blueprint: blueprintExecutor(view),
		// 外部工具与技能通道：nil 是合法状态（没有 server / 没有技能），
		// 对应的工具要么不在工具面上，要么仍在场并解释为什么不可用。
		ExternalSpecs: ext.specsOrNil(),
		Skills:        ext.Skills,
	}
	if env != nil {
		d.ProjectPath = env.ProjectPath()
		d.FullYolo = env.FullYoloMode()
	}
	return d
}

// skillCatalogFor 是"哪个 Agent 的 system prompt 带技能目录"的**唯一**判据。
// 五个子 Agent 各调一行，而不是各写一遍 if —— 压缩开关历史上就是这么写成
// 5 个 AND 点，然后有一处忘了改（根 AGENTS.md §6.5）。
func skillCatalogFor(agent string, skills toolspec.SkillSource) string {
	if skills == nil || !config.SkillsInjectedInto(agent) {
		return ""
	}
	return skills.Catalog(config.GlobalSkills().CatalogMaxTokens)
}

// surfaceFor 按画像与依赖构造工具面，并把"声明了但依赖没装配"的工具打进日志。
// 过去这类信息散在 6 处 slog.Error/Warn 里且没人汇总，是静默失效的主要来源。
func surfaceFor(profile toolspec.Profile, deps *toolspec.Deps, agentDisplay string) *tools.Surface {
	cfg := config.GlobalDeferredTools()
	s := tools.NewSurface(profile, deps, tools.SurfaceOptions{
		Deferred:     !cfg.Disable,
		BudgetTokens: cfg.BudgetTokens,
	})
	if skipped := s.Skipped(); len(skipped) > 0 {
		entries := make([]string, 0, len(skipped))
		for _, item := range skipped {
			entries = append(entries, item.Name+": "+item.Reason)
		}
		slog.Warn("tool surface dropped declared tools", "agent", agentDisplay, "skipped", entries)
	}
	return s
}

// effectCategory 给 Meta 的工具池表格一个人类可读的分组名。
// 分类来自能力标签，因此新工具注册时不需要再去 prompt 里补一行。
func effectCategory(eff toolspec.Effect) string {
	switch {
	case eff.Has(toolspec.EffectFSWrite):
		return "Write"
	case eff.Has(toolspec.EffectFSRead):
		return "File"
	case eff.Has(toolspec.EffectSearchRegex):
		return "Search"
	case eff.Has(toolspec.EffectShell):
		return "System"
	case eff.Has(toolspec.EffectCodeSemantic | toolspec.EffectCodeGraph):
		return "Repo"
	case eff.Has(toolspec.EffectCogReflect | toolspec.EffectCogSubLLM | toolspec.EffectCogDeep | toolspec.EffectCogDesign):
		return "Cognitive"
	case eff.Has(toolspec.EffectContext):
		return "Context"
	case eff.Has(toolspec.EffectFlow | toolspec.EffectToolLoad):
		return "Flow"
	default:
		return "Other"
	}
}

// renderCustomAgentToolPool 生成 Meta-Agent prompt 里的可选工具池表格。
//
// 这张表过去是手写的，结果与 director.go 的 getToolFunc 长期不一致：
// prompt 广告了 semantic_search / query_code_skeleton / query_code_snippet，
// 而实现侧没有对应 case，Custom Agent 选了也是静默丢弃。改成从注册表派生，
// "广告"与"能发"就是同一个来源。
func renderCustomAgentToolPool() string {
	palette := toolspec.ProfileDirector.Palette
	deps := toolspec.ProfileDirector.ProbeDeps()
	var b strings.Builder
	b.WriteString("| Tool Name | Category | Description |\n")
	b.WriteString("|-----------|----------|-------------|\n")
	for _, s := range toolspec.Specs() {
		if !s.Effect.Has(palette) {
			continue
		}
		if ok, _ := s.AvailableFor(deps); !ok {
			continue
		}
		sc, _ := toolspec.GetSchema(s.Name)
		desc := toolspec.FirstSentence(sc.Description)
		if desc == "" {
			desc = s.Brief
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", s.Name, effectCategory(s.Effect), desc)
	}
	return b.String()
}
