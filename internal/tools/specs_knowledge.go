package tools

import (
	"yagent/internal/toolspec"
)

// 知识固化与维护。两个执行器都由 Agent 侧按自己的 source/kind 预先绑定
// （coding_agent/coding_modification、repo_agent/repo_retrieval；Director 只有 prune）。
// 因此 Spec 不知道也不该知道调用者是谁 —— 绑定缺失即不可用。
func init() {
	toolspec.MustRegister(toolspec.Spec{
		Name:      "consolidate_knowledge",
		Effect:    toolspec.EffectKnowledgeWrite,
		Frequency: toolspec.Cold,
		Mutates:   true,
		Brief:     "把本任务关键结论固化进知识库",
		Available: func(d *toolspec.Deps) (bool, string) {
			if d.Consolidate == nil {
				return false, "知识库写入未装配（需要 codeseek MCP + 当前 Agent 的 source/kind）"
			}
			return true, ""
		},
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Consolidate, "Consolidate"); err != nil {
				return nil, err
			}
			return d.Consolidate, nil
		},
	})

	// prune_history 的 action=merge/delete 会改写知识库，所以 Mutates=true（不可并发）。
	toolspec.MustRegister(toolspec.Spec{
		Name:      "prune_history",
		Effect:    toolspec.EffectKnowledgePrune,
		Frequency: toolspec.Cold,
		Mutates:   true,
		Brief:     "列举/合并/删除知识库条目",
		Available: func(d *toolspec.Deps) (bool, string) {
			if d.Prune == nil {
				return false, "知识库维护未装配（需要 codeseek MCP）"
			}
			return true, ""
		},
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Prune, "Prune"); err != nil {
				return nil, err
			}
			return d.Prune, nil
		},
	})
}
