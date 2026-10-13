package tools

import (
	"context"
	"encoding/json"

	"yagent/internal/toolspec"
)

// 认知族：隔离上下文的子 LLM 推理。无副作用，可与其他只读工具并发。
// deepthinking / design 的结果不可重取（一次分析可能几千 token），标受保护不被截断 ——
// 与历史 toolTruncationPriority 的 -1 一致。
func init() {
	toolspec.MustRegister(toolspec.Spec{
		Name:      "thinking",
		Effect:    toolspec.EffectCogReflect,
		Frequency: toolspec.Warm,
		ModelSlot: "thinking",
		Brief:     "失败或不确定时做自我纠错式推理",
		Available: func(d *toolspec.Deps) (bool, string) { return d.Thinker != nil, "反思工具未装配" },
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Thinker, "Thinker"); err != nil {
				return nil, err
			}
			thinker := d.Thinker
			// thinking 的执行器签名是 Call(input string)，其余工具是 map 参数；
			// 这层包装与历史各 Agent 里的 case 完全等价。
			return func(ctx context.Context, params map[string]interface{}) (interface{}, error) {
				inputBytes, err := json.Marshal(params)
				if err != nil {
					return nil, err
				}
				return thinker.Call(ctx, string(inputBytes))
			}, nil
		},
	})

	toolspec.MustRegister(toolspec.Spec{
		Name:      "micro_agent",
		Effect:    toolspec.EffectCogSubLLM,
		Frequency: toolspec.Warm,
		ModelSlot: "micro_agent",
		Brief:     "用自定义系统提示做一次隔离子 LLM 推理",
		Available: func(d *toolspec.Deps) (bool, string) { return d.Micro != nil, "micro_agent 未装配" },
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Micro, "Micro"); err != nil {
				return nil, err
			}
			return d.Micro.Execute, nil
		},
	})

	// deepthinking/design 按其自身描述就是"常规手段失败后再用"/"编码前一次性设计"，
	// 一次运行内期望调用 ≤1 次 → Cold。
	toolspec.MustRegister(toolspec.Spec{
		Name:      "deepthinking",
		Effect:    toolspec.EffectCogDeep,
		Frequency: toolspec.Cold,
		Trunc:     toolspec.TruncProtected,
		ModelSlot: "deepthinking",
		Brief:     "常规手段失败后的多维深度分析",
		Available: func(d *toolspec.Deps) (bool, string) { return d.Deep != nil, "deepthinking 未装配" },
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Deep, "Deep"); err != nil {
				return nil, err
			}
			return d.Deep.Execute, nil
		},
	})

	toolspec.MustRegister(toolspec.Spec{
		Name:      "design",
		Effect:    toolspec.EffectCogDesign,
		Frequency: toolspec.Cold,
		Trunc:     toolspec.TruncProtected,
		ModelSlot: "design",
		Brief:     "编码前产出结构化实施方案",
		Available: func(d *toolspec.Deps) (bool, string) { return d.Design != nil, "design 未装配" },
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			if err := require(d.Design, "Design"); err != nil {
				return nil, err
			}
			return d.Design.Execute, nil
		},
	})
}
