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
func depsFor(profileKey string, env Env, guard *tools.WorkspaceGuard, boardID string) *toolspec.Deps {
	d := &toolspec.Deps{AgentName: profileKey, Guard: guard, BoardID: boardID}
	if env != nil {
		d.ProjectPath = env.ProjectPath()
		d.FullYolo = env.FullYoloMode()
	}
	return d
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
		desc := strings.TrimSpace(firstSentence(sc.Description))
		if desc == "" {
			desc = s.Brief
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s |\n", s.Name, effectCategory(s.Effect), desc)
	}
	return b.String()
}

func firstSentence(text string) string {
	if idx := strings.IndexByte(text, '.'); idx > 0 {
		return text[:idx+1]
	}
	return text
}
