package tools

import (
	"yagent/internal/toolspec"
)

// use_skill：技能正文的读通道。
//
// Frequency=Hot：技能目录已经在 system prompt 里点名了它，让它自己再 Cold 一次
// 等于强迫模型先 load_tool 才能取技能（load_tool 自己必须 Hot 是同一个理由）。
// Available 刻意留 nil：没有技能时工具仍要在场并解释"放哪儿"，
// 而不是从工具面静默消失（read_blueprint 的同一决定，见 toolspec/deps.go）。
func init() {
	toolspec.MustRegister(toolspec.Spec{
		Name:      "use_skill",
		Effect:    toolspec.EffectSkill,
		Frequency: toolspec.Hot,
		Brief:     "取回一个技能的完整正文并按它执行",
		Display:   toolspec.DisplayFull,
		Build: func(d *toolspec.Deps) (toolspec.ToolFunc, error) {
			return newSkillExecutor(d.Skills), nil
		},
	})
}
