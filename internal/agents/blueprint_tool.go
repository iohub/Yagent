package agents

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"yagent/internal/blueprint"
	"yagent/internal/toolspec"
)

// 本文件是设计稿 §7.1 的读通道：`read_blueprint` 是蓝图唯一的散文入口。
//
// 常驻注入只有形状②（目录 + 边），所以"这片到底负责什么"必须靠一次主动读取 ——
// 这不是省 token 的手段，而是把"不可验证的散文"挡在每轮成本之外的结构性决定。
//
// 两条容易写错的语义在这里钉住：
//   - **只读、可并发**：Spec 不写 Serial，于是它天然进 toolbatch（与 read_note 同族）。
//   - **绝不静默摘掉工具**：没有蓝图时执行器返回"为什么没有 + 怎么修"。
//     把工具从面上摘走的失效形态是"模型以为这里没有这个能力"，而它连问都没问过。

// blueprintUnavailable 是降级回执。文案是给模型读的，所以英文、且必须带修复方式。
const blueprintUnavailable = "blueprint: not available for this project.\n" +
	"Reason: [blueprint] enable=false, or env YAGENT_BLUEPRINT=0, or this directory was refused by the " +
	"root guardrails (home directory / filesystem root / neither a git repo nor a manifest project).\n" +
	"Fix: set enable = true in [blueprint] and unset YAGENT_BLUEPRINT; the startup log line " +
	"'blueprint:' names the exact reason if the guardrails refused this directory.\n" +
	"Meanwhile: use semantic_search and read_file — the repository structure itself is never lost, " +
	"only this cached map of it."

// scanningNotice 是派生还没跑完时的回执（I15：不阻塞、不编造）。
const scanningNotice = "blueprint: still scanning the repository structure (first scan has not finished). " +
	"Retry once later in this run; use semantic_search for symbols meanwhile."

func blueprintExecutor(view *BlueprintView) toolspec.ToolFunc {
	return func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
		if view == nil || view.src == nil || view.src.Root() == "" {
			return blueprintUnavailable, nil
		}
		snap := view.src.Snapshot()
		if snap == nil {
			return scanningNotice, nil
		}
		spec, note := proseSpecFromParams(params, view)
		if note != "" {
			return note, nil
		}
		// "module 为空 = 返回索引"（§7.1 的表）：索引就是形状②，不是散文。
		if spec.Section == blueprint.SecModules && spec.Module == "" {
			idx := blueprint.RenderIndex(snap, view.doc(), blueprint.IndexSpec{
				MaxTokens:   view.indexTokens,
				CountTokens: view.counter,
			})
			return idx.String(), nil
		}
		p := blueprint.RenderProse(snap, view.doc(), spec)
		if p.UnknownModule {
			return strings.Join(p.Lines, "\n"), nil
		}
		if len(p.Lines) == 0 {
			// 空正文会被模型读成"这里没东西"，然后开始不信蓝图 —— 所以给一句带计数的话。
			return fmt.Sprintf("blueprint: section %q has no annotations yet (%d modules derived). "+
				"Delegate repo to have it read and annotated.", spec.Section, len(snap.Leaves)), nil
		}
		out := append([]string{}, p.Header...)
		out = append(out, p.Lines...)
		if p.Truncated || p.Omitted > 0 {
			out = append(out, fmt.Sprintf("(showing %d of %d lines; offset/limit page through the rest)",
				len(p.Lines), p.Total))
		}
		if len(p.BadLines) > 0 {
			out = append(out, fmt.Sprintf("warning: %d malformed atom line(s) in the document were skipped", len(p.BadLines)))
		}
		return strings.Join(out, "\n"), nil
	}
}

// proseSpecFromParams 把工具参数换成渲染规格。
// 返回的第二值是给用户的一句话（参数明显写错时用它，而不是静默按默认渲染 ——
// "写了 module 却拼错段落名"和"什么都没写"是两件不同的事）。
func proseSpecFromParams(params map[string]interface{}, view *BlueprintView) (blueprint.ProseSpec, string) {
	spec := blueprint.ProseSpec{BudgetTokens: view.proseTokens, CountTokens: view.counter}
	section := strings.TrimSpace(asString(params["section"]))
	switch {
	case section == "":
	case section == "modules":
		spec.Section = blueprint.SecModules
	default:
		norm, ok := blueprint.NormaliseSection(section)
		if !ok {
			return spec, "blueprint: unknown section " + strconv.Quote(section) +
				" — use one of architecture / flows / conventions / dependencies / gotchas / modules."
		}
		spec.Section = norm
	}
	spec.Module = strings.TrimSpace(asString(params["module"]))
	if spec.Module != "" && spec.Section == "" {
		// 给了片名却没说段落：按 modules 处理是唯一的合理解读，但要说出来。
		spec.Section = blueprint.SecModules
	}
	if field := strings.TrimSpace(asString(params["field"])); field != "" {
		if !blueprint.IsModuleField(field) {
			return spec, "blueprint: unknown field " + strconv.Quote(field) +
				" — modules carry purpose / data_flow / add_here."
		}
		spec.Field = field
	}
	if state := strings.TrimSpace(asString(params["state"])); state != "" {
		switch state {
		case blueprint.StateOK, blueprint.StateStale, blueprint.StateUnanchored:
			spec.State = state
		default:
			return spec, "blueprint: unknown state " + strconv.Quote(state) + " — use ok / stale / unanchored."
		}
	}
	spec.Offset = asInt(params["offset"])
	spec.Limit = asInt(params["limit"])
	return spec, ""
}

func asString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asInt(v interface{}) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(t))
		return n
	}
	return 0
}

func registerBlueprintSpec() {
	toolspec.MustRegister(toolspec.Spec{
		Name:      "read_blueprint",
		Effect:    toolspec.EffectContext,
		Frequency: toolspec.Warm,
		Brief:     "读仓库蓝图：模块骨架与 Repo 提交的散文",
		// Available 恒为真：执行器恒已绑定（depsFor 里 blueprintExecutor 永不为 nil），
		// "这个项目没有蓝图"由回执自己说，而不是把工具从面上摘掉（设计稿 §8）。
		Available: func(d *toolspec.Deps) (bool, string) {
			if d.Blueprint == nil {
				return false, "read_blueprint 未绑定执行器"
			}
			return true, ""
		},
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if d.Blueprint == nil {
				return nil, errNoBinding("read_blueprint")
			}
			return d.Blueprint, nil
		},
	})
}
