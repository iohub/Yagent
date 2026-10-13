package tools

import (
	"yagent/internal/toolspec"
)

// codeseek 桥接的仓库理解与调用图工具。
//
// 并发说明：这些执行器走同一条 MCP stdio 管道。响应侧本来就按 request id
// 多路复用，请求侧的写锁是本次重构的前置修复（见 internal/mcp/client.go 的
// writeMu），因此它们可以标为只读并发 —— 过去它们不在 knownReadOnlyTools 里
// 只是白名单漏项，不是真的有副作用。
func init() {
	toolspec.MustRegister(toolspec.Spec{
		Name:      "semantic_search",
		Effect:    toolspec.EffectCodeSemantic,
		Frequency: toolspec.Hot,
		Trunc:     toolspec.TruncFirst,
		Display:   toolspec.DisplayCompact,
		Brief:     "按自然语言/代码片段语义检索代码",
		Available: func(d *toolspec.Deps) (bool, string) { return d.Repo != nil, "codeseek 未装配" },
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Repo, "Repo"); err != nil {
				return nil, err
			}
			return d.Repo.ExecuteSemanticSearch, nil
		},
	})

	toolspec.MustRegister(toolspec.Spec{
		Name:      "query_code_skeleton",
		Effect:    toolspec.EffectCodeSemantic,
		Frequency: toolspec.Cold,
		Trunc:     toolspec.TruncFirst,
		Brief:     "取文件骨架（只有定义，不含实现）",
		Available: func(d *toolspec.Deps) (bool, string) { return d.Repo != nil, "codeseek 未装配" },
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Repo, "Repo"); err != nil {
				return nil, err
			}
			return d.Repo.ExecuteQueryCodeSkeleton, nil
		},
	})

	toolspec.MustRegister(toolspec.Spec{
		Name:      "query_code_snippet",
		Effect:    toolspec.EffectCodeSemantic,
		Frequency: toolspec.Hot,
		Trunc:     toolspec.TruncFirst,
		Brief:     "取指定函数的实现片段",
		Available: func(d *toolspec.Deps) (bool, string) { return d.Repo != nil, "codeseek 未装配" },
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Repo, "Repo"); err != nil {
				return nil, err
			}
			return d.Repo.ExecuteQueryCodeSnippet, nil
		},
	})

	toolspec.MustRegister(toolspec.Spec{
		Name:      "find_function_callee",
		Effect:    toolspec.EffectCodeGraph,
		Frequency: toolspec.Cold,
		Trunc:     toolspec.TruncFirst,
		Brief:     "查某函数调用了谁（下游）",
		Available: func(d *toolspec.Deps) (bool, string) { return d.Repo != nil, "codeseek 未装配" },
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Repo, "Repo"); err != nil {
				return nil, err
			}
			return d.Repo.ExecuteFindFunctionCallees, nil
		},
	})

	// 改函数前必须查 callers —— 对 Coding/Repo 它是主力工具，画像里会 Promote 回 Hot。
	toolspec.MustRegister(toolspec.Spec{
		Name:      "find_function_caller",
		Effect:    toolspec.EffectCodeGraph,
		Frequency: toolspec.Cold,
		Trunc:     toolspec.TruncFirst,
		Brief:     "查谁调用了某函数（上游）",
		Available: func(d *toolspec.Deps) (bool, string) { return d.Repo != nil, "codeseek 未装配" },
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Repo, "Repo"); err != nil {
				return nil, err
			}
			return d.Repo.ExecuteFindFunctionCallers, nil
		},
	})

	toolspec.MustRegister(toolspec.Spec{
		Name:      "query_call_graph",
		Effect:    toolspec.EffectCodeGraph,
		Frequency: toolspec.Cold,
		Trunc:     toolspec.TruncFirst,
		Brief:     "以某函数为中心查上下游调用图",
		Available: func(d *toolspec.Deps) (bool, string) { return d.Repo != nil, "codeseek 未装配" },
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Repo, "Repo"); err != nil {
				return nil, err
			}
			return d.Repo.ExecuteCallGraph, nil
		},
	})
}
